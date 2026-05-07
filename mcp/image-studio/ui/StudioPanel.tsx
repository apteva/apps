// StudioPanel — image generation gallery + prompt entry.
// Loaded by the dashboard via dynamic import; uses host React via
// importmap; talks to the image-studio sidecar at /api/apps/image-studio/*.

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
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
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
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
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

interface Generation {
  id: number;
  prompt: string;
  revised_prompt?: string;
  provider: string;
  model: string;
  size: string;
  storage_ids: number[];
  storage_urls: string[];
  upstream_urls: string[];
  thumbnail_b64: string;
  count: number;
  created_at: string;
}

// Pick the best image src for a generation: a storage URL (full image,
// served by the storage app via the platform proxy) when available;
// otherwise fall back to the inline thumbnail. Returns "" when neither
// is set so the caller can render a placeholder.
function imageSrc(g: Generation): string {
  if (g.storage_urls && g.storage_urls.length > 0) return g.storage_urls[0];
  if (g.thumbnail_b64) return `data:image/jpeg;base64,${g.thumbnail_b64}`;
  return "";
}

const API = "/api/apps/image-studio";

// Per-model option matrices. The MCP tool schema is the source of truth;
// the UI mirrors a usable subset (no "auto" / 3840x2160 — those are
// available to agents via the tool, but the panel offers concrete sizes).
type ModelId =
  | "gpt-image-2"
  | "gpt-image-1.5"
  | "gpt-image-1"
  | "gpt-image-1-mini"
  | "dall-e-3"
  | "dall-e-2";

const MODEL_LABELS: Record<ModelId, string> = {
  "gpt-image-2": "GPT Image 2 (current)",
  "gpt-image-1.5": "GPT Image 1.5",
  "gpt-image-1": "GPT Image 1",
  "gpt-image-1-mini": "GPT Image 1 Mini",
  "dall-e-3": "DALL·E 3 (legacy)",
  "dall-e-2": "DALL·E 2 (legacy)",
};
const MODELS: ModelId[] = [
  "gpt-image-2",
  "gpt-image-1.5",
  "gpt-image-1",
  "gpt-image-1-mini",
  "dall-e-3",
  "dall-e-2",
];

const MODEL_SIZES: Record<ModelId, string[]> = {
  "gpt-image-2": ["1024x1024", "1024x1536", "1536x1024", "2048x2048", "3840x2160"],
  "gpt-image-1.5": ["1024x1024", "1024x1536", "1536x1024"],
  "gpt-image-1": ["1024x1024", "1024x1536", "1536x1024"],
  "gpt-image-1-mini": ["1024x1024", "1024x1536", "1536x1024"],
  "dall-e-3": ["1024x1024", "1792x1024", "1024x1792"],
  "dall-e-2": ["256x256", "512x512", "1024x1024"],
};

const GPT_IMAGE_QUALITIES = ["auto", "low", "medium", "high"];
const DALLE3_QUALITIES = ["standard", "hd"];

function isGptImage(m: ModelId) { return m.startsWith("gpt-image"); }

