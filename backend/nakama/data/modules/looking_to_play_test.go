package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

const ltpT0 = int64(1_790_000_000_000) // a fixed "now" in Unix milliseconds

func ltpMin(n int) int64 { return (time.Duration(n) * time.Minute).Milliseconds() }

// ltpTestAsk is an ask of u1 to everyone in a crew of u1..u4.
func ltpTestAsk() (*ltpAsk, []string) {
	return newLtpAsk("a1", "c1", "u1", nil, nil, ltpT0), []string{"u2", "u3", "u4"}
}

func TestANewAskIsOpenAndTheAskerIsIn(t *testing.T) {
	a, _ := ltpTestAsk()
	if a.State != ltpStateOpen || a.Answers["u1"].Answer != ltpAnswerIn {
		t.Fatalf("ask = %+v", a)
	}
	if len(a.in()) != 0 {
		t.Fatalf("the asker is not in the list of members who are in: %v", a.in())
	}
	if !a.asksEveryone() || a.gameKey() != "" {
		t.Fatal("an ask with no picked member and no game goes to everyone, with the empty game key")
	}
}

func TestTheFirstMemberWhoIsInTurnsTheAskOn(t *testing.T) {
	for _, answer := range []string{ltpAnswerIn, ltpAnswerIn15, ltpAnswerIn30} {
		a, others := ltpTestAsk()
		res, err := ltpApplyAnswer(a, "u2", answer, others, ltpT0+ltpMin(1))
		if err != nil || !res.Changed || !res.TurnedOn || res.Closed || a.State != ltpStateOn {
			t.Fatalf("%s: res=%+v err=%v state=%s", answer, res, err, a.State)
		}
		// A second member changes the ask and does not turn it on again.
		res, _ = ltpApplyAnswer(a, "u3", ltpAnswerIn, others, ltpT0+ltpMin(2))
		if !res.Changed || res.TurnedOn {
			t.Fatalf("%s: second member res=%+v", answer, res)
		}
		if got := a.in(); !reflect.DeepEqual(got, []string{"u2", "u3"}) {
			t.Fatalf("in = %v", got)
		}
	}
}

func TestInFifteenAndInThirtyStoreWhenTheMemberIsDue(t *testing.T) {
	a, others := ltpTestAsk()
	at := ltpT0 + ltpMin(2)
	ltpApplyAnswer(a, "u2", ltpAnswerIn15, others, at)
	ltpApplyAnswer(a, "u3", ltpAnswerIn30, others, at)
	ltpApplyAnswer(a, "u4", ltpAnswerIn, others, at)
	if a.Answers["u2"].DueAt != at+ltpMin(15) || a.Answers["u3"].DueAt != at+ltpMin(30) || a.Answers["u4"].DueAt != 0 {
		t.Fatalf("answers = %+v", a.Answers)
	}
}

func TestAMemberCanChangeTheAnswer(t *testing.T) {
	a, others := ltpTestAsk()
	ltpApplyAnswer(a, "u2", ltpAnswerIn30, others, ltpT0)
	res, err := ltpApplyAnswer(a, "u2", ltpAnswerIn, others, ltpT0+ltpMin(5))
	if err != nil || !res.Changed || a.Answers["u2"].Answer != ltpAnswerIn || a.Answers["u2"].DueAt != 0 {
		t.Fatalf("res=%+v err=%v answer=%+v", res, err, a.Answers["u2"])
	}
	// The same answer again changes nothing.
	res, err = ltpApplyAnswer(a, "u2", ltpAnswerIn, others, ltpT0+ltpMin(6))
	if err != nil || res.Changed {
		t.Fatalf("a repeated answer: res=%+v err=%v", res, err)
	}
	// A no after the ask is on leaves it on.
	ltpApplyAnswer(a, "u2", ltpAnswerNo, others, ltpT0+ltpMin(7))
	if a.State != ltpStateOn {
		t.Fatalf("state = %s", a.State)
	}
}

func TestEveryAskedMemberSaysNoClosesTheAsk(t *testing.T) {
	a, others := ltpTestAsk()
	ltpApplyAnswer(a, "u2", ltpAnswerNo, others, ltpT0)
	res, _ := ltpApplyAnswer(a, "u3", ltpAnswerNo, others, ltpT0)
	if res.Closed || a.State != ltpStateOpen {
		t.Fatal("one member has no answer yet: the ask stays open")
	}
	res, _ = ltpApplyAnswer(a, "u4", ltpAnswerNo, others, ltpT0+ltpMin(3))
	if !res.Closed || a.State != ltpStateClosed || a.ClosedReason != ltpClosedAllNo || a.ClosedAt != ltpT0+ltpMin(3) {
		t.Fatalf("res=%+v ask=%+v", res, a)
	}
}

