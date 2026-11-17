package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// DevDeleteSelftestRPC runs the SQL statements of the deletion rules on the
// real tables of Nakama: MessageIDs, DeleteMessages, ReactionKeys,
// DropGoneMember and GoneMembers. go test runs the rules on fakes, so only
// this run sees the statements themselves. It also checks what the group
// update of Nakama does with empty values, which groupCreator relies on.
//
// It writes only rows it makes: messages and reactions under new random ids,
// one account and one group it creates, and membership rows between that
// group and a new random user id. It deletes every row by its own id at the
// end, also when a check fails. Gated behind MELLO_ENABLE_DEV_FAULT=1, as
// the other dev RPCs that write. Never enable in production.
func DevDeleteSelftestRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if os.Getenv("MELLO_ENABLE_DEV_FAULT") != "1" {
		return "", runtime.NewError("dev_delete_selftest disabled (set MELLO_ENABLE_DEV_FAULT=1)", 9)
	}
	if db == nil {
		return "", runtime.NewError("no database", 13)
	}
	checks, err := deleteSelftest(ctx, db, nk)
	if err != nil {
		logger.Error("dev_delete_selftest failed: %v", err)
		return "", runtime.NewError("delete selftest failed: "+err.Error(), 13)
	}
	logger.Warn("dev_delete_selftest passed")
	resp, _ := json.Marshal(map[string]interface{}{"success": true, "checks": checks})
	return string(resp), nil
}

