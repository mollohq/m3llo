//! Notification sounds (specs/features/NOTIFICATION-SOUNDS.md).
//!
//! The sounds are Ogg Opus files, built into the binary.
//! [`SoundPlayer`] loads them into libmello once the voice context exists,
//! and plays them from any thread without the command loop, so a sound is
//! never late behind a slow RPC. libmello keeps only the Opus data and
//! decodes a sound on its worker thread when it plays.

use std::sync::{Arc, Mutex};

use serde::{Deserialize, Serialize};

/// One notification sound. Each has its own libmello slot.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Sound {
    YouJoin,
    YouLeave,
    MemberJoin,
    MemberLeave,
    Mute,
    Unmute,
    Deafen,
    Undeafen,
    ConnectionLost,
    CrewLive,
    YouLive,
    StreamEnded,
    Message,
    Mention,
    ClipSaved,
    CrewClip,
    NewMember,
}

/// The settings group that turns a sound on or off.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum SoundGroup {
    Voice,
    Streams,
    Chat,
    Clips,
    Crew,
}

impl Sound {
    pub const ALL: [Sound; 17] = [
        Sound::YouJoin,
        Sound::YouLeave,
        Sound::MemberJoin,
        Sound::MemberLeave,
        Sound::Mute,
        Sound::Unmute,
        Sound::Deafen,
        Sound::Undeafen,
        Sound::ConnectionLost,
        Sound::CrewLive,
        Sound::YouLive,
        Sound::StreamEnded,
        Sound::Message,
        Sound::Mention,
        Sound::ClipSaved,
        Sound::CrewClip,
        Sound::NewMember,
    ];

    /// The libmello slot (`mello_sound_load`, `mello_sound_play`).
    pub fn slot(self) -> u32 {
        self as u32
    }

    pub fn group(self) -> SoundGroup {
        match self {
            Sound::YouJoin
            | Sound::YouLeave
            | Sound::MemberJoin
            | Sound::MemberLeave
            | Sound::Mute
            | Sound::Unmute
            | Sound::Deafen
            | Sound::Undeafen
            | Sound::ConnectionLost => SoundGroup::Voice,
            Sound::CrewLive | Sound::YouLive | Sound::StreamEnded => SoundGroup::Streams,
            Sound::Message | Sound::Mention => SoundGroup::Chat,
            Sound::ClipSaved | Sound::CrewClip => SoundGroup::Clips,
            Sound::NewMember => SoundGroup::Crew,
        }
    }

    /// The sound as an Ogg Opus file.
    pub fn opus(self) -> &'static [u8] {
        macro_rules! opus {
            ($name:literal) => {
                include_bytes!(concat!("../assets/sounds/", $name, ".opus"))
            };
        }
        match self {
            Sound::YouJoin => opus!("you_join"),
            Sound::YouLeave => opus!("you_leave"),
            Sound::MemberJoin => opus!("member_join"),
            Sound::MemberLeave => opus!("member_leave"),
            Sound::Mute => opus!("mute"),
            Sound::Unmute => opus!("unmute"),
            Sound::Deafen => opus!("deafen"),
            Sound::Undeafen => opus!("undeafen"),
            Sound::ConnectionLost => opus!("connection_lost"),
            Sound::CrewLive => opus!("crew_live"),
            Sound::YouLive => opus!("you_live"),
            Sound::StreamEnded => opus!("stream_ended"),
            Sound::Message => opus!("message"),
            Sound::Mention => opus!("mention"),
            Sound::ClipSaved => opus!("clip_saved"),
            Sound::CrewClip => opus!("crew_clip"),
            Sound::NewMember => opus!("new_member"),
        }
    }
}

/// The libmello context that sounds play on. The pointer is only used under
/// the player's mutex, and the voice manager clears it before it destroys
/// the context.
struct Ctx(*mut mello_sys::MelloContext);

// SAFETY: `mello_sound_play` is safe from any thread (it takes the mixer's
// own lock), and the pointer is read and cleared only under the mutex.
unsafe impl Send for Ctx {}

