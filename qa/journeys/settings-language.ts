// SET-04: the Language setting (specs/features/LOCALIZATION.md section 9).
//
// lena runs in English from her Language setting, as a release build does
// (the driver's default, MELLO_LANG=en, comes before the setting). She picks
// Svenska: the restart panel shows, "Keep English" takes the choice back. She
// picks Svenska again and presses "Restart now": the app quits, starts itself
// again (client/src/restart.rs, a new process) and shows Swedish, signed in
// as before. In Swedish she picks 中文（简体）and restarts into Chinese. The
// account language (`lang_tag`) follows each choice.
//
// The labels are the translations of client/translations/<lang>. A member
// whose name is in Han script ("小明") is in the crew: the journey reads the
// pixels of the name and of a Chinese label, so a missing font fallback fails
// it (lib/glyphs.ts).
//
// Not covered: "System" (the OS language differs per machine), and the
// restart on Windows (App.awaitSelfRestart finds the new process with pgrep).

import type { App } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { hasGlyphs, textInk } from "./lib/glyphs.ts";
import { onboardWithNewCrew, type Credentials } from "./lib/onboarding.ts";
import { accountLangTag, profilesFrom, seedMember, signInEmail } from "./lib/seed.ts";
import { waitNoControl, waitText } from "./lib/screen.ts";
import { openLanguageRow, pickLanguage } from "./lib/settings.ts";
import { readInviteCode } from "./lib/setup.ts";

/** The labels of one UI language that the journey uses, as the catalog has them. */
type Labels = {
  settings: string;
  general: string;
  language: string;
  restartNow: string;
  accountMenu: string;
  lookingToPlay: string;
  newCrew: string;
  discover: string;
  crewMenu: string;
  crewSettings: string;
  members: string;
};

const EN: Labels = {
  settings: "Settings",
  general: "GENERAL",
  language: "Language",
  restartNow: "Restart now",
  accountMenu: "Account menu",
  lookingToPlay: "Looking to play",
  newCrew: "New crew",
  discover: "Discover",
  crewMenu: "Crew menu",
  crewSettings: "Crew settings",
  members: "MEMBERS",
};

const SV: Labels = {
  settings: "Inställningar",
  general: "ALLMÄNT",
  language: "Språk",
  restartNow: "Starta om nu",
  accountMenu: "Kontomeny",
  lookingToPlay: "Vill lira",
  newCrew: "Nytt gäng",
  discover: "Upptäck",
  crewMenu: "Gängmeny",
  crewSettings: "Gänginställningar",
  members: "MEDLEMMAR",
};

const ZH: Labels = {
  settings: "设置",
  general: "通用",
  language: "语言",
  restartNow: "立即重启",
  accountMenu: "账号菜单",
  lookingToPlay: "找人开黑",
  newCrew: "新建小队",
  discover: "发现",
  crewMenu: "小队菜单",
  crewSettings: "小队设置",
  members: "成员",
};

