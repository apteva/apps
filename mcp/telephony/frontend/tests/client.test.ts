import { describe, expect, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { TelephonyClient, telephonyExtension, type CallSession } from "../src/client";
import type { AudioRuntime } from "../src/audio";
import type { SoftphoneCallbacks } from "../../ui/softphone-audio";

function fixture() {
  const requests: Array<{ url: URL; headers: Headers; body: any }> = [];
  const session: CallSession = { call_id: "call-1", media_url: "/api/apps/telephony/_install/42/softphone/media/call-1/secret", session_token: "secret" };
  let response = async (url: URL, body: any): Promise<unknown> => {
    if (url.pathname.endsWith("/calls")) return { calls: [{ id: "call-1", status: "answered" }] };
    if (url.pathname.endsWith("/hangup") || url.pathname.includes("/release/")) return { ok: true };
    return session;
  };
  const sdk = new AptevaClient({ baseURL: "https://gateway.example", accessToken: "first", fetch: (async (url, init) => {
    const parsed = new URL(String(url));
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    requests.push({ url: parsed, headers: new Headers(init?.headers), body });
    return Response.json(await response(parsed, body));
  }) as typeof fetch });
  const client = sdk.use(telephonyExtension, { projectId: "p1", installId: 42 });
  let callbacks: SoftphoneCallbacks = {};
  let stopped = 0, started = 0, preflighted = 0;
  let preflight = async () => {};
  let start = async () => { callbacks.onState?.("live"); };
  let muted = false, dtmf = "";
  const startedOptions: any[] = [];
  const runtime: AudioRuntime = {
    async preflight() { preflighted++; await preflight(); },
    create(cb) {
      callbacks = cb;
      return {
        async start(url, options) { startedOptions.push(options); expect(url).toBe("wss://gateway.example" + session.media_url); started++; await start(); },
        stop() { stopped++; }, setMuted(value) { muted = value; },
        sendDTMF(value) { dtmf = value; }, setOutputVolume() {},
      };
    },
  };
  const phone = client.createSoftphone({ audioRuntime: runtime, pollIntervalMs: 0 });
  return { sdk, client, phone, requests, session, runtime, startedOptions,
    setResponse(value: typeof response) { response = value; },
    setPreflight(value: typeof preflight) { preflight = value; },
    setStart(value: typeof start) { start = value; },
    get callbacks() { return callbacks; }, get stopped() { return stopped; },
    get started() { return started; }, get preflighted() { return preflighted; },
    get muted() { return muted; }, get dtmf() { return dtmf; },
  };
}
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(r => { resolve = r; });
  return { promise, resolve };
}

