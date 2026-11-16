//! Looking to play on desktop (plans/LOOKING-TO-PLAY.md 3.6): the events of
//! an ask, the card in the chat feed, the composer pill and the ask popover.
//!
//! The rules are pure functions in `crate::looking_to_play`. This module
//! reads what the client has (the crew members, the voice channels, the
//! avatars), calls them, and writes the Slint structs. The toast, the sound
//! and the OS notification are not here: the `notify` flag is passed on
//! unused until they are built.

use std::collections::{HashMap, HashSet};
use std::rc::Rc;

use mello_core::looking_to_play::{Ask, AskGame};
use mello_core::{Command, Event};
use slint::{ComponentHandle, Model};

use crate::app_context::AppContext;
use crate::converters::{avatar_color_index, make_initials, same_card};
use crate::looking_to_play::{
    self as ltp, Card, Doing, GameRef, LtpState, PendingAsk, Picks, PopoverPerson, Row,
};
use crate::{
    LookingToPlayCardData, LookingToPlayFace, LookingToPlayGame, LookingToPlayGroup,
    LookingToPlayMember, LookingToPlayOpenAsk, LookingToPlayRow, LookingToPlayUi, MainWindow,
};

/// A crew member with what the UI has for them.
struct Member {
    id: String,
    name: String,
    avatar: slint::Image,
    has_avatar: bool,
    game_line: String,
}

/// The crew of the screen: the member list, then the mention list for a
/// member that the member list does not hold. The viewer is in it.
fn roster(app: &MainWindow, avatars: &HashMap<String, slint::Image>) -> Vec<Member> {
    let viewer = app.get_user_id().to_string();
    let picture = |id: &str, own: Option<(slint::Image, bool)>| {
        if id == viewer && app.get_has_user_avatar() {
            (app.get_user_avatar(), true)
        } else if let Some((img, true)) = own {
            (img, true)
        } else if let Some(img) = avatars.get(id) {
            (img.clone(), true)
        } else {
            (slint::Image::default(), false)
        }
    };
    let mut out: Vec<Member> = Vec::new();
    let members = app.get_members();
    for m in (0..members.row_count()).filter_map(|i| members.row_data(i)) {
        let id = m.id.to_string();
        if out.iter().any(|x| x.id == id) {
            continue;
        }
        let (avatar, has_avatar) = picture(&id, Some((m.avatar.clone(), m.has_avatar)));
        out.push(Member {
            name: m.name.to_string(),
            avatar,
            has_avatar,
            game_line: m.game_line.to_string(),
            id,
        });
    }
    let mentions = app.get_mention_members();
    for m in (0..mentions.row_count()).filter_map(|i| mentions.row_data(i)) {
        let id = m.user_id.to_string();
        if out.iter().any(|x| x.id == id) {
            continue;
        }
        let (avatar, has_avatar) = picture(&id, None);
        out.push(Member {
            name: m.display_name.to_string(),
            avatar,
            has_avatar,
            game_line: String::new(),
            id,
        });
    }
    out
}

/// The voice channels of the crew on screen.
fn channels(app: &MainWindow) -> Vec<ltp::Channel> {
    let model = app.get_voice_channels();
    (0..model.row_count())
        .filter_map(|i| model.row_data(i))
        .map(|ch| ltp::Channel {
            id: ch.id.to_string(),
            is_default: ch.is_default,
            members: (0..ch.members.row_count())
                .filter_map(|j| ch.members.row_data(j))
                .map(|m| m.id.to_string())
                .collect(),
        })
        .collect()
}

/// What a card needs from the window, read once for a whole refresh.
pub(crate) struct CardView {
    roster: Vec<Member>,
    persons: Vec<ltp::Person>,
    viewer: String,
    active_crew: String,
    /// Who is in a voice channel of the crew on screen. `None` when the
    /// client has no voice channels of it.
    in_voice: Option<HashSet<String>>,
    now_ms: i64,
}

