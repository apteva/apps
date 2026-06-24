// MediaPanel — media generation gallery + per-kind composer.
// Loaded by the dashboard via dynamic import; uses host React via
// importmap; talks to the media-studio sidecar at /api/apps/media-studio/*.

import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps install independently.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type Kind = "image" | "video" | "audio_tts" | "audio_sfx" | "music" | "avatar";

interface Generation {
  id: number;
  kind: Kind;
  prompt: string;
  revised_prompt?: string;
  provider: string;
  model: string;
  size: string;
  duration_ms: number;
  storage_ids: number[];
  storage_urls: string[];
  upstream_urls: string[];
  thumbnail_b64: string;
  local_cache_url?: string;
  count: number;
  cost_usd: number;
  status?: "draft" | "generating" | "queued" | "ready" | "failed" | string;
  request_json?: string;
  created_at: string;
}

interface GenerationEstimate {
  kind: Kind;
  provider?: string;
  model?: string;
  capability?: string;
  cost_usd: number;
  available: boolean;
  source: string;
  estimated_duration_seconds?: number;
}

interface VideoJob {
  id: number;
  queue_id: string;
  generation_id?: number;
  provider?: string;
  model: string;
  prompt: string;
  status: string;
  error: string;
  cost_usd?: number;
  estimated_duration_seconds?: number;
}

// LiveModel mirrors the sidecar's modelEntry JSON. Constraints arrays
// are empty when the model doesn't pre-enumerate options (e.g.
// pixel-sized image models that accept arbitrary WxH).
interface LiveModel {
  id: string;
  label: string;
  size_modes?: string[];
  model_type?: string;
  pixel_sizes?: string[];
  aspect_ratios?: string[];
  default_aspect_ratio?: string;
  resolutions?: string[];
  default_resolution?: string;
  durations?: string[];
  supports_image_to_video?: boolean;
  supports_image_edit?: boolean;
  max_source_images?: number;
  audio_configurable?: boolean;
  steps_default?: number;
  steps_max?: number;
  prompt_char_limit?: number;
  price_usd?: number;
}

// formatCost renders the per-generation USD figure. Trims trailing
// zeros so $0.0400 reads as "$0.04" but $0.0009 still keeps precision.
function formatCost(n: number): string {
  if (!n || n <= 0) return "";
  if (n >= 0.01) return "$" + n.toFixed(2);
  if (n >= 0.001) return "$" + n.toFixed(4);
  return "$" + n.toFixed(6);
}

interface BindingsStatus {
  image: { bound: boolean; slug?: string };
  video: { bound: boolean; slug?: string };
  audio_tts: { bound: boolean; slug?: string };
  audio_sfx: { bound: boolean; slug?: string };
  music: { bound: boolean; slug?: string };
  avatar: { bound: boolean; slug?: string };
  storage: { bound: boolean; app?: string };
}

interface AvatarEntry {
  id: string;
  name: string;
  thumbnail?: string;
  thumbnail_type?: "image" | "video" | string;
  status?: string;
  default_voice_id?: string;
  supported_api_engines?: string[];
  avatar_type?: string;
  ownership?: string;
}

interface AvatarCreateJob {
  id: number;
  provider: string;
  source_type: "photo" | "prompt" | "video" | string;
  name: string;
  provider_avatar_id?: string;
  provider_group_id?: string;
  status: string;
  error: string;
  created_at: string;
  updated_at: string;
}

interface AvatarCapabilities {
  bound: boolean;
  provider?: string;
  source_types: string[];
  notes?: string;
}

interface VoiceEntry {
  id: string;
  name: string;
  language?: string;
  gender?: string;
  preview?: string;
}

interface MediaIdentity {
  id: number;
  kind: "voice" | "avatar" | string;
  provider: string;
  name: string;
  source_type: string;
  provider_identity_id: string;
  provider_job_id?: string;
  preview_url?: string;
  status: string;
  prompt?: string;
  error?: string;
  created_at?: string;
}

interface SourceImageRef {
  value: string;
  label: string;
}

const API = "/api/apps/media-studio";

const TAB_LABELS: Record<Exclude<Kind, "audio_sfx">, string> = {
  image: "Images",
  video: "Videos",
  audio_tts: "Audio",
  music: "Music",
  avatar: "Avatar",
};

// Image-specific option matrices, lifted from the old StudioPanel.
type ImageModel = string;

const IMAGE_MODEL_LABELS: Record<string, string> = {
  "gpt-image-2": "GPT Image 2 (current)",
  "gpt-image-1.5": "GPT Image 1.5",
  "gpt-image-1": "GPT Image 1",
  "gpt-image-1-mini": "GPT Image 1 Mini",
  "dall-e-3": "DALL·E 3 (legacy)",
  "dall-e-2": "DALL·E 2 (legacy)",
};
const IMAGE_MODELS: ImageModel[] = [
  "gpt-image-2",
  "gpt-image-1.5",
  "gpt-image-1",
  "gpt-image-1-mini",
  "dall-e-3",
  "dall-e-2",
];
const IMAGE_SIZES: Record<string, string[]> = {
  "gpt-image-2": ["1024x1024", "1024x1536", "1536x1024", "2048x2048", "3840x2160"],
  "gpt-image-1.5": ["1024x1024", "1024x1536", "1536x1024"],
  "gpt-image-1": ["1024x1024", "1024x1536", "1536x1024"],
  "gpt-image-1-mini": ["1024x1024", "1024x1536", "1536x1024"],
  "dall-e-3": ["1024x1024", "1792x1024", "1024x1792"],
  "dall-e-2": ["256x256", "512x512", "1024x1024"],
};
const GPT_IMAGE_QUALITIES = ["auto", "low", "medium", "high"];
const DALLE3_QUALITIES = ["standard", "hd"];

function isGptImage(m: ImageModel) {
  return m.startsWith("gpt-image");
}

function modelLabel(m: LiveModel): string {
  const bits = [m.label || m.id];
  if (m.model_type === "image-to-video") bits.push("img→vid");
  if (m.size_modes && m.size_modes.length > 0) bits.push(m.size_modes.join("+"));
  const price = formatCost(m.price_usd || 0);
  if (price) bits.push(price);
  return bits.join(" · ");
}

// Edit-capable models (Venice). Used when the user supplies a reference
// image — the manifest's image.edit capability routes to /image/edit
// which only accepts these. Default firered-image-edit per Venice docs.
const EDIT_MODELS = [
  "firered-image-edit",
  "qwen-edit",
  "grok-imagine-edit",
  "flux-2-max-edit",
  "gpt-image-2-edit",
] as const;
type EditModel = string;
const EDIT_MODEL_SOURCE_LIMITS: Record<string, number> = {
  "firered-image-edit": 3,
  "qwen-edit": 3,
  "grok-imagine-edit": 3,
  "flux-2-max-edit": 3,
  "gpt-image-2-edit": 3,
};

// Small inline SVG icons — no emoji, no Tailwind color classes inside
// the SVG (dashboard's JIT doesn't scan apps/mcp/*/ui/, so color
// utilities inside SVG silently no-op). Use currentColor + className
// on the wrapping element if you need theming.
function IconImage() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
      <rect x="1.5" y="2.5" width="13" height="11" rx="1" />
      <circle cx="5.5" cy="6" r="1" />
      <path d="M2 12l3.5-3.5 3 3L11 7l3 3" />
    </svg>
  );
}
function IconVideo() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
      <rect x="1.5" y="3.5" width="10" height="9" rx="1" />
      <path d="M11.5 7l3-2v6l-3-2z" />
    </svg>
  );
}
function IconAudio() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
      <path d="M3 6v4h2l3 2.5v-9L5 6H3z" />
      <path d="M10 5.5a3 3 0 010 5" />
      <path d="M12 3.5a6 6 0 010 9" />
    </svg>
  );
}
function IconMusic() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
      <path d="M6 12V3l7-1.5v9" />
      <circle cx="4.5" cy="12" r="1.5" />
      <circle cx="11.5" cy="10.5" r="1.5" />
    </svg>
  );
}

function imageSrc(g: Generation): string {
  // Prefer storage (persistent, sharable) > local sidecar cache
  // (full-resolution original kept when storage is unbound) >
  // thumbnail (lossy 256px JPEG, last-resort).
  if (g.storage_urls && g.storage_urls.length > 0) return g.storage_urls[0];
  if (g.local_cache_url) return g.local_cache_url;
  if (g.thumbnail_b64) return `data:image/jpeg;base64,${g.thumbnail_b64}`;
  return "";
}

