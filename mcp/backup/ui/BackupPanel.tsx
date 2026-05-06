// BackupPanel — UI for the backup app.
//
// Layout: four sections stacked vertically.
//   1. Status      — last successful run + last failed run (if any)
//   2. Destinations — local + S3 destinations the user can target
//   3. Policies    — cron-driven schedules with destination
//   4. History     — recent runs (status, size, destination, restore)
//
// Talks to /api/apps/backup/* through the platform proxy. Uses the
// dashboard's Tailwind theme tokens (bg-bg-card / text-text-muted /
// border-border / etc.) so the panel recolors across light/dark/cooler-
// dark themes — same idioms as live-link / storage.
//
// Hand-authored as .tsx so the dashboard's Tailwind v4 build picks up
// the class names. The bundled .mjs is produced by
// `bun run scripts/build-panels.ts` from the apps repo root.

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

function statusColor(s: Run["status"]): string {
  if (s === "success") return "bg-success";
  if (s === "failed")  return "bg-error";
  return "bg-warn"; // running
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
    <div className="h-full flex flex-col p-6 gap-4 min-w-0 overflow-y-auto">
      <header>
        <h2 className="text-text text-base font-bold">Backup</h2>
        <p className="text-text-muted text-xs mt-1">
          Periodic snapshots of your Apteva instance shipped to local disk
          or any S3-compatible bucket.
        </p>
      </header>

      {status && (
        <div className="text-error text-xs border border-error/40 bg-error/10 rounded px-3 py-2">
          {status}
        </div>
      )}

      {/* Status card */}
      <section className="border border-border rounded-lg p-4 bg-bg-card space-y-2">
        <h3 className="text-text text-sm font-bold">Status</h3>
        {lastSuccess ? (
          <div className="text-text-muted text-sm">
            Last successful backup:{" "}
            <span className="text-text font-bold">{formatTime(lastSuccess.finished_at)}</span>{" "}
            to <span className="text-text font-bold">{lastSuccess.destination_name}</span>{" "}
            ({formatBytes(lastSuccess.bytes_compressed)})
          </div>
        ) : (
          <div className="text-text-muted text-sm italic">No successful backups yet.</div>
        )}
        {lastRun && lastRun.status !== "success" && (
          <div className="text-error text-xs">
            Last run: {lastRun.status}
            {lastRun.error ? ` — ${lastRun.error}` : ""}
          </div>
        )}
      </section>

      {/* Destinations */}
      <section className="border border-border rounded-lg p-4 bg-bg-card space-y-2">
        <h3 className="text-text text-sm font-bold">Destinations</h3>
        {destinations.length === 0 && (
          <div className="text-text-muted text-sm italic">No destinations yet — add one below.</div>
        )}
        {destinations.map(d => (
          <Row key={d.id}>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <strong className="text-text">{d.name}</strong>
                <Pill>{d.kind}</Pill>
              </div>
              <div className="text-text-muted text-xs mt-0.5 font-mono truncate">
                {d.kind === "local" && (d.config.path as string)}
                {d.kind === "s3" && `s3://${d.config.bucket}${d.config.key_prefix ? "/" + d.config.key_prefix : ""}`}
              </div>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              <button
                onClick={() => runNow(d.id)}
                disabled={busy === `run-${d.id}`}
                className="px-3 py-1 text-xs bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
              >
                {busy === `run-${d.id}` ? "Running…" : "Run now"}
              </button>
              <button
                onClick={() => deleteDestination(d.id)}
                className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text"
              >
                Delete
              </button>
            </div>
          </Row>
        ))}
        <DestinationForm onCreated={reload} api={api} />
      </section>

      {/* Policies */}
      <section className="border border-border rounded-lg p-4 bg-bg-card space-y-2">
        <h3 className="text-text text-sm font-bold">Policies</h3>
        {policies.length === 0 && (
          <div className="text-text-muted text-sm italic">No scheduled policies — add one to back up automatically.</div>
        )}
        {policies.map(p => (
          <Row key={p.id}>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 flex-wrap">
                <strong className="text-text">{p.name || `policy ${p.id}`}</strong>
                <code className="text-text-muted text-xs font-mono bg-bg-input px-1.5 py-0.5 rounded">{p.schedule}</code>
              </div>
              <div className="text-text-muted text-xs mt-0.5">
                → {destinations.find(d => d.id === p.destination_id)?.name || `destination ${p.destination_id}`}
                {" · keep last "}{p.retention_keep}
                {p.jobs_id ? ` · jobs#${p.jobs_id}` : (
                  <span className="text-warn"> · not scheduled</span>
                )}
              </div>
            </div>
            <button
              onClick={() => deletePolicy(p.id)}
              className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text shrink-0"
            >
              Delete
            </button>
          </Row>
        ))}
        <PolicyForm destinations={destinations} onCreated={reload} api={api} />
      </section>

      {/* History */}
      <section className="border border-border rounded-lg p-4 bg-bg-card space-y-2">
        <h3 className="text-text text-sm font-bold">History</h3>
        {runs.length === 0 && (
          <div className="text-text-muted text-sm italic">No backup runs yet.</div>
        )}
        {runs.map(r => (
          <Row key={r.id}>
            <div className="min-w-0 flex-1 flex items-start gap-2">
              <span className={`inline-block w-2 h-2 rounded-full mt-1.5 shrink-0 ${statusColor(r.status)}`} />
              <div className="min-w-0 flex-1">
                <div className="text-sm text-text-muted">
                  {formatTime(r.started_at)} →{" "}
                  <span className="text-text font-bold">{r.destination_name}</span>
                  {" · "}{formatBytes(r.bytes_compressed)}
                  {" · "}{durationOf(r)}
                </div>
                {r.error && (
                  <div className="text-error text-xs mt-0.5">{r.error}</div>
                )}
              </div>
            </div>
            {r.status === "success" && r.remote_key && (
              <button
                onClick={() => restoreRun(r.id, r.destination_name)}
                disabled={busy === `restore-${r.id}`}
                className="px-2 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-hover hover:text-text disabled:opacity-50 shrink-0"
              >
                {busy === `restore-${r.id}` ? "Restoring…" : "Restore"}
              </button>
            )}
          </Row>
        ))}
      </section>
    </div>
  );
}

