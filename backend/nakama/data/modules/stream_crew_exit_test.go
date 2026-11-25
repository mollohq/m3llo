package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
)

// A stream belongs to a member of the crew. A leave or a kick ends the stream
// that the member hosts, and a host who is no member gets no host token and
// advertises no stream. The crew fixture is in crew_membership_test.go.

// mcHost is the member who hosts the stream.
const mcHost = "mc-stream-host"

// hostStreamCrew makes the crew with the host as a member. The owner mcOwner
// is a subscriber: it gets the pushes of the crew.
func hostStreamCrew(t *testing.T, crewID string) *delNk {
	t.Helper()
	withSFUKey(t, true)
	nk := memberCheckCrew(t, crewID)
	nk.addMemberAs(crewID, mcHost, "Host", groupStateMember)
	openCrew(mcOwner, crewID)
	t.Cleanup(func() { CleanupUser(mcHost) })
	return nk
}

// hostStarts starts a stream of the host in the crew and returns its id.
// advertise is "" or "on_live".
func hostStarts(t *testing.T, nk *delNk, crewID, advertise string) string {
	t.Helper()
	out, err := StartStreamRPC(ctxWithUser(mcHost), testLogger(), nil, nk,
		`{"crew_id":"`+crewID+`","title":"Raid night","advertise":"`+advertise+`"}`)
	if err != nil {
		t.Fatalf("start_stream: %v", err)
	}
	var resp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.SessionID == "" {
		t.Fatalf("start_stream response %q: %v", out, err)
	}
	return resp.SessionID
}

// dropMemberRow removes the row of the user, as Nakama does before it runs
// the after hook of a leave or a kick.
func dropMemberRow(nk *delNk, crewID, userID string) {
	kept := nk.members[crewID][:0:0]
	for _, m := range nk.members[crewID] {
		if m.GetUser().GetId() != userID {
			kept = append(kept, m)
		}
	}
	nk.members[crewID] = kept
}

func streamSessionEvents(nk *delNk, crewID string) int {
	ledger, _ := readLedger(context.Background(), nk, crewID)
	n := 0
	for _, e := range ledger.Events {
		if e.Type == "stream_session" {
			n++
		}
	}
	return n
}

// wantStreamEnded checks everything that the end of the host's stream leaves.
func wantStreamEnded(t *testing.T, nk *delNk, crewID, sessionID string, endedBefore int) {
	t.Helper()
	if storedFor(nk, StreamMetaCollection, crewID, SystemUserID) ||
		storedFor(nk, StreamCollection, crewID, mcHost) ||
		storedFor(nk, StreamSessionCol, sessionID, SystemUserID) {
		t.Fatal("the stream records of a host who is no member are still there")
	}
	if got := crewEventsNamed(mcOwner, crewID, "stream_ended") - endedBefore; got != 1 {
		t.Fatalf("the crew got %d stream_ended events, want 1", got)
	}
	if got := streamSessionEvents(nk, crewID); got != 1 {
		t.Fatalf("the ledger has %d stream_session events, want 1", got)
	}
	if storedFor(nk, StreamResumeCollection, mcHost, SystemUserID) {
		t.Fatal("a host who is no member has a record to advertise the stream again")
	}
	p, err := ReadPresence(context.Background(), nk, mcHost)
	if err != nil {
		t.Fatal(err)
	}
	if p.Activity != nil && p.Activity.Type == ActivityStreaming {
		t.Fatalf("the presence of the host still says streaming: %+v", p.Activity)
	}
	if _, err := RefreshStreamTokenRPC(ctxWithUser(mcHost), testLogger(), nil, nk,
		`{"session_id":"`+sessionID+`"}`); err == nil {
		t.Fatal("a host who is no member got a new token for the stream")
	}
}

func TestALeaveEndsTheStreamOfTheHost(t *testing.T) {
	const crew = "mc-crew-30"
	nk := hostStreamCrew(t, crew)
	sessionID := hostStarts(t, nk, crew, "")
	ended := crewEventsNamed(mcOwner, crew, "stream_ended")

	dropMemberRow(nk, crew, mcHost)
	if err := AfterLeaveCrew(ctxWithUser(mcHost), testLogger(), nil, nk, &api.LeaveGroupRequest{GroupId: crew}); err != nil {
		t.Fatal(err)
	}

	wantStreamEnded(t, nk, crew, sessionID, ended)
}

func TestAKickEndsTheStreamOfTheHost(t *testing.T) {
	const crew = "mc-crew-31"
	nk := hostStreamCrew(t, crew)
	sessionID := hostStarts(t, nk, crew, "")
	ended := crewEventsNamed(mcOwner, crew, "stream_ended")

	if _, err := KickCrewMemberRPC(ctxWithUser(mcOwner), testLogger(), nil, nk,
		`{"crew_id":"`+crew+`","user_id":"`+mcHost+`"}`); err != nil {
		t.Fatal(err)
	}

	wantStreamEnded(t, nk, crew, sessionID, ended)
}

