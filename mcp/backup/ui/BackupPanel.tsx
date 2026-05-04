// BackupPanel — UI for the backup app.
//
// Layout: three sections stacked vertically.
//   1. Status   — last run summary + "Run now" button
//   2. Policies — table of cron-driven schedules with destination
//   3. Destinations — local + S3 destinations the user can target
//   4. History  — recent runs (status, size, destination, restore)
//
// Talks to /api/apps/backup/* through the platform proxy. Same
// useAppEvents hook pattern as CrmPanel for live updates.

import { useCallback, useEffect, useRef, useState } from "react";

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

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Destination {
  id: number;
  name: string;
  kind: "local" | "s3" | "storage_app";
  config: Record<string, unknown>;
  connection_id?: number;
  enabled: boolean;
  created_at?: string;
}

interface Policy {
  id: number;
  name: string;
  schedule: string;
  destination_id: number;
  retention_keep: number;
  enabled: boolean;
  jobs_id?: string;
  created_at?: string;
  updated_at?: string;
}

interface Run {
  id: number;
  policy_id?: number;
  destination_id: number;
  destination_name: string;
  started_at: string;
  finished_at?: string;
  status: "running" | "success" | "failed";
  bytes_compressed: number;
  sha256?: string;
  remote_key?: string;
  error?: string;
}

const API = "/api/apps/backup";

