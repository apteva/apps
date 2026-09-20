import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

type Channel = "sms" | "whatsapp";
type ChannelFilter = "all" | Channel;

interface HostProps {
  appName?: string;
  installId?: number;
  projectId?: string;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
}

export interface PhoneMessage {
  id: number;
  channel: string;
  direction: "in" | "out";
  from: string;
  to: string[];
  body_text?: string;
  status: string;
  status_reason?: string;
  attachments?: Array<{ filename?: string; content_type?: string }>;
  created_at?: string;
  sent_at?: string;
  received_at?: string;
  last_event_at?: string;
}

export interface PhoneConversation {
  key: string;
  channel: Channel;
  peer: string;
  messages: PhoneMessage[];
  latest: PhoneMessage;
  latestAt: number;
}

interface MessageListResponse {
  messages?: PhoneMessage[];
}

interface PhoneTemplate {
  id: number;
  channel: string;
  name: string;
  body_text?: string;
  vars_schema?: Record<string, unknown>;
  provider_template_id?: string;
  provider_status?: string;
}

interface WidgetPreferences {
  defaultChannel: ChannelFilter;
  maxConversations: number;
}

export function messagingWidgetPreferences(settings?: Record<string, unknown>): WidgetPreferences {
  const requestedChannel = settings?.default_channel;
  const defaultChannel: ChannelFilter = requestedChannel === "sms" || requestedChannel === "whatsapp"
    ? requestedChannel
    : "all";
  const requestedLimit = Number(settings?.max_conversations ?? 10);
  return {
    defaultChannel,
    maxConversations: Number.isFinite(requestedLimit)
      ? Math.max(4, Math.min(20, Math.round(requestedLimit)))
      : 10,
  };
}

function stripAddressScheme(address: string): string {
  return address.trim().replace(/^(?:tel|sms|whatsapp):/i, "");
}

export function normalisePhoneRecipient(address: string): string {
  return stripAddressScheme(address).replace(/[\s().-]/g, "");
}

export function validPhoneRecipient(address: string): boolean {
  return /^\+[1-9]\d{6,14}$/.test(normalisePhoneRecipient(address));
}

export function phoneMessagePeer(message: PhoneMessage): string {
  return stripAddressScheme(message.direction === "in" ? message.from : ((message.to || [])[0] || ""));
}

function messageTime(message: PhoneMessage): number {
  const value = message.received_at || message.sent_at || message.created_at || message.last_event_at || "";
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : 0;
}

export function groupPhoneConversations(messages: PhoneMessage[]): PhoneConversation[] {
  const grouped = new Map<string, PhoneMessage[]>();
  const seen = new Set<number>();
  for (const message of messages) {
    if (seen.has(message.id) || (message.channel !== "sms" && message.channel !== "whatsapp")) continue;
    seen.add(message.id);
    const peer = phoneMessagePeer(message);
    if (!peer) continue;
    const key = `${message.channel}:${peer}`;
    grouped.set(key, [...(grouped.get(key) || []), message]);
  }
  return Array.from(grouped, ([key, thread]) => {
    thread.sort((left, right) => messageTime(left) - messageTime(right) || left.id - right.id);
    const latest = thread[thread.length - 1];
    return {
      key,
      channel: latest.channel as Channel,
      peer: phoneMessagePeer(latest),
      messages: thread,
      latest,
      latestAt: messageTime(latest),
    };
  }).sort((left, right) => right.latestAt - left.latestAt || right.latest.id - left.latest.id);
}

function apiURL(props: HostProps, path: string, params: Record<string, string> = {}): string {
  const query = new URLSearchParams(params);
  if (props.installId) query.set("install_id", String(props.installId));
  if (props.projectId) query.set("project_id", props.projectId);
  return `/api/apps/${encodeURIComponent(props.appName || "messaging")}${path}?${query.toString()}`;
}

async function readJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", ...init });
  if (!response.ok) {
    const raw = (await response.text().catch(() => "")).trim();
    let detail = raw;
    try {
      const payload = JSON.parse(raw) as { error?: string | { reason?: string; address?: string } };
      if (typeof payload.error === "string") detail = payload.error;
      else if (payload.error?.reason) detail = `${payload.error.address || "Recipient"}: ${payload.error.reason}`;
    } catch {}
    throw new Error(detail || `Request failed (${response.status})`);
  }
  return response.json() as Promise<T>;
}

