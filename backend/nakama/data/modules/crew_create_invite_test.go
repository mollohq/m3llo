package main

import (
	"testing"
)

// create_crew sends the crew_invite notification only to a user who shares a
// crew with the caller. The fake is in crew_quests_test.go.

// notifyCrewInvite is the code of the crew_invite notification.
const notifyCrewInvite = 200

// crewInvitesTo returns the crew_invite notifications that the user got.
func crewInvitesTo(userID string) []sentNote {
	feedNotesMu.Lock()
	defer feedNotesMu.Unlock()
	var out []sentNote
	for _, n := range feedNotes {
		if n.userID == userID && n.code == notifyCrewInvite {
			out = append(out, n)
		}
	}
	return out
}

func clearSentNotes() {
	feedNotesMu.Lock()
	feedNotes = nil
	feedNotesMu.Unlock()
}

func TestCreateCrew_InvitesOnlyAUserWhoSharesACrewWithTheCaller(t *testing.T) {
	nk := newFakeQuestsNk()
	nk.addMember("old-crew", "own", "Owner")
	nk.addMember("old-crew", "mate", "Mate")
	// A join request in the crew of the caller is no membership.
	nk.addMember("old-crew", "requester", "Requester")
	nk.setRole("old-crew", "requester", int(groupStateJoinRequest))
	nk.addMember("other-crew", "stranger", "Stranger")
	clearSentNotes()

	if _, err := CreateCrewRPC(ctxWithUser("own"), testLogger(), nil, nk,
		`{"name":"new_crew_1","invite_user_ids":["mate","stranger","requester","own","no-such-user"]}`); err != nil {
		t.Fatal(err)
	}

	got := crewInvitesTo("mate")
	if len(got) != 1 || got[0].content["crew_id"] != "g-new_crew_1" || got[0].content["crew_name"] != "new_crew_1" {
		t.Fatalf("the crew mate got %+v, want one crew_invite for the new crew", got)
	}
	for _, id := range []string{"stranger", "requester", "own", "no-such-user"} {
		if n := crewInvitesTo(id); len(n) != 0 {
			t.Errorf("%s shares no crew with the caller and got %d crew_invite notifications", id, len(n))
		}
	}
}

// A join request of the caller in a crew is no membership: the members of
// that crew are no crew mates of the caller.
func TestCreateCrew_AJoinRequestOfTheCallerSharesNoCrew(t *testing.T) {
	nk := newFakeQuestsNk()
	nk.addMember("big-crew", "target", "Target")
	nk.addMember("big-crew", "asker", "Asker")
	nk.setRole("big-crew", "asker", int(groupStateJoinRequest))
	clearSentNotes()

	if _, err := CreateCrewRPC(ctxWithUser("asker"), testLogger(), nil, nk,
		`{"name":"new_crew_2","invite_user_ids":["target"]}`); err != nil {
		t.Fatal(err)
	}

	if n := crewInvitesTo("target"); len(n) != 0 {
		t.Fatalf("a member of a crew that the caller only asked to join got %d crew_invite notifications", len(n))
	}
}
