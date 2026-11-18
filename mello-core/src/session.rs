use keyring::Entry;

#[cfg(target_os = "ios")]
mod ios;

const SERVICE: &str = "mello";
const USER: &str = "session";

/// Keyring account name holding the refresh token, namespaced by
/// `MELLO_SESSION_KEY` when set.
///
/// The keyring is a single machine-wide store that `MELLO_CONFIG_DIR` does not
/// cover. Without this the release smoke test — which ends in `delete_account`,
/// and therefore in `session::clear()` — wipes the *developer's* saved session
/// on the build machine every release. The runners double as development
/// machines, so that is a real signout, not a test artifact.
fn account() -> String {
    match std::env::var("MELLO_SESSION_KEY") {
        Ok(suffix) if !suffix.is_empty() => format!("{}.{}", USER, suffix),
        _ => USER.to_string(),
    }
}

/// The session file for e2e runs, when the `e2e-session` feature is on and
/// `MELLO_E2E_SESSION_FILE` is set. Each test user gets its own file.
#[cfg(feature = "e2e-session")]
fn e2e_file() -> Option<std::path::PathBuf> {
    std::env::var_os("MELLO_E2E_SESSION_FILE")
        .filter(|v| !v.is_empty())
        .map(std::path::PathBuf::from)
}

pub fn save(refresh_token: &str) -> Result<(), String> {
    #[cfg(feature = "e2e-session")]
    if let Some(path) = e2e_file() {
        return std::fs::write(path, refresh_token).map_err(|e| e.to_string());
    }
    // iOS writes the item itself to set its Keychain class (see `ios`).
    #[cfg(target_os = "ios")]
    {
        ios::write(SERVICE, &account(), refresh_token.as_bytes())
            .map_err(|code| format!("Keychain write failed (OSStatus {code})"))
    }
    #[cfg(not(target_os = "ios"))]
    {
        let entry = Entry::new(SERVICE, &account()).map_err(|e| e.to_string())?;
        entry.set_password(refresh_token).map_err(|e| e.to_string())
    }
}

pub fn load() -> Option<String> {
    #[cfg(feature = "e2e-session")]
    if let Some(path) = e2e_file() {
        return std::fs::read_to_string(path).ok().filter(|t| !t.is_empty());
    }
    #[cfg(target_os = "ios")]
    {
        load_ios()
    }
    #[cfg(not(target_os = "ios"))]
    {
        let entry = Entry::new(SERVICE, &account()).ok()?;
        entry.get_password().ok()
    }
}

/// Reads the session and moves an item from an older build to the
/// after-first-unlock class. The restore at a normal foreground start calls
/// `load`, so an existing install moves on its first start after the update,
/// with no new sign-in. An item that has the class already costs no write.
#[cfg(target_os = "ios")]
fn load_ios() -> Option<String> {
    let account = account();
    let item = match ios::read(SERVICE, &account) {
        Ok(item) => item,
        Err(code) => {
            match read_failure(code) {
                ReadFailure::Missing => {}
                // The item is still there. Only a read before the first
                // unlock after a reboot, or an item from an older build,
                // gets this.
                ReadFailure::Locked => log::warn!(
                    "Keychain is locked, the saved session cannot be read now and stays stored"
                ),
                ReadFailure::Other(code) => {
                    log::warn!(
                        "Keychain read failed (OSStatus {code}), the saved session stays stored"
                    )
                }
            }
            return None;
        }
    };
    if !item.after_first_unlock {
        // A failure changes nothing: the item keeps its value and old class,
        // and the next start tries again.
        match ios::move_to_after_first_unlock(SERVICE, &account) {
            Ok(()) => log::info!("Saved session moved to the after-first-unlock Keychain class"),
            Err(code) => log::warn!(
                "Saved session keeps its old Keychain class (OSStatus {code}), retry at next start"
            ),
        }
    }
    String::from_utf8(item.secret)
        .ok()
        .filter(|t| !t.is_empty())
}

/// Why a Keychain read returned no session.
#[cfg(any(target_os = "ios", test))]
#[derive(Debug, PartialEq, Eq)]
enum ReadFailure {
    /// No saved session.
    Missing,
    /// The item exists but the Keychain does not give it out now. The
    /// session must stay stored.
    Locked,
    Other(i32),
}

#[cfg(any(target_os = "ios", test))]
fn read_failure(code: i32) -> ReadFailure {
    // errSecItemNotFound and errSecInteractionNotAllowed from SecBase.h.
    match code {
        -25300 => ReadFailure::Missing,
        -25308 => ReadFailure::Locked,
        other => ReadFailure::Other(other),
    }
}

pub fn clear() {
    #[cfg(feature = "e2e-session")]
    if let Some(path) = e2e_file() {
        let _ = std::fs::remove_file(path);
        return;
    }
    if let Ok(entry) = Entry::new(SERVICE, &account()) {
        let _ = entry.delete_credential();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Serialises the env mutation below. Tests share one process, so an
    /// unguarded `set_var` leaks into whichever test runs concurrently.
    static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

    /// The restore gets `None` for both cases. A locked Keychain must get its
    /// own warning, so a log never shows it as a missing session.
    #[test]
    fn locked_keychain_is_not_a_missing_session() {
        assert_eq!(read_failure(-25308), ReadFailure::Locked);
        assert_eq!(read_failure(-25300), ReadFailure::Missing);
        assert_eq!(read_failure(-25291), ReadFailure::Other(-25291));
    }

    #[test]
    fn account_defaults_to_the_shared_entry() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("MELLO_SESSION_KEY");
        assert_eq!(account(), "session");
    }

    #[test]
    fn account_is_namespaced_when_a_key_is_set() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("MELLO_SESSION_KEY", "smoke");
        assert_eq!(account(), "session.smoke");
        std::env::remove_var("MELLO_SESSION_KEY");
    }

    /// An empty value must fall back rather than create a `session.` entry that
    /// silently differs from the default — CI passing an unset variable through
    /// as "" is normal.
    #[test]
    fn empty_key_falls_back_to_the_default() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        std::env::set_var("MELLO_SESSION_KEY", "");
        assert_eq!(account(), "session");
        std::env::remove_var("MELLO_SESSION_KEY");
    }

    /// With the file set, the token never touches the keyring: a rebuilt test
    /// binary would otherwise block on a keychain prompt.
    #[cfg(feature = "e2e-session")]
    #[test]
    fn e2e_session_file_round_trips() {
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let path = std::env::temp_dir().join(format!("mello-e2e-session-{}", std::process::id()));
        std::env::set_var("MELLO_E2E_SESSION_FILE", &path);

        save("rt-123").expect("save to file");
        assert_eq!(
            std::fs::read_to_string(&path).ok().as_deref(),
            Some("rt-123")
        );
        assert_eq!(load().as_deref(), Some("rt-123"));
        clear();
        assert!(!path.exists());
        assert_eq!(load(), None);

        std::env::remove_var("MELLO_E2E_SESSION_FILE");
    }
}
