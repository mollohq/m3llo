package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ledgerStoreConformance checks that a ledgerStore keeps the contract of the
// interface. `go test` runs it on the in-memory store. The RPC
// dev_ledger_selftest runs it on the SQL store, on the real tables.
//
// It writes only to two crews with a new random id and deletes them at the
// end, so it is safe on a database that holds real crews.
func ledgerStoreConformance(ctx context.Context, store ledgerStore) (err error) {
	const day = int64(24 * 60 * 60 * 1000)
	run := generateEventID()
	crewA := "selftest_a_" + run
	crewB := "selftest_b_" + run
	crewC := "selftest_c_" + run
	user := "selftest_user_" + run
	now := ledgerNow().UnixMilli()

	defer func() {
		for _, crewID := range []string{crewA, crewB, crewC} {
			if delErr := store.DeleteCrew(ctx, crewID); delErr != nil && err == nil {
				err = fmt.Errorf("cleanup of %s: %w", crewID, delErr)
			}
		}
	}()

	mk := func(id, typ string, ts int64, data interface{}) CrewEvent {
		return CrewEvent{ID: id + "_" + run, CrewID: crewA, Type: typ, ActorID: user, Timestamp: ts, Score: 7, Data: data}
	}
	e1 := mk("e1", "voice_session", now-60*60*1000, VoiceSessionData{ChannelName: "General", ParticipantIDs: []string{user, "other_" + run}, DurationMin: 30})
	e2 := mk("e2", "game_session", now-2*day, GameSessionData{GameName: "Game", PlayerIDs: []string{user}, DurationMin: 45})
	e3 := mk("e3", "stream_session", now-3*day, StreamSessionData{SessionID: "sess_" + run, StreamerID: user, Title: "Stream", SnapshotURLs: []string{}})
	e4 := mk("e4", "moment", now-30*day, MomentData{Text: "old", Sentiment: "win"})
	e5 := mk("e5", "game_session", now-10*day, GameSessionData{GameName: "Game", DurationMin: 20})
	e6 := mk("e6", "clip", now-1*day, map[string]interface{}{"clip_id": "clip_" + run})
	e7 := mk("e7", "game_session", now-40*day, GameSessionData{GameName: "Game", DurationMin: 5})
	e7.ActorID = "" // an event with no actor

	ids := func(events []CrewEvent) string {
		out := make([]string, 0, len(events))
		for _, e := range events {
			out = append(out, strings.TrimSuffix(e.ID, "_"+run))
		}
		return strings.Join(out, ",")
	}
	want := func(what string, events []CrewEvent, wantIDs string) error {
		if got := ids(events); got != wantIDs {
			return fmt.Errorf("%s: got [%s], want [%s]", what, got, wantIDs)
		}
		return nil
	}

	// Append is idempotent on the crew and the event id.
	inserted, err := store.Append(ctx, e1, eventRef(e1))
	if err != nil {
		return fmt.Errorf("append e1: %w", err)
	}
	if !inserted {
		return errors.New("append e1: reported a duplicate on the first append")
	}
	if inserted, err = store.Append(ctx, e1, eventRef(e1)); err != nil {
		return fmt.Errorf("append e1 again: %w", err)
	} else if inserted {
		return errors.New("append e1 again: reported an insert for a duplicate")
	}
	for _, ev := range []CrewEvent{e2, e3, e4, e5, e6, e7} {
		if inserted, err = store.Append(ctx, ev, eventRef(ev)); err != nil || !inserted {
			return fmt.Errorf("append %s: inserted=%v err=%v", ev.ID, inserted, err)
		}
	}

	// Between: both bounds are inclusive, the order is oldest first, and an
	// event of 30 or 40 days is still there.
	all, err := store.Between(ctx, crewA, 0, now, nil)
	if err != nil {
		return fmt.Errorf("between all: %w", err)
	}
	if err := want("between all", all, "e7,e4,e5,e3,e2,e6,e1"); err != nil {
		return err
	}
	if all[0].ActorID != "" || all[1].ActorID != user || all[1].Score != 7 || all[1].CrewID != crewA || all[1].Type != "moment" {
		return fmt.Errorf("between all: fields did not survive: %+v %+v", all[0], all[1])
	}
	if obj, ok := all[1].Data.(map[string]interface{}); !ok || obj["text"] != "old" {
		return fmt.Errorf("between all: data of e4 = %#v, want a JSON object with text", all[1].Data)
	}
	got, err := store.Between(ctx, crewA, e2.Timestamp, e1.Timestamp, nil)
	if err != nil {
		return fmt.Errorf("between inclusive: %w", err)
	}
	if err := want("between inclusive", got, "e2,e6,e1"); err != nil {
		return err
	}
	if got, err = store.Between(ctx, crewA, e2.Timestamp+1, e1.Timestamp-1, nil); err != nil {
		return fmt.Errorf("between exclusive: %w", err)
	} else if err := want("between inside the bounds", got, "e6"); err != nil {
		return err
	}
	if got, err = store.Between(ctx, crewA, 0, now, []string{"game_session", "moment"}); err != nil {
		return fmt.Errorf("between types: %w", err)
	} else if err := want("between types", got, "e7,e4,e5,e2"); err != nil {
		return err
	}

	// NewestN: newest first, the type filter holds, beforeMs is exclusive.
	if got, err = store.NewestN(ctx, crewA, ledgerCountedTypes, now+1, 3); err != nil {
		return fmt.Errorf("newest: %w", err)
	} else if err := want("newest 3 counted", got, "e6,e2,e3"); err != nil {
		return err
	}
	if got, err = store.NewestN(ctx, crewA, ledgerCountedTypes, e6.Timestamp, 1); err != nil {
		return fmt.Errorf("newest before: %w", err)
	} else if err := want("newest before e6", got, "e2"); err != nil {
		return err
	}
	if got, err = store.NewestN(ctx, crewA, []string{"moment"}, now+1, 10); err != nil {
		return fmt.Errorf("newest moment: %w", err)
	} else if err := want("newest moments", got, "e4"); err != nil {
		return err
	}

	// CountOlder equals a recount of the rows, at several cut points and on
	// week borders. WeeksBefore sums to the same total.
	checkCounts := func(stage string, types []string) error {
		cuts := []int64{now + 1, now - 5*day, now - 20*day, now - 35*day, now - 50*day,
			weekStartOf(now), weekStartOf(now - 10*day), weekStartOf(now-10*day) + 7*day, e5.Timestamp, e5.Timestamp + 1}
		for _, cut := range cuts {
			n, err := store.CountOlder(ctx, crewA, types, cut)
			if err != nil {
				return fmt.Errorf("%s: count older: %w", stage, err)
			}
			rows, err := store.Between(ctx, crewA, 0, cut-1, types)
			if err != nil {
				return fmt.Errorf("%s: recount: %w", stage, err)
			}
			if n != len(rows) {
				return fmt.Errorf("%s: count older than %d (types %v) = %d, recount = %d", stage, cut, types, n, len(rows))
			}
		}
		weeks, err := store.WeeksBefore(ctx, crewA, types, weekStartOf(now)+7*day, 100)
		if err != nil {
			return fmt.Errorf("%s: weeks before: %w", stage, err)
		}
		total := 0
		for i, w := range weeks {
			if w.N <= 0 || w.WeekStart != weekStartOf(w.WeekStart) {
				return fmt.Errorf("%s: week %+v is empty or not a week start", stage, w)
			}
			if i > 0 && w.WeekStart >= weeks[i-1].WeekStart {
				return fmt.Errorf("%s: weeks are not newest first: %+v", stage, weeks)
			}
			total += w.N
		}
		rows, err := store.Between(ctx, crewA, 0, now, types)
		if err != nil {
			return fmt.Errorf("%s: recount of weeks: %w", stage, err)
		}
		if total != len(rows) {
			return fmt.Errorf("%s: weeks sum to %d (types %v), recount = %d", stage, total, types, len(rows))
		}
		return nil
	}
	for _, types := range [][]string{nil, ledgerCountedTypes, {"game_session"}} {
		if err := checkCounts("after append", types); err != nil {
			return err
		}
	}
	weeks, err := store.WeeksBefore(ctx, crewA, nil, weekStartOf(now)+7*day, 1)
	if err != nil || len(weeks) != 1 {
		return fmt.Errorf("weeks before with limit 1: %+v err=%v", weeks, err)
	}
	if weeks, err = store.WeeksBefore(ctx, crewA, nil, weekStartOf(e7.Timestamp), 100); err != nil || len(weeks) != 0 {
		return fmt.Errorf("weeks before the oldest week: %+v err=%v", weeks, err)
	}

	// RecentView: pulse types only, the 7 days before the last append, in
	// append order. The clip (e6) and the older events are not in it.
	if got, err = store.RecentView(ctx, crewA); err != nil {
		return fmt.Errorf("recent view: %w", err)
	} else if err := want("recent view", got, "e1,e2,e3"); err != nil {
		return err
	}

	// UpdateData replaces the data of one event.
	err = store.UpdateData(ctx, crewA, e3.ID, func(raw json.RawMessage) (json.RawMessage, error) {
		var d StreamSessionData
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		if d.SessionID != "sess_"+run {
			return nil, fmt.Errorf("update data got session id %q", d.SessionID)
		}
		d.SnapshotURLs = []string{"https://cdn.example/1.jpg"}
		return json.Marshal(d)
	})
	if err != nil {
		return fmt.Errorf("update data: %w", err)
	}
	if got, err = store.Between(ctx, crewA, e3.Timestamp, e3.Timestamp, nil); err != nil || len(got) != 1 {
		return fmt.Errorf("read back e3: %d events, err=%v", len(got), err)
	}
	if d, decErr := decodeStreamSessionData(got[0].Data); decErr != nil || len(d.SnapshotURLs) != 1 || d.Title != "Stream" {
		return fmt.Errorf("update data did not hold: %+v err=%v", d, decErr)
	}
	if err = store.UpdateData(ctx, crewA, "missing_"+run, func(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }); !errors.Is(err, errLedgerEventNotFound) {
		return fmt.Errorf("update data of an unknown event: err=%v, want errLedgerEventNotFound", err)
	}
	failed := errors.New("fn failed")
	if err = store.UpdateData(ctx, crewA, e3.ID, func(json.RawMessage) (json.RawMessage, error) { return nil, failed }); !errors.Is(err, failed) {
		return fmt.Errorf("update data with a failing fn: err=%v", err)
	}

	// ByType reads across crews. Only the rows of this run are checked.
	ofRun := func(events []CrewEvent) []CrewEvent {
		var out []CrewEvent
		for _, e := range events {
			if e.CrewID == crewA || e.CrewID == crewB {
				out = append(out, e)
			}
		}
		return out
	}
	if got, err = store.ByType(ctx, "stream_session", now-4*day); err != nil {
		return fmt.Errorf("by type: %w", err)
	} else if err := want("by type since 4 days", ofRun(got), "e3"); err != nil {
		return err
	}
	if got, err = store.ByType(ctx, "stream_session", now-2*day); err != nil {
		return fmt.Errorf("by type: %w", err)
	} else if err := want("by type since 2 days", ofRun(got), ""); err != nil {
		return err
	}

	// EventsNamingUser finds the actor and the users in the data.
	if got, err = store.EventsNamingUser(ctx, user); err != nil {
		return fmt.Errorf("events naming user: %w", err)
	} else if err := want("events naming the actor", got, "e4,e5,e3,e2,e6,e1"); err != nil {
		return err
	}
	if got, err = store.EventsNamingUser(ctx, "other_"+run); err != nil {
		return fmt.Errorf("events naming user: %w", err)
	} else if err := want("events naming a participant", got, "e1"); err != nil {
		return err
	}

	// Delete keeps the rollup right and is safe to repeat.
	if err = store.Delete(ctx, crewA, e5.ID); err != nil {
		return fmt.Errorf("delete e5: %w", err)
	}
	if err = store.Delete(ctx, crewA, e5.ID); err != nil {
		return fmt.Errorf("delete e5 again: %w", err)
	}
	if err = store.Delete(ctx, crewA, "missing_"+run); err != nil {
		return fmt.Errorf("delete of an unknown event: %w", err)
	}
	for _, types := range [][]string{nil, ledgerCountedTypes, {"game_session"}} {
		if err := checkCounts("after delete", types); err != nil {
			return err
		}
	}
	if got, err = store.EventsNamingUser(ctx, user); err != nil {
		return fmt.Errorf("events naming user after delete: %w", err)
	} else if err := want("events naming the actor after delete", got, "e4,e3,e2,e6,e1"); err != nil {
		return err
	}
	// A deleted id can be appended again.
	if inserted, err = store.Append(ctx, e5, ""); err != nil || !inserted {
		return fmt.Errorf("append e5 after delete: inserted=%v err=%v", inserted, err)
	}
	if err := checkCounts("after re-append", nil); err != nil {
		return err
	}

	// Backfill: the events keep the order of the document, the view is
	// anchored to the time of the last append of the document, and a second
	// run inserts nothing.
	anchor := now - 20*day
	mkB := func(id string, ts int64) CrewEvent {
		return CrewEvent{ID: id + "_" + run, Type: "member_joined", ActorID: user, Timestamp: ts, Data: MemberJoinedData{Username: "u"}}
	}
	doc := []CrewEvent{mkB("b_out", anchor-8*day), mkB("b_late", anchor-1*day), mkB("b_early", anchor-6*day)}
	n, err := store.Backfill(ctx, crewB, doc, anchor, true)
	if err != nil || n != 3 {
		return fmt.Errorf("backfill: inserted=%d err=%v, want 3", n, err)
	}
	if n, err = store.Backfill(ctx, crewB, doc, anchor, true); err != nil || n != 0 {
		return fmt.Errorf("backfill again: inserted=%d err=%v, want 0", n, err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view of a dormant crew: %w", err)
	} else if err := want("recent view of a dormant crew", got, "b_late,b_early"); err != nil {
		return err
	}
	if got, err = store.Between(ctx, crewB, 0, now, nil); err != nil {
		return fmt.Errorf("between of the backfilled crew: %w", err)
	} else if err := want("between of the backfilled crew", got, "b_out,b_early,b_late"); err != nil {
		return err
	}
	if got[0].CrewID != crewB {
		return fmt.Errorf("backfill did not set the crew id: %q", got[0].CrewID)
	}
	// The anchor does not move back.
	if _, err = store.Backfill(ctx, crewB, nil, anchor-30*day, true); err != nil {
		return fmt.Errorf("backfill with an older anchor: %w", err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view after an older anchor: %w", err)
	} else if err := want("recent view after an older anchor", got, "b_late,b_early"); err != nil {
		return err
	}
	// A live append moves the anchor to now. The dormant events leave the
	// view and stay in the store.
	live := mkB("b_live", now)
	live.CrewID = crewB
	if inserted, err = store.Append(ctx, live, ""); err != nil || !inserted {
		return fmt.Errorf("append after backfill: inserted=%v err=%v", inserted, err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view after a live append: %w", err)
	} else if err := want("recent view after a live append", got, "b_live"); err != nil {
		return err
	}
	if n, err := store.CountOlder(ctx, crewB, nil, now+1); err != nil || n != 4 {
		return fmt.Errorf("count of the backfilled crew = %d err=%v, want 4", n, err)
	}

	// An event that a legacy document held under a type that is not a pulse
	// type is kept as "ledger_<type>". The view gives it back under its
	// document type. No counted query sees it.
	legacy := mkB("b_legacy", now)
	legacy.CrewID = crewB
	legacy.Type = ledgerStoredType("weekly_recap")
	if inserted, err = store.Append(ctx, legacy, ""); err != nil || !inserted {
		return fmt.Errorf("append of a legacy type: inserted=%v err=%v", inserted, err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view with a legacy type: %w", err)
	} else if err := want("recent view with a legacy type", got, "b_live,b_legacy"); err != nil {
		return err
	}
	if got[1].Type != "weekly_recap" {
		return fmt.Errorf("recent view gives the legacy event the type %q, want weekly_recap", got[1].Type)
	}
	if got, err = store.Between(ctx, crewB, now, now, []string{"ledger_weekly_recap"}); err != nil || len(got) != 1 {
		return fmt.Errorf("between of the stored legacy type: %d events, err=%v", len(got), err)
	}
	if got, err = store.NewestN(ctx, crewB, ledgerCountedTypes, now+day, 10); err != nil || len(got) != 0 {
		return fmt.Errorf("counted events of crew b: %d, err=%v, want 0", len(got), err)
	}

	// Backfill puts a new event at its document position. Without
	// docOrderWins the events the crew holds keep their order.
	mid, end, first := mkB("b_mid", now), mkB("b_end", now), mkB("b_first", now)
	live.CrewID, legacy.CrewID = "", ""
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{live, mid, legacy, end}, now, false); err != nil || n != 2 {
		return fmt.Errorf("backfill of two new events: inserted=%d err=%v, want 2", n, err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view after a positional backfill: %w", err)
	} else if err := want("recent view after a positional backfill", got, "b_live,b_mid,b_legacy,b_end"); err != nil {
		return err
	}
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{first, legacy, live}, now, false); err != nil || n != 1 {
		return fmt.Errorf("backfill of a first event: inserted=%d err=%v, want 1", n, err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view after a backfill at the front: %w", err)
	} else if err := want("recent view after a backfill at the front", got, "b_first,b_live,b_mid,b_legacy,b_end"); err != nil {
		return err
	}
	// With docOrderWins the events of the document take the document order.
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{end, live}, now, true); err != nil || n != 0 {
		return fmt.Errorf("backfill with the document order: inserted=%d err=%v, want 0", n, err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view in document order: %w", err)
	} else if err := want("recent view in document order", got, "b_first,b_mid,b_legacy,b_end,b_live"); err != nil {
		return err
	}
	// A live append goes after everything, also after a reorder.
	last := mkB("b_last", now)
	last.CrewID = crewB
	if inserted, err = store.Append(ctx, last, ""); err != nil || !inserted {
		return fmt.Errorf("append after a reorder: inserted=%v err=%v", inserted, err)
	}

	// Delete remembers the id: a backfill does not bring the event back.
	// Prune does not remember it. A live append clears the memory.
	if err = store.Delete(ctx, crewB, mid.ID); err != nil {
		return fmt.Errorf("delete b_mid: %w", err)
	}
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{mid}, now, false); err != nil || n != 0 {
		return fmt.Errorf("backfill of a deleted event: inserted=%d err=%v, want 0", n, err)
	}
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{mid}, now, true); err != nil || n != 0 {
		return fmt.Errorf("backfill of a deleted event with the document order: inserted=%d err=%v, want 0", n, err)
	}
	if err = store.Prune(ctx, crewB, []string{end.ID, "missing_" + run}); err != nil {
		return fmt.Errorf("prune b_end: %w", err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil {
		return fmt.Errorf("recent view after delete and prune: %w", err)
	} else if err := want("recent view after delete and prune", got, "b_first,b_legacy,b_live,b_last"); err != nil {
		return err
	}
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{end}, now, false); err != nil || n != 1 {
		return fmt.Errorf("backfill of a pruned event: inserted=%d err=%v, want 1", n, err)
	}
	mid.CrewID = crewB
	if inserted, err = store.Append(ctx, mid, ""); err != nil || !inserted {
		return fmt.Errorf("append of a deleted id: inserted=%v err=%v", inserted, err)
	}
	if err = store.Prune(ctx, crewB, []string{mid.ID}); err != nil {
		return fmt.Errorf("prune b_mid: %w", err)
	}
	if n, err = store.Backfill(ctx, crewB, []CrewEvent{mid}, now, false); err != nil || n != 1 {
		return fmt.Errorf("backfill after a live append cleared the delete: inserted=%d err=%v, want 1", n, err)
	}
	if n, err := store.CountOlder(ctx, crewB, nil, now+1); err != nil || n != 9 {
		return fmt.Errorf("count of crew b at the end = %d err=%v, want 9", n, err)
	}

	// Crews do not see each other.
	if got, err = store.RecentView(ctx, crewA); err != nil {
		return fmt.Errorf("recent view of crew a: %w", err)
	} else if err := want("recent view of crew a at the end", got, "e1,e2,e3"); err != nil {
		return err
	}

	// The rows that are not of the legacy ledger path (phase 1b).
	if err := ledgerPutConformance(ctx, store, crewC, run, now); err != nil {
		return err
	}

	// DeleteCrew removes everything.
	if err = store.DeleteCrew(ctx, crewB); err != nil {
		return fmt.Errorf("delete crew: %w", err)
	}
	if got, err = store.Between(ctx, crewB, 0, now+day, nil); err != nil || len(got) != 0 {
		return fmt.Errorf("between after delete crew: %d events, err=%v", len(got), err)
	}
	if got, err = store.RecentView(ctx, crewB); err != nil || len(got) != 0 {
		return fmt.Errorf("recent view after delete crew: %d events, err=%v", len(got), err)
	}
	if weeks, err = store.WeeksBefore(ctx, crewB, nil, now+7*day, 100); err != nil || len(weeks) != 0 {
		return fmt.Errorf("weeks after delete crew: %+v err=%v", weeks, err)
	}
	if n, err := store.CountOlder(ctx, crewB, nil, now+day); err != nil || n != 0 {
		return fmt.Errorf("count after delete crew = %d err=%v", n, err)
	}
	return nil
}

// ledgerPutConformance checks Put, PutUnique, ByRef and ByIDs: the rows of
// the clips, the weekly recaps and the stream sessions of the durable copy.
// It writes only to crewID.
func ledgerPutConformance(ctx context.Context, store ledgerStore, crewID, run string, now int64) error {
	const day = int64(24 * 60 * 60 * 1000)
	user := "selftest_putter_" + run
	id := func(name string) string { return name + "_" + run }
	names := func(events []CrewEvent) string {
		out := make([]string, 0, len(events))
		for _, e := range events {
			out = append(out, strings.TrimSuffix(e.ID, "_"+run))
		}
		return strings.Join(out, ",")
	}
	view := func(what, wantIDs string) error {
		got, err := store.RecentView(ctx, crewID)
		if err != nil {
			return fmt.Errorf("%s: recent view: %w", what, err)
		}
		if names(got) != wantIDs {
			return fmt.Errorf("%s: recent view = [%s], want [%s]", what, names(got), wantIDs)
		}
		return nil
	}

	// A dormant crew: its last append was 20 days ago.
	anchor := now - 20*day
	old := CrewEvent{ID: id("p_old"), Type: "voice_session", ActorID: user, Timestamp: anchor - day, Data: VoiceSessionData{DurationMin: 5}}
	if n, err := store.Backfill(ctx, crewID, []CrewEvent{old}, anchor, true); err != nil || n != 1 {
		return fmt.Errorf("put: backfill of the dormant crew: inserted=%d err=%v", n, err)
	}

	clip := ledgerRow{Ref: id("cid"), Event: CrewEvent{ID: id("p_clip"), Type: "clip", ActorID: user, Timestamp: now - day, Score: 50,
		Data: map[string]interface{}{"clip_id": id("cid"), "participants": []string{"friend_" + run}}}}
	recap := ledgerRow{Event: CrewEvent{ID: id("p_recap"), Type: "weekly_recap", Timestamp: weekStartOf(now - 14*day), Score: 30,
		Data: map[string]interface{}{"week_start": weekStartOf(now - 14*day)}}}
	stream := ledgerRow{Ref: id("sess"), Event: CrewEvent{ID: id("p_stream"), Type: "stream_session", ActorID: user, Timestamp: now - 60*60*1000, Score: 30,
		Data: StreamSessionData{SessionID: id("sess"), StreamerID: user, Title: "Put", SnapshotURLs: []string{"https://cdn.example/p.jpg"}}}}
	rows := []ledgerRow{clip, recap, stream}
	if n, err := store.Put(ctx, crewID, rows); err != nil || n != 3 {
		return fmt.Errorf("put: inserted=%d err=%v, want 3", n, err)
	}
	if n, err := store.Put(ctx, crewID, rows); err != nil || n != 0 {
		return fmt.Errorf("put again: inserted=%d err=%v, want 0", n, err)
	}
	if n, err := store.Put(ctx, crewID, nil); err != nil || n != 0 {
		return fmt.Errorf("put of no rows: inserted=%d err=%v", n, err)
	}
	if _, err := store.Put(ctx, crewID, []ledgerRow{{Event: CrewEvent{Type: "clip", Timestamp: now}}}); err == nil {
		return errors.New("put of a row with no id: no error")
	}

	// Put does not move the anchor: the event of 21 days ago is still the
	// view. The stream session of one hour ago is a pulse type, and it is
	// not in the view.
	if err := view("after put", "p_old"); err != nil {
		return err
	}
	// The counted queries see the rows, newest first, with their fields.
	got, err := store.NewestN(ctx, crewID, ledgerCountedTypes, now+1, 10)
	if err != nil {
		return fmt.Errorf("put: newest: %w", err)
	}
	if names(got) != "p_stream,p_clip,p_recap" {
		return fmt.Errorf("put: newest counted = [%s], want [p_stream,p_clip,p_recap]", names(got))
	}
	if got[1].CrewID != crewID || got[1].ActorID != user || got[1].Score != 50 || got[1].Type != "clip" {
		return fmt.Errorf("put: fields of the clip did not survive: %+v", got[1])
	}
	if got[2].ActorID != "" {
		return fmt.Errorf("put: the recap has the actor %q", got[2].ActorID)
	}
	for _, cut := range []int64{now + 1, now - 2*day, weekStartOf(now), weekStartOf(now - 14*day), weekStartOf(now-14*day) + 1} {
		n, err := store.CountOlder(ctx, crewID, ledgerCountedTypes, cut)
		if err != nil {
			return fmt.Errorf("put: count older: %w", err)
		}
		recount, err := store.Between(ctx, crewID, -1<<62, cut-1, ledgerCountedTypes)
		if err != nil {
			return fmt.Errorf("put: recount: %w", err)
		}
		if n != len(recount) {
			return fmt.Errorf("put: count older than %d = %d, recount = %d", cut, n, len(recount))
		}
	}
	weeks, err := store.WeeksBefore(ctx, crewID, []string{"weekly_recap"}, now, 10)
	if err != nil || len(weeks) != 1 || weeks[0].WeekStart != weekStartOf(now-14*day) || weeks[0].N != 1 {
		return fmt.Errorf("put: weeks of the recap = %+v err=%v", weeks, err)
	}
	if got, err = store.EventsNamingUser(ctx, "friend_"+run); err != nil || names(got) != "p_clip" {
		return fmt.Errorf("put: events naming a clip participant = [%s] err=%v", names(got), err)
	}

	// ByRef and ByIDs.
	if got, err = store.ByRef(ctx, crewID, "clip", clip.Ref); err != nil || names(got) != "p_clip" {
		return fmt.Errorf("by ref: [%s] err=%v, want [p_clip]", names(got), err)
	}
	if obj, ok := got[0].Data.(map[string]interface{}); !ok || obj["clip_id"] != clip.Ref {
		return fmt.Errorf("by ref: data = %#v", got[0].Data)
	}
	if got, err = store.ByRef(ctx, crewID, "stream_session", clip.Ref); err != nil || len(got) != 0 {
		return fmt.Errorf("by ref of another type: %d rows err=%v", len(got), err)
	}
	if got, err = store.ByRef(ctx, crewID, "weekly_recap", ""); err != nil || len(got) != 0 {
		return fmt.Errorf("by ref with no ref: %d rows err=%v", len(got), err)
	}
	// More ids than one query holds, most of them unknown.
	ids := []string{clip.Event.ID, stream.Event.ID}
	for i := 0; i < 2*ledgerByIDsChunk+5; i++ {
		ids = append(ids, fmt.Sprintf("missing_%d_%s", i, run))
	}
	ids = append(ids, recap.Event.ID)
	if got, err = store.ByIDs(ctx, crewID, ids); err != nil || len(got) != 3 {
		return fmt.Errorf("by ids: %d rows err=%v, want 3", len(got), err)
	}
	if got, err = store.ByIDs(ctx, crewID, nil); err != nil || len(got) != 0 {
		return fmt.Errorf("by ids of no ids: %d rows err=%v", len(got), err)
	}

	// UpdateData works on a row of Put.
	err = store.UpdateData(ctx, crewID, clip.Event.ID, func(raw json.RawMessage) (json.RawMessage, error) {
		var d map[string]interface{}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, err
		}
		d["media_url"] = "https://cdn.example/clip.mp4"
		return json.Marshal(d)
	})
	if err != nil {
		return fmt.Errorf("put: update data: %w", err)
	}
	if got, err = store.ByRef(ctx, crewID, "clip", clip.Ref); err != nil || len(got) != 1 {
		return fmt.Errorf("put: read back the clip: %d rows err=%v", len(got), err)
	}
	if obj, _ := got[0].Data.(map[string]interface{}); obj["media_url"] != "https://cdn.example/clip.mp4" || obj["clip_id"] != clip.Ref {
		return fmt.Errorf("put: update data did not hold: %#v", got[0].Data)
	}

	// A legacy document that holds the stream session gives the row its
	// place in the view. Nothing is stored twice.
	doc := stream.Event
	doc.CrewID = ""
	if n, err := store.Backfill(ctx, crewID, []CrewEvent{old, doc}, now, true); err != nil || n != 0 {
		return fmt.Errorf("backfill of a put row: inserted=%d err=%v, want 0", n, err)
	}
	if err := view("after the document took the stream session", "p_stream"); err != nil {
		return err
	}
	if n, err := store.CountOlder(ctx, crewID, []string{"stream_session"}, now+1); err != nil || n != 1 {
		return fmt.Errorf("stream sessions after the backfill = %d err=%v, want 1", n, err)
	}
	// A document event of another type with the id of a put row does not
	// take the row.
	wrong := clip.Event
	wrong.Type = ledgerStoredType("clip")
	if n, err := store.Backfill(ctx, crewID, []CrewEvent{doc, wrong}, now, true); err != nil || n != 0 {
		return fmt.Errorf("backfill of an id of another type: inserted=%d err=%v, want 0", n, err)
	}
	if err := view("after a document event with the id of a clip row", "p_stream"); err != nil {
		return err
	}

	// Put does not bring back an id that Delete removed.
	if err := store.Delete(ctx, crewID, recap.Event.ID); err != nil {
		return fmt.Errorf("put: delete: %w", err)
	}
	if n, err := store.Put(ctx, crewID, rows); err != nil || n != 0 {
		return fmt.Errorf("put of a deleted id: inserted=%d err=%v, want 0", n, err)
	}

	// PutUnique: one row for a ref of a type.
	uniq := func(name, typ, ref string, ts int64) ledgerRow {
		return ledgerRow{Ref: ref, Event: CrewEvent{ID: id(name), Type: typ, ActorID: user, Timestamp: ts, Data: map[string]interface{}{"name": name}}}
	}
	ref := id("uref")
	held, err := store.PutUnique(ctx, crewID, uniq("u_first", "clip", ref, now-3*day))
	if err != nil || len(held) != 0 {
		return fmt.Errorf("put unique: held=%d err=%v", len(held), err)
	}
	held, err = store.PutUnique(ctx, crewID, uniq("u_second", "clip", ref, now-2*day))
	if err != nil || names(held) != "u_first" {
		return fmt.Errorf("put unique of a held ref: held=[%s] err=%v, want [u_first]", names(held), err)
	}
	if obj, _ := held[0].Data.(map[string]interface{}); obj["name"] != "u_first" || held[0].ActorID != user {
		return fmt.Errorf("put unique: the held row = %+v", held[0])
	}
	// The same ref of another type is free. The id of a held row is not.
	if held, err = store.PutUnique(ctx, crewID, uniq("u_stream", "stream_session", ref, now-2*day)); err != nil || len(held) != 0 {
		return fmt.Errorf("put unique of another type: held=%d err=%v", len(held), err)
	}
	if _, err = store.PutUnique(ctx, crewID, uniq("u_first", "clip", id("uref2"), now)); !errors.Is(err, errLedgerIDTaken) {
		return fmt.Errorf("put unique with a taken id: err=%v, want errLedgerIDTaken", err)
	}
	if _, err = store.PutUnique(ctx, crewID, uniq("u_noref", "clip", "", now)); err == nil {
		return errors.New("put unique with no ref: no error")
	}
	// Put does not check the ref: a legacy document can hold a clip_id twice.
	if n, err := store.Put(ctx, crewID, []ledgerRow{uniq("u_dup", "clip", ref, now-4*day)}); err != nil || n != 1 {
		return fmt.Errorf("put of a second row of a ref: inserted=%d err=%v", n, err)
	}
	if got, err = store.ByRef(ctx, crewID, "clip", ref); err != nil || names(got) != "u_dup,u_first" {
		return fmt.Errorf("by ref of a ref with two rows: [%s] err=%v, want [u_dup,u_first]", names(got), err)
	}
	// ByRefs: the rows of several refs of one type in one read.
	sortedNames := func(events []CrewEvent) string {
		out := strings.Split(names(events), ",")
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	manyRefs := []string{ref, clip.Ref, "", id("no_such_ref")}
	for i := 0; i < 2*ledgerByIDsChunk+5; i++ {
		manyRefs = append(manyRefs, fmt.Sprintf("missing_ref_%d_%s", i, run))
	}
	if got, err = store.ByRefs(ctx, crewID, "clip", manyRefs); err != nil || sortedNames(got) != "p_clip,u_dup,u_first" {
		return fmt.Errorf("by refs: [%s] err=%v, want [p_clip,u_dup,u_first]", sortedNames(got), err)
	}
	if got, err = store.ByRefs(ctx, crewID, "stream_session", []string{ref, stream.Ref}); err != nil || sortedNames(got) != "p_stream,u_stream" {
		return fmt.Errorf("by refs of another type: [%s] err=%v, want [p_stream,u_stream]", sortedNames(got), err)
	}
	if got, err = store.ByRefs(ctx, crewID, "weekly_recap", []string{""}); err != nil || len(got) != 0 {
		return fmt.Errorf("by refs with no ref: %d rows err=%v", len(got), err)
	}
	if got, err = store.ByRefs(ctx, crewID, "clip", nil); err != nil || len(got) != 0 {
		return fmt.Errorf("by refs of no refs: %d rows err=%v", len(got), err)
	}

	// RefsBefore: the rows of one type by ref, the highest ref first. The
	// refs are numbers of one width, as the week of a recap (recapWeekRef).
	weekRow := func(name string, week, ts int64) ledgerRow {
		return ledgerRow{Ref: recapWeekRef(week), Event: CrewEvent{ID: id(name), Type: "weekly_recap", Timestamp: ts, Score: 30,
			Data: map[string]interface{}{"week_start": week}}}
	}
	// The time of a row is not its week: w_9 is the oldest week and has the
	// newest time.
	byWeek := []ledgerRow{weekRow("w_100", 100, now-5*day), weekRow("w_9", 9, now-day), weekRow("w_1000", 1000, now-3*day), weekRow("w_20", 20, now-4*day)}
	if n, err := store.Put(ctx, crewID, byWeek); err != nil || n != 4 {
		return fmt.Errorf("refs before: put: inserted=%d err=%v, want 4", n, err)
	}
	for _, tc := range []struct {
		before string
		n      int
		want   string
	}{
		{"", 10, "w_1000,w_100,w_20,w_9"},
		{"", 2, "w_1000,w_100"},
		{recapWeekRef(100), 10, "w_20,w_9"},
		{recapWeekRef(101), 1, "w_100"},
		{recapWeekRef(9), 10, ""},
		{"", 0, ""},
	} {
		got, err = store.RefsBefore(ctx, crewID, "weekly_recap", tc.before, tc.n)
		if err != nil || names(got) != tc.want {
			return fmt.Errorf("refs before %q limit %d: [%s] err=%v, want [%s]", tc.before, tc.n, names(got), err, tc.want)
		}
	}
	// The recap of Put has no ref, and a ref of another type is not seen.
	if got, err = store.RefsBefore(ctx, crewID, "game_session", "", 10); err != nil || len(got) != 0 {
		return fmt.Errorf("refs before of a type with no ref: %d rows err=%v", len(got), err)
	}
	if got, err = store.NewestN(ctx, crewID, []string{"weekly_recap"}, now+1, 1); err != nil || names(got) != "w_9" {
		return fmt.Errorf("refs before: newest by time = [%s] err=%v, want [w_9]", names(got), err)
	}
	if err := store.Prune(ctx, crewID, []string{id("w_100"), id("w_9"), id("w_1000"), id("w_20")}); err != nil {
		return fmt.Errorf("refs before: prune: %w", err)
	}

	// TopInRange: the rows with a text value first, each group newest first,
	// the lower id first for one time.
	top := func(name, kind interface{}, ts int64) ledgerRow {
		data := map[string]interface{}{"n": name}
		if kind != nil {
			data["kind"] = kind
		}
		return ledgerRow{Event: CrewEvent{ID: id(name.(string)), Type: "moment", Timestamp: ts, Data: data}}
	}
	from := now - 300*day
	tops := []ledgerRow{
		top("t_plain_new", nil, from+50), top("t_game_old", "game", from+10), top("t_voice", "voice", from+40),
		top("t_game_b", "game", from+30), top("t_game_a", "game", from+30), top("t_number", 7, from+45),
		top("t_out", "game", from+101),
	}
	if n, err := store.Put(ctx, crewID, tops); err != nil || n != len(tops) {
		return fmt.Errorf("top in range: put: inserted=%d err=%v", n, err)
	}
	for _, tc := range []struct {
		value string
		n     int
		want  string
	}{
		{"game", 10, "t_game_a,t_game_b,t_game_old,t_plain_new,t_number,t_voice"},
		{"game", 4, "t_game_a,t_game_b,t_game_old,t_plain_new"},
		{"game", 2, "t_game_a,t_game_b"},
		{"7", 2, "t_plain_new,t_number"},
		{"none", 3, "t_plain_new,t_number,t_voice"},
		{"game", 0, ""},
	} {
		got, err = store.TopInRange(ctx, crewID, "moment", from, from+100, "kind", tc.value, tc.n)
		if err != nil || names(got) != tc.want {
			return fmt.Errorf("top in range of %q limit %d: [%s] err=%v, want [%s]", tc.value, tc.n, names(got), err, tc.want)
		}
	}
	if got, err = store.TopInRange(ctx, crewID, "clip", from, from+100, "kind", "game", 5); err != nil || len(got) != 0 {
		return fmt.Errorf("top in range of another type: %d rows err=%v", len(got), err)
	}
	topIDs := make([]string, 0, len(tops))
	for _, row := range tops {
		topIDs = append(topIDs, row.Event.ID)
	}
	if err := store.Prune(ctx, crewID, topIDs); err != nil {
		return fmt.Errorf("top in range: prune: %w", err)
	}

	// Eight calls at once for one new ref store one row.
	const racers = 8
	raceRef := id("race")
	stored := make([]bool, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			held, err := store.PutUnique(ctx, crewID, uniq(fmt.Sprintf("u_race%d", i), "clip", raceRef, now-int64(i)))
			stored[i], errs[i] = err == nil && len(held) == 0, err
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := range stored {
		if errs[i] != nil {
			return fmt.Errorf("put unique race: call %d: %w", i, errs[i])
		}
		if stored[i] {
			winners++
		}
	}
	if got, err = store.ByRef(ctx, crewID, "clip", raceRef); err != nil || len(got) != 1 || winners != 1 {
		return fmt.Errorf("put unique race: %d rows, %d calls stored a row, err=%v, want 1 and 1", len(got), winners, err)
	}

	// None of this moved the view.
	return view("at the end of put", "p_stream")
}
