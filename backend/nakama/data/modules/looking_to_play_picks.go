package main

// Looking to play: the recent picks of a member (plans/LOOKING-TO-PLAY.md
// sections 3.3 and 3.6).
//
// A member who asks picked members often asks the same group again. The
// server keeps the last groups that a member picked in a crew, so the
// desktop and the phone offer the same groups. The list is newest first. Each
// group holds sorted user ids, and a group is in the list once.

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// LookingToPlayPicksCollection holds one doc for each member and crew,
	// owned by the member. The key is the crew id.
	LookingToPlayPicksCollection = "looking_to_play_picks"
	// ltpMaxPicks is the number of groups that the list keeps.
	ltpMaxPicks = 3
)

// ltpPicksDoc is the stored doc of the recent picks.
type ltpPicksDoc struct {
	Groups [][]string `json:"groups"`
}

// ltpPickGroup returns the ids of a group sorted, with no duplicate and no
// empty id.
func ltpPickGroup(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func ltpSameGroup(a, b []string) bool {
	a, b = ltpPickGroup(a), ltpPickGroup(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ltpAddPick puts a group at the front of the list. A stored group with the
// same members moves to the front and is not kept twice. The list keeps
// ltpMaxPicks groups. An empty group changes nothing.
func ltpAddPick(groups [][]string, group []string) [][]string {
	group = ltpPickGroup(group)
	if len(group) == 0 {
		return groups
	}
	out := [][]string{group}
	for _, g := range groups {
		if len(out) == ltpMaxPicks {
			break
		}
		if !ltpSameGroup(g, group) {
			out = append(out, g)
		}
	}
	return out
}

// ltpFilterPicks drops the ids that are not members of the crew, then the
// groups that became empty. Two groups that became equal are kept once. The
// result is never nil, so the JSON is [] and not null.
func ltpFilterPicks(groups [][]string, members map[string]bool) [][]string {
	out := [][]string{}
	for _, g := range groups {
		var kept []string
		for _, id := range g {
			if members[id] {
				kept = append(kept, id)
			}
		}
		kept = ltpPickGroup(kept)
		if len(kept) == 0 {
			continue
		}
		dup := false
		for _, o := range out {
			if ltpSameGroup(o, kept) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, kept)
		}
	}
	return out
}

// readLtpPicks reads the stored list of a member in a crew, and its version.
func readLtpPicks(ctx context.Context, nk runtime.NakamaModule, userID, crewID string) ([][]string, string, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: LookingToPlayPicksCollection, Key: crewID, UserID: userID,
	}})
	if err != nil {
		return nil, "", err
	}
	if len(objects) == 0 {
		return nil, "", nil
	}
	var doc ltpPicksDoc
	if err := json.Unmarshal([]byte(objects[0].GetValue()), &doc); err != nil {
		// A doc that does not decode is replaced by the next write.
		return nil, objects[0].GetVersion(), nil
	}
	return doc.Groups, objects[0].GetVersion(), nil
}

// ltpRecentPicks returns the list of a member in a crew, members only. A
// read failure gives an empty list: the picks are an aid, not a rule.
func ltpRecentPicks(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userID, crewID string, members map[string]bool) [][]string {
	groups, _, err := readLtpPicks(ctx, nk, userID, crewID)
	if err != nil {
		logger.Warn("looking_to_play: read recent picks of %s in crew %s: %v", userID, crewID, err)
	}
	return ltpFilterPicks(groups, members)
}

// storeLtpPick puts a group at the front of the list of a member and returns
// the new list, members only. It writes with a version check and reads again
// when another write came first.
func storeLtpPick(ctx context.Context, nk runtime.NakamaModule, userID, crewID string, group []string, members map[string]bool) ([][]string, error) {
	var lastErr error
	for try := 0; try < ltpWriteTries; try++ {
		groups, version, err := readLtpPicks(ctx, nk, userID, crewID)
		if err != nil {
			return nil, err
		}
		next := ltpAddPick(ltpFilterPicks(groups, members), group)
		data, err := json.Marshal(ltpPicksDoc{Groups: next})
		if err != nil {
			return nil, err
		}
		_, lastErr = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      LookingToPlayPicksCollection,
			Key:             crewID,
			UserID:          userID,
			Value:           string(data),
			Version:         version,
			PermissionRead:  0,
			PermissionWrite: 0,
		}})
		if lastErr == nil {
			return next, nil
		}
	}
	return nil, lastErr
}
