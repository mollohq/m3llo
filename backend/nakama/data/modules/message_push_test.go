package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
)

func TestMessagePushRecipientsLeaveOutTheSenderAndTheMentioned(t *testing.T) {
	members := map[string]bool{"u1": true, "u2": true, "u3": true, "u4": true}
	got := messagePushRecipients(members, "u1", []string{"u3", "stranger"})
	if want := []string{"u2", "u4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("recipients = %v, want %v", got, want)
	}
}

func TestMessagePushBody(t *testing.T) {
	cases := []struct{ name, sender, msgType, text, want string }{
		{"text", "Leo", "text", "up for a game?", "Leo: up for a game?"},
		{"gif", "Leo", "gif", "", "Leo sent a GIF"},
		{"image", "Leo", "image", " ", "Leo sent an image"},
		{"image with a caption", "Leo", "image", "look at this", "Leo: look at this"},
		{"no sender name", "", "gif", "", "Someone sent a GIF"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := messagePushBody(c.sender, c.msgType, c.text); got != c.want {
				t.Fatalf("body = %q, want %q", got, c.want)
			}
		})
	}
	long := messagePushBody("Leo", "text", strings.Repeat("a", 500))
	if n := len([]rune(long)); n > pushBodyMaxRunes {
		t.Fatalf("a long body must fit the lock screen, runes=%d", n)
	}
}

func TestMessagePushEnvelopeTakesOnlyMemberMessages(t *testing.T) {
	cases := []struct {
		name, content string
		want          bool
	}{
		{"text", `{"v":1,"type":"text","body":"hi"}`, true},
		{"gif", `{"v":1,"type":"gif","body":"","gif":{"id":"x"}}`, true},
		{"image", `{"v":1,"type":"image","body":""}`, true},
		{"system", `{"v":1,"type":"system","body":"Leo joined the crew"}`, false},
		{"signal", `{"signal":true,"sdp":"x"}`, false},
		{"not json", `hello`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := messagePushEnvelope(c.content); ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
		})
	}
}

func TestMessageCollapseIDIsOneForEachCrewAndFitsAPNs(t *testing.T) {
	a, b := messageCollapseID("crew-a"), messageCollapseID("crew-b")
	if a == b || a != messageCollapseID("crew-a") || len(a) > 64 {
		t.Fatalf("collapse ids %q %q must be stable, differ by crew and fit 64 bytes", a, b)
	}
}

// messageRig replaces the limiter and the sender of the message push, so a
// test sees what would reach a phone.
type messageRig struct {
	clock  time.Time
	timers []func()
	sent   []string // "user: body"
	alerts []pushAlert
}

func newMessageRig(t *testing.T) *messageRig {
	t.Helper()
	r := &messageRig{clock: time.Unix(1_790_000_000, 0)}
	oldLimiter, oldSend, oldAfter := messagePushes, messagePushSend, afterFunc
	messagePushSend = func(userID string, a pushAlert) {
		r.sent = append(r.sent, userID+": "+a.Body)
		r.alerts = append(r.alerts, a)
	}
	messagePushes = newMessagePushLimiter(
		func() time.Time { return r.clock },
		func(_ time.Duration, f func()) { r.timers = append(r.timers, f) })
	// The grace timer of an inactive desktop lands in the same list.
	afterFunc = func(_ time.Duration, f func()) { r.timers = append(r.timers, f) }
	resetSessionCountsForTests()
	resetSessionActivitiesForTests()
	t.Cleanup(func() {
		messagePushes, messagePushSend, afterFunc = oldLimiter, oldSend, oldAfter
		resetSessionCountsForTests()
		resetSessionActivitiesForTests()
	})
	return r
}

func (r *messageRig) fireTimers() {
	timers := r.timers
	r.timers = nil
	for _, f := range timers {
		f()
	}
}

func messageCrew(members int) *fakeGuestNk {
	nk := prefsRig(members)
	nk.groups["c1"] = &api.Group{Id: "c1", Name: "Night Shift"}
	return nk
}

func textEnvelope(body string, mentions ...string) *messageEnvelope {
	return &messageEnvelope{Type: "text", Body: body, Mentions: mentions}
}