describe("Telephony extension", () => {
  test("script-only client reuses live SDK auth and scopes every operation", async () => {
    const f = fixture();
    await f.client.listCalls();
    f.sdk.setAccessToken("renewed");
    await f.client.answer("call-1", { destination_id: "desk" });
    await f.client.hangup("call-1");
    expect(f.requests.every(r => r.url.searchParams.get("project_id") === "p1" && r.url.searchParams.get("install_id") === "42")).toBe(true);
    expect(f.requests[0].headers.get("Authorization")).toBe("Bearer first");
    expect(f.requests[1].headers.get("Authorization")).toBe("Bearer renewed");
    expect(f.preflighted).toBe(0);
    expect(f.started).toBe(0);
  });
  test("rejects media endpoints from another server, install, or call", () => {
    const f = fixture();
    for (const media_url of ["https://evil.example/steal", f.session.media_url.replace("42", "43"), f.session.media_url.replace("call-1", "call-2"), f.session.media_url + "?token=x", f.session.media_url + "/more"]) {
      expect(() => f.client.mediaURL({ ...f.session, media_url })).toThrow("Invalid Telephony media");
    }
  });
  test("failed microphone preflight never starts a paid call", async () => {
    const f = fixture();
    f.setPreflight(async () => { throw new Error("permission denied"); });
    await expect(f.phone.dial({ to: "+12025550100" })).rejects.toThrow("permission denied");
    expect(f.requests).toHaveLength(0);
    expect(f.phone.getSnapshot().busy).toBe(false);
    f.phone.dispose();
  });
  test("uncertain dial retry retains the idempotency key and disallows concurrent dial", async () => {
    const f = fixture();
    let attempts = 0;
    f.setResponse(async () => { if (++attempts === 1) throw new Error("network lost"); return f.session; });
    await expect(f.phone.dial({ to: "+12025550100" })).rejects.toThrow("network lost");
    const gate = deferred();
    f.setPreflight(() => gate.promise);
    const dialing = f.phone.dial({ to: "+12025550100" });
    await expect(f.phone.dial({ to: "+12025550100" })).rejects.toThrow("active operation");
    gate.resolve();
    await dialing;
    expect(f.requests[0].body.idempotency_key).toBe(f.requests[1].body.idempotency_key);
    f.phone.dispose();
  });
  test("supports live controls and keeps mute on reconnect; stale callbacks are ignored", async () => {
    const f = fixture();
    await f.phone.dial({ to: "+12025550100" });
    const stale = f.callbacks;
    f.phone.setMuted(true);
    f.phone.sendDTMF("12#");
    expect(f.muted).toBe(true);
    expect(f.dtmf).toBe("12#");
    f.setStart(async () => { expect(f.muted).toBe(true); f.callbacks.onState?.("live"); });
    await f.phone.reconnect();
    expect(f.muted).toBe(true);
    stale.onState?.("error", "old session");
    expect(f.phone.getSnapshot().audioState).toBe("live");
    f.phone.dispose();
  });
  test("audio setup failure hangs up an outbound leg", async () => {
    const f = fixture();
    f.setStart(async () => { throw new Error("worklet unavailable"); });
    await expect(f.phone.dial({ to: "+12025550100" })).rejects.toThrow("worklet unavailable");
    expect(f.requests.at(-1)?.url.pathname).toEndWith("/calls/call-1/hangup");
    expect(f.phone.getSnapshot().callId).toBeUndefined();
    expect(f.stopped).toBeGreaterThan(0);
  });
  test("failed hangup retains call identity for recovery", async () => {
    const f = fixture();
    await f.phone.answer("call-1");
    f.setResponse(async () => { throw new Error("hangup unreachable"); });
    await expect(f.phone.hangup()).rejects.toThrow("hangup unreachable");
    expect(f.phone.getSnapshot().callId).toBe("call-1");
    expect(f.phone.getSnapshot().busy).toBe(false);
    f.phone.dispose();
  });
  test("failed inbound media releases only its claim, and keeps identity if release fails", async () => {
    for (const fails of [false, true]) {
      const f = fixture();
      f.setStart(async () => { throw new Error("microphone denied"); });
      f.setResponse(async url => {
        if (fails && url.pathname.includes("/release/")) throw new Error("release failed");
        return f.session;
      });
      await expect(f.phone.answer("call-1")).rejects.toThrow("microphone denied");
      expect(f.requests.at(-1)?.body).toEqual({ session_token: "secret" });
      expect(f.phone.getSnapshot().callId).toBe(fails ? "call-1" : undefined);
      f.phone.dispose();
    }
  });
  test("dispose while awaiting permission cancels before placement", async () => {
    const f = fixture(), gate = deferred();
    f.setPreflight(() => gate.promise);
    const pending = f.phone.dial({ to: "+12025550100" });
    f.phone.dispose(); gate.resolve();
    await expect(pending).rejects.toThrow("cancelled");
    expect(f.requests).toHaveLength(0);
  });
  test("dispose during placement cleans up a late accepted call without opening audio", async () => {
    const f = fixture(), gate = deferred();
    f.setResponse(async () => { await gate.promise; return f.session; });
    const pending = f.phone.dial({ to: "+12025550100" });
    while (!f.requests.length) await Bun.sleep(1);
    f.phone.dispose(); gate.resolve();
    await expect(pending).rejects.toThrow("cancelled");
    expect(f.started).toBe(0);
    expect(f.requests.at(-1)?.url.pathname).toEndWith("/hangup");
  });
  test("media loss preserves the call, durable terminal status releases devices", async () => {
    const f = fixture();
    await f.phone.answer("call-1");
    f.callbacks.onState?.("error", "network down");
    expect(f.phone.getSnapshot().callId).toBe("call-1");
    f.phone.observeCall({ id: "another-call", status: "completed" });
    expect(f.phone.getSnapshot().callId).toBe("call-1");
    f.phone.observeCall({ id: "call-1", status: "completed" });
    expect(f.phone.getSnapshot().callId).toBeUndefined();
    expect(f.phone.getSnapshot().carrierStatus).toBe("completed");
  });
  test("observer exceptions cannot interrupt calls or disposal", async () => {
    const f = fixture();
    f.phone.subscribe(() => { throw new Error("host rendering failed"); });
    await f.phone.answer("call-1");
    expect(Object.isFrozen(f.phone.getSnapshot())).toBe(true);
    f.phone.dispose(); f.phone.dispose();
    expect(f.stopped).toBe(1);
    expect(f.requests).toHaveLength(1); // dispose does not hang up an established call
  });
  test("incoming list includes browser ring offers but excludes AI and completed calls", () => {
    const f = fixture();
    const base = { direction: "inbound", status: "pending", from_number: "", to_number: "", peer_kind: "human" };
    const result = f.client.incomingCalls([
      { ...base, id: "human" },
      { ...base, id: "group", peer_kind: "agent", ring_offers: [{ kind: "browser", destination_id: "desk" }] },
      { ...base, id: "ai", peer_kind: "agent" },
      { ...base, id: "waiting", routing_waiting: true },
      { ...base, id: "finished", status: "completed" },
    ]);
    expect(result.map(c => c.id)).toEqual(["human", "group"]);
  });
  test("watch cancellation suppresses late responses and overlapping requests", async () => {
    const f = fixture(), gate = deferred();
    f.setResponse(async () => { await gate.promise; return { calls: [] }; });
    let delivered = 0;
    const watch = f.client.watchCalls(() => { delivered++; }, { intervalMs: 100 });
    await Bun.sleep(150);
    expect(f.requests).toHaveLength(1);
    watch.close(); gate.resolve();
    await Bun.sleep(150);
    expect(f.requests).toHaveLength(1);
    expect(delivered).toBe(0);
  });
  test("active monitoring consumes durable completion and then stops", async () => {
    const f = fixture();
    const phone = f.client.createSoftphone({ audioRuntime: {
      preflight: async () => {},
      create: callbacks => ({ start: async () => { callbacks.onState?.("live"); }, stop() {}, setMuted() {}, setOutputVolume() {}, sendDTMF() {} }),
    }, pollIntervalMs: 100 });
    await phone.answer("call-1");
    f.setResponse(async () => ({ calls: [{ id: "call-1", status: "completed" }] }));
    const deadline = Date.now() + 2000;
    while (phone.getSnapshot().callId && Date.now() < deadline) await Bun.sleep(10);
    expect(phone.getSnapshot().carrierStatus).toBe("completed");
    const count = f.requests.length;
    await Bun.sleep(150);
    expect(f.requests).toHaveLength(count);
    phone.dispose();
  });
});

