// GigQueueWidget — dashboard.home widget for the gigs app.
//
// Leads with the operator's standing question: what needs me, and what is at
// risk? Three sections: Needs review (submitted — the operator is the
// blocker), In flight (offered/accepted with deadline urgency), and Recent
// outcomes (collapsed by default via settings). Half size shows counts plus
// the three most urgent rows; full size shows every enabled section.

import { useEffect, useMemo, useState } from "react";

interface HostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  preview?: boolean;
  widgetId?: string;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

interface GigRow {
  id: number;
  title: string;
  status: string;
  deadline_at?: string;
  completed_at?: string;
  created_at?: string;
}

const PILL: Record<string, string> = {
  default: "bg-bg-input text-text-muted border-border",
  success: "bg-green/10 text-green border-green/30",
  warn: "bg-yellow/10 text-yellow border-yellow/30",
  danger: "bg-red/10 text-red border-red/30",
  info: "bg-accent/10 text-accent border-accent/30",
};

const STATUS_TONE: Record<string, string> = {
  submitted: "info",
  offered: "default",
  accepted: "default",
  reviewed: "success",
  cancelled: "default",
  expired: "danger",
  open: "default",
};

const PREVIEW_ROWS: Record<string, GigRow[]> = {
  submitted: [
    { id: 1, title: "Aug 20 recording — batch A", status: "submitted", deadline_at: sampleDeadline(6) },
  ],
  inflight: [
    { id: 2, title: "Sep 2 recording", status: "offered", deadline_at: sampleDeadline(20) },
    { id: 3, title: "Interview follow-up", status: "accepted", deadline_at: sampleDeadline(-3) },
  ],
  recent: [
    { id: 4, title: "Aug 12 recording", status: "reviewed", completed_at: sampleDeadline(-48) },
  ],
};

function sampleDeadline(hoursFromNow: number): string {
  return new Date(Date.now() + hoursFromNow * 3600_000).toISOString();
}

function baseURL(props: HostProps, statuses: string): string {
  const name = props.appName || "gigs";
  const query = new URLSearchParams();
  if (props.projectId) query.set("project_id", props.projectId);
  query.set("status", statuses);
  query.set("limit", "50");
  return `/api/apps/${encodeURIComponent(name)}/gigs?${query.toString()}`;
}

async function fetchGigs(props: HostProps, statuses: string): Promise<GigRow[]> {
  const response = await fetch(baseURL(props, statuses), { credentials: "same-origin" });
  const body = await response.json().catch(() => ({}));
  if (!response.ok || (body && typeof body === "object" && "error" in body && body.error)) {
    throw new Error((body as { error?: string })?.error || response.statusText);
  }
  return Array.isArray((body as { gigs?: GigRow[] }).gigs) ? (body as { gigs: GigRow[] }).gigs : [];
}

function deadlineState(row: GigRow): { label: string; tone: string } | null {
  if (!row.deadline_at) return null;
  const due = Date.parse(row.deadline_at);
  if (Number.isNaN(due)) return null;
  const hours = (due - Date.now()) / 3600_000;
  if (hours < 0) return { label: "overdue", tone: "danger" };
  if (hours < 24) return { label: `due in ${Math.max(1, Math.round(hours))}h`, tone: "warn" };
  const days = Math.round(hours / 24);
  return { label: `due in ${days}d`, tone: "default" };
}

function settingsBool(settings: Record<string, unknown> | undefined, key: string, fallback: boolean): boolean {
  const value = settings?.[key];
  return typeof value === "boolean" ? value : fallback;
}

function settingsInt(settings: Record<string, unknown> | undefined, key: string, fallback: number): number {
  const value = Number(settings?.[key]);
  return Number.isFinite(value) && value >= 1 ? Math.floor(value) : fallback;
}

function IconClipboard() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="8" y="2" width="8" height="4" rx="1" />
      <path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2" />
      <path d="m9 14 2 2 4-4" />
    </svg>
  );
}

