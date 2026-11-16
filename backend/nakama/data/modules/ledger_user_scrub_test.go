package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	goneUser  = "99999999-9999-4999-8999-999999999999"
	goneName  = "Zed Gone"
	scrubCrew = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	leftCrew  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	awayCrew  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// useScrubInline points the media at the fake and runs the scrub and the
// purge inline.
func useScrubInline(t *testing.T) *fakeS3 {
	t.Helper()
	f := usePurgeBuckets(t)
	prev := userScrubAsync
	userScrubAsync = func(fn func()) { fn() }
	t.Cleanup(func() { userScrubAsync = prev })
	return f
}

func scrubAppend(t *testing.T, nk runtime.NakamaModule, crewID string, ev CrewEvent) {
	t.Helper()
	ev.ID = ev.ID + "-" + crewID[:4]
	if err := AppendCrewEvent(context.Background(), nk, crewID, ev); err != nil {
		t.Fatal(err)
	}
}

// seedScrubCrew gives a crew events, clips, a stream and a recap that name
// the user in every way, through the writers of the module. member says if
// the user is a member at the delete.
func seedScrubCrew(t *testing.T, nk *delNk, f *fakeS3, crewID string, member bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	nk.groups[crewID] = &api.Group{Id: crewID}
	nk.addMember(crewID, "u1", "Ann")
	nk.addMember(crewID, "u3", "Cy")
	if member {
		nk.addMember(crewID, goneUser, goneName)
	}
	tag := crewID[:4]

	own := StreamSessionData{SessionID: "sg" + tag, StreamerID: goneUser, StreamerName: goneName, Title: "Mine", SnapshotURLs: []string{}}
	watched := StreamSessionData{SessionID: "s1" + tag, StreamerID: "u1", StreamerName: "Ann", Title: "Hers", ViewerIDs: []string{goneUser, "u3"}, SnapshotURLs: []string{}}
	events := []CrewEvent{
		// Started by the user: dropped.
		{ID: "own-game", Type: "game_session", ActorID: goneUser, Timestamp: ms(time.Hour), Data: GameSessionData{GameName: "G", PlayerIDs: []string{goneUser}, PlayerNames: []string{goneName}}},
		{ID: "own-join", Type: "member_joined", ActorID: goneUser, Timestamp: ms(2 * time.Hour), Data: MemberJoinedData{Username: goneName, DisplayName: goneName}},
		{ID: "own-moment", Type: "moment", ActorID: goneUser, Timestamp: ms(3 * time.Hour), Data: MomentData{Text: "gg"}},
		{ID: "own-stream", Type: "stream_session", ActorID: goneUser, Timestamp: ms(4 * time.Hour), Score: 30, Data: own},
		// Older than the 7 days of the ledger document: only the store holds it.
		{ID: "own-old-game", Type: "game_session", ActorID: goneUser, Timestamp: ms(40 * 24 * time.Hour), Data: GameSessionData{GameName: "G", PlayerIDs: []string{goneUser}, PlayerNames: []string{goneName}}},
		{ID: "duo-voice", Type: "voice_session", Timestamp: ms(5 * time.Hour), Data: VoiceSessionData{ParticipantIDs: []string{"u1", goneUser}, ParticipantNames: []string{"Ann", goneName}}},
		// Started by another member: the user leaves the lists.
		{ID: "trio-voice", Type: "voice_session", Timestamp: ms(6 * time.Hour), Data: VoiceSessionData{ParticipantIDs: []string{"u1", goneUser, "u3"}, ParticipantNames: []string{"Ann", goneName, "Cy"}}},
		{ID: "co-game", Type: "game_session", ActorID: "u1", Timestamp: ms(7 * time.Hour), Data: GameSessionData{GameName: "G", PlayerIDs: []string{"u1", goneUser}, PlayerNames: []string{"Ann", goneName}, PlayerOverlapMin: []int{50, 20}}},
		{ID: "old-co-game", Type: "game_session", ActorID: "u1", Timestamp: ms(41 * 24 * time.Hour), Data: GameSessionData{GameName: "G", PlayerIDs: []string{"u1", goneUser}, PlayerNames: []string{"Ann", "Old Zed"}, PlayerOverlapMin: []int{50, 20}}},
		{ID: "invited", Type: "member_joined", ActorID: "u3", Timestamp: ms(8 * time.Hour), Data: MemberJoinedData{Username: "Cy", DisplayName: "Cy", InvitedBy: goneUser, InvitedByName: goneName}},
		{ID: "watched", Type: "stream_session", ActorID: "u1", Timestamp: ms(9 * time.Hour), Score: 30, Data: watched},
		// Does not name the user.
		{ID: "other-game", Type: "game_session", ActorID: "u3", Timestamp: ms(10 * time.Hour), Data: GameSessionData{GameName: "G", PlayerIDs: []string{"u3"}, PlayerNames: []string{"Cy"}}},
	}
	for _, ev := range events {
		scrubAppend(t, nk, crewID, ev)
	}
	for _, s := range []struct {
		id   string
		ts   int64
		data StreamSessionData
	}{{"own-stream-" + tag, ms(4 * time.Hour), own}, {"watched-" + tag, ms(9 * time.Hour), watched}} {
		if err := UpsertStreamSession(ctx, nk, crewID, storedStreamSessionFrom(s.id, s.ts, 30, s.data)); err != nil {
			t.Fatal(err)
		}
	}
	clips := []StoredClip{
		{EventID: "ev-own-" + tag, ClipID: "cg" + tag, ActorID: goneUser, ClipperName: goneName, Ts: ms(11 * time.Hour), Score: 50, ClipType: clipTypeVoice},
		{EventID: "ev-old-" + tag, ClipID: "cold" + tag, ActorID: goneUser, ClipperName: goneName, Ts: ms(50 * 24 * time.Hour), Score: 50, ClipType: clipTypeGame},
		{EventID: "ev-shared-" + tag, ClipID: "c1" + tag, ActorID: "u1", ClipperName: "Ann", Ts: ms(12 * time.Hour), Score: 50, ClipType: clipTypeVoice,
			Participants: []string{"u1", goneUser}, ParticipantNames: []string{"Ann", goneName}},
	}
	for _, c := range clips {
		if _, err := AppendClip(ctx, nk, crewID, c); err != nil {
			t.Fatal(err)
		}
	}
	recap := histRecap(time.UnixMilli(weekStartOf(ms(14*24*time.Hour))), 90)
	recap.CrewID = crewID
	recap.MostActive = goneName
	recap.BestStreak, recap.BestStreakName = 3, goneName
	recap.LongestSession = "Ann, " + goneName + " in Lobby (90m)"
	recap.TopMembers = []RecapMember{{UserID: goneUser, DisplayName: goneName, HangoutMin: 60}, {UserID: "u1", DisplayName: "Ann", HangoutMin: 30}}
	recap.ClipOfWeek = &RecapClipOfWeek{ClipID: "cold" + tag, Title: "Old Zed clipped that", ClipperName: "Old Zed", MediaURL: "m"}
	recap.Awards = []RecapAward{{Kind: "grinder", Title: "Grinder of the week", Name: goneName}, {Kind: "heater", Title: "Biggest heater", Name: "Ann"}}
	// A recap from before recaps held user ids: only a name.
	old := histRecap(time.UnixMilli(weekStartOf(ms(28*24*time.Hour))), 40)
	old.CrewID = crewID
	old.MostActive = "Old Zed"
	old.TopMembers = []RecapMember{{DisplayName: "Old Zed"}, {DisplayName: "Ann"}}
	for _, r := range []WeeklyRecapData{old, recap} {
		if err := AppendRecap(ctx, nk, crewID, r); err != nil {
			t.Fatal(err)
		}
	}

	f.put("clips", "crews/"+crewID+"/cg"+tag+".mp4", 10, time.Hour)
	f.put("clips", "crews/"+crewID+"/cold"+tag+".mp4", 10, time.Hour)
	f.put("clips", "crews/"+crewID+"/cold"+tag+".jpg", 10, time.Hour)
	f.put("clips", "crews/"+crewID+"/c1"+tag+".mp4", 10, time.Hour)
	f.put("snaps", "snapshots/"+crewID+"/sg"+tag+"/1.jpg", 10, time.Hour)
	f.put("snaps", "snapshots/"+crewID+"/sg"+tag+"/2.jpg", 10, time.Hour)
	f.put("snaps", "snapshots/"+crewID+"/s1"+tag+"/1.jpg", 10, time.Hour)
}

