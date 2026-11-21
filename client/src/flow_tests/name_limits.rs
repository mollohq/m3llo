//! The name fields stop at the length limits of the server
//! (backend/nakama/data/modules/text_rules.go): a crew name and a voice
//! channel name have at most 24 characters, a display name at most 32.
//! Before, the client sent a longer name, the server refused it, and the
//! user saw "Couldn't create the crew. Try again." or nothing at all.

use super::*;

const LONG: &str = "abcdefghijklmnopqrstuvwxyz0123456789ABCD"; // 40 characters

fn first(n: usize) -> String {
    LONG.chars().take(n).collect()
}

/// Click the field with this label and type the long text into it.
fn type_long_into(h: &mut Harness, label: &str) {
    h.click_label(label);
    h.type_text(LONG);
}

#[test]
fn the_new_crew_name_stops_at_24_characters_and_the_crew_gets_that_name() {
    let mut h = Harness::new();
    resume(h.ctx(), OnboardingState::Done);
    h.app().set_new_crew_open(true);
    h.pump();

    type_long_into(&mut h, "CREW NAME");
    assert_eq!(h.field_text("CREW NAME"), first(24));

    h.commands();
    h.click_label("Establish Crew");
    let names: Vec<String> = h
        .commands()
        .into_iter()
        .filter_map(|c| match c {
            Command::CreateCrew { name, .. } => Some(name),
            _ => None,
        })
        .collect();
    assert_eq!(names, vec![first(24)]);
}

#[test]
fn a_crew_name_of_one_character_creates_nothing() {
    let mut h = Harness::new();
    resume(h.ctx(), OnboardingState::Done);
    h.app().set_new_crew_open(true);
    h.pump();

    h.click_label("CREW NAME");
    h.type_text("a");
    h.commands();
    h.click_label("Establish Crew");
    assert!(
        !h.commands()
            .iter()
            .any(|c| matches!(c, Command::CreateCrew { .. })),
        "the server refuses a crew name under 2 characters"
    );
}

#[test]
fn the_onboarding_crew_name_stops_at_24_characters() {
    let mut h = Harness::new();
    resume(h.ctx(), OnboardingState::PickCrew);
    h.app().set_new_crew_open(true);
    h.pump();

    type_long_into(&mut h, "CREW NAME");
    assert_eq!(h.field_text("CREW NAME"), first(24));
}

#[test]
fn the_onboarding_nickname_stops_at_32_characters() {
    let mut h = Harness::new();
    resume(h.ctx(), OnboardingState::PickAvatar);
    h.pump();

    type_long_into(&mut h, "CREW NICKNAME");
    assert_eq!(h.field_text("CREW NICKNAME"), first(32));
}

#[test]
fn the_settings_display_name_stops_at_32_characters() {
    let mut h = Harness::new();
    h.app().set_settings_open(true);
    h.pump();

    type_long_into(&mut h, "DISPLAY NAME");
    assert_eq!(h.field_text("DISPLAY NAME"), first(32));
}

#[test]
fn a_longer_display_name_set_by_the_app_stays_until_the_user_edits_it() {
    // A name from before the server rule can be longer. The server accepts it
    // unchanged, so the field must not cut it on its own.
    let mut h = Harness::new();
    let old = first(40);
    h.app().set_profile_nickname_value(old.as_str().into());
    h.app().set_settings_open(true);
    h.pump();
    assert_eq!(h.field_text("DISPLAY NAME"), old);
}

#[test]
fn the_crew_settings_name_and_new_channel_stop_at_24_characters() {
    let mut h = Harness::new();
    resume(h.ctx(), OnboardingState::Done);
    h.app().set_active_crew_id("crew-1".into());
    // The owner: only an owner or an admin sees the overview and channels tabs.
    h.app().set_crew_settings_my_role(0);
    h.app().invoke_open_crew_settings(0);
    h.pump();

    type_long_into(&mut h, "CREW NAME");
    assert_eq!(h.field_text("CREW NAME"), first(24));

    h.click_label("CHANNELS");
    type_long_into(&mut h, "New channel");
    assert_eq!(h.field_text("New channel"), first(24));
}
