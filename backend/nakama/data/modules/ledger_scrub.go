package main

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The scrub rules of a deleted account (plans/DURABLE-LEDGER.md section 7).
// Every function of this file is pure: no storage, no clock.
//
//   - An event the user started is dropped: the user is the actor or the
//     streamer.
//   - In an event another member started, the id and the name of the user
//     leave the participant, player and viewer lists, and invited_by is
//     cleared. Lists that run in parallel stay aligned.
//   - A voice session left with fewer than two participants is dropped.
//   - A weekly recap is never dropped. Every field that names the user is
//     cleared (scrubRecapForUser).

// scrubWho is the user of a scrub.
//
// Names are the names of the user that a field with a name and no id is
// matched against: the display name and the username the account had at the
// delete. A recap adds the name it holds beside the id of the user
// (scrubRecapForUser). Names the user had before are not used: nothing says
// that they are names of this user alone.
//
// Taken are the display names and usernames of the members the crew has now.
// A name that another member also has names two people, so a field with only
// that name is not matched: it stays. A field with the id of the user is
// always matched.
//
// ClipIDs are the clip_ids of the clips of the user that the scrub drops.
type scrubWho struct {
	UserID  string
	Names   []string
	Taken   []string
	ClipIDs map[string]bool
}

// taken reports whether another member of the crew has the name.
func (w scrubWho) taken(name string) bool {
	for _, t := range w.Taken {
		if t != "" && strings.EqualFold(t, name) {
			return true
		}
	}
	return false
}

// isName reports whether text is a name of the user. A recap holds the user
// id in a name field when the name did not resolve (resolveOrID).
func (w scrubWho) isName(text string) bool {
	if text == "" {
		return false
	}
	if text == w.UserID {
		return true
	}
	for _, n := range w.Names {
		if n != "" && n == text && !w.taken(n) {
			return true
		}
	}
	return false
}

// holdsName reports whether a free text holds a name of the user as a whole
// name: the text before and after it is not a letter or a digit. The name
// "Al" is not in "Alex" or in "Sal".
func (w scrubWho) holdsName(text string) bool {
	if text == "" {
		return false
	}
	if w.UserID != "" && holdsWholeName(text, w.UserID) {
		return true
	}
	for _, n := range w.Names {
		if n != "" && !w.taken(n) && holdsWholeName(text, n) {
			return true
		}
	}
	return false
}

