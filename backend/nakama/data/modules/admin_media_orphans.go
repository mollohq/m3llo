package main

// admin_media_orphans: the clip and snapshot objects in the bucket that no
// clip and no stream session of a crew points at
// (plans/DURABLE-LEDGER.md section 6a). The caps of the legacy documents
// dropped clips and stream sessions and left their media in the bucket.
//
// The RPC is server-to-server only, as the other admin RPCs (spec 04 section
// 4). It is a dry run unless the request and the env both ask for a delete.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// mediaOrphanMinAge is how old an object must be to count as an orphan.
	// An upload that is in flight has an object and, for a moment, no row.
	mediaOrphanMinAge = 24 * time.Hour
	// mediaOrphanDefaultObjects and mediaOrphanMaxObjects bound the objects
	// one call lists for each prefix. A call continues after next_after.
	mediaOrphanDefaultObjects = 5000
	mediaOrphanMaxObjects     = 50000
	// mediaOrphanSample is the number of orphans one call names.
	mediaOrphanSample = 50
	// mediaOrphanBudget bounds one call.
	mediaOrphanBudget = 2 * time.Minute

	// The deterministic prefixes. A clip is crews/{crew_id}/{clip_id}.mp4 with
	// the poster of a game clip beside it as .jpg (gameClipKeys). A snapshot
	// is snapshots/{crew_id}/{session_id}/{unix_ms}.jpg (ListSnapshotURLs).
	mediaClipPrefix     = "crews/"
	mediaSnapshotPrefix = "snapshots/"

	// mediaOrphanDeleteEnv must be "1" for a call to delete. The delete flag
	// of the request alone deletes nothing.
	mediaOrphanDeleteEnv = "MELLO_MEDIA_ORPHAN_DELETE"
)

// AdminMediaOrphansRequest is the admin_media_orphans request.
type AdminMediaOrphansRequest struct {
	// Delete asks to delete the orphans this call finds. It needs the env
	// MELLO_MEDIA_ORPHAN_DELETE=1 on the server, and the durable store.
	Delete bool `json:"delete"`
	// MaxObjects bounds the objects listed for each prefix.
	MaxObjects int `json:"max_objects"`
	// ClipsAfter and SnapshotsAfter continue a list: the next_after of the
	// call before.
	ClipsAfter     string `json:"clips_after"`
	SnapshotsAfter string `json:"snapshots_after"`
}

// AdminMediaOrphan is one orphan object.
type AdminMediaOrphan struct {
	Key            string `json:"key"`
	Bytes          int64  `json:"bytes"`
	LastModifiedMs int64  `json:"last_modified_ms"`
}

