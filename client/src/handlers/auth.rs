use mello_core::events::AuthFailure;
use mello_core::{Command, Event};

use crate::app_context::AppContext;
use crate::converters::make_initials;
use crate::deep_link::DeepLink;
use crate::onboarding::OnboardingState;

pub fn handle(ctx: &AppContext, event: Event) {
    match event {
        Event::Restoring => {
            log::info!("[auth] restoring session…");
            ctx.app.set_login_loading(true);
        }
        Event::DeviceAuthed { user, created } => {
            log::info!(
                "[auth] device-authed  user_id={} name={} tag={} created={}",
                user.id,
                user.display_name,
                user.tag,
                created
            );
            let uid = user.id.clone();
            ctx.app.set_user_id(user.id.into());
            ctx.app
                .set_user_initials(make_initials(&user.display_name).into());
            ctx.app.set_user_name(user.display_name.into());
            ctx.app.set_user_tag(user.tag.into());
            ctx.app.set_is_returning_user(!created);

            // Device auth after a failed restore (#71). The user finished
            // onboarding on this machine, so an existing device account opens
            // the app, the same as a restored session.
            if ctx.session_recovery.replace(false) {
                if created {
                    log::info!(
                        "[auth] restore failed and the device account is new — back to step 1"
                    );
                    crate::onboarding::advance(ctx, crate::onboarding::Input::RestoreFailed);
                } else {
                    log::info!("[auth] session lost, device account found — opening the app");
                    ctx.app.set_logged_in(true);
                    crate::onboarding::advance(ctx, crate::onboarding::Input::SessionRestored);
                    let _ = ctx.cmd_tx.send(Command::LoadMyCrews);
                    let _ = ctx.cmd_tx.send(Command::FetchUserAvatar { user_id: uid });
                    send_account_language(ctx);
                    dispatch_pending_deep_link(ctx);
                }
            }
        }
        Event::OnboardingReady { user } => {
            ctx.app.set_onboarding_busy(false);
            log::info!(
                "[onboarding] ready — user_id={} name={}",
                user.id,
                user.display_name
            );
            ctx.app.set_user_id(user.id.into());
            ctx.app
                .set_user_initials(make_initials(&user.display_name).into());
            ctx.app.set_user_name(user.display_name.into());
            ctx.app.set_user_tag(user.tag.into());
            ctx.app.set_logged_in(true);
            // Release the pending crew avatar only now that onboarding has
            // actually succeeded. It is deliberately retained through a failed
            // attempt so a retry still carries it.
            *ctx.new_crew_avatar_b64.lock().unwrap() = None;
            {
                let mut s = ctx.settings.borrow_mut();
                s.pending_crew_id = None;
                s.pending_crew_name = None;
            }
            // Finalize joined the invited crew, if any. Forget the invite
            // before the pending link below, so it does not open again.
            crate::onboarding_invite::clear(&ctx.app, &ctx.settings);
            // The account exists, but identity linking is still offered — so
            // this is LinkIdentity, not Done.
            crate::onboarding::advance(ctx, crate::onboarding::Input::AccountReady);
            let _ = ctx.cmd_tx.send(Command::LoadMyCrews);
            send_account_language(ctx);
            dispatch_pending_deep_link(ctx);
        }
        Event::OnboardingInviteFailed { error } => {
            crate::onboarding_invite::join_failed(ctx, error);
        }
        Event::OnboardingFailed { reason, .. } => {
            // Release the guard: the user is still on the same step and must be
            // able to retry.
            ctx.app.set_onboarding_busy(false);
            log::error!("[onboarding] finalization failed: {:?}", reason);
            ctx.app.set_link_error(failure_text(ctx, &reason).into());
        }
        Event::EmailLinked => {
            log::info!("[auth] email linked — onboarding complete");
            ctx.app.set_logged_in(true);
            crate::onboarding::advance(ctx, crate::onboarding::Input::IdentitySettled);
        }
        Event::EmailLinkFailed { reason } => {
            log::warn!("[auth] email-link-failed  reason={:?}", reason);
            ctx.app.set_link_error(failure_text(ctx, &reason).into());
        }
        Event::SocialLinked => {
            log::info!("[auth] social identity linked — onboarding complete");
            ctx.app.set_logged_in(true);
            crate::onboarding::advance(ctx, crate::onboarding::Input::IdentitySettled);
        }
        // No UI affordance deletes an account yet — `Command::DeleteAccount`
        // exists for the release smoke test, which drives it from a scenario
        // and asserts on the event rather than on the window. Log only; give
        // these arms real UI behaviour when a delete-account setting lands.
        Event::AccountDeleted => {
            log::info!("[auth] account deleted — session cleared");
        }
        Event::AccountDeleteFailed { reason } => {
            log::warn!("[auth] account-delete-failed  reason={}", reason);
        }
        Event::SocialLinkFailed { reason } => {
            log::warn!("[auth] social-link-failed  reason={:?}", reason);
            ctx.app.set_login_loading(false);
            ctx.app.set_link_error(failure_text(ctx, &reason).into());
        }
        Event::LoggedIn { user } => {
            log::info!(
                "[auth] logged-in  user_id={} name={} tag={}",
                user.id,
                user.display_name,
                user.tag
            );
            ctx.app.set_logged_in(true);
            ctx.app.set_login_loading(false);
            ctx.app.set_show_sign_in(false);
            ctx.app.set_login_error("".into());
            ctx.app.set_login_account_missing(false);
            let uid = user.id.clone();
            ctx.app.set_user_id(user.id.into());
            ctx.app
                .set_user_initials(make_initials(&user.display_name).into());
            ctx.app.set_user_name(user.display_name.into());
            ctx.app.set_user_tag(user.tag.into());
            crate::onboarding::advance(ctx, crate::onboarding::Input::SessionRestored);
            let _ = ctx.cmd_tx.send(Command::FetchUserAvatar { user_id: uid });
            send_account_language(ctx);

            dispatch_pending_deep_link(ctx);
        }
        Event::LoginFailed { reason } => {
            log::warn!("[auth] login-failed  reason={:?}", reason);
            ctx.app.set_login_loading(false);
            ctx.app.set_logged_in(false);

            if ctx.session_recovery.replace(false) {
                // Device auth after a failed restore failed too. Nobody was
                // signing in, so there is no error to show: go to step 1.
                log::warn!("[auth] device auth after a failed restore failed — back to step 1");
                crate::onboarding::advance(ctx, crate::onboarding::Input::RestoreFailed);
                return;
            }

            if reason.is_silent() {
                restore_failed(ctx);
                return;
            }

            let failure = {
                use slint::ComponentHandle as _;
                SignInFailure::from_failure(&ctx.app.global::<crate::Tr>(), &reason)
            };
            ctx.app.set_login_error(failure.message.into());
            ctx.app.set_login_account_missing(failure.account_missing);
        }
        _ => {}
    }
}

