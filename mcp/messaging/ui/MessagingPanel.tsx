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
  headers?: Record<string, string>;
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
  attachments?: MessageAttachment[];
  created_at?: string;
  sent_at?: string;
  received_at?: string;
  last_event_at?: string;
  event_counts?: Record<string, number>;
}
interface MessageAttachment {
  processing_status?: string;
  processing_error?: string;
  id?: number;
  storage_id?: number;
  url?: string;
  filename: string;
  content_type?: string;
  size_bytes?: number;
  content_id?: string;
  disposition?: string;
  source?: string;
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
  vars_schema?: Record<string, unknown>;
  provider_template_id?: string;
  provider_status?: string;
  var_style?: string;
  last_synced_at?: string;
  created_at?: string;
  updated_at?: string;
}
interface ProviderTemplateRow {
  provider_template_id: string;
  name: string;
  language?: string;
  category?: string;
  status: string;
  body_text?: string;
  variables?: Record<string, unknown>;
  local_id?: number;
  local_state: "new" | "imported" | "changed";
}
interface InboundRoute {
  channel?: string;
  id: number;
  pattern: string;
  target_app: string;
  target_route: string;
  priority: number;
}
interface SuppressionRow {
  channel: string;
  kind?: string;
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
interface MessageListResponse {
  messages: MessageRow[];
  count?: number;
  total?: number;
  limit?: number;
  offset?: number;
  has_more?: boolean;
}
interface MessagePageState {
  limit: number;
  offset: number;
  total: number;
  hasMore: boolean;
}
interface MessageFilters {
  q: string;
  channel: string;
  status: string;
  address: string;
  since: string;
}
interface ConfirmDialogRequest {
  title: string;
  message: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: ConfirmTone;
  onConfirm: () => void | Promise<void>;
}
type ConfirmFn = (request: ConfirmDialogRequest) => void;
const MESSAGE_PAGE_SIZE = 100;
const EMPTY_MESSAGE_FILTERS: MessageFilters = {
  q: "",
  channel: "",
  status: "",
  address: "",
  since: "",
};
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
interface ComposeAttachment {
  key: string;
  filename: string;
  content_type: string;
  size_bytes: number;
  content_base64: string;
}

function hasMessageFilters(filters: MessageFilters): boolean {
  return Object.values(filters).some((value) => value !== "");
}

function messageListParams(
  direction: "in" | "out",
  page: MessagePageState,
  filters: MessageFilters,
): Record<string, string> {
  const params: Record<string, string> = {
    direction,
    limit: String(page.limit),
    offset: String(page.offset),
  };
  for (const [key, value] of Object.entries(filters)) {
    if (value) params[key] = value;
  }
  return params;
}

// ─── Component ────────────────────────────────────────────────────
export default function MessagingPanel(props: NativePanelProps) { return <MessagingWorkspace key={`${props.projectId}:${props.installId}`} {...props}/>; }
function MessagingWorkspace({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("outbox");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  const [outbox, setOutbox] = useState<MessageRow[]>([]);
  const [inbox, setInbox] = useState<MessageRow[]>([]);
  const [outboxCount, setOutboxCount] = useState(0);
  const [inboxCount, setInboxCount] = useState(0);
  const [outboxFilters, setOutboxFilters] = useState<MessageFilters>(EMPTY_MESSAGE_FILTERS);
  const [inboxFilters, setInboxFilters] = useState<MessageFilters>(EMPTY_MESSAGE_FILTERS);
  const [outboxPage, setOutboxPage] = useState<MessagePageState>({
    limit: MESSAGE_PAGE_SIZE,
    offset: 0,
    total: 0,
    hasMore: false,
  });
  const [inboxPage, setInboxPage] = useState<MessagePageState>({
    limit: MESSAGE_PAGE_SIZE,
    offset: 0,
    total: 0,
    hasMore: false,
  });
  const [templates, setTemplates] = useState<TemplateRow[]>([]);
  const [routes, setRoutes] = useState<InboundRoute[]>([]);
  const [suppressionOffset, setSuppressionOffset] = useState(0);
  const [suppressionTotal, setSuppressionTotal] = useState(0);
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
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      let detail = text;
      try {
        const payload = JSON.parse(text) as {
          error?: string | {
            code?: string;
            address?: string;
            matched?: string;
            kind?: string;
            reason?: string;
            source?: string;
          };
        };
        if (payload.error && typeof payload.error === "object" && payload.error.code === "recipient_suppressed") {
          const error = payload.error;
          detail = `${error.address || "Recipient"} is suppressed: ${error.reason || "blocked"}` +
            `${error.matched ? ` (matched ${error.kind || "entry"} ${error.matched}` : ""}` +
            `${error.source ? `, source ${error.source}` : ""}` +
            `${error.matched ? ")" : ""}`;
        } else if (typeof payload.error === "string") {
          detail = payload.error;
        }
      } catch {}
      throw new Error(`${res.status}: ${detail}`);
    }
    return res.json();
  }, [withParams]);

