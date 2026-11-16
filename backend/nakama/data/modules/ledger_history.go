package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Phase 1b of the durable crew ledger (plans/DURABLE-LEDGER.md section 6a):
// the clips, the stream sessions and the weekly recaps of a crew as rows of
// the durable store.
//
// Each of the three has a legacy document for each crew. The document is the
// truth until the marker of phase 1b (historyTruthStore). The reconcile copies
// the documents to the store and compares them.
//
// The rule for the caps. A legacy document is capped (250 clips, 150 stream
// sessions) and the store is not, so the store holds more than the document.
// No RPC deletes a clip, a stream session of the durable copy or a recap. An
// entry leaves a document only by the cap. So the reconcile of phase 1b never
// deletes a row: a row that the document does not hold is history that the
// document trimmed. The parity it checks is that every entry of the document
// has its row, with the same content.

const (
	ledgerTypeClip          = "clip"
	ledgerTypeStreamSession = "stream_session"
	ledgerTypeWeeklyRecap   = "weekly_recap"
)

// historyKind is one kind of legacy document of phase 1b.
type historyKind struct {
	name       string
	collection string
	// plan reads the legacy document of a crew and returns its entries as
	// the rows the store keeps, in document order. noID counts the entries
	// that have no id. A read or decode error is returned: it is not an empty
	// document.
	plan func(ctx context.Context, nk runtime.NakamaModule, crewID string) (rows []ledgerRow, noID int, err error)
	// same reports whether a stored row holds what the document entry holds.
	same func(doc, stored CrewEvent) bool
	// merge returns the data a stored row gets when the document wins.
	merge func(doc CrewEvent, stored json.RawMessage) (json.RawMessage, error)
	// uniqueRef says the store holds one row for a ref of this kind (the
	// clip_id of a clip). The reconcile then does not store an entry whose
	// ref a row outside the document holds.
	uniqueRef bool
}

// historyKinds are the documents the reconcile visits after the ledger
// documents.
var historyKinds = []*historyKind{streamSessionHistoryKind, clipHistoryKind, recapHistoryKind}

// historyPassStats are the totals of phase 1b of one pass.
type historyPassStats struct {
	Docs, Inserted, Updated, NoID, RefHeld int
	// Unclean counts the documents with an entry the store does not hold as
	// the document does. Failed counts the documents with a read or write
	// error.
	Unclean, Failed int
}

func (h historyPassStats) clean() bool {
	return h.Unclean == 0 && h.Failed == 0
}

// historyCrewResult is the result of the reconcile of one document.
type historyCrewResult struct {
	Inserted int
	Updated  int
	NoID     int
	// RefHeld counts the entries that are not stored because a row outside
	// the document holds their ref (putMissingHistoryRows).
	RefHeld int
	Clean   bool
	Err     error
}

// historySameJSON compares two values as JSON.
func historySameJSON(a, b interface{}) bool {
	rawA, errA := json.Marshal(a)
	rawB, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(rawA) == string(rawB)
}

