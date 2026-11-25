//! End-to-end tests against a live Nakama stack.
//!
//! Fills the largest hole in the suite: **not one Go RPC handler or hook is
//! covered by a test.** All 106 backend tests are pure functions — nothing
//! exercises a registered RPC, a before/after hook, or a payload shape. A
//! renamed field, a changed error code, or a broken hook passes every existing
//! check and only fails against a real server.
//!
//! The RPC contract test (`mello-core/tests/rpc_contract.rs`) catches renamed
//! *names* statically; these catch changed *behaviour*.
//!
//! Requires a running backend. Start one with:
//!
//! ```text
//! ./scripts/e2e.sh
//! ```
//!
//! Tests skip (loudly) when `MELLO_E2E` is unset, so `cargo test --workspace`
//! stays hermetic and fast for everyone else.

mod common;

use common::{next_send_of_type, stub_push_worker};
use mello_core::nakama::client::NakamaClient;
use mello_core::Config;

/// Local dev stack, overridable through the usual `NAKAMA_*` variables.
fn e2e_config() -> Config {
    Config::development().with_env_overrides()
}

/// Whether the caller asked for e2e tests.
///
/// Returns false rather than failing so the suite stays green on a machine
/// with no backend — but every skip prints, so a silently-empty e2e run is
/// visible rather than looking like success.
fn e2e_enabled(test_name: &str) -> bool {
    if std::env::var("MELLO_E2E").is_ok() {
        return true;
    }
    println!("SKIP {test_name}: set MELLO_E2E=1 and run ./scripts/e2e.sh for a live backend");
    false
}

fn rt() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .expect("runtime")
}

fn random_device_id() -> String {
    format!("e2e-{:032x}", rand::random::<u128>())
}

/// The health RPC also carries the protocol version the client checks on
/// connect, so a mismatch here means every client refuses to talk to this
/// server.
#[test]
fn health_rpc_reports_a_compatible_protocol() {
    if !e2e_enabled("health_rpc_reports_a_compatible_protocol") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");

        let raw = client
            .rpc("health", &serde_json::json!({}))
            .await
            .expect("health rpc");
        let body: serde_json::Value = serde_json::from_str(&raw).expect("health returns JSON");

        assert!(
            body.get("protocol_version").is_some(),
            "health must report protocol_version; the client gates connection on \
             it. Got: {body}"
        );
    });
}

/// Guest discovery is the first call a new user makes, and the one that took
/// signup down. Exercised here against a real server so a change to the
/// handler's response shape is caught locally rather than in production.
#[test]
fn guest_discovery_works_without_a_session() {
    if !e2e_enabled("guest_discovery_works_without_a_session") {
        return;
    }

    rt().block_on(async {
        // Deliberately no authentication: this must work for a brand-new user
        // who has never had a session, using only the http_key.
        let client = NakamaClient::new(e2e_config());
        let (crews, _cursor) = client
            .discover_crews_public(50, None)
            .await
            .expect("guest discovery must work with only the http_key");

        // Seeded stacks have crews; an empty result is legal but degrades
        // onboarding to create-only, so say so rather than asserting.
        println!("discovered {} crews", crews.len());
    });
}

/// Full signup: create an account, then create a crew through the real
/// `create_crew` RPC (which also provisions a default voice channel and an
/// invite code, and fires the AfterJoinCrew hook).
#[test]
fn signup_and_crew_creation_round_trip() {
    if !e2e_enabled("signup_and_crew_creation_round_trip") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());

        let (user, created) = client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");
        assert!(created, "a fresh device id must create a new account");
        assert!(!user.id.is_empty(), "the new account must have an id");

        let crew_name = format!("E2E Crew {:x}", rand::random::<u32>());
        let raw = client
            .rpc(
                "create_crew",
                &serde_json::json!({
                    "name": crew_name,
                    "description": "created by the e2e suite",
                    "open": false,
                }),
            )
            .await
            .expect("create_crew");

        let body: serde_json::Value = serde_json::from_str(&raw).expect("create_crew returns JSON");
        let crew_id = body
            .get("crew_id")
            .or_else(|| body.get("id"))
            .and_then(|v| v.as_str())
            .unwrap_or_else(|| {
                panic!("create_crew response has no crew id. Shape changed? Got: {body}")
            });
        assert!(!crew_id.is_empty());

        // Clean up so repeated local runs do not accumulate state.
        let _ = client.delete_account().await;
    });
}

/// The delete of Settings (plans/ACCOUNT-DELETE.md): `DELETE /v2/account`,
/// the call `Command::DeleteAccount` makes. After it, the old session reaches
/// no account, and the same device id makes a new account. That is why the
/// desktop client forgets its device id after a delete: its logout path runs
/// device auth, which would make that new account at once.
#[test]
fn a_deleted_account_is_gone_and_its_device_id_makes_a_new_account() {
    if !e2e_enabled("a_deleted_account_is_gone_and_its_device_id_makes_a_new_account") {
        return;
    }

    rt().block_on(async {
        let device_id = random_device_id();
        let mut client = NakamaClient::new(e2e_config());
        let (user, created) = client
            .authenticate_device(&device_id)
            .await
            .expect("device auth");
        assert!(created);
        let refresh = client
            .refresh_token()
            .expect("a session has a refresh token")
            .to_string();

        client.delete_account().await.expect("the delete succeeds");

        let mut again = NakamaClient::new(e2e_config());
        assert!(
            again.refresh_session(&refresh).await.is_err(),
            "the session of a deleted account must not refresh"
        );

        let (next, created) = again
            .authenticate_device(&device_id)
            .await
            .expect("device auth with the old id");
        assert!(created, "the old device id makes a new account");
        assert_ne!(next.id, user.id, "the deleted account does not come back");

        let _ = again.delete_account().await;
    });
}

