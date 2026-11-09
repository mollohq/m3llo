// The UI language that the driver gives each app (MELLO_LANG, src/app.ts).
// Run: node --test "tools/mello-driver/test/*.test.ts"

import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { test } from "node:test";

import { App, type AppOptions } from "../src/app.ts";

const here = import.meta.dirname;
const app = (extra: Partial<AppOptions> = {}) =>
  new App("alice", {
    binary: resolve(here, "fixtures/events/fake-app.mjs"),
    cwd: resolve(here, "../../.."),
    runDir: mkdtempSync(join(tmpdir(), "mello-lang-")),
    mcpPort: 1,
    ...extra,
  });

test("an app shows English, whatever the shell has", () => {
  const was = process.env.MELLO_LANG;
  process.env.MELLO_LANG = "sv";
  try {
    assert.equal(app().launchEnv().MELLO_LANG, "en");
  } finally {
    if (was === undefined) delete process.env.MELLO_LANG;
    else process.env.MELLO_LANG = was;
  }
});

test("a journey can select another language", () => {
  assert.equal(app({ env: { MELLO_LANG: "qps" } }).launchEnv().MELLO_LANG, "qps");
});