impl CardView {
    pub(crate) fn new(app: &MainWindow, avatars: &HashMap<String, slint::Image>) -> Self {
        let roster = roster(app, avatars);
        let persons = roster
            .iter()
            .map(|m| ltp::Person {
                id: m.id.clone(),
                name: m.name.clone(),
            })
            .collect();
        let chans = channels(app);
        let in_voice =
            (!chans.is_empty()).then(|| chans.into_iter().flat_map(|c| c.members).collect());
        CardView {
            roster,
            persons,
            viewer: app.get_user_id().to_string(),
            active_crew: app.get_active_crew_id().to_string(),
            in_voice,
            now_ms: crate::converters::current_time_ms(),
        }
    }

    fn row(&self, r: &Row) -> LookingToPlayRow {
        let m = self.roster.iter().find(|m| m.id == r.user_id);
        LookingToPlayRow {
            user_id: r.user_id.as_str().into(),
            name: r.name.as_str().into(),
            initials: make_initials(&r.name).into(),
            color_index: avatar_color_index(&r.user_id),
            avatar: m.map(|m| m.avatar.clone()).unwrap_or_default(),
            has_avatar: m.is_some_and(|m| m.has_avatar),
            state: r.state.key().into(),
            due_minutes: r.state.due_minutes() as i32,
        }
    }

    /// The card of `ask_id`, or `None` when the client has no state of it.
    pub(crate) fn data(
        &self,
        tr: &crate::Tr<'_>,
        state: &LtpState,
        ask_id: &str,
    ) -> Option<LookingToPlayCardData> {
        let ask = state.asks.get(ask_id)?;
        let in_voice = if ask.crew_id == self.active_crew {
            self.in_voice.as_ref()
        } else {
            None
        };
        let card = ltp::card(&ltp::CardInput {
            ask,
            roster: &self.persons,
            in_voice,
            viewer_id: &self.viewer,
            now_ms: self.now_ms,
            expanded: state.expanded.contains(ask_id),
            changing: state.changing.contains(ask_id),
        });
        Some(self.slint_card(tr, state, ask, &card))
    }

    fn slint_card(
        &self,
        tr: &crate::Tr<'_>,
        state: &LtpState,
        ask: &Ask,
        card: &Card,
    ) -> LookingToPlayCardData {
        let rows: Vec<LookingToPlayRow> = card.rows.iter().map(|r| self.row(r)).collect();
        let was_in: Vec<LookingToPlayRow> = card.was_in.iter().map(|r| self.row(r)).collect();
        let game = ask.game.as_ref();
        let (icon, has_icon) = game
            .and_then(|g| state.game_icons.get(&g.game_id).cloned())
            .unwrap_or_default();
        let closed_at = if card.closed_at_ms > 0 {
            let time = mello_core::chat::display_time(&mello_core::presence::to_rfc3339(
                card.closed_at_ms,
            ));
            crate::l10n_format::chat_time(tr, Some(&time), crate::l10n_format::clock_form())
        } else {
            String::new()
        };
        LookingToPlayCardData {
            ask_id: card.ask_id.as_str().into(),
            state: card.state.into(),
            closed_reason: card.closed_reason.as_str().into(),
            asker_name: card.asker_name.as_str().into(),
            has_game: game.is_some(),
            game_id: game.map(|g| g.game_id.as_str()).unwrap_or("").into(),
            game_name: game.map(|g| g.name.as_str()).unwrap_or("").into(),
            game_monogram: game
                .map(|g| crate::recent_games::monogram(&g.name))
                .unwrap_or_default()
                .into(),
            game_hue: game.map_or(0, |g| crate::recent_games::hue_index(&g.game_id, &g.name)),
            game_icon: icon,
            has_game_icon: has_icon,
            minutes_left: card.minutes_left as i32,
            rows: Rc::new(slint::VecModel::from(rows)).into(),
            more_in: card.more_in as i32,
            no_count: card.no_count as i32,
            viewer_is_asker: card.viewer_is_asker,
            viewer_answer: card.viewer_answer.into(),
            changing: card.changing,
            closed_at: closed_at.into(),
            was_in: Rc::new(slint::VecModel::from(was_in)).into(),
        }
    }
}

