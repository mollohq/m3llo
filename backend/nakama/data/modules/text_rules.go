package main

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text rules for names that people type: crew names, voice channel names and
// display names. A name can use any script. A "character" is one Unicode
// scalar value (a Go rune), so every limit counts runes, not bytes.
//
// NFC normalisation is not done here. Go has it only in golang.org/x/text,
// and that module is not a dependency of this plugin. Two names that look
// the same can have different runes (a precomposed "é" and "e" with a
// combining accent).

// ErrNameInvalidUTF8 is the error of cleanName for a value that is not valid
// UTF-8.
var ErrNameInvalidUTF8 = errors.New("name is not valid UTF-8")

// Limits in characters (runes) after cleanName.
const (
	CrewNameMinChars         = 2
	CrewNameMaxChars         = 24
	VoiceChannelNameMinChars = 1
	VoiceChannelNameMaxChars = 24
	DisplayNameMaxChars      = 32
)

// cleanName returns the name as it is stored. It removes the runes that a
// name must not carry and trims Unicode white space at both ends.
//
// Removed: control characters (category Cc), the bidi embedding, override
// and isolate characters (U+202A to U+202E, U+2066 to U+2069), U+200B (zero
// width space) and U+FEFF (byte order mark). Kept: U+200D (zero width
// joiner), U+200C (zero width non-joiner) and the variation selectors,
// because emoji sequences and some scripts need them.
func cleanName(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", ErrNameInvalidUTF8
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if removedFromName(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimFunc(b.String(), unicode.IsSpace), nil
}

// removedFromName reports whether cleanName drops r.
func removedFromName(r rune) bool {
	switch {
	case unicode.IsControl(r): // category Cc
		return true
	case r >= 0x202A && r <= 0x202E: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	case r == 0x200B, r == 0xFEFF:
		return true
	}
	return false
}

// nameLength is the length of a name in characters (runes).
func nameLength(s string) int {
	return utf8.RuneCountInString(s)
}
