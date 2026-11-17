package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	handCrew  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	handCrew2 = "dddddddd-dddd-4ddd-8ddd-ddddddddddd2"
	handOwner = "eeeeeeee-0000-4000-8000-000000000001"
	handAdmin = "eeeeeeee-0000-4000-8000-000000000002"
	handMem1  = "eeeeeeee-0000-4000-8000-000000000003"
	handMem2  = "eeeeeeee-0000-4000-8000-000000000004"
	handOwnr2 = "eeeeeeee-0000-4000-8000-000000000005"
)

// memCrewEdges is the group_edge table for the rows that Nakama 3.21.0 keeps
// of a deleted superadmin or admin. A row is "crew user", with its state.
type memCrewEdges struct {
	// nk says which accounts and groups exist. A test sets it.
	nk   *delNk
	rows map[string]int
	err  error
	// drops are the calls of DropGoneMember: "crew user". reads counts the
	// calls of GoneMembers.
	drops []string
	reads int
}

func (m *memCrewEdges) DropGoneMember(_ context.Context, crewID, userID string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	m.drops = append(m.drops, crewID+" "+userID)
	// The statement never removes a row of an account that exists.
	if m.nk != nil {
		if _, ok := m.nk.users[userID]; ok {
			return 0, nil
		}
	}
	if _, ok := m.rows[crewID+" "+userID]; !ok {
		return 0, nil
	}
	delete(m.rows, crewID+" "+userID)
	return 2, nil
}

func (m *memCrewEdges) GoneMembers(_ context.Context, limit int) ([]goneMember, error) {
	m.reads++
	if m.err != nil {
		return nil, m.err
	}
	var keys []string
	for k := range m.rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []goneMember
	for _, k := range keys {
		crewID, userID, _ := strings.Cut(k, " ")
		if m.nk != nil {
			if _, group := m.nk.groups[crewID]; !group {
				continue
			}
			if _, account := m.nk.users[userID]; account {
				continue
			}
		}
		if len(out) < limit {
			out = append(out, goneMember{CrewID: crewID, UserID: userID})
		}
	}
	return out, nil
}

// useMemEdges gives the scrub a group_edge table in memory.
func useMemEdges(t *testing.T) *memCrewEdges {
	t.Helper()
	m := &memCrewEdges{rows: map[string]int{}}
	prev := crewEdges
	crewEdges = func() crewEdgeStore { return m }
	t.Cleanup(func() { crewEdges = prev })
	return m
}

// memEdgesOf returns the group_edge table that usePurgeBuckets installed,
// bound to the accounts and groups of nk.
func memEdgesOf(t *testing.T, nk *delNk) *memCrewEdges {
	t.Helper()
	m, ok := crewEdges().(*memCrewEdges)
	if !ok {
		t.Fatal("the test has no group_edge table in memory")
	}
	m.nk = nk
	return m
}

// nakamaRemoveAccount is the delete of Nakama 3.21.0 itself, between the two
// hooks:
//
//   - a group whose last superadmin is the user is deleted with the account,
//     also when it has other members. The rows that Nakama kept of a deleted
//     superadmin count as a superadmin;
//   - in a group that stays, Nakama keeps the membership rows of a
//     superadmin and of an admin.
func nakamaRemoveAccount(nk *delNk, userID string) {
	edges, _ := crewEdges().(*memCrewEdges)
	for crewID, members := range nk.members {
		state, others := -1, 0
		for _, m := range members {
			switch {
			case m.GetUser().GetId() == userID:
				state = int(m.GetState().GetValue())
			case m.GetState().GetValue() == 0:
				others++
			}
		}
		if edges != nil {
			for k, st := range edges.rows {
				if strings.HasPrefix(k, crewID+" ") && st == 0 {
					others++
				}
			}
		}
		switch {
		case state == 0 && others == 0:
			delete(nk.groups, crewID)
			delete(nk.members, crewID)
			if edges != nil {
				for k := range edges.rows {
					if strings.HasPrefix(k, crewID+" ") {
						delete(edges.rows, k)
					}
				}
			}
		case (state == 0 || state == 1) && edges != nil:
			edges.rows[crewID+" "+userID] = state
		}
	}
	removeAccount(nk, userID)
}

