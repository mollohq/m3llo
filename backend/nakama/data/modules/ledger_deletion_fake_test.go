package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// delNk is fakeGuestNk with the calls the deletion rules use: the kick, the
// group delete and the storage delete. It checks storage versions as Nakama
// does: a write with a version that is not the current one fails, and "*"
// writes only an object that does not exist. A test can make the writes or
// the deletes of one collection fail.
type delNk struct {
	*fakeGuestNk
	failDelete map[string]bool // collection -> StorageDelete fails
	failWrite  map[string]bool // collection -> StorageWrite fails
	versions   map[string]int  // storage key -> number of writes
	// groupErr makes every group read fail.
	groupErr error
	// onRead runs before each storage read of a collection, outside the lock.
	onRead map[string]func()
	// onWrite runs before each storage write of a collection, outside the
	// lock: another writer that gets there first.
	onWrite map[string]func()
	// stale counts the writes that a version check refused.
	stale int
	// memberErr makes the member lists of one crew fail.
	memberErr map[string]error
	// promoteErr makes every promote fail. promoteNoop makes a promote
	// change nothing and return no error.
	promoteErr  error
	promoteNoop bool
	// promotes are the promotes that changed a member: "crew user state".
	promotes []string
}

func newDelNk() *delNk {
	return &delNk{fakeGuestNk: newFakeGuestNk(), failDelete: map[string]bool{}, failWrite: map[string]bool{},
		versions: map[string]int{}, onRead: map[string]func(){}, onWrite: map[string]func(){}, memberErr: map[string]error{}}
}

func (n *delNk) versionOf(k string) string { return fmt.Sprintf("v%d", n.versions[k]) }

func (n *delNk) GroupsGetId(ctx context.Context, ids []string) ([]*api.Group, error) {
	if n.groupErr != nil {
		return nil, n.groupErr
	}
	return n.fakeGuestNk.GroupsGetId(ctx, ids)
}

// GroupUsersList lists the members as Nakama does: the members of one state
// when a state is given, in the order they joined, up to the limit.
func (n *delNk) GroupUsersList(_ context.Context, id string, limit int, state *int, _ string) ([]*api.GroupUserList_GroupUser, string, error) {
	if err := n.memberErr[id]; err != nil {
		return nil, "", err
	}
	var out []*api.GroupUserList_GroupUser
	for _, m := range n.members[id] {
		if state != nil && int(m.GetState().GetValue()) != *state {
			continue
		}
		out = append(out, m)
	}
	if state != nil && limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, "", nil
}

// UserGroupsList is the list of the base fake, with the state of the user in
// each crew.
func (n *delNk) UserGroupsList(_ context.Context, userID string, _ int, _ *int, _ string) ([]*api.UserGroupList_UserGroup, string, error) {
	var crews []string
	for crew := range n.members {
		crews = append(crews, crew)
	}
	sort.Strings(crews)
	var out []*api.UserGroupList_UserGroup
	for _, crew := range crews {
		for _, m := range n.members[crew] {
			if m.GetUser().GetId() == userID {
				out = append(out, &api.UserGroupList_UserGroup{Group: &api.Group{Id: crew}, State: wrapperspb.Int32(m.GetState().GetValue())})
			}
		}
	}
	return out, "", nil
}

// GroupUsersPromote moves each member one rank up, as Nakama does.
func (n *delNk) GroupUsersPromote(_ context.Context, _ string, groupID string, userIDs []string) error {
	if n.promoteErr != nil {
		return n.promoteErr
	}
	if n.promoteNoop {
		return nil
	}
	for _, m := range n.members[groupID] {
		for _, id := range userIDs {
			if m.GetUser().GetId() == id && m.GetState().GetValue() > 0 {
				m.State = wrapperspb.Int32(m.GetState().GetValue() - 1)
				n.promotes = append(n.promotes, fmt.Sprintf("%s %s %d", groupID, id, m.GetState().GetValue()))
			}
		}
	}
	return nil
}

// addMemberAs adds a member with a state: 0 superadmin, 1 admin, 2 member,
// 3 join request. The order of the calls is the order of the joins.
func (n *delNk) addMemberAs(crewID, userID, name string, state int32) {
	n.addMember(crewID, userID, name)
	ms := n.members[crewID]
	ms[len(ms)-1].State = wrapperspb.Int32(state)
}

func (n *delNk) GroupUsersKick(_ context.Context, _ string, groupID string, userIDs []string) error {
	kicked := map[string]bool{}
	for _, id := range userIDs {
		kicked[id] = true
	}
	kept := n.members[groupID][:0:0]
	for _, m := range n.members[groupID] {
		if !kicked[m.GetUser().GetId()] {
			kept = append(kept, m)
		}
	}
	n.members[groupID] = kept
	return nil
}

func (n *delNk) GroupDelete(_ context.Context, id string) error {
	delete(n.groups, id)
	delete(n.members, id)
	return nil
}