// scrubDocCollections are the legacy documents the rules cover.
var scrubDocCollections = []string{"crew_events", "crew_clips", "crew_stream_sessions", "crew_recaps"}

// userTraces lists where an install still names the user in a crew: in a
// legacy document or in a row of the store, by id or by the name the account
// had at the delete. A name the user had before ("Old Zed") is not looked
// for: checkScrubbed says where it must be gone and where it stays.
func userTraces(t *testing.T, nk *delNk, store ledgerStore, crewID string) []string {
	t.Helper()
	var out []string
	names := func(text string) string {
		for _, needle := range []string{goneUser, goneName} {
			if strings.Contains(text, needle) {
				return needle
			}
		}
		return ""
	}
	nk.mu.Lock()
	for _, collection := range scrubDocCollections {
		if hit := names(nk.storage[fakeStorageKey(collection, crewID, SystemUserID)]); hit != "" {
			out = append(out, fmt.Sprintf("doc %s holds %q", collection, hit))
		}
	}
	nk.mu.Unlock()
	if store == nil {
		return out
	}
	for _, ev := range crewRows(t, store, crewID) {
		raw, _ := json.Marshal(ev)
		if hit := names(string(raw)); hit != "" {
			out = append(out, fmt.Sprintf("row %s %s holds %q", ev.Type, ev.ID, hit))
		}
	}
	rows, err := store.NamingUserInCrew(context.Background(), goneUser, crewID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range rows {
		out = append(out, "user index names "+ev.ID)
	}
	return out
}

// crewHolds reports whether the documents or the store hold an event id.
func crewHolds(t *testing.T, nk *delNk, store ledgerStore, crewID, id string) (inDocs, inStore bool) {
	t.Helper()
	nk.mu.Lock()
	for _, collection := range scrubDocCollections {
		if strings.Contains(nk.storage[fakeStorageKey(collection, crewID, SystemUserID)], `"`+id+`"`) {
			inDocs = true
		}
	}
	nk.mu.Unlock()
	if store != nil {
		for _, ev := range crewRows(t, store, crewID) {
			if ev.ID == id || ev.ID == "clip_"+id {
				inStore = true
			}
		}
	}
	return inDocs, inStore
}

// deleteAccount is what DELETE /v2/account does around the hooks.
func deleteAccount(t *testing.T, nk *delNk, userID string) {
	t.Helper()
	ctx := ctxWithUser(userID)
	if err := BeforeDeleteAccount(ctx, testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	removeAccount(nk, userID)
	if err := AfterDeleteAccount(ctx, testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
}

func removeAccount(nk *delNk, userID string) {
	delete(nk.users, userID)
	for crewID, members := range nk.members {
		kept := members[:0:0]
		for _, m := range members {
			if m.GetUser().GetId() != userID {
				kept = append(kept, m)
			}
		}
		nk.members[crewID] = kept
	}
}

func scrubRecordOf(t *testing.T, nk runtime.NakamaModule) *userScrubRecord {
	t.Helper()
	rec, err := readUserScrubRecord(context.Background(), nk, goneUser)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func scrubRPC(nk runtime.NakamaModule, userID string) (userScrubResult, error) {
	var res userScrubResult
	out, err := AdminScrubUserRPC(context.Background(), testLogger(), nil, nk, `{"user_id":"`+userID+`"}`)
	if err == nil {
		err = json.Unmarshal([]byte(out), &res)
	}
	return res, err
}

// checkScrubbed asserts what an account delete leaves of the crews of the
// seed: no trace of the user, the events of the others, and the media.
func checkScrubbed(t *testing.T, nk *delNk, store ledgerStore, f *fakeS3, when string) {
	t.Helper()
	for _, crewID := range []string{scrubCrew, leftCrew} {
		checkScrubbedCrew(t, nk, store, f, crewID, when)
	}
}

func checkScrubbedCrew(t *testing.T, nk *delNk, store ledgerStore, f *fakeS3, crewID, when string) {
	t.Helper()
	{
		tag := crewID[:4]
		if traces := userTraces(t, nk, store, crewID); len(traces) != 0 {
			t.Fatalf("%s: crew %s still names the user:\n%s", when, tag, strings.Join(traces, "\n"))
		}
		for _, id := range []string{"own-game", "own-join", "own-moment", "own-stream", "own-old-game", "duo-voice"} {
			if inDocs, inStore := crewHolds(t, nk, store, crewID, id+"-"+tag); inDocs || inStore {
				t.Fatalf("%s: crew %s holds %s: documents=%t store=%t", when, tag, id, inDocs, inStore)
			}
		}
		for _, id := range []string{"ev-own-" + tag, "ev-old-" + tag} {
			if inDocs, inStore := crewHolds(t, nk, store, crewID, id); inDocs || inStore {
				t.Fatalf("%s: crew %s holds the clip %s: documents=%t store=%t", when, tag, id, inDocs, inStore)
			}
		}
		// The events of the other members stay, on both sides.
		for _, id := range []string{"trio-voice", "co-game", "invited", "watched", "other-game"} {
			inDocs, inStore := crewHolds(t, nk, store, crewID, id+"-"+tag)
			if !inDocs || (store != nil && !inStore) {
				t.Fatalf("%s: crew %s lost %s: documents=%t store=%t", when, tag, id, inDocs, inStore)
			}
		}
		if inDocs, inStore := crewHolds(t, nk, store, crewID, "ev-shared-"+tag); !inDocs || (store != nil && !inStore) {
			t.Fatalf("%s: crew %s lost the clip of another member: documents=%t store=%t", when, tag, inDocs, inStore)
		}
		if store != nil {
			if _, inStore := crewHolds(t, nk, store, crewID, "old-co-game-"+tag); !inStore {
				t.Fatalf("%s: crew %s lost an old event of another member", when, tag)
			}
		}
		// The recaps stay, without the user.
		recaps := docRecaps(nk, crewID)
		if store != nil {
			var rows []WeeklyRecapData
			for _, ev := range crewRows(t, store, crewID) {
				if ev.Type == ledgerTypeWeeklyRecap {
					r, _ := recapFromRow(ev)
					rows = append(rows, r)
				}
			}
			if a, b := scrubJSON(t, rows), scrubJSON(t, recaps); a != b {
				t.Fatalf("%s: the recap rows of crew %s differ from the document:\n%s\n%s", when, tag, a, b)
			}
		}
		if len(recaps) != 2 {
			t.Fatalf("%s: crew %s has %d recaps, want 2", when, tag, len(recaps))
		}
		// The recap with the id of the user: every field is clean. Its clip
		// of the week was a clip of the user under an old name, and goes by
		// its clip_id.
		if r := recaps[1]; len(r.TopMembers) != 1 || r.TopMembers[0].DisplayName != "Ann" || r.MostActive != "" || r.BestStreakName != "" ||
			r.LongestSession != "" || r.ClipOfWeek != nil || len(r.Awards) != 1 || r.TotalMin == 0 {
			t.Fatalf("%s: recap of crew %s = %+v", when, tag, r)
		}
		// Known limit: a recap from before recaps held user ids names the
		// user only by a name the account no longer had at the delete. No
		// rule says that this name is the user, so it stays.
		if r := recaps[0]; r.MostActive != "Old Zed" || len(r.TopMembers) != 2 {
			t.Fatalf("%s: old recap of crew %s = %+v", when, tag, r)
		}
		// The media of the clips and the stream of the user is gone. The
		// media of the others stays.
		want := "crews/" + crewID + "/c1" + tag + ".mp4"
		var clips, snaps []string
		for _, k := range f.keys("clips") {
			if strings.Contains(k, crewID) {
				clips = append(clips, k)
			}
		}
		for _, k := range f.keys("snaps") {
			if strings.Contains(k, crewID) {
				snaps = append(snaps, k)
			}
		}
		if len(clips) != 1 || clips[0] != want {
			t.Fatalf("%s: clip objects of crew %s = %v, want [%s]", when, tag, clips, want)
		}
		if len(snaps) != 1 || !strings.Contains(snaps[0], "/s1"+tag+"/") {
			t.Fatalf("%s: snapshot objects of crew %s = %v", when, tag, snaps)
		}
	}
}

// An account delete scrubs the user from the documents and from the store in
// every phase, also in a crew the user left before, and a reconcile brings
// nothing back.
func TestScrubUserFromLedger(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		seedScrubCrew(t, nk, f, scrubCrew, true)
		seedScrubCrew(t, nk, f, leftCrew, false)
		seedPurgeCrew(t, nk, f, otherCrew)
		if n := len(userTraces(t, nk, store, scrubCrew)); n < 20 {
			t.Fatalf("the seed is too small to prove the scrub: %d traces", n)
		}
		other := crewLeftovers(t, nk, store, f, otherCrew)
		otherRows, _ := json.Marshal(crewRows(t, store, otherCrew))

		deleteAccount(t, nk, goneUser)

		checkScrubbed(t, nk, store, f, "after the delete")
		rec := scrubRecordOf(t, nk)
		if rec == nil || rec.DoneMs == 0 || rec.LastError != "" || !rec.idle() {
			t.Fatalf("scrub record = %+v", rec)
		}
		r := reconcilePass(t, nk)
		reconcilePass(t, nk)
		checkScrubbed(t, nk, store, f, "after a reconcile")
		if !r.last.clean() || !r.lastHist.clean() {
			t.Fatalf("the reconcile after a scrub is not clean: %+v %+v", r.last, r.lastHist)
		}
		// A crew the user was never in is not touched.
		if after := crewLeftovers(t, nk, store, f, otherCrew); strings.Join(after, "\n") != strings.Join(other, "\n") {
			t.Fatalf("another crew changed: %v", after)
		}
		if rows, _ := json.Marshal(crewRows(t, store, otherCrew)); string(rows) != string(otherRows) {
			t.Fatal("the rows of another crew changed")
		}

		// The second run, one day later, ends the scrub: the record with the
		// names of the user is deleted.
		prev := userScrubNow
		userScrubNow = func() time.Time { return time.Now().Add(userScrubSettle + time.Minute) }
		t.Cleanup(func() { userScrubNow = prev })
		runPendingUserScrubs(context.Background(), nk, testLogger(), userScrubNow().Add(time.Minute))
		if rec := scrubRecordOf(t, nk); rec != nil {
			t.Fatalf("the scrub record stays after the second run: %+v", rec)
		}
		checkScrubbed(t, nk, store, f, "after the second run")
	})
}

// With no store loaded (the schema setup failed at start) a scrub is not
// done: it scrubs the documents and waits. The job ends it when the store is
// loaded, and the rows that the store held are scrubbed then.
func TestScrubUserWaitsForTheStore(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	seedScrubCrew(t, nk, f, leftCrew, false)

	setLedger(nil)
	deleteAccount(t, nk, goneUser)
	for _, crewID := range []string{scrubCrew, leftCrew} {
		if traces := userTraces(t, nk, nil, crewID); len(traces) != 0 {
			t.Fatalf("the documents of crew %s still name the user:\n%s", crewID[:4], strings.Join(traces, "\n"))
		}
	}
	rec := scrubRecordOf(t, nk)
	if rec == nil || rec.DoneMs != 0 || !rec.StorePending || rec.idle() || len(rec.Names) == 0 {
		t.Fatalf("scrub record with no store = %+v", rec)
	}
	// The job and the RPC do not end it while the store is not loaded.
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if res, err := scrubRPC(nk, goneUser); err != nil || res.Done || !res.StorePending {
		t.Fatalf("admin_scrub_user with no store = %+v err=%v", res, err)
	}
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs != 0 || !rec.StorePending {
		t.Fatalf("scrub record with no store = %+v", rec)
	}
	if n := len(userTraces(t, nk, store, scrubCrew)); n == 0 {
		t.Fatal("the test needs store rows that name the user")
	}

	// The store is loaded.
	setLedger(store)
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	checkScrubbed(t, nk, store, f, "after the store is loaded")
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs == 0 || rec.StorePending {
		t.Fatalf("scrub record with the store = %+v", rec)
	}
}

// A scrub whose document write fails is not done. It does not fail the
// account delete, a reconcile does not undo what it did, and
// admin_scrub_user ends it.
func TestScrubUserIsRepairedAfterAFailure(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		seedScrubCrew(t, nk, f, scrubCrew, true)
		seedScrubCrew(t, nk, f, leftCrew, false)
		nk.failWrite[CrewClipsCollection] = true

		deleteAccount(t, nk, goneUser)
		rec := scrubRecordOf(t, nk)
		if rec == nil || rec.DoneMs != 0 || len(rec.Crews) != 2 || !strings.Contains(rec.LastError, CrewClipsCollection) {
			t.Fatalf("scrub record after a failed write = %+v", rec)
		}
		if n := len(f.deletes); n != 0 {
			t.Fatalf("a scrub that is not done deleted %d objects", n)
		}
		reconcilePass(t, nk)

		// The account is gone, so the names come from the record.
		nk.failWrite = map[string]bool{}
		res, err := scrubRPC(nk, goneUser)
		if err != nil || !res.Done || res.CrewsLeft != 0 {
			t.Fatalf("admin_scrub_user = %+v err=%v", res, err)
		}
		checkScrubbed(t, nk, store, f, "after the repair")
		reconcilePass(t, nk)
		reconcilePass(t, nk)
		checkScrubbed(t, nk, store, f, "after a reconcile")

		// A second call finds nothing to do.
		deletes := len(f.deletes)
		res, err = scrubRPC(nk, goneUser)
		if err != nil || !res.Done || res.Dropped != 0 || res.Changed != 0 || len(f.deletes) != deletes {
			t.Fatalf("second admin_scrub_user = %+v err=%v", res, err)
		}
	})
}

