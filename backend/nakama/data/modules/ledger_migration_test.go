package main

import (
	"context"
	"testing"
	"time"
)

func noParityWait(t *testing.T) {
	t.Helper()
	prev := ledgerParityRetryWait
	ledgerParityRetryWait = 0
	t.Cleanup(func() { ledgerParityRetryWait = prev })
}

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

// seedLegacyDocs writes five crews of legacy documents. crew_dormant had its
// last append 60 days ago.
func seedLegacyDocs(nk *fakeGuestNk, now time.Time) {
	for _, crewID := range []string{"crew_a", "crew_b", "crew_c", "crew_d"} {
		nk.put(CrewEventsCollection, crewID, SystemUserID, CrewEventLedger{CrewID: crewID, UpdatedAt: now.UnixMilli(), Events: []CrewEvent{
			migEvent(crewID+"_1", "game_session", now.Add(-6*charDay)),
			migEvent(crewID+"_2", "voice_session", now.Add(-1*charDay)),
			// Appended last with an older timestamp: document order is not time order.
			migEvent(crewID+"_3", "game_session", now.Add(-3*charDay)),
		}})
	}
	last := now.Add(-60 * charDay)
	nk.put(CrewEventsCollection, "crew_dormant", SystemUserID, CrewEventLedger{CrewID: "crew_dormant", UpdatedAt: last.UnixMilli(), Events: []CrewEvent{
		migEvent("dormant_1", "member_joined", last.Add(-5*charDay)),
		migEvent("dormant_2", "stream_session", last),
	}})
}

func TestBackfillIsIdempotent(t *testing.T) {
	noParityWait(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := newMemLedgerStore()
	now := time.Now()
	seedLegacyDocs(nk, now)

	if !runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("first backfill did not set the marker")
	}
	st := readLedgerMigrationState(ctx, nk)
	if st == nil || st.EventsDoneMs <= 0 {
		t.Fatalf("marker = %+v", st)
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

	// A second run inserts nothing and keeps the marker of the first run.
	if !runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("second backfill reports no marker")
	}
	if got := storeIDs(t, store, "crew_a"); len(got) != 3 {
		t.Fatalf("store of crew_a after the second run = %v", got)
	}
	if again := readLedgerMigrationState(ctx, nk); again == nil || again.EventsDoneMs != st.EventsDoneMs {
		t.Fatalf("marker changed: %+v, was %+v", again, st)
	}
}

func TestBackfillAfterDualWriteAddsOnlyTheOlderEvents(t *testing.T) {
	noParityWait(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	now := time.Now()

	// Before the durable store: two events in the document only.
	nk.put(CrewEventsCollection, "crew", SystemUserID, CrewEventLedger{CrewID: "crew", UpdatedAt: now.Add(-time.Hour).UnixMilli(), Events: []CrewEvent{
		migEvent("before_1", "game_session", now.Add(-5*charDay)),
		migEvent("before_2", "voice_session", now.Add(-2*charDay)),
	}})
	// After the deploy: dual write.
	charAppend(t, nk, charEvent("live_1", now))
	charAppend(t, nk, charEvent("live_2", now))
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "live_1", "live_2") {
		t.Fatalf("view before the backfill = %v", got)
	}

	if !runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("backfill did not set the marker")
	}
	want := docIDs(nk, "crew")
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, want...) || len(got) != 4 {
		t.Fatalf("view after the backfill = %v, document = %v", got, want)
	}
}

func TestBackfillSkipsTypesTheDocumentNoLongerHolds(t *testing.T) {
	noParityWait(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := newMemLedgerStore()
	now := time.Now()
	nk.put(CrewEventsCollection, "crew", SystemUserID, CrewEventLedger{CrewID: "crew", UpdatedAt: now.UnixMilli(), Events: []CrewEvent{
		migEvent("clip_1", "clip", now.Add(-2*charDay)),
		migEvent("game_1", "game_session", now.Add(-1*charDay)),
		migEvent("recap_1", "weekly_recap", now),
	}})

	if !runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("backfill did not set the marker")
	}
	if got := storeIDs(t, store, "crew"); !charSameIDs(got, "game_1") {
		t.Fatalf("store = %v, want only the pulse event", got)
	}
}

