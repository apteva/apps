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
}
interface DeliveryEvent {
  id: number;
  message_id: number;
  kind: string;
  recipient?: string;
  reason?: string;
  occurred_at?: string;
}
interface TemplateRow {
  id: number;
  channel: string;
  name: string;
  subject?: string;
  body_text?: string;
  body_html?: string;
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
interface SenderRow {
  address: string;
  kind: "email" | "domain";
  verified: boolean;
  dkim_status?: string;
  dkim_tokens?: string[];
  sending_enabled: boolean;
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
  const [quota, setQuota] = useState<QuotaInfo | null>(null);
  // Surface the SES-side failure rather than silently rendering an
  // empty Senders tab — most often this means the email_provider
  // role isn't bound to an aws-ses connection on the install.
  const [sendersError, setSendersError] = useState<string>("");

  const [selected, setSelected] = useState<MessageRow | null>(null);
  const [selectedEvents, setSelectedEvents] = useState<DeliveryEvent[]>([]);

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
    const r = await api<{ templates: TemplateRow[] }>("GET", "/templates", {});
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
  const loadQuota = useCallback(async () => {
    try {
      const q = await api<QuotaInfo>("GET", "/senders/quota", {});
      setQuota(q);
    } catch {
      setQuota(null);
    }
  }, [api]);

