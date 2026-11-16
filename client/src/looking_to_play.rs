//! Looking to play on desktop: the state of the asks and the rules of the
//! card and the ask popover (plans/LOOKING-TO-PLAY.md 3.1, 3.2, 3.6, 3.8).
//!
//! One message is an ask, its sender is the asker. The functions here take
//! plain data (the ask, the crew members, who is in voice, the viewer, the
//! time in Unix ms) and give plain structs, so the tests need no Slint.
//! `handlers/looking_to_play.rs` turns them into the Slint structs.

use std::collections::{BTreeSet, HashMap, HashSet};

use mello_core::looking_to_play::{Answer, Ask, AskState};

/// A crew above this many members is large (3.8): the card shows only who
/// is in, the popover offers the recent picks and a search.
pub const LARGE_CREW: usize = 10;
/// The rows of the card of a large ask before "{n} more in".
pub const ROW_LIMIT: usize = 6;
/// An open ask closes 30 minutes after the ask.
pub const OPEN_MS: i64 = 30 * 60_000;
/// The minute counts of the cards count again at most this often.
pub const REFRESH_MS: u64 = 15_000;
/// Members in voice or in a game that a large crew lists in the popover.
pub const ACTIVE_LIMIT: usize = 4;
/// Search matches that a large crew lists in the popover.
pub const MATCH_LIMIT: usize = 6;
/// Game chips after "Any game".
pub const GAME_LIMIT: usize = 3;
/// Faces of a recent group in the popover.
pub const FACE_LIMIT: usize = 3;

/// A crew member as the card and the popover need it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Person {
    pub id: String,
    pub name: String,
}

/// The state of one row of the card.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RowState {
    /// In or due, and in a voice channel of the crew now.
    Voice,
    /// Answered In, or the due time passed.
    In,
    /// Answered In 15 or In 30: the minutes until the member is due.
    Due(i64),
    /// No answer.
    None,
    /// Answered Not now.
    No,
}

impl RowState {
    /// The `state` of `LookingToPlayRow`.
    pub fn key(self) -> &'static str {
        match self {
            RowState::Voice => "voice",
            RowState::In => "in",
            RowState::Due(_) => "due",
            RowState::None => "none",
            RowState::No => "no",
        }
    }

    pub fn due_minutes(self) -> i64 {
        match self {
            RowState::Due(m) => m,
            _ => 0,
        }
    }

    /// In or due, in voice or not.
    pub fn is_in(self) -> bool {
        matches!(self, RowState::Voice | RowState::In | RowState::Due(_))
    }

    /// The order of the card: voice, in, due (soonest first), none, no.
    fn rank(self) -> (u8, i64) {
        match self {
            RowState::Voice => (0, 0),
            RowState::In => (1, 0),
            RowState::Due(m) => (2, m),
            RowState::None => (3, 0),
            RowState::No => (4, 0),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Row {
    pub user_id: String,
    pub name: String,
    pub state: RowState,
}

/// The card of one ask, before it becomes `LookingToPlayCardData`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Card {
    pub ask_id: String,
    /// "open", "on" or "closed".
    pub state: &'static str,
    pub closed_reason: String,
    pub asker_id: String,
    pub asker_name: String,
    /// Minutes until an open ask closes. 0 for a closed ask.
    pub minutes_left: i64,
    pub rows: Vec<Row>,
    pub more_in: usize,
    pub no_count: usize,
    pub viewer_is_asker: bool,
    /// "", "in", "in_15", "in_30" or "no".
    pub viewer_answer: &'static str,
    pub changing: bool,
    /// Unix ms of the close, 0 when not closed.
    pub closed_at_ms: i64,
    pub was_in: Vec<Row>,
}

/// What [`card`] needs.
pub struct CardInput<'a> {
    pub ask: &'a Ask,
    /// Every member of the crew, the viewer included.
    pub roster: &'a [Person],
    /// Who is in a voice channel of the crew now. `None` when the client
    /// has no voice state of the crew: then `arrived_at` decides.
    pub in_voice: Option<&'a HashSet<String>>,
    pub viewer_id: &'a str,
    pub now_ms: i64,
    /// The viewer pressed "{n} more in".
    pub expanded: bool,
    /// The viewer pressed Change.
    pub changing: bool,
}

/// Whole minutes until `at`, rounded up, never below 0.
pub fn minutes_until(at_ms: i64, now_ms: i64) -> i64 {
    let ms = (at_ms - now_ms).max(0);
    (ms + 59_999) / 60_000
}

/// The answer key that the card and the command use.
pub fn answer_key(answer: Answer) -> &'static str {
    match answer {
        Answer::In => "in",
        Answer::In15 => "in_15",
        Answer::In30 => "in_30",
        Answer::No => "no",
    }
}

/// The answer of a card press, `None` for an unknown key.
pub fn answer_of(key: &str) -> Option<Answer> {
    match key {
        "in" => Some(Answer::In),
        "in_15" => Some(Answer::In15),
        "in_30" => Some(Answer::In30),
        "no" => Some(Answer::No),
        _ => None,
    }
}

