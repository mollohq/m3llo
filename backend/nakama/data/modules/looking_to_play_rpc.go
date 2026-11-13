package main

// Looking to play: the RPCs, the chat message of an ask and the voice hook
// (plans/LOOKING-TO-PLAY.md section 3.3).
//
//   looking_to_play_ask    {crew_id, game?, member_ids?}  -> {ask, joined}
//   looking_to_play_answer {ask_id, answer}               -> {ask}
//   looking_to_play_cancel {ask_id}                       -> {ask}
//   looking_to_play_list   {crew_id, ask_ids?}            -> {asks}

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/heroiclabs/nakama-common/runtime"
)

// ltpListMax bounds the asks of one looking_to_play_list call.
const ltpListMax = 50

type ltpAskRequest struct {
	CrewID    string   `json:"crew_id"`
	Game      *ltpGame `json:"game,omitempty"`
	MemberIDs []string `json:"member_ids,omitempty"`
}

type ltpAskResponse struct {
	Ask *ltpAsk `json:"ask"`
	// Joined is true when the call did not make an ask: it counted as In on
	// the open ask to everyone for the same game.
	Joined bool `json:"joined,omitempty"`
}

type ltpListResponse struct {
	Asks []*ltpAsk `json:"asks"`
}

// ltpRPCError maps a rule error to the token that the client knows
// (rpc_error_codes.go).
func ltpRPCError(logger runtime.Logger, err error) error {
	switch {
	case errors.Is(err, errLtpNotFound):
		return errAskNotFound
	case errors.Is(err, errLtpClosed):
		return errAskClosed
	case errors.Is(err, errLtpNotAsker):
		return errAskNotAsker
	case errors.Is(err, errLtpHasOpenAsk):
		return errAskHasOpen
	case errors.Is(err, errLtpTooSoon):
		return errAskTooSoon
	case errors.Is(err, errLtpNobodyWait):
		return errAskNobodyWait
	case errors.Is(err, errLtpBadAnswer), errors.Is(err, errLtpAskerAnswers):
		return rpcFail(logger, errBadRequest, "looking_to_play: %v", err)
	}
	return rpcFail(logger, errInternal, "looking_to_play: %v", err)
}

func ltpReply(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", errInternal
	}
	return string(data), nil
}

// validLtpGame checks the game of a request. The id has the form of the
// library report ("steam-730").
func validLtpGame(g *ltpGame) bool {
	if g == nil {
		return true
	}
	return validLibraryGameID(g.GameID) && g.Name != "" && utf8.RuneCountInString(g.Name) <= maxLibraryNameRunes
}

// sendLtpMessage posts the chat message of an ask and returns its id. It is
// a system message with the event "looking_to_play": a client that does not
// know the event shows the body as a system line.
func sendLtpMessage(ctx context.Context, nk runtime.NakamaModule, a *ltpAsk, askerName string) (string, error) {
	channelID, err := nk.ChannelIdBuild(ctx, "", a.CrewID, runtime.Group)
	if err != nil {
		return "", err
	}
	if askerName == "" {
		askerName = "A crew member"
	}
	body := askerName + " is looking to play"
	data := map[string]interface{}{"ask_id": a.AskID, "asker_id": a.AskerID}
	if a.Game != nil {
		body += " " + a.Game.Name
		data["game"] = map[string]interface{}{"game_id": a.Game.GameID, "name": a.Game.Name}
	}
	raw, err := json.Marshal(systemEnvelope{V: 1, Type: "system", Body: body, Event: "looking_to_play", Data: data})
	if err != nil {
		return "", err
	}
	var content map[string]interface{}
	if err := json.Unmarshal(raw, &content); err != nil {
		return "", err
	}
	ack, err := nk.ChannelMessageSend(ctx, channelID, content, "", "", true)
	if err != nil {
		return "", err
	}
	return ack.GetMessageId(), nil
}

