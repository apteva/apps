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
}

export default function CamerasPanel({ projectId }: NativePanelProps) {
  const [cameras, setCameras] = useState<Camera[]>([]);
  const [room, setRoom] = useState<string>("");
  const [showAdd, setShowAdd] = useState(false);
  const [selected, setSelected] = useState<Camera | null>(null);
  const [status, setStatus] = useState("");

  const refresh = useCallback(async () => {
    try {
      const r = await fetch(`${API}/cameras`);
      if (!r.ok) throw new Error(await r.text());
      setCameras(await r.json());
    } catch (e: any) {
      setStatus(`load: ${e.message}`);
    }
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 30_000);
    return () => clearInterval(t);
  }, [refresh]);

  // Subscribe to tapo.motion events so the panel flashes the
  // affected tile without waiting for the next 30s poll.
  useEffect(() => {
    if (!projectId) return;
    const es = new EventSource(
      `/api/app-events/tapo?project_id=${encodeURIComponent(projectId)}`,
      { withCredentials: true },
    );
    es.onmessage = (e) => {
      try {
        const env = JSON.parse(e.data) as { topic: string; data: { camera_id: number } };
        if (env.topic === "tapo.motion") flashTile(env.data.camera_id);
      } catch {}
    };
    return () => es.close();
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
           style={{ gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))" }}>
        {filtered.map((c) => (
          <CameraTile
            key={c.id}
            camera={c}
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
          onClose={() => setShowAdd(false)}
          onAdded={() => { setShowAdd(false); refresh(); }}
        />
      )}
      {selected && (
        <CameraDetail
          camera={selected}
          onClose={() => setSelected(null)}
          onChanged={refresh}
        />
      )}
    </div>
  );
}

// ─── tile ───────────────────────────────────────────────────────────

function flashTile(id: number) {
  const el = document.querySelector(`[data-camera-tile="${id}"]`) as HTMLElement | null;
  if (!el) return;
  el.classList.add("ring-2", "ring-accent");
  setTimeout(() => el.classList.remove("ring-2", "ring-accent"), 4000);
}

function CameraTile({ camera, onClick }: { camera: Camera; onClick: () => void }) {
  const [bust, setBust] = useState(0);
  useEffect(() => {
    if (!camera.online) return;
    const t = setInterval(() => setBust((n) => n + 1), SNAP_REFRESH_MS);
    return () => clearInterval(t);
  }, [camera.online]);

  return (
    <div
      data-camera-tile={camera.id}
      onClick={onClick}
      className="bg-bg-elev border border-border rounded-lg overflow-hidden cursor-pointer hover:border-accent transition"
    >
      <div className="aspect-video bg-black flex items-center justify-center text-text-dim">
        {camera.online ? (
          <img
            key={bust}
            src={`${API}/snapshots/${camera.id}.jpg?t=${bust}`}
            alt={camera.name}
            className="w-full h-full object-cover"
            onError={(e) => { (e.target as HTMLImageElement).style.display = "none"; }}
          />
        ) : (
          <span className="text-sm">offline</span>
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
        </div>
      </div>
    </div>
  );
}

// ─── add modal ──────────────────────────────────────────────────────

function AddCameraModal({
  onClose,
  onAdded,
}: { onClose: () => void; onAdded: () => void }) {
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
      const r = await fetch(`${API}/cameras`, {
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
  onClose,
  onChanged,
}: { camera: Camera; onClose: () => void; onChanged: () => void }) {
  const [events, setEvents] = useState<MotionEvent[]>([]);
  const [bust, setBust] = useState(0);
  const [busy, setBusy] = useState(false);
  const caps = camera.capabilities;

  useEffect(() => {
    fetch(`${API}/events?camera_id=${camera.id}&limit=20`)
      .then((r) => r.json())
      .then(setEvents)
      .catch(() => {});
    const t = setInterval(() => setBust((n) => n + 1), 2000);
    return () => clearInterval(t);
  }, [camera.id]);

  const ptz = async (direction: string) => {
    setBusy(true);
    try {
      await fetch(`${API}/cameras/${camera.id}/ptz`, {
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
      <div className="grid grid-cols-2 gap-4">
        <div>
          <div className="aspect-video bg-black rounded overflow-hidden">
            {camera.online ? (
              <img
                key={bust}
                src={`${API}/snapshots/${camera.id}.jpg?t=${bust}`}
                className="w-full h-full object-cover"
                alt={camera.name}
              />
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
          <Toggles camera={camera} onChanged={onChanged} />
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

function Toggles({ camera, onChanged }: { camera: Camera; onChanged: () => void }) {
  const post = async (path: string, body: any) => {
    await fetch(`${API}/cameras/${camera.id}/${path}`, {
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
        className={`bg-bg border border-border rounded-lg p-4 ${wide ? "w-[820px]" : "w-[420px]"} max-w-full max-h-[90vh] overflow-auto`}
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
