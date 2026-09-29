import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/bench";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Scope {
  projectId: string;
  installId: number;
}

interface Budget {
  duration_ms: number;
  cost_usd: number;
  tokens_total: number;
  turns: number;
}

interface Check {
  name: string;
  type?: string;
  app?: string;
  tool?: string;
  input?: Record<string, unknown>;
  path?: string;
  equals?: unknown;
}

interface Scenario {
  id: string;
  name: string;
  prompt: string;
  goals?: string[];
  environment_id?: string;
  snapshot_id?: string;
  checks: Check[];
  budget: Budget;
  timeout_seconds?: number;
  max_turns?: number;
  weight?: number;
}

interface Pack {
  id: string;
  name: string;
  description: string;
  state: "draft" | "sealed";
  version?: string;
  digest?: string;
  scoring_version?: string;
  scenarios: Scenario[];
  updated_at: string;
}

interface Target {
  agent_id: number;
  agent_name?: string;
  provider?: string;
  model?: string;
}

interface TargetSummary {
  target_index: number;
  label: string;
  runs: number;
  verified: number;
  invalid: number;
  passed: number;
  pass_rate: number;
  average_score: number;
  average_duration_ms: number;
  average_tokens: number;
}

interface Summary {
  total: number;
  verified: number;
  invalid: number;
  passed: number;
  pass_rate: number;
  average_score: number;
  targets: TargetSummary[];
}

interface Result {
  id: string;
  scenario_id: string;
  scenario_name: string;
  target: Target;
  trial: number;
  admission: string;
  invalid_reason?: string;
  passed: boolean;
  score: { score: number; cost_basis?: string };
  metrics: { duration_ms: number; turns_used: number; tokens_total: number; cost_usd: number; errors: number };
  error?: string;
}

interface Run {
  id: string;
  pack_id: string;
  pack_name: string;
  pack_version: string;
  pack_digest: string;
  name: string;
  trials: number;
  status: string;
  summary: Summary;
  results?: Result[];
  error?: string;
  created_at: string;
}

interface LeaderboardRow {
  label: string;
  runs: number;
  pass_rate: number;
  average_score: number;
  average_duration_ms: number;
  average_tokens: number;
  scenarios: number;
  packs?: number;
  mixed_cost_basis: boolean;
  components?: Record<string, number>;
}

interface ScenarioRow {
  scenario_id: string;
  scenario_name: string;
  label: string;
  runs: number;
  pass_rate: number;
  average_score: number;
}

interface GlobalBoard {
  scoring_version: string;
  scoring_versions?: string[];
  packs: { digest: string; name: string; version: string }[];
  rows: LeaderboardRow[];
  by_scenario?: ScenarioRow[];
  comparable: boolean;
}

interface Catalog {
  agents?: { id: number; name: string; status?: string }[];
  models?: { gateway_model?: string; provider?: string; model_id?: string }[];
  environments?: { id: string; name: string }[];
  snapshots?: { id: string; description?: string }[];
}

// The platform proxy routes /api/apps/<name>/... by project. An absent
// project_id can only resolve a *global* install, and Bench is project-scoped,
// so a request without it 404s. An empty one is rejected outright — that is a
// panel fetching before its project context hydrated — so callers wait for a
// real project id instead of sending a blank.
function scopedPath(path: string, scope: Scope): string {
  const url = new URL(API + path, window.location.origin);
  if (scope.installId) url.searchParams.set("install_id", String(scope.installId));
  url.searchParams.set("project_id", scope.projectId);
  return url.pathname + url.search;
}

async function call<T>(path: string, scope: Scope, init?: RequestInit): Promise<T> {
  const response = await fetch(scopedPath(path, scope), {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ error: response.statusText }));
    throw new Error(body.error || response.statusText);
  }
  return response.json();
}

