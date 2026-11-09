//! Source scanning shared by the static `.slint` lints (`a11y_lint.rs`,
//! `i18n_lint.rs`).
//!
//! The lints read `.slint` text, not a parsed tree. [`scan`] blanks the
//! contents of string literals and comments so that braces and words inside
//! them do not count, and it records where each string literal is.

use std::ops::Range;
use std::path::{Path, PathBuf};

/// Comment markers that [`scan`] keeps verbatim, so an exemption survives
/// the masking.
const MARKERS: [&str; 2] = ["a11y: none", "i18n: none"];

/// The result of [`scan`].
pub(crate) struct Scanned {
    /// The source with string contents and comments replaced by spaces.
    /// Offsets and newlines are kept. The quotes of a string stay.
    pub masked: String,
    /// The byte range of each string literal, quotes included.
    pub strings: Vec<Range<usize>>,
}

/// Mask `src` and find its string literals. See [`Scanned`].
pub(crate) fn scan(src: &str) -> Scanned {
    let b = src.as_bytes();
    let mut out = src.as_bytes().to_vec();
    let mut strings = Vec::new();
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'"' {
            let start = i;
            i += 1;
            while i < b.len() && b[i] != b'"' {
                if b[i] == b'\\' {
                    out[i] = b' ';
                    i += 1;
                }
                if i < b.len() && b[i] != b'\n' {
                    out[i] = b' ';
                }
                i += 1;
            }
            i += 1;
            strings.push(start..i.min(b.len()));
        } else if b[i] == b'/' && b.get(i + 1) == Some(&b'/') {
            let end = src[i..].find('\n').map_or(b.len(), |n| i + n);
            if !MARKERS.iter().any(|m| src[i..end].contains(m)) {
                out[i..end].fill(b' ');
            }
            i = end;
        } else if b[i] == b'/' && b.get(i + 1) == Some(&b'*') {
            let end = src[i + 2..].find("*/").map_or(b.len(), |n| i + 2 + n + 2);
            for c in &mut out[i..end] {
                if *c != b'\n' {
                    *c = b' ';
                }
            }
            i = end;
        } else {
            i += 1;
        }
    }
    Scanned {
        masked: String::from_utf8(out).expect("masking keeps UTF-8 boundaries"),
        strings,
    }
}

/// [`scan`], the masked text only.
pub(crate) fn mask(src: &str) -> String {
    scan(src).masked
}

/// Every `.slint` file under `dir`, recursively. `skip` names directories
/// to leave out (`previews`).
pub(crate) fn slint_files(dir: &Path, skip: &[&str], out: &mut Vec<PathBuf>) {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for e in entries.flatten() {
        let p = e.path();
        if p.is_dir() {
            if !p.file_name().is_some_and(|n| skip.iter().any(|s| n == *s)) {
                slint_files(&p, skip, out);
            }
        } else if p.extension().is_some_and(|x| x == "slint") {
            out.push(p);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strings_are_found_and_masked() {
        let src = "Text { text: \"a \\\"b\\\" {\"; } // c {\n";
        let s = scan(src);
        assert_eq!(s.strings.len(), 1);
        assert_eq!(&src[s.strings[0].clone()], "\"a \\\"b\\\" {\"");
        assert_eq!(s.masked.matches('{').count(), 1, "{}", s.masked);
    }

    #[test]
    fn markers_survive() {
        let s = mask("x // i18n: none (id)\ny // a11y: none (backdrop)\nz // other\n");
        assert!(s.contains("i18n: none"));
        assert!(s.contains("a11y: none"));
        assert!(!s.contains("other"));
    }
}
