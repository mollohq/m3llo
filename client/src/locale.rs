//! The UI language (specs/features/LOCALIZATION.md).
//!
//! The order of the sources:
//! 1. `MELLO_LANG`, when set: tests, the e2e driver, development.
//! 2. `Settings.language`, when not empty.
//! 3. The OS language.
//!
//! A source that names a language with no catalog in this build is skipped
//! with a warning. When no source names a bundled language, the UI shows
//! the source language, English.
//!
//! Slint selects a bundled language from the OS by itself when the first
//! component is created (`set_bundled_languages` in i-slint-core
//! `translations.rs`). [`apply`] selects the language explicitly after that,
//! with the same base-language rule, so the result never depends on that
//! order.

use std::sync::Mutex;

/// Environment variable that sets the UI language, above the setting.
pub const LANG_ENV: &str = "MELLO_LANG";

/// The source language of the UI strings.
pub const SOURCE_LANGUAGE: &str = "en";

/// The languages bundled in this build (catalog folder names), from
/// `build.rs`. Empty when the build has no catalog.
const BUNDLED: &str = env!("MELLO_BUNDLED_LANGUAGES");

/// Where the language came from.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum Source {
    Env,
    Setting,
    System,
}

/// The UI language and its source.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Choice {
    /// A bundled catalog folder name, or [`SOURCE_LANGUAGE`].
    pub language: String,
    pub source: Source,
}

/// The effective language, after [`apply`].
static CHOSEN: Mutex<Option<Choice>> = Mutex::new(None);

/// The languages bundled in this build.
pub(crate) fn bundled() -> Vec<&'static str> {
    BUNDLED.split(',').filter(|l| !l.is_empty()).collect()
}

/// `pt_BR` and `PT-br` are `pt-br`: compare tags this way.
fn normalize(tag: &str) -> String {
    tag.trim().replace('_', "-").to_ascii_lowercase()
}

/// The base language of a tag: `pt` of `pt-BR`, `sv` of `sv_SE.UTF-8`.
fn base(tag: &str) -> &str {
    tag.find(['-', '_', '@', '.']).map_or(tag, |i| &tag[..i])
}

/// The bundled language for `tag`, or the source language. `None` when the
/// tag names a language that this build does not have. First an exact match
/// (case and `_` ignored), then the base language, as Slint does.
fn resolve(tag: &str, bundled: &[&str]) -> Option<String> {
    let want = normalize(tag);
    if want.is_empty() {
        return None;
    }
    if let Some(l) = bundled.iter().find(|l| normalize(l) == want) {
        return Some((*l).to_string());
    }
    let want_base = base(&want);
    if want_base == SOURCE_LANGUAGE {
        return Some(SOURCE_LANGUAGE.to_string());
    }
    bundled
        .iter()
        .find(|l| base(&normalize(l)) == want_base)
        .map(|l| (*l).to_string())
}

/// Choose the UI language from the three sources. Pure, for the tests.
pub(crate) fn choose(
    env: Option<&str>,
    setting: &str,
    os: Option<&str>,
    bundled: &[&str],
) -> Choice {
    let named = [(env.unwrap_or(""), Source::Env), (setting, Source::Setting)];
    for (tag, source) in named {
        if tag.trim().is_empty() {
            continue;
        }
        match resolve(tag, bundled) {
            Some(language) => return Choice { language, source },
            None => log::warn!(
                "[locale] {source:?} names {tag:?}, which this build does not have (bundled: {bundled:?})"
            ),
        }
    }
    Choice {
        language: os
            .and_then(|tag| resolve(tag, bundled))
            .unwrap_or_else(|| SOURCE_LANGUAGE.to_string()),
        source: Source::System,
    }
}

/// Select `language` (a bundled folder name, or the source language) for
/// the components of this thread. Call it after the first component is
/// created: Slint knows the bundled languages only from then.
pub(crate) fn select(language: &str) -> Result<(), slint::SelectBundledTranslationError> {
    let tag = if language == SOURCE_LANGUAGE {
        ""
    } else {
        language
    };
    match slint::select_bundled_translation(tag) {
        // A build with no catalog shows the source language only.
        Err(slint::SelectBundledTranslationError::NoTranslationsBundled)
            if tag.is_empty() && bundled().is_empty() =>
        {
            Ok(())
        }
        other => other,
    }
}

/// Select the source language on this thread. The tests find controls by
/// their English labels, so every window a test makes shows English,
/// whatever the language of the OS.
#[cfg(any(test, feature = "testkit"))]
pub(crate) fn select_source_language() {
    if let Err(e) = select(SOURCE_LANGUAGE) {
        panic!("select the source language: {e}");
    }
}

