//! The crew delete: DELETE CREW in crew settings opens a confirm. Cancel keeps
//! the crew and sends nothing. The red button sends one `DeleteCrew`. A failed
//! delete shows a text and allows a retry.

use super::*;
use crate::DeleteCrewUi;

fn ui(h: &Harness) -> DeleteCrewUi<'_> {
    h.app().global::<DeleteCrewUi>()
}

fn deletes(h: &mut Harness) -> Vec<String> {
    h.commands()
        .into_iter()
        .filter_map(|c| match c {
            Command::DeleteCrew { crew_id } => Some(crew_id),
            _ => None,
        })
        .collect()
}

/// The owner of "crew-1" in its crew settings. The commands of the way in are
/// read and dropped.
fn owner_in_crew_settings() -> Harness {
    let mut h = crew_menu_open();
    h.app().set_crew_settings_my_role(0);
    let items = h.controls_labelled("Crew settings");
    assert_eq!(items.len(), 1, "the crew menu shows one \"Crew settings\"");
    items[0].invoke_accessible_default_action();
    h.pump();
    assert!(h.app().get_crew_settings_open(), "crew settings are open");
    h.commands();
    h
}

#[test]
fn delete_crew_opens_a_confirm_and_cancel_keeps_the_crew() {
    let mut h = owner_in_crew_settings();
    let crews = h.app().get_crews().row_count();
    assert!(h.controls_labelled("Delete this crew").is_empty());

    h.click_label("DELETE CREW");
    assert!(ui(&h).get_open(), "the confirm opens");
    assert_eq!(h.controls_labelled("Delete this crew").len(), 1);
    assert_eq!(
        deletes(&mut h),
        Vec::<String>::new(),
        "DELETE CREW sends no delete"
    );

    h.click_label("Cancel");
    assert!(!ui(&h).get_open(), "Cancel closes the confirm");
    assert!(h.app().get_crew_settings_open(), "crew settings stay open");
    assert_eq!(h.app().get_crews().row_count(), crews, "the crew stays");
    assert_eq!(
        deletes(&mut h),
        Vec::<String>::new(),
        "Cancel sends no delete"
    );
}

#[test]
fn the_red_button_sends_one_delete_and_the_crew_goes() {
    let mut h = owner_in_crew_settings();
    let crews = h.app().get_crews().row_count();
    h.click_label("DELETE CREW");
    h.click_label("Delete this crew");
    assert!(ui(&h).get_busy());
    assert_eq!(deletes(&mut h), vec!["crew-1".to_string()]);

    // The commit control is disabled while busy, so call the callback as a
    // second press would: the guard in Rust must hold too.
    ui(&h).invoke_confirm();
    h.pump();
    assert_eq!(deletes(&mut h), Vec::<String>::new(), "no second delete");

    // Busy: Cancel does not close the confirm.
    h.click_label("Cancel");
    assert!(
        ui(&h).get_open(),
        "the confirm stays while the delete is sent"
    );

    h.emit(Event::CrewDeleted {
        crew_id: "crew-1".into(),
    });
    assert!(!ui(&h).get_open(), "the confirm closes");
    assert!(!ui(&h).get_busy());
    assert!(!h.app().get_crew_settings_open(), "crew settings close");
    assert_eq!(h.app().get_crews().row_count(), crews - 1, "crew-1 is gone");
}

#[test]
fn a_failed_crew_delete_shows_a_text_and_allows_a_retry() {
    let mut h = owner_in_crew_settings();
    let crews = h.app().get_crews().row_count();
    h.click_label("DELETE CREW");
    h.click_label("Delete this crew");
    h.commands();

    h.emit(Event::CrewDeleteFailed {
        reason: "HTTP error: connection refused".into(),
    });
    assert!(ui(&h).get_open(), "the confirm stays open");
    assert!(!ui(&h).get_busy());
    assert_eq!(
        ui(&h).get_error(),
        "The crew was not deleted. Check the connection and try again."
    );
    assert_eq!(h.app().get_crews().row_count(), crews, "the crew stays");

    h.click_label("Delete this crew");
    assert_eq!(
        deletes(&mut h),
        vec!["crew-1".to_string()],
        "the retry sends the delete again"
    );
    assert_eq!(ui(&h).get_error(), "", "a retry clears the old text");
}

#[test]
fn crew_settings_open_again_without_an_old_confirm() {
    let mut h = owner_in_crew_settings();
    h.click_label("DELETE CREW");
    h.emit(Event::CrewDeleteFailed { reason: "x".into() });
    h.click_label("Cancel");
    h.click_label("DELETE CREW");
    assert_eq!(ui(&h).get_error(), "", "a new open shows no old failure");

    // The confirm is open when crew settings close and open again.
    h.app().set_crew_settings_open(false);
    h.pump();
    h.app().invoke_open_crew_settings(0);
    h.pump();
    assert!(!ui(&h).get_open(), "no confirm of an earlier visit");
    assert!(h.controls_labelled("Delete this crew").is_empty());
}
