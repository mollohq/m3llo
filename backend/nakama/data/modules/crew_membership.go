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

// crewMates returns the members of every crew that the user is a member of.
func crewMates(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]bool, error) {
	crews, err := memberCrews(ctx, nk, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for crewID := range crews {
		for id := range crewMemberSet(ctx, nk, crewID) {
			out[id] = true
		}
	}
	return out, nil
}

// mutualFriends returns the users who are friends of the user and accepted
// it: state 0 of the friend list. A request that one side sent is not in it.
func mutualFriends(ctx context.Context, nk runtime.NakamaModule, userID string) (map[string]bool, error) {
	out := map[string]bool{}
	accepted := 0
	cursor := ""
	for {
		friends, next, err := nk.FriendsList(ctx, userID, 100, &accepted, cursor)
		if err != nil {
			return nil, err
		}
		for _, f := range friends {
			if id := f.GetUser().GetId(); id != "" && f.GetState().GetValue() == 0 {
				out[id] = true
			}
		}
		if next == "" || len(friends) == 0 {
			return out, nil
		}
		cursor = next
	}
}