export default function MediaPanel({ projectId }: NativePanelProps) {
  // Two state axes: the visible tab and (for audio) the sub-kind.
  const [tab, setTab] = useState<"image" | "video" | "audio" | "music" | "avatar">("image");
  const [audioSubKind, setAudioSubKind] = useState<"audio_tts" | "audio_sfx">("audio_tts");
  const activeKind: Kind =
    tab === "audio" ? audioSubKind : (tab as Kind);

  const [items, setItems] = useState<Generation[]>([]);
  const [bindings, setBindings] = useState<BindingsStatus | null>(null);
  const [status, setStatus] = useState("");
  const [generating, setGenerating] = useState(false);
  const [creatingDraft, setCreatingDraft] = useState(false);
  const [generatingDraftId, setGeneratingDraftId] = useState<number | null>(null);
  const [deletingId, setDeletingId] = useState<number | null>(null);
  const [selected, setSelected] = useState<Generation | null>(null);
  const [lightbox, setLightbox] = useState<Generation | null>(null);

  // Per-kind composer state.
  const [prompt, setPrompt] = useState("");
  const [imageModel, setImageModel] = useState<ImageModel>("gpt-image-2");
  const [imageSize, setImageSize] = useState("1024x1024");
  const [imageResolution, setImageResolution] = useState("1K");
  const [imageQuality, setImageQuality] = useState("auto");
  const [imageFormat, setImageFormat] = useState("png");
  const [duration, setDuration] = useState(5); // video/audio/music
  const [aspect, setAspect] = useState("16:9");
  const [videoNoSound, setVideoNoSound] = useState(false);
  const [voice, setVoice] = useState("");
  // Video model picker — live-loaded from /models?kind=video.
  // Auto-snaps to the first listed model when the dropdown lands.
  const [videoModel, setVideoModel] = useState<string>("");
  const [audioModel, setAudioModel] = useState<string>("");
  const [sfxModel, setSfxModel] = useState<string>("");
  const [musicModel, setMusicModel] = useState<string>("");
  // safe_mode (image gen + edit) — Venice's own default is true (blurs
  // adult-classified output); sidecar defaults to false so the API
  // returns whatever the model produced. Panel flag mirrors that.
  const [safeMode, setSafeMode] = useState(false);
  // Avatar (talking-head) state — replica/avatar picker + selection.
  const [avatars, setAvatars] = useState<AvatarEntry[]>([]);
  const [selectedAvatar, setSelectedAvatar] = useState("");
  const [avatarEngine, setAvatarEngine] = useState("avatar_v");
  const [avatarResolution, setAvatarResolution] = useState("1080p");
  const [avatarAspect, setAvatarAspect] = useState("16:9");
  const [avatarCaps, setAvatarCaps] = useState<AvatarCapabilities | null>(null);
  const [avatarCreateJobs, setAvatarCreateJobs] = useState<AvatarCreateJob[]>([]);
  const [avatarCreateOpen, setAvatarCreateOpen] = useState(false);
  const [avatarCreateName, setAvatarCreateName] = useState("");
  const [avatarCreateSourceType, setAvatarCreateSourceType] = useState<"photo" | "prompt" | "video">("photo");
  const [avatarCreateSource, setAvatarCreateSource] = useState("");
  const [avatarCreatePrompt, setAvatarCreatePrompt] = useState("");
  const [avatarCreating, setAvatarCreating] = useState(false);
  // Voices — HeyGen needs an explicit voice_id (Tavus bakes voice into
  // the replica, so this stays empty there). Reuses the `voice` state.
  const [voices, setVoices] = useState<VoiceEntry[]>([]);
  const [voiceIdentities, setVoiceIdentities] = useState<MediaIdentity[]>([]);
  const [voiceCreateOpen, setVoiceCreateOpen] = useState(false);
  const [voiceCreateName, setVoiceCreateName] = useState("");
  const [voiceCreateDescription, setVoiceCreateDescription] = useState("");
  const [voiceCreatePreviewText, setVoiceCreatePreviewText] = useState("");
  const [voiceCreateEnhance, setVoiceCreateEnhance] = useState(true);
  const [voiceCreating, setVoiceCreating] = useState(false);
  // Reference-image state. When one or more source images are present,
  // image generation routes through image.edit. Models expose a max source
  // image count; the panel enforces that before submit.
  const [sourceImages, setSourceImages] = useState<SourceImageRef[]>([]);
  const [editModel, setEditModel] = useState<EditModel>("firered-image-edit");
  const isEditMode = activeKind === "image" && sourceImages.length > 0;
  // Live-loaded model lists for the bound provider, refreshed on tab
  // switch and binding change. Image generation and image edit can be
  // different provider-side buckets (Venice image vs inpaint), so keep
  // edit models separate.
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);
  const [editLiveModels, setEditLiveModels] = useState<LiveModel[] | null>(null);
  const [liveProvider, setLiveProvider] = useState<string>("");
  // In-flight video jobs (queued / polling). Shown as a small badge
  // above the video gallery so the user knows something is cooking
  // between submit and the eventual media.generated event.
  const [videoJobs, setVideoJobs] = useState<VideoJob[]>([]);
  const [estimate, setEstimate] = useState<GenerationEstimate | null>(null);
  const [estimateLoading, setEstimateLoading] = useState(false);
  const [storageFolders, setStorageFolders] = useState<Record<Kind, string>>({
    image: "",
    video: "",
    audio_tts: "",
    audio_sfx: "",
    music: "",
    avatar: "",
  });
  const [folderSuggestions, setFolderSuggestions] = useState<string[]>([]);
  const storageFolder = storageFolders[activeKind] || "";
  const setStorageFolder = (v: string) =>
    setStorageFolders((cur) => ({ ...cur, [activeKind]: v }));

  const loadBindings = useCallback(async () => {
    try {
      const res = await fetch(`${API}/bindings?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (!res.ok) return;
      const data = (await res.json()) as BindingsStatus;
      setBindings(data);
    } catch {}
  }, [projectId]);

  const loadGenerations = useCallback(async () => {
    try {
      const res = await fetch(
        `${API}/generations?project_id=${encodeURIComponent(projectId)}&kind=${activeKind}`,
        { credentials: "same-origin" },
      );
      if (!res.ok) {
        setStatus(`Error: ${res.status}`);
        return;
      }
      const data = await res.json();
      setItems(data.generations || []);
      const n = (data.generations || []).length;
      setStatus(`${n} generation${n === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [projectId, activeKind]);

  const loadAvatars = useCallback(async () => {
    try {
      const res = await fetch(`${API}/avatars?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (!res.ok) return;
      const data = await res.json();
      const list: AvatarEntry[] = Array.isArray(data.avatars) ? data.avatars : [];
      setAvatars(list);
      if (list.length > 0 && !list.some((x) => x.id === selectedAvatar)) {
        setSelectedAvatar(list[0].id);
      }
    } catch {}
  }, [projectId, selectedAvatar]);

  const loadAvatarCreateJobs = useCallback(async () => {
    try {
      const res = await fetch(`${API}/avatar-create-jobs?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (!res.ok) return [];
      const data = await res.json();
      const jobs: AvatarCreateJob[] = Array.isArray(data.jobs) ? data.jobs : [];
      setAvatarCreateJobs(jobs);
      return jobs;
    } catch {
      return [];
    }
  }, [projectId]);

  const loadAvatarCapabilities = useCallback(async () => {
    try {
      const res = await fetch(
        `${API}/avatar-capabilities?project_id=${encodeURIComponent(projectId)}`,
        { credentials: "same-origin" },
      );
      if (!res.ok) return;
      const data = await res.json();
      setAvatarCaps({
        bound: !!data.bound,
        provider: data.provider || "",
        source_types: Array.isArray(data.source_types) ? data.source_types : [],
        notes: data.notes || "",
      });
    } catch {}
  }, [projectId]);

  const loadVoiceIdentities = useCallback(async () => {
    try {
      const res = await fetch(
        `${API}/identities?project_id=${encodeURIComponent(projectId)}&kind=voice`,
        { credentials: "same-origin" },
      );
      if (!res.ok) return;
      const data = await res.json();
      setVoiceIdentities(Array.isArray(data.identities) ? data.identities : []);
    } catch {}
  }, [projectId]);

  const loadVoices = useCallback(async () => {
    try {
      const res = await fetch(
        `${API}/voices?project_id=${encodeURIComponent(projectId)}&kind=${encodeURIComponent(activeKind)}`,
        { credentials: "same-origin" },
      );
      if (!res.ok) return;
      const data = await res.json();
      const list: VoiceEntry[] = Array.isArray(data.voices) ? data.voices : [];
      setVoices(list);
      if (list.length > 0 && !list.some((x) => x.id === voice)) {
        setVoice(list[0].id);
      }
    } catch {}
  }, [activeKind, projectId, voice]);

  useEffect(() => {
    loadBindings();
  }, [loadBindings]);
  useEffect(() => {
    loadGenerations();
  }, [loadGenerations]);

  useEffect(() => {
    if (!bindings?.storage?.bound) {
      setFolderSuggestions([]);
      return;
    }
    let cancelled = false;
    const timer = window.setTimeout(() => {
      const params = new URLSearchParams({
        project_id: projectId,
        folder: "/",
        recursive: "true",
        limit: "240",
      });
      if (storageFolder.trim()) params.set("q", storageFolder.trim());
      fetch(`${API}/storage-files?${params.toString()}`, { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : null))
        .then((data) => {
          if (cancelled || !data) return;
          setFolderSuggestions(storageFoldersFromFiles(data.files || []));
        })
        .catch(() => {
          if (!cancelled) setFolderSuggestions([]);
        });
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [bindings?.storage?.bound, projectId, storageFolder]);

  useEffect(() => {
    const types = avatarCaps?.source_types || [];
    if (types.length > 0 && !types.includes(avatarCreateSourceType)) {
      const next = types.find((x) => x === "photo" || x === "prompt" || x === "video");
      if (next) setAvatarCreateSourceType(next as "photo" | "prompt" | "video");
    }
  }, [avatarCaps, avatarCreateSourceType]);

  // Poll in-flight video jobs every 5s while the Videos tab is active.
  // 5s is finer than the sidecar's 15s worker tick so the user sees the
  // failed→cleared transition promptly when the worker gives up. When
  // a job transitions queued|polling → complete we also force a
  // gallery refresh — belt-and-suspenders for the rare case where the
  // media.generated event was dropped or missed by the EventSource.
  useEffect(() => {
    if (activeKind !== "video" && activeKind !== "avatar") return;
    let cancelled = false;
    let prevInFlight = new Set<number>();
    const load = () => {
      fetch(`${API}/video-jobs?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      })
        .then((r) => (r.ok ? r.json() : null))
        .then((data) => {
          if (cancelled || !data) return;
          const jobs = Array.isArray(data.jobs) ? data.jobs : [];
          setVideoJobs(jobs);
          const nowInFlight = new Set<number>(
            jobs
              .filter((j: { status: string }) => j.status === "queued" || j.status === "polling")
              .map((j: { id: number }) => j.id),
          );
          // Any job that was in-flight last tick and isn't now → either
          // completed or failed. Refresh the gallery to surface the new row.
          let transitioned = false;
          for (const id of prevInFlight) if (!nowInFlight.has(id)) transitioned = true;
          if (transitioned) loadGenerations();
          prevInFlight = nowInFlight;
        })
        .catch(() => {});
    };
    load();
    const t = window.setInterval(load, 5000);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [activeKind, projectId, loadGenerations]);

  useEffect(() => {
    if (activeKind !== "avatar") return;
    let cancelled = false;
    let prevInFlight = new Set<number>();
    const load = async () => {
      const jobs = await loadAvatarCreateJobs();
      if (cancelled) return;
      const nowInFlight = new Set<number>(
        jobs.filter((j) => j.status === "queued" || j.status === "training").map((j) => j.id),
      );
      let transitioned = false;
      for (const id of prevInFlight) if (!nowInFlight.has(id)) transitioned = true;
      if (transitioned) loadAvatars();
      prevInFlight = nowInFlight;
    };
    loadAvatarCapabilities();
    load();
    const t = window.setInterval(load, 5000);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [activeKind, loadAvatarCapabilities, loadAvatarCreateJobs, loadAvatars]);

  // Load the avatar/replica list and provider voice catalog when the
  // active path needs voice selection. Avatar reads avatar_provider;
  // audio_tts reads audio_provider (ElevenLabs).
  useEffect(() => {
    if (activeKind !== "avatar" && activeKind !== "audio_tts") return;
    if (activeKind === "avatar") {
      loadAvatars();
    }
    loadVoices();
    if (activeKind === "audio_tts") loadVoiceIdentities();
  }, [activeKind, bindings, loadAvatars, loadVoiceIdentities, loadVoices, projectId]);

  // Live-load the model list whenever the active kind or the bound
  // provider for that kind changes. The sidecar caches per-(provider,
  // kind) for 10 min so this is cheap on tab switches.
  useEffect(() => {
    const currentBoundSlug = bindings?.[activeKind]?.slug || "";
    if (!currentBoundSlug) {
      setLiveModels(null);
      setEditLiveModels(null);
      setLiveProvider("");
      return;
    }
    let cancelled = false;
    fetch(
      `${API}/models?project_id=${encodeURIComponent(projectId)}&kind=${encodeURIComponent(activeKind)}`,
      { credentials: "same-origin" },
    )
      .then((r) => r.ok ? r.json() : null)
      .then((data) => {
        if (cancelled || !data) return;
        if (Array.isArray(data.models)) {
          setLiveModels(data.models);
          setLiveProvider(String(data.provider || ""));
          // Snap the active model to the first live option when the
          // current selection isn't in the new list (e.g. user switched
          // provider, or first load of a kind whose model state is "").
          if (data.models.length > 0) {
            if (activeKind === "image") {
              const have = data.models.some(
                (m: { id: string }) => m.id === imageModel,
              );
              if (!have) setImageModel(data.models[0].id as ImageModel);
            } else if (activeKind === "video") {
              const have = data.models.some(
                (m: { id: string }) => m.id === videoModel,
              );
              if (!have) setVideoModel(data.models[0].id);
            } else if (activeKind === "audio_tts") {
              const have = data.models.some(
                (m: { id: string }) => m.id === audioModel,
              );
              if (!have) setAudioModel(data.models[0].id);
            } else if (activeKind === "audio_sfx") {
              const have = data.models.some(
                (m: { id: string }) => m.id === sfxModel,
              );
              if (!have) setSfxModel(data.models[0].id);
            } else if (activeKind === "music") {
              const have = data.models.some(
                (m: { id: string }) => m.id === musicModel,
              );
              if (!have) setMusicModel(data.models[0].id);
            }
          }
        }
      })
      .catch(() => {
        if (!cancelled) setLiveModels(null);
      });
    if (activeKind === "image") {
      fetch(
        `${API}/models?project_id=${encodeURIComponent(projectId)}&kind=image&capability=image.edit`,
        { credentials: "same-origin" },
      )
        .then((r) => r.ok ? r.json() : null)
        .then((data) => {
          if (cancelled || !data) return;
          const models = Array.isArray(data.models) ? data.models : [];
          setEditLiveModels(models);
          if (models.length > 0 && !models.some((m: LiveModel) => m.id === editModel)) {
            setEditModel(models[0].id);
          }
        })
        .catch(() => {
          if (!cancelled) setEditLiveModels(null);
        });
    } else {
      setEditLiveModels(null);
    }
    return () => {
      cancelled = true;
    };
  }, [activeKind, bindings, projectId]);

  // Live refresh — dispatcher fires media.generated on every success.
  // Refresh when the event's kind matches the current tab; otherwise
  // just refresh bindings (cheap) so the badge updates without
  // shuffling the visible gallery.
  useAppEvents<{ kind: Kind }>("media-studio", projectId, (ev) => {
    if (ev.topic === "media.generated") {
      if (ev.data?.kind === activeKind) loadGenerations();
    }
  });

  const currentBinding = bindings ? bindings[activeKind] : null;
  const isBound = !!currentBinding?.bound;

  // Find the currently-selected model's full entry so the composer
  // can render constrained dropdowns (aspect / duration / resolution)
  // and decide whether to show the reference-image input for
  // image-to-video models.
  const currentModelId =
    activeKind === "image"
      ? (isEditMode ? editModel : imageModel)
      : activeKind === "video"
        ? videoModel
        : activeKind === "audio_tts"
          ? audioModel
          : activeKind === "audio_sfx"
            ? sfxModel
            : activeKind === "music"
              ? musicModel
              : "";
  const currentModelList = activeKind === "image" && isEditMode ? editLiveModels : liveModels;
  const currentModel: LiveModel | undefined =
    currentModelList?.find((m) => m.id === currentModelId);
  const activeGeneratingModel =
    activeKind === "image"
      ? (isEditMode ? editModel : imageModel)
      : activeKind === "video"
        ? videoModel
        : activeKind === "audio_tts"
          ? audioModel
          : activeKind === "audio_sfx"
            ? sfxModel
            : activeKind === "music"
              ? musicModel
              : selectedAvatar;
  const pendingVideoJobs =
    activeKind === "video" || activeKind === "avatar"
      ? videoJobs.filter((j) => j.status === "queued" || j.status === "polling")
      : [];
  // Video reference-image is allowed for both standard (text-to-video)
  // and image-to-video models — required for the latter, optional
  // hint for the former (Venice's queue accepts image_url on most).
  const showVideoRefInput =
    activeKind === "video" && !!currentModel?.supports_image_to_video;
  const editSourceLimit =
    activeKind === "image"
      ? currentModel?.max_source_images || EDIT_MODEL_SOURCE_LIMITS[editModel] || 1
      : 1;
  const referenceInputMax = activeKind === "image" ? editSourceLimit : 1;

  const addSourceImage = (value: string, label: string) => {
    const trimmed = value.trim();
    if (!trimmed) return;
    setSourceImages((cur) => {
      const withoutExisting = cur.filter((x) => x.value !== trimmed);
      return [...withoutExisting, { value: trimmed, label }].slice(0, referenceInputMax);
    });
  };
  const removeSourceImage = (index: number) => {
    setSourceImages((cur) => cur.filter((_, i) => i !== index));
  };

  // Auto-snap aspect / duration / resolution when the user picks a
  // different model whose constraint set doesn't include the current
  // value. Defaults to the model's default* field when set, else the
  // first listed option.
  useEffect(() => {
    if (!currentModel) return;
    if (activeKind === "image" && !isEditMode) {
      const pixelSizes = currentModel.pixel_sizes && currentModel.pixel_sizes.length > 0
        ? currentModel.pixel_sizes
        : IMAGE_SIZES[imageModel] || [];
      if (pixelSizes.length > 0 && !pixelSizes.includes(imageSize)) {
        setImageSize(pixelSizes[0]);
      }
      if (currentModel.resolutions && currentModel.resolutions.length > 0
          && !currentModel.resolutions.includes(imageResolution)) {
        setImageResolution(currentModel.default_resolution || currentModel.resolutions[0]);
      }
      if (currentModel.aspect_ratios && currentModel.aspect_ratios.length > 0
          && !currentModel.aspect_ratios.includes(aspect)) {
        setAspect(currentModel.default_aspect_ratio || currentModel.aspect_ratios[0]);
      }
      if (!liveProvider || liveProvider === "openai-api") {
        if (isGptImage(imageModel) && !GPT_IMAGE_QUALITIES.includes(imageQuality)) {
          setImageQuality("auto");
        } else if (imageModel === "dall-e-3" && !DALLE3_QUALITIES.includes(imageQuality)) {
          setImageQuality("standard");
        }
      }
    }
    if (currentModel.aspect_ratios && currentModel.aspect_ratios.length > 0
        && !currentModel.aspect_ratios.includes(aspect)) {
      setAspect(currentModel.default_aspect_ratio || currentModel.aspect_ratios[0]);
    }
    if (currentModel.durations && currentModel.durations.length > 0) {
      const want = `${duration}s`;
      if (!currentModel.durations.includes(want)) {
        const first = currentModel.durations[0];
        const n = parseInt(first.replace(/[^\d]/g, ""), 10);
        if (!isNaN(n)) setDuration(n);
      }
    }
  }, [currentModelId, activeKind, isEditMode]); // eslint-disable-line react-hooks/exhaustive-deps

  const createAvatar = async () => {
    if (avatarCreating) return;
    const name = avatarCreateName.trim();
    if (!name) {
      setStatus("Avatar name required.");
      return;
    }
    if (avatarCreateSourceType === "photo" && !avatarCreateSource.trim()) {
      setStatus("Photo avatar source required.");
      return;
    }
    if (avatarCreateSourceType === "prompt" && !avatarCreatePrompt.trim()) {
      setStatus("Prompt avatar text required.");
      return;
    }
    setAvatarCreating(true);
    setStatus("Creating avatar…");
    try {
      const body: Record<string, unknown> = {
        project_id: projectId,
        name,
        source_type: avatarCreateSourceType,
      };
      if (avatarCreateSourceType === "photo") body.source_image = avatarCreateSource.trim();
      if (avatarCreateSourceType === "prompt") body.prompt = avatarCreatePrompt.trim();
      if (avatarCreateSourceType === "video") body.source_video = avatarCreateSource.trim();
      const res = await fetch(`${API}/avatar-create`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Error ${res.status}: ${text.slice(0, 300)}`);
        return;
      }
      let result: { isError?: boolean; content?: { type: string; text?: string }[]; _meta?: { status?: string } } = {};
      try {
        result = JSON.parse(text);
      } catch {}
      if (result.isError) {
        const msg = result.content?.find((c) => c.type === "text")?.text || "avatar creation failed";
        setStatus(`Error: ${msg}`);
        return;
      }
      setStatus(result._meta?.status === "completed" ? "Avatar created." : "Avatar creation started.");
      setAvatarCreateName("");
      setAvatarCreateSource("");
      setAvatarCreatePrompt("");
      await loadAvatarCreateJobs();
      await loadAvatars();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setAvatarCreating(false);
    }
  };

  const createVoice = async () => {
    if (voiceCreating) return;
    const name = voiceCreateName.trim();
    const description = voiceCreateDescription.trim();
    if (!name) {
      setStatus("Voice name required.");
      return;
    }
    if (description.length < 20) {
      setStatus("Voice description must be at least 20 characters.");
      return;
    }
    setVoiceCreating(true);
    setStatus("Creating voice…");
    try {
      const options: Record<string, unknown> = {
        auto_generate_text: !voiceCreatePreviewText.trim(),
        should_enhance: voiceCreateEnhance,
        quality: 0.9,
        loudness: 0.5,
        output_format: "mp3_44100_128",
      };
      if (voiceCreatePreviewText.trim()) options.text = voiceCreatePreviewText.trim();
      const res = await fetch(
        `${API}/identity-create?project_id=${encodeURIComponent(projectId)}`,
        {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            project_id: projectId,
            kind: "voice",
            name,
            source_type: "prompt",
            voice_description: description,
            labels: {
              use_case: "hypnosis",
              created_in: "media-studio",
            },
            options,
          }),
        },
      );
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Error ${res.status}: ${text.slice(0, 300)}`);
        return;
      }
      let result: {
        isError?: boolean;
        content?: { type: string; text?: string }[];
        _meta?: { provider_identity_id?: string; identity?: MediaIdentity };
      } = {};
      try {
        result = JSON.parse(text);
      } catch {}
      if (result.isError) {
        const msg = result.content?.find((c) => c.type === "text")?.text || "voice creation failed";
        setStatus(`Error: ${msg}`);
        return;
      }
      const providerVoiceID = result._meta?.provider_identity_id || result._meta?.identity?.provider_identity_id || "";
      if (providerVoiceID) setVoice(providerVoiceID);
      setVoiceCreateName("");
      setVoiceCreateDescription("");
      setVoiceCreatePreviewText("");
      setVoiceCreateOpen(false);
      setStatus("Voice created.");
      await loadVoiceIdentities();
      await loadVoices();
      if (providerVoiceID) setVoice(providerVoiceID);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setVoiceCreating(false);
    }
  };

  const buildGenerationBody = (): Record<string, unknown> | null => {
    const body: Record<string, unknown> = {
      kind: activeKind,
      prompt,
      project_id: projectId,
    };
    if (storageFolder.trim()) body.storage_folder = storageFolder.trim();
    if (activeKind === "image") {
      if (isEditMode) {
        if (sourceImages.length > editSourceLimit) {
          setStatus(`Error: ${editModel} supports at most ${editSourceLimit} source image${editSourceLimit === 1 ? "" : "s"}.`);
          return null;
        }
        body.model = editModel;
        if (sourceImages.length === 1) {
          body.source_image = sourceImages[0].value;
        } else {
          body.source_images = sourceImages.map((x) => x.value);
        }
        body.options = { output_format: imageFormat, safe_mode: safeMode };
      } else {
        body.model = imageModel;
        const wantsAspect = !!currentModel?.aspect_ratios?.length;
        const wantsResolution = !!currentModel?.resolutions?.length;
        const wantsPixelSize = !wantsAspect || !!currentModel?.pixel_sizes?.length;
        if (wantsPixelSize) body.size = imageSize;
        if (wantsAspect) body.aspect = aspect;
        if (wantsResolution) body.resolution = imageResolution;
        const options: Record<string, unknown> = { safe_mode: safeMode };
        if (!liveProvider || liveProvider === "openai-api") {
          if (imageModel !== "dall-e-2") options.quality = imageQuality;
          if (isGptImage(imageModel)) options.output_format = imageFormat;
        } else if (liveProvider === "venice-ai") {
          options.format = imageFormat;
        }
        body.options = options;
      }
    } else if (activeKind === "video") {
      if (videoModel) body.model = videoModel;
      body.duration = duration;
      body.aspect = aspect;
      if (videoNoSound) {
        body.options = { audio: false };
      }
      // Image-to-video: pass the reference image through the same
      // source_image arg the dispatcher uses for image.edit.
      if (showVideoRefInput && sourceImages[0]?.value) {
        body.source_image = sourceImages[0].value;
      }
    } else if (activeKind === "audio_tts") {
      if (audioModel) body.model = audioModel;
      if (voice) body.voice = voice;
    } else if (activeKind === "audio_sfx") {
      if (sfxModel) body.model = sfxModel;
      body.duration = duration;
    } else if (activeKind === "music") {
      if (musicModel) body.model = musicModel;
      body.duration = duration;
    } else if (activeKind === "avatar") {
      // prompt carries the spoken script; avatar = replica/avatar id.
      body.avatar = selectedAvatar;
      if (voice) body.voice = voice;
      if (currentBinding?.slug === "heygen") {
        const selected = avatars.find((x) => x.id === selectedAvatar);
        const options: Record<string, unknown> = {
          resolution: avatarResolution,
          aspect: avatarAspect,
          estimated_duration_seconds: estimateSpeechSeconds(prompt),
        };
        if (avatarEngine !== "auto") options.engine = { type: avatarEngine };
        if (selected?.avatar_type) options.avatar_type = selected.avatar_type;
        body.options = options;
      }
    }
    return body;
  };

  useEffect(() => {
    if (!prompt.trim() || !isBound) {
      setEstimate(null);
      setEstimateLoading(false);
      return;
    }
    const body = buildGenerationBody();
    if (!body) {
      setEstimate(null);
      setEstimateLoading(false);
      return;
    }
    let cancelled = false;
    setEstimateLoading(true);
    const timer = window.setTimeout(() => {
      fetch(`${API}/estimate?project_id=${encodeURIComponent(projectId)}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
        .then((res) => (res.ok ? res.json() : null))
        .then((data) => {
          if (cancelled) return;
          setEstimate(data || null);
        })
        .catch(() => {
          if (!cancelled) setEstimate(null);
        })
        .finally(() => {
          if (!cancelled) setEstimateLoading(false);
        });
    }, 350);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [
    projectId,
    prompt,
    isBound,
    activeKind,
    isEditMode,
    imageModel,
    editModel,
    imageSize,
    imageResolution,
    imageQuality,
    imageFormat,
    safeMode,
    videoModel,
    duration,
    aspect,
    videoNoSound,
    audioModel,
    sfxModel,
    musicModel,
    voice,
    selectedAvatar,
    avatarEngine,
    avatarResolution,
    avatarAspect,
    storageFolder,
    sourceImages,
  ]);

  const submitGeneration = async (mode: "generate" | "draft") => {
    if (!prompt.trim() || generating || creatingDraft) return;
    if (mode === "generate") {
      setGenerating(true);
      setStatus("Generating…");
    } else {
      setCreatingDraft(true);
      setStatus("Saving draft…");
    }
    try {
      const body = buildGenerationBody();
      if (!body) return;
      if (mode === "draft") body.mode = "draft";
      const res = await fetch(`${API}/generate`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Error ${res.status}: ${text.slice(0, 300)}`);
        return;
      }
      let result: { isError?: boolean; content?: { type: string; text?: string }[] } = {};
      try {
        result = JSON.parse(text);
      } catch {}
      if (result.isError) {
        const msg = result.content?.find((c) => c.type === "text")?.text || "generation failed";
        setStatus(`Error: ${msg}`);
        return;
      }
      // Async kinds (video today) return _meta.status === "queued".
      // Tell the user the bytes will arrive later via the event/poll loop.
      const meta = (result as unknown as {
        _meta?: { status?: string; job_id?: number; generation_id?: number; cost_usd?: number };
      })._meta;
      if (meta?.status === "draft") {
        setPrompt("");
        const costStr = meta.cost_usd ? ` · est. ${formatCost(meta.cost_usd)}` : "";
        setStatus(`Draft saved.${costStr}`);
        loadGenerations();
        return;
      }
      if (meta?.status === "queued") {
        const queuedPrompt = prompt;
        setPrompt("");
        const costStr = meta.cost_usd ? ` · est. ${formatCost(meta.cost_usd)}` : "";
        setStatus(`Generating…${costStr}`);
        if (meta.job_id && (activeKind === "video" || activeKind === "avatar")) {
          setVideoJobs((cur) => {
            if (cur.some((j) => j.id === meta.job_id)) return cur;
            return [
              {
                id: meta.job_id || 0,
                queue_id: "",
                model: activeGeneratingModel,
                prompt: queuedPrompt,
                status: "queued",
                error: "",
                generation_id: meta.generation_id || 0,
                cost_usd: meta.cost_usd || 0,
              },
              ...cur,
            ];
          });
        }
        return;
      }
      setPrompt("");
      setStatus("Done.");
      loadGenerations();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setGenerating(false);
      setCreatingDraft(false);
    }
  };

  const generate = () => submitGeneration("generate");
  const createDraft = () => submitGeneration("draft");

  const generateDraft = async (g: Generation) => {
    if (generatingDraftId || g.status !== "draft") return;
    setGeneratingDraftId(g.id);
    setStatus("Generating draft…");
    try {
      const res = await fetch(`${API}/generate`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ generation_id: g.id, project_id: projectId }),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Error ${res.status}: ${text.slice(0, 300)}`);
        return;
      }
      let result: { isError?: boolean; content?: { type: string; text?: string }[]; _meta?: { status?: string; job_id?: number; generation_id?: number; cost_usd?: number } } = {};
      try {
        result = JSON.parse(text);
      } catch {}
      if (result.isError) {
        const msg = result.content?.find((c) => c.type === "text")?.text || "generation failed";
        setStatus(`Error: ${msg}`);
        return;
      }
      const meta = result._meta;
      if (meta?.status === "queued") {
        const costStr = meta.cost_usd ? ` · est. ${formatCost(meta.cost_usd)}` : "";
        setStatus(`Generating…${costStr}`);
        if (meta.job_id && (g.kind === "video" || g.kind === "avatar")) {
          setVideoJobs((cur) => {
            if (cur.some((j) => j.id === meta.job_id)) return cur;
            return [
              {
                id: meta.job_id || 0,
                queue_id: "",
                model: g.model,
                prompt: g.prompt,
                status: "queued",
                error: "",
                generation_id: meta.generation_id || g.id,
                cost_usd: meta.cost_usd || g.cost_usd || 0,
              },
              ...cur,
            ];
          });
        }
      } else {
        setStatus("Done.");
      }
      loadGenerations();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setGeneratingDraftId(null);
    }
  };

  const deleteGeneration = async (g: Generation) => {
    if (deletingId) return;
    const storageCount = g.storage_ids?.length || 0;
    const label = storageCount > 0
      ? `Delete this generation and ${storageCount} linked Storage file${storageCount === 1 ? "" : "s"}?`
      : "Delete this generation?";
    if (!window.confirm(label)) return;
    setDeletingId(g.id);
    setStatus("Deleting…");
    try {
      const res = await fetch(`${API}/delete?project_id=${encodeURIComponent(projectId)}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ id: g.id, delete_storage: true }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.error) {
        setStatus(`Delete failed: ${data.error || res.status}`);
        return;
      }
      setItems((cur) => cur.filter((x) => x.id !== g.id));
      setSelected((cur) => (cur?.id === g.id ? null : cur));
      setLightbox((cur) => (cur?.id === g.id ? null : cur));
      setVideoJobs((cur) => cur.filter((x) => x.generation_id !== g.id));
      setStatus("Deleted.");
    } catch (e) {
      setStatus("Delete failed: " + (e as Error).message);
    } finally {
      setDeletingId(null);
    }
  };

  return (
    <div className="h-full flex flex-col">
      {/* Kind tabs */}
      <nav className="flex items-center border-b border-border px-4">
        {(Object.keys(TAB_LABELS) as Array<"image" | "video" | "audio_tts" | "music" | "avatar">).map((k) => {
          const t: "image" | "video" | "audio" | "music" | "avatar" =
            k === "audio_tts" ? "audio" : (k as "image" | "video" | "music" | "avatar");
          const active = tab === t;
          const bindingKey: Kind = k;
          const bound = bindings ? bindings[bindingKey]?.bound : false;
          return (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={
                "flex items-center gap-1.5 px-3 py-2.5 text-sm border-b-2 transition-colors " +
                (active
                  ? "border-accent text-text"
                  : "border-transparent text-text-muted hover:text-text")
              }
            >
              <KindIcon kind={k} />
              {TAB_LABELS[k]}
              <BoundDot bound={bound} />
            </button>
          );
        })}
      </nav>

      {/* Audio sub-tabs (TTS / SFX) */}
      {tab === "audio" && (
        <div className="flex items-center gap-1 px-4 py-1.5 border-b border-border bg-bg-card">
          <SubTabButton
            label="TTS"
            active={audioSubKind === "audio_tts"}
            onClick={() => setAudioSubKind("audio_tts")}
            bound={!!bindings?.audio_tts.bound}
          />
          <SubTabButton
            label="SFX"
            active={audioSubKind === "audio_sfx"}
            onClick={() => setAudioSubKind("audio_sfx")}
            bound={!!bindings?.audio_sfx.bound}
          />
        </div>
      )}

      {/* Unbound banner */}
      {bindings && !isBound && (
        <div className="px-4 py-2 text-xs text-text-muted bg-bg-card border-b border-border">
          No provider bound for <strong className="text-text">{activeKind}</strong>. Open the app
          settings to pick one.
        </div>
      )}

      {/* Main area */}
      <div className="flex-1 flex min-h-0">
        <div className="flex-1 flex flex-col p-6 gap-4 min-w-0">
          {(activeKind === "image" || showVideoRefInput) && (
            <ReferenceImageInput
              sources={showVideoRefInput ? sourceImages.slice(0, 1) : sourceImages}
              maxSources={referenceInputMax}
              onAdd={addSourceImage}
              onRemove={removeSourceImage}
              onClear={() => setSourceImages([])}
              hint={
                showVideoRefInput
                  ? "Source image for the image-to-video model (required)"
                  : undefined
              }
            />
          )}

          <Composer
            kind={activeKind}
            prompt={prompt}
            setPrompt={setPrompt}
            generate={generate}
            createDraft={createDraft}
            generating={generating}
            creatingDraft={creatingDraft}
            disabled={!isBound}
            isEditMode={isEditMode}
            liveModels={liveModels}
            liveProvider={liveProvider}
            imageModel={imageModel}
            setImageModel={setImageModel}
            imageSize={imageSize}
            setImageSize={setImageSize}
            imageResolution={imageResolution}
            setImageResolution={setImageResolution}
            imageQuality={imageQuality}
            setImageQuality={setImageQuality}
            imageFormat={imageFormat}
            setImageFormat={setImageFormat}
            editModel={editModel}
            setEditModel={setEditModel}
            editSourceLimit={editSourceLimit}
            editModels={editLiveModels || liveModels?.filter((m) => m.supports_image_edit) || []}
            videoModel={videoModel}
            setVideoModel={setVideoModel}
            audioModel={audioModel}
            setAudioModel={setAudioModel}
            sfxModel={sfxModel}
            setSfxModel={setSfxModel}
            musicModel={musicModel}
            setMusicModel={setMusicModel}
            currentModel={currentModel}
            safeMode={safeMode}
            setSafeMode={setSafeMode}
            duration={duration}
            setDuration={setDuration}
            aspect={aspect}
            setAspect={setAspect}
            videoNoSound={videoNoSound}
            setVideoNoSound={setVideoNoSound}
            voice={voice}
            setVoice={setVoice}
            avatars={avatars}
            selectedAvatar={selectedAvatar}
            setSelectedAvatar={setSelectedAvatar}
            avatarCaps={avatarCaps}
            avatarCreateJobs={avatarCreateJobs}
            avatarCreateOpen={avatarCreateOpen}
            setAvatarCreateOpen={setAvatarCreateOpen}
            avatarCreateName={avatarCreateName}
            setAvatarCreateName={setAvatarCreateName}
            avatarCreateSourceType={avatarCreateSourceType}
            setAvatarCreateSourceType={setAvatarCreateSourceType}
            avatarCreateSource={avatarCreateSource}
            setAvatarCreateSource={setAvatarCreateSource}
            avatarCreatePrompt={avatarCreatePrompt}
            setAvatarCreatePrompt={setAvatarCreatePrompt}
            avatarCreating={avatarCreating}
            createAvatar={createAvatar}
            avatarProvider={currentBinding?.slug || ""}
            avatarEngine={avatarEngine}
            setAvatarEngine={setAvatarEngine}
            avatarResolution={avatarResolution}
            setAvatarResolution={setAvatarResolution}
            avatarAspect={avatarAspect}
            setAvatarAspect={setAvatarAspect}
            voices={voices}
            voiceIdentities={voiceIdentities}
            voiceCreateOpen={voiceCreateOpen}
            setVoiceCreateOpen={setVoiceCreateOpen}
            voiceCreateName={voiceCreateName}
            setVoiceCreateName={setVoiceCreateName}
            voiceCreateDescription={voiceCreateDescription}
            setVoiceCreateDescription={setVoiceCreateDescription}
            voiceCreatePreviewText={voiceCreatePreviewText}
            setVoiceCreatePreviewText={setVoiceCreatePreviewText}
            voiceCreateEnhance={voiceCreateEnhance}
            setVoiceCreateEnhance={setVoiceCreateEnhance}
            voiceCreating={voiceCreating}
            createVoice={createVoice}
            audioProvider={currentBinding?.slug || ""}
            storageBound={!!bindings?.storage?.bound}
            storageFolder={storageFolder}
            setStorageFolder={setStorageFolder}
            storageFolderDefault={defaultOutputFolder(activeKind)}
            folderSuggestions={folderSuggestions}
            estimate={estimate}
            estimateLoading={estimateLoading}
          />

          {(activeKind === "video" || activeKind === "avatar") && videoJobs.some((j) => j.status === "failed") && (
            <VideoJobsBanner jobs={videoJobs} />
          )}

          <div className="flex-1 overflow-auto border border-border rounded">
            {items.length === 0 && !generating && pendingVideoJobs.length === 0 ? (
              <div className="py-12 px-6 text-center text-text-muted text-sm">
                {status || "No generations yet for this kind."}
              </div>
            ) : (
              <Gallery
                kind={activeKind}
                items={items}
                  onSelect={setSelected}
                  onOpenLightbox={setLightbox}
                  onGenerateDraft={generateDraft}
                  generating={generating}
                  generatingPrompt={prompt}
                  generatingModel={activeGeneratingModel}
                  generatingCostUSD={estimate?.cost_usd || 0}
                  pendingJobs={pendingVideoJobs}
                  generatingDraftId={generatingDraftId}
                />
            )}
          </div>
          <div className="text-xs text-text-dim">{status}</div>
        </div>

        {selected && (
          <DetailAside
            selected={selected}
            onClose={() => setSelected(null)}
            deleting={deletingId === selected.id}
            onDelete={() => deleteGeneration(selected)}
            onUseAsReference={
              selected.kind === "image" && selected.storage_ids.length > 0
                ? () => {
                    const id = selected.storage_ids[0];
                    addSourceImage(`storage:${id}`, `Storage #${id}`);
                    setSelected(null);
                    setTab("image");
                  }
                : undefined
            }
          />
        )}
      </div>

      {lightbox && (
        <Lightbox
          item={lightbox}
          onClose={() => setLightbox(null)}
          onUseAsReference={
            lightbox.kind === "image" && lightbox.storage_ids.length > 0
              ? () => {
                  const id = lightbox.storage_ids[0];
                  addSourceImage(`storage:${id}`, `Storage #${id}`);
                  setLightbox(null);
                  setTab("image");
                }
              : undefined
          }
        />
      )}
    </div>
  );
}

// ─── sub-components ────────────────────────────────────────────────

function KindIcon({ kind }: { kind: Kind }) {
  if (kind === "image") return <IconImage />;
  if (kind === "video") return <IconVideo />;
  if (kind === "music") return <IconMusic />;
  if (kind === "avatar") return <IconAvatar />;
  return <IconAudio />;
}

function IconAvatar() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5">
      <circle cx="8" cy="5.5" r="2.5" />
      <path d="M3 13.5c0-2.8 2.2-4.5 5-4.5s5 1.7 5 4.5" />
    </svg>
  );
}