func TestAdminScrubUserRefuses(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	before := len(userTraces(t, nk, store, scrubCrew))

	if _, err := AdminScrubUserRPC(ctxWithUser("u1"), testLogger(), nil, nk, `{"user_id":"`+goneUser+`"}`); rpcCode(err) != 7 {
		t.Fatalf("a user call: %v", err)
	}
	for _, bad := range []string{"", "u1", SystemUserID} {
		if _, err := scrubRPC(nk, bad); rpcCode(err) != 3 {
			t.Fatalf("user id %q: %v", bad, err)
		}
	}
	// The account exists.
	if _, err := scrubRPC(nk, goneUser); rpcCode(err) != 9 {
		t.Fatalf("an account that exists: %v", err)
	}
	if after := len(userTraces(t, nk, store, scrubCrew)); after != before || len(f.deletes) != 0 || scrubRecordOf(t, nk) != nil {
		t.Fatalf("a refused scrub changed data: %d traces, was %d", after, before)
	}

	// No hook ran (the account was deleted in the console): the RPC scrubs
	// with the names the events hold.
	removeAccount(nk, goneUser)
	res, err := scrubRPC(nk, goneUser)
	if err != nil || !res.Done || res.Dropped == 0 || res.MediaDeleted == 0 {
		t.Fatalf("admin_scrub_user = %+v err=%v", res, err)
	}
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
}