// selftestUUID returns a new random UUID (version 4).
func selftestUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func deleteSelftest(ctx context.Context, db *sql.DB, nk runtime.NakamaModule) (checks []string, err error) {
	ids := make([]string, 12)
	for i := range ids {
		if ids[i], err = selftestUUID(); err != nil {
			return nil, err
		}
	}
	otherCrew, goneUser, dmA, dmB := ids[0], ids[1], ids[2], ids[3]
	msgA, msgOther, msgDM := ids[4:7], ids[7], ids[8]
	reactA, reactOther := ids[9:11], ids[11]

	// One account and one group that exist.
	userID, _, _, err := nk.AuthenticateDevice(ctx, "selftest-"+ids[0], "selftest_"+ids[0][:8], true)
	if err != nil {
		return nil, fmt.Errorf("account: %w", err)
	}
	defer func() { _ = nk.AccountDeleteId(context.Background(), userID, false) }()
	name := "selftest " + ids[0][:8]
	group, err := nk.GroupCreate(ctx, userID, name, userID, "en", "the description", "", false,
		map[string]interface{}{"created_by": userID, "keep": "yes"}, 10)
	if err != nil {
		return nil, fmt.Errorf("group: %w", err)
	}
	crew := group.GetId()
	defer func() { _ = nk.GroupDelete(context.Background(), crew) }()

	// Every row below is deleted here by its own id.
	defer func() {
		bg := context.Background()
		for _, id := range append(append([]string{}, msgA...), msgOther, msgDM) {
			_, _ = db.ExecContext(bg, `DELETE FROM message WHERE id = $1::UUID`, id)
		}
		for _, key := range append(append([]string{}, reactA...), reactOther) {
			_ = nk.StorageDelete(bg, []*runtime.StorageDelete{{Collection: ChatReactionsCollection, Key: key, UserID: SystemUserID}})
		}
		_, _ = db.ExecContext(bg,
			`DELETE FROM group_edge WHERE (source_id = $1::UUID AND destination_id = $2::UUID) OR (source_id = $2::UUID AND destination_id = $1::UUID)`,
			crew, goneUser)
	}()

	count := func(query string, args ...interface{}) (int, error) {
		var n int
		err := db.QueryRowContext(ctx, query, args...).Scan(&n)
		return n, err
	}
	fail := func(format string, args ...interface{}) ([]string, error) {
		return checks, fmt.Errorf(format, args...)
	}
	pass := func(what string) { checks = append(checks, what) }

	// --- The chat messages -------------------------------------------------
	insert := func(id string, mode int, subject, descriptor string, at time.Time) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO message (id, code, sender_id, username, stream_mode, stream_subject, stream_descriptor, stream_label, content, create_time, update_time)
			 VALUES ($1::UUID, 0, $2::UUID, 'selftest', $3, $4::UUID, $5::UUID, '', '{}', $6, $6)`,
			id, SystemUserID, mode, subject, descriptor, at)
		return err
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i, id := range msgA {
		if err := insert(id, chatStreamModeGroup, crew, SystemUserID, base.Add(time.Duration(i)*time.Second)); err != nil {
			return fail("message insert: %v", err)
		}
	}
	if err := insert(msgOther, chatStreamModeGroup, otherCrew, SystemUserID, base); err != nil {
		return fail("message insert: %v", err)
	}
	// A direct message: stream mode 4, two user ids.
	if err := insert(msgDM, 4, dmA, dmB, base); err != nil {
		return fail("message insert: %v", err)
	}
	chat := sqlCrewChat{db: db}
	got, err := chat.MessageIDs(ctx, crew, 2)
	if err != nil || len(got) != 2 || got[0] != msgA[0] || got[1] != msgA[1] {
		return fail("MessageIDs limit 2 = %v err=%v, want the two oldest %v", got, err, msgA[:2])
	}
	got, err = chat.MessageIDs(ctx, crew, 100)
	if err != nil || len(got) != 3 {
		return fail("MessageIDs = %v err=%v, want the three messages of the crew", got, err)
	}
	pass("MessageIDs returns the messages of the group channel of the crew, the oldest first, up to the limit")
	// Ids of another crew and of a direct message delete nothing.
	n, err := chat.DeleteMessages(ctx, crew, []string{msgA[0], msgOther, msgDM})
	if err != nil || n != 1 {
		return fail("DeleteMessages with ids of other channels deleted %d err=%v, want 1", n, err)
	}
	n, err = chat.DeleteMessages(ctx, crew, got)
	if err != nil || n != 2 {
		return fail("DeleteMessages deleted %d err=%v, want 2", n, err)
	}
	if left, err := chat.MessageIDs(ctx, crew, 100); err != nil || len(left) != 0 {
		return fail("messages of the crew after the delete = %v err=%v", left, err)
	}
	if n, err := count(`SELECT count(*) FROM message WHERE id IN ($1::UUID, $2::UUID)`, msgOther, msgDM); err != nil || n != 2 {
		return fail("the message of another crew and the direct message: %d left err=%v, want 2", n, err)
	}
	pass("DeleteMessages deletes only messages of the group channel of the crew: a message of another crew and a direct message stay")

	// --- The reactions -----------------------------------------------------
	write := func(key, crewID string) error {
		data, _ := json.Marshal(ReactionsDoc{CrewID: crewID, Reactions: map[string][]string{}})
		_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection: ChatReactionsCollection, Key: key, UserID: SystemUserID, Value: string(data), PermissionRead: 2, PermissionWrite: 0,
		}})
		return err
	}
	for _, key := range reactA {
		if err := write(key, crew); err != nil {
			return fail("reactions write: %v", err)
		}
	}
	if err := write(reactOther, otherCrew); err != nil {
		return fail("reactions write: %v", err)
	}
	keys, err := chat.ReactionKeys(ctx, crew, 1)
	if err != nil || len(keys) != 1 || (keys[0] != reactA[0] && keys[0] != reactA[1]) {
		return fail("ReactionKeys limit 1 = %v err=%v", keys, err)
	}
	keys, err = chat.ReactionKeys(ctx, crew, 100)
	if err != nil || len(keys) != 2 || keys[0] == reactOther || keys[1] == reactOther || keys[0] == keys[1] {
		return fail("ReactionKeys = %v err=%v, want the two documents of the crew", keys, err)
	}
	pass("ReactionKeys returns the reactions documents of the crew and not of another crew")

	// --- The membership rows -----------------------------------------------
	edges := sqlCrewEdges{db: db}
	position := time.Now().UTC().UnixNano()
	for _, pair := range [][2]string{{crew, goneUser}, {goneUser, crew}} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO group_edge (source_id, position, state, destination_id) VALUES ($1::UUID, $2, 0, $3::UUID)`,
			pair[0], position, pair[1]); err != nil {
			return fail("group_edge insert: %v", err)
		}
	}
	rowsOf := func(user string) (int, error) {
		return count(`SELECT count(*) FROM group_edge WHERE (source_id = $1::UUID AND destination_id = $2::UUID) OR (source_id = $2::UUID AND destination_id = $1::UUID)`, crew, user)
	}
	// The account of userID exists: nothing goes.
	if n, err := edges.DropGoneMember(ctx, crew, userID); err != nil || n != 0 {
		return fail("DropGoneMember for an account that exists deleted %d err=%v", n, err)
	}
	if n, err := rowsOf(userID); err != nil || n != 2 {
		return fail("rows of the account that exists = %d err=%v, want 2", n, err)
	}
	gone, err := edges.GoneMembers(ctx, 100000)
	if err != nil {
		return fail("GoneMembers: %v", err)
	}
	found := false
	for _, m := range gone {
		if m.CrewID == crew && m.UserID == userID {
			return fail("GoneMembers names a member whose account exists")
		}
		found = found || (m.CrewID == crew && m.UserID == goneUser)
	}
	if !found {
		return fail("GoneMembers does not name the member with no account")
	}
	if n, err := edges.DropGoneMember(ctx, crew, goneUser); err != nil || n != 2 {
		return fail("DropGoneMember deleted %d err=%v, want 2", n, err)
	}
	if n, err := rowsOf(goneUser); err != nil || n != 0 {
		return fail("rows of the gone member = %d err=%v", n, err)
	}
	if n, err := rowsOf(userID); err != nil || n != 2 {
		return fail("rows of the account that exists = %d err=%v, want 2", n, err)
	}
	pass("DropGoneMember deletes both rows of a member with no account, and no row of an account that exists. GoneMembers names only the first")

	// --- The group update of Nakama ----------------------------------------
	read := func() (*api.Group, map[string]interface{}, error) {
		groups, err := nk.GroupsGetId(ctx, []string{crew})
		if err != nil || len(groups) != 1 {
			return nil, nil, fmt.Errorf("group read: %d groups err=%v", len(groups), err)
		}
		var meta map[string]interface{}
		if err := json.Unmarshal([]byte(groups[0].GetMetadata()), &meta); err != nil {
			return nil, nil, err
		}
		return groups[0], meta, nil
	}
	// Empty values and a nil metadata.
	if err := nk.GroupUpdate(ctx, crew, "", "", "", "", "", "", false, nil, 0); err != nil {
		return fail("GroupUpdate with empty values: %v", err)
	}
	g, meta, err := read()
	if err != nil {
		return fail("%v", err)
	}
	if g.GetName() != name || g.GetDescription() != "the description" || g.GetLangTag() != "en" || g.GetCreatorId() != userID ||
		g.GetOpen().GetValue() || g.GetMaxCount() != 10 || len(meta) != 2 || meta["keep"] != "yes" {
		return fail("GroupUpdate with empty values changed the group: %v", g)
	}
	pass("GroupUpdate: an empty name, description, language, avatar and creator, a nil metadata and a max count of 0 change nothing")
	// The open flag is always written.
	if err := nk.GroupUpdate(ctx, crew, "", "", "", "", "", "", true, nil, 0); err != nil {
		return fail("GroupUpdate open: %v", err)
	}
	if g, _, err = read(); err != nil || !g.GetOpen().GetValue() {
		return fail("GroupUpdate did not write the open flag: err=%v", err)
	}
	pass("GroupUpdate: the open flag is always written")
	// A metadata that is given replaces the whole metadata. A creator that
	// is given is written.
	if err := nk.GroupUpdate(ctx, crew, "", "", goneUser, "", "", "", true, map[string]interface{}{"keep": "yes"}, 0); err != nil {
		return fail("GroupUpdate metadata: %v", err)
	}
	g, meta, err = read()
	if err != nil {
		return fail("%v", err)
	}
	if _, has := meta["created_by"]; has || len(meta) != 1 || meta["keep"] != "yes" || g.GetCreatorId() != goneUser || g.GetName() != name || g.GetDescription() != "the description" {
		return fail("GroupUpdate with a metadata and a creator: metadata=%v creator=%s", meta, g.GetCreatorId())
	}
	pass("GroupUpdate: a metadata that is given replaces the whole metadata, and a creator that is given is written")
	return checks, nil
}
