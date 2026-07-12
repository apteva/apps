import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

const API = "/api/apps/fetch";
const SECRET_MASK = "********";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface SavedRequest {
  id: number;
  slug: string;
  name: string;
  description?: string;
  method: string;
  url_template: string;
  headers?: Record<string, unknown>;
  query?: Record<string, unknown>;
  body?: Record<string, unknown>;
  environment_id?: number;
  updated_at?: string;
}

interface EnvironmentVar {
  key: string;
  value?: string;
  is_secret: boolean;
  has_value?: boolean;
}

interface Environment {
  id: number;
  slug: string;
  name: string;
  vars?: EnvironmentVar[];
}

interface FetchResult {
  status?: number;
  status_text?: string;
  headers?: Record<string, string>;
  body_text?: string;
  body_json?: unknown;
  body_base64?: string;
  body_truncated?: boolean;
  body_bytes?: number;
  content_type?: string;
  duration_ms?: number;
  final_url?: string;
  history_id?: number;
}

interface HistoryRow {
  id: number;
  saved_request_id?: number;
  source?: string;
  method: string;
  url: string;
  status?: number;
  duration_ms?: number;
  error?: string;
  created_at: string;
  redacted_request?: unknown;
  redacted_response?: unknown;
}

interface Pair {
  id: number;
  key: string;
  value: string;
}

type Tab = "requests" | "history" | "environments";
type RequestPane = "saved" | "editor" | "response";
type BodyMode = "none" | "json" | "text" | "form" | "base64";

interface Draft {
  name: string;
  method: string;
  url: string;
  headers: Pair[];
  query: Pair[];
  bodyMode: BodyMode;
  bodyText: string;
  environmentId: string;
}

class APIError extends Error {
  payload: any;
  constructor(message: string, payload: any) {
    super(message);
    this.payload = payload;
  }
}

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
let pairSequence = 0;
const pair = (key = "", value = ""): Pair => ({ id: ++pairSequence, key, value });
const emptyDraft = (): Draft => ({
  name: "",
  method: "GET",
  url: "",
  headers: [pair()],
  query: [pair()],
  bodyMode: "none",
  bodyText: "",
  environmentId: "",
});