// reconcileHistoryCrew brings the store in line with one legacy document of
// phase 1b.
//
// Before the cutover (docWins) the document is the truth: the store gets the
// rows it lacks, and a row with other content takes the content of the
// document. After the cutover the store only gets the rows it lacks. These
// come from an instance of an older release or from a rollback period.
//
// No row is deleted in either phase (the rule for the caps, above). A row
// that Delete removed is not stored again: a stream session of a stream that
// was resumed.
//
// Each round reads the document again, so a write to the crew during a round
// shows in the next round.
func reconcileHistoryCrew(ctx context.Context, nk runtime.NakamaModule, store ledgerStore, kind *historyKind, crewID string, docWins bool) historyCrewResult {
	var res historyCrewResult
	for round := 0; ; round++ {
		rows, noID, err := kind.plan(ctx, nk, crewID)
		if err != nil {
			res.Err = fmt.Errorf("document read: %w", err)
			return res
		}
		res.NoID = noID
		// An id counts once. The first entry of the document wins.
		seen := make(map[string]bool, len(rows))
		ids := make([]string, 0, len(rows))
		unique := rows[:0:0]
		for _, row := range rows {
			if seen[row.Event.ID] {
				continue
			}
			seen[row.Event.ID] = true
			ids = append(ids, row.Event.ID)
			unique = append(unique, row)
		}
		stored, err := store.ByIDs(ctx, crewID, ids)
		if err != nil {
			res.Err = fmt.Errorf("store read: %w", err)
			return res
		}
		byID := make(map[string]CrewEvent, len(stored))
		for _, ev := range stored {
			byID[ev.ID] = ev
		}
		var missing, differ []ledgerRow
		for _, row := range unique {
			got, ok := byID[row.Event.ID]
			switch {
			case !ok:
				missing = append(missing, row)
			case !kind.same(row.Event, got):
				differ = append(differ, row)
			}
		}
		if !docWins {
			differ = nil
		}
		if len(missing) == 0 && len(differ) == 0 {
			res.Clean = true
			return res
		}
		if round >= ledgerReconcileRounds {
			return res
		}

		inserted, refHeld, err := putMissingHistoryRows(ctx, store, kind, crewID, missing, seen)
		if err != nil {
			res.Err = fmt.Errorf("store put: %w", err)
			return res
		}
		res.Inserted += inserted
		res.RefHeld = refHeld
		for _, row := range differ {
			doc := row.Event
			err := store.UpdateData(ctx, crewID, doc.ID, func(raw json.RawMessage) (json.RawMessage, error) {
				return kind.merge(doc, raw)
			})
			if err != nil {
				res.Err = fmt.Errorf("store data update: %w", err)
				return res
			}
			res.Updated++
		}
		if inserted == 0 && len(differ) == 0 {
			// What is left was removed from the store on purpose
			// (ledgerStore.Delete) and stays out.
			res.Clean = true
			return res
		}
	}
}

// putMissingHistoryRows stores the entries of a document that the store
// lacks. docIDs are the row ids of every entry of the document.
//
// A kind with uniqueRef keeps one row for a ref. An entry whose ref a row
// outside the document holds is not stored: refHeld counts it. The row of the
// store came first (a node after the cutover wrote it, or the capped document
// dropped it), and a second row would list the clip twice. A document from
// before clip_id was checked can hold one clip_id twice. Those entries are
// rows of the document, and both are stored.
func putMissingHistoryRows(ctx context.Context, store ledgerStore, kind *historyKind, crewID string, missing []ledgerRow, docIDs map[string]bool) (inserted, refHeld int, err error) {
	if !kind.uniqueRef {
		inserted, err = store.Put(ctx, crewID, missing)
		return inserted, 0, err
	}
	perRef := map[string]int{}
	for _, row := range missing {
		perRef[row.Ref]++
	}
	var plain []ledgerRow
	for _, row := range missing {
		if row.Ref == "" {
			plain = append(plain, row)
			continue
		}
		if perRef[row.Ref] == 1 {
			// One transaction checks the ref and stores the row.
			held, err := store.PutUnique(ctx, crewID, row)
			switch {
			case errors.Is(err, errLedgerIDTaken):
				// A writer stored this row since the read. The next round
				// sees it.
				continue
			case err != nil:
				return inserted, refHeld, err
			case len(held) == 0:
				inserted++
				continue
			}
			if historyRefHeldOutside(held, docIDs) {
				refHeld++
				continue
			}
			// The rows that hold the ref are entries of the document: the
			// same clip_id twice. This entry is one more of them.
			plain = append(plain, row)
			continue
		}
		held, err := store.ByRef(ctx, crewID, row.Event.Type, row.Ref)
		if err != nil {
			return inserted, refHeld, err
		}
		if historyRefHeldOutside(held, docIDs) {
			refHeld++
			continue
		}
		plain = append(plain, row)
	}
	n, err := store.Put(ctx, crewID, plain)
	return inserted + n, refHeld, err
}

