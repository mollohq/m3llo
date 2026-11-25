package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// A voice join takes a channel id from the caller. These tests hold the rule:
// the id must be a channel of the crew that the caller joins, and a room
// belongs to the crew that opened it. Two crews: the test crew of
// guest_sessions_test.go, and a second crew that the caller has no part in.

const (
	testOtherCrew = "crew-uuid-2"
	testOtherCode = "WXYZ-2345"
	testChOther   = "ch_other"
	testCarolID   = "member-uuid-carol"
	testUnknownCh = "ch_nowhere"
)

// seedTwoCrews is seedGuestCrew plus the second crew: carol is its only
// member, it has one voice channel, and it takes no web guests.
func seedTwoCrews() *fakeGuestNk {
	nk := seedGuestCrew()
	nk.put(InviteCodeCollection, testOtherCode, SystemUserID, map[string]string{"crew_id": testOtherCrew})
	nk.groups[testOtherCrew] = &api.Group{Id: testOtherCrew, Name: "Day Larks", EdgeCount: 1, Metadata: `{"guest_policy":"off"}`}
	nk.addMember(testOtherCrew, testCarolID, "carol")
	nk.put(VoiceChannelCollection, testOtherCrew, SystemUserID, VoiceChannelList{Channels: []*VoiceChannelDef{
		{ID: testChOther, Name: "General", IsDefault: true},
	}})
	return nk
}

// newChannelCheckTest is two crews, a working SFU and empty voice state. The
// module is fakeQuestsNk: a join that succeeds computes the crew state for
// the sidebar push, and that reads the chat.
func newChannelCheckTest(t *testing.T) *fakeQuestsNk {
	t.Helper()
	resetVoiceState()
	resetGuestState()
	resetLedgerSessions()
	withSFUKey(t, true)
	t.Cleanup(func() {
		resetVoiceState()
		resetGuestState()
		resetLedgerSessions()
	})
	return &fakeQuestsNk{fakeGuestNk: seedTwoCrews(), versions: map[string]int{}}
}

// seatCarolInHerCrew opens the room of the second crew with carol in it.
func seatCarolInHerCrew() {
	seatInVoice(testChOther, testOtherCrew, VoiceMemberState{UserID: testCarolID, Username: "carol", JoinedAt: 100})
}

func roomMemberIDs(channelID string) []string {
	voiceRoomsMu.RLock()
	defer voiceRoomsMu.RUnlock()
	room, ok := voiceRooms[channelID]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(room.Members))
	for id := range room.Members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func roomExists(channelID string) bool {
	voiceRoomsMu.RLock()
	defer voiceRoomsMu.RUnlock()
	_, ok := voiceRooms[channelID]
	return ok
}

func channelCrew(channelID string) string {
	voiceChannelCrewMu.RLock()
	defer voiceChannelCrewMu.RUnlock()
	return voiceChannelCrew[channelID]
}

// assertOtherCrewRoomUntouched checks that the room of the second crew still
// holds carol only and still belongs to that crew.
func assertOtherCrewRoomUntouched(t *testing.T) {
	t.Helper()
	if got := roomMemberIDs(testChOther); len(got) != 1 || got[0] != testCarolID {
		t.Errorf("the room of the other crew holds %v, want carol only", got)
	}
	if got := channelCrew(testChOther); got != testOtherCrew {
		t.Errorf("the room of the other crew maps to crew %q, want %q", got, testOtherCrew)
	}
	voiceRoomsMu.RLock()
	owner := voiceRooms[testChOther].CrewID
	voiceRoomsMu.RUnlock()
	if owner != testOtherCrew {
		t.Errorf("the room of the other crew has owner %q, want %q", owner, testOtherCrew)
	}
}

func assertChannelNotFound(t *testing.T, err error) {
	t.Helper()
	assertRuntimeErrorCode(t, err, 5)
	if got := rpcToken(err); got != "CHANNEL_NOT_FOUND" {
		t.Errorf("error message = %q, want the token CHANNEL_NOT_FOUND", got)
	}
}

func voiceJoinAs(nk runtime.NakamaModule, userID, crewID, channelID string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"crew_id": crewID, "channel_id": channelID})
	return VoiceJoinRPC(ctxWithUser(userID), testLogger(), nil, nk, string(payload))
}

func guestVoiceJoinAs(nk runtime.NakamaModule, userID, code, channelID string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"code": code, "nickname": "visitor", "channel_id": channelID})
	return GuestVoiceJoinRPC(ctxWithUser(userID), testLogger(), nil, nk, string(payload))
}

