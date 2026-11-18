package main

// Looking to play: storage, the notification to clients and the loop that
// closes asks on time (specs/features/LOOKING-TO-PLAY.md section 3.3).
//
// An ask follows the reactions pattern (chat_reactions.go): one stored state
// for each ask, changed with a version check, and the whole new state sent to
// every crew member as a notification.
//
// The set of crews with an open ask lives in memory on the single Nakama
// instance, the same as voice state. It is built again from storage at start.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// LookingToPlayCollection holds one system-owned doc for each ask.
	LookingToPlayCollection = "looking_to_play"
	// LookingToPlayOpenCollection holds one system-owned doc for each crew:
	// its open asks and the last ask of each member.
	LookingToPlayOpenCollection = "looking_to_play_open"

	ltpWriteTries   = 3
	ltpLoopInterval = 15 * time.Second
	// ltpLastKeep is how long the index keeps the last ask of a member. It
	// is longer than both waits between asks.
	ltpLastKeep = time.Hour
)

var errLtpNotFound = errors.New("no such ask")

// ltpNowMs is the clock of Looking to play; tests replace it.
var ltpNowMs = func() int64 { return time.Now().UnixMilli() }

// ltpCrewIndex is the doc of one crew in LookingToPlayOpenCollection.
type ltpCrewIndex struct {
	Open []string              `json:"open"`
	Last map[string]ltpLastAsk `json:"last,omitempty"`
}

// ---------------------------------------------------------------------------
// Crews with an open ask (memory)
// ---------------------------------------------------------------------------

var (
	ltpOpenCrews   = map[string]bool{}
	ltpOpenCrewsMu sync.Mutex
)

func ltpMarkCrewOpen(crewID string) {
	ltpOpenCrewsMu.Lock()
	ltpOpenCrews[crewID] = true
	ltpOpenCrewsMu.Unlock()
}

func ltpForgetCrew(crewID string) {
	ltpOpenCrewsMu.Lock()
	delete(ltpOpenCrews, crewID)
	ltpOpenCrewsMu.Unlock()
}

// ltpCrewHasOpen reads memory only. The voice join path calls it for every
// join, so it must not touch storage.
func ltpCrewHasOpen(crewID string) bool {
	ltpOpenCrewsMu.Lock()
	defer ltpOpenCrewsMu.Unlock()
	return ltpOpenCrews[crewID]
}

func ltpOpenCrewIDs() []string {
	ltpOpenCrewsMu.Lock()
	defer ltpOpenCrewsMu.Unlock()
	out := make([]string, 0, len(ltpOpenCrews))
	for id := range ltpOpenCrews {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func resetLtpOpenCrewsForTests() {
	ltpOpenCrewsMu.Lock()
	ltpOpenCrews = map[string]bool{}
	ltpOpenCrewsMu.Unlock()
}

// ---------------------------------------------------------------------------
// Storage
// ---------------------------------------------------------------------------

func readLtpAsks(ctx context.Context, nk runtime.NakamaModule, askIDs []string) (map[string]*ltpAsk, map[string]string, error) {
	asks := make(map[string]*ltpAsk, len(askIDs))
	versions := make(map[string]string, len(askIDs))
	if len(askIDs) == 0 {
		return asks, versions, nil
	}
	reads := make([]*runtime.StorageRead, 0, len(askIDs))
	for _, id := range askIDs {
		reads = append(reads, &runtime.StorageRead{Collection: LookingToPlayCollection, Key: id, UserID: SystemUserID})
	}
	objects, err := nk.StorageRead(ctx, reads)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range objects {
		var a ltpAsk
		if err := json.Unmarshal([]byte(o.GetValue()), &a); err != nil || a.AskID == "" {
			continue
		}
		if a.Answers == nil {
			a.Answers = map[string]ltpAnswer{}
		}
		asks[a.AskID] = &a
		versions[a.AskID] = o.GetVersion()
	}
	return asks, versions, nil
}

func writeLtpAsk(ctx context.Context, nk runtime.NakamaModule, a *ltpAsk, version string) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      LookingToPlayCollection,
		Key:             a.AskID,
		UserID:          SystemUserID,
		Value:           string(data),
		Version:         version,
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	return err
}

// changeLtpAsk reads an ask, lets mutate change it, and writes it with the
// version it read. It reads again when another write came first. mutate
// returns false for no change: the ask is then not written.
func changeLtpAsk(ctx context.Context, nk runtime.NakamaModule, askID string, mutate func(a *ltpAsk) (bool, error)) (*ltpAsk, error) {
	var lastErr error
	for try := 0; try < ltpWriteTries; try++ {
		asks, versions, err := readLtpAsks(ctx, nk, []string{askID})
		if err != nil {
			return nil, err
		}
		a := asks[askID]
		if a == nil {
			return nil, errLtpNotFound
		}
		changed, err := mutate(a)
		if err != nil {
			return a, err
		}
		if !changed {
			return a, nil
		}
		if lastErr = writeLtpAsk(ctx, nk, a, versions[askID]); lastErr == nil {
			return a, nil
		}
	}
	return nil, lastErr
}

func readLtpIndex(ctx context.Context, nk runtime.NakamaModule, crewID string) (*ltpCrewIndex, string) {
	idx := &ltpCrewIndex{Last: map[string]ltpLastAsk{}}
	objects, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: LookingToPlayOpenCollection, Key: crewID, UserID: SystemUserID,
	}})
	if err != nil || len(objects) == 0 {
		return idx, ""
	}
	if err := json.Unmarshal([]byte(objects[0].GetValue()), idx); err != nil {
		return &ltpCrewIndex{Last: map[string]ltpLastAsk{}}, ""
	}
	if idx.Last == nil {
		idx.Last = map[string]ltpLastAsk{}
	}
	return idx, objects[0].GetVersion()
}

