//! The joined cards of the crew feed (CLIPS 7): one card for each member who
//! joined in the last 24 hours, newest first, at most three. The cards come
//! from the `joined` list of the feed response, which the server reads from
//! the `member_joined` events of the crew ledger.
//!
//! Pure functions: the text comes from the `Tr` that the caller gives.

use mello_core::crew_events::FeedJoin;

use crate::Tr;

const DAY_MS: i64 = 24 * 60 * 60 * 1000;
/// The most cards the feed shows for joins.
pub const MAX_ROWS: usize = 3;

/// One join as the card draws it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Row {
    pub user_id: String,
    /// The joiner's name, for "Jonas joined".
    pub name: String,
    /// "invited by b0bben", or empty when nobody invited.
    pub invited: String,
    /// "Jonas joined, invited by b0bben": the whole line, for a screen reader.
    pub line: String,
    pub when: String,
}

/// "invited by b0bben", or empty without an inviter.
pub fn invited_text(tr: &Tr<'_>, invited_by: &str) -> String {
    if invited_by.trim().is_empty() {
        String::new()
    } else {
        tr.invoke_joined_invited_by(invited_by.trim().into()).into()
    }
}

/// The name on the card: "A new member" when the name is blank.
pub fn display_name(tr: &Tr<'_>, name: &str) -> String {
    if name.trim().is_empty() {
        tr.get_joined_new_member().into()
    } else {
        name.trim().to_string()
    }
}

/// The title of a joined card: "Jonas joined".
pub fn joined_title(tr: &Tr<'_>, name: &str) -> String {
    tr.invoke_joined(name.into()).into()
}

/// "Jonas joined, invited by b0bben", or "Jonas joined" when nobody invited.
pub fn line(tr: &Tr<'_>, name: &str, invited_by: &str) -> String {
    let name = display_name(tr, name);
    if invited_by.trim().is_empty() {
        joined_title(tr, &name)
    } else {
        tr.invoke_joined_line(name.into(), invited_by.trim().into())
            .into()
    }
}

/// The time of the join on the viewer's clock: "Today 23:14" or "Yesterday
/// 23:14", in the clock form of the user. `tz_offset_min` is the local
/// offset, east positive. A join inside the 24 h window is on one of those
/// two days.
pub fn when_label(tr: &Tr<'_>, ts_ms: i64, now_ms: i64, tz_offset_min: i32) -> String {
    crate::l10n_format::today_or_yesterday_at(
        tr,
        ts_ms,
        now_ms,
        tz_offset_min,
        crate::l10n_format::clock_form(),
    )
}

