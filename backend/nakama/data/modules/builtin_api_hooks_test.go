package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	biaOpenCrew    = "aaaaaaaa-0000-4000-8000-000000000001"
	biaClosedCrew  = "aaaaaaaa-0000-4000-8000-000000000002"
	biaUnknownCrew = "aaaaaaaa-0000-4000-8000-00000000000f"
	biaOwner       = "bbbbbbbb-0000-4000-8000-000000000001"
	biaAdmin       = "bbbbbbbb-0000-4000-8000-000000000002"
	biaMember      = "bbbbbbbb-0000-4000-8000-000000000003"
	biaRequester   = "bbbbbbbb-0000-4000-8000-000000000004"
	biaBanned      = "bbbbbbbb-0000-4000-8000-000000000005"
	biaOutsider    = "bbbbbbbb-0000-4000-8000-000000000006"
)

// builtinNk is an in-memory runtime.NakamaModule for the built-in API hooks:
// groups, and the row of each user in a group with its state. A call to any
// other method panics on the nil embedded interface.
type builtinNk struct {
	runtime.NakamaModule

	groups map[string]*api.Group
	edges  map[string]map[string]int32 // crew id -> user id -> state
	// kicked records the rows that GroupUsersKick removed: crew id, user id.
	kicked [][2]string
	// wildcardNames records the names of a GroupsList call that still had a
	// wildcard of the name filter in them.
	wildcardNames []string
	failReads     bool
	// kickKeepsRows makes GroupUsersKick answer nil and remove nothing.
	kickKeepsRows bool
	// requestTimes holds the update time of a join request row.
	requestTimes map[[2]string]time.Time
	// trace, when set, gets one line for each row that GroupUsersKick is
	// asked to remove.
	trace *[]string
}

func newBuiltinNk() *builtinNk {
	nk := &builtinNk{groups: map[string]*api.Group{}, edges: map[string]map[string]int32{}}
	nk.addCrew(biaOpenCrew, "Open Crew", true)
	nk.addCrew(biaClosedCrew, "Closed_Crew", false)
	nk.edges[biaClosedCrew] = map[string]int32{
		biaOwner:     groupStateSuperadmin,
		biaAdmin:     groupStateAdmin,
		biaMember:    groupStateMember,
		biaRequester: groupStateJoinRequest,
		biaBanned:    groupStateBanned,
	}
	nk.edges[biaOpenCrew] = map[string]int32{biaOwner: groupStateSuperadmin}
	return nk
}

func (n *builtinNk) addCrew(id, name string, open bool) {
	n.groups[id] = &api.Group{Id: id, Name: name, Open: wrapperspb.Bool(open)}
	if n.edges[id] == nil {
		n.edges[id] = map[string]int32{}
	}
}

func (n *builtinNk) GroupsGetId(_ context.Context, ids []string) ([]*api.Group, error) {
	if n.failReads {
		return nil, runtime.NewError("database is down", 13)
	}
	var out []*api.Group
	for _, id := range ids {
		if g, ok := n.groups[id]; ok {
			out = append(out, g)
		}
	}
	return out, nil
}

// likeUnescape returns the literal text of a name filter and whether the
// filter has a wildcard that is not escaped.
func likeUnescape(name string) (literal string, wildcard bool) {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '\\' && i+1 < len(name):
			i++
			b.WriteByte(name[i])
		case c == '%' || c == '_':
			wildcard = true
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), wildcard
}

// GroupsList matches the name as Nakama does: without regard to case, with
// the wildcards % and _. A wildcard here matches every group.
func (n *builtinNk) GroupsList(_ context.Context, name, _ string, _ *int, _ *bool, _ int, _ string) ([]*api.Group, string, error) {
	if n.failReads {
		return nil, "", runtime.NewError("database is down", 13)
	}
	literal, wildcard := likeUnescape(name)
	if wildcard {
		n.wildcardNames = append(n.wildcardNames, name)
	}
	var out []*api.Group
	for _, g := range n.groups {
		if wildcard || strings.EqualFold(g.GetName(), literal) {
			out = append(out, g)
		}
	}
	return out, "", nil
}

func (n *builtinNk) UserGroupsList(_ context.Context, userID string, _ int, _ *int, _ string) ([]*api.UserGroupList_UserGroup, string, error) {
	if n.failReads {
		return nil, "", runtime.NewError("database is down", 13)
	}
	if userID == "" {
		// Nakama takes a user id only.
		return nil, "", runtime.NewError("expects user ID to be a valid identifier", 3)
	}
	var out []*api.UserGroupList_UserGroup
	for crew, users := range n.edges {
		if state, ok := users[userID]; ok {
			out = append(out, &api.UserGroupList_UserGroup{Group: n.groups[crew], State: wrapperspb.Int32(state)})
		}
	}
	return out, "", nil
}

