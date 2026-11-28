import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  activityMetrics,
  crmActivityAPIURL,
  crmActivityContactLabel,
  crmActivityContactsLink,
  crmActivityDayLabel,
  crmActivityDelta,
  crmActivityPeriodLabel,
  crmActivityPreferences,
  type ActivityMetric,
  type ActivityResponse,
  type ActivityStats,
  type ActivityTotals,
} from "./activity";
import {
  crmInboxIsGlobal,
  crmInboxProjectColor,
  crmInboxRelativeTime,
  type CRMInboxHostProps,
  type InboxProject,
} from "./inbox";

// Global scope gets no live app events, so it polls on its own.
const projectRefreshMs = 120000;
const globalRefreshMs = 30000;

async function readJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", signal });
  if (!response.ok) {
    const body = (await response.text().catch(() => "")).trim();
    let detail = body;
    try {
      const parsed = JSON.parse(body) as { error?: string };
      if (parsed.error) detail = parsed.error;
    } catch {}
    throw new Error(detail || `Request failed (${response.status})`);
  }
  return response.json() as Promise<T>;
}

const tiles: { key: keyof ActivityTotals | "open_now"; label: string }[] = [
  { key: "new_contacts", label: "New contacts" },
  { key: "messages_received", label: "Received" },
  { key: "messages_sent", label: "Sent" },
  { key: "conversations_opened", label: "Conversations opened" },
  { key: "conversations_closed", label: "Closed" },
  { key: "open_now", label: "Open now" },
];

function StatTile({ stats, tile }: { stats: ActivityStats; tile: (typeof tiles)[number] }) {
  const value = tile.key === "open_now" ? stats.open_now : stats.totals[tile.key];
  const delta = tile.key === "open_now" ? null : crmActivityDelta(stats.totals[tile.key], stats.previous[tile.key]);
  return (
    <div className="min-w-0 rounded-md border border-border bg-bg-input px-2.5 py-2" data-stat={tile.key}>
      <p className="truncate text-[10px] text-text-dim">{tile.label}</p>
      <p className="mt-0.5 text-lg font-bold leading-none tabular-nums text-text">{value.toLocaleString()}</p>
      {delta ? (
        <p className="mt-1 truncate text-[10px] text-text-muted">
          {delta.direction === "up" ? "▲ " : delta.direction === "down" ? "▼ " : ""}{delta.label}
        </p>
      ) : (
        <p className="mt-1 truncate text-[10px] text-text-muted">Right now</p>
      )}
      {tile.key === "messages_sent" && stats.totals.send_failures > 0 && (
        <p className="truncate text-[10px] text-red">{stats.totals.send_failures} failed</p>
      )}
    </div>
  );
}

function DailyBars({ stats, metric }: { stats: ActivityStats; metric: ActivityMetric }) {
  const [hovered, setHovered] = useState<number | null>(null);
  const unit = activityMetrics.find(m => m.key === metric)?.unit || "";
  const values = stats.daily.map(day => day[metric]);
  const max = Math.max(1, ...values);
  const total = values.reduce((sum, n) => sum + n, 0);
  const hoveredDay = hovered === null ? null : stats.daily[hovered];
  const gap = stats.daily.length > 31 ? 1 : 2;
  return (
    <div>
      <p className="text-[11px] tabular-nums text-text-muted" aria-live="polite">
        {hoveredDay
          ? `${crmActivityDayLabel(hoveredDay.date)}: ${hoveredDay[metric].toLocaleString()} ${unit}`
          : `${total.toLocaleString()} ${unit} · ${crmActivityPeriodLabel(stats.period_days).toLowerCase()}`}
      </p>
      <div
        className="mt-1.5 flex items-end border-b border-border"
        style={{ height: 64, gap }}
        role="img"
        aria-label={`${unit} per day, ${crmActivityPeriodLabel(stats.period_days).toLowerCase()}`}
        onMouseLeave={() => setHovered(null)}
      >
        {stats.daily.map((day, index) => {
          const value = day[metric];
          const height = value === 0 ? 0 : Math.max(3, Math.round((value / max) * 64));
          return (
            <div
              key={day.date}
              className="flex h-full min-w-0 flex-1 items-end"
              title={`${crmActivityDayLabel(day.date)}: ${value} ${unit}`}
              onMouseEnter={() => setHovered(index)}
              data-day={day.date}
            >
              <div
                className="w-full"
                style={{
                  height,
                  backgroundColor: "var(--accent)",
                  opacity: hovered === null || hovered === index ? 1 : 0.45,
                  borderRadius: gap > 1 ? "4px 4px 0 0" : "2px 2px 0 0",
                }}
              />
            </div>
          );
        })}
      </div>
      <div className="mt-1 flex justify-between text-[10px] tabular-nums text-text-dim">
        <span>{stats.daily[0] ? crmActivityDayLabel(stats.daily[0].date) : ""}</span>
        <span>{stats.daily.length ? crmActivityDayLabel(stats.daily[stats.daily.length - 1].date) : ""}</span>
      </div>
    </div>
  );
}