/// The joins to draw. Joins older than 24 h, the viewer's own join and repeat
/// joiners are dropped, then the list is sorted newest first and cut to three.
/// `name_of` gives the current name of a member from the roster; the name the
/// event stored is the fallback.
pub fn rows(
    tr: &Tr<'_>,
    joins: &[FeedJoin],
    viewer_id: &str,
    now_ms: i64,
    tz_offset_min: i32,
    name_of: &dyn Fn(&str) -> Option<String>,
) -> Vec<Row> {
    let mut sorted: Vec<&FeedJoin> = joins
        .iter()
        .filter(|j| !j.user_id.is_empty() && j.user_id != viewer_id)
        .filter(|j| j.ts <= now_ms && now_ms - j.ts < DAY_MS)
        .collect();
    sorted.sort_by_key(|j| std::cmp::Reverse(j.ts));
    let mut seen: Vec<&str> = Vec::new();
    let mut out = Vec::new();
    for j in sorted {
        if seen.contains(&j.user_id.as_str()) {
            continue;
        }
        seen.push(&j.user_id);
        let name = name_of(&j.user_id).unwrap_or_else(|| j.name.clone());
        let inviter = if j.invited_by.is_empty() {
            String::new()
        } else {
            name_of(&j.invited_by).unwrap_or_else(|| j.invited_by_name.clone())
        };
        out.push(Row {
            user_id: j.user_id.clone(),
            name: display_name(tr, &name),
            invited: invited_text(tr, &inviter),
            line: line(tr, &name, &inviter),
            when: when_label(tr, j.ts, now_ms, tz_offset_min),
        });
        if out.len() == MAX_ROWS {
            break;
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    const HOUR: i64 = 3_600_000;
    // 2026-10-24 23:30 UTC.
    const NOW: i64 = 1_792_884_600_000;

    fn join(id: &str, name: &str, by: &str, ago: i64) -> FeedJoin {
        FeedJoin {
            user_id: id.into(),
            name: name.into(),
            invited_by: if by.is_empty() {
                String::new()
            } else {
                "bob".into()
            },
            invited_by_name: by.into(),
            ts: NOW - ago,
        }
    }
    fn none(_: &str) -> Option<String> {
        None
    }

    /// The functions under test, with the English `Tr` of a test window.
    fn when_label(ts_ms: i64, now_ms: i64, tz_offset_min: i32) -> String {
        use slint::ComponentHandle;
        let app = crate::testkit::new_main_window();
        super::when_label(&app.global::<Tr>(), ts_ms, now_ms, tz_offset_min)
    }
    fn rows(
        joins: &[FeedJoin],
        viewer_id: &str,
        now_ms: i64,
        tz_offset_min: i32,
        name_of: &dyn Fn(&str) -> Option<String>,
    ) -> Vec<Row> {
        use slint::ComponentHandle;
        let app = crate::testkit::new_main_window();
        super::rows(
            &app.global::<Tr>(),
            joins,
            viewer_id,
            now_ms,
            tz_offset_min,
            name_of,
        )
    }

    #[test]
    fn the_line_names_the_inviter_when_there_is_one() {
        let tr = crate::l10n_format::test_tr();
        assert_eq!(
            line(tr, "Jonas", "b0bben"),
            "Jonas joined, invited by b0bben"
        );
        assert_eq!(line(tr, "Jonas", ""), "Jonas joined");
        assert_eq!(line(tr, "", ""), "A new member joined");
        assert_eq!(invited_text(tr, " b0bben "), "invited by b0bben");
    }

    /// Under the pseudo language the line changes and keeps the names.
    #[cfg(debug_assertions)]
    #[test]
    fn the_line_follows_the_ui_language() {
        use crate::build_l10n::{pseudo, PSEUDO};
        let tr = crate::l10n_format::test_tr();
        crate::locale::select(PSEUDO).expect("select qps");
        let text = line(tr, "Jonas", "b0bben");
        crate::locale::select_source_language();
        assert_eq!(
            text,
            pseudo("{0} joined, invited by {1}")
                .replace("{0}", "Jonas")
                .replace("{1}", "b0bben")
        );
        assert_ne!(text, "Jonas joined, invited by b0bben");
    }

    #[test]
    fn the_time_reads_on_the_viewers_clock() {
        // 23:14 UTC, an hour ago in UTC.
        let ts = NOW - 16 * 60_000;
        assert_eq!(when_label(ts, NOW, 0), "Today 23:14");
        // Sweden in winter: 00:14 the next day, and now is 00:30, same day.
        assert_eq!(when_label(ts, NOW, 60), "Today 00:14");
        // Five hours ago on a UTC clock is the afternoon of the same day.
        assert_eq!(when_label(NOW - 5 * HOUR, NOW, 0), "Today 18:30");
        // At 01:30, three hours ago was 22:30 the evening before.
        let early = NOW - 22 * HOUR;
        assert_eq!(when_label(early - 3 * HOUR, early, 0), "Yesterday 22:30");
    }

    #[test]
    fn a_join_shows_for_24_hours_newest_first_and_three_at_most() {
        let joins = vec![
            join("a", "Ann", "", 5 * HOUR),
            join("b", "Jonas", "b0bben", HOUR),
            join("c", "Cy", "", 23 * HOUR),
            join("d", "Old", "", 25 * HOUR),
            join("e", "Eli", "", 2 * HOUR),
        ];
        let r = rows(&joins, "me", NOW, 0, &none);
        let ids: Vec<&str> = r.iter().map(|r| r.user_id.as_str()).collect();
        assert_eq!(
            ids,
            ["b", "e", "a"],
            "three, newest first, none older than 24 h"
        );
        assert_eq!(r[0].line, "Jonas joined, invited by b0bben");
    }

    #[test]
    fn the_viewer_and_repeat_joiners_do_not_show() {
        let joins = vec![
            join("me", "Me", "", HOUR),
            join("a", "Ann", "", 2 * HOUR),
            join("a", "Ann", "", 3 * HOUR),
        ];
        let r = rows(&joins, "me", NOW, 0, &none);
        assert_eq!(r.len(), 1);
        assert_eq!(r[0].user_id, "a");
        assert!(rows(&[], "me", NOW, 0, &none).is_empty());
    }

    #[test]
    fn the_roster_name_wins_over_the_stored_one() {
        let joins = vec![join("a", "old name", "b0bben", HOUR)];
        let roster = |id: &str| match id {
            "a" => Some("Jonas".to_string()),
            "bob" => Some("Bob".to_string()),
            _ => None,
        };
        assert_eq!(
            rows(&joins, "me", NOW, 0, &roster)[0].line,
            "Jonas joined, invited by Bob"
        );
    }
}
