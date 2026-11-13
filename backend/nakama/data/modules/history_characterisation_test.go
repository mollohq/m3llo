package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Characterisation of the read paths that phase 1b of the durable ledger
// moves (plans/DURABLE-LEDGER.md): the clips, the stream sessions and the
// weekly recaps of a crew, as the crew feed, the catch-up, the guest feed,
// the invite preview, the clip RPCs and the recap job see them.
//
// Each test seeds a crew with the write paths of production, then reads. It
// runs once for each mode of history_modes_test.go. This file does not
// change when the readers move.

const (
	hHour = int64(60 * 60 * 1000)
	hDay  = 24 * hHour
)

// hNow is the "now" of the zone tests: Thursday 2026-10-15 12:00 UTC. The free
// window of 7 days starts on Thursday 2026-10-08 12:00.
var hNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).UnixMilli()

// hAt is a time in the week that is `week` weeks from the week of hNow: day 0
// is Monday.
func hAt(week, day, hour int) int64 {
	return weekStartOf(hNow) + int64(week)*7*hDay + int64(day)*hDay + int64(hour)*hHour
}

func hJSON(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// hCrew is a crew of Ann (u1, no m3llo+) and Bo (u2, m3llo+).
func hCrew(t *testing.T, crewID string) *fakeQuestsNk {
	t.Helper()
	nk := newFakeQuestsNk()
	nk.addMember(crewID, "u1", "Ann")
	nk.addMember(crewID, "u2", "Bo")
	nk.put(UserPlusCollection, "u2", SystemUserID, UserPlus{UntilMs: time.Now().Add(24 * time.Hour).UnixMilli(), Source: "test"})
	return nk
}

func hClip(t *testing.T, nk runtime.NakamaModule, crewID string, c StoredClip) StoredClip {
	t.Helper()
	c.EventID = "ev_" + c.ClipID
	c.Score = 50
	if c.ClipType == "" {
		c.ClipType = clipTypeVoice
	}
	if c.ClipperName == "" {
		c.ClipperName = map[string]string{"u1": "Ann", "u2": "Bo"}[c.ActorID]
	}
	stored, err := AppendClip(context.Background(), nk, crewID, c)
	if err != nil || stored != nil {
		t.Fatalf("append clip %s: stored=%+v err=%v", c.ClipID, stored, err)
	}
	return c
}

// hStream ends a stream the way stop_stream does: the stream_session event of
// the ledger, then the durable copy.
func hStream(t *testing.T, nk runtime.NakamaModule, crewID, id, streamerID string, ts int64, durationMin int, snapshots ...string) StoredStreamSession {
	t.Helper()
	if snapshots == nil {
		snapshots = []string{}
	}
	data := StreamSessionData{
		SessionID:    "sess_" + id,
		StreamerID:   streamerID,
		StreamerName: map[string]string{"u1": "Ann", "u2": "Bo"}[streamerID],
		Title:        "Stream " + id,
		DurationMin:  durationMin,
		PeakViewers:  2,
		ViewerIDs:    []string{"u1", "u2"},
		SnapshotURLs: snapshots,
	}
	ev := CrewEvent{ID: "ev_" + id, CrewID: crewID, Type: "stream_session", ActorID: streamerID, Timestamp: ts, Score: 30, Data: data}
	if err := AppendCrewEvent(context.Background(), nk, crewID, ev); err != nil {
		t.Fatalf("append stream %s: %v", id, err)
	}
	stored := storedStreamSessionFrom(ev.ID, ev.Timestamp, ev.Score, data)
	if err := UpsertStreamSession(context.Background(), nk, crewID, stored); err != nil {
		t.Fatalf("upsert stream %s: %v", id, err)
	}
	return stored
}

func hRecap(t *testing.T, nk runtime.NakamaModule, crewID string, r WeeklyRecapData) WeeklyRecapData {
	t.Helper()
	r.CrewID = crewID
	r.WeekEnd = r.WeekStart + 7*hDay
	if r.GeneratedAt == 0 {
		r.GeneratedAt = r.WeekEnd + 5*60*1000
	}
	if err := AppendRecap(context.Background(), nk, crewID, r); err != nil {
		t.Fatalf("append recap: %v", err)
	}
	return r
}

func hZones(nk runtime.NakamaModule, crewID, viewer string, historyBefore, nowMs int64) FeedZones {
	return crewFeedZones(context.Background(), testLogger(), nk,
		FeedRequest{CrewID: crewID, HistoryBefore: historyBefore}, viewer, nil, nil, nil, nowMs)
}

func hEntries(entries []FeedEntry) string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Type+":"+e.ID)
	}
	return strings.Join(out, " ")
}

// hWeeks is one line for each week of Crew history.
func hWeeks(weeks []ZoneHistoryWeek) []string {
	out := make([]string, 0, len(weeks))
	for _, w := range weeks {
		recap := "-"
		if w.Recap != nil {
			recap = w.Recap.Type + ":" + w.Recap.ID
		}
		teasers := make([]string, 0, len(w.Teasers))
		for _, s := range w.Teasers {
			teasers = append(teasers, fmt.Sprintf("%s/%s/%s/%.0f", s.Kind, s.ActorID, s.ActorName, s.DurationSeconds))
		}
		out = append(out, fmt.Sprintf("week%+d number=%d locked=%t clips=%d streams=%d recap=%s items=[%s] teasers=[%s]",
			(w.WeekStart-weekStartOf(hNow))/(7*hDay), w.WeekNumber, w.Locked, w.ClipCount, w.StreamCount,
			recap, hEntries(w.Items), strings.Join(teasers, " ")))
	}
	return out
}

func hSameLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n got:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// hSeedHistoryCrew seeds the crew of the zone tests. It returns what the
// tests compare payloads with.
type hSeeded struct {
	stageClip   StoredClip
	boundary    StoredStreamSession
	boardRecap  WeeklyRecapData
	week2Recap  WeeklyRecapData
	week4Recap  WeeklyRecapData
	windowClips []StoredClip
}

