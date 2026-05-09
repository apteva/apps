// CampaignsPanel — bulk-send orchestrator UI.
//
// Three views, switched by selection state:
//   • list view (left rail) — every campaign, with status + audience
//     summary
//   • detail view (right pane) — selected campaign, with progress
//     bar + recipient table + lifecycle buttons
//   • editor — modal-style form for create + edit
//
// Talks to /api/apps/campaigns/* directly. Pulls audience options
// (lists + segments) from /api/apps/crm/* for the editor's audience
// picker.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Campaign {
  id: number;
  name: string;
  description?: string;
  status: string;
  channel: string;
  sender_address?: string;
  subject?: string;
  body_text?: string;
  body_html?: string;
  template_name?: string;
  list_id?: number | null;
  segment_id?: number | null;
  schedule_kind: string;
  scheduled_at?: string;
  batch_size?: number;
  tick_interval_seconds?: number;
  open_tracking?: boolean;
  click_tracking?: boolean;
  job_ids?: string;
  created_at?: string;
  updated_at?: string;
  started_at?: string;
  completed_at?: string;
  archived_at?: string;
  error?: string;
  stats?: Record<string, number>;
}

interface Recipient {
  id: number;
  campaign_id: number;
  contact_id: number;
  address: string;
  status: string;
  messaging_id?: number;
  attempt_count: number;
  sent_at?: string;
  error?: string;
}

interface List { id: number; name: string; slug: string; default_sender_email?: string; default_sender_phone?: string; }
interface Segment { id: number; name: string; kind: string; cached_count?: number; list_id?: number | null; }

const API = "/api/apps/campaigns";
const CRM_API = "/api/apps/crm";

const STATUS_COLORS: Record<string, string> = {
  draft: "border-border text-text-muted",
  scheduled: "border-accent/40 text-accent",
  materialising: "border-amber/40 text-amber",
  sending: "border-amber/40 text-amber",
  paused: "border-amber/40 text-amber",
  sent: "border-accent/60 text-accent bg-accent/10",
  cancelled: "border-red/40 text-red",
  failed: "border-red/40 text-red",
};

function fmt(s: string | undefined) {
  if (!s) return "";
  try { return new Date(s).toLocaleString(); } catch { return s; }
}

