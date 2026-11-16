package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	purgeCrew = "11111111-1111-4111-8111-111111111111"
	otherCrew = "22222222-2222-4222-8222-222222222222"
)

// usePurgeBuckets points the purge at the fake: the clips bucket, the
// snapshots bucket and the bucket of the banners and the emoji. The purge
// runs inline.
func usePurgeBuckets(t *testing.T) *fakeS3 {
	t.Helper()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	prevGifts, prevAsync := crewGiftsBucket, crewPurgeAsync
	client := newS3Client(f.server.URL, "test", "test")
	crewGiftsBucket = func() (*s3.Client, string) { return client, "media" }
	crewPurgeAsync = func(fn func()) { fn() }
	t.Cleanup(func() { crewGiftsBucket, crewPurgeAsync = prevGifts, prevAsync })
	return f
}

// purgeTestDocs are the system documents of a crew that the purge must
// remove, written out here and not taken from crewPurgeDocs.
func purgeTestDocs(crewID string) [][2]string {
	return [][2]string{
		{"crew_events", crewID}, {"crew_clips", crewID}, {"crew_stream_sessions", crewID}, {"crew_recaps", crewID},
		{"crew_quests", crewID}, {"crew_profile", crewID}, {"crew_emoji", crewID}, {"crew_avatars", crewID},
		{"voice_channels", crewID}, {"crew_head2head", "holders_" + crewID},
		{"recap_pushes", crewID}, {"recap_pushes", "belt_" + crewID}, {"stream_meta", crewID},
	}
}

