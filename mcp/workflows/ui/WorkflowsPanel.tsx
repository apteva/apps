// WorkflowsPanel — native React panel for the workflows app.
// Two-pane: list of workflows on the left, detail + recent runs on
// the right. Loaded by the dashboard via dynamic import; uses host
// React via importmap; talks to the workflows sidecar through
// /api/apps/workflows/* with same-origin cookies.
//
// Authoring is intentionally not in v0.1 of this panel — defining
// a workflow is a YAML editing task that the agent handles via
// `workflows_create`. The dashboard surface is for inspection,
// manual run, and replay.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { parse as parseYAML, stringify as stringifyYAML } from "yaml";

import { WorkflowGraph, StepStatus } from "./WorkflowGraph";
import { StepEditor } from "./StepEditor";
import { StepDef, WorkflowDef } from "./graph";

// ─── Inlined SSE subscriber (same shape as JobsPanel) ──────────────

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// ─── Domain types (mirror Go shapes) ───────────────────────────────

interface Workflow {
  id: number;
  name: string;
  version: number;
  source_kind: string;
  source: string;
  source_hash: string;
  trigger_kind: string;
  status: string;
  created_at?: string;
  updated_at?: string;
}

interface StepExecution {
  id: number;
  step_id: string;
  step_kind: string;
  attempt: number;
  started_at: string;
  finished_at?: string;
  duration_ms: number;
  status: string;
  input_json?: string;
  output_json?: string;
  error?: string;
}

interface Run {
  id: number;
  workflow_id: number;
  workflow_name: string;
  workflow_version: number;
  trigger_kind: string;
  input_json?: string;
  status: string;
  current_step_id?: string;
  error?: string;
  started_at: string;
  finished_at?: string;
  duration_ms: number;
  steps?: StepExecution[];
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

const API = "/api/apps/workflows";

// ─── Status / formatting helpers ───────────────────────────────────

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const diff = (d.getTime() - Date.now()) / 1000;
  const abs = Math.abs(diff);
  const prefix = diff >= 0 ? "in " : "";
  const suffix = diff < 0 ? " ago" : "";
  if (abs < 60) return `${prefix}${Math.round(abs)}s${suffix}`;
  if (abs < 3600) return `${prefix}${Math.round(abs / 60)}m${suffix}`;
  if (abs < 86400) return `${prefix}${Math.round(abs / 3600)}h${suffix}`;
  return d.toLocaleString();
}