// The before hook writes a record. When the delete does not happen, the job
// deletes the record and scrubs nothing.
func TestScrubRecordOfAnAccountThatStays(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	before := len(userTraces(t, nk, store, scrubCrew))
	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	rec := scrubRecordOf(t, nk)
	if rec == nil || rec.DeletedMs != 0 || len(rec.Crews) != 1 || len(rec.Names) != 1 || rec.Names[0] != goneName {
		t.Fatalf("record of the before hook = %+v", rec)
	}
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if scrubRecordOf(t, nk) == nil {
		t.Fatal("the job deleted a new record")
	}
	prev := userScrubNow
	userScrubNow = func() time.Time { return time.Now().Add(userScrubStale + time.Minute) }
	t.Cleanup(func() { userScrubNow = prev })
	runPendingUserScrubs(context.Background(), nk, testLogger(), userScrubNow().Add(time.Minute))
	if scrubRecordOf(t, nk) != nil {
		t.Fatal("the record of an account that stays is not deleted")
	}
	if after := len(userTraces(t, nk, store, scrubCrew)); after != before || len(f.deletes) != 0 {
		t.Fatalf("an account that stays was scrubbed: %d traces, was %d", after, before)
	}
}

// The after hook did not run (the node stopped). The job sees that the
// account is gone and scrubs.
func TestDeletionJobScrubsWhenTheAfterHookDidNotRun(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	removeAccount(nk, goneUser)
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
}

// A user with more rows than one batch, and a run that is cut at its budget:
// the next run continues.
func TestScrubUserInBatchesAndAfterItsBudget(t *testing.T) {
	store := useHistoryStoreReads(t)
	nk := newDelNk()
	markersInStorage(t, nk)
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	const many = 2*userScrubBatch + 50
	base := time.Now().Add(-90 * 24 * time.Hour)
	for i := 0; i < many; i++ {
		ev := CrewEvent{ID: fmt.Sprintf("bulk-%04d", i), CrewID: scrubCrew, Type: "game_session", ActorID: goneUser,
			Timestamp: base.Add(time.Duration(i) * time.Minute).UnixMilli(), Data: GameSessionData{GameName: "G"}}
		if i%2 == 1 {
			ev.ActorID = "u1"
			ev.Data = GameSessionData{GameName: "G", PlayerIDs: []string{"u1", goneUser}, PlayerNames: []string{"Ann", goneName}}
		}
		if _, err := store.Append(context.Background(), ev, ""); err != nil {
			t.Fatal(err)
		}
	}
	rows := len(crewRows(t, store, scrubCrew))

	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	removeAccount(nk, goneUser)
	rec, err := confirmUserScrub(context.Background(), nk, goneUser)
	if err != nil {
		t.Fatal(err)
	}
	// A run past its deadline does nothing and says so.
	res := runUserScrub(context.Background(), nk, testLogger(), rec, time.Now().Add(-time.Second))
	if res.Done || !res.Incomplete || res.Dropped != 0 {
		t.Fatalf("a run past its deadline = %+v", res)
	}
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs != 0 {
		t.Fatalf("scrub record = %+v", rec)
	}
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user: %d traces, first %s", len(traces), traces[0])
	}
	// Half of the bulk rows were events of the user, the other half stays.
	if got, want := len(crewRows(t, store, scrubCrew)), rows-many/2-8; got != want {
		t.Fatalf("rows after the scrub = %d, want %d", got, want)
	}
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs == 0 {
		t.Fatalf("scrub record = %+v", rec)
	}
}

