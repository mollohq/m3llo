//! "Leave crew" (`Client::handle_leave_crew`) against a Nakama stand-in that
//! records each request.
//!
//! Up to 0.8.1 the handler closed the crew in the client and sent no leave
//! to the server. The user stayed a member: in the member list, in the
//! member count, and in the crew list of the next start.

use std::sync::atomic::{AtomicBool, AtomicU8};
use std::sync::{Arc, Mutex};

use tokio::io::{AsyncReadExt, AsyncWriteExt};

use super::Client;
use crate::config::Config;
use crate::events::Event;
use crate::voice::VoiceManager;

/// The request lines a stand-in saw, as `METHOD path`, in arrival order.
type Seen = Arc<Mutex<Vec<String>>>;

/// A Nakama stand-in on a local port. It records the request line of each
/// request, answers with `status` and `body`, and closes the connection.
async fn nakama_answering(status: &'static str, body: &'static str) -> (Config, Seen) {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
        .await
        .expect("bind a local port");
    let port = listener
        .local_addr()
        .expect("listener has an address")
        .port();
    let seen: Seen = Arc::new(Mutex::new(Vec::new()));
    let recorder = Arc::clone(&seen);
    tokio::spawn(async move {
        loop {
            let Ok((mut socket, _)) = listener.accept().await else {
                return;
            };
            let recorder = Arc::clone(&recorder);
            tokio::spawn(async move {
                let mut head: Vec<u8> = Vec::new();
                while !head.windows(4).any(|w| w == b"\r\n\r\n") {
                    let mut chunk = [0u8; 4096];
                    match socket.read(&mut chunk).await {
                        Ok(0) | Err(_) => return,
                        Ok(n) => head.extend_from_slice(&chunk[..n]),
                    }
                }
                let head = String::from_utf8_lossy(&head).into_owned();
                let mut parts = head.lines().next().unwrap_or_default().split_whitespace();
                let method = parts.next().unwrap_or_default();
                let path = parts.next().unwrap_or_default();
                recorder
                    .lock()
                    .expect("recorder lock")
                    .push(format!("{method} {path}"));
                let response = format!(
                    "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = socket.write_all(response.as_bytes()).await;
            });
        }
    });
    let config = Config {
        nakama_host: "127.0.0.1".into(),
        nakama_port: port,
        nakama_ssl: false,
        ..Config::default()
    };
    (config, seen)
}

/// A port with nothing on it: the connect is refused, as with no network.
fn nakama_down() -> Config {
    let port = std::net::TcpListener::bind("127.0.0.1:0")
        .expect("bind a local port")
        .local_addr()
        .expect("listener has an address")
        .port();
    Config {
        nakama_host: "127.0.0.1".into(),
        nakama_port: port,
        nakama_ssl: false,
        ..Config::default()
    }
}

/// A signed-in client with "crew-1" active.
fn client_in_crew(config: Config) -> (Client, std::sync::mpsc::Receiver<Event>) {
    let (event_tx, events) = std::sync::mpsc::channel();
    let voice = VoiceManager::with_test_audio_backend(event_tx.clone());
    let mut client = Client::with_voice(
        config,
        event_tx,
        voice,
        Arc::new(Mutex::new(None)),
        Arc::new(Mutex::new(None)),
        Arc::new(AtomicBool::new(false)),
        Arc::new(AtomicU8::new(0)),
        false,
        false,
    );
    client
        .nakama
        .set_test_session("test-session", Some("crew-1"));
    (client, events)
}

/// ★ Regression: "Leave crew" ends the membership on the server. The leave
/// is the only request: the leave hook of the server does the voice cleanup,
/// so the client sends no `voice_leave`. Then the client closes the crew.
#[tokio::test(flavor = "current_thread")]
async fn leave_crew_ends_the_membership_on_the_server() {
    let (config, seen) = nakama_answering("200 OK", "{}").await;
    let (mut client, events) = client_in_crew(config);

    client.handle_leave_crew().await;

    assert_eq!(
        *seen.lock().expect("recorder lock"),
        vec!["POST /v2/group/crew-1/leave".to_string()],
        "the client asks the server to end the membership, and sends nothing else"
    );
    let events: Vec<Event> = events.try_iter().collect();
    assert!(
        events
            .iter()
            .any(|e| matches!(e, Event::CrewLeft { crew_id } if crew_id == "crew-1")),
        "the UI learns that the user left: {events:?}"
    );
    assert!(
        events
            .iter()
            .any(|e| matches!(e, Event::VoiceStateChanged { in_call: false, .. })),
        "the call of the crew ends in the client: {events:?}"
    );
    assert!(
        !events
            .iter()
            .any(|e| matches!(e, Event::CrewLeaveFailed { .. })),
        "a leave that the server took is not a failure: {events:?}"
    );
    assert_eq!(
        client.nakama.active_crew_id(),
        None,
        "core has no active crew after the leave"
    );
}

/// A leave that the server refuses changes nothing in the client: the crew
/// stays active, the call stays up, and the UI gets the failure with the
/// answer of the server.
async fn assert_a_refused_leave_changes_nothing(
    status: &'static str,
    body: &'static str,
    cause: &str,
) {
    let (config, seen) = nakama_answering(status, body).await;
    let (mut client, events) = client_in_crew(config);

    client.handle_leave_crew().await;

    assert_eq!(
        *seen.lock().expect("recorder lock"),
        vec!["POST /v2/group/crew-1/leave".to_string()],
        "one leave request, and no retry"
    );
    let events: Vec<Event> = events.try_iter().collect();
    assert!(
        matches!(
            events.as_slice(),
            [Event::CrewLeaveFailed { crew_id, reason }]
                if crew_id == "crew-1" && reason.contains(cause)
        ),
        "the failure is the only event, so the call and the crew stay: {events:?}"
    );
    assert_eq!(
        client.nakama.active_crew_id(),
        Some("crew-1"),
        "the crew stays the active crew"
    );
}

/// Nakama refuses the leave of the only member of a crew (code 3). The
/// crew menu hides "Leave crew" for that member, but a member count can be
/// old.
#[tokio::test(flavor = "current_thread")]
async fn a_refused_leave_keeps_the_crew_and_the_call() {
    assert_a_refused_leave_changes_nothing(
        "400 Bad Request",
        r#"{"code":3,"message":"Cannot leave group when you are the last superadmin."}"#,
        "last superadmin",
    )
    .await;
}

/// The only owner leaves, and the server cannot hand the crew to another
/// member: it refuses the leave (code 14). The user is still the owner.
#[tokio::test(flavor = "current_thread")]
async fn a_leave_with_a_failed_handover_keeps_the_crew_and_the_call() {
    assert_a_refused_leave_changes_nothing(
        "503 Service Unavailable",
        r#"{"code":14,"message":"You did not leave the crew. The crew could not be handed to another member. Try again in a moment."}"#,
        "could not be handed",
    )
    .await;
}

/// The server does not answer: the user is still a member, so the client
/// keeps the crew.
#[tokio::test(flavor = "current_thread")]
async fn a_leave_with_no_answer_keeps_the_crew() {
    let (mut client, events) = client_in_crew(nakama_down());

    client.handle_leave_crew().await;

    let events: Vec<Event> = events.try_iter().collect();
    assert!(
        matches!(
            events.as_slice(),
            [Event::CrewLeaveFailed { crew_id, .. }] if crew_id == "crew-1"
        ),
        "the failure is the only event: {events:?}"
    );
    assert_eq!(client.nakama.active_crew_id(), Some("crew-1"));
}
