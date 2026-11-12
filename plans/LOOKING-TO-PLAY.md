# Looking to play

> Status: direction and mockup approved by Bob, 12 Nov 2026. Nothing built.
> Branch: `feat/looking-to-play` (worktree `mello-wt-looking-to-play`). The iOS app and the
> push Worker get a branch of the same name in their own repos.
> Mockup: `designs/looking-to-play.html` (phase 0, approved 12 Nov 2026).
> Specs to write or update after it ships: `specs/features/LOOKING-TO-PLAY.md` (new),
> `specs/features/TEXT-CHAT.md` (message push, the `play_check` system event),
> `specs/11-PRESENCE-CREW-STATE.md` (notification code 118), `specs/features/NOTIFICATION-SOUNDS.md`,
> and the push spec (spec 23, not in this repo).

## 1. The problem

A small crew asks "up for a game?" on Messenger, Discord or SMS. They do it there because
that question reaches the phone. In m3llo a plain crew message reaches nobody who has the
app closed: only a mention sends a phone push (`push_remote.go`, `queueMentionPushes`).

The question has the same faults in every chat app. The answers are scattered, nobody says
"it's on", and nobody says "no one this time", so the asker keeps checking the phone.

The asker is usually on the phone and wants to know one thing: is it worth going to the PC.

## 2. What we build

Two things, in this order.

1. **Plain message push.** A crew message sends a phone push, with a mute for each crew.
   This makes crew chat usable from the phone.
2. **The check.** A button in the chat composer posts a check to the crew. Members answer
   In, In 15, In 30 or Not now, on the card, on a toast, or on the phone push. The asker gets
   a push when someone is in, and a push when nobody is.

Not in scope: DMs and group chats, checks for a later time, a share link for members with
no app, m3llo asking by itself, belts for checks, the HUD toast (the HUD is off:
`HUD_AVAILABLE = false`), Android (the check is platform neutral, Android gets it with its
client).

## 3. The check

### 3.1 Rules

- A check asks every member of the crew, or the members that the asker picks. Only the
  asked members get the push, the toast and the sound. The card is in crew chat for every
  member, and any member can answer on it.
- A member is the asker of at most one open check.
- A crew can have several open checks. Of the checks that ask everyone, at most one is
  open for each game, and one with no game. Asking everyone for a game that has such a
  check counts as answering In on it.
- The asker counts as in.
- The answers are `in`, `in_15`, `in_30` and `no`. A member can change the answer while the
  check is open or on. `in_15` and `in_30` store the time the member is due.
- A member who is in or due and joins a voice channel of the crew is marked as arrived.
- A member with no answer who joins a voice channel of the crew is set to `in`, when the
  crew has exactly one open check.
- The asker can cancel.
- A member can ask in a crew once in 10 minutes. This holds after a cancel too, so a
  member cannot ask and cancel to send pushes and toasts again and again.
- An asker whose check closed with nobody in waits 30 minutes before the next ask.

### 3.2 States

| State | Means | Leaves when |
|---|---|---|
| `open` | Asked, nobody else is in yet | A member answers `in`, `in_15` or `in_30`: goes to `on`. Every asked member answered `no`, or 30 minutes passed, or the asker cancelled: goes to `closed` |
| `on` | At least one other member is in | 60 minutes after the ask, or the asker cancels: goes to `closed` |
| `closed` | Over. The card keeps its last state in chat | Never |

A closed check has a reason: `nobody`, `all_no`, `cancelled` or `done`. In a large crew
not every member answers, so the 30 minutes close the check.

### 3.3 Backend (`backend/nakama/data/modules/play_check.go`, new)

The check follows the reactions pattern (`chat_reactions.go`): one stored state for each
check, changed through an RPC with a version check, and the whole new state sent to every
crew member as a notification.

- **State**: collection `play_checks`, key `{check_id}`, owned by the system user.
  Fields: `check_id`, `crew_id`, `message_id`, `asker_id`, `asked` (the picked user ids,
  empty when the check asks everyone), `game` (`igdb_id`, `name`,
  optional), `opened_at`, `state`, `closed_reason`, `answers` (user id to `answer`,
  `at`, `due_at`, `arrived_at`).
