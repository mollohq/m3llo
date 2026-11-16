package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Phase 1b of the durable ledger: the zone feed reads one page from the store.

// zonesOfEveryPage is the zone feed of a viewer as JSON, one entry for each
// page of Crew history.
func zonesOfEveryPage(t *testing.T, nk *fakeQuestsNk, crewID, viewer string, nowMs int64) []string {
	t.Helper()
	var pages []string
	before := int64(0)
	for {
		z := hZones(nk, crewID, viewer, before, nowMs)
		raw, err := json.Marshal(z)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, string(raw))
		if z.HistoryNext == 0 {
			return pages
		}
		if len(pages) > 200 {
			t.Fatal("Crew history does not end")
		}
		before = z.HistoryNext
	}
}

// seedRandomCrew writes a crew of 30 weeks to the legacy documents: clips,
// stream sessions and recaps at random times, and some on the borders of a
// week and of the free window.
func seedRandomCrew(t *testing.T, nk *fakeQuestsNk, crewID string, rng *rand.Rand, freeDays int) {
	t.Helper()
	nk.groups[crewID] = &api.Group{Id: crewID, CreateTime: timestamppb.New(time.UnixMilli(hAt(-31, 3, 0)))}
	start := hNow - int64(freeDays)*hDay
	times := []int64{
		start - 1, start, start + 1, // the border of the free window
		hAt(-3, 0, 0), hAt(-3, 0, 0) - 1, hAt(-7, 0, 0), hAt(-7, 0, 0) - 1, // Monday 00:00 and the ms before
		hNow - 1, hNow - hHour,
	}
	for i := 0; i < 120; i++ {
		times = append(times, hNow-rng.Int63n(30*7*hDay))
	}
	actors := []string{"u1", "u2"}
	for i, ts := range times {
		actor := actors[rng.Intn(2)]
		if rng.Intn(3) == 0 {
			var snaps []string
			for n := rng.Intn(5); n > 0; n-- {
				snaps = append(snaps, fmt.Sprintf("https://m.test/%s/%d/%d.jpg", crewID, i, n))
			}
			hStream(t, nk, crewID, fmt.Sprintf("s%d", i), actor, ts, 1+rng.Intn(90), snaps...)
			continue
		}
		c := StoredClip{ClipID: fmt.Sprintf("c%d", i), ActorID: actor, Ts: ts, DurationSeconds: float64(1 + rng.Intn(59))}
		if rng.Intn(2) == 0 {
			c.ClipType, c.Width, c.Height, c.Game = clipTypeGame, 1920, 1080, "CS2"
			if rng.Intn(2) == 0 {
				c.PosterURL = fmt.Sprintf("https://m.test/%s/%d.jpg", crewID, i)
				c.MediaURL = fmt.Sprintf("https://m.test/%s/%d.mp4", crewID, i)
			}
		}
		hClip(t, nk, crewID, c)
	}
	// A recap for two weeks of three, oldest first. Every fourth one is
	// empty. The newest ones are empty, so the board looks further back.
	for week := -29; week <= -1; week++ {
		if rng.Intn(3) == 0 {
			continue
		}
		r := WeeklyRecapData{WeekStart: hAt(week, 0, 0)}
		if rng.Intn(4) != 0 && week < -2 {
			r.TotalHangoutMin, r.TotalMin, r.ClipCount = 1+rng.Intn(500), 1+rng.Intn(500), rng.Intn(9)
		}
		hRecap(t, nk, crewID, r)
	}
}

