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
      mediaTransport: "auto",
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
      mediaTransport: "auto",
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
      mediaTransport: "auto",
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

test("preserves the saved WebRTC FEC opt-out without changing existing defaults",()=>{
 expect(resolveAudioOptions(JSON.stringify({mediaTransport:"webrtc",webrtcFec:false}),null,null).webrtcFec).toBe(false);
 expect(resolveAudioOptions(null,null,null).webrtcFec).toBeUndefined();
});

for (const transport of ["webrtc", "websocket", "auto"] as const) {
  test(`preserves explicit ${transport} across all panel storage generations`, () => {
    for (let generation = 0; generation < 4; generation++) {
      const raw: (string | null)[] = [null, null, null, null];
      raw[generation] = JSON.stringify({mediaTransport: transport, inputGainDB: -3});
      expect(resolveAudioOptions(raw[0], raw[1], raw[2], raw[3])).toMatchObject({mediaTransport: transport, inputGainDB: -3});
    }
  });
}
test("existing implicit transport and corrupt preferences use the panel WebRTC default", () => {
  expect(resolveAudioOptions(JSON.stringify({noiseSuppression: true}), null, null)).toMatchObject({mediaTransport: "auto", noiseSuppression: true});
  expect(resolveAudioOptions("invalid", null, null).mediaTransport).toBe("auto");
});
