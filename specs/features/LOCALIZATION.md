# Localization

The client UI is in English in the source. Translations are gettext `.po`
catalogs. The build bundles them into the binary. At startup the client
selects one language.

This spec gives the design, the rules for a new string, the checks, and how
to add a language.

## 1. Design

| Part | Where | What it does |
|---|---|---|
| `@tr()` in Slint | `client/ui/**/*.slint`, `hud/ui/**/*.slint` | Marks each user-visible string. English is the source text. |
| `Tr` global | `client/ui/tr.slint` | Holds each message that Rust shows. Rust calls it and gets the translated text. |
| Template | `client/translations/mello-client.pot` | All messages. `scripts/l10n-extract.sh` writes it. It is committed. |
| Catalogs | `client/translations/<lang>/LC_MESSAGES/mello-client.po` | One per language. A translator writes it from the template. |
| Bundle | `client/build.rs`, `client/build_l10n.rs` | Copies the catalogs to `OUT_DIR/translations`, adds the pseudo language in debug builds, and gives that folder to `slint_build`. |
| Selection | `client/src/locale.rs` | Chooses the language at startup and selects it. |
| Lint | `client/src/i18n_lint.rs` | Fails `cargo test` on a user-visible string outside `@tr()`. |

Decisions:

- The gettext domain is the crate name, `mello-client`. Slint reads
  `<path>/<lang>/LC_MESSAGES/<domain>.po`.
- There is no default translation context
  (`DefaultTranslationContext::None` in `build.rs`,
  `--no-default-translation-context` in the extractor). One English string
  is one message, in all files.
- There is no gettext crate and no gettext library. Slint compiles the
  catalogs into the binary as string tables.
- Rust does not own user-visible sentences. Rust gets them from `Tr`.

## 2. How Slint handles translations

These facts come from the Slint 1.18.1 source. The design depends on them.

| Fact | Source |
|---|---|
| A bundle folder with no catalog stops the build ("No translations found"). `build.rs` then bundles nothing, and the UI shows the English of the source. | `i-slint-compiler/translations.rs`, `load_translations` |
| A message that a catalog does not have, or has with an empty `msgstr`, gets `None`. At run time `None` shows the English text. A plural uses the English rule then. | `i-slint-compiler/translations.rs`, `lower_translate_call`; `i-slint-core/translations.rs`, `translate_from_bundle` |
| The generated `new()` of each exported component calls `set_bundled_languages`. Only the first call on a thread has an effect: it stores the list and selects the OS language. | `i-slint-compiler/generator/rust.rs`, `init_bundle_translations`; `i-slint-core/translations.rs`, `set_bundled_languages` |
| `select_bundled_translation` fails before the first component exists on the thread. `""` or `"en"` selects the source language. | `i-slint-core/translations.rs`, `select_bundled_translation` |
| The OS match is exact first, then the base language (the part before `-`, `_` or `@`). | `i-slint-core/translations.rs`, `language_index_from_sys_locale` |
| The selection is per thread. Each test thread starts with the OS language. | the same file, `GLOBAL_CONTEXT` |
| Language folders are sorted by name. The language list is the same on each machine. | `i-slint-compiler/translations.rs` |

## 3. Add a string in Slint

1. Write the English text in `@tr()`:
   `text: @tr("Join crew");`
2. Write a value as a `{}` place. Do not join a sentence with `+`:
   - Wrong: `"Added by " + name`
   - Right: `@tr("Added by {}", name)`
   Use `{0}`, `{1}` when a translator must change the order of two values.
3. Run `./scripts/l10n-extract.sh` and commit the `.pot` with the change.

Put these in `@tr()`: `text`, `title`, `placeholder`, `accessible-label`,
`accessible-description`, a tooltip, a `label` or `a11y-label` of a
component, and the default value of a property that is shown.

Do not put these in `@tr()`: user data (names, crew names, chat), ids,
enum-like values that the code compares, font names, paths, the brand name
`m3llo`. When the lint reports one of these, add
`// i18n: none (<reason>)` on its line or on the line above.

## 4. Add a string that Rust needs

1. Add a member to the `Tr` global in `client/ui/tr.slint`:
   - A message with no value: `out property <string> connection-lost: @tr("Connection lost");`
   - A message with a value: a `pure public function`:
     ```slint
     pure public function update-bytes(downloaded: string, total: string) -> string {
         @tr("{} / {} MB", downloaded, total)
     }
     ```
