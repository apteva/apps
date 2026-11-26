import { useEffect, useState } from "react";
import { assignmentAgentIDs, type Assignment } from "./Assignments";
import type { LiveSnapshot } from "./live-events";
import { useScopedRevision } from "./live-events";
import { SourceIcon, type ToolSource } from "./ExecutionTools";
import { Clock } from "lucide-react";
import { eventTitle, scheduleTitle } from "./start-labels";
export { eventTitle, scheduleTitle };

export type ProjectAssignment = Assignment & {
  process_name: string;
  process_status: string;
  process_version: number;
  run_count: number;
  ongoing_count: number;
  last_run_id?: string;
  last_run_state?: string;
  last_run_at?: string;
  triggers?: AssignmentTrigger[];
};

export type AssignmentTrigger = {
  id: string;
  name: string;
  topic: string;
  source_install_id: number;
  status: string;
  subscription_enabled: boolean;
  sync_error?: string;
};

const liveTriggers = (x: ProjectAssignment) => (x.triggers || []).filter((t) => t.status !== "archived");
const startsAutomatically = (x: ProjectAssignment) =>
  !!x.schedule || liveTriggers(x).some((t) => t.status === "active" && t.subscription_enabled);

type Props = {
  api: (path: string) => Promise<any>;
  live: LiveSnapshot;
  ownerName: (id: number) => string;
  cadence: (schedule?: Assignment["schedule"]) => string;
  date: (s?: string) => string;
  onRuns: (assignment: ProjectAssignment) => void;
  onRun: (assignment: ProjectAssignment, runID: string) => void;
  onProcess: (assignment: ProjectAssignment) => void;
  sources?: ToolSource[];
};