  const reload = useCallback(async () => {
    setBusy(true);
    try {
      await Promise.all([
        loadOutbox(), loadInbox(), loadTemplates(), loadRoutes(),
        loadSuppressions(), loadSenders(), loadQuota(),
      ]);
      setStatus("");
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [loadOutbox, loadInbox, loadTemplates, loadRoutes, loadSuppressions, loadSenders, loadQuota]);

  useEffect(() => { reload(); }, [reload]);

  // Live refresh on any messaging event.
  useAppEvents("messaging", projectId, (ev) => {
    if (ev.topic === "message.sent" || ev.topic === "message.received" || ev.topic === "message.event") {
      reload();
    }
  });

  const openMessage = useCallback(async (m: MessageRow) => {
    setSelected(m);
    setSelectedEvents([]);
    try {
      const r = await api<{ message: MessageRow; events: DeliveryEvent[] }>("GET", `/messages/${m.id}`, {});
      setSelected(r.message);
      setSelectedEvents(r.events || []);
    } catch {}
  }, [api]);

  const counts = useMemo(() => ({
    outbox: outbox.length,
    inbox: inbox.length,
    templates: templates.length,
    routes: routes.length,
    suppressions: suppressions.length,
    senders: senders.length,
  }), [outbox, inbox, templates, routes, suppressions, senders]);

  const verifiedSenders = useMemo(() => senders.filter((s) => s.verified), [senders]);

  return (
    <div className="h-full flex flex-col">
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
              onClick={() => { setTab(id); setSelected(null); }}
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
              onSent={() => { reload(); setTab("outbox"); }}
              gotoSenders={() => setTab("senders")}
            />
          )}
          {tab === "senders" && <SendersView rows={senders} quota={quota} api={api} reload={reload} error={sendersError} />}
          {tab === "templates" && <TemplatesView rows={templates} api={api} reload={reload} />}
          {tab === "routes" && <RoutesView rows={routes} api={api} reload={reload} />}
          {tab === "suppressions" && <SuppressionsView rows={suppressions} api={api} reload={reload} />}
        </div>

        {/* Detail pane (only meaningful for messages) */}
        {(tab === "outbox" || tab === "inbox") && selected && (
          <MessageDetail
            m={selected}
            events={selectedEvents}
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
              <td className="px-4 py-2"><StatusPill status={m.direction === "in" ? (m.route_status || m.status) : m.status} /></td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

function MessageDetail({ m, events, onClose }: { m: MessageRow; events: DeliveryEvent[]; onClose: () => void }) {
  return (
    <div className="w-[28rem] border-l border-border overflow-auto p-5 text-sm">
      <div className="flex items-center justify-between mb-3">
        <h3 className="font-semibold">Message #{m.id}</h3>
        <button type="button" className="text-text-dim hover:text-text" onClick={onClose}>×</button>
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
          <div className="text-text-dim text-xs uppercase tracking-wide mb-1">Delivery events</div>
          <ul className="space-y-1">
            {events.map((e) => (
              <li key={e.id} className="text-xs flex items-center gap-2">
                <StatusPill status={e.kind} />
                <span className="text-text-dim">{shortTime(e.occurred_at)}</span>
                {e.recipient && <span>{stripScheme(e.recipient)}</span>}
                {e.reason && <span className="text-text-dim">— {e.reason}</span>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

function ComposeView({
  api, senders, quota, onSent, gotoSenders,
}: {
  api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  senders: SenderRow[];
  quota: QuotaInfo | null;
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
      await api("POST", "/tools/call", {}, {
        tool: "send_message",
        args: { from: computedFrom, to: recipients, subject, body },
      });
      setTo(""); setSubject(""); setBody("");
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
      <p className="text-xs text-text-dim mb-3">Pick a verified sender, comma-separate recipients. v0.2 sends over email; SMS and push come later.</p>

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
              {verified.map((s) => {
                const addr = stripScheme(s.address);
                return (
                  <option key={addr} value={addr}>
                    {s.kind === "domain" ? `@${addr} (domain)` : addr}
                  </option>
                );
              })}
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
            <div className="text-xs text-text-dim mt-1">Will send as <code>{computedFrom}</code></div>
          )}
        </Field>
      )}

      <Field label="To">
        <input className={inputCls} value={to} onChange={(e) => setTo(e.target.value)} placeholder="alice@example.com, bob@x.io" required disabled={noVerifiedSenders} />
      </Field>
      <Field label="Subject">
        <input className={inputCls} value={subject} onChange={(e) => setSubject(e.target.value)} disabled={noVerifiedSenders} />
      </Field>
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
  rows, quota, api, reload, error,
}: {
  rows: SenderRow[];
  quota: QuotaInfo | null;
  api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>;
  reload: () => void;
  error: string;
}) {
  const [verifyAddr, setVerifyAddr] = useState("");
  const [busy, setBusy] = useState(false);
  const [verifyResult, setVerifyResult] = useState<{
    address: string;
    kind: string;
    dns_records?: { name: string; type: string; value: string }[];
    next_step?: string;
  } | null>(null);
  const [err, setErr] = useState("");

  const onVerify = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true); setErr(""); setVerifyResult(null);
    try {
      const out = await api<typeof verifyResult>("POST", "/tools/call", {}, {
        tool: "senders_verify_email",
        args: { address: verifyAddr },
      });
      setVerifyResult(out);
      setVerifyAddr("");
      reload();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const recheck = async (address: string) => {
    try {
      await api("POST", "/tools/call", {}, { tool: "senders_get", args: { address } });
      reload();
    } catch (e) { alert((e as Error).message); }
  };

  const remove = async (address: string) => {
    if (!confirm(`Delete ${stripScheme(address)} from SES? Future sends from this identity will fail.`)) return;
    try {
      await api("POST", "/tools/call", {}, { tool: "senders_delete", args: { address } });
      reload();
    } catch (e) { alert((e as Error).message); }
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
      <form onSubmit={onVerify} className="p-4 flex gap-2 items-end border-b border-border flex-wrap">
        <Field label="Verify email or domain" hint="alice@acme.com or acme.com">
          <input className={inputCls + " w-72"} value={verifyAddr} onChange={(e) => setVerifyAddr(e.target.value)} required />
        </Field>
        <button type="submit" disabled={busy} className="px-3 py-1.5 bg-accent text-white rounded disabled:opacity-50">
          {busy ? "Verifying…" : "Verify"}
        </button>
      </form>

      {err && <div className="p-4 text-red-500 text-sm">{err}</div>}
      {verifyResult && (
        <div className="m-4 p-3 rounded border border-border bg-surface-2 text-sm space-y-2">
          <div>
            <strong>{stripScheme(verifyResult.address)}</strong> — verification pending.
            {verifyResult.next_step && <span className="text-text-dim ml-1">{verifyResult.next_step}</span>}
          </div>
          {verifyResult.dns_records && verifyResult.dns_records.length > 0 && (
            <div>
              <div className="text-xs uppercase tracking-wide text-text-dim mb-1">Publish these CNAME records</div>
              <table className="text-xs font-mono">
                <tbody>
                  {verifyResult.dns_records.map((r, i) => (
                    <tr key={i}>
                      <td className="px-2 py-0.5">{r.name}</td>
                      <td className="px-2 py-0.5 text-text-dim">{r.type}</td>
                      <td className="px-2 py-0.5">{r.value}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {rows.length === 0 ? (
        <div className="p-6 text-text-dim text-sm">No senders configured. Verify an email or domain above.</div>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-xs text-text-dim">
            <tr className="border-b border-border">
              <th className="text-left px-4 py-2">Address</th>
              <th className="text-left px-4 py-2">Kind</th>
              <th className="text-left px-4 py-2">Verified</th>
              <th className="text-left px-4 py-2">DKIM</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={s.address} className="border-b border-border">
                <td className="px-4 py-2">{stripScheme(s.address)}</td>
                <td className="px-4 py-2 text-text-dim">{s.kind}</td>
                <td className="px-4 py-2"><StatusPill status={s.verified ? "verified" : "pending"} /></td>
                <td className="px-4 py-2 text-text-dim">{s.dkim_status || "—"}</td>
                <td className="px-4 py-2 text-right space-x-3">
                  <button type="button" className="text-text-dim hover:text-text text-xs" onClick={() => recheck(s.address)}>Re-check</button>
                  <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => remove(s.address)}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

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

function TemplatesView({ rows, api, reload }: { rows: TemplateRow[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void }) {
  if (rows.length === 0) {
    return <div className="p-6 text-text-dim text-sm">No templates yet. Create one via the <code>template_create</code> tool.</div>;
  }
  const handleDelete = async (id: number) => {
    if (!confirm("Delete this template?")) return;
    try {
      await api("POST", "/tools/call", {}, { tool: "template_delete", args: { id } });
      reload();
    } catch (e) {
      alert("Delete failed: " + (e as Error).message);
    }
  };
  return (
    <table className="w-full text-sm">
      <thead className="text-xs text-text-dim">
        <tr className="border-b border-border">
          <th className="text-left px-4 py-2">Name</th>
          <th className="text-left px-4 py-2">Channel</th>
          <th className="text-left px-4 py-2">Subject</th>
          <th className="text-left px-4 py-2">Updated</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        {rows.map((t) => (
          <tr key={t.id} className="border-b border-border">
            <td className="px-4 py-2 font-medium">{t.name}</td>
            <td className="px-4 py-2">{t.channel}</td>
            <td className="px-4 py-2 text-text-dim truncate max-w-md">{t.subject || "—"}</td>
            <td className="px-4 py-2 text-text-dim">{shortTime(t.updated_at)}</td>
            <td className="px-4 py-2 text-right">
              <button type="button" className="text-text-dim hover:text-red-500 text-xs" onClick={() => handleDelete(t.id)}>Delete</button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function RoutesView({ rows, api, reload }: { rows: InboundRoute[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void }) {
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
      alert("Add failed: " + (e as Error).message);
    }
  };
  const remove = async (id: number) => {
    if (!confirm("Delete this route?")) return;
    try {
      await api("POST", "/tools/call", {}, { tool: "inbound_route_delete", args: { id } });
      reload();
    } catch (e) {
      alert((e as Error).message);
    }
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

function SuppressionsView({ rows, api, reload }: { rows: SuppressionRow[]; api: <T,>(m: string, p: string, q?: Record<string, string>, b?: unknown) => Promise<T>; reload: () => void }) {
  const remove = async (addr: string) => {
    if (!confirm(`Remove ${stripScheme(addr)} from suppression?`)) return;
    try {
      await api("POST", "/tools/call", {}, { tool: "suppression_remove", args: { address: addr } });
      reload();
    } catch (e) {
      alert((e as Error).message);
    }
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

// ─── Tiny UI primitives ──────────────────────────────────────────

function StatusPill({ status }: { status: string }) {
  const cls = (() => {
    switch (status) {
      case "sent":
      case "delivered":
      case "ok":
      case "received":
      case "verified":
      case "SUCCESS":
        return "bg-green-500/20 text-green-400";
      case "pending":
      case "no_match":
      case "PENDING":
        return "bg-yellow-500/20 text-yellow-400";
      case "bounced":
      case "complained":
      case "complaint":
      case "hard-bounce":
      case "failed":
      case "target_failed":
        return "bg-red-500/20 text-red-400";
      case "manual":
        return "bg-blue-500/20 text-blue-400";
      default:
        return "bg-surface-2 text-text-dim";
    }
  })();
  return <span className={`inline-block px-1.5 py-0.5 rounded text-xs ${cls}`}>{status}</span>;
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
