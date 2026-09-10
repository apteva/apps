import { describe, expect, test } from "bun:test";
import { resolveAudioOptions } from "./audio-settings";

describe("softphone audio setting migration", () => {
  test("uses the recommended profile for a new browser", () => {
    expect(resolveAudioOptions(null, null, null)).toEqual({
      echoCancellation: true,
      noiseSuppression: false,
      autoGainControl: false,
      inputGainDB: 0,
      highpassFilter: true,
    });
  });

  test("migrates the v2 automatic-gain default to disabled", () => {
    expect(resolveAudioOptions(null, null, JSON.stringify({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: true,
    }))).toEqual({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: false,
      inputGainDB: 0,
      highpassFilter: true,
    });
  });

  test("keeps the v1 automatic-gain setting disabled", () => {
    expect(resolveAudioOptions(null, null, null, JSON.stringify({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: true,
    }))).toEqual({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: false,
      inputGainDB: 0,
      highpassFilter: true,
    });
  });

  test("preserves an explicit v3 preference while using unity gain by default", () => {
    const result = resolveAudioOptions(null, JSON.stringify({ autoGainControl: true }), null);
    expect(result.autoGainControl).toBe(true);
    expect(result.inputGainDB).toBe(0);
  });
});

 test("preserves a saved explicit microphone gain", () => {
  expect(resolveAudioOptions(JSON.stringify({ inputGainDB: -6 }), null, null).inputGainDB).toBe(-6);
 });