// AdminMediaOrphanKind is the result for one prefix.
type AdminMediaOrphanKind struct {
	// Configured is false when the server has no bucket for this prefix.
	Configured bool `json:"configured"`
	Scanned    int  `json:"scanned"`
	// Orphans and Bytes count the objects with no row that are older than
	// 24 hours.
	Orphans int   `json:"orphans"`
	Bytes   int64 `json:"bytes"`
	// TooNew counts the objects with no row that are younger than that.
	TooNew int `json:"too_new"`
	// Unknown counts the keys that do not follow the layout of the prefix.
	// ReadFailed counts the objects of a crew whose documents or rows could
	// not be read. Neither is an orphan, and neither is deleted.
	Unknown    int `json:"unknown"`
	ReadFailed int `json:"read_failed"`
	// Deleted and DeleteFailed are 0 in a dry run.
	Deleted      int `json:"deleted"`
	DeleteFailed int `json:"delete_failed"`
	// Sample names the first orphans of this call.
	Sample []AdminMediaOrphan `json:"sample"`
	// NextAfter is set when the prefix holds more objects than this call
	// listed. Send it as clips_after or snapshots_after.
	NextAfter string `json:"next_after,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AdminMediaOrphansResponse is the admin_media_orphans response.
type AdminMediaOrphansResponse struct {
	DryRun      bool `json:"dry_run"`
	MinAgeHours int  `json:"min_age_hours"`
	// Store says the durable store was read. Without it only the legacy
	// documents say what a crew holds, and a delete is refused.
	Store     bool                 `json:"store"`
	Clips     AdminMediaOrphanKind `json:"clips"`
	Snapshots AdminMediaOrphanKind `json:"snapshots"`
}

// clipObjectRef reads the crew id and the clip_id from the key of a clip
// object: crews/{crew_id}/{clip_id}.mp4 or .jpg.
func clipObjectRef(key string) (crewID, clipID string, ok bool) {
	rest, found := strings.CutPrefix(key, mediaClipPrefix)
	if !found {
		return "", "", false
	}
	crewID, name, found := strings.Cut(rest, "/")
	if !found || crewID == "" {
		return "", "", false
	}
	for _, ext := range []string{".mp4", ".jpg"} {
		if id, cut := strings.CutSuffix(name, ext); cut && id != "" {
			return crewID, id, true
		}
	}
	return "", "", false
}

// snapshotObjectRef reads the crew id and the session id from the key of a
// snapshot object: snapshots/{crew_id}/{session_id}/{file}.jpg.
func snapshotObjectRef(key string) (crewID, sessionID string, ok bool) {
	rest, found := strings.CutPrefix(key, mediaSnapshotPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || !strings.HasSuffix(parts[2], ".jpg") || parts[2] == ".jpg" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// crewMediaRefs is what the legacy documents of one crew point at.
type crewMediaRefs struct {
	clipIDs    map[string]bool
	sessionIDs map[string]bool
	err        error
}

// mediaRefIndex answers if a crew holds a clip or a stream session. It reads
// the legacy documents of a crew once, and the durable store for each id.
// Both count: before the cutover of phase 1b the document is the truth, and
// after it the store is.
type mediaRefIndex struct {
	ctx   context.Context
	nk    runtime.NakamaModule
	store ledgerStore
	crews map[string]*crewMediaRefs
	rows  map[string]bool
}

func newMediaRefIndex(ctx context.Context, nk runtime.NakamaModule, store ledgerStore) *mediaRefIndex {
	return &mediaRefIndex{ctx: ctx, nk: nk, store: store, crews: map[string]*crewMediaRefs{}, rows: map[string]bool{}}
}

// crew reads the documents of one crew: the clips, the durable copy of the
// stream sessions, the ledger, and the stream that is live now.
func (m *mediaRefIndex) crew(crewID string) *crewMediaRefs {
	if refs, ok := m.crews[crewID]; ok {
		return refs
	}
	refs := &crewMediaRefs{clipIDs: map[string]bool{}, sessionIDs: map[string]bool{}}
	m.crews[crewID] = refs

	var clips CrewClipsDoc
	if _, _, err := readDocStrict(m.ctx, m.nk, CrewClipsCollection, crewID, &clips); err != nil {
		refs.err = err
		return refs
	}
	for _, c := range clips.Clips {
		refs.clipIDs[c.ClipID] = true
	}
	var streams CrewStreamSessionsDoc
	if _, _, err := readDocStrict(m.ctx, m.nk, CrewStreamSessionsCollection, crewID, &streams); err != nil {
		refs.err = err
		return refs
	}
	for _, s := range streams.Sessions {
		refs.sessionIDs[s.SessionID] = true
	}
	// The ledger: the stream sessions of 7 days, and the clip events that an
	// old ledger holds.
	ledger, err := readLedgerDocStrict(m.ctx, m.nk, crewID)
	if err != nil {
		refs.err = err
		return refs
	}
	for _, ev := range ledger.Events {
		switch ev.Type {
		case ledgerTypeStreamSession:
			refs.sessionIDs[eventRef(ev)] = true
		case ledgerTypeClip:
			if cd, ok := decodeCardData[ClipData](ev.Data); ok {
				refs.clipIDs[cd.ClipID] = true
			}
		}
	}
	// A stream that is live has snapshots and no stream session yet.
	var meta StreamMeta
	found, _, err := readDocStrict(m.ctx, m.nk, StreamMetaCollection, crewID, &meta)
	if err != nil {
		refs.err = err
		return refs
	}
	if found {
		refs.sessionIDs[meta.StreamID] = true
	}
	return refs
}

// row reports whether the store holds a row of the type with the ref.
func (m *mediaRefIndex) row(crewID, typ, ref string) (bool, error) {
	if m.store == nil {
		return false, nil
	}
	key := crewID + "\x00" + typ + "\x00" + ref
	if held, ok := m.rows[key]; ok {
		return held, nil
	}
	rows, err := m.store.ByRef(m.ctx, crewID, typ, ref)
	if err != nil {
		return false, err
	}
	m.rows[key] = len(rows) > 0
	return len(rows) > 0, nil
}

// held reports whether a crew holds the clip or the stream session of an
// object. A read error is returned: the object is then not an orphan.
func (m *mediaRefIndex) held(crewID, typ, ref string) (bool, error) {
	refs := m.crew(crewID)
	if refs.err != nil {
		return false, refs.err
	}
	if (typ == ledgerTypeClip && refs.clipIDs[ref]) || (typ == ledgerTypeStreamSession && refs.sessionIDs[ref]) {
		return true, nil
	}
	return m.row(crewID, typ, ref)
}

// mediaBucket is one bucket and prefix that admin_media_orphans scans.
type mediaBucket struct {
	client *s3.Client
	bucket string
	prefix string
	// typ is the row type the objects belong to, ref reads the crew and the
	// ref of the row from a key.
	typ string
	ref func(key string) (crewID, ref string, ok bool)
}

// scanMediaOrphans lists one prefix and sorts its objects: held by a row,
// too new, unknown, or orphan. With del it deletes the orphans.
func scanMediaOrphans(ctx context.Context, logger runtime.Logger, index *mediaRefIndex, b mediaBucket, after string, maxObjects int, now time.Time, del bool) AdminMediaOrphanKind {
	out := AdminMediaOrphanKind{Configured: b.client != nil, Sample: []AdminMediaOrphan{}}
	if b.client == nil {
		return out
	}
	objects, more, err := listObjectsAfter(ctx, b.client, b.bucket, b.prefix, after, maxObjects)
	if err != nil {
		logger.Error("admin_media_orphans: list of %s%s: %v", b.bucket, b.prefix, err)
		out.Error = "list failed"
	}
	if more && len(objects) > 0 {
		out.NextAfter = objects[len(objects)-1].Key
	}
	cutoff := now.Add(-mediaOrphanMinAge)
	for _, obj := range objects {
		out.Scanned++
		crewID, ref, ok := b.ref(obj.Key)
		if !ok {
			out.Unknown++
			continue
		}
		held, err := index.held(crewID, b.typ, ref)
		if err != nil {
			out.ReadFailed++
			continue
		}
		if held {
			continue
		}
		// An object with no time is kept: its age is not known.
		if obj.LastModified.IsZero() || !obj.LastModified.Before(cutoff) {
			out.TooNew++
			continue
		}
		out.Orphans++
		out.Bytes += obj.Size
		if len(out.Sample) < mediaOrphanSample {
			out.Sample = append(out.Sample, AdminMediaOrphan{Key: obj.Key, Bytes: obj.Size, LastModifiedMs: obj.LastModified.UnixMilli()})
		}
		if !del {
			continue
		}
		if err := deleteObject(ctx, b.client, b.bucket, obj.Key); err != nil {
			out.DeleteFailed++
			logger.Error("admin_media_orphans: delete of %s/%s failed: %v", b.bucket, obj.Key, err)
			continue
		}
		out.Deleted++
		logger.Warn("admin_media_orphans: deleted %s/%s (%d bytes)", b.bucket, obj.Key, obj.Size)
	}
	return out
}

// mediaOrphanBuckets returns the clips bucket and the snapshots bucket of
// this server. A var so a test points them at a local fake.
var mediaOrphanBuckets = func() (clips, snapshots mediaBucket) {
	initS3()
	initSnapshotsS3()
	clips = mediaBucket{client: s3Client, bucket: s3Bucket, prefix: mediaClipPrefix, typ: ledgerTypeClip, ref: clipObjectRef}
	snapshots = mediaBucket{client: snapshotsClient, bucket: snapshotsBucket, prefix: mediaSnapshotPrefix, typ: ledgerTypeStreamSession, ref: snapshotObjectRef}
	return clips, snapshots
}

// AdminMediaOrphansRPC lists, and on request deletes, the clip and snapshot
// objects that no row points at.
func AdminMediaOrphansRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if err := requireServerCaller(ctx, "admin_media_orphans"); err != nil {
		return "", err
	}
	var req AdminMediaOrphansRequest
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &req); err != nil {
			return "", runtime.NewError("invalid request", 3)
		}
	}
	if req.MaxObjects < 0 || req.MaxObjects > mediaOrphanMaxObjects {
		return "", runtime.NewError("max_objects out of range", 3)
	}
	if req.MaxObjects == 0 {
		req.MaxObjects = mediaOrphanDefaultObjects
	}
	store := currentLedger()
	if req.Delete {
		// A delete needs the env switch, and the store: without the rows the
		// clips that a capped document dropped would all be orphans.
		if os.Getenv(mediaOrphanDeleteEnv) != "1" {
			return "", runtime.NewError("delete is off: set "+mediaOrphanDeleteEnv+"=1 on the server", 9)
		}
		if store == nil {
			return "", runtime.NewError("delete needs the durable store", 9)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, mediaOrphanBudget)
	defer cancel()
	index := newMediaRefIndex(ctx, nk, store)
	clips, snapshots := mediaOrphanBuckets()
	now := time.Now()
	resp := AdminMediaOrphansResponse{
		DryRun:      !req.Delete,
		MinAgeHours: int(mediaOrphanMinAge / time.Hour),
		Store:       store != nil,
		Clips:       scanMediaOrphans(ctx, logger, index, clips, req.ClipsAfter, req.MaxObjects, now, req.Delete),
		Snapshots:   scanMediaOrphans(ctx, logger, index, snapshots, req.SnapshotsAfter, req.MaxObjects, now, req.Delete),
	}
	logger.Info("admin_media_orphans dry_run=%t clips: scanned=%d orphans=%d bytes=%d deleted=%d snapshots: scanned=%d orphans=%d bytes=%d deleted=%d",
		resp.DryRun, resp.Clips.Scanned, resp.Clips.Orphans, resp.Clips.Bytes, resp.Clips.Deleted,
		resp.Snapshots.Scanned, resp.Snapshots.Orphans, resp.Snapshots.Bytes, resp.Snapshots.Deleted)
	out, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("encode failed", 13)
	}
	return string(out), nil
}
