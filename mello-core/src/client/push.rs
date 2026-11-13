//! Remote push: device registration (spec 23 §3), the activity report the
//! server uses to hold phone pushes while the user is at this app (§6.2), and
//! the push switches of a crew (plans/LOOKING-TO-PLAY.md section 4).
//! Registration and the report are best-effort: a failed call is logged, never
//! surfaced, and retried later. A failed switch call is reported, so the UI
//! can put the switch back.

use crate::activity::{self, ActivityInputs};
use crate::events::Event;

impl super::Client {
    pub(super) async fn handle_register_push_token(
        &mut self,
        token: String,
        platform: &str,
        environment: Option<String>,
    ) {
        let environment = environment.unwrap_or_else(|| "production".to_string());
        match self
            .nakama
            .register_push_token(&token, platform, &environment)
            .await
        {
            Ok(()) => log::info!("[push] registered {} token ({})", platform, environment),
            Err(e) => log::warn!("[push] register_push_token failed: {}", e),
        }
        // Kept even when the RPC failed: logout still tries to remove it.
        self.push_token = Some(token);
    }

    pub(super) async fn unregister_push_token_on_logout(&mut self) {
        let Some(token) = self.push_token.take() else {
            return;
        };
        match self.nakama.unregister_push_token(&token).await {
            Ok(()) => log::info!("[push] unregistered token on logout"),
            Err(e) => log::warn!("[push] unregister_push_token failed: {}", e),
        }
    }

    pub(super) async fn handle_load_push_prefs(&self, crew_id: &str) {
        match self.nakama.push_prefs_get(crew_id).await {
            Ok(prefs) => {
                let _ = self.event_tx.send(Event::PushPrefsLoaded { prefs });
            }
            Err(e) => {
                log::warn!("[push] push_prefs_get for {} failed: {}", crew_id, e);
                let _ = self.event_tx.send(Event::PushPrefsFailed {
                    crew_id: crew_id.to_string(),
                });
            }
        }
    }

    pub(super) async fn handle_set_push_prefs(
        &self,
        crew_id: &str,
        messages: Option<bool>,
        looking_to_play: Option<bool>,
    ) {
        match self
            .nakama
            .push_prefs_set(crew_id, messages, looking_to_play)
            .await
        {
            Ok(prefs) => {
                log::info!(
                    "[push] switches set for {}: messages={} looking_to_play={}",
                    crew_id,
                    prefs.messages,
                    prefs.looking_to_play
                );
                let _ = self.event_tx.send(Event::PushPrefsLoaded { prefs });
            }
            Err(e) => {
                log::warn!("[push] push_prefs_set for {} failed: {}", crew_id, e);
                let _ = self.event_tx.send(Event::PushPrefsFailed {
                    crew_id: crew_id.to_string(),
                });
            }
        }
    }

    fn activity_inputs(&self) -> ActivityInputs {
        ActivityInputs {
            foreground: self.window_foreground,
            input_idle_secs: self.input_idle_secs,
            in_voice: self.voice.is_active(),
            hosting_stream: self.stream_session.is_some(),
            game_running: self.game_state.current_game().is_some(),
        }
    }

    /// Sends the activity flag when it changed or the socket is new.
    pub(super) async fn report_activity_if_changed(&mut self) {
        if self.nakama.current_user_id().is_none() {
            return;
        }
        let active = activity::is_active(&self.activity_inputs());
        let generation = self.nakama.ws_generation();
        if self.reported_activity == Some((active, generation)) {
            return;
        }
        match self
            .nakama
            .set_session_activity(active, activity::platform())
            .await
        {
            Ok(()) => {
                log::info!("[push] activity reported: active={}", active);
                self.reported_activity = Some((active, generation));
            }
            // Not connected yet: the next tick tries again.
            Err(e) => log::debug!("[push] activity report deferred: {}", e),
        }
    }
}
