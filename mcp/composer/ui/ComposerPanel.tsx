// ComposerPanel v0.1 — Tier 1.
// List of compositions on the left, JSON editor + render controls on
// the right, gallery of completed renders at the bottom. No timeline
// drag-and-drop; the data model is small enough that the JSON editor
// is the fastest path to iteration.

import { useCallback, useEffect, useState } from "react";

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

const EXAMPLE_EDIT = `{
  "tracks": [{
    "clips": [
      { "asset": { "type": "video", "src": "storage:1" }, "start": 0, "length": 4, "text": { "body": "Intro", "position": "bottom" }, "transition": { "in": "fade" } },
      { "asset": { "type": "video", "src": "storage:2" }, "start": 4, "length": 6 }
    ]
  }],
  "background": "#000000"
}`;

const EXAMPLE_OUTPUT = `{ "format": "mp4", "resolution": "hd", "aspect": "16:9", "fps": 30 }`;

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

export default function ComposerPanel({ projectId }: NativePanelProps) {
  const [compositions, setCompositions] = useState<Composition[]>([]);
  const [bindings, setBindings] = useState<Bindings | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [editText, setEditText] = useState<string>("");
  const [outputText, setOutputText] = useState<string>("");
  const [name, setName] = useState<string>("");
  const [executor, setExecutor] = useState<"auto" | "local" | "remote">("auto");
  const [rendering, setRendering] = useState(false);
  const [status, setStatus] = useState<string>("");
  const [lightbox, setLightbox] = useState<RenderRow | null>(null);

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

  // Selecting a composition loads it into the editor.
  useEffect(() => {
    if (selectedId == null) {
      setEditText(EXAMPLE_EDIT);
      setOutputText(EXAMPLE_OUTPUT);
      setName("");
      return;
    }
    const c = compositions.find((x) => x.id === selectedId);
    if (!c) return;
    setEditText(c.edit_json);
    setOutputText(c.output_json);
    setName(c.name);
  }, [selectedId, compositions]);

  const createOrUpdate = async () => {
    setStatus("Saving…");
    try {
      let editObj: Record<string, unknown>;
      let outputObj: Record<string, unknown>;
      try {
        editObj = JSON.parse(editText);
      } catch (e) {
        setStatus("Edit JSON invalid: " + (e as Error).message);
        return;
      }
      try {
        outputObj = JSON.parse(outputText);
      } catch (e) {
        setStatus("Output JSON invalid: " + (e as Error).message);
        return;
      }
      const body: Record<string, unknown> = { ...editObj, output: outputObj, name };
      const url = selectedId == null ? `${API}/composition/new` : `${API}/composition/${selectedId}`;
      const method = selectedId == null ? "POST" : "PUT";
      const res = await fetch(url, {
        method,
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(selectedId == null ? body : { ...editObj, output: outputObj, name }),
      });
      const text = await res.text();
      if (!res.ok) {
        setStatus(`Save failed: ${res.status} ${text.slice(0, 300)}`);
        return;
      }
      let result: { id?: number } = {};
      try { result = JSON.parse(text); } catch {}
      setStatus("Saved.");
      await load();
      if (result.id) setSelectedId(result.id);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  };

  const render = async () => {
    if (selectedId == null) {
      setStatus("Save first.");
      return;
    }
    setRendering(true);
    setStatus("Rendering…");
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
      load();
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setRendering(false);
    }
  };

  const deleteSelected = async () => {
    if (selectedId == null) return;
    if (!confirm(`Delete composition #${selectedId}?`)) return;
    await fetch(`${API}/composition/${selectedId}`, {
      method: "DELETE",
      credentials: "same-origin",
    });
    setSelectedId(null);
    load();
  };

  const selected = selectedId != null ? compositions.find((c) => c.id === selectedId) : null;

  return (
    <div className="h-full flex">
      <Sidebar
        compositions={compositions}
        selectedId={selectedId}
        onSelect={setSelectedId}
        onNew={() => setSelectedId(null)}
      />
      <main className="flex-1 flex flex-col min-w-0">
        <Header bindings={bindings} />

        <div className="p-4 grid gap-4 min-h-0 overflow-auto">
          <Field label="Name">
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="optional"
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
            />
          </Field>
          <Field label="Edit JSON">
            <textarea
              value={editText}
              onChange={(e) => setEditText(e.target.value)}
              spellCheck={false}
              style={{
                fontFamily:
                  "ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace",
                minHeight: 220,
              }}
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
            />
          </Field>
          <Field label="Output JSON">
            <textarea
              value={outputText}
              onChange={(e) => setOutputText(e.target.value)}
              spellCheck={false}
              style={{
                fontFamily:
                  "ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace",
                minHeight: 72,
              }}
              className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-xs"
            />
          </Field>

          <div className="flex items-end gap-3 flex-wrap">
            <button
              onClick={createOrUpdate}
              className="px-3 py-1.5 text-sm border border-border rounded text-text"
            >
              {selectedId == null ? "Create" : "Save"}
            </button>
            <Field label="Executor">
              <select
                value={executor}
                onChange={(e) => setExecutor(e.target.value as "auto" | "local" | "remote")}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              >
                <option value="auto">auto (ladder)</option>
                <option value="local">local (ffmpeg in sidecar)</option>
                <option value="remote">remote (via instances)</option>
              </select>
            </Field>
            <button
              onClick={render}
              disabled={rendering || selectedId == null}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
            >
              {rendering ? "…" : "Render"}
            </button>
            {selectedId != null && (
              <button
                onClick={deleteSelected}
                className="px-3 py-1.5 text-sm border border-border rounded text-text-muted ml-auto"
              >
                Delete
              </button>
            )}
          </div>

          <div className="text-xs text-text-dim">{status}</div>

          {selected?.latest_render && (
            <RenderPreview render={selected.latest_render} onOpen={setLightbox} />
          )}
        </div>
      </main>

      {lightbox && <Lightbox render={lightbox} onClose={() => setLightbox(null)} />}
    </div>
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
    <aside
      className="border-r border-border bg-bg-card flex flex-col"
      style={{ width: 280 }}
    >
      <header className="px-3 py-2 border-b border-border flex items-center gap-2">
        <span className="text-text-muted text-xs flex-1">Compositions</span>
        <button
          onClick={onNew}
          className="text-xs px-2 py-0.5 border border-border rounded text-accent"
        >
          + New
        </button>
      </header>
      <div className="flex-1 overflow-auto">
        {compositions.length === 0 && (
          <div className="p-3 text-text-dim text-xs">No compositions yet.</div>
        )}
        {compositions.map((c) => {
          const isSel = c.id === selectedId;
          const r = c.latest_render;
          return (
            <button
              key={c.id}
              onClick={() => onSelect(c.id)}
              className={
                "w-full text-left px-3 py-2 border-b border-border block " +
                (isSel ? "bg-bg-input" : "hover:bg-bg-input")
              }
            >
              <div className="text-text text-sm truncate">
                {c.name || `#${c.id}`}
              </div>
              <div className="text-text-dim mt-0.5 flex items-center gap-1.5" style={{ fontSize: 10 }}>
                <span>{c.duration_seconds.toFixed(1)}s</span>
                {r && (
                  <>
                    <span>·</span>
                    <StatusPill status={r.status} />
                  </>
                )}
              </div>
            </button>
          );
        })}
      </div>
    </aside>
  );
}

