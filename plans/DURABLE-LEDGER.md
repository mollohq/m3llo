# Durable crew ledger

Status: in build on `feat/durable-ledger`. Phase 1a (steps 1 to 7) and phase 1b
(steps 9 to 13) are built. Steps 8 and 14 are open. This is a plan. Specs
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
  clip participants, and the members a weekly recap lists (`recapUserIDs`).

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
    ByRefs(ctx, crewID, typ string, refs []string) ([]CrewEvent, error)                 // rows of several refs, one read
    RefsBefore(ctx, crewID, typ, beforeRef string, n int) ([]CrewEvent, error)          // by ref, the highest first
    TopInRange(ctx, crewID, typ string, fromMs, toMs int64, field, value string, n int) ([]CrewEvent, error)
    // Step 7: the deletion rules.
    DeletedIDs(ctx, crewID string, ids []string) (map[string]bool, error)           // the ids that Delete removed
    UserCrews(ctx, userID string) ([]string, error)                                 // crews of the user index
    NamingUserInCrew(ctx, userID, crewID string, n int) ([]CrewEvent, error)        // a batch, oldest first
    ForgetUser(ctx, userID, crewID string, eventIDs []string) error                 // user index only
}
```

- `RefsBefore` reads the index `(crew_id, type, ref)`. Refs compare as text.
  A caller that wants an order uses refs of one width (`recapWeekRef`).
- `TopInRange` returns the rows whose data holds the text `value` in `field`
  first, each group newest first, the lower id first for one time. The store
  sorts and returns `n` rows.

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
| Weekly recap | `weekly_recap` | `recap_{week_start}` | Week start, 19 digits | `generated_at` | The recap |

- `NewestN` and `CountOlder` see these rows. They are the counted types.
- The prefix `clip_` keeps a clip id apart from the ids of ledger events. One
  crew holds an id once.
- The time of a recap row is `generated_at`. The counted queries of phase 3
  select by time ("newest N", "in the last D days"). A recap dated at its
  week start would be 7 days old when it is written. A recap with no
  `generated_at` gets the end of its week.
- The week of a recap is its ref (`recapWeekRef`). Crew history, the board,
  the merged timeline and the recap job read recaps by week (`RefsBefore`),
  not by time.
- The store holds one recap for a week start. The first one wins.
- An entry with no event id (from before the documents held it) gets a row id
  made from its other id: `clipref_{clip_id}` for a clip,
  `session_{session_id}` for a stream session. The data stays as the document
  holds it, so the readers get the same entry from both.
- A recap row that an earlier build of this branch wrote has its week start
  as its time and no ref. The reconcile does not change the time of a row.
  Reset a dev database that holds such rows (`dev_seed` reset).

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
| `AppendClip` | Document first: it decides if the `clip_id` is free. Then `PutUnique` | `PutUnique` first: it decides. Then the document |
| Clip media and poster URL (`updateStoredClip`) | Document, then `UpdateData` | `UpdateData`, then the document |
| `AppendRecap` | Document, then `Put` | `Put`, then the document |
| Stream session | `AppendCrewEvent` (phase 1a), then the copy, as today | The same. The copy is no longer read |

- The first write of a clips document is now create-only (version `*`). Two
  uploads at once to a new crew both stay.
- The store takes a clip with `PutUnique` in both phases. It holds one row for
  a `clip_id` at all times. Two cases gave two rows before: two nodes that do
  not agree about the marker for 30 seconds, and the kill switch on a crew
  over the document cap.
- When the document took a clip and the store holds the `clip_id` under
  another row, the row of the store stays. The log line is
  `ledger_clip_ref_held`.
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

| | The document is the truth | The store is the truth |
|---|---|---|
| Entry the store lacks | Store it | Store it |
| Row the document lacks | Keep | Keep |
| Content differs | Take the document. A stream row keeps its viewers | Keep the store |
| Id that `Delete` removed | Not stored again | Not stored again |
| Entry with no event id | Stored under an id made from its `clip_id` or `session_id` | The same |
| Entry with neither id | Counted (`no_id`), not stored | The same |
| Clip whose `clip_id` a row outside the document holds | Counted (`ref_held`), not stored | The same |

- The document is the truth until both markers are set, and under a kill
  switch (`MELLO_LEDGER_HISTORY_READ=doc`, `MELLO_LEDGER_READ=doc`). So a
  store update that failed under a kill switch is repaired after the marker.
  The marker alone does not decide (`historyDocumentIsTruth`).
- `no_id` blocks the marker of phase 1b (`not_set_entries_without_id`). Such
  an entry would vanish from the readers at the cutover. A second clip with
  no event id and the same `clip_id` counts too.
- A missing clip is stored with `PutUnique` when the document holds its
  `clip_id` once. A document from before `clip_id` was checked can hold one
  `clip_id` twice. Both entries are rows of the document, and both are stored.
- `ref_held` does not block the marker. The store row came first.
- A pass visits the ledger documents, then `crew_stream_sessions`,
  `crew_clips`, `crew_recaps`.
- Log line: `ledger_history_pass docs= inserted= updated= no_id= ref_held= unclean= failed= marker=`.
  A document that does not converge logs `ledger_history_mismatch kind= crew=`.

### Readers after the cutover

| Reader | Read |
|---|---|
| `crew_feed` (`loadZoneHistory`) | The free window (`Between`), the recap of the newest week that is not empty, the newest clip and stream time, and 5 weeks of Crew history. No whole-crew read |
| `buildMergedTimeline` | Clips of 7 days (`Between`), the recap of the newest week (`RefsBefore`) |
| `crew_catchup` | The newest 250 clips (`NewestN`), then the ones after `last_seen`. That is what the capped document held, also for `last_seen = 0` |
| `post_clip`, `clip_upload_url`, `clip_upload_complete` | `ByRef(clip_id)`. Rate limit: clips of the day (`Between`) |
| Weekly recap job | Clips of the week (`Between`). `recapDue` from the recap of the newest week |
| Guest feed | The newest 250 clips (`NewestN`) and the clip count (`CountOlder`) |
| Invite preview | The ledger only, as before |

A store read that fails falls back to the documents.

One page of Crew history (`loadZoneHistoryFromStore`):

- The weeks with a clip and the weeks with a stream session come from the
  week rollup, one `WeeksBefore` for each type. The recaps of the page come
  by week (`RefsBefore`). A week counts when `zoneHistory` takes something
  of it.
- An open week reads its clips and stream sessions (`Between`).
- A locked week that ends before the free window does not read its clips.
  The clip count is the count of the rollup. The teaser clips are 6 rows of
  `TopInRange` (game clips first, as `rankClips`). Its stream sessions are
  read: two rows of one session count once, and the rollup cannot say that.
- `crewFeedZones` reads the gate (m3llo+, founding weeks) before the history,
  and passes it as `zoneWeekOpen`.
- Two rows can hold one session id. The older row is dropped before the
  weeks are counted (`dropSupersededStreamRows`, one `ByRefs` for a page). A
  page is not short, and no page lists the session a second time.
- Clips of one time are ordered by event id. An event id starts with the
  time it was made, so this is the order of the writes, as in the document.

Store reads of one `crew_feed` call, with W = 5 weeks read for a page of 4.
`TestCrewFeedLoadQueryCount` counts every store call through the RPC.

| Part | Reads |
|---|---|
| Merged timeline: the 7-day view, the clips of 7 days, the newest recap | 3 |
| The free window | 1 |
| The newest clip and the newest stream session, when the window has none | 0 to 2 |
| The recap of the board | 1, and 1 more for each 4 empty recaps |
| The clip weeks, the stream weeks, the recaps of the page | 3 |
| An open week | 1 |
| A locked week | 2 (teaser clips, stream sessions) |
| The rows of the session ids of the page | 1 |

- A viewer with m3llo+: 3 + 1 + 1 + 3 + W + 1 = 14.
- A viewer without it: 3 + 1 + 1 + 3 + 2W + 1 = 19. It is 18 on the first
  page, when the newest week is the one the free window starts in.
- A page reads fewer rows for a viewer without m3llo+ (134 against 185 in the
  test crew). It makes more reads. The gain grows with the clips of a week.

### Known differences after the cutover

- A crew over an old cap sees every clip and stream session. This is the goal.
- A stream that was resumed has no replay entry until it ends again. Before,
  the copy still listed the interrupted part.
- A stream session whose copy write failed is now listed as a replay.
- A stream session whose ledger event write failed, and that only the copy
  held, now counts in the weekly recap of its week (`readLedgerWindow` reads
  every row of the week).
- Two recaps with one week start are one recap: the first one. The board,
  the merged timeline and the recap job read the first one from the documents
  too. Crew history from the documents shows the one written last
  (`TestTwoRecapsForOneWeek`).
- The guest feed counts clips by row above 250 clips.
- Clips of one time whose event ids are not in the order of their writes
  change places. The server makes ids in that order.

### admin_media_orphans (step 13)

`http_key` RPC. Two modes. A delete cannot be undone, so the scan never
deletes.

Scan. Request `{max_objects?, clips_after?, snapshots_after?}`.

- Lists `crews/` in the clips bucket and `snapshots/` in the snapshots bucket.
- An object is an orphan when its crew is a group on this install, no clip
  (by `clip_id`) and no stream session (by `session_id`) of the crew points at
  it, and it is older than 24 hours.
- A crew holds a clip or a session when the legacy document, the ledger
  document, the live stream record or a store row has it.
- Reports for each prefix: `scanned, orphans, bytes, too_new, unknown,
  unknown_crew, read_failed, sample, incomplete, next_after`.
- `unknown_crew`: the crew id is not a group on this install. Another install
  can share the bucket. Such an object is never an orphan here.
- `too_new` also counts every snapshot of a session that has a snapshot
  younger than 24 hours. `StopStreamRPC` and `cleanupStreamIfHost` delete the
  stream record before they write the stream session, so for some seconds
  nothing holds the session id. The streaming code is not changed.
- A key that does not follow the layout is `unknown`. A crew that cannot be
  read is `read_failed`. Neither is an orphan.
- `sample` names the orphans of the call, up to 500.
- `incomplete` is true when the list was cut at `max_objects` or the time
  budget (2 minutes) ended. `next_after` is then the last key that was
  judged. An object that was not judged is not counted.

Delete. Request `{delete: true, clip_keys?, snapshot_keys?}`.

- Needs the env `MELLO_MEDIA_ORPHAN_DELETE=1`, the durable store, and at
  least one key. `delete` with no keys is an error. Keys with no `delete` are
  an error.
- Hard cap: 500 keys in one call, both lists together. One key more deletes
  nothing.
- Each key is judged again as an orphan, with a new read for that key: the
  object in the bucket, the group and the documents of the crew, the store
  row, and for a snapshot the other snapshots of its session. Nothing that
  was read for an earlier key is used again.
- Answer: `deleted_clips` and `deleted_snapshots`, each `{deleted: [key],
  refused: [{key, reason}]}`. Every key of the request is in one of the two
  lists.
- Reasons: `unknown_key, unknown_crew, read_failed, held, not_found, too_new,
  session_active, delete_failed, not_configured`.
- The delete was run only against a local fake in tests.

## 7. Deletion rules (step 7)

Built, and changed after a review. Code: `ledger_scrub.go` (pure rules),
`ledger_user_scrub.go` (account delete), `ledger_purge.go` (crew delete),
`crews.go` (kick).

Events used to vanish after 7 days. Now they stay, so these rules ship in
phase 1. They hold on both sides (store rows and legacy documents) in every
phase: before the cutover, after it, and under both kill switches.

| Case | Rule |
|---|---|
| Member leaves | `member_left` event. Earlier events stay |
| Member kicked | The same `member_left` event (`recordMemberLeft`). Earlier events stay |
| Account deleted | The events the user started are deleted, with their media. The user leaves the events of others, the stored recaps and the head2head holders |
| Crew deleted | Everything of the crew is deleted: rows, documents, media |

### Kick

- `KickCrewMemberRPC` calls `recordMemberLeft`. `GroupUsersKick` of the
  runtime runs no hook.
- A kick through the group API of Nakama has two hooks.
  `BeforeKickCrewMembers` notes which ids of the request are members now
  (state 0 to 2), in process memory, for one minute. `AfterKickCrewMembers`
  writes `member_left` and cleans voice only for an id that was noted and is
  no member now. Nakama does not kick every id of a request.

### Scrub rules (pure functions)

`scrubEventForUser(ev, userID) (out, drop)`:

- Drop the event when the user is the actor or the streamer.
- Otherwise remove the user from `participant_ids`, `player_ids`,
  `viewer_ids` and clip `participants`, and the entry at the same place from
  `participant_names`, `player_names` and `player_overlap_min`.
- Clear `invited_by` and `invited_by_name`.
- Drop a `voice_session` left with fewer than two participants.
- A list that is not aligned with its id list: a name list loses the entries
  that are a name of the user. When none is, it is emptied. Another list is
  removed. `post_clip` skips a participant with no name, so clip lists can
  differ in length.

`scrubClipForUser(clip, who) (out, drop)`: drop a clip of the user. In a clip
of another member, remove the user from `participants` and
`participant_names`. The clip and its media stay.

`scrubStreamSessionForUser(session, who) (out, drop)`: drop a session of the
user. The copy names no other user.

`scrubRecapForUser(recap, who) (out, changed)`: a recap is never dropped.

| Recap field | Action |
|---|---|
| `top_members`, `game_records`, `belts_held`, `leaderboard` | The entry of the user is removed, by `user_id`, or by name when the entry has no id |
| `most_clipped`, `most_clipped_user_id`, `most_clipped_count` | Cleared, all three |
| `most_active` | Cleared when it is a name of the user |
| `best_streak_name`, `best_streak` | Cleared, both |
| `awards` | An award with the name of the user is removed. A belt award loses a detail that names the user |
| `belts_moved` | A move to the user is removed. A move from the user loses `previous_holder_user_id` and `previous_holder_name` |
| `clip_of_week` | Removed when it is a dropped clip of the user (by `clip_id`) or has the clipper name of the user. Otherwise the user leaves `participant_names` |
| `longest_session` | The text is cleared when it holds a name of the user |
| Totals, `top_game`, `games_played` | Stay. They name nobody |

Names (`scrubWho`):

- The names of the user are the display name and the username the account
  had at the delete. A recap adds the name it holds beside the user id.
- A name the user had before is not used. Nothing says it is this user.
- A name is matched whole. In a free text (`longest_session`, an award
  detail) the text before and after it is not a letter or a digit.
- A name that another member of the crew has now (`Taken`, no case) is not
  matched in a field with no id. The field stays, and the log says so without
  the name.
- A name field can hold the user id (`resolveOrID`). It is matched too.

`scrubHead2headForUser(doc, userID) (out, changed)`: the weekly recap makes its
belts from `crew_head2head`, so a recap after the scrub would name the user.

| Holders field | Action |
|---|---|
| A title the user holds | Removed from the document. The title is vacant. The next recompute gives it as a first award, with no previous holder, so nothing says it moved |
| A title taken from the user | Loses `previous_holder_user_id`. The time of the move stays |
| `members`, `pairs` | The user and every pair of the user are removed |

### Account delete

The client calls `DELETE /v2/account` of Nakama. Two hooks, nakama-common
v1.31.0:

| Hook | Does |
|---|---|
| `RegisterBeforeDeleteAccount(BeforeDeleteAccount)` | Writes the scrub record: the names and the crews of the user. After the delete neither can be read. Deletes nothing. Always returns nil |
| `RegisterAfterDeleteAccount(AfterDeleteAccount)` | Marks the record and starts the scrub in a goroutine. Always returns nil |

Scrub record: `ledger_user_scrub/{user_id}`, system-owned, no client read.
`{names, crews, discovered, scan_step, scan_cursor, media, skipped,
started_ms, deleted_ms, done_ms, second_run, store_pending, last_error}`.

- Every write and the delete of the record carry its storage version. A run
  on an old copy cannot write. It stops and writes nothing more: the record
  in storage is the truth, and the job continues from it.
- One scrub of a user runs at a time in a process (`userScrubRunning`). A
  second one answers `busy` and does not touch the record.
- Each delete attempt sets `started_ms` again.

One run (`runUserScrub`), with one deadline for the whole run (2 minutes):

1. Find the crews: the crews of the record, `UserCrews` of the store, and a
   scan for the user id as text in the four legacy collections and in
   `crew_head2head`. The scan saves its cursor in the record.
2. For each crew (`scrubCrewForUser`), in this order:
   1. Read the documents. Save the media of what goes.
   2. Scrub `crew_events`, `crew_clips`, `crew_stream_sessions`. Each write
      uses the version it read, and reads again when it loses.
   3. Scrub the store rows that name the user, 200 at a time
      (`NamingUserInCrew`). A dropped row: `Delete`, which remembers the id.
      A changed row: `UpdateData`, then `ForgetUser`.
   4. Scrub every recap of the crew: the document, then the rows.
   5. Scrub `crew_head2head`.
   6. Remove the crew from the record.
3. Delete the media of the dropped clips and streams.
4. Set `done_ms`. The names leave the record.

- The documents come first in every phase. Before the cutover the reconcile
  copies the documents to the store, so a scrubbed document cannot bring the
  user back.
- The scrub never purges a crew. A crew that is no longer a group is scrubbed
  as any other crew, and the log says so.
- A crew that fails stays in the record. The other crews still run.
- With no store loaded the run scrubs the documents and stays pending
  (`store_pending`). It is not done. The first run with a store looks for the
  crews again and scrubs the rows. An install whose schema can never be made
  is not told apart from one that waits: both stay pending and are logged.
- Second run: 24 hours after `done_ms` the deletion job runs the scrub again
  (a voice session that ends after the delete names the user). It finds the
  user by id, so it needs no name. Then the record is deleted.
- A record of an account that still exists 1 hour after `started_ms` is
  deleted. The account delete did not happen.

Media of the user (rule 5):

- A clip: the two exact keys of `gameClipKeys`. A stream: the prefix
  `snapshots/{crew_id}/{session_id}/`.
- An entry stays in `media` until its delete is complete. It stays when a
  delete failed, when the list of a prefix was cut at the deadline, and when
  the bucket is not configured. The scrub is then not done.
- Not deleted, and kept in `skipped` with the reason, in the result of every
  run and in the log: an id that does not match `[A-Za-z0-9_-]{1,64}`, a
  stream session with no session id (no key can be made), and media that the
  crew still holds under the same `clip_id` or session id.

`admin_scrub_user {user_id}`: `http_key` only. Refuses while the account
exists. Runs one run and returns `{done, incomplete, busy, store_pending,
crews_scrubbed, crews_left, dropped, changed, media_deleted, media_pending,
media_skipped, errors}`. Call it again while `done` is false. A call on a
scrub with no work left looks for the crews again.

### Crew delete

`purgeCrewData`, started by `DeleteCrewRPC` and by
`RegisterAfterDeleteGroup(AfterDeleteCrew)`.

Purge record: `ledger_crew_purge/{crew_id}`, system-owned. `{started_ms,
done_ms, settled_ms, store_pending, last_error}`. It is kept for good.

- It is written before anything is deleted. With no record nothing is
  deleted.
- It is the proof that the crew was a crew of this install. A bucket can be
  shared, and `admin_media_orphans` never deletes under a crew id that is not
  a group (`unknown_crew`). So the purge deletes the media itself.
- The crew id must be a UUID. It is part of object prefixes.
- With no store loaded the purge removes the documents and the media and
  stays pending (`store_pending`).

A record is an order to delete, so three places read the group first:

| Place | The group exists | The group read fails |
|---|---|---|
| `startCrewPurge` | No record is written | No record is written |
| `purgeCrewData` (the job, the RPC) | Nothing is deleted. The record is deleted | Nothing is deleted. The record stays |
| The reconcile (`dropPurgedDoc`) | The record is deleted. The document is reconciled as any other | Nothing is deleted or imported. The document waits |

- The reconcile does not import a document of a purged crew. It deletes the
  document and the rows of the crew.

What a purge removes:

| Kind | What |
|---|---|
| Store | `DeleteCrew`: the event rows, the user index, the week rollup, the anchor, the memory of deleted ids |
| Ledger documents | `crew_events`, `crew_clips`, `crew_stream_sessions`, `crew_recaps` |
| Other documents | `crew_quests`, `crew_profile`, `crew_emoji`, `crew_avatars`, `voice_channels`, `crew_head2head` (`holders_{crew_id}`), `recap_pushes` (`{crew_id}`, `belt_{crew_id}`, `join_{crew_id}`), `stream_meta`, and `active_streams` of the streamer that `stream_meta` names |
| Clips bucket | `crews/{crew_id}/`: voice clips, game clip videos and posters |
| Snapshots bucket | `snapshots/{crew_id}/` |
| Media bucket | `crew-banners/{crew_id}/`, `crew-emoji/{crew_id}/` |

Not removed:

| What | Why |
|---|---|
| `crew_last_seen`, `chat_read` | Owned by each member, key crew id. They hold a time. Nakama deletes them with the account |
| `invite_codes` | The key is the code. Nothing lists the codes of a crew |
| `chat_reactions`, `thumbnails`, `media_reports` | Keyed by a message, a stream or a report |
| Chat messages, chat images (`{crew_id}/` in the media bucket) | Not part of the ledger. Open |

- Each part runs also when another part failed. `done_ms` is set when one run
  removed everything with no error.
- Media is deleted in batches of 1000 keys. A run is cut after 2 minutes and
  the next run continues.
- Second run: one hour after `done_ms` (the life of a presigned upload URL).
  A late writer can write for the crew after the group is gone.

`admin_purge_crew {crew_id}`: `http_key` only. Refuses while the group exists
(code 9). Refuses a crew id this install holds nothing of (code 5): no
record, no document, no row. Returns `{done, incomplete, store_pending,
media_deleted, docs_deleted, store_deleted, media_skipped, errors}`.

### Deletion job

`StartLedgerDeletionJob`, at start and every 5 minutes: the scrubs and the
purges that are not done, and their second runs. Two nodes can run the same
one. Every step can run twice.

- The job starts after the record it handled last (`deletionJobCursor`). A
  record that takes the whole budget, or fails every time, does not keep the
  job from the records behind it.
- The purge records are kept for good, and the reconcile and the job both
  need them every 5 minutes. A node keeps them in memory. Every write of a
  record writes a new token to `ledger_migration/purge_token`. A node reads
  the token (one storage read) and lists the records again only when the
  token changed or its copy is one hour old.
- The scrub records are listed on every run. They are few: a record is
  deleted when its scrub is over.

### The reconcile

| Case | Before the cutover | After the cutover |
|---|---|---|
| Entry the scrub dropped | Gone from the document and the store | The same |
| Entry the scrub changed | The same content on both sides | The same |
| A document holds a dropped entry again | Not stored: `Backfill` and `Put` skip a deleted id. `putMissingHistoryRows` asks `DeletedIDs` before `PutUnique` | The same |
| A document holds the user again in an entry that stays | The store takes the document. Run `admin_scrub_user` | The store keeps its content |
| Document of a purged crew whose group is gone | Deleted, not imported | The same |

### Open decision

Nakama 3.21.0 deletes a group when its only owner deletes the account. This
was seen on a local stack. No delete-group hook runs, so nothing purges the
crew: its documents stay. The scrub removes only the user. Whether such a
crew is removed or handed over is not decided. `admin_purge_crew` removes it
by hand.

### Known limits

- A recap field with only a name is matched by the names at the delete. A
  name the user had before stays in a recap that has no id for the user.
- A scrub with no before hook (the Nakama console, `AccountDeleteId`, a lost
  record) has no names. It matches ids, and the name beside the id in the
  same recap. Run `admin_scrub_user` for an account with no hook.
- A field with only a name that another member also has stays.
- When the purge record cannot be written, nothing is purged. The log line is
  `crew_purge_failed`. Run `admin_purge_crew`.
- A write for a purged crew that makes only a store row, more than one hour
  after the purge, stays.
- With no store that ever loads, the records stay pending for good. A scrub
  record then keeps its names.
- The noted members of a kick are in process memory. They hold for the node
  that runs both hooks of one request.
- Outside the ledger, a deleted user can still be named in: `crew_quests`
  (`friend`, `call`, `owner_id`, `plus_granted`), `crew_emoji` (`added_by`),
  `invite_codes` (`inviter_user_id`), `recap_pushes` (a queued push text),
  `media_reports`, chat messages and chat images.

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
  `TestInvitePreviewSeesNoClipRows`.
- Step 7. `delPhases` runs a test before the cutover, after it and under each
  kill switch. The fake of these tests checks storage versions (`delNk`).
  - Rules: `TestScrubEventForUser` (a case for each event type),
    `TestScrubClipForUser`, `TestScrubStreamSessionForUser`,
    `TestScrubRecapForUser` (a case for each recap field and each name rule),
    `TestHoldsWholeName`, `TestScrubHead2headForUser`.
  - Kick: `TestKickWritesMemberLeft`,
    `TestKickHookWritesMemberLeftOnlyForTheKicked`.
  - Account: `TestScrubUserFromLedger` (all phases, a crew the user left, a
    reconcile after it), `TestScrubUserIsRepairedAfterAFailure`,
    `TestReconcileDoesNotRestoreWhatTheScrubDropped`,
    `TestScrubNeverPurgesACrew`, `TestScrubUserWaitsForTheStore`,
    `TestScrubUserInBatchesAndAfterItsBudget`, `TestAdminScrubUserRefuses`,
    `TestScrubRecordOfAnAccountThatStays`,
    `TestScrubRecordOfARetriedDeleteIsNotCleaned`,
    `TestDeletionJobScrubsWhenTheAfterHookDidNotRun`,
    `TestSecondScrubRunWorksWithoutNames`, `TestScrubUserFromHead2head`,
    `TestScrubKeepsANameThatAnotherMemberHas`.
  - Record and concurrency: `TestStaleScrubRunDoesNotBringBackADeletedRecord`,
    `TestStaleScrubRunDoesNotDropTheMediaOfANewerRecord`,
    `TestScrubRecordDeleteIsVersionChecked`, `TestOneScrubOfAUserAtATime`,
    `TestScrubDocumentWriteLosesToAConcurrentWriter`.
  - Media: `TestScrubKeepsMediaThatAnotherClipHolds`,
    `TestScrubMediaStaysWhileItsBucketIsNotConfigured`,
    `TestScrubMediaIsCutAtTheDeadlineOfTheRun`,
    `TestScrubReportsMediaItDoesNotDelete`.
  - Crew: `TestPurgeCrewData` (all phases, a reconcile after it),
    `TestPurgeRecordOfALiveCrewDeletesNothing` (all phases, with a group read
    that fails), `TestReconcileDoesNotImportADocumentOfAPurgedCrew`,
    `TestAdminPurgeCrew`, `TestPurgeCrewWaitsForTheStore`,
    `TestPurgeCrewContinuesAfterItsBudget`,
    `TestPurgeCrewRunsAgainForALateWriter`, `TestDeleteGroupHookPurgesTheCrew`,
    `TestDeleteObjectsUnderRefusesAWrongPrefix`.
  - Job: `TestRotateAfter`, `TestDeletionJobDoesNotStarveBehindASlowScrub`,
    `TestPurgeRecordsAreListedOnlyWhenTheyChange`.
  - Store: `ledgerScrubConformance` runs in the conformance function, so
    `dev_ledger_selftest` covers `DeletedIDs`, `UserCrews`, `NamingUserInCrew`
    and `ForgetUser` on SQL.
  - Media deletes run only against the loopback S3 fake.
  - The hooks ran once end to end on a local stack: two test accounts, a
    crew, an account delete, a crew delete.
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
  `dev_ledger_selftest` covers `Put`, `PutUnique`, `ByRef`, `ByIDs`, `ByRefs`,
  `RefsBefore` and `TopInRange` on SQL.
- After the review of phase 1b:
  - One row for a `clip_id`: `TestClipIDHasOneRowWhenTwoNodesDisagreeAboutTheMarker`,
    `TestKillSwitchOnACrewOverTheCapKeepsOneRowForAClipID`.
  - Repair under a kill switch: `TestReconcileRepairsContentUnderTheKillSwitch`.
  - Two rows of one session: `TestZonesFoldTwoRowsOfOneSessionBeforeTheWeeksAreCounted`.
  - No event id: `TestClipWithNoEventIDIsImported`,
    `TestStreamSessionWithNoEventIDIsImported`, `TestEntryWithNoIDBlocksTheHistoryMarker`.
  - Recap time: `TestRecapRowIsDatedWhenItWasGenerated`.
  - Cost: `TestCrewFeedLoadQueryCount`, `TestZonesReadOnePageOfALargeCrew`,
    `TestCatchupOfANewMemberReadsTheNewestClips`. `queryCounter` names every
    method of the store, so a new method cannot pass it without a count.
  - Content: `TestZonesContentAtTheOldCapsIsTheSameFromTheStore` (with and
    without the merged timeline), `TestZonesWithClipsOfOneTime`,
    `TestReadersFollowTheTruthWhenDocumentAndStoreDisagree`,
    `TestTwoRecapsForOneWeek`, `TestKillSwitchOnACrewOverTheCapShowsTheDocument`.
  - `TestAdminMediaOrphans*`: the scan deletes nothing, the session with a new
    snapshot, `unknown_crew`, the delete by keys with its reasons, the new
    read for each key, the cap, `next_after` at the end of the time budget.
- Two tests that pinned what the review changed were edited:
  `TestClipReconcileKeepsTwoEntriesOfOneClipID` (a clip with no event id is
  now a row) and `TestRecapWriteOrder` (the row time is `generated_at`). The
  characterisation files did not change.

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
7. Deletion rules (section 7). Built. `go test -run 'TestScrub|TestSecondScrub|TestStaleScrub|TestOneScrub|TestHoldsWholeName|TestPurge|TestAdminPurge|TestAdminScrub|TestKick|TestReconcileDoesNot|TestDeletionJob|TestDeleteGroupHook|TestDeleteObjectsUnder|TestRotateAfter' .`
8. Specs 16 and 04, `TESTING.md`.

Phase 1b, as built:

9. Stream sessions. The reconcile imports `crew_stream_sessions`. The feed
   reads the rows. Own marker for 1b. `go test -run 'TestHistory|TestStreamSession|TestLedgerDocument|TestChar' .`
10. Clips as rows (`type clip`, `ref = clip_id`), dual write, import, no cap in the store. `go test -run 'TestClip|TestConcurrent|TestChar' .`
11. Recaps as rows (`id = recap_{week_start}`), dual write, import. `go test -run 'TestRecap|TestChar' .`
12. `crew_feed` pages from the store. Output shape unchanged. `go test -run 'TestZones|TestChar' .`
13. `admin_media_orphans` with a dry run. `go test -run 'TestAdminMediaOrphans|TestMediaObject' .`
14. Specs CLIPS, CREW-FEED-ZONES and 04 (the `admin_media_orphans` row). Open.

Open after the review of phase 1b:

- `MELLO_LEDGER_READ=doc` does not make the reconcile of phase 1a take the
  content of the ledger document again. Only phase 1b follows the truth.
- A clip whose `clip_id` the store holds under another row (`ref_held`) is
  in the document and not in the store. After the cutover the readers have
  the row of the store.

Different from the first plan:

- `UpsertStreamSession` and `capClips` stay until the legacy documents go.
  The documents must be written while they are the truth and for a rollback.
- The clip row id is `clip_{event_id}`, not the bare event id.
