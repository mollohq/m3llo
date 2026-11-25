package main

import (
	"context"
	"testing"
)

// The stream RPCs that take a crew id answer a member of the crew only. The
// fixture is in streaming_test.go.

// streamOutsider is an account with no row in the crew of the fixture.
const streamOutsider = "user-not-in-crew"

// crewEventsNamed counts the crew events with this name that the user got.
func crewEventsNamed(userID, crewID, event string) int {
	feedNotesMu.Lock()
	defer feedNotesMu.Unlock()
	n := 0
	for _, note := range feedNotes {
		if note.userID == userID && note.code == NotifyCrewEvent &&
			note.content["crew_id"] == crewID && note.content["event"] == event {
			n++
		}
	}
	return n
}

// watchCrewPushes makes the user a subscriber with the crew open.
func watchCrewPushes(t *testing.T, userID, crewID string) {
	t.Helper()
	openCrew(userID, crewID)
	t.Cleanup(func() { CleanupUser(userID) })
}

// ---- stop_stream ----

// A stop from an account outside the crew writes nothing and pushes nothing.
func TestStopStream_ACallerOutsideTheCrewChangesNothing(t *testing.T) {
	nk := seedStreamCrew(t)
	watchCrewPushes(t, streamTestViewer, sfuOnlyCrew)
	ctx := context.Background()
	if err := WritePresence(ctx, nk, &UserPresence{
		UserID:   streamOutsider,
		Status:   StatusDoNotDisturb,
		Activity: &Activity{Type: ActivityPlaying},
	}); err != nil {
		t.Fatal(err)
	}
	before := crewEventsNamed(streamTestViewer, sfuOnlyCrew, "stream_ended")

	out, err := StopStreamRPC(ctxWithUser(streamOutsider), testLogger(), nil, nk,
		`{"crew_id":"`+sfuOnlyCrew+`"}`)

	if rpcCode(err) != 7 {
		t.Fatalf("stop_stream by an account outside the crew: out=%q err=%v, want PERMISSION_DENIED (7)", out, err)
	}
	if got := crewEventsNamed(streamTestViewer, sfuOnlyCrew, "stream_ended") - before; got != 0 {
		t.Fatalf("the crew got %d stream_ended events from an account outside the crew", got)
	}
	p, err := ReadPresence(ctx, nk, streamOutsider)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusDoNotDisturb || p.Activity == nil || p.Activity.Type != ActivityPlaying {
		t.Fatalf("a refused stop_stream changed the presence of the caller: %+v", p)
	}
}

// A member with no live stream keeps the answer it had.
func TestStopStream_AMemberWithNoStreamGetsTheEmptyAnswer(t *testing.T) {
	nk := seedStreamCrew(t)
	out, err := StopStreamRPC(ctxWithUser(streamTestViewer), testLogger(), nil, nk,
		`{"crew_id":"`+sfuOnlyCrew+`"}`)
	if err != nil || out != "{}" {
		t.Fatalf("stop_stream by a member with no stream: out=%q err=%v, want {}", out, err)
	}
}
