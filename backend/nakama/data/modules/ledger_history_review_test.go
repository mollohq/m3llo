package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Phase 1b of the durable ledger: the cases the first tests of the zone feed
// did not cover.

// hZonesWithTimeline builds the zone feed as crew_feed does: with the merged
// timeline of the crew and the joins. hZones passes no timeline.
func hZonesWithTimeline(nk *fakeQuestsNk, crewID, viewer string, historyBefore, nowMs int64) FeedZones {
	ctx := context.Background()
	timeline := buildMergedTimeline(ctx, nk, crewID)
	joins := feedJoinsFor(ctx, nk, crewID, timeline, viewer, nowMs)
	return crewFeedZones(ctx, testLogger(), nk, FeedRequest{CrewID: crewID, HistoryBefore: historyBefore}, viewer, timeline, nil, joins, nowMs)
}

// zoneBuilders are the two ways the tests build the zone feed.
var zoneBuilders = map[string]func(nk *fakeQuestsNk, crewID, viewer string, historyBefore, nowMs int64) FeedZones{
	"no_timeline": func(nk *fakeQuestsNk, crewID, viewer string, historyBefore, nowMs int64) FeedZones {
		return hZones(nk, crewID, viewer, historyBefore, nowMs)
	},
	"timeline": hZonesWithTimeline,
}

// pagesOf is the zone feed of a viewer as JSON, one entry for each page.
func pagesOf(t *testing.T, build func(*fakeQuestsNk, string, string, int64, int64) FeedZones, nk *fakeQuestsNk, crewID, viewer string, nowMs int64) []string {
	t.Helper()
	var pages []string
	before := int64(0)
	for {
		z := build(nk, crewID, viewer, before, nowMs)
		pages = append(pages, hJSON(t, z))
		if z.HistoryNext == 0 {
			return pages
		}
		if len(pages) > 200 {
			t.Fatal("Crew history does not end")
		}
		before = z.HistoryNext
	}
}

func samePages(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d pages, want %d", what, len(got), len(want))
	}
	for page := range got {
		if got[page] != want[page] {
			t.Fatalf("%s: page %d differs\ngot:  %s\nwant: %s", what, page, got[page], want[page])
		}
	}
}

// seedContentCrew writes a crew with content in every field the feed shows:
// game and voice clips with and without media, stream sessions with
// snapshots, recaps, a clip and a stream session from before the event id,
// and a ledger of this week. Times are relative to nowMs. clips and streams
// are how many of each it writes.
func seedContentCrew(t *testing.T, nk *fakeQuestsNk, crewID string, nowMs int64, clips, streams int) {
	t.Helper()
	ctx := context.Background()
	nk.groups[crewID] = &api.Group{Id: crewID, CreateTime: timestamppb.New(time.UnixMilli(nowMs - 400*hDay))}
	for i := 0; i < clips; i++ {
		// Some clips in the free window, the others in Crew history.
		c := StoredClip{ClipID: fmt.Sprintf("c%03d", i), ActorID: []string{"u1", "u2"}[i%2], Ts: nowMs - 2*hHour - int64(i)*13*hHour, DurationSeconds: float64(5 + i%40)}
		if i%3 == 0 {
			c.ClipType, c.Width, c.Height, c.Game = clipTypeGame, 1920, 1080, []string{"CS2", "Valorant"}[i%2]
			c.PosterURL = fmt.Sprintf("https://m.test/%s/%d.jpg", crewID, i)
		}
		if i%4 != 0 {
			c.MediaURL = fmt.Sprintf("https://m.test/%s/%d.mp4", crewID, i)
		}
		hClip(t, nk, crewID, c)
	}
	for i := 0; i < streams; i++ {
		ts := nowMs - 3*hHour - int64(i)*29*hHour
		var snaps []string
		for n := 0; n < i%4; n++ {
			snaps = append(snaps, fmt.Sprintf("https://m.test/%s/s%d/%d.jpg", crewID, i, n))
		}
		hStream(t, nk, crewID, fmt.Sprintf("s%03d", i), []string{"u2", "u1"}[i%2], ts, 5+i%90, snaps...)
	}
	for week := int64(-20); week <= -1; week++ {
		start := weekStartOf(nowMs) + week*7*hDay
		r := WeeklyRecapData{WeekStart: start, TopGame: "CS2", ClipCount: int(-week), MostActive: "Ann"}
		if week%5 != 0 {
			r.TotalHangoutMin, r.TotalMin = int(100-week), int(120-week)
		}
		hRecap(t, nk, crewID, r)
	}
	// The ledger of this week, for the merged timeline.
	for i, ev := range []CrewEvent{
		{ID: "game1", Type: "game_session", ActorID: "u1", Timestamp: nowMs - 5*hHour, Score: 20,
			Data: GameSessionData{GameName: "CS2", PlayerIDs: []string{"u1", "u2"}, DurationMin: 45}},
		{ID: "voice1", Type: "voice_session", ActorID: "u2", Timestamp: nowMs - 26*hHour, Score: 10,
			Data: VoiceSessionData{ChannelName: "General", ParticipantIDs: []string{"u1", "u2"}, DurationMin: 70}},
	} {
		ev.CrewID = crewID
		if err := AppendCrewEvent(ctx, nk, crewID, ev); err != nil {
			t.Fatalf("ledger event %d: %v", i, err)
		}
	}
}

