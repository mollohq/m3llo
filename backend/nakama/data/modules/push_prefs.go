package main

// Push switches for each crew (plans/LOOKING-TO-PLAY.md sections 3.8 and 4).
//
// A member has two switches for each crew: Messages and Looking to play. Only
// a choice that the member made is stored. A switch with no stored choice uses
// its default: Looking to play is on, and Messages is on in a crew of up to
// largeCrewMembers members and off in a larger crew.
//
// The switches gate the phone push only. A mention push ignores them.

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	PushPrefsCollection = "push_prefs"
	// pushPrefsKey is the one doc of a user; the crews are inside it.
	pushPrefsKey = "prefs"
	// largeCrewMembers is the size above which a crew counts as large.
	largeCrewMembers = 10
	// pushPrefsReadBatch bounds one StorageRead of several users' docs.
	pushPrefsReadBatch  = 100
	pushPrefsWriteTries = 3
)

// crewPushPrefs holds the choices of a member for one crew. A nil field means
// the member never set that switch.
type crewPushPrefs struct {
	Messages      *bool `json:"messages,omitempty"`
	LookingToPlay *bool `json:"looking_to_play,omitempty"`
}

type pushPrefsDoc struct {
	Crews map[string]crewPushPrefs `json:"crews"`
}

// messagePushOn says if a plain message of the crew may push to the member.
// doc is nil for a member with no stored choice.
func messagePushOn(doc *pushPrefsDoc, crewID string, memberCount int) bool {
	if doc != nil {
		if c, ok := doc.Crews[crewID]; ok && c.Messages != nil {
			return *c.Messages
		}
	}
	return memberCount <= largeCrewMembers
}

// lookingToPlayPushOn says if a Looking to play ask of the crew may push to
// the member, and show its toast and play its sound.
func lookingToPlayPushOn(doc *pushPrefsDoc, crewID string) bool {
	if doc != nil {
		if c, ok := doc.Crews[crewID]; ok && c.LookingToPlay != nil {
			return *c.LookingToPlay
		}
	}
	return true
}

// readPushPrefs reads the docs of several users. A user with no doc is absent
// from the result.
func readPushPrefs(ctx context.Context, nk runtime.NakamaModule, userIDs []string) (map[string]*pushPrefsDoc, error) {
	out := make(map[string]*pushPrefsDoc, len(userIDs))
	for start := 0; start < len(userIDs); start += pushPrefsReadBatch {
		end := start + pushPrefsReadBatch
		if end > len(userIDs) {
			end = len(userIDs)
		}
		reads := make([]*runtime.StorageRead, 0, end-start)
		for _, uid := range userIDs[start:end] {
			reads = append(reads, &runtime.StorageRead{Collection: PushPrefsCollection, Key: pushPrefsKey, UserID: uid})
		}
		objects, err := nk.StorageRead(ctx, reads)
		if err != nil {
			return nil, err
		}
		for _, o := range objects {
			var doc pushPrefsDoc
			if err := json.Unmarshal([]byte(o.GetValue()), &doc); err != nil {
				continue // a broken doc counts as no choice
			}
			out[o.GetUserId()] = &doc
		}
	}
	return out, nil
}

type pushPrefsRequest struct {
	CrewID        string `json:"crew_id"`
	Messages      *bool  `json:"messages,omitempty"`
	LookingToPlay *bool  `json:"looking_to_play,omitempty"`
}

// pushPrefsResponse carries the switches as they apply now, defaults included.
type pushPrefsResponse struct {
	CrewID        string `json:"crew_id"`
	Messages      bool   `json:"messages"`
	LookingToPlay bool   `json:"looking_to_play"`
}

func pushPrefsReply(doc *pushPrefsDoc, crewID string, memberCount int) (string, error) {
	data, err := json.Marshal(pushPrefsResponse{
		CrewID:        crewID,
		Messages:      messagePushOn(doc, crewID, memberCount),
		LookingToPlay: lookingToPlayPushOn(doc, crewID),
	})
	if err != nil {
		return "", runtime.NewError("failed to encode push switches", 13)
	}
	return string(data), nil
}

// pushPrefsCaller checks the caller and the crew of a request. It returns the
// caller and the member count of the crew.
func pushPrefsCaller(ctx context.Context, nk runtime.NakamaModule, payload string) (string, *pushPrefsRequest, int, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", nil, 0, runtime.NewError("authentication required", 16)
	}
	var req pushPrefsRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.CrewID == "" {
		return "", nil, 0, runtime.NewError("crew_id is required", 3)
	}
	members := crewMemberSet(ctx, nk, req.CrewID)
	if !members[userID] {
		return "", nil, 0, runtime.NewError("not a member of this crew", 7)
	}
	return userID, &req, len(members), nil
}

// PushPrefsGetRPC returns the caller's push switches for one crew.
func PushPrefsGetRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, req, memberCount, err := pushPrefsCaller(ctx, nk, payload)
	if err != nil {
		return "", err
	}
	docs, err := readPushPrefs(ctx, nk, []string{userID})
	if err != nil {
		logger.Error("push_prefs_get: read for %s: %v", userID, err)
		return "", runtime.NewError("failed to read push switches", 13)
	}
	return pushPrefsReply(docs[userID], req.CrewID, memberCount)
}

// PushPrefsSetRPC stores the caller's choice for one or both switches of a crew.
func PushPrefsSetRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, req, memberCount, err := pushPrefsCaller(ctx, nk, payload)
	if err != nil {
		return "", err
	}
	if req.Messages == nil && req.LookingToPlay == nil {
		return "", runtime.NewError("messages or looking_to_play is required", 3)
	}

	var doc pushPrefsDoc
	for try := 0; ; try++ {
		doc = pushPrefsDoc{}
		version := "*" // create only
		objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
			Collection: PushPrefsCollection, Key: pushPrefsKey, UserID: userID,
		}})
		if err != nil {
			logger.Error("push_prefs_set: read for %s: %v", userID, err)
			return "", runtime.NewError("failed to read push switches", 13)
		}
		if len(objects) > 0 {
			version = objects[0].GetVersion()
			if err := json.Unmarshal([]byte(objects[0].GetValue()), &doc); err != nil {
				doc = pushPrefsDoc{} // a broken doc is replaced
			}
		}
		if doc.Crews == nil {
			doc.Crews = map[string]crewPushPrefs{}
		}
		c := doc.Crews[req.CrewID]
		if req.Messages != nil {
			c.Messages = req.Messages
		}
		if req.LookingToPlay != nil {
			c.LookingToPlay = req.LookingToPlay
		}
		doc.Crews[req.CrewID] = c

		data, err := json.Marshal(doc)
		if err != nil {
			return "", runtime.NewError("failed to encode push switches", 13)
		}
		_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      PushPrefsCollection,
			Key:             pushPrefsKey,
			UserID:          userID,
			Value:           string(data),
			Version:         version,
			PermissionRead:  0,
			PermissionWrite: 0,
		}})
		if err == nil {
			break
		}
		// The other device of the member wrote first: read again.
		if try+1 >= pushPrefsWriteTries {
			logger.Error("push_prefs_set: write for %s: %v", userID, err)
			return "", runtime.NewError("failed to store push switches", 13)
		}
	}
	logger.Info("push: switches set user=%s crew=%s", userID, req.CrewID)
	return pushPrefsReply(&doc, req.CrewID, memberCount)
}