/// The pill's pip: the crew on screen has an ask that is open or on.
pub(crate) fn sync_pip(app: &MainWindow, state: &LtpState) {
    let live = state.has_live_ask(app.get_active_crew_id().as_str());
    let ui = app.global::<LookingToPlayUi>();
    if ui.get_has_live_ask() != live {
        ui.set_has_live_ask(live);
    }
}

/// Give every chat row of an ask its card, in place: `set_row_data` on the
/// rows that changed, the model is kept.
pub(crate) fn refresh_cards(ctx: &AppContext) {
    let state = ctx.looking_to_play.borrow();
    let view = CardView::new(&ctx.app, &ctx.avatar_cache.borrow());
    let tr = ctx.app.global::<crate::Tr>();
    let model = ctx.app.get_messages();
    for i in 0..model.row_count() {
        let Some(mut row) = model.row_data(i) else {
            continue;
        };
        if row.ask_id.is_empty() {
            continue;
        }
        let data = view.data(&tr, &state, row.ask_id.as_str());
        let is_ask = data.is_some();
        let ask = data.unwrap_or_default();
        if row.is_ask != is_ask || !same_card(&row.ask, &ask) {
            row.is_ask = is_ask;
            row.ask = ask;
            model.set_row_data(i, row);
        }
    }
    sync_pip(&ctx.app, &state);
}

/// The timer: the minute counts of the cards count on while the crew on
/// screen has an ask that is open or on. Nothing to do otherwise.
pub(crate) fn tick(ctx: &AppContext) {
    let live = ctx
        .looking_to_play
        .borrow()
        .has_live_ask(ctx.app.get_active_crew_id().as_str());
    if live {
        refresh_cards(ctx);
    }
}

/// The icon of the game of each ask, looked up once.
fn resolve_icons<'a>(ctx: &AppContext, games: impl IntoIterator<Item = (&'a str, &'a str)>) {
    for (id, name) in games {
        if ctx.looking_to_play.borrow().game_icons.contains_key(id) {
            continue;
        }
        let art = crate::game_icons::resolve_tile_art_slot(
            ctx,
            id,
            id,
            name,
            mello_core::art::ArtSlot::Icon,
        );
        ctx.looking_to_play
            .borrow_mut()
            .game_icons
            .insert(id.to_string(), (art.icon, art.has_icon));
    }
}

fn error_text(tr: &crate::Tr<'_>, code: &str) -> slint::SharedString {
    match code {
        "ASK_TOO_SOON" => tr.get_looking_to_play_error_too_soon(),
        "ASK_NOBODY_WAIT" => tr.get_looking_to_play_error_nobody_wait(),
        "ASK_HAS_OPEN" => tr.get_looking_to_play_error_has_open(),
        _ => tr.get_looking_to_play_error_generic(),
    }
}

/// The members of the popover with what they do now.
fn popover_people(app: &MainWindow, roster: &[Member]) -> Vec<PopoverPerson> {
    let in_voice: HashSet<String> = channels(app).into_iter().flat_map(|c| c.members).collect();
    roster
        .iter()
        .map(|m| PopoverPerson {
            id: m.id.clone(),
            name: m.name.clone(),
            game_line: m.game_line.clone(),
            in_voice: in_voice.contains(&m.id),
        })
        .collect()
}

