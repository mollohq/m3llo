package main

import (
	"context"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// The RPCs that give crew data answer a member of the crew only. These tests
// hold the rule for the callers that are no member: an account outside the
// crew, an account with a pending join request, a banned account, and a
// member who left.

const (
	mcOwner     = "mc-owner"
	mcMate      = "mc-mate"
	mcOutsider  = "mc-outsider"
	mcRequester = "mc-requester"
	mcBanned    = "mc-banned"
)

// memberCheckCrew makes a crew with an owner, a member, a pending join
// request and a banned user. The outsider has no row in the crew.
func memberCheckCrew(t *testing.T, crewID string) *delNk {
	t.Helper()
	nk := newDelNk()
	nk.groups[crewID] = &api.Group{Id: crewID, Name: "Night Crew"}
	for _, m := range []struct {
		id, name string
		state    int32
	}{
		{mcOwner, "Owner", groupStateSuperadmin},
		{mcMate, "Mate", groupStateMember},
		{mcRequester, "Requester", groupStateJoinRequest},
		{mcBanned, "Banned", groupStateBanned},
	} {
		nk.addMember(crewID, m.id, m.name)
		nk.members[crewID][len(nk.members[crewID])-1].State = wrapperspb.Int32(m.state)
	}
	nk.users[mcOutsider] = &api.User{Id: mcOutsider, Username: "Outsider", DisplayName: "Outsider"}
	// A chat preview in the buffer: crew state then reads no channel history.
	crewRecentMsgsMu.Lock()
	crewRecentMsgs[crewID] = []*MessagePreview{{MessageID: "m1", UserID: mcMate, Username: "Mate", Preview: "members only text"}}
	crewRecentMsgsMu.Unlock()
	InvalidateCrewState(crewID)
	t.Cleanup(func() {
		crewRecentMsgsMu.Lock()
		delete(crewRecentMsgs, crewID)
		crewRecentMsgsMu.Unlock()
		InvalidateCrewState(crewID)
		for _, u := range []string{mcOwner, mcMate, mcOutsider, mcRequester, mcBanned} {
			CleanupUser(u)
		}
	})
	return nk
}

// wantDenied fails unless err is PERMISSION_DENIED (7).
func wantDenied(t *testing.T, what string, out string, err error) {
	t.Helper()
	if rpcCode(err) != 7 {
		t.Fatalf("%s: got out=%q err=%v, want PERMISSION_DENIED (7)", what, out, err)
	}
}

// ---- isCrewMember ----

func TestIsCrewMember_OnlyTheOwnerAnAdminOrAMember(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-1")
	ctx := context.Background()
	for user, want := range map[string]bool{
		mcOwner: true, mcMate: true, mcRequester: false, mcBanned: false, mcOutsider: false, "": false,
	} {
		if got := isCrewMember(ctx, nk, "mc-crew-1", user); got != want {
			t.Errorf("isCrewMember(%q) = %v, want %v", user, got, want)
		}
	}
	if isCrewMember(ctx, nk, "mc-no-such-crew", mcOwner) {
		t.Error("a crew that does not exist has no member")
	}
}

// pagedMembersNk lists the members 100 at a time, as Nakama does.
type pagedMembersNk struct {
	*delNk
}

func (p *pagedMembersNk) GroupUsersList(_ context.Context, id string, limit int, _ *int, cursor string) ([]*api.GroupUserList_GroupUser, string, error) {
	all := p.members[id]
	from := 0
	if cursor != "" {
		from = int(cursor[0])<<8 | int(cursor[1])
	}
	to := from + limit
	if to >= len(all) {
		return all[from:], "", nil
	}
	return all[from:to], string([]byte{byte(to >> 8), byte(to)}), nil
}

func TestIsCrewMember_ReadsEveryPageOfTheMemberList(t *testing.T) {
	base := newDelNk()
	for i := 0; i < 230; i++ {
		id := "mc-page-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		base.addMember("mc-crew-big", id, id)
		base.members["mc-crew-big"][i].State = wrapperspb.Int32(groupStateMember)
	}
	nk := &pagedMembersNk{base}
	last := base.members["mc-crew-big"][229].GetUser().GetId()
	if !isCrewMember(context.Background(), nk, "mc-crew-big", last) {
		t.Fatal("member 230 of a crew is a member")
	}
	if isCrewMember(context.Background(), nk, "mc-crew-big", mcOutsider) {
		t.Fatal("an outsider is no member of a large crew")
	}
}

// ---- the RPCs behind isCrewMember: a pending join request is refused ----

func TestCrewFeed_RefusesAPendingJoinRequest(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-2")
	out, err := CrewFeedRPC(ctxWithUser(mcRequester), testLogger(), nil, nk, `{"crew_id":"mc-crew-2"}`)
	wantDenied(t, "crew_feed by a join request", out, err)
}

func TestCrewStateGet_RefusesAPendingJoinRequest(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-3")
	out, err := CrewStateGetRPC(ctxWithUser(mcRequester), testLogger(), nil, nk, `{"crew_id":"mc-crew-3"}`)
	wantDenied(t, "crew_state_get by a join request", out, err)
}

func TestStartStream_RefusesAPendingJoinRequest(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-4")
	out, err := StartStreamRPC(ctxWithUser(mcRequester), testLogger(), nil, nk, `{"crew_id":"mc-crew-4","title":"x"}`)
	wantDenied(t, "start_stream by a join request", out, err)
}

func TestCreateInviteCode_RefusesAPendingJoinRequest(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-5")
	for _, user := range []string{mcRequester, mcBanned, mcOutsider} {
		out, err := CreateInviteCodeRPC(ctxWithUser(user), testLogger(), nil, nk, `{"crew_id":"mc-crew-5"}`)
		wantDenied(t, "create_invite_code by "+user, out, err)
	}
	out, err := CreateInviteCodeRPC(ctxWithUser(mcMate), testLogger(), nil, nk, `{"crew_id":"mc-crew-5"}`)
	if err != nil || !strings.Contains(out, `"code"`) {
		t.Fatalf("create_invite_code by a member: out=%q err=%v", out, err)
	}
}

// ---- chat_react ----

func TestChatReact_PushesTheReactionToMembersOnly(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-18")
	feedNotesMu.Lock()
	feedNotes = nil
	feedNotesMu.Unlock()
	if _, err := ChatReactRPC(ctxWithUser(mcMate), testLogger(), nil, nk,
		`{"crew_id":"mc-crew-18","message_id":"msg-1","emoji":"👍","op":"add"}`); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	feedNotesMu.Lock()
	for _, n := range feedNotes {
		if n.code == NotifyChatReaction {
			got[n.userID] = true
		}
	}
	feedNotesMu.Unlock()
	if !got[mcOwner] || !got[mcMate] {
		t.Fatalf("the members did not get the reaction: %v", got)
	}
	if got[mcRequester] || got[mcBanned] {
		t.Fatalf("a user who is no member got the reaction: %v", got)
	}
}
