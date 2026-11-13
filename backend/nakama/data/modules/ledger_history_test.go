package main

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Phase 1b of the durable ledger: the reconcile of the clips, stream sessions
// and recaps documents, its marker and its kill switch.

func histRows(t *testing.T, store ledgerStore, crewID, typ string) []CrewEvent {
	t.Helper()
	rows, err := store.Between(context.Background(), crewID, math.MinInt64, math.MaxInt64, []string{typ})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func histIDs(events []CrewEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return ids
}

// histSession is a stream session of the durable copy.
func histSession(id string, ts time.Time, snapshots ...string) StoredStreamSession {
	if snapshots == nil {
		snapshots = []string{}
	}
	return StoredStreamSession{EventID: id, SessionID: "sess_" + id, StreamerID: "u1", StreamerName: "Ann", Title: "T " + id,
		DurationMin: 20, PeakViewers: 2, SnapshotURLs: snapshots, Ts: ts.UnixMilli(), Score: 30}
}

// histStreamEvent is the ledger event of a stream session, with its viewers.
func histStreamEvent(s StoredStreamSession) CrewEvent {
	return CrewEvent{ID: s.EventID, Type: "stream_session", ActorID: s.StreamerID, Timestamp: s.Ts, Score: s.Score,
		Data: StreamSessionData{SessionID: s.SessionID, StreamerID: s.StreamerID, StreamerName: s.StreamerName, Title: s.Title,
			DurationMin: s.DurationMin, PeakViewers: s.PeakViewers, ViewerIDs: []string{"v1", "v2"}, SnapshotURLs: s.SnapshotURLs}}
}

func putStreamsDoc(nk *fakeGuestNk, crewID string, sessions ...StoredStreamSession) {
	nk.put(CrewStreamSessionsCollection, crewID, SystemUserID, CrewStreamSessionsDoc{CrewID: crewID, Sessions: sessions})
}

// The marker of phase 1b needs the marker of phase 1a, a clean pass and its
// own soak time. Each kill switch puts the readers back on the documents.
func TestHistoryMarkerGate(t *testing.T) {
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "")
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()
	putStreamsDoc(nk, "crew", histSession("old", now.Add(-30*charDay)))

	// The first pass copies the document and records the start. No marker.
	r := reconcilePass(t, nk)
	if !r.lastHist.clean() || r.lastHist.Inserted != 1 || r.lastHist.Docs != 1 || historyMarkerSet(nk) {
		t.Fatalf("first pass = %+v marker=%s", r.lastHist, r.lastHistMarker)
	}
	hist, histVersion, _ := readHistoryMigrationState(ctx, nk)
	if hist == nil || hist.StartedMs < now.UnixMilli() || hist.DoneMs != 0 {
		t.Fatalf("history state after the first pass = %+v", hist)
	}

	// The soak of phase 1b is over, phase 1a has no marker: not yet.
	hist.StartedMs = now.Add(-25 * time.Hour).UnixMilli()
	if err := writeHistoryMigrationState(ctx, nk, *hist, histVersion); err != nil {
		t.Fatal(err)
	}
	if r = reconcilePass(t, nk); historyMarkerSet(nk) || r.lastHistMarker != "not_set_events_marker_not_set" {
		t.Fatalf("history marker without the events marker: marker=%s", r.lastHistMarker)
	}

	// Phase 1a has its marker, the soak of phase 1b is not over: not yet.
	events, eventsVersion, _ := readLedgerMigrationState(ctx, nk)
	events.EventsDoneMs = now.UnixMilli()
	if err := writeLedgerMigrationState(ctx, nk, *events, eventsVersion); err != nil {
		t.Fatal(err)
	}
	hist.StartedMs = now.Add(-23 * time.Hour).UnixMilli()
	if err := writeHistoryMigrationState(ctx, nk, *hist, ""); err != nil {
		t.Fatal(err)
	}
	if r = reconcilePass(t, nk); historyMarkerSet(nk) || !strings.HasPrefix(r.lastHistMarker, "not_set_soak_") {
		t.Fatalf("history marker after 23 hours: marker=%s", r.lastHistMarker)
	}
	if ledgerTruthStore(ctx, nk) == nil || historyTruthStore(ctx, nk) != nil {
		t.Fatal("after the events marker alone the store is the truth for the events only")
	}

	// Both are met, but a document of phase 1b cannot be read: not yet.
	hist.StartedMs = now.Add(-25 * time.Hour).UnixMilli()
	if err := writeHistoryMigrationState(ctx, nk, *hist, ""); err != nil {
		t.Fatal(err)
	}
	broken := failHistReadNk{fakeGuestNk: nk, collection: CrewStreamSessionsCollection}
	if r = reconcilePass(t, broken); historyMarkerSet(nk) || r.lastHist.Failed != 1 || r.lastHistMarker != "not_set_pass_not_clean" {
		t.Fatalf("history marker after a failed read: %+v marker=%s", r.lastHist, r.lastHistMarker)
	}

	// A clean pass: the marker.
	if r = reconcilePass(t, nk); !historyMarkerSet(nk) || r.lastHistMarker != "set" {
		t.Fatalf("history marker not set: %+v marker=%s", r.lastHist, r.lastHistMarker)
	}
	if historyTruthStore(ctx, nk) != store {
		t.Fatal("after both markers the store is not the truth")
	}
	if r = reconcilePass(t, nk); r.lastHistMarker != "set_before" {
		t.Fatalf("pass after the marker: marker=%s", r.lastHistMarker)
	}

	// The kill switch of phase 1b puts only its readers back.
	t.Setenv("MELLO_LEDGER_HISTORY_READ", "doc")
	if historyTruthStore(ctx, nk) != nil || ledgerTruthStore(ctx, nk) == nil {
		t.Fatal("MELLO_LEDGER_HISTORY_READ=doc: want the documents for phase 1b only")
	}
	t.Setenv("MELLO_LEDGER_HISTORY_READ", "")
	// The kill switch of phase 1a puts both back.
	t.Setenv("MELLO_LEDGER_READ", "doc")
	if historyTruthStore(ctx, nk) != nil || ledgerTruthStore(ctx, nk) != nil {
		t.Fatal("MELLO_LEDGER_READ=doc: want the documents for both phases")
	}
	t.Setenv("MELLO_LEDGER_READ", "")
	if historyTruthStore(ctx, nk) == nil {
		t.Fatal("without a kill switch the store is the truth again")
	}
}

