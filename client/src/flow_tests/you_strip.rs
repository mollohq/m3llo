//! The You strip (`panels/you_strip.slint`, plan you-strip.md): the zones set
//! its head2head half (the viewer, the chase bars), the game stats its record
//! half, and neither erases the other. It shows only when the viewer and
//! another member share game activity.

use super::*;

/// A crew of three where the viewer is behind Jonas in Locked In, holds Night
/// Owl 12 minutes ahead of Jonas, and is far behind Max in The Carry.
fn chase_feed() -> FeedResponse {
    let mut entry = h2h_entry(
        3,
        vec![
            h2h_title("locked_in", "u2", serde_json::json!(14.2), "h"),
            h2h_title("the_carry", "u3", serde_json::json!(4167.0), "h"),
            h2h_title("night_owl", "user-1", serde_json::json!("03:14"), ""),
        ],
        vec![
            serde_json::json!({"a": "user-1", "b": "u2", "a_count": 1, "b_count": 1,
                "values": [{"title_id": "night_owl", "a_value": "03:14", "b_value": "03:02"}]}),
            serde_json::json!({"a": "user-1", "b": "u3", "a_count": 1, "b_count": 1,
                "values": [{"title_id": "night_owl", "a_value": "03:14", "b_value": "01:30"}]}),
        ],
        None,
    );
    entry.data["own_values"] = serde_json::json!([
        {"title_id": "locked_in", "value": 12.9, "unit": "h", "display": "12.9"},
        {"title_id": "the_carry", "value": 2927.0, "unit": "h", "display": "2,927"},
        {"title_id": "night_owl", "value": "03:14", "unit": "", "display": "03:14"},
    ]);
    feed_of(vec![entry])
}

fn crew_of_three(h: &mut Harness) {
    app_with_crew_of(h, 3);
    set_roster(
        h,
        &[("user-1", "Test User"), ("u2", "Jonas"), ("u3", "Max")],
    );
}

fn bars(h: &Harness) -> Vec<(bool, String, String, String, String)> {
    let strip = h.app().get_you_strip();
    (0..strip.bars.row_count())
        .filter_map(|i| strip.bars.row_data(i))
        .map(|b| {
            (
                b.take,
                b.title_id.to_string(),
                b.other.name.to_string(),
                b.delta.to_string(),
                b.delta_unit.to_string(),
            )
        })
        .collect()
}

fn cs2_stats() -> Event {
    Event::UserGameStatsLoaded {
        games: vec![mello_core::crew_events::UserGameStats {
            game_id: "counter-strike-2".into(),
            wins: 4,
            losses: 2,
            current_streak: 2,
            ..Default::default()
        }],
    }
}

/// The strip draws the closest take and the thinnest hold of the open crew.
#[test]
fn the_strip_draws_the_closest_take_and_the_thinnest_hold() {
    let mut h = Harness::new();
    crew_of_three(&mut h);
    h.emit(Event::FeedLoaded {
        response: chase_feed(),
    });

    assert_eq!(
        bars(&h),
        vec![
            (
                true,
                "locked_in".into(),
                "Jonas".into(),
                "\u{2212}1.3".into(),
                "h".into()
            ),
            (
                false,
                "night_owl".into(),
                "Jonas".into(),
                "+12".into(),
                "min".into()
            ),
        ]
    );
    let strip = h.app().get_you_strip();
    assert!(strip.ready, "Jonas and Max share Night Owl with the viewer");
    assert_eq!(strip.viewer.name.as_str(), "Test User");
    assert_eq!(
        strip.held_icons.row_count(),
        1,
        "the viewer holds Night Owl"
    );
    assert_eq!(count_of(&h, "YouStrip"), 1, "the strip is drawn");
    assert_eq!(count_of(&h, "ChaseBar"), 2, "both bars are drawn");
}

