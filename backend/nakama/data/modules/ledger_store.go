package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// The durable crew ledger (plans/DURABLE-LEDGER.md). Every event is kept. The
// 7-day cut that readers know is a view on top of it (RecentView).

// ledgerPulseTypes are the event types of the legacy ledger document. The
// 7-day view returns only these.
var ledgerPulseTypes = []string{
	"voice_session", "stream_session", "game_session",
	"member_joined", "member_left", "chat_activity", "moment",
}

// ledgerCountedTypes are the event types that count toward the free window.
var ledgerCountedTypes = []string{"clip", "stream_session", "game_session", "weekly_recap"}

// ledgerLegacyTypePrefix marks an event that a legacy ledger document held
// under a type that is not a pulse type: the clip and weekly_recap events
// that old code and the dev seed wrote into the ledger. The store keeps such
// an event as "ledger_clip" or "ledger_weekly_recap", so it does not mix with
// the real clip and recap rows of phase 1b and no counted query sees it.
// RecentView gives it back under its document type.
const ledgerLegacyTypePrefix = "ledger_"

// ledgerStoredType is the type the store keeps for an event of the legacy
// ledger path.
func ledgerStoredType(typ string) string {
	for _, t := range ledgerPulseTypes {
		if t == typ {
			return typ
		}
	}
	return ledgerLegacyTypePrefix + typ
}

// ledgerReaderType is the document type of a stored type, and whether the
// 7-day view holds it. A row that is not of the legacy ledger path (a clip
// row of phase 1b) is not in the view.
func ledgerReaderType(stored string) (string, bool) {
	if strings.HasPrefix(stored, ledgerLegacyTypePrefix) {
		return strings.TrimPrefix(stored, ledgerLegacyTypePrefix), true
	}
	for _, t := range ledgerPulseTypes {
		if t == stored {
			return stored, true
		}
	}
	return stored, false
}

// ledgerRecentWindowMs is the length of the 7-day view.
const ledgerRecentWindowMs = int64(EventRollingWindowDays) * 24 * 60 * 60 * 1000

// errLedgerEventNotFound is the error of UpdateData for an unknown event.
var errLedgerEventNotFound = errors.New("ledger event not found")

// weekCount is the number of events of one week. WeekStart is weekStartOf.
type weekCount struct {
	WeekStart int64
	N         int
}

