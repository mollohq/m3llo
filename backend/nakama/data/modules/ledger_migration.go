package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Migration of the legacy ledger documents to the durable store
// (plans/DURABLE-LEDGER.md section 5): backfill, parity check, marker.

const (
	LedgerMigrationCollection = "ledger_migration"
	ledgerMigrationStateKey   = "state"
	// ledgerParityAttempts is how often the parity check of one crew runs
	// before it counts as a mismatch. A live append between the two reads
	// gives a mismatch that the next attempt does not see.
	ledgerParityAttempts = 3
)

// ledgerParityRetryWait is the wait between two parity attempts. Tests set 0.
var ledgerParityRetryWait = 500 * time.Millisecond

// ledgerMigrationState is the marker document. Readers use the durable store
// when EventsDoneMs is set.
type ledgerMigrationState struct {
	EventsDoneMs int64 `json:"events_done_ms"`
}

// readLedgerMigrationState returns the marker, or nil when there is none.
func readLedgerMigrationState(ctx context.Context, nk runtime.NakamaModule) *ledgerMigrationState {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: LedgerMigrationCollection, Key: ledgerMigrationStateKey, UserID: SystemUserID},
	})
	if err != nil || len(objects) == 0 {
		return nil
	}
	var st ledgerMigrationState
	if err := json.Unmarshal([]byte(objects[0].GetValue()), &st); err != nil || st.EventsDoneMs <= 0 {
		return nil
	}
	return &st
}

func writeLedgerMigrationState(ctx context.Context, nk runtime.NakamaModule, st ledgerMigrationState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      LedgerMigrationCollection,
		Key:             ledgerMigrationStateKey,
		UserID:          SystemUserID,
		Value:           string(data),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	return err
}

// ledgerBackfillPlan is what the backfill takes from one legacy document.
type ledgerBackfillPlan struct {
	Events   []CrewEvent    // pulse events with an id, in document order
	AnchorMs int64          // time of the last append of the document
	Skipped  map[string]int // type -> events not copied (not a pulse type)
	NoID     int            // pulse events without an id, which the store cannot hold
}

// planLedgerBackfill reads one legacy document.
//
// The document holds exactly the events of the 7 days before its last append.
// UpdatedAt is that time, or later when a snapshot update or a remove wrote
// the document after it. So the anchor is UpdatedAt, but never so late that
// the oldest event of the document leaves the 7-day view.
func planLedgerBackfill(doc *CrewEventLedger) ledgerBackfillPlan {
	plan := ledgerBackfillPlan{Skipped: map[string]int{}}
	pulse := ledgerTypeSet(ledgerPulseTypes)
	minTs := int64(-1)
	for _, ev := range doc.Events {
		if minTs < 0 || ev.Timestamp < minTs {
			minTs = ev.Timestamp
		}
		switch {
		case !pulse[ev.Type]:
			plan.Skipped[ev.Type]++
		case ev.ID == "":
			plan.NoID++
		default:
			plan.Events = append(plan.Events, ev)
		}
	}
	if minTs < 0 {
		return plan
	}
	plan.AnchorMs = minTs + ledgerRecentWindowMs
	if doc.UpdatedAt > 0 && doc.UpdatedAt < plan.AnchorMs {
		plan.AnchorMs = doc.UpdatedAt
	}
	return plan
}

// ledgerParity compares the 7-day view of the store with the legacy document
// of one crew, by event id, pulse types only. It returns the ids the store
// lacks and the ids only the store has.
func ledgerParity(ctx context.Context, nk runtime.NakamaModule, store ledgerStore, crewID string) (missing, extra []string, err error) {
	doc, _ := readLedgerDoc(ctx, nk, crewID)
	view, err := store.RecentView(ctx, crewID)
	if err != nil {
		return nil, nil, err
	}
	pulse := ledgerTypeSet(ledgerPulseTypes)
	inDoc := map[string]bool{}
	for _, ev := range doc.Events {
		if pulse[ev.Type] {
			inDoc[ev.ID] = true
		}
	}
	inView := map[string]bool{}
	for _, ev := range view {
		inView[ev.ID] = true
		if !inDoc[ev.ID] {
			extra = append(extra, ev.ID)
		}
	}
	for id := range inDoc {
		if !inView[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra, nil
}

// ledgerParityOK runs the parity check of one crew, with retries, and logs a
// mismatch that stays.
func ledgerParityOK(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, store ledgerStore, crewID string) bool {
	var missing, extra []string
	var err error
	for attempt := 0; attempt < ledgerParityAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(ledgerParityRetryWait)
		}
		missing, extra, err = ledgerParity(ctx, nk, store, crewID)
		if err == nil && len(missing) == 0 && len(extra) == 0 {
			return true
		}
	}
	if err != nil {
		logger.Error("ledger_parity_mismatch crew=%s: store read failed: %v", crewID, err)
		return false
	}
	logger.Error("ledger_parity_mismatch crew=%s missing_in_store=%d extra_in_store=%d first_missing=%s first_extra=%s",
		crewID, len(missing), len(extra), firstOr(missing, "-"), firstOr(extra, "-"))
	return false
}

func firstOr(list []string, fallback string) string {
	if len(list) == 0 {
		return fallback
	}
	return list[0]
}

