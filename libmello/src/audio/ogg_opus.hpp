#pragma once

// Ogg Opus files (RFC 7845) for the notification sounds
// (specs/features/NOTIFICATION-SOUNDS.md). One logical stream, channel mapping
// family 0 (mono or stereo). The parser keeps the audio packets and the
// stream's length; the decoder turns them into 48 kHz interleaved stereo.
// The CRC of each page is not checked: the files come with the app.

#include <opus.h>

#include <cstddef>
#include <cstdint>
#include <optional>
#include <vector>

namespace mello::audio {

struct OggOpus {
    std::vector<uint8_t> packets;       // the audio packets, back to back
    std::vector<uint32_t> packet_ends;  // end offset of each packet in `packets`
    uint16_t pre_skip = 0;              // samples to drop at the start
    int16_t output_gain_q8 = 0;         // OpusHead output gain, dB in Q7.8
    size_t frames = 0;                  // stereo frames after pre-skip and end trim
};

/// Parses an Ogg Opus file. Returns nullopt for anything else, a file cut
/// before its end-of-stream page, or more than one stream.
std::optional<OggOpus> parse_ogg_opus(const uint8_t* data, size_t len);

/// The most samples per channel in one Opus packet (120 ms at 48 kHz).
constexpr size_t kOpusMaxPacketFrames = 5760;

/// Decodes `sound` into `out` as interleaved stereo, exactly `sound.frames`
/// frames. `decoder` is a 48 kHz stereo decoder; it is reset first.
/// `scratch` holds at least kOpusMaxPacketFrames * 2 samples.
bool decode_ogg_opus(const OggOpus& sound, OpusDecoder* decoder, int16_t* scratch,
                     std::vector<int16_t>& out);

}  // namespace mello::audio
