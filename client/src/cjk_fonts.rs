//! CJK font fallback from the OS fonts (spec 01-CLIENT section 5.1).
//!
//! The bundled fonts have no Chinese, Japanese or Korean glyphs. These come
//! from the OS fonts, through the font fallback of fontique.
//!
//! Without this module, fontique asks the OS for one fallback family per
//! script (`fontique/src/collection/mod.rs`, `fallback_families`). Slint sets
//! no locale on text, so the OS answers for Han text without a language. On a
//! Windows PC with a Western locale, DirectWrite gives a Japanese family, and
//! a Simplified-only character (们 顾 语 频) has no glyph in it: parley draws
//! nothing for it.
//!
//! This module sets an ordered list of OS families per script. Parley tries
//! the families in order for each cluster and stops at the first family that
//! maps every character (`parley/src/shape/mod.rs`, `FontSelector::select_font`).
//! A character that the first family does not have comes from the next one.
//! The order follows the UI language, so that a shared character has the
//! regional glyph shape that the reader expects.
//!
//! No font file is bundled and no font file is read into the heap: fontique
//! maps the OS files into memory (`fontique/src/source_cache.rs`, `load_blob`).

use std::cell::Cell;

use slint::fontique_011::fontique::{Collection, FallbackKey, FamilyId, Script};

/// Family names that are one font file with one glyph set. The first name
/// that the OS has is used, and the other names are not added.
///
/// Windows installs a `UI` family and its base family in one `.ttc` file
/// (for example `msyh.ttc` holds "Microsoft YaHei" and "Microsoft YaHei UI").
/// fontique gives one source per file path (`fontique/src/backend/dwrite.rs`,
/// `FontInfo::from_dwrite`), so the two families map one file and have the
/// same characters. A second name adds no glyph, only a second lookup. The
/// `UI` family comes first: its line metrics are made for UI text.
type Slot = &'static [&'static str];

/// The candidate families of one OS, by glyph region. The order inside a
/// group is the order of preference.
#[derive(Debug)]
pub struct CandidateTable {
    pub simplified: &'static [Slot],
    pub japanese: &'static [Slot],
    pub traditional: &'static [Slot],
    pub korean: &'static [Slot],
}

// Unit tests read every table on every OS; a build uses one.
#[allow(dead_code)]
pub const WINDOWS: CandidateTable = CandidateTable {
    simplified: &[&["Microsoft YaHei UI", "Microsoft YaHei"]],
    japanese: &[&["Yu Gothic UI", "Yu Gothic"], &["Meiryo"]],
    traditional: &[&["Microsoft JhengHei UI", "Microsoft JhengHei"]],
    korean: &[&["Malgun Gothic"]],
};

// Unit tests read every table on every OS; a build uses one.
#[allow(dead_code)]
pub const MACOS: CandidateTable = CandidateTable {
    simplified: &[&["PingFang SC"], &["Hiragino Sans GB"], &["Heiti SC"]],
    japanese: &[&["Hiragino Sans"], &["Hiragino Kaku Gothic ProN"]],
    traditional: &[&["PingFang TC"], &["Heiti TC"]],
    korean: &[&["Apple SD Gothic Neo"]],
};

/// The Noto Sans CJK regions can be one `.ttc` file, but each region maps
/// the shared code points to its own glyph shapes. They are different slots.
#[allow(dead_code)]
pub const LINUX: CandidateTable = CandidateTable {
    simplified: &[
        &["Noto Sans CJK SC"],
        &["Source Han Sans SC"],
        &["WenQuanYi Micro Hei"],
    ],
    japanese: &[&["Noto Sans CJK JP"], &["Source Han Sans JP"]],
    traditional: &[&["Noto Sans CJK TC"]],
    korean: &[&["Noto Sans CJK KR"]],
};

/// The table of the OS that this build runs on.
#[cfg(target_os = "windows")]
pub const NATIVE: &CandidateTable = &WINDOWS;
#[cfg(target_os = "macos")]
pub const NATIVE: &CandidateTable = &MACOS;
#[cfg(not(any(target_os = "windows", target_os = "macos")))]
pub const NATIVE: &CandidateTable = &LINUX;

