import ResultContent from "./ResultContent";
import { TimingDetails, type TimingRule } from "./Timing";
import { useRef, useState, type ReactNode } from "react";
import { ProcessFlow } from "./ProcessFlow";
import ExecutionTools, { type ToolSource } from "./ExecutionTools";
export type Step = {
  start_after?: TimingRule;
  due_after?: TimingRule;
  key: string;
  name: string;
  role: string;
  instructions: string;
  expected_output: string;
  depends_on: string[];
  position?: { x: number; y: number };
};
export type Executor = { kind: "agent" | "human"; agent_id?: number };
export type StepRun = {
  released_at?: string;
  start_at?: string;
  due_at?: string;
  completed_at?: string;
  id: string;
  run_id: string;
  key: string;
  definition: Step;
  executor: Executor;
  state: string;
  progress: number;
  output: string;
  error: string;
  updated_by: string;
  updated_at: string;
  delivery_warning?: string;
  delivery_suspended?: boolean;
  target_thread_id?: string;
  execution_id?: string;
};
const examples: Step[] = [
  {
    key: "research",
    name: "Research",
    role: "researcher",
    instructions: "Research the topic using the configured sources.",
    expected_output: "Research notes with sources.",
    depends_on: [],
  },
  {
    key: "write",
    name: "Write",
    role: "writer",
    instructions:
      "Prepare a draft from the research and assignment parameters.",
    expected_output: "Complete draft ready for review.",
    depends_on: ["research"],
  },
  {
    key: "review",
    name: "Review",
    role: "reviewer",
    instructions:
      "Review the draft against the procedure and audience requirements.",
    expected_output: "Review result with findings and evidence.",
    depends_on: ["write"],
  },
  {
    key: "publish",
    name: "Publish",
    role: "publisher",
    instructions:
      "Publish the approved draft to the configured destination and record evidence.",
    expected_output: "Published URL and destination confirmation.",
    depends_on: ["review"],
  },
];
export function StepEditor({
  steps,
  onChange,
}: {
  steps: Step[];
  onChange: (s: Step[]) => void;
}) {
  return <ProcessFlow steps={steps} onChange={onChange} examples={examples} />;
}
export function RolesEditor({
  steps,
  roles,
  owner,
  agents,
  onChange,
}: {
  steps: Step[];
  roles: Record<string, Executor>;
  owner: number;
  agents: { id: number; name: string }[];
  onChange: (r: Record<string, Executor>) => void;
}) {
  const keys = Array.from(new Set(steps.map((s) => s.role)));
  if (!keys.length) return null;
  return (
    <section className="block">
      <h2>Who does each role?</h2>
      <p className="small muted">
        Human roles are handled by authorized project operators in this panel.
        The responsible agent remains the run coordinator.
      </p>
      {keys.map((role) => {
        const fallback = { kind: "agent" as const, agent_id: owner };
        const x = roles[role] || fallback;
        return (
          <div className="field" key={role}>
            <label htmlFor={`role-${role}`}>{role}</label>
            <select
              id={`role-${role}`}
              value={x.kind === "human" ? "human" : String(x.agent_id)}
              onChange={(e) =>
                onChange({
                  ...roles,
                  [role]:
                    e.target.value === "human"
                      ? { kind: "human" }
                      : { kind: "agent", agent_id: Number(e.target.value) },
                })
              }
            >
              {x.kind === "agent" &&
                !agents.some((a) => a.id === x.agent_id) && (
                  <option value={String(x.agent_id)} disabled>
                    {x.agent_id
                      ? `Agent ${x.agent_id} unavailable · choose a replacement`
                      : "Choose an agent"}
                  </option>
                )}
              <option value="human">Human · project operator</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </div>
        );
      })}
    </section>
  );
}
export function RunSteps({
  steps,
  runID,
  runState,
  controlMode = "automatic",
  waitingForAdvance = false,
  eligibleSteps = [],
  agents,
  projectId,
  api,
  onChanged,
  toolSources = [],
  selectedStepID, onSelectStep, renderSidePanel, onActivityStatus,
}: {
  steps: StepRun[];
  runID: string;
  runState: string;
  controlMode?: "automatic" | "step_by_step";
  waitingForAdvance?: boolean;
  eligibleSteps?: { id: string; key: string }[];
  agents: { id: number; name: string }[];
  projectId: string;
  api: (path: string, method?: string, body?: unknown) => Promise<any>;
  onChanged: () => Promise<void>;
  toolSources?: ToolSource[];
  selectedStepID?: string;
  onSelectStep?: (id: string) => void;
  renderSidePanel?: (details: ReactNode) => ReactNode;
  onActivityStatus?: (status: string) => void;
}) {
  const [selected, setSelected] = useState(""),
    [output, setOutput] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [chosen, setChosen] = useState<string[]>([]);
  const releaseKeys = useRef<Record<string, string>>({});
  const advance = async (ids: string[]) => {
    setBusy(true);
    setError("");
    try {
      for (const id of ids) {
        const key = (releaseKeys.current[id] ||= crypto.randomUUID());
        await api(`/runs/${runID}/advance`, "POST", {
          step_id: id,
          idempotency_key: key,
        });
      }
      setChosen([]);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      try {
        await onChanged();
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      }
      setBusy(false);
    }
  };
  const isTerminal = ["completed", "failed", "cancelled"].includes(runState);
  const submit = async (step: StepRun) => {
    setBusy(true);
    setError("");
    try {
      await api(`/runs/${runID}/steps/${step.id}`, "POST", {
        state: "completed",
        output,
      });
      setSelected("");
      setOutput("");
      await onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const renderStep = (s: StepRun) => {
    const human = s.executor.kind === "human",
      actionable =
        human &&
        (controlMode !== "step_by_step" || !!s.released_at) &&
        !isTerminal &&
        ["ready", "waiting", "running", "blocked"].includes(s.state);
    return (
      <section
        className="card run-step-details"
        style={renderSidePanel ? undefined : { marginTop: 10, padding: 16 }}
        key={s.id}
      >
        <div className="row between">
          <strong>{s.definition.name}</strong>
          <span className={`pill ${s.state}`}>{s.state}</span>
        </div>
        <p className="small muted">
          {s.definition.role} ·{" "}
          {human
            ? "Human · project operator"
            : agents.find((a) => a.id === s.executor.agent_id)?.name ||
              `Agent ${s.executor.agent_id}`}
        </p>
        <p className="small muted">
          {s.definition.depends_on.length
            ? `Depends on: ${s.definition.depends_on.join(", ")}`
            : "Starts with the run"}
        </p>
        <section className="step-copy">
          <h3>Instructions</h3><ResultContent content={s.definition.instructions} />
          <h3>Required output</h3><ResultContent content={s.definition.expected_output} />
        </section>
        {renderSidePanel && s.progress !== undefined && <div className="run-progress">
          <div className="run-progress-label"><strong>Step progress</strong><span>{s.progress}%</span></div>
          <progress aria-label="Step progress" max={100} value={s.progress} />
        </div>}
        <TimingDetails step={s} />
        {s.delivery_warning && (
          <div className="notice">
            {s.delivery_suspended
              ? "Delivery suspended—repair required"
              : "Delivery retry pending"}
            : {s.delivery_warning}
          </div>
        )}
        {s.output && <section className="step-output"><h3>Step output</h3><ResultContent content={s.output} /></section>}
        {s.error && <div className="notice">{s.error}</div>}
        {s.updated_by && s.state === "completed" && (
          <p className="small muted">
            Recorded by {s.updated_by} ·{" "}
            {new Date(s.updated_at).toLocaleString()}
          </p>
        )}
        {!human && (
          <ExecutionTools
            agentID={s.executor.agent_id}
            threadID={s.target_thread_id}
            executionID={s.execution_id}
            stepID={s.id}
            completedAt={s.completed_at || (s.state === "completed" ? s.updated_at : undefined)}
            live={!isTerminal && ["running", "ready", "waiting", "blocked"].includes(s.state)}
            sources={toolSources}
            defaultOpen={!!renderSidePanel}
            onActivityStatus={onActivityStatus}
          />
        )}
        {!isTerminal && eligibleSteps.some((item) => item.id === s.id) && (
          <button disabled={busy} onClick={() => advance([s.id])}>
            Run this step
          </button>
        )}
        {actionable && (
          <>
            {selected !== s.id ? (
              <button
                style={{ marginTop: 12 }}
                onClick={() => {
                  setSelected(s.id);
                  setOutput("");
                  setError("");
                }}
              >
                Complete human step
              </button>
            ) : (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  submit(s);
                }}
              >
                <div className="prose small" style={{ marginTop: 14 }}>
                  {s.definition.instructions}
                </div>
                <p className="small muted">
                  Required: {s.definition.expected_output}
                </p>
                <details open>
                  <summary>Completed inputs</summary>
                  {steps
                    .filter((x) => x.state === "completed")
                    .map((x) => (
                      <div className="block" key={x.id}>
                        <strong>{x.definition.name}</strong>
                        <ResultContent content={x.output} />
                      </div>
                    ))}
                </details>
                <div className="field" style={{ marginTop: 12 }}>
                  <label htmlFor={`result-${s.id}`}>
                    Result and evidence
                  </label>
                  <textarea
                    id={`result-${s.id}`}
                    required
                    rows={4}
                    value={output}
                    onChange={(e) => setOutput(e.target.value)}
                  />
                </div>
                <div className="row">
                  <button
                    className="primary"
                    disabled={busy || !output.trim()}
                  >
                    Complete step
                  </button>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => setSelected("")}
                  >
                    Cancel
                  </button>
                </div>
              </form>
            )}
          </>
        )}
      </section>
    );
  };
  return (
    <div className={renderSidePanel ? "run-detail-grid" : "run-steps"}>
      <div className="run-flow-panel">
        {controlMode === "step_by_step" && (
          <section className="notice" aria-label="Step-by-step controls">
            <strong>Step-by-step run</strong>
            <p>
              {isTerminal
                ? `Run ${runState}.`
                : waitingForAdvance
                  ? "Waiting for you to advance. Review saved outputs, then choose the next ready step."
                  : "Released work is active, or the run is waiting on dependencies or timing."}
            </p>
            {!isTerminal && eligibleSteps.length > 0 && (
              <>
                <div>
                  {eligibleSteps.map((item) => (
                    <label
                      key={item.id}
                      style={{ display: "block", marginBottom: 8 }}
                    >
                      <input
                        type="checkbox"
                        disabled={busy}
                        checked={chosen.includes(item.id)}
                        onChange={(e) =>
                          setChosen((ids) =>
                            e.target.checked
                              ? [...ids, item.id]
                              : ids.filter((id) => id !== item.id),
                          )
                        }
                      />{" "}
                      {steps.find((s) => s.id === item.id)?.definition.name ||
                        item.key}
                    </label>
                  ))}
                </div>
                <button
                  disabled={busy}
                  onClick={() => advance([eligibleSteps[0].id])}
                >
                  Run next step
                </button>
                <button
                  disabled={
                    busy ||
                    !chosen.some((id) => eligibleSteps.some((s) => s.id === id))
                  }
                  onClick={() =>
                    advance(
                      chosen.filter((id) =>
                        eligibleSteps.some((s) => s.id === id),
                      ),
                    )
                  }
                >
                  Run selected steps
                </button>
              </>
            )}
          </section>
        )}
        <ProcessFlow
          selectedKey={renderSidePanel ? steps.find(s => s.id === selectedStepID)?.key : undefined}
          onSelectStep={onSelectStep ? key => { const step = steps.find(s => s.key === key); if (step) onSelectStep(step.id); } : undefined}
          steps={steps.map((s) => s.definition)}
          executions={steps}
          agents={agents}
        />
        <div className="row between">
          {!renderSidePanel && <h2>Step execution</h2>}
          {!isTerminal && (
            <button
              disabled={busy}
              onClick={() => {
                setSelected("cancel-run");
                setOutput("");
                setError("");
              }}
            >
              Cancel run
            </button>
          )}
        </div>
        {selected === "cancel-run" && (
          <form
            className="notice"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              setError("");
              try {
                await api(`/runs/${runID}/cancel`, "POST", { reason: output });
                setSelected("");
                await onChanged();
              } catch (e) {
                setError(e instanceof Error ? e.message : String(e));
              } finally {
                setBusy(false);
              }
            }}
          >
            <p>
              Cancel future handoffs. Work already dispatched may still finish.
            </p>
            <label htmlFor={`cancel-${runID}`}>Reason</label>
            <textarea
              id={`cancel-${runID}`}
              required
              value={output}
              onChange={(e) => setOutput(e.target.value)}
            />
            <div className="row" style={{ marginTop: 12 }}>
              <button disabled={busy || !output.trim()}>
                Confirm cancellation
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => setSelected("")}
              >
                Keep running
              </button>
            </div>
          </form>
        )}
        {error && (
          <div className="notice" role="alert">
            {error}
          </div>
        )}
      </div>
      {renderSidePanel ? renderSidePanel(steps.filter(s => s.id === selectedStepID).map(renderStep)) : steps.map(renderStep)}
    </div>
  );
}
