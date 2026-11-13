package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

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
}

// historyKinds are the documents the reconcile visits after the ledger
// documents.
var historyKinds = []*historyKind{streamSessionHistoryKind}

// historyPassStats are the totals of phase 1b of one pass.
type historyPassStats struct {
	Docs, Inserted, Updated, NoID int
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
	Clean    bool
	Err      error
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

		inserted, err := store.Put(ctx, crewID, missing)
		if err != nil {
			res.Err = fmt.Errorf("store put: %w", err)
			return res
		}
		res.Inserted += inserted
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

// crewStreamSessions returns the stream sessions of a crew for the feed,
// oldest first: the rows of the store after the cutover of phase 1b, the
// document crew_stream_sessions before it. A store read that fails falls back
// to the document, which is still written.
func crewStreamSessions(ctx context.Context, nk runtime.NakamaModule, crewID string) []StoredStreamSession {
	if store := historyTruthStore(ctx, nk); store != nil {
		rows, err := store.Between(ctx, crewID, math.MinInt64, math.MaxInt64, []string{ledgerTypeStreamSession})
		if err == nil {
			return streamSessionsFromRows(rows)
		}
		ledgerLogError("ledger_store_read_failed kind=stream_sessions crew=%s: %v. Reading the legacy document", crewID, err)
	}
	doc, _ := readStreamSessionsDoc(ctx, nk, crewID)
	return doc.Sessions
}
