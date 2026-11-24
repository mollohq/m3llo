package main

import (
	"context"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Who is a member of a crew, for the RPCs that give crew data.
//
// A row of a user in a crew has a state (invite_codes.go): owner, admin,
// member, join request or ban. Only the first three are a membership. A join
// request is a row that the user makes alone, so it gives no access.

// isMemberState reports whether a group_edge state is a membership.
func isMemberState(state int32) bool {
	return state >= groupStateSuperadmin && state <= groupStateMember
}

// memberCrews returns the ids of the crews that the user is a member of. It
// reads every page of the list, so it is right for a user in many crews.
func memberCrews(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]bool, error) {
	out := map[string]bool{}
	if userID == "" {
		return out, nil
	}
	cursor := ""
	for {
		groups, next, err := nk.UserGroupsList(ctx, userID, MaxCrewsPerUser, nil, cursor)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if id := g.GetGroup().GetId(); id != "" && isMemberState(g.GetState().GetValue()) {
				out[id] = true
			}
		}
		if next == "" || len(groups) == 0 {
			return out, nil
		}
		cursor = next
	}
}
