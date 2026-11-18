//! Restart the app.
//!
//! A change of the UI language applies at the next start
//! (specs/features/LOCALIZATION.md section 9). The app keeps running in the
//! tray after a close, so the Language row has a "Restart now" button.
//!
//! The button asks for a restart ([`request`]) and quits the normal way
//! (`quit_voice::request_quit`). `lib.rs::run` starts the app again
//! ([`start_again`]) after `run_app` returns. `run_app` drops the
//! single-instance lock when it returns, so the new process gets the lock.

use std::sync::atomic::{AtomicBool, Ordering};

/// Set when the user asks for a restart. Read one time, after the event loop.
static REQUESTED: AtomicBool = AtomicBool::new(false);

/// Asks for a restart after the quit. A seam (`AppContext::restart`): the
/// test harness puts its own function there, so a test starts no process.
pub type RestartHook = Box<dyn Fn()>;

/// Ask for a restart after the quit. The production [`RestartHook`].
pub(crate) fn request() {
    log::info!("[restart] restart requested");
    REQUESTED.store(true, Ordering::SeqCst);
}

/// True when a restart was asked for.
pub(crate) fn requested() -> bool {
    REQUESTED.load(Ordering::SeqCst)
}

/// The arguments of the new process: the arguments of this run without a
/// `mello://` deep link (the link was used one time) and without `--reset`
/// (it would erase the setting that the restart applies). `args` does not
/// have the program name.
pub(crate) fn restart_args<I, S>(args: I) -> Vec<String>
where
    I: IntoIterator<Item = S>,
    S: AsRef<str>,
{
    args.into_iter()
        .map(|a| a.as_ref().to_string())
        .filter(|a| !a.to_ascii_lowercase().starts_with("mello://") && a != "--reset")
        .collect()
}

/// Start this executable again with the arguments of this run
/// ([`restart_args`]). Call it after `run_app` returns.
pub(crate) fn start_again() {
    let exe = match std::env::current_exe() {
        Ok(exe) => exe,
        Err(e) => {
            log::error!("[restart] cannot find the executable: {e}");
            return;
        }
    };
    let args = restart_args(std::env::args().skip(1));
    log::info!("[restart] starting {} {:?}", exe.display(), args);
    match std::process::Command::new(&exe).args(&args).spawn() {
        Ok(child) => log::info!("[restart] started process {}", child.id()),
        Err(e) => log::error!("[restart] cannot start {}: {e}", exe.display()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_restart_keeps_the_arguments_without_the_deep_link() {
        assert_eq!(
            restart_args(["mello://join/ABCD-1234", "--instance", "b", "--loopback"]),
            ["--instance", "b", "--loopback"]
        );
        assert_eq!(restart_args(["MELLO://crew/x"]), Vec::<String>::new());
        assert_eq!(
            restart_args(["--instance", "b", "--reset"]),
            ["--instance", "b"]
        );
        assert_eq!(restart_args(Vec::<&str>::new()), Vec::<String>::new());
    }
}