/// Choose the UI language and select it. Call it after the first component
/// is created (the force-update dialog or the main window), and again for
/// the main window: a second call selects the same language and logs
/// nothing.
pub(crate) fn apply(setting: &str) {
    let env = std::env::var(LANG_ENV).ok();
    let os = sys_locale::get_locale();
    let choice = choose(env.as_deref(), setting, os.as_deref(), &bundled());
    if let Err(e) = select(&choice.language) {
        log::warn!("[locale] cannot select {:?}: {e}", choice.language);
        return;
    }
    let mut chosen = CHOSEN.lock().unwrap_or_else(|e| e.into_inner());
    if chosen.as_ref() != Some(&choice) {
        log::info!(
            "[locale] UI language {} (from {:?}; OS {:?}; bundled {:?})",
            choice.language,
            choice.source,
            os.as_deref().unwrap_or("unknown"),
            bundled()
        );
        *chosen = Some(choice);
    }
}

/// The effective UI language as a BCP 47 tag: the bundled catalog that the
/// UI shows (`sv`, `pt-BR`), or `en`. For fonts, the HUD and dates.
pub fn ui_language() -> String {
    CHOSEN
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .as_ref()
        .map_or_else(|| SOURCE_LANGUAGE.to_string(), |c| c.language.clone())
}

#[cfg(test)]
mod tests {
    use super::*;

    const BUNDLED: [&str; 4] = ["de", "pt-BR", "qps", "sv"];

    fn chosen(env: Option<&str>, setting: &str, os: Option<&str>) -> (String, Source) {
        let c = choose(env, setting, os, &BUNDLED);
        (c.language, c.source)
    }

    #[test]
    fn the_env_variable_wins() {
        assert_eq!(
            chosen(Some("qps"), "de", Some("sv-SE")),
            ("qps".into(), Source::Env)
        );
        assert_eq!(
            chosen(Some("en"), "de", Some("sv-SE")),
            ("en".into(), Source::Env)
        );
    }

    #[test]
    fn the_setting_comes_before_the_os() {
        assert_eq!(
            chosen(None, "de", Some("sv-SE")),
            ("de".into(), Source::Setting)
        );
        assert_eq!(
            chosen(Some(""), "de", Some("sv-SE")),
            ("de".into(), Source::Setting)
        );
    }

    #[test]
    fn the_os_language_matches_by_base_language() {
        assert_eq!(
            chosen(None, "", Some("sv-SE")),
            ("sv".into(), Source::System)
        );
        assert_eq!(
            chosen(None, "", Some("pt_BR")),
            ("pt-BR".into(), Source::System)
        );
        assert_eq!(
            chosen(None, "", Some("pt-PT")),
            ("pt-BR".into(), Source::System)
        );
        assert_eq!(
            chosen(None, "", Some("fi-FI")),
            ("en".into(), Source::System)
        );
        assert_eq!(chosen(None, "", None), ("en".into(), Source::System));
    }

    #[test]
    fn an_unknown_language_falls_through_to_the_next_source() {
        assert_eq!(
            chosen(Some("xx"), "de", None),
            ("de".into(), Source::Setting)
        );
        assert_eq!(
            chosen(None, "xx", Some("sv")),
            ("sv".into(), Source::System)
        );
    }

    /// The pseudo language is the base of the Windows pseudo locale
    /// `qps-ploc`. No real OS locale has that base, so only `MELLO_LANG`
    /// or the setting selects it.
    #[test]
    fn no_real_os_locale_selects_the_pseudo_language() {
        for os in [
            "en-US", "sv-SE", "zh-CN", "ja-JP", "pt-BR", "de-DE", "fr-FR", "es-ES",
        ] {
            assert_ne!(chosen(None, "", Some(os)).0, "qps", "{os}");
        }
        assert_eq!(chosen(None, "", Some("qps-ploc")).0, "qps");
    }

    #[test]
    fn english_variants_are_the_source_language() {
        assert_eq!(chosen(Some("en-GB"), "", None), ("en".into(), Source::Env));
        assert_eq!(
            chosen(None, "", Some("en-US")),
            ("en".into(), Source::System)
        );
    }

    /// The pipeline end to end, on the real UI. Debug builds only: the
    /// pseudo language is not in a release build.
    #[cfg(debug_assertions)]
    mod pipeline {
        use slint::ComponentHandle;

        use super::super::{select, SOURCE_LANGUAGE};
        use crate::build_l10n::{pseudo, PSEUDO};
        use crate::testkit::{Harness, MainWindow};
        use crate::Tr;

        mod probe {
            #![allow(clippy::all, dead_code, unused_imports)]
            include!(concat!(env!("OUT_DIR"), "/l10n_probe.rs"));
        }

        /// A logged-in window with the update banner on screen.
        fn banner() -> Harness {
            let mut h = Harness::new();
            h.emit(mello_core::Event::LoggedIn {
                user: mello_core::events::User {
                    id: "u1".into(),
                    username: "alice".into(),
                    display_name: "Alice".into(),
                    tag: String::new(),
                    created_at: None,
                },
            });
            h.app().set_update_available(true);
            h.app().set_update_version("1.2.3".into());
            h.pump();
            h
        }

        #[test]
        fn the_build_bundles_the_pseudo_language() {
            assert!(super::super::bundled().contains(&PSEUDO));
        }

