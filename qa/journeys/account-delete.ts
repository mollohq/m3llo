// AUTH-06, AUTH-07, AUTH-08: a user deletes the account in Settings
// (plans/ACCOUNT-DELETE.md, specs/16-CREW-EVENT-LEDGER.md section 10.2).
//
// - `member`: bob, a member, opens Settings, PROFILE, ACCOUNT. Cancel keeps
//   the account. "Delete my account" signs him out to step 1. alice sees him
//   gone from the crew. His email and password do not sign in again.
// - `ownerHandover`: alice is the only owner of a crew with bob in it. She
//   deletes her account. bob keeps the crew and is its owner.
// - `handoverRefused`: the same, and the server cannot hand the crew over
//   (`dev_fault`, on a stack with MELLO_ENABLE_DEV_FAULT=1). The confirm shows
//   the text of the server, alice stays signed in, and a second press deletes.
// - `soleMember`: alice is the only member of her crew. Before the delete, its
//   invite link opens the welcome screen for carol. After the delete, the
//   same link tells dave that it is not valid: the crew is gone.

import type { App } from "../../tools/mello-driver/src/app.ts";
import { journey, type JourneyContext } from "../../tools/mello-driver/src/journey.ts";
import { closeCrewSettings, openCrewSettings } from "./lib/crew.ts";
import { inviteWelcome, onboardWithNewCrew, type Credentials } from "./lib/onboarding.ts";
import { failNextHandover, signInEmail } from "./lib/seed.ts";
import { waitNoControl, waitText } from "./lib/screen.ts";
import { openDeleteAccount } from "./lib/settings.ts";
import { readInviteCode, twoUsersInOneCrew } from "./lib/setup.ts";

const HAVE_ACCOUNT = "I already have an account";

/** Press the red button of the confirm and wait for step 1, signed out, with the settings closed. */
async function confirmDelete(app: App): Promise<void> {
  const before = (await app.events()).at(-1)?.seq ?? 0;
  await app.click("Delete my account");
  await app.waitFor(
    "step 1, signed out, the settings closed",
    (s) => s.screen === "onboarding" && s.onboarding_step === 1 && !s.logged_in && !s.open_modals.includes("settings"),
    30_000,
  );
  // The events of this press only: an earlier press can have failed.
  const events = (await app.events()).filter((e) => e.seq > before).map((e) => e.type);
  if (!events.includes("AccountDeleted") || events.includes("AccountDeleteFailed")) {
    throw new Error(`${app.name}: expected AccountDeleted and no AccountDeleteFailed, got: ${events.filter((t) => t.startsWith("Account")).join(", ")}`);
  }
}

/** The server refuses the email and password of a deleted account. */
async function expectNoAccount(ctx: JourneyContext, creds: Credentials): Promise<void> {
  let error = "";
  try {
    await signInEmail(creds.email, creds.password);
  } catch (e) {
    error = String(e);
  }
  ctx.expect(/HTTP 404/.test(error), `the server has no account for the email (HTTP 404), got "${error}"`);
}

export const member = journey({
  id: "account.delete-member",
  flows: ["AUTH-06"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, crew, bobName, bobCreds } = await twoUsersInOneCrew(ctx);
    const bobId = (await bob.state()).user_id;

    await step("alice sees bob in the crew", async () => {
      await alice.waitFor(`${bobName} is a member`, (s) => s.members.includes(bobName), 20_000);
    });

    await step("bob opens the delete confirm in Settings and cancels: nothing changes", async () => {
      await openDeleteAccount(bob);
      expect(await waitText(bob, "Delete your account"), "the confirm has its title");
      expect(await waitText(bob, "This cannot be undone."), "the confirm says that it cannot be undone");
      await bob.checkpoint("confirm");
      await bob.click("Cancel");
      expect(await waitNoControl(bob, "Delete my account"), "the confirm is closed");
      const s = await bob.state();
      expect(s.logged_in && s.user_id === bobId && s.open_modals.includes("settings"), "bob is still signed in, with the settings open");
      expect((await bob.events()).every((e) => !e.type.startsWith("AccountDelete")), "no account delete event");
    });

    await step("bob deletes his account: he is signed out to step 1", async () => {
      await bob.click("Delete account");
      await bob.find("Delete my account");
      await confirmDelete(bob);
      await bob.checkpoint("deleted");
    });

    await step("alice sees bob gone from the crew, and keeps the crew", async () => {
      const s = await alice.waitFor(`${bobName} is not a member`, (st) => !st.members.includes(bobName) && st.crew_members.every((m) => m.id !== bobId), 30_000);
      expect(s.crews.includes(crew), `alice keeps ${crew}`);
    });

    await step("bob's email and password do not sign in again", async () => {
      await expectNoAccount(ctx, bobCreds);
      await bob.settle(["DiscoverCrewsLoaded"]);
      await bob.click(HAVE_ACCOUNT);
      await bob.waitFor("the sign-in panel", (s) => s.screen === "sign_in");
      await bob.click("Email + password");
      await bob.type("Email", bobCreds.email);
      await bob.type("Password", bobCreds.password);
      await bob.click("SIGN IN");
      const s = await bob.waitFor("the sign-in attempt ends with an error", (st) => !st.login_loading && st.login_error !== "", 20_000);
      expect(!s.logged_in && s.screen === "sign_in", `not signed in, still on the sign-in panel, got ${s.screen}`);
      await bob.checkpoint("no-sign-in");
    });

    await step("bob's computer does not open the old account after a restart", async () => {
      await bob.restart();
      const s = await bob.waitFor("step 1", (st) => st.screen === "onboarding" && st.onboarding_step === 1, 30_000);
      expect(!s.logged_in || s.user_id !== bobId, "the deleted account is not signed in");
      expect(s.crews.length === 0, `no crew, got ${s.crews.join(", ")}`);
    });
  },
});

