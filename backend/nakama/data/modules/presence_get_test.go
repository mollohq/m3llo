package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// presence_get gives the presence of the caller, of a crew mate and of a
// friend. The fixture is in crew_membership_test.go.

func presenceOf(t *testing.T, out, userID string) UserPresence {
	t.Helper()
	var a struct {
		Presences map[string]UserPresence `json:"presences"`
	}
	if err := json.Unmarshal([]byte(out), &a); err != nil {
		t.Fatalf("presence answer %q: %v", out, err)
	}
	p, ok := a.Presences[userID]
	if !ok {
		t.Fatalf("no presence of %s in %q", userID, out)
	}
	return p
}

func TestPresenceGet_GivesThePresenceOfACrewMateOnly(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-15")
	live := &UserPresence{UserID: mcOwner, Status: StatusOnline, LastSeen: "2026-11-24T20:00:00Z",
		Activity: &Activity{Type: ActivityInVoice, CrewID: "mc-crew-15", ChannelName: "General"},
		Game:     &GamePresence{GameID: "g1", GameName: "Night Stones"}}
	nk.put(PresenceCollection, mcOwner, mcOwner, live)
	payload := `{"user_ids":["` + mcOwner + `","mc-no-such-user"]}`

	for _, user := range []string{mcMate, mcOwner} {
		out, err := PresenceGetRPC(ctxWithUser(user), testLogger(), nil, nk, payload)
		if err != nil {
			t.Fatal(err)
		}
		if p := presenceOf(t, out, mcOwner); p.Status != StatusOnline || p.Game == nil || p.Activity.CrewID != "mc-crew-15" {
			t.Fatalf("presence of the owner for %s = %+v, want the stored presence", user, p)
		}
	}

	for _, user := range []string{mcOutsider, mcRequester, mcBanned} {
		out, err := PresenceGetRPC(ctxWithUser(user), testLogger(), nil, nk, payload)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "Night Stones") || strings.Contains(out, "mc-crew-15") || strings.Contains(out, "2026-11-24") {
			t.Fatalf("presence_get gave %s the presence of a member: %s", user, out)
		}
		// The answer for a user the caller cannot see is the answer for a
		// user id that does not exist.
		hidden, none := presenceOf(t, out, mcOwner), presenceOf(t, out, "mc-no-such-user")
		hidden.UserID, none.UserID = "", ""
		a, _ := json.Marshal(hidden)
		b, _ := json.Marshal(none)
		if string(a) != string(b) {
			t.Fatalf("for %s: hidden user %s, unknown user %s. The answers must be the same", user, a, b)
		}
	}
}

func TestPresenceGet_GivesTheCallerTheOwnPresence(t *testing.T) {
	nk := memberCheckCrew(t, "mc-crew-16")
	nk.put(PresenceCollection, mcOutsider, mcOutsider, &UserPresence{UserID: mcOutsider, Status: StatusOnline,
		Activity: &Activity{Type: ActivityNone}})
	out, err := PresenceGetRPC(ctxWithUser(mcOutsider), testLogger(), nil, nk, `{"user_ids":["`+mcOutsider+`"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if p := presenceOf(t, out, mcOutsider); p.Status != StatusOnline {
		t.Fatalf("own presence = %+v, want online", p)
	}
}

// FriendsList of the base fake: no user has a friend.
func (f *fakeGuestNk) FriendsList(_ context.Context, _ string, _ int, _ *int, _ string) ([]*api.Friend, string, error) {
	return nil, "", nil
}

// friendsNk adds the friend list of Nakama to the fake.
type friendsNk struct {
	*delNk
	friends map[string][]*api.Friend
}

func (f *friendsNk) FriendsList(_ context.Context, userID string, _ int, state *int, _ string) ([]*api.Friend, string, error) {
	var out []*api.Friend
	for _, fr := range f.friends[userID] {
		if state == nil || int(fr.GetState().GetValue()) == *state {
			out = append(out, fr)
		}
	}
	return out, "", nil
}

func TestPresenceGet_GivesThePresenceOfAFriend(t *testing.T) {
	base := memberCheckCrew(t, "mc-crew-17")
	base.put(PresenceCollection, mcOwner, mcOwner, &UserPresence{UserID: mcOwner, Status: StatusOnline,
		Activity: &Activity{Type: ActivityNone}})
	base.put(PresenceCollection, mcMate, mcMate, &UserPresence{UserID: mcMate, Status: StatusOnline,
		Activity: &Activity{Type: ActivityNone}})
	nk := &friendsNk{delNk: base, friends: map[string][]*api.Friend{mcOutsider: {
		{User: &api.User{Id: mcOwner}, State: wrapperspb.Int32(0)}, // friends, both sides
		{User: &api.User{Id: mcMate}, State: wrapperspb.Int32(1)},  // a request the caller sent
	}}}
	out, err := PresenceGetRPC(ctxWithUser(mcOutsider), testLogger(), nil, nk, `{"user_ids":["`+mcOwner+`","`+mcMate+`"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if p := presenceOf(t, out, mcOwner); p.Status != StatusOnline {
		t.Fatalf("presence of a friend = %+v, want online", p)
	}
	if p := presenceOf(t, out, mcMate); p.Status != StatusOffline {
		t.Fatalf("presence of a user with an open friend request = %+v, want the answer for a hidden user", p)
	}
}