export default function GigQueueWidget(props: HostProps) {
  const settings = props.widgetSettings;
  const showReview = settingsBool(settings, "show_review", true);
  const showInflight = settingsBool(settings, "show_inflight", true);
  const showRecent = settingsBool(settings, "show_recent", false);
  const recentLimit = settingsInt(settings, "recent_limit", 4);
  const compact = props.widgetSize !== "full";

  const [review, setReview] = useState<GigRow[]>([]);
  const [inflight, setInflight] = useState<GigRow[]>([]);
  const [recent, setRecent] = useState<GigRow[]>([]);
  const [error, setError] = useState("");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    if (props.preview) {
      setReview(PREVIEW_ROWS.submitted);
      setInflight(PREVIEW_ROWS.inflight);
      setRecent(PREVIEW_ROWS.recent);
      setLoaded(true);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const [submitted, live, done] = await Promise.all([
          showReview ? fetchGigs(props, "submitted") : Promise.resolve([]),
          showInflight ? fetchGigs(props, "offered,accepted") : Promise.resolve([]),
          showRecent ? fetchGigs(props, "reviewed,cancelled,expired") : Promise.resolve([]),
        ]);
        if (cancelled) return;
        setReview(submitted);
        setInflight(live);
        setRecent(
          done
            .slice()
            .sort((a, b) => (b.completed_at || "").localeCompare(a.completed_at || ""))
            .slice(0, recentLimit),
        );
        setError("");
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      } finally {
        if (!cancelled) setLoaded(true);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eventRevision bumps when one of the manifest's refresh_topics fires.
  }, [props.projectId, props.installId, props.eventRevision, props.preview, showReview, showInflight, showRecent, recentLimit]);

  const urgent = useMemo(() => {
    const byUrgency = (a: GigRow, b: GigRow) =>
      (a.deadline_at || "9999").localeCompare(b.deadline_at || "9999");
    return [...review].concat([...inflight].sort(byUrgency)).slice(0, 3);
  }, [review, inflight]);

  const empty = loaded && !error && review.length === 0 && inflight.length === 0 && recent.length === 0;

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card">
      <header className="flex items-center gap-2 border-b border-border px-4 py-3">
        <span className="text-text-muted"><IconClipboard /></span>
        <div className="min-w-0">
          <h2 className="text-sm font-bold text-text">Gigs</h2>
          <p className="mt-0.5 text-[10px] text-text-dim">Submissions awaiting review and deadlines at risk</p>
        </div>
        <div className="ml-auto flex items-center gap-1.5">
          {showReview && <CountPill label="review" count={review.length} tone={review.length ? "info" : "default"} />}
          {showInflight && <CountPill label="live" count={inflight.length} tone="default" />}
        </div>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-2">
        {error && <div className="my-2 rounded border border-red/30 bg-red/10 px-3 py-2 text-xs text-red">{error}</div>}
        {!loaded && !error && <div className="py-4 text-xs text-text-dim">Loading…</div>}
        {empty && <div className="py-4 text-xs text-text-dim">No gigs need attention.</div>}
        {loaded && !error && compact && !empty && <RowList rows={urgent} />}
        {loaded && !error && !compact && !empty && (
          <>
            {showReview && review.length > 0 && <SectionHead label="Needs review" />}
            {showReview && <RowList rows={review} />}
            {showInflight && inflight.length > 0 && <SectionHead label="In flight" />}
            {showInflight && <RowList rows={inflight} />}
            {showRecent && recent.length > 0 && <SectionHead label="Recent outcomes" />}
            {showRecent && <RowList rows={recent} />}
          </>
        )}
      </div>
    </section>
  );
}

function CountPill({ label, count, tone }: { label: string; count: number; tone: string }) {
  return (
    <span className={`inline-flex items-center gap-1 rounded border px-2 py-0.5 text-[10px] ${PILL[tone] || PILL.default}`}>
      <span className="font-bold">{count}</span> {label}
    </span>
  );
}

function SectionHead({ label }: { label: string }) {
  return <div className="mt-2 mb-1 text-[10px] font-bold uppercase tracking-wide text-text-dim">{label}</div>;
}

function RowList({ rows }: { rows: GigRow[] }) {
  return (
    <ul className="flex flex-col">
      {rows.map((row) => {
        const due = row.status === "submitted" || row.status === "offered" || row.status === "accepted"
          ? deadlineState(row)
          : null;
        return (
          <li key={row.id} className="flex items-center gap-2 border-b border-border/50 py-1.5 last:border-b-0">
            <span className={`inline-flex shrink-0 items-center rounded border px-1.5 py-0.5 text-[10px] ${PILL[STATUS_TONE[row.status] || "default"]}`}>
              {row.status}
            </span>
            <span className="min-w-0 flex-1 truncate text-xs text-text" title={row.title}>{row.title}</span>
            {due && (
              <span className={`inline-flex shrink-0 items-center rounded border px-1.5 py-0.5 text-[10px] ${PILL[due.tone]}`}>
                {due.label}
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}
