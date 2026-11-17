package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Phase 2 of the durable ledger: the game sessions of a crew in the weeks of
// Crew history, read from the store.

// hGameSession appends a game session of `minutes` to the ledger of a crew,
// through the write path of production.
func hGameSession(t *testing.T, nk *fakeQuestsNk, crewID, id, actor string, ts int64, minutes int) {
	t.Helper()
	ev := CrewEvent{ID: id, CrewID: crewID, Type: "game_session", ActorID: actor, Timestamp: ts, Score: 20,
		Data: GameSessionData{GameName: "Counter-Strike 2", GameID: "counter-strike-2", DurationMin: minutes,
			PlayerIDs: []string{actor}, PlayerNames: []string{map[string]string{"u1": "Ann", "u2": "Bo"}[actor]}}}
	if err := AppendCrewEvent(context.Background(), nk, crewID, ev); err != nil {
		t.Fatalf("append game session %s: %v", id, err)
	}
}

// hVoiceSession appends a voice session to the ledger of a crew.
func hVoiceSession(t *testing.T, nk *fakeQuestsNk, crewID, id string, ts int64) {
	t.Helper()
	ev := CrewEvent{ID: id, CrewID: crewID, Type: "voice_session", ActorID: "u1", Timestamp: ts, Score: 10,
		Data: VoiceSessionData{DurationMin: 45}}
	if err := AppendCrewEvent(context.Background(), nk, crewID, ev); err != nil {
		t.Fatalf("append voice session %s: %v", id, err)
	}
}

// weekOf returns the week of Crew history that starts at weekStart.
func weekOf(z FeedZones, weekStart int64) *ZoneHistoryWeek {
	for i := range z.History {
		if z.History[i].WeekStart == weekStart {
			return &z.History[i]
		}
	}
	return nil
}

// A week of Crew history holds its game sessions. The viewer with m3llo+ gets
// the session cards, newest first, after the clips. The viewer without it
// gets the number only. A voice session does not make a week.
func TestHistoryWeeksHoldGameSessionsFromTheStore(t *testing.T) {
	useHistoryStoreReads(t)
	nk := hCrew(t, "gs")
	hGameSession(t, nk, "gs", "g_old", "u1", hAt(-3, 1, 20), 60)
	hGameSession(t, nk, "gs", "g_new", "u2", hAt(-3, 4, 20), 90)
	hGameSession(t, nk, "gs", "g_zero", "u2", hAt(-3, 5, 20), 0)
	hClip(t, nk, "gs", StoredClip{ClipID: "c1", ActorID: "u1", Ts: hAt(-3, 2, 0), DurationSeconds: 9})
	// A week with game sessions and nothing else.
	hGameSession(t, nk, "gs", "g_alone", "u1", hAt(-5, 2, 20), 30)
	// A week with a voice session and nothing else.
	hVoiceSession(t, nk, "gs", "v_alone", hAt(-7, 2, 20))

	// u2 has m3llo+.
	z := hZones(nk, "gs", "u2", 0, hNow)
	if len(z.History) != 2 || z.HistoryNext != 0 {
		t.Fatalf("history = %v, want the weeks -3 and -5", hWeeks(z.History))
	}
	w := weekOf(z, hAt(-3, 0, 0))
	if w == nil || w.Locked || w.ClipCount != 1 || w.SessionCount != 2 {
		t.Fatalf("week -3 = %+v, want open, one clip, two game sessions", w)
	}
	if got := hEntries(w.Items); got != "clip:ev_c1 session:g_new session:g_old" {
		t.Fatalf("items of week -3 = %s, want the clip, then the sessions newest first", got)
	}
	if d, ok := decodeGameSessionData(w.Items[1].Data); !ok || d.GameName != "Counter-Strike 2" || d.DurationMin != 90 {
		t.Fatalf("the session entry holds the game session: %+v", w.Items[1])
	}
	alone := weekOf(z, hAt(-5, 0, 0))
	if alone == nil || alone.SessionCount != 1 || alone.ClipCount != 0 || hEntries(alone.Items) != "session:g_alone" {
		t.Fatalf("week -5 = %+v, want a week of one game session", alone)
	}
	if len(z.Sessions) != 0 {
		t.Fatalf("the Sessions zone holds the free window only: %s", hEntries(z.Sessions))
	}

	// u1 has not.
	z = hZones(nk, "gs", "u1", 0, hNow)
	if len(z.History) != 2 {
		t.Fatalf("locked history = %v, want the weeks -3 and -5", hWeeks(z.History))
	}
	for _, w := range z.History {
		if !w.Locked || len(w.Items) != 0 || w.SessionCount == 0 {
			t.Fatalf("locked week = %+v, want the number of sessions and no item", w)
		}
		for _, teaser := range w.Teasers {
			if teaser.Kind != "voice" && teaser.Kind != "clip" && teaser.Kind != "stream" {
				t.Fatalf("a game session has no teaser: %+v", teaser)
			}
		}
	}
	raw, _ := json.Marshal(z.History)
	if strings.Contains(string(raw), "Counter-Strike") {
		t.Fatalf("a locked week names the game of a session: %s", raw)
	}
}

