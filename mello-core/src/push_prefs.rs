//! The push switches of a member for one crew (plans/LOOKING-TO-PLAY.md
//! section 4). The server holds them: a phone push must obey them when no
//! client runs.

use serde::{Deserialize, Serialize};

/// The switches as they apply now. The server fills in the default of a
/// switch that the member never set.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct PushPrefs {
    pub crew_id: String,
    /// A plain crew message sends a phone push.
    pub messages: bool,
    /// A Looking to play ask sends a phone push, shows its toast and plays
    /// its sound.
    pub looking_to_play: bool,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn decodes_the_server_reply() {
        let prefs: PushPrefs =
            serde_json::from_str(r#"{"crew_id":"c1","messages":false,"looking_to_play":true}"#)
                .expect("the reply of push_prefs_get decodes");
        assert_eq!(
            prefs,
            PushPrefs {
                crew_id: "c1".into(),
                messages: false,
                looking_to_play: true
            }
        );
    }
}