export const ownerHandover = journey({
  id: "account.delete-owner-handover",
  flows: ["AUTH-07"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, crew, aliceName, aliceCreds } = await twoUsersInOneCrew(ctx);
    const aliceId = (await alice.state()).user_id;
    const bobId = (await bob.state()).user_id;

    await step("alice is the owner and bob is a member", async () => {
      const s = await bob.waitFor("bob sees both members", (st) => st.crew_members.length === 2, 20_000);
      expect(s.crew_members.find((m) => m.id === aliceId)?.role === "owner", "alice is the owner");
      expect(s.crew_members.find((m) => m.id === bobId)?.role === "member", "bob is a member");
      expect((await bob.controls()).every((c) => c.label !== "Crew settings"), "sanity: the crew menu is closed");
    });

    await step("alice, the only owner, deletes her account", async () => {
      await openDeleteAccount(alice);
      expect(await waitText(alice, "A crew you own goes to an admin, or to the member who joined first."), "the confirm says what happens to her crew");
      await confirmDelete(alice);
      await expectNoAccount(ctx, aliceCreds);
    });

    await step("bob starts mello again: he keeps the crew and is its owner, alice is gone", async () => {
      await bob.restart();
      const s = await bob.waitFor(
        `${crew} with bob as the owner`,
        (st) => st.screen === "app" && st.crews.includes(crew) && st.crew_members.some((m) => m.id === bobId && m.role === "owner"),
        30_000,
      );
      expect(s.crew_members.every((m) => m.id !== aliceId), "alice is not in the member list");
      expect(!s.members.includes(aliceName), `${aliceName} is not a member`);
      await bob.checkpoint("bob-owner");
    });

    await step("bob opens crew settings as the owner", async () => {
      await openCrewSettings(bob);
      const s = await bob.waitFor("the settings know the role", (st) => st.crew_settings.my_role === "owner", 10_000);
      expect(s.crew_settings.name === crew, `the settings show ${crew}`);
      await bob.find("DELETE CREW");
      await closeCrewSettings(bob);
    });
  },
});

const REFUSED = "Your account was not deleted. A crew you own could not be handed to another member. Try again in a moment.";

export const handoverRefused = journey({
  id: "account.delete-handover-refused",
  flows: ["AUTH-07"],
  async run(ctx) {
    const { step, expect } = ctx;
    const { alice, bob, crew, aliceCreds } = await twoUsersInOneCrew(ctx);
    const aliceId = (await alice.state()).user_id;
    const bobId = (await bob.state()).user_id;

    await step("the server will fail the next handover", async () => {
      const on = await failNextHandover(await signInEmail(aliceCreds.email, aliceCreds.password));
      expect(on, "dev_fault is on (start the stack with MELLO_ENABLE_DEV_FAULT=1)");
    });

    await step("alice presses Delete my account: the server refuses, the confirm says why, she stays signed in", async () => {
      await openDeleteAccount(alice);
      await alice.click("Delete my account");
      expect(await waitText(alice, REFUSED, 20_000), "the confirm shows the text of the server");
      const s = await alice.state();
      expect(s.logged_in && s.user_id === aliceId && s.screen === "app", "alice is still signed in");
      expect(s.crews.includes(crew), `alice keeps ${crew}`);
      expect((await alice.find("Delete my account")).enabled, "the red button works again");
      await alice.checkpoint("refused");
    });

    await step("alice presses again: the account is deleted", async () => {
      await confirmDelete(alice);
      await expectNoAccount(ctx, aliceCreds);
    });

    await step("bob starts mello again and is the owner of the crew", async () => {
      await bob.restart();
      await bob.waitFor(
        `${crew} with bob as the owner`,
        (st) => st.screen === "app" && st.crews.includes(crew) && st.crew_members.some((m) => m.id === bobId && m.role === "owner"),
        30_000,
      );
    });
  },
});

export const soleMember = journey({
  id: "account.delete-sole-member",
  flows: ["AUTH-08"],
  async run(ctx) {
    const { runId, launch, step, expect } = ctx;
    const crew = `Solo ${runId}`;
    const alice = await launch("alice");
    let creds: Credentials = { email: "", password: "" };
    let code = "";

    await step("alice creates a crew of one and reads its invite link", async () => {
      creds = await onboardWithNewCrew(alice, crew, `alice${runId}`, "Public");
      code = await readInviteCode(alice);
    });

    await step("the link works: carol gets the welcome screen of the crew", async () => {
      const carol = await launch("carol", `mello://join/${code}`);
      await inviteWelcome(carol, crew);
      await carol.kill();
    });

    await step("alice deletes her account", async () => {
      await openDeleteAccount(alice);
      expect(await waitText(alice, "A crew where you are the only member is deleted."), "the confirm says that the crew is deleted");
      await confirmDelete(alice);
      await expectNoAccount(ctx, creds);
    });

    await step("the crew is gone: the same link is not valid for dave", async () => {
      const dave = await launch("dave", `mello://join/${code}`);
      const s = await dave.waitFor(
        "step 1 with the invite message",
        (st) => st.screen === "onboarding" && st.onboarding_step === 1 && st.onboarding_invite_error !== "",
        30_000,
      );
      expect(s.onboarding_invite_error === "This invite link is no longer valid.", `a clear message, got "${s.onboarding_invite_error}"`);
      expect(s.onboarding_crews.every((c) => c.name !== crew) && s.discover_crews.every((c) => c.name !== crew), `${crew} is not offered on step 1`);
      await dave.checkpoint("link-not-valid");
    });
  },
});