- **Open checks of a crew**: collection `play_check_open`, key `{crew_id}`, value the
  list of open `check_id`s. An in-memory index is built from it at start. A loop closes checks on time,
  like `StartMessageThrottleLoop`. This is state on one node, the same as voice state.
- **The chat message**: the backend posts it with `SendSystemMessage`, event `play_check`,
  body "Leo is looking to play" or "Leo is looking to play Counter-Strike 2", data
  `check_id`, `asker_id`, `game`. A client that does not know the event shows the body as
  a system line, so no build shows nothing. `chat_validation.go` needs no new type.
- **RPCs**
  - `play_ask {crew_id, game?, member_ids?}`: makes the check and posts the message.
    Returns the state. No `member_ids` asks everyone.
  - `play_answer {check_id, answer}`: stores the answer. Returns the state.
  - `play_cancel {check_id}`: the asker only.
  - `play_checks {crew_id, check_ids[]}`: the states for the checks in loaded history,
    and the open checks of the crew. Called on history load, like `chat_reactions`.
- **Notification 118 `NotifyPlayCheck`**: the full state, to every crew member, on each
  change. A client holds one chat channel at a time, so this is also how a member who has
  another crew open learns of a new check.
- **Recent picks**: the last 3 groups that a member picked in a crew, stored for the
  member, so the phone and the desktop offer the same groups.
- **Voice**: the voice join path calls into `play_check.go` to set `in` and `arrived_at`.
- **Ledger**: on close, append a `play_check` crew event (asker, who was in, game,
  reason). No feed or recap consumer in this plan. It is there so belts and a Crew history
  line can come later.

### 3.4 Phone pushes of a check

| Push | To | When | Body | Buttons |
|---|---|---|---|---|
| Ask | The asked members. For a check that asks everyone and has a game in a large crew, see 3.8 | At the ask | "Leo is looking to play" | In, In 15, In 30, Not now |
| Answer | The asker | Each `in`, `in_15`, `in_30` from another member. The first has a sound. Each later one replaces it with no sound | "Mia is in" or "Mia is in, 15 min", then "Mia and Sam are in", then "5 are in" | None |
| Second call | Asked members with no answer, of those who got the Ask | Once, when two members of the check are in a voice channel of the crew together | "Leo and Mia are playing" | In, In 15, In 30, Not now |
| Nobody | The asker | The check closes with reason `nobody` or `all_no` | "No one this time" | None |
| Called off | Members who are in | The asker cancels | "Leo called it off" | None |

- The title is the crew name.
- A member with an active session gets no phone push (`decidePush` returns skip). The
  client shows the toast and plays the sound from notification 118.
- A check never waits for the 120 second desktop grace. An inactive desktop gets the phone
  push at once.
- The mute of section 4 applies. The Answer and Nobody pushes go to the asker anyway.
- All pushes of one check share one collapse id, so each replaces the one before on the
  lock screen. They expire when the check closes, not after 24 hours.
- No quiet hours. A person asks, and the phone's own focus modes apply.
- A push has no cap on members. It goes out in batches of 20 tokens, the limit of the
  push Worker for one request.

### 3.5 mello-core

- `ChatMessage` gets the `event` and `data` of a system message (it drops `data` today,
  `chat_message_from_envelope`).
- A `PlayCheck` type, a map of check states in crew state beside the reactions map.
- Commands `PlayAsk`, `PlayAnswer`, `PlayCancel`, and the `play_checks` load on history load.
- Event `PlayCheckUpdated` from notification 118 and from the RPC replies.
- The iOS app gets all of this through the same core.

### 3.6 Desktop client

- **The pill.** A fourth `PillButton` in the composer after Attach, GIF and Emoji
  (`chat_panel.slint`). It carries a red pip while the crew has an open check.
- **The ask popover.** Inline over the pills, built like `GifPopover`. Top to bottom:
  1. The open checks of the crew. A press goes to the card.
  2. Who to ask. One list of members. Each row shows what the member does now (in voice,
     in a game) and a box to pick the member. "Everyone" is a row of the list. A crew of
     up to 10 members lists every member and starts with Everyone picked. A larger crew
     starts with the last picks of the member. It lists a field to find a member, the
     recent picks as one row for each group, the members who are on now (at most 4), and
     Everyone.
  3. Game chips with art (`GameArt`). The default is no game.
  4. One red button: "Ask the crew", or "Ask 3" when 3 members are picked.

  Escape closes it. The chips use what the client has: `active_games` and the recent game
  tiles. The `crew_recent_games` RPC of spec 17 is not built and is not part of this plan.
