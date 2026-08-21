import { describe, expect, test } from "bun:test";
import { resolveAudioOptions } from "./audio-settings";

describe("softphone audio setting migration", () => {
  test("uses the recommended profile for a new browser", () => {
    expect(resolveAudioOptions(null, null)).toEqual({
      echoCancellation: true,
      noiseSuppression: false,
      autoGainControl: true,
    });
  });

  test("migrates the v1 automatic-gain default to enabled", () => {
    expect(resolveAudioOptions(null, JSON.stringify({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: false,
    }))).toEqual({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: true,
    });
  });

  test("preserves an explicit v2 preference", () => {
    expect(resolveAudioOptions(JSON.stringify({ autoGainControl: false }), null).autoGainControl).toBe(false);
  });
});
