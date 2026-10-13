import { afterEach, expect, spyOn, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { AUDIO_ASSET_TIMEOUT_MS, loadAudioAsset, loadAudioModules } from "../src/audio-assets";
import { createMicrophonePreview, createBrowserAudio } from "../src/audio";
import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, MicrophoneTestSession, SoftphoneSession } from "../../ui/softphone-audio";
import { WebRTCAudioConnection } from "../src/webrtc-audio";
import worklet from "../../ui/softphone-worklet.js" with { type: "text" };
import worker from "../../ui/softphone-worker.js" with { type: "text" };

const restore: Array<() => void> = [];
afterEach(() => { while (restore.length) restore.pop()!(); });
function fixture(baseURL = "https://host.example") {
  const prior = Object.getOwnPropertyDescriptor(globalThis, "location");
  Object.defineProperty(globalThis, "location", { configurable: true, value: new URL("https://host.example/apps/telephony/page") });
  restore.push(() => { if (prior) Object.defineProperty(globalThis, "location", prior); else Reflect.deleteProperty(globalThis, "location"); });
  const requests: Array<{url: URL; init?: RequestInit}> = [];
  let response = async (url: URL) => url.pathname.includes("/worklet-") ? worklet : worker;
  const sdk = new AptevaClient({ baseURL, accessToken: "test-user-token", fetch: (async (url, init) => {
    const parsed = new URL(String(url)); requests.push({ url: parsed, init });
    return new Response(await response(parsed), { headers: { "Content-Type": "text/javascript" } });
  }) as typeof fetch });
  return { app: sdk.app("telephony", { projectId: "project-one", installId: 42 }), requests, setResponse(fn: typeof response) { response = fn; } };
}

test("strict same-origin hosts use verified installation assets without token URLs", async () => {
  const f = fixture();
  const blob = spyOn(URL, "createObjectURL"); restore.push(() => blob.mockRestore());
  const modules = await loadAudioModules(f.app);
  expect(blob).not.toHaveBeenCalled();
  expect(modules.urls).toHaveLength(2);
  for (const value of modules.urls) {
    const url = new URL(value);
    expect(url.origin).toBe("https://host.example");
    expect(url.pathname).toMatch(/^\/api\/apps\/telephony\/_install\/42\/ui\/frontend\/(worklet|worker)-[a-f0-9]{64}\.js$/);
    expect([...url.searchParams]).toEqual([["project_id", "project-one"], ["install_id", "42"]]);
  }
  for (const {url, init} of f.requests) {
    expect(url.searchParams.get("project_id")).toBe("project-one");
    expect(url.searchParams.get("install_id")).toBe("42");
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer test-user-token");
    expect(init?.redirect).toBe("error");
    expect(init?.cache).toBe("force-cache");
  }
  modules.dispose();
});

test("simultaneous preparation coalesces and warm reconnects make no asset requests", async () => {
  const f = fixture();
  const modules = await Promise.all(Array.from({length: 64}, () => loadAudioModules(f.app)));
  expect(f.requests).toHaveLength(2);
  for (const module of modules) { expect(module.urls).toEqual(modules[0].urls); module.dispose(); }
  const warmed = await loadAudioModules(f.app);
  expect(f.requests).toHaveLength(2);
  warmed.dispose();
});

test("WebRTC preparation fetches one worklet and PCM subsequently adds only its worker", async () => {
  const f = fixture();
  (await loadAudioModules(f.app, true)).dispose();
  expect(f.requests).toHaveLength(1);
  expect(f.requests[0].url.pathname).toContain("/worklet-");
  (await loadAudioModules(f.app)).dispose();
  expect(f.requests).toHaveLength(2);
});

test("verification never crosses client handles, projects or installations", async () => {
  const one = fixture(), two = fixture();
  (await loadAudioModules(one.app)).dispose();
  two.setResponse(async () => "forged script");
  await expect(loadAudioModules(two.app)).rejects.toThrow("integrity mismatch");
  expect(two.requests.length).toBeGreaterThan(0);
  const other = new AptevaClient({baseURL:"https://host.example",accessToken:"other",fetch:(async () => new Response("forged")) as unknown as typeof fetch});
  for (const scope of [{projectId:"project-two",installId:42},{projectId:"project-one",installId:43}]) {
    await expect(loadAudioModules(other.app("telephony", scope))).rejects.toThrow("integrity mismatch");
  }
});

test("failed integrity verification is evicted and a corrected asset can retry", async () => {
  const f = fixture(); f.setResponse(async () => "forged");
  await expect(loadAudioModules(f.app, true)).rejects.toThrow("integrity mismatch");
  f.setResponse(async () => worklet);
  (await loadAudioModules(f.app, true)).dispose();
  expect(f.requests).toHaveLength(2);
});