// Every assignment in the project, with its run activity. Assignment and run
// events from the app bus refresh the list in place.
export default function ProjectAssignments({ api, live, ownerName, cadence, date, onRuns, onRun, onProcess, sources = [] }: Props) {
  const [items, setItems] = useState<ProjectAssignment[] | null>(null);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("active");
  const revision = useScopedRevision((e) => /^(process|assignment|run)\./.test(e.topic), live);
  useEffect(() => {
    let alive = true;
    api("/assignments")
      .then((r) => { if (alive) { setItems(r.assignments || []); setError(""); } })
      .catch((e) => alive && setError(e.message));
    return () => { alive = false; };
  }, [revision]);
  if (error && !items) return <div role="alert" className="notice">{error}</div>;
  if (!items) return <p className="muted">Loading assignments…</p>;
  const needle = search.trim().toLowerCase();
  const visible = items.filter((x) =>
    (!status || (status === "ongoing" ? x.ongoing_count > 0 : status === "active" ? x.status === "active" || x.ongoing_count > 0 : x.status === status)) &&
    (!needle || [x.name, x.process_name, ownerName(x.owner_agent_id)].some((v) => v?.toLowerCase().includes(needle)) || assignmentAgentIDs(x).some((id) => ownerName(id).toLowerCase().includes(needle))),
  );
  const active = items.filter((x) => x.status === "active").length;
  const automatic = items.filter((x) => x.status === "active" && startsAutomatically(x)).length;
  const ongoing = items.reduce((n, x) => n + x.ongoing_count, 0);
  const count = (v: string) => items.filter((x) => x.status === v).length;
  return (
    <>
      <div className="stats">
        <div className="stat"><strong>{active}</strong><span>Active assignments</span></div>
        <div className="stat"><strong>{automatic}</strong><span>Start automatically</span></div>
        <div className="stat"><strong>{ongoing}</strong><span>Runs in progress</span></div>
      </div>
      <div className="row filters">
        <input aria-label="Search assignments" placeholder="Search assignments, processes or agents…" value={search} onChange={(e) => setSearch(e.target.value)} />
        <select aria-label="Filter assignment status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="active">Active ({active})</option>
          <option value="ongoing">With runs in progress</option>
          <option value="paused">Paused ({count("paused")})</option>
          <option value="archived">Archived ({count("archived")})</option>
          <option value="">All assignments ({items.length})</option>
        </select>
      </div>
      {error && <div role="alert" className="notice">{error}</div>}
      {visible.length ? (
        <div className="table-wrap">
          <table aria-label="Project assignments">
            <thead>
              <tr>
                <th>Assignment</th>
                <th>Agent</th>
                <th className="hide-small">Starts</th>
                <th>Runs</th>
                <th>Last run</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((x) => (
                <tr key={x.id} className="process-item" onClick={() => onRuns(x)}>
                  <td>
                    <span className="assignment-title">
                      <button onClick={(e) => { e.stopPropagation(); onRuns(x); }}>{x.name || "Untitled assignment"}</button>
                      <span className={`pill ${x.status}`}>{x.status}</span>
                    </span>
                    <div className="sub assignment-sop">
                      <button className="link" title={x.process_name} onClick={(e) => { e.stopPropagation(); onProcess(x); }}>{x.process_name || "Process"}</button>
                      <span className="version-tag">v{x.follow_latest ? x.process_version : x.procedure_version}</span>
                      <span className="assignment-sop-meta">
                        {x.follow_latest ? " latest" : x.procedure_version < x.process_version ? ` pinned (latest v${x.process_version})` : " pinned"}
                        {x.process_status !== "active" && <> · process {x.process_status}</>}
                      </span>
                    </div>
                    {x.sync_error && <div className="sub">{x.sync_error}</div>}
                  </td>
                  <td>
                    {ownerName(x.owner_agent_id)}
                    {assignmentAgentIDs(x).length > 1 && (
                      <div className="sub">+ {assignmentAgentIDs(x).slice(1).map(ownerName).join(", ")}</div>
                    )}
                  </td>
                  <td className="hide-small starts">
                    {x.schedule && (
                      <div className="start-line start-event" title={x.schedule.cron ? `${x.schedule.cron} · ${x.schedule.timezone || "UTC"}` : cadence(x.schedule)}>
                        <span className="tool-icon fallback" aria-hidden="true"><Clock size={16} /></span>
                        <div className="start-copy">
                          <div className="start-head"><span className="start-app">Schedule</span><span className="start-event-title">{scheduleTitle(x.schedule)}</span></div>
                          <div className="sub">
                            {x.status === "active" && x.next_run_at ? `Next ${date(x.next_run_at)}` : "Not scheduled while inactive"}
                            {x.schedule.timezone ? ` · ${x.schedule.timezone}` : ""}
                          </div>
                        </div>
                      </div>
                    )}
                    {liveTriggers(x).map((t) => {
                      const issue = t.sync_error ? "Sync error" : t.status !== "active" ? t.status : !t.subscription_enabled ? "Not listening" : "";
                      const source = sources.find((s) => s.key === `app:${t.source_install_id}`);
                      const app = source?.label || `App #${t.source_install_id}`;
                      return (
                        <div className="start-line start-event" key={t.id} title={[`${app} · ${t.topic}`, t.name, t.sync_error].filter(Boolean).join("\n")}>
                          <SourceIcon source={source} />
                          <div className="start-copy">
                            <div className="start-head"><span className="start-app">{app}</span><span className="start-event-title">{eventTitle(t.topic)}</span></div>
                            {t.name && <div className="sub">{t.name}</div>}
                            {issue && <div className="sub start-issue">{issue}</div>}
                          </div>
                        </div>
                      );
                    })}
                    {!x.schedule && !liveTriggers(x).length && <span className="muted">Manual only</span>}
                  </td>
                  <td>
                    {x.run_count}
                    {x.ongoing_count > 0 && <div className="sub"><span className="run-live">{x.ongoing_count} in progress</span></div>}
                  </td>
                  <td>
                    {x.last_run_id ? (
                      <button className="last-run" onClick={(e) => { e.stopPropagation(); onRun(x, x.last_run_id!); }}>
                        <span className={`pill ${x.last_run_state}`}>{x.last_run_state}</span>
                        <span className="sub">{date(x.last_run_at)}</span>
                      </button>
                    ) : <span className="muted">Never run</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="empty">
          <h2>{!items.length ? "No assignments yet" : status === "active" && !needle ? "No active assignments" : "No matching assignments"}</h2>
          <p>{!items.length ? "Assign a process to an agent from the process page to see it here." : status === "active" && !needle ? `${items.length} paused or archived ${items.length === 1 ? "assignment is" : "assignments are"} hidden.` : "Change the search or status filter."}</p>
          {items.length > 0 && status && <button onClick={() => setStatus("")}>Show all assignments</button>}
        </div>
      )}
    </>
  );
}

export const projectAssignmentStyles = `
.ap-processes .version-tag{display:inline-block;font-size:11px;padding:1px 7px;border:1px solid var(--pc-line);border-radius:6px;color:var(--pc-text);font-variant-numeric:tabular-nums}
.ap-processes .assignment-title{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
.ap-processes td button.link{font-weight:400;color:var(--pc-muted);text-decoration:underline;text-underline-offset:3px}
.ap-processes td button.link:hover{color:var(--pc-text)}
.ap-processes .assignment-sop{display:flex;align-items:center;gap:7px;max-width:none;white-space:nowrap;min-width:0}
.ap-processes .assignment-sop button.link{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ap-processes .assignment-sop .version-tag,.ap-processes .assignment-sop-meta{flex:none}
.ap-processes td button.last-run{display:flex;flex-direction:column;align-items:flex-start;gap:2px;font-weight:400}
.ap-processes td button.last-run .sub{margin-top:2px}
.ap-processes td.starts{min-width:180px}
.ap-processes .start-line+.start-line{margin-top:8px}
.ap-processes .start-kind{display:inline-block;font-size:10.5px;padding:1px 6px;border:1px solid var(--pc-line);border-radius:5px;color:var(--pc-muted);margin-right:4px}
.ap-processes .start-line .sub{margin-top:3px;overflow-wrap:anywhere}
.ap-processes .start-issue{color:#e3b86d}
.ap-processes .start-event{display:flex;gap:10px;align-items:flex-start}
.ap-processes .start-event .tool-icon{flex:none;width:28px;height:28px;padding:4px}
.ap-processes .start-copy{min-width:0}
.ap-processes .start-head{display:flex;flex-wrap:wrap;align-items:baseline;gap:0 6px}
.ap-processes .start-app{color:var(--pc-muted);font-size:12px}
.ap-processes .start-app::after{content:"·";margin-left:6px}
.ap-processes .start-event-title{font-weight:600}
.ap-processes .start-event .sub{margin-top:2px}
`;