/// The asked members of an ask: the picked members, or every member of the
/// crew but the asker.
pub fn audience(ask: &Ask, roster: &[Person]) -> Vec<String> {
    let ids: Vec<String> = if ask.asks_everyone() {
        roster.iter().map(|p| p.id.clone()).collect()
    } else {
        ask.asked.clone()
    };
    let mut seen = HashSet::new();
    ids.into_iter()
        .filter(|id| *id != ask.asker_id && seen.insert(id.clone()))
        .collect()
}

/// The state of one member on the card of `ask`.
pub fn row_state(
    ask: &Ask,
    user_id: &str,
    in_voice: Option<&HashSet<String>>,
    now_ms: i64,
) -> RowState {
    let entry = ask.answers.get(user_id);
    // The asker counts as in from the start.
    let answer = entry
        .map(|a| a.answer)
        .or((user_id == ask.asker_id).then_some(Answer::In));
    let Some(answer) = answer else {
        return RowState::None;
    };
    if answer == Answer::No {
        return RowState::No;
    }
    let in_a_channel = match in_voice {
        Some(set) => set.contains(user_id),
        None => entry.is_some_and(|a| a.arrived_at != 0),
    };
    if in_a_channel {
        return RowState::Voice;
    }
    match answer {
        Answer::In15 | Answer::In30 => {
            let due_at = entry.map_or(0, |a| a.due_at);
            if due_at > now_ms {
                RowState::Due(minutes_until(due_at, now_ms))
            } else {
                RowState::In
            }
        }
        _ => RowState::In,
    }
}

/// Sort rows: by state, then the asker first, then by name.
fn sort_rows(rows: &mut [Row], asker_id: &str) {
    rows.sort_by(|a, b| {
        let key = |r: &Row| {
            (
                r.state.rank(),
                r.user_id != asker_id,
                r.name.to_lowercase(),
                r.user_id.clone(),
            )
        };
        key(a).cmp(&key(b))
    });
}

/// The card of an ask (3.6).
pub fn card(input: &CardInput<'_>) -> Card {
    let ask = input.ask;
    let names: HashMap<&str, &str> = input
        .roster
        .iter()
        .map(|p| (p.id.as_str(), p.name.as_str()))
        .collect();
    let asker_name = names
        .get(ask.asker_id.as_str())
        .map(|n| n.to_string())
        .unwrap_or_default();
    // A member who left the crew has no name: no row. The asker always has one.
    let known = |id: &str| id == ask.asker_id || names.contains_key(id);
    let row = |id: &str| Row {
        user_id: id.to_string(),
        name: names.get(id).map(|n| n.to_string()).unwrap_or_default(),
        state: row_state(ask, id, input.in_voice, input.now_ms),
    };

    let audience = audience(ask, input.roster);
    let large = audience.len() > LARGE_CREW;
    let mut rows: Vec<Row>;
    let mut more_in = 0;
    let mut no_count = 0;
    if !large {
        let mut ids: Vec<String> = vec![ask.asker_id.clone()];
        ids.extend(audience.iter().cloned());
        for id in ask.answers.keys() {
            if !ids.contains(id) {
                ids.push(id.clone());
            }
        }
        rows = ids
            .iter()
            .filter(|id| known(id))
            .map(|id| row(id))
            .collect();
        sort_rows(&mut rows, &ask.asker_id);
    } else {
        let mut ids: Vec<String> = vec![ask.asker_id.clone()];
        for (id, a) in &ask.answers {
            if a.answer.is_in() && !ids.contains(id) {
                ids.push(id.clone());
            }
        }
        rows = ids
            .iter()
            .filter(|id| known(id))
            .map(|id| row(id))
            .filter(|r| r.state.is_in())
            .collect();
        sort_rows(&mut rows, &ask.asker_id);
        if !input.expanded && rows.len() > ROW_LIMIT {
            more_in = rows.len() - ROW_LIMIT;
            rows.truncate(ROW_LIMIT);
        }
        no_count = ask
            .answers
            .iter()
            .filter(|(id, a)| **id != ask.asker_id && a.answer == Answer::No)
            .count();
    }

    let closed = ask.state == AskState::Closed;
    let was_in = if closed && ask.closed_reason == "done" {
        let mut ids: Vec<String> = vec![ask.asker_id.clone()];
        for (id, a) in &ask.answers {
            if a.answer.is_in() && !ids.contains(id) {
                ids.push(id.clone());
            }
        }
        let mut list: Vec<Row> = ids
            .iter()
            .filter(|id| known(id))
            .map(|id| row(id))
            .collect();
        // Who was in: the asker first, then by name.
        list.sort_by(|a, b| {
            (a.user_id != ask.asker_id, a.name.to_lowercase())
                .cmp(&(b.user_id != ask.asker_id, b.name.to_lowercase()))
        });
        list
    } else {
        Vec::new()
    };

    Card {
        ask_id: ask.ask_id.clone(),
        state: match ask.state {
            AskState::Open => "open",
            AskState::On => "on",
            AskState::Closed => "closed",
        },
        closed_reason: if closed {
            ask.closed_reason.clone()
        } else {
            String::new()
        },
        asker_id: ask.asker_id.clone(),
        asker_name,
        minutes_left: if closed {
            0
        } else {
            minutes_until(ask.opened_at + OPEN_MS, input.now_ms)
        },
        rows: if closed { Vec::new() } else { rows },
        more_in: if closed { 0 } else { more_in },
        no_count: if closed { 0 } else { no_count },
        viewer_is_asker: input.viewer_id == ask.asker_id,
        viewer_answer: ask
            .answers
            .get(input.viewer_id)
            .map_or("", |a| answer_key(a.answer)),
        changing: input.changing,
        closed_at_ms: if closed { ask.closed_at } else { 0 },
        was_in,
    }
}

