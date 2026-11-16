package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// scrubJSON is a value as the JSON a store keeps.
func scrubJSON(t *testing.T, v interface{}) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var norm interface{}
	if err := json.Unmarshal(raw, &norm); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(norm)
	return string(out)
}

// One case for each event type: the user started it, another member started
// it and it names the user, and it does not name the user.
func TestScrubEventForUser(t *testing.T) {
	const gone = "u-gone"
	cases := []struct {
		name string
		ev   CrewEvent
		drop bool
		// want is the data after the scrub. nil: the event is unchanged.
		want interface{}
	}{
		{name: "voice session of three loses one",
			ev: CrewEvent{Type: "voice_session", Data: VoiceSessionData{ChannelID: "c", ChannelName: "Lobby",
				ParticipantIDs: []string{"u1", gone, "u3"}, ParticipantNames: []string{"Ann", "Gone", "Cy"}, DurationMin: 30, PeakCount: 3}},
			want: VoiceSessionData{ChannelID: "c", ChannelName: "Lobby",
				ParticipantIDs: []string{"u1", "u3"}, ParticipantNames: []string{"Ann", "Cy"}, DurationMin: 30, PeakCount: 3}},
		{name: "voice session of two is dropped",
			ev: CrewEvent{Type: "voice_session", Data: VoiceSessionData{
				ParticipantIDs: []string{gone, "u1"}, ParticipantNames: []string{"Gone", "Ann"}}},
			drop: true},
		{name: "voice session without the user",
			ev: CrewEvent{Type: "voice_session", Data: VoiceSessionData{
				ParticipantIDs: []string{"u1"}, ParticipantNames: []string{"Ann"}}}},
		{name: "voice session with names that are not aligned",
			ev: CrewEvent{Type: "voice_session", Data: map[string]interface{}{
				"participant_ids": []string{"u1", gone, "u3"}, "participant_names": []string{"Ann", "Gone"}}},
			want: map[string]interface{}{"participant_ids": []string{"u1", "u3"}, "participant_names": []string{}}},
		{name: "own game session",
			ev:   CrewEvent{Type: "game_session", ActorID: gone, Data: GameSessionData{GameName: "G", PlayerIDs: []string{gone, "u1"}, PlayerNames: []string{"Gone", "Ann"}}},
			drop: true},
		{name: "game session of another member",
			ev: CrewEvent{Type: "game_session", ActorID: "u1", Data: GameSessionData{GameName: "G", DurationMin: 60,
				PlayerIDs: []string{"u1", gone, "u3"}, PlayerNames: []string{"Ann", "Gone", "Cy"}, PlayerOverlapMin: []int{60, 20, 40}}},
			want: GameSessionData{GameName: "G", DurationMin: 60,
				PlayerIDs: []string{"u1", "u3"}, PlayerNames: []string{"Ann", "Cy"}, PlayerOverlapMin: []int{60, 40}}},
		{name: "game session with overlap minutes that are not aligned",
			ev: CrewEvent{Type: "game_session", ActorID: "u1", Data: map[string]interface{}{
				"player_ids": []string{"u1", gone}, "player_names": []string{"Ann", "Gone"}, "player_overlap_min": []int{60}}},
			want: map[string]interface{}{"player_ids": []string{"u1"}, "player_names": []string{"Ann"}}},
		{name: "game session without the user",
			ev: CrewEvent{Type: "game_session", ActorID: "u1", Data: GameSessionData{PlayerIDs: []string{"u1"}, PlayerNames: []string{"Ann"}}}},
		{name: "own stream session",
			ev:   CrewEvent{Type: "stream_session", ActorID: gone, Data: StreamSessionData{SessionID: "s", StreamerID: gone}},
			drop: true},
		{name: "stream session of the user that another actor ended",
			ev:   CrewEvent{Type: "stream_session", ActorID: "u1", Data: StreamSessionData{SessionID: "s", StreamerID: gone}},
			drop: true},
		{name: "stream session the user watched",
			ev: CrewEvent{Type: "stream_session", ActorID: "u1", Data: StreamSessionData{SessionID: "s", StreamerID: "u1", StreamerName: "Ann",
				ViewerIDs: []string{gone, "u3"}, PeakViewers: 2, SnapshotURLs: []string{"a"}}},
			want: StreamSessionData{SessionID: "s", StreamerID: "u1", StreamerName: "Ann",
				ViewerIDs: []string{"u3"}, PeakViewers: 2, SnapshotURLs: []string{"a"}}},
		{name: "own join",
			ev:   CrewEvent{Type: "member_joined", ActorID: gone, Data: MemberJoinedData{Username: "Gone", DisplayName: "Gone"}},
			drop: true},
		{name: "join the user invited",
			ev:   CrewEvent{Type: "member_joined", ActorID: "u1", Data: MemberJoinedData{Username: "Ann", DisplayName: "Ann", InvitedBy: gone, InvitedByName: "Gone"}},
			want: MemberJoinedData{Username: "Ann", DisplayName: "Ann"}},
		{name: "join another member invited",
			ev: CrewEvent{Type: "member_joined", ActorID: "u1", Data: MemberJoinedData{Username: "Ann", DisplayName: "Ann", InvitedBy: "u3", InvitedByName: "Cy"}}},
		{name: "own leave",
			ev:   CrewEvent{Type: "member_left", ActorID: gone, Data: MemberLeftData{Username: "Gone", DisplayName: "Gone"}},
			drop: true},
		{name: "leave of another member",
			ev: CrewEvent{Type: "member_left", ActorID: "u1", Data: MemberLeftData{Username: "Ann", DisplayName: "Ann"}}},
		{name: "own moment",
			ev:   CrewEvent{Type: "moment", ActorID: gone, Data: MomentData{Text: "gg"}},
			drop: true},
		{name: "moment of another member",
			ev: CrewEvent{Type: "moment", ActorID: "u1", Data: MomentData{Text: "gg"}}},
		{name: "chat activity names nobody",
			ev: CrewEvent{Type: "chat_activity", Data: ChatActivityData{MessageCount: 12, ActiveUserCount: 3}}},
		{name: "own clip event of an old ledger",
			ev:   CrewEvent{Type: "clip", ActorID: gone, Data: ClipData{ClipID: "c1", ClipperName: "Gone"}},
			drop: true},
		{name: "clip event of an old ledger, as the store keeps it",
			ev: CrewEvent{Type: "ledger_clip", ActorID: "u1", Data: ClipData{ClipID: "c1", ClipperName: "Ann",
				Participants: []string{"u1", gone}, ParticipantNames: []string{"Ann", "Gone"}}},
			want: map[string]interface{}{"clip_id": "c1", "clip_type": "", "clipper_name": "Ann", "duration_seconds": 0,
				"participants": []string{"u1"}, "participant_names": []string{"Ann"}}},
		{name: "recap event of an old ledger",
			ev: CrewEvent{Type: "weekly_recap", Data: WeeklyRecapData{CrewID: "c", MostActive: "Gone",
				TopMembers: []RecapMember{{UserID: gone, DisplayName: "Gone"}, {UserID: "u1", DisplayName: "Ann"}}}},
			want: WeeklyRecapData{CrewID: "c", TopMembers: []RecapMember{{UserID: "u1", DisplayName: "Ann"}}}},
		{name: "data that is not an object",
			ev: CrewEvent{Type: "moment", ActorID: "u1", Data: "text"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := scrubJSON(t, tc.ev.Data)
			out, drop := scrubEventForUser(tc.ev, gone)
			if drop != tc.drop {
				t.Fatalf("drop = %t, want %t", drop, tc.drop)
			}
			if scrubJSON(t, tc.ev.Data) != before {
				t.Fatal("the scrub changed its input")
			}
			if drop {
				return
			}
			want := before
			if tc.want != nil {
				want = scrubJSON(t, tc.want)
			}
			if got := scrubJSON(t, out.Data); got != want {
				t.Fatalf("data = %s\nwant   %s", got, want)
			}
			// The scrubbed event does not name the user, and a second scrub
			// changes nothing.
			for _, id := range eventUserIDs(out) {
				if id == gone {
					t.Fatalf("the event still names the user: %s", scrubJSON(t, out.Data))
				}
			}
			again, drop := scrubEventForUser(out, gone)
			if drop || scrubJSON(t, again.Data) != scrubJSON(t, out.Data) {
				t.Fatal("a second scrub changed the event")
			}
		})
	}
}