// The media of a clip is not deleted while the crew holds another clip with
// the same clip_id (a document from before clip_id was checked).
func TestScrubKeepsMediaThatAnotherClipHolds(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	tag := scrubCrew[:4]
	doc, _ := readClipsDoc(context.Background(), nk, scrubCrew)
	doc.Clips = append(doc.Clips, StoredClip{EventID: "ev-twin", ClipID: "cg" + tag, ActorID: "u3", ClipperName: "Cy", Ts: time.Now().UnixMilli(), ClipType: clipTypeVoice})
	if err := writeClipsDoc(context.Background(), nk, scrubCrew, doc, ""); err != nil {
		t.Fatal(err)
	}
	deleteAccount(t, nk, goneUser)
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
	keys := strings.Join(f.keys("clips"), ",")
	if !strings.Contains(keys, "cg"+tag+".mp4") {
		t.Fatalf("the media that another clip holds is gone: %s", keys)
	}
	if strings.Contains(keys, "cold"+tag) {
		t.Fatalf("the media of the own clip stays: %s", keys)
	}
}

// A crew of the user that is no longer a group is scrubbed as any other
// crew. The scrub never purges: the events of the other members, their media
// and the documents of the crew stay, and no purge record is written.
func TestScrubNeverPurgesACrew(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		seedScrubCrew(t, nk, f, scrubCrew, true)
		seedScrubCrew(t, nk, f, leftCrew, true)
		if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		removeAccount(nk, goneUser)
		// The group is gone at the time of the scrub, and no hook ran for it.
		if err := nk.GroupDelete(context.Background(), leftCrew); err != nil {
			t.Fatal(err)
		}
		if err := AfterDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
			t.Fatal(err)
		}
		checkScrubbed(t, nk, store, f, "after the delete")
		if rec, _ := readCrewPurgeRecord(context.Background(), nk, leftCrew); rec != nil {
			t.Fatalf("the scrub wrote a purge record: %+v", rec)
		}
		reconcilePass(t, nk)
		runPendingCrewPurges(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		checkScrubbed(t, nk, store, f, "after a reconcile")
	})
}

