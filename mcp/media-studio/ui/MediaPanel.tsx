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

type Kind = "image" | "video" | "audio_tts" | "audio_sfx" | "music";

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
  created_at: string;
}

// LiveModel mirrors the sidecar's modelEntry JSON. Constraints arrays
// are empty when the model doesn't pre-enumerate options (e.g.
// pixel-sized image models that accept arbitrary WxH).
interface LiveModel {
  id: string;
  label: string;
  model_type?: string;
  aspect_ratios?: string[];
  default_aspect_ratio?: string;
  resolutions?: string[];
  default_resolution?: string;
  durations?: string[];
  supports_image_to_video?: boolean;
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
  storage: { bound: boolean; app?: string };
}

const API = "/api/apps/media-studio";

const TAB_LABELS: Record<Exclude<Kind, "audio_sfx">, string> = {
  image: "Images",
  video: "Videos",
  audio_tts: "Audio",
  music: "Music",
};

// Image-specific option matrices, lifted from the old StudioPanel.
type ImageModel =
  | "gpt-image-2"
  | "gpt-image-1.5"
  | "gpt-image-1"
  | "gpt-image-1-mini"
  | "dall-e-3"
  | "dall-e-2";

const IMAGE_MODEL_LABELS: Record<ImageModel, string> = {
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
const IMAGE_SIZES: Record<ImageModel, string[]> = {
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
type EditModel = typeof EDIT_MODELS[number];

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
  const [tab, setTab] = useState<"image" | "video" | "audio" | "music">("image");
  const [audioSubKind, setAudioSubKind] = useState<"audio_tts" | "audio_sfx">("audio_tts");
  const activeKind: Kind =
    tab === "audio" ? audioSubKind : (tab as Kind);

  const [items, setItems] = useState<Generation[]>([]);
  const [bindings, setBindings] = useState<BindingsStatus | null>(null);
  const [status, setStatus] = useState("");
  const [generating, setGenerating] = useState(false);
  const [selected, setSelected] = useState<Generation | null>(null);
  const [lightbox, setLightbox] = useState<Generation | null>(null);

  // Per-kind composer state.
  const [prompt, setPrompt] = useState("");
  const [imageModel, setImageModel] = useState<ImageModel>("gpt-image-2");
  const [imageSize, setImageSize] = useState("1024x1024");
  const [imageQuality, setImageQuality] = useState("auto");
  const [imageFormat, setImageFormat] = useState("png");
  const [duration, setDuration] = useState(5); // video/audio/music
  const [aspect, setAspect] = useState("16:9");
  const [voice, setVoice] = useState("");
  // Video model picker — live-loaded from /models?kind=video.
  // Auto-snaps to the first listed model when the dropdown lands.
  const [videoModel, setVideoModel] = useState<string>("");
  // safe_mode (image gen + edit) — Venice's own default is true (blurs
  // adult-classified output); sidecar defaults to false so the API
  // returns whatever the model produced. Panel flag mirrors that.
  const [safeMode, setSafeMode] = useState(false);
  // Reference-image (edit mode) state. When sourceImage is non-empty,
  // media_generate routes through image.edit instead of image.generate.
  const [sourceImage, setSourceImage] = useState("");
  const [sourceImageLabel, setSourceImageLabel] = useState("");
  const [editModel, setEditModel] = useState<EditModel>("firered-image-edit");
  const isEditMode = activeKind === "image" && sourceImage.trim() !== "";
  // Live-loaded model list for the bound provider, refreshed on tab
  // switch and binding change. Falls back to the hardcoded
  // OpenAI-flavour list (IMAGE_MODELS) when the fetch fails so the
  // dropdown is never empty.
  const [liveModels, setLiveModels] = useState<LiveModel[] | null>(null);
  const [liveProvider, setLiveProvider] = useState<string>("");
  // In-flight video jobs (queued / polling). Shown as a small badge
  // above the video gallery so the user knows something is cooking
  // between submit and the eventual media.generated event.
  const [videoJobs, setVideoJobs] = useState<
    { id: number; queue_id: string; model: string; prompt: string; status: string; error: string }[]
  >([]);

  useEffect(() => {
    const allowed = IMAGE_SIZES[imageModel] || ["1024x1024"];
    if (!allowed.includes(imageSize)) setImageSize(allowed[0]);
    if (isGptImage(imageModel)) {
      if (!GPT_IMAGE_QUALITIES.includes(imageQuality)) setImageQuality("auto");
    } else if (imageModel === "dall-e-3") {
      if (!DALLE3_QUALITIES.includes(imageQuality)) setImageQuality("standard");
    }
  }, [imageModel, imageSize, imageQuality]);

  const loadBindings = useCallback(async () => {
    try {
      const res = await fetch(`${API}/bindings`, { credentials: "same-origin" });
      if (!res.ok) return;
      const data = (await res.json()) as BindingsStatus;
      setBindings(data);
    } catch {}
  }, []);

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

  useEffect(() => {
    loadBindings();
  }, [loadBindings]);
  useEffect(() => {
    loadGenerations();
  }, [loadGenerations]);

  // Poll in-flight video jobs every 5s while the Videos tab is active.
  // 5s is finer than the sidecar's 15s worker tick so the user sees the
  // failed→cleared transition promptly when the worker gives up. When
  // a job transitions queued|polling → complete we also force a
  // gallery refresh — belt-and-suspenders for the rare case where the
  // media.generated event was dropped or missed by the EventSource.
  useEffect(() => {
    if (activeKind !== "video") return;
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

  // Live-load the model list whenever the active kind or the bound
  // provider for that kind changes. The sidecar caches per-(provider,
  // kind) for 10 min so this is cheap on tab switches.
  useEffect(() => {
    const currentBoundSlug = bindings?.[activeKind]?.slug || "";
    if (!currentBoundSlug) {
      setLiveModels(null);
      setLiveProvider("");
      return;
    }
    let cancelled = false;
    fetch(`${API}/models?kind=${activeKind}`, { credentials: "same-origin" })
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
            }
          }
        }
      })
      .catch(() => {
        if (!cancelled) setLiveModels(null);
      });
    return () => {
      cancelled = true;
    };
  }, [activeKind, bindings]);

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
        : "";
  const currentModel: LiveModel | undefined =
    liveModels?.find((m) => m.id === currentModelId);
  // Video reference-image is allowed for both standard (text-to-video)
  // and image-to-video models — required for the latter, optional
  // hint for the former (Venice's queue accepts image_url on most).
  const showVideoRefInput =
    activeKind === "video" && !!currentModel?.supports_image_to_video;

  // Auto-snap aspect / duration / resolution when the user picks a
  // different model whose constraint set doesn't include the current
  // value. Defaults to the model's default* field when set, else the
  // first listed option.
  useEffect(() => {
    if (!currentModel) return;
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
  }, [currentModelId]); // eslint-disable-line react-hooks/exhaustive-deps

  const generate = async () => {
    if (!prompt.trim() || generating) return;
    setGenerating(true);
    setStatus("Generating…");
    try {
      const body: Record<string, unknown> = {
        kind: activeKind,
        prompt,
      };
      if (activeKind === "image") {
        if (isEditMode) {
          body.model = editModel;
          body.source_image = sourceImage;
          body.options = { output_format: imageFormat, safe_mode: safeMode };
        } else {
          body.model = imageModel;
          body.size = imageSize;
          const options: Record<string, unknown> = { safe_mode: safeMode };
          if (imageModel !== "dall-e-2") options.quality = imageQuality;
          if (isGptImage(imageModel)) options.output_format = imageFormat;
          body.options = options;
        }
      } else if (activeKind === "video") {
        if (videoModel) body.model = videoModel;
        body.duration = duration;
        body.aspect = aspect;
        // Image-to-video: pass the reference image through the same
        // source_image arg the dispatcher uses for image.edit.
        if (showVideoRefInput && sourceImage) {
          body.source_image = sourceImage;
        }
      } else if (activeKind === "audio_tts") {
        if (voice) body.voice = voice;
      } else if (activeKind === "audio_sfx" || activeKind === "music") {
        body.duration = duration;
      }
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
        _meta?: { status?: string; job_id?: number; cost_usd?: number };
      })._meta;
      if (meta?.status === "queued") {
        setPrompt("");
        const costStr = meta.cost_usd ? ` · est. ${formatCost(meta.cost_usd)}` : "";
        setStatus(`Queued — job #${meta.job_id}${costStr}, polling for completion…`);
        return;
      }
      setPrompt("");
      setStatus("Done.");
      loadGenerations();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setGenerating(false);
    }
  };

  return (
    <div className="h-full flex flex-col">
      {/* Kind tabs */}
      <nav className="flex items-center border-b border-border px-4">
        {(Object.keys(TAB_LABELS) as Array<"image" | "video" | "audio_tts" | "music">).map((k) => {
          const t: "image" | "video" | "audio" | "music" =
            k === "audio_tts" ? "audio" : (k as "image" | "video" | "music");
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
              sourceImage={sourceImage}
              sourceImageLabel={sourceImageLabel}
              onSet={(value, label) => {
                setSourceImage(value);
                setSourceImageLabel(label);
              }}
              onClear={() => {
                setSourceImage("");
                setSourceImageLabel("");
              }}
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
            generating={generating}
            disabled={!isBound}
            isEditMode={isEditMode}
            liveModels={liveModels}
            liveProvider={liveProvider}
            imageModel={imageModel}
            setImageModel={setImageModel}
            imageSize={imageSize}
            setImageSize={setImageSize}
            imageQuality={imageQuality}
            setImageQuality={setImageQuality}
            imageFormat={imageFormat}
            setImageFormat={setImageFormat}
            editModel={editModel}
            setEditModel={setEditModel}
            videoModel={videoModel}
            setVideoModel={setVideoModel}
            currentModel={currentModel}
            safeMode={safeMode}
            setSafeMode={setSafeMode}
            duration={duration}
            setDuration={setDuration}
            aspect={aspect}
            setAspect={setAspect}
            voice={voice}
            setVoice={setVoice}
          />

          {activeKind === "video" && videoJobs.length > 0 && (
            <VideoJobsBanner jobs={videoJobs} />
          )}

          <div className="flex-1 overflow-auto border border-border rounded">
            {items.length === 0 && !generating ? (
              <div className="py-12 px-6 text-center text-text-muted text-sm">
                {status || "No generations yet for this kind."}
              </div>
            ) : (
              <Gallery
                kind={activeKind}
                items={items}
                onSelect={setSelected}
                onOpenLightbox={setLightbox}
                generating={generating}
                generatingPrompt={prompt}
                generatingModel={isEditMode ? editModel : imageModel}
              />
            )}
          </div>
          <div className="text-xs text-text-dim">{status}</div>
        </div>

        {selected && (
          <DetailAside
            selected={selected}
            onClose={() => setSelected(null)}
            onUseAsReference={
              selected.kind === "image" && selected.storage_ids.length > 0
                ? () => {
                    const id = selected.storage_ids[0];
                    setSourceImage(`storage:${id}`);
                    setSourceImageLabel(`Storage #${id}`);
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
                  setSourceImage(`storage:${id}`);
                  setSourceImageLabel(`Storage #${id}`);
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
  return <IconAudio />;
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
  generating: boolean;
  disabled: boolean;
  isEditMode: boolean;
  liveModels: { id: string; label: string }[] | null;
  liveProvider: string;
  imageModel: ImageModel;
  setImageModel: (v: ImageModel) => void;
  imageSize: string;
  setImageSize: (v: string) => void;
  imageQuality: string;
  setImageQuality: (v: string) => void;
  imageFormat: string;
  setImageFormat: (v: string) => void;
  editModel: EditModel;
  setEditModel: (v: EditModel) => void;
  videoModel: string;
  setVideoModel: (v: string) => void;
  currentModel?: LiveModel;
  safeMode: boolean;
  setSafeMode: (v: boolean) => void;
  duration: number;
  setDuration: (v: number) => void;
  aspect: string;
  setAspect: (v: string) => void;
  voice: string;
  setVoice: (v: string) => void;
}

function Composer(p: ComposerProps) {
  const promptPlaceholder = p.isEditMode
    ? "Edit instruction — 'remove the tree', 'change sky to sunset'"
    : p.kind === "audio_tts"
      ? "Text to speak"
      : p.kind === "music"
        ? "A jazzy lo-fi loop with piano"
        : p.kind === "video"
          ? "A cat walking through a sunlit garden"
          : p.kind === "audio_sfx"
            ? "A door creaking open"
            : "a cat in a hat";
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
        />
      )}
      {p.kind === "image" && !p.isEditMode && (
        <ImageOptions
          model={p.imageModel}
          setModel={p.setImageModel}
          size={p.imageSize}
          setSize={p.setImageSize}
          quality={p.imageQuality}
          setQuality={p.setImageQuality}
          format={p.imageFormat}
          setFormat={p.setImageFormat}
          liveModels={p.liveModels}
          liveProvider={p.liveProvider}
        />
      )}
      {p.kind === "video" && (
        <>
          <VideoModelPicker
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
        </>
      )}
      {p.kind === "audio_tts" && (
        <TextField label="Voice" value={p.voice} onChange={p.setVoice} placeholder="default" />
      )}
      {(p.kind === "audio_sfx" || p.kind === "music") && (
        <NumberField label="Duration (s)" value={p.duration} onChange={p.setDuration} min={1} max={300} />
      )}
      {p.kind === "image" && (
        <SafeModeToggle value={p.safeMode} onChange={p.setSafeMode} />
      )}
      <button
        onClick={p.generate}
        disabled={!p.prompt.trim() || p.generating || p.disabled}
        className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
      >
        {p.generating ? "…" : p.isEditMode ? "Edit" : "Generate"}
      </button>
    </div>
  );
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

function EditOptions({
  model,
  setModel,
  format,
  setFormat,
}: {
  model: EditModel;
  setModel: (v: EditModel) => void;
  format: string;
  setFormat: (v: string) => void;
}) {
  return (
    <>
      <div>
        <label className="text-text-muted text-xs block">Edit model</label>
        <select
          value={model}
          onChange={(e) => setModel(e.target.value as EditModel)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          {EDIT_MODELS.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </select>
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

// ReferenceImageInput — accepts upload (file → base64), URL paste, or
// a "storage:N" handle (set from DetailAside's "Use as reference"). When
// non-empty, the composer flips to edit mode (image.edit capability).
function ReferenceImageInput({
  sourceImage,
  sourceImageLabel,
  onSet,
  onClear,
  hint,
}: {
  sourceImage: string;
  sourceImageLabel: string;
  onSet: (value: string, label: string) => void;
  onClear: () => void;
  hint?: string;
}) {
  const [urlInput, setUrlInput] = useState("");
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const handleFile = (file: File) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = String(reader.result || "");
      // FileReader.readAsDataURL gives us "data:image/png;base64,..."; strip the prefix.
      const b64 = result.includes(",") ? result.split(",", 2)[1] : result;
      onSet(b64, `Upload (${file.name})`);
    };
    reader.readAsDataURL(file);
  };

  const handleDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    const file = e.dataTransfer.files?.[0];
    if (file && file.type.startsWith("image/")) handleFile(file);
  };

  if (sourceImage) {
    // Compute a preview src from whatever shape sourceImage takes.
    const previewSrc = sourceImagePreviewSrc(sourceImage);
    return (
      <div className="flex items-center gap-3 p-2 rounded border border-accent bg-bg-card">
        {previewSrc ? (
          <img
            src={previewSrc}
            alt=""
            style={{
              width: 56,
              height: 56,
              objectFit: "cover",
              borderRadius: 4,
              flexShrink: 0,
            }}
          />
        ) : (
          <div
            style={{
              width: 56,
              height: 56,
              borderRadius: 4,
              background: "var(--apteva-bg-input, #222)",
              flexShrink: 0,
            }}
            className="flex items-center justify-center text-text-dim text-xs"
          >
            ref
          </div>
        )}
        <div className="flex-1 min-w-0">
          <div className="text-xs text-text-muted">Reference image</div>
          <div className="text-sm text-text font-medium truncate" title={sourceImageLabel}>
            {sourceImageLabel || "(set)"}
          </div>
        </div>
        <button
          onClick={onClear}
          className="text-text-muted hover:text-text text-sm px-2 py-0.5 border border-border rounded"
        >
          Clear
        </button>
      </div>
    );
  }

  return (
    <div
      onDrop={handleDrop}
      onDragOver={(e) => e.preventDefault()}
      className="flex items-center gap-3 p-2 rounded border border-dashed border-border bg-bg-card"
    >
      <span className="text-text-muted text-xs">
        {hint || "Reference image (optional):"}
      </span>
      <button
        onClick={() => fileInputRef.current?.click()}
        className="text-xs px-2 py-1 border border-border rounded text-text hover:border-accent"
      >
        Upload
      </button>
      <input
        ref={fileInputRef}
        type="file"
        accept="image/*"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) handleFile(file);
          e.target.value = "";
        }}
        style={{ display: "none" }}
      />
      <span className="text-text-dim text-xs">or paste URL:</span>
      <input
        type="text"
        value={urlInput}
        onChange={(e) => setUrlInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && urlInput.trim()) {
            const trimmed = urlInput.trim();
            onSet(trimmed, trimmed.length > 40 ? trimmed.slice(0, 37) + "…" : trimmed);
            setUrlInput("");
          }
        }}
        placeholder="https://…"
        className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
        style={{ minWidth: 180 }}
      />
      <span className="text-text-dim text-xs">— or pick from history (click a generation → "Use as reference")</span>
    </div>
  );
}

function ImageOptions({
  model,
  setModel,
  size,
  setSize,
  quality,
  setQuality,
  format,
  setFormat,
  liveModels,
  liveProvider,
}: {
  model: ImageModel;
  setModel: (v: ImageModel) => void;
  size: string;
  setSize: (v: string) => void;
  quality: string;
  setQuality: (v: string) => void;
  format: string;
  setFormat: (v: string) => void;
  liveModels: { id: string; label: string }[] | null;
  liveProvider: string;
}) {
  // Live list wins when present. Otherwise fall back to the
  // OpenAI-flavour hardcoded matrix (so dropdown is never empty).
  const usingLive = liveModels && liveModels.length > 0;
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
                  {m.label}
                </option>
              ))
            : IMAGE_MODELS.map((m) => (
                <option key={m} value={m}>
                  {IMAGE_MODEL_LABELS[m]}
                </option>
              ))}
        </select>
      </div>
      <div>
        <label className="text-text-muted text-xs block">Size</label>
        <select
          value={size}
          onChange={(e) => setSize(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          {(IMAGE_SIZES[model] || ["1024x1024"]).map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </div>
      {model !== "dall-e-2" && (
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
      {isGptImage(model) && (
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

function VideoModelPicker({
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

function Gallery({
  kind,
  items,
  onSelect,
  onOpenLightbox,
  generating,
  generatingPrompt,
  generatingModel,
}: {
  kind: Kind;
  items: Generation[];
  onSelect: (g: Generation) => void;
  onOpenLightbox: (g: Generation) => void;
  generating: boolean;
  generatingPrompt: string;
  generatingModel: string;
}) {
  if (kind === "image") {
    return (
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
          gap: 8,
          padding: 8,
        }}
      >
        {generating && (
          <GeneratingCard prompt={generatingPrompt} model={generatingModel} />
        )}
        {items.map((g) => {
          const src = imageSrc(g);
          return (
            <div
              key={g.id}
              className="border border-border rounded overflow-hidden hover:border-accent transition-colors"
            >
              {src ? (
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
        gridTemplateColumns: kind === "video"
          ? "repeat(auto-fill, minmax(360px, 1fr))"
          : "repeat(auto-fill, minmax(280px, 1fr))",
        gap: 8,
        padding: 8,
      }}
    >
      {generating && kind === "video" && (
        <GeneratingCard prompt={generatingPrompt} model={generatingModel} />
      )}
      {items.map((g) => {
        const url = g.storage_urls?.[0] || g.local_cache_url || g.upstream_urls?.[0] || "";
        return (
          <div
            key={g.id}
            className="border border-border rounded overflow-hidden bg-bg-card"
            onClick={() => onSelect(g)}
          >
            {url ? (
              kind === "video" ? (
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
      })}
    </div>
  );
}

function GeneratingCard({ prompt, model }: { prompt: string; model: string }) {
  return (
    <div
      className="border border-accent rounded overflow-hidden bg-bg-card flex flex-col items-center justify-center"
      style={{ minHeight: 220 }}
    >
      <Spinner />
      <div className="mt-3 text-sm text-text">Generating…</div>
      {prompt && (
        <div className="mt-1 px-3 text-xs text-text-muted text-center" style={{ maxWidth: 260 }} title={prompt}>
          {prompt.length > 80 ? prompt.slice(0, 77) + "…" : prompt}
        </div>
      )}
      {model && <div className="mt-1 text-text-dim" style={{ fontSize: 10 }}>{model}</div>}
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
  return (
    <div className="p-2">
      <div className="text-text text-xs truncate">{g.prompt}</div>
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
  onUseAsReference,
}: {
  selected: Generation;
  onClose: () => void;
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
          onClick={onClose}
          className="text-text-muted hover:text-text leading-none px-1"
          style={{ fontSize: 18 }}
        >
          ×
        </button>
      </header>
      <div className="flex-1 overflow-auto">
        {url && selected.kind === "image" && <img src={url} alt="" className="w-full" />}
        {url && selected.kind === "video" && <video controls src={url} className="w-full" />}
        {url && (selected.kind === "audio_tts" || selected.kind === "audio_sfx" || selected.kind === "music") && (
          <audio controls src={url} className="w-full p-3" />
        )}
        <dl className="px-4 py-3 text-xs flex flex-col gap-2">
          <Row label="Kind" value={selected.kind} />
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
  jobs: { id: number; queue_id: string; model: string; prompt: string; status: string; error: string }[];
}) {
  const inFlight = jobs.filter((j) => j.status === "queued" || j.status === "polling");
  const failed = jobs.filter((j) => j.status === "failed");
  if (inFlight.length === 0 && failed.length === 0) return null;
  return (
    <div className="flex flex-col gap-1 p-2 rounded border border-border bg-bg-card">
      {inFlight.length > 0 && (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-text">
            <strong>{inFlight.length}</strong> video{inFlight.length === 1 ? "" : "s"} processing
          </span>
          <span className="text-text-dim">
            {inFlight
              .slice(0, 3)
              .map((j) => `#${j.id} (${j.model})`)
              .join(", ")}
            {inFlight.length > 3 && `, +${inFlight.length - 3} more`}
          </span>
        </div>
      )}
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
        {url && item.kind === "video" && (
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