export default function StudioPanel({ projectId }: NativePanelProps) {
  const [items, setItems] = useState<Generation[]>([]);
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState<ModelId>("gpt-image-2");
  const [size, setSize] = useState("1024x1024");
  const [quality, setQuality] = useState("auto");
  const [outputFormat, setOutputFormat] = useState("png");
  const [generating, setGenerating] = useState(false);
  const [status, setStatus] = useState("");
  const [selected, setSelected] = useState<Generation | null>(null);

  // Keep size + quality valid when the model changes — dall-e-2 has no
  // 1792x… and gpt-image's "auto" isn't a dall-e-3 quality.
  useEffect(() => {
    const allowedSizes = MODEL_SIZES[model] || ["1024x1024"];
    if (!allowedSizes.includes(size)) setSize(allowedSizes[0]);
    if (isGptImage(model)) {
      if (!GPT_IMAGE_QUALITIES.includes(quality)) setQuality("auto");
    } else if (model === "dall-e-3") {
      if (!DALLE3_QUALITIES.includes(quality)) setQuality("standard");
    }
  }, [model, size, quality]);

  const load = useCallback(async () => {
    try {
      const res = await fetch(`${API}/generations?project_id=${encodeURIComponent(projectId)}`, {
        credentials: "same-origin",
      });
      if (!res.ok) {
        setStatus(`Error: ${res.status}`);
        return;
      }
      const data = await res.json();
      setItems(data.generations || []);
      setStatus(`${(data.generations || []).length} generation${data.generations?.length === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [projectId]);

  useEffect(() => { load(); }, [load]);

  // Live refresh — main.go fires image.generated on every successful
  // tool call, including agent-initiated ones from another tab.
  useAppEvents("image-studio", projectId, (ev) => {
    if (ev.topic === "image.generated") {
      load();
    }
  });

  const generate = async () => {
    if (!prompt.trim() || generating) return;
    setGenerating(true);
    setStatus("Generating…");
    try {
      const res = await fetch(`${API}/generate`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          prompt,
          model,
          size,
          // dall-e-2 has no quality; for everything else send what we have.
          quality: model === "dall-e-2" ? undefined : quality,
          // gpt-image-* honors output_format; DALL·E ignores it server-side
          // (we strip it in buildProviderArgs anyway).
          output_format: isGptImage(model) ? outputFormat : undefined,
          n: 1,
        }),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Error ${res.status}: ${text.slice(0, 300)}`);
        return;
      }
      let result: { isError?: boolean; content?: { type: string; text?: string }[] } = {};
      try { result = JSON.parse(text); } catch {}
      if (result.isError) {
        const msg = result.content?.find((c) => c.type === "text")?.text || "generation failed";
        setStatus(`Error: ${msg}`);
        return;
      }
      // Live event from the sidecar will trigger load() — just clear
      // the prompt and let the gallery refresh.
      setPrompt("");
      setStatus("Done.");
      load();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setGenerating(false);
    }
  };

  return (
    <div className="h-full flex">
      <div className="flex-1 flex flex-col p-6 gap-4 min-w-0">
        <div className="flex items-end gap-3 flex-wrap">
          <div className="flex-1 min-w-[240px]">
            <label className="text-text-muted text-xs">Prompt</label>
            <input
              type="text"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") generate(); }}
              placeholder="a cat in a hat"
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            />
          </div>
          <div>
            <label className="text-text-muted text-xs block">Model</label>
            <select
              value={model}
              onChange={(e) => setModel(e.target.value as ModelId)}
              className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            >
              {MODELS.map((m) => (
                <option key={m} value={m}>{MODEL_LABELS[m]}</option>
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
              {(MODEL_SIZES[model] || ["1024x1024"]).map((s) => (
                <option key={s} value={s}>{s}</option>
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
                  <option key={q} value={q}>{q}</option>
                ))}
              </select>
            </div>
          )}
          {isGptImage(model) && (
            <div>
              <label className="text-text-muted text-xs block">Format</label>
              <select
                value={outputFormat}
                onChange={(e) => setOutputFormat(e.target.value)}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              >
                <option value="png">PNG</option>
                <option value="jpeg">JPEG</option>
                <option value="webp">WebP</option>
              </select>
            </div>
          )}
          <button
            onClick={generate}
            disabled={!prompt.trim() || generating}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {generating ? "…" : "Generate"}
          </button>
        </div>

        <div className="flex-1 overflow-auto border border-border rounded">
          {items.length === 0 ? (
            <div className="py-12 px-6 text-center text-text-muted text-sm">
              {status || "No generations yet. Ask an agent to call image_generate, or test the tool from Settings → MCP Servers."}
            </div>
          ) : (
            <div className="grid grid-cols-2 gap-2 p-2">
              {items.map((g) => {
                const src = imageSrc(g);
                return (
                <button
                  key={g.id}
                  onClick={() => setSelected(g)}
                  className="text-left border border-border rounded overflow-hidden hover:border-accent transition-colors"
                >
                  {src ? (
                    <img src={src} alt="" className="w-full" loading="lazy" />
                  ) : (
                    <div className="bg-bg-input py-12 text-center text-text-muted text-xs">no preview</div>
                  )}
                  <div className="p-2">
                    <div className="text-text text-xs truncate">{g.prompt}</div>
                    <div className="text-text-dim text-[10px] mt-0.5">
                      {g.provider} · {g.model || g.size} · {new Date(g.created_at).toLocaleString()}
                    </div>
                  </div>
                </button>
                );
              })}
            </div>
          )}
        </div>

        <div className="text-xs text-text-dim">{status}</div>
      </div>

      {selected && (
        <aside className="w-96 border-l border-border bg-bg-card flex flex-col">
          <header className="flex items-center gap-2 px-4 py-3 border-b border-border">
            <span className="text-text font-medium truncate flex-1">{selected.prompt}</span>
            <button
              onClick={() => setSelected(null)}
              className="text-text-muted hover:text-text text-lg leading-none px-1"
            >×</button>
          </header>
          <div className="flex-1 overflow-auto">
            {(() => {
              const src = imageSrc(selected);
              return src ? <img src={src} alt="" className="w-full" /> : null;
            })()}
            <dl className="px-4 py-3 text-xs flex flex-col gap-2">
              <Row label="Provider" value={selected.provider} />
              <Row label="Model" value={selected.model || "—"} />
              <Row label="Size" value={selected.size || "—"} />
              <Row label="Count" value={String(selected.count)} />
              <Row label="Created" value={new Date(selected.created_at).toLocaleString()} />
              {selected.revised_prompt && (
                <Row label="Revised" value={selected.revised_prompt} />
              )}
              {selected.storage_ids.length > 0 && (
                <Row
                  label="Storage IDs"
                  value={selected.storage_ids.map((id) => `#${id}`).join(", ")}
                />
              )}
            </dl>
            {selected.storage_urls && selected.storage_urls.length > 0 && (
              <div className="px-4 pb-3 flex flex-col gap-1">
                {selected.storage_urls.map((url, i) => (
                  <a
                    key={i}
                    href={url}
                    target="_blank"
                    rel="noopener"
                    className="text-accent text-xs hover:underline"
                  >
                    Open image #{selected.storage_ids[i]} →
                  </a>
                ))}
              </div>
            )}
          </div>
        </aside>
      )}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-2">
      <span className="text-text-dim w-20 flex-shrink-0">{label}</span>
      <span className="flex-1 min-w-0 text-text break-all" title={value}>{value}</span>
    </div>
  );
}
