package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// The purge of a deleted crew (plans/DURABLE-LEDGER.md section 7): everything
// of the crew goes. The rows of the durable store, the legacy documents, the
// other documents keyed by the crew id, and the media under the prefixes of
// the crew.
//
// A purge starts with a purge record. The record:
//
//   - lets a purge that failed or was cut continue (the deletion job, and
//     the RPC admin_purge_crew);
//   - stops the reconcile from importing a document of the crew that was not
//     deleted, or that a late writer made again. The reconcile deletes such a
//     document (ledgerReconciler.tick);
//   - says that the crew was a crew of this install. A media bucket can be
//     shared with another install, and admin_media_orphans never deletes
//     under a crew id that is not a group here. The purge deletes under the
//     prefix of a crew only with this proof.
//
// A purge runs a second time one hour after it was done (crewPurgeSettle). A
// presigned upload, a stream that ends or a voice session that ends can
// write for the crew after the group is gone.

const (
	// CrewPurgeCollection holds one purge record for each deleted crew,
	// system-owned, key crew id. A record is kept: it is the memory that the
	// crew is gone.
	CrewPurgeCollection = "ledger_crew_purge"

	// crewPurgeBudget bounds one run of a purge. A run that is cut continues
	// in the next run.
	crewPurgeBudget = 2 * time.Minute
	// crewPurgeSettle is the wait before the second run. It is the life of
	// the longest presigned upload URL (gameClipURLExpiry).
	crewPurgeSettle = 1 * time.Hour
	// crewPurgeListBatch is the number of objects of one bucket list.
	crewPurgeListBatch = 1000
)

// crewPurgeRecord is the purge record of a crew.
type crewPurgeRecord struct {
	CrewID    string `json:"crew_id"`
	StartedMs int64  `json:"started_ms"`
	// DoneMs is the end of the first run that removed everything.
	DoneMs int64 `json:"done_ms,omitempty"`
	// SettledMs is the end of the second run, crewPurgeSettle after the first.
	SettledMs int64 `json:"settled_ms,omitempty"`
	// StorePending is true when a run had no durable store and removed all
	// but the rows of the store. The purge is not done.
	StorePending bool   `json:"store_pending,omitempty"`
	LastError    string `json:"last_error,omitempty"`
}

