//! The clip key (F8) and the clip button (CLIPS.md section 3.1). Both save
//! the last 30 seconds. The replay state that core last reported picks the
//! clip: a game clip while the replay records, else a voice clip (or a
//! stream clip while the user watches a stream, which core picks).

use mello_core::events::GameClipState;
use mello_core::Command;
use slint::ComponentHandle as _;

use crate::app_context::AppContext;
use crate::game_clip::toast;

/// One press of the clip key or the clip button.
pub(crate) fn clip_pressed(ctx: &AppContext) {
    let ui = ctx.game_clip.borrow().clone();
    let can_voice_clip = ctx.app.get_in_voice() && ctx.app.get_mic_permission_granted();
    let tr = ctx.app.global::<crate::Tr>();
    // Why the replay cannot clip: alone, and after a voice clip.
    let (reason, voice_only) = match ui.state {
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
                toast(ctx, &tr.get_quest_hint_join_voice());
            }
            return;
        }
        GameClipState::DiskLow => (
            tr.get_clip_toast_disk_low(),
            tr.get_clip_toast_voice_disk_low(),
        ),
        GameClipState::Unavailable => (
            tr.get_clip_toast_unavailable(),
            tr.get_clip_toast_voice_unavailable(),
        ),
        GameClipState::NoPermission => (
            tr.get_clip_toast_no_permission(),
            tr.get_clip_toast_voice_no_permission(),
        ),
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
        toast(ctx, &voice_only);
    } else {
        log::info!(
            "[clip key] no clip: replay {:?} {} ({}) and not in voice",
            ui.state,
            ui.game_name,
            ui.reason
        );
        toast(ctx, &reason);
    }
}

/// The last 30 seconds of voice, or of the watched stream.
fn voice_clip(ctx: &AppContext) {
    let _ = ctx.cmd_tx.send(Command::CaptureClip { seconds: 30.0 });
}
