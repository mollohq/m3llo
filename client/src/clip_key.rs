//! The clip key (F8) and the clip button (CLIPS.md section 3.1). Both save
//! the last 30 seconds. The replay state that core last reported picks the
//! clip: a game clip while the replay records, else a voice clip (or a
//! stream clip while the user watches a stream, which core picks).

use mello_core::events::GameClipState;
use mello_core::Command;

use crate::app_context::AppContext;
use crate::game_clip::{toast, DISK_LOW_TOAST, NO_PERMISSION_TOAST, UNAVAILABLE_TOAST};

/// The toast when there is nothing to clip: no replay and no voice.
pub(crate) const JOIN_VOICE_TOAST: &str = "Join voice to clip";

/// The start of the toast when the replay cannot clip and voice can.
const VOICE_ONLY_PREFIX: &str = "Voice clip saved. ";

/// One press of the clip key or the clip button.
pub(crate) fn clip_pressed(ctx: &AppContext) {
    let ui = ctx.game_clip.borrow().clone();
    let can_voice_clip = ctx.app.get_in_voice() && ctx.app.get_mic_permission_granted();
    let reason = match ui.state {
        GameClipState::Recording | GameClipState::Waiting | GameClipState::Paused => {
            log::info!("[clip key] game clip of {} ({:?})", ui.game_name, ui.state);
            let _ = ctx.cmd_tx.send(Command::CaptureGameClip);
            return;
        }
        GameClipState::Off => {
            if can_voice_clip {
                log::info!("[clip key] voice clip: no replay");
                voice_clip(ctx);
            } else {
                log::info!("[clip key] no clip: no replay and not in voice");
                toast(ctx, JOIN_VOICE_TOAST);
            }
            return;
        }
        GameClipState::DiskLow => DISK_LOW_TOAST,
        GameClipState::Unavailable => UNAVAILABLE_TOAST,
        GameClipState::NoPermission => NO_PERMISSION_TOAST,
    };
    // The replay cannot clip. Voice can, when the user is in voice.
    if can_voice_clip {
        log::info!(
            "[clip key] voice clip only: replay {:?} {} ({})",
            ui.state,
            ui.game_name,
            ui.reason
        );
        voice_clip(ctx);
        toast(ctx, &format!("{VOICE_ONLY_PREFIX}{reason}"));
    } else {
        log::info!(
            "[clip key] no clip: replay {:?} {} ({}) and not in voice",
            ui.state,
            ui.game_name,
            ui.reason
        );
        toast(ctx, reason);
    }
}

/// The last 30 seconds of voice, or of the watched stream.
fn voice_clip(ctx: &AppContext) {
    let _ = ctx.cmd_tx.send(Command::CaptureClip { seconds: 30.0 });
}
