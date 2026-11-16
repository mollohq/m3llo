//! Looking to play on desktop (plans/LOOKING-TO-PLAY.md 3.6): the card of an
//! ask in the chat feed, its answers and Cancel, the pip on the composer
//! pill, and the ask popover.

use std::collections::BTreeMap;
use std::rc::Rc;

use mello_core::looking_to_play::{Answer, Ask, AskAnswer, AskState};

use super::*;
use crate::{CrewData, LookingToPlayUi, MemberData, VoiceChannelData, VoiceChannelMember};

const CREW: &str = "crew-1";
const VIEWER: &str = "user-1";

fn now_ms() -> i64 {
    crate::converters::current_time_ms()
}

fn member(id: &str, name: &str) -> MemberData {
    MemberData {
        id: id.into(),
        name: name.into(),
        initials: crate::converters::make_initials(name).into(),
        online: true,
        ..Default::default()
    }
}

/// The crew chat of `crew-1` on screen, with the viewer and `others` as the
/// members. The commands of the way in are dropped.
fn crew_chat(others: &[(&str, &str)]) -> Harness {
    let mut h = Harness::new();
    h.emit(Event::LoggedIn {
        user: sample_user(),
    });
    h.app()
        .set_crews(slint::ModelRc::new(slint::VecModel::from(vec![CrewData {
            id: CREW.into(),
            name: "Night Shift".into(),
            ..Default::default()
        }])));
    let mut members = vec![member(VIEWER, "Test User")];
    members.extend(others.iter().map(|(id, name)| member(id, name)));
    h.app()
        .set_members(slint::ModelRc::new(slint::VecModel::from(members)));
    crate::converters::set_active_crew(h.app(), CREW);
    open_chat(&mut h);
    h.commands();
    h
}

fn small_crew() -> Harness {
    crew_chat(&[("u2", "kim"), ("u3", "leo"), ("u4", "mia")])
}

/// The system message of an ask, as the server posts it.
fn ask_message(ask_id: &str, body: &str) -> mello_core::events::ChatMessage {
    let mut m = chat_message_fixture("m-ask", "system", 0);
    m.sender_id = "00000000-0000-0000-0000-000000000000".into();
    m.sender_name = String::new();
    m.content = body.into();
    m.display_body = body.into();
    m.is_system = true;
    m.event = Some(mello_core::looking_to_play::EVENT.into());
    m.data = Some(serde_json::json!({ "ask_id": ask_id, "asker_id": "u3" }));
    m
}

fn ask(asker: &str, answers: &[(&str, Answer)]) -> Ask {
    let now = now_ms();
    let mut map = BTreeMap::new();
    for (id, a) in answers {
        map.insert(
            id.to_string(),
            AskAnswer {
                answer: *a,
                at: now,
                due_at: 0,
                arrived_at: 0,
            },
        );
    }
    Ask {
        ask_id: "a1".into(),
        crew_id: CREW.into(),
        message_id: "m-ask".into(),
        asker_id: asker.into(),
        asked: Vec::new(),
        game: None,
        opened_at: now - 60_000,
        state: AskState::Open,
        closed_reason: String::new(),
        closed_at: 0,
        answers: map,
    }
}

/// The history with the ask message, and the state of `asks`.
fn load(h: &mut Harness, asks: Vec<Ask>) {
    h.emit(Event::MessagesLoaded {
        messages: vec![
            chat_message_fixture("m1", "kim", 0),
            ask_message("a1", "leo is looking to play"),
        ],
        has_more_history: false,
    });
    h.emit(Event::LookingToPlayLoaded {
        crew_id: CREW.into(),
        asks,
    });
    next_frame();
}

/// The chat row of the ask message.
fn ask_row(h: &Harness) -> crate::ChatMessageData {
    let model = h.app().get_messages();
    (0..model.row_count())
        .filter_map(|i| model.row_data(i))
        .find(|r| r.ask_id == "a1")
        .expect("the ask message has a row")
}

