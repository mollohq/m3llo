package main

// Looking to play: the rules of an ask (plans/LOOKING-TO-PLAY.md section 3).
//
// A member asks the crew, or picked members, if they are up for a game. One
// such message is an ask, and the member who sends it is the asker. This file
// holds the state of an ask and the rules that change it. The rules are pure
// functions: they take the ask and the time, and they do no I/O. Storage, the
// RPCs, the chat message and the pushes are in looking_to_play_rpc.go.

import (
	"errors"
	"sort"
	"time"
)

const (
	// ltpOpenWindow is how long an ask waits for its first member.
	ltpOpenWindow = 30 * time.Minute
	// ltpOnWindow is how long an ask that is on stays in that state, from
	// the moment it opened.
	ltpOnWindow = 60 * time.Minute
	// ltpAskCooldown is the shortest time between two asks of one member in
	// a crew. It holds after a cancel too.
	ltpAskCooldown = 10 * time.Minute
	// ltpNobodyCooldown is the wait after an ask that closed with nobody in.
	ltpNobodyCooldown = 30 * time.Minute
	// ltpMaxAsked bounds the picked members of one ask.
	ltpMaxAsked = 50

	ltpStateOpen   = "open"
	ltpStateOn     = "on"
	ltpStateClosed = "closed"

	ltpAnswerIn   = "in"
	ltpAnswerIn15 = "in_15"
	ltpAnswerIn30 = "in_30"
	ltpAnswerNo   = "no"

	// The reasons of a closed ask.
	ltpClosedNobody    = "nobody"    // the open window ended with nobody in
	ltpClosedAllNo     = "all_no"    // every asked member answered no
	ltpClosedCancelled = "cancelled" // the asker cancelled
	ltpClosedDone      = "done"      // the ask was on, and its time ended
)

var (
	errLtpClosed       = errors.New("the ask is closed")
	errLtpBadAnswer    = errors.New("answer must be in, in_15, in_30 or no")
	errLtpAskerAnswers = errors.New("the asker cancels the ask and does not answer it")
	errLtpNotAsker     = errors.New("only the asker cancels the ask")
	errLtpHasOpenAsk   = errors.New("you have an open ask in this crew")
	errLtpTooSoon      = errors.New("you asked a short time ago")
	errLtpNobodyWait   = errors.New("nobody was in on your last ask, wait before the next one")
)

// ltpGame is the game of an ask. GameID is the stable id that the library
// report and the presence use ("steam-730").
type ltpGame struct {
	GameID string `json:"game_id"`
	Name   string `json:"name"`
}

// ltpAnswer is the answer of one member. Times are Unix milliseconds.
type ltpAnswer struct {
	Answer string `json:"answer"`
	At     int64  `json:"at"`
	// DueAt is when a member who answered in_15 or in_30 is due.
	DueAt int64 `json:"due_at,omitempty"`
	// ArrivedAt is when the member joined a voice channel of the crew.
	ArrivedAt int64 `json:"arrived_at,omitempty"`
}

// ltpAsk is the stored state of one ask. Clients get it as it is.
type ltpAsk struct {
	AskID     string `json:"ask_id"`
	CrewID    string `json:"crew_id"`
	MessageID string `json:"message_id"`
	AskerID   string `json:"asker_id"`
	// Asked holds the picked members. Empty means the ask goes to everyone.
	Asked        []string             `json:"asked"`
	Game         *ltpGame             `json:"game,omitempty"`
	OpenedAt     int64                `json:"opened_at"`
	State        string               `json:"state"`
	ClosedReason string               `json:"closed_reason,omitempty"`
	ClosedAt     int64                `json:"closed_at,omitempty"`
	Answers      map[string]ltpAnswer `json:"answers"`
	// SecondCallAt is when the Second call push went out, else 0. It goes
	// once for an ask.
	SecondCallAt int64 `json:"second_call_at,omitempty"`
}

