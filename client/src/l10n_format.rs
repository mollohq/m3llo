//! Dates, times, durations and numbers in the UI language
//! (specs/features/LOCALIZATION.md, section 13).
//!
//! All date, time, duration and number text of the client comes from this
//! module. The words and the patterns come from the Slint global `Tr`, so a
//! translator can change the words and the order of the parts. Each function
//! takes `tr` and the values, and has no other input. The clock form (12 h
//! or 24 h) comes from the OS setting of the user, not from the UI language.
//!
//! Days are counted in UTC unless a function takes a time zone offset.

use mello_core::chat::{DayLabel, LocalDay, MessageTime};

use crate::Tr;

/// Milliseconds in one day.
pub const DAY_MS: i64 = 24 * 60 * 60 * 1000;

// --- The clock form ---

/// How the time of day is written.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ClockForm {
    /// "11:14 PM".
    H12,
    /// "23:14".
    H24,
}

#[cfg(test)]
thread_local! {
    static TEST_CLOCK: std::cell::Cell<ClockForm> = const { std::cell::Cell::new(ClockForm::H24) };
}

/// The clock form of the user. The app reads the OS setting one time. A
/// test gets 24 h, or the form that [`set_test_clock_form`] set on its
/// thread, so no test depends on the setting of the PC.
pub fn clock_form() -> ClockForm {
    #[cfg(test)]
    {
        TEST_CLOCK.with(std::cell::Cell::get)
    }
    #[cfg(not(test))]
    {
        static OS: std::sync::OnceLock<ClockForm> = std::sync::OnceLock::new();
        *OS.get_or_init(|| {
            let form = os_clock_form();
            log::info!("[l10n] clock form {form:?}");
            form
        })
    }
}

/// The English `Tr` of a test window that lives as long as the test thread,
/// for a test input that holds `&Tr`.
#[cfg(test)]
pub fn test_tr() -> &'static Tr<'static> {
    use slint::ComponentHandle;
    thread_local! {
        static TR: &'static Tr<'static> = {
            let app: &'static crate::MainWindow =
                Box::leak(Box::new(crate::testkit::new_main_window()));
            Box::leak(Box::new(app.global::<Tr>()))
        };
    }
    TR.with(|tr| *tr)
}

/// Set the clock form that [`clock_form`] gives on this test thread.
#[cfg(test)]
pub fn set_test_clock_form(form: ClockForm) {
    TEST_CLOCK.with(|c| c.set(form));
}

/// The clock form of an OS time pattern, such as the Windows short time
/// format: `HH:mm` is 24 h, `h:mm tt` is 12 h. Text in single quotes is a
/// literal and does not count.
pub fn clock_form_of_pattern(pattern: &str) -> ClockForm {
    let mut quoted = false;
    let mut twelve = false;
    for c in pattern.chars() {
        match c {
            '\'' => quoted = !quoted,
            'H' if !quoted => return ClockForm::H24,
            'h' if !quoted => twelve = true,
            _ => {}
        }
    }
    if twelve {
        ClockForm::H12
    } else {
        ClockForm::H24
    }
}

/// The short time format of the user (`LOCALE_SSHORTTIME`), as the taskbar
/// clock shows it.
#[cfg(target_os = "windows")]
#[cfg_attr(test, allow(dead_code))]
fn os_clock_form() -> ClockForm {
    use windows::core::PCWSTR;
    use windows::Win32::Globalization::{GetLocaleInfoEx, LOCALE_SSHORTTIME};
    let mut buf = [0u16; 80];
    // SAFETY: a null name selects the user default locale, and the buffer
    // is a valid slice that the call writes at most `buf.len()` units into.
    let n = unsafe { GetLocaleInfoEx(PCWSTR::null(), LOCALE_SSHORTTIME, Some(&mut buf)) };
    if n <= 1 {
        return ClockForm::H24;
    }
    // `n` counts the closing null.
    let len = (n as usize - 1).min(buf.len());
    clock_form_of_pattern(&String::from_utf16_lossy(&buf[..len]))
}

/// macOS and Linux: no current dependency reads the setting, so the clock
/// is 24 h.
#[cfg(not(target_os = "windows"))]
#[cfg_attr(test, allow(dead_code))]
fn os_clock_form() -> ClockForm {
    ClockForm::H24
}

// --- Calendar arithmetic ---

/// Days between two instants, by UTC day. Never negative.
pub fn days_between(then_ms: i64, now_ms: i64) -> i64 {
    (now_ms.div_euclid(DAY_MS) - then_ms.div_euclid(DAY_MS)).max(0)
}

/// The calendar day of an instant, UTC (Howard Hinnant's civil algorithm).
pub fn civil_day(ms: i64) -> LocalDay {
    let days = ms.div_euclid(DAY_MS);
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    LocalDay {
        year: year as i32,
        month: month as u32,
        day: day as u32,
        // 1970-01-01 was a Thursday.
        weekday: (days + 4).rem_euclid(7) as u32,
    }
}

// --- Names ---

/// The name of a day of the week, 0 is Sunday.
pub fn weekday_name(tr: &Tr<'_>, weekday: u32) -> String {
    match weekday % 7 {
        0 => tr.get_weekday_sunday(),
        1 => tr.get_weekday_monday(),
        2 => tr.get_weekday_tuesday(),
        3 => tr.get_weekday_wednesday(),
        4 => tr.get_weekday_thursday(),
        5 => tr.get_weekday_friday(),
        _ => tr.get_weekday_saturday(),
    }
    .into()
}

