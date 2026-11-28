package main

// The inviter block of guest_crew_feed (CREW-INVITES.md section 4.6).
//
// The invite link of a member leads with that member. The block names the
// member and lists the head2head titles that the member holds in the crew
// now. A title held is the only head2head fact that leaves the crew, and only
// for the member whose link it is. The value behind a title stays in the crew.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// guestInviterBadge is one head2head title that the inviter holds. Key is the
// title id and Name is its English label. The struct has no field for the
// value, the unit, a time or a user ID, so the feed cannot carry them.
type guestInviterBadge struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// guestInviter is the member whose invite link the visitor followed. Badges
// is never null: an inviter who holds no title has an empty list.
type guestInviter struct {
	DisplayName string              `json:"display_name"`
	Badges      []guestInviterBadge `json:"badges"`
}

const (
	// guestHoldersTTL is how long the feed keeps the title holders of a crew
	// in memory. The lounge calls guest_crew_feed every few seconds for the
	// voice roster, and the holders doc of a large crew is megabytes
	// (HEAD2HEAD.md section 3). The cache adds at most this time to the age
	// of a badge.
	guestHoldersTTL = 60 * time.Second

	// guestHoldersCacheMax bounds the entries. An entry is 13 short strings at
	// most.
	guestHoldersCacheMax = 4096
)

// guestHoldersEntry is the holder of each contested title of one crew.
type guestHoldersEntry struct {
	holders map[string]string // title id -> holder user id
	readAt  time.Time
}

// guestHolders is in memory, in this process. Each Nakama instance keeps its
// own copy and fills it from storage, so more than one instance is correct.
var guestHolders = struct {
	mu    sync.Mutex
	crews map[string]guestHoldersEntry
}{crews: map[string]guestHoldersEntry{}}

// readTitleHolders reads the holder of each title from the holders doc of the
// crew. It decodes two fields of each title and nothing else: not the values,
// and not the pairs, which are most of the doc. A crew with no doc has no
// holders. ok is false when the read failed.
func readTitleHolders(ctx context.Context, nk runtime.NakamaModule, crewID string) (holders map[string]string, ok bool) {
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{Collection: CrewHead2headCollection, Key: head2headKey(crewID), UserID: SystemUserID},
	})
	if err != nil {
		return nil, false
	}
	holders = map[string]string{}
	if len(objects) == 0 {
		return holders, true
	}
	var doc struct {
		Titles []struct {
			TitleID      string `json:"title_id"`
			HolderUserID string `json:"holder_user_id"`
		} `json:"titles"`
	}
	if json.Unmarshal([]byte(objects[0].GetValue()), &doc) != nil {
		return nil, false
	}
	for _, t := range doc.Titles {
		if t.TitleID != "" && t.HolderUserID != "" {
			holders[t.TitleID] = t.HolderUserID
		}
	}
	return holders, true
}

// crewTitleHolders returns the title holders of the crew, from memory when
// the entry is under guestHoldersTTL old. It never computes head2head: a crew
// with no holders doc has no holders until the crew feed builds the doc. A
// failed read is not kept, so the next call reads again.
func crewTitleHolders(ctx context.Context, nk runtime.NakamaModule, crewID string, now time.Time) map[string]string {
	guestHolders.mu.Lock()
	entry, hit := guestHolders.crews[crewID]
	guestHolders.mu.Unlock()
	if hit && now.Sub(entry.readAt) < guestHoldersTTL {
		return entry.holders
	}

	holders, ok := readTitleHolders(ctx, nk, crewID)
	if !ok {
		return nil
	}

	guestHolders.mu.Lock()
	if len(guestHolders.crews) >= guestHoldersCacheMax {
		for id, e := range guestHolders.crews {
			if now.Sub(e.readAt) >= guestHoldersTTL {
				delete(guestHolders.crews, id)
			}
		}
		if len(guestHolders.crews) >= guestHoldersCacheMax {
			guestHolders.crews = map[string]guestHoldersEntry{}
		}
	}
	guestHolders.crews[crewID] = guestHoldersEntry{holders: holders, readAt: now}
	guestHolders.mu.Unlock()
	return holders
}

// inviterBadges lists the titles that the user holds, in the display order of
// the client card (ownValueOrder). The result is never nil.
func inviterBadges(holders map[string]string, userID string) []guestInviterBadge {
	out := []guestInviterBadge{}
	if userID == "" {
		return out
	}
	for _, id := range ownValueOrder {
		if holders[id] != userID {
			continue
		}
		if name := head2headTitleLabels[id]; name != "" {
			out = append(out, guestInviterBadge{Key: id, Name: name})
		}
	}
	return out
}

// userShownName is the name the feed shows for a user.
func userShownName(u *api.User) string {
	if name := u.GetDisplayName(); name != "" {
		return name
	}
	return u.GetUsername()
}

// inviterMemberName returns the name of the inviter when the inviter is the
// owner, an admin or a member of the crew now. A user who left, a join
// request and a banned user are not members.
//
// members is the first page of the member list, which the feed reads anyway.
// When wholeList is true the page is every row of the crew, so it answers the
// question with no more reads. Else the function asks for the state of the
// inviter in the crew, and for the account.
func inviterMemberName(ctx context.Context, nk runtime.NakamaModule, crewID, inviterUserID string, members []*api.GroupUserList_GroupUser, wholeList bool) (string, bool) {
	for _, m := range members {
		if m.GetUser().GetId() != inviterUserID {
			continue
		}
		if !isMemberState(m.GetState().GetValue()) {
			return "", false
		}
		name := userShownName(m.GetUser())
		return name, name != ""
	}
	if wholeList {
		return "", false
	}

	state, err := callerCrewState(ctx, nk, inviterUserID, crewID)
	if err != nil || state == nil || !isMemberState(int32(*state)) {
		return "", false
	}
	users, err := nk.UsersGetId(ctx, []string{inviterUserID}, nil)
	if err != nil || len(users) == 0 {
		return "", false
	}
	name := userShownName(users[0])
	return name, name != ""
}

// guestInviterFor builds the inviter block of the feed. It returns nil for a
// link with no inviter (a custom invite name, an old code) and for an inviter
// who is not a member of the crew now. The feed then names no inviter.
//
// There is no consent check and no setting: sharing the link is what puts the
// titles on its page.
func guestInviterFor(ctx context.Context, nk runtime.NakamaModule, crewID, inviterUserID string, members []*api.GroupUserList_GroupUser, wholeList bool, now time.Time) *guestInviter {
	if inviterUserID == "" {
		return nil
	}
	name, isMember := inviterMemberName(ctx, nk, crewID, inviterUserID, members, wholeList)
	if !isMember {
		return nil
	}
	return &guestInviter{
		DisplayName: name,
		Badges:      inviterBadges(crewTitleHolders(ctx, nk, crewID, now), inviterUserID),
	}
}
