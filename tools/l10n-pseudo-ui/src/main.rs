//! Write a copy of the client UI in the pseudo language.
//!
//! `slint-viewer` has no option to select a translation. This tool copies
//! `client/ui` and `hud/ui` to an output folder and replaces the text of each
//! `@tr()` call with its pseudo form (`client/build_l10n.rs`, `pseudo`). Then
//! `slint-viewer --screenshot` on the copy shows the layout with longer,
//! accented text. All other bytes stay the same, so the relative imports and
//! the preview fixtures work in the copy.
//!
//! The forms that change:
//! - `@tr("text", …)`: the text.
//! - `@tr("ctx" => "text", …)`: the text. The context stays.
//! - `@tr("one" | "many" % n, …)`: the two forms.
//!
//! A `//` or `/* */` comment and a string that is not in `@tr()` stay as
//! they are. A literal that has an escape the tool does not know stays too.
//!
//! Usage: `l10n-pseudo-ui <out-dir> [<repo-root>]`. The script
//! `scripts/l10n-pseudo-shots.sh` runs it and renders the previews
//! (specs/features/LOCALIZATION.md, "The pseudo render").

#[allow(dead_code)] // the tool uses only `pseudo`
#[path = "../../../client/build_l10n.rs"]
mod build_l10n;

use std::path::{Path, PathBuf};
use std::process::ExitCode;

/// The UI folders that the tool copies, relative to the repository root.
const UI_DIRS: [&str; 2] = ["client/ui", "hud/ui"];

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let (out, root) = match args.as_slice() {
        [out] => (PathBuf::from(out), default_root()),
        [out, root] => (PathBuf::from(out), PathBuf::from(root)),
        _ => {
            eprintln!("usage: l10n-pseudo-ui <out-dir> [<repo-root>]");
            return ExitCode::from(2);
        }
    };
    match run(&root, &out) {
        Ok((files, texts)) => {
            println!(
                "l10n-pseudo-ui: wrote {} ({files} .slint files, {texts} texts)",
                out.display()
            );
            ExitCode::SUCCESS
        }
        Err(e) => {
            eprintln!("l10n-pseudo-ui: {e}");
            ExitCode::FAILURE
        }
    }
}

/// The repository root: two folders above this crate.
fn default_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

/// Copy each UI folder of `root` to `out`. Returns the number of `.slint`
/// files and the number of texts that changed.
fn run(root: &Path, out: &Path) -> std::io::Result<(usize, usize)> {
    let mut files = 0;
    let mut texts = 0;
    for dir in UI_DIRS {
        let src = root.join(dir);
        let dst = out.join(dir);
        if dst.exists() {
            std::fs::remove_dir_all(&dst)?;
        }
        copy_tree(&src, &dst, &mut files, &mut texts)?;
    }
    Ok((files, texts))
}

fn copy_tree(src: &Path, dst: &Path, files: &mut usize, texts: &mut usize) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    let mut entries: Vec<_> = std::fs::read_dir(src)?.flatten().collect();
    entries.sort_by_key(|e| e.file_name());
    for e in entries {
        let from = e.path();
        let to = dst.join(e.file_name());
        if e.file_type()?.is_dir() {
            copy_tree(&from, &to, files, texts)?;
        } else if from.extension().is_some_and(|x| x == "slint") {
            let text = std::fs::read_to_string(&from)?;
            let (rewritten, count) = rewrite(&text);
            std::fs::write(&to, rewritten)?;
            *files += 1;
            *texts += count;
        } else {
            std::fs::copy(&from, &to)?;
        }
    }
    Ok(())
}

/// The pseudo form of a `.slint` source, and the number of texts that
/// changed.
pub fn rewrite(src: &str) -> (String, usize) {
    let mut r = Rewriter {
        src: src.chars().collect(),
        i: 0,
        out: String::with_capacity(src.len() * 2),
        count: 0,
    };
    while r.i < r.src.len() {
        // An unmatched closer at the top level: copy it and go on.
        if r.code(false).is_some() {
            r.copy_one();
        }
    }
    (r.out, r.count)
}