/// After a sign-in: the account gets the UI language of this run
/// (`lang_tag`). The core writes it only when the account has another tag.
fn send_account_language(ctx: &AppContext) {
    let _ = ctx.cmd_tx.send(Command::SetAccountLanguage {
        lang_tag: crate::locale::ui_language(),
    });
}

/// The saved session did not restore (an empty `LoginFailed` reason).
///
/// A user who finished onboarding on this machine and has a device account
/// gets device auth first, and the window stays on the restore wait. The
/// answer opens the app or goes to step 1 (see `Event::DeviceAuthed`). Before
/// #71 this went to step 1 at once, and the device account that owns the
/// user's crews waited behind a "Sign in" control.
fn restore_failed(ctx: &AppContext) {
    let device_id = {
        let s = ctx.settings.borrow();
        s.device_id.clone().filter(|_| s.has_device_account())
    };
    let onboarded =
        OnboardingState::from_step(ctx.app.get_onboarding_step()) == OnboardingState::Done;

    match device_id {
        Some(device_id) if onboarded => {
            log::info!("[auth] restore failed — device auth decides between the app and step 1");
            ctx.session_recovery.set(true);
            let _ = ctx.cmd_tx.send(Command::DeviceAuth { device_id });
        }
        Some(device_id) => {
            log::info!("[auth] restore failed — falling back to device auth");
            crate::onboarding::advance(ctx, crate::onboarding::Input::RestoreFailed);
            let _ = ctx.cmd_tx.send(Command::DeviceAuth { device_id });
        }
        None => {
            log::info!("[auth] restore failed — no device account, back to step 1");
            crate::onboarding::advance(ctx, crate::onboarding::Input::RestoreFailed);
        }
    }
}

