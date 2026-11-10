//! The catch-up line of a crew card: what happened while the user was away.
//!
//! The server sends the events of the line in `top_events`, with the type and
//! the values. The client makes the line from `Tr`, in the language of the UI
//! (specs/features/LOCALIZATION.md, "Server text with a kind and values").
//! `catchup_text` is the English line of the server. The client shows it only
//! when the server sends no events (an old server).

use mello_core::crew_events::{CatchupEvent, CatchupResponse};

use crate::l10n_format;
use crate::Tr;

/// The most names that a part shows. The other names are a count.
const MAX_NAMES: usize = 3;

/// The catch-up line of `response`, in the language of the UI.
pub(super) fn caption(tr: &Tr<'_>, response: &CatchupResponse) -> String {
    if !response.has_events {
        return tr.get_catchup_quiet().into();
    }
    if response.top_events.is_empty() {
        // An old server: it sends the English line only.
        return response.catchup_text.clone();
    }
    let parts: Vec<String> = response
        .top_events
        .iter()
        .filter_map(|e| part(tr, e))
        .collect();
    l10n_format::list_and_serial(tr, &parts)
}

/// One part of the line, or `None` for an event that the line does not show.
fn part(tr: &Tr<'_>, event: &CatchupEvent) -> Option<String> {
    let data = &event.data;
    let text = |key: &str| -> String {
        data.get(key)
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string()
    };
    let int = |key: &str| -> i64 { data.get(key).and_then(|v| v.as_i64()).unwrap_or(0) };
    let actor = || -> slint::SharedString {
        match event.actor_name.as_deref() {
            Some(name) if !name.is_empty() => name.into(),
            _ => tr.get_someone(),
        }
    };
    let part: slint::SharedString = match event.event_type.as_str() {
        "moment" => {
            let words = text("text");
            if !words.is_empty() {
                tr.invoke_catchup_moment_text(actor(), words.into())
            } else {
                let game = text("game_name").into();
                match text("sentiment").as_str() {
                    "win" => tr.invoke_catchup_had_win(actor(), game),
                    "loss" => tr.invoke_catchup_took_loss(actor(), game),
                    "highlight" => tr.invoke_catchup_had_moment(actor(), game),
                    _ => return None,
                }
            }
        }
        "voice_session" => tr.invoke_catchup_hung_out(
            names(tr, data, "participant_names").into(),
            text("channel_name").into(),
            minutes(tr, int("duration_min")),
        ),
        "stream_session" => tr.invoke_catchup_streamed(
            text("streamer_name").into(),
            text("title").into(),
            minutes(tr, int("duration_min")),
        ),
        "game_session" => {
            let names: slint::SharedString = names(tr, data, "player_names").into();
            let game: slint::SharedString = text("game_name").into();
            let wins = int("wins");
            let losses = int("losses");
            if wins > 0 || losses > 0 {
                let record = tr.invoke_record_dash_w_l(clamp(wins), clamp(losses));
                let streak = int("streak_after");
                if streak >= 2 {
                    tr.invoke_catchup_went_win_streak(names, record, game, clamp(streak))
                } else if streak <= -2 {
                    tr.invoke_catchup_went_loss_skid(names, record, game, clamp(-streak))
                } else {
                    tr.invoke_catchup_went(names, record, game)
                }
            } else {
                tr.invoke_feed_names_played(names, game)
            }
        }
        "member_joined" => tr.invoke_catchup_joined(text("display_name").into()),
        "member_left" => tr.invoke_catchup_left(text("display_name").into()),
        "chat_activity" => {
            tr.invoke_catchup_chat(clamp(int("message_count")), clamp(int("active_user_count")))
        }
        "clip" => {
            let seconds = data
                .get("duration_seconds")
                .and_then(|v| v.as_f64())
                .unwrap_or(0.0);
            tr.invoke_catchup_clipped(
                actor(),
                // Whole seconds, rounded as the server rounds them.
                format!("{seconds:.0}").into(),
                super::clip::clip_kind_text(tr, data).into(),
            )
        }
        "weekly_recap" => tr.get_catchup_weekly_recap(),
        _ => return None,
    };
    if part.is_empty() {
        return None;
    }
    Some(part.into())
}

