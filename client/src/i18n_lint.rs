//! Every user-visible string in the UI goes through `@tr()`
//! (specs/features/LOCALIZATION.md).
//!
//! The rule, checked statically over every `.slint` file under `client/ui`
//! and `hud/ui` (not `previews/`): a string literal with a letter in it is
//! inside `@tr(...)`, unless it is one of these:
//!
//! - an import path (`from "x.slint"`) or an `@image-url("...")`;
//! - the value of a property whose name has `font` in it (a font family),
//!   or of `commands` (the drawing commands of a `Path`);
//! - an operand of `==` or `!=` (an id or an enum-like value);
//! - a lower-case word with no space (`"line"`, `"voice_activity"`): an id;
//! - in a component whose name starts or ends with `Preview` (mock data);
//! - on a line, or under a line, with `// i18n: none (<reason>)`;
//! - in a file with `// i18n: none-file (<reason>)` in its first ten lines
//!   (a developer tool). A file marker with no reason is an error.
//!
//! The UI is converted file by file. `i18n_pending.txt` lists the files that
//! are not converted yet, with their count of untranslated strings. The
//! test fails when a file that is not on the list has one, and when an
//! entry is stale (the count went down). So the list can only shrink.
//!
//! A second test compares the messages of `mello-client.pot` with the
//! `@tr()` calls of the sources, so a stale `.pot` fails `cargo test`
//! without the extractor.

use std::collections::{BTreeMap, BTreeSet};
use std::ops::Range;
use std::path::{Path, PathBuf};

use crate::build_l10n::{parse_po, Message};
use crate::slint_source::{scan, slint_files};

const PENDING: &str = include_str!("i18n_pending.txt");

/// An untranslated string literal.
#[derive(Debug, PartialEq)]
struct Finding {
    line: usize,
    text: String,
}

/// The text of a literal without its quotes, with `\{ ... }` interpolations
/// removed.
fn literal_text(src: &str, r: &Range<usize>) -> String {
    let inner = &src[r.start + 1..r.end.saturating_sub(1).max(r.start + 1)];
    let mut out = String::new();
    let mut chars = inner.chars().peekable();
    while let Some(c) = chars.next() {
        if c == '\\' && chars.peek() == Some(&'{') {
            chars.next();
            let mut depth = 1;
            for c in chars.by_ref() {
                match c {
                    '{' => depth += 1,
                    '}' => {
                        depth -= 1;
                        if depth == 0 {
                            break;
                        }
                    }
                    _ => {}
                }
            }
            continue;
        }
        out.push(c);
    }
    out
}

/// The byte ranges of each `@tr( ... )` call, parentheses included.
fn tr_calls(masked: &str) -> Vec<Range<usize>> {
    let b = masked.as_bytes();
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(n) = masked[from..].find("@tr") {
        let at = from + n;
        from = at + 3;
        let Some(open) = masked[from..].find(|c: char| !c.is_whitespace()) else {
            break;
        };
        let open = from + open;
        if b[open] != b'(' {
            continue;
        }
        let mut depth = 0;
        let mut close = masked.len();
        for (i, &c) in b.iter().enumerate().skip(open) {
            match c {
                b'(' => depth += 1,
                b')' => {
                    depth -= 1;
                    if depth == 0 {
                        close = i + 1;
                        break;
                    }
                }
                _ => {}
            }
        }
        out.push(at..close);
    }
    out
}

/// The name of the component (or global, struct) that contains `pos`.
fn enclosing_component(masked: &str, pos: usize) -> &str {
    let head = &masked[..pos];
    let mut best: Option<(usize, &str)> = None;
    for kw in ["component ", "global ", "struct "] {
        let mut from = 0;
        while let Some(n) = head[from..].find(kw) {
            let at = from + n;
            from = at + kw.len();
            let boundary = at == 0 || !head.as_bytes()[at - 1].is_ascii_alphanumeric();
            if boundary && best.is_none_or(|(b, _)| at > b) {
                let rest = head[from..].trim_start();
                let end = rest
                    .find(|c: char| !(c.is_alphanumeric() || c == '-' || c == '_'))
                    .unwrap_or(rest.len());
                best = Some((at, &rest[..end]));
            }
        }
    }
    best.map_or("", |(_, name)| name)
}