func hSeedHistoryCrew(t *testing.T, nk *fakeQuestsNk) hSeeded {
	t.Helper()
	const crew = "hc"
	// The crew was made in week -10, so week -2 is its week 9.
	nk.groups[crew] = &api.Group{Id: crew, CreateTime: timestamppb.New(time.UnixMilli(hAt(-10, 2, 0)))}
	var s hSeeded

	// The free window.
	cv1 := hClip(t, nk, crew, StoredClip{ClipID: "cv1", ActorID: "u1", Ts: hAt(0, 2, 10), DurationSeconds: 12,
		Participants: []string{"u1", "u2"}, ParticipantNames: []string{"Ann", "Bo"}, Waveform: "d2F2ZQ=="})
	s.stageClip = hClip(t, nk, crew, StoredClip{ClipID: "cg1", ActorID: "u2", ClipType: clipTypeGame, Ts: hAt(0, 1, 9), DurationSeconds: 30,
		Game: "CS2", Width: 1920, Height: 1080, HasVoice: true, PosterURL: "https://m.test/cg1.jpg", MediaURL: "https://m.test/cg1.mp4"})
	cg2 := hClip(t, nk, crew, StoredClip{ClipID: "cg2", ActorID: "u1", ClipType: clipTypeGame, Ts: hAt(-1, 4, 9), DurationSeconds: 20, Width: 1280, Height: 720})
	s.windowClips = []StoredClip{cg2, cv1}
	hStream(t, nk, crew, "s1", "u2", hAt(0, 0, 20), 20, "https://m.test/s1/1.jpg", "https://m.test/s1/2.jpg", "https://m.test/s1/3.jpg")
	hStream(t, nk, crew, "s2", "u1", hAt(-1, 5, 10), 5)

	// Week -1 before the window start: Crew history.
	hClip(t, nk, crew, StoredClip{ClipID: "cb", ActorID: "u2", Ts: hAt(-1, 1, 10), DurationSeconds: 8})
	s.boundary = hStream(t, nk, crew, "sb", "u1", hAt(-1, 2, 10), 40, "https://m.test/sb/1.jpg", "https://m.test/sb/2.jpg")

	// Week -2: eight clips (k3 and k6 are game clips), one stream, a recap.
	for i := 1; i <= 8; i++ {
		c := StoredClip{ClipID: fmt.Sprintf("k%d", i), ActorID: "u1", Ts: hAt(-2, 0, i), DurationSeconds: float64(i)}
		if i == 3 || i == 6 {
			c.ClipType, c.ActorID, c.Width, c.Height = clipTypeGame, "u2", 1280, 720
		}
		hClip(t, nk, crew, c)
	}
	hStream(t, nk, crew, "s3", "u2", hAt(-2, 3, 0), 15)

	// Recaps, appended oldest first as the job does. Week -3 is empty.
	s.week4Recap = hRecap(t, nk, crew, WeeklyRecapData{WeekStart: hAt(-4, 0, 0), TotalGameMin: 30, TotalMin: 30, TopGame: "CS2"})
	hRecap(t, nk, crew, WeeklyRecapData{WeekStart: hAt(-3, 0, 0)})
	s.week2Recap = hRecap(t, nk, crew, WeeklyRecapData{WeekStart: hAt(-2, 0, 0), TotalHangoutMin: 50, TotalMin: 50, ClipCount: 8, MostClipped: "Ann"})
	s.boardRecap = hRecap(t, nk, crew, WeeklyRecapData{WeekStart: hAt(-1, 0, 0), TotalHangoutMin: 100, TotalMin: 100, SessionCount: 3})

	// Older weeks: a stream only, a game clip only, a voice clip only.
	hStream(t, nk, crew, "s5", "u1", hAt(-5, 2, 0), 25, "https://m.test/s5/1.jpg")
	hClip(t, nk, crew, StoredClip{ClipID: "c6", ActorID: "u2", ClipType: clipTypeGame, Ts: hAt(-6, 1, 0), DurationSeconds: 45, Width: 1280, Height: 720})
	hClip(t, nk, crew, StoredClip{ClipID: "c8", ActorID: "u1", Ts: hAt(-8, 6, 23), DurationSeconds: 5})
	return s
}

// hStamped is a recap as a reader returns it: with week_start_ms and the
// week number of the crew.
func hStamped(r WeeklyRecapData, weekNumber int) WeeklyRecapData {
	r.WeekStartMs = r.WeekStart
	r.WeekNumber = weekNumber
	return r
}

func TestCharZonesFreeWindow(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hc")
		s := hSeedHistoryCrew(t, nk)
		m.ready(t, nk)

		z := hZones(nk, "hc", "u1", 0, hNow)
		if z.FreeDays != 7 || z.Plus {
			t.Fatalf("free_days=%d plus=%t", z.FreeDays, z.Plus)
		}
		// The Stage is the newest game clip with a poster, with the stored clip
		// as its payload.
		if z.Stage.Kind != "game_clip" || z.Stage.Entry == nil || z.Stage.Entry.ID != "ev_cg1" || z.Stage.Entry.Type != "clip" || z.Stage.Entry.Ts != s.stageClip.Ts {
			t.Fatalf("stage = %+v", z.Stage)
		}
		if got, want := hJSON(t, z.Stage.Entry.Data), hJSON(t, s.stageClip); got != want {
			t.Fatalf("stage payload:\n got %s\nwant %s", got, want)
		}
		// Every other clip of the window: game clips first, then newest first.
		if got := hEntries(z.Clips); got != "clip:ev_cg2 clip:ev_cv1" {
			t.Fatalf("clips = %s", got)
		}
		if got, want := hJSON(t, []interface{}{z.Clips[0].Data, z.Clips[1].Data}), hJSON(t, s.windowClips); got != want {
			t.Fatalf("clip payloads:\n got %s\nwant %s", got, want)
		}
		// The board: the newest recap that is not empty, the newest clip or
		// stream as the last activity.
		if z.Board.Mode != "tabs" || z.Board.LastActivityMs != hAt(0, 2, 10) {
			t.Fatalf("board = %+v", z.Board)
		}
		if z.Board.Recap == nil || z.Board.Recap.ID != recapEntryID(hAt(-1, 0, 0)) || z.Board.Recap.Type != "recap" || z.Board.Recap.Ts != s.boardRecap.GeneratedAt {
			t.Fatalf("board recap = %+v", z.Board.Recap)
		}
		if got, want := hJSON(t, z.Board.Recap.Data), hJSON(t, hStamped(s.boardRecap, 10)); got != want {
			t.Fatalf("board recap payload:\n got %s\nwant %s", got, want)
		}
	})
}