2. Call it from Rust through the component:
   `app.global::<Tr>().get_connection_lost()`,
   `dialog.global::<Tr>().invoke_update_bytes(a, b)`.
3. Name the member by its subject (`update-unavailable`), and group the
   members by screen.

Rules:

- An argument is a value (a name, a number, a version). It is never a part
  of a sentence. Never join two messages in Rust.
- Rust formats a number before it gives it to `Tr` when the format is not
  the default (`{:.1}`).
- A message that Rust sets in a property is not translated again when the
  language changes. The language is selected once, at startup.

Server text (an error reason, a protocol notice) is data. It is not
translated in the client.

`mello-core` does not make a user sentence. It sends a typed value, and the
client writes the message from `Tr`:

| Core value | Client function |
|---|---|
| `events::AuthFailure` (sign-in, link, onboarding) | `handlers::auth::auth_failure_text` |
| `events::StreamFailure` (a stream start or a watch) | `handlers::streaming::stream_failure_text` |
| `events::GiftFailure` (a crew gift) | `callbacks::gift_failure_text` |
| `events::ClipPlayClose` (the game clip player) | `game_clip_player` |
| `ProtocolMismatch { required, actual }` | `handlers::mod` |
| `CaptureSource { primary, monitor_index }` (a monitor) | `handlers::streaming` ("Display 2") |

A value that holds server text (`AuthFailure::Server`, `GiftFailure::Server`)
or the error text of a native step (`StreamFailure::Detail`) shows that text
as it is.

### Server text with a kind and values

The rule: the server sends no new user sentence. It sends a kind and values,
and the client writes the sentence from `Tr`. The feed cards work this way
already (`handlers::clip::extract_title`).

Old clients and the mobile app read the English fields. These fields stay,
with their English content. The server adds the new fields next to them.

| Payload | Kind and values | English field that stays | Client |
|---|---|---|---|
| `crew_catchup` (`CatchupResponse`) | `top_events[]`: `type`, `data`, and `actor_name` for a moment and a clip | `catchup_text` | `handlers::catchup::caption` |
| A weekly recap (`awards[]`, `RecapAward`) | `kind`, `value` (the count) for `grinder`, `heater`, `skid` | `title`, `detail` | `handlers::clip::extract_recap_awards` |

Old servers and old stored data:

- The catch-up: when `has_events` is true and `top_events` is empty, the
  client shows `catchup_text` as it is. When `has_events` is false, the
  client shows the quiet line from `Tr`. An `actor_name` that is not there
  shows as `someone`.
- A recap is stored with its English awards. A recap stored before `value`
  has no `value`, and the client shows the stored `title` and `detail`.

The English line of the client is the English line of the server, word for
word. The flow tests compare them (`the_catchup_line_reads_as_the_server_sentence`,
`recap_game_awards_read_as_the_stored_english`). The catch-up line joins its
parts with `list_and_serial` ("A, B, and C"). This list has a comma before
"and", and `list_and` does not.

### Text outside the Slint window

The tray menu, the macOS menu bar, the taskbar buttons, OS notifications and
the Linux shortcut descriptions are native text. Rust reads them from `Tr`
on the UI thread, after the main window exists, and passes them in:

| Text | Read in | Given to |
|---|---|---|
| Tray menu | `lib.rs` (`platform::TrayText::from_tr`) | `StatusItem::new`, the Linux tray thread |
| macOS menu bar | `lib.rs` | `platform::macos::build_menu_bar(tr)`, `edit_shortcuts::edit_item_title` |
| Taskbar buttons (Windows) | `TaskbarToolbar::try_init` | the button tooltips |
| Notifications | the caller, on the UI thread | `notifications::*` |
| Linux shortcut descriptions | `lib.rs` | `linux_shortcuts::set_descriptions`, before the portal thread starts |
| HUD "LIVE" badge | `hud_state_builder` | `HudStreamCard::live_label` |

The overlay in the client gives `locale::ui_language()` to DirectWrite. The
`hud` process gets no language from the client: it uses the OS language and
an English badge when the label is empty.

## 5. Plurals and contexts

A plural is one message with the Slint plural form. `{n}` is the count:

```slint
@tr("{n} clip" | "{n} clips" % count)
@tr("clip" | "clips" % count)        // a unit next to a number (StatValue)
```

Do not write `count == 1 ? "clip" : "clips"`. Many languages have more than
two forms, and the catalog gives the rule (`Plural-Forms`).

A context is written only when one English string has two meanings:
`@tr("verb" => "Open")` and `@tr("state" => "Open")`. The context is in
the `.pot` as `msgctxt`.