/// A lower-case word with no space: an id, a tone, a mode.
fn id_like(text: &str) -> bool {
    !text.is_empty()
        && text
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || "_-./:#".contains(c))
}

fn line_at(masked: &str, pos: usize) -> usize {
    masked[..pos].matches('\n').count() + 1
}

/// The marker that exempts a whole file, in one of its first
/// [`FILE_MARKER_LINES`] lines: `// i18n: none-file (<reason>)`.
const FILE_MARKER: &str = "i18n: none-file";
const FILE_MARKER_LINES: usize = 10;

/// `Ok(true)` when the first lines of the file have the file marker with a
/// reason. `Err(line)` when the marker has no reason.
fn file_exempt(src: &str) -> Result<bool, usize> {
    for (i, line) in src.lines().take(FILE_MARKER_LINES).enumerate() {
        let Some(comment) = line.find("//").map(|n| &line[n + 2..]) else {
            continue;
        };
        let Some(at) = comment.find(FILE_MARKER) else {
            continue;
        };
        let rest = comment[at + FILE_MARKER.len()..].trim();
        let reason = rest
            .strip_prefix('(')
            .and_then(|r| r.strip_suffix(')'))
            .map(str::trim);
        return match reason {
            Some(r) if !r.is_empty() => Ok(true),
            _ => Err(i + 1),
        };
    }
    Ok(false)
}

fn check_source(src: &str) -> Vec<Finding> {
    let scanned = scan(src);
    let masked = &scanned.masked;
    let calls = tr_calls(masked);
    let lines: Vec<&str> = masked.lines().collect();
    let mut out = Vec::new();
    for r in &scanned.strings {
        let text = literal_text(src, r);
        if !text.chars().any(char::is_alphabetic) {
            continue;
        }
        if calls.iter().any(|c| c.contains(&r.start)) {
            continue;
        }
        let before = masked[..r.start].trim_end();
        let after = masked[r.end..].trim_start();
        if before.ends_with("from")
            || before.ends_with("import")
            || before.ends_with("@image-url(")
            || before.ends_with("debug(")
        {
            continue;
        }
        if before.ends_with("==")
            || before.ends_with("!=")
            || after.starts_with("==")
            || after.starts_with("!=")
        {
            continue;
        }
        let statement = before
            .rfind([';', '{', '}'])
            .map_or(before, |i| &before[i + 1..]);
        let property = statement.split(':').next().unwrap_or("").trim();
        if property.contains("font") || property == "commands" {
            continue;
        }
        if id_like(&text) {
            continue;
        }
        let component = enclosing_component(masked, r.start);
        if component.starts_with("Preview") || component.ends_with("Preview") {
            continue;
        }
        let line = line_at(masked, r.start);
        let marked =
            |l: usize| l >= 1 && lines.get(l - 1).is_some_and(|t| t.contains("i18n: none"));
        if marked(line) || marked(line - 1) {
            continue;
        }
        out.push(Finding { line, text });
    }
    out
}

/// The repository root: the parent of `client/`.
fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("client/ has a parent")
        .to_path_buf()
}

/// The `.slint` files the extractor reads, as `client/ui/...` paths.
fn ui_files() -> Vec<(String, PathBuf)> {
    let root = repo_root();
    let mut files = Vec::new();
    for dir in ["client/ui", "hud/ui"] {
        slint_files(&root.join(dir), &["previews"], &mut files);
    }
    let mut out: Vec<(String, PathBuf)> = files
        .into_iter()
        .map(|p| {
            let rel = p
                .strip_prefix(&root)
                .unwrap_or(&p)
                .to_string_lossy()
                .replace('\\', "/");
            (rel, p)
        })
        .collect();
    out.sort();
    out
}