/// The full name of a month, 1 is January.
pub fn month_name(tr: &Tr<'_>, month: u32) -> String {
    match month {
        1 => tr.get_month_january(),
        2 => tr.get_month_february(),
        3 => tr.get_month_march(),
        4 => tr.get_month_april(),
        5 => tr.get_month_may(),
        6 => tr.get_month_june(),
        7 => tr.get_month_july(),
        8 => tr.get_month_august(),
        9 => tr.get_month_september(),
        10 => tr.get_month_october(),
        11 => tr.get_month_november(),
        _ => tr.get_month_december(),
    }
    .into()
}

/// The short name of a month, 1 is January.
pub fn month_short_name(tr: &Tr<'_>, month: u32) -> String {
    match month {
        1 => tr.get_month_short_january(),
        2 => tr.get_month_short_february(),
        3 => tr.get_month_short_march(),
        4 => tr.get_month_short_april(),
        5 => tr.get_month_short_may(),
        6 => tr.get_month_short_june(),
        7 => tr.get_month_short_july(),
        8 => tr.get_month_short_august(),
        9 => tr.get_month_short_september(),
        10 => tr.get_month_short_october(),
        11 => tr.get_month_short_november(),
        _ => tr.get_month_short_december(),
    }
    .into()
}

// --- Dates ---

/// "2 October".
pub fn day_month(tr: &Tr<'_>, d: LocalDay) -> String {
    tr.invoke_date_day_month(d.day as i32, month_name(tr, d.month).into())
        .into()
}

/// "6 Oct".
pub fn day_month_short(tr: &Tr<'_>, d: LocalDay) -> String {
    tr.invoke_date_day_month(d.day as i32, month_short_name(tr, d.month).into())
        .into()
}

/// The day and the month of an instant, UTC: "2 October".
pub fn date_label(tr: &Tr<'_>, ms: i64) -> String {
    day_month(tr, civil_day(ms))
}

/// The days of a recap week: "12 to 18 October", or "28 September to 4
/// October" across a month. `week_end_ms` is the Monday that ends the week,
/// so the last day is the one before it. Empty without a time.
pub fn week_range(tr: &Tr<'_>, week_start_ms: i64, week_end_ms: i64) -> String {
    if week_start_ms <= 0 || week_end_ms <= week_start_ms {
        return String::new();
    }
    let first = civil_day(week_start_ms);
    let last = civil_day(week_end_ms - 1);
    if first.month == last.month {
        tr.invoke_date_range_in_month(
            first.day as i32,
            last.day as i32,
            month_name(tr, last.month).into(),
        )
    } else {
        tr.invoke_date_range_across_months(
            first.day as i32,
            month_name(tr, first.month).into(),
            last.day as i32,
            month_name(tr, last.month).into(),
        )
    }
    .into()
}

/// The title of a recap the server did not number: "Week of 6 Oct", from the
/// start of the recap week. Empty without a time.
pub fn week_of(tr: &Tr<'_>, week_start_ms: i64) -> String {
    if week_start_ms <= 0 {
        return String::new();
    }
    let date = day_month_short(tr, civil_day(week_start_ms));
    tr.invoke_week_of(date.into()).into()
}

/// The label of a recap week in upper case: "WEEK 4" when the server
/// numbered it, else "WEEK OF 6 OCT" from its start. Empty without either.
pub fn week_label_upper(tr: &Tr<'_>, number: i64, week_start_ms: i64) -> String {
    if number > 0 {
        return tr.invoke_week_number_upper(number as i32).into();
    }
    if week_start_ms <= 0 {
        return String::new();
    }
    let d = civil_day(week_start_ms);
    let date = tr.invoke_date_day_month(d.day as i32, month_short_upper_name(tr, d.month));
    tr.invoke_week_of_upper(date).into()
}

/// The short name of a month in upper case, 1 is January.
pub fn month_short_upper_name(tr: &Tr<'_>, month: u32) -> slint::SharedString {
    match month {
        1 => tr.get_month_short_upper_january(),
        2 => tr.get_month_short_upper_february(),
        3 => tr.get_month_short_upper_march(),
        4 => tr.get_month_short_upper_april(),
        5 => tr.get_month_short_upper_may(),
        6 => tr.get_month_short_upper_june(),
        7 => tr.get_month_short_upper_july(),
        8 => tr.get_month_short_upper_august(),
        9 => tr.get_month_short_upper_september(),
        10 => tr.get_month_short_upper_october(),
        11 => tr.get_month_short_upper_november(),
        _ => tr.get_month_short_upper_december(),
    }
}

/// The title of a numbered recap: "Week 4".
pub fn week_number(tr: &Tr<'_>, n: i64) -> String {
    tr.invoke_week_number(n as i32).into()
}

// --- Relative days ---

