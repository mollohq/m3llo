package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// useMemLedger sets an empty in-memory ledger store for one test.
func useMemLedger(t *testing.T) *memLedgerStore {
	t.Helper()
	prev := crewLedger
	store := newMemLedgerStore()
	crewLedger = store
	t.Cleanup(func() { crewLedger = prev })
	return store
}

func storeIDs(t *testing.T, store ledgerStore, crewID string) []string {
	t.Helper()
	events, err := store.Between(context.Background(), crewID, 0, time.Now().Add(charDay).UnixMilli(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

func docIDs(nk *fakeGuestNk, crewID string) []string {
	doc, _ := readLedgerDoc(context.Background(), nk, crewID)
	ids := make([]string, 0, len(doc.Events))
	for _, e := range doc.Events {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestDualWriteKeepsDocTrimmed(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	now := time.Now()

	charAppend(t, nk, charEvent("old", now.Add(-8*charDay)))
	charAppend(t, nk, charEvent("recent", now.Add(-6*charDay)))
	charAppend(t, nk, charEvent("new", now))

	// The document keeps its 7-day trim. The store keeps every event.
	if got := docIDs(nk, "crew"); !charSameIDs(got, "recent", "new") {
		t.Fatalf("document ids = %v, want [recent new]", got)
	}
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "old", "recent", "new") {
		t.Fatalf("store ids = %v, want [old recent new]", got)
	}
}

func TestDualWriteSetsTheCrewIDAndTheRef(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	ev := CrewEvent{ID: "s1", Type: "stream_session", ActorID: "u1", Timestamp: time.Now().UnixMilli(),
		Data: StreamSessionData{SessionID: "sess_1", StreamerID: "u1", SnapshotURLs: []string{}}}
	if err := AppendCrewEvent(context.Background(), nk, "crew", ev); err != nil {
		t.Fatal(err)
	}
	rows := store.rows["crew"]
	if len(rows) != 1 || rows[0].ev.CrewID != "crew" || rows[0].ref != "sess_1" {
		t.Fatalf("store rows = %+v", rows)
	}
}

func TestDualWriteSnapshotURLsAndRemoveReachBothStores(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	ev := CrewEvent{ID: "s1", CrewID: "crew", Type: "stream_session", ActorID: "u1", Timestamp: now,
		Data: StreamSessionData{SessionID: "sess_1", StreamerID: "u1", Title: "T", SnapshotURLs: []string{}}}
	if err := AppendCrewEvent(ctx, nk, "crew", ev); err != nil {
		t.Fatal(err)
	}
	charAppend(t, nk, charEvent("v1", time.Now()))

	urls := []string{"https://cdn.example/1.jpg", "https://cdn.example/2.jpg"}
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew", "s1", urls); err != nil {
		t.Fatal(err)
	}
	doc, _ := readLedgerDoc(ctx, nk, "crew")
	fromDoc, err := decodeStreamSessionData(doc.Events[0].Data)
	if err != nil || len(fromDoc.SnapshotURLs) != 2 || fromDoc.Title != "T" {
		t.Fatalf("document data = %+v err=%v", fromDoc, err)
	}
	events, _ := store.Between(ctx, "crew", now, now, []string{"stream_session"})
	if len(events) != 1 {
		t.Fatalf("store events = %d", len(events))
	}
	fromStore, err := decodeStreamSessionData(events[0].Data)
	if err != nil || len(fromStore.SnapshotURLs) != 2 || fromStore.Title != "T" || fromStore.SessionID != "sess_1" {
		t.Fatalf("store data = %+v err=%v", fromStore, err)
	}

	// An event that only the document holds is still updated there.
	nk.put(CrewEventsCollection, "crew2", SystemUserID, CrewEventLedger{CrewID: "crew2", Events: []CrewEvent{ev}})
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew2", "s1", urls); err != nil {
		t.Fatalf("update of a document-only event: %v", err)
	}

	if err := removeCrewEvent(ctx, nk, "crew", "s1"); err != nil {
		t.Fatal(err)
	}
	if got := docIDs(nk, "crew"); !charSameIDs(got, "v1") {
		t.Fatalf("document ids after remove = %v", got)
	}
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "v1") {
		t.Fatalf("store ids after remove = %v", got)
	}
}

// failingLedger fails every write. Reads come from the embedded store.
type failingLedger struct{ *memLedgerStore }

func (failingLedger) Append(context.Context, CrewEvent, string) (bool, error) {
	return false, errors.New("store down")
}

func TestDualWriteStoreFailureDoesNotFailTheAppend(t *testing.T) {
	prev := crewLedger
	crewLedger = failingLedger{newMemLedgerStore()}
	t.Cleanup(func() { crewLedger = prev })
	nk := newFakeGuestNk()

	charAppend(t, nk, charEvent("a", time.Now()))
	if got := docIDs(nk, "crew"); !charSameIDs(got, "a") {
		t.Fatalf("document ids = %v, want [a]", got)
	}
}

func TestConcurrentAppendsLoseNothing(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	const n = 40
	now := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := charEvent(fmt.Sprintf("e%02d", i), now.Add(time.Duration(i)*time.Millisecond))
			if err := AppendCrewEvent(context.Background(), nk, "crew", ev); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got := storeIDs(t, store, "crew")
	if len(got) != n {
		t.Fatalf("store holds %d events, want %d", len(got), n)
	}
	for i, id := range got {
		if id != fmt.Sprintf("e%02d", i) {
			t.Fatalf("store ids = %v", got)
		}
	}
	view, err := store.RecentView(context.Background(), "crew")
	if err != nil || len(view) != n {
		t.Fatalf("recent view holds %d events, err=%v, want %d", len(view), err, n)
	}
}
