// Notification sounds (specs/features/NOTIFICATION-SOUNDS.md): the Ogg Opus
// parser and decoder, the mixer that the render thread runs, its worker, and
// the sound in the pipeline's speaker output.

#include <gtest/gtest.h>

#include "audio/audio_pipeline.hpp"
#include "audio/ogg_opus.hpp"
#include "audio/ui_sound_mixer.hpp"

#include <array>
#include <chrono>
#include <cstdlib>
#include <fstream>
#include <iterator>
#include <string>
#include <thread>
#include <vector>

#ifdef _WIN32
#include <windows.h>
#endif

using mello::audio::AudioPipeline;
using mello::audio::OggOpus;
using mello::audio::UiSoundMixer;

namespace {

constexpr size_t k10ms = 480;

// The 17 sounds that the client embeds, and the frames of each WAV master.
const std::array<std::pair<const char*, size_t>, 17> kSounds = {{
    {"you_join", 36000},       {"you_leave", 36000},     {"member_join", 25296},
    {"member_leave", 25296},   {"mute", 20640},          {"unmute", 21600},
    {"deafen", 29280},         {"undeafen", 30720},      {"connection_lost", 31200},
    {"crew_live", 70560},      {"you_live", 68160},      {"stream_ended", 29280},
    {"message", 22560},        {"mention", 31200},       {"clip_saved", 61440},
    {"crew_clip", 84480},      {"new_member", 130731},
}};

std::vector<uint8_t> read_sound(const std::string& name) {
    std::ifstream f(std::string(MELLO_SOUNDS_DIR) + "/" + name + ".opus", std::ios::binary);
    return {std::istreambuf_iterator<char>(f), std::istreambuf_iterator<char>()};
}

std::vector<int16_t> constant_stereo(size_t frames, int16_t left, int16_t right) {
    std::vector<int16_t> pcm(frames * 2);
    for (size_t i = 0; i < frames; ++i) {
        pcm[2 * i] = left;
        pcm[2 * i + 1] = right;
    }
    return pcm;
}

struct Frame10ms {
    std::array<int16_t, k10ms * 2> stereo{};
    std::array<int16_t, k10ms> mono{};
};

struct Decoder {
    OpusDecoder* dec = nullptr;
    std::vector<int16_t> scratch =
        std::vector<int16_t>(mello::audio::kOpusMaxPacketFrames * 2);
    Decoder() {
        int err = 0;
        dec = opus_decoder_create(48000, 2, &err);
    }
    ~Decoder() { opus_decoder_destroy(dec); }
};

}  // namespace

// ---- Ogg Opus -------------------------------------------------------------

TEST(OggOpus, EveryAssetDecodesToItsMasterLength) {
    Decoder d;
    for (const auto& [name, frames] : kSounds) {
        const auto file = read_sound(name);
        ASSERT_FALSE(file.empty()) << name;
        const auto sound = mello::audio::parse_ogg_opus(file.data(), file.size());
        ASSERT_TRUE(sound) << name;
        EXPECT_EQ(sound->frames, frames) << name;

        std::vector<int16_t> pcm;
        ASSERT_TRUE(mello::audio::decode_ogg_opus(*sound, d.dec, d.scratch.data(), pcm)) << name;
        ASSERT_EQ(pcm.size(), frames * 2) << name;
        int peak = 0;
        for (const int16_t s : pcm) peak = (std::max)(peak, std::abs(static_cast<int>(s)));
        EXPECT_GT(peak, 1000) << name << " is silent";
    }
}

TEST(OggOpus, ADecodeAfterAnotherGivesTheSamePcm) {
    Decoder d;
    const auto mute = read_sound("mute");
    const auto other = read_sound("new_member");
    const auto a = mello::audio::parse_ogg_opus(mute.data(), mute.size());
    const auto b = mello::audio::parse_ogg_opus(other.data(), other.size());
    ASSERT_TRUE(a && b);
    std::vector<int16_t> first, second, between;
    ASSERT_TRUE(mello::audio::decode_ogg_opus(*a, d.dec, d.scratch.data(), first));
    ASSERT_TRUE(mello::audio::decode_ogg_opus(*b, d.dec, d.scratch.data(), between));
    ASSERT_TRUE(mello::audio::decode_ogg_opus(*a, d.dec, d.scratch.data(), second));
    EXPECT_EQ(first, second);  // the decoder is reset
}

