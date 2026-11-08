// FEED-14: the You strip (plan you-strip.md). alice makes a crew and bob
// joins: the quests are open, so neither has the strip. With the quests
// cleared (dev_fault quests_clear, as a crew made before quests) and alice's
// own library reported, titles are contested and both apps share game
// activity. Every belt alice holds is a belt bob can take, with the same gap
// and the sign turned, and the pair score is the mirror. A tap on alice's
// strip opens the You view on Belts; Head2head lists bob. Spec
// features/HEAD2HEAD.md, spec 19 section 2.3.

import type { App, AppState, ChaseBar } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { clearQuests, endSession, reportLibrary, signInEmail } from "./lib/seed.ts";
import { twoUsersInOneCrew } from "./lib/setup.ts";

/** The number of a delta with its sign taken off: "−1.3" and "+1.3" are "1.3". */
function gapOf(b: ChaseBar): string {
  return b.delta.replace(/^[−+±]/, "");
}

export default journey({
  id: "feed.you-strip",
  flows: ["FEED-14"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, aliceCreds, bobName } = await twoUsersInOneCrew(ctx);
    const crewId = (await alice.state()).active_crew_id;
    const token = await signInEmail(aliceCreds.email, aliceCreds.password);

    await step("the quests are open: neither app has the strip", async () => {
      for (const app of [alice, bob]) {
        const s = await app.waitFor("the quest card on the Stage", (st) => st.feed_zones.stage === "quests", 30_000);
        expect(!s.you_strip.ready, `${app.name} has no strip while the quests are open`);
      }
    });

    await step("the quests are cleared and alice reports a library of her own", async () => {
      expect(await clearQuests(token, crewId), "dev_fault is on (MELLO_ENABLE_DEV_FAULT=1)");
      // Both apps report the library of this machine. alice's own report
      // makes the two differ, so titles are contested; it also pushes
      // "feed changed", and both apps load the feed again.
      await reportLibrary(token, [
        { id: "steam-291550", name: "Brawlhalla", playtimeMin: 2_500 },
        { id: "steam-252950", name: "Rocket League", playtimeMin: 18_000 },
        { id: "steam-1097150", name: "Fall Guys", playtimeMin: 600 },
        { id: "steam-1245620", name: "Elden Ring", playtimeMin: 9_000 },
      ]);
      await endSession(token, crewId, { id: "steam-291550", name: "Brawlhalla" }, 40, 23 * 60);
    });

    let a: AppState | undefined;
    let b: AppState | undefined;
    await step("both apps show the strip with bars", async () => {
      a = await alice.waitFor("alice's strip", (st) => st.you_strip.ready && st.you_strip.bars.length > 0, 45_000);
      b = await bob.waitFor("bob's strip", (st) => st.you_strip.ready && st.you_strip.bars.length > 0, 45_000);
      for (const [name, s] of [["alice", a], ["bob", b]] as const) {
        expect(s.you_strip.bars.length <= 2, `${name}: at most two bars, got ${s.you_strip.bars.length}`);
        expect(s.you_strip.bars.every((x) => x.delta !== ""), `${name}: every bar has a delta`);
      }
      await alice.checkpoint("strip-alice");
      await bob.checkpoint("strip-bob");
    });

    await step("every belt alice holds is a belt bob can take, the same gap with the sign turned", async () => {
      const aHolds = a!.you_view.holds;
      const bTakes = b!.you_view.takes;
      expect(aHolds.length + a!.you_view.takes.length > 0, "alice has belts to hold or take");
      for (const h of aHolds) {
        const t = bTakes.find((x) => x.title_id === h.title_id);
        expect(t !== undefined, `bob can take ${h.title_id}, got ${bTakes.map((x) => x.title_id)}`);
        expect(t!.member !== bobName, `bob takes ${h.title_id} from alice, not himself`);
        expect(h.delta.startsWith("+") || h.delta.startsWith("±"), `alice's hold reads as a lead: ${h.delta}`);
        expect(t!.delta.startsWith("−") || t!.delta.startsWith("±"), `bob's take reads as behind: ${t!.delta}`);
        expect(gapOf(h) === gapOf(t!) && h.delta_unit === t!.delta_unit, `the same gap: ${h.delta} ${h.delta_unit} and ${t!.delta} ${t!.delta_unit}`);
      }
      const ap = a!.you_view.pairs;
      const bp = b!.you_view.pairs;
      expect(ap.length === 1 && bp.length === 1, `one pair each, got ${ap.length} and ${bp.length}`);
      expect(ap[0].you === bp[0].them && ap[0].them === bp[0].you, `the score is the mirror: ${ap[0].you}-${ap[0].them} and ${bp[0].you}-${bp[0].them}`);
    });

    await step("alice's strip opens the You view on Belts, Head2head lists bob", async () => {
      await alice.click("You");
      let s = await alice.waitFor("the You view", (st) => st.you_view.open && st.open_modals.includes("you_view"), 10_000);
      expect(s.you_view.tab === "belts", `opens on Belts, got ${s.you_view.tab}`);
      await alice.checkpoint("you-view-belts");
      await alice.click("Head2head");
      s = await alice.waitFor("the Head2head tab", (st) => st.you_view.tab === "head2head", 5_000);
      expect(s.you_view.pairs[0]?.member === bobName, `the pair is bob, got ${s.you_view.pairs[0]?.member}`);
      await alice.checkpoint("you-view-head2head");
      await alice.click("Close");
      await alice.waitFor("the view closed", (st) => !st.you_view.open, 5_000);
    });

    await step("no hidden errors on either side", async () => {
      for (const u of [alice, bob] as App[]) {
        const errors = (await u.events()).filter((e) => e.message).map((e) => e.message);
        expect(errors.length === 0, `${u.name} has no Error events, got: ${errors.join(" | ")}`);
      }
    });
  },
});
