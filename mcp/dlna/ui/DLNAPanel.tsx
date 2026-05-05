// DLNAPanel — single-screen status & configuration view.
//
// Layout:
//   ┌─────────────────────────────────────────────────────────────┐
//   │  ● broadcasting as "Apteva (homeserver)"                    │
//   │  UUID a4f9…  ·  192.168.1.20:8200  ·  3 clients in last 24h │
//   ├─────────────────────────────────────────────────────────────┤
//   │  Published folders                       [+ add ▾]          │
//   │  /movies/kids                "Kids' movies"          [✕]    │
//   ├─────────────────────────────────────────────────────────────┤
//   │  Recent clients                                             │
//   │  192.168.1.42  PlayStation/9.00     9:42pm  3 hits          │
//   └─────────────────────────────────────────────────────────────┘
//
// DLNA isn't conversational, so the panel is the primary surface;
// the agent only ever calls publish/unpublish + status. The "+ add"
// dropdown reads the storage app's folder list so users pick from a
// real tree rather than typing paths.

import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/dlna";
const STORAGE_API = "/api/apps/storage";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Status {
  friendly_name: string;
  uuid: string;
  lan_ip: string;
  http_port: number;
  broadcasting: boolean;
  published_folders: number;
  recent_clients: number;
  storage_reachable: boolean;
  media_reachable: boolean;
}

interface PublishedFolder {
  id: number;
  folder: string;
  label: string;
  created_at: string;
}

interface ClientEntry {
  ip: string;
  user_agent: string;
  last_object_id: string;
  last_action_at: string;
  browse_count: number;
}

export default function DLNAPanel({}: NativePanelProps) {
  const [status, setStatus] = useState<Status | null>(null);
  const [folders, setFolders] = useState<PublishedFolder[]>([]);
  const [clients, setClients] = useState<ClientEntry[]>([]);
  const [showAdd, setShowAdd] = useState(false);
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const [s, f, c] = await Promise.all([
        fetch(`${API}/status`).then((r) => r.json()),
        fetch(`${API}/published_folders`).then((r) => r.json()),
        fetch(`${API}/clients?limit=20`).then((r) => r.json()),
      ]);
      setStatus(s);
      setFolders(f);
      setClients(c);
      setError("");
    } catch (e: any) {
      setError(e.message || String(e));
    }
  }, []);

  useEffect(() => {
    refresh();
    const t = setInterval(refresh, 15_000);
    return () => clearInterval(t);
  }, [refresh]);

  const removeFolder = async (id: number) => {
    await fetch(`${API}/published_folders/${id}`, { method: "DELETE" });
    refresh();
  };

  return (
    <div className="flex flex-col h-full">
      <StatusCard status={status} error={error} />

      <Section
        title="Published folders"
        action={
          <button
            className="bg-accent text-bg px-3 py-1 rounded text-sm"
            onClick={() => setShowAdd(true)}
          >
            + Add folder
          </button>
        }
      >
        {folders.length === 0 ? (
          <Empty hint="Nothing is exposed to the LAN yet — add at least one folder." />
        ) : (
          folders.map((f) => (
            <Row key={f.id}>
              <span className="font-mono text-sm">{f.folder}</span>
              <span className="text-text-dim text-sm flex-1 truncate ml-3">
                {f.label || <em className="opacity-60">(no label)</em>}
              </span>
              <button
                className="text-text-dim hover:text-error text-sm"
                onClick={() => removeFolder(f.id)}
                title="Unpublish"
              >
                ✕
              </button>
            </Row>
          ))
        )}
      </Section>

      <Section title={`Recent clients (last 24h, ${status?.recent_clients ?? 0})`}>
        {clients.length === 0 ? (
          <Empty hint="No client has browsed yet. Once a TV / VLC discovers the server, it'll show up here." />
        ) : (
          clients.map((c) => (
            <Row key={`${c.ip}|${c.user_agent}`}>
              <span className="font-mono text-sm w-36 truncate">{c.ip}</span>
              <span className="text-sm flex-1 truncate ml-3" title={c.user_agent}>
                {c.user_agent || <em className="opacity-60">(no UA)</em>}
              </span>
              <span className="text-text-dim text-xs w-32 text-right">
                {timeAgo(c.last_action_at)}
              </span>
              <span className="text-text-dim text-xs w-16 text-right">
                {c.browse_count} hits
              </span>
            </Row>
          ))
        )}
      </Section>

      {showAdd && (
        <AddFolderModal
          onClose={() => setShowAdd(false)}
          onAdded={() => { setShowAdd(false); refresh(); }}
        />
      )}
    </div>
  );
}

// ─── status card ────────────────────────────────────────────────────

