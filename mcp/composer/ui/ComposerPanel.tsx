// ComposerPanel v0.3.24 - timeline editor with storage and Media Studio AI assets.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

const API = "/api/apps/composer";

function withProject(path: string, projectId: string): string {
  if (!projectId) return path;
  const sep = path.includes("?") ? "&" : "?";
  return `${path}${sep}project_id=${encodeURIComponent(projectId)}`;
}

interface Composition {
  id: number;
  name: string;
  edit_json: string;
  output_json: string;
  duration_seconds: number;
  created_at: string;
  updated_at: string;
  latest_render?: RenderRow | null;
}

interface RenderRow {
  id: number;
  executor: string;
  status: "queued" | "rendering" | "complete" | "failed" | "cancelled";
  storage_id: number;
  duration_ms: number;
  cost_usd: number;
  error: string;
  attempts: number;
  storage_url?: string;
  local_cache_url?: string;
  created_at: string;
  updated_at: string;
}

interface Bindings {
  storage_bound: boolean;
  instances_bound: boolean;
  mediastudio_bound: boolean;
  render_host_id: number;
  ffmpeg_path: string;
  render_executor?: string;
}

type AssetType = "video" | "image";
type MediaKind = "image" | "video" | "audio_tts" | "audio_sfx" | "music" | "avatar";
type OutputFormat = "mp4" | "mp3" | "wav" | "m4a" | "aac";
type Aspect = "16:9" | "9:16" | "1:1" | "4:3";
type DurationMode = "fixed_trim_pad" | "fit_generated" | "fit_generated_keep_start" | "fit_generated_reflow";

interface AIAsset {
  media_kind: MediaKind;
  prompt: string;
  model?: string;
  size?: string;
  duration?: number;
  aspect?: string;
  voice?: string;
  avatar?: string;
  source_image?: string;
  options?: Record<string, unknown>;
  cache_key?: string;
  cache_policy?: "reuse" | "refresh";
  status?: "draft" | "generating" | "ready" | "failed";
  generation_id?: number;
  storage_id?: number;
  job_id?: number;
  estimated_duration_seconds?: number;
  actual_duration_seconds?: number;
  audio_analysis?: {
    duration_seconds?: number;
    peak_db?: number;
    rms_db?: number;
    sample_rate?: number;
    channels?: number;
    codec?: string;
  };
  peak_db?: number;
  rms_db?: number;
  error?: string;
}

interface AudioFX {
  gain_db?: number;
  normalize?: boolean;
  loudness_target?: number;
  peak_limit_db?: number;
  fade_in_seconds?: number;
  fade_out_seconds?: number;
  trim_silence?: boolean;
}

interface ClipDraft {
  id: string;
  asset: { type: AssetType; src: string };
  start: number;
  length: number;
  duration_mode?: DurationMode;
  estimated_length?: number;
  actual_length?: number;
  transition?: { in?: string; out?: string };
  text?: { body: string; position?: "top" | "center" | "bottom"; font_size?: number; color?: string };
  ai?: AIAsset;
}

interface AudioClipDraft {
  id: string;
  asset: { type: "audio" | "silence"; src: string };
  start: number;
  length: number;
  duration_mode?: DurationMode;
  estimated_length?: number;
  actual_length?: number;
  volume: number;
  after_clip_id?: string;
  gap_seconds?: number;
  audio?: AudioFX;
  ai?: AIAsset;
}

interface OutputDraft {
  format: OutputFormat;
  resolution: "sd" | "hd" | "fullhd" | "4k";
  aspect: Aspect;
  fps: 24 | 30 | 60;
}

interface DraftState {
  name: string;
  clips: ClipDraft[];
  audioClips: AudioClipDraft[];
  soundtrack: { src: string; volume: number; ai?: AIAsset } | null;
  background: string;
  output: OutputDraft;
}

interface ResolvedAsset {
  src: string;
  url: string;
  kind: "video" | "image" | "audio";
}

interface StorageFile {
  id: number;
  name: string;
  folder?: string;
  content_type?: string;
  size_bytes?: number;
}

interface MediaHistoryGeneration {
  id: number;
  kind: string;
  prompt: string;
  model?: string;
  size?: string;
  duration_ms?: number;
  storage_ids?: number[];
  cache_key?: string;
  status?: string;
  extra_json?: string;
  request_json?: string;
  estimated_duration_seconds?: number;
  actual_duration_seconds?: number;
}

type Tab = "timeline" | "json";
type AssetPickerTarget = { kind: "clip"; clipId: string } | { kind: "audio"; clipId: string } | { kind: "soundtrack" };
type ClipEditorTarget = { kind: "visual"; id: string } | { kind: "audio"; id: string };

interface DraftExample {
  id: string;
  name: string;
  description: string;
  draft: DraftState;
}

const DEFAULT_DRAFT: DraftState = {
  name: "",
  background: "#000000",
  clips: [],
  audioClips: [],
  soundtrack: null,
  output: { format: "mp4", resolution: "hd", aspect: "16:9", fps: 30 },
};

function cloneDefault(): DraftState {
  return JSON.parse(JSON.stringify(DEFAULT_DRAFT));
}

function renderSrc(r: RenderRow | null | undefined): string {
  if (!r) return "";
  if (r.storage_url) return r.storage_url;
  if (r.local_cache_url) return r.local_cache_url;
  return "";
}

function formatCost(n: number): string {
  if (!n || n <= 0) return "";
  if (n >= 0.01) return "$" + n.toFixed(2);
  return "$" + n.toFixed(4);
}

function formatTime(seconds: number): string {
  const safe = Math.max(0, seconds || 0);
  const m = Math.floor(safe / 60);
  const s = Math.floor(safe % 60);
  return `${m}:${String(s).padStart(2, "0")}`;
}

function estimateSpeechSeconds(script: string): number {
  const words = script.trim().split(/\s+/).filter(Boolean).length;
  if (!words) return 0;
  return Math.max(5, Number((words / 2.5).toFixed(1)));
}

function defaultDurationMode(kind: MediaKind | undefined): DurationMode {
  if (kind === "audio_tts") return "fit_generated_reflow";
  if (kind === "avatar") return "fit_generated_keep_start";
  return "fixed_trim_pad";
}

function estimateForAI(ai: AIAsset | undefined): number {
  if (!ai) return 0;
  if (ai.actual_duration_seconds && ai.actual_duration_seconds > 0) return ai.actual_duration_seconds;
  if (ai.estimated_duration_seconds && ai.estimated_duration_seconds > 0) return ai.estimated_duration_seconds;
  if (ai.media_kind === "audio_tts" || ai.media_kind === "avatar") return estimateSpeechSeconds(ai.prompt);
  return ai.duration || 0;
}

function applyAIDuration<T extends { length: number; duration_mode?: DurationMode; estimated_length?: number; actual_length?: number; ai?: AIAsset }>(clip: T): T {
  if (!clip.ai) return clip;
  const estimated = estimateForAI(clip.ai);
  const actual = clip.ai.actual_duration_seconds || clip.actual_length || 0;
  const mode = clip.duration_mode || defaultDurationMode(clip.ai.media_kind);
  const length = fitsGenerated(mode)
    ? Math.max(0.1, actual || estimated || clip.length)
    : Math.max(0.1, clip.length || estimated || clip.ai.duration || 1);
  return {
    ...clip,
    duration_mode: mode,
    estimated_length: clip.estimated_length || estimated || undefined,
    actual_length: clip.actual_length || actual || undefined,
    length,
  };
}

function isAudioFormat(format: string | undefined): boolean {
  return ["mp3", "wav", "m4a", "aac"].includes(String(format || "").toLowerCase());
}

function durationOf(clips: ClipDraft[]): number {
  return clips.reduce((sum, c) => sum + Math.max(0.1, Number(c.length) || 0), 0);
}

function durationOfAudio(clips: AudioClipDraft[]): number {
  return clips.reduce((max, c) => Math.max(max, Math.max(0, Number(c.start) || 0) + Math.max(0.1, Number(c.length) || 0)), 0);
}

function durationOfDraft(draft: DraftState): number {
  return Math.max(durationOf(draft.clips), durationOfAudio(draft.audioClips));
}

function normalizeClips(clips: ClipDraft[]): ClipDraft[] {
  let t = 0;
  return clips.map((clip, i) => {
    const conformed = applyAIDuration(clip);
    const length = Math.max(0.1, Number(conformed.length) || 1);
    const next = {
      ...conformed,
      id: clip.id || `clip-${i + 1}-${Date.now()}`,
      start: Number(t.toFixed(3)),
      length,
      asset: {
        type: clip.asset?.type === "image" ? "image" : "video",
        src: clip.asset?.src || "",
      },
      ai: clip.ai,
    };
    t += length;
    return next;
  });
}

function normalizeAudioClips(clips: AudioClipDraft[]): AudioClipDraft[] {
  return clips.map((clip, i) => {
    const conformed = applyAIDuration(clip);
    const silence = clip.asset?.type === "silence";
    return {
      ...conformed,
      id: clip.id || `audio-${i + 1}-${Date.now()}`,
      start: Math.max(0, Number(clip.start) || 0),
      length: Math.max(0.1, Number(conformed.length) || 1),
      volume: Math.max(0, Math.min(1, Number(clip.volume) || 1)),
      after_clip_id: clip.after_clip_id || undefined,
      gap_seconds: Math.max(0, Number(clip.gap_seconds) || 0) || undefined,
      audio: clip.audio,
      asset: { type: silence ? "silence" : "audio", src: silence ? "" : (clip.asset?.src || "") },
      ai: clip.ai,
    };
  });
}

function prettyJSON(raw: string, fallback: string): string {
  try {
    return JSON.stringify(JSON.parse(raw || "{}"), null, 2);
  } catch {
    return fallback;
  }
}

function visualTrack(tracks: any[]): any | null {
  if (!Array.isArray(tracks)) return null;
  return tracks.find((track) => {
    const kind = String(track?.type || "visual").toLowerCase();
    return kind !== "audio" && kind !== "sound" && kind !== "music" && kind !== "voice" && kind !== "sfx";
  }) || null;
}

function audioTracks(tracks: any[]): any[] {
  if (!Array.isArray(tracks)) return [];
  return tracks.filter((track) => {
    const kind = String(track?.type || "").toLowerCase();
    if (kind === "audio" || kind === "sound" || kind === "music" || kind === "voice" || kind === "sfx") return true;
    if (kind) return false;
    const clips = Array.isArray(track?.clips) ? track.clips : [];
    return clips.length > 0 && clips.every((clip: any) => clip?.asset?.type === "audio" || ["music", "audio_tts", "audio_sfx"].includes(clip?.ai?.media_kind));
  });
}

function parseComposition(c: Composition | null): DraftState {
  if (!c) return cloneDefault();
  const draft = cloneDefault();
  draft.name = c.name || "";
  try {
    const edit = JSON.parse(c.edit_json || "{}");
    const timeline = edit.timeline || {};
    draft.background = timeline.background || draft.background;
    const track = visualTrack(timeline.tracks || []);
    const clips = Array.isArray(track?.clips) ? track.clips : [];
    if (clips.length) {
      draft.clips = normalizeClips(clips.map((clip: any, i: number) => ({
        id: String(clip.uid || `clip-${i + 1}`),
        asset: {
          type: clip.asset?.type === "image" ? "image" : "video",
          src: String(clip.asset?.src || ""),
        },
        start: Number(clip.start) || 0,
        length: Number(clip.length) || 1,
        duration_mode: clip.duration_mode || defaultDurationMode(clip.ai?.media_kind),
        estimated_length: Number(clip.estimated_length || clip.ai?.estimated_duration_seconds || 0) || undefined,
        actual_length: Number(clip.actual_length || clip.ai?.actual_duration_seconds || 0) || undefined,
        transition: {
          in: clip.transition?.in || "none",
          out: clip.transition?.out || "none",
        },
        text: clip.text?.body ? {
          body: String(clip.text.body || ""),
          position: clip.text.position || "bottom",
          font_size: Number(clip.text.font_size) || 32,
          color: clip.text.color || "#ffffff",
        } : undefined,
        ai: clip.ai,
      })));
    }
    const audio = audioTracks(timeline.tracks || []).flatMap((track: any) => Array.isArray(track?.clips) ? track.clips : []);
    if (audio.length) {
      draft.audioClips = normalizeAudioClips(audio.map((clip: any, i: number) => ({
        id: String(clip.uid || `audio-${i + 1}`),
        asset: { type: clip.asset?.type === "silence" ? "silence" : "audio", src: String(clip.asset?.src || "") },
        start: Number(clip.start) || 0,
        length: Number(clip.length ?? clip.duration) || 1,
        duration_mode: clip.duration_mode || defaultDurationMode(clip.ai?.media_kind),
        estimated_length: Number(clip.estimated_length || clip.ai?.estimated_duration_seconds || 0) || undefined,
        actual_length: Number(clip.actual_length || clip.ai?.actual_duration_seconds || 0) || undefined,
        volume: Number(clip.volume) || 1,
        after_clip_id: clip.after_clip_id,
        gap_seconds: Number(clip.gap_seconds || 0) || undefined,
        audio: clip.audio,
        ai: clip.ai,
      })));
    }
    if (timeline.soundtrack?.src) {
      draft.soundtrack = {
        src: String(timeline.soundtrack.src),
        volume: Number(timeline.soundtrack.volume) || 1,
        ai: timeline.soundtrack.ai,
      };
    } else if (timeline.soundtrack?.ai) {
      draft.soundtrack = {
        src: String(timeline.soundtrack.src || ""),
        volume: Number(timeline.soundtrack.volume) || 1,
        ai: timeline.soundtrack.ai,
      };
    }
  } catch {}
  try {
    const output = JSON.parse(c.output_json || "{}");
    draft.output = {
      format: ["mp4", "mp3", "wav", "m4a", "aac"].includes(output.format) ? output.format : "mp4",
      resolution: ["sd", "hd", "fullhd", "4k"].includes(output.resolution) ? output.resolution : "hd",
      aspect: ["16:9", "9:16", "1:1", "4:3"].includes(output.aspect) ? output.aspect : "16:9",
      fps: [24, 30, 60].includes(output.fps) ? output.fps : 30,
    };
  } catch {}
  return draft;
}

