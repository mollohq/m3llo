package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Phase 1b of the durable ledger: the weekly recaps.

func histRecap(weekStart time.Time, hangoutMin int) WeeklyRecapData {
	return WeeklyRecapData{CrewID: "crew", WeekStart: weekStart.UnixMilli(), WeekEnd: weekStart.Add(7 * 24 * time.Hour).UnixMilli(),
		TotalHangoutMin: hangoutMin, TotalMin: hangoutMin, GeneratedAt: weekStart.Add(7*24*time.Hour + time.Minute).UnixMilli()}
}

func docRecaps(nk runtime.NakamaModule, crewID string) []WeeklyRecapData {
	doc, _ := readRecapsDoc(context.Background(), nk, crewID)
	return doc.Recaps
}

func storeRecaps(t *testing.T, store ledgerStore, crewID string) []WeeklyRecapData {
	t.Helper()
	return recapsFromRows(histRows(t, store, crewID, "weekly_recap"))
}

// failRecapDocNk fails every write of a recaps document.
type failRecapDocNk struct{ *fakeGuestNk }

func (f failRecapDocNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	for _, w := range writes {
		if w.Collection == CrewRecapsCollection {
			return nil, errors.New("document write failed")
		}
	}
	return f.fakeGuestNk.StorageWrite(ctx, writes)
}

func TestRecapWriteOrder(t *testing.T) {
	ctx := context.Background()
	monday := mondayStartUTC(time.Now()).Add(-7 * 24 * time.Hour)

	t.Run("before the cutover", func(t *testing.T) {
		noSoak(t)
		store := useMemLedger(t)
		resetLedgerCutover(t)
		resetHistoryCutover(t)
		// The document fails: an error, nothing in the store.
		if err := AppendRecap(ctx, failRecapDocNk{newFakeGuestNk()}, "crew", histRecap(monday, 10)); err == nil {
			t.Fatal("a failed document write reports success")
		}
		if n := len(storeRecaps(t, store, "crew")); n != 0 {
			t.Fatalf("the store holds %d recaps after a failed document write", n)
		}
		// The store fails: success, and the reconcile copies the recap.
		nk := newFakeGuestNk()
		useLedger(t, failingPuts{store})
		if err := AppendRecap(ctx, nk, "crew", histRecap(monday, 10)); err != nil {
			t.Fatalf("a failed store write fails the recap: %v", err)
		}
		useLedger(t, store)
		if r := reconcilePass(t, nk); r.lastHist.Inserted != 1 || !r.lastHist.clean() {
			t.Fatalf("pass = %+v", r.lastHist)
		}
		// With both stores up a recap is in both at once.
		if err := AppendRecap(ctx, nk, "crew", histRecap(monday.Add(-7*24*time.Hour), 20)); err != nil {
			t.Fatal(err)
		}
		rows := histRows(t, store, "crew", "weekly_recap")
		if len(rows) != 2 || len(docRecaps(nk, "crew")) != 2 {
			t.Fatalf("store %d recaps, document %d", len(rows), len(docRecaps(nk, "crew")))
		}
		// The row: the id of the feed, the week start as its time, no actor.
		newest := rows[1]
		if newest.ID != recapEntryID(monday.UnixMilli()) || newest.Timestamp != monday.UnixMilli() || newest.ActorID != "" || newest.Score != 30 {
			t.Fatalf("recap row = %+v", newest)
		}
		if got := viewIDs(t, store, "crew"); len(got) != 0 {
			t.Fatalf("a recap row is in the 7-day view: %v", got)
		}
	})

	t.Run("after the cutover", func(t *testing.T) {
		store := useHistoryStoreReads(t)
		nk := newFakeGuestNk()
		// The store fails: an error, nothing in the document.
		useLedger(t, failingPuts{store})
		if err := AppendRecap(ctx, nk, "crew", histRecap(monday, 10)); err == nil {
			t.Fatal("a failed store write reports success")
		}
		if n := len(docRecaps(nk, "crew")); n != 0 {
			t.Fatalf("the document holds %d recaps after a failed store write", n)
		}
		useLedger(t, store)
		// The document fails: success, and the readers have the recap.
		broken := failRecapDocNk{nk}
		if err := AppendRecap(ctx, broken, "crew", histRecap(monday, 10)); err != nil {
			t.Fatalf("a failed document write fails the recap: %v", err)
		}
		if got := crewLatestRecap(ctx, broken, "crew"); got == nil || got.WeekStart != monday.UnixMilli() || len(docRecaps(nk, "crew")) != 0 {
			t.Fatalf("latest recap = %+v, the document holds %d", got, len(docRecaps(nk, "crew")))
		}
		// The job reads the store: this crew has its recap of the week, so a
		// tick writes nothing, though the document is empty. The week has a
		// clip, so a tick that takes the crew for due would write a recap.
		if _, err := AppendClip(ctx, nk, "crew", histClip("a", "u1", monday.Add(24*time.Hour))); err != nil {
			t.Fatal(err)
		}
		job := &jobNk{fakeGuestNk: nk, crews: []string{"crew"}}
		generateDueRecaps(ctx, job, testLogger(), monday.Add(7*24*time.Hour+time.Hour))
		if n := len(storeRecaps(t, store, "crew")); n != 1 || len(docRecaps(nk, "crew")) != 0 {
			t.Fatalf("after a tick: store %d recaps, document %d", n, len(docRecaps(nk, "crew")))
		}
	})
}

