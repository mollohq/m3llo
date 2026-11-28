package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// guest_crew_feed inviter block
// ---------------------------------------------------------------------------

// The values of the test holders doc. No part of them may reach the feed.
const (
	testCarryHours  = 4167.5
	testOwlClock    = "03:14"
	testCollectorN  = 37
	testHoldersMsAt = int64(1790000000000)
)

func resetGuestHolders() {
	guestHolders.mu.Lock()
	guestHolders.crews = map[string]guestHoldersEntry{}
	guestHolders.mu.Unlock()
}

// seedInviterMember makes the inviter of the test code a member of the crew.
// seedGuestCrew alone leaves the inviter outside the crew.
func seedInviterMember(nk *fakeGuestNk) {
	nk.addMember(testGuestCrew, testInviterID, "inviter")
}

// seedHolders stores a holders doc as the head2head recompute writes it, with
// values, times and pairs. The inviter holds night_owl and the_carry, in that
// order in the doc. Alice holds the_collector.
func seedHolders(nk *fakeGuestNk) {
	nk.put(CrewHead2headCollection, head2headKey(testGuestCrew), SystemUserID, Head2headDoc{
		CrewID:  testGuestCrew,
		Members: []string{testInviterID, testAliceID},
		Titles: []Head2headTitle{
			{TitleID: titleNightOwl, HolderUserID: testInviterID, Value: testOwlClock, Unit: "", SinceMs: testHoldersMsAt},
			{TitleID: titleTheCollect, HolderUserID: testAliceID, Value: testCollectorN, Unit: "installed", SinceMs: testHoldersMsAt},
			{
				TitleID: titleTheCarry, HolderUserID: testInviterID, Value: testCarryHours, Unit: "h",
				SinceMs: testHoldersMsAt, PreviousHolderUserID: testAliceID, MovedAtMs: testHoldersMsAt,
			},
		},
		Pairs: []Head2headPair{{
			A: testInviterID, B: testAliceID, ACount: 2, BCount: 1,
			Values: []Head2headPairValue{
				{TitleID: titleNightOwl, AValue: testOwlClock, BValue: "23:40"},
				{TitleID: titleTheCollect, AValue: 12, BValue: testCollectorN},
				{TitleID: titleTheCarry, AValue: testCarryHours, BValue: 80.5},
			},
		}},
		MemberCount: 3,
		OpenSeats:   2,
		UpdatedAt:   testHoldersMsAt,
	})
}

func guestFeedFor(t *testing.T, nk *fakeGuestNk, code string) string {
	t.Helper()
	out, err := GuestCrewFeedRPC(context.Background(), testLogger(), nil, nk, `{"code":"`+code+`"}`)
	if err != nil {
		t.Fatalf("guest_crew_feed failed for %s: %v", code, err)
	}
	return out
}

// assertNoInviter fails when the feed names an inviter in any field.
func assertNoInviter(t *testing.T, what, out string) {
	t.Helper()
	var generic interface{}
	if err := json.Unmarshal([]byte(out), &generic); err != nil {
		t.Fatalf("%s: decode: %v", what, err)
	}
	for _, key := range []string{"inviter", "inviter_display_name"} {
		if path, found := findJSONKey(generic, key, "$"); found {
			t.Errorf("%s: the feed has a %q key at %s: %s", what, key, path, out)
		}
	}
	if strings.Contains(out, "badges") || strings.Contains(out, "The Carry") {
		t.Errorf("%s: the feed has a title of the inviter: %s", what, out)
	}
}