// failHistReadNk fails the read of every document of one collection.
type failHistReadNk struct {
	*fakeGuestNk
	collection string
}

func (f failHistReadNk) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	for _, r := range reads {
		if r.Collection == f.collection {
			return nil, errors.New("storage read failed")
		}
	}
	return f.fakeGuestNk.StorageRead(ctx, reads)
}

// A stream has one row: the row of its ledger event. The durable copy adds
// only the sessions that the ledger document does not hold.
func TestStreamSessionHasOneRow(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	now := time.Now()

	recent := histSession("recent", now.Add(-2*charDay), "https://m.test/1.jpg")
	old := histSession("old", now.Add(-40*charDay), "https://m.test/2.jpg")
	putDoc(nk, "crew", now, histStreamEvent(recent), migEvent("game", "game_session", now.Add(-1*charDay)))
	putStreamsDoc(nk, "crew", old, recent)

	r := reconcilePass(t, nk)
	if !r.last.clean() || !r.lastHist.clean() || r.last.Inserted != 2 || r.lastHist.Inserted != 1 {
		t.Fatalf("pass: events %+v history %+v", r.last, r.lastHist)
	}
	rows := histRows(t, store, "crew", "stream_session")
	if got := histIDs(rows); !charSameIDs(got, "old", "recent") {
		t.Fatalf("stream rows = %v, want one for each session", got)
	}
	// The row of the recent session is the row of the event: it has the
	// viewers. The old one comes from the copy.
	if d, err := decodeStreamSessionData(rows[1].Data); err != nil || len(d.ViewerIDs) != 2 {
		t.Fatalf("row of the event = %+v err=%v", d, err)
	}
	if d, err := decodeStreamSessionData(rows[0].Data); err != nil || d.SessionID != "sess_old" || len(d.ViewerIDs) != 0 || len(d.SnapshotURLs) != 1 {
		t.Fatalf("row of the copy = %+v err=%v", d, err)
	}
	if refs, _ := store.ByRef(ctx, "crew", "stream_session", "sess_old"); len(refs) != 1 || refs[0].ActorID != "u1" || refs[0].Score != 30 {
		t.Fatalf("by ref of the old session = %+v", refs)
	}
	// The 7-day view is what the ledger document holds.
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "recent", "game") {
		t.Fatalf("7-day view = %v", got)
	}
	// The counted queries see each session once.
	if n, _ := store.CountOlder(ctx, "crew", []string{"stream_session"}, now.UnixMilli()); n != 2 {
		t.Fatalf("counted stream sessions = %d, want 2", n)
	}
	// A second pass copies nothing.
	if r = reconcilePass(t, nk); r.lastHist.Inserted != 0 || r.last.Inserted != 0 || r.last.Pruned != 0 || !historyMarkerSet(nk) {
		t.Fatalf("second pass: events %+v history %+v marker=%s", r.last, r.lastHist, r.lastHistMarker)
	}
	if got := crewStreamSessions(ctx, nk, "crew"); len(got) != 2 || !historySameJSON(got, []StoredStreamSession{old, recent}) {
		t.Fatalf("sessions after the cutover = %+v", got)
	}
}

