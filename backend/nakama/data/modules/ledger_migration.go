package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Migration of the legacy ledger documents to the durable store
// (plans/DURABLE-LEDGER.md section 5): the reconcile, the soak, the marker
// and the cutover of the readers.

const (
	LedgerMigrationCollection = "ledger_migration"
	ledgerMigrationStateKey   = "state"

	// ledgerReconcileInterval is the time between two reconcile ticks.
	ledgerReconcileInterval = 5 * time.Minute
	// ledgerReconcileCrewsPerTick bounds the work of one tick. A pass over
	// more crews continues on the next tick.
	ledgerReconcileCrewsPerTick = 1000
	// ledgerReconcileRounds is how often one crew is repaired in one pass.
	// A write to the crew during a round shows in the next round.
	ledgerReconcileRounds = 3
	// ledgerDefaultSoak is the time dual write must have run before the
	// marker is set. An instance of the previous release writes only the
	// document, so the cutover waits until no such instance is left.
	ledgerDefaultSoak = 24 * time.Hour
)

// ledgerSoak is the soak time. The env MELLO_LEDGER_SOAK_SECONDS overrides it,
// for tests and for the e2e stack.
func ledgerSoak() time.Duration {
	if raw := os.Getenv("MELLO_LEDGER_SOAK_SECONDS"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return ledgerDefaultSoak
}

// ledgerMigrationState is the state document of the migration.
type ledgerMigrationState struct {
	// DualWriteStartedMs is when dual write first ran on this install.
	DualWriteStartedMs int64 `json:"dual_write_started_ms"`
	// EventsDoneMs is the marker. Readers use the durable store when it is set.
	EventsDoneMs int64 `json:"events_done_ms,omitempty"`
}

// readLedgerMigrationState returns the state document and its version, or
// nil when there is none.
func readLedgerMigrationState(ctx context.Context, nk runtime.NakamaModule) (*ledgerMigrationState, string, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: LedgerMigrationCollection, Key: ledgerMigrationStateKey, UserID: SystemUserID},
	})
	if err != nil {
		return nil, "", err
	}
	if len(objects) == 0 {
		return nil, "", nil
	}
	var st ledgerMigrationState
	if err := json.Unmarshal([]byte(objects[0].GetValue()), &st); err != nil {
		return nil, "", fmt.Errorf("ledger migration state: %w", err)
	}
	return &st, objects[0].GetVersion(), nil
}

// writeLedgerMigrationState writes the state document. version is "*" to
// create it, or the version that was read.
func writeLedgerMigrationState(ctx context.Context, nk runtime.NakamaModule, st ledgerMigrationState, version string) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      LedgerMigrationCollection,
		Key:             ledgerMigrationStateKey,
		UserID:          SystemUserID,
		Value:           string(data),
		Version:         version,
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	return err
}

// ensureLedgerMigrationState returns the state document. The first call on an
// install creates it with the start time of dual write.
func ensureLedgerMigrationState(ctx context.Context, nk runtime.NakamaModule) (*ledgerMigrationState, error) {
	st, _, err := readLedgerMigrationState(ctx, nk)
	if err != nil || st != nil {
		return st, err
	}
	created := ledgerMigrationState{DualWriteStartedMs: time.Now().UnixMilli()}
	if err := writeLedgerMigrationState(ctx, nk, created, "*"); err != nil {
		// Another node made it first.
		st, _, readErr := readLedgerMigrationState(ctx, nk)
		if readErr != nil {
			return nil, readErr
		}
		if st == nil {
			return nil, err
		}
		return st, nil
	}
	return &created, nil
}

// historyMigrationStateKey is the state document of phase 1b: the clips, the
// stream sessions of the durable copy and the weekly recaps. It is a document
// of its own. A release of phase 1a rewrites the state document of 1a as a
// whole, and would drop fields it does not know.
const historyMigrationStateKey = "history"