// nakamaDeleteAccount is DELETE /v2/account: the before hook, the delete of
// Nakama, the after hook. A before hook that fails refuses the delete.
func nakamaDeleteAccount(nk *delNk, userID string) error {
	ctx := ctxWithUser(userID)
	if err := BeforeDeleteAccount(ctx, testLogger(), nil, nk); err != nil {
		return err
	}
	nakamaRemoveAccount(nk, userID)
	return AfterDeleteAccount(ctx, testLogger(), nil, nk)
}

func mustDeleteAccount(t *testing.T, nk *delNk, userID string) {
	t.Helper()
	if err := nakamaDeleteAccount(nk, userID); err != nil {
		t.Fatalf("the account delete failed: %v", err)
	}
}

func memberState(nk *delNk, crewID, userID string) int {
	for _, m := range nk.members[crewID] {
		if m.GetUser().GetId() == userID {
			return int(m.GetState().GetValue())
		}
	}
	return -1
}

// seedHandCrew gives a crew the data of the purge seed, and handOwner as its
// only member and owner.
func seedHandCrew(t *testing.T, nk *delNk, f *fakeS3, crewID string) {
	t.Helper()
	seedPurgeCrew(t, nk, f, crewID)
	nk.members[crewID] = nil
	nk.addMemberAs(crewID, handOwner, "Owner", 0)
}

// The new owner is chosen by rank, then by seniority.
func TestHandOverCrewChoosesByRankThenSeniority(t *testing.T) {
	type member struct {
		id    string
		state int32
	}
	cases := []struct {
		name     string
		members  []member
		want     crewHandover
		newOwner string
		promotes int
	}{
		{"an admin before a member who joined earlier", []member{{"owner", 0}, {"m1", 2}, {"a1", 1}, {"m2", 2}}, handoverDone, "a1", 1},
		{"the admin who joined first", []member{{"owner", 0}, {"a1", 1}, {"m1", 2}, {"a2", 1}}, handoverDone, "a1", 1},
		{"no admin: the member who joined first", []member{{"m0", 2}, {"owner", 0}, {"m1", 2}}, handoverDone, "m0", 2},
		{"another superadmin: nothing to do", []member{{"owner", 0}, {"o2", 0}, {"m1", 2}}, handoverNone, "", 0},
		{"the user is no superadmin", []member{{"o2", 0}, {"owner", 1}, {"m1", 2}}, handoverNone, "", 0},
		{"the only member", []member{{"owner", 0}}, handoverSoleMember, "", 0},
		{"a join request and a ban are no members", []member{{"owner", 0}, {"asks", 3}, {"banned", 4}}, handoverSoleMember, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nk := newDelNk()
			nk.groups[handCrew] = &api.Group{Id: handCrew}
			for _, m := range tc.members {
				nk.addMemberAs(handCrew, m.id, m.id, m.state)
			}
			got, newOwner, err := handOverCrew(context.Background(), nk, handCrew, "owner")
			if err != nil || got != tc.want || newOwner != tc.newOwner || len(nk.promotes) != tc.promotes {
				t.Fatalf("handover = %d %q err=%v promotes=%v", got, newOwner, err, nk.promotes)
			}
			if tc.newOwner != "" && memberState(nk, handCrew, tc.newOwner) != 0 {
				t.Fatalf("the new owner has state %d", memberState(nk, handCrew, tc.newOwner))
			}
			for _, m := range tc.members {
				if m.id != tc.newOwner && memberState(nk, handCrew, m.id) != int(m.state) {
					t.Fatalf("%s changed to state %d", m.id, memberState(nk, handCrew, m.id))
				}
			}
		})
	}
}