/// The record comes from the game stats and the bars from the feed. A load of
/// one keeps the other. A crew with no head2head data clears the bars only.
#[test]
fn the_record_and_the_bars_keep_each_other() {
    let mut h = Harness::new();
    crew_of_three(&mut h);
    h.emit(Event::FeedLoaded {
        response: chase_feed(),
    });
    h.emit(cs2_stats());
    let strip = h.app().get_you_strip();
    assert!(strip.has_stats);
    assert_eq!(bars(&h).len(), 2, "the stats keep the bars");

    h.emit(Event::FeedLoaded {
        response: feed_of(vec![]),
    });
    let strip = h.app().get_you_strip();
    assert!(bars(&h).is_empty(), "no head2head data, no bars");
    assert!(!strip.ready, "no head2head data, no strip");
    assert!(strip.has_stats, "the feed keeps the record");
    assert_eq!(strip.game_name.as_str(), "Counter-Strike 2");
    assert_eq!(strip.viewer.name.as_str(), "Test User");
}

/// A new crew of one has the quest card. The strip waits for another member
/// who shares game activity.
#[test]
fn a_crew_of_one_has_no_strip() {
    let mut h = Harness::new();
    solo_crew_feed(&mut h);
    h.emit(cs2_stats());
    let strip = h.app().get_you_strip();
    assert!(strip.has_stats, "the record is there");
    assert!(!strip.ready, "but the strip waits for a second sharer");
    // A hidden element is absent from the element queries.
    assert_eq!(count_of(&h, "YouStrip"), 0, "the strip is not drawn");
}

/// A member whose pair with the viewer holds only voice, clips and wins does
/// not share game activity: no strip.
#[test]
fn a_member_with_only_voice_and_clips_is_no_rival() {
    let mut h = Harness::new();
    crew_of_three(&mut h);
    let mut entry = h2h_entry(
        2,
        vec![h2h_title("mouth", "u2", serde_json::json!(140), "min")],
        vec![
            serde_json::json!({"a": "user-1", "b": "u2", "a_count": 0, "b_count": 1,
            "values": [{"title_id": "mouth", "a_value": 90, "b_value": 140}]}),
        ],
        None,
    );
    entry.data["own_values"] =
        serde_json::json!([{"title_id": "mouth", "value": 90, "unit": "min", "display": "90"}]);
    h.emit(Event::FeedLoaded {
        response: feed_of(vec![entry]),
    });
    assert!(!h.app().get_you_strip().ready);
}

/// The viewer who does not share game activity has no strip, and turning
/// sharing off hides it at once.
#[test]
fn sharing_off_hides_the_strip() {
    let mut h = Harness::new();
    crew_of_three(&mut h);
    h.emit(Event::FeedLoaded {
        response: chase_feed(),
    });
    assert!(h.app().get_you_strip().ready);

    h.app().invoke_setting_changed_share_game_activity(false);
    assert!(!h.app().get_you_strip().ready, "hidden at once");
    h.emit(Event::FeedLoaded {
        response: chase_feed(),
    });
    assert!(
        !h.app().get_you_strip().ready,
        "a feed load keeps it hidden while sharing is off"
    );
}

/// The chase feed with the crew's quests and these steps done.
fn chase_feed_with_quests(steps: serde_json::Value) -> FeedResponse {
    let mut feed = chase_feed();
    feed.zones.quests = Some(
        serde_json::from_value(serde_json::json!({
            "owner_id": "user-1", "made_ms": 1_793_880_000_000_i64,
            "ends_ms": 1_793_880_000_000_i64 + 14 * 86_400_000,
            "steps": steps, "gifts": {}
        }))
        .expect("a quests payload"),
    );
    feed
}

