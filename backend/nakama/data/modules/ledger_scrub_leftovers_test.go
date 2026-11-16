package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestScrubQuestsForUser(t *testing.T) {
	who := scrubWho{UserID: goneUser, Names: []string{goneName}}
	q := CrewQuests{
		CrewID: scrubCrew, OwnerID: goneUser, MadeMs: 1, EndsMs: 2,
		Steps: CrewQuestsSteps{
			Friend:  &QuestFriend{DoneMs: 10, UserID: "u1", Name: "Ann", InvitedBy: goneUser, InvitedByName: goneName},
			Call:    &QuestCall{DoneMs: 20, UserIDs: []string{"u1", goneUser, "u3"}, Names: []string{"Ann", goneName, "Cy"}, ChannelID: "ch"},
			Clip:    &QuestClip{DoneMs: 30, ClipID: "c1", ClipType: "voice", ClipperID: goneUser, ClipperName: goneName},
			Session: &QuestSession{DoneMs: 40, GameName: "Game", Minutes: 30, UserIDs: []string{goneUser, "u1"}, Names: []string{goneName, "Ann"}},
		},
		PlusGranted: []string{"u1", goneUser, "u3"},
	}
	out, changed := scrubQuestsForUser(q, who, "u1")
	raw, _ := json.Marshal(out)
	if !changed || strings.Contains(string(raw), goneUser) || strings.Contains(string(raw), goneName) {
		t.Fatalf("scrubbed quests = %s", raw)
	}
	s := out.Steps
	if out.OwnerID != "u1" || out.MadeMs != 1 || out.EndsMs != 2 {
		t.Fatalf("record = %s", raw)
	}
	// Every step stays done, with its time and what names no one.
	if s.Friend == nil || s.Friend.DoneMs != 10 || s.Friend.UserID != "u1" || s.Friend.Name != "Ann" ||
		s.Call == nil || s.Call.DoneMs != 20 || strings.Join(s.Call.UserIDs, ",") != "u1,u3" || strings.Join(s.Call.Names, ",") != "Ann,Cy" || s.Call.ChannelID != "ch" ||
		s.Clip == nil || s.Clip.DoneMs != 30 || s.Clip.ClipID != "c1" ||
		s.Session == nil || s.Session.DoneMs != 40 || strings.Join(s.Session.UserIDs, ",") != "u1" || strings.Join(s.Session.Names, ",") != "Ann" || s.Session.Minutes != 30 ||
		strings.Join(out.PlusGranted, ",") != "u1,u3" {
		t.Fatalf("steps = %s", raw)
	}
	// The input is not changed.
	if q.Steps.Call.UserIDs[1] != goneUser || q.Steps.Friend.InvitedBy != goneUser || len(q.PlusGranted) != 3 {
		t.Fatal("the scrub changed its input")
	}

	// The friend is the user: the step stays done and names no one.
	q2 := CrewQuests{OwnerID: "u1", Steps: CrewQuestsSteps{Friend: &QuestFriend{DoneMs: 10, UserID: goneUser, Name: goneName, InvitedBy: "u1", InvitedByName: "Ann"}}}
	out, changed = scrubQuestsForUser(q2, who, "")
	if f := out.Steps.Friend; !changed || f == nil || f.DoneMs != 10 || f.UserID != "" || f.Name != "" || f.InvitedBy != "u1" || out.OwnerID != "u1" {
		t.Fatalf("friend = %+v", out.Steps.Friend)
	}
	// A done friend step is not set again by the next join.
	if setQuestFriend(&out.Steps, out.OwnerID, QuestFriend{UserID: "u5"}) {
		t.Fatal("the friend step was set again")
	}
	// With no owner now, owner_id is empty.
	if out, _ := scrubQuestsForUser(q, who, ""); out.OwnerID != "" {
		t.Fatalf("owner_id = %q", out.OwnerID)
	}
	// A record that does not name the user is not changed.
	if _, changed := scrubQuestsForUser(out, who, "u1"); changed {
		t.Fatal("a record without the user changed")
	}
}