function formatBytes(n: number): string {
  if (!n) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatTime(s: string | undefined): string {
  if (!s) return "—";
  try { return new Date(s).toLocaleString(); } catch { return s; }
}

function durationOf(r: Run): string {
  if (!r.finished_at) return r.status === "running" ? "running…" : "—";
  try {
    const start = new Date(r.started_at).getTime();
    const end = new Date(r.finished_at).getTime();
    const ms = end - start;
    if (ms < 1000) return `${ms} ms`;
    if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
    return `${Math.round(ms / 1000)} s`;
  } catch { return "—"; }
}

export default function BackupPanel({ projectId, installId }: NativePanelProps) {
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState<string | null>(null);

  const withParams = useCallback((extra: Record<string, string> = {}) => {
    const u = new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra });
    return u.toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
    const res = await fetch(`${API}${path}?${withParams()}`, {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [withParams]);

  const reload = useCallback(async () => {
    try {
      const [d, p, r] = await Promise.all([
        api<{ destinations: Destination[] }>("GET", "/destinations"),
        api<{ policies: Policy[] }>("GET", "/policies"),
        api<{ runs: Run[] }>("GET", "/runs"),
      ]);
      setDestinations(d.destinations || []);
      setPolicies(p.policies || []);
      setRuns(r.runs || []);
      setStatus("");
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    }
  }, [api]);

  useEffect(() => { reload(); }, [reload]);
  useAppEvents("backup", projectId, () => reload());

  const lastSuccess = runs.find(r => r.status === "success");
  const lastRun = runs[0];

  // ─── actions ────────────────────────────────────────────────────

  const runNow = async (destID: number) => {
    setBusy(`run-${destID}`);
    setStatus("");
    try {
      await api("POST", "/run", { destination_id: destID });
      await reload();
    } catch (e) {
      setStatus("Run failed: " + (e as Error).message);
    } finally { setBusy(null); }
  };

  const restoreRun = async (runID: number, destName: string) => {
    if (!window.confirm(
      `Restore from run #${runID} on "${destName}"?\n\n` +
      `App databases will be replaced live. The platform DB will be ` +
      `staged and applied on the next server restart.\n\n` +
      `This is a destructive operation.`
    )) return;
    setBusy(`restore-${runID}`);
    try {
      const out = await api<{ report: { restart_required?: boolean } }>("POST", "/restore", { run_id: runID });
      const restart = out?.report?.restart_required;
      window.alert(restart
        ? "Restore complete. Restart apteva-server to activate the platform DB swap."
        : "Restore complete.");
      await reload();
    } catch (e) {
      setStatus("Restore failed: " + (e as Error).message);
    } finally { setBusy(null); }
  };

  const deleteDestination = async (id: number) => {
    if (!window.confirm("Delete this destination? Past runs in history will keep working.")) return;
    try { await api("DELETE", `/destinations/${id}`); await reload(); }
    catch (e) { setStatus("Delete failed: " + (e as Error).message); }
  };

  const deletePolicy = async (id: number) => {
    if (!window.confirm("Delete this policy? Existing backups remain.")) return;
    try { await api("DELETE", `/policies/${id}`); await reload(); }
    catch (e) { setStatus("Delete failed: " + (e as Error).message); }
  };

  // ─── render ─────────────────────────────────────────────────────

  return (
    <div className="backup-panel">
      <header style={{ marginBottom: 24 }}>
        <h2>Backup</h2>
        {status && <div style={{ color: "#b00", marginTop: 8 }}>{status}</div>}
      </header>

      {/* Status card */}
      <section style={{ marginBottom: 24, padding: 16, background: "#f6f8fa", borderRadius: 8 }}>
        <h3 style={{ marginTop: 0 }}>Status</h3>
        {lastSuccess ? (
          <div>
            Last successful backup:{" "}
            <strong>{formatTime(lastSuccess.finished_at)}</strong>{" "}
            to <strong>{lastSuccess.destination_name}</strong>{" "}
            ({formatBytes(lastSuccess.bytes_compressed)})
          </div>
        ) : (
          <div>No successful backups yet.</div>
        )}
        {lastRun && lastRun.status !== "success" && (
          <div style={{ color: "#b00", marginTop: 4 }}>
            Last run: {lastRun.status}
            {lastRun.error ? ` — ${lastRun.error}` : ""}
          </div>
        )}
      </section>

      {/* Destinations */}
      <Section title="Destinations">
        {destinations.length === 0 && <Empty>No destinations yet — add one below.</Empty>}
        {destinations.map(d => (
          <Row key={d.id}>
            <div>
              <strong>{d.name}</strong> <Pill>{d.kind}</Pill>
              <div style={{ fontSize: 12, color: "#666", marginTop: 2 }}>
                {d.kind === "local" && (d.config.path as string)}
                {d.kind === "s3" && `s3://${d.config.bucket}${d.config.key_prefix ? "/" + d.config.key_prefix : ""}`}
              </div>
            </div>
            <div>
              <button disabled={busy === `run-${d.id}`} onClick={() => runNow(d.id)}>
                {busy === `run-${d.id}` ? "Running…" : "Run now"}
              </button>{" "}
              <button onClick={() => deleteDestination(d.id)}>Delete</button>
            </div>
          </Row>
        ))}
        <DestinationForm onCreated={reload} api={api} />
      </Section>

      {/* Policies */}
      <Section title="Policies">
        {policies.length === 0 && <Empty>No scheduled policies — add one to back up automatically.</Empty>}
        {policies.map(p => (
          <Row key={p.id}>
            <div>
              <strong>{p.name || `policy ${p.id}`}</strong>{" "}
              <code style={{ fontSize: 12 }}>{p.schedule}</code>
              <div style={{ fontSize: 12, color: "#666", marginTop: 2 }}>
                → {destinations.find(d => d.id === p.destination_id)?.name || `destination ${p.destination_id}`}
                {" · keep last "}{p.retention_keep}
                {p.jobs_id ? ` · jobs#${p.jobs_id}` : " · not scheduled"}
              </div>
            </div>
            <button onClick={() => deletePolicy(p.id)}>Delete</button>
          </Row>
        ))}
        <PolicyForm destinations={destinations} onCreated={reload} api={api} />
      </Section>

      {/* History */}
      <Section title="History">
        {runs.length === 0 && <Empty>No backup runs yet.</Empty>}
        {runs.map(r => (
          <Row key={r.id}>
            <div>
              <StatusDot status={r.status} />{" "}
              {formatTime(r.started_at)} → <strong>{r.destination_name}</strong>
              {" · "}{formatBytes(r.bytes_compressed)}
              {" · "}{durationOf(r)}
              {r.error && (
                <div style={{ fontSize: 12, color: "#b00", marginTop: 2 }}>{r.error}</div>
              )}
            </div>
            {r.status === "success" && r.remote_key && (
              <button
                disabled={busy === `restore-${r.id}`}
                onClick={() => restoreRun(r.id, r.destination_name)}
              >
                {busy === `restore-${r.id}` ? "Restoring…" : "Restore"}
              </button>
            )}
          </Row>
        ))}
      </Section>
    </div>
  );
}

// ─── tiny presentational helpers ──────────────────────────────────

function Section(p: { title: string; children: React.ReactNode }) {
  return (
    <section style={{ marginBottom: 24 }}>
      <h3>{p.title}</h3>
      {p.children}
    </section>
  );
}

function Row(p: { children: React.ReactNode }) {
  return (
    <div style={{
      display: "flex", justifyContent: "space-between", alignItems: "center",
      padding: "8px 0", borderBottom: "1px solid #eee",
    }}>{p.children}</div>
  );
}

function Empty(p: { children: React.ReactNode }) {
  return <div style={{ color: "#888", padding: "8px 0", fontStyle: "italic" }}>{p.children}</div>;
}

function Pill(p: { children: React.ReactNode }) {
  return <span style={{ fontSize: 11, padding: "2px 6px", background: "#e6e8eb", borderRadius: 4, marginLeft: 6 }}>{p.children}</span>;
}

function StatusDot({ status }: { status: Run["status"] }) {
  const color = status === "success" ? "#0a0" : status === "failed" ? "#b00" : "#aa0";
  return <span style={{ display: "inline-block", width: 8, height: 8, borderRadius: 4, background: color, marginRight: 6 }} />;
}

// ─── forms ────────────────────────────────────────────────────────

function DestinationForm({
  onCreated, api,
}: {
  onCreated: () => void;
  api: <T>(method: string, path: string, body?: unknown) => Promise<T>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"local" | "s3">("local");
  const [path, setPath] = useState("/var/apteva/backups");
  const [bucket, setBucket] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [region, setRegion] = useState("us-east-1");
  const [keyPrefix, setKeyPrefix] = useState("");
  const [connID, setConnID] = useState("");
  const [err, setErr] = useState("");

  if (!open) return <button style={{ marginTop: 8 }} onClick={() => setOpen(true)}>+ Add destination</button>;

  const submit = async () => {
    setErr("");
    const config = kind === "local"
      ? { path }
      : { bucket, endpoint, region, key_prefix: keyPrefix };
    try {
      await api("POST", "/destinations", {
        name, kind, config,
        connection_id: kind === "s3" ? Number(connID || 0) : undefined,
        enabled: true,
      });
      setOpen(false); setName(""); setBucket(""); setKeyPrefix(""); setConnID("");
      onCreated();
    } catch (e) { setErr((e as Error).message); }
  };

  return (
    <div style={{ marginTop: 12, padding: 12, background: "#f6f8fa", borderRadius: 8 }}>
      <div><strong>New destination</strong></div>
      <div style={{ marginTop: 8, display: "grid", gridTemplateColumns: "120px 1fr", gap: 8 }}>
        <label>Name</label>
        <input value={name} onChange={e => setName(e.target.value)} placeholder="nightly-local" />

        <label>Kind</label>
        <select value={kind} onChange={e => setKind(e.target.value as "local" | "s3")}>
          <option value="local">local — host directory</option>
          <option value="s3">s3 — AWS / R2 / B2 / MinIO</option>
        </select>

        {kind === "local" && <>
          <label>Path</label>
          <input value={path} onChange={e => setPath(e.target.value)} placeholder="/var/apteva/backups" />
        </>}

        {kind === "s3" && <>
          <label>Bucket</label>
          <input value={bucket} onChange={e => setBucket(e.target.value)} placeholder="apteva-backups" />
          <label>Endpoint</label>
          <input value={endpoint} onChange={e => setEndpoint(e.target.value)} placeholder="empty for AWS, otherwise e.g. <accountid>.r2.cloudflarestorage.com" />
          <label>Region</label>
          <input value={region} onChange={e => setRegion(e.target.value)} />
          <label>Key prefix</label>
          <input value={keyPrefix} onChange={e => setKeyPrefix(e.target.value)} placeholder="prod/" />
          <label>Connection ID</label>
          <input value={connID} onChange={e => setConnID(e.target.value)} placeholder="from /connections" />
        </>}
      </div>
      {err && <div style={{ color: "#b00", marginTop: 8 }}>{err}</div>}
      <div style={{ marginTop: 12 }}>
        <button onClick={submit} disabled={!name}>Create</button>{" "}
        <button onClick={() => setOpen(false)}>Cancel</button>
      </div>
    </div>
  );
}

function PolicyForm({
  destinations, onCreated, api,
}: {
  destinations: Destination[];
  onCreated: () => void;
  api: <T>(method: string, path: string, body?: unknown) => Promise<T>;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("nightly");
  const [schedule, setSchedule] = useState("0 3 * * *");
  const [destID, setDestID] = useState<number | "">(destinations[0]?.id ?? "");
  const [keep, setKeep] = useState("14");
  const [err, setErr] = useState("");
  const [warning, setWarning] = useState("");

  if (!open) return (
    <button
      style={{ marginTop: 8 }}
      onClick={() => setOpen(true)}
      disabled={destinations.length === 0}
      title={destinations.length === 0 ? "Add a destination first" : ""}
    >+ Add policy</button>
  );

  const submit = async () => {
    setErr(""); setWarning("");
    if (!destID) { setErr("Pick a destination"); return; }
    try {
      const out = await api<{ jobs_warning?: string }>("POST", "/policies", {
        name, schedule, destination_id: destID, retention_keep: Number(keep),
      });
      if (out?.jobs_warning) {
        setWarning("Saved, but cron registration failed: " + out.jobs_warning);
        return;
      }
      setOpen(false);
      onCreated();
    } catch (e) { setErr((e as Error).message); }
  };

  return (
    <div style={{ marginTop: 12, padding: 12, background: "#f6f8fa", borderRadius: 8 }}>
      <div><strong>New policy</strong></div>
      <div style={{ marginTop: 8, display: "grid", gridTemplateColumns: "120px 1fr", gap: 8 }}>
        <label>Name</label>
        <input value={name} onChange={e => setName(e.target.value)} />
        <label>Schedule (cron)</label>
        <input value={schedule} onChange={e => setSchedule(e.target.value)} placeholder="0 3 * * *" />
        <label>Destination</label>
        <select value={destID} onChange={e => setDestID(Number(e.target.value))}>
          {destinations.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}
        </select>
        <label>Retention (last N)</label>
        <input value={keep} onChange={e => setKeep(e.target.value)} />
      </div>
      {err && <div style={{ color: "#b00", marginTop: 8 }}>{err}</div>}
      {warning && <div style={{ color: "#aa0", marginTop: 8 }}>{warning}</div>}
      <div style={{ marginTop: 12 }}>
        <button onClick={submit}>Create</button>{" "}
        <button onClick={() => setOpen(false)}>Cancel</button>
      </div>
    </div>
  );
}