// The zone feed of a crew is the same JSON from the store as from the
// documents: every page of Crew history, for a viewer with and without
// m3llo+, for several lengths of the free window.
func TestZonesFromTheStoreEqualTheDocuments(t *testing.T) {
	for _, freeDays := range []int{7, 1, 30, 90} {
		for seed := int64(1); seed <= 4; seed++ {
			t.Run(fmt.Sprintf("free_days_%d_seed_%d", freeDays, seed), func(t *testing.T) {
				nk := hCrew(t, "rc")
				nk.put(FeedConfigCollection, FeedConfigKey, SystemUserID, FeedConfig{FreeDays: freeDays})
				seedRandomCrew(t, nk, "rc", rand.New(rand.NewSource(seed)), freeDays)

				fromDocs := map[string][]string{}
				for _, viewer := range []string{"u1", "u2"} {
					fromDocs[viewer] = zonesOfEveryPage(t, nk, "rc", viewer, hNow)
				}
				if len(fromDocs["u1"]) < 4 {
					t.Fatalf("the crew has %d pages of history, want a crew with many", len(fromDocs["u1"]))
				}

				// The import and both cutovers.
				importAndCutOver(t, nk)
				for _, viewer := range []string{"u1", "u2"} {
					fromStore := zonesOfEveryPage(t, nk, "rc", viewer, hNow)
					if len(fromStore) != len(fromDocs[viewer]) {
						t.Fatalf("viewer %s: %d pages from the store, %d from the documents", viewer, len(fromStore), len(fromDocs[viewer]))
					}
					for page := range fromStore {
						if fromStore[page] != fromDocs[viewer][page] {
							t.Fatalf("viewer %s page %d differs\nstore:     %s\ndocuments: %s", viewer, page, fromStore[page], fromDocs[viewer][page])
						}
					}
				}
			})
		}
	}
}

// A dormant crew: nothing in the free window, the newest six recaps empty.
// Every page has the last activity and the recap of the board, also a page
// whose weeks hold neither.
func TestZonesOfADormantCrewFromTheStoreEqualTheDocuments(t *testing.T) {
	for _, newestKind := range []string{"clip", "stream"} {
		t.Run("newest_is_a_"+newestKind, func(t *testing.T) {
			nk := hCrew(t, "dc")
			rng := rand.New(rand.NewSource(7))
			newest := hNow - 10*hDay
			clipTs, streamTs := newest, newest-hDay
			if newestKind == "stream" {
				clipTs, streamTs = newest-hDay, newest
			}
			hClip(t, nk, "dc", StoredClip{ClipID: "newest", ActorID: "u1", Ts: clipTs, DurationSeconds: 9})
			hStream(t, nk, "dc", "newest", "u2", streamTs, 30, "https://m.test/n.jpg")
			for i := 0; i < 60; i++ {
				ts := hNow - 12*hDay - rng.Int63n(40*7*hDay)
				if i%4 == 0 {
					hStream(t, nk, "dc", fmt.Sprintf("s%d", i), "u2", ts, 20)
				} else {
					hClip(t, nk, "dc", StoredClip{ClipID: fmt.Sprintf("c%d", i), ActorID: "u1", Ts: ts, DurationSeconds: 9})
				}
			}
			hRecap(t, nk, "dc", WeeklyRecapData{WeekStart: hAt(-7, 0, 0), TotalHangoutMin: 40, TotalMin: 40})
			for week := -6; week <= -1; week++ {
				hRecap(t, nk, "dc", WeeklyRecapData{WeekStart: hAt(week, 0, 0)})
			}

			fromDocs := zonesOfEveryPage(t, nk, "dc", "u2", hNow)
			importAndCutOver(t, nk)
			fromStore := zonesOfEveryPage(t, nk, "dc", "u2", hNow)
			if len(fromDocs) < 5 || len(fromStore) != len(fromDocs) {
				t.Fatalf("%d pages from the documents, %d from the store", len(fromDocs), len(fromStore))
			}
			for page := range fromStore {
				if fromStore[page] != fromDocs[page] {
					t.Fatalf("page %d differs\nstore:     %s\ndocuments: %s", page, fromStore[page], fromDocs[page])
				}
			}
			// The last page still knows the newest clip or stream session and
			// the recap of week -7.
			var last FeedZones
			if err := json.Unmarshal([]byte(fromStore[len(fromStore)-1]), &last); err != nil {
				t.Fatal(err)
			}
			if last.Board.Mode != "quiet" || last.Board.LastActivityMs != newest || last.Board.Recap == nil || last.Board.Recap.ID != recapEntryID(hAt(-7, 0, 0)) {
				t.Fatalf("board of the last page = %+v", last.Board)
			}
		})
	}
}

