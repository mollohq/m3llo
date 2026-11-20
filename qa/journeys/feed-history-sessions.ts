// FEED-15: Crew history with game sessions (specs/features/CREW-FEED-ZONES.md
// section 7, #267).
//
// sara, a seeded member, played Rocket League 9 days ago and Valheim 16 days
// ago: two past weeks that hold a game session and nothing else.
//
// - `lockedWeeks`: bob joins the crew after that and has no m3llo+. A locked
//   week with only game sessions is not sent: bob has no Crew history, no
//   m3llo+ strip and no sign of the two games.
// - `openWeeks`: alice was in the crew when its first game session came in.
//   That is quest step 5, whose gift is m3llo+ for every member (the only way
//   to m3llo+ on a local stack: there is no dev hook that grants it). alice
//   gets both weeks open, each with its session card.
//
// `openWeeks` fails today at "alice has m3llo+". The server does not grant
// the gift: questsGrantPlus (crew_gifts.go) lists the members with the limit
// MaxCrewMembers (10000), Nakama takes 1 to 100, and the grant is skipped
// with a warning. The quest card shows the gift open all the same.
//
// Not covered: a locked week with a clip or a stream (the teasers, the "+N"
// tile, the m3llo+ strip). No hook puts a clip or a stream in a past week.

import type { App } from "../../tools/mello-driver/src/app.ts";
import { journey, type JourneyContext } from "../../tools/mello-driver/src/journey.ts";
import { onboardFromInvite, onboardWithNewCrew } from "./lib/onboarding.ts";
import { waitText } from "./lib/screen.ts";
import { GAME_POOL, seedMember, type SeedProfile } from "./lib/seed.ts";
import { readInviteCode } from "./lib/setup.ts";

const ROCKET_LEAGUE = { ...GAME_POOL[1], playtimeMin: 3_000 };
const VALHEIM = { ...GAME_POOL[7], playtimeMin: 2_000 };
const GAMES = [ROCKET_LEAGUE.name, VALHEIM.name];

/** alice makes a crew. sara joins it and reports one session in each of two past weeks. */
async function crewWithPastSessions(ctx: JourneyContext): Promise<{ alice: App; crew: string; code: string }> {
  const { runId, launch, step } = ctx;
  const crew = `History ${runId}`;
  const alice = await launch("alice");
  let code = "";
  await step("setup: alice creates a crew; sara joins with a session 9 days ago and one 16 days ago", async () => {
    await onboardWithNewCrew(alice, crew, `alice${runId}`, "Public");
    code = await readInviteCode(alice);
    const sara: SeedProfile = {
      name: "Sara",
      games: [ROCKET_LEAGUE, VALHEIM],
      sessions: [
        { game: ROCKET_LEAGUE, durationMin: 45, endLocalMin: 21 * 60, daysAgo: 9 },
        { game: VALHEIM, durationMin: 80, endLocalMin: 22 * 60, daysAgo: 16 },
      ],
      clips: 0,
      hue: 1,
    };
    await seedMember(code, sara.name, sara, runId);
    await alice.waitFor("alice sees sara", (s) => s.members.includes("Sara"), 20_000);
  });
  return { alice, crew, code };
}

/** The sessions are older than the free window: no card of them in the Sessions zone. */
function noFreeSession(app: App, ctx: JourneyContext, sessions: { game_name: string }[]): void {
  ctx.expect(
    sessions.every((c) => !GAMES.includes(c.game_name)),
    `${app.name}'s Sessions zone has no session of the past weeks, got ${sessions.map((c) => c.game_name).join(", ")}`,
  );
}