// seedPurgeCrew gives a crew everything the purge must remove: events, clips,
// a stream session, a recap, the other documents and the media. The writes go
// through the writers of the module, so both stores hold the data in the
// order of the phase.
func seedPurgeCrew(t *testing.T, nk *delNk, f *fakeS3, crewID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	nk.groups[crewID] = &api.Group{Id: crewID}
	nk.addMember(crewID, "owner-"+crewID, "Owner")
	nk.members[crewID][0].State = wrapperspb.Int32(0)

	sess := StreamSessionData{SessionID: "s1", StreamerID: "u1", StreamerName: "Ann", Title: "Stream", SnapshotURLs: []string{}}
	events := []CrewEvent{
		{ID: "g1-" + crewID, Type: "game_session", ActorID: "u1", Timestamp: now.Add(-time.Hour).UnixMilli(), Data: GameSessionData{GameName: "Game", PlayerIDs: []string{"u1"}}},
		{ID: "st1-" + crewID, Type: "stream_session", ActorID: "u1", Timestamp: now.Add(-2 * time.Hour).UnixMilli(), Score: 30, Data: sess},
	}
	for _, ev := range events {
		if err := AppendCrewEvent(ctx, nk, crewID, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := UpsertStreamSession(ctx, nk, crewID, storedStreamSessionFrom(events[1].ID, events[1].Timestamp, 30, sess)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []StoredClip{histClip("c1", "u1", now.Add(-3*time.Hour)), histClip("c2", "u2", now.Add(-30*24*time.Hour))} {
		c.EventID += "-" + crewID
		if _, err := AppendClip(ctx, nk, crewID, c); err != nil {
			t.Fatal(err)
		}
	}
	recap := histRecap(time.UnixMilli(weekStartOf(now.Add(-14*24*time.Hour).UnixMilli())), 90)
	recap.CrewID = crewID
	if err := AppendRecap(ctx, nk, crewID, recap); err != nil {
		t.Fatal(err)
	}
	for _, d := range purgeTestDocs(crewID) {
		if !nk.has(d[0], d[1]) {
			nk.put(d[0], d[1], SystemUserID, map[string]string{"crew_id": crewID})
		}
	}
	nk.put(StreamMetaCollection, crewID, SystemUserID, StreamMeta{StreamID: "live", CrewID: crewID, StreamerID: "u1"})
	nk.put(StreamCollection, crewID, "u1", map[string]string{"crew_id": crewID})

	f.put("clips", "crews/"+crewID+"/c1.mp4", 10, time.Hour)
	f.put("clips", "crews/"+crewID+"/c2.mp4", 10, time.Hour)
	f.put("clips", "crews/"+crewID+"/c2.jpg", 10, time.Hour)
	f.put("snaps", "snapshots/"+crewID+"/s1/1.jpg", 10, time.Hour)
	f.put("snaps", "snapshots/"+crewID+"/s1/2.jpg", 10, time.Hour)
	f.put("media", "crew-banners/"+crewID+"/b.jpg", 10, time.Hour)
	f.put("media", "crew-emoji/"+crewID+"/e.png", 10, time.Hour)
	// The chat images of the crew are not part of the purge.
	f.put("media", crewID+"/u1/img.jpg", 10, time.Hour)
}

// crewLeftovers lists what an install still holds of a crew: documents, rows
// and objects under the prefixes of the purge.
func crewLeftovers(t *testing.T, nk *delNk, store ledgerStore, f *fakeS3, crewID string) []string {
	t.Helper()
	var left []string
	for _, d := range purgeTestDocs(crewID) {
		if nk.has(d[0], d[1]) {
			left = append(left, "doc "+d[0]+"/"+d[1])
		}
	}
	nk.mu.Lock()
	if _, ok := nk.storage[fakeStorageKey(StreamCollection, crewID, "u1")]; ok {
		left = append(left, "doc "+StreamCollection)
	}
	nk.mu.Unlock()
	for _, ev := range crewRows(t, store, crewID) {
		left = append(left, "row "+ev.Type+" "+ev.ID)
	}
	if crews, _ := store.UserCrews(context.Background(), "u1"); strings.Contains(strings.Join(crews, ","), crewID) {
		left = append(left, "user index")
	}
	for _, bucket := range []string{"clips", "snaps", "media"} {
		for _, key := range f.keys(bucket) {
			if strings.HasPrefix(key, crewID+"/") {
				continue // chat images
			}
			if strings.Contains(key, crewID) {
				left = append(left, "object "+bucket+"/"+key)
			}
		}
	}
	return left
}

func deleteCrewByOwner(t *testing.T, nk *delNk, crewID string) {
	t.Helper()
	if _, err := DeleteCrewRPC(ctxWithUser("owner-"+crewID), testLogger(), nil, nk, `{"crew_id":"`+crewID+`"}`); err != nil {
		t.Fatal(err)
	}
}

func purgeRecordOf(t *testing.T, nk runtime.NakamaModule, crewID string) crewPurgeRecord {
	t.Helper()
	rec, err := readCrewPurgeRecord(context.Background(), nk, crewID)
	if err != nil || rec == nil {
		t.Fatalf("purge record of %s = %+v err=%v", crewID, rec, err)
	}
	return *rec
}

// delete_crew removes everything of the crew in every phase, leaves the other
// crew alone, and a reconcile pass brings nothing back.
func TestPurgeCrewData(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := usePurgeBuckets(t)
		seedPurgeCrew(t, nk, f, purgeCrew)
		seedPurgeCrew(t, nk, f, otherCrew)
		if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) < 20 {
			t.Fatalf("the seed is too small to prove the purge: %v", left)
		}
		before := crewLeftovers(t, nk, store, f, otherCrew)

		deleteCrewByOwner(t, nk, purgeCrew)

		if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
			t.Fatalf("left after the purge: %v", left)
		}
		if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs == 0 || rec.LastError != "" {
			t.Fatalf("purge record = %+v", rec)
		}
		reconcilePass(t, nk)
		reconcilePass(t, nk)
		if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
			t.Fatalf("back after a reconcile: %v", left)
		}
		if after := crewLeftovers(t, nk, store, f, otherCrew); strings.Join(after, "\n") != strings.Join(before, "\n") {
			t.Fatalf("the other crew changed:\n%v\nwas\n%v", after, before)
		}
		// The chat images are not part of the purge.
		if keys := strings.Join(f.keys("media"), ","); !strings.Contains(keys, purgeCrew+"/u1/img.jpg") {
			t.Fatalf("the chat image is gone: %s", keys)
		}
	})
}