        #[test]
        fn the_pseudo_language_translates_slint_and_tr() {
            let h = banner();
            assert_eq!(h.controls_labelled("UPDATE").len(), 1, "English first");

            select(PSEUDO).expect("select qps");
            let label = pseudo("UPDATE");
            assert_ne!(label, "UPDATE");
            assert!(h.controls_labelled("UPDATE").is_empty());
            assert_eq!(h.controls_labelled(&label).len(), 1, "{label:?}");

            let tr = h.app().global::<Tr>();
            assert_eq!(tr.get_connection_lost().as_str(), pseudo("Connection lost"));
            assert_ne!(tr.get_connection_lost().as_str(), "Connection lost");
            // A value keeps its place and its text.
            let bytes = tr.invoke_update_bytes("1.5".into(), "20.0".into());
            let want = pseudo("{} / {} MB")
                .replacen("{}", "1.5", 1)
                .replacen("{}", "20.0", 1);
            assert_eq!(bytes.as_str(), want);
            assert_ne!(bytes.as_str(), "1.5 / 20.0 MB");
            assert_eq!(
                tr.invoke_update_percent(42).as_str(),
                pseudo("{}%").replace("{}", "42")
            );

            select(SOURCE_LANGUAGE).expect("select en");
            assert_eq!(h.controls_labelled("UPDATE").len(), 1);
            assert_eq!(tr.get_connection_lost().as_str(), "Connection lost");
            assert_eq!(
                tr.invoke_update_bytes("1.5".into(), "20.0".into()).as_str(),
                "1.5 / 20.0 MB"
            );
        }

        #[test]
        fn a_plural_has_the_right_form() {
            crate::testkit::init_test_backend();
            let p = probe::L10nProbe::new().expect("probe");
            select(SOURCE_LANGUAGE).expect("select en");
            p.set_n(1);
            assert_eq!(p.get_plural().as_str(), "clip");
            p.set_n(2);
            assert_eq!(p.get_plural().as_str(), "clips");

            select(PSEUDO).expect("select qps");
            p.set_n(1);
            assert_eq!(p.get_plural().as_str(), pseudo("clip"));
            p.set_n(2);
            assert_eq!(p.get_plural().as_str(), pseudo("clips"));
        }

        /// The converted panels are in the bundle: a sample of controls from
        /// the onboarding, the control bar and the settings changes under
        /// `qps`. The control bar button has a context (`verb`).
        #[test]
        fn the_converted_panels_show_the_pseudo_language() {
            fn check(h: &Harness, english: &str) {
                let n = h.controls_labelled(english).len();
                assert!(n > 0, "English first: {english:?}");
                select(PSEUDO).expect("select qps");
                let label = pseudo(english);
                assert!(!label.trim().is_empty());
                assert_ne!(label, english);
                assert!(h.controls_labelled(english).is_empty(), "{english:?}");
                assert_eq!(h.controls_labelled(&label).len(), n, "{label:?}");
                select(SOURCE_LANGUAGE).expect("select en");
            }

            let crews = vec![mello_core::crew::Crew {
                id: "crew-0".into(),
                name: "Night Stones".into(),
                description: String::new(),
                member_count: 3,
                max_members: 10,
                open: true,
                avatar_url: None,
            }];

            let mut onboarding = Harness::new();
            onboarding.emit(mello_core::Event::DiscoverCrewsLoaded {
                crews: crews.clone(),
                cursor: None,
            });
            check(&onboarding, "Create your own crew");

            let mut app = banner();
            app.emit(mello_core::Event::CrewsLoaded { crews });
            crate::converters::set_active_crew(app.app(), "crew-0");
            app.pump();
            check(&app, "STREAM");

            app.app().set_settings_open(true);
            app.pump();
            check(&app, "GAMES");
        }

        /// A message that a catalog does not have shows the English text.
        #[test]
        fn a_missing_message_shows_english() {
            crate::testkit::init_test_backend();
            let p = probe::L10nProbe::new().expect("probe");
            select(PSEUDO).expect("select qps");
            assert_eq!(p.get_known().as_str(), pseudo("New version available"));
            assert_eq!(p.get_missing().as_str(), "A message that no catalog has");
        }

        /// The force-update dialog opens before the main window. The
        /// language can be selected when it is the first component.
        #[test]
        fn the_language_can_be_selected_for_the_force_update_dialog() {
            crate::testkit::init_test_backend();
            let dialog = crate::ForceUpdateWindow::new().expect("dialog");
            select(PSEUDO).expect("select qps on the first component");
            assert_eq!(
                dialog
                    .global::<Tr>()
                    .get_update_stage_downloading()
                    .as_str(),
                pseudo("Downloading")
            );
        }

        /// Slint selects the OS language by itself. A harness made on a
        /// thread that shows another language still shows English.
        #[test]
        fn the_harness_shows_english_when_another_language_is_selected() {
            crate::testkit::init_test_backend();
            let first = MainWindow::new().expect("window");
            select(PSEUDO).expect("select qps, as an OS language would be");
            drop(first);

            let h = banner();
            assert_eq!(
                h.controls_labelled("UPDATE").len(),
                1,
                "the harness must select the source language"
            );
        }
    }
}
