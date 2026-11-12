package main

// Friend landed push: a member joins a crew (event_push.go has the shared
// queue, quiet hours and coalescing).
//
//   - The second to fifth member: every other member gets
//     "<Name> joined <Crew>. Say hi." One push per joiner.
//   - From the sixth member: "<Name> joined <Crew>." The first goes at once.
//     Joiners inside crewJoinPushWindow of the last send leave as one push,
//     "<Name> and <m> others joined <Crew>."
//   - The joiner never gets a push.

import (
	"context"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	crewJoinPushType = "crew_join"
	crewJoinPushKey  = "join_" // queue doc key prefix, then the crew id

	// crewJoinSayHiMax is the largest crew size that gets "Say hi".
	crewJoinSayHiMax = 5

	// crewJoinPushWindow is the minimum time between two coalesced join
	// pushes to a member.
	crewJoinPushWindow = time.Hour

	// crewJoinPushKeepMs is how long a sent push without a group stays in the
	// queue doc.
	crewJoinPushKeepMs = 24 * 60 * 60 * 1000
)

// crewJoinPushBody is the line of a join push in the language lang. names
// holds the joiners, the newest last. An empty name shows as "A new member".
// sayHi adds "Say hi." and applies to a single joiner only.
func crewJoinPushBody(lang string, names []string, crew string, sayHi bool) string {
	if len(names) == 0 {
		return ""
	}
	last := names[len(names)-1]
	if last == "" {
		last = pushText(lang, "join_name_fallback")
	}
	var s string
	switch more := len(names) - 1; {
	case more == 0 && sayHi:
		s = pushText(lang, "join_one_say_hi", last, crew)
	case more == 0:
		s = pushText(lang, "join_one", last, crew)
	case more == 1:
		s = pushText(lang, "join_one_other", last, crew)
	default:
		s = pushText(lang, "join_others", last, more, crew)
	}
	return truncateRunes(s, pushBodyMaxRunes)
}

// crewJoinMessageID is the collapse id: one per crew and day.
func crewJoinMessageID(crewID string, now time.Time) string {
	return "join-" + shortPushID(crewID) + "-" + now.UTC().Format("20060102")
}

// addCrewJoinPushes folds one joiner into the queue of a crew. members is the
// crew after the join, the joiner included. langs maps a member to the account
// language of the body (nil: English). It returns whether the doc changed.
func addCrewJoinPushes(doc *RecapPushDoc, crewID, crewName, joinerID, joinerName string, members []string, tzs map[string]*int, langs map[string]string, now time.Time) bool {
	nowMs := now.UnixMilli()
	// Drop old sent pushes that need no memory. A coalescing item stays: it
	// holds the time of its last send.
	kept := doc.Items[:0]
	for _, it := range doc.Items {
		if it.Sent && it.Group == "" && nowMs-it.SendAtMs > crewJoinPushKeepMs {
			continue
		}
		kept = append(kept, it)
	}
	doc.Items = kept

	sayHi := len(members) <= crewJoinSayHiMax
	changed := false
	for _, uid := range members {
		if uid == joinerID {
			continue
		}
		changed = true
		if sayHi {
			doc.Items = append(doc.Items, RecapPushItem{
				UserID: uid, Type: crewJoinPushType, MessageID: crewJoinMessageID(crewID, now),
				Body:     crewJoinPushBody(langs[uid], []string{joinerName}, crewName, true),
				SendAtMs: pushQuietAdjust(now, tzs[uid]).UnixMilli(),
			})
			continue
		}
		var it *RecapPushItem
		for i := range doc.Items {
			if doc.Items[i].UserID == uid && doc.Items[i].Group == crewJoinPushType {
				it = &doc.Items[i]
			}
		}
		if it == nil {
			doc.Items = append(doc.Items, RecapPushItem{UserID: uid, Group: crewJoinPushType, Type: crewJoinPushType, Sent: true})
			it = &doc.Items[len(doc.Items)-1]
		}
		if it.Sent {
			it.Sent = false
			it.Events = nil
		}
		it.Events = append(it.Events, pushEvent{Name: joinerName})
		names := make([]string, len(it.Events))
		for i, e := range it.Events {
			names[i] = e.Name
		}
		it.Body = crewJoinPushBody(langs[uid], names, crewName, false)
		it.MessageID = crewJoinMessageID(crewID, now)
		it.SendAtMs = pushSendAt(now, it.LastSentMs, crewJoinPushWindow, tzs[uid]).UnixMilli()
	}
	return changed
}

// queueCrewJoinPush queues the pushes for a member who joined crewID. Never
// fails the caller: push is best-effort.
func queueCrewJoinPush(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, crewID, joinerID string) {
	ids, err := crewMemberIDs(ctx, nk, crewID)
	if err != nil {
		return
	}
	joined := false
	for _, id := range ids {
		joined = joined || id == joinerID
	}
	// A join request that is not approved yet is no membership. The first
	// member is the creator: nobody is there to say hi.
	if !joined || len(ids) < 2 {
		return
	}
	// One batched read gives the joiner's name and the account language of
	// each member. An empty name shows as "A new member" in each language.
	names, langs := pushUsers(ctx, nk, ids)
	name := names[joinerID]
	crewName := pushCrewName(ctx, nk, crewID)
	tzs := map[string]*int{}
	for uid, a := range readAllUserActivityStats(ctx, nk, ids) {
		tzs[uid] = pushTzOffset(a)
	}
	key := crewJoinPushKey + crewID
	now := eventPushClock()
	if updatePushQueue(ctx, nk, logger, key, crewID, func(q *RecapPushDoc) bool {
		return addCrewJoinPushes(q, crewID, crewName, joinerID, name, ids, tzs, langs, now)
	}) {
		logger.Info("crew join push: queued for crew %s (%d members)", crewID, len(ids))
		flushPushQueueAsync(nk, logger, key)
	}
}
