use thiserror::Error;

#[derive(Debug, Error)]
pub enum Error {
    #[error("Not connected")]
    NotConnected,

    #[error("Authentication failed: {0}")]
    AuthFailed(String),

    #[error("Already in a crew")]
    AlreadyInCrew,

    #[error("Crew not found: {0}")]
    CrewNotFound(String),

    #[error("HTTP error: {0}")]
    Http(#[from] reqwest::Error),

    #[error("WebSocket error: {0}")]
    WebSocket(String),

    #[error("JSON error: {0}")]
    Json(#[from] serde_json::Error),

    #[error("Server error: {0}")]
    Server(String),

    #[error("Internal error: {0}")]
    Internal(String),
}

pub type Result<T> = std::result::Result<T, Error>;

impl Error {
    /// The gRPC status code of a Nakama error response, when this error
    /// carries one. Nakama answers a failed RPC with a JSON body such as
    /// `{"code":5,"message":"..."}`.
    pub fn server_code(&self) -> Option<i64> {
        let Error::Server(body) = self else {
            return None;
        };
        serde_json::from_str::<serde_json::Value>(body)
            .ok()?
            .get("code")?
            .as_i64()
    }

    /// The `message` of a Nakama error response, when this error carries one.
    pub fn server_message(&self) -> Option<String> {
        let Error::Server(body) = self else {
            return None;
        };
        serde_json::from_str::<serde_json::Value>(body)
            .ok()?
            .get("message")?
            .as_str()
            .map(str::to_string)
    }

    /// The stable error code of a Nakama error response: a `message` that is
    /// an upper-case token, such as `NAME_TAKEN` or `SFU_UNAVAILABLE`. The
    /// client maps the token to a message in the UI language. `None` for any
    /// other error, and for the English message of an old server.
    pub fn server_token(&self) -> Option<String> {
        self.server_message().filter(|m| is_server_token(m))
    }
}

/// Whether `message` is a stable server error token: upper-case ASCII
/// letters, digits and underscores, with a letter first.
pub fn is_server_token(message: &str) -> bool {
    let mut chars = message.chars();
    chars.next().is_some_and(|c| c.is_ascii_uppercase())
        && chars.all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_')
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_token_message_is_a_server_token() {
        let e = Error::Server(r#"{"code":6,"message":"NAME_TAKEN"}"#.into());
        assert_eq!(e.server_token().as_deref(), Some("NAME_TAKEN"));
        let old = Error::Server(r#"{"code":6,"message":"name taken"}"#.into());
        assert_eq!(old.server_token(), None, "the English of an old server");
        assert_eq!(Error::Server("<html>".into()).server_token(), None);
        assert!(is_server_token("SFU_UNAVAILABLE"));
        assert!(!is_server_token("_X") && !is_server_token("") && !is_server_token("A B"));
    }
}
