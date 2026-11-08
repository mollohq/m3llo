#!/usr/bin/env bash
# Encodes the notification sound masters to the Ogg Opus files that the
# client embeds (specs/features/NOTIFICATION-SOUNDS.md section 2).
#
# The masters are the 48 kHz stereo 16-bit WAV files that shipped before the
# move to Opus. They stay in git history at MASTERS_COMMIT. Pass a folder of
# WAV files with the same names to encode other masters.
#
# Usage: scripts/encode-sounds.sh [masters dir]
# Needs ffmpeg with libopus.
set -euo pipefail

MASTERS_COMMIT=96e8e438
BITRATE=96k
cd "$(git rev-parse --show-toplevel)"
out=mello-core/assets/sounds
names=(you_join you_leave member_join member_leave mute unmute deafen undeafen
       connection_lost crew_live you_live stream_ended message mention clip_saved
       crew_clip new_member)

src=${1:-}
if [[ -z "$src" ]]; then
  src=$(mktemp -d)
  trap 'rm -rf "$src"' EXIT
  for n in "${names[@]}"; do
    git show "$MASTERS_COMMIT:$out/$n.wav" > "$src/$n.wav"
  done
fi

for n in "${names[@]}"; do
  # Bit-exact flags and no metadata, so the same master gives the same file.
  # Constrained VBR: ffmpeg's unconstrained VBR runs about 40 % over the
  # bitrate on these sounds.
  ffmpeg -nostdin -loglevel error -y -i "$src/$n.wav" \
    -map_metadata -1 -fflags +bitexact -flags:a +bitexact \
    -c:a libopus -b:a "$BITRATE" -vbr constrained -compression_level 10 \
    -frame_duration 20 -application audio \
    "$out/$n.opus"
done
ls -l "$out"/*.opus | awk '{s += $5} END {printf "%d files, %d bytes\n", NR, s}'