/** The controls of the main screen that every language must name in its own words. */
const MAIN: (keyof Labels)[] = ["settings", "accountMenu", "lookingToPlay", "newCrew", "discover", "crewMenu"];

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
    const expectMainIn = async (app: App, l: Labels, tag: string) => {
      const s = await app.waitFor(`the app with ${crew} active`, (st) => st.screen === "app" && st.logged_in && st.crews.includes(crew) && st.active_crew_id !== "", 30_000);
      expect(s.ui_language === tag, `the UI language is ${tag}, got "${s.ui_language}"`);
      expect(s.user_name === name, `signed in as ${name}, got "${s.user_name}"`);
      for (const key of MAIN) await app.find(l[key]);
      const on = (await app.controls()).map((c) => c.label);
      for (const other of [EN, SV, ZH]) {
        if (other === l) continue;
        for (const key of MAIN) expect(!on.includes(other[key]), `no "${other[key]}" on a screen in ${tag}`);
      }
    };

    /** The name in Han script shows glyphs in the member list of crew settings. */
    const expectHanName = async (app: App, l: Labels) => {
      await app.click(l.crewMenu);
      await app.activate(l.crewSettings);
      await app.waitFor("crew settings open", (s) => s.open_modals.includes("crew_settings"), 5_000);
      await app.click(l.members);
      await app.waitFor(`${han.name} in the member list`, (s) => s.crew_members.some((m) => m.name === han.name));
      const ink = await textInk(app, han.name);
      await app.checkpoint("han-name");
      expect(hasGlyphs(ink), `"${han.name}" shows two different glyphs, got ${JSON.stringify(ink)}`);
    };

    const lena = await launch("lena", { language: "en" });

    await step("lena's setting is English: she onboards in English, and a member with a Han name joins", async () => {
      expect((await lena.state()).ui_language === "en", "the UI language is en");
      creds = await onboardWithNewCrew(lena, crew, name, "Public");
      expect(han.name === "小明", `the seeded name is in Han script, got "${han.name}"`);
      await seedMember(await readInviteCode(lena), han.name, han, runId);
      await lena.waitFor(`${han.name} is a member`, (s) => s.members.includes(han.name), 20_000);
      await expectMainIn(lena, EN, "en");
    });

    await step("the Han name shows glyphs in an English UI", async () => {
      await expectHanName(lena, EN);
      await lena.click("Close");
      await lena.waitFor("crew settings closed", (s) => !s.open_modals.includes("crew_settings"), 5_000);
    });

    await step("lena picks Svenska: the restart panel shows, and the UI stays English", async () => {
      await openLanguageRow(lena, EN);
      expect((await lena.controls()).every((c) => c.label !== "Restart now"), "no restart panel before a change");
      await pickLanguage(lena, EN, "Svenska");
      await lena.find("Restart now");
      await lena.find("Keep English");
      expect(await waitText(lena, "m3llo restarts to switch to Svenska."), "the panel names Svenska");
      expect((await lena.state()).ui_language === "en", "the language of this run is still en");
      await lena.checkpoint("panel-svenska");
    });

    await step("\"Keep English\" takes the choice back: the panel goes, the account language is en", async () => {
      await lena.click("Keep English");
      expect(await waitNoControl(lena, "Restart now"), "no restart panel after Keep English");
      expect((await accountLangTag(await signInEmail(creds.email, creds.password))) === "en", "the account language is en");
    });

    await step("lena picks Svenska again and restarts: a new process shows Swedish, signed in as before", async () => {
      await pickLanguage(lena, EN, "Svenska");
      await lena.find("Restart now");
      const before = lena.pid;
      await lena.click("Restart now");
      await lena.awaitSelfRestart();
      expect(lena.pid !== undefined && lena.pid !== before, `a new process, got ${lena.pid} after ${before}`);
      await expectMainIn(lena, SV, "sv");
      expect(!(await lena.state()).open_modals.includes("settings"), "the settings are closed after the restart");
      await lena.checkpoint("swedish");
    });

    await step("the account language is sv", async () => {
      const token = await signInEmail(creds.email, creds.password);
      const deadline = Date.now() + 10_000;
      let tag = "";
      do tag = await accountLangTag(token);
      while (tag !== "sv" && Date.now() < deadline);
      expect(tag === "sv", `lang_tag is sv, got "${tag}"`);
    });

    await step("in Swedish, lena picks 中文（简体）: the panel is in Swedish, and she restarts into Chinese", async () => {
      await openLanguageRow(lena, SV);
      expect((await lena.controls()).every((c) => c.label !== SV.restartNow), "no restart panel: the setting is the language of this run");
      await pickLanguage(lena, SV, "中文（简体）");
      await lena.find(SV.restartNow);
      await lena.find("Behåll Svenska");
      expect(await waitText(lena, "m3llo startar om för att byta till 中文（简体）."), "the panel is in Swedish and names 中文（简体）");
      await lena.checkpoint("panel-chinese");
      const before = lena.pid;
      await lena.click(SV.restartNow);
      await lena.awaitSelfRestart();
      expect(lena.pid !== undefined && lena.pid !== before, `a new process, got ${lena.pid} after ${before}`);
      await expectMainIn(lena, ZH, "zh-CN");
      await lena.checkpoint("chinese");
    });

    await step("in Chinese, the Han name and a Chinese label show glyphs", async () => {
      await expectHanName(lena, ZH);
      const ink = await textInk(lena, ZH.members);
      expect(hasGlyphs(ink), `the tab "${ZH.members}" shows two different glyphs, got ${JSON.stringify(ink)}`);
    });

    await step("no Error events", async () => {
      const errors = (await lena.events()).filter((e) => e.type === "Error");
      expect(errors.length === 0, `no Error events, got: ${errors.map((e) => e.message).join(" | ")}`);
    });
  },
});