func TestPlanLedgerBackfillAnchor(t *testing.T) {
	now := time.Now()
	ev := []CrewEvent{migEvent("a", "game_session", now.Add(-6*charDay)), migEvent("b", "game_session", now.Add(-1*charDay))}

	// The usual document: the anchor is the time of the last write.
	plan := planLedgerBackfill(&CrewEventLedger{Events: ev, UpdatedAt: now.UnixMilli()})
	if plan.AnchorMs != now.UnixMilli() || len(plan.Events) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	// A snapshot update wrote the document 3 days after the last append. The
	// anchor stays early enough to keep the oldest event in the view.
	plan = planLedgerBackfill(&CrewEventLedger{Events: ev, UpdatedAt: now.Add(3 * charDay).UnixMilli()})
	if want := now.Add(-6*charDay).UnixMilli() + ledgerRecentWindowMs; plan.AnchorMs != want {
		t.Fatalf("anchor = %d, want %d", plan.AnchorMs, want)
	}
	// A document with no write time.
	plan = planLedgerBackfill(&CrewEventLedger{Events: ev})
	if want := now.Add(-6*charDay).UnixMilli() + ledgerRecentWindowMs; plan.AnchorMs != want {
		t.Fatalf("anchor with no write time = %d, want %d", plan.AnchorMs, want)
	}
	// An empty document gives nothing to do.
	plan = planLedgerBackfill(&CrewEventLedger{})
	if plan.AnchorMs != 0 || len(plan.Events) != 0 {
		t.Fatalf("plan of an empty document = %+v", plan)
	}
	// An event without an id is counted, not copied.
	plan = planLedgerBackfill(&CrewEventLedger{Events: []CrewEvent{migEvent("", "game_session", now)}})
	if plan.NoID != 1 || len(plan.Events) != 0 {
		t.Fatalf("plan with an event without id = %+v", plan)
	}
}

func TestLedgerParityMismatchBlocksTheMarker(t *testing.T) {
	noParityWait(t)
	ctx := context.Background()
	now := time.Now()

	// The store shows an event that the document does not hold.
	nk := newFakeGuestNk()
	store := newMemLedgerStore()
	seedLegacyDocs(nk, now)
	extra := migEvent("only_in_store", "game_session", now)
	extra.CrewID = "crew_b"
	if _, err := store.Append(ctx, extra, ""); err != nil {
		t.Fatal(err)
	}
	if runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("backfill set the marker with an extra event in the store")
	}
	if readLedgerMigrationState(ctx, nk) != nil {
		t.Fatal("marker exists after a mismatch")
	}
	missing, extraIDs, err := ledgerParity(ctx, nk, store, "crew_b")
	if err != nil || len(missing) != 0 || !charSameIDs(extraIDs, "only_in_store") {
		t.Fatalf("parity of crew_b: missing=%v extra=%v err=%v", missing, extraIDs, err)
	}
	if missing, extraIDs, _ := ledgerParity(ctx, nk, store, "crew_a"); len(missing) != 0 || len(extraIDs) != 0 {
		t.Fatalf("parity of crew_a: missing=%v extra=%v", missing, extraIDs)
	}

	// The store lacks an event of the document: a pulse event without an id
	// cannot be copied.
	nk = newFakeGuestNk()
	store = newMemLedgerStore()
	nk.put(CrewEventsCollection, "crew", SystemUserID, CrewEventLedger{CrewID: "crew", UpdatedAt: now.UnixMilli(), Events: []CrewEvent{
		migEvent("", "game_session", now), migEvent("ok", "game_session", now),
	}})
	if runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("backfill set the marker with an event the store cannot hold")
	}
	if missing, _, _ := ledgerParity(ctx, nk, store, "crew"); !charSameIDs(missing, "") {
		t.Fatalf("missing = %v, want the event without id", missing)
	}

	// Once the mismatch is gone, the next run sets the marker.
	nk.put(CrewEventsCollection, "crew", SystemUserID, CrewEventLedger{CrewID: "crew", UpdatedAt: now.UnixMilli(), Events: []CrewEvent{
		migEvent("ok", "game_session", now),
	}})
	if !runLedgerBackfill(ctx, nk, testLogger(), store) {
		t.Fatal("backfill did not set the marker after the mismatch was gone")
	}
}
