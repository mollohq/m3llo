package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Phase 1b of the durable ledger: the clips.

func histClip(id, actor string, ts time.Time) StoredClip {
	return StoredClip{EventID: "ev_" + id, ClipID: id, ActorID: actor, Ts: ts.UnixMilli(), Score: 50, ClipType: clipTypeVoice, ClipperName: actor, DurationSeconds: 9}
}

func docClips(nk runtime.NakamaModule, crewID string) []StoredClip {
	doc, _ := readClipsDoc(context.Background(), nk, crewID)
	return doc.Clips
}

func storeClips(t *testing.T, store ledgerStore, crewID string) []StoredClip {
	t.Helper()
	return clipsFromRows(histRows(t, store, crewID, "clip"))
}

// versionedNk is a module that checks storage versions, with every read and
// every write one step.
func versionedNk() *serialNk {
	return &serialNk{fakeQuestsNk: newFakeQuestsNk()}
}

// Uploads at once to a crew that has no clips document yet. Each one that
// reports success is stored: the first write of the document does not replace
// the document of another writer.
func TestConcurrentFirstClipsLoseNothing(t *testing.T) {
	for _, phase := range []string{"before the cutover", "after the cutover"} {
		t.Run(phase, func(t *testing.T) {
			var store *memLedgerStore
			if phase == "after the cutover" {
				store = useHistoryStoreReads(t)
			} else {
				store = useMemLedger(t)
				resetLedgerCutover(t)
				resetHistoryCutover(t)
			}
			nk := versionedNk()
			const n = 6
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, errs[i] = AppendClip(context.Background(), nk, "crew", histClip(fmt.Sprintf("c%d", i), "u1", time.Now()))
				}(i)
			}
			wg.Wait()

			inDoc, inStore := map[string]bool{}, map[string]bool{}
			for _, c := range docClips(nk, "crew") {
				inDoc[c.ClipID] = true
			}
			for _, c := range storeClips(t, store, "crew") {
				inStore[c.ClipID] = true
			}
			ok := 0
			for i, err := range errs {
				id := fmt.Sprintf("c%d", i)
				if err == nil {
					ok++
				}
				if phase == "after the cutover" {
					// The store is the truth: every upload is stored.
					if err != nil || !inStore[id] {
						t.Errorf("clip %s: err=%v in store=%v", id, err, inStore[id])
					}
					continue
				}
				// The document is the truth: a success is in both, an error
				// in neither.
				if inDoc[id] != (err == nil) || inStore[id] != (err == nil) {
					t.Errorf("clip %s: err=%v in document=%v in store=%v", id, err, inDoc[id], inStore[id])
				}
			}
			if ok == 0 {
				t.Fatal("no upload succeeded")
			}
		})
	}
}

// Two members upload a clip with one clip_id at once. One of them has the
// clip. The other one gets errClipIDTaken, and no second clip is stored.
func TestClipIDIsUniqueUnderConcurrentUploads(t *testing.T) {
	for _, phase := range []string{"before the cutover", "after the cutover"} {
		t.Run(phase, func(t *testing.T) {
			var store *memLedgerStore
			if phase == "after the cutover" {
				store = useHistoryStoreReads(t)
			} else {
				store = useMemLedger(t)
				resetLedgerCutover(t)
				resetHistoryCutover(t)
			}
			nk := versionedNk()
			const rounds, actors = 10, 4
			for round := 0; round < rounds; round++ {
				crewID := fmt.Sprintf("crew%d", round)
				if round%2 == 1 {
					// Every second crew has a document already.
					if _, err := AppendClip(context.Background(), nk, crewID, histClip("seed", "u0", time.Now())); err != nil {
						t.Fatal(err)
					}
				}
				errs := make([]error, actors)
				prevs := make([]*StoredClip, actors)
				var wg sync.WaitGroup
				for i := 0; i < actors; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						c := histClip("same", fmt.Sprintf("u%d", i+1), time.Now())
						c.EventID = fmt.Sprintf("ev_%d_%d", round, i)
						prevs[i], errs[i] = AppendClip(context.Background(), nk, crewID, c)
					}(i)
				}
				wg.Wait()

				winners := 0
				for i, err := range errs {
					switch {
					case err == nil && prevs[i] == nil:
						winners++
					case err == nil:
						t.Errorf("round %d: actor %d got the clip of %s as its own", round, i, prevs[i].ActorID)
					case phase == "after the cutover" && !errors.Is(err, errClipIDTaken):
						t.Errorf("round %d: actor %d: %v, want errClipIDTaken", round, i, err)
					}
				}
				count := func(clips []StoredClip) int {
					n := 0
					for _, c := range clips {
						if c.ClipID == "same" {
							n++
						}
					}
					return n
				}
				inDoc, inStore := count(docClips(nk, crewID)), count(storeClips(t, store, crewID))
				if winners != 1 || inDoc != 1 || inStore != 1 {
					t.Fatalf("round %d: %d uploads succeeded, the document holds %d and the store %d clips with the clip_id, want 1 each (errors %v)",
						round, winners, inDoc, inStore, errs)
				}
			}
		})
	}
}