// LookingToPlayAskRPC makes an ask, or counts as In on the open ask to
// everyone for the same game.
func LookingToPlayAskRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errNoSession
	}
	var req ltpAskRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.CrewID == "" || !validLtpGame(req.Game) {
		return "", rpcFail(logger, errBadRequest, "looking_to_play_ask: bad payload")
	}
	members := crewMemberSet(ctx, nk, req.CrewID)
	if !members[userID] {
		return "", errNotAMember
	}
	asked := ltpCleanAsked(req.MemberIDs, userID, members)
	if len(req.MemberIDs) > 0 && len(asked) == 0 {
		// Every picked member is gone: do not turn it into an ask to everyone.
		return "", rpcFail(logger, errBadRequest, "looking_to_play_ask: no picked member is in the crew")
	}

	open, idx, err := openLtpAsks(ctx, nk, req.CrewID)
	if err != nil {
		return "", rpcFail(logger, errInternal, "looking_to_play_ask: open asks of %s: %v", req.CrewID, err)
	}
	now := ltpNowMs()
	var last *ltpLastAsk
	if l, ok := idx.Last[userID]; ok {
		last = &l
	}
	joinID, err := ltpCheckNewAsk(open, last, userID, asked, req.Game, now)
	if err != nil {
		return "", ltpRPCError(logger, err)
	}
	if joinID != "" {
		a, err := answerLtpAsk(ctx, logger, nk, joinID, userID, ltpAnswerIn, members)
		if err != nil {
			return "", ltpRPCError(logger, err)
		}
		return ltpReply(ltpAskResponse{Ask: a, Joined: true})
	}

	a := newLtpAsk(generateEventID(), req.CrewID, userID, asked, req.Game, now)
	a.MessageID, err = sendLtpMessage(ctx, nk, a, resolveUsername(ctx, nk, userID))
	if err != nil {
		return "", rpcFail(logger, errInternal, "looking_to_play_ask: chat message for crew %s: %v", req.CrewID, err)
	}
	if err := writeLtpAsk(ctx, nk, a, ""); err != nil {
		return "", rpcFail(logger, errInternal, "looking_to_play_ask: store ask %s: %v", a.AskID, err)
	}
	if _, err := changeLtpIndex(ctx, nk, req.CrewID, func(idx *ltpCrewIndex) {
		idx.Open = append(ltpRemoveID(idx.Open, a.AskID), a.AskID)
		idx.Last[userID] = ltpLastAsk{OpenedAt: now}
	}); err != nil {
		// An ask that the index does not name would never close: close it now.
		a.close(ltpClosedCancelled, now)
		_ = writeLtpAsk(ctx, nk, a, "")
		return "", rpcFail(logger, errInternal, "looking_to_play_ask: index of crew %s: %v", req.CrewID, err)
	}
	ltpMarkCrewOpen(req.CrewID)
	logger.Info("looking_to_play: ask=%s crew=%s opened asked=%d game=%t", a.AskID, a.CrewID, len(a.Asked), a.Game != nil)

	notifyLtpAsk(ctx, nk, a, members)
	ltpPushNewAsk(ctx, logger, nk, a, members)
	return ltpReply(ltpAskResponse{Ask: a})
}

// answerLtpAsk stores the answer of a member and does what follows it: the
// notification, the push to the asker, and the close when every asked member
// said no.
func answerLtpAsk(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, askID, userID, answer string, members map[string]bool) (*ltpAsk, error) {
	var res ltpAnswerResult
	now := ltpNowMs()
	a, err := changeLtpAsk(ctx, nk, askID, func(a *ltpAsk) (bool, error) {
		var err error
		res, err = ltpApplyAnswer(a, userID, answer, ltpOthers(a, members), now)
		return res.Changed, err
	})
	if err != nil {
		return nil, err
	}
	if !res.Changed {
		return a, nil
	}
	logger.Info("looking_to_play: ask=%s answer=%s on=%t closed=%t", a.AskID, answer, res.TurnedOn, res.Closed)
	if res.Closed {
		finishLtpAsk(ctx, logger, nk, a, members)
		return a, nil
	}
	notifyLtpAsk(ctx, nk, a, members)
	if ltpPositive(answer) {
		ltpPushAnswer(ctx, logger, nk, a, res.TurnedOn)
	}
	return a, nil
}

