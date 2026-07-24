import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps { appName: string; installId: number; projectId: string }
interface CatalogApp { install_id: number; name: string; display_name?: string; description?: string; icon?: string; integration_roles?: IntegrationRole[] }
interface IntegrationRole { role: string; kind?: string; required?: boolean; compatible_slugs?: string[]; label?: string }
interface Connection { id: number; app_slug: string; name: string; status: string }
interface Integration { slug: string; name: string; description?: string; logo?: string; tool_count: number }
interface ManagedMCP { id: number; name: string; description?: string; revision?: string; status: string; tool_count: number }
interface RuntimeManagedMCP { source_id: number; name: string; description?: string; revision?: string; status: string; tool_count: number }
interface Tool { name: string; description?: string; inputSchema?: Record<string, any> }
interface Seed { app: string; tool: string; input: Record<string, any> }
interface WebScenario { id: string; name: string; description: string }
interface SeedField { name: string; label: string; description?: string; default?: any; required?: boolean }
interface WebFixtureCatalog { id: string; name: string; description: string; version: string; scenarios: WebScenario[]; seed_fields?: SeedField[] }
interface WebFixtureSpec { id: string; pack: string; version: string; scenario: string; strict: boolean; seed: Record<string, any> }
interface WebFixtureRun { run_id: string; id: string; pack: string; version: string; scenario: string; status: string; preview_path: string; test_url: string; state?: Record<string, any> }
interface RealtimeProvider { name: string; models?: Record<string, string>; default_voice?: string }
interface VoiceCall {
  id: string;
  status: string;
  error?: string;
  spec: { caller_goal: string; target_agent?: string };
  transcript: { speaker: string; text: string; at_ms: number }[];
  metrics: { duration_ms: number; first_response_ms?: number; average_response_ms?: number; tool_calls: number; interruptions: number; realtime_errors: number; ended_by: string };
}
interface Spec { version: number; ttl_seconds: number; app_install_ids: number[]; connection_ids: number[]; mcp_server_ids: number[]; network_mode: string; integration_mode: string; integration_bindings: any[]; seeds: Seed[]; web_fixtures: WebFixtureSpec[]; snapshot_id?: string }
interface Run { id: string; runtime_id: string; status: string; started_at: string; error?: string; web_fixtures?: WebFixtureRun[] }
interface Environment { id: string; name: string; description?: string; desired_state: string; spec: Spec; active_run?: Run; runtime?: { status: string; apps: any[]; managed_mcps: RuntimeManagedMCP[]; agents: any[]; expires_at: string } }
interface Catalog { apps: CatalogApp[]; connections: Connection[]; integrations: Integration[]; managed_mcps: ManagedMCP[]; web_fixtures: WebFixtureCatalog[]; realtime_providers: RealtimeProvider[]; agents: any[]; snapshots: any[] }

const API = "/api/apps/environments/api";
let activeInstallId = 0;
let activeProjectId = "";

const emptySpec = (): Spec => ({ version: 1, ttl_seconds: 3600, app_install_ids: [], connection_ids: [], mcp_server_ids: [], network_mode: "block", integration_mode: "mock", integration_bindings: [], seeds: [], web_fixtures: [] });

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const url = new URL(API + path, window.location.origin);
  if (activeInstallId) url.searchParams.set("install_id", String(activeInstallId));
  if (activeProjectId) url.searchParams.set("project_id", activeProjectId);
  const response = await fetch(url.pathname + url.search, { ...init, headers: { "Content-Type": "application/json", ...(init?.headers || {}) } });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(body.error || response.statusText);
  }
  return response.json();
}

function appURL(path: string) {
  const url = new URL(API + path, window.location.origin);
  if (activeInstallId) url.searchParams.set("install_id", String(activeInstallId));
  if (activeProjectId) url.searchParams.set("project_id", activeProjectId);
  return url.pathname + url.search;
}

function SearchPicker<T>({ label, placeholder, items, selected, keyOf, titleOf, detailOf, onToggle }: { label: string; placeholder: string; items: T[]; selected: Set<string>; keyOf: (item: T) => string; titleOf: (item: T) => string; detailOf: (item: T) => string; onToggle: (item: T) => void }) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const filtered = useMemo(() => items.filter(item => (titleOf(item) + " " + detailOf(item)).toLowerCase().includes(query.toLowerCase())).slice(0, 8), [items, query, titleOf, detailOf]);
  return <div className="space-y-2">
    <label className="text-[11px] font-semibold uppercase text-text-dim">{label}</label>
    <div className="relative">
      <input value={query} onFocus={() => setOpen(true)} onChange={event => { setQuery(event.target.value); setOpen(true) }} placeholder={placeholder} className="w-full rounded-md border border-border bg-bg px-3 py-2 text-sm text-text outline-none focus:border-accent" />
      {query && <button onClick={() => setQuery("")} className="absolute right-2 top-1.5 h-6 w-6 text-text-dim hover:text-text" title="Clear search">x</button>}
      {open && <div className="absolute z-20 mt-1 max-h-60 w-full overflow-auto rounded-md border border-border bg-surface shadow-xl">
        {filtered.length === 0 ? <p className="p-3 text-xs text-text-dim">No matches</p> : filtered.map(item => {
          const key = keyOf(item); const active = selected.has(key);
          return <button key={key} onClick={() => { onToggle(item); setQuery(""); setOpen(false) }} className="flex w-full items-center gap-3 border-b border-border px-3 py-2 text-left last:border-0 hover:bg-surface-hover">
            <span className={`flex h-4 w-4 shrink-0 items-center justify-center rounded border text-[10px] ${active ? "border-accent bg-accent text-bg" : "border-border"}`}>{active ? "OK" : ""}</span>
            <span className="min-w-0"><span className="block truncate text-sm text-text">{titleOf(item)}</span><span className="block truncate text-xs text-text-dim">{detailOf(item)}</span></span>
          </button>;
        })}
      </div>}
    </div>
  </div>;
}