struct Rewriter {
    src: Vec<char>,
    i: usize,
    out: String,
    count: usize,
}

impl Rewriter {
    fn at(&self, offset: usize) -> Option<char> {
        self.src.get(self.i + offset).copied()
    }

    fn starts_with(&self, s: &str) -> bool {
        s.chars().enumerate().all(|(n, c)| self.at(n) == Some(c))
    }

    fn copy_one(&mut self) {
        if let Some(c) = self.at(0) {
            self.out.push(c);
            self.i += 1;
        }
    }

    fn copy_str(&mut self, s: &str) {
        self.out.push_str(s);
        self.i += s.chars().count();
    }

    /// Copy code up to an unmatched `)`, `]` or `}`, or, when `stop_at_comma`
    /// is set, a `,` at the same depth. The stop character is not copied; it
    /// is returned. `None` at the end of the source.
    fn code(&mut self, stop_at_comma: bool) -> Option<char> {
        let mut depth = 0usize;
        while let Some(c) = self.at(0) {
            if self.starts_with("//") {
                while self.at(0).is_some_and(|c| c != '\n') {
                    self.copy_one();
                }
            } else if self.starts_with("/*") {
                self.copy_str("/*");
                while self.at(0).is_some() && !self.starts_with("*/") {
                    self.copy_one();
                }
                if self.starts_with("*/") {
                    self.copy_str("*/");
                }
            } else if c == '"' {
                self.string_verbatim();
            } else if self.starts_with("@tr(") {
                self.copy_str("@tr(");
                self.tr_call();
            } else if matches!(c, '(' | '[' | '{') {
                depth += 1;
                self.copy_one();
            } else if matches!(c, ')' | ']' | '}') {
                if depth == 0 {
                    return Some(c);
                }
                depth -= 1;
                self.copy_one();
            } else if c == ',' && depth == 0 && stop_at_comma {
                return Some(c);
            } else {
                self.copy_one();
            }
        }
        None
    }

    /// Copy a string literal that is not a `@tr()` text. A `\{…}` place in
    /// it holds code, which can hold `@tr()`.
    fn string_verbatim(&mut self) {
        self.copy_one(); // the opening quote
        while let Some(c) = self.at(0) {
            match c {
                '\\' => {
                    self.copy_one();
                    if self.at(0) == Some('{') {
                        self.copy_one();
                        if self.code(false) == Some('}') {
                            self.copy_one();
                        }
                    } else {
                        self.copy_one();
                    }
                }
                '"' => {
                    self.copy_one();
                    return;
                }
                _ => self.copy_one(),
            }
        }
    }

    /// The arguments of a `@tr(` call, up to and with its `)`.
    fn tr_call(&mut self) {
        loop {
            while self.at(0).is_some_and(char::is_whitespace) {
                self.copy_one();
            }
            match self.at(0) {
                None => return,
                Some('"') => self.tr_literal(),
                Some('|') => self.copy_one(),
                Some('=') if self.at(1) == Some('>') => self.copy_str("=>"),
                Some(')') => {
                    self.copy_one();
                    return;
                }
                Some('%') => {
                    self.copy_one();
                    if !matches!(self.code(true), Some(',' | ')')) {
                        return;
                    }
                }
                Some(',') => {
                    self.copy_one();
                    if self.code(false) != Some(')') {
                        return;
                    }
                }
                Some(_) => {
                    // Not a form the tool knows: copy it as code.
                    if !matches!(self.code(true), Some(',' | ')')) {
                        return;
                    }
                }
            }
        }
    }

