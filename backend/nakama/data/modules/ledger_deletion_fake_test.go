package main

import (
	"context"
	"errors"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// delNk is fakeGuestNk with the calls the deletion rules use: the kick, the
// group delete and the storage delete. A test can make the writes or the
// deletes of one collection fail.
type delNk struct {
	*fakeGuestNk
	failDelete map[string]bool // collection -> StorageDelete fails
	failWrite  map[string]bool // collection -> StorageWrite fails
}

func newDelNk() *delNk {
	return &delNk{fakeGuestNk: newFakeGuestNk(), failDelete: map[string]bool{}, failWrite: map[string]bool{}}
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

func (n *delNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	for _, w := range writes {
		if n.failWrite[w.Collection] {
			return nil, errors.New("storage write failed")
		}
	}
	return n.fakeGuestNk.StorageWrite(ctx, writes)
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
		delete(n.storage, fakeStorageKey(d.Collection, d.Key, d.UserID))
	}
	return nil
}

// has reports whether a system document exists.
func (n *delNk) has(collection, key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, ok := n.storage[fakeStorageKey(collection, key, SystemUserID)]
	return ok
}