func TestCharZonesStreamOnStage(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hs")
		// No game clip with a poster: the best stream replay of the window.
		hClip(t, nk, "hs", StoredClip{ClipID: "v", ActorID: "u1", Ts: hAt(0, 1, 0), DurationSeconds: 9})
		best := hStream(t, nk, "hs", "long", "u2", hAt(0, 0, 8), 30, "https://m.test/a.jpg", "https://m.test/b.jpg", "https://m.test/c.jpg")
		hStream(t, nk, "hs", "short", "u1", hAt(0, 2, 8), 10, "https://m.test/d.jpg")
		hStream(t, nk, "hs", "bare", "u1", hAt(0, 3, 8), 50)
		// A better replay that is older than the window does not count.
		hStream(t, nk, "hs", "old", "u1", hAt(-2, 0, 8), 90, "https://m.test/e.jpg", "https://m.test/f.jpg")
		m.ready(t, nk)

		z := hZones(nk, "hs", "u1", 0, hNow)
		if z.Stage.Kind != "stream_replay" || z.Stage.Entry == nil || z.Stage.Entry.ID != "ev_long" || z.Stage.Entry.Type != "session-preview" {
			t.Fatalf("stage = %+v", z.Stage)
		}
		if got, want := hJSON(t, z.Stage.Entry.Data), hJSON(t, best); got != want {
			t.Fatalf("stage payload:\n got %s\nwant %s", got, want)
		}
		if got := hEntries(z.Clips); got != "clip:ev_v" {
			t.Fatalf("clips = %s", got)
		}
		// The newest stream of the crew has no snapshot: it is the last
		// activity all the same.
		if z.Board.LastActivityMs != hAt(0, 3, 8) {
			t.Fatalf("last activity = %d, want %d", z.Board.LastActivityMs, hAt(0, 3, 8))
		}
	})
}

func TestCharZonesHistoryLocked(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hc")
		hSeedHistoryCrew(t, nk)
		m.ready(t, nk)

		// Ann has no m3llo+: counts and teasers, no item, no recap, no URL.
		z := hZones(nk, "hc", "u1", 0, hNow)
		hSameLines(t, "page 1", hWeeks(z.History), []string{
			"week-1 number=0 locked=true clips=1 streams=1 recap=- items=[] teasers=[voice/u2/Bo/8 stream/u1/Ann/2400]",
			"week-2 number=9 locked=true clips=8 streams=1 recap=- items=[] teasers=[clip/u2/Bo/6 clip/u2/Bo/3 voice/u1/Ann/8 voice/u1/Ann/7 voice/u1/Ann/5 voice/u1/Ann/4]",
			"week-4 number=7 locked=true clips=0 streams=0 recap=- items=[] teasers=[]",
			"week-5 number=0 locked=true clips=0 streams=1 recap=- items=[] teasers=[stream/u1/Ann/1500]",
		})
		if z.HistoryNext != hAt(-5, 0, 0) {
			t.Fatalf("history_next = %d, want %d", z.HistoryNext, hAt(-5, 0, 0))
		}
		if raw := hJSON(t, z.History); strings.Contains(raw, "https://") {
			t.Fatalf("a locked week holds a URL: %s", raw)
		}

		z = hZones(nk, "hc", "u1", z.HistoryNext, hNow)
		hSameLines(t, "page 2", hWeeks(z.History), []string{
			"week-6 number=0 locked=true clips=1 streams=0 recap=- items=[] teasers=[clip/u2/Bo/45]",
			"week-8 number=0 locked=true clips=1 streams=0 recap=- items=[] teasers=[voice/u1/Ann/5]",
		})
		if z.HistoryNext != 0 {
			t.Fatalf("history_next of the last page = %d", z.HistoryNext)
		}
		// A history page keeps the zones of the first page.
		if z.Stage.Kind != "game_clip" || hEntries(z.Clips) != "clip:ev_cg2 clip:ev_cv1" || z.Board.Recap == nil || z.Board.LastActivityMs != hAt(0, 2, 10) {
			t.Fatalf("zones of page 2: stage=%+v clips=%s board=%+v", z.Stage, hEntries(z.Clips), z.Board)
		}
	})
}

func TestCharZonesHistoryOpen(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hc")
		s := hSeedHistoryCrew(t, nk)
		m.ready(t, nk)

		// Bo has m3llo+: the items and the recap of each week.
		z := hZones(nk, "hc", "u2", 0, hNow)
		if !z.Plus {
			t.Fatal("Bo has m3llo+")
		}
		recap2, recap4 := "recap:"+recapEntryID(hAt(-2, 0, 0)), "recap:"+recapEntryID(hAt(-4, 0, 0))
		hSameLines(t, "page 1", hWeeks(z.History), []string{
			"week-1 number=0 locked=false clips=1 streams=1 recap=- items=[clip:ev_cb session-preview:ev_sb] teasers=[]",
			"week-2 number=9 locked=false clips=8 streams=1 recap=" + recap2 + " items=[clip:ev_k6 clip:ev_k3 clip:ev_k8 clip:ev_k7 clip:ev_k5 clip:ev_k4 clip:ev_k2 clip:ev_k1 session:ev_s3] teasers=[]",
			"week-4 number=7 locked=false clips=0 streams=0 recap=" + recap4 + " items=[] teasers=[]",
			"week-5 number=0 locked=false clips=0 streams=1 recap=- items=[session-preview:ev_s5] teasers=[]",
		})
		if z.HistoryNext != hAt(-5, 0, 0) {
			t.Fatalf("history_next = %d", z.HistoryNext)
		}
		// The payloads are the stored stream session and the stamped recap.
		if got, want := hJSON(t, z.History[0].Items[1].Data), hJSON(t, s.boundary); got != want {
			t.Fatalf("stream payload:\n got %s\nwant %s", got, want)
		}
		if got, want := hJSON(t, z.History[1].Recap.Data), hJSON(t, hStamped(s.week2Recap, 9)); got != want {
			t.Fatalf("recap payload:\n got %s\nwant %s", got, want)
		}
		if z.History[2].Recap.Ts != s.week4Recap.GeneratedAt {
			t.Fatalf("recap ts = %d, want %d", z.History[2].Recap.Ts, s.week4Recap.GeneratedAt)
		}

		z = hZones(nk, "hc", "u2", z.HistoryNext, hNow)
		hSameLines(t, "page 2", hWeeks(z.History), []string{
			"week-6 number=0 locked=false clips=1 streams=0 recap=- items=[clip:ev_c6] teasers=[]",
			"week-8 number=0 locked=false clips=1 streams=0 recap=- items=[clip:ev_c8] teasers=[]",
		})
		if z.HistoryNext != 0 {
			t.Fatalf("history_next of the last page = %d", z.HistoryNext)
		}
	})
}