/// The text of a failed sign-in, link or onboarding step, in the UI language.
fn failure_text(ctx: &AppContext, failure: &AuthFailure) -> String {
    use slint::ComponentHandle as _;
    SignInFailure::from_failure(&ctx.app.global::<crate::Tr>(), failure).message
}

/// The message of a failure that the core reports as a value. Server text
/// (`AuthFailure::Server`) is data and stays as it is.
pub(crate) fn auth_failure_text(tr: &crate::Tr<'_>, failure: &AuthFailure) -> String {
    use mello_core::crew::InviteError;
    match failure {
        AuthFailure::NoSession => String::new(),
        AuthFailure::Server(text) => text.clone(),
        AuthFailure::Cancelled(p) => tr.invoke_auth_cancelled(p.into()).into(),
        AuthFailure::ProviderFailed { provider, detail } => tr
            .invoke_auth_provider_failed(provider.into(), detail.into())
            .into(),
        AuthFailure::Unexpected(p) => tr.invoke_auth_unexpected(p.into()).into(),
        AuthFailure::NotConfigured(p) => tr.invoke_auth_not_configured(p.into()).into(),
        AuthFailure::NotAvailable(p) => tr.invoke_auth_not_available(p.into()).into(),
        AuthFailure::NoToken(p) => tr.invoke_auth_no_token(p.into()).into(),
        AuthFailure::Socket(detail) => tr.invoke_auth_socket_failed(detail.into()).into(),
        AuthFailure::AccountNotCreated => tr.get_onboarding_account_failed().into(),
        AuthFailure::NotConnected => tr.get_onboarding_connect_failed().into(),
        AuthFailure::CrewNotCreated => tr.get_onboarding_crew_create_failed().into(),
        AuthFailure::CrewNotJoined(error) => match error {
            InviteError::CrewFull => tr.get_onboarding_crew_full(),
            InviteError::InvalidCode => tr.get_onboarding_crew_gone(),
            InviteError::NotAllowed => tr.get_onboarding_crew_not_allowed(),
            InviteError::Failed => tr.get_onboarding_crew_join_failed(),
        }
        .into(),
    }
}

/// What the sign-in panel shows when a sign-in fails.
///
/// The core reports the server's text, for example "Authentication failed:
/// User account not found.". A user cannot act on that (#67), so the known
/// server answers become plain messages.
#[derive(Debug, PartialEq, Eq)]
pub(crate) struct SignInFailure {
    pub message: String,
    /// No account has this identity. The panel offers "Start as a new player".
    pub account_missing: bool,
}

impl SignInFailure {
    /// The message of a failure: a known server answer becomes a plain
    /// message, and a failure that the client names gets its message.
    pub(crate) fn from_failure(tr: &crate::Tr<'_>, failure: &AuthFailure) -> Self {
        match failure {
            AuthFailure::Server(reason) => Self::from_reason(tr, reason),
            other => Self {
                message: auth_failure_text(tr, other),
                account_missing: false,
            },
        }
    }

    pub(crate) fn from_reason(tr: &crate::Tr<'_>, reason: &str) -> Self {
        let lower = reason.to_ascii_lowercase();
        let (message, account_missing) = if lower.contains("account not found") {
            (tr.get_sign_in_no_account(), true)
        } else if lower.contains("invalid credentials") {
            (tr.get_sign_in_wrong_password(), false)
        } else if lower.starts_with("authentication failed") {
            (tr.get_sign_in_failed(), false)
        } else {
            // Other server text is data: it shows as it is.
            return Self {
                message: reason.to_string(),
                account_missing: false,
            };
        };
        Self {
            message: message.to_string(),
            account_missing,
        }
    }
}

