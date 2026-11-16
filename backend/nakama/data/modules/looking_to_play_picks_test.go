package main

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
)

func TestAddPickPutsTheGroupFirstOnceAndKeepsThree(t *testing.T) {
	var groups [][]string
	groups = ltpAddPick(groups, []string{"u3", "u2"})
	groups = ltpAddPick(groups, []string{"u4"})
	groups = ltpAddPick(groups, []string{"u5", "u2", "u5"})
	if want := [][]string{{"u2", "u5"}, {"u4"}, {"u2", "u3"}}; !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups = %v, want %v", groups, want)
	}
	groups = ltpAddPick(groups, []string{"u6"})
	if want := [][]string{{"u6"}, {"u2", "u5"}, {"u4"}}; !reflect.DeepEqual(groups, want) {
		t.Fatalf("a fourth group drops the oldest: %v", groups)
	}
	groups = ltpAddPick(groups, []string{"u5", "u2"})
	if want := [][]string{{"u2", "u5"}, {"u6"}, {"u4"}}; !reflect.DeepEqual(groups, want) {
		t.Fatalf("a repeated group moves to the front: %v", groups)
	}
	if got := ltpAddPick(groups, nil); !reflect.DeepEqual(got, groups) {
		t.Fatalf("an empty group changes nothing: %v", got)
	}
}

