import { useEffect, useState } from "react";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};
type Step = {
  id: string;
  name: string;
  state: string;
  progress: number;
  kind: string;
  origin: string;
  executor: { kind: string; agent_id?: number };
  warning?: string;
};
type Item = {
  id: string;
  process_id: string;
  process_name: string;
  assignment_id?: string;
  assignment_name?: string;
  target?: string;
  agent_id?: number;
  version?: number;
  state: string;
  backend?: string;
  progress?: number;
  current_step?: string;
  created_at?: string;
  next_run_at?: string;
  schedule?: { kind: string; every?: string; cron?: string; timezone?: string };
  needs_attention?: boolean;
  warning?: string;
  steps?: Step[];
  steps_completed?: number;
  steps_total?: number;
};
type Data = {
  counts: {
    active: number;
    scheduled: number;
    attention: number;
    recent: number;
  };
  active: Item[];
  upcoming: Item[];
  recent: Item[];
  attention: Item[];
  warnings: string[];
  partial: boolean;
  generated_at: string;
};
const when = (s?: string) =>
  s
    ? new Date(s).toLocaleString(undefined, {
        dateStyle: "short",
        timeStyle: "short",
      })
    : "Time unavailable";
const terminal = (s: string) =>
  ["completed", "cancelled", "failed"].includes(s);
