package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	swCrew      = "cccccccc-0000-4000-8000-000000000001"
	swOtherCrew = "cccccccc-0000-4000-8000-000000000002"
	swMember    = "dddddddd-0000-4000-8000-000000000001"
	swOutsider  = "dddddddd-0000-4000-8000-000000000002"
)

// storageWriteNk is an in-memory runtime.NakamaModule for the write hooks:
// the members of each crew. A call to any other method panics on the nil
// embedded interface.
type storageWriteNk struct {
	runtime.NakamaModule

	members map[string][]string // crew id -> user ids
	// memberReads counts the member list reads.
	memberReads int
	failReads   bool
}

func newStorageWriteNk() *storageWriteNk {
	return &storageWriteNk{members: map[string][]string{
		swCrew:      {swMember},
		swOtherCrew: {swOutsider},
	}}
}

func (n *storageWriteNk) GroupUsersList(_ context.Context, id string, _ int, _ *int, _ string) ([]*api.GroupUserList_GroupUser, string, error) {
	n.memberReads++
	if n.failReads {
		return nil, "", runtime.NewError("database is down", 13)
	}
	var out []*api.GroupUserList_GroupUser
	for _, user := range n.members[id] {
		out = append(out, &api.GroupUserList_GroupUser{User: &api.User{Id: user}, State: wrapperspb.Int32(groupStateMember)})
	}
	return out, "", nil
}

// swObject builds one object of a client write. A permission below zero
// leaves the field out of the request.
func swObject(collection, key, value string, read, write int32) *api.WriteStorageObject {
	o := &api.WriteStorageObject{Collection: collection, Key: key, Value: value}
	if read >= 0 {
		o.PermissionRead = wrapperspb.Int32(read)
	}
	if write >= 0 {
		o.PermissionWrite = wrapperspb.Int32(write)
	}
	return o
}

func swWrite(objects ...*api.WriteStorageObject) *api.WriteStorageObjectsRequest {
	return &api.WriteStorageObjectsRequest{Objects: objects}
}

// swJSON encodes a value as the clients do.
func swJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(data)
}

// The avatar values that the clients write (mello-core, client/auth.rs and
// client/presence.rs, at every release from v0.2.6; qa/journeys/lib/seed.ts).
func swAvatarValues(t *testing.T) map[string]string {
	t.Helper()
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#6E8FB8"/></svg>`
	return map[string]string{
		"a generated avatar":            swJSON(t, map[string]string{"format": "svg", "data": svg, "style": "adventurer-neutral", "seed": "a7f3c9e1-4b2d-4f8a-9c3e-1d5f7a8b2c4d_r3_2"}),
		"an uploaded image":             swJSON(t, map[string]string{"format": "png", "data": "iVBORw0KGgoAAAANSUhEUgAAAQAAAAEACAYAAABccqhm"}),
		"an svg with no style and seed": swJSON(t, map[string]string{"format": "svg", "data": svg}),
	}
}

func swCursorValue(t *testing.T) string {
	t.Helper()
	return swJSON(t, map[string]string{"message_id": "eeeeeeee-0000-4000-8000-000000000001", "time": "2026-11-24T21:55:01Z"})
}

// wantWriteRefused fails the test unless the hook refused the write with the
// given code and returned no request for Nakama to run.
func wantWriteRefused(t *testing.T, what string, out *api.WriteStorageObjectsRequest, err error, code int) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: allowed, want refused", what)
		return
	}
	if out != nil {
		t.Errorf("%s: the hook returned a request with its error", what)
	}
	rerr, ok := err.(*runtime.Error)
	if !ok {
		t.Errorf("%s: error is %T, want *runtime.Error", what, err)
		return
	}
	if int(rerr.Code) != code {
		t.Errorf("%s: error code = %d (%q), want %d", what, rerr.Code, rerr.Message, code)
	}
}

func wantWriteAllowed(t *testing.T, what string, in, out *api.WriteStorageObjectsRequest, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: refused: %v", what, err)
		return
	}
	if out != in {
		t.Errorf("%s: the hook returned another request", what)
	}
}

// ---- writes that the clients make ----