// A retry of the same member at once with the first call stores one clip, and
// both calls answer with it.
func TestConcurrentRetriesOfOneClipStoreOneClip(t *testing.T) {
	store := useHistoryStoreReads(t)
	nk := versionedNk()
	const calls = 6
	events := make([]string, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := histClip("retry", "u1", time.Now())
			c.EventID = fmt.Sprintf("ev_call%d", i)
			prev, err := AppendClip(context.Background(), nk, "crew", c)
			errs[i] = err
			events[i] = c.EventID
			if prev != nil {
				events[i] = prev.EventID
			}
		}(i)
	}
	wg.Wait()
	clips := storeClips(t, store, "crew")
	if len(clips) != 1 {
		t.Fatalf("the store holds %d clips, want 1", len(clips))
	}
	for i := range events {
		if errs[i] != nil || events[i] != clips[0].EventID {
			t.Fatalf("call %d: event %s err=%v, want the stored clip %s", i, events[i], errs[i], clips[0].EventID)
		}
	}
}

// failClipDocNk fails every write of a clips document.
type failClipDocNk struct{ *fakeGuestNk }

func (f failClipDocNk) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	for _, w := range writes {
		if w.Collection == CrewClipsCollection {
			return nil, errors.New("document write failed")
		}
	}
	return f.fakeGuestNk.StorageWrite(ctx, writes)
}

// failingPuts fails the writes of rows of phase 1b.
type failingPuts struct{ *memLedgerStore }

func (failingPuts) Put(context.Context, string, []ledgerRow) (int, error) {
	return 0, errors.New("store down")
}

func (failingPuts) PutUnique(context.Context, string, ledgerRow) ([]CrewEvent, error) {
	return nil, errors.New("store down")
}

func (failingPuts) UpdateData(context.Context, string, string, func(json.RawMessage) (json.RawMessage, error)) error {
	return errors.New("store down")
}

func TestClipWriteOrderBeforeTheCutover(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	now := time.Now()

	// The document fails: the caller gets the error, the store gets nothing.
	if _, err := AppendClip(ctx, failClipDocNk{newFakeGuestNk()}, "crew", histClip("a", "u1", now)); err == nil {
		t.Fatal("a failed document write reports success")
	}
	if n := len(storeClips(t, store, "crew")); n != 0 {
		t.Fatalf("the store holds %d clips after a failed document write", n)
	}

	// The store fails: the write succeeds, and the reconcile copies the clip.
	nk := newFakeGuestNk()
	useLedger(t, failingPuts{store})
	if _, err := AppendClip(ctx, nk, "crew", histClip("b", "u1", now)); err != nil {
		t.Fatalf("a failed store write fails the upload: %v", err)
	}
	set := func(c *StoredClip) { c.MediaURL = "https://m.test/b.mp4" }
	own := func(clips []StoredClip) *StoredClip { c, _ := existingClip(clips, "b", "u1"); return c }
	if err := updateStoredClip(ctx, nk, "crew", "b", own, set); err != nil {
		t.Fatalf("a failed store update fails the update: %v", err)
	}
	if len(docClips(nk, "crew")) != 1 || len(storeClips(t, store, "crew")) != 0 {
		t.Fatalf("document %d clips, store %d", len(docClips(nk, "crew")), len(storeClips(t, store, "crew")))
	}
	useLedger(t, store)
	r := reconcilePass(t, nk)
	if r.lastHist.Inserted != 1 || !r.lastHist.clean() {
		t.Fatalf("pass = %+v", r.lastHist)
	}
	if got := storeClips(t, store, "crew"); len(got) != 1 || got[0].MediaURL != "https://m.test/b.mp4" || !historySameJSON(got, docClips(nk, "crew")) {
		t.Fatalf("store after the reconcile = %+v", got)
	}

	// With both stores up, an update reaches both at once.
	if err := updateStoredClip(ctx, nk, "crew", "b", own, func(c *StoredClip) { c.PosterURL = "https://m.test/b.jpg" }); err != nil {
		t.Fatal(err)
	}
	if got := storeClips(t, store, "crew"); got[0].PosterURL != "https://m.test/b.jpg" || !historySameJSON(got, docClips(nk, "crew")) {
		t.Fatalf("store after an update = %+v, document %+v", got, docClips(nk, "crew"))
	}
}