// historyRefHeldOutside reports whether one of the rows is not an entry of
// the document.
func historyRefHeldOutside(held []CrewEvent, docIDs map[string]bool) bool {
	for _, ev := range held {
		if !docIDs[ev.ID] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Stream sessions
// ---------------------------------------------------------------------------
//
// A stream that ends writes a stream_session event to the ledger and a copy to
// the document crew_stream_sessions. Both describe the same session, and the
// copy holds the id of the event. The store keeps one row for the session:
// the row of the event (phase 1a), with the id of the event and the session
// id as its ref.
//
//   - The ledger document decides about a session while it holds the event.
//     The reconcile of phase 1a stores and repairs that row.
//   - A session that the ledger document does not hold (it is older than 7
//     days, or its event was never written) comes from the copy. Put stores
//     it under the id of the event, so it meets the row of the event when
//     there is one. It is not in the 7-day view.
//   - A stream that was resumed has its event removed (removeCrewEvent). The
//     store remembers the id, and the copy does not bring the row back.
//
// stream_session is a pulse type, so no "ledger_" row exists for a stream.

// streamSessionRow is a stream session of the durable copy as a row.
func streamSessionRow(s StoredStreamSession) ledgerRow {
	return ledgerRow{
		Ref: s.SessionID,
		Event: CrewEvent{
			ID:        s.EventID,
			Type:      ledgerTypeStreamSession,
			ActorID:   s.StreamerID,
			Timestamp: s.Ts,
			Score:     s.Score,
			Data: StreamSessionData{
				SessionID:    s.SessionID,
				StreamerID:   s.StreamerID,
				StreamerName: s.StreamerName,
				Title:        s.Title,
				Game:         s.Game,
				DurationMin:  s.DurationMin,
				PeakViewers:  s.PeakViewers,
				SnapshotURLs: s.SnapshotURLs,
			},
		},
	}
}

// streamSessionFromRow is the stream session the feed shows for a row.
func streamSessionFromRow(ev CrewEvent) (StoredStreamSession, bool) {
	raw, err := json.Marshal(ev.Data)
	if err != nil {
		return StoredStreamSession{}, false
	}
	var d StreamSessionData
	if err := json.Unmarshal(raw, &d); err != nil {
		return StoredStreamSession{}, false
	}
	return storedStreamSessionFrom(ev.ID, ev.Timestamp, ev.Score, d), true
}

// streamSessionsFromRows turns stream_session rows, oldest first, into the
// list the document held. The document held one entry for a session id: the
// one written last. So of the rows with one session id the newest stays.
func streamSessionsFromRows(rows []CrewEvent) []StoredStreamSession {
	last := map[string]int{}
	for i, ev := range rows {
		if ref := eventRef(ev); ref != "" {
			last[ref] = i
		}
	}
	out := make([]StoredStreamSession, 0, len(rows))
	for i, ev := range rows {
		if ref := eventRef(ev); ref != "" && last[ref] != i {
			continue
		}
		if s, ok := streamSessionFromRow(ev); ok {
			out = append(out, s)
		}
	}
	return out
}

var streamSessionHistoryKind = &historyKind{
	name:       "stream_sessions",
	collection: CrewStreamSessionsCollection,
	plan: func(ctx context.Context, nk runtime.NakamaModule, crewID string) ([]ledgerRow, int, error) {
		var doc CrewStreamSessionsDoc
		if _, _, err := readDocStrict(ctx, nk, CrewStreamSessionsCollection, crewID, &doc); err != nil {
			return nil, 0, err
		}
		ledger, err := readLedgerDocStrict(ctx, nk, crewID)
		if err != nil {
			return nil, 0, err
		}
		inLedger := make(map[string]bool, len(ledger.Events))
		for _, ev := range ledger.Events {
			inLedger[ev.ID] = true
		}
		rows := make([]ledgerRow, 0, len(doc.Sessions))
		noID := 0
		for _, s := range doc.Sessions {
			switch {
			case s.EventID == "":
				noID++
			case inLedger[s.EventID]:
				// The ledger document decides about this session.
			default:
				rows = append(rows, streamSessionRow(s))
			}
		}
		return rows, noID, nil
	},
	same: func(doc, stored CrewEvent) bool {
		if stored.Type != ledgerTypeStreamSession || stored.Timestamp != doc.Timestamp || stored.Score != doc.Score {
			return false
		}
		a, okA := streamSessionFromRow(doc)
		b, okB := streamSessionFromRow(stored)
		return okA && okB && historySameJSON(a, b)
	},
	// The row of an event holds more than the copy: the viewers. Only what
	// the copy holds is taken from it.
	merge: func(doc CrewEvent, stored json.RawMessage) (json.RawMessage, error) {
		from, ok := doc.Data.(StreamSessionData)
		if !ok {
			return nil, fmt.Errorf("stream session %s: unexpected data %T", doc.ID, doc.Data)
		}
		var d StreamSessionData
		if err := json.Unmarshal(stored, &d); err != nil {
			return nil, err
		}
		viewers := d.ViewerIDs
		d = from
		d.ViewerIDs = viewers
		return json.Marshal(d)
	},
}

// ---------------------------------------------------------------------------
// Clips
// ---------------------------------------------------------------------------
//
// A clip is one row of type clip. Its id is "clip_" and the event id of the
// clip, its ref the clip_id, and its data the stored clip. The prefix keeps
// the id apart from the ids of ledger events: one crew holds an id once.
//
// A legacy ledger document can also hold clip events (the dev seed writes
// them, old crews have them). The store keeps those as "ledger_clip" rows,
// for the 7-day view of the public surfaces only. They are not clips of the
// crew, and no counted query sees them.

// clipRowID is the row id of the clip with an event id.
func clipRowID(eventID string) string {
	return "clip_" + eventID
}

// clipRow is a stored clip as a row.
func clipRow(c StoredClip) ledgerRow {
	return ledgerRow{
		Ref: c.ClipID,
		Event: CrewEvent{
			ID:        clipRowID(c.EventID),
			Type:      ledgerTypeClip,
			ActorID:   c.ActorID,
			Timestamp: c.Ts,
			Score:     c.Score,
			Data:      c,
		},
	}
}

// clipFromRow is the stored clip of a row.
func clipFromRow(ev CrewEvent) (StoredClip, bool) {
	raw, err := json.Marshal(ev.Data)
	if err != nil {
		return StoredClip{}, false
	}
	var c StoredClip
	if err := json.Unmarshal(raw, &c); err != nil {
		return StoredClip{}, false
	}
	return c, true
}

func clipsFromRows(rows []CrewEvent) []StoredClip {
	out := make([]StoredClip, 0, len(rows))
	for _, ev := range rows {
		if c, ok := clipFromRow(ev); ok {
			out = append(out, c)
		}
	}
	return out
}

var clipHistoryKind = &historyKind{
	name:       "clips",
	collection: CrewClipsCollection,
	plan: func(ctx context.Context, nk runtime.NakamaModule, crewID string) ([]ledgerRow, int, error) {
		var doc CrewClipsDoc
		if _, _, err := readDocStrict(ctx, nk, CrewClipsCollection, crewID, &doc); err != nil {
			return nil, 0, err
		}
		rows := make([]ledgerRow, 0, len(doc.Clips))
		noID := 0
		for _, c := range doc.Clips {
			if c.EventID == "" {
				noID++
				continue
			}
			rows = append(rows, clipRow(c))
		}
		return rows, noID, nil
	},
	same: func(doc, stored CrewEvent) bool {
		c, ok := clipFromRow(stored)
		return ok && stored.Type == ledgerTypeClip && historySameJSON(doc.Data, c)
	},
	merge: func(doc CrewEvent, _ json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(doc.Data)
	},
	uniqueRef: true,
}

// crewClipsBetween returns the clips of a crew with fromMs <= ts <= toMs,
// oldest first: the rows of the store after the cutover of phase 1b, the
// document crew_clips before it. A store read that fails falls back to the
// document, which is still written.
func crewClipsBetween(ctx context.Context, nk runtime.NakamaModule, crewID string, fromMs, toMs int64) []StoredClip {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.Between(ctx, crewID, fromMs, toMs, []string{ledgerTypeClip})
		if err == nil {
			return clipsFromRows(rows)
		}
		ledgerLogError("ledger_store_read_failed kind=clips crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readClipsDoc(ctx, nk, crewID)
	out := make([]StoredClip, 0, len(doc.Clips))
	for _, c := range doc.Clips {
		if c.Ts >= fromMs && c.Ts <= toMs {
			out = append(out, c)
		}
	}
	return out
}

// crewClipsWithID returns the clips of a crew that have one clip_id.
func crewClipsWithID(ctx context.Context, nk runtime.NakamaModule, crewID, clipID string) []StoredClip {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.ByRef(ctx, crewID, ledgerTypeClip, clipID)
		if err == nil {
			return clipsFromRows(rows)
		}
		ledgerLogError("ledger_store_read_failed kind=clips crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readClipsDoc(ctx, nk, crewID)
	out := []StoredClip{}
	for _, c := range doc.Clips {
		if c.ClipID == clipID {
			out = append(out, c)
		}
	}
	return out
}

// crewClipsForGuestFeed returns the clips the guest feed projects, oldest
// first, and the number of clips of the crew that are not in the list. The
// list holds what the document held: at most the newest CrewClipsMaxRetained
// clips. more is 0 before the cutover and for a crew with fewer clips.
func crewClipsForGuestFeed(ctx context.Context, nk runtime.NakamaModule, crewID string) (clips []StoredClip, more int) {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.NewestN(ctx, crewID, []string{ledgerTypeClip}, math.MaxInt64, CrewClipsMaxRetained)
		total := 0
		if err == nil {
			total, err = store.CountOlder(ctx, crewID, []string{ledgerTypeClip}, math.MaxInt64)
		}
		if err == nil {
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
			if total > len(rows) {
				more = total - len(rows)
			}
			return clipsFromRows(rows), more
		}
		ledgerLogError("ledger_store_read_failed kind=clips crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readClipsDoc(ctx, nk, crewID)
	return doc.Clips, 0
}

// updateStoredClip changes one clip of a crew in both stores, in the order of
// the phase (ledgerWriteOrdered). find picks the clip from the clips of the
// crew. set changes it.
func updateStoredClip(ctx context.Context, nk runtime.NakamaModule, crewID, clipID string, find func([]StoredClip) *StoredClip, set func(*StoredClip)) error {
	truth := historyTruthStore(ctx, nk)
	eventID := ""
	return ledgerWriteOrdered(truth, "clip_update", crewID, clipID,
		func(store ledgerStore) error {
			if truth != nil {
				rows, err := store.ByRef(ctx, crewID, ledgerTypeClip, clipID)
				if err != nil {
					return err
				}
				clip := find(clipsFromRows(rows))
				if clip == nil {
					return fmt.Errorf("clip %s not found in crew %s", clipID, crewID)
				}
				eventID = clip.EventID
			}
			if eventID == "" {
				return errLedgerEventNotFound
			}
			return store.UpdateData(ctx, crewID, clipRowID(eventID), func(raw json.RawMessage) (json.RawMessage, error) {
				var c StoredClip
				if err := json.Unmarshal(raw, &c); err != nil {
					return nil, err
				}
				set(&c)
				return json.Marshal(c)
			})
		},
		func() error {
			id, err := updateClipDoc(ctx, nk, crewID, clipID, find, set)
			if truth == nil {
				eventID = id
			}
			return err
		})
}

// updateClipDoc changes one clip of the legacy clips document, with the
// version retry of the other document writers. It returns the event id of
// the clip.
func updateClipDoc(ctx context.Context, nk runtime.NakamaModule, crewID, clipID string, find func([]StoredClip) *StoredClip, set func(*StoredClip)) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		doc, version := readClipsDoc(ctx, nk, crewID)
		clip := find(doc.Clips)
		if clip == nil {
			return "", fmt.Errorf("clip %s not found in crew %s", clipID, crewID)
		}
		set(clip)
		doc.UpdatedAt = time.Now().UnixMilli()
		if err := writeClipsDoc(ctx, nk, crewID, doc, version); err == nil {
			return clip.EventID, nil
		}
		jitter, _ := rand.Int(rand.Reader, big.NewInt(50))
		time.Sleep(time.Duration(50*(attempt+1)+int(jitter.Int64())) * time.Millisecond)
	}
	return "", fmt.Errorf("crew_clips write failed after 3 retries for crew %s", crewID)
}

// ---------------------------------------------------------------------------
// Weekly recaps
// ---------------------------------------------------------------------------
//
// A weekly recap is one row of type weekly_recap. Its id is recap_{week_start},
// the id the feed gives the recap, so a crew holds one recap for a week start.
// Its time is the week start: Crew history files a recap under the week it
// is about, and the newest recap is the one of the newest week.
//
// A legacy ledger document can also hold weekly_recap events (the dev seed
// writes them). The store keeps those as "ledger_weekly_recap" rows, for the
// invite preview only.

// recapRowScore is the score the merged timeline gives a recap.
const recapRowScore = 30

// recapRow is a weekly recap as a row.
func recapRow(r WeeklyRecapData) ledgerRow {
	return ledgerRow{Event: CrewEvent{
		ID:        recapEntryID(r.WeekStart),
		Type:      ledgerTypeWeeklyRecap,
		Timestamp: r.WeekStart,
		Score:     recapRowScore,
		Data:      r,
	}}
}

// recapFromRow is the weekly recap of a row.
func recapFromRow(ev CrewEvent) (WeeklyRecapData, bool) {
	raw, err := json.Marshal(ev.Data)
	if err != nil {
		return WeeklyRecapData{}, false
	}
	var r WeeklyRecapData
	if err := json.Unmarshal(raw, &r); err != nil {
		return WeeklyRecapData{}, false
	}
	return r, true
}

func recapsFromRows(rows []CrewEvent) []WeeklyRecapData {
	out := make([]WeeklyRecapData, 0, len(rows))
	for _, ev := range rows {
		if r, ok := recapFromRow(ev); ok {
			out = append(out, r)
		}
	}
	return out
}

var recapHistoryKind = &historyKind{
	name:       "recaps",
	collection: CrewRecapsCollection,
	plan: func(ctx context.Context, nk runtime.NakamaModule, crewID string) ([]ledgerRow, int, error) {
		var doc CrewRecapsDoc
		if _, _, err := readDocStrict(ctx, nk, CrewRecapsCollection, crewID, &doc); err != nil {
			return nil, 0, err
		}
		rows := make([]ledgerRow, 0, len(doc.Recaps))
		for _, r := range doc.Recaps {
			rows = append(rows, recapRow(r))
		}
		return rows, 0, nil
	},
	same: func(doc, stored CrewEvent) bool {
		r, ok := recapFromRow(stored)
		return ok && stored.Type == ledgerTypeWeeklyRecap && historySameJSON(doc.Data, r)
	},
	merge: func(doc CrewEvent, _ json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(doc.Data)
	},
}

// crewLatestRecap returns the recap of the newest week of a crew, nil when
// the crew has none.
func crewLatestRecap(ctx context.Context, nk runtime.NakamaModule, crewID string) *WeeklyRecapData {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.NewestN(ctx, crewID, []string{ledgerTypeWeeklyRecap}, math.MaxInt64, 1)
		if err == nil {
			if recaps := recapsFromRows(rows); len(recaps) > 0 {
				return &recaps[0]
			}
			return nil
		}
		ledgerLogError("ledger_store_read_failed kind=recaps crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readRecapsDoc(ctx, nk, crewID)
	if len(doc.Recaps) == 0 {
		return nil
	}
	latest := doc.Recaps[0]
	for _, r := range doc.Recaps[1:] {
		if r.WeekStart > latest.WeekStart {
			latest = r
		}
	}
	return &latest
}

// crewRecapState returns what the recap job reads to decide if a crew is due
// (recapDue): the week of the last recap. Before the cutover of phase 1b it
// is the recaps document. After it, it is made from the recap of the newest
// week in the store.
func crewRecapState(ctx context.Context, nk runtime.NakamaModule, crewID string) *CrewRecapsDoc {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.NewestN(ctx, crewID, []string{ledgerTypeWeeklyRecap}, math.MaxInt64, 1)
		if err == nil {
			state := &CrewRecapsDoc{CrewID: crewID, Recaps: recapsFromRows(rows)}
			if len(state.Recaps) > 0 {
				state.LastRecapWeek = isoWeekKey(time.UnixMilli(state.Recaps[0].WeekEnd))
			}
			return state
		}
		ledgerLogError("ledger_store_read_failed kind=recaps crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readRecapsDoc(ctx, nk, crewID)
	return doc
}

// ---------------------------------------------------------------------------
// The crew feed
// ---------------------------------------------------------------------------

// zoneHistoryData is what the zone feed reads of the clips, the stream
// sessions and the weekly recaps of a crew.
type zoneHistoryData struct {
	Clips   []StoredClip
	Streams []StoredStreamSession
	Recaps  []WeeklyRecapData
	// LastClipMs and LastStreamMs are the times of the newest clip and the
	// newest stream session of the crew. 0 when the lists hold every one.
	LastClipMs   int64
	LastStreamMs int64
}

// zoneHistoryTypes are the row types of Crew history.
var zoneHistoryTypes = []string{ledgerTypeClip, ledgerTypeStreamSession, ledgerTypeWeeklyRecap}

// loadZoneHistory reads what one page of the zone feed shows. windowStart is
// the start of the free window, historyBefore the page of Crew history (0
// for the first page).
//
// Before the cutover of phase 1b it reads the three legacy documents whole.
// After it, it reads the rows of the page from the store. A store read that
// fails falls back to the documents, which are still written.
func loadZoneHistory(ctx context.Context, nk runtime.NakamaModule, crewID string, windowStart, historyBefore int64) zoneHistoryData {
	if store := historyTruthStore(ctx, nk); store != nil {
		data, err := loadZoneHistoryFromStore(ctx, store, crewID, windowStart, historyBefore)
		if err == nil {
			return data
		}
		ledgerLogError("ledger_store_read_failed kind=feed crew=%s: %v. Reading the legacy documents", crewID, err)
	}
	clips, _ := readClipsDoc(ctx, nk, crewID)
	streams, _ := readStreamSessionsDoc(ctx, nk, crewID)
	recaps, _ := readRecapsDoc(ctx, nk, crewID)
	return zoneHistoryData{Clips: clips.Clips, Streams: streams.Sessions, Recaps: recaps.Recaps}
}

// dropSupersededStreamRows drops the stream_session rows whose session id a
// newer row of the crew holds. The document held one entry for a session id,
// the one written last (streamSessionsFromRows). The newer row can be in
// another week or in the free window, so the store is asked.
func dropSupersededStreamRows(ctx context.Context, store ledgerStore, crewID string, rows []CrewEvent) ([]CrewEvent, error) {
	refs := make([]string, 0, len(rows))
	for _, ev := range rows {
		if ref := eventRef(ev); ref != "" {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return rows, nil
	}
	all, err := store.ByRefs(ctx, crewID, ledgerTypeStreamSession, refs)
	if err != nil {
		return nil, err
	}
	newest := map[string]CrewEvent{}
	for _, ev := range all {
		ref := eventRef(ev)
		if cur, ok := newest[ref]; !ok || ev.Timestamp > cur.Timestamp || (ev.Timestamp == cur.Timestamp && ev.ID > cur.ID) {
			newest[ref] = ev
		}
	}
	out := rows[:0:0]
	for _, ev := range rows {
		ref := eventRef(ev)
		if n, ok := newest[ref]; ref != "" && ok && n.ID != ev.ID {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// zoneBoardRecapBatch is the number of recaps one read for the board holds.
const zoneBoardRecapBatch = 4

// loadZoneHistoryFromStore reads from the store only what one page of the
// zone feed shows:
//
//   - the clips and stream sessions of the free window;
//   - the newest recap that is not empty, for the board;
//   - the time of the newest clip and of the newest stream session;
//   - the weeks of one page of Crew history, and one week more, so that
//     zoneHistory knows if a next page exists.
//
// The weeks come from the week rollup (WeeksBefore). A week of the rollup
// that holds nothing for Crew history is skipped: its rows are in the free
// window, or its recap is empty or is the recap of the board.
func loadZoneHistoryFromStore(ctx context.Context, store ledgerStore, crewID string, windowStart, historyBefore int64) (zoneHistoryData, error) {
	var data zoneHistoryData
	var streamRows []CrewEvent

	// The free window.
	window, err := store.Between(ctx, crewID, windowStart, math.MaxInt64, []string{ledgerTypeClip, ledgerTypeStreamSession})
	if err != nil {
		return data, err
	}
	for _, ev := range window {
		switch ev.Type {
		case ledgerTypeClip:
			if c, ok := clipFromRow(ev); ok {
				data.Clips = append(data.Clips, c)
			}
		case ledgerTypeStreamSession:
			streamRows = append(streamRows, ev)
		}
	}

	// The newest clip and stream session. They are in the window when the
	// window holds one.
	newest := func(typ string, have bool) (int64, error) {
		if have {
			return 0, nil
		}
		rows, err := store.NewestN(ctx, crewID, []string{typ}, math.MaxInt64, 1)
		if err != nil || len(rows) == 0 {
			return 0, err
		}
		return rows[0].Timestamp, nil
	}
	if data.LastClipMs, err = newest(ledgerTypeClip, len(data.Clips) > 0); err != nil {
		return data, err
	}
	if data.LastStreamMs, err = newest(ledgerTypeStreamSession, len(streamRows) > 0); err != nil {
		return data, err
	}

	// The recap of the board: the newest one that is not empty.
	boardWeek := int64(-1)
	for before := int64(math.MaxInt64); boardWeek < 0; {
		rows, err := store.NewestN(ctx, crewID, []string{ledgerTypeWeeklyRecap}, before, zoneBoardRecapBatch)
		if err != nil {
			return data, err
		}
		for _, ev := range rows {
			if r, ok := recapFromRow(ev); ok && !isEmptyRecap(r) {
				data.Recaps = append(data.Recaps, r)
				boardWeek = r.WeekStart
				break
			}
		}
		if len(rows) < zoneBoardRecapBatch {
			break
		}
		before = rows[len(rows)-1].Timestamp
	}

	// One page of Crew history, and one week more.
	//
	// Two rows can hold one session id (stop_stream and the socket drop both
	// ended the stream). The feed lists the newer one. The older one is
	// dropped before the weeks are counted (dropSupersededStreamRows), so a
	// page is not short and no page lists the session a second time.
	const want = zonesHistoryPageWeeks + 1
	before := historyBefore
	if before <= 0 {
		before = math.MaxInt64
	}
	weekKept := map[int64]int{}    // week start -> clips and recaps kept
	weekStreams := map[int64]int{} // week start -> stream sessions kept
	found := func() int {
		n := len(weekKept)
		for w := range weekStreams {
			if _, ok := weekKept[w]; !ok {
				n++
			}
		}
		return n
	}
	for {
		weeks, err := store.WeeksBefore(ctx, crewID, zoneHistoryTypes, before, 2*want)
		if err != nil {
			return data, err
		}
		var fresh []CrewEvent
		maybe := found()
		for _, w := range weeks {
			before = w.WeekStart
			// A week that starts in the free window has no clip or stream
			// session for Crew history. Only its recap is read.
			types := zoneHistoryTypes
			if w.WeekStart >= windowStart {
				types = []string{ledgerTypeWeeklyRecap}
			}
			rows, err := store.Between(ctx, crewID, w.WeekStart, w.WeekStart+weekMs-1, types)
			if err != nil {
				return data, err
			}
			// What zoneHistory takes of this week: the clips and stream
			// sessions older than the window, and a recap with content that
			// is not the recap of the board.
			kept, streams := 0, 0
			for _, ev := range rows {
				switch ev.Type {
				case ledgerTypeWeeklyRecap:
					if r, ok := recapFromRow(ev); ok && r.WeekStart > 0 && r.WeekStart != boardWeek && !isEmptyRecap(r) {
						data.Recaps = append(data.Recaps, r)
						kept++
					}
				case ledgerTypeClip:
					if c, ok := clipFromRow(ev); ok && c.Ts < windowStart {
						data.Clips = append(data.Clips, c)
						kept++
					}
				case ledgerTypeStreamSession:
					if ev.Timestamp < windowStart {
						fresh = append(fresh, ev)
						streams++
					}
				}
			}
			if kept > 0 {
				weekKept[w.WeekStart] += kept
			}
			if kept+streams > 0 {
				if maybe++; maybe >= want {
					break
				}
			}
		}
		if fresh, err = dropSupersededStreamRows(ctx, store, crewID, fresh); err != nil {
			return data, err
		}
		for _, ev := range fresh {
			weekStreams[weekStartOf(ev.Timestamp)]++
		}
		streamRows = append(streamRows, fresh...)
		if found() >= want || (maybe < want && len(weeks) < 2*want) {
			break
		}
	}

	// Oldest first, as the documents held them.
	sort.SliceStable(streamRows, func(i, j int) bool { return streamRows[i].Timestamp < streamRows[j].Timestamp })
	sort.SliceStable(data.Clips, func(i, j int) bool { return data.Clips[i].Ts < data.Clips[j].Ts })
	sort.SliceStable(data.Recaps, func(i, j int) bool { return data.Recaps[i].WeekStart < data.Recaps[j].WeekStart })
	data.Streams = streamSessionsFromRows(streamRows)
	return data, nil
}