func TestScrubEmojiForUser(t *testing.T) {
	d := CrewEmojiDoc{CrewID: scrubCrew, Emoji: []CrewEmoji{
		{ID: "e1", Name: "gg", URL: "u", Key: "k1", AddedBy: goneUser, AddedByName: goneName, AddedMs: 5},
		{ID: "e2", Name: "ez", URL: "u", Key: "k2", AddedBy: "u1", AddedByName: "Ann", AddedMs: 6},
	}}
	out, changed := scrubEmojiForUser(d, goneUser)
	if !changed || len(out.Emoji) != 2 || out.Emoji[0].AddedBy != "" || out.Emoji[0].AddedByName != "" || out.Emoji[0].Name != "gg" || out.Emoji[0].Key != "k1" || out.Emoji[0].AddedMs != 5 {
		t.Fatalf("emoji = %+v", out.Emoji)
	}
	if out.Emoji[1] != d.Emoji[1] || d.Emoji[0].AddedBy != goneUser {
		t.Fatalf("the emoji of another member changed, or the input: %+v", out.Emoji)
	}
	if _, changed := scrubEmojiForUser(out, goneUser); changed {
		t.Fatal("a document without the user changed")
	}
}

func TestScrubPushQueueForUser(t *testing.T) {
	who := scrubWho{UserID: goneUser, Names: []string{"Zed"}}
	doc := RecapPushDoc{CrewID: scrubCrew, Week: "2026-W40", Items: []RecapPushItem{
		{UserID: goneUser, Body: "Week 40 recap. Ann leads with 2."},
		{UserID: "u1", Body: "Zed took Night Owl. You hold 1."},
		{UserID: "u3", Body: "You took Night Owl from Zed. You hold 2.", Sent: true},
		{UserID: "u1", Body: "Zedd took Night Owl. You hold 1."},
		{UserID: "u3", Body: "2 belts moved.", Events: []pushEvent{{Name: "Zed", TitleID: "night_owl"}, {Name: "Ann"}}},
		{UserID: "u1", Body: "Week 40 recap. Ann leads with 2."},
	}}
	out, changed := scrubPushQueueForUser(doc, who)
	if !changed || len(out.Items) != 2 || out.Items[0].Body != "Zedd took Night Owl. You hold 1." || out.Items[1].UserID != "u1" || out.Week != "2026-W40" {
		t.Fatalf("queue = %+v", out.Items)
	}
	if len(doc.Items) != 6 {
		t.Fatal("the scrub changed its input")
	}
	if _, changed := scrubPushQueueForUser(out, who); changed {
		t.Fatal("a queue without the user changed")
	}
	// A name that another member has now names two people: the text stays.
	// The item for the user goes by its id.
	who.Taken = []string{"zed"}
	out, _ = scrubPushQueueForUser(doc, who)
	if len(out.Items) != 5 || out.Items[0].UserID != "u1" {
		t.Fatalf("queue with a taken name = %+v", out.Items)
	}
}

func TestScrubInviteCodeForUser(t *testing.T) {
	out, changed, err := scrubInviteCodeForUser(`{"crew_id":"`+scrubCrew+`","inviter_user_id":"`+goneUser+`"}`, goneUser)
	if err != nil || !changed || out != `{"crew_id":"`+scrubCrew+`"}` {
		t.Fatalf("code = %s changed=%t err=%v", out, changed, err)
	}
	for _, value := range []string{`{"crew_id":"` + scrubCrew + `","inviter_user_id":"u1"}`, `{"crew_id":"` + goneUser + `"}`} {
		if _, changed, err := scrubInviteCodeForUser(value, goneUser); err != nil || changed {
			t.Fatalf("code %s changed=%t err=%v", value, changed, err)
		}
	}
}

// leftoverDocs are the documents outside the ledger that can name a user:
// collection and key.
func leftoverDocs(crewID string) [][2]string {
	return [][2]string{
		{CrewQuestsCollection, crewID}, {CrewEmojiCollection, crewID},
		{RecapPushCollection, crewID}, {RecapPushCollection, "belt_" + crewID}, {RecapPushCollection, "join_" + crewID},
	}
}

