# YOU-STRIP

> **Component:** Crew feed (client `panels/you_strip.slint`, `panels/you_view.slint`,
> `head2head/chase.rs`, `handlers/stats.rs`)
> **Status:** built
> **Related:** `19-FEED-CURATION-PERSONAL-STATS.md` (section 2.3, the game
> record), `HEAD2HEAD.md` (titles, pairs, the feed payload), `CREW-FEED-ZONES.md`
> (the zones under the strip), `CREW-QUESTS.md` (the quests the strip waits for)

The You strip is pinned above the zones of the crew feed. Only the viewer sees
it. It shows the viewer, the belts the viewer holds, up to two chase bars and
the game record. A tap opens the You view.

The client builds everything from the `head2head` payload of the zones
(`titles`, `own_values`, `pairs`) and from `user_game_stats_get`. The server
has no strip logic.

## 1. The strip

One row, 76 px, the card shape (one cut corner, top right), lined up with the
zone cards (20 px from the panel edge). Left to right:

| Part | Content |
|---|---|
| You | The octagon (44 px), the name, the icons of the titles the viewer holds. With no title, the label `0 BELTS` |
| Bars | Up to two (section 1.2) |
| Record | The game icon, the streak and the win rate, the form pips. A game with no record shows the week's hours and `THIS WEEK` |
| Chevron | The tap target. The whole strip is one button, label `You` |

A 1 px keyline separates the parts.

A form pip is a disc: a win is white, a draw is dim, a loss is a hollow ring.
A loss is not red. Red is state.

### 1.1 When the strip shows

The strip shows (`YouStripData.ready`) only when all three are true:

1. The crew's quests are done: every one of the five steps (`CREW-QUESTS.md`).
   A crew with no `quests` in the zones (made before quests, or past the 14
   days) has nothing to wait for.
2. The viewer shares game activity ("Show crew what I play", spec 22 section 5).
3. Another member shares game activity: their pair with the viewer holds a
   game title. Mouth, Clipper and Winner do not count, because voice minutes,
   clips and win moments count with sharing off. A pair holds only the titles
   both members contend for.

The server does not know the sharing setting. The client reads the third rule
from the pairs, and the second from its own settings. Turning sharing off hides
the strip at once.

| State | Strip |
|---|---|
| A belt to take and a belt to hold | TAKE, then HOLD |
| No belt held | Two TAKE |
| Nothing to take | Two HOLD, the thinnest lead first |
| One candidate in all | One bar takes both slots |
| A tie on a bar | The delta reads `±0`. The holder keeps the belt (`HEAD2HEAD.md` section 2) |
| Nothing in reach | The viewer and the record |
| No game tracked | The record and its keyline go. The bars share the width |
| A rule of 1.1 fails | No strip |

### 1.2 The bars

Each bar has a label (`TAKE` or `HOLD`), the title icon and name, the other
member's octagon (20 px) and the delta. Under it the track, and under the track
`You <value>` and `<Name> <value>`.

- TAKE: a belt another member holds. The fill is white to the ratio, the
  holder's mark (their identity hue) at the end.
- HOLD: a belt the viewer holds. The fill is full, the runner-up's mark at the
  ratio.
- A pointer on a bar shows the stat sentence of the title (`sealed::stat_line`).

## 2. The chase

`client/src/head2head/chase.rs` builds the bars.

| Item | Rule |
|---|---|
| TAKE candidate | A contested title whose holder is another member, where the viewer has a value in `own_values` |
| HOLD candidate | A contested title the viewer holds. The runner-up is the member with the best value against the viewer in the viewer's `pairs` |
| Gap | `abs(you - other) / max(you, other)`, the gap the pair rows use for their deciding title. A clock title counts minutes from 18:00. Has a Life goes to the fewest hours |
| Ratio | `1 - gap` |
| Pick | The TAKE with the smallest gap and the HOLD with the smallest gap. With no HOLD, the two smallest TAKE. With no TAKE, the two smallest HOLD. Equal gaps go to the display order of the titles |
| Delta | Minus behind, plus a lead, `±0` when the gap shows as zero. Hours below 1 read in minutes. A clock gap reads in minutes up to an hour, then in hours. `min` and `%` keep their unit. A count is a bare number |

## 3. The You view

A modal, 640 px wide, the card shape, on whole pixels. The head: the octagon
(64 px), the name, `<n> BELTS · <CREW>`, Close. Then three tabs. The body
scrolls under the head and the tabs.

| Tab | Content |
|---|---|
| Head2head | The viewer against every member with a contested title, closest score first, no cap. Each row: the octagon, the name, the score, the split of the titles (white for the viewer, the member's hue for them), the deciding title with both values |
| Belts `<n>` | `HOLD` and `TAKE`, every candidate of section 2, smallest gap first, with the bar of the strip |
| Games `<n>` | Every tracked game: the form, the streak, the win rate and the record, or the week's hours for a game with no record |

- The strip opens the view on Belts. The view keeps no tab between opens.
- An open fetches `user_game_stats_get` again.
- The deciding title of a pair follows `HEAD2HEAD.md` section 5, over every
  contested title, session titles too.

## 4. Data

| Field | Set by |
|---|---|
| The record half of `YouStripData` | `UserGameStatsLoaded` (`handlers/stats.rs`) |
| `viewer`, `held-icons`, `bars`, `ready` | Every feed load of the open crew (`handlers/zones.rs`, `stats::set_chase`) |
| `you-view-pairs`, `you-view-holds`, `you-view-takes` | Every feed load (`stats::set_you_view`) |
| `you-view-games` | `UserGameStatsLoaded` |

Each half keeps the other: a stats load keeps the bars, a feed load keeps the
record.

## 5. Tests

- Unit: `head2head/chase.rs` (each rule of section 2), `head2head.rs`
  (`view_pairs`).
- Harness: `client/src/flow_tests/you_strip.rs`.
- Previews: `you_strip.slint` (every state of 1.1), `you_view.slint`
  (`PreviewHead2head`, `PreviewBelts`, `PreviewGames`, `PreviewBigCrew`), and the
  strip in `PreviewFeedSmall`.
- Journey: `qa/journeys/feed-you-strip.ts` (flow FEED-14).
- The e2e state port reports `you_strip` (`ready`, `held`, `bars`) and
  `you_view` (`open`, `tab`, `pairs`, `holds`, `takes`, `games`).

## 6. Out of scope

- iOS has no strip.
- Pinning a game on the strip.
- A push when a bar closes in.
- A server-side sharing flag. A member who turned sharing off keeps the game
  numbers shared before; lifetime titles keep them.
