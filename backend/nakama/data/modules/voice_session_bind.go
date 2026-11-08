package main

// Voice seat to realtime session binding.
//
// A voice seat belongs to a user, but the user can have more than one session
// (desktop and phone). voice_join arrives over HTTP and carries no session, so
// the client names the session that holds the seat with voice_bind_session
// over the realtime socket. When that session ends, OnSessionEnd releases the
// seat even if the user has other live sessions. A seat with no binding (older
// clients, guests) keeps the per-user rule: it goes with the last session.

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/heroiclabs/nakama-common/runtime"
)

type voiceSessionBinding struct {
	SessionID string
	ChannelID string
}

var (
	// Lock order: voiceUserChannelMu before voiceSessionBindsMu.
	voiceSessionBindsMu sync.Mutex
	voiceSessionBinds   = map[string]voiceSessionBinding{} // userID -> binding
)

// bindVoiceSession records that sessionID holds the user's seat in channelID.
// It returns false and records nothing when the user is not seated there.
func bindVoiceSession(userID, sessionID, channelID string) bool {
	voiceUserChannelMu.RLock()
	defer voiceUserChannelMu.RUnlock()
	if voiceUserChannel[userID] != channelID {
		return false
	}
	voiceSessionBindsMu.Lock()
	voiceSessionBinds[userID] = voiceSessionBinding{SessionID: sessionID, ChannelID: channelID}
	voiceSessionBindsMu.Unlock()
	return true
}

// clearVoiceSessionBindingLocked removes the user's binding. The caller holds
// voiceUserChannelMu for writing, so a binding never outlives its seat.
func clearVoiceSessionBindingLocked(userID string) {
	voiceSessionBindsMu.Lock()
	delete(voiceSessionBinds, userID)
	voiceSessionBindsMu.Unlock()
}

// takeVoiceSessionBinding removes and returns the user's binding when it
// belongs to sessionID. A binding of another session stays.
func takeVoiceSessionBinding(userID, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	voiceSessionBindsMu.Lock()
	defer voiceSessionBindsMu.Unlock()
	b, ok := voiceSessionBinds[userID]
	if !ok || b.SessionID != sessionID {
		return "", false
	}
	delete(voiceSessionBinds, userID)
	return b.ChannelID, true
}

// voiceSessionBindingFor returns the user's binding. Tests use it.
func voiceSessionBindingFor(userID string) (voiceSessionBinding, bool) {
	voiceSessionBindsMu.Lock()
	defer voiceSessionBindsMu.Unlock()
	b, ok := voiceSessionBinds[userID]
	return b, ok
}

func resetVoiceSessionBindsForTests() {
	voiceSessionBindsMu.Lock()
	defer voiceSessionBindsMu.Unlock()
	voiceSessionBinds = map[string]voiceSessionBinding{}
}

type voiceBindSessionRequest struct {
	ChannelID string `json:"channel_id"`
}

// VoiceBindSessionRPC binds the caller's voice seat to the calling realtime
// session. Over HTTP there is no session, so the call does nothing. A caller
// who is not seated in the channel gets bound=false.
func VoiceBindSessionRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("authentication required", 16)
	}
	var req voiceBindSessionRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request", 3)
	}
	if req.ChannelID == "" {
		return "", runtime.NewError("channel_id required", 3)
	}
	sessionID, _ := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)
	if sessionID == "" {
		return `{"success":true,"bound":false}`, nil
	}
	if !bindVoiceSession(userID, sessionID, req.ChannelID) {
		logger.Info("voice_bind_session: user=%s not seated in channel=%s; no binding", userID, req.ChannelID)
		return `{"success":true,"bound":false}`, nil
	}
	logger.Info("voice_bind_session: user=%s channel=%s session=%s", userID, req.ChannelID, sessionID)
	return `{"success":true,"bound":true}`, nil
}

// releaseBoundVoiceSeat releases the user's seat when sessionID is the session
// that holds it, and only while the user is still seated in the bound
// channel. It returns true when it released a seat. The caller writes presence.
func releaseBoundVoiceSeat(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, sessionID string) bool {
	channelID, ok := takeVoiceSessionBinding(userID, sessionID)
	if !ok {
		return false
	}
	if !voiceLeaveInternal(ctx, logger, nk, userID, voiceLeaveInternalOpts{skipPresenceWrite: true, onlyChannel: channelID}) {
		return false
	}
	logger.Info("Voice seat released: bound session ended (user=%s channel=%s session=%s)", userID, channelID, sessionID)
	return true
}
