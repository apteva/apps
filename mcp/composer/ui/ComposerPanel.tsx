// ComposerPanel - AI-native multi-track editor for the stable Composer timeline.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AudioLines,
  Bot,
  ChevronLeft,
  ChevronRight,
  Copy,
  Film,
  Image as ImageIcon,
  Layers3,
  Maximize2,
  Music2,
  Pause,
  Play,
  Plus,
  Redo2,
  Save,
  Scissors,
  Search,
  Sparkles,
  Trash2,
  Type,
  Undo2,
  Volume2,
  VolumeX,
  WandSparkles,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { previewCameraAt, previewCropStyle } from "./composer-preview";

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
  edit_json?: string;
  output_json?: string;
  duration_seconds: number;
  created_at: string;
  updated_at: string;
  latest_render?: RenderRow | null;
}

interface RenderRow {
  id: number;
  executor: string;
  status: "queued" | "rendering" | "complete" | "failed" | "cancelled";
  phase?: string;
  progress_pct?: number;
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

interface InstanceHost {
  id: number;
  name?: string;
  status?: string;
}

type AssetType = "video" | "image";
type MediaKind = "image" | "video" | "audio_tts" | "audio_sfx" | "music" | "avatar";
type OutputFormat = "mp4" | "mp3" | "wav" | "m4a" | "aac";
type Aspect = "16:9" | "9:16" | "1:1" | "4:3";
type DurationMode = "fixed_trim_pad" | "fit_generated" | "fit_generated_keep_start" | "fit_generated_reflow";
type TextAnimationPreset = "none" | "fade" | "fade_up" | "fade_down" | "slide_left" | "slide_right" | "scale_pop" | "typewriter" | "word_by_word";

interface Timing {
  mode?: "fixed" | "fit_generated" | "fit_source" | "fit_group" | "fit_timeline";
  source?: string;
  padding_after?: number;
  min_length?: number;
  max_length?: number;
  reflow?: "none" | "following" | "track" | "linked_group" | "composition";
  behavior?: "trim" | "pad" | "trim_or_loop" | "loop" | "stretch" | "regenerate";
  fade_out?: number;
}

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
  source_images?: string[];
  options?: Record<string, unknown>;
  cache_key?: string;
  input_fingerprint?: string;
  continuity_fingerprint?: string;
  cache_policy?: "reuse" | "refresh";
  status?: "draft" | "generating" | "ready" | "failed";
  generation_id?: number;
  provider_request_id?: string;
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

interface ClipLayout {
  fit?: "crop" | "cover" | "contain" | "stretch" | "none";
  x?: number;
  y?: number;
  width?: number;
  height?: number;
  anchor?: "top-left" | "top" | "top-right" | "left" | "center" | "right" | "bottom-left" | "bottom" | "bottom-right";
  position?: string;
  margin?: number;
  margin_x?: number;
  margin_y?: number;
  opacity?: number;
  scale?: number;
  border_radius?: number;
  shadow?: boolean;
  z_index?: number;
}

interface SourceCrop {
  x: number;
  y: number;
  width: number;
  height: number;
}

interface TransformKeyframe {
  time: number;
  x?: number;
  y?: number;
  scale?: number;
  easing?: "linear" | "ease_in" | "ease_out" | "ease_in_out";
}

interface SourceTransform {
  x?: number;
  y?: number;
  scale?: number;
  keyframes?: TransformKeyframe[];
}

interface TimelineMarker {
  id?: string;
  time: number;
  type: string;
  label?: string;
  value?: unknown;
  duration?: number;
  region?: SourceCrop;
  meta?: Record<string, unknown>;
}

interface ClipDraft {
  id: string;
  track_id?: string;
  section_id?: string;
  group_id?: string;
  asset: { type: AssetType; src: string };
  start: number;
  length: number;
  source_start?: number;
  source_end?: number;
  playback_rate?: number;
  crop?: SourceCrop;
  transform?: SourceTransform;
  source_audio?: "auto" | "keep" | "mute";
  duration_mode?: DurationMode;
  estimated_length?: number;
  actual_length?: number;
  timing?: Timing;
  transition?: { in?: string; out?: string };
  fit?: string;
  width?: number;
  height?: number;
  scale?: number;
  opacity?: number;
  offset?: { x?: number; y?: number };
  layout?: ClipLayout;
  z_index?: number;
  border_radius?: number;
  shadow?: boolean;
  text?: { body: string; position?: "top" | "center" | "bottom"; font_size?: number; color?: string };
  ai?: AIAsset;
}

interface AudioClipDraft {
  id: string;
  section_id?: string;
  group_id?: string;
  asset: { type: "audio" | "silence"; src: string };
  start: number;
  length: number;
  source_start?: number;
  source_end?: number;
  playback_rate?: number;
  duration_mode?: DurationMode;
  estimated_length?: number;
  actual_length?: number;
  volume: number;
  after_clip_id?: string;
  gap_seconds?: number;
  timing?: Timing;
  audio?: AudioFX;
  ai?: AIAsset;
}

interface TextClipDraft {
  id: string;
  start: number;
  length: number;
  asset: {
    type: "text";
    text: string;
    font?: { family?: string; size?: number; weight?: number; color?: string; opacity?: number };
    style?: { letter_spacing?: number; line_height?: number; transform?: "none" | "uppercase" | "lowercase" | "capitalize"; wrap?: boolean; auto_size?: boolean; max_width?: number; max_height?: number; min_font_size?: number; padding?: number; safe_area?: number };
    stroke?: { color?: string; width?: number; opacity?: number };
    shadow?: { color?: string; offset_x?: number; offset_y?: number; blur?: number; opacity?: number };
    align?: { horizontal?: "left" | "center" | "right"; vertical?: "top" | "center" | "bottom" };
  };
  position?: { x?: string; y?: string; anchor?: "top-left" | "top" | "top-right" | "left" | "center" | "right" | "bottom-left" | "bottom" | "bottom-right" };
  animation?: {
    in?: { preset?: TextAnimationPreset; duration?: number; easing?: string };
    out?: { preset?: TextAnimationPreset; duration?: number; easing?: string };
  };
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
  visualTrackOrder?: string[];
  audioClips: AudioClipDraft[];
  textClips: TextClipDraft[];
  soundtrack: { src: string; volume: number; timing?: Timing; ai?: AIAsset } | null;
  markers: TimelineMarker[];
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
type ClipEditorTarget = { kind: "visual"; id: string } | { kind: "audio"; id: string } | { kind: "text"; id: string };

const TEXT_ANIMATIONS: TextAnimationPreset[] = ["none", "fade", "fade_up", "fade_down", "slide_left", "slide_right", "scale_pop", "typewriter", "word_by_word"];

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
  visualTrackOrder: ["visual-1"],
  audioClips: [],
  textClips: [],
  markers: [],
  soundtrack: null,
  output: { format: "mp4", resolution: "hd", aspect: "16:9", fps: 30 },
};

function cloneDefault(): DraftState {
  return JSON.parse(JSON.stringify(DEFAULT_DRAFT));
}

function cloneDraft(draft: DraftState): DraftState {
  return JSON.parse(JSON.stringify(draft));
}

function renderSrc(r: RenderRow | null | undefined): string {
  if (!r) return "";
  if (r.storage_url) return r.storage_url;
  if (r.local_cache_url) return r.local_cache_url;
  return "";
}

function pauseAllPanelMedia() {
  if (typeof document === "undefined") return;
  document.querySelectorAll("video,audio").forEach((node) => {
    try {
      (node as HTMLMediaElement).pause();
    } catch {
      // Best-effort cleanup before switching preview owners.
    }
  });
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
  return clips.reduce((max, c) => Math.max(max, Math.max(0, Number(c.start) || 0) + Math.max(0.1, Number(c.length) || 0)), 0);
}

function durationOfAudio(clips: AudioClipDraft[]): number {
  return clips.reduce((max, c) => Math.max(max, Math.max(0, Number(c.start) || 0) + Math.max(0.1, Number(c.length) || 0)), 0);
}

function durationOfText(clips: TextClipDraft[]): number {
  return clips.reduce((max, c) => Math.max(max, Math.max(0, Number(c.start) || 0) + Math.max(0.1, Number(c.length) || 0)), 0);
}

function durationOfDraft(draft: DraftState): number {
  return Math.max(durationOf(draft.clips), durationOfAudio(draft.audioClips), durationOfText(draft.textClips));
}

function normalizeClips(clips: ClipDraft[]): ClipDraft[] {
  const groups = new Map<string, ClipDraft[]>();
  for (const clip of clips) {
    const trackID = clip.track_id || "visual-1";
    const group = groups.get(trackID) || [];
    group.push(clip);
    groups.set(trackID, group);
  }
  const implicitTracks = new Set<string>();
  for (const [trackID, group] of groups) {
    if (group.length > 1 && group.every((clip) => Math.abs(Number(clip.start) || 0) < 0.001)) implicitTracks.add(trackID);
  }
  const cursors = new Map<string, number>();
  return clips.map((clip, i) => {
    const conformed = applyAIDuration(clip);
    const length = Math.max(0.1, Number(conformed.length) || 1);
    const trackID = clip.track_id || "visual-1";
    const cursor = cursors.get(trackID) || 0;
    const start = implicitTracks.has(trackID) ? cursor : Math.max(0, Number(clip.start) || 0);
    const next = {
      ...conformed,
      id: clip.id || `clip-${i + 1}-${Date.now()}`,
      track_id: trackID,
      start: Number(start.toFixed(3)),
      length,
      asset: {
        type: (clip.asset?.type === "image" ? "image" : "video") as AssetType,
        src: clip.asset?.src || "",
      },
      ai: clip.ai,
    };
    cursors.set(trackID, Math.max(cursor, start + length));
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

function normalizeTextClips(clips: TextClipDraft[]): TextClipDraft[] {
  return clips.map((clip, i) => ({
    ...clip,
    id: clip.id || `text-${i + 1}-${Date.now()}`,
    start: Math.max(0, Number(clip.start) || 0),
    length: Math.max(0.1, Number(clip.length) || 3),
    asset: {
      type: "text",
      text: String(clip.asset?.text || ""),
      font: {
        size: Number(clip.asset?.font?.size) || 64,
        color: clip.asset?.font?.color || "#ffffff",
        weight: Number(clip.asset?.font?.weight) || 800,
        opacity: clip.asset?.font?.opacity,
        family: clip.asset?.font?.family,
      },
      style: clip.asset?.style,
      stroke: {
        color: clip.asset?.stroke?.color || "#000000",
        width: Number(clip.asset?.stroke?.width ?? 3),
        opacity: clip.asset?.stroke?.opacity,
      },
      shadow: clip.asset?.shadow,
      align: {
        horizontal: clip.asset?.align?.horizontal || "center",
        vertical: clip.asset?.align?.vertical || "center",
      },
    },
    position: clip.position,
    animation: clip.animation || { in: { preset: "fade_up", duration: 0.6 }, out: { preset: "fade", duration: 0.35 } },
  }));
}

function prettyJSON(raw: string, fallback: string): string {
  try {
    return JSON.stringify(JSON.parse(raw || "{}"), null, 2);
  } catch {
    return fallback;
  }
}

function visualTracks(tracks: any[]): any[] {
  if (!Array.isArray(tracks)) return [];
  return tracks.filter((track) => {
    const kind = String(track?.type || "visual").toLowerCase();
    return kind !== "audio" && kind !== "sound" && kind !== "music" && kind !== "voice" && kind !== "sfx" &&
      kind !== "overlay" && kind !== "text" && kind !== "title" && kind !== "titles" && kind !== "subtitle" && kind !== "subtitles";
  });
}

function visualTrackIDs(draft: DraftState): string[] {
  const found = Array.from(new Set(draft.clips.map((clip) => clip.track_id || "visual-1")));
  const ordered = (draft.visualTrackOrder || []).filter((id) => found.includes(id));
  for (const id of found) if (!ordered.includes(id)) ordered.push(id);
  return ordered.length ? ordered : ["visual-1"];
}

function endOfVisualTrack(clips: ClipDraft[], trackID: string): number {
  return clips
    .filter((clip) => (clip.track_id || "visual-1") === trackID)
    .reduce((max, clip) => Math.max(max, clip.start + clip.length), 0);
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

function textTracks(tracks: any[]): any[] {
  if (!Array.isArray(tracks)) return [];
  return tracks.filter((track) => {
    const kind = String(track?.type || "").toLowerCase();
    return kind === "overlay" || kind === "text" || kind === "title" || kind === "titles" || kind === "subtitle" || kind === "subtitles";
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
    draft.markers = Array.isArray(timeline.markers)
      ? timeline.markers.map((marker: any, index: number) => ({
          id: String(marker.id || `marker-${index + 1}`),
          time: Math.max(0, Number(marker.time) || 0),
          type: String(marker.type || "event"),
          label: String(marker.label || "") || undefined,
          value: marker.value,
          duration: Math.max(0, Number(marker.duration) || 0) || undefined,
          region: marker.region,
          meta: marker.meta,
        }))
      : [];
    const visuals = visualTracks(timeline.tracks || []);
    draft.visualTrackOrder = visuals.map((track: any, i: number) => String(track?.id || `visual-${i + 1}`));
    const clips = visuals.flatMap((track: any, trackIndex: number) => {
      const trackID = String(track?.id || `visual-${trackIndex + 1}`);
      return (Array.isArray(track?.clips) ? track.clips : []).map((clip: any, clipIndex: number) => ({ clip, trackID, clipIndex }));
    });
    if (clips.length) {
      draft.clips = normalizeClips(clips.map(({ clip, trackID, clipIndex }: any) => ({
        id: String(clip.uid || `clip-${clipIndex + 1}`),
        track_id: trackID,
        section_id: clip.section_id,
        group_id: clip.group_id,
        asset: {
          type: clip.asset?.type === "image" ? "image" : "video",
          src: String(clip.asset?.src || ""),
        },
        start: Number(clip.start) || 0,
        length: Number(clip.length) || 1,
        source_start: Number(clip.source_start || 0) || undefined,
        source_end: Number(clip.source_end || 0) || undefined,
        playback_rate: Number(clip.playback_rate || 0) || undefined,
        crop: clip.crop,
        transform: clip.transform,
        source_audio: clip.source_audio,
        duration_mode: clip.duration_mode || defaultDurationMode(clip.ai?.media_kind),
        estimated_length: Number(clip.estimated_length || clip.ai?.estimated_duration_seconds || 0) || undefined,
        actual_length: Number(clip.actual_length || clip.ai?.actual_duration_seconds || 0) || undefined,
        timing: clip.timing,
        transition: {
          in: clip.transition?.in || "none",
          out: clip.transition?.out || "none",
        },
        fit: clip.fit,
        width: clip.width,
        height: clip.height,
        scale: clip.scale,
        opacity: clip.opacity,
        offset: clip.offset,
        layout: clip.layout,
        z_index: clip.z_index,
        border_radius: clip.border_radius,
        shadow: clip.shadow,
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
        section_id: clip.section_id,
        group_id: clip.group_id,
        asset: { type: clip.asset?.type === "silence" ? "silence" : "audio", src: String(clip.asset?.src || "") },
        start: Number(clip.start) || 0,
        length: Number(clip.length ?? clip.duration) || 1,
        source_start: Number(clip.source_start || 0) || undefined,
        source_end: Number(clip.source_end || 0) || undefined,
        playback_rate: Number(clip.playback_rate || 0) || undefined,
        duration_mode: clip.duration_mode || defaultDurationMode(clip.ai?.media_kind),
        estimated_length: Number(clip.estimated_length || clip.ai?.estimated_duration_seconds || 0) || undefined,
        actual_length: Number(clip.actual_length || clip.ai?.actual_duration_seconds || 0) || undefined,
        volume: Number(clip.volume) || 1,
        after_clip_id: clip.after_clip_id,
        gap_seconds: Number(clip.gap_seconds || 0) || undefined,
        timing: clip.timing,
        audio: clip.audio,
        ai: clip.ai,
      })));
    }
    const text = textTracks(timeline.tracks || []).flatMap((track: any) => Array.isArray(track?.clips) ? track.clips : []);
    if (text.length) {
      draft.textClips = normalizeTextClips(text.map((clip: any, i: number) => ({
        id: String(clip.uid || `text-${i + 1}`),
        start: Number(clip.start) || 0,
        length: Number(clip.length ?? clip.duration) || 3,
        asset: {
          type: "text",
          text: String(clip.asset?.text || clip.text?.body || ""),
          font: clip.asset?.font || {
            size: Number(clip.text?.font_size) || 64,
            color: clip.text?.color || "#ffffff",
            weight: 800,
          },
          style: clip.asset?.style,
          stroke: clip.asset?.stroke,
          shadow: clip.asset?.shadow,
          align: clip.asset?.align,
        },
        position: clip.position,
        animation: clip.animation,
      })));
    }
    if (timeline.soundtrack?.src) {
      draft.soundtrack = {
        src: String(timeline.soundtrack.src),
        volume: Number(timeline.soundtrack.volume) || 1,
        timing: timeline.soundtrack.timing,
        ai: timeline.soundtrack.ai,
      };
    } else if (timeline.soundtrack?.ai) {
      draft.soundtrack = {
        src: String(timeline.soundtrack.src || ""),
        volume: Number(timeline.soundtrack.volume) || 1,
        timing: timeline.soundtrack.timing,
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
    if (clip.section_id) out.section_id = clip.section_id;
    if (clip.group_id) out.group_id = clip.group_id;
    if (clip.source_audio) out.source_audio = clip.source_audio;
    if (clip.source_start) out.source_start = clip.source_start;
    if (clip.source_end) out.source_end = clip.source_end;
    if (clip.playback_rate && clip.playback_rate !== 1) out.playback_rate = clip.playback_rate;
    if (clip.crop) out.crop = clip.crop;
    if (clip.transform) out.transform = clip.transform;
    if (clip.ai) out.ai = clip.ai;
    if (clip.duration_mode) out.duration_mode = clip.duration_mode;
    if (clip.estimated_length) out.estimated_length = clip.estimated_length;
    if (clip.actual_length) out.actual_length = clip.actual_length;
    if (clip.timing) out.timing = clip.timing;
    if ((clip.transition?.in && clip.transition.in !== "none") || (clip.transition?.out && clip.transition.out !== "none")) {
      out.transition = {
        in: clip.transition?.in || "none",
        out: clip.transition?.out || "none",
      };
    }
    if (clip.fit) out.fit = clip.fit;
    if (clip.width) out.width = clip.width;
    if (clip.height) out.height = clip.height;
    if (clip.scale) out.scale = clip.scale;
    if (clip.opacity !== undefined) out.opacity = clip.opacity;
    if (clip.offset) out.offset = clip.offset;
    if (clip.layout) out.layout = clip.layout;
    if (clip.z_index) out.z_index = clip.z_index;
    if (clip.border_radius) out.border_radius = clip.border_radius;
    if (clip.shadow) out.shadow = clip.shadow;
    if (clip.text?.body?.trim()) {
      out.text = {
        body: clip.text.body,
        position: clip.text.position || "bottom",
        font_size: clip.text.font_size || 32,
        color: clip.text.color || "#ffffff",
      };
    }
    out.__track_id = clip.track_id || "visual-1";
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
    if (clip.section_id) out.section_id = clip.section_id;
    if (clip.group_id) out.group_id = clip.group_id;
    if (clip.ai) out.ai = clip.ai;
    if (clip.source_start) out.source_start = clip.source_start;
    if (clip.source_end) out.source_end = clip.source_end;
    if (clip.playback_rate && clip.playback_rate !== 1) out.playback_rate = clip.playback_rate;
    if (clip.duration_mode) out.duration_mode = clip.duration_mode;
    if (clip.estimated_length) out.estimated_length = clip.estimated_length;
    if (clip.actual_length) out.actual_length = clip.actual_length;
    if (clip.after_clip_id) out.after_clip_id = clip.after_clip_id;
    if (clip.gap_seconds) out.gap_seconds = clip.gap_seconds;
    if (clip.timing) out.timing = clip.timing;
    if (clip.audio) out.audio = clip.audio;
    return out;
  });
  const textClips = normalizeTextClips(draft.textClips).filter((clip) => clip.asset.text.trim()).map((clip) => ({
    uid: clip.id,
    asset: clip.asset,
    start: clip.start,
    length: clip.length,
    position: clip.position,
    animation: clip.animation,
  }));
  const tracks: any[] = [];
  const groupedVisuals = new Map<string, any[]>();
  for (const clip of clips) {
    const trackID = String(clip.__track_id || "visual-1");
    delete clip.__track_id;
    const group = groupedVisuals.get(trackID) || [];
    group.push(clip);
    groupedVisuals.set(trackID, group);
  }
  for (const trackID of visualTrackIDs(draft)) {
    const trackClips = groupedVisuals.get(trackID);
    if (trackClips?.length) tracks.push({ id: trackID, type: "visual", clips: trackClips });
  }
  if (textClips.length) tracks.push({ type: "overlay", clips: textClips });
  if (audioClips.length) tracks.push({ type: "audio", clips: audioClips });
  const body: Record<string, unknown> = {
    name: draft.name,
    tracks,
    background: draft.background || "#000000",
    markers: draft.markers,
    output: draft.output,
  };
  if (draft.soundtrack?.src?.trim() || draft.soundtrack?.ai) {
    body.soundtrack = {
      src: draft.soundtrack.src.trim(),
      volume: Math.max(0, Math.min(1, Number(draft.soundtrack.volume) || 1)),
      timing: draft.soundtrack.timing,
      ai: draft.soundtrack.ai,
    };
  }
  return body;
}

function bodyFromEditorJSON(name: string, editText: string, outputText: string): Record<string, unknown> {
  const edit = JSON.parse(editText || "{}");
  const output = JSON.parse(outputText || "{}");
  if (isV2CompositionJSON(editText)) {
    return { name, spec: edit, output };
  }
  const timeline = edit.timeline || {};
  return {
    name,
    tracks: Array.isArray(timeline.tracks) ? timeline.tracks : [],
    soundtrack: timeline.soundtrack,
    background: timeline.background,
    markers: timeline.markers,
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
      markers: body.markers,
    },
  }, null, 2);
}

function outputJSONFromDraft(draft: DraftState): string {
  return JSON.stringify(draft.output, null, 2);
}

function isV2CompositionJSON(raw?: string): boolean {
  if (!raw) return false;
  try {
    const parsed = JSON.parse(raw);
    return parsed?.version === "composer/v2" || Array.isArray(parsed?.scenes) || Array.isArray(parsed?.assets) || Array.isArray(parsed?.audio);
  } catch {
    return false;
  }
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

function previewFrameMaxWidth(aspect: Aspect): string {
  switch (aspect) {
    case "9:16": return "360px";
    case "1:1": return "620px";
    case "4:3": return "760px";
    default: return "960px";
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
    options: defaultOptionsForAI(kind),
    cache_policy: "reuse",
    status: "draft",
  };
}

function defaultOptionsForAI(kind: MediaKind): Record<string, unknown> | undefined {
  if (kind !== "audio_tts") return undefined;
  return {
    voice_settings: {
      stability: 0.85,
      similarity_boost: 0.95,
      style: 0,
      use_speaker_boost: true,
    },
  };
}

function withDefaultAIOptions(ai: AIAsset): AIAsset {
  if (ai.media_kind !== "audio_tts") return ai;
  const options = ai.options || {};
  const defaults = (defaultOptionsForAI("audio_tts")?.voice_settings || {}) as Record<string, unknown>;
  const settings = options.voice_settings && typeof options.voice_settings === "object" && !Array.isArray(options.voice_settings)
    ? options.voice_settings as Record<string, unknown>
    : {};
  return {
    ...ai,
    options: {
      ...options,
      voice_settings: { ...defaults, ...settings },
    },
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
  ai = withDefaultAIOptions(ai);
  const estimate = estimateForAI(ai);
  if (estimate <= 0) return ai;
  return { ...ai, estimated_duration_seconds: ai.estimated_duration_seconds || estimate };
}

function aiSourceImages(ai: Pick<AIAsset, "source_image" | "source_images"> | undefined): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const add = (value: string | undefined) => {
    const ref = String(value || "").trim();
    if (!ref || seen.has(ref)) return;
    seen.add(ref);
    out.push(ref);
  };
  for (const ref of ai?.source_images || []) add(ref);
  add(ai?.source_image);
  return out;
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
  const sourceImages = stringArray(extra.source_image_refs).length
    ? stringArray(extra.source_image_refs)
    : stringArray(req.source_images);
  const sourceImage = sourceImages[0] || String(extra.source_image_ref || req.source_image || "") || undefined;
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
    source_image: sourceImage,
    source_images: sourceImages.length ? sourceImages : sourceImage ? [sourceImage] : undefined,
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

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.map((item) => String(item || "").trim()).filter(Boolean);
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
      edit_json: enrichEditJSONWithGenerations(row.edit_json || "", byStorage),
    }));
  } catch {
    return rows;
  }
}

function fitsGenerated(mode: DurationMode | undefined): boolean {
  return mode === "fit_generated" || mode === "fit_generated_reflow";
}

export default function ComposerPanel({ projectId, installId }: NativePanelProps) {
  const [compositions, setCompositions] = useState<Composition[]>([]);
  const [selectedDetail, setSelectedDetail] = useState<Composition | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
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
  const [libraryOpen, setLibraryOpen] = useState(true);
  const [inspectorOpen, setInspectorOpen] = useState(true);
  const undoStack = useRef<DraftState[]>([]);
  const redoStack = useRef<DraftState[]>([]);

  useEffect(() => {
    if (window.innerWidth < 1120) {
      setLibraryOpen(false);
      setInspectorOpen(false);
    }
  }, []);

  const selectedSummary = selectedId != null ? compositions.find((c) => c.id === selectedId) || null : null;
  const selected = selectedId != null && selectedDetail?.id === selectedId ? selectedDetail : selectedSummary;
  const selectedFull = selectedId != null && selectedDetail?.id === selectedId ? selectedDetail : null;
  const examples = useMemo(() => composerExamples(), []);
  const clips = useMemo(() => normalizeClips(draft.clips), [draft.clips]);
  const audioClips = useMemo(() => normalizeAudioClips(draft.audioClips), [draft.audioClips]);
  const textClips = useMemo(() => normalizeTextClips(draft.textClips), [draft.textClips]);
  const totalDuration = useMemo(() => Math.max(durationOf(clips), durationOfAudio(audioClips), durationOfText(textClips)), [clips, audioClips, textClips]);
  const selectedClip = clips.find((clip) => clip.id === selectedClipId) || (!selectedClipId ? clips[0] : null) || null;

  const load = useCallback(async () => {
    try {
      const res = await fetch(withProject(`${API}/compositions?summary=1&limit=100`, projectId), { credentials: "same-origin" });
      if (!res.ok) {
        setStatus(`Error: ${res.status}`);
        return;
      }
      const data = await res.json();
      setCompositions(data.compositions || []);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [projectId]);

  const loadCompositionDetail = useCallback(async (id: number) => {
    setDetailLoading(true);
    try {
      const res = await fetch(withProject(`${API}/composition/${id}`, projectId), { credentials: "same-origin" });
      if (!res.ok) {
        setStatus(`Error loading composition #${id}: ${res.status}`);
        return;
      }
      const row = await res.json();
      const [enriched] = await enrichCompositionsWithMediaStudio(projectId, [row]);
      const detail = enriched || row;
      setSelectedDetail(detail);
      setCompositions((prev) => prev.map((c) => (c.id === detail.id ? { ...c, ...detail } : c)));
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setDetailLoading(false);
    }
  }, [projectId]);

  const loadBindings = useCallback(async () => {
    try {
      const res = await fetch(withProject(`${API}/bindings`, projectId), { credentials: "same-origin" });
      if (!res.ok) return;
      const data = await res.json();
      setBindings(data);
    } catch {
      // Bindings are advisory; rendering still surfaces backend errors.
    }
  }, [projectId]);

  const openLightbox = useCallback((render: RenderRow) => {
    setPlaying(false);
    pauseAllPanelMedia();
    setLightbox(render);
  }, []);

  const closeLightbox = useCallback(() => {
    pauseAllPanelMedia();
    setLightbox(null);
  }, []);

  useEffect(() => {
    setSelectedDetail(null);
    setSelectedId(null);
    setPlaying(false);
    pauseAllPanelMedia();
    load();
    loadBindings();
  }, [load, loadBindings, projectId]);

  useEffect(() => {
    if (selectedId == null) {
      setSelectedDetail(null);
      return;
    }
    if (selectedDetail?.id !== selectedId) {
      loadCompositionDetail(selectedId);
    }
  }, [selectedId, selectedDetail?.id, loadCompositionDetail]);

  useEffect(() => {
    const render = selectedFull?.latest_render;
    if (selectedId == null || !render || ["complete", "failed", "cancelled"].includes(render.status)) return;
    const timer = window.setInterval(() => loadCompositionDetail(selectedId), 2500);
    return () => window.clearInterval(timer);
  }, [selectedId, selectedFull?.latest_render?.id, selectedFull?.latest_render?.status, loadCompositionDetail]);

  useEffect(() => {
    if (selectedId != null && !selectedFull) return;
    const next = parseComposition(selectedFull);
    undoStack.current = [];
    redoStack.current = [];
    setDraft(next);
    setSelectedClipId(next.clips[0]?.id || next.textClips[0]?.id || next.audioClips[0]?.id || "");
    setPlayhead(0);
    setPlaying(false);
    pauseAllPanelMedia();
    setJsonEdit(selectedFull ? prettyJSON(selectedFull.edit_json || "", editJSONFromDraft(next)) : editJSONFromDraft(next));
    setJsonOutput(selectedFull ? prettyJSON(selectedFull.output_json || "", outputJSONFromDraft(next)) : outputJSONFromDraft(next));
  }, [selectedId, selectedFull?.edit_json, selectedFull?.output_json]);

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
      const normalized = { ...next, clips: normalizeClips(next.clips), audioClips: normalizeAudioClips(next.audioClips), textClips: normalizeTextClips(next.textClips) };
      if (JSON.stringify(cur) === JSON.stringify(normalized)) return cur;
      undoStack.current = [...undoStack.current.slice(-79), cloneDraft(cur)];
      redoStack.current = [];
      setJsonEdit(editJSONFromDraft(normalized));
      setJsonOutput(outputJSONFromDraft(normalized));
      return normalized;
    });
  };

  const restoreDraft = (next: DraftState) => {
    setDraft(next);
    setJsonEdit(editJSONFromDraft(next));
    setJsonOutput(outputJSONFromDraft(next));
    setSelectedClipId((id) => next.clips.some((clip) => clip.id === id) || next.audioClips.some((clip) => clip.id === id) || next.textClips.some((clip) => clip.id === id)
      ? id
      : next.clips[0]?.id || next.audioClips[0]?.id || next.textClips[0]?.id || "");
  };

  const undo = () => {
    const previous = undoStack.current.pop();
    if (!previous) return;
    redoStack.current.push(cloneDraft(draft));
    restoreDraft(previous);
    setStatus("Undid last edit.");
  };

  const redo = () => {
    const next = redoStack.current.pop();
    if (!next) return;
    undoStack.current.push(cloneDraft(draft));
    restoreDraft(next);
    setStatus("Redid edit.");
  };

  const updateClip = (id: string, patch: Partial<ClipDraft>) => {
    updateDraft((cur) => ({
      ...cur,
      clips: cur.clips.map((clip) => clip.id === id ? { ...clip, ...patch } : clip),
    }));
  };

  const addClip = () => {
    const id = `clip-${Date.now()}`;
    updateDraft((cur) => {
      const trackID = cur.visualTrackOrder?.[0] || "visual-1";
      return {
        ...cur,
        clips: [...cur.clips, {
          id,
          track_id: trackID,
          asset: { type: "video", src: "" },
          start: endOfVisualTrack(cur.clips, trackID),
          length: 4,
          transition: { in: "none", out: "none" },
        }],
      };
    });
    setSelectedClipId(id);
  };

  const addAIVisualClip = (kind: "image" | "video" | "avatar") => {
    const id = `clip-${Date.now()}`;
    updateDraft((cur) => {
      const trackID = cur.visualTrackOrder?.[0] || "visual-1";
      return {
        ...cur,
        clips: [...cur.clips, {
          id,
          track_id: trackID,
          asset: { type: kind === "image" ? "image" : "video", src: "" },
          start: endOfVisualTrack(cur.clips, trackID),
          length: kind === "image" ? 4 : 6,
          duration_mode: defaultDurationMode(kind),
          transition: { in: "none", out: "none" },
          ai: defaultAI(kind, cur.output.aspect),
        }],
        output: { ...cur.output, format: "mp4" },
      };
    });
    setSelectedClipId(id);
    setClipEditor({ kind: "visual", id });
  };

  const addClipFromFile = (file: StorageFile) => {
    const id = `clip-${Date.now()}`;
    const type = assetTypeFromFile(file);
    updateDraft((cur) => {
      const trackID = cur.visualTrackOrder?.[0] || "visual-1";
      return {
        ...cur,
        clips: [...cur.clips, {
          id,
          track_id: trackID,
          asset: { type, src: `storage:${file.id}` },
          start: endOfVisualTrack(cur.clips, trackID),
          length: type === "image" ? 4 : 6,
          transition: { in: "none", out: "none" },
        }],
      };
    });
    setSelectedClipId(id);
    setPickerTarget(null);
  };

  const deleteClip = (id: string) => {
    updateDraft((cur) => {
      const next = cur.clips.filter((clip) => clip.id !== id);
      return { ...cur, clips: next };
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

  const addVisualLayer = () => {
    const stamp = Date.now();
    const trackID = `visual-layer-${stamp}`;
    const id = `clip-${stamp}`;
    updateDraft((cur) => ({
      ...cur,
      visualTrackOrder: [...(cur.visualTrackOrder || ["visual-1"]), trackID],
      clips: [...cur.clips, {
        id,
        track_id: trackID,
        asset: { type: "image", src: "" },
        start: Math.max(0, playhead),
        length: 5,
        layout: { fit: "cover", width: 0.32, anchor: "bottom-right", margin: 24, opacity: 1, border_radius: 8, shadow: true },
        transition: { in: "fade", out: "fade" },
      }],
    }));
    setSelectedClipId(id);
    setClipEditor({ kind: "visual", id });
  };

  const duplicateSelectedClip = () => {
    const stamp = Date.now();
    const visual = draft.clips.find((clip) => clip.id === selectedClipId);
    const audio = draft.audioClips.find((clip) => clip.id === selectedClipId);
    const text = draft.textClips.find((clip) => clip.id === selectedClipId);
    if (visual) {
      const id = `clip-${stamp}`;
      updateDraft((cur) => ({ ...cur, clips: [...cur.clips, { ...cloneDraft({ ...DEFAULT_DRAFT, clips: [visual] }).clips[0], id, start: visual.start + visual.length }] }));
      setSelectedClipId(id);
    } else if (audio) {
      const id = `audio-${stamp}`;
      updateDraft((cur) => ({ ...cur, audioClips: [...cur.audioClips, { ...JSON.parse(JSON.stringify(audio)), id, start: audio.start + audio.length }] }));
      setSelectedClipId(id);
    } else if (text) {
      const id = `text-${stamp}`;
      updateDraft((cur) => ({ ...cur, textClips: [...cur.textClips, { ...JSON.parse(JSON.stringify(text)), id, start: text.start + text.length }] }));
      setSelectedClipId(id);
    }
  };

  const splitSelectedClip = () => {
    const splitAt = Number(playhead.toFixed(3));
    const split = <T extends { id: string; start: number; length: number }>(items: T[], prefix: string): T[] => {
      const index = items.findIndex((item) => item.id === selectedClipId);
      if (index < 0) return items;
      const source = items[index];
      if (splitAt <= source.start + 0.1 || splitAt >= source.start + source.length - 0.1) return items;
      const right = { ...JSON.parse(JSON.stringify(source)), id: `${prefix}-${Date.now()}`, start: splitAt, length: source.start + source.length - splitAt } as T;
      const left = { ...source, length: splitAt - source.start };
      const next = [...items];
      next.splice(index, 1, left, right);
      setSelectedClipId(right.id);
      return next;
    };
    updateDraft((cur) => ({
      ...cur,
      clips: split(cur.clips, "clip"),
      audioClips: split(cur.audioClips, "audio"),
      textClips: split(cur.textClips, "text"),
    }));
  };

  const updateAudioClip = (id: string, patch: Partial<AudioClipDraft>) => {
    updateDraft((cur) => ({
      ...cur,
      audioClips: cur.audioClips.map((clip) => clip.id === id ? { ...clip, ...patch } : clip),
    }));
  };

  const updateTextClip = (id: string, patch: Partial<TextClipDraft>) => {
    updateDraft((cur) => ({
      ...cur,
      textClips: cur.textClips.map((clip) => clip.id === id ? { ...clip, ...patch, asset: patch.asset ? { ...clip.asset, ...patch.asset } : clip.asset } : clip),
    }));
  };

  const addTextClip = () => {
    const id = `text-${Date.now()}`;
    updateDraft((cur) => ({
      ...cur,
      textClips: [
        ...cur.textClips,
        {
          id,
          start: Math.max(0, playhead || 0),
          length: 4,
          asset: {
            type: "text",
            text: "CINEMATIC TITLE",
            font: { size: 72, color: "#ffffff", weight: 800 },
            style: { transform: "uppercase", letter_spacing: 2 },
            stroke: { color: "#000000", width: 4, opacity: 0.85 },
            shadow: { color: "#ff2f6d", offset_x: 0, offset_y: 2, opacity: 0.65 },
            align: { horizontal: "center", vertical: "center" },
          },
          animation: { in: { preset: "fade_up", duration: 0.6, easing: "ease_out" }, out: { preset: "fade", duration: 0.35 } },
        },
      ],
      output: { ...cur.output, format: "mp4" },
    }));
    setSelectedClipId(id);
    setClipEditor({ kind: "text", id });
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

  const deleteTextClip = (id: string) => {
    updateDraft((cur) => ({ ...cur, textClips: cur.textClips.filter((clip) => clip.id !== id) }));
  };

  const save = async () => {
    setStatus("Saving...");
    try {
      if (tab !== "json" && draft.clips.length === 0 && draft.audioClips.length === 0 && draft.textClips.length === 0) {
        setStatus("Add at least one clip before saving.");
        return;
      }
      const body = tab === "json" ? bodyFromEditorJSON(draft.name, jsonEdit, jsonOutput) : draftToBody(draft);
      if (!(body as any).spec && (!Array.isArray((body as any).tracks) || (body as any).tracks.length === 0)) {
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
      const id = result.id || selectedId || 0;
      if (result.id) setSelectedId(result.id);
      if (id > 0) await loadCompositionDetail(id);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  };

  const applyJSON = () => {
    try {
      const edit = JSON.parse(jsonEdit);
      const output = JSON.parse(jsonOutput);
      if (isV2CompositionJSON(jsonEdit)) {
        setJsonEdit(JSON.stringify(edit, null, 2));
        setJsonOutput(JSON.stringify(output, null, 2));
        setStatus("Composer v2 JSON is valid. Save to persist it.");
        return;
      }
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
    if (selectedId == null && tab !== "json" && draft.clips.length === 0 && draft.audioClips.length === 0 && draft.textClips.length === 0) {
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
      const body: Record<string, unknown> = { id: selectedId, wait: false };
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
      setStatus(`Render #${result.render_id} queued. AI generation and rendering will continue automatically.`);
      await load();
      if (selectedId) await loadCompositionDetail(selectedId);
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
    const normalized = { ...next, clips: normalizeClips(next.clips), audioClips: normalizeAudioClips(next.audioClips), textClips: normalizeTextClips(next.textClips) };
    setSelectedId(null);
    setDraft(normalized);
    setSelectedClipId(normalized.clips[0]?.id || normalized.textClips[0]?.id || normalized.audioClips[0]?.id || "");
    setPlayhead(0);
    setTab("timeline");
    setJsonEdit(editJSONFromDraft(normalized));
    setJsonOutput(outputJSONFromDraft(normalized));
    setStatus(`Loaded example: ${example.name}. Save it to create a composition.`);
  };

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const editing = target?.matches("input, textarea, select, [contenteditable='true']");
      const mod = event.metaKey || event.ctrlKey;
      if (mod && event.key.toLowerCase() === "s") {
        event.preventDefault();
        void save();
        return;
      }
      if (editing) return;
      if (mod && event.key.toLowerCase() === "z") {
        event.preventDefault();
        event.shiftKey ? redo() : undo();
      } else if (mod && (event.key.toLowerCase() === "y")) {
        event.preventDefault();
        redo();
      } else if (mod && event.key.toLowerCase() === "d") {
        event.preventDefault();
        duplicateSelectedClip();
      } else if (event.key === " " && !mod) {
        event.preventDefault();
        setPlaying((value) => !value);
      } else if (event.key.toLowerCase() === "s" && !mod) {
        event.preventDefault();
        splitSelectedClip();
      } else if (event.key === "Delete" || event.key === "Backspace") {
        event.preventDefault();
        if (draft.clips.some((clip) => clip.id === selectedClipId)) deleteClip(selectedClipId);
        else if (draft.audioClips.some((clip) => clip.id === selectedClipId)) deleteAudioClip(selectedClipId);
        else if (draft.textClips.some((clip) => clip.id === selectedClipId)) deleteTextClip(selectedClipId);
      } else if (event.altKey && (event.key === "ArrowLeft" || event.key === "ArrowRight")) {
        event.preventDefault();
        const delta = (event.shiftKey ? 1 : 0.1) * (event.key === "ArrowLeft" ? -1 : 1);
        if (draft.clips.some((clip) => clip.id === selectedClipId)) updateClip(selectedClipId, { start: Math.max(0, (selectedClip?.start || 0) + delta) });
        const audio = draft.audioClips.find((clip) => clip.id === selectedClipId);
        if (audio) updateAudioClip(audio.id, { start: Math.max(0, audio.start + delta) });
        const text = draft.textClips.find((clip) => clip.id === selectedClipId);
        if (text) updateTextClip(text.id, { start: Math.max(0, text.start + delta) });
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

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

  const callComposerGenerate = async (ai: AIAsset, track?: Record<string, unknown>, clipUID?: string) => {
    const nextAI = withDurationEstimate(ai);
    const body: Record<string, unknown> = track && clipUID
      ? { track, clip_uid: clipUID, project_id: projectId }
      : { ai: nextAI, project_id: projectId };
    const res = await fetch(`/api/apps/composer/ai/generate?project_id=${encodeURIComponent(projectId)}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`${res.status}: ${text.slice(0, 400)}`);
    const data = JSON.parse(text || "{}");
    if (!data.ai) throw new Error("Composer returned no AI asset metadata.");
    return { ai: data.ai as AIAsset, pending: String(data.pending || "") };
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
      const { ai: nextAI, pending } = await callComposerGenerate(clip.ai);
      if (nextAI.status === "generating") {
        const estimated = Number(nextAI.estimated_duration_seconds || clip.ai.estimated_duration_seconds || estimateForAI(clip.ai) || 0) || undefined;
        updateClip(clip.id, {
          duration_mode: clip.duration_mode || defaultDurationMode(clip.ai.media_kind),
          estimated_length: estimated,
          ai: nextAI,
        });
        setStatus(pending || `AI clip queued as job #${nextAI.job_id}.`);
        return;
      }
      const storageId = Number(nextAI.storage_id || 0);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
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
      const { ai: nextAI } = await callComposerGenerate(ai);
      const storageId = Number(nextAI.storage_id || 0);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
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
      const draftBody = draftToBody(draft);
      const audioTrack = ((draftBody.tracks as Record<string, unknown>[]) || []).find((track) => track.type === "audio");
      const { ai: nextAI, pending } = await callComposerGenerate(clip.ai, audioTrack, clip.id);
      if (nextAI.status === "generating") {
        const estimated = Number(nextAI.estimated_duration_seconds || clip.ai.estimated_duration_seconds || estimateForAI(clip.ai) || 0) || undefined;
        updateAudioClip(clip.id, {
          duration_mode: clip.duration_mode || defaultDurationMode(clip.ai.media_kind),
          estimated_length: estimated,
          ai: nextAI,
        });
        setStatus(pending || `AI audio queued as job #${nextAI.job_id}.`);
        return;
      }
      const storageId = Number(nextAI.storage_id || 0);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
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
      <style>{`
        @media (max-width: 1119px) {
          .composer-library-panel, .composer-inspector-panel {
            position: absolute !important;
            top: 0;
            bottom: 0;
            z-index: 80;
            box-shadow: 0 18px 50px rgba(0, 0, 0, .55);
          }
          .composer-library-panel { left: 0; width: min(320px, 86vw) !important; }
          .composer-inspector-panel { right: 0; width: min(340px, 90vw) !important; }
        }
        @media (max-width: 639px) {
          .composer-command-title, .composer-executor { display: none !important; }
        }
      `}</style>
      <header className="border-b border-border px-3 py-2 flex items-center gap-2 bg-bg-card">
        <IconButton label={libraryOpen ? "Hide compositions" : "Show compositions"} onClick={() => setLibraryOpen((value) => !value)}>
          {libraryOpen ? <ChevronLeft size={16} /> : <ChevronRight size={16} />}
        </IconButton>
        <div className="composer-command-title min-w-0 flex-1">
          <div className="text-sm font-semibold truncate">{draft.name || selected?.name || "Untitled composition"}</div>
          <div className="text-xs text-text-dim flex items-center gap-2">
            <span>{clips.length + audioClips.length + textClips.length} clips</span>
            <span>{formatTime(totalDuration)}</span>
            <span>{draft.output.aspect}</span>
            <span>{draft.output.resolution}</span>
          </div>
        </div>
        <div className="hidden md:flex items-center gap-1 border-r border-border pr-2 mr-1">
          <IconButton label="Undo (Cmd/Ctrl+Z)" onClick={undo} disabled={undoStack.current.length === 0}><Undo2 size={16} /></IconButton>
          <IconButton label="Redo (Cmd/Ctrl+Shift+Z)" onClick={redo} disabled={redoStack.current.length === 0}><Redo2 size={16} /></IconButton>
          <IconButton label="Split at playhead (S)" onClick={splitSelectedClip}><Scissors size={16} /></IconButton>
          <IconButton label="Duplicate clip (Cmd/Ctrl+D)" onClick={duplicateSelectedClip}><Copy size={16} /></IconButton>
        </div>
        <BindingStrip bindings={bindings} installId={installId} onChanged={loadBindings} setStatus={setStatus} />
        <select
          value={executor}
          onChange={(e) => setExecutor(e.target.value as "auto" | "local" | "remote")}
          className="composer-executor bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
        >
          <option value="auto">auto</option>
          <option value="local">local</option>
          <option value="remote">remote</option>
        </select>
        <button onClick={save} className="h-8 px-3 text-sm border border-border rounded flex items-center gap-2 hover:bg-bg-input"><Save size={15} />Save</button>
        <button
          onClick={render}
          disabled={rendering}
          className="h-8 px-3 text-sm bg-accent text-bg rounded font-semibold flex items-center gap-2 disabled:opacity-50"
        >
          <Sparkles size={15} />{rendering ? "Rendering..." : "Render"}
        </button>
        <IconButton label={inspectorOpen ? "Hide inspector" : "Show inspector"} onClick={() => setInspectorOpen((value) => !value)}>
          {inspectorOpen ? <ChevronRight size={16} /> : <ChevronLeft size={16} />}
        </IconButton>
      </header>

      <div className="composer-workspace flex-1 min-h-0 flex" style={{ position: "relative" }}>
        {libraryOpen && <Sidebar
          compositions={compositions}
          examples={examples}
          selectedId={selectedId}
          onSelect={(id) => {
            setSelectedId(id);
            if (window.innerWidth < 1120) setLibraryOpen(false);
          }}
          onNew={() => {
            setSelectedId(null);
            if (window.innerWidth < 1120) setLibraryOpen(false);
          }}
          onLoadExample={(example) => {
            loadExample(example);
            if (window.innerWidth < 1120) setLibraryOpen(false);
          }}
        />}

        <main className="flex-1 min-w-0 flex flex-col">
          <nav className="border-b border-border px-3 pt-2 flex gap-1 text-xs">
            <TabButton active={tab === "timeline"} onClick={() => setTab("timeline")}>Timeline</TabButton>
            <TabButton active={tab === "json"} onClick={() => setTab("json")}>JSON</TabButton>
          </nav>
          {detailLoading && selectedId != null && (
            <div className="px-4 py-2 border-b border-border text-xs text-text-dim">
              Loading composition #{selectedId}...
            </div>
          )}

          {tab === "timeline" ? (
            <div className="flex-1 min-h-0 flex">
              <section className="flex-1 min-w-0 flex flex-col p-4 gap-4 overflow-auto">
                <PreviewStage
                  clips={clips}
                  audioClips={audioClips}
                  textClips={textClips}
                  soundtrack={draft.soundtrack}
                  resolved={resolved}
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
                  textClips={textClips}
                  soundtrack={draft.soundtrack}
                  markers={draft.markers}
                  selectedClipId={selectedClipId}
                  playhead={playhead}
                  duration={totalDuration}
                  zoom={timelineZoom}
                  onZoom={setTimelineZoom}
                  onSelect={setSelectedClipId}
  onEditVisual={(id) => setClipEditor({ kind: "visual", id })}
  onEditAudio={(id) => setClipEditor({ kind: "audio", id })}
  onEditText={(id) => setClipEditor({ kind: "text", id })}
                  onEditGap={(start, length) => editGapAsSilence(start, length)}
                  onSeek={setPlayhead}
                  onUpdateVisual={(id, patch) => updateClip(id, patch)}
                  onUpdateAudio={(id, patch) => updateAudioClip(id, patch)}
                  onUpdateText={(id, patch) => updateTextClip(id, patch)}
                  onAdd={addClip}
                  onAddLayer={addVisualLayer}
                  onAddAIVisual={addAIVisualClip}
                  onAddAIAudio={() => addAudioClip("music")}
                  onAddText={addTextClip}
                  onAddSilence={addSilenceClip}
                  onAddAISoundtrack={addAISoundtrack}
                  onBrowse={() => openPicker(clips.length ? { kind: "clip", clipId: clips[0].id } : { kind: "clip", clipId: "" })}
                />
                <RenderPreview render={selectedFull?.latest_render || null} outputFormat={draft.output.format} aspect={draft.output.aspect} onOpen={openLightbox} />
              </section>
              {inspectorOpen && <Inspector
                projectId={projectId}
                draft={draft}
                clip={selectedClip}
                onDraft={updateDraft}
                onClip={updateClip}
                onAudioClip={updateAudioClip}
                onTextClip={updateTextClip}
                onDelete={deleteClip}
                onDeleteAudio={deleteAudioClip}
                onDeleteText={deleteTextClip}
                onMove={moveClip}
                onDeleteComposition={deleteSelected}
                canDeleteComposition={selectedId != null}
                onBrowseClip={(clipId) => openPicker({ kind: "clip", clipId })}
                onBrowseAudio={(clipId) => openPicker({ kind: "audio", clipId })}
                onBrowseSoundtrack={() => openPicker({ kind: "soundtrack" })}
                onAddClip={addClip}
                onAddAudioClip={addAudioClip}
                onAddTextClip={addTextClip}
                onAddSilenceClip={addSilenceClip}
                onAddAISoundtrack={addAISoundtrack}
                onAddAIVisualClip={addAIVisualClip}
                onGenerateClipAI={generateClipAI}
                onGenerateAudioClipAI={generateAudioClipAI}
                onGenerateSoundtrackAI={generateSoundtrackAI}
                aiBusy={aiBusy}
              />}
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

      {lightbox && <Lightbox render={lightbox} outputFormat={draft.output.format} onClose={closeLightbox} />}
      {clipEditor && (
        <ClipEditorModal
          projectId={projectId}
          target={clipEditor}
          visualClip={clipEditor.kind === "visual" ? clips.find((clip) => clip.id === clipEditor.id) || null : null}
          audioClip={clipEditor.kind === "audio" ? audioClips.find((clip) => clip.id === clipEditor.id) || null : null}
          textClip={clipEditor.kind === "text" ? textClips.find((clip) => clip.id === clipEditor.id) || null : null}
          aspect={draft.output.aspect}
          aiBusy={aiBusy}
          onClose={() => setClipEditor(null)}
          onVisualClip={updateClip}
          onAudioClip={updateAudioClip}
          onTextClip={updateTextClip}
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
          onDeleteText={(id) => {
            deleteTextClip(id);
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

function BindingStrip({
  bindings,
  installId,
  onChanged,
  setStatus,
}: {
  bindings: Bindings | null;
  installId: number;
  onChanged: () => void;
  setStatus: (message: string) => void;
}) {
  const [hosts, setHosts] = useState<InstanceHost[]>([]);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (!bindings?.instances_bound) {
      setHosts([]);
      return;
    }
    let cancelled = false;
    fetch("/api/apps/instances/api/instances", { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (!cancelled) setHosts(Array.isArray(data?.instances) ? data.instances : []);
      })
      .catch(() => {
        if (!cancelled) setHosts([]);
      });
    return () => {
      cancelled = true;
    };
  }, [bindings?.instances_bound]);

  if (!bindings) return null;
  const renderMode = bindings.render_executor
    ? bindings.render_executor
    : bindings.render_host_id > 0
      ? `host #${bindings.render_host_id}`
      : "local ffmpeg";
  const saveHost = async (hostId: string) => {
    if (!installId) {
      setStatus("Cannot save render host: install id missing.");
      return;
    }
    setSaving(true);
    try {
      const res = await fetch(`/api/apps/installs/${installId}/config`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ config: { render_host_id: hostId } }),
      });
      const text = await res.text();
      if (!res.ok) throw new Error(`${res.status}: ${text.slice(0, 300)}`);
      setStatus(Number(hostId) > 0 ? `Remote render host set to #${hostId}.` : "Composer renders set to local ffmpeg.");
      onChanged();
    } catch (e) {
      setStatus("Render host save failed: " + (e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="hidden lg:flex items-center gap-2 text-xs text-text-dim">
      <Pill label={renderMode} active />
      <Pill label="storage" active={bindings.storage_bound} />
      <Pill label="media studio" active={bindings.mediastudio_bound} />
      {bindings.instances_bound && (
        <select
          value={String(bindings.render_host_id || 0)}
          onChange={(e) => saveHost(e.target.value)}
          disabled={saving}
          title="Remote render host"
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs text-text disabled:opacity-50"
        >
          <option value="0">local</option>
          {hosts.map((host) => (
            <option key={host.id} value={host.id}>
              {host.name || `host #${host.id}`}{host.status ? ` (${host.status})` : ""}
            </option>
          ))}
          {bindings.render_host_id > 0 && !hosts.some((host) => host.id === bindings.render_host_id) && (
            <option value={bindings.render_host_id}>host #{bindings.render_host_id}</option>
          )}
        </select>
      )}
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

function IconButton({ label, onClick, disabled, children, active }: { label: string; onClick: () => void; disabled?: boolean; children: React.ReactNode; active?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      title={label}
      aria-label={label}
      className={`w-8 h-8 shrink-0 inline-flex items-center justify-center border rounded transition-colors disabled:opacity-35 disabled:cursor-not-allowed ${active ? "border-accent bg-accent/10 text-accent" : "border-border text-text-muted hover:text-text hover:bg-bg-input"}`}
    >
      {children}
    </button>
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
  const [query, setQuery] = useState("");
  const filtered = compositions.filter((composition) => !query.trim() || composition.name.toLowerCase().includes(query.trim().toLowerCase()));
  return (
    <aside className="composer-library-panel w-64 2xl:w-72 shrink-0 border-r border-border bg-bg-card flex flex-col">
      <header className="px-3 py-2 border-b border-border flex items-center gap-2 min-h-12">
        <span className="text-text text-xs font-semibold flex-1">Compositions</span>
        <IconButton label="New composition" onClick={onNew}><Plus size={15} /></IconButton>
      </header>
      <div className="p-2 border-b border-border relative">
        <Search size={14} className="absolute left-4 top-1/2 -translate-y-1/2 text-text-dim" />
        <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search compositions" className="w-full h-8 pl-8 pr-2 bg-bg-input border border-border rounded text-xs outline-none focus:border-accent" />
      </div>
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
        {filtered.length === 0 && <div className="p-3 text-text-dim text-xs">{query ? "No matching compositions." : "No compositions."}</div>}
        {filtered.map((c) => {
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
  clips,
  audioClips,
  textClips,
  soundtrack,
  resolved,
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
  clips: ClipDraft[];
  audioClips: AudioClipDraft[];
  textClips: TextClipDraft[];
  soundtrack: DraftState["soundtrack"];
  resolved: Record<string, ResolvedAsset>;
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
  const stageRef = useRef<HTMLDivElement | null>(null);
  const [muted, setMuted] = useState(false);
  const activeVisuals = clips
    .filter((clip) => playhead >= clip.start && playhead < clip.start + clip.length)
    .sort((a, b) => Number(a.layout?.z_index || a.z_index || 0) - Number(b.layout?.z_index || b.z_index || 0));
  const activeAudio = audioClips.filter((clip) => clip.asset.type !== "silence" && playhead >= clip.start && playhead < clip.start + clip.length);
  const activeText = textClips.filter((text) => playhead >= text.start && playhead < text.start + text.length);
  const audioOnly = clips.length === 0 && audioClips.length > 0;
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden shadow-sm" style={{ flexShrink: 0 }}>
      <div className="h-11 px-3 border-b border-border flex items-center gap-2">
        <IconButton label={playing ? "Pause (Space)" : "Play (Space)"} onClick={onToggle} active={playing}>
          {playing ? <Pause size={16} /> : <Play size={16} />}
        </IconButton>
        <input
          type="range"
          aria-label="Preview playhead"
          min={0}
          max={Math.max(0.1, duration)}
          step={0.1}
          value={Math.min(playhead, duration)}
          onChange={(e) => onSeek(Number(e.target.value))}
          className="flex-1 h-1"
          style={{ accentColor: "var(--apteva-accent, #4ade80)" }}
        />
        <span className="text-xs text-text-dim tabular-nums">{formatTime(playhead)} / {formatTime(duration)}</span>
        <IconButton label={muted ? "Unmute preview" : "Mute preview"} onClick={() => setMuted((value) => !value)}>
          {muted ? <VolumeX size={16} /> : <Volume2 size={16} />}
        </IconButton>
        <IconButton label="Fullscreen preview" onClick={() => stageRef.current?.requestFullscreen?.()}><Maximize2 size={16} /></IconButton>
      </div>
      <div className="p-4 md:p-5 flex items-center justify-center bg-bg min-h-[260px]">
        <div
          ref={stageRef}
          className="relative border border-border overflow-hidden bg-black shadow-lg"
          style={{
            background,
            aspectRatio: aspectRatio(aspect),
            width: `min(100%, ${previewFrameMaxWidth(aspect)})`,
          }}
        >
          {activeVisuals.map((clip, index) => (
            <PreviewVisualLayer
              key={clip.id}
              clip={clip}
              asset={resolved[clip.asset.src]}
              playing={playing}
              playhead={playhead}
              muted={muted || clip.source_audio !== "keep"}
              base={index === 0 && (clip.track_id || "visual-1") === (clips[0]?.track_id || "visual-1")}
            />
          ))}
          {activeVisuals.length === 0 && (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 px-6 text-center">
              {audioOnly ? (
                <>
                  <div className="text-text text-sm font-medium">Audio-only composition</div>
                  <div className="text-text-muted text-xs">
                    {audioClips.length} timed audio clips and silences · {formatTime(duration)}
                  </div>
                </>
              ) : (
                <div className="text-text-muted text-sm">{clips.length ? "No visual at the playhead" : "No visual clips yet"}</div>
              )}
              {clips.length === 0 && !audioOnly && (
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
          {activeVisuals.filter((clip) => clip.text?.body).map((clip) => <div key={`${clip.id}-text`} className={`absolute z-40 left-6 right-6 text-center font-medium ${clip.text?.position === "top" ? "top-6" : clip.text?.position === "center" ? "top-1/2 -translate-y-1/2" : "bottom-6"}`} style={{ color: clip.text?.color || "#fff", fontSize: Math.max(12, clip.text?.font_size || 32), textShadow: "0 1px 3px rgba(0,0,0,.7)" }}>{clip.text?.body}</div>)}
          {activeText.map((text) => (
            <div
              key={text.id}
              className="absolute font-bold pointer-events-none"
              style={previewTextStyle(text)}
            >
              {text.asset.text}
            </div>
          ))}
          {!muted && activeAudio.map((clip) => {
            const url = resolved[clip.asset.src]?.url;
            return url ? <SyncedAudio key={clip.id} src={url} playing={playing} playhead={playhead} start={clip.start} volume={clip.volume} sourceStart={clip.source_start} sourceEnd={clip.source_end} playbackRate={clip.playback_rate} /> : null;
          })}
          {!muted && soundtrack?.src && resolved[soundtrack.src]?.url && (
            <SyncedAudio src={resolved[soundtrack.src].url} playing={playing} playhead={playhead} start={0} volume={soundtrack.volume} loop />
          )}
        </div>
      </div>
    </section>
  );
}

function previewLayerStyle(clip: ClipDraft, base: boolean): React.CSSProperties {
  const layout = clip.layout || {};
  const hasPlacement = layout.width !== undefined || layout.height !== undefined || layout.x !== undefined || layout.y !== undefined || layout.anchor !== undefined || layout.position !== undefined;
  const measure = (value: number | undefined, fallback: string) => value === undefined || value <= 0 ? fallback : value <= 1 ? `${value * 100}%` : `${value}px`;
  const width = measure(layout.width || clip.width, "100%");
  const height = measure(layout.height || clip.height, "100%");
  const margin = layout.margin ?? 20;
  const anchor = layout.anchor || layout.position || "top-left";
  const style: React.CSSProperties = {
    width,
    height,
    opacity: layout.opacity ?? clip.opacity ?? 1,
    borderRadius: layout.border_radius || clip.border_radius || 0,
    boxShadow: layout.shadow || clip.shadow ? "10px 10px 10px rgba(0,0,0,.45)" : undefined,
    overflow: "hidden",
  };
  if (!hasPlacement) return { ...style, inset: 0, width: "100%", height: "100%" };
  if (layout.x !== undefined || layout.y !== undefined) {
    style.left = measure(layout.x, "0");
    style.top = measure(layout.y, "0");
  } else {
    if (anchor.includes("left")) style.left = margin;
    else if (anchor.includes("right")) style.right = margin;
    else { style.left = "50%"; style.transform = "translateX(-50%)"; }
    if (anchor.includes("top")) style.top = margin;
    else if (anchor.includes("bottom")) style.bottom = margin;
    else { style.top = "50%"; style.transform = `${style.transform || ""} translateY(-50%)`.trim(); }
  }
  return style;
}

function PreviewVisualLayer({ clip, asset, playing, playhead, muted, base }: { clip: ClipDraft; asset?: ResolvedAsset; playing: boolean; playhead: number; muted: boolean; base: boolean }) {
  const mediaRef = useRef<HTMLVideoElement | null>(null);
  const url = asset?.url || "";
  const localTime = Math.max(0, playhead - clip.start);
  useEffect(() => {
    const media = mediaRef.current;
    if (!media) return;
    const playbackRate = clip.playback_rate || 1;
    const sourceTime = (clip.source_start || 0) + localTime * playbackRate;
    const cappedTime = clip.source_end ? Math.min(sourceTime, Math.max(0, clip.source_end - 0.01)) : sourceTime;
    if (Number.isFinite(media.duration) && Math.abs(media.currentTime - cappedTime) > 0.35) media.currentTime = Math.min(cappedTime, Math.max(0, media.duration - 0.05));
    media.playbackRate = playbackRate;
    if (playing) media.play().catch(() => {}); else media.pause();
  }, [playing, playhead, clip.start, clip.source_start, clip.source_end, clip.playback_rate, localTime, url]);
  const fit = clip.layout?.fit || clip.fit || "cover";
  const camera = previewCameraAt(clip.transform, localTime);
  const crop = clip.crop;
  const mediaStyle = previewCropStyle(crop, fit, camera) as React.CSSProperties;
  return (
    <div className="absolute" style={previewLayerStyle(clip, base)}>
      {!url ? <div className="w-full h-full border border-dashed border-border bg-bg-card flex items-center justify-center text-xs text-text-dim">{clip.ai?.status === "generating" ? "Generating..." : "Source missing"}</div>
        : clip.asset.type === "image" ? <img src={url} alt="" draggable={false} style={mediaStyle} />
          : <video ref={mediaRef} src={url} muted={muted} playsInline style={mediaStyle} />}
    </div>
  );
}

function SyncedAudio({ src, playing, playhead, start, volume, loop, sourceStart = 0, sourceEnd, playbackRate = 1 }: { src: string; playing: boolean; playhead: number; start: number; volume: number; loop?: boolean; sourceStart?: number; sourceEnd?: number; playbackRate?: number }) {
  const ref = useRef<HTMLAudioElement | null>(null);
  useEffect(() => {
    const audio = ref.current;
    if (!audio) return;
    const raw = sourceStart + Math.max(0, playhead - start) * playbackRate;
    const local = loop && Number.isFinite(audio.duration) && audio.duration > 0 ? raw % audio.duration : raw;
    const capped = sourceEnd ? Math.min(local, Math.max(0, sourceEnd - 0.01)) : local;
    if (Math.abs(audio.currentTime - capped) > 0.35) audio.currentTime = Math.min(capped, Math.max(0, (audio.duration || capped + 1) - 0.05));
    audio.volume = Math.max(0, Math.min(1, volume || 1));
    audio.playbackRate = playbackRate;
    if (playing) audio.play().catch(() => {}); else audio.pause();
  }, [playing, playhead, start, src, volume, loop, sourceStart, sourceEnd, playbackRate]);
  return <audio ref={ref} src={src} preload="metadata" />;
}

function previewTextStyle(text: TextClipDraft): React.CSSProperties {
  const align = text.asset.align || {};
  const position = text.position || {};
  const anchor = position.anchor || "center";
  let left = position.x || (align.horizontal === "left" ? "8%" : align.horizontal === "right" ? "92%" : "50%");
  let top = position.y || (align.vertical === "top" ? "12%" : align.vertical === "bottom" ? "84%" : "50%");
  let transform = "";
  if (anchor.includes("right") || anchor === "right") transform += " translateX(-100%)";
  else if (!anchor.includes("left") && anchor !== "left") transform += " translateX(-50%)";
  if (anchor.includes("bottom") || anchor === "bottom") transform += " translateY(-100%)";
  else if (!anchor.includes("top") && anchor !== "top") transform += " translateY(-50%)";
  return {
    left,
    top,
    transform: transform.trim() || undefined,
    color: text.asset.font?.color || "#ffffff",
    fontSize: Math.max(12, Math.min(96, text.asset.font?.size || 64)) / 2,
    fontWeight: text.asset.font?.weight || 800,
    opacity: text.asset.font?.opacity ?? 1,
    textTransform: text.asset.style?.transform === "uppercase" ? "uppercase" : text.asset.style?.transform === "lowercase" ? "lowercase" : undefined,
    letterSpacing: text.asset.style?.letter_spacing ? `${text.asset.style.letter_spacing}px` : undefined,
    textAlign: align.horizontal || "center",
    WebkitTextStroke: `${text.asset.stroke?.width || 3}px ${text.asset.stroke?.color || "#000000"}`,
    textShadow: text.asset.shadow ? `${(text.asset.shadow.offset_x || 0) / 2}px ${(text.asset.shadow.offset_y || 2) / 2}px 0 ${text.asset.shadow.color || "#ff2f6d"}` : "0 1px 0 rgba(0,0,0,.8)",
    whiteSpace: "pre-wrap",
    overflowWrap: text.asset.style?.wrap ? "anywhere" : undefined,
    lineHeight: text.asset.style?.line_height || 1.22,
    padding: text.asset.style?.padding ? `${text.asset.style.padding / 2}px` : undefined,
    boxSizing: "border-box",
    maxWidth: text.asset.style?.max_width
      ? (text.asset.style.max_width <= 1 ? `${text.asset.style.max_width * 100}%` : `${text.asset.style.max_width / 2}px`)
      : "84%",
    maxHeight: text.asset.style?.max_height
      ? (text.asset.style.max_height <= 1 ? `${text.asset.style.max_height * 100}%` : `${text.asset.style.max_height / 2}px`)
      : undefined,
  };
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
  textClips,
  soundtrack,
  markers,
  selectedClipId,
  playhead,
  duration,
  zoom,
  onZoom,
  onSelect,
  onEditVisual,
  onEditAudio,
  onEditText,
  onEditGap,
  onSeek,
  onUpdateVisual,
  onUpdateAudio,
  onUpdateText,
  onAdd,
  onAddLayer,
  onAddAIVisual,
  onAddAIAudio,
  onAddText,
  onAddSilence,
  onAddAISoundtrack,
  onBrowse,
}: {
  clips: ClipDraft[];
  audioClips: AudioClipDraft[];
  textClips: TextClipDraft[];
  soundtrack: DraftState["soundtrack"];
  markers: TimelineMarker[];
  selectedClipId: string;
  playhead: number;
  duration: number;
  zoom: number;
  onZoom: (zoom: number) => void;
  onSelect: (id: string) => void;
  onEditVisual: (id: string) => void;
  onEditAudio: (id: string) => void;
  onEditText: (id: string) => void;
  onEditGap: (start: number, length: number) => void;
  onSeek: (t: number) => void;
  onUpdateVisual: (id: string, patch: Partial<ClipDraft>) => void;
  onUpdateAudio: (id: string, patch: Partial<AudioClipDraft>) => void;
  onUpdateText: (id: string, patch: Partial<TextClipDraft>) => void;
  onAdd: () => void;
  onAddLayer: () => void;
  onAddAIVisual: (kind: "image" | "video" | "avatar") => void;
  onAddAIAudio: () => void;
  onAddText: () => void;
  onAddSilence: () => void;
  onAddAISoundtrack: () => void;
  onBrowse: () => void;
}) {
  const hasSoundtrack = !!(soundtrack?.src || soundtrack?.ai);
  const hasAny = clips.length > 0 || audioClips.length > 0 || textClips.length > 0 || hasSoundtrack;
  const trackIDs = Array.from(new Set(clips.map((clip) => clip.track_id || "visual-1")));
  const laneInset = 104;
  const pxPerSecond = Math.max(3, 24 * Math.max(0.25, zoom));
  const laneWidth = Math.max(820, Math.ceil(Math.max(duration, 1) * pxPerSecond));
  const timelineWidth = laneWidth + laneInset;
  const rulerHeight = 28;
  const laneHeight = 72;
  const laneCount = trackIDs.length + (textClips.length ? 1 : 0) + 1 + (hasSoundtrack ? 1 : 0);
  const timelineHeight = rulerHeight + laneCount * laneHeight;
  const snapPoints = [0, playhead, ...markers.map((marker) => marker.time), ...clips.flatMap((clip) => [clip.start, clip.start + clip.length]), ...audioClips.flatMap((clip) => [clip.start, clip.start + clip.length]), ...textClips.flatMap((clip) => [clip.start, clip.start + clip.length])];
  const sortedAudio = [...audioClips].sort((a, b) => a.start - b.start);
  const gaps: { start: number; length: number }[] = [];
  let audioCursor = 0;
  for (const clip of sortedAudio) {
    if (clip.start - audioCursor > 0.05) {
      gaps.push({ start: audioCursor, length: clip.start - audioCursor });
    }
    audioCursor = Math.max(audioCursor, clip.start + clip.length);
  }
  if (duration - audioCursor > 0.05) gaps.push({ start: audioCursor, length: duration - audioCursor });
  const tickEvery = pxPerSecond >= 80 ? 1 : pxPerSecond >= 30 ? 5 : pxPerSecond >= 12 ? 10 : 30;
  const ticks = Array.from({ length: Math.floor(Math.max(duration, 1) / tickEvery) + 2 }, (_, i) => i * tickEvery);
  const laneTop = (index: number) => rulerHeight + index * laneHeight;
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden shadow-sm" style={{ flexShrink: 0 }}>
      <header className="min-h-12 px-3 py-2 border-b border-border flex items-center gap-1.5 flex-wrap">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-text-dim mr-auto">Timeline</h2>
        <ToolButton label="AI image" onClick={() => onAddAIVisual("image")} icon={<ImageIcon size={14} />} />
        <ToolButton label="AI video" onClick={() => onAddAIVisual("video")} icon={<Film size={14} />} />
        <ToolButton label="AI avatar" onClick={() => onAddAIVisual("avatar")} icon={<Bot size={14} />} />
        <ToolButton label="Overlay layer" onClick={onAddLayer} icon={<Layers3 size={14} />} />
        <ToolButton label="Text" onClick={onAddText} icon={<Type size={14} />} />
        <ToolButton label="Voice / SFX" onClick={onAddAIAudio} icon={<AudioLines size={14} />} />
        <ToolButton label="Music" onClick={onAddAISoundtrack} icon={<Music2 size={14} />} />
        <ToolButton label="Silence" onClick={onAddSilence} icon={<VolumeX size={14} />} />
        <ToolButton label="Storage" onClick={onBrowse} icon={<Search size={14} />} accent />
        <div className="w-px h-6 bg-border mx-1" />
        <IconButton label="Zoom out" onClick={() => onZoom(Math.max(0.25, Number((zoom / 1.4).toFixed(2))))}><ZoomOut size={15} /></IconButton>
        <button type="button" onClick={() => onZoom(1)} className="h-8 px-2 text-[11px] border border-border rounded hover:bg-bg-input" title="Reset timeline zoom">{Math.round(zoom * 100)}%</button>
        <IconButton label="Zoom in" onClick={() => onZoom(Math.min(16, Number((zoom * 1.4).toFixed(2))))}><ZoomIn size={15} /></IconButton>
      </header>
      <div>
        {!hasAny ? (
          <div className="m-3 border border-dashed border-border rounded bg-bg px-4 py-10 text-center">
            <WandSparkles size={24} className="mx-auto mb-3 text-accent" />
            <div className="text-text text-sm font-medium">Build the first scene</div>
            <div className="text-text-muted text-xs mt-1">Generate media, add an existing asset, or start with an empty clip.</div>
            <div className="mt-3 flex items-center justify-center gap-2 flex-wrap">
              <button type="button" onClick={() => onAddAIVisual("image")} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI image</button>
              <button type="button" onClick={() => onAddAIVisual("video")} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI video</button>
              <button type="button" onClick={() => onAddAIVisual("avatar")} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI avatar</button>
              <button type="button" onClick={onAddText} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Text</button>
              <button type="button" onClick={onAddAISoundtrack} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">AI music</button>
              <button type="button" onClick={onAddAIAudio} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Timed AI audio</button>
              <button type="button" onClick={onAddSilence} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Silence</button>
              <button type="button" onClick={onBrowse} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">Browse storage</button>
            </div>
          </div>
        ) : (
          <div className="overflow-x-auto overflow-y-hidden bg-bg">
            <div
              role="presentation"
              onClick={(e) => {
                const rect = e.currentTarget.getBoundingClientRect();
                const x = Math.max(0, e.clientX - rect.left - laneInset);
                onSeek(Math.min(duration, x / pxPerSecond));
              }}
              className="bg-bg select-none"
              style={{ position: "relative", display: "block", minHeight: timelineHeight, width: timelineWidth, minWidth: timelineWidth, overflow: "hidden", cursor: "default" }}
            >
              <div className="absolute top-0 left-0 h-7 bg-bg-card border-r border-b border-border z-30" style={{ width: laneInset }} />
              <div className="absolute top-0 h-7 border-b border-border bg-bg-card" style={{ left: laneInset, width: laneWidth }}>
                {ticks.map((tick) => <div key={tick} className="absolute top-0 bottom-0 border-l border-border/80 text-[9px] text-text-dim pt-1 pl-1" style={{ left: tick * pxPerSecond }}>{formatTime(tick)}</div>)}
                {markers.map((marker, index) => (
                  <button
                    key={marker.id || `${marker.type}-${marker.time}-${index}`}
                    type="button"
                    className="absolute top-0 h-7 w-3 -ml-1.5 z-20 group"
                    style={{ left: Math.max(0, marker.time) * pxPerSecond }}
                    title={`${marker.label || marker.type} · ${formatTime(marker.time)}`}
                    onClick={(event) => { event.stopPropagation(); onSeek(marker.time); }}
                  >
                    <span className="block mx-auto w-0 h-0 border-l-[5px] border-r-[5px] border-t-[7px] border-l-transparent border-r-transparent border-t-amber-400" />
                    <span className="block mx-auto h-5 w-px bg-amber-400/80" />
                  </button>
                ))}
              </div>
              <div
                className="bg-accent z-40 pointer-events-none"
                style={{ position: "absolute", top: 0, bottom: 0, left: laneInset + Math.min(laneWidth, Math.max(0, playhead) * pxPerSecond), width: 1 }}
              />
              {trackIDs.map((trackID, index) => <TimelineLane key={trackID} label={index === 0 ? "Main visual" : `Visual ${index + 1}`} icon={<Film size={13} />} top={laneTop(index)} laneInset={laneInset} laneWidth={laneWidth}>
                {clips.filter((clip) => (clip.track_id || "visual-1") === trackID).map((clip) => <TimelineClipBlock key={clip.id} id={clip.id} kind="visual" start={clip.start} length={clip.length} pxPerSecond={pxPerSecond} selected={clip.id === selectedClipId} label={visualClipLabel(clip)} subtitle={`${clip.ai?.status || clip.asset.type} · ${clip.length.toFixed(1)}s`} snapPoints={snapPoints} onSelect={() => { onSelect(clip.id); onSeek(clip.start); }} onEdit={() => onEditVisual(clip.id)} onCommit={(start, length) => onUpdateVisual(clip.id, { start, length })} />)}
              </TimelineLane>)}
              {textClips.length > 0 && <TimelineLane label="Text" icon={<Type size={13} />} top={laneTop(trackIDs.length)} laneInset={laneInset} laneWidth={laneWidth}>
                {textClips.map((clip) => <TimelineClipBlock key={clip.id} id={clip.id} kind="text" start={clip.start} length={clip.length} pxPerSecond={pxPerSecond} selected={clip.id === selectedClipId} label={clip.asset.text || "Text"} subtitle={`${clip.length.toFixed(1)}s`} snapPoints={snapPoints} onSelect={() => { onSelect(clip.id); onSeek(clip.start); }} onEdit={() => onEditText(clip.id)} onCommit={(start, length) => onUpdateText(clip.id, { start, length })} />)}
              </TimelineLane>}
              <TimelineLane label="Audio" icon={<AudioLines size={13} />} top={laneTop(trackIDs.length + (textClips.length ? 1 : 0))} laneInset={laneInset} laneWidth={laneWidth}>
                {gaps.map((gap, index) => {
                  return <TimelineClipBlock key={`gap-${index}-${gap.start}`} id={`gap-${index}`} kind="gap" start={gap.start} length={gap.length} pxPerSecond={pxPerSecond} label="Silence" subtitle={`${gap.length.toFixed(1)}s`} snapPoints={snapPoints} onSelect={() => onSeek(gap.start)} onEdit={() => onEditGap(gap.start, gap.length)} />;
                })}
                {audioClips.map((clip) => <TimelineClipBlock key={clip.id} id={clip.id} kind={clip.asset.type === "silence" ? "silence" : "audio"} start={clip.start} length={clip.length} pxPerSecond={pxPerSecond} selected={clip.id === selectedClipId} label={audioClipLabel(clip)} subtitle={audioClipSubtitle(clip)} snapPoints={snapPoints} onSelect={() => { onSelect(clip.id); onSeek(clip.start); }} onEdit={() => onEditAudio(clip.id)} onCommit={(start, length) => onUpdateAudio(clip.id, { start, length })} />)}
              </TimelineLane>
              {hasSoundtrack && <TimelineLane label="Soundtrack" icon={<Music2 size={13} />} top={laneTop(trackIDs.length + (textClips.length ? 1 : 0) + 1)} laneInset={laneInset} laneWidth={laneWidth}>
                <TimelineClipBlock id="soundtrack" kind="music" start={0} length={Math.max(0.1, duration)} pxPerSecond={pxPerSecond} label={soundtrack?.ai ? `${aiKindLabel(soundtrack.ai.media_kind)} - ${shortPrompt(soundtrack.ai.prompt, "soundtrack")}` : soundtrack?.src || "Soundtrack"} subtitle={`full timeline · volume ${(soundtrack?.volume ?? 1).toFixed(2)}`} snapPoints={snapPoints} onSelect={() => onSeek(0)} onEdit={() => {}} />
              </TimelineLane>}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}

function ToolButton({ label, icon, onClick, accent }: { label: string; icon: React.ReactNode; onClick: () => void; accent?: boolean }) {
  return <button type="button" aria-label={label} title={label} onClick={onClick} className={`h-8 px-2 inline-flex items-center gap-1.5 text-[11px] border rounded ${accent ? "border-accent text-accent hover:bg-accent hover:text-bg" : "border-border text-text-muted hover:text-text hover:bg-bg-input"}`}>{icon}<span className="hidden 2xl:inline">{label}</span></button>;
}

function TimelineLane({ label, icon, top, laneInset, laneWidth, children }: { label: string; icon: React.ReactNode; top: number; laneInset: number; laneWidth: number; children: React.ReactNode }) {
  return <div className="absolute border-b border-border/70" style={{ position: "absolute", top, left: 0, width: laneInset + laneWidth, height: 72 }}>
    <div className="absolute z-20 bg-bg-card border-r border-border px-3 flex items-center gap-2 text-[10px] uppercase tracking-wide text-text-dim" style={{ position: "absolute", left: 0, top: 0, bottom: 0, width: laneInset }}>{icon}<span className="truncate">{label}</span></div>
    <div className="absolute" style={{ position: "absolute", left: laneInset, top: 6, bottom: 6, width: laneWidth }}>{children}</div>
  </div>;
}

type TimelineBlockKind = "visual" | "audio" | "music" | "silence" | "gap" | "text";

function snapTimelineValue(value: number, points: number[], threshold: number): number {
  const rounded = Math.round(value * 10) / 10;
  let best = rounded;
  let distance = threshold;
  for (const point of points) {
    const nextDistance = Math.abs(point - value);
    if (nextDistance < distance) { best = point; distance = nextDistance; }
  }
  return Math.max(0, Number(best.toFixed(3)));
}

function TimelineClipBlock({ id, kind, start, length, pxPerSecond, selected, label, subtitle, snapPoints, onSelect, onEdit, onCommit }: { id: string; kind: TimelineBlockKind; start: number; length: number; pxPerSecond: number; selected?: boolean; label: string; subtitle: string; snapPoints: number[]; onSelect: () => void; onEdit: () => void; onCommit?: (start: number, length: number) => void }) {
  const gesture = useRef<{ mode: "move" | "left" | "right"; x: number; start: number; length: number } | null>(null);
  const [preview, setPreview] = useState<{ start: number; length: number } | null>(null);
  const current = preview || { start, length };
  const colors: Record<TimelineBlockKind, string> = {
    visual: "bg-accent/10 border-accent/45",
    audio: "bg-emerald-500/10 border-emerald-500/45",
    music: "bg-fuchsia-500/10 border-fuchsia-500/45",
    silence: "bg-bg-input border-border border-dashed",
    gap: "bg-transparent border-border border-dashed text-text-dim",
    text: "bg-sky-500/10 border-sky-500/45",
  };
  const begin = (event: React.PointerEvent<HTMLDivElement>, mode: "move" | "left" | "right") => {
    if (!onCommit || kind === "gap") return;
    event.stopPropagation();
    event.currentTarget.setPointerCapture(event.pointerId);
    gesture.current = { mode, x: event.clientX, start, length };
    onSelect();
  };
  const move = (event: React.PointerEvent<HTMLDivElement>) => {
    const active = gesture.current;
    if (!active) return;
    const delta = (event.clientX - active.x) / pxPerSecond;
    const threshold = 8 / pxPerSecond;
    if (active.mode === "move") setPreview({ start: snapTimelineValue(active.start + delta, snapPoints, threshold), length: active.length });
    if (active.mode === "left") {
      const nextStart = snapTimelineValue(Math.min(active.start + active.length - 0.1, active.start + delta), snapPoints, threshold);
      setPreview({ start: nextStart, length: Math.max(0.1, active.start + active.length - nextStart) });
    }
    if (active.mode === "right") {
      const nextEnd = snapTimelineValue(Math.max(active.start + 0.1, active.start + active.length + delta), snapPoints, threshold);
      setPreview({ start: active.start, length: Math.max(0.1, nextEnd - active.start) });
    }
  };
  const finish = () => {
    if (preview && onCommit) onCommit(preview.start, Number(preview.length.toFixed(3)));
    gesture.current = null;
    setPreview(null);
  };
  return <div role="button" aria-label={`${label}, starts at ${current.start.toFixed(1)} seconds, length ${current.length.toFixed(1)} seconds`} tabIndex={0} title={`${label} · ${current.start.toFixed(1)}s–${(current.start + current.length).toFixed(1)}s`} onClick={(event) => { event.stopPropagation(); onSelect(); }} onDoubleClick={(event) => { event.stopPropagation(); onEdit(); }} onKeyDown={(event) => { if (event.key === "Enter") onEdit(); }} onPointerDown={(event) => begin(event, "move")} onPointerMove={move} onPointerUp={finish} onPointerCancel={finish} className={`absolute border rounded overflow-hidden cursor-grab active:cursor-grabbing ${colors[kind]} ${selected ? "ring-2 ring-accent ring-offset-1 ring-offset-bg z-10" : "hover:border-text-muted"}`} style={{ position: "absolute", top: 0, bottom: 0, left: current.start * pxPerSecond, width: Math.max(28, current.length * pxPerSecond), touchAction: "none" }}>
    {onCommit && <div className="absolute w-2 cursor-ew-resize z-20" style={{ position: "absolute", left: 0, top: 0, bottom: 0 }} onPointerDown={(event) => begin(event, "left")} />}
    <div className="h-full px-2.5 flex flex-col justify-center pointer-events-none min-w-0">
      <span className="text-[11px] font-medium text-text truncate">{label}</span>
      <span className="text-[10px] text-text-dim truncate mt-0.5">{subtitle}</span>
    </div>
    {onCommit && <div className="absolute w-2 cursor-ew-resize z-20" style={{ position: "absolute", right: 0, top: 0, bottom: 0 }} onPointerDown={(event) => begin(event, "right")} />}
  </div>;
}

function VisualLayoutEditor({ clip, onChange }: { clip: ClipDraft; onChange: (patch: Partial<ClipDraft>) => void }) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const layout = clip.layout || {};
  const update = (patch: Partial<ClipLayout>) => onChange({ layout: { ...layout, ...patch } });
  return <div className="border-t border-border pt-3 space-y-2">
    <div className="text-[10px] uppercase tracking-wide text-text-dim">Layout</div>
    <div className="grid grid-cols-2 gap-2">
      <Field label="Fit"><select value={layout.fit || clip.fit || "cover"} onChange={(e) => update({ fit: e.target.value as ClipLayout["fit"] })} className={field}><option value="cover">Cover</option><option value="contain">Contain</option><option value="crop">Crop</option><option value="stretch">Stretch</option><option value="none">Original</option></select></Field>
      <Field label="Anchor"><select value={layout.anchor || "top-left"} onChange={(e) => update({ anchor: e.target.value as ClipLayout["anchor"] })} className={field}><option value="top-left">Top left</option><option value="top">Top</option><option value="top-right">Top right</option><option value="left">Left</option><option value="center">Center</option><option value="right">Right</option><option value="bottom-left">Bottom left</option><option value="bottom">Bottom</option><option value="bottom-right">Bottom right</option></select></Field>
      <Field label="Width (0–1 or px)"><input type="number" min={0} step={0.01} value={layout.width ?? 0} onChange={(e) => update({ width: Number(e.target.value) || undefined })} className={field} /></Field>
      <Field label="Height (0–1 or px)"><input type="number" min={0} step={0.01} value={layout.height ?? 0} onChange={(e) => update({ height: Number(e.target.value) || undefined })} className={field} /></Field>
      <Field label="X (0–1 or px)"><input type="number" min={0} step={0.01} value={layout.x ?? 0} onChange={(e) => update({ x: Number(e.target.value) || undefined })} className={field} /></Field>
      <Field label="Y (0–1 or px)"><input type="number" min={0} step={0.01} value={layout.y ?? 0} onChange={(e) => update({ y: Number(e.target.value) || undefined })} className={field} /></Field>
      <Field label={`Opacity ${Math.round((layout.opacity ?? 1) * 100)}%`}><input type="range" min={0.05} max={1} step={0.05} value={layout.opacity ?? 1} onChange={(e) => update({ opacity: Number(e.target.value) })} className="w-full" /></Field>
      <Field label="Corner radius"><input type="number" min={0} step={1} value={layout.border_radius ?? 0} onChange={(e) => update({ border_radius: Number(e.target.value) || undefined })} className={field} /></Field>
    </div>
    <label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={!!layout.shadow} onChange={(e) => update({ shadow: e.target.checked || undefined })} />Drop shadow</label>
  </div>;
}

function SourceFramingEditor({ clip, onChange }: { clip: ClipDraft; onChange: (patch: Partial<ClipDraft>) => void }) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const transform = clip.transform;
  const keyframes = transform?.keyframes || [];
  const updateTransform = (patch: Partial<SourceTransform>) => onChange({ transform: { x: 0.5, y: 0.5, scale: 1, ...transform, ...patch } });
  const updateKeyframe = (index: number, patch: Partial<TransformKeyframe>) => {
    const next = keyframes.map((keyframe, current) => current === index ? { ...keyframe, ...patch } : keyframe);
    updateTransform({ keyframes: next.sort((a, b) => a.time - b.time) });
  };
  const addKeyframe = () => {
    const last = keyframes[keyframes.length - 1];
    const time = last ? Math.min(clip.length, last.time + Math.max(0.1, clip.length / 4)) : Math.max(0.1, clip.length / 2);
    updateTransform({ keyframes: [...keyframes, { time, x: last?.x ?? transform?.x ?? 0.5, y: last?.y ?? transform?.y ?? 0.5, scale: last?.scale || transform?.scale || 1, easing: "ease_in_out" }] });
  };
  return <div className="border-t border-border pt-3 space-y-3">
    <div className="flex items-center gap-2">
      <div className="text-[10px] uppercase tracking-wide text-text-dim flex-1">Source framing</div>
      {(clip.source_start || clip.source_end || (clip.playback_rate && clip.playback_rate !== 1) || clip.crop || clip.transform) && <button type="button" onClick={() => onChange({ source_start: undefined, source_end: undefined, playback_rate: undefined, crop: undefined, transform: undefined })} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Reset</button>}
    </div>
    {clip.asset.type === "video" && <div className="grid grid-cols-3 gap-2">
      <Field label="Source in (seconds)"><input type="number" min={0} step={0.1} value={clip.source_start ?? 0} onChange={(e) => onChange({ source_start: Math.max(0, Number(e.target.value)) || undefined })} className={field} /></Field>
      <Field label="Source out (seconds)"><input type="number" min={0} step={0.1} value={clip.source_end ?? 0} onChange={(e) => onChange({ source_end: Math.max(0, Number(e.target.value)) || undefined })} className={field} /></Field>
      <Field label="Playback rate"><input type="number" min={0.25} max={16} step={0.25} value={clip.playback_rate ?? 1} onChange={(e) => { const rate = Number(e.target.value); onChange({ playback_rate: rate === 1 ? undefined : rate }); }} className={field} /></Field>
    </div>}
    <div className="flex items-center gap-2">
      <label className="flex items-center gap-2 text-xs text-text-muted flex-1"><input type="checkbox" checked={!!clip.crop} onChange={(e) => onChange({ crop: e.target.checked ? { x: 0, y: 0, width: 1, height: 1 } : undefined })} />Crop source</label>
      <label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={!!transform} onChange={(e) => onChange({ transform: e.target.checked ? { x: 0.5, y: 0.5, scale: 1, keyframes: [] } : undefined })} />Pan and zoom</label>
    </div>
    {clip.crop && <div className="grid grid-cols-4 gap-2">
      <Field label="Crop X"><input type="number" min={0} max={1} step={0.01} value={clip.crop.x} onChange={(e) => onChange({ crop: { ...clip.crop!, x: Number(e.target.value) } })} className={field} /></Field>
      <Field label="Crop Y"><input type="number" min={0} max={1} step={0.01} value={clip.crop.y} onChange={(e) => onChange({ crop: { ...clip.crop!, y: Number(e.target.value) } })} className={field} /></Field>
      <Field label="Width"><input type="number" min={0.01} max={1} step={0.01} value={clip.crop.width} onChange={(e) => onChange({ crop: { ...clip.crop!, width: Number(e.target.value) } })} className={field} /></Field>
      <Field label="Height"><input type="number" min={0.01} max={1} step={0.01} value={clip.crop.height} onChange={(e) => onChange({ crop: { ...clip.crop!, height: Number(e.target.value) } })} className={field} /></Field>
    </div>}
    {transform && <div className="space-y-2">
      <div className="grid grid-cols-3 gap-2">
        <Field label="Focus X"><input type="number" min={0} max={1} step={0.01} value={transform.x ?? 0.5} onChange={(e) => updateTransform({ x: Number(e.target.value) })} className={field} /></Field>
        <Field label="Focus Y"><input type="number" min={0} max={1} step={0.01} value={transform.y ?? 0.5} onChange={(e) => updateTransform({ y: Number(e.target.value) })} className={field} /></Field>
        <Field label="Zoom"><input type="number" min={1} max={8} step={0.1} value={transform.scale || 1} onChange={(e) => updateTransform({ scale: Number(e.target.value) })} className={field} /></Field>
      </div>
      {keyframes.map((keyframe, index) => <div key={`${index}-${keyframe.time}`} className="grid grid-cols-6 gap-1.5 items-end border border-border rounded p-2">
        <Field label="Time"><input type="number" min={0} max={clip.length} step={0.1} value={keyframe.time} onChange={(e) => updateKeyframe(index, { time: Number(e.target.value) })} className={field} /></Field>
        <Field label="X"><input type="number" min={0} max={1} step={0.01} value={keyframe.x ?? transform.x ?? 0.5} onChange={(e) => updateKeyframe(index, { x: Number(e.target.value) })} className={field} /></Field>
        <Field label="Y"><input type="number" min={0} max={1} step={0.01} value={keyframe.y ?? transform.y ?? 0.5} onChange={(e) => updateKeyframe(index, { y: Number(e.target.value) })} className={field} /></Field>
        <Field label="Zoom"><input type="number" min={1} max={8} step={0.1} value={keyframe.scale || transform.scale || 1} onChange={(e) => updateKeyframe(index, { scale: Number(e.target.value) })} className={field} /></Field>
        <Field label="Easing"><select value={keyframe.easing || "linear"} onChange={(e) => updateKeyframe(index, { easing: e.target.value as TransformKeyframe["easing"] })} className={field}><option value="linear">Linear</option><option value="ease_in">Ease in</option><option value="ease_out">Ease out</option><option value="ease_in_out">Ease in/out</option></select></Field>
        <button type="button" onClick={() => updateTransform({ keyframes: keyframes.filter((_, current) => current !== index) })} className="h-9 text-xs border border-red/50 text-red rounded hover:bg-red/10">Remove</button>
      </div>)}
      <button type="button" onClick={addKeyframe} className="w-full text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Add pan/zoom keyframe</button>
    </div>}
  </div>;
}

function TimingEditor({
  timing,
  defaultSource,
  onChange,
}: {
  timing?: Timing;
  defaultSource: string;
  onChange: (timing?: Timing) => void;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const nextTiming = timing || {};
  const update = (patch: Partial<Timing>) => onChange({ ...nextTiming, ...patch });
  return (
    <div className="border border-border rounded p-2 space-y-2">
      <div className="flex items-center gap-2">
        <span className="text-[10px] uppercase tracking-wide text-text-dim flex-1">Timing</span>
        <button type="button" onClick={() => onChange(undefined)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">
          Clear
        </button>
      </div>
      <div className="grid grid-cols-3 gap-2">
        <Field label="Mode">
          <select value={nextTiming.mode || "fixed"} onChange={(e) => update({ mode: e.target.value as Timing["mode"] })} className={field}>
            <option value="fixed">Fixed</option>
            <option value="fit_source">Fit source</option>
            <option value="fit_group">Fit group</option>
            <option value="fit_timeline">Fit timeline</option>
            <option value="fit_generated">Fit generated</option>
          </select>
        </Field>
        <Field label="Source">
          <input
            value={nextTiming.source || defaultSource}
            onChange={(e) => update({ source: e.target.value })}
            placeholder={defaultSource}
            className={field}
          />
        </Field>
        <Field label="Behavior">
          <select value={nextTiming.behavior || ""} onChange={(e) => update({ behavior: (e.target.value || undefined) as Timing["behavior"] })} className={field}>
            <option value="">Default</option>
            <option value="trim">Trim</option>
            <option value="pad">Pad</option>
            <option value="loop">Loop</option>
            <option value="trim_or_loop">Trim or loop</option>
            <option value="stretch">Stretch</option>
            <option value="regenerate">Regenerate</option>
          </select>
        </Field>
        <Field label="Reflow">
          <select value={nextTiming.reflow || "following"} onChange={(e) => update({ reflow: e.target.value as Timing["reflow"] })} className={field}>
            <option value="none">None</option>
            <option value="following">Following</option>
            <option value="track">Track</option>
            <option value="linked_group">Linked group</option>
            <option value="composition">Composition</option>
          </select>
        </Field>
        <Field label="Padding after">
          <input type="number" min={0} step={0.1} value={nextTiming.padding_after ?? 0} onChange={(e) => update({ padding_after: Number(e.target.value) || undefined })} className={field} />
        </Field>
        <Field label="Max length">
          <input type="number" min={0} step={0.1} value={nextTiming.max_length ?? 0} onChange={(e) => update({ max_length: Number(e.target.value) || undefined })} className={field} />
        </Field>
      </div>
      <button
        type="button"
        onClick={() => onChange({ mode: "fit_source", source: defaultSource, behavior: "loop", reflow: "following" })}
        className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input"
      >
        Fit source with loop
      </button>
    </div>
  );
}

function ClipEditorModal({
  projectId,
  target,
  visualClip,
  audioClip,
  textClip,
  aspect,
  aiBusy,
  onClose,
  onVisualClip,
  onAudioClip,
  onTextClip,
  onBrowseVisual,
  onBrowseAudio,
  onGenerateVisual,
  onGenerateAudio,
  onDeleteVisual,
  onDeleteAudio,
  onDeleteText,
}: {
  projectId: string;
  target: ClipEditorTarget;
  visualClip: ClipDraft | null;
  audioClip: AudioClipDraft | null;
  textClip: TextClipDraft | null;
  aspect: Aspect;
  aiBusy: string;
  onClose: () => void;
  onVisualClip: (id: string, patch: Partial<ClipDraft>) => void;
  onAudioClip: (id: string, patch: Partial<AudioClipDraft>) => void;
  onTextClip: (id: string, patch: Partial<TextClipDraft>) => void;
  onBrowseVisual: (id: string) => void;
  onBrowseAudio: (id: string) => void;
  onGenerateVisual: (clip: ClipDraft) => void;
  onGenerateAudio: (clip: AudioClipDraft) => void;
  onDeleteVisual: (id: string) => void;
  onDeleteAudio: (id: string) => void;
  onDeleteText: (id: string) => void;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  const missing = target.kind === "visual" ? !visualClip : target.kind === "audio" ? !audioClip : !textClip;
  return (
    <div className="fixed inset-0 bg-black/70 flex items-center justify-center p-6" style={{ zIndex: 9997 }} onClick={onClose}>
      <div className="bg-bg border border-border rounded shadow-xl w-full max-w-2xl max-h-[88vh] overflow-auto" onClick={(e) => e.stopPropagation()}>
        <header className="px-4 py-3 border-b border-border flex items-center gap-2">
          <div className="min-w-0 flex-1">
            <div className="text-sm text-text font-medium">{target.kind === "visual" ? "Edit visual clip" : target.kind === "audio" ? "Edit audio clip" : "Edit text overlay"}</div>
            <div className="text-xs text-text-dim">{target.kind === "text" ? "Timed overlay text, styling, and animation" : "Storage, URL, or Media Studio generated source"}</div>
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
              <Field label="Track">
                <input value={visualClip.track_id || "visual-1"} onChange={(e) => onVisualClip(visualClip.id, { track_id: e.target.value || "visual-1" })} className={field} />
              </Field>
              <Field label="Start">
                <input type="number" min={0} step={0.1} value={visualClip.start} onChange={(e) => onVisualClip(visualClip.id, { start: Math.max(0, Number(e.target.value)) })} className={field} />
              </Field>
              <Field label="Length">
                <input type="number" min={0.1} step={0.1} value={visualClip.length} onChange={(e) => onVisualClip(visualClip.id, { length: Number(e.target.value) })} className={field} />
              </Field>
              {visualClip.asset.type === "video" && (
                <Field label="Source audio">
                  <select value={visualClip.source_audio || "auto"} onChange={(e) => onVisualClip(visualClip.id, { source_audio: e.target.value as ClipDraft["source_audio"] })} className={field}>
                    <option value="auto">Auto</option>
                    <option value="keep">Keep</option>
                    <option value="mute">Mute</option>
                  </select>
                </Field>
              )}
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
            <TimingEditor
              timing={visualClip.timing}
              defaultSource="track:audio"
              onChange={(timing) => onVisualClip(visualClip.id, { timing })}
            />
            <VisualLayoutEditor clip={visualClip} onChange={(patch) => onVisualClip(visualClip.id, patch)} />
            <SourceFramingEditor clip={visualClip} onChange={(patch) => onVisualClip(visualClip.id, patch)} />
            {!visualClip.ai && (
              <div className="grid grid-cols-3 gap-2">
                <button type="button" onClick={() => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: "image" }, duration_mode: defaultDurationMode("image"), ai: defaultAI("image", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate image</button>
                <button type="button" onClick={() => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: "video" }, duration_mode: defaultDurationMode("video"), ai: defaultAI("video", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate video</button>
                <button type="button" onClick={() => onVisualClip(visualClip.id, { asset: { ...visualClip.asset, type: "video" }, duration_mode: defaultDurationMode("avatar"), ai: defaultAI("avatar", aspect) })} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">Generate avatar</button>
              </div>
            )}
            {visualClip.ai && (
              <AIAssetEditor
                projectId={projectId}
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
        ) : target.kind === "text" && textClip ? (
          <div className="p-4 space-y-4">
            <Field label="Text">
              <textarea
                value={textClip.asset.text}
                onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, text: e.target.value } })}
                className={`${field} resize-y`}
                rows={3}
              />
            </Field>
            <div className="grid grid-cols-3 gap-2">
              <Field label="Start">
                <input type="number" min={0} step={0.1} value={textClip.start} onChange={(e) => onTextClip(textClip.id, { start: Number(e.target.value) })} className={field} />
              </Field>
              <Field label="Length">
                <input type="number" min={0.1} step={0.1} value={textClip.length} onChange={(e) => onTextClip(textClip.id, { length: Number(e.target.value) })} className={field} />
              </Field>
              <Field label="Size">
                <input type="number" min={12} max={180} value={textClip.asset.font?.size || 64} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, font: { ...(textClip.asset.font || {}), size: Number(e.target.value) } } })} className={field} />
              </Field>
              <Field label="Color">
                <input type="color" value={textClip.asset.font?.color || "#ffffff"} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, font: { ...(textClip.asset.font || {}), color: e.target.value } } })} className="w-full h-9 bg-bg-input border border-border rounded" />
              </Field>
              <Field label="Weight">
                <input type="number" min={100} max={900} step={100} value={textClip.asset.font?.weight || 800} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, font: { ...(textClip.asset.font || {}), weight: Number(e.target.value) } } })} className={field} />
              </Field>
              <Field label="Transform">
                <select value={textClip.asset.style?.transform || "uppercase"} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), transform: e.target.value as any } } })} className={field}>
                  <option value="none">None</option>
                  <option value="uppercase">Uppercase</option>
                  <option value="lowercase">Lowercase</option>
                  <option value="capitalize">Capitalize</option>
                </select>
              </Field>
              <Field label="Horizontal">
                <select value={textClip.asset.align?.horizontal || "center"} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, align: { ...(textClip.asset.align || {}), horizontal: e.target.value as any } } })} className={field}>
                  <option value="left">Left</option>
                  <option value="center">Center</option>
                  <option value="right">Right</option>
                </select>
              </Field>
              <Field label="Vertical">
                <select value={textClip.asset.align?.vertical || "center"} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, align: { ...(textClip.asset.align || {}), vertical: e.target.value as any } } })} className={field}>
                  <option value="top">Top</option>
                  <option value="center">Center</option>
                  <option value="bottom">Bottom</option>
                </select>
              </Field>
              <Field label="Letter spacing">
                <input type="number" min={0} max={40} value={textClip.asset.style?.letter_spacing || 0} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), letter_spacing: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <Field label="Line height">
                <input type="number" min={0.5} max={4} step={0.05} value={textClip.asset.style?.line_height || 1.22} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), line_height: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <Field label="Max width">
                <input type="number" min={0} step={0.05} value={textClip.asset.style?.max_width || 0.84} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), max_width: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <Field label="Max height">
                <input type="number" min={0} step={0.05} value={textClip.asset.style?.max_height || 0.8} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), max_height: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <Field label="Minimum font size">
                <input type="number" min={8} max={512} step={1} value={textClip.asset.style?.min_font_size || 12} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), min_font_size: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <Field label="Padding">
                <input type="number" min={0} step={1} value={textClip.asset.style?.padding || 0} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), padding: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <Field label="Safe area">
                <input type="number" min={0} max={0.49} step={0.01} value={textClip.asset.style?.safe_area || 0.05} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), safe_area: Number(e.target.value) || undefined } } })} className={field} />
              </Field>
              <label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={!!textClip.asset.style?.wrap} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), wrap: e.target.checked || undefined } } })} />Wrap text</label>
              <label className="flex items-center gap-2 text-xs text-text-muted"><input type="checkbox" checked={!!textClip.asset.style?.auto_size} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, style: { ...(textClip.asset.style || {}), auto_size: e.target.checked || undefined } } })} />Auto-size to fit</label>
              <Field label="Stroke width">
                <input type="number" min={0} max={20} value={textClip.asset.stroke?.width ?? 3} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, stroke: { ...(textClip.asset.stroke || {}), width: Number(e.target.value) } } })} className={field} />
              </Field>
              <Field label="Stroke color">
                <input type="color" value={textClip.asset.stroke?.color || "#000000"} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, stroke: { ...(textClip.asset.stroke || {}), color: e.target.value } } })} className="w-full h-9 bg-bg-input border border-border rounded" />
              </Field>
              <Field label="Glow/shadow">
                <input type="color" value={textClip.asset.shadow?.color || "#ff2f6d"} onChange={(e) => onTextClip(textClip.id, { asset: { ...textClip.asset, shadow: { ...(textClip.asset.shadow || {}), color: e.target.value, offset_y: textClip.asset.shadow?.offset_y ?? 2, opacity: textClip.asset.shadow?.opacity ?? 0.65 } } })} className="w-full h-9 bg-bg-input border border-border rounded" />
              </Field>
              <Field label="Animate in">
                <select value={textClip.animation?.in?.preset || "fade_up"} onChange={(e) => onTextClip(textClip.id, { animation: { ...(textClip.animation || {}), in: { ...(textClip.animation?.in || {}), preset: e.target.value as TextAnimationPreset } } })} className={field}>
                  {TEXT_ANIMATIONS.map((name) => <option key={name} value={name}>{name}</option>)}
                </select>
              </Field>
              <Field label="In duration">
                <input type="number" min={0} step={0.1} value={textClip.animation?.in?.duration ?? 0.6} onChange={(e) => onTextClip(textClip.id, { animation: { ...(textClip.animation || {}), in: { ...(textClip.animation?.in || {}), duration: Number(e.target.value) } } })} className={field} />
              </Field>
              <Field label="Animate out">
                <select value={textClip.animation?.out?.preset || "fade"} onChange={(e) => onTextClip(textClip.id, { animation: { ...(textClip.animation || {}), out: { ...(textClip.animation?.out || {}), preset: e.target.value as TextAnimationPreset } } })} className={field}>
                  {TEXT_ANIMATIONS.map((name) => <option key={name} value={name}>{name}</option>)}
                </select>
              </Field>
            </div>
            <div className="flex justify-between gap-2">
              <button type="button" onClick={() => onDeleteText(textClip.id)} className="text-sm px-3 py-1.5 border border-red/50 text-red rounded hover:bg-red/10">Delete text</button>
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
              {audioClip.asset.type !== "silence" && <Field label="Source in"><input type="number" min={0} step={0.1} value={audioClip.source_start ?? 0} onChange={(e) => onAudioClip(audioClip.id, { source_start: Math.max(0, Number(e.target.value)) || undefined })} className={field} /></Field>}
              {audioClip.asset.type !== "silence" && <Field label="Source out"><input type="number" min={0} step={0.1} value={audioClip.source_end ?? 0} onChange={(e) => onAudioClip(audioClip.id, { source_end: Math.max(0, Number(e.target.value)) || undefined })} className={field} /></Field>}
              {audioClip.asset.type !== "silence" && <Field label="Playback rate"><input type="number" min={0.25} max={16} step={0.25} value={audioClip.playback_rate ?? 1} onChange={(e) => { const rate = Number(e.target.value); onAudioClip(audioClip.id, { playback_rate: rate === 1 ? undefined : rate }); }} className={field} /></Field>}
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
            <TimingEditor
              timing={audioClip.timing}
              defaultSource="self"
              onChange={(timing) => onAudioClip(audioClip.id, { timing })}
            />
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
                projectId={projectId}
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

