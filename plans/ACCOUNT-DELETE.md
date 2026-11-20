# Account delete in the clients

> Status: built. Bob approved the renders on 17 Nov 2026. In review.
> Branch: `feat/account-delete` (worktree `mello-wt-account-delete`). The iOS app gets a
> branch of the same name in `mello-ios`.
> The backend rules it states merged in mollohq/m3llo#264 (`feat/delete-followups`).
> Specs updated on the branch: `specs/01-CLIENT.md` (PROFILE tab, ACCOUNT) and
> `specs/16-CREW-EVENT-LEDGER.md` section 10.2.

## 1. Why

Apple requires an in-app account delete for an app that lets a user make an account.
The backend has the delete (`DELETE /v2/account` with the two hooks, spec 16 section 10.2).
No client shows it.

## 2. What exists

| Part | State |
|---|---|
| Backend | `BeforeDeleteAccount`, `AfterDeleteAccount`, the scrub (#262). Handover and refusal (#264) |
| mello-core | `Command::DeleteAccount`, `handle_delete_account` (`client/auth.rs`). On success it runs the logout teardown and sends `AccountDeleted`. On failure it keeps the session and sends `AccountDeleteFailed { reason }`, where `reason` is the raw HTTP body |
| Desktop client | `handlers/auth.rs` logs the two events. No UI |
| iOS | Nothing. The FFI is a JSON bridge (`mello_core_send_command`), so no FFI change is needed |

## 3. Core change

`AccountDeleteFailed` gets a second field:

```rust
AccountDeleteFailed {
    reason: String,              // the raw error, for the log
    #[serde(default, skip_serializing_if = "Option::is_none")]
    message: Option<String>,     // the server text, when the server refused (code 14)
}
```

- `message` is `Error::server_message()` when `Error::server_code()` is 14 (UNAVAILABLE).
  Spec 16 says that text is safe to show. Any other failure gives `None`.
- The field is additive. Old JSON still decodes. The perf harness waits on the event name only.
- Tests: the mapping from an error to the event, for code 14, another code, and a network error.

## 4. Desktop

Settings, PROFILE tab, a new section at the bottom: `ACCOUNT`.

- One row: the title "Delete account", the sentence "This cannot be undone.", and an outline
  button "DELETE ACCOUNT". The button opens the confirm. It does not commit, so it is not red.
- The confirm is a dialog inside the settings modal, on a black wash. A card with one cut corner.

Confirm copy:

| Part | Text |
|---|---|
| Title | Delete your account |
| DELETED | Your account, your name and your sign-in. The clips, streams and events you started. Your chat messages and the images you posted |
| REMOVED | Your name, from the events, clips and weekly recaps of other members |
| STAYS | The clips and events of other members, without you. The totals of weekly recaps |
| YOUR CREWS | A crew you own goes to an admin, or to the member who joined first. A crew where you are the only member is deleted |
| Last line | This cannot be undone |
| Buttons | "CANCEL" (outline), "DELETE MY ACCOUNT" (red fill, bottom-right) |

The commit button has a label of its own ("Delete my account"). The row button stays
labelled "Delete account". Both are on screen at once, and the e2e driver and a screen
reader find a control by its label.

States:

| State | UI |
|---|---|
| Idle | As above |
| Busy | The red button shows "DELETING", both buttons are disabled, Escape and the backdrop do nothing |
| Refused (`message` is set) | The server text in red above the buttons. Both buttons work again: the user can retry |
| Other failure | "Your account was not deleted. Check the connection and try again." Same as refused |
| Deleted | The dialog and the settings modal close. The client goes to onboarding step 1 |

After the delete:

- The client runs the UI part of logout. It does not send `Command::Logout` (core already ran it).
- `Settings::device_id` is cleared and saved. Logout sends `DeviceAuth` with the stored id, and
  device auth uses `create=true`, so the old id would make a new account at once. A new id is
  made at the next finalize, as on a fresh install.

Tests (`client/src/flow_tests.rs`, the headless harness):

- The ACCOUNT button opens the confirm. Cancel closes it and sends nothing.
- "Delete account" sends one `Command::DeleteAccount` and sets busy. A second press sends nothing.
- `AccountDeleteFailed` with a message shows the message and clears busy.
- `AccountDeleteFailed` with no message shows the fallback text.
- `AccountDeleted` closes the modal, shows onboarding step 1, clears `device_id`, and sends no `DeviceAuth`.

Renders: `slint-viewer` Previews for the ACCOUNT section and the confirm in idle, busy and refused.

## 5. iOS

- `Command.swift`: `case deleteAccount` (`{"type":"DeleteAccount"}`), in the encoder and the decoder.
- `Event.swift`: `accountDeleted` and `accountDeleteFailed(reason:message:)`.
- `SettingsView.swift`: an ACCOUNT section under Sign out, with a "Delete account" outline button.
- The confirm is a sheet with the same copy as the desktop, and a `.confirm` (red) button with a
  spinner while busy. The sheet cannot be dismissed while busy.
- `AppModel`: `deleteAccount()`, the busy flag, the error text. On `accountDeleted` it runs the
  state reset of `logout()` without sending `.logout`.
- iOS has no test target. The proof is a simulator run against the local Nakama.
- The device id is not forgotten on iOS. iOS sends it only at the finalize of onboarding,
  and runs no device auth after a logout. So no account is made until the user signs up again.

## 6. Proof against the local stack

- Local Nakama only. Test accounts on it are fine. Nothing points at production.
- A core test with a fake HTTP answer for code 14 and for success.
- An e2e run (local Docker Nakama, `tools/e2e`): a member deletes the account through the
  client command path, and the account is gone. The refused case uses `dev_fault` from
  `feat/delete-followups` when that branch is in the stack.

## 7. Open points for Bob

1. The refused text comes from the server in English. Other server errors use tokens that the
   client translates (`server_token`). The plan shows the server text, as asked. A token
   (`ACCOUNT_DELETE_REFUSED`) would let the client translate it.
2. No typed confirm (such as typing DELETE). One confirm step only.
