import { useEffect, useMemo, useState } from "react";
import { ProcessFlow } from "./ProcessFlow";
import type { Step, StepRun } from "./Workflow";

type Assignment = {
  id: string;
  name: string;
  target?: string;
  owner_agent_id?: number;
  status: string;
  schedule?: { kind: string; every?: string; cron?: string; timezone?: string };
};
type Process = {
  id: string;
  name: string;
  description?: string;
  status: string;
  version: number;
  steps?: Step[];
  assignments?: Assignment[];
};
type Run = {
  id: string;
  process_id?: string;
  state: string;
  workflow?: boolean;
  steps?: StepRun[];
  assignment_id?: string;
  assignment?: Assignment;
  created_at?: string;
  backend?: string;
};
type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  agents?: { id: number; name: string }[];
};

const terminal = (s: string) =>
  ["completed", "failed", "cancelled"].includes(s);
function url(props: Props, path: string) {
  const q = new URLSearchParams();
  if (props.projectId) q.set("project_id", props.projectId);
  if (props.installId) q.set("install_id", String(props.installId));
  return `/api/apps/${encodeURIComponent(props.appName || "processes")}/processes${path}?${q}`;
}
function MapBoundary({
  process,
  runs,
  agents,
}: {
  process: Process;
  runs: Run[];
  agents: Props["agents"];
}) {
  const live = runs.filter((r) => !terminal(r.state) && r.steps?.length);
  return (
    <section className="pm-boundary" aria-label={`${process.name} SOP`}>
      <header className="pm-boundary-head">
        <div>
          <h2>{process.name}</h2>
          <p className="small muted">
            SOP v{process.version} · {process.assignments?.length || 0}{" "}
            assignment{process.assignments?.length === 1 ? "" : "s"} ·{" "}
            {process.steps?.length || 0} steps
          </p>
        </div>
        <span className={`pill ${process.status}`}>{process.status}</span>
      </header>
      <div className="pm-flow">
        <ProcessFlow
          steps={process.steps || []}
          runExecutions={live.map((r) => r.steps!)}
          agents={agents}
        />
      </div>
      <div className="pm-run-legend">
        <span className="small muted">Live executions</span>
        {live.length ? (
          live.map((r, i) => (
            <span className="pm-run" key={r.id} data-run={r.id}>
              <span className={`pill ${r.state}`}>{r.state}</span>{" "}
              {r.assignment?.name || r.assignment_id || "Manual"} · run{" "}
              {r.id.slice(-8)}
            </span>
          ))
        ) : (
          <span className="small muted">None running</span>
        )}
      </div>
    </section>
  );
}
export default function ProjectMap(props: Props) {
  const [processes, setProcesses] = useState<Process[]>([]),
    [runs, setRuns] = useState<Record<string, Run[]>>({}),
    [search, setSearch] = useState(""),
    [status, setStatus] = useState(""),
    [liveOnly, setLiveOnly] = useState(false),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const load = async () => {
    if (!props.projectId) return;
    setLoading(true);
    try {
      const list = await fetch(url(props, ""), {
        credentials: "same-origin",
      }).then((r) => r.json());
      const ps: Process[] = list.processes || [];
      const entries = await Promise.all(
        ps.map(async (p) => {
          const d = await fetch(
            url(props, `/${encodeURIComponent(p.id)}/runs`),
            { credentials: "same-origin" },
          ).then((r) => r.json());
          const rs: Run[] = [
            ...(d.direct_runs || []),
            ...(d.runs || []).map((x: any) => ({
              ...x.task,
              process_id: p.id,
              backend: "tasks",
              assignment_id: x.assignment_id,
              assignment: x.assignment,
            })),
          ];
          return [p.id, rs] as const;
        }),
      );
      setProcesses(ps);
      setRuns(Object.fromEntries(entries));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, [props.projectId, props.installId, props.eventRevision]);
  const visible = useMemo(
    () =>
      processes.filter(
        (p) =>
          (!search ||
            `${p.name} ${p.description || ""}`
              .toLowerCase()
              .includes(search.toLowerCase())) &&
          (!status || p.status === status) &&
          (!liveOnly || (runs[p.id] || []).some((r) => !terminal(r.state))),
      ),
    [processes, runs, search, status, liveOnly],
  );
  return (
    <section className="pm" aria-label="Project SOP map">
      <style>{`.pm{height:100%;overflow:auto}.pm-toolbar{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin-bottom:18px}.pm-toolbar input{min-width:220px;flex:1}.pm-toolbar label{display:flex;gap:6px;align-items:center;font-size:12px}.pm-toolbar button{font:inherit}.pm-summary{font-size:12px;color:var(--pc-muted);margin-bottom:14px}.pm-boundary{border:2px solid var(--pc-line);border-radius:14px;margin:0 0 22px;overflow:hidden;background:color-mix(in srgb,var(--pc-panel) 82%,transparent)}.pm-boundary-head{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:16px 18px;border-bottom:1px solid var(--pc-line)}.pm-boundary-head h2{margin:0;font-size:17px}.pm-boundary-head p{margin:4px 0 0}.pm-flow{height:460px;min-height:300px}.pm-flow .react-flow{height:100%;background:var(--pc-bg)}.pm-run-legend{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding:10px 16px;border-top:1px solid var(--pc-line)}.pm-run{font-size:11px}.pm-empty{padding:50px 20px;text-align:center;border:1px dashed var(--pc-line);border-radius:12px}@media(max-width:760px){.pm-boundary-head{padding:12px}.pm-flow{height:410px}.pm-toolbar input{min-width:160px}}`}</style>
      <div className="pm-toolbar">
        <input
          aria-label="Search SOPs"
          placeholder="Search SOPs…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <select
          aria-label="Filter SOP status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
        >
          <option value="">All statuses</option>
          {["draft", "active", "paused", "archived"].map((x) => (
            <option key={x}>{x}</option>
          ))}
        </select>
        <label>
          <input
            type="checkbox"
            checked={liveOnly}
            onChange={(e) => setLiveOnly(e.target.checked)}
          />{" "}
          Live only
        </label>
        <button onClick={() => void load()}>Refresh map</button>
      </div>
      {error && (
        <p role="alert" className="notice">
          {error}
        </p>
      )}
      {loading ? (
        <p className="muted">Loading SOP map…</p>
      ) : (
        <>
          <p className="pm-summary">
            {visible.length} of {processes.length} SOPs ·{" "}
            {
              Object.values(runs)
                .flat()
                .filter((r) => !terminal(r.state)).length
            }{" "}
            live executions
          </p>
          {visible.map((p) => (
            <MapBoundary
              key={p.id}
              process={p}
              runs={runs[p.id] || []}
              agents={props.agents}
            />
          ))}
          {!visible.length && (
            <div className="pm-empty">No SOPs match these filters.</div>
          )}
        </>
      )}
    </section>
  );
}
