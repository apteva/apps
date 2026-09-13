import { useEffect, useState } from "react";
import type { StepRun, Executor } from "./Workflow";
export type Task = StepRun & {
  project_id: string;
  origin: string;
  required: boolean;
  due_at: string;
  revision: number;
  created_by: string;
  delivered_at?: string;
  delivery_attempts?: number;
};
type Agent = { id: number; name: string };
type RunOption = {
  id: string;
  process_id: string;
  process_name: string;
  state: string;
};
type API = (path: string, method?: string, body?: unknown) => Promise<any>;
const terminal = (state: string) =>
  ["completed", "failed", "cancelled"].includes(state);
const executorValue = (e: Executor) =>
  e.kind === "human" ? "human" : String(e.agent_id);
const executorFrom = (value: string): Executor =>
  value === "human"
    ? { kind: "human" }
    : { kind: "agent", agent_id: Number(value) };
const dateInput = (value: string) => {
  if (!value) return "";
  const d = new Date(value);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
};
const dateValue = (value: string) =>
  value ? new Date(value).toISOString() : "";
function Assignee({
  value,
  onChange,
  agents,
  disabled = false,
}: {
  value: string;
  onChange: (v: string) => void;
  agents: Agent[];
  disabled?: boolean;
}) {
  return (
    <select
      aria-label="Task assignee"
      disabled={disabled}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="human">Human · project operator</option>
      {agents.map((a) => (
        <option key={a.id} value={a.id}>
          {a.name}
        </option>
      ))}
    </select>
  );
}
export function TaskComposer({
  api,
  agents,
  runID = "",
  onCreated,
  onClose,
}: {
  api: API;
  agents: Agent[];
  runID?: string;
  onCreated: (s: Task) => Promise<void>;
  onClose: () => void;
}) {
  const [title, setTitle] = useState(""),
    [instructions, setInstructions] = useState(""),
    [expected, setExpected] = useState(""),
    [assignee, setAssignee] = useState("human"),
    [due, setDue] = useState(""),
    [run, setRun] = useState(runID),
    [required, setRequired] = useState(false),
    [kind, setKind] = useState("work"),
    [runs, setRuns] = useState<RunOption[]>([]),
    [key] = useState(() => crypto.randomUUID()),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    if (!runID)
      api("/task-runs")
        .then((r) => active && setRuns(r.runs || []))
        .catch((e) => active && setError(e.message));
    return () => {
      active = false;
    };
  }, [runID]);
  return (
    <form
      className="card"
      aria-label="New task"
      style={{ marginTop: 16 }}
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError("");
        try {
          const r = await api("/tasks", "POST", {
            title,
            instructions,
            expected_output: expected,
            executor: executorFrom(assignee),
            due_at: dateValue(due),
            run_id: run,
            required: !!run && required,
            kind,
            idempotency_key: key,
          });
          await onCreated(r.task);
          onClose();
        } catch (e) {
          setError(e instanceof Error ? e.message : String(e));
        } finally {
          setBusy(false);
        }
      }}
    >
      <h2>{runID ? "Add task to this run" : "New task"}</h2>
      <fieldset disabled={busy} style={{ border: 0, padding: 0, minWidth: 0 }}>
        <label>
          Task title
          <input
            required
            maxLength={160}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
          />
        </label>
        <label>
          Instructions
          <textarea
            required
            maxLength={16000}
            rows={3}
            value={instructions}
            onChange={(e) => setInstructions(e.target.value)}
          />
        </label>
        <label>
          Expected result
          <input
            maxLength={4000}
            value={expected}
            onChange={(e) => setExpected(e.target.value)}
            placeholder="Result and supporting evidence"
          />
        </label>
        <div className="row">
          <label style={{ flex: "1 1 180px" }}>
            Assignee
            <Assignee agents={agents} value={assignee} onChange={setAssignee} />
          </label>
          <label style={{ flex: "1 1 180px" }}>
            Due date
            <input
              type="datetime-local"
              value={due}
              onChange={(e) => setDue(e.target.value)}
            />
          </label>
        </div>
        <label>
          Task type
          <select value={kind} onChange={(e) => setKind(e.target.value)}>
            <option value="work">Work</option>
            <option value="approval">Approval</option>
          </select>
        </label>
        {!runID && (
          <label>
            Attach to a run
            <select value={run} onChange={(e) => setRun(e.target.value)}>
              <option value="">Standalone task</option>
              {runs.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.process_name} · {r.id.slice(-8)}
                </option>
              ))}
            </select>
          </label>
        )}
        {run && (
          <label className="row">
            <input
              style={{ width: "auto" }}
              type="checkbox"
              checked={required}
              onChange={(e) => setRequired(e.target.checked)}
            />
            Required to finish this run
          </label>
        )}
        <p className="small muted">
          {assignee === "human"
            ? "Authorized project operators can complete this task."
            : "Creating this task sends ready work to the selected agent."}{" "}
          {run
            ? "Only this run is affected; the procedure stays as defined."
            : "No process or run is needed."}
        </p>
        {error && (
          <p className="notice" role="alert">
            {error}
          </p>
        )}
        <div className="row">
          <button className="primary">
            {busy ? "Creating…" : "Create task"}
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </fieldset>
    </form>
  );
}
export function TaskDetail({
  id,
  api,
  agents,
  onChanged,
  onClose,
}: {
  id: string;
  api: API;
  agents: Agent[];
  onChanged: () => Promise<void>;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<any>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [output, setOutput] = useState(""),
    [reason, setReason] = useState(""),
    [state, setState] = useState("running"),
    [due, setDue] = useState(""),
    [assignee, setAssignee] = useState("human"),
    [title, setTitle] = useState(""),
    [instructions, setInstructions] = useState("");
  const accept = (d: any) => {
    setDetail(d);
    setOutput(d.task.output || "");
    setReason(d.task.error || "");
    setDue(dateInput(d.task.due_at));
    setAssignee(executorValue(d.task.executor));
    setTitle(d.task.definition.name);
    setInstructions(d.task.definition.instructions);
  };
  useEffect(() => {
    let active = true;
    setDetail(null);
    setError("");
    api(`/tasks/${id}`)
      .then((d) => active && accept(d))
      .catch((e) => active && setError(e.message));
    return () => {
      active = false;
    };
  }, [id]);
  const save = async (body: Record<string, unknown>, cancel = false) => {
    setBusy(true);
    setError("");
    try {
      const d = await api(
        `/tasks/${id}${cancel ? "/cancel" : ""}`,
        cancel ? "POST" : "PUT",
        { ...body, expected_revision: detail.task.revision },
      );
      accept(d);
      await onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const s: Task | undefined = detail?.task;
  return (
    <section
      className="card"
      aria-label="Task details"
      style={{ marginTop: 16 }}
    >
      <div className="row between">
        <h2>{s?.definition.name || "Task details"}</h2>
        <button onClick={onClose}>Close task</button>
      </div>
      {error && (
        <p role="alert" className="notice">
          {error}
        </p>
      )}
      {!s ? (
        <p>Loading task…</p>
      ) : (
        <>
          <p>
            <span className={`pill ${s.state}`}>{s.decision || s.state}</span> ·{" "}
            {s.origin === "standalone"
              ? "Standalone"
              : s.origin === "attached"
                ? "Added to run"
                : "From process"}
            {s.run_id && ` · ${s.required ? "Required" : "Optional"}`}
          </p>
          {detail.process_name && (
            <p className="small muted">
              {detail.process_name} · run {s.run_id} · {detail.run?.state}
            </p>
          )}
          <p className="small muted">
            {s.executor.kind === "human"
              ? "Human · project operator"
              : agents.find((a) => a.id === s.executor.agent_id)?.name ||
                `Agent ${s.executor.agent_id}`}
            {s.due_at && ` · Due ${new Date(s.due_at).toLocaleString()}`}
          </p>
          <div className="prose">{s.definition.instructions}</div>
          <p>Expected result: {s.definition.expected_output}</p>
          {!!Object.keys(detail.dependency_outputs || {}).length && (
            <details open>
              <summary>Dependency results</summary>
              {Object.entries(detail.dependency_outputs).map(([key, value]) => (
                <div key={key}>
                  <strong>{key}</strong>
                  <div className="prose">{String(value)}</div>
                </div>
              ))}
            </details>
          )}
          {s.output && <div className="notice prose">{s.output}</div>}
          {s.error && <p className="notice">{s.error}</p>}
          {s.delivery_warning && (
            <p className="notice">
              Delivery retry pending: {s.delivery_warning}
            </p>
          )}
          {s.task_id && (
            <p className="muted">
              This procedure task uses the optional Tasks integration. Report
              its outcome in that linked task.
            </p>
          )}
          {detail.can_update && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                save({ state, output, error: reason });
              }}
              style={{ marginTop: 16 }}
            >
              <label>
                Result and evidence
                <textarea
                  maxLength={16000}
                  value={output}
                  onChange={(e) => setOutput(e.target.value)}
                  rows={3}
                />
              </label>
              <label>
                Blocker or failure reason
                <input
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                />
              </label>
              <div className="row">
                <label>
                  Status
                  <select
                    value={state}
                    onChange={(e) => setState(e.target.value)}
                  >
                    <option value="running">In progress</option>
                    <option value="waiting">Waiting</option>
                    <option value="blocked">Blocked</option>
                    <option value="failed">Failed</option>
                  </select>
                </label>
                <button disabled={busy}>Save progress</button>
                <button
                  className="primary"
                  type="button"
                  disabled={busy || !output.trim()}
                  onClick={() =>
                    save({
                      state: "completed",
                      output,
                      ...(s.definition.kind === "approval"
                        ? { decision: "approved" }
                        : {}),
                    })
                  }
                >
                  {s.definition.kind === "approval"
                    ? "Approve task"
                    : "Complete task"}
                </button>
                {s.definition.kind === "approval" && (
                  <button
                    type="button"
                    disabled={busy || !output.trim()}
                    onClick={() =>
                      save({ state: "completed", output, decision: "rejected" })
                    }
                  >
                    Reject task
                  </button>
                )}
              </div>
            </form>
          )}
          {detail.can_edit && (
            <details style={{ marginTop: 16 }}>
              <summary>Task settings</summary>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const body: Record<string, unknown> = {
                    due_at: dateValue(due),
                  };
                  if (
                    detail.can_reassign &&
                    assignee !== executorValue(s.executor)
                  )
                    body.executor = executorFrom(assignee);
                  if (detail.can_reassign && s.origin !== "process_step") {
                    if (title !== s.definition.name) body.title = title;
                    if (instructions !== s.definition.instructions)
                      body.instructions = instructions;
                  }
                  save(body);
                }}
              >
                <label>
                  Due date
                  <input
                    type="datetime-local"
                    value={due}
                    onChange={(e) => setDue(e.target.value)}
                  />
                </label>
                <label>
                  Assignee
                  <Assignee
                    value={assignee}
                    onChange={setAssignee}
                    agents={agents}
                    disabled={!detail.can_reassign}
                  />
                </label>
                {!detail.can_reassign && (
                  <p className="small muted">
                    Reassignment is locked once delivery may have started.
                  </p>
                )}
                {detail.can_reassign && s.origin !== "process_step" && (
                  <>
                    <label>
                      Task title
                      <input
                        required
                        maxLength={160}
                        value={title}
                        onChange={(e) => setTitle(e.target.value)}
                      />
                    </label>
                    <label>
                      Instructions
                      <textarea
                        required
                        value={instructions}
                        onChange={(e) => setInstructions(e.target.value)}
                      />
                    </label>
                  </>
                )}
                <button disabled={busy}>Save task settings</button>
              </form>
            </details>
          )}
          {detail.can_manage && !terminal(s.state) && !s.task_id && (
            <details style={{ marginTop: 16 }}>
              <summary>Cancel task</summary>
              <p className="small muted">
                Already dispatched actions cannot be revoked. Cancelling
                required work prevents successful run completion.
              </p>
              <label>
                Cancellation reason
                <input
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                />
              </label>
              <button
                disabled={busy || !reason.trim()}
                onClick={() => save({ reason }, true)}
              >
                Confirm task cancellation
              </button>
            </details>
          )}
          <details style={{ marginTop: 16 }}>
            <summary>History</summary>
            {(detail.history || []).map((h: any, i: number) => (
              <div className="block" key={i}>
                <p className="small muted">
                  {new Date(h.created_at).toLocaleString()} · {h.actor} ·{" "}
                  {h.details?.action || h.state}
                </p>
                {(h.output || h.error) && (
                  <div className="prose">{h.output || h.error}</div>
                )}
                {h.details?.action === "settings_changed" && (
                  <p className="small">
                    Assignee: {executorValue(h.details.before.executor)} →{" "}
                    {executorValue(h.details.after.executor)} · Due:{" "}
                    {h.details.after.due_at || "None"}
                  </p>
                )}
              </div>
            ))}
          </details>
          <button
            style={{ marginTop: 16 }}
            disabled={busy}
            onClick={async () => {
              try {
                accept(await api(`/tasks/${id}`));
                setError("");
              } catch (e) {
                setError(String(e));
              }
            }}
          >
            Refresh task
          </button>
        </>
      )}
    </section>
  );
}
export default function WorkPanel({
  api,
  agents,
  processes = [],
  runID = "",
  eventRevision,
  canCreate = true,
}: {
  api: API;
  agents: Agent[];
  processes?: { id: string; name: string }[];
  runID?: string;
  eventRevision?: number;
  canCreate?: boolean;
}) {
  const [items, setItems] = useState<Task[]>([]),
    [total, setTotal] = useState(0),
    [offset, setOffset] = useState(0),
    [creating, setCreating] = useState(false),
    [selected, setSelected] = useState(""),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const [search, setSearch] = useState(""),
    [assignee, setAssignee] = useState("all"),
    [state, setState] = useState("active"),
    [origin, setOrigin] = useState(""),
    [process, setProcess] = useState(""),
    [overdue, setOverdue] = useState(false);
  const query = new URLSearchParams({
    search,
    assignee,
    state,
    origin,
    process_id: process,
    run_id: runID,
    overdue: String(overdue),
    offset: String(offset),
    limit: "50",
  }).toString();
  const load = async () => {
    const d = await api(`/tasks?${query}`);
    setItems(d.tasks || []);
    setTotal(d.total || 0);
  };
  useEffect(() => {
    let active = true;
    setLoading(true);
    const refresh = () =>
      api(`/tasks?${query}`)
        .then((d) => {
          if (active) {
            setItems(d.tasks || []);
            setTotal(d.total || 0);
            setError("");
          }
        })
        .catch((e) => active && setError(e.message))
        .finally(() => active && setLoading(false));
    refresh();
    const timer = setInterval(refresh, 5000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [query, eventRevision]);
  const filter = (set: (v: string) => void, value: string) => {
    set(value);
    setOffset(0);
  };
  return (
    <section aria-label="Work">
      <div className="row between">
        <div>
          <h2>{runID ? "Tasks in this run" : "Work"}</h2>
          <p className="muted">
            {runID
              ? "Procedure tasks and extra work for this execution."
              : "One-off tasks, procedure tasks and approvals across your project."}
          </p>
        </div>
        <button
          className="primary"
          disabled={!canCreate}
          onClick={() => {
            setCreating(true);
            setSelected("");
          }}
        >
          {runID ? "Add task" : "+ New task"}
        </button>
      </div>
      {creating && (
        <TaskComposer
          api={api}
          agents={agents}
          runID={runID}
          onClose={() => setCreating(false)}
          onCreated={async (s) => {
            await load();
            setSelected(s.id);
          }}
        />
      )}
      {selected && (
        <TaskDetail
          key={selected}
          id={selected}
          api={api}
          agents={agents}
          onChanged={load}
          onClose={() => setSelected("")}
        />
      )}
      <div className="row filters" style={{ marginTop: 20 }}>
        <input
          aria-label="Search work"
          placeholder="Search tasks…"
          value={search}
          onChange={(e) => filter(setSearch, e.target.value)}
        />
        <select
          aria-label="Filter task assignee"
          value={assignee}
          onChange={(e) => filter(setAssignee, e.target.value)}
        >
          <option value="all">All assignees</option>
          <option value="human">Human operators</option>
          {agents.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        <select
          aria-label="Filter task status"
          value={state}
          onChange={(e) => filter(setState, e.target.value)}
        >
          <option value="active">Open tasks</option>
          <option value="">All statuses</option>
          {[
            "pending",
            "ready",
            "running",
            "waiting",
            "blocked",
            "completed",
            "failed",
            "cancelled",
          ].map((s) => (
            <option key={s}>{s}</option>
          ))}
        </select>
        <select
          aria-label="Filter task origin"
          value={origin}
          onChange={(e) => filter(setOrigin, e.target.value)}
        >
          <option value="">All origins</option>
          <option value="standalone">Standalone</option>
          <option value="process_step">From process</option>
          <option value="attached">Added to run</option>
        </select>
        {!runID && (
          <select
            aria-label="Filter task process"
            value={process}
            onChange={(e) => filter(setProcess, e.target.value)}
          >
            <option value="">All processes</option>
            {processes.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        )}
        <label className="row">
          <input
            type="checkbox"
            style={{ width: "auto" }}
            checked={overdue}
            onChange={(e) => {
              setOverdue(e.target.checked);
              setOffset(0);
            }}
          />
          Overdue only
        </label>
        <button onClick={() => load().catch((e) => setError(e.message))}>
          Refresh work
        </button>
      </div>
      {error && (
        <p role="alert" className="notice">
          {error}
        </p>
      )}
      {loading ? (
        <p>Loading tasks…</p>
      ) : !items.length ? (
        <div className="empty">
          <h2>No matching tasks</h2>
          <p>
            Create a standalone task, add work to a run, or adjust the filters.
          </p>
        </div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Task</th>
                <th>Assignee</th>
                <th>Status</th>
                <th>Due</th>
              </tr>
            </thead>
            <tbody>
              {items.map((s) => (
                <tr key={s.id}>
                  <td>
                    <button
                      onClick={() => {
                        setSelected(s.id);
                        setCreating(false);
                      }}
                    >
                      {s.definition.name}
                    </button>
                    <div className="sub">
                      {s.origin === "standalone"
                        ? "Standalone"
                        : `${s.origin === "attached" ? "Added to run" : "From process"} · ${s.run_id.slice(-8)} · ${s.required ? "Required" : "Optional"}`}
                      {s.definition.kind === "approval" && " · Approval"}
                    </div>
                  </td>
                  <td>
                    {s.executor.kind === "human"
                      ? "Human operator"
                      : agents.find((a) => a.id === s.executor.agent_id)
                          ?.name || `Agent ${s.executor.agent_id}`}
                  </td>
                  <td>
                    <span className={`pill ${s.state}`}>
                      {s.decision || s.state}
                    </span>
                    {s.delivery_warning && (
                      <div className="small">Delivery retry pending</div>
                    )}
                  </td>
                  <td>
                    {s.due_at ? new Date(s.due_at).toLocaleString() : "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="row between" style={{ marginTop: 12 }}>
        <span className="small muted">{total} matching tasks</span>
        <div className="row">
          <button
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - 50))}
          >
            Previous
          </button>
          <button
            disabled={offset + items.length >= total}
            onClick={() => setOffset(offset + 50)}
          >
            Next
          </button>
        </div>
      </div>
    </section>
  );
}
export function RunWork({
  runID,
  runState,
  api,
  agents,
  onChanged,
}: {
  runID: string;
  runState: string;
  api: API;
  agents: Agent[];
  onChanged: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div style={{ marginTop: 16 }}>
      <button
        onClick={() => {
          setOpen(!open);
          if (open) onChanged();
        }}
      >
        {open ? "Close task workspace" : "Tasks & add work"}
      </button>
      {open && (
        <div className="card" style={{ marginTop: 12 }}>
          <WorkPanel
            api={api}
            agents={agents}
            runID={runID}
            canCreate={!terminal(runState)}
          />
        </div>
      )}
    </div>
  );
}
