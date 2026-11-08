# Notification Sounds

> **Component:** Notification sounds (libmello, mello-core, client, backend crew events)  
> **Version:** 1.0  
> **Status:** Shipped on the desktop client (macOS, Windows, Linux). Not on iOS.  
> **Parent:** [00-ARCHITECTURE.md](../00-ARCHITECTURE.md)  
> **Related:** [03-LIBMELLO.md](../03-LIBMELLO.md) §3, [10-AUDIO_PIPELINE.md](../10-AUDIO_PIPELINE.md) §5.3, [11-PRESENCE-CREW-STATE.md](../11-PRESENCE-CREW-STATE.md) (code 111)

---

## 1. What This Is

The desktop client plays a short sound for voice, stream, chat, clip and crew events.
The sounds are by Petter B. (https://www.instagram.com/petterb).

## 2. Sounds and Events

| Event | Sound file | Group |
|---|---|---|
| You join a voice channel | `you_join.opus` | Voice |
| You leave a voice channel | `you_leave.opus` | Voice |
| Someone joins your channel | `member_join.opus` | Voice |
| Someone leaves your channel | `member_leave.opus` | Voice |
| Mute | `mute.opus` | Voice |
| Unmute | `unmute.opus` | Voice |
| Deafen | `deafen.opus` | Voice |
| Undeafen | `undeafen.opus` | Voice |
| Voice connection lost | `connection_lost.opus` | Voice |
| A crew member goes live | `crew_live.opus` | Streams |
| You start streaming | `you_live.opus` | Streams |
| A stream you watch ends | `stream_ended.opus` | Streams |
| New chat message | `message.opus` | Chat |
| @mention of you | `mention.opus` | Chat |
| Your clip is saved | `clip_saved.opus` | Clips |
| Someone in the crew clipped | `crew_clip.opus` | Clips |
| A new member joins the crew | `new_member.opus` | Crew |

- The files are in `mello-core/assets/sounds/`: Ogg Opus, 48 kHz stereo, 96 kbps, 20 ms frames,
  no tags. mello-core embeds them with `include_bytes!`. About 197 KB in all.
- `scripts/encode-sounds.sh` makes them from the masters with ffmpeg and libopus, constrained VBR.
  The masters are the 48 kHz stereo 16-bit WAV files that shipped first. They are in git history
  at commit `96e8e438`, and the script reads them from there.
- Each master peaks at -26 LUFS momentary loudness, so no sound is louder than the others.
- The member join and leave sounds are the you-join and you-leave sounds without their first part.
- Licence: all rights reserved by Petter B., used with permission. The sounds are not under the
  repository's Apache 2.0 licence. See `mello-core/assets/sounds/LICENSE`.

## 3. Playback (libmello)

`UiSoundMixer` (`libmello/src/audio/ui_sound_mixer.{hpp,cpp}`) decodes the sounds and mixes them into
the voice output. `ogg_opus.{hpp,cpp}` parses and decodes the files.

```c
#define MELLO_SOUND_SLOTS 32
MelloResult mello_sound_load_opus(MelloContext* ctx, uint32_t id, const uint8_t* ogg_opus, size_t len);
MelloResult mello_sound_play(MelloContext* ctx, uint32_t id, float gain);
```

- `mello_sound_load_opus` copies the audio packets of an Ogg Opus file into slot `id` (0..31).
  One stream, mono or stereo, 10 s at most. A load decodes nothing.
  A new load of a slot replaces its sound. A play that waits keeps the old sound until it is decoded.
- Only the Opus data stays in memory. A sound is decoded when it plays and freed after it played.
- `mello_sound_play` puts a request in a 16-entry queue. It is safe from any thread.
  It takes the mixer's own lock only. It never blocks the render thread.
- The mixer's worker thread decodes the request to 48 kHz stereo PCM and hands it to the render
  thread through a lock-free queue. The longest sound (2.7 s) decodes in about 4 ms, so a sound
  starts a few ms after the play.
- The render thread gives each played sound back through a second lock-free queue.
  The worker frees it within 200 ms. The render thread does not allocate or free.
- Memory: the Opus data (about 197 KB), the worker's decoder and scratch buffer (about 50 KB),
  and the PCM of the sounds that wait or play (at most 20).
- At most 4 sounds play at once. A fifth replaces the oldest.
- `render_frame` adds the sounds after deafen, after the clip-buffer tap and after clip playback:
  - They play while deafened. The deafen and undeafen sounds must be heard.
  - They are not in a voice clip.
  - They are in the echo-canceller reference, so the microphone does not send them.
- The voice output volume does not apply. The `gain` is the sound volume setting.
- The output device runs from pipeline init, so the sounds play outside a call.

## 4. mello-core

- `mello_core::sounds::Sound`: one variant per row of §2, with its slot, its group and its Opus file.
- `SoundGroup`: `Voice`, `Streams`, `Chat`, `Clips`, `Crew`.
- `SoundPlayer`: the client makes it at startup and gives a clone to the core (`Client::set_sound_player`).
- `VoiceManager` loads every sound into its context and attaches the player.
  It detaches the player before `mello_destroy`.
- The client plays from the UI thread through the player, not through a `Command`.
  The command loop awaits RPCs, so a command could play late.

## 5. Backend Crew Events

Two crew events (code 111, `PushCrewEvent`, priority events). The payloads carry ids only.

| Event | Sent from | Data |
|---|---|---|
| `clip_posted` | `PostClipRPC`, after the clip is stored. Not on the retry path | `clip_id`, `clipper_id`, `clip_type` |
| `member_joined` | `recordMemberJoined` (the join hook and the invite code) | `user_id` |

## 6. Client Triggers and Guards

`client/src/sounds.rs` holds the cues. `SoundCues` checks the group setting and the rate limit, then plays.

| Sound | Trigger | Guard |
|---|---|---|
| You join | `VoiceStateChanged { in_call: true }` on the SFU | Not while `voice_reconnecting`: a recovered call is silent |
| You leave | `VoiceStateChanged { in_call: false }` | Only after a call was up. Not after a reconnect that gave up. A crew switch or a sign-out in a call leaves the call, so it plays |
| Member join, leave | A diff of the other members in the voice channel you are in | The first member list after you join sets the diff and plays nothing. One sound per 500 ms at most |
| Mute, unmute, deafen, undeafen | The state change in `voice_state::dispatch` and the control bar | Deafen wins: a deafen that also mutes plays the deafen sound only. Push-to-talk plays nothing |
| Connection lost | `VoiceSfuDisconnected` | Once per drop |
| Crew live | 111 `stream_started` | Active crew only. Not your own stream |
| You live | `StreamStarted` | Not on a host reconnect |
| Stream ended | `StreamEnded { role: Viewer }` | Not when you stop watching (`StreamWatchingStopped`) |
| Message | `MessageReceived` | Not your own, not a system message. Only when the window is not focused. One sound per second at most |
| Mention | `MessageReceived` that mentions you | Replaces the message sound. Only when the window is not focused. One sound per second at most |
| Clip saved | `ClipCaptured`, `GameClipCaptured` | |
| Crew clip | 111 `clip_posted` | Active crew only. Not your own clip. The event does not reload the crew state: `feed_changed` reloads the feed |
| New member | 111 `member_joined` | Active crew only. Not you |

Tests: `SoundCues` keeps the last 64 cues. The flow tests in `client/src/flow_tests.rs` assert on them.

## 7. Settings

- Settings > Audio & Video, section "Notification sounds", after Audio levels.
- One volume slider, 0..100 %, default 80 %.
- One switch per group: Voice, Streams, Chat, Clips, Crew. All on by default.
- Stored in `mello.toml`: `sound_volume`, `sound_voice`, `sound_streams`, `sound_chat`, `sound_clips`, `sound_crew`.

## 8. About

The credits read "Sounds by Petter B.", with a link to https://www.instagram.com/petterb.

## 9. Not in Scope

- iOS.
- A sound for a crew that is not active.
- On macOS a desktop stream carries m3llo's own output (CLIPS.md §15.12a), so the sounds are in it too.
