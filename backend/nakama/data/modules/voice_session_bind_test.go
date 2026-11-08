package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// sessionEndSetup seats u1 and u2 in crew/ch, gives u1 two live sessions
// (desktop and phone) and an in-voice presence, and makes nk the global module
// that OnSessionEnd uses.
func sessionEndSetup(t *testing.T) *fakeGuestNk {
	t.Helper()
	nk := newFakeGuestNk()
	oldNk := globalNk
	globalNk = nk
	resetSessionCountsForTests()
	resetSessionActivitiesForTests()
	resetVoiceSessionBindsForTests()
	t.Cleanup(func() {
		globalNk = oldNk
		resetSessionCountsForTests()
		resetSessionActivitiesForTests()
		resetVoiceSessionBindsForTests()
		resetVoiceState()
	})

	seatVoice("crew", "ch", map[string]int{"u1": 10, "u2": 10})
	registerSessionStart("u1") // desktop
	registerSessionStart("u1") // phone
	_ = WritePresence(context.Background(), nk, &UserPresence{
		UserID:   "u1",
		Status:   StatusOnline,
		Activity: &Activity{Type: ActivityInVoice, CrewID: "crew", ChannelID: "ch"},
	})
	return nk
}

func sessionCtx(userID, sessionID string) context.Context {
	ctx := context.WithValue(context.Background(), runtime.RUNTIME_CTX_USER_ID, userID)
	return context.WithValue(ctx, runtime.RUNTIME_CTX_SESSION_ID, sessionID)
}