/// `i18n_pending.txt`: `<count> <path>` per line; `#` starts a comment.
fn pending(text: &str) -> BTreeMap<String, usize> {
    text.lines()
        .map(str::trim)
        .filter(|l| !l.is_empty() && !l.starts_with('#'))
        .map(|l| {
            let (n, path) = l.split_once(' ').expect("`<count> <path>` per line");
            (
                path.trim().to_string(),
                n.parse().expect("the count is a number"),
            )
        })
        .collect()
}

/// The ratchet: the problems of `found` (untranslated strings per file)
/// against the list.
fn ratchet(found: &BTreeMap<String, Vec<Finding>>, list: &BTreeMap<String, usize>) -> Vec<String> {
    let mut problems = Vec::new();
    for (file, findings) in found {
        let n = findings.len();
        match list.get(file) {
            None if n > 0 => {
                problems.push(format!(
                    "{file}: {n} string(s) not in @tr() (the file is converted):"
                ));
                for f in findings {
                    problems.push(format!("    {file}:{}  {:?}", f.line, f.text));
                }
            }
            Some(&want) if n > want => {
                problems.push(format!(
                    "{file}: {n} string(s) not in @tr(), the list allows {want}. Put the new string in @tr():"
                ));
                for f in findings {
                    problems.push(format!("    {file}:{}  {:?}", f.line, f.text));
                }
            }
            Some(&want) if n < want => problems.push(format!(
                "{file}: the list says {want}, the file has {n}. Write {n} in i18n_pending.txt{}",
                if n == 0 { " (delete the line)" } else { "" }
            )),
            _ => {}
        }
    }
    for file in list.keys() {
        if !found.contains_key(file) {
            problems.push(format!(
                "{file}: on the list, but no such file. Delete the line."
            ));
        }
    }
    problems
}

#[test]
fn every_user_visible_string_is_translated() {
    let files = ui_files();
    assert!(files.len() > 50, "too few .slint files: {}", files.len());
    let mut problems = Vec::new();
    let mut found: BTreeMap<String, Vec<Finding>> = BTreeMap::new();
    for (rel, p) in &files {
        let src = std::fs::read_to_string(p).expect("read .slint file");
        let findings = match file_exempt(&src) {
            Ok(true) => Vec::new(),
            Ok(false) => check_source(&src),
            Err(line) => {
                problems.push(format!(
                    "{rel}:{line}: `// {FILE_MARKER}` needs a reason: `// {FILE_MARKER} (<reason>)`"
                ));
                check_source(&src)
            }
        };
        found.insert(rel.clone(), findings);
    }
    problems.extend(ratchet(&found, &pending(PENDING)));
    assert!(
        problems.is_empty(),
        "Untranslated UI strings (specs/features/LOCALIZATION.md). Put a user-visible \
         string in @tr(). A string that is not user-visible gets \
         `// i18n: none (<reason>)` on its line or the line above. The list of files \
         not converted yet is client/src/i18n_pending.txt; it can only shrink.\n{}",
        problems.join("\n")
    );
}

// --- The .pot against the sources ---------------------------------------

/// Decode a Slint string literal body.
fn unescape_slint(s: &str) -> String {
    let mut out = String::new();
    let mut chars = s.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            Some(other) => out.push(other),
            None => {}
        }
    }
    out
}

/// The messages of the `@tr()` calls in `src`.
fn tr_messages(src: &str) -> Vec<Message> {
    let scanned = scan(src);
    let masked = &scanned.masked;
    let mut out = Vec::new();
    for call in tr_calls(masked) {
        let strings: Vec<&Range<usize>> = scanned
            .strings
            .iter()
            .filter(|r| call.contains(&r.start))
            .collect();
        let Some(first) = strings.first() else {
            continue;
        };
        let text = |r: &Range<usize>| unescape_slint(&src[r.start + 1..r.end - 1]);
        let next_token = |r: &Range<usize>| masked[r.end..].trim_start().to_string();
        let (context, id_range, rest) = if next_token(first).starts_with("=>") {
            match strings.get(1) {
                Some(id) => (text(first), *id, &strings[2..]),
                None => continue,
            }
        } else {
            (String::new(), *first, &strings[1..])
        };
        let plural = (next_token(id_range).starts_with('|'))
            .then(|| rest.first().map(|r| text(r)))
            .flatten();
        out.push(Message {
            context,
            id: text(id_range),
            plural,
        });
    }
    out
}