// seedLeftovers gives a crew a quests record, emoji and push queues that
// name the user, and two invite codes.
func seedLeftovers(nk *delNk, crewID string) {
	tag := strings.ToUpper(crewID[:4])
	nk.put(CrewQuestsCollection, crewID, SystemUserID, CrewQuests{
		CrewID: crewID, OwnerID: goneUser, MadeMs: 1, EndsMs: 2,
		Steps: CrewQuestsSteps{
			Friend: &QuestFriend{DoneMs: 10, UserID: "u1", Name: "Ann", InvitedBy: goneUser, InvitedByName: goneName},
			Call:   &QuestCall{DoneMs: 20, UserIDs: []string{"u1", goneUser}, Names: []string{"Ann", goneName}},
		},
		PlusGranted: []string{goneUser, "u1"},
	})
	nk.put(CrewEmojiCollection, crewID, SystemUserID, CrewEmojiDoc{CrewID: crewID, Emoji: []CrewEmoji{
		{ID: "e1", Name: "gg", AddedBy: goneUser, AddedByName: goneName}, {ID: "e2", Name: "ez", AddedBy: "u1", AddedByName: "Ann"},
	}})
	nk.put(RecapPushCollection, crewID, SystemUserID, RecapPushDoc{CrewID: crewID, Items: []RecapPushItem{
		{UserID: goneUser, Body: "Week 40 recap. Ann leads with 2."}, {UserID: "u1", Body: goneName + " took Night Owl. You hold 1."}, {UserID: "u3", Body: "Week 40 recap. Ann leads with 2."},
	}})
	nk.put(RecapPushCollection, "belt_"+crewID, SystemUserID, RecapPushDoc{CrewID: crewID, Items: []RecapPushItem{
		{UserID: "u1", Body: "2 belts moved.", Events: []pushEvent{{Name: goneName}}}, {UserID: goneUser, Body: "Ann took Night Owl."},
	}})
	nk.put(RecapPushCollection, "join_"+crewID, SystemUserID, RecapPushDoc{CrewID: crewID, Items: []RecapPushItem{
		{UserID: "u1", Body: goneName + " joined Crew."}, {UserID: "u3", Body: "Cy joined Crew."},
	}})
	// Invite codes are readable by all (read 2), as GenerateInviteCode
	// writes them.
	for code, inviter := range map[string]string{"GONE" + tag: goneUser, "ANNS" + tag: "u1"} {
		data, _ := json.Marshal(map[string]string{"crew_id": crewID, "inviter_user_id": inviter})
		if _, err := nk.StorageWrite(context.Background(), []*runtime.StorageWrite{{
			Collection: InviteCodeCollection, Key: code, UserID: SystemUserID, Value: string(data), PermissionRead: 2, PermissionWrite: 0,
		}}); err != nil {
			panic(err)
		}
	}
}

// leftoverTraces lists the documents outside the ledger that still name the
// user.
func leftoverTraces(nk *delNk, crewID string) []string {
	nk.mu.Lock()
	defer nk.mu.Unlock()
	var out []string
	for k, v := range nk.storage {
		if strings.HasPrefix(k, UserScrubCollection+"/") {
			continue
		}
		if strings.Contains(k, crewID) || strings.Contains(v, crewID) {
			for _, d := range append(leftoverDocs(crewID), [2]string{InviteCodeCollection, ""}) {
				if strings.HasPrefix(k, d[0]+"/") && (strings.Contains(v, goneUser) || strings.Contains(v, goneName)) {
					out = append(out, k)
					break
				}
			}
		}
	}
	return out
}