/// The refused delete of Settings (plans/ACCOUNT-DELETE.md, spec 16 section
/// 10.2). The only owner of a crew with another member deletes the account,
/// and the handover fails. The answer is code 14 with a text the client shows
/// as it is (`AccountDeleteFailed.message`). The account stays. A retry
/// deletes it, and the other member owns the crew.
///
/// The stack needs `MELLO_ENABLE_DEV_FAULT=1`. `scripts/e2e.sh` sets it.
#[test]
fn a_refused_delete_carries_a_text_to_show_and_a_retry_hands_the_crew_over() {
    if !e2e_enabled("a_refused_delete_carries_a_text_to_show_and_a_retry_hands_the_crew_over") {
        return;
    }

    rt().block_on(async {
        let (owner, crew_id, code) = crew_with_invite(false).await;
        let mut joiner = NakamaClient::new(e2e_config());
        let (joiner_user, _) = joiner
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth for the joiner");
        joiner
            .join_by_invite_code(&code)
            .await
            .expect("the joiner joins by the invite code");

        owner
            .rpc(
                "dev_fault",
                &serde_json::json!({"action": "fail_next_handover", "count": 1}),
            )
            .await
            .unwrap_or_else(|e| {
                panic!("dev_fault failed ({e}). The stack needs MELLO_ENABLE_DEV_FAULT=1")
            });

        let refused = owner
            .delete_account()
            .await
            .expect_err("a failed handover refuses the delete");
        assert_eq!(refused.server_code(), Some(14), "the refusal: {refused}");
        assert_eq!(
            refused.server_message().as_deref(),
            Some(
                "Your account was not deleted. A crew you own could not be handed to another \
                 member. Try again in a moment."
            ),
            "the client shows this text as it is"
        );
        assert!(
            owner.list_user_groups().await.is_ok(),
            "the account and the session stay after a refusal"
        );

        owner
            .delete_account()
            .await
            .expect("the retry deletes the account");
        assert_eq!(
            crew_member_state(&joiner, &crew_id, &joiner_user.id).await,
            Some(GROUP_STATE_SUPERADMIN),
            "the other member owns the crew after the delete"
        );

        let _ = joiner.delete_account().await;
    });
}

/// Presence at connect belongs to the server. `OnSessionStart` sets a fresh
/// account online when its socket registers. The client sends no
/// `presence_update` at connect: it wrote the same row at the same moment,
/// and on a fresh account Nakama refused the second insert (code 13).
#[test]
fn a_fresh_account_is_online_once_its_socket_connects() {
    if !e2e_enabled("a_fresh_account_is_online_once_its_socket_connects") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        let (user, created) = client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");
        assert!(created, "a fresh device id must create a new account");

        let (event_tx, _events) = std::sync::mpsc::channel();
        client.connect_ws(event_tx).await.expect("socket connect");

        // The hook runs on Nakama's event queue, so wait on the stored
        // state, bounded.
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        loop {
            let raw = client
                .presence_get(std::slice::from_ref(&user.id))
                .await
                .expect("presence_get");
            let body: serde_json::Value =
                serde_json::from_str(&raw).expect("presence_get returns JSON");
            let status = body["presences"][&user.id]["status"].as_str().unwrap_or("");
            if status == "online" {
                break;
            }
            assert!(
                std::time::Instant::now() < deadline,
                "the session-start hook did not set the account online; got {body}"
            );
            tokio::time::sleep(std::time::Duration::from_millis(50)).await;
        }

        let _ = client.delete_account().await;
    });
}

/// Signing in with an unknown email must fail, not quietly create an account.
///
/// This used to send `create=true`, so a typo on the sign-in panel produced a
/// brand-new account with no crew, nickname or avatar — the user saw an empty
/// app and assumed their crews were gone. Asserted against a live server
/// because the behaviour lives in a query parameter, where a unit test would
/// only be re-reading the same string.
#[test]
fn signing_in_with_an_unknown_email_is_refused() {
    if !e2e_enabled("signing_in_with_an_unknown_email_is_refused") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        let email = format!("nobody-{:032x}@example.invalid", rand::random::<u128>());

        let result = client.login_email(&email, "irrelevant-password").await;

        assert!(
            result.is_err(),
            "signing in with an address that has no account must fail; \
             creating one here bypasses onboarding entirely"
        );
    });
}

/// ...and a real account can still sign in, so the above is not just breaking
/// login outright.
#[test]
fn an_existing_email_identity_can_still_sign_in() {
    if !e2e_enabled("an_existing_email_identity_can_still_sign_in") {
        return;
    }

    rt().block_on(async {
        let email = format!("e2e-{:032x}@example.invalid", rand::random::<u128>());
        let password = "correct-horse-battery";

        // Create the identity the way onboarding does: device account first,
        // then link an email to it.
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");
        client
            .link_email(&email, password)
            .await
            .expect("link email to the device account");

        // A fresh client, as a returning user on another launch.
        let mut returning = NakamaClient::new(e2e_config());
        let user = returning
            .login_email(&email, password)
            .await
            .expect("a linked email identity must still sign in with create=false");
        assert!(!user.id.is_empty());

        let _ = client.delete_account().await;
    });
}