// addEntriesWithNoEventID gives the documents a clip and a stream session
// from before they held the event id.
func addEntriesWithNoEventID(t *testing.T, nk *fakeQuestsNk, crewID string, nowMs int64) {
	t.Helper()
	ctx := context.Background()
	clips, version := readClipsDoc(ctx, nk, crewID)
	clips.Clips = append(clips.Clips, StoredClip{ClipID: "legacy_clip", ActorID: "u1", ClipperName: "Ann", ClipType: clipTypeVoice, Ts: nowMs - 30*hDay, Score: 50, DurationSeconds: 11})
	if err := writeClipsDoc(ctx, nk, crewID, clips, version); err != nil {
		t.Fatal(err)
	}
	streams, _ := readStreamSessionsDoc(ctx, nk, crewID)
	streams.Sessions = append(streams.Sessions, StoredStreamSession{SessionID: "legacy_session", StreamerID: "u2", StreamerName: "Bo", Title: "Legacy",
		DurationMin: 33, SnapshotURLs: []string{"https://m.test/legacy.jpg"}, Ts: nowMs - 31*hDay, Score: 30})
	nk.put(CrewStreamSessionsCollection, crewID, SystemUserID, streams)
}

// A crew at the old caps: 250 clips and 150 stream sessions. Every page of
// the zone feed is the same JSON from the store as from the documents: with
// and without the merged timeline, for a viewer with and without m3llo+,
// after an import and for a crew that was written after the cutover.
// TestCharZonesAtTheOldCaps checks the counts of this crew. This test checks
// the content.
func TestZonesContentAtTheOldCapsIsTheSameFromTheStore(t *testing.T) {
	for name, build := range zoneBuilders {
		t.Run(name, func(t *testing.T) {
			// The timeline cuts at the real time, so this crew is seeded
			// relative to it.
			now := time.Now().UnixMilli()
			seed := func(nk *fakeQuestsNk) {
				seedContentCrew(t, nk, "cap", now, CrewClipsMaxRetained-1, CrewStreamSessionsMaxRetained-1)
				addEntriesWithNoEventID(t, nk, "cap", now)
			}

			nk := hCrew(t, "cap")
			seed(nk)
			if len(docClips(nk, "cap")) != CrewClipsMaxRetained {
				t.Fatalf("the document holds %d clips, want the cap", len(docClips(nk, "cap")))
			}
			if doc, _ := readStreamSessionsDoc(context.Background(), nk, "cap"); len(doc.Sessions) != CrewStreamSessionsMaxRetained {
				t.Fatalf("the copy holds %d stream sessions, want the cap", len(doc.Sessions))
			}
			fromDocs := map[string][]string{}
			for _, viewer := range []string{"u1", "u2"} {
				fromDocs[viewer] = pagesOf(t, build, nk, "cap", viewer, now)
				if len(fromDocs[viewer]) < 5 {
					t.Fatalf("viewer %s: %d pages from the documents, want a crew with many", viewer, len(fromDocs[viewer]))
				}
			}
			// The first page has content of every kind.
			var first FeedZones
			if err := json.Unmarshal([]byte(fromDocs["u1"][0]), &first); err != nil {
				t.Fatal(err)
			}
			if len(first.Clips) == 0 || first.Board.Recap == nil || len(first.History) != zonesHistoryPageWeeks || !first.History[1].Locked || len(first.History[1].Teasers) == 0 {
				t.Fatalf("the first page of the documents is thin: %s", fromDocs["u1"][0])
			}
			if name == "timeline" && len(first.Sessions) == 0 {
				t.Fatalf("the timeline gave the feed no session: %s", fromDocs["u1"][0])
			}

			t.Run("import", func(t *testing.T) {
				importAndCutOver(t, nk)
				for _, viewer := range []string{"u1", "u2"} {
					samePages(t, "viewer "+viewer, pagesOf(t, build, nk, "cap", viewer, now), fromDocs[viewer])
				}
			})
			t.Run("store", func(t *testing.T) {
				useHistoryStoreReads(t)
				fresh := hCrew(t, "cap")
				seed(fresh)
				// The entries with no event id come from the documents: the
				// reconcile stores them after the cutover too.
				noSoak(t)
				if r := reconcilePass(t, fresh); r.lastHist.Inserted != 2 || !r.lastHist.clean() {
					t.Fatalf("pass = %+v", r.lastHist)
				}
				for _, viewer := range []string{"u1", "u2"} {
					samePages(t, "viewer "+viewer, pagesOf(t, build, fresh, "cap", viewer, now), fromDocs[viewer])
				}
			})
		})
	}
}

