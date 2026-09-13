import { useEffect, useState } from "react";
import WorkPanel, { RunWork } from "./Work";
import { StepEditor, RunSteps, type Step, type StepRun } from "./Workflow";
import Assignments, {
  ParameterEditor,
  ParameterValues,
  type Parameter,
  type Assignment,
} from "./Assignments";
type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
};
type Schedule = {
  kind: string;
  every?: string;
  cron?: string;
  timezone?: string;
};
type Definition = {
  steps?: Step[];
  parameters?: Parameter[];
  execution_mode: "agent" | "tasks";
  name: string;
  description: string;
  instructions: string;
  required_inputs: string;
  default_inputs: string;
  completion_criteria: string;
  approval_requirements: string;
  owner_agent_id: number;
  schedule?: Schedule;
};
type Process = Definition & {
  assignments?: Assignment[];
  next_run_at?: string;
  last_schedule_note?: string;
  id: string;
  status: string;
  version: number;
  sync_pending: boolean;
  sync_error: string;
  updated_at: string;
};
type Version = { version: number; definition: Definition };
type Entry = {
  assignment_id?: string;
  assignment?: Partial<Assignment>;
  backend: "agent" | "tasks";
  version: number;
  record: {
    id: string;
    trigger_event_id?: string;
    workflow?: boolean;
    steps?: StepRun[];
    title: string;
    state: string;
    schedule_kind?: string;
    schedule_enabled?: boolean;
    next_run_at?: string;
    parent_task_id?: string;
    scheduled_for?: string;
    delivery_warning?: string;
    progress?: number;
    current_step?: string;
    result?: string;
    error?: string;
    created_at: string;
  };
};
const empty: Definition = {
  execution_mode: "agent",
  name: "",
  description: "",
  instructions: "",
  required_inputs: "",
  default_inputs: "",
  completion_criteria: "",
  approval_requirements: "",
  owner_agent_id: 0,
};
type History = {
  runs?: {
    version: number;
    task: Entry["record"];
    assignment_id?: string;
    assignment?: Assignment;
  }[];
  direct_runs?: (Entry["record"] & {
    version: number;
    assignment_id?: string;
    assignment?: Assignment;
  })[];
  tasks_error?: string;
};
const historyEntries = (r: History): Entry[] =>
  [
    ...(r.runs || []).map((e) => ({
      backend: "tasks" as const,
      version: e.version,
      assignment_id: e.assignment_id,
      assignment: e.assignment,
      record: e.task,
    })),
    ...(r.direct_runs || []).map((e) => ({
      backend: "agent" as const,
      version: e.version,
      assignment_id: e.assignment_id,
      assignment: e.assignment,
      record: {
        ...e,
        title: e.workflow ? "Team workflow run" : "Direct agent run",
      },
    })),
  ].sort(
    (a, b) => Date.parse(b.record.created_at) - Date.parse(a.record.created_at),
  );
const cadence = (s?: Schedule) =>
  !s
    ? "On demand"
    : s.kind === "interval"
      ? `Every ${s.every}`
      : `${s.cron} · ${s.timezone || "UTC"}`;
const date = (s?: string) =>
  s
    ? new Date(s).toLocaleString(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      })
    : "—";
