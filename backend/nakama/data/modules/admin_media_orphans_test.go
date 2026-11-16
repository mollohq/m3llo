package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// fakeS3 is an S3 endpoint on the loopback interface: ListObjectsV2 with a
// prefix, start-after, max-keys and a continuation token, and DeleteObject.
// The real S3 client of the module talks to it.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]s3ObjectEntry // bucket/key -> object
	deletes []string
	lists   int
	server  *httptest.Server
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: map[string]s3ObjectEntry{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeS3) put(bucket, key string, size int64, age time.Duration) {
	f.mu.Lock()
	f.objects[bucket+"/"+key] = s3ObjectEntry{Key: key, Size: size, LastModified: time.Now().Add(-age)}
	f.mu.Unlock()
}

func (f *fakeS3) keys(bucket string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k, o := range f.objects {
		if strings.HasPrefix(k, bucket+"/") {
			out = append(out, o.Key)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.lists++
		q := r.URL.Query()
		after := q.Get("start-after")
		if tok := q.Get("continuation-token"); tok != "" {
			after = tok
		}
		max, _ := strconv.Atoi(q.Get("max-keys"))
		if max <= 0 {
			max = 1000
		}
		var keys []string
		for k, o := range f.objects {
			if strings.HasPrefix(k, bucket+"/") && strings.HasPrefix(o.Key, q.Get("prefix")) && o.Key > after {
				keys = append(keys, o.Key)
			}
		}
		sort.Strings(keys)
		type content struct {
			Key          string
			LastModified string
			Size         int64
		}
		result := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			IsTruncated           bool
			NextContinuationToken string `xml:",omitempty"`
			Contents              []content
		}{}
		if len(keys) > max {
			keys = keys[:max]
			result.IsTruncated = true
			result.NextContinuationToken = keys[len(keys)-1]
		}
		for _, k := range keys {
			o := f.objects[bucket+"/"+k]
			result.Contents = append(result.Contents, content{Key: k, LastModified: o.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"), Size: o.Size})
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(result)
	case r.Method == http.MethodDelete && key != "":
		delete(f.objects, bucket+"/"+key)
		f.deletes = append(f.deletes, bucket+"/"+key)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "not implemented by the fake", http.StatusNotImplemented)
	}
}

// useFakeBuckets points admin_media_orphans at the fake: a clips bucket and a
// snapshots bucket.
func useFakeBuckets(t *testing.T, f *fakeS3) {
	t.Helper()
	prev := mediaOrphanBuckets
	client := newS3Client(f.server.URL, "test", "test")
	mediaOrphanBuckets = func() (mediaBucket, mediaBucket) {
		return mediaBucket{client: client, bucket: "clips", prefix: mediaClipPrefix, typ: ledgerTypeClip, ref: clipObjectRef},
			mediaBucket{client: client, bucket: "snaps", prefix: mediaSnapshotPrefix, typ: ledgerTypeStreamSession, ref: snapshotObjectRef, groupPrefix: snapshotSessionPrefix}
	}
	t.Cleanup(func() { mediaOrphanBuckets = prev })
}

func mediaOrphans(t *testing.T, nk runtime.NakamaModule, payload string) AdminMediaOrphansResponse {
	t.Helper()
	out, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, payload)
	if err != nil {
		t.Fatalf("admin_media_orphans %s: %v", payload, err)
	}
	var resp AdminMediaOrphansResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func sampleKeys(k AdminMediaOrphanKind) string {
	keys := make([]string, 0, len(k.Sample))
	for _, o := range k.Sample {
		keys = append(keys, o.Key)
	}
	return strings.Join(keys, " ")
}

func TestMediaObjectRefs(t *testing.T) {
	for _, tc := range []struct {
		key, crew, ref string
		ok             bool
	}{
		{"crews/c1/clip-1.mp4", "c1", "clip-1", true},
		{"crews/c1/clip-1.jpg", "c1", "clip-1", true},
		{"crews/c1/a/b.mp4", "c1", "a/b", true},
		{"crews/c1/clip.mp4.part", "", "", false},
		{"crews/c1/.mp4", "", "", false},
		{"crews/c1", "", "", false},
		{"crews//x.mp4", "", "", false},
		{"other/c1/x.mp4", "", "", false},
	} {
		if crew, ref, ok := clipObjectRef(tc.key); crew != tc.crew || ref != tc.ref || ok != tc.ok {
			t.Errorf("clipObjectRef(%q) = %q %q %t", tc.key, crew, ref, ok)
		}
	}
	for _, tc := range []struct {
		key, crew, ref string
		ok             bool
	}{
		{"snapshots/c1/sess/1700000000000.jpg", "c1", "sess", true},
		{"snapshots/c1/sess/x.png", "", "", false},
		{"snapshots/c1/sess/.jpg", "", "", false},
		{"snapshots/c1/sess/deep/1.jpg", "", "", false},
		{"snapshots/c1/1.jpg", "", "", false},
		{"snapshots//sess/1.jpg", "", "", false},
		{"crews/c1/sess/1.jpg", "", "", false},
	} {
		if crew, ref, ok := snapshotObjectRef(tc.key); crew != tc.crew || ref != tc.ref || ok != tc.ok {
			t.Errorf("snapshotObjectRef(%q) = %q %q %t", tc.key, crew, ref, ok)
		}
	}
}

// seedMediaCrew gives crew c1 clips and stream sessions in each place that
// can hold one, and the bucket their objects plus the orphans. c1 is a group
// of this install. c2 is not.
func seedMediaCrew(t *testing.T, nk *fakeGuestNk, store ledgerStore, f *fakeS3) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	const old, fresh = 50 * time.Hour, time.Hour
	nk.groups["c1"] = &api.Group{Id: "c1", Name: "Crew"}

	// Clips: one in the document, one in the store only (the document
	// dropped it), one in an old ledger.
	nk.put(CrewClipsCollection, "c1", SystemUserID, CrewClipsDoc{CrewID: "c1", Clips: []StoredClip{histClip("in_doc", "u1", now)}})
	if store != nil {
		if _, err := store.Put(ctx, "c1", []ledgerRow{clipRow(histClip("row_only", "u1", now.Add(-400*24*time.Hour)))}); err != nil {
			t.Fatal(err)
		}
	}
	// Stream sessions: the durable copy, the ledger, the store only, and a
	// stream that is live.
	putStreamsDoc(nk, "c1", histSession("copy", now.Add(-30*24*time.Hour)))
	putDoc(nk, "c1", now, histStreamEvent(histSession("ledger", now.Add(-time.Hour))),
		CrewEvent{ID: "lc", Type: "clip", Timestamp: now.UnixMilli(), Data: ClipData{ClipID: "in_ledger"}})
	if store != nil {
		if _, err := store.Put(ctx, "c1", []ledgerRow{streamSessionRow(histSession("row_only", now.Add(-400*24*time.Hour)))}); err != nil {
			t.Fatal(err)
		}
	}
	nk.put(StreamMetaCollection, "c1", SystemUserID, StreamMeta{StreamID: "live", CrewID: "c1"})

	for _, k := range []string{"in_doc.mp4", "in_doc.jpg", "row_only.mp4", "in_ledger.mp4"} {
		f.put("clips", "crews/c1/"+k, 5000, old)
	}
	f.put("clips", "crews/c1/gone.mp4", 1000, old)
	f.put("clips", "crews/c1/gone.jpg", 200, old)
	f.put("clips", "crews/c2/gone_too.mp4", 30, old) // a crew id that is no group here
	f.put("clips", "crews/c1/uploading.mp4", 70, fresh)
	f.put("clips", "crews/c1/notes.txt", 9, old)
	f.put("clips", "crews/readme", 9, old)
	f.put("clips", "other/c1/gone.mp4", 9, old) // not under the prefix

	for _, sess := range []string{"sess_copy", "sess_ledger", "sess_row_only", "live"} {
		f.put("snaps", "snapshots/c1/"+sess+"/1.jpg", 4000, old)
	}
	for i := 1; i <= 3; i++ {
		f.put("snaps", fmt.Sprintf("snapshots/c1/sess_gone/%d.jpg", i), 100, old)
	}
	f.put("snaps", "snapshots/c1/sess_new/1.jpg", 100, fresh)
	f.put("snaps", "snapshots/c1/sess_gone/frame.png", 100, old)
	f.put("snaps", "snapshots/c2/sess_far/1.jpg", 100, old)
	// A stream of 50 hours that stopped a minute ago. stop_stream deleted
	// its stream record and did not write the stream session yet: nothing
	// holds the session id.
	f.put("snaps", "snapshots/c1/sess_stopping/1.jpg", 100, old)
	f.put("snaps", "snapshots/c1/sess_stopping/2.jpg", 100, 30*time.Hour)
	f.put("snaps", "snapshots/c1/sess_stopping/3.jpg", 100, time.Minute)
}

const (
	mediaClipOrphans     = "crews/c1/gone.jpg crews/c1/gone.mp4"
	mediaSnapshotOrphans = "snapshots/c1/sess_gone/1.jpg snapshots/c1/sess_gone/2.jpg snapshots/c1/sess_gone/3.jpg"
)

func orphanDelete(t *testing.T, nk runtime.NakamaModule, clipKeys, snapshotKeys []string) AdminMediaOrphansResponse {
	t.Helper()
	payload, err := json.Marshal(AdminMediaOrphansRequest{Delete: true, ClipKeys: clipKeys, SnapshotKeys: snapshotKeys})
	if err != nil {
		t.Fatal(err)
	}
	resp := mediaOrphans(t, nk, string(payload))
	if resp.DryRun || resp.DeletedClips == nil || resp.DeletedSnapshots == nil {
		t.Fatalf("delete response = %+v", resp)
	}
	return resp
}

// refusals is "key=reason" for each refused key, sorted.
func refusals(d *AdminMediaOrphanDelete) string {
	out := make([]string, 0, len(d.Refused))
	for _, r := range d.Refused {
		out = append(out, r.Key+"="+r.Reason)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func sampleKeyList(k AdminMediaOrphanKind) []string {
	return strings.Fields(sampleKeys(k))
}

func TestAdminMediaOrphansScanDeletesNothing(t *testing.T) {
	store := useMemLedger(t)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)
	clipsBefore, snapsBefore := f.keys("clips"), f.keys("snaps")

	resp := mediaOrphans(t, nk, "")
	if !resp.DryRun || resp.MinAgeHours != 24 || !resp.Store || resp.DeletedClips != nil || resp.DeletedSnapshots != nil {
		t.Fatalf("response = %+v", resp)
	}
	c := resp.Clips
	if !c.Configured || c.Scanned != 10 || c.Orphans != 2 || c.Bytes != 1200 || c.TooNew != 1 || c.Unknown != 2 || c.UnknownCrew != 1 || c.ReadFailed != 0 || c.Incomplete || c.NextAfter != "" {
		t.Fatalf("clips = %+v", c)
	}
	if got := sampleKeys(c); got != mediaClipOrphans {
		t.Fatalf("clip orphans = %s", got)
	}
	s := resp.Snapshots
	// too_new: the new session, and the three snapshots of the session that
	// stopped a minute ago.
	if s.Scanned != 13 || s.Orphans != 3 || s.Bytes != 300 || s.TooNew != 4 || s.Unknown != 1 || s.UnknownCrew != 1 || s.Incomplete {
		t.Fatalf("snapshots = %+v", s)
	}
	if got := sampleKeys(s); got != mediaSnapshotOrphans {
		t.Fatalf("snapshot orphans = %s", got)
	}
	if c.Sample[1].Bytes != 1000 || time.Since(time.UnixMilli(c.Sample[1].LastModifiedMs)) < 49*time.Hour {
		t.Fatalf("sample = %+v", c.Sample[1])
	}
	// Nothing was deleted, with the env switch on.
	if got := f.keys("clips"); strings.Join(got, " ") != strings.Join(clipsBefore, " ") {
		t.Fatalf("clips bucket changed: %v", got)
	}
	if got := f.keys("snaps"); strings.Join(got, " ") != strings.Join(snapsBefore, " ") || len(f.deletes) != 0 {
		t.Fatalf("snapshots bucket changed: %v, deletes %v", got, f.deletes)
	}

	// A list in parts gives the same totals.
	var orphans, scanned, calls int
	after := ""
	for {
		part := mediaOrphans(t, nk, fmt.Sprintf(`{"max_objects":3,"clips_after":%q}`, after)).Clips
		calls++
		orphans += part.Orphans
		scanned += part.Scanned
		if part.Scanned > 3 || calls > 20 {
			t.Fatalf("call %d scanned %d objects after %q", calls, part.Scanned, after)
		}
		if !part.Incomplete {
			break
		}
		after = part.NextAfter
	}
	if orphans != 2 || scanned != 10 || calls != 4 {
		t.Fatalf("in parts: %d orphans of %d objects in %d calls", orphans, scanned, calls)
	}

	// Only a server caller.
	if _, err := AdminMediaOrphansRPC(ctxWithUser("u1"), testLogger(), nil, nk, ""); errCode(err) != 7 {
		t.Fatalf("a client session: %v, want code 7", err)
	}
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"max_objects":-1}`); errCode(err) != 3 {
		t.Fatalf("a bad max_objects: %v, want code 3", err)
	}
	// Keys with no delete flag are a mistake, not a scan.
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"clip_keys":["crews/c1/gone.mp4"]}`); errCode(err) != 3 || len(f.deletes) != 0 {
		t.Fatalf("keys without the delete flag: %v, deletes %v", err, f.deletes)
	}
}