func TestStorageWriteHookAllowsTheAvatarOfTheCaller(t *testing.T) {
	for name, value := range swAvatarValues(t) {
		nk := newStorageWriteNk()
		in := swWrite(swObject("avatars", "current", value, 2, 1))
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk, in)
		wantWriteAllowed(t, name, in, out, err)
		if nk.memberReads != 0 {
			t.Errorf("%s: the avatar write made %d member reads, want none", name, nk.memberReads)
		}
	}
	// A user in no crew writes an avatar: onboarding writes it before the join.
	in := swWrite(swObject("avatars", "current", swAvatarValues(t)["a generated avatar"], 2, 1))
	out, err := BeforeWriteStorageObjects(ctxWithUser("dddddddd-0000-4000-8000-00000000000f"), noopLogger{}, nil, newStorageWriteNk(), in)
	wantWriteAllowed(t, "a user in no crew", in, out, err)
}

func TestStorageWriteHookAllowsTheChatCursorOfAMember(t *testing.T) {
	nk := newStorageWriteNk()
	in := swWrite(swObject("chat_read", swCrew, swCursorValue(t), 1, 1))
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk, in)
	wantWriteAllowed(t, "the cursor of a member", in, out, err)

	// A version on the write does not change the answer.
	in = swWrite(swObject("chat_read", swCrew, swCursorValue(t), 1, 1))
	in.Objects[0].Version = "*"
	out, err = BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk, in)
	wantWriteAllowed(t, "the cursor with a version", in, out, err)
}

func TestStorageWriteHookAllowsTheLargestAvatar(t *testing.T) {
	head := `{"format":"png","data":"`
	tail := `"}`
	fill := clientAvatarMaxBytes - len(head) - len(tail)
	value := head + strings.Repeat("A", fill) + tail
	if len(value) != clientAvatarMaxBytes {
		t.Fatalf("test value is %d bytes, want %d", len(value), clientAvatarMaxBytes)
	}
	in := swWrite(swObject("avatars", "current", value, 2, 1))
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), in)
	wantWriteAllowed(t, "an avatar of the largest size", in, out, err)

	value = head + strings.Repeat("A", fill+1) + tail
	out, err = BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), swWrite(swObject("avatars", "current", value, 2, 1)))
	wantWriteRefused(t, "an avatar of one byte more", out, err, 3)
}

// ---- writes that no client makes ----

func TestStorageWriteHookRefusesEveryOtherCollection(t *testing.T) {
	// Collection names of the server, names close to the two that a client
	// writes, and a name that the server does not use.
	collections := []string{
		"user_plus", "user_game_stats", "user_activity_stats", "user_library",
		"push_tokens", "push_token_owners", "push_prefs", "presence", "riot_account",
		"looking_to_play_picks", "crew_last_seen", "game_session_dedup", "active_streams",
		"crew_events", "crew_head2head", "invite_codes", "stream_meta", "voice_channels",
		"Avatars", "avatars ", "chat_read2", "my_notes", "",
	}
	values := []string{`{}`, swCursorValue(t), swAvatarValues(t)["a generated avatar"]}
	for _, collection := range collections {
		for _, key := range []string{"current", swCrew, swMember, "stats"} {
			for _, value := range values {
				for _, perm := range [][2]int32{{2, 1}, {1, 1}, {0, 0}, {2, 0}} {
					out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(),
						swWrite(swObject(collection, key, value, perm[0], perm[1])))
					wantWriteRefused(t, "collection "+collection+" key "+key, out, err, 7)
				}
			}
		}
	}
}

func TestStorageWriteHookTakesOneAvatarKey(t *testing.T) {
	value := swAvatarValues(t)["a generated avatar"]
	for _, key := range []string{"Current", "current2", "old", swMember, swCrew, ""} {
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(),
			swWrite(swObject("avatars", key, value, 2, 1)))
		wantWriteRefused(t, "avatars key "+key, out, err, 7)
	}
}

func TestStorageWriteHookChecksThePermissionsOfTheAvatar(t *testing.T) {
	value := swAvatarValues(t)["a generated avatar"]
	for _, perm := range [][2]int32{{2, 0}, {1, 1}, {0, 1}, {1, 0}, {0, 0}, {-1, -1}, {2, -1}, {-1, 1}, {3, 1}, {2, 2}} {
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(),
			swWrite(swObject("avatars", "current", value, perm[0], perm[1])))
		wantWriteRefused(t, "avatar permissions", out, err, 7)
	}
}

