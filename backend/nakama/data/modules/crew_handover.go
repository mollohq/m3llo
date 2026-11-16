package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
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
// A crew with other members must not go with the account of its owner. When
// such a crew cannot be handed over, the before hook refuses the delete
// (errAccountDeleteHandover): the user can try again.
//
// A crew with no other member is not handed over. The group goes with the
// account, and the scrub of the account starts the purge of the crew
// (scrubCrewForUser).

// crewHandoverBudget bounds the handovers of one account delete. A var so a
// test makes it short.
var crewHandoverBudget = 10 * time.Second

// errAccountDeleteHandover is what the client gets when the before hook
// refuses an account delete. The text is safe to show.
var errAccountDeleteHandover = runtime.NewError("Your account was not deleted. A crew you own could not be handed to another member. Try again in a moment.", 14)

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
	// handoverFailed: the crew may have other members, and none was promoted.
	handoverFailed
)

// crewMembersInState returns up to limit members of a crew in one state, in
// the order of Nakama: the one who joined first comes first. Nakama lists
// only members whose account exists.
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

// promoteNextOwner makes one member of a crew a superadmin: an admin before
// a member, and among equals the one who joined first. newOwner is "" when
// the crew has no admin and no member. A join request and a banned user are
// no members.
func promoteNextOwner(ctx context.Context, nk runtime.NakamaModule, crewID string) (newOwner string, err error) {
	// One promote moves a member one rank up.
	steps := 0
	for _, state := range []int{groupStateAdmin, groupStateMember} {
		ids, err := crewMembersInState(ctx, nk, crewID, state, 1)
		if err != nil {
			return "", fmt.Errorf("member read: %w", err)
		}
		if len(ids) > 0 {
			newOwner, steps = ids[0], state
			break
		}
	}
	if newOwner == "" {
		return "", nil
	}
	for i := 0; i < steps; i++ {
		// No caller: the system promotes, with no permission check.
		if err := nk.GroupUsersPromote(ctx, "", crewID, []string{newOwner}); err != nil {
			return newOwner, fmt.Errorf("promote: %w", err)
		}
	}
	// A promote of Nakama that changes no row returns no error.
	owners, err := crewMembersInState(ctx, nk, crewID, groupStateSuperadmin, 100)
	if err != nil {
		return newOwner, fmt.Errorf("owner read after the promote: %w", err)
	}
	for _, id := range owners {
		if id == newOwner {
			return newOwner, nil
		}
	}
	return newOwner, errors.New("the member is no superadmin after the promote")
}

