package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// The built-in leave: the member check of BeforeLeaveGroup, and the handover
// of a crew whose only owner leaves (crew_handover.go).

const leaveOutsider = "eeeeeeee-0000-4000-8000-00000000000a"

// leaveMember is one member of a test crew, in the order of the joins.
type leaveMember struct {
	id    string
	state int32
}

// newLeaveNk makes one crew (handCrew) with the given members, in the order
// they joined.
func newLeaveNk(members ...leaveMember) *delNk {
	nk := newDelNk()
	nk.groups[handCrew] = &api.Group{Id: handCrew}
	for _, m := range members {
		nk.addMemberAs(handCrew, m.id, m.id, m.state)
	}
	return nk
}

// errNakamaLastSuperadmin is the refusal of Nakama for the leave of the last
// superadmin of a group.
var errNakamaLastSuperadmin = runtime.NewError("Cannot leave group when you are the last superadmin.", 3)

// nakamaLeaveGroup is the leave of Nakama 3.21.0 itself, after the before
// hook: a user with no row gets no error and nothing changes, the last
// superadmin is refused, and any other row is removed.
func nakamaLeaveGroup(nk *delNk, crewID, userID string) error {
	state, otherOwners := -1, 0
	for _, m := range nk.members[crewID] {
		switch {
		case m.GetUser().GetId() == userID:
			state = int(m.GetState().GetValue())
		case m.GetState().GetValue() == groupStateSuperadmin:
			otherOwners++
		}
	}
	if state < 0 {
		return nil
	}
	if state == groupStateSuperadmin && otherOwners == 0 {
		return errNakamaLastSuperadmin
	}
	kept := nk.members[crewID][:0:0]
	for _, m := range nk.members[crewID] {
		if m.GetUser().GetId() != userID {
			kept = append(kept, m)
		}
	}
	nk.members[crewID] = kept
	return nil
}

// leaveRecorder is a logger that keeps the Info and the Error lines.
type leaveRecorder struct {
	noopLogger
	lines *[]string
}

func (l leaveRecorder) Info(format string, v ...interface{}) {
	*l.lines = append(*l.lines, "info "+fmt.Sprintf(format, v...))
}

func (l leaveRecorder) Error(format string, v ...interface{}) {
	*l.lines = append(*l.lines, "error "+fmt.Sprintf(format, v...))
}

// builtinLeave is POST /v2/group/{id}/leave: the before hook, then the leave
// of Nakama. hookErr is the refusal of the hook. nakamaErr is the answer of
// Nakama to a request that the hook passed on.
func builtinLeave(t *testing.T, nk *delNk, logger runtime.Logger, crewID, userID string) (hookErr, nakamaErr error) {
	t.Helper()
	in := &api.LeaveGroupRequest{GroupId: crewID}
	out, err := BeforeLeaveGroup(ctxWithUser(userID), logger, nil, nk, in)
	if err != nil {
		if out != nil {
			t.Errorf("leave of %s by %s: the hook returned a request with its error", crewID, userID)
		}
		return err, nil
	}
	if out != in {
		t.Errorf("leave of %s by %s: the hook passed on another request", crewID, userID)
	}
	return nil, nakamaLeaveGroup(nk, crewID, userID)
}

// leaveOwners returns the superadmins of the crew, in the order of the joins.
func leaveOwners(nk *delNk, crewID string) []string {
	var out []string
	for _, m := range nk.members[crewID] {
		if m.GetState().GetValue() == groupStateSuperadmin {
			out = append(out, m.GetUser().GetId())
		}
	}
	return out
}

// seedCrewStateCache puts a crew state in the cache and returns a function
// that says if it is still there.
func seedCrewStateCache(t *testing.T, crewID string) (cached func() bool) {
	t.Helper()
	crewStateCacheMu.Lock()
	crewStateCache[crewID] = &CrewState{}
	crewStateCacheMu.Unlock()
	t.Cleanup(func() { InvalidateCrewState(crewID) })
	return func() bool {
		crewStateCacheMu.Lock()
		defer crewStateCacheMu.Unlock()
		_, ok := crewStateCache[crewID]
		return ok
	}
}

// ---- the member check ----