func TestStorageWriteHookChecksTheValueOfTheAvatar(t *testing.T) {
	long := strings.Repeat("x", clientAvatarTagMaxChars+1)
	bad := map[string]string{
		"no format":             `{"data":"<svg/>"}`,
		"a format of no client": `{"format":"gif","data":"R0lGOD"}`,
		"a format in capitals":  `{"format":"SVG","data":"<svg/>"}`,
		"a format that is null": `{"format":null,"data":"<svg/>"}`,
		"no data":               `{"format":"svg"}`,
		"empty data":            `{"format":"svg","data":""}`,
		"data that is a number": `{"format":"svg","data":7}`,
		"data that is an array": `{"format":"svg","data":["<svg/>"]}`,
		"data that is null":     `{"format":"png","data":null}`,
		"one more field":        `{"format":"svg","data":"<svg/>","url":"https://example.com/a.svg"}`,
		"a style that is long":  `{"format":"svg","data":"<svg/>","style":"` + long + `"}`,
		"a seed that is long":   `{"format":"svg","data":"<svg/>","seed":"` + long + `"}`,
		"a seed that is a list": `{"format":"svg","data":"<svg/>","seed":["a"]}`,
		"a seed that is null":   `{"format":"svg","data":"<svg/>","seed":null}`,
		"a style that is null":  `{"format":"svg","data":"<svg/>","style":null}`,
		"an empty object":       `{}`,
		"a list":                `[{"format":"svg","data":"<svg/>"}]`,
		"a text":                `"svg"`,
		"null":                  `null`,
		"not JSON":              `{"format":"svg","data":"<svg/>"`,
		"an empty value":        ``,
	}
	for name, value := range bad {
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(),
			swWrite(swObject("avatars", "current", value, 2, 1)))
		wantWriteRefused(t, "avatar with "+name, out, err, 3)
	}
	// The style and the seed at the limit pass.
	edge := strings.Repeat("x", clientAvatarTagMaxChars)
	in := swWrite(swObject("avatars", "current", `{"format":"svg","data":"<svg/>","style":"`+edge+`","seed":"`+edge+`"}`, 2, 1))
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), in)
	wantWriteAllowed(t, "a style and a seed at the limit", in, out, err)
}

func TestStorageWriteHookRefusesTheChatCursorOfACrewOfOthers(t *testing.T) {
	nk := newStorageWriteNk()
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk,
		swWrite(swObject("chat_read", swOtherCrew, swCursorValue(t), 1, 1)))
	wantWriteRefused(t, "the cursor of a crew of others", out, err, 7)

	out, err = BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk,
		swWrite(swObject("chat_read", "cccccccc-0000-4000-8000-00000000000f", swCursorValue(t), 1, 1)))
	wantWriteRefused(t, "the cursor of a crew that does not exist", out, err, 7)
}

func TestStorageWriteHookRefusesTheChatCursorWhenTheMemberReadFails(t *testing.T) {
	nk := newStorageWriteNk()
	nk.failReads = true
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk,
		swWrite(swObject("chat_read", swCrew, swCursorValue(t), 1, 1)))
	wantWriteRefused(t, "the cursor with a failed member read", out, err, 7)
}

func TestStorageWriteHookTakesACrewIDAsTheChatCursorKey(t *testing.T) {
	keys := []string{
		"current", "stats", "", swCrew + "x", swCrew[:35], strings.ToUpper(swCrew)[:35] + "G",
		"cccccccc_0000_4000_8000_000000000001", "cccccccc-0000-4000-8000-00000000000g",
		strings.Repeat("c", 36), " " + swCrew[1:],
	}
	for _, key := range keys {
		nk := newStorageWriteNk()
		// The caller is a member of a crew with this key, so only the form
		// of the key can refuse the write.
		nk.members[key] = []string{swMember}
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk,
			swWrite(swObject("chat_read", key, swCursorValue(t), 1, 1)))
		wantWriteRefused(t, "chat_read key "+key, out, err, 7)
		if nk.memberReads != 0 {
			t.Errorf("chat_read key %q: the hook read the members %d times, want none", key, nk.memberReads)
		}
	}
}