/// A new crew does its quests first: the strip waits until every step is
/// done, then shows when the members share game activity.
#[test]
fn the_strip_waits_for_the_quests() {
    let mut h = Harness::new();
    crew_of_three(&mut h);
    h.emit(Event::FeedLoaded {
        response: chase_feed_with_quests(serde_json::json!({
            "friend": {"user_id": "u2"}, "call": {"user_ids": ["u2"]}, "clip": {"clip_id": "c1"}
        })),
    });
    assert!(
        !h.app().get_you_strip().ready,
        "the first session is still open"
    );
    assert_eq!(count_of(&h, "YouStrip"), 0);

    h.emit(Event::FeedLoaded {
        response: chase_feed_with_quests(serde_json::json!({
            "friend": {"user_id": "u2"}, "call": {"user_ids": ["u2"]}, "clip": {"clip_id": "c1"},
            "session": {"minutes": 40}
        })),
    });
    assert!(h.app().get_you_strip().ready, "every step is done");
}

// ---- the You view (plan you-strip.md section 3) ----

fn open_view(h: &mut Harness) {
    crew_of_three(h);
    h.emit(Event::FeedLoaded {
        response: chase_feed(),
    });
    h.commands();
    h.click_label("You");
    next_frame();
}

/// A tap on the strip opens the view on Belts, with the holds and the takes
/// of the open crew, and fetches the game stats again.
#[test]
fn the_strip_opens_the_you_view_on_belts() {
    let mut h = Harness::new();
    open_view(&mut h);
    assert!(h.app().get_you_view_open());
    assert_eq!(h.app().get_you_view_tab(), 1, "Belts");
    assert!(h
        .commands()
        .iter()
        .any(|c| matches!(c, Command::GetUserGameStats)));
    assert_eq!(h.app().get_you_view_holds().row_count(), 1, "Night Owl");
    assert_eq!(
        h.app().get_you_view_takes().row_count(),
        2,
        "Locked In, The Carry"
    );
    assert_eq!(count_of(&h, "BeltRow"), 3);
}

/// Head2head lists the viewer against every member with a contested title,
/// closest score first.
#[test]
fn the_head2head_tab_lists_every_pair() {
    let mut h = Harness::new();
    open_view(&mut h);
    h.click_label("Head2head");
    next_frame();
    assert_eq!(h.app().get_you_view_tab(), 0);
    let pairs = h.app().get_you_view_pairs();
    let names: Vec<String> = (0..pairs.row_count())
        .filter_map(|i| pairs.row_data(i))
        .map(|p| format!("{} {}", p.other.name, p.title))
        .collect();
    assert_eq!(names, vec!["Jonas Night Owl", "Max Night Owl"]);
    assert_eq!(count_of(&h, "PairRow"), 2);
}

/// The view keeps no tab between opens: the strip always opens it on Belts.
/// Close closes it.
#[test]
fn the_view_reopens_on_belts_and_closes() {
    let mut h = Harness::new();
    open_view(&mut h);
    h.click_label("Games");
    next_frame();
    assert_eq!(h.app().get_you_view_tab(), 2);
    h.click_label("Close");
    assert!(!h.app().get_you_view_open());

    h.click_label("You");
    next_frame();
    assert!(h.app().get_you_view_open());
    assert_eq!(h.app().get_you_view_tab(), 1, "Belts again");
}

/// Games lists every tracked game: a game with no record shows its week in
/// hours.
#[test]
fn a_game_with_no_record_is_in_the_games_tab() {
    let mut h = Harness::new();
    crew_of_three(&mut h);
    h.emit(Event::UserGameStatsLoaded {
        games: vec![mello_core::crew_events::UserGameStats {
            game_id: "brawlhalla".into(),
            recent_days: vec![mello_core::crew_events::RecentDayEntry {
                date: "2026-11-08".into(),
                wall_min: 390,
                active_min: 380,
            }],
            ..Default::default()
        }],
    });
    let games = h.app().get_you_view_games();
    assert_eq!(games.row_count(), 1);
    let g = games.row_data(0).expect("a game");
    assert!(!g.has_record);
    assert_eq!(g.week_value.as_str(), "6.5");
}
