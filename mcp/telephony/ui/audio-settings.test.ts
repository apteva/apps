import { describe, expect, test } from "bun:test";
import { resolveAudioOptions } from "./audio-settings";

describe("softphone audio setting migration", () => {
  test("uses the recommended profile for a new browser", () => {
    expect(resolveAudioOptions(null, null, null)).toEqual({
      echoCancellation: true,
      noiseSuppression: false,
      autoGainControl: false,
    });
  });

  test("migrates the v2 automatic-gain default to disabled", () => {
    expect(resolveAudioOptions(null, JSON.stringify({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: true,
    }), null)).toEqual({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: false,
    });
  });

  test("keeps the v1 automatic-gain setting disabled", () => {
    expect(resolveAudioOptions(null, null, JSON.stringify({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: false,
    }))).toEqual({
      echoCancellation: false,
      noiseSuppression: true,
      autoGainControl: false,
    });
  });

  test("preserves an explicit v3 preference", () => {
    expect(resolveAudioOptions(JSON.stringify({ autoGainControl: true }), null, null).autoGainControl).toBe(true);
  });
});