// An account delete removes the user from the documents outside the ledger:
// of a crew the user is in, and of a crew the user left before. The invite
// code stays, with no inviter.
func TestAccountDeleteScrubsTheLeftovers(t *testing.T) {
	forDelPhases(t, func(t *testing.T, nk *delNk, store *memLedgerStore) {
		f := useScrubInline(t)
		seedScrubCrew(t, nk, f, scrubCrew, true)
		// The user left this crew before the delete. Its ledger names the
		// user, so the scrub finds the crew.
		seedScrubCrew(t, nk, f, leftCrew, false)
		seedLeftovers(nk, scrubCrew)
		seedLeftovers(nk, leftCrew)
		seedPurgeCrew(t, nk, f, otherCrew)
		before := crewLeftovers(t, nk, store, f, otherCrew)
		for _, crewID := range []string{scrubCrew, leftCrew} {
			if n := len(leftoverTraces(nk, crewID)); n != 6 {
				t.Fatalf("seed of %s: %d documents name the user, want 6", crewID, n)
			}
		}

		deleteAccount(t, nk, goneUser)

		for _, crewID := range []string{scrubCrew, leftCrew} {
			if left := leftoverTraces(nk, crewID); len(left) != 0 {
				t.Fatalf("documents of %s that name the user: %v", crewID, left)
			}
			tag := strings.ToUpper(crewID[:4])
			var q CrewQuests
			if found, _, err := readDocStrict(context.Background(), nk, CrewQuestsCollection, crewID, &q); err != nil || !found {
				t.Fatalf("quests of %s: found=%t err=%v", crewID, found, err)
			}
			// The owner the crew has now. Every step stays done.
			if q.OwnerID != "u1" || q.Steps.Friend == nil || q.Steps.Friend.UserID != "u1" || q.Steps.Call == nil || len(q.Steps.Call.UserIDs) != 1 || len(q.PlusGranted) != 1 {
				t.Fatalf("quests of %s = %+v", crewID, q)
			}
			var e CrewEmojiDoc
			if _, _, err := readDocStrict(context.Background(), nk, CrewEmojiCollection, crewID, &e); err != nil || len(e.Emoji) != 2 || e.Emoji[1].AddedBy != "u1" {
				t.Fatalf("emoji of %s = %+v err=%v", crewID, e, err)
			}
			var push RecapPushDoc
			if _, _, err := readDocStrict(context.Background(), nk, RecapPushCollection, "join_"+crewID, &push); err != nil || len(push.Items) != 1 || push.Items[0].UserID != "u3" {
				t.Fatalf("join queue of %s = %+v err=%v", crewID, push, err)
			}
			// The code of the user still joins the crew, and names no one.
			// The code of another member is as it was.
			if gotCrew, inviter, err := lookupInviteCode(context.Background(), nk, "GONE"+tag); err != nil || gotCrew != crewID || inviter != "" {
				t.Fatalf("invite code of the user: crew=%q inviter=%q err=%v", gotCrew, inviter, err)
			}
			if _, inviter, err := lookupInviteCode(context.Background(), nk, "ANNS"+tag); err != nil || inviter != "u1" {
				t.Fatalf("invite code of another member: inviter=%q err=%v", inviter, err)
			}
			nk.mu.Lock()
			perms := nk.perms[fakeStorageKey(InviteCodeCollection, "GONE"+tag, SystemUserID)]
			nk.mu.Unlock()
			if perms != [2]int{2, 0} {
				t.Fatalf("permissions of the invite code = %v", perms)
			}
		}
		checkScrubbedCrew(t, nk, store, f, scrubCrew, "after the delete")
		if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs == 0 || rec.LastError != "" {
			t.Fatalf("scrub record = %+v", rec)
		}
		// The scan reads the invite codes once. The second run, one day
		// later, reads the ledger documents again and not the codes. The
		// documents of a crew outside the ledger are never listed.
		nk.mu.Lock()
		lists := fmt.Sprint(nk.lists[InviteCodeCollection], nk.lists[CrewEventsCollection], nk.lists[CrewQuestsCollection], nk.lists[CrewEmojiCollection], nk.lists[RecapPushCollection])
		nk.mu.Unlock()
		if lists != "1 1 0 0 0" {
			t.Fatalf("lists of the first run = %s", lists)
		}
		prev := userScrubNow
		userScrubNow = func() time.Time { return time.Now().Add(userScrubSettle + time.Minute) }
		t.Cleanup(func() { userScrubNow = prev })
		runPendingUserScrubs(context.Background(), nk, testLogger(), userScrubNow().Add(time.Minute))
		if rec := scrubRecordOf(t, nk); rec != nil {
			t.Fatalf("the scrub record stays after the second run: %+v", rec)
		}
		nk.mu.Lock()
		lists = fmt.Sprint(nk.lists[InviteCodeCollection], nk.lists[CrewEventsCollection])
		nk.mu.Unlock()
		if lists != "1 2" {
			t.Fatalf("lists after the second run = %s", lists)
		}
		if after := crewLeftovers(t, nk, store, f, otherCrew); strings.Join(after, "\n") != strings.Join(before, "\n") {
			t.Fatalf("another crew changed: %v", after)
		}
	})
}

