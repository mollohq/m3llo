//! "Leave crew" in the crew menu. A press sends one `LeaveCrew`. The crew
//! leaves the crew list when core reports that the server ended the
//! membership (`CrewLeft`). A failed leave (`CrewLeaveFailed`) keeps the crew.

use super::*;

fn leaves(h: &mut Harness) -> usize {
    h.commands()
        .into_iter()
        .filter(|c| matches!(c, Command::LeaveCrew))
        .count()
}

fn crew_ids(h: &Harness) -> Vec<String> {
    let crews = h.app().get_crews();
    (0..crews.row_count())
        .filter_map(|i| crews.row_data(i))
        .map(|c| c.id.to_string())
        .collect()
}

/// The press asks core to leave. The crew stays until the server answers:
/// the user is a member until then.
#[test]
fn leave_crew_sends_one_leave_and_the_crew_stays_until_the_answer() {
    let mut h = crew_menu_open();
    h.commands();

    let items = h.controls_labelled("Leave crew");
    assert_eq!(items.len(), 1, "the crew menu shows one \"Leave crew\"");
    items[0].invoke_accessible_default_action();
    h.pump();

    assert_eq!(leaves(&mut h), 1, "one leave for one press");
    assert_eq!(crew_ids(&h), ["crew-1", "crew-2"]);
    assert_eq!(h.app().get_active_crew_id(), "crew-1");
}

/// ★ Regression: a crew that the user left stayed in the crew list with zero
/// members online, and the next crew list from the server opened it again.
/// The server ended the membership, so the crew leaves the list.
#[test]
fn a_crew_that_the_user_left_leaves_the_crew_list() {
    let mut h = crew_menu_open();

    h.emit(Event::CrewLeft {
        crew_id: "crew-1".into(),
    });

    assert_eq!(crew_ids(&h), ["crew-2"], "crew-1 is gone");
    assert_eq!(h.app().get_active_crew_id(), "", "no crew is open");
    assert_eq!(h.app().get_active_crew_name(), "");
}

/// The server refused the leave or did not answer. The user is still a
/// member, so the crew stays in the list and stays open.
#[test]
fn a_failed_leave_keeps_the_crew_open() {
    let mut h = crew_menu_open();

    h.emit(Event::CrewLeaveFailed {
        crew_id: "crew-1".into(),
        reason: "You did not leave the crew. The crew could not be handed to another member. \
                 Try again in a moment."
            .into(),
    });

    assert_eq!(crew_ids(&h), ["crew-1", "crew-2"], "both crews stay");
    assert_eq!(h.app().get_active_crew_id(), "crew-1", "crew-1 stays open");
    assert_eq!(h.app().get_active_crew_name(), "M3LLO CREW");
}