// scrubReady seeds one crew, runs the before hook and removes the account.
// It returns the record of an account that is seen to be gone.
func scrubReady(t *testing.T, nk *delNk, f *fakeS3) *userScrubRecord {
	t.Helper()
	seedScrubCrew(t, nk, f, scrubCrew, true)
	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	removeAccount(nk, goneUser)
	rec, err := confirmUserScrub(context.Background(), nk, goneUser)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// Two nodes run the scrub of one user. The node that works on an old copy of
// the record must not write it: here the other node ended the scrub and
// deleted the record while this run was in a crew.
func TestStaleScrubRunDoesNotBringBackADeletedRecord(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	rec := scrubReady(t, nk, f)
	fired := false
	nk.onRead[CrewRecapsCollection] = func() {
		if fired {
			return
		}
		fired = true
		// The other node.
		if err := nk.StorageDelete(context.Background(), []*runtime.StorageDelete{{Collection: UserScrubCollection, Key: goneUser, UserID: SystemUserID}}); err != nil {
			t.Error(err)
		}
	}
	res := runUserScrub(context.Background(), nk, testLogger(), rec, time.Now().Add(time.Minute))
	if !fired {
		t.Fatal("the test did not reach the recaps of the crew")
	}
	if res.Done || len(res.Errors) == 0 {
		t.Fatalf("a run on an old record = %+v", res)
	}
	if got := scrubRecordOf(t, nk); got != nil {
		t.Fatalf("a stale run wrote the deleted record again: %+v", got)
	}
	if nk.stale == 0 {
		t.Fatal("no write was refused: the record write carries no version")
	}
	if len(f.deletes) != 0 {
		t.Fatalf("a run that lost its record deleted media: %v", f.deletes)
	}
}

// The other node wrote the record (it found media to delete) while this run
// was in a crew. This run must not overwrite it.
func TestStaleScrubRunDoesNotDropTheMediaOfANewerRecord(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	rec := scrubReady(t, nk, f)
	theirs := scrubMedia{Kind: ledgerTypeClip, CrewID: leftCrew, Ref: "their-clip"}
	fired := false
	nk.onRead[CrewRecapsCollection] = func() {
		if fired {
			return
		}
		fired = true
		other, err := readUserScrubRecord(context.Background(), nk, goneUser)
		if err != nil || other == nil {
			t.Errorf("the other node cannot read the record: %v", err)
			return
		}
		other.addMedia(theirs)
		if err := writeUserScrubRecord(context.Background(), nk, other); err != nil {
			t.Error(err)
		}
	}
	res := runUserScrub(context.Background(), nk, testLogger(), rec, time.Now().Add(time.Minute))
	if res.Done || len(res.Errors) == 0 || !fired {
		t.Fatalf("a run on an old record = %+v fired=%t", res, fired)
	}
	got := scrubRecordOf(t, nk)
	if got == nil {
		t.Fatal("the record is gone")
	}
	found := false
	for _, m := range got.Media {
		found = found || m == theirs
	}
	if !found || got.DoneMs != 0 {
		t.Fatalf("the stale run overwrote the newer record: %+v", got)
	}
	// The next run works on the current record and ends the scrub.
	delete(nk.onRead, CrewRecapsCollection)
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if traces := userTraces(t, nk, currentLedger(), scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
	if got := scrubRecordOf(t, nk); got == nil || got.DoneMs == 0 {
		t.Fatalf("scrub record = %+v", got)
	}
}

// One scrub of a user runs at a time in a process: the hook, the job and the
// RPC can all start one.
func TestOneScrubOfAUserAtATime(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	rec := scrubReady(t, nk, f)
	inside, release := make(chan struct{}), make(chan struct{})
	first := true
	nk.onRead[CrewRecapsCollection] = func() {
		if first {
			first = false
			close(inside)
			<-release
		}
	}
	done := make(chan userScrubResult)
	go func() { done <- runUserScrub(context.Background(), nk, testLogger(), rec, time.Now().Add(time.Minute)) }()
	<-inside
	second, err := scrubRPC(nk, goneUser)
	if err != nil || !second.Busy || second.Done || second.Dropped != 0 {
		t.Fatalf("a second scrub while the first runs = %+v err=%v", second, err)
	}
	close(release)
	if res := <-done; !res.Done {
		t.Fatalf("the first scrub = %+v", res)
	}
	if again, err := scrubRPC(nk, goneUser); err != nil || again.Busy || !again.Done {
		t.Fatalf("a scrub after the first ended = %+v err=%v", again, err)
	}
}

// The delete of a record carries the version too: a run that ends on an old
// copy does not delete the record of a newer run.
func TestScrubRecordDeleteIsVersionChecked(t *testing.T) {
	nk := newDelNk()
	ctx := context.Background()
	mine, err := confirmUserScrub(ctx, nk, goneUser)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := readUserScrubRecord(ctx, nk, goneUser)
	other.addCrews(scrubCrew)
	if err := writeUserScrubRecord(ctx, nk, other); err != nil {
		t.Fatal(err)
	}
	if err := deleteUserScrubRecord(ctx, nk, mine); !errors.Is(err, errScrubRecordLost) {
		t.Fatalf("delete on an old copy: %v", err)
	}
	if got := scrubRecordOf(t, nk); got == nil || len(got.Crews) != 1 {
		t.Fatalf("the record of the newer run = %+v", got)
	}
	if err := writeUserScrubRecord(ctx, nk, mine); !errors.Is(err, errScrubRecordLost) {
		t.Fatalf("write on an old copy: %v", err)
	}
	if err := deleteUserScrubRecord(ctx, nk, other); err != nil {
		t.Fatal(err)
	}
	// A copy of a record that is gone does not make it again.
	if err := writeUserScrubRecord(ctx, nk, other); !errors.Is(err, errScrubRecordLost) || scrubRecordOf(t, nk) != nil {
		t.Fatalf("write of a deleted record: %v", err)
	}
}

// A media entry stays in the record until its delete is complete. Here the
// snapshots bucket is not configured: the scrub is not done, and it ends
// when the bucket is there.
func TestScrubMediaStaysWhileItsBucketIsNotConfigured(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	rec := scrubReady(t, nk, f)
	withSnapshots := mediaOrphanBuckets
	mediaOrphanBuckets = func() (mediaBucket, mediaBucket) {
		clips, snapshots := withSnapshots()
		snapshots.client, snapshots.bucket = nil, ""
		return clips, snapshots
	}
	res := runUserScrub(context.Background(), nk, testLogger(), rec, time.Now().Add(time.Minute))
	mediaOrphanBuckets = withSnapshots
	tag := scrubCrew[:4]
	want := scrubMedia{Kind: ledgerTypeStreamSession, CrewID: scrubCrew, Ref: "sg" + tag}
	got := scrubRecordOf(t, nk)
	if res.Done || res.MediaPending != 1 || got == nil || got.DoneMs != 0 || len(got.Media) != 1 || got.Media[0] != want {
		t.Fatalf("result = %+v record = %+v", res, got)
	}
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
	if keys := strings.Join(f.keys("snaps"), ","); !strings.Contains(keys, "/sg"+tag+"/") {
		t.Fatalf("snapshots = %s", keys)
	}
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	if got := scrubRecordOf(t, nk); got == nil || got.DoneMs == 0 || len(got.Media) != 0 {
		t.Fatalf("record after the bucket is there = %+v", got)
	}
	if keys := strings.Join(f.keys("snaps"), ","); strings.Contains(keys, "/sg"+tag+"/") {
		t.Fatalf("snapshots after the bucket is there = %s", keys)
	}
}

// The media deletes of a run share the one deadline of the run. A stream
// whose snapshots are not all deleted at the deadline stays in the record,
// with every entry after it.
func TestScrubMediaIsCutAtTheDeadlineOfTheRun(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	rec := scrubReady(t, nk, f)
	tag := scrubCrew[:4]
	base := time.Now()
	// The media, in this order: a stream with two snapshots, then a clip.
	rec.Discovered, rec.Crews = true, nil
	rec.Media = []scrubMedia{
		{Kind: ledgerTypeStreamSession, CrewID: scrubCrew, Ref: "ghost"},
		{Kind: ledgerTypeClip, CrewID: scrubCrew, Ref: "ghostclip"},
	}
	f.put("snaps", "snapshots/"+scrubCrew+"/ghost/1.jpg", 10, time.Hour)
	f.put("snaps", "snapshots/"+scrubCrew+"/ghost/2.jpg", 10, time.Hour)
	f.put("clips", "crews/"+scrubCrew+"/ghostclip.mp4", 10, time.Hour)
	// The clock of the object deletes moves 40 seconds with each look: the
	// first snapshot is deleted before the deadline, the second is not.
	ticks := 0
	prev := crewPurgeNow
	crewPurgeNow = func() time.Time { ticks++; return base.Add(time.Duration(ticks) * 40 * time.Second) }
	res := userScrubResult{}
	deleteScrubMedia(context.Background(), nk, rec, &res, base.Add(time.Minute))
	crewPurgeNow = prev
	if !res.Incomplete || res.MediaDeleted != 1 || len(rec.Media) != 2 || rec.Media[0].Ref != "ghost" || rec.Media[1].Ref != "ghostclip" {
		t.Fatalf("result = %+v media = %+v", res, rec.Media)
	}
	if keys := strings.Join(f.keys("snaps"), ","); !strings.Contains(keys, "ghost/2.jpg") {
		t.Fatalf("snapshots = %s", keys)
	}
	// A run whose deadline is over deletes nothing and keeps every entry.
	// A clip is first in the list, so the check between two entries decides.
	rec.Media = append([]scrubMedia{{Kind: ledgerTypeClip, CrewID: scrubCrew, Ref: "cg" + tag}}, rec.Media...)
	deletes := len(f.deletes)
	res = userScrubResult{}
	deleteScrubMedia(context.Background(), nk, rec, &res, time.Now().Add(-time.Second))
	if !res.Incomplete || len(rec.Media) != 3 || len(f.deletes) != deletes {
		t.Fatalf("past the deadline: result = %+v media = %+v", res, rec.Media)
	}
	// The next run ends the work.
	res = userScrubResult{}
	deleteScrubMedia(context.Background(), nk, rec, &res, time.Now().Add(time.Minute))
	if res.Incomplete || len(rec.Media) != 0 || strings.Contains(strings.Join(f.keys("snaps"), ","), "ghost") {
		t.Fatalf("the next run: result = %+v media = %+v", res, rec.Media)
	}
}

// A clip_id that is not safe in an object key makes no key. The entry is not
// dropped in silence: it is in the record and in the result of the run.
func TestScrubReportsMediaItDoesNotDelete(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	ctx := context.Background()
	if _, err := AppendClip(ctx, nk, scrubCrew, StoredClip{EventID: "ev-unsafe", ClipID: "../up", ActorID: goneUser, Ts: time.Now().UnixMilli(), ClipType: clipTypeVoice}); err != nil {
		t.Fatal(err)
	}
	// A stream session of the user from before sessions had an id.
	if err := UpsertStreamSession(ctx, nk, scrubCrew, StoredStreamSession{EventID: "ev-noid", StreamerID: goneUser, StreamerName: goneName, Ts: time.Now().Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	f.put("clips", "crews/"+scrubCrew+"/../up.mp4", 10, time.Hour)
	deleteAccount(t, nk, goneUser)

	rec := scrubRecordOf(t, nk)
	if rec == nil || rec.DoneMs == 0 || len(rec.Media) != 0 {
		t.Fatalf("record = %+v", rec)
	}
	reasons := map[string]string{}
	for _, sk := range rec.Skipped {
		reasons[sk.Kind+"/"+sk.Ref] = sk.Reason
	}
	if reasons["clip/../up"] != scrubSkipUnsafeID || reasons["stream_session/"] != scrubSkipNoSession || len(reasons) != 2 {
		t.Fatalf("skipped = %+v", rec.Skipped)
	}
	if keys := strings.Join(f.keys("clips"), ","); !strings.Contains(keys, "/../up.mp4") {
		t.Fatalf("the object of the unsafe id was deleted: %s", keys)
	}
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
	// Every run reports what the record holds as skipped.
	res, err := scrubRPC(nk, goneUser)
	if err != nil || len(res.MediaSkipped) != 2 || !res.Done {
		t.Fatalf("admin_scrub_user = %+v err=%v", res, err)
	}
}

// The account scrub removes the user from the head2head holders document, so
// a recap written after the scrub cannot name the user through its belts.
// A crew that holds the user only in that document is found by the scan.
func TestScrubUserFromHead2head(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	holders := func(crewID string) Head2headDoc {
		return Head2headDoc{CrewID: crewID, Members: []string{"u1", goneUser}, MemberCount: 2,
			Titles: []Head2headTitle{
				{TitleID: titleTheCarry, HolderUserID: goneUser, SinceMs: 1},
				{TitleID: titleNightOwl, HolderUserID: "u1", PreviousHolderUserID: goneUser, SinceMs: 2, MovedAtMs: 2},
			},
			Pairs: []Head2headPair{{A: "u1", B: goneUser, Values: []Head2headPairValue{}}}}
	}
	nk.groups[awayCrew] = &api.Group{Id: awayCrew}
	for _, crewID := range []string{scrubCrew, awayCrew} {
		nk.put(CrewHead2headCollection, head2headKey(crewID), SystemUserID, holders(crewID))
	}
	deleteAccount(t, nk, goneUser)
	for _, crewID := range []string{scrubCrew, awayCrew} {
		doc, _ := readHead2headDoc(context.Background(), nk, crewID)
		raw, _ := json.Marshal(doc)
		if doc == nil || strings.Contains(string(raw), goneUser) {
			t.Fatalf("holders of crew %s = %s", crewID[:4], raw)
		}
		if len(doc.Titles) != 1 || doc.Titles[0].HolderUserID != "u1" || doc.Titles[0].MovedAtMs != 2 || len(doc.Pairs) != 0 {
			t.Fatalf("holders of crew %s = %s", crewID[:4], raw)
		}
		recap := WeeklyRecapData{CrewID: crewID}
		moved, held := buildRecapBelts(doc, 0, time.Now().UnixMilli(), func(id string) string { return id })
		recap.BeltsMoved, recap.BeltsHeld = moved, held
		if out, _ := json.Marshal(recap); strings.Contains(string(out), goneUser) {
			t.Fatalf("a recap after the scrub names the user: %s", out)
		}
	}
}

// Another member of the crew has the display name of the deleted user. The
// fields that hold only that name stay: they can be the other member. Every
// field with the id of the user is scrubbed.
func TestScrubKeepsANameThatAnotherMemberHas(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	nk.addMember(scrubCrew, "u7", strings.ToUpper(goneName))
	ctx := context.Background()
	shared := histRecap(time.UnixMilli(weekStartOf(time.Now().Add(-42*24*time.Hour).UnixMilli())), 10)
	shared.CrewID = scrubCrew
	shared.MostActive = goneName
	shared.LongestSession = goneName + " in Lobby (10m)"
	shared.TopMembers = []RecapMember{{DisplayName: goneName}}
	if err := AppendRecap(ctx, nk, scrubCrew, shared); err != nil {
		t.Fatal(err)
	}
	deleteAccount(t, nk, goneUser)

	var got *WeeklyRecapData
	for _, r := range docRecaps(nk, scrubCrew) {
		if r.WeekStart == shared.WeekStart {
			r := r
			got = &r
		}
	}
	if got == nil || got.MostActive != goneName || got.LongestSession == "" || len(got.TopMembers) != 1 {
		t.Fatalf("the recap with only the shared name = %+v", got)
	}
	// The id of the user is gone everywhere, in the documents and the rows.
	for _, trace := range userTraces(t, nk, store, scrubCrew) {
		if strings.Contains(trace, goneUser) || strings.HasPrefix(trace, "user index") {
			t.Fatalf("still holds the id of the user: %s", trace)
		}
	}
	nk.mu.Lock()
	raw := nk.storage[fakeStorageKey(CrewEventsCollection, scrubCrew, SystemUserID)] + nk.storage[fakeStorageKey(CrewClipsCollection, scrubCrew, SystemUserID)]
	nk.mu.Unlock()
	if strings.Contains(raw, goneName) {
		t.Fatal("an event or a clip still holds the name beside the id of the user")
	}
}

// A new delete attempt starts the wait of the record again: the job must not
// delete the record of an earlier attempt between the two hooks of this one.
func TestScrubRecordOfARetriedDeleteIsNotCleaned(t *testing.T) {
	store := useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	seedScrubCrew(t, nk, f, scrubCrew, true)
	ctx := context.Background()
	// The first attempt, two hours ago: its before hook ran, the delete did not.
	old := &userScrubRecord{UserID: goneUser, StartedMs: time.Now().Add(-2 * time.Hour).UnixMilli(), Crews: []string{scrubCrew}}
	if err := writeUserScrubRecord(ctx, nk, old); err != nil {
		t.Fatal(err)
	}
	// The second attempt.
	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	// The job runs between the two hooks.
	runPendingUserScrubs(ctx, nk, testLogger(), time.Now().Add(time.Minute))
	rec := scrubRecordOf(t, nk)
	if rec == nil || len(rec.Names) == 0 || time.Since(time.UnixMilli(rec.StartedMs)) > time.Minute {
		t.Fatalf("the record of a retried delete = %+v", rec)
	}
	removeAccount(nk, goneUser)
	if err := AfterDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	checkScrubbedCrew(t, nk, store, f, scrubCrew, "after the retried delete")
}

// When the first run is over the record keeps no name. The second run, one
// day later, finds by id an event that a late writer made (a voice session
// that ended after the delete), scrubs it, and deletes the record.
func TestSecondScrubRunWorksWithoutNames(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		seedScrubCrew(t, nk, f, scrubCrew, true)
		deleteAccount(t, nk, goneUser)
		rec := scrubRecordOf(t, nk)
		if rec == nil || rec.DoneMs == 0 || len(rec.Names) != 0 || len(rec.Crews) != 0 {
			t.Fatalf("record after the first run = %+v", rec)
		}
		nk.mu.Lock()
		raw := nk.storage[fakeStorageKey(UserScrubCollection, goneUser, SystemUserID)]
		nk.mu.Unlock()
		if strings.Contains(raw, goneName) {
			t.Fatalf("the record keeps a name after the first run: %s", raw)
		}

		scrubAppend(t, nk, scrubCrew, CrewEvent{ID: "late-voice", Type: "voice_session", Timestamp: time.Now().UnixMilli(),
			Data: VoiceSessionData{ParticipantIDs: []string{"u1", goneUser, "u3"}, ParticipantNames: []string{"Ann", goneName, "Cy"}}})
		if n := len(userTraces(t, nk, store, scrubCrew)); n == 0 {
			t.Fatal("the test needs a late event that names the user")
		}
		// Before the day is over the job does nothing.
		runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
		if n := len(userTraces(t, nk, store, scrubCrew)); n == 0 {
			t.Fatal("the job ran the second run before its time")
		}
		prev := userScrubNow
		userScrubNow = func() time.Time { return time.Now().Add(userScrubSettle + time.Minute) }
		t.Cleanup(func() { userScrubNow = prev })
		runPendingUserScrubs(context.Background(), nk, testLogger(), userScrubNow().Add(time.Minute))
		if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
			t.Fatalf("still names the user after the second run:\n%s", strings.Join(traces, "\n"))
		}
		if inDocs, _ := crewHolds(t, nk, store, scrubCrew, "late-voice-"+scrubCrew[:4]); !inDocs {
			t.Fatal("the late event of the other members is gone")
		}
		if rec := scrubRecordOf(t, nk); rec != nil {
			t.Fatalf("the record stays after the second run: %+v", rec)
		}
	})
}

// A scrub that takes the whole budget of every run of the job, and never
// ends, must not keep the job from the scrub behind it: the job starts after
// the record it handled last.
func TestDeletionJobDoesNotStarveBehindASlowScrub(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	f := useScrubInline(t)
	scrubReady(t, nk, f)
	deletionJobCursor.mu.Lock()
	deletionJobCursor.scrub = ""
	deletionJobCursor.mu.Unlock()
	// A second account that is gone, behind the first in key order.
	const behind = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := confirmUserScrub(context.Background(), nk, behind); err != nil {
		t.Fatal(err)
	}
	// The scrub of the first user is slow: when it reads the ledger of its
	// crew, more time than the budget of the run has passed.
	var offset time.Duration
	prev := userScrubNow
	userScrubNow = func() time.Time { return time.Now().Add(offset) }
	t.Cleanup(func() { userScrubNow = prev })
	nk.onRead[CrewEventsCollection] = func() { offset += 10 * time.Minute }
	doneMs := func(userID string) int64 {
		rec, err := readUserScrubRecord(context.Background(), nk, userID)
		if err != nil || rec == nil {
			t.Fatalf("record of %s = %+v err=%v", userID[:4], rec, err)
		}
		return rec.DoneMs
	}

	runPendingUserScrubs(context.Background(), nk, testLogger(), userScrubNow().Add(time.Minute))
	if doneMs(goneUser) != 0 || doneMs(behind) != 0 {
		t.Fatal("the test needs a first run that ends at the slow scrub")
	}
	runPendingUserScrubs(context.Background(), nk, testLogger(), userScrubNow().Add(time.Minute))
	if doneMs(behind) == 0 {
		t.Fatal("the scrub behind the slow one did not run in the second run of the job")
	}
	if doneMs(goneUser) != 0 {
		t.Fatal("the slow scrub ended: the test proves nothing")
	}
}

// A member posts a clip while the scrub rewrites the clips document. The
// scrub wrote with the version it read, so its write is refused, it reads
// again, and neither the new clip nor the scrub is lost.
func TestScrubDocumentWriteLosesToAConcurrentWriter(t *testing.T) {
	store := useMemLedger(t)
	resetLedgerCutover(t)
	resetHistoryCutover(t)
	nk := newDelNk()
	f := useScrubInline(t)
	rec := scrubReady(t, nk, f)
	tag := scrubCrew[:4]
	fired := false
	nk.onWrite[CrewClipsCollection] = func() {
		if fired {
			return
		}
		fired = true
		if _, err := AppendClip(context.Background(), nk, scrubCrew, StoredClip{EventID: "ev-racing", ClipID: "racing", ActorID: "u3", ClipperName: "Cy", Ts: time.Now().UnixMilli(), ClipType: clipTypeVoice}); err != nil {
			t.Error(err)
		}
	}
	stale := nk.stale
	res := runUserScrub(context.Background(), nk, testLogger(), rec, time.Now().Add(time.Minute))
	if !fired || nk.stale == stale {
		t.Fatalf("no write of the scrub was refused: fired=%t stale=%d", fired, nk.stale-stale)
	}
	if !res.Done {
		t.Fatalf("scrub = %+v", res)
	}
	ids := map[string]bool{}
	for _, c := range docClips(nk, scrubCrew) {
		ids[c.EventID] = true
		if c.ActorID == goneUser {
			t.Fatalf("the document keeps a clip of the user: %+v", c)
		}
	}
	if !ids["ev-racing"] || !ids["ev-shared-"+tag] || len(ids) != 2 {
		t.Fatalf("clips of the document = %v, want the clip of the concurrent writer and the shared clip", ids)
	}
	if traces := userTraces(t, nk, store, scrubCrew); len(traces) != 0 {
		t.Fatalf("still names the user:\n%s", strings.Join(traces, "\n"))
	}
}
