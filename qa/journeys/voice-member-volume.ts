// VOICE-08: alice sets bob's volume in the member volume popover, quits and
// opens the app again. Back in the call, bob's row has the same volume. The
// local mute does not come back: only the volume is stored (spec 01 §4.1).
// Both calls run through the local SFU. Any other call fails the journey.

import type { AppState } from "../../tools/mello-driver/src/app.ts";
import { voiceMembers } from "../../tools/mello-driver/src/app.ts";
import { journey } from "../../tools/mello-driver/src/journey.ts";
import { twoUsersInOneCrew } from "./lib/setup.ts";
import { joinVoice } from "./lib/voice.ts";

const CHANNEL = "General";

/** bob's row in the channel, as alice sees it. */
function bobRow(s: AppState, bobName: string) {
  return s.voice_channels.flatMap((c) => c.members).find((m) => m.name === bobName);
}

export default journey({
  id: "voice.member-volume",
  flows: ["VOICE-08"],
  voice: true,
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, aliceName, bobName } = await twoUsersInOneCrew(ctx);
    let volume = 0;

    await step(`both join ${CHANNEL} through the SFU`, async () => {
      await joinVoice(alice, CHANNEL);
      await joinVoice(bob, CHANNEL);
      await alice.waitFor(`alice sees both in ${CHANNEL}`, (s) => {
        const m = voiceMembers(s, CHANNEL);
        return m.includes(aliceName) && m.includes(bobName);
      });
    });

    await step("alice sets bob's volume and mutes him for herself", async () => {
      await alice.click(`Volume for ${bobName}`);
      // The speaker glyph is the first control with this label, the slider the second.
      await alice.setValue(`Volume for ${bobName}`, 0.35, 1);
      const s = await alice.waitFor("bob's row shows a volume under 100 %", (s) => {
        const v = bobRow(s, bobName)?.volume ?? 100;
        return v < 100 && v > 0;
      });
      volume = bobRow(s, bobName)?.volume ?? 0;
      await alice.click(`Mute ${bobName} for you`);
      await alice.waitFor("bob muted for alice", (s) => bobRow(s, bobName)?.local_muted === true);
      await alice.key("Escape");
      await alice.checkpoint("volume-set");
    });

    await step("alice quits and opens the app again", async () => {
      await alice.restart();
      await alice.waitFor("the app, logged in", (s) => s.screen === "app" && s.logged_in, 30_000);
    });

    await step(`alice joins ${CHANNEL} again: bob has the stored volume, not muted`, async () => {
      await joinVoice(alice, CHANNEL);
      const s = await alice.waitFor(`bob's row in ${CHANNEL} with ${volume} %`, (s) => bobRow(s, bobName)?.volume === volume);
      expect(bobRow(s, bobName)?.local_muted === false, "the local mute is not stored");
      await alice.checkpoint("volume-after-restart");
    });

    await step("no hidden errors on either side", async () => {
      for (const u of [alice, bob]) {
        const errors = (await u.events()).filter((e) => e.message).map((e) => e.message);
        expect(errors.length === 0, `${u.name} has no Error events, got: ${errors.join(" | ")}`);
      }
    });
  },
});