// stop_stream and the socket drop delete the stream record before they write
// the stream session. In that gap nothing holds the session id. The scan and
// the delete skip the whole session while one of its snapshots is younger
// than 24 hours.
func TestAdminMediaOrphansSkipsASessionWithAFreshSnapshot(t *testing.T) {
	store := useMemLedger(t)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	nk.groups["c1"] = &api.Group{Id: "c1"}
	_ = store
	// A stream of three days. The stream record is gone, the event is not
	// written yet.
	f.put("snaps", "snapshots/c1/gap/1.jpg", 100, 72*time.Hour)
	f.put("snaps", "snapshots/c1/gap/2.jpg", 100, 48*time.Hour)
	f.put("snaps", "snapshots/c1/gap/3.jpg", 100, 10*time.Second)
	// A session that ended two days ago and has no row is an orphan.
	f.put("snaps", "snapshots/c1/lost/1.jpg", 100, 72*time.Hour)
	f.put("snaps", "snapshots/c1/lost/2.jpg", 100, 48*time.Hour)

	s := mediaOrphans(t, nk, "").Snapshots
	if s.Scanned != 5 || s.Orphans != 2 || s.TooNew != 3 || sampleKeys(s) != "snapshots/c1/lost/1.jpg snapshots/c1/lost/2.jpg" {
		t.Fatalf("scan = %+v", s)
	}
	// A delete of the old snapshots of the session in the gap is refused.
	resp := orphanDelete(t, nk, nil, []string{"snapshots/c1/gap/1.jpg", "snapshots/c1/gap/2.jpg", "snapshots/c1/lost/1.jpg"})
	if got := refusals(resp.DeletedSnapshots); got != "snapshots/c1/gap/1.jpg=session_active snapshots/c1/gap/2.jpg=session_active" {
		t.Fatalf("refused = %s", got)
	}
	if got := strings.Join(resp.DeletedSnapshots.Deleted, " "); got != "snapshots/c1/lost/1.jpg" || strings.Join(f.deletes, " ") != "snaps/snapshots/c1/lost/1.jpg" {
		t.Fatalf("deleted = %s, bucket deletes %v", got, f.deletes)
	}
}

