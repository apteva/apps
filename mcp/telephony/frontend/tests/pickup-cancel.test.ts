import { describe, expect, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { telephonyExtension, type CallSession } from "../src/client";
import type { AudioRuntime } from "../src/audio";

// A pick-up whose audio is not live yet must give the call back to routing,
// never end it for the caller.
function fixture(options: { releaseStatus?: number; live?: boolean } = {}) {
  const requests: { path: string; body: any }[] = [];
  const session: CallSession = { call_id: "call-1", media_url: "/api/apps/telephony/_install/42/softphone/media/call-1/secret", session_token: "secret", session_generation: "session-abc" };
  const offered = { id: "call-1", direction: "inbound", status: "pending", peer_kind: "human", answerable: true, ring_offers: [{ id: "offer-1", destination_id: "dest-1", kind: "browser" }] };
  const sdk = new AptevaClient({ baseURL: "https://gateway.example", accessToken: "token", fetch: (async (url, init) => {
    const parsed = new URL(String(url));
    requests.push({ path: parsed.pathname, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (parsed.pathname.endsWith("/calls")) return Response.json({ calls: [offered] });
    if (parsed.pathname.includes("/softphone/release/")) {
      return options.releaseStatus ? new Response("answer session is not releasable", { status: options.releaseStatus }) : Response.json({ status: "pending" });
    }
    if (parsed.pathname.endsWith("/hangup")) return Response.json({ ok: true });
    if (parsed.pathname.includes("/softphone/offer/decline/")) return Response.json({ offer_id: "offer-1", declined: true });
    if (parsed.pathname.includes("/softphone/answer/")) return Response.json(session);
    return Response.json({});
  }) as typeof fetch });
  const client = sdk.use(telephonyExtension, { projectId: "p1", installId: 42 });
  let started = false;
  const runtime: AudioRuntime = {
    async preflight() {},
    create(cb) {
      let stopStart: (() => void) | undefined;
      return {
        start() {
          started = true;
          if (options.live) { cb.onState?.("live"); return Promise.resolve(); }
          // Audio setup still running: it settles only when stopped.
          return new Promise<void>((_, reject) => { stopStart = () => reject(new Error("stopped")); });
        },
        stop() { stopStart?.(); }, setMuted() {}, sendDTMF() {}, setOutputVolume() {},
        startRingback() {}, stopRingback() {},
      };
    },
  };
  const phone = client.createSoftphone({ audioRuntime: runtime, pollIntervalMs: 0 });
  phone.observeCalls([offered as any]);
  return { phone, requests, get started() { return started; }, paths: () => requests.map(r => r.path) };
}

async function until(check: () => boolean) {
  for (let i = 0; i < 500 && !check(); i++) await Bun.sleep(2);
  expect(check()).toBe(true);
}

describe("cancelling an inbound pick-up", () => {
  test("hangup while audio connects releases the claim instead of ending the call", async () => {
    const f = fixture();
    const answering = f.phone.answer("call-1").catch(() => {});
    await until(() => f.started);
    await f.phone.hangup();
    await answering;
    const release = f.requests.find(r => r.path.includes("/softphone/release/call-1"));
    expect(release?.body).toEqual({ session_token: "secret" });
    expect(f.paths().some(p => p.endsWith("/hangup"))).toBe(false);
    expect(f.phone.getSnapshot().callId).toBeUndefined();
  });

  test("a call that connected meanwhile is hung up with its session token", async () => {
    const f = fixture({ releaseStatus: 409 });
    const answering = f.phone.answer("call-1").catch(() => {});
    await until(() => f.started);
    await f.phone.hangup();
    await answering;
    const hangup = f.requests.find(r => r.path.endsWith("/calls/call-1/hangup"));
    expect(hangup?.body).toEqual({ session_token: "secret" });
  });

  test("a live call is hung up with its session token", async () => {
    const f = fixture({ live: true });
    await f.phone.answer("call-1");
    await f.phone.hangup();
    expect(f.paths().some(p => p.includes("/softphone/release/"))).toBe(false);
    expect(f.requests.find(r => r.path.endsWith("/calls/call-1/hangup"))?.body).toEqual({ session_token: "secret" });
  });

  test("cancel on a ringing call declines this browser's offer and never hangs up", async () => {
    const f = fixture();
    await f.phone.cancel("call-1");
    expect(f.requests.find(r => r.path.includes("/softphone/offer/decline/call-1"))?.body).toEqual({ offer_id: "offer-1" });
    expect(f.paths().some(p => p.endsWith("/hangup"))).toBe(false);
  });

  test("cancel during a pick-up releases it", async () => {
    const f = fixture();
    const answering = f.phone.answer("call-1").catch(() => {});
    await until(() => f.started);
    await f.phone.cancel("call-1");
    await answering;
    expect(f.paths().some(p => p.includes("/softphone/release/call-1"))).toBe(true);
    expect(f.paths().some(p => p.endsWith("/hangup"))).toBe(false);
  });

  test("cancel for another call leaves the pick-up alone", async () => {
    const f = fixture();
    const answering = f.phone.answer("call-1").catch(() => {});
    await until(() => f.started);
    await f.phone.cancel("call-2");
    expect(f.paths().some(p => p.includes("/softphone/release/") || p.endsWith("/hangup"))).toBe(false);
    await f.phone.hangup();
    await answering;
  });
});
