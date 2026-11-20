//! The account delete (plans/ACCOUNT-DELETE.md section 4): Settings, PROFILE,
//! ACCOUNT opens a confirm. The red button sends one `DeleteAccount`. A
//! refused delete shows the server text and allows a retry. A delete goes to
//! onboarding step 1 with no device account.

use super::*;
use crate::DeleteAccountUi;

const REFUSED: &str = "Your account was not deleted. A crew you own could not be handed to another member. Try again in a moment.";

fn ui(h: &Harness) -> DeleteAccountUi<'_> {
    h.app().global::<DeleteAccountUi>()
}

fn deletes(h: &mut Harness) -> usize {
    h.commands()
        .iter()
        .filter(|c| matches!(c, Command::DeleteAccount))
        .count()
}

/// A signed-in member with a device account, on the PROFILE tab of Settings.
fn on_profile_settings() -> Harness {
    let mut h = Harness::new();
    h.settings().borrow_mut().device_id = Some("dev-abc".into());
    h.emit(Event::LoggedIn {
        user: sample_user(),
    });
    h.app().set_settings_open(true);
    h.app().set_settings_active_tab(0);
    h.pump();
    h.commands();
    h
}

/// The confirm open, with the delete sent.
fn sent() -> Harness {
    let mut h = on_profile_settings();
    h.click_label("Delete account");
    h.click_label("Delete my account");
    h
}

#[test]
fn the_account_button_opens_the_confirm_and_cancel_sends_nothing() {
    let mut h = on_profile_settings();
    assert!(h.controls_labelled("Delete my account").is_empty());

    h.click_label("Delete account");
    assert!(ui(&h).get_open(), "the confirm opens");
    assert_eq!(h.controls_labelled("Delete my account").len(), 1);

    h.click_label("Cancel");
    assert!(!ui(&h).get_open(), "Cancel closes the confirm");
    assert!(h.app().get_settings_open(), "Settings stays open");
    assert_eq!(deletes(&mut h), 0, "Cancel sends no delete");
}

#[test]
fn the_red_button_sends_one_delete_and_a_second_press_sends_nothing() {
    let mut h = sent();
    assert!(ui(&h).get_busy());
    assert_eq!(deletes(&mut h), 1);

    // The commit control is disabled while busy, so call the callback as a
    // second press would: the guard in Rust must hold too.
    ui(&h).invoke_confirm();
    h.pump();
    assert_eq!(deletes(&mut h), 0, "no second delete while one is sent");

    // Busy: Cancel and the backdrop do not close the confirm.
    h.click_label("Cancel");
    assert!(
        ui(&h).get_open(),
        "the confirm stays while the delete is sent"
    );
}

#[test]
fn a_refused_delete_shows_the_server_text_and_allows_a_retry() {
    let mut h = sent();
    h.commands();

    h.emit(Event::AccountDeleteFailed {
        reason: format!("Server error: {{\"code\":14,\"message\":\"{REFUSED}\"}}"),
        message: Some(REFUSED.into()),
    });

    assert!(ui(&h).get_open(), "the confirm stays open");
    assert!(!ui(&h).get_busy());
    assert_eq!(ui(&h).get_error(), REFUSED);
    assert!(h.app().get_logged_in(), "the account and the session stay");

    h.click_label("Delete my account");
    assert_eq!(deletes(&mut h), 1, "the retry sends the delete again");
    assert_eq!(ui(&h).get_error(), "", "a retry clears the old text");
}

#[test]
fn a_failure_with_no_server_text_shows_the_client_text() {
    let mut h = sent();
    h.emit(Event::AccountDeleteFailed {
        reason: "HTTP error: connection refused".into(),
        message: None,
    });
    assert!(!ui(&h).get_busy());
    assert_eq!(
        ui(&h).get_error(),
        "Your account was not deleted. Check the connection and try again."
    );
}

#[test]
fn a_new_open_shows_no_old_failure() {
    let mut h = sent();
    h.emit(Event::AccountDeleteFailed {
        reason: "x".into(),
        message: Some(REFUSED.into()),
    });
    h.click_label("Cancel");
    h.click_label("Delete account");
    assert_eq!(ui(&h).get_error(), "");
}

#[test]
fn a_deleted_account_goes_to_step_1_with_no_device_account() {
    let mut h = sent();
    h.commands();

    h.emit(Event::AccountDeleted);

    assert!(!ui(&h).get_open(), "the confirm closes");
    assert!(!ui(&h).get_busy());
    assert!(!h.app().get_settings_open(), "Settings closes");
    assert!(!h.app().get_logged_in());
    assert_eq!(h.app().get_user_name(), "");
    assert_eq!(visible_screens(&h), vec![Screen::Onboarding]);
    assert_eq!(
        OnboardingState::from_step(h.app().get_onboarding_step()),
        OnboardingState::PickCrew
    );
    assert_eq!(
        h.settings().borrow().device_id,
        None,
        "the device id of the deleted account is forgotten"
    );
    let cmds = h.commands();
    assert!(
        !cmds
            .iter()
            .any(|c| matches!(c, Command::DeviceAuth { .. } | Command::Logout)),
        "core already logged out, and device auth would make a new account: {cmds:?}"
    );
}
