package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// useLedger sets a ledger store for one test.
func useLedger(t *testing.T, store ledgerStore) {
	t.Helper()
	prev := currentLedger()
	setLedger(store)
	t.Cleanup(func() { setLedger(prev) })
}

// useMemLedger sets an empty in-memory ledger store for one test.
func useMemLedger(t *testing.T) *memLedgerStore {
	t.Helper()
	store := newMemLedgerStore()
	useLedger(t, store)
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

func docIDs(nk runtime.NakamaModule, crewID string) []string {
	doc, _ := readLedgerDoc(context.Background(), nk, crewID)
	ids := make([]string, 0, len(doc.Events))
	for _, e := range doc.Events {
		ids = append(ids, e.ID)
	}
	return ids
}

// failDocNk fails every write of a legacy ledger document.
type failDocNk struct{ *fakeGuestNk }

func (f failDocNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	for _, w := range writes {
		if w.Collection == CrewEventsCollection {
			return nil, errors.New("document write failed")
		}
	}
	return f.fakeGuestNk.StorageWrite(ctx, writes)
}

// failingLedger fails the writes that are switched on. Everything else comes
// from the embedded store.
type failingLedger struct {
	*memLedgerStore
	failAppend, failDelete, failUpdate bool
}

func (f failingLedger) Append(ctx context.Context, ev CrewEvent, ref string) (bool, error) {
	if f.failAppend {
		return false, errors.New("store down")
	}
	return f.memLedgerStore.Append(ctx, ev, ref)
}

func (f failingLedger) Delete(ctx context.Context, crewID, eventID string) error {
	if f.failDelete {
		return errors.New("store down")
	}
	return f.memLedgerStore.Delete(ctx, crewID, eventID)
}

func streamEvent(id string, ts time.Time) CrewEvent {
	return CrewEvent{ID: id, CrewID: "crew", Type: "stream_session", ActorID: "u1", Timestamp: ts.UnixMilli(),
		Data: StreamSessionData{SessionID: "sess_" + id, StreamerID: "u1", StreamerName: "Ann", Title: "T", SnapshotURLs: []string{}}}
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

func TestDualWriteSetsTheCrewIDTheRefAndTheStoredType(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	ev := streamEvent("s1", time.Now())
	ev.CrewID = ""
	if err := AppendCrewEvent(context.Background(), nk, "crew", ev); err != nil {
		t.Fatal(err)
	}
	// A clip event of the legacy ledger path, as the dev seed writes it.
	clip := CrewEvent{ID: "c1", Type: "clip", ActorID: "u1", Timestamp: time.Now().UnixMilli(), Data: ClipData{ClipID: "clip_1"}}
	if err := AppendCrewEvent(context.Background(), nk, "crew", clip); err != nil {
		t.Fatal(err)
	}
	rows := store.rows["crew"]
	if len(rows) != 2 || rows[0].ev.CrewID != "crew" || rows[0].ref != "sess_s1" || rows[0].ev.Type != "stream_session" {
		t.Fatalf("store rows = %+v", rows)
	}
	if rows[1].ev.Type != "ledger_clip" {
		t.Fatalf("stored type of the legacy clip = %q, want ledger_clip", rows[1].ev.Type)
	}
	// The document keeps the type it always had.
	doc, _ := readLedgerDoc(context.Background(), nk, "crew")
	if doc.Events[1].Type != "clip" {
		t.Fatalf("document type = %q, want clip", doc.Events[1].Type)
	}
}

func TestDualWriteSnapshotURLsAndRemoveReachBothStores(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	ctx := context.Background()
	now := time.Now()
	ev := streamEvent("s1", now)
	if err := AppendCrewEvent(ctx, nk, "crew", ev); err != nil {
		t.Fatal(err)
	}
	charAppend(t, nk, charEvent("v1", now))

	urls := []string{"https://cdn.example/1.jpg", "https://cdn.example/2.jpg"}
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew", "s1", urls); err != nil {
		t.Fatal(err)
	}
	doc, _ := readLedgerDoc(ctx, nk, "crew")
	fromDoc, err := decodeStreamSessionData(doc.Events[0].Data)
	if err != nil || len(fromDoc.SnapshotURLs) != 2 || fromDoc.Title != "T" {
		t.Fatalf("document data = %+v err=%v", fromDoc, err)
	}
	events, _ := store.Between(ctx, "crew", now.UnixMilli(), now.UnixMilli(), []string{"stream_session"})
	if len(events) != 1 {
		t.Fatalf("store events = %d", len(events))
	}
	fromStore, err := decodeStreamSessionData(events[0].Data)
	if err != nil || len(fromStore.SnapshotURLs) != 2 || fromStore.Title != "T" || fromStore.SessionID != "sess_s1" {
		t.Fatalf("store data = %+v err=%v", fromStore, err)
	}

	// Before the cutover an event that only the document holds is still
	// updated there. An event that no store holds is an error.
	nk.put(CrewEventsCollection, "crew2", SystemUserID, CrewEventLedger{CrewID: "crew2", Events: []CrewEvent{ev}})
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew2", "s1", urls); err != nil {
		t.Fatalf("update of a document-only event: %v", err)
	}
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew", "missing", urls); err == nil {
		t.Fatal("update of an unknown event gave no error")
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

// Before the cutover the document is the truth.

func TestBeforeCutoverStoreFailureDoesNotFailTheWrite(t *testing.T) {
	store := newMemLedgerStore()
	useLedger(t, failingLedger{memLedgerStore: store, failAppend: true, failDelete: true})
	resetLedgerCutover(t)
	nk := newFakeGuestNk()

	charAppend(t, nk, charEvent("a", time.Now()))
	if got := docIDs(nk, "crew"); !charSameIDs(got, "a") {
		t.Fatalf("document ids = %v, want [a]", got)
	}
	if err := removeCrewEvent(context.Background(), nk, "crew", "a"); err != nil {
		t.Fatalf("remove with a failing store: %v", err)
	}
	if got := docIDs(nk, "crew"); len(got) != 0 {
		t.Fatalf("document ids after remove = %v, want none", got)
	}
}

func TestBeforeCutoverDocumentFailureWritesNothingToTheStore(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	nk := failDocNk{newFakeGuestNk()}

	if err := AppendCrewEvent(context.Background(), nk, "crew", charEvent("a", time.Now())); err == nil {
		t.Fatal("append with a failing document gave no error")
	}
	if got := storeIDs(t, store, "crew"); len(got) != 0 {
		t.Fatalf("store ids = %v, want none: the document is the truth before the cutover", got)
	}
}

// After the cutover the store is the truth.

func TestAfterCutoverStoreAppendFailureReturnsAnError(t *testing.T) {
	useStoreReads(t)
	useLedger(t, failingLedger{memLedgerStore: newMemLedgerStore(), failAppend: true})
	nk := newFakeGuestNk()

	if err := AppendCrewEvent(context.Background(), nk, "crew", charEvent("a", time.Now())); err == nil {
		t.Fatal("append with a failing store gave no error after the cutover")
	}
	if got := docIDs(nk, "crew"); len(got) != 0 {
		t.Fatalf("document ids = %v, want none: the store is written first", got)
	}
}

func TestAfterCutoverDocumentFailureIsLoggedOnly(t *testing.T) {
	store := useStoreReads(t)
	nk := failDocNk{newFakeGuestNk()}

	if err := AppendCrewEvent(context.Background(), nk, "crew", charEvent("a", time.Now())); err != nil {
		t.Fatalf("append with a failing document after the cutover: %v", err)
	}
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "a") {
		t.Fatalf("store ids = %v, want [a]", got)
	}
	ledger, _ := readLedger(context.Background(), nk, "crew")
	if len(ledger.Events) != 1 || ledger.Events[0].ID != "a" {
		t.Fatalf("readers see %+v, want the event", ledger.Events)
	}
}