// A session that only the durable copy holds, inside the 7 days of the
// ledger, is a row outside the 7-day view. The reconcile of phase 1a does not
// delete it, and the feed lists it as a replay as before.
func TestStreamSessionOfTheCopyAloneIsNotInTheSevenDayView(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()

	only := histSession("only_copy", now.Add(-1*charDay), "https://m.test/1.jpg")
	putDoc(nk, "crew", now, migEvent("game", "game_session", now.Add(-2*charDay)))
	putStreamsDoc(nk, "crew", only)

	for pass := 1; pass <= 3; pass++ {
		r := reconcilePass(t, nk)
		if !r.last.clean() || !r.lastHist.clean() || r.last.Pruned != 0 {
			t.Fatalf("pass %d: events %+v history %+v", pass, r.last, r.lastHist)
		}
		if got := histIDs(histRows(t, store, "crew", "stream_session")); !charSameIDs(got, "only_copy") {
			t.Fatalf("pass %d: stream rows = %v", pass, got)
		}
		if got := viewIDs(t, store, "crew"); !charSameIDs(got, "game") {
			t.Fatalf("pass %d: 7-day view = %v, want the ledger document", pass, got)
		}
	}
	if historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	if got := crewStreamSessions(ctx, nk, "crew"); len(got) != 1 || got[0].EventID != "only_copy" {
		t.Fatalf("sessions after the cutover = %+v", got)
	}
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "game") {
		t.Fatalf("ledger after the cutover = %v", got)
	}

	// A ledger document that then holds the event (an instance of an older
	// release wrote it) gives the row its place in the view. Still one row.
	putDoc(nk, "crew", now, migEvent("game", "game_session", now.Add(-2*charDay)), histStreamEvent(only))
	reconcilePass(t, nk)
	if got := viewIDs(t, store, "crew"); !charSameIDs(got, "game", "only_copy") {
		t.Fatalf("7-day view after the document took the event = %v", got)
	}
	if got := histIDs(histRows(t, store, "crew", "stream_session")); !charSameIDs(got, "only_copy") {
		t.Fatalf("stream rows = %v", got)
	}
}

// The copy of a resumed stream stays in its document. The event was removed,
// so the store does not get the row back, and the pass is clean.
func TestStreamSessionOfAResumedStreamStaysOut(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	now := time.Now()

	s := histSession("dropped", now.Add(-time.Hour))
	if err := AppendCrewEvent(ctx, nk, "crew", histStreamEvent(s)); err != nil {
		t.Fatal(err)
	}
	if err := UpsertStreamSession(ctx, nk, "crew", s); err != nil {
		t.Fatal(err)
	}
	// The host comes back: start_stream removes the event.
	if err := removeCrewEvent(ctx, nk, "crew", "dropped"); err != nil {
		t.Fatal(err)
	}
	if doc, _ := readStreamSessionsDoc(ctx, nk, "crew"); len(doc.Sessions) != 1 {
		t.Fatalf("the copy = %+v, want the session still there", doc.Sessions)
	}

	r := reconcilePass(t, nk)
	if !r.lastHist.clean() || r.lastHist.Inserted != 0 || !historyMarkerSet(nk) {
		t.Fatalf("pass: history %+v marker=%s", r.lastHist, r.lastHistMarker)
	}
	if rows := histRows(t, store, "crew", "stream_session"); len(rows) != 0 {
		t.Fatalf("stream rows = %v, want none", histIDs(rows))
	}

	// The stream ends for good: one row, under the id of the new event.
	final := histSession("final", now)
	final.SessionID = s.SessionID
	if err := AppendCrewEvent(ctx, nk, "crew", histStreamEvent(final)); err != nil {
		t.Fatal(err)
	}
	if err := UpsertStreamSession(ctx, nk, "crew", final); err != nil {
		t.Fatal(err)
	}
	reconcilePass(t, nk)
	if got := histIDs(histRows(t, store, "crew", "stream_session")); !charSameIDs(got, "final") {
		t.Fatalf("stream rows after the stream ended = %v", got)
	}
}