export default function FetchPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("requests");
  const [requestPane, setRequestPane] = useState<RequestPane>("editor");
  const [requests, setRequests] = useState<SavedRequest[]>([]);
  const [envs, setEnvs] = useState<Environment[]>([]);
  const [history, setHistory] = useState<HistoryRow[]>([]);
  const [historyHasMore, setHistoryHasMore] = useState(false);
  const [historyDetail, setHistoryDetail] = useState<HistoryRow | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [draft, setDraft] = useState<Draft>(() => emptyDraft());
  const [baseline, setBaseline] = useState("");
  const [result, setResult] = useState<FetchResult | null>(null);
  const [running, setRunning] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [envEditing, setEnvEditing] = useState<Environment | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const selected = useMemo(
    () => requests.find((request) => request.id === selectedId) || null,
    [requests, selectedId],
  );
  const dirty = useMemo(() => fingerprint(draft) !== baseline, [draft, baseline]);

  const api = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const sep = path.includes("?") ? "&" : "?";
    const response = await fetch(`${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
      credentials: "include",
      headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
      ...init,
    });
    const text = await response.text();
    let data: any = null;
    if (text) {
      try { data = JSON.parse(text); }
      catch { data = { error: text }; }
    }
    if (!response.ok || (data && typeof data === "object" && "error" in data)) {
      throw new APIError(data?.error || response.statusText, data);
    }
    return data as T;
  }, [projectId]);

  const loadRequests = useCallback(async () => {
    const data = await api<{ requests: SavedRequest[] }>("/requests");
    setRequests(data.requests || []);
    return data.requests || [];
  }, [api]);

  const loadEnvironments = useCallback(async () => {
    const data = await api<{ environments: Environment[] }>("/environments");
    setEnvs(data.environments || []);
  }, [api]);

  const loadHistory = useCallback(async (reset = true) => {
    const offset = reset ? 0 : history.length;
    const data = await api<{ history: HistoryRow[]; has_more: boolean }>(`/history?limit=50&offset=${offset}`);
    setHistory((current) => reset ? (data.history || []) : [...current, ...(data.history || [])]);
    setHistoryHasMore(Boolean(data.has_more));
  }, [api, history.length]);

  useEffect(() => {
    Promise.all([loadRequests(), loadEnvironments(), loadHistory(true)]).catch((cause) => setError((cause as Error).message));
  }, [loadRequests, loadEnvironments]);

  useEffect(() => {
    if (!selected) return;
    const next = draftFromSaved(selected);
    setDraft(next);
    setBaseline(fingerprint(next));
    setResult(null);
    setError("");
  }, [selected]);

  const payloadFromDraft = useCallback(() => {
    const validation = validateDraft(draft);
    if (validation) throw new Error(validation);
    return {
      method: draft.method,
      url: draft.url.trim(),
      headers: pairsToObject(draft.headers),
      query: pairsToObject(draft.query),
      body: bodyFromDraft(draft),
      environment_id: draft.environmentId ? Number(draft.environmentId) : undefined,
      save_history: true,
    };
  }, [draft]);

  const run = async () => {
    setRunning(true);
    setError("");
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const out = await api<FetchResult>("/execute", {
        method: "POST",
        body: JSON.stringify(payloadFromDraft()),
        signal: controller.signal,
      });
      setResult(out);
      setRequestPane("response");
      await loadHistory(true);
    } catch (cause) {
      if ((cause as Error).name === "AbortError") {
        setError("Request cancelled");
      } else {
        const appError = cause as APIError;
        if (appError.payload?.result) {
          setResult(appError.payload.result as FetchResult);
          setRequestPane("response");
        }
        setError(appError.message);
        await loadHistory(true).catch(() => undefined);
      }
    } finally {
      abortRef.current = null;
      setRunning(false);
    }
  };

  const save = async (duplicate = false) => {
    setSaving(true);
    setError("");
    try {
      const request = payloadFromDraft();
      const payload = {
        name: duplicate ? `${draft.name || defaultRequestName(draft)} copy` : (draft.name || defaultRequestName(draft)),
        method: request.method,
        url_template: request.url,
        headers: request.headers,
        query: request.query,
        body: request.body,
        environment_id: request.environment_id,
      };
      if (selectedId && !duplicate) {
        const data = await api<{ request: SavedRequest }>(`/requests/${selectedId}`, {
          method: "PATCH",
          body: JSON.stringify(payload),
        });
        const next = draftFromSaved(data.request);
        setDraft(next);
        setBaseline(fingerprint(next));
      } else {
        const data = await api<{ request: SavedRequest }>("/requests", {
          method: "POST",
          body: JSON.stringify(payload),
        });
        setSelectedId(data.request.id);
      }
      await loadRequests();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const archive = async () => {
    if (!selectedId || !confirm("Archive this request?")) return;
    try {
      await api(`/requests/${selectedId}`, { method: "DELETE" });
      setSelectedId(null);
      const next = emptyDraft();
      setDraft(next);
      setBaseline(fingerprint(next));
      setResult(null);
      await loadRequests();
    } catch (cause) {
      setError((cause as Error).message);
    }
  };

  const selectHistory = async (row: HistoryRow) => {
    setHistoryDetail(row);
    try {
      const data = await api<{ entry: HistoryRow }>(`/history/${row.id}`);
      setHistoryDetail(data.entry);
    } catch (cause) {
      setError((cause as Error).message);
    }
  };

  const clearHistory = async () => {
    if (!confirm("Clear request history for this project?")) return;
    try {
      await api("/history", { method: "DELETE" });
      setHistory([]);
      setHistoryDetail(null);
      setHistoryHasMore(false);
    } catch (cause) {
      setError((cause as Error).message);
    }
  };

  return (
    <div className="h-full min-w-0 flex flex-col bg-bg text-text">
      <header className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <div className="font-medium mr-1">Fetch</div>
        <nav className="flex items-center gap-1">
          <TabButton active={tab === "requests"} onClick={() => setTab("requests")}>Requests</TabButton>
          <TabButton active={tab === "history"} onClick={() => setTab("history")}>History</TabButton>
          <TabButton active={tab === "environments"} onClick={() => setTab("environments")}>Environments</TabButton>
        </nav>
        {error && <span className="w-full xl:w-auto xl:ml-auto text-xs text-red break-words">{error}</span>}
      </header>

      {tab === "requests" && (
        <>
          <div className="xl:hidden flex items-center gap-1 border-b border-border px-3 py-2">
            <PaneButton active={requestPane === "saved"} onClick={() => setRequestPane("saved")}>Saved</PaneButton>
            <PaneButton active={requestPane === "editor"} onClick={() => setRequestPane("editor")}>Editor</PaneButton>
            <PaneButton active={requestPane === "response"} onClick={() => setRequestPane("response")}>Response</PaneButton>
          </div>
          <div className="flex-1 min-h-0 min-w-0 grid grid-cols-1 xl:grid-cols-[260px_minmax(420px,1fr)_minmax(300px,420px)]">
            <aside className={`${paneVisibility(requestPane, "saved")} border-r border-border overflow-auto`}>
              <div className="p-2 border-b border-border">
                <button
                  type="button"
                  onClick={() => {
                    setSelectedId(null);
                    const next = emptyDraft();
                    setDraft(next);
                    setBaseline(fingerprint(next));
                    setResult(null);
                    setRequestPane("editor");
                  }}
                  className="w-full text-sm px-2 py-1.5 border border-border rounded hover:bg-bg-input"
                >
                  New request
                </button>
              </div>
              {requests.map((request) => (
                <button
                  key={request.id}
                  type="button"
                  onClick={() => { setSelectedId(request.id); setRequestPane("editor"); }}
                  className={`w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input ${selectedId === request.id ? "bg-bg-input" : ""}`}
                >
                  <div className="flex items-center gap-2 min-w-0">
                    <MethodBadge method={request.method} />
                    <span className="text-sm truncate">{request.name}</span>
                  </div>
                  <div className="mt-1 text-xs text-text-dim truncate">{request.url_template}</div>
                </button>
              ))}
            </aside>

            <main className={`${paneVisibility(requestPane, "editor")} min-w-0 overflow-auto p-3 xl:p-4 space-y-3`}>
              <div className="grid grid-cols-1 sm:grid-cols-[100px_minmax(0,1fr)] gap-2 items-center">
                <label className="text-sm text-text-muted">Name</label>
                <div className="flex items-center gap-2 min-w-0">
                  <input
                    value={draft.name}
                    onChange={(event) => setDraft({ ...draft, name: event.target.value })}
                    className="min-w-0 flex-1 bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  />
                  {dirty && <span className="text-xs text-warn shrink-0">Unsaved</span>}
                </div>
                <label className="text-sm text-text-muted">Request</label>
                <div className="grid grid-cols-[100px_minmax(0,1fr)] gap-2 min-w-0">
                  <select
                    value={draft.method}
                    onChange={(event) => setDraft({ ...draft, method: event.target.value })}
                    className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  >
                    {METHODS.map((method) => <option key={method} value={method}>{method}</option>)}
                  </select>
                  <input
                    value={draft.url}
                    onChange={(event) => setDraft({ ...draft, url: event.target.value })}
                    placeholder="https://api.example.com/v1/resource"
                    className="min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono"
                  />
                </div>
                <label className="text-sm text-text-muted">Environment</label>
                <select
                  value={draft.environmentId}
                  onChange={(event) => setDraft({ ...draft, environmentId: event.target.value })}
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                >
                  <option value="">None</option>
                  {envs.map((environment) => <option key={environment.id} value={environment.id}>{environment.name}</option>)}
                </select>
              </div>

              <KeyValueEditor
                label="Headers"
                rows={draft.headers}
                onChange={(headers) => setDraft({ ...draft, headers })}
                valueType={(key) => isSensitiveKey(key) ? "password" : "text"}
              />
              <KeyValueEditor label="Query" rows={draft.query} onChange={(query) => setDraft({ ...draft, query })} />
              <BodyEditor draft={draft} onChange={setDraft} />

              <div className="flex flex-wrap items-center gap-2">
                {running ? (
                  <button type="button" onClick={() => abortRef.current?.abort()} className="px-3 py-1.5 text-sm border border-red/50 text-red rounded hover:bg-red/10">
                    Cancel
                  </button>
                ) : (
                  <button type="button" disabled={!draft.url.trim()} onClick={run} className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">
                    Run
                  </button>
                )}
                <button type="button" disabled={saving || !dirty || !draft.url.trim()} onClick={() => save(false)} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
                  {saving ? "Saving" : selectedId ? "Save changes" : "Save"}
                </button>
                {selectedId && (
                  <>
                    <button type="button" disabled={saving} onClick={() => save(true)} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">Duplicate</button>
                    <button type="button" disabled={saving} onClick={archive} className="px-3 py-1.5 text-sm border border-red/50 text-red rounded hover:bg-red/10 disabled:opacity-50">Archive</button>
                  </>
                )}
              </div>
            </main>

            <section className={`${paneVisibility(requestPane, "response")} border-l border-border overflow-auto p-3 xl:p-4`}>
              <h2 className="text-sm font-medium mb-2">Response</h2>
              {result ? <ResponseView result={result} /> : <div className="text-sm text-text-dim">No response</div>}
            </section>
          </div>
        </>
      )}

      {tab === "history" && (
        <HistoryView
          rows={history}
          selected={historyDetail}
          hasMore={historyHasMore}
          onSelect={selectHistory}
          onLoadMore={() => loadHistory(false)}
          onClear={clearHistory}
        />
      )}

      {tab === "environments" && (
        <EnvironmentView
          envs={envs}
          editing={envEditing}
          setEditing={setEnvEditing}
          save={async (environment) => {
            try {
              if (environment.id) {
                await api(`/environments/${environment.id}`, { method: "PATCH", body: JSON.stringify(environment) });
              } else {
                await api("/environments", { method: "POST", body: JSON.stringify(environment) });
              }
              setEnvEditing(null);
              await loadEnvironments();
            } catch (cause) {
              setError((cause as Error).message);
            }
          }}
          remove={async (environment) => {
            if (!confirm(`Delete environment "${environment.name}"?`)) return;
            try {
              await api(`/environments/${environment.id}`, { method: "DELETE" });
              setEnvEditing(null);
              await loadEnvironments();
            } catch (cause) {
              setError((cause as Error).message);
            }
          }}
        />
      )}
    </div>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} className={`px-2 py-1 text-sm border rounded ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:bg-bg-input"}`}>{children}</button>;
}

function PaneButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return <button type="button" onClick={onClick} className={`flex-1 px-2 py-1 text-xs border rounded ${active ? "border-accent text-accent" : "border-border text-text-muted"}`}>{children}</button>;
}

function paneVisibility(active: RequestPane, pane: RequestPane) {
  return active === pane ? "block" : "hidden xl:block";
}

function MethodBadge({ method }: { method: string }) {
  const tone = method === "GET" ? "text-info" : method === "DELETE" ? "text-red" : "text-accent";
  return <span className={`text-[10px] font-semibold ${tone} w-12 shrink-0`}>{method}</span>;
}

function KeyValueEditor({ label, rows, onChange, valueType }: {
  label: string;
  rows: Pair[];
  onChange: (rows: Pair[]) => void;
  valueType?: (key: string) => "text" | "password";
}) {
  const update = (index: number, patch: Partial<Pair>) => onChange(rows.map((row, rowIndex) => rowIndex === index ? { ...row, ...patch } : row));
  return (
    <section>
      <div className="flex items-center justify-between mb-1">
        <div className="text-sm text-text-muted">{label}</div>
        <button type="button" onClick={() => onChange([...rows, pair()])} className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input">Add</button>
      </div>
      <div className="space-y-1.5">
        {rows.map((row, index) => (
          <div key={row.id} className="grid grid-cols-[minmax(90px,1fr)_minmax(120px,2fr)_32px] gap-2 min-w-0">
            <input value={row.key} onChange={(event) => update(index, { key: event.target.value })} placeholder="Key" className="min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono" />
            <input value={row.value} type={valueType?.(row.key) || "text"} onChange={(event) => update(index, { value: event.target.value })} placeholder="Value" className="min-w-0 bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono" />
            <button type="button" title={`Remove ${label.toLowerCase()} row`} onClick={() => onChange(rows.length === 1 ? [pair()] : rows.filter((_, rowIndex) => rowIndex !== index))} className="border border-border rounded hover:bg-bg-input">x</button>
          </div>
        ))}
      </div>
    </section>
  );
}

