import { useEffect, useMemo, useState } from "react";
import { jsx, jsxs } from "react/jsx-runtime";

const API = "/api/apps/graphql";
const ENVIRONMENTS = ["development", "staging", "production"];
const TABS = ["Schema", "Sources", "Resolvers", "Authentication", "Function security", "Deploy", "Logs", "Realtime"];
const OPERATIONS = ["find", "get", "count", "aggregate", "function", "request"];

function requestURL(path, projectId, environment, apiSlug) {
  const params = new URLSearchParams({ project_id: projectId });
  if (environment) params.set("environment", environment);
  if (apiSlug) params.set("api_slug", apiSlug);
  return `${API}/admin/${path}?${params.toString()}`;
}

async function apiFetch(path, projectId, environment, options = {}) {
  const response = await fetch(requestURL(path, projectId, environment, options.apiSlug), {
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

function SchemaTab({ projectId, apiSlug, environment, schema, schemas, sdl, setSdl, onRefresh, onError }) {
  const [busy, setBusy] = useState(false);
  const [validation, setValidation] = useState(null);
  async function validate() { setBusy(true); setValidation(null); try { setValidation(await apiFetch("schema/validate", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ sdl }) })); } catch (error) { onError(error); } finally { setBusy(false); } }
  async function save() { setBusy(true); onError(null); try { await apiFetch("schema", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ sdl }) }); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  async function publish(version = schema?.version) { if (!version) return; setBusy(true); onError(null); try { await apiFetch("schema/publish", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ version }) }); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_300px] gap-4 p-5", children: [
    jsxs("section", { className: panelClass, children: [jsxs("div", { className: "flex items-center justify-between mb-3", children: [jsx("h2", { className: "font-semibold", children: "Schema editor" }), jsxs("div", { className: "flex gap-2", children: [jsx(Button, { onClick: validate, disabled: busy, children: "Validate" }), jsx(Button, { primary: true, onClick: save, disabled: busy || !sdl.trim(), children: busy ? "Working…" : "Save draft" })] })] }), jsx("textarea", { className: `${inputClass} min-h-[430px] font-mono resize-y`, spellCheck: false, value: sdl, onChange: (event) => setSdl(event.target.value), placeholder: "type Query {\n  hello: String!\n}" }), validation && jsxs("div", { className: `mt-3 rounded p-3 text-sm ${validation.valid ? "bg-green-400/10 text-green-300" : "bg-red-400/10 text-red-300"}`, children: [jsx("div", { className: "font-medium", children: validation.valid ? "SDL is valid" : "SDL has validation errors" }), !validation.valid && jsx("ul", { className: "mt-1 list-disc pl-5", children: (validation.validation_errors || []).map((item, index) => jsx("li", { children: item }, index)) })] })] }),
    jsxs("aside", { className: `${panelClass} space-y-3`, children: [jsxs("div", { className: "flex items-center justify-between", children: [jsx("h2", { className: "font-semibold", children: "Versions" }), jsx("span", { className: "text-xs text-text-dim", children: `${schemas.length} total` })] }), schemas.length === 0 ? jsx(Empty, { children: "No drafts yet. Save your first schema." }) : schemas.map((item) => jsxs("div", { className: "rounded border border-border p-3 space-y-2", children: [jsxs("div", { className: "flex items-center justify-between", children: [jsx("span", { className: "font-medium", children: `v${item.version}` }), jsx(StatusPill, { status: item.status })] }), jsx("div", { className: "text-xs text-text-dim", children: item.created_at ? new Date(item.created_at).toLocaleString() : "" }), item.validation_errors?.length > 0 && jsx("div", { className: "text-xs text-red-300", children: `${item.validation_errors.length} validation error(s)` }), jsxs("div", { className: "flex gap-2", children: [jsx(Button, { onClick: () => setSdl(item.sdl), children: "Load" }), item.status !== "published" && item.status !== "invalid" && jsx(Button, { primary: true, onClick: () => publish(item.version), disabled: busy, children: "Publish" })] })] }, item.id || item.version))] })] });
}

