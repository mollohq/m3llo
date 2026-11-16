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

// Phase 1b of the durable crew ledger (specs/16-CREW-EVENT-LEDGER.md section 3.8):
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
	// that no row can hold: they have no event id and nothing else to make
	// an id from. A read or decode error is returned: it is not an empty
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
	// PutUnique stores a row whose id Delete removed: a live write may use
	// the id again. The reconcile must not: the scrub of a deleted account
	// dropped the row, and the document that still holds the entry is not
	// scrubbed yet or was written by an older release.
	ids := make([]string, 0, len(missing))
	for _, row := range missing {
		ids = append(ids, row.Event.ID)
	}
	deleted, err := store.DeletedIDs(ctx, crewID, ids)
	if err != nil {
		return 0, 0, err
	}
	wanted := missing[:0:0]
	for _, row := range missing {
		if !deleted[row.Event.ID] {
			wanted = append(wanted, row)
		}
	}
	missing = wanted
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

// streamSessionRefIDPrefix starts the row id of a stream session of the
// durable copy that has no event id. An event id is a generated id and never
// has this prefix.
const streamSessionRefIDPrefix = "session_"

// streamSessionRowID is the row id of a stream session of the durable copy:
// the id of its ledger event. An entry from before the copy held the event id
// gets an id made from its session id, so the import stores it and stores it
// once. It is "" for an entry with neither.
func streamSessionRowID(s StoredStreamSession) string {
	switch {
	case s.EventID != "":
		return s.EventID
	case s.SessionID != "":
		return streamSessionRefIDPrefix + s.SessionID
	}
	return ""
}