// Clips of one time: in the free window, in an open week and in a locked
// week of Crew history, on the border of two weeks, game and voice. Every
// page is the same from the store as from the documents, and each clip is
// there once.
//
// The documents keep clips of one time in the order they were written. The
// store orders them by event id. An event id starts with the time it was
// made, so the two orders are the same for the ids the server makes. The ids
// of this test are in the order of the writes.
func TestZonesWithClipsOfOneTime(t *testing.T) {
	nk := hCrew(t, "eq")
	nk.groups["eq"] = &api.Group{Id: "eq", CreateTime: timestamppb.New(time.UnixMilli(hAt(-30, 0, 0)))}
	n := 0
	clip := func(ts int64, game bool) {
		n++
		c := StoredClip{ClipID: fmt.Sprintf("c%02d", n), ActorID: []string{"u1", "u2"}[n%2], Ts: ts, DurationSeconds: float64(n)}
		if game {
			c.ClipType, c.Game = clipTypeGame, "CS2"
		}
		hClip(t, nk, "eq", c)
	}
	for _, ts := range []int64{
		hNow - hHour,      // the free window
		hAt(-3, 2, 0),     // a week of Crew history
		hAt(-5, 0, 0),     // Monday 00:00: the first ms of a week
		hAt(-5, 0, 0) - 1, // the last ms of the week before
		hAt(-9, 3, 0),     // more clips than a locked week has teasers
	} {
		for i := 0; i < 4; i++ {
			clip(ts, i%2 == 1)
		}
	}
	for i := 0; i < 5; i++ {
		clip(hAt(-9, 3, 0), false)
	}
	total := n

	fromDocs := map[string][]string{}
	for _, viewer := range []string{"u1", "u2"} {
		fromDocs[viewer] = zonesOfEveryPage(t, nk, "eq", viewer, hNow)
	}
	importAndCutOver(t, nk)
	for _, viewer := range []string{"u1", "u2"} {
		fromStore := zonesOfEveryPage(t, nk, "eq", viewer, hNow)
		samePages(t, "viewer "+viewer, fromStore, fromDocs[viewer])
		// u2 has m3llo+ and sees every clip: each one once.
		seen, counted := map[string]int{}, 0
		for _, page := range fromStore {
			var z FeedZones
			if err := json.Unmarshal([]byte(page), &z); err != nil {
				t.Fatal(err)
			}
			for _, w := range z.History {
				counted += w.ClipCount
				for _, item := range w.Items {
					seen[item.ID]++
				}
				if w.Locked && w.WeekStart == hAt(-9, 0, 0) && (w.ClipCount != 9 || len(w.Teasers) != zonesTeasersMax || w.Teasers[0].Kind != "clip" || w.Teasers[2].Kind != "voice") {
					t.Fatalf("locked week of 9 clips = %+v", w)
				}
			}
			if len(z.History) > 0 && page == fromStore[0] {
				counted += len(z.Clips)
				for _, c := range z.Clips {
					seen[c.ID]++
				}
			}
		}
		if counted != total {
			t.Fatalf("viewer %s: the pages count %d clips, want %d", viewer, counted, total)
		}
		if viewer == "u2" {
			if len(seen) != total {
				t.Fatalf("viewer u2 sees %d clips, want %d", len(seen), total)
			}
			for id, times := range seen {
				if times != 1 {
					t.Fatalf("clip %s is on the pages %d times", id, times)
				}
			}
		}
	}
}