/// How long ago, by UTC day: "Today", "Yesterday", "2 days ago", "Last
/// week", "3 weeks ago". Empty without a time.
pub fn days_ago(tr: &Tr<'_>, then_ms: i64, now_ms: i64) -> String {
    if then_ms <= 0 {
        return String::new();
    }
    match days_between(then_ms, now_ms) {
        0 => tr.get_today().into(),
        1 => tr.get_yesterday().into(),
        d @ 2..=6 => tr.invoke_days_ago(d as i32).into(),
        7..=13 => tr.get_last_week().into(),
        d => weeks_ago(tr, d / 7),
    }
}

/// How old a weekly recap is: "This week", "Last week", then "3 weeks ago",
/// counted from the start of the recap week. Empty without a time.
pub fn recap_age(tr: &Tr<'_>, week_start_ms: i64, now_ms: i64) -> String {
    if week_start_ms <= 0 {
        return String::new();
    }
    match days_between(week_start_ms, now_ms) {
        0..=6 => tr.get_this_week().into(),
        7..=13 => tr.get_last_week().into(),
        d => weeks_ago(tr, d / 7),
    }
}

/// "3 weeks ago".
pub fn weeks_ago(tr: &Tr<'_>, weeks: i64) -> String {
    tr.invoke_weeks_ago(weeks as i32).into()
}

/// A short age in seconds: "just now", "5m ago", "3h ago", "4d ago", "2w
/// ago".
pub fn short_age(tr: &Tr<'_>, ago_secs: i64) -> String {
    if ago_secs < 60 {
        tr.get_just_now()
    } else if ago_secs < 3600 {
        tr.invoke_minutes_ago_short((ago_secs / 60) as i32)
    } else if ago_secs < 86_400 {
        tr.invoke_hours_ago_short((ago_secs / 3600) as i32)
    } else if ago_secs < 7 * 86_400 {
        tr.invoke_days_ago_short((ago_secs / 86_400) as i32)
    } else {
        tr.invoke_weeks_ago_short((ago_secs / (7 * 86_400)) as i32)
    }
    .into()
}

/// The age of a feed card: "just now", "5m ago", "3h ago" up to 12 hours,
/// "yesterday", "2 days ago", then the weekday for the rest of the week,
/// then "9d ago". `ago_secs` is the age, `ts_secs` the time of the card.
pub fn feed_card_age(tr: &Tr<'_>, ago_secs: i64, ts_secs: i64) -> String {
    if ago_secs < 60 {
        tr.get_just_now().into()
    } else if ago_secs < 3600 {
        tr.invoke_minutes_ago_short((ago_secs / 60) as i32).into()
    } else if ago_secs < 43_200 {
        tr.invoke_hours_ago_short((ago_secs / 3600) as i32).into()
    } else if ago_secs < 86_400 {
        tr.get_yesterday_lower().into()
    } else if ago_secs < 172_800 {
        tr.invoke_days_ago(2).into()
    } else if ago_secs < 604_800 {
        match (ago_secs / 86_400) % 7 {
            0 => tr.get_today_lower().into(),
            1 => tr.get_yesterday_lower().into(),
            _ => weekday_name(tr, civil_day(ts_secs * 1000).weekday),
        }
    } else {
        tr.invoke_days_ago_short((ago_secs / 86_400) as i32).into()
    }
}

/// [`feed_card_age`] in upper case, for the session preview card: "JUST
/// NOW", "5M AGO", "YESTERDAY", "MONDAY".
pub fn feed_card_age_upper(tr: &Tr<'_>, ago_secs: i64, ts_secs: i64) -> String {
    if ago_secs < 60 {
        tr.get_just_now_upper().into()
    } else if ago_secs < 3600 {
        tr.invoke_minutes_ago_short_upper((ago_secs / 60) as i32)
            .into()
    } else if ago_secs < 43_200 {
        tr.invoke_hours_ago_short_upper((ago_secs / 3600) as i32)
            .into()
    } else if ago_secs < 86_400 {
        tr.get_yesterday_upper().into()
    } else if ago_secs < 172_800 {
        tr.invoke_days_ago_upper(2).into()
    } else if ago_secs < 604_800 {
        match (ago_secs / 86_400) % 7 {
            0 => tr.get_today_upper().into(),
            1 => tr.get_yesterday_upper().into(),
            _ => weekday_name_upper(tr, civil_day(ts_secs * 1000).weekday),
        }
    } else {
        tr.invoke_days_ago_short_upper((ago_secs / 86_400) as i32)
            .into()
    }
}

/// The name of a day of the week in upper case, 0 is Sunday.
pub fn weekday_name_upper(tr: &Tr<'_>, weekday: u32) -> String {
    match weekday % 7 {
        0 => tr.get_weekday_upper_sunday(),
        1 => tr.get_weekday_upper_monday(),
        2 => tr.get_weekday_upper_tuesday(),
        3 => tr.get_weekday_upper_wednesday(),
        4 => tr.get_weekday_upper_thursday(),
        5 => tr.get_weekday_upper_friday(),
        _ => tr.get_weekday_upper_saturday(),
    }
    .into()
}

// --- Times ---

/// The time of day: "23:14" on a 24 h clock, "11:14 PM" on a 12 h clock.
pub fn time_of_day(tr: &Tr<'_>, hour: u32, minute: u32, clock: ClockForm) -> String {
    let minute = format!("{:02}", minute % 60);
    match clock {
        ClockForm::H24 => tr
            .invoke_time_24h(format!("{:02}", hour % 24).into(), minute.into())
            .into(),
        ClockForm::H12 => {
            let marker = if hour % 24 < 12 {
                tr.get_time_am()
            } else {
                tr.get_time_pm()
            };
            let h12 = match hour % 12 {
                0 => 12,
                h => h,
            };
            tr.invoke_time_12h(h12 as i32, minute.into(), marker).into()
        }
    }
}