// streamSessionRow is a stream session of the durable copy as a row.
func streamSessionRow(s StoredStreamSession) ledgerRow {
	return ledgerRow{
		Ref: s.SessionID,
		Event: CrewEvent{
			ID:        streamSessionRowID(s),
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
	eventID := ev.ID
	if d.SessionID != "" && eventID == streamSessionRefIDPrefix+d.SessionID {
		// The entry had no event id (streamSessionRowID). The feed gets it
		// as the document held it.
		eventID = ""
	}
	return storedStreamSessionFrom(eventID, ev.Timestamp, ev.Score, d), true
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
		sessionInLedger := map[string]bool{}
		for _, ev := range ledger.Events {
			inLedger[ev.ID] = true
			if ref := eventRef(ev); ref != "" {
				sessionInLedger[ref] = true
			}
		}
		rows := make([]ledgerRow, 0, len(doc.Sessions))
		noID := 0
		for _, s := range doc.Sessions {
			switch {
			case streamSessionRowID(s) == "":
				// Neither an event id nor a session id: no row can hold it.
				noID++
			case inLedger[s.EventID], s.EventID == "" && sessionInLedger[s.SessionID]:
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

// clipRefRowIDPrefix starts the row id of a clip that has no event id. It
// does not start with "clip_", so it never meets the id of clipRowID.
const clipRefRowIDPrefix = "clipref_"

// clipRowIDOf is the row id of a stored clip. A clip from before clips held
// an event id gets an id made from its clip_id, so the import stores it and
// stores it once. It is "" for a clip with neither.
func clipRowIDOf(c StoredClip) string {
	switch {
	case c.EventID != "":
		return clipRowID(c.EventID)
	case c.ClipID != "":
		return clipRefRowIDPrefix + c.ClipID
	}
	return ""
}

// clipRow is a stored clip as a row. The data is the clip as the document
// holds it, so a clip with no event id reads the same from both.
func clipRow(c StoredClip) ledgerRow {
	return ledgerRow{
		Ref: c.ClipID,
		Event: CrewEvent{
			ID:        clipRowIDOf(c),
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
		made := map[string]bool{}
		for _, c := range doc.Clips {
			id := clipRowIDOf(c)
			// No row can hold a clip with neither an event id nor a clip_id,
			// or a second clip with no event id and the same clip_id. Such
			// an entry would vanish from the readers at the cutover, so it
			// is counted and blocks the marker.
			if id == "" || (c.EventID == "" && made[id]) {
				noID++
				continue
			}
			made[id] = true
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

// crewNewestClipsSince returns the clips of a crew with ts >= fromMs, oldest
// first, and at most the newest CrewClipsMaxRetained of them. That is what
// the capped document held, and the bound of a read with no lower time: the
// catch-up of a member who was never in the feed (last_seen 0).
func crewNewestClipsSince(ctx context.Context, nk runtime.NakamaModule, crewID string, fromMs int64) []StoredClip {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.NewestN(ctx, crewID, []string{ledgerTypeClip}, math.MaxInt64, CrewClipsMaxRetained)
		if err == nil {
			// Newest first: the rows from fromMs on are at the front.
			n := 0
			for n < len(rows) && rows[n].Timestamp >= fromMs {
				n++
			}
			rows = rows[:n]
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
			return clipsFromRows(rows)
		}
		ledgerLogError("ledger_store_read_failed kind=clips crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readClipsDoc(ctx, nk, crewID)
	out := make([]StoredClip, 0, len(doc.Clips))
	for _, c := range doc.Clips {
		if c.Ts >= fromMs {
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
	rowID := ""
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
				rowID = clipRowIDOf(*clip)
			}
			if rowID == "" {
				return errLedgerEventNotFound
			}
			return store.UpdateData(ctx, crewID, rowID, func(raw json.RawMessage) (json.RawMessage, error) {
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
				rowID = id
			}
			return err
		})
}

// updateClipDoc changes one clip of the legacy clips document, with the
// version retry of the other document writers. It returns the row id of the
// clip (clipRowIDOf).
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
			return clipRowIDOf(*clip), nil
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
//
// Its time is the time the recap was written (generated_at). A recap is a
// counted event, and the counted queries select by time: the newest N, the
// last D days. A recap dated at its week start would be 7 days old when it is
// written.
//
// Its ref is the week start (recapWeekRef). Crew history files a recap under
// the week it is about, and the newest recap is the one of the newest week:
// both read by ref (RefsBefore), not by time.
//
// A legacy ledger document can also hold weekly_recap events (the dev seed
// writes them). The store keeps those as "ledger_weekly_recap" rows, for the
// invite preview only.

// recapRowScore is the score the merged timeline gives a recap.
const recapRowScore = 30

// recapWeekRef is the ref of the recap of a week: the week start as a number
// of one width, so that refs order as weeks. A recap with no week start has
// no ref.
func recapWeekRef(weekStart int64) string {
	if weekStart <= 0 {
		return ""
	}
	return fmt.Sprintf("%019d", weekStart)
}

// recapRowTs is the time of a recap row: generated_at. A recap that does not
// hold it gets the end of its week, the time a recap is written.
func recapRowTs(r WeeklyRecapData) int64 {
	switch {
	case r.GeneratedAt > 0:
		return r.GeneratedAt
	case r.WeekEnd > 0:
		return r.WeekEnd
	}
	return r.WeekStart + weekMs
}

// recapRow is a weekly recap as a row.
func recapRow(r WeeklyRecapData) ledgerRow {
	return ledgerRow{
		Ref: recapWeekRef(r.WeekStart),
		Event: CrewEvent{
			ID:        recapEntryID(r.WeekStart),
			Type:      ledgerTypeWeeklyRecap,
			Timestamp: recapRowTs(r),
			Score:     recapRowScore,
			Data:      r,
		},
	}
}

// newestWeekRecaps returns up to n recaps of a crew from the store, the
// newest week first. beforeWeek bounds the week start, 0 is no bound.
func newestWeekRecaps(ctx context.Context, store ledgerStore, crewID string, beforeWeek int64, n int) ([]WeeklyRecapData, error) {
	rows, err := store.RefsBefore(ctx, crewID, ledgerTypeWeeklyRecap, recapWeekRef(beforeWeek), n)
	if err != nil {
		return nil, err
	}
	return recapsFromRows(rows), nil
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
		recaps, err := newestWeekRecaps(ctx, store, crewID, 0, 1)
		if err == nil {
			if len(recaps) > 0 {
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
		recaps, err := newestWeekRecaps(ctx, store, crewID, 0, 1)
		if err == nil {
			state := &CrewRecapsDoc{CrewID: crewID, Recaps: recaps}
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
	// Locked holds the clips of the weeks of Crew history that the viewer
	// cannot open, by week start. Clips does not hold the clips of such a
	// week. It is nil when Clips holds every clip (the documents).
	Locked map[int64]zoneLockedWeek
}

// zoneLockedWeek is what a locked week of Crew history shows of its clips:
// how many, and the first ones in the order of rankClips for the teasers.
type zoneLockedWeek struct {
	ClipCount int
	TopClips  []StoredClip
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
//
// open says if the viewer can open a week of Crew history (zoneWeekOpen). The
// store read does not load the clips of a week that is locked. nil means
// every week is open.
func loadZoneHistory(ctx context.Context, nk runtime.NakamaModule, crewID string, windowStart, historyBefore int64, open func(weekStart int64) bool) zoneHistoryData {
	if store := historyTruthStore(ctx, nk); store != nil {
		data, err := loadZoneHistoryFromStore(ctx, store, crewID, windowStart, historyBefore, open)
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
//     zoneHistory knows if a next page exists. Of a week that the viewer
//     cannot open it reads the clip count from the week rollup and the
//     clips of the teasers (zoneLockedWeek), not the clips.
//
// The weeks come from the week rollup (WeeksBefore). A week of the rollup
// that holds nothing for Crew history is skipped: its rows are in the free
// window, or its recap is empty or is the recap of the board.
func loadZoneHistoryFromStore(ctx context.Context, store ledgerStore, crewID string, windowStart, historyBefore int64, open func(weekStart int64) bool) (zoneHistoryData, error) {
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

	// The recap of the board: the one of the newest week that is not empty.
	boardWeek := int64(-1)
	for beforeWeek := int64(0); boardWeek < 0; {
		recaps, err := newestWeekRecaps(ctx, store, crewID, beforeWeek, zoneBoardRecapBatch)
		if err != nil {
			return data, err
		}
		for _, r := range recaps {
			if !isEmptyRecap(r) {
				data.Recaps = append(data.Recaps, r)
				boardWeek = r.WeekStart
				break
			}
		}
		if len(recaps) < zoneBoardRecapBatch {
			break
		}
		beforeWeek = recaps[len(recaps)-1].WeekStart
	}

	// One page of Crew history, and one week more.
	//
	// The weeks that hold a clip or a stream session come from the week
	// rollup (WeeksBefore). The time of a recap row is not in its week, so
	// the recaps come by their week (newestWeekRecaps). A week counts when
	// zoneHistory takes something of it: a clip or a stream session older
	// than the free window, or a recap with content that is not the recap of
	// the board.
	//
	// Two rows can hold one session id (stop_stream and the socket drop both
	// ended the stream). The feed lists the newer one. The older one is
	// dropped before the weeks are counted (dropSupersededStreamRows), so a
	// page is not short and no page lists the session a second time.
	const want = zonesHistoryPageWeeks + 1
	const batch = 2 * want
	// zoneHistory takes the weeks that start before historyBefore.
	before := int64(math.MaxInt64)
	if historyBefore > 0 {
		before = weekStartOf(historyBefore)
		if before < historyBefore {
			before += weekMs
		}
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
		clipWeeks, err := store.WeeksBefore(ctx, crewID, []string{ledgerTypeClip}, before, batch)
		if err != nil {
			return data, err
		}
		streamWeeks, err := store.WeeksBefore(ctx, crewID, []string{ledgerTypeStreamSession}, before, batch)
		if err != nil {
			return data, err
		}
		recapBefore := before
		if before == math.MaxInt64 {
			recapBefore = 0
		}
		recaps, err := newestWeekRecaps(ctx, store, crewID, recapBefore, batch)
		if err != nil {
			return data, err
		}
		// The weeks of this round, newest first. A list that is full can
		// have more below its last week, so the round stops there: the week
		// rollup holds a week whole, a list of recaps can end inside one.
		floor := int64(math.MinInt64)
		clipCount := map[int64]int{}
		hasStreams := map[int64]bool{}
		weekRecaps := map[int64][]WeeklyRecapData{}
		inRound := map[int64]bool{}
		for _, w := range clipWeeks {
			clipCount[w.WeekStart] = w.N
			inRound[w.WeekStart] = true
		}
		for _, w := range streamWeeks {
			hasStreams[w.WeekStart] = true
			inRound[w.WeekStart] = true
		}
		for _, r := range recaps {
			w := weekStartOf(r.WeekStart)
			weekRecaps[w] = append(weekRecaps[w], r)
			inRound[w] = true
		}
		for _, full := range [][]weekCount{clipWeeks, streamWeeks} {
			if len(full) == batch && full[batch-1].WeekStart > floor {
				floor = full[batch-1].WeekStart
			}
		}
		if len(recaps) == batch {
			if last := weekStartOf(recaps[batch-1].WeekStart) + weekMs; last > floor {
				floor = last
			}
		}
		weeks := make([]int64, 0, len(inRound))
		for w := range inRound {
			if w >= floor {
				weeks = append(weeks, w)
			}
		}
		sort.Slice(weeks, func(i, j int) bool { return weeks[i] > weeks[j] })
		more := len(clipWeeks) == batch || len(streamWeeks) == batch || len(recaps) == batch
		if len(weeks) == 0 && more {
			// More recaps of one week than a batch holds. No crew has them.
			return data, fmt.Errorf("crew %s: the recaps before week %d do not fit one read", crewID, before)
		}

		var fresh []CrewEvent
		maybe := found()
		for _, w := range weeks {
			before = w
			kept, streams := 0, 0
			for _, r := range weekRecaps[w] {
				if r.WeekStart != boardWeek && !isEmptyRecap(r) {
					data.Recaps = append(data.Recaps, r)
					kept++
				}
			}
			// A week that starts in the free window has no clip or stream
			// session for Crew history.
			var types []string
			if w < windowStart {
				if clipCount[w] > 0 {
					types = append(types, ledgerTypeClip)
				}
				if hasStreams[w] {
					types = append(types, ledgerTypeStreamSession)
				}
			}
			// A locked week that ends before the free window: the clip
			// count of the rollup and the clips of the teasers. The stream
			// sessions are read, so that two rows of one session count once.
			if clipCount[w] > 0 && w+weekMs <= windowStart && open != nil && !open(w) {
				top, err := store.TopInRange(ctx, crewID, ledgerTypeClip, w, w+weekMs-1, "clip_type", clipTypeGame, zonesTeasersMax)
				if err != nil {
					return data, err
				}
				if data.Locked == nil {
					data.Locked = map[int64]zoneLockedWeek{}
				}
				data.Locked[w] = zoneLockedWeek{ClipCount: clipCount[w], TopClips: clipsFromRows(top)}
				kept += clipCount[w]
				types = nil
				if hasStreams[w] {
					types = []string{ledgerTypeStreamSession}
				}
			}
			if len(types) > 0 {
				rows, err := store.Between(ctx, crewID, w, w+weekMs-1, types)
				if err != nil {
					return data, err
				}
				for _, ev := range rows {
					switch ev.Type {
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
			}
			if kept > 0 {
				weekKept[w] += kept
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
		if found() >= want || (maybe < want && !more) {
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