// ─── tiny presentational helpers ──────────────────────────────────

function Row({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 py-2 border-b border-border last:border-b-0">
      {children}
    </div>
  );
}

function Pill({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-[10px] uppercase tracking-wide bg-bg-input text-text-muted px-1.5 py-0.5 rounded">
      {children}
    </span>
  );
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

  if (!open) return (
    <button
      onClick={() => setOpen(true)}
      className="text-accent text-xs hover:underline self-start mt-1"
    >
      + Add destination
    </button>
  );

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
    <div className="mt-2 p-3 bg-bg-input border border-border rounded space-y-2">
      <div className="text-text font-bold text-sm">New destination</div>
      <FormGrid>
        <Label>Name</Label>
        <Input value={name} onChange={setName} placeholder="nightly-local" />

        <Label>Kind</Label>
        <Select value={kind} onChange={(v) => setKind(v as "local" | "s3")}>
          <option value="local">local — host directory</option>
          <option value="s3">s3 — AWS / R2 / B2 / MinIO</option>
        </Select>

        {kind === "local" && <>
          <Label>Path</Label>
          <Input value={path} onChange={setPath} placeholder="/var/apteva/backups" />
        </>}

        {kind === "s3" && <>
          <Label>Bucket</Label>
          <Input value={bucket} onChange={setBucket} placeholder="apteva-backups" />
          <Label>Endpoint</Label>
          <Input value={endpoint} onChange={setEndpoint} placeholder="empty for AWS, e.g. <accountid>.r2.cloudflarestorage.com" />
          <Label>Region</Label>
          <Input value={region} onChange={setRegion} />
          <Label>Key prefix</Label>
          <Input value={keyPrefix} onChange={setKeyPrefix} placeholder="prod/" />
          <Label>Connection ID</Label>
          <Input value={connID} onChange={setConnID} placeholder="from /connections" />
        </>}
      </FormGrid>
      {err && <div className="text-error text-xs">{err}</div>}
      <div className="flex justify-end gap-2 pt-1">
        <button
          onClick={() => setOpen(false)}
          className="px-3 py-1.5 text-sm text-text-muted hover:text-text"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          disabled={!name}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover disabled:opacity-50"
        >
          Create
        </button>
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
      onClick={() => setOpen(true)}
      disabled={destinations.length === 0}
      title={destinations.length === 0 ? "Add a destination first" : ""}
      className="text-accent text-xs hover:underline self-start mt-1 disabled:opacity-50 disabled:no-underline"
    >
      + Add policy
    </button>
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
    <div className="mt-2 p-3 bg-bg-input border border-border rounded space-y-2">
      <div className="text-text font-bold text-sm">New policy</div>
      <FormGrid>
        <Label>Name</Label>
        <Input value={name} onChange={setName} />
        <Label>Schedule (cron)</Label>
        <Input value={schedule} onChange={setSchedule} placeholder="0 3 * * *" />
        <Label>Destination</Label>
        <Select value={String(destID)} onChange={(v) => setDestID(Number(v))}>
          {destinations.map(d => <option key={d.id} value={d.id}>{d.name}</option>)}
        </Select>
        <Label>Retention (last N)</Label>
        <Input value={keep} onChange={setKeep} />
      </FormGrid>
      {err && <div className="text-error text-xs">{err}</div>}
      {warning && <div className="text-warn text-xs">{warning}</div>}
      <div className="flex justify-end gap-2 pt-1">
        <button
          onClick={() => setOpen(false)}
          className="px-3 py-1.5 text-sm text-text-muted hover:text-text"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          className="px-3 py-1.5 text-sm bg-accent text-bg rounded font-bold hover:bg-accent-hover"
        >
          Create
        </button>
      </div>
    </div>
  );
}

function FormGrid({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid gap-2 items-center" style={{ gridTemplateColumns: "120px 1fr" }}>
      {children}
    </div>
  );
}

function Label({ children }: { children: React.ReactNode }) {
  return <label className="text-text-muted text-xs">{children}</label>;
}

function Input({
  value, onChange, placeholder,
}: { value: string; onChange: (v: string) => void; placeholder?: string }) {
  return (
    <input
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      className="bg-bg border border-border rounded px-2 py-1.5 text-sm text-text font-mono focus:outline-none focus:border-accent"
    />
  );
}

function Select({
  value, onChange, children,
}: { value: string; onChange: (v: string) => void; children: React.ReactNode }) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="bg-bg border border-border rounded px-2 py-1.5 text-sm text-text"
    >
      {children}
    </select>
  );
}
