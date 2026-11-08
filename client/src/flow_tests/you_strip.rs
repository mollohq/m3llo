//! The You strip (`panels/you_strip.slint`, plan you-strip.md): the zones set
//! its head2head half (the viewer, the chase bars, the solo line), the game
//! stats its record half, and neither erases the other.

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
    assert_eq!(strip.viewer.name.as_str(), "Test User");
    assert_eq!(
        strip.held_icons.row_count(),
        1,
        "the viewer holds Night Owl"
    );
    assert!(!strip.solo);
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
    assert!(strip.has_stats, "the feed keeps the record");
    assert_eq!(strip.game_name.as_str(), "Counter-Strike 2");
    assert_eq!(strip.viewer.name.as_str(), "Test User");
}

/// A crew of one has nobody to chase: the strip has the head2head line, and
/// its Invite asks core for an invite code, as the Stage's invite does.
#[test]
fn a_crew_of_one_gets_the_head2head_line_and_invite() {
    let mut h = Harness::new();
    solo_crew_feed(&mut h);
    let strip = h.app().get_you_strip();
    assert!(strip.solo);
    assert!(bars(&h).is_empty());

    h.commands();
    h.click_label("Invite to head2head");
    assert!(
        h.commands()
            .iter()
            .any(|c| matches!(c, Command::CreateInviteCode { crew_id } if crew_id == "crew-0")),
        "the strip's Invite asks for a code"
    );
}