function Header({ bindings }: { bindings: Bindings | null }) {
  if (!bindings) return null;
  const renderMode = bindings.render_executor
    ? `executor: ${bindings.render_executor} (v0.2)`
    : bindings.render_host_id > 0
      ? `remote · host #${bindings.render_host_id}`
      : "local · ffmpeg in sidecar";
  return (
    <header className="px-4 py-2 border-b border-border flex items-center gap-3 text-xs">
      <span className="text-text-muted">Render mode:</span>
      <span className="text-text">{renderMode}</span>
      <span className="text-text-dim">·</span>
      <span className="text-text-muted">storage:</span>
      <span className="text-text">{bindings.storage_bound ? "bound" : "unbound (local cache)"}</span>
    </header>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <label className="text-text-muted text-xs block mb-1">{label}</label>
      {children}
    </div>
  );
}

function StatusPill({ status }: { status: RenderRow["status"] }) {
  const color =
    status === "complete"
      ? "var(--apteva-accent, #4ade80)"
      : status === "failed"
        ? "var(--apteva-danger, #ef4444)"
        : "var(--apteva-text-dim, #888)";
  return (
    <span style={{ color }}>{status}</span>
  );
}

function RenderPreview({
  render,
  onOpen,
}: {
  render: RenderRow;
  onOpen: (r: RenderRow) => void;
}) {
  const url = renderSrc(render);
  if (render.status !== "complete") {
    return (
      <div className="p-3 border border-border rounded bg-bg-card text-xs">
        <div className="flex items-center gap-2">
          <StatusPill status={render.status} />
          <span className="text-text-muted">render #{render.id} via {render.executor}</span>
        </div>
        {render.error && (
          <pre
            className="mt-2 text-text-muted whitespace-pre-wrap break-all"
            style={{ fontSize: 10 }}
          >
            {render.error}
          </pre>
        )}
      </div>
    );
  }
  return (
    <div className="border border-border rounded bg-bg-card overflow-hidden">
      <div onClick={() => onOpen(render)} style={{ cursor: "pointer" }}>
        {url ? (
          <video controls src={url} className="w-full" />
        ) : (
          <div className="py-12 text-center text-text-muted text-xs">no source</div>
        )}
      </div>
      <div className="p-2 text-text-dim flex items-center gap-1.5" style={{ fontSize: 10 }}>
        <span>render #{render.id}</span>
        <span>·</span>
        <span>{render.executor}</span>
        <span>·</span>
        <span>{(render.duration_ms / 1000).toFixed(1)}s render time</span>
        {formatCost(render.cost_usd) && (
          <>
            <span>·</span>
            <span className="text-accent">{formatCost(render.cost_usd)}</span>
          </>
        )}
      </div>
    </div>
  );
}

function Lightbox({
  render,
  onClose,
}: {
  render: RenderRow;
  onClose: () => void;
}) {
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
        style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 12 }}
      >
        {url && (
          <video controls autoPlay src={url} style={{ maxWidth: "92vw", maxHeight: "82vh" }} />
        )}
        <div className="text-text-dim" style={{ fontSize: 11 }}>
          render #{render.id} · {render.executor} · {(render.duration_ms / 1000).toFixed(1)}s
        </div>
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
      </div>
    </div>
  );
}
