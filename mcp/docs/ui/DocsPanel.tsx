// DocsPanel — dashboard surface for the docs app. Three views in
// one panel:
//
//   • Templates — list + editor + live preview
//   • Render    — pick a template + fill a JSON data form + render
//   • Renders   — audit log of past renders (read-only)
//
// All routes go through /api/apps/docs/* (the platform proxy injects
// the per-install bearer token). Inherits the dashboard's Tailwind
// theme tokens.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Template {
  id: number;
  slug: string;
  name: string;
  description?: string;
  body?: string;
  source_format?: string;
  output_format?: string;
  default_folder?: string;
  updated_at?: string;
}

interface RenderRow {
  id: number;
  template_id: number;
  template_slug: string;
  output_file_id: string;
  output_name?: string;
  output_folder?: string;
  data?: unknown;
  rendered_by?: string;
  rendered_at: string;
  bytes?: number;
}

const API = "/api/apps/docs";
type View = "templates" | "render" | "renders";

export default function DocsPanel({ projectId, installId }: NativePanelProps) {
  const [view, setView] = useState<View>("templates");
  const [templates, setTemplates] = useState<Template[]>([]);
  const [selected, setSelected] = useState<Template | null>(null);
  const [renders, setRenders] = useState<RenderRow[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const withParams = useCallback(
    (extra: Record<string, string> = {}) => {
      const u = new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      });
      return u.toString();
    },
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
      const init: RequestInit = {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : undefined,
      };
      if (body !== undefined) init.body = JSON.stringify(body);
      const res = await fetch(`${API}${path}?${withParams()}`, init);
      if (!res.ok) {
        throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      }
      return (await res.json()) as T;
    },
    [withParams],
  );

  const loadTemplates = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await api<{ templates: Template[] }>("GET", "/templates");
      setTemplates(data.templates || []);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [api]);

  const loadRenders = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await api<{ renders: RenderRow[] }>("GET", "/renders");
      setRenders(data.renders || []);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [api]);

  useEffect(() => {
    if (view === "templates" || view === "render") loadTemplates();
    if (view === "renders") loadRenders();
  }, [view, loadTemplates, loadRenders]);

  const tabs: { key: View; label: string }[] = [
    { key: "templates", label: "Templates" },
    { key: "render", label: "Render" },
    { key: "renders", label: "Audit log" },
  ];

  return (
    <div className="h-full flex flex-col p-6 gap-4">
      <div className="flex items-center gap-2 border-b border-border">
        {tabs.map((t) => (
          <button
            key={t.key}
            type="button"
            onClick={() => setView(t.key)}
            className={`px-3 py-2 text-sm border-b-2 transition-colors ${
              view === t.key
                ? "border-accent text-accent"
                : "border-transparent text-text-muted hover:text-text"
            }`}
          >
            {t.label}
          </button>
        ))}
        <div className="flex-1" />
        <button
          type="button"
          onClick={() => (view === "renders" ? loadRenders() : loadTemplates())}
          className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
        >
          Refresh
        </button>
      </div>

      {error && (
        <div className="px-3 py-2 text-sm text-red bg-red/10 border border-red/40 rounded">
          {error}
        </div>
      )}

      {view === "templates" && (
        <TemplatesView
          templates={templates}
          selected={selected}
          onSelect={setSelected}
          onSave={async (t) => {
            if (t.id) {
              await api("PATCH", `/templates/${t.id}`, t);
            } else {
              await api("POST", `/templates`, t);
            }
            await loadTemplates();
          }}
          onDelete={async (id) => {
            await api("DELETE", `/templates/${id}`);
            setSelected(null);
            await loadTemplates();
          }}
          api={api}
          loading={loading}
        />
      )}

      {view === "render" && (
        <RenderView templates={templates} api={api} onRendered={loadRenders} />
      )}

      {view === "renders" && (
        <RendersView renders={renders} loading={loading} />
      )}
    </div>
  );
}

// ─── Templates view ────────────────────────────────────────────────

interface TemplatesViewProps {
  templates: Template[];
  selected: Template | null;
  onSelect: (t: Template | null) => void;
  onSave: (t: Template) => Promise<void>;
  onDelete: (id: number) => Promise<void>;
  api: <T>(method: string, path: string, body?: unknown) => Promise<T>;
  loading: boolean;
}

