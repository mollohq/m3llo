//! `TextRules.fit` (client/ui/theme.slint): the name fields stop at the
//! length limits of the server (backend/nakama/data/modules/text_rules.go).

use crate::app_context::AppContext;
use slint::ComponentHandle as _;

pub(super) fn wire(ctx: &AppContext) {
    ctx.app
        .global::<crate::TextRules>()
        .on_fit(|text, max| fit(&text, max).into());
}

/// `text` cut to `max` characters. A character is a Unicode scalar value,
/// which is how the server counts. A `max` of 0 or less has no limit.
pub(crate) fn fit(text: &str, max: i32) -> String {
    let Ok(max) = usize::try_from(max) else {
        return text.to_string();
    };
    if max == 0 {
        return text.to_string();
    }
    match text.char_indices().nth(max) {
        Some((end, _)) => text[..end].to_string(),
        None => text.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::fit;

    #[test]
    fn a_text_at_or_under_the_limit_is_kept() {
        assert_eq!(fit("Shadow Syndicate", 24), "Shadow Syndicate");
        assert_eq!(
            fit("abcdefghijklmnopqrstuvwx", 24),
            "abcdefghijklmnopqrstuvwx"
        );
        assert_eq!(fit("", 24), "");
    }

    #[test]
    fn a_longer_text_is_cut_to_the_limit() {
        assert_eq!(
            fit("abcdefghijklmnopqrstuvwxyz", 24),
            "abcdefghijklmnopqrstuvwx"
        );
    }

    #[test]
    fn the_limit_counts_characters_not_bytes() {
        // 25 characters, 50 bytes: the server counts runes, so 24 stay.
        let name = "ö".repeat(25);
        assert_eq!(fit(&name, 24), "ö".repeat(24));
        // An emoji is one character of four bytes.
        assert_eq!(fit("🎮🎮🎮", 2), "🎮🎮");
    }

    #[test]
    fn zero_or_less_has_no_limit() {
        assert_eq!(fit("abc", 0), "abc");
        assert_eq!(fit("abc", -1), "abc");
    }
}
