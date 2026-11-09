//! Name helpers that work for every script.
//!
//! A name can use any script: Latin, Cyrillic, Arabic, CJK, emoji. Initials
//! and monograms take whole graphemes (what a reader sees as one character),
//! so a flag, a skin-tone emoji or a ZWJ sequence is never cut in two.
//! Server limits count Unicode scalar values (`char`), not graphemes: see
//! `specs/04-BACKEND.md`, "Text rules for names".

use unicode_segmentation::UnicodeSegmentation;

/// The initials of a name, for an avatar or a tile without an image.
///
/// The name is split on Unicode white space. Two or more words give the first
/// grapheme of the first word and the first grapheme of the last word. One
/// word gives its first two graphemes. An emoji, also a flag, a skin-tone or
/// a ZWJ sequence, is one grapheme. Cased letters become upper case; CJK,
/// Arabic and emoji stay as they are.
///
/// Returns an empty string for a name with no words. Each caller chooses its
/// own fallback for that case.
///
/// ```
/// use mello_core::names::initials;
/// assert_eq!(initials("José María"), "JM");
/// assert_eq!(initials("小明"), "小明");
/// assert_eq!(initials("🇸🇪 Nils"), "🇸🇪N");
/// assert_eq!(initials("   "), "");
/// ```
pub fn initials(name: &str) -> String {
    let words: Vec<&str> = name.split_whitespace().collect();
    let picked: Vec<&str> = match words.as_slice() {
        [] => return String::new(),
        [one] => one.graphemes(true).take(2).collect(),
        [first, .., last] => first
            .graphemes(true)
            .take(1)
            .chain(last.graphemes(true).take(1))
            .collect(),
    };
    picked.iter().map(|g| g.to_uppercase()).collect()
}

/// The extended grapheme clusters of `s`, in order. A grapheme is what a
/// reader sees as one character: "é" written as `e` plus a combining accent,
/// a flag, or a ZWJ emoji sequence is one grapheme.
pub fn graphemes(s: &str) -> impl Iterator<Item = &str> {
    s.graphemes(true)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn two_words_give_the_first_letters_of_the_first_and_last_word() {
        assert_eq!(initials("Alice Baker"), "AB");
        assert_eq!(initials("Olena Shevchenko"), "OS");
        assert_eq!(initials("Олена Шевченко"), "ОШ");
        assert_eq!(initials("José María"), "JM");
        assert_eq!(initials("Anna Maria Larsson"), "AL");
        assert_eq!(initials("タナカ ユウ"), "タユ");
    }

    #[test]
    fn one_word_gives_its_first_two_graphemes() {
        assert_eq!(initials("alice"), "AL");
        assert_eq!(initials("k0ji_tech"), "K0");
        assert_eq!(initials("Jürgen"), "JÜ");
        assert_eq!(initials("小明"), "小明");
        assert_eq!(initials("小明的朋友们"), "小明");
        assert_eq!(initials("مريم"), "مر");
        assert_eq!(initials("X"), "X");
    }

    #[test]
    fn an_emoji_is_one_grapheme() {
        assert_eq!(initials("🔥Max🔥"), "🔥M");
        assert_eq!(initials("🇸🇪 Nils"), "🇸🇪N");
        assert_eq!(initials("🇸🇪🇳🇴"), "🇸🇪🇳🇴");
        // Skin tone and ZWJ sequences stay whole.
        assert_eq!(initials("👍🏽 ok"), "👍🏽O");
        let family = "👨\u{200D}👩\u{200D}👧";
        assert_eq!(initials(&format!("{family} crew")), format!("{family}C"));
        assert_eq!(initials(&format!("{family}x")), format!("{family}X"));
    }

    #[test]
    fn a_combining_accent_stays_with_its_letter() {
        // "é" as "e" + U+0301 is one grapheme.
        assert_eq!(initials("e\u{301}lise"), "E\u{301}L");
    }

    #[test]
    fn any_unicode_white_space_splits_words() {
        assert_eq!(initials("小明\u{3000}王"), "小王");
        assert_eq!(initials("  Max\tPower  "), "MP");
    }

    #[test]
    fn no_words_give_an_empty_string() {
        assert_eq!(initials(""), "");
        assert_eq!(initials(" \t\u{3000} "), "");
    }

    #[test]
    fn graphemes_keep_sequences_whole() {
        let g: Vec<&str> = graphemes("🇸🇪e\u{301}").collect();
        assert_eq!(g, vec!["🇸🇪", "e\u{301}"]);
    }
}
