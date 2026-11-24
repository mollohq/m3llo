package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
)

// The crew state, catch-up and subscription RPCs answer a member of the crew
// only. The fixture is in crew_membership_test.go.

// storedFor reports whether the user owns an object under collection/key.
func storedFor(nk *delNk, collection, key, userID string) bool {
	nk.mu.Lock()
	defer nk.mu.Unlock()
	_, ok := nk.storage[fakeStorageKey(collection, key, userID)]
	return ok
}

func subscribedTo(crewID, userID string) bool {
	for _, sub := range getSubscribersForCrew(crewID) {
		if sub.UserID == userID {
			return true
		}
	}
	return false
}

// ---- crew_catchup ----

func TestCrewCatchup_AnswersAMemberOnly(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-6")
	if err := AppendCrewEvent(context.Background(), nk, "mc-crew-6", CrewEvent{
		ID: "ev1", CrewID: "mc-crew-6", Type: "moment", ActorID: mcOwner, Timestamp: time.Now().Add(-time.Hour).UnixMilli(), Score: 40,
		Data: map[string]interface{}{"text": "members only moment", "sentiment": "win"},
	}); err != nil {
		t.Fatal(err)
	}
	payload := `{"crew_id":"mc-crew-6","last_seen":1}`

	out, err := CrewCatchupRPC(ctxWithUser(mcMate), testLogger(), nil, nk, payload)
	if err != nil || !strings.Contains(out, "members only moment") {
		t.Fatalf("crew_catchup by a member: out=%q err=%v", out, err)
	}
	for _, user := range []string{mcOutsider, mcRequester, mcBanned} {
		out, err := CrewCatchupRPC(ctxWithUser(user), testLogger(), nil, nk, payload)
		wantDenied(t, "crew_catchup by "+user, out, err)
	}
}

func TestCrewCatchup_AnswersTheSameForACrewThatDoesNotExist(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-7")
	_, errReal := CrewCatchupRPC(ctxWithUser(mcOutsider), testLogger(), nil, nk, `{"crew_id":"mc-crew-7","last_seen":1}`)
	_, errNone := CrewCatchupRPC(ctxWithUser(mcOutsider), testLogger(), nil, nk, `{"crew_id":"mc-no-such-crew","last_seen":1}`)
	if errReal == nil || errNone == nil || errReal.Error() != errNone.Error() || rpcCode(errReal) != rpcCode(errNone) {
		t.Fatalf("crew that exists: %v. crew that does not: %v. The answers must be the same", errReal, errNone)
	}
}

// ---- crew_state_get_sidebar ----

type sidebarAnswer struct {
	Crews []struct {
		CrewID string `json:"crew_id"`
	} `json:"crews"`
}

func sidebarCrewIDs(t *testing.T, out string) []string {
	t.Helper()
	var a sidebarAnswer
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("sidebar answer %q: %v", out, err)
	}
	ids := []string{}
	for _, c := range a.Crews {
		ids = append(ids, c.CrewID)
	}
	return ids
}

// twoCrews: the mate is a member of the first crew only.
func twoCrews(t *testing.T, mine, other string) *delNk {
	t.Helper()
	nk := memberCheckCrew(t, mine)
	nk.groups[other] = &api.Group{Id: other, Name: "Other Crew"}
	nk.addMember(other, "mc-other-owner", "Other Owner")
	crewRecentMsgsMu.Lock()
	crewRecentMsgs[other] = []*MessagePreview{{MessageID: "m9", UserID: "mc-other-owner", Username: "Other Owner", Preview: "text of the other crew"}}
	crewRecentMsgsMu.Unlock()
	InvalidateCrewState(other)
	t.Cleanup(func() {
		crewRecentMsgsMu.Lock()
		delete(crewRecentMsgs, other)
		crewRecentMsgsMu.Unlock()
		InvalidateCrewState(other)
		CleanupUser("mc-other-owner")
	})
	return nk
}

func TestCrewStateGetSidebar_GivesOnlyTheCrewsOfTheCaller(t *testing.T) {
	nk := twoCrews(t, "mc-crew-8", "mc-crew-8b")
	payload := `{"crew_ids":["mc-crew-8","mc-crew-8b","mc-no-such-crew"]}`

	out, err := CrewStateGetSidebarRPC(ctxWithUser(mcMate), testLogger(), nil, nk, payload)
	if err != nil {
		t.Fatal(err)
	}
	if ids := sidebarCrewIDs(t, out); len(ids) != 1 || ids[0] != "mc-crew-8" {
		t.Fatalf("crews for the member = %v, want only mc-crew-8", ids)
	}
	if strings.Contains(out, "text of the other crew") {
		t.Fatal("the answer holds a chat preview of a crew the caller is not in")
	}

	for _, user := range []string{mcOutsider, mcRequester, mcBanned} {
		out, err := CrewStateGetSidebarRPC(ctxWithUser(user), testLogger(), nil, nk, payload)
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		if ids := sidebarCrewIDs(t, out); len(ids) != 0 {
			t.Fatalf("crews for %s = %v, want none", user, ids)
		}
	}
}

// ---- subscribe_sidebar ----