// changeLtpIndex changes the index of a crew with a version check.
func changeLtpIndex(ctx context.Context, nk runtime.NakamaModule, crewID string, mutate func(idx *ltpCrewIndex)) (*ltpCrewIndex, error) {
	var lastErr error
	for try := 0; try < ltpWriteTries; try++ {
		idx, version := readLtpIndex(ctx, nk, crewID)
		mutate(idx)
		data, err := json.Marshal(idx)
		if err != nil {
			return nil, err
		}
		_, lastErr = nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      LookingToPlayOpenCollection,
			Key:             crewID,
			UserID:          SystemUserID,
			Value:           string(data),
			Version:         version,
			PermissionRead:  0,
			PermissionWrite: 0,
		}})
		if lastErr == nil {
			return idx, nil
		}
	}
	return nil, lastErr
}

// openLtpAsks returns the asks of a crew that are not closed, oldest first,
// and the index of the crew.
func openLtpAsks(ctx context.Context, nk runtime.NakamaModule, crewID string) ([]*ltpAsk, *ltpCrewIndex, error) {
	idx, _ := readLtpIndex(ctx, nk, crewID)
	asks, _, err := readLtpAsks(ctx, nk, idx.Open)
	if err != nil {
		return nil, idx, err
	}
	var open []*ltpAsk
	for _, id := range idx.Open {
		if a := asks[id]; a != nil && a.State != ltpStateClosed {
			open = append(open, a)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return open[i].OpenedAt < open[j].OpenedAt })
	return open, idx, nil
}