/// Which regional glyph set the Han list starts with.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HanFirst {
    Simplified,
    Japanese,
    Traditional,
}

/// The Han order for a language tag (BCP 47, or POSIX as `zh_TW.UTF-8`).
///
/// `ja*` starts with Japanese. Chinese with the `Hant` script, or with the
/// region TW, HK or MO, starts with Traditional. All other tags start with
/// Simplified.
pub fn han_first(language: &str) -> HanFirst {
    let tag = language
        .split(['.', '@'])
        .next()
        .unwrap_or_default()
        .to_ascii_lowercase()
        .replace('_', "-");
    let mut subtags = tag.split('-');
    match subtags.next() {
        Some("ja") => HanFirst::Japanese,
        Some("zh") => {
            let rest: Vec<&str> = subtags.collect();
            if rest.contains(&"hant") {
                HanFirst::Traditional
            } else if rest.contains(&"hans") {
                HanFirst::Simplified
            } else if rest.iter().any(|s| matches!(*s, "tw" | "hk" | "mo")) {
                HanFirst::Traditional
            } else {
                HanFirst::Simplified
            }
        }
        _ => HanFirst::Simplified,
    }
}

/// The ordered family names per script. Only families that the OS has.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct FallbackPlan {
    /// Han (`Hani`).
    pub han: Vec<&'static str>,
    /// Hiragana (`Hira`) and Katakana (`Kana`).
    pub kana: Vec<&'static str>,
    /// Hangul (`Hang`).
    pub hangul: Vec<&'static str>,
}

/// Makes the fallback plan for a UI language. `has` tells if the OS has a
/// family name. This function does no font I/O.
pub fn plan(
    ui_language: &str,
    table: &CandidateTable,
    mut has: impl FnMut(&str) -> bool,
) -> FallbackPlan {
    let (s, j, t, k) = (
        table.simplified,
        table.japanese,
        table.traditional,
        table.korean,
    );
    let han_groups: [&[Slot]; 4] = match han_first(ui_language) {
        HanFirst::Simplified => [s, j, t, k],
        HanFirst::Japanese => [j, s, t, k],
        HanFirst::Traditional => [t, s, j, k],
    };
    FallbackPlan {
        han: resolve(&han_groups, &mut has),
        kana: resolve(&[j, s], &mut has),
        hangul: resolve(&[k], &mut has),
    }
}

fn resolve(groups: &[&[Slot]], has: &mut impl FnMut(&str) -> bool) -> Vec<&'static str> {
    groups
        .iter()
        .flat_map(|group| group.iter())
        .filter_map(|slot| slot.iter().copied().find(|name| has(name)))
        .collect()
}

fn is_candidate(table: &CandidateTable, name: &str) -> bool {
    [
        table.simplified,
        table.japanese,
        table.traditional,
        table.korean,
    ]
    .iter()
    .flat_map(|group| group.iter())
    .flat_map(|slot| slot.iter())
    .any(|candidate| candidate.eq_ignore_ascii_case(name))
}

/// Han locales that fontique keeps apart from the default Han key
/// (`fontique/src/fallback.rs`, `canonical_locale`). `zh-CN` and every
/// untracked locale use the default key.
const HAN_LOCALES: [&str; 6] = ["ja", "ko", "zh-TW", "zh-HK", "zh-MO", "zh-SG"];

const HANI: Script = Script::from_bytes(*b"Hani");
const HIRA: Script = Script::from_bytes(*b"Hira");
const KANA: Script = Script::from_bytes(*b"Kana");
const HANG: Script = Script::from_bytes(*b"Hang");