/// The games the popover offers, with their icons (3.6).
fn popover_games(ctx: &AppContext, crew_id: &str) -> Vec<GameRef> {
    let viewer = ctx.app.get_user_id().to_string();
    let groups = ctx.live_games.borrow().groups(&viewer);
    let as_ref = |id: &str, name: &str| GameRef {
        game_id: id.to_string(),
        name: name.to_string(),
    };
    let viewer_game = groups
        .iter()
        .find(|g| g.user_ids.contains(&viewer))
        .map(|g| as_ref(&g.game_id, &g.game_name));
    let crew_games: Vec<GameRef> = groups
        .iter()
        .filter(|g| !g.user_ids.contains(&viewer))
        .map(|g| as_ref(&g.game_id, &g.game_name))
        .collect();
    let recent: Vec<GameRef> = ctx
        .last_feed
        .borrow()
        .as_ref()
        .filter(|f| f.crew_id == crew_id)
        .map(|f| {
            f.zones
                .installed
                .iter()
                .filter_map(|v| {
                    Some(as_ref(
                        v.get("game_id")?.as_str()?,
                        v.get("game_name")?.as_str()?,
                    ))
                })
                .collect()
        })
        .unwrap_or_default();
    let games = ltp::game_choices(viewer_game.as_ref(), &crew_games, &recent);
    resolve_icons(
        ctx,
        games.iter().map(|g| (g.game_id.as_str(), g.name.as_str())),
    );
    games
}

/// The popover's crew tile.
fn crew_tile(app: &MainWindow, crew_id: &str) -> (String, slint::Image, bool) {
    let crews = app.get_crews();
    (0..crews.row_count())
        .filter_map(|i| crews.row_data(i))
        .find(|c| c.id == crew_id)
        .map(|c| {
            let initials = if c.initials.is_empty() {
                make_initials(&c.name)
            } else {
                c.initials.to_string()
            };
            (initials, c.avatar.clone(), c.has_avatar)
        })
        .unwrap_or_else(|| ("?".into(), slint::Image::default(), false))
}