// A document that the purge could not delete is not imported again, and the
// reconcile deletes it when it can.
func TestReconcileDoesNotImportADocumentOfAPurgedCrew(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := usePurgeBuckets(t)
		seedPurgeCrew(t, nk, f, purgeCrew)
		nk.failDelete[CrewClipsCollection] = true
		nk.failDelete[CrewEventsCollection] = true

		deleteCrewByOwner(t, nk, purgeCrew)
		if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs != 0 || !strings.Contains(rec.LastError, CrewClipsCollection) {
			t.Fatalf("purge record after a failed delete = %+v", rec)
		}
		if !nk.has(CrewClipsCollection, purgeCrew) || !nk.has(CrewEventsCollection, purgeCrew) {
			t.Fatal("the test needs the documents that failed to delete")
		}
		if rows := crewRows(t, store, purgeCrew); len(rows) != 0 {
			t.Fatalf("rows after the purge: %v", histIDs(rows))
		}

		// The documents are still there. The reconcile does not import them.
		reconcilePass(t, nk)
		if rows := crewRows(t, store, purgeCrew); len(rows) != 0 {
			t.Fatalf("the reconcile imported a purged crew: %v", histIDs(rows))
		}

		// The storage works again: the reconcile deletes the documents.
		nk.failDelete = map[string]bool{}
		reconcilePass(t, nk)
		if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
			t.Fatalf("left after the reconcile: %v", left)
		}
		// The deletion job ends the purge.
		runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs == 0 || rec.LastError != "" {
			t.Fatalf("purge record after the job = %+v", rec)
		}
	})
}

func purgeRPC(nk runtime.NakamaModule, crewID string) (crewPurgeResult, error) {
	var res crewPurgeResult
	out, err := AdminPurgeCrewRPC(context.Background(), testLogger(), nil, nk, `{"crew_id":"`+crewID+`"}`)
	if err == nil {
		err = json.Unmarshal([]byte(out), &res)
	}
	return res, err
}

func rpcCode(err error) int {
	if e, ok := err.(*runtime.Error); ok {
		return int(e.Code)
	}
	return -1
}

func TestAdminPurgeCrew(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	objects := len(f.keys("clips")) + len(f.keys("snaps")) + len(f.keys("media"))

	// Server-to-server only.
	if _, err := AdminPurgeCrewRPC(ctxWithUser("u1"), testLogger(), nil, nk, `{"crew_id":"`+purgeCrew+`"}`); rpcCode(err) != 7 {
		t.Fatalf("a user call: %v", err)
	}
	// Not a group id: it would be part of an object prefix.
	for _, bad := range []string{"", "crews", "..", purgeCrew + "/x"} {
		if _, err := purgeRPC(nk, bad); rpcCode(err) != 3 {
			t.Fatalf("crew id %q: %v", bad, err)
		}
	}
	// The group still exists.
	if _, err := purgeRPC(nk, purgeCrew); rpcCode(err) != 9 {
		t.Fatalf("a crew that exists: %v", err)
	}
	// A crew id this install holds nothing of: the crew of another install
	// on a shared bucket.
	foreign := "33333333-3333-4333-8333-333333333333"
	f.put("clips", "crews/"+foreign+"/c1.mp4", 10, time.Hour)
	if _, err := purgeRPC(nk, foreign); rpcCode(err) != 5 {
		t.Fatalf("a foreign crew: %v", err)
	}
	if got := len(f.keys("clips")) + len(f.keys("snaps")) + len(f.keys("media")); got != objects+1 || len(f.deletes) != 0 {
		t.Fatalf("a refused purge deleted objects: %d left of %d, deletes %v", got, objects+1, f.deletes)
	}
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) < 20 {
		t.Fatalf("a refused purge deleted data: %v", left)
	}

	// The group is gone and no hook ran: the RPC purges, and can run again.
	if err := nk.GroupDelete(context.Background(), purgeCrew); err != nil {
		t.Fatal(err)
	}
	res, err := purgeRPC(nk, purgeCrew)
	if err != nil || !res.Done || res.MediaDeleted != 7 || !res.StoreDeleted || res.DocsDeleted < 13 {
		t.Fatalf("purge = %+v err=%v", res, err)
	}
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
		t.Fatalf("left after the purge: %v", left)
	}
	res, err = purgeRPC(nk, purgeCrew)
	if err != nil || !res.Done || res.MediaDeleted != 0 || res.DocsDeleted != 0 {
		t.Fatalf("second purge = %+v err=%v", res, err)
	}
	if keys := f.keys("clips"); len(keys) != 1 || !strings.Contains(keys[0], foreign) {
		t.Fatalf("clips bucket = %v, want only the object of the foreign crew", keys)
	}
}