/// Sets the fallback lists on a collection, and returns the final family
/// names per script (`Hani`, `Hira`, `Kana`, `Hang`) for the log.
///
/// Each list is the plan, then the OS answer for the key when it is not a
/// candidate (a family that this module does not know stays last). A key
/// with no planned family keeps the OS answer.
pub(crate) fn apply_to(
    collection: &mut Collection,
    ui_language: &str,
    table: &CandidateTable,
) -> Vec<(&'static str, Vec<String>)> {
    let mut has = |name: &str| {
        collection
            .family_id(name)
            .is_some_and(|id| collection.family(id).is_some())
    };
    let ui_plan = plan(ui_language, table, &mut has);
    let mut keyed: Vec<(FallbackKey, Vec<&'static str>)> = vec![
        (FallbackKey::new(HANI, None), ui_plan.han.clone()),
        (FallbackKey::new(HIRA, None), ui_plan.kana.clone()),
        (FallbackKey::new(KANA, None), ui_plan.kana.clone()),
        (FallbackKey::new(HANG, None), ui_plan.hangul.clone()),
    ];
    // Slint 1.18 sets no locale on text, so only the default keys above are
    // used now. The locale keys get the order of their own locale, so the
    // per-character fallback stays when a later Slint sets a locale.
    for locale in HAN_LOCALES {
        let names = plan(locale, table, &mut has).han;
        keyed.push((FallbackKey::from((HANI, locale)), names));
    }

    for (key, names) in &keyed {
        if names.is_empty() {
            continue;
        }
        let mut ids: Vec<FamilyId> = names
            .iter()
            .filter_map(|name| collection.family_id(name))
            .collect();
        // The current list: the OS answer at the first call, the list of the
        // previous call after that. Keep only what is not a candidate, so a
        // second call gives the same members in the new order.
        let current: Vec<FamilyId> = collection.fallback_families(*key).collect();
        for id in current {
            let known = collection
                .family_name(id)
                .is_some_and(|name| is_candidate(table, name));
            if !known && !ids.contains(&id) {
                ids.push(id);
            }
        }
        if !collection.set_fallbacks(*key, ids.into_iter()) {
            log::warn!("[fonts] fontique does not track the fallback key {key:?}");
        }
    }

    [
        ("Hani", HANI),
        ("Hira", HIRA),
        ("Kana", KANA),
        ("Hang", HANG),
    ]
    .into_iter()
    .map(|(label, script)| {
        let ids: Vec<FamilyId> = collection
            .fallback_families(FallbackKey::new(script, None))
            .collect();
        let names = ids
            .into_iter()
            .filter_map(|id| collection.family_name(id).map(str::to_owned))
            .collect();
        (label, names)
    })
    .collect()
}

thread_local! {
    /// Set when this thread has its Slint platform. Slint's
    /// `shared_collection()` unwraps the global context of the thread: without
    /// a platform it makes the default backend (a different renderer) or
    /// panics. `apply` reads this flag first.
    static PLATFORM_SET: Cell<bool> = const { Cell::new(false) };
}

/// Records that this thread has set its Slint platform. Call it directly
/// after `slint::platform::set_platform` succeeds.
pub fn platform_set() {
    PLATFORM_SET.with(|flag| flag.set(true));
}