// newLtpAsk makes an open ask. The asker counts as in from the start.
func newLtpAsk(askID, crewID, askerID string, asked []string, game *ltpGame, nowMs int64) *ltpAsk {
	if asked == nil {
		asked = []string{} // "asked": [] in the JSON that clients decode, not null
	}
	return &ltpAsk{
		AskID:    askID,
		CrewID:   crewID,
		AskerID:  askerID,
		Asked:    asked,
		Game:     game,
		OpenedAt: nowMs,
		State:    ltpStateOpen,
		Answers:  map[string]ltpAnswer{askerID: {Answer: ltpAnswerIn, At: nowMs}},
	}
}

func (a *ltpAsk) asksEveryone() bool { return len(a.Asked) == 0 }

// gameKey names the game for the rule "one open ask to everyone for each
// game". An ask with no game has the empty key.
func (a *ltpAsk) gameKey() string {
	if a.Game == nil {
		return ""
	}
	return a.Game.GameID
}

func ltpPositive(answer string) bool {
	return answer == ltpAnswerIn || answer == ltpAnswerIn15 || answer == ltpAnswerIn30
}

func ltpValidAnswer(answer string) bool {
	return ltpPositive(answer) || answer == ltpAnswerNo
}

// in returns the members who are in or due, the asker left out, in a stable
// order.
func (a *ltpAsk) in() []string {
	var out []string
	for uid, ans := range a.Answers {
		if uid != a.AskerID && ltpPositive(ans.Answer) {
			out = append(out, uid)
		}
	}
	sort.Strings(out)
	return out
}

func (a *ltpAsk) close(reason string, nowMs int64) {
	a.State = ltpStateClosed
	a.ClosedReason = reason
	a.ClosedAt = nowMs
}