    /// One literal in the message part of a `@tr()` call. A context (the
    /// literal before `=>`) stays; a text gets its pseudo form.
    fn tr_literal(&mut self) {
        let start = self.i;
        let mut end = start + 1;
        while end < self.src.len() && self.src[end] != '"' {
            end += if self.src[end] == '\\' { 2 } else { 1 };
        }
        let end = (end + 1).min(self.src.len());
        let raw: String = self.src[start..end].iter().collect();

        let mut next = end;
        while next < self.src.len() && self.src[next].is_whitespace() {
            next += 1;
        }
        let is_context = self.src.get(next) == Some(&'=') && self.src.get(next + 1) == Some(&'>');

        let body = raw
            .strip_prefix('"')
            .and_then(|r| r.strip_suffix('"'))
            .filter(|_| raw.len() >= 2);
        match body.and_then(decode).filter(|_| !is_context) {
            Some(text) => {
                self.out.push_str(&encode(&build_l10n::pseudo(&text)));
                self.count += 1;
            }
            None => self.out.push_str(&raw),
        }
        self.i = end;
    }
}

/// The text of a Slint string literal body. `None` for an escape that the
/// tool does not know, or a `\{…}` place.
fn decode(body: &str) -> Option<String> {
    let mut out = String::with_capacity(body.len());
    let mut chars = body.chars();
    while let Some(c) = chars.next() {
        if c != '\\' {
            out.push(c);
            continue;
        }
        match chars.next()? {
            '"' => out.push('"'),
            '\\' => out.push('\\'),
            'n' => out.push('\n'),
            'u' => {
                if chars.next()? != '{' {
                    return None;
                }
                let hex: String = chars.by_ref().take_while(|&c| c != '}').collect();
                out.push(char::from_u32(u32::from_str_radix(&hex, 16).ok()?)?);
            }
            _ => return None,
        }
    }
    Some(out)
}

/// A Slint string literal with the text `s`.
fn encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn rw(s: &str) -> String {
        rewrite(s).0
    }

    #[test]
    fn the_plain_form_changes() {
        assert_eq!(
            rw(r#"text: @tr("Create crew");"#),
            r#"text: @tr("Çřéáťé çřéw ~~~");"#
        );
    }

    #[test]
    fn the_context_stays_and_the_text_changes() {
        assert_eq!(
            rw(r#"t: @tr("noun" => "OK");"#),
            r#"t: @tr("noun" => "ÖK ~");"#
        );
        assert_eq!(
            rw(r#"t: @tr( "unit"=>"{} min", m);"#),
            r#"t: @tr( "unit"=>"{} mîñ ~", m);"#
        );
    }

    #[test]
    fn the_plural_form_changes_both_forms() {
        assert_eq!(
            rw(r#"t: @tr("{n} clip" | "{n} clips" % Math.round(a.b(1, 2)));"#),
            r#"t: @tr("{n} çľîp ~" | "{n} çľîpš ~" % Math.round(a.b(1, 2)));"#
        );
    }

    #[test]
    fn a_call_in_an_expression_and_in_the_arguments_changes() {
        assert_eq!(
            rw(r#"t: a ? @tr("OK") : @tr("ADDED BY {}", f(@tr("OK"), "x"));"#),
            r#"t: a ? @tr("ÖK ~") : @tr("ÅĎĎÉĎ BÝ {} ~~", f(@tr("ÖK ~"), "x"));"#
        );
    }

    #[test]
    fn comments_and_other_strings_stay() {
        let src = "// @tr(\"OK\")\n/* @tr(\"OK\") */\nx: \"@tr(\\\"OK\\\")\";\ny: \"a \\{@tr(\"OK\")} b\";\n";
        let want = src.replacen("\\{@tr(\"OK\")}", "\\{@tr(\"ÖK ~\")}", 1);
        assert_eq!(rw(src), want);
    }

    #[test]
    fn escapes_round_trip() {
        assert_eq!(
            rw(r#"t: @tr("Say \"hi\"\n\\");"#),
            r#"t: @tr("Šáý \"hî\"\n\\ ~~");"#
        );
        // An escape the tool does not know: the literal stays.
        assert_eq!(rw(r#"t: @tr("a\tb");"#), r#"t: @tr("a\tb");"#);
    }

    #[test]
    fn a_source_with_no_tr_is_byte_for_byte_the_same() {
        let src = "import { A } from \"a.slint\";\r\nexport component B {\r\n  t: \"x\" + (1, [2]) ;\r\n}\r\n) stray }\r\n";
        assert_eq!(rewrite(src), (src.to_string(), 0));
    }
}