Contexts in use: `verb`, `noun`, `state`, `unit`, `title`, `format`, `list`,
`streak`, `short month`, `menu` (a menu name of the macOS menu bar, "Edit")
and `Riot region` (the region ids of the Riot link, "europe").

A sentence that Slint must show in parts (an avatar or a link in the
middle) gets a note for the translator on the line above the `@tr()`. The
extractor keeps the last comment line only:
`// i18n: one sentence in parts: Made with [link: Slint]`.

## 6. Upper case

An upper-case label is written in upper case in the source string:
`@tr("SAVE")`, not `@tr("Save").to-uppercase()`.

`to-uppercase()` on a translated text is wrong in some languages. German
`ß` and the Turkish dotted `i` change, and CJK text has no case.

When the design shows user data in upper case, `to-uppercase()` stays on the
data only:

```slint
Kicker { text: @tr("ADDED BY {}", e.added-by.to-uppercase()); }
```

A control often has an upper-case label and a sentence-case
`accessible-label` (`"SAVE"` and `"Save"`). These are two messages.

When Rust sets a message that Slint shows in upper case, Rust sets the
upper-case form too, from a second `Tr` member (`quest-action-upload` and
`quest-action-upload-upper`). The struct has a field for it
(`title-upper`, `label-upper`, `timestamp-upper`), and the `.slint` file shows
that field with no `to-uppercase()`.

## 7. The lint and its list

`client/src/i18n_lint.rs` reads each `.slint` file under `client/ui` and
`hud/ui`, not `previews/`. A string literal with a letter in it must be in
`@tr()`. The lint does not report:

| Literal | Example |
|---|---|
| An import path, an `@image-url()`, a `debug()` argument | `from "theme.slint"` |
| The value of a property with `font` in its name | `font-family: "Oxanium"` |
| An operand of `==` or `!=` | `mode == "voice_activity"` |
| A lower-case word with no space | `tone: "line"` |
| A string with no letter | `"0%"`, `" -> "` |
| Text in a component whose name ends in `Preview` | mock data |
| A line with `// i18n: none (<reason>)`, or the line under it | the brand name |
| A file with `// i18n: none-file (<reason>)` in its first ten lines | `debug_panel.slint` (developer panel) |

The file marker is for a developer tool that is not translated. Its text
stays in English and is not in `@tr()`, so the file has no message in the
`.pot`. A file marker with no reason fails the test.

The UI is converted file by file. `client/src/i18n_pending.txt` lists the
files that are not converted yet, as `<count> <path>`. The count is the
number of untranslated strings that the lint finds in the file.

The test fails when:

- a file that is not on the list has an untranslated string;
- a listed file has more strings than its count;
- a listed file has fewer strings than its count, or none, or does not
  exist. Write the new count, or delete the line.

So the list can only shrink. When a change converts a file, it deletes the
line of that file.

A second test, `the_pot_has_the_messages_of_the_sources`, compares the
messages of the `.pot` with the `@tr()` calls in the sources. A stale `.pot`
fails `cargo test`. This test does not need the extractor.

`./scripts/l10n-extract.sh --check` compares the full file, references
included. It needs `slint-tr-extractor`, so it is not in `check.sh`.

## 8. The pseudo language

The pseudo language `qps` shows the layout with longer, accented text. It
needs no translator.

- It exists in debug builds only. `build.rs` makes it when `PROFILE` is
  `debug`. A release build does not have it.
- `client/build_l10n.rs` generates it from the `.pot` into
  `OUT_DIR/translations/qps`. It is never written to the source tree, and a
  folder `client/translations/qps` stops the build.
- Each letter gets an accent, and each string gets about 30 % more length:
  `Create crew` is `Çřéáťé çřéw ~~~`. Places (`{}`, `{0}`, `{n}`) do not
  change. A plural gets two forms.
- The name is the base of the Windows pseudo locale `qps-ploc`. No real OS
  locale has that base, so the OS never selects it.

Run with it:

```bash
MELLO_LANG=qps cargo run -p mello-client
```

A string that stays in plain English under `qps` is not in `@tr()`, or the
`.pot` is stale.

## 9. Language selection

`client/src/locale.rs` chooses the language in this order:

| Order | Source | Use |
|---|---|---|
| 1 | `MELLO_LANG` | Tests, the e2e driver, development |
| 2 | `Settings.language`, when not empty | The user's choice. Empty is "System". |
| 3 | The OS language (`sys_locale`) | The default |