// crewPurgeResult is what one run of a purge did.
type crewPurgeResult struct {
	CrewID string `json:"crew_id"`
	// Done is true when the run removed everything it found, with no error.
	Done bool `json:"done"`
	// Incomplete is true when the run was cut at its time budget.
	Incomplete bool `json:"incomplete"`
	// StorePending is true when the durable store is not loaded: the rows of
	// the crew are not deleted, and the purge is not done.
	StorePending bool     `json:"store_pending,omitempty"`
	MediaDeleted int      `json:"media_deleted"`
	DocsDeleted  int      `json:"docs_deleted"`
	StoreDeleted bool     `json:"store_deleted"`
	MediaSkipped []string `json:"media_skipped,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

func (r *crewPurgeResult) fail(format string, args ...interface{}) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

// purgeCrewIDPattern is the shape of a crew id: a Nakama group id. The id is
// part of object prefixes, so nothing else is purged.
var purgeCrewIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validPurgeCrewID(crewID string) bool {
	return purgeCrewIDPattern.MatchString(crewID)
}

// crewPurgeNow is the clock of the purge. Tests change it.
var crewPurgeNow = time.Now

// crewPurgeAsync runs the purge that a delete starts. A var so a test runs
// it inline.
var crewPurgeAsync = func(fn func()) { go fn() }

// crewGiftsBucket returns the bucket of the crew banners and the crew emoji.
// A var so a test points it at a local fake.
var crewGiftsBucket = func() (*s3.Client, string) {
	initChatS3()
	return chatClient, chatBucket
}

func readCrewPurgeRecord(ctx context.Context, nk runtime.NakamaModule, crewID string) (*crewPurgeRecord, error) {
	var rec crewPurgeRecord
	found, _, err := readDocStrict(ctx, nk, CrewPurgeCollection, crewID, &rec)
	if err != nil || !found {
		return nil, err
	}
	rec.CrewID = crewID
	return &rec, nil
}

func writeCrewPurgeRecord(ctx context.Context, nk runtime.NakamaModule, rec crewPurgeRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      CrewPurgeCollection,
		Key:             rec.CrewID,
		UserID:          SystemUserID,
		Value:           string(data),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	return err
}

func deleteCrewPurgeRecord(ctx context.Context, nk runtime.NakamaModule, crewID string) error {
	return nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: CrewPurgeCollection, Key: crewID, UserID: SystemUserID}})
}

// ensureCrewPurgeRecord returns the purge record of a crew and makes it when
// there is none.
func ensureCrewPurgeRecord(ctx context.Context, nk runtime.NakamaModule, crewID string) (*crewPurgeRecord, error) {
	rec, err := readCrewPurgeRecord(ctx, nk, crewID)
	if err != nil || rec != nil {
		return rec, err
	}
	rec = &crewPurgeRecord{CrewID: crewID, StartedMs: crewPurgeNow().UnixMilli()}
	if err := writeCrewPurgeRecord(ctx, nk, *rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// purgedCrewIDs returns the ids of the crews with a purge record.
func purgedCrewIDs(ctx context.Context, nk runtime.NakamaModule) (map[string]bool, error) {
	out := map[string]bool{}
	cursor := ""
	for {
		objects, next, err := nk.StorageList(ctx, "", SystemUserID, CrewPurgeCollection, 100, cursor)
		if err != nil {
			return nil, err
		}
		for _, obj := range objects {
			if obj.GetKey() != "" {
				out[obj.GetKey()] = true
			}
		}
		if next == "" {
			return out, nil
		}
		cursor = next
	}
}

// crewGroupExists reports whether the crew is a group on this install.
func crewGroupExists(ctx context.Context, nk runtime.NakamaModule, crewID string) (bool, error) {
	groups, err := nk.GroupsGetId(ctx, []string{crewID})
	if err != nil {
		return false, err
	}
	for _, g := range groups {
		if g.GetId() == crewID {
			return true, nil
		}
	}
	return false, nil
}

// crewPurgeDoc is one document of a crew that the purge deletes.
type crewPurgeDoc struct {
	Collection string
	Key        string
	UserID     string
}

// crewPurgeDocs lists the system documents keyed by the crew id.
//
// Not in the list, and why:
//   - crew_last_seen, chat_read: owned by each member, key crew id. They hold
//     a time. Nakama deletes them with the account of the member.
//   - invite_codes: key is the code. Nothing lists the codes of a crew.
//   - chat_reactions, thumbnails, media_reports: keyed by a message, a stream
//     or a report.
//   - active_streams: owned by the streamer. purgeCrewDocs deletes it when
//     stream_meta names the streamer.
func crewPurgeDocs(crewID string) []crewPurgeDoc {
	sys := func(collection, key string) crewPurgeDoc {
		return crewPurgeDoc{Collection: collection, Key: key, UserID: SystemUserID}
	}
	return []crewPurgeDoc{
		// The legacy documents of the ledger.
		sys(CrewEventsCollection, crewID),
		sys(CrewClipsCollection, crewID),
		sys(CrewStreamSessionsCollection, crewID),
		sys(CrewRecapsCollection, crewID),
		// The other documents of the crew.
		sys(CrewQuestsCollection, crewID),
		sys(CrewProfileCollection, crewID),
		sys(CrewEmojiCollection, crewID),
		sys(CrewAvatarCollection, crewID),
		sys(VoiceChannelCollection, crewID),
		sys(CrewHead2headCollection, head2headKey(crewID)),
		sys(RecapPushCollection, crewID),
		sys(RecapPushCollection, beltPushKey+crewID),
		sys(StreamMetaCollection, crewID),
	}
}

// crewKnownHere reports whether this install holds anything of the crew: a
// purge record, a document, or a row of the store.
func crewKnownHere(ctx context.Context, nk runtime.NakamaModule, store ledgerStore, crewID string) (bool, error) {
	rec, err := readCrewPurgeRecord(ctx, nk, crewID)
	if err != nil {
		return false, err
	}
	if rec != nil {
		return true, nil
	}
	reads := make([]*runtime.StorageRead, 0, 16)
	for _, d := range crewPurgeDocs(crewID) {
		reads = append(reads, &runtime.StorageRead{Collection: d.Collection, Key: d.Key, UserID: d.UserID})
	}
	objects, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return false, err
	}
	if len(objects) > 0 {
		return true, nil
	}
	if store != nil {
		rows, err := store.NewestN(ctx, crewID, nil, math.MaxInt64, 1)
		if err != nil {
			return false, err
		}
		return len(rows) > 0, nil
	}
	return false, nil
}

// purgeCrewData runs the purge of a crew once: the media, the rows of the
// store, the documents. It needs the purge record of the crew, and it refuses
// while the group exists. It can run again at any time.
//
// Every part runs also when another part failed. The record is done when a
// run removed everything with no error.
func purgeCrewData(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, crewID string, deadline time.Time) crewPurgeResult {
	res := crewPurgeResult{CrewID: crewID}
	if !validPurgeCrewID(crewID) {
		res.fail("the crew id is not a group id")
		return res
	}
	exists, err := crewGroupExists(ctx, nk, crewID)
	if err != nil {
		res.fail("group read: %v", err)
		return res
	}
	rec, err := readCrewPurgeRecord(ctx, nk, crewID)
	if err != nil {
		res.fail("purge record read: %v", err)
		return res
	}
	if rec == nil {
		res.fail("the crew has no purge record")
		return res
	}
	if exists {
		// A record of a crew that is a group is wrong. Nothing of the crew is
		// deleted, and the record goes: while it is there the reconcile would
		// look at the crew as a purged one.
		res.fail("the crew still exists")
		if err := deleteCrewPurgeRecord(ctx, nk, crewID); err != nil {
			res.fail("purge record delete: %v", err)
		}
		logger.Error("crew_purge_refused crew=%s: the crew still exists. Its purge record is deleted", crewID)
		return res
	}

	purgeCrewMedia(ctx, logger, &res, crewID, deadline)

	// The store can be missing: the schema setup failed at start and is tried
	// again in the background. The purge then removes the rest and stays
	// pending, as the scrub of an account does (runUserScrub).
	if store := currentLedger(); store != nil {
		if err := store.DeleteCrew(ctx, crewID); err != nil {
			res.fail("store rows: %v", err)
		} else {
			res.StoreDeleted = true
		}
	} else {
		res.StorePending = true
	}
	purgeCrewDocs(ctx, nk, &res, crewID)
	InvalidateCrewState(crewID)

	rec.StorePending = res.StorePending && len(res.Errors) == 0 && !res.Incomplete
	if res.StorePending {
		logger.Warn("crew_purge_pending crew=%s: the durable store is not loaded. The store rows wait", crewID)
	}
	res.Done = len(res.Errors) == 0 && !res.Incomplete && !res.StorePending
	now := crewPurgeNow().UnixMilli()
	switch {
	case !res.Done:
		rec.LastError = strings.Join(res.Errors, "; ")
		if res.Incomplete && rec.LastError == "" {
			rec.LastError = "cut at the time budget"
		}
		if rec.StorePending {
			rec.LastError = "the durable store is not loaded"
		}
	case rec.DoneMs == 0:
		rec.DoneMs, rec.LastError = now, ""
	case rec.SettledMs == 0 && now >= rec.DoneMs+crewPurgeSettle.Milliseconds():
		rec.SettledMs, rec.LastError = now, ""
	default:
		rec.LastError = ""
	}
	if err := writeCrewPurgeRecord(ctx, nk, *rec); err != nil {
		res.fail("purge record write: %v", err)
		res.Done = false
	}
	switch {
	case res.Done:
		logger.Info("crew_purge crew=%s media=%d docs=%d store=%t", crewID, res.MediaDeleted, res.DocsDeleted, res.StoreDeleted)
	case rec.StorePending:
		// Logged above as pending. Nothing else failed.
	default:
		logger.Error("crew_purge_failed crew=%s media=%d docs=%d incomplete=%t: %s", crewID, res.MediaDeleted, res.DocsDeleted, res.Incomplete, strings.Join(res.Errors, "; "))
	}
	return res
}

// purgeCrewDocs deletes the documents of a crew, one delete for each, so one
// failure does not keep the others.
func purgeCrewDocs(ctx context.Context, nk runtime.NakamaModule, res *crewPurgeResult, crewID string) {
	docs := crewPurgeDocs(crewID)
	// The record of a live stream is owned by the streamer.
	var meta StreamMeta
	if found, _, err := readDocStrict(ctx, nk, StreamMetaCollection, crewID, &meta); err != nil {
		res.fail("%s read: %v", StreamMetaCollection, err)
	} else if found && meta.StreamerID != "" {
		docs = append(docs, crewPurgeDoc{Collection: StreamCollection, Key: crewID, UserID: meta.StreamerID})
	}
	for _, d := range docs {
		existing, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: d.Collection, Key: d.Key, UserID: d.UserID}})
		if err != nil {
			res.fail("%s read: %v", d.Collection, err)
			continue
		}
		if len(existing) == 0 {
			continue
		}
		if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: d.Collection, Key: d.Key, UserID: d.UserID}}); err != nil {
			res.fail("%s delete: %v", d.Collection, err)
			continue
		}
		res.DocsDeleted++
	}
}

// crewMediaPrefix is one bucket and the prefix of a crew in it.
type crewMediaPrefix struct {
	name   string
	client *s3.Client
	bucket string
	prefix string
}

// crewMediaPrefixes returns the prefixes that hold the media of a crew:
//
//	clips bucket      crews/{crew_id}/        voice clips, game clip videos and posters
//	snapshots bucket  snapshots/{crew_id}/    stream snapshots
//	media bucket      crew-banners/{crew_id}/ uploaded banners
//	media bucket      crew-emoji/{crew_id}/   crew emoji
//
// The chat images of a crew ({crew_id}/ in the media bucket) are not in the
// list: the chat messages are not part of the ledger.
func crewMediaPrefixes(crewID string) []crewMediaPrefix {
	clips, snapshots := mediaOrphanBuckets()
	gifts, giftsBucket := crewGiftsBucket()
	return []crewMediaPrefix{
		{name: "clips", client: clips.client, bucket: clips.bucket, prefix: mediaClipPrefix + crewID + "/"},
		{name: "snapshots", client: snapshots.client, bucket: snapshots.bucket, prefix: mediaSnapshotPrefix + crewID + "/"},
		{name: "banners", client: gifts, bucket: giftsBucket, prefix: bannerKeyPrefix + crewID + "/"},
		{name: "emoji", client: gifts, bucket: giftsBucket, prefix: emojiKeyPrefix + crewID + "/"},
	}
}

// purgeCrewMedia deletes every object under the prefixes of a crew. A delete
// cannot be undone, so each key is checked against its prefix before it goes.
// A bucket that is not configured is skipped and named in the result.
func purgeCrewMedia(ctx context.Context, logger runtime.Logger, res *crewPurgeResult, crewID string, deadline time.Time) {
	if !validPurgeCrewID(crewID) {
		res.fail("media: the crew id is not a group id")
		return
	}
	for _, p := range crewMediaPrefixes(crewID) {
		if p.client == nil || p.bucket == "" {
			res.MediaSkipped = append(res.MediaSkipped, p.name+": not configured")
			continue
		}
		n, incomplete, err := deleteObjectsUnder(ctx, p.client, p.bucket, p.prefix, crewID, deadline)
		res.MediaDeleted += n
		if err != nil {
			res.fail("media %s: %v", p.name, err)
		}
		if incomplete {
			res.Incomplete = true
			return
		}
	}
}

// deleteObjectsUnder deletes the objects of a bucket under a prefix, a batch
// of the list at a time, until the list is empty or the deadline passes. The
// prefix must end with "/{owner}/": a wrong prefix deletes nothing.
func deleteObjectsUnder(ctx context.Context, c *s3.Client, bucket, prefix, owner string, deadline time.Time) (deleted int, incomplete bool, err error) {
	if owner == "" || strings.ContainsAny(owner, "/.") || !strings.HasSuffix(prefix, "/"+owner+"/") {
		return 0, false, fmt.Errorf("refusing the prefix %q", prefix)
	}
	for {
		objects, _, err := listObjectsAfter(ctx, c, bucket, prefix, "", crewPurgeListBatch)
		if err != nil {
			return deleted, false, err
		}
		if len(objects) == 0 {
			return deleted, false, nil
		}
		for _, obj := range objects {
			if !strings.HasPrefix(obj.Key, prefix) {
				return deleted, false, fmt.Errorf("the list returned %q outside %q", obj.Key, prefix)
			}
			if crewPurgeNow().After(deadline) {
				return deleted, true, nil
			}
			if err := deleteObject(ctx, c, bucket, obj.Key); err != nil {
				return deleted, false, err
			}
			deleted++
		}
	}
}

// startCrewPurge records that a crew is gone and starts its purge. The delete
// of the crew calls it after the group is deleted. It does not fail the
// delete: a purge that fails stays in its record, and the deletion job and
// admin_purge_crew run it again.
func startCrewPurge(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, crewID string) {
	if !validPurgeCrewID(crewID) {
		logger.Error("crew_purge_failed crew=%s: the crew id is not a group id", crewID)
		return
	}
	// The record is the order to delete everything of the crew, also for the
	// reconcile. It is written only when the group is seen to be gone. A
	// group read that fails is not a group that is gone.
	exists, err := crewGroupExists(ctx, nk, crewID)
	if err != nil {
		logger.Error("crew_purge_failed crew=%s: group read: %v. Run admin_purge_crew", crewID, err)
		return
	}
	if exists {
		logger.Error("crew_purge_refused crew=%s: the crew still exists", crewID)
		return
	}
	if _, err := ensureCrewPurgeRecord(ctx, nk, crewID); err != nil {
		// Without the record nothing is deleted: the documents stay as the
		// proof for admin_purge_crew.
		logger.Error("crew_purge_failed crew=%s: purge record write: %v. Run admin_purge_crew", crewID, err)
		return
	}
	crewPurgeAsync(func() {
		// The context of the request ends with the request.
		bg, cancel := context.WithTimeout(context.Background(), crewPurgeBudget+30*time.Second)
		defer cancel()
		purgeCrewData(bg, nk, logger, crewID, crewPurgeNow().Add(crewPurgeBudget))
	})
}

// AfterDeleteCrew runs when a client deletes a crew through the group API of
// Nakama and not through delete_crew.
func AfterDeleteCrew(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.DeleteGroupRequest) error {
	crewID := in.GetGroupId()
	InvalidateCrewState(crewID)
	startCrewPurge(ctx, logger, nk, crewID)
	return nil
}

// runPendingCrewPurges runs the purges that are not done, and the second run
// of the purges that are (crewPurgeSettle). The deletion job calls it.
func runPendingCrewPurges(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, deadline time.Time) {
	cursor := ""
	for {
		objects, next, err := nk.StorageList(ctx, "", SystemUserID, CrewPurgeCollection, 100, cursor)
		if err != nil {
			logger.Error("crew_purge_failed: list of purge records: %v", err)
			return
		}
		for _, obj := range objects {
			var rec crewPurgeRecord
			if err := json.Unmarshal([]byte(obj.GetValue()), &rec); err != nil || obj.GetKey() == "" {
				continue
			}
			now := crewPurgeNow()
			if rec.SettledMs > 0 || (rec.DoneMs > 0 && now.UnixMilli() < rec.DoneMs+crewPurgeSettle.Milliseconds()) {
				continue
			}
			if rec.StorePending && currentLedger() == nil {
				// Nothing to do until the store is loaded.
				logger.Warn("crew_purge_pending crew=%s: the durable store is not loaded", obj.GetKey())
				continue
			}
			if now.After(deadline) {
				return
			}
			purgeCrewData(ctx, nk, logger, obj.GetKey(), deadline)
		}
		if next == "" {
			return
		}
		cursor = next
	}
}

// AdminPurgeCrewRPC runs the purge of a deleted crew again. Server-to-server
// (http_key). Request {crew_id}. It refuses while the group exists, and for a
// crew id this install holds nothing of. The answer is the result of the run:
// call again while done is false.
func AdminPurgeCrewRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if err := requireServerCaller(ctx, "admin_purge_crew"); err != nil {
		return "", err
	}
	var req struct {
		CrewID string `json:"crew_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request", 3)
	}
	if !validPurgeCrewID(req.CrewID) {
		return "", runtime.NewError("crew_id must be a group id", 3)
	}
	exists, err := crewGroupExists(ctx, nk, req.CrewID)
	if err != nil {
		return "", runtime.NewError("failed to read the crew", 13)
	}
	if exists {
		return "", runtime.NewError("the crew still exists: delete it first", 9)
	}
	known, err := crewKnownHere(ctx, nk, currentLedger(), req.CrewID)
	if err != nil {
		return "", runtime.NewError("failed to read the crew data", 13)
	}
	if !known {
		// The id can be the crew of another install that shares a bucket.
		return "", runtime.NewError("this install holds nothing of that crew", 5)
	}
	if _, err := ensureCrewPurgeRecord(ctx, nk, req.CrewID); err != nil {
		return "", runtime.NewError("failed to write the purge record", 13)
	}
	ctx, cancel := context.WithTimeout(ctx, crewPurgeBudget+30*time.Second)
	defer cancel()
	res := purgeCrewData(ctx, nk, logger, req.CrewID, crewPurgeNow().Add(crewPurgeBudget))
	out, err := json.Marshal(res)
	if err != nil {
		return "", runtime.NewError("encode failed", 13)
	}
	return string(out), nil
}
