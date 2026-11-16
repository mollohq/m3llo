package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func migEvent(id, typ string, ts time.Time) CrewEvent {
	return CrewEvent{ID: id, Type: typ, ActorID: "u1", Timestamp: ts.UnixMilli(), Score: 5,
		Data: map[string]interface{}{"game_name": "Game", "duration_min": float64(10)}}
}

func viewIDs(t *testing.T, store ledgerStore, crewID string) []string {
	t.Helper()
	view, err := store.RecentView(context.Background(), crewID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(view))
	for _, e := range view {
		ids = append(ids, e.ID)
	}
	return ids
}

// noSoak makes the marker wait for no soak time.
func noSoak(t *testing.T) {
	t.Helper()
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "0")
}

// reconcilePass runs one full pass of the reconcile on the current store.
func reconcilePass(t *testing.T, nk runtime.NakamaModule) *ledgerReconciler {
	t.Helper()
	r := &ledgerReconciler{nk: nk, logger: testLogger()}
	for i := 0; !r.tick(context.Background()); i++ {
		if i > 100 {
			t.Fatal("the reconcile pass did not end")
		}
	}
	return r
}

func markerSet(nk runtime.NakamaModule) bool {
	st, _, err := readLedgerMigrationState(context.Background(), nk)
	return err == nil && st != nil && st.EventsDoneMs > 0
}

// setMarker puts the install after the cutover.
func setMarker(t *testing.T, nk runtime.NakamaModule) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := writeLedgerMigrationState(context.Background(), nk, ledgerMigrationState{DualWriteStartedMs: now, EventsDoneMs: now}, ""); err != nil {
		t.Fatal(err)
	}
}

func putDoc(nk *fakeGuestNk, crewID string, updatedAt time.Time, events ...CrewEvent) {
	nk.put(CrewEventsCollection, crewID, SystemUserID, CrewEventLedger{CrewID: crewID, UpdatedAt: updatedAt.UnixMilli(), Events: events})
}

// seedLegacyDocs writes five crews of legacy documents. crew_dormant had its
// last append 60 days ago.
func seedLegacyDocs(nk *fakeGuestNk, now time.Time) {
	for _, crewID := range []string{"crew_a", "crew_b", "crew_c", "crew_d"} {
		putDoc(nk, crewID, now,
			migEvent(crewID+"_1", "game_session", now.Add(-6*charDay)),
			migEvent(crewID+"_2", "voice_session", now.Add(-1*charDay)),
			// Appended last with an older timestamp: document order is not time order.
			migEvent(crewID+"_3", "game_session", now.Add(-3*charDay)),
		)
	}
	last := now.Add(-60 * charDay)
	putDoc(nk, "crew_dormant", last,
		migEvent("dormant_1", "member_joined", last.Add(-5*charDay)),
		migEvent("dormant_2", "stream_session", last),
	)
}

func TestBackfillIsIdempotent(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	now := time.Now()
	seedLegacyDocs(nk, now)

	r := reconcilePass(t, nk)
	if r.last.Crews != 5 || r.last.Inserted != 14 || !r.last.clean() || r.lastMarker != "set" {
		t.Fatalf("first pass = %+v marker=%s", r.last, r.lastMarker)
	}
	st, _, _ := readLedgerMigrationState(ctx, nk)
	if st == nil || st.EventsDoneMs <= 0 || st.DualWriteStartedMs <= 0 {
		t.Fatalf("state = %+v", st)
	}
	for _, crewID := range []string{"crew_a", "crew_b", "crew_c", "crew_d"} {
		if got := viewIDs(t, store, crewID); !charSameIDs(got, crewID+"_1", crewID+"_2", crewID+"_3") {
			t.Fatalf("view of %s = %v, want the document order", crewID, got)
		}
		if got := storeIDs(t, store, crewID); !charSameIDs(got, crewID+"_1", crewID+"_3", crewID+"_2") {
			t.Fatalf("store of %s = %v", crewID, got)
		}
	}
	// The dormant crew keeps the view of its last append.
	if got := viewIDs(t, store, "crew_dormant"); !charSameIDs(got, "dormant_1", "dormant_2") {
		t.Fatalf("view of the dormant crew = %v", got)
	}
	events, _ := store.Between(ctx, "crew_a", 0, now.UnixMilli(), nil)
	if e := events[0]; e.CrewID != "crew_a" || e.Type != "game_session" || e.ActorID != "u1" || e.Score != 5 {
		t.Fatalf("backfilled event = %+v", e)
	}
	if obj, ok := events[0].Data.(map[string]interface{}); !ok || obj["game_name"] != "Game" {
		t.Fatalf("backfilled data = %#v", events[0].Data)
	}

	// A second pass changes nothing and keeps the marker of the first pass.
	r = reconcilePass(t, nk)
	if r.last.Inserted != 0 || r.last.Pruned != 0 || r.last.Updated != 0 || r.lastMarker != "set_before" {
		t.Fatalf("second pass = %+v marker=%s", r.last, r.lastMarker)
	}
	if again, _, _ := readLedgerMigrationState(ctx, nk); again == nil || *again != *st {
		t.Fatalf("state changed: %+v, was %+v", again, st)
	}
}

