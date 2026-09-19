import { useEffect, useMemo, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";

const API = "/api/apps/graphql";
const ENVIRONMENTS = ["development", "staging", "production"];
const TABS = ["Schema", "Sources", "Resolvers", "Deploy", "Logs", "Realtime"];
const OPERATIONS = ["find", "get", "count", "aggregate", "function", "request"];

function requestURL(path, projectId, environment) {
  const params = new URLSearchParams({ project_id: projectId });
  if (environment) params.set("environment", environment);
  return `${API}/admin/${path}?${params.toString()}`;
}

async function apiFetch(path, projectId, environment, options = {}) {
  const response = await fetch(requestURL(path, projectId, environment), {
    credentials: "same-origin",
    ...options,
    headers: { ...(options.body ? { "Content-Type": "application/json" } : {}), ...(options.headers || {}) },
  });
  let body = {};
  try { body = await response.json(); } catch { /* empty response */ }
  // A fresh environment has no published schema: response.status === 404 is an editable empty state.
  if (!response.ok) throw new Error(body.error || body.message || `${response.status} ${response.statusText}`);
  return body;
}

const inputClass = "w-full bg-surface-2 border border-border rounded px-2.5 py-2 text-sm outline-none focus:border-accent";
const buttonClass = "px-3 py-2 rounded text-sm font-medium disabled:opacity-50";
const primaryButton = `${buttonClass} bg-accent text-white`;
const secondaryButton = `${buttonClass} border border-border hover:bg-surface-2`;
const panelClass = "rounded-lg border border-border bg-surface-1 p-4";

function Button({ primary = false, children, ...props }) {
  return jsx("button", { className: primary ? primaryButton : secondaryButton, ...props, children });
}

function Field({ label, children, hint }) {
  return jsxs("label", { className: "block space-y-1.5 text-sm", children: [jsx("span", { className: "text-text-dim", children: label }), children, hint && jsx("span", { className: "block text-xs text-text-dim", children: hint })] });
}

function Empty({ children }) {
  return jsx("div", { className: "py-10 text-center text-sm text-text-dim", children });
}

function StatusPill({ status }) {
  const color = status === "published" || status === "active" ? "text-green-400 bg-green-400/10" : status === "invalid" ? "text-red-400 bg-red-400/10" : "text-yellow-300 bg-yellow-300/10";
  return jsx("span", { className: `inline-flex rounded-full px-2 py-0.5 text-xs ${color}`, children: status || "unknown" });
}

function SchemaTab({ projectId, environment, schema, schemas, sdl, setSdl, onRefresh, onError }) {
  const [busy, setBusy] = useState(false);
  const [validation, setValidation] = useState(null);
  async function validate() { setBusy(true); setValidation(null); try { setValidation(await apiFetch("schema/validate", projectId, environment, { method: "POST", body: JSON.stringify({ sdl }) })); } catch (error) { onError(error); } finally { setBusy(false); } }
  async function save() { setBusy(true); onError(null); try { await apiFetch("schema", projectId, environment, { method: "POST", body: JSON.stringify({ sdl }) }); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  async function publish(version = schema?.version) { if (!version) return; setBusy(true); onError(null); try { await apiFetch("schema/publish", projectId, environment, { method: "POST", body: JSON.stringify({ version }) }); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_300px] gap-4 p-5", children: [
    jsxs("section", { className: panelClass, children: [jsxs("div", { className: "flex items-center justify-between mb-3", children: [jsx("h2", { className: "font-semibold", children: "Schema editor" }), jsxs("div", { className: "flex gap-2", children: [jsx(Button, { onClick: validate, disabled: busy, children: "Validate" }), jsx(Button, { primary: true, onClick: save, disabled: busy || !sdl.trim(), children: busy ? "Working…" : "Save draft" })] })] }), jsx("textarea", { className: `${inputClass} min-h-[430px] font-mono resize-y`, spellCheck: false, value: sdl, onChange: (event) => setSdl(event.target.value), placeholder: "type Query {\n  hello: String!\n}" }), validation && jsxs("div", { className: `mt-3 rounded p-3 text-sm ${validation.valid ? "bg-green-400/10 text-green-300" : "bg-red-400/10 text-red-300"}`, children: [jsx("div", { className: "font-medium", children: validation.valid ? "SDL is valid" : "SDL has validation errors" }), !validation.valid && jsx("ul", { className: "mt-1 list-disc pl-5", children: (validation.validation_errors || []).map((item, index) => jsx("li", { children: item }, index)) })] })] }),
    jsxs("aside", { className: `${panelClass} space-y-3`, children: [jsxs("div", { className: "flex items-center justify-between", children: [jsx("h2", { className: "font-semibold", children: "Versions" }), jsx("span", { className: "text-xs text-text-dim", children: `${schemas.length} total` })] }), schemas.length === 0 ? jsx(Empty, { children: "No drafts yet. Save your first schema." }) : schemas.map((item) => jsxs("div", { className: "rounded border border-border p-3 space-y-2", children: [jsxs("div", { className: "flex items-center justify-between", children: [jsx("span", { className: "font-medium", children: `v${item.version}` }), jsx(StatusPill, { status: item.status })] }), jsx("div", { className: "text-xs text-text-dim", children: item.created_at ? new Date(item.created_at).toLocaleString() : "" }), item.validation_errors?.length > 0 && jsx("div", { className: "text-xs text-red-300", children: `${item.validation_errors.length} validation error(s)` }), jsxs("div", { className: "flex gap-2", children: [jsx(Button, { onClick: () => setSdl(item.sdl), children: "Load" }), item.status !== "published" && item.status !== "invalid" && jsx(Button, { primary: true, onClick: () => publish(item.version), disabled: busy, children: "Publish" })] })] }, item.id || item.version))] })] });
}

function SourcesTab({ projectId, environment, sources, onRefresh, onError }) {
  const [name, setName] = useState(""); const [kind, setKind] = useState("tables"); const [config, setConfig] = useState('{\n  "table": ""\n}'); const [busy, setBusy] = useState(false);
  const defaults = { database: '{\n  "database": "",\n  "collection": ""\n}', tables: '{\n  "table": "",\n  "select": [],\n  "filters": {}\n}', function: '{\n  "name": ""\n}', http: '{\n  "url": "https://",\n  "method": "GET",\n  "headers": {},\n  "query": {}\n}' };
  async function addSource(event) { event.preventDefault(); setBusy(true); onError(null); try { await apiFetch("sources", projectId, environment, { method: "POST", body: JSON.stringify({ name, kind, config: JSON.parse(config || "{}") }) }); setName(""); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_380px] gap-4 p-5", children: [jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold mb-3", children: "Configured sources" }), sources.length === 0 ? jsx(Empty, { children: "No sources configured." }) : jsx("div", { className: "space-y-2", children: sources.map((source) => jsxs("div", { className: "rounded border border-border p-3 flex items-start justify-between gap-3", children: [jsxs("div", { children: [jsx("div", { className: "font-medium", children: source.name }), jsx("div", { className: "text-xs text-text-dim mt-1", children: `${source.kind} · ${JSON.stringify(source.config || {})}` })] }), jsx(StatusPill, { status: source.status })] }, source.id)) })] }), jsxs("form", { className: `${panelClass} space-y-3`, onSubmit: addSource, children: [jsx("h2", { className: "font-semibold", children: "Add or update source" }), jsx(Field, { label: "Name", children: jsx("input", { className: inputClass, value: name, onChange: (event) => setName(event.target.value), placeholder: "orders" }) }), jsx(Field, { label: "Kind", children: jsx("select", { className: inputClass, value: kind, onChange: (event) => { setKind(event.target.value); setConfig(defaults[event.target.value]); }, children: ["database", "tables", "function", "http"].map((value) => jsx("option", { value, children: value }, value)) }) }), jsx(Field, { label: "Configuration", hint: "JSON passed to the source adapter.", children: jsx("textarea", { className: `${inputClass} min-h-[230px] font-mono text-xs`, value: config, onChange: (event) => setConfig(event.target.value), spellCheck: false }) }), jsx(Button, { primary: true, type: "submit", disabled: busy || !name.trim(), children: busy ? "Saving…" : "Save source" })] })] });
}

