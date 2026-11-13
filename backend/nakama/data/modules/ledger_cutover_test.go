package main

import (
	"context"
	"testing"
	"time"
)

// resetLedgerCutover clears what the node knows about the marker, now and at
// the end of the test.
func resetLedgerCutover(t *testing.T) {
	t.Helper()
	reset := func() {
		ledgerCutover.mu.Lock()
		ledgerCutover.done = false
		ledgerCutover.checkedAt = time.Time{}
		ledgerCutover.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// useStoreReads sets an empty in-memory store and puts the node after the
// cutover, so every reader uses the store.
func useStoreReads(t *testing.T) *memLedgerStore {
	t.Helper()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	ledgerCutover.mu.Lock()
	ledgerCutover.done = true
	ledgerCutover.mu.Unlock()
	return store
}

func ledgerIDsOf(nk *fakeGuestNk, crewID string) []string {
	ledger, _ := readLedger(context.Background(), nk, crewID)
	ids := make([]string, 0, len(ledger.Events))
	for _, e := range ledger.Events {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestReadersUseDocUntilMarker(t *testing.T) {
	ctx := context.Background()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	prevRecheck := ledgerMarkerRecheck
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	t.Setenv("MELLO_LEDGER_READ", "")

	nk := newFakeGuestNk()
	now := time.Now()
	// The document and the store differ, so the test sees which one is read.
	nk.put(CrewEventsCollection, "crew", SystemUserID, CrewEventLedger{CrewID: "crew", Events: []CrewEvent{charEvent("in_doc", now)}})
	inStore := charEvent("in_store", now)
	if _, err := store.Append(ctx, inStore, ""); err != nil {
		t.Fatal(err)
	}

	// No marker: the document.
	ledgerMarkerRecheck = time.Hour
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("before the marker: %v, want [in_doc]", got)
	}

	// The marker exists, but the node looked a moment ago: still the document.
	if err := writeLedgerMigrationState(ctx, nk, ledgerMigrationState{EventsDoneMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("before the recheck: %v, want [in_doc]", got)
	}

	// The next look finds the marker: the store.
	ledgerMarkerRecheck = 0
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_store") {
		t.Fatalf("after the marker: %v, want [in_store]", got)
	}
	if ledger, version := readLedger(ctx, nk, "crew"); ledger.CrewID != "crew" || version != "" {
		t.Fatalf("store read: crew=%q version=%q", ledger.CrewID, version)
	}
	// The writers of the document still read the document.
	if got := docIDs(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("document read after the marker: %v", got)
	}

	// The rollback switch forces the document.
	t.Setenv("MELLO_LEDGER_READ", "doc")
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("with MELLO_LEDGER_READ=doc: %v, want [in_doc]", got)
	}
	t.Setenv("MELLO_LEDGER_READ", "")

	// No store: the document.
	crewLedger = nil
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("with no store: %v, want [in_doc]", got)
	}
}

// failingReads fails the reads of the store.
type failingReads struct{ *memLedgerStore }

func (failingReads) RecentView(context.Context, string) ([]CrewEvent, error) {
	return nil, context.DeadlineExceeded
}

func TestReadersFallBackToTheDocumentWhenTheStoreFails(t *testing.T) {
	useStoreReads(t)
	crewLedger = failingReads{newMemLedgerStore()}
	nk := newFakeGuestNk()
	charAppend(t, nk, charEvent("a", time.Now()))
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "a") {
		t.Fatalf("ledger ids = %v, want [a] from the document", got)
	}
}

// The characterisation tests hold on the durable store too. The test
// functions are the ones that ran against the legacy document, unchanged.
func TestCharacterisationOnTheStore(t *testing.T) {
	for name, fn := range map[string]func(*testing.T){
		"TestAppendCrewEventTrimsAtSevenDays":    TestAppendCrewEventTrimsAtSevenDays,
		"TestLedgerTrimAnchorsToLastAppend":      TestLedgerTrimAnchorsToLastAppend,
		"TestMergedTimelineCutsClipsAtSevenDays": TestMergedTimelineCutsClipsAtSevenDays,
		"TestCatchupIgnoresEventsOutsideLedger":  TestCatchupIgnoresEventsOutsideLedger,
	} {
		t.Run(name, func(t *testing.T) {
			store := useStoreReads(t)
			fn(t)
			// The test read the store, not the document: the store holds
			// the events, also the ones the view leaves out.
			if n := len(storeIDs(t, store, "crew")); n < 2 {
				t.Fatalf("the store holds %d events", n)
			}
		})
	}
}

func TestWeeklyRecapReadsItsFullWeekFromTheStore(t *testing.T) {
	ctx := context.Background()
	weekEnd := time.Now().Add(-2 * charDay)
	early := weekEnd.Add(-6*charDay - 12*time.Hour)

	run := func(t *testing.T) *WeeklyRecapData {
		nk := recapFixture()
		// A session early in the week, then an append 2 days after the week
		// ended. That append trims the early session from the 7-day view.
		setLedgerClock(t, early)
		ev := charEvent("early_session", early)
		ev.Data = VoiceSessionData{ChannelName: "General", ParticipantIDs: []string{"u1", "u2"}, ParticipantNames: []string{"Ann", "Bo"}, DurationMin: 45}
		charAppend(t, nk, ev)
		setLedgerClock(t, time.Now())
		charAppend(t, nk, charEvent("after_the_week", time.Now()))
		if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "after_the_week") {
			t.Fatalf("7-day view = %v, want [after_the_week]", got)
		}

		generateWeeklyRecapAt(ctx, nk, testLogger(), "crew", weekEnd)
		doc, _ := readRecapsDoc(ctx, nk, "crew")
		if len(doc.Recaps) == 0 {
			return nil
		}
		return &doc.Recaps[0]
	}

	t.Run("store", func(t *testing.T) {
		useStoreReads(t)
		recap := run(t)
		if recap == nil || recap.TotalHangoutMin != 45 || recap.SessionCount != 1 {
			t.Fatalf("recap = %+v, want the early session of 45 minutes", recap)
		}
	})
	t.Run("document", func(t *testing.T) {
		// Before the cutover the late run does not see the early session.
		if recap := run(t); recap != nil && recap.SessionCount != 0 {
			t.Fatalf("recap = %+v, want no session", recap)
		}
	})
}

