# MELLO Crew Event Ledger Specification

> **Component:** Crew Event Ledger  
> **Version:** 0.3  
> **Status:** Implemented  
> **Parent:** [00-ARCHITECTURE.md](./00-ARCHITECTURE.md)  
> **Related:** [04-BACKEND.md](./04-BACKEND.md), [11-PRESENCE-CREW-STATE.md](./11-PRESENCE-CREW-STATE.md), [13-VOICE-CHANNELS.md](./13-VOICE-CHANNELS.md)

---

## 1. Overview

The crew event ledger records meaningful activity within a crew. It is the single data source that powers the catch-up card, post-game moments, and future features (activity feeds, weekly digests, year-in-review).

Events are **not** chat messages. They are structured signals derived from crew activity.

- The server keeps every event in a durable store: tables in the Nakama database (section 3.7). No event is deleted by age.
- The readers of the ledger still get a 7-day view: the events of the 7 days before the last append of the crew.
- Clips, weekly recaps and stream sessions are rows of the same store.
- During the migration the server also writes the legacy per-crew documents (sections 3.1 and 3.5). Section 3.8 has the dual write and the two cutovers.
- Section 10 has the deletion rules.

### Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Storage model | Rows in tables of the Nakama database, one row for each event | Range queries by time and type. No size limit for a crew |
| Retention | Every event is kept | A plan never causes a delete. A gate, if any, is on read |
| 7-day view | The events of the 7 days before the last append of the crew | Sufficient for catch-up. The readers get what the legacy document gave them |
| Legacy document | One Nakama Storage document per crew, trimmed to 7 days, written next to the store during the migration | Rollback, and instances of an older release |
| Write pattern | One transaction for each append. No trim in the store | No background compaction jobs needed |
| Event ownership | System-owned (UserID = "") | Events belong to the crew, not any individual |
| Catch-up trigger | Client requests when `last_seen` for crew > threshold | No server-side scheduling; on-demand only |
| Catch-up threshold | 4 hours | Shorter than this, catch-up card not shown |
| AI summarization | v2 (premium, deferred) | v1 uses structured template composition |

### What This Spec Covers

- Event types and schemas
- Storage model and write path
- The durable store, the migration from the legacy documents, the reconcile
- What a self-hoster sees and must do
- Deletion rules
- Catch-up generation (template-based, v1)
- RPCs and hooks
- Client integration

### What This Spec Does Not Cover

- AI-powered summarization (v2, separate spec)
- Push notifications for individual events (handled by spec 11)
- Game detection / IGDB integration (separate spec)

---

## 2. Event Types

Every event shares a common envelope:

```go
type CrewEvent struct {
    ID        string `json:"id"`         // ULID (sortable, unique)
    CrewID    string `json:"crew_id"`
    Type      string `json:"type"`       // Event type key
    ActorID   string `json:"actor_id"`   // User who triggered it (empty for system events)
    Timestamp int64  `json:"ts"`         // Unix ms
    Data      any    `json:"data"`       // Type-specific payload
    Score     int    `json:"score"`      // Priority score for catch-up ranking
}
```

### 2.1 Auto-Detected Events

These are written by server hooks with no user action required.

#### `voice_session`

Written when the last member leaves a voice channel that had 2+ participants.

```json
{
    "type": "voice_session",
    "score": 20,
    "data": {
        "channel_id": "ch_general",
        "channel_name": "General",
        "participant_ids": ["user_a", "user_b", "user_c"],
        "participant_names": ["ash", "koji", "nav"],
        "duration_min": 47,
        "peak_count": 3
    }
}
```

#### `stream_session`

Written when a stream ends.

```json
{
    "type": "stream_session",
    "score": 30,
    "data": {
        "streamer_id": "user_c",
        "streamer_name": "koji",
        "title": "PROJECT AVALON",
        "game": "Counter-Strike 2",
        "duration_min": 120,
        "peak_viewers": 4,
        "viewer_ids": ["user_a", "user_b", "user_d"]
    }
}
```

#### `game_session`

Written when a detected game session ends for a user in this crew. Requires game detection on client (spec TBD) to push session data to the server.

```json
{
    "type": "game_session",
    "score": 10,
    "data": {
        "game_name": "Counter-Strike 2",
        "game_id": "counter-strike-2",
        "game_igdb_id": 242408,
        "player_ids": ["user_a", "user_b"],
        "player_names": ["ash", "koji"],
        "player_overlap_min": [45, 22],
        "duration_min": 45,
        "active_min": 41
    }
}
```

**Field notes.**

- `game_id` is the stable key for stats and telemetry adapters. It is an IGDB slug for catalogued games, `steam-<appid>` / `epic-<id>` / `gog-<id>` for a game discovered in an installed library, and `local-<exe-slug>` for one nothing could name. It never changes for a given game — stored `user_game_stats` are keyed on it.
- `game_igdb_id` carries the catalogue's id when the game resolved to one, and `0` otherwise. Zero is not an error. A game found by the library scan, or tracked as provisional, is a real session with no IGDB id.
- `duration_min` is wall time. It starts at the **process creation time**, not at the time Mello detected the game. A game that runs before the client starts reports the full time it ran.
- `active_min` is the time the game held the foreground. A game left open overnight has correct wall time and low active time. The ledger records both values. Each surface selects which value to show.
- `player_ids` lists everyone in the crew who was in that game at the same time, unioned from overlapping ledger sessions and from live presence. It is not just the actor.
- `player_overlap_min` aligns by index with `player_ids`. Each value is the number of whole minutes the actor and that member played at the same time. Index 0 holds the actor's own session duration. **Co-play copy, for example "you and kim played 2h of CS2", must use these values. It must not use `duration_min`**, which describes the actor only. `collectCoPlayers` and `overlapMinutes` in [coplay.go](../backend/nakama/data/modules/coplay.go) compute the values. Two sessions that touch at one endpoint give 0. An overlap below one minute also gives 0.

**Amendment (spec 18 — Game Telemetry):** when the client has a telemetry adapter for the game (e.g. CS2 GSI), this event's `data` is enriched with additive fields `wins`, `losses`, `result` (`"win"`/`"loss"`/`"even"`), and `streak_after` (signed: +N win streak, −N loss streak). These are backward compatible — older clients omit them. The `streak_after` value is derived server-side from the actor's private `user_game_stats` store and copied into this public event so crew surfaces can show streaks without exposing raw per-user history. See [18-GAME-TELEMETRY.md](./18-GAME-TELEMETRY.md) §5.

#### `member_joined`

Written by the `AfterJoinGroup` hook, and by the `join_by_invite_code` RPC, which adds the caller with system authority and so runs no hook. Both call `recordMemberJoined`.

```json
{
    "type": "member_joined",
    "score": 15,
    "data": {
        "username": "newplayer",
        "display_name": "NewPlayer",
        "invited_by": "<user id of the member whose invite code was used>",
        "invited_by_name": "b0bben"
    }
}
```

`invited_by` and `invited_by_name` are absent when the joiner used no invite code. They feed the joined cards of the crew feed (CLIPS 7).

#### `member_left`

Written by `recordMemberLeft`. A leave and a kick both call it: the `AfterLeaveGroup` hook, the `kick_crew_member` RPC, and the `AfterKickGroupUsers` hook (section 10.1).

```json
{
    "type": "member_left",
    "score": 5,
    "data": {
        "username": "oldplayer",
        "display_name": "OldPlayer"
    }
}
```

#### `chat_activity`

Written by a periodic aggregator (every 30 min) when chat volume exceeds a threshold. Not per-message. No message content is stored.

```json
{
    "type": "chat_activity",
    "score": 5,
    "data": {
        "message_count": 47,
        "window_start": 1711400000000,
        "window_end": 1711401800000,
        "active_user_count": 4
    }
}
```

### 2.2 User-Contributed Events

#### `moment`

Written when a user shares a moment via the post-game flow or a future "share moment" action.

```json
{
    "type": "moment",
    "score": 40,
    "data": {
        "text": "40-bomb on Dust2",
        "sentiment": "highlight",
        "game_name": "Counter-Strike 2"
    }
}
```