func (n *builtinNk) GroupUsersList(_ context.Context, id string, _ int, state *int, _ string) ([]*api.GroupUserList_GroupUser, string, error) {
	if n.failReads {
		return nil, "", runtime.NewError("database is down", 13)
	}
	var out []*api.GroupUserList_GroupUser
	for user, s := range n.edges[id] {
		if state != nil && int32(*state) != s {
			continue
		}
		out = append(out, &api.GroupUserList_GroupUser{User: &api.User{Id: user}, State: wrapperspb.Int32(s)})
	}
	return out, "", nil
}

func (n *builtinNk) GroupUsersKick(_ context.Context, _ string, groupID string, userIDs []string) error {
	for _, id := range userIDs {
		if _, ok := n.edges[groupID][id]; !ok {
			continue
		}
		n.kicked = append(n.kicked, [2]string{groupID, id})
		if n.trace != nil {
			*n.trace = append(*n.trace, "kick "+groupID+" "+id)
		}
		if !n.kickKeepsRows {
			delete(n.edges[groupID], id)
		}
	}
	return nil
}

// wantBuiltinRefused fails the test unless the hook refused the call with
// PERMISSION_DENIED and returned no request for Nakama to run.
func wantBuiltinRefused(t *testing.T, what string, requestIsNil bool, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: allowed, want refused", what)
	}
	if !requestIsNil {
		t.Errorf("%s: the hook returned a request with its error", what)
	}
	rerr, ok := err.(*runtime.Error)
	if !ok {
		t.Fatalf("%s: error is %T, want *runtime.Error", what, err)
	}
	if rerr.Code != 7 {
		t.Errorf("%s: error code = %d (%q), want 7", what, rerr.Code, rerr.Message)
	}
}

// ---- join ----

func TestBuiltinJoinRefusesAnInviteOnlyCrew(t *testing.T) {
	nk := newBuiltinNk()
	for _, caller := range []string{biaOutsider, biaRequester, biaBanned} {
		out, err := BeforeJoinGroup(ctxWithUser(caller), noopLogger{}, nil, nk, &api.JoinGroupRequest{GroupId: biaClosedCrew})
		wantBuiltinRefused(t, "join of the invite-only crew by "+caller, out == nil, err)
	}
}

func TestBuiltinJoinAllowsAnOpenCrew(t *testing.T) {
	nk := newBuiltinNk()
	in := &api.JoinGroupRequest{GroupId: biaOpenCrew}
	out, err := BeforeJoinGroup(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, in)
	if err != nil || out != in {
		t.Fatalf("join of an open crew: out=%v err=%v, want the request and no error", out, err)
	}
}

// Nakama answers for a crew that does not exist and for an id that is not
// an id. The hook does not answer in its place.
func TestBuiltinJoinLeavesAnUnknownCrewToNakama(t *testing.T) {
	nk := newBuiltinNk()
	for _, id := range []string{biaUnknownCrew, "not-an-id", ""} {
		in := &api.JoinGroupRequest{GroupId: id}
		out, err := BeforeJoinGroup(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, in)
		if err != nil || out != in {
			t.Errorf("join of %q: out=%v err=%v, want the request and no error", id, out, err)
		}
	}
}

// When the crew cannot be read the hook does not know if it is open. It
// refuses, so no join request row is made on an invite-only crew.
func TestBuiltinJoinRefusesWhenTheCrewReadFails(t *testing.T) {
	nk := newBuiltinNk()
	nk.failReads = true
	out, err := BeforeJoinGroup(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, &api.JoinGroupRequest{GroupId: biaOpenCrew})
	if err == nil || out != nil {
		t.Fatalf("join while the crew read fails: out=%v err=%v, want an error and no request", out, err)
	}
}

func TestBuiltinJoinRefusesWithoutSession(t *testing.T) {
	out, err := BeforeJoinGroup(context.Background(), noopLogger{}, nil, newBuiltinNk(), &api.JoinGroupRequest{GroupId: biaOpenCrew})
	wantBuiltinRefused(t, "join with no session", out == nil, err)
}

// ---- member list ----