function TemplatesView({
  templates,
  selected,
  onSelect,
  onSave,
  onDelete,
  api,
  loading,
}: TemplatesViewProps) {
  const [editing, setEditing] = useState<Template | null>(null);
  const [previewURL, setPreviewURL] = useState<string | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [previewError, setPreviewError] = useState("");

  // Load body when a template is selected (list view strips body to
  // keep the response light).
  useEffect(() => {
    if (!selected || selected.id === editing?.id) return;
    let alive = true;
    api<{ template: Template }>("GET", `/templates/${selected.id}`)
      .then((d) => {
        if (alive) setEditing(d.template);
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [selected, api, editing?.id]);

  // Clean up old preview blob URLs to avoid leaking memory.
  useEffect(() => {
    return () => {
      if (previewURL) URL.revokeObjectURL(previewURL);
    };
  }, [previewURL]);

  const startNew = () => {
    setEditing({
      id: 0,
      slug: "",
      name: "",
      body: "# New template\n\nReplace with markdown body. Use {{.var}} placeholders.",
    });
    onSelect(null);
  };

  const runPreview = async () => {
    if (!editing) return;
    setPreviewing(true);
    setPreviewError("");
    try {
      const id = editing.id || 0;
      // POST /templates/:id/preview — id 0 hits the same handler
      // with body= override (panel-edit mode).
      const path = id ? `/templates/${id}/preview` : `/templates/0/preview`;
      const res = await api<{ base64: string; content_type: string }>(
        "POST",
        path,
        { data: {}, body: editing.body },
      );
      const url = `data:${res.content_type};base64,${res.base64}`;
      setPreviewURL(url);
    } catch (e) {
      setPreviewError((e as Error).message);
    } finally {
      setPreviewing(false);
    }
  };

  return (
    <div className="flex-1 flex gap-4 min-h-0">
      <div className="w-64 flex flex-col gap-2">
        <button
          type="button"
          onClick={startNew}
          className="px-3 py-2 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
        >
          + New template
        </button>
        <div className="flex-1 overflow-auto border border-border rounded">
          {loading && templates.length === 0 ? (
            <div className="p-3 text-text-muted text-xs text-center">Loading…</div>
          ) : templates.length === 0 ? (
            <div className="p-3 text-text-muted text-xs text-center">
              No templates yet. Create one to get started.
            </div>
          ) : (
            templates.map((t) => (
              <button
                key={t.id}
                type="button"
                onClick={() => onSelect(t)}
                className={`w-full text-left px-3 py-2 text-sm border-b border-border last:border-b-0 ${
                  selected?.id === t.id ? "bg-accent/10 text-accent" : "hover:bg-bg-input/50"
                }`}
              >
                <div className="font-medium truncate">{t.name}</div>
                <div className="text-xs text-text-dim truncate">{t.slug}</div>
              </button>
            ))
          )}
        </div>
      </div>

      <div className="flex-1 flex flex-col gap-3 min-w-0">
        {!editing ? (
          <div className="flex-1 flex items-center justify-center text-text-muted text-sm">
            Select a template on the left, or create a new one.
          </div>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-2">
              <input
                type="text"
                value={editing.slug}
                onChange={(e) => setEditing({ ...editing, slug: e.target.value })}
                placeholder="slug (e.g. invoice)"
                disabled={!!editing.id}
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono disabled:opacity-50"
              />
              <input
                type="text"
                value={editing.name}
                onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                placeholder="display name"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
            <input
              type="text"
              value={editing.description || ""}
              onChange={(e) => setEditing({ ...editing, description: e.target.value })}
              placeholder="description (optional)"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={editing.default_folder || ""}
              onChange={(e) => setEditing({ ...editing, default_folder: e.target.value })}
              placeholder="default output folder (e.g. /invoices/)"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            />
            <textarea
              value={editing.body || ""}
              onChange={(e) => setEditing({ ...editing, body: e.target.value })}
              placeholder="# Markdown body with {{.placeholders}}"
              className="flex-1 bg-bg-input border border-border rounded p-3 text-xs font-mono min-h-[18rem]"
              spellCheck={false}
            />
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={runPreview}
                disabled={previewing}
                className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
              >
                {previewing ? "Rendering…" : "Preview"}
              </button>
              <button
                type="button"
                onClick={async () => {
                  if (!editing.slug || !editing.name || !editing.body) {
                    setPreviewError("slug, name, body required");
                    return;
                  }
                  await onSave(editing);
                  setPreviewError("");
                }}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
              >
                Save
              </button>
              {editing.id ? (
                <button
                  type="button"
                  onClick={async () => {
                    if (window.confirm("Delete this template? Past renders are kept.")) {
                      await onDelete(editing.id);
                      setEditing(null);
                    }
                  }}
                  className="px-3 py-1 text-sm border border-red/40 text-red rounded hover:bg-red/10"
                >
                  Delete
                </button>
              ) : null}
              <div className="flex-1" />
              {previewError && (
                <span className="text-xs text-red truncate">{previewError}</span>
              )}
            </div>
            {previewURL && (
              <iframe
                title="preview"
                src={previewURL}
                className="w-full border border-border rounded"
                style={{ height: "30rem" }}
              />
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ─── Render view ───────────────────────────────────────────────────

function RenderView({
  templates,
  api,
  onRendered,
}: {
  templates: Template[];
  api: <T>(method: string, path: string, body?: unknown) => Promise<T>;
  onRendered: () => void;
}) {
  const [templateID, setTemplateID] = useState<number>(0);
  const [dataJSON, setDataJSON] = useState('{\n  "customer": { "name": "Acme" }\n}');
  const [outputName, setOutputName] = useState("");
  const [outputFolder, setOutputFolder] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ url?: string; file_id?: number } | null>(null);
  const [error, setError] = useState("");

  const handleRender = async () => {
    setBusy(true);
    setError("");
    setResult(null);
    try {
      let data: unknown;
      try {
        data = JSON.parse(dataJSON);
      } catch (e) {
        throw new Error("Data isn't valid JSON: " + (e as Error).message);
      }
      const out = await api<{ file_id: number; url: string }>(
        "POST",
        `/templates/${templateID}/render`,
        {
          data,
          output_name: outputName || undefined,
          output_folder: outputFolder || undefined,
        },
      );
      setResult(out);
      onRendered();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex-1 flex flex-col gap-3 max-w-3xl">
      <label className="text-xs text-text-dim">Template</label>
      <select
        value={templateID}
        onChange={(e) => setTemplateID(Number(e.target.value))}
        className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
      >
        <option value={0}>— pick one —</option>
        {templates.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name} ({t.slug})
          </option>
        ))}
      </select>

      <label className="text-xs text-text-dim mt-2">Data (JSON)</label>
      <textarea
        value={dataJSON}
        onChange={(e) => setDataJSON(e.target.value)}
        className="bg-bg-input border border-border rounded p-2 text-xs font-mono min-h-[12rem]"
        spellCheck={false}
      />

      <div className="grid grid-cols-2 gap-2">
        <input
          type="text"
          value={outputName}
          onChange={(e) => setOutputName(e.target.value)}
          placeholder="output filename (optional)"
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
        />
        <input
          type="text"
          value={outputFolder}
          onChange={(e) => setOutputFolder(e.target.value)}
          placeholder="output folder (optional)"
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
        />
      </div>

      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={handleRender}
          disabled={busy || !templateID}
          className="px-4 py-2 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >
          {busy ? "Rendering…" : "Render"}
        </button>
        {error && <span className="text-xs text-red">{error}</span>}
      </div>

      {result?.url && (
        <div className="border border-green/40 bg-green/5 rounded p-3 flex flex-col gap-2">
          <span className="text-sm text-green">Rendered ✓</span>
          <a
            href={result.url}
            target="_blank"
            rel="noopener"
            className="text-xs text-accent hover:underline break-all"
          >
            {result.url}
          </a>
        </div>
      )}
    </div>
  );
}

// ─── Audit log view ────────────────────────────────────────────────

function RendersView({
  renders,
  loading,
}: {
  renders: RenderRow[];
  loading: boolean;
}) {
  const formatBytes = (n?: number) => {
    if (!n) return "—";
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
    return `${(n / 1024 / 1024).toFixed(2)} MB`;
  };

  if (loading && renders.length === 0) {
    return <div className="text-text-muted text-sm text-center mt-12">Loading…</div>;
  }
  if (renders.length === 0) {
    return (
      <div className="text-text-muted text-sm text-center mt-12">
        No renders yet. Use the Render tab to produce one.
      </div>
    );
  }
  return (
    <div className="flex-1 overflow-auto border border-border rounded">
      <table className="w-full text-sm">
        <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
          <tr>
            <th className="text-left px-4 py-2 font-normal">Template</th>
            <th className="text-left px-4 py-2 font-normal">File</th>
            <th className="text-left px-4 py-2 font-normal w-24">Size</th>
            <th className="text-left px-4 py-2 font-normal w-48">Rendered at</th>
          </tr>
        </thead>
        <tbody>
          {renders.map((r) => (
            <tr key={r.id} className="border-t border-border hover:bg-bg-input/30">
              <td className="px-4 py-2">
                <div className="text-text font-medium">{r.template_slug}</div>
                <div className="text-xs text-text-dim">id {r.template_id}</div>
              </td>
              <td className="px-4 py-2">
                <div className="text-text truncate max-w-md" title={r.output_name}>
                  {r.output_name || "—"}
                </div>
                <div className="text-xs text-text-dim font-mono">
                  storage:{r.output_file_id}
                </div>
              </td>
              <td className="px-4 py-2 text-text-muted">{formatBytes(r.bytes)}</td>
              <td className="px-4 py-2 text-text-muted text-xs">
                {new Date(r.rendered_at).toLocaleString()}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// silence unused-var warnings for hooks we don't need in v0.1
const _unused = useMemo;
const _unusedRef = useRef;
void _unused;
void _unusedRef;