**Sentiment values:** `win`, `loss`, `highlight`

A moment with no text (user only tapped a sentiment button) is still valid:

```json
{
    "type": "moment",
    "score": 25,
    "data": {
        "text": "",
        "sentiment": "win",
        "game_name": "Counter-Strike 2"
    }
}
```

Moments with text score higher than moments without text because they produce better catch-up summaries.

---

## 3. Storage Model

The ledger has two stores during the migration: the legacy document (sections 3.1 to 3.5) and the durable store (section 3.7). One of them is the truth: the one the readers use (section 3.8).

### 3.1 Legacy Document (Nakama Storage)

| Field | Value |
|-------|-------|
| Collection | `crew_events` |
| Key | `{crew_id}` |
| UserID | `""` (system-owned) |
| PermissionRead | `2`. A client cannot read or list the document: the storage hooks refuse it (spec 04 section 6) |
| PermissionWrite | `0` (server-only write) |

```go
type CrewEventLedger struct {
    CrewID    string      `json:"crew_id"`
    Events    []CrewEvent `json:"events"`
    UpdatedAt int64       `json:"updated_at"` // Unix ms
}
```

### 3.2 Write Path

`AppendCrewEvent` writes the event to both stores, in the order of section 3.8 (`ledgerWriteBoth`). The store write is `ledgerStore.Append`: one transaction, no trim. The document write is `appendLedgerDoc`, which follows this sequence:

```go
func appendLedgerDoc(ctx context.Context, nk runtime.NakamaModule, crewID string, event CrewEvent) error {
    // 1. Read current document
    ledger := readLedgerDoc(ctx, nk, crewID)

    // 2. Append new event
    ledger.Events = append(ledger.Events, event)

    // 3. Trim the document: remove events older than 7 days
    cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
    trimmed := make([]CrewEvent, 0, len(ledger.Events))
    for _, e := range ledger.Events {
        if e.Timestamp >= cutoff {
            trimmed = append(trimmed, e)
        }
    }
    ledger.Events = trimmed
    ledger.UpdatedAt = time.Now().UnixMilli()

    // 4. Write back
    return writeLedger(ctx, nk, crewID, ledger)
}
```

### 3.3 Concurrency (legacy document)

Nakama Storage uses optimistic concurrency via version tokens. If two writes race, one fails and retries. At expected event volumes (tens of events per day per crew), collisions are rare. Retry up to 3 times with jitter.

```go
func writeLedgerWithRetry(ctx context.Context, nk runtime.NakamaModule, crewID string, ledger CrewEventLedger, version string) error {
    for attempt := 0; attempt < 3; attempt++ {
        err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
            Collection:      "crew_events",
            Key:             crewID,
            UserID:          "",
            Value:           marshal(ledger),
            Version:         version, // Empty string = unconditional on first write
            PermissionRead:  2,
            PermissionWrite: 0,
        }})
        if err == nil {
            return nil
        }
        // Version conflict: re-read, re-append, retry
        time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
        freshLedger := readLedgerDoc(ctx, nk, crewID)
        freshLedger.Events = append(freshLedger.Events, ledger.Events[len(ledger.Events)-1])
        ledger = freshLedger
    }
    return fmt.Errorf("crew_events write failed after 3 retries for crew %s", crewID)
}
```

### 3.4 Document Size Budget

This budget is for the legacy document only. The durable store has no size limit for a crew.

Worst case estimate for a very active crew over 7 days (pulse events only; clips and recaps are not in the ledger document):
- 50 voice sessions * ~200 bytes = 10 KB
- 20 stream sessions * ~250 bytes = 5 KB
- 100 game sessions * ~200 bytes = 20 KB
- 50 moments * ~150 bytes = 7.5 KB
- 30 chat activity windows * ~100 bytes = 3 KB
- 10 member changes * ~100 bytes = 1 KB

**Total: ~46.5 KB** -- well within Nakama's 256 KB storage object limit.

### 3.5 Legacy documents of clips, recaps and stream replays

Clips, weekly recaps and stream replays are durable crew memory. Each is a row of the durable store (section 3.7). Each also has a legacy system-owned, per-crew document. The server writes the documents during the migration (section 3.8). They are the truth until the second cutover. After it they are copies for a rollback.

- **`crew_clips/{crew_id}`** -- array of `StoredClip`, newest appended last, capped at the most-recent 250 entries to stay under Nakama's 256 KB object limit. Written by `post_clip` (via `AppendClip`) and updated by `clip_upload_complete`. The store has no cap.
- **`crew_recaps/{crew_id}`** -- array of `WeeklyRecapData`, newest appended last, no cap (recaps are tiny, one per crew per week). Written by the weekly recap job (via `AppendRecap`).
- **`crew_stream_sessions/{crew_id}`** -- array of `StoredStreamSession` (a lean, display-only projection: streamer, title, duration, peak viewers, snapshot URLs; viewer IDs dropped), capped at the most-recent 150 entries. Upserted by `UpsertStreamSession` when a stream ends (`stop_stream`, host cleanup) and again from the snapshot backfill job, so the copy tracks the ledger's snapshot lifecycle. The backfill job also recreates a missing copy when ledger snapshots are already current. Snapshot URLs only ever grow on upsert. The store has no cap.

All three mirror the storage pattern of the ledger document (system-owned, read permission 2 with no client read (spec 04 section 6), server-only write, optimistic-concurrency retry). The first write of a clips document is create-only (version `*`), so two uploads at once to a new crew both stay.

Crew quests (features/CREW-QUESTS.md) add these durable stores. All are system-owned and server-written, with the same retry:

- **`crew_quests/{crew_id}`** -- the quest record of a crew made after quests shipped: the owner, the 14-day window, the four steps after crew made, `plus_granted`, `invite_copied_ms`. A step is written once. A crew made before has none.
- **`crew_profile/{crew_id}`** -- the banner and the custom invite name.
- **`crew_emoji/{crew_id}`** -- up to five crew emoji.
- **`user_plus/{user_id}`** -- the m3llo+ entitlement of a user (`until_ms`, `source`, the one quest grant). `IsUserPremium` reads only it.
- **`media_reports/{id}`** -- a report of an uploaded banner or a crew emoji, and its takedown or dismiss.

Read paths:
- `crew_feed` is the one feed endpoint. It merges the 7-day view of the ledger with the clips, recaps and stream replays, and returns the zones (features/CREW-FEED-ZONES.md): the Stage, the Board, the clips and sessions of the free window, People, and one page of Crew history by week (`history_before`). No layout, styling or copy crosses the boundary.
- `crew_catchup` merges recent clips (newer than `last_seen`) into the candidate set.

### 3.6 Library report (not a ledger event)

The `library_report` RPC stores the games installed on a member's device. It is not a ledger event. Nothing is appended to the ledger and no card type exists for it.

Reason: the legacy ledger document keeps 7 days of history inside a 256 KB object (section 3.4). A library of 500 games is about 60 KB. Each member would add that to the shared document. The library is state, not history, so a replace is the right model.

| Item | Value |
|---|---|
| Collection / key | `user_library` / `snapshot`, one object per user |
| Permissions | Read: owner only. Write: server only |
| Write | A replace. The last report wins. No version check |
| Reader | `crew_feed` reads the documents of the crew members as the system |
| Gate | The client sends only when `share_game_activity` is on. An empty report clears the document |

Request:

```json
{ "games": [ { "game_id": "steam-730", "name": "Counter-Strike 2", "launcher": "steam",
               "playtime_min": 249008, "last_played_unix": 1760000000 } ] }
```

| Field | Rule |
|---|---|
| `game_id` | Required. `<prefix>-<external_id>`, at most 128 bytes, no whitespace |
| `name` | Required. At most 128 characters |
| `launcher` | Required. Lower case letters, digits and `_`, at most 32 bytes |
| `playtime_min` | Optional. 0 to 26,280,000 |
| `last_played_unix` | Optional. Unix seconds, not negative |
| `hidden` | Not part of the contract. An entry with `hidden: true` is dropped |

