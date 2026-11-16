package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The scrub of a deleted account (specs/16-CREW-EVENT-LEDGER.md section 10). The
// rules are the pure functions of ledger_scrub.go. This file applies them to
// the legacy documents and to the rows of the durable store, and deletes the
// media of the clips and streams of the user.
//
// Both sides are scrubbed in every phase, the documents first:
//
//   - Before the cutover the documents are the truth and the reconcile copies
//     them to the store. A document that still names the user would bring the
//     user back.
//   - After the cutover the documents are the rollback path (the kill
//     switches). The reconcile only inserts, and the store remembers the id
//     of each row the scrub drops (ledgerStore.Delete).
//
// A scrub has a record. It holds what a later run needs and the account no
// longer gives: the names of the user and the crews of the user. A run does a
// bounded part of the work and saves its place, so a scrub of a user with
// many crews continues in the next run (the deletion job, or the RPC
// admin_scrub_user).
//
// A scrub runs a second time one day after it was done (userScrubSettle): a
// voice session that ends after the delete still names the user. Then the
// record is deleted.

const (
	// UserScrubCollection holds one scrub record for each deleted account
	// whose scrub is not over, system-owned, key user id.
	UserScrubCollection = "ledger_user_scrub"

	// userScrubBudget bounds one run of a scrub.
	userScrubBudget = 2 * time.Minute
	// userScrubSettle is the wait before the second run.
	userScrubSettle = 24 * time.Hour
	// userScrubStale is the age at which a record of an account that still
	// exists is deleted: the account delete did not happen.
	userScrubStale = 1 * time.Hour
	// userScrubBatch is the number of store rows of one batch.
	userScrubBatch = 200
)

// scrubMedia is the media of one dropped clip or stream that is still to be
// deleted. Kind is the row type: clip (Ref is the clip_id) or stream_session
// (Ref is the session id).
type scrubMedia struct {
	Kind   string `json:"kind"`
	CrewID string `json:"crew_id"`
	Ref    string `json:"ref"`
}

// scrubSkipped is media of a dropped clip or stream that the scrub does not
// delete, and why. It stays in the record, so the result of every run names
// it: an operator must look at it.
type scrubSkipped struct {
	scrubMedia
	Reason string `json:"reason"`
}

const (
	scrubSkipUnsafeID  = "the id is not safe in an object key"
	scrubSkipNoSession = "the stream session has no session id, so no key can be made"
	scrubSkipHeld      = "the crew still holds a clip or a stream session with this id"
)

// userScrubRecord is the scrub record of a deleted account.
type userScrubRecord struct {
	UserID string `json:"user_id"`
	// Names are the display name and the username the account had at the
	// delete (BeforeDeleteAccount). They are matched in the fields that hold a
	// name and no id. A scrub with no before hook has none.
	Names []string `json:"names,omitempty"`
	// Crews are the crews that are still to be scrubbed.
	Crews []string `json:"crews,omitempty"`
	// SoleCrews are the crews the user was the only member of at the last
	// delete attempt (BeforeDeleteAccount). Nakama deletes such a group with
	// the account. The scrub purges a crew of this list when its group is
	// gone, and no other crew.
	SoleCrews []string `json:"sole_crews,omitempty"`
	// Discovered is true when the user index of the store and the scan of the
	// legacy documents have added their crews. ScanStep and ScanCursor are
	// the place of the scan.
	Discovered bool   `json:"discovered,omitempty"`
	ScanStep   int    `json:"scan_step,omitempty"`
	ScanCursor string `json:"scan_cursor,omitempty"`
	// Media is the media of dropped clips and streams that is not deleted yet.
	// An entry leaves the list when its delete is complete.
	Media []scrubMedia `json:"media,omitempty"`
	// Skipped is the media the scrub does not delete (scrubSkipped).
	Skipped   []scrubSkipped `json:"skipped,omitempty"`
	StartedMs int64          `json:"started_ms"`
	// DeletedMs is when the account was seen to be gone. 0: the record was
	// written before the delete, and the delete is not confirmed.
	DeletedMs int64 `json:"deleted_ms,omitempty"`
	// DoneMs is the end of the first full run. SecondRun is true when the
	// run after userScrubSettle has started.
	DoneMs    int64 `json:"done_ms,omitempty"`
	SecondRun bool  `json:"second_run,omitempty"`
	// StorePending is true when a run had no durable store: the documents
	// are scrubbed and the rows of the store are not. The scrub is not done.
	// The first run with a store looks for the crews again.
	StorePending bool   `json:"store_pending,omitempty"`
	LastError    string `json:"last_error,omitempty"`

	// version is the storage version this copy of the record was read or
	// written at. Every write and the delete carry it, so a run that works on
	// an old copy cannot overwrite the record of a newer run.
	version string
}

// idle reports whether the record has no work left.
func (r *userScrubRecord) idle() bool {
	return r.Discovered && len(r.Crews) == 0 && len(r.Media) == 0 && !r.StorePending
}

// waitsForStore reports whether the record has no work left but the rows of
// a store that is not loaded.
func (r *userScrubRecord) waitsForStore() bool {
	return r.StorePending && r.Discovered && len(r.Crews) == 0 && len(r.Media) == 0
}

// rediscover makes the next run look for the crews of the user again.
func (r *userScrubRecord) rediscover() {
	r.Discovered, r.ScanStep, r.ScanCursor = false, 0, ""
}

func (r *userScrubRecord) who() scrubWho {
	return scrubWho{UserID: r.UserID, Names: r.Names}
}

func (r *userScrubRecord) addNames(names ...string) {
	for _, n := range names {
		if n == "" {
			continue
		}
		known := false
		for _, have := range r.Names {
			if have == n {
				known = true
				break
			}
		}
		if !known {
			r.Names = append(r.Names, n)
		}
	}
}

func (r *userScrubRecord) addCrews(crews ...string) {
	for _, c := range crews {
		if c == "" {
			continue
		}
		known := false
		for _, have := range r.Crews {
			if have == c {
				known = true
				break
			}
		}
		if !known {
			r.Crews = append(r.Crews, c)
		}
	}
}