function BodyEditor({ draft, onChange }: { draft: Draft; onChange: (draft: Draft) => void }) {
  let validation = "";
  if (draft.bodyMode === "json" || draft.bodyMode === "form") {
    try {
      const parsed = draft.bodyText.trim() ? JSON.parse(draft.bodyText) : {};
      if (draft.bodyMode === "form" && (Array.isArray(parsed) || !parsed || typeof parsed !== "object")) validation = "Form body must be a JSON object";
    } catch (cause) {
      validation = (cause as Error).message;
    }
  }
  return (
    <section>
      <div className="flex items-center gap-2 mb-1">
        <label className="text-sm text-text-muted">Body</label>
        <select value={draft.bodyMode} onChange={(event) => onChange({ ...draft, bodyMode: event.target.value as BodyMode, bodyText: "" })} className="ml-auto bg-bg-input border border-border rounded px-2 py-1 text-xs">
          <option value="none">None</option>
          <option value="json">JSON</option>
          <option value="text">Text</option>
          <option value="form">Form</option>
          <option value="base64">Base64</option>
        </select>
      </div>
      {draft.bodyMode !== "none" && (
        <textarea value={draft.bodyText} onChange={(event) => onChange({ ...draft, bodyText: event.target.value })} spellCheck={false} rows={8} className={`w-full bg-bg-input border rounded px-2 py-1.5 text-xs font-mono ${validation ? "border-red" : "border-border"}`} />
      )}
      {validation && <div className="mt-1 text-xs text-red break-words">{validation}</div>}
    </section>
  );
}