- More than 500 games, or a payload over 256 KB, gives error code 3 (invalid argument). A repeated `game_id` keeps its first entry.
- The stored document is `{ "games": [...], "updated_at": <unix ms> }`.
- The client sends after the first scan following login, then only when the set or the playtime changed. Spec [17](./17-GAME-SENSING.md) section 3.3 has the scan.
- Module: `backend/nakama/data/modules/library_report.go`.

### 3.7 Durable store

The durable store is five tables in the Nakama PostgreSQL database. `ensureLedgerSchema` creates them at start. Every statement is `IF NOT EXISTS`. The code uses only `database/sql`.

| Table | Holds | Key |
|---|---|---|
| `mello_crew_event` | One row for each event, clip, weekly recap and stream session | `(crew_id, ts, id)`, and unique `(crew_id, id)` |
| `mello_crew_event_user` | One row for each user an event names (the user index) | `(user_id, crew_id, event_id)` |
| `mello_crew_event_week` | The number of rows of a type in a UTC week (the week rollup) | `(crew_id, week_start, type)` |
| `mello_crew_event_anchor` | The time of the last append of a crew, and the last position given out | `crew_id` |
| `mello_crew_event_deleted` | The ids that `Delete` removed | `(crew_id, event_id)` |

A row of `mello_crew_event`:

| Column | Content |
|---|---|
| `crew_id`, `id`, `type`, `actor_id`, `ts`, `score` | The envelope of section 2 |
| `data` | The payload, JSONB |
| `ref` | A lookup key next to the id: the `session_id` of a stream session, the `clip_id` of a clip, the week of a recap. Empty for the other types |
| `created_ms` | The time the row was written |
| `pos` | The append order of the crew. `0` for a row that did not come through the ledger path |

- One append is one transaction: the anchor row, the event row, one row for each user the event names, one rollup upsert. The unique `(crew_id, id)` makes a repeated append a no-op.
- Every write that gives out a position updates the anchor row first. So the writes of one crew run one after the other, and no append is lost.
- `eventUserIDs` lists the users an event names: the actor, `participant_ids`, `player_ids`, `viewer_ids`, `streamer_id`, `invited_by`, clip `participants`, and the members a weekly recap lists.
- `week_start` is the UTC Monday of the week (`weekStartOf`).
- A `Delete` writes the id to `mello_crew_event_deleted`. An import does not store that id again. A live `Append` of the id clears it.

**Type sets** (`ledger_store.go`):

| Set | Types | Use |
|---|---|---|
| Pulse (`ledgerPulseTypes`) | `voice_session`, `stream_session`, `game_session`, `member_joined`, `member_left`, `chat_activity`, `moment` | The types of the ledger path. The 7-day view returns them |
| Counted (`ledgerCountedTypes`) | `clip`, `stream_session`, `game_session`, `weekly_recap` | The types that will count for a free window by events. No shipped rule reads the set |
| Legacy (`ledger_` prefix) | `ledger_clip`, `ledger_weekly_recap` | A `clip` or `weekly_recap` event that a ledger document held (old crews, the dev seed). The 7-day view returns it under its document type. No counted query sees it |

**Rows of clips, recaps and stream sessions:**

| Thing | Type | Row id | Ref | Time |
|---|---|---|---|---|
| Clip | `clip` | `clip_{event_id}` | `clip_id` | `ts` of the clip |
| Stream session | `stream_session` | The id of the ledger event | `session_id` | `ts` of the event |
| Weekly recap | `weekly_recap` | `recap_{week_start}` | The week start, 19 digits | `generated_at`, or the end of the week when it is absent |

- An entry of a legacy document with no event id gets a row id from its other id: `clipref_{clip_id}`, `session_{session_id}`.
- The store holds one row for a `clip_id` (`PutUnique`), and one recap for a week start. The first one wins.
- A stream that ends writes one ledger event. That row is the stream session. A session that only the legacy copy holds is stored under the id of the event with `pos = 0`. When a ledger document later holds the event, the row gets its position. No second row is made.
- A stop and a socket drop can give two events for one session id. Both are rows. The feed lists the newest one.

**The 7-day view** (`RecentView`) is what the legacy document held:

| Rule | Value |
|---|---|
| Types | The pulse types and the `ledger_` types |
| Rows | `pos > 0`. A clip row, a recap row and a stream session with `pos = 0` are not in the view |
| Window | `ts >= last_append_ms - 7 days`. The window follows the time of the last append, not the present time. A game session from the pending queue can arrive with an old timestamp |
| Order | `pos`, the append order |

`readLedger` returns this view after the first cutover. The readers did not change: `crew_feed` (the merged timeline), `crew_catchup`, the guest feed, the invite preview, the rate limit of `post_moment`, and the co-players of `game_session_end`. The weekly recap job reads its whole week from the store (`readLedgerWindow`), so a late run covers the full week.

### 3.8 Migration: dual write and two cutovers

The migration has two phases. Each phase has its own state document, marker and kill switch.

| | Phase 1a | Phase 1b |
|---|---|---|
| Data | The ledger events (`crew_events`) | Clips, stream sessions, weekly recaps (`crew_clips`, `crew_stream_sessions`, `crew_recaps`) |
| State document | `ledger_migration/state`: `dual_write_started_ms`, `events_done_ms` | `ledger_migration/history`: `started_ms`, `done_ms` |
| Marker | `events_done_ms` | `done_ms` |
| Kill switch | `MELLO_LEDGER_READ=doc` (both phases) | `MELLO_LEDGER_HISTORY_READ=doc` (phase 1b only) |

**Dual write.** Every write goes to both stores. The truth is written first.

| | Truth | Write order | Failure of the first write | Failure of the second write |
|---|---|---|---|---|
| Before the cutover | Legacy document | Document, then store | Error to the caller. The store is not written | Logged. The reconcile repairs it |
| After the cutover | Durable store | Store, then document | Error to the caller. The document is not written | Logged only |

- Phase 1a: `AppendCrewEvent`, `UpdateLedgerEventSnapshotURLs`, `removeCrewEvent` (`ledgerWriteBoth`).
- Phase 1b: `AppendClip`, the media and poster URL of a clip (`updateStoredClip`), `AppendRecap` (`historyWriteBoth`). The truth decides if a `clip_id` is free.
- A stream session is written by `AppendCrewEvent`, then to the legacy copy (`UpsertStreamSession`).
- With no store loaded, only the documents are written.

**Cutover conditions.** The reconcile (section 3.9) sets a marker at the end of a pass.

| Marker | Conditions |
|---|---|
| Phase 1a | The pass is clean. Dual write has run for the soak time since `dual_write_started_ms` |
| Phase 1b | The pass is clean for both phases. No entry is without an id. The marker of phase 1a is set. The soak time has passed since `started_ms` |

- The soak time is 24 hours. `MELLO_LEDGER_SOAK_SECONDS` overrides it. The e2e stack sets 0.
- The soak covers a deploy overlap: an instance of the previous release writes only the documents.
- An install that starts on this release starts both soaks on the first reconcile tick. Both markers can be set in the same pass.
- Readers and the write order follow the marker. A node reads the state document again every 30 seconds until it sees the marker. Then it does not read it again.
- No operator action sets a marker.

**After the cutover of phase 1a:** `readLedger` returns the 7-day view of the store. The snapshot backfill lists the stream sessions of every crew in one read (`ByType`). A store read that fails falls back to the document.

**After the cutover of phase 1b:**

| Reader | Read |
|---|---|
| `crew_feed` | The rows of the free window, the recap for the Board, and one page of Crew history by week: the clips, the stream sessions and the game sessions of its weeks. No whole-crew read |
| Merged timeline | The clips of 7 days, the recap of the newest week |
| `crew_catchup` | The newest 250 clips, then the ones after `last_seen` |
| `post_clip`, `clip_upload_url`, `clip_upload_complete` | The clip by `clip_id`. The daily limit counts the clips of the day |
| Weekly recap job | The clips of the week |
| Guest feed | The newest 250 clips, and the count of the older ones |
| Invite preview | The 7-day view only, as before |

