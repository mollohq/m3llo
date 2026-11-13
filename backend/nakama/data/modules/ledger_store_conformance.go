package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	user := "selftest_user_" + run
	now := ledgerNow().UnixMilli()

	defer func() {
		for _, crewID := range []string{crewA, crewB} {
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
	n, err := store.Backfill(ctx, crewB, doc, anchor)
	if err != nil || n != 3 {
		return fmt.Errorf("backfill: inserted=%d err=%v, want 3", n, err)
	}
	if n, err = store.Backfill(ctx, crewB, doc, anchor); err != nil || n != 0 {
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
	if _, err = store.Backfill(ctx, crewB, nil, anchor-30*day); err != nil {
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

	// Crews do not see each other.
	if got, err = store.RecentView(ctx, crewA); err != nil {
		return fmt.Errorf("recent view of crew a: %w", err)
	} else if err := want("recent view of crew a at the end", got, "e1,e2,e3"); err != nil {
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