export const lockedWeeks = journey({
  id: "feed.history-locked-sessions",
  flows: ["FEED-15"],
  async run(ctx) {
    const { runId, launch, step, expect } = ctx;
    const { crew, code } = await crewWithPastSessions(ctx);

    const bob = await launch("bob", `mello://join/${code}`);
    await step("bob joins after the sessions: his feed has the crew of three and he has no m3llo+", async () => {
      await onboardFromInvite(bob, crew, `bob${runId}`);
      const s = await bob.waitFor("bob's feed with three members", (st) => st.feed_zones.people.members === 3, 30_000);
      expect(!s.feed_zones.plus, "bob has no m3llo+");
    });

    await step("bob has no Crew history: a locked week with only game sessions is not there", async () => {
      // The feed loads again after a join. The check is on the feed at rest.
      await bob.settle(["FeedLoaded"], 2_000, 20_000);
      const z = (await bob.state()).feed_zones;
      expect(z.people.members === 3, "the feed is the feed of the crew");
      expect(z.history.length === 0 && !z.history_more, `no week of Crew history, got ${JSON.stringify(z.history)}`);
      noFreeSession(bob, ctx, z.sessions);
      // The end of the feed: no week, no m3llo+ strip, no game of the past weeks.
      for (let i = 0; i < 12; i++) await bob.ui.scrollAt(700, 500, -300);
      const texts = await bob.ui.texts();
      for (const game of GAMES) {
        expect(texts.every((t) => !t.includes(game) || t.includes("Sara ·")), `nothing of ${game} from a past week on bob's feed`);
      }
      expect(texts.every((t) => !/^WEEK OF /.test(t) && !t.includes("Keep every clip and recap forever")), "no week head and no m3llo+ strip");
      const labels = (await bob.controls()).map((c) => c.label);
      expect(!labels.includes("Crew history") && labels.every((l) => !l.startsWith("Locked ")), "no Crew history tile and no locked teaser");
      await bob.checkpoint("no-history");
    });
  },
});

export const openWeeks = journey({
  id: "feed.history-sessions",
  flows: ["FEED-15"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice } = await crewWithPastSessions(ctx);

    await step("the first session of the crew is quest step 5: alice has m3llo+", async () => {
      const s = await alice.waitFor("the gift of step 5 is open", (st) => st.feed_zones.quests?.gifts[4]?.state !== "locked", 30_000);
      expect(s.feed_zones.quests!.rows[4]?.done, "First session is done");
      await alice.waitFor("alice has m3llo+", (st) => st.feed_zones.plus, 30_000);
    });

    await step("alice has two open weeks of Crew history, each with one session card", async () => {
      const s = await alice.waitFor("two weeks of Crew history", (st) => st.feed_zones.history.length === 2, 30_000);
      const z = s.feed_zones;
      for (const w of z.history) {
        expect(!w.locked, `"${w.label}" is open`);
        expect(w.clips === 1 && w.teasers === 0 && w.more === 0, `"${w.label}" has one card and no teaser, got ${JSON.stringify(w)}`);
      }
      expect(z.history[0].label !== z.history[1].label, "two different weeks");
      noFreeSession(alice, ctx, z.sessions);
    });

    await step("the weeks show sara's sessions: Rocket League in the newer week, Valheim in the older one", async () => {
      const weeks = (await alice.state()).feed_zones.history.map((w) => w.label.toUpperCase());
      await alice.scrollToControl(weeks[0], 700, 500, 20);
      expect(await waitText(alice, ROCKET_LEAGUE.name), `${ROCKET_LEAGUE.name} is on screen`);
      await alice.checkpoint("week-newer");
      await alice.scrollToControl(weeks[1], 700, 500, 20);
      expect(await waitText(alice, VALHEIM.name), `${VALHEIM.name} is on screen`);
      expect(await waitText(alice, "Sara"), "the card names sara");
      const labels = (await alice.controls()).map((c) => c.label);
      expect(labels.every((l) => !l.startsWith("Locked ")), "no locked teaser");
      expect((await alice.text("Keep every clip and recap forever")) === null, "no m3llo+ strip for a viewer with m3llo+");
      await alice.checkpoint("week-older");
    });
  },
});