`MELLO_CLOCK` (`12` or `24`) sets the clock form in the same way, before
the OS setting (section 13).

A source that names a language with no catalog is skipped with a warning.
When no source names a bundled language, the UI is English. The match is
exact first (case and `_` ignored), then the base language: `sv-SE` selects
`sv`. `en` and `en-*` select the source language.

### Startup order

The choice is made one time per run, before the first window. The CJK font
fallback must be set before the first layout, and Slint can select a
language only after the first component exists.

| Step | Where | What it does |
|---|---|---|
| 1 | `configure_slint_platform`, after `set_platform` | `locale::startup` chooses with the pure `locale::choose` (env, `Settings::load().language`, OS, bundled list), stores the choice and logs one `info` line with the language and its source. |
| 2 | The same place | `cjk_fonts::apply(locale::startup_font_language(&choice))` sets the CJK fallback order. |
| 3 | `run_gate` after `ForceUpdateWindow::new()`, and `lib.rs` after `MainWindow::new()` | `locale::apply` selects the stored choice. It does not choose again. |

`locale::ui_language()` gives the stored tag (`sv`, `pt-BR`, `en`) at each
step: for fonts, the HUD, dates and the account language.

The font language (`locale::font_language`) is the UI language when it has
Han text (`ja`, `zh-*`, `ko`). For another UI language, Han text comes only
from user data, so the OS language orders the fonts: a Japanese OS with an
English UI keeps the Japanese glyph shapes.

### The Settings row

The GENERAL tab has a "Language" dropdown in the APPEARANCE section.

- The first row is "System" (`Tr.language-system`). It saves an empty
  `Settings.language`.
- Then English, then each bundled language (`locale::setting_choices`).
  English is always a choice: it is the source language, not a catalog.
- Each language shows its own name in its own script
  (`locale::native_name`): English, Svenska, Deutsch, Español, Français,
  Português (Brasil), 中文（简体）, 日本語. An unknown tag shows the tag.
  `qps` shows "Pseudo (qps)". The names are data, not messages.
- A release build with no catalog shows "System" and "English". A debug
  build shows `qps` too.
- A change saves `Settings.language` and applies at the next start. When the
  choice differs from the setting of this run, the row shows "Takes effect
  after restart".

### Tests run in English

The tests find controls by their English `accessible-label`. Slint selects
the OS language on each test thread by itself, so:

- `testkit::new_main_window()` and `Harness::new()` select the source
  language after they make the window. A test never calls
  `MainWindow::new()` directly.
- The e2e driver (`tools/mello-driver/src/app.ts`) starts each app with
  `MELLO_LANG=en` and `MELLO_CLOCK=24`. A journey can set another value in
  its `env`. The state port gives the language of the run as
  `ui_language` (`client/src/e2e_state.rs`).

## 9a. The account language (`lang_tag`)

The Nakama account has a `lang_tag`. The client writes the UI language to
it, so the server knows the language of the user.

- After a sign-in (`LoggedIn`, `OnboardingReady`, the device sign-in after
  a failed restore), the client sends `Command::SetAccountLanguage` with
  `locale::ui_language()`.
- After a change of the Language setting, the client sends the language of
  the next start: `locale::language_for_setting` (env, the new setting, OS).
- The core keeps the `lang_tag` of the account from each account read. It
  writes the tag with `NakamaClient::update_account_fields` only when the
  tag differs (`set_account_lang_tag`).
- `BeforeUpdateAccount` in `auth.go` changes only the display name. It
  keeps `lang_tag` as the client sends it.
- The pseudo language goes to the account as `qps`. It is in the private
  use range of BCP 47, and only a debug build has it.

## 10. Add a language

1. Make the catalog from the template. Copy
   `client/translations/mello-client.pot` to
   `client/translations/<lang>/LC_MESSAGES/mello-client.po`. Use the BCP 47
   tag as the folder name: `sv`, `de`, `ja`, `zh-CN`, `pt-BR`.
2. In the header, set `Language: <lang>` and the `Plural-Forms` line of
   the language.
3. Translate each `msgstr`. An empty `msgstr` shows English.
4. Build. `build.rs` finds the folder and bundles it.
5. Check the fonts. Oxanium and Barlow have Latin letters only. CJK text
   needs a fallback font.

When the `.pot` changes, merge it into each catalog with a tool that has a
compatible licence (for example Poedit or a Rust tool). Keep the
translations of the messages that did not change.

## 11. Tools