func TestBuiltinLeavePassesAMember(t *testing.T) {
	for _, caller := range []string{handOwner, handAdmin, handMem1} {
		nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handOwnr2, 0}, leaveMember{handAdmin, 1}, leaveMember{handMem1, 2})
		hookErr, nakamaErr := builtinLeave(t, nk, noopLogger{}, handCrew, caller)
		if hookErr != nil || nakamaErr != nil {
			t.Errorf("leave by %s of its crew: hook=%v nakama=%v, want the leave done", caller, hookErr, nakamaErr)
		}
		if memberState(nk, handCrew, caller) != -1 {
			t.Errorf("%s is in the crew after the leave", caller)
		}
	}
}

func TestBuiltinLeaveRefusesACallerOutsideTheCrew(t *testing.T) {
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handAdmin, 1}, leaveMember{handMem1, 2})
	nk.groups[handCrew2] = &api.Group{Id: handCrew2}
	nk.addMemberAs(handCrew2, handOwnr2, handOwnr2, 0)
	nk.addMemberAs(handCrew2, handMem2, handMem2, 2)

	out, err := BeforeLeaveGroup(ctxWithUser(leaveOutsider), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
	wantBuiltinRefused(t, "leave by an account in no crew", out == nil, err)
	// A member of one crew is outside another crew.
	out, err = BeforeLeaveGroup(ctxWithUser(handMem2), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
	wantBuiltinRefused(t, "leave of a crew that the caller is not in", out == nil, err)
	out, err = BeforeLeaveGroup(ctxWithUser(handOwnr2), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
	wantBuiltinRefused(t, "leave by the owner of another crew", out == nil, err)
	if len(nk.promotes) != 0 {
		t.Errorf("a refused leave promoted: %v", nk.promotes)
	}
	if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != handOwner {
		t.Errorf("owners of the crew after the refused leaves: %v", got)
	}
}

func TestBuiltinLeaveRefusesAJoinRequestRow(t *testing.T) {
	const asks, banned = "eeeeeeee-0000-4000-8000-00000000000b", "eeeeeeee-0000-4000-8000-00000000000c"
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{asks, groupStateJoinRequest}, leaveMember{banned, groupStateBanned}, leaveMember{handMem1, 2})
	for _, caller := range []string{asks, banned} {
		out, err := BeforeLeaveGroup(ctxWithUser(caller), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
		wantBuiltinRefused(t, "leave by a user with a join request row or a ban row", out == nil, err)
	}
	if memberState(nk, handCrew, asks) != groupStateJoinRequest || memberState(nk, handCrew, banned) != groupStateBanned {
		t.Errorf("a refused leave changed a row: join request %d, ban %d", memberState(nk, handCrew, asks), memberState(nk, handCrew, banned))
	}
	if len(nk.promotes) != 0 {
		t.Errorf("a refused leave promoted: %v", nk.promotes)
	}
}

func TestBuiltinLeaveRefusesACrewThatDoesNotExist(t *testing.T) {
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handMem1, 2})
	_, outsideErr := BeforeLeaveGroup(ctxWithUser(leaveOutsider), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
	if outsideErr != errBuiltinNotAMember {
		t.Fatalf("leave by an outsider: error %v, want errBuiltinNotAMember", outsideErr)
	}
	for _, id := range []string{handCrew2, "not-an-id", ""} {
		for _, caller := range []string{leaveOutsider, handMem1, handOwner} {
			out, err := BeforeLeaveGroup(ctxWithUser(caller), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: id})
			wantBuiltinRefused(t, "leave of the crew id "+id, out == nil, err)
			if err != outsideErr {
				t.Errorf("leave of the crew id %q: error %v, want the error of a caller outside a crew that exists", id, err)
			}
		}
	}
}

func TestBuiltinLeaveRefusesWhenTheMembershipReadFails(t *testing.T) {
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handMem1, 2})
	nk.userGroupsErr = errors.New("database is down")
	for _, caller := range []string{handMem1, handOwner} {
		out, err := BeforeLeaveGroup(ctxWithUser(caller), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
		if err == nil || out != nil {
			t.Fatalf("leave with a failed membership read: out=%v err=%v, want refused", out, err)
		}
		if rerr, ok := err.(*runtime.Error); !ok || rerr.Code != 13 {
			t.Errorf("leave with a failed membership read: error %v, want code 13", err)
		}
	}
	if len(nk.promotes) != 0 {
		t.Errorf("a leave with no membership read promoted: %v", nk.promotes)
	}
}

