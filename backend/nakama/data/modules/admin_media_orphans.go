package main

// admin_media_orphans: the clip and snapshot objects in the bucket that no
// clip and no stream session of a crew points at
// (plans/DURABLE-LEDGER.md section 6a). The caps of the legacy documents
// dropped clips and stream sessions and left their media in the bucket.
//
// The RPC is server-to-server only, as the other admin RPCs (spec 04 section
// 4). It has two modes, and a delete cannot be undone, so they are apart:
//
//   - The scan lists a prefix and reports the orphans. It deletes nothing.
//   - The delete takes the keys of a scan. It checks each key again as an
//     orphan with a new read of everything, deletes at most 500 keys in one
//     call, and answers with each key it deleted and each key it refused.
//     It needs the env switch and the durable store.
//
// Three rules keep an object that may still have an owner:
//
//   - A crew id that is not a group on this install is unknown_crew. Another
//     install can share the bucket. Such an object is never deleted here.
//   - A session whose newest snapshot is younger than 24 hours is skipped
//     whole. stop_stream and the socket drop delete the stream record before
//     they write the stream session, so for some seconds nothing holds the
//     session id.
//   - An object of a crew that cannot be read is read_failed, not an orphan.

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
	// mediaOrphanSample is the number of orphans one scan names for each
	// prefix. It is the number of keys one delete takes.
	mediaOrphanSample = mediaOrphanDeleteMax
	// mediaOrphanDeleteMax is the hard cap of the keys of one delete call.
	mediaOrphanDeleteMax = 500
	// mediaOrphanBudget bounds the scan of one call. The reads of a call
	// have mediaOrphanGrace more, so that a read that began in the budget
	// can end.
	mediaOrphanBudget = 2 * time.Minute
	mediaOrphanGrace  = 30 * time.Second
	// mediaSessionMaxObjects bounds the list of the snapshots of one
	// session. A session with more is not judged.
	mediaSessionMaxObjects = 20000

	// The deterministic prefixes. A clip is crews/{crew_id}/{clip_id}.mp4 with
	// the poster of a game clip beside it as .jpg (gameClipKeys). A snapshot
	// is snapshots/{crew_id}/{session_id}/{unix_ms}.jpg (ListSnapshotURLs).
	mediaClipPrefix     = "crews/"
	mediaSnapshotPrefix = "snapshots/"

	// mediaOrphanDeleteEnv must be "1" for a call to delete. The delete flag
	// of the request alone deletes nothing.
	mediaOrphanDeleteEnv = "MELLO_MEDIA_ORPHAN_DELETE"
)

// mediaOrphanClock is the clock of the time budget. A var so a test ends the
// budget in the middle of a scan.
var mediaOrphanClock = time.Now

