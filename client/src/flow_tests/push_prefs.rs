//! The push switches of a crew (crew settings, NOTIFICATIONS tab;
//! plans/LOOKING-TO-PLAY.md section 4): every way into crew settings loads
//! them, the server's answer sets and enables them, a press sends only the
//! switch that changed, and a failure puts them back.

use i_slint_backend_testing::AccessibleRole;
use mello_core::push_prefs::PushPrefs;

use super::*;

/// Press an item of the open crew menu. The menu is a PopupWindow, so the
/// press is the default accessibility action (see
/// `crew_menu_add_channel_answers_the_accessibility_action`).
fn press_menu_item(h: &mut Harness, label: &str) {
    let items = h.controls_labelled(label);
    assert_eq!(items.len(), 1, "the crew menu shows one {label:?}");
    items[0].invoke_accessible_default_action();
    h.pump();
}

/// Crew settings of "crew-1", opened from the crew menu, on the
/// NOTIFICATIONS tab. The commands of the way in are read and dropped.
fn notifications_tab() -> Harness {
    let mut h = crew_menu_open();
    press_menu_item(&mut h, "Crew settings");
    assert!(h.app().get_crew_settings_open(), "crew settings are open");
    h.click_label("NOTIFICATIONS");
    h.commands();
    h
}

/// The switch with this label on the NOTIFICATIONS tab.
fn switch(h: &Harness, label: &str) -> ElementHandle {
    let mut found: Vec<_> = h
        .controls_labelled(label)
        .into_iter()
        .filter(|e| e.accessible_role() == Some(AccessibleRole::Switch))
        .collect();
    assert_eq!(found.len(), 1, "one switch labelled {label:?} is on screen");
    found.remove(0)
}

/// (enabled, on) of a switch, as a screen reader gets them.
fn state(h: &Harness, label: &str) -> (bool, bool) {
    let s = switch(h, label);
    (
        s.accessible_enabled()
            .expect("a switch says if it is enabled"),
        s.accessible_checked().expect("a switch says if it is on"),
    )
}

fn loaded(crew_id: &str, messages: bool, looking_to_play: bool) -> Event {
    Event::PushPrefsLoaded {
        prefs: PushPrefs {
            crew_id: crew_id.into(),
            messages,
            looking_to_play,
        },
    }
}

fn push_commands(h: &mut Harness) -> Vec<Command> {
    h.commands()
        .into_iter()
        .filter(|c| {
            matches!(
                c,
                Command::LoadPushPrefs { .. } | Command::SetPushPrefs { .. }
            )
        })
        .collect()
}

/// Both ways into crew settings ask the server for the switches of the crew.
#[test]
fn opening_crew_settings_loads_the_push_switches_of_the_crew() {
    for item in ["Crew settings", "Add channel"] {
        let mut h = crew_menu_open();
        h.commands();

        press_menu_item(&mut h, item);

        assert!(h.app().get_crew_settings_open(), "{item:?} opens the modal");
        let cmds = push_commands(&mut h);
        assert!(
            matches!(
                cmds.as_slice(),
                [Command::LoadPushPrefs { crew_id }] if crew_id == "crew-1"
            ),
            "{item:?} sends one LoadPushPrefs for crew-1, got: {cmds:?}"
        );
    }
}

/// The switches cannot be pressed before the server gives their state. The
/// state of the open crew sets and enables them; the state of another crew
/// changes nothing.
#[test]
fn the_loaded_push_switches_show_and_enable_for_the_open_crew_only() {
    let mut h = notifications_tab();
    assert_eq!(state(&h, "Messages"), (false, false), "before the answer");
    assert_eq!(state(&h, "Looking to play"), (false, false));

    switch(&h, "Messages").mock_single_click(slint::platform::PointerEventButton::Left);
    h.pump();
    assert_eq!(state(&h, "Messages"), (false, false));
    assert!(
        push_commands(&mut h).is_empty(),
        "a press on a disabled switch sends nothing"
    );

    h.emit(loaded("crew-2", true, true));
    assert_eq!(state(&h, "Messages"), (false, false), "another crew");
    assert_eq!(state(&h, "Looking to play"), (false, false));

    h.emit(loaded("crew-1", true, false));
    assert_eq!(state(&h, "Messages"), (true, true));
    assert_eq!(state(&h, "Looking to play"), (true, false));

    h.emit(loaded("crew-2", false, true));
    assert_eq!(state(&h, "Messages"), (true, true), "another crew");
    assert_eq!(state(&h, "Looking to play"), (true, false));
}

