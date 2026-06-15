import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/fetch";

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

type Tab = "requests" | "history" | "environments";

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

const emptyDraft = {
  name: "",
  method: "GET",
  url: "",
  headers: "{\n}",
  query: "{\n}",
  body: "{\n}",
  environmentId: "",
};

export default function FetchPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("requests");
  const [requests, setRequests] = useState<SavedRequest[]>([]);
  const [envs, setEnvs] = useState<Environment[]>([]);
  const [history, setHistory] = useState<HistoryRow[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [draft, setDraft] = useState(emptyDraft);
  const [result, setResult] = useState<FetchResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [envEditing, setEnvEditing] = useState<Environment | null>(null);

  const selected = useMemo(
    () => requests.find((r) => r.id === selectedId) || null,
    [requests, selectedId],
  );

  const api = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const sep = path.includes("?") ? "&" : "?";
    const res = await fetch(`${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`, {
      credentials: "include",
      headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
      ...init,
    });
    const text = await res.text();
    const data = text ? JSON.parse(text) : null;
    if (!res.ok || (data && typeof data === "object" && "error" in data)) {
      throw new Error((data as any)?.error || res.statusText);
    }
    return data as T;
  }, [projectId]);

  const loadAll = useCallback(async () => {
    try {
      const [reqData, envData, histData] = await Promise.all([
        api<{ requests: SavedRequest[] }>("/requests"),
        api<{ environments: Environment[] }>("/environments"),
        api<{ history: HistoryRow[] }>("/history?limit=100"),
      ]);
      setRequests(reqData.requests || []);
      setEnvs(envData.environments || []);
      setHistory(histData.history || []);
      setStatus("");
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [api]);

  useEffect(() => { loadAll(); }, [loadAll]);

  useEffect(() => {
    if (!selected) return;
    setDraft({
      name: selected.name,
      method: selected.method || "GET",
      url: selected.url_template,
      headers: pretty(selected.headers || {}),
      query: pretty(selected.query || {}),
      body: pretty(selected.body || {}),
      environmentId: selected.environment_id ? String(selected.environment_id) : "",
    });
  }, [selected]);

  const requestPayload = () => ({
    method: draft.method,
    url: draft.url,
    headers: parseObject(draft.headers, "headers"),
    query: parseObject(draft.query, "query"),
    body: parseObject(draft.body, "body"),
    environment_id: draft.environmentId ? Number(draft.environmentId) : undefined,
    save_history: true,
  });

  const runDraft = async () => {
    setBusy(true);
    setStatus("");
    try {
      const out = await api<FetchResult>("/execute", {
        method: "POST",
        body: JSON.stringify(requestPayload()),
      });
      setResult(out);
      await loadAll();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const saveDraft = async () => {
    setBusy(true);
    setStatus("");
    try {
      const payload = {
        name: draft.name || `${draft.method} ${new URL(draft.url).hostname}`,
        method: draft.method,
        url_template: draft.url,
        headers: parseObject(draft.headers, "headers"),
        query: parseObject(draft.query, "query"),
        body: parseObject(draft.body, "body"),
        environment_id: draft.environmentId ? Number(draft.environmentId) : undefined,
      };
      if (selectedId) {
        await api(`/requests/${selectedId}`, { method: "PATCH", body: JSON.stringify(payload) });
      } else {
        const created = await api<{ request: SavedRequest }>("/requests", {
          method: "POST",
          body: JSON.stringify(payload),
        });
        setSelectedId(created.request.id);
      }
      await loadAll();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const runSaved = async (id: number) => {
    setBusy(true);
    setStatus("");
    try {
      const out = await api<FetchResult>(`/requests/${id}/run`, {
        method: "POST",
        body: JSON.stringify({}),
      });
      setResult(out);
      await loadAll();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const archiveSelected = async () => {
    if (!selectedId) return;
    setBusy(true);
    try {
      await api(`/requests/${selectedId}`, { method: "DELETE" });
      setSelectedId(null);
      setDraft(emptyDraft);
      await loadAll();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="flex items-center gap-2 border-b border-border px-4 py-2">
        <div className="font-medium">Fetch</div>
        <nav className="flex items-center gap-1">
          <TabButton active={tab === "requests"} onClick={() => setTab("requests")}>Requests</TabButton>
          <TabButton active={tab === "history"} onClick={() => setTab("history")}>History</TabButton>
          <TabButton active={tab === "environments"} onClick={() => setTab("environments")}>Environments</TabButton>
        </nav>
        <span className="ml-auto text-xs text-red">{status}</span>
      </header>

      {tab === "requests" && (
        <div className="flex-1 min-h-0 grid grid-cols-[280px_minmax(420px,1fr)_minmax(300px,420px)]">
          <aside className="border-r border-border overflow-auto">
            <div className="p-2 border-b border-border">
              <button
                type="button"
                onClick={() => { setSelectedId(null); setDraft(emptyDraft); setResult(null); }}
                className="w-full text-sm px-2 py-1.5 border border-border rounded hover:bg-bg-input"
              >
                New request
              </button>
            </div>
            {requests.map((r) => (
              <button
                key={r.id}
                type="button"
                onClick={() => setSelectedId(r.id)}
                className={`w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input ${selectedId === r.id ? "bg-bg-input" : ""}`}
              >
                <div className="flex items-center gap-2">
                  <MethodBadge method={r.method} />
                  <span className="text-sm truncate">{r.name}</span>
                </div>
                <div className="mt-1 text-xs text-text-dim truncate">{r.url_template}</div>
              </button>
            ))}
          </aside>

          <main className="overflow-auto p-4 space-y-3">
            <div className="grid grid-cols-[110px_1fr] gap-2 items-center">
              <label className="text-sm text-text-muted">Name</label>
              <input
                value={draft.name}
                onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              />
              <label className="text-sm text-text-muted">Request</label>
              <div className="grid grid-cols-[110px_1fr] gap-2">
                <select
                  value={draft.method}
                  onChange={(e) => setDraft({ ...draft, method: e.target.value })}
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                >
                  {METHODS.map((m) => <option key={m} value={m}>{m}</option>)}
                </select>
                <input
                  value={draft.url}
                  onChange={(e) => setDraft({ ...draft, url: e.target.value })}
                  placeholder="https://api.example.com/v1/resource"
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono"
                />
              </div>
              <label className="text-sm text-text-muted">Environment</label>
              <select
                value={draft.environmentId}
                onChange={(e) => setDraft({ ...draft, environmentId: e.target.value })}
                className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
              >
                <option value="">None</option>
                {envs.map((e) => <option key={e.id} value={e.id}>{e.name}</option>)}
              </select>
            </div>

            <Editor label="Headers" value={draft.headers} onChange={(v) => setDraft({ ...draft, headers: v })} />
            <Editor label="Query" value={draft.query} onChange={(v) => setDraft({ ...draft, query: v })} />
            <Editor label="Body" value={draft.body} onChange={(v) => setDraft({ ...draft, body: v })} />

            <div className="flex items-center gap-2">
              <button
                type="button"
                disabled={busy || !draft.url}
                onClick={runDraft}
                className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50"
              >
                {busy ? "Running" : "Run"}
              </button>
              <button
                type="button"
                disabled={busy || !draft.url}
                onClick={saveDraft}
                className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
              >
                Save
              </button>
              {selectedId && (
                <>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => runSaved(selectedId)}
                    className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
                  >
                    Run saved
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={archiveSelected}
                    className="px-3 py-1.5 text-sm border border-red/50 text-red rounded hover:bg-red/10 disabled:opacity-50"
                  >
                    Archive
                  </button>
                </>
              )}
            </div>
          </main>

          <section className="border-l border-border overflow-auto p-4">
            <h2 className="text-sm font-medium mb-2">Response</h2>
            {result ? <ResponseView result={result} /> : <div className="text-sm text-text-dim">No response</div>}
          </section>
        </div>
      )}

      {tab === "history" && (
        <HistoryView rows={history} />
      )}

      {tab === "environments" && (
        <EnvironmentView
          envs={envs}
          editing={envEditing}
          setEditing={setEnvEditing}
          save={async (env) => {
            setBusy(true);
            try {
              if (env.id) {
                await api(`/environments/${env.id}`, { method: "PATCH", body: JSON.stringify(env) });
              } else {
                await api("/environments", { method: "POST", body: JSON.stringify(env) });
              }
              setEnvEditing(null);
              await loadAll();
            } catch (e) {
              setStatus((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        />
      )}
    </div>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`px-2 py-1 text-sm border rounded ${active ? "border-accent text-accent" : "border-transparent text-text-muted hover:bg-bg-input"}`}
    >
      {children}
    </button>
  );
}

function MethodBadge({ method }: { method: string }) {
  const tone = method === "GET" ? "text-info" : method === "DELETE" ? "text-red" : "text-accent";
  return <span className={`text-[10px] font-semibold ${tone} w-12 shrink-0`}>{method}</span>;
}

function Editor({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="block">
      <div className="text-sm text-text-muted mb-1">{label}</div>
      <textarea
        value={value}
        onChange={(e) => onChange(e.target.value)}
        spellCheck={false}
        className="w-full min-h-[92px] bg-bg-input border border-border rounded px-2 py-1.5 text-xs font-mono"
      />
    </label>
  );
}

function ResponseView({ result }: { result: FetchResult }) {
  const body = result.body_json !== undefined
    ? pretty(result.body_json)
    : result.body_text || result.body_base64 || "";
  return (
    <div className="space-y-3 text-sm">
      <div className="flex items-center gap-2">
        <span className={`font-semibold ${statusTone(result.status || 0)}`}>{result.status || "ERR"}</span>
        <span className="text-text-dim">{result.duration_ms ?? 0} ms</span>
      </div>
      {result.final_url && <div className="text-xs text-text-dim break-all">{result.final_url}</div>}
      <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto max-h-[240px]">{body}</pre>
      {result.body_truncated && <div className="text-xs text-warn">Body truncated</div>}
      <details>
        <summary className="cursor-pointer text-xs text-text-muted">Headers</summary>
        <pre className="mt-2 bg-bg-input border border-border rounded p-2 text-xs overflow-auto">{pretty(result.headers || {})}</pre>
      </details>
    </div>
  );
}

function HistoryView({ rows }: { rows: HistoryRow[] }) {
  const [selected, setSelected] = useState<HistoryRow | null>(null);
  return (
    <div className="flex-1 min-h-0 grid grid-cols-[minmax(420px,1fr)_minmax(320px,520px)]">
      <div className="overflow-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 bg-bg border-b border-border">
            <tr className="text-left text-xs text-text-muted">
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Method</th>
              <th className="px-3 py-2">URL</th>
              <th className="px-3 py-2">Time</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} onClick={() => setSelected(r)} className="border-b border-border hover:bg-bg-input cursor-pointer">
                <td className={`px-3 py-2 ${statusTone(r.status || 0)}`}>{r.error ? "ERR" : r.status}</td>
                <td className="px-3 py-2"><MethodBadge method={r.method} /></td>
                <td className="px-3 py-2 truncate max-w-[520px]">{r.url}</td>
                <td className="px-3 py-2 text-text-dim">{r.duration_ms || 0} ms</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <aside className="border-l border-border overflow-auto p-4">
        {selected ? (
          <div className="space-y-3">
            <div className="font-medium">Run #{selected.id}</div>
            {selected.error && <div className="text-red text-sm">{selected.error}</div>}
            <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto">{pretty(selected.redacted_request || {})}</pre>
            <pre className="bg-bg-input border border-border rounded p-2 text-xs overflow-auto">{pretty(selected.redacted_response || {})}</pre>
          </div>
        ) : (
          <div className="text-sm text-text-dim">No entry selected</div>
        )}
      </aside>
    </div>
  );
}

function EnvironmentView({
  envs,
  editing,
  setEditing,
  save,
}: {
  envs: Environment[];
  editing: Environment | null;
  setEditing: (e: Environment | null) => void;
  save: (e: Environment) => Promise<void>;
}) {
  const [local, setLocal] = useState<Environment | null>(null);

  useEffect(() => {
    setLocal(editing ? { ...editing, vars: [...(editing.vars || [])] } : null);
  }, [editing]);

  if (local) {
    const vars = local.vars || [];
    return (
      <div className="flex-1 overflow-auto p-4 max-w-3xl">
        <div className="space-y-3">
          <input
            value={local.name}
            onChange={(e) => setLocal({ ...local, name: e.target.value })}
            className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
          />
          <div className="space-y-2">
            {vars.map((v, i) => (
              <div key={i} className="grid grid-cols-[1fr_1fr_90px_32px] gap-2">
                <input
                  value={v.key}
                  onChange={(e) => updateVar(local, setLocal, i, { ...v, key: e.target.value })}
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  placeholder="KEY"
                />
                <input
                  value={v.value || ""}
                  type={v.is_secret ? "password" : "text"}
                  onChange={(e) => updateVar(local, setLocal, i, { ...v, value: e.target.value })}
                  className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm"
                  placeholder={v.has_value ? "unchanged" : "value"}
                />
                <label className="flex items-center gap-2 text-sm text-text-muted">
                  <input
                    type="checkbox"
                    checked={v.is_secret}
                    onChange={(e) => updateVar(local, setLocal, i, { ...v, is_secret: e.target.checked })}
                  />
                  Secret
                </label>
                <button type="button" onClick={() => setLocal({ ...local, vars: vars.filter((_, idx) => idx !== i) })} className="border border-border rounded hover:bg-bg-input">x</button>
              </div>
            ))}
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => setLocal({ ...local, vars: [...vars, { key: "", value: "", is_secret: false }] })}
              className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input"
            >
              Add variable
            </button>
            <button
              type="button"
              onClick={() => save(local)}
              disabled={!local.name.trim()}
              className="px-3 py-1.5 text-sm bg-accent text-bg rounded disabled:opacity-50"
            >
              Save
            </button>
            <button type="button" onClick={() => setEditing(null)} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex-1 overflow-auto p-4">
      <button
        type="button"
        onClick={() => setEditing({ id: 0, slug: "", name: "", vars: [] })}
        className="mb-3 px-3 py-1.5 text-sm bg-accent text-bg rounded"
      >
        New environment
      </button>
      <div className="max-w-3xl border border-border rounded overflow-hidden">
        {envs.map((e) => (
          <button
            key={e.id}
            type="button"
            onClick={() => setEditing(e)}
            className="w-full flex items-center justify-between px-3 py-2 border-b border-border last:border-b-0 hover:bg-bg-input text-left"
          >
            <span>{e.name}</span>
            <span className="text-xs text-text-dim">{e.vars?.length || 0} vars</span>
          </button>
        ))}
      </div>
    </div>
  );
}

function updateVar(env: Environment, setLocal: (e: Environment) => void, index: number, next: EnvironmentVar) {
  const vars = [...(env.vars || [])];
  vars[index] = next;
  setLocal({ ...env, vars });
}

function parseObject(text: string, label: string): Record<string, unknown> {
  const trimmed = text.trim();
  if (!trimmed) return {};
  const parsed = JSON.parse(trimmed);
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object`);
  }
  return parsed as Record<string, unknown>;
}

function pretty(v: unknown) {
  return JSON.stringify(v ?? {}, null, 2);
}

function statusTone(status: number) {
  if (status >= 200 && status < 300) return "text-success";
  if (status >= 300 && status < 400) return "text-info";
  if (status >= 400) return "text-red";
  return "text-text-muted";
}
