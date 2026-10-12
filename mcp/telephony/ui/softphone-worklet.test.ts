import { beforeAll, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";

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
  // Execute the worklet as a browser AudioWorklet script. Importing the
  // .js file as an ES module here collides with the frontend tests' text
  // asset import in Bun's module cache when the whole suite runs together.
  // Reading the source also matches how the browser loads the worklet URL.
  vm.runInThisContext(readFileSync(new URL("./softphone-worklet.js", import.meta.url), "utf8"));
});

describe("softphone realtime worklet", () => {
  for (const nativeOutput of [false, true]) {
    test(`redundant unmute preserves opening speech (${nativeOutput ? "RTC" : "PCM"})`, () => {
      const Capture = processors.get("softphone-capture")!;
      const control = new Capture({processorOptions:{nativeOutput}});
      const repeated = new Capture({processorOptions:{nativeOutput}});
      for (let block = 0; block < 30; block++) {
        const input = Float32Array.from({length:128}, (_, i) => Math.sin((block * 128 + i) * .13) * .6);
        const expected = new Float32Array(128), actual = new Float32Array(128);
        repeated.port.onmessage?.({data:{type:"muted",value:false}});
        control.process([[input]], [[expected]]);
        repeated.process([[input]], [[actual]]);
        expect(actual).toEqual(expected);
      }
      expect(repeated.port.messages).toEqual(control.port.messages);
      if (!nativeOutput) expect(repeated.port.messages.filter(v => v instanceof Float32Array)).toHaveLength(8);

      // A real transition must still erase both partial PCM and DSP history.
      repeated.port.messages.length = 0;
      repeated.process([[new Float32Array(128).fill(.5)]], [[new Float32Array(128)]]);
      repeated.port.onmessage?.({data:{type:"muted",value:true}});
      for (let block = 0; block < 4; block++) {
        const output = new Float32Array(128);
        repeated.process([[new Float32Array(128).fill(.5)]], [[output]]);
        expect(output.every(v => v === 0)).toBe(true);
      }
      for (const frame of repeated.port.messages.filter(v => v instanceof Float32Array) as Float32Array[]) {
        expect(frame.every(v => v === 0)).toBe(true);
      }
      repeated.port.onmessage?.({data:{type:"muted",value:false}});
      const output = new Float32Array(128);
      repeated.process([[new Float32Array(128)]], [[output]]);
      expect(output.every(v => v === 0)).toBe(true);
    });
  }
  test("native RTC output preserves capture DSP and mute without PCM messages", () => {
    const Capture = processors.get("softphone-capture")!;
    const normal = new Capture({processorOptions:{inputGainDB:-3}});
    const native = new Capture({processorOptions:{nativeOutput:true,inputGainDB:-3}});
    const rendered:number[]=[];
    for(let block=0;block<12;block++) {
      const input=new Float32Array(128);for(let k=0;k<128;k++)input[k]=Math.sin((block*128+k)*.2)*1.2;
      const output=new Float32Array(128), silent=new Float32Array(128);
      native.process([[input]],[[output]]);normal.process([[input]],[[silent]]);
      rendered.push(...output);expect(silent.every(v=>v===0)).toBe(true);
    }
    const frames=normal.port.messages.filter(v=>v instanceof Float32Array) as Float32Array[];
    expect(rendered.slice(0,frames.length*480)).toEqual(frames.flatMap(f=>Array.from(f)));
    expect(native.port.messages.some(v=>v instanceof Float32Array)).toBe(false);
    native.port.onmessage?.({data:{type:"muted",value:true}});
    const output=new Float32Array(128);native.process([[new Float32Array(128).fill(1)]],[[output]]);
    expect(output.every(v=>v===0)).toBe(true);
  });
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
