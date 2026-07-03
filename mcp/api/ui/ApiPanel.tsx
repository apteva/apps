import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface APIRecord {
  id: number;
  slug: string;
  name: string;
  description?: string;
  status: string;
  hostname?: string;
  dns_mode: string;
  dns_status?: string;
  ingress_status?: string;
  allow_http?: boolean;
  cors_json?: string;
  auth_json?: string;
  created_at?: string;
  updated_at?: string;
}

interface APIRoute {
  id: number;
  api_id: number;
  method: string;
  path_pattern: string;
  target_kind: string;
  target_ref: string;
  target_path?: string;
  auth_json?: string;
  cors_json?: string;
  timeout_ms: number;
  enabled: boolean;
  priority: number;
}

interface APIKey {
  id: number;
  api_id: number;
  name: string;
  key_prefix: string;
  status: string;
  last_used_at?: string;
  created_at?: string;
  revoked_at?: string;
}

interface RequestLog {
  id: number;
  api_id?: number;
  route_id?: number;
  hostname: string;
  method: string;
  path: string;
  status_code: number;
  target_kind?: string;
  target_ref?: string;
  auth_kind?: string;
  subject?: string;
  duration_ms: number;
  error?: string;
  request_id?: string;
  created_at?: string;
}

type View = "details" | "routes" | "keys" | "logs";

const API = "/api/apps/api";
const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";
const buttonCls = "px-3 py-1.5 rounded border border-border hover:bg-surface-2 disabled:opacity-50 disabled:cursor-not-allowed";
const primaryBtn = "px-3 py-1.5 rounded bg-accent text-white disabled:opacity-50 disabled:cursor-not-allowed";

