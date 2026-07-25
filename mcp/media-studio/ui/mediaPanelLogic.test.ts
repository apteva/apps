import { describe, expect, test } from "bun:test";
import {
  clampDuration,
  buildVideoReferencePayload,
  DEFAULT_IMAGE_FORMAT,
  formatMediaTime,
  imageGenerationOptions,
  isReferenceToVideoModel,
  isDurableMediaReference,
  mergeHistoryPage,
  projectScopedStorageContentURL,
  selectedModelProvider,
  shouldClearSubmittedPrompt,
  shouldCommitScopedResponse,
  shouldReplaceVoiceSelection,
  shouldSendVideoAspect,
  ttsOutputFormats,
  ttsProviderUsesSeparateVoice,
  uploadValidationError,
  videoSourceRequired,
  videoReferenceImageLimit,
  voiceProviderSupportsCreation,
  voiceProviderSupportsPrompt,
  voicesForProvider,
  type VideoReferencePurpose,
} from "./mediaPanelLogic";

describe("Media Panel logic", () => {
  test("defaults image generation to JPEG", () => {
    expect(DEFAULT_IMAGE_FORMAT).toBe("jpeg");
  });

  test("uses the selected model provider instead of an aggregate provider label", () => {
    expect(selectedModelProvider("venice-ai", "venice-ai:gpt-image-2", "openai-api,venice-ai"))
      .toBe("venice-ai");
    expect(selectedModelProvider(undefined, "openai-api:gpt-image-2", "openai-api,venice-ai"))
      .toBe("openai-api");
  });

  test("preserves provider-specific image output options under multi-binding", () => {
    expect(imageGenerationOptions("venice-ai", "gpt-image-2", "high", "jpeg", false))
      .toEqual({ safe_mode: false, output_format: "jpeg" });
    expect(imageGenerationOptions("openai-api", "gpt-image-2", "high", "jpeg", true))
      .toEqual({ safe_mode: true, quality: "high", output_format: "jpeg" });
  });

  test("keeps duration values inside each media kind's contract", () => {
    expect(clampDuration("audio_sfx", 300)).toBe(30);
    expect(clampDuration("music", 0)).toBe(3);
    expect(clampDuration("video", 12)).toBe(12);
  });

  test("builds project-scoped Storage preview URLs", () => {
    expect(projectScopedStorageContentURL("12", "project a"))
      .toBe("/api/apps/storage/files/12/content?project_id=project%20a");
  });

  test("requires sources for image and reference video models", () => {
    expect(videoSourceRequired("image-to-video", "model")).toBe(true);
    expect(videoSourceRequired(undefined, "seedance-reference-to-video")).toBe(true);
    expect(videoSourceRequired("text-to-video", "model")).toBe(false);
  });

  test("builds provider-neutral reference groups for every R2V model", () => {
    const images = ["storage:1", "https://example.test/side.jpg"];
    expect(isReferenceToVideoModel("kling-o3-pro-reference-to-video")).toBe(true);
    expect(buildVideoReferencePayload(
      "kling-o3-pro-reference-to-video",
      "identity" satisfies VideoReferencePurpose,
      images,
    )).toEqual({
      reference_groups: [{ role: "identity", images }],
    });
    expect(buildVideoReferencePayload(
      "happyhorse-1-1-reference-to-video",
      "reference",
      images,
    )).toEqual({
      reference_groups: [{ role: "reference", images }],
    });
    expect(buildVideoReferencePayload("wan-2-7-image-to-video", "identity", images))
      .toEqual({ source_image: "storage:1" });
    expect(videoReferenceImageLimit("kling-o3-pro-reference-to-video", "identity")).toBe(4);
    expect(videoReferenceImageLimit("kling-o3-pro-reference-to-video", "reference")).toBe(7);
    expect(videoReferenceImageLimit("grok-imagine-reference-to-video-private")).toBe(7);
    expect(videoReferenceImageLimit("happyhorse-1-1-reference-to-video")).toBe(9);
  });

  test("only sends video aspect when live model metadata supports it", () => {
    expect(shouldSendVideoAspect(["16:9", "9:16"], true)).toBe(true);
    expect(shouldSendVideoAspect(undefined, true)).toBe(false);
    expect(shouldSendVideoAspect(undefined, false)).toBe(true);
  });

  test("treats Deepgram Aura models as voices", () => {
    expect(ttsProviderUsesSeparateVoice("deepgram")).toBe(false);
    expect(ttsProviderUsesSeparateVoice("elevenlabs")).toBe(true);
    expect(ttsProviderUsesSeparateVoice("fish-audio")).toBe(true);
  });

  test("exposes only provider-supported TTS output formats", () => {
    expect(ttsOutputFormats("deepgram")).toEqual(["mp3", "wav", "opus", "flac", "aac"]);
    expect(ttsOutputFormats("fish-audio")).toEqual(["mp3", "wav", "opus", "pcm"]);
    expect(ttsOutputFormats("cartesia")).toEqual(["mp3", "wav", "pcm"]);
    expect(ttsOutputFormats("minimax-audio")).toEqual(["mp3", "wav", "flac", "pcm"]);
    expect(ttsOutputFormats("elevenlabs")).toEqual([]);
  });

  test("only exposes prompt voice design for providers that support it", () => {
    expect(voiceProviderSupportsPrompt("elevenlabs")).toBe(true);
    expect(voiceProviderSupportsPrompt("minimax-audio")).toBe(true);
    expect(voiceProviderSupportsPrompt("fish-audio")).toBe(false);
    expect(voiceProviderSupportsPrompt("cartesia")).toBe(false);
  });

  test("exposes custom voice creation only for compatible providers", () => {
    expect(voiceProviderSupportsCreation("elevenlabs")).toBe(true);
    expect(voiceProviderSupportsCreation("fish-audio")).toBe(true);
    expect(voiceProviderSupportsCreation("cartesia")).toBe(true);
    expect(voiceProviderSupportsCreation("minimax-audio")).toBe(true);
    expect(voiceProviderSupportsCreation("deepgram")).toBe(false);
    expect(
      ["deepgram", "fish-audio"].filter(voiceProviderSupportsCreation),
    ).toEqual(["fish-audio"]);
  });

  test("only exposes voices from the selected provider", () => {
    const voices = [
      { id: "elevenlabs:voice-1", provider: "elevenlabs" },
      { id: "fish-audio:voice-2", provider: "fish-audio" },
      { id: "fish-audio:voice-3" },
    ];
    expect(voicesForProvider("fish-audio", voices).map((voice) => voice.id)).toEqual([
      "fish-audio:voice-2",
      "fish-audio:voice-3",
    ]);
    expect(voicesForProvider("elevenlabs", voices).map((voice) => voice.id)).toEqual([
      "elevenlabs:voice-1",
    ]);
  });

  test("preserves tracked voices that are not active in the provider catalog yet", () => {
    expect(shouldReplaceVoiceSelection(
      "minimax-audio:clone-1",
      ["minimax-audio:system-1"],
      ["minimax-audio:clone-1"],
    )).toBe(false);
    expect(shouldReplaceVoiceSelection(
      "minimax-audio:missing",
      ["minimax-audio:system-1"],
      [],
    )).toBe(true);
    expect(shouldReplaceVoiceSelection(
      "elevenlabs:stale",
      [],
      [],
    )).toBe(true);
    expect(shouldReplaceVoiceSelection(
      "",
      [],
      ["fish-audio:clone-1"],
    )).toBe(true);
  });

  test("formats player time without invalid or shifting values", () => {
    expect(formatMediaTime(Number.NaN)).toBe("0:00");
    expect(formatMediaTime(0)).toBe("0:00");
    expect(formatMediaTime(65.9)).toBe("1:05");
    expect(formatMediaTime(3661)).toBe("1:01:01");
  });

  test("rejects stale tab responses and preserves newer prompts", () => {
    expect(shouldCommitScopedResponse("image", "video")).toBe(false);
    expect(shouldClearSubmittedPrompt("image", "image", "old", "new")).toBe(false);
    expect(shouldClearSubmittedPrompt("image", "image", "same", "same")).toBe(true);
  });

  test("appends cursor pages without reordering or duplicating generations", () => {
    const first = [{ id: 5 }, { id: 4 }, { id: 3 }];
    const next = [{ id: 3 }, { id: 2 }, { id: 1 }];
    expect(mergeHistoryPage(first, next, true).map((item) => item.id)).toEqual([5, 4, 3, 2, 1]);
    expect(mergeHistoryPage(first, next, false).map((item) => item.id)).toEqual([3, 2, 1]);
  });

  test("only persists durable source references in drafts", () => {
    expect(isDurableMediaReference("storage:12")).toBe(true);
    expect(isDurableMediaReference("https://example.test/source.jpg")).toBe(true);
    expect(isDurableMediaReference("data:image/jpeg;base64,/9j/")).toBe(false);
  });

  test("validates upload type and size", () => {
    expect(uploadValidationError({ size: 1, type: "text/plain" }, "image")).toContain("valid image");
    expect(uploadValidationError({ size: 26 * 1024 * 1024, type: "audio/wav" }, "audio")).toContain("25 MB");
    expect(uploadValidationError({ size: 1024, type: "image/jpeg" }, "image")).toBe("");
  });
});