/// Write the popover data from the state.
pub(crate) fn sync_popover(ctx: &AppContext) {
    let app = &ctx.app;
    let ui = app.global::<LookingToPlayUi>();
    let tr = app.global::<crate::Tr>();
    let crew = app.get_active_crew_id().to_string();
    let viewer = app.get_user_id().to_string();
    let state = ctx.looking_to_play.borrow();
    let roster = roster(app, &ctx.avatar_cache.borrow());
    let people = popover_people(app, &roster);
    let recent = state.picks.get(&crew).cloned().unwrap_or_default();
    let lists = ltp::popover_lists(&people, &viewer, &state.popover, &recent);
    let member_of = |id: &str| roster.iter().find(|m| m.id == id);

    let members: Vec<LookingToPlayMember> = lists
        .members
        .iter()
        .filter_map(|pm| {
            let m = member_of(&pm.id)?;
            Some(LookingToPlayMember {
                user_id: m.id.as_str().into(),
                name: m.name.as_str().into(),
                initials: make_initials(&m.name).into(),
                color_index: avatar_color_index(&m.id),
                avatar: m.avatar.clone(),
                has_avatar: m.has_avatar,
                doing: match &pm.doing {
                    Doing::Game(line) => line.as_str().into(),
                    Doing::InVoice => tr.get_looking_to_play_in_voice(),
                    Doing::Nothing => slint::SharedString::default(),
                },
                picked: pm.picked,
            })
        })
        .collect();

    let groups: Vec<LookingToPlayGroup> = lists
        .groups
        .iter()
        .map(|g| {
            let faces: Vec<LookingToPlayFace> = g
                .ids
                .iter()
                .take(ltp::FACE_LIMIT)
                .filter_map(|id| member_of(id))
                .map(|m| LookingToPlayFace {
                    initials: make_initials(&m.name).into(),
                    color_index: avatar_color_index(&m.id),
                    avatar: m.avatar.clone(),
                    has_avatar: m.has_avatar,
                })
                .collect();
            let names: Vec<String> = g
                .ids
                .iter()
                .filter_map(|id| member_of(id))
                .map(|m| m.name.clone())
                .collect();
            LookingToPlayGroup {
                index: g.index as i32,
                faces: Rc::new(slint::VecModel::from(faces)).into(),
                more: g.ids.len().saturating_sub(ltp::FACE_LIMIT) as i32,
                label: if g.index == 0 {
                    tr.get_looking_to_play_last_time()
                } else {
                    slint::SharedString::default()
                },
                a11y_label: crate::l10n_format::list_and(&tr, &names).into(),
                picked: g.picked,
            }
        })
        .collect();

    let open_asks: Vec<LookingToPlayOpenAsk> = state
        .live_asks(&crew)
        .into_iter()
        .map(|a| {
            let game = a.game.as_ref();
            let (icon, has_icon) = game
                .and_then(|g| state.game_icons.get(&g.game_id).cloned())
                .unwrap_or_default();
            LookingToPlayOpenAsk {
                ask_id: a.ask_id.as_str().into(),
                has_game: game.is_some(),
                game_id: game.map(|g| g.game_id.as_str()).unwrap_or("").into(),
                game_name: game.map(|g| g.name.as_str()).unwrap_or("").into(),
                game_monogram: game
                    .map(|g| crate::recent_games::monogram(&g.name))
                    .unwrap_or_default()
                    .into(),
                game_hue: game.map_or(0, |g| crate::recent_games::hue_index(&g.game_id, &g.name)),
                game_icon: icon,
                has_game_icon: has_icon,
                count_in: (1 + a.members_in().len()) as i32,
            }
        })
        .collect();

    let games: Vec<LookingToPlayGame> = state
        .popover_games
        .iter()
        .map(|g| {
            let (icon, has_icon) = state
                .game_icons
                .get(&g.game_id)
                .cloned()
                .unwrap_or_default();
            LookingToPlayGame {
                game_id: g.game_id.as_str().into(),
                name: g.name.as_str().into(),
                monogram: crate::recent_games::monogram(&g.name).into(),
                hue: crate::recent_games::hue_index(&g.game_id, &g.name),
                icon,
                has_icon,
                selected: state.popover.game.as_deref() == Some(g.game_id.as_str()),
            }
        })
        .collect();

    let (initials, avatar, has_avatar) = crew_tile(app, &crew);
    ui.set_open_asks(Rc::new(slint::VecModel::from(open_asks)).into());
    ui.set_members(Rc::new(slint::VecModel::from(members)).into());
    ui.set_groups(Rc::new(slint::VecModel::from(groups)).into());
    ui.set_games(Rc::new(slint::VecModel::from(games)).into());
    ui.set_large_crew(lists.large);
    ui.set_everyone_picked(state.popover.everyone);
    ui.set_picked_count(state.popover.picked.len() as i32);
    ui.set_member_count(lists.member_count as i32);
    ui.set_crew_initials(initials.into());
    ui.set_crew_color_index(avatar_color_index(&crew));
    ui.set_crew_avatar(avatar);
    ui.set_has_crew_avatar(has_avatar);
    ui.set_busy(state.pending.is_some());
    ui.set_error_text(
        state
            .error_code
            .as_deref()
            .map(|c| error_text(&tr, c))
            .unwrap_or_default(),
    );
}

/// The pill opened the popover: start it for the crew on screen. An ask on
/// its way keeps its picks, so the popover shows it busy.
pub(crate) fn open_popover(ctx: &AppContext) {
    let crew = ctx.app.get_active_crew_id().to_string();
    if crew.is_empty() {
        ctx.app.global::<LookingToPlayUi>().set_popover_open(false);
        return;
    }
    let busy = ctx.looking_to_play.borrow().pending.is_some();
    if !busy {
        let viewer = ctx.app.get_user_id().to_string();
        let roster = roster(&ctx.app, &ctx.avatar_cache.borrow());
        let known: HashSet<String> = roster
            .iter()
            .filter(|m| m.id != viewer)
            .map(|m| m.id.clone())
            .collect();
        let large = known.len() > ltp::LARGE_CREW;
        let games = popover_games(ctx, &crew);
        let mut state = ctx.looking_to_play.borrow_mut();
        let recent = state.picks.get(&crew).cloned().unwrap_or_default();
        state.popover = Picks::start(large, &recent, &known, &viewer);
        state.popover_crew = crew.clone();
        state.popover_games = games;
        state.error_code = None;
        log::debug!(
            "[looking-to-play] popover for crew {crew}: large={large}, {} recent groups",
            recent.len()
        );
    }
    sync_popover(ctx);
}