/// (user id, state) of each row of the card, in order.
fn rows(h: &Harness) -> Vec<(String, String)> {
    let card = ask_row(h).ask;
    (0..card.rows.row_count())
        .filter_map(|i| card.rows.row_data(i))
        .map(|r| (r.user_id.to_string(), r.state.to_string()))
        .collect()
}

fn cards_on_screen(h: &Harness) -> usize {
    ElementHandle::find_by_element_type_name(h.app(), "LookingToPlayCard").count()
}

fn ltp_commands(h: &mut Harness) -> Vec<Command> {
    h.commands()
        .into_iter()
        .filter(|c| {
            matches!(
                c,
                Command::LookingToPlayAsk { .. }
                    | Command::LookingToPlayAnswer { .. }
                    | Command::LookingToPlayCancel { .. }
                    | Command::JoinVoice { .. }
            )
        })
        .collect()
}

fn voice_channels(h: &Harness, channels: &[(&str, bool, &[&str])]) {
    let data: Vec<VoiceChannelData> = channels
        .iter()
        .map(|(id, default, members)| VoiceChannelData {
            id: (*id).into(),
            name: (*id).into(),
            is_default: *default,
            member_count: members.len() as i32,
            members: Rc::new(slint::VecModel::from(
                members
                    .iter()
                    .map(|m| VoiceChannelMember {
                        id: (*m).into(),
                        ..Default::default()
                    })
                    .collect::<Vec<_>>(),
            ))
            .into(),
            ..Default::default()
        })
        .collect();
    h.app()
        .set_voice_channels(Rc::new(slint::VecModel::from(data)).into());
}

/// History with an ask and its state: the message is a card with a row for
/// the asker and each member, voice, in, none, no.
#[test]
fn an_ask_in_the_history_shows_its_card() {
    let mut h = small_crew();
    load(
        &mut h,
        vec![ask("u3", &[("u3", Answer::In), ("u4", Answer::No)])],
    );

    let row = ask_row(&h);
    assert!(row.is_ask, "the client has the state: a card");
    assert_eq!(row.ask.state, "open");
    assert_eq!(row.ask.asker_name, "leo");
    assert_eq!(
        rows(&h),
        [
            ("u3".to_string(), "in".to_string()),
            ("u2".into(), "none".into()),
            (VIEWER.into(), "none".into()),
            ("u4".into(), "no".into()),
        ]
    );
    assert_eq!(cards_on_screen(&h), 1);
    assert_eq!(
        ElementHandle::find_by_element_type_name(h.app(), "LtpMemberRow").count(),
        4
    );
    assert_eq!(h.controls_labelled("In").len(), 1, "the viewer can answer");
}

/// No state of the ask (not loaded, or the load failed): the body as a
/// plain system line.
#[test]
fn an_ask_with_no_state_is_a_system_line() {
    let mut h = small_crew();
    load(&mut h, Vec::new());

    let row = ask_row(&h);
    assert!(!row.is_ask);
    assert_eq!(cards_on_screen(&h), 0);
    assert!(
        ElementHandle::find_by_accessible_label(h.app(), "leo is looking to play").count() > 0,
        "the body shows as the system line"
    );
}

/// An update changes the card in place: the model stays, its row changes.
#[test]
fn an_update_changes_the_card_in_place() {
    let mut h = small_crew();
    load(&mut h, vec![ask("u3", &[("u3", Answer::In)])]);
    let model = h.app().get_messages();
    let count = model.row_count();

    let mut on = ask("u3", &[("u3", Answer::In), ("u2", Answer::In)]);
    on.state = AskState::On;
    h.emit(Event::LookingToPlayUpdated {
        ask: on,
        notify: false,
    });

    assert!(model == h.app().get_messages(), "the same model");
    assert_eq!(model.row_count(), count);
    assert_eq!(ask_row(&h).ask.state, "on");
    assert_eq!(rows(&h)[1], ("u2".to_string(), "in".to_string()));
}