// handOverCrew makes another member the owner of a crew when userID is its
// only superadmin. An error always comes with handoverFailed: the crew was
// not read, or no member was promoted. A read that fails never says that the
// user is the only member.
func handOverCrew(ctx context.Context, nk runtime.NakamaModule, crewID, userID string) (outcome crewHandover, newOwner string, err error) {
	// Two superadmins are enough to know that the user is not the only one.
	owners, err := crewMembersInState(ctx, nk, crewID, groupStateSuperadmin, 2)
	if err != nil {
		return handoverFailed, "", fmt.Errorf("owner read: %w", err)
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
	newOwner, err = promoteNextOwner(ctx, nk, crewID)
	switch {
	case err != nil:
		return handoverFailed, newOwner, err
	case newOwner == "":
		return handoverSoleMember, "", nil
	}
	return handoverDone, newOwner, nil
}

// handOverOwnedCrews runs the handover for each crew the user is a
// superadmin of. soleCrews are the crews the user is the only member of.
// failed is the number of crews that could not be handed over, or not be
// read: the account delete must then not go on.
func handOverOwnedCrews(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, userID string, ownedCrews []string) (soleCrews []string, failed int) {
	ctx, cancel := context.WithTimeout(ctx, crewHandoverBudget)
	defer cancel()
	for _, crewID := range ownedCrews {
		if ctx.Err() != nil {
			failed++
			logger.Error("crew_handover_failed user=%s crew=%s: the time budget of the handover is over. The account delete is refused", userID, crewID)
			continue
		}
		outcome, newOwner, err := handOverCrew(ctx, nk, crewID, userID)
		switch outcome {
		case handoverDone:
			InvalidateCrewState(crewID)
			logger.Info("crew_handover user=%s crew=%s new_owner=%s", userID, crewID, newOwner)
		case handoverSoleMember:
			soleCrews = append(soleCrews, crewID)
			logger.Info("crew_handover user=%s crew=%s: the user is the only member. The group goes with the account, and the scrub purges the crew", userID, crewID)
		case handoverFailed:
			failed++
			// A promote can have changed a rank before it failed.
			InvalidateCrewState(crewID)
			logger.Error("crew_handover_failed user=%s crew=%s: %v. The account delete is refused", userID, crewID, err)
		}
	}
	return soleCrews, failed
}

// repairOwnerlessCrew gives a crew an owner when it has members and no
// superadmin, by the rule of the handover. newOwner is "" when the crew has
// a superadmin, or no member to promote.
//
// A crew can lose its owner when two superadmins delete their accounts at
// the same time: each before hook sees the other, and Nakama counts the
// membership rows it keeps of the first (see below) as a superadmin when the
// second goes. The scrub of an account and the sweep of the deletion job
// call this after they removed such rows.
func repairOwnerlessCrew(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, crewID string) (newOwner string, err error) {
	exists, err := crewGroupExists(ctx, nk, crewID)
	if err != nil || !exists {
		return "", err
	}
	owners, err := crewMembersInState(ctx, nk, crewID, groupStateSuperadmin, 1)
	if err != nil {
		return "", fmt.Errorf("owner read: %w", err)
	}
	if len(owners) > 0 {
		return "", nil
	}
	newOwner, err = promoteNextOwner(ctx, nk, crewID)
	if err != nil {
		logger.Error("crew_owner_repair_failed crew=%s: %v", crewID, err)
		return "", err
	}
	if newOwner == "" {
		logger.Warn("crew_owner_repair crew=%s: the crew has no superadmin and no member", crewID)
		return "", nil
	}
	InvalidateCrewState(crewID)
	logger.Info("crew_owner_repaired crew=%s new_owner=%s", crewID, newOwner)
	return newOwner, nil
}

// The membership rows of a deleted account.
//
// Nakama 3.21.0 keeps both group_edge rows of a deleted account that was a
// superadmin or an admin of a group that stays: its delete removes only rows
// with a state above 1 (member, join request), and lowers the member count
// in every case. The rows that stay name the deleted user. A superadmin row
// also counts in the checks of Nakama for the last superadmin: with it, the
// new owner could leave the crew, and the crew would have no owner.
//
// The runtime of Nakama has no call that removes these rows without a second
// change of the member count (a kick lowers it again). So the scrub deletes
// them with SQL, after the account is gone. The statement checks again that
// the account is gone: it never removes a row of an account that exists.

// crewEdgeStore is what the deletion rules need of the group_edge table.
type crewEdgeStore interface {
	// DropGoneMember deletes the rows between a crew and a user whose
	// account is gone. It returns the number of deleted rows.
	DropGoneMember(ctx context.Context, crewID, userID string) (int, error)
	// GoneMembers returns up to limit pairs of a group that exists and a
	// member of it whose account is gone.
	GoneMembers(ctx context.Context, limit int) ([]goneMember, error)
}

// goneMember is a membership row of a group whose user has no account.
type goneMember struct {
	CrewID string
	UserID string
}

// crewEdges returns the group_edge table of this install, or nil when the
// module has no database handle. A var so a test gives a fake.
var crewEdges = func() crewEdgeStore {
	if globalDb == nil {
		return nil
	}
	return sqlCrewEdges{db: globalDb}
}

type sqlCrewEdges struct {
	db *sql.DB
}

func (s sqlCrewEdges) DropGoneMember(ctx context.Context, crewID, userID string) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM group_edge
		 WHERE ((source_id = $1::UUID AND destination_id = $2::UUID) OR (source_id = $2::UUID AND destination_id = $1::UUID))
		   AND NOT EXISTS (SELECT 1 FROM users WHERE id = $2::UUID)`,
		crewID, userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s sqlCrewEdges) GoneMembers(ctx context.Context, limit int) ([]goneMember, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ge.source_id, ge.destination_id FROM group_edge ge
		 WHERE EXISTS (SELECT 1 FROM groups g WHERE g.id = ge.source_id)
		   AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = ge.destination_id)
		 ORDER BY ge.source_id, ge.destination_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []goneMember
	for rows.Next() {
		var m goneMember
		if err := rows.Scan(&m.CrewID, &m.UserID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// errNoDatabase is the error of a step that needs the database handle of
// the module and has none. The step is then not done.
var errNoDatabase = errors.New("the module has no database handle")

// dropGoneMember removes the membership rows that Nakama left of a deleted
// account in a crew. Ids that are no UUID are in no row.
func dropGoneMember(ctx context.Context, crewID, userID string) (int, error) {
	if !validPurgeCrewID(crewID) || !validPurgeCrewID(userID) {
		return 0, nil
	}
	edges := crewEdges()
	if edges == nil {
		return 0, errNoDatabase
	}
	return edges.DropGoneMember(ctx, crewID, userID)
}

// The sweep of the membership rows of accounts that were deleted before the
// scrub removed such rows (and of accounts deleted with no hook). It runs in
// the deletion job, a batch at a time, until one pass finds no row. Then a
// marker is written and the sweep does not run again on this install.
//
// Cost: one pass reads the group_edge table once, with one index read in
// groups and one in users for each row. An install with no such row pays
// one pass.

const (
	goneMemberSweepKey = "gone_members_swept"
	// goneMemberSweepBatch is the number of rows of one pass.
	goneMemberSweepBatch = 500
)

// goneMemberSweepDone is true when this process saw or wrote the marker.
var goneMemberSweepDone atomic.Bool

// sweepGoneMembers runs one pass of the sweep: it removes the rows of up to
// goneMemberSweepBatch members whose account is gone, and gives an owner to
// each of their crews that has none. It returns the number of removed rows.
func sweepGoneMembers(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, deadline time.Time) (removed int) {
	if goneMemberSweepDone.Load() {
		return 0
	}
	edges := crewEdges()
	if edges == nil {
		return 0
	}
	var marker struct {
		DoneMs int64 `json:"done_ms"`
	}
	if found, _, err := readDocStrict(ctx, nk, LedgerMigrationCollection, goneMemberSweepKey, &marker); err != nil {
		logger.Error("gone_member_sweep_failed: marker read: %v", err)
		return 0
	} else if found && marker.DoneMs > 0 {
		goneMemberSweepDone.Store(true)
		return 0
	}
	members, err := edges.GoneMembers(ctx, goneMemberSweepBatch)
	if err != nil {
		logger.Error("gone_member_sweep_failed: read: %v", err)
		return 0
	}
	crews := map[string]bool{}
	failed := 0
	for _, m := range members {
		if time.Now().After(deadline) {
			break
		}
		// The statement checks again that the account is gone.
		n, err := dropGoneMember(ctx, m.CrewID, m.UserID)
		if err != nil {
			failed++
			logger.Error("gone_member_sweep_failed crew=%s user=%s: %v", m.CrewID, m.UserID, err)
			continue
		}
		removed += n
		crews[m.CrewID] = true
	}
	repaired := 0
	for crewID := range crews {
		InvalidateCrewState(crewID)
		if owner, err := repairOwnerlessCrew(ctx, nk, logger, crewID); err != nil {
			failed++
		} else if owner != "" {
			repaired++
		}
	}
	if len(members) > 0 || failed > 0 {
		logger.Info("gone_member_sweep members=%d rows=%d crews=%d owners_repaired=%d failed=%d", len(members), removed, len(crews), repaired, failed)
		return removed
	}
	data, _ := json.Marshal(map[string]int64{"done_ms": time.Now().UnixMilli()})
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection: LedgerMigrationCollection, Key: goneMemberSweepKey, UserID: SystemUserID,
		Value: string(data), PermissionRead: 0, PermissionWrite: 0,
	}}); err != nil {
		logger.Error("gone_member_sweep_failed: marker write: %v", err)
		return 0
	}
	goneMemberSweepDone.Store(true)
	logger.Info("gone_member_sweep done: no membership row of a deleted account is left")
	return 0
}