// Before the cutover and under each kill switch the documents are the truth.
// The ledger document holds 7 days, so no week of Crew history has a game
// session of the store.
func TestHistorySessionsFollowTheKillSwitches(t *testing.T) {
	for _, env := range []string{"MELLO_LEDGER_READ", "MELLO_LEDGER_HISTORY_READ"} {
		t.Run(env, func(t *testing.T) {
			useHistoryStoreReads(t)
			nk := hCrew(t, "ks")
			hGameSession(t, nk, "ks", "g1", "u1", hAt(-3, 1, 20), 60)
			hClip(t, nk, "ks", StoredClip{ClipID: "c1", ActorID: "u1", Ts: hAt(-4, 2, 0), DurationSeconds: 9})
			if z := hZones(nk, "ks", "u2", 0, hNow); len(z.History) != 2 {
				t.Fatalf("from the store: history = %v, want two weeks", hWeeks(z.History))
			}
			t.Setenv(env, "doc")
			z := hZones(nk, "ks", "u2", 0, hNow)
			if len(z.History) != 1 || z.History[0].SessionCount != 0 || z.History[0].ClipCount != 1 {
				t.Fatalf("under %s=doc: history = %v, want the clip week only", env, hWeeks(z.History))
			}
		})
	}
}

// The ledger document holds 7 days. Under a free window shorter than that, a
// game session of the document that is older than the window is in its week
// of Crew history. A voice session is not.
func TestHistoryTakesTheGameSessionCardsOlderThanTheFreeWindow(t *testing.T) {
	in := zInput()
	in.FreeDays = 3
	in.Plus = true
	voice := feedCard{id: "v", feedType: "session", backendType: "voice_session", ts: zonesNow - 5*dayMs, data: VoiceSessionData{DurationMin: 30}}
	in.Cards = []feedCard{zGameSession("in-window", 1, "Ann"), zGameSession("older", 5, "Ann"), voice}
	z := buildFeedZones(in)
	if got := entryIDs(z.Sessions); len(got) != 1 || got[0] != "in-window" {
		t.Fatalf("sessions = %v, want the one of the free window", got)
	}
	if len(z.History) != 1 || z.History[0].SessionCount != 1 || hEntries(z.History[0].Items) != "session:older" {
		t.Fatalf("history = %+v, want one week with the older game session", z.History)
	}
	in.Plus = false
	z = buildFeedZones(in)
	if w := z.History[0]; !w.Locked || w.SessionCount != 1 || len(w.Items) != 0 {
		t.Fatalf("locked week = %+v", w)
	}
}

