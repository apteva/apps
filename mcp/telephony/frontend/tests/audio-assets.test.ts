import { afterEach, expect, spyOn, test } from "bun:test";
import { AptevaClient } from "@apteva/web-sdk";
import { loadAudioModules } from "../src/audio-assets";
import { createMicrophonePreview, createBrowserAudio } from "../src/audio";
import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, MicrophoneTestSession, SoftphoneSession } from "../../ui/softphone-audio";
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
  }
  modules.dispose();
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