// The document and the store hold other content for the same clip, stream
// session and recap. The readers follow the truth: the document before the
// cutover, the store after it, the document again under the kill switch.
func TestReadersFollowTheTruthWhenDocumentAndStoreDisagree(t *testing.T) {
	ctx := context.Background()
	nk := hCrew(t, "dis")
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "") // 24 hours: no marker yet
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })

	hClip(t, nk, "dis", StoredClip{ClipID: "c", ActorID: "u1", Ts: hNow - hHour, DurationSeconds: 9, MediaURL: "https://m.test/doc.mp4"})
	hStream(t, nk, "dis", "s", "u2", hAt(-3, 1, 0), 10)
	hRecap(t, nk, "dis", WeeklyRecapData{WeekStart: hAt(-1, 0, 0), TotalHangoutMin: 10, TotalMin: 10, TopGame: "from the document"})
	// The store gets other content for each of the three.
	set := func(id string, fn func(m map[string]interface{})) {
		t.Helper()
		err := store.UpdateData(ctx, "dis", id, func(raw json.RawMessage) (json.RawMessage, error) {
			var m map[string]interface{}
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, err
			}
			fn(m)
			return json.Marshal(m)
		})
		if err != nil {
			t.Fatalf("update of %s: %v", id, err)
		}
	}
	disagree := func() {
		set(clipRowID("ev_c"), func(m map[string]interface{}) { m["media_url"] = "https://m.test/store.mp4" })
		set("ev_s", func(m map[string]interface{}) { m["title"] = "from the store" })
		set(recapEntryID(hAt(-1, 0, 0)), func(m map[string]interface{}) { m["top_game"] = "from the store" })
	}
	disagree()

	read := func() (clipURL, streamTitle, recapGame string) {
		t.Helper()
		z := hZones(nk, "dis", "u2", 0, hNow)
		if len(z.Clips)+boolInt(z.Stage.Entry != nil) != 1 || len(z.History) != 1 || len(z.History[0].Items) != 1 || z.Board.Recap == nil {
			t.Fatalf("zones = %s", hJSON(t, z))
		}
		var data struct {
			MediaURL string `json:"media_url"`
			Title    string `json:"title"`
			TopGame  string `json:"top_game"`
		}
		for _, e := range []FeedEntry{firstClipEntry(z), z.History[0].Items[0], *z.Board.Recap} {
			if err := json.Unmarshal([]byte(hJSON(t, e.Data)), &data); err != nil {
				t.Fatal(err)
			}
		}
		clips := crewClipsWithID(ctx, nk, "dis", "c")
		if len(clips) != 1 || clips[0].MediaURL != data.MediaURL {
			t.Fatalf("the clip by its id = %+v, the feed has %s", clips, data.MediaURL)
		}
		if latest := crewLatestRecap(ctx, nk, "dis"); latest == nil || latest.TopGame != data.TopGame {
			t.Fatalf("the latest recap = %+v, the feed has %s", latest, data.TopGame)
		}
		return data.MediaURL, data.Title, data.TopGame
	}
	fromDoc := func(what string) {
		t.Helper()
		if url, title, game := read(); url != "https://m.test/doc.mp4" || title != "Stream s" || game != "from the document" {
			t.Fatalf("%s: readers have %q %q %q, want the document", what, url, title, game)
		}
	}
	fromStore := func(what string) {
		t.Helper()
		if url, title, game := read(); url != "https://m.test/store.mp4" || title != "from the store" || game != "from the store" {
			t.Fatalf("%s: readers have %q %q %q, want the store", what, url, title, game)
		}
	}

	// Before the cutover: the document. The stream session of the copy is
	// older than the ledger, so the copy decides about it.
	fromDoc("before the cutover")

	// The cutover. The pass before the marker takes the document, so the
	// store is made to disagree again after it.
	noSoak(t)
	if reconcilePass(t, nk); historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	disagree()
	fromStore("after the cutover")
	// A pass after the cutover does not take the document.
	if r := reconcilePass(t, nk); r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("pass after the cutover = %+v", r.lastHist)
	}
	fromStore("after a pass")

	// The kill switch of phase 1b, then the one of both phases.
	for _, env := range []string{"MELLO_LEDGER_HISTORY_READ", "MELLO_LEDGER_READ"} {
		t.Setenv(env, "doc")
		fromDoc(env + "=doc")
		t.Setenv(env, "")
	}
	fromStore("without a kill switch")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// firstClipEntry is the one clip of the free window: on the stage or in the
