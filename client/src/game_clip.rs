//! Game clips in the client (CLIPS.md 15.2): the replay state that core
//! reports, the saved clip, and the HUD toast. The clip key (F8) is in
//! `clip_key.rs`. The Settings section and the Keybinds row are in
//! `callbacks/settings.rs`. A saved clip goes to core for the post and the
//! upload (`mello_core::clip_upload`).

use mello_core::events::GameClipState;
use mello_core::{Command, Event};
use slint::ComponentHandle as _;

use crate::app_context::AppContext;
use crate::hud_manager::HudClipToast;

/// The replay as core last reported it.
#[derive(Debug, Clone, Default)]
pub struct GameClipUi {
    pub state: GameClipState,
    pub game_name: String,
    pub reason: String,
}

pub(crate) fn handle(ctx: &AppContext, event: Event) {
    match event {
        Event::GameClipStateChanged {
            state,
            game_name,
            reason,
        } => {
            log::info!("[game clip] replay {:?} {} {}", state, game_name, reason);
            let was = ctx.game_clip.borrow().state;
            // macOS: the Settings row that asks for Screen Recording.
            ctx.app
                .set_game_clips_need_permission(state == GameClipState::NoPermission);
            *ctx.game_clip.borrow_mut() = GameClipUi {
                state,
                game_name,
                reason,
            };
            // CLIPS.md 15.2: the replay stops on a full disk, and says so once.
            if state == GameClipState::DiskLow && was != GameClipState::DiskLow {
                toast(
                    ctx,
                    &ctx.app.global::<crate::Tr>().get_clip_toast_disk_low(),
                );
            }
        }
        Event::GameClipCaptured {
            clip_id,
            path,
            poster_path,
            game_name,
            duration_seconds,
            width,
            height,
        } => {
            crate::sounds::cue(ctx, mello_core::sounds::Sound::ClipSaved);
            log::info!(
                "[game clip] saved {} ({}, {:.1} s, {}x{})",
                path,
                game_name,
                duration_seconds,
                width,
                height
            );
            toast(ctx, &ctx.app.global::<crate::Tr>().get_clip_toast_saved());
            // The clip goes to the active crew (CLIPS.md 15.2), and core
            // uploads it in the background (CLIPS.md 15.7).
            let crew_id = ctx.app.get_active_crew_id().to_string();
            if crew_id.is_empty() {
                log::warn!("[game clip] {clip_id} stays local: no active crew");
                return;
            }
            let _ = ctx.cmd_tx.send(Command::ShareGameClip {
                crew_id,
                clip_id,
                path,
                poster_path,
                duration_seconds,
                width,
                height,
                game_name,
            });
        }
        Event::GameClipFailed { reason } => {
            log::warn!("[game clip] no clip: {reason}");
            toast(ctx, &ctx.app.global::<crate::Tr>().get_clip_toast_none());
        }
        Event::GameClipUploadFailed { clip_id, reason } => {
            log::warn!("[game clip] {clip_id} does not go up: {reason}");
        }
        _ => {}
    }
}

/// The HUD clip toast over the game (4 s, the overlay's own timer). With
/// the HUD off or hidden, the log line is all.
pub(crate) fn toast(ctx: &AppContext, label: &str) {
    #[cfg(test)]
    TOASTS.with(|t| t.borrow_mut().push(label.to_string()));
    if !ctx.hud_manager.is_enabled() {
        return;
    }
    let mode = ctx.fg_monitor.borrow().current_mode();
    let mut state = crate::hud_state_builder::build_hud_state(ctx, mode);
    state.clip_toast = Some(HudClipToast {
        label: label.to_string(),
    });
    ctx.hud_manager.push_state(state);
}

// The toasts of this thread, for the tests: the HUD is off in the harness.
#[cfg(test)]
thread_local! {
    static TOASTS: std::cell::RefCell<Vec<String>> = const { std::cell::RefCell::new(Vec::new()) };
}

/// The toasts since the last call, on this thread.
#[cfg(test)]
pub(crate) fn take_toasts() -> Vec<String> {
    TOASTS.with(|t| std::mem::take(&mut *t.borrow_mut()))
}