/// The names of a list in `data`: all names up to three, or three names and
/// the count of the others. "someone" when the list is empty.
fn names(tr: &Tr<'_>, data: &serde_json::Value, key: &str) -> String {
    let names: Vec<String> = data
        .get(key)
        .and_then(|v| v.as_array())
        .map(|a| {
            a.iter()
                .filter_map(|v| v.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default();
    if names.is_empty() {
        return tr.get_someone().into();
    }
    if names.len() <= MAX_NAMES {
        return l10n_format::list_and_serial(tr, &names);
    }
    let shown = l10n_format::list_comma(tr, &names[..MAX_NAMES]);
    tr.invoke_catchup_names_and_others(shown.into(), clamp((names.len() - MAX_NAMES) as i64))
        .into()
}

/// A duration in whole minutes, with the unit letter ("45m", "90m").
fn minutes(tr: &Tr<'_>, minutes: i64) -> slint::SharedString {
    tr.invoke_duration_m_short(clamp(minutes))
}

fn clamp(n: i64) -> i32 {
    n.clamp(i64::from(i32::MIN), i64::from(i32::MAX)) as i32
}

#[cfg(test)]
mod tests {
    use mello_core::crew_events::{CatchupEvent, CatchupResponse};
    use serde_json::json;

    use super::caption;

    fn event(kind: &str, actor_name: Option<&str>, data: serde_json::Value) -> CatchupEvent {
        CatchupEvent {
            event_type: kind.into(),
            actor_id: "u1".into(),
            actor_name: actor_name.map(String::from),
            ts: 1,
            data,
        }
    }

    fn response(events: Vec<CatchupEvent>) -> CatchupResponse {
        CatchupResponse {
            crew_id: "crew-0".into(),
            catchup_text: "server text".into(),
            event_count: events.len() as u32,
            top_events: events,
            has_events: true,
        }
    }

    fn line(events: Vec<CatchupEvent>) -> String {
        caption(crate::l10n_format::test_tr(), &response(events))
    }

    /// Each part reads as the English part of the server
    /// (`renderEventFragment` in crew_events.go).
    #[test]
    fn each_part_reads_as_the_server_part() {
        let cases = [
            (
                event("moment", Some("Bob"), json!({"text": "what a round"})),
                "Bob: what a round",
            ),
            (
                event(
                    "moment",
                    Some("Bob"),
                    json!({"sentiment": "loss", "game_name": "CS2"}),
                ),
                "Bob took an L in CS2",
            ),
            (
                event(
                    "moment",
                    None,
                    json!({"sentiment": "highlight", "game_name": "CS2"}),
                ),
                "someone had a moment in CS2",
            ),
            (
                event(
                    "stream_session",
                    None,
                    json!({"streamer_name": "Ida", "title": "ranked", "duration_min": 90}),
                ),
                "Ida streamed ranked for 90m",
            ),
            (
                event(
                    "game_session",
                    None,
                    json!({"player_names": ["Bob"], "game_name": "CS2", "wins": 1, "losses": 4, "streak_after": -3}),
                ),
                "Bob went 1W–4L in CS2, on a 3-loss skid",
            ),
            (
                event(
                    "game_session",
                    None,
                    json!({"player_names": ["Bob"], "game_name": "CS2", "wins": 2, "losses": 1, "streak_after": 1}),
                ),
                "Bob went 2W–1L in CS2",
            ),
            (
                event(
                    "game_session",
                    None,
                    json!({"player_names": [], "game_name": "CS2"}),
                ),
                "someone played CS2",
            ),
            (
                event("member_joined", None, json!({"display_name": "Jonas"})),
                "Jonas joined the crew",
            ),
            (
                event("member_left", None, json!({"display_name": "Jonas"})),
                "Jonas left the crew",
            ),
            (
                event(
                    "chat_activity",
                    None,
                    json!({"message_count": 14, "active_user_count": 3}),
                ),
                "14 messages from 3 people in chat",
            ),
            (
                event(
                    "clip",
                    None,
                    json!({"clip_type": "game", "duration_seconds": 29.6}),
                ),
                "someone clipped that (30s game)",
            ),
            (
                event("weekly_recap", None, json!({})),
                "weekly recap is ready",
            ),
            (
                event(
                    "voice_session",
                    None,
                    json!({"participant_names": ["A", "B", "C", "D", "E"], "channel_name": "General", "duration_min": 5}),
                ),
                "A, B, C and 2 others hung out in General for 5m",
            ),
            (
                event(
                    "voice_session",
                    None,
                    json!({"participant_names": ["A", "B", "C"], "channel_name": "General", "duration_min": 5}),
                ),
                "A, B, and C hung out in General for 5m",
            ),
        ];
        for (e, want) in cases {
            assert_eq!(line(vec![e]), want);
        }
    }

    /// An event that the server does not show is not a part.
    #[test]
    fn an_unknown_event_is_not_a_part() {
        let text = line(vec![
            event("moment", Some("Bob"), json!({"sentiment": "meh"})),
            event("something_new", None, json!({})),
            event("weekly_recap", None, json!({})),
        ]);
        assert_eq!(text, "weekly recap is ready");
    }

    /// An old server sends no events: the line is its English text.
    #[test]
    fn an_old_server_shows_its_text() {
        assert_eq!(line(vec![]), "server text");
    }
}
