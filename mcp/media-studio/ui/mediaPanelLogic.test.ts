import { describe, expect, test } from "bun:test";
import {
  clampDuration,
  imageGenerationOptions,
  isDurableMediaReference,
  projectScopedStorageContentURL,
  selectedModelProvider,
  shouldClearSubmittedPrompt,
  shouldCommitScopedResponse,
  uploadValidationError,
  videoSourceRequired,
} from "./mediaPanelLogic";

describe("Media Panel logic", () => {
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

  test("rejects stale tab responses and preserves newer prompts", () => {
    expect(shouldCommitScopedResponse("image", "video")).toBe(false);
    expect(shouldClearSubmittedPrompt("image", "image", "old", "new")).toBe(false);
    expect(shouldClearSubmittedPrompt("image", "image", "same", "same")).toBe(true);
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
