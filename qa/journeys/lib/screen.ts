// Reads of the screen that wait for what they read.

import type { App } from "../../../tools/mello-driver/src/app.ts";

/**
 * Wait until a text with `fragment` is on screen. False when it does not come
 * within `timeoutMs`. A text of a panel can come a frame after its buttons,
 * so a journey waits for the text itself and not for a control next to it.
 */
export async function waitText(app: App, fragment: string, timeoutMs = 5_000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if ((await app.text(fragment)) !== null) return true;
    if (Date.now() > deadline) return false;
  }
}

/**
 * Scroll the page under the pointer at (x, y) down until a text is in the
 * element tree. A text below the fold is absent from it. False when the text
 * is still absent after `maxSteps` steps.
 *
 * `App.scrollToControl` stops when the control is on screen. A text under
 * that control (the card of a week under the week's label) can still be below
 * the fold then.
 */
export async function scrollToText(app: App, fragment: string, x: number, y: number, maxSteps = 20): Promise<boolean> {
  for (let i = 0; i < maxSteps; i++) {
    if ((await app.text(fragment)) !== null) return true;
    await app.ui.scrollAt(x, y, -120);
  }
  return waitText(app, fragment);
}

/**
 * Wait until no control with this label is on screen. False when one is
 * still there after `timeoutMs`.
 */
export async function waitNoControl(app: App, label: string, timeoutMs = 5_000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if ((await app.controls()).every((c) => c.label !== label)) return true;
    if (Date.now() > deadline) return false;
  }
}

/** The number of lines in the app's log now. A later `logLines` call takes it as its start. */
export function logMark(app: App): number {
  return app.logTail(Number.MAX_SAFE_INTEGER).split("\n").length;
}

/**
 * The lines of the app's log (stdout and stderr of the process) from `mark`
 * on that contain `fragment`, without the colour codes.
 */
export function logLines(app: App, fragment: string, mark = 0): string[] {
  return app
    .logTail(Number.MAX_SAFE_INTEGER)
    .split("\n")
    .slice(Math.max(0, mark - 1))
    .map((l) => l.replace(/\x1b\[[0-9;]*m/g, ""))
    .filter((l) => l.includes(fragment));
}