func TestClipWriteOrderAfterTheCutover(t *testing.T) {
	ctx := context.Background()
	store := useHistoryStoreReads(t)
	now := time.Now()

	// The store fails: the caller gets the error, the document gets nothing.
	nk := newFakeGuestNk()
	useLedger(t, failingPuts{store})
	if _, err := AppendClip(ctx, nk, "crew", histClip("a", "u1", now)); err == nil {
		t.Fatal("a failed store write reports success")
	}
	if n := len(docClips(nk, "crew")); n != 0 {
		t.Fatalf("the document holds %d clips after a failed store write", n)
	}
	useLedger(t, store)

	// The document fails: the write succeeds and the clip is read.
	broken := failClipDocNk{nk}
	if _, err := AppendClip(ctx, broken, "crew", histClip("b", "u1", now)); err != nil {
		t.Fatalf("a failed document write fails the upload: %v", err)
	}
	set := func(c *StoredClip) { c.MediaURL = "https://m.test/b.mp4" }
	own := func(clips []StoredClip) *StoredClip { c, _ := existingClip(clips, "b", "u1"); return c }
	if err := updateStoredClip(ctx, broken, "crew", "b", own, set); err != nil {
		t.Fatalf("a failed document update fails the update: %v", err)
	}
	got := crewClipsBetween(ctx, broken, "crew", math.MinInt64, math.MaxInt64)
	if len(got) != 1 || got[0].ClipID != "b" || got[0].MediaURL != "https://m.test/b.mp4" || len(docClips(nk, "crew")) != 0 {
		t.Fatalf("readers see %+v, the document holds %d clips", got, len(docClips(nk, "crew")))
	}
	// An update of a clip the store does not hold is an error.
	if err := updateStoredClip(ctx, nk, "crew", "nope", own, set); err == nil {
		t.Fatal("an update of an unknown clip reports success")
	}
}