describe("softphone lifecycle races", () => {
  test("hangup interrupts unresolved audio setup and a late result cannot restore the call", async () => {
    const f = fixture(), gate = deferred();
    f.setStart(() => gate.promise);
    const startup = f.phone.answer("call-1").catch(error => error);
    while (!f.started) await Bun.sleep(1);
    await f.phone.hangup();
    expect((await startup).message).toContain("cancelled");
    expect(f.phone.getSnapshot()).toMatchObject({ audioState: "idle", busy: false });
    gate.resolve(); await Bun.sleep(1);
    expect(f.phone.getSnapshot().callId).toBeUndefined();
    expect(f.requests.filter(r => r.url.pathname.includes("/release/"))).toHaveLength(0);
  });
  test("cancel a permission prompt without waiting for the user to answer it", async () => {
    const f = fixture(), gate = deferred();
    f.setPreflight(() => gate.promise);
    const startup = f.phone.dial({ to: "+12025550100" }).catch(error => error);
    await f.phone.hangup();
    expect((await startup).message).toContain("cancelled");
    expect(f.phone.getSnapshot().busy).toBe(false);
    gate.resolve(); await Bun.sleep(1);
    expect(f.requests).toHaveLength(0);
  });
  test("cancelling an in-flight placement waits for its outcome and cleans up once", async () => {
    const f = fixture(), gate = deferred();
    f.setResponse(async url => { if (url.pathname.endsWith("/place")) await gate.promise; return f.session; });
    const startup = f.phone.dial({ to: "+12025550100" }).catch(error => error);
    while (!f.requests.length) await Bun.sleep(1);
    await f.phone.hangup();
    expect(f.phone.getSnapshot().busy).toBe(true);
    await expect(f.phone.dial({ to: "+12025550101" })).rejects.toThrow("active operation");
    gate.resolve();
    expect((await startup).message).toContain("cancelled");
    expect(f.started).toBe(0);
    expect(f.requests.filter(r => r.url.pathname.endsWith("/hangup"))).toHaveLength(1);
    expect(f.phone.getSnapshot().busy).toBe(false);
  });
  test("terminal status during reconnect frees controls and cannot be overwritten by stale startup", async () => {
    const f = fixture(), gate = deferred();
    await f.phone.answer("call-1");
    f.setStart(() => gate.promise);
    const reconnecting = f.phone.reconnect().catch(error => error);
    f.phone.observeCall({ id: "call-1", status: "completed" });
    expect((await reconnecting).message).toContain("cancelled");
    f.setStart(async () => { f.callbacks.onState?.("live"); });
    await f.phone.answer("call-1");
    const stopped = f.stopped;
    gate.resolve(); await Bun.sleep(1);
    expect(f.phone.getSnapshot()).toMatchObject({ callId: "call-1", audioState: "live", busy: false });
    expect(f.stopped).toBe(stopped);
    f.phone.dispose();
  });
  test("carrier call.ended clears identity without waiting for a list poll", async () => {
    const f = fixture(); await f.phone.answer("call-1");
    f.callbacks.onState?.("ended", "call.ended");
    expect(f.phone.getSnapshot()).toMatchObject({ audioState: "idle", carrierStatus: "completed", busy: false });
    expect(f.phone.getSnapshot().callId).toBeUndefined();
  });
  test("an audio error during startup must not report successful answer", async () => {
    const f = fixture();
    f.setStart(async () => { f.callbacks.onState?.("error", "carrier refused"); });
    await expect(f.phone.answer("call-1")).rejects.toThrow("carrier refused");
    expect(f.requests.at(-1)?.url.pathname).toContain("/release/");
  });
  test("throwing media observers cannot break startup or disconnect audio", async () => {
    const f = fixture();
    let callbacks: SoftphoneCallbacks = {};
    const phone = f.client.createSoftphone({ pollIntervalMs: 0,
      onLevels() { throw new Error("broken meter"); },
      onNotice() { throw new Error("broken toast"); },
      onDiagnostics() { throw new Error("broken chart"); },
      audioRuntime: { preflight: async () => {}, create(cb) {
        callbacks = cb;
        return { async start() { cb.onLevels?.(1, 1); cb.onNotice?.("hello"); cb.onDiagnostics?.({} as any); cb.onState?.("live"); },
          stop() {}, setMuted() {}, setOutputVolume() {}, sendDTMF() {} };
      } },
    });
    await phone.answer("call-1");
    expect(phone.getSnapshot().audioState).toBe("live");
    expect(() => callbacks.onLevels?.(1, 1)).not.toThrow();
    phone.dispose();
  });
  test("synchronous audio factory failure retains and monitors a claim if cleanup is unavailable", async () => {
    const f = fixture();
    f.setResponse(async url => {
      if (url.pathname.includes("/release/")) throw new Error("unreachable");
      if (url.pathname.endsWith("/calls")) return { calls: [{ id: "call-1", status: "completed" }] };
      return f.session;
    });
    const phone = f.client.createSoftphone({ pollIntervalMs: 100, audioRuntime: {
      preflight: async () => {}, create() { throw new Error("worker unsupported"); },
    } });
    await expect(phone.answer("call-1")).rejects.toThrow("worker unsupported");
    expect(phone.getSnapshot().callId).toBe("call-1");
    const deadline = Date.now() + 1000;
    while (phone.getSnapshot().callId && Date.now() < deadline) await Bun.sleep(10);
    expect(phone.getSnapshot().carrierStatus).toBe("completed");
    phone.dispose();
  });
});