export function overviewLink(props: Props, x?: Item, assignment = false) {
  const q = new URLSearchParams();
  if (props.projectId) q.set("project_id", props.projectId);
  if (props.installId) q.set("install_id", String(props.installId));
  let app = props.appName || "processes";
  if (x) {
    if (x.backend === "tasks" && !assignment) {
      app = "tasks";
      q.delete("install_id");
      q.set("task_id", x.id);
    } else {
      q.set("process_id", x.process_id);
      if (assignment) q.set("assignment_id", x.assignment_id || x.id);
      else q.set("run_id", x.id);
    }
  }
  return `/apps/${encodeURIComponent(app)}/page?${q}`;
}
const css = `.po-widget{font:12px/1.35 system-ui;color:var(--color-text,#eceef2);background:var(--color-bg-card,#181b21);border:1px solid var(--color-border,#30343e);border-radius:8px;padding:12px;box-sizing:border-box;height:100%;max-height:520px;min-width:0;overflow:auto}.po-widget *{box-sizing:border-box}.po-widget h2,.po-widget h3,.po-widget p{margin:0}.po-widget h2{font-size:14px}.po-widget h3{font-size:10px;text-transform:uppercase;letter-spacing:.06em;color:var(--color-text-muted,#969eac);margin:12px 0 4px}.po-widget header,.po-widget .po-line{display:flex;gap:6px;align-items:center;justify-content:space-between;flex-wrap:wrap}.po-widget .po-sub{font-size:10px;color:var(--color-text-muted,#969eac);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-widget a{color:var(--color-accent,#a6afff);text-decoration:none;font-size:10px}.po-widget button{font:inherit;font-size:10px;color:inherit;background:transparent;border:1px solid var(--color-border,#30343e);padding:3px 7px;border-radius:5px;cursor:pointer}.po-widget .po-stats{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:6px;padding:9px 0;border-bottom:1px solid var(--color-border,#30343e)}.po-widget .po-stats b{display:inline;font-size:18px;margin-right:4px}.po-widget .po-stats span{font-size:10px;color:var(--color-text-muted,#969eac)}.po-widget article{padding:6px 0;border-bottom:1px solid var(--color-border,#30343e)}.po-widget article:last-child{border-bottom:0}.po-widget .po-badge{font-size:9px;border-radius:6px;background:#969eac18;padding:1px 5px;white-space:nowrap;text-transform:capitalize}.po-widget .po-badge.running,.po-widget .po-badge.ready{color:#9caaff}.po-widget .po-badge.completed{color:#64cba5}.po-widget .po-badge.waiting,.po-widget .po-badge.blocked,.po-widget .po-badge.failed,.po-widget .po-warning{color:#e3b86d}.po-widget .po-warning{font-size:10px;margin:3px 0}.po-widget progress{display:block;width:100%;height:3px;margin:4px 0}.po-widget summary{cursor:pointer;font-size:10px;padding:3px 0}.po-widget ol{padding:0;list-style:none;margin:2px 0}.po-widget li{padding:3px 0 3px 7px;border-left:2px solid var(--color-border,#30343e)}.po-widget .po-live{margin:2px 0}.po-widget footer{padding-top:7px}.po-widget .po-filters{display:flex;gap:4px;overflow-x:auto;padding:7px 0;border-bottom:1px solid var(--color-border,#30343e)}.po-widget .po-filters button{border:0;color:var(--color-text-muted,#969eac);padding:3px 6px;white-space:nowrap}.po-widget .po-filters button[aria-pressed=true]{color:var(--color-accent,#a6afff);background:#969eac18}@media(max-width:600px){.po-widget{padding:10px}}`;
export default function ProcessOverviewWidget(props: Props) {
  return (
    <Overview
      key={`${props.projectId}:${props.installId}:${props.appName}`}
      {...props}
    />
  );
}
function Overview(props: Props) {
  const [view, setView] = useState("active"),
    [data, setData] = useState<Data | null>(null),
    [error, setError] = useState(""),
    [refresh, setRefresh] = useState(0),
    [names, setNames] = useState<Record<number, string>>({});
  useEffect(() => {
    if (!props.projectId) return;
    let alive = true;
    const c = new AbortController();
    const q = new URLSearchParams({ project_id: props.projectId });
    if (props.installId) q.set("install_id", String(props.installId));
    fetch(
      `/api/apps/${encodeURIComponent(props.appName || "processes")}/processes/overview?${q}`,
      { credentials: "same-origin", signal: c.signal },
    )
      .then(async (r) => {
        if (!r.ok) throw new Error(`Overview unavailable (${r.status})`);
        return r.json();
      })
      .then((d) => {
        if (alive) {
          setData(d);
          setError("");
        }
      })
      .catch((e) => {
        if (alive) setError(e.message);
      });
    return () => {
      alive = false;
      c.abort();
    };
  }, [
    props.appName,
    props.projectId,
    props.installId,
    props.eventRevision,
    refresh,
  ]);
  useEffect(() => {
    if (!props.projectId) return;
    let alive = true;
    const c = new AbortController();
    fetch(
      `/api/agents?${new URLSearchParams({ project_id: props.projectId })}`,
      { credentials: "same-origin", signal: c.signal },
    )
      .then((r) => {
        if (!r.ok) throw new Error("Agents unavailable");
        return r.json();
      })
      .then((d) => {
        if (alive)
          setNames(
            Object.fromEntries(
              (Array.isArray(d) ? d : d.agents || []).map((a: any) => [
                a.id,
                a.name,
              ]),
            ),
          );
      })
      .catch(() => {});
    return () => {
      alive = false;
      c.abort();
    };
  }, [props.projectId]);
  const raw = Number(props.widgetSettings?.recent_limit ?? 4),
    limit = Number.isFinite(raw)
      ? Math.max(1, Math.min(12, Math.round(raw)))
      : 4;
  const name = (id?: number) =>
    id ? names[id] || `Agent ${id}` : "Unassigned";
  const badge = (state: string) => (
    <span className={`po-badge ${state}`}>{state.replaceAll("_", " ")}</span>
  );
  const row = (x: Item, assignment = false) => (
    <article key={`${x.process_id}:${x.id}`}>
      <div className="po-line">
        <strong>{x.process_name}</strong>
        {badge(x.state)}
      </div>
      <p className="po-sub">
        {[x.assignment_name, x.target, name(x.agent_id)]
          .filter(Boolean)
          .join(" · ")}
      </p>
      {assignment ? (
        <p className="po-sub">
          {x.next_run_at
            ? `Next ${when(x.next_run_at)}`
            : x.schedule
              ? "Next time unavailable"
              : "Synchronization needs attention"}
          {x.schedule?.kind === "interval"
            ? ` · Every ${x.schedule.every}`
            : x.schedule?.cron
              ? ` · ${x.schedule.cron} (${x.schedule.timezone || "UTC"})`
              : ""}
        </p>
      ) : (
        <p className="po-sub">
          Started {when(x.created_at)}
          {x.version ? ` · v${x.version}` : ""}
        </p>
      )}
      {x.warning && <p className="po-warning">{x.warning}</p>}
      {!assignment && (
        <>
          {x.current_step && <p className="po-sub po-live">{x.current_step}</p>}
          {(x.steps || [])
            .filter((s) =>
              ["ready", "running", "waiting", "blocked"].includes(s.state),
            )
            .map((s) => (
              <p className="po-sub po-live" key={s.id}>
                {badge(s.state)} {s.name} ·{" "}
                {s.executor.kind === "human"
                  ? "Human review"
                  : name(s.executor.agent_id)}
              </p>
            ))}
          {!terminal(x.state) && (
            <progress
              aria-label={`${x.process_name} progress`}
              max={100}
              value={x.progress || 0}
            />
          )}{" "}
          {!!x.steps_total && (
            <details>
              <summary>
                {x.steps_completed || 0}/{x.steps_total} steps complete · View
                steps
              </summary>
              <ol>
                {(x.steps || []).map((s) => (
                  <li key={s.id}>
                    <div className="po-line">
                      <span>{s.name}</span>
                      {badge(s.state)}
                    </div>
                    <p className="po-sub">
                      {s.executor.kind === "human"
                        ? "Human review"
                        : name(s.executor.agent_id)}{" "}
                      · {s.progress}%
                      {s.origin === "attached" ? " · Added task" : ""}
                    </p>
                    {s.warning && <p className="po-warning">{s.warning}</p>}
                  </li>
                ))}
              </ol>
              {x.steps_total > (x.steps || []).length && (
                <p className="po-sub">More steps in run details.</p>
              )}
            </details>
          )}
        </>
      )}
      <a href={overviewLink(props, x, assignment)}>
        {assignment ? "Open assignment" : "Open run"} ↗
      </a>
    </article>
  );
  return (
    <section
      aria-label="Processes overview"
      className="po-widget"
      data-size={props.widgetSize || "half"}
    >
      <style>{css}</style>
      <header>
        <div>
          <h2>Processes</h2>
          <p className="po-sub">Across this project</p>
        </div>
        <button onClick={() => setRefresh((v) => v + 1)}>Refresh</button>
      </header>
      {error && (
        <p role="alert" className="po-warning">
          {error}
          {data ? " · Showing the last received snapshot." : ""}
        </p>
      )}
      {!props.projectId ? (
        <p className="po-sub">Select a project to see its processes.</p>
      ) : !data ? (
        <p role="status" className="po-sub">
          {error ? "Retry using Refresh." : "Loading processes…"}
        </p>
      ) : (
        <>
          <div className="po-stats">
            {[
              [data.counts.active, "Active"],
              [data.counts.scheduled, "Scheduled"],
              [data.counts.attention, "Attention"],
            ].map(([n, t]) => (
              <div key={t}>
                <b>
                  {data.partial ? "≥ " : ""}
                  {n}
                </b>
                <span>{t}</span>
              </div>
            ))}
          </div>
          {data.warnings.map((w, i) => (
            <p className="po-warning" key={i}>
              {w}
            </p>
          ))}
          <nav className="po-filters" aria-label="Filter processes">
            {["active", "scheduled", "recent"].map((key) => (
              <button
                type="button"
                key={key}
                aria-pressed={view === key}
                onClick={() => setView(key)}
              >
                {key[0].toUpperCase() + key.slice(1)}
                {key === "active"
                  ? ` · ${data.counts.active}`
                  : key === "scheduled"
                    ? ` · ${data.counts.scheduled}`
                    : ` · ${data.counts.recent}`}
              </button>
            ))}
          </nav>
          <h3>
            {view === "active"
              ? "Running and attention"
              : view === "scheduled"
                ? "Upcoming schedules"
                : "Recent outcomes"}
          </h3>
          {(view === "active"
            ? [
                ...data.active,
                ...data.attention.map((x) => ({
                  ...x,
                  state: x.state || "attention",
                })),
              ]
            : view === "scheduled"
              ? data.upcoming
              : data.recent
          )
            .slice(0, limit)
            .map((x) =>
              row(
                x,
                view === "scheduled" ||
                  (view === "active" && x.state === "sync pending"),
              ),
            )}
          {(view === "active"
            ? data.active.length + data.attention.length
            : view === "scheduled"
              ? data.upcoming.length
              : data.recent.length) === 0 && (
            <p className="po-sub po-empty">
              {view === "active"
                ? "Nothing running or needing attention."
                : view === "scheduled"
                  ? "No upcoming schedules."
                  : "No recent executions."}
            </p>
          )}
          <footer className="po-line">
            <span className="po-sub">Updated {when(data.generated_at)}</span>
            <a href={overviewLink(props)}>All processes ↗</a>
          </footer>
        </>
      )}
    </section>
  );
}
