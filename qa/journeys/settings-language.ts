// SET-04: the Language setting (specs/features/LOCALIZATION.md section 9).
//
// lena runs in English from her Language setting, as a release build does
// (the driver's default, MELLO_LANG=en, comes before the setting). She picks
// 中文（简体）: the restart panel shows, "Keep English" takes the choice back.
// She picks it again and presses "Restart now": the app quits, starts itself
// again (client/src/restart.rs, a new process) and shows Chinese, signed in
// as before. In Chinese she picks Svenska and restarts into Swedish, and the
// journey ends in Swedish with the crew feed on screen. The account language
// (`lang_tag`) follows each choice.
//
// The labels are the translations of client/translations/<lang>. A member
// whose name is in Han script ("小明") is in the crew: the journey reads the
// pixels of the name and of a Chinese label, so a missing font fallback fails
// it (lib/glyphs.ts).
//
// Swedish writes a decimal comma. The last step fails when the app logs
// "Error while parsing path commands" while it draws the feed in Swedish: the
// outlines of the cards and the octagons are then not drawn.
//
// Where the Language row is in the settings: lib/settings.ts, LANGUAGE_TAB.
//
// Not covered: "System" (the OS language differs per machine), and the
// restart on Windows (App.awaitSelfRestart finds the new process with pgrep).

import type { App } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { hasGlyphs, textInk } from "./lib/glyphs.ts";
import { onboardWithNewCrew, type Credentials } from "./lib/onboarding.ts";
import { logLines, logMark, waitNoControl, waitText } from "./lib/screen.ts";
import { accountLangTag, profilesFrom, seedMember, signInEmail } from "./lib/seed.ts";
import { openLanguageRow, pickLanguage, type UiLanguage } from "./lib/settings.ts";
import { readInviteCode } from "./lib/setup.ts";

/** The labels of one UI language that the journey reads, as the catalog has them. */
type Labels = {
  tag: UiLanguage;
  restartNow: string;
  settings: string;
  accountMenu: string;
  lookingToPlay: string;
  newCrew: string;
  discover: string;
  crewMenu: string;
  crewSettings: string;
  members: string;
  inviteYourCrew: string;
};

const EN: Labels = {
  tag: "en",
  restartNow: "Restart now",
  settings: "Settings",
  accountMenu: "Account menu",
  lookingToPlay: "Looking to play",
  newCrew: "New crew",
  discover: "Discover",
  crewMenu: "Crew menu",
  crewSettings: "Crew settings",
  members: "MEMBERS",
  inviteYourCrew: "Invite your crew",
};

const ZH: Labels = {
  tag: "zh-CN",
  restartNow: "立即重启",
  settings: "设置",
  accountMenu: "账号菜单",
  lookingToPlay: "找人开黑",
  newCrew: "新建小队",
  discover: "发现",
  crewMenu: "小队菜单",
  crewSettings: "小队设置",
  members: "成员",
  inviteYourCrew: "邀请你的小队",
};

const SV: Labels = {
  tag: "sv",
  restartNow: "Starta om nu",
  settings: "Inställningar",
  accountMenu: "Kontomeny",
  lookingToPlay: "Vill lira",
  newCrew: "Nytt gäng",
  discover: "Upptäck",
  crewMenu: "Gängmeny",
  crewSettings: "Gänginställningar",
  members: "MEDLEMMAR",
  inviteYourCrew: "Bjud in gänget",
};

/** The controls of the main screen that every language must name in its own words. */
const MAIN: (keyof Labels)[] = ["settings", "accountMenu", "lookingToPlay", "newCrew", "discover", "crewMenu"];

const PATH_ERROR = "Error while parsing path commands";