// After a handover the quests record names the new owner.
func TestHandoverMovesTheQuestsOwner(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	useScrubInline(t)
	nk.groups[scrubCrew] = &api.Group{Id: scrubCrew}
	nk.addMemberAs(scrubCrew, goneUser, goneName, 0)
	nk.addMemberAs(scrubCrew, "u1", "Ann", 2)
	nk.addMemberAs(scrubCrew, "u3", "Cy", 1)
	seedLeftovers(nk, scrubCrew)
	// The group of a crew names who made it, twice.
	nk.groups[scrubCrew] = &api.Group{Id: scrubCrew, CreatorId: goneUser, Open: wrapperspb.Bool(true),
		Metadata: `{"created_by":"` + goneUser + `","invite_only":false,"max_members":10000}`}
	nk.groups[otherCrew] = &api.Group{Id: otherCrew, CreatorId: "u1", Metadata: `{"created_by":"u1"}`}
	nk.addMemberAs(otherCrew, "u1", "Ann", 0)
	nk.addMemberAs(otherCrew, goneUser, goneName, 2)
	memEdgesOf(t, nk)
	mustDeleteAccount(t, nk, goneUser)
	g := nk.groups[scrubCrew]
	if g.GetCreatorId() != "u3" || g.GetMetadata() != `{"invite_only":false,"max_members":10000}` || !g.GetOpen().GetValue() {
		t.Fatalf("group = creator %q metadata %s open %t", g.GetCreatorId(), g.GetMetadata(), g.GetOpen().GetValue())
	}
	// A group that another member made is not updated.
	if nk.groupUpdates != 1 || nk.groups[otherCrew].GetCreatorId() != "u1" {
		t.Fatalf("group updates = %d", nk.groupUpdates)
	}
	var q CrewQuests
	if _, _, err := readDocStrict(context.Background(), nk, CrewQuestsCollection, scrubCrew, &q); err != nil || q.OwnerID != "u3" {
		t.Fatalf("owner_id = %q err=%v, want the promoted admin", q.OwnerID, err)
	}
	if left := leftoverTraces(nk, scrubCrew); len(left) != 0 {
		t.Fatalf("documents that name the user: %v", left)
	}
}

// The group update has no version check. A group that another writer changes
// between the two reads of the scrub is not written: the change stays, and
// the next run removes the user.
func TestGroupCreatorSkipsAGroupThatChanged(t *testing.T) {
	useMemLedger(t)
	nk := newDelNk()
	useScrubInline(t)
	memEdgesOf(t, nk)
	nk.groups[scrubCrew] = &api.Group{Id: scrubCrew, CreatorId: goneUser, Open: wrapperspb.Bool(true),
		Metadata: `{"created_by":"` + goneUser + `","invite_policy":"admins"}`}
	nk.addMemberAs(scrubCrew, goneUser, goneName, 0)
	nk.addMemberAs(scrubCrew, "u1", "Ann", 1)
	if err := BeforeDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	nakamaRemoveAccount(nk, goneUser)
	// The new owner closes the crew and changes a policy while the scrub is
	// between its two reads of the group in groupCreator. Before them it
	// reads the group twice: for the scrub and for the owner repair.
	changed := `{"created_by":"` + goneUser + `","invite_policy":"everyone"}`
	reads := 0
	nk.onGroupRead = func() {
		if reads++; reads == 4 {
			nk.groups[scrubCrew].Open = wrapperspb.Bool(false)
			nk.groups[scrubCrew].Metadata = changed
		}
	}
	if err := AfterDeleteAccount(ctxWithUser(goneUser), testLogger(), nil, nk); err != nil {
		t.Fatal(err)
	}
	g := nk.groups[scrubCrew]
	if nk.groupUpdates != 0 || g.GetOpen().GetValue() || g.GetMetadata() != changed {
		t.Fatalf("the scrub wrote over the change: updates=%d open=%t metadata=%s", nk.groupUpdates, g.GetOpen().GetValue(), g.GetMetadata())
	}
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs != 0 || len(rec.Crews) != 1 {
		t.Fatalf("scrub record = %+v", rec)
	}
	// The next run removes the user and keeps the change.
	nk.onGroupRead = nil
	runPendingUserScrubs(context.Background(), nk, testLogger(), time.Now().Add(time.Minute))
	g = nk.groups[scrubCrew]
	if g.GetCreatorId() != "u1" || g.GetOpen().GetValue() || g.GetMetadata() != `{"invite_policy":"everyone"}` {
		t.Fatalf("group = creator %q open %t metadata %s", g.GetCreatorId(), g.GetOpen().GetValue(), g.GetMetadata())
	}
	if rec := scrubRecordOf(t, nk); rec == nil || rec.DoneMs == 0 {
		t.Fatalf("scrub record = %+v", rec)
	}
}