func TestScrubClipForUser(t *testing.T) {
	const gone = "u-gone"
	who := scrubWho{UserID: gone, Names: []string{"Gone"}}
	cases := []struct {
		name string
		clip StoredClip
		drop bool
		want *StoredClip
	}{
		{name: "own clip", clip: StoredClip{ClipID: "c1", ActorID: gone, ClipperName: "Gone"}, drop: true},
		{name: "clip of another member with the voice of the user",
			clip: StoredClip{ClipID: "c1", ActorID: "u1", ClipperName: "Ann", MediaURL: "m",
				Participants: []string{"u1", gone, "u3"}, ParticipantNames: []string{"Ann", "Gone", "Cy"}},
			want: &StoredClip{ClipID: "c1", ActorID: "u1", ClipperName: "Ann", MediaURL: "m",
				Participants: []string{"u1", "u3"}, ParticipantNames: []string{"Ann", "Cy"}}},
		{name: "names not aligned, the name of the user is known",
			clip: StoredClip{ClipID: "c1", ActorID: "u1", Participants: []string{"u1", gone, "u3"}, ParticipantNames: []string{"Ann", "Gone"}},
			want: &StoredClip{ClipID: "c1", ActorID: "u1", Participants: []string{"u1", "u3"}, ParticipantNames: []string{"Ann"}}},
		{name: "names not aligned, the name of the user is not known",
			clip: StoredClip{ClipID: "c1", ActorID: "u1", Participants: []string{"u1", gone, "u3"}, ParticipantNames: []string{"Ann", "Old"}},
			want: &StoredClip{ClipID: "c1", ActorID: "u1", Participants: []string{"u1", "u3"}, ParticipantNames: []string{}}},
		{name: "clip without the user",
			clip: StoredClip{ClipID: "c1", ActorID: "u1", Participants: []string{"u1"}, ParticipantNames: []string{"Ann"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := scrubJSON(t, tc.clip)
			out, drop := scrubClipForUser(tc.clip, who)
			if drop != tc.drop {
				t.Fatalf("drop = %t, want %t", drop, tc.drop)
			}
			if scrubJSON(t, tc.clip) != before {
				t.Fatal("the scrub changed its input")
			}
			if drop {
				return
			}
			want := before
			if tc.want != nil {
				want = scrubJSON(t, *tc.want)
			}
			if got := scrubJSON(t, out); got != want {
				t.Fatalf("clip = %s\nwant   %s", got, want)
			}
		})
	}
}

func TestScrubStreamSessionForUser(t *testing.T) {
	who := scrubWho{UserID: "u-gone"}
	if _, drop := scrubStreamSessionForUser(StoredStreamSession{SessionID: "s", StreamerID: "u-gone"}, who); !drop {
		t.Fatal("the stream session of the user is not dropped")
	}
	s := StoredStreamSession{SessionID: "s", StreamerID: "u1", StreamerName: "Ann"}
	if out, drop := scrubStreamSessionForUser(s, who); drop || !reflect.DeepEqual(out, s) {
		t.Fatalf("the stream session of another member changed: %+v drop=%t", out, drop)
	}
}

// Every field of a recap that names a user, one case for each.
func TestScrubRecapForUser(t *testing.T) {
	const gone = "u-gone"
	base := func() WeeklyRecapData {
		return WeeklyRecapData{CrewID: "c", WeekStart: 1000, TotalMin: 90, SessionCount: 4, ClipCount: 2, TopGame: "G"}
	}
	cases := []struct {
		name string
		who  scrubWho
		in   func(*WeeklyRecapData)
		want func(*WeeklyRecapData) // nil: unchanged
	}{
		{name: "top_members by id",
			in: func(r *WeeklyRecapData) {
				r.TopMembers = []RecapMember{{UserID: gone, DisplayName: "Gone", HangoutMin: 50}, {UserID: "u1", DisplayName: "Ann", HangoutMin: 40}}
			},
			want: func(r *WeeklyRecapData) {
				r.TopMembers = []RecapMember{{UserID: "u1", DisplayName: "Ann", HangoutMin: 40}}
			}},
		{name: "top_members of an old recap, by name",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.TopMembers = []RecapMember{{DisplayName: "Gone"}, {DisplayName: "Ann"}}
			},
			want: func(r *WeeklyRecapData) { r.TopMembers = []RecapMember{{DisplayName: "Ann"}} }},
		{name: "most_active takes the name beside the id in the same recap",
			in: func(r *WeeklyRecapData) {
				r.MostActive = "Old Name"
				r.TopMembers = []RecapMember{{UserID: gone, DisplayName: "Old Name"}}
			},
			want: func(r *WeeklyRecapData) { r.TopMembers = []RecapMember{} }},
		{name: "most_active of another member",
			in: func(r *WeeklyRecapData) { r.MostActive = "Ann" }},
		{name: "most_clipped by id",
			in: func(r *WeeklyRecapData) {
				r.MostClipped, r.MostClippedUserID, r.MostClippedCount = "Gone", gone, 5
			},
			want: func(r *WeeklyRecapData) {}},
		{name: "most_clipped of an old recap, by name",
			who:  scrubWho{Names: []string{"Gone"}},
			in:   func(r *WeeklyRecapData) { r.MostClipped = "Gone" },
			want: func(r *WeeklyRecapData) {}},
		{name: "most_clipped of another member with the same name stays when it has an id",
			who: scrubWho{Names: []string{"Sam"}},
			in: func(r *WeeklyRecapData) {
				r.MostClipped, r.MostClippedUserID, r.MostClippedCount = "Sam", "u1", 3
			}},
		{name: "game_records",
			in: func(r *WeeklyRecapData) {
				r.GameRecords = []RecapGameRecord{{UserID: "u1", DisplayName: "Ann", Wins: 2}, {UserID: gone, DisplayName: "Gone", Wins: 1}}
			},
			want: func(r *WeeklyRecapData) {
				r.GameRecords = []RecapGameRecord{{UserID: "u1", DisplayName: "Ann", Wins: 2}}
			}},
		{name: "best_streak_name",
			who:  scrubWho{Names: []string{"Gone"}},
			in:   func(r *WeeklyRecapData) { r.BestStreak, r.BestStreakName = 4, "Gone" },
			want: func(r *WeeklyRecapData) {}},
		{name: "a name field that holds the user id",
			in:   func(r *WeeklyRecapData) { r.BestStreak, r.BestStreakName = 4, gone },
			want: func(r *WeeklyRecapData) {}},
		{name: "awards",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.Awards = []RecapAward{
					{Kind: "grinder", Title: "Grinder of the week", Name: "Gone", Detail: "9 matches", Value: 9},
					{Kind: "heater", Title: "Biggest heater", Name: "Ann", Detail: "3-win streak", Value: 3},
					{Kind: "belt", Title: "Most hours", Name: "Ann", Detail: "took it from Gone"},
					{Kind: "belt", Title: "Most games", Name: "Gone", Detail: "took it from Ann"},
				}
			},
			want: func(r *WeeklyRecapData) {
				r.Awards = []RecapAward{
					{Kind: "heater", Title: "Biggest heater", Name: "Ann", Detail: "3-win streak", Value: 3},
					{Kind: "belt", Title: "Most hours", Name: "Ann"},
				}
			}},
		{name: "belts_moved",
			in: func(r *WeeklyRecapData) {
				r.BeltsMoved = []RecapBeltMoved{
					{TitleID: "t1", HolderUserID: gone, HolderName: "Gone", PreviousUserID: "u1", PreviousName: "Ann"},
					{TitleID: "t2", HolderUserID: "u1", HolderName: "Ann", PreviousUserID: gone, PreviousName: "Gone", MovedAtMs: 5},
					{TitleID: "t3", HolderUserID: "u1", HolderName: "Ann", PreviousUserID: "u3", PreviousName: "Cy"},
				}
			},
			want: func(r *WeeklyRecapData) {
				r.BeltsMoved = []RecapBeltMoved{
					{TitleID: "t2", HolderUserID: "u1", HolderName: "Ann", MovedAtMs: 5},
					{TitleID: "t3", HolderUserID: "u1", HolderName: "Ann", PreviousUserID: "u3", PreviousName: "Cy"},
				}
			}},
		{name: "belts_held and leaderboard",
			in: func(r *WeeklyRecapData) {
				rows := []RecapBeltHeld{{UserID: gone, DisplayName: "Gone", Count: 2, TitleIDs: []string{"a", "b"}}, {UserID: "u1", DisplayName: "Ann", Count: 1, TitleIDs: []string{"c"}}}
				r.BeltsHeld, r.Leaderboard = rows, rows
			},
			want: func(r *WeeklyRecapData) {
				rows := []RecapBeltHeld{{UserID: "u1", DisplayName: "Ann", Count: 1, TitleIDs: []string{"c"}}}
				r.BeltsHeld, r.Leaderboard = rows, rows
			}},
		{name: "clip_of_week is a dropped clip of the user",
			who: scrubWho{ClipIDs: map[string]bool{"c9": true}},
			in: func(r *WeeklyRecapData) {
				r.ClipOfWeek = &RecapClipOfWeek{ClipID: "c9", Title: "Old Name clipped that", ClipperName: "Old Name", MediaURL: "m"}
			},
			want: func(r *WeeklyRecapData) {}},
		{name: "clip_of_week by clipper name",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.ClipOfWeek = &RecapClipOfWeek{ClipID: "c9", Title: "Gone clipped that", ClipperName: "Gone"}
			},
			want: func(r *WeeklyRecapData) {}},
		{name: "clip_of_week of another member with the voice of the user",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.ClipOfWeek = &RecapClipOfWeek{ClipID: "c2", Title: "Ann clipped that", ClipperName: "Ann", ParticipantNames: []string{"Ann", "Gone"}, MediaURL: "m"}
			},
			want: func(r *WeeklyRecapData) {
				r.ClipOfWeek = &RecapClipOfWeek{ClipID: "c2", Title: "Ann clipped that", ClipperName: "Ann", ParticipantNames: []string{"Ann"}, MediaURL: "m"}
			}},
		{name: "longest_session",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.LongestSession, r.LongestSessionMin = "Ann, Gone in Lobby (90m)", 90
			},
			want: func(r *WeeklyRecapData) { r.LongestSessionMin = 90 }},
		{name: "longest_session of other members",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.LongestSession, r.LongestSessionMin = "Ann, Cy in Lobby (90m)", 90
			}},
		{name: "a recap that does not name the user",
			who: scrubWho{Names: []string{"Gone"}},
			in: func(r *WeeklyRecapData) {
				r.MostActive = "Ann"
				r.TopMembers = []RecapMember{{UserID: "u1", DisplayName: "Ann"}}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.in(&in)
			before := scrubJSON(t, in)
			who := tc.who
			who.UserID = gone
			out, changed := scrubRecapForUser(in, who)
			if scrubJSON(t, in) != before {
				t.Fatal("the scrub changed its input")
			}
			if changed != (tc.want != nil) {
				t.Fatalf("changed = %t", changed)
			}
			want := before
			if tc.want != nil {
				w := base()
				tc.want(&w)
				want = scrubJSON(t, w)
			}
			if got := scrubJSON(t, out); got != want {
				t.Fatalf("recap = %s\nwant    %s", got, want)
			}
			if again, changed := scrubRecapForUser(out, who); changed || scrubJSON(t, again) != scrubJSON(t, out) {
				t.Fatal("a second scrub changed the recap")
			}
			for _, id := range recapUserIDs(out) {
				if id == gone {
					t.Fatal("the recap still holds the user id")
				}
			}
		})
	}
}

