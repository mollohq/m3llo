package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"database/sql"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
)

// ltpFakeNk adds the chat message, the notification and the storage list to
// the in-memory module of the guest tests.
type ltpFakeNk struct {
	*fakeGuestNk
	messages []map[string]interface{}
	notes    []ltpNote
}

type ltpNote struct {
	UserID  string
	Code    int
	Content map[string]interface{}
}

func (f *ltpFakeNk) ChannelMessageSend(_ context.Context, _ string, content map[string]interface{}, senderID, _ string, _ bool) (*rtapi.ChannelMessageAck, error) {
	// As the real module: the sender is stored as a UUID.
	if len(senderID) != 36 {
		return nil, errors.New(`invalid input syntax for type uuid: "` + senderID + `"`)
	}
	f.messages = append(f.messages, content)
	return &rtapi.ChannelMessageAck{MessageId: fmt.Sprintf("msg-%d", len(f.messages))}, nil
}

func (f *ltpFakeNk) NotificationSend(_ context.Context, userID, _ string, content map[string]interface{}, code int, _ string, _ bool) error {
	f.notes = append(f.notes, ltpNote{UserID: userID, Code: code, Content: content})
	return nil
}

func (f *ltpFakeNk) StorageList(_ context.Context, _, userID, collection string, _ int, _ string) ([]*api.StorageObject, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*api.StorageObject
	prefix := collection + "/"
	suffix := "/" + userID
	for k, v := range f.storage {
		if len(k) > len(prefix)+len(suffix) && k[:len(prefix)] == prefix && k[len(k)-len(suffix):] == suffix {
			out = append(out, &api.StorageObject{Collection: collection, Key: k[len(prefix) : len(k)-len(suffix)], UserId: userID, Value: v})
		}
	}
	return out, "", nil
}

// ltpRig is a crew "c1" of members u1..uN with the clock, the pushes and the
// voice state of Looking to play under the test's control.
type ltpRig struct {
	t      *testing.T
	nk     *ltpFakeNk
	now    int64
	pushes []ltpSentPush
	voice  map[string]bool
}

type ltpSentPush struct {
	To string
	N  pushAlert
}

func newLtpRig(t *testing.T, members int) *ltpRig {
	t.Helper()
	r := &ltpRig{t: t, nk: &ltpFakeNk{fakeGuestNk: prefsRig(members)}, now: ltpT0, voice: map[string]bool{}}
	r.nk.groups["c1"] = &api.Group{Id: "c1", Name: "Night Shift"}
	oldNow, oldSend, oldVoice := ltpNowMs, ltpSendPush, ltpCrewVoiceMembers
	ltpNowMs = func() int64 { return r.now }
	ltpSendPush = func(uid string, n pushAlert) { r.pushes = append(r.pushes, ltpSentPush{To: uid, N: n}) }
	ltpCrewVoiceMembers = func(context.Context, runtime.NakamaModule, string) map[string]bool {
		out := map[string]bool{}
		for k, v := range r.voice {
			out[k] = v
		}
		return out
	}
	resetLtpOpenCrewsForTests()
	resetSessionCountsForTests()
	resetSessionActivitiesForTests()
	t.Cleanup(func() {
		ltpNowMs, ltpSendPush, ltpCrewVoiceMembers = oldNow, oldSend, oldVoice
		resetLtpOpenCrewsForTests()
		resetSessionCountsForTests()
		resetSessionActivitiesForTests()
	})
	return r
}

// call runs an RPC as a user and returns the ask of the reply.
func (r *ltpRig) call(rpc func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error), user, payload string) (*ltpAskResponse, error) {
	raw, err := rpc(ctxWithUser(user), testLogger(), nil, r.nk, payload)
	if err != nil {
		return nil, err
	}
	var out ltpAskResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		r.t.Fatalf("decode %q: %v", raw, err)
	}
	return &out, nil
}

func (r *ltpRig) ask(user, payload string) *ltpAsk {
	r.t.Helper()
	out, err := r.call(LookingToPlayAskRPC, user, payload)
	if err != nil {
		r.t.Fatalf("ask by %s: %v", user, err)
	}
	return out.Ask
}