// A purge that is cut at its time budget says so, and the next run ends it.
func TestPurgeCrewContinuesAfterItsBudget(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	if err := nk.GroupDelete(context.Background(), purgeCrew); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCrewPurgeRecord(context.Background(), nk, purgeCrew); err != nil {
		t.Fatal(err)
	}
	res := purgeCrewData(context.Background(), nk, testLogger(), purgeCrew, time.Now().Add(-time.Second))
	if res.Done || !res.Incomplete || res.MediaDeleted != 0 {
		t.Fatalf("a purge past its deadline = %+v", res)
	}
	if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs != 0 || rec.LastError == "" {
		t.Fatalf("purge record = %+v", rec)
	}
	runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
		t.Fatalf("left after the second run: %v", left)
	}
	if rec := purgeRecordOf(t, nk, purgeCrew); rec.DoneMs == 0 {
		t.Fatalf("purge record = %+v", rec)
	}
}

// A writer that is late (a presigned upload, a clip of a member whose client
// did not see the delete) writes after the purge. The second run of the purge
// removes it, one hour after the first.
func TestPurgeCrewRunsAgainForALateWriter(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	deleteCrewByOwner(t, nk, purgeCrew)

	if _, err := AppendClip(context.Background(), nk, purgeCrew, histClip("late", "u1", time.Now())); err != nil {
		t.Fatal(err)
	}
	f.put("clips", "crews/"+purgeCrew+"/late.mp4", 10, 0)
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 4 {
		t.Fatalf("the late writer left %v, want a document, a row, its user index and an object", left)
	}

	// Before the hour the job does nothing.
	runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 4 {
		t.Fatalf("the job ran before the hour: %v", left)
	}
	prev := crewPurgeNow
	crewPurgeNow = func() time.Time { return time.Now().Add(crewPurgeSettle + time.Minute) }
	t.Cleanup(func() { crewPurgeNow = prev })
	runPendingCrewPurges(context.Background(), nk, testLogger(), crewPurgeNow().Add(time.Minute))
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
		t.Fatalf("left after the second run: %v", left)
	}
	if rec := purgeRecordOf(t, nk, purgeCrew); rec.SettledMs == 0 {
		t.Fatalf("purge record = %+v", rec)
	}
	// A settled purge does not run again.
	deletes := len(f.deletes)
	f.put("clips", "crews/"+purgeCrew+"/later.mp4", 10, 0)
	runPendingCrewPurges(context.Background(), nk, testLogger(), crewPurgeNow().Add(time.Minute))
	if len(f.deletes) != deletes {
		t.Fatal("a settled purge ran again")
	}
}

// A crew that is deleted through the group API of Nakama is purged too.
func TestDeleteGroupHookPurgesTheCrew(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := usePurgeBuckets(t)
	seedPurgeCrew(t, nk, f, purgeCrew)
	if err := nk.GroupDelete(context.Background(), purgeCrew); err != nil {
		t.Fatal(err)
	}
	if err := AfterDeleteCrew(ctxWithUser("owner"), testLogger(), nil, nk, &api.DeleteGroupRequest{GroupId: purgeCrew}); err != nil {
		t.Fatal(err)
	}
	if left := crewLeftovers(t, nk, store, f, purgeCrew); len(left) != 0 {
		t.Fatalf("left after the hook: %v", left)
	}
}

