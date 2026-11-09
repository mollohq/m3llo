use notify_rust::Notification;

/// The AppUserModelID that Velopack puts on the Start menu and desktop
/// shortcuts: `velopack.<packId>`, with `--packId m3llo` in release.yml.
/// Windows shows a toast with the name and icon of the shortcut that has
/// this id. Without it, notify-rust sends the toast as Windows PowerShell.
#[cfg(target_os = "windows")]
const TOAST_APP_ID: &str = "velopack.m3llo";

/// A notification with the app's identity on it.
fn notification() -> Notification {
    #[allow(unused_mut)]
    let mut n = Notification::new();
    #[cfg(target_os = "windows")]
    n.app_id(TOAST_APP_ID);
    n
}

/// Shows an OS notification and returns true when the OS accepted it.
///
/// A seam (`AppContext::tray_notice`): the test harness sets none, so a test
/// never posts a real notification.
/// It takes the title and the body.
pub type ShowNotice = Box<dyn Fn(&str, &str) -> bool>;

/// The title and the body of the tray notice, in the UI language. Read
/// them on the UI thread.
pub fn tray_notice_text(tr: &crate::Tr<'_>) -> (String, String) {
    let title = tr.invoke_notice_tray_title(crate::APP_NAME.into());
    #[cfg(target_os = "macos")]
    let body = tr.get_notice_tray_body_macos();
    #[cfg(not(target_os = "macos"))]
    let body = tr.get_notice_tray_body();
    (title.into(), body.into())
}

/// Tell the user that the closed window went to the tray, and that m3llo
/// did not quit. Users who did not know this thought the app was closed.
pub fn notify_closed_to_tray(title: &str, body: &str) -> bool {
    match notification().summary(title).body(body).show() {
        Ok(_) => true,
        Err(e) => {
            log::warn!("[close] tray notice not shown: {e}");
            false
        }
    }
}

/// Shows an OS notification with this text.
///
/// A seam (`AppContext::stream_key_notice`): the test harness sets none, so
/// a test never posts a real notification.
/// It takes the title and the text.
pub type ShowText = Box<dyn Fn(&str, &str)>;

/// The result of a press of the stream key (spec 12 section 11). The user
/// plays in fullscreen and does not see the app, so the OS tells them.
/// `title` and `text` come from `Tr`.
pub fn notify_stream_key(title: &str, text: &str) {
    if let Err(e) = notification()
        .summary(title)
        .body(text)
        .timeout(notify_rust::Timeout::Milliseconds(4000))
        .show()
    {
        log::warn!("[stream key] notice {text:?} not shown: {e}");
    }
}

/// A member joined voice while the window is hidden.
pub fn notify_member_joined(tr: &crate::Tr<'_>, name: &str) {
    notification()
        .summary(&tr.get_notice_crew_title())
        .body(&tr.invoke_notice_member_joined(name.into()))
        .timeout(notify_rust::Timeout::Milliseconds(4000))
        .show()
        .ok();
}

/// A chat message while the window is hidden.
pub fn notify_message(tr: &crate::Tr<'_>, crew: &str, sender: &str, preview: &str) {
    notification()
        .summary(crew)
        .body(&tr.invoke_notice_message(sender.into(), preview.into()))
        .timeout(notify_rust::Timeout::Milliseconds(4000))
        .show()
        .ok();
}

#[allow(dead_code)]
pub fn notify_invite(tr: &crate::Tr<'_>, inviter: &str, crew: &str) {
    notification()
        .summary(&tr.invoke_notice_invite_title(crate::APP_NAME.into()))
        .body(&tr.invoke_notice_invite_body(inviter.into(), crew.into()))
        .timeout(notify_rust::Timeout::Milliseconds(0)) // persistent
        .show()
        .ok();
}
