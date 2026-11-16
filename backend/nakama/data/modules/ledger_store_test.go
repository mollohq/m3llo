package main

import (
	"context"
	"reflect"
	"testing"
)

func TestMemLedgerConformance(t *testing.T) {
	if err := ledgerStoreConformance(context.Background(), newMemLedgerStore()); err != nil {
		t.Fatal(err)
	}
}

func TestEventUserIDs(t *testing.T) {
	cases := []struct {
		name string
		ev   CrewEvent
		want []string
	}{
		{"voice session", CrewEvent{Type: "voice_session", Data: VoiceSessionData{ParticipantIDs: []string{"u1", "u2"}}}, []string{"u1", "u2"}},
		{"game session, actor first and once", CrewEvent{Type: "game_session", ActorID: "u2", Data: GameSessionData{PlayerIDs: []string{"u1", "u2", "u3"}}}, []string{"u2", "u1", "u3"}},
		{"stream session", CrewEvent{Type: "stream_session", ActorID: "u1", Data: StreamSessionData{StreamerID: "u1", ViewerIDs: []string{"u4", ""}}}, []string{"u1", "u4"}},
		{"member joined with an inviter", CrewEvent{Type: "member_joined", ActorID: "u5", Data: MemberJoinedData{InvitedBy: "u1"}}, []string{"u5", "u1"}},
		{"clip participants", CrewEvent{Type: "clip", ActorID: "u1", Data: ClipData{Participants: []string{"u2", "u1"}}}, []string{"u1", "u2"}},
		{"data read back from JSON", CrewEvent{Type: "voice_session", Data: map[string]interface{}{"participant_ids": []interface{}{"u7", "u8"}}}, []string{"u7", "u8"}},
		{"chat activity names nobody", CrewEvent{Type: "chat_activity", Data: ChatActivityData{MessageCount: 12}}, []string{}},
		{"no data", CrewEvent{Type: "moment", ActorID: "u1"}, []string{"u1"}},
		{"data that is not an object", CrewEvent{Type: "moment", ActorID: "u1", Data: "text"}, []string{"u1"}},
	}
	for _, c := range cases {
		if got := eventUserIDs(c.ev); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEventRef(t *testing.T) {
	if got := eventRef(CrewEvent{Type: "stream_session", Data: StreamSessionData{SessionID: "s1"}}); got != "s1" {
		t.Errorf("stream session ref = %q, want s1", got)
	}
	if got := eventRef(CrewEvent{Type: "game_session", Data: map[string]interface{}{"session_id": "s1"}}); got != "" {
		t.Errorf("game session ref = %q, want empty", got)
	}
}