// A week lists at most zonesWeekSessionsMax game sessions, the newest. Its
// count holds every one. A week over zonesWeekSessionsReadMax does not load
// its rows.
func TestHistoryWeekListsTheNewestGameSessions(t *testing.T) {
	store := useHistoryStoreReads(t)
	nk := hCrew(t, "many")
	some, many := zonesWeekSessionsMax+8, zonesWeekSessionsReadMax+12
	for i := 0; i < some; i++ {
		hGameSession(t, nk, "many", fmt.Sprintf("a%03d", i), "u1", hAt(-2, 0, 1)+int64(i)*hHour, 30)
	}
	for i := 0; i < many; i++ {
		hGameSession(t, nk, "many", fmt.Sprintf("b%03d", i), "u1", hAt(-4, 0, 1)+int64(i)*hHour, 30)
	}
	counter := newQueryCounter(store)
	useLedger(t, counter)
	z := hZones(nk, "many", "u2", 0, hNow)
	_, rows := counter.total()
	if rows > some+zonesWeekSessionsMax {
		t.Fatalf("the page read %d rows, want at most %d: %s", rows, some+zonesWeekSessionsMax, counter)
	}
	for _, c := range []struct {
		week   int
		n      int
		newest string
	}{{-2, some, fmt.Sprintf("a%03d", some-1)}, {-4, many, fmt.Sprintf("b%03d", many-1)}} {
		w := weekOf(z, hAt(c.week, 0, 0))
		if w == nil || w.SessionCount != c.n || len(w.Items) != zonesWeekSessionsMax || w.Items[0].ID != c.newest {
			t.Fatalf("week %d = %+v, want %d sessions, %d listed, %s first", c.week, w, c.n, zonesWeekSessionsMax, c.newest)
		}
	}
	// The viewer without m3llo+ reads no game session row.
	counter.reset()
	z = hZones(nk, "many", "u1", 0, hNow)
	if _, rows := counter.total(); rows != 0 {
		t.Fatalf("the locked page read %d rows: %s", rows, counter)
	}
	if w := weekOf(z, hAt(-4, 0, 0)); w == nil || !w.Locked || w.SessionCount != many {
		t.Fatalf("locked week -4 = %+v", w)
	}
}

// Weeks that hold only game sessions page as the other weeks: four to a
// page, and every week once.
func TestHistoryPagesWeeksOfGameSessions(t *testing.T) {
	useHistoryStoreReads(t)
	nk := hCrew(t, "pg")
	for week := -2; week >= -11; week-- {
		hGameSession(t, nk, "pg", fmt.Sprintf("g%d", -week), "u1", hAt(week, 3, 20), 40)
	}
	for _, viewer := range []string{"u1", "u2"} {
		seen := map[int64]int{}
		pages := 0
		for before := int64(0); ; pages++ {
			z := hZones(nk, "pg", viewer, before, hNow)
			for _, w := range z.History {
				seen[w.WeekStart]++
				if w.SessionCount != 1 {
					t.Fatalf("viewer %s: week %+v, want one session", viewer, w)
				}
			}
			if z.HistoryNext == 0 {
				break
			}
			if len(z.History) != zonesHistoryPageWeeks {
				t.Fatalf("viewer %s: a page of %d weeks with a next page", viewer, len(z.History))
			}
			before = z.HistoryNext
		}
		if pages != 2 || len(seen) != 10 {
			t.Fatalf("viewer %s: %d next pages and %d weeks, want 2 and 10", viewer, pages, len(seen))
		}
		for ws, n := range seen {
			if n != 1 {
				t.Fatalf("viewer %s: week %d is on %d pages", viewer, ws, n)
			}
		}
	}
}

