package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/heroiclabs/nakama-common/api"
)

// The scrub of a deleted account outside the ledger
// (specs/16-CREW-EVENT-LEDGER.md section 10.2): the quests record of a crew,
// the crew emoji, the queued pushes of a crew and the invite codes. The rule
// functions are pure. crewScrub.leftovers applies them.

// scrubQuestsForUser removes the user from the quests record of a crew. No
// step is taken back: a step that is done stays done, with its time.
//
//   - owner_id: the id of the owner the crew has now (newOwner), or empty.
//     The client opens the gifts for the member that owner_id names, and the
//     friend step skips that member.
//   - friend: the id and the name of the friend are cleared, and the inviter.
//   - call, session: the user leaves the id list and the name list.
//   - clip: the clipper is cleared. The clip itself is deleted by the scrub
//     of the clips.
//   - plus_granted: the user leaves the list.
func scrubQuestsForUser(q CrewQuests, who scrubWho, newOwner string) (CrewQuests, bool) {
	if who.UserID == "" {
		return q, false
	}
	changed := false
	if q.OwnerID == who.UserID {
		q.OwnerID = newOwner
		changed = true
	}
	if f := q.Steps.Friend; f != nil && (f.UserID == who.UserID || f.InvitedBy == who.UserID) {
		out := *f
		if out.UserID == who.UserID {
			out.UserID, out.Name = "", ""
		}
		if out.InvitedBy == who.UserID {
			out.InvitedBy, out.InvitedByName = "", ""
		}
		q.Steps.Friend = &out
		changed = true
	}
	if c := q.Steps.Call; c != nil {
		if ids, names, did := scrubNamesAligned(c.UserIDs, c.Names, who); did {
			out := *c
			out.UserIDs, out.Names = ids, names
			q.Steps.Call = &out
			changed = true
		}
	}
	if c := q.Steps.Clip; c != nil && c.ClipperID == who.UserID {
		out := *c
		out.ClipperID, out.ClipperName = "", ""
		q.Steps.Clip = &out
		changed = true
	}
	if s := q.Steps.Session; s != nil {
		if ids, names, did := scrubNamesAligned(s.UserIDs, s.Names, who); did {
			out := *s
			out.UserIDs, out.Names = ids, names
			q.Steps.Session = &out
			changed = true
		}
	}
	kept := q.PlusGranted[:0:0]
	for _, id := range q.PlusGranted {
		if id != who.UserID {
			kept = append(kept, id)
		}
	}
	if len(kept) != len(q.PlusGranted) {
		q.PlusGranted = kept
		changed = true
	}
	return q, changed
}

// scrubEmojiForUser clears who added an emoji. The emoji is of the crew: it
// stays. With no added_by, only the owner can remove it.
func scrubEmojiForUser(d CrewEmojiDoc, userID string) (CrewEmojiDoc, bool) {
	if userID == "" {
		return d, false
	}
	changed := false
	out := make([]CrewEmoji, len(d.Emoji))
	copy(out, d.Emoji)
	for i := range out {
		if out[i].AddedBy == userID {
			out[i].AddedBy, out[i].AddedByName = "", ""
			changed = true
		}
	}
	if !changed {
		return d, false
	}
	d.Emoji = out
	return d, true
}

// scrubPushQueueForUser removes from a push queue of a crew the items for
// the user, and the items whose text names the user. An item is removed
// whole, sent or not: a text is not made again without the user.
func scrubPushQueueForUser(doc RecapPushDoc, who scrubWho) (RecapPushDoc, bool) {
	if who.UserID == "" {
		return doc, false
	}
	names := func(it RecapPushItem) bool {
		if who.holdsName(it.Body) {
			return true
		}
		for _, ev := range it.Events {
			if who.isName(ev.Name) {
				return true
			}
		}
		return false
	}
	kept := doc.Items[:0:0]
	for _, it := range doc.Items {
		if it.UserID == who.UserID || names(it) {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(doc.Items) {
		return doc, false
	}
	doc.Items = kept
	return doc, true
}

// scrubInviteCodeForUser clears the inviter of an invite code. The code
// stays: it is the invite link of the crew, and other people hold it. A join
// with the code then names no inviter.
func scrubInviteCodeForUser(value, userID string) (out string, changed bool, err error) {
	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(value), &doc); err != nil {
		return "", false, err
	}
	if inviter, _ := doc["inviter_user_id"].(string); userID == "" || inviter != userID {
		return "", false, nil
	}
	delete(doc, "inviter_user_id")
	data, err := json.Marshal(doc)
	return string(data), true, err
}

// crewOwnerNow returns a superadmin the crew has now, the one who joined
// first, or "" when the crew has none.
func (c *crewScrub) crewOwnerNow() (string, error) {
	owners, err := crewMembersInState(c.ctx, c.nk, c.crewID, groupStateSuperadmin, 2)
	if err != nil {
		return "", err
	}
	for _, id := range owners {
		if id != c.rec.UserID {
			return id, nil
		}
	}
	return "", nil
}

// errGroupChanged is the error of a group update that was not made: the
// group changed between the two reads. The next run of the scrub tries again.
var errGroupChanged = errors.New("the group changed during the scrub")

// groupCreatorPlan says what a group update must change to remove the user
// from a group: creator_id, and created_by in the metadata. needed is false
// for a group that does not name the user. metadata is nil when the metadata
// stays.
func groupCreatorPlan(g *api.Group, userID string) (needsCreator bool, metadata map[string]interface{}, err error) {
	if raw := g.GetMetadata(); raw != "" {
		var doc map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return false, nil, err
		}
		if by, _ := doc["created_by"].(string); by == userID {
			delete(doc, "created_by")
			metadata = doc
		}
	}
	return g.GetCreatorId() == userID, metadata, nil
}