test("a server rollback after carrier answer failure makes the same call answerable again", async () => {
  const f = fixture();
  await f.phone.answer("call-1");
  f.setResponse(async url => url.pathname.endsWith("/calls") ? { calls: [{ id: "call-1", status: "pending" }] } : f.session);
  f.callbacks.onState?.("error", "The carrier could not answer the call.");
  await Bun.sleep(1);
  expect(f.phone.getSnapshot().callId).toBeUndefined();
  expect(f.phone.getSnapshot().carrierStatus).toBe("pending");
  await f.phone.answer("call-1");
  expect(f.phone.getSnapshot().audioState).toBe("live");
  f.phone.dispose();
});

describe("authorized application-user sessions", () => {
  test("attach uses the existing call and never places or answers a carrier leg", async () => {
    const f = fixture();
    await f.phone.attach("call-1");
    expect(f.requests).toHaveLength(1);
    expect(f.requests[0].url.pathname).toEndWith("/softphone/attach/call-1");
    expect(f.phone.getSnapshot().callId).toBe("call-1");
    expect(f.preflighted).toBe(1);
    f.phone.dispose();
  });
  test("takeover is an explicit separate endpoint", async () => {
    const f = fixture();
    await f.phone.takeover("call-1");
    expect(f.requests[0].url.pathname).toEndWith("/softphone/takeover/call-1");
    f.phone.dispose();
  });
  test("renewal uses current SDK auth, with the session token only in the body", async () => {
    const f = fixture();
    f.sdk.setAccessToken("refreshed-user-session");
    await f.client.renew(f.session);
    expect(f.requests[0].headers.get("Authorization")).toBe("Bearer refreshed-user-session");
    expect(f.requests[0].body).toEqual({ session_token: "secret" });
    expect(f.requests[0].url.search).not.toContain("secret");
  });
});


