//! The cut shapes draw under every bundled language.
//!
//! Slint writes a number in a string with the decimal separator of the
//! selected language: `80,5` under Swedish, German, French, Spanish and
//! Portuguese. The `commands` of a `Path` are SVG path data, where a comma
//! separates two numbers. A path with a localized number does not parse,
//! Slint logs "Error while parsing path commands" and draws nothing: a card
//! loses its outline and its fill.
//!
//! The harness backend of the other tests has no renderer, so it never
//! parses a path. This test renders the probe (ui/previews/cut_probe.slint)
//! with the Skia CPU renderer and reads the pixels.

use i_slint_backend_testing::{TestingBackend, TestingBackendOptions};
use slint::ComponentHandle;

mod probe {
    // Debug builds only: build.rs compiles the probe when debug info is on,
    // and `cargo test` is a debug build.
    #![allow(clippy::all, dead_code, unused_imports)]
    include!(concat!(env!("OUT_DIR"), "/cut_probe.rs"));
}

/// The rows of the probe that fill their centre, top to bottom.
const FILLED_ROWS: [&str; 9] = [
    "CutCard tr",
    "CutCard tl",
    "CutCard br",
    "CutCard bl",
    "CutFill",
    "CutButton",
    "CutButton outlined",
    "OctAvatar",
    "CutTile",
];

/// The rows that paint corners only, after the filled rows.
const CORNER_ROWS: [&str; 3] = ["CutMask br", "OctMask", "OctFrame"];

/// The shapes of the probe that do not have white at their sample point.
fn missing_shapes(win: &probe::CutProbe) -> Vec<&'static str> {
    let shot = win.window().take_snapshot().expect("snapshot");
    let width = shot.width() as usize;
    let scale = width as f32 / 100.0;
    let pixels = shot.as_slice();
    // A point of the probe, in logical pixels.
    let white = |x: f32, y: f32| {
        let p = pixels[(y * scale) as usize * width + (x * scale) as usize];
        p.r > 200 && p.g > 200 && p.b > 200
    };
    let mut missing = Vec::new();
    for (row, name) in FILLED_ROWS.iter().enumerate() {
        if !white(40.0, row as f32 * 50.0 + 20.0) {
            missing.push(*name);
        }
    }
    for (i, name) in CORNER_ROWS.iter().enumerate() {
        let top = (FILLED_ROWS.len() + i) as f32 * 50.0;
        // Inside the bottom-right corner, and the centre stays open.
        if !white(78.0, top + 38.0) || white(40.0, top + 20.0) {
            missing.push(*name);
        }
    }
    missing
}

#[test]
fn cut_shapes_draw_under_every_bundled_language() {
    // A test runs on its own thread, so this thread has no platform yet.
    slint::platform::set_platform(Box::new(TestingBackend::new(TestingBackendOptions {
        mock_time: true,
        renderer_name: Some("skia-software".into()),
        ..Default::default()
    })))
    .expect("testing platform with the Skia CPU renderer");

    let win = probe::CutProbe::new().expect("probe window");
    win.show().expect("show probe");

    crate::locale::select_source_language();
    assert_eq!(
        missing_shapes(&win),
        Vec::<&str>::new(),
        "English: the probe or its sample points are wrong"
    );

    let languages = crate::locale::bundled();
    // Swedish writes 80,5. Without it this test proves nothing.
    assert!(languages.contains(&"sv"), "bundled: {languages:?}");
    for language in languages {
        crate::locale::select(language).expect("select a bundled language");
        assert_eq!(
            missing_shapes(&win),
            Vec::<&str>::new(),
            "{language}: these shapes did not draw"
        );
    }
}