// Through the RPC: a game session of the free window is in the Sessions zone
// and not in Crew history. An older one is in its week and not in Sessions.
func TestCrewFeedSplitsGameSessionsAtTheFreeWindow(t *testing.T) {
	useHistoryStoreReads(t)
	nk := hCrew(t, "rpc")
	now := time.Now().UnixMilli()
	hGameSession(t, nk, "rpc", "g_week", "u1", now-2*hDay, 60)
	hGameSession(t, nk, "rpc", "g_past", "u1", now-20*hDay, 60)
	out, err := CrewFeedRPC(ctxWithUser("u2"), testLogger(), nil, nk, `{"crew_id":"rpc"}`)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Zones FeedZones `json:"zones"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	z := resp.Zones
	if hEntries(z.Sessions) != "session:g_week" {
		t.Fatalf("sessions = %s", hEntries(z.Sessions))
	}
	if len(z.History) != 1 || hEntries(z.History[0].Items) != "session:g_past" || z.History[0].SessionCount != 1 {
		t.Fatalf("history = %v", hWeeks(z.History))
	}
}

// The store reads of one crew_feed call for a crew with game sessions, on
// top of the crew of TestCrewFeedLoadQueryCount.
//
// A week of up to zonesWeekSessionsReadMax game sessions adds no read: the
// sessions come with the clips of the week. A week with more adds one read
// for a viewer with m3llo+ (the newest zonesWeekSessionsMax) and none for a
// viewer without it.
func TestCrewFeedLoadQueryCountWithGameSessions(t *testing.T) {
	for _, c := range []struct {
		name        string
		everyHours  int64
		plusQueries int
		plusRows    int
	}{
		// About 24 game sessions a week.
		{"sessions_with_the_clips", 7, 15, 400},
		// About 84 a week: one read more for each of the W = 5 weeks, less
		// the week the free window starts in on the first page.
		{"sessions_over_the_read_limit", 2, 20, 350},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := useHistoryStoreReads(t)
			nk := hCrew(t, "big")
			now := time.Now().UnixMilli()
			seedLargeCrewRows(t, store, "big", now)
			for i := int64(0); i*c.everyHours < 120*7*24; i++ {
				ev := CrewEvent{ID: fmt.Sprintf("g%05d", i), CrewID: "big", Type: "game_session", ActorID: "u1", Timestamp: now - i*c.everyHours*hHour,
					Data: GameSessionData{GameName: "CS2", DurationMin: 30, PlayerIDs: []string{"u1"}, PlayerNames: []string{"Ann"}}}
				if _, err := store.Append(context.Background(), ev, ""); err != nil {
					t.Fatal(err)
				}
			}
			counter := newQueryCounter(store)
			useLedger(t, counter)
			deep := weekStartOf(now) - 52*7*hDay
			for _, viewer := range []string{"u2", "u1"} {
				counter.reset()
				out, err := CrewFeedRPC(ctxWithUser(viewer), testLogger(), nil, nk, fmt.Sprintf(`{"crew_id":"big","history_before":%d}`, deep))
				if err != nil {
					t.Fatal(err)
				}
				var resp struct {
					Zones FeedZones `json:"zones"`
				}
				if err := json.Unmarshal([]byte(out), &resp); err != nil {
					t.Fatal(err)
				}
				queries, rows := counter.total()
				t.Logf("viewer %s: %d rows, %d queries: %s", viewer, rows, queries, counter)
				w := resp.Zones.History[1]
				if len(resp.Zones.History) != zonesHistoryPageWeeks || w.SessionCount < 20 {
					t.Fatalf("viewer %s: %d weeks, second week %+v", viewer, len(resp.Zones.History), w)
				}
				if viewer == "u2" {
					if queries != c.plusQueries || rows > c.plusRows+7*24/int(c.everyHours)+1 {
						t.Fatalf("m3llo+: %d queries and %d rows, want %d queries: %s", queries, rows, c.plusQueries, counter)
					}
					continue
				}
				if queries != 20 || w.Locked == false || len(w.Items) != 0 {
					t.Fatalf("locked: %d queries, want 20, second week %+v: %s", queries, w, counter)
				}
			}
		})
	}
}
