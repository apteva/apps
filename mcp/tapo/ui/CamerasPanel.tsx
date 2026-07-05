// CamerasPanel — registry + live preview grid for Tapo cameras.
//
// Layout:
//   ┌─ topbar ────────────────────────────────────────────┐
//   │  [room filter ▾]    [+ Add camera]                 │
//   ├─────────────────────────────────────────────────────┤
//   │  ┌────────┐ ┌────────┐ ┌────────┐                  │
//   │  │ tile   │ │ tile   │ │ tile   │   (snapshot grid)│
//   │  └────────┘ └────────┘ └────────┘                  │
//   └─────────────────────────────────────────────────────┘
//
// Click a tile → modal detail with PTZ joystick, toggles row, and
// motion timeline. Tile snapshots refresh every 5s; we use the
// /api/apps/tapo/snapshots/{id}.jpg passthrough so the browser does
// the work, not the sidecar's JSON layer.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/tapo";
const SNAP_REFRESH_MS = 5000;
const EVENT_HIGHLIGHT_MS = 12_000;

function apiPath(installId: number, path: string) {
  const join = path.includes("?") ? "&" : "?";
  return `${API}${path}${join}install_id=${encodeURIComponent(String(installId))}`;
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Capabilities {
  ptz: boolean;
  privacy_lens: boolean;
  siren: boolean;
  night_vision: boolean;
  status_led: boolean;
  motion_detect: boolean;
  baby_cry: boolean;
  onvif_port: number;
}

interface Camera {
  id: number;
  name: string;
  room: string;
  ip: string;
  model: string;
  firmware: string;
  capabilities: Capabilities;
  online: boolean;
  last_seen_at?: string;
  last_error?: string;
}

interface MotionEvent {
  id: number;
  camera_id: number;
  occurred_at: string;
  kind: string;
  snapshot_file_id?: number;
  source?: string;
  onvif_topic?: string;
}

interface AppBusEnvelope {
  topic: string;
  data: {
    camera_id?: number;
    occurred_at?: string;
    kind?: string;
    source?: string;
    onvif_topic?: string;
  };
}

export default function CamerasPanel({ installId, projectId }: NativePanelProps) {
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [room, setRoom] = useState<string>("");
  const [showAdd, setShowAdd] = useState(false);
  const [selected, setSelected] = useState<Camera | null>(null);
  const [status, setStatus] = useState("");
  const [latestEvents, setLatestEvents] = useState<Record<number, MotionEvent>>({});
  const [highlighted, setHighlighted] = useState<Record<number, boolean>>({});

  const refresh = useCallback(async () => {
    try {
      const r = await fetch(apiPath(installId, "/cameras"));
      if (!r.ok) throw new Error(await r.text());
      setCameras(await r.json());
    } catch (e: any) {
      setStatus(`load: ${e.message}`);
    }
  }, [installId]);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 30_000);
    return () => clearInterval(t);
  }, [refresh]);

  // Subscribe to tapo.* events so the panel can react immediately
  // without waiting for the next cameras/motion-events poll.
  useEffect(() => {
    if (!projectId) return;
    const timers = new Map<number, number>();
    const es = new EventSource(
      `/api/app-events/tapo?project_id=${encodeURIComponent(projectId)}`,
      { withCredentials: true },
    );
    es.onmessage = (e) => {
      try {
        const env = JSON.parse(e.data) as AppBusEnvelope;
        if (!env.topic.startsWith("tapo.") || !env.data.camera_id) return;
        const event = eventFromBus(env);
        setLatestEvents((prev) => ({ ...prev, [event.camera_id]: event }));
        setHighlighted((prev) => ({ ...prev, [event.camera_id]: true }));
        const prevTimer = timers.get(event.camera_id);
        if (prevTimer) window.clearTimeout(prevTimer);
        timers.set(event.camera_id, window.setTimeout(() => {
          setHighlighted((prev) => {
            const next = { ...prev };
            delete next[event.camera_id];
            return next;
          });
          timers.delete(event.camera_id);
        }, EVENT_HIGHLIGHT_MS));
      } catch {}
    };
    return () => {
      es.close();
      timers.forEach((timer) => window.clearTimeout(timer));
    };
  }, [projectId]);

  const rooms = useMemo(
    () => Array.from(new Set(cameras.map((c) => c.room).filter(Boolean))).sort(),
    [cameras],
  );
  const filtered = useMemo(
    () => (room ? cameras.filter((c) => c.room === room) : cameras),
    [cameras, room],
  );

  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center gap-3 px-4 py-3 border-b border-border">
        <select
          className="bg-bg-elev border border-border rounded px-2 py-1 text-sm"
          value={room}
          onChange={(e) => setRoom(e.target.value)}
        >
          <option value="">All rooms</option>
          {rooms.map((r) => (
            <option key={r} value={r}>{r}</option>
          ))}
        </select>
        <div className="flex-1" />
        <button
          className="bg-accent text-bg px-3 py-1 rounded text-sm"
          onClick={() => setShowAdd(true)}
        >
          + Add camera
        </button>
      </div>

      {status && (
        <div className="px-4 py-2 text-sm text-error">{status}</div>
      )}

      <div className="flex-1 overflow-auto p-4 grid gap-4"
           style={{
             alignItems: "start",
             gridAutoRows: "max-content",
             gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
           }}>
        {filtered.map((c) => (
          <CameraTile
            key={c.id}
            camera={c}
            installId={installId}
            latestEvent={latestEvents[c.id]}
            highlighted={!!highlighted[c.id]}
            onClick={() => setSelected(c)}
          />
        ))}
        {filtered.length === 0 && (
          <div className="col-span-full text-center text-text-dim py-12">
            No cameras yet. Click <em>Add camera</em> to register one.
          </div>
        )}
      </div>

      {showAdd && (
        <AddCameraModal
          installId={installId}
          onClose={() => setShowAdd(false)}
          onAdded={() => { setShowAdd(false); refresh(); }}
        />
      )}
      {selected && (
        <CameraDetail
          camera={selected}
          installId={installId}
          latestEvent={latestEvents[selected.id]}
          onClose={() => setSelected(null)}
          onChanged={refresh}
        />
      )}
    </div>
  );
}