test("hung preparation is bounded, aborts the lookup, and does not poison retries", async () => {
  const f = fixture(); f.setResponse(() => new Promise(() => {}));
  const timeout = globalThis.setTimeout;
  const timer = spyOn(globalThis, "setTimeout").mockImplementation(((fn: TimerHandler, ms?: number, ...args: any[]) => timeout(fn, ms === AUDIO_ASSET_TIMEOUT_MS ? 5 : ms, ...args)) as typeof setTimeout);
  restore.push(() => timer.mockRestore());
  await expect(loadAudioModules(f.app, true)).rejects.toThrow("timed out");
  expect(f.requests[0].init?.signal?.aborted).toBe(true);
  f.setResponse(async () => worklet);
  (await loadAudioModules(f.app, true)).dispose();
  expect(f.requests).toHaveLength(2);
});

test("coaching and the softphone share the capture worklet verification", async () => {
  const f = fixture();
  (await loadAudioAsset(f.app, "worklet")).dispose();
  (await loadAudioModules(f.app, true)).dispose();
  expect(f.requests).toHaveLength(1);
});

test("WebRTC runtime preparation and startup never fetch the PCM worker", async () => {
  const f = fixture(), events: any[] = [];
  const start = spyOn(WebRTCAudioConnection.prototype, "start").mockResolvedValue(); restore.push(() => start.mockRestore());
  const runtime = createBrowserAudio(f.app), options = {...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"webrtc" as const};
  await runtime.prepare!(options, event => events.push(event));
  const audio = runtime.create({}); await audio.start("wss://host.example/media", options); audio.stop();
  expect(f.requests).toHaveLength(1);
  expect(start.mock.calls[0][2]).toContain("/worklet-");
  expect(events.map(event => event.outcome)).toEqual(["started", "completed"]);
});

test("auto fallback reuses its worklet and verifies the PCM worker only when needed", async () => {
  const f = fixture();
  const rtc = spyOn(WebRTCAudioConnection.prototype, "start").mockRejectedValue(new Error("RTC unavailable")); restore.push(() => rtc.mockRestore());
  const pcm = spyOn(SoftphoneSession.prototype, "start").mockResolvedValue(); restore.push(() => pcm.mockRestore());
  const runtime = createBrowserAudio(f.app), options = {...DEFAULT_SOFTPHONE_AUDIO_OPTIONS,mediaTransport:"auto" as const};
  await runtime.prepare!(options);
  expect(f.requests).toHaveLength(1);
  const audio = runtime.create({}); await audio.start("wss://host.example/media", options); audio.stop();
  expect(f.requests).toHaveLength(2);
  expect(pcm).toHaveBeenCalledTimes(1);
});

test("asset preparation does not request microphone access", async () => {
  const f = fixture();
  await createBrowserAudio(f.app).prepare!(DEFAULT_SOFTPHONE_AUDIO_OPTIONS);
  expect(f.requests).toHaveLength(2);
});

test("changed asset bytes fail closed before worklet or microphone startup", async () => {
  const f = fixture(); f.setResponse(async () => "modified script");
  const start = spyOn(MicrophoneTestSession.prototype, "start"); restore.push(() => start.mockRestore());
  const preview = createMicrophonePreview(undefined, f.app);
  await expect(preview.start()).rejects.toThrow("integrity mismatch");
  expect(start).not.toHaveBeenCalled();
});

test("external hosts retain embedded modules without downloading cross-origin workers", async () => {
  const f = fixture("https://gateway.example");
  const revoke = spyOn(URL, "revokeObjectURL"); restore.push(() => revoke.mockRestore());
  const modules = await loadAudioModules(f.app);
  expect(modules.urls.every(url => url.startsWith("blob:"))).toBe(true);
  expect(f.requests).toHaveLength(0);
  modules.dispose(); modules.dispose();
  expect(revoke).toHaveBeenCalledTimes(2);
});

test("stopping during asset download prevents a late microphone preview", async () => {
  const f = fixture();
  let ready!: () => void;
  const requested = new Promise<void>(resolve => { ready = resolve; });
  let finish!: (source: string) => void;
  f.setResponse(() => { ready(); return new Promise(resolve => { finish = resolve; }); });
  const start = spyOn(MicrophoneTestSession.prototype, "start"); restore.push(() => start.mockRestore());
  const preview = createMicrophonePreview(undefined, f.app);
  const started = preview.start();
  await requested; await preview.stop(); finish(worklet);
  await expect(started).rejects.toThrow("cancelled");
  expect(start).not.toHaveBeenCalled();
});

test("stopping call audio during asset download prevents a late device or socket", async () => {
  const f = fixture();
  let finish!: () => void;
  const gate = new Promise<void>(resolve => { finish = resolve; });
  f.setResponse(async url => { await gate; return url.pathname.includes("/worklet-") ? worklet : worker; });
  const start = spyOn(SoftphoneSession.prototype, "start"); restore.push(() => start.mockRestore());
  const audio = createBrowserAudio(f.app).create({});
  const started = audio.start("wss://host.example/media", DEFAULT_SOFTPHONE_AUDIO_OPTIONS);
  audio.stop(); finish();
  await expect(started).rejects.toThrow("cancelled");
  expect(start).not.toHaveBeenCalled();
});
