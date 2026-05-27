// ComposerPanel v0.2 - structured timeline editor with approximate preview.

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
type Aspect = "16:9" | "9:16" | "1:1" | "4:3";

interface ClipDraft {
  id: string;
  asset: { type: AssetType; src: string };
  start: number;
  length: number;
  transition?: { in?: string; out?: string };
  text?: { body: string; position?: "top" | "center" | "bottom"; font_size?: number; color?: string };
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
  soundtrack: { src: string; volume: number } | null;
  background: string;
  output: OutputDraft;
}

interface ResolvedAsset {
  src: string;
  url: string;
  kind: "video" | "image" | "audio";
}

type Tab = "timeline" | "json";

const DEFAULT_DRAFT: DraftState = {
  name: "",
  background: "#000000",
  clips: [
    {
      id: "clip-1",
      asset: { type: "video", src: "storage:1" },
      start: 0,
      length: 4,
      transition: { in: "fade", out: "none" },
      text: { body: "Intro", position: "bottom", font_size: 32, color: "#ffffff" },
    },
    {
      id: "clip-2",
      asset: { type: "video", src: "storage:2" },
      start: 4,
      length: 6,
      transition: { in: "none", out: "none" },
    },
  ],
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
        id: `clip-${i + 1}`,
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
      })));
    }
    if (timeline.soundtrack?.src) {
      draft.soundtrack = {
        src: String(timeline.soundtrack.src),
        volume: Number(timeline.soundtrack.volume) || 1,
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
      asset: { type: clip.asset.type, src: clip.asset.src.trim() },
      start: clip.start,
      length: clip.length,
    };
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
  if (draft.soundtrack?.src?.trim()) {
    body.soundtrack = {
      src: draft.soundtrack.src.trim(),
      volume: Math.max(0, Math.min(1, Number(draft.soundtrack.volume) || 1)),
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
                />
                <Timeline
                  clips={clips}
                  selectedClipId={selectedClipId}
                  playhead={playhead}
                  duration={totalDuration}
                  onSelect={setSelectedClipId}
                  onSeek={setPlayhead}
                  onAdd={addClip}
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
        <div className="relative w-full max-w-4xl border border-border overflow-hidden" style={{ background, aspectRatio: aspectRatio(aspect) }}>
          {url && clip?.asset.type === "image" && (
            <img src={url} alt="" className="absolute inset-0 w-full h-full object-cover" />
          )}
          {url && clip?.asset.type !== "image" && (
            <video ref={mediaRef} src={url} muted className="absolute inset-0 w-full h-full object-cover" />
          )}
          {!url && (
            <div className="absolute inset-0 flex items-center justify-center text-text-dim text-sm">
              {clip?.asset.src || "No clip source"}
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
}: {
  clips: ClipDraft[];
  selectedClipId: string;
  playhead: number;
  duration: number;
  onSelect: (id: string) => void;
  onSeek: (t: number) => void;
  onAdd: () => void;
}) {
  return (
    <section className="border border-border rounded bg-bg-card overflow-hidden">
      <header className="px-3 py-2 border-b border-border flex items-center gap-2">
        <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Timeline</h2>
        <button onClick={onAdd} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Add clip</button>
      </header>
      <div className="p-3">
        <button
          type="button"
          onClick={(e) => {
            const rect = e.currentTarget.getBoundingClientRect();
            onSeek(((e.clientX - rect.left) / rect.width) * duration);
          }}
          className="relative w-full h-24 border border-border rounded bg-bg text-left overflow-hidden"
        >
          <div
            className="absolute top-0 bottom-0 w-px bg-accent"
            style={{ left: `${duration ? Math.min(100, (playhead / duration) * 100) : 0}%` }}
          />
          <div className="absolute inset-x-2 top-8 h-10 flex">
            {clips.map((clip) => {
              const width = duration ? (clip.length / duration) * 100 : 100;
              const selected = clip.id === selectedClipId;
              return (
                <div
                  key={clip.id}
                  role="button"
                  tabIndex={0}
                  onClick={(e) => {
                    e.stopPropagation();
                    onSelect(clip.id);
                    onSeek(clip.start);
                  }}
                  className={`h-10 border text-xs flex flex-col justify-center px-2 overflow-hidden ${selected ? "border-accent bg-accent/10" : "border-border bg-bg-input hover:border-accent"}`}
                  style={{ width: `${Math.max(6, width)}%` }}
                >
                  <span className="text-text truncate">{clip.asset.src || "empty source"}</span>
                  <span className="text-text-dim">{clip.asset.type} - {clip.length.toFixed(1)}s</span>
                </div>
              );
            })}
          </div>
        </button>
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
}: {
  draft: DraftState;
  clip: ClipDraft | null;
  onDraft: (fn: (draft: DraftState) => DraftState) => void;
  onClip: (id: string, patch: Partial<ClipDraft>) => void;
  onDelete: (id: string) => void;
  onMove: (id: string, dir: -1 | 1) => void;
  onDeleteComposition: () => void;
  canDeleteComposition: boolean;
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
            <input
              value={draft.soundtrack?.src || ""}
              onChange={(e) => onDraft((cur) => ({ ...cur, soundtrack: e.target.value ? { src: e.target.value, volume: cur.soundtrack?.volume ?? 1 } : null }))}
              placeholder="storage:99 or https://..."
              className={field}
            />
          </Field>
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

        {clip && (
          <section className="space-y-2">
            <div className="flex items-center gap-2">
              <h2 className="text-xs uppercase tracking-wide text-text-dim flex-1">Selected clip</h2>
              <button onClick={() => onMove(clip.id, -1)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Left</button>
              <button onClick={() => onMove(clip.id, 1)} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Right</button>
            </div>
            <Field label="Source">
              <input
                value={clip.asset.src}
                onChange={(e) => onClip(clip.id, { asset: { ...clip.asset, src: e.target.value } })}
                placeholder="storage:1, mediastudio:4, or https://..."
                className={field}
              />
            </Field>
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