func (r *ltpRig) answer(user, askID, answer string) *ltpAsk {
	r.t.Helper()
	out, err := r.call(LookingToPlayAnswerRPC, user, fmt.Sprintf(`{"ask_id":%q,"answer":%q}`, askID, answer))
	if err != nil {
		r.t.Fatalf("answer %s by %s: %v", answer, user, err)
	}
	return out.Ask
}

// takePushes returns the pushes since the last call as "user: body" lines.
func (r *ltpRig) takePushes() []string {
	var out []string
	for _, p := range r.pushes {
		out = append(out, p.To+": "+p.N.Body)
	}
	r.pushes = nil
	sort.Strings(out)
	return out
}

func rpcToken(err error) string {
	var re *runtime.Error
	if errors.As(err, &re) {
		return re.Message
	}
	return fmt.Sprint(err)
}

func TestAnAskPostsTheMessageNotifiesTheCrewAndPushes(t *testing.T) {
	r := newLtpRig(t, 4)
	a := r.ask("u1", `{"crew_id":"c1"}`)

	if a.State != ltpStateOpen || a.AskerID != "u1" || a.AskerName != "user1" || a.CrewID != "c1" || a.MessageID != "msg-1" || len(a.Asked) != 0 {
		t.Fatalf("ask = %+v", a)
	}
	// The chat message: a system message that an older client shows as a line.
	msg := r.nk.messages[0]
	if msg["type"] != "system" || msg["event"] != "looking_to_play" || msg["body"] != "user1 is looking to play" {
		t.Fatalf("message = %v", msg)
	}
	if data, _ := msg["data"].(map[string]interface{}); data["ask_id"] != a.AskID || data["asker_id"] != "u1" {
		t.Fatalf("message data = %v", msg["data"])
	}
	// The reply is what a client decodes: no picked member is an empty list.
	raw, _ := json.Marshal(a)
	var wire map[string]interface{}
	_ = json.Unmarshal(raw, &wire)
	if list, ok := wire["asked"].([]interface{}); !ok || len(list) != 0 {
		t.Fatalf(`"asked" must be [] on the wire, got %v`, wire["asked"])
	}
	// Notification 118 with the state, to every member.
	var noted []string
	for _, n := range r.nk.notes {
		if n.Code != NotifyLookingToPlay || n.Content["ask_id"] != a.AskID || n.Content["state"] != ltpStateOpen {
			t.Fatalf("note = %+v", n)
		}
		noted = append(noted, n.UserID)
	}
	if want := []string{"u1", "u2", "u3", "u4"}; !reflect.DeepEqual(noted, want) {
		t.Fatalf("notified = %v, want %v", noted, want)
	}
	// The New ask push, to every member but the asker.
	want := []string{"u2: user1 is looking to play", "u3: user1 is looking to play", "u4: user1 is looking to play"}
	if got := r.takePushesKeep(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pushes = %v, want %v", got, want)
	}
	n := r.pushes[0].N
	if n.Type != pushTypeLtp || n.Category != ltpPushCategory || n.Title != "Night Shift" || n.CrewID != "c1" ||
		n.AskID != a.AskID || n.MessageID != ltpCollapseID(a.AskID) || n.TTLSeconds != 30*60 || n.Silent {
		t.Fatalf("push = %+v", n)
	}
	if !ltpCrewHasOpen("c1") {
		t.Fatal("the crew has an open ask")
	}
}

// takePushesKeep is takePushes without the reset.
func (r *ltpRig) takePushesKeep() []string {
	var out []string
	for _, p := range r.pushes {
		out = append(out, p.To+": "+p.N.Body)
	}
	sort.Strings(out)
	return out
}

func TestAnAskToPickedMembersWithAGame(t *testing.T) {
	r := newLtpRig(t, 5)
	a := r.ask("u1", `{"crew_id":"c1","game":{"game_id":"steam-730","name":"Counter-Strike 2"},"member_ids":["u3","u1","stranger","u4"]}`)

	if want := []string{"u3", "u4"}; !reflect.DeepEqual(a.Asked, want) {
		t.Fatalf("asked = %v, want %v", a.Asked, want)
	}
	if r.nk.messages[0]["body"] != "user1 is looking to play Counter-Strike 2" {
		t.Fatalf("message = %v", r.nk.messages[0])
	}
	want := []string{"u3: user1 is looking to play Counter-Strike 2", "u4: user1 is looking to play Counter-Strike 2"}
	if got := r.takePushes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("only the picked members get the push: %v", got)
	}
	// The card is in crew chat for every member: all five get the state.
	if len(r.nk.notes) != 5 {
		t.Fatalf("notified %d members, want 5", len(r.nk.notes))
	}
	// A member who was not asked can be in.
	if got := r.answer("u5", a.AskID, ltpAnswerIn); got.State != ltpStateOn {
		t.Fatalf("state = %s", got.State)
	}
}