/// An instant of today or yesterday on the viewer's clock: "Today 23:14",
/// "Yesterday 23:14". `tz_offset_min` is the local offset, east positive.
pub fn today_or_yesterday_at(
    tr: &Tr<'_>,
    ts_ms: i64,
    now_ms: i64,
    tz_offset_min: i32,
    clock: ClockForm,
) -> String {
    let local = |ms: i64| ms + i64::from(tz_offset_min) * 60_000;
    let day = |ms: i64| local(ms).div_euclid(DAY_MS);
    let in_day = local(ts_ms).rem_euclid(DAY_MS) / 60_000;
    let time = time_of_day(tr, (in_day / 60) as u32, (in_day % 60) as u32, clock);
    if day(ts_ms) >= day(now_ms) {
        tr.invoke_today_at(time.into()).into()
    } else {
        tr.invoke_yesterday_at(time.into()).into()
    }
}

/// The day separator of the chat: "Today", "Sunday 1 Nov", "27 Oct", "29
/// Sep 2025". Empty for `None`.
pub fn chat_day(tr: &Tr<'_>, label: Option<&DayLabel>) -> String {
    let Some(label) = label else {
        return String::new();
    };
    match *label {
        DayLabel::Today => tr.get_today().into(),
        DayLabel::Yesterday => tr.get_yesterday().into(),
        DayLabel::Weekday(d) => tr
            .invoke_date_weekday_day_month(
                d.day as i32,
                month_short_name(tr, d.month).into(),
                weekday_name(tr, d.weekday).into(),
            )
            .into(),
        DayLabel::ThisYear(d) => day_month_short(tr, d),
        DayLabel::Older(d) => tr
            .invoke_date_day_month_year(d.day as i32, month_short_name(tr, d.month).into(), d.year)
            .into(),
    }
}

/// The time beside a chat message: "just now", "5m ago", "23:14",
/// "Yesterday", "Oct 27", "Oct 27, 2025". Empty for `None`.
pub fn chat_time(tr: &Tr<'_>, time: Option<&MessageTime>, clock: ClockForm) -> String {
    let Some(time) = time else {
        return String::new();
    };
    match time {
        MessageTime::Unparsed(s) => s.clone(),
        MessageTime::JustNow => tr.get_just_now().into(),
        MessageTime::MinutesAgo(m) => tr.invoke_minutes_ago_short(*m as i32).into(),
        MessageTime::Clock { hour, minute } => time_of_day(tr, *hour, *minute, clock),
        MessageTime::Yesterday => tr.get_yesterday().into(),
        MessageTime::ThisYear(d) => tr
            .invoke_date_month_day(d.day as i32, month_short_name(tr, d.month).into())
            .into(),
        MessageTime::Older(d) => tr
            .invoke_date_month_day_year(d.day as i32, month_short_name(tr, d.month).into(), d.year)
            .into(),
    }
}

// --- Durations ---

/// Minutes with unit letters: "47m", "1h 23m". Zero and less is "0m".
pub fn duration_hm_short(tr: &Tr<'_>, minutes: i64) -> String {
    let m = minutes.max(0);
    if m >= 60 {
        tr.invoke_duration_h_m_short((m / 60) as i32, (m % 60) as i32)
    } else {
        tr.invoke_duration_m_short(m as i32)
    }
    .into()
}

/// Minutes with unit letters, with no zero part: "4h 12m", "2h", "45m".
pub fn duration_hm_compact(tr: &Tr<'_>, minutes: i64) -> String {
    let (h, m) = (minutes.max(0) / 60, minutes.max(0) % 60);
    if h > 0 && m > 0 {
        tr.invoke_duration_h_m_short(h as i32, m as i32).into()
    } else if h > 0 {
        tr.invoke_duration_h_short(h.to_string().into()).into()
    } else {
        tr.invoke_duration_m_short(m as i32).into()
    }
}

/// Minutes with unit letters, hours with one decimal from an hour: "45m",
/// "2.5h".
pub fn duration_hours_decimal_short(tr: &Tr<'_>, minutes: f64) -> String {
    if minutes >= 60.0 {
        tr.invoke_duration_h_short(format!("{:.1}", minutes / 60.0).into())
            .into()
    } else {
        tr.invoke_duration_m_short(minutes as i32).into()
    }
}

/// Minutes as the recap writes them: "6 h 40", "3 h 00", "48 min".
pub fn duration_h_mm(tr: &Tr<'_>, minutes: i64) -> String {
    let m = minutes.max(0);
    if m >= 60 {
        tr.invoke_duration_h_mm((m / 60) as i32, format!("{:02}", m % 60).into())
    } else {
        tr.invoke_duration_min(m as i32)
    }
    .into()
}

/// The length of a clip: "0:45", "12:03".
pub fn duration_clock(tr: &Tr<'_>, seconds: i64) -> String {
    let s = seconds.max(0);
    tr.invoke_duration_clock((s / 60) as i32, format!("{:02}", s % 60).into())
        .into()
}