// The user index of the store holds the members a recap lists.
func TestEventUserIDsOfARecap(t *testing.T) {
	row := recapRow(WeeklyRecapData{WeekStart: 1000, MostClippedUserID: "a",
		TopMembers:  []RecapMember{{UserID: "b"}},
		GameRecords: []RecapGameRecord{{UserID: "c"}},
		BeltsMoved:  []RecapBeltMoved{{HolderUserID: "d", PreviousUserID: "e"}},
		BeltsHeld:   []RecapBeltHeld{{UserID: "f"}},
		Leaderboard: []RecapBeltHeld{{UserID: "f"}},
	})
	if got := eventUserIDs(row.Event); !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Fatalf("user ids of the recap = %v", got)
	}
}

func TestScrubNamesOfEvent(t *testing.T) {
	got := scrubNamesOfEvent(CrewEvent{Type: "game_session", ActorID: "u1", Data: GameSessionData{
		PlayerIDs: []string{"u1", "u-gone"}, PlayerNames: []string{"Ann", "Old Name"}}}, "u-gone")
	if !reflect.DeepEqual(got, []string{"Old Name"}) {
		t.Fatalf("names = %v", got)
	}
	got = scrubNamesOfEvent(clipRow(StoredClip{EventID: "e", ClipID: "c", ActorID: "u-gone", ClipperName: "Clipper"}).Event, "u-gone")
	if !reflect.DeepEqual(got, []string{"Clipper"}) {
		t.Fatalf("names of a clip = %v", got)
	}
}

