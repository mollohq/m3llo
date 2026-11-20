// Steps in the settings modal ("SYSTEM PARAMETERS").
//
// Where a setting is, is in one place here: LANGUAGE_TAB and
// DELETE_ACCOUNT_TAB. When a row moves to another tab, change the constant.
// No journey names a settings tab itself.

import type { App } from "../../../tools/mello-driver/src/app.ts";

/** The UI languages that the journeys run the settings in. */
export type UiLanguage = "en" | "sv" | "zh-CN";

/** A tab of the settings. */
export type SettingsTab = "profile" | "general";

/** The tab with the Language row. */
export const LANGUAGE_TAB: SettingsTab = "general";

/** The tab with the ACCOUNT section and "Delete account". */
export const DELETE_ACCOUNT_TAB: SettingsTab = "profile";

/** The labels on the way to a settings row, as the catalogs have them (client/translations). */
const LABELS: Record<UiLanguage, { settings: string; language: string; tabs: Record<SettingsTab, string> }> = {
  en: { settings: "Settings", language: "Language", tabs: { profile: "PROFILE", general: "GENERAL" } },
  sv: { settings: "Inställningar", language: "Språk", tabs: { profile: "PROFIL", general: "ALLMÄNT" } },
  "zh-CN": { settings: "设置", language: "语言", tabs: { profile: "个人资料", general: "通用" } },
};

/** The label of the Language dropdown in a UI language. */
export function languageLabel(ui: UiLanguage = "en"): string {
  return LABELS[ui].language;
}

/** Open the settings from the control bar, on a tab. The tab is always clicked: the modal can keep another one. */
export async function openSettings(app: App, tab: SettingsTab, ui: UiLanguage = "en"): Promise<void> {
  await app.click(LABELS[ui].settings);
  await app.waitFor("settings open", (s) => s.open_modals.includes("settings"), 5_000);
  await app.click(LABELS[ui].tabs[tab]);
}

/**
 * Bring a control of the open settings page on screen. The page scrolls
 * under the pointer, and the page is the right part of the modal, which is in
 * the centre of the window: the pointer goes at 62 % of the width and half of
 * the height. A control that is on screen already needs no scroll.
 */
export async function scrollSettingsTo(app: App, label: string): Promise<void> {
  const [win] = await app.ui.props([await app.ui.window()]);
  if (!win?.size) throw new Error(`${app.name}: the window has no size`);
  await app.scrollToControl(label, win.size.width * 0.62, win.size.height / 2, 20);
  await app.find(label);
}

/** Open the settings and bring the "Delete account" row on screen. */
export async function openDeleteAccountRow(app: App): Promise<void> {
  await openSettings(app, DELETE_ACCOUNT_TAB);
  await scrollSettingsTo(app, "Delete account");
}

/** Open the delete confirm: the settings, the "Delete account" row, its button. */
export async function openDeleteAccount(app: App): Promise<void> {
  await openDeleteAccountRow(app);
  await app.click("Delete account");
  await app.find("Delete my account");
}

/** Open the settings and bring the Language row on screen. */
export async function openLanguageRow(app: App, ui: UiLanguage = "en"): Promise<void> {
  await openSettings(app, LANGUAGE_TAB, ui);
  await scrollSettingsTo(app, LABELS[ui].language);
}

/** Pick a language in the dropdown by the name that it shows. An item of a PopupWindow takes `activate`. */
export async function pickLanguage(app: App, name: string, ui: UiLanguage = "en"): Promise<void> {
  await app.click(LABELS[ui].language);
  await app.activate(name);
}

/**
 * Change the language of an app that shows English, the way a user does:
 * the Language row, the new language, "Restart now". The app starts itself
 * again. The app must run from its Language setting
 * (`launch(name, { language })`). `tag` is the tag of the new language on the
 * state port ("sv", "zh-CN").
 */
export async function switchLanguage(app: App, name: string, tag: string): Promise<void> {
  await openLanguageRow(app);
  await pickLanguage(app, name);
  await app.find("Restart now");
  await app.click("Restart now");
  await app.awaitSelfRestart();
  await app.waitFor(`the app in ${tag}`, (s) => s.screen === "app" && s.logged_in && s.ui_language === tag && s.active_crew_id !== "", 30_000);
}