function BoundDot({ bound }: { bound: boolean }) {
  // Tiny status dot. Green when bound, dim when not.
  return (
    <span
      className="rounded-full ml-1"
      style={{
        width: 6,
        height: 6,
        background: bound ? "var(--apteva-accent, #4ade80)" : "var(--apteva-text-dim, #555)",
      }}
    />
  );
}

function SubTabButton({
  label,
  active,
  bound,
  onClick,
}: {
  label: string;
  active: boolean;
  bound: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={
        "flex items-center gap-1.5 px-2.5 py-1 text-xs rounded transition-colors " +
        (active
          ? "bg-bg-input text-text"
          : "text-text-muted hover:text-text")
      }
    >
      {label}
      <BoundDot bound={bound} />
    </button>
  );
}

interface ComposerProps {
  kind: Kind;
  prompt: string;
  setPrompt: (v: string) => void;
  generate: () => void;
  createDraft: () => void;
  generating: boolean;
  creatingDraft: boolean;
  disabled: boolean;
  isEditMode: boolean;
  liveModels: LiveModel[] | null;
  liveProvider: string;
  imageModel: ImageModel;
  setImageModel: (v: ImageModel) => void;
  imageSize: string;
  setImageSize: (v: string) => void;
  imageResolution: string;
  setImageResolution: (v: string) => void;
  imageQuality: string;
  setImageQuality: (v: string) => void;
  imageFormat: string;
  setImageFormat: (v: string) => void;
  editModel: EditModel;
  setEditModel: (v: EditModel) => void;
  editSourceLimit: number;
  editModels: LiveModel[];
  videoModel: string;
  setVideoModel: (v: string) => void;
  audioModel: string;
  setAudioModel: (v: string) => void;
  sfxModel: string;
  setSfxModel: (v: string) => void;
  musicModel: string;
  setMusicModel: (v: string) => void;
  currentModel?: LiveModel;
  safeMode: boolean;
  setSafeMode: (v: boolean) => void;
  duration: number;
  setDuration: (v: number) => void;
  aspect: string;
  setAspect: (v: string) => void;
  videoNoSound: boolean;
  setVideoNoSound: (v: boolean) => void;
  voice: string;
  setVoice: (v: string) => void;
  avatars: AvatarEntry[];
  selectedAvatar: string;
  setSelectedAvatar: (v: string) => void;
  avatarCaps: AvatarCapabilities | null;
  avatarCreateJobs: AvatarCreateJob[];
  avatarCreateOpen: boolean;
  setAvatarCreateOpen: (v: boolean) => void;
  avatarCreateName: string;
  setAvatarCreateName: (v: string) => void;
  avatarCreateSourceType: "photo" | "prompt" | "video";
  setAvatarCreateSourceType: (v: "photo" | "prompt" | "video") => void;
  avatarCreateSource: string;
  setAvatarCreateSource: (v: string) => void;
  avatarCreatePrompt: string;
  setAvatarCreatePrompt: (v: string) => void;
  avatarCreating: boolean;
  createAvatar: () => void;
  avatarProvider: string;
  avatarEngine: string;
  setAvatarEngine: (v: string) => void;
  avatarResolution: string;
  setAvatarResolution: (v: string) => void;
  avatarAspect: string;
  setAvatarAspect: (v: string) => void;
  voices: VoiceEntry[];
  voiceIdentities: MediaIdentity[];
  voiceCreateOpen: boolean;
  setVoiceCreateOpen: (v: boolean) => void;
  voiceCreateName: string;
  setVoiceCreateName: (v: string) => void;
  voiceCreateDescription: string;
  setVoiceCreateDescription: (v: string) => void;
  voiceCreatePreviewText: string;
  setVoiceCreatePreviewText: (v: string) => void;
  voiceCreateEnhance: boolean;
  setVoiceCreateEnhance: (v: boolean) => void;
  voiceCreating: boolean;
  createVoice: () => void;
  audioProvider: string;
  storageBound: boolean;
  storageFolder: string;
  setStorageFolder: (v: string) => void;
  storageFolderDefault: string;
  folderSuggestions: string[];
  estimate: GenerationEstimate | null;
  estimateLoading: boolean;
}

