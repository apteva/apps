// EmbedPanel — operator surface for storage-backed share links.
//
// The agent-facing MCP tools are the source of truth. This panel
// mirrors those workflows over the app's HTTP routes: list embeds,
// create one for a storage file, refresh storage metadata, delete,
// and copy the viewer/oEmbed/iframe values.

import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface EmbedRow {
  id: number;
  token: string;
  project_id: string;
  storage_file_id: number;
  storage_project_id?: string;
  title: string;
  name: string;
  content_type: string;
  size_bytes: number;
  width: number;
  height: number;
  status: string;
  created_at: string;
  updated_at: string;
}

interface EmbedEnvelope {
  embed: EmbedRow;
  viewer_url: string;
  oembed_url: string;
  html: string;
}

interface StorageFile {
  id: number;
  name: string;
  folder: string;
  content_type: string;
  size_bytes: number;
  visibility?: string;
  created_at?: string;
}

const API = "/api/apps/embed";
const STORAGE = "/api/apps/storage";

export default function EmbedPanel({ projectId, installId }: NativePanelProps) {
  const [embeds, setEmbeds] = useState<EmbedEnvelope[]>([]);
  const [files, setFiles] = useState<StorageFile[]>([]);
  const [selected, setSelected] = useState<EmbedEnvelope | null>(null);
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [q, setQ] = useState("");
  const [draftID, setDraftID] = useState("");
  const [draftTitle, setDraftTitle] = useState("");
  const [draftWidth, setDraftWidth] = useState("640");
  const [draftHeight, setDraftHeight] = useState("360");

  const query = useMemo(() => {
    const p = new URLSearchParams();
    if (projectId) p.set("project_id", projectId);
    if (installId != null) p.set("install_id", String(installId));
    return p.toString();
  }, [projectId, installId]);

  const loadEmbeds = useCallback(async () => {
    setLoading(true);
    try {
      const res = await fetch(`${API}/embeds?${query}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { embeds?: EmbedEnvelope[] };
      const rows = data.embeds ?? [];
      setEmbeds(rows);
      setSelected((cur) => rows.find((r) => r.embed.id === cur?.embed.id) ?? rows[0] ?? null);
      setStatus(`${rows.length} embed${rows.length === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus("Load embeds: " + (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [query]);

  const loadFiles = useCallback(async () => {
    try {
      const p = new URLSearchParams();
      if (projectId) p.set("project_id", projectId);
      p.set("limit", "100");
      if (q.trim()) p.set("q", q.trim());
      const res = await fetch(`${STORAGE}/files?${p.toString()}`, { credentials: "same-origin" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json() as { files?: StorageFile[] };
      setFiles(data.files ?? []);
    } catch (e) {
      setStatus("Load storage: " + (e as Error).message);
    }
  }, [projectId, q]);

  useEffect(() => { loadEmbeds(); }, [loadEmbeds]);
  useEffect(() => { loadFiles(); }, [loadFiles]);

  const createEmbed = async (file?: StorageFile) => {
    const id = file?.id ?? parseInt(draftID, 10);
    if (!id) {
      setStatus("Pick a storage file or enter a file id.");
      return;
    }
    try {
      const body = {
        storage_file_id: id,
        title: (file ? file.name : draftTitle).trim() || undefined,
        width: parseInt(draftWidth, 10) || undefined,
        height: parseInt(draftHeight, 10) || undefined,
        storage_project_id: projectId,
      };
      const res = await fetch(`${API}/embeds?${query}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await res.text());
      const created = await res.json() as EmbedEnvelope;
      setDraftID("");
      setDraftTitle("");
      await loadEmbeds();
      setSelected(created);
      setStatus(`Created embed for storage file ${id}`);
    } catch (e) {
      setStatus("Create: " + (e as Error).message);
    }
  };

  const refreshEmbed = async (row: EmbedEnvelope) => {
    try {
      const res = await fetch(`${API}/embeds/${row.embed.token}?${query}`, {
        method: "PATCH",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      await loadEmbeds();
      setStatus("Refreshed storage metadata");
    } catch (e) {
      setStatus("Refresh: " + (e as Error).message);
    }
  };

  const deleteEmbed = async (row: EmbedEnvelope) => {
    if (!confirm(`Delete embed for ${row.embed.title || row.embed.name}?`)) return;
    try {
      const res = await fetch(`${API}/embeds/${row.embed.token}?${query}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(await res.text());
      await loadEmbeds();
      setStatus("Deleted embed");
    } catch (e) {
      setStatus("Delete: " + (e as Error).message);
    }
  };

  const copy = async (label: string, value: string) => {
    await navigator.clipboard.writeText(value);
    setStatus(`Copied ${label}`);
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="px-4 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-sm font-semibold">Embed</h1>
        <button
          type="button"
          onClick={() => { loadEmbeds(); loadFiles(); }}
          disabled={loading}
          className="px-2.5 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
        >Refresh</button>
        <span className="ml-auto text-xs text-text-muted">{status}</span>
      </header>

      <div className="flex-1 min-h-0 grid grid-cols-[minmax(22rem,28rem)_1fr]">
        <aside className="border-r border-border min-h-0 flex flex-col">
          <section className="p-4 border-b border-border">
            <div className="text-xs font-medium mb-2">Create from storage</div>
            <div className="grid grid-cols-[1fr_auto_auto] gap-2">
              <input
                value={draftID}
                onChange={(e) => setDraftID(e.target.value)}
                placeholder="storage file id"
                className="bg-bg-input border border-border rounded px-2 py-1 text-xs min-w-0"
              />
              <input
                value={draftWidth}
                onChange={(e) => setDraftWidth(e.target.value)}
                title="Width"
                className="bg-bg-input border border-border rounded px-2 py-1 text-xs w-16"
              />
              <input
                value={draftHeight}
                onChange={(e) => setDraftHeight(e.target.value)}
                title="Height"
                className="bg-bg-input border border-border rounded px-2 py-1 text-xs w-16"
              />
            </div>
            <input
              value={draftTitle}
              onChange={(e) => setDraftTitle(e.target.value)}
              placeholder="title (optional)"
              className="mt-2 w-full bg-bg-input border border-border rounded px-2 py-1 text-xs"
            />
            <button
              type="button"
              onClick={() => createEmbed()}
              className="mt-2 w-full px-2.5 py-1.5 text-xs bg-accent text-bg rounded"
            >Create embed</button>
          </section>

          <section className="p-4 border-b border-border">
            <div className="flex items-center gap-2 mb-2">
              <div className="text-xs font-medium">Storage files</div>
              <input
                value={q}
                onChange={(e) => setQ(e.target.value)}
                placeholder="search"
                className="ml-auto bg-bg-input border border-border rounded px-2 py-1 text-xs w-32"
              />
            </div>
            <div className="max-h-56 overflow-auto border border-border rounded">
              {files.length === 0 ? (
                <div className="p-3 text-xs text-text-muted">No files found.</div>
              ) : files.map((f) => (
                <div key={f.id} className="px-3 py-2 border-b border-border last:border-b-0 flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => createEmbed(f)}
                    className="px-2 py-0.5 text-[11px] border border-accent text-accent rounded hover:bg-accent hover:text-bg"
                  >Create</button>
                  <div className="min-w-0 flex-1">
                    <div className="text-xs truncate" title={f.name}>{f.name}</div>
                    <div className="text-[11px] text-text-muted truncate">
                      #{f.id} · {f.folder || "/"} · {kindLabel(f.content_type)} · {formatSize(f.size_bytes)}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </section>

          <section className="flex-1 min-h-0 overflow-auto">
            {embeds.length === 0 ? (
              <div className="p-4 text-xs text-text-muted">No embeds yet.</div>
            ) : embeds.map((row) => (
              <button
                key={row.embed.id}
                type="button"
                onClick={() => setSelected(row)}
                className={`w-full text-left px-4 py-3 border-b border-border hover:bg-bg-input/40 ${selected?.embed.id === row.embed.id ? "bg-bg-input/60" : ""}`}
              >
                <div className="text-xs font-medium truncate">{row.embed.title || row.embed.name}</div>
                <div className="text-[11px] text-text-muted truncate">
                  storage #{row.embed.storage_file_id} · {kindLabel(row.embed.content_type)}
                </div>
              </button>
            ))}
          </section>
        </aside>

        <main className="min-h-0 overflow-auto p-4">
          {!selected ? (
            <div className="text-sm text-text-muted text-center mt-12">Select or create an embed.</div>
          ) : (
            <EmbedDetails
              row={selected}
              onCopy={copy}
              onRefresh={() => refreshEmbed(selected)}
              onDelete={() => deleteEmbed(selected)}
            />
          )}
        </main>
      </div>
    </div>
  );
}

function EmbedDetails({
  row,
  onCopy,
  onRefresh,
  onDelete,
}: {
  row: EmbedEnvelope;
  onCopy: (label: string, value: string) => void;
  onRefresh: () => void;
  onDelete: () => void;
}) {
  const e = row.embed;
  return (
    <div className="max-w-4xl mx-auto">
      <div className="flex items-start gap-3 mb-4">
        <div className="min-w-0 flex-1">
          <h2 className="text-lg font-semibold truncate">{e.title || e.name}</h2>
          <div className="text-xs text-text-muted">
            storage #{e.storage_file_id} · embed #{e.id} · {kindLabel(e.content_type)} · {formatSize(e.size_bytes)}
          </div>
        </div>
        <button type="button" onClick={onRefresh} className="px-2.5 py-1 text-xs border border-border rounded hover:bg-bg-input">Refresh</button>
        <button type="button" onClick={onDelete} className="px-2.5 py-1 text-xs border border-red text-red rounded hover:bg-red/10">Delete</button>
      </div>

      <div className="border border-border rounded overflow-hidden mb-4 bg-black" style={{ aspectRatio: `${Math.max(e.width, 1)} / ${Math.max(e.height, 1)}` }}>
        <iframe src={row.viewer_url} title={e.title || e.name} className="w-full h-full border-0" allow="fullscreen; picture-in-picture" />
      </div>

      <div className="grid gap-3">
        <CopyField label="Viewer URL" value={row.viewer_url} onCopy={onCopy} />
        <CopyField label="oEmbed URL" value={row.oembed_url} onCopy={onCopy} />
        <CopyField label="Iframe HTML" value={row.html} onCopy={onCopy} multiline />
      </div>
    </div>
  );
}

function CopyField({
  label,
  value,
  multiline,
  onCopy,
}: {
  label: string;
  value: string;
  multiline?: boolean;
  onCopy: (label: string, value: string) => void;
}) {
  return (
    <div>
      <div className="flex items-center gap-2 mb-1">
        <label className="text-xs text-text-muted">{label}</label>
        <button
          type="button"
          onClick={() => onCopy(label, value)}
          className="ml-auto px-2 py-0.5 text-[11px] border border-border rounded hover:bg-bg-input"
        >Copy</button>
      </div>
      {multiline ? (
        <textarea readOnly value={value} className="w-full h-20 bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono resize-none" />
      ) : (
        <input readOnly value={value} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono" />
      )}
    </div>
  );
}

function kindLabel(contentType: string): string {
  if (!contentType) return "file";
  if (contentType.startsWith("video/")) return "video";
  if (contentType.startsWith("audio/")) return "audio";
  if (contentType.startsWith("image/")) return "image";
  if (contentType === "application/pdf") return "pdf";
  return contentType;
}

function formatSize(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}