function shortTime(timestamp: number): string {
  if (!timestamp) return "";
  const date = new Date(timestamp);
  const now = new Date();
  if (date.toDateString() === now.toDateString()) {
    return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(date);
  }
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(date);
}

function messageClock(message: PhoneMessage): string {
  const timestamp = messageTime(message);
  return timestamp
    ? new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(new Date(timestamp))
    : "";
}

function preview(message: PhoneMessage): string {
  const body = (message.body_text || "").trim().replace(/\s+/g, " ");
  if (body) return body;
  const count = message.attachments?.length || 0;
  return count ? `${count} attachment${count === 1 ? "" : "s"}` : "Message";
}

function statusMark(message: PhoneMessage): string {
  if (message.direction === "in") return "";
  if (["failed", "bounced", "complained", "rejected"].includes(message.status)) return "!";
  if (["delivered", "read"].includes(message.status)) return "✓✓";
  return "✓";
}

function initials(peer: string): string {
  const digits = peer.replace(/\D/g, "");
  return digits.slice(-2) || "#";
}

function newSendKey(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `widget-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

export function templateVariableKeys(template: PhoneTemplate): string[] {
  const keys = new Set<string>();
  if (template.vars_schema && typeof template.vars_schema === "object") {
    Object.keys(template.vars_schema).forEach((key) => keys.add(key));
  }
  const matcher = /\{\{\s*([^{}\s]+)\s*\}\}/g;
  let match: RegExpExecArray | null;
  while ((match = matcher.exec(template.body_text || ""))) keys.add(match[1]);
  return Array.from(keys).sort((left, right) => {
    const leftNumber = Number(left);
    const rightNumber = Number(right);
    if (Number.isFinite(leftNumber) && Number.isFinite(rightNumber)) return leftNumber - rightNumber;
    return left.localeCompare(right);
  });
}

function renderTemplateBody(template: PhoneTemplate, values: Record<string, string>): string {
  return (template.body_text || "").replace(/\{\{\s*([^{}\s]+)\s*\}\}/g, (_, key: string) => values[key]?.trim() || `{{${key}}}`);
}

function ChannelTabs({ value, onChange }: { value: ChannelFilter; onChange: (value: ChannelFilter) => void }) {
  return (
    <div className="flex rounded-full border border-border bg-bg p-0.5" aria-label="Message channel">
      {(["all", "sms", "whatsapp"] as ChannelFilter[]).map((channel) => (
        <button
          key={channel}
          type="button"
          aria-pressed={value === channel}
          onClick={() => onChange(channel)}
          className={`rounded-full px-2 py-1 text-[9px] font-semibold uppercase tracking-wide ${value === channel ? "bg-accent text-white" : "text-text-dim hover:text-text"}`}
        >
          {channel === "whatsapp" ? "WA" : channel}
        </button>
      ))}
    </div>
  );
}

function ConversationList({ conversations, selected, onSelect }: {
  conversations: PhoneConversation[];
  selected: string;
  onSelect: (conversation: PhoneConversation) => void;
}) {
  if (!conversations.length) {
    return <div className="flex min-h-40 items-center justify-center px-5 text-center text-xs text-text-dim">No SMS or WhatsApp conversations yet.</div>;
  }
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      {conversations.map((conversation) => (
        <button
          key={conversation.key}
          type="button"
          onClick={() => onSelect(conversation)}
          className={`flex w-full items-center gap-3 border-b border-border px-3 py-3 text-left last:border-b-0 ${selected === conversation.key ? "bg-accent/10" : "hover:bg-bg-hover/60"}`}
        >
          <span className={`flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-[10px] font-bold ${conversation.channel === "whatsapp" ? "bg-green/15 text-green" : "bg-accent/15 text-accent"}`}>
            {initials(conversation.peer)}
          </span>
          <span className="min-w-0 flex-1">
            <span className="flex items-center gap-2">
              <strong className="min-w-0 flex-1 truncate text-xs text-text">{conversation.peer}</strong>
              <time className="shrink-0 text-[9px] text-text-dim">{shortTime(conversation.latestAt)}</time>
            </span>
            <span className="mt-0.5 flex items-center gap-1.5">
              <span className={`text-[8px] font-bold uppercase ${conversation.channel === "whatsapp" ? "text-green" : "text-accent"}`}>
                {conversation.channel === "whatsapp" ? "WhatsApp" : "SMS"}
              </span>
              <span className="truncate text-[10px] text-text-muted">
                {conversation.latest.direction === "out" ? "You: " : ""}{preview(conversation.latest)}
              </span>
            </span>
          </span>
        </button>
      ))}
    </div>
  );
}

function NewMessageComposer({ props, initialChannel, onClose, onSent }: {
  props: HostProps;
  initialChannel: Channel;
  onClose: () => void;
  onSent: (channel: Channel, peer: string) => Promise<void>;
}) {
  const [channel, setChannel] = useState<Channel>(initialChannel);
  const [recipient, setRecipient] = useState("");
  const [body, setBody] = useState("");
  const [templates, setTemplates] = useState<PhoneTemplate[]>([]);
  const [templatesLoading, setTemplatesLoading] = useState(false);
  const [templateID, setTemplateID] = useState("");
  const [templateVars, setTemplateVars] = useState<Record<string, string>>({});
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const sendKey = useRef(newSendKey());

  useEffect(() => {
    sendKey.current = newSendKey();
  }, [channel, recipient, body, templateID, templateVars]);

  useEffect(() => {
    if (channel !== "whatsapp") {
      setTemplateID("");
      setTemplateVars({});
      return;
    }
    let cancelled = false;
    setTemplatesLoading(true);
    readJSON<{ templates?: PhoneTemplate[] }>(apiURL(props, "/tools/call"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ tool: "template_list", args: { channel: "whatsapp", limit: 200, offset: 0 } }),
    }).then((result) => {
      if (cancelled) return;
      setTemplates((result.templates || []).filter((template) =>
        Boolean(template.provider_template_id) && (template.provider_status || "").toLowerCase() === "approved"
      ));
    }).catch((caught) => {
      if (!cancelled) setError(caught instanceof Error ? caught.message : "Unable to load WhatsApp templates");
    }).finally(() => {
      if (!cancelled) setTemplatesLoading(false);
    });
    return () => { cancelled = true; };
  }, [channel, props.appName, props.installId, props.projectId]);

  const selectedTemplate = templates.find((template) => String(template.id) === templateID) || null;
  const variableKeys = selectedTemplate ? templateVariableKeys(selectedTemplate) : [];

  useEffect(() => {
    if (!selectedTemplate) {
      setTemplateVars({});
      return;
    }
    setTemplateVars((current) => Object.fromEntries(variableKeys.map((key) => [key, current[key] || ""])));
  }, [templateID]);

  const send = async (event: FormEvent) => {
    event.preventDefault();
    const peer = normalisePhoneRecipient(recipient);
    if (!validPhoneRecipient(peer)) {
      setError("Enter a phone number in international format, for example +15551234567.");
      return;
    }
    if (!selectedTemplate && !body.trim()) {
      setError("Enter a message.");
      return;
    }
    const missingVariables = variableKeys.filter((key) => !templateVars[key]?.trim());
    if (selectedTemplate && missingVariables.length) {
      setError(`Fill template variable${missingVariables.length === 1 ? "" : "s"} ${missingVariables.map((key) => `{{${key}}}`).join(", ")}.`);
      return;
    }
    setSending(true);
    setError("");
    try {
      const args: Record<string, unknown> = {
        channel,
        to: peer,
        idempotency_key: sendKey.current,
      };
      if (selectedTemplate) {
        args.template_id = selectedTemplate.id;
        args.vars = templateVars;
      } else {
        args.body = body.trim();
      }
      const result = await readJSON<PhoneMessage>(apiURL(props, "/tools/call"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ tool: "send_message", args }),
      });
      if (["failed", "suppressed"].includes(result.status)) {
        throw new Error(result.status_reason || `Message was not sent (${result.status}).`);
      }
      await onSent(channel, peer);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Unable to send message");
    } finally {
      setSending(false);
    }
  };

  return (
    <form onSubmit={send} className="flex min-h-0 flex-1 flex-col bg-bg">
      <header className="flex items-center gap-3 border-b border-border bg-bg-card px-3 py-2.5">
        <button type="button" onClick={onClose} className="text-lg text-text-muted hover:text-text" aria-label="Close new message">‹</button>
        <div className="min-w-0 flex-1">
          <strong className="block text-xs text-text">New message</strong>
          <span className="text-[9px] text-text-dim">Start an SMS or WhatsApp conversation</span>
        </div>
      </header>
      <div className="min-h-0 flex-1 space-y-4 overflow-auto p-4">
        <fieldset>
          <legend className="mb-1.5 text-[9px] font-bold uppercase tracking-wide text-text-dim">Channel</legend>
          <div className="grid grid-cols-2 gap-2">
            {(["sms", "whatsapp"] as Channel[]).map((item) => (
              <button
                key={item}
                type="button"
                aria-pressed={channel === item}
                onClick={() => { setChannel(item); setError(""); }}
                className={`rounded-lg border px-3 py-2 text-xs font-semibold ${channel === item ? "border-accent bg-accent/10 text-accent" : "border-border text-text-muted hover:bg-bg-hover"}`}
              >
                {item === "whatsapp" ? "WhatsApp" : "SMS"}
              </button>
            ))}
          </div>
        </fieldset>
        <label className="block">
          <span className="mb-1.5 block text-[9px] font-bold uppercase tracking-wide text-text-dim">To</span>
          <input
            autoFocus
            type="tel"
            value={recipient}
            onChange={(event) => setRecipient(event.target.value)}
            placeholder="+15551234567"
            className="w-full rounded-lg border border-border bg-bg px-3 py-2.5 text-xs text-text outline-none focus:border-accent"
          />
          <span className="mt-1 block text-[9px] text-text-dim">Use an international number beginning with +.</span>
        </label>
        {channel === "whatsapp" && (
          <label className="block">
            <span className="mb-1.5 block text-[9px] font-bold uppercase tracking-wide text-text-dim">Message type</span>
            <select
              value={templateID}
              onChange={(event) => { setTemplateID(event.target.value); setError(""); }}
              disabled={templatesLoading}
              className="w-full rounded-lg border border-border bg-bg px-3 py-2.5 text-xs text-text outline-none focus:border-accent"
            >
              <option value="">Free-form message</option>
              {templates.map((template) => <option key={template.id} value={String(template.id)}>{template.name}</option>)}
            </select>
            <span className="mt-1 block text-[9px] text-text-dim">
              {templatesLoading
                ? "Loading approved templates…"
                : templates.length
                  ? "Starting a WhatsApp conversation usually requires an approved template. Free-form messages require an inbound reply within 24 hours."
                  : "No approved templates are available. Free-form messages require an inbound reply within 24 hours."}
            </span>
          </label>
        )}
        {selectedTemplate ? (
          <div className="space-y-3">
            <div className="rounded-xl border border-border bg-bg-card p-3">
              <span className="text-[9px] font-bold uppercase tracking-wide text-text-dim">Preview</span>
              <p className="mt-2 whitespace-pre-wrap text-xs leading-5 text-text">{renderTemplateBody(selectedTemplate, templateVars)}</p>
            </div>
            {variableKeys.map((key) => (
              <label key={key} className="block">
                <span className="mb-1.5 block text-[9px] font-bold uppercase tracking-wide text-text-dim">{`{{${key}}}`}</span>
                <input
                  value={templateVars[key] || ""}
                  onChange={(event) => setTemplateVars((current) => ({ ...current, [key]: event.target.value }))}
                  className="w-full rounded-lg border border-border bg-bg px-3 py-2.5 text-xs text-text outline-none focus:border-accent"
                />
              </label>
            ))}
          </div>
        ) : (
          <label className="block">
            <span className="mb-1.5 block text-[9px] font-bold uppercase tracking-wide text-text-dim">Message</span>
            <textarea
              value={body}
              onChange={(event) => setBody(event.target.value)}
              rows={5}
              placeholder="Write a message…"
              className="w-full resize-none rounded-xl border border-border bg-bg px-3 py-2.5 text-xs leading-5 text-text outline-none focus:border-accent"
            />
          </label>
        )}
        {error && <p role="alert" className="rounded-lg border border-red/25 bg-red/5 p-2.5 text-[10px] text-red">{error}</p>}
      </div>
      <footer className="flex justify-end gap-2 border-t border-border bg-bg-card p-3">
        <button type="button" onClick={onClose} className="rounded-full border border-border px-3 py-2 text-[10px] font-semibold text-text-muted">Cancel</button>
        <button type="submit" disabled={sending || !recipient.trim() || (!selectedTemplate && !body.trim())} className="rounded-full bg-accent px-4 py-2 text-[10px] font-bold text-white disabled:opacity-40">
          {sending ? "Sending…" : "Send message"}
        </button>
      </footer>
    </form>
  );
}

function Thread({ conversation, onBack, onSent, props, compact }: {
  conversation: PhoneConversation;
  onBack: () => void;
  onSent: () => Promise<void>;
  props: HostProps;
  compact: boolean;
}) {
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const sendKey = useRef(newSendKey());

  useEffect(() => {
    setDraft("");
    setError("");
    sendKey.current = newSendKey();
  }, [conversation.key]);

  useEffect(() => {
    sendKey.current = newSendKey();
  }, [draft]);

  const send = async (event: FormEvent) => {
    event.preventDefault();
    const body = draft.trim();
    if (!body || sending) return;
    setSending(true);
    setError("");
    try {
      const result = await readJSON<PhoneMessage>(apiURL(props, "/tools/call"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          tool: "send_message",
          args: { channel: conversation.channel, to: conversation.peer, body, idempotency_key: sendKey.current },
        }),
      });
      if (result.status === "failed") throw new Error(result.status_reason || "The provider rejected this message.");
      setDraft("");
      sendKey.current = newSendKey();
      await onSent();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Unable to send message");
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-bg">
      <header className="flex items-center gap-3 border-b border-border bg-bg-card px-3 py-2.5">
        {compact && <button type="button" onClick={onBack} className="text-lg text-text-muted hover:text-text" aria-label="Back to conversations">‹</button>}
        <span className={`flex h-8 w-8 items-center justify-center rounded-full text-[9px] font-bold ${conversation.channel === "whatsapp" ? "bg-green/15 text-green" : "bg-accent/15 text-accent"}`}>
          {initials(conversation.peer)}
        </span>
        <span className="min-w-0">
          <strong className="block truncate text-xs text-text">{conversation.peer}</strong>
          <span className="text-[9px] uppercase text-text-dim">{conversation.channel === "whatsapp" ? "WhatsApp" : "SMS"}</span>
        </span>
      </header>
      <div className="flex min-h-0 flex-1 flex-col-reverse overflow-auto px-3 py-3">
        <div className="space-y-2">
          {conversation.messages.slice(-100).map((message) => (
            <div key={message.id} className={`flex ${message.direction === "out" ? "justify-end" : "justify-start"}`}>
              <div className={`max-w-[82%] rounded-2xl px-3 py-2 ${message.direction === "out" ? "rounded-br-sm bg-accent text-white" : "rounded-bl-sm border border-border bg-bg-card text-text"}`}>
                <p className="whitespace-pre-wrap break-words text-xs leading-5">{preview(message)}</p>
                <div className={`mt-1 flex items-center justify-end gap-1 text-[8px] ${message.direction === "out" ? "text-white/70" : "text-text-dim"}`}>
                  <time>{messageClock(message)}</time>
                  {statusMark(message) && <span title={message.status}>{statusMark(message)}</span>}
                </div>
              </div>
            </div>
          ))}
        </div>
      </div>
      <form onSubmit={send} className="border-t border-border bg-bg-card p-2.5">
        {error && <p role="alert" className="mb-2 text-[10px] text-red">{error}</p>}
        <div className="flex items-end gap-2">
          <textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            rows={2}
            placeholder={`Message ${conversation.peer}`}
            className="min-h-10 flex-1 resize-none rounded-2xl border border-border bg-bg px-3 py-2 text-xs text-text outline-none focus:border-accent"
          />
          <button type="submit" disabled={!draft.trim() || sending} className="h-9 rounded-full bg-accent px-3 text-[10px] font-bold text-white disabled:opacity-40">
            {sending ? "Sending…" : "Send"}
          </button>
        </div>
      </form>
    </div>
  );
}

export default function MessagingWidget(props: HostProps) {
  const preferences = messagingWidgetPreferences(props.widgetSettings);
  const [channel, setChannel] = useState<ChannelFilter>(preferences.defaultChannel);
  const [messages, setMessages] = useState<PhoneMessage[]>([]);
  const [selectedKey, setSelectedKey] = useState("");
  const [composing, setComposing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [reloadRevision, setReloadRevision] = useState(0);
  const full = props.widgetSize === "full";

  const load = useCallback(async () => {
    if (!props.projectId) {
      setMessages([]);
      setLoading(false);
      return;
    }
    setLoading(true);
    setError("");
    try {
      const responses = await Promise.all(([
        "sms",
        "whatsapp",
      ] as Channel[]).map((item) => readJSON<MessageListResponse>(apiURL(props, "/messages", {
        channel: item,
        limit: "200",
        offset: "0",
      }))));
      setMessages(responses.flatMap((response) => response.messages || []));
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Unable to load messages");
    } finally {
      setLoading(false);
    }
  }, [props.appName, props.installId, props.projectId, props.eventRevision, reloadRevision]);

  useEffect(() => { void load(); }, [load]);

  const allConversations = useMemo(() => groupPhoneConversations(messages), [messages]);
  const conversations = useMemo(() => allConversations
    .filter((conversation) => channel === "all" || conversation.channel === channel)
    .slice(0, preferences.maxConversations), [allConversations, channel, preferences.maxConversations]);
  const selected = conversations.find((conversation) => conversation.key === selectedKey) || null;

  useEffect(() => {
    if (full && !composing && conversations.length && !selected && !selectedKey) setSelectedKey(conversations[0].key);
    if (!conversations.length && selectedKey) setSelectedKey("");
  }, [full, composing, conversations, selected, selectedKey]);

  const sentNewMessage = async (sentChannel: Channel, peer: string) => {
    setChannel(sentChannel);
    setSelectedKey(`${sentChannel}:${peer}`);
    setComposing(false);
    await load();
  };

  const conversationList = (
    <div className="flex min-h-0 flex-1 flex-col bg-bg-card">
      {loading ? (
        <p className="p-4 text-xs text-text-dim">Loading conversations…</p>
      ) : error ? (
        <div className="p-4">
          <p className="text-xs text-red">{error}</p>
          <button type="button" onClick={() => setReloadRevision((value) => value + 1)} className="mt-2 text-xs text-accent">Try again</button>
        </div>
      ) : (
        <ConversationList conversations={conversations} selected={selectedKey} onSelect={(conversation) => setSelectedKey(conversation.key)} />
      )}
    </div>
  );

  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-xl border border-border bg-bg-card shadow-sm">
      <header className="flex items-center gap-3 border-b border-border px-3 py-2.5">
        <div className="min-w-0 flex-1">
          <h2 className="text-sm font-bold text-text">Messages</h2>
          <p className="truncate text-[9px] text-text-dim">SMS and WhatsApp</p>
        </div>
        <ChannelTabs value={channel} onChange={(value) => { setChannel(value); setSelectedKey(""); }} />
        <button type="button" onClick={() => setComposing(true)} className="rounded-full bg-accent px-2.5 py-1.5 text-[9px] font-bold text-white" aria-label="New message">+ New</button>
        <button type="button" onClick={() => setReloadRevision((value) => value + 1)} disabled={loading} className="text-sm text-text-dim hover:text-text disabled:opacity-40" aria-label="Refresh messages">↻</button>
      </header>
      {full ? (
        <div className="grid min-h-0 flex-1 grid-cols-[minmax(220px,0.8fr)_minmax(0,1.4fr)] divide-x divide-border">
          {conversationList}
          {composing ? (
            <NewMessageComposer props={props} initialChannel={channel === "whatsapp" ? "whatsapp" : "sms"} onClose={() => setComposing(false)} onSent={sentNewMessage} />
          ) : selected ? (
            <Thread conversation={selected} onBack={() => setSelectedKey("")} onSent={load} props={props} compact={false} />
          ) : (
            <div className="flex items-center justify-center p-6 text-xs text-text-dim">Choose a conversation.</div>
          )}
        </div>
      ) : composing ? (
        <NewMessageComposer props={props} initialChannel={channel === "whatsapp" ? "whatsapp" : "sms"} onClose={() => setComposing(false)} onSent={sentNewMessage} />
      ) : selected ? (
        <Thread conversation={selected} onBack={() => setSelectedKey("")} onSent={load} props={props} compact />
      ) : conversationList}
    </section>
  );
}