func TestBackfillAfterDualWriteAddsOnlyTheOlderEvents(t *testing.T) {
	noSoak(t)
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	now := time.Now()

	// Before the durable store: two events in the document only.
	putDoc(nk, "crew", now.Add(-time.Hour),
		migEvent("before_1", "game_session", now.Add(-5*charDay)),
		migEvent("before_2", "voice_session", now.Add(-2*charDay)))
	// After the deploy: dual write.
	charAppend(t, nk, charEvent("live_1", now))
	charAppend(t, nk, charEvent("live_2", now))
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "live_1", "live_2") {
		t.Fatalf("view before the reconcile = %v", got)
	}

	r := reconcilePass(t, nk)
	if r.last.Inserted != 2 || r.lastMarker != "set" {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
	// The older events go before the live ones, as in the document.
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "before_1", "before_2", "live_1", "live_2") {
		t.Fatalf("view after the reconcile = %v, document = %v", got, docIDs(nk, "crew"))
	}
}

// A document that holds clip and weekly_recap events (the dev seed, an old
// crew) reads the same after the cutover.
func TestReconcileKeepsTheLegacyTypesOfADocument(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()
	events := []CrewEvent{
		migEvent("clip_1", "clip", now.Add(-2*charDay)),
		migEvent("game_1", "game_session", now.Add(-1*charDay)),
		migEvent("recap_1", "weekly_recap", now),
	}
	for i := range events {
		events[i].CrewID = "crew" // as every event that AppendCrewEvent wrote
	}
	putDoc(nk, "crew", now, events...)

	before, _ := readLedger(ctx, nk, "crew")
	if r := reconcilePass(t, nk); r.lastMarker != "set" || r.last.Inserted != 3 {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
	if ledgerTruthStore(ctx, nk) == nil {
		t.Fatal("the store is not the truth after the marker")
	}
	after, _ := readLedger(ctx, nk, "crew")
	if !reflect.DeepEqual(before.Events, after.Events) {
		t.Fatalf("readers see another ledger after the cutover\nbefore: %+v\nafter:  %+v", before.Events, after.Events)
	}
	// The store keeps them apart from the clip and recap rows of phase 1b.
	if got, _ := store.Between(ctx, "crew", 0, now.UnixMilli(), []string{"clip", "weekly_recap"}); len(got) != 0 {
		t.Fatalf("the store holds %d rows of the types clip and weekly_recap", len(got))
	}
	if got, _ := store.NewestN(ctx, "crew", ledgerCountedTypes, now.UnixMilli()+1, 10); len(got) != 1 || got[0].ID != "game_1" {
		t.Fatalf("counted events = %+v, want only game_1", got)
	}
}

func TestPlanLedgerDocAnchor(t *testing.T) {
	now := time.Now()
	ev := []CrewEvent{migEvent("a", "game_session", now.Add(-6*charDay)), migEvent("b", "game_session", now.Add(-1*charDay))}

	// The usual document: the anchor is the time of the last write.
	plan := planLedgerDoc(&CrewEventLedger{CrewID: "crew", Events: ev, UpdatedAt: now.UnixMilli()})
	if plan.AnchorMs != now.UnixMilli() || len(plan.Events) != 2 || plan.Events[0].CrewID != "crew" {
		t.Fatalf("plan = %+v", plan)
	}
	if !plan.HasWindow || plan.WindowFromMs != now.UnixMilli()-ledgerRecentWindowMs {
		t.Fatalf("window = %d %v", plan.WindowFromMs, plan.HasWindow)
	}
	// A snapshot update wrote the document 3 days after the last append. The
	// anchor stays early enough to keep the oldest event in the view.
	plan = planLedgerDoc(&CrewEventLedger{Events: ev, UpdatedAt: now.Add(3 * charDay).UnixMilli()})
	if want := now.Add(-6*charDay).UnixMilli() + ledgerRecentWindowMs; plan.AnchorMs != want {
		t.Fatalf("anchor = %d, want %d", plan.AnchorMs, want)
	}
	// A document with no write time.
	plan = planLedgerDoc(&CrewEventLedger{Events: ev})
	if want := now.Add(-6*charDay).UnixMilli() + ledgerRecentWindowMs; plan.AnchorMs != want {
		t.Fatalf("anchor with no write time = %d, want %d", plan.AnchorMs, want)
	}
	// A document that a remove left empty still gives its window.
	plan = planLedgerDoc(&CrewEventLedger{UpdatedAt: now.UnixMilli()})
	if plan.AnchorMs != now.UnixMilli() || !plan.HasWindow || len(plan.Events) != 0 {
		t.Fatalf("plan of an emptied document = %+v", plan)
	}
	// An empty document with no write time gives nothing.
	plan = planLedgerDoc(&CrewEventLedger{})
	if plan.AnchorMs != 0 || plan.HasWindow {
		t.Fatalf("plan of an empty document = %+v", plan)
	}
	// An event without an id is counted, not copied. A repeated id is
	// copied once. A type that is not a pulse type gets its stored type.
	plan = planLedgerDoc(&CrewEventLedger{Events: []CrewEvent{
		migEvent("", "game_session", now), migEvent("x", "clip", now), migEvent("x", "clip", now),
	}})
	if plan.NoID != 1 || len(plan.Events) != 1 || plan.Events[0].Type != "ledger_clip" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestLedgerMergeOrder(t *testing.T) {
	cases := []struct {
		name         string
		view, doc    []string
		isNew        []string
		docOrderWins bool
		want         []string
	}{
		{"new event in the middle", []string{"a", "c", "b"}, []string{"a", "b", "c"}, []string{"b"}, false, []string{"a", "b", "c"}},
		{"new event at the end", []string{"a", "b", "c"}, []string{"a", "b", "c"}, []string{"c"}, false, []string{"a", "b", "c"}},
		{"new event with nothing before it", []string{"b", "c", "a"}, []string{"a", "b"}, []string{"a"}, false, []string{"a", "b", "c"}},
		{"new events in a row", []string{"a", "d", "b", "c"}, []string{"a", "b", "c", "d"}, []string{"b", "c"}, false, []string{"a", "b", "c", "d"}},
		{"store-only event keeps its place", []string{"a", "s", "c", "b"}, []string{"a", "b", "c"}, []string{"b"}, false, []string{"a", "b", "s", "c"}},
		{"old events keep their order", []string{"b", "a"}, []string{"a", "b"}, nil, false, []string{"b", "a"}},
		{"document order wins", []string{"b", "s", "a"}, []string{"a", "b"}, nil, true, []string{"s", "a", "b"}},
		{"document repeats an id", []string{"a", "b"}, []string{"b", "a", "b"}, nil, true, []string{"b", "a"}},
	}
	for _, c := range cases {
		got := ledgerMergeOrder(c.view, c.doc, idSet(c.isNew), c.docOrderWins)
		if !charSameIDs(got, c.want...) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// Before the cutover the document is the truth: the reconcile repairs every
// difference of the 7-day view, and leaves the older history alone.
func TestReconcileRepairsTheStoreBeforeTheCutover(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	now := time.Now()

	// Dual write of five events and one that is 30 days old.
	setLedgerClock(t, now.Add(-30*charDay))
	charAppend(t, nk, charEvent("history", now.Add(-30*charDay)))
	setLedgerClock(t, now)
	for _, id := range []string{"keep", "removed", "s1"} {
		ev := charEvent(id, now.Add(-time.Hour))
		if id == "s1" {
			ev = streamEvent(id, now.Add(-time.Hour))
		}
		charAppend(t, nk, ev)
	}

	// Three store writes failed.
	useLedger(t, failingLedger{memLedgerStore: store, failAppend: true, failDelete: true})
	charAppend(t, nk, charEvent("append_failed", now))
	if err := removeCrewEvent(ctx, nk, "crew", "removed"); err != nil {
		t.Fatal(err)
	}
	useLedger(t, failingUpdates{store})
	urls := []string{"https://cdn.example/1.jpg"}
	if err := UpdateLedgerEventSnapshotURLs(ctx, nk, "crew", "s1", urls); err != nil {
		t.Fatal(err)
	}
	useLedger(t, store)
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "keep", "removed", "s1") {
		t.Fatalf("view before the reconcile = %v", got)
	}

	r := reconcilePass(t, nk)
	if r.last.Inserted != 1 || r.last.Pruned != 1 || r.last.Updated != 1 || !r.last.clean() || r.lastMarker != "set" {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
	if got, want := viewIDs(t, store, "crew"), docIDs(nk, "crew"); !charSameIDs(got, want...) || len(got) != 3 {
		t.Fatalf("view after the reconcile = %v, document = %v", got, want)
	}
	view, _ := store.RecentView(ctx, "crew")
	if d, err := decodeStreamSessionData(view[1].Data); err != nil || len(d.SnapshotURLs) != 1 {
		t.Fatalf("data of s1 after the reconcile = %+v err=%v", d, err)
	}
	// The event the document trimmed long ago is still in the store.
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "history", "keep", "s1", "append_failed") {
		t.Fatalf("store after the reconcile = %v", got)
	}
}

// failingUpdates fails UpdateData.
type failingUpdates struct{ *memLedgerStore }

func (failingUpdates) UpdateData(context.Context, string, string, func(json.RawMessage) (json.RawMessage, error)) error {
	return errors.New("store down")
}

// After the cutover the store is the truth: the reconcile only inserts.
func TestReconcileAfterTheCutoverIsInsertOnly(t *testing.T) {
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useStoreReads(t)
	setMarker(t, nk)
	now := time.Now()

	// The store and the document hold a, b and c.
	for _, id := range []string{"a", "b", "c"} {
		charAppend(t, nk, streamEvent(id, now.Add(-time.Hour)))
	}
	// Only the store holds store_only (its document write failed) and the
	// snapshot URLs of a.
	if _, err := store.Append(ctx, charEvent("store_only", now), ""); err != nil {
		t.Fatal(err)
	}
	if err := ledgerStoreSetSnapshotURLs(ctx, store, "crew", "a", []string{"https://cdn.example/1.jpg"}); err != nil {
		t.Fatal(err)
	}
	// An instance of the previous release appended doc_only between a and b,
	// and the document has another order for b and c.
	doc, _ := readLedgerDoc(ctx, nk, "crew")
	doc.Events = []CrewEvent{doc.Events[0], migEvent("doc_only", "game_session", now.Add(-time.Minute)), doc.Events[2], doc.Events[1]}
	putDoc(nk, "crew", now, doc.Events...)

	r := reconcilePass(t, nk)
	if r.last.Inserted != 1 || r.last.Pruned != 0 || r.last.Updated != 0 || r.lastMarker != "set_before" {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
	// doc_only is in, right after a as in the document. Nothing else moved,
	// nothing is gone, and the data of the store stays.
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "a", "doc_only", "b", "c", "store_only") {
		t.Fatalf("view after the reconcile = %v", got)
	}
	view, _ := store.RecentView(ctx, "crew")
	if d, err := decodeStreamSessionData(view[0].Data); err != nil || len(d.SnapshotURLs) != 1 {
		t.Fatalf("data of a after the reconcile = %+v err=%v", d, err)
	}
	if got := ledgerIDsOf(nk, "crew"); len(got) != 5 {
		t.Fatalf("readers see %v", got)
	}
	// The next pass has nothing to do.
	if r = reconcilePass(t, nk); r.last.Inserted != 0 {
		t.Fatalf("second pass = %+v", r.last)
	}
}

// hookLedger runs a function once, before the first Backfill or the first
// RecentView. It stands for a write of another request in the middle of a
// reconcile round.
type hookLedger struct {
	*memLedgerStore
	beforeBackfill func()
	beforeView     func()
}

func (h *hookLedger) Backfill(ctx context.Context, crewID string, events []CrewEvent, anchorMs int64, docOrderWins bool) (int, error) {
	if fn := h.beforeBackfill; fn != nil {
		h.beforeBackfill = nil
		fn()
	}
	return h.memLedgerStore.Backfill(ctx, crewID, events, anchorMs, docOrderWins)
}

func (h *hookLedger) RecentView(ctx context.Context, crewID string) ([]CrewEvent, error) {
	if fn := h.beforeView; fn != nil {
		h.beforeView = nil
		fn()
	}
	return h.memLedgerStore.RecentView(ctx, crewID)
}

// A remove between the document read of a round and its insert must not
// bring the event back.
func TestReconcileDoesNotResurrectAnEventRemovedDuringARound(t *testing.T) {
	for _, afterCutover := range []bool{false, true} {
		name := "before the cutover"
		if afterCutover {
			name = "after the cutover"
		}
		t.Run(name, func(t *testing.T) {
			noSoak(t)
			ctx := context.Background()
			nk := newFakeGuestNk()
			mem := newMemLedgerStore()
			if afterCutover {
				useStoreReads(t)
				setMarker(t, nk)
			} else {
				resetLedgerCutover(t)
			}
			store := &hookLedger{memLedgerStore: mem}
			useLedger(t, store)
			now := time.Now()

			// The document holds two events that the store lacks.
			putDoc(nk, "crew", now, streamEvent("stays", now), streamEvent("removed", now))
			store.beforeBackfill = func() {
				if err := removeCrewEvent(ctx, nk, "crew", "removed"); err != nil {
					t.Errorf("remove: %v", err)
				}
			}

			reconcilePass(t, nk)
			if store.beforeBackfill != nil {
				t.Fatal("the remove did not run")
			}
			if got := storeIDs(t, mem, "crew"); !charSameIDs(got, "stays") {
				t.Fatalf("store after the reconcile = %v, want [stays]", got)
			}
			if got := docIDs(nk, "crew"); !charSameIDs(got, "stays") {
				t.Fatalf("document after the reconcile = %v, want [stays]", got)
			}
		})
	}
}

// An append between the document read of a round and its store read looks
// like an event the document does not hold. The round deletes it. The next
// round reads the document again and puts it back.
func TestReconcileRedoesACrewThatChangedDuringARound(t *testing.T) {
	noSoak(t)
	nk := newFakeGuestNk()
	mem := newMemLedgerStore()
	store := &hookLedger{memLedgerStore: mem}
	useLedger(t, store)
	resetLedgerCutover(t)
	now := time.Now()

	charAppend(t, nk, charEvent("a", now))
	store.beforeView = func() { charAppend(t, nk, charEvent("appended_during_the_round", now)) }

	r := reconcilePass(t, nk)
	if store.beforeView != nil {
		t.Fatal("the append did not run")
	}
	if got := viewIDs(t, mem, "crew"); !charSameIDs(got, "a", "appended_during_the_round") {
		t.Fatalf("view after the reconcile = %v", got)
	}
	if !r.last.clean() || r.lastMarker != "set" {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
}

// After the cutover an event that only the document holds comes from an
// instance of the previous release. The reconcile picks it up.
func TestReconcilePicksUpADocumentOnlyEventAfterTheMarker(t *testing.T) {
	nk := newFakeGuestNk()
	store := useStoreReads(t)
	setMarker(t, nk)
	now := time.Now()

	charAppend(t, nk, charEvent("first", now))
	// The old instance appends to the document only.
	if err := appendLedgerDoc(context.Background(), nk, "crew", charEvent("from_old_instance", now)); err != nil {
		t.Fatal(err)
	}
	charAppend(t, nk, charEvent("last", now))
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "first", "last") {
		t.Fatalf("readers before the reconcile see %v", got)
	}

	if r := reconcilePass(t, nk); r.last.Inserted != 1 {
		t.Fatalf("pass = %+v", r.last)
	}
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "first", "from_old_instance", "last") {
		t.Fatalf("readers after the reconcile see %v, want the document order", got)
	}
	_ = store
}

func TestFailedStoreDeleteBeforeTheCutoverIsRepaired(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	now := time.Now()
	charAppend(t, nk, streamEvent("s1", now))
	charAppend(t, nk, charEvent("v1", now))

	useLedger(t, failingLedger{memLedgerStore: store, failDelete: true})
	if err := removeCrewEvent(ctx, nk, "crew", "s1"); err != nil {
		t.Fatalf("remove with a failing store: %v", err)
	}
	useLedger(t, store)
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "s1", "v1") {
		t.Fatalf("view before the reconcile = %v", got)
	}

	r := reconcilePass(t, nk)
	if r.last.Pruned != 1 || r.lastMarker != "set" {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "v1") {
		t.Fatalf("store after the reconcile = %v, want [v1]", got)
	}
}

// The marker waits until dual write has run for the soak time.
func TestSoakGate(t *testing.T) {
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "")
	now := time.Now()
	seedLegacyDocs(nk, now)

	// The first pass records the start of dual write. It is clean, but the
	// soak time has not passed.
	r := reconcilePass(t, nk)
	if !r.last.clean() || r.last.Inserted != 14 {
		t.Fatalf("first pass = %+v", r.last)
	}
	st, version, _ := readLedgerMigrationState(ctx, nk)
	if st == nil || st.DualWriteStartedMs < now.UnixMilli() || st.EventsDoneMs != 0 {
		t.Fatalf("state after the first pass = %+v", st)
	}
	if markerSet(nk) || ledgerTruthStore(ctx, nk) != nil {
		t.Fatalf("marker set before the soak time passed (marker=%s)", r.lastMarker)
	}

	// 23 hours of dual write: not yet.
	started := st.DualWriteStartedMs
	st.DualWriteStartedMs = now.Add(-23 * time.Hour).UnixMilli()
	if err := writeLedgerMigrationState(ctx, nk, *st, version); err != nil {
		t.Fatal(err)
	}
	if reconcilePass(t, nk); markerSet(nk) {
		t.Fatal("marker set after 23 hours")
	}

	// 25 hours, but one crew does not reconcile: not yet.
	st.DualWriteStartedMs = now.Add(-25 * time.Hour).UnixMilli()
	if err := writeLedgerMigrationState(ctx, nk, *st, ""); err != nil {
		t.Fatal(err)
	}
	useLedger(t, failingBackfill{store})
	putDoc(nk, "crew_new", now, migEvent("n1", "game_session", now))
	if r = reconcilePass(t, nk); markerSet(nk) || r.last.Failed != 1 {
		t.Fatalf("marker set after a pass that was not clean: %+v marker=%s", r.last, r.lastMarker)
	}

	// 25 hours and a clean pass: the marker.
	useLedger(t, store)
	if r = reconcilePass(t, nk); !markerSet(nk) || r.lastMarker != "set" {
		t.Fatalf("marker not set after 25 hours and a clean pass: %+v marker=%s", r.last, r.lastMarker)
	}
	after, _, _ := readLedgerMigrationState(ctx, nk)
	if after.DualWriteStartedMs != st.DualWriteStartedMs || started == 0 {
		t.Fatalf("state = %+v", after)
	}

	// The env sets the soak time.
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "90")
	if ledgerSoak() != 90*time.Second {
		t.Fatalf("soak = %s, want 90s", ledgerSoak())
	}
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "")
	if ledgerSoak() != 24*time.Hour {
		t.Fatalf("soak = %s, want 24h", ledgerSoak())
	}
}

