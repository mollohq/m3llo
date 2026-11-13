package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
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
	// Append stores one event at the time of ledgerNow. It reports false when
	// the crew already holds an event with this id. It moves the anchor of
	// the 7-day view in both cases, as the legacy trim ran on every append.
	Append(ctx context.Context, ev CrewEvent, ref string) (inserted bool, err error)
	// Backfill stores the events of a legacy document in their order and
	// skips the ones the crew already holds. anchorMs is the time of the last
	// append of the document. The anchor never moves back.
	Backfill(ctx context.Context, crewID string, events []CrewEvent, anchorMs int64) (inserted int, err error)
	// UpdateData replaces the data of one event with the result of fn.
	UpdateData(ctx context.Context, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error
	// Delete removes one event. An unknown event is not an error.
	Delete(ctx context.Context, crewID, eventID string) error
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
	// RecentView returns what the legacy document held: the pulse events of
	// the 7 days before the last append of the crew, in append order.
	RecentView(ctx context.Context, crewID string) ([]CrewEvent, error)
	// ByType returns the events of one type with ts >= sinceMs in every crew,
	// ordered by crew and time.
	ByType(ctx context.Context, typ string, sinceMs int64) ([]CrewEvent, error)
	// DeleteCrew removes every event of a crew.
	DeleteCrew(ctx context.Context, crewID string) error
	// EventsNamingUser returns every event that names the user.
	EventsNamingUser(ctx context.Context, userID string) ([]CrewEvent, error)
}

// crewLedger is the durable store. InitModule sets it. It is nil when the
// schema could not be made, and in tests that do not set it: the module then
// uses only the legacy document.
var crewLedger ledgerStore

// eventUserIDs lists every user an event names: the actor, the participants,
// players and viewers, the streamer, the inviter and the clip participants.
// The actor comes first. No id repeats.
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
	users     []string
}

type memLedgerStore struct {
	mu      sync.Mutex
	rows    map[string][]*memLedgerRow          // crew id -> rows
	anchors map[string]int64                    // crew id -> time of the last append
	weeks   map[string]map[int64]map[string]int // crew id -> week start -> type -> count
}

func newMemLedgerStore() *memLedgerStore {
	return &memLedgerStore{
		rows:    map[string][]*memLedgerRow{},
		anchors: map[string]int64{},
		weeks:   map[string]map[int64]map[string]int{},
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

func (m *memLedgerStore) insert(ev CrewEvent, ref string, createdMs int64) (bool, error) {
	if m.find(ev.CrewID, ev.ID) >= 0 {
		return false, nil
	}
	data, err := ledgerEncodeData(ev.Data)
	if err != nil {
		return false, err
	}
	users := eventUserIDs(ev)
	ev.Data = nil
	m.rows[ev.CrewID] = append(m.rows[ev.CrewID], &memLedgerRow{ev: ev, data: data, ref: ref, createdMs: createdMs, users: users})
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
	inserted, err := m.insert(ev, ref, nowMs)
	if err != nil {
		return false, err
	}
	if nowMs > m.anchors[ev.CrewID] {
		m.anchors[ev.CrewID] = nowMs
	}
	return inserted, nil
}

func (m *memLedgerStore) Backfill(_ context.Context, crewID string, events []CrewEvent, anchorMs int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The backfilled events are older than every event the store already
	// holds, so they sort before them and keep their own order.
	base := anchorMs
	for _, r := range m.rows[crewID] {
		if r.createdMs < base {
			base = r.createdMs
		}
	}
	base -= int64(len(events))
	inserted := 0
	for i, ev := range events {
		if ev.ID == "" {
			continue
		}
		ev.CrewID = crewID
		ok, err := m.insert(ev, eventRef(ev), base+int64(i))
		if err != nil {
			return inserted, err
		}
		if ok {
			inserted++
		}
	}
	if anchorMs > m.anchors[crewID] {
		m.anchors[crewID] = anchorMs
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

func (m *memLedgerStore) Delete(_ context.Context, crewID, eventID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.find(crewID, eventID)
	if i < 0 {
		return nil
	}
	rows := m.rows[crewID]
	m.bumpWeek(crewID, rows[i].ev, -1)
	m.rows[crewID] = append(rows[:i:i], rows[i+1:]...)
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
	anchor, ok := m.anchors[crewID]
	if !ok {
		return []CrewEvent{}, nil
	}
	cutoff := anchor - ledgerRecentWindowMs
	set := ledgerTypeSet(ledgerPulseTypes)
	rows := m.selectRows(crewID, func(r *memLedgerRow) bool {
		return r.ev.Timestamp >= cutoff && set[r.ev.Type]
	}, func(a, b *memLedgerRow) bool {
		if a.createdMs != b.createdMs {
			return a.createdMs < b.createdMs
		}
		return a.ev.ID < b.ev.ID
	})
	return memLedgerEvents(rows), nil
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