func TestTheAskerHearsWhoIsInOnceAndThenSeesTheCount(t *testing.T) {
	r := newLtpRig(t, 5)
	a := r.ask("u1", `{"crew_id":"c1"}`)
	r.takePushes()

	r.answer("u2", a.AskID, ltpAnswerIn15)
	if got := r.takePushesKeep(); !reflect.DeepEqual(got, []string{"u1: user2 is in, 15 min"}) {
		t.Fatalf("first answer: %v", got)
	}
	if n := r.pushes[0].N; n.Type != pushTypeLtpResult || n.Silent || n.Category != "" || n.MessageID != ltpCollapseID(a.AskID) {
		t.Fatalf("first answer push = %+v", n)
	}
	r.takePushes()

	r.answer("u3", a.AskID, ltpAnswerIn)
	if got := r.takePushesKeep(); !reflect.DeepEqual(got, []string{"u1: user2 and user3 are in"}) {
		t.Fatalf("second answer: %v", got)
	}
	if !r.pushes[0].N.Silent {
		t.Fatal("a later answer replaces the push with no sound")
	}
	r.takePushes()

	r.answer("u4", a.AskID, ltpAnswerIn)
	if got := r.takePushes(); !reflect.DeepEqual(got, []string{"u1: 3 are in"}) {
		t.Fatalf("third answer: %v", got)
	}
	// A no sends nothing to the asker.
	r.answer("u5", a.AskID, ltpAnswerNo)
	if got := r.takePushes(); len(got) != 0 {
		t.Fatalf("a no pushes nothing: %v", got)
	}
}

func TestEveryoneSaysNoTellsTheAskerAndStartsTheWait(t *testing.T) {
	r := newLtpRig(t, 3)
	// The ledger keeps the events of the last days, by the real clock.
	r.now = time.Now().UnixMilli()
	a := r.ask("u1", `{"crew_id":"c1"}`)
	r.takePushes()
	r.answer("u2", a.AskID, ltpAnswerNo)
	got := r.answer("u3", a.AskID, ltpAnswerNo)

	if got.State != ltpStateClosed || got.ClosedReason != ltpClosedAllNo {
		t.Fatalf("ask = %+v", got)
	}
	if p := r.takePushesKeep(); !reflect.DeepEqual(p, []string{"u1: No one this time"}) {
		t.Fatalf("pushes = %v", p)
	}
	if n := r.pushes[0].N; n.Type != pushTypeLtpResult || n.Category != "" {
		t.Fatalf("push = %+v", n)
	}
	if ltpCrewHasOpen("c1") {
		t.Fatal("the crew has no open ask")
	}
	// The ledger has the close.
	ledger, _ := readLedger(context.Background(), r.nk, "c1")
	if len(ledger.Events) != 1 || ledger.Events[0].Type != "looking_to_play" || ledger.Events[0].ActorID != "u1" {
		t.Fatalf("ledger = %+v", ledger.Events)
	}
	// Nobody was in: the asker waits 30 minutes from the close.
	r.now += ltpMin(29)
	if _, err := r.call(LookingToPlayAskRPC, "u1", `{"crew_id":"c1"}`); rpcToken(err) != "ASK_NOBODY_WAIT" {
		t.Fatalf("ask after nobody: %v", err)
	}
	r.now += ltpMin(1)
	if _, err := r.call(LookingToPlayAskRPC, "u1", `{"crew_id":"c1"}`); err != nil {
		t.Fatalf("ask after the wait: %v", err)
	}
}

