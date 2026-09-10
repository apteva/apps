import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { DEFAULT_SOFTPHONE_AUDIO_OPTIONS, microphoneConstraints, playbackBufferOptions } from "./softphone-audio";

function processors() {
  const ctors: Record<string, any> = {};
  const context = vm.createContext({ sampleRate: 24000, currentTime: 0,
    AudioWorkletProcessor: class { port = { messages: [] as any[], postMessage(x: any) { this.messages.push(x); } }; },
    registerProcessor(name: string, ctor: any) { ctors[name] = ctor; },
  });
  vm.runInContext(readFileSync(new URL("./softphone-worklet.js", import.meta.url), "utf8"), context);
  return ctors;
}

function capture(gain: number, amplitude: number) {
  const C = processors()["softphone-capture"];
  const p = new C({ processorOptions: { inputGainDB: gain, highpassFilter: true } });
  for (let block = 0; block < 200; block++) {
    const input = Float32Array.from({ length: 128 }, (_, i) => amplitude * Math.sin(2 * Math.PI * 440 * (block * 128 + i) / 24000));
    p.process([[input]], [[new Float32Array(128)]]);
  }
  const samples = p.port.messages.filter((m: any) => m.length).slice(2).flatMap((m: any) => Array.from(m)) as number[];
  return { rms: Math.sqrt(samples.reduce((sum, v) => sum + v * v, 0) / samples.length), peak: Math.max(...samples.map(Math.abs)) };
}

test("unity microphone gain preserves quiet speech while retaining echo cancellation and limiter", () => {
  expect(DEFAULT_SOFTPHONE_AUDIO_OPTIONS.inputGainDB).toBe(0);
  expect(microphoneConstraints(DEFAULT_SOFTPHONE_AUDIO_OPTIONS).echoCancellation).toBe(true);
  const quiet0 = capture(0, 0.01), quietMinus6 = capture(-6, 0.01);
  expect(20 * Math.log10(quiet0.rms / quietMinus6.rms)).toBeCloseTo(6, 2);
  expect(capture(0, 1).peak).toBeLessThanOrEqual(10 ** (-3 / 20) + 1e-6);
});

// Deterministic 20 ms PCM packets through the real playback worklet, rendered
// in browser-sized 128-sample quanta. A delayed packet blocks later packets,
// matching ordered WebSocket delivery rather than simulating UDP reordering.
function playback(target: number, stallMS: number) {
  const C = processors()["softphone-playback"];
  const p = new C({ processorOptions: { initialTargetMs: target, minTargetMs: target } });
  let next = 0, firstMS = -1, peakTarget = target;
  for (let samples = 0; samples < 24000 * 10; samples += 128) {
    const now = samples / 24;
    while (next * 20 <= now && !(next >= 100 && next < 105 && now < 2000 + stallMS)) {
      p.handleMessage({ frame: new Float32Array(480).fill(0.1), sequence: next++ });
    }
    const out = new Float32Array(128);
    p.process([], [[out]]);
    if (firstMS < 0 && out.some(x => x !== 0)) firstMS = now;
    peakTarget = Math.max(peakTarget, p.targetMs);
  }
  return { target, stallMS, firstMS, underruns: p.underruns, peakTarget, droppedMS: p.droppedSamples / 24 };
}

test("compare 40/60/80 ms playback and retain adaptive recovery after a delivery stall", () => {
  const steady = [40, 60, 80].map(target => playback(target, 0));
  const moderate = [40, 60, 80].map(target => playback(target, 40));
  const stalled = [40, 60, 80].map(target => playback(target, 100));
  console.log("Playback simulation (local worklet, not end-to-end latency):", JSON.stringify({ steady, moderate, stalled }));
  for (const result of steady) { expect(result.underruns).toBe(0); expect(result.droppedMS).toBe(0); }
  expect(steady[2].firstMS - steady[1].firstMS).toBeGreaterThan(15);
  expect(steady[1].firstMS - steady[0].firstMS).toBeGreaterThan(15);
  expect(moderate[0].underruns).toBeGreaterThan(0);
  expect(moderate[1].underruns).toBe(0);
  for (const result of stalled) { expect(result.peakTarget).toBeGreaterThan(result.target); expect(result.peakTarget).toBeLessThanOrEqual(160); }
});

test("playback options support safe A/B profiles and reject invalid bounds", () => {
  expect(playbackBufferOptions({})).toEqual({ initialTargetMs: 60, minTargetMs: 60, maxTargetMs: 160, hardMaxMs: 320 });
  expect(playbackBufferOptions({ playbackTargetMs: 40 }).minTargetMs).toBe(40);
  expect(playbackBufferOptions({ playbackTargetMs: 80, playbackMinMs: 40, playbackMaxMs: 120 })).toEqual({ initialTargetMs: 80, minTargetMs: 40, maxTargetMs: 120, hardMaxMs: 320 });
  for (const value of [NaN, Infinity, 0, 39, 161]) expect(() => playbackBufferOptions({ playbackTargetMs: value })).toThrow();
  expect(() => playbackBufferOptions({ playbackTargetMs: 60, playbackMinMs: 80 })).toThrow();
  expect(() => playbackBufferOptions({ playbackTargetMs: 80, playbackMaxMs: 60 })).toThrow();
});
