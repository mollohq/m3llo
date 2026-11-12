use std::rc::Rc;

use mello_core::Event;
use slint::ComponentHandle as _;

use crate::app_context::AppContext;
use crate::{GameIntegrationData, InstalledGameData};

pub fn handle(ctx: &AppContext, event: Event) {
    match event {
        Event::GamesSettings { games } => {
            *ctx.games_integrations.borrow_mut() = games;
            push_games_list(ctx);
        }
        Event::InstalledGames { games } => {
            *ctx.installed_games.borrow_mut() = games;
            push_installed_games(ctx);
        }
        Event::RiotStatus {
            available,
            linked,
            riot_id,
            ..
        } => {
            ctx.app.set_riot_available(available);
            ctx.app.set_riot_linked(linked);
            ctx.app.set_riot_id(riot_id.into());

            if linked {
                // Successful link (or already linked): close the dialog and
                // drop any pending/visible post-game CTA.
                ctx.app.set_riot_dialog_open(false);
                ctx.app.set_riot_dialog_busy(false);
                ctx.app.set_riot_cta_visible(false);
                ctx.riot_cta_pending.set(false);
            } else if ctx.riot_cta_pending.get() {
                // A Riot-linkable session just ended; show the CTA while the
                // post-game card is still up.
                ctx.riot_cta_pending.set(false);
                if available && ctx.app.get_bar_state() == 2 {
                    ctx.app.set_riot_cta_visible(true);
                }
            }
        }
        Event::RiotLinkFailed { reason, code } => {
            ctx.app.set_riot_dialog_busy(false);
            ctx.app.set_riot_dialog_error(friendly_link_error(
                &ctx.app.global::<crate::Tr>(),
                &code,
                &reason,
            ));
        }
        _ => {}
    }
}

/// Rebuild the Games settings row model: core-reported adapter info merged
/// with the persisted per-game consent state.
fn push_games_list(ctx: &AppContext) {
    let settings = ctx.settings.borrow();
    let rows: Vec<GameIntegrationData> = ctx
        .games_integrations
        .borrow()
        .iter()
        .map(|g| {
            let icon = row_icon(ctx, &g.game_id, &g.name);
            GameIntegrationData {
                game_id: g.game_id.as_str().into(),
                name: g.name.as_str().into(),
                short_name: g.short_name.as_str().into(),
                color: slint::Color::from_argb_encoded(super::game::parse_hex_color(&g.color)),
                install_status: match g.installed {
                    Some(true) => "found".into(),
                    Some(false) => "not-found".into(),
                    None => "".into(),
                },
                icon: icon.0,
                has_icon: icon.1,
                writes_files: g.writes_files,
                note: g.note.as_str().into(),
                enabled: !settings.disabled_game_integrations.contains(&g.game_id),
                has_account_link: g.account_link.is_some(),
            }
        })
        .collect();
    ctx.app
        .set_games_list(Rc::new(slint::VecModel::from(rows)).into());
}

/// The icon of a settings row: the same icon slot ladder as every square
/// frame (exe or crew icon, then the art Worker icon).
fn row_icon(ctx: &AppContext, game_id: &str, name: &str) -> (slint::Image, bool) {
    let art = crate::game_icons::resolve_tile_art_slot(
        ctx,
        game_id,
        game_id,
        name,
        mello_core::art::ArtSlot::Icon,
    );
    (art.icon, art.has_icon)
}

/// Rebuild the "Hidden from crew" list: the installed games merged with the
/// persisted hidden set. A game hidden in Steam shows as hidden and locked.
pub fn push_installed_games(ctx: &AppContext) {
    let installed = ctx.installed_games.borrow().clone();
    let hidden_games = ctx.settings.borrow().hidden_games.clone();
    let rows: Vec<InstalledGameData> = installed
        .iter()
        .map(|g| {
            let icon = row_icon(ctx, &g.game_id, &g.name);
            InstalledGameData {
                game_id: g.game_id.as_str().into(),
                name: g.name.as_str().into(),
                short_name: mello_core::library::derive_short_name(&g.name).into(),
                icon: icon.0,
                has_icon: icon.1,
                hidden: g.steam_hidden || hidden_games.contains(&g.game_id),
                locked: g.steam_hidden,
            }
        })
        .collect();
    ctx.app
        .set_installed_games(Rc::new(slint::VecModel::from(rows)).into());
}

/// An icon landed in the art cache: the settings rows that it names take it.
/// Only the rows without an icon whose Worker ids include `art_id` look again,
/// so a long library costs no request per landing.
pub(super) fn on_icon_art_loaded(ctx: &AppContext, art_id: &str) {
    use slint::Model;
    let installed = ctx.installed_games.borrow().clone();
    let names_art = |game_id: &str, name: &str| {
        crate::game_icons::worker_ids(game_id, game_id, name, &installed)
            .iter()
            .any(|i| i == art_id)
    };
    let rows = ctx.app.get_games_list();
    for i in 0..rows.row_count() {
        let Some(mut r) = rows.row_data(i) else {
            continue;
        };
        if r.has_icon || !names_art(r.game_id.as_str(), r.name.as_str()) {
            continue;
        }
        let (img, has) = row_icon(ctx, r.game_id.as_str(), r.name.as_str());
        if has {
            r.icon = img;
            r.has_icon = true;
            rows.set_row_data(i, r);
        }
    }
    let rows = ctx.app.get_installed_games();
    for i in 0..rows.row_count() {
        let Some(mut r) = rows.row_data(i) else {
            continue;
        };
        if r.has_icon || !names_art(r.game_id.as_str(), r.name.as_str()) {
            continue;
        }
        let (img, has) = row_icon(ctx, r.game_id.as_str(), r.name.as_str());
        if has {
            r.icon = img;
            r.has_icon = true;
            rows.set_row_data(i, r);
        }
    }
}

/// The message of a failed Riot link. `code` is the stable error code of
/// the server (`riot.go`). An unknown code, or no code, shows the generic
/// message, and the log gets the code and the error text.
fn friendly_link_error(tr: &crate::Tr<'_>, code: &str, reason: &str) -> slint::SharedString {
    match code {
        "RIOT_ID_FORMAT" | "RIOT_ID_NOT_FOUND" => tr.get_riot_id_not_found(),
        "RIOT_REGION_INVALID" => tr.get_riot_pick_region(),
        "RIOT_NOT_CONFIGURED" => tr.get_riot_error_not_configured(),
        "RIOT_UNREACHABLE" => tr.get_riot_error_unreachable(),
        "RIOT_RATE_LIMITED" => tr.get_riot_error_rate_limited(),
        "RIOT_API_ERROR" => tr.get_riot_error_api(),
        _ => {
            log::warn!("[riot] no message for server code {code:?}: {reason}");
            tr.get_server_error_generic()
        }
    }
}
