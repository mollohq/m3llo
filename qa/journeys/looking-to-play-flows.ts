// LTP-02, LTP-03, LTP-04: the Looking to play flows that
// `looking-to-play.two-users` (LTP-01) does not cover
// (specs/features/LOOKING-TO-PLAY.md 3.1, 3.2, 3.6). No journey here joins
// voice: In 15, In 30, Not now and In on a toast of another crew only answer.
//
// - `answers`: bob answers In 15 (the ask goes on), changes to In 30, then to
//   Not now. The ask stays on. alice cancels. Her next ask in the same crew
//   is refused with the 10 minute line, and no ask opens.
// - `allNo`: bob, the only other member, answers Not now. The ask closes at
//   once and both cards say "No one this time".
// - `toastAndSwitch`: cara is a member with another crew on screen. alice's
//   ask shows her the toast. In on the toast answers, opens the crew of the
//   ask and joins no voice. cara then turns the Looking to play switch of the
//   crew off. bob's ask reaches her app and shows no toast.
//
// Not covered: the 30 minute wait after "No one this time" (ASK_NOBODY_WAIT;
// the 10 minute rule answers first), the close after 30 and 60 minutes, an ask
// to picked members or with a game, the sound, the OS notification and the
// phone pushes.

import type { App, AppState, Ask } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { crewMenu } from "./lib/crew.ts";
import { onboardWithNewCrew } from "./lib/onboarding.ts";
import { waitText } from "./lib/screen.ts";
import { selectCrew, twoUsersInOneCrew } from "./lib/setup.ts";

const ANSWERS = ["In", "In 15", "In 30", "Not now"];
const TOO_SOON = "You asked a short time ago. Try again in a few minutes.";

function onlyAsk(s: AppState): Ask | null {
  return s.looking_to_play.asks.length === 1 ? s.looking_to_play.asks[0] : null;
}

/** Open the popover and ask everyone. Returns the ask id when the ask opened. */
async function askTheCrew(app: App): Promise<string> {
  await app.click("Looking to play");
  await app.waitFor("the popover is open", (s) => s.looking_to_play.popover_open, 5_000);
  await app.click("Ask the crew");
  const s = await app.waitFor("one open ask, the popover closed", (st) => onlyAsk(st)?.state === "open" && !st.looking_to_play.popover_open);
  return onlyAsk(s)!.ask_id;
}

async function noErrors(apps: App[], expect: (cond: unknown, message: string) => void): Promise<void> {
  for (const u of apps) {
    const errors = (await u.events()).filter((e) => e.type === "Error");
    expect(errors.length === 0, `no Error events on ${u.name}, got: ${errors.map((e) => e.message).join(" | ")}`);
  }
}

export const answers = journey({
  id: "looking-to-play.answers",
  flows: ["LTP-02"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, aliceName } = await twoUsersInOneCrew(ctx);
    const bobId = (await bob.state()).user_id;
    let askId = "";

    await step("alice asks the crew", async () => {
      askId = await askTheCrew(alice);
      await bob.waitFor("bob sees the ask", (s) => onlyAsk(s)?.ask_id === askId);
    });

    await step("bob answers In 15: the ask is on, bob is due, and he joins no voice", async () => {
      await bob.click("In 15");
      const s = await bob.waitFor("bob's answer is in_15", (st) => onlyAsk(st)?.my_answer === "in_15" && onlyAsk(st)?.state === "on");
      expect(!s.in_voice, "In 15 joins no voice channel");
      await alice.waitFor("alice sees the ask on, with bob in", (st) => onlyAsk(st)?.state === "on" && onlyAsk(st)!.members_in.includes(bobId));
      await bob.find("Change answer");
      const labels = (await bob.controls()).map((c) => c.label);
      expect(!labels.includes("In 15") && !labels.includes("Not now"), "an answered card hides the other answers until Change");
      await bob.checkpoint("in-15");
    });

    await step("bob changes his answer to In 30", async () => {
      await bob.click("Change answer");
      for (const label of ANSWERS) await bob.find(label);
      await bob.click("In 30");
      await bob.waitFor("bob's answer is in_30", (st) => onlyAsk(st)?.my_answer === "in_30");
      await alice.waitFor("alice still sees bob in", (st) => onlyAsk(st)?.state === "on" && onlyAsk(st)!.members_in.includes(bobId));
    });

    await step("bob changes his answer to Not now: he is not in, and the ask stays on", async () => {
      await bob.click("Change answer");
      await bob.click("Not now");
      const s = await bob.waitFor("bob's answer is no", (st) => onlyAsk(st)?.my_answer === "no");
      expect(onlyAsk(s)!.state === "on", `an ask that is on does not go back to open, got "${onlyAsk(s)!.state}"`);
      expect(!s.in_voice, "bob is not in voice");
      const a = await alice.waitFor("alice sees bob out", (st) => onlyAsk(st) !== null && !onlyAsk(st)!.members_in.includes(bobId));
      expect(onlyAsk(a)!.state === "on", `alice's card is still on, got "${onlyAsk(a)!.state}"`);
      await alice.checkpoint("bob-not-now");
    });

    await step("alice cancels the ask: both cards say that she called it off", async () => {
      await alice.click("Cancel the ask");
      for (const u of [alice, bob]) {
        await u.waitFor(`${u.name} has no open ask and no pip`, (s) => s.looking_to_play.asks.length === 0 && !s.looking_to_play.pip);
        expect(await waitText(u, `${aliceName} called it off`), `${u.name}'s card says who called it off`);
      }
    });

    await step("alice asks again at once: the popover says to wait, and no ask opens", async () => {
      await alice.click("Looking to play");
      await alice.waitFor("the popover is open", (s) => s.looking_to_play.popover_open, 5_000);
      await alice.click("Ask the crew");
      expect(await waitText(alice, TOO_SOON, 10_000), `the popover shows "${TOO_SOON}"`);
      const s = await alice.state();
      expect(s.looking_to_play.popover_open, "the popover stays open");
      expect(s.looking_to_play.asks.length === 0 && !s.looking_to_play.pip, "alice has no open ask");
      expect((await alice.find("Ask the crew")).enabled, "the button works again");
      const b = await bob.state();
      expect(b.looking_to_play.asks.length === 0 && b.looking_to_play.toast === null, "bob gets no ask");
      await alice.checkpoint("too-soon");
    });

    await step("no Error events on either app", () => noErrors([alice, bob], expect));
  },
});