func TestAfterCutoverStoreRemoveAndUpdateFailuresReturnAnError(t *testing.T) {
	useStoreReads(t)
	store := newMemLedgerStore()
	useLedger(t, failingLedger{memLedgerStore: store, failDelete: true})
	ctx := context.Background()
	nk := newFakeGuestNk()
	if err := AppendCrewEvent(ctx, nk, "crew", streamEvent("s1", time.Now())); err != nil {
		t.Fatal(err)
	}

	if err := removeCrewEvent(ctx, nk, "crew", "s1"); err == nil {
		t.Fatal("remove with a failing store gave no error after the cutover")
	}
	if got := docIDs(nk, "crew"); !charSameIDs(got, "s1") {
		t.Fatalf("document ids = %v, want [s1]: the store is written first", got)
	}
	// The store does not hold the event: an error, and the document is not
	// touched.
	nk.put(CrewEventsCollection, "crew2", SystemUserID, CrewEventLedger{CrewID: "crew2", Events: []CrewEvent{streamEvent("s2", time.Now())}})
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew2", "s2", []string{"https://cdn.example/1.jpg"}); err == nil {
		t.Fatal("update of an event the store does not hold gave no error after the cutover")
	}
	doc, _ := readLedgerDoc(ctx, nk, "crew2")
	if d, _ := decodeStreamSessionData(doc.Events[0].Data); len(d.SnapshotURLs) != 0 {
		t.Fatalf("document was updated: %+v", d)
	}
}

// serialNk makes every storage read and write of fakeQuestsNk one step, so a
// version check and its write cannot interleave with another write.
type serialNk struct {
	*fakeQuestsNk
	big sync.Mutex
}