function SourcesTab({ projectId, apiSlug, environment, sources, onRefresh, onError }) {
  const [name, setName] = useState(""); const [kind, setKind] = useState("tables"); const [config, setConfig] = useState('{\n  "table": ""\n}'); const [busy, setBusy] = useState(false);
  const defaults = { database: '{\n  "database": "",\n  "collection": ""\n}', tables: '{\n  "table": "",\n  "select": [],\n  "filters": {}\n}', function: '{\n  "name": ""\n}', http: '{\n  "url": "https://",\n  "method": "GET",\n  "headers": {},\n  "query": {}\n}' };
  async function addSource(event) { event.preventDefault(); setBusy(true); onError(null); try { await apiFetch("sources", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ name, kind, config: JSON.parse(config || "{}") }) }); setName(""); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_380px] gap-4 p-5", children: [jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold mb-3", children: "Configured sources" }), sources.length === 0 ? jsx(Empty, { children: "No sources configured." }) : jsx("div", { className: "space-y-2", children: sources.map((source) => jsxs("div", { className: "rounded border border-border p-3 flex items-start justify-between gap-3", children: [jsxs("div", { children: [jsx("div", { className: "font-medium", children: source.name }), jsx("div", { className: "text-xs text-text-dim mt-1", children: `${source.kind} · ${JSON.stringify(source.config || {})}` })] }), jsx(StatusPill, { status: source.status })] }, source.id)) })] }), jsxs("form", { className: `${panelClass} space-y-3`, onSubmit: addSource, children: [jsx("h2", { className: "font-semibold", children: "Add or update source" }), jsx(Field, { label: "Name", children: jsx("input", { className: inputClass, value: name, onChange: (event) => setName(event.target.value), placeholder: "orders" }) }), jsx(Field, { label: "Kind", children: jsx("select", { className: inputClass, value: kind, onChange: (event) => { setKind(event.target.value); setConfig(defaults[event.target.value]); }, children: ["database", "tables", "function", "http"].map((value) => jsx("option", { value, children: value }, value)) }) }), jsx(Field, { label: "Configuration", hint: "JSON passed to the source adapter.", children: jsx("textarea", { className: `${inputClass} min-h-[230px] font-mono text-xs`, value: config, onChange: (event) => setConfig(event.target.value), spellCheck: false }) }), jsx(Button, { primary: true, type: "submit", disabled: busy || !name.trim(), children: busy ? "Saving…" : "Save source" })] })] });
}

function ResolversTab({ projectId, apiSlug, environment, sources, resolvers, onRefresh, onError }) {
  const [parentType, setParentType] = useState("Query"); const [fieldName, setFieldName] = useState(""); const [source, setSource] = useState(""); const [operation, setOperation] = useState("find"); const [config, setConfig] = useState("{}"); const [busy, setBusy] = useState(false);
  async function saveResolver(event) { event.preventDefault(); setBusy(true); onError(null); try { await apiFetch("resolvers", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ parent_type: parentType, field_name: fieldName, source, operation, config: JSON.parse(config || "{}") }) }); setFieldName(""); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_430px] gap-4 p-5", children: [jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold mb-3", children: "Resolver bindings" }), resolvers.length === 0 ? jsx(Empty, { children: "No resolvers configured." }) : jsx("div", { className: "overflow-x-auto", children: jsxs("table", { className: "w-full text-sm", children: [jsx("thead", { className: "text-left text-text-dim", children: jsx("tr", { children: ["Field", "Source", "Operation", "Config"].map((label) => jsx("th", { className: "pb-2 pr-3 font-normal", children: label }, label)) }) }), jsx("tbody", { children: resolvers.map((item) => jsx("tr", { className: "border-t border-border", children: [jsx("td", { className: "py-2 pr-3 font-mono", children: `${item.parent_type}.${item.field_name}` }), jsx("td", { className: "py-2 pr-3", children: item.source || `#${item.source_id}` }), jsx("td", { className: "py-2 pr-3", children: item.operation }), jsx("td", { className: "py-2 text-xs text-text-dim max-w-[260px] truncate", children: JSON.stringify(item.config || {}) })] }, item.id)) })] }) })] }), jsxs("form", { className: `${panelClass} space-y-3`, onSubmit: saveResolver, children: [jsx("h2", { className: "font-semibold", children: "Bind a field" }), jsxs("div", { className: "grid grid-cols-2 gap-3", children: [jsx(Field, { label: "Parent type", children: jsx("input", { className: inputClass, value: parentType, onChange: (event) => setParentType(event.target.value), placeholder: "Query" }) }), jsx(Field, { label: "Field name", children: jsx("input", { className: inputClass, value: fieldName, onChange: (event) => setFieldName(event.target.value), placeholder: "orders" }) })] }), jsx(Field, { label: "Source", children: jsx("select", { className: inputClass, value: source, onChange: (event) => setSource(event.target.value), children: [jsx("option", { value: "", children: "Select source" }), sources.map((item) => jsx("option", { value: item.name, children: `${item.name} (${item.kind})` }, item.id))] }) }), jsx(Field, { label: "Operation", children: jsx("select", { className: inputClass, value: operation, onChange: (event) => setOperation(event.target.value), children: OPERATIONS.map((item) => jsx("option", { value: item, children: item }, item)) }) }), jsx(Field, { label: "Operation config", hint: "For aggregate: metrics, group_by, where, order, limit.", children: jsx("textarea", { className: `${inputClass} min-h-[190px] font-mono text-xs`, value: config, onChange: (event) => setConfig(event.target.value), spellCheck: false }) }), jsx(Button, { primary: true, type: "submit", disabled: busy || !fieldName.trim() || !source, children: busy ? "Saving…" : "Save resolver" })] })] });
}

