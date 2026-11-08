//! The chase of the You strip: the belts the viewer can take and the belts the
//! viewer holds, each with the gap to the other member. Spec `features/YOU-STRIP.md`
//! section 2, spec `features/HEAD2HEAD.md`.
//!
//! Everything comes from the `head2head` zone data: `titles` (the holder and
//! the holder's value), `own_values` (the viewer's value) and `pairs` (the
//! value of every member against the viewer).

use std::cmp::Ordering;

use serde_json::{json, Value};

use super::{
    any_display_rank, any_title_name, grouped, higher_wins, magnitude, value_text, Person,
    WireFeed, WireTitle,
};

/// A bar to take a belt from its holder, or to hold a belt against the
/// runner-up.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    Take,
    Hold,
}

/// One bar of the strip, or one row of the Belts tab.
#[derive(Debug, Clone, PartialEq)]
pub struct Bar {
    pub kind: Kind,
    pub title_id: String,
    pub title: String,
    /// The holder of a take, the runner-up of a hold.
    pub other: Person,
    /// The two values as text, and their unit: ("12.9", "14.2", "h"),
    /// ("03:14", "03:02", "").
    pub you_value: String,
    pub other_value: String,
    pub unit: String,
    /// The signed gap and its unit: ("−1.3", "h"), ("+12", "min"), ("±0", "").
    /// Time keeps its unit. A count is a bare number.
    pub delta: String,
    pub delta_unit: String,
    /// `abs(you - other) / max(you, other)`, from 0 to 1. The pair rows use
    /// the same gap for their deciding title.
    pub gap: f64,
}

impl Bar {
    /// Where the bar ends: a take fills to it, a hold puts the runner-up's
    /// mark on it.
    pub fn ratio(&self) -> f32 {
        (1.0 - self.gap).clamp(0.0, 1.0) as f32
    }
}

/// Titles that do not come from game activity: voice minutes, clips and win
/// moments count with "Show crew what I play" off.
const NOT_GAME_TITLES: [&str; 3] = ["mouth", "clipper", "winner"];

/// The viewer's chase in one crew.
#[derive(Debug, Clone, PartialEq, Default)]
pub struct Chase {
    /// The titles the viewer holds, in display order.
    pub held: Vec<String>,
    /// Every contested title of the crew.
    pub contested: usize,
    /// Every belt the viewer can take, smallest gap first.
    pub takes: Vec<Bar>,
    /// Every belt the viewer holds, smallest lead first.
    pub holds: Vec<Bar>,
    /// Another member shares game activity with the viewer: their pair with
    /// the viewer has a value in a game title. A pair holds only the titles
    /// both contend for, so the viewer shares too. The strip shows only then.
    pub game_rival: bool,
}

impl Chase {
    /// The bars of the strip: the closest take and the thinnest hold. With no
    /// hold, the two closest takes. With no take, the two thinnest holds.
    pub fn strip(&self) -> Vec<&Bar> {
        match (self.takes.first(), self.holds.first()) {
            (Some(t), Some(h)) => vec![t, h],
            (Some(_), None) => self.takes.iter().take(2).collect(),
            (None, Some(_)) => self.holds.iter().take(2).collect(),
            (None, None) => Vec::new(),
        }
    }
}

