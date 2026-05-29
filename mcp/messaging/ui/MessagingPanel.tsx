// MessagingPanel — operator UI for the messaging app.
// Tabs: Outbox · Inbox · Templates · Inbound routes · Suppressions.
// Talks to the messaging sidecar via /api/apps/messaging/* (the
// platform proxy injects the per-install bearer token).

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// ─── Inline app-event subscription (copied from StoragePanel) ────
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
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
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
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
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

// ─── Types ────────────────────────────────────────────────────────
interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}
interface MessageRow {
  id: number;
  channel: string;
  direction: "in" | "out";
  from: string;
  to: string[];
  cc: string[];
  bcc?: string[];
  subject?: string;
  body_text?: string;
  body_html?: string;
  status: string;
  status_reason?: string;
  provider_message_id?: string;
  route_status?: string;
  route_target_app?: string;
  route_target_route?: string;
  route_error?: string;
  matched_recipient?: string;
  to_subaddress?: string;
  message_id_header?: string;
  in_reply_to?: string;
  references?: string[];
  created_at?: string;
  sent_at?: string;
  received_at?: string;
  last_event_at?: string;
  event_counts?: Record<string, number>;
}
interface DeliveryEvent {
  id: number;
  message_id: number;
  kind: string;
  recipient?: string;
  reason?: string;
  raw?: unknown;
  occurred_at?: string;
}
interface TemplateRow {
  id: number;
  channel: string;
  name: string;
  subject?: string;
  body_text?: string;
  body_html?: string;
  provider_template_id?: string;
  provider_status?: string;
  var_style?: string;
  last_synced_at?: string;
  created_at?: string;
  updated_at?: string;
}
interface InboundRoute {
  id: number;
  pattern: string;
  target_app: string;
  target_route: string;
  priority: number;
}
interface SuppressionRow {
  channel: string;
  address: string;
  reason: string;
  source: string;
  last_seen?: string;
}
// v0.12: senders + identities split. Identities = DKIM-verified
// domain / WhatsApp Business Account anchors. Never appear in From
// dropdowns; rendered in a separate panel section.
interface IdentityRow {
  id: number;
  kind: string;          // 'email_domain' | 'whatsapp_business_account'
  address: string;
  provider: string;
  verified: boolean;
  verification_status: string;
  dkim_status?: string;
  inbound_bootstrapped: boolean;
  inbound_config?: Record<string, unknown>;
  last_synced_at?: string;
}

interface SenderRow {
  id?: number;
  channel: "email" | "sms" | "whatsapp";
  address: string;
  kind: "email" | "domain" | "phone";
  verified: boolean;
  verification_status?: string;
  dkim_status?: string;
  dkim_tokens?: string[];
  sending_enabled?: boolean;
  // v0.9 local-table additions.
  is_default?: boolean;
  inbound_bootstrapped?: boolean;
  display_name?: string;
  provider?: string;
  last_synced_at?: string;
}
interface ProviderSenderOption {
  channel: "sms" | "whatsapp";
  address: string;
  label?: string;
  kind?: string;
  provider_id?: string;
  status?: string;
}
interface QuotaInfo {
  sandboxed: boolean;
  sending_enabled: boolean;
  production_access: boolean;
  send_quota_24h: number;
  send_rate_per_second: number;
  sent_last_24h: number;
}

const API = "/api/apps/messaging";

type Tab = "outbox" | "inbox" | "templates" | "routes" | "suppressions" | "senders" | "compose";

type Notice = { kind: "error" | "info"; text: string };
type Notify = (kind: Notice["kind"], text: string) => void;
type ConfirmTone = "danger" | "default";
interface ConfirmDialogRequest {
  title: string;
  message: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: ConfirmTone;
  onConfirm: () => void | Promise<void>;
}
type ConfirmFn = (request: ConfirmDialogRequest) => void;
interface ComposeDraft {
  key: number;
  channel: string;
  from: string;
  to: string;
  subject: string;
  body: string;
  in_reply_to?: string;
  references?: string[];
}