function AuthenticationTab({ projectId, apiSlug, onError }) {
  const [mode, setMode] = useState("platform");
  const [tenant, setTenant] = useState("default");
  const [environment, setEnvironment] = useState("production");
  const [claims, setClaims] = useState("");
  const [permissions, setPermissions] = useState("");
  const [fields, setFields] = useState("{}");
  const [busy, setBusy] = useState(true);
  const [notice, setNotice] = useState("");
  const [validation, setValidation] = useState(null);
  useEffect(() => {
    let active = true; setBusy(true); setNotice(""); setValidation(null);
    apiFetch("security", projectId, "", { apiSlug }).then(({ security: p }) => {
      if (!active) return;
      setMode(p.mode); setTenant(p.tenant_id || "default"); setEnvironment(p.environment || "production");
      setClaims((p.claims || []).join(", ")); setPermissions((p.permissions || []).join(", "));
      setFields(JSON.stringify(p.fields || {}, null, 2));
    }).catch((err) => { if (active) onError(err); }).finally(() => { if (active) setBusy(false); });
    return () => { active = false; };
  }, [projectId, apiSlug]);
  const list = (text) => text.split(",").map((x) => x.trim()).filter(Boolean);
  async function save(event) {
    event.preventDefault(); setBusy(true); setNotice(""); setValidation(null); onError(null);
    try {
      const security = mode === "platform" ? { mode } : { mode, tenant_id: tenant, environment, claims: list(claims), permissions: list(permissions), fields: JSON.parse(fields) };
      await apiFetch("security", projectId, "", { apiSlug, method: "PUT", body: JSON.stringify({ security }) });
      setNotice("Security policy saved. Applies immediately; Function trust policies were not changed.");
    } catch (err) { onError(err); } finally { setBusy(false); }
  }
  async function validate() {
    setBusy(true); onError(null);
    try { setValidation(await apiFetch("security/validate", projectId, "", { apiSlug, method: "POST" })); }
    catch (err) { onError(err); } finally { setBusy(false); }
  }
  return jsxs("form", { onSubmit: save, className: "p-5 space-y-4 max-w-3xl", children: [
    jsx("h2", { className: "font-semibold", children: "API authentication" }),
    jsx("p", { className: "text-sm text-text-dim", children: "Apteva Auth verifies the user for this API. Field permissions tighten access; they do not provide row-level commercial/team filtering. Keep business authorization in your Functions." }),
    jsx(Field, { label: "Access mode", children: jsx("select", { className: inputClass, value: mode, disabled: busy, onChange: (e) => setMode(e.target.value), children: [jsx("option", { value: "platform", children: "Platform only (existing internal endpoints)" }, "platform"), jsx("option", { value: "auth", children: "Authenticated users — Apteva Auth" }, "auth")] }) }),
    mode === "auth" && jsxs("div", { className: `${panelClass} space-y-4`, children: [
      jsx(Field, { label: "Required Auth tenant / organization slug", children: jsx("input", { className: inputClass, value: tenant, onChange: (e) => setTenant(e.target.value), required: true }) }),
      jsx(Field, { label: "Published environment exposed to users", hint: "Clients cannot override this environment.", children: jsx("select", { className: inputClass, value: environment, onChange: (e) => setEnvironment(e.target.value), children: ENVIRONMENTS.map((value) => jsx("option", { value, children: value }, value)) }) }),
      jsx(Field, { label: "Claims to forward (comma-separated)", hint: "Only server-managed authorization claims, never user metadata or credentials.", children: jsx("input", { className: inputClass, value: claims, placeholder: "roles, permissions, authorization_version", onChange: (e) => setClaims(e.target.value) }) }),
      jsx(Field, { label: "Required API permissions (all, comma-separated)", children: jsx("input", { className: inputClass, value: permissions, onChange: (e) => setPermissions(e.target.value) }) }),
      jsx(Field, { label: "Additional field permissions (JSON)", hint: 'Example: {"Query.reports":["reports:read"]}. Nested fields are checked too.', children: jsx("textarea", { className: `${inputClass} font-mono min-h-[130px]`, value: fields, onChange: (e) => setFields(e.target.value), spellCheck: false }) }),
      jsx("code", { className: "block text-xs break-all", children: `${API}/public/graphql/${apiSlug}?project_id=${encodeURIComponent(projectId)}` }),
      jsx("p", { className: "text-sm text-text-dim", children: "Send the user's Auth bearer token to this endpoint. Project-scoped installations only. Protected subscriptions are disabled in this release. Cross-origin deployment requires platform CORS configuration." }),
    ] }),
    jsxs("div", { className: "flex gap-2", children: [jsx(Button, { primary: true, type: "submit", disabled: busy, children: busy ? "Working…" : "Save security policy" }), jsx(Button, { type: "button", disabled: busy, onClick: validate, children: "Check saved policy & Function trust" })] }),
    notice && jsx("p", { className: "text-sm text-green-300", children: notice }),
    validation && jsxs("div", { className: panelClass, children: [jsx("p", { children: validation.valid ? "Configuration and Function trust checks passed." : "Configuration needs attention:" }), jsx("ul", { className: "list-disc pl-5 text-sm", children: (validation.issues || []).map((issue, index) => jsx("li", { children: issue }, index)) })] }),
  ] });
}