function ResolversTab({ projectId, environment, sources, resolvers, onRefresh, onError }) {
  const [parentType, setParentType] = useState("Query"); const [fieldName, setFieldName] = useState(""); const [source, setSource] = useState(""); const [operation, setOperation] = useState("find"); const [config, setConfig] = useState("{}"); const [busy, setBusy] = useState(false);
  async function saveResolver(event) { event.preventDefault(); setBusy(true); onError(null); try { await apiFetch("resolvers", projectId, environment, { method: "POST", body: JSON.stringify({ parent_type: parentType, field_name: fieldName, source, operation, config: JSON.parse(config || "{}") }) }); setFieldName(""); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_430px] gap-4 p-5", children: [jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold mb-3", children: "Resolver bindings" }), resolvers.length === 0 ? jsx(Empty, { children: "No resolvers configured." }) : jsx("div", { className: "overflow-x-auto", children: jsxs("table", { className: "w-full text-sm", children: [jsx("thead", { className: "text-left text-text-dim", children: jsx("tr", { children: ["Field", "Source", "Operation", "Config"].map((label) => jsx("th", { className: "pb-2 pr-3 font-normal", children: label }, label)) }) }), jsx("tbody", { children: resolvers.map((item) => jsx("tr", { className: "border-t border-border", children: [jsx("td", { className: "py-2 pr-3 font-mono", children: `${item.parent_type}.${item.field_name}` }), jsx("td", { className: "py-2 pr-3", children: item.source || `#${item.source_id}` }), jsx("td", { className: "py-2 pr-3", children: item.operation }), jsx("td", { className: "py-2 text-xs text-text-dim max-w-[260px] truncate", children: JSON.stringify(item.config || {}) })] }, item.id)) })] }) })] }), jsxs("form", { className: `${panelClass} space-y-3`, onSubmit: saveResolver, children: [jsx("h2", { className: "font-semibold", children: "Bind a field" }), jsxs("div", { className: "grid grid-cols-2 gap-3", children: [jsx(Field, { label: "Parent type", children: jsx("input", { className: inputClass, value: parentType, onChange: (event) => setParentType(event.target.value), placeholder: "Query" }) }), jsx(Field, { label: "Field name", children: jsx("input", { className: inputClass, value: fieldName, onChange: (event) => setFieldName(event.target.value), placeholder: "orders" }) })] }), jsx(Field, { label: "Source", children: jsx("select", { className: inputClass, value: source, onChange: (event) => setSource(event.target.value), children: [jsx("option", { value: "", children: "Select source" }), sources.map((item) => jsx("option", { value: item.name, children: `${item.name} (${item.kind})` }, item.id))] }) }), jsx(Field, { label: "Operation", children: jsx("select", { className: inputClass, value: operation, onChange: (event) => setOperation(event.target.value), children: OPERATIONS.map((item) => jsx("option", { value: item, children: item }, item)) }) }), jsx(Field, { label: "Operation config", hint: "For aggregate: metrics, group_by, where, order, limit.", children: jsx("textarea", { className: `${inputClass} min-h-[190px] font-mono text-xs`, value: config, onChange: (event) => setConfig(event.target.value), spellCheck: false }) }), jsx(Button, { primary: true, type: "submit", disabled: busy || !fieldName.trim() || !source, children: busy ? "Saving…" : "Save resolver" })] })] });
}