function draftToBody(draft: DraftState): Record<string, unknown> {
  const clips = normalizeClips(draft.clips).map((clip) => {
    const out: any = {
      uid: clip.id,
      asset: { type: clip.asset.type, src: clip.asset.src.trim() },
      start: clip.start,
      length: clip.length,
    };
    if (clip.ai) out.ai = clip.ai;
    if (clip.duration_mode) out.duration_mode = clip.duration_mode;
    if (clip.estimated_length) out.estimated_length = clip.estimated_length;
    if (clip.actual_length) out.actual_length = clip.actual_length;
    if ((clip.transition?.in && clip.transition.in !== "none") || (clip.transition?.out && clip.transition.out !== "none")) {
      out.transition = {
        in: clip.transition?.in || "none",
        out: clip.transition?.out || "none",
      };
    }
    if (clip.text?.body?.trim()) {
      out.text = {
        body: clip.text.body,
        position: clip.text.position || "bottom",
        font_size: clip.text.font_size || 32,
        color: clip.text.color || "#ffffff",
      };
    }
    return out;
  });
  const audioClips = normalizeAudioClips(draft.audioClips).map((clip) => {
    const out: any = {
      uid: clip.id,
      asset: { type: clip.asset.type, src: clip.asset.type === "silence" ? "" : clip.asset.src.trim() },
      start: clip.start,
      length: clip.length,
      volume: Math.max(0, Math.min(1, Number(clip.volume) || 1)),
    };
    if (clip.ai) out.ai = clip.ai;
    if (clip.duration_mode) out.duration_mode = clip.duration_mode;
    if (clip.estimated_length) out.estimated_length = clip.estimated_length;
    if (clip.actual_length) out.actual_length = clip.actual_length;
    if (clip.after_clip_id) out.after_clip_id = clip.after_clip_id;
    if (clip.gap_seconds) out.gap_seconds = clip.gap_seconds;
    if (clip.audio) out.audio = clip.audio;
    return out;
  });
  const tracks: any[] = [];
  if (clips.length) tracks.push({ type: "visual", clips });
  if (audioClips.length) tracks.push({ type: "audio", clips: audioClips });
  const body: Record<string, unknown> = {
    name: draft.name,
    tracks,
    background: draft.background || "#000000",
    output: draft.output,
  };
  if (draft.soundtrack?.src?.trim() || draft.soundtrack?.ai) {
    body.soundtrack = {
      src: draft.soundtrack.src.trim(),
      volume: Math.max(0, Math.min(1, Number(draft.soundtrack.volume) || 1)),
      ai: draft.soundtrack.ai,
    };
  }
  return body;
}

function bodyFromEditorJSON(name: string, editText: string, outputText: string): Record<string, unknown> {
  const edit = JSON.parse(editText || "{}");
  const output = JSON.parse(outputText || "{}");
  const timeline = edit.timeline || {};
  return {
    name,
    tracks: Array.isArray(timeline.tracks) ? timeline.tracks : [],
    soundtrack: timeline.soundtrack,
    background: timeline.background,
    output,
  };
}

function editJSONFromDraft(draft: DraftState): string {
  const body = draftToBody(draft);
  return JSON.stringify({
    timeline: {
      tracks: body.tracks,
      soundtrack: body.soundtrack,
      background: body.background,
    },
  }, null, 2);
}

function outputJSONFromDraft(draft: DraftState): string {
  return JSON.stringify(draft.output, null, 2);
}

function activeClipAt(clips: ClipDraft[], seconds: number): ClipDraft | null {
  const normalized = normalizeClips(clips);
  if (normalized.length === 0) return null;
  return normalized.find((clip) => seconds >= clip.start && seconds < clip.start + clip.length) || normalized[normalized.length - 1] || null;
}

function aspectRatio(aspect: Aspect): string {
  switch (aspect) {
    case "9:16": return "9 / 16";
    case "1:1": return "1 / 1";
    case "4:3": return "4 / 3";
    default: return "16 / 9";
  }
}

function assetTypeFromFile(file: StorageFile): AssetType {
  return storageFileKind(file) === "image" ? "image" : "video";
}

function storageFileKind(file: StorageFile): "video" | "image" | "audio" {
  const ct = (file.content_type || "").toLowerCase();
  const name = (file.name || "").toLowerCase();
  if (ct.startsWith("image/") || /\.(png|jpe?g|webp|gif|avif)$/.test(name)) return "image";
  if (ct.startsWith("audio/") || /\.(mp3|wav|m4a|aac|flac)$/.test(name)) return "audio";
  return "video";
}

