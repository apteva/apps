/// <reference path="./flow-css.d.ts" />
import { useEffect, useMemo, useState, useId } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  Handle,
  Position,
  MarkerType,
  BaseEdge,
  getSmoothStepPath,
  applyNodeChanges,
  type Node,
  type NodeProps,
  type Edge,
  type EdgeProps,
  type ReactFlowInstance,
} from "@xyflow/react";
import flowStyles from "@xyflow/react/dist/style.css" with { type: "text" };
import type { Step, StepRun } from "./Workflow";
import {
  canConnect,
  connectSteps,
  layoutSteps,
  nextStepKey,
  removeStep,
  stepProblem,
  NODE_WIDTH,
} from "./flow-model";
import styles from "./process-flow.css" with { type: "text" };

type StepData = {
  step: Step;
  execution?: StepRun;
  index: number;
  editable: boolean;
  problem: string;
  select: () => void;
  [key: string]: unknown;
};
type FlowNode = Node<StepData>;
function StepCard({ data, selected }: NodeProps<FlowNode>) {
  const s = data.step;
  return (
    <div
      className={`pf-step ${s.kind} ${selected ? "is-selected" : ""} ${data.problem ? "incomplete" : ""}`}
      role="button"
      tabIndex={0}
      aria-label={`Step ${data.index + 1}: ${s.name || "Untitled step"}`}
      aria-pressed={selected}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          data.select();
        }
      }}
    >
      <Handle
        type="target"
        position={Position.Left}
        isConnectable={data.editable}
        aria-label={`Input to ${s.name}`}
      />
      <div className="pf-step-top">
        <span className="pf-step-number">
          {String(data.index + 1).padStart(2, "0")}
        </span>
        <span className="pf-kind">
          {s.kind === "approval" ? "◇ Approval" : "▤ Work"}
        </span>
      </div>
      <strong className="pf-step-title">{s.name || "Untitled step"}</strong>
      <div className="pf-step-bottom">
        <span className="pf-role">{s.role || "Choose role"}</span>
        <span>
          {s.depends_on.length > 1
            ? `${s.depends_on.length} inputs`
            : s.depends_on.length
              ? "After previous step"
              : "Starts with run"}
        </span>
      </div>
      {data.execution && <div className="pf-execution" data-state={data.execution.state}>
        <span className={`pill ${data.execution.state}`}>{data.execution.decision || data.execution.state}</span>
        <span>{data.execution.progress}% · {data.execution.executor.kind === "human" ? "Human" : `Agent ${data.execution.executor.agent_id}`}</span>
        {data.execution.delivery_warning && <span>Delivery retry pending</span>}
      </div>}
      {data.problem && <div className="pf-problem">{data.problem}</div>}
      <Handle
        type="source"
        position={Position.Right}
        isConnectable={data.editable}
        aria-label={`Output from ${s.name}`}
      />
    </div>
  );
}
function Endpoint({ data }: NodeProps) {
  return (
    <div className={`pf-endpoint ${data.end ? "end" : ""}`}>
      {!!data.end && (
        <Handle type="target" position={Position.Left} isConnectable={false} />
      )}
      <span>{data.end ? "✓" : "▶"}</span>
      <strong>{String(data.label)}</strong>
      {!data.end && (
        <Handle type="source" position={Position.Right} isConnectable={false} />
      )}
    </div>
  );
}
function DependencyEdge(props: EdgeProps) {
  const { sourceX: sx, sourceY: sy, targetX: tx, targetY: ty } = props;
  const rail = props.data?.rail as number | undefined;
  const path =
    rail !== undefined
      ? `M ${sx} ${sy} C ${sx + 42} ${sy}, ${sx + 25} ${rail}, ${sx + 72} ${rail} L ${tx - 72} ${rail} C ${tx - 25} ${rail}, ${tx - 42} ${ty}, ${tx} ${ty}`
      : getSmoothStepPath({
          sourceX: sx,
          sourceY: sy,
          targetX: tx,
          targetY: ty,
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
          borderRadius: 18,
        })[0];
  return (
    <BaseEdge
      path={path}
      markerEnd={props.markerEnd}
      interactionWidth={24}
      style={props.style}
    />
  );
}
const nodeTypes = { step: StepCard, endpoint: Endpoint };
const edgeTypes = { dependency: DependencyEdge };