func TestAKickThroughTheGroupAPIEndsTheStreamOfTheHost(t *testing.T) {
	const crew = "mc-crew-32"
	nk := hostStreamCrew(t, crew)
	sessionID := hostStarts(t, nk, crew, "")
	ended := crewEventsNamed(mcOwner, crew, "stream_ended")

	ctx := ctxWithUser(mcOwner)
	in := &api.KickGroupUsersRequest{GroupId: crew, UserIds: []string{mcHost}}
	if _, err := BeforeKickCrewMembers(ctx, testLogger(), nil, nk, in); err != nil {
		t.Fatal(err)
	}
	dropMemberRow(nk, crew, mcHost)
	if err := AfterKickCrewMembers(ctx, testLogger(), nil, nk, in); err != nil {
		t.Fatal(err)
	}

	wantStreamEnded(t, nk, crew, sessionID, ended)
}

// A member who is not the host leaves: the stream of the host stays.
func TestALeaveOfAnotherMemberKeepsTheStream(t *testing.T) {
	const crew = "mc-crew-33"
	nk := hostStreamCrew(t, crew)
	nk.addMemberAs(crew, "mc-third", "Third", groupStateMember)
	sessionID := hostStarts(t, nk, crew, "")
	ended := crewEventsNamed(mcOwner, crew, "stream_ended")

	dropMemberRow(nk, crew, "mc-third")
	if err := AfterLeaveCrew(ctxWithUser("mc-third"), testLogger(), nil, nk, &api.LeaveGroupRequest{GroupId: crew}); err != nil {
		t.Fatal(err)
	}

	if !storedFor(nk, StreamMetaCollection, crew, SystemUserID) || !storedFor(nk, StreamSessionCol, sessionID, SystemUserID) {
		t.Fatal("the leave of another member removed the stream of the host")
	}
	if got := crewEventsNamed(mcOwner, crew, "stream_ended") - ended; got != 0 {
		t.Fatalf("the crew got %d stream_ended events for the leave of another member", got)
	}
}

// The host token is for a member. The row of the host goes with no hook here,
// as it does for a ban through the group API.
func TestRefreshStreamToken_TheHostMustBeAMember(t *testing.T) {
	const crew = "mc-crew-34"
	nk := hostStreamCrew(t, crew)
	sessionID := hostStarts(t, nk, crew, "")
	dropMemberRow(nk, crew, mcHost)

	out, err := RefreshStreamTokenRPC(ctxWithUser(mcHost), testLogger(), nil, nk,
		`{"session_id":"`+sessionID+`"}`)

	if rpcCode(err) != 7 {
		t.Fatalf("refresh_stream_token by a host who is no member: out=%q err=%v, want PERMISSION_DENIED (7)", out, err)
	}
}

// A stream that start_stream recorded for stream_live is advertised only by
// a host who is still a member.
func TestStreamLive_TheHostMustBeAMember(t *testing.T) {
	const crew = "mc-crew-35"
	nk := hostStreamCrew(t, crew)
	sessionID := hostStarts(t, nk, crew, advertiseOnLive)
	started := crewEventsNamed(mcOwner, crew, "stream_started")
	dropMemberRow(nk, crew, mcHost)
	if err := AfterLeaveCrew(ctxWithUser(mcHost), testLogger(), nil, nk, &api.LeaveGroupRequest{GroupId: crew}); err != nil {
		t.Fatal(err)
	}

	out, err := StreamLiveRPC(ctxWithUser(mcHost), testLogger(), nil, nk,
		`{"crew_id":"`+crew+`","session_id":"`+sessionID+`"}`)

	if rpcCode(err) != 7 {
		t.Fatalf("stream_live by a host who is no member: out=%q err=%v, want PERMISSION_DENIED (7)", out, err)
	}
	if storedFor(nk, StreamMetaCollection, crew, SystemUserID) {
		t.Fatal("a host who is no member advertised a stream in the crew")
	}
	if got := crewEventsNamed(mcOwner, crew, "stream_started") - started; got != 0 {
		t.Fatalf("the crew got %d stream_started events from a host who is no member", got)
	}
	if _, err := RefreshStreamTokenRPC(ctxWithUser(mcHost), testLogger(), nil, nk,
		`{"session_id":"`+sessionID+`"}`); rpcCode(err) != 7 {
		t.Fatalf("refresh_stream_token for the stream that was never advertised: err=%v, want PERMISSION_DENIED (7)", err)
	}
}