// historyMigrationState is the state document of phase 1b.
type historyMigrationState struct {
	// StartedMs is when the dual write of phase 1b first ran on this install.
	StartedMs int64 `json:"started_ms"`
	// DoneMs is the marker of phase 1b. The readers of clips, stream sessions
	// and recaps use the durable store when it is set.
	DoneMs int64 `json:"done_ms,omitempty"`
}

func readHistoryMigrationState(ctx context.Context, nk runtime.NakamaModule) (*historyMigrationState, string, error) {
	var st historyMigrationState
	found, version, err := readDocStrict(ctx, nk, LedgerMigrationCollection, historyMigrationStateKey, &st)
	if err != nil {
		return nil, "", fmt.Errorf("history migration state: %w", err)
	}
	if !found {
		return nil, "", nil
	}
	return &st, version, nil
}

func writeHistoryMigrationState(ctx context.Context, nk runtime.NakamaModule, st historyMigrationState, version string) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      LedgerMigrationCollection,
		Key:             historyMigrationStateKey,
		UserID:          SystemUserID,
		Value:           string(data),
		Version:         version,
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	return err
}

// ensureHistoryMigrationState returns the state document of phase 1b. The
// first call on an install creates it with the start time of its dual write.
func ensureHistoryMigrationState(ctx context.Context, nk runtime.NakamaModule) (*historyMigrationState, error) {
	st, _, err := readHistoryMigrationState(ctx, nk)
	if err != nil || st != nil {
		return st, err
	}
	created := historyMigrationState{StartedMs: time.Now().UnixMilli()}
	if err := writeHistoryMigrationState(ctx, nk, created, "*"); err != nil {
		// Another node made it first.
		st, _, readErr := readHistoryMigrationState(ctx, nk)
		if readErr != nil {
			return nil, readErr
		}
		if st == nil {
			return nil, err
		}
		return st, nil
	}
	return &created, nil
}

// readDocStrict reads one system storage object into out. It reports a read
// or decode error, which the reconcile must not take for an empty document.
func readDocStrict(ctx context.Context, nk runtime.NakamaModule, collection, key string, out interface{}) (found bool, version string, err error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: collection, Key: key, UserID: SystemUserID},
	})
	if err != nil {
		return false, "", err
	}
	if len(objects) == 0 {
		return false, "", nil
	}
	if err := json.Unmarshal([]byte(objects[0].GetValue()), out); err != nil {
		return false, "", err
	}
	return true, objects[0].GetVersion(), nil
}

