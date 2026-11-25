//! "Leave crew" in the crew menu. A press sends one `LeaveCrew`. The crew
//! leaves the crew list when core reports that the server ended the
//! membership (`CrewLeft`). A failed leave (`CrewLeaveFailed`) keeps the crew.
//! The only member of a crew has no "Leave crew": the server refuses that
//! leave.

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
/// the user is a member until then. The crews here have a member count of 0,
/// which is a count that is not loaded yet: the item shows.
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

/// "crew-1" is open, and the crew state of the server gives it `members`
/// members. Then the crew menu opens.
fn crew_menu_with_member_count(members: u32) -> Harness {
    use mello_core::crew_state::{CrewCounts, CrewState};

    let mut h = Harness::new();
    h.emit(Event::LoggedIn {
        user: sample_user(),
    });
    h.app()
        .set_crews(slint::ModelRc::new(slint::VecModel::from(vec![
            crate::CrewData {
                id: "crew-1".into(),
                name: "M3LLO CREW".into(),
                ..Default::default()
            },
            crate::CrewData {
                id: "crew-2".into(),
                name: "Night Stones".into(),
                ..Default::default()
            },
        ])));
    crate::converters::set_active_crew(h.app(), "crew-1");
    h.emit(Event::CrewStateLoaded {
        state: CrewState {
            crew_id: "crew-1".into(),
            name: "M3LLO CREW".into(),
            counts: CrewCounts {
                online: 1,
                total: members,
            },
            ..Default::default()
        },
    });
    assert_eq!(crew_row(&h, "crew-1").member_count, members as i32);

    // Only the menu has "Notifications". The crew card has an "Invite" too.
    h.click_label("Crew menu");
    assert_eq!(
        h.controls_labelled("Notifications").len(),
        1,
        "the crew menu is open"
    );
    h
}

/// The server refuses the leave of the only member of a crew. That member
/// has no "Leave crew": DELETE CREW in crew settings is the way out.
#[test]
fn the_only_member_of_a_crew_has_no_leave_crew() {
    let h = crew_menu_with_member_count(1);
    assert!(
        h.controls_labelled("Leave crew").is_empty(),
        "the only member cannot leave, so the menu has no \"Leave crew\""
    );
}

/// A member of a crew with other members can leave. So can its only owner:
/// the server hands the crew to another member first.
#[test]
fn a_crew_with_other_members_has_leave_crew() {
    for members in [2, 50] {
        let h = crew_menu_with_member_count(members);
        assert_eq!(
            h.controls_labelled("Leave crew").len(),
            1,
            "a crew of {members} shows \"Leave crew\""
        );
    }
}
