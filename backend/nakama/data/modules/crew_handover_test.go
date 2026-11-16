package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
)

const (
	handCrew  = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	handOwner = "owner-" + handCrew
)

// nakamaDeleteAccount is what DELETE /v2/account does around the hooks on
// Nakama 3.21.0: a group whose last superadmin is the user is deleted with
// the account, also when it has other members, and no delete-group hook
// runs. Any other group loses only the user.
func nakamaDeleteAccount(t *testing.T, nk *delNk, userID string) {
	t.Helper()
	ctx := ctxWithUser(userID)
	if err := BeforeDeleteAccount(ctx, testLogger(), nil, nk); err != nil {
		t.Fatalf("the before hook failed the delete: %v", err)
	}
	for crewID, members := range nk.members {
		owner, others := false, 0
		for _, m := range members {
			if m.GetState().GetValue() != 0 {
				continue
			}
			if m.GetUser().GetId() == userID {
				owner = true
			} else {
				others++
			}
		}
		if owner && others == 0 {
			delete(nk.groups, crewID)
			delete(nk.members, crewID)
		}
	}
	removeAccount(nk, userID)
	if err := AfterDeleteAccount(ctx, testLogger(), nil, nk); err != nil {
		t.Fatal(err)
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

// A read that fails never says that the user is the only member, and a
// promote that fails or changes nothing is a failed handover.
func TestHandOverCrewFailures(t *testing.T) {
	seed := func() *delNk {
		nk := newDelNk()
		nk.groups[handCrew] = &api.Group{Id: handCrew}
		nk.addMemberAs(handCrew, "owner", "owner", 0)
		nk.addMemberAs(handCrew, "m1", "m1", 2)
		return nk
	}
	nk := seed()
	nk.memberErr[handCrew] = errors.New("database down")
	if got, _, err := handOverCrew(context.Background(), nk, handCrew, "owner"); got != handoverNone || err == nil {
		t.Fatalf("a failed member read = %d err=%v", got, err)
	}
	nk = seed()
	nk.promoteErr = errors.New("database down")
	if got, _, err := handOverCrew(context.Background(), nk, handCrew, "owner"); got != handoverFailed || err == nil {
		t.Fatalf("a failed promote = %d err=%v", got, err)
	}
	nk = seed()
	nk.promoteNoop = true
	if got, _, err := handOverCrew(context.Background(), nk, handCrew, "owner"); got != handoverFailed || err == nil {
		t.Fatalf("a promote that changed nothing = %d err=%v", got, err)
	}
}

// The only owner of a crew with other members deletes the account: the crew
// stays, with a new owner and all its data, in every phase.
func TestAccountDeleteHandsTheCrewOver(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		chat := currentMemChat(t)
		seedPurgeCrew(t, nk, f, handCrew)
		seedCrewChat(nk, f, chat, handCrew)
		nk.addMemberAs(handCrew, "m1", "Mo", 2)
		nk.addMemberAs(handCrew, "a1", "Ada", 1)
		before := crewLeftovers(t, nk, store, f, handCrew)

		nakamaDeleteAccount(t, nk, handOwner)

		if _, ok := nk.groups[handCrew]; !ok {
			t.Fatal("the crew is gone")
		}
		if memberState(nk, handCrew, "a1") != 0 || memberState(nk, handCrew, "m1") != 2 || memberState(nk, handCrew, handOwner) != -1 {
			t.Fatalf("members = %v", nk.members[handCrew])
		}
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, handCrew); rec != nil {
			t.Fatalf("a purge record of a crew that stays: %+v", rec)
		}
		reconcilePass(t, nk)
		runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		if after := crewLeftovers(t, nk, store, f, handCrew); strings.Join(after, "\n") != strings.Join(before, "\n") {
			t.Fatalf("the data of the crew changed:\n%v\nwas\n%v", after, before)
		}
		if left := chatLeftovers(nk, f, chat, handCrew); len(left) != 10 {
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
		seedPurgeCrew(t, nk, f, handCrew)
		seedPurgeCrew(t, nk, f, otherCrew)
		seedCrewChat(nk, f, chat, handCrew)
		// A join request is no member.
		nk.addMemberAs(handCrew, "asks", "Asks", 3)
		before := crewLeftovers(t, nk, store, f, otherCrew)

		nakamaDeleteAccount(t, nk, handOwner)

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

// A handover that fails does not fail the delete. Nakama then deletes the
// group with its members. The scrub does not purge that crew: the user was
// not its only member. admin_purge_crew removes it.
func TestFailedHandoverLeavesTheCrewToAdminPurge(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedPurgeCrew(t, nk, f, handCrew)
	nk.addMemberAs(handCrew, "m1", "Mo", 2)
	nk.promoteErr = errors.New("database down")

	nakamaDeleteAccount(t, nk, handOwner)

	if _, ok := nk.groups[handCrew]; ok {
		t.Fatal("the fake kept a group with no superadmin")
	}
	if rec, _ := readCrewPurgeRecord(context.Background(), nk, handCrew); rec != nil {
		t.Fatalf("the scrub purged a crew that had another member: %+v", rec)
	}
	if left := crewLeftovers(t, nk, store, f, handCrew); len(left) < 20 {
		t.Fatalf("the data of the crew is gone: %v", left)
	}
	runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if left := crewLeftovers(t, nk, store, f, handCrew); len(left) < 20 {
		t.Fatalf("the job purged the crew: %v", left)
	}
	if res, err := purgeRPC(nk, handCrew); err != nil || !res.Done {
		t.Fatalf("admin_purge_crew = %+v err=%v", res, err)
	}
	if left := crewLeftovers(t, nk, store, f, handCrew); len(left) != 0 {
		t.Fatalf("left after admin_purge_crew: %v", left)
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
		seedPurgeCrew(t, nk, f, handCrew)
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

	t.Run("the member read of the before hook failed", func(t *testing.T) {
		nk, store, f := seed(t)
		nk.memberErr[handCrew] = errors.New("database down")
		nakamaDeleteAccount(t, nk, handOwner)
		kept(t, nk, store, f, "after the delete")
	})

	t.Run("the group read of the scrub fails", func(t *testing.T) {
		nk, store, f := seed(t)
		if err := BeforeDeleteAccount(ctxWithUser(handOwner), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		removeAccount(nk, handOwner)
		delete(nk.groups, handCrew)
		delete(nk.members, handCrew)
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
		// The delete failed. The group is deleted by its owner later, with
		// its own purge. The scrub record does nothing.
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
		nk.addMemberAs(handCrew, "m1", "Mo", 2)
		nakamaDeleteAccount(t, nk, handOwner)
		if memberState(nk, handCrew, "m1") != 0 {
			t.Fatalf("members = %v", nk.members[handCrew])
		}
		kept(t, nk, store, f, "after the second attempt")
		// The new owner deletes the crew later: the scrub record of the
		// account must not hold the crew as a crew of an only member.
		if rec, _ := readUserScrubRecord(ctx, nk, handOwner); rec != nil && len(rec.SoleCrews) != 0 {
			t.Fatalf("record of the second attempt = %+v", rec)
		}
	})
}