/// `voice_join` enforces capacity and signs an SFU token. The SFU is the only
/// voice transport, so the answer is always mode "sfu" with an endpoint and a
/// token. scripts/e2e.sh gives Nakama a signing key.
#[test]
fn voice_join_returns_a_usable_room() {
    if !e2e_enabled("voice_join_returns_a_usable_room") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");

        let crew_name = format!("E2E Voice {:x}", rand::random::<u32>());
        let raw = client
            .rpc(
                "create_crew",
                &serde_json::json!({ "name": crew_name, "open": false }),
            )
            .await
            .expect("create_crew");
        let body: serde_json::Value = serde_json::from_str(&raw).expect("JSON");
        let crew_id = body
            .get("crew_id")
            .or_else(|| body.get("id"))
            .and_then(|v| v.as_str())
            .expect("crew id")
            .to_string();

        let raw = client
            .rpc("voice_join", &serde_json::json!({ "crew_id": crew_id }))
            .await
            .expect("voice_join on a crew we just created and own");
        let body: serde_json::Value = serde_json::from_str(&raw).expect("JSON");

        assert_eq!(
            body.get("mode").and_then(|v| v.as_str()),
            Some("sfu"),
            "voice_join must answer mode sfu. Got: {body}"
        );
        assert!(
            body.get("channel_id").and_then(|v| v.as_str()).is_some(),
            "voice_join must say which channel was joined. Got: {body}"
        );
        for field in ["sfu_endpoint", "sfu_token"] {
            assert!(
                body.get(field)
                    .and_then(|v| v.as_str())
                    .is_some_and(|s| !s.is_empty()),
                "voice_join must carry {field}. Got: {body}"
            );
        }

        let _ = client.delete_account().await;
    });
}

/// Joining a crew through an invite link.
///
/// `join_by_invite_code` passed an empty username to `GroupUserJoin`, which
/// Nakama 3.21 rejects with "expects a username string". Every invite join
/// failed, and the only other join path (Discover) uses Nakama's built-in
/// group join, so nothing else covered it.
#[test]
fn a_second_user_can_join_a_crew_by_invite_code() {
    if !e2e_enabled("a_second_user_can_join_a_crew_by_invite_code") {
        return;
    }

    rt().block_on(async {
        // User A creates an open crew and shares a fresh invite code.
        let mut owner = NakamaClient::new(e2e_config());
        owner
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth for the owner");
        let crew_name = format!("E2E Invite {:x}", rand::random::<u32>());
        let (crew, _) = owner
            .create_crew(&crew_name, "", true, None, &[])
            .await
            .expect("create_crew");
        let code = owner
            .create_invite_code(&crew.id)
            .await
            .expect("create_invite_code");

        // User B signs up and follows the link.
        let mut joiner = NakamaClient::new(e2e_config());
        let (joiner_user, _) = joiner
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth for the joiner");

        let (joined_id, joined_name) = joiner
            .join_by_invite_code(&code)
            .await
            .expect("join_by_invite_code must succeed for a valid code");
        assert_eq!(joined_id, crew.id, "the RPC must return the invited crew");
        assert_eq!(joined_name, crew_name);

        let joiner_crews = joiner.list_user_groups().await.expect("list_user_groups");
        assert!(
            joiner_crews.iter().any(|c| c.id == crew.id),
            "the joiner must now be in the crew. Got: {joiner_crews:?}"
        );
        let members = owner
            .list_group_users(&crew.id)
            .await
            .expect("list_group_users");
        assert!(
            members.iter().any(|m| m.id == joiner_user.id),
            "the owner must see the joiner as a member. Got: {members:?}"
        );
        assert_eq!(
            crew_member_state(&owner, &crew.id, &joiner_user.id).await,
            Some(GROUP_STATE_MEMBER),
            "the joiner must be a member, not a join request"
        );

        // Following the same link again is not an error: the client opens the
        // crew.
        let (again_id, _) = joiner
            .join_by_invite_code(&code)
            .await
            .expect("an existing member following the link again must succeed");
        assert_eq!(again_id, crew.id);

        let _ = joiner.delete_account().await;
        let _ = owner.delete_account().await;
    });
}

/// Nakama `group_edge` states the invite tests assert on.
const GROUP_STATE_SUPERADMIN: i64 = 0;
const GROUP_STATE_MEMBER: i64 = 2;

/// The caller's view of `user_id`'s edge state in the crew, or `None` when the
/// user has no edge.
///
/// Nakama lists a join request next to the real members, so a membership
/// check that only looks for the user ID passes for a pending request.
/// `crew_state_get` reports each member's edge state as `role`.
async fn crew_member_state(viewer: &NakamaClient, crew_id: &str, user_id: &str) -> Option<i64> {
    let raw = viewer
        .rpc("crew_state_get", &serde_json::json!({ "crew_id": crew_id }))
        .await
        .expect("crew_state_get");
    let body: serde_json::Value = serde_json::from_str(&raw).expect("crew_state_get returns JSON");
    let members = body
        .get("members")
        .and_then(|m| m.as_array())
        .unwrap_or_else(|| panic!("crew_state_get has no members list. Got: {body}"));
    members
        .iter()
        .find(|m| m.get("user_id").and_then(|v| v.as_str()) == Some(user_id))
        .map(|m| {
            m.get("role")
                .and_then(|v| v.as_i64())
                .unwrap_or_else(|| panic!("member has no role. Got: {m}"))
        })
}

/// A new owner with a crew and a fresh invite code for it.
async fn crew_with_invite(open: bool) -> (NakamaClient, String, String) {
    let mut owner = NakamaClient::new(e2e_config());
    owner
        .authenticate_device(&random_device_id())
        .await
        .expect("device auth for the owner");
    let crew_name = format!("E2E Invite {:x}", rand::random::<u32>());
    let (crew, _) = owner
        .create_crew(&crew_name, "", open, None, &[])
        .await
        .expect("create_crew");
    let code = owner
        .create_invite_code(&crew.id)
        .await
        .expect("create_invite_code");
    (owner, crew.id, code)
}

/// The members of the crew that sit in a voice channel, as `crew_state_get`
/// lists them.
async fn crew_voice_member_ids(viewer: &NakamaClient, crew_id: &str) -> Vec<String> {
    let raw = viewer
        .rpc("crew_state_get", &serde_json::json!({ "crew_id": crew_id }))
        .await
        .expect("crew_state_get");
    let body: serde_json::Value = serde_json::from_str(&raw).expect("crew_state_get returns JSON");
    body["voice_channels"]
        .as_array()
        .into_iter()
        .flatten()
        .flat_map(|ch| ch["members"].as_array().cloned().unwrap_or_default())
        .filter_map(|m| m["user_id"].as_str().map(String::from))
        .collect()
}