test("online provider routes calls through authenticated user API", async () => {
  const f = fixture();
  const client = new TelephonyClient(f.client.app, { authProvider: "customer-login" });
  await client.listCalls();
  await client.attach("call-1");
  expect(f.requests.every(r => r.url.pathname.includes("/telephony/user/"))).toBe(true);
  expect(f.requests.every(r => r.url.searchParams.get("auth_provider") === "customer-login")).toBe(true);
  expect(f.requests.every(r => r.headers.get("Authorization") === "Bearer first")).toBe(true);
});

test("lease renewal runs without status polling and stops audio on revoked authentication", async () => {
  const f = fixture();
  f.setResponse(async url => {
    if (url.pathname.includes("/renew/")) throw new Error("login revoked");
    return { ...f.session, lease_seconds: 10 };
  });
  await f.phone.attach("call-1");
  await new Promise(resolve => setTimeout(resolve, 3500));
  expect(f.requests.some(r => r.url.pathname.includes("/renew/"))).toBe(true);
  expect(f.stopped).toBe(1);
  expect(f.phone.getSnapshot().audioState).toBe("error");
  expect(f.phone.getSnapshot().detail).toContain("login revoked");
  f.phone.dispose();
});

test("audio tuning passes through create/reconnect and invalid profiles leave a live call alone", async () => {
 const f = fixture();
 const phone = f.client.createSoftphone({ audioRuntime: f.runtime, pollIntervalMs: 0, audio: { inputGainDB: 0, playbackTargetMs: 40 } });
 try {
  await phone.dial({ to: "+14155550100" });
  expect(f.startedOptions.at(-1).playbackTargetMs).toBe(40);
  await phone.reconnect({ inputGainDB: -6, playbackTargetMs: 80, playbackMaxMs: 120 });
  expect(f.startedOptions.at(-1)).toMatchObject({ inputGainDB: -6, playbackTargetMs: 80, playbackMaxMs: 120 });
  const stopped = f.stopped, started = f.started;
  await expect(phone.reconnect({ playbackTargetMs: 200 })).rejects.toThrow();
  expect(f.stopped).toBe(stopped); expect(f.started).toBe(started);
 } finally { await phone.dispose(); await f.phone.dispose(); }
 expect(() => f.client.createSoftphone({ audio: { playbackTargetMs: NaN } })).toThrow();
});