// Two events for one session (stop_stream and the socket drop both ended it)
// are two rows of the ledger. The feed lists the session once, as the copy
// did: the newer one.
func TestStreamSessionsFromRowsListASessionOnce(t *testing.T) {
	now := time.Now()
	first := histStreamEvent(histSession("first", now.Add(-2*time.Second)))
	second := histStreamEvent(histSession("second", now, "https://m.test/1.jpg"))
	second.Data = StreamSessionData{SessionID: "sess_first", StreamerID: "u1", SnapshotURLs: []string{"https://m.test/1.jpg"}}
	other := histStreamEvent(histSession("other", now.Add(-time.Hour)))
	noSession := CrewEvent{ID: "legacy", Type: "stream_session", Timestamp: now.Add(-3 * time.Hour).UnixMilli(), Data: StreamSessionData{StreamerID: "u1"}}
	noSession2 := CrewEvent{ID: "legacy2", Type: "stream_session", Timestamp: now.Add(-4 * time.Hour).UnixMilli(), Data: StreamSessionData{StreamerID: "u1"}}

	got := streamSessionsFromRows([]CrewEvent{noSession2, noSession, other, first, second})
	ids := make([]string, 0, len(got))
	for _, s := range got {
		ids = append(ids, s.EventID)
	}
	// A session with no session id is not folded into another one.
	if !charSameIDs(ids, "legacy2", "legacy", "other", "second") {
		t.Fatalf("sessions = %v", ids)
	}
	if len(got[3].SnapshotURLs) != 1 || got[3].SessionID != "sess_first" {
		t.Fatalf("the session = %+v", got[3])
	}
}

// Before the cutover a row takes the content of the copy, and keeps what the
// copy does not hold. After the cutover the store is the truth.
func TestStreamSessionReconcileTakesTheCopyOnlyBeforeTheCutover(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	now := time.Now()
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "") // 24 hours: no marker yet

	// The row of an event of 20 days ago: the ledger document trimmed it.
	s := histSession("s", now.Add(-20*charDay))
	ev := histStreamEvent(s)
	ev.CrewID = "crew"
	if _, err := store.Append(ctx, ev, s.SessionID); err != nil {
		t.Fatal(err)
	}
	// The copy got two snapshots that the row did not get.
	s.SnapshotURLs = []string{"https://m.test/1.jpg", "https://m.test/2.jpg"}
	putStreamsDoc(nk, "crew", s)

	r := reconcilePass(t, nk)
	if r.lastHist.Updated != 1 || r.lastHist.Inserted != 0 || !r.lastHist.clean() {
		t.Fatalf("pass before the cutover = %+v", r.lastHist)
	}
	d, err := decodeStreamSessionData(histRows(t, store, "crew", "stream_session")[0].Data)
	if err != nil || len(d.SnapshotURLs) != 2 || len(d.ViewerIDs) != 2 || d.Title != "T s" {
		t.Fatalf("row after the repair = %+v err=%v", d, err)
	}
	if r = reconcilePass(t, nk); r.lastHist.Updated != 0 {
		t.Fatalf("second pass = %+v", r.lastHist)
	}

	// After the cutover the copy changes nothing.
	noSoak(t)
	if reconcilePass(t, nk); !historyMarkerSet(nk) {
		t.Fatal("no history marker")
	}
	s.SnapshotURLs = []string{}
	s.Title = "changed in the copy"
	putStreamsDoc(nk, "crew", s)
	if r = reconcilePass(t, nk); r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("pass after the cutover = %+v", r.lastHist)
	}
	if d, _ := decodeStreamSessionData(histRows(t, store, "crew", "stream_session")[0].Data); len(d.SnapshotURLs) != 2 || d.Title != "T s" {
		t.Fatalf("row after the cutover = %+v", d)
	}
}

