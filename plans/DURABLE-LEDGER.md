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
at start, all `IF NOT EXISTS`. Self-host needs nothing new. Only `database/sql`,
no array columns, no new dependency.

- The schema setup takes `pg_try_advisory_lock` and gives up after 30 seconds.
- When the setup fails, the module starts on the legacy document alone.
  `startLedgerStore` tries again in the background (5 s, doubling to 5 min)
  and sets the store when the tables exist. The reconcile then copies what the
  store missed.

```
mello_crew_event(crew_id TEXT, ts BIGINT, id TEXT, type TEXT, actor_id TEXT DEFAULT '',
                 score INT, ref TEXT DEFAULT '', data JSONB, created_ms BIGINT,
                 pos BIGINT,
                 PRIMARY KEY (crew_id, ts, id))
UNIQUE (crew_id, id)
INDEX  (crew_id, type, ts DESC, id)
INDEX  (crew_id, type, ref) WHERE ref <> ''      -- clip_id, stream session_id
INDEX  (type, ts)                                -- snapshot backfill
INDEX  (crew_id, pos)                            -- append order
mello_crew_event_user(user_id TEXT, crew_id TEXT, event_id TEXT,
                      PRIMARY KEY (user_id, crew_id, event_id))
INDEX  (crew_id, event_id)
mello_crew_event_week(crew_id TEXT, week_start BIGINT, type TEXT, n INT,
                      PRIMARY KEY (crew_id, week_start, type))
mello_crew_event_anchor(crew_id TEXT PRIMARY KEY, last_append_ms BIGINT, last_pos BIGINT)
mello_crew_event_deleted(crew_id TEXT, event_id TEXT, deleted_ms BIGINT,
                         PRIMARY KEY (crew_id, event_id))
```

- `week_start` uses the existing `weekStartOf` (UTC Monday).
- One append is one transaction: the anchor row, the event row, one row per
  user the event names, one rollup upsert. Unique `(crew_id, id)` makes it
  idempotent.
- `mello_crew_event_anchor` holds the time of the last append of a crew and
  the last position given out. Every write that gives out positions updates
  this row first, so the writes of one crew run one after the other.
- `pos` is the append order of a crew. `created_ms` is only the time the row
  was written.
- `mello_crew_event_deleted` holds the ids that `Delete` removed. `Backfill`
  does not store such an id again. A live `Append` of the id clears it.
- `eventUserIDs(event)` lists every user an event names: actor,
  `participant_ids`, `player_ids`, `viewer_ids`, `streamer_id`, `invited_by`,
  clip participants.

Type sets, in `ledger_store.go`:

- `ledgerPulseTypes`: `voice_session, stream_session, game_session, member_joined, member_left, chat_activity, moment`
- `ledgerCountedTypes`: `clip, stream_session, game_session, weekly_recap`
- Legacy types. A ledger document can hold `clip` and `weekly_recap` events
  (the dev seed writes them, old crews have them). The legacy ledger path
  stores an event whose type is not a pulse type as `ledger_<type>`
  (`ledgerStoredType`): `ledger_clip`, `ledger_weekly_recap`. They do not mix
  with the `clip` and `weekly_recap` rows of phase 1b, and no counted query
  sees them. `RecentView` gives them back under their document type.

Interface. `currentLedger()` returns the store, `setLedger` sets it. It is nil
until the schema is ready, and in tests that do not set it.

```go
type ledgerStore interface {
    Append(ctx, ev CrewEvent, ref string) (inserted bool, err error)
    Backfill(ctx, crewID string, docEvents []CrewEvent, anchorMs int64, docOrderWins bool) (inserted int, err error)
    UpdateData(ctx, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error
    Delete(ctx, crewID, eventID string) error            // remembers the id
    Prune(ctx, crewID string, eventIDs []string) error   // does not remember the ids
    Between(ctx, crewID string, fromMs, toMs int64, types []string) ([]CrewEvent, error)   // both bounds inclusive
    NewestN(ctx, crewID string, types []string, beforeMs int64, n int) ([]CrewEvent, error)
    CountOlder(ctx, crewID string, types []string, beforeMs int64) (int, error)
    WeeksBefore(ctx, crewID string, types []string, beforeWeek int64, limit int) ([]weekCount, error)
    RecentView(ctx, crewID string) ([]CrewEvent, error)   // the 7-day compatibility view
    ByType(ctx, typ string, sinceMs int64) ([]CrewEvent, error)
    DeleteCrew(ctx, crewID string) error
    EventsNamingUser(ctx, userID string) ([]CrewEvent, error)
}
```

`RecentView` is what the legacy document held:

- Types: the pulse types and the `ledger_` types.
- Window: `ts >= last_append_ms - 7 days`. The legacy document was trimmed
  against the time of each append, not against the newest event time. A game
  session from the pending queue can arrive with an old timestamp.
- Order: `pos`, the append order. The guest feed and the invite preview take
  the last N events.

`Backfill` puts an event at its document position (`ledgerMergeOrder`):

- With `docOrderWins` (before the cutover) the events of the document take
  the document order.
- Without it (after the cutover) the events the crew holds keep their order.
  Each new event goes right after the nearest event that precedes it in the
  document.
- Only a changed order is written.

