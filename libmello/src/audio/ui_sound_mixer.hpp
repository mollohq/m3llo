#pragma once

// Notification sounds (specs/features/NOTIFICATION-SOUNDS.md): short sounds that
// the app loads once as Ogg Opus and plays on events (a member joins, a mute,
// a message). Only the Opus data stays in memory. A play decodes its sound on
// the mixer's worker thread. The render thread mixes the PCM into the speaker
// output after the deafen gate and after the clip-buffer tap, then gives it
// back to the worker, which frees it.
//
// Threads:
// - Control (any thread, the caller serializes, as for every MelloContext
//   call): load_opus, play. They take `mutex_` only.
// - Worker (one, started by the first load): decodes the plays and frees the
//   played sounds. It takes `mutex_`, but not while it decodes.
// - Render (the audio device thread): mix_into. It takes no lock, does not
//   allocate and does not free.
//
// Memory: the Opus data of the loaded sounds, the worker's decoder and scratch
// buffer (about 50 KB), and the PCM of the sounds that wait or play (at most
// kMaxLive). The worker frees a played sound within kReapInterval.

#include "ogg_opus.hpp"

#include <array>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <memory>
#include <mutex>
#include <thread>
#include <vector>

namespace mello::audio {

class UiSoundMixer {
public:
    static constexpr uint32_t kSampleRate = 48000;
    static constexpr uint32_t kChannels = 2;
    static constexpr uint32_t kMaxSounds = 32;
    /// Sounds that play at the same time. A play over the limit replaces the
    /// sound that started first.
    static constexpr size_t kMaxVoices = 4;
    /// Plays that wait for the worker, and decoded sounds that wait for the
    /// render thread. A play over the limit is dropped.
    static constexpr size_t kQueueSize = 16;
    /// Decoded sounds that exist at one time: waiting, playing or played.
    static constexpr size_t kMaxLive = kQueueSize + kMaxVoices;
    /// At most 10 s of sound in one slot.
    static constexpr size_t kMaxFrames = kSampleRate * 10;
    /// How often the worker frees played sounds while any exist.
    static constexpr std::chrono::milliseconds kReapInterval{200};

    UiSoundMixer() = default;
    /// Stops the worker. The render thread must be stopped first.
    ~UiSoundMixer();
    UiSoundMixer(const UiSoundMixer&) = delete;
    UiSoundMixer& operator=(const UiSoundMixer&) = delete;

    // ---- Control ---------------------------------------------------------

    /// Copies the Ogg Opus file `ogg` into slot `id`. Returns false for a bad
    /// id, a file that does not parse or more than kMaxFrames.
    bool load_opus(uint32_t id, const uint8_t* ogg, size_t len);
    /// Queues slot `id` to decode and play at `gain` (0..1, clamped). Returns
    /// false when the slot is empty or kQueueSize plays wait.
    bool play(uint32_t id, float gain);
    bool loaded(uint32_t id) const;

    // ---- Tests -------------------------------------------------------------

    /// Plays `frames` interleaved stereo frames at `gain` without a slot or
    /// the worker. Returns false when kQueueSize sounds wait for the render
    /// thread.
    bool play_pcm(const int16_t* stereo, size_t frames, float gain);
    /// Waits until the worker has decoded every queued play.
    void wait_decoded();
    /// Frees the played sounds and returns the count of sounds that wait or play.
    size_t live_sounds();

    // ---- Render thread -----------------------------------------------------

    /// Adds the next `frames` stereo frames of every sound that plays into
    /// `stereo`, and their mono mix into `mono` (the echo canceller's
    /// reference), saturated. Returns true when any sound played.
    bool mix_into(int16_t* stereo, int16_t* mono, size_t frames);

    /// Sounds that play now. For tests and logs; any thread.
    size_t active_voices() const { return active_.load(std::memory_order_relaxed); }

private:
    struct Pcm {
        std::vector<int16_t> samples;  // interleaved stereo
        size_t frames = 0;
    };
    struct Request {
        uint32_t id = 0;
        float gain = 0.0f;
    };
    struct Ready {
        std::unique_ptr<Pcm> pcm;
        float gain = 0.0f;
    };
    struct Voice {
        std::unique_ptr<Pcm> pcm;
        size_t pos = 0;      // frames played
        float gain = 0.0f;
        uint64_t order = 0;  // start order, for replacement
    };

    // A played sound goes back to the worker through `done_`. It never
    // fills: every sound in it counts in `live_`, and `live_` <= kMaxLive.
    static constexpr size_t kDoneSize = 32;
    static_assert(kDoneSize >= kMaxLive, "a played sound must always fit in done_");

    void worker_loop();
    bool submit_locked(std::unique_ptr<Pcm> pcm, float gain);
    void reap_locked();
    void take_ready();             // render thread
    void retire(std::unique_ptr<Pcm> pcm);  // render thread

    mutable std::mutex mutex_;
    std::condition_variable wake_;  // the worker: a play or the stop
    std::condition_variable idle_;  // wait_decoded
    // Under mutex_.
    std::array<std::shared_ptr<const OggOpus>, kMaxSounds> sounds_{};
    std::deque<Request> requests_;
    bool decoding_ = false;
    bool stop_ = false;
    size_t live_ = 0;
    std::thread worker_;

    // Worker (under mutex_) to render thread.
    std::array<Ready, kQueueSize> ready_{};
    std::atomic<uint64_t> ready_write_{0};
    std::atomic<uint64_t> ready_read_{0};

    // Render thread to worker (under mutex_).
    std::array<std::unique_ptr<Pcm>, kDoneSize> done_{};
    std::atomic<uint64_t> done_write_{0};
    std::atomic<uint64_t> done_read_{0};

    // Render thread only.
    std::array<Voice, kMaxVoices> voices_{};
    uint64_t next_order_ = 0;
    std::atomic<size_t> active_{0};
};

}  // namespace mello::audio