function FunctionSecurityTab({ projectId, apiSlug, sources, resolvers, onRefresh, onError }) {
  const [source, setSource] = useState(""); const [parent, setParent] = useState("Query"); const [field, setField] = useState("");
  const [trusted, setTrusted] = useState(true); const [id, setId] = useState(""); const [ids, setIds] = useState("");
  const [contract, setContract] = useState("http"); const [busy, setBusy] = useState(false); const [notice, setNotice] = useState("");
  const functions = sources.filter((s) => s.kind === "function");
  const bindings = resolvers.filter((r) => functions.some((s) => s.id === r.source_id));
  function edit(r) {
    const s = functions.find((s) => s.id === r.source_id); const c = { ...s?.config, ...r.config };
    setSource(s?.name || ""); setParent(r.parent_type); setField(r.field_name); setTrusted(!!c.authenticated);
    setId(String(c.function_id || "")); setIds((c.function_ids || []).join(", ")); setContract(c.contract || "graphql"); setNotice("");
  }
  async function save(e) {
    e.preventDefault(); setBusy(true); onError(null); setNotice("");
    try {
      const existing = resolvers.find((r) => r.parent_type === parent && r.field_name === field);
      const allowed = ids.split(",").map((x) => x.trim()).filter(Boolean).map(Number);
      if (trusted && (!Number.isSafeInteger(Number(id)) || Number(id) <= 0 || !allowed.length || allowed.some((x) => !Number.isSafeInteger(x) || x <= 0))) throw new Error("Enter positive integer Function IDs and an explicit allowlist.");
      await apiFetch("resolvers", projectId, "", { apiSlug, method: "POST", body: JSON.stringify({ parent_type: parent, field_name: field, source, operation: "function", config: { ...(existing?.config || {}), authenticated: trusted, function_id: Number(id) || 0, function_ids: allowed, contract } }) });
      await onRefresh(); setNotice("Resolver saved. Use Authentication → Check saved policy & Function trust before publishing. Function trust is never granted automatically.");
    } catch (err) { onError(err); } finally { setBusy(false); }
  }
  return jsxs("div", { className: "p-5 space-y-4 max-w-3xl", children: [
    jsx("h2", { className: "font-semibold", children: "Trusted Function resolvers" }),
    jsx("p", { className: "text-sm text-text-dim", children: "Create a Function source in Sources first. Trusted mode requires API Auth and an explicit caller/issuer allowlist in each target Function. Browser input cannot supply the identity or change these IDs." }),
    jsxs("div", { className: "flex flex-wrap gap-2", children: bindings.map((r) => jsx(Button, { onClick: () => edit(r), children: `Edit ${r.parent_type}.${r.field_name}` }, r.id)) }),
    jsxs("form", { className: `${panelClass} space-y-3`, onSubmit: save, children: [
      jsx(Field, { label: "Function source", children: jsx("select", { className: inputClass, value: source, onChange: (e) => setSource(e.target.value), required: true, children: [jsx("option", { value: "", children: "Select Function source" }, "empty"), functions.map((s) => jsx("option", { value: s.name, children: s.name }, s.id))] }) }),
      jsx(Field, { label: "Parent type", children: jsx("input", { className: inputClass, value: parent, required: true, onChange: (e) => setParent(e.target.value) }) }),
      jsx(Field, { label: "Field name", children: jsx("input", { className: inputClass, value: field, required: true, onChange: (e) => setField(e.target.value) }) }),
      jsxs("label", { className: "flex gap-2 text-sm", children: [jsx("input", { type: "checkbox", checked: trusted, onChange: (e) => { setTrusted(e.target.checked); if (!e.target.checked) setContract("graphql"); } }), "Invoke as authenticated user"] }),
      jsx(Field, { label: "Root Function ID", children: jsx("input", { className: inputClass, type: "number", min: 1, value: id, required: trusted, onChange: (e) => setId(e.target.value) }) }),
      jsx(Field, { label: "Allowed Function IDs (comma-separated)", hint: "Include the root and only the nested Functions it needs.", children: jsx("input", { className: inputClass, value: ids, required: trusted, onChange: (e) => setIds(e.target.value) }) }),
      jsx(Field, { label: "Input/output contract", children: jsx("select", { className: inputClass, value: contract, onChange: (e) => setContract(e.target.value), children: [jsx("option", { value: "http", disabled: !trusted, children: "HTTP-compatible: event.body → response.body" }, "http"), jsx("option", { value: "graphql", children: "GraphQL-native: arguments / parent → field data" }, "graphql")] }) }),
      jsx(Button, { primary: true, type: "submit", disabled: busy || !source, children: busy ? "Saving…" : "Save Function resolver" }),
    ] }),
    notice && jsx("p", { className: "text-sm text-green-300", children: notice }),
  ] });
}

