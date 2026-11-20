// CREW-07: the owner deletes a crew (specs/16-CREW-EVENT-LEDGER.md section 10).
//
// alice owns a crew with bob in it. Both write in the chat. alice opens crew
// settings and presses DELETE CREW. A confirm opens. Cancel keeps the crew and
// its chat. She presses DELETE CREW again and confirms with the red button.
// Her crew list loses the crew at once.
// bob's loses it when he starts mello again: the server sends a member no
// event for a deleted crew. The invite link of the crew is not valid any
// more, and the server gives bob no chat of the crew.
//
// Not asserted: that the chat rows are gone from the database. The server
// refuses the chat of a crew that does not exist, purged or not. The purge
// has its own tests (ledger_purge_chat_test.go).

import { journey } from "../../tools/mello-driver/src/journey.ts";
import { openCrewSettings } from "./lib/crew.ts";
import { waitNoControl, waitText } from "./lib/screen.ts";
import { crewChatTexts, signInEmail } from "./lib/seed.ts";
import { twoUsersInOneCrew } from "./lib/setup.ts";

export default journey({
  id: "crew.delete",
  flows: ["CREW-07"],
  async run(ctx) {
    const { runId, launch, step, expect } = ctx;
    const { alice, bob, crew, aliceName, bobName, bobCreds, code } = await twoUsersInOneCrew(ctx);
    const crewId = (await alice.state()).active_crew_id;
    const fromAlice = `gg from alice ${runId}`;
    const fromBob = `gg from bob ${runId}`;
    let bobToken = "";

    await step("alice and bob write in the crew chat, and the server has both messages", async () => {
      await alice.type("Message crewmates", fromAlice);
      await alice.key("\n");
      await bob.waitFor("bob sees alice's message", (s) => s.messages.some((m) => m.text === fromAlice && m.sender === aliceName), 15_000);
      await bob.type("Message crewmates", fromBob);
      await bob.key("\n");
      await alice.waitFor("alice sees bob's message", (s) => s.messages.some((m) => m.text === fromBob && m.sender === bobName), 15_000);
      bobToken = await signInEmail(bobCreds.email, bobCreds.password);
      const texts = await crewChatTexts(bobToken, crewId);
      expect(texts !== null && texts.includes(fromAlice) && texts.includes(fromBob), `the server gives bob both messages, got ${JSON.stringify(texts)}`);
    });

    await step("alice presses DELETE CREW: a confirm opens, and Cancel keeps the crew", async () => {
      await openCrewSettings(alice);
      await alice.click("DELETE CREW");
      await alice.find("Delete this crew");
      expect(await waitText(alice, `Delete ${crew}`), "the confirm names the crew");
      expect(await waitText(alice, "This cannot be undone."), "the confirm says that it cannot be undone");
      await alice.checkpoint("confirm");
      await alice.click("Cancel");
      expect(await waitNoControl(alice, "Delete this crew"), "Cancel closes the confirm");
      const s = await alice.state();
      expect(s.crews.includes(crew) && s.active_crew_id === crewId && s.open_modals.includes("crew_settings"), "the crew stays, with crew settings open");
      const types = (await alice.events()).map((e) => e.type);
      expect(!types.includes("CrewDeleted") && !types.includes("CrewDeleteFailed"), "Cancel sends no delete");
      const texts = await crewChatTexts(bobToken, crewId);
      expect(texts !== null && texts.includes(fromAlice) && texts.includes(fromBob), `the server still has the chat, got ${JSON.stringify(texts)}`);
    });

    await step("alice presses DELETE CREW and confirms: the crew leaves her crew list", async () => {
      await alice.click("DELETE CREW");
      await alice.click("Delete this crew");
      const s = await alice.waitFor("the crew is gone for alice", (st) => !st.crews.includes(crew) && !st.open_modals.includes("crew_settings"), 15_000);
      expect(s.active_crew_id !== crewId, "the deleted crew is not the active crew");
      expect(await waitNoControl(alice, "Delete this crew"), "the confirm is closed");
      const types = (await alice.events()).map((e) => e.type);
      expect(types.includes("CrewDeleted") && !types.includes("CrewDeleteFailed"), "one CrewDeleted, no CrewDeleteFailed");
      await alice.checkpoint("deleted");
    });

    await step("bob starts mello again: the crew is not in his list", async () => {
      await bob.restart();
      const s = await bob.waitFor("bob is signed in", (st) => st.logged_in && st.screen !== "blank", 30_000);
      await bob.settle(["CrewsLoaded", "DiscoverCrewsLoaded"]);
      const after = await bob.state();
      expect(!after.crews.includes(crew), `${crew} is not in bob's crews, got ${after.crews.join(", ")}`);
      expect(after.active_crew_id !== crewId, "the deleted crew is not bob's active crew");
      expect(after.messages.every((m) => m.text !== fromAlice && m.text !== fromBob), "bob's screen shows no message of the crew");
      expect(s.user_name === bobName, `bob is still ${bobName}`);
      await bob.checkpoint("bob-after");
    });

    await step("the server gives bob no chat of the crew", async () => {
      const texts = await crewChatTexts(bobToken, crewId);
      expect(texts === null || (!texts.includes(fromAlice) && !texts.includes(fromBob)), `no message of the crew, got ${JSON.stringify(texts)}`);
    });

    await step("the invite link of the crew is not valid for carol", async () => {
      const carol = await launch("carol", `mello://join/${code}`);
      const s = await carol.waitFor(
        "step 1 with the invite message",
        (st) => st.screen === "onboarding" && st.onboarding_step === 1 && st.onboarding_invite_error !== "",
        30_000,
      );
      expect(s.onboarding_invite_error === "This invite link is no longer valid.", `a clear message, got "${s.onboarding_invite_error}"`);
    });
  },
});