export default function CampaignsPanel({ projectId, installId }: NativePanelProps) {
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<Campaign | null>(null);
  const [recipients, setRecipients] = useState<Recipient[]>([]);
  const [recipientFilter, setRecipientFilter] = useState<string>("");
  const [editorOpen, setEditorOpen] = useState<"new" | number | null>(null);
  const [scheduleOpen, setScheduleOpen] = useState(false);
  const [testOpen, setTestOpen] = useState(false);
  const [confirmDialog, setConfirmDialog] = useState<{ title: string; message: string; confirmLabel: string; destructive?: boolean; onConfirm: () => void | Promise<void> } | null>(null);
  const [errorToast, setErrorToast] = useState<string | null>(null);
  const [lists, setLists] = useState<List[]>([]);
  const [segments, setSegments] = useState<Segment[]>([]);
  const [status, setStatus] = useState("");

  useEffect(() => {
    if (!errorToast) return;
    const id = window.setTimeout(() => setErrorToast(null), 5000);
    return () => window.clearTimeout(id);
  }, [errorToast]);

  const params = useCallback((extra: Record<string, string> = {}) =>
    new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString(),
    [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, body?: unknown, prefix: string = API, query: Record<string, string> = {}): Promise<T> => {
    const res = await fetch(`${prefix}${path}?${params(query)}`, {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [params]);

  const loadList = useCallback(async () => {
    setStatus("Loading…");
    try {
      const r = await api<{ campaigns?: Campaign[] }>("GET", "/campaigns");
      setCampaigns(r.campaigns || []);
      setStatus(`${(r.campaigns || []).length} campaign${(r.campaigns || []).length === 1 ? "" : "s"}`);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [api]);

  const loadAudienceOptions = useCallback(async () => {
    try {
      const [ls, sg] = await Promise.all([
        api<{ lists?: List[] }>("GET", "/lists", undefined, CRM_API),
        api<{ segments?: Segment[] }>("GET", "/segments", undefined, CRM_API),
      ]);
      setLists(ls.lists || []);
      setSegments(sg.segments || []);
    } catch (e) {
      // CRM might not be installed; the audience picker handles empty.
    }
  }, [api]);

  useEffect(() => { loadList(); }, [loadList]);
  useEffect(() => { loadAudienceOptions(); }, [loadAudienceOptions]);

  useAppEvents("campaigns", projectId, (ev) => {
    if (ev.topic.startsWith("campaign.")) {
      loadList();
      if (selectedId) loadDetail(selectedId);
    }
  });

  const loadDetail = useCallback(async (id: number) => {
    try {
      const [c, rs] = await Promise.all([
        api<{ campaign: Campaign }>("GET", `/campaigns/${id}`),
        api<{ recipients?: Recipient[] }>("GET", `/campaigns/${id}/recipients`, undefined, API, recipientFilter ? { status: recipientFilter, limit: "100" } : { limit: "100" }),
      ]);
      setDetail(c.campaign);
      setRecipients(rs.recipients || []);
    } catch (e) {
      setErrorToast("Detail error: " + (e as Error).message);
    }
  }, [api, recipientFilter]);

  const select = (id: number) => { setSelectedId(id); loadDetail(id); };

  // Reload recipients when the filter changes.
  useEffect(() => {
    if (selectedId) loadDetail(selectedId);
  }, [recipientFilter, selectedId, loadDetail]);

  // ─── Lifecycle handlers ───────────────────────────────────────

  const lifecycleAction = async (id: number, action: string, body?: unknown) => {
    try {
      await api("POST", `/campaigns/${id}/${action}`, body || {});
      await loadList();
      if (selectedId === id) await loadDetail(id);
    } catch (e) {
      setErrorToast(`${action} failed: ` + (e as Error).message);
    }
  };

  const archive = (c: Campaign) => setConfirmDialog({
    title: "Archive campaign",
    message: `Archive "${c.name}"? The recipient rows are kept; the row just stops appearing.`,
    confirmLabel: "Archive",
    destructive: true,
    onConfirm: async () => {
      try {
        await api("DELETE", `/campaigns/${c.id}`);
        await loadList();
        if (selectedId === c.id) { setSelectedId(null); setDetail(null); }
      } catch (e) {
        setErrorToast("Archive failed: " + (e as Error).message);
      }
    },
  });

  const cancelCampaign = (c: Campaign) => setConfirmDialog({
    title: "Cancel campaign",
    message: `Cancel "${c.name}"? Pending recipients will be marked 'skipped' and the tick job will stop.`,
    confirmLabel: "Cancel campaign",
    destructive: true,
    onConfirm: () => lifecycleAction(c.id, "cancel"),
  });

  const handleSaveCampaign = async (which: "new" | number, body: Partial<Campaign>) => {
    try {
      if (which === "new") {
        const r = await api<{ campaign: Campaign }>("POST", "/campaigns", body);
        await loadList();
        select(r.campaign.id);
      } else {
        await api("PATCH", `/campaigns/${which}`, body);
        await loadList();
        if (selectedId === which) await loadDetail(which);
      }
      setEditorOpen(null);
    } catch (e) {
      setErrorToast("Save failed: " + (e as Error).message);
    }
  };

  // ─── Render ───────────────────────────────────────────────────

  return (
    <div className="h-full flex">
      {/* Left rail */}
      <aside className="w-80 border-r border-border flex flex-col">
        <div className="p-3 border-b border-border flex items-center gap-2">
          <h1 className="text-sm text-text font-semibold flex-1">Campaigns</h1>
          <button
            type="button"
            onClick={() => setEditorOpen("new")}
            className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >+ New</button>
        </div>
        <div className="flex-1 overflow-auto">
          {campaigns.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">
              No campaigns yet. Create one targeting a CRM segment to send a broadcast.
            </div>
          ) : (
            <ul>
              {campaigns.map((c) => (
                <li
                  key={c.id}
                  onClick={() => select(c.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    c.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center gap-2">
                    <span className="text-sm text-text font-medium truncate flex-1">{c.name}</span>
                    <StatusPill status={c.status} />
                  </div>
                  <div className="text-xs text-text-muted truncate">
                    {c.channel} · {c.scheduled_at ? `scheduled ${fmt(c.scheduled_at)}` : (c.completed_at ? `sent ${fmt(c.completed_at)}` : "draft")}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="p-2 text-xs text-text-dim border-t border-border">{status}</div>
      </aside>

      {/* Detail */}
      <main className="flex-1 overflow-auto p-6">
        {!detail ? (
          <div className="text-text-muted text-sm text-center mt-12">
            {selectedId ? "Loading…" : "Pick a campaign to see details, or create a new one."}
          </div>
        ) : (
          <div className="max-w-3xl space-y-6">
            <header className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <h1 className="text-xl text-text font-semibold flex items-center gap-2">
                  {detail.name}
                  <StatusPill status={detail.status} />
                </h1>
                <p className="text-text-muted text-sm">
                  {detail.channel}
                  {detail.scheduled_at ? ` · scheduled ${fmt(detail.scheduled_at)}` : ""}
                  {detail.started_at ? ` · started ${fmt(detail.started_at)}` : ""}
                  {detail.completed_at ? ` · completed ${fmt(detail.completed_at)}` : ""}
                </p>
                {detail.error && (
                  <p className="text-red text-xs mt-1">⚠ {detail.error}</p>
                )}
              </div>
              <div className="flex flex-wrap gap-2 justify-end">
                <CampaignActions
                  c={detail}
                  onEdit={() => setEditorOpen(detail.id)}
                  onClone={() => lifecycleAction(detail.id, "clone")}
                  onSchedule={() => setScheduleOpen(true)}
                  onStartNow={() => lifecycleAction(detail.id, "start_now")}
                  onPause={() => lifecycleAction(detail.id, "pause")}
                  onResume={() => lifecycleAction(detail.id, "resume")}
                  onCancel={() => cancelCampaign(detail)}
                  onSendTest={() => setTestOpen(true)}
                  onArchive={() => archive(detail)}
                />
              </div>
            </header>

            <StatsCard stats={detail.stats || {}} />

            <section>
              <div className="flex items-center justify-between mb-2">
                <h2 className="text-xs uppercase tracking-wide text-text-dim">Recipients ({recipients.length})</h2>
                <select
                  value={recipientFilter}
                  onChange={(e) => setRecipientFilter(e.target.value)}
                  className="bg-bg-input border border-border rounded text-xs px-1.5 py-0.5"
                >
                  <option value="">all</option>
                  {["pending", "sending", "sent", "delivered", "bounced", "complained", "failed", "skipped", "unsubscribed"].map((s) => (
                    <option key={s} value={s}>{s}</option>
                  ))}
                </select>
              </div>
              {recipients.length === 0 ? (
                <p className="text-text-muted text-sm">No recipients in this view.</p>
              ) : (
                <ul className="border border-border rounded divide-y divide-border text-sm">
                  {recipients.map((r) => (
                    <li key={r.id} className="px-2 py-1 flex items-center gap-2">
                      <span className={`text-[10px] px-1.5 py-0.5 rounded ${recipientStatusClass(r.status)}`}>{r.status}</span>
                      <span className="text-text truncate flex-1">{r.address}</span>
                      {r.attempt_count > 1 && (
                        <span className="text-[10px] text-text-dim">attempt {r.attempt_count}</span>
                      )}
                      {r.error && (
                        <span className="text-[10px] text-red truncate" title={r.error}>{r.error}</span>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Content</h2>
              <div className="border border-border rounded p-3 space-y-2 text-sm">
                <div><span className="text-text-muted">From: </span><span className="text-text">{detail.sender_address || <em className="text-text-dim">install / list default</em>}</span></div>
                {detail.channel === "email" && detail.subject && <div><span className="text-text-muted">Subject: </span><span className="text-text">{detail.subject}</span></div>}
                {detail.body_text && <pre className="whitespace-pre-wrap font-mono text-xs text-text bg-bg-input/50 rounded p-2 max-h-60 overflow-auto">{detail.body_text}</pre>}
                {detail.body_html && (
                  <details>
                    <summary className="text-text-muted text-xs cursor-pointer">HTML body</summary>
                    <pre className="whitespace-pre-wrap font-mono text-xs text-text bg-bg-input/50 rounded p-2 max-h-60 overflow-auto mt-1">{detail.body_html}</pre>
                  </details>
                )}
              </div>
            </section>

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Audience</h2>
              <div className="border border-border rounded p-3 text-sm">
                {detail.segment_id != null && (
                  <div>
                    <span className="text-text-muted">Segment: </span>
                    <span className="text-text">{segments.find((s) => s.id === detail.segment_id)?.name || `#${detail.segment_id}`}</span>
                  </div>
                )}
                {detail.list_id != null && (
                  <div>
                    <span className="text-text-muted">List: </span>
                    <span className="text-text">{lists.find((l) => l.id === detail.list_id)?.name || `#${detail.list_id}`}</span>
                  </div>
                )}
                {detail.segment_id == null && detail.list_id == null && (
                  <span className="text-text-dim">No audience selected — set one before scheduling.</span>
                )}
              </div>
            </section>
          </div>
        )}
      </main>

      {editorOpen !== null && (
        <CampaignEditorModal
          editing={editorOpen === "new" ? null : (campaigns.find((c) => c.id === editorOpen) || null)}
          lists={lists}
          segments={segments}
          onCancel={() => setEditorOpen(null)}
          onSubmit={(body) => handleSaveCampaign(editorOpen, body)}
        />
      )}

      {scheduleOpen && detail && (
        <ScheduleModal
          campaign={detail}
          onCancel={() => setScheduleOpen(false)}
          onSubmit={async (when) => {
            await lifecycleAction(detail.id, "schedule", { scheduled_at: when });
            setScheduleOpen(false);
          }}
        />
      )}

      {testOpen && detail && (
        <SendTestModal
          campaign={detail}
          onCancel={() => setTestOpen(false)}
          onSubmit={async (contactID) => {
            await lifecycleAction(detail.id, "send_test", { contact_id: contactID });
            setTestOpen(false);
          }}
        />
      )}

      {confirmDialog && (
        <ConfirmDialog
          state={confirmDialog}
          onClose={() => setConfirmDialog(null)}
        />
      )}

      {errorToast && (
        <ErrorToast message={errorToast} onDismiss={() => setErrorToast(null)} />
      )}
    </div>
  );
}

// ─── Status pill + per-recipient class ─────────────────────────────

function StatusPill({ status }: { status: string }) {
  const cls = STATUS_COLORS[status] || "border-border text-text-muted";
  return <span className={`text-[10px] uppercase px-1.5 py-0.5 rounded border ${cls}`}>{status}</span>;
}

function recipientStatusClass(s: string) {
  if (s === "sent" || s === "delivered") return "bg-accent/10 text-accent";
  if (s === "bounced" || s === "complained" || s === "failed") return "bg-red/15 text-red";
  if (s === "unsubscribed") return "bg-amber/15 text-amber";
  if (s === "skipped") return "bg-border text-text-dim";
  if (s === "sending") return "bg-amber/15 text-amber";
  return "bg-border text-text-muted";
}

// ─── Stats card ────────────────────────────────────────────────────

function StatsCard({ stats }: { stats: Record<string, number> }) {
  const total = useMemo(() => Object.values(stats).reduce((a, b) => a + b, 0), [stats]);
  if (total === 0) {
    return (
      <div className="border border-border rounded p-3 text-sm text-text-muted">
        No recipients yet — schedule the campaign or run materialise to populate.
      </div>
    );
  }
  const sent = (stats.sent || 0) + (stats.delivered || 0);
  const bounced = (stats.bounced || 0) + (stats.complained || 0);
  const failed = stats.failed || 0;
  const pending = (stats.pending || 0) + (stats.sending || 0);
  const skipped = (stats.skipped || 0);
  const unsubbed = (stats.unsubscribed || 0);
  return (
    <section className="border border-border rounded p-3">
      <div className="grid grid-cols-3 gap-2 text-center text-sm">
        <Stat label="sent" value={sent} />
        <Stat label="bounced/spam" value={bounced} accent="red" />
        <Stat label="failed" value={failed} accent="red" />
        <Stat label="pending" value={pending} accent="amber" />
        <Stat label="skipped" value={skipped} />
        <Stat label="unsubscribed" value={unsubbed} accent="amber" />
      </div>
      <div className="mt-3 h-1.5 bg-bg-input rounded overflow-hidden flex">
        <div className="bg-accent" style={{ width: `${(sent / total) * 100}%` }} />
        <div className="bg-red" style={{ width: `${(bounced / total) * 100}%` }} />
        <div className="bg-amber" style={{ width: `${(pending / total) * 100}%` }} />
      </div>
      <div className="mt-1 text-xs text-text-dim text-right">{total} total recipient{total === 1 ? "" : "s"}</div>
    </section>
  );
}

function Stat({ label, value, accent }: { label: string; value: number; accent?: "red" | "amber" }) {
  const cls = accent === "red" ? "text-red" : accent === "amber" ? "text-amber" : "text-text";
  return (
    <div>
      <div className={`text-lg font-semibold ${cls}`}>{value}</div>
      <div className="text-[10px] uppercase text-text-dim tracking-wide">{label}</div>
    </div>
  );
}

// ─── Campaign actions ──────────────────────────────────────────────

function CampaignActions({ c, onEdit, onClone, onSchedule, onStartNow, onPause, onResume, onCancel, onSendTest, onArchive }: {
  c: Campaign;
  onEdit: () => void;
  onClone: () => void;
  onSchedule: () => void;
  onStartNow: () => void;
  onPause: () => void;
  onResume: () => void;
  onCancel: () => void;
  onSendTest: () => void;
  onArchive: () => void;
}) {
  const editable = c.status === "draft" || c.status === "paused";
  const cancellable = c.status !== "sent" && c.status !== "cancelled" && c.status !== "draft";
  const startable = c.status === "draft" || c.status === "scheduled";
  return (
    <>
      {editable && <ActBtn onClick={onEdit}>Edit</ActBtn>}
      <ActBtn onClick={onClone}>Clone</ActBtn>
      <ActBtn onClick={onSendTest}>Test send</ActBtn>
      {c.status === "draft" && <ActBtn onClick={onSchedule}>Schedule</ActBtn>}
      {startable && <ActBtn primary onClick={onStartNow}>Start now</ActBtn>}
      {c.status === "sending" && <ActBtn onClick={onPause}>Pause</ActBtn>}
      {c.status === "paused" && <ActBtn primary onClick={onResume}>Resume</ActBtn>}
      {cancellable && <ActBtn destructive onClick={onCancel}>Cancel</ActBtn>}
      <ActBtn destructive onClick={onArchive}>Archive</ActBtn>
    </>
  );
}

function ActBtn({ children, onClick, primary, destructive }: { children: React.ReactNode; onClick: () => void; primary?: boolean; destructive?: boolean }) {
  const cls = primary
    ? "border-accent text-accent hover:bg-accent hover:text-bg"
    : destructive
    ? "border-red/50 text-red hover:bg-red/10"
    : "border-border text-text-muted hover:bg-bg-input";
  return (
    <button type="button" onClick={onClick} className={`text-xs px-2 py-1 border rounded ${cls}`}>{children}</button>
  );
}

// ─── Editor modal ──────────────────────────────────────────────────

function CampaignEditorModal({ editing, lists, segments, onCancel, onSubmit }: {
  editing?: Campaign | null;
  lists: List[];
  segments: Segment[];
  onCancel: () => void;
  onSubmit: (body: Partial<Campaign>) => void | Promise<void>;
}) {
  const isEdit = !!editing;
  const [name, setName] = useState(editing?.name || "");
  const [description, setDescription] = useState(editing?.description || "");
  const [channel, setChannel] = useState<string>(editing?.channel || "email");
  const [sender, setSender] = useState(editing?.sender_address || "");
  const [subject, setSubject] = useState(editing?.subject || "");
  const [body, setBody] = useState(editing?.body_text || "");
  const [bodyHTML, setBodyHTML] = useState(editing?.body_html || "");
  const [audienceKind, setAudienceKind] = useState<"segment" | "list">(editing?.segment_id ? "segment" : "list");
  const [segmentID, setSegmentID] = useState<number | "">(editing?.segment_id ?? "");
  const [listID, setListID] = useState<number | "">(editing?.list_id ?? "");
  const [batchSize, setBatchSize] = useState<string>(editing?.batch_size ? String(editing.batch_size) : "");
  const [tickInterval, setTickInterval] = useState<string>(editing?.tick_interval_seconds ? String(editing.tick_interval_seconds) : "");
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const payload: Partial<Campaign> = {
        name: name.trim(),
        description: description.trim(),
        channel,
        sender_address: sender.trim(),
        subject: subject.trim(),
        body_text: body,
        body_html: bodyHTML.trim(),
        list_id: audienceKind === "list" && listID ? Number(listID) : null,
        segment_id: audienceKind === "segment" && segmentID ? Number(segmentID) : null,
      };
      if (batchSize) payload.batch_size = Number(batchSize);
      if (tickInterval) payload.tick_interval_seconds = Number(tickInterval);
      await onSubmit(payload);
    } finally { setBusy(false); }
  };

  return (
    <ModalShell
      title={isEdit ? `Edit — ${editing!.name}` : "New campaign"}
      onCancel={onCancel}
      footer={
        <>
          <button
            type="button"
            onClick={submit}
            disabled={busy || !name.trim()}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >{busy ? "Saving…" : (isEdit ? "Save" : "Create")}</button>
          <button type="button" onClick={onCancel} disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
            Cancel
          </button>
        </>
      }
    >
      <div className="grid grid-cols-[120px_1fr] gap-2 items-center">
        <label className="text-text-muted">Name</label>
        <input type="text" value={name} onChange={(e) => setName(e.target.value)} autoFocus
          className="bg-bg-input border border-border rounded px-2 py-1" placeholder="July product update" />
        <label className="text-text-muted">Description</label>
        <input type="text" value={description} onChange={(e) => setDescription(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1" />
        <label className="text-text-muted">Channel</label>
        <select value={channel} onChange={(e) => setChannel(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1">
          {["email", "sms", "whatsapp"].map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
        <label className="text-text-muted">From</label>
        <input type="text" value={sender} onChange={(e) => setSender(e.target.value)}
          placeholder={channel === "email" ? "hello@example.com" : "+15551234567"}
          className="bg-bg-input border border-border rounded px-2 py-1" />
        {channel === "email" && (
          <>
            <label className="text-text-muted">Subject</label>
            <input type="text" value={subject} onChange={(e) => setSubject(e.target.value)}
              className="bg-bg-input border border-border rounded px-2 py-1" />
          </>
        )}
        <label className="text-text-muted self-start mt-1">Body</label>
        <textarea value={body} onChange={(e) => setBody(e.target.value)} rows={6}
          className="bg-bg-input border border-border rounded px-2 py-1 font-mono text-xs" />
        {channel === "email" && (
          <>
            <label className="text-text-muted self-start mt-1">HTML body</label>
            <textarea value={bodyHTML} onChange={(e) => setBodyHTML(e.target.value)} rows={4}
              placeholder="optional"
              className="bg-bg-input border border-border rounded px-2 py-1 font-mono text-xs" />
          </>
        )}
      </div>
      <hr className="border-border my-3" />
      <div className="space-y-2">
        <h3 className="text-xs uppercase tracking-wide text-text-dim">Audience</h3>
        <div className="flex items-center gap-2 text-xs">
          <label className="flex items-center gap-1.5">
            <input type="radio" checked={audienceKind === "segment"} onChange={() => setAudienceKind("segment")} /> Segment
          </label>
          <label className="flex items-center gap-1.5">
            <input type="radio" checked={audienceKind === "list"} onChange={() => setAudienceKind("list")} /> List
          </label>
        </div>
        {audienceKind === "segment" ? (
          <select value={segmentID === "" ? "" : String(segmentID)}
            onChange={(e) => setSegmentID(e.target.value === "" ? "" : Number(e.target.value))}
            className="bg-bg-input border border-border rounded px-2 py-1 w-full text-sm">
            <option value="">— pick a segment —</option>
            {segments.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} ({s.kind}{typeof s.cached_count === "number" ? `, ~${s.cached_count}` : ""})
              </option>
            ))}
          </select>
        ) : (
          <select value={listID === "" ? "" : String(listID)}
            onChange={(e) => setListID(e.target.value === "" ? "" : Number(e.target.value))}
            className="bg-bg-input border border-border rounded px-2 py-1 w-full text-sm">
            <option value="">— pick a list —</option>
            {lists.map((l) => (
              <option key={l.id} value={l.id}>{l.name}</option>
            ))}
          </select>
        )}
      </div>
      <hr className="border-border my-3" />
      <div className="grid grid-cols-[120px_1fr] gap-2 items-center text-xs">
        <label className="text-text-muted">Batch size</label>
        <input type="number" value={batchSize} onChange={(e) => setBatchSize(e.target.value)}
          placeholder="default 100" className="bg-bg-input border border-border rounded px-2 py-1" />
        <label className="text-text-muted">Tick interval (s)</label>
        <input type="number" value={tickInterval} onChange={(e) => setTickInterval(e.target.value)}
          placeholder="default 60" className="bg-bg-input border border-border rounded px-2 py-1" />
      </div>
    </ModalShell>
  );
}

// ─── Schedule modal ───────────────────────────────────────────────

function ScheduleModal({ campaign, onCancel, onSubmit }: {
  campaign: Campaign;
  onCancel: () => void;
  onSubmit: (when: string) => void | Promise<void>;
}) {
  const def = useMemo(() => {
    // Default to 5 minutes from now, rounded up to the next minute.
    const t = new Date(Date.now() + 5 * 60 * 1000);
    t.setSeconds(0, 0);
    return new Date(t.getTime() - t.getTimezoneOffset() * 60_000).toISOString().slice(0, 16);
  }, []);
  const [when, setWhen] = useState(def);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    if (!when) return;
    setBusy(true);
    try {
      await onSubmit(new Date(when).toISOString());
    } finally { setBusy(false); }
  };
  return (
    <ModalShell
      title={`Schedule "${campaign.name}"`}
      onCancel={onCancel}
      footer={
        <>
          <button type="button" onClick={submit} disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {busy ? "Scheduling…" : "Schedule"}
          </button>
          <button type="button" onClick={onCancel} disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
            Cancel
          </button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-20">Send at</label>
        <input type="datetime-local" value={when} onChange={(e) => setWhen(e.target.value)} autoFocus
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1" />
      </div>
      <p className="text-text-dim text-xs">
        Jobs will fire materialise at this time, then drive the tick loop until every recipient is sent.
      </p>
    </ModalShell>
  );
}

// ─── Send-test modal ──────────────────────────────────────────────

function SendTestModal({ campaign, onCancel, onSubmit }: {
  campaign: Campaign;
  onCancel: () => void;
  onSubmit: (contactID: number) => void | Promise<void>;
}) {
  const [contactID, setContactID] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    const n = Number(contactID);
    if (!n) return;
    setBusy(true);
    try { await onSubmit(n); }
    finally { setBusy(false); }
  };
  return (
    <ModalShell
      title={`Test send — ${campaign.name}`}
      onCancel={onCancel}
      footer={
        <>
          <button type="button" onClick={submit} disabled={busy || !contactID}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {busy ? "Sending…" : "Send test"}
          </button>
          <button type="button" onClick={onCancel} disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">
            Cancel
          </button>
        </>
      }
    >
      <div className="flex items-center gap-2">
        <label className="text-text-muted w-24">Contact id</label>
        <input type="number" value={contactID} onChange={(e) => setContactID(e.target.value)} autoFocus
          placeholder="42"
          className="flex-1 bg-bg-input border border-border rounded px-2 py-1 font-mono" />
      </div>
      <p className="text-text-dim text-xs">
        The test is logged in CRM as <code>{campaign.channel}_test_sent</code>. The recipient sees the same body as the real campaign.
      </p>
    </ModalShell>
  );
}

// ─── Generic modal shell + confirm dialog + toast ─────────────────

function ModalShell({ title, onCancel, children, footer }: {
  title: string;
  onCancel: () => void;
  children: React.ReactNode;
  footer: React.ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onCancel(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);
  return (
    <div className="absolute inset-0 bg-black/40 flex items-center justify-center pointer-events-auto z-10">
      <div className="bg-bg border border-border rounded-lg shadow-lg w-full max-w-2xl mx-4">
        <header className="flex items-center justify-between px-4 py-2 border-b border-border">
          <div className="text-sm text-text font-medium">{title}</div>
          <button type="button" onClick={onCancel} className="text-text-dim hover:text-text px-2">×</button>
        </header>
        <div className="p-4 space-y-3 text-sm max-h-[70vh] overflow-auto">{children}</div>
        <footer className="flex items-center gap-2 px-4 py-3 border-t border-border">{footer}</footer>
      </div>
    </div>
  );
}

interface ConfirmState { title: string; message: string; confirmLabel: string; destructive?: boolean; onConfirm: () => void | Promise<void>; }
function ConfirmDialog({ state, onClose }: { state: ConfirmState; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const handleConfirm = async () => {
    setBusy(true);
    try { await state.onConfirm(); }
    finally { setBusy(false); onClose(); }
  };
  return (
    <ModalShell title={state.title} onCancel={onClose}
      footer={
        <>
          <button type="button" onClick={handleConfirm} disabled={busy}
            className={`px-3 py-1 text-sm rounded disabled:opacity-50 ${state.destructive ? "border border-red/50 text-red hover:bg-red/10" : "border border-accent text-accent hover:bg-accent hover:text-bg"}`}>
            {busy ? "Working…" : state.confirmLabel}
          </button>
          <button type="button" onClick={onClose} disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">Cancel</button>
        </>
      }>
      <p className="text-text">{state.message}</p>
    </ModalShell>
  );
}

function ErrorToast({ message, onDismiss }: { message: string; onDismiss: () => void }) {
  return (
    <div className="absolute bottom-4 right-4 z-20 max-w-sm border border-red/50 bg-red/10 text-text rounded shadow-lg flex items-start gap-2 p-3 text-sm">
      <span className="text-red shrink-0">⚠</span>
      <span className="flex-1 break-words">{message}</span>
      <button type="button" onClick={onDismiss} className="text-text-dim hover:text-text shrink-0">×</button>
    </div>
  );
}