/// Plays notification sounds. Cheap to clone; any thread.
///
/// The client makes one at startup and gives a clone to the core client,
/// which attaches the voice context and loads every sound. A play before
/// that, or after shutdown, does nothing.
#[derive(Clone, Default)]
pub struct SoundPlayer {
    ctx: Arc<Mutex<Option<Ctx>>>,
}

impl SoundPlayer {
    pub fn new() -> Self {
        Self::default()
    }

    /// Plays `sound` once at `volume` (0..1). Returns false when no context
    /// is attached or libmello refused it.
    pub fn play(&self, sound: Sound, volume: f32) -> bool {
        let guard = self.ctx.lock().unwrap_or_else(|e| e.into_inner());
        let Some(Ctx(ctx)) = guard.as_ref() else {
            log::debug!("sound {sound:?}: no voice context yet");
            return false;
        };
        let rc = unsafe { mello_sys::mello_sound_play(*ctx, sound.slot(), volume.clamp(0.0, 1.0)) };
        if rc != mello_sys::MelloResult_MELLO_OK {
            log::warn!("sound {sound:?}: mello_sound_play failed ({rc})");
            return false;
        }
        log::debug!("sound {sound:?} at {volume:.2}");
        true
    }

    /// Loads every sound into `ctx` and plays on it from now on. A load
    /// copies the Opus data and decodes nothing.
    pub(crate) fn attach(&self, ctx: *mut mello_sys::MelloContext) {
        if ctx.is_null() {
            return;
        }
        let mut loaded = 0;
        for sound in Sound::ALL {
            let opus = sound.opus();
            let rc = unsafe {
                mello_sys::mello_sound_load_opus(ctx, sound.slot(), opus.as_ptr(), opus.len())
            };
            if rc == mello_sys::MelloResult_MELLO_OK {
                loaded += 1;
            } else {
                log::error!("sound {sound:?}: mello_sound_load_opus failed ({rc})");
            }
        }
        log::info!(
            "notification sounds: {loaded} of {} loaded",
            Sound::ALL.len()
        );
        *self.ctx.lock().unwrap_or_else(|e| e.into_inner()) = Some(Ctx(ctx));
    }

    /// Stops using the context. Call before the context is destroyed.
    pub(crate) fn detach(&self) {
        *self.ctx.lock().unwrap_or_else(|e| e.into_inner()) = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_asset_is_stereo_ogg_opus() {
        for sound in Sound::ALL {
            let b = sound.opus();
            assert_eq!(&b[0..4], b"OggS", "{sound:?}");
            // The first page holds OpusHead: 27-byte header, 1 lacing value.
            assert_eq!(&b[28..36], b"OpusHead", "{sound:?}");
            assert_eq!(b[37], 2, "{sound:?} is not stereo");
            assert!(b.len() < 64 * 1024, "{sound:?} is {} bytes", b.len());
        }
    }

    #[test]
    fn slots_are_unique_and_fit_libmello() {
        let mut slots: Vec<u32> = Sound::ALL.iter().map(|s| s.slot()).collect();
        slots.sort_unstable();
        slots.dedup();
        assert_eq!(slots.len(), Sound::ALL.len());
        assert!(slots.iter().all(|&s| s < mello_sys::MELLO_SOUND_SLOTS));
    }

    #[test]
    fn a_player_without_a_context_does_nothing() {
        assert!(!SoundPlayer::new().play(Sound::Mute, 1.0));
    }

    #[test]
    fn a_player_plays_on_the_voice_context_until_it_is_destroyed() {
        let (tx, _rx) = std::sync::mpsc::channel();
        let mut voice = crate::voice::VoiceManager::with_test_audio_backend(tx);
        let player = SoundPlayer::new();
        voice.attach_sound_player(player.clone());
        // The test backend has no render thread, so plays wait in libmello's
        // queue (16 deep). A few are enough.
        for sound in [Sound::YouJoin, Sound::Mention, Sound::NewMember] {
            assert!(player.play(sound, 0.8), "{sound:?}");
        }
        drop(voice);
        assert!(
            !player.play(Sound::Mute, 0.8),
            "a play after the context is gone"
        );
    }
}