  const outboxGeneration = useRef(0);
  const inboxGeneration = useRef(0);
  const detailGeneration = useRef(0);
  const quotaReadAt = useRef(0);
  const loadOutbox = useCallback(async () => {
    const generation = ++outboxGeneration.current;
    const params = {...messageListParams("out", outboxPage, outboxFilters), summary:"true"};
    const [r, unfiltered] = await Promise.all([
      api<MessageListResponse>("GET", "/messages", params),
      hasMessageFilters(outboxFilters)
        ? api<MessageListResponse>("GET", "/messages", { direction: "out", limit: "1", offset: "0" })
        : Promise.resolve(null),
    ]);
    if (generation !== outboxGeneration.current) return;
    setOutboxCount(unfiltered?.total ?? r.total ?? (r.messages || []).length);
    setOutbox(r.messages || []);
    setOutboxPage((p) => ({
      ...p,
      total: typeof r.total === "number" ? r.total : (r.messages || []).length,
      hasMore: !!r.has_more,
    }));
  }, [api, outboxPage.limit, outboxPage.offset, outboxFilters]);
  const loadInbox = useCallback(async () => {
    const generation = ++inboxGeneration.current;
    const params = {...messageListParams("in", inboxPage, inboxFilters), summary:"true"};
    const [r, unfiltered] = await Promise.all([
      api<MessageListResponse>("GET", "/messages", params),
      hasMessageFilters(inboxFilters)
        ? api<MessageListResponse>("GET", "/messages", { direction: "in", limit: "1", offset: "0" })
        : Promise.resolve(null),
    ]);
    if (generation !== inboxGeneration.current) return;
    setInboxCount(unfiltered?.total ?? r.total ?? (r.messages || []).length);
    setInbox(r.messages || []);
    setInboxPage((p) => ({
      ...p,
      total: typeof r.total === "number" ? r.total : (r.messages || []).length,
      hasMore: !!r.has_more,
    }));
  }, [api, inboxPage.limit, inboxPage.offset, inboxFilters]);
  const loadTemplates = useCallback(async () => {
    const all: TemplateRow[]=[];
    for(let offset=0;;offset+=200) {
      const r=await api<{templates:TemplateRow[];has_more:boolean}>("POST","/tools/call",{},{tool:"template_list",args:{limit:200,offset}});
      all.push(...r.templates);if(!r.has_more) break;
    }
    setTemplates(all);
  },[api]);
  const loadRoutes = useCallback(async () => {
    const r = await api<{ routes: InboundRoute[] }>("GET", "/inbound-routes", {});
    setRoutes(r.routes || []);
  }, [api]);
  const loadSuppressions = useCallback(async () => {
    const r = await api<{ suppressions: SuppressionRow[]; total: number }>("GET", "/suppressions", {limit:"100",offset:String(suppressionOffset)});
    setSuppressionTotal(r.total);
    setSuppressions(r.suppressions || []);
  }, [api, suppressionOffset]);
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
    if (Date.now() - quotaReadAt.current < 60000) return;
    quotaReadAt.current = Date.now();
    try {
      const q = await api<QuotaInfo>("GET", "/senders/quota", {});
      setQuota(q);
    } catch {
      setQuota(null);
    }
  }, [api]);
  const loadMessageDetail = useCallback(async (id: number) => {
    const generation = ++detailGeneration.current;
    const r = await api<{ message: MessageRow; events: DeliveryEvent[] }>("GET", `/messages/${id}`, {});
    if (generation !== detailGeneration.current) return;
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

  // Coalesce provider-specific + generic notifications without refreshing provider inventory.
  const liveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const liveRefresh = useRef(() => {});
  liveRefresh.current = () => {
    const work: Promise<unknown>[] = [];
    if (tab === "inbox" || tab === "compose") work.push(loadInbox());
    if (tab === "outbox") work.push(loadOutbox());
    if (tab === "templates" || tab === "compose") work.push(loadTemplates());
    if (tab === "suppressions") work.push(loadSuppressions());
    if (selectedRef.current) work.push(loadMessageDetail(selectedRef.current.id));
    void Promise.all(work).catch((e) => setStatus((e as Error).message));
  };
  useEffect(() => () => { if (liveTimer.current) clearTimeout(liveTimer.current); }, []);
  useAppEvents("messaging", projectId, (ev) => {
    if ((messagingLiveTopics.has(ev.topic) || ev.topic === "suppression.changed") && !liveTimer.current) {
      liveTimer.current = setTimeout(() => { liveTimer.current = null; liveRefresh.current(); }, 200);
    }
  });

  const openMessage = useCallback(async (m: MessageRow) => {
    setSelected(m);
    setSelectedEvents([]);
    try {
      await loadMessageDetail(m.id);
    } catch {}
  }, [loadMessageDetail]);

  const pageOutbox = useCallback((delta: number) => {
    setOutboxPage((p) => ({
      ...p,
      offset: Math.max(0, p.offset + delta * p.limit),
    }));
  }, []);
  const pageInbox = useCallback((delta: number) => {
    setInboxPage((p) => ({
      ...p,
      offset: Math.max(0, p.offset + delta * p.limit),
    }));
  }, []);

  const filterOutbox = useCallback((filters: MessageFilters) => {
    outboxGeneration.current++;detailGeneration.current++;
    setSelected(null);
    setOutboxFilters(filters);
    setOutboxPage((p) => ({ ...p, offset: 0 }));
  }, []);
  const filterInbox = useCallback((filters: MessageFilters) => {
    inboxGeneration.current++;detailGeneration.current++;
    setSelected(null);
    setInboxFilters(filters);
    setInboxPage((p) => ({ ...p, offset: 0 }));
  }, []);

  const counts = useMemo(() => ({
    outbox: outboxCount,
    inbox: inboxCount,
    templates: templates.length,
    routes: routes.length,
    suppressions: suppressionTotal,
    senders: senders.length,
  }), [outboxCount, inboxCount, templates, routes, suppressionTotal, senders]);

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
          {tab === "outbox" && (
            <MessageList
              rows={outbox}
              page={outboxPage}
              direction="out"
              filters={outboxFilters}
              onFilters={filterOutbox}
              onPage={pageOutbox}
              onSelect={openMessage}
              selectedId={selected?.id}
            />
          )}
          {tab === "inbox" && (
            <MessageList
              rows={inbox}
              page={inboxPage}
              direction="in"
              filters={inboxFilters}
              onFilters={filterInbox}
              onPage={pageInbox}
              onSelect={openMessage}
              selectedId={selected?.id}
            />
          )}
          {tab === "compose" && (
            <ComposeView key={projectId}
              api={api}
              senders={verifiedSenders}
              templates={templates}
              quota={quota}
              inbox={inbox}
              draft={composeDraft}
              onSent={() => { setComposeDraft(null); reload(); setTab("outbox"); }}
              gotoSenders={() => setTab("senders")}
              gotoTemplates={() => setTab("templates")}
            />
          )}
          {tab === "senders" && <SendersView rows={senders} identities={identities} quota={quota} api={api} reload={reload} error={sendersError} notify={notify} confirmAction={confirmAction} />}
          {tab === "templates" && <TemplatesView rows={templates} api={api} reload={reload} notify={notify} confirmAction={confirmAction} />}
          {tab === "routes" && <RoutesView rows={routes} api={api} reload={reload} notify={notify} confirmAction={confirmAction} />}
          {tab === "suppressions" && <><div className="p-3 flex gap-3"><button disabled={suppressionOffset===0} onClick={() => setSuppressionOffset(Math.max(0,suppressionOffset-100))}>Previous</button><span>{Math.min(suppressionOffset+1,suppressionTotal)}–{Math.min(suppressionOffset+100,suppressionTotal)} of {suppressionTotal}</span><button disabled={suppressionOffset+100>=suppressionTotal} onClick={() => setSuppressionOffset(suppressionOffset+100)}>Next</button></div><SuppressionsView rows={suppressions} api={api} reload={reload} notify={notify} confirmAction={confirmAction} /></>}
        </div>

        {/* Detail pane (only meaningful for messages) */}
        {(tab === "outbox" || tab === "inbox") && selected && (
          <MessageDetail key={selected.id} api={api} reload={reload}
            m={selected}
            events={selectedEvents}
            onReply={replyToMessage}
            onClose={() => {detailGeneration.current++;setSelected(null);}}
          />
        )}
      </div>
    </div>
  );
}

// ─── Subviews ─────────────────────────────────────────────────────

function MessageList({
  rows, page, direction, filters, onFilters, onPage, onSelect, selectedId,
}: {
  rows: MessageRow[];
  page: MessagePageState;
  direction: "in" | "out";
  filters: MessageFilters;
  onFilters: (filters: MessageFilters) => void;
  onPage: (delta: number) => void;
  onSelect: (m: MessageRow) => void;
  selectedId?: number;
}) {
  const [draft, setDraft] = useState<MessageFilters>(filters);
  useEffect(() => setDraft(filters), [filters]);
  const update = (field: keyof MessageFilters, value: string) => {
    setDraft((current) => ({ ...current, [field]: value }));
  };
  const apply = (event: React.FormEvent) => {
    event.preventDefault();
    onFilters({
      q: draft.q.trim(),
      channel: draft.channel,
      status: draft.status,
      address: draft.address.trim(),
      since: draft.since,
    });
  };
  const clear = () => {
    const empty = { ...EMPTY_MESSAGE_FILTERS };
    setDraft(empty);
    onFilters(empty);
  };
  const statusOptions = direction === "in"
    ? ["received"]
    : ["pending", "sent", "delivered", "opened", "clicked", "bounced", "complained", "rejected", "failed"];
  return (
    <div>
      <form onSubmit={apply} className="px-4 py-3 border-b border-border flex items-end gap-2 flex-wrap">
        <label className="min-w-[15rem] flex-1">
          <span className="block text-xs text-text-dim mb-1">Search</span>
          <input
            type="search"
            value={draft.q}
            onChange={(e) => update("q", e.target.value)}
            placeholder="Sender, recipient, subject, or body"
            className="w-full px-2.5 py-1.5 rounded border border-border bg-surface text-sm"
          />
        </label>
        <label>
          <span className="block text-xs text-text-dim mb-1">Channel</span>
          <select
            value={draft.channel}
            onChange={(e) => update("channel", e.target.value)}
            className="px-2.5 py-1.5 rounded border border-border bg-surface text-sm"
          >
            <option value="">All</option>
            <option value="email">Email</option>
            <option value="sms">SMS</option>
            <option value="whatsapp">WhatsApp</option>
          </select>
        </label>
        <label>
          <span className="block text-xs text-text-dim mb-1">Status</span>
          <select
            value={draft.status}
            onChange={(e) => update("status", e.target.value)}
            className="px-2.5 py-1.5 rounded border border-border bg-surface text-sm"
          >
            <option value="">All</option>
            {statusOptions.map((value) => <option key={value} value={value}>{value}</option>)}
          </select>
        </label>
        <label className="min-w-[13rem]">
          <span className="block text-xs text-text-dim mb-1">Exact address</span>
          <input
            type="text"
            value={draft.address}
            onChange={(e) => update("address", e.target.value)}
            placeholder="name@example.com"
            className="w-full px-2.5 py-1.5 rounded border border-border bg-surface text-sm"
          />
        </label>
        <label>
          <span className="block text-xs text-text-dim mb-1">Since</span>
          <input
            type="date"
            value={draft.since}
            onChange={(e) => update("since", e.target.value)}
            className="px-2.5 py-1.5 rounded border border-border bg-surface text-sm"
          />
        </label>
        <button type="submit" className="px-3 py-1.5 rounded bg-accent text-white text-sm">Apply</button>
        <button type="button" onClick={clear} className="px-3 py-1.5 rounded border border-border text-sm hover:bg-surface-2">Clear</button>
      </form>
      <MessagePager page={page} rowCount={rows.length} onPage={onPage} />
      {rows.length === 0 ? (
        <div className="p-6 text-text-dim text-sm">No messages.</div>
      ) : (
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
      )}
    </div>
  );
}