// The copy is capped at 150 sessions and the store is not. The reconcile
// never deletes a row that the copy dropped.
func TestStreamSessionReconcileKeepsWhatTheCopyDropped(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()

	const total = CrewStreamSessionsMaxRetained + 5
	for i := 0; i < total; i++ {
		s := histSession(charID(i), now.Add(-time.Duration(total-i)*time.Hour))
		if err := AppendCrewEvent(ctx, nk, "crew", histStreamEvent(s)); err != nil {
			t.Fatal(err)
		}
		if err := UpsertStreamSession(ctx, nk, "crew", s); err != nil {
			t.Fatal(err)
		}
	}
	if doc, _ := readStreamSessionsDoc(ctx, nk, "crew"); len(doc.Sessions) != CrewStreamSessionsMaxRetained {
		t.Fatalf("the copy holds %d sessions", len(doc.Sessions))
	}
	if n := len(histRows(t, store, "crew", "stream_session")); n != total {
		t.Fatalf("the store holds %d sessions before the reconcile, want %d", n, total)
	}

	for pass := 1; pass <= 2; pass++ {
		r := reconcilePass(t, nk)
		if !r.lastHist.clean() || !r.last.clean() || r.last.Pruned != 0 {
			t.Fatalf("pass %d: events %+v history %+v", pass, r.last, r.lastHist)
		}
		if n := len(histRows(t, store, "crew", "stream_session")); n != total {
			t.Fatalf("pass %d: the store holds %d sessions, want %d", pass, n, total)
		}
	}
	// After the cutover the feed has every session.
	if historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	if got := crewStreamSessions(ctx, nk, "crew"); len(got) != total || got[0].EventID != charID(0) {
		t.Fatalf("sessions after the cutover: %d, first %+v", len(got), got[0])
	}
}

func charID(i int) string {
	return "e" + string(rune('a'+i/26/26%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i%26))
}

// failingBetween fails the range read of the store.
type failingBetween struct{ *memLedgerStore }

func (failingBetween) Between(context.Context, string, int64, int64, []string) ([]CrewEvent, error) {
	return nil, context.DeadlineExceeded
}

func TestStreamSessionsFallBackToTheDocumentWhenTheStoreFails(t *testing.T) {
	useHistoryStoreReads(t)
	useLedger(t, failingBetween{newMemLedgerStore()})
	nk := newFakeGuestNk()
	putStreamsDoc(nk, "crew", histSession("in_doc", time.Now()))
	if got := crewStreamSessions(context.Background(), nk, "crew"); len(got) != 1 || got[0].EventID != "in_doc" {
		t.Fatalf("sessions = %+v, want the one of the document", got)
	}
}

// The ledger document decides about a session while it holds the event. The
// copy with other content does not change the row, so the two reconciles do
// not undo each other.
func TestLedgerDocumentDecidesAboutASessionItHolds(t *testing.T) {
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "") // both documents win
	now := time.Now()

	s := histSession("s", now.Add(-time.Hour), "https://m.test/1.jpg")
	putDoc(nk, "crew", now, histStreamEvent(s))
	// The copy got a second snapshot that the ledger document did not get.
	s.SnapshotURLs = []string{"https://m.test/1.jpg", "https://m.test/2.jpg"}
	putStreamsDoc(nk, "crew", s)

	for pass := 1; pass <= 3; pass++ {
		r := reconcilePass(t, nk)
		wantInserted := 0
		if pass == 1 {
			wantInserted = 1
		}
		if !r.last.clean() || !r.lastHist.clean() || r.last.Inserted != wantInserted || r.last.Updated != 0 || r.lastHist.Updated != 0 || r.lastHist.Inserted != 0 {
			t.Fatalf("pass %d: events %+v history %+v", pass, r.last, r.lastHist)
		}
		rows := histRows(t, store, "crew", "stream_session")
		if d, err := decodeStreamSessionData(rows[0].Data); len(rows) != 1 || err != nil || len(d.SnapshotURLs) != 1 || len(d.ViewerIDs) != 2 {
			t.Fatalf("pass %d: row = %+v err=%v", pass, d, err)
		}
	}
}
