package main

// Stable error codes for the RPC errors that the user sees
// (specs/features/LOCALIZATION.md, "Server error codes").
//
// The rule: an RPC error that the client shows is an upper-case token. The
// client maps the token to a message in the UI language. The server log gets
// the detail. A token never changes after a release: an old client knows it.
//
// The gRPC code of each error stays the code that the RPC used before the
// token. errSFUUnavailable (sfu_entitlement.go) follows the same rule.
//
// An error that a correct client never causes (a bad payload, a missing
// session, a storage failure) gets one shared token: BAD_REQUEST or INTERNAL.

import "github.com/heroiclabs/nakama-common/runtime"

// rpcFail writes the detail of a failed RPC to the log and returns the token.
// The client gets the token only.
func rpcFail(logger runtime.Logger, token *runtime.Error, format string, args ...any) error {
	logger.Warn("rpc error %s: "+format, append([]any{token.Message}, args...)...)
	return token
}

// Shared tokens.
var (
	// errBadRequest is a payload that a correct client never sends.
	errBadRequest = runtime.NewError("BAD_REQUEST", 3)
	// errNoSession is an RPC without a user session. Code 16 is UNAUTHENTICATED.
	errNoSession = runtime.NewError("BAD_REQUEST", 16)
	// errInternal is a server failure. The detail is in the log.
	errInternal = runtime.NewError("INTERNAL", 13)
	// errMediaOff is a server without a media bucket. Code 9 is FAILED_PRECONDITION.
	errMediaOff = runtime.NewError("INTERNAL", 9)
)

// Crew gifts (crew_gifts.go).
var (
	errGiftLocked           = runtime.NewError("GIFT_LOCKED", 9)
	errOwnerOnly            = runtime.NewError("OWNER_ONLY", 7)
	errNotAMember           = runtime.NewError("NOT_A_MEMBER", 7)
	errBannerNotJPEG        = runtime.NewError("BANNER_NOT_JPEG", 3)
	errBannerTooLarge       = runtime.NewError("BANNER_TOO_LARGE", 3)
	errNameNotAllowed       = runtime.NewError("NAME_NOT_ALLOWED", 3)
	errNameTaken            = runtime.NewError("NAME_TAKEN", 6)
	errImageTypeUnsupported = runtime.NewError("IMAGE_TYPE_UNSUPPORTED", 3)
	errEmojiTooLarge        = runtime.NewError("EMOJI_TOO_LARGE", 3)
	errNoFreeEmojiSlot      = runtime.NewError("NO_FREE_EMOJI_SLOT", 9)
	errEmojiNameInvalid     = runtime.NewError("EMOJI_NAME_INVALID", 3)
	errEmojiRemoveForbidden = runtime.NewError("EMOJI_REMOVE_FORBIDDEN", 7)
	errEmojiNotFound        = runtime.NewError("EMOJI_NOT_FOUND", 5)
	errBannerNotFound       = runtime.NewError("BANNER_NOT_FOUND", 5)
)

// The Riot link (riot.go).
var (
	errRiotNotConfigured = runtime.NewError("RIOT_NOT_CONFIGURED", 9)
	errRiotIDFormat      = runtime.NewError("RIOT_ID_FORMAT", 3)
	errRiotRegionInvalid = runtime.NewError("RIOT_REGION_INVALID", 3)
	errRiotUnreachable   = runtime.NewError("RIOT_UNREACHABLE", 14)
	errRiotIDNotFound    = runtime.NewError("RIOT_ID_NOT_FOUND", 5)
	errRiotRateLimited   = runtime.NewError("RIOT_RATE_LIMITED", 8)
	errRiotAPIError      = runtime.NewError("RIOT_API_ERROR", 13)
)