func TestGuestCrewFeed_InviterBlockListsTheTitlesTheInviterHolds(t *testing.T) {
	resetVoiceState()
	nk := seedGuestCrew()
	seedInviterMember(nk)
	seedHolders(nk)

	out := callGuestCrewFeed(t, nk)

	// The exact text of the block: the two titles of the inviter in the display
	// order of the card, not the order of the doc. Alice's title is not in it.
	want := `"inviter":{"display_name":"inviter","badges":[` +
		`{"key":"the_carry","name":"The Carry"},{"key":"night_owl","name":"Night Owl"}]}`
	if !strings.Contains(out, want) {
		t.Errorf("inviter block\n got: %s\nwant: %s", out, want)
	}
	if !strings.Contains(out, `"inviter_display_name":"inviter"`) {
		t.Errorf("inviter_display_name must stay beside the block: %s", out)
	}
	if strings.Contains(out, "the_collector") {
		t.Errorf("a title of another member is in the feed: %s", out)
	}
}

func TestGuestCrewFeed_InviterWithNoTitleHasAnEmptyBadgeList(t *testing.T) {
	const want = `"inviter":{"display_name":"inviter","badges":[]}`

	t.Run("the crew has no holders doc", func(t *testing.T) {
		resetVoiceState()
		nk := seedGuestCrew()
		seedInviterMember(nk)
		if out := callGuestCrewFeed(t, nk); !strings.Contains(out, want) {
			t.Errorf("got %s, want %s", out, want)
		}
		// The public feed never builds the doc.
		if _, has := nk.storage[fakeStorageKey(CrewHead2headCollection, head2headKey(testGuestCrew), SystemUserID)]; has {
			t.Error("guest_crew_feed wrote a holders doc")
		}
	})

	t.Run("other members hold every title", func(t *testing.T) {
		resetVoiceState()
		nk := seedGuestCrew()
		seedInviterMember(nk)
		nk.put(CrewHead2headCollection, head2headKey(testGuestCrew), SystemUserID, Head2headDoc{
			CrewID: testGuestCrew,
			Titles: []Head2headTitle{
				{TitleID: titleTheCarry, HolderUserID: testAliceID, Value: 12.0, Unit: "h"},
				{TitleID: titleClipper, HolderUserID: testBobID, Value: 4, Unit: "clips"},
			},
		})
		if out := callGuestCrewFeed(t, nk); !strings.Contains(out, want) {
			t.Errorf("got %s, want %s", out, want)
		}
	})
}

func TestGuestCrewFeed_LinkWithNoInviterHasNoInviterBlock(t *testing.T) {
	cases := map[string]interface{}{
		// The custom invite name of the crew (crew_gifts.go).
		"NIGHT-OWLS-HQ": inviteCodeObject{CrewID: testGuestCrew, Custom: true},
		// A code from before codes carried the inviter.
		"OLDC-ODE1": map[string]string{"crew_id": testGuestCrew},
	}
	for code, value := range cases {
		resetVoiceState()
		nk := seedGuestCrew()
		seedInviterMember(nk)
		seedHolders(nk)
		nk.put(InviteCodeCollection, code, SystemUserID, value)

		assertNoInviter(t, code, guestFeedFor(t, nk, code))
	}
}

// The holders doc is not recomputed when a member leaves, so it can name the
// inviter as a holder after the inviter left. The feed must not.
func TestGuestCrewFeed_InviterWhoIsNotAMemberIsNotNamed(t *testing.T) {
	cases := map[string]func(nk *fakeGuestNk){
		"left the crew": func(nk *fakeGuestNk) {},
		"join request":  func(nk *fakeGuestNk) { nk.addEdge(testGuestCrew, testInviterID, "inviter", groupStateJoinRequest) },
		"banned":        func(nk *fakeGuestNk) { nk.addEdge(testGuestCrew, testInviterID, "inviter", groupStateBanned) },
	}
	for name, seed := range cases {
		resetVoiceState()
		nk := seedGuestCrew()
		seedHolders(nk)
		seed(nk)

		assertNoInviter(t, name, callGuestCrewFeed(t, nk))
	}
}

func TestGuestCrewFeed_InviterIsNamedInEveryMemberRole(t *testing.T) {
	for _, state := range []int32{groupStateSuperadmin, groupStateAdmin, groupStateMember} {
		resetVoiceState()
		nk := seedGuestCrew()
		nk.addEdge(testGuestCrew, testInviterID, "inviter", state)
		seedHolders(nk)

		if out := callGuestCrewFeed(t, nk); !strings.Contains(out, `"inviter":{"display_name":"inviter","badges":[{"key":"the_carry"`) {
			t.Errorf("state %d: the inviter block is missing: %s", state, out)
		}
	}
}