function Composer(p: ComposerProps) {
  const promptPlaceholder = p.isEditMode
    ? "Edit instruction — 'remove the tree', 'change sky to sunset'"
    : p.kind === "avatar"
      ? "Script the avatar will speak…"
      : p.kind === "audio_tts"
      ? "Text to speak"
      : p.kind === "music"
        ? "A jazzy lo-fi loop with piano"
        : p.kind === "video"
          ? "A cat walking through a sunlit garden"
          : p.kind === "audio_sfx"
            ? "A door creaking open"
            : "a cat in a hat";
  const selectedAvatar = p.avatars.find((av) => av.id === p.selectedAvatar);
  const avatarVBlocked =
    p.kind === "avatar" &&
    p.avatarProvider === "heygen" &&
    p.avatarEngine === "avatar_v" &&
    !!selectedAvatar &&
    !avatarSupportsEngine(selectedAvatar, "avatar_v");
  return (
    <div className="flex items-end gap-3 flex-wrap">
      <div className="flex-1" style={{ minWidth: 240 }}>
        <label className="text-text-muted text-xs">Prompt</label>
        <input
          type="text"
          value={p.prompt}
          onChange={(e) => p.setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") p.generate();
          }}
          placeholder={promptPlaceholder}
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        />
      </div>
      {p.kind === "image" && p.isEditMode && (
        <EditOptions
          model={p.editModel}
          setModel={p.setEditModel}
          format={p.imageFormat}
          setFormat={p.setImageFormat}
          maxSources={p.editSourceLimit}
          liveModels={p.editModels}
          liveProvider={p.liveProvider}
        />
      )}
      {p.kind === "image" && !p.isEditMode && (
        <ImageOptions
          model={p.imageModel}
          setModel={p.setImageModel}
          size={p.imageSize}
          setSize={p.setImageSize}
          imageResolution={p.imageResolution}
          setImageResolution={p.setImageResolution}
          quality={p.imageQuality}
          setQuality={p.setImageQuality}
          format={p.imageFormat}
          setFormat={p.setImageFormat}
          liveModels={p.liveModels}
          liveProvider={p.liveProvider}
          currentModel={p.currentModel}
          aspect={p.aspect}
          setAspect={p.setAspect}
        />
      )}
      {p.kind === "video" && (
        <>
          <MediaModelPicker
            model={p.videoModel}
            setModel={p.setVideoModel}
            liveModels={p.liveModels}
            liveProvider={p.liveProvider}
          />
          <ConstrainedDuration
            durations={p.currentModel?.durations}
            value={p.duration}
            onChange={p.setDuration}
          />
          <ConstrainedAspect
            aspects={p.currentModel?.aspect_ratios}
            value={p.aspect}
            onChange={p.setAspect}
            disabledHint={
              p.currentModel?.model_type === "image-to-video"
                ? "Inherited from source image"
                : undefined
            }
          />
          <VideoAudioSelect
            value={p.videoNoSound}
            onChange={p.setVideoNoSound}
            configurable={p.currentModel?.audio_configurable}
          />
        </>
      )}
      {p.kind === "audio_tts" && (
        <>
          <VoiceCreatePanel
            provider={p.audioProvider}
            identities={p.voiceIdentities}
            open={p.voiceCreateOpen}
            setOpen={p.setVoiceCreateOpen}
            name={p.voiceCreateName}
            setName={p.setVoiceCreateName}
            description={p.voiceCreateDescription}
            setDescription={p.setVoiceCreateDescription}
            previewText={p.voiceCreatePreviewText}
            setPreviewText={p.setVoiceCreatePreviewText}
            enhance={p.voiceCreateEnhance}
            setEnhance={p.setVoiceCreateEnhance}
            creating={p.voiceCreating}
            createVoice={p.createVoice}
            selectedVoice={p.voice}
            setSelectedVoice={p.setVoice}
          />
          <MediaModelPicker
            model={p.audioModel}
            setModel={p.setAudioModel}
            liveModels={p.liveModels}
            liveProvider={p.liveProvider}
          />
          {p.voices.length > 0 ? (
            <VoiceSelect voice={p.voice} setVoice={p.setVoice} voices={p.voices} />
          ) : (
            <TextField label="Voice" value={p.voice} onChange={p.setVoice} placeholder="voice_id" />
          )}
        </>
      )}
      {p.kind === "audio_sfx" && (
        <>
          <MediaModelPicker
            model={p.sfxModel}
            setModel={p.setSfxModel}
            liveModels={p.liveModels}
            liveProvider={p.liveProvider}
          />
          <NumberField label="Duration (s)" value={p.duration} onChange={p.setDuration} min={1} max={30} />
        </>
      )}
      {p.kind === "music" && (
        <>
          <MediaModelPicker
            model={p.musicModel}
            setModel={p.setMusicModel}
            liveModels={p.liveModels}
            liveProvider={p.liveProvider}
          />
          <NumberField label="Duration (s)" value={p.duration} onChange={p.setDuration} min={3} max={300} />
        </>
      )}
      {p.kind === "avatar" && (
        <AvatarCreatePanel
          provider={p.avatarProvider}
          caps={p.avatarCaps}
          jobs={p.avatarCreateJobs}
          open={p.avatarCreateOpen}
          setOpen={p.setAvatarCreateOpen}
          name={p.avatarCreateName}
          setName={p.setAvatarCreateName}
          sourceType={p.avatarCreateSourceType}
          setSourceType={p.setAvatarCreateSourceType}
          source={p.avatarCreateSource}
          setSource={p.setAvatarCreateSource}
          prompt={p.avatarCreatePrompt}
          setPrompt={p.setAvatarCreatePrompt}
          creating={p.avatarCreating}
          createAvatar={p.createAvatar}
        />
      )}
      {p.kind === "avatar" && (
        <AvatarPicker
          avatars={p.avatars}
          selected={p.selectedAvatar}
          setSelected={p.setSelectedAvatar}
        />
      )}
      {p.kind === "avatar" && p.avatarProvider === "heygen" && (
        <HeyGenAvatarOptions
          prompt={p.prompt}
          selectedAvatar={selectedAvatar}
          engine={p.avatarEngine}
          setEngine={p.setAvatarEngine}
          resolution={p.avatarResolution}
          setResolution={p.setAvatarResolution}
          aspect={p.avatarAspect}
          setAspect={p.setAvatarAspect}
        />
      )}
      {p.kind === "avatar" && p.voices.length > 0 && (
        <VoiceSelect voice={p.voice} setVoice={p.setVoice} voices={p.voices} />
      )}
      {p.kind === "image" && (
        <SafeModeToggle value={p.safeMode} onChange={p.setSafeMode} />
      )}
      {p.storageBound && (
        <StorageFolderField
          value={p.storageFolder}
          onChange={p.setStorageFolder}
          placeholder={p.storageFolderDefault}
          suggestions={p.folderSuggestions}
        />
      )}
      <EstimateBadge estimate={p.estimate} loading={p.estimateLoading} />
      <button
        onClick={p.generate}
        disabled={!p.prompt.trim() || p.generating || p.creatingDraft || p.disabled || avatarVBlocked}
        className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
        title={avatarVBlocked ? "Selected avatar does not advertise Avatar V support" : undefined}
      >
        {p.generating ? "…" : p.isEditMode ? "Edit" : p.kind === "avatar" ? "Generate avatar" : "Generate"}
      </button>
      <button
        onClick={p.createDraft}
        disabled={!p.prompt.trim() || p.generating || p.creatingDraft}
        className="px-3 py-1.5 text-sm border border-border rounded text-text-muted hover:text-text hover:border-accent disabled:opacity-50"
      >
        {p.creatingDraft ? "Saving…" : "Create draft"}
      </button>
    </div>
  );
}

