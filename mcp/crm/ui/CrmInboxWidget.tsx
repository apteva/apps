import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { messageRecipientSummary } from "./message_addresses";
import { channelPresentation, channelThemeCSS } from "./channels";
import {
  crmInboxAPIURL,
  crmInboxContactLabel,
  crmInboxHeaderLink,
  crmInboxIsGlobal,
  crmInboxPageLink,
  crmInboxProjectColor,
  crmInboxPreferences,
  crmInboxSummary,
  crmInboxRelativeTime,
  type CRMInboxHostProps,
  type InboxItem,
  type InboxProject,
  type InboxResponse,
} from "./inbox";

const priorityStyles: Record<string, string> = {
  urgent: "bg-red/15 text-red border-red/30",
  high: "bg-yellow/15 text-yellow border-yellow/30",
  normal: "bg-bg-input text-text-muted border-border",
  low: "bg-bg-input text-text-dim border-border",
};

function channelLabel(channel: string): string {
  if (channel === "whatsapp") return "WhatsApp";
  if (channel === "sms") return "SMS";
  if (channel === "email") return "Email";
  return channel || "Message";
}

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

// Global scope gets no live app events, so it polls on its own.
const projectRefreshMs = 60000;
const globalRefreshMs = 15000;

export default function CrmInboxWidget(props: CRMInboxHostProps) {
  const global = crmInboxIsGlobal(props);
  const preferences = useMemo(() => crmInboxPreferences(props.widgetSettings), [props.widgetSettings]);
  const [items, setItems] = useState<InboxItem[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");
  const [projects, setProjects] = useState<InboxProject[]>([]);
  const [selectedProject, setSelectedProject] = useState("");
  const request = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    if (!global && !props.projectId) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setError("");
    try {
      const response = await readJSON<InboxResponse>(crmInboxAPIURL(props, preferences, global ? selectedProject : ""), controller.signal);
      if (request.current !== controller) return;
      if (global) setProjects(response.projects || []);
      setItems(response.inbox || []);
      setTotal(typeof response.total === "number" ? response.total : (response.inbox || []).length);
    } catch (cause) {
      if (controller.signal.aborted || request.current !== controller) return;
      // Keep the last rows on a failed refresh; the error shows above them.
      setError((cause as Error).message);
      setItems(previous => previous ?? []);
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

  const headerLink = crmInboxHeaderLink(props, selectedProject);

  if (!global && !props.projectId) {
    return (
      <section className="flex h-full min-h-0 items-center justify-center rounded-lg border border-border bg-bg-card p-5 text-center">
        <div>
          <p className="text-sm font-semibold text-text">Customer inbox</p>
          <p className="mt-1 text-xs text-text-dim">Select a project to see customer conversations.</p>
        </div>
      </section>
    );
  }

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <style>{channelThemeCSS}</style>
      <header className="flex shrink-0 items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-bold text-text">Customer inbox</h2>
            {total > 0 && (
              <span className="inline-flex min-w-5 items-center justify-center rounded-full bg-accent/15 px-1.5 py-0.5 text-[10px] font-bold tabular-nums text-accent">
                {total}
              </span>
            )}
          </div>
          <p className="mt-0.5 text-[11px] text-text-dim">
            {preferences.defaultStatus === "all" ? "Recent" : preferences.defaultStatus === "pending" ? "Pending" : "Open"} customer conversations
            {global && !selectedProject ? " across projects" : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2 pt-0.5">
          {global && projects.length > 1 && (
            <select
              aria-label="Project"
              value={selectedProject}
              onChange={event => { setSelectedProject(event.target.value); setItems(null); }}
              className="rounded border border-border bg-bg-input px-1.5 py-0.5 text-[11px] text-text"
              style={{ maxWidth: 160 }}
            >
              <option value="">All projects</option>
              {projects.map(project => <option key={project.id} value={project.id}>{project.name || project.id}</option>)}
            </select>
          )}
          {headerLink && <a href={headerLink} className="text-[11px] text-text-muted hover:text-text">View inbox →</a>}
        </div>
      </header>

      {error && items !== null && items.length > 0 && (
        <div className="flex shrink-0 items-center justify-between gap-2 border-b border-border px-4 py-2" role="alert">
          <p className="truncate text-[11px] text-red" title={error}>Couldn't refresh: {error}</p>
          <button type="button" onClick={() => void load()} className="shrink-0 rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button>
        </div>
      )}

      {error && !(items && items.length > 0) ? (
        <div className="flex flex-1 flex-col items-start justify-center gap-2 p-4" style={{ minHeight: 112 }} role="alert">
          <p className="text-xs text-red">{error}</p>
          <button type="button" onClick={() => void load()} className="rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button>
        </div>
      ) : items === null ? (
        <p className="flex flex-1 items-center p-4 text-xs text-text-dim" style={{ minHeight: 112 }}>Loading customer inbox…</p>
      ) : items.length === 0 ? (
        <div className="flex flex-1 flex-col justify-center p-4" style={{ minHeight: 112 }}>
          <p className="text-xs font-semibold text-text">Inbox clear</p>
          <p className="mt-1 text-[11px] text-text-dim">New matching conversations will appear here.</p>
        </div>
      ) : (
        <div className="min-h-0 flex-1 divide-y divide-border overflow-y-auto" aria-live="polite">
          {items.map((item) => (
            <a
              key={item.id}
              href={crmInboxPageLink(props, item)}
              className="block px-4 py-3 hover:bg-bg-hover/60"
            >
              <div className="flex min-w-0 items-center gap-2">
                <span className="truncate text-xs font-semibold text-text">{crmInboxContactLabel(item)}</span>
                {global && item.project_name && (() => {
                  const color = crmInboxProjectColor(props, item.project_id);
                  return (
                    <span
                      className="inline-flex shrink-0 items-center gap-1 rounded border border-border bg-bg-input px-1.5 py-0.5 text-[10px] text-text-muted"
                      style={{ maxWidth: "40%" }}
                      title={item.project_name}
                      data-project-color={color || undefined}
                    >
                      {color && <span aria-hidden="true" className="shrink-0 rounded-full" style={{ width: 6, height: 6, backgroundColor: color }} />}
                      <span className="truncate">{item.project_name}</span>
                    </span>
                  );
                })()}
                {(item.channels || [item.channel]).map(channel => <span key={channel} className="shrink-0 rounded px-1.5 py-0.5 text-[8px] font-bold" style={channelPresentation[channel] ? {color: channelPresentation[channel].color, backgroundColor: channelPresentation[channel].backgroundColor} : undefined}>
                  {channelPresentation[channel]?.icon} {channelPresentation[channel]?.label || channelLabel(channel)}
                </span>)}
                <span className="ml-auto shrink-0 text-[10px] tabular-nums text-text-dim">{crmInboxRelativeTime(item.last_activity_at)}</span>
              </div>
              <div className="mt-1 flex min-w-0 items-center gap-2">
                {(item.priority === "urgent" || item.priority === "high") && (
                  <span className={`shrink-0 rounded border px-1.5 py-0.5 text-[8px] font-bold uppercase ${priorityStyles[item.priority]}`}>
                    {item.priority}
                  </span>
                )}
                {item.automated && (
                  <span className="shrink-0 rounded border border-border bg-bg-input px-1.5 py-0.5 text-[8px] font-bold uppercase text-text-dim">Automated</span>
                )}
                <p className="truncate text-[11px] text-text-muted">{crmInboxSummary(item)}</p>
              </div>
              <p className="mt-1 truncate text-[10px] text-text-dim" title={`Latest message · ${messageRecipientSummary(item.last_message_addresses)}`}>
                Latest · {messageRecipientSummary(item.last_message_addresses)}
              </p>
            </a>
          ))}
          {total > items.length && headerLink && (
            <a href={headerLink} className="block px-4 py-2.5 text-center text-[10px] text-text-muted hover:bg-bg-hover hover:text-text">
              {total - items.length} more conversation{total - items.length === 1 ? "" : "s"} →
            </a>
          )}
        </div>
      )}
    </section>
  );
}
