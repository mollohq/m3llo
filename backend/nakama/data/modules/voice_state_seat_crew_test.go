package main

import "testing"

// voice_speaking and voice_mute_state send the voice update to the crew of
// the caller's seat. The crew id of the request does not choose the crew.

const (
	seatCrew    = "crew-of-the-seat"
	requestCrew = "crew-of-the-request"
	seatChannel = "ch-of-the-seat"
	seatUser    = "user-with-a-seat"
)

// takeVoiceDirty returns the crews with a queued voice update and empties
// the queue.
func takeVoiceDirty() map[string]bool {
	voiceDirtyMu.Lock()
	defer voiceDirtyMu.Unlock()
	out := voiceDirty
	voiceDirty = make(map[string]bool)
	return out
}

func wantOnlySeatCrewDirty(t *testing.T, rpc string) {
	t.Helper()
	dirty := takeVoiceDirty()
	if !dirty[seatCrew] {
		t.Errorf("%s: no voice update queued for the crew of the seat", rpc)
	}
	if dirty[requestCrew] {
		t.Errorf("%s: a voice update is queued for the crew of the request, where the caller has no seat", rpc)
	}
}

func TestVoiceSpeaking_UpdatesTheCrewOfTheSeat(t *testing.T) {
	resetVoiceState()
	takeVoiceDirty()
	seatInVoice(seatChannel, seatCrew, VoiceMemberState{UserID: seatUser, Username: "Seat"})

	if _, err := VoiceSpeakingRPC(ctxWithUser(seatUser), testLogger(), nil, nil,
		`{"crew_id":"`+requestCrew+`","speaking":true}`); err != nil {
		t.Fatal(err)
	}

	if m := voiceMember(seatChannel, seatUser); !m.Speaking {
		t.Fatalf("the speaking state of the seat is not stored: %+v", m)
	}
	wantOnlySeatCrewDirty(t, "voice_speaking")
}

func TestVoiceMuteState_UpdatesTheCrewOfTheSeat(t *testing.T) {
	resetVoiceState()
	takeVoiceDirty()
	seatInVoice(seatChannel, seatCrew, VoiceMemberState{UserID: seatUser, Username: "Seat"})

	if _, err := VoiceMuteStateRPC(ctxWithUser(seatUser), testLogger(), nil, nil,
		`{"crew_id":"`+requestCrew+`","muted":true,"deafened":true}`); err != nil {
		t.Fatal(err)
	}

	if m := voiceMember(seatChannel, seatUser); !m.Muted || !m.Deafened {
		t.Fatalf("the mute state of the seat is not stored: %+v", m)
	}
	wantOnlySeatCrewDirty(t, "voice_mute_state")
}

// A caller with no seat queues no voice update for any crew.
func TestVoiceState_ACallerWithNoSeatQueuesNoUpdate(t *testing.T) {
	resetVoiceState()
	takeVoiceDirty()

	if _, err := VoiceSpeakingRPC(ctxWithUser(seatUser), testLogger(), nil, nil,
		`{"crew_id":"`+requestCrew+`","speaking":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := VoiceMuteStateRPC(ctxWithUser(seatUser), testLogger(), nil, nil,
		`{"crew_id":"`+requestCrew+`","muted":true}`); err != nil {
		t.Fatal(err)
	}

	if dirty := takeVoiceDirty(); len(dirty) != 0 {
		t.Fatalf("a caller with no seat queued voice updates for %v", dirty)
	}
}
