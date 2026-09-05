import { parseJSON, corsEnabledFrom, corsOriginsFrom, corsCredentialsFrom, updatedCORS, LatestRequest, fetchPanelRows } from "./policy";
import { useCallback, useEffect, useState, useRef } from "react";
import type { ReactNode } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface APIRecord {
 browser_origins_pending?: boolean;
 browser_origins_error?: string;
 exposure_error?: string;
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
  events_json?: string;
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
  const [notice, setNotice] = useState("");
  const [secret, setSecret] = useState("");
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [logBefore, setLogBefore] = useState(0);
  useEffect(() => { setLogBefore(0); }, [selected?.id, projectId, installId]);
  const [detailOwner, setDetailOwner] = useState("");
  const apiRequests = useRef(new LatestRequest());
  const detailRequests = useRef(new LatestRequest());
  const scope = `${projectId}:${installId}`;
  const currentScope = useRef(scope); currentScope.current = scope;
  const currentSelection = useRef(selected?.id); currentSelection.current = selected?.id;
  useEffect(() => { setSecret(""); setNotice(""); setErr(""); setApis([]); setSelected(null); setDetailOwner(""); return () => { apiRequests.current.cancel(); detailRequests.current.cancel(); }; }, [scope]);

  const withParams = useCallback(
    () => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(),
    [projectId, installId],
  );

  const apiUrl = useCallback((path: string) => `${API}${path}?${withParams()}`, [withParams]);

  const loadAPIs = useCallback(async () => {
    const request = apiRequests.current.begin();
    try {
      const r = await fetch(apiUrl("/apis"), { credentials: "same-origin", signal: request.signal });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { apis: APIRecord[] };
      const rows = j.apis || [];
      if (!request.current() || currentScope.current !== scope) return;
      setApis(rows);
      setSelected((cur) => {
        if (!rows.length) return null;
        if (!cur) return rows[0];
        return rows.find((row) => row.id === cur.id) || rows[0];
      });
      setErr("");
    } catch (e) {
      if (!request.current() || currentScope.current !== scope) return;
      setErr((e as Error).message);
      setApis([]);
      setSelected(null);
    }
  }, [apiUrl, scope]);

  const loadDetail = useCallback(async (api: APIRecord | null) => {
    const request = detailRequests.current.begin();
    setDetailOwner(""); setRoutes([]); setKeys([]); setLogs([]);
    if (!api || view === "details") { setLoadingDetail(false); return; }
    const owner = `${scope}:${api.id}:${view}`;
    setLoadingDetail(true);
    try {
      const rows = await fetchPanelRows(fetch, apiUrl(`/apis/${api.id}/${view}`) + (view === "logs" ? `&before_id=${logBefore}` : ""), view, request.signal);
      if (!request.current() || currentScope.current !== scope || currentSelection.current !== api.id) return;
      if (view === "routes") setRoutes(rows);
      if (view === "keys") setKeys(rows);
      if (view === "logs") setLogs(rows);
      setDetailOwner(owner); setErr("");
    } catch (e) {
      if (request.current() && currentScope.current === scope) setErr((e as Error).message);
    } finally { if (request.current()) setLoadingDetail(false); }
  }, [apiUrl, scope, view, logBefore]);

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
    const result = await r.json();
    if (currentScope.current !== scope) throw new Error("Project selection changed; refresh to see the result.");
    if (result.browser_origins_synced === false || result.exposure_error) setNotice(result.browser_origins_error || result.exposure_error || "Browser policy synchronization is pending.");
    return result;
  }, [apiUrl, scope]);

  const refresh = useCallback(async () => {
    await loadAPIs();
  }, [loadAPIs]);

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
          <button type="button" className={buttonCls} onClick={refresh} disabled={busy}>Refresh</button>
        </div>
      </div>

      {notice && <div className="mx-4 mt-3 text-sm text-amber-600">{notice}</div>}
      {err && <div className="mx-4 mt-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 whitespace-pre-wrap">{err}</div>}
      {selected?.browser_origins_pending && <div className="mx-4 mt-3 text-sm text-amber-600">Browser policy sync pending: {selected.browser_origins_error || "retrying"}</div>}
      {selected?.exposure_error && <div className="mx-4 mt-3 text-sm text-red-500">{selected.exposure_error}</div>}
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
                return true;
              } catch (e) {
                setErr((e as Error).message);
                return false;
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
                disabled={busy}
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
                  return false;
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
                  return false;
                } finally {
                  setBusy(false);
                }
              }}
            />
          ) : view === "routes" ? (
            <RoutesView
              api={selected}
              routes={detailOwner === `${scope}:${selected.id}:${view}` ? routes : []}
              busy={busy || loadingDetail || detailOwner !== `${scope}:${selected.id}:${view}`}
              projectId={projectId}
              installId={installId}
              onAdd={async (args) => {
                setBusy(true);
                setErr("");
                try {
                  await callTool("api_route_add", { api_id: selected.id, ...args });
                  await loadDetail(selected);
                  return true;
                } catch (e) {
                  setErr((e as Error).message);
                  return false;
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
                  return false;
                } finally {
                  setBusy(false);
                }
              }}
            />
          ) : view === "keys" ? (
            <KeysView
              keys={detailOwner === `${scope}:${selected.id}:${view}` ? keys : []}
              busy={busy || loadingDetail || detailOwner !== `${scope}:${selected.id}:${view}`}
              onCreate={async (name) => {
                setBusy(true);
                setErr("");
                setSecret("");
                try {
                  const out = await callTool("api_key_create", { api_id: selected.id, name }) as { secret?: string };
                  setSecret(out.secret || "");
                  await loadDetail(selected);
                  return true;
                } catch (e) {
                  setErr((e as Error).message);
                  return false;
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
                  return false;
                } finally {
                  setBusy(false);
                }
              }}
            />
          ) : (
            <div><div className="flex gap-2 p-3"><button className={buttonCls} disabled={loadingDetail || logBefore === 0} onClick={() => setLogBefore(0)}>Newest</button><button className={buttonCls} disabled={loadingDetail || detailOwner !== `${scope}:${selected.id}:${view}` || logs.length < 100} onClick={() => setLogBefore(logs[logs.length-1].id)}>Older</button></div><LogsView logs={detailOwner === `${scope}:${selected.id}:${view}` ? logs : []} /></div>
          )}
        </main>
      </div>
    </div>
  );
}

