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

import { useCallback, useEffect, useMemo, useState } from "react";

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

interface RenderResult {
  file_id: number;
  url: string;
  warnings?: string[];
  page_size?: string;
}

const API = "/api/apps/docs";
type View = "templates" | "render" | "renders";

interface TemplateVar {
  path: string;
  kind: "value" | "list" | "object_list";
  children: string[];
}

const BLANK_STARTER = `# New template

Replace this with your markdown. Use {{.variable}} placeholders.`;

const LEAD_MAGNET_STARTER = `# {{.title}}

## A practical guide for {{.audience}}

{{.introduction}}

---

## What you'll learn

{{range .takeaways}}- {{.}}
{{end}}

## The framework

{{range .steps}}### {{.title}}

{{.description}}

{{end}}

---

## Your next step

{{.call_to_action}}

**{{.brand_name}}** · {{.website}}`;

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
              await api("PATCH", `/templates/${t.id}`, {
                name: t.name,
                description: t.description || "",
                body: t.body || "",
                default_folder: t.default_folder || "",
              });
              const saved = await api<{ template: Template }>("GET", `/templates/${t.id}`);
              await loadTemplates();
              setSelected(saved.template);
              return saved.template;
            } else {
              const saved = await api<{ template: Template }>("POST", `/templates`, {
                slug: t.slug,
                name: t.name,
                description: t.description || "",
                body: t.body || "",
                default_folder: t.default_folder || "",
              });
              await loadTemplates();
              setSelected(saved.template);
              return saved.template;
            }
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
        <RendersView
          renders={renders}
          loading={loading}
          onRefresh={loadRenders}
          projectId={projectId}
        />
      )}
    </div>
  );
}

// ─── Templates view ────────────────────────────────────────────────

interface TemplatesViewProps {
  templates: Template[];
  selected: Template | null;
  onSelect: (t: Template | null) => void;
  onSave: (t: Template) => Promise<Template>;
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
  const [saving, setSaving] = useState(false);
  const [previewError, setPreviewError] = useState("");
  const [pageSize, setPageSize] = useState("A4");
  const templateVars = useMemo(
    () => extractTemplateVariables(editing?.body || ""),
    [editing?.body],
  );
  const sampleData = useMemo(
    () => sampleDataForVariables(templateVars),
    [templateVars],
  );

