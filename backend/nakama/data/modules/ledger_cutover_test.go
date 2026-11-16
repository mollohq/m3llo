package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// resetLedgerCutover clears what the node knows about the marker, now and at
// the end of the test. The node is then before the cutover.
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
	t.Setenv("MELLO_LEDGER_READ", "")
}

// useStoreReads sets an empty in-memory store and puts the node after the
// cutover: the store is the truth.
func useStoreReads(t *testing.T) *memLedgerStore {
	t.Helper()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	ledgerCutover.mu.Lock()
	ledgerCutover.done = true
	ledgerCutover.mu.Unlock()
	return store
}

func ledgerIDsOf(nk runtime.NakamaModule, crewID string) []string {
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

	nk := newFakeGuestNk()
	now := time.Now()
	// The document and the store differ, so the test sees which one is read.
	nk.put(CrewEventsCollection, "crew", SystemUserID, CrewEventLedger{CrewID: "crew", Events: []CrewEvent{charEvent("in_doc", now)}})
	if _, err := store.Append(ctx, charEvent("in_store", now), ""); err != nil {
		t.Fatal(err)
	}

	// No state document: the document.
	ledgerMarkerRecheck = time.Hour
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("before the marker: %v, want [in_doc]", got)
	}

	// Dual write runs, the marker is not set: the document.
	ledgerMarkerRecheck = 0
	if err := writeLedgerMigrationState(ctx, nk, ledgerMigrationState{DualWriteStartedMs: now.UnixMilli()}, ""); err != nil {
		t.Fatal(err)
	}
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("during the soak: %v, want [in_doc]", got)
	}

	// The marker exists, but the node looked a moment ago: still the document.
	ledgerMarkerRecheck = time.Hour
	if err := writeLedgerMigrationState(ctx, nk, ledgerMigrationState{DualWriteStartedMs: now.UnixMilli(), EventsDoneMs: now.UnixMilli()}, ""); err != nil {
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

	// The kill switch forces the document, for readers and for the write order.
	t.Setenv("MELLO_LEDGER_READ", "doc")
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("with MELLO_LEDGER_READ=doc: %v, want [in_doc]", got)
	}
	if ledgerTruthStore(ctx, nk) != nil {
		t.Fatal("with MELLO_LEDGER_READ=doc the store is still the truth")
	}
	t.Setenv("MELLO_LEDGER_READ", "")

	// No store: the document.
	setLedger(nil)
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "in_doc") {
		t.Fatalf("with no store: %v, want [in_doc]", got)
	}
}

// failingReads fails the 7-day view of the store.
type failingReads struct{ *memLedgerStore }

func (failingReads) RecentView(context.Context, string) ([]CrewEvent, error) {
	return nil, context.DeadlineExceeded
}

func TestReadersFallBackToTheDocumentWhenTheStoreFails(t *testing.T) {
	useStoreReads(t)
	useLedger(t, failingReads{newMemLedgerStore()})
	nk := newFakeGuestNk()
	charAppend(t, nk, charEvent("a", time.Now()))
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "a") {
		t.Fatalf("ledger ids = %v, want [a] from the document", got)
	}
}

// countingLedger counts the reads of the 7-day view of the store.
type countingLedger struct {
	*memLedgerStore
	views int
}

func (c *countingLedger) RecentView(ctx context.Context, crewID string) ([]CrewEvent, error) {
	c.views++
	return c.memLedgerStore.RecentView(ctx, crewID)
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
			useStoreReads(t)
			store := &countingLedger{memLedgerStore: newMemLedgerStore()}
			useLedger(t, store)
			fn(t)
			// The readers of the test took the view of the store.
			if store.views == 0 {
				t.Fatal("no reader used the 7-day view of the store")
			}
			if n := len(storeIDs(t, store, "crew")); n < 2 {
				t.Fatalf("the store holds %d events", n)
			}
		})
	}
}

