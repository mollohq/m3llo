//! Looking to play: the presses of the cards, the toast and the ask popover
//! (plans/LOOKING-TO-PLAY.md 3.6). The work is in
//! `handlers/looking_to_play.rs`.
//!
//! These callbacks are on the `LookingToPlayUi` global and capture a strong
//! `AppContext`. `Harness::drop` clears them, so a test window still drops.

use slint::ComponentHandle;

use crate::app_context::AppContext;
use crate::handlers::looking_to_play as ltp;
use crate::LookingToPlayUi;

pub fn wire(ctx: &AppContext) {
    let ui = ctx.app.global::<LookingToPlayUi>();
    {
        let ctx = ctx.clone();
        ui.on_answered(move |ask_id, answer| ltp::answer(&ctx, &ask_id, &answer));
    }
    {
        let ctx = ctx.clone();
        ui.on_change_clicked(move |ask_id| {
            ltp::set_flag(&ctx, &ask_id, |s, id| {
                s.changing.insert(id);
            });
        });
    }
    {
        let ctx = ctx.clone();
        ui.on_cancel_clicked(move |ask_id| {
            log::info!("[looking-to-play] cancel ask {ask_id}");
            let _ = ctx.cmd_tx.send(mello_core::Command::LookingToPlayCancel {
                ask_id: ask_id.to_string(),
            });
        });
    }
    {
        let ctx = ctx.clone();
        ui.on_more_clicked(move |ask_id| {
            ltp::set_flag(&ctx, &ask_id, |s, id| {
                s.expanded.insert(id);
            });
        });
    }
    {
        let ctx = ctx.clone();
        ui.on_popover_opened(move || ltp::open_popover(&ctx));
    }
    {
        let ctx = ctx.clone();
        ui.on_search_edited(move |text| {
            ltp::edit_picks(&ctx, |picks, _| picks.search = text.to_string());
        });
    }
    {
        let ctx = ctx.clone();
        ui.on_group_toggled(move |index| ltp::toggle_group(&ctx, index.max(0) as usize));
    }
    {
        let ctx = ctx.clone();
        ui.on_member_toggled(move |user_id| {
            ltp::edit_picks(&ctx, |picks, _| picks.toggle_member(&user_id));
        });
    }
    {
        let ctx = ctx.clone();
        ui.on_everyone_toggled(move || ltp::edit_picks(&ctx, |picks, _| picks.toggle_everyone()));
    }
    {
        let ctx = ctx.clone();
        ui.on_game_selected(move |game_id| {
            ltp::edit_picks(&ctx, |picks, _| {
                picks.game = (!game_id.is_empty()).then(|| game_id.to_string());
            });
        });
    }
    {
        let ctx = ctx.clone();
        ui.on_ask_clicked(move || ltp::send_ask(&ctx));
    }
    {
        let ctx = ctx.clone();
        ui.on_toast_answered(move |ask_id, answer| ltp::toast_answered(&ctx, &ask_id, &answer));
    }
    {
        let ctx = ctx.clone();
        ui.on_toast_dismissed(move |ask_id| ltp::toast_dismissed(&ctx, &ask_id));
    }
}