/// Sets the CJK fallback order for a UI language on Slint's shared font
/// collection. Call it on the UI thread after `platform_set`. A second call
/// with another language changes the order for text that is laid out after
/// the call. It does not panic: without a platform it logs and returns.
pub fn apply(ui_language: &str) {
    if !PLATFORM_SET.with(Cell::get) {
        log::warn!("[fonts] CJK fallback not set: the Slint platform is not set on this thread");
        return;
    }
    let mut collection = slint::fontique_011::shared_collection();
    let lists = apply_to(&mut collection, ui_language, NATIVE);
    let text: Vec<String> = lists
        .iter()
        .map(|(script, names)| format!("{script}={names:?}"))
        .collect();
    log::info!(
        "[fonts] CJK fallback for UI language {ui_language}: {}",
        text.join(" ")
    );
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    fn all_names(table: &CandidateTable) -> HashSet<&'static str> {
        [
            table.simplified,
            table.japanese,
            table.traditional,
            table.korean,
        ]
        .iter()
        .flat_map(|g| g.iter())
        .flat_map(|s| s.iter().copied())
        .collect()
    }

    fn plan_with(lang: &str, table: &CandidateTable, os: &HashSet<&str>) -> FallbackPlan {
        plan(lang, table, |n| os.contains(n))
    }

    #[test]
    fn cjk_han_first_follows_the_language_tag() {
        assert_eq!(han_first("ja"), HanFirst::Japanese);
        assert_eq!(han_first("ja-JP"), HanFirst::Japanese);
        assert_eq!(han_first("ja_JP.UTF-8"), HanFirst::Japanese);
        assert_eq!(han_first("zh-TW"), HanFirst::Traditional);
        assert_eq!(han_first("zh-HK"), HanFirst::Traditional);
        assert_eq!(han_first("zh-MO"), HanFirst::Traditional);
        assert_eq!(han_first("zh-Hant"), HanFirst::Traditional);
        assert_eq!(han_first("zh-Hant-TW"), HanFirst::Traditional);
        assert_eq!(han_first("zh_TW.UTF-8"), HanFirst::Traditional);
        assert_eq!(han_first("zh-Hans-HK"), HanFirst::Simplified);
        assert_eq!(han_first("zh-CN"), HanFirst::Simplified);
        assert_eq!(han_first("zh"), HanFirst::Simplified);
        assert_eq!(han_first("zh-SG"), HanFirst::Simplified);
        assert_eq!(han_first("en-GB"), HanFirst::Simplified);
        assert_eq!(han_first("ko-KR"), HanFirst::Simplified);
        assert_eq!(han_first("jam"), HanFirst::Simplified);
        assert_eq!(han_first(""), HanFirst::Simplified);
    }

    #[test]
    fn cjk_windows_order_by_language() {
        let os = all_names(&WINDOWS);
        let en = plan_with("en-GB", &WINDOWS, &os);
        assert_eq!(
            en.han,
            [
                "Microsoft YaHei UI",
                "Yu Gothic UI",
                "Meiryo",
                "Microsoft JhengHei UI",
                "Malgun Gothic"
            ]
        );
        assert_eq!(en.kana, ["Yu Gothic UI", "Meiryo", "Microsoft YaHei UI"]);
        assert_eq!(en.hangul, ["Malgun Gothic"]);

        let ja = plan_with("ja-JP", &WINDOWS, &os);
        assert_eq!(
            ja.han,
            [
                "Yu Gothic UI",
                "Meiryo",
                "Microsoft YaHei UI",
                "Microsoft JhengHei UI",
                "Malgun Gothic"
            ]
        );
        assert_eq!(ja.kana, en.kana);

        let tw = plan_with("zh-TW", &WINDOWS, &os);
        assert_eq!(
            tw.han,
            [
                "Microsoft JhengHei UI",
                "Microsoft YaHei UI",
                "Yu Gothic UI",
                "Meiryo",
                "Malgun Gothic"
            ]
        );
    }

    #[test]
    fn cjk_alternate_used_only_when_the_ui_family_is_absent() {
        let os: HashSet<&str> = ["Microsoft YaHei", "Yu Gothic", "Yu Gothic UI"].into();
        let p = plan_with("en", &WINDOWS, &os);
        assert_eq!(p.han, ["Microsoft YaHei", "Yu Gothic UI"]);
    }

    #[test]
    fn cjk_absent_families_are_left_out() {
        let none = plan_with("zh-CN", &WINDOWS, &HashSet::new());
        assert_eq!(none, FallbackPlan::default());

        let os: HashSet<&str> = ["Meiryo", "Malgun Gothic"].into();
        let p = plan_with("zh-CN", &WINDOWS, &os);
        assert_eq!(p.han, ["Meiryo", "Malgun Gothic"]);
        assert_eq!(p.kana, ["Meiryo"]);
        assert_eq!(p.hangul, ["Malgun Gothic"]);
    }

    #[test]
    fn cjk_macos_order_by_language() {
        let os = all_names(&MACOS);
        let zh = plan_with("zh-Hans-CN", &MACOS, &os);
        assert_eq!(
            zh.han,
            [
                "PingFang SC",
                "Hiragino Sans GB",
                "Heiti SC",
                "Hiragino Sans",
                "Hiragino Kaku Gothic ProN",
                "PingFang TC",
                "Heiti TC",
                "Apple SD Gothic Neo"
            ]
        );
        let ja = plan_with("ja", &MACOS, &os);
        assert_eq!(ja.han[0], "Hiragino Sans");
        assert_eq!(ja.han[2], "PingFang SC");
        let hk = plan_with("zh-HK", &MACOS, &os);
        assert_eq!(hk.han[..3], ["PingFang TC", "Heiti TC", "PingFang SC"]);
        assert_eq!(
            zh.kana,
            [
                "Hiragino Sans",
                "Hiragino Kaku Gothic ProN",
                "PingFang SC",
                "Hiragino Sans GB",
                "Heiti SC"
            ]
        );
        assert_eq!(zh.hangul, ["Apple SD Gothic Neo"]);
    }

    #[test]
    fn cjk_linux_order_by_language() {
        let os = all_names(&LINUX);
        let en = plan_with("en_US.UTF-8", &LINUX, &os);
        assert_eq!(
            en.han,
            [
                "Noto Sans CJK SC",
                "Source Han Sans SC",
                "WenQuanYi Micro Hei",
                "Noto Sans CJK JP",
                "Source Han Sans JP",
                "Noto Sans CJK TC",
                "Noto Sans CJK KR"
            ]
        );
        let ja = plan_with("ja_JP.UTF-8", &LINUX, &os);
        assert_eq!(
            ja.han[..3],
            ["Noto Sans CJK JP", "Source Han Sans JP", "Noto Sans CJK SC"]
        );
        let tw = plan_with("zh_TW.UTF-8", &LINUX, &os);
        assert_eq!(tw.han[..2], ["Noto Sans CJK TC", "Noto Sans CJK SC"]);
        assert_eq!(en.hangul, ["Noto Sans CJK KR"]);
    }

    #[test]
    fn cjk_apply_without_platform_returns() {
        // A fresh test thread has no platform: apply must log and return,
        // and must not make a backend.
        apply("zh-CN");
    }

    /// The characters that the Japanese fallback of Windows does not have.
    const SIMPLIFIED_ONLY: [char; 4] = ['们', '顾', '语', '频'];

    fn has_glyph(collection: &mut Collection, ids: &[FamilyId], ch: char) -> bool {
        use slint::fontique_011::fontique::{QueryFamily, QueryStatus, SourceCache};
        let mut cache = SourceCache::default();
        let mut query = collection.query(&mut cache);
        query.set_families(ids.iter().copied().map(QueryFamily::Id));
        let mut found = false;
        query.matches_with(|font| {
            found = font
                .charmap()
                .and_then(|map| map.map(ch))
                .is_some_and(|glyph| glyph != 0);
            if found {
                QueryStatus::Stop
            } else {
                QueryStatus::Continue
            }
        });
        found
    }

    #[test]
    fn cjk_fallback_on_the_real_system_fonts() {
        let mut collection = Collection::new(Default::default());
        let has_simplified = NATIVE
            .simplified
            .iter()
            .flat_map(|slot| slot.iter())
            .any(|name| collection.family_id(name).is_some());
        if !has_simplified {
            eprintln!("cjk: no Simplified Chinese OS font on this machine; test skipped");
            return;
        }
        let simplified: Vec<&str> = NATIVE
            .simplified
            .iter()
            .flat_map(|s| s.iter().copied())
            .collect();
        let japanese: Vec<&str> = NATIVE
            .japanese
            .iter()
            .flat_map(|s| s.iter().copied())
            .collect();
        let hani = FallbackKey::new(HANI, None);

        let lists = apply_to(&mut collection, "zh-CN", NATIVE);
        let han: Vec<FamilyId> = collection.fallback_families(hani).collect();
        let first = collection.family_name(han[0]).map(str::to_owned);
        assert!(
            first.as_deref().is_some_and(|n| simplified.contains(&n)),
            "zh-CN: Han list starts with {first:?}; lists {lists:?}"
        );
        for ch in SIMPLIFIED_ONLY {
            assert!(
                has_glyph(&mut collection, &han, ch),
                "no family in the Han list has {ch}: {lists:?}"
            );
        }

        let has_japanese = japanese.iter().any(|n| collection.family_id(n).is_some());
        if !has_japanese {
            eprintln!("cjk: no Japanese OS font on this machine; ja check skipped");
            return;
        }
        // A second call on the same collection: the new order replaces the old.
        let lists = apply_to(&mut collection, "ja", NATIVE);
        let han_ja: Vec<FamilyId> = collection.fallback_families(hani).collect();
        let first = collection.family_name(han_ja[0]).map(str::to_owned);
        assert!(
            first.as_deref().is_some_and(|n| japanese.contains(&n)),
            "ja: Han list starts with {first:?}; lists {lists:?}"
        );
        let mut before = han.clone();
        let mut after = han_ja.clone();
        before.sort();
        after.sort();
        assert_eq!(before, after, "a second call keeps the same families");
        let kana: Vec<FamilyId> = collection
            .fallback_families(FallbackKey::new(HIRA, None))
            .collect();
        let first_kana = collection.family_name(kana[0]).map(str::to_owned);
        assert!(first_kana.as_deref().is_some_and(|n| japanese.contains(&n)));
    }

    mod render_probe {
        #![allow(clippy::all, dead_code, unused_imports)]
        slint::slint! {
            export component CjkProbe inherits Window {
                width: 250px;
                height: 50px;
                background: #ffffff;
                // The last cell has a code point that no font maps: it draws
                // the missing-glyph box of the first font.
                for ch[i] in ["们", "顾", "语", "频", "\u{10FFFD}"]: Text {
                    x: i * 50px;
                    y: 0;
                    width: 50px;
                    height: 50px;
                    text: ch;
                    font-size: 36px;
                    color: #000000;
                }
            }
        }
    }

    /// Draws 们 顾 语 频 with the UI language `ja`: the Han list starts with a
    /// Japanese family that does not have these characters. A character with
    /// no glyph in any family draws the missing-glyph box, which also has ink.
    /// So each cell must differ from the cell of an unmapped code point: then
    /// the character came from a later family in the list.
    #[test]
    fn cjk_render_draws_simplified_only_characters() {
        use i_slint_backend_testing::{TestingBackend, TestingBackendOptions};
        use slint::ComponentHandle;

        {
            let mut collection = Collection::new(Default::default());
            let has_simplified = NATIVE
                .simplified
                .iter()
                .flat_map(|slot| slot.iter())
                .any(|name| collection.family_id(name).is_some());
            if !has_simplified {
                eprintln!("cjk: no Simplified Chinese OS font on this machine; test skipped");
                return;
            }
        }

        // A test runs on its own thread, so this thread has no platform yet.
        slint::platform::set_platform(Box::new(TestingBackend::new(TestingBackendOptions {
            mock_time: true,
            renderer_name: Some("skia-software".into()),
            ..Default::default()
        })))
        .expect("testing platform with the Skia CPU renderer");
        platform_set();
        apply("ja");

        let probe = render_probe::CjkProbe::new().expect("probe window");
        probe.show().expect("show probe");
        let shot = probe.window().take_snapshot().expect("snapshot");
        let width = shot.width() as usize;
        let height = shot.height() as usize;
        let pixels = shot.as_slice();
        let dark = |cell: usize, x: usize, y: usize| pixels[y * width + cell * 50 + x].r < 128;
        let box_cell = SIMPLIFIED_ONLY.len();
        let box_ink: usize = (0..height)
            .map(|y| (0..50).filter(|&x| dark(box_cell, x, y)).count())
            .sum();
        for (cell, ch) in SIMPLIFIED_ONLY.iter().enumerate() {
            let (mut ink, mut differ) = (0, 0);
            for y in 0..height {
                for x in 0..50 {
                    ink += usize::from(dark(cell, x, y));
                    differ += usize::from(dark(cell, x, y) != dark(box_cell, x, y));
                }
            }
            assert!(
                ink > 30 && differ > 30,
                "{ch}: {ink} ink pixels, {differ} differ from the missing-glyph box ({box_ink} ink)"
            );
        }
    }
}