func TestFilterPicksDropsFormerMembersAndEmptyGroups(t *testing.T) {
	members := map[string]bool{"u1": true, "u2": true, "u3": true}
	got := ltpFilterPicks([][]string{{"u2", "u9"}, {"u9"}, {"u2"}, {"u3", "u2"}}, members)
	if want := [][]string{{"u2"}, {"u2", "u3"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	if got := ltpFilterPicks(nil, members); got == nil || len(got) != 0 {
		t.Fatalf("no groups is an empty list, not nil: %#v", got)
	}
}

// cancelAndWait closes the open ask of a member and moves the clock past the wait
// between two asks.
func (r *ltpRig) cancelAndWait(user, askID string) {
	r.t.Helper()
	if _, err := r.call(LookingToPlayCancelRPC, user, fmt.Sprintf(`{"ask_id":%q}`, askID)); err != nil {
		r.t.Fatalf("cancel %s: %v", askID, err)
	}
	r.now += ltpAskCooldown.Milliseconds()
}

// storedPicks reads the stored doc of the recent picks of a member in c1.
func (r *ltpRig) storedPicks(user string) ([][]string, bool) {
	r.t.Helper()
	groups, _, err := readLtpPicks(context.Background(), r.nk, user, "c1")
	if err != nil {
		r.t.Fatal(err)
	}
	r.nk.mu.Lock()
	_, ok := r.nk.storage[fakeStorageKey(LookingToPlayPicksCollection, "c1", user)]
	r.nk.mu.Unlock()
	return groups, ok
}

// listRaw calls looking_to_play_list as a member and returns the reply as it
// goes on the wire.
func (r *ltpRig) listRaw(user string) string {
	r.t.Helper()
	raw, err := LookingToPlayListRPC(ctxWithUser(user), testLogger(), nil, r.nk, `{"crew_id":"c1"}`)
	if err != nil {
		r.t.Fatalf("list by %s: %v", user, err)
	}
	return raw
}

func TestAnAskToPickedMembersStoresTheRecentPicks(t *testing.T) {
	r := newLtpRig(t, 6)
	steps := []struct {
		picked string
		want   [][]string
	}{
		{`["u3","u2"]`, [][]string{{"u2", "u3"}}},
		{`["u4"]`, [][]string{{"u4"}, {"u2", "u3"}}},
		{`["u5","u2"]`, [][]string{{"u2", "u5"}, {"u4"}, {"u2", "u3"}}},
		{`["u6"]`, [][]string{{"u6"}, {"u2", "u5"}, {"u4"}}},
		{`["u2","u5"]`, [][]string{{"u2", "u5"}, {"u6"}, {"u4"}}},
	}
	for i, s := range steps {
		out, err := r.call(LookingToPlayAskRPC, "u1", `{"crew_id":"c1","member_ids":`+s.picked+`}`)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !reflect.DeepEqual(out.RecentPicks, s.want) {
			t.Fatalf("step %d: reply recent_picks = %v, want %v", i, out.RecentPicks, s.want)
		}
		if got, _ := r.storedPicks("u1"); !reflect.DeepEqual(got, s.want) {
			t.Fatalf("step %d: stored = %v, want %v", i, got, s.want)
		}
		r.cancelAndWait("u1", out.Ask.AskID)
	}
	// The doc is owned by the asker, and no client reads or writes it.
	if perms := r.nk.perms[fakeStorageKey(LookingToPlayPicksCollection, "c1", "u1")]; perms != [2]int{0, 0} {
		t.Fatalf("permissions = %v, want read 0 and write 0", perms)
	}
	if !strings.Contains(r.listRaw("u1"), `"recent_picks":[["u2","u5"],["u6"],["u4"]]`) {
		t.Fatalf("list = %s", r.listRaw("u1"))
	}
}

func TestAnAskToEveryoneAndAJoinedAskStoreNoPicks(t *testing.T) {
	r := newLtpRig(t, 4)
	game := `{"crew_id":"c1","game":{"game_id":"steam-730","name":"Counter-Strike 2"}}`
	raw, err := LookingToPlayAskRPC(ctxWithUser("u1"), testLogger(), nil, r.nk, game)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "recent_picks") {
		t.Fatalf("an ask to everyone has no recent_picks: %s", raw)
	}
	out, err := r.call(LookingToPlayAskRPC, "u2", game)
	if err != nil || !out.Joined {
		t.Fatalf("join: %v %+v", err, out)
	}
	if out.RecentPicks != nil {
		t.Fatalf("a joined ask has no recent_picks: %v", out.RecentPicks)
	}
	for _, u := range []string{"u1", "u2"} {
		if _, ok := r.storedPicks(u); ok {
			t.Fatalf("%s has a stored doc of recent picks", u)
		}
	}
}

func TestRecentPicksDropAMemberWhoLeftTheCrew(t *testing.T) {
	r := newLtpRig(t, 4)
	a := r.ask("u1", `{"crew_id":"c1","member_ids":["u2","u3"]}`)
	r.cancelAndWait("u1", a.AskID)
	r.ask("u1", `{"crew_id":"c1","member_ids":["u3"]}`)

	// u3 leaves the crew.
	var kept []*api.GroupUserList_GroupUser
	for _, m := range r.nk.members["c1"] {
		if m.GetUser().GetId() != "u3" {
			kept = append(kept, m)
		}
	}
	r.nk.members["c1"] = kept

	if raw := r.listRaw("u1"); !strings.Contains(raw, `"recent_picks":[["u2"]]`) {
		t.Fatalf("list = %s", raw)
	}
}

func TestListGivesAnEmptyListOfRecentPicks(t *testing.T) {
	r := newLtpRig(t, 3)
	if raw := r.listRaw("u2"); !strings.Contains(raw, `"recent_picks":[]`) {
		t.Fatalf(`"recent_picks" must be [] on the wire: %s`, raw)
	}
}

// notifyOf returns, for each member, the "notify" of the notifications
// 118 since index from: "true", "false" or "none".
func (r *ltpRig) notifyOf(from int) map[string]string {
	out := map[string]string{}
	for _, n := range r.nk.notes[from:] {
		if n.Code != NotifyLookingToPlay {
			continue
		}
		v, ok := n.Content["notify"]
		switch {
		case !ok:
			out[n.UserID] = "none"
		default:
			out[n.UserID] = fmt.Sprint(v)
		}
	}
	return out
}

func TestOnlyTheRecipientsOfANewAskGetNotify(t *testing.T) {
	r := newLtpRig(t, 4)
	// u2 turned Looking to play off.
	r.nk.put(PushPrefsCollection, pushPrefsKey, "u2", pushPrefsDoc{Crews: map[string]crewPushPrefs{"c1": {LookingToPlay: boolPtr(false)}}})

	a := r.ask("u1", `{"crew_id":"c1","member_ids":["u2","u3"]}`)
	want := map[string]string{"u1": "none", "u2": "false", "u3": "true", "u4": "false"}
	if got := r.notifyOf(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("notify = %v, want %v", got, want)
	}

	from := len(r.nk.notes)
	r.answer("u3", a.AskID, ltpAnswerIn)
	want = map[string]string{"u1": "none", "u2": "none", "u3": "none", "u4": "none"}
	if got := r.notifyOf(from); !reflect.DeepEqual(got, want) {
		t.Fatalf("an answer has no notify: %v", got)
	}
}

func TestInALargeCrewNotifyGoesToTheMembersWhoHaveTheGame(t *testing.T) {
	r := newLtpRig(t, largeCrewMembers+2) // u1..u12
	lib := func(ids ...string) LibraryDoc {
		var d LibraryDoc
		for _, id := range ids {
			d.Games = append(d.Games, LibraryGame{GameID: id, Name: id})
		}
		return d
	}
	r.nk.put(UserLibraryCollection, UserLibraryKey, "u2", lib("steam-730"))
	r.nk.put(UserLibraryCollection, UserLibraryKey, "u3", lib("steam-892970"))
	for i := 5; i <= 12; i++ {
		r.nk.put(UserLibraryCollection, UserLibraryKey, fmt.Sprintf("u%d", i), lib("steam-1"))
	}
	// u2 has the game. u4 has no library report. The others do not have it.
	r.ask("u1", `{"crew_id":"c1","game":{"game_id":"steam-730","name":"Counter-Strike 2"}}`)

	var notified []string
	for uid, v := range r.notifyOf(0) {
		switch {
		case uid == "u1" && v != "none":
			t.Fatalf("the asker gets no notify: %s", v)
		case v == "true":
			notified = append(notified, uid)
		case uid != "u1" && v != "false":
			t.Fatalf("%s: notify = %s, want false", uid, v)
		}
	}
	sort.Strings(notified)
	if want := []string{"u2", "u4"}; !reflect.DeepEqual(notified, want) {
		t.Fatalf("notify true for %v, want %v", notified, want)
	}
}