function ResponseView({ result }: { result: FetchResult }) {
  const body = result.body_json !== undefined ? pretty(result.body_json) : result.body_text || "";
  const binary = Boolean(result.body_base64);
  return (
    <div className="space-y-3 text-sm min-w-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className={`font-semibold ${statusTone(result.status || 0)}`}>{result.status || "ERR"}</span>
        <span className="text-text-dim">{result.duration_ms ?? 0} ms</span>
        {typeof result.body_bytes === "number" && <span className="text-text-dim">{formatBytes(result.body_bytes)}</span>}
      </div>
      {result.final_url && <div className="text-xs text-text-dim break-all">{result.final_url}</div>}
      {binary ? (
        <div className="border border-border rounded p-3 text-sm text-text-muted">
          Binary response{result.content_type ? ` (${result.content_type})` : ""}; inline base64 preview suppressed.
        </div>
      ) : (
        <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto max-h-[55vh] whitespace-pre-wrap break-words">{body}</pre>
      )}
      {result.body_truncated && <div className="text-xs text-warn">Body truncated at the configured response limit</div>}
      <details>
        <summary className="cursor-pointer text-xs text-text-muted">Headers</summary>
        <pre className="mt-2 bg-bg-input border border-border rounded p-2 text-xs overflow-auto whitespace-pre-wrap break-words">{pretty(result.headers || {})}</pre>
      </details>
    </div>
  );
}

