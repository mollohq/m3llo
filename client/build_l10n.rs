//! Build-time preparation of the translation catalogs
//! (specs/features/LOCALIZATION.md).
//!
//! `build.rs` bundles the folder that [`prepare`] writes under `OUT_DIR`:
//! a copy of each real `client/translations/<lang>/LC_MESSAGES/mello-client.po`
//! and, in debug builds only, the pseudo language `qps`, generated from
//! `client/translations/mello-client.pot`. The pseudo catalog is never
//! written to the source tree, and a release build never makes it.
//!
//! The PO reader is small and reads only what the pseudo language and the
//! lint need: `msgctxt`, `msgid` and `msgid_plural`. The Slint compiler reads
//! the real catalogs with its own parser.
//!
//! The client's tests include this file by path (`i18n_lint.rs`), so it has no
//! Cargo output of its own.

use std::path::{Path, PathBuf};

/// The gettext domain: the client's `CARGO_PKG_NAME`. Slint looks for
/// `<lang>/LC_MESSAGES/<domain>.po`.
pub const DOMAIN: &str = "mello-client";

/// The folder name of the pseudo language. It is the base of the Windows
/// pseudo locale `qps-ploc`, so no real OS locale selects it.
pub const PSEUDO: &str = "qps";

/// One message of a catalog.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub struct Message {
    /// `msgctxt`, or empty.
    pub context: String,
    pub id: String,
    /// `msgid_plural`, when the message has plural forms.
    pub plural: Option<String>,
}

/// Decode the body of one quoted PO string (without the quotes).
pub fn unescape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut chars = s.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next() {
            Some('n') => out.push('\n'),
            Some('t') => out.push('\t'),
            Some('r') => out.push('\r'),
            Some(other) => out.push(other),
            None => {}
        }
    }
    out
}

/// Encode `s` as one quoted PO string.
pub fn escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\t' => out.push_str("\\t"),
            '\r' => out.push_str("\\r"),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// The text of a quoted PO string line: `"abc"` gives `abc`.
fn quoted(line: &str) -> Option<String> {
    let l = line.trim();
    let inner = l.strip_prefix('"')?.strip_suffix('"')?;
    Some(unescape(inner))
}

/// The messages of a `.po` or `.pot` text, in file order. The header (empty
/// `msgid`) and obsolete entries (`#~`) are left out.
pub fn parse_po(text: &str) -> Vec<Message> {
    #[derive(Clone, Copy, PartialEq)]
    enum Field {
        None,
        Context,
        Id,
        Plural,
        Str,
    }
    let mut out = Vec::new();
    let mut context: Option<String> = None;
    let mut id: Option<String> = None;
    let mut plural: Option<String> = None;
    let mut field = Field::None;

    let mut flush =
        |context: &mut Option<String>, id: &mut Option<String>, plural: &mut Option<String>| {
            if let Some(i) = id.take() {
                if !i.is_empty() {
                    out.push(Message {
                        context: context.take().unwrap_or_default(),
                        id: i,
                        plural: plural.take(),
                    });
                }
            }
            *context = None;
            *plural = None;
        };

    for raw in text.lines() {
        let line = raw.trim_end_matches('\r').trim();
        if line.is_empty() || line.starts_with('#') {
            if line.is_empty() {
                field = Field::None;
            }
            continue;
        }
        let (keyword, rest) = match line.find(char::is_whitespace) {
            Some(n) if !line.starts_with('"') => (&line[..n], &line[n..]),
            _ => ("", line),
        };
        let value = quoted(rest).unwrap_or_default();
        match keyword {
            "msgctxt" => {
                flush(&mut context, &mut id, &mut plural);
                context = Some(value);
                field = Field::Context;
            }
            "msgid" => {
                if field != Field::Context {
                    flush(&mut context, &mut id, &mut plural);
                }
                id = Some(value);
                field = Field::Id;
            }
            "msgid_plural" => {
                plural = Some(value);
                field = Field::Plural;
            }
            k if k.starts_with("msgstr") => field = Field::Str,
            "" => match field {
                Field::Context => context.get_or_insert_with(String::new).push_str(&value),
                Field::Id => id.get_or_insert_with(String::new).push_str(&value),
                Field::Plural => plural.get_or_insert_with(String::new).push_str(&value),
                Field::Str | Field::None => {}
            },
            _ => {}
        }
    }
    flush(&mut context, &mut id, &mut plural);
    out
}

