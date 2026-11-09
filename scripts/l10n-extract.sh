#!/bin/sh
# Extract the @tr() messages of the client UI into
# client/translations/mello-client.pot (specs/features/LOCALIZATION.md).
#
# Usage:
#   ./scripts/l10n-extract.sh           write the .pot
#   ./scripts/l10n-extract.sh --check   exit 1 when the committed .pot is out of date
#
# Needs slint-tr-extractor, the same version as the slint crate:
#   cargo install slint-tr-extractor --version 1.18.1 --locked
#
# The output is stable: the same sources give the same file. The script
# sorts the input files, removes the creation date, and keeps only the
# file name in each reference (no line number), so the .pot changes only
# when a message is added, removed or moved to another file.
#
# `cargo test` has a check that needs no extractor: i18n_lint.rs compares
# the messages of the .pot with the @tr() calls of the sources.
set -eu

cd "$(dirname "$0")/.."

POT=client/translations/mello-client.pot
EXTRACTOR=${SLINT_TR_EXTRACTOR:-slint-tr-extractor}

if ! command -v "$EXTRACTOR" >/dev/null 2>&1; then
    printf 'l10n-extract: %s is not installed.\n' "$EXTRACTOR" >&2
    printf '  cargo install slint-tr-extractor --version 1.18.1 --locked\n' >&2
    exit 2
fi

check=0
case "${1:-}" in
    --check) check=1 ;;
    "") ;;
    *)
        printf 'usage: %s [--check]\n' "$0" >&2
        exit 2
        ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Every .slint file of the client and the HUD, not the previews (mock data
# and test probes). Sorted bytewise, so the order is the same on every OS.
find client/ui hud/ui -name '*.slint' -not -path '*/previews/*' \
    | LC_ALL=C sort > "$tmp/files"

# shellcheck disable=SC2046 # one argument per file; the paths have no spaces
"$EXTRACTOR" --no-default-translation-context \
    --default-domain mello-client \
    --package-name mello-client \
    -o "$tmp/raw.pot" $(cat "$tmp/files")

# The creation date changes on every run, and line numbers change on every
# edit above a message. Remove both; keep each file name once per message.
awk '
    function flush() {
        if (refs != "") print "#:" refs
        refs = ""
        split("", seen)
    }
    { sub(/\r$/, "") }
    /^"POT-Creation-Date:/ { next }
    /^#: / {
        for (i = 2; i <= NF; i++) {
            ref = $i
            sub(/:[0-9]+$/, "", ref)
            if (!(ref in seen)) {
                seen[ref] = 1
                refs = refs " " ref
            }
        }
        next
    }
    { flush(); print }
    END { flush() }
' "$tmp/raw.pot" > "$tmp/mello-client.pot"

if [ "$check" -eq 1 ]; then
    if cmp -s "$tmp/mello-client.pot" "$POT"; then
        printf 'l10n-extract: %s is up to date\n' "$POT"
        exit 0
    fi
    printf 'l10n-extract: %s is out of date. Run ./scripts/l10n-extract.sh and commit it.\n' "$POT" >&2
    diff -u "$POT" "$tmp/mello-client.pot" >&2 || true
    exit 1
fi

mkdir -p "$(dirname "$POT")"
cp "$tmp/mello-client.pot" "$POT"
count=$(grep -c '^msgid ' "$POT")
printf 'l10n-extract: wrote %s (%s messages)\n' "$POT" "$((count - 1))"
