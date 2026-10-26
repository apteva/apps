import { describe, expect, test, spyOn } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { TelephonyClient, telephonyExtension, callTerminationLabel, type CallControlResult, type CallSession } from "../src/client";
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
    const value = await response(parsed, body);
    return value instanceof Response ? value : Response.json(value);
  }) as typeof fetch });
  const client = sdk.use(telephonyExtension, { projectId: "p1", installId: 42 });
  let callbacks: SoftphoneCallbacks = {};
  let stopped = 0, started = 0, preflighted = 0;
  let preflight = async () => {};
  let start = async () => { callbacks.onState?.("live"); };
  let muted = false, dtmf = "";
  const startedOptions: any[] = [];
  const startedURLs: string[] = [];
  const runtime: AudioRuntime = {
    async preflight() { preflighted++; await preflight(); },
    create(cb) {
      callbacks = cb;
      return {
        async start(url, options) { startedOptions.push(options); startedURLs.push(url); expect(url).toStartWith("wss://gateway.example/api/apps/telephony/_install/42/softphone/media/call-1/"); started++; await start(); },
        stop() { stopped++; }, setMuted(value) { muted = value; },
        sendDTMF(value) { dtmf = value; }, setOutputVolume() {},
      };
    },
  };
  const phone = client.createSoftphone({ audioRuntime: runtime, pollIntervalMs: 0 });
  return { sdk, client, phone, requests, session, runtime, startedOptions, startedURLs,
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

test("answer prepares microphone and scripts before claiming a carrier call", async () => {
  const f = fixture(), gate = deferred();
  f.setPreflight(() => gate.promise);
  const pending = f.phone.answer("call-1");
  await Bun.sleep(1);
  expect(f.preflighted).toBe(1);
  expect(f.requests).toHaveLength(0);
  gate.resolve(); await pending;
  expect(f.requests[0].url.pathname).toEndWith("/answer/call-1");
  f.phone.dispose();
});

test("failed or cancelled inbound preflight never claims the call", async () => {
  const failed = fixture(); failed.setPreflight(async () => { throw new Error("permission denied"); });
  await expect(failed.phone.answer("call-1")).rejects.toThrow("permission denied");
  expect(failed.requests).toHaveLength(0); failed.phone.dispose();
  const f = fixture(), gate = deferred(); f.setPreflight(() => gate.promise);
  const pending = f.phone.answer("call-1").catch(error => error);
  await f.phone.hangup();
  expect((await pending).message).toContain("cancelled");
  gate.resolve(); await Bun.sleep(1);
  expect(f.requests).toHaveLength(0); f.phone.dispose();
});

test("reconnect preparation keeps live audio and ownership until ready", async () => {
  const f = fixture(); await f.phone.answer("call-1");
  const before = f.requests.length, stopped = f.stopped, gate = deferred();
  f.runtime.prepare = async () => { await gate.promise; };
  const pending = f.phone.reconnect(); await Bun.sleep(1);
  expect(f.stopped).toBe(stopped);
  expect(f.requests).toHaveLength(before);
  gate.resolve(); await pending;
  expect(f.requests[before].url.pathname).toEndWith("/attach/call-1");
  f.phone.dispose();
});

test("failed reconnect preparation leaves the current audio running", async () => {
  const f = fixture(); await f.phone.answer("call-1");
  const before = f.requests.length, stopped = f.stopped;
  f.runtime.prepare = async () => { throw new Error("asset unavailable"); };
  await expect(f.phone.reconnect()).rejects.toThrow("asset unavailable");
  expect(f.stopped).toBe(stopped); expect(f.requests).toHaveLength(before);
  expect(f.phone.getSnapshot().audioState).toBe("live"); f.phone.dispose();
});

test("ending during preparation cannot acquire a replacement session later", async () => {
  const f = fixture(); await f.phone.answer("call-1");
  const gate = deferred(); f.runtime.prepare = () => gate.promise;
  const pending = f.phone.reconnect().catch(error => error);
  await f.phone.hangup(); expect((await pending).message).toContain("cancelled");
  gate.resolve(); await Bun.sleep(1);
  expect(f.requests.filter(request => request.url.pathname.includes("/attach/"))).toHaveLength(0);
  expect(f.phone.getSnapshot().callId).toBeUndefined(); f.phone.dispose();
});

describe("Telephony extension", () => {
  test("outbound choices use the authorized headless endpoint and preserve provider warnings", async () => {
    const f=fixture();
    const result={numbers:[{phone_number:"+33123456789",provider:"generic-carrier",carrier_connection_id:7,outbound_enabled:true,outbound:{required:false,status:"ready"}}],inventory_status:"partial" as const,warnings:[{provider:"unavailable-carrier",carrier_connection_id:8,code:"inventory_unavailable",message:"Unavailable"}]};
    f.setResponse(async () => result);
    expect(await f.client.outboundNumbers()).toEqual(result);
    const request=f.requests[0];
    expect(request.url.pathname).toEndWith("/softphone/numbers");
    expect(request.url.searchParams.get("project_id")).toBe("p1");
    expect(request.url.searchParams.get("install_id")).toBe("42");
    expect(request.headers.get("Authorization")).toBe("Bearer first");
  });

  test("fresh inventory keeps authorization and project scoping", async () => {
    const f = fixture();
    f.setResponse(async () => ({ numbers: [], inventory_status: "available", warnings: [] }));
    await f.client.outboundNumbers({ fresh: true });
    const request = f.requests[0];
    expect(request.url.searchParams.get("fresh")).toBe("true");
    expect(request.url.searchParams.get("project_id")).toBe("p1");
    expect(request.url.searchParams.get("install_id")).toBe("42");
    expect(request.headers.get("Authorization")).toBe("Bearer first");
  });

  test("call controls use the same authenticated project-scoped client", async () => {
    const f = fixture();
    const state: CallControlResult = { call_id: "call-1", hold_state: "held", recording_state: "pause_requested", control_error: "",
      capabilities: { hold_music: true, recording_pause: true } };
    f.setResponse(async () => state);
    expect(await f.client.hold("call-1")).toEqual(state);
    await f.client.resume("call-1");
    await f.client.pauseRecording("call-1");
    await f.client.resumeRecording("call-1");
    expect(f.requests.map(r => r.url.pathname.split("/").at(-1))).toEqual(["hold", "resume", "pause-recording", "resume-recording"]);
    expect(f.requests.every(r => r.url.searchParams.get("project_id") === "p1" && r.url.searchParams.get("install_id") === "42")).toBe(true);
  });
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
  test("headless softphone controls its attached call and updates the snapshot", async () => {
    const f = fixture();
    const result: CallControlResult = { call_id: "call-1", hold_state: "starting", recording_state: "active",
      control_error: "", capabilities: { hold_music: true, recording_pause: true } };
    f.setResponse(async url => url.pathname.endsWith("/hold") ? result : f.session);
    await f.phone.dial({ to: "+12025550100" });
    expect(await f.phone.hold()).toEqual(result);
    expect(f.phone.getSnapshot().holdState).toBe("starting");
    expect(f.requests.at(-1)?.url.pathname).toEndWith("/calls/call-1/hold");
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
  test("incoming list includes answerable browser offers but excludes stale and supervisor-visible calls", () => {
    const f = fixture();
    const base = { direction: "inbound", status: "pending", from_number: "", to_number: "", peer_kind: "human" };
    const result = f.client.incomingCalls([
      { ...base, id: "human" },
      { ...base, id: "group", peer_kind: "agent", ring_offers: [{ id: "offer-desk", kind: "browser", destination_id: "desk" }] },
      { ...base, id: "moved", answerable: false, ring_offers: [{ id: "offer-other", kind: "browser", destination_id: "other" }] },
      { ...base, id: "supervisor", answerable: false },
      { ...base, id: "ai", peer_kind: "agent" },
      { ...base, id: "waiting", routing_waiting: true },
      { ...base, id: "finished", status: "completed" },
    ]);
    expect(result.map(c => c.id)).toEqual(["human", "group"]);
  });
  test("Answer exposes offer_expired as a stable error code", async () => {
    const f = fixture();
    f.setResponse(async () => Response.json({ code: "offer_expired", error: "call offer expired" }, { status: 409 }));
    await expect(f.client.answer("call-1")).rejects.toMatchObject({ code: "offer_expired", status: 409 });
  });
  test("watch cancellation suppresses late responses and overlapping requests", async () => {
    const f = fixture(), gate = deferred();
    f.setResponse(async () => { await gate.promise; return { calls: [] }; });
    let delivered = 0;
    const watch = f.client.watchCalls(() => { delivered++; }, { intervalMs: 100 });
    await Bun.sleep(150);
    expect(f.requests.filter(r=>r.url.pathname.endsWith("/calls"))).toHaveLength(1);
    watch.close(); gate.resolve();
    await Bun.sleep(150);
    expect(f.requests.filter(r=>r.url.pathname.endsWith("/calls"))).toHaveLength(1);
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
    // Attach claims the existing call, then reads it once for direction and
    // status. It never places or answers a carrier leg.
    expect(f.requests).toHaveLength(2);
    expect(f.requests[0].url.pathname).toEndWith("/softphone/attach/call-1");
    expect(f.requests[1].url.pathname).toEndWith("/calls");
    expect(f.requests.every((r) => !/\/(place|answer)/.test(r.url.pathname))).toBe(true);
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
    if (url.pathname.includes("/renew/")) return new Response("login revoked", { status: 403 });
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
  expect(() => phone.configureAudio({ playbackTargetMs: 200 })).toThrow();
  await expect(phone.reconnect({ playbackTargetMs: 200 })).rejects.toThrow();
  expect(f.stopped).toBe(stopped); expect(f.started).toBe(started);
 } finally { await phone.dispose(); await f.phone.dispose(); }
 expect(() => f.client.createSoftphone({ audio: { playbackTargetMs: NaN } })).toThrow();
});


test("a temporary renewal failure preserves healthy audio and retries", async () => {
 const f=fixture(); let renews=0;
 f.setResponse(async url=>{
  if(url.pathname.includes("/renew/")) { if(++renews===1) throw new TypeError("Failed to fetch"); return {lease_seconds:10}; }
  return {...f.session,lease_seconds:10};
 });
 try {
  await f.phone.attach("call-1"); await Bun.sleep(3700);
  expect(renews).toBe(2); expect(f.stopped).toBe(0); expect(f.phone.getSnapshot().audioState).toBe("live");
 } finally { f.phone.dispose(); }
});

test("explicit and automatic reconnect obtain fresh credentials without placing or answering",async()=>{
 const f=fixture();let token=0;
 f.setResponse(async url=>url.pathname.includes("/attach/")?{...f.session,session_token:`fresh-${++token}`,media_url:f.session.media_url.replace("secret",`fresh-${token}`)}:{calls:[{id:"call-1",status:"answered"}]});
 try {
  await f.phone.attach("call-1");f.phone.setMuted(true);
  await f.phone.reconnect();expect(f.startedURLs.at(-1)).toEndWith("fresh-2");expect(f.muted).toBe(true);
  const refresh=f.callbacks.refreshMediaURL!;
  const [a,b]=await Promise.all([refresh(),refresh()]);expect(a).toBe(b);expect(a).toEndWith("fresh-3");
  expect(token).toBe(3);expect(f.requests.some(r=>/\/(place|answer|takeover)/.test(r.url.pathname))).toBe(false);
 } finally {f.phone.dispose();}
});

test("late automatic authorization cannot revive a terminated call",async()=>{
 const f=fixture();await f.phone.attach("call-1");
 let resolve!:(value:any)=>void;
 f.setResponse(async url=>url.pathname.includes("/attach/")?await new Promise(r=>{resolve=r}):{ok:true});
 const pending=f.callbacks.refreshMediaURL!();
 f.phone.observeCall({id:"call-1",status:"completed"});resolve(f.session);
 await expect(pending).rejects.toThrow("cancelled");expect(f.phone.getSnapshot().callId).toBeUndefined();expect(f.started).toBe(1);f.phone.dispose();
});


test("AI policy endings have intentional, localized labels", () => {
 expect(callTerminationLabel({reason:"ai_max_duration"}, "fr")).toBe("Durée maximale atteinte");
 expect(callTerminationLabel({reason:"ai_inactivity"}, "en")).toBe("Call ended: no response");
 expect(callTerminationLabel({reason:"ai_inactivity"}, "fr")).toBe("Fin de l’appel : aucune réponse");
 expect(callTerminationLabel({reason:"ai_policy_failure"}, "en")).toBe("AI inactivity reminder failed");
});

test("headless recovery carries attempt IDs and session generations without changing call control",async()=>{
 const f=fixture();let generation=0;
 f.setResponse(async(url)=>{
  if(url.pathname.endsWith("/calls"))return {calls:[{id:"call-1",status:"answered"}]};
  return {...f.session,session_generation:`session-${(++generation).toString(16)}`,session_token:`secret-${generation}`,media_url:f.session.media_url};
 });
 await f.phone.dial({to:"+33123456789",from:"+33912345678"});
 await f.phone.reconnect(undefined,"audio_device_change");
 const explicit=f.requests.filter(r=>r.url.pathname.includes("/attach/")).at(-1)!.body.media_diagnostics;
 expect(explicit.initiating_action).toBe("audio_device_change");expect(explicit.previous_session_id).toBe("session-1");
 expect(explicit.recovery_id).toStartWith("recovery-");expect(explicit.attempt_id).toStartWith("attempt-");
 const context={recovery_id:"recovery-a",attempt_id:"attempt-b",initiating_action:"automatic_retry" as const};
 const refresh=f.callbacks.refreshMediaURL!;const first=refresh(context),second=refresh({...context,attempt_id:"attempt-c"});
 expect(first).toBe(second);await first;
 const automatic=f.requests.filter(r=>r.url.pathname.includes("/attach/")).at(-1)!.body.media_diagnostics;
 expect(automatic).toMatchObject(context);expect(automatic.previous_session_id).toBe("session-2");
 expect(JSON.stringify(automatic)).not.toContain("secret");
 expect(f.requests.filter(r=>r.url.pathname.includes("/place")).length).toBe(1);
 expect(f.requests.filter(r=>r.url.pathname.includes("/answer/")||r.url.pathname.endsWith("/hangup"))).toHaveLength(0);
 f.phone.dispose();
});

test("outbound transport configuration uses the installation and project scope",async()=>{
 const f=fixture();
 f.setResponse(async()=>({ok:true,outbound_transport:{transport:"sip_direct",scope:"number",credentials_configured:true}}));
 const r=await f.client.outboundTransport({carrier_connection_id:10,phone_number:"+33123456780",transport:"sip_direct",config:{host:"sip.telnyx.com",resource_id:"sip-1",auth_mode:"ip",media_encryption:"srtp_sdes"}});
 expect(r.outbound_transport?.transport).toBe("sip_direct");
 expect(f.requests[0].url.pathname).toBe("/api/apps/telephony/numbers/outbound-transport");
 expect(f.requests[0].url.searchParams.get("project_id")).toBe("p1");
 expect(f.requests[0].url.searchParams.get("install_id")).toBe("42");
 expect(f.requests[0].body.carrier_connection_id).toBe(10);
});

test("automatic SIP setup and profile policy use the same headless account scope",async()=>{
 const f=fixture();
 f.setResponse(async()=>({ok:true,setup_options:{automatic_supported:true,profiles:[{id:"profile-1",name:"France",enabled:true,destinations:["FR"],daily_spend_limit_enabled:true,daily_spend_limit:"20.00"}]}}));
 const options=await f.client.outboundTransport({carrier_connection_id:10,setup_options:true});
 expect(options.setup_options?.profiles[0]?.destinations).toEqual(["FR"]);
 await f.client.outboundTransport({carrier_connection_id:10,phone_number:"+33123456780",transport:"sip_direct",provision:true,profile_policy:{destinations:["FR"],daily_spend_limit:"20.00"}});
 expect(f.requests[1].body).toEqual({carrier_connection_id:10,phone_number:"+33123456780",transport:"sip_direct",provision:true,profile_policy:{destinations:["FR"],daily_spend_limit:"20.00"}});
 expect(f.requests[1].headers.get("Authorization")).toBe("Bearer first");
 expect(f.requests[1].url.searchParams.get("project_id")).toBe("p1");
 expect(f.requests[1].url.searchParams.get("install_id")).toBe("42");
});

test("standalone status push reduces polls while live and retains old-server fallback",async()=>{
 const f=fixture();const phone=f.client.createSoftphone({audioRuntime:f.runtime,pollIntervalMs:100});
 try{
  await phone.answer("call-1");
  await Bun.sleep(120);
  const count=()=>f.requests.filter(r=>r.url.pathname.endsWith("/calls")).length;
  expect(count()).toBeGreaterThan(0);
  f.callbacks.onCallStatus?.({call_id:"call-1",status:"in-progress"});
  const before=count();await Bun.sleep(350);expect(count()).toBe(before);
  f.callbacks.onState?.("connecting");await Bun.sleep(150);expect(count()).toBeGreaterThan(before);
 }finally{phone.dispose();}
});


test("aborted old status request cannot disable push reconciliation on a reconnected call",async()=>{
 const f=fixture();let polls=0;
 let rejectFirst!:(error:Error)=>void;
 const blocked=new Promise<never>((_,reject)=>{rejectFirst=reject;});
 f.setResponse(async url=>{
  if(url.pathname.endsWith("/calls")){polls++;if(polls===1)return blocked;return {calls:[{id:"call-1",status:"in-progress"}]};}
  return f.session;
 });
 const phone=f.client.createSoftphone({audioRuntime:f.runtime,pollIntervalMs:100});
 try{
  await phone.answer("call-1");
  const until=Date.now()+1000;while(!polls&&Date.now()<until)await Bun.sleep(1);expect(polls).toBe(1);
  await phone.reconnect();
  f.callbacks.onCallStatus?.({call_id:"call-1",status:"in-progress"});
  const before=polls;rejectFirst(new Error("old request canceled"));await Bun.sleep(350);
  expect(polls).toBe(before);
 }finally{rejectFirst(new Error("cleanup"));phone.dispose();}
});

test("network recovery rejoins the existing call with a guarded session and preserves mute",async()=>{
 const f=fixture();let generation=0;f.setResponse(async(url)=>url.pathname.endsWith("/calls")?{calls:[{id:"call-1",status:"answered"}]}:{...f.session,session_generation:`session-${(++generation).toString(16)}`,attachment_guard_version:1});
 await f.phone.dial({to:"+12025550100"});f.phone.setMuted(true);
 f.callbacks.onState?.("error","temporary outage",{recoverable:true});expect(f.phone.getSnapshot().audioState).toBe("reconnecting");
 const recorded:any[]=[];const phone=f.phone as any;phone.options.onSessionEvent=(e:any)=>recorded.push(e);
 const loop=phone.automaticRecovery;await loop.tick();
 const complete=recorded.find(e=>e.action==="network_recovery"&&e.outcome==="connected");expect(complete?.recovery_id).toStartWith("recovery-");expect(complete?.attempt_id).toStartWith("attempt-");
 expect(f.phone.getSnapshot().audioState).toBe("live");expect(f.phone.getSnapshot().muted).toBe(true);
 const attach=f.requests.find(r=>r.url.pathname.includes("/attach/"));expect(attach?.body.attachment_guard.expected_generation).toBe("session-1");
 expect(f.requests.filter(r=>r.url.pathname.includes("/place"))).toHaveLength(1);expect(f.requests.filter(r=>r.url.pathname.endsWith("/hangup"))).toHaveLength(0);f.phone.dispose();
});
test("unknown attachment outcome retries the same mutation; hangup stops the loop",async()=>{
 const f=fixture();let stage=0;f.setResponse(async(url)=>{if(url.pathname.endsWith("/calls"))return {calls:[{id:"call-1",status:"answered"}]};if(url.pathname.includes("/attach/")){if(stage++===0)throw Error("response lost");return {...f.session,session_generation:"session-2",attachment_guard_version:1};}return {...f.session,session_generation:"session-1",attachment_guard_version:1};});
 await f.phone.dial({to:"+12025550100"});f.callbacks.onState?.("error","network",{recoverable:true});const loop=(f.phone as any).automaticRecovery;await loop.tick();await loop.tick();
 const calls=f.requests.filter(r=>r.url.pathname.includes("/attach/"));expect(calls).toHaveLength(2);expect(calls[0].body.attachment_guard).toEqual(calls[1].body.attachment_guard);
 f.callbacks.onState?.("error","network",{recoverable:true});const stopped=(f.phone as any).automaticRecovery;await f.phone.hangup();await stopped.tick();expect(f.requests.filter(r=>r.url.pathname.includes("/attach/"))).toHaveLength(2);f.phone.dispose();
});
test("permanent media and microphone failures do not restart audio",async()=>{
 const f=fixture();await f.phone.dial({to:"+12025550100"});f.callbacks.onState?.("error","denied");expect((f.phone as any).automaticRecovery).toBeUndefined();f.phone.dispose();
});

test("background network check never holds inbound answer or outbound placement",async()=>{
 for(const direction of ["inbound","outbound"]){
  const f=fixture(),policyGate=deferred();
  f.setResponse(async(url)=>{if(url.pathname.endsWith("/network-policy")){await policyGate.promise;return {effective:{media_transport:"webrtc"}};}return f.session;});
  const checking=f.phone.checkNetwork();await Bun.sleep(1);
  if(direction==="inbound")await f.phone.answer("call-1");else await f.phone.dial({to:"+33123456789"});
  expect(f.phone.getSnapshot().audioState).toBe("live");expect((await checking).state).toBe("cancelled");expect(f.started).toBe(1);
  expect(f.requests.some(r=>r.url.pathname.endsWith("/network-check"))).toBe(false);
  expect(f.startedOptions[0].mediaTransport).toBeUndefined();
  policyGate.resolve();await Bun.sleep(1);expect(f.started).toBe(1);expect(f.phone.getSnapshot().audioState).toBe("live");f.phone.dispose();
 }
});

test("network checks do not mark a phone busy, change live controls or survive disposal",async()=>{
 const f=fixture(),gate=deferred();f.setResponse(async()=>{await gate.promise;return {};});
 const pending=f.phone.checkNetwork();await Bun.sleep(1);expect(f.phone.getSnapshot().busy).toBe(false);expect(f.phone.getSnapshot().phase).toBe("idle");
 f.phone.dispose();expect((await pending).state).toBe("cancelled");gate.resolve();await Bun.sleep(1);expect(f.started).toBe(0);expect(f.phone.getSnapshot().busy).toBe(false);
});

test("incoming offers stop even policy discovery and prevent a later probe from starting",async()=>{
 const f=fixture(),gate=deferred();f.setResponse(async url=>{
  if(url.pathname.endsWith("/network-policy")){await gate.promise;return {effective:{}};}
  if(url.pathname.endsWith("/calls"))return {calls:[{id:"incoming-2",status:"pending",direction:"inbound",peer_kind:"human",answerable:true}]};return f.session;
 });
 const checking=f.phone.checkNetwork();await Bun.sleep(1);await f.client.listCalls();expect((await checking).state).toBe("cancelled");
 await expect(f.phone.checkNetwork()).rejects.toThrow("incoming offers");gate.resolve();await Bun.sleep(1);
 expect(f.requests.some(r=>r.url.pathname.endsWith("/network-check"))).toBe(false);expect(f.phone.getSnapshot().busy).toBe(false);f.phone.dispose();
});

test("network profiles affect the next attachment and restore inherited transport",async()=>{
 const {validateNetworkPolicy}=await import("../src/network-policy");
 const f=fixture();f.phone.configureAudio({mediaTransport:"websocket"});
 f.phone.applyNetworkPolicy(validateNetworkPolicy({media_transport:"webrtc",initial_bitrate_bps:24000,max_bitrate_bps:24000,playback_target_ms:80}));
 await f.phone.answer("call-1");expect(f.startedOptions[0].mediaTransport).toBe("webrtc");expect(f.startedOptions[0].playbackTargetMs).toBe(80);
 f.phone.applyNetworkPolicy(validateNetworkPolicy({media_transport:"inherit"}));
 expect(f.started).toBe(1);expect(f.phone.getSnapshot().audioState).toBe("live");expect(f.startedOptions[0].mediaTransport).toBe("webrtc");
 await f.phone.reconnect();expect(f.startedOptions[1].mediaTransport).toBe("websocket");expect(f.startedOptions[1].networkProfile?.initial_bitrate_bps).toBe(32000);f.phone.dispose();
});

test("unsaved network defaults never overwrite existing host playback preferences",async()=>{
 const {DEFAULT_NETWORK_POLICY}=await import("../src/network-policy");const f=fixture();
 f.phone.configureAudio({mediaTransport:"webrtc",playbackTargetMs:100,playbackMaxMs:280});
 f.setResponse(async url=>url.pathname.endsWith("/network-policy")?{configured:false,effective:DEFAULT_NETWORK_POLICY}:f.session);
 expect((await f.phone.checkNetwork()).state).toBe("unavailable");await f.phone.answer("call-1");
 expect(f.startedOptions[0].playbackTargetMs).toBe(100);expect(f.startedOptions[0].playbackMaxMs).toBe(280);expect(f.startedOptions[0].mediaTransport).toBe("webrtc");f.phone.dispose();
});

test("removing a network override restores host playback and does not restart live audio",async()=>{
 const {validateNetworkPolicy}=await import("../src/network-policy");const f=fixture();
 f.phone.configureAudio({mediaTransport:"websocket",playbackTargetMs:100,playbackMinMs:80,playbackMaxMs:280});
 f.phone.applyNetworkPolicy(validateNetworkPolicy({media_transport:"webrtc",playback_target_ms:60}));
 await f.phone.answer("call-1");expect(f.startedOptions[0].playbackTargetMs).toBe(60);expect(f.startedOptions[0].playbackMinMs).toBe(60);
 f.phone.applyNetworkPolicy();expect(f.started).toBe(1);await f.phone.reconnect();
 expect(f.startedOptions[1]).toMatchObject({mediaTransport:"websocket",playbackTargetMs:100,playbackMinMs:80,playbackMaxMs:280});expect(f.startedOptions[1].networkProfile).toBeUndefined();f.phone.dispose();
});


test("device/worklet stall after answer releases the claim and allows a fresh answer",async()=>{
 const f=fixture(),pending=deferred();let cancels=0;f.runtime.cancelPreparation=()=>{cancels++;};
 f.setStart(()=>pending.promise);
 const original=setTimeout;
 const timer=spyOn(globalThis,"setTimeout").mockImplementation(((fn:any,ms:number,...args:any[])=>original(fn,ms===20000?5:ms,...args)) as typeof setTimeout);
 try {
  await expect(f.phone.answer("call-1")).rejects.toThrow("Audio setup timed out");
  expect(f.requests.filter(r=>r.url.pathname.includes("/release/"))).toHaveLength(1);
  expect(f.phone.getSnapshot().busy).toBe(false);expect(f.phone.getSnapshot().callId).toBeUndefined();
  expect(f.stopped).toBe(1);expect(cancels).toBeGreaterThan(0);
  f.setStart(async()=>{f.callbacks.onState?.("live");});await f.phone.answer("call-1");
  pending.resolve();await Bun.sleep(0);
  expect(f.phone.getSnapshot().audioState).toBe("live");expect(f.stopped).toBe(1);
 }finally{timer.mockRestore();f.phone.dispose();}
});

test("answer request failure and cancelled preflight release prepared local devices",async()=>{
 const f=fixture();let cleaned=0;f.runtime.cancelPreparation=()=>{cleaned++;};
 f.setResponse(async()=>new Response("gone",{status:410}));await expect(f.phone.answer("call-1")).rejects.toThrow();
 expect(cleaned).toBeGreaterThanOrEqual(2);expect(f.started).toBe(0);
 const gate=deferred();f.setPreflight(()=>gate.promise);const starting=f.phone.answer("call-1").catch(e=>e);
 const before=cleaned;await f.phone.hangup();expect(cleaned).toBeGreaterThan(before);await starting;gate.resolve();f.phone.dispose();
});