// The only owner of a crew with other members deletes the account: the crew
// stays, with a new owner and all its data, in every phase. The membership
// rows that Nakama keeps of the owner go.
func TestAccountDeleteHandsTheCrewOver(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		chat := currentMemChat(t)
		edges := memEdgesOf(t, nk)
		seedHandCrew(t, nk, f, handCrew)
		seedCrewChat(nk, f, chat, handCrew)
		nk.addMemberAs(handCrew, handMem1, "Mo", 2)
		nk.addMemberAs(handCrew, handAdmin, "Ada", 1)
		before := crewLeftovers(t, nk, store, f, handCrew)

		mustDeleteAccount(t, nk, handOwner)

		if _, ok := nk.groups[handCrew]; !ok {
			t.Fatal("the crew is gone")
		}
		if memberState(nk, handCrew, handAdmin) != 0 || memberState(nk, handCrew, handMem1) != 2 || memberState(nk, handCrew, handOwner) != -1 {
			t.Fatalf("members = %v", nk.members[handCrew])
		}
		if len(edges.rows) != 0 || len(edges.drops) != 1 || edges.drops[0] != handCrew+" "+handOwner {
			t.Fatalf("membership rows = %v, drops = %v", edges.rows, edges.drops)
		}
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, handCrew); rec != nil {
			t.Fatalf("a purge record of a crew that stays: %+v", rec)
		}
		reconcilePass(t, nk)
		runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		if after := crewLeftovers(t, nk, store, f, handCrew); strings.Join(after, "\n") != strings.Join(before, "\n") {
			t.Fatalf("the data of the crew changed:\n%v\nwas\n%v", after, before)
		}
		if left := chatLeftovers(nk, f, chat, handCrew); len(left) != 11 {
			t.Fatalf("the chat of the crew = %v", left)
		}
	})
}

// The only member of a crew deletes the account: the group goes with the
// account, and the scrub purges the crew, in every phase.
func TestAccountDeleteOfTheOnlyMemberPurgesTheCrew(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		chat := currentMemChat(t)
		memEdgesOf(t, nk)
		seedHandCrew(t, nk, f, handCrew)
		seedPurgeCrew(t, nk, f, otherCrew)
		seedCrewChat(nk, f, chat, handCrew)
		// A join request is no member.
		nk.addMemberAs(handCrew, handMem1, "Asks", 3)
		before := crewLeftovers(t, nk, store, f, otherCrew)

		mustDeleteAccount(t, nk, handOwner)

		if _, ok := nk.groups[handCrew]; ok {
			t.Fatal("the fake kept the group of the only member")
		}
		if len(nk.promotes) != 0 {
			t.Fatalf("promotes = %v", nk.promotes)
		}
		if left := crewLeftovers(t, nk, store, f, handCrew); len(left) != 0 {
			t.Fatalf("left of the crew: %v", left)
		}
		if left := chatLeftovers(nk, f, chat, handCrew); len(left) != 0 {
			t.Fatalf("left of the chat: %v", left)
		}
		if rec := purgeRecordOf(t, nk, handCrew); rec.DoneMs == 0 {
			t.Fatalf("purge record = %+v", rec)
		}
		reconcilePass(t, nk)
		if after := crewLeftovers(t, nk, store, f, otherCrew); strings.Join(after, "\n") != strings.Join(before, "\n") {
			t.Fatalf("the other crew changed:\n%v\nwas\n%v", after, before)
		}
	})
}

