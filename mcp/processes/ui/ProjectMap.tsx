import { TimingDetails, TimingRules } from "./Timing";
/// <reference path="./flow-css.d.ts" />
import { useEffect, useMemo, useRef, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  Handle,
  Position,
  MarkerType,
  type Node,
  type NodeProps,
  type Edge,
  type ReactFlowInstance,
} from "@xyflow/react";
import flowStyles from "@xyflow/react/dist/style.css" with { type: "text" };
import {
  layoutProject,
  liveRun,
  overlayRuns,
  supplementalRuns,
  currentRunSteps,
  runColor,
  runLabel,
  STEP_WIDTH,
  ENDPOINT_WIDTH,
  ENDPOINT_HEIGHT,
  type MapProcess,
  type MapRun,
  type MapTrigger,
} from "./project-map-model";
import { FlowStatus, StepKind, flowState } from "./FlowStatus";
import { Workflow } from "lucide-react";
import themeStyles from "./flow-theme.css" with { type: "text" };
import type { Step, StepRun } from "./Workflow";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  agents?: { id: number; name: string }[];
};
type Selection = { processId: string; stepKey?: string; runId?: string };
type BoundaryData = {
  process: MapProcess;
  runs: MapRun[];
  selected: boolean;
  select: () => void;
};
type StepData = {
  step: Step;
  index: number;
  selected: boolean;
  vertical: boolean;
  executions: { run: MapRun; step?: StepRun; agent: string }[];
  select: (runId?: string) => void;
};
function Boundary({ data }: NodeProps<Node<BoundaryData>>) {
  const triggers = data.process.triggers || [];
  const activeTriggers = triggers.filter(
    (trigger) =>
      trigger.status === "active" &&
      !trigger.sync_pending &&
      trigger.subscription_enabled !== false,
  ).length;
  return (
    <section
      className={`pm-boundary ${data.selected ? "is-selected" : ""}`}
      aria-label={`${data.process.name} SOP`}
    >
      <button className="pm-boundary-head nodrag" onClick={data.select}>
        <strong>
          <Workflow size={17} aria-hidden="true" />
          {data.process.name}
        </strong>
        <span>
          {data.process.category || "Uncategorized"} · {data.process.steps?.length || 0} steps
        </span>
        <span>
          v{data.process.version} · {data.process.status}
        </span>
        <span>
          <b className="pm-live-count">
            {data.runs.filter(liveRun).length} live
          </b>{" "}
          · {data.process.assignments?.length || 0} assignments
        </span>
        <span
          className="pm-trigger-summary"
          title={triggers
            .map(
              (trigger) =>
                `${trigger.config?.name || "Unnamed trigger"} · ${trigger.config?.topic || "topic unavailable"}`,
            )
            .join(" · ")}
        >
          <b>
            {triggers.length ? `${activeTriggers}/${triggers.length} triggers` : "No triggers"}
          </b>
          {triggers.slice(0, 2).map((trigger) => (
            <span className="pm-trigger-chip" key={trigger.id}>
              {trigger.config?.name || trigger.config?.topic || "Unnamed trigger"}
            </span>
          ))}
          {triggers.length > 2 && <span>+{triggers.length - 2} more</span>}
        </span>
      </button>
      {!data.process.steps?.length && (
        <p className="pm-no-steps">
          No structured steps · select SOP for execution details
        </p>
      )}
    </section>
  );
}
function MapStep({ data }: NodeProps<Node<StepData>>) {
  return (
    <div
      className={`pm-step ${data.selected ? "is-selected" : ""}`}
      data-state={flowState(data.executions.map((e) => e.step?.state))}
    >
      <Handle
        type="target"
        position={data.vertical ? Position.Top : Position.Left}
        isConnectable={false}
      />
      <button className="pm-step-title nodrag" onClick={() => data.select()}>
        <StepKind index={data.index} />
        <strong>{data.step.name}</strong>
      </button>
      {data.executions.map(({ run, step, agent }) => (
        <button
          className="pm-execution nodrag"
          key={run.id}
          data-run={run.id}
          style={{ borderLeftColor: runColor(run.id) }}
          onClick={() => data.select(run.id)}
          title={`${runLabel(run)} · ${step?.state || "not started"} · ${agent}`}
        >
          <span>{runLabel(run)}</span>
          <span>
            <FlowStatus state={step?.state || "pending"} timing={step} />
            {agent && ` · ${agent}`}
          </span>
        </button>
      ))}
      <Handle
        type="source"
        position={data.vertical ? Position.Bottom : Position.Right}
        isConnectable={false}
      />
    </div>
  );
}
function MapEndpoint({ data }: NodeProps<Node<{ label: string; end?: boolean; title?: string; vertical?: boolean }>>) {
  const vertical = !!data.vertical;
  return (
    <div
      className={`pm-endpoint ${data.end ? "end" : ""}`}
      title={data.title || data.label}
    >
      {data.end && (
        <Handle
          type="target"
          position={vertical ? Position.Top : Position.Left}
          isConnectable={false}
        />
      )}
      <span>{data.end ? "✓" : "▶"}</span>
      <strong>{data.label}</strong>
      {!data.end && (
        <Handle
          type="source"
          position={vertical ? Position.Bottom : Position.Right}
          isConnectable={false}
        />
      )}
    </div>
  );
}
type RunCardData = {
  run: MapRun;
  process: MapProcess;
  selected: boolean;
  select: () => void;
};
function RunCard({ data }: NodeProps<Node<RunCardData>>) {
  const { run, process } = data;
  return (
    <button
      className={`pm-run-card nodrag ${data.selected ? "is-selected" : ""}`}
      data-run={run.id}
      data-state={run.state}
      style={{ borderLeftColor: runColor(run.id) }}
      onClick={data.select}
      title={`${run.id} · ${currentRunSteps(run)}`}
    >
      <span className="pm-run-card-meta">
        {run.version ? `v${run.version}` : "Version unknown"} ·{" "}
        {run.version !== process.version
          ? "Original execution"
          : "Run execution"}
        <FlowStatus state={run.state} />
      </span>
      <strong>{runLabel(run)}</strong>
      <span className="pm-run-current">{currentRunSteps(run)}</span>
    </button>
  );
}
const nodeTypes = {
  sop: Boundary,
  sopStep: MapStep,
  sopRun: RunCard,
  sopEndpoint: MapEndpoint,
};
function apiURL(props: Props, path: string) {
  const q = new URLSearchParams();
  if (props.projectId) q.set("project_id", props.projectId);
  if (props.installId) q.set("install_id", String(props.installId));
  return `/api/apps/${encodeURIComponent(props.appName || "processes")}/processes${path}?${q}`;
}
function detailURL(props: Props, process: MapProcess, run?: MapRun) {
  const q = new URLSearchParams({ project_id: props.projectId || "" });
  q.set("process_id", process.id);
  if (run) q.set("run_id", run.id);
  if (props.installId) q.set("install_id", String(props.installId));
  return `/apps/${encodeURIComponent(props.appName || "processes")}/page?${q}`;
}
export default function ProjectMap(props: Props) {
  const [processes, setProcesses] = useState<MapProcess[]>([]),
    [runs, setRuns] = useState<Record<string, MapRun[]>>({});
  const [search, setSearch] = useState(""),
    [status, setStatus] = useState(""),
    [category, setCategory] = useState(""),
    [liveOnly, setLiveOnly] = useState(false);
  const [loaded, setLoaded] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [refresh, setRefresh] = useState(0);
  const [selection, setSelection] = useState<Selection>();
  const flow = useRef<ReactFlowInstance | null>(null);
  const scope = `${props.projectId}:${props.installId}:${props.appName}`;
  const previousScope = useRef(scope);
  useEffect(() => {
    const controller = new AbortController();
    if (scope !== previousScope.current) {
      setProcesses([]);
      setRuns({});
      setLoaded(false);
      setSelection(undefined);
      previousScope.current = scope;
    }
    if (!props.projectId) {
      setBusy(false);
      return () => controller.abort();
    }
    const get = async (path: string) => {
      const response = await fetch(apiURL(props, path), {
        credentials: "same-origin",
        signal: controller.signal,
      });
      if (!response.ok)
        throw new Error(`Could not load map (${response.status})`);
      return response.json();
    };
    setBusy(true);
    void (async () => {
      try {
        const list = await get("");
        const ps: MapProcess[] = list.processes || [],
          result: Record<string, MapRun[]> = {},
          triggerResult: Record<string, MapTrigger[]> = {},
          warnings: string[] = [];
        const queue = [...ps];
        await Promise.all(
          Array.from({ length: Math.min(4, ps.length) }, async () => {
            while (queue.length && !controller.signal.aborted) {
              const p = queue.shift()!;
              try {
                const [d, triggerLists] = await Promise.all([
                  get(`/${encodeURIComponent(p.id)}/runs`),
                  Promise.all(
                    (p.assignments || []).map(async (assignment) => {
                      try {
                        const response = await get(
                          `/${encodeURIComponent(p.id)}/assignments/${encodeURIComponent(assignment.id)}/triggers`,
                        );
                        return (response.triggers || []) as MapTrigger[];
                      } catch (error) {
                        warnings.push(
                          `${p.name}: ${error instanceof Error ? error.message : error}`,
                        );
                        return [];
                      }
                    }),
                  ),
                ]);
                triggerResult[p.id] = triggerLists.flat();
                result[p.id] = [...(d.direct_runs || [])].map((x: any) => ({
                  ...x,
                  backend: "agent",
                }));
              } catch (e) {
                if (!controller.signal.aborted)
                  warnings.push(
                    `${p.name}: ${e instanceof Error ? e.message : e}`,
                  );
              }
            }
          }),
        );
        if (controller.signal.aborted) return;
        setProcesses(
          ps.map((process) => ({
            ...process,
            triggers: triggerResult[process.id] || [],
          })),
        );
        setRuns(result);
        setLoaded(true);
        setError(warnings.join(". "));
      } catch (e) {
        if (!controller.signal.aborted)
          setError(e instanceof Error ? e.message : String(e));
      } finally {
        if (!controller.signal.aborted) setBusy(false);
      }
    })();
    return () => controller.abort();
  }, [scope, props.eventRevision, refresh]);
  const visible = useMemo(
    () =>
      processes.filter(
        (p) =>
          (!search ||
            `${p.name} ${p.description || ""} ${p.category || ""} ${(p.tags || []).join(" ")}`
              .toLowerCase()
              .includes(search.toLowerCase())) &&
          (!status || p.status === status) &&
          (!category || (category === "uncategorized" ? !p.category : p.category === category)) &&
          (!liveOnly || (runs[p.id] || []).some(liveRun)),
      ),
    [processes, runs, search, status, category, liveOnly],
  );
  const graph = useMemo(() => {
    const nodes: Node[] = [],
      edges: Edge[] = [];
    for (const box of layoutProject(visible, runs)) {
      const p = box.process,
        rs = runs[p.id] || [],
        overlays = overlayRuns(p, rs),
        parentId = `sop:${p.id}`;
      const select = (stepKey?: string, runId?: string) =>
        setSelection({ processId: p.id, stepKey, runId });
      nodes.push({
        id: parentId,
        type: "sop",
        position: { x: box.x, y: box.y },
        style: { width: box.width, height: box.height, pointerEvents: "auto" },
        zIndex: 0,
        data: {
          process: p,
          runs: rs,
          selected:
            selection?.processId === p.id &&
            !selection?.stepKey &&
            !selection?.runId,
          select: () => select(),
        },
        selectable: false,
      });
      const steps = p.steps || [];
      if (steps.length) {
        const byKey = new Set(steps.map((step) => step.key));
        const used = new Set(
          steps.flatMap((step) => step.depends_on.filter((key) => byKey.has(key))),
        );
        const roots = steps.filter(
          (step) => !step.depends_on.some((key) => byKey.has(key)),
        );
        const terminals = steps.filter((step) => !used.has(step.key));
        const triggerTopics = (p.triggers || [])
          .map((trigger) => trigger.config?.topic || trigger.config?.name)
          .filter(Boolean) as string[];
        const startLabel =
          triggerTopics.length === 1
            ? triggerTopics[0]
            : triggerTopics.length
              ? `${triggerTopics.length} event triggers`
              : "Run starts";
        const startID = `${parentId}:start`;
        const endID = `${parentId}:end`;
        nodes.push(
          {
            id: startID,
            parentId,
            extent: "parent",
            type: "sopEndpoint",
            position: box.endpointPositions.start,
            style: { width: ENDPOINT_WIDTH, height: ENDPOINT_HEIGHT },
            zIndex: 2,
            data: {
              label: startLabel,
              title: triggerTopics.join(" · ") || "A manual or scheduled run starts here",
              vertical: box.vertical,
            },
          },
          {
            id: endID,
            parentId,
            extent: "parent",
            type: "sopEndpoint",
            position: box.endpointPositions.end,
            style: { width: ENDPOINT_WIDTH, height: ENDPOINT_HEIGHT },
            zIndex: 2,
            data: { label: "Run complete", end: true, vertical: box.vertical },
          },
        );
        for (const root of roots)
          edges.push({
            id: `${startID}:${root.key}`,
            source: startID,
            target: `${parentId}:${root.key}`,
            type: "smoothstep",
            zIndex: 1,
            markerEnd: { type: MarkerType.ArrowClosed, color: "var(--pf-edge)" },
            style: { stroke: "var(--pf-edge)", strokeWidth: 1.5, strokeDasharray: "5 5" },
          });
        for (const terminal of terminals)
          edges.push({
            id: `${parentId}:${terminal.key}:${endID}`,
            source: `${parentId}:${terminal.key}`,
            target: endID,
            type: "smoothstep",
            zIndex: 1,
            markerEnd: { type: MarkerType.ArrowClosed, color: "var(--pf-edge)" },
            style: { stroke: "var(--pf-edge)", strokeWidth: 1.5, strokeDasharray: "5 5" },
          });
      }
      for (const step of p.steps || []) {
        const id = `${parentId}:${step.key}`;
        nodes.push({
          id,
          parentId,
          extent: "parent",
          type: "sopStep",
          position: box.positions[step.key],
          style: {
            width: STEP_WIDTH,
            height: box.stepHeight,
            pointerEvents: "auto",
          },
          zIndex: 2,
          data: {
            step,
            index: p.steps!.indexOf(step),
            selected:
              selection?.processId === p.id && selection?.stepKey === step.key,
            vertical: box.vertical,
            select: (runId?: string) => select(step.key, runId),
            executions: overlays.map((run) => {
              const execution = run.steps?.find((s) => s.key === step.key);
              const agentID = execution?.executor?.agent_id;
              return {
                run,
                step: execution,
                agent:
                  execution?.executor?.kind === "human"
                    ? "Human"
                    : props.agents?.find((a) => a.id === agentID)?.name ||
                      (agentID ? `Agent ${agentID}` : ""),
              };
            }),
          },
        });
        for (const dep of step.depends_on)
          if (p.steps?.some((s) => s.key === dep))
            edges.push({
              id: `${id}:${dep}`,
              source: `${parentId}:${dep}`,
              target: id,
              type: "smoothstep",
              zIndex: 1,
              markerEnd: {
                type: MarkerType.ArrowClosed,
                color: "var(--pf-edge)",
              },
              style: {
                stroke: overlays.some((r) =>
                  r.steps?.some(
                    (s) => s.key === step.key && s.state === "running",
                  ),
                )
                  ? "var(--pc-accent)"
                  : "var(--pf-edge)",
                strokeWidth: 1.5,
              },
              animated: overlays.some((r) =>
                r.steps?.some(
                  (s) => s.key === step.key && s.state === "running",
                ),
              ),
            });
      }
      for (const run of supplementalRuns(p, rs))
        nodes.push({
          id: `${parentId}:run:${run.id}`,
          parentId,
          extent: "parent",
          type: "sopRun",
          position: box.runPositions[run.id],
          style: { width: box.runWidth, height: 110, pointerEvents: "auto" },
          zIndex: 2,
          data: {
            run,
            process: p,
            selected: selection?.runId === run.id,
            select: () => select(undefined, run.id),
          },
        });
    }
    return { nodes, edges };
  }, [visible, runs, props.agents, selection]);
  const selectedProcess = processes.find((p) => p.id === selection?.processId),
    selectedRun =
      selectedProcess &&
      runs[selectedProcess.id]?.find((r) => r.id === selection?.runId);
  const selectedStep = selectedProcess?.steps?.find(
    (s) => s.key === selection?.stepKey,
  );
  return (
    <section className="pm" aria-label="Project SOP map">
      <style>{flowStyles}</style>
      <style>
        {styles}
        {themeStyles}
      </style>
      <div className="pm-toolbar">
        <input
          type="search"
          aria-label="Search SOPs"
          placeholder="Search SOPs…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <select
          aria-label="Filter SOP category"
          value={category}
          onChange={(e) => setCategory(e.target.value)}
        >
          <option value="">All categories</option>
          <option value="uncategorized">Uncategorized</option>
          {Array.from(new Set(processes.map((p) => p.category).filter(Boolean) as string[])).sort().map((x) => <option key={x}>{x}</option>)}
        </select>
        <select
          aria-label="Filter SOP status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
        >
          <option value="">All statuses</option>
          {["draft", "active", "paused", "archived"].map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
        <label>
          <input
            type="checkbox"
            checked={liveOnly}
            onChange={(e) => setLiveOnly(e.target.checked)}
          />
          Live only
        </label>
        <button onClick={() => setRefresh((r) => r + 1)} disabled={busy}>
          {busy ? "Refreshing…" : "Refresh map"}
        </button>
        <button
          onClick={() =>
            flow.current?.fitView({ padding: 0.035, duration: 250 })
          }
        >
          Fit all SOPs
        </button>
      </div>
      {error && (
        <p role="alert" className="notice">
          {error}
        </p>
      )}
      <p className="pm-summary">
        {!props.projectId
          ? "Select a project to see its SOPs."
          : !loaded
            ? "Loading SOP map…"
            : `${visible.length} of ${processes.length} SOPs · ${Object.values(runs).flat().filter(liveRun).length} live executions${error ? " · execution data incomplete" : ""}`}
        <span>Every step · select a SOP or step for details</span>
      </p>
      <div className="pm-workspace">
        <div className="pm-canvas">
          {loaded && visible.length > 0 ? (
            <ReactFlow
              key={scope}
              nodes={graph.nodes}
              edges={graph.edges}
              nodeTypes={nodeTypes}
              onInit={(instance) => {
                flow.current = instance;
              }}
              fitView
              fitViewOptions={{ padding: 0.035 }}
              minZoom={0.08}
              maxZoom={1.6}
              nodesDraggable={false}
              nodesConnectable={false}
              elementsSelectable={false}
              panOnScroll
              zoomOnScroll={false}
              zoomOnPinch
              preventScrolling={false}
              proOptions={{ hideAttribution: true }}
            >
              <Background gap={24} size={1} color="var(--pf-dot)" />
              <Controls showInteractive={false} />
              <MiniMap
                pannable
                zoomable
                nodeColor={(n) =>
                  n.type === "sop" ? "var(--pf-divider)" : "var(--pc-accent)"
                }
              />
            </ReactFlow>
          ) : (
            loaded && (
              <div className="pm-empty">No SOPs match these filters.</div>
            )
          )}
        </div>
        {selectedProcess && (
          <aside className="pm-inspector" aria-label="Map details">
            <button
              className="pm-close"
              onClick={() => setSelection(undefined)}
              aria-label="Close map details"
            >
              ×
            </button>
            <h2>{selectedStep?.name || selectedProcess.name}</h2>
            <p>
              {selectedProcess.name} · SOP v{selectedProcess.version}
            </p>
            {selectedStep && (
              <>
                <TimingRules step={selectedStep} steps={selectedProcess.steps || []} />
                <p className="pm-instructions">{selectedStep.instructions}</p>
                <p>
                  <strong>Expected output:</strong>{" "}
                  {selectedStep.expected_output}
                </p>
              </>
            )}
            <a href={detailURL(props, selectedProcess)}>Open SOP</a>
            <h3>Executions</h3>
            {!(runs[selectedProcess.id] || []).length && (
              <p>No executions yet.</p>
            )}
            {(runs[selectedProcess.id] || [])
              .filter((r) => !r.schedule_kind || r.schedule_kind === "once")
              .sort((a, b) => Number(liveRun(b)) - Number(liveRun(a)))
              .map((run) => (
                <div
                  className="pm-run-detail"
                  key={run.id}
                  style={{ borderLeftColor: runColor(run.id) }}
                >
                  <button
                    onClick={() =>
                      setSelection({ ...selection!, runId: run.id })
                    }
                    aria-expanded={selectedRun?.id === run.id}
                  >
                    <strong>{runLabel(run)}</strong>
                    <span>
                      {run.state} ·{" "}
                      {run.version ? `v${run.version}` : "version unavailable"}
                    </span>
                  </button>
                  {run.version !== selectedProcess.version && (
                    <p>Original version · shown in execution details</p>
                  )}
                  {selectedRun?.id === run.id && (
                    <>
                      <ol>
                        {run.steps?.map((step) => (
                          <li key={step.id}>
                            <strong>{step.definition?.name || step.key}</strong>
                            <span>
                              {step.state}
                              {step.executor?.agent_id
                                ? ` · ${props.agents?.find((a) => a.id === step.executor.agent_id)?.name || `Agent ${step.executor.agent_id}`}`
                                : ""}
                            </span>
                            <TimingDetails step={step} />
                          </li>
                        ))}
                      </ol>
                      {!run.steps?.length && (
                        <p>
                          {run.current_step ||
                            "No structured step data for this run."}
                        </p>
                      )}
                      <a href={detailURL(props, selectedProcess, run)}>
                        Open run
                      </a>
                    </>
                  )}
                </div>
              ))}
          </aside>
        )}
      </div>
    </section>
  );
}
const styles = `
.pm{min-width:0;color:var(--pc-text,#e7edf5)}.pm *{box-sizing:border-box}
.pm .pm-toolbar{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:12px}
.pm .pm-toolbar input[type=search]{width:auto;min-width:160px;flex:1 1 220px;max-width:420px}
.pm .pm-toolbar select{width:auto;max-width:180px;flex:0 1 auto}
.pm .pm-toolbar label{display:flex;align-items:center;gap:7px;white-space:nowrap;margin:0;font-size:12px}
.pm .pm-toolbar input[type=checkbox]{width:15px;height:15px;min-width:0;flex:0 0 15px;margin:0;padding:0;appearance:auto;accent-color:var(--pc-accent,#ff8000)}
.pm .pm-summary{display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap;font-size:12px;color:var(--pc-muted,#9aa8b9);margin:12px 0}
.pm-workspace{position:relative;min-width:0}.pm-canvas{height:clamp(480px,72vh,1000px);border:1px solid var(--pf-border);border-radius:12px;overflow:hidden;background:var(--pc-bg,#10151d)}
.pm .react-flow__node-sop{border:none;background:none;border-radius:14px;z-index:0}.pm-boundary{height:100%;border:1.25px solid var(--pf-border);border-radius:14px;background:color-mix(in srgb,var(--pc-panel,#1b2430) 88%,var(--pc-bg));overflow:hidden}
.pm .pm-endpoint{display:flex;align-items:center;justify-content:center;gap:7px;width:100%;height:100%;border:1px solid var(--pf-border);border-radius:24px;padding:8px 10px;background:var(--pc-bg,#10151d);color:var(--pc-muted,#9aa8b9);font-size:10px;white-space:nowrap;overflow:visible}.pm .pm-endpoint span{color:var(--pc-accent,#ff8000)}.pm .pm-endpoint.end span{color:var(--pf-success,#62ccaa)}.pm .pm-endpoint strong{max-width:88px;overflow:hidden;text-overflow:ellipsis}.pm .pm-endpoint .react-flow__handle{opacity:0}
.pm button.pm-boundary-head{display:flex;flex-direction:column;gap:3px;width:100%;height:126px;text-align:left;padding:10px 22px;border:0;border-bottom:1px solid var(--pf-divider);border-radius:0;background:var(--pc-panel,#1b2430);color:inherit;cursor:pointer;font:inherit}
.pm-boundary-head strong{font-size:16px;line-height:20px;flex-shrink:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:100%}.pm-boundary-head span{font-size:11px;line-height:14px;flex-shrink:0;color:var(--pc-muted,#9aa8b9)}
.pm .pm-trigger-summary{display:flex;align-items:center;gap:6px;min-width:0;overflow:hidden;white-space:nowrap;color:var(--pc-muted,#9aa8b9);font-size:10px}.pm .pm-trigger-summary>b{color:var(--pc-accent,#ff8000);font-weight:600;flex:none}.pm .pm-trigger-chip{min-width:0;overflow:hidden;text-overflow:ellipsis;color:var(--pc-text,#e7edf5);font-size:10px}
.pm-step{height:100%;border:1px solid var(--pf-border);border-radius:10px;background:var(--pf-surface);box-shadow:0 4px 14px #0002;overflow:hidden}
.pm button.pm-step-title{display:flex;flex-direction:column;gap:7px;width:100%;height:90px;border:0;border-radius:0;background:none;color:inherit;text-align:left;padding:12px 15px;font:inherit;cursor:pointer}.pm-step-title strong{font-size:14px;line-height:1.35;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}.pm-step-title small{font-size:10px;color:var(--pc-muted,#9aa8b9);text-transform:uppercase;letter-spacing:.07em;max-width:100%;overflow:hidden;white-space:nowrap}
.pm button.pm-execution{display:flex;flex-direction:column;gap:2px;width:calc(100% - 16px);height:44px;margin:0 8px 4px;padding:3px 8px;background:var(--pc-bg,#10151d);border:1px solid var(--pf-divider);border-left:3px solid;border-radius:4px;text-align:left;font:inherit;color:inherit;cursor:pointer}.pm-execution span{font-size:10px;line-height:16px;overflow:hidden;white-space:nowrap;text-overflow:ellipsis;max-width:100%}.pm-execution b{font-weight:600}
.pm .react-flow__handle{width:7px;height:7px;background:var(--pf-edge);border:1px solid var(--pc-panel,#1b2430)}.pm .react-flow__controls button{padding:5px;width:28px;height:28px;border-radius:0;border:0;border-bottom:1px solid #334155;background:var(--pc-panel,#1b2430);color:var(--pc-text,#e7edf5)}.pm .react-flow__controls button svg{fill:currentColor;max-width:14px;max-height:14px}.pm .react-flow__minimap{background:var(--pc-panel,#1b2430);border-radius:6px;width:140px;height:95px}.pm .react-flow__minimap-mask{fill:#0002}
.pm-inspector{position:absolute;right:12px;top:12px;bottom:12px;width:min(350px,calc(100% - 24px));overflow:auto;background:var(--pc-panel,#1b2430);border:1px solid var(--pf-border);border-radius:10px;padding:18px;box-shadow:0 8px 32px #0005;z-index:6;font-size:12px}.pm .pm-inspector h2{font-size:16px;padding-right:26px;margin:0 0 10px}.pm .pm-inspector h3{font-size:13px;margin:22px 0 12px}.pm .pm-inspector p{line-height:1.6;overflow-wrap:anywhere}.pm .pm-close{float:right;padding:0 6px;font-size:20px;background:none;border:0}.pm-instructions{white-space:pre-wrap}.pm-run-detail{border-left:3px solid;margin:12px 0;padding:4px 0 4px 10px}.pm .pm-run-detail>button{display:flex;flex-direction:column;gap:5px;border:0;background:none;padding:0;text-align:left;color:inherit;font:inherit;cursor:pointer;max-width:100%;overflow-wrap:anywhere}.pm-run-detail span{color:var(--pc-muted,#9aa8b9);font-size:11px}.pm-run-detail ol{padding-left:17px}.pm-run-detail li{margin:12px 0}.pm-run-detail li span{display:block;margin-top:4px}.pm-no-steps,.pm-empty{padding:30px;color:var(--pc-muted,#9aa8b9);font-size:12px}.pm button:hover{filter:brightness(1.12)}
@media(max-width:600px){.pm .pm-toolbar input[type=search]{max-width:none}.pm .pm-summary>span{display:none}.pm .react-flow__minimap{display:none}.pm-canvas{height:65vh;min-height:460px}}

.pm .pm-boundary-head strong{display:flex;gap:9px;align-items:center}.pm-boundary-head strong svg{color:var(--pc-accent);flex:none}
.pm .pm-live-count{color:var(--pc-accent);font-weight:600}.pm .pm-boundary.is-selected{border-color:var(--pc-accent)}
.pm .pm-execution>span:last-child{display:flex;align-items:center;gap:5px}.pm .pm-execution .flow-status{border:0;padding:0 4px;flex:none;overflow:visible;font-size:9px;line-height:16px}.pm .pm-execution .flow-status svg{width:10px;height:10px}
.pm button.pm-run-card{height:100%;width:100%;display:flex;flex-direction:column;gap:10px;padding:12px 14px;text-align:left;font:inherit;color:var(--pc-text);background:var(--pf-surface);border:1px solid var(--pf-border);border-left:3px solid;border-radius:9px;cursor:pointer}
.pm-run-card-meta{display:flex;justify-content:space-between;gap:8px;align-items:center;font-size:10px;color:var(--pc-muted);width:100%}.pm-run-card strong{font-size:12px;line-height:18px;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.pm-run-current{font-size:11px;line-height:16px;max-width:100%;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;color:var(--pc-muted)}
.pm .pm-run-card.is-selected{outline:2px solid var(--pc-accent);outline-offset:3px}
`;
