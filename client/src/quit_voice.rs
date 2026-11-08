//! Leave voice before the app exits.
//!
//! A quit used to end the process while in voice, with no SFU leave and no
//! `voice_leave` RPC. Other crew members then saw a ghost member until the
//! server noticed. Every quit path ends the Slint event loop, and `run_app`
//! calls [`leave_voice_before_exit`] after it, so one step covers all of them.
//! The wait is bounded: a quit never hangs on a dead network.

use std::sync::mpsc::{Receiver, RecvTimeoutError};
use std::time::{Duration, Instant};

use mello_core::{Command, Event};
use tokio::sync::mpsc::UnboundedSender;

/// The longest a quit waits for the voice leave.
pub const QUIT_VOICE_LEAVE_TIMEOUT: Duration = Duration::from_millis(1500);

/// What the voice leave before exit did.
#[derive(Debug, PartialEq, Eq)]
pub enum QuitVoiceOutcome {
    /// Not in voice: nothing sent, no wait.
    NotInVoice,
    /// The core finished the leave (SFU leave and `voice_leave` RPC).
    Left,
    /// The core did not finish the leave in time.
    TimedOut,
    /// The core is gone: the command or its answer could not pass.
    CoreGone,
}

/// Send the normal voice leave and wait for the core to finish it, at most
/// `timeout`. The core ends `handle_leave_voice` with
/// `VoiceStateChanged { in_call: false }`; other events are dropped, because
/// the app exits.
pub fn leave_voice_before_exit(
    in_voice: bool,
    cmd_tx: &UnboundedSender<Command>,
    event_rx: &Receiver<Event>,
    timeout: Duration,
) -> QuitVoiceOutcome {
    if !in_voice {
        return QuitVoiceOutcome::NotInVoice;
    }
    log::info!("[quit] leaving voice before exit");
    let started = Instant::now();
    if cmd_tx.send(Command::LeaveVoice).is_err() {
        log::warn!("[quit] voice leave not sent: the core loop is gone");
        return QuitVoiceOutcome::CoreGone;
    }
    let deadline = started + timeout;
    loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        match event_rx.recv_timeout(remaining) {
            Ok(Event::VoiceStateChanged { in_call: false, .. }) => {
                log::info!("[quit] voice left in {} ms", started.elapsed().as_millis());
                return QuitVoiceOutcome::Left;
            }
            // A steady event stream must not hold the quit past the deadline.
            Ok(_) if Instant::now() < deadline => continue,
            Ok(_) | Err(RecvTimeoutError::Timeout) => {
                log::warn!(
                    "[quit] voice leave timed out after {} ms; exiting",
                    timeout.as_millis()
                );
                return QuitVoiceOutcome::TimedOut;
            }
            Err(RecvTimeoutError::Disconnected) => {
                log::warn!("[quit] voice leave: the core loop is gone; exiting");
                return QuitVoiceOutcome::CoreGone;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mello_core::voice::VoiceMode;

    #[test]
    fn not_in_voice_sends_nothing_and_does_not_wait() {
        let (cmd_tx, mut cmd_rx) = tokio::sync::mpsc::unbounded_channel();
        let (_event_tx, event_rx) = std::sync::mpsc::channel::<Event>();
        let started = Instant::now();
        let outcome = leave_voice_before_exit(false, &cmd_tx, &event_rx, Duration::from_secs(10));
        assert_eq!(outcome, QuitVoiceOutcome::NotInVoice);
        assert!(started.elapsed() < Duration::from_secs(1));
        assert!(cmd_rx.try_recv().is_err(), "no command when not in voice");
    }

    #[test]
    fn in_voice_sends_the_leave_and_waits_for_the_core() {
        let (cmd_tx, mut cmd_rx) = tokio::sync::mpsc::unbounded_channel();
        let (event_tx, event_rx) = std::sync::mpsc::channel::<Event>();
        // Other events come first; the wait skips them.
        event_tx
            .send(Event::VoiceStateChanged {
                in_call: true,
                transport: VoiceMode::SFU,
            })
            .expect("send");
        event_tx
            .send(Event::VoiceStateChanged {
                in_call: false,
                transport: VoiceMode::Disconnected,
            })
            .expect("send");
        let outcome = leave_voice_before_exit(true, &cmd_tx, &event_rx, Duration::from_secs(10));
        assert_eq!(outcome, QuitVoiceOutcome::Left);
        assert!(matches!(cmd_rx.try_recv(), Ok(Command::LeaveVoice)));
    }

    #[test]
    fn a_core_that_does_not_answer_times_out() {
        let (cmd_tx, _cmd_rx) = tokio::sync::mpsc::unbounded_channel();
        let (_event_tx, event_rx) = std::sync::mpsc::channel::<Event>();
        let outcome = leave_voice_before_exit(true, &cmd_tx, &event_rx, Duration::from_millis(50));
        assert_eq!(outcome, QuitVoiceOutcome::TimedOut);
    }

    #[test]
    fn a_closed_core_does_not_wait() {
        let (cmd_tx, cmd_rx) = tokio::sync::mpsc::unbounded_channel();
        drop(cmd_rx);
        let (_event_tx, event_rx) = std::sync::mpsc::channel::<Event>();
        let outcome = leave_voice_before_exit(true, &cmd_tx, &event_rx, Duration::from_secs(10));
        assert_eq!(outcome, QuitVoiceOutcome::CoreGone);
    }
}