// A crew with other members that cannot be handed over refuses the account
// delete: the crew must not go with the account. The user tries again, and
// the delete goes on when the handover works.
func TestHandoverThatFailsRefusesTheAccountDelete(t *testing.T) {
	down := errors.New("database down")
	cases := []struct {
		name  string
		fault func(nk *delNk)
		clear func(nk *delNk)
	}{
		{"the list of the crews fails", func(nk *delNk) { nk.userGroupsErr = down }, func(nk *delNk) { nk.userGroupsErr = nil }},
		{"the member read fails", func(nk *delNk) { nk.memberErr[handCrew] = down }, func(nk *delNk) { delete(nk.memberErr, handCrew) }},
		{"the promote fails", func(nk *delNk) { nk.promoteErr = down }, func(nk *delNk) { nk.promoteErr = nil }},
		{"the promote changes nothing", func(nk *delNk) { nk.promoteNoop = true }, func(nk *delNk) { nk.promoteNoop = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := useMemLedger(t)
			nk := newDelNk()
			f := useScrubInline(t)
			memEdgesOf(t, nk)
			seedHandCrew(t, nk, f, handCrew)
			nk.addMemberAs(handCrew, handMem1, "Mo", 2)
			before := crewLeftovers(t, nk, store, f, handCrew)

			tc.fault(nk)
			err := nakamaDeleteAccount(nk, handOwner)
			if err != errAccountDeleteHandover {
				t.Fatalf("the delete with a failed handover: err=%v, want the refusal", err)
			}
			// The text is what a client shows: no id, no cause.
			if msg := err.Error(); strings.Contains(msg, handCrew) || strings.Contains(msg, "database") {
				t.Fatalf("the refusal says %q", msg)
			}
			if _, ok := nk.users[handOwner]; !ok {
				t.Fatal("the account is gone")
			}
			if _, ok := nk.groups[handCrew]; !ok || memberState(nk, handCrew, handOwner) != 0 || memberState(nk, handCrew, handMem1) != 2 {
				t.Fatalf("the crew after a refused delete: members = %v", nk.members[handCrew])
			}
			if rec, _ := readUserScrubRecord(context.Background(), nk, handOwner); rec != nil {
				t.Fatalf("a refused delete wrote a scrub record: %+v", rec)
			}

			tc.clear(nk)
			mustDeleteAccount(t, nk, handOwner)
			if _, ok := nk.groups[handCrew]; !ok || memberState(nk, handCrew, handMem1) != 0 {
				t.Fatalf("the crew after the second attempt: members = %v", nk.members[handCrew])
			}
			if rec, _ := readCrewPurgeRecord(context.Background(), nk, handCrew); rec != nil {
				t.Fatalf("a purge record: %+v", rec)
			}
			if after := crewLeftovers(t, nk, store, f, handCrew); strings.Join(after, "\n") != strings.Join(before, "\n") {
				t.Fatalf("the data of the crew changed: %v", after)
			}
		})
	}
}

// The handovers of one delete have a time budget. A crew that the budget
// did not reach refuses the delete.
func TestHandoverBudgetRefusesTheAccountDelete(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedHandCrew(t, nk, f, handCrew)
	nk.addMemberAs(handCrew, handMem1, "Mo", 2)
	prev := crewHandoverBudget
	crewHandoverBudget = 0
	t.Cleanup(func() { crewHandoverBudget = prev })
	if err := nakamaDeleteAccount(nk, handOwner); err != errAccountDeleteHandover {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if _, ok := nk.users[handOwner]; !ok || len(nk.promotes) != 0 {
		t.Fatalf("the account is gone, or a member was promoted: %v", nk.promotes)
	}
}

// Nothing else in the before hook fails the delete: a scrub record that is
// not written, and a user who owns no crew when a member list is down.
func TestBeforeDeleteAccountFailsOnlyForAHandover(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	// The user is the only member of one crew and a plain member of another
	// whose member list cannot be read.
	seedHandCrew(t, nk, f, handCrew)
	nk.groups[handCrew2] = &api.Group{Id: handCrew2}
	nk.addMemberAs(handCrew2, handOwnr2, "Other", 0)
	nk.addMemberAs(handCrew2, handOwner, "Owner", 2)
	nk.memberErr[handCrew2] = errors.New("database down")
	nk.failWrite[UserScrubCollection] = true
	if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
		t.Fatalf("the before hook failed the delete: %v", err)
	}
}

// The before hook reads every page of the crews of the user.
func TestBeforeDeleteAccountReadsEveryPageOfCrews(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	useScrubInline(t)
	nk.groupsPage = 1
	for _, crewID := range []string{handCrew, handCrew2, otherCrew} {
		nk.groups[crewID] = &api.Group{Id: crewID}
		nk.addMemberAs(crewID, handOwner, "Owner", 0)
		nk.addMemberAs(crewID, handMem1, "Mo", 2)
	}
	if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	if nk.userGroupsCalls != 3 {
		t.Fatalf("calls of the crews list = %d, want 3 pages", nk.userGroupsCalls)
	}
	for _, crewID := range []string{handCrew, handCrew2, otherCrew} {
		if memberState(nk, crewID, handMem1) != 0 {
			t.Fatalf("crew %s was not handed over", crewID)
		}
	}
	rec, _ := readUserScrubRecord(context.Background(), nk, handOwner)
	if rec == nil || len(rec.Crews) != 3 {
		t.Fatalf("scrub record = %+v", rec)
	}
}