/// Build the chase from the zone data. `None` when the data does not parse.
pub fn chase(data: &Value, viewer: &Person, roster: &dyn Fn(&str) -> Person) -> Option<Chase> {
    let wire: WireFeed = serde_json::from_value(data.clone()).ok()?;
    let contested: Vec<&WireTitle> = wire
        .titles
        .iter()
        .filter(|t| !t.holder_user_id.is_empty())
        .collect();
    let mut held: Vec<&WireTitle> = contested
        .iter()
        .copied()
        .filter(|t| t.holder_user_id == viewer.id)
        .collect();
    held.sort_by_key(|t| any_display_rank(&t.title_id));

    // A take needs the viewer's own value: a title the viewer does not
    // contend for is not in reach.
    let mut takes: Vec<Bar> = contested
        .iter()
        .filter(|t| t.holder_user_id != viewer.id)
        .filter_map(|t| {
            let own = wire.own_values.iter().find(|o| o.title_id == t.title_id)?;
            Some(bar(
                Kind::Take,
                t,
                &own.value,
                &t.value,
                roster(&t.holder_user_id),
            ))
        })
        .collect();

    // The runner-up of a hold is the member with the best value against the
    // viewer. Equal values keep the payload order.
    let mut holds: Vec<Bar> = held
        .iter()
        .filter_map(|t| {
            let (who, value) = wire
                .pairs
                .iter()
                .filter(|p| p.a == viewer.id)
                .filter_map(|p| {
                    p.values
                        .iter()
                        .find(|v| v.title_id == t.title_id)
                        .map(|v| (p.b.as_str(), &v.b_value))
                })
                .reduce(|best, next| {
                    if beats(&t.title_id, next.1, best.1) {
                        next
                    } else {
                        best
                    }
                })?;
            Some(bar(Kind::Hold, t, &t.value, value, roster(who)))
        })
        .collect();

    sort(&mut takes);
    sort(&mut holds);
    let game_rival = wire.pairs.iter().any(|p| {
        p.a == viewer.id
            && p.b != viewer.id
            && p.values
                .iter()
                .any(|v| !NOT_GAME_TITLES.contains(&v.title_id.as_str()))
    });
    Some(Chase {
        held: held.iter().map(|t| t.title_id.clone()).collect(),
        contested: contested.len(),
        takes,
        holds,
        game_rival,
    })
}

/// Whether value `a` beats value `b` in a title. Has a Life goes to the
/// fewest hours.
fn beats(title_id: &str, a: &Value, b: &Value) -> bool {
    let (a, b) = (magnitude(a), magnitude(b));
    if higher_wins(title_id) {
        a > b
    } else {
        a < b
    }
}

/// Smallest gap first. Equal gaps go to the display order of the titles.
fn sort(bars: &mut [Bar]) {
    bars.sort_by(|x, y| {
        x.gap
            .partial_cmp(&y.gap)
            .unwrap_or(Ordering::Equal)
            .then_with(|| any_display_rank(&x.title_id).cmp(&any_display_rank(&y.title_id)))
    });
}

fn bar(kind: Kind, t: &WireTitle, you: &Value, other: &Value, who: Person) -> Bar {
    let (a, b) = (magnitude(you), magnitude(other));
    let top = a.max(b);
    let gap = if top > 0.0 { (a - b).abs() / top } else { 0.0 };
    let (you_value, unit) = value_text(you, &t.unit);
    let (other_value, _) = value_text(other, &t.unit);
    // A clock title sends "HH:MM"; magnitude counts its minutes from 18:00.
    let clock = matches!(you, Value::String(_));
    let (delta, delta_unit) = delta_text(kind, &t.unit, clock, (a - b).abs());
    Bar {
        kind,
        title_id: t.title_id.clone(),
        title: any_title_name(&t.title_id),
        other: who,
        you_value,
        other_value,
        unit,
        delta,
        delta_unit,
        gap,
    }
}

/// The gap as signed text. Minus is behind (a take), plus is a lead (a hold).
/// A gap that shows as zero reads "±0", as a tie does: the holder keeps it.
fn delta_text(kind: Kind, unit: &str, clock: bool, diff: f64) -> (String, String) {
    let (number, unit) = if clock {
        clock_minutes(diff)
    } else if unit == "h" && diff < 1.0 {
        (grouped((diff * 60.0).round() as i64), "min".to_string())
    } else if unit == "h" {
        value_text(&json!(diff), "h")
    } else if unit == "min" || unit == "%" {
        (grouped(diff.round() as i64), unit.to_string())
    } else {
        (grouped(diff.round() as i64), String::new())
    };
    if number.chars().all(|c| matches!(c, '0' | '.' | ',')) {
        return ("\u{b1}0".to_string(), String::new());
    }
    let sign = match kind {
        Kind::Take => "\u{2212}",
        Kind::Hold => "+",
    };
    (format!("{sign}{number}"), unit)
}