// listLedgerDocCrews returns the crew id of every legacy ledger document.
func listLedgerDocCrews(ctx context.Context, nk runtime.NakamaModule) ([]string, error) {
	var crews []string
	cursor := ""
	for {
		objects, nextCursor, err := nk.StorageList(ctx, "", SystemUserID, CrewEventsCollection, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, obj := range objects {
			if obj.Key != "" {
				crews = append(crews, obj.Key)
			}
		}
		if nextCursor == "" {
			return crews, nil
		}
		cursor = nextCursor
	}
}

// runLedgerBackfill copies every legacy ledger document into the store, then
// checks parity for every crew. It writes the marker when every crew passes
// and no marker exists yet. It is safe to repeat: the store skips the events
// it already holds. It reports whether the marker is set at the end.
func runLedgerBackfill(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, store ledgerStore) bool {
	crews, err := listLedgerDocCrews(ctx, nk)
	if err != nil {
		logger.Error("ledger_backfill_failed: list of ledger documents: %v", err)
		return false
	}

	clean := true
	inserted := 0
	for _, crewID := range crews {
		doc, _ := readLedgerDoc(ctx, nk, crewID)
		plan := planLedgerBackfill(doc)
		if len(plan.Skipped) > 0 {
			parts := make([]string, 0, len(plan.Skipped))
			for typ, n := range plan.Skipped {
				parts = append(parts, fmt.Sprintf("%s=%d", typ, n))
			}
			sort.Strings(parts)
			logger.Warn("ledger_backfill_skipped_types crew=%s %s", crewID, strings.Join(parts, " "))
		}
		if plan.NoID > 0 {
			logger.Error("ledger_backfill_no_id crew=%s events=%d", crewID, plan.NoID)
			clean = false
		}
		if len(plan.Events) == 0 && plan.AnchorMs == 0 {
			continue
		}
		n, err := store.Backfill(ctx, crewID, plan.Events, plan.AnchorMs)
		if err != nil {
			logger.Error("ledger_backfill_failed crew=%s: %v", crewID, err)
			clean = false
			continue
		}
		inserted += n
	}

	mismatches := 0
	for _, crewID := range crews {
		if !ledgerParityOK(ctx, nk, logger, store, crewID) {
			mismatches++
		}
	}

	if st := readLedgerMigrationState(ctx, nk); st != nil {
		logger.Info("ledger_backfill_done crews=%d inserted=%d mismatches=%d marker=set_before", len(crews), inserted, mismatches)
		return true
	}
	if !clean || mismatches > 0 {
		logger.Error("ledger_backfill_done crews=%d inserted=%d mismatches=%d marker=not_set. Readers stay on the legacy document", len(crews), inserted, mismatches)
		return false
	}
	if err := writeLedgerMigrationState(ctx, nk, ledgerMigrationState{EventsDoneMs: time.Now().UnixMilli()}); err != nil {
		logger.Error("ledger_backfill_done crews=%d inserted=%d marker=write_failed: %v", len(crews), inserted, err)
		return false
	}
	logger.Info("ledger_backfill_done crews=%d inserted=%d mismatches=0 marker=set", len(crews), inserted)
	return true
}

// StartLedgerBackfill runs the backfill once. InitModule starts it on every
// start of a node.
func StartLedgerBackfill(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger) {
	if crewLedger == nil {
		return
	}
	runLedgerBackfill(ctx, nk, logger, crewLedger)
}

// ---------------------------------------------------------------------------
// Cutover: which store the readers use
// ---------------------------------------------------------------------------

// ledgerMarkerRecheck is how often a node looks for the marker until it is
// set. Tests change it.
var ledgerMarkerRecheck = 30 * time.Second

var ledgerCutover struct {
	mu        sync.Mutex
	done      bool
	checkedAt time.Time
}

// ledgerReadsStore reports whether readers use the durable store. They do
// when the store is set, the marker of the backfill exists, and the env
// MELLO_LEDGER_READ is not "doc" (the rollback switch). Once a node has seen
// the marker it does not read it again.
func ledgerReadsStore(ctx context.Context, nk runtime.NakamaModule) bool {
	if crewLedger == nil || os.Getenv("MELLO_LEDGER_READ") == "doc" {
		return false
	}
	ledgerCutover.mu.Lock()
	defer ledgerCutover.mu.Unlock()
	if ledgerCutover.done {
		return true
	}
	if !ledgerCutover.checkedAt.IsZero() && time.Since(ledgerCutover.checkedAt) < ledgerMarkerRecheck {
		return false
	}
	ledgerCutover.checkedAt = time.Now()
	if readLedgerMigrationState(ctx, nk) != nil {
		ledgerCutover.done = true
	}
	return ledgerCutover.done
}

// ledgerGroupByCrew splits events that are ordered by crew into one slice
// per crew.
func ledgerGroupByCrew(events []CrewEvent) [][]CrewEvent {
	var out [][]CrewEvent
	start := 0
	for i := 1; i <= len(events); i++ {
		if i == len(events) || events[i].CrewID != events[start].CrewID {
			out = append(out, events[start:i])
			start = i
		}
	}
	return out
}