/// A press in the popover changed the picks: draw them.
pub(crate) fn edit_picks(ctx: &AppContext, f: impl FnOnce(&mut Picks, &[Vec<String>])) {
    {
        let crew = ctx.app.get_active_crew_id().to_string();
        let mut state = ctx.looking_to_play.borrow_mut();
        let recent = state.picks.get(&crew).cloned().unwrap_or_default();
        f(&mut state.popover, &recent);
    }
    sync_popover(ctx);
}

/// A group press: the members of the group that the popover lists.
pub(crate) fn toggle_group(ctx: &AppContext, index: usize) {
    let viewer = ctx.app.get_user_id().to_string();
    let roster = roster(&ctx.app, &ctx.avatar_cache.borrow());
    edit_picks(ctx, |picks, recent| {
        let Some(group) = recent.get(index) else {
            return;
        };
        let ids: Vec<String> = group
            .iter()
            .filter(|id| **id != viewer && roster.iter().any(|m| m.id == **id))
            .cloned()
            .collect();
        picks.toggle_group(&ids);
    });
}

/// The red button: send the ask.
pub(crate) fn send_ask(ctx: &AppContext) {
    let crew = ctx.app.get_active_crew_id().to_string();
    let cmd = {
        let mut state = ctx.looking_to_play.borrow_mut();
        if state.pending.is_some() || crew.is_empty() {
            return;
        }
        let Some(member_ids) = state.popover.member_ids() else {
            return;
        };
        let game = state.popover.game.as_ref().and_then(|id| {
            state
                .popover_games
                .iter()
                .find(|g| g.game_id == *id)
                .map(|g| AskGame {
                    game_id: g.game_id.clone(),
                    name: g.name.clone(),
                })
        });
        state.pending = Some(PendingAsk {
            crew_id: crew.clone(),
            game_id: game.as_ref().map(|g| g.game_id.clone()),
            everyone: member_ids.is_empty(),
        });
        state.error_code = None;
        log::info!(
            "[looking-to-play] ask in crew {crew}: {} picked, game {:?}",
            member_ids.len(),
            game.as_ref().map(|g| g.game_id.as_str())
        );
        Command::LookingToPlayAsk {
            crew_id: crew,
            game,
            member_ids,
        }
    };
    let _ = ctx.cmd_tx.send(cmd);
    sync_popover(ctx);
}

/// A press on an answer of a card. In also joins voice in the crew, unless
/// the user is in a voice channel already (3.6).
pub(crate) fn answer(ctx: &AppContext, ask_id: &str, key: &str) {
    let Some(answer) = ltp::answer_of(key) else {
        log::warn!("[looking-to-play] unknown answer {key:?}");
        return;
    };
    log::info!("[looking-to-play] answer {key} on ask {ask_id}");
    let _ = ctx.cmd_tx.send(Command::LookingToPlayAnswer {
        ask_id: ask_id.to_string(),
        answer,
    });
    if answer != mello_core::looking_to_play::Answer::In {
        return;
    }
    let in_voice = ctx.app.get_in_voice() || !ctx.active_voice_channel.borrow().is_empty();
    let channel = {
        let state = ctx.looking_to_play.borrow();
        let Some(ask) = state.asks.get(ask_id) else {
            return;
        };
        if ask.crew_id != ctx.app.get_active_crew_id().as_str() {
            return;
        }
        ltp::channel_to_join(ask, &channels(&ctx.app), in_voice)
    };
    if let Some(channel) = channel {
        log::info!("[looking-to-play] In: joining voice channel {channel}");
        ctx.app.invoke_join_voice_channel(channel.into());
    }
}