// A crew far over the old caps: 3000 clips and 600 stream sessions in three
// years. A page of the feed reads the rows of its weeks, not the crew.
func TestZonesReadOnePageOfALargeCrew(t *testing.T) {
	ctx := context.Background()
	store := useHistoryStoreReads(t)
	nk := hCrew(t, "big")
	var rows []ledgerRow
	for i := 0; i < 3000; i++ {
		c := StoredClip{EventID: fmt.Sprintf("c%04d", i), ClipID: fmt.Sprintf("c%04d", i), ActorID: "u1", Ts: hNow - int64(i)*9*hHour, DurationSeconds: 9}
		rows = append(rows, clipRow(c))
	}
	for i := 0; i < 600; i++ {
		rows = append(rows, streamSessionRow(StoredStreamSession{EventID: fmt.Sprintf("s%04d", i), SessionID: fmt.Sprintf("sess%04d", i), StreamerID: "u2",
			Ts: hNow - int64(i)*45*hHour, DurationMin: 10, SnapshotURLs: []string{}}))
	}
	for week := -160; week < 0; week++ {
		rows = append(rows, recapRow(WeeklyRecapData{WeekStart: hAt(week, 0, 0), TotalHangoutMin: 10, TotalMin: 10}))
	}
	if n, err := store.Put(ctx, "big", rows); err != nil || n != len(rows) {
		t.Fatalf("put: %d err=%v", n, err)
	}
	counter := newQueryCounter(store)
	useLedger(t, counter)

	// One week holds about 19 clips and 4 stream sessions.
	page := func(before int64) FeedZones {
		counter.reset()
		z := hZones(nk, "big", "u2", before, hNow)
		if len(z.History) != zonesHistoryPageWeeks || z.HistoryNext == 0 {
			t.Fatalf("page before %d: %d weeks, next %d", before, len(z.History), z.HistoryNext)
		}
		queries, rows := counter.total()
		t.Logf("page before %d: %d rows, %d queries: %s", before, rows, queries, counter)
		if rows > 250 || queries > 11 {
			t.Fatalf("page before %d read %d rows in %d queries: %s", before, rows, queries, counter)
		}
		return z
	}
	first := page(0)
	if len(first.Clips) < 15 || first.Board.Recap == nil || first.Board.LastActivityMs != hNow {
		t.Fatalf("first page: %d window clips, board %+v", len(first.Clips), first.Board)
	}
	// Two years back.
	deep := page(hAt(-104, 0, 0))
	if deep.History[0].WeekStart != hAt(-105, 0, 0) || deep.History[0].ClipCount < 15 || deep.History[0].Recap == nil {
		t.Fatalf("deep page: first week %+v", deep.History[0])
	}
	if deep.Board.LastActivityMs != hNow || len(deep.Clips) != len(first.Clips) {
		t.Fatalf("deep page: board %+v, %d window clips", deep.Board, len(deep.Clips))
	}

	// Every page: every clip and stream session is there once.
	clips, streams := len(first.Clips), 0
	if first.Stage.Entry != nil {
		t.Fatalf("stage = %+v", first.Stage)
	}
	for before := int64(0); ; {
		z := hZones(nk, "big", "u2", before, hNow)
		for _, w := range z.History {
			clips += w.ClipCount
			streams += w.StreamCount
		}
		if z.HistoryNext == 0 {
			break
		}
		before = z.HistoryNext
	}
	window := 0
	for i := 0; i < 600; i++ {
		if int64(i)*45*hHour <= 7*hDay {
			window++
		}
	}
	if clips != 3000 || streams != 600-window {
		t.Fatalf("the pages hold %d clips and %d stream sessions, want 3000 and %d", clips, streams, 600-window)
	}
}