func (s *serialNk) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	s.big.Lock()
	defer s.big.Unlock()
	return s.fakeQuestsNk.StorageRead(ctx, reads)
}

func (s *serialNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	s.big.Lock()
	defer s.big.Unlock()
	return s.fakeQuestsNk.StorageWrite(ctx, writes)
}

// losingNk is a module that checks storage versions, where the document
// write of some events loses every retry: another writer always got there
// first. lose names those events by the id of the event the write appends.
type losingNk struct {
	*serialNk
	lose func(eventID string) bool
}

func (l *losingNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	for _, w := range writes {
		if w.Collection != CrewEventsCollection {
			continue
		}
		var doc CrewEventLedger
		if err := json.Unmarshal([]byte(w.Value), &doc); err == nil && len(doc.Events) > 0 && l.lose(doc.Events[len(doc.Events)-1].ID) {
			return nil, errors.New("storage write rejected: version check failed")
		}
	}
	return l.serialNk.StorageWrite(ctx, writes)
}

func newLosingNk() *losingNk {
	return &losingNk{
		serialNk: &serialNk{fakeQuestsNk: newFakeQuestsNk()},
		// Every fourth event loses.
		lose: func(eventID string) bool {
			return eventID == "e03" || eventID == "e07" || eventID == "e11" || eventID == "e15"
		},
	}
}

// concurrentAppends appends n events to one crew at once, on a module that
// checks storage versions. It returns the error of each append.
func concurrentAppends(t *testing.T, nk runtime.NakamaModule, n int) []error {
	t.Helper()
	now := time.Now()
	// The document exists, so every write carries a version. The first write
	// of a document has none (specs/16-CREW-EVENT-LEDGER.md section 1).
	if err := AppendCrewEvent(context.Background(), nk, "crew", charEvent("seed", now)); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ev := charEvent(fmt.Sprintf("e%02d", i), now.Add(time.Duration(i+1)*time.Millisecond))
			errs[i] = AppendCrewEvent(context.Background(), nk, "crew", ev)
		}(i)
	}
	wg.Wait()
	return errs
}

func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// 16 appends at once. The document write of 4 of them loses every retry, the
// case that lost events before the durable store (plan section 1).
func TestConcurrentAppendsLoseNothing(t *testing.T) {
	const n, losers = 16, 4

	t.Run("after the cutover", func(t *testing.T) {
		store := useStoreReads(t)
		nk := newLosingNk()
		errs := concurrentAppends(t, nk, n)

		// The store is the truth: every append succeeds and is kept, also
		// the ones whose document write lost every retry.
		for i, err := range errs {
			if err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}
		inStore := idSet(storeIDs(t, store, "crew"))
		if len(inStore) != n+1 {
			t.Fatalf("store holds %d events, want %d", len(inStore), n+1)
		}
		if view := ledgerIDsOf(nk, "crew"); len(view) != n+1 {
			t.Fatalf("readers see %d events, want %d", len(view), n+1)
		}
		// The document holds no event twice, nothing the store lacks, and
		// not the events that lost.
		inDoc := docIDs(nk, "crew")
		if len(idSet(inDoc)) != len(inDoc) || len(inDoc) > n+1-losers || len(inDoc) < 2 {
			t.Fatalf("document holds %d events: %v", len(inDoc), inDoc)
		}
		for _, id := range inDoc {
			if !inStore[id] || nk.lose(id) {
				t.Fatalf("document holds %s", id)
			}
		}
	})

	t.Run("before the cutover", func(t *testing.T) {
		store := useMemLedger(t)
		resetLedgerCutover(t)
		nk := newLosingNk()
		errs := concurrentAppends(t, nk, n)

		// The document is the truth: an append that reports success is in
		// the document and in the store. One that reports an error is in
		// neither, so the store never shows more than the document.
		inDoc := idSet(docIDs(nk, "crew"))
		inStore := idSet(storeIDs(t, store, "crew"))
		ok := 0
		for i, err := range errs {
			id := fmt.Sprintf("e%02d", i)
			if err == nil {
				ok++
			}
			if nk.lose(id) && err == nil {
				t.Errorf("append %s lost every retry and reports success", id)
			}
			if inDoc[id] != (err == nil) || inStore[id] != (err == nil) {
				t.Errorf("append %s: err=%v in document=%v in store=%v", id, err, inDoc[id], inStore[id])
			}
		}
		if ok == 0 || ok > n-losers {
			t.Fatalf("%d appends succeeded, want 1 to %d", ok, n-losers)
		}
		if len(inDoc) != ok+1 || len(inStore) != ok+1 {
			t.Fatalf("document holds %d, store holds %d, want %d", len(inDoc), len(inStore), ok+1)
		}
	})
}