// failingBackfill fails Backfill.
type failingBackfill struct{ *memLedgerStore }

func (failingBackfill) Backfill(context.Context, string, []CrewEvent, int64, bool) (int, error) {
	return 0, errors.New("store down")
}

// failReadNk fails the read of the legacy document of one crew.
type failReadNk struct {
	*fakeGuestNk
	crewID string
}

func (f failReadNk) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	for _, r := range reads {
		if r.Collection == CrewEventsCollection && r.Key == f.crewID {
			return nil, errors.New("storage read failed")
		}
	}
	return f.fakeGuestNk.StorageRead(ctx, reads)
}

// A document that cannot be read is not an empty document: the pass is not
// clean and the store of that crew is not touched.
func TestReconcileFailedDocumentReadBlocksTheMarker(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	base := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	now := time.Now()
	seedLegacyDocs(base, now)
	// The store holds an event of crew_b. With an empty document as the
	// truth the reconcile would delete it.
	inStore := migEvent("crew_b_1", "game_session", now.Add(-6*charDay))
	inStore.CrewID = "crew_b"
	if _, err := store.Append(ctx, inStore, ""); err != nil {
		t.Fatal(err)
	}

	r := reconcilePass(t, failReadNk{fakeGuestNk: base, crewID: "crew_b"})
	if r.last.Failed != 1 || r.last.clean() || markerSet(base) {
		t.Fatalf("pass = %+v marker=%s, want one failed crew and no marker", r.last, r.lastMarker)
	}
	if got := storeIDs(t, store, "crew_b"); !charSameIDs(got, "crew_b_1") {
		t.Fatalf("store of crew_b = %v, want it untouched", got)
	}
	// A document that does not decode is the same.
	base.mu.Lock()
	base.storage[fakeStorageKey(CrewEventsCollection, "crew_c", SystemUserID)] = "{not json"
	base.mu.Unlock()
	if r = reconcilePass(t, base); r.last.Failed != 1 || markerSet(base) {
		t.Fatalf("pass with a broken document = %+v marker=%s", r.last, r.lastMarker)
	}

	// The read works again: the next pass sets the marker.
	seedLegacyDocs(base, now)
	if r = reconcilePass(t, base); !markerSet(base) {
		t.Fatalf("pass after the read works = %+v marker=%s", r.last, r.lastMarker)
	}
}