/// A new account that joined the crew of the invite code.
async fn joined_account(code: &str) -> (NakamaClient, String) {
    let mut account = NakamaClient::new(e2e_config());
    let (user, _) = account
        .authenticate_device(&random_device_id())
        .await
        .expect("device auth");
    account
        .join_by_invite_code(code)
        .await
        .expect("join by invite");
    (account, user.id)
}

/// Whether the crew is in the crew list of the account: what the next start
/// of its client loads.
async fn has_crew(account: &NakamaClient, crew_id: &str) -> bool {
    account
        .list_user_groups()
        .await
        .expect("list_user_groups")
        .iter()
        .any(|c| c.id == crew_id)
}

/// "Leave crew" ends the membership with the group leave of Nakama
/// (`POST /v2/group/{id}/leave`), and the leave hooks do the rest.
///
/// Up to 0.8.1 the desktop client sent no leave. It closed the crew on the
/// screen, the user stayed in the member list and in the member count, and
/// the crew came back at the next start. The client depends on these
/// answers of the server, asserted here against a live one:
/// 1. The only owner of a crew with other members can leave. The server
///    hands the crew over first: an admin before a member, also when the
///    member joined first.
/// 2. After a leave the user is no member, and the crew is not in the crew
///    list of the user.
/// 3. The leave hook takes the user out of the voice room. The client sends
///    no `voice_leave` for a leave.
/// 4. The only member of a crew cannot leave. The client hides "Leave crew"
///    for that member, and keeps the crew open when the leave is refused.
#[test]
fn a_leave_ends_the_membership_and_an_only_owner_hands_the_crew_over() {
    if !e2e_enabled("a_leave_ends_the_membership_and_an_only_owner_hands_the_crew_over") {
        return;
    }

    rt().block_on(async {
        let (owner, crew_id, code) = crew_with_invite(false).await;
        let owner_id = owner.current_user_id().expect("owner id").to_string();
        // The member joins before the admin: rank must win over the join time.
        let (member, member_id) = joined_account(&code).await;
        let (admin, admin_id) = joined_account(&code).await;
        owner
            .change_crew_role(&crew_id, &admin_id, 1)
            .await
            .expect("the owner makes the second joiner an admin");
        member
            .rpc("voice_join", &serde_json::json!({ "crew_id": crew_id }))
            .await
            .expect("voice_join as a member");

        // 1. The only owner leaves a crew of three.
        owner
            .leave_group(&crew_id)
            .await
            .expect("the only owner can leave a crew that has other members");
        assert_eq!(
            crew_member_state(&admin, &crew_id, &admin_id).await,
            Some(GROUP_STATE_SUPERADMIN),
            "the admin is the owner now"
        );
        assert_eq!(
            crew_member_state(&admin, &crew_id, &member_id).await,
            Some(GROUP_STATE_MEMBER),
            "the member who joined first stays a member"
        );
        assert_eq!(
            crew_member_state(&admin, &crew_id, &owner_id).await,
            None,
            "the old owner is no member"
        );
        let members = admin
            .list_group_users(&crew_id)
            .await
            .expect("list_group_users");
        assert!(
            members.iter().all(|m| m.id != owner_id),
            "the member list has no row for the old owner. Got: {members:?}"
        );
        assert!(
            !has_crew(&owner, &crew_id).await,
            "the crew list of the old owner has no row for the crew, so the \
             next start does not open it"
        );

        // 2 and 3. A member leaves from a voice channel.
        assert_eq!(
            crew_voice_member_ids(&admin, &crew_id).await,
            vec![member_id.clone()],
            "before the leave the member sits in the voice channel"
        );
        member
            .leave_group(&crew_id)
            .await
            .expect("a member can leave the crew");
        assert_eq!(
            crew_member_state(&admin, &crew_id, &member_id).await,
            None,
            "after the leave the user is no member"
        );
        assert!(
            !has_crew(&member, &crew_id).await,
            "the crew list of the user has no row for the crew"
        );
        let crews = admin.list_user_groups().await.expect("list_user_groups");
        assert_eq!(
            crews
                .iter()
                .find(|c| c.id == crew_id)
                .map(|c| c.member_count),
            Some(1),
            "the member count is one: the new owner alone"
        );
        assert_eq!(
            crew_voice_member_ids(&admin, &crew_id).await,
            Vec::<String>::new(),
            "the leave hook takes the user out of the voice channel"
        );

        // 4. The only member: Nakama refuses, and the crew stays.
        let refused = admin
            .leave_group(&crew_id)
            .await
            .expect_err("the only member of a crew cannot leave it");
        assert!(
            refused.to_string().contains("last superadmin"),
            "the refusal names the cause. Got: {refused}"
        );
        assert_eq!(
            crew_member_state(&admin, &crew_id, &admin_id).await,
            Some(GROUP_STATE_SUPERADMIN),
            "the only member is still the owner"
        );
        assert!(has_crew(&admin, &crew_id).await, "the crew stays");

        let _ = member.delete_account().await;
        let _ = owner.delete_account().await;
        let _ = admin.delete_account().await;
    });
}

