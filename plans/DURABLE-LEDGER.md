# Durable crew ledger

Status: phase 1 is merged. Phase 2 is built on `feat/history-sessions`. This
document holds only what is still a plan.

## 1. What is built

Phase 1 is in the specs. The code is in `backend/nakama/data/modules/`.

| Topic | Spec |
|---|---|
| Store, type sets, rows, the 7-day view | `specs/16-CREW-EVENT-LEDGER.md` section 3.7 |
| Dual write, the two cutovers, kill switches | Spec 16 section 3.8 |
| Reconcile | Spec 16 section 3.9 |
| Self-hosting, log lines, env variables | Spec 16 section 3.10 |
| Deletion rules, the handover of a crew, the chat of a deleted crew, leftovers | Spec 16 section 10 |
| Modules, collections, hooks, admin RPCs | `specs/04-BACKEND.md` sections 3, 4, 4.5, 4.6, 6 |
| Clips with no cap | `specs/features/CLIPS.md` section 6.2 |
| Crew history from the store | `specs/features/CREW-FEED-ZONES.md` section 7 |
| Game sessions in Crew history (phase 2) | CREW-FEED-ZONES.md sections 7 and 8, spec 16 section 3.8 |
| Conformance run, `dev_ledger_selftest` | `TESTING.md`, "Crew ledger store" |

## 2. Decisions that hold for the next phases

- Every event is kept. A plan never causes a delete. The gate is on read.
- m3llo+ is per user, never per crew.
- Counted events: `clip`, `stream_session`, `game_session`, `weekly_recap`
  (`ledgerCountedTypes`). Voice sessions, joins and chat activity are stored
  but do not count.

## 3. Phases

| Phase | What | Visible change | State |
|---|---|---|---|
| 1a | Ledger events in the durable store, dual write, reconcile, readers moved, deletion rules | None | Built |
| 1b | Clips, stream sessions and recaps in the same store, caps removed | A crew over an old cap sees all its clips and streams | Built |
| 2 | Game sessions in Crew history | Yes | Built, iOS open |
| 3 | The free rule, as an admin setting | Yes | Plan |
| 4 | m3llo+ made real: admin grant, then purchase | Yes | Plan |

### Phase 2: game sessions in Crew history

Built: the server and the desktop client. The rules are in
CREW-FEED-ZONES.md section 7.

- A voice session is not in Crew history. It is not a counted event
  (section 2). The first plan of this phase had it.
- One `crew_feed` call reads the store 15 times with m3llo+ and at most 20
  times without it. Before phase 2 it was 14 and 19.

Still a plan:

- iOS (`mello-ios`). It shows the game sessions of an open week now: they are
  entries of `items`. It does not read `session_count`. A locked week of
  game sessions only shows on iOS with no count.
- A locked week has no teaser for a game session. A teaser costs one read
  more for each locked week. Not decided.
- The Sessions zone reads the 7-day view. With `free_days` above 7 a game
  session between 7 days and the free window is in no zone. Phase 3 changes
  the free window, so it must read the sessions of the window from the store.

### Phase 3: the free rule

- A member without m3llo+ sees the last N counted events or the last D days,
  whichever reaches further back.
- Start values: N = 30, D = 14. Both are in the admin config
  (`feed_config/config`).
- Not built. The free window that ships is `free_days`
  (CREW-FEED-ZONES.md section 7).
- The store has the queries: `NewestN` for the Nth counted event, `CountOlder`
  from the week rollup.

| Query | How | Cost |
|---|---|---|
| Newest N counted | One `ORDER BY ts DESC LIMIT N` with `type IN (...)` | N rows |
| Count older than t | Sum of the week rollup for whole weeks, index count for the partial week | No event rows |

### Phase 4: m3llo+ made real

- First an admin grant for a user. Then a purchase flow.
- Until then every m3llo+ action opens the "coming soon" modal.

## 4. Retirement of the legacy documents

One release after both cutovers are done on every install we support:

- Remove the dual write (`ledgerWriteBoth`, `historyWriteBoth`) and the
  document writers (`appendLedgerDoc`, `appendClipDoc`, `capClips`,
  `appendRecapDoc`, `UpsertStreamSession`).
- Remove the reconcile, the markers and the kill switches
  (`MELLO_LEDGER_READ`, `MELLO_LEDGER_HISTORY_READ`, `MELLO_LEDGER_SOAK_SECONDS`).
- Remove the document fallback of the readers.
- Delete the documents `crew_events`, `crew_clips`, `crew_stream_sessions`,
  `crew_recaps`. The scrub and the purge then work on rows only.

## 5. Open decisions

- Chat of a deleted account (spec 16 section 10.6). Nakama deletes the
  messages of the user, and the scrub deletes the chat images of the user.
  The reactions of the user and the mentions of the user in other messages
  stay. No rule yet.
- The members of a crew are not told when the owner changes after a
  handover (spec 16 section 10.2). A client reads the role at the next read
  of the crew state. Tell them, or not: not decided.
- The account delete can now be refused (a handover that fails). The
  clients have no delete in the UI. When one is built it must show the text
  of the server and let the user try again.
- A crew with no owner and no member (two owners deleted at once, no one
  else in the crew) keeps its group and its data. Delete it, or not: not
  decided.
- `MELLO_LEDGER_READ=doc` does not make the reconcile of phase 1a take the
  content of the ledger document again. Only phase 1b follows the truth. Fix
  it, or let it go with the legacy documents.
- A clip whose `clip_id` the store holds under another row (`ref_held`) is in
  the document and not in the store. Decide if the reconcile must repair it.
- The delete of `admin_media_orphans` and the media deletes of the scrub and
  the purge ran only against a local S3 fake.
- A self-hoster who skips the release with the dual write gets no import
  after the retirement. How the retirement release handles that: not decided.
- The code comments point to sections of this plan that are now in the
  specs. Update them with the next code change.