// readLedgerDocStrict reads the legacy document of a crew and reports a read
// or decode error. readLedgerDoc turns both into an empty ledger, which the
// reconcile must not take for the truth.
func readLedgerDocStrict(ctx context.Context, nk runtime.NakamaModule, crewID string) (*CrewEventLedger, error) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: CrewEventsCollection, Key: crewID, UserID: SystemUserID},
	})
	if err != nil {
		return nil, err
	}
	doc := &CrewEventLedger{CrewID: crewID, Events: []CrewEvent{}}
	if len(objects) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal([]byte(objects[0].GetValue()), doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// ledgerDocPlan is what the reconcile takes from one legacy document.
type ledgerDocPlan struct {
	// Events are the events of the document as the store keeps them
	// (ledgerStoreEvent), in document order, each id once.
	Events []CrewEvent
	// AnchorMs is the time of the last append of the document. 0 when the
	// document gives no time.
	AnchorMs int64
	// WindowFromMs is a time the 7-day window of the document is known to
	// reach back to. A store event from that time on that the document does
	// not hold is not history that the document trimmed. HasWindow is false
	// when the document gives no time.
	WindowFromMs int64
	HasWindow    bool
	// NoID counts the events without an id. The store cannot hold them.
	NoID int
}

// planLedgerDoc reads one legacy document.
//
// The document holds exactly the events of the 7 days before its last append.
// UpdatedAt is that time, or later when a snapshot update or a remove wrote
// the document after it. So the anchor is UpdatedAt, but never so late that
// the oldest event of the document leaves the 7-day view.
func planLedgerDoc(doc *CrewEventLedger) ledgerDocPlan {
	var plan ledgerDocPlan
	seen := map[string]bool{}
	minTs := int64(-1)
	for _, ev := range doc.Events {
		if minTs < 0 || ev.Timestamp < minTs {
			minTs = ev.Timestamp
		}
		if ev.ID == "" {
			plan.NoID++
			continue
		}
		if seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		plan.Events = append(plan.Events, ledgerStoreEvent(doc.CrewID, ev))
	}
	switch {
	case minTs >= 0:
		plan.AnchorMs = minTs + ledgerRecentWindowMs
		if doc.UpdatedAt > 0 && doc.UpdatedAt < plan.AnchorMs {
			plan.AnchorMs = doc.UpdatedAt
		}
	case doc.UpdatedAt > 0:
		plan.AnchorMs = doc.UpdatedAt
	default:
		return plan
	}
	plan.WindowFromMs = plan.AnchorMs - ledgerRecentWindowMs
	plan.HasWindow = true
	return plan
}

// ledgerDiff is the difference between a legacy document and the 7-day view
// of the store.
type ledgerDiff struct {
	Missing []string // ids of the document that the view lacks
	Extra   []string // ids of the view, inside the window of the document, that the document lacks
	Stale   []string // ids of the view that are older than the window of the document
	Data    []string // ids whose data differs
	Order   bool     // the common events are in another order
}

func (d ledgerDiff) empty() bool {
	return len(d.Missing) == 0 && len(d.Extra) == 0 && len(d.Stale) == 0 && len(d.Data) == 0 && !d.Order
}

// ledgerSameData compares the data of two events as JSON values.
func ledgerSameData(a, b interface{}) bool {
	rawA, errA := ledgerEncodeData(a)
	rawB, errB := ledgerEncodeData(b)
	if errA != nil || errB != nil {
		return false
	}
	return reflect.DeepEqual(ledgerDecodeData(rawA), ledgerDecodeData(rawB))
}

// diffLedger compares the plan of a document with the 7-day view of the store.
func diffLedger(plan ledgerDocPlan, view []CrewEvent) ledgerDiff {
	var d ledgerDiff
	inView := make(map[string]CrewEvent, len(view))
	for _, ev := range view {
		inView[ev.ID] = ev
	}
	inDoc := make(map[string]bool, len(plan.Events))
	var docCommon []string
	for _, ev := range plan.Events {
		inDoc[ev.ID] = true
		got, ok := inView[ev.ID]
		if !ok {
			d.Missing = append(d.Missing, ev.ID)
			continue
		}
		docCommon = append(docCommon, ev.ID)
		if !ledgerSameData(ev.Data, got.Data) {
			d.Data = append(d.Data, ev.ID)
		}
	}
	var viewCommon []string
	for _, ev := range view {
		switch {
		case inDoc[ev.ID]:
			viewCommon = append(viewCommon, ev.ID)
		case plan.HasWindow && ev.Timestamp >= plan.WindowFromMs:
			d.Extra = append(d.Extra, ev.ID)
		default:
			d.Stale = append(d.Stale, ev.ID)
		}
	}
	d.Order = !ledgerSameOrder(docCommon, viewCommon)
	return d
}

// ledgerCrewResult is the result of the reconcile of one crew.
type ledgerCrewResult struct {
	Inserted int
	Pruned   int
	Updated  int
	NoID     int
	// Clean is true when the 7-day view of the store equals the document at
	// the end. After the cutover it only says that no document event is
	// left to insert.
	Clean bool
	Err   error
}

// reconcileLedgerCrew brings the store of one crew in line with its legacy
// document.
//
// Before the cutover (docWins) the document is the truth: the store gets the
// events it lacks, loses the events of the 7-day view that the document does
// not hold, and takes the data and the order of the document.
//
// After the cutover the store is the truth: the store only gets the events it
// lacks. These come from an instance of an older release or from a rollback
// period. No store row is deleted or overwritten.
//
// Each round reads the document again. So a write to the crew during a round
// (an append the round deleted as unknown, a remove the round brought back)
// is seen and repaired in the next round.
func reconcileLedgerCrew(ctx context.Context, nk runtime.NakamaModule, store ledgerStore, crewID string, docWins bool) ledgerCrewResult {
	var res ledgerCrewResult
	for round := 0; ; round++ {
		doc, err := readLedgerDocStrict(ctx, nk, crewID)
		if err != nil {
			res.Err = fmt.Errorf("document read: %w", err)
			return res
		}
		plan := planLedgerDoc(doc)
		res.NoID = plan.NoID
		view, err := store.RecentView(ctx, crewID)
		if err != nil {
			res.Err = fmt.Errorf("store read: %w", err)
			return res
		}
		diff := diffLedger(plan, view)
		if diff.empty() || (!docWins && len(diff.Missing) == 0) {
			res.Clean = true
			return res
		}
		if round >= ledgerReconcileRounds {
			return res
		}

		if docWins && len(diff.Extra) > 0 {
			if err := store.Prune(ctx, crewID, diff.Extra); err != nil {
				res.Err = fmt.Errorf("store prune: %w", err)
				return res
			}
			res.Pruned += len(diff.Extra)
		}
		inserted, err := store.Backfill(ctx, crewID, plan.Events, plan.AnchorMs, docWins)
		if err != nil {
			res.Err = fmt.Errorf("store backfill: %w", err)
			return res
		}
		res.Inserted += inserted
		if !docWins {
			if inserted == 0 {
				// What is left was removed from the store on purpose
				// (ledgerStore.Delete) and stays out.
				res.Clean = true
				return res
			}
			continue
		}
		for _, id := range diff.Data {
			data := ledgerPlanData(plan, id)
			err := store.UpdateData(ctx, crewID, id, func(json.RawMessage) (json.RawMessage, error) { return data, nil })
			if err != nil {
				res.Err = fmt.Errorf("store data update: %w", err)
				return res
			}
			res.Updated++
		}
	}
}

func ledgerPlanData(plan ledgerDocPlan, eventID string) json.RawMessage {
	for _, ev := range plan.Events {
		if ev.ID == eventID {
			if raw, err := ledgerEncodeData(ev.Data); err == nil {
				return raw
			}
		}
	}
	return json.RawMessage("null")
}

// ledgerPassStats are the totals of one pass over every crew.
type ledgerPassStats struct {
	Crews, Inserted, Pruned, Updated, NoID int
	// Unclean counts the crews whose view did not equal the document at the
	// end. Failed counts the crews with a read or write error.
	Unclean, Failed int
	ListFailed      bool
}

func (p ledgerPassStats) clean() bool {
	return p.Unclean == 0 && p.Failed == 0 && !p.ListFailed
}

// ledgerReconcileStep is one collection of legacy documents that a pass
// visits. history is nil for the ledger documents of phase 1a.
type ledgerReconcileStep struct {
	collection string
	history    *historyKind
}

// ledgerReconcileSteps are the collections of one pass, in order. The ledger
// documents come first: the stream sessions of phase 1b read them.
func ledgerReconcileSteps() []ledgerReconcileStep {
	steps := []ledgerReconcileStep{{collection: CrewEventsCollection}}
	for _, kind := range historyKinds {
		steps = append(steps, ledgerReconcileStep{collection: kind.collection, history: kind})
	}
	return steps
}

// ledgerReconciler runs the reconcile. A pass visits every legacy document:
// the ledger documents, then the documents of phase 1b. A tick does a bounded
// part of a pass.
type ledgerReconciler struct {
	nk     runtime.NakamaModule
	logger runtime.Logger
	// step is the collection the pass is in, cursor the place in its list.
	step   int
	cursor string
	pass   ledgerPassStats
	hist   historyPassStats
	// last is the result of the last finished pass, and lastMarker what the
	// pass did with the marker. lastHist and lastHistMarker are the same for
	// phase 1b.
	last           ledgerPassStats
	lastMarker     string
	lastHist       historyPassStats
	lastHistMarker string
}

// tick does up to ledgerReconcileCrewsPerTick documents of the current pass.
// It reports whether the pass ended.
func (r *ledgerReconciler) tick(ctx context.Context) bool {
	store := currentLedger()
	if store == nil {
		return false
	}
	st, err := ensureLedgerMigrationState(ctx, r.nk)
	if err != nil {
		r.logger.Error("ledger_reconcile_failed: migration state: %v", err)
		return false
	}
	hist, err := ensureHistoryMigrationState(ctx, r.nk)
	if err != nil {
		r.logger.Error("ledger_reconcile_failed: history migration state: %v", err)
		return false
	}
	docWins := st.EventsDoneMs == 0
	// The marker of phase 1b is open until it is set. The content repair
	// follows the truth: a kill switch makes the documents the truth again
	// after the marker.
	histMarkerOpen := hist.DoneMs == 0
	histDocWins := historyDocumentIsTruth(st, hist)
	steps := ledgerReconcileSteps()

	done := 0
	for done < ledgerReconcileCrewsPerTick {
		step := steps[r.step]
		objects, next, err := r.nk.StorageList(ctx, "", SystemUserID, step.collection, 100, r.cursor)
		if err != nil {
			r.logger.Error("ledger_reconcile_failed: list of %s documents: %v", step.collection, err)
			r.pass.ListFailed = true
			r.finishPass(ctx, docWins, histMarkerOpen)
			return true
		}
		for _, obj := range objects {
			if obj.Key == "" {
				continue
			}
			if step.history != nil {
				r.reconcileHistoryDoc(ctx, store, step.history, obj.Key, histDocWins)
			} else {
				r.reconcileLedgerDoc(ctx, store, obj.Key, docWins)
			}
			done++
		}
		r.cursor = next
		if next == "" {
			r.step++
			if r.step >= len(steps) {
				r.finishPass(ctx, docWins, histMarkerOpen)
				return true
			}
		}
	}
	return false
}

func (r *ledgerReconciler) reconcileLedgerDoc(ctx context.Context, store ledgerStore, crewID string, docWins bool) {
	res := reconcileLedgerCrew(ctx, r.nk, store, crewID, docWins)
	r.pass.Crews++
	r.pass.Inserted += res.Inserted
	r.pass.Pruned += res.Pruned
	r.pass.Updated += res.Updated
	r.pass.NoID += res.NoID
	switch {
	case res.Err != nil:
		r.pass.Failed++
		r.logger.Error("ledger_reconcile_crew_failed crew=%s: %v", crewID, res.Err)
	case !res.Clean:
		r.pass.Unclean++
		r.logger.Error("ledger_parity_mismatch crew=%s: the store differs from the document after %d rounds", crewID, ledgerReconcileRounds)
	}
}

func (r *ledgerReconciler) reconcileHistoryDoc(ctx context.Context, store ledgerStore, kind *historyKind, crewID string, docWins bool) {
	res := reconcileHistoryCrew(ctx, r.nk, store, kind, crewID, docWins)
	r.hist.Docs++
	r.hist.Inserted += res.Inserted
	r.hist.Updated += res.Updated
	r.hist.NoID += res.NoID
	r.hist.RefHeld += res.RefHeld
	switch {
	case res.Err != nil:
		r.hist.Failed++
		r.logger.Error("ledger_history_crew_failed kind=%s crew=%s: %v", kind.name, crewID, res.Err)
	case !res.Clean:
		r.hist.Unclean++
		r.logger.Error("ledger_history_mismatch kind=%s crew=%s: the store differs from the document after %d rounds", kind.name, crewID, ledgerReconcileRounds)
	}
}

// finishPass logs the pass and sets each marker when the pass was clean and
// its soak time is over.
func (r *ledgerReconciler) finishPass(ctx context.Context, docWins, histMarkerOpen bool) {
	p, h := r.pass, r.hist
	r.pass, r.hist = ledgerPassStats{}, historyPassStats{}
	r.step, r.cursor = 0, ""

	marker := "set_before"
	if docWins {
		marker = r.trySetMarker(ctx, p)
	}
	r.last, r.lastMarker = p, marker
	line := fmt.Sprintf("ledger_reconcile_pass crews=%d inserted=%d pruned=%d updated=%d no_id=%d unclean=%d failed=%d marker=%s",
		p.Crews, p.Inserted, p.Pruned, p.Updated, p.NoID, p.Unclean, p.Failed, marker)
	if p.clean() {
		r.logger.Info("%s", line)
	} else {
		r.logger.Error("%s", line)
	}

	histMarker := "set_before"
	if histMarkerOpen {
		histMarker = r.trySetHistoryMarker(ctx, p, h)
	}
	r.lastHist, r.lastHistMarker = h, histMarker
	line = fmt.Sprintf("ledger_history_pass docs=%d inserted=%d updated=%d no_id=%d ref_held=%d unclean=%d failed=%d marker=%s",
		h.Docs, h.Inserted, h.Updated, h.NoID, h.RefHeld, h.Unclean, h.Failed, histMarker)
	if h.clean() && h.NoID == 0 {
		r.logger.Info("%s", line)
	} else {
		r.logger.Error("%s", line)
	}
}

func (r *ledgerReconciler) trySetMarker(ctx context.Context, p ledgerPassStats) string {
	if !p.clean() {
		return "not_set_pass_not_clean"
	}
	st, version, err := readLedgerMigrationState(ctx, r.nk)
	if err != nil || st == nil {
		return "not_set_state_unreadable"
	}
	if st.EventsDoneMs > 0 {
		return "set_before"
	}
	now := time.Now().UnixMilli()
	if wait := st.DualWriteStartedMs + ledgerSoak().Milliseconds() - now; wait > 0 {
		return fmt.Sprintf("not_set_soak_%dmin_left", wait/60_000+1)
	}
	st.EventsDoneMs = now
	if err := writeLedgerMigrationState(ctx, r.nk, *st, version); err != nil {
		r.logger.Error("ledger_marker_write_failed: %v", err)
		return "not_set_write_failed"
	}
	return "set"
}

// trySetHistoryMarker sets the marker of phase 1b. It needs a clean pass of
// both phases, the marker of phase 1a, and the soak time of phase 1b.
func (r *ledgerReconciler) trySetHistoryMarker(ctx context.Context, p ledgerPassStats, h historyPassStats) string {
	if !p.clean() || !h.clean() {
		return "not_set_pass_not_clean"
	}
	if h.NoID > 0 {
		// An entry that no row can hold would vanish from the readers at
		// the cutover.
		return "not_set_entries_without_id"
	}
	events, _, err := readLedgerMigrationState(ctx, r.nk)
	if err != nil || events == nil {
		return "not_set_state_unreadable"
	}
	if events.EventsDoneMs == 0 {
		return "not_set_events_marker_not_set"
	}
	st, version, err := readHistoryMigrationState(ctx, r.nk)
	if err != nil || st == nil {
		return "not_set_state_unreadable"
	}
	if st.DoneMs > 0 {
		return "set_before"
	}
	now := time.Now().UnixMilli()
	if wait := st.StartedMs + ledgerSoak().Milliseconds() - now; wait > 0 {
		return fmt.Sprintf("not_set_soak_%dmin_left", wait/60_000+1)
	}
	st.DoneMs = now
	if err := writeHistoryMigrationState(ctx, r.nk, *st, version); err != nil {
		r.logger.Error("ledger_history_marker_write_failed: %v", err)
		return "not_set_write_failed"
	}
	return "set"
}

// StartLedgerReconcile runs the reconcile at start and then on a timer.
// InitModule starts it once per node.
func StartLedgerReconcile(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger) {
	r := &ledgerReconciler{nk: nk, logger: logger}
	ticker := time.NewTicker(ledgerReconcileInterval)
	defer ticker.Stop()
	for {
		r.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ---------------------------------------------------------------------------
// Cutover: which store is the truth
// ---------------------------------------------------------------------------

// ledgerMarkerRecheck is how often a node looks for the marker until it is
// set. Tests change it.
var ledgerMarkerRecheck = 30 * time.Second

var ledgerCutover struct {
	mu        sync.Mutex
	done      bool
	checkedAt time.Time
}

// ledgerTruthStore returns the durable store when it is the truth: readers
// read it, and writers write it first. It is when the store is set, the
// marker exists, and the env MELLO_LEDGER_READ is not "doc" (the kill
// switch). Otherwise it returns nil and the legacy document is the truth.
// Once a node has seen the marker it does not read it again.
func ledgerTruthStore(ctx context.Context, nk runtime.NakamaModule) ledgerStore {
	store := currentLedger()
	if store == nil || os.Getenv("MELLO_LEDGER_READ") == "doc" {
		return nil
	}
	ledgerCutover.mu.Lock()
	defer ledgerCutover.mu.Unlock()
	if ledgerCutover.done {
		return store
	}
	if !ledgerCutover.checkedAt.IsZero() && time.Since(ledgerCutover.checkedAt) < ledgerMarkerRecheck {
		return nil
	}
	ledgerCutover.checkedAt = time.Now()
	if st, _, err := readLedgerMigrationState(ctx, nk); err == nil && st != nil && st.EventsDoneMs > 0 {
		ledgerCutover.done = true
		return store
	}
	return nil
}

// historyCutover is what this node knows about the marker of phase 1b.
var historyCutover struct {
	mu        sync.Mutex
	done      bool
	checkedAt time.Time
}

// historyTruthStore returns the durable store when it is the truth for the
// clips, the stream sessions and the weekly recaps of a crew: readers read
// it, and writers write it first. It is when the store is the truth for the
// ledger events (ledgerTruthStore), the marker of phase 1b exists, and the
// env MELLO_LEDGER_HISTORY_READ is not "doc". That env is the kill switch of
// phase 1b alone. MELLO_LEDGER_READ=doc switches both phases back.
func historyTruthStore(ctx context.Context, nk runtime.NakamaModule) ledgerStore {
	if os.Getenv("MELLO_LEDGER_HISTORY_READ") == "doc" {
		return nil
	}
	store := ledgerTruthStore(ctx, nk)
	if store == nil {
		return nil
	}
	historyCutover.mu.Lock()
	defer historyCutover.mu.Unlock()
	if historyCutover.done {
		return store
	}
	if !historyCutover.checkedAt.IsZero() && time.Since(historyCutover.checkedAt) < ledgerMarkerRecheck {
		return nil
	}
	historyCutover.checkedAt = time.Now()
	if st, _, err := readHistoryMigrationState(ctx, nk); err == nil && st != nil && st.DoneMs > 0 {
		historyCutover.done = true
		return store
	}
	return nil
}

// historyDocumentIsTruth reports whether the legacy documents of phase 1b are
// the truth on this node: until both markers are set, and under a kill switch
// (historyTruthStore). The reconcile then repairs the content of the store
// from the documents. A store write that failed while the documents were the
// truth is repaired this way, also after the marker.
func historyDocumentIsTruth(events *ledgerMigrationState, hist *historyMigrationState) bool {
	if os.Getenv("MELLO_LEDGER_HISTORY_READ") == "doc" || os.Getenv("MELLO_LEDGER_READ") == "doc" {
		return true
	}
	return events == nil || events.EventsDoneMs == 0 || hist == nil || hist.DoneMs == 0
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