function DeployTab({ projectId, apiSlug, environment, schema, schemas, onRefresh, onError }) {
  const [busy, setBusy] = useState(false);
  async function publish(version) { setBusy(true); onError(null); try { await apiFetch("schema/publish", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ version }) }); await onRefresh(); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "p-5 space-y-4", children: [jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold", children: "Environment deployment" }), jsxs("div", { className: "mt-3 flex flex-wrap items-center gap-3", children: [jsx(StatusPill, { status: schema?.status || "not deployed" }), schema && jsx("span", { className: "text-sm text-text-dim", children: `Active editor version v${schema.version}` }), schema && jsx(Button, { primary: true, onClick: () => publish(schema.version), disabled: busy || schema.status === "invalid", children: busy ? "Publishing…" : "Publish current version" })] })] }), jsxs("section", { className: panelClass, children: [jsx("h2", { className: "font-semibold mb-3", children: "Promotion history" }), schemas.length === 0 ? jsx(Empty, { children: "No versions available." }) : schemas.map((item) => jsxs("div", { className: "flex flex-wrap items-center justify-between gap-3 border-b border-border py-2 last:border-0", children: [jsxs("div", { className: "flex items-center gap-2", children: [jsx("span", { className: "font-medium", children: `v${item.version}` }), jsx(StatusPill, { status: item.status }), jsx("span", { className: "text-xs text-text-dim", children: item.hash?.slice(0, 10) })] }), item.status !== "published" && item.status !== "invalid" && jsx(Button, { onClick: () => publish(item.version), disabled: busy, children: "Promote" })] }, item.id || item.version))] })] });
}