func TestStorageWriteHookChecksThePermissionsOfTheChatCursor(t *testing.T) {
	for _, perm := range [][2]int32{{2, 1}, {0, 1}, {1, 0}, {2, 0}, {0, 0}, {-1, -1}, {1, -1}, {-1, 1}} {
		nk := newStorageWriteNk()
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk,
			swWrite(swObject("chat_read", swCrew, swCursorValue(t), perm[0], perm[1])))
		wantWriteRefused(t, "cursor permissions", out, err, 7)
	}
}

func TestStorageWriteHookChecksTheValueOfTheChatCursor(t *testing.T) {
	long := strings.Repeat("9", clientChatCursorFieldMaxChars+1)
	bad := map[string]string{
		"no time":                  `{"message_id":"m1"}`,
		"no message id":            `{"time":"2026-11-24T21:55:01Z"}`,
		"a message id of a number": `{"message_id":5,"time":"2026-11-24T21:55:01Z"}`,
		"a time that is null":      `{"message_id":"m1","time":null}`,
		"a long message id":        `{"message_id":"` + long + `","time":"t"}`,
		"a long time":              `{"message_id":"m1","time":"` + long + `"}`,
		"one more field":           `{"message_id":"m1","time":"t","note":"x"}`,
		"an empty object":          `{}`,
		"a list":                   `["m1","t"]`,
		"not JSON":                 `{"message_id":"m1"`,
		"a value that is large":    `{"message_id":"m1","time":"t"` + strings.Repeat(" ", clientChatCursorMaxBytes) + `}`,
	}
	for name, value := range bad {
		nk := newStorageWriteNk()
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk,
			swWrite(swObject("chat_read", swCrew, value, 1, 1)))
		wantWriteRefused(t, "cursor with "+name, out, err, 3)
		if nk.memberReads != 0 {
			t.Errorf("cursor with %s: the hook read the members before it checked the value", name)
		}
	}
	// Fields at the limit pass.
	edge := strings.Repeat("9", clientChatCursorFieldMaxChars)
	in := swWrite(swObject("chat_read", swCrew, `{"message_id":"`+edge+`","time":"`+edge+`"}`, 1, 1))
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), in)
	wantWriteAllowed(t, "cursor fields at the limit", in, out, err)
}

func TestStorageWriteHookTakesOneObjectInARequest(t *testing.T) {
	avatar := swObject("avatars", "current", swAvatarValues(t)["a generated avatar"], 2, 1)
	cursor := swObject("chat_read", swCrew, swCursorValue(t), 1, 1)
	nk := newStorageWriteNk()
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, nk, swWrite(avatar, cursor))
	wantWriteRefused(t, "two objects that are each allowed", out, err, 7)
	if nk.memberReads != 0 {
		t.Errorf("a request of two objects made %d member reads, want none", nk.memberReads)
	}
	out, err = BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(),
		swWrite(avatar, swObject("user_game_stats", "cs2", `{"wins":9}`, 1, 1)))
	wantWriteRefused(t, "an allowed object with one that is not", out, err, 7)
}

func TestStorageWriteHookRefusesWithoutSession(t *testing.T) {
	in := swWrite(swObject("avatars", "current", swAvatarValues(t)["a generated avatar"], 2, 1))
	out, err := BeforeWriteStorageObjects(context.Background(), noopLogger{}, nil, newStorageWriteNk(), in)
	wantWriteRefused(t, "a write with no session", out, err, 7)
	out, err = BeforeWriteStorageObjects(context.Background(), noopLogger{}, nil, newStorageWriteNk(), swWrite())
	wantWriteRefused(t, "an empty write with no session", out, err, 7)
}

func TestStorageWriteHookRefusesANilObject(t *testing.T) {
	out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), swWrite(nil))
	wantWriteRefused(t, "a nil object", out, err, 7)
}

func TestStorageWriteHookPassesAnEmptyRequest(t *testing.T) {
	for _, in := range []*api.WriteStorageObjectsRequest{swWrite(), {}} {
		out, err := BeforeWriteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), in)
		wantWriteAllowed(t, "an empty request", in, out, err)
	}
}

// ---- deletes ----