// The purge of the crew of the only member needs all of: the before hook saw
// no other member, the account is gone, and the group is seen to be gone.
func TestScrubPurgesOnlyTheCrewOfTheOnlyMember(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) (*delNk, *memLedgerStore, *fakeS3) {
		store := useMemLedger(t)
		nk := newDelNk()
		f := useScrubInline(t)
		memEdgesOf(t, nk)
		seedHandCrew(t, nk, f, handCrew)
		return nk, store, f
	}
	kept := func(t *testing.T, nk *delNk, store *memLedgerStore, f *fakeS3, when string) {
		t.Helper()
		if rec, _ := readCrewPurgeRecord(ctx, nk, handCrew); rec != nil {
			t.Fatalf("%s: a purge record: %+v", when, rec)
		}
		if left := crewLeftovers(t, nk, store, f, handCrew); len(left) < 20 {
			t.Fatalf("%s: the data of the crew is gone: %v", when, left)
		}
	}

	t.Run("a crew with no group that had another member", func(t *testing.T) {
		nk, store, f := seed(t)
		// The account was deleted with no before hook, and Nakama deleted
		// the group with its member.
		nk.addMemberAs(handCrew, handMem1, "Mo", 2)
		rec, err := updateUserScrubRecord(ctx, nk, handOwner, func(rec *userScrubRecord) { rec.addCrews(handCrew) })
		if err != nil || rec == nil {
			t.Fatal(err)
		}
		nakamaRemoveAccount(nk, handOwner)
		if _, ok := nk.groups[handCrew]; ok {
			t.Fatal("the fake kept a group with no superadmin")
		}
		if err := AfterDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		kept(t, nk, store, f, "after the scrub")
		runPendingCrewPurges(ctx, nk, testLogger(), time.Now().Add(time.Minute))
		kept(t, nk, store, f, "after the job")
		// admin_purge_crew removes it.
		if res, err := purgeRPC(nk, handCrew); err != nil || !res.Done {
			t.Fatalf("admin_purge_crew = %+v err=%v", res, err)
		}
		if left := crewLeftovers(t, nk, store, f, handCrew); len(left) != 0 {
			t.Fatalf("left after admin_purge_crew: %v", left)
		}
	})

	t.Run("the group read of the scrub fails", func(t *testing.T) {
		nk, store, f := seed(t)
		if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		nakamaRemoveAccount(nk, handOwner)
		nk.groupErr = errors.New("database down")
		if err := AfterDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		kept(t, nk, store, f, "with a failed group read")
		// The read works again: the next run of the scrub purges.
		nk.groupErr = nil
		runPendingUserScrubs(ctx, nk, testLogger(), time.Now().Add(time.Minute))
		if left := crewLeftovers(t, nk, store, f, handCrew); len(left) != 0 {
			t.Fatalf("left after the next run: %v", left)
		}
	})

	t.Run("the group still exists", func(t *testing.T) {
		nk, store, f := seed(t)
		if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		removeAccount(nk, handOwner)
		if err := AfterDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		kept(t, nk, store, f, "with a group")
	})

	t.Run("the account still exists", func(t *testing.T) {
		nk, store, f := seed(t)
		if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		// The delete failed. The scrub record does nothing.
		runPendingUserScrubs(ctx, nk, testLogger(), time.Now().Add(time.Minute))
		kept(t, nk, store, f, "with an account")
	})

	t.Run("a second attempt replaces the list of the first", func(t *testing.T) {
		nk, store, f := seed(t)
		if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		rec, _ := readUserScrubRecord(ctx, nk, handOwner)
		if rec == nil || len(rec.SoleCrews) != 1 {
			t.Fatalf("record of the first attempt = %+v", rec)
		}
		// The delete failed, and a member joined. The second attempt hands
		// the crew over.
		nk.addMemberAs(handCrew, handMem1, "Mo", 2)
		mustDeleteAccount(t, nk, handOwner)
		if memberState(nk, handCrew, handMem1) != 0 {
			t.Fatalf("members = %v", nk.members[handCrew])
		}
		kept(t, nk, store, f, "after the second attempt")
		if rec, _ := readUserScrubRecord(ctx, nk, handOwner); rec != nil && len(rec.SoleCrews) != 0 {
			t.Fatalf("record of the second attempt = %+v", rec)
		}
	})
}

