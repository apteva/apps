export type DurationKind = "video" | "audio_sfx" | "music";
export type VideoReferencePurpose = "identity" | "reference";
export interface VideoPromptReference {
  token: string;
  label: string;
}

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

export function isReferenceToVideoModel(modelID: string): boolean {
  return modelID.toLowerCase().includes("reference-to-video");
}

export function videoReferenceImageLimit(
  modelID: string,
  purpose: VideoReferencePurpose = "reference",
): number {
  const normalized = modelID.toLowerCase();
  if (normalized.startsWith("kling-")) {
    return purpose === "identity" ? 4 : 7;
  }
  if (normalized.startsWith("grok-imagine-")) {
    return 7;
  }
  return 9;
}

export function videoPromptReferences(
  modelID: string,
  purpose: VideoReferencePurpose,
  imageCount: number,
): VideoPromptReference[] {
  if (!isReferenceToVideoModel(modelID) || imageCount <= 0) return [];
  const normalized = modelID.toLowerCase();
  if (normalized.startsWith("kling-") && purpose === "identity") {
    return [{ token: "@Element1", label: "Subject" }];
  }
  return Array.from({ length: imageCount }, (_, index) => {
    const number = index + 1;
    if (normalized.startsWith("happyhorse-1-0-")) {
      return { token: `character${number}`, label: `Reference ${number}` };
    }
    if (normalized.startsWith("happyhorse-")) {
      return { token: `[Image ${number}]`, label: `Reference ${number}` };
    }
    return {
      token: `@Image${number}`,
      label: purpose === "identity" ? `Identity photo ${number}` : `Reference ${number}`,
    };
  });
}

export function insertPromptToken(
  prompt: string,
  token: string,
  selectionStart: number,
  selectionEnd: number,
): { value: string; cursor: number } {
  const start = Math.max(0, Math.min(selectionStart, prompt.length));
  const end = Math.max(start, Math.min(selectionEnd, prompt.length));
  const before = prompt.slice(0, start);
  const after = prompt.slice(end);
  const prefix = before.length > 0 && !/\s$/.test(before) ? " " : "";
  const suffix = after.length > 0 && !/^\s/.test(after) ? " " : "";
  const inserted = prefix + token + suffix;
  return {
    value: before + inserted + after,
    cursor: before.length + prefix.length + token.length,
  };
}

