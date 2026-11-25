package main

import (
	"context"
	"testing"
)

// crew_emoji_remove answers a member of the crew only. The fixture is in
// crew_gifts_test.go.

// leaveGiftsCrew takes the user out of the member list of the crew.
func leaveGiftsCrew(nk *fakeQuestsNk, crewID, userID string) {
	kept := nk.members[crewID][:0]
	for _, m := range nk.members[crewID] {
		if m.GetUser().GetId() != userID {
			kept = append(kept, m)
		}
	}
	nk.members[crewID] = kept
}

func emojiNames(t *testing.T, nk *fakeQuestsNk, crewID string) []string {
	t.Helper()
	d, _ := readCrewEmoji(context.Background(), nk, crewID)
	names := make([]string, 0, len(d.Emoji))
	for _, e := range d.Emoji {
		names = append(names, e.Name)
	}
	return names
}

// A member who left the crew does not remove the emoji that it added.
func TestCrewEmojiRemove_RefusesAMemberWhoLeft(t *testing.T) {
	nk := giftsCrew(t)
	markStep(t, nk, "gc", func(q *CrewQuests) { q.Steps.Call = &QuestCall{} })
	if _, err := addEmoji(t, nk, "ida", "gg"); err != nil {
		t.Fatal(err)
	}
	leaveGiftsCrew(nk, "gc", "ida")

	out, err := rpcT(t, CrewEmojiRemoveRPC, nk, "ida", `{"crew_id":"gc","name":"gg"}`)

	if err != errNotAMember {
		t.Fatalf("crew_emoji_remove by a member who left: out=%q err=%v, want NOT_A_MEMBER", out, err)
	}
	if names := emojiNames(t, nk, "gc"); len(names) != 1 || names[0] != "gg" {
		t.Fatalf("the emoji of the crew after a refused remove: %v, want [gg]", names)
	}
}

// An account outside the crew gets one answer for a name that is an emoji of
// the crew and for a name that is not.
func TestCrewEmojiRemove_AnswersAnOutsiderTheSameForEveryName(t *testing.T) {
	nk := giftsCrew(t)
	markStep(t, nk, "gc", func(q *CrewQuests) { q.Steps.Call = &QuestCall{} })
	if _, err := addEmoji(t, nk, "ida", "gg"); err != nil {
		t.Fatal(err)
	}

	_, errIs := rpcT(t, CrewEmojiRemoveRPC, nk, "outsider", `{"crew_id":"gc","name":"gg"}`)
	_, errIsNot := rpcT(t, CrewEmojiRemoveRPC, nk, "outsider", `{"crew_id":"gc","name":"nope"}`)

	if errIs != errNotAMember || errIsNot != errNotAMember {
		t.Fatalf("a name of the crew: %v. another name: %v. Want NOT_A_MEMBER for both", errIs, errIsNot)
	}
	if names := emojiNames(t, nk, "gc"); len(names) != 1 {
		t.Fatalf("the emoji of the crew after two refused removes: %v", names)
	}
}