func TestCharZonesBoardOfAQuietAndADormantCrew(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hq")
		// Quiet: a clip three days ago, no stream or recap this week.
		hClip(t, nk, "hq", StoredClip{ClipID: "q", ActorID: "u1", Ts: hNow - 3*hDay, DurationSeconds: 9})
		// Dormant: the last clip is 20 days old, the last stream 30 days.
		hClip(t, nk, "hd", StoredClip{ClipID: "d", ActorID: "u1", Ts: hNow - 20*hDay, DurationSeconds: 9})
		hStream(t, nk, "hd", "d", "u1", hNow-30*hDay, 30, "https://m.test/d.jpg")
		// A recap of an old week is not activity. Two empty recaps are newer.
		old := hRecap(t, nk, "hd", WeeklyRecapData{WeekStart: hAt(-6, 0, 0), TotalHangoutMin: 20, TotalMin: 20})
		hRecap(t, nk, "hd", WeeklyRecapData{WeekStart: hAt(-5, 0, 0)})
		hRecap(t, nk, "hd", WeeklyRecapData{WeekStart: hAt(-4, 0, 0)})
		m.ready(t, nk)

		z := hZones(nk, "hq", "u1", 0, hNow)
		if z.Board.Mode != "quiet" || z.Board.LastActivityMs != hNow-3*hDay || z.Board.Recap != nil || z.Stage.Kind != "" {
			t.Fatalf("quiet crew: board=%+v stage=%+v", z.Board, z.Stage)
		}
		if hEntries(z.Clips) != "clip:ev_q" || len(z.History) != 0 || z.HistoryNext != 0 {
			t.Fatalf("quiet crew: clips=%s history=%v", hEntries(z.Clips), hWeeks(z.History))
		}

		z = hZones(nk, "hd", "u2", 0, hNow)
		if z.Board.Mode != "past" || z.Board.LastActivityMs != hNow-20*hDay || z.Stage.Kind != "" || len(z.Clips) != 0 {
			t.Fatalf("dormant crew: board=%+v stage=%+v clips=%s", z.Board, z.Stage, hEntries(z.Clips))
		}
		// The board shows the newest recap that is not empty. It is then not
		// a week of Crew history.
		if z.Board.Recap == nil || z.Board.Recap.ID != recapEntryID(old.WeekStart) {
			t.Fatalf("dormant crew: board recap = %+v", z.Board.Recap)
		}
		if got := len(z.History); got != 2 || z.History[0].ClipCount != 1 || z.History[1].StreamCount != 1 || z.History[0].Recap != nil || z.History[1].Recap != nil {
			t.Fatalf("dormant crew: history = %v", hWeeks(z.History))
		}
	})
}

// A crew at the old caps: 250 clips and 150 stream sessions, all in Crew
// history. Every one is in a week of the pages.
func TestCharZonesAtTheOldCaps(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hx")
		for i := 0; i < CrewClipsMaxRetained; i++ {
			hClip(t, nk, "hx", StoredClip{ClipID: fmt.Sprintf("c%03d", i), ActorID: "u1", Ts: hNow - 8*hDay - int64(i)*7*hHour, DurationSeconds: 5})
		}
		for i := 0; i < CrewStreamSessionsMaxRetained; i++ {
			hStream(t, nk, "hx", fmt.Sprintf("s%03d", i), "u2", hNow-8*hDay-int64(i)*11*hHour, 10)
		}
		m.ready(t, nk)

		clips, streams, items, pages := 0, 0, 0, 0
		seen := map[string]bool{}
		before := int64(0)
		for {
			z := hZones(nk, "hx", "u2", before, hNow)
			pages++
			if len(z.History) == 0 || len(z.History) > zonesHistoryPageWeeks || len(z.Clips) != 0 {
				t.Fatalf("page %d: %d weeks, %d window clips", pages, len(z.History), len(z.Clips))
			}
			for _, w := range z.History {
				if w.ClipCount+w.StreamCount != len(w.Items) {
					t.Fatalf("week %d: counts %d+%d, %d items", w.WeekStart, w.ClipCount, w.StreamCount, len(w.Items))
				}
				clips += w.ClipCount
				streams += w.StreamCount
				for _, it := range w.Items {
					if seen[it.ID] || weekStartOf(it.Ts) != w.WeekStart {
						t.Fatalf("item %s of week %d: twice or in the wrong week", it.ID, w.WeekStart)
					}
					seen[it.ID] = true
					items++
				}
			}
			if z.HistoryNext == 0 {
				break
			}
			if before != 0 && z.HistoryNext >= before {
				t.Fatalf("history_next does not go back: %d after %d", z.HistoryNext, before)
			}
			before = z.HistoryNext
		}
		if clips != CrewClipsMaxRetained || streams != CrewStreamSessionsMaxRetained || items != clips+streams {
			t.Fatalf("history holds %d clips and %d streams in %d items, want %d and %d", clips, streams, items, CrewClipsMaxRetained, CrewStreamSessionsMaxRetained)
		}
		// 250 clips over 7 hours each are 73 days: more than two pages.
		if pages < 3 {
			t.Fatalf("%d pages", pages)
		}
	})
}

func TestCharMergedTimeline(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "ht")
		now := time.Now().UnixMilli()
		recent := hClip(t, nk, "ht", StoredClip{ClipID: "recent", ActorID: "u1", Ts: now - 6*hDay, DurationSeconds: 9, MediaURL: "https://m.test/r.mp4"})
		hClip(t, nk, "ht", StoredClip{ClipID: "old", ActorID: "u1", Ts: now - 8*hDay, DurationSeconds: 9})
		// The timeline holds one recap: the one of the newest week.
		hRecap(t, nk, "ht", WeeklyRecapData{WeekStart: weekStartOf(now) - 14*hDay, TotalHangoutMin: 10, TotalMin: 10})
		latest := hRecap(t, nk, "ht", WeeklyRecapData{WeekStart: weekStartOf(now) - 7*hDay, TotalHangoutMin: 20, TotalMin: 20, GeneratedAt: now - 3*hDay})
		// A stream of the window is a ledger event. An older one is not there.
		hStream(t, nk, "ht", "live", "u2", now-hHour, 30, "https://m.test/1.jpg")
		hStream(t, nk, "ht", "gone", "u2", now-9*hDay, 30, "https://m.test/2.jpg")
		m.ready(t, nk)

		timeline := buildMergedTimeline(context.Background(), nk, "ht")
		got := make([]string, 0, len(timeline))
		for _, e := range timeline {
			got = append(got, fmt.Sprintf("%s:%s:%d:%d", e.Type, e.ID, e.Ts, e.Score))
		}
		hSameLines(t, "timeline", got, []string{
			fmt.Sprintf("stream_session:ev_live:%d:30", now-hHour),
			fmt.Sprintf("weekly_recap:%s:%d:30", recapEntryID(latest.WeekStart), latest.GeneratedAt),
			fmt.Sprintf("clip:ev_recent:%d:50", recent.Ts),
		})
		if got, want := hJSON(t, timeline[1].Data), hJSON(t, hStamped(latest, 0)); got != want {
			t.Fatalf("recap payload:\n got %s\nwant %s", got, want)
		}
		if got, want := hJSON(t, timeline[2].Data), hJSON(t, recent); got != want {
			t.Fatalf("clip payload:\n got %s\nwant %s", got, want)
		}
		if timeline[2].ActorID != "u1" {
			t.Fatalf("clip actor = %q", timeline[2].ActorID)
		}
	})
}

