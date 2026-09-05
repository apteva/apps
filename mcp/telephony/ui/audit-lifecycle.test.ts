// Audit-only regression: deliberately fails on unmodified telephony/v0.3.7.
import { expect, test } from "bun:test";
import { SoftphoneSession } from "./softphone-audio";

test("audit: cancel during microphone permission must release a late stream", async () => {
  const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, "navigator");
  const originalAudioContext = Object.getOwnPropertyDescriptor(globalThis, "AudioContext");
  let resolvePermission!: (stream: unknown) => void;
  let stopped = 0;
  const permission = new Promise((resolve) => { resolvePermission = resolve; });
  const track = { stop() { stopped++; }, getSettings() { return {}; } };
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaDevices: { getUserMedia: () => permission } } });
  // If cancelled startup continues, simulate audio initialization being unavailable.
  Object.defineProperty(globalThis, "AudioContext", { configurable: true, value: class { constructor() { throw new Error("audio context unavailable after cancellation"); } } });
  try {
    const session = new SoftphoneSession();
    const startup = session.start("ws://unused", "worklet.js", "worker.js").catch(() => {});
    session.stop();
    resolvePermission({ getAudioTracks: () => [track], getTracks: () => [track] });
    await startup;
    session.stop();
    expect(stopped).toBe(1);
  } finally {
    if (originalNavigator) Object.defineProperty(globalThis, "navigator", originalNavigator);
    else Reflect.deleteProperty(globalThis, "navigator");
    if (originalAudioContext) Object.defineProperty(globalThis, "AudioContext", originalAudioContext);
    else Reflect.deleteProperty(globalThis, "AudioContext");
  }
});
