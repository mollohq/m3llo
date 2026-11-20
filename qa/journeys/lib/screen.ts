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
