// A stand-in for the push Worker (mello-push-worker), for journeys that read
// the text of a phone push. Nakama posts each push to PUSH_WORKER_URL/send
// (backend push_remote.go). The stub keeps what it gets and answers "sent".
//
// The Nakama of the run must point at the stub. Start the stack with:
//   PUSH_WORKER_URL=http://host.docker.internal:<port>  PUSH_WORKER_TOKEN=<any>
// scripts/e2e.sh does this for port 18787 (MELLO_E2E_PUSH_PORT).

import { createServer, type Server } from "node:http";
import { randomBytes } from "node:crypto";

/** One push, as the Worker gets it. */
export type Push = { type: string; crew_id: string; title: string; body: string; ask_id?: string; tokens: string[] };

export type PushStub = {
  port: number;
  /** Every push so far, oldest first. */
  pushes(): Push[];
  /** Wait for a push to this device token that `match` accepts. */
  waitFor(token: string, what: string, match: (p: Push) => boolean, timeoutMs?: number): Promise<Push>;
  close(): Promise<void>;
};

/** The port of the stub: MELLO_E2E_PUSH_PORT, or 18787 as in scripts/e2e.sh. */
export function pushPort(): number {
  return Number(process.env.MELLO_E2E_PUSH_PORT ?? 18787);
}

/** A device token that no other run has: 64 hex characters, as APNs gives. */
export function deviceToken(): string {
  return randomBytes(32).toString("hex");
}

export async function startPushStub(port = pushPort()): Promise<PushStub> {
  const got: Push[] = [];
  const server: Server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      let tokens: string[] = [];
      try {
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8")) as { notification?: Omit<Push, "tokens">; tokens?: { token: string }[] };
        tokens = (body.tokens ?? []).map((t) => t.token);
        if (req.url === "/send" && body.notification) got.push({ ...body.notification, tokens });
      } catch {
        // Not a push: answer with no result.
      }
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ results: tokens.map((token) => ({ token, status: "sent" })), prune: [] }));
    });
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", (e) => reject(new Error(`the push stub cannot listen on port ${port}: ${e.message}`)));
    // All interfaces: the Nakama container reaches the host by host.docker.internal.
    server.listen(port, "0.0.0.0", () => resolve());
  });
  return {
    port,
    pushes: () => [...got],
    async waitFor(token, what, match, timeoutMs = 15_000) {
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const hit = got.find((p) => p.tokens.includes(token) && match(p));
        if (hit) return hit;
        if (Date.now() > deadline) break;
        await new Promise((r) => setTimeout(r, 100));
      }
      const mine = got.filter((p) => p.tokens.includes(token)).map((p) => `${p.type}: ${p.body}`);
      throw new Error(
        `no push for: ${what} within ${timeoutMs} ms. Pushes to this device: ${mine.join(" | ") || "none"}; pushes in all: ${got.length}. ` +
          `With none in all, the Nakama of the run does not post to http://host.docker.internal:${port} (PUSH_WORKER_URL, PUSH_WORKER_TOKEN).`,
      );
    },
    close: () =>
      new Promise<void>((resolve) => {
        server.close(() => resolve());
        // Nakama keeps its connection open: without this, close waits for it.
        server.closeAllConnections();
      }),
  };
}