// ltpCleanAsked makes the list of picked members from a request: members of
// the crew only, no asker, no duplicate, in the order given, bounded.
func ltpCleanAsked(picked []string, askerID string, members map[string]bool) []string {
	seen := map[string]bool{askerID: true}
	var out []string
	for _, uid := range picked {
		if uid == "" || seen[uid] || !members[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
		if len(out) == ltpMaxAsked {
			break
		}
	}
	return out
}

// ltpAnswerResult says what an answer changed.
type ltpAnswerResult struct {
	Changed bool
	// TurnedOn is true when this answer took the ask from open to on.
	TurnedOn bool
	// Closed is true when this answer closed the ask (every asked member
	// answered no).
	Closed bool
}

// ltpApplyAnswer stores the answer of a member. others holds the members
// that the ask goes to: the picked members, or every member but the asker.
// Any member of the crew can answer, asked or not.
func ltpApplyAnswer(a *ltpAsk, userID, answer string, others []string, nowMs int64) (ltpAnswerResult, error) {
	if a.State == ltpStateClosed {
		return ltpAnswerResult{}, errLtpClosed
	}
	if !ltpValidAnswer(answer) {
		return ltpAnswerResult{}, errLtpBadAnswer
	}
	if userID == a.AskerID {
		return ltpAnswerResult{}, errLtpAskerAnswers
	}
	prev, had := a.Answers[userID]
	if had && prev.Answer == answer {
		return ltpAnswerResult{}, nil
	}
	next := ltpAnswer{Answer: answer, At: nowMs}
	switch answer {
	case ltpAnswerIn15:
		next.DueAt = nowMs + (15 * time.Minute).Milliseconds()
	case ltpAnswerIn30:
		next.DueAt = nowMs + (30 * time.Minute).Milliseconds()
	}
	// A member who is in voice stays arrived through a change of answer.
	if ltpPositive(answer) {
		next.ArrivedAt = prev.ArrivedAt
	}
	a.Answers[userID] = next

	res := ltpAnswerResult{Changed: true}
	if a.State != ltpStateOpen {
		return res, nil
	}
	if ltpPositive(answer) {
		a.State = ltpStateOn
		res.TurnedOn = true
		return res, nil
	}
	if ltpAllSaidNo(a, others) {
		a.close(ltpClosedAllNo, nowMs)
		res.Closed = true
	}
	return res, nil
}

// ltpAllSaidNo is true when every member that the ask goes to answered no.
// An ask to nobody is never "all no": the open window closes it.
func ltpAllSaidNo(a *ltpAsk, others []string) bool {
	if len(others) == 0 {
		return false
	}
	for _, uid := range others {
		if uid == a.AskerID {
			continue
		}
		if a.Answers[uid].Answer != ltpAnswerNo {
			return false
		}
	}
	return true
}

// ltpCancel closes the ask for its asker.
func ltpCancel(a *ltpAsk, userID string, nowMs int64) error {
	if a.State == ltpStateClosed {
		return errLtpClosed
	}
	if userID != a.AskerID {
		return errLtpNotAsker
	}
	a.close(ltpClosedCancelled, nowMs)
	return nil
}

// ltpExpire closes an ask whose time ended. It returns true when it closed
// the ask.
func ltpExpire(a *ltpAsk, nowMs int64) bool {
	switch a.State {
	case ltpStateOpen:
		if nowMs >= a.OpenedAt+ltpOpenWindow.Milliseconds() {
			a.close(ltpClosedNobody, nowMs)
			return true
		}
	case ltpStateOn:
		if nowMs >= a.OpenedAt+ltpOnWindow.Milliseconds() {
			a.close(ltpClosedDone, nowMs)
			return true
		}
	}
	return false
}

// ltpVoiceJoin records that a member joined a voice channel of the crew. A
// member who is in or due is marked as arrived. A member with no answer is
// set to in, when onlyOpenAsk says that this is the one open ask of the crew:
// with several open asks, a join does not say which one the member means.
func ltpVoiceJoin(a *ltpAsk, userID string, onlyOpenAsk bool, nowMs int64) ltpAnswerResult {
	if a.State == ltpStateClosed {
		return ltpAnswerResult{}
	}
	ans, had := a.Answers[userID]
	if had && ltpPositive(ans.Answer) {
		if ans.ArrivedAt != 0 {
			return ltpAnswerResult{}
		}
		ans.ArrivedAt = nowMs
		a.Answers[userID] = ans
		return ltpAnswerResult{Changed: true}
	}
	if had || !onlyOpenAsk {
		// The member said no, or the crew has several open asks.
		return ltpAnswerResult{}
	}
	a.Answers[userID] = ltpAnswer{Answer: ltpAnswerIn, At: nowMs, ArrivedAt: nowMs}
	res := ltpAnswerResult{Changed: true}
	if a.State == ltpStateOpen {
		a.State = ltpStateOn
		res.TurnedOn = true
	}
	return res
}

// ltpLastAsk is what the crew index keeps of the last ask of a member, for
// the two waits between asks.
type ltpLastAsk struct {
	OpenedAt int64 `json:"opened_at"`
	// NobodyAt is when the ask closed with nobody in, else 0.
	NobodyAt int64 `json:"nobody_at,omitempty"`
}

// ltpCheckNewAsk applies the rules for a new ask. open holds the open asks of
// the crew. It returns the id of an open ask that the new one joins: an ask
// to everyone for a game that has an open ask to everyone counts as In on it.
func ltpCheckNewAsk(open []*ltpAsk, last *ltpLastAsk, askerID string, asked []string, game *ltpGame, nowMs int64) (joinAskID string, err error) {
	for _, a := range open {
		if a.AskerID == askerID {
			return "", errLtpHasOpenAsk
		}
	}
	if len(asked) == 0 {
		key := ""
		if game != nil {
			key = game.GameID
		}
		for _, a := range open {
			if a.asksEveryone() && a.gameKey() == key {
				return a.AskID, nil
			}
		}
	}
	if last != nil {
		if nowMs < last.OpenedAt+ltpAskCooldown.Milliseconds() {
			return "", errLtpTooSoon
		}
		if last.NobodyAt != 0 && nowMs < last.NobodyAt+ltpNobodyCooldown.Milliseconds() {
			return "", errLtpNobodyWait
		}
	}
	return "", nil
}