/// In sends the answer and joins the channel that holds the most members of
/// the ask.
#[test]
fn in_answers_and_joins_the_voice_channel_of_the_ask() {
    let mut h = small_crew();
    voice_channels(&h, &[("lobby", true, &["u4"]), ("ranked", false, &["u3"])]);
    load(
        &mut h,
        vec![ask("u3", &[("u3", Answer::In), ("u4", Answer::No)])],
    );

    h.click_label("In");

    let cmds = ltp_commands(&mut h);
    assert!(
        matches!(
            cmds.as_slice(),
            [
                Command::LookingToPlayAnswer { ask_id, answer: Answer::In },
                Command::JoinVoice { channel_id },
            ] if ask_id == "a1" && channel_id == "ranked"
        ),
        "{cmds:?}"
    );
}

/// A user who is in a voice channel already only answers.
#[test]
fn in_while_in_voice_only_answers() {
    let mut h = small_crew();
    voice_channels(
        &h,
        &[("lobby", true, &[VIEWER]), ("ranked", false, &["u3"])],
    );
    h.app().set_in_voice(true);
    load(&mut h, vec![ask("u3", &[("u3", Answer::In)])]);

    h.click_label("In");

    let cmds = ltp_commands(&mut h);
    assert!(
        matches!(
            cmds.as_slice(),
            [Command::LookingToPlayAnswer {
                answer: Answer::In,
                ..
            }]
        ),
        "{cmds:?}"
    );
}

/// The asker sees Cancel, and it sends `LookingToPlayCancel`.
#[test]
fn the_asker_cancels_the_ask() {
    let mut h = small_crew();
    load(&mut h, vec![ask(VIEWER, &[(VIEWER, Answer::In)])]);
    assert!(
        h.controls_labelled("In").is_empty(),
        "the asker does not answer"
    );

    h.click_label("Cancel the ask");

    let cmds = ltp_commands(&mut h);
    assert!(
        matches!(cmds.as_slice(), [Command::LookingToPlayCancel { ask_id }] if ask_id == "a1"),
        "{cmds:?}"
    );
}

/// The pill carries the pip only while the crew on screen has an ask that
/// is open or on.
#[test]
fn the_pill_has_the_pip_only_with_an_open_ask() {
    let mut h = small_crew();
    let pip = |h: &Harness| ui(h).get_has_live_ask();
    load(&mut h, Vec::new());
    assert!(!pip(&h));

    h.emit(Event::LookingToPlayUpdated {
        ask: ask("u3", &[("u3", Answer::In)]),
        notify: true,
    });
    assert!(pip(&h), "an open ask");

    let mut closed = ask("u3", &[("u3", Answer::In)]);
    closed.state = AskState::Closed;
    closed.closed_reason = "nobody".into();
    h.emit(Event::LookingToPlayUpdated {
        ask: closed,
        notify: false,
    });
    assert!(!pip(&h), "a closed ask has no pip");

    let mut other = ask("u3", &[("u3", Answer::In)]);
    other.ask_id = "a2".into();
    other.crew_id = "crew-2".into();
    h.emit(Event::LookingToPlayUpdated {
        ask: other,
        notify: true,
    });
    assert!(!pip(&h), "an ask of another crew is stored only");
}

/// The recent games of the feed of the crew, for the game chips.
fn recent_games(h: &Harness) {
    let zones: mello_core::crew_events::FeedZones = serde_json::from_value(serde_json::json!({
        "installed": [
            { "game_id": "steam-730", "game_name": "Counter-Strike 2" },
            { "game_id": "valheim", "game_name": "Valheim" },
        ]
    }))
    .expect("zones decode");
    *h.ctx().last_feed.borrow_mut() = Some(FeedResponse {
        crew_id: CREW.into(),
        zones,
    });
}

fn ui(h: &Harness) -> LookingToPlayUi<'_> {
    h.app().global::<LookingToPlayUi>()
}

fn open_popover(h: &mut Harness) {
    h.click_label("Looking to play");
    next_frame();
    assert!(ui(h).get_popover_open());
}