// A reader that gets the store sees an event that only the store holds, and
// a reader that gets the document does not.
func TestStoreAndDocumentReadsAreDistinct(t *testing.T) {
	ctx := context.Background()
	store := useStoreReads(t)
	nk := newFakeGuestNk()
	charAppend(t, nk, charEvent("both", time.Now()))
	if _, err := store.Append(ctx, charEvent("store_only", time.Now()), ""); err != nil {
		t.Fatal(err)
	}
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "both", "store_only") {
		t.Fatalf("after the cutover readers see %v, want the store", got)
	}
	timeline := buildMergedTimeline(ctx, nk, "crew")
	if len(timeline) != 2 {
		t.Fatalf("timeline holds %d entries, want the 2 of the store", len(timeline))
	}
	resetLedgerCutover(t)
	if got := ledgerIDsOf(nk, "crew"); !charSameIDs(got, "both") {
		t.Fatalf("before the cutover readers see %v, want the document", got)
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
		store := useStoreReads(t)
		// A clip row and a recap row of phase 1b in the week. The recap of
		// the week does not read them from the ledger.
		for _, typ := range []string{"clip", "weekly_recap"} {
			if _, err := store.Append(ctx, CrewEvent{ID: "row_" + typ, CrewID: "crew", Type: typ, ActorID: "u1", Timestamp: early.UnixMilli()}, ""); err != nil {
				t.Fatal(err)
			}
		}
		recap := run(t)
		if recap == nil || recap.TotalHangoutMin != 45 || recap.SessionCount != 1 {
			t.Fatalf("recap = %+v, want the early session of 45 minutes", recap)
		}
	})
	t.Run("document", func(t *testing.T) {
		resetLedgerCutover(t)
		// Before the cutover the late run does not see the early session.
		if recap := run(t); recap != nil && recap.SessionCount != 0 {
			t.Fatalf("recap = %+v, want no session", recap)
		}
	})
}

// publicLedger is what the public surfaces (guest feed, invite preview) get
// from the ledger of a crew.
type publicLedger struct {
	Highlight string
	Recap     *WeeklyRecapData
	Events    []CrewEvent
}

// publicLedgerFixture builds a crew as the dev seed and old crews have it:
// the ledger holds stream sessions and also clip and weekly_recap events.
// With rows it also puts a clip row and a recap row of phase 1b into the
// store, which the ledger never held.
func publicLedgerFixture(t *testing.T, store *memLedgerStore, now time.Time) (*fakeGuestNk, publicLedger) {
	t.Helper()
	ctx := context.Background()
	nk := newFakeGuestNk()

	setLedgerClock(t, now.Add(-30*charDay))
	old := streamEvent("old_stream", now.Add(-30*charDay))
	if err := AppendCrewEvent(ctx, nk, "crew", old); err != nil {
		t.Fatal(err)
	}
	setLedgerClock(t, now)
	stream := streamEvent("new_stream", now.Add(-time.Hour))
	stream.Data = StreamSessionData{SessionID: "sess_new", StreamerID: "u1", StreamerName: "Ann", Title: "New",
		SnapshotURLs: []string{"https://cdn.example/new.jpg"}}
	for _, ev := range []CrewEvent{
		stream,
		{ID: "ledger_clip_1", CrewID: "crew", Type: "clip", ActorID: "u1", Timestamp: now.Add(-30 * time.Minute).UnixMilli(), Score: 50,
			Data: ClipData{ClipID: "c1", ClipType: "voice", ClipperName: "Ann", DurationSeconds: 12, MediaURL: "https://cdn.example/c1.mp4"}},
		{ID: "ledger_recap_1", CrewID: "crew", Type: "weekly_recap", Timestamp: now.Add(-10 * time.Minute).UnixMilli(), Score: 30,
			Data: WeeklyRecapData{CrewID: "crew", TotalHangoutMin: 500, ClipCount: 9, TopGame: "Game"}},
	} {
		if err := AppendCrewEvent(ctx, nk, "crew", ev); err != nil {
			t.Fatal(err)
		}
	}
	if store != nil {
		for _, ev := range []CrewEvent{
			{ID: "clip_row", CrewID: "crew", Type: "clip", ActorID: "u1", Timestamp: now.UnixMilli(),
				Data: ClipData{ClipID: "c2", ClipType: "voice", MediaURL: "https://cdn.example/c2.mp4"}},
			{ID: "recap_row", CrewID: "crew", Type: "weekly_recap", Timestamp: now.UnixMilli(),
				Data: WeeklyRecapData{CrewID: "crew", TotalHangoutMin: 9999, ClipCount: 99}},
		} {
			if _, err := store.Append(ctx, ev, ""); err != nil {
				t.Fatal(err)
			}
		}
	}

	highlight, recap, ledger := buildRecapHighlightWithData(ctx, nk, testLogger(), "crew")
	return nk, publicLedger{Highlight: highlight, Recap: recap, Events: ledger.Events}
}