function CreateAPIForm({ busy, onCreate }: { busy: boolean; onCreate(args: Record<string, unknown>): Promise<boolean> }) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  return (
    <form
      className="p-4 border-b border-border space-y-2"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!await onCreate({ slug: slug.trim(), name: name.trim() || slug.trim(), auth: { kind: "public" }, cors: {} })) return;
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
  onSave(patch: Record<string, unknown>): Promise<unknown>;
  onDelete(): Promise<unknown>;
}) {
  const [name, setName] = useState(api.name);
  const [description, setDescription] = useState(api.description || "");
  const [hostname, setHostname] = useState(api.hostname || "");
  const [dnsMode, setDNSMode] = useState(api.dns_mode || "manual");
  const [status, setStatus] = useState(api.status || "active");
  const [allowHTTP, setAllowHTTP] = useState(!!api.allow_http);
  const [authKind, setAuthKind] = useState(parseAuthKind(api.auth_json) || "public");
  const [corsEnabled, setCORSEnabled] = useState(corsEnabledFrom(api.cors_json));
  const [corsOrigins, setCORSOrigins] = useState(corsOriginsFrom(api.cors_json).join("\n"));
  const [corsCredentials, setCORSCredentials] = useState(corsCredentialsFrom(api.cors_json));

  useEffect(() => {
    setName(api.name);
    setDescription(api.description || "");
    setHostname(api.hostname || "");
    setDNSMode(api.dns_mode || "manual");
    setStatus(api.status || "active");
    setAllowHTTP(!!api.allow_http);
    setAuthKind(parseAuthKind(api.auth_json) || "public");
    setCORSEnabled(corsEnabledFrom(api.cors_json));
    setCORSOrigins(corsOriginsFrom(api.cors_json).join("\n"));
    setCORSCredentials(corsCredentialsFrom(api.cors_json));
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
          auth: { kind: authKind || "public" },
          cors: updatedCORS(api.cors_json, corsEnabled, corsOrigins, corsCredentials),
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
      {corsEnabled && (
        <div className="grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_220px] gap-4">
          <Field label="Allowed browser origins">
            <textarea
              className={inputCls}
              value={corsOrigins}
              onChange={(e) => setCORSOrigins(e.target.value)}
              placeholder={"https://app.example.com\nhttp://localhost:3000"}
              rows={3}
              required
            />
          </Field>
          <Field label="Browser credentials">
            <label className="flex items-center gap-2 h-9 text-sm">
              <input type="checkbox" checked={corsCredentials} onChange={(e) => setCORSCredentials(e.target.checked)} />
              Allow cookies or credentials
            </label>
          </Field>
        </div>
      )}
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

function RoutesView({ api, routes, busy, onAdd, onDelete, projectId, installId }: {
 projectId: string; installId: number;
  api: APIRecord;
  routes: APIRoute[];
  busy: boolean;
  onAdd(args: Record<string, unknown>): Promise<boolean>;
  onDelete(id: number): Promise<unknown>;
}) {
  const [method, setMethod] = useState("GET");
  const [path, setPath] = useState("/");
  const [targetKind, setTargetKind] = useState("http");
  const [targetRef, setTargetRef] = useState("");
  const [targetPath, setTargetPath] = useState("");
  const [auth, setAuth] = useState("default");
  const [eventTopics, setEventTopics] = useState("");
  const [eventMatchPath, setEventMatchPath] = useState("");
  const [eventMatchValue, setEventMatchValue] = useState("");
  const [eventResource, setEventResource] = useState("");
  const [coalesceMS, setCoalesceMS] = useState("200");

  const publicBase = api.hostname ? `https://${api.hostname}` : `/api/apps/api/gw/${api.slug}?${new URLSearchParams({project_id: projectId, install_id: String(installId)})}`;

  return (
    <div className="min-h-full flex flex-col">
      <form
        className="p-4 border-b border-border grid grid-cols-1 lg:grid-cols-[100px_minmax(140px,1fr)_120px_minmax(180px,1.4fr)_minmax(140px,1fr)_120px] gap-2 items-end"
        onSubmit={async (e) => {
          e.preventDefault();
          const saved = await onAdd({
            method,
            path_pattern: path,
            target_kind: targetKind,
            target_ref: targetRef,
            target_path: targetKind === "app_events" ? "" : targetPath,
            auth: auth === "default" ? {} : { kind: auth },
            events: targetKind === "app_events" ? {
              topics: eventTopics.split(",").map((topic) => topic.trim()).filter(Boolean),
              match: eventMatchPath.trim() ? { [eventMatchPath.trim()]: eventMatchCondition(eventMatchValue) } : {},
              output: { type: "invalidate", resource: eventResource.trim() },
              coalesce_ms: Number(coalesceMS),
            } : {},
            enabled: true,
          });
          if (!saved) return;
          setTargetRef("");
          setTargetPath("");
        }}
      >
        <Field label="Method"><select className={inputCls} value={method} onChange={(e) => setMethod(e.target.value)} disabled={targetKind === "app_events"}>{["GET", "POST", "PUT", "PATCH", "DELETE", "ANY"].map((m) => <option key={m}>{m}</option>)}</select></Field>
        <Field label="Path"><input className={inputCls} value={path} onChange={(e) => setPath(e.target.value)} placeholder="/v1/items" /></Field>
        <Field label="Target"><select className={inputCls} value={targetKind} onChange={(e) => { const kind = e.target.value; setTargetKind(kind); if (kind === "app_events") { setMethod("GET"); setAuth("api_key"); setTargetPath(""); } }}>{["http", "function", "app", "app_events"].map((k) => <option key={k}>{k}</option>)}</select></Field>
        <Field label={targetKind === "app_events" ? "Source app" : "Ref"}><input className={inputCls} value={targetRef} onChange={(e) => setTargetRef(e.target.value)} placeholder={targetKind === "http" ? "https://..." : targetKind === "function" ? "function-name" : targetKind === "app_events" ? "tables" : "app-name"} /></Field>
        <Field label="Target path"><input className={inputCls} value={targetPath} onChange={(e) => setTargetPath(e.target.value)} placeholder={targetKind === "app_events" ? "not used" : "/upstream"} disabled={targetKind === "app_events"} /></Field>
        <Field label="Auth"><select className={inputCls} value={auth} onChange={(e) => setAuth(e.target.value)}>{(targetKind === "app_events" ? ["api_key", "auth_jwt"] : ["default", "public", "api_key", "auth_jwt"]).map((k) => <option key={k}>{k}</option>)}</select></Field>
        {targetKind === "app_events" && (
          <div className="lg:col-span-6 grid grid-cols-1 lg:grid-cols-4 gap-2 border border-border rounded p-3">
            <Field label="Topics (comma separated)"><input className={inputCls} value={eventTopics} onChange={(e) => setEventTopics(e.target.value)} placeholder="row.inserted, row.updated" /></Field>
            <Field label="Optional match path"><input className={inputCls} value={eventMatchPath} onChange={(e) => setEventMatchPath(e.target.value)} placeholder="data.table" /></Field>
            <Field label="Match value(s)"><input className={inputCls} value={eventMatchValue} onChange={(e) => setEventMatchValue(e.target.value)} placeholder="appels, ventes, prospects" disabled={!eventMatchPath.trim()} /></Field>
            <Field label="Public resource"><input className={inputCls} value={eventResource} onChange={(e) => setEventResource(e.target.value)} placeholder="$data.table" /></Field>
            <Field label="Coalesce (ms)"><input className={inputCls} type="number" min="0" max="5000" value={coalesceMS} onChange={(e) => setCoalesceMS(e.target.value)} /></Field>
            <div className="lg:col-span-3 text-xs text-text-dim self-end pb-2">Comma-separated values create a safe allowlist. Use $data.&lt;field&gt; as the public resource to project only that constrained value.</div>
          </div>
        )}
        <button type="submit" className={primaryBtn + " lg:col-start-6"} disabled={busy || !targetRef.trim() || !path.trim() || (targetKind === "app_events" && (!eventTopics.trim() || !eventResource.trim()))}>Add</button>
      </form>
      <div className="px-4 py-2 border-b border-border text-xs text-text-dim font-mono truncate">{publicBase}</div>
      <DataTable
        columns={["method", "path", "target", "auth", "status", ""]}
        rows={routes.map((r) => [
          r.method,
          r.path_pattern,
          `${r.target_kind}:${r.target_ref}${r.target_path ? r.target_path : ""}${eventsSummary(r.events_json)}`,
          parseAuthKind(r.auth_json) || "default",
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
  onCreate(name: string): Promise<boolean>;
  onRevoke(id: number): Promise<unknown>;
}) {
  const [name, setName] = useState("");
  return (
    <div className="min-h-full flex flex-col">
      <form className="p-4 border-b border-border flex gap-2 items-end" onSubmit={async (e) => { e.preventDefault(); if (await onCreate(name || "default")) setName(""); }}>
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

function eventMatchCondition(raw: string): string | { in: string[] } {
  const values = raw.split(",").map((value) => value.trim()).filter(Boolean);
  return values.length > 1 ? { in: values } : values[0] || "";
}

function parseAuthKind(s?: string): string {
  const obj = parseJSON(s);
  return typeof obj.kind === "string" ? obj.kind : "";
}

function eventsSummary(s?: string): string {
  const obj = parseJSON(s);
  const topics = Array.isArray(obj.topics) ? obj.topics.filter((topic): topic is string => typeof topic === "string") : [];
  return topics.length ? ` [${topics.join(", ")}]` : "";
}

function date(s?: string): string {
  if (!s) return "-";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleString();
}