func hCatchup(t *testing.T, nk runtime.NakamaModule, crewID, userID string, lastSeen int64) CatchupResponse {
	t.Helper()
	out, err := CrewCatchupRPC(ctxWithUser(userID), testLogger(), nil, nk, fmt.Sprintf(`{"crew_id":%q,"last_seen":%d}`, crewID, lastSeen))
	if err != nil {
		t.Fatal(err)
	}
	var resp CatchupResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCharCatchupClips(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hk")
		now := time.Now().UnixMilli()
		hClip(t, nk, "hk", StoredClip{ClipID: "before", ActorID: "u1", Ts: now - 3*hDay, DurationSeconds: 9})
		hClip(t, nk, "hk", StoredClip{ClipID: "after", ActorID: "u2", Ts: now - 1*hDay, DurationSeconds: 14, Game: "CS2"})
		// A clip of 30 days counts for a member who was never here.
		hClip(t, nk, "hk", StoredClip{ClipID: "ancient", ActorID: "u1", Ts: now - 30*hDay, DurationSeconds: 9})
		m.ready(t, nk)

		// Seen two days ago: the one clip after it, with the name of the actor.
		resp := hCatchup(t, nk, "hk", "u1", now-2*hDay)
		if !resp.HasEvents || resp.EventCount != 1 || len(resp.TopEvents) != 1 {
			t.Fatalf("catch-up = %+v", resp)
		}
		top := resp.TopEvents[0]
		if top.Type != "clip" || top.ActorID != "u2" || top.ActorName != "Bo" || top.Ts != now-1*hDay {
			t.Fatalf("top event = %+v", top)
		}
		data, _ := top.Data.(map[string]interface{})
		if data["clip_id"] != "after" || data["event_id"] != "ev_after" || data["game"] != "CS2" || data["duration_seconds"] != float64(14) {
			t.Fatalf("top event data = %+v", top.Data)
		}

		// Never seen: every clip counts, the newest ones lead.
		resp = hCatchup(t, nk, "hk", "u2", 0)
		if resp.EventCount != 3 || len(resp.TopEvents) != 3 || resp.TopEvents[0].Ts != now-1*hDay || resp.TopEvents[2].Ts != now-30*hDay {
			t.Fatalf("catch-up of a new member = %+v", resp)
		}
	})
}

func TestCharGuestFeedClips(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		ctx := context.Background()
		nk := seedGuestCrew()
		now := time.Now().UnixMilli()
		for i := 1; i <= 8; i++ {
			c := StoredClip{ClipID: fmt.Sprintf("g%d", i), ActorID: testAliceID, ClipperName: "alice", Ts: now - int64(i)*hDay, DurationSeconds: float64(i), MediaURL: "https://m.test/secret.mp4"}
			if i == 2 {
				c.ClipType, c.Game = clipTypeGame, "CS2"
			}
			hClip(t, nk, testGuestCrew, c)
		}
		// The ledger holds a clip event of a clip the document has, and one
		// it does not have.
		for _, ev := range []CrewEvent{
			{ID: "led_dup", Type: "clip", ActorID: testBobID, Timestamp: now - 2*hHour, Data: ClipData{ClipID: "g1", ClipType: clipTypeVoice, ClipperName: "bob", DurationSeconds: 99}},
			{ID: "led_own", Type: "clip", ActorID: testBobID, Timestamp: now - 3*hHour, Data: ClipData{ClipID: "only_ledger", ClipType: clipTypeVoice, ClipperName: "bob", DurationSeconds: 77}},
		} {
			ev.CrewID = testGuestCrew
			if err := AppendCrewEvent(ctx, nk, testGuestCrew, ev); err != nil {
				t.Fatal(err)
			}
		}
		m.ready(t, nk)

		var resp struct {
			Clips     []guestClip `json:"clips"`
			ClipCount int         `json:"clip_count"`
		}
		raw := callGuestCrewFeed(t, nk)
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatal(err)
		}
		// Nine clips: the eight of the document and the one only the ledger has.
		if resp.ClipCount != 9 || len(resp.Clips) != guestFeedClipLimit {
			t.Fatalf("clip_count=%d clips=%d", resp.ClipCount, len(resp.Clips))
		}
		got := make([]string, 0, len(resp.Clips))
		for _, c := range resp.Clips {
			got = append(got, fmt.Sprintf("%s/%s/%.0f/%s/%d", c.ClipType, c.ClipperName, c.DurationSeconds, c.Game, (now-c.Ts)/hHour))
		}
		hSameLines(t, "guest clips", got, []string{
			"voice/bob/77//3",
			"voice/alice/1//24",
			"game/alice/2/CS2/48",
			"voice/alice/3//72",
			"voice/alice/4//96",
			"voice/alice/5//120",
		})
		if strings.Contains(raw, "secret.mp4") {
			t.Fatalf("the guest feed holds a media URL: %s", raw)
		}
	})
}

