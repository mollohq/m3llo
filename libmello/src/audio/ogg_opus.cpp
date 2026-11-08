#include "ogg_opus.hpp"

#include <algorithm>
#include <cstring>

namespace mello::audio {

namespace {

uint16_t le16(const uint8_t* p) { return static_cast<uint16_t>(p[0] | p[1] << 8); }

uint32_t le32(const uint8_t* p) {
    return static_cast<uint32_t>(p[0]) | static_cast<uint32_t>(p[1]) << 8 |
           static_cast<uint32_t>(p[2]) << 16 | static_cast<uint32_t>(p[3]) << 24;
}

uint64_t le64(const uint8_t* p) {
    return static_cast<uint64_t>(le32(p)) | static_cast<uint64_t>(le32(p + 4)) << 32;
}

constexpr size_t kPageHeader = 27;
constexpr uint8_t kContinued = 0x01;
constexpr uint8_t kEndOfStream = 0x04;
constexpr uint64_t kNoGranule = ~0ull;

}  // namespace

std::optional<OggOpus> parse_ogg_opus(const uint8_t* data, size_t len) {
    if (!data) return std::nullopt;

    OggOpus out;
    bool have_head = false;
    bool have_tags = false;
    bool first_page = true;
    uint32_t serial = 0;
    uint64_t last_granule = kNoGranule;
    uint64_t total_samples = 0;
    std::vector<uint8_t> packet;

    // Takes one whole packet: OpusHead, then OpusTags, then audio.
    auto take = [&](const std::vector<uint8_t>& p) -> bool {
        if (!have_head) {
            if (p.size() < 19 || std::memcmp(p.data(), "OpusHead", 8) != 0) return false;
            const uint8_t version = p[8];
            const uint8_t channels = p[9];
            const uint8_t mapping = p[18];
            if (version >> 4 != 0 || (channels != 1 && channels != 2) || mapping != 0) {
                return false;
            }
            out.pre_skip = le16(&p[10]);
            out.output_gain_q8 = static_cast<int16_t>(le16(&p[16]));
            have_head = true;
            return true;
        }
        if (!have_tags) {
            if (p.size() < 8 || std::memcmp(p.data(), "OpusTags", 8) != 0) return false;
            have_tags = true;
            return true;
        }
        const int n = opus_packet_get_nb_samples(p.data(), static_cast<opus_int32>(p.size()),
                                                 48000);
        if (p.empty() || n <= 0) return false;
        total_samples += static_cast<uint64_t>(n);
        out.packets.insert(out.packets.end(), p.begin(), p.end());
        out.packet_ends.push_back(static_cast<uint32_t>(out.packets.size()));
        return true;
    };

    size_t pos = 0;
    bool ended = false;
    while (!ended && pos < len) {
        if (len - pos < kPageHeader) return std::nullopt;
        const uint8_t* page = data + pos;
        if (std::memcmp(page, "OggS", 4) != 0 || page[4] != 0) return std::nullopt;
        const uint8_t flags = page[5];
        const uint64_t granule = le64(page + 6);
        const uint32_t page_serial = le32(page + 14);
        const size_t segments = page[26];
        if (len - pos - kPageHeader < segments) return std::nullopt;
        const uint8_t* lacing = page + kPageHeader;
        size_t body_len = 0;
        for (size_t i = 0; i < segments; ++i) body_len += lacing[i];
        const size_t body = pos + kPageHeader + segments;
        if (len - body < body_len) return std::nullopt;

        if (first_page) {
            serial = page_serial;
            first_page = false;
        } else if (page_serial != serial) {
            return std::nullopt;
        }
        // A page continues a packet exactly when one is left open.
        if (((flags & kContinued) != 0) != !packet.empty()) return std::nullopt;

        const uint8_t* p = data + body;
        for (size_t i = 0; i < segments; ++i) {
            packet.insert(packet.end(), p, p + lacing[i]);
            p += lacing[i];
            if (lacing[i] < 255) {
                if (!take(packet)) return std::nullopt;
                packet.clear();
            }
        }
        if (granule != kNoGranule) last_granule = granule;
        ended = (flags & kEndOfStream) != 0;
        pos = body + body_len;
    }

    if (!ended || !packet.empty() || !have_tags || out.packet_ends.empty()) return std::nullopt;
    // The last granule position is the stream's length with the pre-skip; it
    // may cut the last packet short, never make it longer.
    if (last_granule == kNoGranule || last_granule <= out.pre_skip ||
        last_granule > total_samples) {
        return std::nullopt;
    }
    out.frames = static_cast<size_t>(last_granule - out.pre_skip);
    return out;
}

bool decode_ogg_opus(const OggOpus& sound, OpusDecoder* decoder, int16_t* scratch,
                     std::vector<int16_t>& out) {
    if (!decoder || !scratch) return false;
    opus_decoder_ctl(decoder, OPUS_RESET_STATE);
    opus_decoder_ctl(decoder, OPUS_SET_GAIN(sound.output_gain_q8));

    out.assign(sound.frames * 2, 0);
    size_t skip = sound.pre_skip;
    size_t written = 0;
    uint32_t start = 0;
    for (const uint32_t end : sound.packet_ends) {
        if (written == sound.frames) break;
        const int n = opus_decode(decoder, sound.packets.data() + start,
                                  static_cast<opus_int32>(end - start), scratch,
                                  static_cast<int>(kOpusMaxPacketFrames), 0);
        start = end;
        if (n < 0) return false;
        const size_t got = static_cast<size_t>(n);
        const size_t from = (std::min)(skip, got);
        skip -= from;
        const size_t take = (std::min)(got - from, sound.frames - written);
        std::copy(scratch + from * 2, scratch + (from + take) * 2, out.data() + written * 2);
        written += take;
    }
    return written == sound.frames;
}

}  // namespace mello::audio