// AdminMediaOrphansRequest is the admin_media_orphans request.
type AdminMediaOrphansRequest struct {
	// Delete asks to delete ClipKeys and SnapshotKeys. It needs the env
	// MELLO_MEDIA_ORPHAN_DELETE=1 on the server, and the durable store.
	// Without it the call is a scan and the keys must be empty.
	Delete bool `json:"delete"`
	// ClipKeys and SnapshotKeys are the keys to delete: orphans that a scan
	// named. At most 500 in one call, both lists together.
	ClipKeys     []string `json:"clip_keys"`
	SnapshotKeys []string `json:"snapshot_keys"`
	// MaxObjects bounds the objects a scan lists for each prefix.
	MaxObjects int `json:"max_objects"`
	// ClipsAfter and SnapshotsAfter continue a scan: the next_after of the
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

// AdminMediaOrphanKind is the result of the scan of one prefix.
type AdminMediaOrphanKind struct {
	// Configured is false when the server has no bucket for this prefix.
	Configured bool `json:"configured"`
	// Scanned counts the objects this call judged.
	Scanned int `json:"scanned"`
	// Orphans and Bytes count the objects with no row that are older than
	// 24 hours.
	Orphans int   `json:"orphans"`
	Bytes   int64 `json:"bytes"`
	// TooNew counts the objects with no row that are younger than that, and
	// the snapshots of a session that has a snapshot younger than that.
	TooNew int `json:"too_new"`
	// Unknown counts the keys that do not follow the layout of the prefix.
	// UnknownCrew counts the objects of a crew id that is not a group on
	// this install. ReadFailed counts the objects of a crew whose group,
	// documents or rows could not be read. None of them is an orphan.
	Unknown     int `json:"unknown"`
	UnknownCrew int `json:"unknown_crew"`
	ReadFailed  int `json:"read_failed"`
	// Sample names the orphans of this call, up to 500. They are the keys a
	// delete takes.
	Sample []AdminMediaOrphan `json:"sample"`
	// Incomplete is true when the prefix has objects this call did not
	// judge: the list was cut at max_objects, or the time budget ended.
	// NextAfter is then the last key this call judged. Send it as
	// clips_after or snapshots_after: the next call starts at the first key
	// that was not judged. It is empty when this call judged no key.
	Incomplete bool   `json:"incomplete"`
	NextAfter  string `json:"next_after,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AdminMediaOrphanRefusal is one key that a delete did not delete.
type AdminMediaOrphanRefusal struct {
	Key string `json:"key"`
	// Reason is one of: unknown_key, unknown_crew, read_failed, held,
	// not_found, too_new, session_active, delete_failed, not_configured.
	Reason string `json:"reason"`
}

// AdminMediaOrphanDelete is the result of the delete for one prefix: every
// key of the request is in one of the two lists.
type AdminMediaOrphanDelete struct {
	Deleted []string                  `json:"deleted"`
	Refused []AdminMediaOrphanRefusal `json:"refused"`
}

// AdminMediaOrphansResponse is the admin_media_orphans response.
type AdminMediaOrphansResponse struct {
	// DryRun is true for a scan.
	DryRun      bool `json:"dry_run"`
	MinAgeHours int  `json:"min_age_hours"`
	// Store says the durable store was read. Without it only the legacy
	// documents say what a crew holds, and a delete is refused.
	Store bool `json:"store"`
	// Clips and Snapshots are the scan. A delete call does not scan.
	Clips     AdminMediaOrphanKind `json:"clips"`
	Snapshots AdminMediaOrphanKind `json:"snapshots"`
	// DeletedClips and DeletedSnapshots are set by a delete call.
	DeletedClips     *AdminMediaOrphanDelete `json:"deleted_clips,omitempty"`
	DeletedSnapshots *AdminMediaOrphanDelete `json:"deleted_snapshots,omitempty"`
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
	// known is false when the crew id is not a group on this install.
	known      bool
	clipIDs    map[string]bool
	sessionIDs map[string]bool
	err        error
}

// mediaRefIndex answers if a crew holds a clip or a stream session. It reads
// the group and the legacy documents of a crew once, and the durable store
// for each id. Both count: before the cutover of phase 1b the document is
// the truth, and after it the store is.
//
// One index serves one scan. The delete makes a new index for each key, so
// each key is judged on a new read.
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

// crew reads one crew: its group, the clips, the durable copy of the stream
// sessions, the ledger, and the stream that is live now.
func (m *mediaRefIndex) crew(crewID string) *crewMediaRefs {
	if refs, ok := m.crews[crewID]; ok {
		return refs
	}
	refs := &crewMediaRefs{clipIDs: map[string]bool{}, sessionIDs: map[string]bool{}}
	m.crews[crewID] = refs

	// The crew must be a group on this install. An id that is no group can
	// be the crew of another install that shares the bucket.
	groups, err := m.nk.GroupsGetId(m.ctx, []string{crewID})
	if err != nil {
		refs.err = err
		return refs
	}
	for _, g := range groups {
		if g.GetId() == crewID {
			refs.known = true
		}
	}
	if !refs.known {
		return refs
	}

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
// object. known is false when the crew id is not a group on this install. A
// read error is returned: the object is then not an orphan.
func (m *mediaRefIndex) held(crewID, typ, ref string) (held, known bool, err error) {
	refs := m.crew(crewID)
	if refs.err != nil {
		return false, false, refs.err
	}
	if !refs.known {
		return false, false, nil
	}
	if (typ == ledgerTypeClip && refs.clipIDs[ref]) || (typ == ledgerTypeStreamSession && refs.sessionIDs[ref]) {
		return true, true, nil
	}
	held, err = m.row(crewID, typ, ref)
	return held, true, err
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
	// groupPrefix returns the prefix of the objects that are judged as one
	// group by their newest object: the snapshots of one session. It is nil
	// when each object is judged alone (the clips).
	groupPrefix func(crewID, ref string) string
}

// mediaGroupAges answers if a group of objects (the snapshots of a session)
// has an object younger than the cutoff. It lists the prefix of a group once
// in a call.
type mediaGroupAges struct {
	ctx    context.Context
	b      mediaBucket
	cutoff time.Time
	fresh  map[string]bool
}

func newMediaGroupAges(ctx context.Context, b mediaBucket, cutoff time.Time) *mediaGroupAges {
	return &mediaGroupAges{ctx: ctx, b: b, cutoff: cutoff, fresh: map[string]bool{}}
}

// hasFresh reports whether the group of the object has an object that is
// younger than the cutoff or has no time. A group that is too long to list
// counts as fresh: it is not judged. It is false for a bucket with no groups.
func (g *mediaGroupAges) hasFresh(crewID, ref string) (bool, error) {
	if g.b.groupPrefix == nil {
		return false, nil
	}
	prefix := g.b.groupPrefix(crewID, ref)
	if fresh, ok := g.fresh[prefix]; ok {
		return fresh, nil
	}
	objects, more, err := listObjectsAfter(g.ctx, g.b.client, g.b.bucket, prefix, "", mediaSessionMaxObjects)
	if err != nil {
		return false, err
	}
	fresh := more
	for _, obj := range objects {
		if obj.LastModified.IsZero() || !obj.LastModified.Before(g.cutoff) {
			fresh = true
			break
		}
	}
	g.fresh[prefix] = fresh
	return fresh, nil
}

// Verdicts of judgeMediaObject. Only mediaOrphan is an orphan.
const (
	mediaOrphan        = "orphan"
	mediaUnknownKey    = "unknown_key"
	mediaUnknownCrew   = "unknown_crew"
	mediaReadFailed    = "read_failed"
	mediaHeld          = "held"
	mediaTooNew        = "too_new"
	mediaSessionActive = "session_active"
)

// judgeMediaObject sorts one object: held by a row, too new, unknown, or
// orphan. The scan and the delete use the same rule.
func judgeMediaObject(index *mediaRefIndex, ages *mediaGroupAges, b mediaBucket, obj s3ObjectEntry, cutoff time.Time) string {
	crewID, ref, ok := b.ref(obj.Key)
	if !ok {
		return mediaUnknownKey
	}
	held, known, err := index.held(crewID, b.typ, ref)
	switch {
	case err != nil:
		return mediaReadFailed
	case !known:
		return mediaUnknownCrew
	case held:
		return mediaHeld
	}
	// An object with no time is kept: its age is not known.
	if obj.LastModified.IsZero() || !obj.LastModified.Before(cutoff) {
		return mediaTooNew
	}
	fresh, err := ages.hasFresh(crewID, ref)
	switch {
	case err != nil:
		return mediaReadFailed
	case fresh:
		return mediaSessionActive
	}
	return mediaOrphan
}

// scanMediaOrphans lists one prefix and sorts its objects. It deletes
// nothing. It stops when the time budget ends (deadline): the objects it did
// not judge are not counted, and next_after is the last key it judged.
func scanMediaOrphans(ctx context.Context, logger runtime.Logger, index *mediaRefIndex, b mediaBucket, after string, maxObjects int, now, deadline time.Time) AdminMediaOrphanKind {
	out := AdminMediaOrphanKind{Configured: b.client != nil, Sample: []AdminMediaOrphan{}}
	if b.client == nil {
		return out
	}
	if !mediaOrphanClock().Before(deadline) {
		// The budget ended before this prefix: the next call starts where
		// this one would have.
		out.Incomplete, out.NextAfter = true, after
		return out
	}
	objects, more, err := listObjectsAfter(ctx, b.client, b.bucket, b.prefix, after, maxObjects)
	if err != nil {
		logger.Error("admin_media_orphans: list of %s%s: %v", b.bucket, b.prefix, err)
		out.Error = "list failed"
		more = true
	}
	cutoff := now.Add(-mediaOrphanMinAge)
	ages := newMediaGroupAges(ctx, b, cutoff)
	last := after
	for _, obj := range objects {
		if !mediaOrphanClock().Before(deadline) {
			more = true
			break
		}
		out.Scanned++
		last = obj.Key
		switch judgeMediaObject(index, ages, b, obj, cutoff) {
		case mediaUnknownKey:
			out.Unknown++
		case mediaUnknownCrew:
			out.UnknownCrew++
		case mediaReadFailed:
			out.ReadFailed++
		case mediaTooNew, mediaSessionActive:
			out.TooNew++
		case mediaOrphan:
			out.Orphans++
			out.Bytes += obj.Size
			if len(out.Sample) < mediaOrphanSample {
				out.Sample = append(out.Sample, AdminMediaOrphan{Key: obj.Key, Bytes: obj.Size, LastModifiedMs: obj.LastModified.UnixMilli()})
			}
		}
	}
	if more {
		out.Incomplete, out.NextAfter = true, last
	}
	return out
}

// deleteMediaOrphans deletes the keys of one prefix that are orphans now.
// Each key is judged on its own new read: the object, the group and the
// documents of its crew, the rows of the store, and for a snapshot the other
// snapshots of its session. Every key is in one of the two lists of the
// result.
func deleteMediaOrphans(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, store ledgerStore, b mediaBucket, keys []string, now time.Time) *AdminMediaOrphanDelete {
	out := &AdminMediaOrphanDelete{Deleted: []string{}, Refused: []AdminMediaOrphanRefusal{}}
	refuse := func(key, reason string) {
		out.Refused = append(out.Refused, AdminMediaOrphanRefusal{Key: key, Reason: reason})
	}
	cutoff := now.Add(-mediaOrphanMinAge)
	done := map[string]bool{}
	for _, key := range keys {
		if done[key] {
			continue
		}
		done[key] = true
		if b.client == nil {
			refuse(key, "not_configured")
			continue
		}
		if !strings.HasPrefix(key, b.prefix) {
			refuse(key, mediaUnknownKey)
			continue
		}
		// The object as the bucket holds it now.
		listed, _, err := listObjectsAfter(ctx, b.client, b.bucket, key, "", 2)
		if err != nil {
			refuse(key, mediaReadFailed)
			continue
		}
		var obj *s3ObjectEntry
		for i := range listed {
			if listed[i].Key == key {
				obj = &listed[i]
			}
		}
		if obj == nil {
			refuse(key, "not_found")
			continue
		}
		// A new index and a new list of the session for each key: nothing
		// that an earlier key read is used again.
		verdict := judgeMediaObject(newMediaRefIndex(ctx, nk, store), newMediaGroupAges(ctx, b, cutoff), b, *obj, cutoff)
		if verdict != mediaOrphan {
			refuse(key, verdict)
			continue
		}
		if err := deleteObject(ctx, b.client, b.bucket, key); err != nil {
			logger.Error("admin_media_orphans: delete of %s/%s failed: %v", b.bucket, key, err)
			refuse(key, "delete_failed")
			continue
		}
		out.Deleted = append(out.Deleted, key)
		logger.Warn("admin_media_orphans: deleted %s/%s (%d bytes)", b.bucket, key, obj.Size)
	}
	return out
}

// snapshotSessionPrefix is the prefix of the snapshots of one session.
func snapshotSessionPrefix(crewID, sessionID string) string {
	return mediaSnapshotPrefix + crewID + "/" + sessionID + "/"
}

// mediaOrphanBuckets returns the clips bucket and the snapshots bucket of
// this server. A var so a test points them at a local fake.
var mediaOrphanBuckets = func() (clips, snapshots mediaBucket) {
	initS3()
	initSnapshotsS3()
	clips = mediaBucket{client: s3Client, bucket: s3Bucket, prefix: mediaClipPrefix, typ: ledgerTypeClip, ref: clipObjectRef}
	snapshots = mediaBucket{client: snapshotsClient, bucket: snapshotsBucket, prefix: mediaSnapshotPrefix, typ: ledgerTypeStreamSession, ref: snapshotObjectRef, groupPrefix: snapshotSessionPrefix}
	return clips, snapshots
}

// AdminMediaOrphansRPC scans the clip and snapshot objects that no row
// points at, or deletes the keys that a scan named.
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
	keys := len(req.ClipKeys) + len(req.SnapshotKeys)
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
		if keys == 0 {
			return "", runtime.NewError("delete needs clip_keys or snapshot_keys from a scan", 3)
		}
		if keys > mediaOrphanDeleteMax {
			return "", runtime.NewError("too many keys: one delete takes at most 500", 3)
		}
	} else if keys > 0 {
		return "", runtime.NewError("clip_keys and snapshot_keys need delete", 3)
	}

	start := mediaOrphanClock()
	ctx, cancel := context.WithTimeout(ctx, mediaOrphanBudget+mediaOrphanGrace)
	defer cancel()
	clips, snapshots := mediaOrphanBuckets()
	now := time.Now()
	resp := AdminMediaOrphansResponse{
		DryRun:      !req.Delete,
		MinAgeHours: int(mediaOrphanMinAge / time.Hour),
		Store:       store != nil,
		Clips:       AdminMediaOrphanKind{Configured: clips.client != nil, Sample: []AdminMediaOrphan{}},
		Snapshots:   AdminMediaOrphanKind{Configured: snapshots.client != nil, Sample: []AdminMediaOrphan{}},
	}
	if req.Delete {
		resp.DeletedClips = deleteMediaOrphans(ctx, logger, nk, store, clips, req.ClipKeys, now)
		resp.DeletedSnapshots = deleteMediaOrphans(ctx, logger, nk, store, snapshots, req.SnapshotKeys, now)
		logger.Info("admin_media_orphans delete clips: deleted=%d refused=%d snapshots: deleted=%d refused=%d",
			len(resp.DeletedClips.Deleted), len(resp.DeletedClips.Refused), len(resp.DeletedSnapshots.Deleted), len(resp.DeletedSnapshots.Refused))
	} else {
		index := newMediaRefIndex(ctx, nk, store)
		deadline := start.Add(mediaOrphanBudget)
		resp.Clips = scanMediaOrphans(ctx, logger, index, clips, req.ClipsAfter, req.MaxObjects, now, deadline)
		resp.Snapshots = scanMediaOrphans(ctx, logger, index, snapshots, req.SnapshotsAfter, req.MaxObjects, now, deadline)
		logger.Info("admin_media_orphans scan clips: scanned=%d orphans=%d bytes=%d unknown_crew=%d incomplete=%t snapshots: scanned=%d orphans=%d bytes=%d unknown_crew=%d incomplete=%t",
			resp.Clips.Scanned, resp.Clips.Orphans, resp.Clips.Bytes, resp.Clips.UnknownCrew, resp.Clips.Incomplete,
			resp.Snapshots.Scanned, resp.Snapshots.Orphans, resp.Snapshots.Bytes, resp.Snapshots.UnknownCrew, resp.Snapshots.Incomplete)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("encode failed", 13)
	}
	return string(out), nil
}