// ledgerStore is the durable store of crew events. An event is identified by
// its crew and its id. All times are Unix ms.
type ledgerStore interface {
	// Append stores one event at the time of ledgerNow, after every event the
	// crew holds. It reports false when the crew already holds an event with
	// this id. It moves the anchor of the 7-day view in both cases, as the
	// legacy trim ran on every append.
	Append(ctx context.Context, ev CrewEvent, ref string) (inserted bool, err error)
	// Backfill stores the events of a legacy document that the crew does not
	// hold, and puts them at their document position in the 7-day view.
	// docEvents are all events of the document, in document order. anchorMs
	// is the time of the last append of the document; the anchor never moves
	// back. An id that Delete removed is not stored again.
	//
	// With docOrderWins the view gets the order of the document. Without it
	// the events the crew already holds keep their order, and each new event
	// goes after the nearest event that precedes it in the document.
	Backfill(ctx context.Context, crewID string, docEvents []CrewEvent, anchorMs int64, docOrderWins bool) (inserted int, err error)
	// UpdateData replaces the data of one event with the result of fn.
	UpdateData(ctx context.Context, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error
	// Delete removes one event and remembers the id, so that Backfill does
	// not bring it back from a document. An unknown event is not an error.
	Delete(ctx context.Context, crewID, eventID string) error
	// Prune removes events and does not remember the ids. The reconcile uses
	// it before the cutover, when the document is the truth.
	Prune(ctx context.Context, crewID string, eventIDs []string) error
	// Between returns the events with fromMs <= ts <= toMs, oldest first.
	// An empty types list means every type.
	Between(ctx context.Context, crewID string, fromMs, toMs int64, types []string) ([]CrewEvent, error)
	// NewestN returns up to n events with ts < beforeMs, newest first.
	NewestN(ctx context.Context, crewID string, types []string, beforeMs int64, n int) ([]CrewEvent, error)
	// CountOlder counts the events with ts < beforeMs.
	CountOlder(ctx context.Context, crewID string, types []string, beforeMs int64) (int, error)
	// WeeksBefore returns the weeks before beforeWeek that hold events,
	// newest week first, up to limit weeks.
	WeeksBefore(ctx context.Context, crewID string, types []string, beforeWeek int64, limit int) ([]weekCount, error)
	// RecentView returns what the legacy document held: the events of the
	// legacy ledger path (pulse types and "ledger_" types, under their
	// document type) of the 7 days before the last append of the crew, in
	// append order.
	RecentView(ctx context.Context, crewID string) ([]CrewEvent, error)
	// ByType returns the events of one type with ts >= sinceMs in every crew,
	// ordered by crew and time.
	ByType(ctx context.Context, typ string, sinceMs int64) ([]CrewEvent, error)
	// DeleteCrew removes every event of a crew.
	DeleteCrew(ctx context.Context, crewID string) error
	// EventsNamingUser returns every event that names the user.
	EventsNamingUser(ctx context.Context, userID string) ([]CrewEvent, error)

	// Put stores rows that do not come from the legacy ledger document: a
	// clip, a weekly recap, a stream session of the durable copy. Such a row
	// is not in the 7-day view, and Put does not move the anchor of the view.
	// A row whose id the crew holds, or whose id Delete removed, is skipped.
	// It returns the number of rows it stored.
	Put(ctx context.Context, crewID string, rows []ledgerRow) (inserted int, err error)
	// PutUnique stores one such row, unless the crew holds a row of the same
	// type with the same ref. It then stores nothing and returns the rows
	// that hold the ref, oldest first. Two calls at once store one row.
	PutUnique(ctx context.Context, crewID string, row ledgerRow) (held []CrewEvent, err error)
	// ByRef returns the rows of one type with one ref, oldest first.
	ByRef(ctx context.Context, crewID, typ, ref string) ([]CrewEvent, error)
	// ByIDs returns the rows with these ids, in no order.
	ByIDs(ctx context.Context, crewID string, ids []string) ([]CrewEvent, error)
	// ByRefs returns the rows of one type with one of these refs, in no
	// order. An empty ref matches no row.
	ByRefs(ctx context.Context, crewID, typ string, refs []string) ([]CrewEvent, error)
	// RefsBefore returns up to n rows of one type that have a ref below
	// beforeRef, the highest ref first. An empty beforeRef is no bound. A row
	// with no ref is never returned. Refs compare as text, so a caller that
	// wants an order uses refs of one width (recapWeekRef).
	RefsBefore(ctx context.Context, crewID, typ, beforeRef string, n int) ([]CrewEvent, error)
	// TopInRange returns up to n rows of one type with fromMs <= ts <= toMs.
	// The rows whose data holds the text value in field come first. In each
	// of the two groups the newest row is first, and of two rows of one time
	// the one with the lower id. The store sorts, so a caller that shows a
	// few rows of a long range does not load the range.
	TopInRange(ctx context.Context, crewID, typ string, fromMs, toMs int64, field, value string, n int) ([]CrewEvent, error)
}

// ledgerRow is an event and its ref, for Put.
type ledgerRow struct {
	Event CrewEvent
	Ref   string
}

// errLedgerIDTaken is the error of PutUnique when the ref is free and the id
// of the row is not.
var errLedgerIDTaken = errors.New("ledger event id is taken")

// crewLedger holds the durable store. It is empty until the schema is ready,
// and in tests that do not set it: the module then uses only the legacy
// document. The schema retry sets it from another goroutine, so every use
// goes through currentLedger.
var crewLedger atomic.Pointer[ledgerStoreBox]

type ledgerStoreBox struct{ store ledgerStore }

// currentLedger returns the durable store, or nil when there is none.
func currentLedger() ledgerStore {
	if box := crewLedger.Load(); box != nil {
		return box.store
	}
	return nil
}

// setLedger sets the durable store. nil removes it.
func setLedger(store ledgerStore) {
	if store == nil {
		crewLedger.Store(nil)
		return
	}
	crewLedger.Store(&ledgerStoreBox{store: store})
}

// ledgerMergeOrder is the order of the 7-day view after a backfill. view is
// the current order, with the new events at the end. doc is the order of the
// document.
//
// With docOrderWins the events of the document take the document order, after
// the events the document does not hold. Without it the old events keep their
// order, and each new event goes right after the nearest event that precedes
// it in the document, or to the front when there is none.
func ledgerMergeOrder(view, doc []string, isNew map[string]bool, docOrderWins bool) []string {
	inView := make(map[string]bool, len(view))
	for _, id := range view {
		inView[id] = true
	}
	// The document order, without repeats and without ids the view lacks.
	seen := map[string]bool{}
	docOrder := make([]string, 0, len(doc))
	for _, id := range doc {
		if inView[id] && !seen[id] {
			seen[id] = true
			docOrder = append(docOrder, id)
		}
	}
	out := make([]string, 0, len(view))
	if docOrderWins {
		for _, id := range view {
			if !seen[id] {
				out = append(out, id)
			}
		}
		return append(out, docOrder...)
	}
	for _, id := range view {
		if !isNew[id] || !seen[id] {
			out = append(out, id)
		}
	}
	for i, id := range docOrder {
		if !isNew[id] {
			continue
		}
		at := 0
		for j := i - 1; j >= 0 && at == 0; j-- {
			for k, placed := range out {
				if placed == docOrder[j] {
					at = k + 1
					break
				}
			}
		}
		out = append(out, "")
		copy(out[at+1:], out[at:])
		out[at] = id
	}
	return out
}

// eventUserIDs lists every user an event names: the actor, the participants,
// players and viewers, the streamer, the inviter, the clip participants and
// the members a weekly recap lists. The actor comes first. No id repeats.
func eventUserIDs(ev CrewEvent) []string {
	var d struct {
		ParticipantIDs []string `json:"participant_ids"`
		PlayerIDs      []string `json:"player_ids"`
		ViewerIDs      []string `json:"viewer_ids"`
		StreamerID     string   `json:"streamer_id"`
		InvitedBy      string   `json:"invited_by"`
		Participants   []string `json:"participants"`
	}
	if raw, err := json.Marshal(ev.Data); err == nil {
		// A payload that is not an object names nobody but the actor.
		_ = json.Unmarshal(raw, &d)
	}
	seen := map[string]bool{}
	out := []string{}
	add := func(ids ...string) {
		for _, id := range ids {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	add(ev.ActorID)
	add(d.ParticipantIDs...)
	add(d.PlayerIDs...)
	add(d.ViewerIDs...)
	add(d.StreamerID, d.InvitedBy)
	add(d.Participants...)
	// A weekly recap names its members in lists of its own. The scrub of a
	// deleted account finds the recap rows of a user through them.
	if typ, _ := ledgerReaderType(ev.Type); typ == ledgerTypeWeeklyRecap {
		if r, ok := recapFromRow(ev); ok {
			add(recapUserIDs(r)...)
		}
	}
	return out
}

// eventRef is the lookup key of an event next to its id: the session id of a
// stream session. It is empty for the other pulse types.
func eventRef(ev CrewEvent) string {
	if ev.Type != "stream_session" {
		return ""
	}
	var d struct {
		SessionID string `json:"session_id"`
	}
	if raw, err := json.Marshal(ev.Data); err == nil {
		_ = json.Unmarshal(raw, &d)
	}
	return d.SessionID
}

// ledgerEncodeData is the JSON a store keeps for the data of an event.
func ledgerEncodeData(data interface{}) (json.RawMessage, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// ledgerDecodeData gives the data of a stored event the shape a read of the
// legacy document gives: a JSON object is a map.
func ledgerDecodeData(raw []byte) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

func ledgerTypeSet(types []string) map[string]bool {
	if len(types) == 0 {
		return nil
	}
	set := make(map[string]bool, len(types))
	for _, t := range types {
		set[t] = true
	}
	return set
}

// ---------------------------------------------------------------------------
// In-memory store, for tests and as the reference of the conformance run
// ---------------------------------------------------------------------------

type memLedgerRow struct {
	ev        CrewEvent // Data is not used; data holds the JSON
	data      json.RawMessage
	ref       string
	createdMs int64
	pos       int64 // order of the 7-day view. 0: the row is not in the view
	users     []string
}

type memLedgerAnchor struct {
	lastAppendMs int64
	lastPos      int64
}

type memLedgerStore struct {
	mu      sync.Mutex
	rows    map[string][]*memLedgerRow          // crew id -> rows
	anchors map[string]*memLedgerAnchor         // crew id -> last append and last position
	weeks   map[string]map[int64]map[string]int // crew id -> week start -> type -> count
	deleted map[string]map[string]bool          // crew id -> ids that Delete removed
}

func newMemLedgerStore() *memLedgerStore {
	return &memLedgerStore{
		rows:    map[string][]*memLedgerRow{},
		anchors: map[string]*memLedgerAnchor{},
		weeks:   map[string]map[int64]map[string]int{},
		deleted: map[string]map[string]bool{},
	}
}

func (m *memLedgerStore) find(crewID, eventID string) int {
	for i, r := range m.rows[crewID] {
		if r.ev.ID == eventID {
			return i
		}
	}
	return -1
}

func (m *memLedgerStore) bumpWeek(crewID string, ev CrewEvent, delta int) {
	byWeek, ok := m.weeks[crewID]
	if !ok {
		byWeek = map[int64]map[string]int{}
		m.weeks[crewID] = byWeek
	}
	w := weekStartOf(ev.Timestamp)
	if byWeek[w] == nil {
		byWeek[w] = map[string]int{}
	}
	byWeek[w][ev.Type] += delta
	if byWeek[w][ev.Type] <= 0 {
		delete(byWeek[w], ev.Type)
	}
}

// anchor returns the anchor of a crew and moves its time forward to atMs.
func (m *memLedgerStore) anchor(crewID string, atMs int64) *memLedgerAnchor {
	a, ok := m.anchors[crewID]
	if !ok {
		a = &memLedgerAnchor{lastAppendMs: atMs}
		m.anchors[crewID] = a
	}
	if atMs > a.lastAppendMs {
		a.lastAppendMs = atMs
	}
	return a
}

func (m *memLedgerStore) insert(ev CrewEvent, ref string, createdMs, pos int64) (bool, error) {
	if m.find(ev.CrewID, ev.ID) >= 0 {
		return false, nil
	}
	data, err := ledgerEncodeData(ev.Data)
	if err != nil {
		return false, err
	}
	users := eventUserIDs(ev)
	ev.Data = nil
	m.rows[ev.CrewID] = append(m.rows[ev.CrewID], &memLedgerRow{ev: ev, data: data, ref: ref, createdMs: createdMs, pos: pos, users: users})
	m.bumpWeek(ev.CrewID, ev, 1)
	return true, nil
}

func (r *memLedgerRow) event() CrewEvent {
	ev := r.ev
	ev.Data = ledgerDecodeData(r.data)
	return ev
}

func (m *memLedgerStore) Append(_ context.Context, ev CrewEvent, ref string) (bool, error) {
	if ev.CrewID == "" || ev.ID == "" {
		return false, errors.New("ledger append needs a crew id and an event id")
	}
	nowMs := ledgerNow().UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.anchor(ev.CrewID, nowMs)
	a.lastPos++
	inserted, err := m.insert(ev, ref, nowMs, a.lastPos)
	if err != nil {
		return false, err
	}
	delete(m.deleted[ev.CrewID], ev.ID)
	return inserted, nil
}

// viewRows returns the rows of the 7-day view of a crew, in view order.
func (m *memLedgerStore) viewRows(crewID string) []*memLedgerRow {
	a, ok := m.anchors[crewID]
	if !ok {
		return nil
	}
	cutoff := a.lastAppendMs - ledgerRecentWindowMs
	return m.selectRows(crewID, func(r *memLedgerRow) bool {
		_, inView := ledgerReaderType(r.ev.Type)
		return inView && r.pos > 0 && r.ev.Timestamp >= cutoff
	}, func(a, b *memLedgerRow) bool { return a.pos < b.pos })
}

func (m *memLedgerStore) Backfill(_ context.Context, crewID string, docEvents []CrewEvent, anchorMs int64, docOrderWins bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.anchor(crewID, anchorMs)
	inserted := 0
	isNew := map[string]bool{}
	docIDs := make([]string, 0, len(docEvents))
	for _, ev := range docEvents {
		if ev.ID == "" {
			continue
		}
		docIDs = append(docIDs, ev.ID)
		if m.deleted[crewID][ev.ID] {
			continue
		}
		ev.CrewID = crewID
		a.lastPos++
		ok, err := m.insert(ev, eventRef(ev), anchorMs, a.lastPos)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
			isNew[ev.ID] = true
			continue
		}
		// A row that Put stored is not in the view. The document holds the
		// event, so it now gets a position.
		if i := m.find(crewID, ev.ID); i >= 0 && m.rows[crewID][i].pos == 0 && m.rows[crewID][i].ev.Type == ev.Type {
			m.rows[crewID][i].pos = a.lastPos
			isNew[ev.ID] = true
		}
	}
	view := m.viewRows(crewID)
	current := make([]string, 0, len(view))
	byID := make(map[string]*memLedgerRow, len(view))
	for _, r := range view {
		current = append(current, r.ev.ID)
		byID[r.ev.ID] = r
	}
	wanted := ledgerMergeOrder(current, docIDs, isNew, docOrderWins)
	if !ledgerSameOrder(current, wanted) {
		for _, id := range wanted {
			a.lastPos++
			byID[id].pos = a.lastPos
		}
	}
	return inserted, nil
}

func (m *memLedgerStore) UpdateData(_ context.Context, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(crewID, eventID)
	if i < 0 {
		return errLedgerEventNotFound
	}
	row := m.rows[crewID][i]
	out, err := fn(append(json.RawMessage(nil), row.data...))
	if err != nil {
		return err
	}
	row.data = out
	return nil
}

func (m *memLedgerStore) remove(crewID, eventID string) {
	i := m.find(crewID, eventID)
	if i < 0 {
		return
	}
	rows := m.rows[crewID]
	m.bumpWeek(crewID, rows[i].ev, -1)
	m.rows[crewID] = append(rows[:i:i], rows[i+1:]...)
}

func (m *memLedgerStore) Delete(_ context.Context, crewID, eventID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remove(crewID, eventID)
	if m.deleted[crewID] == nil {
		m.deleted[crewID] = map[string]bool{}
	}
	m.deleted[crewID][eventID] = true
	return nil
}

func (m *memLedgerStore) Prune(_ context.Context, crewID string, eventIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range eventIDs {
		m.remove(crewID, id)
	}
	return nil
}

// selectRows returns the rows of a crew that pass keep, sorted by less.
func (m *memLedgerStore) selectRows(crewID string, keep func(*memLedgerRow) bool, less func(a, b *memLedgerRow) bool) []*memLedgerRow {
	var out []*memLedgerRow
	for _, r := range m.rows[crewID] {
		if keep(r) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

func memLedgerEvents(rows []*memLedgerRow) []CrewEvent {
	out := make([]CrewEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.event())
	}
	return out
}

func memByTsAsc(a, b *memLedgerRow) bool {
	if a.ev.Timestamp != b.ev.Timestamp {
		return a.ev.Timestamp < b.ev.Timestamp
	}
	return a.ev.ID < b.ev.ID
}

func (m *memLedgerStore) Between(_ context.Context, crewID string, fromMs, toMs int64, types []string) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := ledgerTypeSet(types)
	rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
		return r.ev.Timestamp >= fromMs && r.ev.Timestamp <= toMs && (set == nil || set[r.ev.Type])
	}, memByTsAsc)
	return memLedgerEvents(rows), nil
}

func (m *memLedgerStore) NewestN(_ context.Context, crewID string, types []string, beforeMs int64, n int) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := ledgerTypeSet(types)
	rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
		return r.ev.Timestamp < beforeMs && (set == nil || set[r.ev.Type])
	}, func(a, b *memLedgerRow) bool { return memByTsAsc(b, a) })
	if n < 0 {
		n = 0
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	return memLedgerEvents(rows), nil
}