// ltpAskForMember reads an ask and checks that the caller is a member of its
// crew. It returns the members of the crew.
func ltpAskForMember(ctx context.Context, nk runtime.NakamaModule, askID, userID string) (*ltpAsk, map[string]bool, error) {
	asks, _, err := readLtpAsks(ctx, nk, []string{askID})
	if err != nil {
		return nil, nil, err
	}
	a := asks[askID]
	if a == nil {
		return nil, nil, errLtpNotFound
	}
	members := crewMemberSet(ctx, nk, a.CrewID)
	if !members[userID] {
		return nil, nil, errNotAMember
	}
	return a, members, nil
}

// LookingToPlayAnswerRPC stores the caller's answer to an ask.
func LookingToPlayAnswerRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errNoSession
	}
	var req struct {
		AskID  string `json:"ask_id"`
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.AskID == "" || !ltpValidAnswer(req.Answer) {
		return "", rpcFail(logger, errBadRequest, "looking_to_play_answer: bad payload")
	}
	_, members, err := ltpAskForMember(ctx, nk, req.AskID, userID)
	if err != nil {
		if errors.Is(err, errNotAMember) {
			return "", errNotAMember
		}
		return "", ltpRPCError(logger, err)
	}
	a, err := answerLtpAsk(ctx, logger, nk, req.AskID, userID, req.Answer, members)
	if err != nil {
		return "", ltpRPCError(logger, err)
	}
	return ltpReply(ltpAskResponse{Ask: a})
}

// LookingToPlayCancelRPC closes an ask for its asker.
func LookingToPlayCancelRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errNoSession
	}
	var req struct {
		AskID string `json:"ask_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.AskID == "" {
		return "", rpcFail(logger, errBadRequest, "looking_to_play_cancel: bad payload")
	}
	_, members, err := ltpAskForMember(ctx, nk, req.AskID, userID)
	if err != nil {
		if errors.Is(err, errNotAMember) {
			return "", errNotAMember
		}
		return "", ltpRPCError(logger, err)
	}
	now := ltpNowMs()
	a, err := changeLtpAsk(ctx, nk, req.AskID, func(a *ltpAsk) (bool, error) {
		if err := ltpCancel(a, userID, now); err != nil {
			return false, err
		}
		return true, nil
	})
	if err != nil {
		return "", ltpRPCError(logger, err)
	}
	finishLtpAsk(ctx, logger, nk, a, members)
	return ltpReply(ltpAskResponse{Ask: a})
}

// LookingToPlayListRPC returns the asks of loaded chat history and the open
// asks of the crew. The client calls it on history load, as it does for the
// reactions.
func LookingToPlayListRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errNoSession
	}
	var req struct {
		CrewID string   `json:"crew_id"`
		AskIDs []string `json:"ask_ids,omitempty"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.CrewID == "" || len(req.AskIDs) > ltpListMax {
		return "", rpcFail(logger, errBadRequest, "looking_to_play_list: bad payload")
	}
	if !crewMemberSet(ctx, nk, req.CrewID)[userID] {
		return "", errNotAMember
	}
	open, _, err := openLtpAsks(ctx, nk, req.CrewID)
	if err != nil {
		return "", rpcFail(logger, errInternal, "looking_to_play_list: open asks of %s: %v", req.CrewID, err)
	}
	out := ltpListResponse{Asks: []*ltpAsk{}}
	seen := map[string]bool{}
	for _, a := range open {
		seen[a.AskID] = true
		out.Asks = append(out.Asks, a)
	}
	asks, _, err := readLtpAsks(ctx, nk, req.AskIDs)
	if err != nil {
		return "", rpcFail(logger, errInternal, "looking_to_play_list: asks of %s: %v", req.CrewID, err)
	}
	for _, id := range req.AskIDs {
		// An ask of another crew is not given out.
		if a := asks[id]; a != nil && a.CrewID == req.CrewID && !seen[id] {
			seen[id] = true
			out.Asks = append(out.Asks, a)
		}
	}
	return ltpReply(out)
}