/// The members of an ask who count for the voice channel: the asker and
/// the members who are in or due.
pub fn ask_people(ask: &Ask) -> Vec<String> {
    let mut out = vec![ask.asker_id.clone()];
    out.extend(ask.members_in().into_iter().map(str::to_string));
    out
}

/// A voice channel of the crew, as the join rule needs it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Channel {
    pub id: String,
    pub is_default: bool,
    pub members: Vec<String>,
}

/// The channel to join when the viewer answers In: the channel that holds
/// the most members of the ask, else the default channel (none of them is
/// in voice, or a tie). `None` when the viewer is in voice already, or the
/// crew has no channel.
pub fn channel_to_join(ask: &Ask, channels: &[Channel], viewer_in_voice: bool) -> Option<String> {
    if viewer_in_voice || channels.is_empty() {
        return None;
    }
    let people = ask_people(ask);
    let counts: Vec<usize> = channels
        .iter()
        .map(|c| c.members.iter().filter(|m| people.contains(m)).count())
        .collect();
    let best = counts.iter().copied().max().unwrap_or(0);
    if best > 0 && counts.iter().filter(|&&n| n == best).count() == 1 {
        let i = counts.iter().position(|&n| n == best)?;
        return Some(channels[i].id.clone());
    }
    channels
        .iter()
        .find(|c| c.is_default)
        .or_else(|| channels.first())
        .map(|c| c.id.clone())
}

// ---------------------------------------------------------------- popover

/// What the popover can say about a member.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PopoverPerson {
    pub id: String,
    pub name: String,
    /// The game line of the member list ("Valorant · 12m"), empty when not
    /// in a game.
    pub game_line: String,
    pub in_voice: bool,
}

/// What a member does now, in the popover list.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Doing {
    Game(String),
    InVoice,
    Nothing,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PopoverMember {
    pub id: String,
    pub doing: Doing,
    pub picked: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PopoverGroup {
    /// The index in the recent picks, sent back by a press.
    pub index: usize,
    /// The members of the group that the popover can name, the viewer out.
    pub ids: Vec<String>,
    pub picked: bool,
}

/// Who to ask, and the game. Picks are user ids.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Picks {
    pub everyone: bool,
    pub picked: BTreeSet<String>,
    pub search: String,
    /// The selected game id. `None`: Any game.
    pub game: Option<String>,
}

impl Picks {
    /// The picks at the start (3.6): Everyone in a crew of up to 10, else
    /// the newest recent group, else nothing.
    pub fn start(
        large: bool,
        recent: &[Vec<String>],
        known: &HashSet<String>,
        viewer: &str,
    ) -> Self {
        if !large {
            return Picks {
                everyone: true,
                ..Default::default()
            };
        }
        let picked = recent
            .first()
            .map(|g| {
                g.iter()
                    .filter(|id| id.as_str() != viewer && known.contains(id.as_str()))
                    .cloned()
                    .collect()
            })
            .unwrap_or_default();
        Picks {
            picked,
            ..Default::default()
        }
    }

    pub fn toggle_member(&mut self, id: &str) {
        self.everyone = false;
        if !self.picked.remove(id) {
            self.picked.insert(id.to_string());
        }
    }

    /// A group shows picked when all of its members are: a press then
    /// unpicks them, else it picks them all.
    pub fn toggle_group(&mut self, ids: &[String]) {
        if ids.is_empty() {
            return;
        }
        self.everyone = false;
        if ids.iter().all(|id| self.picked.contains(id)) {
            for id in ids {
                self.picked.remove(id);
            }
        } else {
            self.picked.extend(ids.iter().cloned());
        }
    }

    /// Picking Everyone unpicks every member.
    pub fn toggle_everyone(&mut self) {
        if self.everyone {
            self.everyone = false;
        } else {
            self.everyone = true;
            self.picked.clear();
        }
    }

    /// The `member_ids` of the ask: empty for Everyone. `None` when nothing
    /// is picked.
    pub fn member_ids(&self) -> Option<Vec<String>> {
        if self.everyone {
            Some(Vec::new())
        } else if self.picked.is_empty() {
            None
        } else {
            Some(self.picked.iter().cloned().collect())
        }
    }
}

/// The lists of the popover.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PopoverLists {
    pub large: bool,
    /// Members that Everyone reaches.
    pub member_count: usize,
    pub members: Vec<PopoverMember>,
    pub groups: Vec<PopoverGroup>,
}