func TestCancelTellsTheMembersWhoAreInAndKeepsTheShortWait(t *testing.T) {
	r := newLtpRig(t, 4)
	a := r.ask("u1", `{"crew_id":"c1"}`)
	r.answer("u2", a.AskID, ltpAnswerIn)
	r.answer("u3", a.AskID, ltpAnswerNo)
	r.takePushes()

	if _, err := r.call(LookingToPlayCancelRPC, "u2", fmt.Sprintf(`{"ask_id":%q}`, a.AskID)); rpcToken(err) != "ASK_NOT_ASKER" {
		t.Fatalf("cancel by a member: %v", err)
	}
	out, err := r.call(LookingToPlayCancelRPC, "u1", fmt.Sprintf(`{"ask_id":%q}`, a.AskID))
	if err != nil || out.Ask.ClosedReason != ltpClosedCancelled {
		t.Fatalf("cancel: %v %+v", err, out)
	}
	if got := r.takePushes(); !reflect.DeepEqual(got, []string{"u2: user1 called it off"}) {
		t.Fatalf("pushes = %v", got)
	}
	// A cancel does not let a member ask again at once.
	r.now += ltpMin(9)
	if _, err := r.call(LookingToPlayAskRPC, "u1", `{"crew_id":"c1"}`); rpcToken(err) != "ASK_TOO_SOON" {
		t.Fatalf("ask 9 minutes after an ask: %v", err)
	}
	if _, err := r.call(LookingToPlayAnswerRPC, "u4", fmt.Sprintf(`{"ask_id":%q,"answer":"in"}`, a.AskID)); rpcToken(err) != "ASK_CLOSED" {
		t.Fatalf("answer on a closed ask: %v", err)
	}
}

func TestASecondAskForTheSameGameCountsAsIn(t *testing.T) {
	r := newLtpRig(t, 4)
	game := `{"crew_id":"c1","game":{"game_id":"steam-730","name":"Counter-Strike 2"}}`
	a := r.ask("u1", game)
	r.takePushes()

	out, err := r.call(LookingToPlayAskRPC, "u2", game)
	if err != nil || !out.Joined || out.Ask.AskID != a.AskID || out.Ask.State != ltpStateOn {
		t.Fatalf("join: %v %+v", err, out)
	}
	if got := r.takePushes(); !reflect.DeepEqual(got, []string{"u1: user2 is in"}) {
		t.Fatalf("pushes = %v", got)
	}
	if len(r.nk.messages) != 1 {
		t.Fatalf("a join posts no second message: %d", len(r.nk.messages))
	}
	// Another game is an ask of its own, and the first asker cannot ask twice.
	other := `{"crew_id":"c1","game":{"game_id":"steam-892970","name":"Valheim"}}`
	if b := r.ask("u3", other); b.AskID == a.AskID {
		t.Fatal("another game makes a new ask")
	}
	if _, err := r.call(LookingToPlayAskRPC, "u1", other); rpcToken(err) != "ASK_HAS_OPEN" {
		t.Fatalf("second ask of an asker: %v", err)
	}
}

func TestTheLoopClosesAsksOnTime(t *testing.T) {
	r := newLtpRig(t, 3)
	a := r.ask("u1", `{"crew_id":"c1"}`)
	r.takePushes()

	r.now += ltpMin(29)
	ltpTick(context.Background(), testLogger(), r.nk)
	if len(r.takePushes()) != 0 || !ltpCrewHasOpen("c1") {
		t.Fatal("29 minutes: the ask is open")
	}
	r.now += ltpMin(1)
	ltpTick(context.Background(), testLogger(), r.nk)
	if got := r.takePushes(); !reflect.DeepEqual(got, []string{"u1: No one this time"}) {
		t.Fatalf("pushes = %v", got)
	}
	asks, _, _ := readLtpAsks(context.Background(), r.nk, []string{a.AskID})
	if asks[a.AskID].ClosedReason != ltpClosedNobody || ltpCrewHasOpen("c1") {
		t.Fatalf("ask = %+v", asks[a.AskID])
	}

	// An ask that is on ends 60 minutes after it opened, with no push.
	r.now += ltpMin(31)
	b := r.ask("u2", `{"crew_id":"c1"}`)
	r.answer("u3", b.AskID, ltpAnswerIn)
	r.takePushes()
	r.now += ltpMin(60)
	ltpTick(context.Background(), testLogger(), r.nk)
	asks, _, _ = readLtpAsks(context.Background(), r.nk, []string{b.AskID})
	if asks[b.AskID].ClosedReason != ltpClosedDone || len(r.takePushes()) != 0 {
		t.Fatalf("ask = %+v", asks[b.AskID])
	}
}

