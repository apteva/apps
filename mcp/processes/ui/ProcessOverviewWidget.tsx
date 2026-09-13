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
export type Item = {
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
export type Data = {
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
export type QueueEntry = {
  item: Item;
  assignment: boolean;
  group: "running" | "scheduled" | "history";
};
export function overviewQueue(
  data: Data,
  settings?: Record<string, unknown>,
): QueueEntry[] {
  const working = (x: Item) =>
    ["running", "ready", "queued", "pending"].includes(x.state);
  const entries: QueueEntry[] = [
    ...(settings?.show_active === false
      ? []
      : data.active.map((item) => ({
          item,
          assignment: false,
          group: working(item) ? ("running" as const) : ("history" as const),
        }))),
    ...(settings?.show_upcoming === false
      ? []
      : data.upcoming.map((item) => ({
          item,
          assignment: true,
          group: "scheduled" as const,
        }))),
    ...(settings?.show_recent === false
      ? []
      : data.recent.map((item) => ({
          item,
          assignment: false,
          group: "history" as const,
        }))),
    ...(settings?.show_active === false
      ? []
      : data.attention.map((item) => ({
          item,
          assignment: true,
          group: "history" as const,
        }))),
  ];
  const rank = { running: 0, scheduled: 1, history: 2 };
  const date = (x: QueueEntry) =>
    Date.parse(x.item.next_run_at || x.item.created_at || "") || 0;
  return entries.sort(
    (a, b) =>
      rank[a.group] - rank[b.group] ||
      (a.group === "scheduled" ? date(a) - date(b) : date(b) - date(a)),
  );
}
export function executionStatus({ item: x, assignment }: QueueEntry) {
  if (assignment)
    return {
      label: x.next_run_at ? "Next run" : "Assignment",
      state: x.state,
      current: false,
      extra: 0,
      hint: "",
    };
  if (terminal(x.state))
    return {
      label:
        x.state === "completed"
          ? "Finished"
          : x.state === "cancelled"
            ? "Stopped"
            : "Run failed",
      state: x.state,
      current: false,
      extra: 0,
      hint: x.warning || "",
    };
  const steps = x.steps || [];
  const current = steps.filter((s) =>
    ["running", "blocked", "waiting", "ready"].includes(s.state),
  );
  const matching = current.find((s) => s.state === x.state);
  if (["blocked", "waiting"].includes(x.state) && !matching)
    return {
      label:
        x.current_step ||
        (x.state === "blocked" ? "Run blocked" : "Waiting for input"),
      state: x.state,
      current: true,
      extra: 0,
      hint: x.warning || "",
    };
  const step =
    matching ||
    current.find((s) => s.state === "running") ||
    current.find((s) => s.state === "blocked") ||
    current.find((s) => s.state === "waiting") ||
    current[0];
  const same = current.filter((s) => s.state === step?.state);
  return {
    label: step?.name || x.current_step || "Preparing run",
    state:
      step?.kind === "approval" &&
      step.executor.kind === "human" &&
      ["ready", "running", "waiting"].includes(step.state)
        ? "review"
        : step?.state || x.state,
    current: true,
    extra: Math.max(0, same.length - 1),
    hint:
      current.map((s) => `${s.name} (${s.state})`).join(" · ") ||
      x.warning ||
      "",
  };
}
const css = `
.po-widget{color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-family:inherit;min-width:0;overflow:hidden}
.po-widget *{box-sizing:border-box}.po-widget h2,.po-widget p{margin:0}.po-widget header{padding:14px 16px;border-bottom:1px solid var(--color-border,#303030)}.po-widget h2{font-size:14px;font-weight:700}.po-widget header p{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}.po-widget button,.po-widget a{font-family:inherit}.po-widget button{cursor:pointer}.po-widget :is(button,a):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}.po-widget .po-filters{display:flex;gap:5px;padding:8px 16px;border-bottom:1px solid var(--color-border,#303030);overflow-x:auto}.po-widget .po-filters button,.po-widget .po-close{background:transparent;color:var(--color-text-muted,#aaa);font-size:11px;font-weight:600;border:1px solid var(--color-border,#303030);border-radius:4px;padding:6px 9px;white-space:nowrap}.po-widget .po-filters button[aria-pressed=true]{color:var(--color-accent,#ff8c36);border-color:var(--color-accent,#ff8c36);background:color-mix(in srgb,var(--color-accent,#ff8c36) 10%,transparent)}
.po-widget .po-row{display:grid;grid-template-columns:minmax(0,1fr) minmax(160px,35%);gap:12px;align-items:center;width:100%;height:80px;padding:12px 16px;background:transparent;color:inherit;border:0;border-bottom:1px solid var(--color-border,#303030);text-align:left}.po-widget .po-row:hover,.po-widget .po-row[aria-expanded=true]{background:var(--color-bg-hover,#202020)}.po-widget .po-copy{min-width:0}.po-widget .po-execution{min-width:0;text-align:right}.po-widget .po-current{display:block;font-size:12px;font-weight:500;line-height:20px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.po-widget .po-execution-meta{display:flex;align-items:center;justify-content:flex-end;gap:7px;margin-top:5px;font-size:10px;line-height:18px;color:var(--color-text-muted,#aaa);white-space:nowrap}.po-widget .po-badge.review{color:#e3b86d;border-color:#e3b86d60}.po-widget .po-title{display:block;font-size:13px;line-height:20px;font-weight:600;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-widget .po-summary{display:block;margin-top:5px;font-size:11px;line-height:18px;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.po-widget time{font-size:10px;color:var(--color-text-muted,#999);text-align:right;white-space:nowrap}.po-widget .po-badge{display:inline-block;justify-self:start;font-size:10px;font-weight:600;text-transform:uppercase;line-height:16px;padding:2px 6px;border-radius:4px;border:1px solid var(--color-border,#424242);color:var(--color-text-muted,#aaa)}.po-widget .po-badge.running,.po-widget .po-badge.ready{color:#8eabff;border-color:#8eabff60}.po-widget .po-badge.scheduled{color:#be9ff5;border-color:#be9ff560}.po-widget .po-badge.completed{color:#43c878;border-color:#43c87860}.po-widget .po-badge.blocked,.po-widget .po-badge.waiting,.po-widget .po-badge.attention{color:#e3b86d;border-color:#e3b86d60}.po-widget .po-badge.failed{color:#f08b8b;border-color:#f08b8b60}
.po-widget .po-warning{font-size:11px;color:#e3b86d;padding:10px 16px;overflow-wrap:anywhere}.po-widget .po-empty{font-size:12px;color:var(--color-text-muted,#aaa);padding:24px 16px}.po-widget footer{display:flex;justify-content:space-between;gap:8px;padding:9px 16px;font-size:11px;color:var(--color-text-muted,#999)}.po-widget a{color:var(--color-accent,#ff8c36);text-decoration:none;font-size:11px}.po-widget .po-detail{padding:16px;border-bottom:1px solid var(--color-border,#303030)}.po-widget .po-detail-head{display:flex;align-items:center;justify-content:space-between;gap:12px}.po-widget .po-detail h3{font-size:13px;margin:0}.po-widget .po-detail p{font-size:12px;color:var(--color-text-muted,#aaa);margin:8px 0;overflow-wrap:anywhere}.po-widget .po-detail ol{padding:0;margin:10px 0;list-style:none;max-height:200px;overflow:auto}.po-widget .po-detail li{display:flex;justify-content:space-between;align-items:center;gap:12px;font-size:12px;padding:10px 0;border-top:1px solid var(--color-border,#303030)}.po-widget .po-step-copy{min-width:0}.po-widget .po-step-copy small{display:block;color:var(--color-text-muted,#aaa);margin-top:4px}
@media(max-width:480px){.po-widget .po-row{grid-template-columns:minmax(0,1fr) minmax(130px,46%);gap:8px;padding:12px}.po-widget .po-badge{font-size:9px;padding:2px 4px}.po-widget header,.po-widget .po-filters{padding-left:12px;padding-right:12px}}
`;
export default function ProcessOverviewWidget(props: Props) {
  return (
    <Overview
      key={`${props.projectId}:${props.installId}:${props.appName}`}
      {...props}
    />
  );
}
function Overview(props: Props) {
  const [view, setView] = useState("all"),
    [selected, setSelected] = useState<string | null>(null),
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
  const raw = Number(
    props.widgetSettings?.row_limit ?? props.widgetSettings?.recent_limit ?? 4,
  );
  const limit = Number.isFinite(raw)
    ? Math.max(1, Math.min(6, Math.round(raw)))
    : 4;
  const name = (id?: number) =>
    id ? names[id] || `Agent ${id}` : "Unassigned";
  const key = (x: QueueEntry) =>
    `${x.assignment ? "assignment" : "run"}:${x.item.process_id}:${x.item.id}`;
  const queue = data ? overviewQueue(data, props.widgetSettings) : [];
  const filtered = queue.filter((x) => view === "all" || x.group === view);
  const shown = filtered.slice(0, limit);
  const detail = queue.find((x) => key(x) === selected);
  const badge = (state: string) => (
    <span
      className={`po-badge ${state === "sync pending" ? "attention" : state}`}
    >
      {state === "sync pending"
        ? "Attention"
        : state === "review"
          ? "Review"
          : state.replaceAll("_", " ")}
    </span>
  );
  const relative = (value?: string) => {
    const time = Date.parse(value || "");
    if (!Number.isFinite(time)) return "—";
    const delta = time - Date.now(),
      abs = Math.abs(delta);
    if (abs < 60000) return delta > 0 ? "Soon" : "Just now";
    const amount =
      abs < 3600000
        ? `${Math.floor(abs / 60000)}m`
        : abs < 86400000
          ? `${Math.floor(abs / 3600000)}h`
          : `${Math.floor(abs / 86400000)}d`;
    return delta > 0 ? `in ${amount}` : `${amount} ago`;
  };
  return (
    <section aria-label="Processes overview" className="po-widget">
      <style>{css}</style>
      <header>
        <h2>Processes</h2>
        <p>Running flows, upcoming schedules, and recent outcomes</p>
      </header>
      <nav className="po-filters" aria-label="Filter processes">
        {[
          ["all", "All"],
          ["running", "Running"],
          ["scheduled", "Scheduled"],
          ["history", "History"],
        ].map(([value, label]) => (
          <button
            key={value}
            type="button"
            aria-pressed={view === value}
            onClick={() => {
              setView(value);
              setSelected(null);
            }}
          >
            {label}
          </button>
        ))}
      </nav>
      {error && (
        <p role="alert" className="po-warning">
          {error}
          {data ? " · Showing the last received snapshot." : ""}{" "}
          <button className="po-close" onClick={() => setRefresh((v) => v + 1)}>
            Retry
          </button>
        </p>
      )}
      {!props.projectId ? (
        <p className="po-empty">Select a project to see its processes.</p>
      ) : !data ? (
        <p role="status" className="po-empty">
          {error ? "Overview unavailable." : "Loading processes…"}
        </p>
      ) : (
        <>
          {!!data.warnings.length && (
            <p className="po-warning">{data.warnings.join(" ")}</p>
          )}
          <div className="po-queue" aria-label="Process executions">
            {shown.map((entry) => {
              const x = entry.item;
              const status = executionStatus(entry);
              const identity = [x.assignment_name || name(x.agent_id), x.target]
                .filter(Boolean)
                .join(" · ");
              const progress = x.steps_total
                ? `${x.steps_completed || 0}/${x.steps_total} done`
                : "";
              const statusLabel =
                entry.assignment && x.next_run_at
                  ? `Next ${relative(x.next_run_at)}`
                  : status.label + (status.extra ? ` +${status.extra}` : "");
              return (
                <button
                  type="button"
                  className="po-row"
                  data-state={x.state}
                  key={key(entry)}
                  aria-expanded={selected === key(entry)}
                  aria-label={`${x.process_name} · ${x.state} · ${statusLabel} · ${status.state}`}
                  onClick={() =>
                    setSelected(selected === key(entry) ? null : key(entry))
                  }
                >
                  <span className="po-copy">
                    <span className="po-title" title={x.process_name}>
                      {x.process_name}
                    </span>
                    <span className="po-summary" title={identity}>
                      {identity}
                    </span>
                  </span>
                  <span
                    className="po-execution"
                    title={
                      status.hint ||
                      `${entry.assignment ? "Next" : "Started"}: ${when(entry.assignment ? x.next_run_at : x.created_at)}`
                    }
                  >
                    <span className="po-current">{statusLabel}</span>
                    <span className="po-execution-meta">
                      {badge(status.state)}
                      {progress ? (
                        <span>{progress}</span>
                      ) : !entry.assignment && terminal(x.state) ? (
                        <time dateTime={x.created_at}>
                          {relative(x.created_at)}
                        </time>
                      ) : null}
                    </span>
                  </span>
                </button>
              );
            })}
            {!shown.length && (
              <p className="po-empty">
                No{" "}
                {view === "all"
                  ? "process executions"
                  : view === "history"
                    ? "past or blocked runs"
                    : view === "scheduled"
                      ? "scheduled processes"
                      : "running processes"}
                .
              </p>
            )}
          </div>
          {detail && (
            <section
              className="po-detail"
              aria-label="Selected process details"
              onKeyDown={(e) => {
                if (e.key === "Escape") setSelected(null);
              }}
            >
              <div className="po-detail-head">
                <h3>{detail.item.process_name}</h3>
                <button className="po-close" onClick={() => setSelected(null)}>
                  Close details
                </button>
              </div>
              <p>
                {[
                  detail.item.assignment_name,
                  detail.item.target,
                  name(detail.item.agent_id),
                ]
                  .filter(Boolean)
                  .join(" · ")}
              </p>
              <p>
                {detail.assignment ? "Next" : "Started"}{" "}
                {when(
                  detail.assignment
                    ? detail.item.next_run_at
                    : detail.item.created_at,
                )}
                {detail.item.version ? ` · v${detail.item.version}` : ""}
              </p>
              {detail.item.warning && <p>{detail.item.warning}</p>}
              {!!detail.item.steps_total && (
                <>
                  <p>
                    {detail.item.steps_completed || 0}/{detail.item.steps_total}{" "}
                    steps complete
                  </p>
                  <ol>
                    {(detail.item.steps || []).map((s) => (
                      <li key={s.id}>
                        <span className="po-step-copy">
                          {s.name}
                          <small>
                            {s.executor.kind === "human"
                              ? "Human review"
                              : name(s.executor.agent_id)}
                            {s.warning ? ` · ${s.warning}` : ""}
                          </small>
                        </span>
                        {badge(s.state)}
                      </li>
                    ))}
                  </ol>
                </>
              )}
              <a href={overviewLink(props, detail.item, detail.assignment)}>
                {detail.assignment ? "Open assignment" : "Open run"} ↗
              </a>
            </section>
          )}
          <footer>
            <span>
              {filtered.length > limit
                ? `Showing first ${limit}`
                : `${shown.length} shown`}
            </span>
            <a href={overviewLink(props)}>All processes ↗</a>
          </footer>
        </>
      )}
    </section>
  );
}