const pct = (v: number) => `${Math.round(v * 100)}%`;
const secs = (ms: number) => (ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${Math.round(ms)}ms`);
const short = (d?: string) => (d ? d.slice(0, 12) : "");
const modelLabel = (m: Catalog["models"] extends (infer T)[] | undefined ? T : never) =>
  m.gateway_model || `${m.provider || ""}/${m.model_id || ""}`.replace(/^\//, "");

const btn = "border border-border rounded px-2 py-1 text-sm hover:bg-bg-input disabled:opacity-40";
const field = "bg-bg-input border border-border rounded px-2 py-1 text-sm w-full";
const labelCls = "text-xs text-text-dim";

function emptyScenario(): Scenario {
  return {
    id: "", name: "", prompt: "", goals: [], environment_id: "", snapshot_id: "",
    checks: [{ name: "", app: "", tool: "", path: "", equals: "" }],
    budget: { duration_ms: 120000, cost_usd: 1, tokens_total: 80000, turns: 12 },
    timeout_seconds: 600, max_turns: 12, weight: 1,
  };
}

export default function BenchPanel({ projectId, installId }: NativePanelProps) {
  const [packs, setPacks] = useState<Pack[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [catalog, setCatalog] = useState<Catalog>({});
  const [selectedId, setSelectedId] = useState("");
  const [tab, setTab] = useState<"definition" | "runs" | "leaderboard">("definition");
  const [board, setBoard] = useState<LeaderboardRow[]>([]);
  const [boardScenarios, setBoardScenarios] = useState<ScenarioRow[]>([]);
  const [globalBoard, setGlobalBoard] = useState<GlobalBoard | null>(null);
  const [view, setView] = useState<"pack" | "global">("pack");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Scenario | null>(null);
  const [newPackOpen, setNewPackOpen] = useState(false);
  const [runFormOpen, setRunFormOpen] = useState(false);
  const [openRun, setOpenRun] = useState<Run | null>(null);

  const scope = useMemo<Scope>(() => ({ projectId, installId }), [projectId, installId]);
  const selected = useMemo(() => packs.find((p) => p.id === selectedId), [packs, selectedId]);
  const packRuns = useMemo(
    () => (selected ? runs.filter((r) => r.pack_digest === selected.digest || r.pack_id === selected.id) : runs),
    [runs, selected],
  );

  const refresh = useCallback(async () => {
    if (!projectId) return;
    try {
      const [p, r] = await Promise.all([
        call<Pack[]>("/api/packs", scope),
        call<Run[]>("/api/runs?limit=50", scope),
      ]);
      setPacks(p || []);
      setRuns(r || []);
      setSelectedId((cur) => cur || p?.[0]?.id || "");
    } catch (e) {
      setStatus(String(e));
    }
  }, [projectId, scope]);

  useEffect(() => {
    if (!projectId) return;
    void refresh();
    call<Catalog>("/api/catalog", scope).then(setCatalog).catch(() => setCatalog({}));
    // Runs advance on a 5s worker tick; match it rather than poll harder.
    const timer = setInterval(() => void refresh(), 5000);
    return () => clearInterval(timer);
  }, [refresh, projectId, scope]);

  useEffect(() => {
    if (tab !== "leaderboard" || !selected?.digest || !projectId || view !== "pack") {
      setBoard([]);
      setBoardScenarios([]);
      return;
    }
    call<{ rows: LeaderboardRow[]; by_scenario: ScenarioRow[] }>(`/api/packs/${selected.id}/leaderboard`, scope)
      .then((r) => { setBoard(r.rows || []); setBoardScenarios(r.by_scenario || []); })
      .catch((e) => setStatus(String(e)));
  }, [tab, selected, projectId, scope, view]);

  useEffect(() => {
    if (view !== "global" || !projectId) return;
    const load = () => call<GlobalBoard>("/api/leaderboard", scope).then(setGlobalBoard).catch((e) => setStatus(String(e)));
    void load();
    const timer = setInterval(load, 10000);
    return () => clearInterval(timer);
  }, [view, projectId, scope]);

  const act = async (label: string, fn: () => Promise<unknown>) => {
    setBusy(true);
    setStatus("");
    try {
      await fn();
      await refresh();
      if (label) setStatus(label);
    } catch (e) {
      setStatus(String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!projectId) {
    return (
      <div className="h-full flex items-center justify-center bg-bg text-text-dim text-sm">
        Waiting for project context…
      </div>
    );
  }

  const drafts = packs.filter((p) => p.state === "draft");
  const sealed = packs.filter((p) => p.state === "sealed");
  const isDraft = selected?.state === "draft";

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <div className="flex items-center gap-3 border-b border-border px-4 py-2">
        <span className="font-medium">Bench</span>
        {view === "global" && <span className="text-xs text-text-dim">every sealed benchmark</span>}
        {view === "pack" && selected && (
          <span className="text-xs text-text-dim">
            {selected.state === "sealed" ? `v${selected.version} · ${short(selected.digest)}` : "draft · seal to run"}
          </span>
        )}
        {status && <span className="ml-auto text-xs text-text-dim truncate max-w-[45%]">{status}</span>}
      </div>

      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "250px 1fr" }}>
        <div className="border-r border-border overflow-auto p-3 flex flex-col gap-3">
          <button className={`text-left border rounded px-2 py-1.5 ${view === "global" ? "border-text" : "border-border"}`}
            onClick={() => setView("global")}>
            <div className="font-medium text-sm">Global leaderboard</div>
            <div className="text-xs text-text-dim">Every sealed benchmark</div>
          </button>
          <button className={btn} onClick={() => { setView("pack"); setNewPackOpen(true); }}>+ New benchmark</button>
          <PackGroup label="Drafts" packs={drafts} selectedId={view === "pack" ? selectedId : ""} onSelect={(id) => { setView("pack"); setSelectedId(id); }} />
          <PackGroup label="Sealed" packs={sealed} selectedId={view === "pack" ? selectedId : ""} onSelect={(id) => { setView("pack"); setSelectedId(id); }} />
          {packs.length === 0 && (
            <div className="text-xs text-text-dim">
              No benchmarks yet. Create one, add scenarios, then seal it to make it runnable.
            </div>
          )}
        </div>

        <div className="overflow-auto p-4 flex flex-col gap-4">
          {view === "global" && (
            <GlobalLeaderboard board={globalBoard} />
          )}

          {view === "pack" && !selected && <div className="text-sm text-text-dim">Select or create a benchmark.</div>}

          {view === "pack" && selected && (
            <>
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="font-medium">{selected.name}</div>
                  <div className="text-xs text-text-dim">{selected.description || "No description."}</div>
                </div>
                <div className="flex gap-2">
                  {isDraft && (
                    <button className={btn} disabled={busy || selected.scenarios.length === 0}
                      onClick={() => act("", async () => {
                        const s = await call<Pack>(`/api/packs/${selected.id}/seal`, scope, { method: "POST", body: "{}" });
                        setSelectedId(s.id);
                        setStatus(`Sealed v${s.version} · ${short(s.digest)}`);
                      })}>Seal version</button>
                  )}
                  {!isDraft && (
                    <button className={btn} disabled={busy} onClick={() => { setRunFormOpen(true); setTab("runs"); }}>
                      Run benchmark
                    </button>
                  )}
                  <button className={btn} disabled={busy}
                    onClick={() => act("Forked to a new draft.", async () => {
                      const d = await call<Pack>(`/api/packs/${selected.id}/fork`, scope, { method: "POST", body: "{}" });
                      setSelectedId(d.id);
                    })}>Fork</button>
                  <button className={btn} disabled={busy}
                    onClick={() => {
                      if (!confirm(`Delete "${selected.name}"? This cannot be undone.`)) return;
                      void act("Deleted.", async () => {
                        await call(`/api/packs/${selected.id}`, scope, { method: "DELETE" });
                        setSelectedId("");
                      });
                    }}>Delete</button>
                </div>
              </div>

              <div className="flex gap-2 text-sm border-b border-border">
                {(["definition", "runs", "leaderboard"] as const).map((k) => (
                  <button key={k} onClick={() => setTab(k)}
                    className={`px-2 py-1 border-b-2 ${tab === k ? "border-text" : "border-transparent text-text-dim"}`}>
                    {k[0].toUpperCase() + k.slice(1)}
                  </button>
                ))}
              </div>

              {tab === "definition" && (
                <Definition pack={selected} isDraft={!!isDraft}
                  onAdd={() => setEditing(emptyScenario())}
                  onEdit={(s) => setEditing({ ...s })}
                  onDelete={(sid) => act("Scenario removed.", () =>
                    call(`/api/packs/${selected.id}/scenarios/${encodeURIComponent(sid)}`, scope, { method: "DELETE" }))}
                />
              )}

              {tab === "runs" && (
                <Runs runs={packRuns} busy={busy}
                  onNew={() => setRunFormOpen(true)}
                  onOpen={async (id) => {
                    try { setOpenRun(await call<Run>(`/api/runs/${id}`, scope)); }
                    catch (e) { setStatus(String(e)); }
                  }}
                  onCancel={(id) => act("Run cancelled.", () =>
                    call(`/api/runs/${id}/cancel`, scope, { method: "POST", body: "{}" }))}
                  runnable={!isDraft}
                />
              )}

              {tab === "leaderboard" && <Leaderboard pack={selected} rows={board} byScenario={boardScenarios} />}
            </>
          )}
        </div>
      </div>

      {newPackOpen && (
        <NewPackForm busy={busy} onClose={() => setNewPackOpen(false)}
          onSave={(name, description) => act("Benchmark created.", async () => {
            const p = await call<Pack>("/api/packs", scope, { method: "POST", body: JSON.stringify({ name, description }) });
            setSelectedId(p.id);
            setNewPackOpen(false);
          })} />
      )}

      {editing && selected && (
        <ScenarioForm scenario={editing} catalog={catalog} busy={busy}
          onClose={() => setEditing(null)}
          onSave={(s) => act("Scenario saved.", async () => {
            await call(`/api/packs/${selected.id}/scenarios`, scope, { method: "PUT", body: JSON.stringify(s) });
            setEditing(null);
          })} />
      )}

      {runFormOpen && selected && (
        <RunForm pack={selected} catalog={catalog} busy={busy}
          onClose={() => setRunFormOpen(false)}
          onSave={(targets, trials, name) => act("Run queued.", async () => {
            await call("/api/runs", scope, {
              method: "POST",
              body: JSON.stringify({ pack_id: selected.id, name, targets, trials }),
            });
            setRunFormOpen(false);
            setTab("runs");
          })} />
      )}

      {openRun && <RunDetail run={openRun} onClose={() => setOpenRun(null)} />}
    </div>
  );
}

function PackGroup({ label, packs, selectedId, onSelect }: {
  label: string; packs: Pack[]; selectedId: string; onSelect: (id: string) => void;
}) {
  if (packs.length === 0) return null;
  return (
    <div className="flex flex-col gap-1">
      <div className="text-xs text-text-dim uppercase tracking-wide">{label}</div>
      {packs.map((p) => (
        <button key={p.id} onClick={() => onSelect(p.id)}
          className={`text-left border rounded px-2 py-1.5 ${p.id === selectedId ? "border-text" : "border-border"}`}>
          <div className="font-medium text-sm">{p.name}</div>
          <div className="text-xs text-text-dim">
            {p.state === "sealed" ? `v${p.version} · ${short(p.digest)}` : "draft"} · {p.scenarios?.length || 0} scenario
            {(p.scenarios?.length || 0) === 1 ? "" : "s"}
          </div>
        </button>
      ))}
    </div>
  );
}

function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-6" onClick={onClose}>
      <div className="bg-bg border border-border rounded w-full max-w-2xl max-h-full overflow-auto"
        onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between border-b border-border px-4 py-2">
          <span className="font-medium text-sm">{title}</span>
          <button className="text-text-dim text-sm" onClick={onClose}>✕</button>
        </div>
        <div className="p-4 flex flex-col gap-3">{children}</div>
      </div>
    </div>
  );
}

function NewPackForm({ busy, onSave, onClose }: {
  busy: boolean; onSave: (n: string, d: string) => void; onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  return (
    <Modal title="New benchmark" onClose={onClose}>
      <label className={labelCls}>Name</label>
      <input className={field} value={name} onChange={(e) => setName(e.target.value)} placeholder="Apteva Core" />
      <label className={labelCls}>Description</label>
      <textarea className={field} rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
      <div className="flex justify-end gap-2">
        <button className={btn} onClick={onClose}>Cancel</button>
        <button className={btn} disabled={busy || !name.trim()} onClick={() => onSave(name, description)}>Create</button>
      </div>
    </Modal>
  );
}

function ScenarioForm({ scenario, catalog, busy, onSave, onClose }: {
  scenario: Scenario; catalog: Catalog; busy: boolean; onSave: (s: Scenario) => void; onClose: () => void;
}) {
  const [s, setS] = useState<Scenario>(scenario);
  const set = (patch: Partial<Scenario>) => setS((cur) => ({ ...cur, ...patch }));
  const setBudget = (patch: Partial<Budget>) => setS((cur) => ({ ...cur, budget: { ...cur.budget, ...patch } }));
  const setCheck = (i: number, patch: Partial<Check>) =>
    setS((cur) => ({ ...cur, checks: cur.checks.map((c, j) => (j === i ? { ...c, ...patch } : c)) }));

  // `equals` is typed as unknown server-side: try JSON so 91 and true keep
  // their types, and fall back to the raw string for ordinary values.
  const parseEquals = (raw: string): unknown => {
    const t = raw.trim();
    if (t === "") return "";
    try { return JSON.parse(t); } catch { return raw; }
  };
  const showEquals = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v ?? ""));

  return (
    <Modal title={scenario.id ? `Edit ${scenario.name}` : "Add scenario"} onClose={onClose}>
      <label className={labelCls}>Name</label>
      <input className={field} value={s.name} onChange={(e) => set({ name: e.target.value })}
        placeholder="Create then update a contact" />

      <label className={labelCls}>Prompt — what the agent is asked to do</label>
      <textarea className={field} rows={4} value={s.prompt} onChange={(e) => set({ prompt: e.target.value })} />

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className={labelCls}>Environment</label>
          <select className={field} value={s.environment_id || ""} onChange={(e) => set({ environment_id: e.target.value })}>
            <option value="">— none —</option>
            {(catalog.environments || []).map((e) => <option key={e.id} value={e.id}>{e.name || e.id}</option>)}
          </select>
        </div>
        <div>
          <label className={labelCls}>Or pinned snapshot</label>
          <select className={field} value={s.snapshot_id || ""} onChange={(e) => set({ snapshot_id: e.target.value })}>
            <option value="">— none —</option>
            {(catalog.snapshots || []).map((x) => <option key={x.id} value={x.id}>{x.description || x.id}</option>)}
          </select>
        </div>
      </div>
      <div className="text-xs text-text-dim">
        A scenario needs one of these — sealing refuses a world it cannot reproduce.
      </div>

      <div className="flex items-center justify-between pt-2">
        <span className={labelCls}>Final-state checks — what must be true when the agent stops</span>
        <button className={btn} onClick={() => set({ checks: [...s.checks, { name: "", app: "", tool: "", path: "", equals: "" }] })}>
          + Check
        </button>
      </div>
      {s.checks.map((c, i) => (
        <div key={i} className="border border-border rounded p-2 grid grid-cols-6 gap-2 items-end">
          <div className="col-span-2">
            <label className={labelCls}>Name</label>
            <input className={field} value={c.name} onChange={(e) => setCheck(i, { name: e.target.value })} />
          </div>
          <div>
            <label className={labelCls}>App</label>
            <input className={field} value={c.app || ""} onChange={(e) => setCheck(i, { app: e.target.value })} placeholder="crm" />
          </div>
          <div className="col-span-2">
            <label className={labelCls}>Tool</label>
            <input className={field} value={c.tool || ""} onChange={(e) => setCheck(i, { tool: e.target.value })} placeholder="crm_contact_get" />
          </div>
          <button className={btn} onClick={() => set({ checks: s.checks.filter((_, j) => j !== i) })}>Remove</button>
          <div className="col-span-3">
            <label className={labelCls}>Tool input (JSON)</label>
            <input className={field} defaultValue={JSON.stringify(c.input || {})}
              onChange={(e) => { try { setCheck(i, { input: JSON.parse(e.target.value || "{}") }); } catch { /* keep typing */ } }} />
          </div>
          <div className="col-span-2">
            <label className={labelCls}>Path</label>
            <input className={field} value={c.path || ""} onChange={(e) => setCheck(i, { path: e.target.value })} placeholder="lifecycle" />
          </div>
          <div>
            <label className={labelCls}>Equals</label>
            <input className={field} value={showEquals(c.equals)} onChange={(e) => setCheck(i, { equals: parseEquals(e.target.value) })} />
          </div>
        </div>
      ))}

      <div className={labelCls + " pt-2"}>Budgets — full efficiency points at or under, zero at twice</div>
      <div className="grid grid-cols-4 gap-3">
        <div>
          <label className={labelCls}>Duration (s)</label>
          <input className={field} type="number" value={Math.round(s.budget.duration_ms / 1000)}
            onChange={(e) => setBudget({ duration_ms: Number(e.target.value) * 1000 })} />
        </div>
        <div>
          <label className={labelCls}>Turns</label>
          <input className={field} type="number" value={s.budget.turns}
            onChange={(e) => setBudget({ turns: Number(e.target.value) })} />
        </div>
        <div>
          <label className={labelCls}>Tokens</label>
          <input className={field} type="number" value={s.budget.tokens_total}
            onChange={(e) => setBudget({ tokens_total: Number(e.target.value) })} />
        </div>
        <div>
          <label className={labelCls}>Cost (USD)</label>
          <input className={field} type="number" step="0.01" value={s.budget.cost_usd}
            onChange={(e) => setBudget({ cost_usd: Number(e.target.value) })} />
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className={labelCls}>Timeout (s)</label>
          <input className={field} type="number" value={s.timeout_seconds || 600}
            onChange={(e) => set({ timeout_seconds: Number(e.target.value) })} />
        </div>
        <div>
          <label className={labelCls}>Max turns</label>
          <input className={field} type="number" value={s.max_turns || 12}
            onChange={(e) => set({ max_turns: Number(e.target.value) })} />
        </div>
      </div>

      <div className="flex justify-end gap-2 pt-2">
        <button className={btn} onClick={onClose}>Cancel</button>
        <button className={btn} disabled={busy || !s.name.trim() || !s.prompt.trim()}
          onClick={() => onSave({ ...s, checks: s.checks.filter((c) => c.name.trim() || c.tool?.trim()) })}>
          Save scenario
        </button>
      </div>
    </Modal>
  );
}

function RunForm({ pack, catalog, busy, onSave, onClose }: {
  pack: Pack; catalog: Catalog; busy: boolean;
  onSave: (targets: Target[], trials: number, name: string) => void; onClose: () => void;
}) {
  const [targets, setTargets] = useState<Target[]>([{ agent_id: 0, provider: "", model: "" }]);
  const [trials, setTrials] = useState(3);
  const [name, setName] = useState("");
  const setTarget = (i: number, patch: Partial<Target>) =>
    setTargets((cur) => cur.map((t, j) => (j === i ? { ...t, ...patch } : t)));
  const valid = targets.some((t) => t.agent_id > 0);

  return (
    <Modal title={`Run ${pack.name} v${pack.version}`} onClose={onClose}>
      <label className={labelCls}>Run name (optional)</label>
      <input className={field} value={name} onChange={(e) => setName(e.target.value)} placeholder="gpt-5.5 vs kimi-k3" />

      <div className="flex items-center justify-between pt-2">
        <span className={labelCls}>Targets — each is one agent, optionally pinned to a provider and model</span>
        <button className={btn} onClick={() => setTargets([...targets, { agent_id: 0, provider: "", model: "" }])}>
          + Target
        </button>
      </div>
      {targets.map((t, i) => (
        <div key={i} className="border border-border rounded p-2 grid grid-cols-5 gap-2 items-end">
          <div className="col-span-2">
            <label className={labelCls}>Agent</label>
            <select className={field} value={t.agent_id || ""}
              onChange={(e) => {
                const id = Number(e.target.value);
                const a = (catalog.agents || []).find((x) => x.id === id);
                setTarget(i, { agent_id: id, agent_name: a?.name });
              }}>
              <option value="">— select —</option>
              {(catalog.agents || []).map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}
            </select>
          </div>
          <div className="col-span-2">
            <label className={labelCls}>Model (optional)</label>
            <select className={field} value={t.model || ""}
              onChange={(e) => {
                const m = (catalog.models || []).find((x) => modelLabel(x) === e.target.value);
                setTarget(i, { model: e.target.value, provider: m?.provider || t.provider });
              }}>
              <option value="">— agent default —</option>
              {(catalog.models || []).map((m) => {
                const l = modelLabel(m);
                return <option key={l} value={l}>{l}</option>;
              })}
            </select>
          </div>
          <button className={btn} onClick={() => setTargets(targets.filter((_, j) => j !== i))}>Remove</button>
        </div>
      ))}

      <div className="grid grid-cols-2 gap-3 pt-2">
        <div>
          <label className={labelCls}>Trials per target</label>
          <input className={field} type="number" min={1} value={trials}
            onChange={(e) => setTrials(Math.max(1, Number(e.target.value)))} />
        </div>
        <div className="text-xs text-text-dim self-end">
          {pack.scenarios.length} scenario{pack.scenarios.length === 1 ? "" : "s"} × {targets.length} target
          {targets.length === 1 ? "" : "s"} × {trials} = {pack.scenarios.length * targets.length * trials} runs
        </div>
      </div>

      <div className="flex justify-end gap-2 pt-2">
        <button className={btn} onClick={onClose}>Cancel</button>
        <button className={btn} disabled={busy || !valid}
          onClick={() => onSave(targets.filter((t) => t.agent_id > 0), trials, name)}>Queue run</button>
      </div>
    </Modal>
  );
}

function Definition({ pack, isDraft, onAdd, onEdit, onDelete }: {
  pack: Pack; isDraft: boolean; onAdd: () => void;
  onEdit: (s: Scenario) => void; onDelete: (id: string) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      {pack.state === "sealed" && (
        <div className="text-xs text-text-dim border border-border rounded p-2">
          Sealed under <code>{pack.scoring_version}</code>. Immutable — fork it to change anything.
        </div>
      )}
      {isDraft && (
        <div className="flex justify-end"><button className={btn} onClick={onAdd}>+ Add scenario</button></div>
      )}
      {(pack.scenarios || []).map((s) => (
        <div key={s.id} className="border border-border rounded p-3 flex flex-col gap-2">
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="font-medium text-sm">{s.name}</div>
              <div className="text-xs text-text-dim">{s.id}</div>
            </div>
            <div className="flex gap-2 items-center">
              <span className="text-xs text-text-dim">{s.checks?.length || 0} check{(s.checks?.length || 0) === 1 ? "" : "s"}</span>
              {isDraft && <button className={btn} onClick={() => onEdit(s)}>Edit</button>}
              {isDraft && <button className={btn} onClick={() => onDelete(s.id)}>Remove</button>}
            </div>
          </div>
          <div className="text-sm">{s.prompt}</div>
          <div className="text-xs text-text-dim flex flex-wrap gap-3">
            <span>≤ {secs(s.budget.duration_ms)}</span>
            <span>≤ {s.budget.turns} turns</span>
            <span>≤ {s.budget.tokens_total.toLocaleString()} tokens</span>
            {s.budget.cost_usd > 0 && <span>≤ ${s.budget.cost_usd}</span>}
            <span>{s.snapshot_id || s.environment_id || "no pinned world"}</span>
          </div>
        </div>
      ))}
      {(pack.scenarios || []).length === 0 && (
        <div className="text-sm text-text-dim">
          No scenarios yet. {isDraft ? "Add one to describe a task, the world it runs in, and what must be true afterwards." : ""}
        </div>
      )}
    </div>
  );
}

function Runs({ runs, busy, runnable, onNew, onOpen, onCancel }: {
  runs: Run[]; busy: boolean; runnable: boolean;
  onNew: () => void; onOpen: (id: string) => void; onCancel: (id: string) => void;
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex justify-end">
        <button className={btn} disabled={busy || !runnable} onClick={onNew}
          title={runnable ? "" : "Seal the benchmark before running it"}>+ New run</button>
      </div>
      {runs.length === 0 && <div className="text-sm text-text-dim">No runs yet.</div>}
      {runs.map((r) => (
        <div key={r.id} className="border border-border rounded p-3 flex flex-col gap-2">
          <div className="flex items-start justify-between gap-4">
            <div>
              <div className="font-medium text-sm">{r.name || r.id}</div>
              <div className="text-xs text-text-dim">
                {r.status} · {r.trials} trial{r.trials === 1 ? "" : "s"} · {new Date(r.created_at).toLocaleString()}
              </div>
            </div>
            <div className="flex items-center gap-3">
              {r.summary?.verified > 0 && (
                <div className="text-right">
                  <div className="font-medium text-sm">{pct(r.summary.pass_rate)}</div>
                  <div className="text-xs text-text-dim">{r.summary.average_score}/100</div>
                </div>
              )}
              <button className={btn} onClick={() => onOpen(r.id)}>Results</button>
              {(r.status === "queued" || r.status === "running") && (
                <button className={btn} disabled={busy} onClick={() => onCancel(r.id)}>Cancel</button>
              )}
            </div>
          </div>
          {r.error && <div className="text-xs text-text-dim">{r.error}</div>}
          {r.summary?.invalid > 0 && (
            <div className="text-xs text-text-dim">
              {r.summary.invalid} withheld as harness failures — excluded from the pass rate.
            </div>
          )}
          {(r.summary?.targets || []).map((t) => (
            <div key={t.target_index} className="text-xs flex justify-between gap-4 border-t border-border pt-1">
              <span>{t.label}</span>
              <span className="text-text-dim">
                {pct(t.pass_rate)} · {t.average_score}/100 · {secs(t.average_duration_ms)} ·{" "}
                {Math.round(t.average_tokens).toLocaleString()} tok{t.invalid > 0 ? ` · ${t.invalid} withheld` : ""}
              </span>
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}

function RunDetail({ run, onClose }: { run: Run; onClose: () => void }) {
  return (
    <Modal title={run.name || run.id} onClose={onClose}>
      <div className="text-xs text-text-dim">
        {run.pack_name} v{run.pack_version} · {short(run.pack_digest)} · {run.status}
      </div>
      <table className="text-sm w-full">
        <thead className="text-xs text-text-dim">
          <tr className="text-left border-b border-border">
            <th className="py-1">Scenario</th><th>Target</th><th>Trial</th><th>Outcome</th><th>Score</th><th>Duration</th><th>Tokens</th>
          </tr>
        </thead>
        <tbody>
          {(run.results || []).map((r) => (
            <tr key={r.id} className="border-b border-border">
              <td className="py-1">{r.scenario_name}</td>
              <td>{r.target?.model || r.target?.agent_name || "—"}</td>
              <td>{r.trial}</td>
              <td>
                {r.admission === "invalid"
                  ? <span className="text-text-dim" title={r.invalid_reason}>withheld</span>
                  : r.passed ? "pass" : "fail"}
              </td>
              <td>{r.admission === "invalid" ? "—" : r.score?.score}</td>
              <td>{r.admission === "invalid" ? "—" : secs(r.metrics?.duration_ms || 0)}</td>
              <td>{r.admission === "invalid" ? "—" : (r.metrics?.tokens_total || 0).toLocaleString()}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {(run.results || []).length === 0 && <div className="text-sm text-text-dim">No results recorded.</div>}
    </Modal>
  );
}


// ── Chart layer ────────────────────────────────────────────────────────────
// Hand-rolled SVG rather than a chart library: these are a handful of bars, and
// recharts would add ~430KB to a 21KB panel. Palette is the validated
// categorical set (both modes checked for CVD separation and contrast).
const VIZ_CSS = `
.bench-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 28px 40px;
  align-items: start;
}
@media (min-width: 880px) {
  .bench-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
.bench-scroll { overflow-x: auto; }
.bench-fig { width: 100%; max-width: 620px; }
/* Never style SVG fill with a utility class: Tailwind only emits classes it
   finds in dashboard source, and a runtime-loaded panel is not scanned. */
.bench-viz text { fill: currentColor; }
.bench-viz .viz-dim { opacity: 0.68; }
.bench-viz {
  --viz-grid: color-mix(in srgb, currentColor 14%, transparent);
  --viz-surface: var(--bg, #fcfcfb);
  --s1: #2a78d6; --s2: #eb6834; --s3: #1baf7a; --s4: #eda100; --s5: #e87ba4;
}
@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) .bench-viz {
    --viz-surface: var(--bg, #1a1a19);
    --s1: #3987e5; --s2: #d95926; --s3: #199e70; --s4: #c98500; --s5: #d55181;
  }
}
:root[data-theme="dark"] .bench-viz {
  --viz-surface: var(--bg, #1a1a19);
  --s1: #3987e5; --s2: #d95926; --s3: #199e70; --s4: #c98500; --s5: #d55181;
}
`;

// Square at the baseline, 4px rounded at the data end — never a pill.
function barPath(x: number, y: number, w: number, h: number, r = 4): string {
  const rr = Math.max(0, Math.min(r, w));
  if (rr <= 0) return `M${x},${y} H${x + w} V${y + h} H${x} Z`;
  return `M${x},${y} H${x + w - rr} A${rr},${rr} 0 0 1 ${x + w},${y + rr} V${y + h - rr} A${rr},${rr} 0 0 1 ${x + w - rr},${y + h} H${x} Z`;
}

// ~6px per character at 11px; a label that would overflow the gutter is
// truncated with the full text kept in the tooltip and the table, never clipped.
const MAX_LABEL_CHARS = 20;
function fitLabel(label: string): string {
  return label.length <= MAX_LABEL_CHARS ? label : label.slice(0, MAX_LABEL_CHARS - 1) + "\u2026";
}

const ROW_H = 38;
const BAR_H = 22;        // <= 24px cap
const LABEL_W = 150;
const VALUE_W = 86;      // gutter reserved for the direct label at the bar tip
const VIEW_W = 560;
const FONT = 12;

// One measure across a few named targets: magnitude, so a single hue with the
// value direct-labelled at the tip. No legend — the title names the series.
function RankBars({ title, rows, max, format, unit }: {
  title: string;
  rows: { label: string; value: number; color: string }[];
  max?: number;
  format: (v: number) => string;
  unit?: string;
}) {
  if (rows.length === 0) return null;
  const top = max ?? Math.max(...rows.map((r) => r.value), 1);
  const plot = VIEW_W - LABEL_W - VALUE_W;
  const height = rows.length * ROW_H + 16;
  const ticks = [0, 0.5, 1].map((t) => t * top);
  return (
    <figure className="bench-viz bench-fig m-0 flex flex-col gap-2">
      <figcaption className="text-xs text-text-dim">{title}{unit ? ` (${unit})` : ""}</figcaption>
      <svg viewBox={`0 0 ${VIEW_W} ${height}`} width="100%" role="img" aria-label={title}>
        {ticks.map((t, i) => {
          const x = LABEL_W + (plot * t) / top;
          return <line key={i} x1={x} y1={6} x2={x} y2={height - 6}
            stroke="var(--viz-grid)" strokeWidth="1" shapeRendering="crispEdges" />;
        })}
        {rows.map((r, i) => {
          const y = 8 + i * ROW_H;
          const w = top > 0 ? (plot * Math.max(0, r.value)) / top : 0;
          return (
            <g key={r.label}>
              <title>{`${r.label}: ${format(r.value)}`}</title>
              <RowKey rank={i + 1} color={r.color} label={r.label} y={y} />
              <path d={barPath(LABEL_W, y, Math.max(w, 1), BAR_H)} fill={r.color} />
              <text x={LABEL_W + w + 8} y={y + BAR_H / 2 + 4}
                style={{ fontSize: FONT }}>{format(r.value)}</text>
            </g>
          );
        })}
      </svg>
    </figure>
  );
}

const MODEL_HUES = ["var(--s1)", "var(--s2)", "var(--s3)", "var(--s4)", "var(--s5)",
  "var(--s6)", "var(--s7)", "var(--s8)"];

// Colour follows the entity, never its rank: keyed off a stable alphabetical
// order so re-sorting the board leaves every model its own hue.
function hueMap(labels: string[]): Record<string, string> {
  const ordered = [...new Set(labels)].sort();
  const out: Record<string, string> = {};
  ordered.forEach((l, i) => { out[l] = MODEL_HUES[i % MODEL_HUES.length]; });
  return out;
}

const RANK_W = 18;
const SWATCH_W = 16;

// Rank number, colour key, then the name — laid out left to right so the
// ordering reads without relying on bar length or hue.
function RowKey({ rank, color, label, y, swatch = true }: {
  rank: number; color: string; label: string; y: number; swatch?: boolean;
}) {
  return (
    <g>
      <text x={0} y={y + BAR_H / 2 + 4} className="viz-dim" style={{ fontSize: FONT }}>{rank}</text>
      {swatch && <rect x={RANK_W} y={y + BAR_H / 2 - 5} width="10" height="10" rx="2" fill={color} />}
      <text x={RANK_W + (swatch ? SWATCH_W : 0)} y={y + BAR_H / 2 + 4} style={{ fontSize: FONT }}>{fitLabel(label)}</text>
    </g>
  );
}

const COMPONENT_SERIES = [
  { key: "success", label: "Task success", color: "var(--s1)", max: 70 },
  { key: "duration", label: "Duration", color: "var(--s2)", max: 10 },
  { key: "cost", label: "Cost / tokens", color: "var(--s3)", max: 10 },
  { key: "turns", label: "Turns", color: "var(--s4)", max: 5 },
  { key: "tool_errors", label: "No tool errors", color: "var(--s5)", max: 5 },
] as const;

// Part-to-whole across five fixed scoring components: stacked bars, categorical,
// legend always present. Segments are separated by a 2px surface gap, and the
// numbers live in the table below rather than crowding the interior segments.
function CompositionBars({ rows }: { rows: { label: string; components: Record<string, number>; color: string }[] }) {
  if (rows.length === 0) return null;
  const plot = VIEW_W - LABEL_W - VALUE_W;
  const height = rows.length * ROW_H + 16;
  const GAP = 2;
  return (
    <figure className="bench-viz bench-fig m-0 flex flex-col gap-2">
      <figcaption className="text-xs text-text-dim">Where the 100 points went</figcaption>
      <svg viewBox={`0 0 ${VIEW_W} ${height}`} width="100%" role="img" aria-label="Score composition by target">
        {[0, 50, 100].map((t) => {
          const x = LABEL_W + (plot * t) / 100;
          return <line key={t} x1={x} y1={6} x2={x} y2={height - 6}
            stroke="var(--viz-grid)" strokeWidth="1" shapeRendering="crispEdges" />;
        })}
        {rows.map((r, i) => {
          const y = 8 + i * ROW_H;
          let cursor = LABEL_W;
          const total = COMPONENT_SERIES.reduce((sum, c) => sum + (r.components[c.key] || 0), 0);
          const segs = COMPONENT_SERIES.map((c, ci) => {
            const v = r.components[c.key] || 0;
            const raw = (plot * v) / 100;
            const last = ci === COMPONENT_SERIES.length - 1;
            const w = Math.max(0, raw - (last ? 0 : GAP));
            const seg = { x: cursor, w, c, v, last };
            cursor += raw;
            return seg;
          });
          return (
            <g key={r.label}>
              <title>{`${r.label}: ${round1(total)} of 100`}</title>
              <RowKey rank={i + 1} color={r.color} label={r.label} y={y} swatch={false} />
              {segs.map((s) => s.w <= 0 ? null : (
                <g key={s.c.key}>
                  <title>{`${r.label} — ${s.c.label}: ${s.v} of ${s.c.max}`}</title>
                  <path d={s.last ? barPath(s.x, y, s.w, BAR_H) : `M${s.x},${y} h${s.w} v${BAR_H} h${-s.w} Z`}
                    fill={s.c.color} />
                </g>
              ))}
              <text x={LABEL_W + plot + 8} y={y + BAR_H / 2 + 4}
                style={{ fontSize: FONT }}>{round1(total)}</text>
            </g>
          );
        })}
      </svg>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-text-dim">
        {COMPONENT_SERIES.map((c) => (
          <span key={c.key} className="flex items-center gap-1.5">
            <svg width="10" height="10" aria-hidden="true"><rect width="10" height="10" rx="2" fill={c.color} /></svg>
            {c.label} <span className="opacity-60">/{c.max}</span>
          </span>
        ))}
      </div>
    </figure>
  );
}

const round1 = (v: number) => Math.round(v * 10) / 10;

// Three light-mode palette slots sit under 3:1 contrast, so the component
// values must also be readable as text — the relief rule, not decoration.
function ComponentTable({ rows }: { rows: { label: string; components: Record<string, number>; color: string }[] }) {
  return (
    <div className="bench-viz flex flex-col gap-2">
      <div className="text-xs text-text-dim">Score components</div>
      <div className="bench-scroll">
      <table className="text-xs w-full">
        <thead className="text-text-dim">
          <tr className="text-left border-b border-border">
            <th className="py-1">Target</th>
            {COMPONENT_SERIES.map((c) => <th key={c.key}>{c.label}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.label} className="border-b border-border">
              <td className="py-1">{r.label}</td>
              {COMPONENT_SERIES.map((c) => (
                <td key={c.key} className="text-text-dim">
                  {r.components[c.key] ?? 0}<span className="opacity-60">/{c.max}</span>
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      </div>
    </div>
  );
}

// The table is not optional: three light-mode palette slots sit under 3:1
// contrast, and the relief rule requires the values be readable as text too.
function BoardTable({ rows, hues, labelOf }: {
  rows: LeaderboardRow[]; hues: Record<string, string>; labelOf: (r: LeaderboardRow, i: number) => string;
}) {
  return (
    <table className="text-sm w-full bench-viz">
      <thead className="text-xs text-text-dim">
        <tr className="text-left border-b border-border">
          <th className="py-1">#</th><th>Target</th><th>Pass</th><th>Score</th><th>Duration</th>
          <th>Tokens</th><th>Runs</th><th>Coverage</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row, i) => (
          <tr key={row.label} className="border-b border-border">
            <td className="py-1 text-text-dim">{i + 1}</td>
            <td className="py-1">
              <span className="flex items-center gap-2">
                <svg width="10" height="10" aria-hidden="true">
                  <rect width="10" height="10" rx="2" fill={hues[labelOf(row, i)]} />
                </svg>
                {row.label}
              </span>
              {row.mixed_cost_basis && (
                <span className="text-xs text-text-dim" title="Some runs priced in cost, others in tokens"> · mixed basis</span>
              )}
            </td>
            <td>{pct(row.pass_rate)}</td>
            <td>{row.average_score}</td>
            <td>{secs(row.average_duration_ms)}</td>
            <td>{Math.round(row.average_tokens).toLocaleString()}</td>
            <td className="text-text-dim">{row.runs}</td>
            <td className="text-text-dim">{row.packs ?? 1} pack{(row.packs ?? 1) === 1 ? "" : "s"} · {row.scenarios} scen.</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Board({ rows, byScenario, note }: {
  rows: LeaderboardRow[]; byScenario?: ScenarioRow[]; note?: string;
}) {
  if (rows.length === 0) return <div className="text-sm text-text-dim">No admitted results yet.</div>;
  // Chart gutters are narrow, so drop the shared provider prefix when the model
  // alone still identifies each row. The table keeps the full label.
  const models = rows.map((r) => r.model || r.label);
  const distinct = new Set(models).size === models.length;
  const chartLabel = (r: LeaderboardRow, i: number) => (distinct ? models[i] : r.label);
  const hues = hueMap(rows.map((r, i) => chartLabel(r, i)));
  const colorOf = (r: LeaderboardRow, i: number) => hues[chartLabel(r, i)];
  const componentRows = rows.map((r, i) => ({
    label: chartLabel(r, i), components: (r.components || {}) as Record<string, number>,
    color: colorOf(r, i),
  }));
  return (
    <div className="flex flex-col gap-8">
      <style dangerouslySetInnerHTML={{ __html: VIZ_CSS }} />
      {note && <div className="text-xs text-text-dim border border-border rounded p-2">{note}</div>}
      <div className="bench-grid">
        <CompositionBars rows={componentRows} />
        <ComponentTable rows={componentRows} />
      </div>
      <div className="bench-grid">
        <RankBars title="Pass rate" rows={rows.map((r, i) => ({ label: chartLabel(r, i), value: r.pass_rate * 100, color: colorOf(r, i) }))}
          max={100} format={(v) => `${Math.round(v)}%`} unit="%" />
        <RankBars title="Average score" rows={rows.map((r, i) => ({ label: chartLabel(r, i), value: r.average_score, color: colorOf(r, i) }))}
          max={100} format={(v) => String(round1(v))} unit="of 100" />
        <RankBars title="Average duration" rows={rows.map((r, i) => ({ label: chartLabel(r, i), value: r.average_duration_ms, color: colorOf(r, i) }))}
          format={(v) => secs(v)} />
        <RankBars title="Average tokens" rows={rows.map((r, i) => ({ label: chartLabel(r, i), value: r.average_tokens, color: colorOf(r, i) }))}
          format={(v) => Math.round(v).toLocaleString()} />
      </div>
      <BoardTable rows={rows} hues={hues} labelOf={chartLabel} />
      {byScenario && byScenario.length > 0 && (
        <div className="flex flex-col gap-1">
          <div className="text-xs text-text-dim">By scenario — where an aggregate hides a weak spot</div>
          <table className="text-sm w-full">
            <thead className="text-xs text-text-dim">
              <tr className="text-left border-b border-border">
                <th className="py-1">Scenario</th><th>Target</th><th>Pass</th><th>Score</th><th>Runs</th>
              </tr>
            </thead>
            <tbody>
              {byScenario.map((r, i) => (
                <tr key={i} className="border-b border-border">
                  <td className="py-1">{r.scenario_name || r.scenario_id}</td>
                  <td>{r.label}</td><td>{pct(r.pass_rate)}</td><td>{r.average_score}</td>
                  <td className="text-text-dim">{r.runs}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function Leaderboard({ pack, rows, byScenario }: {
  pack: Pack; rows: LeaderboardRow[]; byScenario: ScenarioRow[];
}) {
  if (pack.state !== "sealed") {
    return <div className="text-sm text-text-dim">Only sealed benchmarks have a leaderboard. Seal a version first.</div>;
  }
  return (
    <Board rows={rows} byScenario={byScenario}
      note={`Every admitted result for digest ${short(pack.digest)} under ${pack.scoring_version}. Ranked by pass rate; score breaks ties.`} />
  );
}

function GlobalLeaderboard({ board }: { board: GlobalBoard | null }) {
  if (!board) return <div className="text-sm text-text-dim">Loading…</div>;
  if (!board.rows || board.rows.length === 0) {
    return <div className="text-sm text-text-dim">No admitted results yet. Seal a benchmark and run it.</div>;
  }
  const packs = board.packs || [];
  // A ranking across targets that faced different benchmarks is not a fair
  // comparison; say so rather than letting the ordering imply otherwise.
  const note = board.comparable
    ? `Every target has faced all ${packs.length} sealed benchmark${packs.length === 1 ? "" : "s"} under ${board.scoring_version}. Ranked by pass rate; score breaks ties.`
    : `Targets here have not all faced the same benchmarks, so this ranking is not a like-for-like comparison — check the coverage column. Scoring contract ${board.scoring_version}.`;
  return (
    <div className="flex flex-col gap-4">
      <div>
        <div className="font-medium">Global leaderboard</div>
        <div className="text-xs text-text-dim">
          {packs.length} sealed benchmark{packs.length === 1 ? "" : "s"}: {packs.map((p) => `${p.name} v${p.version}`).join(", ")}
        </div>
      </div>
      {(board.scoring_versions?.length || 0) > 1 && (
        <div className="text-xs text-text-dim border border-border rounded p-2">
          Results exist under {board.scoring_versions!.length} scoring contracts; only {board.scoring_version} is shown.
          Scores from different contracts are not comparable.
        </div>
      )}
      <Board rows={board.rows} byScenario={board.by_scenario} note={note} />
    </div>
  );
}
