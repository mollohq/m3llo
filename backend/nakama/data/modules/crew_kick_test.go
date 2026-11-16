package main

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func kickFixture(t *testing.T) *delNk {
	t.Helper()
	nk := newDelNk()
	nk.groups["crew"] = &api.Group{Id: "crew"}
	nk.addMember("crew", "owner", "Owner")
	nk.addMember("crew", "u2", "Kim")
	nk.members["crew"][0].State = wrapperspb.Int32(0)
	nk.members["crew"][1].State = wrapperspb.Int32(2)
	return nk
}

// A kick writes the member_left event that a leave writes
// (plans/DURABLE-LEDGER.md section 7). The earlier events of the member stay.
func TestKickWritesMemberLeft(t *testing.T) {
	nk := kickFixture(t)
	ctx := ctxWithUser("owner")
	recordMemberJoined(ctx, testLogger(), nk, "crew", "u2", "")
	if _, err := KickCrewMemberRPC(ctx, testLogger(), nil, nk, `{"crew_id":"crew","user_id":"u2"}`); err != nil {
		t.Fatal(err)
	}
	ledger, _ := readLedger(context.Background(), nk, "crew")
	var types []string
	for _, e := range ledger.Events {
		if e.ActorID == "u2" {
			types = append(types, e.Type)
		}
	}
	if len(types) != 2 || types[0] != "member_joined" || types[1] != "member_left" {
		t.Fatalf("events of the kicked member = %v, want member_joined and member_left", types)
	}
	left, ok := decodeCardData[MemberLeftData](ledger.Events[len(ledger.Events)-1].Data)
	if !ok || left.DisplayName != "Kim" {
		t.Fatalf("member_left data = %+v", left)
	}
}

// A kick through the group API of Nakama writes the event too.
func TestKickHookWritesMemberLeft(t *testing.T) {
	nk := kickFixture(t)
	in := &api.KickGroupUsersRequest{GroupId: "crew", UserIds: []string{"u2"}}
	if err := AfterKickCrewMembers(ctxWithUser("owner"), testLogger(), nil, nk, in); err != nil {
		t.Fatal(err)
	}
	ledger, _ := readLedger(context.Background(), nk, "crew")
	if len(ledger.Events) != 1 || ledger.Events[0].Type != "member_left" || ledger.Events[0].ActorID != "u2" {
		t.Fatalf("ledger = %+v", ledger.Events)
	}
}