func TestAnAskToPickedMembersClosesWhenThePickedSayNo(t *testing.T) {
	a := newLtpAsk("a1", "c1", "u1", []string{"u2", "u3"}, nil, ltpT0)
	picked := []string{"u2", "u3"}
	// u4 was not asked. A no from u4 does not count.
	ltpApplyAnswer(a, "u4", ltpAnswerNo, picked, ltpT0)
	ltpApplyAnswer(a, "u2", ltpAnswerNo, picked, ltpT0)
	if a.State != ltpStateOpen {
		t.Fatal("u3 has no answer yet")
	}
	res, _ := ltpApplyAnswer(a, "u3", ltpAnswerNo, picked, ltpT0)
	if !res.Closed || a.ClosedReason != ltpClosedAllNo {
		t.Fatalf("res=%+v ask=%+v", res, a)
	}
}

func TestAMemberWhoWasNotAskedCanBeIn(t *testing.T) {
	a := newLtpAsk("a1", "c1", "u1", []string{"u2"}, nil, ltpT0)
	res, err := ltpApplyAnswer(a, "u9", ltpAnswerIn, []string{"u2"}, ltpT0)
	if err != nil || !res.TurnedOn {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestAnswerErrors(t *testing.T) {
	a, others := ltpTestAsk()
	if _, err := ltpApplyAnswer(a, "u2", "maybe", others, ltpT0); !errors.Is(err, errLtpBadAnswer) {
		t.Fatalf("bad answer: %v", err)
	}
	if _, err := ltpApplyAnswer(a, "u1", ltpAnswerNo, others, ltpT0); !errors.Is(err, errLtpAskerAnswers) {
		t.Fatalf("asker answers: %v", err)
	}
	if err := ltpCancel(a, "u2", ltpT0); !errors.Is(err, errLtpNotAsker) {
		t.Fatalf("cancel by a member: %v", err)
	}
	if err := ltpCancel(a, "u1", ltpT0+ltpMin(1)); err != nil || a.ClosedReason != ltpClosedCancelled {
		t.Fatalf("cancel: %v %+v", err, a)
	}
	if _, err := ltpApplyAnswer(a, "u2", ltpAnswerIn, others, ltpT0); !errors.Is(err, errLtpClosed) {
		t.Fatalf("answer on a closed ask: %v", err)
	}
	if err := ltpCancel(a, "u1", ltpT0); !errors.Is(err, errLtpClosed) {
		t.Fatalf("cancel twice: %v", err)
	}
}

func TestAnOpenAskClosesWithNobodyAfterThirtyMinutes(t *testing.T) {
	a, _ := ltpTestAsk()
	if ltpExpire(a, ltpT0+ltpMin(29)) {
		t.Fatal("29 minutes: still open")
	}
	if !ltpExpire(a, ltpT0+ltpMin(30)) || a.ClosedReason != ltpClosedNobody {
		t.Fatalf("30 minutes: %+v", a)
	}
	if ltpExpire(a, ltpT0+ltpMin(90)) {
		t.Fatal("a closed ask does not close again")
	}
}

func TestAnAskThatIsOnEndsSixtyMinutesAfterItOpened(t *testing.T) {
	a, others := ltpTestAsk()
	ltpApplyAnswer(a, "u2", ltpAnswerIn, others, ltpT0+ltpMin(25))
	if ltpExpire(a, ltpT0+ltpMin(59)) {
		t.Fatal("59 minutes: still on")
	}
	if !ltpExpire(a, ltpT0+ltpMin(60)) || a.ClosedReason != ltpClosedDone {
		t.Fatalf("60 minutes: %+v", a)
	}
}

func TestAVoiceJoinMarksAMemberWhoIsInAsArrived(t *testing.T) {
	a, others := ltpTestAsk()
	ltpApplyAnswer(a, "u2", ltpAnswerIn15, others, ltpT0)
	res := ltpVoiceJoin(a, "u2", false, ltpT0+ltpMin(4))
	if !res.Changed || res.TurnedOn || a.Answers["u2"].ArrivedAt != ltpT0+ltpMin(4) {
		t.Fatalf("res=%+v answer=%+v", res, a.Answers["u2"])
	}
	if res := ltpVoiceJoin(a, "u2", false, ltpT0+ltpMin(9)); res.Changed {
		t.Fatal("a second join changes nothing")
	}
	// The asker is in from the start, so a join marks the asker as arrived.
	if res := ltpVoiceJoin(a, "u1", false, ltpT0+ltpMin(5)); !res.Changed || a.Answers["u1"].ArrivedAt == 0 {
		t.Fatalf("asker join: %+v", res)
	}
	// A change of answer keeps the arrival.
	ltpApplyAnswer(a, "u2", ltpAnswerIn, others, ltpT0+ltpMin(10))
	if a.Answers["u2"].ArrivedAt != ltpT0+ltpMin(4) {
		t.Fatalf("answer after the change = %+v", a.Answers["u2"])
	}
}

func TestAVoiceJoinWithNoAnswerCountsAsInOnTheOnlyOpenAsk(t *testing.T) {
	a, _ := ltpTestAsk()
	res := ltpVoiceJoin(a, "u3", true, ltpT0+ltpMin(2))
	if !res.Changed || !res.TurnedOn || a.State != ltpStateOn || a.Answers["u3"].Answer != ltpAnswerIn || a.Answers["u3"].ArrivedAt == 0 {
		t.Fatalf("res=%+v ask=%+v", res, a)
	}

	// With several open asks the join does not say which ask the member means.
	b, _ := ltpTestAsk()
	if res := ltpVoiceJoin(b, "u3", false, ltpT0); res.Changed || b.State != ltpStateOpen {
		t.Fatalf("several open asks: %+v", res)
	}
	// A member who said no stays out.
	c, others := ltpTestAsk()
	ltpApplyAnswer(c, "u3", ltpAnswerNo, others, ltpT0)
	if res := ltpVoiceJoin(c, "u3", true, ltpT0); res.Changed {
		t.Fatalf("a no is not turned into in: %+v", res)
	}
}

func TestCleanAskedKeepsMembersOnceWithoutTheAsker(t *testing.T) {
	members := map[string]bool{"u1": true, "u2": true, "u3": true}
	got := ltpCleanAsked([]string{"u2", "u1", "stranger", "u2", "", "u3"}, "u1", members)
	if want := []string{"u2", "u3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asked = %v, want %v", got, want)
	}
	many := make([]string, 0, ltpMaxAsked+10)
	all := map[string]bool{}
	for i := 0; i < ltpMaxAsked+10; i++ {
		id := "m" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		many = append(many, id)
		all[id] = true
	}
	if got := ltpCleanAsked(many, "u1", all); len(got) != ltpMaxAsked {
		t.Fatalf("picked members are bounded: %d", len(got))
	}
}

func TestNewAskRules(t *testing.T) {
	cs := &ltpGame{GameID: "steam-730", Name: "Counter-Strike 2"}
	val := &ltpGame{GameID: "steam-892970", Name: "Valheim"}
	openCS := newLtpAsk("a-cs", "c1", "u2", nil, cs, ltpT0)
	openAny := newLtpAsk("a-any", "c1", "u3", nil, nil, ltpT0)
	picked := newLtpAsk("a-picked", "c1", "u4", []string{"u5"}, cs, ltpT0)
	open := []*ltpAsk{openCS, openAny, picked}

	// An ask to everyone joins the open ask to everyone for the same game.
	if join, err := ltpCheckNewAsk(open, nil, "u9", nil, cs, ltpT0); err != nil || join != "a-cs" {
		t.Fatalf("same game: join=%q err=%v", join, err)
	}
	if join, err := ltpCheckNewAsk(open, nil, "u9", nil, nil, ltpT0); err != nil || join != "a-any" {
		t.Fatalf("no game: join=%q err=%v", join, err)
	}
	// Another game, or picked members, is a new ask.
	if join, err := ltpCheckNewAsk(open, nil, "u9", nil, val, ltpT0); err != nil || join != "" {
		t.Fatalf("another game: join=%q err=%v", join, err)
	}
	if join, err := ltpCheckNewAsk(open, nil, "u9", []string{"u5"}, cs, ltpT0); err != nil || join != "" {
		t.Fatalf("picked members: join=%q err=%v", join, err)
	}
	// A member is the asker of one open ask.
	if _, err := ltpCheckNewAsk(open, nil, "u2", nil, val, ltpT0); !errors.Is(err, errLtpHasOpenAsk) {
		t.Fatalf("second ask of an asker: %v", err)
	}
}

func TestNewAskWaits(t *testing.T) {
	last := &ltpLastAsk{OpenedAt: ltpT0}
	if _, err := ltpCheckNewAsk(nil, last, "u1", nil, nil, ltpT0+ltpMin(9)); !errors.Is(err, errLtpTooSoon) {
		t.Fatalf("9 minutes after an ask: %v", err)
	}
	if _, err := ltpCheckNewAsk(nil, last, "u1", nil, nil, ltpT0+ltpMin(10)); err != nil {
		t.Fatalf("10 minutes after an ask: %v", err)
	}
	// Nobody was in: 30 minutes from the close.
	nobody := &ltpLastAsk{OpenedAt: ltpT0, NobodyAt: ltpT0 + ltpMin(30)}
	if _, err := ltpCheckNewAsk(nil, nobody, "u1", nil, nil, ltpT0+ltpMin(59)); !errors.Is(err, errLtpNobodyWait) {
		t.Fatalf("29 minutes after nobody: %v", err)
	}
	if _, err := ltpCheckNewAsk(nil, nobody, "u1", nil, nil, ltpT0+ltpMin(60)); err != nil {
		t.Fatalf("30 minutes after nobody: %v", err)
	}
	// The waits do not stop a member from joining an open ask.
	open := []*ltpAsk{newLtpAsk("a1", "c1", "u2", nil, nil, ltpT0)}
	if join, err := ltpCheckNewAsk(open, last, "u1", nil, nil, ltpT0+ltpMin(1)); err != nil || join != "a1" {
		t.Fatalf("join inside the wait: join=%q err=%v", join, err)
	}
}