TEST(OggOpus, RejectsOtherDataAndCutFiles) {
    EXPECT_FALSE(mello::audio::parse_ogg_opus(nullptr, 10));
    const std::vector<uint8_t> junk(200, 0x4f);
    EXPECT_FALSE(mello::audio::parse_ogg_opus(junk.data(), junk.size()));

    const auto file = read_sound("mention");
    ASSERT_TRUE(mello::audio::parse_ogg_opus(file.data(), file.size()));
    // A cut anywhere fails: inside a page, or after a page, before the
    // end-of-stream page.
    for (size_t cut = 0; cut < file.size(); cut += 7) {
        EXPECT_FALSE(mello::audio::parse_ogg_opus(file.data(), cut)) << "cut at " << cut;
    }

    auto bad_head = file;
    bad_head[28] = 'X';  // "OpusHead" in the first page
    EXPECT_FALSE(mello::audio::parse_ogg_opus(bad_head.data(), bad_head.size()));
}

// ---- Render side ------------------------------------------------------------

TEST(UiSoundMixer, PlaysTheSoundOnceAtItsGain) {
    UiSoundMixer mixer;
    const auto pcm = constant_stereo(k10ms + 100, 1000, -400);
    ASSERT_TRUE(mixer.play_pcm(pcm.data(), k10ms + 100, 0.5f));

    Frame10ms f;
    ASSERT_TRUE(mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms));
    EXPECT_EQ(f.stereo[0], 500);
    EXPECT_EQ(f.stereo[1], -200);
    EXPECT_EQ(f.mono[0], 150);  // (500 - 200) / 2
    EXPECT_EQ(mixer.active_voices(), 1u);

    // The last 100 frames, then silence.
    Frame10ms g;
    ASSERT_TRUE(mixer.mix_into(g.stereo.data(), g.mono.data(), k10ms));
    EXPECT_EQ(g.stereo[2 * 99], 500);
    EXPECT_EQ(g.stereo[2 * 100], 0);
    EXPECT_EQ(mixer.active_voices(), 0u);

    Frame10ms h;
    EXPECT_FALSE(mixer.mix_into(h.stereo.data(), h.mono.data(), k10ms));
    EXPECT_EQ(h.stereo[0], 0);
    EXPECT_EQ(mixer.live_sounds(), 0u);  // the played sound is freed
}

TEST(UiSoundMixer, AddsToWhatPlaysAndSaturates) {
    UiSoundMixer mixer;
    const auto pcm = constant_stereo(k10ms, 30000, -30000);
    ASSERT_TRUE(mixer.play_pcm(pcm.data(), k10ms, 1.0f));

    Frame10ms f;
    f.stereo.fill(10000);
    f.mono.fill(100);
    ASSERT_TRUE(mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms));
    EXPECT_EQ(f.stereo[0], 32767);
    EXPECT_EQ(f.stereo[1], -20000);
    EXPECT_EQ(f.mono[0], 100);  // the sound's mono mix is 0
}

TEST(UiSoundMixer, TwoSoundsPlayTogether) {
    UiSoundMixer mixer;
    const auto a = constant_stereo(k10ms, 100, 100);
    const auto b = constant_stereo(k10ms, 20, 20);
    ASSERT_TRUE(mixer.play_pcm(a.data(), k10ms, 1.0f));
    ASSERT_TRUE(mixer.play_pcm(b.data(), k10ms, 1.0f));

    Frame10ms f;
    ASSERT_TRUE(mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms));
    EXPECT_EQ(f.stereo[0], 120);
}

TEST(UiSoundMixer, AFifthSoundReplacesTheFirst) {
    UiSoundMixer mixer;
    // Sound i plays the value 1 << i, so the sum names the sounds that play.
    std::vector<std::vector<int16_t>> pcm;
    for (int i = 0; i < 5; ++i) pcm.push_back(constant_stereo(k10ms * 4, int16_t(1 << i), 0));
    for (int i = 0; i < 4; ++i) ASSERT_TRUE(mixer.play_pcm(pcm[i].data(), k10ms * 4, 1.0f));
    Frame10ms f;
    ASSERT_TRUE(mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms));
    EXPECT_EQ(f.stereo[0], 1 + 2 + 4 + 8);

    ASSERT_TRUE(mixer.play_pcm(pcm[4].data(), k10ms * 4, 1.0f));
    Frame10ms g;
    ASSERT_TRUE(mixer.mix_into(g.stereo.data(), g.mono.data(), k10ms));
    EXPECT_EQ(g.stereo[0], 2 + 4 + 8 + 16);
    EXPECT_EQ(mixer.active_voices(), UiSoundMixer::kMaxVoices);
    EXPECT_EQ(mixer.live_sounds(), UiSoundMixer::kMaxVoices);  // the first is freed
}