export default function ApiPanel({ projectId, installId }: NativePanelProps) {
  const [apis, setApis] = useState<APIRecord[]>([]);
  const [selected, setSelected] = useState<APIRecord | null>(null);
  const [routes, setRoutes] = useState<APIRoute[]>([]);
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [logs, setLogs] = useState<RequestLog[]>([]);
  const [view, setView] = useState<View>("details");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [secret, setSecret] = useState("");

  const withParams = useCallback(
    () => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(),
    [projectId, installId],
  );

  const apiUrl = useCallback((path: string) => `${API}${path}?${withParams()}`, [withParams]);

  const loadAPIs = useCallback(async () => {
    try {
      const r = await fetch(apiUrl("/apis"), { credentials: "same-origin" });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { apis: APIRecord[] };
      const rows = j.apis || [];
      setApis(rows);
      setSelected((cur) => {
        if (!rows.length) return null;
        if (!cur) return rows[0];
        return rows.find((row) => row.id === cur.id) || rows[0];
      });
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
      setApis([]);
      setSelected(null);
    }
  }, [apiUrl]);

  const loadDetail = useCallback(async (api: APIRecord | null) => {
    if (!api) {
      setRoutes([]);
      setKeys([]);
      setLogs([]);
      return;
    }
    const [routeRes, keyRes, logRes] = await Promise.allSettled([
      fetch(apiUrl(`/apis/${api.id}/routes`), { credentials: "same-origin" }),
      fetch(apiUrl(`/apis/${api.id}/keys`), { credentials: "same-origin" }),
      fetch(apiUrl(`/apis/${api.id}/logs`), { credentials: "same-origin" }),
    ]);
    if (routeRes.status === "fulfilled" && routeRes.value.ok) {
      const j = (await routeRes.value.json().catch(() => ({ routes: [] }))) as { routes: APIRoute[] };
      setRoutes(j.routes || []);
    } else {
      setRoutes([]);
    }
    if (keyRes.status === "fulfilled" && keyRes.value.ok) {
      const j = (await keyRes.value.json().catch(() => ({ keys: [] }))) as { keys: APIKey[] };
      setKeys(j.keys || []);
    } else {
      setKeys([]);
    }
    if (logRes.status === "fulfilled" && logRes.value.ok) {
      const j = (await logRes.value.json().catch(() => ({ logs: [] }))) as { logs: RequestLog[] };
      setLogs(j.logs || []);
    } else {
      setLogs([]);
    }
  }, [apiUrl]);

  useEffect(() => { loadAPIs(); }, [loadAPIs]);
  useEffect(() => { loadDetail(selected); }, [selected, loadDetail]);

  const callTool = useCallback(async (tool: string, args: Record<string, unknown>) => {
    const r = await fetch(apiUrl("/tools/call"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool, args }),
    });
    if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
    return r.json();
  }, [apiUrl]);

  const refresh = useCallback(async () => {
    await loadAPIs();
    await loadDetail(selected);
  }, [loadAPIs, loadDetail, selected]);

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <div className="px-6 pt-6 pb-3 flex items-center justify-between border-b border-border">
        <div className="flex items-center gap-4 min-w-0">
          <h1 className="text-lg font-semibold whitespace-nowrap">API Gateway</h1>
          {selected && (
            <div className="flex rounded border border-border overflow-hidden text-xs">
              {(["details", "routes", "keys", "logs"] as View[]).map((v) => (
                <button
                  key={v}
                  type="button"
                  className={`px-3 py-1 capitalize ${view === v ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
                  onClick={() => setView(v)}
                >
                  {v}
                </button>
              ))}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>working...</span>}
          <button type="button" className={buttonCls} onClick={refresh}>Refresh</button>
        </div>
      </div>

      {err && <div className="mx-4 mt-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">{err}</div>}
      {secret && <div className="mx-4 mt-4 p-3 rounded border border-green/30 bg-green/10 text-sm font-mono break-all">{secret}</div>}

      <div className="flex-1 min-h-0 grid grid-cols-[280px_minmax(0,1fr)]">
        <aside className="border-r border-border min-h-0 flex flex-col">
          <CreateAPIForm
            busy={busy}
            onCreate={async (args) => {
              setBusy(true);
              setErr("");
              try {
                await callTool("api_create", args);
                await loadAPIs();
              } catch (e) {
                setErr((e as Error).message);
              } finally {
                setBusy(false);
              }
            }}
          />
          <div className="flex-1 overflow-auto">
            {apis.map((api) => (
              <button
                key={api.id}
                type="button"
                className={`w-full text-left px-4 py-3 border-b border-border hover:bg-surface-2 ${selected?.id === api.id ? "bg-surface-2" : ""}`}
                onClick={() => { setSelected(api); setSecret(""); }}
              >
                <div className="font-medium truncate">{api.name || api.slug}</div>
                <div className="text-xs text-text-dim truncate">{api.hostname || `/${api.slug}`}</div>
              </button>
            ))}
            {apis.length === 0 && <div className="p-4 text-sm text-text-dim">No APIs</div>}
          </div>
        </aside>

        <main className="min-w-0 min-h-0 overflow-auto">
          {!selected ? (
            <div className="p-6 text-sm text-text-dim">No API selected.</div>
          ) : view === "details" ? (
            <DetailsView
              api={selected}
              busy={busy}
              onSave={async (patch) => {
                setBusy(true);
                setErr("");
                try {
                  await callTool("api_update", { id: selected.id, ...patch });
                  await loadAPIs();
                } catch (e) {
                  setErr((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
              onDelete={async () => {
                if (!confirm(`Delete ${selected.slug}?`)) return;
                setBusy(true);
                setErr("");
                try {
                  await callTool("api_delete", { id: selected.id });
                  setSelected(null);
                  await loadAPIs();
                } catch (e) {
                  setErr((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            />
          ) : view === "routes" ? (
            <RoutesView
              api={selected}
              routes={routes}
              busy={busy}
              onAdd={async (args) => {
                setBusy(true);
                setErr("");
                try {
                  await callTool("api_route_add", { api_id: selected.id, ...args });
                  await loadDetail(selected);
                } catch (e) {
                  setErr((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
              onDelete={async (id) => {
                setBusy(true);
                setErr("");
                try {
                  await callTool("api_route_delete", { id });
                  await loadDetail(selected);
                } catch (e) {
                  setErr((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            />
          ) : view === "keys" ? (
            <KeysView
              keys={keys}
              busy={busy}
              onCreate={async (name) => {
                setBusy(true);
                setErr("");
                setSecret("");
                try {
                  const out = await callTool("api_key_create", { api_id: selected.id, name }) as { secret?: string };
                  setSecret(out.secret || "");
                  await loadDetail(selected);
                } catch (e) {
                  setErr((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
              onRevoke={async (id) => {
                setBusy(true);
                setErr("");
                try {
                  await callTool("api_key_revoke", { id });
                  await loadDetail(selected);
                } catch (e) {
                  setErr((e as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            />
          ) : (
            <LogsView logs={logs} />
          )}
        </main>
      </div>
    </div>
  );
}

function CreateAPIForm({ busy, onCreate }: { busy: boolean; onCreate(args: Record<string, unknown>): Promise<void> }) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  return (
    <form
      className="p-4 border-b border-border space-y-2"
      onSubmit={async (e) => {
        e.preventDefault();
        await onCreate({ slug: slug.trim(), name: name.trim() || slug.trim(), auth: { kind: "public" }, cors: {} });
        setSlug("");
        setName("");
      }}
    >
      <input className={inputCls} value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="slug" required />
      <input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} placeholder="name" />
      <button type="submit" className={primaryBtn + " w-full"} disabled={busy || !slug.trim()}>Create</button>
    </form>
  );
}

function DetailsView({ api, busy, onSave, onDelete }: {
  api: APIRecord;
  busy: boolean;
  onSave(patch: Record<string, unknown>): Promise<void>;
  onDelete(): Promise<void>;
}) {
  const [name, setName] = useState(api.name);
  const [description, setDescription] = useState(api.description || "");
  const [hostname, setHostname] = useState(api.hostname || "");
  const [dnsMode, setDNSMode] = useState(api.dns_mode || "manual");
  const [status, setStatus] = useState(api.status || "active");
  const [allowHTTP, setAllowHTTP] = useState(!!api.allow_http);
  const [authKind, setAuthKind] = useState(authKind(api.auth_json));
  const [corsEnabled, setCORSEnabled] = useState(corsEnabledFrom(api.cors_json));

  useEffect(() => {
    setName(api.name);
    setDescription(api.description || "");
    setHostname(api.hostname || "");
    setDNSMode(api.dns_mode || "manual");
    setStatus(api.status || "active");
    setAllowHTTP(!!api.allow_http);
    setAuthKind(authKind(api.auth_json));
    setCORSEnabled(corsEnabledFrom(api.cors_json));
  }, [api]);

  return (
    <form
      className="p-6 max-w-4xl space-y-6"
      onSubmit={async (e) => {
        e.preventDefault();
        await onSave({
          name,
          description,
          hostname,
          dns_mode: dnsMode,
          status,
          allow_http: allowHTTP,
          auth: { kind: authKind },
          cors: corsEnabled ? { enabled: "true" } : {},
        });
      }}
    >
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <Field label="Name"><input className={inputCls} value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Status">
          <select className={inputCls} value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="active">active</option>
            <option value="disabled">disabled</option>
          </select>
        </Field>
        <Field label="Hostname"><input className={inputCls} value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="api.example.com" /></Field>
        <Field label="DNS">
          <select className={inputCls} value={dnsMode} onChange={(e) => setDNSMode(e.target.value)}>
            <option value="manual">manual</option>
            <option value="domains">domains</option>
            <option value="skipped">skipped</option>
          </select>
        </Field>
        <Field label="Auth">
          <select className={inputCls} value={authKind} onChange={(e) => setAuthKind(e.target.value)}>
            <option value="public">public</option>
            <option value="api_key">api_key</option>
            <option value="auth_jwt">auth_jwt</option>
          </select>
        </Field>
        <Field label="Options">
          <div className="flex items-center gap-4 h-9 text-sm">
            <label className="flex items-center gap-2"><input type="checkbox" checked={allowHTTP} onChange={(e) => setAllowHTTP(e.target.checked)} /> HTTP</label>
            <label className="flex items-center gap-2"><input type="checkbox" checked={corsEnabled} onChange={(e) => setCORSEnabled(e.target.checked)} /> CORS</label>
          </div>
        </Field>
      </div>
      <Field label="Description"><textarea className={inputCls} value={description} onChange={(e) => setDescription(e.target.value)} rows={3} /></Field>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-px border border-border bg-border text-sm">
        <StatusCell label="DNS status" value={api.dns_status || "-"} />
        <StatusCell label="Ingress status" value={api.ingress_status || "-"} />
      </div>
      <div className="flex gap-2">
        <button type="submit" className={primaryBtn} disabled={busy}>Save</button>
        <button type="button" className={buttonCls} onClick={onDelete} disabled={busy}>Delete</button>
      </div>
    </form>
  );
}

function RoutesView({ api, routes, busy, onAdd, onDelete }: {
  api: APIRecord;
  routes: APIRoute[];
  busy: boolean;
  onAdd(args: Record<string, unknown>): Promise<void>;
  onDelete(id: number): Promise<void>;
}) {
  const [method, setMethod] = useState("GET");
  const [path, setPath] = useState("/");
  const [targetKind, setTargetKind] = useState("http");
  const [targetRef, setTargetRef] = useState("");
  const [targetPath, setTargetPath] = useState("");
  const [auth, setAuth] = useState("default");

  const publicBase = api.hostname ? `https://${api.hostname}` : `/api/apps/api/gw/${api.slug}`;

  return (
    <div className="min-h-full flex flex-col">
      <form
        className="p-4 border-b border-border grid grid-cols-1 lg:grid-cols-[100px_minmax(140px,1fr)_120px_minmax(180px,1.4fr)_minmax(140px,1fr)_120px] gap-2 items-end"
        onSubmit={async (e) => {
          e.preventDefault();
          await onAdd({
            method,
            path_pattern: path,
            target_kind: targetKind,
            target_ref: targetRef,
            target_path: targetPath,
            auth: auth === "default" ? {} : { kind: auth },
            enabled: true,
          });
          setTargetRef("");
          setTargetPath("");
        }}
      >
        <Field label="Method"><select className={inputCls} value={method} onChange={(e) => setMethod(e.target.value)}>{["GET", "POST", "PUT", "PATCH", "DELETE", "ANY"].map((m) => <option key={m}>{m}</option>)}</select></Field>
        <Field label="Path"><input className={inputCls} value={path} onChange={(e) => setPath(e.target.value)} placeholder="/v1/items" /></Field>
        <Field label="Target"><select className={inputCls} value={targetKind} onChange={(e) => setTargetKind(e.target.value)}>{["http", "function", "app"].map((k) => <option key={k}>{k}</option>)}</select></Field>
        <Field label="Ref"><input className={inputCls} value={targetRef} onChange={(e) => setTargetRef(e.target.value)} placeholder={targetKind === "http" ? "https://..." : targetKind === "function" ? "function-name" : "app-name"} /></Field>
        <Field label="Target path"><input className={inputCls} value={targetPath} onChange={(e) => setTargetPath(e.target.value)} placeholder="/upstream" /></Field>
        <Field label="Auth"><select className={inputCls} value={auth} onChange={(e) => setAuth(e.target.value)}>{["default", "public", "api_key", "auth_jwt"].map((k) => <option key={k}>{k}</option>)}</select></Field>
        <button type="submit" className={primaryBtn + " lg:col-start-6"} disabled={busy || !targetRef.trim() || !path.trim()}>Add</button>
      </form>
      <div className="px-4 py-2 border-b border-border text-xs text-text-dim font-mono truncate">{publicBase}</div>
      <DataTable
        columns={["method", "path", "target", "auth", "status", ""]}
        rows={routes.map((r) => [
          r.method,
          r.path_pattern,
          `${r.target_kind}:${r.target_ref}${r.target_path ? r.target_path : ""}`,
          authKind(r.auth_json) || "default",
          r.enabled ? "enabled" : "disabled",
          <button type="button" className="text-red-300 hover:underline" disabled={busy} onClick={() => onDelete(r.id)}>Delete</button>,
        ])}
      />
    </div>
  );
}

function KeysView({ keys, busy, onCreate, onRevoke }: {
  keys: APIKey[];
  busy: boolean;
  onCreate(name: string): Promise<void>;
  onRevoke(id: number): Promise<void>;
}) {
  const [name, setName] = useState("");
  return (
    <div className="min-h-full flex flex-col">
      <form className="p-4 border-b border-border flex gap-2 items-end" onSubmit={async (e) => { e.preventDefault(); await onCreate(name || "default"); setName(""); }}>
        <Field label="Name"><input className={inputCls + " w-72"} value={name} onChange={(e) => setName(e.target.value)} placeholder="production" /></Field>
        <button type="submit" className={primaryBtn} disabled={busy}>Create</button>
      </form>
      <DataTable
        columns={["name", "prefix", "status", "last used", ""]}
        rows={keys.map((k) => [
          k.name,
          k.key_prefix,
          k.status,
          date(k.last_used_at),
          k.status === "active" ? <button type="button" className="text-red-300 hover:underline" disabled={busy} onClick={() => onRevoke(k.id)}>Revoke</button> : "",
        ])}
      />
    </div>
  );
}

function LogsView({ logs }: { logs: RequestLog[] }) {
  return (
    <DataTable
      columns={["time", "status", "method", "path", "target", "auth", "ms", "error"]}
      rows={logs.map((l) => [
        date(l.created_at),
        String(l.status_code),
        l.method,
        l.path,
        `${l.target_kind || ""}:${l.target_ref || ""}`,
        l.auth_kind || "",
        String(l.duration_ms),
        l.error || "",
      ])}
    />
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block min-w-0"><div className="text-xs text-text-dim mb-1">{label}</div>{children}</label>;
}

function StatusCell({ label, value }: { label: string; value: string }) {
  return <div className="bg-bg p-4 min-w-0"><div className="text-xs text-text-dim truncate">{label}</div><div className="mt-1 text-sm font-mono truncate">{value}</div></div>;
}

function DataTable({ columns, rows }: { columns: string[]; rows: (ReactNode[])[] }) {
  return (
    <div className="flex-1 min-h-0 overflow-auto">
      <table className="w-full text-sm">
        <thead className="sticky top-0 bg-bg border-b border-border text-text-dim">
          <tr>{columns.map((c) => <th key={c} className="text-left font-medium px-4 py-2 whitespace-nowrap">{c}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((row, idx) => (
            <tr key={idx} className="border-b border-border/70 hover:bg-surface-2">
              {row.map((cell, i) => <td key={i} className="px-4 py-2 max-w-[360px] truncate">{cell}</td>)}
            </tr>
          ))}
          {rows.length === 0 && <tr><td className="px-4 py-6 text-text-dim" colSpan={columns.length}>No rows</td></tr>}
        </tbody>
      </table>
    </div>
  );
}

function parseJSON(s?: string): Record<string, unknown> {
  if (!s) return {};
  try {
    const parsed = JSON.parse(s);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function authKind(s?: string): string {
  const obj = parseJSON(s);
  return typeof obj.kind === "string" ? obj.kind : "";
}

function corsEnabledFrom(s?: string): boolean {
  const obj = parseJSON(s);
  return obj.enabled === true || obj.enabled === "true";
}

function date(s?: string): string {
  if (!s) return "-";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleString();
}
