import { describe, expect, test } from "bun:test";
import {
  formatGenerationCost,
  formatGenerationDuration,
  generationMediaURLs,
  isAudioKind,
  isTerminalJobStatus,
  isVideoKind,
  mediaKindLabel,
  shouldRefreshGenerationCard,
} from "./generationCardLogic";

describe("generation card logic", () => {
  test("prefers durable media URLs and removes duplicates", () => {
    expect(generationMediaURLs({
      storage_urls: ["/storage/1", "/storage/1"],
      local_cache_url: "/cache/1",
      upstream_urls: ["https://upstream.test/1"],
    })).toEqual(["/storage/1", "/cache/1", "https://upstream.test/1"]);

    expect(generationMediaURLs({ upstream_urls: ["javascript:alert(1)"] })).toEqual([]);
  });

  test("classifies media kinds", () => {
    expect(isVideoKind("avatar")).toBe(true);
    expect(isAudioKind("audio_tts")).toBe(true);
    expect(mediaKindLabel("music")).toBe("Generated music");
    expect(mediaKindLabel("unknown")).toBe("Generated media");
  });

  test("formats compact metadata", () => {
    expect(formatGenerationCost(0.0042)).toBe("$0.0042");
    expect(formatGenerationCost(0.55)).toBe("$0.55");
    expect(formatGenerationDuration(65)).toBe("1:05");
    expect(formatGenerationDuration(8)).toBe("8s");
  });

  test("matches only relevant generation events", () => {
    expect(shouldRefreshGenerationCard(
      { topic: "media.generated", data: { job_id: 12, generation_id: 42 } },
      42,
      undefined,
    )).toBe(true);
    expect(shouldRefreshGenerationCard(
      { topic: "media.generated", data: { job_id: 12 } },
      undefined,
      12,
    )).toBe(true);
    expect(shouldRefreshGenerationCard(
      { topic: "media.generated", data: { job_id: 99 } },
      undefined,
      12,
    )).toBe(false);
    expect(isTerminalJobStatus("failed")).toBe(true);
    expect(isTerminalJobStatus("polling")).toBe(false);
  });
});
