// L10N-01: one event, each reader in the reader's own language
// (specs/features/LOCALIZATION.md sections 4, 9a and 9b, #256 and #258).
//
// alice runs mello in English and bob in Swedish. Three members have only a
// phone: tom with an English account, mei with a Simplified Chinese account
// and sven with a Swedish account. alice asks the crew with Looking to play.
// - alice's card says it in English, and bob's card in Swedish. The server
//   sends the ask as values and each app writes the sentence.
// - Each phone gets the push in its account language. The server writes that
//   text (push_text.go).
//
// The journey has its own push Worker stub (lib/push.ts). The Nakama of the
// run must post to it: see lib/push.ts.
//
// Not covered: the catch-up line and the recap awards in two languages (the
// harness tests the_catchup_line_follows_the_ui_language and
// recap_game_awards_follow_the_ui_language cover one app), and the text that a
// real phone shows.

import type { AppState, Ask } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { onboardFromInvite, onboardWithNewCrew } from "./lib/onboarding.ts";
import { deviceToken, startPushStub, type PushStub } from "./lib/push.ts";
import { waitText } from "./lib/screen.ts";
import { accountLangTag, seedPhoneMember, signInEmail } from "./lib/seed.ts";
import { switchLanguage } from "./lib/settings.ts";
import { readInviteCode, selectCrew } from "./lib/setup.ts";

function onlyAsk(s: AppState): Ask | null {
  return s.looking_to_play.asks.length === 1 ? s.looking_to_play.asks[0] : null;
}

export default journey({
  id: "l10n.readers",
  flows: ["L10N-01"],
  async run(ctx) {
    const { runId, launch, step, expect } = ctx;
    const crew = `Sprak ${runId}`;
    const aliceName = `alice${runId}`;
    const phones = { tom: deviceToken(), mei: deviceToken(), sven: deviceToken() };
    let stub: PushStub | null = null;

    try {
      stub = await startPushStub();
      const alice = await launch("alice");
      let code = "";
      await step("alice creates a crew in English and reads the invite link", async () => {
        await onboardWithNewCrew(alice, crew, aliceName, "Public");
        code = await readInviteCode(alice);
      });

      const bob = await launch("bob", { deeplink: `mello://join/${code}`, language: "en" });
      await step("bob joins by the link, then switches mello to Svenska and restarts", async () => {
        const creds = await onboardFromInvite(bob, crew, `bob${runId}`);
        await switchLanguage(bob, "Svenska", "sv");
        await bob.waitFor(`${crew} is on bob's screen`, (s) => s.crews.includes(crew) && s.active_crew_id !== "", 20_000);
        const token = await signInEmail(creds.email, creds.password);
        const deadline = Date.now() + 10_000;
        let tag = "";
        do tag = await accountLangTag(token);
        while (tag !== "sv" && Date.now() < deadline);
        expect(tag === "sv", `bob's account language is sv, got "${tag}"`);
      });

      await step("tom (en), mei (zh-CN) and sven (sv) join with a phone only", async () => {
        await seedPhoneMember(code, "Tom", "en", phones.tom, runId);
        await seedPhoneMember(code, "Mei", "zh-CN", phones.mei, runId);
        await seedPhoneMember(code, "Sven", "sv", phones.sven, runId);
        await alice.waitFor("alice sees five members", (s) => s.members.length === 5, 20_000);
        await selectCrew(alice, crew);
      });

      let askId = "";
      await step("alice asks the crew: her card is in English", async () => {
        await alice.click("Looking to play");
        await alice.waitFor("the popover is open", (s) => s.looking_to_play.popover_open, 5_000);
        await alice.click("Ask the crew");
        const s = await alice.waitFor("alice has one open ask", (st) => onlyAsk(st)?.state === "open");
        askId = onlyAsk(s)!.ask_id;
        expect(await waitText(alice, `${aliceName} is looking to play`), "alice's card says it in English");
        await alice.find("Cancel the ask");
        await alice.checkpoint("ask-english");
      });

      await step("bob's card of the same ask is in Swedish", async () => {
        await bob.waitFor("bob sees the ask", (s) => onlyAsk(s)?.ask_id === askId);
        expect(await waitText(bob, `${aliceName} vill lira`), "bob's card says it in Swedish");
        expect((await bob.text(`${aliceName} is looking to play`)) === null, "bob's screen does not say it in English");
        for (const label of ["Med", "Med om 15", "Med om 30", "Inte nu"]) await bob.find(label);
        await bob.checkpoint("ask-swedish");
      });

      await step("each phone gets the push of the ask in its account language", async () => {
        const want: [keyof typeof phones, string][] = [
          ["tom", `${aliceName} is looking to play`],
          ["mei", `${aliceName}想找人一起玩`],
          ["sven", `${aliceName} vill lira`],
        ];
        for (const [who, body] of want) {
          const p = await stub!.waitFor(phones[who], `the new ask for ${who}`, (x) => x.type === "looking_to_play" && x.ask_id === askId);
          expect(p.body === body, `${who}'s push says "${body}", got "${p.body}"`);
          expect(p.title === crew, `${who}'s push has the crew as its title, got "${p.title}"`);
        }
        expect(stub!.pushes().every((p) => p.ask_id !== askId || p.tokens.length === 1), "one device for each push of the ask");
      });

      await step("alice cancels the ask", async () => {
        await alice.click("Cancel the ask");
        await alice.waitFor("no open ask", (s) => s.looking_to_play.asks.length === 0);
      });
    } finally {
      await stub?.close();
    }
  },
});