function Chips<T>({ items, keyOf, labelOf, onRemove }: { items: T[]; keyOf: (item: T) => string; labelOf: (item: T) => string; onRemove: (item: T) => void }) {
  return <div className="flex min-h-8 flex-wrap gap-1.5">{items.length === 0 ? <span className="py-1 text-xs text-text-dim">Nothing selected</span> : items.map(item => <span key={keyOf(item)} className="inline-flex max-w-full items-center gap-1 rounded border border-border bg-surface px-2 py-1 text-xs text-text"><span className="truncate">{labelOf(item)}</span><button onClick={() => onRemove(item)} className="text-text-dim hover:text-red" title="Remove">x</button></span>)}</div>;
}

function SeedEditor({ app, seed, onChange, onRemove }: { app: CatalogApp; seed: Seed; onChange: (seed: Seed) => void; onRemove: () => void }) {
  const [tools, setTools] = useState<Tool[]>([]);
  const [advanced, setAdvanced] = useState(false);
  const [raw, setRaw] = useState(JSON.stringify(seed.input || {}, null, 2));
  useEffect(() => { request<Tool[]>(`/catalog/apps/${app.install_id}/tools`).then(setTools).catch(() => setTools([])) }, [app.install_id]);
  const tool = tools.find(item => item.name === seed.tool);
  const schema = tool?.inputSchema || {};
  const props = schema.properties || {};
  const required: string[] = schema.required || [];
  const fields = Object.keys(props).sort((a, b) => Number(required.includes(b)) - Number(required.includes(a)) || a.localeCompare(b));
  const setField = (key: string, value: string) => {
    const type = props[key]?.type;
    let parsed: any = value;
    if (type === "number" || type === "integer") parsed = value === "" ? undefined : Number(value);
    if (type === "boolean") parsed = value === "true";
    const next = { ...seed.input };
    if (parsed === undefined || parsed === "") delete next[key]; else next[key] = parsed;
    onChange({ ...seed, input: next }); setRaw(JSON.stringify(next, null, 2));
  };
  return <div className="rounded-md border border-border bg-bg">
    <div className="flex items-center gap-2 border-b border-border px-3 py-2"><span className="text-xs font-semibold text-text">{app.display_name || app.name}</span><select value={seed.tool} onChange={event => onChange({ ...seed, tool: event.target.value, input: {} })} className="min-w-0 flex-1 bg-transparent text-xs text-text outline-none"><option value="">Select seed tool</option>{tools.map(item => <option key={item.name} value={item.name}>{item.name}</option>)}</select><button onClick={onRemove} className="h-6 w-6 text-text-dim hover:text-red" title="Remove seed">x</button></div>
    {tool && <div className="space-y-3 p-3">{tool.description && <p className="text-xs text-text-dim">{tool.description}</p>}<div className="grid gap-2 sm:grid-cols-2">{fields.filter(field => advanced || required.includes(field)).map(field => <label key={field} className="space-y-1"><span className="text-[11px] text-text-dim">{field}{required.includes(field) && <span className="text-red"> *</span>}</span>{props[field]?.type === "boolean" ? <select value={String(seed.input?.[field] ?? false)} onChange={event => setField(field, event.target.value)} className="w-full rounded border border-border bg-surface px-2 py-1.5 text-xs"><option value="false">False</option><option value="true">True</option></select> : <input value={typeof seed.input?.[field] === "object" ? JSON.stringify(seed.input[field]) : String(seed.input?.[field] ?? "")} onChange={event => setField(field, event.target.value)} placeholder={props[field]?.description || props[field]?.type || "value"} className="w-full rounded border border-border bg-surface px-2 py-1.5 text-xs text-text outline-none focus:border-accent" />}</label>)}</div><div className="flex gap-3"><button onClick={() => setAdvanced(!advanced)} className="text-xs text-accent hover:underline">{advanced ? "Required fields only" : "Show optional fields"}</button><button onClick={() => setAdvanced(true)} className="text-xs text-text-dim hover:text-text">Raw JSON</button></div>{advanced && <textarea value={raw} onChange={event => setRaw(event.target.value)} onBlur={() => { try { onChange({ ...seed, input: JSON.parse(raw) }) } catch {} }} rows={4} className="w-full rounded border border-border bg-surface p-2 font-mono text-xs text-text outline-none focus:border-accent" />}</div>}
  </div>;
}

