// InstancesPanel — install-settings admin view for the instances app.
//
// Shows the host fleet: local (always present, id=0) plus any
// provisioned VPS rows. Per-host card surfaces vitals (CPU / mem /
// disk / load / uptime), status, IP, and destroy / refresh actions.
//
// Apps that consume Instances (Live Link self-vps, Deploy SSHRuntime,
// future Containers / Backup) own their own surfaces — this panel
// just inventories the substrate.

import { useCallback, useEffect, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Instance {
  id: number;
  name: string;
  provider: string;
  provider_id?: string;
  public_ipv4?: string;
  public_ipv6?: string;
  status: string;
  region?: string;
  size?: string;
  image?: string;
  ssh_user?: string;
  ssh_public_key?: string;
  monthly_cost_cents: number;
  error?: string;
  created_at?: string;
  ready_at?: string;
}

interface MetricsWire {
  timestamp: string;
  cpu: { total_pct: number };
  mem: { used_bytes: number; total_bytes: number; available_bytes: number };
  disk: Array<{ mount: string; used_bytes: number; total_bytes: number; used_pct: number }>;
  load: { l1: number; l5: number; l15: number };
  uptime_s: number;
  process_count: number;
}

const API = "/api/apps/instances/api";

function statusColor(s: string): string {
  if (s === "ready") return "text-green";
  if (s === "provisioning" || s === "pending") return "text-blue";
  if (s === "error") return "text-red";
  return "text-text-dim";
}

function formatBytes(n: number): string {
  if (!n) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

function formatUptime(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86400)}d`;
}

export default function InstancesPanel({ projectId, installId }: NativePanelProps) {
  const [instances, setInstances] = useState<Instance[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showCreate, setShowCreate] = useState(false);

  const withParams = useCallback(
    () =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
      }).toString(),
    [projectId, installId],
  );

  const load = useCallback(async () => {
    try {
      const r = await fetch(`${API}/instances?${withParams()}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { instances: Instance[] };
      setInstances(j.instances || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
      setInstances([]);
    }
  }, [withParams]);

  useEffect(() => { load(); }, [load]);

  // Refresh status cards every 10s — provisioning rows can flip
  // ready, ready rows can flip to error, etc.
  useEffect(() => {
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  const destroy = async (id: number, name: string) => {
    if (!confirm(`Destroy instance "${name}"? Upstream resource will be terminated.`)) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/instances/${id}?${withParams()}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      await load();
    } catch (e) {
      setError("Destroy failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full flex flex-col">
      <header className="px-4 py-3 border-b border-border flex items-baseline gap-3">
        <h1 className="text-text font-semibold">Instances</h1>
        <span className="text-xs text-text-muted flex-1">
          Host inventory — local + remote (Hetzner v0.1).
        </span>
        <button
          type="button"
          onClick={() => setShowCreate(true)}
          className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
        >+ Provision</button>
        <button
          type="button"
          onClick={load}
          disabled={busy}
          className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
        >Refresh</button>
      </header>

      {error && <div className="px-4 py-2 text-red text-xs border-b border-border">{error}</div>}

      <main className="flex-1 overflow-auto p-3 space-y-3">
        {instances === null ? (
          <div className="p-6 text-text-muted text-sm">Loading…</div>
        ) : instances.length === 0 ? (
          <div className="p-6 text-text-muted text-sm">No instances. Local should auto-seed at app boot.</div>
        ) : (
          instances.map((inst) => (
            <InstanceCard
              key={inst.id}
              inst={inst}
              withParams={withParams}
              busy={busy}
              onDestroy={() => destroy(inst.id, inst.name)}
            />
          ))
        )}
      </main>

      {showCreate && (
        <CreateDialog
          onClose={() => setShowCreate(false)}
          onCreated={() => { setShowCreate(false); load(); }}
          withParams={withParams}
          setError={setError}
        />
      )}
    </div>
  );
}

function InstanceCard({
  inst, withParams, busy, onDestroy,
}: {
  inst: Instance;
  withParams: () => string;
  busy: boolean;
  onDestroy: () => void;
}) {
  const [metrics, setMetrics] = useState<MetricsWire | null>(null);

  useEffect(() => {
    let cancelled = false;
    const fetchMetrics = () => {
      if (inst.status !== "ready") return;
      fetch(`${API}/instances/${inst.id}/metrics?${withParams()}`, { credentials: "same-origin" })
        .then((r) => r.ok ? r.json() : null)
        .then((j) => { if (!cancelled && j?.metrics) setMetrics(j.metrics); })
        .catch(() => {});
    };
    fetchMetrics();
    const t = setInterval(fetchMetrics, 10000);
    return () => { cancelled = true; clearInterval(t); };
  }, [inst.id, inst.status, withParams]);

  const ip = inst.public_ipv4 || inst.public_ipv6 || "—";
  const isLocal = inst.provider === "local";

  return (
    <div className="border border-border rounded p-3 space-y-2 bg-bg-input/20">
      <div className="flex items-baseline gap-2">
        <span className={statusColor(inst.status) + " text-xs"}>●</span>
        <span className="text-text font-medium">{inst.name}</span>
        <span className="text-text-dim text-xs">
          {inst.provider}
          {inst.size ? ` · ${inst.size}` : ""}
          {inst.region ? ` · ${inst.region}` : ""}
        </span>
        <span className="text-text-dim text-xs font-mono ml-2">{ip}</span>
        <span className="flex-1" />
        <span className={statusColor(inst.status) + " text-xs"}>{inst.status}</span>
        {!isLocal && (
          <button
            type="button"
            onClick={onDestroy}
            disabled={busy}
            className="px-2 py-0.5 text-[11px] border border-red/60 text-red rounded hover:bg-red hover:text-white disabled:opacity-50"
          >Destroy</button>
        )}
      </div>

      {inst.error && (
        <div className="text-red text-xs">{inst.error}</div>
      )}

      {metrics ? (
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs text-text-muted">
          <Stat label="CPU" value={`${metrics.cpu.total_pct.toFixed(1)}%`} />
          <Stat label="MEM" value={`${formatBytes(metrics.mem.used_bytes)} / ${formatBytes(metrics.mem.total_bytes)}`} />
          <Stat label="LOAD" value={`${metrics.load.l1.toFixed(2)} / ${metrics.load.l5.toFixed(2)} / ${metrics.load.l15.toFixed(2)}`} />
          <Stat label="UP" value={formatUptime(metrics.uptime_s)} />
        </div>
      ) : inst.status === "ready" ? (
        <div className="text-text-dim text-[11px]">Loading vitals…</div>
      ) : null}

      {metrics && metrics.disk?.length > 0 && (
        <div className="text-[11px] text-text-dim font-mono space-y-0.5">
          {metrics.disk.slice(0, 3).map((d) => (
            <div key={d.mount}>
              {d.mount.padEnd(20)} {formatBytes(d.used_bytes)} / {formatBytes(d.total_bytes)} ({d.used_pct.toFixed(0)}%)
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-text-dim uppercase text-[10px]">{label}</div>
      <div className="text-text font-mono text-xs">{value}</div>
    </div>
  );
}

function CreateDialog({
  onClose, onCreated, withParams, setError,
}: {
  onClose: () => void;
  onCreated: () => void;
  withParams: () => string;
  setError: (s: string) => void;
}) {
  const [name, setName] = useState("");
  const [size, setSize] = useState("cx22");
  const [region, setRegion] = useState("fsn1");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/instances?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), provider: "hetzner", size, region }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      onCreated();
    } catch (e) {
      setError("Provision failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        className="w-[420px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">Provision a new instance</h2>
        <p className="text-xs text-text-muted">
          v0.1 supports Hetzner Cloud. Bind a Hetzner connection on this install before provisioning.
        </p>
        <div>
          <label className="text-xs text-text-muted block mb-1">Name</label>
          <input
            autoFocus
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="tunnel-1"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div>
            <label className="text-xs text-text-muted block mb-1">Size</label>
            <select
              value={size}
              onChange={(e) => setSize(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="cx22">cx22 (€3.79/mo, 2 vCPU shared, 4 GB)</option>
              <option value="cx32">cx32 (€6.49/mo, 4 vCPU shared, 8 GB)</option>
              <option value="ccx13">ccx13 (€12.49/mo, 2 vCPU dedicated, 8 GB)</option>
            </select>
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Region</label>
            <select
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="fsn1">Falkenstein (fsn1)</option>
              <option value="nbg1">Nuremberg (nbg1)</option>
              <option value="hel1">Helsinki (hel1)</option>
              <option value="ash">Ashburn US (ash)</option>
              <option value="hil">Hillsboro US (hil)</option>
            </select>
          </div>
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy || !name.trim()}
            className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
          >{busy ? "Provisioning…" : "Provision"}</button>
        </div>
      </form>
    </div>
  );
}
