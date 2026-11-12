package main

import (
	"errors"
	"regexp"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

// errToken is the message of a Nakama runtime error, "" for another error.
func errToken(err error) string {
	var re *runtime.Error
	if errors.As(err, &re) {
		return re.Message
	}
	return ""
}

var rpcTokenRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func TestEveryRPCErrorCodeIsAnUpperCaseToken(t *testing.T) {
	for _, e := range []*runtime.Error{
		errBadRequest, errNoSession, errInternal, errMediaOff,
		errGiftLocked, errOwnerOnly, errNotAMember, errBannerNotJPEG, errBannerTooLarge,
		errNameNotAllowed, errNameTaken, errImageTypeUnsupported, errEmojiTooLarge,
		errNoFreeEmojiSlot, errEmojiNameInvalid, errEmojiRemoveForbidden, errEmojiNotFound,
		errBannerNotFound,
		errRiotNotConfigured, errRiotIDFormat, errRiotRegionInvalid, errRiotUnreachable,
		errRiotIDNotFound, errRiotRateLimited, errRiotAPIError,
		errSFUUnavailable,
	} {
		if !rpcTokenRe.MatchString(e.Message) {
			t.Errorf("%q is not an upper-case token", e.Message)
		}
	}
}

// expectToken fails when err is not the token with the gRPC code.
func expectToken(t *testing.T, what string, err error, token string, code int) {
	t.Helper()
	if errToken(err) != token || errCode(err) != code {
		t.Fatalf("%s: got %v (code %d), want %s (code %d)", what, err, errCode(err), token, code)
	}
}

func TestCrewBannerErrorsAreTokens(t *testing.T) {
	nk := giftsCrew(t)
	_, err := rpcT(t, CrewBannerUploadURLRPC, nk, "own", `{"crew_id":"gc","mime":"image/png","bytes":10}`)
	expectToken(t, "a PNG banner", err, "BANNER_NOT_JPEG", 3)
	_, err = rpcT(t, CrewBannerUploadURLRPC, nk, "own", `{"crew_id":"gc","mime":"image/jpeg","bytes":999999999}`)
	expectToken(t, "a large banner", err, "BANNER_TOO_LARGE", 3)
	_, err = rpcT(t, CrewBannerUploadURLRPC, nk, "ida", `{"crew_id":"gc","mime":"image/jpeg","bytes":10}`)
	expectToken(t, "a member asks for a slot", err, "OWNER_ONLY", 7)
	_, err = rpcT(t, CrewBannerUploadURLRPC, nk, "own", `{}`)
	expectToken(t, "no crew id", err, "BAD_REQUEST", 3)
	_, err = rpcT(t, SetCrewBannerRPC, nk, "own", `{"crew_id":"gc","kind":"game","game_id":"bad id"}`)
	expectToken(t, "a bad game id", err, "BAD_REQUEST", 3)
	_, err = rpcT(t, ClearCrewBannerRPC, nk, "ida", `{"crew_id":"gc"}`)
	expectToken(t, "a member clears", err, "OWNER_ONLY", 7)
	_, err = rpcT(t, ReportCrewMediaRPC, nk, "max", `{"crew_id":"gc","kind":"banner"}`)
	expectToken(t, "no uploaded banner", err, "BANNER_NOT_FOUND", 5)
}

func TestInviteNameErrorsAreTokens(t *testing.T) {
	nk := giftsCrew(t)
	_, err := rpcT(t, SetInviteNameRPC, nk, "own", `{"crew_id":"gc","name":"brawlbros"}`)
	expectToken(t, "before the step", err, "GIFT_LOCKED", 9)
	markStep(t, nk, "gc", func(q *CrewQuests) { q.Steps.Friend = &QuestFriend{UserID: "ida"} })
	_, err = rpcT(t, SetInviteNameRPC, nk, "ida", `{"crew_id":"gc","name":"brawlbros"}`)
	expectToken(t, "a member sets it", err, "OWNER_ONLY", 7)
	_, err = rpcT(t, SetInviteNameRPC, nk, "own", `{"crew_id":"gc","name":"admin"}`)
	expectToken(t, "a reserved name", err, "NAME_NOT_ALLOWED", 3)
	if _, err := rpcT(t, SetInviteNameRPC, nk, "own", `{"crew_id":"gc","name":"squad"}`); err != nil {
		t.Fatal(err)
	}
	nk.addMember("other", "max", "Max")
	nk.setRole("other", "max", 0) // a crew with no record has the link gift open
	_, err = rpcT(t, SetInviteNameRPC, nk, "max", `{"crew_id":"other","name":"squad"}`)
	expectToken(t, "a name of another crew", err, "NAME_TAKEN", 6)
}

func TestCrewEmojiErrorsAreTokens(t *testing.T) {
	nk := giftsCrew(t)
	nk.addMember("gc", "elin", "Elin")
	_, err := addEmoji(t, nk, "ida", "gg")
	expectToken(t, "before the step", err, "GIFT_LOCKED", 9)
	markStep(t, nk, "gc", func(q *CrewQuests) { q.Steps.Call = &QuestCall{} })
	_, err = rpcT(t, CrewEmojiUploadURLRPC, nk, "ida", `{"crew_id":"gc","mime":"image/bmp","bytes":10}`)
	expectToken(t, "a BMP", err, "IMAGE_TYPE_UNSUPPORTED", 3)
	_, err = rpcT(t, CrewEmojiUploadURLRPC, nk, "ida", `{"crew_id":"gc","mime":"image/png","bytes":999999999}`)
	expectToken(t, "a large emoji", err, "EMOJI_TOO_LARGE", 3)
	_, err = rpcT(t, CrewEmojiUploadURLRPC, nk, "stranger", `{"crew_id":"gc","mime":"image/png","bytes":10}`)
	expectToken(t, "a stranger", err, "NOT_A_MEMBER", 7)
	_, err = rpcT(t, CrewEmojiAddRPC, nk, "ida", `{"crew_id":"gc","name":"x!","key":"crew-emoji/gc/x.png"}`)
	expectToken(t, "a bad name", err, "EMOJI_NAME_INVALID", 3)
	_, err = rpcT(t, CrewEmojiAddRPC, nk, "ida", `{"crew_id":"gc","name":"ok","key":"crew-emoji/other/x.png"}`)
	expectToken(t, "a key of another crew", err, "BAD_REQUEST", 3)
	if _, err := addEmoji(t, nk, "ida", "gg"); err != nil {
		t.Fatal(err)
	}
	_, err = addEmoji(t, nk, "own", "gg")
	expectToken(t, "a second gg", err, "NAME_TAKEN", 6)
	_, err = rpcT(t, CrewEmojiRemoveRPC, nk, "elin", `{"crew_id":"gc","name":"gg"}`)
	expectToken(t, "another member removes", err, "EMOJI_REMOVE_FORBIDDEN", 7)
	_, err = rpcT(t, CrewEmojiRemoveRPC, nk, "own", `{"crew_id":"gc","name":"nope"}`)
	expectToken(t, "no such emoji", err, "EMOJI_NOT_FOUND", 5)
	for _, n := range []string{"a1", "a2", "a3", "a4"} {
		if _, err := addEmoji(t, nk, "ida", n); err != nil {
			t.Fatal(err)
		}
	}
	_, err = addEmoji(t, nk, "ida", "a5")
	expectToken(t, "a sixth emoji", err, "NO_FREE_EMOJI_SLOT", 9)
}

func TestRiotLinkErrorsAreTokens(t *testing.T) {
	t.Setenv("RIOT_API_KEY", "")
	_, err := RiotLinkRPC(ctxWithUser("u1"), testLogger(), nil, nil, `{"riot_id":"Bob#EUW","region":"europe"}`)
	expectToken(t, "no key", err, "RIOT_NOT_CONFIGURED", 9)

	t.Setenv("RIOT_API_KEY", "test-key")
	_, err = RiotLinkRPC(ctxWithUser("u1"), testLogger(), nil, nil, `not json`)
	expectToken(t, "a bad payload", err, "BAD_REQUEST", 3)
	_, err = RiotLinkRPC(ctxWithUser("u1"), testLogger(), nil, nil, `{"riot_id":"Bob","region":"europe"}`)
	expectToken(t, "no tag", err, "RIOT_ID_FORMAT", 3)
	_, err = RiotLinkRPC(ctxWithUser("u1"), testLogger(), nil, nil, `{"riot_id":"Bob#EUW","region":"mars"}`)
	expectToken(t, "a bad region", err, "RIOT_REGION_INVALID", 3)
}