#[test]
fn the_pot_has_the_messages_of_the_sources() {
    let mut sources = BTreeSet::new();
    for (_, p) in ui_files() {
        let src = std::fs::read_to_string(&p).expect("read .slint file");
        sources.extend(tr_messages(&src));
    }
    let pot_path = repo_root().join("client/translations/mello-client.pot");
    let pot: BTreeSet<Message> =
        parse_po(&std::fs::read_to_string(&pot_path).expect("read mello-client.pot"))
            .into_iter()
            .collect();
    let missing: Vec<_> = sources.difference(&pot).collect();
    let extra: Vec<_> = pot.difference(&sources).collect();
    assert!(
        missing.is_empty() && extra.is_empty(),
        "client/translations/mello-client.pot is out of date. Run ./scripts/l10n-extract.sh.\n\
         Not in the .pot: {missing:#?}\nNot in the sources: {extra:#?}"
    );
}

#[cfg(test)]
mod tests {
    use super::*;

    fn texts(src: &str) -> Vec<String> {
        check_source(src).into_iter().map(|f| f.text).collect()
    }

    #[test]
    fn a_bare_string_is_reported_and_a_translated_one_is_not() {
        let src = "export component A inherits Rectangle {\n    Text { text: \"Join crew\"; }\n    Text { text: @tr(\"Join crew\"); }\n    Text { text: @tr(\"Hi {}\", \"name\"); }\n}\n";
        assert_eq!(texts(src), vec!["Join crew".to_string()]);
        assert_eq!(check_source(src)[0].line, 2);
    }

    #[test]
    fn labels_tooltips_and_placeholders_are_reported() {
        let src = "Rectangle {\n    accessible-label: \"Mute\";\n    placeholder: \"Search\";\n    tooltip: root.on ? \"On\" : \"Off\";\n}\n";
        assert_eq!(texts(src), ["Mute", "Search", "On", "Off"]);
    }

    #[test]
    fn a_joined_sentence_is_reported() {
        let src = "Text { text: \"Added by \" + name; }\n";
        assert_eq!(texts(src), ["Added by "]);
    }

    #[test]
    fn non_visible_strings_are_not_reported() {
        let src = "import { Theme } from \"../theme.slint\";\n\
            export component A inherits Rectangle {\n\
                in property <string> font-sans: \"Barlow\";\n\
                Image { source: @image-url(\"../icons/mic.svg\"); }\n\
                Text { font-family: \"Oxanium\"; text: \"0%\"; }\n\
                Text { text: \"\\{n}\"; }\n\
                if root.mode == \"Voice Activity\": Rectangle { }\n\
                ZoneButton { tone: \"line\"; key: \"voice_activity\"; }\n\
                Text { text: \"M3LLO\"; } // i18n: none (the brand name)\n\
                // i18n: none (a key of the e2e driver)\n\
                Text { text: \"Debug Key\"; }\n\
            }\n";
        assert!(texts(src).is_empty(), "{:?}", texts(src));
    }

    #[test]
    fn mock_data_in_a_preview_is_not_reported() {
        let src = "export component Preview inherits Rectangle {\n    Text { text: \"Ida Rage\"; }\n}\ncomponent B inherits Rectangle {\n    Text { text: \"Real text\"; }\n}\n";
        assert_eq!(texts(src), ["Real text"]);
    }

    #[test]
    fn an_interpolated_sentence_is_reported() {
        let src = "Text { text: \"\\{n} clips\"; }\n";
        assert_eq!(texts(src), [" clips"]);
    }

    #[test]
    fn strings_in_comments_are_not_reported() {
        let src = "Rectangle {\n    // Text { text: \"Old label\"; }\n    /* \"Other\" */\n}\n";
        assert!(texts(src).is_empty());
    }

