package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
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

// ---- stream_thumbnail_upload ----

// thumbnailPayload is a stream_thumbnail_upload request with a small picture.
func thumbnailPayload(crewID, streamID string) string {
	return `{"crew_id":"` + crewID + `","stream_id":"` + streamID + `","thumbnail_base64":"aGVsbG8="}`
}

// The host writes the thumbnail of the live stream of the crew, and only
// under the id of that stream.
func TestStreamThumbnailUpload_TakesOnlyTheLiveStreamOfTheCrew(t *testing.T) {
	nk := seedStreamCrew(t)
	sessionID := startTestStream(t, nk)
	const otherStream = "stream_of_another_crew"

	out, err := StreamThumbnailUploadRPC(ctxWithUser(sfuOnlyUser), testLogger(), nil, nk,
		thumbnailPayload(sfuOnlyCrew, otherStream))

	if rpcCode(err) != 5 {
		t.Fatalf("a thumbnail for another stream id: out=%q err=%v, want NOT_FOUND (5)", out, err)
	}
	if nk.has(ThumbnailCollection, otherStream+"/latest", SystemUserID) {
		t.Fatal("the RPC stored a thumbnail under a stream id that is not the live stream of the crew")
	}
	if meta := readStreamMeta(context.Background(), nk, sfuOnlyCrew); meta == nil || meta.ThumbnailURL != "" {
		t.Fatalf("a refused upload changed the stream record: %+v", meta)
	}

	if _, err := StreamThumbnailUploadRPC(ctxWithUser(sfuOnlyUser), testLogger(), nil, nk,
		thumbnailPayload(sfuOnlyCrew, sessionID)); err != nil {
		t.Fatalf("the host uploads the thumbnail of its live stream: %v", err)
	}
	if !nk.has(ThumbnailCollection, sessionID+"/latest", SystemUserID) {
		t.Fatal("the thumbnail of the live stream is not stored")
	}
	if meta := readStreamMeta(context.Background(), nk, sfuOnlyCrew); meta == nil || meta.ThumbnailURL == "" {
		t.Fatalf("the stream record has no thumbnail URL: %+v", meta)
	}
}

// ---- one answer for an account outside the crew ----

// An account outside the crew gets the same answer from every stream RPC
// that takes a crew id, for a crew with a live stream and for a crew without
// one. Its calls change nothing.
func TestStreamRPCs_AnswerAnOutsiderTheSameWithAndWithoutAStream(t *testing.T) {
	nk := seedStreamCrew(t)
	sessionID := startTestStream(t, nk)
	watchTestStream(t, nk, streamTestViewer, sessionID)

	crew := `"crew_id":"` + sfuOnlyCrew + `"`
	calls := []struct {
		name    string
		rpc     func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error)
		payload string
	}{
		{"start_stream", StartStreamRPC, `{` + crew + `,"title":"x"}`},
		{"stop_stream", StopStreamRPC, `{` + crew + `}`},
		{"stream_live", StreamLiveRPC, `{` + crew + `,"session_id":"` + sessionID + `"}`},
		{"stream_picture", StreamPictureRPC, `{` + crew + `,"session_id":"` + sessionID + `"}`},
		{"stream_thumbnail_upload", StreamThumbnailUploadRPC, thumbnailPayload(sfuOnlyCrew, sessionID)},
		{"update_stream_resolution", UpdateStreamResolutionRPC, `{` + crew + `,"width":1,"height":1}`},
		{"stream_viewer_left", StreamViewerLeftRPC, `{` + crew + `,"viewer_id":"` + streamTestViewer + `"}`},
	}
	answer := func(i int) string {
		out, err := calls[i].rpc(ctxWithUser(streamOutsider), testLogger(), nil, nk, calls[i].payload)
		if err == nil {
			return "ok " + out
		}
		return fmt.Sprintf("code %d %s", rpcCode(err), err.Error())
	}

	live := make([]string, len(calls))
	for i := range calls {
		live[i] = answer(i)
	}
	meta := readStreamMeta(context.Background(), nk, sfuOnlyCrew)
	if meta == nil || meta.StreamID != sessionID || meta.Width != 0 || meta.ThumbnailURL != "" || len(meta.ViewerIDs) != 1 {
		t.Fatalf("the calls of an account outside the crew changed the stream: %+v", meta)
	}

	if _, err := StopStreamRPC(ctxWithUser(sfuOnlyUser), testLogger(), nil, nk, `{`+crew+`}`); err != nil {
		t.Fatalf("stop_stream by the host: %v", err)
	}
	for i, c := range calls {
		if none := answer(i); none != live[i] {
			t.Errorf("%s by an account outside the crew: %q with a live stream, %q without. The answers must be the same", c.name, live[i], none)
		}
	}
}