// clips zone.
func firstClipEntry(z FeedZones) FeedEntry {
	if z.Stage.Entry != nil {
		return *z.Stage.Entry
	}
	return z.Clips[0]
}

// Two recaps for one week in the recaps document (two nodes wrote the recap
// of a week at once). The store holds one row for a week: the first one.
//
// The board, the merged timeline and the recap job read the first one from
// the documents too. Crew history is the one place that differs: from the
// documents it shows the recap written last, from the store the first one.
// This is a known difference (plans/DURABLE-LEDGER.md section 6a).
func TestTwoRecapsForOneWeek(t *testing.T) {
	ctx := context.Background()
	nk := hCrew(t, "two")
	week, older := hAt(-1, 0, 0), hAt(-3, 0, 0)
	hRecap(t, nk, "two", WeeklyRecapData{WeekStart: older, TotalHangoutMin: 10, TotalMin: 10, TopGame: "first"})
	hRecap(t, nk, "two", WeeklyRecapData{WeekStart: older, TotalHangoutMin: 20, TotalMin: 20, TopGame: "second"})
	hRecap(t, nk, "two", WeeklyRecapData{WeekStart: week, TotalHangoutMin: 30, TotalMin: 30, TopGame: "first"})
	hRecap(t, nk, "two", WeeklyRecapData{WeekStart: week, TotalHangoutMin: 40, TotalMin: 40, TopGame: "second"})

	type view struct{ board, history, latest string }
	read := func() view {
		t.Helper()
		z := hZones(nk, "two", "u2", 0, hNow)
		if z.Board.Recap == nil || len(z.History) != 1 || z.History[0].WeekStart != older || z.History[0].Recap == nil {
			t.Fatalf("zones = %s", hJSON(t, z))
		}
		game := func(e *FeedEntry) string {
			var d struct {
				TopGame string `json:"top_game"`
			}
			if err := json.Unmarshal([]byte(hJSON(t, e.Data)), &d); err != nil {
				t.Fatal(err)
			}
			return d.TopGame
		}
		latest := crewLatestRecap(ctx, nk, "two")
		if latest == nil || latest.WeekStart != week || z.Board.Recap.ID != recapEntryID(week) {
			t.Fatalf("latest recap = %+v, board %+v", latest, z.Board.Recap)
		}
		return view{board: game(z.Board.Recap), history: game(z.History[0].Recap), latest: latest.TopGame}
	}
	if got := read(); got != (view{board: "first", history: "second", latest: "first"}) {
		t.Fatalf("from the documents = %+v", got)
	}

	importAndCutOver(t, nk)
	rows := histRows(t, currentLedger(), "two", "weekly_recap")
	if len(rows) != 2 {
		t.Fatalf("the store holds %d recap rows, want one for each week", len(rows))
	}
	if got := read(); got != (view{board: "first", history: "first", latest: "first"}) {
		t.Fatalf("from the store = %+v", got)
	}
	// The pass is clean with the second recaps not stored, and stays so.
	if r := reconcilePass(t, nk); r.lastHist.Inserted != 0 || r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("pass after the cutover = %+v", r.lastHist)
	}
	// The job does not write a third recap for the week.
	if state := crewRecapState(ctx, nk, "two"); len(state.Recaps) != 1 || state.Recaps[0].WeekStart != week {
		t.Fatalf("recap state = %+v", state)
	}
}