func TestBuiltinMemberListIsForMembers(t *testing.T) {
	nk := newBuiltinNk()
	for _, caller := range []string{biaOwner, biaAdmin, biaMember} {
		in := &api.ListGroupUsersRequest{GroupId: biaClosedCrew}
		out, err := BeforeListGroupUsers(ctxWithUser(caller), noopLogger{}, nil, nk, in)
		if err != nil || out != in {
			t.Errorf("member list for %s: out=%v err=%v, want the request and no error", caller, out, err)
		}
	}
	for _, caller := range []string{biaRequester, biaBanned, biaOutsider} {
		out, err := BeforeListGroupUsers(ctxWithUser(caller), noopLogger{}, nil, nk, &api.ListGroupUsersRequest{GroupId: biaClosedCrew})
		wantBuiltinRefused(t, "member list of the invite-only crew for "+caller, out == nil, err)
	}
	// An open crew too: a visitor of Discover gets the crew from discover_crews.
	out, err := BeforeListGroupUsers(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, &api.ListGroupUsersRequest{GroupId: biaOpenCrew})
	wantBuiltinRefused(t, "member list of an open crew for a user who is not in it", out == nil, err)
	// The answer is the same for a crew that does not exist.
	out, err = BeforeListGroupUsers(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, &api.ListGroupUsersRequest{GroupId: biaUnknownCrew})
	wantBuiltinRefused(t, "member list of a crew that does not exist", out == nil, err)
}

func TestBuiltinMemberListRefusesWhenTheMembershipReadFails(t *testing.T) {
	nk := newBuiltinNk()
	nk.failReads = true
	out, err := BeforeListGroupUsers(ctxWithUser(biaMember), noopLogger{}, nil, nk, &api.ListGroupUsersRequest{GroupId: biaClosedCrew})
	if err == nil || out != nil {
		t.Fatalf("member list while the membership read fails: out=%v err=%v, want an error and no request", out, err)
	}
}

// ---- crews of a user ----

func TestBuiltinCrewListIsForTheCallerOnly(t *testing.T) {
	nk := newBuiltinNk()
	in := &api.ListUserGroupsRequest{UserId: biaMember}
	out, err := BeforeListUserGroups(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
	if err != nil || out != in {
		t.Fatalf("own crew list: out=%v err=%v, want the request and no error", out, err)
	}
	for _, target := range []string{biaOwner, ""} {
		out, err := BeforeListUserGroups(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, &api.ListUserGroupsRequest{UserId: target})
		wantBuiltinRefused(t, "crew list of user "+target, out == nil, err)
	}
	// A crew mate too: the client lists only its own crews.
	out, err = BeforeListUserGroups(ctxWithUser(biaMember), noopLogger{}, nil, nk, &api.ListUserGroupsRequest{UserId: biaOwner})
	wantBuiltinRefused(t, "crew list of a crew mate", out == nil, err)
}

// ---- group list ----

func TestBuiltinGroupListGivesOpenCrewsOnly(t *testing.T) {
	nk := newBuiltinNk()
	for _, in := range []*api.ListGroupsRequest{
		{},
		{Open: wrapperspb.Bool(true)},
		{Open: wrapperspb.Bool(false)},
		{Members: wrapperspb.Int32(3), LangTag: "en"},
	} {
		out, err := BeforeListGroups(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, in)
		if err != nil || out == nil {
			t.Fatalf("group list %v: out=%v err=%v, want a request and no error", in, out, err)
		}
		if out.GetOpen() == nil || !out.GetOpen().GetValue() {
			t.Errorf("group list %v: the open filter is %v, want true", in, out.GetOpen())
		}
		if out.GetName() != "" {
			t.Errorf("group list %v: the hook set a name filter %q", in, out.GetName())
		}
	}
}

// namesFound runs the list that the hook passes on and returns the names of
// the groups that Nakama would answer with.
func namesFound(t *testing.T, nk *builtinNk, out *api.ListGroupsRequest) []string {
	t.Helper()
	if out.GetOpen() != nil {
		t.Errorf("a list by name has the open filter set: Nakama refuses a name with another filter")
	}
	groups, _, err := nk.GroupsList(context.Background(), out.GetName(), "", nil, nil, 100, "")
	if err != nil {
		t.Fatalf("GroupsList: %v", err)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.GetName())
	}
	sort.Strings(names)
	return names
}

