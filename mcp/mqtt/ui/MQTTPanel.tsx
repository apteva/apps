// MQTTPanel — three tabs over the embedded broker.
//
//   Live      streaming feed of recent broker traffic (events bus)
//   Discovery HA-discovered device cards
//   Settings  users + bus subscriptions + test publish

import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";

const API_ROOT = "/api/apps/mqtt";
const APIContext = createContext(API_ROOT);

function useAPI() {
  return useContext(APIContext);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface AppEventEnvelope<T = unknown> {
  app: string;
  topic: string;
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

interface BrokerStatus {
  port: number;
  listen_address: string;
  clients: number;
  retained_count: number;
  message_count: number;
  users_enabled: number;
  devices: number;
  audit_rows_dropped: number;
  events_dropped: number;
  events_emitted: number;
  rate_limited_messages: number;
  auth_rejected: number;
  acl_rejected: number;
  endpoint: string;
  bind_address: string;
  advertised_host: string;
  uptime_seconds: number;
  limits: {
    max_clients: number;
    max_payload_bytes: number;
    max_publish_per_second: number;
    max_publish_burst: number;
    max_event_per_second: number;
    max_event_burst: number;
    max_event_payload_bytes: number;
    max_log_payload_bytes: number;
  };
}

interface MQTTClient {
  client_id: string;
  username: string;
  remote: string;
  protocol_version: number;
  clean_session: boolean;
}

interface MQTTUser {
  id: number;
  username: string;
  allow_publish: string[];
  allow_subscribe: string[];
  enabled: boolean;
  created_at: string;
}

interface BusSubscription {
  id: number;
  topic_pattern: string;
  bus_topic: string;
  created_at: string;
}

interface RetainedMessage {
  topic: string;
  qos: number;
  payload: unknown;
  size_bytes: number;
  updated_at: string;
}

interface Device {
  id: number;
  slug: string;
  component: string;
  display_name: string;
  manufacturer: string;
  model: string;
  state_topic: string;
  command_topic: string;
  last_seen?: string;
}

type Tab = "live" | "discovery" | "settings";

export default function MQTTPanel({ installId, projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("live");
  // Select this exact install in the proxy path. MQTT can be installed once
  // per project, and an unscoped /api/apps/mqtt request intentionally cannot
  // choose an arbitrary project's sidecar.
  const api = `${API_ROOT}/_install/${encodeURIComponent(String(installId))}`;
  return (
    <APIContext.Provider value={api}>
      <div className="flex flex-col h-full">
        <div className="flex border-b border-border">
          {(["live", "discovery", "settings"] as Tab[]).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-4 py-2 text-sm capitalize ${tab === t ? "border-b-2 border-accent" : "text-text-dim"}`}
            >
              {t}
            </button>
          ))}
        </div>
        <div className="flex-1 overflow-auto">
          {tab === "live" && <LiveTab projectId={projectId} />}
          {tab === "discovery" && <DiscoveryTab />}
          {tab === "settings" && <SettingsTab />}
        </div>
      </div>
    </APIContext.Provider>
  );
}

// ─── Live ──────────────────────────────────────────────────────────

interface LiveMessage {
  id: number;
  ts: string;
  topic: string;
  qos: number;
  retain: boolean;
  client_id: string;
  username: string;
  payload?: string;
  payload_size_bytes?: number;
  payload_binary?: boolean;
  payload_truncated?: boolean;
}

function LiveTab({ projectId }: { projectId: string }) {
  const API = useAPI();
  const [status, setStatus] = useState<BrokerStatus | null>(null);
  const [messages, setMessages] = useState<LiveMessage[]>([]);
  const [clients, setClients] = useState<MQTTClient[]>([]);
  const [filter, setFilter] = useState("");
  const [error, setError] = useState("");
  const refreshRunning = useRef(false);
  const eventRefreshTimer = useRef<number | null>(null);

  const refresh = useCallback(async () => {
    if (refreshRunning.current) return;
    refreshRunning.current = true;
    try {
      const [statusResponse, messagesResponse, clientsResponse] = await Promise.all([
        fetch(`${API}/status`),
        fetch(`${API}/messages?limit=200`),
        fetch(`${API}/clients`),
      ]);
      if (!statusResponse.ok || !messagesResponse.ok || !clientsResponse.ok) {
        throw new Error("Could not load broker state");
      }
      const [s, m, c] = await Promise.all([
        statusResponse.json(), messagesResponse.json(), clientsResponse.json(),
      ]);
      setStatus(s);
      if (Array.isArray(m)) setMessages(m);
      if (Array.isArray(c)) setClients(c);
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not load broker state");
    } finally {
      refreshRunning.current = false;
    }
  }, [API]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5_000);
    return () => clearInterval(t);
  }, [refresh]);

  useEffect(() => () => {
    if (eventRefreshTimer.current) window.clearTimeout(eventRefreshTimer.current);
  }, []);

  // Coalesce a burst of events into at most two refreshes per second.
  useAppEvents("mqtt", projectId, (ev) => {
    if (!["mqtt.message", "mqtt.client.connected", "mqtt.client.disconnected"].includes(ev.topic) || eventRefreshTimer.current) return;
    eventRefreshTimer.current = window.setTimeout(() => {
      eventRefreshTimer.current = null;
      refresh();
    }, 500);
  });

  const visible = filter
    ? messages.filter((m) => m.topic.includes(filter))
    : messages;

  return (
    <div>
      <div className="flex items-center px-4 py-3 border-b border-border gap-3">
        {status ? (
          <div className="text-xs text-text-dim flex gap-3">
            <span>:{status.port}</span>
            <span>{status.clients} connected</span>
            <span>{status.users_enabled} users</span>
            <span>{status.devices} devices</span>
            <span>{status.retained_count} retained</span>
            <span>{status.message_count} msgs logged</span>
            <span>{Math.floor(status.uptime_seconds / 60)}m uptime</span>
          </div>
        ) : (
          <div className="text-xs text-text-dim italic">Loading…</div>
        )}
        <div className="flex-1" />
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter topic substring"
          className="bg-bg-elev border border-border rounded px-2 py-1 text-xs"
        />
      </div>
      {error && <div className="mx-4 mt-3 rounded border border-error/40 bg-error/10 px-3 py-2 text-xs text-error">{error}</div>}
      {clients.length > 0 && (
        <div className="flex flex-wrap gap-2 px-4 py-2 border-b border-border">
          {clients.map((client) => (
            <span key={client.client_id} className="rounded bg-bg-elev px-2 py-1 text-xs" title={`${client.remote} · MQTT ${client.protocol_version === 5 ? "5" : "3.1.1"}`}>
              <span className="font-mono">{client.client_id}</span>
              {client.username && <span className="text-text-dim"> · {client.username}</span>}
            </span>
          ))}
        </div>
      )}
      {status && status.audit_rows_dropped > 0 && (
        <div className="mx-4 mt-3 rounded border border-warning/40 bg-warning/10 px-3 py-2 text-xs">
          {status.audit_rows_dropped.toLocaleString()} audit rows were dropped because broker traffic exceeded the persistence queue. MQTT delivery was not blocked.
        </div>
      )}
      {status && (status.events_dropped > 0 || status.rate_limited_messages > 0) && (
        <div className="mx-4 mt-3 rounded border border-warning/40 bg-warning/10 px-3 py-2 text-xs">
          {status.events_dropped.toLocaleString()} platform events dropped · {status.rate_limited_messages.toLocaleString()} client publishes rate-limited.
        </div>
      )}
      <div className="px-4 py-2">
        {visible.length === 0 ? (
          <div className="text-text-dim text-xs italic">No messages yet. Publish something or wait for a client.</div>
        ) : (
          <table className="w-full text-xs">
            <thead className="text-text-dim">
              <tr>
                <th className="text-left py-1">Time</th>
                <th className="text-left py-1">Topic</th>
                <th className="text-left py-1">Payload</th>
                <th className="text-left py-1">QoS</th>
                <th className="text-left py-1">Client</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((m) => (
                <tr key={m.id} className="border-t border-border">
                  <td className="py-1">{new Date(m.ts).toLocaleTimeString()}</td>
                  <td className="py-1 font-mono">{m.topic}</td>
                  <td className="py-1 font-mono truncate max-w-md" title={m.payload}>
                    {m.payload_binary ? `<binary preview, ${m.payload_size_bytes}B total>` : `${m.payload ?? ""}${m.payload_truncated ? `… (${m.payload_size_bytes}B total)` : ""}`}
                  </td>
                  <td className="py-1">{m.qos}{m.retain && " R"}</td>
                  <td className="py-1 text-text-dim">{m.client_id}{m.username && <span> · {m.username}</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// ─── Devices ───────────────────────────────────────────────────────

function DiscoveryTab() {
  const API = useAPI();
  const [devices, setDevices] = useState<Device[]>([]);
  const [filter, setFilter] = useState("");
  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/devices${filter ? `?filter=${encodeURIComponent(filter)}` : ""}`);
    if (r.ok) setDevices(await r.json());
  }, [API, filter]);
  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 10_000);
    return () => clearInterval(t);
  }, [refresh]);
  return (
    <div className="px-4 py-3">
      <div className="flex items-center mb-3 gap-3">
        <input
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder="Filter slug / model / manufacturer"
          className="bg-bg-elev border border-border rounded px-2 py-1 text-xs flex-1 max-w-sm"
        />
        <span className="text-xs text-text-dim">{devices.length} device(s)</span>
      </div>
      {devices.length === 0 ? (
        <div className="text-text-dim text-xs italic">
          No devices discovered yet. Devices that publish to <code className="font-mono">homeassistant/+/+/config</code> will appear here automatically.
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          {devices.map((d) => (
            <div key={d.id} className="border border-border rounded p-3">
              <div className="flex items-center gap-2">
                <span className="font-medium text-sm flex-1">{d.display_name || d.slug}</span>
                <span className="text-xs text-text-dim">{d.component}</span>
              </div>
              {(d.manufacturer || d.model) && (
                <div className="text-xs text-text-dim mt-1">{[d.manufacturer, d.model].filter(Boolean).join(" · ")}</div>
              )}
              {d.state_topic && (
                <div className="text-xs mt-1 font-mono truncate" title={d.state_topic}>
                  state: {d.state_topic}
                </div>
              )}
              {d.command_topic && (
                <div className="text-xs font-mono truncate" title={d.command_topic}>
                  cmd: {d.command_topic}
                </div>
              )}
              {d.last_seen && (
                <div className="text-xs text-text-dim mt-1">
                  last seen: {new Date(d.last_seen).toLocaleString()}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// ─── Settings ──────────────────────────────────────────────────────

function SettingsTab() {
  return (
    <div className="px-4 py-3 space-y-6">
      <ConnectionSection />
      <UsersSection />
      <SubscriptionsSection />
      <RetainedSection />
      <TestPublishSection />
    </div>
  );
}

function ConnectionSection() {
  const API = useAPI();
  const [status, setStatus] = useState<BrokerStatus | null>(null);
  useEffect(() => {
    fetch(`${API}/status`).then((r) => r.ok ? r.json() : null).then(setStatus).catch(() => {});
  }, [API]);
  return (
    <div>
      <h3 className="text-sm font-medium mb-2">Broker connection</h3>
      <div className="border border-border rounded px-3 py-2 text-xs">
        <div>Client endpoint: <code className="font-mono">{status?.endpoint || "…"}</code></div>
        {status && <div className="text-text-dim mt-1">Bind address: <code className="font-mono">{status.bind_address}</code></div>}
        {status && <div className="text-text-dim mt-1">
          Limits: {status.limits.max_clients.toLocaleString()} clients · {(status.limits.max_payload_bytes / 1024).toLocaleString()} KiB packets · {status.limits.max_publish_per_second}/s per client
        </div>}
        <div className="text-text-dim mt-1">
          Connect to the host running this sidecar. Authentication is required by default; TLS is not terminated by this app, so expose it only on a trusted network, VPN, or protected TCP proxy.
        </div>
      </div>
    </div>
  );
}

function UsersSection() {
  const API = useAPI();
  const [users, setUsers] = useState<MQTTUser[]>([]);
  const [showAdd, setShowAdd] = useState(false);
  const [editing, setEditing] = useState<{ user: MQTTUser; mode: "acl" | "password" } | null>(null);
  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/users`);
    if (r.ok) setUsers(await r.json());
  }, [API]);
  useEffect(() => { refresh(); }, [refresh]);
  return (
    <div>
      <div className="flex items-center mb-2">
        <h3 className="text-sm font-medium flex-1">Users ({users.length})</h3>
        <button onClick={() => setShowAdd(true)} className="bg-accent text-bg px-2 py-1 rounded text-xs">+ user</button>
      </div>
      {users.length === 0 ? (
        <div className="text-text-dim text-xs italic">No users.</div>
      ) : (
        <div className="border border-border rounded">
          {users.map((u) => (
            <div key={u.id} className="flex items-center px-3 py-2 border-b border-border last:border-b-0">
              <span className="font-mono text-sm flex-1">{u.username}</span>
              <span className="text-xs text-text-dim mr-3">
                pub: {u.allow_publish.join(",") || "—"} · sub: {u.allow_subscribe.join(",") || "—"}
              </span>
              <button
                onClick={async () => {
                  await fetch(`${API}/users/${encodeURIComponent(u.username)}`, {
                    method: "PATCH",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify({ enabled: !u.enabled }),
                  });
                  refresh();
                }}
                className="text-xs text-text-dim hover:text-text mr-2"
              >
                {u.enabled ? "disable" : "enable"}
              </button>
              <button onClick={() => setEditing({ user: u, mode: "acl" })} className="text-xs text-text-dim hover:text-text mr-2">
                ACL
              </button>
              <button onClick={() => setEditing({ user: u, mode: "password" })} className="text-xs text-text-dim hover:text-text mr-2">
                password
              </button>
              <button
                onClick={async () => {
                  if (!window.confirm(`Delete MQTT user ${u.username}? Connected clients using it will fail their next authentication.`)) return;
                  await fetch(`${API}/users/${encodeURIComponent(u.username)}`, { method: "DELETE" });
                  refresh();
                }}
                className="text-xs text-text-dim hover:text-error"
              >
                delete
              </button>
            </div>
          ))}
        </div>
      )}
      {showAdd && <AddUserModal onClose={() => setShowAdd(false)} onAdded={() => { setShowAdd(false); refresh(); }} />}
      {editing && <EditUserModal user={editing.user} mode={editing.mode} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); refresh(); }} />}
    </div>
  );
}

function EditUserModal({ user, mode, onClose, onSaved }: { user: MQTTUser; mode: "acl" | "password"; onClose: () => void; onSaved: () => void }) {
  const API = useAPI();
  const [password, setPassword] = useState("");
  const [pub, setPub] = useState(user.allow_publish.join(","));
  const [sub, setSub] = useState(user.allow_subscribe.join(","));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const submit = async () => {
    setBusy(true);
    setErr("");
    const body = mode === "password"
      ? { password }
      : {
          allow_publish: pub.split(",").map((s) => s.trim()).filter(Boolean),
          allow_subscribe: sub.split(",").map((s) => s.trim()).filter(Boolean),
        };
    try {
      const r = await fetch(`${API}/users/${encodeURIComponent(user.username)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(await r.text());
      onSaved();
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Could not update user");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={mode === "password" ? `Rotate password · ${user.username}` : `Edit ACL · ${user.username}`} onClose={onClose}>
      {mode === "password" ? (
        <input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="new password" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm mb-2" />
      ) : (
        <>
          <input value={pub} onChange={(e) => setPub(e.target.value)} placeholder="publish allow (comma-separated filters)" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-xs mb-2 font-mono" />
          <input value={sub} onChange={(e) => setSub(e.target.value)} placeholder="subscribe allow (comma-separated filters)" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-xs mb-2 font-mono" />
        </>
      )}
      <p className="text-xs text-text-dim mb-2">Saving disconnects active sessions for this user so the change takes effect immediately.</p>
      {err && <div className="text-error text-xs mb-2">{err}</div>}
      <div className="flex gap-2 justify-end">
        <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
        <button className="bg-accent text-bg px-3 py-1 rounded text-sm" disabled={busy || (mode === "password" && !password)} onClick={submit}>Save</button>
      </div>
    </Modal>
  );
}

function AddUserModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const API = useAPI();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [pub, setPub] = useState("#");
  const [sub, setSub] = useState("#");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await fetch(`${API}/users`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          username, password,
          allow_publish: pub.split(",").map((s) => s.trim()).filter(Boolean),
          allow_subscribe: sub.split(",").map((s) => s.trim()).filter(Boolean),
        }),
      });
      if (!r.ok) throw new Error(await r.text());
      onAdded();
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title="Add MQTT user" onClose={onClose}>
      <input value={username} onChange={(e) => setUsername(e.target.value)} placeholder="username" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm mb-2" />
      <input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="password" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm mb-2" />
      <input value={pub} onChange={(e) => setPub(e.target.value)} placeholder="publish allow (comma-separated globs)" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-xs mb-2 font-mono" />
      <input value={sub} onChange={(e) => setSub(e.target.value)} placeholder="subscribe allow (comma-separated globs)" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-xs mb-2 font-mono" />
      {err && <div className="text-error text-xs mb-2">{err}</div>}
      <div className="flex gap-2 justify-end">
        <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
        <button className="bg-accent text-bg px-3 py-1 rounded text-sm" disabled={busy || !username || !password} onClick={submit}>Save</button>
      </div>
    </Modal>
  );
}

function SubscriptionsSection() {
  const API = useAPI();
  const [subs, setSubs] = useState<BusSubscription[]>([]);
  const [topicPattern, setTopicPattern] = useState("");
  const [busTopic, setBusTopic] = useState("");
  const [err, setErr] = useState("");
  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/subscriptions`);
    if (r.ok) setSubs(await r.json());
  }, [API]);
  useEffect(() => { refresh(); }, [refresh]);
  const add = async () => {
    setErr("");
    const r = await fetch(`${API}/subscriptions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ topic_pattern: topicPattern, bus_topic: busTopic }),
    });
    if (!r.ok) {
      setErr(await r.text());
      return;
    }
    setTopicPattern("");
    setBusTopic("");
    refresh();
  };
  return (
    <div>
      <h3 className="text-sm font-medium mb-2">Bus subscriptions ({subs.length})</h3>
      <p className="text-xs text-text-dim mb-2">
        Promote MQTT topics to named platform-bus events. Pattern matches re-emit as <code className="font-mono">mqtt.&lt;bus_topic&gt;</code>.
      </p>
      <div className="flex gap-2 mb-2">
        <input value={topicPattern} onChange={(e) => setTopicPattern(e.target.value)} placeholder="motion/+/state" className="flex-1 bg-bg-elev border border-border rounded px-2 py-1 text-xs font-mono" />
        <input value={busTopic} onChange={(e) => setBusTopic(e.target.value)} placeholder="motion" className="flex-1 bg-bg-elev border border-border rounded px-2 py-1 text-xs font-mono" />
        <button className="bg-accent text-bg px-3 py-1 rounded text-xs" disabled={!topicPattern || !busTopic} onClick={add}>add</button>
      </div>
      {err && <div className="text-error text-xs mb-2">{err}</div>}
      {subs.length > 0 && (
        <div className="border border-border rounded">
          {subs.map((s) => (
            <div key={s.id} className="flex items-center px-3 py-2 border-b border-border last:border-b-0">
              <span className="font-mono text-xs flex-1">{s.topic_pattern}</span>
              <span className="text-text-dim text-xs">→ mqtt.{s.bus_topic}</span>
              <button
                onClick={async () => {
                  if (!window.confirm(`Delete bridge ${s.topic_pattern} → mqtt.${s.bus_topic}?`)) return;
                  await fetch(`${API}/subscriptions/${s.id}`, { method: "DELETE" });
                  refresh();
                }}
                className="ml-3 text-xs text-text-dim hover:text-error"
              >
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function RetainedSection() {
  const API = useAPI();
  const [messages, setMessages] = useState<RetainedMessage[]>([]);
  const [filter, setFilter] = useState("");
  const [error, setError] = useState("");
  const refresh = useCallback(async () => {
    const suffix = filter ? `?topic_pattern=${encodeURIComponent(filter)}` : "";
    const r = await fetch(`${API}/retained${suffix}`);
    if (!r.ok) {
      setError(await r.text());
      return;
    }
    setMessages(await r.json());
    setError("");
  }, [API, filter]);
  useEffect(() => { refresh(); }, [refresh]);
  return (
    <div>
      <div className="flex items-center gap-2 mb-2">
        <h3 className="text-sm font-medium flex-1">Retained state ({messages.length})</h3>
        <input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="devices/+/state" className="bg-bg-elev border border-border rounded px-2 py-1 text-xs font-mono" />
      </div>
      {error && <div className="text-error text-xs mb-2">{error}</div>}
      {messages.length === 0 ? (
        <div className="text-text-dim text-xs italic">No retained messages.</div>
      ) : (
        <div className="border border-border rounded max-h-64 overflow-auto">
          {messages.map((m) => (
            <div key={m.topic} className="flex items-center gap-3 px-3 py-2 border-b border-border last:border-b-0">
              <span className="font-mono text-xs flex-1 truncate" title={m.topic}>{m.topic}</span>
              <span className="font-mono text-xs text-text-dim max-w-xs truncate" title={typeof m.payload === "string" ? m.payload : JSON.stringify(m.payload)}>
                {typeof m.payload === "string" ? m.payload : JSON.stringify(m.payload)}
              </span>
              <span className="text-xs text-text-dim">{m.size_bytes}B · Q{m.qos}</span>
              <button
                className="text-xs text-text-dim hover:text-error"
                onClick={async () => {
                  if (!window.confirm(`Delete retained message ${m.topic}?`)) return;
                  await fetch(`${API}/retained/${encodeURIComponent(m.topic)}`, { method: "DELETE" });
                  refresh();
                }}
              >delete</button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function TestPublishSection() {
  const API = useAPI();
  const [topic, setTopic] = useState("");
  const [payload, setPayload] = useState("");
  const [retain, setRetain] = useState(false);
  const [qos, setQos] = useState(0);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const submit = async () => {
    setBusy(true);
    setMsg("");
    try {
      const r = await fetch(`${API}/test_publish`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ topic, payload, retain, qos }),
      });
      if (r.ok) setMsg("published"); else setMsg(await r.text());
    } finally {
      setBusy(false);
    }
  };
  return (
    <div>
      <h3 className="text-sm font-medium mb-2">Test publish</h3>
      <div className="flex gap-2 mb-2">
        <input value={topic} onChange={(e) => setTopic(e.target.value)} placeholder="foo/bar" className="flex-1 bg-bg-elev border border-border rounded px-2 py-1 text-xs font-mono" />
        <input value={payload} onChange={(e) => setPayload(e.target.value)} placeholder="payload" className="flex-1 bg-bg-elev border border-border rounded px-2 py-1 text-xs font-mono" />
      </div>
      <div className="flex gap-3 items-center text-xs mb-2">
        <label><input type="checkbox" checked={retain} onChange={(e) => setRetain(e.target.checked)} /> retain</label>
        <label>QoS <select value={qos} onChange={(e) => setQos(Number(e.target.value))} className="bg-bg-elev border border-border rounded px-1">
          <option>0</option><option>1</option><option>2</option>
        </select></label>
        <button className="bg-accent text-bg px-3 py-1 rounded text-xs ml-2" disabled={busy || !topic} onClick={submit}>send</button>
        {msg && <span className="text-text-dim">{msg}</span>}
      </div>
    </div>
  );
}

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50" onClick={onClose}>
      <div className="bg-bg border border-border rounded p-4 max-w-md w-full" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center mb-3">
          <h3 className="text-sm font-medium flex-1">{title}</h3>
          <button onClick={onClose} className="text-text-dim hover:text-text">×</button>
        </div>
        {children}
      </div>
    </div>
  );
}