// holdsWholeName reports whether text holds name with no letter or digit
// right before it or right after it.
func holdsWholeName(text, name string) bool {
	if name == "" {
		return false
	}
	wordRune := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for from := 0; from <= len(text)-len(name); {
		i := strings.Index(text[from:], name)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(name)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (start == 0 || !wordRune(before)) && (end == len(text) || !wordRune(after)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		from = start + size
	}
	return false
}

// withName returns who with one more name.
func (w scrubWho) withName(name string) scrubWho {
	if name == "" || name == w.UserID {
		return w
	}
	for _, n := range w.Names {
		if n == name {
			return w
		}
	}
	names := make([]string, 0, len(w.Names)+1)
	names = append(names, w.Names...)
	w.Names = append(names, name)
	return w
}

// scrubEventForUser applies the scrub rules to one event of the legacy ledger
// path. drop is true when the event must be deleted. Otherwise out is the
// event without the user. An event that does not name the user comes back
// unchanged.
func scrubEventForUser(ev CrewEvent, userID string) (out CrewEvent, drop bool) {
	return scrubEventFor(ev, scrubWho{UserID: userID})
}

// scrubEventFor is scrubEventForUser with the names of the user. The names
// matter only for a name list that is not aligned with its id list.
func scrubEventFor(ev CrewEvent, who scrubWho) (CrewEvent, bool) {
	if who.UserID == "" {
		return ev, false
	}
	if ev.ActorID == who.UserID {
		return ev, true
	}
	raw, err := json.Marshal(ev.Data)
	if err != nil {
		return ev, false
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		// A payload that is not an object names nobody but the actor.
		return ev, false
	}
	if id, _ := obj["streamer_id"].(string); id == who.UserID {
		return ev, true
	}
	typ, _ := ledgerReaderType(ev.Type)
	if typ == ledgerTypeWeeklyRecap {
		// A recap event that an old ledger document holds.
		var r WeeklyRecapData
		if err := json.Unmarshal(raw, &r); err != nil {
			return ev, false
		}
		scrubbed, changed := scrubRecapForUser(r, who)
		if !changed {
			return ev, false
		}
		ev.Data = scrubbed
		return ev, false
	}

	changed := false
	inVoice := scrubListsOfObject(obj, who, "participant_ids", "participant_names")
	changed = changed || inVoice
	changed = scrubListsOfObject(obj, who, "player_ids", "player_names", "player_overlap_min") || changed
	changed = scrubListsOfObject(obj, who, "viewer_ids") || changed
	// The clip events that an old ledger document holds.
	changed = scrubListsOfObject(obj, who, "participants", "participant_names") || changed
	if id, _ := obj["invited_by"].(string); id == who.UserID {
		delete(obj, "invited_by")
		delete(obj, "invited_by_name")
		changed = true
	}
	if !changed {
		return ev, false
	}
	if typ == "voice_session" && inVoice {
		if ids, _ := obj["participant_ids"].([]interface{}); len(ids) < 2 {
			return ev, true
		}
	}
	ev.Data = obj
	return ev, false
}

// scrubListsOfObject removes the user from the id list idsKey of a JSON
// object, and the entries at the same places from the lists that run in
// parallel. It reports whether the user was in the id list.
//
// A parallel list that has another length than the id list is not aligned.
// A name list then loses the entries that are a name of the user. When none
// is, it is emptied: one of its names can be the name of the user. Any other
// list that is not aligned is removed.
func scrubListsOfObject(obj map[string]interface{}, who scrubWho, idsKey string, parallelKeys ...string) bool {
	ids, ok := obj[idsKey].([]interface{})
	if !ok {
		return false
	}
	gone := map[int]bool{}
	for i, v := range ids {
		if id, _ := v.(string); id == who.UserID {
			gone[i] = true
		}
	}
	if len(gone) == 0 {
		return false
	}
	without := func(list []interface{}) []interface{} {
		out := make([]interface{}, 0, len(list))
		for i, v := range list {
			if !gone[i] {
				out = append(out, v)
			}
		}
		return out
	}
	for _, key := range parallelKeys {
		list, ok := obj[key].([]interface{})
		if !ok {
			continue
		}
		switch {
		case len(list) == len(ids):
			obj[key] = without(list)
		case strings.HasSuffix(key, "_names"):
			kept := make([]interface{}, 0, len(list))
			for _, v := range list {
				if name, _ := v.(string); !who.isName(name) {
					kept = append(kept, v)
				}
			}
			if len(kept) == len(list) {
				kept = []interface{}{}
			}
			obj[key] = kept
		default:
			delete(obj, key)
		}
	}
	obj[idsKey] = without(ids)
	return true
}

// scrubNamesAligned removes the user from an id list and from the name list
// beside it, by the rule of scrubListsOfObject.
func scrubNamesAligned(ids, names []string, who scrubWho) (outIDs, outNames []string, changed bool) {
	gone := map[int]bool{}
	for i, id := range ids {
		if id == who.UserID {
			gone[i] = true
		}
	}
	if len(gone) == 0 {
		return ids, names, false
	}
	outIDs = make([]string, 0, len(ids))
	for i, id := range ids {
		if !gone[i] {
			outIDs = append(outIDs, id)
		}
	}
	outNames = make([]string, 0, len(names))
	if len(names) == len(ids) {
		for i, n := range names {
			if !gone[i] {
				outNames = append(outNames, n)
			}
		}
		return outIDs, outNames, true
	}
	for _, n := range names {
		if !who.isName(n) {
			outNames = append(outNames, n)
		}
	}
	if len(outNames) == len(names) {
		outNames = outNames[:0]
	}
	return outIDs, outNames, true
}

// scrubClipForUser applies the scrub rules to one stored clip. drop is true
// for a clip the user made. A clip of another member that holds the voice of
// the user stays: only the id and the name of the user leave its participant
// lists. post_clip skips a participant with no name, so the two lists can
// have different lengths (scrubListsOfObject says what then happens).
func scrubClipForUser(c StoredClip, who scrubWho) (out StoredClip, drop bool) {
	if who.UserID == "" {
		return c, false
	}
	if c.ActorID == who.UserID {
		return c, true
	}
	ids, names, changed := scrubNamesAligned(c.Participants, c.ParticipantNames, who)
	if changed {
		c.Participants, c.ParticipantNames = ids, names
	}
	return c, false
}

// scrubStreamSessionForUser applies the scrub rules to one stream session of
// the durable copy. The copy names one user: the streamer.
func scrubStreamSessionForUser(s StoredStreamSession, who scrubWho) (out StoredStreamSession, drop bool) {
	return s, who.UserID != "" && s.StreamerID == who.UserID
}

// scrubRecapForUser removes the user from a stored weekly recap. A recap is
// never dropped. The totals of the crew stay: they name nobody.
//
// The fields that name a user, and what happens to each:
//
//	top_members, game_records,   the entry of the user is removed (by user_id;
//	belts_held, leaderboard      by name when the entry has no id)
//	most_clipped,                cleared, all three, when the id is the user,
//	most_clipped_user_id, _count or the name is and there is no id
//	most_active                  cleared when it is a name of the user
//	best_streak_name,            cleared, both, when the name is a name of
//	best_streak                  the user
//	awards                       an award whose name is a name of the user is
//	                             removed. A belt award that says "took it
//	                             from" the user loses that detail
//	belts_moved                  a move to the user is removed. A move from
//	                             the user loses previous_holder_user_id and
//	                             previous_holder_name
//	clip_of_week                 removed when it is a clip of the user (by
//	                             clip_id of a dropped clip, or clipper_name).
//	                             Otherwise the name of the user leaves
//	                             participant_names
//	longest_session              the text is cleared when it holds a name of
//	                             the user
//
// A recap resolves all its names at one time. So the name beside the id of
// the user in this recap is also matched in the fields that hold only a name.
// A name that another member of the crew has now is never matched alone
// (scrubWho.Taken): a field with that name and no id stays.
func scrubRecapForUser(r WeeklyRecapData, who scrubWho) (out WeeklyRecapData, changed bool) {
	if who.UserID == "" {
		return r, false
	}
	// The names the user has in this recap.
	for _, m := range r.TopMembers {
		if m.UserID == who.UserID {
			who = who.withName(m.DisplayName)
		}
	}
	for _, g := range r.GameRecords {
		if g.UserID == who.UserID {
			who = who.withName(g.DisplayName)
		}
	}
	for _, list := range [][]RecapBeltHeld{r.BeltsHeld, r.Leaderboard} {
		for _, h := range list {
			if h.UserID == who.UserID {
				who = who.withName(h.DisplayName)
			}
		}
	}
	for _, m := range r.BeltsMoved {
		if m.HolderUserID == who.UserID {
			who = who.withName(m.HolderName)
		}
		if m.PreviousUserID == who.UserID {
			who = who.withName(m.PreviousName)
		}
	}
	if r.MostClippedUserID == who.UserID {
		who = who.withName(r.MostClipped)
	}
	// is reports whether an entry with an id and a name is the user.
	is := func(id, name string) bool {
		return id == who.UserID || (id == "" && who.isName(name))
	}

	if len(r.TopMembers) > 0 {
		kept := make([]RecapMember, 0, len(r.TopMembers))
		for _, m := range r.TopMembers {
			if !is(m.UserID, m.DisplayName) {
				kept = append(kept, m)
			}
		}
		changed = changed || len(kept) != len(r.TopMembers)
		r.TopMembers = kept
	}
	if len(r.GameRecords) > 0 {
		kept := make([]RecapGameRecord, 0, len(r.GameRecords))
		for _, g := range r.GameRecords {
			if !is(g.UserID, g.DisplayName) {
				kept = append(kept, g)
			}
		}
		changed = changed || len(kept) != len(r.GameRecords)
		r.GameRecords = kept
	}
	held := func(list []RecapBeltHeld) []RecapBeltHeld {
		if len(list) == 0 {
			return list
		}
		kept := make([]RecapBeltHeld, 0, len(list))
		for _, h := range list {
			if !is(h.UserID, h.DisplayName) {
				kept = append(kept, h)
			}
		}
		changed = changed || len(kept) != len(list)
		return kept
	}
	r.BeltsHeld = held(r.BeltsHeld)
	r.Leaderboard = held(r.Leaderboard)

	if len(r.BeltsMoved) > 0 {
		kept := make([]RecapBeltMoved, 0, len(r.BeltsMoved))
		for _, m := range r.BeltsMoved {
			if is(m.HolderUserID, m.HolderName) {
				changed = true
				continue
			}
			if m.PreviousUserID == who.UserID || (m.PreviousUserID == "" && who.isName(m.PreviousName)) {
				m.PreviousUserID, m.PreviousName = "", ""
				changed = true
			}
			kept = append(kept, m)
		}
		r.BeltsMoved = kept
	}
	if len(r.Awards) > 0 {
		kept := make([]RecapAward, 0, len(r.Awards))
		for _, a := range r.Awards {
			if who.isName(a.Name) {
				changed = true
				continue
			}
			if a.Kind == "belt" && who.holdsName(a.Detail) {
				a.Detail = ""
				changed = true
			}
			kept = append(kept, a)
		}
		r.Awards = kept
	}
	if is(r.MostClippedUserID, r.MostClipped) && (r.MostClippedUserID != "" || r.MostClipped != "") {
		r.MostClipped, r.MostClippedUserID, r.MostClippedCount = "", "", 0
		changed = true
	}
	if who.isName(r.MostActive) {
		r.MostActive = ""
		changed = true
	}
	if who.isName(r.BestStreakName) {
		r.BestStreakName, r.BestStreak = "", 0
		changed = true
	}
	if who.holdsName(r.LongestSession) {
		r.LongestSession = ""
		changed = true
	}
	if c := r.ClipOfWeek; c != nil {
		if who.ClipIDs[c.ClipID] || who.isName(c.ClipperName) {
			r.ClipOfWeek = nil
			changed = true
		} else {
			kept := make([]string, 0, len(c.ParticipantNames))
			for _, n := range c.ParticipantNames {
				if !who.isName(n) {
					kept = append(kept, n)
				}
			}
			if len(kept) != len(c.ParticipantNames) {
				clip := *c
				clip.ParticipantNames = kept
				r.ClipOfWeek = &clip
				changed = true
			}
		}
	}
	return r, changed
}

// recapUserIDs lists the user ids a weekly recap holds, for the user index of
// the store (eventUserIDs).
func recapUserIDs(r WeeklyRecapData) []string {
	ids := []string{r.MostClippedUserID}
	for _, m := range r.TopMembers {
		ids = append(ids, m.UserID)
	}
	for _, g := range r.GameRecords {
		ids = append(ids, g.UserID)
	}
	for _, m := range r.BeltsMoved {
		ids = append(ids, m.HolderUserID, m.PreviousUserID)
	}
	for _, h := range r.BeltsHeld {
		ids = append(ids, h.UserID)
	}
	for _, h := range r.Leaderboard {
		ids = append(ids, h.UserID)
	}
	return ids
}

// scrubHead2headForUser removes the user from the head2head holders document
// of a crew. The weekly recap reads this document (attachRecapBelts), so a
// recap written after the scrub would name the user again.
//
//   - A title the user holds is removed from the document. The title is then
//     vacant. The next recompute gives it to a member as a first award: it
//     has no previous holder, so no recap and no push says that it moved.
//   - A title another member took from the user loses
//     previous_holder_user_id. The time of the move stays.
//   - The user leaves members, and every pair of the user is removed.
func scrubHead2headForUser(doc Head2headDoc, userID string) (out Head2headDoc, changed bool) {
	if userID == "" {
		return doc, false
	}
	members := make([]string, 0, len(doc.Members))
	for _, id := range doc.Members {
		if id != userID {
			members = append(members, id)
		}
	}
	titles := make([]Head2headTitle, 0, len(doc.Titles))
	for _, t := range doc.Titles {
		if t.HolderUserID == userID {
			changed = true
			continue
		}
		if t.PreviousHolderUserID == userID {
			t.PreviousHolderUserID = ""
			changed = true
		}
		titles = append(titles, t)
	}
	pairs := make([]Head2headPair, 0, len(doc.Pairs))
	for _, p := range doc.Pairs {
		if p.A != userID && p.B != userID {
			pairs = append(pairs, p)
		}
	}
	if len(members) != len(doc.Members) || len(pairs) != len(doc.Pairs) {
		changed = true
	}
	if !changed {
		return doc, false
	}
	doc.Members, doc.Titles, doc.Pairs = members, titles, pairs
	return doc, true
}