/// The only owner leaves, and the handover fails (spec 16 section 10.1). The
/// answer is code 14, and the user is still the owner. The client keeps the
/// crew open (`CrewLeaveFailed`). A retry hands the crew over and leaves.
///
/// The stack needs `MELLO_ENABLE_DEV_FAULT=1`. `scripts/e2e.sh` sets it.
#[test]
fn a_leave_with_a_failed_handover_is_refused_and_a_retry_leaves() {
    if !e2e_enabled("a_leave_with_a_failed_handover_is_refused_and_a_retry_leaves") {
        return;
    }

    rt().block_on(async {
        let (owner, crew_id, code) = crew_with_invite(false).await;
        let owner_id = owner.current_user_id().expect("owner id").to_string();
        let (joiner, joiner_id) = joined_account(&code).await;

        owner
            .rpc(
                "dev_fault",
                &serde_json::json!({"action": "fail_next_handover", "count": 1}),
            )
            .await
            .unwrap_or_else(|e| {
                panic!("dev_fault failed ({e}). The stack needs MELLO_ENABLE_DEV_FAULT=1")
            });

        let refused = owner
            .leave_group(&crew_id)
            .await
            .expect_err("a failed handover refuses the leave");
        assert_eq!(refused.server_code(), Some(14), "the refusal: {refused}");
        assert_eq!(
            crew_member_state(&joiner, &crew_id, &owner_id).await,
            Some(GROUP_STATE_SUPERADMIN),
            "the user is still the owner after a refusal"
        );
        assert!(has_crew(&owner, &crew_id).await, "the crew stays");

        owner
            .leave_group(&crew_id)
            .await
            .expect("the retry leaves the crew");
        assert_eq!(
            crew_member_state(&joiner, &crew_id, &joiner_id).await,
            Some(GROUP_STATE_SUPERADMIN),
            "the other member owns the crew after the leave"
        );
        assert!(!has_crew(&owner, &crew_id).await, "the old owner has left");

        let _ = owner.delete_account().await;
        let _ = joiner.delete_account().await;
    });
}

/// Joining a private crew through an invite link.
///
/// New crews are private (a closed Nakama group). `join_by_invite_code` called
/// `GroupUserJoin`, which only files a join request for a closed group. The
/// RPC reported success, but no one was asked to approve the request, and the
/// crew chat refused the user as a non-member (issue #83).
#[test]
fn a_second_user_can_join_a_private_crew_by_invite_code() {
    if !e2e_enabled("a_second_user_can_join_a_private_crew_by_invite_code") {
        return;
    }

    rt().block_on(async {
        let (owner, crew_id, code) = crew_with_invite(false).await;

        let mut joiner = NakamaClient::new(e2e_config());
        let (joiner_user, _) = joiner
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth for the joiner");

        let (joined_id, _) = joiner
            .join_by_invite_code(&code)
            .await
            .expect("join_by_invite_code must succeed for a valid code");
        assert_eq!(joined_id, crew_id, "the RPC must return the invited crew");
        assert_eq!(
            crew_member_state(&owner, &crew_id, &joiner_user.id).await,
            Some(GROUP_STATE_MEMBER),
            "a valid invite code must make the joiner a member of a private crew, \
             not a join request"
        );

        let (again_id, _) = joiner
            .join_by_invite_code(&code)
            .await
            .expect("an existing member following the link again must succeed");
        assert_eq!(again_id, crew_id);

        let _ = joiner.delete_account().await;
        let _ = owner.delete_account().await;
    });
}

/// A private crew takes no member through Nakama's own group join.
///
/// m3llo adds a member to a private crew by invite code. The server refuses
/// the group join of a private crew and makes no join request. The invite
/// code then makes that user a member.
#[test]
fn a_private_crew_refuses_the_group_join_and_takes_the_invite_code() {
    if !e2e_enabled("a_private_crew_refuses_the_group_join_and_takes_the_invite_code") {
        return;
    }

    rt().block_on(async {
        let (owner, crew_id, code) = crew_with_invite(false).await;

        let mut joiner = NakamaClient::new(e2e_config());
        let (joiner_user, _) = joiner
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth for the joiner");

        let refused = joiner
            .join_group(&crew_id)
            .await
            .expect_err("Nakama group join on a private crew must be refused");
        assert_eq!(refused.server_code(), Some(7), "the refusal: {refused}");
        assert_eq!(
            crew_member_state(&owner, &crew_id, &joiner_user.id).await,
            None,
            "the refused join must leave no join request"
        );

        joiner
            .join_by_invite_code(&code)
            .await
            .expect("join_by_invite_code must succeed after a refused group join");
        assert_eq!(
            crew_member_state(&owner, &crew_id, &joiner_user.id).await,
            Some(GROUP_STATE_MEMBER),
            "the invite code must make the user a member"
        );

        let _ = joiner.delete_account().await;
        let _ = owner.delete_account().await;
    });
}

/// Push tokens (spec 23): the core's register and unregister calls reach the
/// real handlers, a repeat register is an upsert, and invalid input is refused
/// rather than stored. The rows are server-only, so storage and the reassign
/// rule are checked by the Go tests and a manual SQL check, not here.
#[test]
fn push_token_register_and_unregister_round_trip() {
    if !e2e_enabled("push_token_register_and_unregister_round_trip") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");

        client
            .register_push_token("ab12cd34", "ios", "sandbox")
            .await
            .expect("register");
        client
            .register_push_token("ab12cd34", "ios", "sandbox")
            .await
            .expect("a repeat register is an upsert");
        client
            .register_push_token("not-hex", "ios", "production")
            .await
            .expect_err("a non-hex iOS token must be refused");
        client
            .register_push_token("ab12cd34", "ios", "staging")
            .await
            .expect_err("an unknown APNs environment must be refused");
        client
            .unregister_push_token("ab12cd34")
            .await
            .expect("unregister");

        let _ = client.delete_account().await;
    });
}

/// Sends a chat message, retrying while the channel join is still in flight
/// (the send reports NotConnected until Nakama's join reply arrives).
async fn send_when_joined(client: &NakamaClient, text: &str) {
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    loop {
        match client.send_chat_message(text).await {
            Ok(()) => return,
            Err(e) if std::time::Instant::now() < deadline => {
                let _ = e;
                tokio::time::sleep(std::time::Duration::from_millis(100)).await;
            }
            Err(e) => panic!("send {text:?}: {e}"),
        }
    }
}

async fn joined_member(code: &str, token: &str) -> (NakamaClient, String) {
    let mut member = NakamaClient::new(e2e_config());
    member
        .authenticate_device(&random_device_id())
        .await
        .expect("device auth for the member");
    member
        .join_by_invite_code(code)
        .await
        .expect("join by invite");
    let id = member.current_user_id().expect("member id").to_string();
    member
        .register_push_token(token, "ios", "sandbox")
        .await
        .expect("register");
    (member, id)
}