function MarkerEditor({ markers, onChange }: { markers: TimelineMarker[]; onChange: (markers: TimelineMarker[]) => void }) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-xs w-full";
  const update = (index: number, patch: Partial<TimelineMarker>) => onChange(markers.map((marker, current) => current === index ? { ...marker, ...patch } : marker));
  const add = () => {
    const lastTime = markers.reduce((latest, marker) => Math.max(latest, marker.time), 0);
    onChange([...markers, { id: `marker-${Date.now()}`, time: lastTime, type: "event", label: "Event" }]);
  };
  return <div className="border-t border-border pt-3 space-y-2">
    <div className="flex items-center gap-2">
      <div className="text-[10px] uppercase tracking-wide text-text-dim flex-1">Event markers</div>
      <button type="button" onClick={add} className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg">Add</button>
    </div>
    {markers.length === 0 ? <div className="text-xs text-text-muted border border-dashed border-border rounded p-2">Paste or generate markers through the API, or add one here.</div> : markers.map((marker, index) => <div key={marker.id || index} className="border border-border rounded p-2 bg-bg space-y-1.5">
      <div className="grid grid-cols-2 gap-1.5">
        <Field label="Time"><input type="number" min={0} step={0.1} value={marker.time} onChange={(e) => update(index, { time: Math.max(0, Number(e.target.value)) })} className={field} /></Field>
        <Field label="Type"><input value={marker.type} onChange={(e) => update(index, { type: e.target.value })} placeholder="click" className={field} /></Field>
      </div>
      <Field label="Label"><input value={marker.label || ""} onChange={(e) => update(index, { label: e.target.value || undefined })} placeholder="Send" className={field} /></Field>
      <Field label="Value"><input value={typeof marker.value === "string" || typeof marker.value === "number" ? String(marker.value) : ""} onChange={(e) => update(index, { value: e.target.value || undefined })} placeholder="thinking" className={field} /></Field>
      <div className="flex items-center gap-2">
        <span className="text-[10px] text-text-dim flex-1">{marker.region ? "Includes active-region coordinates" : "Timeline event"}</span>
        <button type="button" onClick={() => onChange(markers.filter((_, current) => current !== index))} className="text-xs px-2 py-1 border border-red/50 text-red rounded hover:bg-red/10">Remove</button>
      </div>
    </div>)}
  </div>;
}