// The membership rows that Nakama keeps of a deleted admin go with the
// scrub, and only after the account is gone. A delete of the rows that fails
// keeps the crew in the scrub, and the next run removes them.
func TestScrubRemovesTheMembershipRowsOfADeletedAccount(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	edges := memEdgesOf(t, nk)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	for _, m := range nk.members[scrubCrew] {
		if m.GetUser().GetId() == goneUser {
			m.State = wrapperspb.Int32(1)
		}
	}
	// A row of a member whose account exists.
	edges.rows[scrubCrew+" "+handMem1] = 1
	nk.users[handMem1] = &api.User{Id: handMem1}

	// The before hook ran and the delete did not happen yet: the job asks
	// for no row of an account that exists.
	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if len(edges.drops) != 0 {
		t.Fatalf("rows asked for while the account exists: %v", edges.drops)
	}

	nakamaRemoveAccount(nk, goneUser)
	if _, ok := edges.rows[scrubCrew+" "+goneUser]; !ok {
		t.Fatal("the fake kept no row of the deleted admin")
	}
	edges.err = errors.New("database down")
	if err := AfterDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs != 0 || len(rec.Crews) != 1 {
		t.Fatalf("scrub record after a failed delete of the rows = %+v", rec)
	}
	edges.err = nil
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs == 0 {
		t.Fatalf("scrub record = %+v", rec)
	}
	if _, ok := edges.rows[scrubCrew+" "+goneUser]; ok {
		t.Fatalf("rows = %v", edges.rows)
	}
	// Only the rows of the deleted account were asked for.
	for _, d := range edges.drops {
		if d != scrubCrew+" "+goneUser {
			t.Fatalf("the scrub asked for the rows %q", d)
		}
	}
	if _, ok := edges.rows[scrubCrew+" "+handMem1]; !ok {
		t.Fatal("the row of an account that exists is gone")
	}
}

// With no database handle the membership rows cannot be removed: the scrub
// is not done. It ends when the handle is there.
func TestScrubWaitsWithoutADatabaseHandle(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	edges := memEdgesOf(t, nk)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	crewEdges = func() crewEdgeStore { return nil }
	deleteAccount(t, nk, goneUser)
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs != 0 || len(rec.Crews) != 1 || !strings.Contains(rec.LastError, "database handle") {
		t.Fatalf("scrub record with no database handle = %+v", rec)
	}
	crewEdges = func() crewEdgeStore { return edges }
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs == 0 {
		t.Fatalf("scrub record = %+v", rec)
	}
}