/// A press changes the switch at once and sends only that switch.
#[test]
fn a_press_on_a_push_switch_sends_only_that_switch() {
    let mut h = notifications_tab();
    h.emit(loaded("crew-1", true, false));

    switch(&h, "Messages").mock_single_click(slint::platform::PointerEventButton::Left);
    h.pump();

    assert_eq!(state(&h, "Messages"), (true, false), "off at once");
    assert_eq!(state(&h, "Looking to play"), (true, false));
    let cmds = push_commands(&mut h);
    assert!(
        matches!(
            cmds.as_slice(),
            [Command::SetPushPrefs {
                crew_id,
                messages: Some(false),
                looking_to_play: None,
            }] if crew_id == "crew-1"
        ),
        "got: {cmds:?}"
    );

    switch(&h, "Looking to play").mock_single_click(slint::platform::PointerEventButton::Left);
    h.pump();

    assert_eq!(state(&h, "Looking to play"), (true, true), "on at once");
    let cmds = push_commands(&mut h);
    assert!(
        matches!(
            cmds.as_slice(),
            [Command::SetPushPrefs {
                crew_id,
                messages: None,
                looking_to_play: Some(true),
            }] if crew_id == "crew-1"
        ),
        "got: {cmds:?}"
    );
}

/// A set that fails puts both switches back to the last state of the server.
/// The failure of another crew changes nothing.
#[test]
fn a_failed_push_switch_goes_back_to_the_state_of_the_server() {
    let mut h = notifications_tab();
    h.emit(loaded("crew-1", true, false));
    switch(&h, "Messages").mock_single_click(slint::platform::PointerEventButton::Left);
    switch(&h, "Looking to play").mock_single_click(slint::platform::PointerEventButton::Left);
    h.pump();
    assert_eq!(state(&h, "Messages"), (true, false));
    assert_eq!(state(&h, "Looking to play"), (true, true));

    h.emit(Event::PushPrefsFailed {
        crew_id: "crew-2".into(),
    });
    assert_eq!(state(&h, "Messages"), (true, false), "another crew");

    h.emit(Event::PushPrefsFailed {
        crew_id: "crew-1".into(),
    });
    assert_eq!(state(&h, "Messages"), (true, true));
    assert_eq!(state(&h, "Looking to play"), (true, false));
}

/// A load that fails gave no state: the switches stay disabled. The next
/// open asks again.
#[test]
fn a_failed_load_leaves_the_push_switches_disabled() {
    let mut h = notifications_tab();

    h.emit(Event::PushPrefsFailed {
        crew_id: "crew-1".into(),
    });

    assert_eq!(state(&h, "Messages"), (false, false));
    assert_eq!(state(&h, "Looking to play"), (false, false));
}

/// Crew settings that open again do not show the switches of the last open
/// as pressable: they wait for the new answer.
#[test]
fn opening_crew_settings_again_disables_the_push_switches_until_the_answer() {
    let mut h = notifications_tab();
    h.emit(loaded("crew-1", true, true));
    assert_eq!(state(&h, "Messages"), (true, true));

    h.click_label("Close");
    assert!(!h.app().get_crew_settings_open());
    h.app().invoke_open_crew_settings(3);
    h.pump();

    assert!(!state(&h, "Messages").0, "disabled until the answer");
    assert_eq!(push_commands(&mut h).len(), 1, "the open asks again");
}

/// The Notifications item of the crew menu opens crew settings on the
/// NOTIFICATIONS tab and loads the switches.
#[test]
fn the_notifications_item_opens_the_notifications_tab() {
    let mut h = crew_menu_open();
    h.commands();

    press_menu_item(&mut h, "Notifications");

    assert!(h.app().get_crew_settings_open(), "crew settings are open");
    assert_eq!(h.app().get_crew_settings_active_tab(), 3);
    let sent = push_commands(&mut h);
    assert!(
        matches!(sent.as_slice(), [Command::LoadPushPrefs { crew_id }] if crew_id == "crew-1"),
        "the way in loads the switches of the crew, sent: {sent:?}"
    );
}

/// A plain member cannot change the crew, so the crew menu has no Crew
/// settings item. The Notifications item is there, and the window it opens
/// has the NOTIFICATIONS tab only.
#[test]
fn a_plain_member_gets_the_notifications_tab_only() {
    let mut h = crew_menu_open();
    h.app().set_can_manage_channels(false);
    h.app().set_crew_settings_my_role(2);
    h.pump();
    assert!(
        h.controls_labelled("Crew settings").is_empty(),
        "a plain member has no Crew settings item"
    );

    press_menu_item(&mut h, "Notifications");

    assert!(h.app().get_crew_settings_open(), "crew settings are open");
    for tab in ["OVERVIEW", "CHANNELS", "MEMBERS"] {
        assert!(
            h.controls_labelled(tab).is_empty(),
            "a plain member has no {tab} tab"
        );
    }
    assert_eq!(h.controls_labelled("NOTIFICATIONS").len(), 1);
    // The switches are on screen: the tab shows whatever the active tab is.
    switch(&h, "Messages");
    switch(&h, "Looking to play");
}
