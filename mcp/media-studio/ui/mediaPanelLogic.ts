export type DurationKind = "video" | "audio_sfx" | "music";

export const DEFAULT_IMAGE_FORMAT = "jpeg";
export const MAX_IMAGE_UPLOAD_BYTES = 20 * 1024 * 1024;
export const MAX_AUDIO_UPLOAD_BYTES = 25 * 1024 * 1024;

const DURATION_BOUNDS: Record<DurationKind, { min: number; max: number }> = {
  video: { min: 1, max: 60 },
  audio_sfx: { min: 1, max: 30 },
  music: { min: 3, max: 300 },
};

export function clampDuration(kind: DurationKind, value: number): number {
  const bounds = DURATION_BOUNDS[kind];
  if (!Number.isFinite(value)) return bounds.min;
  return Math.min(bounds.max, Math.max(bounds.min, value));
}

export function providerFromQualifiedId(value: string): string {
  const separator = value.indexOf(":");
  return separator > 0 ? value.slice(0, separator) : "";
}

export function selectedModelProvider(
  modelProvider: string | undefined,
  modelID: string,
  aggregateProvider: string,
): string {
  return modelProvider || providerFromQualifiedId(modelID) || aggregateProvider;
}

export function imageGenerationOptions(
  provider: string,
  model: string,
  quality: string,
  outputFormat: string,
  safeMode: boolean,
): Record<string, unknown> {
  const options: Record<string, unknown> = { safe_mode: safeMode };
  if (!provider || provider === "openai-api" || provider === "openai-codex") {
    if (model !== "dall-e-2") options.quality = quality;
    if (model.startsWith("gpt-image")) options.output_format = outputFormat;
  } else if (provider === "venice-ai") {
    options.output_format = outputFormat;
  }
  return options;
}

export function projectScopedStorageContentURL(id: string, projectID: string): string {
  return `/api/apps/storage/files/${encodeURIComponent(id)}/content?project_id=${encodeURIComponent(projectID)}`;
}

export function videoSourceRequired(modelType: string | undefined, modelID: string): boolean {
  const normalizedType = String(modelType || "").toLowerCase();
  const normalizedID = modelID.toLowerCase();
  return normalizedType === "image-to-video" ||
    normalizedID.includes("image-to-video") ||
    normalizedID.includes("reference-to-video");
}

export function shouldSendVideoAspect(
  aspectRatios: string[] | undefined,
  hasModelMetadata: boolean,
): boolean {
  return !hasModelMetadata || !!aspectRatios?.length;
}

export function ttsProviderUsesSeparateVoice(provider: string): boolean {
  return provider !== "deepgram";
}

export function ttsOutputFormats(provider: string): string[] {
  if (provider === "deepgram") return ["mp3", "wav", "opus", "flac", "aac"];
  if (provider === "fish-audio") return ["mp3", "wav", "opus", "pcm"];
  if (provider === "cartesia") return ["mp3", "wav", "pcm"];
  if (provider === "minimax-audio") return ["mp3", "wav", "flac", "pcm"];
  return [];
}

export function voiceProviderSupportsPrompt(provider: string): boolean {
  return provider === "elevenlabs" || provider === "minimax-audio";
}

export function voiceProviderSupportsCreation(provider: string): boolean {
  return provider === "elevenlabs" ||
    provider === "fish-audio" ||
    provider === "cartesia" ||
    provider === "minimax-audio";
}

export function modelForVoiceProvider(
  provider: string,
  models: Array<{ id: string; provider?: string }>,
): string {
  return models.find((model) =>
    (model.provider || providerFromQualifiedId(model.id)) === provider
  )?.id || "";
}

export function shouldReplaceVoiceSelection(
  currentVoice: string,
  catalogVoiceIDs: string[],
  trackedVoiceIDs: string[],
): boolean {
  const availableVoiceIDs = [...catalogVoiceIDs, ...trackedVoiceIDs];
  return !availableVoiceIDs.includes(currentVoice) &&
    (!!currentVoice || availableVoiceIDs.length > 0);
}

export function formatMediaTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "0:00";
  const total = Math.floor(seconds);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const remainingSeconds = total % 60;
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(remainingSeconds).padStart(2, "0")}`;
  }
  return `${minutes}:${String(remainingSeconds).padStart(2, "0")}`;
}

export function shouldCommitScopedResponse(requestKind: string, activeKind: string): boolean {
  return requestKind === activeKind;
}

export function mergeHistoryPage<T extends { id: number }>(
  current: T[],
  incoming: T[],
  append: boolean,
): T[] {
  if (!append) return incoming;
  const seen = new Set(current.map((item) => item.id));
  return [...current, ...incoming.filter((item) => !seen.has(item.id))];
}

export function isDurableMediaReference(value: string): boolean {
  const normalized = value.trim().toLowerCase();
  return normalized.startsWith("storage:") ||
    normalized.startsWith("http://") ||
    normalized.startsWith("https://");
}

export function shouldClearSubmittedPrompt(
  requestKind: string,
  activeKind: string,
  submittedPrompt: string,
  currentPrompt: string,
): boolean {
  return requestKind === activeKind && submittedPrompt === currentPrompt;
}

export function uploadValidationError(
  file: { size: number; type: string },
  mediaType: "image" | "audio",
): string {
  const expectedPrefix = `${mediaType}/`;
  if (!file.type.startsWith(expectedPrefix)) {
    return `Choose a valid ${mediaType} file.`;
  }
  const limit = mediaType === "image" ? MAX_IMAGE_UPLOAD_BYTES : MAX_AUDIO_UPLOAD_BYTES;
  if (file.size > limit) {
    return `${mediaType === "image" ? "Image" : "Audio"} files must be ${limit / (1024 * 1024)} MB or smaller.`;
  }
  return "";
}