func TestAfterARestartTheLoopFindsTheOpenAsks(t *testing.T) {
	r := newLtpRig(t, 3)
	r.ask("u1", `{"crew_id":"c1"}`)
	resetLtpOpenCrewsForTests() // a restart loses the memory

	loadLtpOpenCrews(context.Background(), testLogger(), r.nk)
	if !ltpCrewHasOpen("c1") {
		t.Fatal("the crew with an open ask is found in storage")
	}
}

func TestAVoiceJoinCountsAsInAndTwoInVoiceSendTheSecondCall(t *testing.T) {
	r := newLtpRig(t, 5)
	a := r.ask("u1", `{"crew_id":"c1"}`)
	r.answer("u5", a.AskID, ltpAnswerNo)
	r.takePushes()

	// u3 has no answer and joins voice: in, and the asker hears it.
	r.voice["u3"] = true
	onLtpVoiceJoin(context.Background(), testLogger(), r.nk, "c1", "u3")
	asks, _, _ := readLtpAsks(context.Background(), r.nk, []string{a.AskID})
	if got := asks[a.AskID]; got.State != ltpStateOn || got.Answers["u3"].Answer != ltpAnswerIn || got.Answers["u3"].ArrivedAt == 0 {
		t.Fatalf("ask = %+v", got)
	}
	if got := r.takePushes(); !reflect.DeepEqual(got, []string{"u1: user3 is in"}) {
		t.Fatalf("pushes = %v", got)
	}

	// The asker joins too: two of the ask are in voice. The members with no
	// answer get the Second call, once. u5 said no and gets none.
	r.voice["u1"] = true
	onLtpVoiceJoin(context.Background(), testLogger(), r.nk, "c1", "u1")
	want := []string{"u2: user1 and user3 are playing", "u4: user1 and user3 are playing"}
	if got := r.takePushesKeep(); !reflect.DeepEqual(got, want) {
		t.Fatalf("second call = %v, want %v", got, want)
	}
	if n := r.pushes[0].N; n.Type != pushTypeLtp || n.Category != ltpPushCategory {
		t.Fatalf("second call push = %+v", n)
	}
	r.takePushes()
	r.voice["u2"] = true
	onLtpVoiceJoin(context.Background(), testLogger(), r.nk, "c1", "u2")
	for _, p := range r.takePushes() {
		if p != "u1: 2 are in" && p != "u1: user2 and user3 are in" {
			t.Fatalf("no second Second call: %v", p)
		}
	}
}

func TestTheAskPushObeysTheSettingAndTheActivityRule(t *testing.T) {
	r := newLtpRig(t, 4)
	// u2 turned Looking to play off. u3 is at the desktop. u4 has an inactive
	// desktop: an ask does not wait for the grace period.
	r.nk.put(PushPrefsCollection, pushPrefsKey, "u2", pushPrefsDoc{Crews: map[string]crewPushPrefs{"c1": {LookingToPlay: boolPtr(false)}}})
	registerSessionStart("u3")
	setSessionActivity("u3", "s3", sessionActivity{Platform: "desktop", Active: true})
	registerSessionStart("u4")
	setSessionActivity("u4", "s4", sessionActivity{Platform: "desktop", Active: false})

	r.ask("u1", `{"crew_id":"c1"}`)
	if got := r.takePushes(); !reflect.DeepEqual(got, []string{"u4: user1 is looking to play"}) {
		t.Fatalf("pushes = %v", got)
	}
}