function DeployTab({ projectId, environment, schema, schemas, onRefresh, onError }) {
  const [busy, setBusy] = useState(false);
  async function publish(version) { setBusy(true); onError(null); try { await apiFetch("schema/publish", projectId, environment, { method: "POST", body: JSON.stringify({ version }) }); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "p-5 space-y-4", children: [jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold", children: "Environment deployment" }), jsxs("div", { className: "mt-3 flex flex-wrap items-center gap-3", children: [jsx(StatusPill, { status: schema?.status || "not deployed" }), schema && jsx("span", { className: "text-sm text-text-dim", children: `Active editor version v${schema.version}` }), schema && jsx(Button, { primary: true, onClick: () => publish(schema.version), disabled: busy || schema.status === "invalid", children: busy ? "Publishing…" : "Publish current version" })] })] }), jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold mb-3", children: "Promotion history" }), schemas.length === 0 ? jsx(Empty, { children: "No versions available." }) : schemas.map((item) => jsxs("div", { className: "flex flex-wrap items-center justify-between gap-3 border-b border-border py-2 last:border-0", children: [jsxs("div", { className: "flex items-center gap-2", children: [jsx("span", { className: "font-medium", children: `v${item.version}` }), jsx(StatusPill, { status: item.status }), jsx("span", { className: "text-xs text-text-dim", children: item.hash?.slice(0, 10) })] }), item.status !== "published" && item.status !== "invalid" && jsx(Button, { onClick: () => publish(item.version), disabled: busy, children: "Promote" })] }, item.id || item.version))] })] });
}

