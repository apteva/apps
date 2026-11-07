import { expect, test } from "bun:test";
import { measureAudioSetup } from "./audio-setup";
import type { MediaSessionEvent } from "../frontend/src/media-lease";

test("setup measurements retain UTC boundaries and elapsed time without error details", async () => {
  const events: MediaSessionEvent[] = [];
  expect(await measureAudioSetup("audio_assets", event => events.push(event), async () => { await Bun.sleep(5); return 42; })).toBe(42);
  expect(events.map(event => event.outcome)).toEqual(["started", "completed"]);
  expect(events[1].duration_ms!).toBeGreaterThanOrEqual(3);
  expect(events.every(event => event.timestamp.endsWith("Z"))).toBe(true);
  await expect(measureAudioSetup("audio_assets", event => events.push(event), async () => { throw new Error("https://private/token-secret"); })).rejects.toThrow("token-secret");
  expect(events.at(-1)?.outcome).toBe("failed");
  expect(JSON.stringify(events)).not.toContain("token-secret");
});

test("throwing setup observers cannot fail audio", async () => {
  expect(await measureAudioSetup("audio_worklet", () => { throw new Error("observer"); }, async () => "ready")).toBe("ready");
});