export function ProcessFlow({
  steps,
  onChange,
  examples,
  executions,
}: {
  steps: Step[];
  onChange?: (s: Step[]) => void;
  examples?: Step[];
  executions?: StepRun[];
}) {
  const editable = !!onChange,
    instanceID = useId();
  const [selected, setSelected] = useState(""),
    [selectedEdge, setSelectedEdge] = useState<{
      source: string;
      target: string;
    } | null>(null);
  const [flow, setFlow] = useState<ReactFlowInstance | null>(null),
    [nodes, setNodes] = useState<Node[]>([]);
  const [notice, setNotice] = useState("");
  const positioned = useMemo(() => {
    const arranged = layoutSteps(steps);
    return steps.map((s, i) => ({
      ...s,
      position: s.position || arranged[i].position!,
    }));
  }, [steps]);
  const minX = Math.min(0, ...positioned.map((s) => s.position.x)),
    maxX = Math.max(0, ...positioned.map((s) => s.position.x));
  const minY = Math.min(100, ...positioned.map((s) => s.position.y)),
    maxY = Math.max(100, ...positioned.map((s) => s.position.y));
  useEffect(() => {
    const stepNodes: Node[] = positioned.map((s, i) => ({
      id: s.key,
      type: "step",
      position: s.position,
      selected: selected === s.key,
      style: { width: NODE_WIDTH },
      data: {
        step: s,
        execution: executions?.find(e => e.key === s.key),
        index: i,
        editable,
        problem: editable ? stepProblem(s) : "",
        select: () => setSelected(s.key),
      },
    }));
    setNodes([
      {
        id: "__start",
        type: "endpoint",
        position: { x: minX - 200, y: (minY + maxY) / 2 + 48 },
        draggable: false,
        selectable: false,
        data: { label: "Run starts" },
      },
      ...stepNodes,
      {
        id: "__end",
        type: "endpoint",
        position: { x: maxX + NODE_WIDTH + 96, y: (minY + maxY) / 2 + 48 },
        draggable: false,
        selectable: false,
        data: { label: "Run complete", end: true },
      },
    ]);
  }, [positioned, editable, selected, executions]);
  const edges: Edge[] = useMemo(() => {
    const byKey = new Map(positioned.map((s) => [s.key, s]));
    const used = new Set(steps.flatMap((s) => s.depends_on));
    const edge = (source: string, target: string, virtual = false): Edge => {
      const from = byKey.get(source),
        to = byKey.get(target);
      const selected =
        selectedEdge?.source === source && selectedEdge?.target === target;
      const rail =
        from &&
        to &&
        (to.position.x - from.position.x > 450 ||
          to.position.x <= from.position.x)
          ? minY - 55
          : undefined;
      return {
        id: `${source}:${target}`,
        source,
        target,
        type: "dependency",
        data: { rail, virtual },
        selectable: !virtual && editable,
        markerEnd: {
          type: MarkerType.ArrowClosed,
          width: 18,
          height: 18,
          color: selected ? "var(--pc-accent)" : "var(--pf-edge)",
        },
        style: {
          stroke: selected ? "var(--pc-accent)" : "var(--pf-edge)",
          strokeWidth: selected ? 3 : 2,
          ...(virtual ? { strokeDasharray: "5 5" } : {}),
        },
      };
    };
    return steps.flatMap((s) => [
      ...(s.depends_on.length
        ? s.depends_on.filter((k) => byKey.has(k)).map((k) => edge(k, s.key))
        : [edge("__start", s.key, true)]),
      ...(!used.has(s.key) ? [edge(s.key, "__end", true)] : []),
    ]);
  }, [positioned, selectedEdge, editable]);
  // Refit when connections or available canvas width change; keep text edits and drags stable.
  const inspectorOpen = steps.some((s) => s.key === selected);
  const topology = steps
    .map((s) => `${s.key}:${s.depends_on.join(",")}`)
    .join("|");
  useEffect(() => {
    if (!flow) return;
    const timer = setTimeout(
      () => flow.fitView({ padding: 0.18, maxZoom: 1, duration: 220 }),
      100,
    );
    return () => clearTimeout(timer);
  }, [flow, topology, inspectorOpen]);
  const active = steps.find((s) => s.key === selected),
    activeIndex = steps.findIndex((s) => s.key === selected);
  const update = (patch: Partial<Step>) =>
    onChange?.(steps.map((s) => (s.key === selected ? { ...s, ...patch } : s)));
  const add = (kind: Step["kind"]) => {
    if (!onChange || steps.length >= 30) return;
    const key = nextStepKey(steps),
      predecessor = active || steps.at(-1);
    const next: Step = {
      key,
      name: kind === "approval" ? "Review & approve" : "New step",
      role: kind === "approval" ? "reviewer" : predecessor?.role || "worker",
      kind,
      instructions: "",
      expected_output: "",
      depends_on: predecessor ? [predecessor.key] : [],
    };
    const draft = [...positioned, next];
    const arranged = layoutSteps(draft);
    next.position = arranged.at(-1)!.position;
    onChange([...positioned, next]);
    setSelected(key);
    setSelectedEdge(null);
    setNotice("");
  };
  const connect = (source: string, target: string) => {
    if (!onChange) return;
    if (!canConnect(steps, source, target)) {
      setNotice(
        "That connection would duplicate a dependency or create a loop.",
      );
      return;
    }
    onChange(connectSteps(steps, source, target));
    setNotice("");
  };
  return (
    <section
      className={`pf-shell ${editable ? "editable" : "readonly"}`}
      aria-label={editable ? "Process flow editor" : "Process flow"}
    >
      <style>
        {flowStyles}
        {styles}
      </style>
      <div className="pf-toolbar">
        <div>
          <h2>
            Process flow{" "}
            <span>
              {steps.length} {steps.length === 1 ? "step" : "steps"}
            </span>
          </h2>
          <p>
            {editable
              ? "Connect steps to define the order. Select a card to edit it."
              : "Follow the connections. Select any step to see its instructions."}
          </p>
        </div>
        <div className="pf-actions">
          {editable && (
            <>
              <button
                type="button"
                disabled={steps.length >= 30}
                onClick={() => add("work")}
              >
                + Add step
              </button>
              <button
                type="button"
                disabled={steps.length >= 30}
                onClick={() => add("approval")}
              >
                ◇ Add approval
              </button>
              <button
                type="button"
                disabled={!steps.length}
                onClick={() => {
                  onChange!(layoutSteps(steps));
                  setTimeout(
                    () =>
                      flow?.fitView({
                        padding: 0.18,
                        maxZoom: 1,
                        duration: 220,
                      }),
                    60,
                  );
                }}
              >
                Auto layout
              </button>
            </>
          )}
          <button
            type="button"
            disabled={!steps.length}
            onClick={() =>
              flow?.fitView({ padding: 0.18, maxZoom: 1, duration: 220 })
            }
          >
            Fit flow
          </button>
        </div>
      </div>
      {notice && (
        <p role="status" className="pf-notice">
          {notice}
        </p>
      )}
      <div className={`pf-workspace ${active ? "has-inspector" : ""}`}>
        <div className="pf-canvas">
          {steps.length ? (
            <ReactFlow
              id={instanceID}
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              onInit={setFlow}
              onNodesChange={(changes) =>
                setNodes((current) => applyNodeChanges(changes, current))
              }
              onNodeClick={(_, node) => {
                if (node.type === "step") {
                  setSelected(node.id);
                  setSelectedEdge(null);
                }
              }}
              onNodeDragStop={(_, node) =>
                onChange?.(
                  positioned.map((s) =>
                    s.key === node.id
                      ? {
                          ...s,
                          position: {
                            x: Math.round(node.position.x),
                            y: Math.round(node.position.y),
                          },
                        }
                      : s,
                  ),
                )
              }
              onEdgeClick={(_, edge) => {
                if (!edge.data?.virtual && editable)
                  setSelectedEdge({ source: edge.source, target: edge.target });
              }}
              onPaneClick={() => {
                setSelectedEdge(null);
                setSelected("");
              }}
              onConnect={({ source, target }) => connect(source, target)}
              isValidConnection={(connection) =>
                canConnect(steps, connection.source, connection.target)
              }
              nodesDraggable={editable}
              nodesConnectable={editable}
              elementsSelectable={true}
              connectOnClick={true}
              deleteKeyCode={null}
              minZoom={0.2}
              maxZoom={1.6}
              fitView
              fitViewOptions={{ padding: 0.18, maxZoom: 1 }}
              zoomOnScroll={false}
              panOnScroll={false}
              defaultEdgeOptions={{ type: "dependency" }}
              aria-label="Connected process steps"
              colorMode="dark"
            >
              <Background gap={22} size={1} color="var(--pf-dot)" />
              <Controls showInteractive={false} />
            </ReactFlow>
          ) : (
            <div className="pf-empty">
              <div className="pf-empty-diagram">
                <span>▶</span>
                <i /> <span>+</span>
                <i />
                <span>✓</span>
              </div>
              <h3>
                {editable
                  ? "Build your process, one step at a time"
                  : "This process has no structured steps"}
              </h3>
              <p>
                {editable
                  ? "Start with a work step, then connect what happens next. Add approval gates when a decision is needed."
                  : "The assigned agent follows the general instructions for the whole run."}
              </p>
              {editable && (
                <div className="pf-actions">
                  <button
                    type="button"
                    className="primary"
                    onClick={() => add("work")}
                  >
                    Add first step
                  </button>
                  {examples && (
                    <button
                      type="button"
                      onClick={() => {
                        onChange!(layoutSteps(structuredClone(examples)));
                        setSelected(examples[0].key);
                      }}
                    >
                      Use research → write → review → publish
                    </button>
                  )}
                </div>
              )}
            </div>
          )}
          {selectedEdge && editable && (
            <div className="pf-connection">
              <span>
                {steps.find((s) => s.key === selectedEdge.source)?.name} →{" "}
                {steps.find((s) => s.key === selectedEdge.target)?.name}
              </span>
              <button
                type="button"
                onClick={() => {
                  onChange!(
                    steps.map((s) =>
                      s.key === selectedEdge.target
                        ? {
                            ...s,
                            depends_on: s.depends_on.filter(
                              (k) => k !== selectedEdge.source,
                            ),
                          }
                        : s,
                    ),
                  );
                  setSelectedEdge(null);
                }}
              >
                Remove connection
              </button>
            </div>
          )}
          <div className="pf-canvas-caption">
            {editable
              ? "Drag cards to arrange · Drag or click the ports to connect"
              : "Arrows show dependencies · All incoming steps must finish"}
            <span>Scroll the page · Pinch to zoom</span>
          </div>
        </div>
        {active && (
          <aside className="pf-inspector" aria-label="Step details">
            <div className="pf-inspector-head">
              <div>
                <span className="pf-eyebrow">
                  STEP {String(activeIndex + 1).padStart(2, "0")}
                </span>
                <h3>{editable ? "Step settings" : active.name}</h3>
              </div>
              <button
                type="button"
                onClick={() => setSelected("")}
                aria-label="Close step details"
              >
                ×
              </button>
            </div>
            {editable ? (
              <>
                <label htmlFor={`${instanceID}-name`}>Name</label>
                <input
                  id={`${instanceID}-name`}
                  aria-label="Step name"
                  required
                  maxLength={160}
                  value={active.name}
                  onChange={(e) => update({ name: e.target.value })}
                />
                <div className="pf-two">
                  <div>
                    <label htmlFor={`${instanceID}-kind`}>Type</label>
                    <select
                      id={`${instanceID}-kind`}
                      value={active.kind}
                      onChange={(e) =>
                        update({ kind: e.target.value as Step["kind"] })
                      }
                    >
                      <option value="work">Work</option>
                      <option value="approval">Approval gate</option>
                    </select>
                  </div>
                  <div>
                    <label htmlFor={`${instanceID}-role`}>Role</label>
                    <input
                      id={`${instanceID}-role`}
                      required
                      pattern="[a-zA-Z][a-zA-Z0-9_]{0,63}"
                      value={active.role}
                      onChange={(e) => update({ role: e.target.value })}
                    />
                  </div>
                </div>
                <p className="pf-hint">
                  Choose the agent for this role later, in Assignments.
                </p>
                <label htmlFor={`${instanceID}-instructions`}>
                  Instructions
                </label>
                <textarea
                  id={`${instanceID}-instructions`}
                  aria-label="Step instructions"
                  required
                  rows={6}
                  value={active.instructions}
                  onChange={(e) => update({ instructions: e.target.value })}
                  placeholder="What should happen in this step?"
                />
                <label htmlFor={`${instanceID}-output`}>Required output</label>
                <textarea
                  id={`${instanceID}-output`}
                  required
                  rows={3}
                  value={active.expected_output}
                  onChange={(e) => update({ expected_output: e.target.value })}
                  placeholder="What evidence confirms this step is complete?"
                />
                <fieldset>
                  <legend>Wait for</legend>
                  {steps
                    .filter((s) => s.key !== active.key)
                    .map((s) => {
                      const checked = active.depends_on.includes(s.key);
                      return (
                        <label className="pf-check" key={s.key}>
                          <input
                            type="checkbox"
                            checked={checked}
                            disabled={
                              !checked && !canConnect(steps, s.key, active.key)
                            }
                            onChange={(e) =>
                              e.target.checked
                                ? connect(s.key, active.key)
                                : update({
                                    depends_on: active.depends_on.filter(
                                      (k) => k !== s.key,
                                    ),
                                  })
                            }
                          />
                          {s.name || s.key}
                        </label>
                      );
                    })}
                  {!active.depends_on.length && (
                    <p className="pf-hint">Starts as soon as the run begins.</p>
                  )}
                </fieldset>
                <button
                  type="button"
                  className="pf-delete"
                  onClick={() => {
                    onChange!(removeStep(steps, active.key));
                    setSelected("");
                    setSelectedEdge(null);
                  }}
                >
                  Remove step
                </button>
              </>
            ) : (
              <>
                <div className="pf-read-meta">
                  <span>
                    {active.kind === "approval" ? "Approval gate" : "Work step"}
                  </span>
                  <span>Role: {active.role}</span>
                </div>
                <h4>Instructions</h4>
                <div className="prose">{active.instructions}</div>
                <h4>Required output</h4>
                <div className="prose">{active.expected_output}</div>
                <h4>Waits for</h4>
                <p>
                  {active.depends_on.length
                    ? active.depends_on
                        .map((k) => steps.find((s) => s.key === k)?.name || k)
                        .join(", ")
                    : "Run starts"}
                </p>
              </>
            )}
          </aside>
        )}
      </div>
    </section>
  );
}