// The reconcile copies the recaps document. Two recaps of one week start are
// one row: the first one, the one the board shows.
func TestRecapReconcile(t *testing.T) {
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "")
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	monday := mondayStartUTC(time.Now()).Add(-7 * 24 * time.Hour)

	older, empty := histRecap(monday.Add(-14*24*time.Hour), 30), histRecap(monday.Add(-7*24*time.Hour), 0)
	first, second := histRecap(monday, 10), histRecap(monday, 99)
	nk.put(CrewRecapsCollection, "crew", SystemUserID, CrewRecapsDoc{CrewID: "crew", Recaps: []WeeklyRecapData{older, empty, first, second}, LastRecapWeek: "x"})

	r := reconcilePass(t, nk)
	if r.lastHist.Inserted != 3 || !r.lastHist.clean() {
		t.Fatalf("pass = %+v", r.lastHist)
	}
	if got := storeRecaps(t, store, "crew"); !historySameJSON(got, []WeeklyRecapData{older, empty, first}) {
		t.Fatalf("store = %+v", got)
	}
	if r = reconcilePass(t, nk); r.lastHist.Inserted != 0 || r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("second pass = %+v", r.lastHist)
	}

	// Before the cutover a row takes the content of the document.
	older.TopGame = "CS2"
	nk.put(CrewRecapsCollection, "crew", SystemUserID, CrewRecapsDoc{CrewID: "crew", Recaps: []WeeklyRecapData{older, empty, first, second}})
	if r = reconcilePass(t, nk); r.lastHist.Updated != 1 || storeRecaps(t, store, "crew")[0].TopGame != "CS2" {
		t.Fatalf("pass after a change of the document = %+v", r.lastHist)
	}

	// After the cutover the readers have the recaps of the store, and the
	// document changes nothing.
	noSoak(t)
	if reconcilePass(t, nk); historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	older.TopGame = "changed in the document"
	nk.put(CrewRecapsCollection, "crew", SystemUserID, CrewRecapsDoc{CrewID: "crew", Recaps: []WeeklyRecapData{older}})
	if r = reconcilePass(t, nk); r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("pass after the cutover = %+v", r.lastHist)
	}
	if got := crewRecaps(ctx, nk, "crew"); len(got) != 3 || got[0].TopGame != "CS2" || got[2].TotalHangoutMin != 10 {
		t.Fatalf("recaps after the cutover = %+v", got)
	}
	if got := crewLatestRecap(ctx, nk, "crew"); got == nil || got.WeekStart != monday.UnixMilli() || got.TotalHangoutMin != 10 {
		t.Fatalf("latest recap = %+v", got)
	}
	if state := crewRecapState(ctx, nk, "crew"); state.LastRecapWeek != isoWeekKey(monday.Add(7*24*time.Hour)) || len(state.Recaps) != 1 {
		t.Fatalf("recap state = %+v", state)
	}
}

func TestRecapReadersFallBackToTheDocumentWhenTheStoreFails(t *testing.T) {
	ctx := context.Background()
	useHistoryStoreReads(t)
	useLedger(t, failingClipReads{newMemLedgerStore()})
	nk := newFakeGuestNk()
	monday := mondayStartUTC(time.Now())
	nk.put(CrewRecapsCollection, "crew", SystemUserID, CrewRecapsDoc{CrewID: "crew", LastRecapWeek: "2026-W01",
		Recaps: []WeeklyRecapData{histRecap(monday.Add(-7*24*time.Hour), 10), histRecap(monday.Add(-14*24*time.Hour), 20)}})
	if got := crewRecaps(ctx, nk, "crew"); len(got) != 2 {
		t.Fatalf("recaps = %+v", got)
	}
	if got := crewLatestRecap(ctx, nk, "crew"); got == nil || got.TotalHangoutMin != 10 {
		t.Fatalf("latest recap = %+v", got)
	}
	if state := crewRecapState(ctx, nk, "crew"); state.LastRecapWeek != "2026-W01" {
		t.Fatalf("recap state = %+v", state)
	}
}