// The public surfaces read the ledger through buildRecapHighlightWithData.
// On the store they get the 7-day view and nothing else.
func publicLedgerFixture(t *testing.T) (*fakeGuestNk, *CrewEventLedger) {
	t.Helper()
	store := useStoreReads(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	now := time.Now()

	stream := func(id string, ts time.Time, title string) CrewEvent {
		return CrewEvent{ID: id, CrewID: "crew", Type: "stream_session", ActorID: "u1", Timestamp: ts.UnixMilli(),
			Data: StreamSessionData{SessionID: "sess_" + id, StreamerID: "u1", StreamerName: "Ann", Title: title,
				SnapshotURLs: []string{"https://cdn.example/" + id + ".jpg"}}}
	}
	setLedgerClock(t, now.Add(-30*charDay))
	if err := AppendCrewEvent(ctx, nk, "crew", stream("old_stream", now.Add(-30*charDay), "Old")); err != nil {
		t.Fatal(err)
	}
	setLedgerClock(t, now)
	if err := AppendCrewEvent(ctx, nk, "crew", stream("new_stream", now.Add(-time.Hour), "New")); err != nil {
		t.Fatal(err)
	}
	// Rows of the types that phase 1b adds to the store.
	for _, ev := range []CrewEvent{
		{ID: "clip_row", CrewID: "crew", Type: "clip", ActorID: "u1", Timestamp: now.UnixMilli(),
			Data: ClipData{ClipID: "c1", ClipType: "voice", MediaURL: "https://cdn.example/c1.mp4"}},
		{ID: "recap_row", CrewID: "crew", Type: "weekly_recap", Timestamp: now.UnixMilli(),
			Data: WeeklyRecapData{CrewID: "crew", TotalHangoutMin: 500, ClipCount: 9}},
	} {
		if _, err := store.Append(ctx, ev, ""); err != nil {
			t.Fatal(err)
		}
	}

	highlight, recap, ledger := buildRecapHighlightWithData(ctx, nk, testLogger(), "crew")
	if highlight != "" || recap != nil {
		t.Fatalf("highlight = %q recap = %+v, want none from a recap row of the store", highlight, recap)
	}
	return nk, ledger
}

func TestInvitePreviewSeesNoClipRows(t *testing.T) {
	_, ledger := publicLedgerFixture(t)
	if len(ledger.Events) != 1 || ledger.Events[0].ID != "new_stream" {
		t.Fatalf("ledger of the invite preview = %+v, want only new_stream", ledger.Events)
	}
	for _, e := range ledger.Events {
		if e.Type == "clip" || e.Type == "weekly_recap" {
			t.Fatalf("the invite preview sees a %s row", e.Type)
		}
	}
}

func TestGuestFeedSeesOnlyTheSevenDayViewOfTheStore(t *testing.T) {
	_, ledger := publicLedgerFixture(t)
	sessions := projectGuestSessions(ledger, guestFeedSessionCap)
	if len(sessions) != 1 || sessions[0].Title != "New" {
		t.Fatalf("guest sessions = %+v, want only the new stream", sessions)
	}
	clips, count := collectGuestClips(&CrewClipsDoc{}, ledger, guestFeedClipLimit)
	if len(clips) != 0 || count != 0 {
		t.Fatalf("guest clips = %+v count=%d, want none", clips, count)
	}
}

func TestSnapshotBackfillGroupsStoreEventsByCrew(t *testing.T) {
	ev := func(crewID, id string) CrewEvent { return CrewEvent{CrewID: crewID, ID: id} }
	groups := ledgerGroupByCrew([]CrewEvent{ev("a", "1"), ev("a", "2"), ev("b", "3"), ev("c", "4"), ev("c", "5")})
	if len(groups) != 3 || len(groups[0]) != 2 || len(groups[1]) != 1 || len(groups[2]) != 2 ||
		groups[1][0].ID != "3" || groups[2][1].ID != "5" {
		t.Fatalf("groups = %+v", groups)
	}
	if got := ledgerGroupByCrew(nil); len(got) != 0 {
		t.Fatalf("groups of no events = %+v", got)
	}
}

func TestSnapshotBackfillReadsTheStoreAfterTheCutover(t *testing.T) {
	store := useStoreReads(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if _, err := store.Append(ctx, CrewEvent{ID: "s1", CrewID: "crew", Type: "stream_session", Timestamp: now,
		Data: StreamSessionData{SessionID: "sess_1", SnapshotURLs: []string{}}}, "sess_1"); err != nil {
		t.Fatal(err)
	}
	// A nil module: the store path lists no storage. Without snapshot storage
	// the session is skipped, so nothing else is read.
	snapshotBackfill(ctx, nil, testLogger())
}
