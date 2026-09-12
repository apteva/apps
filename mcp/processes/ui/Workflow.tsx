import { useState } from "react";
export type Step = {
  key: string;
  name: string;
  role: string;
  kind: "work" | "approval";
  instructions: string;
  expected_output: string;
  depends_on: string[];
};
export type Executor = { kind: "agent" | "human"; agent_id?: number };
export type StepRun = {
  id: string;
  run_id: string;
  key: string;
  definition: Step;
  executor: Executor;
  state: string;
  progress: number;
  output: string;
  error: string;
  decision: string;
  updated_by: string;
  updated_at: string;
  task_id?: string;
  delivery_warning?: string;
};
const examples: Step[] = [
  {
    key: "research",
    name: "Research",
    role: "researcher",
    kind: "work",
    instructions: "Research the topic using the configured sources.",
    expected_output: "Research notes with sources.",
    depends_on: [],
  },
  {
    key: "write",
    name: "Write",
    role: "writer",
    kind: "work",
    instructions:
      "Prepare a draft from the research and assignment parameters.",
    expected_output: "Complete draft ready for review.",
    depends_on: ["research"],
  },
  {
    key: "review",
    name: "Review",
    role: "reviewer",
    kind: "approval",
    instructions:
      "Review the draft against the procedure and audience requirements.",
    expected_output: "Approve or reject with a reason.",
    depends_on: ["write"],
  },
  {
    key: "publish",
    name: "Publish",
    role: "publisher",
    kind: "work",
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
  const update = (i: number, patch: Partial<Step>) =>
    onChange(steps.map((s, j) => (i === j ? { ...s, ...patch } : s)));
  return (
    <section className="card" style={{ marginTop: 20 }}>
      <div className="row between">
        <h2>Steps & roles</h2>
        <button
          type="button"
          onClick={() =>
            onChange([
              ...steps,
              {
                key: `step_${steps.length + 1}`,
                name: "",
                role: "worker",
                kind: "work",
                instructions: "",
                expected_output: "",
                depends_on: steps.length ? [steps[steps.length - 1].key] : [],
              },
            ])
          }
        >
          Add step
        </button>
      </div>
      {!steps.length ? (
        <>
          <p className="small muted">
            Without steps, the responsible agent executes the whole run. Add
            steps to assign roles and enforce handoffs.
          </p>
          <button
            type="button"
            onClick={() => onChange(structuredClone(examples))}
          >
            Use research → write → review → publish
          </button>
        </>
      ) : (
        <p className="small muted">
          Steps without dependencies run in parallel. Approval steps release
          dependent work only after an explicit approval.
        </p>
      )}
      {steps.map((s, i) => (
        <div className="card" style={{ marginTop: 14 }} key={i}>
          <div className="row between">
            <strong>Step {i + 1}</strong>
            <button
              type="button"
              onClick={() =>
                onChange(
                  steps
                    .filter((_, j) => i !== j)
                    .map((item) => ({
                      ...item,
                      depends_on: (item.depends_on || []).filter(
                        (key) => key !== s.key,
                      ),
                    })),
                )
              }
            >
              Remove step
            </button>
          </div>
          <div className="row" style={{ marginTop: 12 }}>
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`step-key-${i}`}>Key</label>
              <input
                id={`step-key-${i}`}
                required
                value={s.key}
                pattern="[a-zA-Z][a-zA-Z0-9_]*"
                onChange={(e) => {
                  const key = e.target.value;
                  onChange(
                    steps.map((item, j) => ({
                      ...item,
                      ...(j === i ? { key } : {}),
                      depends_on: (item.depends_on || []).map((dep) =>
                        dep === s.key ? key : dep,
                      ),
                    })),
                  );
                }}
              />
            </div>
            <div className="field" style={{ flex: 2 }}>
              <label htmlFor={`step-name-${i}`}>Name</label>
              <input
                id={`step-name-${i}`}
                required
                value={s.name}
                onChange={(e) => update(i, { name: e.target.value })}
              />
            </div>
          </div>
          <div className="row">
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`step-role-${i}`}>Role</label>
              <input
                id={`step-role-${i}`}
                required
                pattern="[a-zA-Z][a-zA-Z0-9_]*"
                value={s.role}
                onChange={(e) => update(i, { role: e.target.value })}
              />
            </div>
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`step-kind-${i}`}>Step type</label>
              <select
                id={`step-kind-${i}`}
                value={s.kind}
                onChange={(e) =>
                  update(i, { kind: e.target.value as Step["kind"] })
                }
              >
                <option value="work">Work</option>
                <option value="approval">Approval gate</option>
              </select>
            </div>
          </div>
          <div className="field">
            <label htmlFor={`step-instructions-${i}`}>Instructions</label>
            <textarea
              id={`step-instructions-${i}`}
              required
              rows={3}
              value={s.instructions}
              onChange={(e) => update(i, { instructions: e.target.value })}
            />
          </div>
          <div className="field">
            <label htmlFor={`step-output-${i}`}>Required output</label>
            <input
              id={`step-output-${i}`}
              required
              value={s.expected_output}
              onChange={(e) => update(i, { expected_output: e.target.value })}
            />
          </div>
          <fieldset
            style={{ border: "1px solid var(--pc-line)", borderRadius: 8 }}
          >
            <legend className="small muted">Wait for these steps</legend>
            {steps
              .filter((_, j) => i !== j)
              .map((dep) => (
                <label
                  key={dep.key}
                  style={{ display: "flex", alignItems: "center", gap: 8 }}
                >
                  <input
                    style={{ width: "auto" }}
                    type="checkbox"
                    checked={(s.depends_on || []).includes(dep.key)}
                    onChange={(e) =>
                      update(i, {
                        depends_on: e.target.checked
                          ? [...(s.depends_on || []), dep.key]
                          : (s.depends_on || []).filter((k) => k !== dep.key),
                      })
                    }
                  />
                  {dep.name || dep.key}
                </label>
              ))}
            {steps.length === 1 && (
              <span className="small muted">Starts when the run begins.</span>
            )}
          </fieldset>
        </div>
      ))}
    </section>
  );
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
        const fallback = steps.some(
          (s) => s.role === role && s.kind === "approval",
        )
          ? { kind: "human" as const }
          : { kind: "agent" as const, agent_id: owner };
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
  agents,
  projectId,
  api,
  onChanged,
}: {
  steps: StepRun[];
  runID: string;
  runState: string;
  agents: { id: number; name: string }[];
  projectId: string;
  api: (path: string, method?: string, body?: unknown) => Promise<any>;
  onChanged: () => Promise<void>;
}) {
  const [selected, setSelected] = useState(""),
    [output, setOutput] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const isTerminal = ["completed", "failed", "cancelled"].includes(runState);
  const submit = async (step: StepRun, decision?: string) => {
    setBusy(true);
    setError("");
    try {
      await api(`/runs/${runID}/steps/${step.id}`, "POST", {
        state: "completed",
        output,
        ...(decision ? { decision } : {}),
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
  return (
    <div style={{ marginTop: 18 }}>
      <div className="row between">
        <h2>Step execution</h2>
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
      {steps.map((s) => {
        const human = s.executor.kind === "human",
          actionable =
            human &&
            !isTerminal &&
            ["ready", "waiting", "running", "blocked"].includes(s.state);
        return (
          <section
            className="card"
            style={{ marginTop: 10, padding: 16 }}
            key={s.id}
          >
            <div className="row between">
              <strong>{s.definition.name}</strong>
              <span className={`pill ${s.state}`}>{s.decision || s.state}</span>
            </div>
            <p className="small muted">
              {s.definition.role} ·{" "}
              {human
                ? "Human · project operator"
                : agents.find((a) => a.id === s.executor.agent_id)?.name ||
                  `Agent ${s.executor.agent_id}`}
              {s.definition.kind === "approval" ? " · approval gate" : ""}
            </p>
            <p className="small muted">
              {s.definition.depends_on.length
                ? `Depends on: ${s.definition.depends_on.join(", ")}`
                : "Starts with the run"}
            </p>
            {s.delivery_warning && (
              <div className="notice">
                Delivery retry pending: {s.delivery_warning}
              </div>
            )}
            {s.output && <div className="prose">{s.output}</div>}
            {s.error && <div className="notice">{s.error}</div>}
            {s.updated_by && s.state === "completed" && (
              <p className="small muted">
                Recorded by {s.updated_by} ·{" "}
                {new Date(s.updated_at).toLocaleString()}
              </p>
            )}
            {s.task_id && (
              <a
                href={`/apps/tasks/page?${new URLSearchParams({ project_id: projectId, task_id: s.task_id })}`}
              >
                Open step task ↗
              </a>
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
                    {s.definition.kind === "approval"
                      ? "Review & decide"
                      : "Complete human step"}
                  </button>
                ) : (
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      submit(
                        s,
                        s.definition.kind === "approval"
                          ? "approved"
                          : undefined,
                      );
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
                            <div className="prose">{x.output}</div>
                          </div>
                        ))}
                    </details>
                    <div className="field" style={{ marginTop: 12 }}>
                      <label htmlFor={`decision-${s.id}`}>
                        {s.definition.kind === "approval"
                          ? "Decision reason and evidence"
                          : "Result and evidence"}
                      </label>
                      <textarea
                        id={`decision-${s.id}`}
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
                        {s.definition.kind === "approval"
                          ? "Approve"
                          : "Complete step"}
                      </button>
                      {s.definition.kind === "approval" && (
                        <button
                          type="button"
                          disabled={busy || !output.trim()}
                          onClick={() => submit(s, "rejected")}
                        >
                          Reject & stop run
                        </button>
                      )}
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
      })}
    </div>
  );
}