function fileSize(bytes?: number): string {
  if (!bytes || bytes <= 0) return "";
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function defaultAI(kind: MediaKind, aspect: Aspect): AIAsset {
  return {
    media_kind: kind,
    prompt: "",
    size: kind === "image" ? imageSizeForAspect(aspect) : undefined,
    duration: kind === "image" ? undefined : kind === "music" ? 30 : 5,
    aspect: kind === "video" || kind === "avatar" ? aspect : undefined,
    cache_policy: "reuse",
    status: "draft",
  };
}

function imageSizeForAspect(aspect: Aspect): string {
  switch (aspect) {
    case "9:16":
      return "720x1280";
    case "1:1":
      return "1024x1024";
    case "4:3":
      return "1024x768";
    default:
      return "1280x720";
  }
}

function exampleAI(kind: MediaKind, prompt: string, aspect: Aspect, duration?: number): AIAsset {
  const ai = defaultAI(kind, aspect);
  ai.prompt = prompt;
  if (duration) {
    ai.duration = duration;
    ai.estimated_duration_seconds = duration;
  }
  return withDurationEstimate(ai);
}

function composerExamples(): DraftExample[] {
  const voiceOne = "Welcome to this quick generated audio example.";
  const voiceTwo = "This second generated voice starts after a deliberate pause.";
  const voiceOneLength = estimateSpeechSeconds(voiceOne);
  const voiceTwoLength = estimateSpeechSeconds(voiceTwo);
  return [
    {
      id: "ai-voice-gap",
      name: "AI voice, silence, AI voice",
      description: "MP3 with two TTS clips and a 5 second gap.",
      draft: {
        ...cloneDefault(),
        name: "AI voice with silence gap",
        clips: [],
        audioClips: [
          {
            id: "voice-1",
            asset: { type: "audio", src: "" },
            start: 0,
            length: voiceOneLength,
            duration_mode: "fit_generated_reflow",
            estimated_length: voiceOneLength,
            volume: 1,
            ai: exampleAI("audio_tts", voiceOne, "16:9", voiceOneLength),
          },
          {
            id: "voice-2",
            asset: { type: "audio", src: "" },
            start: voiceOneLength + 5,
            length: voiceTwoLength,
            duration_mode: "fit_generated_reflow",
            estimated_length: voiceTwoLength,
            volume: 1,
            ai: exampleAI("audio_tts", voiceTwo, "16:9", voiceTwoLength),
          },
        ],
        soundtrack: null,
        output: { format: "mp3", resolution: "hd", aspect: "16:9", fps: 30 },
      },
    },
    {
      id: "ai-image-music",
      name: "AI image with AI music",
      description: "Short MP4 using generated image and background music.",
      draft: {
        ...cloneDefault(),
        name: "AI image with music",
        clips: [
          {
            id: "hero-image",
            asset: { type: "image", src: "" },
            start: 0,
            length: 8,
            duration_mode: "fixed_trim_pad",
            ai: exampleAI("image", "A clean cinematic product hero image on a modern studio desk", "16:9"),
            text: { body: "Product launch", position: "bottom", font_size: 36, color: "#ffffff" },
          },
        ],
        audioClips: [
          {
            id: "music-bed",
            asset: { type: "audio", src: "" },
            start: 0,
            length: 8,
            duration_mode: "fixed_trim_pad",
            estimated_length: 8,
            volume: 0.65,
            ai: exampleAI("music", "Minimal optimistic electronic background music for a product reveal", "16:9", 8),
          },
        ],
        soundtrack: null,
        output: { format: "mp4", resolution: "hd", aspect: "16:9", fps: 30 },
      },
    },
    {
      id: "avatar-explainer",
      name: "AI avatar explainer",
      description: "Talking-head avatar clip that fits generated duration.",
      draft: {
        ...cloneDefault(),
        name: "AI avatar explainer",
        clips: [
          {
            id: "avatar-1",
            asset: { type: "video", src: "" },
            start: 0,
            length: 7,
            duration_mode: "fit_generated_keep_start",
            estimated_length: 7,
            ai: exampleAI("avatar", "Hi, this is an example avatar video generated from a short script.", "16:9", 7),
          },
        ],
        audioClips: [],
        soundtrack: null,
        output: { format: "mp4", resolution: "hd", aspect: "16:9", fps: 30 },
      },
    },
  ];
}

function withDurationEstimate(ai: AIAsset): AIAsset {
  const estimate = estimateForAI(ai);
  if (estimate <= 0) return ai;
  return { ...ai, estimated_duration_seconds: ai.estimated_duration_seconds || estimate };
}

function cacheKeyForAI(ai: AIAsset): string {
  const stable = JSON.stringify({
    media_kind: ai.media_kind,
    prompt: ai.prompt,
    model: ai.model || "",
    size: ai.size || "",
    duration: ai.duration || 0,
    aspect: ai.aspect || "",
    voice: ai.voice || "",
    avatar: ai.avatar || "",
    source_image: ai.source_image || "",
    options: ai.options || {},
  });
  let h = 2166136261;
  for (let i = 0; i < stable.length; i += 1) {
    h ^= stable.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return `composer:${(h >>> 0).toString(16)}`;
}

function storageIDFromMeta(meta: any): number {
  const ids = Array.isArray(meta?.storage_ids) ? meta.storage_ids : [];
  const n = Number(ids[0] || 0);
  return Number.isFinite(n) ? n : 0;
}

function generationIDFromMeta(meta: any): number {
  const n = Number(meta?.generation_id || 0);
  return Number.isFinite(n) ? n : 0;
}

function aiFromMeta(ai: AIAsset, meta: any, cacheKey: string): AIAsset {
  const estimated = Number(meta?.estimated_duration_seconds || ai.estimated_duration_seconds || 0) || undefined;
  const actual = Number(meta?.actual_duration_seconds || ai.actual_duration_seconds || 0) || undefined;
  const analysis = meta?.audio_analysis || ai.audio_analysis;
  return {
    ...ai,
    cache_key: cacheKey,
    status: "ready",
    storage_id: storageIDFromMeta(meta),
    generation_id: generationIDFromMeta(meta),
    estimated_duration_seconds: estimated,
    actual_duration_seconds: actual,
    audio_analysis: analysis,
    peak_db: Number(meta?.peak_db || ai.peak_db || analysis?.peak_db || 0) || undefined,
    rms_db: Number(meta?.rms_db || ai.rms_db || analysis?.rms_db || 0) || undefined,
    error: "",
  };
}

function parseJSONRecord(raw: string | undefined): Record<string, any> {
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function storageIDFromSrc(src: string | undefined): number {
  const match = String(src || "").trim().match(/^storage:(\d+)$/);
  return match ? Number(match[1]) : 0;
}

function aiFromGeneration(g: MediaHistoryGeneration, storageId: number): AIAsset {
  const extra = parseJSONRecord(g.extra_json);
  const req = parseJSONRecord(g.request_json);
  const opts = req.options && typeof req.options === "object" && !Array.isArray(req.options) ? req.options as Record<string, any> : {};
  const kind = (g.kind || "audio_tts") as MediaKind;
  const status = g.status === "complete" || g.status === "completed" ? "ready" : (g.status || "ready");
  return {
    media_kind: kind,
    prompt: g.prompt || String(req.prompt || ""),
    model: g.model || String(req.model || "") || undefined,
    size: g.size || String(req.size || "") || undefined,
    duration: Number(req.duration || Math.round((g.duration_ms || 0) / 1000)) || undefined,
    aspect: String(extra.aspect || req.aspect || "") || undefined,
    voice: String(extra.voice || req.voice || "") || undefined,
    avatar: String(extra.avatar || req.avatar || "") || undefined,
    source_image: String(extra.source_image_ref || req.source_image || "") || undefined,
    options: Object.keys(opts).length ? opts : undefined,
    cache_key: g.cache_key || undefined,
    cache_policy: "reuse",
    status: status as AIAsset["status"],
    generation_id: Number(g.id) || undefined,
    storage_id: storageId,
    estimated_duration_seconds: Number(g.estimated_duration_seconds || 0) || undefined,
    actual_duration_seconds: Number(g.actual_duration_seconds || 0) || undefined,
    error: "",
  };
}

function compositionNeedsAIEnrichment(c: Composition): boolean {
  try {
    const timeline = JSON.parse(c.edit_json || "{}").timeline || {};
    for (const track of timeline.tracks || []) {
      for (const clip of track.clips || []) {
        if (!clip.ai && storageIDFromSrc(clip.asset?.src) > 0) return true;
      }
    }
    if (timeline.soundtrack && !timeline.soundtrack.ai && storageIDFromSrc(timeline.soundtrack.src) > 0) return true;
  } catch {}
  return false;
}

function enrichEditJSONWithGenerations(editJSON: string, byStorage: Map<number, MediaHistoryGeneration>): string {
  if (!byStorage.size) return editJSON;
  try {
    const edit = JSON.parse(editJSON || "{}");
    const timeline = edit.timeline || {};
    let changed = false;
    for (const track of timeline.tracks || []) {
      for (const clip of track.clips || []) {
        if (clip.ai) continue;
        const storageId = storageIDFromSrc(clip.asset?.src);
        const gen = byStorage.get(storageId);
        if (!gen) continue;
        clip.ai = aiFromGeneration(gen, storageId);
        clip.duration_mode = clip.duration_mode || defaultDurationMode(clip.ai.media_kind);
        clip.estimated_length = clip.estimated_length || clip.ai.estimated_duration_seconds;
        clip.actual_length = clip.actual_length || clip.ai.actual_duration_seconds;
        changed = true;
      }
    }
    if (timeline.soundtrack && !timeline.soundtrack.ai) {
      const storageId = storageIDFromSrc(timeline.soundtrack.src);
      const gen = byStorage.get(storageId);
      if (gen) {
        timeline.soundtrack.ai = aiFromGeneration(gen, storageId);
        changed = true;
      }
    }
    return changed ? JSON.stringify(edit) : editJSON;
  } catch {
    return editJSON;
  }
}

async function enrichCompositionsWithMediaStudio(projectId: string, rows: Composition[]): Promise<Composition[]> {
  if (!projectId || !rows.some(compositionNeedsAIEnrichment)) return rows;
  try {
    const url = `/api/apps/media-studio/generations?project_id=${encodeURIComponent(projectId)}&limit=200`;
    const res = await fetch(url, { credentials: "same-origin" });
    if (!res.ok) return rows;
    const data = await res.json();
    const generations: MediaHistoryGeneration[] = Array.isArray(data?.generations) ? data.generations : [];
    const byStorage = new Map<number, MediaHistoryGeneration>();
    for (const gen of generations) {
      for (const id of gen.storage_ids || []) {
        if (id > 0) byStorage.set(id, gen);
      }
    }
    if (!byStorage.size) return rows;
    return rows.map((row) => ({
      ...row,
      edit_json: enrichEditJSONWithGenerations(row.edit_json, byStorage),
    }));
  } catch {
    return rows;
  }
}

function fitsGenerated(mode: DurationMode | undefined): boolean {
  return mode === "fit_generated" || mode === "fit_generated_keep_start" || mode === "fit_generated_reflow";
}

export default function ComposerPanel({ projectId }: NativePanelProps) {
  const [compositions, setCompositions] = useState<Composition[]>([]);
  const [bindings, setBindings] = useState<Bindings | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [draft, setDraft] = useState<DraftState>(() => cloneDefault());
  const [selectedClipId, setSelectedClipId] = useState<string>("clip-1");
  const [executor, setExecutor] = useState<"auto" | "local" | "remote">("auto");
  const [rendering, setRendering] = useState(false);
  const [status, setStatus] = useState<string>("");
  const [lightbox, setLightbox] = useState<RenderRow | null>(null);
  const [tab, setTab] = useState<Tab>("timeline");
  const [playhead, setPlayhead] = useState(0);
  const [timelineZoom, setTimelineZoom] = useState(1);
  const [playing, setPlaying] = useState(false);
  const [resolved, setResolved] = useState<Record<string, ResolvedAsset>>({});
  const [jsonEdit, setJsonEdit] = useState("");
  const [jsonOutput, setJsonOutput] = useState("");
  const [pickerTarget, setPickerTarget] = useState<AssetPickerTarget | null>(null);
  const [clipEditor, setClipEditor] = useState<ClipEditorTarget | null>(null);
  const [storageFiles, setStorageFiles] = useState<StorageFile[]>([]);
  const [storageLoading, setStorageLoading] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [aiBusy, setAIBusy] = useState("");

  const selected = selectedId != null ? compositions.find((c) => c.id === selectedId) || null : null;
  const examples = useMemo(() => composerExamples(), []);
  const clips = useMemo(() => normalizeClips(draft.clips), [draft.clips]);
  const audioClips = useMemo(() => normalizeAudioClips(draft.audioClips), [draft.audioClips]);
  const totalDuration = useMemo(() => Math.max(durationOf(clips), durationOfAudio(audioClips)), [clips, audioClips]);
  const activeClip = useMemo(() => activeClipAt(clips, playhead), [clips, playhead]);
  const selectedClip = clips.find((clip) => clip.id === selectedClipId) || clips[0] || null;

  const load = useCallback(async () => {
    try {
      const res = await fetch(withProject(`${API}/compositions`, projectId), { credentials: "same-origin" });
      if (!res.ok) {
        setStatus(`Error: ${res.status}`);
        return;
      }
      const data = await res.json();
      const rows = await enrichCompositionsWithMediaStudio(projectId, data.compositions || []);
      setCompositions(rows);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [projectId]);

  useEffect(() => {
    load();
    fetch(withProject(`${API}/bindings`, projectId), { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => d && setBindings(d))
      .catch(() => {});
  }, [load]);

  useEffect(() => {
    const next = parseComposition(selected);
    setDraft(next);
    setSelectedClipId(next.clips[0]?.id || "");
    setPlayhead(0);
    setJsonEdit(selected ? prettyJSON(selected.edit_json, editJSONFromDraft(next)) : editJSONFromDraft(next));
    setJsonOutput(selected ? prettyJSON(selected.output_json, outputJSONFromDraft(next)) : outputJSONFromDraft(next));
  }, [selectedId, selected?.edit_json, selected?.output_json]);

  useEffect(() => {
    if (!playing) return;
    const id = window.setInterval(() => {
      setPlayhead((t) => {
        const next = t + 0.1;
        if (next >= totalDuration) {
          setPlaying(false);
          return 0;
        }
        return next;
      });
    }, 100);
    return () => window.clearInterval(id);
  }, [playing, totalDuration]);

  useEffect(() => {
    const sources = Array.from(new Set([
      ...clips.map((clip) => clip.asset.src.trim()).filter(Boolean),
      ...audioClips.map((clip) => clip.asset.src.trim()).filter(Boolean),
      draft.soundtrack?.src?.trim() || "",
    ].filter(Boolean)));
    for (const src of sources) {
      if (resolved[src]) continue;
      if (src.startsWith("http://") || src.startsWith("https://")) {
        setResolved((prev) => ({ ...prev, [src]: { src, url: src, kind: src.match(/\.(png|jpe?g|webp|gif)(\?|$)/i) ? "image" : "video" } }));
        continue;
      }
      fetch(withProject(`${API}/assets/resolve?src=${encodeURIComponent(src)}`, projectId), { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : null))
        .then((asset) => {
          if (!asset?.url) return;
          setResolved((prev) => ({ ...prev, [src]: asset }));
        })
        .catch(() => {});
    }
  }, [clips, audioClips, draft.soundtrack?.src, resolved, projectId]);

  const updateDraft = (fn: (draft: DraftState) => DraftState) => {
    setDraft((cur) => {
      const next = fn(cur);
      const normalized = { ...next, clips: normalizeClips(next.clips), audioClips: normalizeAudioClips(next.audioClips) };
      setJsonEdit(editJSONFromDraft(normalized));
      setJsonOutput(outputJSONFromDraft(normalized));
      return normalized;
    });
  };

  const updateClip = (id: string, patch: Partial<ClipDraft>) => {
    updateDraft((cur) => ({
      ...cur,
      clips: cur.clips.map((clip) => clip.id === id ? { ...clip, ...patch } : clip),
    }));
  };

  const addClip = () => {
    const id = `clip-${Date.now()}`;
    updateDraft((cur) => ({
      ...cur,
      clips: [
        ...cur.clips,
        {
          id,
          asset: { type: "video", src: "" },
          start: durationOf(cur.clips),
          length: 4,
          transition: { in: "none", out: "none" },
        },
      ],
    }));
    setSelectedClipId(id);
  };

  const addAIVisualClip = (kind: "image" | "video" | "avatar") => {
    const id = `clip-${Date.now()}`;
    updateDraft((cur) => ({
      ...cur,
      clips: [
        ...cur.clips,
        {
          id,
          asset: { type: kind === "image" ? "image" : "video", src: "" },
          start: durationOf(cur.clips),
          length: kind === "image" ? 4 : 6,
          duration_mode: defaultDurationMode(kind),
          transition: { in: "none", out: "none" },
          ai: defaultAI(kind, cur.output.aspect),
        },
      ],
      output: { ...cur.output, format: "mp4" },
    }));
    setSelectedClipId(id);
    setClipEditor({ kind: "visual", id });
  };

  const addClipFromFile = (file: StorageFile) => {
    const id = `clip-${Date.now()}`;
    const type = assetTypeFromFile(file);
    updateDraft((cur) => ({
      ...cur,
      clips: [
        ...cur.clips,
        {
          id,
          asset: { type, src: `storage:${file.id}` },
          start: durationOf(cur.clips),
          length: type === "image" ? 4 : 6,
          transition: { in: "none", out: "none" },
        },
      ],
    }));
    setSelectedClipId(id);
    setPickerTarget(null);
  };

  const deleteClip = (id: string) => {
    updateDraft((cur) => {
      const next = cur.clips.filter((clip) => clip.id !== id);
      return { ...cur, clips: next.length ? next : cloneDefault().clips.slice(0, 1) };
    });
    setSelectedClipId((cur) => clips.find((clip) => clip.id !== id)?.id || cur);
  };

  const moveClip = (id: string, dir: -1 | 1) => {
    updateDraft((cur) => {
      const next = [...cur.clips];
      const i = next.findIndex((clip) => clip.id === id);
      const j = i + dir;
      if (i < 0 || j < 0 || j >= next.length) return cur;
      [next[i], next[j]] = [next[j], next[i]];
      return { ...cur, clips: next };
    });
  };

  const updateAudioClip = (id: string, patch: Partial<AudioClipDraft>) => {
    updateDraft((cur) => ({
      ...cur,
      audioClips: cur.audioClips.map((clip) => clip.id === id ? { ...clip, ...patch } : clip),
    }));
  };

  const addAudioClip = (ai: boolean | MediaKind = true) => {
    const id = `audio-${Date.now()}`;
    const aiKind: MediaKind = typeof ai === "string" ? ai : "music";
    const withAI = Boolean(ai);
    updateDraft((cur) => ({
      ...cur,
      audioClips: [
        ...cur.audioClips,
        {
          id,
          asset: { type: "audio", src: "" },
          start: durationOfDraft(cur),
          length: withAI ? 30 : 8,
          duration_mode: withAI ? defaultDurationMode(aiKind) : undefined,
          volume: 1,
          ai: withAI ? defaultAI(aiKind, cur.output.aspect) : undefined,
        },
      ],
      output: cur.clips.length === 0 ? { ...cur.output, format: isAudioFormat(cur.output.format) ? cur.output.format : "mp3" } : cur.output,
    }));
    setClipEditor({ kind: "audio", id });
  };

  const addSilenceClip = () => {
    const id = `silence-${Date.now()}`;
    updateDraft((cur) => ({
      ...cur,
      audioClips: [
        ...cur.audioClips,
        {
          id,
          asset: { type: "silence", src: "" },
          start: durationOfDraft(cur),
          length: 5,
          volume: 1,
        },
      ],
      output: cur.clips.length === 0 ? { ...cur.output, format: isAudioFormat(cur.output.format) ? cur.output.format : "mp3" } : cur.output,
    }));
    setClipEditor({ kind: "audio", id });
  };

  const editGapAsSilence = (start: number, length: number) => {
    const id = `silence-${Date.now()}`;
    updateDraft((cur) => ({
      ...cur,
      audioClips: [
        ...cur.audioClips,
        {
          id,
          asset: { type: "silence", src: "" },
          start: Number(start.toFixed(3)),
          length: Math.max(0.1, Number(length.toFixed(3))),
          volume: 1,
        },
      ],
      output: cur.clips.length === 0 ? { ...cur.output, format: isAudioFormat(cur.output.format) ? cur.output.format : "mp3" } : cur.output,
    }));
    setSelectedClipId(id);
    setPlayhead(start);
    setClipEditor({ kind: "audio", id });
  };

  const addAISoundtrack = () => {
    updateDraft((cur) => ({
      ...cur,
      soundtrack: {
        src: cur.soundtrack?.src || "",
        volume: cur.soundtrack?.volume ?? 0.7,
        ai: cur.soundtrack?.ai || defaultAI("music", cur.output.aspect),
      },
    }));
    setStatus("AI music soundtrack added. Enter a prompt in the soundtrack panel, then generate now or render.");
  };

  const deleteAudioClip = (id: string) => {
    updateDraft((cur) => ({ ...cur, audioClips: cur.audioClips.filter((clip) => clip.id !== id) }));
  };

  const save = async () => {
    setStatus("Saving...");
    try {
      if (tab !== "json" && draft.clips.length === 0 && draft.audioClips.length === 0) {
        setStatus("Add at least one clip before saving.");
        return;
      }
      const body = tab === "json" ? bodyFromEditorJSON(draft.name, jsonEdit, jsonOutput) : draftToBody(draft);
      if (!Array.isArray((body as any).tracks) || (body as any).tracks.length === 0) {
        setStatus("Add at least one track before saving.");
        return;
      }
      const url = withProject(selectedId == null ? `${API}/composition/new` : `${API}/composition/${selectedId}`, projectId);
      const method = selectedId == null ? "POST" : "PUT";
      const res = await fetch(url, {
        method,
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Save failed: ${res.status} ${text.slice(0, 300)}`);
        return;
      }
      const result = JSON.parse(text || "{}") as { id?: number };
      setStatus("Saved.");
      await load();
      if (result.id) setSelectedId(result.id);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  };

  const applyJSON = () => {
    try {
      const edit = JSON.parse(jsonEdit);
      const output = JSON.parse(jsonOutput);
      const c: Composition = {
        id: selectedId || 0,
        name: draft.name,
        edit_json: JSON.stringify(edit),
        output_json: JSON.stringify(output),
        duration_seconds: 0,
        created_at: "",
        updated_at: "",
      };
      const next = parseComposition(c);
      setDraft(next);
      setSelectedClipId(next.clips[0]?.id || "");
      setPlayhead(0);
      setJsonEdit(JSON.stringify(edit, null, 2));
      setJsonOutput(JSON.stringify(output, null, 2));
      setStatus("JSON applied.");
    } catch (e) {
      setStatus("JSON error: " + (e as Error).message);
    }
  };

  const render = async () => {
    if (selectedId == null && tab !== "json" && draft.clips.length === 0 && draft.audioClips.length === 0) {
      setStatus("Add at least one clip before rendering.");
      return;
    }
    if (selectedId == null) {
      await save();
      setStatus("Saved. Render after selecting the new composition.");
      return;
    }
    setRendering(true);
    setStatus("Rendering...");
    try {
      const body: Record<string, unknown> = { id: selectedId };
      if (executor !== "auto") body.executor = executor;
      const res = await fetch(withProject(`${API}/render`, projectId), {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Render failed: ${res.status} ${text.slice(0, 500)}`);
        return;
      }
      const result = JSON.parse(text);
      if (result.status === "waiting_ai") {
        const pending = Array.isArray(result.pending) ? result.pending.join("; ") : result.message || "";
        setStatus(`AI assets started. ${pending} Render again when they are ready.`);
        await load();
        return;
      }
      const costStr = result.cost_usd ? ` (${formatCost(result.cost_usd)})` : "";
      setStatus(`Render ${result.status}${costStr} via ${result.executor} in ${(result.duration_ms / 1000).toFixed(1)}s`);
      await load();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setRendering(false);
    }
  };

  const deleteSelected = async () => {
    if (selectedId == null) return;
    if (!confirm(`Delete composition #${selectedId}?`)) return;
    await fetch(withProject(`${API}/composition/${selectedId}`, projectId), { method: "DELETE", credentials: "same-origin" });
    setSelectedId(null);
    await load();
  };

  const loadExample = (example: DraftExample) => {
    const next = JSON.parse(JSON.stringify(example.draft)) as DraftState;
    const normalized = { ...next, clips: normalizeClips(next.clips), audioClips: normalizeAudioClips(next.audioClips) };
    setSelectedId(null);
    setDraft(normalized);
    setSelectedClipId(normalized.clips[0]?.id || "");
    setPlayhead(0);
    setTab("timeline");
    setJsonEdit(editJSONFromDraft(normalized));
    setJsonOutput(outputJSONFromDraft(normalized));
    setStatus(`Loaded example: ${example.name}. Save it to create a composition.`);
  };

  const openPicker = (target: AssetPickerTarget) => {
    setPickerTarget(target);
    setStorageLoading(true);
    setStorageError("");
    fetch(withProject(`${API}/assets/storage?folder=/&recursive=true&limit=200`, projectId), { credentials: "same-origin" })
      .then(async (res) => {
        if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
        return res.json();
      })
      .then((data) => setStorageFiles(data.files || []))
      .catch((e) => {
        setStorageFiles([]);
        setStorageError((e as Error).message);
      })
      .finally(() => setStorageLoading(false));
  };

  const chooseStorageFile = (file: StorageFile) => {
    if (!pickerTarget) return;
    if (pickerTarget.kind === "soundtrack") {
      updateDraft((cur) => ({ ...cur, soundtrack: { src: `storage:${file.id}`, volume: cur.soundtrack?.volume ?? 1 } }));
      setPickerTarget(null);
      return;
    }
    if (pickerTarget.kind === "audio") {
      updateAudioClip(pickerTarget.clipId, { asset: { type: "audio", src: `storage:${file.id}` } });
      setPickerTarget(null);
      return;
    }
    const clip = clips.find((c) => c.id === pickerTarget.clipId);
    if (!clip) {
      addClipFromFile(file);
      return;
    }
    updateClip(clip.id, { asset: { type: assetTypeFromFile(file), src: `storage:${file.id}` } });
    setSelectedClipId(clip.id);
    setPickerTarget(null);
  };

  const callMediaStudioGenerate = async (ai: AIAsset) => {
    const nextAI = withDurationEstimate(ai);
    const cache_key = nextAI.cache_key || cacheKeyForAI(nextAI);
    const body: Record<string, unknown> = {
      kind: nextAI.media_kind,
      prompt: nextAI.prompt,
      cache_key,
      cache_policy: nextAI.cache_policy || "reuse",
    };
    if (nextAI.model) body.model = nextAI.model;
    if (nextAI.size) body.size = nextAI.size;
    if (nextAI.duration) body.duration = nextAI.duration;
    if (nextAI.aspect) body.aspect = nextAI.aspect;
    if (nextAI.voice) body.voice = nextAI.voice;
    if (nextAI.avatar) body.avatar = nextAI.avatar;
    if (nextAI.source_image) body.source_image = nextAI.source_image;
    const options = { ...(nextAI.options || {}) };
    if (nextAI.estimated_duration_seconds && !options.estimated_duration_seconds) {
      options.estimated_duration_seconds = nextAI.estimated_duration_seconds;
    }
    if (Object.keys(options).length > 0) body.options = options;
    const res = await fetch(`/api/apps/media-studio/generate?project_id=${encodeURIComponent(projectId)}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`${res.status}: ${text.slice(0, 400)}`);
    const data = JSON.parse(text || "{}");
    return { data, meta: data._meta || {}, cache_key };
  };

  const generateClipAI = async (clip: ClipDraft) => {
    if (!clip.ai) return;
    if (!clip.ai.prompt.trim()) {
      setStatus("AI prompt required.");
      return;
    }
    setAIBusy(clip.id);
    setStatus("Generating AI clip...");
    try {
      const { meta, cache_key } = await callMediaStudioGenerate(clip.ai);
      if (meta.status === "queued" || meta.status === "polling") {
        const estimated = Number(meta.estimated_duration_seconds || clip.ai.estimated_duration_seconds || estimateForAI(clip.ai) || 0) || undefined;
        updateClip(clip.id, {
          duration_mode: clip.duration_mode || defaultDurationMode(clip.ai.media_kind),
          estimated_length: estimated,
          ai: { ...clip.ai, cache_key, estimated_duration_seconds: estimated, status: "generating", job_id: Number(meta.job_id || 0), error: "" },
        });
        setStatus(`AI clip queued as job #${meta.job_id}.`);
        return;
      }
      const storageId = storageIDFromMeta(meta);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
      const nextAI = aiFromMeta(clip.ai, meta, cache_key);
      updateClip(clip.id, {
        asset: { type: clip.ai.media_kind === "image" ? "image" : "video", src: `storage:${storageId}` },
        ai: nextAI,
        duration_mode: clip.duration_mode || defaultDurationMode(clip.ai.media_kind),
        estimated_length: nextAI.estimated_duration_seconds || clip.estimated_length,
        actual_length: nextAI.actual_duration_seconds || clip.actual_length,
        length: fitsGenerated(clip.duration_mode || defaultDurationMode(clip.ai.media_kind))
          ? (nextAI.actual_duration_seconds || nextAI.estimated_duration_seconds || clip.length)
          : clip.length,
      });
      setStatus(`AI clip ready as storage:${storageId}.`);
    } catch (e) {
      updateClip(clip.id, { ai: { ...clip.ai, status: "failed", error: (e as Error).message } });
      setStatus("AI generation failed: " + (e as Error).message);
    } finally {
      setAIBusy("");
    }
  };

  const generateSoundtrackAI = async () => {
    const ai = draft.soundtrack?.ai;
    if (!ai) return;
    if (!ai.prompt.trim()) {
      setStatus("AI prompt required.");
      return;
    }
    setAIBusy("soundtrack");
    setStatus("Generating AI soundtrack...");
    try {
      const { meta, cache_key } = await callMediaStudioGenerate(ai);
      const storageId = storageIDFromMeta(meta);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
      const nextAI = aiFromMeta(ai, meta, cache_key);
      updateDraft((cur) => ({
        ...cur,
        soundtrack: {
          src: `storage:${storageId}`,
          volume: cur.soundtrack?.volume ?? 1,
          ai: nextAI,
        },
      }));
      setStatus(`AI soundtrack ready as storage:${storageId}.`);
    } catch (e) {
      updateDraft((cur) => ({
        ...cur,
        soundtrack: cur.soundtrack ? { ...cur.soundtrack, ai: { ...ai, status: "failed", error: (e as Error).message } } : cur.soundtrack,
      }));
      setStatus("AI generation failed: " + (e as Error).message);
    } finally {
      setAIBusy("");
    }
  };

  const generateAudioClipAI = async (clip: AudioClipDraft) => {
    if (!clip.ai) return;
    if (!clip.ai.prompt.trim()) {
      setStatus("AI prompt required.");
      return;
    }
    setAIBusy(clip.id);
    setStatus("Generating AI audio...");
    try {
      const { meta, cache_key } = await callMediaStudioGenerate(clip.ai);
      if (meta.status === "queued" || meta.status === "polling") {
        const estimated = Number(meta.estimated_duration_seconds || clip.ai.estimated_duration_seconds || estimateForAI(clip.ai) || 0) || undefined;
        updateAudioClip(clip.id, {
          duration_mode: clip.duration_mode || defaultDurationMode(clip.ai.media_kind),
          estimated_length: estimated,
          ai: { ...clip.ai, cache_key, estimated_duration_seconds: estimated, status: "generating", job_id: Number(meta.job_id || 0), error: "" },
        });
        setStatus(`AI audio queued as job #${meta.job_id}.`);
        return;
      }
      const storageId = storageIDFromMeta(meta);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
      const nextAI = aiFromMeta(clip.ai, meta, cache_key);
      updateAudioClip(clip.id, {
        asset: { type: "audio", src: `storage:${storageId}` },
        ai: nextAI,
        duration_mode: clip.duration_mode || defaultDurationMode(clip.ai.media_kind),
        estimated_length: nextAI.estimated_duration_seconds || clip.estimated_length,
        actual_length: nextAI.actual_duration_seconds || clip.actual_length,
        length: fitsGenerated(clip.duration_mode || defaultDurationMode(clip.ai.media_kind))
          ? (nextAI.actual_duration_seconds || nextAI.estimated_duration_seconds || clip.length)
          : clip.length,
      });
      setStatus(`AI audio ready as storage:${storageId}.`);
    } catch (e) {
      updateAudioClip(clip.id, { ai: { ...clip.ai, status: "failed", error: (e as Error).message } });
      setStatus("AI generation failed: " + (e as Error).message);
    } finally {
      setAIBusy("");
    }
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="border-b border-border px-4 py-2 flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium truncate">{draft.name || selected?.name || "Untitled composition"}</div>
          <div className="text-xs text-text-dim flex items-center gap-2">
            <span>{clips.length + audioClips.length} clips</span>
            <span>{formatTime(totalDuration)}</span>
            <span>{draft.output.aspect}</span>
            <span>{draft.output.resolution}</span>
          </div>
        </div>
        <BindingStrip bindings={bindings} />
        <select
          value={executor}
          onChange={(e) => setExecutor(e.target.value as "auto" | "local" | "remote")}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
        >
          <option value="auto">auto</option>
          <option value="local">local</option>
          <option value="remote">remote</option>
        </select>
        <button onClick={save} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Save</button>
        <button
          onClick={render}
          disabled={rendering}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50"
        >
          {rendering ? "Rendering..." : "Render"}
        </button>
      </header>

      <div className="flex-1 min-h-0 flex">
        <Sidebar
          compositions={compositions}
          examples={examples}
          selectedId={selectedId}
          onSelect={setSelectedId}
          onNew={() => setSelectedId(null)}
          onLoadExample={loadExample}
        />

        <main className="flex-1 min-w-0 flex flex-col">
          <nav className="border-b border-border px-3 pt-2 flex gap-1 text-xs">
            <TabButton active={tab === "timeline"} onClick={() => setTab("timeline")}>Timeline</TabButton>
            <TabButton active={tab === "json"} onClick={() => setTab("json")}>JSON</TabButton>
          </nav>

          {tab === "timeline" ? (
            <div className="flex-1 min-h-0 flex">
              <section className="flex-1 min-w-0 flex flex-col p-4 gap-4 overflow-auto">
                <PreviewStage
                  clip={activeClip}
                  audioClips={audioClips}
                  asset={activeClip ? resolved[activeClip.asset.src] : undefined}
                  background={draft.background}
                  aspect={draft.output.aspect}
                  playing={playing}
                  playhead={playhead}
                  duration={totalDuration}
                  onToggle={() => setPlaying((v) => !v)}
                  onSeek={setPlayhead}
                  onAdd={addClip}
                  onBrowse={() => openPicker(clips.length ? { kind: "clip", clipId: clips[0].id } : { kind: "clip", clipId: "" })}
                />
                <Timeline
                  clips={clips}
                  audioClips={audioClips}
                  selectedClipId={selectedClipId}
                  playhead={playhead}
                  duration={totalDuration}
                  zoom={timelineZoom}
                  onZoom={setTimelineZoom}
                  onSelect={setSelectedClipId}
  onEditVisual={(id) => setClipEditor({ kind: "visual", id })}
  onEditAudio={(id) => setClipEditor({ kind: "audio", id })}
  onEditGap={(start, length) => editGapAsSilence(start, length)}
  onSeek={setPlayhead}
                  onAdd={addClip}
                  onAddAIVisual={addAIVisualClip}
                  onAddAIAudio={() => addAudioClip("music")}
                  onAddSilence={addSilenceClip}
                  onAddAISoundtrack={addAISoundtrack}
                  onBrowse={() => openPicker(clips.length ? { kind: "clip", clipId: clips[0].id } : { kind: "clip", clipId: "" })}
                />
                <RenderPreview render={selected?.latest_render || null} outputFormat={draft.output.format} onOpen={setLightbox} />
              </section>
              <Inspector
                draft={draft}
                clip={selectedClip}
                onDraft={updateDraft}
                onClip={updateClip}
                onAudioClip={updateAudioClip}
                onDelete={deleteClip}
                onDeleteAudio={deleteAudioClip}
                onMove={moveClip}
                onDeleteComposition={deleteSelected}
                canDeleteComposition={selectedId != null}
                onBrowseClip={(clipId) => openPicker({ kind: "clip", clipId })}
                onBrowseAudio={(clipId) => openPicker({ kind: "audio", clipId })}
                onBrowseSoundtrack={() => openPicker({ kind: "soundtrack" })}
                onAddClip={addClip}
                onAddAudioClip={addAudioClip}
                onAddSilenceClip={addSilenceClip}
                onAddAISoundtrack={addAISoundtrack}
                onAddAIVisualClip={addAIVisualClip}
                onGenerateClipAI={generateClipAI}
                onGenerateAudioClipAI={generateAudioClipAI}
                onGenerateSoundtrackAI={generateSoundtrackAI}
                aiBusy={aiBusy}
              />
            </div>
          ) : (
            <JSONEditor
              editText={jsonEdit}
              outputText={jsonOutput}
              onEdit={setJsonEdit}
              onOutput={setJsonOutput}
              onApply={applyJSON}
            />
          )}

          <footer className="border-t border-border px-3 py-1.5 text-xs text-text-dim min-h-8">{status}</footer>
        </main>
      </div>

      {lightbox && <Lightbox render={lightbox} outputFormat={draft.output.format} onClose={() => setLightbox(null)} />}
      {clipEditor && (
        <ClipEditorModal
          target={clipEditor}
          visualClip={clipEditor.kind === "visual" ? clips.find((clip) => clip.id === clipEditor.id) || null : null}
          audioClip={clipEditor.kind === "audio" ? audioClips.find((clip) => clip.id === clipEditor.id) || null : null}
          aspect={draft.output.aspect}
          aiBusy={aiBusy}
          onClose={() => setClipEditor(null)}
          onVisualClip={updateClip}
          onAudioClip={updateAudioClip}
          onBrowseVisual={(id) => openPicker({ kind: "clip", clipId: id })}
          onBrowseAudio={(id) => openPicker({ kind: "audio", clipId: id })}
          onGenerateVisual={generateClipAI}
          onGenerateAudio={generateAudioClipAI}
          onDeleteVisual={(id) => {
            deleteClip(id);
            setClipEditor(null);
          }}
          onDeleteAudio={(id) => {
            deleteAudioClip(id);
            setClipEditor(null);
          }}
        />
      )}
      {pickerTarget && (
        <StoragePicker
          files={storageFiles}
          loading={storageLoading}
          error={storageError}
          target={pickerTarget.kind}
          onClose={() => setPickerTarget(null)}
          onChoose={chooseStorageFile}
        />
      )}
    </div>
  );
}

function BindingStrip({ bindings }: { bindings: Bindings | null }) {
  if (!bindings) return null;
  const renderMode = bindings.render_executor
    ? bindings.render_executor
    : bindings.render_host_id > 0
      ? `host #${bindings.render_host_id}`
      : "local ffmpeg";
  return (
    <div className="hidden lg:flex items-center gap-2 text-xs text-text-dim">
      <Pill label={renderMode} active />
      <Pill label="storage" active={bindings.storage_bound} />
      <Pill label="media studio" active={bindings.mediastudio_bound} />
    </div>
  );
}

function Pill({ label, active }: { label: string; active: boolean }) {
  return (
    <span className={`px-1.5 py-0.5 rounded ${active ? "bg-accent/10 text-accent" : "bg-border text-text-muted"}`}>
      {label}
    </span>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`px-3 py-1.5 rounded-t border-b-2 ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:text-text"}`}
    >
      {children}
    </button>
  );
}

function Sidebar({
  compositions,
  examples,
  selectedId,
  onSelect,
  onNew,
  onLoadExample,
}: {
  compositions: Composition[];
  examples: DraftExample[];
  selectedId: number | null;
  onSelect: (id: number) => void;
  onNew: () => void;
  onLoadExample: (example: DraftExample) => void;
}) {
  return (
    <aside className="w-72 shrink-0 border-r border-border bg-bg-card flex flex-col">
      <header className="px-3 py-2 border-b border-border flex items-center gap-2">
        <span className="text-text-muted text-xs flex-1">Compositions</span>
        <button onClick={onNew} className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg">
          New
        </button>
      </header>
      <div className="flex-1 overflow-auto">
        {examples.length > 0 && (
          <section className="border-b border-border">
            <div className="px-3 py-2 text-[10px] uppercase tracking-wide text-text-dim">Examples</div>
            <div className="px-2 pb-2 space-y-1">
              {examples.map((example) => (
                <button
                  key={example.id}
                  type="button"
                  onClick={() => onLoadExample(example)}
                  className="w-full text-left px-2 py-2 rounded border border-border hover:bg-bg-input"
                >
                  <span className="block text-xs text-text font-medium truncate">{example.name}</span>
                  <span className="block text-[11px] text-text-dim leading-snug mt-0.5">{example.description}</span>
                </button>
              ))}
            </div>
          </section>
        )}
        {compositions.length === 0 && <div className="p-3 text-text-dim text-xs">No compositions.</div>}
        {compositions.map((c) => {
          const isSel = c.id === selectedId;
          const r = c.latest_render;
          return (
            <button
              key={c.id}
              onClick={() => onSelect(c.id)}
              className={`w-full text-left px-3 py-2 border-b border-border block hover:bg-bg-input/50 ${isSel ? "bg-bg-input" : ""}`}
            >
              <div className="text-sm text-text font-medium truncate">{c.name || `#${c.id}`}</div>
              <div className="text-xs text-text-dim mt-0.5 flex items-center gap-1.5">
                <span>{formatTime(c.duration_seconds)}</span>
                {r && <StatusPill status={r.status} />}
              </div>
            </button>
          );
        })}
      </div>
    </aside>
  );
}

function PreviewStage({
  clip,
  audioClips,
  asset,
  background,
  aspect,
  playing,
  playhead,
  duration,
  onToggle,
  onSeek,
  onAdd,
  onBrowse,
}: {
  clip: ClipDraft | null;
  audioClips: AudioClipDraft[];
  asset?: ResolvedAsset;
  background: string;
  aspect: Aspect;
  playing: boolean;
  playhead: number;
  duration: number;
  onToggle: () => void;
  onSeek: (t: number) => void;
  onAdd: () => void;
  onBrowse: () => void;
}) {
  const mediaRef = useRef<HTMLVideoElement | null>(null);
  const audioOnly = !clip && audioClips.length > 0;
  useEffect(() => {
    const video = mediaRef.current;
    if (!video) return;
    if (playing) {
      video.play().catch(() => {});
    } else {
      video.pause();
    }
  }, [playing, asset?.url]);

  const url = asset?.url || "";
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden">
      <div className="p-3 border-b border-border flex items-center gap-2">
        <button onClick={onToggle} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">
          {playing ? "Pause" : "Play"}
        </button>
        <input
          type="range"
          min={0}
          max={Math.max(0.1, duration)}
          step={0.1}
          value={Math.min(playhead, duration)}
          onChange={(e) => onSeek(Number(e.target.value))}
          className="flex-1"
          style={{ accentColor: "var(--apteva-accent, #4ade80)" }}
        />
        <span className="text-xs text-text-dim tabular-nums">{formatTime(playhead)} / {formatTime(duration)}</span>
      </div>
      <div className="p-4 flex items-center justify-center bg-bg">
        <div className="relative w-full max-w-4xl min-h-72 border border-border overflow-hidden" style={{ background, aspectRatio: aspectRatio(aspect) }}>
          {url && clip?.asset.type === "image" && (
            <img src={url} alt="" className="absolute inset-0 w-full h-full object-cover" />
          )}
          {url && clip?.asset.type !== "image" && (
            <video ref={mediaRef} src={url} muted className="absolute inset-0 w-full h-full object-cover" />
          )}
          {!url && (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
              {audioOnly ? (
                <>
                  <div className="text-text text-sm font-medium">Audio-only composition</div>
                  <div className="text-text-muted text-xs">
                    {audioClips.length} timed audio clips and silences · {formatTime(duration)}
                  </div>
                </>
              ) : (
                <div className="text-text-muted text-sm">{clip ? (clip.asset.src || "No clip source") : "No clips yet"}</div>
              )}
              {!clip && !audioOnly && (
                <div className="flex items-center gap-2">
                  <button type="button" onClick={onBrowse} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
                    Browse storage
                  </button>
                  <button type="button" onClick={onAdd} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">
                    Add empty clip
                  </button>
                </div>
              )}
            </div>
          )}
          {clip?.text?.body && (
            <div
              className={`absolute left-6 right-6 text-center font-medium ${clip.text.position === "top" ? "top-6" : clip.text.position === "center" ? "top-1/2 -translate-y-1/2" : "bottom-6"}`}
              style={{
                color: clip.text.color || "#ffffff",
                fontSize: Math.max(12, clip.text.font_size || 32),
                textShadow: "0 1px 3px rgba(0,0,0,.7)",
              }}
            >
              {clip.text.body}
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function shortPrompt(prompt: string | undefined, fallback: string): string {
  const text = (prompt || "").trim();
  if (!text) return fallback;
  return text.length > 46 ? text.slice(0, 43) + "..." : text;
}

function aiKindLabel(kind: string | undefined): string {
  switch (kind) {
    case "audio_tts": return "AI voice";
    case "audio_sfx": return "AI SFX";
    case "music": return "AI music";
    case "avatar": return "AI avatar";
    case "image": return "AI image";
    case "video": return "AI video";
    default: return "AI";
  }
}

function visualClipLabel(clip: ClipDraft): string {
  if (clip.ai) return `${aiKindLabel(clip.ai.media_kind)} - ${shortPrompt(clip.ai.prompt, clip.asset.src || "draft")}`;
  return clip.asset.src || "empty source";
}

function audioClipLabel(clip: AudioClipDraft): string {
  if (clip.asset.type === "silence") return "Silence";
  if (clip.ai) return `${aiKindLabel(clip.ai.media_kind)} - ${shortPrompt(clip.ai.prompt, clip.asset.src || "draft")}`;
  return clip.asset.src || "empty audio";
}

function audioClipSubtitle(clip: AudioClipDraft): string {
  const status = clip.ai?.status || clip.asset.type;
  const storage = clip.ai?.storage_id ? `storage:${clip.ai.storage_id}` : clip.asset.src;
  return `${status}${storage ? ` - ${storage}` : ""} - ${clip.length.toFixed(1)}s @ ${clip.start.toFixed(1)}s`;
}

function Timeline({
  clips,
  audioClips,
  selectedClipId,
  playhead,
  duration,
  zoom,
  onZoom,
  onSelect,
  onEditVisual,
  onEditAudio,
  onEditGap,
  onSeek,
  onAdd,
  onAddAIVisual,
  onAddAIAudio,
  onAddSilence,
  onAddAISoundtrack,
  onBrowse,
}: {
  clips: ClipDraft[];
  audioClips: AudioClipDraft[];
  selectedClipId: string;
  playhead: number;
  duration: number;
  zoom: number;
  onZoom: (zoom: number) => void;
  onSelect: (id: string) => void;
  onEditVisual: (id: string) => void;
  onEditAudio: (id: string) => void;
  onEditGap: (start: number, length: number) => void;
  onSeek: (t: number) => void;
  onAdd: () => void;
  onAddAIVisual: (kind: "image" | "video" | "avatar") => void;
  onAddAIAudio: () => void;
  onAddSilence: () => void;
  onAddAISoundtrack: () => void;
  onBrowse: () => void;
}) {
  const hasAny = clips.length > 0 || audioClips.length > 0;
  const hasVisual = clips.length > 0;
  const timelineHeight = hasVisual ? 192 : 128;
  const audioLabelTop = hasVisual ? 112 : 12;
  const audioTrackTop = hasVisual ? 128 : 32;
  const laneInset = 12;
  const laneWidth = Math.max(936, Math.ceil(Math.max(duration, 1) * 12 * Math.max(1, zoom)));
  const timelineWidth = laneWidth + laneInset * 2;
  const pxPerSecond = laneWidth / Math.max(duration, 1);
  const sortedAudio = [...audioClips].sort((a, b) => a.start - b.start);
  const gaps: { start: number; length: number }[] = [];
  let audioCursor = 0;
  for (const clip of sortedAudio) {
    if (clip.start - audioCursor > 0.05) {
      gaps.push({ start: audioCursor, length: clip.start - audioCursor });
    }
    audioCursor = Math.max(audioCursor, clip.start + clip.length);
  }
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden">
      <header className="px-3 py-2 border-b border-border flex items-center gap-2 flex-wrap">
        <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Timeline</h2>
        <div className="flex items-center gap-1 mr-2">
          <button type="button" onClick={() => onZoom(Math.max(1, Number((zoom / 1.5).toFixed(2))))} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input" title="Zoom out">-</button>
          <button type="button" onClick={() => onZoom(1)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input" title="Fit timeline">Fit</button>
          <button type="button" onClick={() => onZoom(Math.min(12, Number((zoom * 1.5).toFixed(2))))} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input" title="Zoom in">+</button>
        </div>
        <button onClick={() => onAddAIVisual("image")} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">AI image</button>
        <button onClick={() => onAddAIVisual("video")} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">AI video</button>
        <button onClick={() => onAddAIVisual("avatar")} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">AI avatar</button>
        <button onClick={onAddAISoundtrack} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">AI music</button>
        <button onClick={onAddAIAudio} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Timed AI audio</button>
        <button onClick={onAddSilence} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Silence</button>
        <button onClick={onBrowse} className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg">Browse storage</button>
        <button onClick={onAdd} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Add empty clip</button>
      </header>
      <div className="p-3">
        {!hasAny ? (
          <div className="border border-dashed border-border rounded bg-bg px-4 py-8 text-center">
            <div className="text-text-muted text-sm">Start with generated media, a file from Storage, or a blank clip.</div>
            <div className="mt-3 flex items-center justify-center gap-2 flex-wrap">
              <button type="button" onClick={() => onAddAIVisual("image")} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI image</button>
              <button type="button" onClick={() => onAddAIVisual("video")} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI video</button>
              <button type="button" onClick={() => onAddAIVisual("avatar")} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI avatar</button>
              <button type="button" onClick={onAddAISoundtrack} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI music</button>
              <button type="button" onClick={onAddAIAudio} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Timed AI audio</button>
              <button type="button" onClick={onAddSilence} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Silence</button>
              <button type="button" onClick={onBrowse} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">Browse storage</button>
            </div>
          </div>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <div
              role="presentation"
              onClick={(e) => {
                const rect = e.currentTarget.getBoundingClientRect();
                const x = Math.max(0, e.clientX - rect.left - laneInset);
                onSeek(Math.min(duration, x / pxPerSecond));
              }}
              className="border border-border rounded bg-bg"
              style={{ position: "relative", display: "block", minHeight: timelineHeight, width: timelineWidth, minWidth: timelineWidth, overflow: "hidden", cursor: "crosshair" }}
            >
              <div
                className="bg-accent"
                style={{ position: "absolute", top: 0, bottom: 0, left: laneInset + Math.min(laneWidth, Math.max(0, playhead) * pxPerSecond), width: 1 }}
              />
              {hasVisual && (
                <>
                  <div className="text-[10px] uppercase tracking-wide text-text-dim" style={{ position: "absolute", left: laneInset, top: 12 }}>Visual</div>
                  <div style={{ position: "absolute", left: laneInset, top: 32, width: laneWidth, height: 64, display: "flex" }}>
                    {clips.map((clip) => {
                      const width = Math.max(64, clip.length * pxPerSecond);
                      const selected = clip.id === selectedClipId;
                      return (
                        <div
                          key={clip.id}
                          role="button"
                          tabIndex={0}
                          title={clip.ai?.prompt || clip.asset.src || "empty source"}
                          onClick={(e) => {
                            e.stopPropagation();
                            onSelect(clip.id);
                            onSeek(clip.start);
                            onEditVisual(clip.id);
                          }}
                          className={`border text-xs ${selected ? "border-accent bg-accent/10" : "border-border bg-bg-input hover:border-accent"}`}
                          style={{ width, minWidth: width, height: 64, display: "flex", flexDirection: "column", justifyContent: "center", padding: "0 8px", overflow: "hidden" }}
                        >
                          <span className="block text-text truncate leading-5">{visualClipLabel(clip)}</span>
                          <span className="block text-text-dim truncate leading-5">{clip.ai?.status || clip.asset.type} - {clip.length.toFixed(1)}s</span>
                        </div>
                      );
                    })}
                  </div>
                </>
              )}
              <div className="text-[10px] uppercase tracking-wide text-text-dim" style={{ position: "absolute", left: laneInset, top: audioLabelTop }}>Audio</div>
              <div style={{ position: "absolute", left: laneInset, top: audioTrackTop, width: laneWidth, height: 64 }}>
                {gaps.map((gap, index) => {
                  const left = Math.max(0, gap.start * pxPerSecond);
                  const width = Math.max(16, gap.length * pxPerSecond);
                  return (
                    <div
                      key={`gap-${index}-${gap.start}`}
                      role="button"
                      tabIndex={0}
                      onClick={(e) => {
                        e.stopPropagation();
                        onSeek(gap.start);
                        onEditGap(gap.start, gap.length);
                      }}
                      className="border border-dashed border-border text-xs bg-bg-card text-text-dim hover:border-accent hover:text-text"
                      style={{ position: "absolute", left, top: 0, width, minWidth: width, height: 64, display: "flex", flexDirection: "column", justifyContent: "center", padding: "0 8px", overflow: "hidden", cursor: "pointer" }}
                      title={`Implicit silence from ${gap.start.toFixed(1)}s for ${gap.length.toFixed(1)}s`}
                    >
                      <span className="block truncate leading-5">Gap</span>
                      <span className="block truncate leading-5">{gap.length.toFixed(1)}s</span>
                    </div>
                  );
                })}
                {audioClips.map((clip) => {
                  const left = Math.max(0, clip.start * pxPerSecond);
                  const width = Math.max(64, clip.length * pxPerSecond);
                  const selected = clip.id === selectedClipId;
                  return (
                    <div
                      key={clip.id}
                      role="button"
                      tabIndex={0}
                      title={clip.ai?.prompt || clip.asset.src || "empty audio"}
                      onClick={(e) => {
                        e.stopPropagation();
                        onSelect(clip.id);
                        onSeek(clip.start);
                        onEditAudio(clip.id);
                      }}
                      className={`border text-xs ${selected ? "border-accent bg-accent/10" : "border-border bg-bg-input hover:border-accent"}`}
                      style={{ position: "absolute", left, top: 0, width, minWidth: width, height: 64, display: "flex", flexDirection: "column", justifyContent: "center", padding: "0 8px", overflow: "hidden" }}
                    >
                      <span className="block text-text truncate leading-5">{audioClipLabel(clip)}</span>
                      <span className="block text-text-dim truncate leading-5">{audioClipSubtitle(clip)}</span>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function ClipEditorModal({
  target,
  visualClip,
  audioClip,
  aspect,
  aiBusy,
  onClose,
  onVisualClip,
  onAudioClip,
  onBrowseVisual,
  onBrowseAudio,
  onGenerateVisual,
  onGenerateAudio,
  onDeleteVisual,
  onDeleteAudio,
}: {
  target: ClipEditorTarget;
  visualClip: ClipDraft | null;
  audioClip: AudioClipDraft | null;
  aspect: Aspect;
  aiBusy: string;
  onClose: () => void;
  onVisualClip: (id: string, patch: Partial<ClipDraft>) => void;
  onAudioClip: (id: string, patch: Partial<AudioClipDraft>) => void;
  onBrowseVisual: (id: string) => void;
  onBrowseAudio: (id: string) => void;
  onGenerateVisual: (clip: ClipDraft) => void;
  onGenerateAudio: (clip: AudioClipDraft) => void;
  onDeleteVisual: (id: string) => void;
  onDeleteAudio: (id: string) => void;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const missing = target.kind === "visual" ? !visualClip : !audioClip;
  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center p-6" style={{ zIndex: 9997 }} onClick={onClose}>
      <div className="bg-bg border border-border rounded shadow-xl w-full max-w-2xl max-h-[88vh] overflow-auto" onClick={(e) => e.stopPropagation()}>
        <header className="px-4 py-3 border-b border-border flex items-center gap-2">
          <div className="min-w-0 flex-1">
            <div className="text-sm text-text font-medium">{target.kind === "visual" ? "Edit visual clip" : "Edit audio clip"}</div>
            <div className="text-xs text-text-dim">Storage, URL, or Media Studio generated source</div>
          </div>
          <button type="button" onClick={onClose} className="text-text-dim hover:text-text px-2 text-lg leading-none">x</button>
        </header>
        {missing ? (
          <div className="p-4 text-sm text-text-muted">Clip not found.</div>
        ) : target.kind === "visual" && visualClip ? (
          <div className="p-4 space-y-4">
            <div className="grid grid-cols-2 gap-2">
              <Field label="Type">
                <select
                  value={visualClip.asset.type}
                  onChange={(e) => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: e.target.value as AssetType } })}
                  className={field}
                >
                  <option value="video">Video</option>
                  <option value="image">Image</option>
                </select>
              </Field>
              <Field label="Length">
                <input type="number" min={0.1} step={0.1} value={visualClip.length} onChange={(e) => onVisualClip(visualClip.id, { length: Number(e.target.value) })} className={field} />
              </Field>
              {visualClip.ai && (
                <Field label="Duration mode">
                  <select value={visualClip.duration_mode || defaultDurationMode(visualClip.ai.media_kind)} onChange={(e) => onVisualClip(visualClip.id, { duration_mode: e.target.value as DurationMode })} className={field}>
                    <option value="fit_generated_reflow">Fit + reflow</option>
                    <option value="fit_generated_keep_start">Fit, keep start</option>
                    <option value="fit_generated">Fit generated</option>
                    <option value="fixed_trim_pad">Keep slot</option>
                  </select>
                </Field>
              )}
            </div>
            <Field label="Source">
              <div className="flex gap-2">
                <input
                  value={visualClip.asset.src}
                  onChange={(e) => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, src: e.target.value } })}
                  placeholder="storage:1, mediastudio:4, or https://..."
                  className={field}
                />
                <button type="button" onClick={() => onBrowseVisual(visualClip.id)} className="px-2 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
                  Browse
                </button>
              </div>
            </Field>
            {!visualClip.ai && (
              <div className="grid grid-cols-3 gap-2">
                <button type="button" onClick={() => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: "image" }, duration_mode: defaultDurationMode("image"), ai: defaultAI("image", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate image</button>
                <button type="button" onClick={() => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: "video" }, duration_mode: defaultDurationMode("video"), ai: defaultAI("video", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate video</button>
                <button type="button" onClick={() => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: "video" }, duration_mode: defaultDurationMode("avatar"), ai: defaultAI("avatar", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate avatar</button>
              </div>
            )}
            {visualClip.ai && (
              <AIAssetEditor
                title="AI source"
                ai={visualClip.ai}
                allowedKinds={["image", "video", "avatar"]}
                busy={aiBusy === visualClip.id}
                onChange={(ai) => {
                  const mode = visualClip.duration_mode || defaultDurationMode(ai.media_kind);
                  const estimate = estimateForAI(ai);
                  onVisualClip(visualClip.id, {
                    ai,
                    duration_mode: mode,
                    estimated_length: estimate || visualClip.estimated_length,
                    length: fitsGenerated(mode) ? (estimate || visualClip.length) : visualClip.length,
                    asset: { ...visualClip.asset, type: ai.media_kind === "image" ? "image" : "video" },
                  });
                }}
                onGenerate={() => onGenerateVisual(visualClip)}
                onClear={() => onVisualClip(visualClip.id, { ai: undefined })}
              />
            )}
            <Field label="Text overlay">
              <textarea
                value={visualClip.text?.body || ""}
                onChange={(e) => onVisualClip(visualClip.id, { text: e.target.value ? { ...(visualClip.text || {}), body: e.target.value, position: visualClip.text?.position || "bottom" } : undefined })}
                className={`${field} resize-y`}
                rows={3}
              />
            </Field>
            <div className="flex justify-between gap-2">
              <button type="button" onClick={() => onDeleteVisual(visualClip.id)} className="text-sm px-3 py-1.5 border border-red/50 text-red rounded hover:bg-red/10">Delete clip</button>
              <button type="button" onClick={onClose} className="text-sm px-3 py-1.5 border border-border rounded hover:bg-bg-input">Done</button>
            </div>
          </div>
        ) : audioClip ? (
          <div className="p-4 space-y-4">
            {audioClip.asset.type !== "silence" && (
              <Field label="Source">
                <div className="flex gap-2">
                  <input
                    value={audioClip.asset.src}
                    onChange={(e) => onAudioClip(audioClip.id, { asset: { type: "audio", src: e.target.value } })}
                    placeholder="storage:1 or https://..."
                    className={field}
                  />
                  <button type="button" onClick={() => onBrowseAudio(audioClip.id)} className="px-2 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
                    Browse
                  </button>
                </div>
              </Field>
            )}
            <div className="grid grid-cols-3 gap-2">
              <Field label="Type">
                <select value={audioClip.asset.type} onChange={(e) => onAudioClip(audioClip.id, { asset: { type: e.target.value as "audio" | "silence", src: e.target.value === "silence" ? "" : audioClip.asset.src } })} className={field}>
                  <option value="audio">Audio</option>
                  <option value="silence">Silence</option>
                </select>
              </Field>
              <Field label="Start">
                <input type="number" min={0} step={0.1} value={audioClip.start} onChange={(e) => onAudioClip(audioClip.id, { start: Number(e.target.value) })} className={field} />
              </Field>
              <Field label="Length">
                <input type="number" min={0.1} step={0.1} value={audioClip.length} onChange={(e) => onAudioClip(audioClip.id, { length: Number(e.target.value) })} className={field} />
              </Field>
              <Field label="Volume">
                <input type="number" min={0} max={1} step={0.05} value={audioClip.volume} onChange={(e) => onAudioClip(audioClip.id, { volume: Number(e.target.value) })} className={field} />
              </Field>
              {audioClip.ai && (
                <Field label="Duration mode">
                  <select value={audioClip.duration_mode || defaultDurationMode(audioClip.ai.media_kind)} onChange={(e) => onAudioClip(audioClip.id, { duration_mode: e.target.value as DurationMode })} className={field}>
                    <option value="fit_generated_reflow">Fit + reflow</option>
                    <option value="fit_generated_keep_start">Fit, keep start</option>
                    <option value="fit_generated">Fit generated</option>
                    <option value="fixed_trim_pad">Keep slot</option>
                  </select>
                </Field>
              )}
            </div>
            {audioClip.asset.type !== "silence" && (
              <AudioProcessingEditor clip={audioClip} onChange={(patch) => onAudioClip(audioClip.id, patch)} />
            )}
            {audioClip.asset.type !== "silence" && !audioClip.ai && (
              <div className="grid grid-cols-3 gap-2">
                <button type="button" onClick={() => onAudioClip(audioClip.id, { duration_mode: defaultDurationMode("music"), ai: defaultAI("music", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate music</button>
                <button type="button" onClick={() => onAudioClip(audioClip.id, { duration_mode: defaultDurationMode("audio_tts"), ai: defaultAI("audio_tts", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate TTS</button>
                <button type="button" onClick={() => onAudioClip(audioClip.id, { duration_mode: defaultDurationMode("audio_sfx"), ai: defaultAI("audio_sfx", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate SFX</button>
              </div>
            )}
            {audioClip.ai && (
              <AIAssetEditor
                title="AI audio"
                ai={audioClip.ai}
                allowedKinds={["music", "audio_tts", "audio_sfx"]}
                busy={aiBusy === audioClip.id}
                onChange={(ai) => {
                  const mode = audioClip.duration_mode || defaultDurationMode(ai.media_kind);
                  const estimate = estimateForAI(ai);
                  onAudioClip(audioClip.id, {
                    ai,
                    duration_mode: mode,
                    estimated_length: estimate || audioClip.estimated_length,
                    length: fitsGenerated(mode) ? (estimate || audioClip.length) : audioClip.length,
                  });
                }}
                onGenerate={() => onGenerateAudio(audioClip)}
                onClear={() => onAudioClip(audioClip.id, { ai: undefined })}
              />
            )}
            <div className="flex justify-between gap-2">
              <button type="button" onClick={() => onDeleteAudio(audioClip.id)} className="text-sm px-3 py-1.5 border border-red/50 text-red rounded hover:bg-red/10">Delete clip</button>
              <button type="button" onClick={onClose} className="text-sm px-3 py-1.5 border border-border rounded hover:bg-bg-input">Done</button>
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}

function Inspector({
  draft,
  clip,
  onDraft,
  onClip,
  onAudioClip,
  onDelete,
  onDeleteAudio,
  onMove,
  onDeleteComposition,
  canDeleteComposition,
  onBrowseClip,
  onBrowseAudio,
  onBrowseSoundtrack,
  onAddClip,
  onAddAudioClip,
  onAddSilenceClip,
  onAddAISoundtrack,
  onAddAIVisualClip,
  onGenerateClipAI,
  onGenerateAudioClipAI,
  onGenerateSoundtrackAI,
  aiBusy,
}: {
  draft: DraftState;
  clip: ClipDraft | null;
  onDraft: (fn: (draft: DraftState) => DraftState) => void;
  onClip: (id: string, patch: Partial<ClipDraft>) => void;
  onAudioClip: (id: string, patch: Partial<AudioClipDraft>) => void;
  onDelete: (id: string) => void;
  onDeleteAudio: (id: string) => void;
  onMove: (id: string, dir: -1 | 1) => void;
  onDeleteComposition: () => void;
  canDeleteComposition: boolean;
  onBrowseClip: (clipId: string) => void;
  onBrowseAudio: (clipId: string) => void;
  onBrowseSoundtrack: () => void;
  onAddClip: () => void;
  onAddAudioClip: (ai?: boolean | MediaKind) => void;
  onAddSilenceClip: () => void;
  onAddAISoundtrack: () => void;
  onAddAIVisualClip: (kind: "image" | "video" | "avatar") => void;
  onGenerateClipAI: (clip: ClipDraft) => void;
  onGenerateAudioClipAI: (clip: AudioClipDraft) => void;
  onGenerateSoundtrackAI: () => void;
  aiBusy: string;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  return (
    <aside className="w-72 2xl:w-80 shrink-0 border-l border-border bg-bg-card overflow-auto">
      <div className="p-4 space-y-5">
        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">Composition</h2>
          <input
            value={draft.name}
            onChange={(e) => onDraft((cur) => ({ ...cur, name: e.target.value }))}
            placeholder="Name"
            className={field}
          />
          <div className="grid grid-cols-2 gap-2">
            <Field label="Format">
              <select value={draft.output.format} onChange={(e) => onDraft((cur) => ({ ...cur, output: { ...cur.output, format: e.target.value as OutputFormat } }))} className={field}>
                <option value="mp4">MP4 video</option>
                <option value="mp3">MP3 audio</option>
                <option value="wav">WAV audio</option>
                <option value="m4a">M4A audio</option>
                <option value="aac">AAC audio</option>
              </select>
            </Field>
            <Field label="Aspect">
              <select value={draft.output.aspect} onChange={(e) => onDraft((cur) => ({ ...cur, output: { ...cur.output, aspect: e.target.value as Aspect } }))} className={field}>
                <option value="16:9">16:9</option>
                <option value="9:16">9:16</option>
                <option value="1:1">1:1</option>
                <option value="4:3">4:3</option>
              </select>
            </Field>
            <Field label="Resolution">
              <select value={draft.output.resolution} onChange={(e) => onDraft((cur) => ({ ...cur, output: { ...cur.output, resolution: e.target.value as OutputDraft["resolution"] } }))} className={field}>
                <option value="sd">SD</option>
                <option value="hd">HD</option>
                <option value="fullhd">Full HD</option>
                <option value="4k">4K</option>
              </select>
            </Field>
            <Field label="FPS">
              <select value={draft.output.fps} onChange={(e) => onDraft((cur) => ({ ...cur, output: { ...cur.output, fps: Number(e.target.value) as OutputDraft["fps"] } }))} className={field}>
                <option value={24}>24</option>
                <option value={30}>30</option>
                <option value={60}>60</option>
              </select>
            </Field>
            <Field label="Background">
              <input type="color" value={draft.background} onChange={(e) => onDraft((cur) => ({ ...cur, background: e.target.value }))} className="w-full h-9 bg-bg-input border border-border rounded" />
            </Field>
          </div>
          <Field label="Soundtrack">
            <div className="flex gap-2">
              <input
                value={draft.soundtrack?.src || ""}
                onChange={(e) => onDraft((cur) => ({
                  ...cur,
                  soundtrack: e.target.value || cur.soundtrack?.ai
                    ? { src: e.target.value, volume: cur.soundtrack?.volume ?? 1, ai: cur.soundtrack?.ai }
                    : null,
                }))}
                placeholder="storage:99 or https://..."
                className={field}
              />
              <button type="button" onClick={onBrowseSoundtrack} className="px-2 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
                Browse
              </button>
            </div>
          </Field>
          <button
            type="button"
            onClick={onAddAISoundtrack}
            className="w-full text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input"
          >
            Add AI music soundtrack
          </button>
          {draft.soundtrack?.ai && (
            <AIAssetEditor
              title="AI soundtrack"
              ai={draft.soundtrack.ai}
              allowedKinds={["music", "audio_tts", "audio_sfx"]}
              busy={aiBusy === "soundtrack"}
              onChange={(ai) => onDraft((cur) => ({
                ...cur,
                soundtrack: { src: cur.soundtrack?.src || "", volume: cur.soundtrack?.volume ?? 1, ai },
              }))}
              onGenerate={onGenerateSoundtrackAI}
              onClear={() => onDraft((cur) => ({
                ...cur,
                soundtrack: cur.soundtrack ? { src: cur.soundtrack.src, volume: cur.soundtrack.volume } : null,
              }))}
            />
          )}
          {draft.soundtrack && (
            <Field label={`Volume ${draft.soundtrack.volume.toFixed(2)}`}>
              <input
                type="range"
                min={0}
                max={1}
                step={0.05}
                value={draft.soundtrack.volume}
                onChange={(e) => onDraft((cur) => ({ ...cur, soundtrack: cur.soundtrack ? { ...cur.soundtrack, volume: Number(e.target.value) } : null }))}
                className="w-full"
                style={{ accentColor: "var(--apteva-accent, #4ade80)" }}
              />
            </Field>
          )}
          <div className="grid grid-cols-2 gap-2">
            <button type="button" onClick={() => onAddAIVisualClip("image")} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              AI image
            </button>
            <button type="button" onClick={() => onAddAIVisualClip("video")} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              AI video
            </button>
            <button type="button" onClick={() => onAddAIVisualClip("avatar")} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              AI avatar
            </button>
            <button type="button" onClick={onAddAISoundtrack} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              AI music
            </button>
            <button type="button" onClick={() => onAddAudioClip("music")} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              Timed AI audio
            </button>
            <button type="button" onClick={onAddSilenceClip} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              Silence
            </button>
          </div>
        </section>

        <section className="space-y-2">
          <div className="flex items-center gap-2">
            <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Audio clips</h2>
            <button type="button" onClick={() => onAddAudioClip(true)} className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              AI audio
            </button>
            <button type="button" onClick={() => onAddAudioClip(false)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">
              Empty
            </button>
            <button type="button" onClick={onAddSilenceClip} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">
              Silence
            </button>
          </div>
          {draft.audioClips.length === 0 ? (
            <div className="border border-dashed border-border rounded p-3 text-sm text-text-muted">
              No timed audio clips.
            </div>
          ) : (
            <div className="space-y-3">
              {draft.audioClips.map((audio) => (
                <div key={audio.id} className="border border-border rounded p-3 space-y-2 bg-bg">
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-text-dim flex-1 truncate">{audio.asset.type === "silence" ? "silence" : audio.asset.src || (audio.ai ? `AI ${audio.ai.media_kind}` : "empty audio")}</span>
                    {audio.ai?.status && <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">{audio.ai.status}</span>}
                    <button type="button" onClick={() => onDeleteAudio(audio.id)} className="text-xs px-2 py-1 border border-red/50 text-red rounded hover:bg-red/10">
                      Delete
                    </button>
                  </div>
                  {audio.asset.type !== "silence" && (
                    <Field label="Source">
                      <div className="flex gap-2">
                        <input
                          value={audio.asset.src}
                          onChange={(e) => onAudioClip(audio.id, { asset: { type: "audio", src: e.target.value } })}
                          placeholder="storage:1 or https://..."
                          className={field}
                        />
                        <button type="button" onClick={() => onBrowseAudio(audio.id)} className="px-2 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
                          Browse
                        </button>
                      </div>
                    </Field>
                  )}
                  <div className="grid grid-cols-3 gap-2">
                    <Field label="Type">
                      <select value={audio.asset.type} onChange={(e) => onAudioClip(audio.id, { asset: { type: e.target.value as "audio" | "silence", src: e.target.value === "silence" ? "" : audio.asset.src } })} className={field}>
                        <option value="audio">Audio</option>
                        <option value="silence">Silence</option>
                      </select>
                    </Field>
                    <Field label="Start">
                      <input type="number" min={0} step={0.1} value={audio.start} onChange={(e) => onAudioClip(audio.id, { start: Number(e.target.value) })} className={field} />
                    </Field>
                    <Field label="Length">
                      <input type="number" min={0.1} step={0.1} value={audio.length} onChange={(e) => onAudioClip(audio.id, { length: Number(e.target.value) })} className={field} />
                    </Field>
                    <Field label="Volume">
                      <input type="number" min={0} max={1} step={0.05} value={audio.volume} onChange={(e) => onAudioClip(audio.id, { volume: Number(e.target.value) })} className={field} />
                    </Field>
                    {audio.ai && (
                      <Field label="Duration mode">
                        <select value={audio.duration_mode || defaultDurationMode(audio.ai.media_kind)} onChange={(e) => onAudioClip(audio.id, { duration_mode: e.target.value as DurationMode })} className={field}>
                          <option value="fit_generated_reflow">Fit + reflow</option>
                          <option value="fit_generated_keep_start">Fit, keep start</option>
                          <option value="fit_generated">Fit generated</option>
                          <option value="fixed_trim_pad">Keep slot</option>
                        </select>
                      </Field>
                    )}
                  </div>
                  {audio.asset.type !== "silence" && !audio.ai && (
                    <button
                      type="button"
                      onClick={() => onAudioClip(audio.id, { duration_mode: defaultDurationMode("music"), ai: defaultAI("music", draft.output.aspect) })}
                      className="w-full text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input"
                    >
                      Add AI source
                    </button>
                  )}
                  {audio.asset.type !== "silence" && (
                    <AudioProcessingEditor clip={audio} onChange={(patch) => onAudioClip(audio.id, patch)} />
                  )}
                  {audio.ai && (
                    <AIAssetEditor
                      title="AI audio"
                      ai={audio.ai}
                      allowedKinds={["music", "audio_tts", "audio_sfx"]}
                      busy={aiBusy === audio.id}
                      onChange={(ai) => {
                        const mode = audio.duration_mode || defaultDurationMode(ai.media_kind);
                        const estimate = estimateForAI(ai);
                        onAudioClip(audio.id, {
                          ai,
                          duration_mode: mode,
                          estimated_length: estimate || audio.estimated_length,
                          length: fitsGenerated(mode) ? (estimate || audio.length) : audio.length,
                        });
                      }}
                      onGenerate={() => onGenerateAudioClipAI(audio)}
                      onClear={() => onAudioClip(audio.id, { ai: undefined })}
                    />
                  )}
                </div>
              ))}
            </div>
          )}
        </section>

        {!clip && (
          <section className="space-y-2">
            <h2 className="text-xs uppercase tracking-wide text-text-dim">Selected clip</h2>
            <div className="border border-dashed border-border rounded p-3 text-sm text-text-muted">
              No clip selected.
            </div>
            <button onClick={() => onBrowseClip("")} className="w-full text-sm px-3 py-1.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              Browse storage
            </button>
            <button onClick={onAddClip} className="w-full text-sm px-3 py-1.5 border border-border rounded hover:bg-bg-input">
              Add empty clip
            </button>
          </section>
        )}

        {clip && (
          <section className="space-y-2">
            <div className="flex items-center gap-2">
              <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Selected clip</h2>
              <button onClick={() => onMove(clip.id, -1)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Left</button>
              <button onClick={() => onMove(clip.id, 1)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Right</button>
            </div>
            <Field label="Source">
              <div className="flex gap-2">
                <input
                  value={clip.asset.src}
                  onChange={(e) => onClip(clip.id, { asset: { ...clip.asset, src: e.target.value } })}
                  placeholder="storage:1, mediastudio:4, or https://..."
                  className={field}
                />
                <button type="button" onClick={() => onBrowseClip(clip.id)} className="px-2 py-1.5 text-xs border border-border rounded hover:bg-bg-input">
                  Browse
                </button>
              </div>
            </Field>
            <button
              type="button"
              onClick={() => {
                const kind = clip.asset.type === "image" ? "image" : "video";
                onClip(clip.id, { duration_mode: defaultDurationMode(kind), ai: clip.ai || defaultAI(kind, draft.output.aspect) });
              }}
              className="w-full text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input"
            >
              Add AI source
            </button>
            {clip.ai && (
              <AIAssetEditor
                title="AI source"
                ai={clip.ai}
                allowedKinds={["video", "image", "avatar"]}
                busy={aiBusy === clip.id}
                onChange={(ai) => {
                  const mode = clip.duration_mode || defaultDurationMode(ai.media_kind);
                  const estimate = estimateForAI(ai);
                  onClip(clip.id, {
                    ai,
                    duration_mode: mode,
                    estimated_length: estimate || clip.estimated_length,
                    length: fitsGenerated(mode) ? (estimate || clip.length) : clip.length,
                    asset: { ...clip.asset, type: ai.media_kind === "image" ? "image" : "video" },
                  });
                }}
                onGenerate={() => onGenerateClipAI(clip)}
                onClear={() => onClip(clip.id, { ai: undefined })}
              />
            )}
            <div className="grid grid-cols-2 gap-2">
              <Field label="Type">
                <select value={clip.asset.type} onChange={(e) => onClip(clip.id, { asset: { ...clip.asset, type: e.target.value as AssetType } })} className={field}>
                  <option value="video">Video</option>
                  <option value="image">Image</option>
                </select>
              </Field>
              <Field label="Length">
                <input type="number" min={0.1} step={0.1} value={clip.length} onChange={(e) => onClip(clip.id, { length: Number(e.target.value) })} className={field} />
              </Field>
              {clip.ai && (
                <Field label="Duration mode">
                  <select value={clip.duration_mode || defaultDurationMode(clip.ai.media_kind)} onChange={(e) => onClip(clip.id, { duration_mode: e.target.value as DurationMode })} className={field}>
                    <option value="fit_generated_reflow">Fit + reflow</option>
                    <option value="fit_generated_keep_start">Fit, keep start</option>
                    <option value="fit_generated">Fit generated</option>
                    <option value="fixed_trim_pad">Keep slot</option>
                  </select>
                </Field>
              )}
              <Field label="Transition in">
                <select value={clip.transition?.in || "none"} onChange={(e) => onClip(clip.id, { transition: { ...clip.transition, in: e.target.value } })} className={field}>
                  <option value="none">None</option>
                  <option value="fade">Fade</option>
                </select>
              </Field>
              <Field label="Transition out">
                <select value={clip.transition?.out || "none"} onChange={(e) => onClip(clip.id, { transition: { ...clip.transition, out: e.target.value } })} className={field}>
                  <option value="none">None</option>
                  <option value="fade">Fade</option>
                </select>
              </Field>
            </div>
            <Field label="Text overlay">
              <textarea
                value={clip.text?.body || ""}
                onChange={(e) => onClip(clip.id, { text: e.target.value ? { ...(clip.text || {}), body: e.target.value, position: clip.text?.position || "bottom" } : undefined })}
                className={`${field} resize-y`}
                rows={3}
              />
            </Field>
            {clip.text?.body && (
              <div className="grid grid-cols-3 gap-2">
                <Field label="Position">
                  <select value={clip.text.position || "bottom"} onChange={(e) => onClip(clip.id, { text: { ...clip.text!, position: e.target.value as "top" | "center" | "bottom" } })} className={field}>
                    <option value="top">Top</option>
                    <option value="center">Center</option>
                    <option value="bottom">Bottom</option>
                  </select>
                </Field>
                <Field label="Size">
                  <input type="number" min={12} max={96} value={clip.text.font_size || 32} onChange={(e) => onClip(clip.id, { text: { ...clip.text!, font_size: Number(e.target.value) } })} className={field} />
                </Field>
                <Field label="Color">
                  <input type="color" value={clip.text.color || "#ffffff"} onChange={(e) => onClip(clip.id, { text: { ...clip.text!, color: e.target.value } })} className="w-full h-9 bg-bg-input border border-border rounded" />
                </Field>
              </div>
            )}
            <button onClick={() => onDelete(clip.id)} className="w-full text-sm px-3 py-1.5 border border-red/50 text-red rounded hover:bg-red/10">
              Delete clip
            </button>
          </section>
        )}

        {canDeleteComposition && (
          <button onClick={onDeleteComposition} className="w-full text-sm px-3 py-1.5 border border-border text-text-muted rounded hover:bg-bg-input">
            Delete composition
          </button>
        )}
      </div>
    </aside>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="text-text-muted text-xs block mb-1">{label}</span>
      {children}
    </label>
  );
}

function AudioProcessingEditor({
  clip,
  onChange,
}: {
  clip: AudioClipDraft;
  onChange: (patch: Partial<AudioClipDraft>) => void;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const fx = clip.audio || {};
  const updateFX = (patch: Partial<AudioFX>) => onChange({ audio: { ...fx, ...patch } });
  return (
    <div className="border border-border rounded p-2 space-y-2">
      <div className="text-[10px] uppercase tracking-wide text-text-dim">Audio processing</div>
      <div className="grid grid-cols-3 gap-2">
        <Field label="Gain dB">
          <input type="number" min={-60} max={24} step={0.5} value={fx.gain_db ?? 0} onChange={(e) => updateFX({ gain_db: Number(e.target.value) || undefined })} className={field} />
        </Field>
        <Field label="Fade in">
          <input type="number" min={0} step={0.1} value={fx.fade_in_seconds ?? 0} onChange={(e) => updateFX({ fade_in_seconds: Number(e.target.value) || undefined })} className={field} />
        </Field>
        <Field label="Fade out">
          <input type="number" min={0} step={0.1} value={fx.fade_out_seconds ?? 0} onChange={(e) => updateFX({ fade_out_seconds: Number(e.target.value) || undefined })} className={field} />
        </Field>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <label className="flex items-center gap-2 text-xs text-text-muted">
          <input type="checkbox" checked={!!fx.normalize} onChange={(e) => updateFX({ normalize: e.target.checked || undefined })} />
          Normalize
        </label>
        <label className="flex items-center gap-2 text-xs text-text-muted">
          <input type="checkbox" checked={!!fx.trim_silence} onChange={(e) => updateFX({ trim_silence: e.target.checked || undefined })} />
          Trim silence
        </label>
      </div>
      {clip.ai?.audio_analysis && (
        <div className="text-[11px] text-text-dim">
          actual {clip.ai.audio_analysis.duration_seconds?.toFixed(1)}s
          {clip.ai.audio_analysis.peak_db ? ` · peak ${clip.ai.audio_analysis.peak_db.toFixed(1)} dB` : ""}
          {clip.ai.audio_analysis.rms_db ? ` · rms ${clip.ai.audio_analysis.rms_db.toFixed(1)} dB` : ""}
        </div>
      )}
    </div>
  );
}

function AIAssetEditor({
  title,
  ai,
  allowedKinds,
  busy,
  onChange,
  onGenerate,
  onClear,
}: {
  title: string;
  ai: AIAsset;
  allowedKinds: MediaKind[];
  busy: boolean;
  onChange: (ai: AIAsset) => void;
  onGenerate: () => void;
  onClear: () => void;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const generationInputKeys = new Set([
    "media_kind",
    "prompt",
    "model",
    "size",
    "duration",
    "aspect",
    "voice",
    "avatar",
    "source_image",
    "options",
  ]);
  const update = (patch: Partial<AIAsset>) => {
    const changesGenerationInput = Object.keys(patch).some((key) => generationInputKeys.has(key));
    const resetGeneratedState = changesGenerationInput && patch.status === undefined;
    const next = {
      ...ai,
      ...patch,
      status: resetGeneratedState ? "draft" : patch.status || ai.status || "draft",
      cache_key: resetGeneratedState ? undefined : ai.cache_key,
      storage_id: resetGeneratedState ? undefined : ai.storage_id,
      generation_id: resetGeneratedState ? undefined : ai.generation_id,
      job_id: resetGeneratedState ? undefined : ai.job_id,
      actual_duration_seconds: resetGeneratedState ? undefined : ai.actual_duration_seconds,
      audio_analysis: resetGeneratedState ? undefined : ai.audio_analysis,
      peak_db: resetGeneratedState ? undefined : ai.peak_db,
      rms_db: resetGeneratedState ? undefined : ai.rms_db,
      error: resetGeneratedState ? "" : ai.error,
    };
    onChange(withDurationEstimate(next));
  };
  const [models, setModels] = useState<{ id: string; name?: string }[]>([]);
  const [voices, setVoices] = useState<{ id: string; name?: string; language?: string }[]>([]);
  const [avatars, setAvatars] = useState<{ id: string; name?: string }[]>([]);

  useEffect(() => {
    let cancelled = false;
    fetch(`/api/apps/media-studio/models?kind=${encodeURIComponent(ai.media_kind)}`, { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (!cancelled) setModels(Array.isArray(data?.models) ? data.models : []);
      })
      .catch(() => !cancelled && setModels([]));
    if (ai.media_kind === "audio_tts" || ai.media_kind === "avatar") {
      fetch(`/api/apps/media-studio/voices?kind=${encodeURIComponent(ai.media_kind)}`, { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : null))
        .then((data) => {
          if (!cancelled) setVoices(Array.isArray(data?.voices) ? data.voices : []);
        })
        .catch(() => !cancelled && setVoices([]));
    } else {
      setVoices([]);
    }
    if (ai.media_kind === "avatar") {
      fetch(`/api/apps/media-studio/avatars`, { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : null))
        .then((data) => {
          if (!cancelled) setAvatars(Array.isArray(data?.avatars) ? data.avatars : []);
        })
        .catch(() => !cancelled && setAvatars([]));
    } else {
      setAvatars([]);
    }
    return () => {
      cancelled = true;
    };
  }, [ai.media_kind]);

  return (
    <div className="border border-border rounded p-3 space-y-2 bg-bg">
      <div className="flex items-center gap-2">
        <span className="text-xs uppercase tracking-wide text-text-dim flex-1">{title}</span>
        {ai.status && <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted">{ai.status}</span>}
        <button type="button" onClick={onClear} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">
          Clear
        </button>
      </div>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Kind">
          <select
          value={ai.media_kind}
            onChange={(e) => {
              const mediaKind = e.target.value as MediaKind;
              update({
                media_kind: mediaKind,
                size: mediaKind === "image" ? ai.size || "1024x1024" : undefined,
                estimated_duration_seconds: undefined,
                actual_duration_seconds: undefined,
              });
            }}
            className={field}
          >
            {allowedKinds.map((kind) => <option key={kind} value={kind}>{kind}</option>)}
          </select>
        </Field>
        <Field label="Duration">
          <input
            type="number"
            min={1}
            step={1}
          value={ai.duration || ""}
            onChange={(e) => update({ duration: Number(e.target.value) || undefined, estimated_duration_seconds: Number(e.target.value) || undefined })}
            className={field}
            disabled={ai.media_kind === "image"}
          />
        </Field>
      </div>
      <Field label={ai.media_kind === "audio_tts" || ai.media_kind === "avatar" ? "Script" : "Prompt"}>
        <textarea
          value={ai.prompt}
          onChange={(e) => update({ prompt: e.target.value, estimated_duration_seconds: undefined, actual_duration_seconds: undefined })}
          className={`${field} resize-y`}
          rows={3}
          placeholder={ai.media_kind === "music" ? "minimal upbeat electronic background music" : "Describe the asset to generate"}
        />
      </Field>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Model">
          {models.length > 0 ? (
            <select value={ai.model || ""} onChange={(e) => update({ model: e.target.value })} className={field}>
              <option value="">auto</option>
              {models.map((m) => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
            </select>
          ) : (
            <input value={ai.model || ""} onChange={(e) => update({ model: e.target.value })} placeholder="auto" className={field} />
          )}
        </Field>
        {ai.media_kind === "image" && (
          <Field label="Size">
            <input
              value={ai.size || ""}
              onChange={(e) => update({ size: e.target.value })}
              placeholder="1024x1024"
              className={field}
            />
          </Field>
        )}
        <Field label="Aspect">
          <input
            value={ai.aspect || ""}
            onChange={(e) => update({ aspect: e.target.value })}
            placeholder="16:9"
            className={field}
            disabled={ai.media_kind !== "video" && ai.media_kind !== "avatar"}
          />
        </Field>
        <Field label="Voice">
          {voices.length > 0 ? (
            <select value={ai.voice || ""} onChange={(e) => update({ voice: e.target.value })} className={field}>
              <option value="">auto</option>
              {voices.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name || v.id}{v.language ? ` - ${v.language}` : ""}
                </option>
              ))}
            </select>
          ) : (
            <input
              value={ai.voice || ""}
              onChange={(e) => update({ voice: e.target.value })}
              placeholder="voice_id"
              className={field}
              disabled={ai.media_kind !== "audio_tts" && ai.media_kind !== "avatar"}
            />
          )}
        </Field>
        <Field label="Avatar">
          {avatars.length > 0 ? (
            <select value={ai.avatar || ""} onChange={(e) => update({ avatar: e.target.value })} className={field}>
              <option value="">select</option>
              {avatars.map((a) => <option key={a.id} value={a.id}>{a.name || a.id}</option>)}
            </select>
          ) : (
            <input
              value={ai.avatar || ""}
              onChange={(e) => update({ avatar: e.target.value })}
              placeholder="avatar_id"
              className={field}
              disabled={ai.media_kind !== "avatar"}
            />
          )}
        </Field>
      </div>
      {(ai.media_kind === "image" || ai.media_kind === "video" || ai.media_kind === "avatar") && (
        <Field label="Reference image">
          <input
            value={ai.source_image || ""}
            onChange={(e) => update({ source_image: e.target.value })}
            placeholder="storage:1 or URL"
            className={field}
          />
        </Field>
      )}
      {ai.error && <div className="text-xs text-red whitespace-pre-wrap">{ai.error}</div>}
      {ai.storage_id && <div className="text-xs text-text-dim">storage:{ai.storage_id}</div>}
      {(ai.estimated_duration_seconds || ai.actual_duration_seconds) && (
        <div className="text-xs text-text-dim">
          {ai.actual_duration_seconds ? `actual ${ai.actual_duration_seconds.toFixed(1)}s` : `estimated ${ai.estimated_duration_seconds?.toFixed(1)}s`}
        </div>
      )}
      {ai.job_id && ai.status === "generating" && <div className="text-xs text-text-dim">media-studio job #{ai.job_id}</div>}
      <button
        type="button"
        onClick={onGenerate}
        disabled={busy || !ai.prompt.trim()}
        className="w-full text-sm px-3 py-1.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
      >
        {busy ? "Generating..." : "Generate now"}
      </button>
      <div className="text-[11px] text-text-dim">Saved draft AI clips generate automatically when the composition is rendered.</div>
    </div>
  );
}

function JSONEditor({
  editText,
  outputText,
  onEdit,
  onOutput,
  onApply,
}: {
  editText: string;
  outputText: string;
  onEdit: (s: string) => void;
  onOutput: (s: string) => void;
  onApply: () => void;
}) {
  const mono = "ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace";
  return (
    <div className="flex-1 overflow-auto p-4 space-y-4">
      <Field label="Edit JSON">
        <textarea
          value={editText}
          onChange={(e) => onEdit(e.target.value)}
          spellCheck={false}
          style={{ fontFamily: mono, minHeight: 360 }}
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
        />
      </Field>
      <Field label="Output JSON">
        <textarea
          value={outputText}
          onChange={(e) => onOutput(e.target.value)}
          spellCheck={false}
          style={{ fontFamily: mono, minHeight: 120 }}
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
        />
      </Field>
      <button onClick={onApply} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
        Apply JSON
      </button>
    </div>
  );
}

function StatusPill({ status }: { status: RenderRow["status"] }) {
  const cls =
    status === "complete"
      ? "bg-accent/10 text-accent"
      : status === "failed"
        ? "bg-red/15 text-red"
        : "bg-border text-text-muted";
  return <span className={`text-[10px] px-1.5 py-0.5 rounded ${cls}`}>{status}</span>;
}

function StoragePicker({
  files,
  loading,
  error,
  target,
  onClose,
  onChoose,
}: {
  files: StorageFile[];
  loading: boolean;
  error: string;
  target: AssetPickerTarget["kind"];
  onClose: () => void;
  onChoose: (file: StorageFile) => void;
}) {
  const mediaFiles = files.filter((file) => {
    const kind = storageFileKind(file);
    if (target === "soundtrack") return kind === "audio" || kind === "video";
    if (target === "audio") return kind === "audio";
    return kind === "video" || kind === "image";
  });
  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6" style={{ zIndex: 9998 }}>
      <div className="bg-bg border border-border rounded shadow-xl w-full max-w-3xl max-h-[82vh] flex flex-col">
        <header className="px-4 py-3 border-b border-border flex items-center gap-2">
          <div className="text-sm text-text font-medium flex-1">Browse storage</div>
          <button onClick={onClose} className="text-text-dim hover:text-text px-2 text-lg leading-none">x</button>
        </header>
        <div className="flex-1 overflow-auto">
          {loading && <div className="p-4 text-text-muted text-sm">Loading storage...</div>}
          {error && <div className="p-4 text-red text-sm whitespace-pre-wrap">{error}</div>}
          {!loading && !error && mediaFiles.length === 0 && (
            <div className="p-4 text-text-muted text-sm">No media files found in storage.</div>
          )}
          {!loading && !error && mediaFiles.length > 0 && (
            <ul className="divide-y divide-border">
              {mediaFiles.map((file) => (
                <li key={file.id}>
                  <button
                    type="button"
                    onClick={() => onChoose(file)}
                    className="w-full text-left px-4 py-3 hover:bg-bg-input flex items-center gap-3"
                  >
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted uppercase">
                      {storageFileKind(file)}
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm text-text truncate">{file.name || `file #${file.id}`}</span>
                      <span className="block text-xs text-text-dim truncate">
                        {file.folder || "/"} · storage:{file.id}{file.content_type ? ` · ${file.content_type}` : ""}{fileSize(file.size_bytes) ? ` · ${fileSize(file.size_bytes)}` : ""}
                      </span>
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

function RenderPreview({ render, outputFormat, onOpen }: { render: RenderRow | null; outputFormat: string; onOpen: (r: RenderRow) => void }) {
  if (!render) return null;
  const url = renderSrc(render);
  const audio = isAudioFormat(outputFormat);
  if (render.status !== "complete") {
    return (
      <div className="p-3 border border-border rounded bg-bg-card text-xs">
        <div className="flex items-center gap-2">
          <StatusPill status={render.status} />
          <span className="text-text-muted">render #{render.id} via {render.executor}</span>
        </div>
        {render.error && <pre className="mt-2 text-text-muted whitespace-pre-wrap break-all text-[10px]">{render.error}</pre>}
      </div>
    );
  }
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden">
      <header className="px-3 py-2 border-b border-border flex items-center gap-2 text-xs">
        <span className="text-text font-medium flex-1">Latest render</span>
        <span className="text-text-dim">{(render.duration_ms / 1000).toFixed(1)}s render</span>
        {formatCost(render.cost_usd) && <span className="text-accent">{formatCost(render.cost_usd)}</span>}
      </header>
      <button type="button" onClick={() => onOpen(render)} className="block w-full text-left">
        {url ? (
          audio
            ? <div className="p-3"><audio controls src={url} className="w-full" /></div>
            : <video controls src={url} className="w-full" />
        ) : <div className="py-12 text-center text-text-muted text-xs">no source</div>}
      </button>
    </section>
  );
}

function Lightbox({ render, outputFormat, onClose }: { render: RenderRow; outputFormat: string; onClose: () => void }) {
  const url = renderSrc(render);
  const audio = isAudioFormat(outputFormat);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div
      onClick={onClose}
      className="fixed inset-0 bg-black/85 flex flex-col items-center justify-center p-6"
      style={{ zIndex: 9999 }}
    >
      <div onClick={(e) => e.stopPropagation()} className="flex flex-col items-center gap-3">
        {url && (audio
          ? <audio controls autoPlay src={url} style={{ width: "min(720px, 92vw)" }} />
          : <video controls autoPlay src={url} style={{ maxWidth: "92vw", maxHeight: "82vh" }} />
        )}
        <div className="text-text-dim text-xs">render #{render.id} - {render.executor} - {(render.duration_ms / 1000).toFixed(1)}s</div>
        {url && (
          <a href={url} target="_blank" rel="noopener" className="text-xs px-3 py-1.5 border border-border rounded text-text">
            Open original
          </a>
        )}
      </div>
    </div>
  );
}