// The purge deletes only under a prefix that ends with the crew id.
func TestDeleteObjectsUnderRefusesAWrongPrefix(t *testing.T) {
	f := usePurgeBuckets(t)
	f.put("clips", "crews/"+purgeCrew+"/c1.mp4", 10, time.Hour)
	client := newS3Client(f.server.URL, "test", "test")
	for _, prefix := range []string{"crews/", "", "crews/" + purgeCrew, "/"} {
		n, _, err := deleteObjectsUnder(context.Background(), client, "clips", prefix, purgeCrew, time.Now().Add(time.Minute))
		if err == nil || n != 0 {
			t.Fatalf("prefix %q: deleted=%d err=%v", prefix, n, err)
		}
	}
	if _, _, err := deleteObjectsUnder(context.Background(), client, "clips", "crews//", "", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("an empty owner passed")
	}
	if len(f.deletes) != 0 || len(f.keys("clips")) != 1 {
		t.Fatalf("deletes = %v", f.deletes)
	}
}

// A purge record of a crew that is a group must delete nothing: not through
// the purge, not through the job, not through the reconcile. The record is
// wrong, so it is deleted and the crew is reconciled as any other.
func TestPurgeRecordOfALiveCrewDeletesNothing(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := usePurgeBuckets(t)
		seedPurgeCrew(t, nk, f, purgeCrew)
		reconcilePass(t, nk)
		before := crewLeftovers(t, nk, store, f, purgeCrew)
		if len(before) < 20 {
			t.Fatalf("the seed is too small: %v", before)
		}
		same := func(when string) {
			t.Helper()
			if after := crewLeftovers(t, nk, store, f, purgeCrew); strings.Join(after, "\n") != strings.Join(before, "\n") || len(f.deletes) != 0 {
				t.Fatalf("%s: a live crew lost data:\n%v\nwas\n%v\ndeletes %v", when, after, before, f.deletes)
			}
		}
		record := func() {
			t.Helper()
			if err := writeCrewPurgeRecord(context.Background(), nk, crewPurgeRecord{CrewID: purgeCrew, StartedMs: 1}); err != nil {
				t.Fatal(err)
			}
		}

		// The reconcile, with a record that something wrote for a live crew.
		record()
		reconcilePass(t, nk)
		reconcilePass(t, nk)
		same("after the reconcile")
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, purgeCrew); rec != nil {
			t.Fatalf("the reconcile kept the record of a live crew: %+v", rec)
		}

		// The group read fails: nothing is deleted, and the record stays for
		// a pass that can read the group.
		record()
		nk.groupErr = errors.New("group read failed")
		reconcilePass(t, nk)
		runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		same("with a group read that fails")
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, purgeCrew); rec == nil {
			t.Fatal("a group read error deleted the record")
		}
		nk.groupErr = nil

		// The job.
		runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		same("after the job")
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, purgeCrew); rec != nil {
			t.Fatalf("the job kept the record of a live crew: %+v", rec)
		}

		// The hook and the start of a purge write no record for a live crew,
		// and none when the group cannot be read.
		if err := AfterDeleteCrew(context.Background(), testLogger(), nil, nk, &api.DeleteGroupRequest{GroupId: purgeCrew}); err != nil {
			t.Fatal(err)
		}
		nk.groupErr = errors.New("group read failed")
		startCrewPurge(context.Background(), testLogger(), nk, purgeCrew)
		nk.groupErr = nil
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, purgeCrew); rec != nil {
			t.Fatalf("a purge of a live crew wrote a record: %+v", rec)
		}
		reconcilePass(t, nk)
		same("after the hook")
	})
}
