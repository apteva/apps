import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";

function fixture(stereo = false) {
  let Processor: any;
  const context = { sampleRate: 24_000, currentTime: 0, Float32Array, AudioWorkletProcessor: class { port = { onmessage: undefined as any, messages: [] as any[], postMessage(value: any) { this.messages.push(value); } }; }, registerProcessor(_name: string, ctor: any) { Processor = ctor; } };
  vm.runInNewContext(readFileSync(new URL("./listener-worklet.js", import.meta.url), "utf8"), context);
  return { context, processor: new Processor({ processorOptions: { stereo } }) };
}
test("listener renders both directions on one clock with unchanged stereo samples", () => {
  const { context, processor } = fixture(true);
  const a = Float32Array.from({ length: 480 }, (_, i) => Math.sin(i * 0.15) * 0.8);
  const b = Float32Array.from(a, x => -x || 0);
  for (const [direction, frame] of [[0, a], [1, b]] as const) processor.port.onmessage({ data: { direction, frame, playAtMS: 60 } });
  const out = [new Float32Array(480), new Float32Array(480)];
  processor.process([], [out]); expect(out[0].every(x => x === 0)).toBe(true);
  context.currentTime = 0.06; processor.process([], [out]);
  expect(Array.from(out[0])).toEqual(Array.from(a)); expect(Array.from(out[1])).toEqual(Array.from(b));
});
test("mixed playback avoids clipping while preserving simultaneous conversation", () => {
  const { context, processor } = fixture();
  for (let direction = 0; direction < 2; direction++) processor.port.onmessage({ data: { direction, frame: new Float32Array(480).fill(0.8), playAtMS: 60 } });
  context.currentTime = 0.06; const out = [new Float32Array(480)]; processor.process([], [out]);
  expect(out[0][0]).toBeCloseTo(0.8, 6); expect(out[0].every(x => x <= 1)).toBe(true);
});
test("stale audio, excessive scheduling and bursts cannot accumulate listener delay", () => {
  const { context, processor } = fixture();
  context.currentTime = 1;
  processor.port.onmessage({ data: { direction: 0, frame: new Float32Array(480), playAtMS: 0 } });
  processor.port.onmessage({ data: { direction: 1, frame: new Float32Array(480), playAtMS: 5000 } });
  expect(processor.queues[0]).toHaveLength(0); expect(processor.queues[1]).toHaveLength(0);
  for (let i = 0; i < 100; i++) processor.port.onmessage({ data: { direction: 0, frame: new Float32Array(480), playAtMS: 1060 } });
  expect(processor.queues[0].length).toBeLessThanOrEqual(8);
  expect(processor.maxQueueMS).toBeLessThanOrEqual(160);
  expect(processor.dropped[0]).toBeGreaterThan(0);
});

test("moderate carrier bursts with identical timestamps preserve every sample in order", () => {
  const { context, processor } = fixture(true);
  const frames = Array.from({ length: 4 }, (_, packet) => Float32Array.from({ length: 480 }, (_, i) => Math.sin((packet * 480 + i) * 0.15) * 0.8));
  for (let direction = 0; direction < 2; direction++) for (const frame of frames) processor.port.onmessage({ data: { direction, frame, playAtMS: 60 } });
  const actual = [[], []] as number[][];
  for (let packet = 0; packet < 4; packet++) {
    context.currentTime = 0.06 + packet * 0.02;
    const out = [new Float32Array(480), new Float32Array(480)];
    processor.process([], [out]); out.forEach((channel, d) => actual[d].push(...channel));
  }
  const expected = frames.flatMap(frame => Array.from(frame));
  expect(actual[0]).toEqual(expected); expect(actual[1]).toEqual(expected);
  expect(processor.dropped).toEqual([0, 0]);
});
