package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	hookCrewID   = "11111111-1111-4111-8111-111111111111"
	hookOutsider = "22222222-2222-4222-8222-222222222222"
	hookMember   = "33333333-3333-4333-8333-333333333333"
	hookOther    = "44444444-4444-4444-8444-444444444444"
)

// readAs runs the read hook for callerID with the given object ids.
func readAs(callerID string, ids ...*api.ReadStorageObjectId) (*api.ReadStorageObjectsRequest, error) {
	ctx := context.Background()
	if callerID != "" {
		ctx = ctxWithUser(callerID)
	}
	return BeforeReadStorageObjects(ctx, noopLogger{}, nil, nil, &api.ReadStorageObjectsRequest{ObjectIds: ids})
}

// objectID is one object id of a client read. An empty owner is the system
// user, as a client sends it.
func objectID(collection, key, owner string) *api.ReadStorageObjectId {
	return &api.ReadStorageObjectId{Collection: collection, Key: key, UserId: owner}
}

// wantRefused fails the test unless the hook refused the request with
// PERMISSION_DENIED and returned no request for Nakama to run.
func wantRefused(t *testing.T, what string, got interface{}, isNil bool, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: allowed, want refused", what)
	}
	if !isNil {
		t.Errorf("%s: the hook returned a request (%v) with its error", what, got)
	}
	rerr, ok := err.(*runtime.Error)
	if !ok {
		t.Fatalf("%s: error is %T, want *runtime.Error", what, err)
	}
	if rerr.Code != 7 {
		t.Errorf("%s: error code = %d (%q), want 7", what, rerr.Code, rerr.Message)
	}
}

// crewObjects are the objects of a crew, as a client names them.
func crewObjects() []*api.ReadStorageObjectId {
	return []*api.ReadStorageObjectId{
		objectID(InviteCodeCollection, "ABCD-EFGH", ""),
		objectID(VoiceChannelCollection, hookCrewID, ""),
		objectID(CrewClipsCollection, hookCrewID, ""),
		objectID(CrewEventsCollection, hookCrewID, ""),
		objectID(CrewRecapsCollection, hookCrewID, ""),
		objectID(CrewStreamSessionsCollection, hookCrewID, ""),
		objectID(ChatReactionsCollection, "message-1", ""),
		objectID(CrewAvatarCollection, hookCrewID, ""),
		objectID(StreamMetaCollection, hookCrewID, ""),
		objectID(ThumbnailCollection, "stream-1/latest", ""),
		objectID(GameIconCollection, "game-1", ""),
	}
}

func TestStorageListHookRefusesEveryClientList(t *testing.T) {
	lists := []*api.ListStorageObjectsRequest{
		{Collection: InviteCodeCollection},
		{Collection: CrewClipsCollection},
		{Collection: CrewEventsCollection},
		{Collection: PresenceCollection},
		{Collection: UserAvatarCollection},
		{Collection: UserAvatarCollection, UserId: hookOther},
		// The caller's own objects too: no client lists a collection.
		{Collection: "chat_read", UserId: hookOutsider},
	}
	for _, in := range lists {
		out, err := BeforeListStorageObjects(ctxWithUser(hookOutsider), noopLogger{}, nil, nil, in)
		wantRefused(t, "list "+in.GetCollection()+" owner "+in.GetUserId(), out, out == nil, err)
	}
}

func TestStorageReadHookRefusesCrewObjectForOutsider(t *testing.T) {
	for _, id := range crewObjects() {
		out, err := readAs(hookOutsider, id)
		wantRefused(t, "outsider reads "+id.GetCollection(), out, out == nil, err)
	}
}

// A member gets crew data from the RPCs. The storage API stays closed to a
// member too: the hook does not look at crew membership.
func TestStorageReadHookRefusesCrewObjectForMember(t *testing.T) {
	nk := newFakeGuestNk()
	nk.addMember(hookCrewID, hookMember, "Member")
	if !isCrewMember(context.Background(), nk, hookCrewID, hookMember) {
		t.Fatal("test setup: the caller is not a member of the crew")
	}
	for _, id := range crewObjects() {
		out, err := BeforeReadStorageObjects(ctxWithUser(hookMember), noopLogger{}, nil, nk,
			&api.ReadStorageObjectsRequest{ObjectIds: []*api.ReadStorageObjectId{id}})
		wantRefused(t, "member reads "+id.GetCollection(), out, out == nil, err)
	}
}

func TestStorageReadHookAllowsAvatarOfAnotherUser(t *testing.T) {
	// One avatar (mello-core read_storage).
	one := objectID(UserAvatarCollection, UserAvatarKey, hookOther)
	out, err := readAs(hookOutsider, one)
	if err != nil {
		t.Fatalf("avatar of another user: refused: %v", err)
	}
	if len(out.GetObjectIds()) != 1 || out.GetObjectIds()[0] != one {
		t.Errorf("avatar of another user: the hook changed the request: %v", out)
	}

	// Many avatars in one request (mello-core read_storage_batch).
	out, err = readAs(hookOutsider,
		objectID(UserAvatarCollection, UserAvatarKey, hookOther),
		objectID(UserAvatarCollection, UserAvatarKey, hookMember),
		objectID(UserAvatarCollection, UserAvatarKey, hookOutsider))
	if err != nil {
		t.Fatalf("avatars of three users: refused: %v", err)
	}
	if len(out.GetObjectIds()) != 3 {
		t.Errorf("avatars of three users: %d object ids pass, want 3", len(out.GetObjectIds()))
	}
}

