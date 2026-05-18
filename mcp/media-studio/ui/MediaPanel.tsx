// MediaPanel — media generation gallery + per-kind composer.
// Loaded by the dashboard via dynamic import; uses host React via
// importmap; talks to the media-studio sidecar at /api/apps/media-studio/*.

import { useCallback, useEffect, useRef, useState } from "react";

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
  count: number;
  created_at: string;
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
  if (g.storage_urls && g.storage_urls.length > 0) return g.storage_urls[0];
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

  // Per-kind composer state.
  const [prompt, setPrompt] = useState("");
  const [imageModel, setImageModel] = useState<ImageModel>("gpt-image-2");
  const [imageSize, setImageSize] = useState("1024x1024");
  const [imageQuality, setImageQuality] = useState("auto");
  const [imageFormat, setImageFormat] = useState("png");
  const [duration, setDuration] = useState(5); // video/audio/music
  const [aspect, setAspect] = useState("16:9");
  const [voice, setVoice] = useState("");

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
        body.model = imageModel;
        body.size = imageSize;
        const options: Record<string, unknown> = {};
        if (imageModel !== "dall-e-2") options.quality = imageQuality;
        if (isGptImage(imageModel)) options.output_format = imageFormat;
        body.options = options;
      } else if (activeKind === "video") {
        body.duration = duration;
        body.aspect = aspect;
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
          <Composer
            kind={activeKind}
            prompt={prompt}
            setPrompt={setPrompt}
            generate={generate}
            generating={generating}
            disabled={!isBound}
            imageModel={imageModel}
            setImageModel={setImageModel}
            imageSize={imageSize}
            setImageSize={setImageSize}
            imageQuality={imageQuality}
            setImageQuality={setImageQuality}
            imageFormat={imageFormat}
            setImageFormat={setImageFormat}
            duration={duration}
            setDuration={setDuration}
            aspect={aspect}
            setAspect={setAspect}
            voice={voice}
            setVoice={setVoice}
          />

          <div className="flex-1 overflow-auto border border-border rounded">
            {items.length === 0 ? (
              <div className="py-12 px-6 text-center text-text-muted text-sm">
                {status || "No generations yet for this kind."}
              </div>
            ) : (
              <Gallery kind={activeKind} items={items} onSelect={setSelected} />
            )}
          </div>
          <div className="text-xs text-text-dim">{status}</div>
        </div>

        {selected && (
          <DetailAside selected={selected} onClose={() => setSelected(null)} />
        )}
      </div>
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
  imageModel: ImageModel;
  setImageModel: (v: ImageModel) => void;
  imageSize: string;
  setImageSize: (v: string) => void;
  imageQuality: string;
  setImageQuality: (v: string) => void;
  imageFormat: string;
  setImageFormat: (v: string) => void;
  duration: number;
  setDuration: (v: number) => void;
  aspect: string;
  setAspect: (v: string) => void;
  voice: string;
  setVoice: (v: string) => void;
}

function Composer(p: ComposerProps) {
  const promptPlaceholder =
    p.kind === "audio_tts"
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
      {p.kind === "image" && (
        <ImageOptions
          model={p.imageModel}
          setModel={p.setImageModel}
          size={p.imageSize}
          setSize={p.setImageSize}
          quality={p.imageQuality}
          setQuality={p.setImageQuality}
          format={p.imageFormat}
          setFormat={p.setImageFormat}
        />
      )}
      {p.kind === "video" && (
        <>
          <NumberField label="Duration (s)" value={p.duration} onChange={p.setDuration} min={1} max={60} />
          <TextField label="Aspect" value={p.aspect} onChange={p.setAspect} />
        </>
      )}
      {p.kind === "audio_tts" && (
        <TextField label="Voice" value={p.voice} onChange={p.setVoice} placeholder="default" />
      )}
      {(p.kind === "audio_sfx" || p.kind === "music") && (
        <NumberField label="Duration (s)" value={p.duration} onChange={p.setDuration} min={1} max={300} />
      )}
      <button
        onClick={p.generate}
        disabled={!p.prompt.trim() || p.generating || p.disabled}
        className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
      >
        {p.generating ? "…" : "Generate"}
      </button>
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
}: {
  model: ImageModel;
  setModel: (v: ImageModel) => void;
  size: string;
  setSize: (v: string) => void;
  quality: string;
  setQuality: (v: string) => void;
  format: string;
  setFormat: (v: string) => void;
}) {
  return (
    <>
      <div>
        <label className="text-text-muted text-xs block">Model</label>
        <select
          value={model}
          onChange={(e) => setModel(e.target.value as ImageModel)}
          className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
        >
          {IMAGE_MODELS.map((m) => (
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
}: {
  kind: Kind;
  items: Generation[];
  onSelect: (g: Generation) => void;
}) {
  if (kind === "image") {
    return (
      <div className="grid grid-cols-2 gap-2 p-2">
        {items.map((g) => {
          const src = imageSrc(g);
          return (
            <button
              key={g.id}
              onClick={() => onSelect(g)}
              className="text-left border border-border rounded overflow-hidden hover:border-accent transition-colors"
            >
              {src ? (
                <img src={src} alt="" className="w-full" loading="lazy" />
              ) : (
                <div className="bg-bg-input py-12 text-center text-text-muted text-xs">no preview</div>
              )}
              <CardMeta g={g} />
            </button>
          );
        })}
      </div>
    );
  }
  // Video, audio, music: single-column list with native players.
  return (
    <div className="flex flex-col gap-2 p-2">
      {items.map((g) => {
        const url = g.storage_urls?.[0] || g.upstream_urls?.[0] || "";
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

function CardMeta({ g }: { g: Generation }) {
  return (
    <div className="p-2">
      <div className="text-text text-xs truncate">{g.prompt}</div>
      <div className="text-text-dim mt-0.5" style={{ fontSize: 10 }}>
        {g.provider} · {g.model || g.size || "—"} · {new Date(g.created_at).toLocaleString()}
      </div>
    </div>
  );
}

function DetailAside({
  selected,
  onClose,
}: {
  selected: Generation;
  onClose: () => void;
}) {
  const url = selected.storage_urls?.[0] || selected.upstream_urls?.[0] || "";
  return (
    <aside
      className="border-l border-border bg-bg-card flex flex-col"
      style={{ width: 384 }}
    >
      <header className="flex items-center gap-2 px-4 py-3 border-b border-border">
        <span className="text-text font-medium truncate flex-1">{selected.prompt}</span>
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
