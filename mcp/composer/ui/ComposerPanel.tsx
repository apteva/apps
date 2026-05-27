// ComposerPanel v0.3.0 - timeline editor with storage and Media Studio AI assets.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

const API = "/api/apps/composer";

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
type Aspect = "16:9" | "9:16" | "1:1" | "4:3";

interface AIAsset {
  media_kind: MediaKind;
  prompt: string;
  model?: string;
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
  error?: string;
}

interface ClipDraft {
  id: string;
  asset: { type: AssetType; src: string };
  start: number;
  length: number;
  transition?: { in?: string; out?: string };
  text?: { body: string; position?: "top" | "center" | "bottom"; font_size?: number; color?: string };
  ai?: AIAsset;
}

interface OutputDraft {
  format: "mp4";
  resolution: "sd" | "hd" | "fullhd" | "4k";
  aspect: Aspect;
  fps: 24 | 30 | 60;
}

interface DraftState {
  name: string;
  clips: ClipDraft[];
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

type Tab = "timeline" | "json";
type AssetPickerTarget = { kind: "clip"; clipId: string } | { kind: "soundtrack" };

const DEFAULT_DRAFT: DraftState = {
  name: "",
  background: "#000000",
  clips: [],
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

function durationOf(clips: ClipDraft[]): number {
  return clips.reduce((sum, c) => sum + Math.max(0.1, Number(c.length) || 0), 0);
}

function normalizeClips(clips: ClipDraft[]): ClipDraft[] {
  let t = 0;
  return clips.map((clip, i) => {
    const length = Math.max(0.1, Number(clip.length) || 1);
    const next = {
      ...clip,
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

function parseComposition(c: Composition | null): DraftState {
  if (!c) return cloneDefault();
  const draft = cloneDefault();
  draft.name = c.name || "";
  try {
    const edit = JSON.parse(c.edit_json || "{}");
    const timeline = edit.timeline || {};
    draft.background = timeline.background || draft.background;
    const clips = Array.isArray(timeline.tracks?.[0]?.clips) ? timeline.tracks[0].clips : [];
    if (clips.length) {
      draft.clips = normalizeClips(clips.map((clip: any, i: number) => ({
        id: String(clip.uid || `clip-${i + 1}`),
        asset: {
          type: clip.asset?.type === "image" ? "image" : "video",
          src: String(clip.asset?.src || ""),
        },
        start: Number(clip.start) || 0,
        length: Number(clip.length) || 1,
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
      format: "mp4",
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
  const body: Record<string, unknown> = {
    name: draft.name,
    tracks: [{ clips }],
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
    duration: kind === "image" ? undefined : kind === "music" ? 30 : 5,
    aspect: kind === "video" || kind === "avatar" ? aspect : undefined,
    cache_policy: "reuse",
    status: "draft",
  };
}

function cacheKeyForAI(ai: AIAsset): string {
  const stable = JSON.stringify({
    media_kind: ai.media_kind,
    prompt: ai.prompt,
    model: ai.model || "",
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
  const [playing, setPlaying] = useState(false);
  const [resolved, setResolved] = useState<Record<string, ResolvedAsset>>({});
  const [jsonEdit, setJsonEdit] = useState("");
  const [jsonOutput, setJsonOutput] = useState("");
  const [pickerTarget, setPickerTarget] = useState<AssetPickerTarget | null>(null);
  const [storageFiles, setStorageFiles] = useState<StorageFile[]>([]);
  const [storageLoading, setStorageLoading] = useState(false);
  const [storageError, setStorageError] = useState("");
  const [aiBusy, setAIBusy] = useState("");

  const selected = selectedId != null ? compositions.find((c) => c.id === selectedId) || null : null;
  const clips = useMemo(() => normalizeClips(draft.clips), [draft.clips]);
  const totalDuration = useMemo(() => durationOf(clips), [clips]);
  const activeClip = useMemo(() => activeClipAt(clips, playhead), [clips, playhead]);
  const selectedClip = clips.find((clip) => clip.id === selectedClipId) || clips[0] || null;

  const load = useCallback(async () => {
    try {
      const res = await fetch(`${API}/compositions`, { credentials: "same-origin" });
      if (!res.ok) {
        setStatus(`Error: ${res.status}`);
        return;
      }
      const data = await res.json();
      setCompositions(data.compositions || []);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, []);

  useEffect(() => {
    load();
    fetch(`${API}/bindings`, { credentials: "same-origin" })
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => d && setBindings(d))
      .catch(() => {});
  }, [load]);

  useEffect(() => {
    const next = parseComposition(selected);
    setDraft(next);
    setSelectedClipId(next.clips[0]?.id || "");
    setPlayhead(0);
    setJsonEdit(editJSONFromDraft(next));
    setJsonOutput(outputJSONFromDraft(next));
  }, [selectedId, selected?.edit_json, selected?.output_json]);

  useEffect(() => {
    setJsonEdit(editJSONFromDraft(draft));
    setJsonOutput(outputJSONFromDraft(draft));
  }, [draft]);

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
      draft.soundtrack?.src?.trim() || "",
    ].filter(Boolean)));
    for (const src of sources) {
      if (resolved[src]) continue;
      if (src.startsWith("http://") || src.startsWith("https://")) {
        setResolved((prev) => ({ ...prev, [src]: { src, url: src, kind: src.match(/\.(png|jpe?g|webp|gif)(\?|$)/i) ? "image" : "video" } }));
        continue;
      }
      fetch(`${API}/assets/resolve?src=${encodeURIComponent(src)}`, { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : null))
        .then((asset) => {
          if (!asset?.url) return;
          setResolved((prev) => ({ ...prev, [src]: asset }));
        })
        .catch(() => {});
    }
  }, [clips, draft.soundtrack?.src, resolved]);

  const updateDraft = (fn: (draft: DraftState) => DraftState) => {
    setDraft((cur) => {
      const next = fn(cur);
      return { ...next, clips: normalizeClips(next.clips) };
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

  const save = async () => {
    setStatus("Saving...");
    try {
      if (draft.clips.length === 0) {
        setStatus("Add at least one clip before saving.");
        return;
      }
      const body = draftToBody(draft);
      const url = selectedId == null ? `${API}/composition/new` : `${API}/composition/${selectedId}`;
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
      setDraft(parseComposition(c));
      setStatus("JSON applied.");
    } catch (e) {
      setStatus("JSON error: " + (e as Error).message);
    }
  };

  const render = async () => {
    if (draft.clips.length === 0) {
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
      const res = await fetch(`${API}/render`, {
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
    await fetch(`${API}/composition/${selectedId}`, { method: "DELETE", credentials: "same-origin" });
    setSelectedId(null);
    await load();
  };

  const openPicker = (target: AssetPickerTarget) => {
    setPickerTarget(target);
    setStorageLoading(true);
    setStorageError("");
    fetch(`${API}/assets/storage?folder=/&recursive=true&limit=200`, { credentials: "same-origin" })
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
    const cache_key = ai.cache_key || cacheKeyForAI(ai);
    const body: Record<string, unknown> = {
      kind: ai.media_kind,
      prompt: ai.prompt,
      cache_key,
      cache_policy: ai.cache_policy || "reuse",
    };
    if (ai.model) body.model = ai.model;
    if (ai.duration) body.duration = ai.duration;
    if (ai.aspect) body.aspect = ai.aspect;
    if (ai.voice) body.voice = ai.voice;
    if (ai.avatar) body.avatar = ai.avatar;
    if (ai.source_image) body.source_image = ai.source_image;
    if (ai.options && Object.keys(ai.options).length > 0) body.options = ai.options;
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
        updateClip(clip.id, { ai: { ...clip.ai, cache_key, status: "generating", job_id: Number(meta.job_id || 0), error: "" } });
        setStatus(`AI clip queued as job #${meta.job_id}.`);
        return;
      }
      const storageId = storageIDFromMeta(meta);
      if (!storageId) throw new Error("Media Studio returned no storage id. Make sure Storage is linked to Media Studio.");
      updateClip(clip.id, {
        asset: { type: clip.ai.media_kind === "image" ? "image" : "video", src: `storage:${storageId}` },
        ai: {
          ...clip.ai,
          cache_key,
          status: "ready",
          storage_id: storageId,
          generation_id: generationIDFromMeta(meta),
          error: "",
        },
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
      updateDraft((cur) => ({
        ...cur,
        soundtrack: {
          src: `storage:${storageId}`,
          volume: cur.soundtrack?.volume ?? 1,
          ai: { ...ai, cache_key, status: "ready", storage_id: storageId, generation_id: generationIDFromMeta(meta), error: "" },
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

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="border-b border-border px-4 py-2 flex items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium truncate">{draft.name || selected?.name || "Untitled composition"}</div>
          <div className="text-xs text-text-dim flex items-center gap-2">
            <span>{clips.length} clips</span>
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
          selectedId={selectedId}
          onSelect={setSelectedId}
          onNew={() => setSelectedId(null)}
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
                  selectedClipId={selectedClipId}
                  playhead={playhead}
                  duration={totalDuration}
                  onSelect={setSelectedClipId}
                  onSeek={setPlayhead}
                  onAdd={addClip}
                  onBrowse={() => openPicker(clips.length ? { kind: "clip", clipId: clips[0].id } : { kind: "clip", clipId: "" })}
                />
                <RenderPreview render={selected?.latest_render || null} onOpen={setLightbox} />
              </section>
              <Inspector
                draft={draft}
                clip={selectedClip}
                onDraft={updateDraft}
                onClip={updateClip}
                onDelete={deleteClip}
                onMove={moveClip}
                onDeleteComposition={deleteSelected}
                canDeleteComposition={selectedId != null}
                onBrowseClip={(clipId) => openPicker({ kind: "clip", clipId })}
                onBrowseSoundtrack={() => openPicker({ kind: "soundtrack" })}
                onAddClip={addClip}
                onGenerateClipAI={generateClipAI}
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

      {lightbox && <Lightbox render={lightbox} onClose={() => setLightbox(null)} />}
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
  selectedId,
  onSelect,
  onNew,
}: {
  compositions: Composition[];
  selectedId: number | null;
  onSelect: (id: number) => void;
  onNew: () => void;
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
              <div className="text-text-muted text-sm">{clip ? (clip.asset.src || "No clip source") : "No clips yet"}</div>
              {!clip && (
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

function Timeline({
  clips,
  selectedClipId,
  playhead,
  duration,
  onSelect,
  onSeek,
  onAdd,
  onBrowse,
}: {
  clips: ClipDraft[];
  selectedClipId: string;
  playhead: number;
  duration: number;
  onSelect: (id: string) => void;
  onSeek: (t: number) => void;
  onAdd: () => void;
  onBrowse: () => void;
}) {
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden">
      <header className="px-3 py-2 border-b border-border flex items-center gap-2">
        <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Timeline</h2>
        <button onClick={onBrowse} className="text-xs px-2 py-1 border border-accent text-accent rounded hover:bg-accent hover:text-bg">Browse storage</button>
        <button onClick={onAdd} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Add empty clip</button>
      </header>
      <div className="p-3">
        {clips.length === 0 ? (
          <div className="border border-dashed border-border rounded bg-bg px-4 py-8 text-center">
            <div className="text-text-muted text-sm">Start with a file from Storage or add a blank clip.</div>
            <div className="mt-3 flex items-center justify-center gap-2">
              <button type="button" onClick={onBrowse} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">Browse storage</button>
              <button type="button" onClick={onAdd} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Add empty clip</button>
            </div>
          </div>
        ) : (
          <button
            type="button"
            onClick={(e) => {
              const rect = e.currentTarget.getBoundingClientRect();
              onSeek(((e.clientX - rect.left) / rect.width) * duration);
            }}
            className="relative w-full h-32 border border-border rounded bg-bg text-left overflow-hidden"
          >
            <div
              className="absolute top-0 bottom-0 w-px bg-accent"
              style={{ left: `${duration ? Math.min(100, (playhead / duration) * 100) : 0}%` }}
            />
            <div className="absolute inset-x-3 top-10 h-16 flex">
              {clips.map((clip) => {
                const width = duration ? (clip.length / duration) * 100 : 100;
                const selected = clip.id === selectedClipId;
                return (
                  <div
                    key={clip.id}
                    role="button"
                    tabIndex={0}
                    title={clip.asset.src || "empty source"}
                    onClick={(e) => {
                      e.stopPropagation();
                      onSelect(clip.id);
                      onSeek(clip.start);
                    }}
                    className={`h-16 min-w-24 border text-xs flex flex-col justify-center px-2 overflow-hidden ${selected ? "border-accent bg-accent/10" : "border-border bg-bg-input hover:border-accent"}`}
                    style={{ width: `${Math.max(10, width)}%` }}
                  >
                    <span className="block text-text truncate leading-5">{clip.asset.src || (clip.ai ? `AI ${clip.ai.media_kind}` : "empty source")}</span>
                    <span className="block text-text-dim truncate leading-5">{clip.ai?.status || clip.asset.type} - {clip.length.toFixed(1)}s</span>
                  </div>
                );
              })}
            </div>
          </button>
        )}
      </div>
    </section>
  );
}

function Inspector({
  draft,
  clip,
  onDraft,
  onClip,
  onDelete,
  onMove,
  onDeleteComposition,
  canDeleteComposition,
  onBrowseClip,
  onBrowseSoundtrack,
  onAddClip,
  onGenerateClipAI,
  onGenerateSoundtrackAI,
  aiBusy,
}: {
  draft: DraftState;
  clip: ClipDraft | null;
  onDraft: (fn: (draft: DraftState) => DraftState) => void;
  onClip: (id: string, patch: Partial<ClipDraft>) => void;
  onDelete: (id: string) => void;
  onMove: (id: string, dir: -1 | 1) => void;
  onDeleteComposition: () => void;
  canDeleteComposition: boolean;
  onBrowseClip: (clipId: string) => void;
  onBrowseSoundtrack: () => void;
  onAddClip: () => void;
  onGenerateClipAI: (clip: ClipDraft) => void;
  onGenerateSoundtrackAI: () => void;
  aiBusy: string;
}) {
  const field = "bg-bg-input border border-border rounded px-2 py-1.5 text-sm w-full";
  return (
    <aside className="w-80 shrink-0 border-l border-border bg-bg-card overflow-auto">
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
            onClick={() => onDraft((cur) => ({
              ...cur,
              soundtrack: {
                src: cur.soundtrack?.src || "",
                volume: cur.soundtrack?.volume ?? 1,
                ai: cur.soundtrack?.ai || defaultAI("music", cur.output.aspect),
              },
            }))}
            className="w-full text-xs px-2 py-1.5 border border-border rounded hover:bg-bg-input"
          >
            Add AI soundtrack
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
              onClick={() => onClip(clip.id, { ai: clip.ai || defaultAI(clip.asset.type === "image" ? "image" : "video", draft.output.aspect) })}
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
                onChange={(ai) => onClip(clip.id, { ai })}
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
  const update = (patch: Partial<AIAsset>) => onChange({ ...ai, ...patch, status: patch.status || ai.status || "draft" });
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
            onChange={(e) => update({ media_kind: e.target.value as MediaKind })}
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
            onChange={(e) => update({ duration: Number(e.target.value) || undefined })}
            className={field}
            disabled={ai.media_kind === "image"}
          />
        </Field>
      </div>
      <Field label={ai.media_kind === "audio_tts" || ai.media_kind === "avatar" ? "Script" : "Prompt"}>
        <textarea
          value={ai.prompt}
          onChange={(e) => update({ prompt: e.target.value })}
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
      {ai.job_id && ai.status === "generating" && <div className="text-xs text-text-dim">media-studio job #{ai.job_id}</div>}
      <button
        type="button"
        onClick={onGenerate}
        disabled={busy || !ai.prompt.trim()}
        className="w-full text-sm px-3 py-1.5 border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
      >
        {busy ? "Generating..." : "Generate in place"}
      </button>
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

function RenderPreview({ render, onOpen }: { render: RenderRow | null; onOpen: (r: RenderRow) => void }) {
  if (!render) return null;
  const url = renderSrc(render);
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
        {url ? <video controls src={url} className="w-full" /> : <div className="py-12 text-center text-text-muted text-xs">no source</div>}
      </button>
    </section>
  );
}

function Lightbox({ render, onClose }: { render: RenderRow; onClose: () => void }) {
  const url = renderSrc(render);
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
        {url && <video controls autoPlay src={url} style={{ maxWidth: "92vw", maxHeight: "82vh" }} />}
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