// An event without an id cannot go into the store. It is counted and does
// not block the marker.
func TestReconcileEventWithoutIDDoesNotBlockTheMarker(t *testing.T) {
	noSoak(t)
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	now := time.Now()
	putDoc(nk, "crew", now, migEvent("", "game_session", now), migEvent("ok", "game_session", now))

	r := reconcilePass(t, nk)
	if r.last.NoID != 1 || !r.last.clean() || !markerSet(nk) {
		t.Fatalf("pass = %+v marker=%s", r.last, r.lastMarker)
	}
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "ok") {
		t.Fatalf("store = %v, want [ok]", got)
	}
}

// With no store the reconcile does nothing and makes no state document.
func TestReconcileWithoutAStoreDoesNothing(t *testing.T) {
	useLedger(t, nil)
	nk := newFakeGuestNk()
	seedLegacyDocs(nk, time.Now())
	r := &ledgerReconciler{nk: nk, logger: testLogger()}
	if r.tick(context.Background()) {
		t.Fatal("a tick with no store ended a pass")
	}
	if st, _, _ := readLedgerMigrationState(context.Background(), nk); st != nil {
		t.Fatalf("state document made with no store: %+v", st)
	}
}

// A failed schema setup is tried again until it works.
func TestStartLedgerStoreRetriesTheSchema(t *testing.T) {
	useLedger(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := newMemLedgerStore()
	ready := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	setup := func(context.Context) (ledgerStore, error) {
		if calls.Add(1) < 3 {
			return nil, errors.New("database not ready")
		}
		once.Do(func() { close(ready) })
		return store, nil
	}
	startLedgerStore(ctx, testLogger(), time.Millisecond, setup)
	if currentLedger() != nil {
		t.Fatal("the store is set after a failed setup")
	}
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the schema setup was not tried again")
	}
	// setLedger runs right after the setup returns.
	deadline := time.Now().Add(3 * time.Second)
	for currentLedger() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the store was not set after the setup succeeded")
		}
		time.Sleep(time.Millisecond)
	}
	if currentLedger() != ledgerStore(store) {
		t.Fatal("another store is set")
	}

	// A setup that works at once sets the store before it returns.
	setLedger(nil)
	startLedgerStore(ctx, testLogger(), time.Millisecond, func(context.Context) (ledgerStore, error) { return store, nil })
	if currentLedger() == nil {
		t.Fatal("the store is not set after a setup that worked")
	}
}
