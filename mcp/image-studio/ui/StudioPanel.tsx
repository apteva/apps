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
  upstream_urls: string[];
  thumbnail_b64: string;
  count: number;
  created_at: string;
}

const API = "/api/apps/image-studio";

export default function StudioPanel({ projectId }: NativePanelProps) {
  const [items, setItems] = useState<Generation[]>([]);
  const [prompt, setPrompt] = useState("");
  const [size, setSize] = useState("1024x1024");
  const [generating, setGenerating] = useState(false);
  const [status, setStatus] = useState("");
  const [selected, setSelected] = useState<Generation | null>(null);

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
      // Trigger via the MCP tool — the dashboard's MCP servers expose
      // a call-tool endpoint, but image-studio also has an HTTP bridge
      // at /generations. Simplest: post to /generate which forwards
      // through the sidecar's MCP handler. For v0.1 we just call MCP
      // via our parent's tools/call surface using the dashboard's
      // existing mcpServers.callTool — but the panel is in the app's
      // own sidecar. Cleanest: have an HTTP /generate route on the
      // sidecar that wraps toolImageGenerate. Falling back to the
      // tool-call API the dashboard already has would require the
      // mcp_servers row id, which we don't carry into the panel.
      //
      // For v0.1: just call the storage-style POST — image-studio's
      // sidecar already exposes /generations as GET. Add POST that
      // calls toolImageGenerate. For now, this panel triggers via
      // the agent / chat — direct UI generation is v0.2.
      setStatus("Direct UI generation lands in v0.2 — for now ask an agent or use the MCP test in Settings → MCP Servers.");
    } finally {
      setGenerating(false);
    }
  };

  return (
    <div className="h-full flex">
      <div className="flex-1 flex flex-col p-6 gap-4 min-w-0">
        <div className="flex items-end gap-3">
          <div className="flex-1">
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
          <select
            value={size}
            onChange={(e) => setSize(e.target.value)}
            className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          >
            <option>1024x1024</option>
            <option>1792x1024</option>
            <option>1024x1792</option>
          </select>
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
              {items.map((g) => (
                <button
                  key={g.id}
                  onClick={() => setSelected(g)}
                  className="text-left border border-border rounded overflow-hidden hover:border-accent transition-colors"
                >
                  {g.thumbnail_b64 ? (
                    <img
                      src={`data:image/jpeg;base64,${g.thumbnail_b64}`}
                      alt=""
                      className="w-full"
                    />
                  ) : (
                    <div className="bg-bg-input py-12 text-center text-text-muted text-xs">no thumbnail</div>
                  )}
                  <div className="p-2">
                    <div className="text-text text-xs truncate">{g.prompt}</div>
                    <div className="text-text-dim text-[10px] mt-0.5">
                      {g.provider} · {g.model || g.size} · {new Date(g.created_at).toLocaleString()}
                    </div>
                  </div>
                </button>
              ))}
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
            {selected.thumbnail_b64 && (
              <img
                src={`data:image/jpeg;base64,${selected.thumbnail_b64}`}
                alt=""
                className="w-full"
              />
            )}
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
            {selected.storage_ids.length > 0 && (
              <div className="px-4 pb-3">
                <a
                  href={`/api/apps/storage/files/${selected.storage_ids[0]}/content?project_id=${encodeURIComponent(projectId)}`}
                  target="_blank"
                  rel="noopener"
                  className="text-accent text-xs hover:underline"
                >
                  Open full image →
                </a>
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