func (m *memLedgerStore) CountOlder(_ context.Context, crewID string, types []string, beforeMs int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := ledgerTypeSet(types)
	// As the SQL store: whole weeks from the rollup, the open week from rows.
	openWeek := weekStartOf(beforeMs)
	n := 0
	for w, byType := range m.weeks[crewID] {
		if w >= openWeek {
			continue
		}
		for typ, c := range byType {
			if set == nil || set[typ] {
				n += c
			}
		}
	}
	for _, r := range m.rows[crewID] {
		if r.ev.Timestamp >= openWeek && r.ev.Timestamp < beforeMs && (set == nil || set[r.ev.Type]) {
			n++
		}
	}
	return n, nil
}

func (m *memLedgerStore) WeeksBefore(_ context.Context, crewID string, types []string, beforeWeek int64, limit int) ([]weekCount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := ledgerTypeSet(types)
	var out []weekCount
	for w, byType := range m.weeks[crewID] {
		if w >= beforeWeek {
			continue
		}
		n := 0
		for typ, c := range byType {
			if set == nil || set[typ] {
				n += c
			}
		}
		if n > 0 {
			out = append(out, weekCount{WeekStart: w, N: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WeekStart > out[j].WeekStart })
	if limit < 0 {
		limit = 0
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memLedgerStore) RecentView(_ context.Context, crewID string) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []CrewEvent{}
	for _, r := range m.viewRows(crewID) {
		ev := r.event()
		ev.Type, _ = ledgerReaderType(ev.Type)
		out = append(out, ev)
	}
	return out, nil
}

func (m *memLedgerStore) ByType(_ context.Context, typ string, sinceMs int64) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	crews := make([]string, 0, len(m.rows))
	for crewID := range m.rows {
		crews = append(crews, crewID)
	}
	sort.Strings(crews)
	out := []CrewEvent{}
	for _, crewID := range crews {
		rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
			return r.ev.Type == typ && r.ev.Timestamp >= sinceMs
		}, memByTsAsc)
		out = append(out, memLedgerEvents(rows)...)
	}
	return out, nil
}