function HistoryView({ rows, selected, hasMore, onSelect, onLoadMore, onClear }: {
  rows: HistoryRow[];
  selected: HistoryRow | null;
  hasMore: boolean;
  onSelect: (row: HistoryRow) => void;
  onLoadMore: () => void;
  onClear: () => void;
}) {
  return (
    <div className="flex-1 min-h-0 min-w-0 grid grid-cols-1 xl:grid-cols-[minmax(420px,1fr)_minmax(320px,520px)]">
      <div className="overflow-auto min-w-0">
        <div className="sticky top-0 z-[1] flex justify-end border-b border-border bg-bg px-3 py-2">
          <button type="button" onClick={onClear} disabled={rows.length === 0} className="text-xs px-2 py-1 border border-red/50 text-red rounded disabled:opacity-50">Clear history</button>
        </div>
        <table className="w-full text-sm table-fixed">
          <thead className="bg-bg border-b border-border">
            <tr className="text-left text-xs text-text-muted">
              <th className="px-3 py-2 w-16">Status</th>
              <th className="px-3 py-2 w-20">Method</th>
              <th className="px-3 py-2">URL</th>
              <th className="px-3 py-2 w-24">Time</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.id} onClick={() => onSelect(row)} className={`border-b border-border hover:bg-bg-input cursor-pointer ${selected?.id === row.id ? "bg-bg-input" : ""}`}>
                <td className={`px-3 py-2 ${statusTone(row.status || 0)}`}>{row.error ? "ERR" : row.status}</td>
                <td className="px-3 py-2"><MethodBadge method={row.method} /></td>
                <td className="px-3 py-2 truncate">{row.url}</td>
                <td className="px-3 py-2 text-text-dim">{row.duration_ms || 0} ms</td>
              </tr>
            ))}
          </tbody>
        </table>
        {hasMore && <button type="button" onClick={onLoadMore} className="m-3 px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Load more</button>}
      </div>
      <aside className="border-t xl:border-t-0 xl:border-l border-border overflow-auto p-4 min-w-0">
        {selected ? (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2"><span className="font-medium">Run #{selected.id}</span><span className="text-xs text-text-dim">{selected.source}</span></div>
            {selected.error && <div className="text-red text-sm break-words">{selected.error}</div>}
            {selected.redacted_request && <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto whitespace-pre-wrap break-words">{pretty(selected.redacted_request)}</pre>}
            {selected.redacted_response && <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto whitespace-pre-wrap break-words">{pretty(selected.redacted_response)}</pre>}
          </div>
        ) : <div className="text-sm text-text-dim">No entry selected</div>}
      </aside>
    </div>
  );
}

function EnvironmentView({ envs, editing, setEditing, save, remove }: {
  envs: Environment[];
  editing: Environment | null;
  setEditing: (environment: Environment | null) => void;
  save: (environment: Environment) => Promise<void>;
  remove: (environment: Environment) => Promise<void>;
}) {
  const [local, setLocal] = useState<Environment | null>(null);
  useEffect(() => { setLocal(editing ? { ...editing, vars: [...(editing.vars || [])] } : null); }, [editing]);
  if (local) {
    const vars = local.vars || [];
    return (
      <div className="flex-1 overflow-auto p-3 xl:p-4 max-w-4xl">
        <div className="space-y-3">
          <input value={local.name} onChange={(event) => setLocal({ ...local, name: event.target.value })} placeholder="Environment name" className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
          <div className="space-y-2">
            {vars.map((variable, index) => (
              <div key={`${variable.key}-${index}`} className="grid grid-cols-1 sm:grid-cols-[1fr_1fr_90px_32px] gap-2">
                <input value={variable.key} onChange={(event) => updateVar(local, setLocal, index, { ...variable, key: event.target.value })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" placeholder="KEY" />
                <input value={variable.value || ""} type={variable.is_secret ? "password" : "text"} onChange={(event) => updateVar(local, setLocal, index, { ...variable, value: event.target.value })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" placeholder={variable.has_value ? "unchanged" : "value"} />
                <label className="flex items-center gap-2 text-sm text-text-muted"><input type="checkbox" checked={variable.is_secret} onChange={(event) => updateVar(local, setLocal, index, { ...variable, is_secret: event.target.checked })} />Secret</label>
                <button type="button" title="Remove variable" onClick={() => setLocal({ ...local, vars: vars.filter((_, rowIndex) => rowIndex !== index) })} className="border border-border rounded hover:bg-bg-input">x</button>
              </div>
            ))}
          </div>
          <div className="flex flex-wrap gap-2">
            <button type="button" onClick={() => setLocal({ ...local, vars: [...vars, { key: "", value: "", is_secret: false }] })} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input">Add variable</button>
            <button type="button" onClick={() => save(local)} disabled={!local.name.trim()} className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50">Save</button>
            <button type="button" onClick={() => setEditing(null)} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
            {local.id > 0 && <button type="button" onClick={() => remove(local)} className="sm:ml-auto px-3 py-1.5 text-sm border border-red/50 text-red rounded">Delete</button>}
          </div>
        </div>
      </div>
    );
  }
  return (
    <div className="flex-1 overflow-auto p-3 xl:p-4">
      <button type="button" onClick={() => setEditing({ id: 0, slug: "", name: "", vars: [] })} className="mb-3 px-3 py-1.5 text-sm bg-accent text-bg rounded">New environment</button>
      <div className="max-w-3xl border border-border rounded overflow-hidden">
        {envs.map((environment) => (
          <button key={environment.id} type="button" onClick={() => setEditing(environment)} className="w-full flex items-center justify-between px-3 py-2 border-b border-border last:border-b-0 hover:bg-bg-input text-left">
            <span>{environment.name}</span><span className="text-xs text-text-dim">{environment.vars?.length || 0} vars</span>
          </button>
        ))}
      </div>
    </div>
  );
}

function updateVar(environment: Environment, setLocal: (environment: Environment) => void, index: number, next: EnvironmentVar) {
  const vars = [...(environment.vars || [])];
  vars[index] = next;
  setLocal({ ...environment, vars });
}

function draftFromSaved(saved: SavedRequest): Draft {
  const body = saved.body || {};
  let bodyMode: BodyMode = "none";
  let bodyText = "";
  for (const mode of ["json", "text", "form", "base64"] as BodyMode[]) {
    if (mode in body) {
      bodyMode = mode;
      bodyText = mode === "text" || mode === "base64" ? String(body[mode] ?? "") : pretty(body[mode]);
      break;
    }
  }
  return {
    name: saved.name,
    method: saved.method || "GET",
    url: saved.url_template,
    headers: objectToPairs(saved.headers),
    query: objectToPairs(saved.query),
    bodyMode,
    bodyText,
    environmentId: saved.environment_id ? String(saved.environment_id) : "",
  };
}

function bodyFromDraft(draft: Draft): Record<string, unknown> | undefined {
  const text = draft.bodyText;
  switch (draft.bodyMode) {
    case "none": return undefined;
    case "json": return { json: text.trim() ? JSON.parse(text) : {} };
    case "form": return { form: text.trim() ? JSON.parse(text) : {} };
    case "base64": return { base64: text.trim() };
    case "text": return { text };
  }
}

function validateDraft(draft: Draft) {
  try {
    const parsed = new URL(draft.url.trim());
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "URL must use http or https";
  } catch {
    return "Enter a valid absolute URL";
  }
  try {
    pairsToObject(draft.headers);
    pairsToObject(draft.query);
    bodyFromDraft(draft);
  } catch (cause) {
    return (cause as Error).message;
  }
  return "";
}

function pairsToObject(rows: Pair[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) continue;
    if (key in out) throw new Error(`Duplicate key: ${key}`);
    out[key] = row.value;
  }
  return out;
}

function objectToPairs(value: unknown): Pair[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) return [pair()];
  const rows = Object.entries(value as Record<string, unknown>).map(([key, entry]) => pair(key, typeof entry === "string" ? entry : JSON.stringify(entry)));
  return rows.length ? rows : [pair()];
}

function fingerprint(draft: Draft) {
  return JSON.stringify({ ...draft, headers: pairsToObjectSafe(draft.headers), query: pairsToObjectSafe(draft.query) });
}

function pairsToObjectSafe(rows: Pair[]) {
  return rows.map(({ key, value }) => [key, value]);
}

function defaultRequestName(draft: Draft) {
  try { return `${draft.method} ${new URL(draft.url).hostname}`; }
  catch { return `${draft.method} request`; }
}

function isSensitiveKey(key: string) {
  const normalized = key.toLowerCase().replace(/[-_. ]/g, "");
  return ["authorization", "apikey", "token", "secret", "password", "cookie", "privatekey"].some((part) => normalized.includes(part));
}

function pretty(value: unknown) { return JSON.stringify(value ?? {}, null, 2); }
function formatBytes(bytes: number) { return bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`; }
function statusTone(status: number) { return status >= 200 && status < 300 ? "text-success" : status >= 300 && status < 400 ? "text-info" : status >= 400 ? "text-red" : "text-text-muted"; }
