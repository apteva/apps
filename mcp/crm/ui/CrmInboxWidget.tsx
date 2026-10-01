import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  crmInboxAPIURL,
  crmInboxContactLabel,
  crmInboxPageLink,
  crmInboxPreferences,
  crmInboxPreview,
  crmInboxRelativeTime,
  type CRMInboxHostProps,
  type InboxItem,
  type InboxResponse,
} from "./inbox";

const priorityStyles: Record<string, string> = {
  urgent: "bg-red/15 text-red border-red/30",
  high: "bg-yellow/15 text-yellow border-yellow/30",
  normal: "bg-bg-input text-text-muted border-border",
  low: "bg-bg-input text-text-dim border-border",
};

const channelStyles: Record<string, string> = {
  email: "bg-blue/10 text-blue",
  sms: "bg-accent/10 text-accent",
  whatsapp: "bg-green/10 text-green",
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

export default function CrmInboxWidget(props: CRMInboxHostProps) {
  const preferences = useMemo(() => crmInboxPreferences(props.widgetSettings), [props.widgetSettings]);
  const [items, setItems] = useState<InboxItem[] | null>(null);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    if (!props.projectId) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setError("");
    try {
      const response = await readJSON<InboxResponse>(crmInboxAPIURL(props, preferences), controller.signal);
      if (request.current !== controller) return;
      setItems(response.inbox || []);
      setTotal(typeof response.total === "number" ? response.total : (response.inbox || []).length);
    } catch (cause) {
      if (controller.signal.aborted || request.current !== controller) return;
      setError((cause as Error).message);
      setItems([]);
      setTotal(0);
    }
  }, [props.appName, props.installId, props.projectId, preferences]);

  useEffect(() => {
    void load();
    return () => request.current?.abort();
  }, [load, props.eventRevision]);

  useEffect(() => {
    const refresh = window.setInterval(() => void load(), 60000);
    return () => window.clearInterval(refresh);
  }, [load]);

  if (!props.projectId) {
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
          </p>
        </div>
        <a href={crmInboxPageLink(props)} className="shrink-0 pt-0.5 text-[11px] text-text-muted hover:text-text">View inbox →</a>
      </header>

      {error ? (
        <div className="flex min-h-28 flex-1 flex-col items-start justify-center gap-2 p-4" role="alert">
          <p className="text-xs text-red">{error}</p>
          <button type="button" onClick={() => void load()} className="rounded border border-border px-2 py-1 text-[10px] text-text-muted hover:bg-bg-hover">Retry</button>
        </div>
      ) : items === null ? (
        <p className="flex min-h-28 flex-1 items-center p-4 text-xs text-text-dim">Loading customer inbox…</p>
      ) : items.length === 0 ? (
        <div className="flex min-h-28 flex-1 flex-col justify-center p-4">
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
                <span className={`shrink-0 rounded px-1.5 py-0.5 text-[8px] font-bold uppercase ${channelStyles[item.channel] || "bg-bg-input text-text-muted"}`}>
                  {channelLabel(item.channel)}
                </span>
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
                <p className="truncate text-[11px] text-text-muted">
                  {item.subject ? `${item.subject} · ` : ""}{crmInboxPreview(item)}
                </p>
              </div>
            </a>
          ))}
          {total > items.length && (
            <a href={crmInboxPageLink(props)} className="block px-4 py-2.5 text-center text-[10px] text-text-muted hover:bg-bg-hover hover:text-text">
              {total - items.length} more conversation{total - items.length === 1 ? "" : "s"} →
            </a>
          )}
        </div>
      )}
    </section>
  );
}