func (m *memLedgerStore) DeleteCrew(_ context.Context, crewID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, crewID)
	delete(m.anchors, crewID)
	delete(m.weeks, crewID)
	delete(m.deleted, crewID)
	return nil
}

func (m *memLedgerStore) EventsNamingUser(_ context.Context, userID string) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	crews := make([]string, 0, len(m.rows))
	for crewID := range m.rows {
		crews = append(crews, crewID)
	}
	sort.Strings(crews)
	out := []CrewEvent{}
	for _, crewID := range crews {
		rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
			for _, u := range r.users {
				if u == userID {
					return true
				}
			}
			return false
		}, memByTsAsc)
		out = append(out, memLedgerEvents(rows)...)
	}
	return out, nil
}

func (m *memLedgerStore) Put(_ context.Context, crewID string, rows []ledgerRow) (int, error) {
	nowMs := ledgerNow().UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	inserted := 0
	for _, row := range rows {
		ev := row.Event
		if ev.ID == "" {
			return inserted, errors.New("ledger put needs an event id")
		}
		if m.deleted[crewID][ev.ID] {
			continue
		}
		ev.CrewID = crewID
		ok, err := m.insert(ev, row.Ref, nowMs, 0)
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
		}
	}
	return inserted, nil
}

func (m *memLedgerStore) byRef(crewID, typ, ref string) []*memLedgerRow {
	if ref == "" {
		return nil
	}
	return m.selectRows(crewID, func(r *memLedgerRow) bool { return r.ev.Type == typ && r.ref == ref }, memByTsAsc)
}