/// Spec 23 M4 + M5, against a live Nakama and a stub Worker:
/// 1. A mention of an offline member is posted to the Worker at once, with the
///    tap route and the mention shown as a name.
/// 2. A mention of a member whose desktop is open but inactive is held for the
///    grace period (`PUSH_DESKTOP_GRACE_SECS`, 2 s under `scripts/e2e.sh`).
///
/// 3. A plain message is posted to the Worker for an offline member, with one
///    collapse id for the crew. A member who turned Messages off for the crew
///    gets none (specs/features/LOOKING-TO-PLAY.md section 4). The notification
///    settings keep a choice across a second write.
///
/// 4. Looking to play: an ask reaches an offline member with the answer
///    buttons, an answer reaches the asker, a cancel reaches the member who is
///    in, and a member with Looking to play off gets none.
///
/// One test, because the scenarios share the stub's fixed port.
#[test]
fn mention_pushes_follow_the_delivery_rules() {
    if !e2e_enabled("mention_pushes_follow_the_delivery_rules") {
        return;
    }
    let (Some(port), Some(grace)) = (
        std::env::var("MELLO_E2E_PUSH_PORT")
            .ok()
            .and_then(|p| p.parse::<u16>().ok()),
        std::env::var("PUSH_DESKTOP_GRACE_SECS")
            .ok()
            .and_then(|g| g.parse::<u64>().ok()),
    ) else {
        println!("SKIP mention_pushes_follow_the_delivery_rules: run ./scripts/e2e.sh (push stub env unset)");
        return;
    };
    let grace = std::time::Duration::from_secs(grace);
    let captured = stub_push_worker(port);

    rt().block_on(async {
        let (mut owner, crew_id, code) = crew_with_invite(true).await;
        let (event_tx, _events) = std::sync::mpsc::channel();
        owner.connect_ws(event_tx).await.expect("owner socket");
        owner.join_crew_channel(&crew_id).await.expect("join crew channel");

        // 1. Offline member: pushed at once.
        let (offline, offline_id) = joined_member(&code, "feedface01").await;
        send_when_joined(&owner, &format!("gg <@{offline_id}> ping")).await;
        let got = next_send_of_type(&captured, "mention", "mention of an offline member").await;
        let head = got.head.to_ascii_lowercase();
        assert!(head.starts_with("post /send "), "{}", got.head);
        assert!(head.contains("authorization: bearer e2e-push-token"), "{}", got.head);
        let n = &got.body["notification"];
        assert_eq!(n["type"], "mention");
        assert_eq!(n["crew_id"], crew_id.as_str());
        assert!(!n["message_id"].as_str().unwrap_or_default().is_empty());
        assert!(!n["channel_id"].as_str().unwrap_or_default().is_empty());
        let text = n["body"].as_str().unwrap_or_default();
        assert!(text.contains("ping") && !text.contains("<@"), "mention must show as a name: {text}");
        assert_eq!(
            got.body["tokens"],
            serde_json::json!([{"token": "feedface01", "platform": "ios", "environment": "sandbox"}])
        );

        // 2. Desktop open but inactive: held for the grace period.
        let (mut away, away_id) = joined_member(&code, "feedface02").await;
        let (away_tx, _away_events) = std::sync::mpsc::channel();
        away.connect_ws(away_tx).await.expect("member socket");
        away.set_session_activity(false, "desktop").await.expect("report inactive");
        // Nakama handles one session's messages in order, so once this send
        // succeeds the activity report above has been processed.
        away.join_crew_channel(&crew_id).await.expect("member joins channel");
        send_when_joined(&away, "brb").await;

        let sent_at = std::time::Instant::now();
        owner
            .send_chat_message(&format!("<@{away_id}> you there?"))
            .await
            .expect("mention the away member");
        let got = next_send_of_type(&captured, "mention", "held mention of an inactive desktop user").await;
        assert_eq!(got.body["tokens"][0]["token"], "feedface02");
        let waited = got.at.duration_since(sent_at);
        assert!(
            waited >= grace.saturating_sub(std::time::Duration::from_millis(300)),
            "the push must wait for the grace period ({grace:?}), came after {waited:?}"
        );

        // 3. A plain message: pushed to a member with Messages on, not to a
        // member who turned it off.
        let (listener, _listener_id) = joined_member(&code, "feedface03").await;
        let (muted, _muted_id) = joined_member(&code, "feedface04").await;
        let prefs = muted
            .push_prefs_get(&crew_id)
            .await
            .expect("push_prefs_get");
        assert!(
            prefs.messages && prefs.looking_to_play,
            "a small crew starts with both settings on: {prefs:?}"
        );
        let prefs = muted
            .push_prefs_set(&crew_id, Some(false), None)
            .await
            .expect("push_prefs_set messages");
        assert!(!prefs.messages && prefs.looking_to_play, "{prefs:?}");
        // The second write changes the other setting and keeps the first.
        let prefs = muted
            .push_prefs_set(&crew_id, None, Some(false))
            .await
            .expect("push_prefs_set looking_to_play");
        assert!(!prefs.messages && !prefs.looking_to_play, "{prefs:?}");
        let prefs = muted
            .push_prefs_get(&crew_id)
            .await
            .expect("push_prefs_get after the writes");
        assert!(!prefs.messages && !prefs.looking_to_play, "{prefs:?}");

        owner
            .send_chat_message("anyone up for a game?")
            .await
            .expect("plain message");
        // The first two members had a message push in the last seconds, so
        // theirs is held. The new member gets this one at once.
        let got = loop {
            let got = next_send_of_type(&captured, "message", "plain message to an offline member").await;
            if got.body["tokens"][0]["token"] == "feedface03" {
                break got;
            }
        };
        let n = &got.body["notification"];
        assert_eq!(n["crew_id"], crew_id.as_str());
        assert!(
            n["message_id"].as_str().unwrap_or_default().starts_with("msg-"),
            "a message push carries the collapse id of the crew: {n}"
        );
        let text = n["body"].as_str().unwrap_or_default();
        // An e2e account has no display name, so the body is the text alone.
        assert!(text.ends_with("anyone up for a game?"), "body = {text}");

        let rest = common::drain_sends(&captured, std::time::Duration::from_secs(2)).await;
        assert!(
            rest.iter().all(|s| s.body["notification"]["type"] != "message"
                || s.body["tokens"][0]["token"] != "feedface04"),
            "a member who turned Messages off gets no push"
        );

        // 4. Looking to play. The member with token 03 asks the crew. The
        // offline member gets the ask with the answer buttons. The member who
        // turned Looking to play off (04) gets none. An answer reaches the
        // asker, and a cancel reaches the member who is in.
        let reply = listener
            .looking_to_play_ask(&crew_id, None, &[])
            .await
            .expect("looking_to_play_ask");
        let ask_id = reply.ask.ask_id.clone();
        assert!(!reply.joined && reply.ask.asks_everyone());
        assert_eq!(reply.ask.state, mello_core::looking_to_play::AskState::Open);
        let got = loop {
            let got = next_send_of_type(&captured, "looking_to_play", "ask to an offline member").await;
            if got.body["tokens"][0]["token"] == "feedface01" {
                break got;
            }
        };
        let n = &got.body["notification"];
        assert_eq!(n["category"], "LOOKING_TO_PLAY");
        assert_eq!(n["ask_id"], ask_id.as_str());
        assert!(n["message_id"].as_str().unwrap_or_default().starts_with("ltp-"), "{n}");
        assert!(n["ttl_seconds"].as_u64().unwrap_or(0) > 0, "{n}");
        assert!(
            n["body"].as_str().unwrap_or_default().ends_with("is looking to play"),
            "{n}"
        );

        let reply = offline
            .looking_to_play_answer(&ask_id, mello_core::looking_to_play::Answer::In)
            .await
            .expect("looking_to_play_answer");
        assert_eq!(reply.ask.state, mello_core::looking_to_play::AskState::On);
        let got = loop {
            let got = next_send_of_type(&captured, "looking_to_play_result", "answer to the asker").await;
            if got.body["tokens"][0]["token"] == "feedface03" {
                break got;
            }
        };
        let n = &got.body["notification"];
        assert!(n["body"].as_str().unwrap_or_default().ends_with("is in"), "{n}");
        assert!(n.get("category").is_none(), "a result has no buttons: {n}");
        assert!(n.get("silent").is_none(), "the first answer has a sound: {n}");

        let asks = owner
            .looking_to_play_list(&crew_id, &[])
            .await
            .expect("looking_to_play_list");
        assert!(asks.asks.iter().any(|a| a.ask_id == ask_id), "the open ask is in the list");

        let reply = listener
            .looking_to_play_cancel(&ask_id)
            .await
            .expect("looking_to_play_cancel");
        assert_eq!(reply.ask.closed_reason, "cancelled");
        let got = loop {
            let got = next_send_of_type(&captured, "looking_to_play_result", "cancel to the member who is in").await;
            if got.body["tokens"][0]["token"] == "feedface01" {
                break got;
            }
        };
        assert!(
            got.body["notification"]["body"].as_str().unwrap_or_default().ends_with("called it off"),
            "{}",
            got.body["notification"]
        );
        // The closed ask is still given out by id, for the card in history.
        let asks = owner
            .looking_to_play_list(&crew_id, std::slice::from_ref(&ask_id))
            .await
            .expect("looking_to_play_list by id");
        assert!(asks.asks.iter().any(|a| a.ask_id == ask_id && a.closed_reason == "cancelled"));

        let rest = common::drain_sends(&captured, std::time::Duration::from_secs(1)).await;
        assert!(
            rest.iter().all(|s| !s.body["notification"]["type"]
                .as_str()
                .unwrap_or_default()
                .starts_with("looking_to_play")
                || s.body["tokens"][0]["token"] != "feedface04"),
            "a member who turned Looking to play off gets no push of an ask"
        );

        for c in [offline, away, listener, muted, owner] {
            let _ = c.delete_account().await;
        }
    });
}