    #[test]
    fn the_file_marker_exempts_the_whole_file() {
        let body =
            "export component A inherits Rectangle {\n    Text { text: \"Frame time\"; }\n}\n";
        assert_eq!(file_exempt(body), Ok(false));
        assert_eq!(texts(body), ["Frame time"]);

        let marked = format!("// i18n: none-file (developer panel)\n{body}");
        assert_eq!(file_exempt(&marked), Ok(true));

        let below = format!("import {{ Theme }} from \"theme.slint\";\n\n// i18n: none-file (developer panel)\n{body}");
        assert_eq!(file_exempt(&below), Ok(true));
    }

    #[test]
    fn a_file_marker_with_no_reason_is_an_error() {
        let body = "Text { text: \"Frame time\"; }\n";
        assert_eq!(file_exempt(&format!("// i18n: none-file\n{body}")), Err(1));
        assert_eq!(
            file_exempt(&format!("\n// i18n: none-file ()\n{body}")),
            Err(2)
        );
    }

    #[test]
    fn a_file_marker_far_below_the_top_has_no_effect() {
        let src = format!(
            "{}// i18n: none-file (too late)\nText {{ text: \"Frame time\"; }}\n",
            "\n".repeat(FILE_MARKER_LINES)
        );
        assert_eq!(file_exempt(&src), Ok(false));
    }

    #[test]
    fn an_empty_list_has_no_entries() {
        let list = pending("# comment\n# Format: <count> <path>\n\n");
        assert!(list.is_empty());
        assert!(ratchet(&found(&[("a.slint", 0)]), &list).is_empty());
        assert_eq!(ratchet(&found(&[("a.slint", 1)]), &list).len(), 2);
    }

    fn found(entries: &[(&str, usize)]) -> BTreeMap<String, Vec<Finding>> {
        entries
            .iter()
            .map(|(f, n)| {
                let v = (0..*n)
                    .map(|i| Finding {
                        line: i + 1,
                        text: "x".into(),
                    })
                    .collect();
                (f.to_string(), v)
            })
            .collect()
    }

    #[test]
    fn the_ratchet_only_shrinks() {
        let list = pending("# comment\n3 a.slint\n2 b.slint\n");
        // As listed, and a converted file that is clean.
        assert!(ratchet(
            &found(&[("a.slint", 3), ("b.slint", 2), ("c.slint", 0)]),
            &list
        )
        .is_empty());
        // A new string in a converted file.
        assert_eq!(
            ratchet(
                &found(&[("a.slint", 3), ("b.slint", 2), ("c.slint", 1)]),
                &list
            )
            .len(),
            2
        );
        // A new string in a listed file.
        assert!(!ratchet(&found(&[("a.slint", 4), ("b.slint", 2)]), &list).is_empty());
        // Fewer strings than listed: the entry is stale.
        assert_eq!(
            ratchet(&found(&[("a.slint", 2), ("b.slint", 2)]), &list).len(),
            1
        );
        // A listed file that is clean: stale too.
        assert_eq!(
            ratchet(&found(&[("a.slint", 3), ("b.slint", 0)]), &list).len(),
            1
        );
        // A listed file that does not exist.
        assert_eq!(ratchet(&found(&[("a.slint", 3)]), &list).len(), 1);
    }

    #[test]
    fn tr_calls_give_their_messages() {
        let src = "Text { text: @tr(\"verb\" => \"Open\"); }\nText { text: @tr(\"{n} clip\" | \"{n} clips\" % n); }\nText { text: @tr(\"Hi {}\", \"x\"); }\nText { text: @tr(\"Say \\\"hi\\\"\"); }\n";
        let m = tr_messages(src);
        assert_eq!(
            m,
            vec![
                Message {
                    context: "verb".into(),
                    id: "Open".into(),
                    plural: None
                },
                Message {
                    context: String::new(),
                    id: "{n} clip".into(),
                    plural: Some("{n} clips".into())
                },
                Message {
                    context: String::new(),
                    id: "Hi {}".into(),
                    plural: None
                },
                Message {
                    context: String::new(),
                    id: "Say \"hi\"".into(),
                    plural: None
                },
            ]
        );
    }
}
