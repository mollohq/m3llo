package main

import (
	"context"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The handover of a crew whose only owner deletes the account
// (specs/16-CREW-EVENT-LEDGER.md section 10.2).
//
// When the last superadmin of a group deletes the account, Nakama deletes
// the group with the account, also when the group has other members, and no
// delete-group hook runs. So the handover runs in BeforeDeleteAccount,
// before Nakama acts: one other member becomes a superadmin, and Nakama then
// removes only the membership of the user.
//
// The new owner is chosen by rank, then by seniority: an admin before a
// member, and among equals the one who joined first. The join time is the
// position of the member in the group: Nakama sets it when the member joins
// (or asks to join), never changes it, and lists the members of one state in
// its order. The module reads no other join time.
//
// A crew with no other member is not handed over. The group goes with the
// account, and the scrub of the account starts the purge of the crew
// (scrubCrewForUser).

// crewHandoverBudget bounds the handovers of one account delete. The before
// hook must not hold the delete.
const crewHandoverBudget = 10 * time.Second

// crewHandover is what the before hook did for one crew.
type crewHandover int

const (
	// handoverNone: the user is no superadmin of the crew, or the crew has
	// another superadmin. Nakama keeps the group.
	handoverNone crewHandover = iota
	// handoverDone: another member is a superadmin now.
	handoverDone
	// handoverSoleMember: the user is the only member. The group goes with
	// the account.
	handoverSoleMember
	// handoverFailed: the crew has other members and none was promoted.
	handoverFailed
)

// crewMembersInState returns up to limit members of a crew in one state, in
// the order of Nakama: the one who joined first comes first.
func crewMembersInState(ctx context.Context, nk runtime.NakamaModule, crewID string, state, limit int) ([]string, error) {
	members, _, err := nk.GroupUsersList(ctx, crewID, limit, &state, "")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		if int(m.GetState().GetValue()) != state {
			continue
		}
		if id := m.GetUser().GetId(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// handOverCrew makes another member the owner of a crew when userID is its
// only superadmin. newOwner is the promoted member. An error comes with
// handoverFailed, and with handoverNone when the crew could not be read: a
// read that fails never says that the user is the only member.
func handOverCrew(ctx context.Context, nk runtime.NakamaModule, crewID, userID string) (outcome crewHandover, newOwner string, err error) {
	// Two superadmins are enough to know that the user is not the only one.
	owners, err := crewMembersInState(ctx, nk, crewID, groupStateSuperadmin, 2)
	if err != nil {
		return handoverNone, "", fmt.Errorf("owner read: %w", err)
	}
	isOwner := false
	for _, id := range owners {
		if id != userID {
			return handoverNone, "", nil
		}
		isOwner = true
	}
	if !isOwner {
		return handoverNone, "", nil
	}
	// Rank, then seniority. One promote moves a member one rank up.
	steps := 0
	for _, state := range []int{groupStateAdmin, groupStateMember} {
		ids, err := crewMembersInState(ctx, nk, crewID, state, 1)
		if err != nil {
			return handoverNone, "", fmt.Errorf("member read: %w", err)
		}
		if len(ids) > 0 {
			newOwner, steps = ids[0], state
			break
		}
	}
	if newOwner == "" {
		return handoverSoleMember, "", nil
	}
	for i := 0; i < steps; i++ {
		// No caller: the system promotes, with no permission check.
		if err := nk.GroupUsersPromote(ctx, "", crewID, []string{newOwner}); err != nil {
			return handoverFailed, newOwner, fmt.Errorf("promote: %w", err)
		}
	}
	// A promote of Nakama that changes no row returns no error.
	owners, err = crewMembersInState(ctx, nk, crewID, groupStateSuperadmin, 3)
	if err != nil {
		return handoverFailed, newOwner, fmt.Errorf("owner read after the promote: %w", err)
	}
	for _, id := range owners {
		if id == newOwner {
			return handoverDone, newOwner, nil
		}
	}
	return handoverFailed, newOwner, fmt.Errorf("the member is no superadmin after the promote")
}

// handOverOwnedCrews runs the handover for each crew the user is a
// superadmin of. It returns the crews the user is the only member of. It
// never returns an error: a handover that fails is logged, and the account
// delete goes on.
func handOverOwnedCrews(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, ownedCrews []string) (soleCrews []string) {
	ctx, cancel := context.WithTimeout(ctx, crewHandoverBudget)
	defer cancel()
	for _, crewID := range ownedCrews {
		if ctx.Err() != nil {
			logger.Error("crew_handover_failed user=%s crew=%s: the time budget of the handover is over. If the user is the only owner, the group goes with the account: run admin_purge_crew after the delete", userID, crewID)
			continue
		}
		outcome, newOwner, err := handOverCrew(ctx, nk, crewID, userID)
		switch {
		case outcome == handoverDone:
			InvalidateCrewState(crewID)
			logger.Info("crew_handover user=%s crew=%s new_owner=%s", userID, crewID, newOwner)
		case outcome == handoverSoleMember:
			soleCrews = append(soleCrews, crewID)
			logger.Info("crew_handover user=%s crew=%s: the user is the only member. The group goes with the account, and the scrub purges the crew", userID, crewID)
		case err != nil:
			// The delete goes on. When the user is the only owner, Nakama
			// deletes the group with its members, and the data of the crew
			// stays until admin_purge_crew is run.
			InvalidateCrewState(crewID)
			logger.Error("crew_handover_failed user=%s crew=%s: %v. If the user is the only owner, the group goes with the account: run admin_purge_crew after the delete", userID, crewID, err)
		}
	}
	return soleCrews
}
