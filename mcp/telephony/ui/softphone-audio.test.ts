import { describe, expect, test } from "bun:test";
import { pcm16WAV } from "./softphone-audio";

describe("local microphone WAV", () => {
  test("writes mono PCM16 with an exact duration and payload", async () => {
    const samples = new Int16Array([0, 1234, -2345, 32767, -32768]);
    const wav = pcm16WAV([samples.subarray(0, 2), samples.subarray(2)], samples.length, 24000);
    const bytes = new Uint8Array(await wav.arrayBuffer());
    const view = new DataView(bytes.buffer);

    expect(wav.type).toBe("audio/wav");
    expect(new TextDecoder().decode(bytes.subarray(0, 4))).toBe("RIFF");
    expect(new TextDecoder().decode(bytes.subarray(8, 12))).toBe("WAVE");
    expect(view.getUint16(22, true)).toBe(1);
    expect(view.getUint32(24, true)).toBe(24000);
    expect(view.getUint16(34, true)).toBe(16);
    expect(view.getUint32(40, true)).toBe(samples.length * 2);
    expect(Array.from({ length: samples.length }, (_, index) => view.getInt16(44 + index * 2, true))).toEqual(Array.from(samples));
  });
});