func TestBuiltinLeaveRefusesWithoutSession(t *testing.T) {
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handMem1, 2})
	out, err := BeforeLeaveGroup(context.Background(), noopLogger{}, nil, nk, &api.LeaveGroupRequest{GroupId: handCrew})
	wantBuiltinRefused(t, "leave with no session", out == nil, err)
	if nk.userGroupsCalls != 0 || len(nk.promotes) != 0 {
		t.Errorf("a leave with no session read the crews %d times and promoted %v", nk.userGroupsCalls, nk.promotes)
	}
}

// ---- the handover of the crew of an only owner ----

func TestBuiltinLeaveOfTheOnlyOwnerHandsTheCrewToAnAdmin(t *testing.T) {
	// The member joined before the admin. The rank comes first.
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handMem1, 2}, leaveMember{handAdmin, 1}, leaveMember{handMem2, 2})
	cached := seedCrewStateCache(t, handCrew)
	var lines []string

	hookErr, nakamaErr := builtinLeave(t, nk, leaveRecorder{lines: &lines}, handCrew, handOwner)
	if hookErr != nil || nakamaErr != nil {
		t.Fatalf("leave of the only owner: hook=%v nakama=%v, want the leave done", hookErr, nakamaErr)
	}
	if memberState(nk, handCrew, handOwner) != -1 {
		t.Errorf("the owner is in the crew after the leave")
	}
	if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != handAdmin {
		t.Errorf("owners after the leave: %v, want the admin only", got)
	}
	if memberState(nk, handCrew, handMem1) != groupStateMember || memberState(nk, handCrew, handMem2) != groupStateMember {
		t.Errorf("a member changed its rank: %d, %d", memberState(nk, handCrew, handMem1), memberState(nk, handCrew, handMem2))
	}
	if len(nk.promotes) != 1 {
		t.Errorf("promotes = %v, want one promote of the admin", nk.promotes)
	}
	if cached() {
		t.Errorf("the crew state is still in the cache after the handover")
	}
	want := "info " + fmt.Sprintf("crew_handover user=%s crew=%s new_owner=%s", handOwner, handCrew, handAdmin)
	if len(lines) != 1 || lines[0] != want {
		t.Errorf("log lines = %q, want one line %q", lines, want)
	}
}

func TestBuiltinLeaveOfTheOnlyOwnerHandsTheCrewToTheFirstMember(t *testing.T) {
	cases := []struct {
		name    string
		members []leaveMember
		want    string
	}{
		{"the owner joined first", []leaveMember{{handOwner, 0}, {handMem1, 2}, {handMem2, 2}}, handMem1},
		{"a member joined before the owner", []leaveMember{{handMem2, 2}, {handOwner, 0}, {handMem1, 2}}, handMem2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nk := newLeaveNk(tc.members...)
			hookErr, nakamaErr := builtinLeave(t, nk, noopLogger{}, handCrew, handOwner)
			if hookErr != nil || nakamaErr != nil {
				t.Fatalf("leave of the only owner: hook=%v nakama=%v, want the leave done", hookErr, nakamaErr)
			}
			if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != tc.want {
				t.Errorf("owners after the leave: %v, want %s", got, tc.want)
			}
			if memberState(nk, handCrew, handOwner) != -1 {
				t.Errorf("the owner is in the crew after the leave")
			}
			if len(nk.members[handCrew]) != 2 {
				t.Errorf("the crew has %d members after the leave, want 2", len(nk.members[handCrew]))
			}
			// A member goes two ranks up.
			if len(nk.promotes) != 2 {
				t.Errorf("promotes = %v, want two promotes of %s", nk.promotes, tc.want)
			}
		})
	}
}