| Tool | Use | Licence |
|---|---|---|
| `slint-tr-extractor` 1.18.1 | Writes the `.pot` from `@tr()` | The Slint licence, the same as the `slint` crate |
| `client/build_l10n.rs` | Reads the `.pot`, writes `qps` | This repository |
| The Slint compiler | Reads the catalogs at build time | The Slint licence |

Install the extractor:

```bash
cargo install slint-tr-extractor --version 1.18.1 --locked
```

The project does not use GNU gettext (`xgettext`, `msgfmt`, `msgmerge`,
`msginit`) or `xtr`, in scripts, tests or the build. They are GPL tools,
and the client is Apache 2.0. The Slint tools and the compiler do the same
work.

## 12. Size

The catalogs are string tables in the binary. A release build has the real
languages only.

| Item | Size |
|---|---|
| `.pot` of the first files (41 messages) | about 4 KB |
| One language, about 1,000 messages of about 30 bytes | about 30 KB of text, plus about 16 bytes of table per message |
| Seven languages | about 0.3 MB in the binary |

CJK text in UTF-8 is 3 bytes for each character, but it is shorter in
characters. The estimate stays under 0.5 MB.

## 13. Dates, times, durations and numbers

`client/src/l10n_format.rs` makes all date, time, duration and number text
of the client. The words and the patterns are members of `Tr` (the section
"Dates, times, durations and numbers" in `client/ui/tr.slint`).

The rule: no date, time, duration or number text outside this module. Do
not write a weekday, a month, "ago", "h", "min" or a `{:02}:{:02}` pattern in
another file. Add a function to the module, and call it.

| Part | What it does |
|---|---|
| Names | Weekdays, full and short months, from `Tr`. |
| Dates | `day_month`, `week_range`, `week_of`, `chat_day`. Each date is one pattern message. |
| Relative time | `days_ago` ("3 days ago", a plural), `recap_age`, `short_age` ("5m ago"), `feed_card_age`, `chat_time`. |
| Time of day | `time_of_day`: 12 h or 24 h from `clock_form()`. The AM and PM markers come from `Tr`. |
| Durations | `duration_hm_short` ("1h 23m"), `duration_h_mm` ("6 h 40"), `duration_clock` ("0:45"), `since`. The unit letters have the context `unit`. |
| Numbers | `grouped`: the group separator is the message `digit-group-separator` (context `format`). `percent`, `approx` ("~1h 5m"), `file_size` ("860 KB"), `milliseconds` ("118 ms"). |
| Lists | `list_and` ("Bob, Max and Sara"), `list_and_serial` ("Bob, Max, and Sara", the catch-up line), `list_comma`, `list_plus` ("Bob, Max +3"), `list_and_more` ("Bob, Max and 3 more"), `dot_pair` ("4W–2L · 3-win streak"). A list is never joined with `join(", ")` for the UI. |
| Upper case | `week_label_upper`, `feed_card_age_upper`, `weekday_name_upper`, `month_short_upper_name`. |

Decisions:

- `mello-core` returns values (`chat::DayLabel`, `chat::MessageTime`,
  `chat::ReplyPreview`, `chat::ImageUploadError`,
  `telemetry::SetupHint`). The client makes the text.
- Two more contexts: `list` (the comma between the items of a list, which
  differs from the comma between two parts of a caption) and `streak` ("W4",
  which is not the short week "W45").
- The clock form comes from the OS setting of the user, not from the UI
  language. Windows reads the short time format (`LOCALE_SSHORTTIME`). macOS
  and Linux use 24 h. The value is read one time per run.
- `MELLO_CLOCK=12` or `MELLO_CLOCK=24` comes before the OS setting. Another
  value is ignored with a warning. The e2e driver sets `24`, so a journey
  reads the same time on every PC.
- A test gets 24 h. A test that needs 12 h calls `set_test_clock_form`. No
  test reads the OS setting.
- Days are UTC days, except where a function takes a time zone offset or a
  `mello-core` value in local time.

How a translator changes a pattern: a pattern message has the context
`format` and a `///` line that tells what each place is. Write the places in
the order of the language, with its marks. Keep each place:

| Message | English | Chinese |
|---|---|---|
| `date-day-month` | `{0} {1}` (5 October) | `{1}{0}日` (10月5日) |
| `time-12h` | `{0}:{1} {2}` (11:14 PM) | `{2}{0}:{1}` (下午11:14) |

The pseudo language `qps` keeps a message with no letter as it is, with no
padding (`build_l10n::pseudo`). So a pattern or a separator does not change
under `qps`, and the words in it do.
