// LTP-01: alice asks the crew from the Looking to play pill. bob sees the
// card, presses In and joins voice. alice joins the same channel, then
// cancels the ask. Both see the ask closed. Both calls run through the local
// SFU. Any other call fails the journey.
//
// Not asserted: the closed state of the card in the snapshot. A closed ask is
// left out of `looking_to_play.asks`, so the journey reads the line of the
// closed card ("<asker> called it off") from the screen.

import { voiceMembers, type App, type AppState, type Ask } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { expectSfuVoice } from "../../tools/mello-driver/src/voice.ts";
import { twoUsersInOneCrew } from "./lib/setup.ts";
import { joinVoice } from "./lib/voice.ts";

const ANSWERS = ["In", "In 15", "In 30", "Not now"];

/** The one ask on screen, or null. */
function onlyAsk(s: AppState): Ask | null {
  return s.looking_to_play.asks.length === 1 ? s.looking_to_play.asks[0] : null;
}

function labelled(controls: { label: string }[], label: string): number {
  return controls.filter((c) => c.label === label).length;
}

export default journey({
  id: "looking-to-play.two-users",
  flows: ["LTP-01"],
  voice: true,
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, aliceName, bobName } = await twoUsersInOneCrew(ctx);
    const aliceId = (await alice.state()).user_id;
    const bobId = (await bob.state()).user_id;
    let askId = "";
    let channel = "";

    await step("setup: neither is in voice, and the crew has no ask", async () => {
      for (const u of [alice, bob]) {
        const s = await u.state();
        expect(!s.in_voice, `${u.name} is not in voice`);
        expect(s.looking_to_play.asks.length === 0, `${u.name} sees no ask`);
      }
    });

    await step("alice opens the Looking to play popover: Everyone is picked", async () => {
      await alice.click("Looking to play");
      await alice.waitFor("the popover is open", (s) => s.looking_to_play.popover_open, 5_000);
      const everyone = await alice.find("Everyone");
      expect(everyone.checked, "Everyone is picked");
      await alice.checkpoint("popover-open");
    });

    await step("alice asks the crew: the popover closes, her card shows Cancel", async () => {
      await alice.click("Ask the crew");
      const s = await alice.waitFor("alice has one open ask", (st) => {
        const a = onlyAsk(st);
        return a !== null && a.state === "open" && !st.looking_to_play.popover_open;
      });
      const a = onlyAsk(s)!;
      askId = a.ask_id;
      expect(a.asker_id === aliceId, `alice is the asker, got "${a.asker_id}"`);
      expect(a.members_in.includes(aliceId), "the asker counts as in");
      expect(s.looking_to_play.pip, "the pill carries the pip");
      await alice.find("Cancel the ask");
      const controls = await alice.controls();
      expect(labelled(controls, "In") === 0, "alice, the asker, has no In on the card");
      await alice.checkpoint("asked");
    });

    await step("bob sees the open ask, with the four answers and no toast", async () => {
      const s = await bob.waitFor("bob sees the ask", (st) => onlyAsk(st)?.ask_id === askId);
      const a = onlyAsk(s)!;
      expect(a.state === "open", `the ask is open, got "${a.state}"`);
      expect(a.asker_id === aliceId, `alice is the asker, got "${a.asker_id}"`);
      expect(a.my_answer === "", `bob has no answer yet, got "${a.my_answer}"`);
      expect(s.looking_to_play.pip, "bob's pill carries the pip");
      expect(s.looking_to_play.toast === null, `no toast with the card on screen, got "${s.looking_to_play.toast}"`);
      for (const label of ANSWERS) await bob.find(label);
      const ins = (await bob.controls()).filter((c) => c.label === "In");
      expect(ins.length === 1, `one In on bob's screen: the card, not a toast. Found: ${JSON.stringify(ins)}`);
      await bob.checkpoint("bob-sees-ask");
    });

    await step("bob presses In: he is in and joins voice through the SFU", async () => {
      await bob.click("In");
      const s = await bob.waitFor("bob is in and in voice", (st) => onlyAsk(st)?.my_answer === "in" && st.in_voice, 20_000);
      await expectSfuVoice(bob);
      const c = s.voice_channels.find((ch) => ch.members.some((m) => m.id === bobId));
      expect(c !== undefined, "bob is listed in a voice channel of the crew");
      channel = c!.name;
      await bob.waitFor(`bob listed in ${channel}`, (st) => voiceMembers(st, channel).includes(bobName));
      await bob.checkpoint("bob-in");
    });

    await step("alice sees the ask on, with bob in", async () => {
      await alice.waitFor("the ask is on with bob in", (s) => {
        const a = onlyAsk(s);
        return a !== null && a.state === "on" && a.members_in.includes(bobId);
      });
    });

    await step("alice joins bob's channel through the SFU, both see both", async () => {
      await joinVoice(alice, channel);
      for (const u of [alice, bob]) {
        await u.waitFor(`${u.name} sees both in ${channel}`, (s) => {
          const m = voiceMembers(s, channel);
          return m.includes(aliceName) && m.includes(bobName);
        });
      }
      await alice.checkpoint("both-in-voice");
    });

    await step("alice cancels the ask: both see it closed and the pip gone", async () => {
      await alice.click("Cancel the ask");
      for (const u of [alice, bob]) {
        await u.waitFor(`${u.name} has no open ask and no pip`, (s) => s.looking_to_play.asks.length === 0 && !s.looking_to_play.pip);
        const line = `${aliceName} called it off`;
        expect((await u.text(line)) !== null, `${u.name}'s card shows "${line}"`);
        const controls = await u.controls();
        for (const label of [...ANSWERS, "Change answer", "Cancel the ask"]) {
          expect(labelled(controls, label) === 0, `${u.name}'s closed card has no "${label}"`);
        }
        await u.checkpoint("ask-closed");
      }
    });

    await step("both leave voice", async () => {
      for (const u of [alice, bob] as App[]) {
        await u.click("Leave voice");
        await u.waitFor(`${u.name} out of voice`, (s) => !s.in_voice);
      }
    });

    await step("no Error events on either app", async () => {
      for (const u of [alice, bob]) {
        const errors = (await u.events()).filter((e) => e.type === "Error");
        expect(errors.length === 0, `no Error events on ${u.name}, got: ${errors.map((e) => e.message).join(" | ")}`);
      }
    });
  },
});