function LogsTab({ logs, onRefresh, busy }) {
  return jsx("div", { className: "p-5", children: jsxs("section", { className: panelClass, children: [jsxs("div", { className: "flex items-center justify-between mb-3", children: [jsx("h2", { className: "font-semibold", children: "Request logs" }), jsx(Button, { onClick: onRefresh, disabled: busy, children: "Refresh" })] }), logs.length === 0 ? jsx(Empty, { children: "No GraphQL requests recorded yet." }) : jsx("div", { className: "overflow-x-auto", children: jsxs("table", { className: "w-full text-sm", children: [jsx("thead", { className: "text-left text-text-dim", children: jsx("tr", { children: ["Time", "Operation", "Type", "Status", "Duration", "Error"].map((label) => jsx("th", { className: "pb-2 pr-3 font-normal", children: label }, label)) }) }), jsx("tbody", { children: logs.map((item) => jsx("tr", { className: "border-t border-border", children: [jsx("td", { className: "py-2 pr-3 text-xs", children: item.created_at ? new Date(item.created_at).toLocaleString() : "" }), jsx("td", { className: "py-2 pr-3", children: item.operation_name || "-" }), jsx("td", { className: "py-2 pr-3", children: item.operation_type || "-" }), jsx("td", { className: `py-2 pr-3 ${item.status_code >= 400 ? "text-red-300" : "text-green-300"}`, children: item.status_code }), jsx("td", { className: "py-2 pr-3", children: `${item.duration_ms} ms` }), jsx("td", { className: "py-2 text-red-300 max-w-[300px] truncate", children: item.error || "" })] }, item.id)) })] }) })] }) });
}

function RealtimeTab({ projectId, apiSlug, environment, onError }) {
  const [topic, setTopic] = useState("rows.updated"); const [payload, setPayload] = useState('{\n  "id": "demo"\n}'); const [result, setResult] = useState(null); const [busy, setBusy] = useState(false);
  async function publish(event) { event.preventDefault(); setBusy(true); setResult(null); onError(null); try { setResult(await apiFetch("events", projectId, environment, { apiSlug, method: "POST", body: JSON.stringify({ topic, payload: JSON.parse(payload || "{}") }) })); } catch (error) { onError(error); } finally { setBusy(false); } }
  return jsxs("div", { className: "p-5", children: [jsxs("form", { className: `${panelClass} max-w-2xl space-y-3`, onSubmit: publish, children: [jsx("h2", { className: "font-semibold", children: "Publish realtime event" }), jsx(Field, { label: "Topic", hint: "Subscribers listen on this exact topic.", children: jsx("input", { className: inputClass, value: topic, onChange: (event) => setTopic(event.target.value), placeholder: "orders.updated" }) }), jsx(Field, { label: "JSON payload", children: jsx("textarea", { className: `${inputClass} min-h-[180px] font-mono text-xs`, value: payload, onChange: (event) => setPayload(event.target.value), spellCheck: false }) }), jsx(Button, { primary: true, type: "submit", disabled: busy || !topic.trim(), children: busy ? "Publishing…" : "Publish event" }), result && jsx("div", { className: "rounded bg-green-400/10 p-3 text-sm text-green-300", children: `Published to ${result.topic}; delivered to ${result.delivered || 0} subscriber(s).` })] })] });
}