pub(crate) fn set_flag(ctx: &AppContext, ask_id: &str, f: impl FnOnce(&mut LtpState, String)) {
    f(&mut ctx.looking_to_play.borrow_mut(), ask_id.to_string());
    refresh_cards(ctx);
}

fn popover_open(ctx: &AppContext) -> bool {
    ctx.app.global::<LookingToPlayUi>().get_popover_open()
}

pub fn handle(ctx: &AppContext, event: Event) {
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
            if let Some(g) = &ask.game {
                resolve_icons(ctx, [(g.game_id.as_str(), g.name.as_str())]);
            }
            let viewer = ctx.app.get_user_id().to_string();
            let on_screen = ask.crew_id == ctx.app.get_active_crew_id().as_str();
            let answered = {
                let mut state = ctx.looking_to_play.borrow_mut();
                let answered = state.answers_pending(&ask, &viewer);
                if answered {
                    state.pending = None;
                    state.error_code = None;
                    state.popover = Picks::default();
                }
                state.store(ask);
                answered
            };
            if answered {
                log::info!("[looking-to-play] the ask went out");
                ctx.app.global::<LookingToPlayUi>().set_popover_open(false);
            }
            if on_screen {
                refresh_cards(ctx);
                if popover_open(ctx) || answered {
                    sync_popover(ctx);
                }
            }
        }
        Event::LookingToPlayLoaded { crew_id, asks } => {
            log::debug!(
                "[looking-to-play] {} asks loaded for crew {}",
                asks.len(),
                crew_id
            );
            for a in &asks {
                if let Some(g) = &a.game {
                    resolve_icons(ctx, [(g.game_id.as_str(), g.name.as_str())]);
                }
            }
            {
                let mut state = ctx.looking_to_play.borrow_mut();
                for a in asks {
                    state.store(a);
                }
            }
            if crew_id == ctx.app.get_active_crew_id().as_str() {
                refresh_cards(ctx);
                if popover_open(ctx) {
                    sync_popover(ctx);
                }
            }
        }
        Event::LookingToPlayPicks { crew_id, groups } => {
            log::debug!(
                "[looking-to-play] {} recent picks for crew {}",
                groups.len(),
                crew_id
            );
            let on_screen = crew_id == ctx.app.get_active_crew_id().as_str();
            ctx.looking_to_play
                .borrow_mut()
                .picks
                .insert(crew_id, groups);
            if on_screen && popover_open(ctx) {
                sync_popover(ctx);
            }
        }
        Event::LookingToPlayFailed { ask_id, code } => {
            log::warn!("[looking-to-play] command failed: ask={ask_id:?} code={code:?}");
            if ask_id.is_empty() {
                {
                    let mut state = ctx.looking_to_play.borrow_mut();
                    state.pending = None;
                    state.error_code = Some(code);
                }
                sync_popover(ctx);
            } else {
                set_flag(ctx, &ask_id, |s, id| {
                    s.changing.remove(&id);
                });
            }
        }
        _ => {}
    }
}

/// `same_card` must see every field a refresh can change. Kept here so a
/// new field of the card is checked next to the code that fills it.
#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_card_with_another_row_state_is_not_the_same() {
        let row = |state: &str| LookingToPlayRow {
            user_id: "u1".into(),
            name: "kim".into(),
            state: state.into(),
            ..Default::default()
        };
        let card = |state: &str| LookingToPlayCardData {
            ask_id: "a1".into(),
            rows: Rc::new(slint::VecModel::from(vec![row(state)])).into(),
            ..Default::default()
        };
        assert!(same_card(&card("in"), &card("in")));
        assert!(!same_card(&card("in"), &card("voice")));
        let mut a = card("in");
        a.minutes_left = 3;
        assert!(!same_card(&a, &card("in")));
    }
}