// The public surfaces see after the cutover exactly what they saw before it:
// the clip and weekly_recap events the ledger held stay, and the rows of
// phase 1b do not show.
func TestPublicSurfacesSeeTheSameLedgerAfterTheCutover(t *testing.T) {
	resetLedgerCutover(t)
	useMemLedger(t)
	now := time.Now()
	_, before := publicLedgerFixture(t, nil, now)
	if before.Highlight == "" || before.Recap == nil || len(before.Events) != 3 {
		t.Fatalf("before the cutover: %+v", before)
	}

	store := useStoreReads(t)
	_, after := publicLedgerFixture(t, store, now)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the public surfaces see another ledger after the cutover\nbefore: %+v\nafter:  %+v", before, after)
	}
	// The store is what was read: it holds the old stream and the two rows.
	if n := len(storeIDs(t, store, "crew")); n != 6 {
		t.Fatalf("the store holds %d events, want 6", n)
	}
}

func TestInvitePreviewSeesNoClipRows(t *testing.T) {
	store := useStoreReads(t)
	_, ledger := publicLedgerFixture(t, store, time.Now())
	for _, e := range ledger.Events {
		if e.ID == "clip_row" || e.ID == "recap_row" || e.ID == "old_stream" {
			t.Fatalf("the invite preview sees %s", e.ID)
		}
	}
	if ledger.Recap == nil || ledger.Recap.TotalHangoutMin != 500 {
		t.Fatalf("recap of the invite preview = %+v, want the one the ledger held", ledger.Recap)
	}
}

func TestGuestFeedSeesOnlyTheSevenDayViewOfTheStore(t *testing.T) {
	store := useStoreReads(t)
	_, ledger := publicLedgerFixture(t, store, time.Now())
	view := &CrewEventLedger{CrewID: "crew", Events: ledger.Events}
	sessions := projectGuestSessions(view, guestFeedSessionCap)
	if len(sessions) != 1 || sessions[0].Title != "New" {
		t.Fatalf("guest sessions = %+v, want only the new stream", sessions)
	}
	clips, count := collectGuestClips(&CrewClipsDoc{}, view, guestFeedClipLimit)
	if len(clips) != 1 || count != 1 {
		t.Fatalf("guest clips = %+v count=%d, want the one clip the ledger held", clips, count)
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
	nk := newFakeGuestNk()
	now := time.Now()

	// Two crews. crew_a has a stream session without snapshots, in the
	// store only: the list of ledger documents does not find it.
	a := streamEvent("s_a", now)
	a.CrewID = "crew_a"
	if _, err := store.Append(ctx, a, "sess_s_a"); err != nil {
		t.Fatal(err)
	}
	// crew_b has one that is older than the backfill window.
	b := streamEvent("s_b", now.Add(-48*time.Hour))
	b.CrewID = "crew_b"
	if _, err := store.Append(ctx, b, "sess_s_b"); err != nil {
		t.Fatal(err)
	}

	var asked []string
	prev := listSnapshotURLs
	listSnapshotURLs = func(crewID, sessionID string) ([]string, error) {
		asked = append(asked, crewID+"/"+sessionID)
		return []string{"https://cdn.example/1.jpg", "https://cdn.example/2.jpg"}, nil
	}
	t.Cleanup(func() { listSnapshotURLs = prev })

	snapshotBackfill(ctx, nk, testLogger())

	if !charSameIDs(asked, "crew_a/sess_s_a") {
		t.Fatalf("snapshot lists asked = %v, want only the recent session of crew_a", asked)
	}
	events, _ := store.Between(ctx, "crew_a", 0, now.UnixMilli(), nil)
	data, err := decodeStreamSessionData(events[0].Data)
	if err != nil || len(data.SnapshotURLs) != 2 {
		t.Fatalf("store event after the backfill = %+v err=%v, want 2 snapshot URLs", data, err)
	}
	sessions, _ := readStreamSessionsDoc(ctx, nk, "crew_a")
	if len(sessions.Sessions) != 1 || len(sessions.Sessions[0].SnapshotURLs) != 2 {
		t.Fatalf("durable stream sessions = %+v, want the session with 2 snapshot URLs", sessions.Sessions)
	}
}