// A crew over the document cap after the cutover: the readers have every
// clip. Under the kill switch they have the capped document again, as before
// the cutover. Without it they have every clip again.
func TestKillSwitchOnACrewOverTheCapShowsTheDocument(t *testing.T) {
	ctx := context.Background()
	store := useHistoryStoreReads(t)
	nk := hCrew(t, "over")
	const total = CrewClipsMaxRetained + 30
	for i := 0; i < total; i++ {
		hClip(t, nk, "over", StoredClip{ClipID: fmt.Sprintf("c%03d", i), ActorID: "u1", Ts: hNow - 8*hDay - int64(total-i)*5*hHour, DurationSeconds: 9})
	}
	if len(docClips(nk, "over")) != CrewClipsMaxRetained || len(storeClips(t, store, "over")) != total {
		t.Fatalf("the seed is wrong: document %d clips, store %d", len(docClips(nk, "over")), len(storeClips(t, store, "over")))
	}
	count := func() (clips int, oldest string) {
		t.Helper()
		for _, page := range zonesOfEveryPage(t, nk, "over", "u2", hNow) {
			var z FeedZones
			if err := json.Unmarshal([]byte(page), &z); err != nil {
				t.Fatal(err)
			}
			for _, w := range z.History {
				clips += w.ClipCount
				if len(w.Items) != w.ClipCount {
					t.Fatalf("week %d: %d items, clip count %d", w.WeekStart, len(w.Items), w.ClipCount)
				}
				if len(w.Items) > 0 {
					oldest = w.Items[len(w.Items)-1].ID
				}
			}
		}
		return clips, oldest
	}
	if n, oldest := count(); n != total || oldest != "ev_c000" {
		t.Fatalf("after the cutover: %d clips, oldest %s, want %d and ev_c000", n, oldest, total)
	}
	if got := crewClipsBetween(ctx, nk, "over", math.MinInt64, math.MaxInt64); len(got) != total {
		t.Fatalf("clip readers after the cutover: %d clips", len(got))
	}

	t.Setenv("MELLO_LEDGER_HISTORY_READ", "doc")
	if n, oldest := count(); n != CrewClipsMaxRetained || oldest != "ev_c030" {
		t.Fatalf("under the kill switch: %d clips, oldest %s, want %d and ev_c030", n, oldest, CrewClipsMaxRetained)
	}
	if got := crewClipsBetween(ctx, nk, "over", math.MinInt64, math.MaxInt64); len(got) != CrewClipsMaxRetained {
		t.Fatalf("clip readers under the kill switch: %d clips", len(got))
	}
	// A dropped clip is not found by its id, as before the cutover.
	if got := crewClipsWithID(ctx, nk, "over", "c000"); len(got) != 0 {
		t.Fatalf("a dropped clip under the kill switch = %+v", got)
	}

	t.Setenv("MELLO_LEDGER_HISTORY_READ", "")
	if n, _ := count(); n != total {
		t.Fatalf("without the kill switch: %d clips, want %d", n, total)
	}
}