func TestBuiltinLeaveOfTheOnlyMemberPassesWithNoPromote(t *testing.T) {
	const asks, banned = "eeeeeeee-0000-4000-8000-00000000000b", "eeeeeeee-0000-4000-8000-00000000000c"
	cases := map[string][]leaveMember{
		"the owner alone":                       {{handOwner, 0}},
		"with a join request row and a ban row": {{handOwner, 0}, {asks, groupStateJoinRequest}, {banned, groupStateBanned}},
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			nk := newLeaveNk(members...)
			cached := seedCrewStateCache(t, handCrew)
			var lines []string
			hookErr, nakamaErr := builtinLeave(t, nk, leaveRecorder{lines: &lines}, handCrew, handOwner)
			if hookErr != nil {
				t.Fatalf("leave of the only member: the hook refused it (%v), want the request passed on", hookErr)
			}
			if nakamaErr != errNakamaLastSuperadmin {
				t.Errorf("leave of the only member: Nakama answers %v, want its refusal of the last superadmin", nakamaErr)
			}
			if len(nk.promotes) != 0 {
				t.Errorf("promotes = %v, want none", nk.promotes)
			}
			if memberState(nk, handCrew, handOwner) != groupStateSuperadmin || len(nk.members[handCrew]) != len(members) {
				t.Errorf("the crew changed: owner state %d, %d rows", memberState(nk, handCrew, handOwner), len(nk.members[handCrew]))
			}
			if _, ok := nk.groups[handCrew]; !ok {
				t.Errorf("the crew is gone")
			}
			if !cached() || len(lines) != 0 {
				t.Errorf("a leave with no handover dropped the cache (%v) or wrote log lines %q", !cached(), lines)
			}
		})
	}
}

func TestBuiltinLeaveIsRefusedWhenTheHandoverFails(t *testing.T) {
	down := errors.New("database is down")
	cases := []struct {
		name  string
		fault func(nk *delNk)
	}{
		{"the next promote fails (handoverFaults)", func(nk *delNk) { handoverFaults.Store(1) }},
		{"the promote fails", func(nk *delNk) { nk.promoteErr = down }},
		{"the promote changes nothing", func(nk *delNk) { nk.promoteNoop = true }},
		{"the member read fails", func(nk *delNk) { nk.memberErr[handCrew] = down }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { handoverFaults.Store(0) })
			nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handMem1, 2}, leaveMember{handMem2, 2})
			cached := seedCrewStateCache(t, handCrew)
			var lines []string
			tc.fault(nk)

			hookErr, _ := builtinLeave(t, nk, leaveRecorder{lines: &lines}, handCrew, handOwner)
			if hookErr != errLeaveHandover {
				t.Fatalf("leave with a failed handover: hook error %v, want errLeaveHandover", hookErr)
			}
			if rerr, ok := hookErr.(*runtime.Error); !ok || rerr.Code != 14 {
				t.Errorf("the refusal has the code of %v, want 14", hookErr)
			}
			// The text is what a client shows: no id, no cause.
			if msg := hookErr.Error(); strings.Contains(msg, handCrew) || strings.Contains(msg, "database") || strings.Contains(msg, "promote") {
				t.Errorf("the refusal says %q", msg)
			}
			// Nobody left, and nobody has a new rank.
			if len(nk.members[handCrew]) != 3 || memberState(nk, handCrew, handOwner) != groupStateSuperadmin {
				t.Errorf("after the refused leave the crew has %d members and the owner has state %d", len(nk.members[handCrew]), memberState(nk, handCrew, handOwner))
			}
			if got := leaveOwners(nk, handCrew); len(got) != 1 {
				t.Errorf("owners after the refused leave: %v, want the owner only", got)
			}
			if cached() {
				t.Errorf("the crew state is still in the cache after the failed handover")
			}
			prefix := "error " + fmt.Sprintf("crew_handover_failed user=%s crew=%s:", handOwner, handCrew)
			if len(lines) != 1 || !strings.HasPrefix(lines[0], prefix) {
				t.Errorf("log lines = %q, want one line that starts with %q", lines, prefix)
			}

			// The fault is over: the same leave hands the crew over.
			handoverFaults.Store(0)
			nk.promoteErr, nk.promoteNoop = nil, false
			delete(nk.memberErr, handCrew)
			hookErr, nakamaErr := builtinLeave(t, nk, noopLogger{}, handCrew, handOwner)
			if hookErr != nil || nakamaErr != nil {
				t.Fatalf("the second leave: hook=%v nakama=%v, want the leave done", hookErr, nakamaErr)
			}
			if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != handMem1 {
				t.Errorf("owners after the second leave: %v, want %s", got, handMem1)
			}
		})
	}
}