- **The card.** A `play_check` system message draws as a `CutCard` in the feed, not as a
  system line. A check that asks 10 members or fewer has one row for each asked member:
  octagon avatar, name, state. States: in voice, in, the minutes left (`tabular-nums`,
  counted by the client from `due_at`), no answer, not now. A member who was not asked and
  answers gets a row too. A check that asks more than 10 members has rows only for members
  who are in or due, at most 6, then "8 more in", which opens the full list. On that card,
  not now is a count with no names, and a member with no answer is not shown. The order is
  in voice, in, due with the soonest first, no answer, not now.
  A picked member sees the check as a mention: the unread badge of the crew uses the
  mention colour.
  A member who has not answered sees the four buttons. In is the red button. The asker
  sees Cancel. A closed card shows who was in, or "No one this time".
  `ChatMessageData` and `chat_row_same` get the new fields so the row updates in place.
- **The toast.** `PlayCheckToast`, a small card in the top right corner of the window,
  mounted in `main.slint`. It has no scrim, does not take the keyboard focus and does not
  block the window. It shows the crew, the asker, the game, the count of members who are
  in, and the four buttons. It closes after 12 seconds, on its close button, or on an
  answer. It shows once for a check, when the check arrives for any crew of the member,
  the member is not the asker and has not answered, the window is visible, and the card
  is not on screen. One toast shows at a time, and the next check waits. A crew with the
  Looking to play switch off gives no toast and no sound.
- **In on desktop** sends the answer and joins the voice channel that holds the most
  members of the check, or the default channel when none is in voice
  (`Command::JoinVoice`). A member who is in voice already only answers.
- **The sound.** A new `Sound::PlayCheck`, played once for a check, with the toast or the
  OS notification, and at most once in 60 seconds. A second, shorter cue for the asker when someone is in.
- **Window hidden.** An OS notification, "Leo is looking to play". It has no buttons:
  notify-rust drops actions on Windows and macOS. A click brings the window up at the
  card in chat.

### 3.7 iOS app

- **The button.** A second button in the composer beside GIF. It opens a sheet with the
  same content as the desktop popover.
- **The card.** A new row in `ConversationView` for a system message with event
  `play_check`. The conversation list shows the body as the preview.
- **Push buttons.** A notification category with the four actions. The push Worker must
  pass a category (section 5).
- **Answering from the lock screen.** The app has no path for this today. The core starts
  only from `RootView`, the session lives in the Keychain inside the core, and Swift has
  no HTTP path to the backend. The first iOS step is a spike: start the core from the
  notification action with no UI, send `PlayAnswer`, wait for the reply, finish. The spike
  must also show that the session can be read while the phone is locked.
  If the spike fails, the buttons open the app and the answer goes out on launch.
- **Who is in a game.** The phone shows voice only (`CrewLive` has no game field). The ask
  sheet shows voice in this plan. Game presence on the phone is a later step.

### 3.8 Crew size

The check works the same in a crew of 5 and a crew of 50. A large crew has more than 10
members. Two rules depend on it.

| Rule | Up to 10 members | More than 10 members |
|---|---|---|
| Who gets the Ask and the Second call of a check that asks everyone and has a game | Every member | Members who have the game, and members whose games are not known |
| The Messages switch at the start (section 4) | On | Off |

A check that asks everyone and has no game goes to every member at every size. A check
with picked members goes to those members at every size. A member can ask, answer and see
every card in chat with no regard to who got the push.

Rules that hold at every size and keep a large crew usable: picked members and several
open checks (3.1),
the row limits of the card, the popover and the toast (3.6), the one notification that the
asker gets (3.4), and the batches of 20 (3.4). Notification 118 goes to every member on
each change, as reactions do.

## 4. Plain message push

- **Which messages.** `text`, `gif` and `image`. A mentioned member gets the mention push,
  as today. Every other member gets the message push.
