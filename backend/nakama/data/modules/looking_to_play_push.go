package main

// Looking to play: the phone pushes of an ask (specs/features/LOOKING-TO-PLAY.md
// sections 3.4 and 3.8).
//
// | Push        | To                              | When                          |
// | New ask     | the members the ask goes to     | at the ask                    |
// | Answer      | the asker                       | a member is in                |
// | Second call | asked members with no answer    | two of the ask are in voice   |
// | Nobody      | the asker                       | the ask closes with nobody in |
// | Called off  | the members who are in          | the asker cancels             |
//
// A member with an active session gets no phone push: the client shows the
// toast from notification 118. An ask never waits for the desktop grace
// period. All pushes of one ask have one collapse id, so each replaces the one
// before on the lock screen, and they expire when the ask closes.
//
// Push is best-effort: every failure is logged and never reaches the caller.

import (
	"context"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// pushTypeLtp is a push with the answer buttons: New ask and Second call.
	pushTypeLtp = "looking_to_play"
	// pushTypeLtpResult is a push with no buttons.
	pushTypeLtpResult = "looking_to_play_result"
	// ltpPushCategory is the notification category that the phone app
	// registers with the four answers.
	ltpPushCategory = "LOOKING_TO_PLAY"
	// ltpMinTTL keeps a push alive for a phone that is briefly off the net.
	ltpMinTTL = 60 * time.Second
)

// ltpCollapseID is the collapse id of every push of an ask.
func ltpCollapseID(askID string) string { return "ltp-" + shortPushID(askID) }

// ltpPushTTL is the time left until the ask closes, in seconds.
func ltpPushTTL(a *ltpAsk, nowMs int64) int {
	window := ltpOpenWindow
	if a.State == ltpStateOn {
		window = ltpOnWindow
	}
	left := time.Duration(a.OpenedAt+window.Milliseconds()-nowMs) * time.Millisecond
	if left < ltpMinTTL {
		left = ltpMinTTL
	}
	return int(left / time.Second)
}

// ltpHasGame says if a member has the game. A member with no library report
// is not known: the answer is yes, so the member gets the push.
func ltpHasGame(lib *LibraryDoc, gameID string) bool {
	if lib == nil {
		return true
	}
	for _, g := range lib.Games {
		if g.GameID == gameID {
			return true
		}
	}
	return false
}

// ltpAudience picks the members who get the New ask and the Second call of an
// ask, before the Looking to play setting. others holds the members that the
// ask goes to. In a large crew, an ask to everyone that has a game goes to
// the members who have the game (section 3.8).
//
// When no library names the game, the libraries tell nothing about it: the
// game is not one that a launcher reports, or the client sent the id of the
// game catalogue ("counter-strike-2") and not a library id ("steam-730").
// The ask then goes to every member, as an ask with no game does.
func ltpAudience(a *ltpAsk, others []string, memberCount int, libs map[string]*LibraryDoc) []string {
	if !a.asksEveryone() || a.Game == nil || memberCount <= largeCrewMembers {
		return others
	}
	var out []string
	named := false
	for _, uid := range others {
		lib := libs[uid]
		if !ltpHasGame(lib, a.Game.GameID) {
			continue
		}
		out = append(out, uid)
		if lib != nil {
			named = true
		}
	}
	if !named {
		return others
	}
	return out
}

// ltpAnswerBody is the body of the Answer push, in the language lang. in
// holds the members who are in or due, the asker left out. names gives the
// name of each.
func ltpAnswerBody(lang string, a *ltpAsk, in []string, names map[string]string, nowMs int64) string {
	name := func(uid string) string {
		if n := names[uid]; n != "" {
			return n
		}
		return pushText(lang, "ltp_name_fallback")
	}
	switch len(in) {
	case 0:
		return ""
	case 1:
		ans := a.Answers[in[0]]
		if ans.DueAt > nowMs && ans.ArrivedAt == 0 {
			minutes := int((time.Duration(ans.DueAt-nowMs)*time.Millisecond + time.Minute - 1) / time.Minute)
			return truncateRunes(pushText(lang, "ltp_in_due", name(in[0]), minutes), pushBodyMaxRunes)
		}
		return truncateRunes(pushText(lang, "ltp_in", name(in[0])), pushBodyMaxRunes)
	case 2:
		return truncateRunes(pushText(lang, "ltp_two_in", name(in[0]), name(in[1])), pushBodyMaxRunes)
	default:
		return truncateRunes(pushText(lang, "ltp_many_in", len(in)), pushBodyMaxRunes)
	}
}