// Without the durable store only the documents say what a crew holds. The
// clip and the stream session that only the store holds then count as
// orphans in the report, and a delete is refused.
func TestAdminMediaOrphansWithoutTheStore(t *testing.T) {
	useLedger(t, nil)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, nil, f)

	resp := mediaOrphans(t, nk, "")
	if resp.Store || resp.Clips.Orphans != 3 || resp.Snapshots.Orphans != 4 {
		t.Fatalf("response = %+v", resp)
	}
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"delete":true,"clip_keys":["crews/c1/gone.mp4"]}`); errCode(err) != 9 {
		t.Fatalf("delete without the store: %v, want code 9", err)
	}
	if len(f.deletes) != 0 {
		t.Fatalf("deleted %v", f.deletes)
	}
}

// A delete needs the env switch and a list of keys. It deletes the keys of
// the list that are orphans, and no other object.
func TestAdminMediaOrphansDeleteTakesTheKeysOfAScan(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)

	t.Setenv(mediaOrphanDeleteEnv, "")
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"delete":true,"clip_keys":["crews/c1/gone.mp4"]}`); errCode(err) != 9 {
		t.Fatalf("delete without the env switch: %v, want code 9", err)
	}
	if len(f.deletes) != 0 || f.lists != 0 {
		t.Fatalf("without the env switch: deletes %v, %d lists", f.deletes, f.lists)
	}
	t.Setenv(mediaOrphanDeleteEnv, "1")
	// The delete flag with no keys deletes nothing: there is no "delete what
	// you find".
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"delete":true}`); errCode(err) != 3 || len(f.deletes) != 0 {
		t.Fatalf("delete with no keys: %v, deletes %v", err, f.deletes)
	}
	// The hard cap: one key too many deletes nothing.
	tooMany := make([]string, 0, mediaOrphanDeleteMax+1)
	for i := 0; i < mediaOrphanDeleteMax; i++ {
		tooMany = append(tooMany, fmt.Sprintf("crews/c1/none_%d.mp4", i))
	}
	payload, _ := json.Marshal(AdminMediaOrphansRequest{Delete: true, ClipKeys: tooMany, SnapshotKeys: []string{"snapshots/c1/sess_gone/1.jpg"}})
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, string(payload)); errCode(err) != 3 || len(f.deletes) != 0 {
		t.Fatalf("delete of %d keys: %v, deletes %v", mediaOrphanDeleteMax+1, err, f.deletes)
	}

	// One orphan clip of the two: the other one stays.
	scan := mediaOrphans(t, nk, "")
	resp := orphanDelete(t, nk, []string{"crews/c1/gone.mp4"}, sampleKeyList(scan.Snapshots))
	if got := strings.Join(resp.DeletedClips.Deleted, " "); got != "crews/c1/gone.mp4" || len(resp.DeletedClips.Refused) != 0 {
		t.Fatalf("deleted clips = %+v", resp.DeletedClips)
	}
	if got := strings.Join(resp.DeletedSnapshots.Deleted, " "); got != mediaSnapshotOrphans || len(resp.DeletedSnapshots.Refused) != 0 {
		t.Fatalf("deleted snapshots = %+v", resp.DeletedSnapshots)
	}
	sort.Strings(f.deletes)
	if got := strings.Join(f.deletes, " "); got != "clips/crews/c1/gone.mp4 snaps/"+strings.ReplaceAll(mediaSnapshotOrphans, " ", " snaps/") {
		t.Fatalf("bucket deletes = %s", got)
	}
	if got := strings.Join(f.keys("clips"), " "); got != "crews/c1/gone.jpg crews/c1/in_doc.jpg crews/c1/in_doc.mp4 crews/c1/in_ledger.mp4 crews/c1/notes.txt crews/c1/row_only.mp4 crews/c1/uploading.mp4 crews/c2/gone_too.mp4 crews/readme other/c1/gone.mp4" {
		t.Fatalf("clips bucket after the delete: %s", got)
	}

	// Every key that is not an orphan is refused with its reason, and stays.
	f.deletes = nil
	resp = orphanDelete(t, nk,
		[]string{"crews/c1/in_doc.mp4", "crews/c1/row_only.mp4", "crews/c2/gone_too.mp4", "crews/c1/uploading.mp4", "crews/c1/notes.txt",
			"crews/c1/gone.mp4", "other/c1/gone.mp4", "snapshots/c1/sess_gone/1.jpg", "crews/c1/gone.jpg", "crews/c1/gone.jpg"},
		[]string{"snapshots/c1/live/1.jpg", "snapshots/c1/sess_row_only/1.jpg", "snapshots/c2/sess_far/1.jpg", "snapshots/c1/sess_new/1.jpg",
			"snapshots/c1/sess_stopping/1.jpg", "snapshots/c1/sess_gone/frame.png", "snapshots/c1/sess_gone/1.jpg", "crews/c1/gone.jpg"})
	wantClips := "crews/c1/gone.mp4=not_found crews/c1/in_doc.mp4=held crews/c1/notes.txt=unknown_key crews/c1/row_only.mp4=held " +
		"crews/c1/uploading.mp4=too_new crews/c2/gone_too.mp4=unknown_crew other/c1/gone.mp4=unknown_key snapshots/c1/sess_gone/1.jpg=unknown_key"
	if got := refusals(resp.DeletedClips); got != wantClips {
		t.Fatalf("refused clips =\n%s, want\n%s", got, wantClips)
	}
	wantSnaps := "crews/c1/gone.jpg=unknown_key snapshots/c1/live/1.jpg=held snapshots/c1/sess_gone/1.jpg=not_found snapshots/c1/sess_gone/frame.png=unknown_key " +
		"snapshots/c1/sess_new/1.jpg=too_new snapshots/c1/sess_row_only/1.jpg=held snapshots/c1/sess_stopping/1.jpg=session_active snapshots/c2/sess_far/1.jpg=unknown_crew"
	if got := refusals(resp.DeletedSnapshots); got != wantSnaps {
		t.Fatalf("refused snapshots =\n%s, want\n%s", got, wantSnaps)
	}
	// The one orphan of the lists went, once.
	if got := strings.Join(resp.DeletedClips.Deleted, " "); got != "crews/c1/gone.jpg" || len(resp.DeletedSnapshots.Deleted) != 0 || strings.Join(f.deletes, " ") != "clips/crews/c1/gone.jpg" {
		t.Fatalf("deleted = %+v %+v, bucket deletes %v", resp.DeletedClips.Deleted, resp.DeletedSnapshots.Deleted, f.deletes)
	}
	// A scan after it finds no orphan.
	if again := mediaOrphans(t, nk, ""); again.Clips.Orphans != 0 || again.Snapshots.Orphans != 0 {
		t.Fatalf("scan after the delete = %+v", again)
	}
}

// A key is an orphan at the scan and not any more at the delete: the crew
// took the clip_id, the session got its row. The delete reads again and
// refuses.
func TestAdminMediaOrphansDeleteChecksEachKeyAgain(t *testing.T) {
	store := useMemLedger(t)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	ctx := context.Background()
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)

	scan := mediaOrphans(t, nk, "")
	if sampleKeys(scan.Clips) != mediaClipOrphans || sampleKeys(scan.Snapshots) != mediaSnapshotOrphans {
		t.Fatalf("scan = %+v", scan)
	}
	// After the scan: a clip with the clip_id is uploaded, and the stream
	// session of the session is written to the store.
	if _, err := AppendClip(ctx, nk, "c1", histClip("gone", "u1", time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "c1", []ledgerRow{streamSessionRow(histSession("gone", time.Now()))}); err != nil {
		t.Fatal(err)
	}

	resp := orphanDelete(t, nk, sampleKeyList(scan.Clips), sampleKeyList(scan.Snapshots))
	if got := refusals(resp.DeletedClips); got != "crews/c1/gone.jpg=held crews/c1/gone.mp4=held" {
		t.Fatalf("refused clips = %s", got)
	}
	if got := refusals(resp.DeletedSnapshots); got != "snapshots/c1/sess_gone/1.jpg=held snapshots/c1/sess_gone/2.jpg=held snapshots/c1/sess_gone/3.jpg=held" {
		t.Fatalf("refused snapshots = %s", got)
	}
	if len(resp.DeletedClips.Deleted) != 0 || len(resp.DeletedSnapshots.Deleted) != 0 || len(f.deletes) != 0 {
		t.Fatalf("deleted = %+v %+v, bucket deletes %v", resp.DeletedClips.Deleted, resp.DeletedSnapshots.Deleted, f.deletes)
	}
}

// crewTakesClipNk gives the crew a clip of a clip_id when the group of the
// crew is read for the second time: between the check of one key and the
// check of the next.
type crewTakesClipNk struct {
	*fakeGuestNk
	reads int
}

func (c *crewTakesClipNk) GroupsGetId(ctx context.Context, ids []string) ([]*api.Group, error) {
	if c.reads++; c.reads == 2 {
		c.fakeGuestNk.put(CrewClipsCollection, "c1", SystemUserID, CrewClipsDoc{CrewID: "c1", Clips: []StoredClip{histClip("b", "u1", time.Now())}})
	}
	return c.fakeGuestNk.GroupsGetId(ctx, ids)
}

// The delete does not keep what it read for one key. A clip that the crew
// takes while a delete call runs is seen at the next key.
func TestAdminMediaOrphansDeleteReadsTheCrewForEachKey(t *testing.T) {
	useMemLedger(t)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	base := newFakeGuestNk()
	base.groups["c1"] = &api.Group{Id: "c1"}
	nk := &crewTakesClipNk{fakeGuestNk: base}
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	f.put("clips", "crews/c1/a.mp4", 10, 50*time.Hour)
	f.put("clips", "crews/c1/b.mp4", 10, 50*time.Hour)

	resp := orphanDelete(t, nk, []string{"crews/c1/a.mp4", "crews/c1/b.mp4"}, nil)
	if got := strings.Join(resp.DeletedClips.Deleted, " "); got != "crews/c1/a.mp4" || refusals(resp.DeletedClips) != "crews/c1/b.mp4=held" {
		t.Fatalf("deleted = %s, refused = %s", got, refusals(resp.DeletedClips))
	}
	if nk.reads != 2 || strings.Join(f.keys("clips"), " ") != "crews/c1/b.mp4" {
		t.Fatalf("%d group reads, bucket %v", nk.reads, f.keys("clips"))
	}
}

// A crew whose documents cannot be read has no orphans: its objects are
// kept and counted. A delete refuses them.
func TestAdminMediaOrphansKeepsWhatItCannotCheck(t *testing.T) {
	store := useMemLedger(t)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)

	broken := failHistReadNk{fakeGuestNk: nk, collection: CrewClipsCollection}
	resp := mediaOrphans(t, broken, "")
	if resp.Clips.Orphans != 0 || resp.Clips.ReadFailed != 7 || resp.Clips.UnknownCrew != 1 || resp.Snapshots.Orphans != 0 || resp.Snapshots.ReadFailed != 11 {
		t.Fatalf("response = %+v", resp)
	}
	del := orphanDelete(t, broken, strings.Fields(mediaClipOrphans), strings.Fields(mediaSnapshotOrphans))
	if len(del.DeletedClips.Deleted) != 0 || len(del.DeletedSnapshots.Deleted) != 0 || len(f.deletes) != 0 ||
		refusals(del.DeletedClips) != "crews/c1/gone.jpg=read_failed crews/c1/gone.mp4=read_failed" {
		t.Fatalf("delete with a document that cannot be read = %+v %+v, deletes %v", del.DeletedClips, del.DeletedSnapshots, f.deletes)
	}
	// The store fails: the same.
	useLedger(t, failingClipReads{store})
	resp = mediaOrphans(t, nk, "")
	if resp.Clips.Orphans != 0 || resp.Clips.ReadFailed != 4 || resp.Snapshots.ReadFailed != 8 {
		t.Fatalf("response with a failing store = %+v", resp)
	}
	del = orphanDelete(t, nk, strings.Fields(mediaClipOrphans), strings.Fields(mediaSnapshotOrphans))
	if len(del.DeletedClips.Deleted) != 0 || len(del.DeletedSnapshots.Deleted) != 0 || len(f.deletes) != 0 || len(del.DeletedSnapshots.Refused) != 3 {
		t.Fatalf("delete with a failing store = %+v %+v, deletes %v", del.DeletedClips, del.DeletedSnapshots, f.deletes)
	}
}

// The time budget ends in the middle of a scan. The objects that were not
// judged are not counted, and next_after is the last key that was judged:
// the calls that follow judge every other key once.
func TestAdminMediaOrphansNextAfterStopsAtTheFirstKeyNotJudged(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)
	whole := mediaOrphans(t, nk, "")

	// The clock of the budget: each call of a scan gets four looks at the
	// clock inside its budget. The next one is after it.
	looks := 0
	start := time.Now()
	prev := mediaOrphanClock
	mediaOrphanClock = func() time.Time {
		if looks++; looks > 5 {
			return start.Add(mediaOrphanBudget + time.Second)
		}
		return start
	}
	t.Cleanup(func() { mediaOrphanClock = prev })

	var clips, snaps AdminMediaOrphanKind
	judged := map[string]bool{}
	clipsAfter, snapsAfter := "", ""
	clipsDone, snapsDone := false, false
	for call := 1; !clipsDone || !snapsDone; call++ {
		if call > 30 {
			t.Fatal("the scan does not end")
		}
		looks = 0
		resp := mediaOrphans(t, nk, fmt.Sprintf(`{"clips_after":%q,"snapshots_after":%q}`, clipsAfter, snapsAfter))
		// The first look is the start of the call, so a call judges at most
		// three objects of the first prefix it has time for.
		if resp.Clips.Scanned+resp.Snapshots.Scanned > 3 || resp.Clips.Scanned+resp.Snapshots.Scanned == 0 {
			t.Fatalf("call %d judged %d clips and %d snapshots", call, resp.Clips.Scanned, resp.Snapshots.Scanned)
		}
		for _, part := range []struct {
			got   AdminMediaOrphanKind
			sum   *AdminMediaOrphanKind
			after *string
			done  *bool
		}{{resp.Clips, &clips, &clipsAfter, &clipsDone}, {resp.Snapshots, &snaps, &snapsAfter, &snapsDone}} {
			if *part.done {
				continue
			}
			part.sum.Scanned += part.got.Scanned
			part.sum.Orphans += part.got.Orphans
			part.sum.TooNew += part.got.TooNew
			part.sum.Unknown += part.got.Unknown
			part.sum.UnknownCrew += part.got.UnknownCrew
			for _, o := range part.got.Sample {
				if judged[o.Key] {
					t.Fatalf("call %d: %s is an orphan of two calls", call, o.Key)
				}
				judged[o.Key] = true
			}
			if !part.got.Incomplete {
				*part.done = true
				continue
			}
			// No key that was not judged is behind next_after.
			if part.got.Scanned == 0 && part.got.NextAfter != *part.after {
				t.Fatalf("call %d judged nothing and moved next_after from %q to %q", call, *part.after, part.got.NextAfter)
			}
			*part.after = part.got.NextAfter
		}
	}
	for name, pair := range map[string][2]AdminMediaOrphanKind{"clips": {clips, whole.Clips}, "snapshots": {snaps, whole.Snapshots}} {
		got, want := pair[0], pair[1]
		if got.Scanned != want.Scanned || got.Orphans != want.Orphans || got.TooNew != want.TooNew || got.Unknown != want.Unknown || got.UnknownCrew != want.UnknownCrew {
			t.Fatalf("%s in parts = %+v, in one call %+v", name, got, want)
		}
	}
}

// No bucket: the report says so and lists nothing.
func TestAdminMediaOrphansWithoutABucket(t *testing.T) {
	requireNoS3(t)
	nk := newFakeGuestNk()
	resp := mediaOrphans(t, nk, "")
	if resp.Clips.Configured || resp.Snapshots.Configured || resp.Clips.Scanned != 0 || resp.Clips.Sample == nil {
		t.Fatalf("response = %+v", resp)
	}
}