export default function CrmActivityWidget(props: CRMInboxHostProps) {
  const global = crmInboxIsGlobal(props);
  const preferences = useMemo(() => crmActivityPreferences(props.widgetSettings), [props.widgetSettings]);
  const [stats, setStats] = useState<ActivityStats | null>(null);
  const [error, setError] = useState("");
  const [projects, setProjects] = useState<InboxProject[]>([]);
  const [selectedProject, setSelectedProject] = useState("");
  const [metric, setMetric] = useState<ActivityMetric>(preferences.metric);
  const request = useRef<AbortController | null>(null);

  useEffect(() => setMetric(preferences.metric), [preferences.metric]);

  const load = useCallback(async () => {
    if (!global && !props.projectId) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    try {
      const response = await readJSON<ActivityResponse>(crmActivityAPIURL(props, preferences, global ? selectedProject : ""), controller.signal);
      if (request.current !== controller) return;
      if (global) setProjects(response.projects || []);
      setStats(response.stats);
      setError("");
    } catch (cause) {
      if (controller.signal.aborted || request.current !== controller) return;
      // Keep the last numbers on a failed refresh; the error shows above them.
      setError((cause as Error).message);
    }
  }, [global, selectedProject, props.appName, props.installId, props.projectId, preferences]);

  useEffect(() => {
    void load();
    return () => request.current?.abort();
  }, [load, props.eventRevision]);

  useEffect(() => {
    const refresh = window.setInterval(() => void load(), global ? globalRefreshMs : projectRefreshMs);
    return () => window.clearInterval(refresh);
  }, [load, global]);

  if (!global && !props.projectId) {
    return (
      <section className="flex h-full min-h-0 items-center justify-center rounded-lg border border-border bg-bg-card p-5 text-center">
        <div>
          <p className="text-sm font-semibold text-text">CRM activity</p>
          <p className="mt-1 text-xs text-text-dim">Select a project to see CRM activity.</p>
        </div>
      </section>
    );
  }

  const full = props.widgetSize === "full";
  const contacts = (stats?.latest_contacts || []).slice(0, full ? 6 : 3);
  const headerLink = crmActivityContactsLink(props, selectedProject);

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <h2 className="text-sm font-bold text-text">CRM activity</h2>
          <p className="mt-0.5 text-[11px] text-text-dim">
            {crmActivityPeriodLabel(preferences.period)}
            {global && !selectedProject ? " across projects" : ""}
            {preferences.hideAutomated ? " · automated senders hidden" : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2 pt-0.5">
          {global && projects.length > 1 && (
            <select
              aria-label="Project"
              value={selectedProject}
              onChange={event => { setSelectedProject(event.target.value); setStats(null); }}
              className="rounded border border-border bg-bg-input px-1.5 py-0.5 text-[11px] text-text"
              style={{ maxWidth: 160 }}
            >
              <option value="">All projects</option>
              {projects.map(project => <option key={project.id} value={project.id}>{project.name || project.id}</option>)}
            </select>
          )}
          {headerLink && <a href={headerLink} className="text-[11px] text-text-muted hover:text-text">Contacts →</a>}
        </div>
      </header>

      {error && stats && (
        <div className="flex shrink-0 items-center justify-between gap-2 border-b border-border px-4 py-2" role="alert">
          <p className="truncate text-[11px] text-red" title={error}>Couldn't refresh: {error}</p>
          <button type="button" onClick={() => void load()} className="shrink-0 rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button>
        </div>
      )}

      {!stats ? (
        error ? (
          <div className="flex flex-1 flex-col items-start justify-center gap-2 p-4" style={{ minHeight: 112 }} role="alert">
            <p className="text-xs text-red">{error}</p>
            <button type="button" onClick={() => void load()} className="rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button>
          </div>
        ) : (
          <p className="flex flex-1 items-center p-4 text-xs text-text-dim" style={{ minHeight: 112 }}>Loading CRM activity…</p>
        )
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
          <div className="grid grid-cols-3 gap-2">
            {tiles.map(tile => <StatTile key={tile.key} stats={stats} tile={tile} />)}
          </div>

          <div className="mt-4">
            <div className="flex flex-wrap items-center gap-1" role="tablist" aria-label="Daily metric">
              {activityMetrics.map(option => (
                <button
                  key={option.key}
                  type="button"
                  role="tab"
                  aria-selected={metric === option.key}
                  onClick={() => setMetric(option.key)}
                  className={`rounded border px-2 py-0.5 text-[10px] ${metric === option.key ? "border-border bg-bg-input font-semibold text-text" : "border-transparent text-text-muted hover:text-text"}`}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <div className="mt-2">
              <DailyBars stats={stats} metric={metric} />
            </div>
          </div>

          <div className="mt-4">
            <p className="text-[11px] font-semibold text-text">Latest contacts</p>
            {contacts.length === 0 ? (
              <p className="mt-1 text-[11px] text-text-dim">No contacts yet.</p>
            ) : (
              <ul className="mt-1 divide-y divide-border">
                {contacts.map(contact => {
                  const color = global ? crmInboxProjectColor(props, contact.project_id) : "";
                  const secondary = [contact.name && (contact.email || contact.phone), contact.source].filter(Boolean).join(" · ");
                  return (
                    <li key={`${contact.project_id || ""}:${contact.id}`} className="flex min-w-0 items-center gap-2 py-1.5">
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-xs font-semibold text-text">{crmActivityContactLabel(contact)}</p>
                        {secondary && <p className="truncate text-[10px] text-text-dim">{secondary}</p>}
                      </div>
                      {global && contact.project_name && (
                        <span
                          className="inline-flex shrink-0 items-center gap-1 rounded border border-border bg-bg-input px-1.5 py-0.5 text-[10px] text-text-muted"
                          style={{ maxWidth: "40%" }}
                          title={contact.project_name}
                          data-project-color={color || undefined}
                        >
                          {color && <span aria-hidden="true" className="shrink-0 rounded-full" style={{ width: 6, height: 6, backgroundColor: color }} />}
                          <span className="truncate">{contact.project_name}</span>
                        </span>
                      )}
                      <span className="shrink-0 text-[10px] tabular-nums text-text-dim">{crmInboxRelativeTime(contact.created_at)}</span>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