TEST(UiSoundMixer, TheRenderQueueHoldsSixteen) {
    UiSoundMixer mixer;
    const auto pcm = constant_stereo(10, 1, 1);
    for (size_t i = 0; i < UiSoundMixer::kQueueSize; ++i) {
        ASSERT_TRUE(mixer.play_pcm(pcm.data(), 10, 1.0f));
    }
    EXPECT_FALSE(mixer.play_pcm(pcm.data(), 10, 1.0f));  // full until a mix takes them

    Frame10ms f;
    mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms);
    EXPECT_TRUE(mixer.play_pcm(pcm.data(), 10, 1.0f));
}

TEST(UiSoundMixer, GainIsClamped) {
    UiSoundMixer mixer;
    const auto pcm = constant_stereo(k10ms, 1000, 1000);
    ASSERT_TRUE(mixer.play_pcm(pcm.data(), k10ms, 5.0f));
    Frame10ms f;
    ASSERT_TRUE(mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms));
    EXPECT_EQ(f.stereo[0], 1000);
}

// ---- Slots and the worker -------------------------------------------------

TEST(UiSoundMixer, RejectsBadLoadsAndPlays) {
    UiSoundMixer mixer;
    const auto file = read_sound("mute");
    const std::vector<uint8_t> junk(64, 1);
    EXPECT_FALSE(mixer.load_opus(UiSoundMixer::kMaxSounds, file.data(), file.size()));
    EXPECT_FALSE(mixer.load_opus(0, nullptr, file.size()));
    EXPECT_FALSE(mixer.load_opus(0, junk.data(), junk.size()));
    EXPECT_FALSE(mixer.loaded(0));
    EXPECT_FALSE(mixer.play(0, 1.0f));  // not loaded
    EXPECT_FALSE(mixer.play(UiSoundMixer::kMaxSounds, 1.0f));
}

TEST(UiSoundMixer, APlayDecodesOnTheWorkerAndFreesThePcmAfter) {
    UiSoundMixer mixer;
    const auto file = read_sound("mute");
    ASSERT_TRUE(mixer.load_opus(4, file.data(), file.size()));
    EXPECT_TRUE(mixer.loaded(4));
    EXPECT_EQ(mixer.live_sounds(), 0u);  // a load decodes nothing

    ASSERT_TRUE(mixer.play(4, 1.0f));
    mixer.wait_decoded();
    EXPECT_EQ(mixer.live_sounds(), 1u);

    // mute is 20640 frames: 43 mixes of 10 ms, then nothing.
    size_t mixes = 0;
    bool sound = false;
    for (;;) {
        Frame10ms f;
        if (!mixer.mix_into(f.stereo.data(), f.mono.data(), k10ms)) break;
        for (const int16_t s : f.stereo) sound = sound || s != 0;
        ++mixes;
    }
    EXPECT_EQ(mixes, 43u);
    EXPECT_TRUE(sound);
    EXPECT_EQ(mixer.live_sounds(), 0u);
}

TEST(UiSoundMixer, AReloadKeepsAPlayThatWaits) {
    UiSoundMixer mixer;
    const auto a = read_sound("mute");
    const auto b = read_sound("new_member");
    ASSERT_TRUE(mixer.load_opus(0, a.data(), a.size()));
    ASSERT_TRUE(mixer.play(0, 1.0f));
    ASSERT_TRUE(mixer.load_opus(0, b.data(), b.size()));
    mixer.wait_decoded();
    EXPECT_EQ(mixer.live_sounds(), 1u);
}

// The pipeline plays a sound outside a call and while deafened, through the
// device-free test backend.
TEST(UiSoundPipeline, PlaysWhileDeafenedOutsideACall) {
#ifdef _WIN32
    SetEnvironmentVariableA("MELLO_AUDIO_BACKEND", "test");
#else
    setenv("MELLO_AUDIO_BACKEND", "test", 1);
#endif
    AudioPipeline pipeline;
    ASSERT_TRUE(pipeline.initialize());
    pipeline.set_deafen(true);

    std::vector<int16_t> out(k10ms);
    ASSERT_EQ(pipeline.render_test_output(out.data(), out.size()), 0);  // silence

    const auto file = read_sound("message");
    ASSERT_TRUE(pipeline.load_ui_sound_opus(5, file.data(), file.size()));
    ASSERT_TRUE(pipeline.play_ui_sound(5, 0.5f));

    // The worker decodes in a few ms; the render then plays the sound.
    bool played = false;
    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(2);
    while (!played && std::chrono::steady_clock::now() < deadline) {
        played = pipeline.render_test_output(out.data(), out.size()) > 0;
        if (!played) std::this_thread::sleep_for(std::chrono::milliseconds(2));
    }
    EXPECT_TRUE(played);
    pipeline.shutdown();
}
