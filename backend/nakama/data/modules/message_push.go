package main

// Plain message push (plans/LOOKING-TO-PLAY.md section 4).
//
// A crew chat message sends a phone push to the members who cannot see it:
// every member but the sender, with the Messages switch of the crew on
// (push_prefs.go), by the activity rule of the mention push (spec 23 §6.2).
// A member that the message mentions gets the mention push and not this one.
//
// One crew has one notification on the lock screen. Each push of the crew
// carries the same collapse id, so it replaces the one before. The first
// message goes out at once. More messages in the next messagePushWindow leave
// as one push when the window ends.
//
// Push is best-effort: every failure is logged and never reaches the chat path.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	pushTypeMessage = "message"
	// messagePushWindow is the shortest time between two message pushes of one
	// crew to one member.
	messagePushWindow = 30 * time.Second
	// messagePushWorkers bounds the sends that run at the same time, so a
	// message in a crew of 50 does not open 49 requests at once.
	messagePushWorkers = 8
)

// messagePushKey is the limiter key of one member and one crew. A user id is
// a UUID and has no "|".
func messagePushKey(userID, crewID string) string { return userID + "|" + crewID }

func userOfMessagePushKey(key string) string {
	if i := strings.IndexByte(key, '|'); i >= 0 {
		return key[:i]
	}
	return key
}

// messageCollapseID is the collapse id of every message push of a crew. It
// goes in message_id, as the collapse ids of the event pushes do.
func messageCollapseID(crewID string) string { return "msg-" + shortPushID(crewID) }

// messagePushRecipients returns the members that a message may push to: every
// member but the sender and the members who get the mention push. The order
// is stable.
func messagePushRecipients(members map[string]bool, senderID string, mentioned []string) []string {
	skip := map[string]bool{senderID: true}
	for _, id := range mentioned {
		skip[id] = true
	}
	out := make([]string, 0, len(members))
	for id, ok := range members {
		if ok && id != "" && !skip[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// messagePushBody is the lock screen line of a message. text is the body with
// the mention tokens resolved.
func messagePushBody(senderName, msgType, text string) string {
	if strings.TrimSpace(text) == "" {
		who := senderName
		if who == "" {
			who = "Someone"
		}
		switch msgType {
		case "gif":
			return truncateRunes(who+" sent a GIF", pushBodyMaxRunes)
		case "image":
			return truncateRunes(who+" sent an image", pushBodyMaxRunes)
		}
	}
	return pushBody(senderName, text)
}

// queueMessagePushes starts the push fan-out for a plain chat message. It
// returns at once: the chat hook must not wait on storage or the Worker.
func queueMessagePushes(logger runtime.Logger, nk runtime.NakamaModule, senderID, senderName, crewID, content string) {
	env, ok := messagePushEnvelope(content)
	if !ok {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("push: message fan-out panic for crew %s: %v", crewID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), pushFanoutTimeout)
		defer cancel()
		deliverMessagePushes(ctx, logger, nk, senderID, senderName, crewID, env)
	}()
}

// messagePushEnvelope parses a channel message. It returns false for content
// that is not a text, GIF or image message of a member: a signal, a system
// message, or an envelope that does not parse.
func messagePushEnvelope(content string) (*messageEnvelope, bool) {
	var env messageEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		return nil, false
	}
	if env.Type != "text" && env.Type != "gif" && env.Type != "image" {
		return nil, false
	}
	return &env, true
}

func deliverMessagePushes(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, senderID, senderName, crewID string, env *messageEnvelope) {
	members := crewMemberSet(ctx, nk, crewID)
	var mentioned []string
	if env.Type == "text" {
		mentioned = mentionPushTargets(env.Mentions, senderID) // they get the mention push
	}

	// The activity rule first: it reads memory only, and most members of an
	// active crew are at the desktop.
	actions := map[string]pushAction{}
	var candidates []string
	for _, uid := range messagePushRecipients(members, senderID, mentioned) {
		if action := pushDecisionFor(uid); action != pushSkip {
			actions[uid] = action
			candidates = append(candidates, uid)
		}
	}
	if len(candidates) == 0 {
		return
	}

	prefs, err := readPushPrefs(ctx, nk, candidates)
	if err != nil {
		// Without the switches a muted member could get a push: send nothing.
		logger.Warn("push: read switches for crew %s: %v", crewID, err)
		return
	}
	var now, later []string
	for _, uid := range candidates {
		if !messagePushOn(prefs[uid], crewID, len(members)) {
			continue
		}
		if actions[uid] == pushNow {
			now = append(now, uid)
		} else {
			later = append(later, uid)
		}
	}
	logger.Debug("push: message crew=%s members=%d now=%d later=%d", crewID, len(members), len(now), len(later))
	if len(now) == 0 && len(later) == 0 {
		return
	}

	channelID, err := nk.ChannelIdBuild(ctx, "", crewID, runtime.Group)
	if err != nil {
		logger.Warn("push: channel id for crew %s: %v", crewID, err)
		return
	}
	n := pushAlert{
		Type:      pushTypeMessage,
		CrewID:    crewID,
		ChannelID: channelID,
		MessageID: messageCollapseID(crewID),
		Title:     truncateRunes(pushCrewName(ctx, nk, crewID), pushTitleMaxRunes),
		Body:      messagePushBody(senderName, env.Type, resolveMentionTokens(ctx, nk, env.Body)),
	}
	for _, uid := range now {
		submitPush(uid, n)
	}
	for _, uid := range later {
		schedulePushAfterGrace(logger, nk, uid, n)
	}
}

// submitPush passes a push to the limiter of its type.
func submitPush(userID string, n pushAlert) {
	if n.Type == pushTypeMessage {
		messagePushes.submit(messagePushKey(userID, n.CrewID), n)
		return
	}
	mentionPushes.submit(userID, n)
}

// messagePushSlots holds one slot for each send that runs.
var messagePushSlots = make(chan struct{}, messagePushWorkers)

// messagePushSend sends one message push to the devices of a member. It
// returns at once. Tests replace it to see what leaves the limiter.
var messagePushSend = func(userID string, a pushAlert) {
	go func() {
		messagePushSlots <- struct{}{}
		defer func() { <-messagePushSlots }()
		defer func() {
			if r := recover(); r != nil && globalLogger != nil {
				globalLogger.Error("push: message send panic for user %s: %v", userID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), pushFanoutTimeout)
		defer cancel()
		sendPushToUser(ctx, globalLogger, globalNk, userID, a)
	}()
}

// newMessagePushLimiter builds the limiter in front of every message push. Its
// key is one member and one crew, so a busy crew does not hold back another.
func newMessagePushLimiter(now func() time.Time, after func(time.Duration, func())) *pushLimiter {
	return newPushLimiter(messagePushWindow, "messages", now, after,
		func(key string, a pushAlert) { messagePushSend(userOfMessagePushKey(key), a) })
}

var messagePushes = newMessagePushLimiter(time.Now, func(d time.Duration, f func()) { afterFunc(d, f) })
