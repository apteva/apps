// MQTTPanel — three tabs over the embedded broker.
//
//   Live      streaming feed of recent broker traffic (events bus)
//   Devices   HA-discovered device cards
//   Settings  users + bus subscriptions + test publish

import { useCallback, useEffect, useRef, useState } from "react";

const API = "/api/apps/mqtt";

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
  retained_count: number;
  message_count: number;
  users_enabled: number;
  devices: number;
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

type Tab = "live" | "devices" | "settings";

export default function MQTTPanel({ projectId }: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("live");
  return (
    <div className="flex flex-col h-full">
      <div className="flex border-b border-border">
        {(["live", "devices", "settings"] as Tab[]).map((t) => (
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
        {tab === "devices" && <DevicesTab />}
        {tab === "settings" && <SettingsTab />}
      </div>
    </div>
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
  payload?: string;
  payload_size_bytes?: number;
  payload_binary?: boolean;
}

function LiveTab({ projectId }: { projectId: string }) {
  const [status, setStatus] = useState<BrokerStatus | null>(null);
  const [messages, setMessages] = useState<LiveMessage[]>([]);
  const [filter, setFilter] = useState("");

  const refresh = useCallback(async () => {
    const [s, m] = await Promise.all([
      fetch(`${API}/status`).then((r) => r.json()).catch(() => null),
      fetch(`${API}/messages?limit=200`).then((r) => r.json()).catch(() => []),
    ]);
    if (s) setStatus(s);
    if (Array.isArray(m)) setMessages(m);
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 5_000);
    return () => clearInterval(t);
  }, [refresh]);

  // Live mqtt.message bus events — refresh on each so the feed stays fresh
  // without spamming the network at high message rates.
  useAppEvents("mqtt", projectId, (ev) => {
    if (ev.topic === "mqtt.message") refresh();
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
            <span>{status.users_enabled} users</span>
            <span>{status.devices} devices</span>
            <span>{status.retained_count} retained</span>
            <span>{status.message_count} msgs logged</span>
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
                    {m.payload_binary ? `<binary, ${m.payload_size_bytes}B>` : m.payload}
                  </td>
                  <td className="py-1">{m.qos}{m.retain && " R"}</td>
                  <td className="py-1 text-text-dim">{m.client_id}</td>
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

function DevicesTab() {
  const [devices, setDevices] = useState<Device[]>([]);
  const [filter, setFilter] = useState("");
  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/devices${filter ? `?filter=${encodeURIComponent(filter)}` : ""}`);
    if (r.ok) setDevices(await r.json());
  }, [filter]);
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
      <UsersSection />
      <SubscriptionsSection />
      <TestPublishSection />
    </div>
  );
}

function UsersSection() {
  const [users, setUsers] = useState<MQTTUser[]>([]);
  const [showAdd, setShowAdd] = useState(false);
  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/users`);
    if (r.ok) setUsers(await r.json());
  }, []);
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
              <button
                onClick={async () => {
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
    </div>
  );
}

function AddUserModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
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
      <input value={password} onChange={(e) => setPassword(e.target.value)} placeholder="password" className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm mb-2" />
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
  const [subs, setSubs] = useState<BusSubscription[]>([]);
  const [topicPattern, setTopicPattern] = useState("");
  const [busTopic, setBusTopic] = useState("");
  const [err, setErr] = useState("");
  const refresh = useCallback(async () => {
    const r = await fetch(`${API}/subscriptions`);
    if (r.ok) setSubs(await r.json());
  }, []);
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

function TestPublishSection() {
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