fn ask_button(h: &Harness, label: &str) -> ElementHandle {
    let found = h.controls_labelled(label);
    assert_eq!(found.len(), 1, "one {label:?} button");
    found.into_iter().next().expect("one button")
}

/// The popover sends the ask with the picked members and the game; the
/// answer closes it.
#[test]
fn the_popover_asks_the_picked_members_for_a_game() {
    let mut h = small_crew();
    recent_games(&h);
    open_popover(&mut h);
    assert!(
        ui(&h).get_everyone_picked(),
        "a small crew starts with Everyone"
    );
    assert_eq!(
        ui(&h).get_games().row_count(),
        1,
        "only a game with a library id"
    );

    h.click_label("kim");
    assert!(!ui(&h).get_everyone_picked(), "a member unpicks Everyone");
    h.click_label("Counter-Strike 2");
    h.click_label("Ask 1 member");

    let cmds = ltp_commands(&mut h);
    assert!(
        matches!(
            cmds.as_slice(),
            [Command::LookingToPlayAsk { crew_id, game: Some(g), member_ids }]
                if crew_id == CREW && g.game_id == "steam-730" && member_ids == &["u2".to_string()]
        ),
        "{cmds:?}"
    );
    assert!(ui(&h).get_busy());
    next_frame();
    assert_eq!(
        ask_button(&h, "Ask 1 member").accessible_enabled(),
        Some(false)
    );

    let mut own = ask(VIEWER, &[(VIEWER, Answer::In)]);
    own.asked = vec!["u2".into()];
    h.emit(Event::LookingToPlayUpdated {
        ask: own,
        notify: false,
    });
    assert!(!ui(&h).get_busy());
    assert!(
        !ui(&h).get_popover_open(),
        "the ask went out: the popover closes"
    );
}

/// A failed ask shows why, and the button works again.
#[test]
fn a_failed_ask_shows_the_reason_and_enables_the_button() {
    let mut h = small_crew();
    open_popover(&mut h);
    h.click_label("Ask the crew");
    let cmds = ltp_commands(&mut h);
    assert!(
        matches!(
            cmds.as_slice(),
            [Command::LookingToPlayAsk { game: None, member_ids, .. }] if member_ids.is_empty()
        ),
        "{cmds:?}"
    );

    h.emit(Event::LookingToPlayFailed {
        ask_id: String::new(),
        code: "ASK_TOO_SOON".into(),
    });
    next_frame();
    let text = "You asked a short time ago. Try again in a few minutes.";
    assert_eq!(ui(&h).get_error_text(), text);
    assert!(ElementHandle::find_by_accessible_label(h.app(), text).count() > 0);
    assert!(!ui(&h).get_busy());
    assert_eq!(
        ask_button(&h, "Ask the crew").accessible_enabled(),
        Some(true)
    );
}

/// A crew of more than 10 starts with the members of the last ask picked.
#[test]
fn a_large_crew_starts_with_the_last_picks() {
    let names: Vec<(String, String)> = (2..=13)
        .map(|i| (format!("u{i}"), format!("member{i:02}")))
        .collect();
    let others: Vec<(&str, &str)> = names
        .iter()
        .map(|(id, n)| (id.as_str(), n.as_str()))
        .collect();
    let mut h = crew_chat(&others);
    h.emit(Event::LookingToPlayPicks {
        crew_id: CREW.into(),
        groups: vec![vec!["u5".into(), "u6".into()], vec!["u9".into()]],
    });

    open_popover(&mut h);

    assert!(ui(&h).get_large_crew());
    assert!(!ui(&h).get_everyone_picked());
    assert_eq!(ui(&h).get_picked_count(), 2);
    let groups = ui(&h).get_groups();
    let first = groups.row_data(0).expect("the last picks are a group");
    assert!(first.picked);
    assert_eq!(first.label, "Last time");
    assert_eq!(
        ask_button(&h, "Ask 2 members").accessible_enabled(),
        Some(true)
    );
}