const fields = [
  ["instructions", "Steps to follow"],
  ["required_inputs", "Required inputs / sources"],
  ["default_inputs", "Standing context"],
  ["approval_requirements", "Approval checkpoints"],
  ["completion_criteria", "Completion criteria & evidence"],
] as const;
const css = `
.ap-processes{--pc-bg:var(--color-bg,#101216);--pc-panel:var(--color-bg-card,#181b21);--pc-line:var(--color-border,#30343e);--pc-text:var(--color-text,#eceef2);--pc-muted:var(--color-text-muted,#969eac);--pc-accent:var(--color-accent,#9ea8ff);color:var(--pc-text);background:var(--pc-bg);font-size:14px;line-height:1.55;min-height:100%;height:100%;overflow:auto;padding:28px;box-sizing:border-box}
.ap-processes *{box-sizing:border-box}.ap-processes h1{font-size:24px;line-height:1.25;margin:0;font-weight:650;letter-spacing:-.5px}.ap-processes h2{font-size:16px;margin:0 0 14px;font-weight:600}.ap-processes p{margin:6px 0}.ap-processes .muted{color:var(--pc-muted)}.ap-processes .row{display:flex;gap:12px;align-items:center;flex-wrap:wrap}.ap-processes .between{justify-content:space-between}.ap-processes .head{margin-bottom:24px}.ap-processes button,.ap-processes .button{font:inherit;font-size:13px;border:1px solid var(--pc-line);background:var(--pc-panel);color:var(--pc-text);border-radius:8px;padding:9px 14px;cursor:pointer;text-decoration:none;display:inline-flex;gap:6px}.ap-processes button:hover{border-color:var(--pc-accent)}.ap-processes button:disabled{opacity:.45;cursor:default}.ap-processes .primary{background:var(--pc-accent);border-color:transparent;color:var(--pc-bg);font-weight:650}.ap-processes input,.ap-processes select,.ap-processes textarea{width:100%;border:1px solid var(--pc-line);border-radius:8px;background:var(--pc-bg);color:var(--pc-text);font:inherit;font-size:13px;padding:10px 12px}.ap-processes textarea{resize:vertical}.ap-processes :is(button,a,input,select,textarea):focus-visible{outline:2px solid var(--pc-accent);outline-offset:3px}.ap-processes label{display:block;font-size:12px;font-weight:600;margin-bottom:7px}.ap-processes .field{margin-bottom:19px}.ap-processes .card{background:var(--pc-panel);border:1px solid var(--pc-line);border-radius:12px;padding:22px}.ap-processes .grid{display:grid;grid-template-columns:minmax(0,1.7fr) minmax(250px,1fr);gap:20px}.ap-processes .stats{display:grid;grid-template-columns:repeat(3,1fr);gap:12px;margin:22px 0}.ap-processes .stat{border:1px solid var(--pc-line);border-radius:10px;padding:16px}.ap-processes .stat strong{display:block;font-size:25px}.ap-processes .stat span{font-size:12px;color:var(--pc-muted)}.ap-processes .pill{font-size:11px;padding:3px 9px;border-radius:999px;border:1px solid var(--pc-line);text-transform:capitalize;white-space:nowrap}.ap-processes .pill.active,.ap-processes .pill.completed{color:#62ccaa;background:#62ccaa14;border-color:#62ccaa40}.ap-processes .pill.blocked,.ap-processes .pill.failed,.ap-processes .pill.paused{color:#e3b86d;background:#e3b86d14;border-color:#e3b86d40}.ap-processes .notice{border:1px solid #e3b86d66;background:#e3b86d10;border-radius:9px;padding:12px 15px;margin:15px 0;overflow-wrap:anywhere}.ap-processes .filters{margin-bottom:16px}.ap-processes .filters input{flex:1;min-width:180px}.ap-processes .filters select{width:auto;max-width:240px}.ap-processes table{color:var(--pc-text);border-collapse:collapse;width:100%;text-align:left;font-size:13px}.ap-processes th{color:var(--pc-muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:500;padding:13px 16px;border-bottom:1px solid var(--pc-line)}.ap-processes td{padding:16px;border-bottom:1px solid var(--pc-line);vertical-align:top}.ap-processes tbody tr:last-child td{border-bottom:0}.ap-processes .table-wrap{overflow:auto;border:1px solid var(--pc-line);border-radius:10px}.ap-processes td button{border:0;padding:0;background:none;text-align:left;font-weight:600}.ap-processes .sub{font-size:12px;color:var(--pc-muted);margin-top:4px;max-width:390px}.ap-processes .tabs{display:flex;gap:20px;border-bottom:1px solid var(--pc-line);margin-bottom:23px}.ap-processes .tabs button{background:none;border:0;border-radius:0;padding:10px 0 13px;color:var(--pc-muted)}.ap-processes .tabs button.on{color:var(--pc-accent);border-bottom:2px solid var(--pc-accent)}.ap-processes .prose{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.8}.ap-processes .block+.block{margin-top:26px}.ap-processes .empty{text-align:center;padding:60px 24px;border:1px dashed var(--pc-line);border-radius:12px}.ap-processes .empty p{margin:10px auto 20px;max-width:430px;color:var(--pc-muted)}.ap-processes .crumb{background:none;border:0;padding:0;color:var(--pc-muted);margin-bottom:18px}.ap-processes .small{font-size:12px}.ap-processes .toolbar{position:sticky;bottom:0;background:var(--pc-panel);padding:15px;border:1px solid var(--pc-line);border-radius:10px;margin-top:20px}.ap-processes .run{margin-bottom:12px}.ap-processes a{color:var(--pc-accent)}.ap-processes .run .prose{margin-top:12px}.ap-processes .overlay{position:fixed;inset:0;z-index:100;background:#0008;display:grid;place-items:center;padding:20px}.ap-processes .dialog{width:min(560px,100%);max-height:85vh;overflow:auto}@media(max-width:760px){.ap-processes{padding:18px}.ap-processes .grid{grid-template-columns:1fr}.ap-processes h1{font-size:21px}.ap-processes .stats{gap:7px}.ap-processes .stat{padding:12px}.ap-processes .hide-small{display:none}}
`;
const Pill = ({ state }: { state: string }) => (
  <span className={`pill ${state}`}>{state}</span>
);
export default function ProcessesPanel(props: Props) {
  return <Panel key={`${props.projectId}:${props.installId}`} {...props} />;
}
function Panel(props: Props) {
  const [items, setItems] = useState<Process[]>([]),
    [agents, setAgents] = useState<{ id: number; name: string }[]>([]),
    [selected, setSelected] = useState<string | null>(() =>
      new URLSearchParams(window.location.search).get("process_id"),
    ),
    [detail, setDetail] = useState<{
      process: Process;
      versions: Version[];
    } | null>(null);
  const [tab, setTab] = useState(
      new URLSearchParams(window.location.search).has("version")
        ? "procedure"
        : "overview",
    ),
    [version, setVersion] = useState(
      Number(new URLSearchParams(window.location.search).get("version")) || 0,
    ),
    [editing, setEditing] = useState(false),
    [creating, setCreating] = useState(false),
    [draft, setDraft] = useState<Definition>(empty);
  const [area, setArea] = useState("processes");
  const [historyWarning, setHistoryWarning] = useState("");
  const [runs, setRuns] = useState<Entry[]>([]),
    [more, setMore] = useState(false),
    [search, setSearch] = useState(""),
    [filter, setFilter] = useState(""),
    [owner, setOwner] = useState(0),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [runAssignment, setRunAssignment] = useState<Assignment | null>(null),
    [runParameters, setRunParameters] = useState<Record<string, unknown>>({}),
    [assignmentFilter, setAssignmentFilter] = useState(""),
    [runStateFilter, setRunStateFilter] = useState(""),
    [runOwnerFilter, setRunOwnerFilter] = useState(0);
  const [runModal, setRunModal] = useState(false),
    [runInput, setRunInput] = useState(""),
    [runKey, setRunKey] = useState("");
  const api = async (path = "", method = "GET", body?: unknown) => {
    const [route, searchParams] = path.split("?");
    const q = new URLSearchParams(searchParams);
    if (props.projectId) q.set("project_id", props.projectId);
    if (props.installId) q.set("install_id", String(props.installId));
    const r = await fetch(
      `/api/apps/${encodeURIComponent(props.appName || "processes")}/processes${route}?${q}`,
      {
        method,
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      },
    );
    if (!r.ok)
      throw new Error((await r.text()) || `Request failed (${r.status})`);
    return r.json();
  };
  const load = async () => {
    const r = await api();
    setItems(r.processes || []);
  };
  const loadDetail = async (id: string) => {
    const r = await api(`/${encodeURIComponent(id)}`);
    setDetail(r);
  };
  const loadRuns = async (id: string) => {
    const r = await api(`/${encodeURIComponent(id)}/runs`);
    setRuns(historyEntries(r));
    setHistoryWarning(r.tasks_error || "");
    setMore(!!r.has_more);
  };
  useEffect(() => {
    let live = true;
    setItems([]);
    setDetail(null);
    setRuns([]);
    setLoading(true);
    setError("");
    if (!props.projectId) {
      setLoading(false);
      return;
    }
    Promise.all([
      api(),
      fetch(`/api/agents?project_id=${encodeURIComponent(props.projectId)}`, {
        credentials: "same-origin",
      }).then(async (r) => {
        if (!r.ok) throw new Error("Could not load owner agents");
        return r.json();
      }),
    ])
      .then(([data, owners]) => {
        if (live) {
          setItems(data.processes || []);
          setAgents(owners);
        }
      })
      .catch((e) => live && setError(e.message))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [props.projectId, props.installId]);
  useEffect(() => {
    let live = true;
    setDetail(null);
    setRuns([]);
    if (!selected || !props.projectId) return;
    api(`/${encodeURIComponent(selected)}`)
      .then((r) => live && setDetail(r))
      .catch((e) => live && setError(e.message));
    return () => {
      live = false;
    };
  }, [selected, props.projectId, props.installId]);
  useEffect(() => {
    let live = true;
    if (!selected || !detail) return;
    const refresh = () =>
      Promise.all([
        api(`/${encodeURIComponent(selected)}/runs`),
        api(`/${encodeURIComponent(selected)}`),
      ])
        .then(([r, current]) => {
          if (live) {
            setDetail(current);
            setRuns(historyEntries(r));
            setHistoryWarning(r.tasks_error || "");
            setMore(!!r.has_more);
          }
        })
        .catch((e) => live && setError(e.message));
    refresh();
    const timer = setInterval(() => {
      if (document.visibilityState === "visible") refresh();
    }, 15000);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [selected, detail?.process.version, props.projectId, props.installId]);
  useEffect(() => {
    if (!detail?.process.sync_pending || !selected) return;
    let live = true;
    const timer = setTimeout(
      () =>
        api(`/${selected}`)
          .then((r) => live && setDetail(r))
          .catch((e) => live && setError(e.message)),
      4000,
    );
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [detail, selected]);
  const work = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await fn();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  const open = (p: Process) => {
    setSelected(p.id);
    setTab("overview");
    setVersion(0);
    setEditing(false);
    setCreating(false);
    setError("");
    setNotice("");
  };
  const back = () => {
    setSelected(null);
    setEditing(false);
    setCreating(false);
    setRunModal(false);
    setDetail(null);
    setError("");
    setNotice("");
  };
  const mutate = (action: string) =>
    work(async () => {
      const r = await api(`/${selected}/${action}`, "POST", {});
      setDetail((d) => (d ? { ...d, process: r } : d));
      await load();
      await loadRuns(selected!);
    });
  const p = detail?.process,
    chosen = version
      ? detail?.versions.find((v) => v.version === version)?.definition
      : p;
  const ownerName = (id: number) =>
    agents.find((a) => a.id === id)?.name || `Agent ${id}`;
  const visible = items.filter(
    (p) =>
      (!filter || p.status === filter) &&
      (!owner ||
        (p.assignments || []).some((x) => x.owner_agent_id === owner) ||
        (!p.assignments?.length && p.owner_agent_id === owner)) &&
      `${p.name} ${p.description}`.toLowerCase().includes(search.toLowerCase()),
  );
  const setField = (key: keyof Definition, value: unknown) =>
    setDraft((d) => ({ ...d, [key]: value }));
  const executions = runs.filter((r) => !r.record.schedule_kind);
  const filteredExecutions = executions.filter(
    (r) =>
      (!assignmentFilter || r.assignment_id === assignmentFilter) &&
      (!runStateFilter || r.record.state === runStateFilter) &&
      (!runOwnerFilter ||
        r.assignment?.owner_agent_id === runOwnerFilter ||
        r.record.steps?.some(
          (s) =>
            s.executor.kind === "agent" &&
            s.executor.agent_id === runOwnerFilter,
        )),
  );
  const prepareRun = (x: Assignment) => {
    setRunAssignment(x);
    setRunParameters({});
    setRunInput("");
    setRunKey(crypto.randomUUID());
    setError("");
    setRunModal(true);
  };
  const runSchema =
    detail?.versions.find((v) => v.version === runAssignment?.procedure_version)
      ?.definition.parameters || [];
  const newProcess = () => {
    setDraft({ ...empty, owner_agent_id: agents[0]?.id || 0 });
    setCreating(true);
    setError("");
  };
  return (
    <div className="ap-processes">
      <style>{css}</style>
      {(selected || creating) && (
        <button className="crumb" disabled={busy} onClick={back}>
          ← All processes
        </button>
      )}
      <header className="row between head">
        <div>
          <h1>
            {creating
              ? "New process"
              : editing
                ? "Edit procedure"
                : p?.name || "Processes"}
          </h1>
          <p className="muted">
            {selected || creating
              ? "One procedure. Independent assignments. A record of every run."
              : "Define the work your company does, and let agents follow through."}
          </p>
        </div>
        {!selected && !creating && area === "processes" && (
          <button className="primary" onClick={newProcess}>
            + New process
          </button>
        )}
        {p && !editing && (
          <div className="row">
            <Pill state={p.status} />
            <span className="small muted">Version {p.version}</span>
          </div>
        )}
      </header>
      {!selected && !creating && (
        <nav className="tabs" aria-label="Processes navigation">
          <button
            className={area === "processes" ? "on" : ""}
            onClick={() => setArea("processes")}
          >
            Processes
          </button>
          <button
            className={area === "work" ? "on" : ""}
            onClick={() => setArea("work")}
          >
            Work
          </button>
        </nav>
      )}
      {error && (
        <div role="alert" className="notice">
          {error}
        </div>
      )}
      {notice && (
        <div role="status" className="notice">
          {notice}
        </div>
      )}
      {!props.projectId ? (
        <div className="empty">Select a project to manage its processes.</div>
      ) : loading ? (
        <p className="muted">Loading processes…</p>
      ) : !selected && !creating && area === "work" ? (
        <WorkPanel
          key={`${props.projectId}:${props.installId}`}
          api={api}
          agents={agents}
          processes={items}
          eventRevision={props.eventRevision}
        />
      ) : creating || editing ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            work(async () => {
              const r = await api(
                creating ? "" : `/${selected}`,
                creating ? "POST" : "PUT",
                {
                  definition: draft,
                  ...(!creating ? { expected_version: p?.version } : {}),
                },
              );
              setCreating(false);
              setEditing(false);
              setSelected(r.id);
              setVersion(0);
              setTab(creating ? "assignments" : "procedure");
              await load();
              await loadDetail(r.id);
            });
          }}
        >
          <div className="grid">
            <section className="card">
              <h2>Procedure</h2>
              <div className="field">
                <label htmlFor="pc-name">Process name</label>
                <input
                  id="pc-name"
                  required
                  maxLength={160}
                  value={draft.name}
                  onChange={(e) => setField("name", e.target.value)}
                  placeholder="Monthly financial close"
                />
              </div>
              <div className="field">
                <label htmlFor="pc-purpose">Purpose</label>
                <textarea
                  id="pc-purpose"
                  rows={3}
                  value={draft.description}
                  onChange={(e) => setField("description", e.target.value)}
                  placeholder="What does this process achieve?"
                />
              </div>
              {fields
                .filter(([key]) =>
                  ["instructions", "completion_criteria"].includes(key),
                )
                .map(([key, label]) => (
                  <div className="field" key={key}>
                    <label htmlFor={`pc-${key}`}>{label}</label>
                    <textarea
                      id={`pc-${key}`}
                      required
                      rows={key === "instructions" ? 10 : 4}
                      value={draft[key]}
                      onChange={(e) => setField(key, e.target.value)}
                      placeholder={
                        key === "instructions"
                          ? "1. Collect the required records.\n2. Review and investigate discrepancies.\n3. Prepare the result and request approval."
                          : "What must be true, and what evidence should the agent provide?"
                      }
                    />
                  </div>
                ))}
              <StepEditor
                steps={draft.steps || []}
                onChange={(steps) => setField("steps", steps)}
              />
              <ParameterEditor
                fields={draft.parameters || []}
                onChange={(v) => setField("parameters", v)}
              />
            </section>
            <aside>
              {creating && (
                <section className="card">
                  <h2>First assignment</h2>
                  <p className="small muted">
                    A default assignment will be created. Add more agents,
                    pages, and schedules after saving.
                  </p>
                  <div className="field">
                    <label htmlFor="pc-mode">Execution</label>
                    <select
                      id="pc-mode"
                      value={draft.execution_mode}
                      onChange={(e) =>
                        setField(
                          "execution_mode",
                          e.target.value as "agent" | "tasks",
                        )
                      }
                    >
                      <option value="agent">Direct agent</option>
                      <option value="tasks">Tasks</option>
                    </select>
                    <p className="small muted">
                      {draft.execution_mode === "agent"
                        ? "Runs and results are tracked here. No Tasks app needed."
                        : "Requires Tasks 3.6.0 or later connected to Processes."}
                    </p>
                  </div>
                  <div className="field">
                    <label htmlFor="pc-owner">Responsible agent</label>
                    <select
                      id="pc-owner"
                      required
                      value={draft.owner_agent_id || ""}
                      onChange={(e) =>
                        setField("owner_agent_id", Number(e.target.value))
                      }
                    >
                      <option value="" disabled>
                        Choose an agent
                      </option>
                      {agents.map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.name}
                        </option>
                      ))}
                    </select>
                    {!agents.length && (
                      <p className="small muted">
                        Create an agent in this project before saving a process.
                      </p>
                    )}
                  </div>
                  <div className="field">
                    <label htmlFor="pc-cadence">Cadence</label>
                    <select
                      id="pc-cadence"
                      value={draft.schedule?.kind || "manual"}
                      onChange={(e) =>
                        setField(
                          "schedule",
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
                                  cron: "0 9 * * 1",
                                  timezone:
                                    Intl.DateTimeFormat().resolvedOptions()
                                      .timeZone,
                                },
                        )
                      }
                    >
                      <option value="manual">On demand</option>
                      <option value="interval">Every interval</option>
                      <option value="cron">Calendar schedule</option>
                    </select>
                  </div>
                  {draft.schedule?.kind === "interval" && (
                    <div className="field">
                      <label htmlFor="pc-interval">Interval</label>
                      <input
                        id="pc-interval"
                        required
                        value={draft.schedule.every}
                        onChange={(e) =>
                          setField("schedule", {
                            ...draft.schedule,
                            every: e.target.value,
                          })
                        }
                      />
                      <p className="small muted">
                        Examples: 1h, 24h, 168h. For fixed local times, use a
                        calendar schedule.
                      </p>
                    </div>
                  )}
                  {draft.schedule?.kind === "cron" && (
                    <>
                      <div className="field">
                        <label htmlFor="pc-cron">Calendar expression</label>
                        <input
                          id="pc-cron"
                          required
                          value={draft.schedule.cron}
                          onChange={(e) =>
                            setField("schedule", {
                              ...draft.schedule,
                              cron: e.target.value,
                            })
                          }
                        />
                        <p className="small muted">
                          “0 9 * * 1” means Mondays at 09:00.
                        </p>
                      </div>
                      <div className="field">
                        <label htmlFor="pc-timezone">Timezone</label>
                        <input
                          id="pc-timezone"
                          required
                          value={draft.schedule.timezone}
                          onChange={(e) =>
                            setField("schedule", {
                              ...draft.schedule,
                              timezone: e.target.value,
                            })
                          }
                        />
                      </div>
                    </>
                  )}
                </section>
              )}
              <section className="card" style={{ marginTop: 20 }}>
                <h2>Inputs & approvals</h2>
                {fields
                  .filter(
                    ([key]) =>
                      !["instructions", "completion_criteria"].includes(key),
                  )
                  .map(([key, label]) => (
                    <div className="field" key={key}>
                      <label htmlFor={`pc-${key}`}>{label}</label>
                      <textarea
                        id={`pc-${key}`}
                        rows={3}
                        value={draft[key]}
                        onChange={(e) => setField(key, e.target.value)}
                      />
                    </div>
                  ))}
                <p className="small muted">
                  Agents must obtain the approvals described here before
                  proceeding.
                </p>
              </section>
            </aside>
          </div>
          <div className="row between toolbar">
            <span className="small muted">
              {creating
                ? "Saved as a draft. Activate when ready."
                : "Saving creates a new draft version. Existing runs keep their procedure."}
            </span>
            <div className="row">
              <button
                type="button"
                disabled={busy}
                onClick={() => (creating ? back() : setEditing(false))}
              >
                Cancel
              </button>
              <button className="primary" disabled={busy || !agents.length}>
                {busy ? "Saving…" : "Save draft"}
              </button>
            </div>
          </div>
        </form>
      ) : !selected ? (
        <>
          <div className="stats">
            {[
              [
                items.filter((p) => p.status === "active").length,
                "Active processes",
              ],
              [
                items.reduce(
                  (n, p) =>
                    n +
                    (p.assignments || []).filter(
                      (x) =>
                        x.status === "active" &&
                        p.status === "active" &&
                        x.schedule,
                    ).length,
                  0,
                ),
                "Recurring assignments",
              ],
              [
                items.filter((p) => p.sync_pending).length,
                "Need synchronization",
              ],
            ].map(([value, label]) => (
              <div className="stat" key={label}>
                <strong>{value}</strong>
                <span>{label}</span>
              </div>
            ))}
          </div>
          <div className="row filters">
            <input
              aria-label="Search processes"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search company processes…"
            />
            <select
              aria-label="Filter status"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            >
              <option value="">All statuses</option>
              {["active", "draft", "paused", "archived"].map((s) => (
                <option key={s}>{s}</option>
              ))}
            </select>
            <select
              aria-label="Filter owner"
              value={owner}
              onChange={(e) => setOwner(Number(e.target.value))}
            >
              <option value={0}>All owners</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
            <button disabled={busy} onClick={() => work(load)}>
              Refresh
            </button>
          </div>
          {visible.length ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Process</th>
                    <th>Agents</th>
                    <th>Assignments</th>
                    <th>Status</th>
                    <th className="hide-small">Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.map((p) => (
                    <tr key={p.id}>
                      <td>
                        <button onClick={() => open(p)}>{p.name}</button>
                        <div className="sub">{p.description}</div>
                      </td>
                      <td>
                        {Array.from(
                          new Set(
                            (p.assignments || []).map((x) =>
                              ownerName(x.owner_agent_id),
                            ),
                          ),
                        ).join(", ") || ownerName(p.owner_agent_id)}
                      </td>
                      <td>{p.assignments?.length || 1}</td>
                      <td>
                        <Pill state={p.status} />
                        {p.sync_pending && (
                          <div className="sub">Sync pending</div>
                        )}
                      </td>
                      <td className="hide-small muted">{date(p.updated_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="empty">
              <h2>
                {items.length
                  ? "No matching processes"
                  : "Build your company’s operating playbook"}
              </h2>
              <p>
                {items.length
                  ? "Try a different search or filter."
                  : "Start with a weekly sales review, monthly close, or customer follow-up routine."}
              </p>
              {!items.length && (
                <button className="primary" onClick={newProcess}>
                  Create your first process
                </button>
              )}
            </div>
          )}
        </>
      ) : !p ? (
        <p className="muted">Loading procedure…</p>
      ) : (
        <>
          {p.sync_pending && (
            <div className="notice" role="status">
              <strong>Synchronization pending.</strong>{" "}
              {p.sync_error || "Applying the requested schedule change."}{" "}
              Automatic retries run every 30 seconds.{" "}
              <button
                disabled={busy}
                onClick={() =>
                  mutate(
                    p.status === "active"
                      ? "activate"
                      : p.status === "archived"
                        ? "archive"
                        : "pause",
                  )
                }
              >
                Retry now
              </button>
            </div>
          )}
          <nav className="tabs" aria-label="Process detail">
            {["overview", "procedure", "assignments", "runs"].map((t) => (
              <button
                className={tab === t ? "on" : ""}
                key={t}
                onClick={() => setTab(t)}
              >
                {t[0].toUpperCase() + t.slice(1)}
              </button>
            ))}
          </nav>
          {tab === "overview" ? (
            <div className="grid">
              <section className="card">
                <div className="block">
                  <h2>Purpose</h2>
                  <p className="prose">{p.description || p.name}</p>
                </div>
                <div className="block">
                  <h2>Successful completion</h2>
                  <div className="prose">{p.completion_criteria}</div>
                </div>
                <div className="block">
                  <h2>Latest execution</h2>
                  {executions[0] ? (
                    <>
                      <div className="row">
                        <Pill state={executions[0].record.state} />
                        <span className="muted small">
                          {date(executions[0].record.created_at)}
                        </span>
                      </div>
                      <p className="prose">
                        {executions[0].record.result ||
                          executions[0].record.error ||
                          executions[0].record.current_step ||
                          "Waiting for the owner to begin."}
                      </p>
                      <button
                        style={{ marginTop: 12 }}
                        onClick={() => setTab("runs")}
                      >
                        View runs →
                      </button>
                    </>
                  ) : (
                    <p className="muted">No executions yet.</p>
                  )}
                </div>
              </section>
              <aside className="card">
                <h2>Operations</h2>
                <p>
                  {
                    (p.assignments || []).filter((x) => x.status === "active")
                      .length
                  }{" "}
                  enabled assignments · {(p.assignments || []).length} total
                </p>
                <p className="small muted">
                  Pause this process to stop future runs for all its
                  assignments. Use Assignments to control one page or agent.
                </p>
                <button onClick={() => setTab("assignments")}>
                  Manage assignments
                </button>
                <p className="small muted">
                  Recent runs needing attention:{" "}
                  {
                    executions.filter(
                      (r) =>
                        ["blocked", "failed", "waiting"].includes(
                          r.record.state,
                        ) || r.record.delivery_warning,
                    ).length
                  }
                </p>
                <div className="row" style={{ marginTop: 25 }}>
                  {p.status === "active" ? (
                    <>
                      <button
                        className="primary"
                        disabled={busy || p.sync_pending}
                        onClick={() => {
                          const active = (p.assignments || []).filter(
                            (x) => x.status === "active" && !x.sync_pending,
                          );
                          if (active.length === 1) prepareRun(active[0]);
                          else setTab("assignments");
                        }}
                      >
                        Run now
                      </button>
                      <button disabled={busy} onClick={() => mutate("pause")}>
                        Pause
                      </button>
                    </>
                  ) : (
                    p.status !== "archived" && (
                      <button
                        className="primary"
                        disabled={busy}
                        onClick={() => mutate("activate")}
                      >
                        Activate
                      </button>
                    )
                  )}
                  {p.status !== "archived" && (
                    <button
                      disabled={busy || p.status === "active" || p.sync_pending}
                      onClick={() => {
                        setDraft({ ...p });
                        setEditing(true);
                      }}
                    >
                      Edit procedure
                    </button>
                  )}
                </div>
                {p.status === "active" && (
                  <p className="small muted" style={{ marginTop: 14 }}>
                    Pause the process before editing its procedure.
                  </p>
                )}
                {p.status !== "archived" && (
                  <button
                    style={{ marginTop: 25 }}
                    disabled={busy}
                    onClick={() => {
                      if (
                        window.confirm(
                          "Archive this process? Future scheduled runs will stop. Existing runs will continue.",
                        )
                      )
                        mutate("archive");
                    }}
                  >
                    Archive process
                  </button>
                )}
              </aside>
            </div>
          ) : tab === "assignments" ? (
            <Assignments
              items={(p.assignments || []).map((x) => ({
                ...x,
                next_run_at:
                  x.next_run_at ||
                  runs.find(
                    (r) =>
                      r.assignment_id === x.id && r.record.schedule_enabled,
                  )?.record.next_run_at,
              }))}
              versions={detail.versions}
              agents={agents}
              processStatus={p.status}
              api={(path, method, body) => api(`/${p.id}${path}`, method, body)}
              onChanged={async () => {
                await loadDetail(p.id);
                await loadRuns(p.id);
                await load();
              }}
              onRun={prepareRun}
            />
          ) : tab === "procedure" ? (
            <section className="card">
              <div className="row between head">
                <h2 style={{ margin: 0 }}>Procedure</h2>
                <select
                  aria-label="Procedure version"
                  style={{ width: "auto" }}
                  value={version || p.version}
                  onChange={(e) => setVersion(Number(e.target.value))}
                >
                  {detail.versions.map((v) => (
                    <option key={v.version} value={v.version}>
                      Version {v.version}
                      {v.version === p.version ? " · current" : ""}
                    </option>
                  ))}
                </select>
              </div>
              {version > 0 && version !== p.version && (
                <div className="notice">
                  Historical procedure. Current settings use version {p.version}
                  .
                </div>
              )}
              {!!chosen?.steps?.length && (
                <div className="block">
                  <h2>Steps & roles</h2>
                  {chosen.steps.map((s) => (
                    <div className="block" key={s.key}>
                      <strong>{s.name}</strong>
                      <p className="small muted">
                        {s.role} · {s.kind}
                        {(s.depends_on || []).length
                          ? ` · after ${(s.depends_on || []).join(", ")}`
                          : " · starts with run"}
                      </p>
                      <div className="prose">{s.instructions}</div>
                      <p className="small muted">
                        Expected output: {s.expected_output}
                      </p>
                    </div>
                  ))}
                </div>
              )}
              {!!chosen?.parameters?.length && (
                <div className="block">
                  <h2>Parameters</h2>
                  {chosen.parameters.map((f) => (
                    <p key={f.key}>
                      <strong>{f.label || f.key}</strong> · {f.type}
                      {f.required ? " · required" : ""}
                      {f.default !== undefined
                        ? ` · default: ${String(f.default)}`
                        : ""}
                    </p>
                  ))}
                </div>
              )}
              {chosen ? (
                fields.map(([key, label]) => (
                  <div className="block" key={key}>
                    <h2>{label}</h2>
                    <div className="prose">
                      {chosen[key] || "None specified."}
                    </div>
                  </div>
                ))
              ) : (
                <p role="alert">This procedure version is unavailable.</p>
              )}
            </section>
          ) : (
            <>
              <div className="row between head">
                <p className="muted">
                  Progress, approvals, and results from single-agent and team
                  runs.
                </p>
                <button
                  disabled={busy}
                  onClick={() => work(() => loadRuns(p.id))}
                >
                  Refresh history
                </button>
              </div>
              <div className="row filters">
                <select
                  aria-label="Filter run assignment"
                  value={assignmentFilter}
                  onChange={(e) => setAssignmentFilter(e.target.value)}
                >
                  <option value="">All assignments</option>
                  {(p.assignments || []).map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.name}
                    </option>
                  ))}
                </select>
                <select
                  aria-label="Filter run agent"
                  value={runOwnerFilter}
                  onChange={(e) => setRunOwnerFilter(Number(e.target.value))}
                >
                  <option value="0">All agents</option>
                  {agents.map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.name}
                    </option>
                  ))}
                </select>
                <select
                  aria-label="Filter run state"
                  value={runStateFilter}
                  onChange={(e) => setRunStateFilter(e.target.value)}
                >
                  <option value="">All outcomes</option>
                  {[
                    "queued",
                    "running",
                    "waiting",
                    "blocked",
                    "completed",
                    "failed",
                    "cancelled",
                  ].map((x) => (
                    <option key={x}>{x}</option>
                  ))}
                </select>
              </div>
              {historyWarning && (
                <div className="notice" role="status">
                  Tasks history unavailable: {historyWarning}
                </div>
              )}
              {more && (
                <div className="notice">
                  Showing the 200 most recent task records. Older history
                  remains in Tasks.
                </div>
              )}
              {filteredExecutions.length ? (
                filteredExecutions.map((r) => (
                  <article className="card run" key={r.record.id}>
                    <div className="row between">
                      <div className="row">
                        <Pill state={r.record.state} />
                        <strong>{r.record.title}</strong>
                      </div>
                      {r.backend === "tasks" && (
                        <a
                          href={`/apps/tasks/page?${new URLSearchParams({ project_id: props.projectId!, task_id: r.record.id })}`}
                        >
                          Open task ↗
                        </a>
                      )}
                    </div>
                    {r.assignment && (
                      <p className="small muted">
                        {r.assignment.name} · {r.assignment.target || ""} ·{" "}
                        {ownerName(r.assignment.owner_agent_id || 0)}
                      </p>
                    )}
                    <p className="muted small">
                      {r.record.trigger_event_id && <span>App event · </span>}
                      {date(r.record.created_at)} ·{" "}
                      <button
                        style={{
                          padding: 0,
                          border: 0,
                          background: "none",
                          color: "var(--pc-accent)",
                        }}
                        onClick={() => {
                          setVersion(r.version);
                          setTab("procedure");
                        }}
                      >
                        Procedure v{r.version}
                      </button>{" "}
                      ·{" "}
                      {r.record.parent_task_id || r.record.scheduled_for
                        ? "Scheduled"
                        : "Manual"}
                    </p>
                    {r.record.progress !== undefined && (
                      <progress
                        style={{
                          width: "100%",
                          accentColor: "var(--pc-accent)",
                          height: 5,
                        }}
                        max={100}
                        value={r.record.progress}
                      />
                    )}
                    <div className="prose">
                      {r.record.delivery_warning && (
                        <p className="notice">
                          Delivery retry pending: {r.record.delivery_warning}
                        </p>
                      )}
                      {r.record.result ||
                        r.record.error ||
                        r.record.current_step ||
                        "Queued for the owner agent."}
                    </div>
                    {(r.backend === "agent" || r.record.workflow) && (
                      <RunWork
                        runID={r.record.id}
                        runState={r.record.state}
                        api={api}
                        agents={agents}
                        onChanged={() => loadRuns(p.id)}
                      />
                    )}
                    {r.record.workflow && (
                      <RunSteps
                        steps={r.record.steps || []}
                        runID={r.record.id}
                        runState={r.record.state}
                        agents={agents}
                        projectId={props.projectId!}
                        api={(path, method, body) =>
                          api(`/${p.id}${path}`, method, body)
                        }
                        onChanged={() => loadRuns(p.id)}
                      />
                    )}
                  </article>
                ))
              ) : (
                <div className="empty">
                  <h2>No runs yet</h2>
                  <p>Executions will appear here when this process starts.</p>
                </div>
              )}
            </>
          )}
        </>
      )}
      {runModal && p && runAssignment && (
        <div className="overlay">
          <section
            role="dialog"
            aria-modal="true"
            aria-labelledby="pc-run-title"
            className="card dialog"
          >
            <h2 id="pc-run-title">Run {runAssignment.name}</h2>
            <p className="muted">
              {detail?.versions.find(
                (v) => v.version === runAssignment.procedure_version,
              )?.definition.steps?.length ? (
                <>
                  This run follows procedure version{" "}
                  {runAssignment.procedure_version} with its assigned roles.{" "}
                  {ownerName(runAssignment.owner_agent_id)} coordinates the run.
                  Progress and approvals appear here in Processes.
                </>
              ) : (
                <>
                  {ownerName(runAssignment.owner_agent_id)} receives procedure
                  version {runAssignment.procedure_version}, tracked{" "}
                  {runAssignment.execution_mode === "agent"
                    ? "here in Processes"
                    : "in Tasks"}
                  .
                </>
              )}
            </p>
            <ParameterValues
              fields={runSchema}
              values={{ ...runAssignment.parameters, ...runParameters }}
              prefix="run-parameter"
              onChange={setRunParameters}
            />
            <p className="small muted">
              Parameter changes here apply only to this run.
            </p>
            <div className="field" style={{ marginTop: 20 }}>
              <label htmlFor="pc-run-input">Run-specific context</label>
              <textarea
                autoFocus
                id="pc-run-input"
                rows={5}
                value={runInput}
                onChange={(e) => setRunInput(e.target.value)}
                placeholder="For example: review September’s records."
              />
            </div>
            {error && (
              <div className="notice" role="alert">
                {error}
              </div>
            )}
            <div className="row" style={{ justifyContent: "flex-end" }}>
              <button disabled={busy} onClick={() => setRunModal(false)}>
                Cancel
              </button>
              <button
                className="primary"
                disabled={busy}
                onClick={() =>
                  work(async () => {
                    const r = await api(`/${p.id}/start`, "POST", {
                      assignment_id: runAssignment.id,
                      parameters: runParameters,
                      idempotency_key: runKey,
                      inputs: runInput,
                    });
                    setRunModal(false);
                    setNotice(
                      r.delivery_warning
                        ? `Run created; delivery needs attention: ${r.delivery_warning}`
                        : "Run created and sent to the owner.",
                    );
                    setTab("runs");
                    await loadRuns(p.id);
                  })
                }
              >
                {busy ? "Starting…" : "Start run"}
              </button>
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