func TestBuiltinGroupListByNameFindsAnOpenCrewOrACrewOfTheCaller(t *testing.T) {
	cases := []struct {
		what, caller, name string
		want               []string
	}{
		{"an open crew, for a user who is not in it", biaOutsider, "Open Crew", []string{"Open Crew"}},
		{"an open crew, in another case", biaOutsider, "open crew", []string{"Open Crew"}},
		{"an invite-only crew, for a member", biaMember, "Closed_Crew", []string{"Closed_Crew"}},
		{"an invite-only crew, for the owner", biaOwner, "Closed_Crew", []string{"Closed_Crew"}},
		{"an invite-only crew, for a user who is not in it", biaOutsider, "Closed_Crew", nil},
		{"an invite-only crew, for a pending join request", biaRequester, "Closed_Crew", nil},
		{"an invite-only crew, for a banned user", biaBanned, "Closed_Crew", nil},
		{"a name that no crew has", biaOutsider, "No Such Crew", nil},
	}
	for _, c := range cases {
		nk := newBuiltinNk()
		out, err := BeforeListGroups(ctxWithUser(c.caller), noopLogger{}, nil, nk, &api.ListGroupsRequest{Name: c.name})
		if err != nil || out == nil {
			t.Errorf("%s: out=%v err=%v, want a request and no error", c.what, out, err)
			continue
		}
		got := namesFound(t, nk, out)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: the list finds %v, want %v", c.what, got, c.want)
		}
		if len(nk.wildcardNames) > 0 {
			t.Errorf("%s: a name with a wildcard reached the group list: %q", c.what, nk.wildcardNames)
		}
	}
}