func TestInALargeCrewAnAskWithAGameGoesToTheMembersWhoHaveIt(t *testing.T) {
	r := newLtpRig(t, largeCrewMembers+2) // u1..u12
	lib := func(ids ...string) LibraryDoc {
		var d LibraryDoc
		for _, id := range ids {
			d.Games = append(d.Games, LibraryGame{GameID: id, Name: id})
		}
		return d
	}
	r.nk.put(UserLibraryCollection, UserLibraryKey, "u2", lib("steam-730"))
	r.nk.put(UserLibraryCollection, UserLibraryKey, "u3", lib("steam-892970"))
	for i := 5; i <= 12; i++ {
		r.nk.put(UserLibraryCollection, UserLibraryKey, fmt.Sprintf("u%d", i), lib("steam-1"))
	}
	// u2 has the game. u3 and u5..u12 do not. u4 has no library report.
	r.ask("u1", `{"crew_id":"c1","game":{"game_id":"steam-730","name":"Counter-Strike 2"}}`)
	want := []string{"u2: user1 is looking to play Counter-Strike 2", "u4: user1 is looking to play Counter-Strike 2"}
	if got := r.takePushes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pushes = %v, want %v", got, want)
	}

	// A game that no library names: the id of the game catalogue, or a game
	// that no launcher reports. The libraries tell nothing, so every member
	// gets the ask.
	r.ask("u3", `{"crew_id":"c1","game":{"game_id":"counter-strike-2","name":"Counter-Strike 2"}}`)
	if got := r.takePushes(); len(got) != largeCrewMembers+1 {
		t.Fatalf("a game that no library names pushed to %d members, want %d", len(got), largeCrewMembers+1)
	}

	// An ask with no game goes to every member of the large crew.
	r.ask("u2", `{"crew_id":"c1"}`)
	if got := r.takePushes(); len(got) != largeCrewMembers+1 {
		t.Fatalf("an ask with no game pushed to %d members, want %d", len(got), largeCrewMembers+1)
	}
}

func TestListGivesTheOpenAsksAndTheAsksOfTheHistory(t *testing.T) {
	r := newLtpRig(t, 3)
	closed := r.ask("u1", `{"crew_id":"c1"}`)
	if _, err := r.call(LookingToPlayCancelRPC, "u1", fmt.Sprintf(`{"ask_id":%q}`, closed.AskID)); err != nil {
		t.Fatal(err)
	}
	open := r.ask("u2", `{"crew_id":"c1"}`)
	// An ask of another crew that the caller names is not given out.
	other := newLtpAsk("other", "c2", "x1", nil, nil, ltpT0)
	if err := writeLtpAsk(context.Background(), r.nk, other, ""); err != nil {
		t.Fatal(err)
	}

	raw, err := LookingToPlayListRPC(ctxWithUser("u3"), testLogger(), nil, r.nk,
		fmt.Sprintf(`{"crew_id":"c1","ask_ids":[%q,"other","missing"]}`, closed.AskID))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var out ltpListResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range out.Asks {
		ids = append(ids, a.AskID)
	}
	if want := []string{open.AskID, closed.AskID}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("asks = %v, want %v", ids, want)
	}
	if _, err := LookingToPlayListRPC(ctxWithUser("stranger"), testLogger(), nil, r.nk, `{"crew_id":"c1"}`); rpcToken(err) != "NOT_A_MEMBER" {
		t.Fatalf("list by a non-member: %v", err)
	}
}

func TestBadCallsAreRefused(t *testing.T) {
	r := newLtpRig(t, 2)
	cases := []struct{ name, user, payload, token string }{
		{"no crew", "u1", `{}`, "BAD_REQUEST"},
		{"bad game id", "u1", `{"crew_id":"c1","game":{"game_id":"x","name":"X"}}`, "BAD_REQUEST"},
		{"game with no name", "u1", `{"crew_id":"c1","game":{"game_id":"steam-1","name":""}}`, "BAD_REQUEST"},
		{"picked members who are not in the crew", "u1", `{"crew_id":"c1","member_ids":["stranger"]}`, "BAD_REQUEST"},
		{"not a member", "stranger", `{"crew_id":"c1"}`, "NOT_A_MEMBER"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := r.call(LookingToPlayAskRPC, c.user, c.payload); rpcToken(err) != c.token {
				t.Fatalf("got %v, want %s", err, c.token)
			}
		})
	}
	if _, err := r.call(LookingToPlayAnswerRPC, "u1", `{"ask_id":"missing","answer":"in"}`); rpcToken(err) != "ASK_NOT_FOUND" {
		t.Fatalf("answer to a missing ask: %v", err)
	}
	if len(r.nk.messages) != 0 || len(r.pushes) != 0 {
		t.Fatal("a refused call posts and pushes nothing")
	}
}
