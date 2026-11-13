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
			mediaBucket{client: client, bucket: "snaps", prefix: mediaSnapshotPrefix, typ: ledgerTypeStreamSession, ref: snapshotObjectRef}
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
// can hold one, and the bucket their objects plus the orphans.
func seedMediaCrew(t *testing.T, nk *fakeGuestNk, store ledgerStore, f *fakeS3) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	const old, fresh = 50 * time.Hour, time.Hour

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
	f.put("clips", "crews/c2/gone_too.mp4", 30, old) // a crew with no document
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
}

func TestAdminMediaOrphansIsADryRun(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)
	clipsBefore, snapsBefore := f.keys("clips"), f.keys("snaps")

	resp := mediaOrphans(t, nk, "")
	if !resp.DryRun || resp.MinAgeHours != 24 || !resp.Store {
		t.Fatalf("response = %+v", resp)
	}
	c := resp.Clips
	if !c.Configured || c.Scanned != 10 || c.Orphans != 3 || c.Bytes != 1230 || c.TooNew != 1 || c.Unknown != 2 || c.ReadFailed != 0 || c.Deleted != 0 || c.NextAfter != "" {
		t.Fatalf("clips = %+v", c)
	}
	if got := sampleKeys(c); got != "crews/c1/gone.jpg crews/c1/gone.mp4 crews/c2/gone_too.mp4" {
		t.Fatalf("clip orphans = %s", got)
	}
	s := resp.Snapshots
	if s.Scanned != 9 || s.Orphans != 3 || s.Bytes != 300 || s.TooNew != 1 || s.Unknown != 1 || s.Deleted != 0 {
		t.Fatalf("snapshots = %+v", s)
	}
	if got := sampleKeys(s); got != "snapshots/c1/sess_gone/1.jpg snapshots/c1/sess_gone/2.jpg snapshots/c1/sess_gone/3.jpg" {
		t.Fatalf("snapshot orphans = %s", got)
	}
	if c.Sample[1].Bytes != 1000 || time.Since(time.UnixMilli(c.Sample[1].LastModifiedMs)) < 49*time.Hour {
		t.Fatalf("sample = %+v", c.Sample[1])
	}
	// Nothing was deleted.
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
		if part.NextAfter == "" {
			break
		}
		after = part.NextAfter
	}
	if orphans != 3 || scanned != 10 || calls != 4 {
		t.Fatalf("in parts: %d orphans of %d objects in %d calls", orphans, scanned, calls)
	}

	// Only a server caller.
	if _, err := AdminMediaOrphansRPC(ctxWithUser("u1"), testLogger(), nil, nk, ""); errCode(err) != 7 {
		t.Fatalf("a client session: %v, want code 7", err)
	}
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"max_objects":-1}`); errCode(err) != 3 {
		t.Fatalf("a bad max_objects: %v, want code 3", err)
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
	if resp.Store || resp.Clips.Orphans != 4 || resp.Snapshots.Orphans != 4 {
		t.Fatalf("response = %+v", resp)
	}
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"delete":true}`); errCode(err) != 9 {
		t.Fatalf("delete without the store: %v, want code 9", err)
	}
	if len(f.deletes) != 0 {
		t.Fatalf("deleted %v", f.deletes)
	}
}

// The delete flag deletes nothing without the env switch. With both, only
// the orphans go.
func TestAdminMediaOrphansDeleteNeedsTheEnvSwitch(t *testing.T) {
	store := useMemLedger(t)
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)

	t.Setenv(mediaOrphanDeleteEnv, "")
	if _, err := AdminMediaOrphansRPC(context.Background(), testLogger(), nil, nk, `{"delete":true}`); errCode(err) != 9 {
		t.Fatalf("delete without the env switch: %v, want code 9", err)
	}
	if len(f.deletes) != 0 || f.lists != 0 {
		t.Fatalf("without the env switch: deletes %v, %d lists", f.deletes, f.lists)
	}
	// The env switch alone, with no delete flag, is a dry run.
	t.Setenv(mediaOrphanDeleteEnv, "1")
	if resp := mediaOrphans(t, nk, `{"delete":false}`); !resp.DryRun || resp.Clips.Deleted != 0 || len(f.deletes) != 0 {
		t.Fatalf("env switch without the flag: %+v, deletes %v", resp, f.deletes)
	}

	resp := mediaOrphans(t, nk, `{"delete":true}`)
	if resp.DryRun || resp.Clips.Deleted != 3 || resp.Clips.DeleteFailed != 0 || resp.Snapshots.Deleted != 3 {
		t.Fatalf("delete = %+v", resp)
	}
	sort.Strings(f.deletes)
	want := "clips/crews/c1/gone.jpg clips/crews/c1/gone.mp4 clips/crews/c2/gone_too.mp4 " +
		"snaps/snapshots/c1/sess_gone/1.jpg snaps/snapshots/c1/sess_gone/2.jpg snaps/snapshots/c1/sess_gone/3.jpg"
	if got := strings.Join(f.deletes, " "); got != want {
		t.Fatalf("deleted %s", got)
	}
	// What a row points at, what is new and what is unknown stays.
	if got := strings.Join(f.keys("clips"), " "); got != "crews/c1/in_doc.jpg crews/c1/in_doc.mp4 crews/c1/in_ledger.mp4 crews/c1/notes.txt crews/c1/row_only.mp4 crews/c1/uploading.mp4 crews/readme other/c1/gone.mp4" {
		t.Fatalf("clips bucket after the delete: %s", got)
	}
	if got := len(f.keys("snaps")); got != 6 {
		t.Fatalf("snapshots bucket after the delete: %v", f.keys("snaps"))
	}
	// A second run finds nothing.
	if again := mediaOrphans(t, nk, `{"delete":true}`); again.Clips.Orphans != 0 || again.Snapshots.Orphans != 0 {
		t.Fatalf("second delete = %+v", again)
	}
}

// A crew whose documents cannot be read has no orphans: its objects are
// kept and counted.
func TestAdminMediaOrphansKeepsWhatItCannotCheck(t *testing.T) {
	store := useMemLedger(t)
	t.Setenv(mediaOrphanDeleteEnv, "1")
	nk := newFakeGuestNk()
	f := newFakeS3(t)
	useFakeBuckets(t, f)
	seedMediaCrew(t, nk, store, f)

	resp := mediaOrphans(t, failHistReadNk{fakeGuestNk: nk, collection: CrewClipsCollection}, `{"delete":true}`)
	if resp.Clips.Orphans != 0 || resp.Clips.ReadFailed != 8 || resp.Snapshots.Orphans != 0 || resp.Snapshots.ReadFailed != 8 || len(f.deletes) != 0 {
		t.Fatalf("response = %+v, deletes %v", resp, f.deletes)
	}
	// The store fails: the same.
	useLedger(t, failingClipReads{store})
	resp = mediaOrphans(t, nk, `{"delete":true}`)
	if resp.Clips.Orphans != 0 || resp.Clips.ReadFailed != 5 || resp.Snapshots.ReadFailed != 5 || len(f.deletes) != 0 {
		t.Fatalf("response with a failing store = %+v, deletes %v", resp, f.deletes)
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
