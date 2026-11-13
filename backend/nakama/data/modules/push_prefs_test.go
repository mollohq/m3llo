package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestMessagePushDefaultFollowsTheCrewSize(t *testing.T) {
	if !messagePushOn(nil, "c1", largeCrewMembers) {
		t.Fatalf("a crew of %d has message push on at the start", largeCrewMembers)
	}
	if messagePushOn(nil, "c1", largeCrewMembers+1) {
		t.Fatalf("a crew of %d has message push off at the start", largeCrewMembers+1)
	}
	// A doc with a choice for another crew does not change the default.
	other := &pushPrefsDoc{Crews: map[string]crewPushPrefs{"c2": {Messages: boolPtr(false)}}}
	if !messagePushOn(other, "c1", 5) {
		t.Fatal("a choice for another crew must not apply")
	}
}

func TestAStoredChoiceWinsOverTheDefault(t *testing.T) {
	doc := &pushPrefsDoc{Crews: map[string]crewPushPrefs{
		"small": {Messages: boolPtr(false)},
		"large": {Messages: boolPtr(true)},
		"quiet": {LookingToPlay: boolPtr(false)},
	}}
	if messagePushOn(doc, "small", 5) {
		t.Fatal("a member who turned Messages off in a small crew gets no push")
	}
	if !messagePushOn(doc, "large", 50) {
		t.Fatal("a member who turned Messages on in a large crew gets the push")
	}
	if lookingToPlayPushOn(doc, "quiet") {
		t.Fatal("a member who turned Looking to play off gets no push")
	}
	// The Looking to play switch says nothing about Messages, and the reverse.
	if !messagePushOn(doc, "quiet", 5) || !lookingToPlayPushOn(doc, "small") {
		t.Fatal("one switch must not change the other")
	}
}

func TestLookingToPlayPushIsOnAtTheStart(t *testing.T) {
	if !lookingToPlayPushOn(nil, "c1") {
		t.Fatal("Looking to play is on for a member with no stored choice")
	}
}

func prefsRig(members int) *fakeGuestNk {
	nk := newFakeGuestNk()
	for i := 1; i <= members; i++ {
		nk.addMember("c1", fmt.Sprintf("u%d", i), fmt.Sprintf("user%d", i))
	}
	return nk
}

func decodePrefs(t *testing.T, raw string) pushPrefsResponse {
	t.Helper()
	var out pushPrefsResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

func TestPushPrefsSetStoresOnlyTheChoiceThatWasMade(t *testing.T) {
	nk := prefsRig(3)
	ctx := ctxWithUser("u1")

	raw, err := PushPrefsGetRPC(ctx, testLogger(), nil, nk, `{"crew_id":"c1"}`)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := decodePrefs(t, raw); !got.Messages || !got.LookingToPlay || got.CrewID != "c1" {
		t.Fatalf("defaults of a small crew = %+v, want both on", got)
	}

	raw, err = PushPrefsSetRPC(ctx, testLogger(), nil, nk, `{"crew_id":"c1","messages":false}`)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := decodePrefs(t, raw); got.Messages || !got.LookingToPlay {
		t.Fatalf("after messages=false: %+v", got)
	}

	docs, err := readPushPrefs(ctx, nk, []string{"u1", "u2"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if docs["u2"] != nil {
		t.Fatal("a member who set nothing has no doc")
	}
	c := docs["u1"].Crews["c1"]
	if c.Messages == nil || *c.Messages || c.LookingToPlay != nil {
		t.Fatalf("stored = %+v, want messages=false and no looking_to_play choice", c)
	}
	if perms := nk.perms[fakeStorageKey(PushPrefsCollection, pushPrefsKey, "u1")]; perms != [2]int{0, 0} {
		t.Fatalf("the doc must not be readable or writable by a client, perms=%v", perms)
	}

	// A second set keeps the first choice.
	if _, err := PushPrefsSetRPC(ctx, testLogger(), nil, nk, `{"crew_id":"c1","looking_to_play":false}`); err != nil {
		t.Fatalf("second set: %v", err)
	}
	raw, _ = PushPrefsGetRPC(ctx, testLogger(), nil, nk, `{"crew_id":"c1"}`)
	if got := decodePrefs(t, raw); got.Messages || got.LookingToPlay {
		t.Fatalf("after both off: %+v", got)
	}
}

func TestPushPrefsGetUsesTheLargeCrewDefault(t *testing.T) {
	nk := prefsRig(largeCrewMembers + 1)
	raw, err := PushPrefsGetRPC(ctxWithUser("u1"), testLogger(), nil, nk, `{"crew_id":"c1"}`)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := decodePrefs(t, raw); got.Messages || !got.LookingToPlay {
		t.Fatalf("defaults of a large crew = %+v, want messages off and looking_to_play on", got)
	}
}

func TestPushPrefsRejectBadCalls(t *testing.T) {
	nk := prefsRig(2)
	cases := []struct {
		name, user, payload string
		set                 bool
	}{
		{"no user", "", `{"crew_id":"c1"}`, false},
		{"no crew", "u1", `{}`, false},
		{"bad json", "u1", `nope`, false},
		{"not a member", "stranger", `{"crew_id":"c1"}`, false},
		{"set by a non-member", "stranger", `{"crew_id":"c1","messages":true}`, true},
		{"set with no switch", "u1", `{"crew_id":"c1"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.set {
				_, err = PushPrefsSetRPC(ctxWithUser(c.user), testLogger(), nil, nk, c.payload)
			} else {
				_, err = PushPrefsGetRPC(ctxWithUser(c.user), testLogger(), nil, nk, c.payload)
			}
			if err == nil {
				t.Fatal("want an error")
			}
		})
	}
	if len(nk.storage) != 0 {
		t.Fatalf("a rejected call must store nothing, storage=%v", nk.storage)
	}
}
