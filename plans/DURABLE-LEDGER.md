# Durable crew ledger

Status: in build on `feat/durable-ledger`. This is a plan. Specs change when the code ships.

Paths are relative to `backend/nakama/data/modules/` unless stated.

## 1. Why

- `AppendCrewEvent` (`crew_events.go:264-290`) deletes ledger events older than
  `EventRollingWindowDays = 7` on every append. Game sessions, voice sessions,
  joins, chat activity and moments have no other copy, so they are lost.
- `feed_config.free_days` moves the free window, but the session data under it
  is already cut to 7 days. Raising the setting shows no older sessions.
- Crew history (`zoneHistory`) holds only clips, streams and recaps, so m3llo+
  cannot show older sessions.
- Three appends that race can lose an event: the retry stops after 3 attempts
  and callers only log. The first write uses version `""`, which is unconditional.

## 2. Decisions

- Every event is kept. Plan never causes a delete. The gate is on read.
- Free rule (phase 3): a member without m3llo+ sees the last N counted events
  or the last D days, whichever reaches further back. Start N = 30, D = 14,
  both in admin config.
- Counted events: `clip`, `stream_session`, `game_session`, `weekly_recap`.
  Voice sessions, joins and chat activity are stored but do not count.
- The 250 clip cap and the 150 stream cap are dropped for everyone (phase 1b).
- m3llo+ is per user, never per crew.

## 3. Phases

| Phase | What | Visible change |
|---|---|---|
| 1a | Ledger events in a durable store, dual write, backfill, readers moved | None |
| 1b | Clips, stream sessions and recaps in the same store, caps removed | None |
| 2 | Game and voice sessions in Crew history | Yes |
| 3 | The free rule above, admin setting | Yes |
| 4 | m3llo+ admin grant, then purchase | Yes |

This document covers phase 1.

## 4. Store

Tables in the Nakama Postgres database, created by `ensureLedgerSchema(ctx, db)`
in `InitModule` under `pg_advisory_lock`, all `IF NOT EXISTS`. Self-host needs
nothing new. Only `database/sql`, no array columns, no new dependency.

```
mello_crew_event(crew_id TEXT, ts BIGINT, id TEXT, type TEXT, actor_id TEXT DEFAULT '',
                 score INT, ref TEXT DEFAULT '', data JSONB, created_ms BIGINT,
                 PRIMARY KEY (crew_id, ts, id))
UNIQUE (crew_id, id)
INDEX  (crew_id, type, ts DESC, id)
INDEX  (crew_id, type, ref) WHERE ref <> ''      -- clip_id, stream session_id
INDEX  (type, ts)                                -- snapshot backfill
mello_crew_event_user(user_id TEXT, crew_id TEXT, event_id TEXT,
                      PRIMARY KEY (user_id, crew_id, event_id))
mello_crew_event_week(crew_id TEXT, week_start BIGINT, type TEXT, n INT,
                      PRIMARY KEY (crew_id, week_start, type))
```

- `week_start` uses the existing `weekStartOf` (UTC Monday).
- One append is one transaction: the event row, one row per user the event
  names, one rollup upsert. Unique `(crew_id, id)` makes it idempotent.
- `eventUserIDs(event)` lists every user an event names: actor,
  `participant_ids`, `player_ids`, `viewer_ids`, `streamer_id`, `invited_by`,
  clip participants.

Type sets, constants in `ledger_store.go`:

- `ledgerPulseTypes`: `voice_session, stream_session, game_session, member_joined, member_left, chat_activity, moment`
- `ledgerCountedTypes`: `clip, stream_session, game_session, weekly_recap`

Interface, package-level `var ledger ledgerStore`, set in `InitModule`, swapped in tests:

