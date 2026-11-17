import { useEffect, useState } from "react";
import { assignmentAgentIDs, type Assignment } from "./Assignments";
import type { LiveSnapshot } from "./live-events";
import { useScopedRevision } from "./live-events";

export type ProjectAssignment = Assignment & {
  process_name: string;
  process_status: string;
  run_count: number;
  ongoing_count: number;
  last_run_id?: string;
  last_run_state?: string;
  last_run_at?: string;
};

type Props = {
  api: (path: string) => Promise<any>;
  live: LiveSnapshot;
  ownerName: (id: number) => string;
  cadence: (schedule?: Assignment["schedule"]) => string;
  date: (s?: string) => string;
  onRuns: (assignment: ProjectAssignment) => void;
  onRun: (assignment: ProjectAssignment, runID: string) => void;
  onProcess: (assignment: ProjectAssignment) => void;
};

// Every assignment in the project, with its run activity. Assignment and run
// events from the app bus refresh the list in place.
export default function ProjectAssignments({ api, live, ownerName, cadence, date, onRuns, onRun, onProcess }: Props) {
  const [items, setItems] = useState<ProjectAssignment[] | null>(null);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
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
    (!status || (status === "ongoing" ? x.ongoing_count > 0 : x.status === status)) &&
    (!needle || [x.name, x.process_name, ownerName(x.owner_agent_id)].some((v) => v?.toLowerCase().includes(needle)) || assignmentAgentIDs(x).some((id) => ownerName(id).toLowerCase().includes(needle))),
  );
  const active = items.filter((x) => x.status === "active").length;
  const scheduled = items.filter((x) => x.status === "active" && x.schedule).length;
  const ongoing = items.reduce((n, x) => n + x.ongoing_count, 0);
  return (
    <>
      <div className="stats">
        <div className="stat"><strong>{active}</strong><span>Active assignments</span></div>
        <div className="stat"><strong>{scheduled}</strong><span>On a schedule</span></div>
        <div className="stat"><strong>{ongoing}</strong><span>Runs in progress</span></div>
      </div>
      <div className="row filters">
        <input aria-label="Search assignments" placeholder="Search assignments, processes or agents…" value={search} onChange={(e) => setSearch(e.target.value)} />
        <select aria-label="Filter assignment status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">All assignments</option>
          <option value="active">Active</option>
          <option value="paused">Paused</option>
          <option value="ongoing">With runs in progress</option>
          <option value="archived">Archived</option>
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
                <th className="hide-small">Schedule</th>
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
                    <div className="sub">
                      <button className="link" onClick={(e) => { e.stopPropagation(); onProcess(x); }}>{x.process_name || "Process"}</button>
                      {x.process_status !== "active" && <> · process {x.process_status}</>}
                    </div>
                    {x.sync_error && <div className="sub">{x.sync_error}</div>}
                  </td>
                  <td>
                    {ownerName(x.owner_agent_id)}
                    {assignmentAgentIDs(x).length > 1 && (
                      <div className="sub">+ {assignmentAgentIDs(x).slice(1).map(ownerName).join(", ")}</div>
                    )}
                  </td>
                  <td className="hide-small">
                    {cadence(x.schedule)}
                    {x.status === "active" && x.next_run_at && <div className="sub">Next {date(x.next_run_at)}</div>}
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
          <h2>{items.length ? "No matching assignments" : "No assignments yet"}</h2>
          <p>{items.length ? "Change the search or status filter." : "Assign a process to an agent from the process page to see it here."}</p>
        </div>
      )}
    </>
  );
}

export const projectAssignmentStyles = `
.ap-processes .assignment-title{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
.ap-processes td button.link{font-weight:400;color:var(--pc-muted);text-decoration:underline;text-underline-offset:3px}
.ap-processes td button.link:hover{color:var(--pc-text)}
.ap-processes td button.last-run{display:flex;flex-direction:column;align-items:flex-start;gap:2px;font-weight:400}
.ap-processes td button.last-run .sub{margin-top:2px}
`;