- The store has no cap. A crew with more than 250 clips or 150 stream sessions sees all of them in Crew history.
- A game session older than the 7-day view is in its week of Crew history (features/CREW-FEED-ZONES.md section 7). Before the cutover no reader shows it.
- A store read that fails falls back to the documents.

**Kill switches.** Both are env variables of the Nakama process. A change needs a restart.

| Variable | Effect |
|---|---|
| `MELLO_LEDGER_READ=doc` | Both phases read the documents, and the documents are written first |
| `MELLO_LEDGER_HISTORY_READ=doc` | Only phase 1b reads its documents, and they are written first |

- The documents stay current under dual write, so a kill switch loses no event.
- Under a kill switch a crew over an old cap sees its capped document again.
- A rollback to the previous release also works. That release reads and writes only the documents. The tables stay. After the roll forward the reconcile inserts the events of that period.

Known limits:

- After a cutover, a document write that fails is only logged. The document then lacks that entry, and a kill switch does not show it.
- An event that `removeCrewEvent` removed while the previous release ran stays in the store. This affects only a stream that was resumed in that period.
- `MELLO_LEDGER_READ=doc` does not make the reconcile of phase 1a take the content of the ledger document again. Phase 1b follows the truth (`historyDocumentIsTruth`).
- A clip whose `clip_id` the store holds under another row stays out of the store. The log line is `ledger_clip_ref_held`.

### 3.9 Reconcile

`StartLedgerReconcile` runs at start and every 5 minutes, on every node. One tick handles up to 1000 documents. A pass over more documents continues on the next tick. A pass visits `crew_events`, then `crew_stream_sessions`, `crew_clips`, `crew_recaps`.

The ledger document of one crew (`reconcileLedgerCrew`):

| | Before the cutover | After the cutover |
|---|---|---|
| Document event the store lacks | Insert at its document position | Insert at its document position |
| Store event of the 7-day view that the document lacks | Prune | Keep |
| Data differs | Take the data of the document | Keep the data of the store |
| Order differs | Take the order of the document | Keep the order of the store |
| Event the store removed (`Delete`) | Not inserted again | Not inserted again |

A document of phase 1b (`reconcileHistoryCrew`):

| | The document is the truth | The store is the truth |
|---|---|---|
| Entry the store lacks | Store it | Store it |
| Row the document lacks | Keep | Keep |
| Content differs | Take the document. A stream row keeps its viewers | Keep the store |
| Id that `Delete` removed | Not stored again | Not stored again |
| Entry with neither an event id nor a `clip_id` or `session_id` | Counted (`no_id`), not stored | The same |
| Clip whose `clip_id` another row holds | Counted (`ref_held`), not stored | The same |

- The reconcile of phase 1a prunes only inside the 7-day window of the document. Older rows are history that the document trimmed. They stay.
- The reconcile of phase 1b never deletes a row. A row that the document lacks is history that the cap removed from the document.
- Each crew gets up to 3 repair rounds in a pass. Each round reads the document again.
- A pass is clean when every document ends equal to the store, and no read or write failed. A document that cannot be read or decoded makes the pass not clean. It is not taken for an empty document.
- A ledger event without an id cannot go into the store. It is counted (`no_id`) and does not block the marker of phase 1a.
- `no_id` in phase 1b blocks its marker (`not_set_entries_without_id`). `ref_held` does not.
- The reconcile does not import a document of a deleted crew (section 10.4).

### 3.10 Self-hosting

A self-hoster does nothing in the normal case. The tables are created at start, the reconcile copies the documents, and the markers are set after the soak time.

**Database right.** The database user of Nakama must have the right to create tables and indexes in its schema (`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`). The schema setup also takes a session advisory lock (`pg_try_advisory_lock`), which needs no extra right. No extension and no new dependency is needed.

**When the schema setup fails** the module still starts. It runs on the legacy documents alone. The setup is tried again in the background: after 5 seconds, then doubling up to 5 minutes. When the tables exist, the store is set and the reconcile copies what the store missed.

**Log lines:**

| Line | Level | Meaning | Action |
|---|---|---|---|
| `Crew ledger tables ready` | Info | The store is loaded | None |
| `ledger_schema_failed: ... Retry in ...` | Error | The tables were not created | Give the database user the right above. No restart is needed |
| `ledger_reconcile_pass crews= inserted= pruned= updated= no_id= unclean= failed= marker=` | Info, or Error when not clean | One pass of phase 1a ended | None when `unclean=0 failed=0` |
| `ledger_history_pass docs= inserted= updated= no_id= ref_held= unclean= failed= marker=` | Info, or Error when not clean or `no_id` > 0 | One pass of phase 1b ended | None when `unclean=0 failed=0 no_id=0` |
| `ledger_parity_mismatch crew=`, `ledger_history_mismatch kind= crew=` | Error | A document did not converge in 3 rounds | None when the next pass is clean |
| `ledger_reconcile_crew_failed`, `ledger_history_crew_failed` | Error | A read or a write of one crew failed | Check the database |
| `ledger_store_write_failed`, `ledger_doc_write_failed`, `ledger_store_read_failed` | Error | One write or read of one store failed. The other store served it | Check the database when it repeats |
| `user_scrub ...`, `crew_purge ...` | Info | A deletion of section 10 ended | None |
| `user_scrub_failed`, `crew_purge_failed` | Error | A deletion did not end. The deletion job runs it again every 5 minutes | Section 10.5 |
| `user_scrub_pending`, `crew_purge_pending` | Warn | A deletion waits for the store | Fix the schema setup |

Values of `marker=`:

| Value | Meaning |
|---|---|
| `set` | This pass set the marker. The cutover is done |
| `set_before` | The marker was set on an earlier pass |
| `not_set_soak_Nmin_left` | The pass was clean. The soak time is not over |
| `not_set_pass_not_clean` | A document did not converge, or a read or write failed |
| `not_set_events_marker_not_set` | Phase 1b waits for the marker of phase 1a |
| `not_set_entries_without_id` | A document of phase 1b holds an entry that no row can hold |
| `not_set_state_unreadable`, `not_set_write_failed` | The state document was not read or written. The next pass tries again |

**Env variables.** All are optional. `backend/docker-compose.yml` passes them through.

| Variable | Default | Use |
|---|---|---|
| `MELLO_LEDGER_SOAK_SECONDS` | 86400 | The soak time before each cutover |
| `MELLO_LEDGER_READ` | Not set | `doc`: the kill switch of both phases |
| `MELLO_LEDGER_HISTORY_READ` | Not set | `doc`: the kill switch of phase 1b |
| `MELLO_MEDIA_ORPHAN_DELETE` | Not set | `1`: lets `admin_media_orphans` delete (spec 04 section 4.5) |

---

## 4. Catch-Up Generation (v1, Templates)

### 4.1 Catch-Up RPC

```go
initializer.RegisterRpc("crew_catchup", CrewCatchupRPC)
```

Only a member of the crew. Another caller gets PERMISSION_DENIED (7) with the
token `NOT_A_MEMBER`, also for a crew id that does not exist.

**Request:**

```json
{
    "crew_id": "crew_xyz",
    "last_seen": 1711300000000
}
```

**Response:**

```json
{
    "crew_id": "crew_xyz",
    "catchup_text": "ash hit Immortal in Valorant, new scrim times posted, and koji streamed his first ace yesterday.",
    "event_count": 14,
    "top_events": [
        { "type": "moment", "actor_id": "user_a", "actor_name": "ash", "ts": 1711350000000, "data": { "text": "hit Immortal", "sentiment": "highlight", "game_name": "Valorant" } },
        { "type": "stream_session", "actor_id": "user_c", "ts": 1711340000000, "data": { "streamer_name": "koji", "title": "first ace", "duration_min": 30 } }
    ],
    "has_events": true
}
```

`catchup_text` is English, for old clients. The client makes its own line
from `top_events` in the language of the user (`specs/features/LOCALIZATION.md`,
"Server text with a kind and values"). `actor_name` is on a moment and a clip
only, and is absent when the server does not find the user.