```go
type ledgerStore interface {
    Append(ctx, ev CrewEvent, ref string) (inserted bool, err error)
    UpdateData(ctx, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error
    Delete(ctx, crewID, eventID string) error
    Between(ctx, crewID string, fromMs, toMs int64, types []string) ([]CrewEvent, error)
    NewestN(ctx, crewID string, types []string, beforeMs int64, n int) ([]CrewEvent, error)
    CountOlder(ctx, crewID string, types []string, beforeMs int64) (int, error)
    WeeksBefore(ctx, crewID string, types []string, beforeWeek int64, limit int) ([]weekCount, error)
    RecentView(ctx, crewID string) ([]CrewEvent, error)   // the 7-day compatibility view
    ByType(ctx, typ string, sinceMs int64) ([]CrewEvent, error)
    DeleteCrew(ctx, crewID string) error
    EventsNamingUser(ctx, userID string) ([]CrewEvent, error)
}
```

Read cost on a feed load in later phases:

| Query | How | Cost |
|---|---|---|
| Newest N counted | One `ORDER BY ts DESC LIMIT N` per counted type, merged in Go | 4 x N index rows |
| Count older than t | Sum `mello_crew_event_week` for whole weeks, index count for the partial week | No event rows |
| Window | `Between(start, now)` | Rows in the window |
| History page | `WeeksBefore`, then `Between` for open weeks | Rollup rows plus open weeks |

## 5. Migration and rollback

1. Schema is created at start. Harmless if unused.
2. Dual write: `AppendCrewEvent` writes SQL first, then the legacy doc with its
   trim unchanged. `UpdateLedgerEventSnapshotURLs` and `removeCrewEvent` apply to both.
3. Backfill: goroutine `StartLedgerBackfill` lists `crew_events` docs and inserts
   each event with `ON CONFLICT DO NOTHING`. It runs at every start. It then
   writes `ledger_migration/state {"events_done_ms": ...}`.
4. Cutover: readers use the doc until the marker exists, then SQL. Each node
   re-reads the marker every 30 s until set.
5. Parity: before the marker is set, compare `RecentView` with the doc per crew
   and log `ledger_parity_mismatch crew=...`. No marker on a mismatch.

Rollback:

- Env `MELLO_LEDGER_READ=doc` forces doc reads. The doc stays current under dual write.
- Or deploy the previous image. It reads the doc. The tables stay and are ignored.
- Known gap: an event removed by `removeCrewEvent` while rolled back stays in
  SQL. It affects only a stream resumed in that window.
- Dual write and the legacy doc go one release after cutover.

## 6. Readers

`readLedger(ctx, nk, crewID)` keeps its signature and returns `ledger.RecentView`
after cutover. `RecentView` returns pulse types only, with
`ts >= max(ts of the crew) - 7 days`. That reproduces the append-anchored trim,
including for a dormant crew. The anchor is the newest event time instead of the
last append time.

| Reader | Change |
|---|---|
| `buildMergedTimeline` (`clips.go:380`) | Call unchanged. `EventRollingWindowDays` stays for the clip cut |
| `crew_catchup` (`crew_events.go:394`) | Call unchanged |
| Guest feed, invite preview (`guest_sessions.go:525-530`, `invite_codes.go:337, 474`) | Call unchanged. Public surfaces, so they must see no more than today |
| `PostMomentRPC` rate limit (`crew_events.go:694`) | Call unchanged |
| `GameSessionEndRPC` co-players (`crew_events.go:934`, `coplay.go:116`) | Call unchanged |
| `generateWeeklyRecapAt` (`crew_recaps.go:297`) | `ledger.Between(startMs, endMs, pulse)`. A late run now covers its full week |
| `UpdateLedgerEventSnapshotURLs` | `ledger.UpdateData` |
| `removeCrewEvent` (`streaming.go:1470`) | `ledger.Delete` |
| `snapshotBackfill` (`streaming.go:1554-1630`) | `ledger.ByType("stream_session", cutoff)`, grouped by crew, one session per crew per tick as today |
| `dev_seed.go:368-372` | Reset also calls `ledger.DeleteCrew` |

## 7. Deletion rules (step 7, waits for sign-off)

Events used to vanish after 7 days. Now they stay, so these rules ship in phase 1.

| Case | Today | Add |
|---|---|---|
| Crew deleted | `GroupDelete` only (`crews.go:406-411`) | `purgeCrewData`, called from `DeleteCrewRPC` and a delete-group hook |
| Member leaves | `member_left` event, earlier events stay | Keep events |
| Member kicked | No event (`crews.go:536`) | Write `member_left` |
| Account deleted | Nothing on the server | Delete-account hook calls `scrubUserFromLedger(userID)` |

