use muda::{Menu, MenuId, MenuItem, PredefinedMenuItem, Submenu};

use crate::edit_shortcuts::{self, EDIT_ITEMS};
use crate::APP_NAME;

/// Build the macOS NSMenuBar. Must be called on the main thread before
/// the Slint event loop starts, after the main window exists: the labels
/// come from `tr`.
pub fn build_menu_bar(tr: &crate::Tr<'_>) -> Menu {
    let app = || slint::SharedString::from(APP_NAME);
    let menu = Menu::new();

    // -- Mello --
    let app_menu = Submenu::with_id("app", APP_NAME, true);
    app_menu
        .append(&PredefinedMenuItem::about(
            Some(tr.invoke_menu_about(app()).as_str()),
            None,
        ))
        .ok();
    app_menu.append(&PredefinedMenuItem::separator()).ok();
    app_menu
        .append(&MenuItem::with_id(
            MenuId::new("check_updates"),
            tr.get_menu_check_updates().as_str(),
            true,
            None,
        ))
        .ok();
    app_menu.append(&PredefinedMenuItem::separator()).ok();
    app_menu
        .append(&MenuItem::with_id(
            MenuId::new("prefs"),
            tr.get_menu_preferences().as_str(),
            true,
            Some("cmd+,".parse().unwrap()),
        ))
        .ok();
    app_menu.append(&PredefinedMenuItem::separator()).ok();
    app_menu.append(&PredefinedMenuItem::services(None)).ok();
    app_menu.append(&PredefinedMenuItem::separator()).ok();
    app_menu
        .append(&PredefinedMenuItem::hide(Some(
            tr.invoke_menu_hide(app()).as_str(),
        )))
        .ok();
    app_menu.append(&PredefinedMenuItem::hide_others(None)).ok();
    app_menu.append(&PredefinedMenuItem::show_all(None)).ok();
    app_menu.append(&PredefinedMenuItem::separator()).ok();
    app_menu
        .append(&PredefinedMenuItem::quit(Some(
            tr.invoke_tray_quit(app()).as_str(),
        )))
        .ok();
    menu.append(&app_menu).ok();

    // -- Edit --
    // Undo, Redo, Cut, Copy, Paste and Select All are our own items, not
    // `PredefinedMenuItem`s. A predefined item sends a Cocoa action (`copy:`)
    // to the first responder. That is winit's `WinitView`, and it implements
    // none of them. The menu would consume the shortcut and do nothing, and
    // the Slint `TextInput` would never get the key. Each item here forwards
    // its key press to the Slint window instead. See `edit_shortcuts`.
    let edit_menu = Submenu::with_id("edit", tr.get_menu_edit().as_str(), true);
    let append_edit_item = |item: &edit_shortcuts::EditItem| {
        edit_menu
            .append(&MenuItem::with_id(
                MenuId::new(item.id),
                edit_shortcuts::edit_item_title(tr, item.id).as_str(),
                true,
                Some(item.accelerator.parse().unwrap()),
            ))
            .ok();
    };
    append_edit_item(&EDIT_ITEMS[0]); // Undo
    append_edit_item(&EDIT_ITEMS[1]); // Redo
    edit_menu.append(&PredefinedMenuItem::separator()).ok();
    for item in &EDIT_ITEMS[2..] {
        // Cut, Copy, Paste, Select All
        append_edit_item(item);
    }
    edit_menu.append(&PredefinedMenuItem::separator()).ok();
    edit_menu
        .append(&MenuItem::with_id(
            MenuId::new("find"),
            tr.get_menu_find().as_str(),
            true,
            Some("cmd+f".parse().unwrap()),
        ))
        .ok();
    menu.append(&edit_menu).ok();

    // -- View --
    let view_menu = Submenu::with_id("view", tr.get_menu_view().as_str(), true);
    view_menu
        .append(&MenuItem::with_id(
            MenuId::new("mute"),
            tr.get_menu_toggle_mute().as_str(),
            true,
            Some("cmd+ctrl+m".parse().unwrap()),
        ))
        .ok();
    view_menu
        .append(&MenuItem::with_id(
            MenuId::new("deafen"),
            tr.get_menu_toggle_deafen().as_str(),
            true,
            Some("cmd+ctrl+d".parse().unwrap()),
        ))
        .ok();
    menu.append(&view_menu).ok();

    // -- Help --
    let help_menu = Submenu::with_id("help", tr.get_menu_help().as_str(), true);
    help_menu
        .append(&MenuItem::with_id(
            MenuId::new("github"),
            tr.invoke_menu_github(app()).as_str(),
            true,
            None,
        ))
        .ok();
    menu.append(&help_menu).ok();

    menu
}