// The invite preview reads the ledger only. The durable clips and recaps of
// the crew are not in it.
func TestCharInvitePreviewReadsTheLedgerOnly(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		ctx := context.Background()
		nk := seedGuestCrew()
		const other, otherCode = "crew-uuid-2", "WXYZ-2345"
		nk.put(InviteCodeCollection, otherCode, SystemUserID, map[string]string{"crew_id": other, "inviter_user_id": testInviterID})
		nk.groups[other] = &api.Group{Id: other, Name: "Day Owls", EdgeCount: 1}
		nk.addMember(other, testAliceID, "alice")
		now := time.Now().UnixMilli()
		for _, crewID := range []string{testGuestCrew, other} {
			hClip(t, nk, crewID, StoredClip{ClipID: "doc_clip", ActorID: testAliceID, ClipperName: "alice", Ts: now - hDay, DurationSeconds: 9, MediaURL: "https://m.test/doc.mp4"})
			hRecap(t, nk, crewID, WeeklyRecapData{WeekStart: weekStartOf(now) - 7*hDay, TotalHangoutMin: 300, TotalMin: 300, TopGame: "DocGame", MostActive: "alice"})
		}
		// Only the second crew has a recap and a clip in its ledger.
		for _, ev := range []CrewEvent{
			{ID: "led_recap", Type: "weekly_recap", Timestamp: now - 2*hDay, Data: WeeklyRecapData{CrewID: other, TotalHangoutMin: 120, TopGame: "LedgerGame", LongestSessionMin: 45, MostActive: "bob"}},
			{ID: "led_clip", Type: "clip", ActorID: testAliceID, Timestamp: now - hHour, Data: ClipData{ClipID: "lc", ClipType: clipTypeVoice, ClipperName: "alice", DurationSeconds: 11, MediaURL: "https://m.test/ledger.mp4"}},
		} {
			ev.CrewID = other
			if err := AppendCrewEvent(ctx, nk, other, ev); err != nil {
				t.Fatal(err)
			}
		}
		m.ready(t, nk)

		resolve := func(code string) ResolveCrewInviteResponse {
			out, err := ResolveCrewInviteRPC(ctx, testLogger(), nil, nk, `{"code":"`+code+`"}`)
			if err != nil {
				t.Fatal(err)
			}
			var resp ResolveCrewInviteResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatal(err)
			}
			return resp
		}
		first := resolve(testGuestCode)
		if first.Highlight != "" || first.TopGame != "" || first.MostActive != "" || len(first.RecentClips) != 0 {
			t.Fatalf("preview of a crew with an empty ledger = %+v", first)
		}
		second := resolve(otherCode)
		if second.Highlight != "2h hangout · LedgerGame" || second.TopGame != "LedgerGame" || second.LongestSessionMin != 45 || second.MostActive != "bob" {
			t.Fatalf("preview = %+v", second)
		}
		if len(second.RecentClips) != 1 || second.RecentClips[0].MediaURL != "https://m.test/ledger.mp4" || second.RecentClips[0].DurationSeconds != 11 {
			t.Fatalf("preview clips = %+v", second.RecentClips)
		}
	})
}

func hPostClip(nk runtime.NakamaModule, userID, crewID, clipID string) (map[string]interface{}, error) {
	out, err := PostClipRPC(ctxWithUser(userID), testLogger(), nil, nk,
		fmt.Sprintf(`{"crew_id":%q,"clip_id":%q,"duration_seconds":12,"participants":["u1","u2"],"game":"CS2"}`, crewID, clipID))
	if err != nil {
		return nil, err
	}
	var resp map[string]interface{}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func TestCharPostClip(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		nk := hCrew(t, "hp")
		today := time.Now().Truncate(24 * time.Hour).UnixMilli()
		// Ann posted 49 clips today, one yesterday and one long ago.
		for i := 0; i < ClipMaxPerUserPerDay-1; i++ {
			hClip(t, nk, "hp", StoredClip{ClipID: fmt.Sprintf("today%02d", i), ActorID: "u1", Ts: today + int64(i), DurationSeconds: 5})
		}
		hClip(t, nk, "hp", StoredClip{ClipID: "yesterday", ActorID: "u1", Ts: today - 1, DurationSeconds: 5})
		hClip(t, nk, "hp", StoredClip{ClipID: "seeded", ActorID: "u1", Ts: today - 40*hDay, DurationSeconds: 5})
		m.ready(t, nk)

		// A new clip: stored, and in the feed with the names of the participants.
		first, err := hPostClip(nk, "u1", "hp", "new")
		if err != nil || first["success"] != true || first["clip_id"] != "new" || first["event_id"] == "" {
			t.Fatalf("post: %+v err=%v", first, err)
		}
		z := hZones(nk, "hp", "u1", 0, time.Now().UnixMilli())
		var posted *StoredClip
		for _, e := range z.Clips {
			if c, ok := e.Data.(StoredClip); ok && c.ClipID == "new" {
				posted = &c
				if e.ID != first["event_id"] || e.Ts != c.Ts {
					t.Fatalf("feed entry = %+v", e)
				}
			}
		}
		if posted == nil || posted.ActorID != "u1" || posted.ClipperName != "Ann" || posted.Score != 50 || posted.ClipType != clipTypeVoice ||
			posted.Game != "CS2" || hJSON(t, posted.ParticipantNames) != `["Ann","Bo"]` || posted.DurationSeconds != 12 {
			t.Fatalf("posted clip in the feed = %+v", posted)
		}
		if len(z.Clips) != ClipMaxPerUserPerDay+1 {
			t.Fatalf("the window holds %d clips, want %d", len(z.Clips), ClipMaxPerUserPerDay+1)
		}

		// A retry answers as the first call and stores nothing.
		again, err := hPostClip(nk, "u1", "hp", "new")
		if err != nil || again["event_id"] != first["event_id"] {
			t.Fatalf("retry: %+v err=%v, first %+v", again, err, first)
		}
		// A retry of a clip from before answers with that clip, also an old one.
		if old, err := hPostClip(nk, "u1", "hp", "seeded"); err != nil || old["event_id"] != "ev_seeded" {
			t.Fatalf("retry of an old clip: %+v err=%v", old, err)
		}
		// The clip_id of another actor is taken.
		if _, err := hPostClip(nk, "u2", "hp", "new"); errCode(err) != 6 {
			t.Fatalf("clip_id of another actor: %v, want code 6", err)
		}
		// That was clip 50 of today for Ann. The next one is over the limit.
		if _, err := hPostClip(nk, "u1", "hp", "over"); errCode(err) != 8 {
			t.Fatalf("clip 51 of the day: %v, want code 8", err)
		}
		// A retry does not count against the limit. Bo has his own count.
		if _, err := hPostClip(nk, "u1", "hp", "new"); err != nil {
			t.Fatalf("retry at the limit: %v", err)
		}
		if _, err := hPostClip(nk, "u2", "hp", "bo"); err != nil {
			t.Fatalf("post of Bo: %v", err)
		}
		if n := len(hZones(nk, "hp", "u1", 0, time.Now().UnixMilli()).Clips); n != ClipMaxPerUserPerDay+2 {
			t.Fatalf("the window holds %d clips, want %d", n, ClipMaxPerUserPerDay+2)
		}
	})
}

