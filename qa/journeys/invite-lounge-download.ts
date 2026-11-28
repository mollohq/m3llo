// INV-10: a friend installs from the invite page, and the invite survives the
// install (CREW-INVITES §7.1, §9.2). alice shares her link. bob opens it: the
// page names alice and her crew. bob presses the install button. The page
// asks for the installer of bob's system, copies the link and shows "Open in
// m3llo". bob's fresh install then gets the invite one of two ways, and both
// end with bob in alice's crew:
//
// - clipboard: the first launch finds the link on the clipboard and opens the
//   welcome screen.
// - button: bob copied something else during the install, so the app opens
//   step 1. "Open in m3llo" then hands the link to the running app, which
//   opens the welcome screen (the installer started the app first).
//
// Needs the local lounge (`npm run dev` in mello-site) next to the stack.
// The page takes bob's system from the browser, so the installer is the one
// of the machine that runs the journey.

import { openInvite } from "./lib/crew.ts";
import { journey, type Journey, type JourneyContext } from "../../tools/mello-driver/src/journey.ts";
import { downloadFromLounge, LOUNGE, loungeUp } from "../../tools/mello-driver/src/browser.ts";
import { DriverError } from "../../tools/mello-driver/src/app.ts";
import { joinFromWelcome, inviteWelcome, linkEmailAtStep3, onboardWithNewCrew, profileAtStep2 } from "./lib/onboarding.ts";

type Way = "clipboard" | "button";

/** The installer of each system (mello-site lounge/links.js, INSTALLERS). */
const INSTALLERS: Partial<Record<NodeJS.Platform, string>> = {
  win32: "m3llo-win-x64-stable-Setup.exe",
  darwin: "m3llo-osx-arm64-stable-Setup.pkg",
  linux: "m3llo-linux-x64-stable.AppImage",
};

function loungeJourney(way: Way): Journey {
  return journey({
    id: `invite.lounge-download.${way}`,
    flows: ["INV-10", "INV-01", "INV-03"],
    run: (ctx) => run(ctx, way),
  });
}

export const clipboard = loungeJourney("clipboard");
export const button = loungeJourney("button");
export default clipboard;

async function run({ runId, launch, step, expect, clipboard }: JourneyContext, way: Way): Promise<void> {
  if (!(await loungeUp())) {
    throw new DriverError(`no lounge at ${LOUNGE}: run \`npm run dev\` in mello-site, or set MELLO_E2E_LOUNGE_URL`);
  }
  const crew = `Lounge Owls ${runId}`;
  const aliceName = `alice${runId}`;
  const bobName = `bob${runId}`;

  const alice = await launch("alice");
  let code = "";
  await step("alice: fresh install, creates a crew, reads her invite link", async () => {
    await onboardWithNewCrew(alice, crew, aliceName);
    await openInvite(alice);
    const link = await alice.find("Invite link");
    // A generated code is upper case. A custom invite name is lower case.
    const m = link.value.match(/\/join\/([A-Za-z0-9-]+)$/);
    expect(m, `the invite link field shows a join URL, got "${link.value}"`);
    code = m![1];
  });

  let openLink = "";
  await step("bob: the invite page names alice, copies the invite on the install click and offers Open in m3llo", async () => {
    const file = INSTALLERS[process.platform];
    expect(file, `m3llo has an installer for this system (${process.platform})`);
    const d = await downloadFromLounge(code);
    expect(d.title === `${aliceName} invited you to ${crew}`, `the page names the friend and the crew, got "${d.title}"`);
    expect(d.installer.endsWith(`/releases/latest/download/${file}`), `the installer of this system (${file}), got "${d.installer}"`);
    expect(!d.installer.includes("?"), "the installer URL carries no invite");
    expect(d.copied === `https://m3llo.app/join/${code}`, `the page copied the join link, got "${d.copied}"`);
    expect(d.copiedLineShown, `the install steps say "Invite copied"`);
    expect(d.openLink === `mello://join/${code}`, `"Open in m3llo" opens the deep link, got "${d.openLink}"`);
    openLink = d.openLink;
    // The browser and the app share one clipboard on a real machine.
    clipboard.write(d.copied);
  });

  if (way === "button") {
    await step("bob: copies something else while the installer runs", async () => {
      clipboard.write("see you at 9");
    });
  }

  const bob = await launch("bob");

  if (way === "clipboard") {
    await step("bob: the first launch takes the invite from the clipboard: the welcome screen", async () => {
      await inviteWelcome(bob, crew, aliceName);
      expect(clipboard.read() === `https://m3llo.app/join/${code}`, "the app does not change the clipboard");
      await bob.checkpoint("welcome-from-clipboard");
    });
  } else {
    await step("bob: the first launch ignores the other text: step 1", async () => {
      await bob.waitFor("onboarding step 1", (s) => s.screen === "onboarding" && s.onboarding_step === 1, 30_000);
      await bob.settle(["DiscoverCrewsLoaded"]);
      const s = await bob.state();
      expect(!s.invite_welcome, "no welcome screen from unrelated text");
      expect(clipboard.read() === "see you at 9", "the app does not change the clipboard");
      await bob.checkpoint("step1-no-invite");
    });

    await step("bob: Open in m3llo reaches the running app on step 1: the welcome screen", async () => {
      await bob.openLink(openLink);
      await inviteWelcome(bob, crew, aliceName);
      const s = await bob.state();
      expect(!s.join_crew_modal_open, "no join modal: onboarding joins the crew");
      await bob.checkpoint("welcome-from-button");
    });
  }

  await step("bob: joins, finishes onboarding, and is in alice's crew only", async () => {
    await joinFromWelcome(bob, crew);
    await profileAtStep2(bob, bobName, 2);
    await linkEmailAtStep3(bob, bobName);
    const s = await bob.waitFor(`${crew} in bob's crews`, (st) => st.crews.includes(crew));
    expect(s.crews.length === 1, `only the invited crew, got: ${s.crews.join(", ")}`);
    expect(!s.join_crew_modal_open, "no join modal in the app");
    await bob.checkpoint("bob-in-app");
  });

  await step("alice: sees bob in her crew", async () => {
    await alice.waitFor(`${bobName} in alice's member list`, (st) => st.members.includes(bobName));
  });
}
