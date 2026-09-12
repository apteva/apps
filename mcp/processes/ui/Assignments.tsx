import { useState } from "react";
import { RolesEditor, type Step, type Executor } from "./Workflow";
export type Parameter = {
  key: string;
  label?: string;
  type: "string" | "number" | "boolean";
  required?: boolean;
  default?: string | number | boolean;
  options?: string[];
};
export type Schedule = {
  kind: string;
  every?: string;
  cron?: string;
  timezone?: string;
};
export type Assignment = {
  roles?: Record<string, Executor>;
  id: string;
  process_id: string;
  revision: number;
  name: string;
  target: string;
  owner_agent_id: number;
  execution_mode: "agent" | "tasks";
  procedure_version: number;
  follow_latest: boolean;
  parameters: Record<string, unknown>;
  schedule?: Schedule;
  status: string;
  sync_pending: boolean;
  sync_error: string;
  next_run_at?: string;
  last_schedule_note?: string;
};
type Version = {
  version: number;
  definition: { parameters?: Parameter[]; steps?: Step[] };
};
export function ParameterValues({
  fields,
  values,
  onChange,
  prefix = "parameter",
}: {
  fields: Parameter[];
  values: Record<string, unknown>;
  onChange: (v: Record<string, unknown>) => void;
  prefix?: string;
}) {
  return (
    <>
      {fields.map((f) => {
        const value = values[f.key] ?? f.default ?? "";
        const set = (v: unknown) => onChange({ ...values, [f.key]: v });
        return (
          <div className="field" key={f.key}>
            <label htmlFor={`${prefix}-${f.key}`}>
              {f.label || f.key}
              {f.required ? " *" : ""}
            </label>
            {f.type === "boolean" ? (
              <select
                id={`${prefix}-${f.key}`}
                value={String(value)}
                onChange={(e) =>
                  set(e.target.value === "" ? null : e.target.value === "true")
                }
                required={f.required}
              >
                <option value="">Choose…</option>
                <option value="true">Yes</option>
                <option value="false">No</option>
              </select>
            ) : f.options?.length ? (
              <select
                id={`${prefix}-${f.key}`}
                value={String(value)}
                required={f.required}
                onChange={(e) => set(e.target.value)}
              >
                <option value="">Choose…</option>
                {f.options.map((o) => (
                  <option key={o}>{o}</option>
                ))}
              </select>
            ) : (
              <input
                id={`${prefix}-${f.key}`}
                type={f.type === "number" ? "number" : "text"}
                step="any"
                required={f.required}
                value={String(value)}
                onChange={(e) =>
                  set(
                    f.type === "number"
                      ? e.target.value === ""
                        ? null
                        : Number(e.target.value)
                      : e.target.value,
                  )
                }
              />
            )}
          </div>
        );
      })}
    </>
  );
}
export function ParameterEditor({
  fields,
  onChange,
}: {
  fields: Parameter[];
  onChange: (v: Parameter[]) => void;
}) {
  const update = (i: number, v: Partial<Parameter>) =>
    onChange(fields.map((f, j) => (i === j ? { ...f, ...v } : f)));
  return (
    <section className="card" style={{ marginTop: 20 }}>
      <div className="row between">
        <h2>Parameters</h2>
        <button
          type="button"
          onClick={() =>
            onChange([
              ...fields,
              { key: "", label: "", type: "string", required: true },
            ])
          }
        >
          Add parameter
        </button>
      </div>
      <p className="small muted">
        Define what changes per page or client. Values are set on each
        assignment; use connection references rather than credentials.
      </p>
      {fields.map((f, i) => (
        <div className="card" key={i} style={{ marginTop: 12 }}>
          <div className="row">
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`param-key-${i}`}>Key</label>
              <input
                id={`param-key-${i}`}
                required
                pattern="[a-zA-Z][a-zA-Z0-9_]*"
                value={f.key}
                placeholder="page_id"
                onChange={(e) => update(i, { key: e.target.value })}
              />
            </div>
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`param-label-${i}`}>Label</label>
              <input
                id={`param-label-${i}`}
                value={f.label || ""}
                placeholder="Patreon page"
                onChange={(e) => update(i, { label: e.target.value })}
              />
            </div>
          </div>
          <div className="row">
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`param-type-${i}`}>Type</label>
              <select
                id={`param-type-${i}`}
                value={f.type}
                onChange={(e) =>
                  update(i, {
                    type: e.target.value as Parameter["type"],
                    default: undefined,
                    options: undefined,
                  })
                }
              >
                <option value="string">Text</option>
                <option value="number">Number</option>
                <option value="boolean">Yes / no</option>
              </select>
            </div>
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor={`param-required-${i}`}>Required</label>
              <select
                id={`param-required-${i}`}
                value={String(!!f.required)}
                onChange={(e) =>
                  update(i, { required: e.target.value === "true" })
                }
              >
                <option value="true">Required</option>
                <option value="false">Optional</option>
              </select>
            </div>
            <button
              type="button"
              aria-label={`Remove parameter ${i + 1}`}
              onClick={() => onChange(fields.filter((_, j) => i !== j))}
            >
              Remove
            </button>
          </div>
          <ParameterValues
            fields={[{ ...f, required: false, label: "Default value" }]}
            values={{ [f.key]: f.default }}
            prefix={`default-${i}`}
            onChange={(v) =>
              update(i, {
                default:
                  v[f.key] === null
                    ? undefined
                    : (v[f.key] as Parameter["default"]),
              })
            }
          />
        </div>
      ))}
    </section>
  );
}
export default function Assignments({
  items,
  versions,
  agents,
  processStatus,
  api,
  onChanged,
  onRun,
}: {
  items: Assignment[];
  versions: Version[];
  agents: { id: number; name: string }[];
  processStatus: string;
  api: (path: string, method?: string, body?: unknown) => Promise<any>;
  onChanged: () => Promise<void>;
  onRun: (x: Assignment) => void;
}) {
  const [draft, setDraft] = useState<Assignment | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const current = versions[0]?.version || 1;
  const edit = (x?: Assignment) => {
    setError("");
    setDraft(
      x
        ? structuredClone(x)
        : {
            id: "",
            process_id: "",
            revision: 0,
            name: "",
            target: "",
            owner_agent_id: agents[0]?.id || 0,
            execution_mode: "agent",
            procedure_version: current,
            follow_latest: true,
            parameters: {},
            status: "paused",
            sync_pending: false,
            sync_error: "",
          },
    );
  };
  const work = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    try {
      await fn();
      await onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const action = (x: Assignment, name: string) =>
    work(async () => {
      await api(`/assignments/${x.id}/${name}`, "POST", {});
    });
  const workflow =
    versions.find(
      (v) =>
        v.version ===
        (draft?.follow_latest ? current : draft?.procedure_version),
    )?.definition.steps || [];
  const schema =
    versions.find(
      (v) =>
        v.version ===
        (draft?.follow_latest ? current : draft?.procedure_version),
    )?.definition.parameters || [];
  const set = (v: Partial<Assignment>) =>
    setDraft((d) => (d ? { ...d, ...v } : d));
  return (
    <>
      <div className="row between head">
        <div>
          <h2>Assignments</h2>
          <p className="muted">
            One procedure, different agents, pages, parameters, and schedules.
          </p>
        </div>
        <button
          className="primary"
          disabled={busy || processStatus === "archived"}
          onClick={() => edit()}
        >
          Add assignment
        </button>
      </div>
      {error && (
        <div role="alert" className="notice">
          {error}
        </div>
      )}
      {draft ? (
        <form
          className="card"
          onSubmit={(e) => {
            e.preventDefault();
            work(async () => {
              await api(
                `/assignments${draft.id ? `/${draft.id}` : ""}`,
                draft.id ? "PUT" : "POST",
                {
                  assignment: {
                    ...draft,
                    roles: Object.fromEntries(
                      Object.entries(draft.roles || {}).filter(([role]) =>
                        workflow.some((s) => s.role === role),
                      ),
                    ),
                  },
                  expected_revision: draft.revision,
                },
              );
              setDraft(null);
            });
          }}
        >
          <h2>{draft.id ? "Edit assignment" : "New assignment"}</h2>
          <div className="grid">
            <div>
              <div className="field">
                <label htmlFor="assignment-name">Assignment name</label>
                <input
                  id="assignment-name"
                  required
                  maxLength={160}
                  value={draft.name}
                  placeholder="Photography Patreon"
                  onChange={(e) => set({ name: e.target.value })}
                />
              </div>
              <div className="field">
                <label htmlFor="assignment-target">
                  Page, client, or business
                </label>
                <input
                  id="assignment-target"
                  maxLength={500}
                  value={draft.target || ""}
                  placeholder="Photography page"
                  onChange={(e) => set({ target: e.target.value })}
                />
              </div>
              <div className="field">
                <label htmlFor="assignment-agent">
                  Responsible agent / coordinator
                </label>
                <select
                  id="assignment-agent"
                  required
                  value={draft.owner_agent_id}
                  onChange={(e) =>
                    set({ owner_agent_id: Number(e.target.value) })
                  }
                >
                  <option value="0" disabled>
                    Choose an agent
                  </option>
                  {agents.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="assignment-version">Procedure version</label>
                <select
                  id="assignment-version"
                  value={
                    draft.follow_latest ? "latest" : draft.procedure_version
                  }
                  onChange={(e) => {
                    const version =
                      e.target.value === "latest"
                        ? current
                        : Number(e.target.value);
                    const fields =
                      versions.find((v) => v.version === version)?.definition
                        .parameters || [];
                    set({
                      follow_latest: e.target.value === "latest",
                      procedure_version: version,
                      parameters: Object.fromEntries(
                        Object.entries(draft.parameters).filter(([key]) =>
                          fields.some((f) => f.key === key),
                        ),
                      ),
                    });
                  }}
                >
                  <option value="latest">Follow latest version</option>
                  {versions.map((v) => (
                    <option key={v.version} value={v.version}>
                      Pin version {v.version}
                    </option>
                  ))}
                </select>
                <p className="small muted">
                  Running work always keeps its original version. New versions
                  take effect when the process is activated.
                </p>
              </div>
              <RolesEditor
                steps={workflow}
                roles={draft.roles || {}}
                owner={draft.owner_agent_id}
                agents={agents}
                onChange={(roles) => set({ roles })}
              />
              <h2>Parameter values</h2>
              {schema.length ? (
                <ParameterValues
                  fields={schema}
                  values={draft.parameters}
                  onChange={(parameters) => set({ parameters })}
                />
              ) : (
                <p className="muted">
                  Add parameters in the shared procedure to configure different
                  inputs here.
                </p>
              )}
            </div>
            <div>
              <div className="field">
                <label htmlFor="assignment-mode">Execution</label>
                <select
                  id="assignment-mode"
                  value={draft.execution_mode}
                  onChange={(e) =>
                    set({
                      execution_mode: e.target
                        .value as Assignment["execution_mode"],
                    })
                  }
                >
                  <option value="agent">Direct agent</option>
                  <option value="tasks">Tasks</option>
                </select>
                <p className="small muted">
                  {draft.execution_mode === "tasks"
                    ? "Requires the optional Tasks integration (3.6.0+)."
                    : "Progress and results are recorded in Processes."}
                </p>
              </div>
              <div className="field">
                <label htmlFor="assignment-cadence">Schedule</label>
                <select
                  id="assignment-cadence"
                  value={draft.schedule?.kind || "manual"}
                  onChange={(e) =>
                    set({
                      schedule:
                        e.target.value === "manual"
                          ? undefined
                          : e.target.value === "interval"
                            ? {
                                kind: "interval",
                                every: "24h",
                                timezone: "UTC",
                              }
                            : {
                                kind: "cron",
                                cron: "0 9 * * *",
                                timezone: "UTC",
                              },
                    })
                  }
                >
                  <option value="manual">On demand</option>
                  <option value="interval">Every interval</option>
                  <option value="cron">Calendar schedule</option>
                </select>
              </div>
              {draft.schedule && (
                <>
                  <div className="field">
                    <label htmlFor="assignment-frequency">
                      {draft.schedule.kind === "interval"
                        ? "Interval (e.g. 24h)"
                        : "Calendar expression (minute hour day month weekday)"}
                    </label>
                    <input
                      id="assignment-frequency"
                      required
                      value={
                        draft.schedule.kind === "interval"
                          ? draft.schedule.every
                          : draft.schedule.cron
                      }
                      onChange={(e) =>
                        set({
                          schedule: {
                            ...draft.schedule!,
                            [draft.schedule!.kind === "interval"
                              ? "every"
                              : "cron"]: e.target.value,
                          },
                        })
                      }
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="assignment-timezone">Timezone</label>
                    <input
                      id="assignment-timezone"
                      required
                      value={draft.schedule.timezone || "UTC"}
                      onChange={(e) =>
                        set({
                          schedule: {
                            ...draft.schedule!,
                            timezone: e.target.value,
                          },
                        })
                      }
                    />
                  </div>
                </>
              )}
              <p className="small muted">
                Pause an assignment before changing its settings. Other
                assignments continue independently.
              </p>
            </div>
          </div>
          <div className="row">
            <button
              type="button"
              disabled={busy}
              onClick={() => setDraft(null)}
            >
              Cancel
            </button>
            <button
              className="primary"
              disabled={busy || !draft.owner_agent_id}
            >
              {busy ? "Saving…" : "Save assignment"}
            </button>
          </div>
        </form>
      ) : (
        items.map((x) => (
          <article className="card run" key={x.id}>
            <div className="row between">
              <div>
                <h2>{x.name}</h2>
                <p className="muted">
                  {x.target || "No target label"} ·{" "}
                  {agents.find((a) => a.id === x.owner_agent_id)?.name ||
                    `Agent ${x.owner_agent_id}`}{" "}
                  · {x.execution_mode === "agent" ? "Direct agent" : "Tasks"}
                </p>
              </div>
              <span className={`pill ${x.status}`}>
                {x.status}
                {x.status === "active" && processStatus !== "active"
                  ? " · process paused"
                  : ""}
              </span>
            </div>
            <p className="small muted">
              {!x.schedule
                ? "On demand"
                : x.schedule.kind === "interval"
                  ? `Every ${x.schedule.every}`
                  : `${x.schedule.cron} · ${x.schedule.timezone}`}{" "}
              · Procedure v{x.procedure_version}
              {x.follow_latest ? " · follows latest" : " · pinned"}
            </p>
            <p className="small muted">
              Next run:{" "}
              {processStatus === "active" &&
              x.status === "active" &&
              x.next_run_at
                ? new Date(x.next_run_at).toLocaleString()
                : processStatus === "active" &&
                    x.status === "active" &&
                    x.schedule &&
                    x.execution_mode === "tasks"
                  ? "See Tasks schedule"
                  : "—"}
            </p>
            {Object.keys(x.parameters || {}).length > 0 && (
              <p className="small prose">
                {Object.entries(x.parameters)
                  .map(([k, v]) => `${k}: ${String(v)}`)
                  .join(" · ")}
              </p>
            )}
            {x.sync_pending && (
              <div role="status" className="notice">
                Synchronization pending:{" "}
                {x.sync_error || "Applying schedule change."}
                <button
                  disabled={busy}
                  onClick={() =>
                    action(
                      x,
                      x.status === "active"
                        ? "activate"
                        : x.status === "archived"
                          ? "archive"
                          : "pause",
                    )
                  }
                >
                  Retry
                </button>
              </div>
            )}
            {x.last_schedule_note && (
              <p className="small muted">{x.last_schedule_note}</p>
            )}
            <div className="row" style={{ marginTop: 15 }}>
              {x.status !== "archived" && (
                <>
                  <button
                    disabled={
                      busy ||
                      processStatus !== "active" ||
                      x.status !== "active" ||
                      x.sync_pending
                    }
                    onClick={() => onRun(x)}
                  >
                    Run now
                  </button>
                  <button
                    disabled={
                      busy ||
                      (x.status !== "active" && processStatus !== "active")
                    }
                    onClick={() =>
                      action(x, x.status === "active" ? "pause" : "activate")
                    }
                  >
                    {x.status === "active" ? "Pause" : "Activate"}
                  </button>
                  <button
                    disabled={
                      busy ||
                      x.sync_pending ||
                      (x.status === "active" && processStatus === "active")
                    }
                    onClick={() => edit(x)}
                  >
                    Edit assignment
                  </button>
                  <button
                    disabled={busy}
                    onClick={() => {
                      if (
                        window.confirm(
                          `Archive ${x.name}? Future runs stop; existing runs continue.`,
                        )
                      )
                        action(x, "archive");
                    }}
                  >
                    Archive
                  </button>
                </>
              )}
            </div>
          </article>
        ))
      )}
    </>
  );
}
