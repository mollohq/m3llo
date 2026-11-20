// Steps in the settings modal ("SYSTEM PARAMETERS"). The steps of the
// Language row take their labels, so a journey can use them in any language.

import type { App } from "../../../tools/mello-driver/src/app.ts";

/** Open the settings from the control bar. The modal opens on the PROFILE tab. */
export async function openSettings(app: App): Promise<void> {
  await app.click("Settings");
  await app.waitFor("settings open", (s) => s.open_modals.includes("settings"), 5_000);
}

/**
 * Bring a control of the open settings page on screen. The page scrolls
 * under the pointer, and the page is the right part of the modal, which is in
 * the centre of the window: the pointer goes at 62 % of the width and half of
 * the height.
 */
export async function scrollSettingsTo(app: App, label: string): Promise<void> {
  const [win] = await app.ui.props([await app.ui.window()]);
  if (!win?.size) throw new Error(`${app.name}: the window has no size`);
  await app.scrollToControl(label, win.size.width * 0.62, win.size.height / 2, 20);
  await app.find(label);
}

/** Settings, PROFILE, ACCOUNT: open the delete confirm. */
export async function openDeleteAccount(app: App): Promise<void> {
  await openSettings(app);
  await scrollSettingsTo(app, "Delete account");
  await app.click("Delete account");
  await app.find("Delete my account");
}

/** The labels of the way to the Language row, in the language of the run. */
export type LanguageLabels = { settings: string; general: string; language: string };

export const LANGUAGE_LABELS_EN: LanguageLabels = { settings: "Settings", general: "GENERAL", language: "Language" };

/** Open Settings on the GENERAL tab and bring the Language row on screen. */
export async function openLanguageRow(app: App, l: LanguageLabels = LANGUAGE_LABELS_EN): Promise<void> {
  await app.click(l.settings);
  await app.waitFor("settings open", (s) => s.open_modals.includes("settings"), 5_000);
  await app.click(l.general);
  // The row is under the fold of the page. The pointer goes on the first
  // switch of the page, whatever its label is in this language.
  const deadline = Date.now() + 5_000;
  let row = (await app.controls()).find((c) => c.role === "Switch");
  while (!row && Date.now() < deadline) row = (await app.controls()).find((c) => c.role === "Switch");
  if (!row) throw new Error(`${app.name}: the GENERAL page shows no switch`);
  await app.scrollToControl(l.language, row.x, row.y);
  await app.find(l.language);
}

/** Pick a language in the dropdown by the name that it shows. An item of a PopupWindow takes `activate`. */
export async function pickLanguage(app: App, l: LanguageLabels, name: string): Promise<void> {
  await app.click(l.language);
  await app.activate(name);
}

/**
 * Change the language of an app that shows English, the way a user does:
 * Settings, GENERAL, Language, "Restart now". The app starts itself again.
 * The app must run from its Language setting (`launch(name, { language })`).
 * `tag` is the tag of the new language on the state port ("sv", "zh-CN").
 */
export async function switchLanguage(app: App, name: string, tag: string): Promise<void> {
  await openLanguageRow(app);
  await pickLanguage(app, LANGUAGE_LABELS_EN, name);
  await app.find("Restart now");
  await app.click("Restart now");
  await app.awaitSelfRestart();
  await app.waitFor(`the app in ${tag}`, (s) => s.screen === "app" && s.logged_in && s.ui_language === tag && s.active_crew_id !== "", 30_000);
}