function MessagePager({ page, rowCount, onPage }: { page: MessagePageState; rowCount: number; onPage: (delta: number) => void }) {
  const from = page.total === 0 ? 0 : page.offset + 1;
  const to = Math.min(page.offset + rowCount, page.total);
  return (
    <div className="px-4 py-2 border-b border-border flex items-center justify-between gap-3 text-xs text-text-dim">
      <span>
        Showing <span className="text-text">{from}-{to}</span> of <span className="text-text">{page.total}</span>
      </span>
      <div className="flex items-center gap-2">
        <button
          type="button"
          className="px-2 py-1 rounded border border-border hover:bg-surface-2 disabled:opacity-50 disabled:hover:bg-transparent"
          disabled={page.offset <= 0}
          onClick={() => onPage(-1)}
        >
          Previous
        </button>
        <button
          type="button"
          className="px-2 py-1 rounded border border-border hover:bg-surface-2 disabled:opacity-50 disabled:hover:bg-transparent"
          disabled={!page.hasMore}
          onClick={() => onPage(1)}
        >
          Next
        </button>
      </div>
    </div>
  );
}

function MessageDetail({ m, events, onReply, onClose, api, reload }: { api: <T,>(m: string,p: string,q?: Record<string,string>,b?: unknown) => Promise<T>; reload: () => void; m: MessageRow; events: DeliveryEvent[]; onReply: (m: MessageRow) => void; onClose: () => void }) {
  const [actionError, setActionError] = useState("");
  const download = async (att: MessageAttachment) => {
    const tab = window.open("about:blank", "_blank");
    if (!tab) { setActionError("Allow popups to download this attachment."); return; }
    tab.opener = null;
    try { const result = await api<{url:string}>("GET", `/messages/${m.id}/attachments/${att.id}`, {}); const url = new URL(result.url); if (!["https:","http:"].includes(url.protocol)) throw new Error("Invalid attachment URL"); tab.location.replace(url.href); } catch(e) { tab.close(); setActionError((e as Error).message); }
  };
  const retry = async () => {try {await api("POST", "/tools/call", {}, {tool:"inbound_redispatch",args:{id:m.id}});reload();}catch(e){setActionError((e as Error).message)}};
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
      {actionError && <p role="alert" className="text-red-500">{actionError}</p>}
      {m.direction === "in" && ["no_match","target_failed","failed","pending"].includes(m.route_status || "") && <button type="button" className="text-accent" onClick={retry}>Retry processing</button>}
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

      {(m.body_text || m.body_html) && (
        <EmailBodyViewer key={m.id} bodyText={m.body_text || ""} bodyHTML={m.body_html || ""} />
      )}
      {m.attachments && m.attachments.length > 0 && (
        <div className="mt-4">
          <div className="text-text-dim text-xs uppercase tracking-wide mb-1">Attachments</div>
          <ul className="rounded border border-border divide-y divide-border">
            {m.attachments.map((att, i) => (
              <li key={att.id || i} className="px-3 py-2 text-xs">
                <div className="font-medium break-all">{att.filename || "attachment"}</div>
                <div className="text-text-dim">
                  {att.content_type || "application/octet-stream"}
                  {att.size_bytes ? ` · ${formatBytes(att.size_bytes)}` : ""}
                  {att.source ? ` · ${att.source}` : ""}
                </div>
                {att.processing_status && att.processing_status !== "ready" && <div className="text-yellow-500">{att.processing_status}: {att.processing_error}</div>}
                {att.storage_id && <button type="button" className="text-accent" onClick={() => download(att)}>Download</button>}
                {att.url && (
                  <a className="text-accent break-all" href={att.url} target="_blank" rel="noreferrer">Open URL</a>
                )}
              </li>
            ))}
          </ul>
        </div>
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

export function EmailBodyViewer({ bodyText, bodyHTML }: { bodyText: string; bodyHTML: string }) {
  const [mode, setMode] = useState<"rendered" | "text" | "source">(bodyHTML ? "rendered" : "text");
  const [loadRemoteImages, setLoadRemoteImages] = useState(false);
  const srcDoc = useMemo(
    () => safeEmailHTMLDocument(bodyHTML, { loadRemoteImages }),
    [bodyHTML, loadRemoteImages],
  );

  const hasHTML = bodyHTML.trim() !== "";
  const hasText = bodyText.trim() !== "";

  useEffect(() => {
    if (!hasHTML && mode !== "text") setMode("text");
  }, [hasHTML, mode]);

  return (
    <section className="mt-4">
      <div className="flex items-center justify-between gap-3 mb-2">
        <div className="text-text-dim text-xs uppercase tracking-wide">Body</div>
        <div className="flex items-center gap-2 flex-wrap justify-end">
          {hasHTML && (
            <label className="flex items-center gap-1 text-xs text-text-dim">
              <input
                type="checkbox"
                checked={loadRemoteImages}
                onChange={(e) => setLoadRemoteImages(e.target.checked)}
              />
              images
            </label>
          )}
          <div className="inline-flex rounded border border-border overflow-hidden text-xs">
            {hasHTML && (
              <button
                type="button"
                className={bodyModeButtonClass(mode === "rendered")}
                onClick={() => setMode("rendered")}
              >
                Rendered
              </button>
            )}
            {hasText && (
              <button
                type="button"
                className={bodyModeButtonClass(mode === "text")}
                onClick={() => setMode("text")}
              >
                Text
              </button>
            )}
            {hasHTML && (
              <button
                type="button"
                className={bodyModeButtonClass(mode === "source")}
                onClick={() => setMode("source")}
              >
                Source
              </button>
            )}
          </div>
        </div>
      </div>

      {mode === "rendered" && hasHTML && (
        <iframe
          title="Rendered email body"
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          referrerPolicy="no-referrer"
          className="w-full h-[34rem] bg-white rounded border border-border"
          srcDoc={srcDoc}
        />
      )}
      {mode === "text" && (
        <pre className="whitespace-pre-wrap break-words text-text bg-surface-2 p-3 rounded border border-border">
          {bodyText || "(no text body)"}
        </pre>
      )}
      {mode === "source" && hasHTML && (
        <pre className="whitespace-pre-wrap break-all text-text bg-surface-2 p-3 rounded border border-border text-xs">
          {bodyHTML}
        </pre>
      )}
    </section>
  );
}

function bodyModeButtonClass(active: boolean): string {
  return active
    ? "px-2 py-1 bg-surface-2 text-text"
    : "px-2 py-1 text-text-dim hover:text-text hover:bg-surface-2";
}

export function safeEmailHTMLDocument(rawHTML: string, opts: { loadRemoteImages: boolean }): string {
  if (!rawHTML.trim()) return "";
  try {
    const parser = new DOMParser();
    const doc = parser.parseFromString(rawHTML, "text/html");

    doc.querySelectorAll("script, iframe, object, embed, applet, form, input, button, textarea, select, meta, base, link").forEach((el) => el.remove());
    doc.querySelectorAll("style").forEach((el) => {
      el.textContent = stripUnsafeCSS(el.textContent || "", opts.loadRemoteImages);
    });

    doc.querySelectorAll<HTMLElement>("*").forEach((el) => {
      for (const attr of Array.from(el.attributes)) {
        const name = attr.name.toLowerCase();
        const value = attr.value.trim();
        if (name.startsWith("on")) {
          el.removeAttribute(attr.name);
          continue;
        }
        if (name === "style") {
          el.setAttribute(attr.name, stripUnsafeCSS(value, opts.loadRemoteImages));
          continue;
        }
        if (["href", "src", "xlink:href", "action", "formaction", "poster", "background"].includes(name)) {
          if (!isSafeURL(value)) {
            el.removeAttribute(attr.name);
            continue;
          }
          if (!opts.loadRemoteImages && isRemoteURL(value) && ["src", "poster", "background"].includes(name)) {
            el.removeAttribute(attr.name);
            continue;
          }
        }
        if (["srcset", "imagesrcset"].includes(name) && (!opts.loadRemoteImages || containsUnsafeSrcSet(value))) {
          el.removeAttribute(attr.name);
        }
      }

      if (el.tagName.toLowerCase() === "a") {
        el.setAttribute("target", "_blank");
        el.setAttribute("rel", "noopener noreferrer nofollow");
      }
    });

    const body = doc.body ? doc.body.innerHTML : rawHTML;
    return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src ${opts.loadRemoteImages ? 'https: http: data:' : 'data:'}; font-src 'none'; media-src 'none'; connect-src 'none'; base-uri 'none'; form-action 'none'">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  html, body { margin: 0; padding: 0; background: #fff; color: #111; }
  body { padding: 16px; overflow-wrap: anywhere; }
  img, table { max-width: 100% !important; }
  pre { white-space: pre-wrap; }
</style>
</head>
<body>${body}</body>
</html>`;
  } catch {
    return `<!doctype html><html><body><pre>${escapeHTML(rawHTML)}</pre></body></html>`;
  }
}

function stripUnsafeCSS(value: string, loadRemoteImages: boolean): string {
  let css = value
    .replace(/@import[^;]+;/gi, "")
    .replace(/expression\s*\([^)]*\)/gi, "")
    .replace(/url\s*\(\s*(['"]?)\s*javascript:[^)]+\)/gi, "")
    .replace(/url\s*\(\s*(['"]?)\s*data:text\/html[^)]+\)/gi, "");
  if (!loadRemoteImages) {
    css = css.replace(/url\s*\(\s*(['"]?)\s*https?:[^)]+\)/gi, "");
  }
  return css;
}

function isSafeURL(value: string): boolean {
  if (!value || value.startsWith("#")) return true;
  try {
    const parsed = new URL(value, "https://email.local");
    return ["http:", "https:", "mailto:", "tel:", "cid:"].includes(parsed.protocol);
  } catch {
    return false;
  }
}

function isRemoteURL(value: string): boolean {
  try {
    const parsed = new URL(value, "https://email.local");
    return parsed.protocol === "http:" || parsed.protocol === "https:";
  } catch {
    return false;
  }
}

function containsUnsafeSrcSet(value: string): boolean {
  return value.split(",").some((part) => !isSafeURL(part.trim().split(/\s+/)[0] || ""));
}

function escapeHTML(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
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
  "message.suppressed",
  "message.delivery_delayed",
  "message.rendering_failed",
  "message.subscription_changed",
  "message.event",
  "message.processed",
  "templates.synced",
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

function replyAddressList(value: string): string {
  // Keep quoted display-name commas out of the recipient tokenizer.
  const mailboxes = [...value.matchAll(/<([^<>]+)>|([^,;<>]+@[^,;<>]+)/g)].map(match => stripScheme((match[1] || match[2]).trim()));
  return mailboxes.length ? mailboxes.join(", ") : stripScheme(value);
}

export function composeDraftFromMessage(m: MessageRow, senders: SenderRow[]): ComposeDraft | null {
  const from = replyFromAddress(m, senders);
  if (!from) return null;
  const inReplyTo = m.message_id_header || "";
  const refs = uniqueStrings([...(m.references || []), ...(inReplyTo ? [inReplyTo] : [])]);
  return {
    key: m.id,
    channel: m.channel || "email",
    from,
    to: replyAddressList(Object.entries(m.headers || {}).find(([key]) => key.toLowerCase() === "reply-to")?.[1] || m.from),
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
    if (senders.some((s) => s.channel === m.channel && senderCanSendAddress(s, candidate))) {
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

export function ComposeView({
  api, senders, templates, quota, inbox, draft, onSent, gotoSenders, gotoTemplates,
}: {
  api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  senders: SenderRow[];
  templates: TemplateRow[];
  quota: QuotaInfo | null;
  inbox: MessageRow[];
  draft: ComposeDraft | null;
  onSent: () => void;
  gotoSenders: () => void;
  gotoTemplates: () => void;
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
  const [selectedChannel, setSelectedChannel] = useState<string>("");
  const [localPart, setLocalPart] = useState("");
  const [to, setTo] = useState("");
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [inReplyTo, setInReplyTo] = useState("");
  const [references, setReferences] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [waSession, setWaSession] = useState<WhatsAppSessionState>({ state: "idle", checkedRecipients: [] });
  const [templateID, setTemplateID] = useState("");
  const [templateVars, setTemplateVars] = useState<Record<string, string>>({});
  const [attachments, setAttachments] = useState<ComposeAttachment[]>([]);

  const initializedDraft = useRef<number | null>(null);
  const sendAttempt = useRef<{key: string; payload: string} | null>(null);

  // Default selection: prefer email/domain for the original email-first
  // workflow, but phone-only installs must still initialise state. Without
  // this, the browser visually shows the first <option> while React keeps an
  // empty selectedAddress, so submit fails with "Pick a sender."
  useEffect(() => {
    if (selectedAddress) return;
    const firstEmail = verified.find((s) => s.is_default) || verified.find((s) => s.channel === "email");
    if (firstEmail) {
      setSelectedAddress(stripScheme(firstEmail.address));
      setSelectedChannel(firstEmail.channel);
      return;
    }
    const firstDomain = verified.find((s) => s.kind === "domain");
    if (firstDomain) {
      setSelectedAddress(stripScheme(firstDomain.address));
      setSelectedChannel(firstDomain.channel);
      setLocalPart("noreply");
      return;
    }
    const firstPhone = verified.find((s) => s.channel === "whatsapp") ||
      verified.find((s) => s.channel === "sms") ||
      verified[0];
    if (firstPhone) {
      setSelectedAddress(stripScheme(firstPhone.address));
      setSelectedChannel(firstPhone.channel);
      setLocalPart("");
    }
  }, [verified, selectedAddress]);

  useEffect(() => {
    if (!draft || initializedDraft.current === draft.key) return;
    initializedDraft.current = draft.key;
    const from = stripScheme(draft.from);
    const at = from.indexOf("@");
    const exact = verified.find((s) => stripScheme(s.address) === from && s.channel === draft.channel);
    const domain = at > 0
      ? verified.find((s) => s.kind === "domain" && stripScheme(s.address) === from.slice(at + 1))
      : undefined;
    if (exact) {
      setSelectedAddress(stripScheme(exact.address));
      setSelectedChannel(exact.channel);
      setLocalPart("");
    } else if (domain) {
      setSelectedAddress(stripScheme(domain.address));
      setSelectedChannel(domain.channel);
      setLocalPart(from.slice(0, at));
    } else {
      setSelectedAddress(from);
      setSelectedChannel(draft.channel || "");
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
    () => verified.find((s) => stripScheme(s.address) === selectedAddress && (!selectedChannel || s.channel === selectedChannel)),
    [verified, selectedAddress, selectedChannel],
  );
  const isDomain = selectedSender?.kind === "domain";
  const channel = selectedSender?.channel || "email";

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
  const recipients = useMemo(() => parseRecipientList(to), [to]);
  const approvedWhatsAppTemplates = useMemo(
    () => templates.filter((t) =>
      t.channel === "whatsapp" &&
      t.provider_template_id &&
      (t.provider_status || "").toLowerCase() === "approved"
    ),
    [templates],
  );
  const composeTemplates = channel === "whatsapp" ? approvedWhatsAppTemplates : templates.filter((t) => t.channel === channel);
  const selectedTemplate = useMemo(
    () => composeTemplates.find((t) => String(t.id) === templateID) || null,
    [composeTemplates, templateID],
  );
  const selectedTemplateVars = useMemo(
    () => selectedTemplate ? templateVarKeys(selectedTemplate) : [],
    [selectedTemplate],
  );
  const whatsAppFreeformAllowed = channel === "whatsapp" && waSession.state === "active";
  const whatsAppRequiresTemplate = channel === "whatsapp" && (
    waSession.state === "closed" ||
    waSession.state === "error" ||
    (recipients.length > 0 && waSession.state === "idle")
  );
  const whatsAppChecking = channel === "whatsapp" && waSession.state === "checking";

  useEffect(() => {
    if (channel !== "whatsapp") { if (templateID && !templates.some((t) => t.channel === channel && String(t.id) === templateID)) setTemplateID(""); return; }
    if (templateID && approvedWhatsAppTemplates.some((t) => String(t.id) === templateID)) return;
    setTemplateID(approvedWhatsAppTemplates[0] ? String(approvedWhatsAppTemplates[0].id) : "");
  }, [channel, approvedWhatsAppTemplates, templateID]);

  useEffect(() => {
    if (!selectedTemplate) {
      setTemplateVars({});
      return;
    }
    const keys = templateVarKeys(selectedTemplate);
    setTemplateVars((prev) => {
      const next: Record<string, string> = {};
      for (const key of keys) next[key] = prev[key] || "";
      return next;
    });
  }, [selectedTemplate]);

  useEffect(() => {
    if (channel !== "whatsapp") {
      setWaSession({ state: "idle", checkedRecipients: [] });
      return;
    }
    const cleanFrom = stripScheme(computedFrom);
    if (!cleanFrom || recipients.length === 0) {
      setWaSession({ state: "idle", checkedRecipients: [] });
      return;
    }

    const localActive = whatsAppRecipientsInSession(inbox, recipients, cleanFrom);
    if (localActive.allActive) {
      setWaSession({ state: "active", checkedRecipients: recipients });
      return;
    }

    let cancelled = false;
    const timer = window.setTimeout(async () => {
      setWaSession({ state: "checking", checkedRecipients: recipients });
      try {
        const since = new Date(Date.now() - whatsappSessionWindowMs).toISOString();
        const checks = await Promise.all(recipients.map(async (recipient) => {
          const r = await api<{ messages: MessageRow[] }>("GET", "/messages", {
            direction: "in",
            channel: "whatsapp",
            address: recipient,
            since,
            limit: "20",
          });
          return whatsAppRecipientsInSession(r.messages || [], [recipient], cleanFrom).allActive;
        }));
        if (cancelled) return;
        setWaSession({
          state: checks.every(Boolean) ? "active" : "closed",
          checkedRecipients: recipients,
        });
      } catch (e) {
        if (cancelled) return;
        setWaSession({
          state: "error",
          checkedRecipients: recipients,
          error: parseSendersError((e as Error).message),
        });
      }
    }, 250);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [api, channel, computedFrom, inbox, recipients]);

  const addAttachments = async (files: FileList | null) => {
    if (!files || files.length === 0) return;
    setErr("");
    try {
      const selectedFiles = Array.from(files);
      if (attachments.length + selectedFiles.length > 20) {
        throw new Error("You can attach at most 20 files.");
      }
      const totalBytes = attachments.reduce((sum, att) => sum + att.size_bytes, 0) +
        selectedFiles.reduce((sum, file) => sum + file.size, 0);
      if (totalBytes > 25 * 1024 * 1024) {
        throw new Error("Attachments cannot exceed 25 MB in total.");
      }
      const next = await Promise.all(selectedFiles.map(fileToComposeAttachment));
      setAttachments((prev) => [...prev, ...next]);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  const send = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!computedFrom) {
      setErr(isDomain ? "Enter a local-part for the domain sender." : "Pick a sender.");
      return;
    }
    setBusy(true);
    setErr("");
    try {
      if (recipients.length === 0) {
        setErr("Enter at least one recipient.");
        setBusy(false);
        return;
      }
      if (channel !== "email" && recipients.length !== 1) {
        setErr("SMS and WhatsApp messages must have exactly one recipient.");
        setBusy(false);
        return;
      }
      const args: Record<string, unknown> = {
        channel,
        from: computedFrom,
        to: recipients,
      };
      if (selectedTemplate || (channel === "whatsapp" && !whatsAppFreeformAllowed)) {
        if (!selectedTemplate) {
          setErr("Pick an approved WhatsApp template. Free-form WhatsApp messages require an inbound message in the last 24 hours.");
          setBusy(false);
          return;
        }
        const missing = selectedTemplateVars.filter((key) => !templateVars[key]?.trim());
        if (missing.length > 0) {
          setErr(`Fill template variable${missing.length === 1 ? "" : "s"} ${missing.map((k) => `{{${k}}}`).join(", ")}.`);
          setBusy(false);
          return;
        }
        args.template_id = selectedTemplate.id;
        args.vars = templateVars;
      } else {
        if (!body.trim() && attachments.length === 0) {
          setErr("Enter a body.");
          setBusy(false);
          return;
        }
        args.body = body;
      }
      // Email-only fields — included only when relevant so the
      // server's "irrelevant fields warned + dropped" pathway
      // doesn't fire on every SMS send.
      if (channel === "email") {
        if (subject) args.subject = subject;
        if (inReplyTo) args.in_reply_to = inReplyTo;
        if (references.length > 0) args.references = references;
      }
      if (attachments.length > 0) {
        args.attachments = attachments.map(({ key, ...att }) => ({
          filename: att.filename,
          content_type: att.content_type,
          size_bytes: att.size_bytes,
          content_base64: att.content_base64,
        }));
      }
      const payload = JSON.stringify(args);
      if (!sendAttempt.current || sendAttempt.current.payload !== payload) sendAttempt.current = {key: crypto.randomUUID(), payload};
      args.idempotency_key = sendAttempt.current.key;
      const result = await api<{status: string; status_reason?: string}>("POST", "/tools/call", {}, { tool: "send_message", args });
      if (!["sent", "delivered", "opened", "clicked"].includes(result.status)) {
        if (["failed", "suppressed"].includes(result.status)) sendAttempt.current = null;
        throw new Error(result.status_reason || `Message was not sent (${result.status}).`);
      }
      sendAttempt.current = null;
      setTo(""); setSubject(""); setBody(""); setInReplyTo(""); setReferences([]); setTemplateVars({}); setAttachments([]);
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
        <p className="text-xs text-text-dim mb-3">
          {channel === "email"
            ? "Pick a verified sender. Separate multiple recipients with commas."
            : "Pick a verified sender and enter one recipient."}
        </p>
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
              value={selectedSender ? senderOptionValue(selectedSender) : senderOptionValue({ channel: selectedChannel || "email", address: selectedAddress })}
              onChange={(e) => {
                const parsed = parseSenderOptionValue(e.target.value);
                setSelectedAddress(parsed.address);
                setSelectedChannel(parsed.channel);
                const next = verified.find((s) => stripScheme(s.address) === parsed.address && s.channel === parsed.channel);
                if (next?.kind === "domain" && !localPart) setLocalPart("noreply");
              }}
              required
            >
              {(["email", "whatsapp", "sms"] as const).map((ch) => {
                const inCh = verified.filter((s) => s.channel === ch);
                if (inCh.length === 0) return null;
                const groupLabel = ch === "email" ? "Email (SES)" : ch === "sms" ? "SMS (Twilio)" : "WhatsApp (Twilio)";
                return (
                  <optgroup key={ch} label={groupLabel}>
                    {inCh.map((s) => (
                      <option key={`${s.channel}:${s.address}`} value={senderOptionValue(s)}>
                        {s.kind === "domain" ? `@${stripScheme(s.address)} (any local-part)` : stripScheme(s.address)}
                      </option>
                    ))}
                  </optgroup>
                );
              })}
              {selectedAddress && !selectedSender && (
                <option value={senderOptionValue({ channel: selectedChannel || "email", address: selectedAddress })}>{selectedAddress}</option>
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
      {channel === "whatsapp" && (
        <WhatsAppComposeHint
          session={waSession}
          freeformAllowed={whatsAppFreeformAllowed}
          requiresTemplate={whatsAppRequiresTemplate}
          checking={whatsAppChecking}
          recipients={recipients}
        />
      )}
      {channel === "email" && (
        <Field label="Subject">
          <input className={inputCls} value={subject} onChange={(e) => setSubject(e.target.value)} disabled={noVerifiedSenders} />
        </Field>
      )}
      {channel !== "whatsapp" && <Field label="Saved template"><select className={inputCls} value={templateID} onChange={(e) => setTemplateID(e.target.value)}><option value="">Write a message</option>{composeTemplates.map((t) => <option key={t.id} value={String(t.id)}>{t.name}</option>)}</select></Field>}
      {selectedTemplate || (channel === "whatsapp" && !whatsAppFreeformAllowed) ? (
        <WhatsAppTemplateComposer
          templates={composeTemplates}
          selectedTemplate={selectedTemplate}
          templateID={templateID}
          vars={templateVars}
          onTemplateID={setTemplateID}
          onVars={setTemplateVars}
          onGotoTemplates={gotoTemplates}
          disabled={noVerifiedSenders || whatsAppChecking || recipients.length === 0}
        />
      ) : (
        <Field label="Body">
          <textarea
            className={inputCls + " font-mono text-sm"}
            rows={10}
            value={body}
            onChange={(e) => setBody(e.target.value)}
            required={attachments.length === 0}
            disabled={noVerifiedSenders || whatsAppChecking}
          />
        </Field>
      )}

      <Field label={channel === "email" ? "Attachments" : "Media"}>
        <div className="space-y-2">
          <input
            className={inputCls}
            type="file"
            multiple
            disabled={noVerifiedSenders || whatsAppChecking || busy}
            onChange={(e) => {
              void addAttachments(e.currentTarget.files);
              e.currentTarget.value = "";
            }}
          />
          {attachments.length > 0 && (
            <ul className="rounded border border-border divide-y divide-border bg-surface-2">
              {attachments.map((att) => (
                <li key={att.key} className="px-3 py-2 flex items-center justify-between gap-3 text-xs">
                  <div className="min-w-0">
                    <div className="truncate text-text">{att.filename}</div>
                    <div className="text-text-dim">{att.content_type || "application/octet-stream"} · {formatBytes(att.size_bytes)}</div>
                  </div>
                  <button
                    type="button"
                    className="text-text-dim hover:text-red-400"
                    onClick={() => setAttachments((prev) => prev.filter((x) => x.key !== att.key))}
                    aria-label={`Remove ${att.filename}`}
                  >
                    Remove
                  </button>
                </li>
              ))}
            </ul>
          )}
          {channel !== "email" && attachments.length > 0 && (
            <p className="text-xs text-text-dim">Media files are sent as Twilio-reachable signed URLs.</p>
          )}
        </div>
      </Field>

      {quota && quota.sandboxed && (
        <p className="text-xs text-yellow-400/80">
          Sandbox: only recipients you've verified in SES will receive this message.
        </p>
      )}
      {err && <div className="text-red-500 text-sm">{err}</div>}

      <div className="flex justify-end gap-2 pt-2">
        <button type="submit" disabled={busy || noVerifiedSenders || whatsAppChecking} className="px-4 py-1.5 bg-accent text-white rounded disabled:opacity-50">
          {busy ? "Sending…" : "Send"}
        </button>
      </div>
    </form>
  );
}

const whatsappSessionWindowMs = 24 * 60 * 60 * 1000;

async function fileToComposeAttachment(file: File): Promise<ComposeAttachment> {
  const maxBytes = 25 * 1024 * 1024;
  if (file.size > maxBytes) {
    throw new Error(`${file.name} is larger than 25 MB.`);
  }
  const dataURL = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("Could not read file."));
    reader.readAsDataURL(file);
  });
  const comma = dataURL.indexOf(",");
  return {
    key: `${file.name}:${file.size}:${file.lastModified}:${Math.random().toString(36).slice(2)}`,
    filename: file.name || "attachment",
    content_type: file.type || "application/octet-stream",
    size_bytes: file.size,
    content_base64: comma >= 0 ? dataURL.slice(comma + 1) : dataURL,
  };
}

function formatBytes(n?: number): string {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = n;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i += 1;
  }
  return `${value >= 10 || i === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[i]}`;
}

type WhatsAppSessionState =
  | { state: "idle" | "checking" | "active" | "closed"; checkedRecipients: string[] }
  | { state: "error"; checkedRecipients: string[]; error: string };

function WhatsAppComposeHint({
  session, freeformAllowed, requiresTemplate, checking, recipients,
}: {
  session: WhatsAppSessionState;
  freeformAllowed: boolean;
  requiresTemplate: boolean;
  checking: boolean;
  recipients: string[];
}) {
  if (recipients.length === 0) {
    return (
      <div className="rounded border border-border bg-surface-2 p-3 text-xs text-text-dim">
        Enter a recipient to check whether the WhatsApp 24-hour reply window is open.
      </div>
    );
  }
  if (checking) {
    return (
      <div className="rounded border border-border bg-surface-2 p-3 text-xs text-text-dim">
        Checking the WhatsApp 24-hour window…
      </div>
    );
  }
  if (freeformAllowed) {
    return (
      <div className="rounded border border-green-500/30 bg-green-500/10 p-3 text-xs text-green-300">
        Recent inbound WhatsApp message found. Free-form reply is allowed for this recipient.
      </div>
    );
  }
  if (requiresTemplate) {
    return (
      <div className="rounded border border-yellow-500/30 bg-yellow-500/10 p-3 text-xs text-yellow-300">
        No recent inbound WhatsApp message was found for every recipient. Use an approved template to send outside the 24-hour window.
        {session.state === "error" && <div className="mt-1 text-yellow-200/80">{session.error}</div>}
      </div>
    );
  }
  return null;
}

function WhatsAppTemplateComposer({
  templates, selectedTemplate, templateID, vars, onTemplateID, onVars, onGotoTemplates, disabled,
}: {
  templates: TemplateRow[];
  selectedTemplate: TemplateRow | null;
  templateID: string;
  vars: Record<string, string>;
  onTemplateID: (id: string) => void;
  onVars: (vars: Record<string, string>) => void;
  onGotoTemplates: () => void;
  disabled: boolean;
}) {
  const keys = selectedTemplate ? templateVarKeys(selectedTemplate) : [];
  const preview = selectedTemplate ? renderTemplatePreview(selectedTemplate.body_text || "", vars) : "";

  if (templates.length === 0) {
    return (
      <div className="rounded border border-yellow-500/30 bg-yellow-500/10 p-3 text-sm text-yellow-300">
        No approved WhatsApp templates are available.{" "}
        <button type="button" className="underline" onClick={onGotoTemplates}>Import or refresh templates</button>
        .
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <Field label="Template">
        <select
          className={inputCls}
          value={templateID}
          onChange={(e) => onTemplateID(e.target.value)}
          required
          disabled={disabled}
        >
          {templates.map((t) => (
            <option key={t.id} value={String(t.id)}>
              {t.name}
            </option>
          ))}
        </select>
      </Field>
      {keys.length > 0 && (
        <div className="grid gap-2 sm:grid-cols-2">
          {keys.map((key) => (
            <Field key={key} label={`{{${key}}}`} hint={templateVarHint(selectedTemplate, key)}>
              <input
                className={inputCls}
                value={vars[key] || ""}
                onChange={(e) => onVars({ ...vars, [key]: e.target.value })}
                required
                disabled={disabled}
              />
            </Field>
          ))}
        </div>
      )}
      {preview && (
        <div className="rounded border border-border bg-surface-2 p-3 text-sm whitespace-pre-wrap break-words">
          {preview}
        </div>
      )}
    </div>
  );
}

function parseRecipientList(value: string): string[] {
  return uniqueStrings(value.split(",").map((s) => stripScheme(s.trim())).filter(Boolean));
}

function senderOptionValue(sender: { channel?: string; address?: string }): string {
  return `${sender.channel || ""}|${stripScheme(sender.address || "")}`;
}

function parseSenderOptionValue(value: string): { channel: string; address: string } {
  const i = value.indexOf("|");
  if (i < 0) return { channel: "", address: stripScheme(value) };
  return { channel: value.slice(0, i), address: stripScheme(value.slice(i + 1)) };
}

function whatsAppRecipientsInSession(messages: MessageRow[], recipients: string[], from: string): { allActive: boolean } {
  const sender = stripScheme(from);
  const active = new Set<string>();
  for (const message of messages) {
    if (message.channel !== "whatsapp" || message.direction !== "in") continue;
    if (!isWithinWhatsAppWindow(message.received_at || message.created_at || message.last_event_at)) continue;
    const inboundFrom = stripScheme(message.from);
    if (!recipients.includes(inboundFrom)) continue;
    const inboundTo = (message.to || []).map(stripScheme);
    if (inboundTo.includes(sender) || stripScheme(message.matched_recipient || "") === sender) {
      active.add(inboundFrom);
    }
  }
  return { allActive: recipients.every((recipient) => active.has(recipient)) };
}

function isWithinWhatsAppWindow(value?: string): boolean {
  if (!value) return false;
  const t = new Date(value).getTime();
  return Number.isFinite(t) && Date.now() - t < whatsappSessionWindowMs;
}

function templateVarKeys(template: TemplateRow): string[] {
  const keys = new Set<string>();
  if (template.vars_schema && typeof template.vars_schema === "object") {
    Object.keys(template.vars_schema).forEach((key) => keys.add(key));
  }
  for (const source of [template.subject || "", template.body_text || "", template.body_html || ""]) {
    const re = /\{\{\s*([^{}\s]+)\s*\}\}/g;
    let match: RegExpExecArray | null;
    while ((match = re.exec(source))) keys.add(match[1]);
  }
  return Array.from(keys).sort((a, b) => {
    const na = Number(a);
    const nb = Number(b);
    if (Number.isFinite(na) && Number.isFinite(nb)) return na - nb;
    return a.localeCompare(b);
  });
}

function templateVarHint(template: TemplateRow | null, key: string): string | undefined {
  const raw = template?.vars_schema?.[key];
  if (typeof raw === "string" && raw.trim()) return raw;
  return undefined;
}

function renderTemplatePreview(body: string, vars: Record<string, string>): string {
  return body.replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (_, key: string) => vars[key]?.trim() || `{{${key}}}`);
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
  const [providerCreate, setProviderCreate] = useState(true);
  const [submitForApproval, setSubmitForApproval] = useState(true);
  const [busy, setBusy] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [providerRows, setProviderRows] = useState<ProviderTemplateRow[]>([]);
  const [providerLabel, setProviderLabel] = useState("Provider");
  const [providerLoading, setProviderLoading] = useState(false);
  const [selectedProviderTemplateIDs, setSelectedProviderTemplateIDs] = useState<string[]>([]);
  const [updateExisting, setUpdateExisting] = useState(true);
  const providerCapable = channel === "sms" || channel === "whatsapp";

  useEffect(() => {
    setProviderCreate(channel === "sms" || channel === "whatsapp");
  }, [channel]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const out = await api<{ template?: TemplateRow; provider?: { provider?: string; provider_template_id?: string; provider_status?: string; submitted?: boolean } }>("POST", "/tools/call", {}, {
        tool: "template_create",
        args: {
          channel,
          name,
          body_text: bodyText,
          language,
          category,
          provider_create: providerCapable ? providerCreate : false,
          submit_for_approval: channel === "whatsapp" && providerCreate ? submitForApproval : false,
        },
      });
      setName("");
      setBodyText("");
      reload();
      if (out.provider?.provider_template_id) {
        const label = out.provider.provider || providerLabel;
        notify("info", `Template added on ${label}: ${out.provider.provider_status || "created"}.`);
      } else {
        notify("info", "Template added.");
      }
    } catch (e) {
      notify("error", `Add failed: ${parseSendersError((e as Error).message)}`);
    } finally {
      setBusy(false);
    }
  };

  const loadProviderTemplates = async () => {
    setProviderLoading(true);
    try {
      const out = await api<{ templates: ProviderTemplateRow[]; provider_label?: string }>("GET", "/templates/provider-preview", { channel: "whatsapp" });
      const next = out.templates || [];
      setProviderLabel(out.provider_label || "Provider");
      setProviderRows(next);
      setSelectedProviderTemplateIDs(next.filter((t) => t.local_state !== "imported" && t.status === "approved").map((t) => t.provider_template_id));
      setImportOpen(true);
    } catch (e) {
      notify("error", `Load failed: ${parseSendersError((e as Error).message)}`);
    } finally {
      setProviderLoading(false);
    }
  };

  const toggleProviderTemplate = (providerTemplateID: string) => {
    setSelectedProviderTemplateIDs((prev) => prev.includes(providerTemplateID) ? prev.filter((x) => x !== providerTemplateID) : [...prev, providerTemplateID]);
  };

  const importProviderTemplates = async (mode: "selected" | "approved") => {
    setProviderLoading(true);
    try {
      const out = await api<{ imported?: number; updated?: number; skipped?: number }>("POST", "/templates/import", {}, {
        channel: "whatsapp",
        provider_template_ids: mode === "selected" ? selectedProviderTemplateIDs : [],
        approved_only: mode === "approved",
        update_existing: updateExisting,
      });
      reload();
      await loadProviderTemplates();
      notify("info", `Imported ${out.imported ?? 0}, updated ${out.updated ?? 0}, skipped ${out.skipped ?? 0}.`);
    } catch (e) {
      notify("error", `Import failed: ${parseSendersError((e as Error).message)}`);
    } finally {
      setProviderLoading(false);
    }
  };

  const refreshAllStatuses = async () => {
    setBusy(true);
    try {
      const out = await api<{ refreshed?: number }>("POST", "/templates/refresh-statuses", { channel: "whatsapp" });
      reload();
      notify("info", `Refreshed ${out.refreshed ?? 0} WhatsApp template${out.refreshed === 1 ? "" : "s"}.`);
    } catch (e) {
      notify("error", `Refresh failed: ${parseSendersError((e as Error).message)}`);
    } finally {
      setBusy(false);
    }
  };

  const handleDelete = async (id: number) => {
    const tpl = rows.find((t) => t.id === id);
    confirmAction({
      title: "Remove template",
      tone: "danger",
      confirmLabel: "Remove",
      message: (
        <>
          <div>Remove template <code>{tpl?.name || `#${id}`}</code> from this project?</div>
          {tpl?.provider_template_id ? (
            <div className="mt-2 text-text-dim">
              The shared provider template <code>{tpl.provider_template_id}</code> remains available to other projects.
            </div>
          ) : null}
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
  const handleProviderCreate = async (t: TemplateRow) => {
    try {
      await api("POST", `/templates/${t.id}/provider-create`, {}, { category, language });
      reload();
      notify("info", "Template synced to the phone provider.");
    } catch (e) {
      notify("error", `Sync failed: ${parseSendersError((e as Error).message)}`);
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
          </>
        )}
        {providerCapable && (
          <label className="flex items-center gap-2 text-xs text-text-dim pb-2">
            <input type="checkbox" checked={providerCreate} onChange={(e) => setProviderCreate(e.target.checked)} />
            Sync to provider
          </label>
        )}
        {channel === "whatsapp" && providerCreate && (
          <label className="flex items-center gap-2 text-xs text-text-dim pb-2">
            <input type="checkbox" checked={submitForApproval} onChange={(e) => setSubmitForApproval(e.target.checked)} />
            Submit for approval
          </label>
        )}
        <Field label="Body">
          <input className={inputCls + " w-[28rem]"} value={bodyText} onChange={(e) => setBodyText(e.target.value)} required placeholder="Hi {{1}}, your appointment is {{2}}." />
        </Field>
        <button type="submit" className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50" disabled={busy}>Add</button>
        <button type="button" className="px-3 py-1.5 rounded border border-border text-text-dim hover:text-text hover:bg-surface-2 disabled:opacity-50" onClick={loadProviderTemplates} disabled={busy || providerLoading}>
          {providerLoading ? "Loading…" : `Import from ${providerLabel}`}
        </button>
        <button type="button" className="px-3 py-1.5 rounded border border-border text-text-dim hover:text-text hover:bg-surface-2 disabled:opacity-50" onClick={refreshAllStatuses} disabled={busy || providerLoading}>
          Refresh WhatsApp statuses
        </button>
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
                      {t.provider_template_id && (
                        <button type="button" className="text-accent hover:underline text-xs mr-3" onClick={() => handleRefreshStatus(t.id)}>Refresh</button>
                      )}
                      {t.provider_status !== "approved" && (
                        <button type="button" className="text-accent hover:underline text-xs mr-3" onClick={() => handleSubmit(t.id)}>Submit</button>
                      )}
                    </>
                  )}
                  {t.channel === "sms" && !t.provider_template_id && (
                    <button type="button" className="text-accent hover:underline text-xs mr-3" onClick={() => handleProviderCreate(t)}>Sync to provider</button>
                  )}
                  <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => handleDelete(t.id)}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {importOpen && (
        <ProviderTemplateImportModal
          rows={providerRows}
          providerLabel={providerLabel}
          selectedProviderTemplateIDs={selectedProviderTemplateIDs}
          updateExisting={updateExisting}
          busy={providerLoading}
          onToggle={toggleProviderTemplate}
          onUpdateExisting={setUpdateExisting}
          onImportSelected={() => importProviderTemplates("selected")}
          onImportApproved={() => importProviderTemplates("approved")}
          onRefresh={loadProviderTemplates}
          onClose={() => setImportOpen(false)}
        />
      )}
    </div>
  );
}