// soleMemberOf reports whether the user was the only member of the crew at
// the delete.
func (r *userScrubRecord) soleMemberOf(crewID string) bool {
	for _, c := range r.SoleCrews {
		if c == crewID {
			return true
		}
	}
	return false
}

func (r *userScrubRecord) addMedia(m scrubMedia) {
	if m.CrewID == "" {
		return
	}
	if m.Ref == "" {
		// A voice clip always has a clip_id. A stream session from before
		// sessions had an id has none: its snapshots, if any, have no key
		// that can be made from the row.
		if m.Kind == ledgerTypeStreamSession {
			r.addSkipped(m, scrubSkipNoSession)
		}
		return
	}
	for _, have := range r.Media {
		if have == m {
			return
		}
	}
	r.Media = append(r.Media, m)
}

func (r *userScrubRecord) addSkipped(m scrubMedia, reason string) {
	for _, have := range r.Skipped {
		if have.scrubMedia == m {
			return
		}
	}
	r.Skipped = append(r.Skipped, scrubSkipped{scrubMedia: m, Reason: reason})
}

// userScrubResult is what one run of a scrub did.
type userScrubResult struct {
	UserID string `json:"user_id"`
	// Done is true when the run left nothing to do.
	Done bool `json:"done"`
	// Incomplete is true when the run was cut at its time budget.
	Incomplete bool `json:"incomplete"`
	// Busy is true when another scrub of the user runs in this process. The
	// run did nothing.
	Busy bool `json:"busy,omitempty"`
	// StorePending is true when the durable store is not loaded: the rows of
	// the store are not scrubbed, and the scrub is not done.
	StorePending  bool `json:"store_pending,omitempty"`
	CrewsScrubbed int  `json:"crews_scrubbed"`
	// CrewsPurged is the number of crews the run started a purge of: the
	// user was their only member, and their group went with the account.
	CrewsPurged  int `json:"crews_purged,omitempty"`
	CrewsLeft    int `json:"crews_left"`
	Dropped      int `json:"dropped"`
	Changed      int `json:"changed"`
	MediaDeleted int `json:"media_deleted"`
	// MediaPending is the number of clips and streams whose media is still
	// to be deleted. MediaSkipped names the media the scrub will not delete.
	MediaPending int      `json:"media_pending"`
	MediaSkipped []string `json:"media_skipped,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

func (r *userScrubResult) fail(format string, args ...interface{}) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

// userScrubNow is the clock of the scrub. Tests change it.
var userScrubNow = time.Now

// userScrubAsync runs the scrub that an account delete starts. A var so a
// test runs it inline.
var userScrubAsync = func(fn func()) { go fn() }

// errScrubRecordLost is the error of a write or a delete of a scrub record
// that the storage refused. The copy of the record is then not the current
// one (another run wrote it, on this node or on another), or the storage
// failed. In both cases the run that holds the copy must stop: the record in
// storage is the truth, and the deletion job continues from it.
var errScrubRecordLost = errors.New("the scrub record was not written")

func readUserScrubRecord(ctx context.Context, nk runtime.NakamaModule, userID string) (*userScrubRecord, error) {
	var rec userScrubRecord
	found, version, err := readDocStrict(ctx, nk, UserScrubCollection, userID, &rec)
	if err != nil || !found {
		return nil, err
	}
	rec.UserID = userID
	rec.version = version
	return &rec, nil
}

// writeUserScrubRecord writes the record when storage holds the version of
// this copy. A new record is written only when storage holds none.
func writeUserScrubRecord(ctx context.Context, nk runtime.NakamaModule, rec *userScrubRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	version := rec.version
	if version == "" {
		version = "*"
	}
	acks, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      UserScrubCollection,
		Key:             rec.UserID,
		UserID:          SystemUserID,
		Value:           string(data),
		Version:         version,
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	if err != nil {
		return fmt.Errorf("%w: %v", errScrubRecordLost, err)
	}
	if len(acks) == 0 || acks[0].GetVersion() == "" {
		return fmt.Errorf("%w: the write returned no version", errScrubRecordLost)
	}
	rec.version = acks[0].GetVersion()
	return nil
}

// deleteUserScrubRecord deletes the record when storage holds the version of
// this copy.
func deleteUserScrubRecord(ctx context.Context, nk runtime.NakamaModule, rec *userScrubRecord) error {
	if rec.version == "" {
		return fmt.Errorf("%w: the record was never read", errScrubRecordLost)
	}
	err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: UserScrubCollection, Key: rec.UserID, UserID: SystemUserID, Version: rec.version}})
	if err != nil {
		return fmt.Errorf("%w: %v", errScrubRecordLost, err)
	}
	return nil
}

// updateUserScrubRecord reads the record, changes it with fn and writes it.
// When another writer was first it reads again. A record that does not exist
// is made. It returns the record as written.
func updateUserScrubRecord(ctx context.Context, nk runtime.NakamaModule, userID string, fn func(rec *userScrubRecord)) (*userScrubRecord, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		rec, err := readUserScrubRecord(ctx, nk, userID)
		if err != nil {
			return nil, err
		}
		before := []byte(nil)
		if rec == nil {
			rec = &userScrubRecord{UserID: userID, StartedMs: userScrubNow().UnixMilli()}
		} else {
			before, _ = json.Marshal(rec)
		}
		fn(rec)
		// A record that fn did not change is not written: a write would take
		// the record from the run that works on it.
		if after, _ := json.Marshal(rec); before != nil && string(after) == string(before) {
			return rec, nil
		}
		if lastErr = writeUserScrubRecord(ctx, nk, rec); lastErr == nil {
			return rec, nil
		}
	}
	return nil, lastErr
}

// userScrubRunning holds the user ids whose scrub runs in this process. One
// scrub of a user runs at a time: the hook, the job and the RPC can all
// start one. Two nodes are kept apart by the version of the record.
var userScrubRunning sync.Map

// accountExists reports whether the user is an account on this install.
func accountExists(ctx context.Context, nk runtime.NakamaModule, userID string) (bool, error) {
	users, err := nk.UsersGetId(ctx, []string{userID}, nil)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.GetId() == userID {
			return true, nil
		}
	}
	return false, nil
}

// BeforeDeleteAccount runs before Nakama deletes an account
// (DELETE /v2/account). After the delete the names and the crews of the user
// cannot be read, so this hook writes them to the scrub record. A crew whose
// only owner is the user gets a new owner here (crew_handover.go): Nakama
// deletes a group whose last superadmin deletes the account. The hook deletes
// nothing: the delete can still fail. It never fails the delete.
func BeforeDeleteAccount(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return nil
	}
	// The account is read once. The record is then written with a version
	// check, and read again when the deletion job wrote it in between.
	var names, crews, owned []string
	if users, err := nk.UsersGetId(ctx, []string{userID}, nil); err != nil {
		logger.Error("user_scrub_failed user=%s: account read: %v", userID, err)
	} else {
		for _, u := range users {
			names = append(names, u.GetDisplayName(), u.GetUsername())
		}
	}
	cursor := ""
	for {
		groups, next, err := nk.UserGroupsList(ctx, userID, 100, nil, cursor)
		if err != nil {
			logger.Error("user_scrub_failed user=%s: crews read: %v", userID, err)
			break
		}
		for _, g := range groups {
			crews = append(crews, g.GetGroup().GetId())
			if g.GetState().GetValue() == groupStateSuperadmin {
				owned = append(owned, g.GetGroup().GetId())
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	soleCrews := handOverOwnedCrews(ctx, nk, logger, userID, owned)
	_, err := updateUserScrubRecord(ctx, nk, userID, func(rec *userScrubRecord) {
		rec.addNames(names...)
		rec.addCrews(crews...)
		// The list of this attempt replaces the list of an earlier one: a
		// crew can have a member now that it did not have then.
		rec.SoleCrews = soleCrews
		// Each delete attempt starts the wait for the delete again. The job
		// deletes a record of an account that still exists after
		// userScrubStale: a record of an earlier attempt must not be cleaned
		// between the two hooks of this one.
		if rec.DeletedMs == 0 {
			rec.StartedMs = userScrubNow().UnixMilli()
		}
	})
	if err != nil {
		logger.Error("user_scrub_failed user=%s: scrub record write: %v. Run admin_scrub_user after the delete", userID, err)
	}
	return nil
}

// AfterDeleteAccount runs after Nakama deleted an account. It starts the
// scrub and does not wait for it. A scrub that fails stays in its record:
// the deletion job and admin_scrub_user run it again.
func AfterDeleteAccount(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	userID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if userID == "" {
		return nil
	}
	rec, err := confirmUserScrub(ctx, nk, userID)
	if err != nil {
		logger.Error("user_scrub_failed user=%s: scrub record: %v. Run admin_scrub_user", userID, err)
		return nil
	}
	userScrubAsync(func() {
		// The context of the request ends with the request.
		bg, cancel := context.WithTimeout(context.Background(), userScrubBudget+30*time.Second)
		defer cancel()
		runUserScrub(bg, nk, logger, rec, userScrubNow().Add(userScrubBudget))
	})
	return nil
}

// confirmUserScrub returns the scrub record of an account that is gone, with
// the time it was seen to be gone. It makes the record when there is none:
// the names and the crews then come only from the data.
func confirmUserScrub(ctx context.Context, nk runtime.NakamaModule, userID string) (*userScrubRecord, error) {
	return updateUserScrubRecord(ctx, nk, userID, func(rec *userScrubRecord) {
		if rec.DeletedMs == 0 {
			rec.DeletedMs = userScrubNow().UnixMilli()
		}
	})
}

// scrubScanCollections are the documents the scrub scans for the id of the
// user, in order: the legacy documents of the ledger, and the head2head
// holders documents.
var scrubScanCollections = []string{CrewEventsCollection, CrewClipsCollection, CrewStreamSessionsCollection, CrewRecapsCollection, CrewHead2headCollection}

// scrubScanCrewID is the crew id of a scanned document, from its key.
func scrubScanCrewID(collection, key string) string {
	if collection == CrewHead2headCollection {
		crewID, ok := strings.CutPrefix(key, head2headKeyPrefix)
		if !ok {
			return ""
		}
		return crewID
	}
	return key
}

// discoverUserCrews adds to the record the crews that hold the user: the
// crews of the user index of the store, and the crews whose legacy documents
// hold the id of the user. The crews the user was a member of at the delete
// are in the record already. It returns false when the deadline cut the scan.
func discoverUserCrews(ctx context.Context, nk runtime.NakamaModule, rec *userScrubRecord, res *userScrubResult, deadline time.Time) (bool, error) {
	if store := currentLedger(); store != nil && rec.ScanStep == 0 && rec.ScanCursor == "" {
		crews, err := store.UserCrews(ctx, rec.UserID)
		if err != nil {
			res.fail("user index: %v", err)
			return false, nil
		}
		rec.addCrews(crews...)
	}
	for rec.ScanStep < len(scrubScanCollections) {
		if userScrubNow().After(deadline) {
			res.Incomplete = true
			return false, nil
		}
		collection := scrubScanCollections[rec.ScanStep]
		objects, next, err := nk.StorageList(ctx, "", SystemUserID, collection, 100, rec.ScanCursor)
		if err != nil {
			res.fail("scan of %s: %v", collection, err)
			return false, nil
		}
		for _, obj := range objects {
			// The id of the user anywhere in the document, in any field.
			if strings.Contains(obj.GetValue(), rec.UserID) {
				rec.addCrews(scrubScanCrewID(collection, obj.GetKey()))
			}
		}
		rec.ScanCursor = next
		if next == "" {
			rec.ScanStep++
		}
		if err := writeUserScrubRecord(ctx, nk, rec); err != nil {
			return false, err
		}
	}
	rec.Discovered = true
	return true, nil
}

// runUserScrub runs the scrub of a deleted account once, up to the deadline.
// It can run again at any time: every step leaves the data without the user
// or as it was.
func runUserScrub(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, rec *userScrubRecord, deadline time.Time) userScrubResult {
	res := userScrubResult{UserID: rec.UserID}
	if _, busy := userScrubRunning.LoadOrStore(rec.UserID, true); busy {
		res.Busy = true
		res.fail("a scrub of this user runs in this process")
		return res
	}
	defer userScrubRunning.Delete(rec.UserID)

	// The store can be missing: the schema setup failed at start and is tried
	// again in the background (startLedgerStore). A run then scrubs the
	// documents and stays pending. Nothing tells an install whose schema can
	// never be made from one that waits for its database, so both stay
	// pending and are logged.
	noStore := currentLedger() == nil
	if !noStore && rec.StorePending {
		rec.StorePending = false
		rec.rediscover()
	}

	// lost is true when a write of the record was refused. This run then
	// stops and writes nothing more: its copy of the record is old.
	lost := false
	save := func() bool {
		if err := writeUserScrubRecord(ctx, nk, rec); err != nil {
			res.fail("%v. This run stops", err)
			lost = true
		}
		return !lost
	}
	finish := func() userScrubResult {
		res.CrewsLeft = len(rec.Crews)
		res.MediaPending = len(rec.Media)
		for _, sk := range rec.Skipped {
			res.MediaSkipped = append(res.MediaSkipped, sk.Kind+" "+sk.CrewID+"/"+sk.Ref+": "+sk.Reason)
		}
		line := func() string {
			return fmt.Sprintf("user=%s crews=%d crews_left=%d dropped=%d changed=%d media=%d media_pending=%d media_skipped=%d incomplete=%t",
				rec.UserID, res.CrewsScrubbed, res.CrewsLeft, res.Dropped, res.Changed, res.MediaDeleted, res.MediaPending, len(rec.Skipped), res.Incomplete)
		}
		if lost {
			logger.Error("user_scrub_failed %s: %s", line(), strings.Join(res.Errors, "; "))
			return res
		}
		if noStore {
			rec.StorePending = true
			res.StorePending = true
			logger.Warn("user_scrub_pending user=%s: the durable store is not loaded. The documents are scrubbed, the store rows wait", rec.UserID)
		}
		res.Done = len(res.Errors) == 0 && !res.Incomplete && rec.idle()
		now := userScrubNow().UnixMilli()
		rec.LastError = strings.Join(res.Errors, "; ")
		settled := false
		if res.Done {
			switch {
			case rec.DoneMs == 0:
				rec.DoneMs = now
			case rec.SecondRun:
				settled = true
			}
		}
		if rec.DoneMs > 0 && rec.idle() {
			// The record waits for its second run. That run finds the user by
			// id (the user index and the scan), so the names are not kept.
			rec.Names = nil
		}
		var err error
		if settled {
			// The second run is over. The record goes.
			err = deleteUserScrubRecord(ctx, nk, rec)
		} else {
			err = writeUserScrubRecord(ctx, nk, rec)
		}
		if err != nil {
			res.fail("%v", err)
			res.Done = false
		}
		if len(res.Errors) > 0 {
			logger.Error("user_scrub_failed %s: %s", line(), strings.Join(res.Errors, "; "))
		} else {
			logger.Info("user_scrub %s", line())
		}
		if len(res.MediaSkipped) > 0 {
			// The record is deleted in the end. The log keeps what was skipped.
			logger.Error("user_scrub_media_skipped user=%s: %s", rec.UserID, strings.Join(res.MediaSkipped, "; "))
		}
		return res
	}

	if !rec.Discovered {
		ok, err := discoverUserCrews(ctx, nk, rec, &res, deadline)
		if errors.Is(err, errScrubRecordLost) {
			res.fail("%v. This run stops", err)
			lost = true
		}
		if !ok {
			return finish()
		}
	}
	if !save() {
		return finish()
	}
	for len(rec.Crews) > 0 {
		if userScrubNow().After(deadline) {
			res.Incomplete = true
			return finish()
		}
		crewID := rec.Crews[0]
		done, err := scrubCrewForUser(ctx, nk, logger, rec, &res, crewID, deadline)
		if errors.Is(err, errScrubRecordLost) {
			res.fail("crew %s: %v. This run stops", crewID, err)
			lost = true
			return finish()
		}
		if err != nil {
			// The crew stays in the record. The other crews still run.
			res.fail("crew %s: %v", crewID, err)
			rec.Crews = append(rec.Crews[1:], crewID)
			if len(res.Errors) >= len(rec.Crews) {
				return finish()
			}
			continue
		}
		if !done {
			res.Incomplete = true
			return finish()
		}
		rec.Crews = rec.Crews[1:]
		res.CrewsScrubbed++
		if !save() {
			return finish()
		}
	}
	deleteScrubMedia(ctx, nk, rec, &res, deadline)
	return finish()
}

// scrubDoc reads one system document, gives its value to fn, and writes the
// result when fn changed it. It writes with the version it read and retries
// when another writer was first.
func scrubDoc(ctx context.Context, nk runtime.NakamaModule, collection, key string, fn func(value string) (out string, changed bool, err error)) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{Collection: collection, Key: key, UserID: SystemUserID}})
		if err != nil {
			return err
		}
		if len(objects) == 0 {
			return nil
		}
		out, changed, err := fn(objects[0].GetValue())
		if err != nil || !changed {
			return err
		}
		_, lastErr = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      collection,
			Key:             key,
			UserID:          SystemUserID,
			Value:           out,
			Version:         objects[0].GetVersion(),
			PermissionRead:  int(objects[0].GetPermissionRead()),
			PermissionWrite: int(objects[0].GetPermissionWrite()),
		}})
		if lastErr == nil {
			return nil
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("%s write failed after 3 tries: %w", collection, lastErr)
}

// crewScrub is the scrub of one crew for one user.
type crewScrub struct {
	ctx    context.Context
	nk     runtime.NakamaModule
	rec    *userScrubRecord
	res    *userScrubResult
	crewID string
	// clipIDs are the clip_ids of the clips the scrub dropped in this run.
	clipIDs map[string]bool
	// taken are the names of the members the crew has now (scrubWho.Taken).
	taken []string
}

func (c *crewScrub) who() scrubWho {
	who := c.rec.who()
	who.Taken = c.taken
	// The clips an earlier run of this scrub dropped are in the record until
	// their media is deleted.
	ids := make(map[string]bool, len(c.clipIDs))
	for id := range c.clipIDs {
		ids[id] = true
	}
	for _, m := range c.rec.Media {
		if m.Kind == ledgerTypeClip && m.CrewID == c.crewID {
			ids[m.Ref] = true
		}
	}
	for _, sk := range c.rec.Skipped {
		if sk.Kind == ledgerTypeClip && sk.CrewID == c.crewID {
			ids[sk.Ref] = true
		}
	}
	who.ClipIDs = ids
	return who
}

// crewMemberNames returns the display names and the usernames of the members
// a crew has now.
func crewMemberNames(ctx context.Context, nk runtime.NakamaModule, crewID string) ([]string, error) {
	var names []string
	cursor := ""
	for {
		members, next, err := nk.GroupUsersList(ctx, crewID, 100, nil, cursor)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m.GetState().GetValue() > 2 {
				continue
			}
			for _, n := range []string{m.GetUser().GetDisplayName(), m.GetUser().GetUsername()} {
				if n != "" {
					names = append(names, n)
				}
			}
		}
		if next == "" {
			return names, nil
		}
		cursor = next
	}
}

// dropClip notes a dropped clip: its media goes, and a recap must not show it.
func (c *crewScrub) dropClip(clipID string) {
	if clipID == "" {
		return
	}
	c.clipIDs[clipID] = true
	c.rec.addMedia(scrubMedia{Kind: ledgerTypeClip, CrewID: c.crewID, Ref: clipID})
}

func (c *crewScrub) dropStream(sessionID string) {
	c.rec.addMedia(scrubMedia{Kind: ledgerTypeStreamSession, CrewID: c.crewID, Ref: sessionID})
}

// dropEvent notes the media of a dropped event of the legacy ledger path.
func (c *crewScrub) dropEvent(ev CrewEvent) {
	switch typ, _ := ledgerReaderType(ev.Type); typ {
	case ledgerTypeStreamSession:
		c.dropStream(eventRef(CrewEvent{Type: ledgerTypeStreamSession, Data: ev.Data}))
	case ledgerTypeClip:
		if cd, ok := decodeCardData[ClipData](ev.Data); ok {
			c.dropClip(cd.ClipID)
		}
	}
}

// saveRecord writes the media the scrub found so far. It runs before the
// data that named the media is changed.
func (c *crewScrub) saveRecord() error {
	return writeUserScrubRecord(c.ctx, c.nk, c.rec)
}

// scrubCrewForUser scrubs one crew: the legacy documents, the rows of the
// store, then the recaps. done is false when the deadline cut it.
func scrubCrewForUser(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, rec *userScrubRecord, res *userScrubResult, crewID string, deadline time.Time) (done bool, err error) {
	// A group read that fails is never a group that is gone.
	exists, err := crewGroupExists(ctx, nk, crewID)
	if err != nil {
		return false, fmt.Errorf("group read: %w", err)
	}
	if !exists && rec.DeletedMs > 0 && rec.soleMemberOf(crewID) && validPurgeCrewID(crewID) {
		// The user was the only member, and Nakama deleted the group with
		// the account. No delete-group hook ran, so the purge starts here.
		// The purge record is the order: with the record written, the
		// deletion job and admin_purge_crew end a purge that fails here.
		if _, err := ensureCrewPurgeRecord(ctx, nk, crewID); err != nil {
			return false, fmt.Errorf("purge record write: %w", err)
		}
		logger.Info("user_scrub user=%s crew=%s: the user was the only member and the group is gone. The crew is purged", rec.UserID, crewID)
		purgeCrewData(ctx, nk, logger, crewID, deadline)
		res.CrewsPurged++
		return true, nil
	}
	if !exists {
		// Any other crew with no group is scrubbed as a crew with a group,
		// and never purged: nothing says here that no member is left.
		logger.Warn("user_scrub user=%s crew=%s: the crew is not a group. Its data is scrubbed, not purged. To remove it, run admin_purge_crew", rec.UserID, crewID)
	}

	if exists && rec.DeletedMs > 0 {
		// Nakama keeps the membership rows of a deleted superadmin or admin
		// (crew_handover.go).
		n, err := dropGoneMember(ctx, crewID, rec.UserID)
		if err != nil {
			return false, fmt.Errorf("membership rows: %w", err)
		}
		if n > 0 {
			logger.Info("user_scrub user=%s crew=%s: removed %d membership rows that Nakama left", rec.UserID, crewID, n)
		}
	}

	c := &crewScrub{ctx: ctx, nk: nk, rec: rec, res: res, crewID: crewID, clipIDs: map[string]bool{}}
	if exists {
		// A name of the user that another member has now is not matched in a
		// field with no id. The log names no one.
		if c.taken, err = crewMemberNames(ctx, nk, crewID); err != nil {
			return false, fmt.Errorf("member read: %w", err)
		}
		for _, n := range rec.Names {
			if c.who().taken(n) {
				logger.Warn("user_scrub user=%s crew=%s: another member has a name of the user. Fields with only that name stay", rec.UserID, crewID)
				break
			}
		}
	}
	if err := c.documents(); err != nil {
		return false, err
	}
	if store := currentLedger(); store != nil {
		if done, err := c.rows(store, deadline); err != nil || !done {
			return false, err
		}
	}
	if err := c.recaps(currentLedger()); err != nil {
		return false, err
	}
	if err := c.head2head(); err != nil {
		return false, err
	}
	InvalidateCrewState(crewID)
	return true, nil
}

// documents scrubs the ledger document, the clips document and the document
// of the stream sessions.
func (c *crewScrub) documents() error {
	userID := c.rec.UserID

	// The media of what goes. It is saved before a document changes.
	err := scrubDoc(c.ctx, c.nk, CrewEventsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewEventLedger
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		for _, ev := range doc.Events {
			if _, drop := scrubEventFor(ev, c.who()); drop {
				c.dropEvent(ev)
			}
		}
		return "", false, nil
	})
	if err != nil {
		return fmt.Errorf("%s read: %w", CrewEventsCollection, err)
	}
	err = scrubDoc(c.ctx, c.nk, CrewClipsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewClipsDoc
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		for _, clip := range doc.Clips {
			if clip.ActorID == userID {
				c.dropClip(clip.ClipID)
			}
		}
		return "", false, nil
	})
	if err != nil {
		return fmt.Errorf("%s read: %w", CrewClipsCollection, err)
	}
	err = scrubDoc(c.ctx, c.nk, CrewStreamSessionsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewStreamSessionsDoc
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		for _, s := range doc.Sessions {
			if s.StreamerID == userID {
				c.dropStream(s.SessionID)
			}
		}
		return "", false, nil
	})
	if err != nil {
		return fmt.Errorf("%s read: %w", CrewStreamSessionsCollection, err)
	}
	if err := c.saveRecord(); err != nil {
		return err
	}

	err = scrubDoc(c.ctx, c.nk, CrewEventsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewEventLedger
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		kept := make([]CrewEvent, 0, len(doc.Events))
		dropped, changed := 0, 0
		for _, ev := range doc.Events {
			out, drop := scrubEventFor(ev, c.who())
			switch {
			case drop:
				dropped++
				continue
			case !ledgerSameData(ev.Data, out.Data):
				changed++
			}
			kept = append(kept, out)
		}
		if dropped+changed == 0 {
			return "", false, nil
		}
		c.res.Dropped += dropped
		c.res.Changed += changed
		doc.Events = kept
		doc.UpdatedAt = time.Now().UnixMilli()
		out, err := json.Marshal(doc)
		return string(out), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewEventsCollection, err)
	}
	err = scrubDoc(c.ctx, c.nk, CrewClipsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewClipsDoc
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		kept := make([]StoredClip, 0, len(doc.Clips))
		dropped, changed := 0, 0
		for _, clip := range doc.Clips {
			out, drop := scrubClipForUser(clip, c.who())
			switch {
			case drop:
				dropped++
				continue
			case !historySameJSON(clip, out):
				changed++
			}
			kept = append(kept, out)
		}
		if dropped+changed == 0 {
			return "", false, nil
		}
		c.res.Dropped += dropped
		c.res.Changed += changed
		doc.Clips = kept
		doc.UpdatedAt = time.Now().UnixMilli()
		out, err := json.Marshal(doc)
		return string(out), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewClipsCollection, err)
	}
	err = scrubDoc(c.ctx, c.nk, CrewStreamSessionsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewStreamSessionsDoc
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		kept := make([]StoredStreamSession, 0, len(doc.Sessions))
		for _, s := range doc.Sessions {
			if _, drop := scrubStreamSessionForUser(s, c.who()); !drop {
				kept = append(kept, s)
			}
		}
		if len(kept) == len(doc.Sessions) {
			return "", false, nil
		}
		c.res.Dropped += len(doc.Sessions) - len(kept)
		doc.Sessions = kept
		doc.UpdatedAt = time.Now().UnixMilli()
		out, err := json.Marshal(doc)
		return string(out), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewStreamSessionsCollection, err)
	}
	return nil
}

// scrubRow applies the scrub rules to one row of the store. data is the new
// data of a row that stays and changed, nil when the row is unchanged.
func (c *crewScrub) scrubRow(ev CrewEvent) (drop bool, data json.RawMessage, err error) {
	who := c.who()
	switch ev.Type {
	case ledgerTypeClip:
		clip, ok := clipFromRow(ev)
		if !ok {
			return false, nil, fmt.Errorf("row %s: not a clip", ev.ID)
		}
		out, drop := scrubClipForUser(clip, who)
		if drop {
			c.dropClip(clip.ClipID)
			return true, nil, nil
		}
		if historySameJSON(clip, out) {
			return false, nil, nil
		}
		data, err = json.Marshal(out)
		return false, data, err
	case ledgerTypeWeeklyRecap:
		recap, ok := recapFromRow(ev)
		if !ok {
			return false, nil, fmt.Errorf("row %s: not a recap", ev.ID)
		}
		out, changed := scrubRecapForUser(recap, who)
		if !changed {
			return false, nil, nil
		}
		data, err = json.Marshal(out)
		return false, data, err
	}
	out, drop := scrubEventFor(ev, who)
	if drop {
		c.dropEvent(ev)
		return true, nil, nil
	}
	if ledgerSameData(ev.Data, out.Data) {
		return false, nil, nil
	}
	data, err = ledgerEncodeData(out.Data)
	return false, data, err
}

// rows scrubs the rows of the store that name the user, a batch at a time.
// A row that was handled no longer names the user: it is deleted, or the
// user leaves its data and the user index.
func (c *crewScrub) rows(store ledgerStore, deadline time.Time) (done bool, err error) {
	userID := c.rec.UserID
	for {
		if userScrubNow().After(deadline) {
			return false, nil
		}
		batch, err := store.NamingUserInCrew(c.ctx, userID, c.crewID, userScrubBatch)
		if err != nil {
			return false, fmt.Errorf("store read: %w", err)
		}
		if len(batch) == 0 {
			return true, nil
		}
		type plan struct {
			id   string
			drop bool
			data json.RawMessage
		}
		plans := make([]plan, 0, len(batch))
		for _, ev := range batch {
			drop, data, err := c.scrubRow(ev)
			if err != nil {
				return false, err
			}
			plans = append(plans, plan{id: ev.ID, drop: drop, data: data})
		}
		if err := c.saveRecord(); err != nil {
			return false, err
		}
		kept := make([]string, 0, len(plans))
		for _, p := range plans {
			if p.drop {
				// Delete remembers the id: no reconcile stores it again.
				if err := store.Delete(c.ctx, c.crewID, p.id); err != nil {
					return false, fmt.Errorf("store delete: %w", err)
				}
				c.res.Dropped++
				continue
			}
			if p.data != nil {
				data := p.data
				err := store.UpdateData(c.ctx, c.crewID, p.id, func(json.RawMessage) (json.RawMessage, error) { return data, nil })
				if err != nil && !errors.Is(err, errLedgerEventNotFound) {
					return false, fmt.Errorf("store update: %w", err)
				}
				c.res.Changed++
			}
			kept = append(kept, p.id)
		}
		if len(kept) > 0 {
			if err := store.ForgetUser(c.ctx, userID, c.crewID, kept); err != nil {
				return false, fmt.Errorf("store user index: %w", err)
			}
		}
	}
}

// recaps scrubs every stored recap of the crew: the document and the rows.
// It runs after the events, with every name they gave. A recap can name the
// user in a field with no id, so every recap of the crew is read.
func (c *crewScrub) recaps(store ledgerStore) error {
	err := scrubDoc(c.ctx, c.nk, CrewRecapsCollection, c.crewID, func(value string) (string, bool, error) {
		var doc CrewRecapsDoc
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		changed := 0
		for i, r := range doc.Recaps {
			if out, did := scrubRecapForUser(r, c.who()); did {
				doc.Recaps[i] = out
				changed++
			}
		}
		if changed == 0 {
			return "", false, nil
		}
		c.res.Changed += changed
		doc.UpdatedAt = time.Now().UnixMilli()
		out, err := json.Marshal(doc)
		return string(out), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewRecapsCollection, err)
	}
	if store == nil {
		return nil
	}
	update := func(ev CrewEvent) error {
		_, data, err := c.scrubRow(ev)
		if err != nil || data == nil {
			return err
		}
		err = store.UpdateData(c.ctx, c.crewID, ev.ID, func(json.RawMessage) (json.RawMessage, error) { return data, nil })
		if err != nil && !errors.Is(err, errLedgerEventNotFound) {
			return fmt.Errorf("store update: %w", err)
		}
		c.res.Changed++
		return nil
	}
	for beforeRef := ""; ; {
		rows, err := store.RefsBefore(c.ctx, c.crewID, ledgerTypeWeeklyRecap, beforeRef, userScrubBatch)
		if err != nil {
			return fmt.Errorf("store read: %w", err)
		}
		for _, ev := range rows {
			if err := update(ev); err != nil {
				return err
			}
		}
		if len(rows) < userScrubBatch {
			break
		}
		beforeRef = recapWeekRef(func() int64 { r, _ := recapFromRow(rows[len(rows)-1]); return r.WeekStart }())
		if beforeRef == "" {
			break
		}
	}
	// The recap events that an old ledger document held.
	legacy, err := store.Between(c.ctx, c.crewID, 0, math.MaxInt64, []string{ledgerStoredType(ledgerTypeWeeklyRecap)})
	if err != nil {
		return fmt.Errorf("store read: %w", err)
	}
	for _, ev := range legacy {
		out, drop := scrubEventFor(ev, c.who())
		if drop || ledgerSameData(ev.Data, out.Data) {
			continue
		}
		data, err := ledgerEncodeData(out.Data)
		if err != nil {
			return err
		}
		err = store.UpdateData(c.ctx, c.crewID, ev.ID, func(json.RawMessage) (json.RawMessage, error) { return data, nil })
		if err != nil && !errors.Is(err, errLedgerEventNotFound) {
			return fmt.Errorf("store update: %w", err)
		}
		c.res.Changed++
	}
	return nil
}

// head2head scrubs the holders document of the crew. The weekly recap makes
// its belt sections from this document, so without this a recap written
// after the scrub names the user again (scrubHead2headForUser).
func (c *crewScrub) head2head() error {
	err := scrubDoc(c.ctx, c.nk, CrewHead2headCollection, head2headKey(c.crewID), func(value string) (string, bool, error) {
		var doc Head2headDoc
		if err := json.Unmarshal([]byte(value), &doc); err != nil {
			return "", false, err
		}
		out, changed := scrubHead2headForUser(doc, c.rec.UserID)
		if !changed {
			return "", false, nil
		}
		c.res.Changed++
		out.UpdatedAt = time.Now().UnixMilli()
		data, err := json.Marshal(out)
		return string(data), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewHead2headCollection, err)
	}
	return nil
}

// deleteScrubMedia deletes the media of the dropped clips and streams of the
// user, up to the deadline of the run. A delete cannot be undone, so:
//
//   - a key is made only from a crew id, a clip_id or a session id that is
//     safe in a key. Anything else is not deleted. It moves to the skipped
//     list of the record, with the reason;
//   - nothing is deleted while the crew still holds a clip with the clip_id
//     or a stream session with the session id (a document from before
//     clip_id was checked can hold one clip_id for two actors). Such an entry
//     moves to the skipped list too;
//   - a clip is two exact keys (gameClipKeys). A stream is the prefix of its
//     session.
//
// An entry leaves the record only when its delete is complete. It stays when
// a delete failed, when the list of a prefix was cut at the deadline, and
// when its bucket is not configured: the scrub is then not done.
func deleteScrubMedia(ctx context.Context, nk runtime.NakamaModule, rec *userScrubRecord, res *userScrubResult, deadline time.Time) {
	if len(rec.Media) == 0 {
		return
	}
	clips, snapshots := mediaOrphanBuckets()
	// The crew of a dropped clip need not be a group any more: the documents
	// and the rows of the crew still say what it holds.
	index := newMediaRefIndex(ctx, nk, currentLedger())
	index.anyCrew = true
	left := rec.Media[:0:0]
	for i, m := range rec.Media {
		name := m.Kind + " " + m.CrewID + "/" + m.Ref
		if userScrubNow().After(deadline) {
			res.Incomplete = true
			left = append(left, rec.Media[i:]...)
			break
		}
		if !validGameClipID(m.CrewID) || !validGameClipID(m.Ref) {
			rec.addSkipped(m, scrubSkipUnsafeID)
			continue
		}
		bucket := clips
		if m.Kind == ledgerTypeStreamSession {
			bucket = snapshots
		}
		if bucket.client == nil || bucket.bucket == "" {
			res.fail("media %s: the bucket is not configured. The entry stays in the record", name)
			left = append(left, m)
			continue
		}
		held, _, err := index.held(m.CrewID, m.Kind, m.Ref)
		if err != nil {
			res.fail("media %s: %v", name, err)
			left = append(left, m)
			continue
		}
		if held {
			rec.addSkipped(m, scrubSkipHeld)
			continue
		}
		switch m.Kind {
		case ledgerTypeClip:
			poster, video := gameClipKeys(m.CrewID, m.Ref)
			failed := false
			for _, key := range []string{video, poster} {
				if err := deleteObject(ctx, bucket.client, bucket.bucket, key); err != nil {
					res.fail("media %s: %v", key, err)
					failed = true
					continue
				}
				res.MediaDeleted++
			}
			if failed {
				left = append(left, m)
			}
		case ledgerTypeStreamSession:
			n, incomplete, err := deleteObjectsUnder(ctx, bucket.client, bucket.bucket, snapshotSessionPrefix(m.CrewID, m.Ref), m.Ref, deadline)
			res.MediaDeleted += n
			if err != nil {
				res.fail("media %s: %v", name, err)
			}
			if incomplete {
				// The deadline of the run is over: this entry and every
				// entry after it stay.
				res.Incomplete = true
				rec.Media = append(left, rec.Media[i:]...)
				return
			}
			if err != nil {
				left = append(left, m)
			}
		default:
			left = append(left, m)
		}
	}
	rec.Media = left
}

// runPendingUserScrubs runs the scrubs that are not done, and the second run
// of the scrubs that are (userScrubSettle). The deletion job calls it. It
// starts after the record it handled last (deletionJobCursor). The scrub
// records are few: a record is deleted when its scrub is over. So the list
// is read from storage on every run.
func runPendingUserScrubs(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, deadline time.Time) {
	records := map[string]*userScrubRecord{}
	var keys []string
	cursor := ""
	for {
		objects, next, err := nk.StorageList(ctx, "", SystemUserID, UserScrubCollection, 100, cursor)
		if err != nil {
			logger.Error("user_scrub_failed: list of scrub records: %v", err)
			return
		}
		for _, obj := range objects {
			var rec userScrubRecord
			if err := json.Unmarshal([]byte(obj.GetValue()), &rec); err != nil || obj.GetKey() == "" {
				continue
			}
			rec.UserID = obj.GetKey()
			rec.version = obj.GetVersion()
			records[rec.UserID] = &rec
			keys = append(keys, rec.UserID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	deletionJobCursor.mu.Lock()
	last := deletionJobCursor.scrub
	deletionJobCursor.mu.Unlock()
	for _, userID := range rotateAfter(keys, last) {
		rec := records[userID]
		now := userScrubNow()
		if now.After(deadline) {
			return
		}
		if rec.DeletedMs == 0 {
			exists, err := accountExists(ctx, nk, rec.UserID)
			if err != nil {
				logger.Error("user_scrub_failed user=%s: account read: %v", rec.UserID, err)
				continue
			}
			if exists {
				// The delete did not happen, or is not over yet.
				if now.UnixMilli() >= rec.StartedMs+userScrubStale.Milliseconds() {
					if err := deleteUserScrubRecord(ctx, nk, rec); err != nil {
						logger.Error("user_scrub_failed user=%s: scrub record delete: %v", rec.UserID, err)
					}
				}
				continue
			}
			rec.DeletedMs = now.UnixMilli()
		}
		if rec.waitsForStore() && currentLedger() == nil {
			// Nothing to do until the store is loaded.
			logger.Warn("user_scrub_pending user=%s: the durable store is not loaded", rec.UserID)
			continue
		}
		if rec.DoneMs > 0 && !rec.SecondRun && rec.idle() {
			if now.UnixMilli() < rec.DoneMs+userScrubSettle.Milliseconds() {
				continue
			}
			// The second run looks for the crews again.
			rec.SecondRun = true
			rec.rediscover()
		}
		runUserScrub(ctx, nk, logger, rec, deadline)
		deletionJobCursor.mu.Lock()
		deletionJobCursor.scrub = rec.UserID
		deletionJobCursor.mu.Unlock()
	}
}

// ledgerDeletionInterval is the time between two runs of the deletion job.
const ledgerDeletionInterval = 5 * time.Minute

// StartLedgerDeletionJob runs the scrubs and the purges that are not over, at
// start and then on a timer. InitModule starts it once per node. Two nodes
// can run the same scrub: every step can run twice.
func StartLedgerDeletionJob(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger) {
	ticker := time.NewTicker(ledgerDeletionInterval)
	defer ticker.Stop()
	for {
		deadline := time.Now().Add(userScrubBudget)
		runPendingUserScrubs(ctx, nk, logger, deadline)
		runPendingCrewPurges(ctx, nk, logger, time.Now().Add(crewPurgeBudget))
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// AdminScrubUserRPC runs the scrub of a deleted account again. Server-to-
// server (http_key). Request {user_id}. It refuses while the account exists.
// The answer is the result of the run: call again while done is false.
func AdminScrubUserRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if err := requireServerCaller(ctx, "admin_scrub_user"); err != nil {
		return "", err
	}
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", runtime.NewError("invalid request", 3)
	}
	// A user id is a UUID, as a group id.
	if !validPurgeCrewID(req.UserID) || req.UserID == SystemUserID {
		return "", runtime.NewError("user_id must be a user id", 3)
	}
	exists, err := accountExists(ctx, nk, req.UserID)
	if err != nil {
		return "", runtime.NewError("failed to read the account", 13)
	}
	if exists {
		return "", runtime.NewError("the account still exists: delete it first", 9)
	}
	if _, busy := userScrubRunning.Load(req.UserID); busy {
		// A scrub of the user runs in this process. Its record is not touched.
		out, _ := json.Marshal(userScrubResult{UserID: req.UserID, Busy: true, Errors: []string{"a scrub of this user runs in this process"}})
		return string(out), nil
	}
	rec, err := confirmUserScrub(ctx, nk, req.UserID)
	if err != nil {
		return "", runtime.NewError("failed to write the scrub record", 13)
	}
	// A call on a scrub with no work left looks for the crews again: it is
	// the repair of a scrub that missed something. A scrub that was cut
	// continues at its place.
	if rec.idle() {
		rec.rediscover()
	}
	ctx, cancel := context.WithTimeout(ctx, userScrubBudget+30*time.Second)
	defer cancel()
	res := runUserScrub(ctx, nk, logger, rec, userScrubNow().Add(userScrubBudget))
	out, err := json.Marshal(res)
	if err != nil {
		return "", runtime.NewError("encode failed", 13)
	}
	return string(out), nil
}
