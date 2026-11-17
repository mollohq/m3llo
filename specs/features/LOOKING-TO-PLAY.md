# LOOKING-TO-PLAY

> **Component:** Crew chat, phone push (backend `looking_to_play*.go`,
> `message_push.go`, `push_prefs.go`; mello-core `looking_to_play.rs`; client
> `looking_to_play.rs`, `handlers/looking_to_play.rs`,
> `components/looking_to_play*.slint`; the iOS app)
> **Status:** shipped. Plain message push and the push switches shipped first
> (#260). The backend, mello-core and the desktop client followed (#263), then
> the core changes for the iOS app (#266). The iOS app has the card, the ask
> sheet and the answer buttons. One path is not verified on a real phone: an
> answer from a notification button with the phone locked. If the session read
> fails there, the member gets a notice and the answer goes out when the app
> opens (section 7, point 1).
> **Related:** `TEXT-CHAT.md` (the system message, section 10.3 the message
> push and the switches), `11-PRESENCE-CREW-STATE.md` (notification code 118),
> `NOTIFICATION-SOUNDS.md` (the sound), `16-CREW-EVENT-LEDGER.md` (the ledger
> that holds the `looking_to_play` crew event), `LOCALIZATION.md` (the error tokens),
> `designs/looking-to-play.html` (the mockup)

## Names

The feature is Looking to play: in the UI, in the specs and in the code
(`looking_to_play`, `ltp`). One Looking to play message is an ask. The member
who sends it is the asker. Do not call it a play check or a check.

## 1. The problem

A small crew asks "up for a game?" in a chat app that reaches the phone. The
answers are scattered. Nobody says "it's on", and nobody says "no one this
time", so the asker keeps checking the phone. The asker wants to know one
thing: is it worth going to the PC.

## 2. What the feature is

Two parts.

1. **Plain message push** (section 4). A crew message sends a phone push to
   the members who cannot see it. Each member has a Messages switch for each
   crew.
2. **Looking to play** (section 3). A button in the chat composer posts an ask
   to the crew, or to picked members. Members answer In, In 15, In 30 or Not
   now: on the card in chat, on the desktop toast, or on the phone push. The
   asker gets a push when someone is in, and a push when nobody is.

Not in scope: DMs and group chats, asks for a later time, a share link for
members with no app, m3llo asking by itself, belts for asks, the HUD toast,
Android.

## 3. The ask

### 3.1 Rules

- An ask goes to every member of the crew, or to the members that the asker
  picks. Only the members that the ask reaches (section 3.4) get the push, the
  toast and the sound. The card is in crew chat for every member, and any
  member can answer on it.
- An ask to picked members holds at most 50 members. The backend drops ids
  that are not crew members, the asker and duplicates. When no picked member
  is left, the ask fails with `BAD_REQUEST`.
- A member is the asker of at most one open ask in a crew (`ASK_HAS_OPEN`).
- A crew can have several open asks. Of the asks to everyone, at most one is
  open for each game, and one with no game. An ask to everyone with the game of
  such an ask counts as In on that ask. The reply then has `joined: true`, and
  no new ask opens.
- The asker counts as in from the start. The asker cannot answer, only cancel.
- The answers are `in`, `in_15`, `in_30` and `no`. A member can change the
  answer while the ask is open or on. `in_15` and `in_30` store the time when
  the member is due (`due_at`).
- A member who is in or due and joins a voice channel of the crew is marked as
  arrived (`arrived_at`). An answer that stays in keeps `arrived_at`.
- A member with no answer who joins a voice channel of the crew is set to `in`
  and arrived, when the crew has exactly one open ask. With several open asks,
  a voice join sets no answer. A guest in the voice channel does not count.
- Only the asker can cancel (`ASK_NOT_ASKER`).
- A member can ask in a crew once in 10 minutes, counted from the previous ask
  (`ASK_TOO_SOON`). This holds after a cancel too. The 10 minutes do not apply
  to an ask that counts as In on an open ask.
- An asker whose ask closed with reason `nobody` or `all_no` waits 30 minutes
  from the close before the next ask (`ASK_NOBODY_WAIT`).

### 3.2 States

| State | Means | Leaves when |
|---|---|---|
| `open` | Asked, no other member is in yet | A member other than the asker answers `in`, `in_15` or `in_30`, or a voice join sets a member to `in`: goes to `on`. Every member that the ask goes to answered `no`, 30 minutes passed since the ask, or the asker cancelled: goes to `closed` |
| `on` | At least one other member is in or due | 60 minutes after the ask, or the asker cancels: goes to `closed`. An `on` ask does not go back to `open` |
| `closed` | Over. The card keeps its last state in chat | Never |

A closed ask has a reason:

| Reason | Cause |
|---|---|
| `nobody` | The ask was `open` for 30 minutes |
| `all_no` | Every member that the ask goes to answered `no` while the ask was `open` |
| `cancelled` | The asker cancelled |
| `done` | The ask was `on`, and 60 minutes passed since the ask |

A loop closes asks on time. It runs every 15 seconds, so a close can come up
to 15 seconds late. In a large crew not every member answers, so the 30
minutes close most asks with no answer.

### 3.3 Backend

The files are in `backend/nakama/data/modules/`. An ask follows the reactions
pattern (`chat_reactions.go`). Each ask has one stored state. An RPC changes it
with a version check. The whole new state goes to every crew member as
notification 118.

| File | Holds |
|---|---|
| `looking_to_play.go` | The state of an ask and the rules of 3.1 and 3.2, as pure functions |
| `looking_to_play_store.go` | Storage, notification 118, the close of an ask, the loop |
| `looking_to_play_rpc.go` | The RPCs, the chat message of an ask, the voice join hook |
| `looking_to_play_push.go` | The phone pushes of an ask (3.4, 3.8) |
| `looking_to_play_picks.go` | The recent picks |
| `push_prefs.go` | The Messages and Looking to play switches (section 4) |

**Storage**

| Collection | Key | Owner | Value |
|---|---|---|---|
| `looking_to_play` | ask id | system user | The state of one ask (below) |
| `looking_to_play_open` | crew id | system user | `open`: the ids of the open asks. `last`: the last ask of each member (`opened_at`, `nobody_at`), kept 1 hour |
| `looking_to_play_picks` | crew id | the member | `groups`: the last 3 groups that the member picked |

No client can read or write these collections. The set of crews with an open
ask is in memory on the one Nakama node, the same as voice state. It is built
from `looking_to_play_open` at start.

**The state of an ask.** Clients get it as it is stored.

| Field | Value |
|---|---|
| `ask_id`, `crew_id` | Ids |
| `message_id` | The chat message of the ask |
| `asker_id` | The asker |
| `asker_name` | The display name of the asker when the ask opened. A client shows it for an ask of a crew whose members it has not loaded |
| `asked` | The picked user ids. Empty (`[]`, never `null`) for an ask to everyone |
| `game` | `{game_id, name}`, optional |
| `opened_at`, `closed_at` | Unix ms. `closed_at` only when closed |
| `state` | `open`, `on` or `closed` |
| `closed_reason` | `nobody`, `all_no`, `cancelled` or `done`. Only when closed |
| `answers` | User id to `{answer, at, due_at, arrived_at}`. Times in Unix ms. The asker has `in` from the start |
| `second_call_at` | Unix ms of the Second call push (3.4), else absent |

**The game of an ask.** `game_id` must have the form `<prefix>-<rest>`, with no
space and at most 128 bytes. `name` must not be empty and has at most 128
characters. The stable id of the library report (`steam-730`) has this form. A
client sends that id when it can map the game. Else it sends the id of the
game catalogue (3.6, 3.7). Section 3.8 says what each id does.

**The chat message.** The backend posts the message of an ask with
`ChannelMessageSend`. It is a system message:

```json
{
  "v": 1,
  "type": "system",
  "event": "looking_to_play",
  "body": "Leo is looking to play Counter-Strike 2",
  "data": {
    "ask_id": "...",
    "asker_id": "...",
    "game": { "game_id": "steam-730", "name": "Counter-Strike 2" }
  }
}
```

- The body is "{name} is looking to play", with " {game}" when the ask has a
  game. The name is the display name of the asker, or "A crew member".
- `data.game` is there only when the ask has a game.
- The sender is the system user. Nakama stores the sender as a UUID, so an
  empty sender cannot be stored. No member can edit or delete the message.
- A client that does not know the event shows the body as a system line.
- The message does not pass the chat hooks (section 7, point 2).

**RPCs**

| RPC | Payload | Reply |
|---|---|---|
| `looking_to_play_ask` | `{crew_id, game?, member_ids?}`. No `member_ids` asks everyone | `{ask, joined?, recent_picks?}`. `recent_picks` only after a new ask to picked members |
| `looking_to_play_answer` | `{ask_id, answer}` | `{ask}` |
| `looking_to_play_cancel` | `{ask_id}`. The asker only | `{ask}` |
| `looking_to_play_list` | `{crew_id, ask_ids?}`, at most 50 ids | `{asks, recent_picks}`. The open asks of the crew, then the asks of `ask_ids` that belong to the crew. `recent_picks` is `[]` when there are none |

The caller must be a member of the crew (`NOT_A_MEMBER`). The error tokens
follow `rpc_error_codes.go`:

| Token | Cause |
|---|---|
| `ASK_NOT_FOUND` | No ask has that id |
| `ASK_CLOSED` | An answer or a cancel on a closed ask |
| `ASK_NOT_ASKER` | A cancel by a member who is not the asker |
| `ASK_HAS_OPEN` | The member has an open ask in the crew |
| `ASK_TOO_SOON` | The 10 minutes of 3.1 |
| `ASK_NOBODY_WAIT` | The 30 minutes of 3.1 |
| `BAD_REQUEST` | A bad payload, an answer by the asker, or no picked member left |

**Notification 118** (`NotifyLookingToPlay`) carries the full state of one ask.
It goes to every crew member on each change: a new ask, an answer, a voice
join that changes the ask, a cancel and a close. A client holds one chat
channel at a time, so this is also how a member with another crew open learns
of a new ask. The notification of a new ask adds `notify` for each member but
the asker. `notify` is true for a member that the ask reaches (3.4), whether
or not the member gets the phone push. The asker gets no `notify` field.

**Recent picks.** After an ask to picked members, the backend puts that group
first in the list of the asker for the crew. A group holds sorted user ids and
is in the list once. The list keeps 3 groups. A read drops the ids of members
who left the crew, and the groups that become empty. An ask to everyone and an
ask that counts as In store no group.

**Voice.** The voice join path (`voice_state.go`) calls `queueLtpVoiceJoin` for
each member who joins a voice channel. It reads memory only, and does nothing
when the crew has no open ask. Else it applies the voice rules of 3.1 to each
open ask off the join path. It also sends the Answer and Second call pushes of
3.4.

**Ledger.** On each close, the backend adds a `looking_to_play` crew event:
actor the asker, `data` with `ask_id`, `reason`, `in` (the members who were in,
the asker left out), and `game_id` and `game_name` when the ask has a game.
Nothing reads this event yet (section 7, point 5).

### 3.4 Phone pushes of an ask

**Who an ask reaches.** The members that the ask goes to are the picked
members, or every member but the asker. Section 3.8 narrows this in a large
crew. Of these, a member with the Looking to play switch of the crew off is
left out. The rest are the members that the ask reaches. When the backend
cannot read the switches, the ask reaches nobody: no push and no
`notify: true`.

| Push | Type | To | When | Body | Buttons | Sound |
|---|---|---|---|---|---|---|
| New ask | `looking_to_play` | The members that the ask reaches | At the ask | "Leo is looking to play" or "Leo is looking to play Counter-Strike 2" | In, In 15, In 30, Not now | Yes |
| Answer | `looking_to_play_result` | The asker | Each answer that is `in`, `in_15` or `in_30` and changes the answer of a member. Each voice join that sets a member to `in` | "Mia is in", "Mia is in, 15 min", "Mia and Sam are in", "5 are in" | None | Only the push that turns the ask `on`. Each later one is silent |
| Second call | `looking_to_play` | The members that the ask reaches and who have not answered | Once for an ask: the first voice join after which two of the asker and the members who are in are in a voice channel of the crew | "Leo and Mia are playing", the asker first when the asker is one of them | In, In 15, In 30, Not now | Yes |
| Nobody | `looking_to_play_result` | The asker | The ask closes with reason `nobody` or `all_no` | "No one this time" | None | Yes |
| Called off | `looking_to_play_result` | The members who are in or due, the asker left out | The asker cancels | "Leo called it off" | None | Yes |

- An ask that closes with reason `done` sends no push.
- "Mia is in, 15 min" shows when one member is in, that member is due later,
  and has not arrived. The minutes are rounded up.
- "5 are in" counts the members who are in or due, the asker left out.
- The title is the crew name. With no crew name, the title is a fallback in
  the language of the recipient.
- The text that m3llo writes is in the account language of the recipient.
  The backend has English and Simplified Chinese texts. Other languages get
  English.
- A member with an active session gets no phone push of an ask. The client
  shows the toast and plays the sound from notification 118 (3.6).
- An ask never waits for the desktop grace of section 4. A member with only an
  inactive desktop gets the push at once.
- The Messages switch of section 4 does not apply. The Answer, Nobody and
  Called off pushes do not check the Looking to play switch.
- Every push of one ask has the collapse id `ltp-<id>`. `<id>` is the first 12
  hex characters of the SHA-256 of the ask id. Each push replaces the one
  before on the lock screen.
- `ttl_seconds` is the time left until the ask would close: 30 minutes after
  the ask while `open`, 60 minutes after the ask while `on`. The minimum is 60
  seconds. The Nobody and Called off pushes have 60 seconds.
- No quiet hours. A person asks, and the focus modes of the phone apply.
- A push has no cap on members. Each push goes to every registered device of
  the member, at most 20 device tokens in one Worker request. At most 8 sends
  run at the same time, shared with the message push. The burst limit of the
  message push does not apply.

### 3.5 mello-core

- `ChatMessage` has the `event` and the `data` of a system message.
  `looking_to_play::ask_id_of` gives the `ask_id` of an ask message.
- `looking_to_play.rs` holds the types of the state: `Ask`, `AskState`,
  `Answer`, `AskAnswer`, `AskGame`, and `AskNotification` (an `Ask` with
  `notify`). A `null` `asked` decodes as an ask to everyone. The core keeps no
  ask state. The client keeps it (3.6, the iOS app).
- Commands: `LookingToPlayAsk {crew_id, game?, member_ids}`,
  `LookingToPlayAnswer {ask_id, answer}`, `LookingToPlayCancel {ask_id}`.
- After each load of chat history, the core calls `looking_to_play_list` with
  the ask ids of the loaded messages.

| Event | Sent |
|---|---|
| `LookingToPlayUpdated {ask, notify}` | On notification 118, and on the reply to each command. `notify` is true only in the notification of a new ask, for a member that the ask reaches. The replies have `false` |
| `LookingToPlayLoaded {crew_id, asks}` | After `looking_to_play_list`, when it gives at least one ask |
| `LookingToPlayPicks {crew_id, groups}` | After each `looking_to_play_list`, and after a new ask to picked members |
| `LookingToPlayFailed {ask_id, code}` | A command failed. `code` is the server token, empty when the server gave none. `ask_id` is empty for a failed ask |

- The push switches: `Command::LoadPushPrefs`, `Command::SetPushPrefs`,
  `Event::PushPrefsLoaded`, `Event::PushPrefsFailed` (section 4).
- **A restore keeps the saved session** (`client/auth.rs`, `restore_session`).
  Only a refresh that the server refuses with a 4xx clears the saved session.
  A 408, a 429, a 5xx, a network failure and a failed socket connect keep it.
  A lock-screen answer with a bad network must not sign the member out. This
  holds on every platform.
- **The session on a locked iPhone** (`session/ios.rs`). The core writes the
  saved session to the Keychain with the class "after first unlock". An item
  of an older build moves to that class at the next restore. iOS gives an item
  with no class "when unlocked", and a locked phone refuses that read. The
  class is not "this device only", so the item moves to a new phone in an
  encrypted backup.

### 3.6 Desktop client

The rules are pure functions in `client/src/looking_to_play.rs`.
`handlers/looking_to_play.rs` reads the window state, calls them and writes
the Slint structs. `callbacks/looking_to_play.rs` wires the presses on the
`LookingToPlayUi` global. The client keeps every ask that it knows in
`LtpState`.

**The pill.** The fourth `PillButton` of the composer, after Attach, GIF and
Emoji (`chat_panel.slint`). Its label is "Looking to play". It carries a red
pip while the crew on screen has an ask that is open or on. It opens the ask
popover and closes the GIF and emoji popovers.

**The ask popover** (`looking_to_play_popover.slint`) sits over the pills.
Escape closes it. Top to bottom:

1. OPEN: the open asks of the crew, with the game and the count of members who
   are in. A press closes the popover and scrolls the chat to the bottom,
   where the newest asks are.
2. ASK: who to ask. Each member row shows what the member does now: the game
   line, or "in voice". A press picks or unpicks the member.
   - A crew with up to 10 members besides the viewer lists every member and
     Everyone. It starts with Everyone picked.
   - A larger crew has a field to find a member, the recent picks as one row
     for each group, then Everyone. Its member list holds the members in voice
     or in a game (at most 4), the search matches (at most 6) and the picked
     members. It starts with the newest recent group picked. With no recent
     group, nothing is picked.
   - A member press unpicks Everyone. Everyone unpicks every member. A group
     press picks every member of the group, or unpicks them when all are
     picked.
3. GAME: "Any game", then at most 3 game chips with art (`GameArt`). The
   default is Any game.
4. One red button: "ASK THE CREW" for Everyone, or "ASK 3" when 3 members are
   picked. It is off while nothing is picked and while an ask is on its way.

The game chips come from these sources, in this order. Each game shows once.

1. The game that the viewer plays now.
2. The games that the crew plays now.
3. The installed games of the viewer, in the order of the scan. Games hidden in
   Steam or in settings stay out.
4. The installed games of the crew feed.

A chip carries a library id (`steam-730`) when the client can map the game.
A catalogue id becomes the id of the installed game of the viewer with the
same name or an id that the catalogue gives it. Else it becomes the first
library id that the catalogue gives. Else the catalogue id stays.

A failed ask shows a line over the button and turns the button on again. The
line names the reason for `ASK_TOO_SOON`, `ASK_NOBODY_WAIT` and
`ASK_HAS_OPEN`. Any other code gives a general line.

**The card** (`LookingToPlayCard` in `looking_to_play.slint`). A
`looking_to_play` system message draws as a `CutCard` in the chat feed, not as
a system line. A message whose ask the client has no state of draws as a system
line. The chat row has `is_ask` and `ask`, and `same_card` compares every field,
so a row changes in place.

- The top line: the red pip while `open` or the ON tag while `on`, the label
  LOOKING TO PLAY, and the minutes until an open ask closes. A closed card
  shows the time of the close in the format of chat ("Yesterday", "23:14",
  "Oct 27").
- The title: "Leo is looking to play", or "Leo was looking to play" when
  closed.
- The game, with its art, while the ask is not closed.
- The rows: octagon avatar, name, state. The states are IN VOICE, IN, the
  minutes until the member is due (`tabular-nums`), NO ANSWER and NOT NOW.
  - IN VOICE: in or due, and in a voice channel of the crew. For the crew on
    screen this comes from the voice channels of the client. Else it comes from
    `arrived_at`.
  - An In 15 or In 30 whose due time passed shows IN.
  - The order is in voice, in, due with the soonest first, no answer, not now.
    Within a state, the asker is first, then by name.
- An ask to 10 members or fewer has a row for the asker, for each member that
  it goes to, and for each other member who answered.
- An ask to more than 10 members has rows only for the asker and the members
  who are in or due, at most 6. Then "{n} MORE IN" opens the full list. Not now
  shows as a count with no names. A member with no answer has no row.
- A member who left the crew has no row.
- A member who has not answered sees the four buttons. In is the red button.
  A member who answered sees In (when the answer is not In) and Change. Change
  shows the four buttons again. The asker sees Cancel.
- A closed card has no buttons and no rows.
  - Reason `done`: who was in, the asker first, three to a line (two when the
    card is narrow), at most six, then "{n} MORE IN".
  - Reason `cancelled`: "Leo called it off".
  - Reason `nobody` or `all_no`: "No one this time".
- Every 15 seconds the client counts the minutes again, while the crew on
  screen has an ask that is open or on.

**In.** A press on In sends the answer. When the ask is of the crew on screen
and the member is not in voice, the client also joins a voice channel. It is
the channel that holds the most of the asker and the members who are in. With
no such channel, or with a tie, it is the default channel. A member who is in
voice already only answers. In 15, In 30 and Not now only answer.

**What a new ask does.** Only `LookingToPlayUpdated` with `notify: true` does
something, and only once for an ask. The ask must still call for the member:
it is not closed, the member is not the asker and has not answered.

| Case | Result |
|---|---|
| The window is hidden or minimised | The OS notification and the sound |
| The card is on screen | The sound only |
| Any other case | The toast, and the sound when the toast shows |

The card is on screen when the crew of the ask is the crew on screen, the chat
panel shows, and the chat feed is at its bottom. With the Looking to play
switch of the crew off, the member gets `notify: false`: no toast, no sound and
no OS notification.

**The toast** (`LookingToPlayToast`, mounted in `main.slint`). A small card in
the top right corner of the window, 16 px in.

- It shows the crew, the asker ("Someone is looking to play" with no name), the
  game, the minutes until the ask closes, the count of members who are in, the
  four buttons and a close control.
- It has no scrim, does not take the keyboard focus and does not block the
  window. A modal draws over it.
- It closes after 12 seconds, on its close control, on an answer on the toast
  or the card, and when the ask stops calling for the member.
- One toast shows at a time. The next ask waits, and shows with the sound when
  the toast before it closes.
- In on the toast of an ask of another crew answers, opens that crew, and
  joins no voice channel.
- The name of the asker comes from the names that the client saw in this run.
  Else it comes from `asker_name`.

**The OS notification.** The title is the crew name. The body is "Leo is
looking to play", "Leo is looking to play Counter-Strike 2", or the same with
"Someone". It shows for 4 seconds. It has no buttons: notify-rust drops
actions on Windows and macOS. A click on it does not bring the window up
(section 7, point 3).

**The sound.** `Sound::LookingToPlay`, in the Crew group of the sound settings.
It plays at most once in 60 seconds. It uses the mention sound (section 7,
point 4). The asker gets no sound when a member is in. See
`NOTIFICATION-SOUNDS.md`.

**Unread.** An ask that picked the member is a mention for that member, until
its message is read.

- In a crew that is not on screen, the ask adds 1 to the unread count when the
  message previews do not hold its message. The badge uses the mention colour.
- In the crew on screen, with the chat not at its bottom, the badge of the crew
  turns to the mention colour.
- An ask to everyone in a crew that is not on screen adds nothing (section 7,
  point 2).

**The switches.** The NOTIFICATIONS tab of crew settings has the Messages row
and the Looking to play row (section 4).

### 3.7 iOS app

The iOS app is in its own repo. It uses the same mello-core. The push setup of
the app is in `IOS-07-PUSH-APNS.md` of that repo.

- **The button.** The composer has a second button. It has a red pip while the
  crew has an open ask. It opens the ask sheet.
- **The ask sheet.** It has the member list, the recent picks, the game chips
  and one red button. The game chips are "Any game" and at most 3 games: the
  games of the asks of the crew, then the installed games of the crew feed,
  then the game sessions of the crew feed. The phone has no game catalogue, so
  a catalogue id goes out as it is.
- **The card.** An ask draws as a card in the chat, with the same rules as the
  desktop card. In on the phone only answers. It does not join voice.
- **The answer buttons.** The app registers the notification category
  `LOOKING_TO_PLAY` with the actions In, In 15, In 30 and Not now. The actions
  need no unlock and do not bring the app to the front.
- **An answer from a notification button.**
  1. The action starts the core with no UI. It reports the real activity of
     the app before the restore, so the backend does not count the phone as
     active.
  2. The action waits up to 15 seconds for the session, then up to 15 seconds
     for the result.
  3. An answer that cannot go out is stored with the ask and the time. A local
     notice says "Could not send your answer. Open m3llo to send it."
  4. The next session sends a stored answer that is less than 30 minutes old.
  5. An ask that is over gives the notice "This ask is over." Nothing is
     stored.
  A restore that fails in the background does not show onboarding. The app
  tries the restore again when it comes to the front.
- **An ask with the app open.** The backend sends no phone push to an active
  phone. The app posts a local notice with the same four buttons when all of
  these are true: `notify` is true, the member is not the asker and has not
  answered, and the chat of that crew is not open. The local notice has the
  collapse id of the push, so one replaces the other.
- **Who is in a game.** The phone shows voice only, not who is in a game.
- **Game tiles** show the identity hue and a short code, not art.
- **Unread.** An ask in the crew that is open counts as a message. An ask that
  picked the member in a crew that is not open adds 1 to the unread count. The
  badge has no mention colour. An ask to everyone in a crew that is not open
  adds nothing (section 7, point 2).
- **The switches.** The Notifications section of the crew sheet (section 4).

### 3.8 Crew size

Looking to play works the same in a crew of 5 and a crew of 50. Two backend
rules depend on the size. The backend counts every member of the crew: a crew
with more than 10 members is large.

| Rule | Up to 10 members | More than 10 members |
|---|---|---|
| Who an ask to everyone that has a game reaches (New ask and Second call push, `notify`) | Every member | Members whose library has the game, and members with no library report |
| The Messages switch at the start (section 4) | On | Off |

- In a large crew, when no library of the members names the game, the ask
  reaches every member, as an ask with no game does. This is the case for a
  game that no launcher reports, and for a catalogue id.
- An ask to everyone with no game reaches every member at every size.
- An ask to picked members reaches those members at every size.
- A member can ask, answer and see every card in chat, whether or not the ask
  reached the member.

The desktop uses its own count for its layout:

| Layout | Large when |
|---|---|
| The ask popover | More than 10 members besides the viewer |
| The card | The ask goes to more than 10 members |

In a crew of exactly 11 members, the backend rules of a large crew apply, and
the desktop shows the layout of a small crew.

Rules that hold at every size: picked members and several open asks (3.1),
the row limits of the card, the popover and the toast (3.6), the pushes that
the asker gets (3.4), and the batches of 20 device tokens (3.4). Notification
118 goes to every member on each change, as notification 117 of the reactions
does.

## 4. Plain message push

`message_push.go`, called from the chat hook `OnChatMessage`
(`crew_state.go`). See also `TEXT-CHAT.md` section 10.3.

- **Which messages.** `text`, `gif` and `image` of a member. A system message
  sends none. A mentioned member gets the mention push, and not this one.
- **Who.** Every crew member but the sender, with the Messages switch of the
  crew on. There is no cap on members.
- **When.** The activity rule of the mention push:

  | Sessions of the member | Push |
  |---|---|
  | None | At once |
  | Any session active, or a session that never reported its activity | None |
  | Only inactive sessions, one of them a desktop | After the desktop grace (120 seconds), when no session is active then |
  | Only inactive phone sessions | At once |

- **Text.** The title is the crew name. The body is "Leo: the text", "Leo sent
  a GIF" or "Leo sent an image". Mention tokens in the text show as names. The
  text that m3llo writes is in the account language of the recipient.
- **Bursts.** Every message push of a crew has the collapse id `msg-<id>`.
  `<id>` is the first 12 hex characters of the SHA-256 of the crew id. A new
  push replaces the one before on the lock screen. The first message to a
  member goes at once. More messages of that crew in the next 30 seconds are
  held. One push leaves when the 30 seconds end: one message as it is, several
  as "4 new messages". A busy crew does not hold back the pushes of another
  crew.
- **Tap.** Opens the crew chat in the iOS app.
- **The switches** (`push_prefs.go`). Each member has two switches for each
  crew, Messages and Looking to play.
  - Only a choice that the member made is stored: collection `push_prefs`, key
    `prefs`, one doc for each user, with the crews inside it. No client can read
    it.
  - A switch with no stored choice uses its default. Looking to play is on.
    Messages is on in a crew of up to 10 members and off in a larger crew. A
    crew that grows past 10 members turns Messages off for each member who
    never set it.
  - `push_prefs_get {crew_id}` and `push_prefs_set {crew_id, messages?,
    looking_to_play?}` return `{crew_id, messages, looking_to_play}` with the
    defaults filled in. The caller must be a member.
  - The backend reads the switches before each send. When the read fails, it
    sends nothing.
  - A mention push ignores both switches.
  - Desktop: the NOTIFICATIONS tab of crew settings. Every member opens it from
    the Notifications item of the crew menu. A plain member sees that tab only.
  - iOS: the Notifications section of the crew sheet.
- **Privacy.** The message text goes through the push Worker and Apple, as the
  text of a mention push does. Nothing new is stored.

## 5. Push Worker

The backend sends each push to the push Worker, which sends it to APNs. This
section is the contract.

| Type | Use | Buttons |
|---|---|---|
| `message` | A plain message push (section 4) | None |
| `looking_to_play` | New ask and Second call | The answer buttons |
| `looking_to_play_result` | Answer, Nobody and Called off | None |

- A notification has `type`, `crew_id`, `channel_id`, `message_id`, `title`
  and `body`.
- The Worker passes these optional fields to APNs. A push of another type
  leaves them out.

  | Field | Value |
  |---|---|
  | `category` | `LOOKING_TO_PLAY`: the notification category with the answer buttons |
  | `silent` | True for a push with no sound. It replaces a push that the member heard |
  | `ttl_seconds` | How long the push waits for a phone that is off the net |
  | `ask_id` | The ask, as custom data, for the answer that a button sends |

- The collapse id is `message_id`. Every push of one ask sends `ltp-<id>`
  there (3.4). Every message push of a crew sends `msg-<id>` (section 4).

## 6. Verification

**Go tests** (`backend/nakama/data/modules/`):

| File | Covers |
|---|---|
| `looking_to_play_test.go` | The rules of 3.1 and 3.2: the states, the answers, the due times, all no, the windows of 30 and 60 minutes, the voice join, the picked members, the rules and waits of a new ask |
| `looking_to_play_rpc_test.go` | The RPCs on a fake Nakama: the message, notification 118, each push of 3.4, the join of an ask for the same game, the loop, the restart, the voice join and the Second call, the switch and the activity rule, the large crew, the list, bad calls |
| `looking_to_play_picks_test.go` | The recent picks, and `notify` for the members that an ask reaches, in a small and a large crew |
| `push_prefs_test.go` | The defaults by crew size, a stored choice, the RPCs |
| `message_push_test.go`, `push_coalesce_test.go`, `push_text_test.go`, `push_remote_test.go` | The message push: recipients, body, language, collapse id, bursts, the limiter for each crew |

**mello-core tests**

- `looking_to_play.rs`: the state decodes, a `null` `asked`, the `notify` flag
  of notification 118, the recent picks of the replies, the answer names.
- `command.rs`: the Looking to play and push switch commands decode.
- `nakama/client.rs`: the payloads of `looking_to_play_ask`,
  `looking_to_play_answer`, `push_prefs_get` and `push_prefs_set`, and the
  refresh errors that clear the session.
- `client/auth.rs` and `client/connection_tests.rs`: only a refused refresh
  token clears the saved session.
- `session.rs`: a locked Keychain is not a missing session.

**Desktop tests**

- `client/src/looking_to_play.rs`: the card rows and order, the large card,
  the closed card, the minutes, the voice channel of In, the popover lists,
  the picks, the game chips and the id mapping, the toast rules and queue,
  the mention, the pending ask, the name of the asker.
- `client/src/flow_tests/looking_to_play.rs`: the headless harness with the
  real wiring. The card from history, a system line with no state, an update in
  place, In and the voice join, In while in voice, Cancel, the pip, the
  popover, a failed ask, a large crew, the toast and its queue, the sound, the
  OS notification, the mention, the game chip of the viewer.
- `client/src/flow_tests/push_prefs.rs`: every way into crew settings loads the
  switches.
- `client/src/sounds.rs`: the sound plays once in 60 seconds and obeys the
  Crew switch.

**Driver journey.** `qa/journeys/looking-to-play-two-users.ts`
(`looking-to-play.two-users`, flow `LTP-01` in `qa/flows.yaml`). Two clients on
the local stack. The asker asks the crew from the pill with Everyone picked.
The other member sees the card with the four answers and no toast, presses In
and joins voice. The asker sees the ask on, joins the same channel, then
cancels. Both see the ask closed and the pip gone. Both calls go through the
local SFU. Any other call path fails the journey.

**Live-stack suite.** `tools/e2e/tests/backend.rs`,
`mention_pushes_follow_the_delivery_rules`, with a stub Worker:

- A plain message reaches an offline member with one collapse id for the crew.
  A member with Messages off gets none. A second write of the switches keeps the
  first choice.
- An ask reaches an offline member with the answer buttons. An answer reaches
  the asker. A cancel reaches the member who is in. A member with Looking to
  play off gets no push of an ask.

**iOS simulator** (iPhone 18 Pro, iOS 27.0, local stack):

- The card with live data, and an answer from the card.
- An ask from the sheet to one picked member.
- The answer path from a cold start: the session restored, the answer on the
  server in about 1 second.
- The fallback with the server not reachable: the session kept, the answer
  stored, the notice shown.
- The stored answer sent at the next start.
- A debug gallery of the app shows each state of the card and the sheet on
  mock data.

**Not verified.** These need a real phone:

- The four buttons on the lock screen. A script cannot hold a notification down
  on the simulator.
- The start of the core from a notification action with no scene.
- The Keychain read with the phone locked.
- A remote push that replaces a local notice.

## 7. Known gaps and later work

1. **The answer from a locked phone.** The paths of section 6 "Not verified"
   are not tested on a real phone. When the session read fails, the fallback
   of 3.7 applies: a notice, and the answer goes out when the app opens.
2. **Messages that the backend posts skip the chat hooks.** `OnChatMessage`
   runs for client sends only. The message of an ask therefore sends no
   message preview (notification 115) and no message push. It does not count
   as chat activity and does not update the last seen time. What this means
   today:
   - For a member with another crew open, an ask to everyone adds no unread
     count and no sidebar preview, on desktop and on the phone.
   - An ask that picked the member adds 1 to the unread count of that crew. The
     desktop badge has the mention colour. The phone badge has none.
   - Notification 118 covers the toast, the sound, the OS notification and the
     local notice of the phone.
3. **The click on the desktop OS notification** does not bring the window up.
   No notification of the desktop client does that today.
4. **The sound.** `Sound::LookingToPlay` has no file of its own. It uses
   `mention.opus` until a new file is here.
5. **The ledger event has no reader.** The `looking_to_play` crew event of 3.3
   has no feed card, no Crew history line and no belts.
6. **In from the phone has no time.** It means "on my way". The card shows "in"
   until the member joins voice. In from the phone, on the card or on a
   notification button, does not join voice.
7. **Game presence on the phone.** The phone shows voice only, so its ask sheet
   cannot show who is in a game. Its game tiles have no art.
8. **The size limit differs at 11 members.** The backend counts every member,
   the desktop counts the others (3.8). A crew of 11 gets the backend rules of
   a large crew and the desktop layout of a small crew.
9. **A game id with no hyphen.** The backend refuses a game id that does not
   have the form `<prefix>-<rest>` (`BAD_REQUEST`). A catalogue id with no
   hyphen fails. No test covers an ask with such an id.
10. **Who to offer in a large crew.** The popover offers the recent picks, who
    is on now and a search. It does not offer the members that a member plays
    with most. The ledger has who played together.
11. **Chinese.** No native reader reviewed the Simplified Chinese push texts of
    an ask. The new UI messages of the desktop have no Chinese text, except the
    label "Looking to play".
