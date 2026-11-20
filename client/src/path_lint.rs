//! Every number in the `commands` of a `Path` goes through `PathNum.n()`
//! (ui/components/cut.slint).
//!
//! `"\{x}"` writes a number with the decimal separator of the selected
//! language: `80,5` under Swedish. SVG path data reads that comma as a
//! separator between two numbers, the path does not parse, and Slint draws
//! nothing. `PathNum.n(x)` always writes `80.5`.
//!
//! The rule, checked statically over every `.slint` file under `client/ui`
//! and `hud/ui`: in a `commands:` binding, each `\{ ... }` of a string
//! literal is `\{PathNum.n( ... )}`. A string that comes from Rust
//! (`commands: root.belt.code`) has no interpolation and passes: Rust
//! writes numbers with a point.
//!
//! `cut_shape_tests.rs` renders the shared shapes under each language.
//! This lint covers the paths that a probe cannot reach.

use std::path::{Path, PathBuf};

use crate::slint_source::{scan, slint_files};

const HELPER: &str = "PathNum.n(";

/// The interpolations of `commands:` bindings in `src` that do not go
/// through the helper, as (line, text).
fn findings(src: &str) -> Vec<(usize, String)> {
    let scanned = scan(src);
    let masked = scanned.masked.as_str();
    let mut out = Vec::new();
    let mut from = 0;
    while let Some(at) = masked[from..].find("commands").map(|i| from + i) {
        from = at + "commands".len();
        let word_start = at == 0
            || !masked.as_bytes()[at - 1].is_ascii_alphanumeric()
                && !matches!(masked.as_bytes()[at - 1], b'-' | b'_' | b'.');
        if !word_start || !masked[from..].trim_start().starts_with(':') {
            continue;
        }
        // The binding ends at the first `;` outside a string.
        let end = masked[from..].find(';').map_or(masked.len(), |i| from + i);
        for string in scanned
            .strings
            .iter()
            .filter(|s| s.start >= from && s.end <= end)
        {
            let text = &src[string.clone()];
            let mut rest = text;
            while let Some(i) = rest.find("\\{") {
                let tail = &rest[i + 2..];
                let close = tail.find('}').unwrap_or(tail.len());
                let inner = tail[..close].trim();
                if !(inner.starts_with(HELPER) && inner.ends_with(')')) {
                    let offset = string.start + (text.len() - rest.len()) + i;
                    let line = src[..offset].matches('\n').count() + 1;
                    out.push((line, format!("\\{{{inner}}}")));
                }
                rest = &tail[close..];
            }
        }
        from = end;
    }
    out
}

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .expect("client/ has a parent")
        .to_path_buf()
}

#[test]
fn path_commands_write_numbers_through_the_helper() {
    let root = repo_root();
    let mut files = Vec::new();
    for dir in ["client/ui", "hud/ui"] {
        slint_files(&root.join(dir), &[], &mut files);
    }
    assert!(files.len() > 20, "found {} .slint files", files.len());
    let mut failures = Vec::new();
    let mut bindings = 0;
    for file in files {
        let src = std::fs::read_to_string(&file).expect("read a .slint file");
        bindings += src.matches("PathNum.n(").count();
        for (line, text) in findings(&src) {
            failures.push(format!(
                "{}:{line}: {text}",
                file.strip_prefix(&root).unwrap_or(&file).display()
            ));
        }
    }
    // The scan found the paths: cut.slint alone has more than this.
    assert!(bindings > 50, "{bindings} uses of PathNum.n() found");
    assert!(
        failures.is_empty(),
        "a number in Path commands is written with the decimal separator of the UI language. \
         Write it as \\{{PathNum.n(...)}} (ui/components/cut.slint):\n{}",
        failures.join("\n")
    );
}

#[test]
fn a_bare_number_in_commands_is_found() {
    let src = "Path {\n    commands: a == \"tl\"\n        ? \"M \\{x0 + c} 0 Z\"\n        : \"M \\{PathNum.n(x0)} 0 Z\";\n    text: \"\\{n} clips\";\n}\n";
    assert_eq!(findings(src), vec![(3, "\\{x0 + c}".to_string())]);
}

#[test]
fn a_string_from_rust_passes() {
    assert!(findings("Path { commands: root.belt.code; viewbox-width: 10; }").is_empty());
}