// hClipInFeed finds a clip of the free window by its clip_id.
func hClipInFeed(t *testing.T, nk runtime.NakamaModule, crewID, clipID string) StoredClip {
	t.Helper()
	z := hZones(nk, crewID, "u1", 0, time.Now().UnixMilli())
	entries := append([]FeedEntry{}, z.Clips...)
	if z.Stage.Entry != nil {
		entries = append(entries, *z.Stage.Entry)
	}
	for _, e := range entries {
		if c, ok := e.Data.(StoredClip); ok && c.ClipID == clipID {
			return c
		}
	}
	t.Fatalf("clip %s is not in the feed", clipID)
	return StoredClip{}
}

func TestCharClipUpload(t *testing.T) {
	requireNoS3(t)
	// The public base of the clips bucket, as S3PublicURL reads it.
	initS3()
	prevBase := s3PublicURL
	s3PublicURL = "https://clips.test"
	t.Cleanup(func() { s3PublicURL = prevBase })

	forHistModes(t, func(t *testing.T, m histMode) {
		ctx := context.Background()
		nk := hCrew(t, "hu")
		now := time.Now().UnixMilli()
		hClip(t, nk, "hu", StoredClip{ClipID: "voice", ActorID: "u1", Ts: now - 2*hHour, DurationSeconds: 9})
		hClip(t, nk, "hu", StoredClip{ClipID: "game", ActorID: "u1", ClipType: clipTypeGame, Ts: now - 3*hHour, DurationSeconds: 30, Width: 1280, Height: 720})
		m.ready(t, nk)

		url := func(user, clipID, clipType string) error {
			_, err := ClipUploadURLRPC(ctxWithUser(user), testLogger(), nil, nk,
				fmt.Sprintf(`{"crew_id":"hu","clip_id":%q,"clip_type":%q,"poster_bytes":10,"video_bytes":10}`, clipID, clipType))
			return err
		}
		complete := func(user, clipID string) (string, error) {
			return ClipUploadCompleteRPC(ctxWithUser(user), testLogger(), nil, nk, fmt.Sprintf(`{"crew_id":"hu","clip_id":%q}`, clipID))
		}
		for _, tc := range []struct {
			user, clipID, clipType string
			code                   int
		}{
			{"u1", "voice", "", -1},
			{"u2", "voice", "", 7},
			{"u1", "nope", "", 5},
			{"u1", "game", "", 3},
			{"u1", "game", "game", -1},
			{"u2", "game", "game", 7},
			{"u1", "voice", "game", 3},
			{"u1", "nope", "game", 5},
		} {
			if err := url(tc.user, tc.clipID, tc.clipType); errCode(err) != tc.code {
				t.Errorf("clip_upload_url %+v: %v", tc, err)
			}
		}

		// A voice upload completes: the clip in the feed has its media URL.
		if _, err := complete("u2", "voice"); errCode(err) != 7 {
			t.Fatalf("complete by another member: %v", err)
		}
		if _, err := complete("u1", "nope"); errCode(err) != 5 {
			t.Fatalf("complete of an unknown clip: %v", err)
		}
		if _, err := complete("u1", "game"); errCode(err) != 3 {
			t.Fatalf("voice complete of a game clip: %v", err)
		}
		out, err := complete("u1", "voice")
		if err != nil || !strings.Contains(out, `"media_url":"https://clips.test/crews/hu/voice.mp4"`) {
			t.Fatalf("complete: %s err=%v", out, err)
		}
		if c := hClipInFeed(t, nk, "hu", "voice"); c.MediaURL != "https://clips.test/crews/hu/voice.mp4" || c.EventID != "ev_voice" || c.DurationSeconds != 9 {
			t.Fatalf("voice clip in the feed = %+v", c)
		}

		// A game clip gets its poster, then its video. With the poster it is
		// the Stage.
		if err := setGameClipURL(ctx, nk, "hu", "game", "u1", gameClipPartPoster, "https://clips.test/crews/hu/game.jpg"); err != nil {
			t.Fatal(err)
		}
		if err := setGameClipURL(ctx, nk, "hu", "game", "u1", gameClipPartVideo, "https://clips.test/crews/hu/game.mp4"); err != nil {
			t.Fatal(err)
		}
		if err := setGameClipURL(ctx, nk, "hu", "game", "u2", gameClipPartPoster, "https://clips.test/other.jpg"); err == nil {
			t.Fatal("another member set the poster")
		}
		c := hClipInFeed(t, nk, "hu", "game")
		if c.PosterURL != "https://clips.test/crews/hu/game.jpg" || c.MediaURL != "https://clips.test/crews/hu/game.mp4" || c.Width != 1280 {
			t.Fatalf("game clip in the feed = %+v", c)
		}
		if z := hZones(nk, "hu", "u1", 0, time.Now().UnixMilli()); z.Stage.Kind != "game_clip" || z.Stage.Entry.ID != "ev_game" {
			t.Fatalf("stage = %+v", z.Stage)
		}
	})
}