func TestStorageReadHookAllowsOwnObject(t *testing.T) {
	// The chat read cursors of the caller, one key for each crew
	// (mello-core read_chat_cursors).
	out, err := readAs(hookMember,
		objectID("chat_read", hookCrewID, hookMember),
		objectID("chat_read", "55555555-5555-4555-8555-555555555555", hookMember))
	if err != nil {
		t.Fatalf("own chat cursors: refused: %v", err)
	}
	if len(out.GetObjectIds()) != 2 {
		t.Errorf("own chat cursors: %d object ids pass, want 2", len(out.GetObjectIds()))
	}

	for _, id := range []*api.ReadStorageObjectId{
		objectID(UserAvatarCollection, UserAvatarKey, hookMember),
		objectID(PresenceCollection, hookMember, hookMember),
		objectID(StreamCollection, hookCrewID, hookMember),
	} {
		if _, err := readAs(hookMember, id); err != nil {
			t.Errorf("own %s: refused: %v", id.GetCollection(), err)
		}
	}
}

func TestStorageReadHookRefusesObjectOfAnotherUser(t *testing.T) {
	for _, id := range []*api.ReadStorageObjectId{
		objectID(PresenceCollection, hookOther, hookOther),
		objectID(StreamCollection, hookCrewID, hookOther),
		objectID("chat_read", hookCrewID, hookOther),
		objectID(CrewLastSeenCollection, hookCrewID, hookOther),
	} {
		out, err := readAs(hookOutsider, id)
		wantRefused(t, "reads "+id.GetCollection()+" of another user", out, out == nil, err)
	}
}

// Only avatars/current of a user is open. Another key of the collection and
// an avatar object with no owner are not.
func TestStorageReadHookAvatarRuleIsExact(t *testing.T) {
	for _, id := range []*api.ReadStorageObjectId{
		objectID(UserAvatarCollection, "previous", hookOther),
		objectID(UserAvatarCollection, UserAvatarKey, ""),
		objectID("avatars_private", UserAvatarKey, hookOther),
		objectID(CrewAvatarCollection, UserAvatarKey, hookOther),
	} {
		out, err := readAs(hookOutsider, id)
		wantRefused(t, "reads "+id.GetCollection()+"/"+id.GetKey()+" owner "+id.GetUserId(), out, out == nil, err)
	}
}

// One object that is not allowed refuses the full request.
func TestStorageReadHookRefusesMixedRequest(t *testing.T) {
	out, err := readAs(hookOutsider,
		objectID(UserAvatarCollection, UserAvatarKey, hookOther),
		objectID(CrewClipsCollection, hookCrewID, ""),
		objectID("chat_read", hookCrewID, hookOutsider))
	wantRefused(t, "avatar, crew clips and own cursor in one request", out, out == nil, err)
}

func TestStorageReadHookRefusesWithoutSession(t *testing.T) {
	for _, id := range []*api.ReadStorageObjectId{
		objectID(UserAvatarCollection, UserAvatarKey, hookOther),
		objectID(CrewClipsCollection, hookCrewID, ""),
		// An empty caller must not match the empty owner of a system object.
		objectID(InviteCodeCollection, "ABCD-EFGH", ""),
	} {
		out, err := readAs("", id)
		wantRefused(t, "no session reads "+id.GetCollection(), out, out == nil, err)
	}
}

// A read with no object ids names nothing, so the hook lets Nakama answer it.
func TestStorageReadHookPassesEmptyRequest(t *testing.T) {
	if _, err := readAs(hookOutsider); err != nil {
		t.Errorf("empty request: refused: %v", err)
	}
}

// The server reads storage through the runtime module, which does not pass
// the hooks. The same caller and the same invite code: the client read is
// refused, the server read of join_by_invite_code and resolve_crew_invite
// (lookupInviteCode) finds the crew.
func TestRuntimeReadDoesNotPassTheStorageHook(t *testing.T) {
	nk := newFakeGuestNk()
	ctx := ctxWithUser(hookOutsider)

	code, err := GenerateInviteCode(ctx, nk, noopLogger{}, hookCrewID, hookMember)
	if err != nil {
		t.Fatalf("GenerateInviteCode: %v", err)
	}
	key := normalizeInviteCode(code)

	out, err := BeforeReadStorageObjects(ctx, noopLogger{}, nil, nk,
		&api.ReadStorageObjectsRequest{ObjectIds: []*api.ReadStorageObjectId{objectID(InviteCodeCollection, key, "")}})
	wantRefused(t, "client read of the invite code", out, out == nil, err)

	crewID, inviterID, err := lookupInviteCode(ctx, nk, key)
	if err != nil {
		t.Fatalf("server read of the invite code: %v", err)
	}
	if crewID != hookCrewID || inviterID != hookMember {
		t.Errorf("server read = crew %q inviter %q, want %q and %q", crewID, inviterID, hookCrewID, hookMember)
	}

	// The same holds for a crew document: the feed RPCs read it this way.
	if err := saveVoiceChannels(ctx, nk, hookCrewID, &VoiceChannelList{Channels: []*VoiceChannelDef{{ID: "ch_1", Name: "General", IsDefault: true}}}); err != nil {
		t.Fatalf("saveVoiceChannels: %v", err)
	}
	list, err := GetVoiceChannels(ctx, nk, hookCrewID)
	if err != nil || len(list.Channels) != 1 || list.Channels[0].Name != "General" {
		t.Errorf("server read of the voice channels = %+v, %v", list, err)
	}
}

// The hooks do nothing until InitModule registers them. InitModule needs a
// full Nakama initializer, so this test reads the source.
func TestStorageHooksAreRegistered(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	for _, call := range []string{
		"initializer.RegisterBeforeReadStorageObjects(BeforeReadStorageObjects)",
		"initializer.RegisterBeforeListStorageObjects(BeforeListStorageObjects)",
	} {
		if !strings.Contains(string(src), call) {
			t.Errorf("main.go does not register the hook: %s", call)
		}
	}
}