// ltpSendPush sends one push to the devices of a member. It returns at once.
// Tests replace it to see what would reach a phone.
var ltpSendPush = func(userID string, n pushAlert) {
	messagePushSend(userID, n)
}

// ltpCrewTitle is the crew name for the title of a push. The fallback is by
// language, so it is resolved for each recipient (mentionPushTitle).
func ltpCrewTitle(ctx context.Context, nk runtime.NakamaModule, crewID string) string {
	if groups, err := nk.GroupsGetId(ctx, []string{crewID}); err == nil && len(groups) > 0 {
		return groups[0].GetName()
	}
	return ""
}

// ltpDeliver sends a push to each member of uids who has no active session.
// build makes the push for the language of the recipient. names holds the
// names of the members that the text needs.
func ltpDeliver(logger runtime.Logger, a *ltpAsk, crewName string, uids []string, langs map[string]string, build func(lang string) pushAlert) {
	sent := 0
	for _, uid := range uids {
		if pushDecisionFor(uid) == pushSkip {
			continue // at the app: the client shows it
		}
		lang := langs[uid]
		n := build(lang)
		n.CrewID = a.CrewID
		n.MessageID = ltpCollapseID(a.AskID)
		n.AskID = a.AskID
		n.Title = mentionPushTitle(lang, crewName)
		n.Lang = lang
		if n.Body == "" {
			continue
		}
		ltpSendPush(uid, n)
		sent++
	}
	logger.Debug("looking_to_play: ask=%s push members=%d sent=%d", a.AskID, len(uids), sent)
}

// ltpPushContext reads what every push of an ask needs: the channel id, the
// crew name, and the names and languages of the members in ids.
func ltpPushContext(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk, ids []string) (channelID, crewName string, names, langs map[string]string, ok bool) {
	channelID, err := nk.ChannelIdBuild(ctx, "", a.CrewID, runtime.Group)
	if err != nil {
		logger.Warn("looking_to_play: channel id for crew %s: %v", a.CrewID, err)
		return "", "", nil, nil, false
	}
	names, langs = pushUsers(ctx, nk, ids)
	return channelID, ltpCrewTitle(ctx, nk, a.CrewID), names, langs, true
}

// ltpAskRecipients returns the members who get the New ask and the Second
// call: the audience of the ask, with the Looking to play setting on.
func ltpAskRecipients(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk, members map[string]bool) []string {
	others := ltpOthers(a, members)
	var libs map[string]*LibraryDoc
	if a.asksEveryone() && a.Game != nil && len(members) > largeCrewMembers {
		libs = readLibraryDocs(ctx, nk, others)
	}
	audience := ltpAudience(a, others, len(members), libs)
	prefs, err := readPushPrefs(ctx, nk, audience)
	if err != nil {
		// Without the settings a member who turned it off could get a push.
		logger.Warn("looking_to_play: read settings for ask %s: %v", a.AskID, err)
		return nil
	}
	var out []string
	for _, uid := range audience {
		if members[uid] && lookingToPlayPushOn(prefs[uid], a.CrewID) {
			out = append(out, uid)
		}
	}
	return out
}

