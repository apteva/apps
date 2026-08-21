import { beforeAll, describe, expect, test } from "bun:test";

type WorkletCtor = new (options?: { processorOptions?: Record<string, unknown> }) => {
  port: MockPort;
  process: (inputs: Float32Array[][], outputs: Float32Array[][]) => boolean;
  reportStats?: () => void;
};

class MockPort {
  onmessage: ((event: { data: unknown }) => void) | null = null;
  messages: unknown[] = [];
  postMessage(value: unknown) { this.messages.push(value); }
}

const processors = new Map<string, WorkletCtor>();

beforeAll(async () => {
  const globals = globalThis as typeof globalThis & Record<string, unknown>;
  globals.sampleRate = 24_000;
  globals.currentTime = 0;
  globals.AudioWorkletProcessor = class { port = new MockPort(); };
  globals.registerProcessor = (name: string, ctor: WorkletCtor) => processors.set(name, ctor);
  await import("./softphone-worklet.js");
});

describe("softphone realtime worklet", () => {
  test("adds microphone headroom and caps the processed carrier signal", () => {
    const Capture = processors.get("softphone-capture");
    expect(Capture).toBeDefined();
    const processor = new Capture!({ processorOptions: { inputGainDB: -6, highpassFilter: true } });
    for (let block = 0; block < 8; block += 1) {
      const input = new Float32Array(128);
      for (let index = 0; index < input.length; index += 1) input[index] = index % 2 === 0 ? 1 : -1;
      processor.process([[input]], [[new Float32Array(128)]]);
    }
    const frame = processor.port.messages.find((value) => value instanceof Float32Array) as Float32Array;
    expect(frame).toBeInstanceOf(Float32Array);
    let peak = 0;
    for (const sample of frame) peak = Math.max(peak, Math.abs(sample));
    expect(peak).toBeGreaterThan(0.3);
    expect(peak).toBeLessThanOrEqual(10 ** (-3 / 20) + 1e-6);
  });

  test("bounds playback backlog and emits an exact directional drop record", () => {
    const Playback = processors.get("softphone-playback");
    expect(Playback).toBeDefined();
    const processor = new Playback!({ processorOptions: { initialTargetMs: 80, minTargetMs: 60, maxTargetMs: 160, hardMaxMs: 320 } });
    for (let sequence = 0; sequence < 25; sequence += 1) {
      processor.port.onmessage?.({ data: { frame: new Float32Array(480), sequence, timestamp_ms: sequence * 20 } });
    }
    processor.reportStats?.();
    const stats = processor.port.messages.at(-1) as {
      queue_ms: number;
      drop_events: Array<{ timestamp: string; direction: string; reason: string; duration_ms: number; queue_before_ms: number; queue_after_ms: number }>;
    };
    expect(stats.queue_ms).toBeLessThanOrEqual(320);
    expect(stats.drop_events.length).toBeGreaterThan(0);
    const drop = stats.drop_events.at(-1)!;
    expect(drop.direction).toBe("carrier_to_operator");
    expect(drop.reason).toBe("playback_hard_limit");
    expect(drop.timestamp.length).toBeGreaterThan(10);
    expect(drop.duration_ms).toBeGreaterThan(0);
    expect(drop.queue_before_ms).toBeGreaterThan(drop.queue_after_ms);
    expect(drop.queue_after_ms).toBeLessThanOrEqual(160);
  });
});
