//! Looking to play: a member asks the crew, or picked members, if they are
//! up for a game (plans/LOOKING-TO-PLAY.md section 3). One such message is an
//! ask, and the member who sends it is the asker.
//!
//! The server holds the state of an ask and sends the whole of it after each
//! change (`looking_to_play_store.go`). These types mirror that state.

use std::collections::BTreeMap;

use serde::{Deserialize, Deserializer, Serialize};

use crate::events::ChatMessage;

/// The `event` of the system message that carries an ask. Its `data` has the
/// `ask_id`.
pub const EVENT: &str = "looking_to_play";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AskState {
    /// Asked, nobody else is in yet.
    Open,
    /// At least one other member is in.
    On,
    /// Over. The card keeps its last state in chat.
    Closed,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Answer {
    #[serde(rename = "in")]
    In,
    #[serde(rename = "in_15")]
    In15,
    #[serde(rename = "in_30")]
    In30,
    #[serde(rename = "no")]
    No,
}

impl Answer {
    /// In, or due in 15 or 30 minutes.
    pub fn is_in(self) -> bool {
        self != Answer::No
    }
}

/// The game of an ask. `game_id` is the stable id of the library report and
/// of the presence ("steam-730").
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AskGame {
    pub game_id: String,
    pub name: String,
}

/// The answer of one member. Times are Unix milliseconds, 0 for "not set".
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AskAnswer {
    pub answer: Answer,
    pub at: i64,
    /// When a member who answered In 15 or In 30 is due.
    #[serde(default)]
    pub due_at: i64,
    /// When the member joined a voice channel of the crew.
    #[serde(default)]
    pub arrived_at: i64,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Ask {
    pub ask_id: String,
    pub crew_id: String,
    /// The chat message of the ask.
    #[serde(default)]
    pub message_id: String,
    pub asker_id: String,
    /// The picked members. Empty means the ask goes to everyone.
    #[serde(default, deserialize_with = "null_as_empty")]
    pub asked: Vec<String>,
    #[serde(default)]
    pub game: Option<AskGame>,
    pub opened_at: i64,
    pub state: AskState,
    /// `nobody`, `all_no`, `cancelled` or `done`. Empty while not closed.
    #[serde(default)]
    pub closed_reason: String,
    #[serde(default)]
    pub closed_at: i64,
    /// By user id. The asker is in from the start.
    #[serde(default)]
    pub answers: BTreeMap<String, AskAnswer>,
}

fn null_as_empty<'de, D: Deserializer<'de>>(d: D) -> Result<Vec<String>, D::Error> {
    Ok(Option::<Vec<String>>::deserialize(d)?.unwrap_or_default())
}

impl Ask {
    pub fn asks_everyone(&self) -> bool {
        self.asked.is_empty()
    }

    /// The members who are in or due, the asker left out.
    pub fn members_in(&self) -> Vec<&str> {
        self.answers
            .iter()
            .filter(|(id, a)| id.as_str() != self.asker_id && a.answer.is_in())
            .map(|(id, _)| id.as_str())
            .collect()
    }
}

/// The id of the ask that a chat message carries, if it carries one.
pub fn ask_id_of(message: &ChatMessage) -> Option<String> {
    if message.event.as_deref() != Some(EVENT) {
        return None;
    }
    message
        .data
        .as_ref()?
        .get("ask_id")?
        .as_str()
        .filter(|id| !id.is_empty())
        .map(str::to_string)
}

/// The ask ids of loaded messages, in order, each once.
pub fn ask_ids(messages: &[ChatMessage]) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for id in messages.iter().filter_map(ask_id_of) {
        if !out.contains(&id) {
            out.push(id);
        }
    }
    out
}

/// The reply of `looking_to_play_ask`, `_answer` and `_cancel`.
#[derive(Debug, Clone, Deserialize)]
pub struct AskReply {
    pub ask: Ask,
    /// The call made no ask: it counted as In on the open ask to everyone
    /// for the same game.
    #[serde(default)]
    pub joined: bool,
}

/// The reply of `looking_to_play_list`.
#[derive(Debug, Clone, Deserialize)]
pub struct AskListReply {
    #[serde(default)]
    pub asks: Vec<Ask>,
}

#[cfg(test)]
mod tests {
    use super::*;

    const OPEN: &str = r#"{"ask_id":"a1","crew_id":"c1","message_id":"m1","asker_id":"u1",
        "asked":[],"opened_at":1790000000000,"state":"open",
        "answers":{"u1":{"answer":"in","at":1790000000000}}}"#;

    #[test]
    fn decodes_the_state_that_the_server_sends() {
        let ask: Ask = serde_json::from_str(OPEN).expect("an open ask decodes");
        assert_eq!(ask.state, AskState::Open);
        assert!(ask.asks_everyone());
        assert!(ask.members_in().is_empty(), "the asker is not in the list");
        assert_eq!(ask.game, None);
        assert_eq!(ask.closed_reason, "");

        let on: Ask = serde_json::from_str(
            r#"{"ask_id":"a1","crew_id":"c1","message_id":"m1","asker_id":"u1",
            "asked":["u2","u3"],"game":{"game_id":"steam-730","name":"Counter-Strike 2"},
            "opened_at":1,"state":"on","second_call_at":5,
            "answers":{"u1":{"answer":"in","at":1,"arrived_at":4},
                       "u2":{"answer":"in_15","at":2,"due_at":900002},
                       "u3":{"answer":"no","at":3}}}"#,
        )
        .expect("an ask that is on decodes");
        assert_eq!(on.state, AskState::On);
        assert_eq!(on.members_in(), vec!["u2"]);
        assert_eq!(on.answers["u2"].answer, Answer::In15);
        assert_eq!(on.answers["u2"].due_at, 900_002);
        assert_eq!(
            on.game.as_ref().map(|g| g.game_id.as_str()),
            Some("steam-730")
        );
    }

    /// An older server build could send `"asked": null`.
    #[test]
    fn a_null_list_of_picked_members_is_an_ask_to_everyone() {
        let ask: Ask = serde_json::from_str(&OPEN.replace(r#""asked":[]"#, r#""asked":null"#))
            .expect("decodes");
        assert!(ask.asks_everyone());
    }

    #[test]
    fn an_answer_goes_out_as_the_server_names_it() {
        for (answer, wire) in [
            (Answer::In, "\"in\""),
            (Answer::In15, "\"in_15\""),
            (Answer::In30, "\"in_30\""),
            (Answer::No, "\"no\""),
        ] {
            assert_eq!(serde_json::to_string(&answer).unwrap(), wire);
        }
    }
}
