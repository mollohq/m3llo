#!/bin/sh
# Render each panel preview in English and in the pseudo language, side by
# side, to find text that clips with a longer translation
# (specs/features/LOCALIZATION.md, "The pseudo render").
#
# Usage:
#   ./scripts/l10n-pseudo-shots.sh <out-dir>
#
# Writes to <out-dir>:
#   src-qps/            a copy of client/ui and hud/ui with each @tr() text
#                       in the pseudo language (tools/l10n-pseudo-ui)
#   <file>.<Component>.en.png    the preview from the real tree
#   <file>.<Component>.qps.png   the same preview from the copy
#   rejected.txt        the .slint files that `slint-viewer --check` rejects
#                       alone (not rendered)
#   failed.txt          the previews that did not render
#
# A preview is each exported component whose name starts or ends with
# "Preview", in each .slint file of client/ui and hud/ui.
#
# Needs slint-viewer, the same version as the slint crate:
#   cargo install slint-viewer --version 1.18.1 --locked
set -eu

cd "$(dirname "$0")/.."

VIEWER=${SLINT_VIEWER:-slint-viewer}

if [ $# -ne 1 ]; then
    printf 'usage: %s <out-dir>\n' "$0" >&2
    exit 2
fi
if ! command -v "$VIEWER" >/dev/null 2>&1; then
    printf 'l10n-pseudo-shots: %s is not installed.\n' "$VIEWER" >&2
    printf '  cargo install slint-viewer --version 1.18.1 --locked\n' >&2
    exit 2
fi

out=$1
mkdir -p "$out"
rm -f "$out"/*.png "$out/rejected.txt" "$out/failed.txt"
: > "$out/rejected.txt"
: > "$out/failed.txt"

cargo run --quiet -p l10n-pseudo-ui -- "$out/src-qps" .

find client/ui hud/ui -name '*.slint' | LC_ALL=C sort | while read -r file; do
    if ! diag=$("$VIEWER" --check "$file" 2>&1); then
        # A file with only globals and structs has no component to check.
        case "$diag" in
            *'No component found'*) ;;
            *) printf '%s\n' "$file" >> "$out/rejected.txt" ;;
        esac
        continue
    fi
    stem=$(basename "$file" .slint)
    sed -n 's/^export component \([A-Za-z0-9_]*\).*/\1/p' "$file" \
        | grep -E '^Preview|Preview$' \
        | while read -r comp; do
            for lang in en qps; do
                if [ "$lang" = en ]; then src=$file; else src=$out/src-qps/$file; fi
                png=$out/$stem.$comp.$lang.png
                if ! "$VIEWER" --component "$comp" --screenshot "$png" "$src" >/dev/null 2>&1; then
                    printf '%s %s %s\n' "$file" "$comp" "$lang" >> "$out/failed.txt"
                fi
            done
        done
done

shots=$(find "$out" -maxdepth 1 -name '*.qps.png' | wc -l)
printf 'l10n-pseudo-shots: %s previews in %s\n' "$shots" "$out"
if [ -s "$out/rejected.txt" ]; then
    printf 'Rejected by slint-viewer --check (not rendered):\n' >&2
    sed 's/^/  /' "$out/rejected.txt" >&2
fi
if [ -s "$out/failed.txt" ]; then
    printf 'Did not render:\n' >&2
    sed 's/^/  /' "$out/failed.txt" >&2
    exit 1
fi