function ProviderTemplateImportModal({
  rows,
  providerLabel,
  selectedProviderTemplateIDs,
  updateExisting,
  busy,
  onToggle,
  onUpdateExisting,
  onImportSelected,
  onImportApproved,
  onRefresh,
  onClose,
}: {
  rows: ProviderTemplateRow[];
  providerLabel: string;
  selectedProviderTemplateIDs: string[];
  updateExisting: boolean;
  busy: boolean;
  onToggle: (providerTemplateID: string) => void;
  onUpdateExisting: (value: boolean) => void;
  onImportSelected: () => void;
  onImportApproved: () => void;
  onRefresh: () => void;
  onClose: () => void;
}) {
  const selectedCount = selectedProviderTemplateIDs.length;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 px-4" role="presentation" onMouseDown={(e) => {
      if (e.target === e.currentTarget && !busy) onClose();
    }}>
      <div role="dialog" aria-modal="true" aria-labelledby="provider-template-import-title" className="w-full max-w-5xl max-h-[86vh] rounded border border-border bg-surface text-text shadow-2xl flex flex-col" onMouseDown={(e) => e.stopPropagation()}>
        <div className="px-5 py-4 border-b border-border flex items-center justify-between gap-3">
          <div>
            <div id="provider-template-import-title" className="font-semibold">Import WhatsApp templates</div>
            <div className="text-xs text-text-dim">Select which {providerLabel} templates belong to this project. Approval status stays synchronized.</div>
          </div>
          <button type="button" className="text-text-dim hover:text-text" onClick={onClose} disabled={busy}>×</button>
        </div>
        <div className="px-5 py-3 border-b border-border flex items-center justify-between gap-3 text-xs">
          <label className="flex items-center gap-2 text-text-dim">
            <input type="checkbox" checked={updateExisting} onChange={(e) => onUpdateExisting(e.target.checked)} disabled={busy} />
            Update already imported templates
          </label>
          <div className="flex items-center gap-2">
            <button type="button" className="px-2 py-1 rounded border border-border text-text-dim hover:text-text hover:bg-surface-2 disabled:opacity-50" onClick={onRefresh} disabled={busy}>Refresh {providerLabel}</button>
            <button type="button" className="px-2 py-1 rounded border border-border text-text-dim hover:text-text hover:bg-surface-2 disabled:opacity-50" onClick={onImportApproved} disabled={busy}>Import all approved</button>
            <button type="button" className="px-3 py-1 rounded bg-accent text-white disabled:opacity-50" onClick={onImportSelected} disabled={busy || selectedCount === 0}>
              Import selected ({selectedCount})
            </button>
          </div>
        </div>
        <div className="overflow-auto">
          {rows.length === 0 ? (
            <div className="p-6 text-sm text-text-dim">No provider templates found.</div>
          ) : (
            <table className="w-full text-sm">
              <thead className="text-xs text-text-dim sticky top-0 bg-surface">
                <tr className="border-b border-border">
                  <th className="px-4 py-2 w-10"></th>
                  <th className="text-left px-4 py-2">Template</th>
                  <th className="text-left px-4 py-2">Status</th>
                  <th className="text-left px-4 py-2">Local</th>
                  <th className="text-left px-4 py-2">Variables</th>
                  <th className="text-left px-4 py-2">Preview</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const vars = Object.keys(row.variables || {}).sort();
                  return (
                    <tr key={row.provider_template_id} className="border-b border-border">
                      <td className="px-4 py-2">
                        <input type="checkbox" checked={selectedProviderTemplateIDs.includes(row.provider_template_id)} onChange={() => onToggle(row.provider_template_id)} disabled={busy} aria-label={`Select ${row.name}`} />
                      </td>
                      <td className="px-4 py-2 min-w-56">
                        <div className="font-medium">{row.name || row.provider_template_id}</div>
                        <div className="text-[11px] text-text-dim font-mono">{row.provider_template_id}</div>
                        {(row.language || row.category) && (
                          <div className="text-[11px] text-text-dim">{[row.language, row.category].filter(Boolean).join(" · ")}</div>
                        )}
                      </td>
                      <td className="px-4 py-2"><StatusPill status={row.status || "pending"} /></td>
                      <td className="px-4 py-2"><ProviderTemplateStatePill state={row.local_state} /></td>
                      <td className="px-4 py-2 text-text-dim font-mono text-xs">
                        {vars.length > 0 ? vars.map((v) => `{{${v}}}`).join(" ") : "—"}
                      </td>
                      <td className="px-4 py-2 text-text-dim max-w-md">
                        <div className="max-h-10 overflow-hidden">{row.body_text || "—"}</div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </div>
      </div>
    </div>
  );
}

function ProviderTemplateStatePill({ state }: { state: ProviderTemplateRow["local_state"] }) {
  const cls = state === "new"
    ? "bg-blue-500/20 text-blue-300 border-blue-500/30"
    : state === "changed"
      ? "bg-yellow-500/20 text-yellow-400 border-yellow-500/30"
      : "bg-green-500/20 text-green-400 border-green-500/30";
  return <span className={`inline-block px-1.5 py-0.5 rounded text-xs border ${cls}`}>{state}</span>;
}

function RoutesView({ rows, api, reload, notify, confirmAction }: { rows: InboundRoute[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void; notify: Notify; confirmAction: ConfirmFn }) {
  const [pattern, setPattern] = useState("");
  const [targetApp, setTargetApp] = useState("");
  const [targetRoute, setTargetRoute] = useState("inbound");

  const [channel, setChannel] = useState("email");
  const [priority, setPriority] = useState(0);
  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await api("POST", "/tools/call", {}, {
        tool: "inbound_route_set",
        args: { channel, priority, pattern, target_app: targetApp, target_route: targetRoute },
      });
      setPattern(""); setTargetApp(""); setTargetRoute("inbound");
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
        <Field label="Channel"><select className={inputCls} value={channel} onChange={(e) => setChannel(e.target.value)}>{["email", "sms", "whatsapp"].map((c) => <option key={c}>{c}</option>)}</select></Field>
        <Field label="Priority"><input className={inputCls} type="number" value={priority} onChange={(e) => setPriority(Number(e.target.value))}/></Field>
        <Field label="Pattern" hint="e.g. mailto:support+*@acme.com">
          <input className={inputCls + " w-72"} value={pattern} onChange={(e) => setPattern(e.target.value)} required />
        </Field>
        <Field label="Target app">
          <input className={inputCls + " w-40"} value={targetApp} onChange={(e) => setTargetApp(e.target.value)} required placeholder="support" />
        </Field>
        <Field label="Target tool">
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
                <td className="px-4 py-2"><code className="text-xs">{r.channel || "email"}: {r.pattern}</code></td>
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
  const remove = async (row: SuppressionRow) => {
    const kind = row.kind || "address";
    confirmAction({
      title: "Remove suppression",
      confirmLabel: "Remove",
      message: (
        <>
          <div>Remove <code>{stripScheme(row.address)}</code> from suppressions?</div>
          <div className="mt-2 text-text-dim">Messaging will allow future {kind === "domain" ? "email to and from this domain" : "communication with this address"} again.</div>
        </>
      ),
      onConfirm: async () => {
        try {
          await api("POST", "/tools/call", {}, { tool: "suppression_remove", args: { address: row.address, channel: row.channel, kind } });
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
          <th className="text-left px-4 py-2">Kind</th>
          <th className="text-left px-4 py-2">Reason</th>
          <th className="text-left px-4 py-2">Source</th>
          <th className="text-left px-4 py-2">Last seen</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {rows.map((s) => (
          <tr key={`${s.channel}:${s.kind || "address"}:${s.address}`} className="border-b border-border">
            <td className="px-4 py-2">{stripScheme(s.address)}</td>
            <td className="px-4 py-2 text-text-dim">{s.kind || "address"}</td>
            <td className="px-4 py-2"><StatusPill status={s.reason} /></td>
            <td className="px-4 py-2 text-text-dim">{s.source}</td>
            <td className="px-4 py-2 text-text-dim">{shortTime(s.last_seen)}</td>
            <td className="px-4 py-2 text-right">
              <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => remove(s)}>Remove</button>
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
    case "created":
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
    case "suppressed":
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