Scrub rule, pure function `scrubEventForUser(ev, userID) (out CrewEvent, drop bool)`:

- Drop the event when the user is the actor or the streamer.
- Otherwise remove the user's id and name from the participant, player and
  viewer lists (keep index alignment) and clear `invited_by`. Drop a
  `voice_session` left with fewer than two participants.
- http_key RPC `admin_scrub_user {user_id}` re-runs it after a failed hook.

Open: clip media and stored recap text that hold a deleted user's voice or name.

## 8. Tests

- Characterisation first, against the current code:
  `TestAppendCrewEventTrimsAtSevenDays`, `TestLedgerTrimAnchorsToLastAppend`,
  `TestMergedTimelineCutsClipsAtSevenDays`, `TestCatchupIgnoresEventsOutsideLedger`.
  They pass unchanged after cutover.
- `ledgerStoreConformance(store) error` in `ledger_store_conformance.go`: append
  idempotency, `Between` bounds, `NewestN` order and type filter, `CountOlder`
  equals a recount, rollup after delete, `RecentView` anchor, a 30-day-old event survives.
- `TestMemLedgerConformance` runs it on `memLedgerStore`.
- SQL: RPC `dev_ledger_selftest` (gated by `MELLO_ENABLE_DEV_FAULT=1`) runs the
  same function on the real table with a throwaway crew id. A test in
  `tools/e2e/tests/backend.rs` calls it.
- `TestConcurrentAppendsLoseNothing`, `TestBackfillIsIdempotent`,
  `TestDualWriteKeepsDocTrimmed`, `TestReadersUseDocUntilMarker`,
  `TestScrubEventForUser`, `TestPurgeCrewData`, `TestInvitePreviewSeesNoClipRows`.
- Tests that seed `CrewEventsCollection` directly move to a `seedLedger(t, ...)` helper.

## 9. Build steps

Run in `backend/nakama/data/modules`. After each step: `gofmt -l . && go vet ./...`.
`scripts/check-full.sh` runs once, before the branch is pushed.

Phase 1a:

1. Characterisation tests. `go test -run 'TestAppendCrewEventTrims|TestLedgerTrimAnchors|TestMergedTimelineCuts|TestCatchupIgnores' .`
2. `ledger_store.go`, `memLedgerStore`, conformance function. `go test -run 'TestMemLedgerConformance|TestEventUserIDs' .`
3. `ledger_store_sql.go`: schema, `sqlLedgerStore`, `dev_ledger_selftest`, wiring in `main.go`. `go test -run TestMemLedgerConformance . && go build ./...`, then the selftest once on the local stack.
4. Dual write. `go test -run 'TestDualWrite|TestConcurrentAppends|TestStream|TestSessionTimes' .`
5. `ledger_migration.go`: backfill, marker, parity log. `go test -run 'TestBackfill|TestLedgerParity' .`
6. Reader switch behind the marker and `MELLO_LEDGER_READ`. `go test -run 'TestReadersUseDoc|TestAppendCrewEventTrims|TestLedgerTrimAnchors|TestMergedTimeline|TestCatchup|TestWeeklyRecap|TestSnapshotBackfill|TestGuest|TestInvite' .`
7. Deletion rules (section 7). `go test -run 'TestScrub|TestPurgeCrewData|TestKick' .`
8. Specs 16 and 04, `TESTING.md`.

Phase 1b, second PR:

9. Stream sessions read from events, import the mirror, remove `UpsertStreamSession` and the cap.
10. Clips as rows (`type clip`, `ref = clip_id`), import the doc, remove `capClips`.
11. Recaps as rows (`id = recap_{week_start}`), import the doc.
12. `zoneHistory` and `crewFeedZones` page from the store. Output shape unchanged.
13. `admin_media_orphans` with a dry run, for media orphaned by past cap trims.
14. Specs CLIPS and CREW-FEED-ZONES.