// sfuTokenClaims reads the claims of an SFU token without a signature check.
func sfuTokenClaims(t *testing.T, token string) SFUTokenClaims {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims SFUTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// ---------------------------------------------------------------------------
// voice_join
// ---------------------------------------------------------------------------

// The base case: a channel of the caller's crew seats the caller.
func TestVoiceJoin_SeatsAMemberInAChannelOfTheCrew(t *testing.T) {
	nk := newChannelCheckTest(t)

	out, err := voiceJoinAs(nk, testAliceID, testGuestCrew, testChLounge)
	if err != nil {
		t.Fatalf("voice_join failed: %v", err)
	}
	var resp struct {
		ChannelID string `json:"channel_id"`
		SFUToken  string `json:"sfu_token"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ChannelID != testChLounge || seatedIn(testAliceID) != testChLounge {
		t.Errorf("channel = %q, seat = %q, want %q", resp.ChannelID, seatedIn(testAliceID), testChLounge)
	}
	// The SFU session of the token is the crew's own.
	claims := sfuTokenClaims(t, resp.SFUToken)
	if claims.CrewID != testGuestCrew || claims.ChannelID != testChLounge ||
		claims.SessionID != "voice:"+testGuestCrew+":"+testChLounge {
		t.Errorf("token crew=%q channel=%q sid=%q", claims.CrewID, claims.ChannelID, claims.SessionID)
	}
}

// An id that is in no channel list opens no room.
func TestVoiceJoin_RefusesAnUnknownChannel(t *testing.T) {
	nk := newChannelCheckTest(t)

	_, err := voiceJoinAs(nk, testAliceID, testGuestCrew, testUnknownCh)

	assertChannelNotFound(t, err)
	if seatedIn(testAliceID) != "" || roomExists(testUnknownCh) {
		t.Errorf("a refused join left state: seat=%q room=%v", seatedIn(testAliceID), roomExists(testUnknownCh))
	}
}

// alice is in the test crew only. The id of a channel of another crew must
// not put her in that crew's room.
func TestVoiceJoin_RefusesAChannelOfAnotherCrew(t *testing.T) {
	nk := newChannelCheckTest(t)
	seatCarolInHerCrew()

	_, err := voiceJoinAs(nk, testAliceID, testGuestCrew, testChOther)

	assertChannelNotFound(t, err)
	if seatedIn(testAliceID) != "" {
		t.Errorf("alice has the seat %q after a refused join", seatedIn(testAliceID))
	}
	assertOtherCrewRoomUntouched(t)
}

// The room of the other crew is empty, so no room exists yet. The join must
// not open one under the caller's crew.
func TestVoiceJoin_RefusesAChannelOfAnotherCrewWithNoRoom(t *testing.T) {
	nk := newChannelCheckTest(t)

	_, err := voiceJoinAs(nk, testAliceID, testGuestCrew, testChOther)

	assertChannelNotFound(t, err)
	if roomExists(testChOther) || channelCrew(testChOther) != "" {
		t.Errorf("a refused join opened the room: exists=%v crew=%q", roomExists(testChOther), channelCrew(testChOther))
	}
}

// A refused switch leaves the caller in the channel they sit in.
func TestVoiceJoin_RefusedSwitchKeepsTheSeat(t *testing.T) {
	nk := newChannelCheckTest(t)
	seatCarolInHerCrew()
	if _, err := voiceJoinAs(nk, testAliceID, testGuestCrew, testChGeneral); err != nil {
		t.Fatalf("voice_join failed: %v", err)
	}

	_, err := voiceJoinAs(nk, testAliceID, testGuestCrew, testChOther)

	assertChannelNotFound(t, err)
	if seatedIn(testAliceID) != testChGeneral || !roomHas(testChGeneral, testAliceID) {
		t.Errorf("alice lost her seat: seat=%q", seatedIn(testAliceID))
	}
	assertOtherCrewRoomUntouched(t)
}

// The client sends its active crew with the channel it last sat in. After a
// crew switch that is a channel of the old crew. dave is in both crews and
// sits in the second crew's channel: a join for the test crew with that id
// must not move the room to the test crew.
func TestVoiceJoin_RefusesTheSeatedChannelUnderAnotherCrew(t *testing.T) {
	nk := newChannelCheckTest(t)
	const dave = "member-uuid-dave"
	nk.addMember(testGuestCrew, dave, "dave")
	nk.addMember(testOtherCrew, dave, "dave")
	seatCarolInHerCrew()
	if _, err := voiceJoinAs(nk, dave, testOtherCrew, testChOther); err != nil {
		t.Fatalf("voice_join failed: %v", err)
	}

	_, err := voiceJoinAs(nk, dave, testGuestCrew, testChOther)

	assertChannelNotFound(t, err)
	if got := channelCrew(testChOther); got != testOtherCrew {
		t.Errorf("the room maps to crew %q, want %q", got, testOtherCrew)
	}
	if seatedIn(dave) != testChOther || !roomHas(testChOther, dave) {
		t.Errorf("dave lost his seat: seat=%q", seatedIn(dave))
	}
}

// A caller who is not in the crew gets the membership error for every channel
// id, so the answer does not say which ids exist.
func TestVoiceJoin_NonMemberGetsTheSameErrorForAnyChannel(t *testing.T) {
	nk := newChannelCheckTest(t)

	for _, ch := range []string{testChOther, testUnknownCh, ""} {
		_, err := voiceJoinAs(nk, testAliceID, testOtherCrew, ch)
		assertRuntimeErrorCode(t, err, 7)
	}
}

// voice_join for a crew id that is no crew must not write a channel list.
func TestVoiceJoin_UnknownCrewWritesNoChannelList(t *testing.T) {
	nk := newChannelCheckTest(t)
	const noCrew = "crew-uuid-gone"

	_, err := voiceJoinAs(nk, testAliceID, noCrew, "")

	assertRuntimeErrorCode(t, err, 7)
	nk.mu.Lock()
	_, written := nk.storage[fakeStorageKey(VoiceChannelCollection, noCrew, SystemUserID)]
	nk.mu.Unlock()
	if written {
		t.Error("voice_join wrote a channel list for a crew that does not exist")
	}
}

// ---------------------------------------------------------------------------
// guest_voice_join
// ---------------------------------------------------------------------------

// The base case: no channel id seats the guest in the default channel.
func TestGuestVoiceJoin_SeatsAGuestInTheDefaultChannel(t *testing.T) {
	nk := newChannelCheckTest(t)

	out, err := guestVoiceJoinAs(nk, testGuestID, testGuestCode, "")
	if err != nil {
		t.Fatalf("guest_voice_join failed: %v", err)
	}
	var resp struct {
		CrewID    string `json:"crew_id"`
		ChannelID string `json:"channel_id"`
		SFUToken  string `json:"sfu_token"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CrewID != testGuestCrew || resp.ChannelID != testChGeneral || seatedIn(testGuestID) != testChGeneral {
		t.Errorf("crew=%q channel=%q seat=%q", resp.CrewID, resp.ChannelID, seatedIn(testGuestID))
	}
	claims := sfuTokenClaims(t, resp.SFUToken)
	if claims.SessionID != "voice:"+testGuestCrew+":"+testChGeneral {
		t.Errorf("token sid = %q", claims.SessionID)
	}
}

func TestGuestVoiceJoin_RefusesAnUnknownChannel(t *testing.T) {
	nk := newChannelCheckTest(t)

	_, err := guestVoiceJoinAs(nk, testGuestID, testGuestCode, testUnknownCh)

	assertChannelNotFound(t, err)
	if seatedIn(testGuestID) != "" || roomExists(testUnknownCh) || IsGuestUser(testGuestID) {
		t.Errorf("a refused join left state: seat=%q room=%v guest=%v",
			seatedIn(testGuestID), roomExists(testUnknownCh), IsGuestUser(testGuestID))
	}
}

// The second crew takes no web guests. A guest with an invite of the test
// crew must not reach its room with the channel id.
func TestGuestVoiceJoin_RefusesAChannelOfAnotherCrew(t *testing.T) {
	nk := newChannelCheckTest(t)
	seatCarolInHerCrew()

	_, err := guestVoiceJoinAs(nk, testGuestID, testGuestCode, testChOther)

	assertChannelNotFound(t, err)
	if seatedIn(testGuestID) != "" || IsGuestUser(testGuestID) {
		t.Errorf("the guest is seated after a refused join: seat=%q guest=%v", seatedIn(testGuestID), IsGuestUser(testGuestID))
	}
	assertOtherCrewRoomUntouched(t)
	if got := countGuestsInChannel(testChOther, ""); got != 0 {
		t.Errorf("the other crew's room counts %d guests, want 0", got)
	}
	// The roster of that room is not readable either.
	_, err = callGuestVoiceRoster(ctxWithUser(testGuestID), nk.fakeGuestNk)
	assertRuntimeErrorCode(t, err, 7)
}

// An invite code outlives its crew: the purge of a deleted crew cannot list
// the codes (ledger_purge.go). The code must not seat a guest, and it must
// not write a channel list for the deleted crew again.
func TestGuestVoiceJoin_RefusesACodeOfADeletedCrew(t *testing.T) {
	nk := newChannelCheckTest(t)
	const goneCrew, goneCode = "crew-uuid-gone", "GONE-2345"
	nk.put(InviteCodeCollection, goneCode, SystemUserID, map[string]string{"crew_id": goneCrew})

	_, err := guestVoiceJoinAs(nk, testGuestID, goneCode, "")

	assertRuntimeErrorCode(t, err, 5)
	if seatedIn(testGuestID) != "" || IsGuestUser(testGuestID) {
		t.Errorf("a code of a deleted crew seated a guest: seat=%q", seatedIn(testGuestID))
	}
	nk.mu.Lock()
	_, written := nk.storage[fakeStorageKey(VoiceChannelCollection, goneCrew, SystemUserID)]
	nk.mu.Unlock()
	if written {
		t.Error("guest_voice_join wrote a channel list for a deleted crew")
	}
}

// ---------------------------------------------------------------------------
// joinVoiceRoom
// ---------------------------------------------------------------------------

// Rooms are keyed by channel id. A room that another crew opened is not the
// caller's, whatever the caller's channel list says.
func TestJoinVoiceRoom_RefusesARoomOfAnotherCrew(t *testing.T) {
	nk := newChannelCheckTest(t)
	seatCarolInHerCrew()
	seatInVoice(testChGeneral, testGuestCrew, VoiceMemberState{UserID: testAliceID, Username: "alice", JoinedAt: 100})

	_, err := joinVoiceRoom(context.Background(), testLogger(), nk, voiceJoinParams{
		CrewID: testGuestCrew, ChannelID: testChOther, ChannelName: "General",
		UserID: testAliceID, Username: "alice", MaxMembers: MaxVoiceChannelMembers,
	})

	assertChannelNotFound(t, err)
	assertOtherCrewRoomUntouched(t)
	// The refusal comes before the leave: alice keeps her seat.
	if seatedIn(testAliceID) != testChGeneral || !roomHas(testChGeneral, testAliceID) {
		t.Errorf("alice lost her seat: seat=%q", seatedIn(testAliceID))
	}
}

// The re-join path adds nobody, but it wrote the room's crew. A member seated
// in the room must not move it to another crew.
func TestJoinVoiceRoom_RejoinDoesNotMoveTheRoomToAnotherCrew(t *testing.T) {
	nk := newChannelCheckTest(t)
	seatCarolInHerCrew()

	_, err := joinVoiceRoom(context.Background(), testLogger(), nk, voiceJoinParams{
		CrewID: testGuestCrew, ChannelID: testChOther, ChannelName: "General",
		UserID: testCarolID, Username: "carol", MaxMembers: MaxVoiceChannelMembers,
	})

	assertChannelNotFound(t, err)
	assertOtherCrewRoomUntouched(t)
}

// leaveHookNk runs hook once, at the first storage write. A switch writes
// the presence of the leave before it takes the seat in the new room, so the
// hook runs between the two.
type leaveHookNk struct {
	*fakeQuestsNk
	once sync.Once
	hook func()
}

func (h *leaveHookNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	h.once.Do(h.hook)
	return h.fakeQuestsNk.StorageWrite(ctx, writes)
}

// The switch leaves the old room without the room lock. Another crew opens
// the target room in that time: the owner check at the seat must refuse.
func TestJoinVoiceRoom_RefusesARoomAnotherCrewOpensDuringTheLeave(t *testing.T) {
	base := newChannelCheckTest(t)
	seatInVoice(testChGeneral, testGuestCrew, VoiceMemberState{UserID: testAliceID, Username: "alice", JoinedAt: 100})
	nk := &leaveHookNk{fakeQuestsNk: base, hook: func() {
		seatInVoice(testChLounge, testOtherCrew, VoiceMemberState{UserID: testCarolID, Username: "carol", JoinedAt: 100})
	}}

	_, err := joinVoiceRoom(context.Background(), testLogger(), nk, voiceJoinParams{
		CrewID: testGuestCrew, ChannelID: testChLounge, ChannelName: "Lounge",
		UserID: testAliceID, Username: "alice", MaxMembers: MaxVoiceChannelMembers,
	})

	assertChannelNotFound(t, err)
	if got := roomMemberIDs(testChLounge); len(got) != 1 || got[0] != testCarolID {
		t.Errorf("the room holds %v, want carol only", got)
	}
	if got := channelCrew(testChLounge); got != testOtherCrew {
		t.Errorf("the room maps to crew %q, want %q", got, testOtherCrew)
	}
	if seatedIn(testAliceID) != "" {
		t.Errorf("alice has the seat %q in a room of another crew", seatedIn(testAliceID))
	}
}