// hStreamEnd ends a stream with its own event id for a session id: the ledger
// event, then the durable copy, which holds one entry for a session id.
func hStreamEnd(t *testing.T, nk *fakeQuestsNk, crewID, eventID, sessionID string, ts int64) {
	t.Helper()
	data := StreamSessionData{SessionID: sessionID, StreamerID: "u2", StreamerName: "Bo", Title: "Stream " + sessionID, DurationMin: 10, SnapshotURLs: []string{}}
	ev := CrewEvent{ID: eventID, CrewID: crewID, Type: "stream_session", ActorID: "u2", Timestamp: ts, Score: 30, Data: data}
	if err := AppendCrewEvent(context.Background(), nk, crewID, ev); err != nil {
		t.Fatal(err)
	}
	if err := UpsertStreamSession(context.Background(), nk, crewID, storedStreamSessionFrom(ev.ID, ev.Timestamp, ev.Score, data)); err != nil {
		t.Fatal(err)
	}
}

// Two stream_session events for one session id, in different weeks. The
// store holds two rows and the document one entry. Crew history from the
// store has the pages of the document: the older row does not count as a
// week, and no page lists the session a second time.
func TestZonesFoldTwoRowsOfOneSessionBeforeTheWeeksAreCounted(t *testing.T) {
	useHistoryStoreReads(t)
	nk := hCrew(t, "dup")
	// Session x: the older row alone in week -5, the newer row in week -3.
	hStreamEnd(t, nk, "dup", "x_old", "sess_x", hAt(-5, 2, 0))
	hStreamEnd(t, nk, "dup", "x_new", "sess_x", hAt(-3, 2, 0))
	// Session y: the older row alone in week -9, the newer row in week -2.
	hStreamEnd(t, nk, "dup", "y_old", "sess_y", hAt(-9, 2, 0))
	hStreamEnd(t, nk, "dup", "y_new", "sess_y", hAt(-2, 2, 0))
	for _, week := range []int{-4, -6, -7, -8} {
		hClip(t, nk, "dup", StoredClip{ClipID: fmt.Sprintf("c%d", -week), ActorID: "u1", Ts: hAt(week, 1, 0), DurationSeconds: 9})
	}

	fromStore := zonesOfEveryPage(t, nk, "dup", "u2", hNow)
	t.Setenv("MELLO_LEDGER_HISTORY_READ", "doc")
	fromDocs := zonesOfEveryPage(t, nk, "dup", "u2", hNow)
	if len(fromDocs) != 2 {
		t.Fatalf("the documents give %d pages, want 2", len(fromDocs))
	}
	if len(fromStore) != len(fromDocs) {
		t.Fatalf("%d pages from the store, %d from the documents\nstore:     %v\ndocuments: %v", len(fromStore), len(fromDocs), fromStore, fromDocs)
	}
	listed := map[string]int{}
	for page := range fromStore {
		if fromStore[page] != fromDocs[page] {
			t.Fatalf("page %d differs\nstore:     %s\ndocuments: %s", page, fromStore[page], fromDocs[page])
		}
		var z FeedZones
		if err := json.Unmarshal([]byte(fromStore[page]), &z); err != nil {
			t.Fatal(err)
		}
		for _, w := range z.History {
			for _, item := range w.Items {
				listed[item.ID]++
			}
		}
	}
	if listed["x_new"] != 1 || listed["y_new"] != 1 || listed["x_old"] != 0 || listed["y_old"] != 0 {
		t.Fatalf("stream sessions of the pages = %v, want each session once, the newer row", listed)
	}
}

// seedLargeCrewRows puts a crew far over the old caps into the store: 3000
// clips, 600 stream sessions and 160 recaps in three years before nowMs. One
// week holds about 19 clips and 4 stream sessions.
func seedLargeCrewRows(t *testing.T, store ledgerStore, crewID string, nowMs int64) {
	t.Helper()
	var rows []ledgerRow
	for i := 0; i < 3000; i++ {
		c := StoredClip{EventID: fmt.Sprintf("c%04d", i), ClipID: fmt.Sprintf("c%04d", i), ActorID: "u1", Ts: nowMs - int64(i)*9*hHour, DurationSeconds: 9}
		if i%3 == 0 {
			c.ClipType = clipTypeGame
		}
		rows = append(rows, clipRow(c))
	}
	for i := 0; i < 600; i++ {
		rows = append(rows, streamSessionRow(StoredStreamSession{EventID: fmt.Sprintf("s%04d", i), SessionID: fmt.Sprintf("sess%04d", i), StreamerID: "u2",
			Ts: nowMs - int64(i)*45*hHour, DurationMin: 10, SnapshotURLs: []string{}}))
	}
	for week := int64(-160); week < 0; week++ {
		start := weekStartOf(nowMs) + week*7*hDay
		rows = append(rows, recapRow(WeeklyRecapData{WeekStart: start, WeekEnd: start + 7*hDay, GeneratedAt: start + 7*hDay + hHour, TotalHangoutMin: 10, TotalMin: 10}))
	}
	if n, err := store.Put(context.Background(), crewID, rows); err != nil || n != len(rows) {
		t.Fatalf("put: %d err=%v", n, err)
	}
}