function StatusCard({ status, error }: { status: Status | null; error: string }) {
  if (error) {
    return (
      <div className="px-4 py-3 border-b border-border bg-error/10 text-sm">
        {error}
      </div>
    );
  }
  if (!status) {
    return <div className="px-4 py-3 border-b border-border text-sm text-text-dim">loading…</div>;
  }
  return (
    <div className="px-4 py-3 border-b border-border">
      <div className="flex items-center gap-2 text-sm">
        <span className={`w-2 h-2 rounded-full ${status.broadcasting ? "bg-success" : "bg-error"}`} />
        <span>
          {status.broadcasting
            ? <>broadcasting as <strong>"{status.friendly_name}"</strong></>
            : <span className="text-error">not broadcasting — multicast unavailable?</span>}
        </span>
      </div>
      <div className="text-xs text-text-dim mt-1">
        UUID <span className="font-mono">{status.uuid.slice(0, 8)}…</span>
        {" · "}
        {status.lan_ip}:{status.http_port}
        {" · "}
        storage <Dot ok={status.storage_reachable} />
        {" · "}
        media <Dot ok={status.media_reachable} />
      </div>
    </div>
  );
}

function Dot({ ok }: { ok: boolean }) {
  return <span className={`inline-block w-1.5 h-1.5 rounded-full align-middle ${ok ? "bg-success" : "bg-text-dim"}`} />;
}

// ─── add folder modal ──────────────────────────────────────────────

function AddFolderModal({
  onClose, onAdded,
}: { onClose: () => void; onAdded: () => void }) {
  const [folders, setFolders] = useState<{ name: string; path: string }[]>([]);
  const [picked, setPicked] = useState("");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  // Walk storage's top-level folders. Two clicks deep is enough for
  // the modal — anything more demanding belongs in the storage panel.
  // Storage returns `{folders: ["a","b"], parent: "/"}` — an envelope
  // with the names as a plain string array, NOT a list of objects.
  // (Earlier versions of this panel called rows.map(...) directly on
  // the envelope and crashed with "H.map is not a function".)
  useEffect(() => {
    fetch(`${STORAGE_API}/folders?parent=/`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.statusText)))
      .then((env: { folders?: string[] }) => {
        const names = Array.isArray(env?.folders) ? env.folders : [];
        setFolders(names.map((n) => ({ name: n, path: "/" + n })));
      })
      .catch((e) => setErr(`storage: ${e}`));
  }, []);

  const submit = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await fetch(`${API}/published_folders`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ folder: picked, label }),
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
    <Modal title="Publish a folder" onClose={onClose}>
      <div className="space-y-3">
        <label className="block">
          <div className="text-xs text-text-dim mb-1">Storage folder</div>
          <select
            className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm"
            value={picked}
            onChange={(e) => setPicked(e.target.value)}
          >
            <option value="">— pick one —</option>
            {folders.map((f) => (
              <option key={f.path} value={f.path}>{f.path}</option>
            ))}
          </select>
          <div className="text-xs text-text-dim mt-1">
            Or type a custom path:{" "}
            <input
              className="ml-1 bg-bg-elev border border-border rounded px-2 py-0.5 text-xs font-mono"
              placeholder="/movies/kids"
              value={picked}
              onChange={(e) => setPicked(e.target.value)}
            />
          </div>
        </label>
        <label className="block">
          <div className="text-xs text-text-dim mb-1">Display label (optional)</div>
          <input
            className="w-full bg-bg-elev border border-border rounded px-2 py-1 text-sm"
            value={label}
            placeholder="Kids' movies"
            onChange={(e) => setLabel(e.target.value)}
          />
        </label>
        <p className="text-xs text-text-dim">
          DLNA has no auth — anything in this folder is browseable by every
          device on your LAN. Don't publish folders containing personal
          documents.
        </p>
        {err && <div className="text-sm text-error">{err}</div>}
        <div className="flex gap-2 justify-end pt-2">
          <button className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
          <button
            className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50"
            onClick={submit}
            disabled={busy || !picked}
          >
            {busy ? "Publishing…" : "Publish"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// ─── primitives ────────────────────────────────────────────────────

function Section({
  title, action, children,
}: { title: string; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="border-b border-border">
      <div className="flex items-center px-4 py-2">
        <h3 className="text-sm font-medium flex-1">{title}</h3>
        {action}
      </div>
      <div className="px-4 pb-3">
        <div className="border border-border rounded">{children}</div>
      </div>
    </div>
  );
}

function Row({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 px-3 py-2 border-b border-border last:border-b-0">
      {children}
    </div>
  );
}

function Empty({ hint }: { hint: string }) {
  return <div className="text-xs text-text-dim italic px-3 py-3">{hint}</div>;
}

function Modal({
  title, onClose, children,
}: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50" onClick={onClose}>
      <div
        className="bg-bg border border-border rounded-lg p-4 w-[480px] max-w-full max-h-[90vh] overflow-auto"
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

function timeAgo(rfc3339: string): string {
  const t = new Date(rfc3339).getTime();
  if (!Number.isFinite(t)) return "—";
  const secs = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}
