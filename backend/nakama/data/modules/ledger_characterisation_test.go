package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Characterisation tests of the crew ledger as readers see it. They use only
// AppendCrewEvent and the readers, so they hold for any store behind them.
// Do not edit them to fit a new store.

const charDay = 24 * time.Hour

// setLedgerClock moves the clock of the ledger trim for one test.
func setLedgerClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := ledgerNow
	ledgerNow = func() time.Time { return now }
	t.Cleanup(func() { ledgerNow = prev })
}

func charEvent(id string, ts time.Time) CrewEvent {
	return CrewEvent{
		ID: id, CrewID: "crew", Type: "voice_session", Timestamp: ts.UnixMilli(), Score: 10,
		Data: VoiceSessionData{ChannelName: "General", ParticipantIDs: []string{"u1", "u2"}, ParticipantNames: []string{"Ann", "Bo"}, DurationMin: 30},
	}
}

func charAppend(t *testing.T, nk *fakeGuestNk, ev CrewEvent) {
	t.Helper()
	if err := AppendCrewEvent(context.Background(), nk, "crew", ev); err != nil {
		t.Fatalf("append %s: %v", ev.ID, err)
	}
}

func charLedgerIDs(nk *fakeGuestNk) []string {
	ledger, _ := readLedger(context.Background(), nk, "crew")
	ids := make([]string, 0, len(ledger.Events))
	for _, e := range ledger.Events {
		ids = append(ids, e.ID)
	}
	return ids
}

func charSameIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAppendCrewEventTrimsAtSevenDays(t *testing.T) {
	nk := newFakeGuestNk()
	now := time.Now()

	charAppend(t, nk, charEvent("a_six_days", now.Add(-6*charDay)))
	// An event that is already older than 7 days when it arrives is dropped
	// by its own append. A game session from the pending queue can be that old.
	charAppend(t, nk, charEvent("b_eight_days", now.Add(-8*charDay)))
	charAppend(t, nk, charEvent("c_now", now))

	if got := charLedgerIDs(nk); !charSameIDs(got, "a_six_days", "c_now") {
		t.Fatalf("ledger ids = %v, want [a_six_days c_now]", got)
	}

	// The data of an event reads back as a JSON object.
	ledger, _ := readLedger(context.Background(), nk, "crew")
	obj, ok := ledger.Events[0].Data.(map[string]interface{})
	if !ok {
		t.Fatalf("data type = %T, want a JSON object", ledger.Events[0].Data)
	}
	if obj["channel_name"] != "General" || obj["duration_min"] != float64(30) {
		t.Fatalf("data = %v", obj)
	}
	if e := ledger.Events[0]; e.Type != "voice_session" || e.Score != 10 || e.CrewID != "crew" {
		t.Fatalf("event = %+v", e)
	}
}

func TestLedgerTrimAnchorsToLastAppend(t *testing.T) {
	nk := newFakeGuestNk()
	t0 := time.Now().Add(-30 * charDay)

	setLedgerClock(t, t0)
	charAppend(t, nk, charEvent("a_old", t0.Add(-6*charDay)))
	charAppend(t, nk, charEvent("b_old", t0))

	// 30 days pass with no append. The read does not trim: the crew still
	// sees the last 7 days before its last append.
	if got := charLedgerIDs(nk); !charSameIDs(got, "a_old", "b_old") {
		t.Fatalf("dormant ledger ids = %v, want [a_old b_old]", got)
	}

	// An append 5 days later trims against the time of that append, also when
	// the new event carries an older timestamp.
	t1 := t0.Add(5 * charDay)
	setLedgerClock(t, t1)
	charAppend(t, nk, charEvent("c_backdated", t1.Add(-3*charDay)))
	if got := charLedgerIDs(nk); !charSameIDs(got, "b_old", "c_backdated") {
		t.Fatalf("ledger ids after a late append = %v, want [b_old c_backdated]", got)
	}

	// The next append is 10 days later. Everything before it is gone.
	t2 := t1.Add(10 * charDay)
	setLedgerClock(t, t2)
	charAppend(t, nk, charEvent("d_new", t2))
	if got := charLedgerIDs(nk); !charSameIDs(got, "d_new") {
		t.Fatalf("ledger ids after the last append = %v, want [d_new]", got)
	}
}

func TestMergedTimelineCutsClipsAtSevenDays(t *testing.T) {
	nk := newFakeGuestNk()
	now := time.Now()

	charAppend(t, nk, charEvent("session_recent", now.Add(-2*charDay)))
	charAppend(t, nk, charEvent("session_old", now.Add(-8*charDay)))
	nk.put(CrewClipsCollection, "crew", SystemUserID, CrewClipsDoc{CrewID: "crew", Clips: []StoredClip{
		{EventID: "clip_old", ClipID: "c1", ActorID: "u1", Ts: now.Add(-8 * charDay).UnixMilli()},
		{EventID: "clip_recent", ClipID: "c2", ActorID: "u1", Ts: now.Add(-6 * charDay).UnixMilli()},
	}})

	timeline := buildMergedTimeline(context.Background(), nk, "crew")
	got := make([]string, 0, len(timeline))
	for _, e := range timeline {
		got = append(got, e.Type+":"+e.ID)
	}
	if !charSameIDs(got, "voice_session:session_recent", "clip:clip_recent") {
		t.Fatalf("timeline = %v, want [voice_session:session_recent clip:clip_recent]", got)
	}
}

func TestCatchupIgnoresEventsOutsideLedger(t *testing.T) {
	nk := newFakeGuestNk()
	nk.addMember("crew", "u1", "Ann")
	now := time.Now()

	charAppend(t, nk, charEvent("session_recent", now.Add(-1*charDay)))
	charAppend(t, nk, charEvent("session_old", now.Add(-8*charDay)))

	out, err := CrewCatchupRPC(ctxWithUser("u1"), testLogger(), nil, nk, `{"crew_id":"crew"}`)
	if err != nil {
		t.Fatal(err)
	}
	var resp CatchupResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.HasEvents || resp.EventCount != 1 || len(resp.TopEvents) != 1 {
		t.Fatalf("catch-up = %+v, want exactly the recent session", resp)
	}
	if resp.TopEvents[0].Type != "voice_session" || resp.TopEvents[0].Ts != now.Add(-1*charDay).UnixMilli() {
		t.Fatalf("top event = %+v", resp.TopEvents[0])
	}
}