/// Hours, upper case: "4,167 H" from 100 hours up, one decimal below
/// ("12.6 H"). Less than 0.1 hours shows as 0.1.
pub fn hours_upper(tr: &Tr<'_>, minutes: i64) -> String {
    let hours = minutes as f64 / 60.0;
    let value = if hours >= 100.0 {
        grouped(tr, hours.round() as i64)
    } else {
        format!("{:.1}", hours.max(0.1))
    };
    tr.invoke_duration_hours_upper(value.into()).into()
}

/// Hours, lower case: "4,167 h" from 100 hours up, one decimal below
/// ("12.6 h"). The lower-case form of [`hours_upper`].
pub fn hours_lower(tr: &Tr<'_>, minutes: i64) -> String {
    let hours = minutes as f64 / 60.0;
    let value = if hours >= 100.0 {
        grouped(tr, hours.round() as i64)
    } else {
        format!("{:.1}", hours.max(0.1))
    };
    tr.invoke_duration_hours(value.into()).into()
}

/// Play time in a caption: "45 min" under an hour, "4.5 h" or "4 h" under
/// ten hours, then whole hours ("4,167 h"). Empty for no minutes.
pub fn play_time(tr: &Tr<'_>, minutes: i64) -> String {
    if minutes <= 0 {
        return String::new();
    }
    if minutes < 60 {
        return tr.invoke_duration_min(minutes as i32).into();
    }
    let h = minutes as f64 / 60.0;
    let value = if h >= 10.0 {
        grouped(tr, h.round() as i64)
    } else if (h - h.round()).abs() < 0.05 {
        (h.round() as i64).to_string()
    } else {
        format!("{h:.1}")
    };
    tr.invoke_duration_hours(value.into()).into()
}

/// How long a live session runs: "since 12 min", "since 1 h 5 min", "since
/// 2 h". Under a minute counts as one.
pub fn since(tr: &Tr<'_>, minutes: i64) -> String {
    let m = minutes.max(1);
    if m >= 60 && m % 60 == 0 {
        tr.invoke_since_hours((m / 60) as i32)
    } else if m >= 60 {
        tr.invoke_since_hours_minutes((m / 60) as i32, (m % 60) as i32)
    } else {
        tr.invoke_since_minutes(m as i32)
    }
    .into()
}

/// The size of a file, as a file browser writes it: "860 KB", "1.2 MB".
pub fn file_size(tr: &Tr<'_>, bytes: u64) -> String {
    if bytes >= 1 << 20 {
        tr.invoke_size_mb(format!("{:.1}", bytes as f64 / (1 << 20) as f64).into())
    } else {
        tr.invoke_size_kb(bytes.div_ceil(1024).max(1) as i32)
    }
    .into()
}

// --- Numbers ---

/// `n` with `separator` between the groups of three digits.
pub fn group_digits(n: i64, separator: &str) -> String {
    let digits = n.unsigned_abs().to_string();
    let mut out = String::with_capacity(digits.len() + digits.len() / 3 * separator.len() + 1);
    if n < 0 {
        out.push('-');
    }
    for (i, c) in digits.chars().enumerate() {
        if i > 0 && (digits.len() - i).is_multiple_of(3) {
            out.push_str(separator);
        }
        out.push(c);
    }
    out
}

/// A number with the group separator of the UI language: "4,201" in
/// English.
pub fn grouped(tr: &Tr<'_>, n: i64) -> String {
    group_digits(n, &tr.get_digit_group_separator())
}

/// A time in milliseconds. The value is a number formatted by the caller,
/// with a sign or a decimal: "118 ms", "+8 ms", "2.1 ms".
pub fn milliseconds(tr: &Tr<'_>, value: &str) -> String {
    tr.invoke_duration_ms(value.into()).into()
}

/// A percent: "62%".
pub fn percent(tr: &Tr<'_>, n: i64) -> String {
    tr.invoke_percent(n as i32).into()
}

/// A value that is not exact: "~1h 5m".
pub fn approx(tr: &Tr<'_>, value: &str) -> String {
    tr.invoke_approx(value.into()).into()
}

// --- Lists ---

/// Items with commas: "Bob, Max, Sara".
pub fn list_comma(tr: &Tr<'_>, items: &[String]) -> String {
    let mut iter = items.iter();
    let Some(first) = iter.next() else {
        return String::new();
    };
    iter.fold(first.clone(), |acc, item| {
        tr.invoke_list_comma(acc.into(), item.as_str().into())
            .into()
    })
}

/// Items with commas and "and" before the last: "Bob", "Bob and Max",
/// "Bob, Max and Sara".
pub fn list_and(tr: &Tr<'_>, items: &[String]) -> String {
    match items {
        [] => String::new(),
        [one] => one.clone(),
        [rest @ .., last] => tr
            .invoke_list_and(list_comma(tr, rest).into(), last.as_str().into())
            .into(),
    }
}

/// The first items with commas and the count of the others: "Bob, Max,
/// Sara +2". With no others, the items only.
pub fn list_plus(tr: &Tr<'_>, shown: &[String], others: usize) -> String {
    let list = list_comma(tr, shown);
    if others == 0 {
        return list;
    }
    tr.invoke_list_plus(list.into(), others as i32).into()
}