fn doing(p: &PopoverPerson) -> Doing {
    if !p.game_line.is_empty() {
        Doing::Game(p.game_line.clone())
    } else if p.in_voice {
        Doing::InVoice
    } else {
        Doing::Nothing
    }
}

/// The member list and the groups of the popover (3.6, 3.8).
pub fn popover_lists(
    people: &[PopoverPerson],
    viewer: &str,
    picks: &Picks,
    recent: &[Vec<String>],
) -> PopoverLists {
    let others: Vec<&PopoverPerson> = people.iter().filter(|p| p.id != viewer).collect();
    let large = others.len() > LARGE_CREW;
    let member = |p: &PopoverPerson| PopoverMember {
        id: p.id.clone(),
        doing: doing(p),
        picked: picks.picked.contains(&p.id),
    };
    if !large {
        return PopoverLists {
            large,
            member_count: others.len(),
            members: others.iter().map(|p| member(p)).collect(),
            groups: Vec::new(),
        };
    }

    let mut listed: Vec<&PopoverPerson> = Vec::new();
    fn add<'a>(p: &'a PopoverPerson, listed: &mut Vec<&'a PopoverPerson>) {
        if !listed.iter().any(|q| q.id == p.id) {
            listed.push(p);
        }
    }
    for p in others
        .iter()
        .filter(|p| p.in_voice || !p.game_line.is_empty())
        .take(ACTIVE_LIMIT)
    {
        add(p, &mut listed);
    }
    let needle = picks.search.trim().to_lowercase();
    if !needle.is_empty() {
        for p in others
            .iter()
            .filter(|p| p.name.to_lowercase().contains(&needle))
            .take(MATCH_LIMIT)
        {
            add(p, &mut listed);
        }
    }
    for p in others.iter().filter(|p| picks.picked.contains(&p.id)) {
        add(p, &mut listed);
    }

    let known: HashSet<&str> = others.iter().map(|p| p.id.as_str()).collect();
    let groups = recent
        .iter()
        .enumerate()
        .filter_map(|(index, g)| {
            let ids: Vec<String> = g
                .iter()
                .filter(|id| known.contains(id.as_str()))
                .cloned()
                .collect();
            (!ids.is_empty()).then(|| PopoverGroup {
                index,
                picked: ids.iter().all(|id| picks.picked.contains(id)),
                ids,
            })
        })
        .collect();

    PopoverLists {
        large,
        member_count: others.len(),
        members: listed.into_iter().map(member).collect(),
        groups,
    }
}

/// A game the popover can offer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GameRef {
    pub game_id: String,
    pub name: String,
}

/// The id form of the library report and of the ask: `steam-730`,
/// `epic-<id>`, `gog-<id>`. A catalogue id ("counter-strike-2") or a
/// provisional one ("local-hades") is not that id.
pub fn is_library_id(id: &str) -> bool {
    ["steam-", "epic-", "gog-"]
        .iter()
        .any(|p| id.strip_prefix(p).is_some_and(|rest| !rest.is_empty()))
}

/// The game chips (3.6): the viewer's game, the crew's active games, then
/// the recent games, each once, at most [`GAME_LIMIT`]. A game without a
/// library id is left out: the ask must carry that id.
pub fn game_choices(
    viewer_game: Option<&GameRef>,
    crew_games: &[GameRef],
    recent: &[GameRef],
) -> Vec<GameRef> {
    let mut out: Vec<GameRef> = Vec::new();
    for g in viewer_game.into_iter().chain(crew_games).chain(recent) {
        if out.len() == GAME_LIMIT {
            break;
        }
        if is_library_id(&g.game_id)
            && !g.name.is_empty()
            && !out.iter().any(|o| o.game_id == g.game_id)
        {
            out.push(g.clone());
        }
    }
    out
}

// ---------------------------------------------------------------- state

/// The ask that the popover sent, until its answer.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PendingAsk {
    pub crew_id: String,
    pub game_id: Option<String>,
    pub everyone: bool,
}

/// All Looking to play state of the client. Lives in `AppContext`.
#[derive(Default)]
pub struct LtpState {
    /// Every ask the client knows, by ask id.
    pub asks: HashMap<String, Ask>,
    /// The recent picks of the user, by crew, newest first.
    pub picks: HashMap<String, Vec<Vec<String>>>,
    /// Asks whose card shows the four answers again (Change).
    pub changing: HashSet<String>,
    /// Asks whose card shows every member who is in ("{n} more in").
    pub expanded: HashSet<String>,
    /// The popover: its crew and what is picked.
    pub popover_crew: String,
    pub popover: Picks,
    /// The game chips of the popover, set when it opens.
    pub popover_games: Vec<GameRef>,
    /// The ask on its way. The popover button is off until it answers.
    pub pending: Option<PendingAsk>,
    /// The server code of the last failed ask, shown until the next ask or open.
    pub error_code: Option<String>,
    /// The icon of each game of an ask, by game id (resolved when an ask
    /// arrives, so a chat refresh needs no art lookup).
    pub game_icons: HashMap<String, (slint::Image, bool)>,
}