func (m *memLedgerStore) PutUnique(_ context.Context, crewID string, row ledgerRow) ([]CrewEvent, error) {
	ev := row.Event
	if ev.ID == "" || row.Ref == "" {
		return nil, errors.New("ledger put needs an event id and a ref")
	}
	nowMs := ledgerNow().UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	if held := m.byRef(crewID, ev.Type, row.Ref); len(held) > 0 {
		return memLedgerEvents(held), nil
	}
	ev.CrewID = crewID
	ok, err := m.insert(ev, row.Ref, nowMs, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errLedgerIDTaken
	}
	delete(m.deleted[crewID], ev.ID)
	return nil, nil
}

func (m *memLedgerStore) ByRef(_ context.Context, crewID, typ, ref string) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return memLedgerEvents(m.byRef(crewID, typ, ref)), nil
}

func (m *memLedgerStore) ByIDs(_ context.Context, crewID string, ids []string) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	return memLedgerEvents(m.selectRows(crewID, func(r *memLedgerRow) bool { return want[r.ev.ID] }, memByTsAsc)), nil
}

func (m *memLedgerStore) ByRefs(_ context.Context, crewID, typ string, refs []string) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref != "" {
			want[ref] = true
		}
	}
	return memLedgerEvents(m.selectRows(crewID, func(r *memLedgerRow) bool { return r.ev.Type == typ && want[r.ref] }, memByTsAsc)), nil
}

