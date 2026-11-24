package main

// Who is a member of a crew, for the RPCs that give crew data.
//
// A row of a user in a crew has a state (invite_codes.go): owner, admin,
// member, join request or ban. Only the first three are a membership. A join
// request is a row that the user makes alone, so it gives no access.

// isMemberState reports whether a group_edge state is a membership.
func isMemberState(state int32) bool {
	return state >= groupStateSuperadmin && state <= groupStateMember
}