func TestStorageDeleteHookRefusesEveryDelete(t *testing.T) {
	ids := [][2]string{
		{"avatars", "current"}, {"chat_read", swCrew}, {"crew_last_seen", swCrew},
		{"user_game_stats", "cs2"}, {"push_tokens", "k"}, {"my_notes", "a"},
	}
	for _, id := range ids {
		for _, caller := range []string{swMember, swOutsider} {
			in := &api.DeleteStorageObjectsRequest{ObjectIds: []*api.DeleteStorageObjectId{{Collection: id[0], Key: id[1]}}}
			out, err := BeforeDeleteStorageObjects(ctxWithUser(caller), noopLogger{}, nil, newStorageWriteNk(), in)
			if err == nil {
				t.Errorf("delete of %s/%s: allowed, want refused", id[0], id[1])
				continue
			}
			if out != nil {
				t.Errorf("delete of %s/%s: the hook returned a request with its error", id[0], id[1])
			}
			if rerr, ok := err.(*runtime.Error); !ok || rerr.Code != 7 {
				t.Errorf("delete of %s/%s: error = %v, want code 7", id[0], id[1], err)
			}
		}
	}
	for _, in := range []*api.DeleteStorageObjectsRequest{{}, nil} {
		if out, err := BeforeDeleteStorageObjects(ctxWithUser(swMember), noopLogger{}, nil, newStorageWriteNk(), in); err == nil || out != nil {
			t.Errorf("an empty delete: allowed, want refused")
		}
	}
	if out, err := BeforeDeleteStorageObjects(context.Background(), noopLogger{}, nil, newStorageWriteNk(), &api.DeleteStorageObjectsRequest{}); err == nil || out != nil {
		t.Errorf("a delete with no session: allowed, want refused")
	}
}

// ---- wiring ----

func TestStorageWriteHooksAreRegistered(t *testing.T) {
	main, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if !strings.Contains(string(main), "registerStorageWriteHooks(initializer)") {
		t.Errorf("main.go does not call registerStorageWriteHooks")
	}
	src, err := os.ReadFile("storage_write_hooks.go")
	if err != nil {
		t.Fatalf("read storage_write_hooks.go: %v", err)
	}
	for _, call := range []string{
		"initializer.RegisterBeforeWriteStorageObjects(BeforeWriteStorageObjects)",
		"initializer.RegisterBeforeDeleteStorageObjects(BeforeDeleteStorageObjects)",
	} {
		if !strings.Contains(string(src), call) {
			t.Errorf("storage_write_hooks.go does not have %s", call)
		}
	}
}

// The largest avatar is not above the request limit of the server. A larger
// limit in the hook would add nothing, and a smaller request limit must not
// go unseen.
func TestStorageWriteAvatarLimitIsTheRequestLimit(t *testing.T) {
	cfg, err := os.ReadFile("../nakama.yml")
	if err != nil {
		t.Fatalf("read nakama.yml: %v", err)
	}
	if !strings.Contains(string(cfg), "max_request_size_bytes: 131072") {
		t.Errorf("nakama.yml no longer sets max_request_size_bytes to 131072: set clientAvatarMaxBytes to the new limit")
	}
	if clientAvatarMaxBytes != 131072 {
		t.Errorf("clientAvatarMaxBytes = %d, want 131072", clientAvatarMaxBytes)
	}
}

// A write that names no permission gets the defaults of Nakama, so it does
// not count as a write that names them.
func TestHasStoragePermissionsNeedsBothFields(t *testing.T) {
	if hasStoragePermissions(swObject("avatars", "current", "{}", -1, -1), 0, 0) {
		t.Errorf("a write with no permission fields counts as permissions 0 and 0")
	}
	if hasStoragePermissions(swObject("avatars", "current", "{}", 0, -1), 0, 0) {
		t.Errorf("a write with no write permission field counts as write 0")
	}
	if hasStoragePermissions(swObject("avatars", "current", "{}", -1, 0), 0, 0) {
		t.Errorf("a write with no read permission field counts as read 0")
	}
	if !hasStoragePermissions(swObject("avatars", "current", "{}", 0, 0), 0, 0) {
		t.Errorf("a write that names 0 and 0 does not count")
	}
}

func TestIsUUIDText(t *testing.T) {
	for _, s := range []string{swCrew, strings.ToUpper(swCrew), "00000000-0000-0000-0000-000000000000"} {
		if !isUUIDText(s) {
			t.Errorf("isUUIDText(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "abc", swCrew + "0", swCrew[1:], "cccccccc-0000-4000-8000_000000000001", "cccccccc-0000-4000-8000-00000000000z"} {
		if isUUIDText(s) {
			t.Errorf("isUUIDText(%q) = true, want false", s)
		}
	}
}