- **Who.** Every crew member but the sender who has the Messages switch on. There is no
  cap of 20 as for mention push: the send goes out in batches of 20 tokens.
- **When.** `decidePush`, as for mentions: none while a session is active, after 120
  seconds when only an inactive desktop is connected, at once in every other case.
- **Text.** The title is the crew name. The body is "Leo: the text", "Leo sent a GIF" or
  "Leo sent an image".
- **Bursts.** One notification for each crew on the lock screen. More messages replace it:
  "4 new messages". After the first push a member gets at most one update every 30
  seconds for a crew. It reuses `push_coalesce.go` with its own type and window.
- **Tap.** Opens the crew chat. The app does this today.
- **Mute.** A stored doc for each user with two switches for each crew: Messages and
  Looking to play. Looking to play is on at the start. Messages is on at the start in a
  crew of up to 10 members and off in a larger crew (3.8). Only a choice the member made
  is stored. RPCs to read and set them. The check sits
  before the send in `push_remote.go`. The switches are in crew settings on desktop and
  in the crew sheet on iOS. A mention push ignores the Messages switch.
- **Privacy.** The message text goes through the push Worker and Apple, as a mention does
  today. Nothing new is stored.

## 5. Push Worker

- New types: `message`, `play_check`, `play_result`. It rejects an unknown type today.
- New optional fields that it passes to APNs: `category`, `sound`, a collapse id that is
  not the message id, and a time to live. It sets none of these today.
- The custom data gets `check_id`.

## 6. Phases

Each phase ends with its own tests green. `scripts/check-full.sh` runs once, before the push
of the branch.

| Phase | What | Where | Done when |
|---|---|---|---|
| 0 | Mockup: pill, popover, the card in each state, the toast, the iOS card, sheet and notification | `designs/looking-to-play.html` | Bob approves it |
| 1 | Plain message push and the mute | Backend, Worker, crew settings on desktop and iOS | A message from the desktop reaches a phone with the app closed. A muted crew sends none. Go tests for the rules of section 4 |
| 2 | The check: backend and core | `play_check.go`, mello-core | Go tests for every row of 3.1, 3.2 and 3.4. Core tests for the system event and for a client that does not know it |
| 3 | The check on desktop | Slint, client | slint-viewer previews of each state with mock data, shown to Bob at each checkpoint. One mello-driver journey with two clients on the local stack and the local SFU: ask, answer In, both in voice |
| 4 | The check on iOS | iOS app, Worker | The spike of 3.7 first. Then ask from the phone, answer from the lock screen of a second phone, the asker gets "Mia is in" |

Phase 1 can ship alone. Phases 2, 3 and 4 ship together: the check is of little use
without the phone.

After this plan: the ledger line in Crew history and belts for checks, game presence on
the phone, m3llo asking when two members are online and nobody asked, the HUD toast when
the HUD is back, Android.

## 7. Open points

1. **Lock screen answer.** The spike in 3.7 decides it. The plan holds with the fallback.
2. **Messages the backend posts skip the chat hooks.** `OnChatMessage` runs for client
   sends only, so the check adds no sidebar preview and no unread count for a member with
   another crew open. Notification 118 covers the toast. The unread badge needs a look in
   phase 2.
3. **The click on an OS notification.** Not checked: whether a click brings the window up
   today on Windows and macOS.
4. **The sound.** Needs a new file from Petter, or a sound we have until then.
5. **The default of message push.** On in a crew of up to 10 members from the release
   that has it. Those members get pushes they did not get before. A crew that grows past
   10 turns it off for each member who never set the switch.
6. **In from the phone has no time.** It means "on my way". The card shows "in" until the
   member joins voice.
7. **Who has a game.** Not checked: that the backend can tell which members have a game.
   The feed's installed list is sorted by owner count, so the data exists in some form.
   Phase 2 starts with this check. Without it a large crew gets every Ask.
8. **The number 10.** Closed. Bob confirmed 10 on 12 Nov 2026, for "large crew" and for
   the card with a row for each asked member.
9. **Who to offer in a large crew.** The first version offers the recent picks and who is
   on now. The members that a member plays with most can come later: the ledger has who
   played together.