function LogsTab({ logs, onRefresh, busy }) {
  return jsx("div", { className: "p-5", children: jsxs("section", { className: panelClass, children: [jsxs("div", { className: "flex items-center justify-between mb-3", children: [jsx("h2", { className: "font-semibold", children: "Request logs" }), jsx(Button, { onClick: onRefresh, disabled: busy, children: "Refresh" })] }), logs.length === 0 ? jsx(Empty, { children: "No GraphQL requests recorded yet." }) : jsx("div", { className: "overflow-x-auto", children: jsxs("table", { className: "w-full text-sm", children: [jsx("thead", { className: "text-left text-text-dim", children: jsx("tr", { children: ["Time", "Operation", "Type", "Status", "Duration", "Error"].map((label) => jsx("th", { className: "pb-2 pr-3 font-normal", children: label }, label)) }) }), jsx("tbody", { children: logs.map((item) => jsx("tr", { className: "border-t border-border", children: [jsx("td", { className: "py-2 pr-3 text-xs", children: item.created_at ? new Date(item.created_at).toLocaleString() : "" }), jsx("td", { className: "py-2 pr-3", children: item.operation_name || "-" }), jsx("td", { className: "py-2 pr-3", children: item.operation_type || "-" }), jsx("td", { className: `py-2 pr-3 ${item.status_code >= 400 ? "text-red-300" : "text-green-300"}`, children: item.status_code }), jsx("td", { className: "py-2 pr-3", children: `${item.duration_ms} ms` }), jsx("td", { className: "py-2 text-red-300 max-w-[300px] truncate", children: item.error || "" })] }, item.id)) })] }) })] }) });
}

function RealtimeTab({ projectId, environment, onError }) {
  const [topic, setTopic] = useState("rows.updated"); const [payload, setPayload] = useState('{\n  "id": "demo"\n}'); const [result, setResult] = useState(null); const [busy, setBusy] = useState(false);
  async function publish(event) { event.preventDefault(); setBusy(true); setResult(null); onError(null); try { setResult(await apiFetch("events", projectId, environment, { method: "POST", body: JSON.stringify({ topic, payload: JSON.parse(payload || "{}") }) })); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "p-5", children: [jsxs("form", { className: `${panelClass} max-w-2xl space-y-3`, onSubmit: publish, children: [jsx("h2", { className: "font-semibold", children: "Publish realtime event" }), jsx(Field, { label: "Topic", hint: "Subscribers listen on this exact topic.", children: jsx("input", { className: inputClass, value: topic, onChange: (event) => setTopic(event.target.value), placeholder: "orders.updated" }) }), jsx(Field, { label: "JSON payload", children: jsx("textarea", { className: `${inputClass} min-h-[180px] font-mono text-xs`, value: payload, onChange: (event) => setPayload(event.target.value), spellCheck: false }) }), jsx(Button, { primary: true, type: "submit", disabled: busy || !topic.trim(), children: busy ? "Publishing…" : "Publish event" }), result && jsx("div", { className: "rounded bg-green-400/10 p-3 text-sm text-green-300", children: `Published to ${result.topic}; delivered to ${result.delivered || 0} subscriber(s).` })] })] });
}