/// A clock gap: minutes up to an hour, then hours.
fn clock_minutes(minutes: f64) -> (String, String) {
    if minutes.round() >= 60.0 {
        value_text(&json!(minutes / 60.0), "h")
    } else {
        (grouped(minutes.round() as i64), "min".to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn me() -> Person {
        Person::new("me", "Bob")
    }

    fn roster(id: &str) -> Person {
        match id {
            "me" => me(),
            "u2" => Person::new("u2", "Ebba"),
            "u3" => Person::new("u3", "Axel"),
            other => Person::unknown(other),
        }
    }

    fn title(id: &str, holder: &str, value: Value, unit: &str) -> Value {
        json!({"title_id": id, "holder_user_id": holder, "value": value, "unit": unit})
    }

    fn own(id: &str, value: Value, unit: &str) -> Value {
        json!({"title_id": id, "value": value, "unit": unit, "display": ""})
    }

    /// A pair of the viewer and `b`, with (title, viewer value, b value).
    fn pair(b: &str, values: &[(&str, Value, Value)]) -> Value {
        let values: Vec<Value> = values
            .iter()
            .map(|(id, a, bv)| json!({"title_id": id, "a_value": a, "b_value": bv}))
            .collect();
        json!({"a": "me", "b": b, "a_count": 1, "b_count": 1, "values": values})
    }

    fn feed(titles: Vec<Value>, own_values: Vec<Value>, pairs: Vec<Value>) -> Value {
        json!({"member_count": 3, "open_seats": 2, "titles": titles,
               "own_values": own_values, "pairs": pairs})
    }

    fn run(data: Value) -> Chase {
        chase(&data, &me(), &roster).expect("the zone data parses")
    }

    fn strip_ids(c: &Chase) -> Vec<(Kind, &str)> {
        c.strip()
            .iter()
            .map(|b| (b.kind, b.title_id.as_str()))
            .collect()
    }

    /// The crew of mockup R1: two belts to take, two held.
    fn r1() -> Value {
        feed(
            vec![
                title("locked_in", "u2", json!(14.2), "h"),
                title("the_carry", "u3", json!(4167.0), "h"),
                title("night_owl", "me", json!("03:14"), ""),
                title("the_collector", "me", json!(212), "installed"),
            ],
            vec![
                own("locked_in", json!(12.9), "h"),
                own("the_carry", json!(2927.0), "h"),
                own("night_owl", json!("03:14"), ""),
                own("the_collector", json!(212), "installed"),
            ],
            vec![
                pair(
                    "u2",
                    &[
                        ("night_owl", json!("03:14"), json!("03:02")),
                        ("the_collector", json!(212), json!(150)),
                    ],
                ),
                pair(
                    "u3",
                    &[
                        ("night_owl", json!("03:14"), json!("01:30")),
                        ("the_collector", json!(212), json!(203)),
                    ],
                ),
            ],
        )
    }

    #[test]
    fn the_strip_shows_the_closest_take_and_the_thinnest_hold() {
        let c = run(r1());
        assert_eq!(
            strip_ids(&c),
            vec![(Kind::Take, "locked_in"), (Kind::Hold, "night_owl")]
        );
        let take = c.strip()[0];
        assert_eq!(take.other.id, "u2");
        assert_eq!(
            (
                take.you_value.as_str(),
                take.other_value.as_str(),
                take.unit.as_str()
            ),
            ("12.9", "14.2", "h")
        );
        assert_eq!(
            (take.delta.as_str(), take.delta_unit.as_str()),
            ("\u{2212}1.3", "h")
        );
        let hold = c.strip()[1];
        assert_eq!(hold.other.id, "u2");
        assert_eq!(
            (hold.delta.as_str(), hold.delta_unit.as_str()),
            ("+12", "min")
        );
        assert_eq!(c.held, vec!["night_owl", "the_collector"]);
        assert_eq!(c.contested, 4);
    }

    #[test]
    fn the_lists_keep_every_belt_smallest_gap_first() {
        let c = run(r1());
        let takes: Vec<&str> = c.takes.iter().map(|b| b.title_id.as_str()).collect();
        let holds: Vec<&str> = c.holds.iter().map(|b| b.title_id.as_str()).collect();
        assert_eq!(takes, vec!["locked_in", "the_carry"]);
        assert_eq!(holds, vec!["night_owl", "the_collector"]);
    }

    #[test]
    fn the_runner_up_is_the_best_value_against_you() {
        let c = run(r1());
        let collector = c
            .holds
            .iter()
            .find(|b| b.title_id == "the_collector")
            .unwrap();
        assert_eq!(collector.other.id, "u3", "203 beats 150");
        assert_eq!(
            (collector.delta.as_str(), collector.delta_unit.as_str()),
            ("+9", "")
        );
        assert_eq!(collector.unit, "installed", "the values keep their unit");
    }

    #[test]
    fn has_a_life_goes_to_the_fewest_hours() {
        let c = run(feed(
            vec![title("has_a_life", "me", json!(2.0), "h")],
            vec![own("has_a_life", json!(2.0), "h")],
            vec![
                pair("u2", &[("has_a_life", json!(2.0), json!(3.5))]),
                pair("u3", &[("has_a_life", json!(2.0), json!(2.5))]),
            ],
        ));
        let hold = &c.holds[0];
        assert_eq!(
            hold.other.id, "u3",
            "2.5 h is closer to the fewest than 3.5 h"
        );
        assert_eq!(
            (hold.delta.as_str(), hold.delta_unit.as_str()),
            ("+30", "min")
        );
    }

    #[test]
    fn a_clock_title_counts_past_midnight() {
        // 23:50 is 350 minutes from 18:00, 01:10 is 430: 80 minutes behind.
        let c = run(feed(
            vec![title("night_owl", "u2", json!("01:10"), "")],
            vec![own("night_owl", json!("23:50"), "")],
            vec![],
        ));
        let take = &c.takes[0];
        assert_eq!(
            (take.delta.as_str(), take.delta_unit.as_str()),
            ("\u{2212}1.3", "h")
        );
        assert_eq!(
            (take.you_value.as_str(), take.other_value.as_str()),
            ("23:50", "01:10")
        );
        assert!((take.ratio() - 350.0 / 430.0).abs() < 1e-4);
    }

    #[test]
    fn a_tie_reads_plus_minus_zero_and_fills_the_bar() {
        let c = run(feed(
            vec![title("sampler", "u2", json!(5), "games")],
            vec![own("sampler", json!(5), "games")],
            vec![],
        ));
        let take = &c.takes[0];
        assert_eq!(
            (take.delta.as_str(), take.delta_unit.as_str()),
            ("\u{b1}0", "")
        );
        assert_eq!(take.ratio(), 1.0);
    }

    #[test]
    fn with_no_hold_the_strip_shows_two_takes() {
        let c = run(feed(
            vec![
                title("locked_in", "u2", json!(14.2), "h"),
                title("the_carry", "u3", json!(4167.0), "h"),
                title("sampler", "u3", json!(9), "games"),
            ],
            vec![
                own("locked_in", json!(12.9), "h"),
                own("the_carry", json!(2927.0), "h"),
                own("sampler", json!(4), "games"),
            ],
            vec![],
        ));
        assert_eq!(
            strip_ids(&c),
            vec![(Kind::Take, "locked_in"), (Kind::Take, "the_carry")]
        );
    }

    #[test]
    fn with_no_take_the_strip_shows_two_holds() {
        let c = run(feed(
            vec![
                title("night_owl", "me", json!("03:14"), ""),
                title("the_collector", "me", json!(212), "installed"),
                title("locked_in", "u2", json!(14.2), "h"),
            ],
            vec![
                own("night_owl", json!("03:14"), ""),
                own("the_collector", json!(212), "installed"),
            ],
            vec![pair(
                "u2",
                &[
                    ("night_owl", json!("03:14"), json!("03:02")),
                    ("the_collector", json!(212), json!(203)),
                ],
            )],
        ));
        assert_eq!(
            strip_ids(&c),
            vec![(Kind::Hold, "night_owl"), (Kind::Hold, "the_collector")]
        );
    }

    #[test]
    fn one_candidate_is_one_bar() {
        let c = run(feed(
            vec![title("locked_in", "u2", json!(14.2), "h")],
            vec![own("locked_in", json!(12.9), "h")],
            vec![],
        ));
        assert_eq!(strip_ids(&c), vec![(Kind::Take, "locked_in")]);
    }

    #[test]
    fn a_title_without_your_value_is_out_of_reach() {
        let c = run(feed(
            vec![title("the_carry", "u3", json!(4167.0), "h")],
            vec![own("locked_in", json!(12.9), "h")],
            vec![],
        ));
        assert!(c.takes.is_empty());
        assert!(c.strip().is_empty());
        assert_eq!(c.contested, 1);
    }

    #[test]
    fn a_crew_of_one_has_nothing_to_chase() {
        let c = run(
            json!({"member_count": 1, "open_seats": 4, "titles": null, "pairs": null,
                           "own_values": [own("locked_in", json!(12.9), "h")]}),
        );
        assert!(c.strip().is_empty());
        assert_eq!(c.contested, 0);
        assert!(c.held.is_empty());
        assert!(!c.game_rival);
        assert!(
            run(r1()).game_rival,
            "Ebba and Axel share Night Owl with you"
        );
    }

    #[test]
    fn voice_clips_and_wins_alone_are_no_game_rival() {
        let pairs = |title: &str| {
            feed(
                vec![title_v(title)],
                vec![],
                vec![pair("u2", &[(title, json!(5), json!(9))])],
            )
        };
        fn title_v(id: &str) -> Value {
            title(id, "u2", json!(9), "")
        }
        for id in ["mouth", "clipper", "winner"] {
            assert!(!run(pairs(id)).game_rival, "{id} is not game activity");
        }
        assert!(run(pairs("sampler")).game_rival);
    }

    #[test]
    fn a_session_title_has_its_name_and_a_bare_count() {
        let c = run(feed(
            vec![title("clipper", "u3", json!(41), "clips")],
            vec![own("clipper", json!(38), "clips")],
            vec![],
        ));
        let take = &c.takes[0];
        assert_eq!(take.title, "Clipper");
        assert_eq!(
            (take.delta.as_str(), take.delta_unit.as_str()),
            ("\u{2212}3", "")
        );
    }

    #[test]
    fn equal_gaps_keep_the_display_order() {
        // Both 10 % behind: The Carry comes before Locked In.
        let c = run(feed(
            vec![
                title("locked_in", "u2", json!(10.0), "h"),
                title("the_carry", "u3", json!(1000.0), "h"),
            ],
            vec![
                own("locked_in", json!(9.0), "h"),
                own("the_carry", json!(900.0), "h"),
            ],
            vec![],
        ));
        let takes: Vec<&str> = c.takes.iter().map(|b| b.title_id.as_str()).collect();
        assert_eq!(takes, vec!["the_carry", "locked_in"]);
    }

    #[test]
    fn deltas_read_like_the_mockup() {
        let d = |k, unit, clock, diff| delta_text(k, unit, clock, diff);
        assert_eq!(
            d(Kind::Take, "h", false, 1.3),
            ("\u{2212}1.3".into(), "h".into())
        );
        assert_eq!(
            d(Kind::Take, "h", false, 1240.0),
            ("\u{2212}1,240".into(), "h".into())
        );
        assert_eq!(d(Kind::Hold, "h", false, 0.5), ("+30".into(), "min".into()));
        assert_eq!(d(Kind::Hold, "", true, 12.0), ("+12".into(), "min".into()));
        assert_eq!(
            d(Kind::Take, "min", false, 140.0),
            ("\u{2212}140".into(), "min".into())
        );
        assert_eq!(
            d(Kind::Take, "%", false, 4.0),
            ("\u{2212}4".into(), "%".into())
        );
        assert_eq!(
            d(Kind::Hold, "installed", false, 9.0),
            ("+9".into(), "".into())
        );
        assert_eq!(
            d(Kind::Take, "h", false, 0.004),
            ("\u{b1}0".into(), "".into())
        );
    }
}