Read cost on a feed load in later phases:

| Query | How | Cost |
|---|---|---|
| Newest N counted | One `ORDER BY ts DESC LIMIT N` with `type IN (...)` | N rows |
| Count older than t | Sum `mello_crew_event_week` for whole weeks, index count for the partial week | No event rows |
| Window | `Between(start, now)` | Rows in the window |
| History page | `WeeksBefore`, then `Between` for open weeks | Rollup rows plus open weeks |

## 5. Migration and rollback

One of the two stores is the truth: the one the readers use.

| Phase | Truth | Write order | Failure of the first write | Failure of the second write |
|---|---|---|---|---|
| Before the cutover | Legacy document | Document, then store | Error to the caller, store not written | Logged, the reconcile repairs it |
| After the cutover | Durable store | Store, then document | Error to the caller, document not written | Logged only |

The rule holds for `AppendCrewEvent`, `UpdateLedgerEventSnapshotURLs` and
`removeCrewEvent` (`ledgerWriteBoth` in `ledger_dual_write.go`).

Steps:

1. Schema is created at start. Harmless if unused.
2. Dual write starts with the first reconcile tick. It writes
   `ledger_migration/state {"dual_write_started_ms": ...}`.
3. Reconcile: goroutine `StartLedgerReconcile`, at start and every 5 minutes.
   One tick handles up to 1000 crews. A pass over more crews continues on the
   next tick.
4. Marker: the reconcile sets `events_done_ms` in the state document when a
   pass was clean and dual write has run for 24 hours
   (`MELLO_LEDGER_SOAK_SECONDS` overrides, the e2e stack uses 0). The soak
   closes the deploy overlap: an instance of the previous release writes only
   the document.
5. Cutover: readers and the write order follow the marker. Each node re-reads
   the state document every 30 s until the marker is set.

Reconcile of one crew (`reconcileLedgerCrew`):

| | Before the cutover | After the cutover |
|---|---|---|
| Document event the store lacks | Insert at its document position | Insert at its document position |
| Store event of the 7-day view that the document lacks | Prune | Keep |
| Data differs | Take the data of the document | Keep the data of the store |
| Order differs | Take the order of the document | Keep the order of the store |
| Event the store removed (`Delete`) | Not inserted again | Not inserted again |

- Each round reads the document again, up to 3 repair rounds per crew. A
  write to the crew during a round shows in the next round.
- The reconcile prunes only inside the 7-day window of the document. Older
  rows are history that the document trimmed, and they stay.
- A pass is clean when every crew ends with a 7-day view equal to its
  document, and no read or write failed. A document that cannot be read or
  decoded makes the pass not clean. It is not taken for an empty document.
- An event without an id cannot go into the store. It is counted
  (`no_id` in the log line) and does not block the marker.
- Log line per pass: `ledger_reconcile_pass crews= inserted= pruned= updated= no_id= unclean= failed= marker=`.
  A crew that does not converge logs `ledger_parity_mismatch crew=...`.

Rollback:

- Env `MELLO_LEDGER_READ=doc` is the kill switch. Readers use the document and
  the document is written first again. The document stays current under dual
  write.
- Or deploy the previous image. It reads and writes only the document. The
  tables stay and are ignored. After the roll forward the reconcile inserts
  the events of that period.
- Known gap: an event removed by `removeCrewEvent` while rolled back to the
  previous image stays in SQL. It affects only a stream resumed in that window.
- Known gap: after the cutover a document write that fails is only logged.
  The document then lacks that event until the legacy document is removed.
- Dual write and the legacy doc go one release after cutover.

## 6. Readers

`readLedger(ctx, nk, crewID)` keeps its signature and returns `RecentView` of
the store after the cutover (section 4). A store read that fails falls back to
the document.

| Reader | Change |
|---|---|
| `buildMergedTimeline` (`clips.go`) | Call unchanged. `EventRollingWindowDays` stays for the clip cut |
| `crew_catchup` (`crew_events.go`) | Call unchanged |
| Guest feed, invite preview (`guest_sessions.go`, `invite_codes.go`) | Call unchanged. Public surfaces: they see what they saw before, also the `clip` and `weekly_recap` events a ledger document held, and no row of phase 1b |
| `PostMomentRPC` rate limit | Call unchanged |
| `GameSessionEndRPC` co-players (`coplay.go`) | Call unchanged |
| `generateWeeklyRecapAt` (`crew_recaps.go`) | `readLedgerWindow`: `Between(startMs, endMs)`, legacy ledger path only. A late run now covers its full week |
| `UpdateLedgerEventSnapshotURLs` | `ledgerStore.UpdateData`, write order of section 5 |
| `removeCrewEvent` (`streaming.go`) | `ledgerStore.Delete`, write order of section 5 |
| `snapshotBackfill` (`streaming.go`) | `ByType("stream_session", cutoff)`, grouped by crew, one session per crew per tick as today |
| `dev_seed.go` | Reset also calls `DeleteCrew` |

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
  `TestReconcile*`, `TestSoakGate`, `TestAfterCutover*`, `TestBeforeCutover*`,
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
5. `ledger_migration.go`: reconcile, soak, marker. `go test -run 'TestBackfill|TestReconcile|TestSoakGate' .`
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