impl LtpState {
    /// The asks of a crew that are open or on, oldest first.
    pub fn live_asks(&self, crew_id: &str) -> Vec<&Ask> {
        let mut list: Vec<&Ask> = self
            .asks
            .values()
            .filter(|a| a.crew_id == crew_id && a.state != AskState::Closed)
            .collect();
        list.sort_by(|a, b| (a.opened_at, &a.ask_id).cmp(&(b.opened_at, &b.ask_id)));
        list
    }

    pub fn has_live_ask(&self, crew_id: &str) -> bool {
        self.asks
            .values()
            .any(|a| a.crew_id == crew_id && a.state != AskState::Closed)
    }

    /// Keep an ask. A newer state replaces the older one.
    pub fn store(&mut self, ask: Ask) {
        self.changing.remove(&ask.ask_id);
        self.asks.insert(ask.ask_id.clone(), ask);
    }

    /// True when `ask` is the answer to the pending ask: the user's own new
    /// ask, or the In on an open ask to everyone for the same game that the
    /// server counted the ask as (3.1).
    pub fn answers_pending(&self, ask: &Ask, viewer: &str) -> bool {
        let Some(p) = &self.pending else {
            return false;
        };
        if ask.crew_id != p.crew_id {
            return false;
        }
        if ask.asker_id == viewer {
            return ask.state != AskState::Closed;
        }
        let game = ask.game.as_ref().map(|g| g.game_id.clone());
        p.everyone
            && ask.asks_everyone()
            && game == p.game_id
            && ask
                .answers
                .get(viewer)
                .is_some_and(|a| a.answer == Answer::In)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mello_core::looking_to_play::{AskAnswer, AskGame};
    use std::collections::BTreeMap;

    const NOW: i64 = 1_790_000_000_000;
    const MIN: i64 = 60_000;

    fn people(n: usize) -> Vec<Person> {
        (0..n)
            .map(|i| Person {
                id: format!("u{i}"),
                name: format!("name{i:02}"),
            })
            .collect()
    }

    fn ask(asker: &str, asked: &[&str], answers: &[(&str, Answer, i64, i64)]) -> Ask {
        let mut map = BTreeMap::new();
        for (id, answer, due_at, arrived_at) in answers {
            map.insert(
                id.to_string(),
                AskAnswer {
                    answer: *answer,
                    at: NOW - MIN,
                    due_at: *due_at,
                    arrived_at: *arrived_at,
                },
            );
        }
        Ask {
            ask_id: "a1".into(),
            crew_id: "c1".into(),
            message_id: "m1".into(),
            asker_id: asker.into(),
            asked: asked.iter().map(|s| s.to_string()).collect(),
            game: None,
            opened_at: NOW - 5 * MIN,
            state: AskState::Open,
            closed_reason: String::new(),
            closed_at: 0,
            answers: map,
        }
    }

    fn card_of(a: &Ask, roster: &[Person], voice: Option<&HashSet<String>>) -> Card {
        card(&CardInput {
            ask: a,
            roster,
            in_voice: voice,
            viewer_id: "u1",
            now_ms: NOW,
            expanded: false,
            changing: false,
        })
    }

    fn ids(rows: &[Row]) -> Vec<&str> {
        rows.iter().map(|r| r.user_id.as_str()).collect()
    }

    #[test]
    fn a_small_ask_has_a_row_for_the_asker_and_each_asked_member() {
        let roster = people(5);
        let a = ask("u0", &[], &[("u0", Answer::In, 0, 0)]);
        let c = card_of(&a, &roster, None);
        assert_eq!(ids(&c.rows), ["u0", "u1", "u2", "u3", "u4"]);
        assert_eq!(c.rows[0].state, RowState::In);
        assert!(c.rows[1..].iter().all(|r| r.state == RowState::None));
        assert_eq!((c.more_in, c.no_count), (0, 0));
    }

    #[test]
    fn a_member_who_was_not_asked_but_answered_gets_a_row() {
        let roster = people(6);
        let a = ask(
            "u0",
            &["u1", "u2"],
            &[("u0", Answer::In, 0, 0), ("u5", Answer::In, 0, 0)],
        );
        let c = card_of(&a, &roster, None);
        assert_eq!(ids(&c.rows), ["u0", "u5", "u1", "u2"]);
    }

    #[test]
    fn the_order_is_voice_in_due_soonest_first_none_then_no() {
        let roster = people(8);
        let a = ask(
            "u3",
            &[],
            &[
                ("u3", Answer::In, 0, 0),
                ("u1", Answer::No, 0, 0),
                ("u2", Answer::In30, NOW + 20 * MIN, 0),
                ("u4", Answer::In15, NOW + 4 * MIN + 1, 0),
                ("u5", Answer::In, 0, 0),
                ("u6", Answer::In, 0, 0),
            ],
        );
        let voice: HashSet<String> = ["u6".to_string()].into();
        let c = card_of(&a, &roster, Some(&voice));
        assert_eq!(
            ids(&c.rows),
            ["u6", "u3", "u5", "u4", "u2", "u0", "u7", "u1"],
            "voice, in (the asker first), due soonest first, none by name, no"
        );
        assert_eq!(c.rows[3].state, RowState::Due(5), "minutes round up");
        assert_eq!(c.rows[4].state, RowState::Due(20));
        assert_eq!(c.rows[7].state, RowState::No);
    }

    #[test]
    fn a_passed_due_time_is_in() {
        let roster = people(3);
        let a = ask(
            "u0",
            &[],
            &[("u0", Answer::In, 0, 0), ("u1", Answer::In15, NOW - MIN, 0)],
        );
        let c = card_of(&a, &roster, None);
        assert_eq!(c.rows[1].user_id, "u1");
        assert_eq!(c.rows[1].state, RowState::In);
    }

    #[test]
    fn voice_comes_from_the_client_voice_state_and_else_from_arrived_at() {
        let roster = people(3);
        let a = ask(
            "u0",
            &[],
            &[("u0", Answer::In, 0, NOW - MIN), ("u1", Answer::In, 0, 0)],
        );
        // No client voice state: arrived_at decides.
        let c = card_of(&a, &roster, None);
        assert_eq!(c.rows[0].state, RowState::Voice);
        assert_eq!(c.rows[1].state, RowState::In);
        // The client's voice state wins over arrived_at.
        let voice: HashSet<String> = ["u1".to_string()].into();
        let c = card_of(&a, &roster, Some(&voice));
        let state = |id: &str| c.rows.iter().find(|r| r.user_id == id).map(|r| r.state);
        assert_eq!(state("u1"), Some(RowState::Voice));
        assert_eq!(state("u0"), Some(RowState::In));
        // A member with no answer is not "voice", in voice or not.
        let voice: HashSet<String> = ["u2".to_string()].into();
        let c = card_of(&a, &roster, Some(&voice));
        assert_eq!(state_of(&c, "u2"), Some(RowState::None));
    }

    fn state_of(c: &Card, id: &str) -> Option<RowState> {
        c.rows.iter().find(|r| r.user_id == id).map(|r| r.state)
    }

    #[test]
    fn a_large_ask_shows_who_is_in_up_to_six_and_counts_the_rest() {
        let roster = people(20);
        let mut answers: Vec<(String, Answer)> = vec![("u0".into(), Answer::In)];
        for i in 1..=8 {
            answers.push((format!("u{i}"), Answer::In));
        }
        for i in 9..=11 {
            answers.push((format!("u{i}"), Answer::No));
        }
        let refs: Vec<(&str, Answer, i64, i64)> = answers
            .iter()
            .map(|(id, a)| (id.as_str(), *a, 0, 0))
            .collect();
        let a = ask("u0", &[], &refs);
        let c = card_of(&a, &roster, None);
        assert_eq!(c.rows.len(), ROW_LIMIT);
        assert_eq!(c.rows[0].user_id, "u0", "the asker first");
        assert!(c.rows.iter().all(|r| r.state.is_in()));
        assert_eq!(c.more_in, 9 - ROW_LIMIT);
        assert_eq!(c.no_count, 3, "Not now is a count, with no rows");

        let expanded = card(&CardInput {
            ask: &a,
            roster: &roster,
            in_voice: None,
            viewer_id: "u1",
            now_ms: NOW,
            expanded: true,
            changing: false,
        });
        assert_eq!(expanded.rows.len(), 9, "expanded: no limit of six");
        assert_eq!(expanded.more_in, 0);
    }

    #[test]
    fn a_small_ask_has_no_count_of_not_now() {
        let roster = people(4);
        let a = ask(
            "u0",
            &[],
            &[("u0", Answer::In, 0, 0), ("u2", Answer::No, 0, 0)],
        );
        let c = card_of(&a, &roster, None);
        assert_eq!(c.no_count, 0, "a member who said Not now has a row");
        assert_eq!(state_of(&c, "u2"), Some(RowState::No));
    }

    #[test]
    fn eleven_picked_members_make_a_large_card() {
        let roster = people(15);
        let picked: Vec<String> = (1..=11).map(|i| format!("u{i}")).collect();
        let picked: Vec<&str> = picked.iter().map(String::as_str).collect();
        let a = ask("u0", &picked, &[("u0", Answer::In, 0, 0)]);
        let c = card_of(&a, &roster, None);
        assert_eq!(ids(&c.rows), ["u0"], "only who is in");
    }

    #[test]
    fn minutes_left_count_to_thirty_minutes_after_the_ask_and_stop_at_zero() {
        let roster = people(2);
        let mut a = ask("u0", &[], &[]);
        a.opened_at = NOW - 5 * MIN - 1;
        assert_eq!(card_of(&a, &roster, None).minutes_left, 25);
        a.opened_at = NOW - 40 * MIN;
        assert_eq!(card_of(&a, &roster, None).minutes_left, 0);
        assert_eq!(minutes_until(NOW + 1, NOW), 1);
    }

    #[test]
    fn a_closed_card_names_who_was_in_only_when_done() {
        let roster = people(4);
        let mut a = ask(
            "u2",
            &[],
            &[
                ("u2", Answer::In, 0, 0),
                ("u3", Answer::In15, 0, 0),
                ("u0", Answer::In, 0, 0),
                ("u1", Answer::No, 0, 0),
            ],
        );
        a.state = AskState::Closed;
        a.closed_at = NOW - MIN;
        a.closed_reason = "done".into();
        let c = card_of(&a, &roster, None);
        assert_eq!(c.state, "closed");
        assert_eq!(
            ids(&c.was_in),
            ["u2", "u0", "u3"],
            "the asker, then by name"
        );
        assert!(c.rows.is_empty());
        assert_eq!(c.closed_at_ms, NOW - MIN);
        for reason in ["nobody", "all_no", "cancelled"] {
            a.closed_reason = reason.into();
            let c = card_of(&a, &roster, None);
            assert!(c.was_in.is_empty(), "{reason}: no list");
            assert_eq!(c.closed_reason, reason);
        }
    }

    #[test]
    fn the_viewer_flags_follow_the_ask() {
        let roster = people(3);
        let a = ask(
            "u1",
            &[],
            &[("u1", Answer::In, 0, 0), ("u2", Answer::In30, NOW + MIN, 0)],
        );
        let c = card_of(&a, &roster, None);
        assert!(c.viewer_is_asker);
        assert_eq!(c.asker_name, "name01");
        let c = card(&CardInput {
            ask: &a,
            roster: &roster,
            in_voice: None,
            viewer_id: "u2",
            now_ms: NOW,
            expanded: false,
            changing: true,
        });
        assert!(!c.viewer_is_asker);
        assert_eq!(c.viewer_answer, "in_30");
        assert!(c.changing);
    }

    fn channel(id: &str, default: bool, members: &[&str]) -> Channel {
        Channel {
            id: id.into(),
            is_default: default,
            members: members.iter().map(|s| s.to_string()).collect(),
        }
    }

    #[test]
    fn in_joins_the_channel_with_the_most_members_of_the_ask() {
        let a = ask(
            "u0",
            &[],
            &[
                ("u0", Answer::In, 0, 0),
                ("u1", Answer::In15, NOW + MIN, 0),
                ("u2", Answer::In, 0, 0),
                ("u3", Answer::No, 0, 0),
            ],
        );
        let channels = [
            channel("lobby", true, &["u3", "u9"]),
            channel("ranked", false, &["u0", "u1"]),
            channel("chill", false, &["u2"]),
        ];
        assert_eq!(
            channel_to_join(&a, &channels, false).as_deref(),
            Some("ranked")
        );
        // A tie goes to the default channel.
        let tie = [
            channel("lobby", true, &[]),
            channel("ranked", false, &["u0"]),
            channel("chill", false, &["u2"]),
        ];
        assert_eq!(channel_to_join(&a, &tie, false).as_deref(), Some("lobby"));
        // Nobody of the ask in voice: the default channel.
        let none = [
            channel("ranked", false, &["u3"]),
            channel("lobby", true, &[]),
        ];
        assert_eq!(channel_to_join(&a, &none, false).as_deref(), Some("lobby"));
        // The viewer is in voice already: no join.
        assert_eq!(channel_to_join(&a, &channels, true), None);
        assert_eq!(channel_to_join(&a, &[], false), None);
    }

    fn pp(id: &str, name: &str, game: &str, voice: bool) -> PopoverPerson {
        PopoverPerson {
            id: id.into(),
            name: name.into(),
            game_line: game.into(),
            in_voice: voice,
        }
    }

    #[test]
    fn a_small_crew_lists_every_member_but_the_viewer_with_everyone_picked() {
        let crew = vec![
            pp("me", "bobdev", "", false),
            pp("u1", "k0ji", "Counter-Strike 2 · 40m", true),
            pp("u2", "owe", "", true),
            pp("u3", "sannah", "", false),
        ];
        let known: HashSet<String> = crew.iter().map(|p| p.id.clone()).collect();
        let picks = Picks::start(false, &[vec!["u1".into()]], &known, "me");
        assert!(picks.everyone);
        assert!(picks.picked.is_empty());
        let l = popover_lists(&crew, "me", &picks, &[vec!["u1".into()]]);
        assert!(!l.large);
        assert_eq!(l.member_count, 3);
        assert!(l.groups.is_empty(), "no groups in a small crew");
        let ids: Vec<&str> = l.members.iter().map(|m| m.id.as_str()).collect();
        assert_eq!(ids, ["u1", "u2", "u3"]);
        assert_eq!(
            l.members[0].doing,
            Doing::Game("Counter-Strike 2 · 40m".into())
        );
        assert_eq!(l.members[1].doing, Doing::InVoice);
        assert_eq!(l.members[2].doing, Doing::Nothing);
    }

    fn large_crew() -> Vec<PopoverPerson> {
        let mut crew = vec![pp("me", "bobdev", "", true)];
        for i in 1..=14 {
            let game = if i <= 3 { "Valheim · 1h" } else { "" };
            let voice = (4..=6).contains(&i);
            crew.push(pp(&format!("u{i}"), &format!("member{i:02}"), game, voice));
        }
        crew
    }

    #[test]
    fn a_large_crew_lists_who_is_on_the_matches_and_the_picks() {
        let crew = large_crew();
        let known: HashSet<String> = crew.iter().map(|p| p.id.clone()).collect();
        let recent = vec![
            vec!["u12".to_string(), "u13".into(), "me".into()],
            vec!["u9".into()],
        ];
        let mut picks = Picks::start(true, &recent, &known, "me");
        assert!(!picks.everyone);
        let picked: Vec<&str> = picks.picked.iter().map(String::as_str).collect();
        assert_eq!(picked, ["u12", "u13"], "the newest group, the viewer out");

        let l = popover_lists(&crew, "me", &picks, &recent);
        assert!(l.large);
        assert_eq!(l.member_count, 14);
        let ids: Vec<&str> = l.members.iter().map(|m| m.id.as_str()).collect();
        assert_eq!(
            ids,
            ["u1", "u2", "u3", "u4", "u12", "u13"],
            "4 on now, then the picks"
        );
        assert_eq!(l.groups.len(), 2);
        assert!(l.groups[0].picked);
        assert_eq!(l.groups[0].ids, ["u12", "u13"]);
        assert!(!l.groups[1].picked);

        picks.search = "MEMBER1".into();
        let l = popover_lists(&crew, "me", &picks, &recent);
        let ids: Vec<&str> = l.members.iter().map(|m| m.id.as_str()).collect();
        assert_eq!(
            ids,
            ["u1", "u2", "u3", "u4", "u10", "u11", "u12", "u13", "u14"],
            "the matches ignore case, at most 6, each member once"
        );
    }

    #[test]
    fn a_large_crew_with_no_recent_picks_starts_with_nothing_picked() {
        let crew = large_crew();
        let known: HashSet<String> = crew.iter().map(|p| p.id.clone()).collect();
        let picks = Picks::start(true, &[], &known, "me");
        assert!(!picks.everyone);
        assert_eq!(picks.member_ids(), None, "the button is off");
    }

    #[test]
    fn the_pick_rules() {
        let mut p = Picks {
            everyone: true,
            ..Default::default()
        };
        assert_eq!(p.member_ids(), Some(Vec::new()), "Everyone: no member ids");
        p.toggle_member("u2");
        assert!(!p.everyone, "a member unpicks Everyone");
        assert_eq!(p.member_ids(), Some(vec!["u2".to_string()]));

        let group = vec!["u2".to_string(), "u3".into()];
        p.toggle_group(&group);
        assert_eq!(p.member_ids(), Some(group.clone()), "a group picks all");
        p.toggle_group(&group);
        assert_eq!(p.member_ids(), None, "a picked group unpicks all");

        p.toggle_member("u4");
        p.toggle_everyone();
        assert!(p.everyone);
        assert!(p.picked.is_empty(), "Everyone unpicks the members");
        p.toggle_group(&group);
        assert!(!p.everyone, "a group unpicks Everyone");
        p.toggle_everyone();
        p.toggle_everyone();
        assert_eq!(p.member_ids(), None, "Everyone pressed again: nothing");
    }

    fn g(id: &str, name: &str) -> GameRef {
        GameRef {
            game_id: id.into(),
            name: name.into(),
        }
    }

    #[test]
    fn the_games_come_in_order_once_each_and_only_with_a_library_id() {
        let viewer = g("steam-730", "Counter-Strike 2");
        let crew = [
            g("counter-strike-2", "Counter-Strike 2"),
            g("steam-730", "Counter-Strike 2"),
            g("steam-548430", "Deep Rock Galactic"),
        ];
        let recent = [
            g("steam-548430", "Deep Rock Galactic"),
            g("gog-1", "Valheim"),
            g("epic-x", "Fortnite"),
        ];
        let out = game_choices(Some(&viewer), &crew, &recent);
        let ids: Vec<&str> = out.iter().map(|g| g.game_id.as_str()).collect();
        assert_eq!(ids, ["steam-730", "steam-548430", "gog-1"]);
        let out = game_choices(None, &[], &recent);
        let ids: Vec<&str> = out.iter().map(|g| g.game_id.as_str()).collect();
        assert_eq!(ids, ["steam-548430", "gog-1", "epic-x"]);
        assert!(!is_library_id("steam-"));
        assert!(!is_library_id("local-hades"));
    }

    #[test]
    fn the_answer_to_a_pending_ask() {
        let mut s = LtpState {
            pending: Some(PendingAsk {
                crew_id: "c1".into(),
                game_id: Some("steam-730".into()),
                everyone: true,
            }),
            ..Default::default()
        };
        let own = ask("me", &[], &[("me", Answer::In, 0, 0)]);
        assert!(s.answers_pending(&own, "me"));
        let mut joined = ask(
            "u2",
            &[],
            &[("u2", Answer::In, 0, 0), ("me", Answer::In, 0, 0)],
        );
        joined.game = Some(AskGame {
            game_id: "steam-730".into(),
            name: "Counter-Strike 2".into(),
        });
        assert!(s.answers_pending(&joined, "me"), "the ask counted as In");
        joined.game = None;
        assert!(!s.answers_pending(&joined, "me"), "another game");
        s.pending = None;
        assert!(!s.answers_pending(&own, "me"));
    }
}