func TestScrubHead2headForUser(t *testing.T) {
	const gone = "u-gone"
	doc := Head2headDoc{
		CrewID:  "c",
		Members: []string{"u1", gone, "u3"},
		Titles: []Head2headTitle{
			{TitleID: "held", HolderUserID: gone, Value: 9.0, Unit: "h", SinceMs: 5, PreviousHolderUserID: "u1", MovedAtMs: 5},
			{TitleID: "taken", HolderUserID: "u1", Value: 7.0, Unit: "h", SinceMs: 8, PreviousHolderUserID: gone, MovedAtMs: 8},
			{TitleID: "other", HolderUserID: "u3", Value: 2.0, Unit: "h", SinceMs: 3, PreviousHolderUserID: "u1", MovedAtMs: 3},
		},
		Pairs: []Head2headPair{
			{A: "u1", B: gone, ACount: 1, BCount: 1, Values: []Head2headPairValue{}},
			{A: "u1", B: "u3", ACount: 2, Values: []Head2headPairValue{}},
			{A: gone, B: "u3", Values: []Head2headPairValue{}},
		},
		MemberCount: 3, UpdatedAt: 10,
	}
	before := scrubJSON(t, doc)
	out, changed := scrubHead2headForUser(doc, gone)
	if !changed || scrubJSON(t, doc) != before {
		t.Fatalf("changed=%t, or the scrub changed its input", changed)
	}
	want := Head2headDoc{
		CrewID:  "c",
		Members: []string{"u1", "u3"},
		Titles: []Head2headTitle{
			// The title the user held is vacant: it is not in the document.
			{TitleID: "taken", HolderUserID: "u1", Value: 7.0, Unit: "h", SinceMs: 8, MovedAtMs: 8},
			{TitleID: "other", HolderUserID: "u3", Value: 2.0, Unit: "h", SinceMs: 3, PreviousHolderUserID: "u1", MovedAtMs: 3},
		},
		Pairs:       []Head2headPair{{A: "u1", B: "u3", ACount: 2, Values: []Head2headPairValue{}}},
		MemberCount: 3, UpdatedAt: 10,
	}
	if got := scrubJSON(t, out); got != scrubJSON(t, want) {
		t.Fatalf("doc = %s\nwant  %s", got, scrubJSON(t, want))
	}
	if strings.Contains(scrubJSON(t, out), gone) {
		t.Fatal("the document still holds the user id")
	}
	if again, changed := scrubHead2headForUser(out, gone); changed || scrubJSON(t, again) != scrubJSON(t, out) {
		t.Fatal("a second scrub changed the document")
	}
	// The recap of the next week makes its belts from the document.
	moved, held := buildRecapBelts(&out, 0, 100, func(id string) string { return id })
	if strings.Contains(scrubJSON(t, moved)+scrubJSON(t, held), gone) {
		t.Fatalf("the belts of a recap name the user: %s %s", scrubJSON(t, moved), scrubJSON(t, held))
	}
	// The next recompute gives the vacant title as a first award, not a move.
	next := computeHead2head(h2hInput{CrewID: "c", NowMs: 50, Prev: &out})
	for _, tt := range next.Titles {
		if tt.PreviousHolderUserID == gone || tt.HolderUserID == gone {
			t.Fatalf("the recompute names the user: %+v", tt)
		}
	}
}