When there are no events or `last_seen` is recent (< 4 hours), return the quiet state:

```json
{
    "crew_id": "crew_xyz",
    "catchup_text": "All quiet, crew's been chilling. Nothing new since you left.",
    "event_count": 0,
    "top_events": [],
    "has_events": false
}
```

### 4.2 Ranking and Selection

The catch-up card shows a summary built from the top 3 events by score, deduplicated by type.

```go
func buildCatchup(events []CrewEvent, lastSeen int64) CatchupResponse {
    // 1. Filter to events after lastSeen
    recent := filterAfter(events, lastSeen)
    if len(recent) == 0 {
        return quietCatchup()
    }

    // 2. Sort by score descending, then timestamp descending
    sort.Slice(recent, func(i, j int) bool {
        if recent[i].Score != recent[j].Score {
            return recent[i].Score > recent[j].Score
        }
        return recent[i].Timestamp > recent[j].Timestamp
    })

    // 3. Pick top 3, preferring type diversity
    selected := selectDiverse(recent, 3)

    // 4. Render text from templates
    text := renderCatchupText(selected)

    return CatchupResponse{
        CatchupText: text,
        EventCount:  len(recent),
        TopEvents:   selected,
        HasEvents:   true,
    }
}
```

The `selectDiverse` function picks the highest-scored event, then skips events of the same type for subsequent picks unless no other types remain.

### 4.3 Template Rendering

Each event type has a template function that produces a natural-language fragment:

```go
var templateFuncs = map[string]func(CrewEvent) string{
    "moment": func(e CrewEvent) string {
        d := e.Data.(MomentData)
        name := lookupUsername(e.ActorID)
        if d.Text != "" {
            return fmt.Sprintf("%s: %s", name, d.Text)
        }
        switch d.Sentiment {
        case "win":
            return fmt.Sprintf("%s had a win in %s", name, d.GameName)
        case "loss":
            return fmt.Sprintf("%s took an L in %s", name, d.GameName)
        case "highlight":
            return fmt.Sprintf("%s had a moment in %s", name, d.GameName)
        }
        return ""
    },
    "voice_session": func(e CrewEvent) string {
        d := e.Data.(VoiceSessionData)
        names := joinNames(d.ParticipantNames, 3)
        return fmt.Sprintf("%s hung out in %s for %dm", names, d.ChannelName, d.DurationMin)
    },
    "stream_session": func(e CrewEvent) string {
        d := e.Data.(StreamSessionData)
        return fmt.Sprintf("%s streamed %s for %dm", d.StreamerName, d.Title, d.DurationMin)
    },
    "game_session": func(e CrewEvent) string {
        d := e.Data.(GameSessionData)
        names := joinNames(d.PlayerNames, 3)
        return fmt.Sprintf("%s played %s", names, d.GameName)
    },
    "member_joined": func(e CrewEvent) string {
        d := e.Data.(MemberJoinedData)
        return fmt.Sprintf("%s joined the crew", d.DisplayName)
    },
    "member_left": func(e CrewEvent) string {
        d := e.Data.(MemberLeftData)
        return fmt.Sprintf("%s left the crew", d.DisplayName)
    },
    "chat_activity": func(e CrewEvent) string {
        d := e.Data.(ChatActivityData)
        return fmt.Sprintf("%d messages from %d people in chat", d.MessageCount, d.ActiveUserCount)
    },
}
```

The final catch-up text joins selected fragments with commas and "and":

```go
func renderCatchupText(events []CrewEvent) string {
    fragments := make([]string, 0, len(events))
    for _, e := range events {
        if fn, ok := templateFuncs[e.Type]; ok {
            if f := fn(e); f != "" {
                fragments = append(fragments, f)
            }
        }
    }
    return joinWithAnd(fragments)
    // "ash: hit Immortal, koji streamed PROJECT AVALON for 120m, and nav joined the crew"
}

func joinWithAnd(parts []string) string {
    switch len(parts) {
    case 0:
        return ""
    case 1:
        return parts[0]
    case 2:
        return parts[0] + " and " + parts[1]
    default:
        return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
    }
}
```

---

## 5. Post-Game Moment RPC

Handles the post-game flow (win/loss/highlight taps from the client bottom bar).

```go
initializer.RegisterRpc("post_moment", PostMomentRPC)
```

**Request:**

```json
{
    "crew_id": "crew_xyz",
    "sentiment": "highlight",
    "text": "40-bomb on Dust2",
    "game_name": "Counter-Strike 2"
}
```

`text` is optional (empty string if user only tapped a sentiment button). `game_name` is populated by the client from the most recent detected game.

**Validation:**
- `sentiment` must be one of: `win`, `loss`, `highlight`
- `text` max length: 140 characters
- `game_name` max length: 100 characters
- User must be a member of the crew
- Rate limit: max 10 moments per user per crew per day

**Response:**

```json
{
    "success": true,
    "event_id": "01J5K3M..."
}
```

---

## 6. Hook Integration

### 6.1 Events Written by Existing Hooks

| Hook / Module | Event Written | Trigger |
|---------------|---------------|---------|
| `voice_state.go` voice_leave | `voice_session` | Last member leaves a channel that had 2+ members (track via in-memory session start time) |
| `streaming.go` stream_stop | `stream_session` | Stream ends |
| `crews.go` AfterJoinGroup, `invite_codes.go` join_by_invite_code | `member_joined` | User joins crew |
| `crews.go` AfterLeaveGroup, `kick_crew_member`, AfterKickGroupUsers | `member_left` | User leaves crew, or is kicked |
| `crew_state.go` OnChatMessage | `chat_activity` | Aggregated, not per-message (see 6.2) |

### 6.2 Chat Activity Aggregator

Chat activity is not logged per-message. Instead, an in-memory counter per crew tracks messages. Every 30 minutes, if the count exceeds a threshold (10 messages), a `chat_activity` event is written and the counter resets.

```go
var chatCounters = struct {
    sync.Mutex
    counts map[string]*chatWindow // crew_id -> window
}{}

type chatWindow struct {
    count      int
    userSet    map[string]bool
    windowStart int64
}

// Called from OnChatMessage hook
func incrementChatCounter(crewID, userID string) {
    chatCounters.Lock()
    defer chatCounters.Unlock()
    w, ok := chatCounters.counts[crewID]
    if !ok {
        w = &chatWindow{
            userSet:     make(map[string]bool),
            windowStart: time.Now().UnixMilli(),
        }
        chatCounters.counts[crewID] = w
    }
    w.count++
    w.userSet[userID] = true
}

// Called by a goroutine ticker every 30 minutes
func flushChatCounters(ctx context.Context, nk runtime.NakamaModule) {
    chatCounters.Lock()
    snapshot := chatCounters.counts
    chatCounters.counts = make(map[string]*chatWindow)
    chatCounters.Unlock()

    for crewID, w := range snapshot {
        if w.count < 10 {
            continue // Below threshold, discard
        }
        event := CrewEvent{
            ID:        generateULID(),
            CrewID:    crewID,
            Type:      "chat_activity",
            ActorID:   "",
            Timestamp: time.Now().UnixMilli(),
            Score:     5,
            Data: ChatActivityData{
                MessageCount:    w.count,
                WindowStart:     w.windowStart,
                WindowEnd:       time.Now().UnixMilli(),
                ActiveUserCount: len(w.userSet),
            },
        }
        AppendCrewEvent(ctx, nk, crewID, event)
    }
}
```

### 6.3 Game Session Events

Game sessions require the client to report session data. The client detects game start/stop via process scanning and pushes a `game_session_end` RPC when a game closes.

```go
initializer.RegisterRpc("game_session_end", GameSessionEndRPC)
```

**Request (from client):**

```json
{
    "crew_id": "crew_xyz",
    "game_name": "Counter-Strike 2",
    "duration_min": 45
}
```

The server enriches with co-players: any other crew members who also had an active game session for the same game overlapping in time (tracked via presence activity or a lightweight in-memory map).

---

## 7. Last-Seen Tracking