func (n *delNk) StorageRead(_ context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	for _, r := range reads {
		if hook := n.onRead[r.Collection]; hook != nil {
			hook()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*api.StorageObject
	for _, r := range reads {
		k := fakeStorageKey(r.Collection, r.Key, r.UserID)
		if v, ok := n.storage[k]; ok {
			out = append(out, &api.StorageObject{Collection: r.Collection, Key: r.Key, UserId: r.UserID, Value: v, Version: n.versionOf(k),
				PermissionRead: int32(n.perms[k][0]), PermissionWrite: int32(n.perms[k][1])})
		}
	}
	return out, nil
}

func (n *delNk) StorageList(_ context.Context, _, userID, collection string, _ int, _ string) ([]*api.StorageObject, string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	prefix, suffix := collection+"/", "/"+userID
	var keys []string
	for k := range n.storage {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, suffix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []*api.StorageObject
	for _, k := range keys {
		key := strings.TrimSuffix(strings.TrimPrefix(k, prefix), suffix)
		out = append(out, &api.StorageObject{Collection: collection, Key: key, UserId: userID, Value: n.storage[k], Version: n.versionOf(k)})
	}
	return out, "", nil
}

func (n *delNk) StorageWrite(_ context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	for _, w := range writes {
		if n.failWrite[w.Collection] {
			return nil, errors.New("storage write failed")
		}
	}
	for _, w := range writes {
		if hook := n.onWrite[w.Collection]; hook != nil {
			hook()
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, w := range writes {
		k := fakeStorageKey(w.Collection, w.Key, w.UserID)
		_, exists := n.storage[k]
		switch {
		case w.Version == "":
		case w.Version == "*" && !exists:
		case w.Version != "*" && exists && w.Version == n.versionOf(k):
		default:
			n.stale++
			return nil, errors.New("storage write rejected: version check failed")
		}
	}
	acks := make([]*api.StorageObjectAck, 0, len(writes))
	for _, w := range writes {
		k := fakeStorageKey(w.Collection, w.Key, w.UserID)
		n.storage[k] = w.Value
		n.versions[k]++
		n.perms[k] = [2]int{w.PermissionRead, w.PermissionWrite}
		acks = append(acks, &api.StorageObjectAck{Collection: w.Collection, Key: w.Key, UserId: w.UserID, Version: n.versionOf(k)})
	}
	return acks, nil
}

func (n *delNk) StorageDelete(_ context.Context, deletes []*runtime.StorageDelete) error {
	for _, d := range deletes {
		if n.failDelete[d.Collection] {
			return errors.New("storage delete failed")
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, d := range deletes {
		k := fakeStorageKey(d.Collection, d.Key, d.UserID)
		if _, exists := n.storage[k]; d.Version != "" && (!exists || d.Version != n.versionOf(k)) {
			n.stale++
			return errors.New("storage delete rejected: version check failed")
		}
	}
	for _, d := range deletes {
		k := fakeStorageKey(d.Collection, d.Key, d.UserID)
		delete(n.storage, k)
		n.versions[k]++
	}
	return nil
}

// put stores v as JSON, as a write with no version.
func (n *delNk) put(collection, key, userID string, v interface{}) {
	n.fakeGuestNk.put(collection, key, userID, v)
	n.mu.Lock()
	n.versions[fakeStorageKey(collection, key, userID)]++
	n.mu.Unlock()
}

// has reports whether a system document exists.
func (n *delNk) has(collection, key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.storage[fakeStorageKey(collection, key, SystemUserID)]
	return ok
}

// delPhase is one phase of the migration that a deletion rule must hold in.
type delPhase struct {
	name string
	// setup sets the store and the phase. It runs before the test seeds.
	setup func(t *testing.T, nk runtime.NakamaModule) *memLedgerStore
}

// markersInStorage writes both markers, so that the reconcile also knows that
// the cutovers are done.
func markersInStorage(t *testing.T, nk runtime.NakamaModule) {
	t.Helper()
	setMarker(t, nk)
	now := time.Now().UnixMilli()
	if err := writeHistoryMigrationState(context.Background(), nk, historyMigrationState{StartedMs: now, DoneMs: now}, ""); err != nil {
		t.Fatal(err)
	}
}

// delPhases are the phases: before the cutovers (the documents are the
// truth), after both (the store is the truth), and under each kill switch.
var delPhases = []delPhase{
	{name: "before the cutover", setup: func(t *testing.T, _ runtime.NakamaModule) *memLedgerStore {
		store := useMemLedger(t)
		resetLedgerCutover(t)
		resetHistoryCutover(t)
		return store
	}},
	{name: "after the cutover", setup: func(t *testing.T, nk runtime.NakamaModule) *memLedgerStore {
		store := useHistoryStoreReads(t)
		markersInStorage(t, nk)
		return store
	}},
	{name: "kill switch of phase 1b", setup: func(t *testing.T, nk runtime.NakamaModule) *memLedgerStore {
		store := useHistoryStoreReads(t)
		markersInStorage(t, nk)
		t.Setenv("MELLO_LEDGER_HISTORY_READ", "doc")
		return store
	}},
	{name: "kill switch of both phases", setup: func(t *testing.T, nk runtime.NakamaModule) *memLedgerStore {
		store := useHistoryStoreReads(t)
		markersInStorage(t, nk)
		t.Setenv("MELLO_LEDGER_READ", "doc")
		return store
	}},
}

func forDelPhases(t *testing.T, fn func(t *testing.T, nk *delNk, store *memLedgerStore)) {
	for _, p := range delPhases {
		t.Run(p.name, func(t *testing.T) {
			nk := newDelNk()
			fn(t, nk, p.setup(t, nk))
		})
	}
}

// crewRows returns every row the store holds for a crew.
func crewRows(t *testing.T, store ledgerStore, crewID string) []CrewEvent {
	t.Helper()
	rows, err := store.Between(context.Background(), crewID, 0, math.MaxInt64, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
