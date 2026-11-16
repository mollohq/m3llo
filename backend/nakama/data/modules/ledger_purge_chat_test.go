package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// memChatRow is one row of the message table of Nakama, with the columns the
// purge uses.
type memChatRow struct {
	id      string
	mode    int
	subject string
}

// memCrewChat is the message table in memory. It holds the rows of every
// channel, so a test sees that the purge takes only the group channel of the
// crew.
type memCrewChat struct {
	mu   sync.Mutex
	rows []memChatRow
	// readErr and deleteErr make the next calls fail.
	readErr, deleteErr error
	// batches counts the deletes.
	batches int
	// nk holds the reactions documents. seedCrewChat sets it.
	nk *delNk
}

func (m *memCrewChat) ReactionKeys(_ context.Context, crewID string, limit int) ([]string, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	if m.nk == nil {
		return nil, nil
	}
	objects, _, err := m.nk.StorageList(context.Background(), "", SystemUserID, ChatReactionsCollection, 0, "")
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, obj := range objects {
		var doc ReactionsDoc
		if json.Unmarshal([]byte(obj.GetValue()), &doc) == nil && doc.CrewID == crewID {
			keys = append(keys, obj.GetKey())
		}
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (m *memCrewChat) add(mode int, subject string, ids ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		m.rows = append(m.rows, memChatRow{id: id, mode: mode, subject: subject})
	}
}

// ids returns the ids of the rows of one stream.
func (m *memCrewChat) ids(mode int, subject string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.rows {
		if r.mode == mode && r.subject == subject {
			out = append(out, r.id)
		}
	}
	sort.Strings(out)
	return out
}

func (m *memCrewChat) MessageIDs(_ context.Context, crewID string, limit int) ([]string, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	ids := m.ids(chatStreamModeGroup, crewID)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (m *memCrewChat) DeleteMessages(_ context.Context, crewID string, ids []string) (int, error) {
	if m.deleteErr != nil {
		return 0, m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	kept := m.rows[:0:0]
	n := 0
	for _, r := range m.rows {
		if r.mode == chatStreamModeGroup && r.subject == crewID && drop[r.id] {
			n++
			continue
		}
		kept = append(kept, r)
	}
	m.rows = kept
	m.batches++
	return n, nil
}

// useMemChat gives the purge a message table in memory.
func useMemChat(t *testing.T) *memCrewChat {
	t.Helper()
	m := &memCrewChat{}
	prev := crewChat
	crewChat = func() crewChatStore { return m }
	t.Cleanup(func() { crewChat = prev })
	return m
}

// currentMemChat returns the message table that usePurgeBuckets installed.
func currentMemChat(t *testing.T) *memCrewChat {
	t.Helper()
	m, ok := crewChat().(*memCrewChat)
	if !ok {
		t.Fatal("the test has no message table in memory")
	}
	return m
}

const (
	chatMemberA = "aaaaaaaa-0000-4000-8000-000000000001"
	chatMemberB = "aaaaaaaa-0000-4000-8000-000000000002"
)

// seedCrewChat gives a crew five chat messages, reactions on two of them,
// the reactions of a message that is gone, and two chat images.
func seedCrewChat(nk *delNk, f *fakeS3, chat *memCrewChat, crewID string) {
	chat.nk = nk
	tag := crewID[:4]
	nk.put(ChatReactionsCollection, "gone-"+tag, SystemUserID, ReactionsDoc{CrewID: crewID, Reactions: map[string][]string{"🔥": {chatMemberA}}})
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		chat.add(chatStreamModeGroup, crewID, "m"+n+"-"+tag)
	}
	nk.put(ChatReactionsCollection, "m2-"+tag, SystemUserID, ReactionsDoc{CrewID: crewID, Reactions: map[string][]string{"🔥": {chatMemberA}}})
	nk.put(ChatReactionsCollection, "m4-"+tag, SystemUserID, ReactionsDoc{CrewID: crewID, Reactions: map[string][]string{"👀": {chatMemberB}}})
	f.put("media", crewID+"/"+chatMemberA+"/one.jpg", 10, time.Hour)
	f.put("media", crewID+"/"+chatMemberB+"/two.png", 10, time.Hour)
}

// chatLeftovers lists what is left of the chat of a crew.
func chatLeftovers(nk *delNk, f *fakeS3, chat *memCrewChat, crewID string) []string {
	var left []string
	for _, id := range chat.ids(chatStreamModeGroup, crewID) {
		left = append(left, "message "+id)
	}
	tag := crewID[:4]
	for _, key := range []string{"m1-" + tag, "m2-" + tag, "m3-" + tag, "m4-" + tag, "m5-" + tag, "gone-" + tag} {
		if nk.has(ChatReactionsCollection, key) {
			left = append(left, "reactions "+key)
		}
	}
	for _, key := range f.keys("media") {
		if strings.HasPrefix(key, crewID+"/") {
			left = append(left, "image "+key)
		}
	}
	return left
}

// A crew delete removes the chat of the crew: the messages, in batches, their
// reactions and the chat images. The chat of another crew, a direct message
// between two members and a room stay.
func TestPurgeCrewRemovesItsChat(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := usePurgeBuckets(t)
		chat := currentMemChat(t)
		prevBatch := crewPurgeChatBatch
		crewPurgeChatBatch = 2
		t.Cleanup(func() { crewPurgeChatBatch = prevBatch })
		seedPurgeCrew(t, nk, f, purgeCrew)
		seedPurgeCrew(t, nk, f, otherCrew)
		seedCrewChat(nk, f, chat, purgeCrew)
		seedCrewChat(nk, f, chat, otherCrew)
		// A direct message between two members of the crew (stream mode 4),
		// and a room whose subject is the crew id by accident (stream mode 2).
		chat.add(4, chatMemberA, "dm1", "dm2")
		chat.add(2, purgeCrew, "room1")
		nk.put(ChatReactionsCollection, "dm1", SystemUserID, ReactionsDoc{Reactions: map[string][]string{"🔥": {chatMemberB}}})
		chat.nk = nk
		// The images of the purge seed are counted too.
		if left := chatLeftovers(nk, f, chat, purgeCrew); len(left) != 11 {
			t.Fatalf("seed = %v", left)
		}

		deleteCrewByOwner(t, nk, purgeCrew)
		if left := chatLeftovers(nk, f, chat, purgeCrew); len(left) != 0 {
			t.Fatalf("left after delete_crew: %v", left)
		}
		if chat.batches != 3 {
			t.Fatalf("batches = %d, want 3 for five messages, two at a time", chat.batches)
		}
		if left := chatLeftovers(nk, f, chat, otherCrew); len(left) != 11 {
			t.Fatalf("the chat of the other crew = %v", left)
		}
		if got := chat.ids(4, chatMemberA); len(got) != 2 || !nk.has(ChatReactionsCollection, "dm1") {
			t.Fatalf("the direct message = %v", got)
		}
		if got := chat.ids(2, purgeCrew); len(got) != 1 {
			t.Fatalf("the room = %v", got)
		}
		if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs == 0 {
			t.Fatalf("purge record = %+v", rec)
		}
	})
}

