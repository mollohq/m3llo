package main

import (
	"context"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The characterisation tests of the clip, stream session and recap read
// paths (history_characterisation_test.go) run once for each mode of this
// list. A mode says where the crew history is read from. The tests and what
// they expect are the same in every mode.
type histMode struct {
	name string
	// setup runs before the test seeds anything.
	setup func(t *testing.T)
	// cutover runs after the test seeded its data and before it reads.
	cutover func(t *testing.T, nk runtime.NakamaModule)
}

// histModes are the modes.
//
//   - document: no durable store. Everything is read from the legacy documents.
//   - import: the crew is seeded on the legacy documents alone, as on a
//     release before the durable store. Then the store is set, the reconcile
//     copies the documents and sets both markers, and the test reads.
//   - store: both cutovers are done before the test seeds. The writes go to
//     the store first.
var histModes = []histMode{
	{name: "document"},
	{name: "import", cutover: importAndCutOver},
	{name: "store", setup: func(t *testing.T) {
		t.Helper()
		useHistoryStoreReads(t)
	}},
}

// importAndCutOver sets an empty store on an install that so far wrote the
// legacy documents alone, runs the reconcile, and checks that both markers
// are set and the store is the truth.
func importAndCutOver(t *testing.T, nk runtime.NakamaModule) {
	t.Helper()
	{
		useMemLedger(t)
		resetLedgerCutover(t)
		resetHistoryCutover(t)
		noSoak(t)
		prevRecheck := ledgerMarkerRecheck
		ledgerMarkerRecheck = 0
		t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
		r := reconcilePass(t, nk)
		if !markerSet(nk) || !historyMarkerSet(nk) {
			t.Fatalf("the reconcile did not set both markers: events %+v %s, history %+v %s", r.last, r.lastMarker, r.lastHist, r.lastHistMarker)
		}
		if historyTruthStore(context.Background(), nk) == nil {
			t.Fatal("the store is not the truth after the markers")
		}
	}
}

// resetHistoryCutover clears what the node knows about the marker of phase
// 1b, now and at the end of the test.
func resetHistoryCutover(t *testing.T) {
	t.Helper()
	reset := func() {
		historyCutover.mu.Lock()
		historyCutover.done = false
		historyCutover.checkedAt = time.Time{}
		historyCutover.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
	t.Setenv("MELLO_LEDGER_HISTORY_READ", "")
}

// useHistoryStoreReads sets an empty in-memory store and puts the node after
// both cutovers: the store is the truth for the ledger events and for the
// clips, stream sessions and recaps.
func useHistoryStoreReads(t *testing.T) *memLedgerStore {
	t.Helper()
	store := useStoreReads(t)
	resetHistoryCutover(t)
	historyCutover.mu.Lock()
	historyCutover.done = true
	historyCutover.mu.Unlock()
	return store
}

func historyMarkerSet(nk runtime.NakamaModule) bool {
	st, _, err := readHistoryMigrationState(context.Background(), nk)
	return err == nil && st != nil && st.DoneMs > 0
}

// forHistModes runs fn once for each mode.
func forHistModes(t *testing.T, fn func(t *testing.T, m histMode)) {
	t.Helper()
	for _, m := range histModes {
		m := m
		t.Run(m.name, func(t *testing.T) {
			if m.setup != nil {
				m.setup(t)
			}
			fn(t, m)
		})
	}
}

// ready is called by a test when its seed data is written.
func (m histMode) ready(t *testing.T, nk runtime.NakamaModule) {
	t.Helper()
	if m.cutover != nil {
		m.cutover(t, nk)
	}
}
