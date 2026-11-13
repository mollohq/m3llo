# Durable crew ledger

Status: in build on `feat/durable-ledger`. Phase 1a (steps 1 to 6) and phase 1b
(steps 9 to 13) are built. Steps 7, 8 and 14 are open. This is a plan. Specs
change when the code ships.

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
- `pos = 0` is a row that did not come through the legacy ledger path (`Put`,
  phase 1b). It is not in the 7-day view. `Backfill` gives it a position when
  a ledger document holds the event.
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
    // Phase 1b: rows outside the 7-day view. They do not move the anchor.
    Put(ctx, crewID string, rows []ledgerRow) (inserted int, err error)        // skips a held id and a deleted id
    PutUnique(ctx, crewID string, row ledgerRow) (held []CrewEvent, err error) // one row for a ref of a type
    ByRef(ctx, crewID, typ, ref string) ([]CrewEvent, error)
    ByIDs(ctx, crewID string, ids []string) ([]CrewEvent, error)
}
```

`PutUnique` takes `pg_advisory_xact_lock` on the crew, the type and the ref,
then checks and inserts in one transaction.

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
- Phase 1b: `MELLO_LEDGER_HISTORY_READ=doc` puts its readers back on the
  documents. A crew over an old cap then sees its capped document again.
- Phase 1b known gap: after its cutover a clip whose document write failed is
  missing on a rollback to the documents.

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

## 6a. Phase 1b: clips, stream sessions, recaps

Built. Code: `ledger_history.go`, `ledger_migration.go`, `admin_media_orphans.go`.

### Rows

| Thing | Type | Row id | Ref | Time | Data |
|---|---|---|---|---|---|
| Clip | `clip` | `clip_{event_id}` | `clip_id` | `ts` of the clip | The stored clip |
| Stream session | `stream_session` | Id of the ledger event | `session_id` | `ts` of the event | `StreamSessionData` |
| Weekly recap | `weekly_recap` | `recap_{week_start}` | none | `week_start` | The recap |

- `NewestN` and `CountOlder` see these rows. They are the counted types.
- The prefix `clip_` keeps a clip id apart from the ids of ledger events. One
  crew holds an id once.
- The time of a recap is its week start. Crew history files a recap under the
  week it is about. `generated_at` stays in the data.
- The store holds one recap for a week start. The first one wins.

### One row for a stream

A stream that ends writes a `stream_session` event to the ledger and a copy to
`crew_stream_sessions`. The copy holds the id of the event.

| Case | Rule |
|---|---|
| The ledger document holds the event | The row is the row of the event (phase 1a). The copy changes nothing |
| Only the copy holds the session (older than 7 days, or the event write failed) | `Put` stores it under the id of the event, with `pos = 0` |
| The ledger document later holds the event | `Backfill` gives the row its position. No second row |
| The stream was resumed (`removeCrewEvent`) | The store remembers the id. The copy does not bring the row back |
| Two events for one session id (stop and socket drop) | Two ledger rows. The feed lists the newest one, as the copy did |

- `stream_session` is a pulse type. No `ledger_` row exists for a stream.
- `ledger_clip` and `ledger_weekly_recap` rows are the clip and recap events
  that a ledger document held. They serve the 7-day view of the guest feed
  and the invite preview only. They are not clips or recaps of the crew, and
  no counted query sees them.

### Marker, soak, kill switch

Phase 1b has its own state document, `ledger_migration/history
{"started_ms", "done_ms"}`, and its own marker.

- Reason 1: a release of phase 1a can be cut over before phase 1b is
  deployed. With a shared marker the readers of 1b would switch with no copy
  and no soak.
- Reason 2: a release of phase 1a rewrites its state document as a whole. It
  would drop fields of 1b.
- The marker of 1b needs: a clean pass of both phases, the marker of 1a, and
  24 hours since `started_ms` (the same `MELLO_LEDGER_SOAK_SECONDS`).
- An install that jumps from a release before 1a starts both soaks on the
  first tick. Both markers can be set in the same pass.
- `MELLO_LEDGER_HISTORY_READ=doc` puts only the readers and the write order
  of 1b back on the documents. `MELLO_LEDGER_READ=doc` puts both phases back.

### Write order

The same rule as section 5 (`ledgerWriteOrdered`), with the marker of 1b.

| Write | Before the cutover of 1b | After it |
|---|---|---|
| `AppendClip` | Document first: it decides if the `clip_id` is free. Then `Put` | `PutUnique` first: it decides. Then the document |
| Clip media and poster URL (`updateStoredClip`) | Document, then `UpdateData` | `UpdateData`, then the document |
| `AppendRecap` | Document, then `Put` | `Put`, then the document |
| Stream session | `AppendCrewEvent` (phase 1a), then the copy, as today | The same. The copy is no longer read |

- The first write of a clips document is now create-only (version `*`). Two
  uploads at once to a new crew both stay.
- `UpsertStreamSession` and `capClips` stay as the writers of the legacy
  documents. They go with the documents.

### The rule for the caps

- A legacy document keeps its cap (250 clips, 150 stream sessions). The store
  has no cap. So the store holds more than the document.
- No RPC deletes a clip, a stream session of the copy or a recap. An entry
  leaves a document only by the cap.
- So the reconcile of phase 1b never deletes a row. A row that the document
  does not hold is history that the document trimmed.
- Parity is: every entry of the document has its row, with the same content.

Reconcile of one document (`reconcileHistoryCrew`):

| | Before the cutover | After the cutover |
|---|---|---|
| Entry the store lacks | `Put` | `Put` |
| Row the document lacks | Keep | Keep |
| Content differs | Take the document. A stream row keeps its viewers | Keep the store |
| Id that `Delete` removed | Not stored again | Not stored again |
| Entry with no event id | Counted (`no_id`), not stored | The same |

- A pass visits the ledger documents, then `crew_stream_sessions`,
  `crew_clips`, `crew_recaps`.
- Log line: `ledger_history_pass docs= inserted= updated= no_id= unclean= failed= marker=`.
  A document that does not converge logs `ledger_history_mismatch kind= crew=`.

### Readers after the cutover

| Reader | Read |
|---|---|
| `crew_feed` (`loadZoneHistory`) | The free window (`Between`), the newest recap that is not empty, the newest clip and stream time, and 5 weeks of Crew history from `WeeksBefore`. No whole-crew read |
| `buildMergedTimeline` | Clips of 7 days (`Between`), the newest recap (`NewestN`) |
| `crew_catchup` | Clips after `last_seen` (`Between`) |
| `post_clip`, `clip_upload_url`, `clip_upload_complete` | `ByRef(clip_id)`. Rate limit: clips of the day (`Between`) |
| Weekly recap job | Clips of the week (`Between`). `recapDue` from the newest recap row |
| Guest feed | The newest 250 clips (`NewestN`) and the clip count (`CountOlder`) |
| Invite preview | The ledger only, as before |

A store read that fails falls back to the documents.

### Known differences after the cutover

- A crew over an old cap sees every clip and stream session. This is the goal.
- A stream that was resumed has no replay entry until it ends again. Before,
  the copy still listed the interrupted part.
- A stream session whose copy write failed is now listed as a replay.
- A stream session whose ledger event write failed, and that only the copy
  held, now counts in the weekly recap of its week (`readLedgerWindow` reads
  every row of the week).
- Two recaps with one week start are one recap.
- The guest feed counts clips by row above 250 clips.

### admin_media_orphans (step 13)

`http_key` RPC. Request `{delete?, max_objects?, clips_after?, snapshots_after?}`.

- Lists `crews/` in the clips bucket and `snapshots/` in the snapshots bucket.
- An object is an orphan when no clip (by `clip_id`) and no stream session (by
  `session_id`) of the crew points at it, and it is older than 24 hours.
- A crew holds a clip or a session when the legacy document, the ledger
  document, the live stream record or a store row has it.
- Reports for each prefix: `scanned, orphans, bytes, too_new, unknown,
  read_failed, deleted, delete_failed, sample, next_after`.
- A key that does not follow the layout is `unknown`. A crew that cannot be
  read is `read_failed`. Neither is deleted.
- Dry run by default. A delete needs `delete: true`, the env
  `MELLO_MEDIA_ORPHAN_DELETE=1` and the durable store.
- The delete was run only against a local fake in tests.

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
- Phase 1b. `history_characterisation_test.go` pins the read paths of clips,
  stream sessions and recaps. It was written against the code before 1b and
  did not change. Each test runs in three modes (`history_modes_test.go`):
  `document`, `import` (seed the documents, then reconcile and cut over) and
  `store` (cut over, then seed).
- `TestZonesFromTheStoreEqualTheDocuments`: the zone feed JSON of random
  crews, every page, from the documents and from the store.
- `TestZonesReadOnePageOfALargeCrew`: rows and queries of one page are bounded.
- `TestClipIDIsUniqueUnderConcurrentUploads`, `TestConcurrentFirstClipsLoseNothing`,
  `TestClipReconcileKeepsWhatTheDocumentDropped`, `TestStreamSessionHasOneRow`,
  `TestHistoryMarkerGate`, `TestAdminMediaOrphans*`.
- `ledgerPutConformance` runs in the conformance function, so
  `dev_ledger_selftest` covers `Put`, `PutUnique`, `ByRef` and `ByIDs` on SQL.

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

Phase 1b, as built:

9. Stream sessions. The reconcile imports `crew_stream_sessions`. The feed
   reads the rows. Own marker for 1b. `go test -run 'TestHistory|TestStreamSession|TestLedgerDocument|TestChar' .`
10. Clips as rows (`type clip`, `ref = clip_id`), dual write, import, no cap in the store. `go test -run 'TestClip|TestConcurrent|TestChar' .`
11. Recaps as rows (`id = recap_{week_start}`), dual write, import. `go test -run 'TestRecap|TestChar' .`
12. `crew_feed` pages from the store. Output shape unchanged. `go test -run 'TestZones|TestChar' .`
13. `admin_media_orphans` with a dry run. `go test -run 'TestAdminMediaOrphans|TestMediaObject' .`
14. Specs CLIPS, CREW-FEED-ZONES and 04 (the `admin_media_orphans` row). Open.

Different from the first plan:

- `UpsertStreamSession` and `capClips` stay until the legacy documents go.
  The documents must be written while they are the truth and for a rollback.
- The clip row id is `clip_{event_id}`, not the bare event id.