export default function GraphQLPanel({ projectId }) {
  const [environment, setEnvironment] = useState("development"); const [apiSlug, setApiSlug] = useState("default"); const [apis, setApis] = useState([]); const [tab, setTab] = useState("Schema"); const [schema, setSchema] = useState(null); const [schemas, setSchemas] = useState([]); const [sdl, setSdl] = useState(""); const [sources, setSources] = useState([]); const [resolvers, setResolvers] = useState([]); const [logs, setLogs] = useState([]); const [error, setError] = useState(""); const [loading, setLoading] = useState(false);
  const loadAPIs = async () => { try { const body = await apiFetch("apis", projectId); setApis(body.apis || []); if (!(body.apis || []).some((item) => item.slug === apiSlug)) setApiSlug(body.apis?.[0]?.slug || "default"); } catch (loadError) { setError(loadError.message); } };
  const createAPI = async () => { const slug = window.prompt("API slug (for example analytics)"); if (!slug) return; try { await apiFetch("apis", projectId, "", { method: "POST", body: JSON.stringify({ slug, name: slug }) }); await loadAPIs(); setApiSlug(slug); } catch (createError) { setError(createError.message); } };
  const load = async () => {
    setLoading(true); setError("");
    const results = await Promise.allSettled([apiFetch("schema", projectId, environment, { apiSlug }).catch((e) => e.message === "published schema not found" ? { schema: null } : Promise.reject(e)), apiFetch("schemas", projectId, environment, { apiSlug }), apiFetch("sources", projectId, environment, { apiSlug }), apiFetch("resolvers", projectId, environment, { apiSlug }), apiFetch("logs", projectId, environment, { apiSlug })]);
    const failure = results.find((item) => item.status === "rejected"); if (failure) setError(failure.reason?.message || "Unable to load GraphQL admin data");
    const [active, versions, sourceRows, resolverRows, logRows] = results.map((item) => item.status === "fulfilled" ? item.value : {});
    setSchema(active.schema || versions.schemas?.[0] || null); setSdl(active.schema?.sdl || versions.schemas?.[0]?.sdl || ""); setSchemas(versions.schemas || []); setSources(sourceRows.sources || []); setResolvers(resolverRows.resolvers || []); setLogs(logRows.logs || []); setLoading(false);
  };
  useEffect(() => { loadAPIs(); }, [projectId]);
  useEffect(() => { load(); }, [projectId, environment, apiSlug]);
  const activeTab = useMemo(() => {
    const common = { projectId, environment, onRefresh: load, onError: (value) => setError(value?.message || "") };
    if (tab === "Sources") return jsx(SourcesTab, { ...common, apiSlug, sources });
    if (tab === "Resolvers") return jsx(ResolversTab, { ...common, apiSlug, sources, resolvers });
    if (tab === "Authentication") return jsx(AuthenticationTab, { ...common, apiSlug }, `${projectId}:${apiSlug}`);
    if (tab === "Function security") return jsx(FunctionSecurityTab, { ...common, apiSlug, sources, resolvers }, `${projectId}:${apiSlug}`);
    if (tab === "Deploy") return jsx(DeployTab, { ...common, apiSlug, schema, schemas });
    if (tab === "Logs") return jsx(LogsTab, { logs, onRefresh: load, busy: loading });
    if (tab === "Realtime") return jsx(RealtimeTab, { projectId, apiSlug, environment, onError: common.onError });
    return jsx(SchemaTab, { ...common, apiSlug, schema, schemas, sdl, setSdl });
  }, [tab, projectId, apiSlug, environment, schema, schemas, sdl, sources, resolvers, logs, loading]);
  return jsxs("div", { className: "h-full flex flex-col bg-bg text-text", children: [jsxs("header", { className: "px-5 pt-5 border-b border-border", children: [jsxs("div", { className: "flex flex-wrap items-center justify-between gap-3", children: [jsxs("div", { children: [jsx("h1", { className: "text-xl font-semibold", children: "GraphQL" }), jsx("p", { className: "text-sm text-text-dim mt-1", children: "Design schemas, connect sources, bind resolvers, and operate realtime APIs." })] }), jsxs("div", { className: "flex items-center gap-2 text-sm", children: [jsx("span", { className: "text-text-dim", children: "API" }), jsx("select", { className: `${inputClass} w-auto`, value: apiSlug, onChange: (event) => setApiSlug(event.target.value), children: apis.map((item) => jsx("option", { value: item.slug, children: item.name || item.slug }, item.slug)) }), jsx(Button, { onClick: createAPI, children: "+ API" }), jsx("span", { className: "text-text-dim ml-2", children: "Environment" }), jsx("select", { className: `${inputClass} w-auto`, value: environment, onChange: (event) => setEnvironment(event.target.value), children: ENVIRONMENTS.map((value) => jsx("option", { value, children: value }, value)) })] })] }), jsx("nav", { className: "mt-5 flex gap-1 overflow-x-auto", children: TABS.map((value) => jsx("button", { className: `px-3 py-2 text-sm border-b-2 ${tab === value ? "border-accent text-text" : "border-transparent text-text-dim hover:text-text"}`, onClick: () => setTab(value), children: value }, value)) })] }), error && jsx("div", { className: "mx-5 mt-4 rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-200 flex items-center justify-between", children: [jsx("span", { children: error }), jsx("button", { className: "text-red-200 underline", onClick: () => setError(""), children: "Dismiss" })] }), loading && jsx("div", { className: "px-5 pt-3 text-xs text-text-dim", children: "Loading GraphQL configuration…" }), activeTab] });
}