func TestAMessagePushesToTheMembersWhoCannotSeeIt(t *testing.T) {
	r := newMessageRig(t)
	nk := messageCrew(6)
	// u1 sends and mentions u2. u3 muted the crew. u4 has no session. u5 is
	// active at the desktop. u6 has an inactive desktop.
	nk.put(PushPrefsCollection, pushPrefsKey, "u3", pushPrefsDoc{Crews: map[string]crewPushPrefs{"c1": {Messages: boolPtr(false)}}})
	registerSessionStart("u5")
	setSessionActivity("u5", "s5", sessionActivity{Platform: "desktop", Active: true})
	registerSessionStart("u6")
	setSessionActivity("u6", "s6", sessionActivity{Platform: "desktop", Active: false})

	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c1", textEnvelope("up for a game?", "u2"))

	if want := []string{"u4: user1: up for a game?"}; !reflect.DeepEqual(r.sent, want) {
		t.Fatalf("sent at once = %v, want %v", r.sent, want)
	}
	got := r.alerts[0]
	want := pushAlert{Type: "message", CrewID: "c1", ChannelID: "3.c1..", MessageID: messageCollapseID("c1"),
		Title: "Night Shift", Body: "user1: up for a game?"}
	if got != want {
		t.Fatalf("alert = %+v, want %+v", got, want)
	}

	// The inactive desktop gets it when the grace period ends.
	r.fireTimers()
	if want := []string{"u4: user1: up for a game?", "u6: user1: up for a game?"}; !reflect.DeepEqual(r.sent, want) {
		t.Fatalf("sent after the grace period = %v, want %v", r.sent, want)
	}
}

func TestALargeCrewPushesOnlyToMembersWhoTurnedMessagesOn(t *testing.T) {
	r := newMessageRig(t)
	nk := messageCrew(largeCrewMembers + 1)
	nk.put(PushPrefsCollection, pushPrefsKey, "u7", pushPrefsDoc{Crews: map[string]crewPushPrefs{"c1": {Messages: boolPtr(true)}}})

	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c1", textEnvelope("hello"))

	if want := []string{"u7: user1: hello"}; !reflect.DeepEqual(r.sent, want) {
		t.Fatalf("sent = %v, want %v", r.sent, want)
	}
}

func TestABurstOfMessagesLeavesAsOnePush(t *testing.T) {
	r := newMessageRig(t)
	nk := messageCrew(2)

	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c1", textEnvelope("one"))
	r.clock = r.clock.Add(5 * time.Second)
	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c1", textEnvelope("two"))
	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c1", textEnvelope("three"))
	if want := []string{"u2: user1: one"}; !reflect.DeepEqual(r.sent, want) {
		t.Fatalf("only the first goes out inside the window, sent = %v", r.sent)
	}

	r.clock = r.clock.Add(messagePushWindow)
	r.fireTimers()
	if want := []string{"u2: user1: one", "u2: 2 new messages"}; !reflect.DeepEqual(r.sent, want) {
		t.Fatalf("sent after the window = %v, want %v", r.sent, want)
	}
	if r.alerts[1].MessageID != r.alerts[0].MessageID || r.alerts[1].Title != "Night Shift" {
		t.Fatalf("the burst push must replace the first one: %+v", r.alerts[1])
	}
}

func TestABusyCrewDoesNotHoldBackAnotherCrew(t *testing.T) {
	r := newMessageRig(t)
	nk := messageCrew(2)
	nk.groups["c2"] = &api.Group{Id: "c2", Name: "Day Shift"}
	nk.addMember("c2", "u1", "user1")
	nk.addMember("c2", "u2", "user2")

	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c1", textEnvelope("one"))
	deliverMessagePushes(ctxWithUser("u1"), testLogger(), nk, "u1", "user1", "c2", textEnvelope("two"))

	if want := []string{"u2: user1: one", "u2: user1: two"}; !reflect.DeepEqual(r.sent, want) {
		t.Fatalf("sent = %v, want %v", r.sent, want)
	}
}

func TestSubmitPushRoutesAMessageToTheMessageLimiter(t *testing.T) {
	r := newMessageRig(t)
	submitPush("u2", pushAlert{Type: "message", CrewID: "c1", Body: "user1: hi"})
	if len(r.sent) != 1 {
		t.Fatalf("a message push goes through the message limiter, sent = %v", r.sent)
	}
	if got := userOfMessagePushKey(messagePushKey("u2", "c1")); got != "u2" {
		t.Fatalf("user of key = %q", got)
	}
}