function eventFromBus(env: AppBusEnvelope): MotionEvent {
  const cameraID = Number(env.data.camera_id);
  return {
    id: -Date.now(),
    camera_id: cameraID,
    occurred_at: env.data.occurred_at || new Date().toISOString(),
    kind: env.data.kind || env.topic.replace(/^tapo\./, "") || "motion",
    source: env.data.source,
    onvif_topic: env.data.onvif_topic,
  };
}

function formatEventAge(iso: string) {
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "";
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (seconds < 5) return "now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

// ─── tile ───────────────────────────────────────────────────────────

function CameraImage({
  camera,
  installId,
  refreshMs,
  className,
}: { camera: Camera; installId: number; refreshMs: number; className?: string }) {
  const [frameURL, setFrameURL] = useState("");
  const currentURL = useRef("");

  useEffect(() => {
    if (!camera.online) return;
    let stopped = false;
    let timer: number | undefined;
    const controller = new AbortController();

    const load = async () => {
      try {
        const r = await fetch(apiPath(installId, `/snapshots/${camera.id}.jpg?t=${Date.now()}`), {
          cache: "no-store",
          signal: controller.signal,
        });
        if (!r.ok) throw new Error(await r.text());
        const blob = await r.blob();
        const url = URL.createObjectURL(blob);
        await new Promise<void>((resolve, reject) => {
          const img = new Image();
          img.onload = () => resolve();
          img.onerror = () => reject(new Error("image decode failed"));
          img.src = url;
        });
        if (stopped) {
          URL.revokeObjectURL(url);
          return;
        }
        setFrameURL((prev) => {
          if (prev) URL.revokeObjectURL(prev);
          currentURL.current = url;
          return url;
        });
      } catch {
        // Keep the last good frame visible. The next tick will retry.
      } finally {
        if (!stopped) timer = window.setTimeout(load, refreshMs);
      }
    };

    load();
    return () => {
      stopped = true;
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
      if (currentURL.current) {
        URL.revokeObjectURL(currentURL.current);
        currentURL.current = "";
      }
    };
  }, [camera.id, camera.online, installId, refreshMs]);

  if (!camera.online) {
    return <span className="text-sm">offline</span>;
  }
  if (!frameURL) {
    return <span className="text-sm text-text-dim">loading…</span>;
  }
  return <img src={frameURL} alt={camera.name} className={className || "w-full h-full object-cover"} />;
}

function CameraTile({
  camera,
  installId,
  latestEvent,
  highlighted,
  onClick,
}: {
  camera: Camera;
  installId: number;
  latestEvent?: MotionEvent;
  highlighted: boolean;
  onClick: () => void;
}) {
  return (
    <div
      data-camera-tile={camera.id}
      onClick={onClick}
      className={`bg-bg-elev border rounded-lg overflow-hidden cursor-pointer transition ${
        highlighted ? "border-accent ring-2 ring-accent/60" : "border-border hover:border-accent"
      }`}
    >
      <div className="aspect-video bg-black flex items-center justify-center text-text-dim relative">
        <CameraImage camera={camera} installId={installId} refreshMs={SNAP_REFRESH_MS} />
        {highlighted && latestEvent && (
          <div className="absolute top-2 left-2 bg-accent text-bg text-xs font-medium px-2 py-1 rounded">
            {latestEvent.kind || "motion"} detected
          </div>
        )}
      </div>
      <div className="px-3 py-2 flex items-center gap-2">
        <span
          className={`w-2 h-2 rounded-full ${camera.online ? "bg-success" : "bg-error"}`}
        />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium truncate">{camera.name}</div>
          <div className="text-xs text-text-dim truncate">
            {camera.room || "—"} · {camera.model || "Tapo"}
          </div>
          {latestEvent && (
            <div className="text-xs text-accent truncate">
              {latestEvent.kind || "motion"} {formatEventAge(latestEvent.occurred_at)}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ─── add modal ──────────────────────────────────────────────────────

function AddCameraModal({
  installId,
  onClose,
  onAdded,
}: { installId: number; onClose: () => void; onAdded: () => void }) {
  const [name, setName] = useState("");
  const [ip, setIp] = useState("");
  const [room, setRoom] = useState("");
  const [user, setUser] = useState("");
  const [pass, setPass] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await fetch(apiPath(installId, "/cameras"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, ip, room, username: user, password: pass }),
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
    <Modal title="Add camera" onClose={onClose}>
      <div className="space-y-3">
        <Field label="Name"      value={name} onChange={setName}     placeholder="Front porch" />
        <Field label="Room"      value={room} onChange={setRoom}     placeholder="Outside" />
        <Field label="IP address"value={ip}   onChange={setIp}       placeholder="192.168.1.42" />
        <Field label="Username"  value={user} onChange={setUser}     placeholder="Camera Account user" />
        <Field label="Password"  value={pass} onChange={setPass}     type="password" />
        <p className="text-xs text-text-dim">
          Use the <strong>Camera Account</strong> credentials set in the Tapo mobile app
          (<em>Settings → Advanced Settings → Camera Account</em>) — not your TP-Link cloud login.
        </p>
        {err && <div className="text-sm text-error">{err}</div>}
        <div className="flex gap-2 justify-end pt-2">
          <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
          <button
            className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50"
            onClick={submit}
            disabled={busy || !name || !ip || !user || !pass}
          >
            {busy ? "Probing…" : "Add"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// ─── detail modal ───────────────────────────────────────────────────

function CameraDetail({
  camera,
  installId,
  latestEvent,
  onClose,
  onChanged,
}: {
  camera: Camera;
  installId: number;
  latestEvent?: MotionEvent;
  onClose: () => void;
  onChanged: () => void;
}) {
  const [events, setEvents] = useState<MotionEvent[]>([]);
  const [busy, setBusy] = useState(false);
  const caps = camera.capabilities;

  useEffect(() => {
    fetch(apiPath(installId, `/motion-events?camera_id=${camera.id}&limit=20`))
      .then((r) => r.json())
      .then(setEvents)
      .catch(() => {});
  }, [camera.id, installId]);

  useEffect(() => {
    if (!latestEvent || latestEvent.camera_id !== camera.id) return;
    setEvents((prev) => {
      const exists = prev.some((event) =>
        event.id === latestEvent.id ||
        (event.occurred_at === latestEvent.occurred_at && event.kind === latestEvent.kind),
      );
      if (exists) return prev;
      return [latestEvent, ...prev].slice(0, 20);
    });
  }, [camera.id, latestEvent]);

  const ptz = async (direction: string) => {
    setBusy(true);
    try {
      await fetch(apiPath(installId, `/cameras/${camera.id}/ptz`), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ direction, duration_ms: 400 }),
      });
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title={camera.name} onClose={onClose} wide>
      <div className="grid gap-4 lg:grid-cols-[minmax(320px,1.2fr)_minmax(260px,0.8fr)]">
        <div>
          <div className="aspect-video bg-black rounded overflow-hidden">
            {camera.online ? (
              <div className="w-full h-full flex items-center justify-center text-text-dim">
                <CameraImage camera={camera} installId={installId} refreshMs={250} />
              </div>
            ) : (
              <div className="flex items-center justify-center h-full text-text-dim">offline</div>
            )}
          </div>
          {caps.ptz && (
            <div className="grid grid-cols-3 gap-1 mt-3 max-w-[180px] mx-auto select-none">
              <span />
              <PTZBtn label="↑" onClick={() => ptz("up")}    busy={busy} />
              <span />
              <PTZBtn label="←" onClick={() => ptz("left")}  busy={busy} />
              <PTZBtn label="·" onClick={() => ptz("stop")}  busy={busy} />
              <PTZBtn label="→" onClick={() => ptz("right")} busy={busy} />
              <span />
              <PTZBtn label="↓" onClick={() => ptz("down")}  busy={busy} />
              <span />
            </div>
          )}
        </div>

        <div className="space-y-3 text-sm">
          <div className="text-text-dim">
            {camera.model} · fw {camera.firmware} · {camera.ip}
          </div>
          <Toggles camera={camera} installId={installId} onChanged={onChanged} />
          <div>
            <div className="text-xs text-text-dim mb-1">Recent motion</div>
            <div className="border border-border rounded max-h-64 overflow-auto">
              {events.length === 0 && (
                <div className="text-text-dim text-xs p-3">No events in the cache.</div>
              )}
              {events.map((e) => (
                <div key={e.id} className="px-3 py-2 border-b border-border last:border-b-0 flex items-center gap-2">
                  <span className="text-xs text-text-dim w-32">
                    {new Date(e.occurred_at).toLocaleString()}
                  </span>
                  <span className="text-xs">{e.kind}</span>
                  {e.source && (
                    <span className="text-[11px] text-text-dim">{e.source}</span>
                  )}
                  {e.snapshot_file_id && (
                    <a
                      className="text-xs text-accent ml-auto"
                      href={`/api/apps/storage/files/${e.snapshot_file_id}/raw`}
                      target="_blank"
                      rel="noreferrer"
                    >
                      view
                    </a>
                  )}
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
    </Modal>
  );
}

function Toggles({
  camera,
  installId,
  onChanged,
}: { camera: Camera; installId: number; onChanged: () => void }) {
  const post = async (path: string, body: any) => {
    await fetch(apiPath(installId, `/cameras/${camera.id}/${path}`), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    onChanged();
  };
  // The simple toggles (privacy / led / motion) are surfaced through
  // the MCP tools; the panel doesn't replicate every server route. A
  // future iteration will add a /toggles endpoint and wire it here.
  return (
    <div className="text-xs text-text-dim space-y-1">
      <div>privacy lens: {camera.capabilities.privacy_lens ? "supported" : "—"}</div>
      <div>siren: {camera.capabilities.siren ? "supported" : "—"}</div>
      <div>night vision: {camera.capabilities.night_vision ? "supported" : "—"}</div>
      <div>motion detect: {camera.capabilities.motion_detect ? "supported" : "—"}</div>
      <div className="pt-1 italic">
        Trigger toggles via the agent (privacy_set, led_set, motion_detection_set).
      </div>
    </div>
  );
}

// ─── small primitives ──────────────────────────────────────────────

function PTZBtn({ label, onClick, busy }: { label: string; onClick: () => void; busy: boolean }) {
  return (
    <button
      className="bg-bg-elev border border-border rounded h-10 hover:border-accent disabled:opacity-50"
      onClick={onClick}
      disabled={busy}
    >
      {label}
    </button>
  );
}

function Field({
  label, value, onChange, placeholder, type = "text",
}: { label: string; value: string; onChange: (s: string) => void; placeholder?: string; type?: string }) {
  return (
    <label className="block">
      <div className="text-xs text-text-dim mb-1">{label}</div>
      <input
        className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm"
        value={value}
        type={type}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  );
}

function Modal({
  title, onClose, children, wide,
}: { title: string; onClose: () => void; children: React.ReactNode; wide?: boolean }) {
  return (
    <div
      className="fixed inset-0 bg-black/60 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg p-4 max-h-[90vh] overflow-auto"
        style={{ width: wide ? "min(960px, calc(100vw - 32px))" : "min(420px, calc(100vw - 32px))" }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center mb-3">
          <h3 className="text-base font-medium flex-1">{title}</h3>
          <button className="text-text-dim hover:text-text" onClick={onClose}>×</button>
        </div>
        {children}
      </div>
    </div>
  );
}