function WebsiteEditor({ catalog, value, onChange, onRemove }: { catalog: WebFixtureCatalog; value: WebFixtureSpec; onChange: (value: WebFixtureSpec) => void; onRemove: () => void }) {
  return <div className="rounded-md border border-border bg-bg">
    <div className="flex items-start justify-between gap-3 border-b border-border px-3 py-2"><div className="min-w-0"><div className="text-sm font-semibold text-text">{catalog.name}</div><div className="truncate text-xs text-text-dim">{catalog.description}</div></div><button onClick={onRemove} className="h-7 w-7 shrink-0 text-text-dim hover:text-red" title="Remove website">x</button></div>
    <div className="grid gap-3 p-3 sm:grid-cols-2">
      <label className="space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">Scenario</span><select value={value.scenario} onChange={event => onChange({ ...value, scenario: event.target.value })} className="w-full rounded border border-border bg-surface px-2 py-2 text-sm text-text">{catalog.scenarios.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select><span className="block text-xs text-text-dim">{catalog.scenarios.find(item => item.id === value.scenario)?.description}</span></label>
      <div className="grid gap-2 sm:grid-cols-2">{(catalog.seed_fields || []).map(field => <label key={field.name} className="space-y-1"><span className="text-[11px] text-text-dim">{field.label}{field.required && <span className="text-red"> *</span>}</span><input value={String(value.seed[field.name] ?? field.default ?? "")} onChange={event => onChange({ ...value, seed: { ...value.seed, [field.name]: event.target.value } })} className="w-full rounded border border-border bg-surface px-2 py-1.5 text-xs text-text outline-none focus:border-accent" /></label>)}</div>
    </div>
  </div>;
}

function Builder({ initial, catalog, onClose, onSaved }: { initial?: Environment; catalog: Catalog; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState(initial?.name || "");
  const [description, setDescription] = useState(initial?.description || "");
  const [spec, setSpec] = useState<Spec>({ ...emptySpec(), ...(initial?.spec || {}), mcp_server_ids: initial?.spec.mcp_server_ids || [], web_fixtures: initial?.spec.web_fixtures || [] });
  const [seedApp, setSeedApp] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const selectedApps = catalog.apps.filter(item => spec.app_install_ids.includes(item.install_id));
  const selectedConnections = catalog.connections.filter(item => spec.connection_ids.includes(item.id));
  const selectedMCPs = (catalog.managed_mcps || []).filter(item => spec.mcp_server_ids.includes(item.id));
  const fakeSlugs = new Set(spec.integration_bindings.filter(item => item.expose_to_agents).map(item => item.slug));
  const selectedFakes = catalog.integrations.filter(item => fakeSlugs.has(item.slug));
  const selectedWebPacks = new Set(spec.web_fixtures.map(item => item.pack));
  const suggested = useMemo(() => { const slugs = new Set<string>(); selectedApps.forEach(app => (app.integration_roles || []).forEach(role => (role.compatible_slugs || []).forEach(slug => slugs.add(slug)))); return catalog.integrations.filter(item => slugs.has(item.slug)) }, [selectedApps, catalog.integrations]);
  const toggleApp = (item: CatalogApp) => setSpec(value => ({ ...value, app_install_ids: value.app_install_ids.includes(item.install_id) ? value.app_install_ids.filter(id => id !== item.install_id) : [...value.app_install_ids, item.install_id] }));
  const toggleConnection = (item: Connection) => setSpec(value => ({ ...value, connection_ids: value.connection_ids.includes(item.id) ? value.connection_ids.filter(id => id !== item.id) : [...value.connection_ids, item.id] }));
  const toggleMCP = (item: ManagedMCP) => setSpec(value => ({ ...value, mcp_server_ids: value.mcp_server_ids.includes(item.id) ? value.mcp_server_ids.filter(id => id !== item.id) : [...value.mcp_server_ids, item.id] }));
  const toggleFake = (item: Integration) => setSpec(value => { if (fakeSlugs.has(item.slug)) return { ...value, integration_bindings: value.integration_bindings.filter(binding => binding.slug !== item.slug) }; const roleBindings = selectedApps.flatMap(app => (app.integration_roles || []).filter(role => (role.kind || "integration") !== "app" && (role.compatible_slugs || []).includes(item.slug)).map(role => ({ app: app.name, role: role.role, slug: item.slug, name: `Mock ${item.name}` }))); return { ...value, integration_bindings: [...value.integration_bindings, { slug: item.slug, expose_to_agents: true, name: `Mock ${item.name}` }, ...roleBindings] } });
  const toggleWebsite = (item: WebFixtureCatalog) => setSpec(value => selectedWebPacks.has(item.id) ? { ...value, web_fixtures: value.web_fixtures.filter(fixture => fixture.pack !== item.id) } : { ...value, web_fixtures: [...value.web_fixtures, { id: item.id, pack: item.id, version: item.version, scenario: item.scenarios[0]?.id || "default", strict: true, seed: Object.fromEntries((item.seed_fields || []).map(field => [field.name, field.default])) }] });
  const save = async (start: boolean) => { setSaving(true); setError(""); try { const definition = await request<Environment>(initial ? `/environments/${initial.id}` : "/environments", { method: initial ? "PUT" : "POST", body: JSON.stringify({ id: initial?.id, name, description, spec, desired_state: initial?.desired_state || "stopped" }) }); if (start) await request(`/environments/${definition.id}/start`, { method: "POST" }); onSaved(); onClose() } catch (caught: any) { setError(caught.message) } finally { setSaving(false) } };
  const availableSeedApps = selectedApps.filter(app => !spec.seeds.some(seed => seed.app === app.name));
  return <div className="fixed inset-0 z-40 flex justify-end bg-black/40"><div className="flex h-full w-full max-w-3xl flex-col border-l border-border bg-bg shadow-2xl">
    <header className="flex h-14 items-center justify-between border-b border-border px-5"><div><h2 className="text-sm font-semibold text-text">{initial ? "Edit environment" : "New environment"}</h2><p className="text-xs text-text-dim">{initial?.id || "Isolated project runtime"}</p></div><button onClick={onClose} className="h-8 w-8 text-lg text-text-dim hover:text-text" title="Close">x</button></header>
    <div className="flex-1 space-y-6 overflow-y-auto p-5">
      <section className="grid gap-3 sm:grid-cols-2"><label className="space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">Name</span><input value={name} onChange={event => setName(event.target.value)} autoFocus className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm text-text outline-none focus:border-accent" /></label><label className="space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">TTL</span><select value={spec.ttl_seconds} onChange={event => setSpec({ ...spec, ttl_seconds: Number(event.target.value) })} className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm"><option value={900}>15 minutes</option><option value={3600}>1 hour</option><option value={14400}>4 hours</option><option value={86400}>24 hours</option></select></label><label className="space-y-1 sm:col-span-2"><span className="text-[11px] font-semibold uppercase text-text-dim">Description</span><input value={description} onChange={event => setDescription(event.target.value)} className="w-full rounded-md border border-border bg-surface px-3 py-2 text-sm text-text outline-none focus:border-accent" /></label></section>
      <section className="space-y-3"><SearchPicker label="Apps" placeholder="Search installed apps" items={catalog.apps} selected={new Set(spec.app_install_ids.map(String))} keyOf={item => String(item.install_id)} titleOf={item => item.display_name || item.name} detailOf={item => item.description || item.name} onToggle={toggleApp} /><Chips items={selectedApps} keyOf={item => String(item.install_id)} labelOf={item => item.display_name || item.name} onRemove={toggleApp} /></section>
      <section className="space-y-3"><SearchPicker label="Managed MCP servers" placeholder="Search project MCP servers" items={catalog.managed_mcps || []} selected={new Set(spec.mcp_server_ids.map(String))} keyOf={item => String(item.id)} titleOf={item => item.name} detailOf={item => item.description || `${item.tool_count} tools`} onToggle={toggleMCP} /><Chips items={selectedMCPs} keyOf={item => String(item.id)} labelOf={item => `${item.name} - ${item.tool_count} tools`} onRemove={toggleMCP} /></section>
      <section className="space-y-3"><SearchPicker label="Existing connections" placeholder="Search project connections" items={catalog.connections} selected={new Set(spec.connection_ids.map(String))} keyOf={item => String(item.id)} titleOf={item => item.name} detailOf={item => item.app_slug} onToggle={toggleConnection} /><Chips items={selectedConnections} keyOf={item => String(item.id)} labelOf={item => `${item.name} - ${item.app_slug}`} onRemove={toggleConnection} /></section>
      <section className="space-y-3"><SearchPicker label="Fake connections" placeholder="Search integration catalog" items={catalog.integrations} selected={fakeSlugs} keyOf={item => item.slug} titleOf={item => item.name} detailOf={item => item.description || `${item.tool_count} tools`} onToggle={toggleFake} />{suggested.length > 0 && <div className="flex flex-wrap items-center gap-1.5"><span className="text-xs text-text-dim">Suggested:</span>{suggested.filter(item => !fakeSlugs.has(item.slug)).slice(0, 6).map(item => <button key={item.slug} onClick={() => toggleFake(item)} className="rounded border border-border px-2 py-1 text-xs text-text hover:border-accent">+ {item.name}</button>)}</div>}<Chips items={selectedFakes} keyOf={item => item.slug} labelOf={item => `Mock ${item.name}`} onRemove={toggleFake} /></section>
      <section className="space-y-3"><SearchPicker label="Websites" placeholder="Search simulated websites" items={catalog.web_fixtures || []} selected={selectedWebPacks} keyOf={item => item.id} titleOf={item => item.name} detailOf={item => item.description} onToggle={toggleWebsite} /><div className="space-y-2">{spec.web_fixtures.map((fixture, index) => { const item = catalog.web_fixtures?.find(candidate => candidate.id === fixture.pack); return item ? <WebsiteEditor key={fixture.id} catalog={item} value={fixture} onChange={next => setSpec(value => ({ ...value, web_fixtures: value.web_fixtures.map((current, currentIndex) => currentIndex === index ? next : current) }))} onRemove={() => toggleWebsite(item)} /> : null })}</div></section>
      <section className="space-y-3"><div className="flex items-center justify-between"><h3 className="text-[11px] font-semibold uppercase text-text-dim">Seed plan</h3><select value={seedApp} onChange={event => { const app = catalog.apps.find(item => item.name === event.target.value); if (app) { setSpec(value => ({ ...value, seeds: [...value.seeds, { app: app.name, tool: "", input: {} }] })); setSeedApp("") } }} className="rounded border border-border bg-surface px-2 py-1.5 text-xs"><option value="">Add seed step</option>{availableSeedApps.map(app => <option key={app.install_id} value={app.name}>{app.display_name || app.name}</option>)}</select></div><div className="space-y-2">{spec.seeds.length === 0 ? <div className="rounded-md border border-dashed border-border p-4 text-center text-xs text-text-dim">No seed steps</div> : spec.seeds.map((seed, index) => { const app = catalog.apps.find(item => item.name === seed.app); return app ? <SeedEditor key={`${seed.app}-${index}`} app={app} seed={seed} onChange={next => setSpec(value => ({ ...value, seeds: value.seeds.map((current, currentIndex) => currentIndex === index ? next : current) }))} onRemove={() => setSpec(value => ({ ...value, seeds: value.seeds.filter((_, currentIndex) => currentIndex !== index) }))} /> : null })}</div></section>
      <section className="grid gap-3 sm:grid-cols-2"><label className="space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">Network</span><select value={spec.network_mode} onChange={event => setSpec({ ...spec, network_mode: event.target.value })} className="w-full rounded border border-border bg-surface px-2 py-2 text-sm"><option value="block">Block external network</option><option value="record">Record traffic</option><option value="replay">Replay cassette</option><option value="passthrough">Allow network</option></select></label><label className="space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">Integrations</span><select value={spec.integration_mode} onChange={event => setSpec({ ...spec, integration_mode: event.target.value })} className="w-full rounded border border-border bg-surface px-2 py-2 text-sm"><option value="mock">Fake responses</option><option value="real">Real connections</option></select></label></section>
      {error && <p className="rounded border border-red/40 bg-red/10 p-3 text-xs text-red">{error}</p>}
    </div>
    <footer className="flex items-center justify-end gap-2 border-t border-border px-5 py-3"><button onClick={onClose} className="rounded-md border border-border px-4 py-2 text-sm text-text hover:bg-surface">Cancel</button><button disabled={saving || !name} onClick={() => save(false)} className="rounded-md border border-border px-4 py-2 text-sm text-text hover:bg-surface disabled:opacity-40">Save</button><button disabled={saving || !name} onClick={() => save(true)} className="rounded-md bg-accent px-4 py-2 text-sm font-semibold text-bg hover:bg-accent-hover disabled:opacity-40">Save and start</button></footer>
  </div></div>;
}

function Metric({ label, value }: { label: string; value: string }) { return <div className="rounded border border-border bg-surface p-3"><span className="block text-[11px] uppercase text-text-dim">{label}</span><span className="mt-1 block truncate text-sm font-semibold">{value}</span></div> }
function CompactList({ rows, empty }: { rows: { title: string; detail: string; code?: string }[]; empty: string }) { if (rows.length === 0) return <p className="py-8 text-center text-xs text-text-dim">{empty}</p>; return <div className="divide-y divide-border">{rows.map((item, index) => <div key={`${item.title}-${index}`} className="py-2 first:pt-0 last:pb-0"><div className="text-xs font-medium text-text">{item.title}</div><div className="text-xs text-text-dim">{item.detail}</div>{item.code && <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap rounded bg-bg p-2 text-[10px] text-text-dim">{item.code}</pre>}</div>)}</div> }

function WebsiteInspector({ run, fixtures }: { run: Run; fixtures: WebFixtureRun[] }) {
  const [selected, setSelected] = useState(fixtures[0]?.id || "");
  const [detail, setDetail] = useState<any>(null);
  const [error, setError] = useState("");
  const load = useCallback(async (id: string) => { if (!id) return; try { setDetail(await request(`/runs/${run.id}/fixtures/${id}`)); setError("") } catch (caught: any) { setError(caught.message) } }, [run.id]);
  useEffect(() => { if (!fixtures.some(item => item.id === selected)) setSelected(fixtures[0]?.id || "") }, [fixtures, selected]);
  useEffect(() => { load(selected) }, [load, selected]);
  const current = fixtures.find(item => item.id === selected);
  const reset = async () => { if (!current) return; try { setDetail(await request(`/runs/${run.id}/fixtures/${current.id}/reset`, { method: "POST" })); setError("") } catch (caught: any) { setError(caught.message) } };
  if (fixtures.length === 0) return <p className="py-8 text-center text-xs text-text-dim">No websites in this environment</p>;
  return <div className="grid gap-4 lg:grid-cols-[260px_minmax(0,1fr)]">
    <div className="overflow-hidden rounded-md border border-border">{fixtures.map(item => <button key={item.id} onClick={() => setSelected(item.id)} className={`block w-full border-b border-border px-3 py-3 text-left last:border-0 ${selected === item.id ? "bg-surface" : "hover:bg-surface/50"}`}><span className="block text-sm font-medium capitalize">{item.pack}</span><span className="block text-xs text-text-dim">{item.scenario}</span></button>)}</div>
    <div className="min-w-0 space-y-3">{current && <div className="flex flex-wrap items-center justify-between gap-2"><div><h3 className="text-sm font-semibold capitalize">{current.pack}</h3><p className="font-mono text-xs text-text-dim">{current.id}</p></div><div className="flex gap-2"><button onClick={() => window.open(current.preview_path, "_blank", "noopener,noreferrer")} className="rounded-md bg-accent px-3 py-1.5 text-xs font-semibold text-bg">Open preview</button><button onClick={() => navigator.clipboard.writeText(new URL(current.preview_path, window.location.origin).toString())} className="rounded-md border border-border px-3 py-1.5 text-xs hover:bg-surface">Copy URL</button><button onClick={reset} className="rounded-md border border-border px-3 py-1.5 text-xs hover:border-red hover:text-red">Reset</button></div></div>}
      {error && <p className="rounded border border-red/40 bg-red/10 p-2 text-xs text-red">{error}</p>}
      {detail && <div className="grid gap-3 xl:grid-cols-2"><section><h4 className="mb-2 text-[11px] font-semibold uppercase text-text-dim">State</h4><pre className="max-h-96 overflow-auto rounded-md border border-border bg-bg p-3 text-[11px] text-text">{JSON.stringify(detail.state, null, 2)}</pre></section><section><h4 className="mb-2 text-[11px] font-semibold uppercase text-text-dim">Events</h4><div className="max-h-96 overflow-auto rounded-md border border-border px-3">{detail.events?.length ? detail.events.map((event: any) => <div key={event.id} className="border-b border-border py-3 last:border-0"><div className="text-xs font-semibold text-text">{event.type}</div><div className="mt-1 text-[11px] text-text-dim">{new Date(event.created_at).toLocaleString()}</div><pre className="mt-1 whitespace-pre-wrap text-[10px] text-text-dim">{JSON.stringify(event.data, null, 2)}</pre></div>) : <p className="py-8 text-center text-xs text-text-dim">No events yet</p>}</div></section></div>}
    </div>
  </div>;
}

function VoiceInspector({ run, agents, providers }: { run: Run; agents: any[]; providers: RealtimeProvider[] }) {
  const [calls, setCalls] = useState<VoiceCall[]>([]);
  const [selected, setSelected] = useState("");
  const [agent, setAgent] = useState(agents[0]?.alias || "");
  const [goal, setGoal] = useState("");
  const [provider, setProvider] = useState(providers[0]?.name || "");
  const [voice, setVoice] = useState(providers[0]?.default_voice || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => { try { const rows = await request<VoiceCall[]>(`/runs/${run.id}/voice-calls`); setCalls(rows); setSelected(current => current || rows[0]?.id || ""); setError("") } catch (caught: any) { setError(caught.message) } }, [run.id]);
  useEffect(() => { load() }, [load]);
  useEffect(() => { if (!agents.some(item => item.alias === agent)) setAgent(agents[0]?.alias || "") }, [agents, agent]);
  const start = async () => {
    setBusy(true);
    setError("");
    try {
      const call = await request<VoiceCall>(`/runs/${run.id}/voice-calls`, { method: "POST", body: JSON.stringify({ caller_goal: goal.trim(), caller_persona: "A natural, concise customer", caller_behavior: "Ask follow-up questions when needed. End the call once the goal is resolved or clearly cannot be resolved.", target_agent: agent, provider, caller_provider: provider, voice, caller_voice: voice, timeout_seconds: 90 }) });
      setCalls(current => [call, ...current.filter(item => item.id !== call.id)]);
      setSelected(call.id);
      setGoal("");
    } catch (caught: any) {
      setError(caught.message);
      await load();
    } finally {
      setBusy(false);
    }
  };
  const call = calls.find(item => item.id === selected);
  if (agents.length === 0) return <p className="py-8 text-center text-xs text-text-dim">This runtime has no agents. Add an agent to the environment or launch the call from Evals.</p>;
  return <div className="grid gap-4 lg:grid-cols-[300px_minmax(0,1fr)]">
    <div className="space-y-3">
      <div className="space-y-3 rounded-md border border-border bg-surface p-3">
        <label className="block space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">Receptionist</span><select value={agent} onChange={event => setAgent(event.target.value)} className="w-full rounded border border-border bg-bg px-2 py-2 text-sm">{agents.map(item => <option key={item.alias} value={item.alias}>{item.alias || item.name}</option>)}</select></label>
        <label className="block space-y-1"><span className="text-[11px] font-semibold uppercase text-text-dim">What does the caller need?</span><textarea rows={4} value={goal} onChange={event => setGoal(event.target.value)} placeholder="Book an appointment for tomorrow afternoon." className="w-full resize-y rounded border border-border bg-bg p-2 text-sm outline-none focus:border-accent" /></label>
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-1 xl:grid-cols-2"><label className="space-y-1"><span className="text-[11px] text-text-dim">Provider</span><select value={provider} onChange={event => { const next = providers.find(item => item.name === event.target.value); setProvider(event.target.value); setVoice(next?.default_voice || "") }} className="w-full rounded border border-border bg-bg px-2 py-2 text-xs">{providers.map(item => <option key={item.name} value={item.name}>{item.name}</option>)}</select></label><label className="space-y-1"><span className="text-[11px] text-text-dim">Voice</span><input value={voice} onChange={event => setVoice(event.target.value)} placeholder="Default" className="w-full rounded border border-border bg-bg px-2 py-2 text-xs" /></label></div>
        {providers.length === 0 && <p className="text-xs text-red">No realtime voice provider is configured.</p>}
        <button disabled={busy || !goal.trim() || !agent || !provider} onClick={start} className="w-full rounded bg-accent px-3 py-2 text-sm font-semibold text-bg disabled:opacity-40">{busy ? "Call in progress..." : "Start simulated call"}</button>
        {error && <p className="text-xs text-red">{error}</p>}
      </div>
      <div className="overflow-hidden rounded-md border border-border">{calls.length === 0 ? <p className="p-4 text-center text-xs text-text-dim">No calls yet</p> : calls.map(item => <button key={item.id} onClick={() => setSelected(item.id)} className={`block w-full border-b border-border p-3 text-left last:border-0 ${selected === item.id ? "bg-surface" : "hover:bg-surface/50"}`}><span className="block truncate text-xs font-medium">{item.spec.caller_goal}</span><span className="mt-1 block text-[11px] text-text-dim">{item.status} - {(item.metrics.duration_ms / 1000).toFixed(1)}s</span></button>)}</div>
    </div>
    <div className="min-w-0">{call ? <div className="space-y-3">
      <div className="grid gap-2 sm:grid-cols-3 xl:grid-cols-6"><Metric label="Status" value={call.status} /><Metric label="Duration" value={`${(call.metrics.duration_ms / 1000).toFixed(1)}s`} /><Metric label="First response" value={call.metrics.first_response_ms ? `${call.metrics.first_response_ms}ms` : "-"} /><Metric label="Average response" value={call.metrics.average_response_ms ? `${call.metrics.average_response_ms}ms` : "-"} /><Metric label="Tool calls" value={String(call.metrics.tool_calls)} /><Metric label="Audio errors" value={String(call.metrics.realtime_errors)} /></div>
      {call.error && <p className="rounded border border-red/40 bg-red/10 p-2 text-xs text-red">{call.error}</p>}
      <div className="grid gap-3 sm:grid-cols-2"><label><span className="mb-1 block text-[11px] uppercase text-text-dim">Receptionist recording</span><audio controls preload="none" src={appURL(`/voice-recordings/${call.id}/receptionist.wav`)} className="h-9 w-full" /></label><label><span className="mb-1 block text-[11px] uppercase text-text-dim">Caller recording</span><audio controls preload="none" src={appURL(`/voice-recordings/${call.id}/caller.wav`)} className="h-9 w-full" /></label></div>
      <div className="overflow-hidden rounded-md border border-border">{call.transcript.length === 0 ? <p className="p-6 text-center text-xs text-text-dim">No transcript recorded</p> : call.transcript.map((turn, index) => <div key={`${turn.at_ms}-${index}`} className="border-b border-border p-3 last:border-0"><div className="text-[10px] font-semibold uppercase text-text-dim">{turn.speaker} - {(turn.at_ms / 1000).toFixed(1)}s</div><p className="mt-1 whitespace-pre-wrap text-sm text-text">{turn.text}</p></div>)}</div>
    </div> : <p className="py-8 text-center text-xs text-text-dim">Start or select a call to inspect it.</p>}</div>
  </div>;
}

function Detail({ environment, catalog, onEdit, onRefresh }: { environment: Environment; catalog: Catalog; onEdit: () => void; onRefresh: () => void }) {
  const tabs = ["Overview", "Websites", "Voice calls", "Apps", "MCP servers", "Agents", "Activity", "Network", "Snapshots"];
  const [tab, setTab] = useState("Overview");
  const [inspect, setInspect] = useState<any>(null);
  const [error, setError] = useState("");
  const run = environment.active_run;
  const load = useCallback(async () => { if (!run) return; try { const agent = environment.runtime?.agents?.[0]?.alias || ""; setInspect(await request(`/runs/${run.id}/inspect${agent ? `?agent=${encodeURIComponent(agent)}` : ""}`)); setError("") } catch (caught: any) { setError(caught.message) } }, [run?.id, environment.runtime?.agents?.length]);
  useEffect(() => { load(); if (!run) return; const timer = setInterval(load, 5000); return () => clearInterval(timer) }, [load, run?.id]);
  const runtime = inspect?.runtime || environment.runtime;
  const edge: any[] = inspect?.edge_calls || [];
  const telemetry: any[] = inspect?.telemetry || [];
  const fixtures: WebFixtureRun[] = inspect?.web_fixtures || run?.web_fixtures || [];
  const snapshot = async () => { try { await request(`/environments/${environment.id}/snapshot`, { method: "POST", body: JSON.stringify({ description: `${environment.name} snapshot` }) }); onRefresh() } catch (caught: any) { setError(caught.message) } };
  return <section className="mt-4 overflow-hidden rounded-md border border-border">
    <header className="flex items-center justify-between border-b border-border bg-surface px-4 py-3"><div><h2 className="text-sm font-semibold">{environment.name}</h2><p className="font-mono text-xs text-text-dim">{run?.runtime_id || environment.id}</p></div><button onClick={onEdit} className="rounded border border-border px-3 py-1.5 text-xs hover:bg-bg">Edit definition</button></header>
    <nav className="flex overflow-x-auto border-b border-border px-2">{tabs.map(item => <button key={item} onClick={() => setTab(item)} className={`border-b-2 px-3 py-2 text-xs ${tab === item ? "border-accent text-text" : "border-transparent text-text-dim hover:text-text"}`}>{item}</button>)}</nav>
    {error && <p className="border-b border-border bg-red/10 px-4 py-2 text-xs text-red">{error}</p>}
    <div className="min-h-44 p-4">
      {tab === "Overview" && <div className="grid gap-3 sm:grid-cols-3 xl:grid-cols-6"><Metric label="Status" value={run?.status || "stopped"} /><Metric label="Apps" value={String(runtime?.apps?.length || 0)} /><Metric label="MCP servers" value={String(runtime?.managed_mcps?.length || 0)} /><Metric label="Websites" value={String(fixtures.length)} /><Metric label="Agents" value={String(runtime?.agents?.length || 0)} /><Metric label="Edge calls" value={String(edge.length)} /><div className="grid gap-2 text-xs text-text-dim sm:col-span-3 sm:grid-cols-3 xl:col-span-6"><span>Network: <b className="text-text">{environment.spec.network_mode}</b></span><span>Integrations: <b className="text-text">{environment.spec.integration_mode}</b></span><span>Expires: <b className="text-text">{runtime?.expires_at ? new Date(runtime.expires_at).toLocaleString() : "-"}</b></span></div></div>}
      {tab === "Websites" && (run ? <WebsiteInspector run={run} fixtures={fixtures} /> : <p className="py-8 text-center text-xs text-text-dim">Start the environment to open its websites</p>)}
      {tab === "Voice calls" && (run ? <VoiceInspector run={run} agents={runtime?.agents || []} providers={catalog.realtime_providers || []} /> : <p className="py-8 text-center text-xs text-text-dim">Start the environment to run a voice call</p>)}
      {tab === "Apps" && <CompactList empty="No apps" rows={(runtime?.apps || []).map((item: any) => ({ title: item.name, detail: `${item.status || "running"} - ${item.kind || "app"}` }))} />}
      {tab === "MCP servers" && <CompactList empty="No managed MCP servers" rows={(runtime?.managed_mcps || []).map((item: RuntimeManagedMCP) => ({ title: item.name, detail: `${item.status || "unknown"} - ${item.tool_count || 0} tools${item.revision ? ` - ${item.revision}` : ""}` }))} />}
      {tab === "Agents" && <CompactList empty="No agents" rows={(runtime?.agents || []).map((item: any) => ({ title: item.alias || item.name, detail: `${item.status || "unknown"}${item.model ? ` - ${item.model}` : ""}` }))} />}
      {tab === "Activity" && <CompactList empty="No telemetry" rows={telemetry.slice().reverse().map((item: any) => ({ title: item.type, detail: item.time ? new Date(item.time).toLocaleString() : "", code: JSON.stringify(item.data, null, 2) }))} />}
      {tab === "Network" && <CompactList empty="No edge calls" rows={edge.slice().reverse().map((item: any) => ({ title: `${item.method} ${item.host}${item.path}`, detail: `${item.status || "-"} - ${item.mocked ? "mocked" : item.blocked ? "blocked" : item.allowed ? "allowed" : "observed"}` }))} />}
      {tab === "Snapshots" && <div className="flex items-center justify-between"><p className="text-xs text-text-dim">Snapshot runtime apps and website state.</p><button disabled={!run} onClick={snapshot} className="rounded bg-accent px-3 py-1.5 text-xs font-semibold text-bg disabled:opacity-40">Create snapshot</button></div>}
    </div>
  </section>;
}

export default function EnvironmentsPanel({ installId, projectId }: NativePanelProps) {
  activeInstallId = installId;
  activeProjectId = projectId;
  const [rows, setRows] = useState<Environment[]>([]);
  const [catalog, setCatalog] = useState<Catalog>({ apps: [], connections: [], integrations: [], managed_mcps: [], web_fixtures: [], realtime_providers: [], agents: [], snapshots: [] });
  const [builder, setBuilder] = useState<Environment | true | null>(null);
  const [selectedID, setSelectedID] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(async () => { try { const [environments, nextCatalog] = await Promise.all([request<Environment[]>("/environments"), request<Catalog>("/catalog")]); setRows(environments); setCatalog(nextCatalog); setError("") } catch (caught: any) { setError(caught.message) } }, []);
  useEffect(() => { load(); const timer = setInterval(load, 5000); return () => clearInterval(timer) }, [load]);
  const act = async (id: string, action: string) => { setBusy(id + action); try { await request(`/environments/${id}/${action}`, { method: "POST" }); await load() } catch (caught: any) { setError(caught.message) } finally { setBusy("") } };
  const selected = rows.find(item => item.id === selectedID);
  return <div className="h-full overflow-auto bg-bg text-text"><div className="w-full p-5">
    <header className="mb-5 flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between"><div><h1 className="text-xl font-semibold">Environments</h1><p className="text-sm text-text-dim">Apps, managed MCP servers, connections, agents, and simulated websites in isolated runtimes.</p></div><button onClick={() => setBuilder(true)} className="rounded-md bg-accent px-3 py-2 text-sm font-semibold text-bg hover:bg-accent-hover">New environment</button></header>
    {error && <div className="mb-4 flex items-center justify-between rounded-md border border-red/40 bg-red/10 px-3 py-2 text-xs text-red"><span>{error}</span><button onClick={() => setError("")} title="Dismiss">x</button></div>}
    <div className="overflow-hidden rounded-md border border-border"><div className="hidden grid-cols-[minmax(0,1.6fr)_100px_1fr_150px] gap-3 border-b border-border bg-surface px-4 py-2 text-[11px] font-semibold uppercase text-text-dim sm:grid"><span>Environment</span><span>Status</span><span>Resources</span><span className="text-right">Actions</span></div>{rows.length === 0 ? <div className="p-10 text-center"><p className="text-sm text-text-muted">No environments yet</p><p className="mt-1 text-xs text-text-dim">Create one from this project.</p></div> : rows.map(row => { const running = !!row.active_run && row.active_run.status === "running"; return <div key={row.id} className={`grid grid-cols-1 items-center gap-2 border-b border-border px-4 py-3 last:border-0 hover:bg-surface/50 sm:grid-cols-[minmax(0,1.6fr)_100px_1fr_150px] sm:gap-3 ${selectedID === row.id ? "bg-surface" : ""}`}><button onClick={() => setSelectedID(row.id)} className="min-w-0 text-left"><span className="block truncate text-sm font-medium">{row.name}</span><span className="block truncate text-xs text-text-dim">{row.description || row.id}</span></button><span className={`text-xs font-medium ${running ? "text-green" : row.active_run?.status === "failed" ? "text-red" : "text-text-dim"}`}>{row.active_run?.status || "stopped"}</span><div className="flex min-w-0 flex-wrap gap-2 text-xs text-text-dim"><span>{row.spec.app_install_ids?.length || 0} apps</span><span>{row.spec.mcp_server_ids?.length || 0} MCPs</span><span>{(row.spec.connection_ids?.length || 0) + (row.spec.integration_bindings?.length || 0)} connections</span><span>{row.spec.web_fixtures?.length || 0} websites</span></div><div className="flex justify-start gap-1 sm:justify-end">{running ? <><button onClick={() => act(row.id, "snapshot")} disabled={!!busy} className="rounded border border-border px-2 py-1 text-xs hover:bg-surface" title="Create snapshot">Snapshot</button><button onClick={() => act(row.id, "stop")} disabled={!!busy} className="rounded border border-border px-2 py-1 text-xs hover:border-red hover:text-red">Stop</button></> : <button onClick={() => act(row.id, "start")} disabled={!!busy} className="rounded bg-accent px-3 py-1 text-xs font-semibold text-bg">Start</button>}</div></div> })}</div>
    {selected && <Detail environment={selected} catalog={catalog} onEdit={() => setBuilder(selected)} onRefresh={load} />}
  </div>{builder && <Builder initial={builder === true ? undefined : builder} catalog={catalog} onClose={() => setBuilder(null)} onSaved={load} />}</div>;
}