// ltpPushNewAsk sends the New ask push. to is the result of
// ltpAskRecipients.
func ltpPushNewAsk(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk, to []string) {
	if len(to) == 0 {
		return
	}
	channelID, crewName, names, langs, ok := ltpPushContext(ctx, logger, nk, a, append([]string{a.AskerID}, to...))
	if !ok {
		return
	}
	ttl := ltpPushTTL(a, ltpNowMs())
	ltpDeliver(logger, a, crewName, to, langs, func(lang string) pushAlert {
		asker := names[a.AskerID]
		if asker == "" {
			asker = pushText(lang, "ltp_name_fallback")
		}
		body := pushText(lang, "ltp_ask", asker)
		if a.Game != nil {
			body = pushText(lang, "ltp_ask_game", asker, a.Game.Name)
		}
		return pushAlert{
			Type:       pushTypeLtp,
			ChannelID:  channelID,
			Body:       truncateRunes(body, pushBodyMaxRunes),
			Category:   ltpPushCategory,
			TTLSeconds: ttl,
		}
	})
}

// ltpPushAnswer tells the asker who is in. The first push has a sound. Each
// later one replaces it with no sound.
func ltpPushAnswer(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk, first bool) {
	in := a.in()
	if len(in) == 0 {
		return
	}
	channelID, crewName, names, langs, ok := ltpPushContext(ctx, logger, nk, a, append([]string{a.AskerID}, in...))
	if !ok {
		return
	}
	now := ltpNowMs()
	ttl := ltpPushTTL(a, now)
	ltpDeliver(logger, a, crewName, []string{a.AskerID}, langs, func(lang string) pushAlert {
		return pushAlert{
			Type:       pushTypeLtpResult,
			ChannelID:  channelID,
			Body:       ltpAnswerBody(lang, a, in, names, now),
			Silent:     !first,
			TTLSeconds: ttl,
		}
	})
}

// ltpPushSecondCall goes once to the asked members with no answer, when two
// members of the ask are in a voice channel of the crew together. playing
// holds two of those members.
func ltpPushSecondCall(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk, members map[string]bool, playing [2]string) {
	var to []string
	for _, uid := range ltpAskRecipients(ctx, logger, nk, a, members) {
		if _, answered := a.Answers[uid]; !answered {
			to = append(to, uid)
		}
	}
	if len(to) == 0 {
		return
	}
	channelID, crewName, names, langs, ok := ltpPushContext(ctx, logger, nk, a, append(playing[:], to...))
	if !ok {
		return
	}
	ttl := ltpPushTTL(a, ltpNowMs())
	ltpDeliver(logger, a, crewName, to, langs, func(lang string) pushAlert {
		name := func(uid string) string {
			if n := names[uid]; n != "" {
				return n
			}
			return pushText(lang, "ltp_name_fallback")
		}
		return pushAlert{
			Type:       pushTypeLtp,
			ChannelID:  channelID,
			Body:       truncateRunes(pushText(lang, "ltp_playing", name(playing[0]), name(playing[1])), pushBodyMaxRunes),
			Category:   ltpPushCategory,
			TTLSeconds: ttl,
		}
	})
}

// ltpPushClosed sends the push of a close: Nobody to the asker, or Called off
// to the members who are in. An ask that was on and ran out sends none.
func ltpPushClosed(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk) {
	var to []string
	key := ""
	switch a.ClosedReason {
	case ltpClosedNobody, ltpClosedAllNo:
		to, key = []string{a.AskerID}, "ltp_nobody"
	case ltpClosedCancelled:
		to, key = a.in(), "ltp_called_off"
	}
	if len(to) == 0 {
		return
	}
	channelID, crewName, names, langs, ok := ltpPushContext(ctx, logger, nk, a, append([]string{a.AskerID}, to...))
	if !ok {
		return
	}
	ltpDeliver(logger, a, crewName, to, langs, func(lang string) pushAlert {
		body := pushText(lang, key)
		if key == "ltp_called_off" {
			asker := names[a.AskerID]
			if asker == "" {
				asker = pushText(lang, "ltp_name_fallback")
			}
			body = pushText(lang, key, asker)
		}
		return pushAlert{
			Type:       pushTypeLtpResult,
			ChannelID:  channelID,
			Body:       truncateRunes(body, pushBodyMaxRunes),
			TTLSeconds: int(ltpMinTTL / time.Second),
		}
	})
}