export function buildVideoReferencePayload(
  modelID: string,
  purpose: VideoReferencePurpose,
  images: string[],
): Record<string, unknown> {
  if (images.length === 0) return {};
  if (isReferenceToVideoModel(modelID)) {
    return {
      reference_groups: [{
        role: purpose,
        images,
      }],
    };
  }
  return { source_image: images[0] };
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

export function voicesForProvider<T extends { id: string; provider?: string }>(
  provider: string,
  voices: T[],
): T[] {
  return voices.filter((voice) =>
    (voice.provider || providerFromQualifiedId(voice.id) || provider) === provider
  );
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

// --- HeyGen avatar engines ---
//
// Engine choices come from the selected look's supported_api_engines, so
// new HeyGen engine versions show up without a panel change. The profile
// table only adds labels, resolution limits and motion controls for
// engines we know; unknown engines fall back to permissive defaults and
// HeyGen validates them. Keep in sync with heygenEngineProfiles in
// avatar.go.

export interface HeyGenEngineProfile {
  label: string;
  resolutions?: string[];
  motionPrompt: boolean;
  maxMotionPromptChars?: number;
  /** "top" = expressiveness high/medium/low, "engine" = motion_expressiveness default/less. */
  expressiveness: "top" | "engine" | "";
  requiresAdvertised: boolean;
}

export const HEYGEN_ENGINE_PROFILES: Record<string, HeyGenEngineProfile> = {
  "heygen-avatar-6": {
    label: "Avatar 6",
    resolutions: ["1080p", "2k"],
    motionPrompt: true,
    maxMotionPromptChars: 1200,
    expressiveness: "engine",
    requiresAdvertised: true,
  },
  "heygen-avatar-6-turbo": {
    label: "Avatar 6 Turbo",
    resolutions: ["1080p", "2k"],
    motionPrompt: true,
    maxMotionPromptChars: 1200,
    expressiveness: "engine",
    requiresAdvertised: true,
  },
  avatar_v: { label: "Avatar V", motionPrompt: false, expressiveness: "", requiresAdvertised: true },
  avatar_iv: { label: "Avatar IV", motionPrompt: true, expressiveness: "top", requiresAdvertised: false },
  avatar_iii: { label: "Avatar III", motionPrompt: false, expressiveness: "", requiresAdvertised: false },
};

export const HEYGEN_ALL_RESOLUTIONS = ["1080p", "720p", "2k", "4k"];
const HEYGEN_CLASSIC_RESOLUTIONS = ["1080p", "720p", "4k"];

export function heygenEngineLabel(engine: string): string {
  if (engine === "auto") return "Auto";
  return HEYGEN_ENGINE_PROFILES[engine]?.label || engine;
}

/** Engine ids offered for a look, always starting with "auto". */
export function heygenEngineChoices(look?: { supported_api_engines?: string[] }): string[] {
  const out = ["auto"];
  const advertised = look?.supported_api_engines || [];
  if (look && advertised.length > 0) {
    for (const e of advertised) if (e && !out.includes(e)) out.push(e);
    return out;
  }
  for (const [id, profile] of Object.entries(HEYGEN_ENGINE_PROFILES)) {
    // Looks that advertise nothing can't use engines HeyGen gates on the list.
    if (look && profile.requiresAdvertised) continue;
    out.push(id);
  }
  return out;
}

export function heygenEngineResolutions(engine: string): string[] {
  if (engine === "auto") return HEYGEN_CLASSIC_RESOLUTIONS;
  const profile = HEYGEN_ENGINE_PROFILES[engine];
  if (!profile) return HEYGEN_ALL_RESOLUTIONS;
  return profile.resolutions || HEYGEN_CLASSIC_RESOLUTIONS;
}

export function heygenResolutionLabel(resolution: string): string {
  return resolution === "2k" || resolution === "4k" ? resolution.toUpperCase() : resolution;
}

/** Motion controls for an engine. Auto hides them; unknown engines show both generically. */
export function heygenEngineMotion(engine: string): {
  prompt: boolean;
  maxChars?: number;
  expressiveness: "top" | "engine" | "";
} {
  if (engine === "auto") return { prompt: false, expressiveness: "" };
  const profile = HEYGEN_ENGINE_PROFILES[engine];
  if (!profile) return { prompt: true, expressiveness: "top" };
  return {
    prompt: profile.motionPrompt,
    maxChars: profile.maxMotionPromptChars,
    expressiveness: profile.expressiveness,
  };
}

export function heygenExpressivenessLevels(kind: "top" | "engine" | ""): string[] {
  if (kind === "engine") return ["default", "less"];
  if (kind === "top") return ["high", "medium", "low"];
  return [];
}

/** True when the selected look can't use the engine (auto is always allowed). */
export function heygenEngineBlocked(
  engine: string,
  look?: { supported_api_engines?: string[] },
): boolean {
  if (engine === "auto" || !look) return false;
  return !heygenEngineChoices(look).includes(engine);
}

/** HeyGen request options for the selected engine and motion settings. */
export function heygenAvatarEngineOptions(
  engine: string,
  motionPrompt: string,
  expressiveness: string,
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (engine === "auto") return out;
  out.engine = { type: engine };
  const motion = heygenEngineMotion(engine);
  const prompt = motionPrompt.trim();
  if (motion.prompt && prompt) out.motion_prompt = prompt;
  if (expressiveness && heygenExpressivenessLevels(motion.expressiveness).includes(expressiveness)) {
    if (motion.expressiveness === "engine") out.motion_expressiveness = expressiveness;
    else out.expressiveness = expressiveness;
  }
  return out;
}

/**
 * Estimated HeyGen API cost in USD, or 0 when there is no known rate
 * (Avatar 6 standard, 2K, unknown engines). Mirrors heygenAvatarQuote.
 */
export function estimateHeyGenAvatarCostUSD(
  seconds: number,
  engine: string,
  avatarType: string | undefined,
  resolution: string,
): number {
  if (seconds <= 0) return 0;
  const res = resolution.toLowerCase();
  if (engine === "heygen-avatar-6-turbo") return res === "1080p" ? seconds * 0.05 : 0;
  if (!["auto", "avatar_iii", "avatar_iv", "avatar_v"].includes(engine)) return 0;
  const type = avatarType || "photo_avatar";
  let rate = type === "studio_avatar" || type === "digital_twin" ? 0.0667 : 0.05;
  if (res === "4k") rate = type === "photo_avatar" ? 0.0667 : 0.0833;
  return seconds * rate;
}

// parseServerTimestamp reads a sidecar timestamp as epoch milliseconds.
// SQLite's CURRENT_TIMESTAMP is "YYYY-MM-DD HH:MM:SS" in UTC with no zone,
// which browsers would otherwise read as local time. Returns 0 when the
// value is missing or unparseable.
export function parseServerTimestamp(value: string | undefined | null): number {
  if (!value) return 0;
  let v = value.trim();
  if (/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(v)) v = v.replace(" ", "T") + "Z";
  const ms = Date.parse(v);
  return Number.isFinite(ms) ? ms : 0;
}