/// A clip never passes through Nakama. `clip_upload_url` presigns a PUT on
/// the stack's S3 server, the client PUTs the MP4 there, and
/// `clip_upload_complete` publishes the `media_url` that every crew member
/// plays without credentials. This runs that chain against the real local
/// storage service, so a server that rejects path-style SigV4, a bucket
/// without anonymous download, or a lost Range request fails here and not
/// in a crew feed.
#[test]
fn a_clip_uploads_to_local_storage_and_plays_back_without_credentials() {
    if !e2e_enabled("a_clip_uploads_to_local_storage_and_plays_back_without_credentials") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");

        let raw = client
            .rpc(
                "create_crew",
                &serde_json::json!({
                    "name": format!("E2E Clip Crew {:x}", rand::random::<u32>()),
                    "description": "created by the e2e suite",
                    "open": false,
                }),
            )
            .await
            .expect("create_crew");
        let body: serde_json::Value = serde_json::from_str(&raw).expect("create_crew returns JSON");
        let crew_id = body["crew_id"]
            .as_str()
            .unwrap_or_else(|| panic!("create_crew response has no crew_id. Got: {body}"))
            .to_string();

        let clip_id = format!("e2e-clip-{:x}", rand::random::<u64>());
        let posted = client
            .post_clip(&mello_core::crew_events::PostClipRequest {
                crew_id: crew_id.clone(),
                clip_id: clip_id.clone(),
                clip_type: "voice".into(),
                duration_seconds: 3.0,
                participants: Vec::new(),
                game: String::new(),
                local_path: String::new(),
                waveform: String::new(),
                width: 0,
                height: 0,
                has_voice: false,
            })
            .await
            .expect("post_clip");
        assert_eq!(posted.clip_id, clip_id, "post_clip must echo the clip id");

        let urls = client
            .clip_upload_url(&mello_core::crew_events::ClipUploadURLRequest {
                clip_id: clip_id.clone(),
                crew_id: crew_id.clone(),
            })
            .await
            .expect("clip_upload_url");
        assert!(
            !urls.upload_url.is_empty(),
            "clip_upload_url returned no URL: the stack has no S3 configured \
             (S3_ENDPOINT / S3_BUCKET / S3_ACCESS_KEY / S3_SECRET_KEY)"
        );
        assert!(
            urls.media_url
                .ends_with(&format!("/crews/{crew_id}/{clip_id}.mp4")),
            "media_url must be the public path of the object. Got: {}",
            urls.media_url
        );

        // Not a real MP4: the storage server stores bytes, it does not decode.
        let payload: Vec<u8> = (0..64 * 1024).map(|i| (i % 251) as u8).collect();
        let http = reqwest::Client::new();
        let put = http
            .put(&urls.upload_url)
            .header("Content-Type", "audio/mp4")
            .body(payload.clone())
            .send()
            .await
            .expect("PUT to the presigned URL");
        let status = put.status();
        if !status.is_success() {
            let text = put.text().await.unwrap_or_default();
            panic!("presigned PUT to local storage returned {status}: {text}");
        }

        let done = client
            .clip_upload_complete(&mello_core::crew_events::ClipUploadCompleteRequest {
                clip_id: clip_id.clone(),
                crew_id: crew_id.clone(),
            })
            .await
            .expect("clip_upload_complete");
        assert!(done.success, "clip_upload_complete must report success");
        assert_eq!(done.media_url, urls.media_url);

        // Playback: no credentials, the bytes that went up, and a Range
        // request for a seek.
        let get = http
            .get(&urls.media_url)
            .send()
            .await
            .expect("GET media_url");
        assert_eq!(
            get.status(),
            200,
            "the clip must be public at its media_url (anonymous download on the bucket)"
        );
        let got = get.bytes().await.expect("media body");
        assert_eq!(
            got.as_ref(),
            payload.as_slice(),
            "the public object must hold the uploaded bytes"
        );

        let range = http
            .get(&urls.media_url)
            .header("Range", "bytes=0-1023")
            .send()
            .await
            .expect("ranged GET media_url");
        assert_eq!(range.status(), 206, "a player seeks with Range requests");
        assert_eq!(range.bytes().await.expect("range body").len(), 1024);

        let _ = client.delete_account().await;
    });
}