function fmtDuration(ms: number): string {
  if (!ms || ms < 0) return "—";
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${(ms / 60_000).toFixed(1)} m`;
}

function statusClass(s: string): string {
  switch (s) {
    case "running":
      return "bg-accent/15 text-accent";
    case "completed":
    case "ok":
      return "bg-green/15 text-green";
    case "failed":
    case "error":
    case "timeout":
      return "bg-red/15 text-red";
    case "cancelled":
    case "skipped":
      return "bg-border text-text-muted";
    case "pending":
      return "bg-blue/15 text-blue";
    case "disabled":
      return "bg-border text-text-dim";
    default:
      return "bg-border text-text-muted";
  }
}

// ─── Panel root ────────────────────────────────────────────────────

export default function WorkflowsPanel({ projectId, installId }: NativePanelProps) {
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [filter, setFilter] = useState<string>("");
  const [selectedID, setSelectedID] = useState<number | null>(null);
  const [selectedWf, setSelectedWf] = useState<Workflow | null>(null);
  const [runs, setRuns] = useState<Run[]>([]);
  const [activeRun, setActiveRun] = useState<Run | null>(null);
  const [err, setErr] = useState<string>("");
  const [showRunModal, setShowRunModal] = useState(false);

  const qs = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
    [projectId, installId],
  );

  const apiCall = useCallback(
    async <T,>(method: string, path: string, body?: unknown, extra: Record<string, string> = {}): Promise<T> => {
      const res = await fetch(`${API}${path}?${qs(extra)}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : {},
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      return res.json() as Promise<T>;
    },
    [qs],
  );

  // ─── Data loaders ────────────────────────────────────────────────

  const loadWorkflows = useCallback(async () => {
    try {
      const extra: Record<string, string> = {};
      if (filter) extra.status = filter;
      const r = await apiCall<{ workflows: Workflow[] }>("GET", "/workflows", undefined, extra);
      setWorkflows(r.workflows || []);
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    }
  }, [apiCall, filter]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const wf = await apiCall<{ workflow: Workflow }>("GET", `/workflows/${id}`);
        const rs = await apiCall<{ runs: Run[] }>("GET", `/workflows/${id}/runs`);
        setSelectedWf(wf.workflow);
        setRuns(rs.runs || []);
      } catch (e) {
        setSelectedWf(null);
        setErr((e as Error).message);
      }
    },
    [apiCall],
  );

  const loadRunSteps = useCallback(
    async (runID: number) => {
      try {
        const r = await apiCall<{ run: Run }>("GET", `/runs/${runID}`);
        setActiveRun(r.run);
      } catch (e) {
        setErr((e as Error).message);
      }
    },
    [apiCall],
  );

  // Initial + periodic refresh.
  useEffect(() => {
    loadWorkflows();
  }, [loadWorkflows]);
  useEffect(() => {
    const t = setInterval(() => {
      loadWorkflows();
      if (selectedID) loadDetail(selectedID);
    }, 5000);
    return () => clearInterval(t);
  }, [loadWorkflows, loadDetail, selectedID]);

  // Live step status overlay for the graph. Keys are step ids;
  // populated by workflow.step.started / workflow.step.completed
  // events for the currently-open workflow's runs only.
  const [liveStatuses, setLiveStatuses] = useState<Record<string, StepStatus>>({});

  // Live updates: lifecycle events refresh the list + the open
  // detail view. Step events update the graph overlay in place.
  useAppEvents<Record<string, unknown>>("workflows", projectId, (ev) => {
    if (
      ev.topic === "workflow.created" ||
      ev.topic === "workflow.updated" ||
      ev.topic === "workflow.deleted" ||
      ev.topic === "workflow.run.finished" ||
      ev.topic === "workflow.run.cancelled"
    ) {
      loadWorkflows();
      if (selectedID) loadDetail(selectedID);
      // A finished run clears the live overlay (final per-step
      // statuses come back via the run row).
      if (ev.topic === "workflow.run.finished") {
        setLiveStatuses({});
      }
      return;
    }
    if (ev.topic === "workflow.step.started" || ev.topic === "workflow.step.completed") {
      const data = ev.data;
      const wfID = Number(data.workflow_id);
      if (selectedID !== wfID) return;
      const stepID = String(data.step_id || "");
      if (!stepID) return;
      const status = ev.topic === "workflow.step.started" ? "running" : String(data.status || "ok");
      const attempt = Number(data.attempt) || 1;
      const error = String(data.error || "");
      setLiveStatuses((prev) => ({
        ...prev,
        [stepID]: { status, attempt, error: error || undefined },
      }));
    }
  });

  // ─── Actions ─────────────────────────────────────────────────────

  const select = (id: number) => {
    setSelectedID(id);
    loadDetail(id);
  };
  const closeDetail = () => {
    setSelectedID(null);
    setSelectedWf(null);
    setRuns([]);
  };

  const replayRun = async (runID: number) => {
    try {
      await apiCall<{ run: Run }>("POST", `/workflows/${selectedID}/run`, {});
      // The platform proxy doesn't expose POST /runs/<id>/replay yet;
      // use the MCP tool path instead via the sidecar's /workflows/<id>/run.
      // For now, replay = re-run with empty input. v0.2 wires this fully.
      if (selectedID) await loadDetail(selectedID);
    } catch (e) {
      setErr((e as Error).message);
    }
    void runID;
  };

  const runWith = async (input: unknown) => {
    if (!selectedWf) return;
    try {
      await apiCall<{ run: Run }>("POST", `/workflows/${selectedWf.id}/run`, input);
      setShowRunModal(false);
      await loadDetail(selectedWf.id);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  // ─── Render ──────────────────────────────────────────────────────

  return (
    <div className="h-full flex flex-col">
      <header className="px-6 py-3 border-b border-border flex items-center gap-3">
        <h1 className="text-text font-medium">Workflows</h1>
        <span className="text-text-dim text-xs">
          {workflows.length} workflow{workflows.length !== 1 ? "s" : ""}
        </span>
        <select
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm ml-4"
        >
          <option value="">all</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
        </select>
        <button
          type="button"
          onClick={loadWorkflows}
          className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
        >
          Refresh
        </button>
      </header>

      <main className="flex-1 overflow-auto">
        {err ? (
          <div className="p-6 text-red text-sm">{err}</div>
        ) : workflows.length === 0 ? (
          <div className="py-12 px-6 text-center text-text-muted text-sm">
            No workflows yet. Ask the agent to <span className="text-text">create one</span> via{" "}
            <code className="text-accent text-xs">workflows_create</code>.
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50 sticky top-0">
              <tr>
                <th className="text-left px-4 py-2 font-normal">Name</th>
                <th className="text-left px-4 py-2 font-normal w-24">Version</th>
                <th className="text-left px-4 py-2 font-normal w-32">Trigger</th>
                <th className="text-left px-4 py-2 font-normal w-32">Status</th>
                <th className="text-left px-4 py-2 font-normal w-40">Updated</th>
              </tr>
            </thead>
            <tbody>
              {workflows.map((w) => (
                <tr
                  key={w.id}
                  onClick={() => select(w.id)}
                  className="border-t border-border cursor-pointer hover:bg-bg-input/50"
                >
                  <td className="px-4 py-2 text-text font-medium truncate max-w-md">{w.name}</td>
                  <td className="px-4 py-2 text-text-muted">v{w.version}</td>
                  <td className="px-4 py-2 text-text-muted">{w.trigger_kind}</td>
                  <td className="px-4 py-2">
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${statusClass(w.status)}`}>
                      {w.status}
                    </span>
                  </td>
                  <td className="px-4 py-2 text-text-muted">{fmtTime(w.updated_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </main>

      {selectedWf && (
        <WorkflowDetail
          wf={selectedWf}
          runs={runs}
          liveStatuses={liveStatuses}
          onClose={closeDetail}
          onRun={() => setShowRunModal(true)}
          onOpenRun={loadRunSteps}
          onReplay={replayRun}
          onSave={async (yaml) => {
            await apiCall<{ workflow: Workflow }>("PATCH", `/workflows/${selectedWf.id}`, {
              source: yaml,
            });
            await loadDetail(selectedWf.id);
          }}
        />
      )}

      {activeRun && <RunTrace run={activeRun} onClose={() => setActiveRun(null)} />}

      {showRunModal && selectedWf && (
        <RunInputModal
          wf={selectedWf}
          onClose={() => setShowRunModal(false)}
          onRun={runWith}
        />
      )}
    </div>
  );
}

// ─── Workflow detail (overlay): graph + editor + runs panel ────────
//
// Architecture: parse the workflow's YAML source into a typed
// WorkflowDef once, treat that as the source of truth. The graph
// renders from it, the side-panel editor mutates it in-place, and
// the YAML tab is a derived view. Save serializes back to YAML
// and PATCHes the workflow row.

function WorkflowDetail({
  wf,
  runs,
  liveStatuses,
  onClose,
  onRun,
  onOpenRun,
  onReplay,
  onSave,
}: {
  wf: Workflow;
  runs: Run[];
  liveStatuses: Record<string, StepStatus>;
  onClose: () => void;
  onRun: () => void;
  onOpenRun: (runID: number) => void;
  onReplay: (runID: number) => void;
  onSave: (yaml: string) => Promise<void>;
}) {
  // Parse the source once on mount; subsequent edits live in `def`
  // until saved. parse errors block editing — the user gets a
  // visible warning in the toolbar.
  const initial = useMemo(() => safeParseYAML(wf.source), [wf.source, wf.id, wf.version]);
  const [def, setDef] = useState<WorkflowDef | null>(initial.def);
  const [parseErr, setParseErr] = useState<string | null>(initial.err);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveErr, setSaveErr] = useState<string>("");
  const [tab, setTab] = useState<"graph" | "yaml">("graph");
  const [selectedStepID, setSelectedStepID] = useState<string | null>(null);
  const [yamlText, setYamlText] = useState(wf.source);
  // Reset state when the workflow changes (different row opened).
  useEffect(() => {
    setDef(initial.def);
    setParseErr(initial.err);
    setYamlText(wf.source);
    setDirty(false);
    setSelectedStepID(null);
  }, [wf.id, initial.def, initial.err, wf.source]);

  // ─── State mutators ──────────────────────────────────────────────

  const patchStep = (next: StepDef) => {
    if (!def) return;
    const idx = def.steps.findIndex((s) => s.id === selectedStepID);
    if (idx < 0) return;
    const steps = def.steps.slice();
    steps[idx] = next;
    const newDef = { ...def, steps };
    setDef(newDef);
    setDirty(true);
    // If the user renamed the step, follow the rename so the form
    // stays attached to the same row.
    if (next.id !== selectedStepID) setSelectedStepID(next.id);
    setYamlText(stringifyYAML(newDef));
  };

  const deleteStep = () => {
    if (!def || !selectedStepID) return;
    const newDef = { ...def, steps: def.steps.filter((s) => s.id !== selectedStepID) };
    setDef(newDef);
    setSelectedStepID(null);
    setDirty(true);
    setYamlText(stringifyYAML(newDef));
  };

  const addStep = () => {
    if (!def) return;
    const id = uniqueStepID(def, "new_step");
    const newStep: StepDef = { id, kind: "emit", topic: "todo" };
    const newDef = { ...def, steps: [...def.steps, newStep] };
    setDef(newDef);
    setSelectedStepID(id);
    setDirty(true);
    setYamlText(stringifyYAML(newDef));
  };

  // YAML tab edits flow back through the parser so the graph stays
  // in sync (when the YAML is valid).
  const onYamlChange = (text: string) => {
    setYamlText(text);
    setDirty(true);
    const r = safeParseYAML(text);
    if (r.def) {
      setDef(r.def);
      setParseErr(null);
    } else {
      setParseErr(r.err);
    }
  };

  const save = async () => {
    if (!def) return;
    setSaving(true);
    setSaveErr("");
    try {
      const yaml = tab === "yaml" ? yamlText : stringifyYAML(def);
      await onSave(yaml);
      setDirty(false);
    } catch (e) {
      setSaveErr((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const selectedStep = def && selectedStepID
    ? def.steps.find((s) => s.id === selectedStepID) || null
    : null;

  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg w-[95vw] h-[92vh] flex flex-col"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Toolbar */}
        <header className="px-4 py-3 border-b border-border flex items-center gap-3 shrink-0">
          <h2 className="text-text font-semibold truncate">{wf.name}</h2>
          <span className="text-text-dim text-xs">v{wf.version}</span>
          <span className={`text-[11px] px-2 py-0.5 rounded ${statusClass(wf.status)}`}>{wf.status}</span>
          <span className="text-text-dim text-xs">trigger: {wf.trigger_kind}</span>

          <div className="ml-auto flex items-center gap-1 border border-border rounded">
            <TabButton active={tab === "graph"} onClick={() => setTab("graph")}>Edit</TabButton>
            <TabButton active={tab === "yaml"} onClick={() => setTab("yaml")}>YAML</TabButton>
          </div>

          <button
            type="button"
            onClick={addStep}
            disabled={!def}
            className="px-2 py-1 text-xs border border-border rounded text-text-muted hover:bg-bg-input disabled:opacity-50"
          >
            + Step
          </button>
          <button
            type="button"
            onClick={onRun}
            disabled={wf.status !== "active" || dirty}
            title={dirty ? "Save changes before running" : ""}
            className="px-3 py-1 text-sm border border-accent text-accent rounded disabled:opacity-50"
          >
            Run…
          </button>
          <button
            type="button"
            onClick={save}
            disabled={!dirty || !!parseErr || saving}
            className="px-3 py-1 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {saving ? "Saving…" : dirty ? "Save" : "Saved"}
          </button>
          <button onClick={onClose} className="text-text-muted hover:text-text text-xl leading-none ml-2">×</button>
        </header>

        {(parseErr || saveErr) && (
          <div className="px-4 py-1.5 bg-red/10 border-b border-red/30 text-red text-xs">
            {parseErr ? `YAML parse: ${parseErr}` : saveErr}
          </div>
        )}

        {/* Body: graph (left) + side panel (right). YAML tab takes the full width. */}
        <div className="flex-1 flex min-h-0">
          {tab === "graph" ? (
            <>
              <div className="flex-1 min-w-0 border-r border-border">
                {def ? (
                  <WorkflowGraph
                    def={def}
                    statuses={liveStatuses}
                    selectedID={selectedStepID}
                    onSelect={setSelectedStepID}
                  />
                ) : (
                  <div className="p-6 text-text-muted text-sm">
                    Workflow source can't be parsed. Switch to the YAML tab to fix it.
                  </div>
                )}
              </div>
              <aside className="w-96 shrink-0 overflow-auto">
                {selectedStep ? (
                  <StepEditor
                    step={selectedStep}
                    onPatch={patchStep}
                    onDelete={deleteStep}
                  />
                ) : (
                  <RunsList
                    runs={runs}
                    onOpenRun={onOpenRun}
                    onReplay={onReplay}
                  />
                )}
              </aside>
            </>
          ) : (
            <textarea
              value={yamlText}
              onChange={(e) => onYamlChange(e.target.value)}
              className="flex-1 bg-bg-input border-0 p-4 text-text font-mono text-sm focus:outline-none resize-none"
              spellCheck={false}
            />
          )}
        </div>
      </div>
    </div>
  );
}

function TabButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "px-3 py-1 text-xs " +
        (active ? "bg-accent text-bg" : "text-text-muted hover:text-text")
      }
    >
      {children}
    </button>
  );
}

function RunsList({
  runs,
  onOpenRun,
  onReplay,
}: {
  runs: Run[];
  onOpenRun: (runID: number) => void;
  onReplay: (runID: number) => void;
}) {
  return (
    <div className="p-4">
      <h3 className="text-xs uppercase tracking-wide text-text-dim mb-3">
        Recent runs ({runs.length})
      </h3>
      {runs.length === 0 ? (
        <p className="text-text-muted text-sm">No runs yet. Click Run… in the toolbar to fire one.</p>
      ) : (
        <ul className="flex flex-col gap-2">
          {runs.map((r) => (
            <li key={r.id} className="border border-border rounded p-2 flex flex-col gap-1">
              <div className="flex items-center gap-2">
                <span className={`text-[10px] px-1.5 py-0.5 rounded ${statusClass(r.status)}`}>
                  {r.status}
                </span>
                <span className="text-text-muted text-xs">{fmtTime(r.started_at)}</span>
                <span className="text-text-dim text-xs ml-auto">{fmtDuration(r.duration_ms)}</span>
              </div>
              {r.error && (
                <p className="text-red text-xs truncate" title={r.error}>{r.error}</p>
              )}
              <div className="flex gap-2 mt-1">
                <button
                  type="button"
                  onClick={() => onOpenRun(r.id)}
                  className="text-accent text-xs hover:underline"
                >
                  Trace
                </button>
                <button
                  type="button"
                  onClick={() => onReplay(r.id)}
                  className="text-text-muted text-xs hover:text-text"
                >
                  Replay
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ─── YAML helpers ──────────────────────────────────────────────────

function safeParseYAML(src: string): { def: WorkflowDef | null; err: string | null } {
  if (!src || src.trim() === "") return { def: null, err: "empty source" };
  try {
    const parsed = parseYAML(src);
    if (!parsed || typeof parsed !== "object") return { def: null, err: "source must be an object" };
    const def = parsed as WorkflowDef;
    if (!Array.isArray(def.steps)) return { def: null, err: "missing steps array" };
    return { def, err: null };
  } catch (e) {
    return { def: null, err: (e as Error).message };
  }
}

function uniqueStepID(def: WorkflowDef, base: string): string {
  const existing = new Set(def.steps.map((s) => s.id));
  if (!existing.has(base)) return base;
  for (let i = 2; i < 1000; i++) {
    const candidate = `${base}_${i}`;
    if (!existing.has(candidate)) return candidate;
  }
  return base + "_" + Date.now();
}

// ─── Run trace modal ───────────────────────────────────────────────

function RunTrace({ run, onClose }: { run: Run; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-3xl w-full mx-4 overflow-auto flex flex-col max-h-[90vh] p-5 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="flex items-start gap-3">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-1">
              <h2 className="text-lg text-text font-semibold truncate">
                {run.workflow_name} run #{run.id}
              </h2>
              <span className={`text-[11px] px-2 py-0.5 rounded ${statusClass(run.status)}`}>
                {run.status}
              </span>
            </div>
            <p className="text-text-muted text-sm">
              {fmtTime(run.started_at)} · {fmtDuration(run.duration_ms)} · trigger {run.trigger_kind}
            </p>
          </div>
          <button onClick={onClose} className="text-text-muted hover:text-text text-xl leading-none">
            ×
          </button>
        </header>

        {run.error && (
          <div className="text-red text-sm bg-red/10 border border-red/30 rounded p-2">
            {run.error}
          </div>
        )}

        <section>
          <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Step trace</h3>
          {!run.steps || run.steps.length === 0 ? (
            <p className="text-text-muted text-sm">No steps recorded.</p>
          ) : (
            <ol className="flex flex-col gap-2">
              {run.steps.map((s, i) => (
                <StepRow key={s.id} step={s} index={i + 1} />
              ))}
            </ol>
          )}
        </section>

        {run.input_json && (
          <section>
            <h3 className="text-xs uppercase tracking-wide text-text-dim mb-2">Input</h3>
            <pre className="text-[11px] bg-bg-input border border-border rounded p-3 overflow-auto whitespace-pre-wrap max-h-32">
              {prettyJSON(run.input_json)}
            </pre>
          </section>
        )}
      </div>
    </div>
  );
}

function StepRow({ step, index }: { step: StepExecution; index: number }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <li className="border border-border rounded">
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-bg-input/50"
      >
        <span className="text-text-dim text-xs w-6">{index}.</span>
        <span className="text-text font-medium flex-1 truncate">{step.step_id}</span>
        <span className="text-text-dim text-xs w-16">{step.step_kind}</span>
        <span className={`text-[10px] px-1.5 py-0.5 rounded ${statusClass(step.status)}`}>
          {step.status}
        </span>
        <span className="text-text-muted text-xs w-16 text-right">{fmtDuration(step.duration_ms)}</span>
        {step.attempt > 1 && (
          <span className="text-text-dim text-xs">attempt {step.attempt}</span>
        )}
      </button>
      {expanded && (
        <div className="border-t border-border p-3 flex flex-col gap-2 bg-bg/40">
          {step.error && (
            <div>
              <div className="text-text-dim text-[10px] uppercase tracking-wide mb-1">Error</div>
              <pre className="text-[11px] text-red whitespace-pre-wrap">{step.error}</pre>
            </div>
          )}
          {step.input_json && (
            <div>
              <div className="text-text-dim text-[10px] uppercase tracking-wide mb-1">Input</div>
              <pre className="text-[11px] bg-bg-input border border-border rounded p-2 overflow-auto whitespace-pre-wrap max-h-32">
                {prettyJSON(step.input_json)}
              </pre>
            </div>
          )}
          {step.output_json && (
            <div>
              <div className="text-text-dim text-[10px] uppercase tracking-wide mb-1">Output</div>
              <pre className="text-[11px] bg-bg-input border border-border rounded p-2 overflow-auto whitespace-pre-wrap max-h-32">
                {prettyJSON(step.output_json)}
              </pre>
            </div>
          )}
        </div>
      )}
    </li>
  );
}

function prettyJSON(s?: string): string {
  if (!s) return "";
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// ─── Run-with-input modal ──────────────────────────────────────────

function RunInputModal({
  wf,
  onClose,
  onRun,
}: {
  wf: Workflow;
  onClose: () => void;
  onRun: (input: unknown) => void;
}) {
  const [text, setText] = useState("{}");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const fire = async () => {
    setErr("");
    let parsed: unknown;
    try {
      parsed = text.trim() === "" ? null : JSON.parse(text);
    } catch (e) {
      setErr("Input must be valid JSON: " + (e as Error).message);
      return;
    }
    setBusy(true);
    try {
      await onRun(parsed);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-bg/80 backdrop-blur-sm" />
      <div
        className="relative bg-bg-card border border-border rounded-lg shadow-lg max-w-xl w-full mx-4 overflow-auto flex flex-col max-h-[80vh] p-4 gap-3"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <div className="text-text font-medium">Run {wf.name}</div>
          <button onClick={onClose} className="text-text-muted hover:text-text">
            ×
          </button>
        </div>
        <label className="text-xs uppercase tracking-wide text-text-dim">Input (JSON)</label>
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          className="w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm font-mono min-h-[160px]"
          placeholder='{"customer_id": 123}'
        />
        <p className="text-text-dim text-xs">
          Available in steps as <code className="text-accent">{"{{ input.* }}"}</code>.
        </p>
        {err && <div className="text-red text-xs">{err}</div>}
        <div className="flex gap-2 justify-end mt-1">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm text-text-muted">
            Cancel
          </button>
          <button
            type="button"
            onClick={fire}
            disabled={busy}
            className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold disabled:opacity-50"
          >
            {busy ? "Running…" : "Run"}
          </button>
        </div>
      </div>
    </div>
  );
}