// A chat read that fails deletes nothing of the chat, and the purge is not
// done. admin_purge_crew runs it again.
func TestPurgeCrewChatFailsAndRunsAgain(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	chat := currentMemChat(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	seedCrewChat(nk, f, chat, purgeCrew)

	chat.readErr = errors.New("database down")
	deleteCrewByOwner(t, nk, purgeCrew)
	if got := chat.ids(chatStreamModeGroup, purgeCrew); len(got) != 5 || !nk.has(ChatReactionsCollection, "m2-1111") {
		t.Fatalf("a failed read deleted chat: %v", got)
	}
	if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs != 0 || !strings.Contains(rec.LastError, "chat read") {
		t.Fatalf("purge record = %+v", rec)
	}

	// The delete fails: the messages stay, and the next run finds them
	// again.
	chat.readErr, chat.deleteErr = nil, errors.New("database down")
	if res, err := purgeRPC(nk, purgeCrew); err != nil || res.Done || res.ChatDeleted != 0 {
		t.Fatalf("purge = %+v err=%v", res, err)
	}
	if got := chat.ids(chatStreamModeGroup, purgeCrew); len(got) != 5 {
		t.Fatalf("messages = %v", got)
	}

	chat.deleteErr = nil
	res, err := purgeRPC(nk, purgeCrew)
	if err != nil || !res.Done || res.ChatDeleted != 5 {
		t.Fatalf("purge = %+v err=%v", res, err)
	}
	if left := chatLeftovers(nk, f, chat, purgeCrew); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
}

// Nothing of the chat goes while the group exists or the group read fails,
// and a run past its deadline deletes no message.
func TestPurgeCrewChatOnlyWhenTheGroupIsGone(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	chat := currentMemChat(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	seedCrewChat(nk, f, chat, purgeCrew)
	ctx := context.Background()

	// A purge record of a crew that is a group.
	if _, err := ensureCrewPurgeRecord(ctx, nk, purgeCrew); err != nil {
		t.Fatal(err)
	}
	purgeCrewData(ctx, nk, testLogger(), purgeCrew, time.Now().Add(time.Minute))
	if left := chatLeftovers(nk, f, chat, purgeCrew); len(left) != 11 {
		t.Fatalf("the chat of a live crew = %v", left)
	}

	// The group is gone, and the group read fails.
	if err := nk.GroupDelete(ctx, purgeCrew); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCrewPurgeRecord(ctx, nk, purgeCrew); err != nil {
		t.Fatal(err)
	}
	nk.groupErr = errors.New("database down")
	purgeCrewData(ctx, nk, testLogger(), purgeCrew, time.Now().Add(time.Minute))
	if left := chatLeftovers(nk, f, chat, purgeCrew); len(left) != 11 {
		t.Fatalf("the chat after a failed group read = %v", left)
	}
	nk.groupErr = nil

	// Past the deadline: cut, nothing deleted.
	res := purgeCrewData(ctx, nk, testLogger(), purgeCrew, time.Now().Add(-time.Second))
	if !res.Incomplete || res.ChatDeleted != 0 || len(chat.ids(chatStreamModeGroup, purgeCrew)) != 5 {
		t.Fatalf("a purge past its deadline = %+v", res)
	}
	res = purgeCrewData(ctx, nk, testLogger(), purgeCrew, time.Now().Add(time.Minute))
	if !res.Done || res.ChatDeleted != 5 || res.ReactionsDeleted != 3 {
		t.Fatalf("purge = %+v", res)
	}
}

// A crew that has only chat messages left is a crew of this install:
// admin_purge_crew takes it.
func TestAdminPurgeCrewKnowsACrewByItsChat(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	usePurgeBuckets(t)
	chat := currentMemChat(t)
	chat.add(chatStreamModeGroup, purgeCrew, "m1", "m2")
	res, err := purgeRPC(nk, purgeCrew)
	if err != nil || !res.Done || res.ChatDeleted != 2 {
		t.Fatalf("purge = %+v err=%v", res, err)
	}
}

// The module has no database handle: the purge is not done.
func TestPurgeCrewWithoutADatabaseIsNotDone(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	prev := crewChat
	crewChat = func() crewChatStore { return nil }
	t.Cleanup(func() { crewChat = prev })
	deleteCrewByOwner(t, nk, purgeCrew)
	if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs != 0 || !strings.Contains(rec.LastError, "database handle") {
		t.Fatalf("purge record = %+v", rec)
	}
}