The catch-up system needs to know when a user last interacted with a crew. This is tracked per user per crew.

### 7.1 Storage

| Field | Value |
|-------|-------|
| Collection | `crew_last_seen` |
| Key | `{crew_id}` |
| UserID | `{user_id}` (user-owned) |
| PermissionRead | `1` (owner only) |
| PermissionWrite | `1` (owner only) |

```json
{
    "crew_id": "crew_xyz",
    "last_seen": 1711400000000
}
```

### 7.2 Update Triggers

`last_seen` is updated when:

- User sets this crew as active (`set_active_crew` RPC, spec 11)
- User sends a chat message in this crew
- User joins a voice channel in this crew

It is NOT updated for passive sidebar visibility. The point is to capture "the last time the user was actively engaged with this crew."

---

## 8. Backend Module

### 8.1 Files

```
backend/nakama/data/modules/
├── ...existing files...
├── crew_events.go               # Event ledger, catch-up RPC, moment RPC, chat aggregator
├── ledger_store.go              # ledgerStore interface, type sets, the in-memory store
├── ledger_store_sql.go          # Schema, the SQL store, dev_ledger_selftest
├── ledger_store_conformance.go  # The store contract, run on both stores
├── ledger_dual_write.go         # The write order of both stores
├── ledger_migration.go          # Reconcile, soak, markers, cutover
├── ledger_history.go            # Clips, stream sessions and recaps as rows, Crew history pages
├── ledger_scrub.go              # The scrub rules of a deleted account (pure)
├── ledger_user_scrub.go         # Account delete hooks, the scrub, the deletion job
├── ledger_purge.go              # The purge of a deleted crew
└── admin_media_orphans.go       # Scan and delete of media that no row points at
```

### 8.2 Registration

Add to `main.go` `InitModule`:

```go
// RPCs -- crew events
initializer.RegisterRpc("crew_catchup", CrewCatchupRPC)
initializer.RegisterRpc("post_moment", PostMomentRPC)
initializer.RegisterRpc("game_session_end", GameSessionEndRPC)

// Start chat activity flush ticker
go startChatActivityTicker(ctx, nk, 30 * time.Minute)

// Durable store: schema, reconcile, deletion job
startLedgerStore(ctx, logger, ledgerSchemaRetry, setup)
go StartLedgerReconcile(ctx, nk, logger)
go StartLedgerDeletionJob(ctx, nk, logger)
```

Spec 04 lists the hooks and the admin RPCs of the durable store.

### 8.3 ULID Generation

Events use ULIDs for IDs (time-sortable, unique). Use `github.com/oklog/ulid/v2`.

```go
import "github.com/oklog/ulid/v2"

func generateULID() string {
    return ulid.Make().String()
}
```

---

## 9. Client Integration

### 9.1 Catch-Up Flow

```
User opens app
    |
    v
For each crew in sidebar:
    1. Read local last_seen cache
    2. If now - last_seen > 4 hours:
        Call crew_catchup RPC
    3. If response.has_events:
        Show catch-up card on sidebar item
    4. If !response.has_events:
        Show quiet state ("All quiet...")
    |
    v
User taps into crew (set_active_crew):
    1. Catch-up card stays visible until user sends a message or joins voice
    2. Update last_seen (server + local cache)
    3. Dismiss catch-up card
```

### 9.2 Post-Game Flow

```
Client detects game process exit
    |
    v
If user is in a crew (has active/recent crew):
    1. Morph bottom bar to post-game card
    2. Show sentiment buttons (win / loss / highlight)
    3. Wait for tap or 30s timeout
    |
    v
If user tapped a button:
    1. If highlight: show text input, wait for submit or 15s timeout
    2. Call post_moment RPC with sentiment + optional text + game_name
    3. Show confirmation ("Moment shared with crew")
    4. Fade to idle after 2s
    |
    v
If timeout (no tap):
    1. Fade post-game card to idle
    2. Call game_session_end RPC (session still logged, just no moment)
```

### 9.3 Rust Types

In `mello-core/src/crew_events.rs`:

```rust
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CatchupResponse {
    pub crew_id: String,
    pub catchup_text: String,
    pub event_count: u32,
    pub top_events: Vec<CatchupEvent>,
    pub has_events: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CatchupEvent {
    #[serde(rename = "type")]
    pub event_type: String,
    pub actor_id: String,
    /// The display name of the actor of a moment or a clip.
    pub actor_name: Option<String>,
    pub ts: i64,
    pub data: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PostMomentRequest {
    pub crew_id: String,
    pub sentiment: String, // "win" | "loss" | "highlight"
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub game_name: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GameSessionEndRequest {
    pub crew_id: String,
    pub game_name: String,
    pub duration_min: u32,
}
```

---

## 10. Deletion Rules

Events stay for good, so these rules say when data leaves. They hold in both stores (rows and legacy documents) in every phase: before a cutover, after it, and under both kill switches.

| Case | Rule |
|---|---|
| Member leaves | A `member_left` event. Earlier events stay |
| Member is kicked | The same `member_left` event. Earlier events stay |
| Account deleted | The events the user started are deleted, with their media. The user leaves the events of other members, the stored recaps, the head2head holders and the other documents of the crew. A crew whose only owner is the user gets a new owner. A crew whose only member is the user is purged |
| Crew deleted | Everything of the crew is deleted: rows, documents, media, chat |

### 10.1 Leave and kick

- `AfterLeaveCrew`, `kick_crew_member` and the kick hooks call `recordMemberLeft`.
- A kick through the group API of Nakama has two hooks. `BeforeKickCrewMembers` notes which ids of the request are members now. `AfterKickCrewMembers` writes `member_left` and cleans voice only for an id that was noted and is no member now. Nakama does not kick every id of a request.
- The noted ids are in process memory for one minute. They hold for the node that runs both hooks of one request.
- The events, clips and recaps that name the member stay. A leave is not an account delete.

### 10.2 Account deleted

The client calls `DELETE /v2/account` of Nakama. Two hooks run:

| Hook | Does |
|---|---|
| `BeforeDeleteAccount` | Hands over each crew whose only owner is the user (see below). Writes the scrub record: the names and the crews of the user, and the crews the user is the only member of. After the delete none of them can be read. Deletes nothing. Fails the delete only when a crew with other members cannot be handed over |
| `AfterDeleteAccount` | Marks the record and starts the scrub in a goroutine. Never fails |

The scrub record is `ledger_user_scrub/{user_id}`, system-owned, with no client read. Every write of the record carries its storage version. One scrub of a user runs at a time in a process.

**The crew of an only owner.** Nakama 3.21.0 deletes a group when its last superadmin deletes the account, also when the group has other members. No delete-group hook runs. So `BeforeDeleteAccount` acts before Nakama does (`crew_handover.go`):

| The user in the crew | Before the delete | After the delete |
|---|---|---|
| Not a superadmin, or another superadmin exists | Nothing | Nakama removes the user from the group |
| The only superadmin, with other members | One member becomes a superadmin | The crew stays, with the new owner |
| The only member | The crew goes in `sole_crews` of the scrub record | Nakama deletes the group. The scrub purges the crew |

- The new owner is chosen by rank, then by seniority: an admin before a member, and among equals the one who joined first.
- The join time is the position of the member in the group. Nakama sets it at the join, does not change it, and lists the members of one state in its order. The module reads no other join time.
- A join request and a banned user are not members.
- The promote is a call of the Nakama runtime (`GroupUsersPromote`), one call for an admin, two for a member. Nakama writes one message with code 7 and no content to the chat of the crew for each call. The clients do not show it.
- No ledger event is written. No event type is a change of owner.
- No client is told. A client reads the roles with the crew state (`set_active_crew`), so the new owner sees the role at the next read.
- When the account delete fails after a handover, the crew has two owners. This is accepted. Nothing takes the role back.
- A member read that fails never makes a crew a crew of an only member.

A crew with other members must not go with the account of its owner. So the hook refuses the delete when it cannot hand such a crew over:

| Case | Log line `crew_handover_failed` |
|---|---|
| The list of the crews of the user is not read | With the user. Crews of other members can be at stake |
| A member list of an owned crew is not read | With the crew |
| The promote fails, or the member is no superadmin after it | With the crew |
| The 10 seconds of the handovers are over before an owned crew | With the crew |

- The delete answers `UNAVAILABLE` (14, HTTP 503) with a text that is safe to show: "Your account was not deleted. A crew you own could not be handed to another member. Try again in a moment."
- The account, the session and the crew stay as they were. The user tries again. A crew that was handed over before the failure keeps its new owner beside the user.
- No scrub record is written for a refused delete.
- Nothing else in the hook fails the delete: not the account read, not the write of the scrub record, not a crew of an only member.
- The clients delete the account from Settings (spec 01, PROFILE tab). Core keeps the session on a failed delete and sends `AccountDeleteFailed`. Its `message` is this text for code 14, and empty for any other failure. The client shows the text and allows a retry.

**A crew with no owner.** Two owners who delete their accounts at the same time each see the other in the before hook. Nakama counts the membership rows it keeps of the first (see the table below) as a superadmin when the second goes, so the group stays with no owner. `repairOwnerlessCrew` gives a crew with members and no superadmin an owner, by the rule of the handover. The scrub calls it for each crew after it removed the membership rows of the account. The sweep of section 10.4 calls it too. A crew with no member at all is logged (`crew_owner_repair`) and stays.

The scrub purges a crew only when all of these hold: the crew is in `sole_crews` of the last delete attempt, the account is gone, and the group is read and is gone. It writes the purge record and runs the purge of section 10.3. Any other crew with no group is scrubbed and not purged.

What the scrub does to each thing that names the user:

| Thing | Rule |
|---|---|
| Event where the user is the actor or the streamer | Deleted |
| Event of another member | The user leaves `participant_ids`, `player_ids`, `viewer_ids` and clip `participants`, and the entry at the same place in `participant_names`, `player_names`, `player_overlap_min`. `invited_by` and `invited_by_name` are cleared |
| `voice_session` left with fewer than two participants | Deleted |
| Clip of the user | Deleted, with its media |
| Clip of another member that names the user | The user leaves `participants` and `participant_names`. The clip and its media stay |
| Stream session of the user | Deleted, with its snapshots |
| Head2head holders (`crew_head2head`) | A title the user holds is removed. A title taken from the user loses `previous_holder_user_id`. The user leaves `members` and `pairs` |
| Quests record (`crew_quests`) | No step is taken back. `owner_id` becomes the owner the crew has now, or empty. `friend` loses the id and the name of the user, as the friend or as the inviter. The user leaves the lists of `call` and `session`, and `plus_granted`. `clip` loses the clipper |
| Crew emoji (`crew_emoji`) | `added_by` and `added_by_name` are cleared. The emoji stays. Then only the owner can remove it |
| Push queues (`recap_pushes`: recap, belt, join) | An item for the user is removed. An item whose text or event names the user is removed, sent or not |
| Chat images the user posted | Every object under `{crew_id}/{user_id}/` in the media bucket is deleted, for each crew of the record |
| Invite codes (`invite_codes`) | `inviter_user_id` is removed. The code stays: it is the invite link of the crew, and other people hold it |
| The group of the crew | `creator_id` becomes the owner the crew has now. `created_by` leaves the metadata. See below |
| Membership rows (`group_edge`) | Nakama 3.21.0 keeps the rows of a deleted superadmin or admin. The scrub deletes them |

The membership rows are deleted with SQL. The Nakama runtime has no call for it: a kick lowers the member count a second time. The statement names the crew and the user, and deletes nothing while the account exists. A superadmin row that stays would count in the last-superadmin checks of Nakama. With no database handle in the module the scrub is not done.

The group update of Nakama (`GroupUpdate`) has no version check. Verified on Nakama 3.21.0 (`dev_delete_selftest`): an empty name, description, language, avatar and creator, a nil metadata and a max count of 0 change nothing. The open flag is always written. A metadata that is given replaces the whole metadata. So the scrub reads the group again right before the update. When the open flag, the metadata or the creator changed since the first read, it writes nothing and the next run tries again.

The quests record, the crew emoji and the push queues are read by key for each crew of the record. They are not scanned: a member has a `member_joined` event in the ledger of the crew, so the ledger finds the crew.

`media_reports` is not scrubbed. A report is a moderation record.

A name list that is not aligned with its id list loses the entries that are a name of the user. When none is, the list is emptied.

**Recaps.** A weekly recap is never deleted. The totals, `top_game` and `games_played` stay: they name nobody.

| Recap field | Action |
|---|---|
| `top_members`, `game_records`, `belts_held`, `leaderboard` | The entry of the user is removed, by `user_id`, or by name when the entry has no id |
| `most_clipped`, `most_clipped_user_id`, `most_clipped_count` | Cleared, all three |
| `most_active` | Cleared when it is a name of the user |
| `best_streak_name`, `best_streak` | Cleared, both |
| `awards` | An award with the name of the user is removed. A belt award loses a detail that names the user |
| `belts_moved` | A move to the user is removed. A move from the user loses `previous_holder_user_id` and `previous_holder_name` |
| `clip_of_week` | Removed when it is a deleted clip of the user, or has the clipper name of the user. Otherwise the user leaves `participant_names` |
| `longest_session` | The text is cleared when it holds a name of the user |

Names:

- The names of the user are the display name and the username at the delete. A recap adds the name it holds beside the user id.
- A name is matched whole. In a free text, the character before and after it is not a letter or a digit.
- A name that another member of the crew has now is not matched in a field with no id. That field stays.
- A name the user had before the delete is not used.

One run of the scrub (`runUserScrub`), with a deadline of 2 minutes:

1. Find the crews: the crews of the record, the user index of the store, and a scan for the user id in `crew_events`, `crew_clips`, `crew_stream_sessions`, `crew_recaps`, `crew_head2head`. The scan also reads `invite_codes` and clears the inviter there, in the first run only.
2. For each crew: scrub the three documents, then the rows (200 at a time), then every recap, then the head2head holders, then the group, the quests record, the crew emoji and the push queues. The membership rows and the owner repair come first. A crew of `sole_crews` with no group is purged in place of this step.
3. Delete the media of the deleted clips and stream sessions.
4. Set `done_ms`. The names leave the record.

- The documents come first in every phase, so the reconcile cannot bring the user back from a document.
- A row that the scrub deletes is remembered (`mello_crew_event_deleted`). No import stores it again.
- A crew that fails stays in the record. The other crews still run.
- A second run comes 24 hours after `done_ms`. A voice session that ends after the delete can name the user. The second run finds the user by id. Then the record is deleted.
- A record of an account that still exists 1 hour after the delete attempt is deleted. The account delete did not happen.
- With no store loaded, the run scrubs the documents and stays pending. The first run with a store scrubs the rows.
- The scrub purges only a crew the user was the only member of (see above).

Media of the user:

- A clip: the video and the poster of the clip. A stream session: every object under `snapshots/{crew_id}/{session_id}/`. The chat images of the user: every object under `{crew_id}/{user_id}/` in the media bucket, from two ids that are UUIDs. An install with no media bucket has none.
- An entry stays in the record until its delete is complete.
- Not deleted, and named in the result and in the log (`user_scrub_media_skipped`): an id that is not safe in an object key, a stream session with no session id, and media that the crew still holds under the same `clip_id` or session id.

### 10.3 Crew deleted

`delete_crew` and the `AfterDeleteGroup` hook (`AfterDeleteCrew`) start the purge (`purgeCrewData`). The scrub of an account starts it for a crew the user was the only member of (section 10.2).

The purge record is `ledger_crew_purge/{crew_id}`, system-owned. It is written before anything is deleted, and it is kept for good. It is the proof that the crew was a crew of this install: a bucket can be shared with another install.

A record is an order to delete. Three places read the group first:

| Place | The group exists | The group read fails |
|---|---|---|
| The start of a purge | No record is written | No record is written |
| The purge itself | Nothing is deleted. The record is deleted | Nothing is deleted. The record stays |
| The reconcile | The record is deleted. The document is reconciled as any other | Nothing is deleted or imported. The document waits |