// ─── Component ────────────────────────────────────────────────────
export default function MessagingPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("outbox");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  const [outbox, setOutbox] = useState<MessageRow[]>([]);
  const [inbox, setInbox] = useState<MessageRow[]>([]);
  const [templates, setTemplates] = useState<TemplateRow[]>([]);
  const [routes, setRoutes] = useState<InboundRoute[]>([]);
  const [suppressions, setSuppressions] = useState<SuppressionRow[]>([]);
  const [senders, setSenders] = useState<SenderRow[]>([]);
  const [identities, setIdentities] = useState<IdentityRow[]>([]);
  const [quota, setQuota] = useState<QuotaInfo | null>(null);
  // Surface the SES-side failure rather than silently rendering an
  // empty Senders tab — most often this means the email_provider
  // role isn't bound to an aws-ses connection on the install.
  const [sendersError, setSendersError] = useState<string>("");

  // Panel-wide transient notice (errors from handlers + info messages
  // like "copied!"). Replaces native browser dialogs. Auto-dismisses; errors
  // get longer because the operator usually wants to read them.
  const [notice, setNotice] = useState<Notice | null>(null);
  const notify = useCallback<Notify>((kind, text) => setNotice({ kind, text }), []);
  const [confirmDialog, setConfirmDialog] = useState<ConfirmDialogRequest | null>(null);
  const confirmAction = useCallback<ConfirmFn>((request) => setConfirmDialog(request), []);
  useEffect(() => {
    if (!notice) return;
    const ms = notice.kind === "error" ? 8000 : 4000;
    const t = setTimeout(() => setNotice(null), ms);
    return () => clearTimeout(t);
  }, [notice]);

  const [selected, setSelected] = useState<MessageRow | null>(null);
  const [selectedEvents, setSelectedEvents] = useState<DeliveryEvent[]>([]);
  const [composeDraft, setComposeDraft] = useState<ComposeDraft | null>(null);
  const selectedRef = useRef<MessageRow | null>(null);
  useEffect(() => { selectedRef.current = selected; }, [selected]);

  const withParams = useCallback((extra: Record<string, string>) => {
    return new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, params?: Record<string, string>, body?: unknown): Promise<T> => {
    const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
    if (body) {
      (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    const qs = withParams(params || {});
    const res = await fetch(`${API}${path}?${qs}`, opts);
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [withParams]);

  const loadOutbox = useCallback(async () => {
    const r = await api<{ messages: MessageRow[] }>("GET", "/messages", { direction: "out", limit: "100" });
    setOutbox(r.messages || []);
  }, [api]);
  const loadInbox = useCallback(async () => {
    const r = await api<{ messages: MessageRow[] }>("GET", "/messages", { direction: "in", limit: "100" });
    setInbox(r.messages || []);
  }, [api]);
  const loadTemplates = useCallback(async () => {
    const r = await api<{ templates: TemplateRow[] }>("POST", "/tools/call", {}, {
      tool: "template_list",
      args: { limit: 200 },
    });
    setTemplates(r.templates || []);
  }, [api]);
  const loadRoutes = useCallback(async () => {
    const r = await api<{ routes: InboundRoute[] }>("GET", "/inbound-routes", {});
    setRoutes(r.routes || []);
  }, [api]);
  const loadSuppressions = useCallback(async () => {
    const r = await api<{ suppressions: SuppressionRow[] }>("GET", "/suppressions", {});
    setSuppressions(r.suppressions || []);
  }, [api]);
  // Senders + quota call the bound SES integration. We capture the
  // error rather than silently swallowing it — the most common cause
  // is "no email_provider bound", which the operator needs to fix in
  // app settings → integrations.
  const loadSenders = useCallback(async () => {
    try {
      const r = await api<{ senders: SenderRow[] }>("GET", "/senders", {});
      setSenders(r.senders || []);
      setSendersError("");
    } catch (e) {
      setSenders([]);
      setSendersError(parseSendersError((e as Error).message));
    }
  }, [api]);
  // Anchors are the v0.12 split's other half. Soft-fail: empty on
  // error so a hiccup doesn't take down the Senders tab.
  const loadIdentities = useCallback(async () => {
    try {
      const r = await api<{ identities: IdentityRow[] }>("GET", "/identities", {});
      setIdentities(r.identities || []);
    } catch {
      setIdentities([]);
    }
  }, [api]);
  const loadQuota = useCallback(async () => {
    try {
      const q = await api<QuotaInfo>("GET", "/senders/quota", {});
      setQuota(q);
    } catch {
      setQuota(null);
    }
  }, [api]);
  const loadMessageDetail = useCallback(async (id: number) => {
    const r = await api<{ message: MessageRow; events: DeliveryEvent[] }>("GET", `/messages/${id}`, {});
    setSelected(r.message);
    setSelectedEvents(r.events || []);
  }, [api]);

  const reload = useCallback(async () => {
    setBusy(true);
    try {
      await Promise.all([
        loadOutbox(), loadInbox(), loadTemplates(), loadRoutes(),
        loadSuppressions(), loadSenders(), loadIdentities(), loadQuota(),
      ]);
      if (selectedRef.current) {
        await loadMessageDetail(selectedRef.current.id);
      }
      setStatus("");
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [loadOutbox, loadInbox, loadTemplates, loadRoutes, loadSuppressions, loadSenders, loadIdentities, loadQuota, loadMessageDetail]);

  useEffect(() => { reload(); }, [reload]);

  // Live refresh on any messaging event.
  useAppEvents("messaging", projectId, (ev) => {
    if (messagingLiveTopics.has(ev.topic)) {
      reload();
    }
  });

  const openMessage = useCallback(async (m: MessageRow) => {
    setSelected(m);
    setSelectedEvents([]);
    try {
      await loadMessageDetail(m.id);
    } catch {}
  }, [loadMessageDetail]);

  const counts = useMemo(() => ({
    outbox: outbox.length,
    inbox: inbox.length,
    templates: templates.length,
    routes: routes.length,
    suppressions: suppressions.length,
    senders: senders.length,
  }), [outbox, inbox, templates, routes, suppressions, senders]);

  const verifiedSenders = useMemo(() => senders.filter((s) => s.verified), [senders]);

  const replyToMessage = useCallback((m: MessageRow) => {
    const draft = composeDraftFromMessage(m, verifiedSenders);
    if (!draft) {
      notify("error", "Cannot reply to this message because no matching sender was found.");
      return;
    }
    setComposeDraft(draft);
    setSelected(null);
    setTab("compose");
  }, [notify, verifiedSenders]);

  return (
    <div className="h-full flex flex-col">
      {notice && <Toast notice={notice} onDismiss={() => setNotice(null)} />}
      {confirmDialog && (
        <ConfirmDialog
          request={confirmDialog}
          onClose={() => setConfirmDialog(null)}
        />
      )}

      {/* Sandbox banner — only when SES reports we're sandboxed. */}
      {quota && quota.sandboxed && (
        <div className="px-6 py-2 bg-yellow-500/10 border-b border-yellow-500/30 text-xs text-yellow-300">
          <strong>SES sandbox</strong> — only verified recipients receive mail. Sent {quota.sent_last_24h.toFixed(0)}/{quota.send_quota_24h.toFixed(0)} in the last 24h. To lift the limit, request production access in the AWS console.
        </div>
      )}

      {/* Header */}
      <div className="px-6 pt-6 pb-3 flex items-center justify-between gap-4 border-b border-border">
        <div className="flex items-center gap-1 text-sm flex-wrap">
          {([
            ["outbox", `Outbox (${counts.outbox})`],
            ["inbox", `Inbox (${counts.inbox})`],
            ["compose", "Compose"],
            ["senders", `Senders (${counts.senders})`],
            ["templates", `Templates (${counts.templates})`],
            ["routes", `Routes (${counts.routes})`],
            ["suppressions", `Suppressions (${counts.suppressions})`],
          ] as [Tab, string][]).map(([id, label]) => (
            <button
              key={id}
              type="button"
              className={`px-3 py-1.5 rounded ${tab === id ? "bg-surface-2 text-text" : "text-text-dim hover:text-text"}`}
              onClick={() => { if (id === "compose") setComposeDraft(null); setTab(id); setSelected(null); }}
            >{label}</button>
          ))}
        </div>
        <div className="flex items-center gap-2 text-xs text-text-dim">
          {busy && <span>loading…</span>}
          {status && <span className="text-red-500">{status}</span>}
          <button
            type="button"
            className="px-2 py-1 rounded border border-border hover:bg-surface-2"
            onClick={reload}
          >Refresh</button>
        </div>
      </div>

      {/* Body */}
      <div className="flex-1 min-h-0 flex">
        {/* Main pane */}
        <div className="flex-1 min-w-0 overflow-auto">
          {tab === "outbox" && <MessageList rows={outbox} onSelect={openMessage} selectedId={selected?.id} />}
          {tab === "inbox" && <MessageList rows={inbox} onSelect={openMessage} selectedId={selected?.id} />}
          {tab === "compose" && (
            <ComposeView
              api={api}
              senders={verifiedSenders}
              quota={quota}
              draft={composeDraft}
              onSent={() => { setComposeDraft(null); reload(); setTab("outbox"); }}
              gotoSenders={() => setTab("senders")}
            />
          )}
          {tab === "senders" && <SendersView rows={senders} identities={identities} quota={quota} api={api} reload={reload} error={sendersError} notify={notify} confirmAction={confirmAction} />}
          {tab === "templates" && <TemplatesView rows={templates} api={api} reload={reload} notify={notify} confirmAction={confirmAction} />}
          {tab === "routes" && <RoutesView rows={routes} api={api} reload={reload} notify={notify} confirmAction={confirmAction} />}
          {tab === "suppressions" && <SuppressionsView rows={suppressions} api={api} reload={reload} notify={notify} confirmAction={confirmAction} />}
        </div>

        {/* Detail pane (only meaningful for messages) */}
        {(tab === "outbox" || tab === "inbox") && selected && (
          <MessageDetail
            m={selected}
            events={selectedEvents}
            onReply={replyToMessage}
            onClose={() => setSelected(null)}
          />
        )}
      </div>
    </div>
  );
}

// ─── Subviews ─────────────────────────────────────────────────────

function MessageList({ rows, onSelect, selectedId }: { rows: MessageRow[]; onSelect: (m: MessageRow) => void; selectedId?: number }) {
  if (rows.length === 0) {
    return <div className="p-6 text-text-dim text-sm">No messages.</div>;
  }
  return (
    <table className="w-full text-sm">
      <thead className="text-xs text-text-dim">
        <tr className="border-b border-border">
          <th className="text-left px-4 py-2">When</th>
          <th className="text-left px-4 py-2">From / To</th>
          <th className="text-left px-4 py-2">Subject</th>
          <th className="text-left px-4 py-2">Status</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((m) => {
          const counterpart = m.direction === "out" ? m.to.join(", ") : m.from;
          return (
            <tr
              key={m.id}
              className={`border-b border-border cursor-pointer hover:bg-surface-2 ${selectedId === m.id ? "bg-surface-2" : ""}`}
              onClick={() => onSelect(m)}
            >
              <td className="px-4 py-2 text-text-dim">{shortTime(m.last_event_at || m.created_at)}</td>
              <td className="px-4 py-2 truncate max-w-[20rem]">{stripScheme(counterpart)}</td>
              <td className="px-4 py-2 truncate max-w-[24rem]">{m.subject || <span className="text-text-dim">(no subject)</span>}</td>
              <td className="px-4 py-2">
                <div className="flex items-center gap-1 flex-wrap">
                  <StatusPill status={m.direction === "in" ? (m.route_status || m.status) : m.status} />
                </div>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function MessageDetail({ m, events, onReply, onClose }: { m: MessageRow; events: DeliveryEvent[]; onReply: (m: MessageRow) => void; onClose: () => void }) {
  return (
    <div className="w-[28rem] border-l border-border overflow-auto p-5 text-sm">
      <div className="flex items-center justify-between mb-3">
        <h3 className="font-semibold">Message #{m.id}</h3>
        <div className="flex items-center gap-2">
          {m.direction === "in" && (
            <button
              type="button"
              className="px-2 py-1 rounded border border-border text-xs text-text-dim hover:text-text hover:bg-surface-2"
              onClick={() => onReply(m)}
            >Reply</button>
          )}
          <button type="button" className="text-text-dim hover:text-text" onClick={onClose}>×</button>
        </div>
      </div>
      <DL label="From" value={stripScheme(m.from)} />
      <DL label="To" value={m.to.map(stripScheme).join(", ")} />
      {m.cc && m.cc.length > 0 && <DL label="Cc" value={m.cc.map(stripScheme).join(", ")} />}
      <DL label="Subject" value={m.subject || "(no subject)"} />
      <DL label="Status" value={<StatusPill status={m.status} />} />
      {m.status_reason && <DL label="Reason" value={m.status_reason} />}
      {m.direction === "out" && m.provider_message_id && <DL label="SES id" value={<code className="text-xs">{m.provider_message_id}</code>} />}
      {m.direction === "in" && m.route_status && (
        <>
          <DL label="Route" value={
            <span>
              <StatusPill status={m.route_status} />
              {m.route_target_app && <span className="ml-2 text-xs">→ {m.route_target_app}{m.route_target_route}</span>}
            </span>
          } />
          {m.matched_recipient && <DL label="Matched" value={<code className="text-xs">{m.matched_recipient}</code>} />}
          {m.to_subaddress && <DL label="Subaddress" value={<code className="text-xs">{m.to_subaddress}</code>} />}
          {m.route_error && <DL label="Route error" value={<span className="text-red-500">{m.route_error}</span>} />}
        </>
      )}
      {m.message_id_header && <DL label="Message-ID" value={<code className="text-xs">{m.message_id_header}</code>} />}
      {m.in_reply_to && <DL label="In-Reply-To" value={<code className="text-xs">{m.in_reply_to}</code>} />}

      {m.body_text && (
        <details className="mt-4" open>
          <summary className="cursor-pointer text-text-dim text-xs uppercase tracking-wide mb-1">Body</summary>
          <pre className="whitespace-pre-wrap break-words text-text bg-surface-2 p-3 rounded border border-border">{m.body_text}</pre>
        </details>
      )}
      {m.body_html && (
        <details className="mt-3">
          <summary className="cursor-pointer text-text-dim text-xs uppercase tracking-wide mb-1">HTML body</summary>
          <pre className="whitespace-pre-wrap break-all text-text bg-surface-2 p-3 rounded border border-border text-xs">{m.body_html}</pre>
        </details>
      )}
      {events.length > 0 && (
        <div className="mt-4">
          <div className="text-text-dim text-xs uppercase tracking-wide mb-1">Message events</div>
          <ul className="space-y-2">
            {events.map((e) => (
              <li key={e.id} className="text-xs">
                <div className="flex items-center gap-2">
                  <StatusPill status={e.kind} />
                  <span className="text-text-dim">{shortTime(e.occurred_at)}</span>
                  {e.recipient && <span>{stripScheme(e.recipient)}</span>}
                </div>
                <EventDetail event={e} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

const messagingLiveTopics = new Set([
  "message.sent",
  "message.received",
  "message.delivered",
  "message.bounced",
  "message.complained",
  "message.opened",
  "message.clicked",
  "message.failed",
  "message.rejected",
  "message.delivery_delayed",
  "message.rendering_failed",
  "message.subscription_changed",
  "message.event",
]);

function EventDetail({ event }: { event: DeliveryEvent }) {
  const meta = eventMetadata(event.raw);
  const url = stringMeta(meta, "url") || stringMeta(meta, "link");
  const userAgent = stringMeta(meta, "userAgent");
  const ip = stringMeta(meta, "ipAddress");
  return (
    <div className="ml-2 mt-0.5 text-text-dim space-y-0.5">
      {event.reason && <div>{event.reason}</div>}
      {url && <div>URL: <span className="break-all text-text">{url}</span></div>}
      {(ip || userAgent) && (
        <div>
          {ip && <span>IP: <span className="text-text">{ip}</span></span>}
          {ip && userAgent && <span> · </span>}
          {userAgent && <span>Agent: <span className="text-text">{userAgent}</span></span>}
        </div>
      )}
    </div>
  );
}

function eventMetadata(raw: unknown): Record<string, unknown> {
  if (!raw || typeof raw !== "object") return {};
  const obj = raw as Record<string, unknown>;
  const nested = obj.metadata;
  if (nested && typeof nested === "object") return nested as Record<string, unknown>;
  return obj;
}

function stringMeta(meta: Record<string, unknown>, key: string): string {
  const v = meta[key];
  return typeof v === "string" ? v : "";
}

function composeDraftFromMessage(m: MessageRow, senders: SenderRow[]): ComposeDraft | null {
  const from = replyFromAddress(m, senders);
  if (!from) return null;
  const inReplyTo = m.message_id_header || "";
  const refs = uniqueStrings([...(m.references || []), ...(inReplyTo ? [inReplyTo] : [])]);
  return {
    key: m.id,
    channel: m.channel || "email",
    from,
    to: stripScheme(m.from),
    subject: replySubject(m.subject || ""),
    body: "",
    in_reply_to: inReplyTo,
    references: refs,
  };
}

function replyFromAddress(m: MessageRow, senders: SenderRow[]): string {
  const candidates = uniqueStrings([
    m.matched_recipient || "",
    ...(m.to || []),
  ].map(stripScheme));
  for (const candidate of candidates) {
    if (senders.some((s) => senderCanSendAddress(s, candidate))) {
      return candidate;
    }
  }
  return candidates[0] || "";
}

function senderCanSendAddress(sender: SenderRow, address: string): boolean {
  const clean = stripScheme(address);
  const senderAddr = stripScheme(sender.address);
  if (senderAddr === clean) return true;
  const at = clean.indexOf("@");
  return sender.kind === "domain" && at > 0 && senderAddr === clean.slice(at + 1);
}

function replySubject(subject: string): string {
  const s = subject.trim();
  if (!s) return "Re: (no subject)";
  return /^re:/i.test(s) ? s : `Re: ${s}`;
}

function uniqueStrings(values: string[]): string[] {
  const out: string[] = [];
  for (const value of values) {
    const s = value.trim();
    if (s && !out.includes(s)) out.push(s);
  }
  return out;
}

function ComposeView({
  api, senders, quota, draft, onSent, gotoSenders,
}: {
  api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  senders: SenderRow[];
  quota: QuotaInfo | null;
  draft: ComposeDraft | null;
  onSent: () => void;
  gotoSenders: () => void;
}) {
  // Verified senders: a unified select with two kinds. Email rows
  // expose the full address as the value; domain rows expose just
  // the domain — when picked, a local-part input appears so the
  // operator can compose addr@domain explicitly.
  const verified = useMemo(() => senders.filter((s) => s.verified), [senders]);

  // selectedAddress is what's chosen in the dropdown — either a full
  // email or a bare domain. selectedKind tracks which (drives the
  // local-part field).
  const [selectedAddress, setSelectedAddress] = useState<string>("");
  const [localPart, setLocalPart] = useState("");
  const [to, setTo] = useState("");
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [inReplyTo, setInReplyTo] = useState("");
  const [references, setReferences] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // Default selection: first verified email, otherwise first verified domain.
  useEffect(() => {
    if (selectedAddress) return;
    const firstEmail = verified.find((s) => s.kind === "email");
    if (firstEmail) {
      setSelectedAddress(stripScheme(firstEmail.address));
      return;
    }
    const firstDomain = verified.find((s) => s.kind === "domain");
    if (firstDomain) {
      setSelectedAddress(stripScheme(firstDomain.address));
      setLocalPart("noreply");
    }
  }, [verified, selectedAddress]);

  useEffect(() => {
    if (!draft) return;
    const from = stripScheme(draft.from);
    const at = from.indexOf("@");
    const exact = verified.find((s) => stripScheme(s.address) === from);
    const domain = at > 0
      ? verified.find((s) => s.kind === "domain" && stripScheme(s.address) === from.slice(at + 1))
      : undefined;
    if (exact) {
      setSelectedAddress(stripScheme(exact.address));
      setLocalPart("");
    } else if (domain) {
      setSelectedAddress(stripScheme(domain.address));
      setLocalPart(from.slice(0, at));
    } else {
      setSelectedAddress(from);
      setLocalPart("");
    }
    setTo(draft.to);
    setSubject(draft.subject);
    setBody(draft.body);
    setInReplyTo(draft.in_reply_to || "");
    setReferences(draft.references || []);
    setErr("");
  }, [draft, verified]);

  const selectedSender = useMemo(
    () => verified.find((s) => stripScheme(s.address) === selectedAddress),
    [verified, selectedAddress],
  );
  const isDomain = selectedSender?.kind === "domain";

  // The actual From string handed to send_message.
  const computedFrom = useMemo(() => {
    if (!selectedAddress) return "";
    if (isDomain) {
      const lp = localPart.trim();
      return lp ? `${lp}@${selectedAddress}` : "";
    }
    return selectedAddress;
  }, [selectedAddress, localPart, isDomain]);

  const noVerifiedSenders = verified.length === 0;

  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!computedFrom) {
      setErr(isDomain ? "Enter a local-part for the domain sender." : "Pick a sender.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const recipients = to.split(",").map((s) => s.trim()).filter(Boolean);
      const channel = selectedSender?.channel || "email";
      const args: Record<string, unknown> = {
        channel,
        from: computedFrom,
        to: recipients,
        body,
      };
      // Email-only fields — included only when relevant so the
      // server's "irrelevant fields warned + dropped" pathway
      // doesn't fire on every SMS send.
      if (channel === "email") {
        if (subject) args.subject = subject;
        if (inReplyTo) args.in_reply_to = inReplyTo;
        if (references.length > 0) args.references = references;
      }
      await api("POST", "/tools/call", {}, { tool: "send_message", args });
      setTo(""); setSubject(""); setBody(""); setInReplyTo(""); setReferences([]);
      onSent();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={send} className="p-6 max-w-2xl space-y-3">
      <h2 className="text-lg font-semibold mb-1">Compose message</h2>
      {draft ? (
        <p className="text-xs text-text-dim mb-3">Replying to message #{draft.key}</p>
      ) : (
        <p className="text-xs text-text-dim mb-3">Pick a verified sender; the channel comes from the sender. Comma-separate recipients.</p>
      )}

      {noVerifiedSenders ? (
        <div className="rounded border border-yellow-500/30 bg-yellow-500/10 p-3 text-sm text-yellow-300">
          No verified senders. <button type="button" className="underline" onClick={gotoSenders}>Verify one in the Senders tab →</button>
        </div>
      ) : (
        <Field label="Sender">
          <div className="flex gap-2 items-stretch">
            <select
              className={inputCls + " flex-1"}
              value={selectedAddress}
              onChange={(e) => {
                setSelectedAddress(e.target.value);
                const next = verified.find((s) => stripScheme(s.address) === e.target.value);
                if (next?.kind === "domain" && !localPart) setLocalPart("noreply");
              }}
              required
            >
              {(["email", "sms", "whatsapp"] as const).map((ch) => {
                const inCh = verified.filter((s) => s.channel === ch);
                if (inCh.length === 0) return null;
                const groupLabel = ch === "email" ? "Email (SES)" : ch === "sms" ? "SMS (Twilio)" : "WhatsApp (Twilio)";
                return (
                  <optgroup key={ch} label={groupLabel}>
                    {inCh.map((s) => (
                      <option key={s.address} value={stripScheme(s.address)}>
                        {s.kind === "domain" ? `@${stripScheme(s.address)} (any local-part)` : stripScheme(s.address)}
                      </option>
                    ))}
                  </optgroup>
                );
              })}
              {selectedAddress && !verified.some((s) => stripScheme(s.address) === selectedAddress) && (
                <option value={selectedAddress}>{selectedAddress}</option>
              )}
            </select>
            {isDomain && (
              <>
                <input
                  className={inputCls + " w-44"}
                  value={localPart}
                  onChange={(e) => setLocalPart(e.target.value)}
                  placeholder="noreply"
                  aria-label="Local-part"
                  required
                />
                <span className="self-center text-text-dim text-sm whitespace-nowrap">@{selectedAddress}</span>
              </>
            )}
          </div>
          {computedFrom && (
            <div className="text-xs text-text-dim mt-1">
              Will send via <strong>{selectedSender?.channel || "email"}</strong> from <code>{computedFrom}</code>
            </div>
          )}
        </Field>
      )}

      <Field label="To">
        <input
          className={inputCls}
          value={to}
          onChange={(e) => setTo(e.target.value)}
          placeholder={
            selectedSender?.channel === "email" || !selectedSender
              ? "alice@example.com, bob@x.io"
              : "+15551234567"
          }
          required
          disabled={noVerifiedSenders}
        />
      </Field>
      {(selectedSender?.channel || "email") === "email" && (
        <Field label="Subject">
          <input className={inputCls} value={subject} onChange={(e) => setSubject(e.target.value)} disabled={noVerifiedSenders} />
        </Field>
      )}
      <Field label="Body">
        <textarea className={inputCls + " font-mono text-sm"} rows={10} value={body} onChange={(e) => setBody(e.target.value)} required disabled={noVerifiedSenders} />
      </Field>

      {quota && quota.sandboxed && (
        <p className="text-xs text-yellow-400/80">
          Sandbox: only recipients you've verified in SES will receive this message.
        </p>
      )}
      {err && <div className="text-red-500 text-sm">{err}</div>}

      <div className="flex justify-end gap-2 pt-2">
        <button type="submit" disabled={busy || noVerifiedSenders} className="px-4 py-1.5 bg-accent text-white rounded disabled:opacity-50">
          {busy ? "Sending…" : "Send"}
        </button>
      </div>
    </form>
  );
}

function SendersView({
  rows, identities, quota, api, reload, error, notify, confirmAction,
}: {
  rows: SenderRow[];
  identities: IdentityRow[];
  quota: QuotaInfo | null;
  api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  reload: () => void;
  error: string;
  notify: Notify;
  confirmAction: ConfirmFn;
}) {
  const [addr, setAddr] = useState("");
  const [inbound, setInbound] = useState<"auto" | "true" | "false">("auto");
  const [spf, setSpf] = useState(true);
  const [region, setRegion] = useState("eu-west-1");
  const [advanced, setAdvanced] = useState(false);
  const [displayName, setDisplayName] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<SendersCreateResp | null>(null);
  const [err, setErr] = useState("");
  const [senderChannel, setSenderChannel] = useState<"email" | "sms" | "whatsapp">("email");

  // Domain inventory from the Domains app, when bound. Lets the
  // operator compose <mailbox>@<domain> by picking from a curated
  // list instead of typing free-text. Soft-fails: any error leaves
  // the picker hidden and the form keeps working with the plain
  // input below.
  const [inventoryDomains, setInventoryDomains] = useState<string[]>([]);
  const [localPart, setLocalPart] = useState("");
  const [pickedDomain, setPickedDomain] = useState("");
  // pickerMode=true → compose via dropdown + mailbox input.
  // false → original free-text input. Flips to false automatically
  // when inventory loads empty / unavailable.
  const [pickerMode, setPickerMode] = useState(true);
  const [providerOptions, setProviderOptions] = useState<ProviderSenderOption[]>([]);
  const [providerOptionsError, setProviderOptionsError] = useState("");
  const [providerPickerMode, setProviderPickerMode] = useState(true);
  const [pickedProviderAddress, setPickedProviderAddress] = useState("");

  useEffect(() => {
    api<{ available: boolean; domains: { name: string }[] }>("GET", "/senders/domains")
      .then((r) => {
        const names = (r.available && Array.isArray(r.domains))
          ? r.domains.map((d) => d.name).filter(Boolean)
          : [];
        setInventoryDomains(names);
        if (names.length === 0) setPickerMode(false);
      })
      .catch(() => { setInventoryDomains([]); setPickerMode(false); });
  }, [api]);

  useEffect(() => {
    api<{ available: boolean; options: ProviderSenderOption[]; error?: string }>("GET", "/senders/provider-options")
      .then((r) => {
        setProviderOptions(r.available && Array.isArray(r.options) ? r.options : []);
        setProviderOptionsError(r.error || "");
        if (!r.available || !Array.isArray(r.options) || r.options.length === 0) {
          setProviderPickerMode(false);
        }
      })
      .catch((e) => {
        setProviderOptions([]);
        setProviderOptionsError(parseSendersError((e as Error).message));
        setProviderPickerMode(false);
      });
  }, [api]);

  // Compose addr from picker controls. In manual mode the user types
  // directly into addr; this effect is a no-op there.
  useEffect(() => {
    if (!pickerMode) return;
    if (senderChannel !== "email") return;
    if (!pickedDomain) { setAddr(""); return; }
    setAddr(localPart ? `${localPart}@${pickedDomain}` : pickedDomain);
  }, [pickerMode, senderChannel, pickedDomain, localPart]);

  const channelProviderOptions = providerOptions.filter((o) => o.channel === senderChannel);

  useEffect(() => {
    if (senderChannel === "email") return;
    if (!providerPickerMode) return;
    setAddr(pickedProviderAddress);
  }, [senderChannel, providerPickerMode, pickedProviderAddress]);

  useEffect(() => {
    setAddr("");
    setLocalPart("");
    setPickedDomain("");
    setPickedProviderAddress("");
    setAdvanced(false);
    setErr("");
  }, [senderChannel]);

  const addressIsDomain = senderChannel === "email" && addr.length > 0 && !addr.includes("@");

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(""); setResult(null);
    try {
      const args: Record<string, unknown> = { address: addr };
      if (senderChannel !== "email") {
        args.channel = senderChannel;
      }
      if (addressIsDomain) {
        args.inbound = inbound;
        args.spf = spf;
        if (inbound !== "false") args.region = region;
      }
      if (displayName.trim()) {
        args.display_name = displayName.trim();
      }
      const out = await api<SendersCreateResp>("POST", "/tools/call", {}, {
        tool: "senders_create",
        args,
      });
      setResult(out);
      setAddr("");
      setLocalPart("");
      setPickedDomain("");
      setPickedProviderAddress("");
      setDisplayName("");
      reload();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const makeDefault = async (address: string, channel: string) => {
    try {
      await api("POST", "/tools/call", {}, {
        tool: "senders_set_default",
        args: { address, channel },
      });
      reload();
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const recheck = async (address: string, channel?: string) => {
    try {
      await api("POST", "/tools/call", {}, { tool: "senders_get", args: { address, channel } });
      reload();
    } catch (e) { notify("error", `Re-check failed: ${(e as Error).message}`); }
  };

  const recheckSenderSetup = async (address: string, displayName?: string, channel?: string) => {
    try {
      const args: Record<string, unknown> = {
        address: stripScheme(address),
        inbound: "auto",
        spf: true,
      };
      if (channel) args.channel = channel;
      if (displayName) args.display_name = displayName;
      const out = await api<SendersCreateResp>("POST", "/tools/call", {}, {
        tool: "senders_create",
        args,
      });
      setResult(out);
      reload();
    } catch (e) {
      notify("error", `Setup re-check failed: ${(e as Error).message}`);
    }
  };

  // saveDisplayName — inline-edit save. Calls the local-only edit
  // endpoint (no provider round-trip). The next send from this sender
  // will use the new display name as the friendly From.
  const saveDisplayName = async (address: string, channel: string, displayName: string) => {
    try {
      await api("POST", "/senders/edit", {}, { address, channel, display_name: displayName });
      reload();
    } catch (e) {
      notify("error", `Save display name failed: ${(e as Error).message}`);
    }
  };

  const remove = async (address: string, channel: string) => {
    const label = stripScheme(address);
    confirmAction({
      title: "Delete sender",
      tone: "danger",
      confirmLabel: "Delete",
      message: (
        <>
          <div>Delete <code>{label}</code> from Messaging?</div>
          <div className="mt-2 text-text-dim">Future sends from this sender will fail.</div>
        </>
      ),
      onConfirm: async () => {
        try {
          await api("POST", "/tools/call", {}, { tool: "senders_delete", args: { address, channel } });
          reload();
        } catch (e) { notify("error", `Delete failed: ${(e as Error).message}`); }
      },
    });
  };

  return (
    <div>
      {error && (
        <div className="m-4 p-3 rounded border border-red-500/30 bg-red-500/10 text-sm text-red-300 space-y-1">
          <div className="font-medium">SES not reachable</div>
          <div className="text-xs whitespace-pre-wrap">{error}</div>
          {error.toLowerCase().includes("no email_provider") && (
            <div className="text-xs text-text-dim">
              Open this app's settings → Integrations and bind your aws-ses connection to the <code>email_provider</code> role, then refresh.
            </div>
          )}
        </div>
      )}
      <form onSubmit={onSubmit} className="p-4 border-b border-border space-y-3">
        <div className="flex gap-2 items-end flex-wrap">
          <Field label="Channel">
            <select
              className={inputCls + " w-36"}
              value={senderChannel}
              onChange={(e) => setSenderChannel(e.target.value as "email" | "sms" | "whatsapp")}
            >
              <option value="email">Email</option>
              <option value="sms">SMS</option>
              <option value="whatsapp">WhatsApp</option>
            </select>
          </Field>
          {senderChannel === "email" && pickerMode && inventoryDomains.length > 0 ? (
            <>
              <Field label="Mailbox (optional)" hint="leave empty for the whole domain">
                <input
                  className={inputCls + " w-32"}
                  value={localPart}
                  onChange={(e) => setLocalPart(e.target.value.trim().toLowerCase())}
                  placeholder="alice"
                />
              </Field>
              <span className="text-text-dim pb-2 select-none">@</span>
              <Field label="Domain" hint="loaded from the Domains app">
                <select
                  className={inputCls + " w-56"}
                  value={pickedDomain}
                  onChange={(e) => {
                    if (e.target.value === "__manual__") {
                      setPickerMode(false);
                      setPickedDomain("");
                    } else {
                      setPickedDomain(e.target.value);
                    }
                  }}
                >
                  <option value="">— pick a domain —</option>
                  {inventoryDomains.map((d) => (
                    <option key={d} value={d}>{d}</option>
                  ))}
                  <option value="__manual__">Other / type manually…</option>
                </select>
              </Field>
            </>
          ) : senderChannel !== "email" && providerPickerMode && channelProviderOptions.length > 0 ? (
            <Field
              label={senderChannel === "whatsapp" ? "WhatsApp sender" : "Twilio number"}
              hint="loaded from the bound Twilio connection"
            >
              <select
                className={inputCls + " w-80"}
                value={pickedProviderAddress}
                onChange={(e) => {
                  if (e.target.value === "__manual__") {
                    setProviderPickerMode(false);
                    setPickedProviderAddress("");
                    setAddr("");
                  } else {
                    setPickedProviderAddress(e.target.value);
                  }
                }}
                required
              >
                <option value="">— pick a sender —</option>
                {channelProviderOptions.map((o) => (
                  <option key={`${o.channel}:${o.address}:${o.provider_id || ""}`} value={o.address}>
                    {o.address}{o.label ? ` · ${o.label}` : ""}{o.status ? ` · ${o.status}` : ""}
                  </option>
                ))}
                <option value="__manual__">Other / type manually…</option>
              </select>
            </Field>
          ) : (
            <Field
              label="Add sender"
              hint={
                senderChannel === "email" && inventoryDomains.length > 0
                  ? "alice@acme.com / acme.com — or pick from your domains"
                  : senderChannel === "email"
                    ? "alice@acme.com (one mailbox) or acme.com (whole domain)"
                    : senderChannel === "whatsapp"
                      ? "E.164 WhatsApp sender from Twilio, e.g. +14155238886"
                      : "E.164 Twilio SMS number, e.g. +14155238886"
              }
            >
              <input
                className={inputCls + " w-80"}
                value={addr}
                onChange={(e) => setAddr(e.target.value)}
                placeholder={senderChannel === "email" ? "email or domain" : "+14155238886"}
                required
              />
            </Field>
          )}
          <Field label="Display name (optional)" hint='shown as "Name" <addr> in From'>
            <input
              className={inputCls + " w-48"}
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Marco at Socialcast"
            />
          </Field>
          <button
            type="submit"
            disabled={busy || !addr.trim()}
            className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50"
          >
            {busy ? "Working…" : "Add"}
          </button>
          {senderChannel === "email" && !pickerMode && inventoryDomains.length > 0 && (
            <button
              type="button"
              className="text-xs text-text-dim hover:text-text underline"
              onClick={() => { setAddr(""); setLocalPart(""); setPickedDomain(""); setPickerMode(true); }}
            >
              Pick from domains
            </button>
          )}
          {!providerPickerMode && senderChannel !== "email" && channelProviderOptions.length > 0 && (
            <button
              type="button"
              className="text-xs text-text-dim hover:text-text underline"
              onClick={() => { setAddr(""); setPickedProviderAddress(""); setProviderPickerMode(true); }}
            >
              Pick from Twilio
            </button>
          )}
          {addressIsDomain && (
            <button
              type="button"
              className="text-xs text-text-dim hover:text-text underline"
              onClick={() => setAdvanced((v) => !v)}
            >
              {advanced ? "Hide" : "Show"} domain options
            </button>
          )}
        </div>
        {senderChannel !== "email" && providerOptionsError && (
          <div className="text-xs text-yellow-300">{providerOptionsError}</div>
        )}
        {senderChannel !== "email" && !providerOptionsError && (
          <div className="text-xs text-text-dim">
            {senderChannel === "whatsapp"
              ? "Inbound uses the Twilio WhatsApp sender callback URL; status events use the Messaging status webhook."
              : "Inbound uses the Twilio phone SmsUrl; status events use the Messaging status webhook."}
          </div>
        )}

        {addressIsDomain && advanced && (
          <div className="ml-1 pl-3 border-l border-border space-y-2 text-sm">
            <Field label="Inbound mail" hint="auto = enable when aws-s3 + aws-sns are bound">
              <select
                className={inputCls + " w-44"}
                value={inbound}
                onChange={(e) => setInbound(e.target.value as "auto" | "true" | "false")}
              >
                <option value="auto">Auto (default)</option>
                <option value="true">Yes — bootstrap inbound</option>
                <option value="false">No — outbound only</option>
              </select>
            </Field>
            {inbound !== "false" && (
              <Field label="AWS region (inbound MX target)">
                <select className={inputCls + " w-44"} value={region} onChange={(e) => setRegion(e.target.value)}>
                  <option value="eu-west-1">eu-west-1</option>
                  <option value="us-east-1">us-east-1</option>
                  <option value="us-west-2">us-west-2</option>
                  <option value="eu-central-1">eu-central-1</option>
                  <option value="ap-southeast-1">ap-southeast-1</option>
                  <option value="ap-southeast-2">ap-southeast-2</option>
                  <option value="ap-south-1">ap-south-1</option>
                  <option value="ap-northeast-1">ap-northeast-1</option>
                  <option value="ca-central-1">ca-central-1</option>
                  <option value="eu-north-1">eu-north-1</option>
                </select>
              </Field>
            )}
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={spf} onChange={(e) => setSpf(e.target.checked)} />
              <span>Publish SPF TXT record <span className="text-text-dim text-xs">(<code>v=spf1 include:amazonses.com ~all</code>)</span></span>
            </label>
          </div>
        )}
      </form>

      {err && <div className="p-4 text-red-500 text-sm">{err}</div>}
      {result && <SendersCreateResult result={result} onDismiss={() => setResult(null)} />}

      {rows.length === 0 ? (
        <div className="p-6 text-text-dim text-sm">No senders configured. Verify an email or domain above.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-xs text-text-dim">
            <tr className="border-b border-border">
              <th className="text-left px-4 py-2">Address</th>
              <th className="text-left px-4 py-2">Display name</th>
              <th className="text-left px-4 py-2">Channel</th>
              <th className="text-left px-4 py-2">Kind</th>
              <th className="text-left px-4 py-2">Verified</th>
              <th className="text-left px-4 py-2">Setup</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={`${s.channel}:${s.address}`} className="border-b border-border">
                <td className="px-4 py-2">
                  <span>{stripScheme(s.address)}</span>
                  {s.is_default && <span className="ml-2 bg-blue-500/20 text-blue-400 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded">default</span>}
                </td>
                <td className="px-4 py-2">
                  <DisplayNameCell
                    value={s.display_name || ""}
                    onSave={(v) => saveDisplayName(s.address, s.channel, v)}
                    placeholder="Set display name…"
                  />
                </td>
                <td className="px-4 py-2 text-text-dim">{s.channel}</td>
                <td className="px-4 py-2 text-text-dim">{s.kind}</td>
                <td className="px-4 py-2"><StatusPill status={s.verified ? "verified" : "pending"} /></td>
                <td className="px-4 py-2 text-text-dim text-xs">
                  <SenderSetupCell sender={s} />
                </td>
                <td className="px-4 py-2 text-right space-x-3">
                  {!s.is_default && (
                    <button
                      type="button"
                      className="text-text-dim hover:text-accent text-xs"
                      onClick={() => makeDefault(s.address, s.channel)}
                      title="Make this the default sender for this channel. send_message uses the default when 'from' is omitted."
                    >Make default</button>
                  )}
                  <button
                    type="button"
                    className="text-accent hover:underline text-xs"
                    onClick={() => recheckSenderSetup(s.address, s.display_name, s.channel)}
                    title="Re-runs the full sender setup for this address: SES identity checks, DNS hints, inbound receiving, SNS notifications, and SES delivery/open/click events where available. Safe to run again."
                  >Recheck setup</button>
                  <button type="button" className="text-text-dim hover:text-text text-xs" onClick={() => recheck(s.address, s.channel)}>Refresh</button>
                  <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => remove(s.address, s.channel)}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <IdentitiesSection identities={identities} onRecheckSetup={(address) => recheckSenderSetup(address)} />

      {quota && (
        <div className="p-4 text-xs text-text-dim border-t border-border mt-4">
          24h quota: {quota.sent_last_24h.toFixed(0)} / {quota.send_quota_24h.toFixed(0)} ·
          rate: {quota.send_rate_per_second}/s ·
          {quota.sandboxed ? " sandboxed" : " production"}
          {!quota.sending_enabled && <span className="text-red-400"> · sending disabled</span>}
        </div>
      )}
    </div>
  );
}

// DisplayNameCell — click-to-edit display_name cell for the senders
// table. The next send from this sender will use the new name as the
// friendly From (`"Name" <addr>`). Empty value clears (well — keeps
// the existing one: the backend's COALESCE+NULLIF preserves on empty;
// to truly blank it out we'd need a dedicated path, which can wait).
function DisplayNameCell({ value, onSave, placeholder }: {
  value: string;
  onSave: (v: string) => void | Promise<void>;
  placeholder?: string;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(value);

  // Keep draft in sync if the parent reloads with a new value while
  // we're not editing — otherwise the cell shows stale state.
  useEffect(() => { if (!editing) setDraft(value); }, [value, editing]);

  const commit = () => {
    setEditing(false);
    if (draft !== value) onSave(draft);
  };
  const cancel = () => {
    setEditing(false);
    setDraft(value);
  };

  if (!editing) {
    return (
      <button
        type="button"
        onClick={() => setEditing(true)}
        className={`text-left w-full hover:bg-bg-input/50 rounded px-1 py-0.5 ${value ? "text-text" : "text-text-dim italic"}`}
        title="Click to edit"
      >
        {value || placeholder || "—"}
      </button>
    );
  }
  return (
    <input
      type="text"
      autoFocus
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") commit();
        if (e.key === "Escape") cancel();
      }}
      placeholder={placeholder}
      className="w-full bg-bg-input border border-accent/50 rounded px-1 py-0.5 text-sm"
    />
  );
}

function SenderSetupCell({ sender }: { sender: SenderRow }) {
  const parts: React.ReactNode[] = [];
  if (sender.channel === "email") {
    if (sender.dkim_status) {
      parts.push(<span key="dkim">DKIM: {sender.dkim_status}</span>);
    }
    if (sender.inbound_bootstrapped) {
      parts.push(<span key="inbound" className="text-green-400">inbound wired</span>);
    }
  } else {
    parts.push(<span key="provider">{sender.provider || "twilio"}</span>);
    if (sender.inbound_bootstrapped) {
      parts.push(
        <span key="inbound" className="text-green-400">
          {sender.channel === "whatsapp" ? "callback wired" : "SmsUrl wired"}
        </span>,
      );
    } else {
      parts.push(<span key="inbound" className="text-yellow-400">inbound not wired</span>);
    }
  }
  if (parts.length === 0) return <span>—</span>;
  return (
    <div className="flex flex-wrap gap-x-2 gap-y-1">
      {parts.map((part, i) => (
        <span key={i} className="inline-flex items-center gap-1">
          {i > 0 && <span className="text-text-dim">·</span>}
          {part}
        </span>
      ))}
    </div>
  );
}

// IdentitiesSection — anchors (DKIM-verified domains, future WABA)
// rendered below the senders table in the Senders tab. Operator sees
// what's verified upstream without these rows polluting any From
// dropdown (they're not in the senders table at all).
function IdentitiesSection({ identities, onRecheckSetup }: { identities: IdentityRow[]; onRecheckSetup: (address: string) => void }) {
  if (identities.length === 0) return null;
  return (
    <div className="border-t border-border mt-6 pt-4">
      <div className="px-4 pb-2 flex items-baseline justify-between">
        <h3 className="text-sm font-medium text-text">Verified domains & accounts</h3>
        <span className="text-xs text-text-dim">
          authentication anchors — not sendable, but enable inheritance + inbound
        </span>
      </div>
      <table className="w-full text-sm">
        <thead className="text-xs text-text-dim">
          <tr className="border-b border-border">
            <th className="text-left px-4 py-2">Address</th>
            <th className="text-left px-4 py-2">Kind</th>
            <th className="text-left px-4 py-2">Provider</th>
            <th className="text-left px-4 py-2">Status</th>
            <th className="text-left px-4 py-2">Inbound / Events</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {identities.map((i) => (
            <tr key={i.id} className="border-b border-border">
              <td className="px-4 py-2 font-mono">{i.address}</td>
              <td className="px-4 py-2 text-text-dim">{i.kind}</td>
              <td className="px-4 py-2 text-text-dim">{i.provider}</td>
              <td className="px-4 py-2">
                <span className={i.verified ? "text-green-400" : "text-yellow-400"}>
                  {i.verification_status}
                </span>
                {i.dkim_status && (
                  <span className="ml-2 text-xs text-text-dim">DKIM: {i.dkim_status}</span>
                )}
              </td>
              <td className="px-4 py-2 text-text-dim text-xs">
                {i.inbound_bootstrapped ? "wired" : "—"}
              </td>
              <td className="px-4 py-2 text-right">
                <button
                  type="button"
                  className="text-accent hover:underline text-xs"
                  onClick={() => onRecheckSetup(i.address)}
                  title="Re-runs the full domain/account setup: SES identity checks, DNS hints, inbound receiving, SNS notifications, and SES delivery/open/click events where available. Safe to run again."
                >Recheck setup</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function TemplatesView({ rows, api, reload, notify, confirmAction }: { rows: TemplateRow[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void; notify: Notify; confirmAction: ConfirmFn }) {
  const [channel, setChannel] = useState("whatsapp");
  const [name, setName] = useState("");
  const [bodyText, setBodyText] = useState("");
  const [language, setLanguage] = useState("en");
  const [category, setCategory] = useState("UTILITY");
  const [submitForApproval, setSubmitForApproval] = useState(true);
  const [busy, setBusy] = useState(false);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api("POST", "/tools/call", {}, {
        tool: "template_create",
        args: {
          channel,
          name,
          body_text: bodyText,
          language,
          category,
          submit_for_approval: submitForApproval,
        },
      });
      setName("");
      setBodyText("");
      reload();
      notify("info", "Template added.");
    } catch (e) {
      notify("error", `Add failed: ${parseSendersError((e as Error).message)}`);
    } finally {
      setBusy(false);
    }
  };

  const handleSync = async () => {
    setBusy(true);
    try {
      const out = await api<{ synced?: number }>("POST", "/templates/sync", { channel: "whatsapp" });
      reload();
      notify("info", `Synced ${out.synced ?? 0} WhatsApp template${out.synced === 1 ? "" : "s"}.`);
    } catch (e) {
      notify("error", `Sync failed: ${parseSendersError((e as Error).message)}`);
    } finally {
      setBusy(false);
    }
  };

  const handleDelete = async (id: number) => {
    const tpl = rows.find((t) => t.id === id);
    confirmAction({
      title: "Delete template",
      tone: "danger",
      confirmLabel: "Delete",
      message: (
        <>
          <div>Delete template <code>{tpl?.name || `#${id}`}</code>?</div>
          <div className="mt-2 text-text-dim">Messages that reference this saved template will no longer be able to use it.</div>
        </>
      ),
      onConfirm: async () => {
        try {
          await api("POST", "/tools/call", {}, { tool: "template_delete", args: { id } });
          reload();
        } catch (e) {
          notify("error", `Delete failed: ${(e as Error).message}`);
        }
      },
    });
  };
  const handleRefreshStatus = async (id: number) => {
    try {
      await api("POST", `/templates/${id}/refresh-status`, {});
      reload();
    } catch (e) {
      notify("error", `Refresh failed: ${parseSendersError((e as Error).message)}`);
    }
  };
  const handleSubmit = async (id: number) => {
    try {
      await api("POST", `/templates/${id}/submit`, {}, { category, language });
      reload();
    } catch (e) {
      notify("error", `Submit failed: ${parseSendersError((e as Error).message)}`);
    }
  };
  return (
    <div>
      <form onSubmit={handleCreate} className="p-4 flex gap-2 items-end border-b border-border flex-wrap">
        <Field label="Channel">
          <select className={inputCls + " w-36"} value={channel} onChange={(e) => setChannel(e.target.value)}>
            <option value="whatsapp">WhatsApp</option>
            <option value="sms">SMS</option>
            <option value="email">Email</option>
          </select>
        </Field>
        <Field label="Name">
          <input className={inputCls + " w-56"} value={name} onChange={(e) => setName(e.target.value)} required placeholder="appointment_reminder" />
        </Field>
        {channel === "whatsapp" && (
          <>
            <Field label="Category">
              <select className={inputCls + " w-44"} value={category} onChange={(e) => setCategory(e.target.value)}>
                <option value="UTILITY">Utility</option>
                <option value="MARKETING">Marketing</option>
                <option value="AUTHENTICATION">Authentication</option>
              </select>
            </Field>
            <Field label="Language">
              <input className={inputCls + " w-24"} value={language} onChange={(e) => setLanguage(e.target.value)} />
            </Field>
            <label className="flex items-center gap-2 text-xs text-text-dim pb-2">
              <input type="checkbox" checked={submitForApproval} onChange={(e) => setSubmitForApproval(e.target.checked)} />
              Submit
            </label>
          </>
        )}
        <Field label="Body">
          <input className={inputCls + " w-[28rem]"} value={bodyText} onChange={(e) => setBodyText(e.target.value)} required placeholder="Hi {{1}}, your appointment is {{2}}." />
        </Field>
        <button type="submit" className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50" disabled={busy}>Add</button>
        <button type="button" className="px-3 py-1.5 rounded border border-border text-text-dim hover:text-text hover:bg-surface-2 disabled:opacity-50" onClick={handleSync} disabled={busy}>Sync WhatsApp</button>
      </form>
      {rows.length === 0 ? (
        <div className="p-6 text-text-dim text-sm">No templates.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-xs text-text-dim">
            <tr className="border-b border-border">
              <th className="text-left px-4 py-2">Name</th>
              <th className="text-left px-4 py-2">Channel</th>
              <th className="text-left px-4 py-2">Status</th>
              <th className="text-left px-4 py-2">Body</th>
              <th className="text-left px-4 py-2">Updated</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((t) => (
              <tr key={t.id} className="border-b border-border">
                <td className="px-4 py-2">
                  <div className="font-medium">{t.name}</div>
                  {t.provider_template_id && <div className="text-[11px] text-text-dim font-mono">{t.provider_template_id}</div>}
                </td>
                <td className="px-4 py-2">{t.channel}</td>
                <td className="px-4 py-2">
                  {t.provider_status ? <StatusPill status={t.provider_status} /> : <span className="text-text-dim">local</span>}
                </td>
                <td className="px-4 py-2 text-text-dim truncate max-w-xl">{t.subject || t.body_text || "—"}</td>
                <td className="px-4 py-2 text-text-dim">{shortTime(t.updated_at)}</td>
                <td className="px-4 py-2 text-right">
                  {t.channel === "whatsapp" && (
                    <>
                      <button type="button" className="text-accent hover:underline text-xs mr-3" onClick={() => handleRefreshStatus(t.id)}>Refresh</button>
                      {t.provider_status !== "approved" && (
                        <button type="button" className="text-accent hover:underline text-xs mr-3" onClick={() => handleSubmit(t.id)}>Submit</button>
                      )}
                    </>
                  )}
                  <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => handleDelete(t.id)}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function RoutesView({ rows, api, reload, notify, confirmAction }: { rows: InboundRoute[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void; notify: Notify; confirmAction: ConfirmFn }) {
  const [pattern, setPattern] = useState("");
  const [targetApp, setTargetApp] = useState("");
  const [targetRoute, setTargetRoute] = useState("/inbound");

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await api("POST", "/tools/call", {}, {
        tool: "inbound_route_set",
        args: { pattern, target_app: targetApp, target_route: targetRoute },
      });
      setPattern(""); setTargetApp(""); setTargetRoute("/inbound");
      reload();
    } catch (e) {
      notify("error", `Add failed: ${(e as Error).message}`);
    }
  };
  const remove = async (id: number) => {
    const route = rows.find((r) => r.id === id);
    confirmAction({
      title: "Delete route",
      tone: "danger",
      confirmLabel: "Delete",
      message: (
        <>
          <div>Delete this inbound route?</div>
          {route && <div className="mt-2 text-text-dim"><code>{route.pattern}</code> → {route.target_app}{route.target_route}</div>}
        </>
      ),
      onConfirm: async () => {
        try {
          await api("POST", "/tools/call", {}, { tool: "inbound_route_delete", args: { id } });
          reload();
        } catch (e) {
          notify("error", `Delete failed: ${(e as Error).message}`);
        }
      },
    });
  };
  return (
    <div>
      <form onSubmit={add} className="p-4 flex gap-2 items-end border-b border-border flex-wrap">
        <Field label="Pattern" hint="e.g. mailto:support+*@acme.com">
          <input className={inputCls + " w-72"} value={pattern} onChange={(e) => setPattern(e.target.value)} required />
        </Field>
        <Field label="Target app">
          <input className={inputCls + " w-40"} value={targetApp} onChange={(e) => setTargetApp(e.target.value)} required placeholder="support" />
        </Field>
        <Field label="Target route">
          <input className={inputCls + " w-40"} value={targetRoute} onChange={(e) => setTargetRoute(e.target.value)} required />
        </Field>
        <button type="submit" className="px-3 py-1.5 bg-accent text-white rounded">Add</button>
      </form>
      {rows.length === 0 ? (
        <div className="p-6 text-text-dim text-sm">No inbound routes configured.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-xs text-text-dim">
            <tr className="border-b border-border">
              <th className="text-left px-4 py-2">Pattern</th>
              <th className="text-left px-4 py-2">Target</th>
              <th className="text-left px-4 py-2">Priority</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className="border-b border-border">
                <td className="px-4 py-2"><code className="text-xs">{r.pattern}</code></td>
                <td className="px-4 py-2">{r.target_app}<span className="text-text-dim">{r.target_route}</span></td>
                <td className="px-4 py-2">{r.priority}</td>
                <td className="px-4 py-2 text-right">
                  <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => remove(r.id)}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function SuppressionsView({ rows, api, reload, notify, confirmAction }: { rows: SuppressionRow[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void; notify: Notify; confirmAction: ConfirmFn }) {
  const remove = async (addr: string) => {
    confirmAction({
      title: "Remove suppression",
      confirmLabel: "Remove",
      message: (
        <>
          <div>Remove <code>{stripScheme(addr)}</code> from suppressions?</div>
          <div className="mt-2 text-text-dim">Messaging will allow future sends to this address again.</div>
        </>
      ),
      onConfirm: async () => {
        try {
          await api("POST", "/tools/call", {}, { tool: "suppression_remove", args: { address: addr } });
          reload();
        } catch (e) {
          notify("error", `Remove failed: ${(e as Error).message}`);
        }
      },
    });
  };
  if (rows.length === 0) {
    return <div className="p-6 text-text-dim text-sm">No suppressions. Hard bounces and complaints land here automatically.</div>;
  }
  return (
    <table className="w-full text-sm">
      <thead className="text-xs text-text-dim">
        <tr className="border-b border-border">
          <th className="text-left px-4 py-2">Address</th>
          <th className="text-left px-4 py-2">Reason</th>
          <th className="text-left px-4 py-2">Source</th>
          <th className="text-left px-4 py-2">Last seen</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {rows.map((s) => (
          <tr key={s.address} className="border-b border-border">
            <td className="px-4 py-2">{stripScheme(s.address)}</td>
            <td className="px-4 py-2"><StatusPill status={s.reason} /></td>
            <td className="px-4 py-2 text-text-dim">{s.source}</td>
            <td className="px-4 py-2 text-text-dim">{shortTime(s.last_seen)}</td>
            <td className="px-4 py-2 text-right">
              <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => remove(s.address)}>Remove</button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ─── senders_create result renderer ──────────────────────────────

interface SendersCreateStep {
  step: string;
  ok: boolean;
  detail?: string;
  skipped?: string;
  error?: string;
}

interface SendersCreateInboundInfo {
  bootstrapped: boolean;
  skipped_reason?: string;
  bucket_name?: string;
  topic_arn?: string;
  account_id?: string;
  webhook_url?: string;
  subscription_arn?: string;
  region?: string;
  rule_set_name?: string;
  rule_name?: string;
}

interface SendersCreateResp {
  address: string;
  kind: "email" | "domain" | "phone";
  pending: boolean;
  next_step?: string;
  dkim_tokens?: string[];
  dkim_status?: string;
  dns_records?: { name: string; type: string; value: string }[];
  inbound?: SendersCreateInboundInfo;
  steps: SendersCreateStep[];
}

function SendersCreateResult({ result, onDismiss }: { result: SendersCreateResp; onDismiss: () => void }) {
  const failed = result.steps.filter((s) => !s.ok).length;
  return (
    <div className="m-4 p-3 rounded border border-border bg-surface-2 text-sm space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <div>
            <strong>{stripScheme(result.address)}</strong>
            <span className="text-text-dim text-xs ml-2">({result.kind})</span>
            {failed === 0 && <span className="ml-2 inline-block"><StatusPill status="ok" /></span>}
            {failed > 0 && (
              <span className="ml-2 inline-block">
                <span className="bg-red-500/20 text-red-400 text-xs px-2 py-0.5 rounded">{failed} step{failed === 1 ? "" : "s"} failed</span>
              </span>
            )}
          </div>
          {result.next_step && <div className="text-text-dim text-xs mt-1">{result.next_step}</div>}
        </div>
        <button type="button" className="text-text-dim hover:text-text" onClick={onDismiss}>×</button>
      </div>

      {result.inbound && (result.inbound.bootstrapped || result.inbound.skipped_reason) && (
        <div className="rounded border border-border p-2 text-xs space-y-1">
          <div className="uppercase tracking-wide text-text-dim text-[10px]">
            {result.kind === "phone" ? "Twilio inbound" : "Email inbound"}
          </div>
          {result.inbound.bootstrapped ? (
            <>
              {result.kind === "phone" && (
                <div className="text-green-400">Webhook wired</div>
              )}
              {result.inbound.bucket_name && (
                <div>S3 bucket: <span className="font-mono">{result.inbound.bucket_name}</span></div>
              )}
              {result.inbound.topic_arn && (
                <div>SNS topic: <span className="font-mono break-all">{result.inbound.topic_arn}</span></div>
              )}
              {result.inbound.webhook_url && (
                <div>Webhook: <span className="font-mono break-all">{result.inbound.webhook_url}</span></div>
              )}
              {result.inbound.subscription_arn && (
                <div className="text-text-dim">Subscription: <span className="font-mono break-all">{result.inbound.subscription_arn}</span></div>
              )}
            </>
          ) : (
            <div className="text-yellow-300">Skipped: {result.inbound.skipped_reason}</div>
          )}
        </div>
      )}

      {result.dns_records && result.dns_records.length > 0 && (
        <div>
          <div className="text-xs uppercase tracking-wide text-text-dim mb-1">DKIM CNAMEs</div>
          <table className="text-xs font-mono w-full">
            <tbody>
              {result.dns_records.map((r, i) => (
                <tr key={i} className="border-b border-border">
                  <td className="px-2 py-0.5 break-all">{r.name}</td>
                  <td className="px-2 py-0.5 text-text-dim">{r.type}</td>
                  <td className="px-2 py-0.5 break-all">{r.value}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <details className="text-xs">
        <summary className="cursor-pointer text-text-dim">Step details ({result.steps.length})</summary>
        <table className="w-full font-mono mt-1">
          <tbody>
            {result.steps.map((s, i) => (
              <tr key={i} className="border-b border-border">
                <td className="px-2 py-1 w-20">
                  <StatusPill status={s.error ? "failed" : s.skipped ? "pending" : "ok"} />
                </td>
                <td className="px-2 py-1 text-text-dim">{s.step}</td>
                <td className="px-2 py-1 text-text-dim break-all">{s.error || s.skipped || s.detail || ""}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}

// ─── Tiny UI primitives ──────────────────────────────────────────

function StatusPill({ status }: { status: string }) {
  return <span className={`inline-block px-1.5 py-0.5 rounded text-xs ${statusPillClass(status)}`} style={statusPillStyle(status)}>{status}</span>;
}

function statusPillClass(status: string): string {
  switch (status) {
    case "sent":
    case "delivered":
    case "ok":
    case "received":
    case "verified":
    case "approved":
    case "SUCCESS":
      return "bg-green-500/20 text-green-400 border-green-500/30";
    case "opened":
      return "bg-blue-500/20 text-blue-300 border-blue-500/30";
    case "clicked":
      return "bg-violet-500/20 text-violet-300 border-violet-500/30";
    case "pending":
    case "no_match":
    case "PENDING":
    case "delivery_delayed":
    case "draft":
      return "bg-yellow-500/20 text-yellow-400 border-yellow-500/30";
    case "bounced":
    case "complained":
    case "complaint":
    case "hard-bounce":
    case "failed":
    case "target_failed":
    case "rejected":
    case "rendering_failed":
    case "deleted":
      return "bg-red-500/20 text-red-400 border-red-500/30";
    case "manual":
      return "bg-sky-500/20 text-sky-300 border-sky-500/30";
    default:
      return "bg-surface-2 text-text-dim border-border";
  }
}

function statusPillStyle(status: string): React.CSSProperties | undefined {
  switch (status) {
    case "opened":
      return {
        backgroundColor: "rgba(59, 130, 246, 0.2)",
        borderColor: "rgba(59, 130, 246, 0.3)",
        color: "#93c5fd",
      };
    case "clicked":
      return {
        backgroundColor: "rgba(139, 92, 246, 0.2)",
        borderColor: "rgba(139, 92, 246, 0.3)",
        color: "#c4b5fd",
      };
    case "delivery_delayed":
      return {
        backgroundColor: "rgba(234, 179, 8, 0.2)",
        borderColor: "rgba(234, 179, 8, 0.3)",
        color: "#facc15",
      };
    default:
      return undefined;
  }
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}{hint && <span className="ml-2 italic">{hint}</span>}</div>
      {children}
    </label>
  );
}

function DL({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex gap-2 py-1 border-b border-border/50">
      <div className="w-24 text-text-dim text-xs uppercase tracking-wide pt-0.5">{label}</div>
      <div className="flex-1 break-words">{value}</div>
    </div>
  );
}

// inputCls is the shared style for every text/select/textarea control
// in this panel. The dashboard ships an `.input` utility class but its
// default uses light-mode colours that wash out in dark themes. We
// define our own with the panel's own design tokens (bg-surface-2,
// text-text, border-border) so contrast stays correct in both modes.
const inputCls =
  "w-full bg-surface-2 text-text border border-border rounded px-3 py-1.5 " +
  "placeholder:text-text-dim/70 focus:outline-none focus:ring-1 focus:ring-accent " +
  "disabled:opacity-50 disabled:cursor-not-allowed";

function stripScheme(s: string): string {
  if (!s) return "";
  if (s.startsWith("mailto:")) return s.slice(7);
  if (s.startsWith("tel:")) return s.slice(4);
  return s;
}

// parseSendersError unwraps the "<status>: <body>" shape that api()
// throws so the panel shows just the meaningful bit. The body is
// usually `{"error":"..."}` JSON from the Go side.
function parseSendersError(raw: string): string {
  if (!raw) return "";
  // raw is like "502: {\"error\":\"no email_provider bound — install/select…\"}"
  const colon = raw.indexOf(": ");
  const body = colon >= 0 ? raw.slice(colon + 2) : raw;
  try {
    const parsed = JSON.parse(body);
    if (typeof parsed.error === "string") return parsed.error;
  } catch {}
  return body;
}

function shortTime(s?: string): string {
  if (!s) return "";
  try {
    const d = new Date(s);
    const now = new Date();
    const sameDay = d.toDateString() === now.toDateString();
    return sameDay
      ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
      : d.toLocaleDateString([], { month: "short", day: "numeric" });
  } catch {
    return s;
  }
}

function ConfirmDialog({ request, onClose }: { request: ConfirmDialogRequest; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const confirmLabel = request.confirmLabel || "Confirm";
  const cancelLabel = request.cancelLabel || "Cancel";
  const danger = request.tone === "danger";

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose]);

  const onConfirm = async () => {
    setBusy(true);
    try {
      await request.onConfirm();
      onClose();
    } catch {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 px-4" role="presentation" onMouseDown={(e) => { if (e.target === e.currentTarget && !busy) onClose(); }}>
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="messaging-confirm-title"
        className="w-full max-w-md rounded border border-border bg-surface text-text shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="px-5 py-4 border-b border-border">
          <div id="messaging-confirm-title" className="font-semibold">{request.title}</div>
        </div>
        <div className="px-5 py-4 text-sm leading-6">
          {request.message}
        </div>
        <div className="px-5 py-4 border-t border-border flex justify-end gap-2">
          <button
            type="button"
            className="px-3 py-1.5 rounded border border-border text-text-dim hover:text-text hover:bg-surface-2 disabled:opacity-50"
            onClick={onClose}
            disabled={busy}
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            className={`px-3 py-1.5 rounded text-white disabled:opacity-50 ${danger ? "bg-red-600 hover:bg-red-500" : "bg-accent hover:opacity-90"}`}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? "Working…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

// Panel-local toast. Replaces native browser dialogs so a failed delete /
// re-check / add doesn't yank focus into a modal that blocks the rest
// of the UI. Auto-dismisses (see notice effect in MessagingPanel) and
// can be cleared manually via the × button.
function Toast({ notice, onDismiss }: { notice: Notice; onDismiss: () => void }) {
  const palette = notice.kind === "error"
    ? "border-red-500/30 bg-red-500/10 text-red-300"
    : "border-accent/30 bg-accent/10 text-text";
  return (
    <div
      role="status"
      aria-live="polite"
      className={`mx-4 mt-3 px-3 py-2 rounded border ${palette} flex items-start gap-2 text-sm`}
    >
      <span className="flex-1 whitespace-pre-wrap break-words">{notice.text}</span>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className="text-text-dim hover:text-text shrink-0"
      >×</button>
    </div>
  );
}