// ---------------------------------------------------------------------------
// Voice
// ---------------------------------------------------------------------------

// ltpCrewVoiceMembers returns the members who are in a voice channel of the
// crew. Tests replace it.
var ltpCrewVoiceMembers = func(ctx context.Context, nk runtime.NakamaModule, crewID string) map[string]bool {
	out := map[string]bool{}
	for _, snap := range GetCrewVoiceSnapshots(ctx, nk, crewID) {
		if snap == nil {
			continue
		}
		for _, id := range snap.MemberIDs {
			out[id] = true
		}
	}
	return out
}

// ltpPlayingPair returns two members of the ask who are in voice, the asker
// first when the asker is one of them.
func ltpPlayingPair(a *ltpAsk, inVoice map[string]bool) ([2]string, bool) {
	var pair []string
	for _, uid := range append([]string{a.AskerID}, a.in()...) {
		if inVoice[uid] {
			pair = append(pair, uid)
			if len(pair) == 2 {
				return [2]string{pair[0], pair[1]}, true
			}
		}
	}
	return [2]string{}, false
}

// onLtpVoiceJoin applies a voice join to the open asks of the crew. The voice
// join path calls it for every join, so the first check reads memory only.
func onLtpVoiceJoin(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, crewID, userID string) {
	if !ltpCrewHasOpen(crewID) {
		return
	}
	open, _, err := openLtpAsks(ctx, nk, crewID)
	if err != nil || len(open) == 0 {
		return
	}
	members := crewMemberSet(ctx, nk, crewID)
	if !members[userID] {
		return // a guest in the voice channel
	}
	inVoice := ltpCrewVoiceMembers(ctx, nk, crewID)
	inVoice[userID] = true
	for _, open1 := range open {
		var res ltpAnswerResult
		now := ltpNowMs()
		wasIn := ltpPositive(open1.Answers[userID].Answer)
		a, err := changeLtpAsk(ctx, nk, open1.AskID, func(a *ltpAsk) (bool, error) {
			res = ltpVoiceJoin(a, userID, len(open) == 1, now)
			return res.Changed, nil
		})
		if err != nil {
			logger.Warn("looking_to_play: voice join on ask %s: %v", open1.AskID, err)
			continue
		}
		if res.Changed {
			notifyLtpAsk(ctx, nk, a, members)
			if userID != a.AskerID && !wasIn {
				ltpPushAnswer(ctx, logger, nk, a, res.TurnedOn)
			}
		}
		if a.SecondCallAt != 0 || a.State == ltpStateClosed {
			continue
		}
		pair, ok := ltpPlayingPair(a, inVoice)
		if !ok {
			continue
		}
		sent := false
		a, err = changeLtpAsk(ctx, nk, a.AskID, func(a *ltpAsk) (bool, error) {
			if a.SecondCallAt != 0 || a.State == ltpStateClosed {
				return false, nil
			}
			a.SecondCallAt = now
			sent = true
			return true, nil
		})
		if err == nil && sent {
			ltpPushSecondCall(ctx, logger, nk, a, members, pair)
		}
	}
}

// queueLtpVoiceJoin runs onLtpVoiceJoin off the voice join path.
func queueLtpVoiceJoin(logger runtime.Logger, nk runtime.NakamaModule, crewID, userID string) {
	if !ltpCrewHasOpen(crewID) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("looking_to_play: voice join panic for crew %s: %v", crewID, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), pushFanoutTimeout)
		defer cancel()
		onLtpVoiceJoin(ctx, logger, nk, crewID, userID)
	}()
}