/// The accented form of an ASCII letter. Only Latin-1 and Latin
/// Extended-A letters, which the bundled fonts carry.
fn accent(c: char) -> char {
    match c {
        'a' => 'á',
        'c' => 'ç',
        'd' => 'ď',
        'e' => 'é',
        'g' => 'ğ',
        'i' => 'î',
        'l' => 'ľ',
        'n' => 'ñ',
        'o' => 'ö',
        'r' => 'ř',
        's' => 'š',
        't' => 'ť',
        'u' => 'ü',
        'y' => 'ý',
        'z' => 'ž',
        'A' => 'Å',
        'C' => 'Ç',
        'D' => 'Ď',
        'E' => 'É',
        'G' => 'Ğ',
        'I' => 'Î',
        'L' => 'Ľ',
        'N' => 'Ñ',
        'O' => 'Ö',
        'R' => 'Ř',
        'S' => 'Š',
        'T' => 'Ť',
        'U' => 'Ü',
        'Y' => 'Ý',
        'Z' => 'Ž',
        c => c,
    }
}

/// The pseudo translation of one string: each letter accented, and about
/// 30 % more length as ` ~~~` at the end. A place (`{}`, `{0}`, `{n}`) and
/// an escaped brace (`{{`, `}}`) stay as they are.
///
/// A string with no letter outside its places is a pattern or a mark
/// (`{0} {1}`, `{0}:{1}`, `,`). It stays as it is: the padding would put
/// ` ~` into each date, time and number.
pub fn pseudo(s: &str) -> String {
    let chars: Vec<char> = s.chars().collect();
    if !has_letter(&chars) {
        return s.to_string();
    }
    let mut out = String::with_capacity(s.len() * 2 + 8);
    let mut visible = 0usize;
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        if (c == '{' || c == '}') && chars.get(i + 1) == Some(&c) {
            out.push(c);
            out.push(c);
            i += 2;
            continue;
        }
        if c == '{' {
            if let Some(end) = chars[i..].iter().position(|&x| x == '}') {
                out.extend(&chars[i..=i + end]);
                i += end + 1;
                continue;
            }
        }
        out.push(accent(c));
        visible += 1;
        i += 1;
    }
    // ceil(30 %), at least a space and one tilde.
    let pad = (visible * 3).div_ceil(10).max(2);
    out.push(' ');
    out.extend(std::iter::repeat_n('~', pad - 1));
    out
}

/// Whether `chars` has a letter outside its places and escaped braces.
fn has_letter(chars: &[char]) -> bool {
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        if (c == '{' || c == '}') && chars.get(i + 1) == Some(&c) {
            i += 2;
            continue;
        }
        if c == '{' {
            if let Some(end) = chars[i..].iter().position(|&x| x == '}') {
                i += end + 1;
                continue;
            }
        }
        if c.is_alphabetic() {
            return true;
        }
        i += 1;
    }
    false
}

/// The `.po` text of the pseudo language for these messages.
pub fn pseudo_po(messages: &[Message]) -> String {
    let mut out = String::new();
    out.push_str("# Generated by client/build_l10n.rs from mello-client.pot. Do not edit.\n");
    out.push_str("msgid \"\"\nmsgstr \"\"\n");
    out.push_str("\"Content-Type: text/plain; charset=UTF-8\\n\"\n");
    out.push_str(&format!("\"Language: {PSEUDO}\\n\"\n"));
    out.push_str("\"Plural-Forms: nplurals=2; plural=(n != 1);\\n\"\n");
    for m in messages {
        out.push('\n');
        if !m.context.is_empty() {
            out.push_str(&format!("msgctxt {}\n", escape(&m.context)));
        }
        out.push_str(&format!("msgid {}\n", escape(&m.id)));
        match &m.plural {
            Some(p) => {
                out.push_str(&format!("msgid_plural {}\n", escape(p)));
                out.push_str(&format!("msgstr[0] {}\n", escape(&pseudo(&m.id))));
                out.push_str(&format!("msgstr[1] {}\n", escape(&pseudo(p))));
            }
            None => out.push_str(&format!("msgstr {}\n", escape(&pseudo(&m.id)))),
        }
    }
    out
}

/// The real languages in `source`: each folder with
/// `LC_MESSAGES/mello-client.po`, sorted by name.
pub fn real_languages(source: &Path) -> Vec<String> {
    let Ok(entries) = std::fs::read_dir(source) else {
        return Vec::new();
    };
    let mut out: Vec<String> = entries
        .flatten()
        .filter(|e| catalog(&e.path()).is_file())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    out.sort();
    out
}

fn catalog(lang_dir: &Path) -> PathBuf {
    lang_dir.join("LC_MESSAGES").join(format!("{DOMAIN}.po"))
}

/// Write `contents` to `path` only when the file differs. Slint names the
/// bundle folder as a build input, and Cargo runs the build script again
/// when an input is newer than the last run, so an unchanged file keeps its
/// time.
fn write_if_changed(path: &Path, contents: &[u8]) -> std::io::Result<()> {
    if std::fs::read(path).is_ok_and(|old| old == contents) {
        return Ok(());
    }
    std::fs::create_dir_all(path.parent().expect("catalog path has a parent"))?;
    std::fs::write(path, contents)
}