// The store reads of one crew_feed call, through the RPC: the merged timeline
// and the zones. The crew is far over the old caps.
//
// The count, with W = 5 weeks read for a page of 4:
//
//	timeline   3  the 7-day view, the clips of 7 days, the newest recap
//	window     1  the clips and stream sessions of the free window
//	board      1  the recaps of the newest weeks
//	weeks      3  the clip weeks, the stream weeks, the recaps of the page
//	open week  1  its clips and stream sessions
//	locked     2  its teaser clips, its stream sessions
//	sessions   1  the rows of the session ids of the page
//
// A viewer with m3llo+ reads 3+1+1+3+W+1 = 14. A viewer without it reads at
// most 3+1+1+3+2W+1 = 19, and 18 when the newest week of the page is the
// one the free window starts in, which is read as an open week.
func TestCrewFeedLoadQueryCount(t *testing.T) {
	store := useHistoryStoreReads(t)
	nk := hCrew(t, "big")
	now := time.Now().UnixMilli()
	seedLargeCrewRows(t, store, "big", now)
	counter := newQueryCounter(store)
	useLedger(t, counter)

	load := func(viewer string, before int64) (FeedZones, int, int) {
		t.Helper()
		counter.reset()
		out, err := CrewFeedRPC(ctxWithUser(viewer), testLogger(), nil, nk, fmt.Sprintf(`{"crew_id":"big","history_before":%d}`, before))
		if err != nil {
			t.Fatal(err)
		}
		var resp struct {
			Zones FeedZones `json:"zones"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Zones.History) != zonesHistoryPageWeeks || resp.Zones.HistoryNext == 0 {
			t.Fatalf("viewer %s before %d: %d weeks, next %d", viewer, before, len(resp.Zones.History), resp.Zones.HistoryNext)
		}
		queries, rows := counter.total()
		t.Logf("viewer %s before %d: %d rows, %d queries: %s", viewer, before, rows, queries, counter)
		return resp.Zones, queries, rows
	}
	deep := weekStartOf(now) - 104*7*hDay

	// u2 has m3llo+: every week of the page is read whole.
	for _, before := range []int64{0, deep} {
		z, queries, rows := load("u2", before)
		if queries != 14 || rows > 260 {
			t.Fatalf("m3llo+ page before %d: %d queries and %d rows, want 14 queries and at most 260 rows: %s", before, queries, rows, counter)
		}
		// The first week of the first page can be a part of a week.
		if w := z.History[1]; w.Locked || len(w.Items) < 15 {
			t.Fatalf("m3llo+ page before %d: second week %+v", before, w)
		}
	}
	// u1 has not: a locked week costs two reads and at most 6 clip rows.
	for _, before := range []int64{0, deep} {
		z, queries, rows := load("u1", before)
		if queries < 18 || queries > 19 || rows > 170 {
			t.Fatalf("locked page before %d: %d queries and %d rows, want 18 or 19 queries and at most 170 rows: %s", before, queries, rows, counter)
		}
		last := z.History[len(z.History)-1]
		if !last.Locked || last.ClipCount < 15 || len(last.Teasers) != zonesTeasersMax || len(last.Items) != 0 {
			t.Fatalf("locked page before %d: last week %+v", before, last)
		}
	}
}