/// The SQL crew ledger store keeps the store contract on the real tables.
/// `dev_ledger_selftest` runs the same conformance function that `go test`
/// runs on the in-memory store (TESTING.md). It writes
/// only to two throwaway crew ids and deletes them at the end.
///
/// The stack needs `MELLO_ENABLE_DEV_FAULT=1`. `scripts/e2e.sh` sets it.
#[test]
fn the_sql_ledger_store_passes_the_conformance_run() {
    if !e2e_enabled("the_sql_ledger_store_passes_the_conformance_run") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");

        let raw = client
            .rpc("dev_ledger_selftest", &serde_json::json!({}))
            .await
            .unwrap_or_else(|e| {
                panic!(
                    "dev_ledger_selftest failed: {e}. If it says disabled, the stack needs \
                     MELLO_ENABLE_DEV_FAULT=1 (scripts/e2e.sh sets it)"
                )
            });
        let body: serde_json::Value =
            serde_json::from_str(&raw).expect("dev_ledger_selftest returns JSON");
        assert_eq!(body["success"], true, "selftest response: {body}");
        assert_eq!(body["store"], "sql", "selftest response: {body}");

        // A second run finds no rows of the first one in its way.
        client
            .rpc("dev_ledger_selftest", &serde_json::json!({}))
            .await
            .expect("second dev_ledger_selftest");

        let _ = client.delete_account().await;
    });
}

/// The SQL statements of the deletion rules run on the real tables of Nakama:
/// the chat messages and the reactions of a crew, and the membership rows of
/// an account that is gone. `go test` runs the rules on fakes, so this is the
/// only run that sees the statements. `dev_delete_selftest` makes its own
/// rows under new random ids and deletes them at the end.
///
/// The negative cases are in the RPC: a direct message and the messages of
/// another crew stay, the reactions of another crew are not returned, and the
/// membership rows of an account that exists stay. It also checks what the
/// group update of Nakama does with empty values.
///
/// The stack needs `MELLO_ENABLE_DEV_FAULT=1`. `scripts/e2e.sh` sets it.
#[test]
fn the_sql_of_the_deletion_rules_passes_its_selftest() {
    if !e2e_enabled("the_sql_of_the_deletion_rules_passes_its_selftest") {
        return;
    }

    rt().block_on(async {
        let mut client = NakamaClient::new(e2e_config());
        client
            .authenticate_device(&random_device_id())
            .await
            .expect("device auth");

        for run in 0..2 {
            let raw = client
                .rpc("dev_delete_selftest", &serde_json::json!({}))
                .await
                .unwrap_or_else(|e| {
                    panic!(
                        "dev_delete_selftest run {run} failed: {e}. If it says disabled, the \
                         stack needs MELLO_ENABLE_DEV_FAULT=1 (scripts/e2e.sh sets it)"
                    )
                });
            let body: serde_json::Value =
                serde_json::from_str(&raw).expect("dev_delete_selftest returns JSON");
            assert_eq!(body["success"], true, "selftest response: {body}");
            let checks = body["checks"].as_array().expect("checks");
            assert_eq!(checks.len(), 7, "selftest checks: {body}");
        }

        let _ = client.delete_account().await;
    });
}