export default journey({
  id: "settings.language",
  flows: ["SET-04"],
  async run(ctx) {
    const { runId, launch, step, expect } = ctx;
    const crew = `Lang ${runId}`;
    const name = `lena${runId}`;
    const han = profilesFrom(3, 1, runId)[0];
    let creds: Credentials = { email: "", password: "" };

    /** The main screen names its controls in this language, and in no other one. */
    const expectMainIn = async (app: App, l: Labels) => {
      const s = await app.waitFor(
        `the app with ${crew} active`,
        (st) => st.screen === "app" && st.logged_in && st.crews.includes(crew) && st.active_crew_id !== "",
        30_000,
      );
      expect(s.ui_language === l.tag, `the UI language is ${l.tag}, got "${s.ui_language}"`);
      expect(s.user_name === name, `signed in as ${name}, got "${s.user_name}"`);
      for (const key of MAIN) await app.find(l[key]);
      const on = (await app.controls()).map((c) => c.label);
      for (const other of [EN, SV, ZH]) {
        if (other === l) continue;
        for (const key of MAIN) expect(!on.includes(other[key]), `no "${other[key]}" on a screen in ${l.tag}`);
      }
    };

    /** The name in Han script shows glyphs in the member list of crew settings. */
    const expectHanName = async (app: App, l: Labels) => {
      await app.click(l.crewMenu);
      await app.activate(l.crewSettings);
      await app.waitFor("crew settings open", (s) => s.open_modals.includes("crew_settings"), 5_000);
      await app.click(l.members);
      await app.waitFor(`${han.name} in the member list`, (s) => s.crew_members.some((m) => m.id !== "" && m.name === han.name));
      const ink = await textInk(app, han.name);
      await app.checkpoint("han-name");
      expect(hasGlyphs(ink), `"${han.name}" shows two different glyphs, got ${JSON.stringify(ink)}`);
    };

    /** The account language on the server becomes `tag`. */
    const expectAccountLanguage = async (tag: string) => {
      const token = await signInEmail(creds.email, creds.password);
      const deadline = Date.now() + 10_000;
      let got = "";
      do got = await accountLangTag(token);
      while (got !== tag && Date.now() < deadline);
      expect(got === tag, `lang_tag is ${tag}, got "${got}"`);
    };

    /** Press the restart button of the panel: a new process comes up in the language of `l`. */
    const restartInto = async (app: App, button: string, l: Labels) => {
      const before = app.pid;
      await app.click(button);
      await app.awaitSelfRestart();
      expect(app.pid !== undefined && app.pid !== before, `a new process, got ${app.pid} after ${before}`);
      await expectMainIn(app, l);
      expect(!(await app.state()).open_modals.includes("settings"), "the settings are closed after the restart");
    };

    const lena = await launch("lena", { language: "en" });

    await step("lena's setting is English: she onboards in English, and a member with a Han name joins", async () => {
      expect((await lena.state()).ui_language === "en", "the UI language is en");
      creds = await onboardWithNewCrew(lena, crew, name, "Public");
      expect(han.name === "小明", `the seeded name is in Han script, got "${han.name}"`);
      await seedMember(await readInviteCode(lena), han.name, han, runId);
      await lena.waitFor(`${han.name} is a member`, (s) => s.members.includes(han.name), 20_000);
      await expectMainIn(lena, EN);
    });

    await step("the Han name shows glyphs in an English UI", async () => {
      await expectHanName(lena, EN);
      await lena.click("Close");
      await lena.waitFor("crew settings closed", (s) => !s.open_modals.includes("crew_settings"), 5_000);
    });

    await step("lena picks 中文（简体）: the restart panel shows, and the UI stays English", async () => {
      await openLanguageRow(lena);
      expect((await lena.controls()).every((c) => c.label !== "Restart now"), "no restart panel before a change");
      await pickLanguage(lena, "中文（简体）");
      await lena.find("Restart now");
      await lena.find("Keep English");
      expect(await waitText(lena, "m3llo restarts to switch to 中文（简体）."), "the panel names 中文（简体）");
      expect((await lena.state()).ui_language === "en", "the language of this run is still en");
      await lena.checkpoint("panel-chinese");
    });

    await step("\"Keep English\" takes the choice back: the panel goes, the account language is en", async () => {
      await lena.click("Keep English");
      expect(await waitNoControl(lena, "Restart now"), "no restart panel after Keep English");
      await expectAccountLanguage("en");
    });

    await step("lena picks 中文（简体）again and restarts: a new process shows Chinese, signed in as before", async () => {
      await pickLanguage(lena, "中文（简体）");
      await lena.find("Restart now");
      await restartInto(lena, "Restart now", ZH);
      await expectAccountLanguage("zh-CN");
      await lena.checkpoint("chinese");
    });

    await step("in Chinese, the Han name and a Chinese label show glyphs", async () => {
      await expectHanName(lena, ZH);
      const ink = await textInk(lena, ZH.members);
      expect(hasGlyphs(ink), `the tab "${ZH.members}" shows two different glyphs, got ${JSON.stringify(ink)}`);
      await lena.click("关闭");
      await lena.waitFor("crew settings closed", (s) => !s.open_modals.includes("crew_settings"), 5_000);
    });

    let swedishStart = 0;
    await step("in Chinese, lena picks Svenska: the panel is in Chinese, and she restarts into Swedish", async () => {
      await openLanguageRow(lena, "zh-CN");
      expect((await lena.controls()).every((c) => c.label !== ZH.restartNow), "no restart panel: the setting is the language of this run");
      await pickLanguage(lena, "Svenska", "zh-CN");
      await lena.find(ZH.restartNow);
      await lena.find("保留中文（简体）");
      expect(await waitText(lena, "切换到Svenska需要重启 m3llo。"), "the panel is in Chinese and names Svenska");
      await lena.checkpoint("panel-svenska");
      swedishStart = logMark(lena);
      await restartInto(lena, ZH.restartNow, SV);
      await expectAccountLanguage("sv");
    });

    await step("in Swedish, the crew feed draws with no path error in the log", async () => {
      // The feed of the crew: the quest card, the board and the People zone
      // with its octagons. The screenshot comes after the app drew them.
      const s = await lena.waitFor("the feed with two members", (st) => st.feed_zones.people.shown && st.feed_zones.people.members === 2, 30_000);
      expect(s.ui_language === "sv", "the feed is on screen in Swedish");
      await lena.scrollToControl(SV.inviteYourCrew);
      await lena.find(SV.inviteYourCrew);
      await lena.checkpoint("swedish-feed");
      const errors = logLines(lena, PATH_ERROR, swedishStart);
      expect(errors.length === 0, `no "${PATH_ERROR}" in the log of the Swedish run, got ${errors.length}, the first: ${errors[0]}`);
    });

    await step("no Error events", async () => {
      const errors = (await lena.events()).filter((e) => e.type === "Error");
      expect(errors.length === 0, `no Error events, got: ${errors.map((e) => e.message).join(" | ")}`);
    });
  },
});