/// The first items with commas and the count of the others in words: "Bob,
/// Max and 3 more". With no others, [`list_and`].
pub fn list_and_more(tr: &Tr<'_>, shown: &[String], others: usize) -> String {
    if others == 0 {
        return list_and(tr, shown);
    }
    tr.invoke_list_and_more(list_comma(tr, shown).into(), others as i32)
        .into()
}

/// Two parts of one line with a middle dot: "4W–2L · 3-win streak".
pub fn dot_pair(tr: &Tr<'_>, first: &str, second: &str) -> String {
    tr.invoke_dot_pair(first.into(), second.into()).into()
}

#[cfg(test)]
mod tests {
    use slint::ComponentHandle;

    use super::*;

    const DAY: i64 = DAY_MS;
    /// Monday 5 October 2026, 00:00 UTC.
    const MON_5_OCT: i64 = 20_731 * DAY;

    fn with_tr(f: impl FnOnce(&Tr<'_>)) {
        let app = crate::testkit::new_main_window();
        f(&app.global::<Tr>());
    }

    fn day(year: i32, month: u32, day: u32, weekday: u32) -> LocalDay {
        LocalDay {
            year,
            month,
            day,
            weekday,
        }
    }

    #[test]
    fn the_civil_day_is_right() {
        assert_eq!(civil_day(MON_5_OCT), day(2026, 10, 5, 1));
        assert_eq!(civil_day(0), day(1970, 1, 1, 4));
        // 29 February 2028, a Tuesday.
        assert_eq!(civil_day(21_243 * DAY), day(2028, 2, 29, 2));
        assert_eq!(days_between(MON_5_OCT + 1, MON_5_OCT - 1), 0);
    }

    #[test]
    fn dates_read_as_today() {
        with_tr(|tr| {
            assert_eq!(date_label(tr, MON_5_OCT), "5 October");
            assert_eq!(date_label(tr, MON_5_OCT - 6 * DAY), "29 September");
            assert_eq!(date_label(tr, 0), "1 January");
            assert_eq!(date_label(tr, 21_243 * DAY), "29 February");
            assert_eq!(weekday_name(tr, civil_day(MON_5_OCT).weekday), "Monday");
            assert_eq!(weekday_name(tr, 4), "Thursday");
            let start = MON_5_OCT + 7 * DAY;
            assert_eq!(week_range(tr, start, start + 7 * DAY), "12 to 18 October");
            let start = MON_5_OCT - 7 * DAY;
            assert_eq!(
                week_range(tr, start, start + 7 * DAY),
                "28 September to 4 October"
            );
            assert_eq!(week_range(tr, 0, 0), "");
            assert_eq!(week_of(tr, MON_5_OCT + DAY), "Week of 6 Oct");
            assert_eq!(week_of(tr, 0), "");
            assert_eq!(week_number(tr, 4), "Week 4");
            assert_eq!(week_label_upper(tr, 4, MON_5_OCT), "WEEK 4");
            assert_eq!(week_label_upper(tr, 0, MON_5_OCT + DAY), "WEEK OF 6 OCT");
            assert_eq!(week_label_upper(tr, 0, 0), "");
        });
    }

    #[test]
    fn relative_days_read_as_today() {
        with_tr(|tr| {
            let now = MON_5_OCT + 3_600_000;
            assert_eq!(days_ago(tr, now - 1_000, now), "Today");
            assert_eq!(days_ago(tr, MON_5_OCT - DAY, now), "Yesterday");
            assert_eq!(days_ago(tr, MON_5_OCT - 2 * DAY, now), "2 days ago");
            assert_eq!(days_ago(tr, MON_5_OCT - 8 * DAY, now), "Last week");
            assert_eq!(days_ago(tr, MON_5_OCT - 24 * DAY, now), "3 weeks ago");
            assert_eq!(days_ago(tr, 0, now), "");

            let start = MON_5_OCT;
            assert_eq!(recap_age(tr, start, start + 6 * DAY), "This week");
            assert_eq!(recap_age(tr, start, start + 13 * DAY), "Last week");
            assert_eq!(recap_age(tr, start, start + 14 * DAY), "2 weeks ago");
            assert_eq!(recap_age(tr, 0, start), "");

            assert_eq!(short_age(tr, 30), "just now");
            assert_eq!(short_age(tr, 300), "5m ago");
            assert_eq!(short_age(tr, 3 * 3600), "3h ago");
            assert_eq!(short_age(tr, 4 * 86_400), "4d ago");
            assert_eq!(short_age(tr, 15 * 86_400), "2w ago");

            let ts = MON_5_OCT / 1000;
            assert_eq!(feed_card_age(tr, 30, ts), "just now");
            assert_eq!(feed_card_age(tr, 7_200, ts), "2h ago");
            assert_eq!(feed_card_age(tr, 50_000, ts), "yesterday");
            assert_eq!(feed_card_age(tr, 100_000, ts), "2 days ago");
            assert_eq!(feed_card_age(tr, 3 * 86_400, ts), "Monday");
            assert_eq!(feed_card_age(tr, 9 * 86_400, ts), "9d ago");
            for ago in [30, 300, 7_200, 50_000, 100_000, 3 * 86_400, 9 * 86_400] {
                assert_eq!(
                    feed_card_age_upper(tr, ago, ts),
                    feed_card_age(tr, ago, ts).to_uppercase(),
                    "{ago}"
                );
            }
        });
    }

    #[test]
    fn times_follow_the_clock_form() {
        with_tr(|tr| {
            assert_eq!(time_of_day(tr, 23, 14, ClockForm::H24), "23:14");
            assert_eq!(time_of_day(tr, 0, 5, ClockForm::H24), "00:05");
            assert_eq!(time_of_day(tr, 23, 14, ClockForm::H12), "11:14 PM");
            assert_eq!(time_of_day(tr, 0, 5, ClockForm::H12), "12:05 AM");
            assert_eq!(time_of_day(tr, 12, 0, ClockForm::H12), "12:00 PM");

            // 2026-10-24 23:30 UTC.
            let now = 1_792_884_600_000;
            let ts = now - 16 * 60_000;
            let at = |ts, now, tz| today_or_yesterday_at(tr, ts, now, tz, ClockForm::H24);
            assert_eq!(at(ts, now, 0), "Today 23:14");
            assert_eq!(at(ts, now, 60), "Today 00:14");
            let early = now - 22 * 3_600_000;
            assert_eq!(at(early - 3 * 3_600_000, early, 0), "Yesterday 22:30");
            assert_eq!(
                today_or_yesterday_at(tr, ts, now, 0, ClockForm::H12),
                "Today 11:14 PM"
            );
        });
    }

    #[test]
    fn a_test_gets_24_hours_unless_it_sets_the_form() {
        assert_eq!(clock_form(), ClockForm::H24);
        set_test_clock_form(ClockForm::H12);
        assert_eq!(clock_form(), ClockForm::H12);
        set_test_clock_form(ClockForm::H24);
    }

    #[test]
    fn the_os_pattern_gives_the_clock_form() {
        assert_eq!(clock_form_of_pattern("HH:mm"), ClockForm::H24);
        assert_eq!(clock_form_of_pattern("H:mm"), ClockForm::H24);
        assert_eq!(clock_form_of_pattern("h:mm tt"), ClockForm::H12);
        assert_eq!(clock_form_of_pattern("tt hh:mm"), ClockForm::H12);
        assert_eq!(clock_form_of_pattern("'h' HH:mm"), ClockForm::H24);
        assert_eq!(clock_form_of_pattern(""), ClockForm::H24);
    }

    #[test]
    fn chat_days_and_times_read_as_today() {
        with_tr(|tr| {
            let h24 = ClockForm::H24;
            assert_eq!(chat_day(tr, Some(&DayLabel::Today)), "Today");
            assert_eq!(chat_day(tr, Some(&DayLabel::Yesterday)), "Yesterday");
            assert_eq!(
                chat_day(tr, Some(&DayLabel::Weekday(day(2026, 11, 1, 0)))),
                "Sunday 1 Nov"
            );
            assert_eq!(
                chat_day(tr, Some(&DayLabel::Weekday(day(2026, 10, 28, 3)))),
                "Wednesday 28 Oct"
            );
            assert_eq!(
                chat_day(tr, Some(&DayLabel::ThisYear(day(2026, 10, 27, 2)))),
                "27 Oct"
            );
            assert_eq!(
                chat_day(tr, Some(&DayLabel::Older(day(2025, 9, 29, 1)))),
                "29 Sep 2025"
            );
            assert_eq!(chat_day(tr, None), "");

            assert_eq!(chat_time(tr, Some(&MessageTime::JustNow), h24), "just now");
            assert_eq!(
                chat_time(tr, Some(&MessageTime::MinutesAgo(5)), h24),
                "5m ago"
            );
            let clock = MessageTime::Clock { hour: 9, minute: 3 };
            assert_eq!(chat_time(tr, Some(&clock), h24), "09:03");
            assert_eq!(chat_time(tr, Some(&clock), ClockForm::H12), "9:03 AM");
            assert_eq!(
                chat_time(tr, Some(&MessageTime::Yesterday), h24),
                "Yesterday"
            );
            assert_eq!(
                chat_time(tr, Some(&MessageTime::ThisYear(day(2026, 10, 27, 2))), h24),
                "Oct 27"
            );
            assert_eq!(
                chat_time(tr, Some(&MessageTime::Older(day(2025, 10, 27, 1))), h24),
                "Oct 27, 2025"
            );
            assert_eq!(
                chat_time(tr, Some(&MessageTime::Unparsed("x".into())), h24),
                "x"
            );
            assert_eq!(chat_time(tr, None, h24), "");
        });
    }

    #[test]
    fn durations_read_as_today() {
        with_tr(|tr| {
            assert_eq!(duration_hm_short(tr, 47), "47m");
            assert_eq!(duration_hm_short(tr, 83), "1h 23m");
            assert_eq!(duration_hm_short(tr, 0), "0m");
            assert_eq!(duration_hm_compact(tr, 252), "4h 12m");
            assert_eq!(duration_hm_compact(tr, 120), "2h");
            assert_eq!(duration_hm_compact(tr, 45), "45m");
            assert_eq!(duration_hours_decimal_short(tr, 150.0), "2.5h");
            assert_eq!(duration_hours_decimal_short(tr, 45.0), "45m");
            assert_eq!(duration_h_mm(tr, 400), "6 h 40");
            assert_eq!(duration_h_mm(tr, 180), "3 h 00");
            assert_eq!(duration_h_mm(tr, 48), "48 min");
            assert_eq!(duration_clock(tr, 45), "0:45");
            assert_eq!(duration_clock(tr, 723), "12:03");
            assert_eq!(hours_upper(tr, 250_020), "4,167 H");
            assert_eq!(hours_upper(tr, 756), "12.6 H");
            assert_eq!(hours_upper(tr, 2), "0.1 H");
            assert_eq!(play_time(tr, 45), "45 min");
            assert_eq!(play_time(tr, 240), "4 h");
            assert_eq!(play_time(tr, 270), "4.5 h");
            assert_eq!(play_time(tr, 250_020), "4,167 h");
            assert_eq!(play_time(tr, 0), "");
            assert_eq!(since(tr, 0), "since 1 min");
            assert_eq!(since(tr, 65), "since 1 h 5 min");
            assert_eq!(since(tr, 120), "since 2 h");
        });
    }

    #[test]
    fn lists_read_as_today() {
        with_tr(|tr| {
            let s = |v: &[&str]| v.iter().map(|x| x.to_string()).collect::<Vec<_>>();
            assert_eq!(list_and(tr, &s(&[])), "");
            assert_eq!(list_and(tr, &s(&["Bob"])), "Bob");
            assert_eq!(list_and(tr, &s(&["Bob", "Max"])), "Bob and Max");
            assert_eq!(
                list_and(tr, &s(&["Bob", "Max", "Sara"])),
                "Bob, Max and Sara"
            );
            assert_eq!(
                list_comma(tr, &s(&["Bob", "Max", "Sara"])),
                "Bob, Max, Sara"
            );
            assert_eq!(list_plus(tr, &s(&["Bob", "Max"]), 3), "Bob, Max +3");
            assert_eq!(list_plus(tr, &s(&["Bob", "Max"]), 0), "Bob, Max");
            assert_eq!(
                list_and_more(tr, &s(&["Bob", "Max"]), 3),
                "Bob, Max and 3 more"
            );
            assert_eq!(dot_pair(tr, "a", "b"), "a · b");
            assert_eq!(percent(tr, 62), "62%");
            assert_eq!(approx(tr, "1h"), "~1h");
        });
    }

    #[test]
    fn file_sizes_read_like_a_file_browser() {
        with_tr(|tr| {
            assert_eq!(file_size(tr, 880_640), "860 KB");
            assert_eq!(file_size(tr, 1_258_291), "1.2 MB");
            assert_eq!(file_size(tr, 10), "1 KB");
        });
    }

    #[test]
    fn numbers_get_the_group_separator() {
        assert_eq!(group_digits(4201, ","), "4,201");
        assert_eq!(group_digits(1_234_567, " "), "1 234 567");
        assert_eq!(group_digits(-4167, "."), "-4.167");
        assert_eq!(group_digits(999, ","), "999");
        assert_eq!(group_digits(i64::MIN, ""), i64::MIN.to_string());
        with_tr(|tr| assert_eq!(grouped(tr, 4201), "4,201"));
    }

    /// Under the pseudo language each text changes and keeps its values.
    #[cfg(debug_assertions)]
    #[test]
    fn the_text_follows_the_ui_language() {
        use crate::build_l10n::{pseudo, PSEUDO};
        with_tr(|tr| {
            crate::locale::select(PSEUDO).expect("select qps");
            let october = pseudo("October");
            assert_eq!(date_label(tr, MON_5_OCT), format!("5 {october}"));
            let start = MON_5_OCT + 7 * DAY;
            assert_eq!(
                week_range(tr, start, start + 7 * DAY),
                pseudo("{0} to {1} {2}")
                    .replace("{0}", "12")
                    .replace("{1}", "18")
                    .replace("{2}", &october)
            );
            let now = MON_5_OCT + 3_600_000;
            assert_eq!(
                days_ago(tr, MON_5_OCT - 2 * DAY, now),
                pseudo("{n} days ago").replace("{n}", "2")
            );
            assert_eq!(days_ago(tr, now, now), pseudo("Today"));
            assert_eq!(
                time_of_day(tr, 23, 14, ClockForm::H12),
                format!("11:14 {}", pseudo("PM"))
            );
            assert_eq!(time_of_day(tr, 23, 14, ClockForm::H24), "23:14");
            assert_eq!(duration_h_mm(tr, 48), pseudo("{} min").replace("{}", "48"));
            assert_ne!(duration_h_mm(tr, 48), "48 min");
            assert_eq!(
                chat_day(tr, Some(&DayLabel::Weekday(day(2026, 11, 1, 0)))),
                format!("{} 1 {}", pseudo("Sunday"), pseudo("Nov"))
            );
            let names: Vec<String> = ["Bob", "Max", "Sara"].map(String::from).to_vec();
            assert_eq!(
                list_and(tr, &names),
                pseudo("{0} and {1}")
                    .replace("{0}", "Bob, Max")
                    .replace("{1}", "Sara")
            );
            // A pattern and the separator have no letter: no change.
            assert_eq!(grouped(tr, 4201), "4,201");
            crate::locale::select_source_language();
            assert_eq!(date_label(tr, MON_5_OCT), "5 October");
        });
    }
}
