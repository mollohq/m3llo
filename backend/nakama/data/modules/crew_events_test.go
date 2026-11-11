package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamSessionDataToObjectKeepsJSONObjectShape(t *testing.T) {
	data := StreamSessionData{
		SessionID:    "stream_test_1",
		StreamerID:   "user_1",
		StreamerName: "alice",
		Title:        "test stream",
		DurationMin:  12,
		PeakViewers:  3,
		ViewerIDs:    []string{"user_2"},
		SnapshotURLs: []string{"https://cdn.example/snapshots/1.jpg"},
	}

	obj, err := streamSessionDataToObject(data)
	if err != nil {
		t.Fatalf("streamSessionDataToObject returned error: %v", err)
	}

	encoded, err := json.Marshal(map[string]interface{}{"data": obj})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	dataObj, ok := parsed["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("data is not a JSON object: %T", parsed["data"])
	}
	if got := dataObj["session_id"]; got != "stream_test_1" {
		t.Fatalf("session_id mismatch: got %v", got)
	}
	if _, ok := dataObj["snapshot_urls"].([]interface{}); !ok {
		t.Fatalf("snapshot_urls is not an array: %T", dataObj["snapshot_urls"])
	}
}

func TestDecodeStreamSessionDataFromObject(t *testing.T) {
	expected := StreamSessionData{
		SessionID:    "stream_test_2",
		StreamerID:   "user_2",
		StreamerName: "bob",
		Title:        "another stream",
		DurationMin:  7,
		PeakViewers:  2,
		ViewerIDs:    []string{"user_3"},
		SnapshotURLs: []string{"https://cdn.example/snapshots/2.jpg"},
	}

	obj, err := streamSessionDataToObject(expected)
	if err != nil {
		t.Fatalf("streamSessionDataToObject returned error: %v", err)
	}

	decoded, err := decodeStreamSessionData(obj)
	if err != nil {
		t.Fatalf("decodeStreamSessionData returned error: %v", err)
	}

	if decoded.SessionID != expected.SessionID {
		t.Fatalf("session_id mismatch: got %q want %q", decoded.SessionID, expected.SessionID)
	}
	if decoded.StreamerID != expected.StreamerID {
		t.Fatalf("streamer_id mismatch: got %q want %q", decoded.StreamerID, expected.StreamerID)
	}
	if len(decoded.SnapshotURLs) != len(expected.SnapshotURLs) || decoded.SnapshotURLs[0] != expected.SnapshotURLs[0] {
		t.Fatalf("snapshot_urls mismatch: got %v want %v", decoded.SnapshotURLs, expected.SnapshotURLs)
	}
}

func TestClampSessionOutcome(t *testing.T) {
	cases := []struct{ in, want int }{
		{-5, 0},
		{0, 0},
		{7, 7},
		{maxSessionOutcomes, maxSessionOutcomes},
		{maxSessionOutcomes + 1, maxSessionOutcomes},
		{1000000, maxSessionOutcomes},
	}
	for _, c := range cases {
		if got := clampSessionOutcome(c.in); got != c.want {
			t.Fatalf("clampSessionOutcome(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClampGameSessionDurations(t *testing.T) {
	cases := []struct {
		duration, active, wantDuration, wantActive int
	}{
		{-10, -5, 0, 0},
		{0, 0, 0, 0},
		{90, 60, 90, 60},
		{1440, 1440, 1440, 1440},
		{1441, 2000, 1440, 1440},
		{60, 90, 60, 60},
		{1000000, 500000, 1440, 1440},
	}
	for _, c := range cases {
		gotDuration, gotActive := clampGameSessionDurations(c.duration, c.active)
		if gotDuration != c.wantDuration || gotActive != c.wantActive {
			t.Fatalf("clampGameSessionDurations(%d, %d) = (%d, %d), want (%d, %d)",
				c.duration, c.active, gotDuration, gotActive, c.wantDuration, c.wantActive)
		}
	}
}

// The catch-up names the actor of a moment and of a clip in actor_name, so
// the client can write the sentence in the language of the user. The English
// catchup_text does not change.
func TestCatchupEventsCarryTheActorName(t *testing.T) {
	nk := newFakeGuestNk()
	nk.addMember("crew", "u1", "Ann")
	nk.addMember("crew", "u2", "Bo")
	events := []CrewEvent{
		{ID: "m", Type: "moment", ActorID: "u1", Timestamp: 300, Score: 30,
			Data: MomentData{Sentiment: "win", GameName: "Valorant"}},
		{ID: "c", Type: "clip", ActorID: "u2", Timestamp: 200, Score: 20,
			Data: StoredClip{ClipType: "voice", DurationSeconds: 12}},
		{ID: "v", Type: "voice_session", ActorID: "u1", Timestamp: 100, Score: 10,
			Data: VoiceSessionData{ChannelName: "General", ParticipantNames: []string{"Ann", "Bo"}, DurationMin: 45}},
	}

	resp := buildCatchup(events, 0, "crew", context.Background(), nk)

	if len(resp.TopEvents) != 3 {
		t.Fatalf("top_events: %+v", resp.TopEvents)
	}
	names := map[string]string{}
	for _, e := range resp.TopEvents {
		names[e.Type] = e.ActorName
	}
	if names["moment"] != "Ann" || names["clip"] != "Bo" {
		t.Errorf("actor names: %+v", names)
	}
	if names["voice_session"] != "" {
		t.Errorf("a voice session has no actor name: %q", names["voice_session"])
	}
	wantText := "Ann had a win in Valorant, Bo clipped that (12s voice), and Ann and Bo hung out in General for 45m"
	if resp.CatchupText != wantText {
		t.Errorf("catchup_text:\n got %q\nwant %q", resp.CatchupText, wantText)
	}
	raw, _ := json.Marshal(resp)
	if !strings.Contains(string(raw), `"actor_name":"Ann"`) || strings.Count(string(raw), `"actor_name"`) != 2 {
		t.Errorf("catch-up JSON: %s", raw)
	}
}

// An actor that the server does not find has no actor_name. The English text
// says "someone".
func TestCatchupActorNotFoundHasNoName(t *testing.T) {
	nk := newFakeGuestNk()
	events := []CrewEvent{
		{ID: "c", Type: "clip", ActorID: "gone", Timestamp: 100, Score: 20,
			Data: StoredClip{ClipType: "game", DurationSeconds: 30}},
	}
	resp := buildCatchup(events, 0, "crew", context.Background(), nk)
	if len(resp.TopEvents) != 1 || resp.TopEvents[0].ActorName != "" {
		t.Fatalf("top_events: %+v", resp.TopEvents)
	}
	if resp.CatchupText != "someone clipped that (30s game)" {
		t.Errorf("catchup_text: %q", resp.CatchupText)
	}
}