func TestBuiltinLeaveOfAMemberMakesNoHandover(t *testing.T) {
	for _, caller := range []string{handMem1, handAdmin} {
		nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handAdmin, 1}, leaveMember{handMem1, 2})
		cached := seedCrewStateCache(t, handCrew)
		var lines []string
		hookErr, nakamaErr := builtinLeave(t, nk, leaveRecorder{lines: &lines}, handCrew, caller)
		if hookErr != nil || nakamaErr != nil {
			t.Fatalf("leave by %s: hook=%v nakama=%v, want the leave done", caller, hookErr, nakamaErr)
		}
		if len(nk.promotes) != 0 || len(lines) != 0 || !cached() {
			t.Errorf("leave by %s: promotes %v, log lines %q, cache kept %v; want no handover", caller, nk.promotes, lines, cached())
		}
		if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != handOwner {
			t.Errorf("owners after the leave by %s: %v, want the owner only", caller, got)
		}
		if memberState(nk, handCrew, caller) != -1 || len(nk.members[handCrew]) != 2 {
			t.Errorf("leave by %s: the crew has %d members", caller, len(nk.members[handCrew]))
		}
	}
}

func TestBuiltinLeaveOfASecondOwnerMakesNoHandover(t *testing.T) {
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handAdmin, 1}, leaveMember{handOwnr2, 0}, leaveMember{handMem1, 2})
	hookErr, nakamaErr := builtinLeave(t, nk, noopLogger{}, handCrew, handOwner)
	if hookErr != nil || nakamaErr != nil {
		t.Fatalf("leave of one of two owners: hook=%v nakama=%v, want the leave done", hookErr, nakamaErr)
	}
	if len(nk.promotes) != 0 {
		t.Errorf("promotes = %v, want none: the crew has another owner", nk.promotes)
	}
	if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != handOwnr2 {
		t.Errorf("owners after the leave: %v, want the second owner only", got)
	}
	if memberState(nk, handCrew, handAdmin) != groupStateAdmin || memberState(nk, handCrew, handMem1) != groupStateMember {
		t.Errorf("a member changed its rank")
	}
}

// When Nakama fails the leave after a handover, the crew has two owners. The
// next leave of the user needs no handover.
func TestBuiltinLeaveAgainAfterAHandoverMakesNoSecondHandover(t *testing.T) {
	nk := newLeaveNk(leaveMember{handOwner, 0}, leaveMember{handAdmin, 1}, leaveMember{handMem1, 2})
	// The hook runs, and the leave of Nakama does not.
	in := &api.LeaveGroupRequest{GroupId: handCrew}
	if out, err := BeforeLeaveGroup(ctxWithUser(handOwner), noopLogger{}, nil, nk, in); err != nil || out != in {
		t.Fatalf("the hook: out=%v err=%v, want the request passed on", out, err)
	}
	if got := leaveOwners(nk, handCrew); len(got) != 2 {
		t.Fatalf("owners after the handover with no leave: %v, want the user and the admin", got)
	}
	hookErr, nakamaErr := builtinLeave(t, nk, noopLogger{}, handCrew, handOwner)
	if hookErr != nil || nakamaErr != nil {
		t.Fatalf("the second leave: hook=%v nakama=%v, want the leave done", hookErr, nakamaErr)
	}
	if len(nk.promotes) != 1 {
		t.Errorf("promotes = %v, want the one promote of the first leave", nk.promotes)
	}
	if got := leaveOwners(nk, handCrew); len(got) != 1 || got[0] != handAdmin {
		t.Errorf("owners after the second leave: %v, want the admin only", got)
	}
	if memberState(nk, handCrew, handMem1) != groupStateMember {
		t.Errorf("the member has state %d, want member", memberState(nk, handCrew, handMem1))
	}
}