  // Load body when a template is selected (list view strips body to
  // keep the response light).
  useEffect(() => {
    if (!selected || selected.id === editing?.id) return;
    let alive = true;
    setPreviewError("");
    setPreviewURL(null);
    api<{ template: Template }>("GET", `/templates/${selected.id}`)
      .then((d) => {
        if (alive) setEditing(d.template);
      })
      .catch((e) => {
        if (alive) setPreviewError(`Could not load template: ${(e as Error).message}`);
      });
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

  const startNew = (starter: "blank" | "lead-magnet" = "blank") => {
    setEditing({
      id: 0,
      slug: "",
      name: "",
      body: starter === "lead-magnet" ? LEAD_MAGNET_STARTER : BLANK_STARTER,
    });
    setPreviewURL(null);
    setPreviewError("");
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
        { data: sampleData, body: editing.body, page_size: pageSize },
      );
      const bytes = Uint8Array.from(atob(res.base64), (char) => char.charCodeAt(0));
      const url = URL.createObjectURL(new Blob([bytes], { type: res.content_type }));
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
        <div className="grid grid-cols-2 gap-2">
          <button
            type="button"
            onClick={() => startNew("blank")}
            className="px-2 py-2 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            + Blank
          </button>
          <button
            type="button"
            onClick={() => startNew("lead-magnet")}
            className="px-2 py-2 text-xs border border-border rounded hover:bg-bg-input"
          >
            Lead magnet
          </button>
        </div>
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
            <VariablesPanel variables={templateVars} sampleData={sampleData} compact />
            <textarea
              value={editing.body || ""}
              onChange={(e) => setEditing({ ...editing, body: e.target.value })}
              placeholder="# Markdown body with {{.placeholders}}"
              className="flex-1 bg-bg-input border border-border rounded p-3 text-xs font-mono min-h-[18rem]"
              spellCheck={false}
            />
            <div className="flex items-center gap-2">
              <select
                aria-label="Preview page size"
                value={pageSize}
                onChange={(e) => setPageSize(e.target.value)}
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              >
                <option value="A4">A4</option>
                <option value="letter">Letter</option>
                <option value="legal">Legal</option>
              </select>
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
                  setSaving(true);
                  setPreviewError("");
                  try {
                    const saved = await onSave(editing);
                    setEditing(saved);
                  } catch (e) {
                    setPreviewError((e as Error).message);
                  } finally {
                    setSaving(false);
                  }
                }}
                disabled={saving}
                className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
              >
                {saving ? "Saving…" : "Save"}
              </button>
              {editing.id ? (
                <button
                  type="button"
                  onClick={async () => {
                    if (window.confirm("Delete this template? Past renders are kept.")) {
                      try {
                        await onDelete(editing.id);
                        setEditing(null);
                        setPreviewURL(null);
                      } catch (e) {
                        setPreviewError((e as Error).message);
                      }
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
  const [pageSize, setPageSize] = useState("A4");
  const [busy, setBusy] = useState(false);
  const [loadingTemplate, setLoadingTemplate] = useState(false);
  const [templateDetail, setTemplateDetail] = useState<Template | null>(null);
  const [result, setResult] = useState<RenderResult | null>(null);
  const [error, setError] = useState("");
  const selectedTemplate = useMemo(
    () => templates.find((t) => t.id === templateID) || null,
    [templates, templateID],
  );
  const templateVars = useMemo(
    () => extractTemplateVariables(templateDetail?.body || ""),
    [templateDetail?.body],
  );
  const sampleData = useMemo(
    () => sampleDataForVariables(templateVars),
    [templateVars],
  );

  useEffect(() => {
    if (!selectedTemplate) {
      setTemplateDetail(null);
      return;
    }
    let alive = true;
    setLoadingTemplate(true);
    setResult(null);
    setError("");
    api<{ template: Template }>("GET", `/templates/${selectedTemplate.id}`)
      .then((response) => {
        if (alive) setTemplateDetail(response.template);
      })
      .catch((e) => {
        if (alive) setError(`Could not load template: ${(e as Error).message}`);
      })
      .finally(() => {
        if (alive) setLoadingTemplate(false);
      });
    return () => {
      alive = false;
    };
  }, [selectedTemplate, api]);

  useEffect(() => {
    if (!templateDetail) return;
    setDataJSON(JSON.stringify(sampleData, null, 2));
    setOutputFolder(templateDetail.default_folder || "");
  }, [templateDetail, sampleData]);

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
      if (!data || typeof data !== "object" || Array.isArray(data)) {
        throw new Error("Data must be a JSON object.");
      }
      const out = await api<RenderResult>(
        "POST",
        `/templates/${templateID}/render`,
        {
          data,
          output_name: outputName || undefined,
          output_folder: outputFolder || undefined,
          page_size: pageSize,
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
      <VariablesPanel variables={templateVars} sampleData={sampleData} compact={false} />

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

      <label className="text-xs text-text-dim">Page size</label>
      <select
        value={pageSize}
        onChange={(e) => setPageSize(e.target.value)}
        className="w-48 bg-bg-input border border-border rounded px-2 py-1 text-sm"
      >
        <option value="A4">A4</option>
        <option value="letter">Letter</option>
        <option value="legal">Legal</option>
      </select>

      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={handleRender}
          disabled={busy || loadingTemplate || !templateID || !templateDetail}
          className="px-4 py-2 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >
          {busy ? "Rendering…" : loadingTemplate ? "Loading template…" : "Render PDF"}
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
          {result.warnings && result.warnings.length > 0 && (
            <div className="text-xs text-amber-600">
              Rendered with {result.warnings.length} warning
              {result.warnings.length === 1 ? "" : "s"}:
              <ul className="list-disc ml-5 mt-1">
                {result.warnings.map((warning, index) => (
                  <li key={`${index}-${warning}`}>{warning}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ─── Template variable helpers ─────────────────────────────────────

function extractTemplateVariables(body: string): TemplateVar[] {
  const vars = new Map<string, TemplateVar>();
  const rangeStack: string[] = [];
  const actions = body.matchAll(/{{\s*([^{}]+?)\s*}}/g);

  const upsert = (path: string, kind: TemplateVar["kind"], child = "") => {
    if (!path) return;
    const prev = vars.get(path);
    if (!prev) {
      vars.set(path, { path, kind, children: child ? [child] : [] });
      return;
    }
    if (prev.kind === "value" && kind !== "value") prev.kind = kind;
    if (prev.kind === "list" && kind === "object_list") prev.kind = kind;
    if (child && !prev.children.includes(child)) prev.children.push(child);
  };

  for (const m of actions) {
    let action = m[1].trim();
    if (action.startsWith("/*") || action === "end" || action.startsWith("else")) {
      if (action === "end") rangeStack.pop();
      continue;
    }
    if (action.startsWith("range ")) {
      const path = cleanTemplatePath(action.replace(/^range\s+/, ""));
      upsert(path, "list");
      rangeStack.push(path);
      continue;
    }
    if (action.startsWith("if ") || action.startsWith("with ")) {
      action = action.replace(/^(if|with)\s+/, "");
    }
    const currentRange = rangeStack[rangeStack.length - 1] || "";
    if (currentRange && action === ".") {
      upsert(currentRange, "list");
      continue;
    }
    if (currentRange && /^\.[A-Za-z0-9_]+$/.test(action)) {
      upsert(currentRange, "object_list", action.slice(1));
      continue;
    }
    const path = cleanTemplatePath(action);
    if (path) upsert(path, "value");
  }

  return Array.from(vars.values()).sort((a, b) => a.path.localeCompare(b.path));
}

function cleanTemplatePath(action: string): string {
  const first = action.trim().split(/\s+/)[0] || "";
  if (!first.startsWith(".")) return "";
  return first
    .slice(1)
    .replace(/[|,)]*$/g, "")
    .split(".")
    .filter(Boolean)
    .join(".");
}

function sampleDataForVariables(vars: TemplateVar[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const v of vars) {
    if (v.kind === "object_list") {
      setPath(out, v.path, [
        Object.fromEntries(v.children.map((child) => [child, sampleValue(child)])),
      ]);
      continue;
    }
    if (v.kind === "list") {
      setPath(out, v.path, ["Sample item"]);
      continue;
    }
    setPath(out, v.path, sampleValue(v.path.split(".").at(-1) || v.path));
  }
  return out;
}

function setPath(target: Record<string, unknown>, path: string, value: unknown) {
  const parts = path.split(".").filter(Boolean);
  let cur: Record<string, unknown> = target;
  parts.forEach((part, idx) => {
    if (idx === parts.length - 1) {
      cur[part] = value;
      return;
    }
    if (!cur[part] || typeof cur[part] !== "object" || Array.isArray(cur[part])) {
      cur[part] = {};
    }
    cur = cur[part] as Record<string, unknown>;
  });
}

function sampleValue(name: string): string {
  const key = name.toLowerCase();
  if (key.includes("email")) return "hello@example.com";
  if (key.includes("website")) return "https://example.com";
  if (key.includes("date")) return new Date().toISOString().slice(0, 10);
  if (key.includes("duration")) return "6 weeks";
  if (key.includes("amount") || key.includes("total")) return "$10,000";
  if (key === "qty" || key.includes("quantity")) return "1";
  if (key.includes("name")) return "Sample name";
  if (key.includes("contact")) return "Sample contact";
  if (key.includes("title") || key.includes("phase")) return "Sample title";
  if (key.includes("folder")) return "/docs/";
  return "Sample " + name.replace(/_/g, " ");
}

function VariablesPanel({
  variables,
  sampleData,
  compact,
}: {
  variables: TemplateVar[];
  sampleData: Record<string, unknown>;
  compact: boolean;
}) {
  if (variables.length === 0) {
    return (
      <div className="border border-border rounded px-3 py-2 text-xs text-text-dim">
        No template variables detected.
      </div>
    );
  }
  return (
    <div className="border border-border rounded bg-bg-input/30 p-3">
      <div className="flex items-center justify-between gap-2 mb-2">
        <div className="text-xs uppercase tracking-wide text-text-dim">Variables</div>
        <div className="text-xs text-text-dim">{variables.length}</div>
      </div>
      <div className="flex flex-wrap gap-1.5 mb-2">
        {variables.map((v) => (
          <span
            key={v.path}
            className="px-2 py-0.5 rounded border border-border text-xs font-mono text-text-muted"
            title={v.kind === "object_list" ? v.children.join(", ") : v.kind}
          >
            {v.kind === "object_list" ? `${v.path}[]` : v.kind === "list" ? `${v.path}[]` : v.path}
          </span>
        ))}
      </div>
      {!compact && (
        <pre className="max-h-48 overflow-auto text-xs font-mono text-text-muted whitespace-pre-wrap">
          {JSON.stringify(sampleData, null, 2)}
        </pre>
      )}
    </div>
  );
}

// ─── Audit log view ────────────────────────────────────────────────

function RendersView({
  renders,
  loading,
  onRefresh,
  projectId,
}: {
  renders: RenderRow[];
  loading: boolean;
  onRefresh: () => void;
  projectId: string;
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
      <div className="flex-1 flex flex-col items-center justify-center gap-3 text-text-muted text-sm">
        <div>No renders yet. Use the Render tab to produce one.</div>
        <button
          type="button"
          onClick={onRefresh}
          className="px-3 py-1 text-xs border border-border rounded hover:bg-bg-input"
        >
          Refresh audit log
        </button>
      </div>
    );
  }
  return (
    <div className="flex-1 flex flex-col min-h-0 gap-2">
      <div className="flex items-center justify-between text-xs text-text-dim">
        <span>
          {renders.length} render{renders.length === 1 ? "" : "s"}
        </span>
        <button
          type="button"
          onClick={onRefresh}
          className="px-2 py-1 border border-border rounded hover:bg-bg-input"
        >
          Refresh
        </button>
      </div>
      <div className="flex-1 overflow-auto border border-border rounded">
        <table className="w-full text-sm">
          <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
            <tr>
              <th className="text-left px-4 py-2 font-normal">Template</th>
              <th className="text-left px-4 py-2 font-normal">File</th>
              <th className="text-left px-4 py-2 font-normal w-24">Size</th>
              <th className="text-left px-4 py-2 font-normal w-48">Rendered at</th>
              <th className="text-right px-4 py-2 font-normal w-24">Open</th>
            </tr>
          </thead>
          <tbody>
            {renders.map((r) => (
              <tr key={r.id} className="border-t border-border hover:bg-bg-input/30">
                <td className="px-4 py-2">
                  <div className="text-text font-medium">{r.template_slug}</div>
                  <div className="text-xs text-text-dim">render #{r.id}</div>
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
                <td className="px-4 py-2 text-right">
                  {r.output_file_id ? (
                    <a
                      href={storageContentURL(r, projectId)}
                      target="_blank"
                      rel="noopener"
                      className="text-xs text-accent hover:underline"
                    >
                      View PDF
                    </a>
                  ) : (
                    <span className="text-xs text-text-dim">—</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function storageContentURL(r: RenderRow, projectId?: string): string {
  const name = r.output_name || `render-${r.id}.pdf`;
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : "";
  return `/api/apps/storage/files/${encodeURIComponent(r.output_file_id)}/content/${encodeURIComponent(name)}${q}`;
}