func TestSubscribeSidebar_SubscribesOnlyToTheCrewsOfTheCaller(t *testing.T) {
	nk := twoCrews(t, "mc-crew-9", "mc-crew-9b")
	payload := `{"crew_ids":["mc-crew-9","mc-crew-9b"]}`

	out, err := SubscribeSidebarRPC(ctxWithUser(mcMate), testLogger(), nil, nk, payload)
	if err != nil {
		t.Fatal(err)
	}
	if ids := sidebarCrewIDs(t, out); len(ids) != 1 || ids[0] != "mc-crew-9" {
		t.Fatalf("crews for the member = %v, want only mc-crew-9", ids)
	}
	if !subscribedTo("mc-crew-9", mcMate) {
		t.Fatal("the member is not subscribed to the own crew")
	}
	if subscribedTo("mc-crew-9b", mcMate) {
		t.Fatal("the caller is subscribed to a crew the caller is not in")
	}

	for _, user := range []string{mcOutsider, mcRequester, mcBanned} {
		out, err := SubscribeSidebarRPC(ctxWithUser(user), testLogger(), nil, nk, payload)
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		if ids := sidebarCrewIDs(t, out); len(ids) != 0 {
			t.Fatalf("crews for %s = %v, want none", user, ids)
		}
		if subscribedTo("mc-crew-9", user) || subscribedTo("mc-crew-9b", user) {
			t.Fatalf("%s is subscribed to the pushes of a crew", user)
		}
	}
}

// ---- set_active_crew ----

func TestSetActiveCrew_AnswersAMemberOnly(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-10")
	payload := `{"crew_id":"mc-crew-10"}`

	out, err := SetActiveCrewRPC(ctxWithUser(mcMate), testLogger(), nil, nk, payload)
	if err != nil || !strings.Contains(out, "members only text") {
		t.Fatalf("set_active_crew by a member: out=%q err=%v", out, err)
	}
	if !subscribedTo("mc-crew-10", mcMate) {
		t.Fatal("the member is not subscribed to the crew")
	}

	for _, user := range []string{mcOutsider, mcRequester, mcBanned} {
		out, err := SetActiveCrewRPC(ctxWithUser(user), testLogger(), nil, nk, payload)
		wantDenied(t, "set_active_crew by "+user, out, err)
		if subscribedTo("mc-crew-10", user) {
			t.Fatalf("%s is subscribed to the pushes of the crew", user)
		}
		if storedFor(nk, CrewLastSeenCollection, "mc-crew-10", user) {
			t.Fatalf("set_active_crew wrote a last_seen record for %s", user)
		}
	}
}

func TestSetActiveCrew_ARefusedCallKeepsTheOpenCrew(t *testing.T) {
	nk := twoCrews(t, "mc-crew-11", "mc-crew-11b")
	if _, err := SetActiveCrewRPC(ctxWithUser(mcMate), testLogger(), nil, nk, `{"crew_id":"mc-crew-11"}`); err != nil {
		t.Fatal(err)
	}
	out, err := SetActiveCrewRPC(ctxWithUser(mcMate), testLogger(), nil, nk, `{"crew_id":"mc-crew-11b"}`)
	wantDenied(t, "set_active_crew for a crew of others", out, err)
	if !subscribedTo("mc-crew-11", mcMate) {
		t.Fatal("the refused call took the member out of the open crew")
	}
}

func TestSetActiveCrew_AnswersTheSameForACrewThatDoesNotExist(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-12")
	_, errReal := SetActiveCrewRPC(ctxWithUser(mcOutsider), testLogger(), nil, nk, `{"crew_id":"mc-crew-12"}`)
	_, errNone := SetActiveCrewRPC(ctxWithUser(mcOutsider), testLogger(), nil, nk, `{"crew_id":"mc-no-such-crew"}`)
	if errReal == nil || errNone == nil || errReal.Error() != errNone.Error() || rpcCode(errReal) != rpcCode(errNone) {
		t.Fatalf("crew that exists: %v. crew that does not: %v. The answers must be the same", errReal, errNone)
	}
}

// ---- the pushes of a crew end with the membership ----

func crewEventCount(userID, crewID string) int {
	feedNotesMu.Lock()
	defer feedNotesMu.Unlock()
	n := 0
	for _, note := range feedNotes {
		if note.userID == userID && note.code == NotifyCrewEvent && note.content["crew_id"] == crewID {
			n++
		}
	}
	return n
}

func TestALeaveEndsThePushesOfTheCrew(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-13")
	ctx := ctxWithUser(mcMate)
	if _, err := SetActiveCrewRPC(ctx, testLogger(), nil, nk, `{"crew_id":"mc-crew-13"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := SubscribeSidebarRPC(ctx, testLogger(), nil, nk, `{"crew_ids":["mc-crew-13"]}`); err != nil {
		t.Fatal(err)
	}
	// The built-in leave of Nakama removes the row, then runs the hook.
	nk.members["mc-crew-13"] = nk.members["mc-crew-13"][:1]
	if err := AfterLeaveCrew(ctx, testLogger(), nil, nk, &api.LeaveGroupRequest{GroupId: "mc-crew-13"}); err != nil {
		t.Fatal(err)
	}
	if subscribedTo("mc-crew-13", mcMate) {
		t.Fatal("a member who left is still subscribed to the crew")
	}
	before := crewEventCount(mcMate, "mc-crew-13")
	PushCrewEvent(context.Background(), testLogger(), nk, "mc-crew-13", "voice_joined", map[string]interface{}{"user_id": mcOwner})
	if got := crewEventCount(mcMate, "mc-crew-13"); got != before {
		t.Fatalf("a member who left got %d crew events after the leave", got-before)
	}
}

func TestAKickEndsThePushesOfTheCrew(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-14")
	if _, err := SetActiveCrewRPC(ctxWithUser(mcMate), testLogger(), nil, nk, `{"crew_id":"mc-crew-14"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := KickCrewMemberRPC(ctxWithUser(mcOwner), testLogger(), nil, nk,
		`{"crew_id":"mc-crew-14","user_id":"`+mcMate+`"}`); err != nil {
		t.Fatal(err)
	}
	if subscribedTo("mc-crew-14", mcMate) {
		t.Fatal("a kicked member is still subscribed to the crew")
	}
}