// The name filter of Nakama takes % and _ as wildcards. The hook passes them
// on as plain characters, so a name finds one crew by its full name.
func TestBuiltinGroupListByNameTakesNoWildcard(t *testing.T) {
	for _, name := range []string{"%", "Closed%", "%Crew", "Closed_Cre_", "______ ____", `Closed\%`, `%\`} {
		nk := newBuiltinNk()
		out, err := BeforeListGroups(ctxWithUser(biaMember), noopLogger{}, nil, nk, &api.ListGroupsRequest{Name: name})
		if err != nil || out == nil {
			t.Errorf("list by name %q: out=%v err=%v, want a request and no error", name, out, err)
			continue
		}
		if got := namesFound(t, nk, out); len(got) != 0 {
			t.Errorf("list by name %q finds %v, want nothing", name, got)
		}
		if len(nk.wildcardNames) > 0 {
			t.Errorf("list by name %q: a wildcard reached the group list: %q", name, nk.wildcardNames)
		}
	}
}

func TestBuiltinGroupListByNameRefusesWhenTheCrewReadFails(t *testing.T) {
	nk := newBuiltinNk()
	nk.failReads = true
	out, err := BeforeListGroups(ctxWithUser(biaMember), noopLogger{}, nil, nk, &api.ListGroupsRequest{Name: "Closed_Crew"})
	if err == nil || out != nil {
		t.Fatalf("list by name while the crew read fails: out=%v err=%v, want an error and no request", out, err)
	}
}

// ---- users ----

func TestBuiltinUserLookupIsByIDOnly(t *testing.T) {
	nk := newBuiltinNk()
	in := &api.GetUsersRequest{Ids: []string{biaOwner}}
	out, err := BeforeGetUsers(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
	if err != nil || out != in {
		t.Fatalf("user lookup by id: out=%v err=%v, want the request and no error", out, err)
	}
	for _, in := range []*api.GetUsersRequest{
		{Usernames: []string{"someone"}},
		{FacebookIds: []string{"1"}},
		{Ids: []string{biaOwner}, Usernames: []string{"someone"}},
	} {
		out, err := BeforeGetUsers(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
		wantBuiltinRefused(t, "user lookup "+in.String(), out == nil, err)
	}
}

func socialUser(id string) *api.User {
	return &api.User{
		Id: id, Username: "name-" + id, DisplayName: "Name", Metadata: `{"tag":"#1"}`, Online: true,
		AppleId: "apple", GoogleId: "google", SteamId: "steam", FacebookId: "fb", FacebookInstantGameId: "fbig", GamecenterId: "gc",
	}
}

func wantNoSocialID(t *testing.T, what string, u *api.User) {
	t.Helper()
	if ids := u.GetAppleId() + u.GetGoogleId() + u.GetSteamId() + u.GetFacebookId() + u.GetFacebookInstantGameId() + u.GetGamecenterId(); ids != "" {
		t.Errorf("%s: the user still has a provider id: %q", what, ids)
	}
	if u.GetId() == "" || u.GetUsername() == "" || u.GetDisplayName() == "" || u.GetMetadata() == "" || !u.GetOnline() {
		t.Errorf("%s: a field that the client reads is gone: %v", what, u)
	}
}

func TestBuiltinUserAnswersCarryNoProviderID(t *testing.T) {
	users := &api.Users{Users: []*api.User{socialUser(biaOwner), socialUser(biaMember)}}
	if err := AfterGetUsers(ctxWithUser(biaMember), noopLogger{}, nil, nil, users, &api.GetUsersRequest{}); err != nil {
		t.Fatalf("AfterGetUsers: %v", err)
	}
	for _, u := range users.GetUsers() {
		wantNoSocialID(t, "user lookup", u)
	}

	members := &api.GroupUserList{GroupUsers: []*api.GroupUserList_GroupUser{
		{User: socialUser(biaOwner), State: wrapperspb.Int32(groupStateSuperadmin)},
		{User: socialUser(biaMember), State: wrapperspb.Int32(groupStateMember)},
		{State: wrapperspb.Int32(groupStateMember)},
	}}
	if err := AfterListGroupUsers(ctxWithUser(biaMember), noopLogger{}, nil, nil, members, &api.ListGroupUsersRequest{}); err != nil {
		t.Fatalf("AfterListGroupUsers: %v", err)
	}
	for _, gu := range members.GetGroupUsers()[:2] {
		wantNoSocialID(t, "member list", gu.GetUser())
	}
	if got := members.GetGroupUsers()[0].GetState().GetValue(); got != groupStateSuperadmin {
		t.Errorf("member list: the state of the owner is %d, want %d", got, groupStateSuperadmin)
	}
	// An empty answer is not an error.
	if err := AfterGetUsers(ctxWithUser(biaMember), noopLogger{}, nil, nil, nil, nil); err != nil {
		t.Errorf("AfterGetUsers with no answer: %v", err)
	}
}

// ---- chat history ----

func TestBuiltinChatHistoryIsForCrewChannelsOnly(t *testing.T) {
	nk := newBuiltinNk()
	in := &api.ListChannelMessagesRequest{ChannelId: "3." + biaClosedCrew + ".."}
	out, err := BeforeListChannelMessages(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
	if err != nil || out != in {
		t.Fatalf("history of a crew channel: out=%v err=%v, want the request and no error", out, err)
	}
	for _, id := range []string{"2...some-room", "4." + biaOwner + "." + biaMember + ".", "", "3", "33." + biaClosedCrew + ".."} {
		out, err := BeforeListChannelMessages(ctxWithUser(biaMember), noopLogger{}, nil, nk, &api.ListChannelMessagesRequest{ChannelId: id})
		wantBuiltinRefused(t, "history of channel "+id, out == nil, err)
	}
}

// ---- realtime ----

func channelJoin(target string, kind rtapi.ChannelJoin_Type) *rtapi.Envelope {
	return &rtapi.Envelope{Message: &rtapi.Envelope_ChannelJoin{ChannelJoin: &rtapi.ChannelJoin{Target: target, Type: int32(kind)}}}
}

func TestBuiltinChannelJoinIsForCrewChannelsOnly(t *testing.T) {
	nk := newBuiltinNk()
	in := channelJoin(biaClosedCrew, rtapi.ChannelJoin_GROUP)
	out, err := BeforeRtChannelJoin(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
	if err != nil || out != in {
		t.Fatalf("join of a crew channel: out=%v err=%v, want the envelope and no error", out, err)
	}
	for _, in := range []*rtapi.Envelope{
		channelJoin("some-room", rtapi.ChannelJoin_ROOM),
		channelJoin(biaOwner, rtapi.ChannelJoin_DIRECT_MESSAGE),
		channelJoin("x", rtapi.ChannelJoin_TYPE_UNSPECIFIED),
	} {
		out, err := BeforeRtChannelJoin(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
		wantBuiltinRefused(t, "channel join "+in.GetChannelJoin().String(), out == nil, err)
	}
}

func statusFollow(ids []string, usernames []string) *rtapi.Envelope {
	return &rtapi.Envelope{Message: &rtapi.Envelope_StatusFollow{StatusFollow: &rtapi.StatusFollow{UserIds: ids, Usernames: usernames}}}
}

func sortedIDs(ids []string) string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return strings.Join(out, ",")
}

// The client follows the ids of its member list. The hook keeps the members
// of the crews of the caller and drops every other id, so the follow of the
// crew mates still works when one id of the request is not a crew mate.
func TestBuiltinStatusFollowKeepsCrewMatesOnly(t *testing.T) {
	nk := newBuiltinNk()
	in := statusFollow([]string{biaOwner, biaAdmin, biaMember, biaRequester, biaBanned, biaOutsider}, nil)
	out, err := BeforeRtStatusFollow(ctxWithUser(biaMember), noopLogger{}, nil, nk, in)
	if err != nil || out == nil {
		t.Fatalf("status follow by a member: out=%v err=%v, want an envelope and no error", out, err)
	}
	if got, want := sortedIDs(out.GetStatusFollow().GetUserIds()), sortedIDs([]string{biaOwner, biaAdmin, biaMember}); got != want {
		t.Errorf("status follow by a member keeps %s, want %s", got, want)
	}
}

// A user with no crew, a pending join request and a banned user have no crew
// mates. The follow is answered, with no id in it.
func TestBuiltinStatusFollowOfAUserWithNoCrewKeepsNoID(t *testing.T) {
	for _, caller := range []string{biaOutsider, biaRequester, biaBanned} {
		nk := newBuiltinNk()
		out, err := BeforeRtStatusFollow(ctxWithUser(caller), noopLogger{}, nil, nk, statusFollow([]string{biaOwner, biaMember}, nil))
		if err != nil || out == nil {
			t.Errorf("status follow by %s: out=%v err=%v, want an envelope and no error", caller, out, err)
			continue
		}
		if ids := out.GetStatusFollow().GetUserIds(); len(ids) != 0 {
			t.Errorf("status follow by %s keeps %v, want no id", caller, ids)
		}
	}
}

func TestBuiltinStatusFollowKeepsTheCaller(t *testing.T) {
	nk := newBuiltinNk()
	out, err := BeforeRtStatusFollow(ctxWithUser(biaOutsider), noopLogger{}, nil, nk, statusFollow([]string{biaOutsider, biaOwner}, nil))
	if err != nil || out == nil {
		t.Fatalf("status follow of the caller: out=%v err=%v", out, err)
	}
	if got := sortedIDs(out.GetStatusFollow().GetUserIds()); got != biaOutsider {
		t.Errorf("status follow keeps %s, want the caller only", got)
	}
}

func TestBuiltinStatusFollowRefusesUsernames(t *testing.T) {
	nk := newBuiltinNk()
	out, err := BeforeRtStatusFollow(ctxWithUser(biaMember), noopLogger{}, nil, nk, statusFollow([]string{biaOwner}, []string{"someone"}))
	wantBuiltinRefused(t, "status follow by username", out == nil, err)
}

func TestBuiltinStatusFollowRefusesWhenTheCrewReadFails(t *testing.T) {
	nk := newBuiltinNk()
	nk.failReads = true
	out, err := BeforeRtStatusFollow(ctxWithUser(biaMember), noopLogger{}, nil, nk, statusFollow([]string{biaOwner}, nil))
	if err == nil || out != nil {
		t.Fatalf("status follow while the crew read fails: out=%v err=%v, want an error and no envelope", out, err)
	}
}

// ---- the calls that no client makes ----

func TestBuiltinRefusedCallsAnswerPermissionDenied(t *testing.T) {
	out, err := refuseBuiltinCall[*api.AddFriendsRequest]("AddFriends")(ctxWithUser(biaOutsider), noopLogger{}, nil, nil, &api.AddFriendsRequest{Ids: []string{biaOwner}})
	wantBuiltinRefused(t, "AddFriends", out == nil, err)
	ban, err := refuseBuiltinCall[*api.BanGroupUsersRequest]("BanGroupUsers")(ctxWithUser(biaOwner), noopLogger{}, nil, nil, &api.BanGroupUsersRequest{GroupId: biaClosedCrew, UserIds: []string{biaMember}})
	wantBuiltinRefused(t, "BanGroupUsers", ban == nil, err)
	env, err := refuseBuiltinRt("MatchCreate")(ctxWithUser(biaOutsider), noopLogger{}, nil, nil, &rtapi.Envelope{Message: &rtapi.Envelope_MatchCreate{MatchCreate: &rtapi.MatchCreate{}}})
	wantBuiltinRefused(t, "MatchCreate", env == nil, err)
}

// The hooks do nothing until InitModule registers them. InitModule needs a
// full Nakama initializer, so this test reads the source.
func TestBuiltinHooksAreRegistered(t *testing.T) {
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(main), "registerBuiltinAPIHooks(initializer)") {
		t.Errorf("main.go does not call registerBuiltinAPIHooks(initializer)")
	}
	if !strings.Contains(string(main), "StartJoinRequestSweep(ctx, nk, logger)") {
		t.Errorf("main.go does not start the join request sweep")
	}
	src, err := os.ReadFile("builtin_api_hooks.go")
	if err != nil {
		t.Fatalf("read builtin_api_hooks.go: %v", err)
	}
	guarded := []string{"JoinGroup", "LeaveGroup", "ListGroupUsers", "ListUserGroups", "ListGroups", "GetUsers", "ListChannelMessages"}
	refused := []string{
		"UpdateGroup", "AddGroupUsers", "BanGroupUsers", "PromoteGroupUsers", "DemoteGroupUsers",
		"AddFriends", "BlockFriends", "DeleteFriends", "ImportFacebookFriends", "ImportSteamFriends", "ListFriends",
		"ListNotifications", "DeleteNotifications",
		"ListLeaderboardRecords", "ListLeaderboardRecordsAroundOwner", "WriteLeaderboardRecord", "DeleteLeaderboardRecord",
		"ListTournaments", "ListTournamentRecords", "ListTournamentRecordsAroundOwner", "WriteTournamentRecord", "DeleteTournamentRecord", "JoinTournament",
		"ListMatches", "ListSubscriptions", "GetSubscription",
		"ValidatePurchaseApple", "ValidatePurchaseGoogle", "ValidatePurchaseHuawei", "ValidatePurchaseFacebookInstant",
		"ValidateSubscriptionApple", "ValidateSubscriptionGoogle",
	}
	for _, call := range []string{"GetUsers", "ListGroupUsers"} {
		if !strings.Contains(string(src), "initializer.RegisterAfter"+call+"(") {
			t.Errorf("builtin_api_hooks.go does not register a hook after %s", call)
		}
	}
	for _, call := range append(guarded, refused...) {
		if !strings.Contains(string(src), "initializer.RegisterBefore"+call+"(") {
			t.Errorf("builtin_api_hooks.go does not register a hook before %s", call)
		}
	}
	for _, call := range refused {
		if !strings.Contains(string(src), `("`+call+`")`) {
			t.Errorf("builtin_api_hooks.go does not refuse %s by its name", call)
		}
	}
	messages := []string{
		"ChannelJoin", "StatusFollow", "StatusUnfollow", "StatusUpdate",
		"MatchCreate", "MatchJoin", "MatchLeave", "MatchmakerAdd", "MatchmakerRemove",
		"PartyCreate", "PartyJoin", "PartyLeave", "PartyPromote", "PartyAccept", "PartyRemove", "PartyClose",
		"PartyJoinRequestList", "PartyMatchmakerAdd", "PartyMatchmakerRemove",
	}
	for _, message := range messages {
		if !strings.Contains(string(src), `"`+message+`"`) {
			t.Errorf("builtin_api_hooks.go has no hook before the realtime message %s", message)
		}
	}
}

// ---- the sweep of old join requests ----

type fakeJoinRequests struct {
	nk *builtinNk
}

func (f fakeJoinRequests) PendingJoinRequests(_ context.Context, limit int) ([]pendingJoinRequest, error) {
	var out []pendingJoinRequest
	for crew, users := range f.nk.edges {
		for user, state := range users {
			if state == groupStateJoinRequest {
				out = append(out, pendingJoinRequest{CrewID: crew, UserID: user, UpdateTime: f.nk.requestTimes[[2]string{crew, user}]})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CrewID+out[i].UserID < out[j].CrewID+out[j].UserID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestJoinRequestSweepRemovesOnlyJoinRequests(t *testing.T) {
	nk := newBuiltinNk()
	nk.edges[biaOpenCrew]["req-2"] = groupStateJoinRequest
	nk.edges[biaClosedCrew]["req-3"] = groupStateJoinRequest

	rows, crews := sweepJoinRequests(context.Background(), noopLogger{}, nk, fakeJoinRequests{nk})
	if rows != 3 || crews != 2 {
		t.Errorf("sweep removed %d rows in %d crews, want 3 rows in 2 crews", rows, crews)
	}
	for crew, users := range nk.edges {
		for user, state := range users {
			if state == groupStateJoinRequest {
				t.Errorf("a join request stays: crew %s user %s", crew, user)
			}
		}
	}
	for _, user := range []string{biaOwner, biaAdmin, biaMember, biaBanned} {
		if _, ok := nk.edges[biaClosedCrew][user]; !ok {
			t.Errorf("the sweep removed the row of %s, which is not a join request", user)
		}
	}
	// A second pass finds nothing.
	if rows, crews := sweepJoinRequests(context.Background(), noopLogger{}, nk, fakeJoinRequests{nk}); rows != 0 || crews != 0 {
		t.Errorf("second sweep removed %d rows in %d crews, want none", rows, crews)
	}
}

// A row that the kick does not remove must not keep the sweep in a loop.
func TestJoinRequestSweepEndsWhenARowStays(t *testing.T) {
	nk := newBuiltinNk()
	nk.kickKeepsRows = true
	rows, _ := sweepJoinRequests(context.Background(), noopLogger{}, nk, fakeJoinRequests{nk})
	if rows != 0 {
		t.Errorf("sweep counts %d removed rows, want 0: the row stays", rows)
	}
	if len(nk.kicked) != 1 {
		t.Errorf("the sweep tried the row %d times, want 1", len(nk.kicked))
	}
}

// sweepLogRecorder is a logger that keeps each Info line in a trace.
type sweepLogRecorder struct {
	noopLogger
	trace *[]string
}

func (l sweepLogRecorder) Info(format string, v ...interface{}) {
	*l.trace = append(*l.trace, "info "+fmt.Sprintf(format, v...))
}

func TestJoinRequestSweepLogsEachRowBeforeItsKick(t *testing.T) {
	nk := newBuiltinNk()
	nk.edges[biaOpenCrew]["req-2"] = groupStateJoinRequest
	nk.edges[biaClosedCrew]["req-3"] = groupStateJoinRequest
	// The second time has an offset: the line gives the time in UTC.
	nk.requestTimes = map[[2]string]time.Time{
		{biaClosedCrew, biaRequester}: time.Date(2026, 3, 1, 10, 20, 30, 0, time.UTC),
		{biaOpenCrew, "req-2"}:        time.Date(2026, 4, 2, 9, 0, 0, 0, time.FixedZone("", 2*3600)),
		{biaClosedCrew, "req-3"}:      time.Date(2026, 5, 3, 23, 59, 59, 0, time.UTC),
	}
	want := map[[2]string]string{
		{biaClosedCrew, biaRequester}: "2026-03-01T10:20:30Z",
		{biaOpenCrew, "req-2"}:        "2026-04-02T07:00:00Z",
		{biaClosedCrew, "req-3"}:      "2026-05-03T23:59:59Z",
	}
	var trace []string
	nk.trace = &trace

	sweepJoinRequests(context.Background(), sweepLogRecorder{trace: &trace}, nk, fakeJoinRequests{nk})

	if len(nk.kicked) != len(want) {
		t.Fatalf("the sweep kicked %d rows, want %d", len(nk.kicked), len(want))
	}
	rowLines := 0
	for row, updateTime := range want {
		line := "info " + fmt.Sprintf(joinRequestSweepRowLog, row[0], row[1], updateTime)
		logged, kicked := -1, -1
		for i, entry := range trace {
			switch entry {
			case line:
				logged = i
				rowLines++
			case "kick " + row[0] + " " + row[1]:
				kicked = i
			}
		}
		switch {
		case logged < 0:
			t.Errorf("no log line for the row of crew %s user %s with the time %s; trace: %q", row[0], row[1], updateTime, trace)
		case kicked < 0:
			t.Errorf("no kick of the row of crew %s user %s", row[0], row[1])
		case logged > kicked:
			t.Errorf("the row of crew %s user %s was kicked before its log line", row[0], row[1])
		}
	}
	if rowLines != len(want) {
		t.Errorf("the sweep wrote %d row lines, want one line for each of the %d rows", rowLines, len(want))
	}
	for _, entry := range trace {
		for _, user := range []string{biaOwner, biaAdmin, biaMember, biaBanned} {
			if strings.HasPrefix(entry, "info ") && strings.Contains(entry, user) {
				t.Errorf("a log line names %s, which has no join request: %q", user, entry)
			}
		}
	}
}

// A row that stays after its kick is logged one time.
func TestJoinRequestSweepLogsARowThatStaysOneTime(t *testing.T) {
	nk := newBuiltinNk()
	nk.kickKeepsRows = true
	var trace []string
	nk.trace = &trace
	sweepJoinRequests(context.Background(), sweepLogRecorder{trace: &trace}, nk, fakeJoinRequests{nk})
	lines := 0
	for _, entry := range trace {
		if strings.HasPrefix(entry, "info ") && strings.Contains(entry, biaRequester) {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("the row that stays has %d log lines, want 1; trace: %q", lines, trace)
	}
}