// The document is capped at 250 clips and the store is not. The reconcile
// never deletes a row that the document dropped, before or after the
// cutover, and after the cutover the readers have every clip.
func TestClipReconcileKeepsWhatTheDocumentDropped(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	nk.put(InviteCodeCollection, testGuestCode, SystemUserID, map[string]string{"crew_id": "crew", "inviter_user_id": "u1"})
	nk.groups["crew"] = &api.Group{Id: "crew", Name: "Crew"}
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()

	const total = CrewClipsMaxRetained + 7
	for i := 0; i < total; i++ {
		if _, err := AppendClip(ctx, nk, "crew", histClip(charID(i), "u1", now.Add(-time.Duration(total-i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(docClips(nk, "crew")) != CrewClipsMaxRetained || len(storeClips(t, store, "crew")) != total {
		t.Fatalf("document %d clips, store %d", len(docClips(nk, "crew")), len(storeClips(t, store, "crew")))
	}
	// Before the cutover the readers see the document.
	if n := len(crewClipsBetween(ctx, nk, "crew", math.MinInt64, math.MaxInt64)); n != CrewClipsMaxRetained {
		t.Fatalf("readers see %d clips before the cutover", n)
	}

	for pass := 1; pass <= 3; pass++ {
		r := reconcilePass(t, nk)
		if !r.lastHist.clean() || r.lastHist.Inserted != 0 || r.lastHist.Updated != 0 {
			t.Fatalf("pass %d: %+v", pass, r.lastHist)
		}
		if n := len(storeClips(t, store, "crew")); n != total {
			t.Fatalf("pass %d: the store holds %d clips, want %d", pass, n, total)
		}
	}
	if historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	all := crewClipsBetween(ctx, nk, "crew", math.MinInt64, math.MaxInt64)
	if len(all) != total || all[0].ClipID != charID(0) {
		t.Fatalf("readers see %d clips after the cutover, first %+v", len(all), all[0])
	}
	// A clip that the document dropped still gets its media URL.
	own := func(clips []StoredClip) *StoredClip { c, _ := existingClip(clips, charID(0), "u1"); return c }
	if err := updateStoredClip(ctx, nk, "crew", charID(0), own, func(c *StoredClip) { c.MediaURL = "https://m.test/0.mp4" }); err != nil {
		t.Fatal(err)
	}
	if got := crewClipsWithID(ctx, nk, "crew", charID(0)); len(got) != 1 || got[0].MediaURL != "https://m.test/0.mp4" {
		t.Fatalf("the dropped clip = %+v", got)
	}
	// The guest feed counts every clip and shows the newest.
	out, err := GuestCrewFeedRPC(ctx, testLogger(), nil, nk, `{"code":"abcd-efgh"}`)
	if err != nil {
		t.Fatal(err)
	}
	var feed struct {
		Clips     []guestClip `json:"clips"`
		ClipCount int         `json:"clip_count"`
	}
	if err := json.Unmarshal([]byte(out), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.ClipCount != total || len(feed.Clips) != guestFeedClipLimit || feed.Clips[0].Ts != now.Add(-time.Minute).UnixMilli() {
		t.Fatalf("guest feed: clip_count=%d clips=%+v", feed.ClipCount, feed.Clips)
	}
}

// A clips document from before clip_id was checked can hold one clip_id
// twice. Both entries are kept, as the feed showed both.
func TestClipReconcileKeepsTwoEntriesOfOneClipID(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()

	first, second := histClip("dup", "u1", now.Add(-2*time.Hour)), histClip("dup", "u1", now.Add(-time.Hour))
	second.EventID = "ev_dup_again"
	noID := histClip("no_event_id", "u1", now)
	noID.EventID = ""
	nk.put(CrewClipsCollection, "crew", SystemUserID, CrewClipsDoc{CrewID: "crew", Clips: []StoredClip{first, second, noID}})

	r := reconcilePass(t, nk)
	if r.lastHist.Inserted != 2 || r.lastHist.NoID != 1 || !r.lastHist.clean() || !historyMarkerSet(nk) {
		t.Fatalf("pass = %+v marker=%s", r.lastHist, r.lastHistMarker)
	}
	if got := storeClips(t, store, "crew"); !historySameJSON(got, []StoredClip{first, second}) {
		t.Fatalf("store = %+v", got)
	}
	// A retry of that clip_id answers with the first entry, as before.
	prev, err := AppendClip(ctx, nk, "crew", histClip("dup", "u1", now))
	if err != nil || prev == nil || prev.EventID != "ev_dup" {
		t.Fatalf("retry: prev=%+v err=%v", prev, err)
	}
}

// Before the cutover a row takes the content of the document. After it the
// store is the truth and the document changes nothing.
func TestClipReconcileTakesTheDocumentOnlyBeforeTheCutover(t *testing.T) {
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	t.Setenv("MELLO_LEDGER_SOAK_SECONDS", "")
	now := time.Now()

	c := histClip("a", "u1", now)
	if _, err := store.Put(ctx, "crew", []ledgerRow{clipRow(c)}); err != nil {
		t.Fatal(err)
	}
	c.MediaURL = "https://m.test/a.mp4"
	nk.put(CrewClipsCollection, "crew", SystemUserID, CrewClipsDoc{CrewID: "crew", Clips: []StoredClip{c}})

	r := reconcilePass(t, nk)
	if r.lastHist.Updated != 1 || !r.lastHist.clean() {
		t.Fatalf("pass before the cutover = %+v", r.lastHist)
	}
	if got := storeClips(t, store, "crew"); len(got) != 1 || got[0].MediaURL != "https://m.test/a.mp4" {
		t.Fatalf("store after the repair = %+v", got)
	}

	noSoak(t)
	if reconcilePass(t, nk); !historyMarkerSet(nk) {
		t.Fatal("no history marker")
	}
	c.MediaURL = ""
	nk.put(CrewClipsCollection, "crew", SystemUserID, CrewClipsDoc{CrewID: "crew", Clips: []StoredClip{c}})
	if r = reconcilePass(t, nk); r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("pass after the cutover = %+v", r.lastHist)
	}
	if got := storeClips(t, store, "crew"); got[0].MediaURL != "https://m.test/a.mp4" {
		t.Fatalf("store after the cutover = %+v", got)
	}
}

// After the cutover an instance of an older release, or a rollback period,
// writes a clip to the document alone. The next pass stores it. A clip that
// only the store holds stays.
func TestClipReconcileStoresADocumentOnlyClipAfterTheCutover(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()

	if _, err := AppendClip(ctx, nk, "crew", histClip("first", "u1", now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if reconcilePass(t, nk); historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	// The store alone: the document write failed.
	if _, err := AppendClip(ctx, failClipDocNk{nk}, "crew", histClip("store_only", "u1", now.Add(-30*time.Minute))); err != nil {
		t.Fatal(err)
	}
	// The document alone: an older release wrote it.
	if _, err := appendClipDoc(ctx, nk, "crew", histClip("doc_only", "u2", now)); err != nil {
		t.Fatal(err)
	}

	r := reconcilePass(t, nk)
	if r.lastHist.Inserted != 1 || r.lastHist.Updated != 0 || !r.lastHist.clean() {
		t.Fatalf("pass = %+v", r.lastHist)
	}
	got := crewClipsBetween(ctx, nk, "crew", math.MinInt64, math.MaxInt64)
	if len(got) != 3 || got[0].ClipID != "first" || got[1].ClipID != "store_only" || got[2].ClipID != "doc_only" {
		t.Fatalf("clips after the pass = %+v", got)
	}
	if r = reconcilePass(t, nk); r.lastHist.Inserted != 0 || !r.lastHist.clean() {
		t.Fatalf("second pass = %+v", r.lastHist)
	}
	if n := len(storeClips(t, store, "crew")); n != 3 {
		t.Fatalf("the store holds %d clips", n)
	}
}

// failingClipReads fails every read of the store that the clip readers use.
type failingClipReads struct{ *memLedgerStore }

func (failingClipReads) Between(context.Context, string, int64, int64, []string) ([]CrewEvent, error) {
	return nil, context.DeadlineExceeded
}

func (failingClipReads) ByRef(context.Context, string, string, string) ([]CrewEvent, error) {
	return nil, context.DeadlineExceeded
}

func (failingClipReads) NewestN(context.Context, string, []string, int64, int) ([]CrewEvent, error) {
	return nil, context.DeadlineExceeded
}

func TestClipReadersFallBackToTheDocumentWhenTheStoreFails(t *testing.T) {
	ctx := context.Background()
	useHistoryStoreReads(t)
	useLedger(t, failingClipReads{newMemLedgerStore()})
	nk := newFakeGuestNk()
	nk.put(CrewClipsCollection, "crew", SystemUserID, CrewClipsDoc{CrewID: "crew", Clips: []StoredClip{histClip("in_doc", "u1", time.Now())}})
	if got := crewClipsBetween(ctx, nk, "crew", 0, math.MaxInt64); len(got) != 1 || got[0].ClipID != "in_doc" {
		t.Fatalf("clips between = %+v", got)
	}
	if got := crewClipsWithID(ctx, nk, "crew", "in_doc"); len(got) != 1 {
		t.Fatalf("clips with id = %+v", got)
	}
	if got, more := crewClipsForGuestFeed(ctx, nk, "crew"); len(got) != 1 || more != 0 {
		t.Fatalf("guest clips = %+v more=%d", got, more)
	}
}

func clipsWithID(clips []StoredClip, clipID string) int {
	n := 0
	for _, c := range clips {
		if c.ClipID == clipID {
			n++
		}
	}
	return n
}

// Two nodes do not agree about the marker for 30 seconds. The node after the
// cutover stored a clip in the store. The node before it then takes the same
// clip_id into the document. The store keeps one row for the clip_id, also
// after the reconcile.
func TestClipIDHasOneRowWhenTwoNodesDisagreeAboutTheMarker(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	now := time.Now()

	// The node after the cutover: the store first. Its document write lost.
	first := histClip("same", "u1", now.Add(-time.Second))
	if held, err := store.PutUnique(ctx, "crew", clipRow(first)); err != nil || len(held) != 0 {
		t.Fatalf("put unique: held=%v err=%v", held, err)
	}
	// The node before the cutover: the document first, and it is free.
	second := histClip("same", "u2", now)
	second.EventID = "ev_same_again"
	if prev, err := AppendClip(ctx, nk, "crew", second); err != nil || prev != nil {
		t.Fatalf("append on the node before the cutover: prev=%+v err=%v", prev, err)
	}
	if n := clipsWithID(storeClips(t, store, "crew"), "same"); n != 1 {
		t.Fatalf("the store holds %d rows for the clip_id after the upload, want 1", n)
	}
	for pass := 1; pass <= 2; pass++ {
		r := reconcilePass(t, nk)
		if r.lastHist.Inserted != 0 || r.lastHist.RefHeld != 1 || !r.lastHist.clean() {
			t.Fatalf("pass %d = %+v", pass, r.lastHist)
		}
		if got := storeClips(t, store, "crew"); len(got) != 1 || got[0].EventID != first.EventID {
			t.Fatalf("pass %d: store = %+v, want the first row alone", pass, got)
		}
	}
}

// The kill switch on a crew over the document cap. The document dropped a
// clip that the store holds. An upload of that clip_id is free in the
// document, which is the truth again. The store does not get a second row.
func TestKillSwitchOnACrewOverTheCapKeepsOneRowForAClipID(t *testing.T) {
	noSoak(t)
	ctx := context.Background()
	nk := newFakeGuestNk()
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	prevRecheck := ledgerMarkerRecheck
	ledgerMarkerRecheck = 0
	t.Cleanup(func() { ledgerMarkerRecheck = prevRecheck })
	now := time.Now()

	const total = CrewClipsMaxRetained + 3
	for i := 0; i < total; i++ {
		if _, err := AppendClip(ctx, nk, "crew", histClip(charID(i), "u1", now.Add(-time.Duration(total-i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if reconcilePass(t, nk); historyTruthStore(ctx, nk) == nil {
		t.Fatal("no cutover")
	}
	dropped := charID(0)
	if clipsWithID(docClips(nk, "crew"), dropped) != 0 || clipsWithID(storeClips(t, store, "crew"), dropped) != 1 {
		t.Fatal("the seed is wrong: want the oldest clip in the store alone")
	}

	t.Setenv("MELLO_LEDGER_HISTORY_READ", "doc")
	again := histClip(dropped, "u1", now)
	again.EventID = "ev_again"
	if prev, err := AppendClip(ctx, nk, "crew", again); err != nil || prev != nil {
		t.Fatalf("append under the kill switch: prev=%+v err=%v", prev, err)
	}
	if clipsWithID(docClips(nk, "crew"), dropped) != 1 {
		t.Fatal("the document did not take the clip")
	}
	if n := clipsWithID(storeClips(t, store, "crew"), dropped); n != 1 {
		t.Fatalf("the store holds %d rows for the clip_id after the upload, want 1", n)
	}
	for pass := 1; pass <= 2; pass++ {
		r := reconcilePass(t, nk)
		if r.lastHist.Inserted != 0 || r.lastHist.RefHeld != 1 || !r.lastHist.clean() {
			t.Fatalf("pass %d = %+v", pass, r.lastHist)
		}
		if n := clipsWithID(storeClips(t, store, "crew"), dropped); n != 1 {
			t.Fatalf("pass %d: the store holds %d rows for the clip_id, want 1", pass, n)
		}
	}
	if n := len(storeClips(t, store, "crew")); n != total {
		t.Fatalf("the store holds %d clips, want %d", n, total)
	}
}