function Inspector({
  projectId,
  draft,
  clip,
  onDraft,
  onClip,
  onAudioClip,
  onTextClip,
  onDelete,
  onDeleteAudio,
  onDeleteText,
  onMove,
  onDeleteComposition,
  canDeleteComposition,
  onBrowseClip,
  onBrowseAudio,
  onBrowseSoundtrack,
  onAddClip,
  onAddAudioClip,
  onAddTextClip,
  onAddSilenceClip,
  onAddAISoundtrack,
  onAddAIVisualClip,
  onGenerateClipAI,
  onGenerateAudioClipAI,
  onGenerateSoundtrackAI,
  aiBusy,
}: {
  projectId: string;
  draft: DraftState;
  clip: ClipDraft | null;
  onDraft: (fn: (draft: DraftState) => DraftState) => void;
  onClip: (id: string, patch: Partial<ClipDraft>) => void;
  onAudioClip: (id: string, patch: Partial<AudioClipDraft>) => void;
  onTextClip: (id: string, patch: Partial<TextClipDraft>) => void;
  onDelete: (id: string) => void;
  onDeleteAudio: (id: string) => void;
  onDeleteText: (id: string) => void;
  onMove: (id: string, dir: -1 | 1) => void;
  onDeleteComposition: () => void;
  canDeleteComposition: boolean;
  onBrowseClip: (clipId: string) => void;
  onBrowseAudio: (clipId: string) => void;
  onBrowseSoundtrack: () => void;
  onAddClip: () => void;
  onAddAudioClip: (ai?: boolean | MediaKind) => void;
  onAddTextClip: () => void;
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
    <aside className="composer-inspector-panel w-72 2xl:w-80 shrink-0 border-l border-border bg-bg-card overflow-auto">
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
              projectId={projectId}
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
            <button type="button" onClick={onAddTextClip} className="text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input">
              Text
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
          <MarkerEditor markers={draft.markers} onChange={(markers) => onDraft((cur) => ({ ...cur, markers }))} />
        </section>

        <section className="space-y-2">
          <div className="flex items-center gap-2">
            <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Text overlays</h2>
            <button type="button" onClick={onAddTextClip} className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              Add
            </button>
          </div>
          {draft.textClips.length === 0 ? (
            <div className="border border-dashed border-border rounded p-3 text-sm text-text-muted">
              No text overlays.
            </div>
          ) : (
            <div className="space-y-2">
              {draft.textClips.map((text) => (
                <div key={text.id} className="border border-border rounded p-2 bg-bg space-y-2">
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-text flex-1 truncate">{text.asset.text || "empty text"}</span>
                    <button type="button" onClick={() => onDeleteText(text.id)} className="text-xs px-2 py-1 border border-red/50 text-red rounded hover:bg-red/10">
                      Delete
                    </button>
                  </div>
                  <div className="grid grid-cols-2 gap-2">
                    <Field label="Start">
                      <input type="number" min={0} step={0.1} value={text.start} onChange={(e) => onTextClip(text.id, { start: Number(e.target.value) })} className={field} />
                    </Field>
                    <Field label="Length">
                      <input type="number" min={0.1} step={0.1} value={text.length} onChange={(e) => onTextClip(text.id, { length: Number(e.target.value) })} className={field} />
                    </Field>
                  </div>
                  <Field label="Text">
                    <input value={text.asset.text} onChange={(e) => onTextClip(text.id, { asset: { ...text.asset, text: e.target.value } })} className={field} />
                  </Field>
                </div>
              ))}
            </div>
          )}
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
                    {audio.asset.type !== "silence" && <Field label="Source in"><input type="number" min={0} step={0.1} value={audio.source_start ?? 0} onChange={(e) => onAudioClip(audio.id, { source_start: Math.max(0, Number(e.target.value)) || undefined })} className={field} /></Field>}
                    {audio.asset.type !== "silence" && <Field label="Source out"><input type="number" min={0} step={0.1} value={audio.source_end ?? 0} onChange={(e) => onAudioClip(audio.id, { source_end: Math.max(0, Number(e.target.value)) || undefined })} className={field} /></Field>}
                    {audio.asset.type !== "silence" && <Field label="Playback rate"><input type="number" min={0.25} max={16} step={0.25} value={audio.playback_rate ?? 1} onChange={(e) => { const rate = Number(e.target.value); onAudioClip(audio.id, { playback_rate: rate === 1 ? undefined : rate }); }} className={field} /></Field>}
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
                      projectId={projectId}
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
                projectId={projectId}
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
            <VisualLayoutEditor clip={clip} onChange={(patch) => onClip(clip.id, patch)} />
            <SourceFramingEditor clip={clip} onChange={(patch) => onClip(clip.id, patch)} />
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
  projectId,
  title,
  ai,
  allowedKinds,
  busy,
  onChange,
  onGenerate,
  onClear,
}: {
  projectId: string;
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
    "source_images",
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
      input_fingerprint: resetGeneratedState ? undefined : ai.input_fingerprint,
      continuity_fingerprint: resetGeneratedState ? undefined : ai.continuity_fingerprint,
      storage_id: resetGeneratedState ? undefined : ai.storage_id,
      generation_id: resetGeneratedState ? undefined : ai.generation_id,
      provider_request_id: resetGeneratedState ? undefined : ai.provider_request_id,
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
    fetch(withProject(`/api/apps/media-studio/models?kind=${encodeURIComponent(ai.media_kind)}`, projectId), { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((data) => {
        if (!cancelled) setModels(Array.isArray(data?.models) ? data.models : []);
      })
      .catch(() => !cancelled && setModels([]));
    if (ai.media_kind === "audio_tts" || ai.media_kind === "avatar") {
      fetch(withProject(`/api/apps/media-studio/voices?kind=${encodeURIComponent(ai.media_kind)}`, projectId), { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : null))
        .then((data) => {
          if (!cancelled) setVoices(Array.isArray(data?.voices) ? data.voices : []);
        })
        .catch(() => !cancelled && setVoices([]));
    } else {
      setVoices([]);
    }
    if (ai.media_kind === "avatar") {
      fetch(withProject(`/api/apps/media-studio/avatars`, projectId), { credentials: "same-origin" })
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
  }, [ai.media_kind, projectId]);

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
        <Field label="Reference images">
          <textarea
            value={aiSourceImages(ai).join("\n")}
            onChange={(e) => {
              const refs = e.target.value.split(/\r?\n|,/).map((ref) => ref.trim()).filter(Boolean);
              update({ source_images: refs.length ? refs : undefined, source_image: refs[0] || undefined });
            }}
            placeholder={"storage:1\nstorage:2 or https://..."}
            className={`${field} resize-y`}
            rows={Math.max(2, Math.min(5, aiSourceImages(ai).length || 2))}
          />
          <div className="mt-1 text-[11px] text-text-dim">One storage id or URL per line. The first is also sent as the single-reference fallback.</div>
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

function RenderPreview({ render, outputFormat, aspect, onOpen }: { render: RenderRow | null; outputFormat: string; aspect: Aspect; onOpen: (r: RenderRow) => void }) {
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
        {render.status !== "failed" && render.status !== "cancelled" && (
          <div className="mt-2">
            <div className="h-1.5 bg-bg-input rounded overflow-hidden"><div className="h-full bg-accent transition-all duration-300" style={{ width: `${Math.max(2, Math.min(100, render.progress_pct || 0))}%` }} /></div>
            <div className="mt-1 text-text-dim">{Math.round(render.progress_pct || 0)}% · {render.phase || "queued"}</div>
          </div>
        )}
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
        {url && (
          <button type="button" onClick={() => onOpen(render)} className="px-2 py-1 border border-border rounded hover:bg-bg-input text-text">
            Open
          </button>
        )}
      </header>
      {url ? (
        audio
          ? <div className="p-3"><audio controls src={url} className="w-full" /></div>
          : (
            <div className="p-3 flex justify-center bg-bg">
              <video
                controls
                src={url}
                className="block object-contain border border-border bg-black"
                style={{ aspectRatio: aspectRatio(aspect), width: `min(100%, ${previewFrameMaxWidth(aspect)})` }}
              />
            </div>
          )
      ) : <div className="py-12 text-center text-text-muted text-xs">no source</div>}
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
          ? <audio controls src={url} style={{ width: "min(720px, 92vw)" }} />
          : <video controls src={url} style={{ maxWidth: "92vw", maxHeight: "82vh" }} />
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