func bindRPC(t *testing.T, ctx context.Context, nk runtime.NakamaModule, channelID string) bool {
	t.Helper()
	out, err := VoiceBindSessionRPC(ctx, testLogger(), nil, nk, `{"channel_id":"`+channelID+`"}`)
	if err != nil {
		t.Fatalf("voice_bind_session: %v", err)
	}
	var resp struct {
		Success bool `json:"success"`
		Bound   bool `json:"bound"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || !resp.Success {
		t.Fatalf("voice_bind_session response %q: %v", out, err)
	}
	return resp.Bound
}

func seatedIn(userID string) string {
	voiceUserChannelMu.RLock()
	defer voiceUserChannelMu.RUnlock()
	return voiceUserChannel[userID]
}

func roomHas(channelID, userID string) bool {
	voiceRoomsMu.RLock()
	defer voiceRoomsMu.RUnlock()
	room, ok := voiceRooms[channelID]
	if !ok {
		return false
	}
	_, ok = room.Members[userID]
	return ok
}

// The desktop that holds the seat quits while the phone stays connected: the
// seat goes, the user stays online without the in-voice activity.
func TestBoundSessionEndReleasesSeatWhileOtherSessionsRemain(t *testing.T) {
	nk := sessionEndSetup(t)
	if !bindRPC(t, sessionCtx("u1", "desktop"), nk, "ch") {
		t.Fatal("a seated caller must be bound")
	}

	OnSessionEnd(sessionCtx("u1", "desktop"), testLogger(), &api.Event{})

	if seatedIn("u1") != "" || roomHas("ch", "u1") {
		t.Fatal("the seat of the bound session must be released")
	}
	if !roomHas("ch", "u2") {
		t.Fatal("other members keep their seats")
	}
	if _, ok := voiceSessionBindingFor("u1"); ok {
		t.Fatal("the binding must go with the seat")
	}
	if sessionCount("u1") != 1 {
		t.Fatalf("session count = %d, want 1", sessionCount("u1"))
	}
	p, err := ReadPresence(context.Background(), nk, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusOnline {
		t.Fatalf("status = %q, want online: the phone is still connected", p.Status)
	}
	if p.Activity == nil || p.Activity.Type != ActivityNone {
		t.Fatalf("activity = %+v, want none", p.Activity)
	}
}

// The phone disconnects while the desktop holds the seat: the seat stays.
func TestUnboundSessionEndKeepsSeat(t *testing.T) {
	nk := sessionEndSetup(t)
	bindRPC(t, sessionCtx("u1", "desktop"), nk, "ch")

	OnSessionEnd(sessionCtx("u1", "phone"), testLogger(), &api.Event{})

	if seatedIn("u1") != "ch" || !roomHas("ch", "u1") {
		t.Fatal("the end of another session must not release the seat")
	}
	if b, ok := voiceSessionBindingFor("u1"); !ok || b.SessionID != "desktop" {
		t.Fatalf("binding = %+v (%v), want the desktop session", b, ok)
	}
	p, _ := ReadPresence(context.Background(), nk, "u1")
	if p.Activity == nil || p.Activity.Type != ActivityInVoice {
		t.Fatalf("activity = %+v, want in_voice", p.Activity)
	}
}

// After a reconnect the client rebinds to its new session. The end event of
// the old session can come later, and it must not release the seat.
func TestRebindThenOldSessionEndKeepsSeat(t *testing.T) {
	nk := sessionEndSetup(t)
	bindRPC(t, sessionCtx("u1", "desktop-old"), nk, "ch")
	bindRPC(t, sessionCtx("u1", "desktop-new"), nk, "ch")

	OnSessionEnd(sessionCtx("u1", "desktop-old"), testLogger(), &api.Event{})

	if seatedIn("u1") != "ch" || !roomHas("ch", "u1") {
		t.Fatal("the old session's end must not release a seat bound to the new session")
	}
	if b, _ := voiceSessionBindingFor("u1"); b.SessionID != "desktop-new" {
		t.Fatalf("binding session = %q, want desktop-new", b.SessionID)
	}
}

// An older client never binds: the seat keeps the per-user rule and stays
// while another session is live.
func TestUnboundSeatKeepsTodaysBehavior(t *testing.T) {
	nk := sessionEndSetup(t)

	OnSessionEnd(sessionCtx("u1", "desktop"), testLogger(), &api.Event{})

	if seatedIn("u1") != "ch" || !roomHas("ch", "u1") {
		t.Fatal("an unbound seat must stay while the user has another session")
	}
	p, _ := ReadPresence(context.Background(), nk, "u1")
	if p.Activity == nil || p.Activity.Type != ActivityInVoice {
		t.Fatalf("activity = %+v, want in_voice", p.Activity)
	}
}

// The last session ends: the seat goes and presence goes offline, bound or not.
func TestBoundSessionEndWithNoSessionsLeftGoesOffline(t *testing.T) {
	nk := sessionEndSetup(t)
	resetSessionCountsForTests()
	registerSessionStart("u1")
	bindRPC(t, sessionCtx("u1", "desktop"), nk, "ch")

	OnSessionEnd(sessionCtx("u1", "desktop"), testLogger(), &api.Event{})

	if seatedIn("u1") != "" || roomHas("ch", "u1") {
		t.Fatal("the seat must be released")
	}
	p, _ := ReadPresence(context.Background(), nk, "u1")
	if p.Status != StatusOffline {
		t.Fatalf("status = %q, want offline", p.Status)
	}
}

func TestBindNeedsTheSocketAndTheSeat(t *testing.T) {
	nk := sessionEndSetup(t)

	// Over HTTP there is no session: harmless, nothing recorded.
	if bindRPC(t, ctxWithUser("u1"), nk, "ch") {
		t.Fatal("an HTTP call must not bind")
	}
	// Not seated in that channel.
	if bindRPC(t, sessionCtx("u1", "desktop"), nk, "other") {
		t.Fatal("a caller not seated in the channel must not be bound")
	}
	if _, ok := voiceSessionBindingFor("u1"); ok {
		t.Fatal("no binding expected")
	}
}

func TestLeaveAndSwitchClearTheBinding(t *testing.T) {
	nk := sessionEndSetup(t)
	ctx := context.Background()

	bindRPC(t, sessionCtx("u1", "desktop"), nk, "ch")
	voiceLeaveInternal(ctx, testLogger(), nk, "u1")
	if _, ok := voiceSessionBindingFor("u1"); ok {
		t.Fatal("a leave must clear the binding")
	}

	bindRPC(t, sessionCtx("u2", "desktop"), nk, "ch")
	_, err := joinVoiceRoom(ctx, testLogger(), nk, voiceJoinParams{
		CrewID: "crew", ChannelID: "ch2", UserID: "u2", Username: "u2", MaxMembers: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := voiceSessionBindingFor("u2"); ok {
		t.Fatal("a channel switch must clear the binding of the old channel")
	}
}

// A bound session that ends after the user moved to another channel (the
// binding of the new seat not yet made) must not release the new seat.
func TestReleaseOnlyTouchesTheBoundChannel(t *testing.T) {
	nk := sessionEndSetup(t)
	voiceSessionBindsMu.Lock()
	voiceSessionBinds["u1"] = voiceSessionBinding{SessionID: "desktop", ChannelID: "elsewhere"}
	voiceSessionBindsMu.Unlock()

	if releaseBoundVoiceSeat(context.Background(), testLogger(), nk, "u1", "desktop") {
		t.Fatal("no seat in the bound channel, so nothing is released")
	}
	if seatedIn("u1") != "ch" || !roomHas("ch", "u1") {
		t.Fatal("a seat in another channel must stay")
	}
}
