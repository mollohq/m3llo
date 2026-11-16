//! Looking to play: the commands of an ask and the load of the asks that the
//! chat history shows (plans/LOOKING-TO-PLAY.md section 3.5).

use crate::events::{ChatMessage, Event};
use crate::looking_to_play::{self, Answer, AskGame};

impl super::Client {
    pub(super) async fn handle_looking_to_play_ask(
        &self,
        crew_id: &str,
        game: Option<AskGame>,
        member_ids: Vec<String>,
    ) {
        match self
            .nakama
            .looking_to_play_ask(crew_id, game.as_ref(), &member_ids)
            .await
        {
            Ok(reply) => {
                log::info!(
                    "[looking-to-play] ask {} in crew {} (joined={})",
                    reply.ask.ask_id,
                    crew_id,
                    reply.joined
                );
                // The notification reaches every member, this user included;
                // the answer makes the card show at once.
                let _ = self.event_tx.send(Event::LookingToPlayUpdated {
                    ask: reply.ask,
                    notify: false,
                });
                if let Some(groups) = reply.recent_picks {
                    let _ = self.event_tx.send(Event::LookingToPlayPicks {
                        crew_id: crew_id.to_string(),
                        groups,
                    });
                }
            }
            Err(e) => self.looking_to_play_failed("ask", "", &e),
        }
    }

    pub(super) async fn handle_looking_to_play_answer(&self, ask_id: &str, answer: Answer) {
        match self.nakama.looking_to_play_answer(ask_id, answer).await {
            Ok(reply) => {
                log::info!("[looking-to-play] answered {:?} on {}", answer, ask_id);
                let _ = self.event_tx.send(Event::LookingToPlayUpdated {
                    ask: reply.ask,
                    notify: false,
                });
            }
            Err(e) => self.looking_to_play_failed("answer", ask_id, &e),
        }
    }

    pub(super) async fn handle_looking_to_play_cancel(&self, ask_id: &str) {
        match self.nakama.looking_to_play_cancel(ask_id).await {
            Ok(reply) => {
                log::info!("[looking-to-play] cancelled {}", ask_id);
                let _ = self.event_tx.send(Event::LookingToPlayUpdated {
                    ask: reply.ask,
                    notify: false,
                });
            }
            Err(e) => self.looking_to_play_failed("cancel", ask_id, &e),
        }
    }

    fn looking_to_play_failed(&self, what: &str, ask_id: &str, e: &crate::error::Error) {
        log::warn!("[looking-to-play] {} failed: {}", what, e);
        let _ = self.event_tx.send(Event::LookingToPlayFailed {
            ask_id: ask_id.to_string(),
            code: e.server_token().unwrap_or_default(),
        });
    }

    /// Loads the asks that `messages` carry, the open asks of the crew and
    /// the recent picks of the user, in one call. It runs after each load of
    /// chat history.
    pub(super) async fn load_looking_to_play(&self, crew_id: &str, messages: &[ChatMessage]) {
        let ids = looking_to_play::ask_ids(messages);
        match self.nakama.looking_to_play_list(crew_id, &ids).await {
            Ok(reply) => {
                // The picks go out with no ask too: the ask popover needs them.
                let _ = self.event_tx.send(Event::LookingToPlayPicks {
                    crew_id: crew_id.to_string(),
                    groups: reply.recent_picks,
                });
                if reply.asks.is_empty() {
                    return;
                }
                let _ = self.event_tx.send(Event::LookingToPlayLoaded {
                    crew_id: crew_id.to_string(),
                    asks: reply.asks,
                });
            }
            Err(e) => log::warn!("[looking-to-play] asks for {} failed: {}", crew_id, e),
        }
    }
}
