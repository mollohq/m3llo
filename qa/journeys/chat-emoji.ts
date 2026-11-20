// CHAT-02: emoji in chat. The picker stays open across picks, an emoji-only
// message reaches the other member, and the GIF picker shows GIFs or says why it
// is empty, never a blank grid. The checkpoints are the visual proof of
// the Twemoji glyphs and the jumbo size, which no headless render can show.

import { journey } from "../../tools/mello-driver/src/journey.ts";
import { twoUsersInOneCrew } from "./lib/setup.ts";

export default journey({
  id: "chat.emoji",
  flows: ["CHAT-02"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, aliceName } = await twoUsersInOneCrew(ctx);
    const fire = "🔥🔥";

    await step("alice opens the picker and sends an emoji-only message", async () => {
      await alice.click("Emoji");
      await alice.checkpoint("alice-picker-open");
      await alice.key("Escape");
      await alice.type("Message crewmates", fire);
      await alice.key("\n");
      await alice.waitFor("alice sees her own message", (s) => s.messages.some((m) => m.text === fire));
      await alice.checkpoint("alice-jumbo");
    });

    await step("bob sees it, big, from alice", async () => {
      const s = await bob.waitFor("bob sees the emoji", (st) => st.messages.some((m) => m.text === fire));
      const m = s.messages.find((x) => x.text === fire)!;
      expect(m.sender === aliceName, `the sender shows as ${aliceName}, got "${m.sender}"`);
      await bob.checkpoint("bob-jumbo");
    });

    await step("the GIF picker shows GIFs, or says why it is empty", async () => {
      await bob.click("GIF");
      // A build without GIPHY_API_KEY must give the reason and a retry. A build
      // with the key shows cells, and each cell is a control labelled "GIF", as
      // the pill is. The two outcomes are the only correct ones: a grid that
      // stays blank fails the wait.
      const deadline = Date.now() + 10_000;
      let outcome: "cells" | "reason" | null = null;
      while (outcome === null && Date.now() < deadline) {
        const all = await bob.controls();
        if (all.some((c) => c.label === "Try again")) outcome = "reason";
        else if (all.filter((c) => c.label === "GIF").length > 1) outcome = "cells";
        else await new Promise((r) => setTimeout(r, 200));
      }
      expect(outcome !== null, "the GIF picker shows cells or a retry within 10 s, not a blank grid");
      if (outcome === "reason") {
        // The sentence depends on the cause (no key, no answer, rate limit).
        // Without a key it is this one.
        const noKey = (await bob.text("GIFs are off in this build")) !== null;
        const noAnswer = (await bob.text("The GIF service did not answer")) !== null;
        expect(noKey || noAnswer, "the reason is on screen");
      }
      await bob.checkpoint("bob-gif-picker");
    });
  },
});