func (m *memLedgerStore) RefsBefore(_ context.Context, crewID, typ, beforeRef string, n int) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
		return r.ev.Type == typ && r.ref != "" && (beforeRef == "" || r.ref < beforeRef)
	}, func(a, b *memLedgerRow) bool {
		if a.ref != b.ref {
			return a.ref > b.ref
		}
		return memByTsAsc(b, a)
	})
	if n < 0 {
		n = 0
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	return memLedgerEvents(rows), nil
}

func (m *memLedgerStore) TopInRange(_ context.Context, crewID, typ string, fromMs, toMs int64, field, value string, n int) ([]CrewEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	first := func(r *memLedgerRow) bool {
		var obj map[string]interface{}
		if err := json.Unmarshal(r.data, &obj); err != nil {
			return false
		}
		text, ok := obj[field].(string)
		return ok && text == value
	}
	rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
		return r.ev.Type == typ && r.ev.Timestamp >= fromMs && r.ev.Timestamp <= toMs
	}, func(a, b *memLedgerRow) bool {
		if fa, fb := first(a), first(b); fa != fb {
			return fa
		}
		if a.ev.Timestamp != b.ev.Timestamp {
			return a.ev.Timestamp > b.ev.Timestamp
		}
		return a.ev.ID < b.ev.ID
	})
	if n < 0 {
		n = 0
	}
	if len(rows) > n {
		rows = rows[:n]
	}
	return memLedgerEvents(rows), nil
}

func ledgerSameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