What a purge removes:

| Kind | What |
|---|---|
| Store | The event rows, the user index, the week rollup, the anchor, the memory of deleted ids |
| Ledger documents | `crew_events`, `crew_clips`, `crew_stream_sessions`, `crew_recaps` |
| Other documents | `crew_quests`, `crew_profile`, `crew_emoji`, `crew_avatars`, `voice_channels`, `crew_head2head`, the three `recap_pushes` documents of the crew, `stream_meta`, and `active_streams` of the streamer that `stream_meta` names |
| Clips bucket | `crews/{crew_id}/`: voice clips, game clip videos and posters |
| Snapshots bucket | `snapshots/{crew_id}/` |
| Media bucket | `crew-banners/{crew_id}/`, `crew-emoji/{crew_id}/`, and the chat images under `{crew_id}/` |
| Chat messages | Every message of the group channel of the crew, in the `message` table of Nakama |
| Chat reactions | Every `chat_reactions` document with the crew id, also of a message that is gone |

Chat:

- A crew has one text channel: the group channel of Nakama (stream mode 3, the subject is the group id). A voice channel has no text. The clients open no other channel.
- A group delete of Nakama leaves the messages in the `message` table.
- The Nakama runtime has no call that deletes the messages of a channel. `ChannelMessageRemove` deletes one message, needs its sender, and sends one event to the channel. So the purge uses SQL on the `message` table, with placeholders, 200 messages at a time (`ledger_purge_chat.go`).
- Every statement names stream mode 3 and the crew id. A direct message has stream mode 4 and two user ids. No statement can match it.
- Nothing lists the reactions of a crew. The purge reads the keys from the storage table with SQL, by the crew id in the document, and deletes the documents with the storage call of Nakama. The read looks at every reactions document of the install. It runs once for each full batch and once more. When the reactions are gone the purge record says so (`reactions_done`), and the second run does not read. `admin_purge_crew` reads again. No reaction is written for a crew with no group: `chat_react` asks for a member.
- A prefix at the root of a bucket (`{crew_id}/`) is deleted only for an owner that is a group id.
- The chat is deleted under the same rules as the rest: only with a purge record, only when the group is read and is gone, in batches, up to the deadline of the run. A run that is cut continues in the next run.
- A message table that holds a message of the crew makes the crew known to `admin_purge_crew`.

- Each part runs also when another part failed. The purge is done when one run removed everything with no error.
- A run is cut after 2 minutes. The next run continues.
- A second run comes one hour after the first complete run. A late writer can write for the crew after the group is gone.
- With no store loaded, the purge removes the documents and the media and stays pending.

### 10.4 Deletion job and reconcile

`StartLedgerDeletionJob` runs at start and every 5 minutes, on every node. It runs the scrubs and the purges that are not done, and their second runs. Two nodes can run the same one: every step can run twice.

| Case | Result |
|---|---|
| A document holds an entry the scrub deleted | Not stored again |
| A document holds the user again in an entry that stays, before the cutover | The store takes the document. Run `admin_scrub_user` |
| A document of a purged crew whose group is gone | The reconcile deletes the document and the rows. It imports nothing |

**The sweep of membership rows.** Accounts that were deleted before the scrub removed membership rows, or with no hook, can have rows in `group_edge`. The deletion job removes them (`sweepGoneMembers`):

- One pass reads up to 500 pairs of a group that exists and a member with no account. It deletes the rows of each pair with the statement of the scrub, which checks again that the account is gone. Then it runs the owner repair for each of those crews.
- The log line is `gone_member_sweep members=.. rows=.. crews=.. owners_repaired=.. failed=..`.
- A pass that finds no pair writes the marker `ledger_migration/gone_members_swept`. Then the sweep does not run again on this install.
- Cost: one pass reads the `group_edge` table once, with one index read in `groups` and one in `users` for each row. An install with no such row pays one pass.

### 10.5 Repair by hand

Both RPCs are server-to-server (`http_key`). Spec 04 has the contracts.

| RPC | Use |
|---|---|
| `admin_scrub_user {user_id}` | Runs the scrub of a deleted account again. It refuses while the account exists. Call it again while `done` is false |
| `admin_purge_crew {crew_id}` | Runs the purge of a deleted crew. It refuses while the group exists, and for a crew id this install holds nothing of |

Use `admin_scrub_user` for an account that was deleted with no hook: the Nakama console, or a lost scrub record. Such a scrub has no names. It matches ids, and the name beside the id in the same recap. It has no `sole_crews`, so it purges no crew.

Use `admin_purge_crew` for a crew whose group went with the account of its owner and that the scrub did not purge: the account was deleted with no hook (the Nakama console). The purge then removes the chat of the crew too.

### 10.6 Not covered yet

**Leftovers of a deleted account.**

| Where | What stays | Why |
|---|---|---|
| Chat messages of the user | Nothing. Nakama deletes them with the account: the `message` table has a foreign key on the sender | |
| Chat messages of other members | A mention of the user (`<@user_id>`), and the text | Not decided |
| Chat reactions | The id of the user in the reactions of a message. The reactions document of a message of the user, with no message: nothing finds it without a read of every reactions document. It goes when the crew is purged | Not decided |
| Chat images of the user | Nothing in a crew of the record. An image in a crew that neither the membership nor the ledger names stays until the crew is purged | |
| Direct messages | Nakama keeps a message that another user sent to the user. The clients open no direct message channel | |
| `media_reports` | The report | A moderation record |
| A crew whose owner was deleted with no hook | The data of the crew, without the user | Run `admin_purge_crew` |
| A crew with no owner and no member | The group and its data | Two owners deleted at once, and no one else was in the crew |

**Leftovers of a deleted crew.** A purge does not remove:

| What | Why |
|---|---|
| `crew_last_seen`, `chat_read` | Owned by each member. They hold a time. Nakama deletes them with the account |
| `invite_codes` | The key is the code. Nothing lists the codes of a crew. A join with the code answers that the crew is gone |
| `thumbnails`, `media_reports` | Keyed by a stream or a report |
| Direct messages between two members | Not of the crew |

**Other limits:**

- A name the user had before the delete stays in a recap field or a queued push that has no id for the user.
- The second run of a scrub has no names. It removes a queued push by the id of the user, and not by a name in the text.
- A crew that was deleted before the purge had the chat keeps its chat. Run `admin_purge_crew` for it.
- The promote messages of a handover (code 7) stay in the chat of the crew. They hold the id of the new owner.
- An emoji, a quests field or a queued push of a crew that neither the membership nor the ledger names the user in stays. Such a crew is not found.
- The group update of the scrub can still undo a change that another writer makes between its second read and its write.
- An account that is deleted with no hook after the sweep is done keeps its membership rows until `admin_scrub_user` is run for it.
- A recap field with only a name that another member also has stays.
- When the purge record cannot be written, nothing is purged. The log line is `crew_purge_failed`. Run `admin_purge_crew`.
- A write for a purged crew that makes only a store row, more than one hour after the purge, stays.
- With a store that never loads, the scrub and purge records stay pending. A scrub record then keeps its names.

---

## 11. Future Extensions (Not In Scope)

These are documented so future specs can build on the event ledger without redesigning it:

- **AI-powered catch-up (v2):** Feed `top_events` to a lightweight LLM for natural language polish. Premium/hosted feature only.
- **Weekly crew digest:** Aggregate a week's events into a summary, delivered as a notification or email.
- **Activity feed:** Render the full event list as a scrollable feed within the crew view.
- **Year in review:** Annual summary of crew activity (most played games, total voice hours, top moments).
- **Crew analytics (premium):** Charts showing activity trends, peak hours, member engagement.

---

*This spec covers the crew event ledger, its durable store and deletion rules, catch-up card data, and post-game moment flow. For presence and crew state, see [11-PRESENCE-CREW-STATE.md](./11-PRESENCE-CREW-STATE.md). For voice channels, see [13-VOICE-CHANNELS.md](./13-VOICE-CHANNELS.md).*