function EstimateBadge({
  estimate,
  loading,
}: {
  estimate: GenerationEstimate | null;
  loading: boolean;
}) {
  const cost = formatCost(estimate?.cost_usd || 0);
  const label = loading
    ? "estimating"
    : cost
      ? `est. ${cost}`
      : estimate && !estimate.available
        ? "cost unavailable"
        : "";
  if (!label) return null;
  return (
    <div
      className="px-2 py-1 rounded border border-border bg-bg-card text-xs text-text-muted"
      title={estimate?.source || "estimate"}
    >
      {label}
    </div>
  );
}

function VoiceCreatePanel({
  provider,
  identities,
  open,
  setOpen,
  name,
  setName,
  description,
  setDescription,
  previewText,
  setPreviewText,
  enhance,
  setEnhance,
  creating,
  createVoice,
  selectedVoice,
  setSelectedVoice,
}: {
  provider: string;
  identities: MediaIdentity[];
  open: boolean;
  setOpen: (v: boolean) => void;
  name: string;
  setName: (v: string) => void;
  description: string;
  setDescription: (v: string) => void;
  previewText: string;
  setPreviewText: (v: string) => void;
  enhance: boolean;
  setEnhance: (v: boolean) => void;
  creating: boolean;
  createVoice: () => void;
  selectedVoice: string;
  setSelectedVoice: (v: string) => void;
}) {
  const readyVoices = identities.filter((x) => x.status === "ready" && x.provider_identity_id);
  const selectedTracked = readyVoices.some((x) => x.provider_identity_id === selectedVoice);
  return (
    <div style={{ flexBasis: "100%" }} className="border border-border rounded p-2 bg-bg-card">
      <div className="flex items-center gap-2 flex-wrap">
        <button
          onClick={() => setOpen(!open)}
          className="text-xs px-2 py-1 border border-border rounded text-accent hover:border-accent disabled:opacity-50"
          disabled={provider !== "elevenlabs"}
        >
          {open ? "Close voice creator" : "Create voice"}
        </button>
        <span className="text-text-dim" style={{ fontSize: 10 }}>
          {provider === "elevenlabs"
            ? "ElevenLabs Voice Design"
            : provider
              ? `voice creation not wired for ${provider}`
              : "bind ElevenLabs audio"}
        </span>
        {readyVoices.length > 0 && (
          <>
            <span className="text-text-dim" style={{ fontSize: 10 }}>
              {readyVoices.length} custom
            </span>
            <select
              value={selectedTracked ? selectedVoice : ""}
              onChange={(e) => {
                if (e.target.value) setSelectedVoice(e.target.value);
              }}
              className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
              style={{ maxWidth: 260 }}
            >
              <option value="">Tracked voices</option>
              {readyVoices.map((identity) => (
                <option key={identity.id} value={identity.provider_identity_id}>
                  {identity.name || identity.provider_identity_id}
                </option>
              ))}
            </select>
          </>
        )}
      </div>
      {open && (
        <div className="mt-2 flex flex-col gap-2">
          <div className="flex items-end gap-2 flex-wrap">
            <TextField label="Name" value={name} onChange={setName} placeholder="Hypno Mistress" />
            <label className="flex items-center gap-1.5 text-xs text-text-muted cursor-pointer select-none pb-2">
              <input
                type="checkbox"
                checked={enhance}
                onChange={(e) => setEnhance(e.target.checked)}
                style={{ accentColor: "var(--apteva-accent, #4ade80)" }}
              />
              Enhance prompt
            </label>
            <button
              onClick={createVoice}
              disabled={creating || !name.trim() || description.trim().length < 20 || provider !== "elevenlabs"}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              {creating ? "…" : "Create"}
            </button>
          </div>
          <div>
            <label className="text-text-muted text-xs block">Voice description</label>
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Adult feminine voice, intimate and composed, low warm tone, slow confident pacing, polished studio recording."
              rows={3}
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            />
          </div>
          <div>
            <label className="text-text-muted text-xs block">Preview text</label>
            <textarea
              value={previewText}
              onChange={(e) => setPreviewText(e.target.value)}
              placeholder="Optional. Leave empty to let ElevenLabs generate preview text."
              rows={2}
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            />
          </div>
        </div>
      )}
      {identities.some((x) => x.status === "failed") && (
        <div className="flex gap-2 flex-wrap mt-2">
          {identities.filter((x) => x.status === "failed").slice(0, 4).map((identity) => (
            <div
              key={identity.id}
              className="border rounded px-2 py-1 bg-bg-input"
              style={{ borderColor: "var(--apteva-danger, #ef4444)", fontSize: 10, maxWidth: 220 }}
              title={identity.error || identity.prompt || identity.name}
            >
              <span className="text-text truncate block">{identity.name || `#${identity.id}`}</span>
              <span className="text-text-dim truncate block">{identity.error || identity.status}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function AvatarCreatePanel({
  provider,
  caps,
  jobs,
  open,
  setOpen,
  name,
  setName,
  sourceType,
  setSourceType,
  source,
  setSource,
  prompt,
  setPrompt,
  creating,
  createAvatar,
}: {
  provider: string;
  caps: AvatarCapabilities | null;
  jobs: AvatarCreateJob[];
  open: boolean;
  setOpen: (v: boolean) => void;
  name: string;
  setName: (v: string) => void;
  sourceType: "photo" | "prompt" | "video";
  setSourceType: (v: "photo" | "prompt" | "video") => void;
  source: string;
  setSource: (v: string) => void;
  prompt: string;
  setPrompt: (v: string) => void;
  creating: boolean;
  createAvatar: () => void;
}) {
  const sourceTypes = (caps?.source_types || ["photo"]).filter((x) => x === "photo" || x === "prompt" || x === "video") as Array<"photo" | "prompt" | "video">;
  const shownTypes = sourceTypes.length > 0 ? sourceTypes : ["photo"];
  const activeJobs = jobs.filter((j) => j.status === "queued" || j.status === "training");
  const failedJobs = jobs.filter((j) => j.status === "failed");
  const readFile = (file: File) => {
    const reader = new FileReader();
    reader.onload = () => setSource(String(reader.result || ""));
    reader.readAsDataURL(file);
  };
  return (
    <div style={{ flexBasis: "100%" }} className="border border-border rounded p-2 bg-bg-card">
      <div className="flex items-center gap-2">
        <button
          onClick={() => setOpen(!open)}
          className="text-xs px-2 py-1 border border-border rounded text-accent hover:border-accent"
          disabled={!provider}
        >
          {open ? "Close avatar creator" : "Create avatar"}
        </button>
        {activeJobs.length > 0 && (
          <span className="text-text-dim" style={{ fontSize: 10 }}>
            {activeJobs.length} creating
          </span>
        )}
        {failedJobs.length > 0 && (
          <span style={{ fontSize: 10, color: "var(--apteva-danger, #ef4444)" }}>
            {failedJobs.length} failed
          </span>
        )}
      </div>
      {open && (
        <div className="flex items-end gap-2 flex-wrap mt-2">
          <TextField label="Name" value={name} onChange={setName} placeholder="Avatar name" />
          <div>
            <label className="text-text-muted text-xs block">Source</label>
            <select
              value={sourceType}
              onChange={(e) => setSourceType(e.target.value as "photo" | "prompt" | "video")}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            >
              {shownTypes.map((t) => (
                <option key={t} value={t}>
                  {t === "photo" ? "Photo" : t === "prompt" ? "Prompt" : "Video"}
                </option>
              ))}
            </select>
          </div>
          {sourceType === "photo" && (
            <>
              <div className="flex-1" style={{ minWidth: 220 }}>
                <label className="text-text-muted text-xs block">Photo</label>
                <input
                  type="text"
                  value={source}
                  onChange={(e) => setSource(e.target.value)}
                  placeholder="storage:123 or https://..."
                  className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                />
              </div>
              <label className="text-xs px-2 py-1.5 border border-border rounded text-text hover:border-accent cursor-pointer">
                Upload
                <input
                  type="file"
                  accept="image/*"
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    if (file) readFile(file);
                    e.currentTarget.value = "";
                  }}
                  style={{ display: "none" }}
                />
              </label>
            </>
          )}
          {sourceType === "prompt" && (
            <div className="flex-1" style={{ minWidth: 260 }}>
              <label className="text-text-muted text-xs block">Prompt</label>
              <input
                type="text"
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              />
            </div>
          )}
          {sourceType === "video" && (
            <div className="flex-1" style={{ minWidth: 260 }}>
              <label className="text-text-muted text-xs block">Video URL</label>
              <input
                type="text"
                value={source}
                onChange={(e) => setSource(e.target.value)}
                placeholder="https://..."
                className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              />
            </div>
          )}
          <button
            onClick={createAvatar}
            disabled={creating || !name.trim() || ((sourceType === "photo" || sourceType === "video") && !source.trim()) || (sourceType === "prompt" && !prompt.trim())}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {creating ? "…" : "Create"}
          </button>
        </div>
      )}
      {jobs.length > 0 && (
        <div className="flex gap-2 flex-wrap mt-2">
          {jobs.slice(0, 6).map((job) => (
            <AvatarCreateJobPill key={job.id} job={job} />
          ))}
        </div>
      )}
    </div>
  );
}

function AvatarCreateJobPill({ job }: { job: AvatarCreateJob }) {
  const isFailed = job.status === "failed";
  const isDone = job.status === "completed";
  return (
    <div
      className="border rounded px-2 py-1 bg-bg-input"
      style={{
        borderColor: isFailed
          ? "var(--apteva-danger, #ef4444)"
          : isDone
            ? "var(--apteva-accent, #4ade80)"
            : "var(--apteva-border, #333)",
        fontSize: 10,
        maxWidth: 180,
      }}
      title={job.error || job.provider_avatar_id || job.name}
    >
      <span className="text-text truncate block">{job.name || `#${job.id}`}</span>
      <span className="text-text-dim truncate block">
        #{job.id} · {job.provider} · {job.status}
      </span>
    </div>
  );
}

// AvatarPicker — replica/avatar grid for the Avatar tab. Private
// HeyGen looks are grouped first so custom avatars do not get buried
// under the public library catalog.
function AvatarPicker({
  avatars,
  selected,
  setSelected,
}: {
  avatars: AvatarEntry[];
  selected: string;
  setSelected: (v: string) => void;
}) {
  if (avatars.length === 0) {
    return (
      <div>
        <label className="text-text-muted text-xs block">Avatar</label>
        <div className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim" style={{ minWidth: 200 }}>
          no replicas — train one in your provider
        </div>
      </div>
    );
  }
  const sections = avatarSections(avatars);
  return (
    <div style={{ flexBasis: "100%" }}>
      <label className="text-text-muted text-xs block mb-1">Avatar / replica ({avatars.length})</label>
      <div className="flex flex-col gap-2" style={{ maxHeight: 324, overflow: "auto", paddingRight: 4 }}>
        {sections.map((section) => (
          <div key={section.key}>
            <div className="text-text-dim mb-1" style={{ fontSize: 10 }}>
              {section.label} ({section.items.length})
            </div>
            <div className="flex gap-2 flex-wrap">
              {section.items.map((av) => (
                <AvatarTile key={av.id} avatar={av} selected={av.id === selected} onSelect={() => setSelected(av.id)} />
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function AvatarTile({ avatar, selected, onSelect }: { avatar: AvatarEntry; selected: boolean; onSelect: () => void }) {
  const titleParts = [
    avatar.name || avatar.id,
    avatar.status ? `(${avatar.status})` : "",
    avatar.avatar_type ? avatar.avatar_type.replace(/_/g, " ") : "",
  ].filter(Boolean);
  return (
    <button
      onClick={onSelect}
      title={titleParts.join(" ")}
      className={
        "border rounded overflow-hidden text-left bg-bg-card " +
        (selected ? "border-accent" : "border-border hover:border-accent")
      }
      style={{ width: 96 }}
    >
      <AvatarThumb avatar={avatar} />
      <div className="text-text truncate px-1 pt-0.5" style={{ fontSize: 10 }}>
        {avatar.name || avatar.id}
      </div>
      <div className="text-text-dim truncate px-1 pb-0.5" style={{ fontSize: 9 }}>
        {avatarTypeLabel(avatar)}
      </div>
    </button>
  );
}

function AvatarThumb({ avatar }: { avatar: AvatarEntry }) {
  if (!avatar.thumbnail) {
    return (
      <div
        className="flex items-center justify-center text-text-dim"
        style={{ width: 96, height: 96, background: "var(--apteva-bg-input, #222)", fontSize: 10 }}
      >
        {avatar.name || avatar.id}
      </div>
    );
  }
  if (avatar.thumbnail_type === "image" || looksLikeImageURL(avatar.thumbnail)) {
    return (
      <img
        src={avatar.thumbnail}
        alt=""
        loading="lazy"
        style={{ width: 96, height: 96, objectFit: "cover", display: "block" }}
      />
    );
  }
  return (
    <video
      src={avatar.thumbnail}
      muted
      loop
      playsInline
      onMouseEnter={(e) => (e.currentTarget as HTMLVideoElement).play()}
      onMouseLeave={(e) => (e.currentTarget as HTMLVideoElement).pause()}
      style={{ width: 96, height: 96, objectFit: "cover", display: "block" }}
    />
  );
}

function avatarSections(avatars: AvatarEntry[]) {
  const isPrivate = (av: AvatarEntry) => av.ownership === "private";
  const isPublic = (av: AvatarEntry) => av.ownership !== "private";
  const sections = [
    { key: "mine", label: "My avatars", items: avatars.filter(isPrivate) },
    {
      key: "photo",
      label: "Photo avatars",
      items: avatars.filter((av) => isPublic(av) && av.avatar_type === "photo_avatar"),
    },
    {
      key: "studio",
      label: "Studio avatars",
      items: avatars.filter((av) => isPublic(av) && (av.avatar_type === "studio_avatar" || av.avatar_type === "digital_twin")),
    },
    {
      key: "library",
      label: "Library",
      items: avatars.filter((av) => isPublic(av) && av.avatar_type !== "photo_avatar" && av.avatar_type !== "studio_avatar" && av.avatar_type !== "digital_twin"),
    },
  ];
  return sections.filter((section) => section.items.length > 0);
}

function avatarTypeLabel(av: AvatarEntry) {
  if (av.ownership === "private") return "mine";
  if (!av.avatar_type) return av.ownership === "public" ? "library" : "";
  return av.avatar_type.replace(/_/g, " ");
}

function looksLikeImageURL(url: string) {
  const clean = url.split("?", 1)[0].toLowerCase();
  return clean.endsWith(".png") || clean.endsWith(".jpg") || clean.endsWith(".jpeg") || clean.endsWith(".webp");
}

function avatarSupportsEngine(av: AvatarEntry, engine: string) {
  const engines = av.supported_api_engines || [];
  if (engines.length === 0) return engine !== "avatar_v";
  return engines.includes(engine);
}

function HeyGenAvatarOptions({
  prompt,
  selectedAvatar,
  engine,
  setEngine,
  resolution,
  setResolution,
  aspect,
  setAspect,
}: {
  prompt: string;
  selectedAvatar?: AvatarEntry;
  engine: string;
  setEngine: (v: string) => void;
  resolution: string;
  setResolution: (v: string) => void;
  aspect: string;
  setAspect: (v: string) => void;
}) {
  const cost = estimateHeyGenAvatarCost(prompt, selectedAvatar, resolution);
  const blocked = engine === "avatar_v" && !!selectedAvatar && !avatarSupportsEngine(selectedAvatar, "avatar_v");
  return (
    <>
      <div>
        <label className="text-text-muted text-xs block">Engine</label>
        <select
          value={engine}
          onChange={(e) => setEngine(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          <option value="avatar_v">Avatar V</option>
          <option value="avatar_iv">Avatar IV</option>
          <option value="auto">Auto</option>
        </select>
      </div>
      <div>
        <label className="text-text-muted text-xs block">Resolution</label>
        <select
          value={resolution}
          onChange={(e) => setResolution(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          <option value="1080p">1080p</option>
          <option value="720p">720p</option>
          <option value="4k">4K</option>
        </select>
      </div>
      <div>
        <label className="text-text-muted text-xs block">Aspect</label>
        <select
          value={aspect}
          onChange={(e) => setAspect(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          <option value="16:9">16:9</option>
          <option value="9:16">9:16</option>
        </select>
      </div>
      <div className="text-text-dim text-xs pb-1">
        {blocked ? "Avatar V unavailable" : cost ? `est. ${formatCost(cost)} API` : "est. after script"}
      </div>
    </>
  );
}

function estimateHeyGenAvatarCost(prompt: string, selectedAvatar: AvatarEntry | undefined, resolution: string) {
  const seconds = estimateSpeechSeconds(prompt);
  if (seconds <= 0) return 0;
  const type = selectedAvatar?.avatar_type || "photo_avatar";
  const is4k = resolution.toLowerCase() === "4k";
  let rate = type === "studio_avatar" || type === "digital_twin" ? 0.0667 : 0.05;
  if (is4k) rate = type === "photo_avatar" ? 0.0667 : 0.0833;
  return seconds * rate;
}

function estimateSpeechSeconds(script: string) {
  const words = script.trim().split(/\s+/).filter(Boolean).length;
  if (words === 0) return 0;
  return Math.max(5, words / 2.5);
}

function SafeModeToggle({
  value,
  onChange,
}: {
  value: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label
      className="flex items-center gap-1.5 text-xs text-text-muted cursor-pointer select-none"
      title="When on, Venice blurs adult-classified output. Off = pass-through (default)."
    >
      <input
        type="checkbox"
        checked={value}
        onChange={(e) => onChange(e.target.checked)}
        style={{ accentColor: "var(--apteva-accent, #4ade80)" }}
      />
      Safe mode
    </label>
  );
}

function VideoAudioSelect({
  value,
  onChange,
  configurable,
}: {
  value: boolean;
  onChange: (v: boolean) => void;
  configurable?: boolean;
}) {
  return (
    <div>
      <label className="text-text-muted text-xs block">Audio</label>
      <select
        value={value ? "silent" : "default"}
        onChange={(e) => onChange(e.target.value === "silent")}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        title={
          configurable === false
            ? "This model does not advertise configurable audio; silent output may already be its default."
            : "No sound sends audio=false to Venice video models that support generated audio."
        }
      >
        <option value="default">Default</option>
        <option value="silent">No sound</option>
      </select>
    </div>
  );
}

function EditOptions({
  model,
  setModel,
  format,
  setFormat,
  maxSources,
  liveModels,
  liveProvider,
}: {
  model: EditModel;
  setModel: (v: EditModel) => void;
  format: string;
  setFormat: (v: string) => void;
  maxSources: number;
  liveModels: LiveModel[];
  liveProvider: string;
}) {
  const modelOptions = liveModels.length > 0 ? liveModels : EDIT_MODELS.map((id) => ({ id, label: id }));
  return (
    <>
      <div>
        <label className="text-text-muted text-xs block">
          Edit model
          {liveModels.length > 0 && (
            <span className="text-text-dim ml-1" style={{ fontSize: 10 }}>
              · {liveProvider} ({liveModels.length})
            </span>
          )}
        </label>
        <select
          value={model}
          onChange={(e) => setModel(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          {modelOptions.map((m) => (
            <option key={m.id} value={m.id}>
              {m.label}
            </option>
          ))}
        </select>
        <div className="text-text-dim mt-0.5" style={{ fontSize: 10 }}>
          max {maxSources} reference{maxSources === 1 ? "" : "s"}
        </div>
      </div>
      <div>
        <label className="text-text-muted text-xs block">Format</label>
        <select
          value={format}
          onChange={(e) => setFormat(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          <option value="png">PNG</option>
          <option value="jpeg">JPEG</option>
          <option value="webp">WebP</option>
        </select>
      </div>
    </>
  );
}

// ReferenceImageInput — accepts uploads (file → base64), URL paste, or
// "storage:N" handles (set from DetailAside's "Use as reference"). When
// non-empty, image generation flips to edit mode (image.edit capability).
function ReferenceImageInput({
  sources,
  maxSources,
  onAdd,
  onRemove,
  onClear,
  hint,
}: {
  sources: SourceImageRef[];
  maxSources: number;
  onAdd: (value: string, label: string) => void;
  onRemove: (index: number) => void;
  onClear: () => void;
  hint?: string;
}) {
  const [urlInput, setUrlInput] = useState("");
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const atLimit = sources.length >= maxSources;

  const handleFile = (file: File) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result || "");
      // FileReader.readAsDataURL gives us "data:image/png;base64,..."; strip the prefix.
      const b64 = result.includes(",") ? result.split(",", 2)[1] : result;
      onAdd(b64, `Upload (${file.name})`);
    };
    reader.readAsDataURL(file);
  };

  const handleDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    const files = Array.from(e.dataTransfer.files || [])
      .filter((file) => file.type.startsWith("image/"))
      .slice(0, Math.max(0, maxSources - sources.length));
    files.forEach(handleFile);
  };

  return (
    <div
      onDrop={handleDrop}
      onDragOver={(e) => e.preventDefault()}
      className={
        "flex flex-col gap-2 p-2 rounded bg-bg-card " +
        (sources.length > 0 ? "border border-accent" : "border border-dashed border-border")
      }
    >
      {sources.length > 0 && (
        <div className="flex gap-2 overflow-x-auto pb-1">
          {sources.map((src, index) => {
            const previewSrc = sourceImagePreviewSrc(src.value);
            return (
              <div
                key={`${src.value}-${index}`}
                className="flex items-center gap-2 border border-border rounded p-1.5 bg-bg"
                style={{ minWidth: 180, maxWidth: 240 }}
              >
                {previewSrc ? (
                  <img
                    src={previewSrc}
                    alt=""
                    style={{ width: 44, height: 44, objectFit: "cover", borderRadius: 4, flexShrink: 0 }}
                  />
                ) : (
                  <div
                    style={{ width: 44, height: 44, borderRadius: 4, background: "var(--apteva-bg-input, #222)", flexShrink: 0 }}
                    className="flex items-center justify-center text-text-dim text-xs"
                  >
                    ref
                  </div>
                )}
                <div className="min-w-0 flex-1">
                  <div className="text-text-dim" style={{ fontSize: 10 }}>
                    Reference {index + 1}
                  </div>
                  <div className="text-xs text-text truncate" title={src.label}>
                    {src.label || "(set)"}
                  </div>
                </div>
                <button
                  onClick={() => onRemove(index)}
                  className="text-text-muted hover:text-text text-xs px-1.5 py-0.5 border border-border rounded"
                  title="Remove reference"
                >
                  x
                </button>
              </div>
            );
          })}
        </div>
      )}
      <div className="flex items-center gap-3 flex-wrap">
        <span className="text-text-muted text-xs">
          {hint || "Reference images"}
          <span className="text-text-dim"> · {sources.length}/{maxSources}</span>
        </span>
        <button
          disabled={atLimit}
          onClick={() => fileInputRef.current?.click()}
          className="text-xs px-2 py-1 border border-border rounded text-text hover:border-accent disabled:opacity-50"
        >
          Upload
        </button>
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          onChange={(e) => {
            const files = Array.from(e.target.files || []).slice(0, Math.max(0, maxSources - sources.length));
            files.forEach(handleFile);
            e.target.value = "";
          }}
          style={{ display: "none" }}
        />
        <span className="text-text-dim text-xs">or paste URL:</span>
        <input
          type="text"
          value={urlInput}
          disabled={atLimit}
          onChange={(e) => setUrlInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && urlInput.trim() && !atLimit) {
              const trimmed = urlInput.trim();
              onAdd(trimmed, trimmed.length > 40 ? trimmed.slice(0, 37) + "..." : trimmed);
              setUrlInput("");
            }
          }}
          placeholder={atLimit ? "reference limit reached" : "https://..."}
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
          style={{ minWidth: 180 }}
        />
        {sources.length > 0 && (
          <button
            onClick={onClear}
            className="text-text-muted hover:text-text text-xs px-2 py-1 border border-border rounded"
          >
            Clear
          </button>
        )}
        <span className="text-text-dim text-xs">pick from history with Use as reference</span>
      </div>
    </div>
  );
}

function ImageOptions({
  model,
  setModel,
  size,
  setSize,
  imageResolution,
  setImageResolution,
  quality,
  setQuality,
  format,
  setFormat,
  liveModels,
  liveProvider,
  currentModel,
  aspect,
  setAspect,
}: {
  model: ImageModel;
  setModel: (v: ImageModel) => void;
  size: string;
  setSize: (v: string) => void;
  imageResolution: string;
  setImageResolution: (v: string) => void;
  quality: string;
  setQuality: (v: string) => void;
  format: string;
  setFormat: (v: string) => void;
  liveModels: LiveModel[] | null;
  liveProvider: string;
  currentModel?: LiveModel;
  aspect: string;
  setAspect: (v: string) => void;
}) {
  // Live list wins when present. Otherwise fall back to the
  // OpenAI-flavour hardcoded matrix (so dropdown is never empty).
  const usingLive = liveModels && liveModels.length > 0;
  const pixelSizes = currentModel?.pixel_sizes && currentModel.pixel_sizes.length > 0
    ? currentModel.pixel_sizes
    : usingLive
      ? []
      : IMAGE_SIZES[model] || ["1024x1024"];
  const showPixelSize = !usingLive || !currentModel?.aspect_ratios?.length || !!currentModel?.pixel_sizes?.length;
  const showAspect = !!currentModel?.aspect_ratios?.length;
  const showResolution = !!currentModel?.resolutions?.length;
  const showOpenAIQuality = !usingLive || liveProvider === "openai-api";
  const showFormat = !usingLive || liveProvider === "openai-api" || liveProvider === "venice-ai";
  return (
    <>
      <div>
        <label className="text-text-muted text-xs block">
          Model
          {usingLive && (
            <span className="text-text-dim ml-1" style={{ fontSize: 10 }}>
              · {liveProvider} ({liveModels!.length})
            </span>
          )}
        </label>
        <select
          value={model}
          onChange={(e) => setModel(e.target.value as ImageModel)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          {usingLive
            ? liveModels!.map((m) => (
                <option key={m.id} value={m.id}>
                  {modelLabel(m)}
                </option>
              ))
            : IMAGE_MODELS.map((m) => (
                <option key={m} value={m}>
                  {IMAGE_MODEL_LABELS[m]}
                </option>
              ))}
        </select>
      </div>
      {showPixelSize && (
        pixelSizes.length > 0 ? (
          <div>
            <label className="text-text-muted text-xs block">Size</label>
            <select
              value={size}
              onChange={(e) => setSize(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            >
              {pixelSizes.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </div>
        ) : (
          <TextField label="Size" value={size} onChange={setSize} placeholder="720x1280" />
        )
      )}
      {showAspect && (
        <ConstrainedAspect
          aspects={currentModel?.aspect_ratios}
          value={aspect}
          onChange={setAspect}
        />
      )}
      {showResolution && (
        <ConstrainedResolution
          resolutions={currentModel?.resolutions}
          value={imageResolution}
          onChange={setImageResolution}
        />
      )}
      {showOpenAIQuality && model !== "dall-e-2" && (
        <div>
          <label className="text-text-muted text-xs block">Quality</label>
          <select
            value={quality}
            onChange={(e) => setQuality(e.target.value)}
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          >
            {(isGptImage(model) ? GPT_IMAGE_QUALITIES : DALLE3_QUALITIES).map((q) => (
              <option key={q} value={q}>
                {q}
              </option>
            ))}
          </select>
        </div>
      )}
      {showFormat && (
        <div>
          <label className="text-text-muted text-xs block">Format</label>
          <select
            value={format}
            onChange={(e) => setFormat(e.target.value)}
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          >
            <option value="png">PNG</option>
            <option value="jpeg">JPEG</option>
            <option value="webp">WebP</option>
          </select>
        </div>
      )}
    </>
  );
}

function MediaModelPicker({
  model,
  setModel,
  liveModels,
  liveProvider,
}: {
  model: string;
  setModel: (v: string) => void;
  liveModels: LiveModel[] | null;
  liveProvider: string;
}) {
  const models = liveModels || [];
  if (models.length === 0) {
    return (
      <div>
        <label className="text-text-muted text-xs block">Model</label>
        <div className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim" style={{ minWidth: 200 }}>
          {liveProvider ? `loading ${liveProvider}…` : "no provider bound"}
        </div>
      </div>
    );
  }
  return (
    <div>
      <label className="text-text-muted text-xs block">
        Model
        <span className="text-text-dim ml-1" style={{ fontSize: 10 }}>
          · {liveProvider} ({models.length})
        </span>
      </label>
      <select
        value={model}
        onChange={(e) => setModel(e.target.value)}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        style={{ maxWidth: 280 }}
      >
        {models.map((m) => {
          const tag = m.model_type === "image-to-video" ? " · img→vid" : "";
          const price = formatCost(m.price_usd || 0);
          const suffix = [tag, price ? ` ${price}` : ""].filter(Boolean).join("");
          return (
            <option key={m.id} value={m.id}>
              {m.id}
              {suffix}
            </option>
          );
        })}
      </select>
    </div>
  );
}

function VoiceSelect({
  voice,
  setVoice,
  voices,
}: {
  voice: string;
  setVoice: (v: string) => void;
  voices: VoiceEntry[];
}) {
  return (
    <div>
      <label className="text-text-muted text-xs block">Voice</label>
      <select
        value={voice}
        onChange={(e) => setVoice(e.target.value)}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        style={{ maxWidth: 260 }}
      >
        {voices.map((v) => (
          <option key={v.id} value={v.id}>
            {v.name || v.id}
            {v.language ? ` · ${v.language}` : ""}
            {v.gender ? ` · ${v.gender}` : ""}
          </option>
        ))}
      </select>
    </div>
  );
}

function ConstrainedDuration({
  durations,
  value,
  onChange,
}: {
  durations: string[] | undefined;
  value: number;
  onChange: (v: number) => void;
}) {
  if (!durations || durations.length === 0) {
    return (
      <NumberField label="Duration (s)" value={value} onChange={onChange} min={1} max={60} />
    );
  }
  return (
    <div>
      <label className="text-text-muted text-xs block">Duration</label>
      <select
        value={`${value}s`}
        onChange={(e) => {
          const n = parseInt(e.target.value.replace(/[^\d]/g, ""), 10);
          if (!isNaN(n)) onChange(n);
        }}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      >
        {durations.map((d) => (
          <option key={d} value={d}>
            {d}
          </option>
        ))}
      </select>
    </div>
  );
}

function ConstrainedAspect({
  aspects,
  value,
  onChange,
  disabledHint,
}: {
  aspects: string[] | undefined;
  value: string;
  onChange: (v: string) => void;
  disabledHint?: string;
}) {
  if (disabledHint) {
    return (
      <div>
        <label className="text-text-muted text-xs block">Aspect</label>
        <div
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text-dim"
          style={{ minWidth: 160 }}
          title={disabledHint}
        >
          {disabledHint}
        </div>
      </div>
    );
  }
  if (!aspects || aspects.length === 0) {
    return <TextField label="Aspect" value={value} onChange={onChange} />;
  }
  return (
    <div>
      <label className="text-text-muted text-xs block">Aspect</label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      >
        {aspects.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
      </select>
    </div>
  );
}

function ConstrainedResolution({
  resolutions,
  value,
  onChange,
}: {
  resolutions: string[] | undefined;
  value: string;
  onChange: (v: string) => void;
}) {
  if (!resolutions || resolutions.length === 0) {
    return <TextField label="Resolution" value={value} onChange={onChange} />;
  }
  return (
    <div>
      <label className="text-text-muted text-xs block">Resolution</label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
      >
        {resolutions.map((r) => (
          <option key={r} value={r}>
            {r}
          </option>
        ))}
      </select>
    </div>
  );
}

function NumberField({
  label,
  value,
  onChange,
  min,
  max,
}: {
  label: string;
  value: number;
  onChange: (v: number) => void;
  min?: number;
  max?: number;
}) {
  return (
    <div>
      <label className="text-text-muted text-xs block">{label}</label>
      <input
        type="number"
        value={value}
        min={min}
        max={max}
        onChange={(e) => onChange(Number(e.target.value) || 0)}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        style={{ width: 96 }}
      />
    </div>
  );
}

function TextField({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  return (
    <div>
      <label className="text-text-muted text-xs block">{label}</label>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        style={{ width: 140 }}
      />
    </div>
  );
}

function StorageFolderField({
  value,
  onChange,
  placeholder,
  suggestions,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  suggestions: string[];
}) {
  return (
    <div>
      <label className="text-text-muted text-xs block">Output folder</label>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        list="media-storage-folder-suggestions"
        className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        style={{ width: 220 }}
        title="Storage folder for generated output. Leave empty for the default."
      />
      <datalist id="media-storage-folder-suggestions">
        {suggestions.map((folder) => (
          <option key={folder} value={folder} />
        ))}
      </datalist>
    </div>
  );
}

function defaultOutputFolder(kind: Kind): string {
  if (kind === "image") return "/.generated/images/";
  if (kind === "video") return "/.generated/videos/";
  if (kind === "music") return "/.generated/music/";
  if (kind === "avatar") return "/.generated/avatars/";
  return "/.generated/audio/";
}

function storageFoldersFromFiles(files: { folder?: string }[]): string[] {
  const set = new Set<string>();
  for (const file of files || []) {
    const folder = normalizeFolderInput(file.folder || "");
    if (folder && !folderHasDotSegment(folder)) set.add(folder);
  }
  return Array.from(set).sort().slice(0, 40);
}

function normalizeFolderInput(raw: string): string {
  const s = String(raw || "").trim();
  if (!s) return "";
  const prefixed = s.startsWith("/") ? s : `/${s}`;
  return prefixed.endsWith("/") ? prefixed : `${prefixed}/`;
}

function folderHasDotSegment(folder: string): boolean {
  return folder.split("/").some((part) => part.startsWith("."));
}

function Gallery({
  kind,
  items,
  onSelect,
  onOpenLightbox,
  onGenerateDraft,
  generating,
  generatingPrompt,
  generatingModel,
  generatingCostUSD,
  pendingJobs,
  generatingDraftId,
}: {
  kind: Kind;
  items: Generation[];
  onSelect: (g: Generation) => void;
  onOpenLightbox: (g: Generation) => void;
  onGenerateDraft: (g: Generation) => void;
  generating: boolean;
  generatingPrompt: string;
  generatingModel: string;
  generatingCostUSD: number;
  pendingJobs: VideoJob[];
  generatingDraftId: number | null;
}) {
  if (kind === "image") {
    return (
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
          gap: 8,
          padding: 8,
          alignItems: "start",
          alignContent: "start",
        }}
      >
        {generating && (
          <GeneratingCard prompt={generatingPrompt} model={generatingModel} costUSD={generatingCostUSD} />
        )}
        {items.map((g) => {
          const src = imageSrc(g);
          const draft = g.status === "draft";
          return (
            <div
              key={g.id}
              className="border border-border rounded overflow-hidden hover:border-accent transition-colors"
            >
              {draft ? (
                <DraftPreview generation={g} busy={generatingDraftId === g.id} onGenerate={onGenerateDraft} />
              ) : src ? (
                <button
                  onClick={() => onOpenLightbox(g)}
                  className="block w-full"
                  title="Click to open"
                >
                  <img src={src} alt="" className="w-full" loading="lazy" style={{ display: "block" }} />
                </button>
              ) : (
                <div className="bg-bg-input py-12 text-center text-text-muted text-xs">no preview</div>
              )}
              <button
                onClick={() => onSelect(g)}
                className="block w-full text-left"
                title="Show details"
              >
                <CardMeta g={g} />
              </button>
            </div>
          );
        })}
      </div>
    );
  }
  // Video, audio, music: responsive grid of media-card players.
  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: kind === "video" || kind === "avatar"
          ? "repeat(auto-fill, minmax(360px, 1fr))"
          : "repeat(auto-fill, minmax(280px, 1fr))",
        gap: 8,
        padding: 8,
        alignItems: "start",
        alignContent: "start",
      }}
    >
      {generating && (
        <GeneratingCard prompt={generatingPrompt} model={generatingModel} costUSD={generatingCostUSD} />
      )}
      {(kind === "video" || kind === "avatar") &&
        pendingJobs.map((job) => (
          <GeneratingCard
            key={`job-${job.id}`}
            prompt={job.prompt}
            model={job.model ? `#${job.id} · ${job.model}` : `#${job.id}`}
            costUSD={job.cost_usd || 0}
          />
        ))}
      {(() => {
        const pendingGenerationIds = new Set(
          pendingJobs
            .map((job) => job.generation_id || 0)
            .filter((id) => id > 0),
        );
        return items.map((g) => {
          const url = g.storage_urls?.[0] || g.local_cache_url || g.upstream_urls?.[0] || "";
          const asyncPending =
            (kind === "video" || kind === "avatar") &&
            (g.status === "queued" || g.status === "generating") &&
            !url;
          if (asyncPending && pendingGenerationIds.has(g.id)) return null;
          if (asyncPending) {
            return (
              <GeneratingCard
                key={g.id}
                prompt={g.prompt}
                model={g.model ? `#${g.id} · ${g.model}` : `#${g.id}`}
                costUSD={g.cost_usd || 0}
              />
            );
          }
          const draft = g.status === "draft";
          return (
            <div
              key={g.id}
              className="border border-border rounded overflow-hidden bg-bg-card"
              onClick={() => onSelect(g)}
            >
              {draft ? (
                <DraftPreview generation={g} busy={generatingDraftId === g.id} onGenerate={onGenerateDraft} />
              ) : url ? (
                kind === "video" || kind === "avatar" ? (
                  <video controls src={url} className="w-full" />
                ) : (
                  <audio controls src={url} className="w-full" />
                )
              ) : (
                <div className="bg-bg-input py-6 text-center text-text-muted text-xs">no source</div>
              )}
              <CardMeta g={g} />
            </div>
          );
        });
      })()}
    </div>
  );
}

function DraftPreview({
  generation,
  busy,
  onGenerate,
}: {
  generation: Generation;
  busy: boolean;
  onGenerate: (g: Generation) => void;
}) {
  const cost = formatCost(generation.cost_usd);
  return (
    <div className="bg-bg-input flex flex-col items-center justify-center text-center px-4 py-8" style={{ minHeight: 160 }}>
      <div className="text-xs uppercase text-text-dim">Draft</div>
      <div className="mt-2 text-sm text-text" style={{ maxWidth: 280 }}>
        {generation.prompt.length > 90 ? generation.prompt.slice(0, 87) + "…" : generation.prompt}
      </div>
      {cost && <div className="mt-2 text-accent" style={{ fontSize: 10 }}>est. {cost}</div>}
      <button
        type="button"
        disabled={busy}
        onClick={(e) => {
          e.stopPropagation();
          onGenerate(generation);
        }}
        className="mt-4 px-3 py-1.5 text-xs bg-accent text-bg rounded font-bold disabled:opacity-50"
      >
        {busy ? "Generating…" : "Generate"}
      </button>
    </div>
  );
}

function GeneratingCard({ prompt, model, costUSD = 0 }: { prompt: string; model: string; costUSD?: number }) {
  const cost = formatCost(costUSD);
  return (
    <div
      className="border border-accent rounded overflow-hidden bg-bg-card flex flex-col items-center justify-center"
      style={{ minHeight: 220, alignSelf: "start" }}
    >
      <Spinner />
      <div className="mt-3 text-sm text-text">Generating…</div>
      {prompt && (
        <div className="mt-1 px-3 text-xs text-text-muted text-center" style={{ maxWidth: 260 }} title={prompt}>
          {prompt.length > 80 ? prompt.slice(0, 77) + "…" : prompt}
        </div>
      )}
      {model && <div className="mt-1 text-text-dim" style={{ fontSize: 10 }}>{model}</div>}
      {cost && <div className="mt-1 text-accent" style={{ fontSize: 10 }}>est. {cost}</div>}
    </div>
  );
}

function Spinner() {
  return (
    <svg width="28" height="28" viewBox="0 0 24 24">
      <circle
        cx="12"
        cy="12"
        r="9"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeDasharray="44"
        strokeDashoffset="22"
        style={{ animation: "ms-spin 0.9s linear infinite" }}
      />
      <style>{`@keyframes ms-spin { to { transform: rotate(360deg); transform-origin: 12px 12px; } }`}</style>
    </svg>
  );
}

function CardMeta({ g }: { g: Generation }) {
  const cost = formatCost(g.cost_usd);
  const status = g.status && g.status !== "ready" ? g.status : "";
  return (
    <div className="p-2">
      <div className="text-text text-xs truncate flex items-center gap-1.5">
        {status && <span className="text-[10px] px-1.5 py-0.5 rounded bg-border text-text-muted uppercase">{status}</span>}
        <span className="truncate">{g.prompt}</span>
      </div>
      <div className="text-text-dim mt-0.5 flex items-center gap-1.5" style={{ fontSize: 10 }}>
        <span>{g.provider}</span>
        <span>·</span>
        <span>{g.model || g.size || "—"}</span>
        <span>·</span>
        <span>{new Date(g.created_at).toLocaleString()}</span>
        {cost && (
          <>
            <span>·</span>
            <span className="text-accent">{cost}</span>
          </>
        )}
      </div>
    </div>
  );
}

function DetailAside({
  selected,
  onClose,
  deleting,
  onDelete,
  onUseAsReference,
}: {
  selected: Generation;
  onClose: () => void;
  deleting: boolean;
  onDelete: () => void;
  onUseAsReference?: () => void;
}) {
  const url = selected.storage_urls?.[0] || selected.upstream_urls?.[0] || "";
  return (
    <aside
      className="border-l border-border bg-bg-card flex flex-col"
      style={{ width: 384 }}
    >
      <header className="flex items-center gap-2 px-4 py-3 border-b border-border">
        <span className="text-text font-medium truncate flex-1">{selected.prompt}</span>
        {onUseAsReference && (
          <button
            onClick={onUseAsReference}
            className="text-xs px-2 py-1 border border-border rounded text-accent hover:border-accent"
            title="Use this image as the reference for an edit"
          >
            Use as reference
          </button>
        )}
        <button
          onClick={onDelete}
          disabled={deleting}
          className="text-xs px-2 py-1 border border-border rounded text-text-muted hover:text-text hover:border-accent disabled:opacity-50"
          title="Delete this generation and linked Storage files"
        >
          {deleting ? "Deleting…" : "Delete"}
        </button>
        <button
          onClick={onClose}
          className="text-text-muted hover:text-text leading-none px-1"
          style={{ fontSize: 18 }}
        >
          ×
        </button>
      </header>
      <div className="flex-1 overflow-auto">
        {url && selected.kind === "image" && <img src={url} alt="" className="w-full" />}
        {url && (selected.kind === "video" || selected.kind === "avatar") && <video controls src={url} className="w-full" />}
        {url && (selected.kind === "audio_tts" || selected.kind === "audio_sfx" || selected.kind === "music") && (
          <audio controls src={url} className="w-full p-3" />
        )}
        <dl className="px-4 py-3 text-xs flex flex-col gap-2">
          <Row label="Kind" value={selected.kind} />
          {selected.status && <Row label="Status" value={selected.status} />}
          <Row label="Provider" value={selected.provider} />
          <Row label="Model" value={selected.model || "—"} />
          {selected.size && <Row label="Size" value={selected.size} />}
          {selected.duration_ms > 0 && (
            <Row label="Duration" value={`${(selected.duration_ms / 1000).toFixed(1)}s`} />
          )}
          <Row label="Count" value={String(selected.count)} />
          {formatCost(selected.cost_usd) && (
            <Row label="Cost" value={formatCost(selected.cost_usd)} />
          )}
          <Row label="Created" value={new Date(selected.created_at).toLocaleString()} />
          {selected.revised_prompt && <Row label="Revised" value={selected.revised_prompt} />}
          {selected.request_json && <Row label="Request" value={selected.request_json} />}
          {selected.storage_ids.length > 0 && (
            <Row
              label="Storage IDs"
              value={selected.storage_ids.map((id) => `#${id}`).join(", ")}
            />
          )}
        </dl>
        {selected.storage_urls && selected.storage_urls.length > 0 && (
          <div className="px-4 pb-3 flex flex-col gap-1">
            {selected.storage_urls.map((u, i) => (
              <a
                key={i}
                href={u}
                target="_blank"
                rel="noopener"
                className="text-accent text-xs hover:underline"
              >
                Open #{selected.storage_ids[i]} →
              </a>
            ))}
          </div>
        )}
      </div>
    </aside>
  );
}

function VideoJobsBanner({
  jobs,
}: {
  jobs: VideoJob[];
}) {
  const failed = jobs.filter((j) => j.status === "failed");
  if (failed.length === 0) return null;
  return (
    <div className="flex flex-col gap-1 p-2 rounded border border-border bg-bg-card">
      {failed.map((j) => (
        <div key={j.id} className="flex items-start gap-2 text-xs">
          <span className="text-text" style={{ color: "var(--apteva-danger, #ef4444)" }}>
            Failed #{j.id} ({j.model})
          </span>
          <span className="text-text-dim flex-1 truncate" title={j.error}>
            {j.error || "(no detail)"}
          </span>
        </div>
      ))}
    </div>
  );
}

// sourceImagePreviewSrc renders a tiny <img> for the reference state.
// Handles three shapes: storage handle ("storage:N" → platform-proxy
// URL), http(s) URL (pass-through), or base64 (assume PNG, wrap in
// data: URL). Returns "" when none match (caller renders placeholder).
function sourceImagePreviewSrc(value: string): string {
  const v = value.trim();
  if (!v) return "";
  if (v.startsWith("storage:")) {
    const id = v.slice("storage:".length);
    // Same routing the gallery uses for storage_urls.
    return `/api/apps/storage/files/${id}/content`;
  }
  if (v.startsWith("http://") || v.startsWith("https://") || v.startsWith("data:")) {
    return v;
  }
  // Bare base64 — try as PNG; browser will figure out if it's something else.
  return `data:image/png;base64,${v}`;
}

function Lightbox({
  item,
  onClose,
  onUseAsReference,
}: {
  item: Generation;
  onClose: () => void;
  onUseAsReference?: () => void;
}) {
  const url = item.storage_urls?.[0] || item.local_cache_url || item.upstream_urls?.[0] || imageSrc(item);
  // Close on Esc.
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
      style={{
        position: "fixed",
        inset: 0,
        background: "rgba(0,0,0,0.85)",
        zIndex: 9999,
        display: "flex",
        flexDirection: "column",
        alignItems: "center",
        justifyContent: "center",
        padding: 24,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          maxWidth: "100%",
          maxHeight: "100%",
          gap: 12,
        }}
      >
        {url && item.kind === "image" && (
          <img
            src={url}
            alt=""
            style={{ maxWidth: "92vw", maxHeight: "82vh", objectFit: "contain", borderRadius: 4 }}
          />
        )}
        {url && (item.kind === "video" || item.kind === "avatar") && (
          <video controls src={url} style={{ maxWidth: "92vw", maxHeight: "82vh" }} />
        )}
        {url && (item.kind === "audio_tts" || item.kind === "audio_sfx" || item.kind === "music") && (
          <audio controls src={url} style={{ width: 480 }} />
        )}
        <div className="text-text text-sm text-center" style={{ maxWidth: 700 }}>
          {item.prompt}
        </div>
        <div className="text-text-dim" style={{ fontSize: 11 }}>
          {item.provider} · {item.model || item.size || "—"} ·{" "}
          {new Date(item.created_at).toLocaleString()}
          {formatCost(item.cost_usd) && (
            <>
              {" · "}
              <span className="text-accent">{formatCost(item.cost_usd)}</span>
            </>
          )}
        </div>
        <div className="flex items-center gap-2">
          {onUseAsReference && (
            <button
              onClick={onUseAsReference}
              className="text-xs px-3 py-1.5 border border-border rounded text-accent hover:border-accent"
            >
              Use as reference
            </button>
          )}
          {url && (
            <a
              href={url}
              target="_blank"
              rel="noopener"
              className="text-xs px-3 py-1.5 border border-border rounded text-text"
            >
              Open original
            </a>
          )}
          <button
            onClick={onClose}
            className="text-xs px-3 py-1.5 border border-border rounded text-text-muted"
          >
            Close (Esc)
          </button>
        </div>
      </div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <span className="text-text-dim flex-shrink-0" style={{ width: 80 }}>
        {label}
      </span>
      <span className="flex-1 min-w-0 text-text break-all" title={value}>
        {value}
      </span>
    </div>
  );
}