// Two owners delete their accounts at the same time. Each before hook sees
// the other owner, and Nakama counts the rows it kept of the first as a
// superadmin when the second goes: the crew stays with no owner. The scrub
// gives it one, by the rule of the handover.
func TestTwoOwnersDeleteAtOnceLeaveAnOwner(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	useScrubInline(t)
	edges := memEdgesOf(t, nk)
	nk.groups[handCrew] = &api.Group{Id: handCrew}
	nk.addMemberAs(handCrew, handOwner, "Owner", 0)
	nk.addMemberAs(handCrew, handOwnr2, "Owner two", 0)
	nk.addMemberAs(handCrew, handMem1, "Mo", 2)
	nk.addMemberAs(handCrew, handMem2, "Mia", 2)
	for _, id := range []string{handOwner, handOwnr2} {
		if err := BeforeDeleteAccount(ctxWithUser(id), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
	}
	if len(nk.promotes) != 0 {
		t.Fatalf("a before hook promoted: %v", nk.promotes)
	}
	nakamaRemoveAccount(nk, handOwner)
	nakamaRemoveAccount(nk, handOwnr2)
	if _, ok := nk.groups[handCrew]; !ok || len(edges.rows) != 2 {
		t.Fatalf("the fake did not keep the group and the rows of both owners: %v", edges.rows)
	}
	for _, id := range []string{handOwner, handOwnr2} {
		if err := AfterDeleteAccount(ctxWithUser(id), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
	}
	if memberState(nk, handCrew, handMem1) != 0 || memberState(nk, handCrew, handMem2) != 2 || len(edges.rows) != 0 {
		t.Fatalf("members = %v rows = %v", nk.members[handCrew], edges.rows)
	}
	// A crew with an owner is left alone.
	nk.promotes = nil
	if owner, err := repairOwnerlessCrew(context.Background(), nk, testLogger(), handCrew); err != nil || owner != "" || len(nk.promotes) != 0 {
		t.Fatalf("repair of a crew with an owner = %q err=%v promotes=%v", owner, err, nk.promotes)
	}
}

// The sweep of the deletion job removes the membership rows of accounts that
// were deleted before the scrub did, a batch for each pass, and gives an
// owner to a crew that has none. The rows of an account that exists stay.
// After a pass that finds nothing the sweep does not read again.
func TestSweepGoneMembers(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	useScrubInline(t)
	edges := memEdgesOf(t, nk)
	goneMemberSweepDone.Store(false)
	t.Cleanup(func() { goneMemberSweepDone.Store(false) })
	ctx := context.Background()
	// A crew with an owner and the rows of a deleted admin.
	nk.groups[handCrew] = &api.Group{Id: handCrew}
	nk.addMemberAs(handCrew, handOwner, "Owner", 0)
	edges.rows[handCrew+" "+goneUser] = 1
	// A crew whose owner was deleted after a second owner: no owner now.
	nk.groups[handCrew2] = &api.Group{Id: handCrew2}
	nk.addMemberAs(handCrew2, handMem1, "Mo", 2)
	nk.addMemberAs(handCrew2, handAdmin, "Ada", 1)
	edges.rows[handCrew2+" "+goneUser] = 0
	// A row of an account that exists, and a row of a group that is gone.
	edges.rows[handCrew+" "+handOwner] = 0
	edges.rows[otherCrew+" "+goneUser] = 0

	if n := sweepGoneMembers(ctx, nk, testLogger(), time.Now().Add(time.Minute)); n != 4 {
		t.Fatalf("removed rows = %d, want 4", n)
	}
	if len(edges.rows) != 2 || edges.rows[handCrew+" "+handOwner] != 0 {
		t.Fatalf("rows = %v", edges.rows)
	}
	if _, ok := edges.rows[otherCrew+" "+goneUser]; !ok {
		t.Fatalf("rows = %v", edges.rows)
	}
	if memberState(nk, handCrew2, handAdmin) != 0 || memberState(nk, handCrew2, handMem1) != 2 || memberState(nk, handCrew, handOwner) != 0 {
		t.Fatalf("members = %v %v", nk.members[handCrew], nk.members[handCrew2])
	}
	if nk.has(LedgerMigrationCollection, goneMemberSweepKey) {
		t.Fatal("the marker is written after a pass that removed rows")
	}
	// The next pass finds nothing and writes the marker. Later passes do
	// not read.
	if n := sweepGoneMembers(ctx, nk, testLogger(), time.Now().Add(time.Minute)); n != 0 || !nk.has(LedgerMigrationCollection, goneMemberSweepKey) {
		t.Fatalf("second pass removed %d", n)
	}
	reads := edges.reads
	sweepGoneMembers(ctx, nk, testLogger(), time.Now().Add(time.Minute))
	// Another node reads the marker, and not the table.
	goneMemberSweepDone.Store(false)
	sweepGoneMembers(ctx, nk, testLogger(), time.Now().Add(time.Minute))
	if edges.reads != reads {
		t.Fatalf("reads after the marker = %d, want %d", edges.reads, reads)
	}
}