fn dispatch_pending_deep_link(ctx: &AppContext) {
    // An invite that onboarding stored, when the user signed in to an
    // existing account instead of finishing onboarding (#68). The account
    // exists now, so the invite opens the join modal.
    let stored_invite = ctx.settings.borrow().pending_invite_code.clone();
    if let Some(code) = stored_invite {
        crate::onboarding_invite::clear(&ctx.app, &ctx.settings);
        ctx.settings.borrow().save();
        log::info!(
            "[deep_link] dispatching the invite stored by onboarding code={}",
            code
        );
        let _ = ctx.cmd_tx.send(Command::ResolveCrewInvite { code });
    }

    let link = ctx.pending_deep_link.borrow_mut().take();
    if let Some(deep_link) = link {
        match deep_link {
            DeepLink::Join { code } => {
                log::info!("[deep_link] dispatching pending join code={}", code);
                let _ = ctx.cmd_tx.send(Command::ResolveCrewInvite { code });
            }
            DeepLink::Crew { id } => {
                log::info!("[deep_link] dispatching pending crew select id={}", id);
                let _ = ctx.cmd_tx.send(Command::SelectCrew { crew_id: id });
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{auth_failure_text, SignInFailure};
    use crate::l10n_format::test_tr;
    use mello_core::events::AuthFailure;

    /// #67: the raw server text for an unknown account never reaches the user.
    #[test]
    fn an_unknown_account_reads_as_a_plain_message_with_a_way_forward() {
        let f =
            SignInFailure::from_reason(test_tr(), "Authentication failed: User account not found.");
        assert_eq!(f.message, "No account found.");
        assert!(f.account_missing);
    }

    #[test]
    fn other_server_answers_read_as_plain_messages() {
        let f =
            SignInFailure::from_reason(test_tr(), "Authentication failed: Invalid credentials.");
        assert_eq!(f.message, "Wrong email or password.");
        assert!(!f.account_missing);

        let f = SignInFailure::from_reason(test_tr(), "Authentication failed: something new");
        assert_eq!(f.message, "Sign-in failed. Try again.");
        assert!(!f.account_missing);
    }

    #[test]
    fn client_messages_pass_through() {
        let reason = "HTTP error: connection refused";
        let f = SignInFailure::from_reason(test_tr(), reason);
        assert_eq!(f.message, reason);
        assert!(!f.account_missing);
    }

    #[test]
    fn a_failure_that_the_client_names_reads_in_english() {
        let f = SignInFailure::from_failure(
            test_tr(),
            &AuthFailure::ProviderFailed {
                provider: "Discord".into(),
                detail: "Timeout waiting for authentication".into(),
            },
        );
        assert_eq!(
            f.message,
            "Discord sign-in failed: Timeout waiting for authentication"
        );
        assert_eq!(
            auth_failure_text(test_tr(), &AuthFailure::Cancelled("Steam".into())),
            "You cancelled the Steam sign-in."
        );
        assert_eq!(
            auth_failure_text(
                test_tr(),
                &AuthFailure::CrewNotJoined(mello_core::crew::InviteError::CrewFull)
            ),
            "That crew is full. Pick another one."
        );
        assert_eq!(
            auth_failure_text(test_tr(), &AuthFailure::NotAvailable("Apple".into())),
            "Apple sign-in isn't available here"
        );
    }

    /// Under the pseudo language a failure that the client names changes
    /// and keeps the provider.
    #[cfg(debug_assertions)]
    #[test]
    fn a_failure_follows_the_ui_language() {
        use crate::build_l10n::{pseudo, PSEUDO};
        let tr = test_tr();
        crate::locale::select(PSEUDO).expect("select qps");
        let text = auth_failure_text(tr, &AuthFailure::Cancelled("Discord".into()));
        let server = auth_failure_text(tr, &AuthFailure::Server("raw text".into()));
        crate::locale::select_source_language();
        assert_eq!(
            text,
            pseudo("You cancelled the {} sign-in.").replace("{}", "Discord")
        );
        assert_ne!(text, "You cancelled the Discord sign-in.");
        assert_eq!(server, "raw text", "server text is data");
    }
}
