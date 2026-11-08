#include "ui_sound_mixer.hpp"

#include "../util/log.hpp"

#include <algorithm>
#include <cmath>

namespace mello::audio {

namespace {

int16_t saturate(float v) {
    return static_cast<int16_t>(std::clamp(std::lround(v), -32768L, 32767L));
}

float clamp_gain(float gain) {
    if (!std::isfinite(gain)) return 0.0f;
    return std::clamp(gain, 0.0f, 1.0f);
}

}  // namespace

UiSoundMixer::~UiSoundMixer() {
    {
        std::lock_guard<std::mutex> lock(mutex_);
        stop_ = true;
    }
    wake_.notify_all();
    if (worker_.joinable()) worker_.join();
    // The render thread has stopped: the members free the rest.
}

bool UiSoundMixer::load_opus(uint32_t id, const uint8_t* ogg, size_t len) {
    if (id >= kMaxSounds || !ogg || len == 0) {
        MELLO_LOG_WARN("ui_sound", "load rejected: id=%u bytes=%zu", id, len);
        return false;
    }
    auto parsed = parse_ogg_opus(ogg, len);
    if (!parsed || parsed->frames > kMaxFrames) {
        MELLO_LOG_WARN("ui_sound", "load rejected: sound %u is not Ogg Opus of at most 10 s",
                       id);
        return false;
    }
    const size_t frames = parsed->frames;
    const size_t bytes = parsed->packets.size();
    auto sound = std::make_shared<const OggOpus>(std::move(*parsed));

    std::lock_guard<std::mutex> lock(mutex_);
    sounds_[id] = std::move(sound);
    if (!worker_.joinable()) worker_ = std::thread(&UiSoundMixer::worker_loop, this);
    MELLO_LOG_INFO("ui_sound", "loaded sound %u: %zu frames (%.2f s), %zu bytes of Opus", id,
                   frames, static_cast<double>(frames) / kSampleRate, bytes);
    return true;
}

bool UiSoundMixer::loaded(uint32_t id) const {
    if (id >= kMaxSounds) return false;
    std::lock_guard<std::mutex> lock(mutex_);
    return sounds_[id] != nullptr;
}

bool UiSoundMixer::play(uint32_t id, float gain) {
    {
        std::lock_guard<std::mutex> lock(mutex_);
        if (id >= kMaxSounds || !sounds_[id]) {
            MELLO_LOG_WARN("ui_sound", "play: sound %u is not loaded", id);
            return false;
        }
        if (requests_.size() >= kQueueSize) {
            MELLO_LOG_WARN("ui_sound", "play: queue full, sound %u dropped", id);
            return false;
        }
        requests_.push_back(Request{id, clamp_gain(gain)});
    }
    wake_.notify_one();
    return true;
}

bool UiSoundMixer::play_pcm(const int16_t* stereo, size_t frames, float gain) {
    if (!stereo || frames == 0 || frames > kMaxFrames) return false;
    auto pcm = std::make_unique<Pcm>();
    pcm->samples.assign(stereo, stereo + frames * kChannels);
    pcm->frames = frames;

    std::lock_guard<std::mutex> lock(mutex_);
    reap_locked();
    return submit_locked(std::move(pcm), clamp_gain(gain));
}

void UiSoundMixer::wait_decoded() {
    std::unique_lock<std::mutex> lock(mutex_);
    idle_.wait(lock, [this] { return requests_.empty() && !decoding_; });
}

size_t UiSoundMixer::live_sounds() {
    std::lock_guard<std::mutex> lock(mutex_);
    reap_locked();
    return live_;
}

void UiSoundMixer::worker_loop() {
    std::unique_ptr<OpusDecoder, void (*)(OpusDecoder*)> decoder(nullptr, opus_decoder_destroy);
    int err = 0;
    decoder.reset(opus_decoder_create(static_cast<opus_int32>(kSampleRate),
                                      static_cast<int>(kChannels), &err));
    if (!decoder) MELLO_LOG_ERROR("ui_sound", "worker: opus_decoder_create failed (%d)", err);
    std::vector<int16_t> scratch(kOpusMaxPacketFrames * kChannels);

    std::unique_lock<std::mutex> lock(mutex_);
    for (;;) {
        reap_locked();
        if (stop_) return;
        if (requests_.empty()) {
            if (live_ > 0) {
                wake_.wait_for(lock, kReapInterval);
            } else {
                wake_.wait(lock);
            }
            continue;
        }

        const Request req = requests_.front();
        requests_.pop_front();
        const std::shared_ptr<const OggOpus> sound = sounds_[req.id];
        if (live_ >= kMaxLive || !sound || !decoder) {
            MELLO_LOG_WARN("ui_sound", "worker: sound %u dropped (%zu live)", req.id, live_);
            idle_.notify_all();
            continue;
        }

        decoding_ = true;
        lock.unlock();
        auto pcm = std::make_unique<Pcm>();
        const auto t0 = std::chrono::steady_clock::now();
        const bool ok = decode_ogg_opus(*sound, decoder.get(), scratch.data(), pcm->samples);
        const auto us = std::chrono::duration_cast<std::chrono::microseconds>(
                            std::chrono::steady_clock::now() - t0)
                            .count();
        pcm->frames = sound->frames;
        lock.lock();
        decoding_ = false;

        if (!ok) {
            MELLO_LOG_ERROR("ui_sound", "worker: sound %u does not decode", req.id);
        } else if (!submit_locked(std::move(pcm), req.gain)) {
            MELLO_LOG_WARN("ui_sound", "worker: render queue full, sound %u dropped", req.id);
        } else {
            MELLO_LOG_DEBUG("ui_sound", "decoded sound %u in %lld us", req.id,
                            static_cast<long long>(us));
        }
        idle_.notify_all();
    }
}

bool UiSoundMixer::submit_locked(std::unique_ptr<Pcm> pcm, float gain) {
    const uint64_t w = ready_write_.load(std::memory_order_relaxed);
    const uint64_t r = ready_read_.load(std::memory_order_acquire);
    if (w - r >= kQueueSize || live_ >= kMaxLive) return false;
    ready_[w % kQueueSize] = Ready{std::move(pcm), gain};
    ready_write_.store(w + 1, std::memory_order_release);
    ++live_;
    return true;
}

void UiSoundMixer::reap_locked() {
    uint64_t r = done_read_.load(std::memory_order_relaxed);
    const uint64_t w = done_write_.load(std::memory_order_acquire);
    for (; r < w; ++r) {
        done_[r % kDoneSize].reset();
        --live_;
    }
    done_read_.store(r, std::memory_order_release);
}

void UiSoundMixer::retire(std::unique_ptr<Pcm> pcm) {
    // Never full (see kDoneSize), so the slot is empty and nothing is freed here.
    const uint64_t w = done_write_.load(std::memory_order_relaxed);
    done_[w % kDoneSize] = std::move(pcm);
    done_write_.store(w + 1, std::memory_order_release);
}

void UiSoundMixer::take_ready() {
    uint64_t r = ready_read_.load(std::memory_order_relaxed);
    const uint64_t w = ready_write_.load(std::memory_order_acquire);
    for (; r < w; ++r) {
        Ready& ready = ready_[r % kQueueSize];

        // A free voice, else the voice that started first.
        Voice* slot = &voices_[0];
        for (auto& v : voices_) {
            if (!v.pcm) {
                slot = &v;
                break;
            }
            if (v.order < slot->order) slot = &v;
        }
        if (slot->pcm) retire(std::move(slot->pcm));
        slot->pcm = std::move(ready.pcm);
        slot->pos = 0;
        slot->gain = ready.gain;
        slot->order = next_order_++;
    }
    ready_read_.store(r, std::memory_order_release);
}

bool UiSoundMixer::mix_into(int16_t* stereo, int16_t* mono, size_t frames) {
    take_ready();

    bool played = false;
    size_t active = 0;
    for (auto& v : voices_) {
        if (!v.pcm) continue;
        const size_t n = (std::min)(frames, v.pcm->frames - v.pos);
        const int16_t* src = v.pcm->samples.data() + v.pos * kChannels;
        for (size_t i = 0; i < n; ++i) {
            const float l = static_cast<float>(src[2 * i]) * v.gain;
            const float r = static_cast<float>(src[2 * i + 1]) * v.gain;
            stereo[2 * i] = saturate(static_cast<float>(stereo[2 * i]) + l);
            stereo[2 * i + 1] = saturate(static_cast<float>(stereo[2 * i + 1]) + r);
            mono[i] = saturate(static_cast<float>(mono[i]) + 0.5f * (l + r));
        }
        played = played || n > 0;
        v.pos += n;
        if (v.pos >= v.pcm->frames) {
            retire(std::move(v.pcm));
        } else {
            ++active;
        }
    }
    active_.store(active, std::memory_order_relaxed);
    return played;
}

}  // namespace mello::audio