export default function GraphQLPanel({ projectId }) {
  const [environment, setEnvironment] = useState("development"); const [tab, setTab] = useState("Schema"); const [schema, setSchema] = useState(null); const [schemas, setSchemas] = useState([]); const [sdl, setSdl] = useState(""); const [sources, setSources] = useState([]); const [resolvers, setResolvers] = useState([]); const [logs, setLogs] = useState([]); const [error, setError] = useState(""); const [loading, setLoading] = useState(false);
  const load = async () => {
    setLoading(true); setError("");
    const results = await Promise.allSettled([apiFetch("schema", projectId, environment).catch((e) => e.message === "published schema not found" ? { schema: null } : Promise.reject(e)), apiFetch("schemas", projectId, environment), apiFetch("sources", projectId, environment), apiFetch("resolvers", projectId, environment), apiFetch("logs", projectId, environment)]);
    const failure = results.find((item) => item.status === "rejected"); if (failure) setError(failure.reason?.message || "Unable to load GraphQL admin data");
    const [active, versions, sourceRows, resolverRows, logRows] = results.map((item) => item.status === "fulfilled" ? item.value : {});
    setSchema(active.schema || versions.schemas?.[0] || null); setSdl(active.schema?.sdl || versions.schemas?.[0]?.sdl || ""); setSchemas(versions.schemas || []); setSources(sourceRows.sources || []); setResolvers(resolverRows.resolvers || []); setLogs(logRows.logs || []); setLoading(false);
  };
  useEffect(() => { load(); }, [projectId, environment]);
  const activeTab = useMemo(() => {
    const common = { projectId, environment, onRefresh: load, onError: (value) => setError(value?.message || "") };
    if (tab === "Sources") return jsx(SourcesTab, { ...common, sources });
    if (tab === "Resolvers") return jsx(ResolversTab, { ...common, sources, resolvers });
    if (tab === "Deploy") return jsx(DeployTab, { ...common, schema, schemas });
    if (tab === "Logs") return jsx(LogsTab, { logs, onRefresh: load, busy: loading });
    if (tab === "Realtime") return jsx(RealtimeTab, { projectId, environment, onError: common.onError });
    return jsx(SchemaTab, { ...common, schema, schemas, sdl, setSdl });
  }, [tab, projectId, environment, schema, schemas, sdl, sources, resolvers, logs, loading]);
  return jsxs("div", { className: "h-full flex flex-col bg-bg text-text", children: [jsxs("header", { className: "px-5 pt-5 border-b border-border", children: [jsxs("div", { className: "flex flex-wrap items-center justify-between gap-3", children: [jsxs("div", { children: [jsx("h1", { className: "text-xl font-semibold", children: "GraphQL" }), jsx("p", { className: "text-sm text-text-dim mt-1", children: "Design schemas, connect sources, bind resolvers, and operate realtime APIs." })] }), jsxs("label", { className: "flex items-center gap-2 text-sm", children: [jsx("span", { className: "text-text-dim", children: "Environment" }), jsx("select", { className: `${inputClass} w-auto`, value: environment, onChange: (event) => setEnvironment(event.target.value), children: ENVIRONMENTS.map((value) => jsx("option", { value, children: value }, value)) })] })] }), jsx("nav", { className: "mt-5 flex gap-1 overflow-x-auto", children: TABS.map((value) => jsx("button", { className: `px-3 py-2 text-sm border-b-2 ${tab === value ? "border-accent text-text" : "border-transparent text-text-dim hover:text-text"}`, onClick: () => setTab(value), children: value }, value)) })] }), error && jsx("div", { className: "mx-5 mt-4 rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-200 flex items-center justify-between", children: [jsx("span", { children: error }), jsx("button", { className: "text-red-200 underline", onClick: () => setError(""), children: "Dismiss" })] }), loading && jsx("div", { className: "px-5 pt-3 text-xs text-text-dim", children: "Loading GraphQL configuration…" }), activeTab] });
}