// groupCreator removes the user from the group of the crew: creator_id, and
// created_by in the metadata. Nakama keeps both when the account is deleted
// and the group stays. creator_id becomes the owner the crew has now: the
// group update of Nakama cannot clear it. created_by is removed.
//
// The group update of Nakama (GroupUpdate) has no version check. An empty
// name, description, avatar, language and creator stay as they are, and so
// does a nil metadata. The open flag is always written, and a metadata that
// is given replaces the whole metadata (verified on Nakama 3.21.0,
// dev_delete_selftest). So the update can undo a change of the open flag or
// of the metadata that another writer makes at the same time. To keep that
// time short the group is read again right before the update: when the open
// flag, the metadata or the creator are not the same, nothing is written.
func (c *crewScrub) groupCreator() error {
	userID := c.rec.UserID
	read := func() (*api.Group, error) {
		groups, err := c.nk.GroupsGetId(c.ctx, []string{c.crewID})
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g.GetId() == c.crewID {
				return g, nil
			}
		}
		return nil, nil
	}
	g, err := read()
	if err != nil || g == nil {
		return err
	}
	needsCreator, metadata, err := groupCreatorPlan(g, userID)
	if err != nil {
		return err
	}
	creator := ""
	if needsCreator {
		if creator, err = c.crewOwnerNow(); err != nil {
			return err
		}
	}
	if creator == "" && metadata == nil {
		return nil
	}
	now, err := read()
	if err != nil {
		return err
	}
	if now == nil || now.GetOpen().GetValue() != g.GetOpen().GetValue() || now.GetMetadata() != g.GetMetadata() || now.GetCreatorId() != g.GetCreatorId() {
		return errGroupChanged
	}
	if err := c.nk.GroupUpdate(c.ctx, c.crewID, "", "", creator, "", "", "", now.GetOpen().GetValue(), metadata, 0); err != nil {
		return err
	}
	c.res.Changed++
	return nil
}

// leftovers scrubs what names the user outside the ledger: the group of the
// crew, the quests record, the crew emoji and the three push queues. exists
// says whether the crew is a group.
func (c *crewScrub) leftovers(exists bool) error {
	who := c.who()
	if exists && c.rec.DeletedMs > 0 {
		if err := c.groupCreator(); err != nil {
			return fmt.Errorf("group update: %w", err)
		}
	}
	err := scrubDoc(c.ctx, c.nk, CrewQuestsCollection, c.crewID, func(value string) (string, bool, error) {
		var q CrewQuests
		if err := json.Unmarshal([]byte(value), &q); err != nil {
			return "", false, err
		}
		newOwner := ""
		if exists && q.OwnerID == who.UserID {
			var err error
			if newOwner, err = c.crewOwnerNow(); err != nil {
				return "", false, fmt.Errorf("owner read: %w", err)
			}
		}
		out, changed := scrubQuestsForUser(q, who, newOwner)
		if !changed {
			return "", false, nil
		}
		c.res.Changed++
		data, err := json.Marshal(out)
		return string(data), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewQuestsCollection, err)
	}
	err = scrubDoc(c.ctx, c.nk, CrewEmojiCollection, c.crewID, func(value string) (string, bool, error) {
		var d CrewEmojiDoc
		if err := json.Unmarshal([]byte(value), &d); err != nil {
			return "", false, err
		}
		out, changed := scrubEmojiForUser(d, who.UserID)
		if !changed {
			return "", false, nil
		}
		c.res.Changed++
		data, err := json.Marshal(out)
		return string(data), true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", CrewEmojiCollection, err)
	}
	for _, key := range []string{c.crewID, beltPushKey + c.crewID, crewJoinPushKey + c.crewID} {
		err := scrubDoc(c.ctx, c.nk, RecapPushCollection, key, func(value string) (string, bool, error) {
			var doc RecapPushDoc
			if err := json.Unmarshal([]byte(value), &doc); err != nil {
				return "", false, err
			}
			out, changed := scrubPushQueueForUser(doc, who)
			if !changed {
				return "", false, nil
			}
			c.res.Changed++
			data, err := json.Marshal(out)
			return string(data), true, err
		})
		if err != nil {
			return fmt.Errorf("%s/%s: %w", RecapPushCollection, key, err)
		}
	}
	return nil
}
