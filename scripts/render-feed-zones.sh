#!/bin/sh
# Render the zone feed matrix (client/ui/previews/feed_board_preview.slint) to
# one review page: a PNG for each crew case and an index.html that shows them
# in order. The review gate for every feed change.
#
# Renders never go in the repo. The default output is outside it.
#
# Usage:
#   scripts/render-feed-zones.sh [out-dir]     # default /tmp/mello-feed-zones
set -eu

cd "$(dirname "$0")/.."
OUT="${1:-/tmp/mello-feed-zones}"
FILE=client/ui/previews/feed_board_preview.slint
mkdir -p "$OUT"

command -v slint-viewer >/dev/null 2>&1 || {
    echo "slint-viewer is needed (cargo install slint-viewer, the Slint version of client/Cargo.toml)" >&2
    exit 1
}

# name|caption, in review order.
CASES='Day0|1 · A crew of one, day 0
Small|2 · Three members, a little activity
Big|3 · Forty members, a busy evening (tall)
Monday|4 · Monday, the sealed recap
Quiet|5 · A quiet week
Dormant|6 · Dormant
HistoryPlus|7 · Crew history with m3llo+ (tall)
HistorySessions|7b · Crew history with m3llo+: game sessions, and a quiet week (tall)
HistorySessionsLocked|7c · Crew history without m3llo+: game sessions counted (tall)
QuietTwo|8 · A quiet crew of two: the first five and voice are two cards
JoinCall|9 · Three members, a join and a call (tall)
Quests1|10 · Quests: a crew of one, minute one, the banner ready
Quests2|11 · Quests: the owner, a friend joined, the invite link ready
Quests3|12 · Quests: a member, the crew emoji waiting for the owner
Quests4|13 · Quests: the owner at 4 / 5, game activity off
GapWeek|14 · Sessions and belts, no stream, no clip: the sessions beside the board (tall)
GapWeekOpen|15 · The same week, the belts open (tall)
GapWeek30|16 · A 30 day window: many sessions, no older week (tall)
GapClip|17 · A clip on the stage, no stream (tall)
GapBoth|18 · A clip on the stage and more clips (tall)
GapNothing|19 · No stream, no clip, no session (tall)
GapFifty|20 · A crew of 50: joins and a call (tall)
GapClips30|21 · A 30 day window with clips and older weeks (tall)
GapQuestsCall|22 · A new crew in its first call: the quest card and the people
GapNarrow|23 · The narrowest window, the feed 444 px (tall)'

{
    echo '<!doctype html><meta charset="utf-8"><title>Zone feed matrix</title>'
    echo '<style>body{background:#0b0b0c;color:#a2a2a8;font:14px system-ui;margin:24px}h2{color:#f3f3f4;font-weight:600;font-size:15px;margin:28px 0 6px}img{display:block;width:865px;border:1px solid #2f2f35}</style>'
    echo '<h1 style="color:#f3f3f4;font-size:20px">Zone feed matrix</h1>'
} >"$OUT/index.html"

echo "$CASES" | while IFS='|' read -r name caption; do
    slint-viewer --component "PreviewFeed$name" --screenshot "$OUT/$name.png" "$FILE"
    echo "<h2>$caption</h2><img src=\"$name.png\">" >>"$OUT/index.html"
    echo "rendered $name"
done

echo "open $OUT/index.html"
