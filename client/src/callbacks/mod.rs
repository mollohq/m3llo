pub(crate) mod auth;
pub(crate) mod chat;
mod clip;
pub(crate) mod crew;
mod crew_settings;
pub(crate) mod game;
mod games;
pub(crate) mod onboarding;
mod quests;
#[cfg(test)]
pub(crate) use quests::banner_jpeg as banner_jpeg_for_test;
#[cfg(test)]
pub(crate) use quests::invite_name_ok as invite_name_ok_for_test;
#[cfg(test)]
pub(crate) use quests::{emoji_name_ok as emoji_name_ok_for_test, emoji_png as emoji_png_for_test};
pub(crate) use quests::{
    gift_failure_text, on_gift_result, on_invite_name_checked, refresh_picker_art,
};
mod sealed;
pub(crate) mod settings;
pub(crate) mod streaming;
mod voice;
mod window;

use crate::app_context::AppContext;

pub use chat::{add_mention_member, refresh_mention_members};

/// The name of the image filter of a file dialog, from `Tr`. Call it on the
/// UI thread, before the dialog task starts.
pub(crate) fn images_filter(app: &slint::Weak<crate::MainWindow>) -> String {
    use slint::ComponentHandle as _;
    app.upgrade()
        .map(|app| {
            app.global::<crate::Tr>()
                .get_file_filter_images()
                .to_string()
        })
        .unwrap_or_default()
}

pub fn wire_all(ctx: &AppContext) {
    auth::wire(ctx);
    crew::wire(ctx);
    crew_settings::wire(ctx);
    voice::wire(ctx);
    chat::wire(ctx);
    clip::wire(ctx);
    sealed::wire(ctx);
    quests::wire(ctx);
    streaming::wire(ctx);
    settings::wire(ctx);
    onboarding::wire(ctx);
    game::wire(ctx);
    games::wire(ctx);
    window::wire(ctx);
}