func ltpRemoveID(ids []string, id string) []string {
	out := ids[:0:0]
	for _, v := range ids {
		if v != id {
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Members and the notification
// ---------------------------------------------------------------------------

// ltpMemberIDs returns the members of a crew in a stable order.
func ltpMemberIDs(members map[string]bool) []string {
	out := make([]string, 0, len(members))
	for id, ok := range members {
		if ok && id != "" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ltpOthers returns the members that an ask goes to: the picked members, or
// every member but the asker.
func ltpOthers(a *ltpAsk, members map[string]bool) []string {
	if !a.asksEveryone() {
		return a.Asked
	}
	var out []string
	for _, id := range ltpMemberIDs(members) {
		if id != a.AskerID {
			out = append(out, id)
		}
	}
	return out
}

func ltpAskContent(a *ltpAsk) map[string]interface{} {
	content := map[string]interface{}{}
	data, err := json.Marshal(a)
	if err != nil {
		return content
	}
	_ = json.Unmarshal(data, &content)
	return content
}

// notifyLtpAsk sends the full state of an ask to every crew member
// (notification 118). A client holds one chat channel at a time, so this is
// also how a member with another crew open learns of the ask.
func notifyLtpAsk(ctx context.Context, nk runtime.NakamaModule, a *ltpAsk, members map[string]bool) {
	content := ltpAskContent(a)
	for _, uid := range ltpMemberIDs(members) {
		pushNotification(ctx, nk, uid, NotifyLookingToPlay, content)
	}
}

// notifyLtpNewAsk sends notification 118 of a new ask. Each member but the
// asker gets "notify": true when the member is in recipients (the result of
// ltpAskRecipients), else false. The desktop shows the toast and plays the
// sound only on true, so they reach the same members as the phone push.
func notifyLtpNewAsk(ctx context.Context, nk runtime.NakamaModule, a *ltpAsk, members map[string]bool, recipients []string) {
	notify := make(map[string]bool, len(recipients))
	for _, uid := range recipients {
		notify[uid] = true
	}
	base := ltpAskContent(a)
	for _, uid := range ltpMemberIDs(members) {
		if uid == a.AskerID {
			pushNotification(ctx, nk, uid, NotifyLookingToPlay, base)
			continue
		}
		content := make(map[string]interface{}, len(base)+1)
		for k, v := range base {
			content[k] = v
		}
		content["notify"] = notify[uid]
		pushNotification(ctx, nk, uid, NotifyLookingToPlay, content)
	}
}

// ---------------------------------------------------------------------------
// Closing an ask
// ---------------------------------------------------------------------------

// finishLtpAsk does what follows the close of an ask: the index of the crew,
// the ledger, the notification and the push of the close. The ask is stored
// as closed already.
func finishLtpAsk(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, a *ltpAsk, members map[string]bool) {
	idx, err := changeLtpIndex(ctx, nk, a.CrewID, func(idx *ltpCrewIndex) {
		idx.Open = ltpRemoveID(idx.Open, a.AskID)
		if a.ClosedReason == ltpClosedNobody || a.ClosedReason == ltpClosedAllNo {
			last := idx.Last[a.AskerID]
			last.NobodyAt = a.ClosedAt
			idx.Last[a.AskerID] = last
		}
		cutoff := a.ClosedAt - ltpLastKeep.Milliseconds()
		for uid, last := range idx.Last {
			if last.OpenedAt < cutoff && last.NobodyAt < cutoff {
				delete(idx.Last, uid)
			}
		}
	})
	if err != nil {
		logger.Warn("looking_to_play: index of crew %s after close of %s: %v", a.CrewID, a.AskID, err)
	} else if len(idx.Open) == 0 {
		ltpForgetCrew(a.CrewID)
	}
	logger.Info("looking_to_play: ask=%s crew=%s closed reason=%s in=%d", a.AskID, a.CrewID, a.ClosedReason, len(a.in()))

	data := map[string]interface{}{
		"ask_id": a.AskID,
		"reason": a.ClosedReason,
		"in":     a.in(),
	}
	if a.Game != nil {
		data["game_id"] = a.Game.GameID
		data["game_name"] = a.Game.Name
	}
	if err := AppendCrewEvent(ctx, nk, a.CrewID, CrewEvent{
		ID:        generateEventID(),
		CrewID:    a.CrewID,
		Type:      "looking_to_play",
		ActorID:   a.AskerID,
		Timestamp: a.ClosedAt,
		Data:      data,
	}); err != nil {
		logger.Warn("looking_to_play: ledger event for ask %s: %v", a.AskID, err)
	}

	notifyLtpAsk(ctx, nk, a, members)
	ltpPushClosed(ctx, logger, nk, a)
}

// ---------------------------------------------------------------------------
// The loop that closes asks on time
// ---------------------------------------------------------------------------

// ltpTick closes every ask whose time ended.
func ltpTick(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) {
	for _, crewID := range ltpOpenCrewIDs() {
		open, idx, err := openLtpAsks(ctx, nk, crewID)
		if err != nil {
			logger.Warn("looking_to_play: open asks of crew %s: %v", crewID, err)
			continue
		}
		if len(open) == 0 {
			// The index can name an ask that is closed: tidy it.
			if len(idx.Open) > 0 {
				_, _ = changeLtpIndex(ctx, nk, crewID, func(idx *ltpCrewIndex) { idx.Open = nil })
			}
			ltpForgetCrew(crewID)
			continue
		}
		var members map[string]bool
		for _, a := range open {
			closed := false
			now := ltpNowMs()
			got, err := changeLtpAsk(ctx, nk, a.AskID, func(a *ltpAsk) (bool, error) {
				closed = ltpExpire(a, now)
				return closed, nil
			})
			if err != nil {
				logger.Warn("looking_to_play: expire ask %s: %v", a.AskID, err)
				continue
			}
			if closed {
				if members == nil {
					members = crewMemberSet(ctx, nk, crewID)
				}
				finishLtpAsk(ctx, logger, nk, got, members)
			}
		}
	}
}

// loadLtpOpenCrews fills the set of crews with an open ask from storage.
func loadLtpOpenCrews(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) {
	cursor := ""
	for {
		objects, next, err := nk.StorageList(ctx, "", SystemUserID, LookingToPlayOpenCollection, 100, cursor)
		if err != nil {
			logger.Warn("looking_to_play: list the open asks at start: %v", err)
			return
		}
		for _, o := range objects {
			var idx ltpCrewIndex
			if json.Unmarshal([]byte(o.GetValue()), &idx) == nil && len(idx.Open) > 0 {
				ltpMarkCrewOpen(o.GetKey())
			}
		}
		if next == "" {
			return
		}
		cursor = next
	}
}

// StartLookingToPlayLoop loads the crews with an open ask and starts the loop
// that closes asks on time.
func StartLookingToPlayLoop(nk runtime.NakamaModule, logger runtime.Logger, interval time.Duration) {
	go func() {
		ctx := context.Background()
		loadLtpOpenCrews(ctx, logger, nk)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("looking_to_play: loop panic: %v", r)
					}
				}()
				ltpTick(ctx, logger, nk)
			}()
		}
	}()
}
