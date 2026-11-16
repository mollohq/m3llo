//! Looking to play: the events of an ask (plans/LOOKING-TO-PLAY.md).
//!
//! Phase 2 of the plan ends at mello-core. The card, the toast and the ask
//! popover are phase 3: until then the client takes the events and logs them,
//! so a developer sees that the core side works.

use mello_core::Event;

use crate::app_context::AppContext;

pub fn handle(_ctx: &AppContext, event: Event) {
    match event {
        Event::LookingToPlayUpdated { ask, notify } => {
            log::debug!(
                "[looking-to-play] ask {} in crew {} is {:?}, {} in, notify={}",
                ask.ask_id,
                ask.crew_id,
                ask.state,
                ask.members_in().len(),
                notify
            );
        }
        Event::LookingToPlayPicks { crew_id, groups } => {
            log::debug!(
                "[looking-to-play] {} recent picks for crew {}",
                groups.len(),
                crew_id
            );
        }
        Event::LookingToPlayLoaded { crew_id, asks } => {
            log::debug!(
                "[looking-to-play] {} asks loaded for crew {}",
                asks.len(),
                crew_id
            );
        }
        Event::LookingToPlayFailed { ask_id, code } => {
            log::warn!("[looking-to-play] command failed: ask={ask_id:?} code={code:?}");
        }
        _ => {}
    }
}