func TestCharWeeklyRecapCountsTheClipsOfItsWeek(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		ctx := context.Background()
		nk := hCrew(t, "crew")
		monday := upcomingMonday()
		end := monday.UnixMilli()
		hClip(t, nk, "crew", StoredClip{ClipID: "a1", ActorID: "u1", Ts: end - 2*hDay, DurationSeconds: 10})
		hClip(t, nk, "crew", StoredClip{ClipID: "a2", ActorID: "u1", Ts: end - 3*hDay, DurationSeconds: 25})
		longest := hClip(t, nk, "crew", StoredClip{ClipID: "b1", ActorID: "u2", Ts: end - 4*hDay, DurationSeconds: 40, Game: "CS2", MediaURL: "https://m.test/b1.mp4"})
		// The edges of the week count. A clip before the week does not.
		hClip(t, nk, "crew", StoredClip{ClipID: "edge", ActorID: "u1", Ts: end - 7*hDay, DurationSeconds: 1})
		hClip(t, nk, "crew", StoredClip{ClipID: "before", ActorID: "u2", Ts: end - 7*hDay - 1, DurationSeconds: 59})
		m.ready(t, nk)

		generateWeeklyRecapAt(ctx, nk, testLogger(), "crew", monday)

		var recap *WeeklyRecapData
		for _, e := range buildMergedTimeline(ctx, nk, "crew") {
			if r, ok := e.Data.(WeeklyRecapData); ok && e.Type == "weekly_recap" {
				recap = &r
				if e.ID != recapEntryID(end-7*hDay) {
					t.Fatalf("recap id = %s", e.ID)
				}
			}
		}
		if recap == nil {
			t.Fatal("no recap in the timeline")
		}
		if recap.ClipCount != 4 || recap.MostClippedUserID != "u1" || recap.MostClippedCount != 3 || recap.MostClipped != "Ann" {
			t.Fatalf("recap clips = %+v", recap)
		}
		cw := recap.ClipOfWeek
		if cw == nil || cw.ClipID != "b1" || cw.DurationSeconds != 40 || cw.Ts != longest.Ts || cw.MediaURL != "https://m.test/b1.mp4" || cw.Title != "Bo clipped that" || cw.Game != "CS2" {
			t.Fatalf("clip of the week = %+v", cw)
		}
		if recap.WeekStart != end-7*hDay || recap.WeekEnd != end || recap.WeekStartMs != recap.WeekStart {
			t.Fatalf("recap week = %d..%d", recap.WeekStart, recap.WeekEnd)
		}
	})
}

func TestCharWeeklyRecapJobWritesOneRecapForAWeek(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		ctx := context.Background()
		nk := newJobNk()
		monday := upcomingMonday()
		hClip(t, nk, "crew", StoredClip{ClipID: "a1", ActorID: "u1", Ts: monday.UnixMilli() - 2*hDay, DurationSeconds: 10})
		// A recap of the week before is there: the job still writes this week.
		hRecap(t, nk, "crew", WeeklyRecapData{WeekStart: monday.UnixMilli() - 14*hDay, TotalHangoutMin: 10, TotalMin: 10})
		m.ready(t, nk)

		latest := func() (string, int64) {
			for _, e := range buildMergedTimeline(ctx, nk, "crew") {
				if e.Type == "weekly_recap" {
					return e.ID, e.Ts
				}
			}
			return "", 0
		}
		if id, _ := latest(); id != recapEntryID(monday.UnixMilli()-14*hDay) {
			t.Fatalf("latest recap before the job = %s", id)
		}

		// Monday 01:00: the job writes the recap of the week that ended.
		generateDueRecaps(ctx, nk, testLogger(), monday.Add(time.Hour))
		id, generated := latest()
		if id != recapEntryID(monday.UnixMilli()-7*hDay) || recapCount(nk) != 2 {
			t.Fatalf("after the job: latest=%s, %d recaps", id, recapCount(nk))
		}
		// The next ticks of the same week write nothing.
		generateDueRecaps(ctx, nk, testLogger(), monday.Add(2*time.Hour))
		generateDueRecaps(ctx, nk, testLogger(), monday.Add(30*time.Hour))
		if again, ts := latest(); again != id || ts != generated || recapCount(nk) != 2 {
			t.Fatalf("after three ticks: latest=%s ts=%d, %d recaps", again, ts, recapCount(nk))
		}
		// After the lookback the week is skipped.
		generateDueRecaps(ctx, nk, testLogger(), monday.Add(7*24*time.Hour+recapLookback))
		if recapCount(nk) != 2 {
			t.Fatalf("after the lookback: %d recaps", recapCount(nk))
		}
	})
}

func TestCharStreamReplayGetsItsSnapshots(t *testing.T) {
	forHistModes(t, func(t *testing.T, m histMode) {
		ctx := context.Background()
		nk := hCrew(t, "hr")
		now := time.Now().UnixMilli()
		hStream(t, nk, "hr", "earlier", "u1", now-2*hDay, 10, "https://m.test/e/1.jpg")
		m.ready(t, nk)

		// A stream ends with no snapshot yet: a plain session, not a replay.
		hStream(t, nk, "hr", "fresh", "u2", now-hHour, 30)
		feed := func() FeedZones {
			return crewFeedZones(ctx, testLogger(), nk, FeedRequest{CrewID: "hr"}, "u1", buildMergedTimeline(ctx, nk, "hr"), nil, nil, time.Now().UnixMilli())
		}
		z := feed()
		if z.Stage.Kind != "stream_replay" || z.Stage.Entry.ID != "ev_earlier" || hEntries(z.Sessions) != "session:ev_fresh" {
			t.Fatalf("before the snapshots: stage=%+v sessions=%s", z.Stage, hEntries(z.Sessions))
		}

		// The SFU uploads the frames after the stream ended. The backfill job
		// finds them.
		urls := []string{"https://m.test/f/1.jpg", "https://m.test/f/2.jpg", "https://m.test/f/3.jpg"}
		prev := listSnapshotURLs
		listSnapshotURLs = func(crewID, sessionID string) ([]string, error) {
			if crewID == "hr" && sessionID == "sess_fresh" {
				return urls, nil
			}
			return nil, nil
		}
		t.Cleanup(func() { listSnapshotURLs = prev })
		snapshotBackfill(ctx, nk, testLogger())

		// The replay with more frames and minutes is now the Stage. The
		// other one is a session of the window.
		z = feed()
		if z.Stage.Kind != "stream_replay" || z.Stage.Entry.ID != "ev_fresh" || z.Stage.Entry.Type != "session-preview" {
			t.Fatalf("after the snapshots: stage=%+v", z.Stage)
		}
		replay, ok := z.Stage.Entry.Data.(StoredStreamSession)
		if !ok || hJSON(t, replay.SnapshotURLs) != hJSON(t, urls) || replay.SessionID != "sess_fresh" || replay.StreamerName != "Bo" || replay.DurationMin != 30 || replay.PeakViewers != 2 {
			t.Fatalf("stage payload = %+v", z.Stage.Entry.Data)
		}
		if hEntries(z.Sessions) != "session-preview:ev_earlier" {
			t.Fatalf("sessions = %s", hEntries(z.Sessions))
		}
		// A second run of the job changes nothing.
		snapshotBackfill(ctx, nk, testLogger())
		if again := feed(); hJSON(t, again.Stage) != hJSON(t, z.Stage) {
			t.Fatalf("stage after a second backfill = %+v", again.Stage)
		}
	})
}