/// Write the bundle folder `<out_dir>/translations` from `source`
/// (`client/translations`): a copy of each real catalog, and the pseudo
/// language when `with_pseudo` is set and the `.pot` exists. A language
/// folder from an earlier run that is not in the set is removed.
///
/// Returns the folder and its languages, or `None` when it has no language.
/// Slint stops the build when a bundle folder has no catalog, so the caller
/// then bundles nothing and the UI is English.
pub fn prepare(
    source: &Path,
    out_dir: &Path,
    with_pseudo: bool,
) -> std::io::Result<Option<(PathBuf, Vec<String>)>> {
    let bundle = out_dir.join("translations");
    let mut languages = Vec::new();
    for lang in real_languages(source) {
        if lang == PSEUDO {
            // A hand-made qps catalog would replace the generated one.
            return Err(std::io::Error::other(format!(
                "{} must not exist: the pseudo language is generated",
                catalog(&source.join(PSEUDO)).display()
            )));
        }
        let text = std::fs::read(catalog(&source.join(&lang)))?;
        write_if_changed(&catalog(&bundle.join(&lang)), &text)?;
        languages.push(lang);
    }
    let pot = source.join(format!("{DOMAIN}.pot"));
    if with_pseudo && pot.is_file() {
        let messages = parse_po(&std::fs::read_to_string(&pot)?);
        write_if_changed(
            &catalog(&bundle.join(PSEUDO)),
            pseudo_po(&messages).as_bytes(),
        )?;
        languages.push(PSEUDO.to_string());
    }
    languages.sort();
    if let Ok(entries) = std::fs::read_dir(&bundle) {
        for e in entries.flatten() {
            let name = e.file_name().to_string_lossy().into_owned();
            if !languages.contains(&name) {
                std::fs::remove_dir_all(e.path())?;
            }
        }
    }
    Ok((!languages.is_empty()).then_some((bundle, languages)))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pseudo_accents_letters_and_adds_about_30_percent() {
        assert_eq!(pseudo("Create crew"), "Çřéáťé çřéw ~~~");
        assert_eq!(pseudo("OK"), "ÖK ~");
    }

    #[test]
    fn pseudo_keeps_a_pattern_or_a_mark_with_no_letter() {
        for kept in [
            "{0} {1}",
            "{0}:{1} {2}",
            "{1} {0}, {2}",
            ",",
            "",
            "{}%",
            "{n}",
        ] {
            assert_eq!(pseudo(kept), kept);
        }
        assert_eq!(pseudo("{}h"), "{}h ~");
        assert_eq!(pseudo("{0} to {1} {2}"), "{0} ťö {1} {2} ~");
    }

    #[test]
    fn pseudo_keeps_places_and_escaped_braces() {
        assert_eq!(pseudo("{} / {} MB"), "{} / {} MB ~");
        let p = pseudo("Added by {0} and {n}, {{x}}");
        assert!(p.starts_with("Åďďéď bý {0} áñď {n}, {{x}}"), "{p}");
        for kept in ["{0}", "{n}", "{{", "}}"] {
            assert!(p.contains(kept), "{kept} in {p}");
        }
    }

    #[test]
    fn parse_reads_context_plural_and_continued_lines() {
        let pot = "msgid \"\"\nmsgstr \"\"\n\"Content-Type: text/plain\n\"\n\n#: a.slint\nmsgctxt \"verb\"\nmsgid \"Open\"\nmsgstr \"\"\n\nmsgid \"\"\n\"Long \"\n\"line \\\"q\\\"\"\nmsgstr \"\"\n\nmsgid \"{n} clip\"\nmsgid_plural \"{n} clips\"\nmsgstr[0] \"\"\nmsgstr[1] \"\"\n\n#~ msgid \"Old\"\n#~ msgstr \"\"\n";
        assert_eq!(
            parse_po(pot),
            vec![
                Message {
                    context: "verb".into(),
                    id: "Open".into(),
                    plural: None
                },
                Message {
                    context: String::new(),
                    id: "Long line \"q\"".into(),
                    plural: None
                },
                Message {
                    context: String::new(),
                    id: "{n} clip".into(),
                    plural: Some("{n} clips".into()),
                },
            ]
        );
    }

    #[test]
    fn the_pseudo_catalog_round_trips() {
        let messages = vec![
            Message {
                context: "verb".into(),
                id: "Say \"hi\"\n".into(),
                plural: None,
            },
            Message {
                context: String::new(),
                id: "{n} clip".into(),
                plural: Some("{n} clips".into()),
            },
        ];
        let po = pseudo_po(&messages);
        assert_eq!(parse_po(&po), messages);
        assert!(po.contains("msgstr[1] \"{n} çľîpš ~\""), "{po}");
        assert!(po.contains("Plural-Forms: nplurals=2; plural=(n != 1);"));
    }
}