// The feed reads one page of the member list. An inviter after that page is
// still a member, and a user who left is still not one.
func TestGuestCrewFeed_InviterBeyondTheFirstMemberPage(t *testing.T) {
	t.Run("a member after the page", func(t *testing.T) {
		resetVoiceState()
		nk := seedGuestCrew()
		seedInviterMember(nk) // third in the list
		seedHolders(nk)
		nk.memberPage = 2

		out := callGuestCrewFeed(t, nk)
		if !strings.Contains(out, `"inviter":{"display_name":"inviter","badges":[{"key":"the_carry"`) {
			t.Errorf("the inviter block is missing: %s", out)
		}
	})
	t.Run("not a member, and the page is not the whole list", func(t *testing.T) {
		resetVoiceState()
		nk := seedGuestCrew()
		nk.addMember(testGuestCrew, "member-uuid-carol", "carol")
		seedHolders(nk)
		nk.memberPage = 2

		assertNoInviter(t, "cut list", callGuestCrewFeed(t, nk))
	})
}

// guest_crew_feed is readable by anyone with an invite code. The holders doc
// has a value, a unit and times for each title, and the user ids of the
// holders. Only the key and the name of a title may leave it.
func TestGuestCrewFeed_InviterBlockHasNoValueAndNoID(t *testing.T) {
	resetVoiceState()
	nk := seedGuestCrew()
	seedInviterMember(nk)
	seedHolders(nk)
	out := callGuestCrewFeed(t, nk)

	var resp struct {
		Inviter map[string]json.RawMessage `json:"inviter"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Inviter) != 2 || resp.Inviter["display_name"] == nil || resp.Inviter["badges"] == nil {
		t.Fatalf("the inviter block has the fields display_name and badges only: %s", out)
	}
	var badges []map[string]json.RawMessage
	if err := json.Unmarshal(resp.Inviter["badges"], &badges); err != nil || len(badges) != 2 {
		t.Fatalf("badges = %s (%v), want two entries", resp.Inviter["badges"], err)
	}
	for _, b := range badges {
		if len(b) != 2 || b["key"] == nil || b["name"] == nil {
			t.Errorf("a badge has the fields key and name only, got %v", b)
		}
	}
	for _, s := range []string{testOwlClock, "4167", "23:40", "80.5", "installed", testInviterID, testAliceID} {
		if strings.Contains(out, s) {
			t.Errorf("guest_crew_feed leaked %q from the holders doc: %s", s, out)
		}
	}
}

// The lounge calls guest_crew_feed every few seconds. The holders doc is read
// once for each crew inside guestHoldersTTL.
func TestCrewTitleHolders_ReadsTheDocOncePerTTL(t *testing.T) {
	nk := seedGuestCrew()
	seedHolders(nk)
	ctx := context.Background()
	t0 := time.UnixMilli(testHoldersMsAt)

	first := crewTitleHolders(ctx, nk, testGuestCrew, t0)
	if first[titleTheCarry] != testInviterID || first[titleTheCollect] != testAliceID || len(first) != 3 {
		t.Fatalf("holders = %v", first)
	}
	crewTitleHolders(ctx, nk, testGuestCrew, t0.Add(guestHoldersTTL-time.Second))
	if got := nk.reads[CrewHead2headCollection]; got != 1 {
		t.Fatalf("%d reads inside the TTL, want 1", got)
	}

	// The doc changes hands. The old entry answers until the TTL is over.
	nk.put(CrewHead2headCollection, head2headKey(testGuestCrew), SystemUserID, Head2headDoc{
		CrewID: testGuestCrew,
		Titles: []Head2headTitle{{TitleID: titleTheCarry, HolderUserID: testAliceID, Value: 1.0, Unit: "h"}},
	})
	after := crewTitleHolders(ctx, nk, testGuestCrew, t0.Add(guestHoldersTTL))
	if got := nk.reads[CrewHead2headCollection]; got != 2 {
		t.Fatalf("%d reads after the TTL, want 2", got)
	}
	if after[titleTheCarry] != testAliceID || len(after) != 1 {
		t.Fatalf("holders after the TTL = %v", after)
	}

	// Two feed calls in a row read the doc one time.
	nk2 := seedGuestCrew()
	seedInviterMember(nk2)
	seedHolders(nk2)
	callGuestCrewFeed(t, nk2)
	callGuestCrewFeed(t, nk2)
	if got := nk2.reads[CrewHead2headCollection]; got != 1 {
		t.Fatalf("two feed calls made %d reads of the holders doc, want 1", got)
	}
}

// A failed read gives no badge for that call. It is not kept, so the next
// call reads again.
func TestCrewTitleHolders_AFailedReadIsNotKept(t *testing.T) {
	resetVoiceState()
	nk := seedGuestCrew()
	seedInviterMember(nk)
	seedHolders(nk)

	nk.failReads = CrewHead2headCollection
	if out := callGuestCrewFeed(t, nk); !strings.Contains(out, `"inviter":{"display_name":"inviter","badges":[]}`) {
		t.Errorf("a failed read keeps the inviter with an empty list: %s", out)
	}
	nk.failReads = ""
	if out := callGuestCrewFeed(t, nk); !strings.Contains(out, `{"key":"the_carry","name":"The Carry"}`) {
		t.Errorf("the next call must read the doc again: %s", out)
	}
}

func TestCrewTitleHolders_CacheIsBounded(t *testing.T) {
	resetGuestHolders()
	defer resetGuestHolders()
	nk := newFakeGuestNk()
	ctx := context.Background()
	t0 := time.UnixMilli(testHoldersMsAt)

	guestHolders.mu.Lock()
	for i := 0; i < guestHoldersCacheMax; i++ {
		guestHolders.crews[fmt.Sprintf("crew-%d", i)] =
			guestHoldersEntry{holders: map[string]string{}, readAt: t0.Add(-2 * guestHoldersTTL)}
	}
	n := len(guestHolders.crews)
	guestHolders.mu.Unlock()
	if n != guestHoldersCacheMax {
		t.Fatalf("seeded %d entries, want %d", n, guestHoldersCacheMax)
	}

	crewTitleHolders(ctx, nk, "one-more-crew", t0)
	guestHolders.mu.Lock()
	got := len(guestHolders.crews)
	guestHolders.mu.Unlock()
	if got != 1 {
		t.Errorf("a full cache drops its old entries, got %d entries, want 1", got)
	}
}

func TestInviterBadges(t *testing.T) {
	holders := map[string]string{
		titleWinner:   "u1",
		titleTheCarry: "u1",
		titleMouth:    "u2",
		"new_title":   "u1", // a title this build has no label for
	}
	got := inviterBadges(holders, "u1")
	want := []guestInviterBadge{{Key: "the_carry", Name: "The Carry"}, {Key: "winner", Name: "Winner"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("badges = %+v, want %+v", got, want)
	}
	for what, list := range map[string][]guestInviterBadge{
		"holds no title": inviterBadges(holders, "u3"),
		"no holders":     inviterBadges(nil, "u1"),
		"no user":        inviterBadges(map[string]string{titleMouth: ""}, ""),
	} {
		if list == nil || len(list) != 0 {
			t.Errorf("%s: badges = %#v, want an empty list that is not nil", what, list)
		}
	}
}

// Every title has a label, so no badge is dropped for a missing name.
func TestEveryTitleHasABadgeName(t *testing.T) {
	if len(ownValueOrder) != 13 {
		t.Fatalf("ownValueOrder has %d titles, want 13", len(ownValueOrder))
	}
	for _, id := range ownValueOrder {
		if head2headTitleLabels[id] == "" {
			t.Errorf("title %q has no label", id)
		}
	}
}