export const allNo = journey({
  id: "looking-to-play.all-no",
  flows: ["LTP-03"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob } = await twoUsersInOneCrew(ctx);
    let askId = "";

    await step("alice asks the crew", async () => {
      askId = await askTheCrew(alice);
      await bob.waitFor("bob sees the ask", (s) => onlyAsk(s)?.ask_id === askId);
    });

    await step("bob, the only other member, answers Not now: the ask closes for both", async () => {
      await bob.click("Not now");
      for (const u of [alice, bob]) {
        await u.waitFor(`${u.name} has no open ask and no pip`, (s) => s.looking_to_play.asks.length === 0 && !s.looking_to_play.pip);
        expect(await waitText(u, "No one this time"), `${u.name}'s card says "No one this time"`);
        const labels = (await u.controls()).map((c) => c.label);
        for (const label of [...ANSWERS, "Change answer", "Cancel the ask"]) {
          expect(!labels.includes(label), `${u.name}'s closed card has no "${label}"`);
        }
        expect(!(await u.state()).in_voice, `${u.name} is not in voice`);
        await u.checkpoint("no-one");
      }
    });

    await step("no Error events on either app", () => noErrors([alice, bob], expect));
  },
});

export const toastAndSwitch = journey({
  id: "looking-to-play.toast-and-switch",
  flows: ["LTP-04"],
  async run(ctx) {
    const { runId, launch, step, expect } = ctx;
    const { alice, bob, crew, aliceName, code } = await twoUsersInOneCrew(ctx);
    const home = `Cara Home ${runId}`;
    const cara = await launch("cara");
    let caraId = "";
    let askId = "";

    await step("cara has her own crew, joins alice's crew by link, and goes back to her own crew", async () => {
      await onboardWithNewCrew(cara, home, `cara${runId}`, "Public");
      caraId = (await cara.state()).user_id;
      await cara.openLink(`mello://join/${code}`);
      await cara.waitFor("the join modal", (s) => s.join_crew_modal_open && s.join_crew_name === crew);
      await cara.click("Join crew");
      await cara.waitFor("cara has both crews", (s) => s.crews.includes(crew) && s.crews.includes(home));
      await alice.waitFor("alice sees three members", (s) => s.members.length === 3, 20_000);
      await selectCrew(cara, home);
      const homeId = (await cara.state()).active_crew_id;
      await selectCrew(cara, crew);
      await selectCrew(cara, home);
      const s = await cara.waitFor("cara's own crew is on screen", (st) => st.active_crew_id === homeId);
      expect(s.looking_to_play.toast === null && s.looking_to_play.asks.length === 0, "no toast and no ask yet");
    });

    await step("alice asks: bob has the card and no toast, cara has the toast", async () => {
      askId = await askTheCrew(alice);
      const b = await bob.waitFor("bob sees the card", (s) => onlyAsk(s)?.ask_id === askId);
      expect(b.looking_to_play.toast === null, "bob has the card on screen, so no toast");
      const c = await cara.waitFor("cara has the toast of the ask", (s) => s.looking_to_play.toast === askId, 10_000);
      expect(c.looking_to_play.asks.length === 0, "the crew on cara's screen has no ask");
      expect(c.open_modals.length === 0, "the toast is no modal");
      expect(await waitText(cara, `${aliceName} is looking to play`), "the toast names the asker");
      for (const label of ANSWERS) await cara.find(label);
      await cara.checkpoint("toast");
    });

    await step("the toast does not block cara's window: she sends a chat message with it on screen", async () => {
      const text = `still here ${runId}`;
      await cara.type("Message crewmates", text);
      await cara.key("\n");
      const s = await cara.waitFor("cara's message is in her chat", (st) => st.messages.some((m) => m.text === text), 10_000);
      expect(s.looking_to_play.toast === askId, "the toast is still on screen");
    });

    await step("cara presses In on the toast: she is in, the crew of the ask opens, and she joins no voice", async () => {
      await cara.click("In");
      const s = await cara.waitFor(
        "the crew of the ask on screen with cara in",
        (st) => onlyAsk(st)?.ask_id === askId && onlyAsk(st)!.my_answer === "in" && st.looking_to_play.toast === null,
        15_000,
      );
      expect(!s.in_voice, "In on a toast of another crew joins no voice channel");
      await alice.waitFor("alice sees the ask on with cara in", (st) => onlyAsk(st)?.state === "on" && onlyAsk(st)!.members_in.includes(caraId));
      await cara.checkpoint("answered-on-toast");
    });

    await step("alice cancels the ask", async () => {
      await alice.click("Cancel the ask");
      for (const u of [alice, bob, cara]) {
        await u.waitFor(`${u.name} has no open ask`, (s) => s.looking_to_play.asks.length === 0 && !s.looking_to_play.pip);
      }
    });

    await step("cara turns the Looking to play switch of the crew off", async () => {
      await crewMenu(cara, "Notifications");
      await cara.waitFor("crew settings open", (s) => s.open_modals.includes("crew_settings"), 5_000);
      // The pill of the composer has the same label: the switch is the one
      // with the role Switch. It is enabled when the server gave its state.
      const deadline = Date.now() + 10_000;
      let index = -1;
      let all = (await cara.controls()).filter((c) => c.label === "Looking to play");
      while ((index = all.findIndex((c) => c.role === "Switch" && c.enabled)) < 0 && Date.now() < deadline) {
        all = (await cara.controls()).filter((c) => c.label === "Looking to play");
      }
      expect(index >= 0, `an enabled Looking to play switch, got ${JSON.stringify(all.map((c) => c.role))}`);
      expect(all[index].checked, "the switch is on at the start");
      const saved = (await cara.events()).at(-1)?.seq ?? 0;
      await cara.click("Looking to play", index);
      // The server answers the change with the stored switches.
      await cara.waitForEvent("the switches are saved", (e) => e.type === "PushPrefsLoaded" && e.seq > saved, 10_000);
      const after = (await cara.controls()).find((c) => c.label === "Looking to play" && c.role === "Switch");
      expect(after !== undefined && !after.checked, "the switch shows off");
      await cara.checkpoint("switch-off");
      await cara.click("Close");
      await cara.waitFor("crew settings closed", (s) => !s.open_modals.includes("crew_settings"), 5_000);
      await selectCrew(cara, home);
    });

    await step("bob asks: the ask reaches cara's app and shows no toast", async () => {
      const seen = (await cara.events()).filter((e) => e.type === "LookingToPlayUpdated").at(-1)?.seq ?? 0;
      const bobAsk = await askTheCrew(bob);
      const a = await alice.waitFor("alice sees bob's ask", (s) => onlyAsk(s)?.ask_id === bobAsk);
      expect(a.looking_to_play.toast === null, "alice has the card on screen, so no toast");
      await cara.waitForEvent("the update of bob's ask", (e) => e.type === "LookingToPlayUpdated" && e.seq > seen, 15_000);
      const c = await cara.state();
      expect(c.looking_to_play.toast === null, `no toast with the switch off, got "${c.looking_to_play.toast}"`);
      await cara.checkpoint("no-toast");
      await bob.click("Cancel the ask");
      await bob.waitFor("bob's ask is closed", (s) => s.looking_to_play.asks.length === 0);
    });

    await step("no Error events on any app", () => noErrors([alice, bob, cara], expect));
  },
});
