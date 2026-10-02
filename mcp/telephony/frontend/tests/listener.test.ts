import { describe, expect, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { TelephonyClient } from "../src/client";
import { decodeListenerFrame, type ListenerAudioCallbacks, type ListenerAudioRuntime } from "../src/listener-audio";

function fixture() {
  const requests: Array<{ url: URL; body: any }> = [];
  let response: (url: URL) => unknown = url => url.pathname.includes("listen-renew") || url.pathname.includes("listen-stop") ? { ok: true } : ({ call_id: "call", media_url: "/api/apps/telephony/_install/42/softphone/listen-media/call/secret", session_token: "secret", lease_seconds: 60 });
  const sdk = new AptevaClient({ baseURL: "https://gateway.example", accessToken: "user-token", fetch: (async (url, init) => { const parsed = new URL(String(url)); requests.push({ url: parsed, body: init?.body ? JSON.parse(String(init.body)) : undefined }); return Response.json(await response(parsed)); }) as typeof fetch });
  const client = new TelephonyClient(sdk.app("telephony", { projectId: "project", installId: 42 }), { authProvider: "auth" });
  let callbacks: ListenerAudioCallbacks | undefined;
  let stopped = 0;
  const runtime: ListenerAudioRuntime = { create(cb) { callbacks = cb; return { async start() { cb.onReady(); }, stop() { stopped++; }, setOutputVolume() {} }; } };
  return { client, requests, runtime, get callbacks() { return callbacks!; }, get stopped() { return stopped; }, setResponse(value: typeof response) { response = value; } };
}

describe("passive Telephony listener", () => {
  test("uses a separate authorized session and never answers, claims, or hangs up", async () => {
    const f = fixture(), listener = f.client.createCallListener({ runtime: f.runtime });
    await listener.listen("call");
    expect(listener.getSnapshot().state).toBe("listening");
    expect(f.requests[0].url.pathname).toEndWith("/user/softphone/listen/call");
    expect(f.requests[0].url.searchParams.get("auth_provider")).toBe("auth");
    await listener.stop();
    expect(f.requests.filter(r => r.url.pathname.endsWith("/listen-stop/call"))).toHaveLength(1);
    expect(f.stopped).toBe(1);
    expect(f.requests.some(r => /\/answer\/|\/attach\/|\/takeover\/|\/hangup/.test(r.url.pathname))).toBe(false);
    await listener.dispose();
  });
  test("listener and operator URLs cannot be interchanged or cross installations", async () => {
    const f = fixture();
    const session = await f.client.listenSession("call");
    expect(f.client.listenerMediaURL(session)).toBe("wss://gateway.example/api/apps/telephony/_install/42/softphone/listen-media/call/secret");
    expect(() => f.client.mediaURL(session)).toThrow();
    expect(() => f.client.listenerMediaURL({ ...session, media_url: session.media_url.replace("42", "99") })).toThrow();
    expect(() => f.client.listenerMediaURL({ ...session, media_url: "https://evil.example" + session.media_url })).toThrow();
  });
  test("revocation and caller termination are distinct and never reconnect", async () => {
    for (const reason of ["access_revoked", "call_ended"] as const) {
      const f = fixture(), listener = f.client.createCallListener({ runtime: f.runtime });
      await listener.listen("call"); f.callbacks.onClose(reason);
      expect(listener.getSnapshot().state).toBe(reason);
      expect(f.stopped).toBe(1);
      await listener.dispose();
    }
  });
  test("transient disconnect retries with a new listener credential", async () => {
    const f = fixture(), listener = f.client.createCallListener({ runtime: f.runtime });
    await listener.listen("call"); f.callbacks.onClose("media_disconnected");
    expect(listener.getSnapshot().state).toBe("reconnecting");
    await Bun.sleep(600);
    expect(listener.getSnapshot().state).toBe("listening");
    expect(f.requests.filter(r => r.url.pathname.endsWith("/listen/call"))).toHaveLength(2);
    await listener.dispose();
  });
  test("stop during delayed authorization releases the late credential without playing", async () => {
    const f = fixture();
    let resolve!: (value: unknown) => void;
    f.setResponse(url => url.pathname.endsWith("/listen/call") ? new Promise(r => { resolve = r; }) : { ok: true });
    const listener = f.client.createCallListener({ runtime: f.runtime });
    const pending = listener.listen("call");
    while (!resolve) await Bun.sleep(1);
    await listener.stop();
    resolve({ call_id: "call", media_url: "/api/apps/telephony/_install/42/softphone/listen-media/call/secret", session_token: "secret", lease_seconds: 60 });
    await pending;
    expect(listener.getSnapshot().state).toBe("idle");
    expect(f.requests.filter(r => r.url.pathname.endsWith("/listen-stop/call"))).toHaveLength(1);
    expect(f.stopped).toBe(0);
    await listener.dispose();
  });
  test("directional decoding preserves PCM amplitude and rejects malformed frames", () => {
    const frame = new ArrayBuffer(28), view = new DataView(frame);
    view.setUint32(0, 0x314c5441, true); view.setUint32(4, 1, true); view.setBigUint64(8, 4n, true); view.setBigUint64(16, 1000n, true);
    view.setInt16(24, 16384, true); view.setInt16(26, -16384, true);
    const result = decodeListenerFrame(frame);
    expect(result.direction).toBe(1); expect(result.sequence).toBe(4); expect(Array.from(result.frame)).toEqual([0.5, -0.5]);
    view.setUint32(4, 2, true); expect(() => decodeListenerFrame(frame)).toThrow();
    expect(() => decodeListenerFrame(new ArrayBuffer(24))).toThrow();
  });
});

describe('private coaching SDK',()=>{
 test('requires coaching mode, uses separate endpoints and never retargets on reconnect',async()=>{
  const f=fixture();f.setResponse(url=>url.pathname.endsWith('/coach/call')?{call_id:'call',media_url:'/api/apps/telephony/_install/42/softphone/listen-media/call/secret',session_token:'secret',lease_seconds:60,coaching:true}:{ok:true});
  const listener=f.client.createCallListener({runtime:f.runtime});
  await listener.listen('call').catch(()=>{});await expect(listener.startTalking()).rejects.toThrow();await listener.stop();
  await listener.coach('call');expect(listener.getSnapshot().coaching).toBe(true);expect(listener.getSnapshot().talking).toBe(false);
  f.callbacks.onClose('media_disconnected');expect(listener.getSnapshot().state).toBe('disconnected');
  await Bun.sleep(550);expect(f.requests.filter(r=>r.url.pathname.endsWith('/coach/call'))).toHaveLength(1);
  expect(f.requests.filter(r=>r.url.pathname.endsWith('/coach-stop/call'))).toHaveLength(1);await listener.dispose();
 });
 test('late coaching authorization is cancelled and its credential released',async()=>{
  const f=fixture();let resolve!:(v:any)=>void;
  f.setResponse(url=>url.pathname.endsWith('/coach/call')?new Promise(r=>{resolve=r}):{ok:true});
  const listener=f.client.createCallListener({runtime:f.runtime});const start=listener.coach('call');while(!resolve)await Bun.sleep(1);
  await listener.stop();resolve({call_id:'call',media_url:'/api/apps/telephony/_install/42/softphone/listen-media/call/secret',session_token:'secret',lease_seconds:60,coaching:true});await start;
  expect(f.stopped).toBe(0);expect(f.requests.some(r=>r.url.pathname.endsWith('/coach-stop/call'))).toBe(true);await listener.dispose();
 });
});
