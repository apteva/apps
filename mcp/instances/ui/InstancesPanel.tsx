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
  ssh_host?: string;
  ssh_port?: number;
  status: string;
  region?: string;
  size?: string;
  image?: string;
  platform?: string;
  resource_class?: string;
  deletable_at?: string;
  ssh_user?: string;
  ssh_public_key?: string;
  resources_json?: string;
  storage_json?: string;
  ports_json?: string;
  monthly_cost_cents: number;
  error?: string;
	lifecycle_stage?: string;
	primary_error?: string;
	cleanup_error?: string;
	provider_checked_at?: string;
  created_at?: string;
  ready_at?: string;
  capabilities?: {
    run: boolean;
    upload: boolean;
    download: boolean;
    metrics: boolean;
    tunnel: boolean;
    destroy: boolean;
    upgrade: boolean;
  };
}

interface InstanceVolumeWire {
  id: number;
  instance_id?: number;
  provider: string;
  provider_volume_id: string;
  name: string;
  role: "boot" | "data";
  storage_class: "local" | "block" | "network" | "ephemeral";
  tier: string;
  provider_type?: string;
  size_gb: number;
  region?: string;
  status: string;
  filesystem?: string;
  mount_path?: string;
  device_path?: string;
  guest_ready?: boolean;
  delete_policy: "retain" | "with_instance";
  error?: string;
}

interface ObjectStorageWire {
  id: number;
  name: string;
  provider: string;
  provider_connection_id: number;
  provider_id: string;
  status: string;
  region?: string;
  plan?: string;
  endpoint?: string;
  bucket?: string;
  access_key_id?: string;
  error?: string;
  created_at?: string;
}

interface ObjectStorageCredentialsWire {
  endpoint: string;
  region?: string;
  bucket?: string;
  access_key_id: string;
  secret_access_key: string;
  expires_at?: string;
  shown_once: boolean;
}

interface ObjectStorageProviderWire {
  provider: string;
  connection_id: number;
  default: boolean;
}

interface StorageCapabilitiesWire {
  provider: string;
  boot_size_configurable: boolean;
  data_volumes: boolean;
  dynamic_attach: boolean;
  resize: boolean;
  guest_prepare?: boolean;
  guest_filesystems?: string[];
  notes?: string;
  storage_classes?: Array<"local" | "block" | "network" | "ephemeral">;
  tiers: Array<{ name: string; storage_class?: string; provider_type?: string; description?: string }>;
}

interface MetricsWire {
  timestamp: string;
  cpu: { total_pct: number; cores?: number };
  mem: { used_bytes: number; total_bytes: number; available_bytes: number };
  disk: Array<{ mount: string; used_bytes: number; total_bytes: number; used_pct: number }>;
  load: { l1: number; l5: number; l15: number };
  uptime_s: number;
  process_count: number;
}

const API = "/api/apps/instances/api";

function statusColor(s: string): string {
  if (s === "ready") return "text-green";
  if (s === "provisioning" || s === "pending" || s === "upgrading" || s === "destroying") return "text-blue";
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

// Subtle border + divider colors used throughout the card. The
// dashboard's `--border` token reads as too bright in dark mode, so
// we lean on rgba inline-style borders that sit at ~5% opacity over
// the background. Same reason we colorize bars with explicit hex
// rather than `bg-…/N` opacity classes.
const SUBTLE_BORDER = "rgba(255,255,255,0.05)";
const FAINT_DIVIDER = "rgba(255,255,255,0.04)";
const SUB_CARD_BG = "rgba(255,255,255,0.015)";
const HEADER_STRIP_BG = "rgba(255,255,255,0.025)";

// Color ramps for utilization. Returns a CSS color literal so callers
// can stick it into inline style (Tailwind arbitrary classes like
// bg-[#…] don't ship to the panel CSS bundle — feedback_no_arbitrary_tailwind_in_panels).
function pctColor(pct: number): string {
  if (pct >= 90) return "#dc2626"; // red-600
  if (pct >= 75) return "#f59e0b"; // amber-500
  if (pct >= 50) return "#eab308"; // yellow-500
  return "#16a34a";                 // green-600
}

function loadColor(l1: number, cores?: number): string {
  if (cores && cores > 0) {
    const ratio = l1 / cores;
    if (ratio >= 1) return "#dc2626";
    if (ratio >= 0.7) return "#f59e0b";
    return "#16a34a";
  }
  if (l1 >= 4) return "#dc2626";
  if (l1 >= 2) return "#f59e0b";
  return "#16a34a";
}

function formatCPUDetail(cpu: MetricsWire["cpu"]): string {
  const pct = `${cpu.total_pct.toFixed(1)}%`;
  return cpu.cores && cpu.cores > 0 ? `${pct} · ${cpu.cores} vCPU` : pct;
}

function providerCurrencySymbol(provider?: string): string {
  return ["digitalocean", "runpod", "vultr", "aws-ec2", "huawei-cloud", "linode", "ovhcloud"].includes(provider || "") ? "$" : "€";
}

function formatProviderPrice(cents: number, provider?: string): string {
  if (!cents) return "";
  return `${providerCurrencySymbol(provider)}${(cents / 100).toFixed(2)}`;
}

function formatRemoteTotal(instances: Instance[]): string {
  const priced = instances.filter((i) => i.provider !== "local" && i.monthly_cost_cents > 0);
  if (priced.length === 0) return "";
  const providers = new Set(priced.map((i) => i.provider));
  const total = priced.reduce((s, i) => s + (i.monthly_cost_cents || 0), 0);
  return providers.size === 1 ? formatProviderPrice(total, priced[0]?.provider) : "";
}

function deletionLockLabel(inst: Instance): string {
  if (!inst.deletable_at || inst.capabilities?.destroy) return "";
  const date = new Date(inst.deletable_at);
  if (!Number.isFinite(date.getTime()) || date.getTime() <= Date.now()) return "";
  return `Delete after ${date.toLocaleString()}`;
}

function resourceSummary(inst: Instance): string {
  if (!inst.resources_json) return "";
  try {
    const parsed = JSON.parse(inst.resources_json);
    const accelerators = Array.isArray(parsed?.accelerators) ? parsed.accelerators : [];
    const gpu = accelerators
      .filter((a: any) => a?.kind === "gpu")
      .map((a: any) => {
        const count = Number(a.count || 1);
        const model = String(a.model || "GPU").replace(/^NVIDIA\s+/i, "");
        return `${count}x ${model}`;
      });
    if (gpu.length) return gpu.join(", ");
    const cores = Number(parsed?.cpu?.cores || 0);
    const mem = Number(parsed?.memory_gb || 0);
    const parts = [];
    if (cores) parts.push(`${cores} vCPU`);
    if (mem) parts.push(`${mem} GB RAM`);
    return parts.join(" · ");
  } catch {
    return "";
  }
}

// ─── Visuals ──────────────────────────────────────────────────────

// ProgressBar — labeled percentage with a colored fill. Width via
// inline style; Tailwind arbitrary widths don't compile in the
// dashboard's CSS bundle.
//
// Default height is 10px (up from the original 6px which read as a
// thin colored line against the dark backgrounds). Disk bars use the
// smaller 6px form for compact stacks.
function ProgressBar({
  pct, label, sublabel, height,
}: {
  pct: number;
  label?: string;
  sublabel?: string;
  height?: number;
}) {
  const clamped = Math.max(0, Math.min(100, pct));
  const h = height ?? 10;
  return (
    <div className="space-y-1">
      {(label || sublabel) && (
        <div className="flex justify-between items-baseline text-[11px]">
          {label && <span className="text-text-dim uppercase tracking-wider font-medium">{label}</span>}
          {sublabel && <span className="text-text font-mono">{sublabel}</span>}
        </div>
      )}
      <div
        className="w-full rounded-full overflow-hidden"
        // Track sits at ~5% white on a dark bg — readable as a track
        // without the bright outlined-rectangle effect we had before.
        style={{ height: `${h}px`, backgroundColor: "rgba(255,255,255,0.06)" }}
        title={`${clamped.toFixed(1)}%`}
      >
        <div
          className="h-full rounded-full transition-all duration-500"
          style={{ width: `${clamped}%`, backgroundColor: pctColor(clamped) }}
        />
      </div>
    </div>
  );
}

// Sparkline — single-series tiny line chart. Plots in equal-width
// steps, auto-scales y to data range. Pure SVG, no library.
function Sparkline({
  values, width, height, color,
}: {
  values: number[];
  width?: number;
  height?: number;
  color?: string;
}) {
  const w = width ?? 80;
  const h = height ?? 20;
  if (values.length < 2) {
    return <svg width={w} height={h} />;
  }
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1;
  const stepX = w / (values.length - 1);
  const path = values
    .map((v, i) => {
      const x = i * stepX;
      const y = h - ((v - min) / range) * h;
      return `${i === 0 ? "M" : "L"} ${x.toFixed(1)} ${y.toFixed(1)}`;
    })
    .join(" ");
  return (
    <svg width={w} height={h} className="block" aria-hidden>
      <path d={path} fill="none" stroke={color ?? "#3b82f6"} strokeWidth="1.5" />
    </svg>
  );
}

// MultiLineChart — dual-line (CPU% + memory%) over the in-memory
// history window. Y axis pinned 0-100 so both series are comparable.
// Includes gridlines at 25/50/75/100 and a tiny legend.
function MultiLineChart({
  cpu, mem, height,
}: {
  cpu: number[];
  mem: number[];
  height?: number;
}) {
  // ViewBox sized to roughly match a typical panel width so
  // preserveAspectRatio="none" stretching is minimal (1.0x–1.3x
  // horizontal). The earlier 800-wide viewBox stretched to 1900px
  // panels showed a 2.4x horizontal squash — every CPU/MEM change
  // got visually compressed and the chart read as a flat line.
  // 1400 lands much closer to common panel widths.
  const VIEW_W = 1400;
  const h = height ?? 160;
  const padLeft = 32;
  const padRight = 12;
  const padTop = 10;
  const padBottom = 26;
  const plotW = VIEW_W - padLeft - padRight;
  const plotH = h - padTop - padBottom;
  const n = Math.max(cpu.length, mem.length);
  if (n < 2) {
    // Slim placeholder — full-height empty box dominated the card
    // when there were 0-1 samples; this reads as "waiting" without
    // wasting vertical space.
    return (
      <div
        className="text-[11px] text-text-dim flex items-center justify-center rounded"
        style={{
          height: 32,
          backgroundColor: "rgba(255,255,255,0.02)",
          color: "rgba(255,255,255,0.35)",
        }}
      >
        Accumulating samples · chart will fill in over the next ticks
      </div>
    );
  }
  const xAt = (i: number, len: number) =>
    padLeft + (len > 1 ? (i / (len - 1)) * plotW : 0);
  const yAt = (v: number) =>
    padTop + (1 - Math.max(0, Math.min(100, v)) / 100) * plotH;
  const lineFor = (vs: number[]) =>
    vs
      .map((v, i) => `${i === 0 ? "M" : "L"} ${xAt(i, vs.length).toFixed(1)} ${yAt(v).toFixed(1)}`)
      .join(" ");
  return (
    <svg
      viewBox={`0 0 ${VIEW_W} ${h}`}
      // "none" → fills the container width completely (operator
      // wanted edge-to-edge). The widened viewBox above keeps the
      // visual aspect close to natural; lines stay readable without
      // the flat-horizon stretch that the original 800-wide viewBox
      // showed at 1900px.
      preserveAspectRatio="none"
      className="block w-full"
      style={{ height: h }}
      aria-label="cpu/memory history"
    >
      {/* gridlines — non-scaling stroke so they stay 1px sharp on
          any panel width (preserveAspectRatio="none" stretches the
          chart horizontally, which would otherwise thicken strokes). */}
      {[0, 25, 50, 75, 100].map((g) => (
        <line
          key={g}
          x1={padLeft} y1={yAt(g)} x2={VIEW_W - padRight} y2={yAt(g)}
          stroke="currentColor"
          strokeOpacity={g === 0 || g === 100 ? "0.15" : "0.06"}
          strokeWidth="1"
          vectorEffect="non-scaling-stroke"
        />
      ))}
      {/* y labels */}
      {[0, 50, 100].map((g) => (
        <text
          key={g}
          x={padLeft - 6} y={yAt(g) + 3}
          textAnchor="end" fontSize="10" fill="currentColor" fillOpacity="0.45"
        >{g}%</text>
      ))}
      <path d={lineFor(cpu)} fill="none" stroke="#3b82f6" strokeWidth="2" vectorEffect="non-scaling-stroke" />
      <path d={lineFor(mem)} fill="none" stroke="#a78bfa" strokeWidth="2" vectorEffect="non-scaling-stroke" />
      {/* Color-key only legend — the "N samples · ~M min" hint moved
          up to the section header so we don't repeat it inside the
          plot area. */}
      <g transform={`translate(${padLeft}, ${h - 8})`} fontSize="10" fill="currentColor" fillOpacity="0.7">
        <rect x="0" y="-7" width="10" height="2" fill="#3b82f6" />
        <text x="16" y="0">CPU</text>
        <rect x="56" y="-7" width="10" height="2" fill="#a78bfa" />
        <text x="72" y="0">Memory</text>
      </g>
    </svg>
  );
}

export default function InstancesPanel({ projectId, installId }: NativePanelProps) {
  const [view, setView] = useState<"compute" | "object-storage">("compute");
  const [objectStorageRefresh, setObjectStorageRefresh] = useState(0);
  const [instances, setInstances] = useState<Instance[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [pendingDestroy, setPendingDestroy] = useState<Instance | null>(null);
  const [pendingUpgrade, setPendingUpgrade] = useState<Instance | null>(null);
  const [volumeInstance, setVolumeInstance] = useState<Instance | null>(null);

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

  const destroy = async (inst: Instance, options?: { force: boolean; retainVolumes?: boolean; retainFlexibleIPs: boolean }) => {
    setBusy(true);
    try {
	  const params = new URLSearchParams(withParams());
	  if (options) { params.set("force", String(options.force)); if (options.retainVolumes !== undefined) params.set("retain_volumes", String(options.retainVolumes)); params.set("retain_flexible_ips", String(options.retainFlexibleIPs)); }
      const r = await fetch(`${API}/instances/${inst.id}?${params.toString()}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      setPendingDestroy(null);
      await load();
    } catch (e) {
      setError("Destroy failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const upgrade = async (inst: Instance, size: string, upgradeDisk: boolean) => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/instances/${inst.id}/upgrade?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ size, upgrade_disk: upgradeDisk }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      setPendingUpgrade(null);
      await load();
    } catch (e) {
      setError("Upgrade failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  // Cost rollup: sum of monthly cost across remote (non-local)
  // instances. Local is free by construction; including it would just
  // confuse the number.
  const remoteInstances = (instances || []).filter((i) => i.provider !== "local");
  const remoteCount = remoteInstances.length;
  const monthlyTotal = formatRemoteTotal(remoteInstances);

  return (
    <div className="h-full flex flex-col">
      <header className="px-4 py-3 border-b border-border flex flex-wrap items-baseline gap-3">
        <h1 className="text-text font-semibold">Instances</h1>
        <span className="hidden md:inline text-xs text-text-muted">
          Provision compute, block volumes, and object storage across bound cloud providers.
        </span>
        <div className="flex items-center gap-1 ml-2">
          <button type="button" onClick={() => setView("compute")}
            className={`px-2 py-1 text-xs rounded ${view === "compute" ? "bg-bg-input text-text" : "text-text-muted hover:text-text"}`}>
            Compute
          </button>
          <button type="button" onClick={() => setView("object-storage")}
            className={`px-2 py-1 text-xs rounded ${view === "object-storage" ? "bg-bg-input text-text" : "text-text-muted hover:text-text"}`}>
            Object storage
          </button>
        </div>
        <span className="flex-1" />
        {view === "compute" && remoteCount > 0 && (
          <span
            className="text-xs text-text-muted px-2 py-0.5 rounded bg-bg-input/40 border border-border/40"
            title="Sum of monthly cost across non-local instances (0 when the catalog hasn't priced them yet)"
          >
            {monthlyTotal ? (
              <>
                <span className="font-mono text-text">{monthlyTotal}</span>
                <span className="text-text-dim">/mo</span>
                <span className="text-text-dim mx-1">·</span>
              </>
            ) : null}
            <span className="text-text-dim">{remoteCount} remote</span>
          </span>
        )}
        {view === "compute" && <button
          type="button"
          onClick={() => setShowCreate(true)}
          className="px-2 py-0.5 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg"
        >+ Provision</button>}
        <button
          type="button"
          onClick={() => view === "object-storage" ? setObjectStorageRefresh((value) => value + 1) : load()}
          disabled={busy}
          className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
        >Refresh</button>
      </header>

      {error && <div className="px-4 py-2 text-red text-xs border-b border-border">{error}</div>}

      <main className="flex-1 overflow-auto p-3 space-y-2">
        {view === "object-storage" ? (
          <ObjectStorageSection withParams={withParams} setError={setError} refresh={objectStorageRefresh} />
        ) : instances === null ? (
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
              onUpgrade={() => setPendingUpgrade(inst)}
              onVolumes={() => setVolumeInstance(inst)}
              onDestroy={() => setPendingDestroy(inst)}
            />
          ))
        )}
      </main>

      {pendingDestroy && (
        <DestroyConfirmDialog
          inst={pendingDestroy}
          busy={busy}
          onCancel={() => setPendingDestroy(null)}
		  onConfirm={(options) => destroy(pendingDestroy, options)}
        />
      )}

      {volumeInstance && (
        <VolumesDialog
          inst={volumeInstance}
          withParams={withParams}
          onClose={() => setVolumeInstance(null)}
          setError={setError}
        />
      )}

      {pendingUpgrade && (
        <UpgradeDialog
          inst={pendingUpgrade}
          busy={busy}
          withParams={withParams}
          onCancel={() => setPendingUpgrade(null)}
          onConfirm={(size, upgradeDisk) => upgrade(pendingUpgrade, size, upgradeDisk)}
        />
      )}

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

function ObjectStorageSection({ withParams, setError, refresh }: {
  withParams: () => string;
  setError: (value: string) => void;
  refresh: number;
}) {
  const [items, setItems] = useState<ObjectStorageWire[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [showCreate, setShowCreate] = useState(false);
  const [credentialResult, setCredentialResult] = useState<{
    object_storage: ObjectStorageWire;
    credentials: ObjectStorageCredentialsWire;
    warning?: string;
    warnings?: string[];
  } | null>(null);

  const load = useCallback(async () => {
    try {
      const response = await fetch(`${API}/object-storage?${withParams()}`, { credentials: "same-origin" });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      const body = await response.json();
      setItems(body.object_storages || []);
      setError("");
    } catch (error) {
      setItems([]);
      setError("Object storage failed: " + (error as Error).message);
    }
  }, [setError, withParams]);

  useEffect(() => { load(); }, [load, refresh]);

  const rotate = async (item: ObjectStorageWire) => {
    if (!window.confirm(`Rotate credentials for ${item.name}? The previous key will stop working.`)) return;
    setBusy(true);
    try {
      const response = await fetch(`${API}/object-storage/${item.id}/rotate-credentials?${withParams()}`, {
        method: "POST", credentials: "same-origin",
      });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      setCredentialResult(await response.json());
      await load();
    } catch (error) {
      setError("Credential rotation failed: " + (error as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const destroy = async (item: ObjectStorageWire) => {
    const scope = item.bucket ? `bucket ${item.bucket}` : `subscription ${item.name}`;
    if (!window.confirm(`Permanently delete ${scope}? Scaleway buckets must be empty. This cannot be undone.`)) return;
	setCredentialResult(null);
    setBusy(true);
    try {
      const response = await fetch(`${API}/object-storage/${item.id}?confirm=true&${withParams()}`, {
        method: "DELETE", credentials: "same-origin",
      });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      await load();
    } catch (error) {
      setError("Object storage destroy failed: " + (error as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 px-1 pb-1">
        <div>
          <div className="text-sm text-text font-medium">Object storage</div>
          <div className="text-[11px] text-text-muted">Instances provisions the resource and displays S3 credentials once. It does not store objects or create a Connection.</div>
        </div>
        <span className="flex-1" />
        <button type="button" onClick={() => setShowCreate(true)}
          className="px-2 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg">
          + Provision object storage
        </button>
      </div>

      {items === null ? (
        <div className="p-6 text-sm text-text-muted">Loading object storage…</div>
      ) : items.length === 0 ? (
        <div className="rounded p-6 text-sm text-text-muted" style={{ border: `1px solid ${SUBTLE_BORDER}` }}>
          No object-storage resources are tracked yet.
        </div>
      ) : items.map((item) => (
        <article key={item.id} className="rounded overflow-hidden" style={{ border: `1px solid ${SUBTLE_BORDER}` }}>
          <div className="px-3 py-2 flex flex-wrap items-center gap-2" style={{ backgroundColor: HEADER_STRIP_BG }}>
            <span className={statusColor(item.status) + " text-sm"}>●</span>
            <div className="min-w-0">
              <div className="text-sm text-text font-semibold truncate">{item.name}</div>
              <div className="text-[10px] text-text-dim">{[item.provider, item.region, item.plan].filter(Boolean).join(" · ")}</div>
            </div>
            <span className="flex-1" />
            <span className={statusColor(item.status) + " text-[10px] uppercase tracking-wider"}>{item.status}</span>
            <button type="button" disabled={busy} onClick={() => rotate(item)}
              className="px-2 py-0.5 text-[10px] border border-blue/60 text-blue rounded disabled:opacity-50">
              Rotate credentials
            </button>
            <button type="button" disabled={busy} onClick={() => destroy(item)}
              className="px-2 py-0.5 text-[10px] border border-red/60 text-red rounded disabled:opacity-50">
              Destroy
            </button>
          </div>
          <div className="p-3 grid grid-cols-1 md:grid-cols-3 gap-3 text-[11px]">
            <div><span className="text-text-dim block uppercase tracking-wider text-[9px]">Endpoint</span><span className="text-text font-mono break-all">{item.endpoint || "—"}</span></div>
            <div><span className="text-text-dim block uppercase tracking-wider text-[9px]">Bucket / resource</span><span className="text-text font-mono break-all">{item.bucket || item.provider_id}</span></div>
            <div><span className="text-text-dim block uppercase tracking-wider text-[9px]">Access key ID</span><span className="text-text font-mono break-all">{item.access_key_id || "—"}</span></div>
          </div>
          {item.error && <div className="px-3 pb-3 text-[11px] text-red">{item.error}</div>}
        </article>
      ))}

      {showCreate && (
        <CreateObjectStorageDialog
          withParams={withParams}
          onClose={() => setShowCreate(false)}
          onCreated={(result) => { setShowCreate(false); setCredentialResult(result); load(); }}
          setError={setError}
        />
      )}
      {credentialResult && (
        <ObjectStorageCredentialsDialog result={credentialResult} onClose={() => setCredentialResult(null)} />
      )}
    </section>
  );
}

function CreateObjectStorageDialog({ withParams, onClose, onCreated, setError }: {
  withParams: () => string;
  onClose: () => void;
  onCreated: (result: { object_storage: ObjectStorageWire; credentials: ObjectStorageCredentialsWire; warning?: string }) => void;
  setError: (value: string) => void;
}) {
  const [providers, setProviders] = useState<ObjectStorageProviderWire[]>([]);
  const [connectionID, setConnectionID] = useState(0);
  const [locations, setLocations] = useState<any[]>([]);
  const [plans, setPlans] = useState<any[]>([]);
  const [name, setName] = useState("");
  const [region, setRegion] = useState("");
  const [plan, setPlan] = useState("");
  const [bucket, setBucket] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState("");

  const selectedProvider = providers.find((provider) => provider.connection_id === connectionID);

  useEffect(() => {
    (async () => {
      try {
        const response = await fetch(`${API}/object-storage-providers?${withParams()}`, { credentials: "same-origin" });
        if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
        const body = await response.json();
        const available = (body.providers || []) as ObjectStorageProviderWire[];
        setProviders(available);
        setConnectionID((available.find((provider) => provider.default) || available[0])?.connection_id || 0);
      } catch (error) {
        setLocalError((error as Error).message);
        setLoading(false);
      }
    })();
  }, [withParams]);

  useEffect(() => {
    if (!selectedProvider) {
      if (providers.length === 0) setLoading(false);
      return;
    }
    (async () => {
      setLoading(true);
      setLocalError("");
      try {
        const params = new URLSearchParams(withParams());
        params.set("provider", selectedProvider.provider);
        params.set("provider_connection_id", String(selectedProvider.connection_id));
        const response = await fetch(`${API}/object-storage-plans?${params}`, { credentials: "same-origin" });
        if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
        const body = await response.json();
        const nextLocations = body.locations || [];
        const nextPlans = body.plans || [];
        setLocations(nextLocations);
        setPlans(nextPlans);
        setRegion(String(nextLocations[0]?.id ?? nextLocations[0]?.cluster_id ?? ""));
        setPlan(String(nextPlans[0]?.id ?? nextPlans[0]?.tier_id ?? ""));
      } catch (error) {
        setLocalError((error as Error).message);
      } finally {
        setLoading(false);
      }
    })();
  }, [providers, selectedProvider, withParams]);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!selectedProvider) return;
    setBusy(true);
    setLocalError("");
    try {
      const response = await fetch(`${API}/object-storage?${withParams()}`, {
        method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: name.trim(), provider: selectedProvider.provider, provider_connection_id: selectedProvider.connection_id,
          region, plan, bucket: selectedProvider.provider === "scaleway" ? bucket.trim() : "",
        }),
      });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      onCreated(await response.json());
    } catch (error) {
      const message = (error as Error).message;
      setLocalError(message);
      setError("Object storage provisioning failed: " + message);
    } finally {
      setBusy(false);
    }
  };

  const locationLabel = (location: any) => [location.name, location.location, location.region, location.country].filter(Boolean).join(" · ") || String(location.id);
  const planLabel = (candidate: any) => [candidate.name, candidate.label, candidate.description].filter(Boolean).join(" · ") || String(candidate.id);

  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/60" style={{ padding: 24 }} onClick={() => !busy && onClose()} role="presentation">
      <form onSubmit={submit} onClick={(event) => event.stopPropagation()} className="bg-bg border border-border rounded shadow-xl overflow-hidden" style={{ width: "min(560px, 100%)" }}>
        <div className="px-5 py-4 space-y-1" style={{ borderBottom: `1px solid ${SUBTLE_BORDER}` }}>
          <h2 className="text-text font-semibold">Provision object storage</h2>
          <p className="text-xs text-text-muted">Creates provider infrastructure and returns S3 credentials. No Connection is created.</p>
        </div>
        <div className="p-5 space-y-4">
          {localError && <div className="text-xs text-red">{localError}</div>}
          <div>
            <label className="text-xs text-text-muted block mb-1">Provider account</label>
            <select value={connectionID} onChange={(event) => setConnectionID(Number(event.target.value))} disabled={busy || providers.length === 0}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50">
              {providers.length === 0 && <option value={0}>No compatible provider bound</option>}
              {providers.map((provider) => <option key={provider.connection_id} value={provider.connection_id}>{provider.provider} · connection {provider.connection_id}{provider.default ? " (default)" : ""}</option>)}
            </select>
          </div>
          <div>
            <label className="text-xs text-text-muted block mb-1">Name</label>
            <input value={name} onChange={(event) => setName(event.target.value)} required className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" placeholder="media-production" />
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div>
              <label className="text-xs text-text-muted block mb-1">Region / cluster</label>
              <select value={region} onChange={(event) => setRegion(event.target.value)} disabled={loading || !locations.length}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50">
                {locations.map((location) => {
                  const value = String(location.id ?? location.cluster_id ?? "");
                  return <option key={value} value={value}>{locationLabel(location)}</option>;
                })}
              </select>
            </div>
            <div>
              <label className="text-xs text-text-muted block mb-1">Plan / tier</label>
              <select value={plan} onChange={(event) => setPlan(event.target.value)} disabled={loading || !plans.length}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50">
                {plans.map((candidate) => {
                  const value = String(candidate.id ?? candidate.tier_id ?? "");
                  return <option key={value} value={value}>{planLabel(candidate)}</option>;
                })}
              </select>
            </div>
          </div>
          {selectedProvider?.provider === "scaleway" && (
            <div>
              <label className="text-xs text-text-muted block mb-1">Bucket name <span className="text-text-dim">(optional)</span></label>
              <input value={bucket} onChange={(event) => setBucket(event.target.value.toLowerCase())}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono" placeholder="Generated automatically when blank" />
            </div>
          )}
          <div className="rounded p-3 text-[11px] text-amber" style={{ backgroundColor: "rgba(245,158,11,0.06)", border: "1px solid rgba(245,158,11,0.18)" }}>
            The secret key will be displayed once after provisioning. Instances does not save it, so copy it before closing the next screen.
          </div>
          <div className="flex justify-end gap-2">
            <button type="button" onClick={onClose} disabled={busy} className="px-3 py-1.5 text-sm border border-border rounded disabled:opacity-50">Cancel</button>
            <button type="submit" disabled={busy || loading || !name.trim() || !selectedProvider || !region || (selectedProvider?.provider === "vultr" && !plan)}
              className="px-3 py-1.5 text-sm bg-blue text-white rounded disabled:opacity-50">{busy ? "Provisioning…" : "Provision"}</button>
          </div>
        </div>
      </form>
    </div>
  );
}

function ObjectStorageCredentialsDialog({ result, onClose }: {
  result: { object_storage: ObjectStorageWire; credentials: ObjectStorageCredentialsWire; warning?: string; warnings?: string[] };
  onClose: () => void;
}) {
  const credentials = result.credentials;
  const copy = (value: string) => navigator.clipboard.writeText(value);
  const copyAll = () => copy([
    `S3_ENDPOINT=${credentials.endpoint}`,
    credentials.region ? `S3_REGION=${credentials.region}` : "",
    credentials.bucket ? `S3_BUCKET=${credentials.bucket}` : "",
    `S3_ACCESS_KEY_ID=${credentials.access_key_id}`,
    `S3_SECRET_ACCESS_KEY=${credentials.secret_access_key}`,
    credentials.expires_at ? `S3_CREDENTIALS_EXPIRES_AT=${credentials.expires_at}` : "",
  ].filter(Boolean).join("\n"));
  const rows = [
    ["Endpoint", credentials.endpoint], ["Region", credentials.region || ""], ["Bucket", credentials.bucket || ""],
    ["Access key ID", credentials.access_key_id], ["Secret access key", credentials.secret_access_key],
    ["Credentials expire", credentials.expires_at || ""],
  ].filter((row) => row[1]);
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70" style={{ padding: 24 }} role="presentation">
      <div role="dialog" aria-modal="true" className="bg-bg border border-amber/40 rounded shadow-xl overflow-hidden" style={{ width: "min(680px, 100%)" }}>
        <div className="px-5 py-4 space-y-1" style={{ borderBottom: `1px solid ${SUBTLE_BORDER}` }}>
          <h2 className="text-text font-semibold">Save object-storage credentials</h2>
          <p className="text-xs text-amber">Shown once. Closing this dialog permanently removes the secret from the Instances UI.</p>
        </div>
        <div className="p-5 space-y-3">
          {rows.map(([label, value]) => (
            <div key={label}>
              <span className="text-[10px] text-text-dim uppercase tracking-wider block mb-1">{label}</span>
              <div className="flex items-center gap-2">
                <code className="flex-1 min-w-0 bg-bg-input border border-border rounded px-2 py-2 text-xs text-text break-all select-all">{value}</code>
                <button type="button" onClick={() => copy(value)} className="px-2 py-1 text-xs border border-border rounded">Copy</button>
              </div>
            </div>
          ))}
          {(result.warnings || []).map((warning) => <div key={warning} className="text-xs text-amber">{warning}</div>)}
          <div className="flex justify-between items-center pt-2">
            <button type="button" onClick={copyAll} className="px-3 py-1.5 text-sm border border-blue/60 text-blue rounded">Copy all as environment variables</button>
            <button type="button" onClick={onClose} className="px-3 py-1.5 text-sm bg-blue text-white rounded">I saved them</button>
          </div>
        </div>
      </div>
    </div>
  );
}

function DestroyConfirmDialog({
  inst, busy, onCancel, onConfirm,
}: {
  inst: Instance;
  busy: boolean;
  onCancel: () => void;
	 onConfirm: (options: { force: boolean; retainVolumes?: boolean; retainFlexibleIPs: boolean }) => void;
}) {
  const ip = inst.public_ipv4 || inst.public_ipv6 || "—";
	const [force, setForce] = useState(false);
	const [volumePolicy, setVolumePolicy] = useState<"existing" | "retain" | "delete">("existing");
	const [retainFlexibleIPs, setRetainFlexibleIPs] = useState(false);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onCancel();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy, onCancel]);

  return (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/60"
      style={{ padding: 24 }}
      onClick={() => { if (!busy) onCancel(); }}
      role="presentation"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="destroy-instance-title"
        onClick={(e) => e.stopPropagation()}
        className="bg-bg border border-red/40 rounded shadow-xl overflow-hidden"
        style={{ width: "min(420px, 100%)" }}
      >
        <div
          className="px-5 py-4 space-y-1"
          style={{ borderBottom: `1px solid ${SUBTLE_BORDER}` }}
        >
          <h2 id="destroy-instance-title" className="text-text font-semibold">
            Destroy instance
          </h2>
          <p className="text-xs text-text-muted">
            This removes the host from Instances and terminates the upstream resource.
          </p>
        </div>

        <div className="p-5 space-y-4">
          <div
            className="rounded p-3 space-y-2"
            style={{ backgroundColor: "rgba(220,38,38,0.08)", border: "1px solid rgba(220,38,38,0.22)" }}
          >
            <div className="flex items-baseline justify-between gap-3">
              <span className="text-sm text-text font-medium truncate">{inst.name}</span>
              <span className={statusColor(inst.status) + " text-[11px] uppercase tracking-wider font-medium"}>
                {inst.status}
              </span>
            </div>
            <div className="grid grid-cols-2 gap-2 text-[11px] text-text-dim">
              <span>Provider: <span className="text-text font-mono">{inst.provider}</span></span>
              <span>Host: <span className="text-text font-mono">{ip}</span></span>
              {inst.region && <span>Region: <span className="text-text font-mono">{inst.region}</span></span>}
              {inst.size && <span>Size: <span className="text-text font-mono">{inst.size}</span></span>}
            </div>
          </div>
		  <div className="space-y-2 text-xs text-text-muted">
			<label className="flex items-center gap-2">Managed volumes <select value={volumePolicy} onChange={(event) => setVolumePolicy(event.target.value as "existing" | "retain" | "delete")} className="bg-bg-input border border-border rounded px-2 py-1"><option value="existing">Use each volume policy</option><option value="retain">Retain all</option><option value="delete">Delete all managed volumes</option></select></label>
			{inst.size?.startsWith("elastic-metal/") && <label className="flex items-center gap-2"><input type="checkbox" checked={retainFlexibleIPs} onChange={(event) => setRetainFlexibleIPs(event.target.checked)} /> Retain Elastic Metal Flexible IPs</label>}
			<label className="flex items-center gap-2"><input type="checkbox" checked={force} onChange={(event) => setForce(event.target.checked)} /> Continue provider deletion if guest unmount over SSH fails</label>
		  </div>

          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={onCancel}
              disabled={busy}
              className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="button"
			  onClick={() => onConfirm({ force, retainVolumes: volumePolicy === "existing" ? undefined : volumePolicy === "retain", retainFlexibleIPs })}
              disabled={busy}
              className="px-3 py-1.5 text-sm rounded bg-red text-white hover:bg-red/90 disabled:opacity-50"
            >
              {busy ? "Destroying…" : "Destroy"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function UpgradeDialog({
  inst, busy, withParams, onCancel, onConfirm,
}: {
  inst: Instance;
  busy: boolean;
  withParams: () => string;
  onCancel: () => void;
  onConfirm: (size: string, upgradeDisk: boolean) => void;
}) {
  const [serverTypes, setServerTypes] = useState<ServerTypeWire[]>([]);
  const [size, setSize] = useState("");
  const [upgradeDisk, setUpgradeDisk] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onCancel();
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy, onCancel]);

  useEffect(() => {
    (async () => {
      setLoading(true);
      setError(null);
      try {
        const qs = new URLSearchParams(withParams());
        qs.set("provider", inst.provider);
        const r = await fetch(`${API}/instances-server-types?${qs.toString()}`, { credentials: "same-origin" });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        const j = await r.json();
        const allTypes = ((j.server_types || []) as ServerTypeWire[])
          .filter((t) => !t.deprecated)
          .filter((t) => !inst.region || !t.available_in?.length || t.available_in.includes(inst.region))
          .sort((a, b) => catalogMonthlyPrice(a) - catalogMonthlyPrice(b));
        const current = allTypes.find((t) => t.name === inst.size);
        const types = allTypes.filter((t) => t.name !== inst.size);
        const upgrades = current
          ? types.filter((t) =>
              catalogMonthlyPrice(t) >= catalogMonthlyPrice(current) &&
              (t.cores > current.cores || t.memory_gb > current.memory_gb || t.disk_gb > current.disk_gb)
            )
          : types;
        setServerTypes(upgrades);
        if (upgrades.length) setSize(upgrades[0].name);
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setLoading(false);
      }
    })();
  }, [inst.region, inst.size, withParams]);

  const selected = serverTypes.find((t) => t.name === size);
  const typeLabel = (t: ServerTypeWire) => {
    const specs = [
      t.cores ? `${t.cores} ${t.cpu_type === "dedicated" ? "dedicated vCPU" : "vCPU"}` : "",
      t.memory_gb ? `${t.memory_gb} GB RAM` : "",
      t.disk_gb ? `${t.disk_gb} GB disk` : "",
    ].filter(Boolean).join(", ");
    return `${t.name} · ${formatCatalogMonthlyPrice(t) || "—"}${specs ? ` · ${specs}` : ""}`;
  };

  return (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/60"
      style={{ padding: 24 }}
      onClick={() => { if (!busy) onCancel(); }}
      role="presentation"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="upgrade-instance-title"
        onClick={(e) => e.stopPropagation()}
        className="bg-bg border border-blue/40 rounded shadow-xl overflow-hidden"
        style={{ width: "min(520px, 100%)" }}
      >
        <div
          className="px-5 py-4 space-y-1"
          style={{ borderBottom: `1px solid ${SUBTLE_BORDER}` }}
        >
          <h2 id="upgrade-instance-title" className="text-text font-semibold">
            Upgrade instance
          </h2>
          <p className="text-xs text-text-muted">
            In-place resize through the {inst.provider} adapter. The server is shut down, resized, powered on, then checked over SSH.
          </p>
        </div>

        <div className="p-5 space-y-4">
          <div className="grid grid-cols-2 gap-3 text-xs">
            <div
              className="rounded p-3"
              style={{ backgroundColor: SUB_CARD_BG, border: `1px solid ${SUBTLE_BORDER}` }}
            >
              <div className="text-text-dim uppercase text-[10px] tracking-wider mb-1">Current</div>
              <div className="text-text font-mono">{inst.size || "unknown"}</div>
              <div className="text-text-dim mt-1">{inst.region || "region unknown"}</div>
            </div>
            <div
              className="rounded p-3"
              style={{ backgroundColor: SUB_CARD_BG, border: `1px solid ${SUBTLE_BORDER}` }}
            >
              <div className="text-text-dim uppercase text-[10px] tracking-wider mb-1">Target</div>
              <div className="text-text font-mono">{selected?.name || "—"}</div>
              <div className="text-text-dim mt-1">{selected ? (formatCatalogMonthlyPrice(selected) || "—") : "—"}</div>
            </div>
          </div>

          {error ? (
            <div className="text-xs text-red">{error}</div>
          ) : loading ? (
            <div className="text-xs text-text-muted">Loading server types…</div>
          ) : serverTypes.length === 0 ? (
            <div className="text-xs text-text-muted">No upgrade target is available for this region.</div>
          ) : (
            <div className="space-y-3">
              <div>
                <label className="text-xs text-text-muted block mb-1">Target size</label>
                <select
                  value={size}
                  onChange={(e) => setSize(e.target.value)}
                  disabled={busy}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
                >
                  {serverTypes.map((t) => (
                    <option key={t.name} value={t.name}>{typeLabel(t)}</option>
                  ))}
                </select>
              </div>

              <label className="flex items-start gap-2 text-xs text-text-muted">
                <input
                  type="checkbox"
                  checked={upgradeDisk}
                  onChange={(e) => setUpgradeDisk(e.target.checked)}
                  disabled={busy}
                  className="mt-0.5"
                />
                <span>
                  <span className="text-text">Expand disk</span>
                  <span className="block text-text-dim">Off by default. Disk expansion usually cannot be undone.</span>
                </span>
              </label>
            </div>
          )}

          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={onCancel}
              disabled={busy}
              className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => onConfirm(size, upgradeDisk)}
              disabled={busy || loading || !!error || !size}
              className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
            >
              {busy ? "Upgrading…" : "Upgrade"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// MetricsSample is a single tick captured for the in-memory history.
// We don't persist anything; the panel session is the entire window.
// Caps at HISTORY_MAX entries so memory stays bounded if someone
// leaves the panel open for hours.
interface MetricsSample {
  ts: number; // ms since epoch when the metrics fetch resolved
  cpuPct: number;
  memPct: number;
  l1: number;
}
const HISTORY_MAX = 360;          // 10s polling × 360 = 1 hour
const STALE_THRESHOLD_MS = 30000; // 30s without a successful poll → "stale"

function InstanceCard({
  inst, withParams, busy, onUpgrade, onVolumes, onDestroy,
}: {
  inst: Instance;
  withParams: () => string;
  busy: boolean;
  onUpgrade: () => void;
  onVolumes: () => void;
  onDestroy: () => void;
}) {
  const [metrics, setMetrics] = useState<MetricsWire | null>(null);
  const [metricsError, setMetricsError] = useState("");
  const [lastPollAt, setLastPollAt] = useState(0);
  const [expanded, setExpanded] = useState(false);
	const [comparison, setComparison] = useState<{ differences?: string[]; provider_state?: string; checked_at?: string } | null>(null);
	const [diagnosticBusy, setDiagnosticBusy] = useState(false);
	const [benchmark, setBenchmark] = useState<{ output?: string; elapsed_seconds?: number; measured_at?: string } | null>(null);
	const [benchmarkBusy, setBenchmarkBusy] = useState(false);
  const [, setNowTick] = useState(0);

  useEffect(() => {
    let cancelled = false;
    let inFlight = false;
    const controller = new AbortController();
    const fetchMetrics = async () => {
      if (inst.status !== "ready" || inFlight) return;
      inFlight = true;
      try {
        const response = await fetch(`${API}/instances/${inst.id}/metrics?${withParams()}`, {
          credentials: "same-origin",
          signal: controller.signal,
        });
        if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "metrics unavailable")}`);
        const payload = await response.json();
        if (cancelled || !payload?.metrics) return;
        const next = payload.metrics as MetricsWire;
        setMetrics(next);
        setMetricsError("");
        setLastPollAt(Date.now());
      } catch (error) {
        if (!cancelled && (error as Error).name !== "AbortError") setMetricsError((error as Error).message);
      } finally {
        inFlight = false;
      }
    };
    fetchMetrics();
    const poll = setInterval(fetchMetrics, 10000);
    const clock = setInterval(() => setNowTick((value) => value + 1), 5000);
    return () => {
      cancelled = true;
      controller.abort();
      clearInterval(poll);
      clearInterval(clock);
    };
  }, [inst.id, inst.status, withParams]);

  const ip = inst.public_ipv4 || inst.public_ipv6 || "—";
  const sshHost = inst.ssh_host || ip;
  const endpoint = inst.ssh_port && inst.ssh_port !== 22 && sshHost !== "—" ? `${sshHost}:${inst.ssh_port}` : sshHost;
  const isLocal = inst.provider === "local";
  const canUpgrade = inst.capabilities?.upgrade ?? inst.provider === "hetzner";
  const canDestroy = inst.capabilities?.destroy ?? ["hetzner", "digitalocean", "vultr", "aws-ec2", "scaleway", "huawei-cloud", "linode", "ovhcloud", "runpod"].includes(inst.provider);
  const deleteLocked = deletionLockLabel(inst);
  const resources = resourceSummary(inst);
  const memPct = metrics?.mem.total_bytes ? (metrics.mem.used_bytes / metrics.mem.total_bytes) * 100 : 0;
  const loadPct = metrics?.cpu.cores ? (metrics.load.l1 / metrics.cpu.cores) * 100 : 0;
  const rootDisk = metrics?.disk.find((disk) => disk.mount === "/") || metrics?.disk[0];
  const staleAge = lastPollAt ? Math.floor((Date.now() - lastPollAt) / 1000) : 0;
  const stale = staleAge > STALE_THRESHOLD_MS / 1000;
  const meta = [inst.provider, inst.size, inst.region, resources].filter(Boolean).join(" · ");
	const compare = async () => {
		setDiagnosticBusy(true);
		try {
			const response = await fetch(`${API}/instances/${inst.id}/compare?${withParams()}`, { method: "POST", credentials: "same-origin" });
			if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
			setComparison((await response.json()).comparison);
		} catch (error) { setComparison({ differences: [(error as Error).message] }); }
		finally { setDiagnosticBusy(false); }
	};
	const runBenchmark = async () => {
		setBenchmarkBusy(true);
		try {
			const response = await fetch(`${API}/instances/${inst.id}/storage-benchmark?${withParams()}`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ target_path: "/" }) });
			if (!response.ok) throw new Error(`${response.status}: ${await response.text()}`);
			setBenchmark((await response.json()).result);
		} catch (error) { setBenchmark({ output: (error as Error).message }); }
		finally { setBenchmarkBusy(false); }
	};

  return (
    <article
      className="overflow-hidden rounded"
      style={{ backgroundColor: "var(--bg-card, transparent)", border: `1px solid ${SUBTLE_BORDER}` }}
    >
      <div className="flex flex-wrap items-center gap-2 px-3 py-2 min-w-0" style={{ backgroundColor: HEADER_STRIP_BG }}>
        <span className={statusColor(inst.status) + " text-sm leading-none"}>●</span>
        <div className="min-w-0" style={{ minWidth: 180 }}>
          <div className="flex items-baseline gap-2 min-w-0">
            <span className="text-sm text-text font-semibold truncate">{inst.name}</span>
            <span className="text-[11px] text-text-dim truncate">{meta}</span>
          </div>
          <div className="flex items-center gap-2 text-[10px] text-text-dim min-w-0">
            <span className="font-mono truncate">{endpoint}</span>
            {!isLocal && inst.monthly_cost_cents > 0 && (
              <span className="font-mono">{formatProviderPrice(inst.monthly_cost_cents, inst.provider)}/mo</span>
            )}
            {stale && <span className="text-amber">stale {staleAge}s</span>}
          </div>
        </div>
        <span className="flex-1" />
        <span className={statusColor(inst.status) + " text-[10px] uppercase tracking-wider font-medium"}>{inst.status}</span>
        {deleteLocked && <span className="text-[10px] text-amber" title={deleteLocked}>24h lock</span>}
        {canUpgrade && (
          <button type="button" onClick={onUpgrade} disabled={busy || inst.status !== "ready"}
            className="px-2 py-0.5 text-[10px] border border-blue/60 text-blue rounded hover:bg-blue hover:text-white disabled:opacity-50">
            Upgrade
          </button>
        )}
        {!isLocal && (
          <button type="button" onClick={onVolumes} disabled={busy}
            className="px-2 py-0.5 text-[10px] border border-border text-text-muted rounded hover:text-text disabled:opacity-50">
            Volumes
          </button>
        )}
		{!isLocal && inst.provider !== "external" && (
			<button type="button" onClick={compare} disabled={diagnosticBusy}
				className="px-2 py-0.5 text-[10px] border border-border text-text-muted rounded hover:text-text disabled:opacity-50">
				{diagnosticBusy ? "Comparing…" : "Compare provider"}
			</button>
		)}
        {canDestroy && (
          <button type="button" onClick={onDestroy} disabled={busy}
            className="px-2 py-0.5 text-[10px] border border-red/60 text-red rounded hover:bg-red hover:text-white disabled:opacity-50">
            Destroy
          </button>
        )}
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="w-7 h-7 shrink-0 text-text-muted hover:text-text"
          aria-label={expanded ? `Collapse ${inst.name}` : `Expand ${inst.name}`}
          title={expanded ? "Hide details" : "Show details"}
        >
          {expanded ? "▾" : "›"}
        </button>
      </div>

      {inst.error && <div className="px-3 py-1.5 text-[10px] text-red border-t border-red/20">{inst.lifecycle_stage ? `${inst.lifecycle_stage}: ` : ""}{inst.error}{inst.cleanup_error ? ` · cleanup: ${inst.cleanup_error}` : ""}</div>}
	  {comparison && <div className={`px-3 py-1.5 text-[10px] border-t ${comparison.differences?.length ? "text-amber border-amber/20" : "text-green border-green/20"}`}>Provider {comparison.provider_state || "state"}: {comparison.differences?.length ? comparison.differences.join(" · ") : "matches tracked state"}</div>}

      {metrics ? (
        <div
          className="grid grid-cols-2 md:grid-cols-5 px-3 py-2.5"
          style={{ borderTop: `1px solid ${FAINT_DIVIDER}`, opacity: stale ? 0.6 : 1, columnGap: 24, rowGap: 10 }}
        >
          <CompactMetric label="CPU" value={formatCPUDetail(metrics.cpu)} pct={metrics.cpu.total_pct} />
          <CompactMetric label="Memory" value={`${formatBytes(metrics.mem.used_bytes)} / ${formatBytes(metrics.mem.total_bytes)} used (${memPct.toFixed(0)}%)`} pct={memPct} />
          <CompactMetric label="Root disk" value={rootDisk ? `${rootDisk.used_pct.toFixed(0)}% · ${formatBytes(rootDisk.used_bytes)}` : "—"} pct={rootDisk?.used_pct} />
          <CompactMetric label="Load" value={metrics.cpu.cores ? `${metrics.load.l1.toFixed(2)} · ${loadPct.toFixed(0)}% cap` : metrics.load.l1.toFixed(2)} pct={loadPct} />
          <CompactMetric label="Uptime" value={`${formatUptime(metrics.uptime_s)} · ${metrics.process_count} proc`} />
        </div>
      ) : inst.status === "ready" ? (
        <div className={`px-3 py-2 text-[10px] border-t ${metricsError ? "text-red border-red/20" : "text-text-dim"}`}>
          {metricsError ? `Vitals unavailable: ${metricsError}` : "Loading vitals…"}
        </div>
      ) : null}

      {expanded && (
        <div className="px-3 py-3 space-y-3" style={{ borderTop: `1px solid ${SUBTLE_BORDER}`, backgroundColor: SUB_CARD_BG }}>
          {metricsError && metrics && <div className="text-[10px] text-red">Metrics refresh failed: {metricsError}</div>}
          {metrics?.disk?.length ? (
            <div className="grid grid-cols-1 md:grid-cols-2" style={{ columnGap: 24, rowGap: 10 }}>
              {metrics.disk.map((disk) => (
                <CompactMetric
                  key={disk.mount}
                  label={disk.mount}
                  value={`${formatBytes(disk.used_bytes)} / ${formatBytes(disk.total_bytes)} · ${disk.used_pct.toFixed(0)}%`}
                  pct={disk.used_pct}
                />
              ))}
            </div>
          ) : null}
          <div className="flex flex-wrap text-[10px] text-text-dim" style={{ columnGap: 20, rowGap: 4 }}>
            {inst.image && <span>Image <span className="text-text font-mono">{inst.image}</span></span>}
            {inst.provider_id && <span>Provider ID <span className="text-text font-mono">{inst.provider_id}</span></span>}
            {metrics && <span>Load 1/5/15 <span className="text-text font-mono">{metrics.load.l1.toFixed(2)} / {metrics.load.l5.toFixed(2)} / {metrics.load.l15.toFixed(2)}</span></span>}
          </div>
		  {inst.status === "ready" && (
			<div className="space-y-1">
			  <button type="button" onClick={runBenchmark} disabled={benchmarkBusy} className="px-2 py-0.5 text-[10px] border border-border text-text-muted rounded hover:text-text disabled:opacity-50">
				{benchmarkBusy ? "Benchmarking…" : "Benchmark root disk (256 MiB)"}
			  </button>
			  {benchmark?.output && <pre className="text-[10px] text-text-dim whitespace-pre-wrap break-all">{benchmark.output}</pre>}
			</div>
		  )}
        </div>
      )}
    </article>
  );
}

function CompactMetric({
  label, value, pct, children,
}: {
  label: string;
  value: string;
  pct?: number;
  children?: React.ReactNode;
}) {
  const clamped = pct === undefined ? undefined : Math.max(0, Math.min(100, pct));
  return (
    <div className="min-w-0" style={{ minHeight: 32 }}>
      <div className="flex items-center justify-between gap-2 text-[10px] leading-4">
        <span className="text-text-dim uppercase tracking-wider truncate">{label}</span>
        <span className="text-text font-mono whitespace-nowrap">{value}</span>
        {children}
      </div>
      {clamped !== undefined && (
        <div className="h-1 rounded-full overflow-hidden mt-1" style={{ backgroundColor: "rgba(255,255,255,0.06)" }}>
          <div className="h-full rounded-full transition-all duration-500" style={{ width: `${clamped}%`, backgroundColor: pctColor(clamped) }} />
        </div>
      )}
    </div>
  );
}

function ExpandedInstanceCardLegacy({
  inst, withParams, busy, onUpgrade, onDestroy,
}: {
  inst: Instance;
  withParams: () => string;
  busy: boolean;
  onUpgrade: () => void;
  onDestroy: () => void;
}) {
  const [metrics, setMetrics] = useState<MetricsWire | null>(null);
  const [history, setHistory] = useState<MetricsSample[]>([]);
  const [lastPollAt, setLastPollAt] = useState<number>(0);
  const [metricsError, setMetricsError] = useState("");
  const [, setNowTick] = useState(0); // forces stale-badge re-render

  useEffect(() => {
    let cancelled = false;
    let inFlight = false;
    const controller = new AbortController();
    const fetchMetrics = async () => {
      if (inst.status !== "ready") return;
      if (inFlight) return;
      inFlight = true;
      try {
        const r = await fetch(`${API}/instances/${inst.id}/metrics?${withParams()}`, {
          credentials: "same-origin",
          signal: controller.signal,
        });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "metrics unavailable")}`);
        const j = await r.json();
        if (cancelled || !j?.metrics) return;
        const m = j.metrics as MetricsWire;
        setMetrics(m);
        setMetricsError("");
        setLastPollAt(Date.now());
          // Append to history. Memory % derived from used/total so
          // the chart can put cpu + mem on the same 0-100 scale.
          const memPct = m.mem.total_bytes > 0
            ? (m.mem.used_bytes / m.mem.total_bytes) * 100
            : 0;
        setHistory((prev) => {
          const next = [...prev, { ts: Date.now(), cpuPct: m.cpu.total_pct, memPct, l1: m.load.l1 }];
          return next.length > HISTORY_MAX ? next.slice(-HISTORY_MAX) : next;
        });
      } catch (e) {
        if (!cancelled && (e as Error).name !== "AbortError") setMetricsError((e as Error).message);
      } finally {
        inFlight = false;
      }
    };
    fetchMetrics();
    const t = setInterval(fetchMetrics, 10000);
    // Tick the clock every 5s so the "stale Ns ago" badge updates
    // without waiting on the next metrics fetch.
    const tick = setInterval(() => setNowTick((n) => n + 1), 5000);
    return () => { cancelled = true; controller.abort(); clearInterval(t); clearInterval(tick); };
  }, [inst.id, inst.status, withParams]);

  const ip = inst.public_ipv4 || inst.public_ipv6 || "—";
  const sshHost = inst.ssh_host || ip;
  const endpoint = inst.ssh_port && inst.ssh_port !== 22 && sshHost !== "—" ? `${sshHost}:${inst.ssh_port}` : sshHost;
  const isLocal = inst.provider === "local";
  const canUpgrade = inst.capabilities?.upgrade ?? inst.provider === "hetzner";
  const canDestroy = inst.capabilities?.destroy ?? ["hetzner", "digitalocean", "vultr", "aws-ec2", "scaleway", "huawei-cloud", "linode", "ovhcloud", "runpod"].includes(inst.provider);
  const deleteLocked = deletionLockLabel(inst);
  const resources = resourceSummary(inst);
  const resourceModel = resources.toLowerCase().replace(/^[0-9]+x\s+/, "");
  const showResources = resources && (!resources.startsWith("1x ") || !inst.size?.toLowerCase().includes(resourceModel));
  const memPct = metrics && metrics.mem.total_bytes > 0
    ? (metrics.mem.used_bytes / metrics.mem.total_bytes) * 100
    : 0;
  const staleAgeS = lastPollAt > 0 ? Math.floor((Date.now() - lastPollAt) / 1000) : 0;
  const stale = lastPollAt > 0 && (Date.now() - lastPollAt) > STALE_THRESHOLD_MS;
  const loadPressurePct = metrics?.cpu.cores ? (metrics.load.l1 / metrics.cpu.cores) * 100 : 0;

  // Pick the most space-pressed mounts first when there are many
  // (local dev box has /dev, /System/Volumes/VM, etc.). Top 3 by
  // used_pct keeps the interesting ones; full list is one click
  // away in the existing /metrics REST shape if needed later.
  const sortedDisks = metrics?.disk
    ? [...metrics.disk].sort((a, b) => b.used_pct - a.used_pct).slice(0, 3)
    : [];

  return (
    <div
      className="rounded-lg overflow-hidden"
      // Outer card: very subtle border + bg-card tint. The dashboard's
      // `--border` token looked stark in dark mode; inline rgba sits
      // closer to the panel bg, giving a quiet edge.
      style={{ backgroundColor: "var(--bg-card, transparent)", border: `1px solid ${SUBTLE_BORDER}` }}
    >
      {/* Header strip — distinct, slightly lighter background so the
          card has a visible "spine" and the body content reads as
          bounded sections without needing strong borders. */}
      <div
        className="flex items-center gap-2 px-4 py-3 flex-wrap"
        style={{ backgroundColor: HEADER_STRIP_BG, borderBottom: `1px solid ${SUBTLE_BORDER}` }}
      >
        <span className={statusColor(inst.status) + " text-base leading-none"}>●</span>
        <span className="text-text font-semibold">{inst.name}</span>
        <span className="text-text-dim text-xs ml-1">
          {inst.provider}
          {inst.size ? ` · ${inst.size}` : ""}
          {inst.region ? ` · ${inst.region}` : ""}
          {showResources ? ` · ${resources}` : ""}
        </span>
        <span className="text-text-dim text-xs font-mono ml-2">{endpoint}</span>
        {!isLocal && inst.monthly_cost_cents > 0 && (
          <span
            className="text-[11px] text-text-muted font-mono ml-2"
            title="Monthly cost from the provider catalog"
          >
            {formatProviderPrice(inst.monthly_cost_cents, inst.provider)}/mo
          </span>
        )}
        <span className="flex-1" />
        {stale && metrics && (
          <span
            className="text-[10px] px-1.5 py-0.5 rounded bg-amber/15 text-amber"
            title={`No successful metrics poll for ${staleAgeS}s — values shown may be outdated`}
          >stale {staleAgeS}s</span>
        )}
        <span className={statusColor(inst.status) + " text-[11px] uppercase tracking-wider font-medium"}>
          {inst.status}
        </span>
        {deleteLocked && <span className="text-[10px] text-amber" title={deleteLocked}>24h lock</span>}
        {(canUpgrade || canDestroy) && (
          <>
            {canUpgrade && <button
              type="button"
              onClick={onUpgrade}
              disabled={busy || inst.status !== "ready"}
              className="px-2 py-0.5 text-[11px] border border-blue/60 text-blue rounded hover:bg-blue hover:text-white disabled:opacity-50"
            >Upgrade</button>}
            {canDestroy && <button
              type="button"
              onClick={onDestroy}
              disabled={busy}
              className="px-2 py-0.5 text-[11px] border border-red/60 text-red rounded hover:bg-red hover:text-white disabled:opacity-50"
            >Destroy</button>}
          </>
        )}
      </div>

      {inst.error && (
        <div className="px-4 py-2 text-red text-xs bg-red/5 border-b border-red/20">
          {inst.error}
        </div>
      )}

      {/* Body — split into three sub-cards (vitals / disk / history).
          Background depth comes from rgba tints rather than tokens
          because the dashboard's bg-input/N classes can be loud in
          dark mode. */}
      {metrics ? (
        <div
          className="p-4 space-y-4"
          style={stale ? { opacity: 0.55 } : undefined}
        >
          {metricsError && <div className="text-[11px] text-red">Metrics refresh failed: {metricsError}</div>}
          {/* Vitals — bars + stat row. */}
          <div
            className="rounded-md p-4 space-y-4"
            style={{ backgroundColor: SUB_CARD_BG, border: `1px solid ${SUBTLE_BORDER}` }}
          >
            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div className="flex items-end gap-3">
                <div className="flex-1 min-w-0">
                  <ProgressBar
                    label="CPU"
                    sublabel={formatCPUDetail(metrics.cpu)}
                    pct={metrics.cpu.total_pct}
                  />
                </div>
                <Sparkline
                  values={history.map((s) => s.cpuPct)}
                  color="#3b82f6"
                  width={64}
                  height={22}
                />
              </div>
              <div className="flex items-end gap-3">
                <div className="flex-1 min-w-0">
                  <ProgressBar
                    label="Memory"
                    sublabel={`${formatBytes(metrics.mem.used_bytes)} / ${formatBytes(metrics.mem.total_bytes)} · ${memPct.toFixed(0)}%`}
                    pct={memPct}
                  />
                </div>
                <Sparkline
                  values={history.map((s) => s.memPct)}
                  color="#a78bfa"
                  width={64}
                  height={22}
                />
              </div>
              <div className="flex items-end gap-3">
                <div className="flex-1 min-w-0">
                  <ProgressBar
                    label="Load pressure"
                    sublabel={metrics.cpu.cores ? `${metrics.load.l1.toFixed(2)} / ${metrics.cpu.cores} vCPU · ${loadPressurePct.toFixed(0)}%` : metrics.load.l1.toFixed(2)}
                    pct={loadPressurePct}
                  />
                </div>
              </div>
            </div>

            <div
              className="flex flex-wrap text-xs pt-3"
              // Inline gap + border because Tailwind JIT can't be
              // relied on for gap-x-N / border-border/N at panel-
              // bundle time. Real screenshot showed labels touching
              // values + chips merging without padding.
              style={{ gap: "20px", borderTop: `1px solid ${FAINT_DIVIDER}` }}
            >
              <StatChip label="Load (1m)">
                <span
                  className="font-mono"
                  style={{ color: loadColor(metrics.load.l1, metrics.cpu.cores) }}
                  title={
                    metrics.cpu.cores && metrics.cpu.cores > 0
                      ? `1/5/15 min: ${metrics.load.l1.toFixed(2)} / ${metrics.load.l5.toFixed(2)} / ${metrics.load.l15.toFixed(2)} · ${((metrics.load.l1 / metrics.cpu.cores) * 100).toFixed(0)}% of ${metrics.cpu.cores} vCPU capacity`
                      : `1/5/15 min: ${metrics.load.l1.toFixed(2)} / ${metrics.load.l5.toFixed(2)} / ${metrics.load.l15.toFixed(2)}`
                  }
                >
                  {metrics.load.l1.toFixed(2)}
                </span>
                <span className="text-text-dim font-mono ml-1">
                  / {metrics.load.l5.toFixed(2)} / {metrics.load.l15.toFixed(2)}
                </span>
              </StatChip>
              <StatChip label="Uptime">
                <span className="text-text font-mono">{formatUptime(metrics.uptime_s)}</span>
              </StatChip>
              {metrics.process_count > 0 && (
                <StatChip label="Processes">
                  <span className="text-text font-mono">{metrics.process_count}</span>
                </StatChip>
              )}
            </div>
          </div>

          {/* Disks — sub-card with titled header strip. */}
          {sortedDisks.length > 0 && (
            <div
              className="rounded-md overflow-hidden"
              style={{ backgroundColor: SUB_CARD_BG, border: `1px solid ${SUBTLE_BORDER}` }}
            >
              <SectionHeader title="Disk" />
              <div className="p-4 space-y-3">
                {sortedDisks.map((d) => (
                  <ProgressBar
                    key={d.mount}
                    label={d.mount}
                    sublabel={`${formatBytes(d.used_bytes)} / ${formatBytes(d.total_bytes)} · ${d.used_pct.toFixed(0)}%`}
                    pct={d.used_pct}
                    height={8}
                  />
                ))}
                {metrics.disk.length > sortedDisks.length && (
                  <div className="text-[10px] text-text-dim italic">
                    + {metrics.disk.length - sortedDisks.length} more mount(s) — top {sortedDisks.length} by utilization shown
                  </div>
                )}
              </div>
            </div>
          )}

          {/* History sub-card disabled in v0.4.4 — operator preferred
              the at-a-glance sparklines next to CPU/MEM bars over a
              full-width time-range chart that mostly read as flat
              lines anyway in dark mode. MultiLineChart kept defined
              above so re-enabling is one block of JSX away. */}
        </div>
      ) : inst.status === "ready" ? (
        <div className={`px-4 py-3 text-[11px] ${metricsError ? "text-red" : "text-text-dim"}`}>
          {metricsError ? `Vitals unavailable: ${metricsError}` : "Loading vitals…"}
        </div>
      ) : null}
    </div>
  );
}

// StatChip — uniform label + value pair for the stat row under the
// vitals bars. Inline-padded label because Tailwind utility margins
// (mr-1.5 etc.) sometimes don't compile in the panel CSS bundle, and
// the live screenshot showed labels touching values.
function StatChip({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="inline-flex items-baseline">
      <span
        className="text-text-dim uppercase text-[10px] tracking-wider font-medium"
        style={{ marginRight: "8px" }}
      >{label}</span>
      <span>{children}</span>
    </div>
  );
}

// SectionHeader — uniform titled strip on each sub-card. Used for
// Disk and History (live). Right side optional, useful for showing
// the session-scope hint on the history chart.
function SectionHeader({ title, right }: { title: string; right?: string }) {
  return (
    <div
      className="px-4 py-2 flex items-center justify-between"
      style={{
        backgroundColor: "rgba(255,255,255,0.02)",
        borderBottom: `1px solid ${FAINT_DIVIDER}`,
      }}
    >
      <span className="text-[10px] uppercase tracking-wider text-text-dim font-semibold">
        {title}
      </span>
      {right && (
        <span className="text-[10px] text-text-dim normal-case">{right}</span>
      )}
    </div>
  );
}

function VolumesDialog({ inst, withParams, onClose, setError }: {
  inst: Instance;
  withParams: () => string;
  onClose: () => void;
  setError: (message: string) => void;
}) {
  const [volumes, setVolumes] = useState<InstanceVolumeWire[]>([]);
  const [capabilities, setCapabilities] = useState<StorageCapabilitiesWire | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [name, setName] = useState(`${inst.name}-data`);
  const [sizeGB, setSizeGB] = useState(80);
  const [tier, setTier] = useState("");
  const [deletePolicy, setDeletePolicy] = useState<"retain" | "with_instance">("retain");
  const [prepareGuest, setPrepareGuest] = useState(true);
  const [filesystem, setFilesystem] = useState<"ext4" | "xfs">("ext4");
  const [mountPath, setMountPath] = useState(`/srv/${inst.name.replace(/[^A-Za-z0-9._-]+/g, "-")}-data`);
  const [mountOwner, setMountOwner] = useState("root:root");
  const params = useCallback(() => {
    const value = new URLSearchParams(withParams());
    value.set("instance_id", String(inst.id));
    return value.toString();
  }, [inst.id, withParams]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const capParams = new URLSearchParams(withParams());
      capParams.set("provider", inst.provider);
      const [volumesResponse, capabilitiesResponse] = await Promise.all([
        fetch(`${API}/instance-volumes?${params()}`, { credentials: "same-origin" }),
        fetch(`${API}/instances-storage-capabilities?${capParams}`, { credentials: "same-origin" }),
      ]);
      if (!volumesResponse.ok) throw new Error(`${volumesResponse.status}: ${await volumesResponse.text()}`);
      if (!capabilitiesResponse.ok) throw new Error(`${capabilitiesResponse.status}: ${await capabilitiesResponse.text()}`);
      const volumeBody = await volumesResponse.json();
      const capabilitiesBody = await capabilitiesResponse.json();
      const nextCapabilities = capabilitiesBody.capabilities as StorageCapabilitiesWire;
      setVolumes(volumeBody.volumes || []);
      setCapabilities(nextCapabilities);
      if (!tier) setTier(nextCapabilities.tiers?.[0]?.name || "provider-default");
    } catch (error) {
      setError("Volumes failed: " + (error as Error).message);
    } finally {
      setLoading(false);
    }
  }, [inst.provider, params, setError, tier, withParams]);

  useEffect(() => { load(); }, [load]);

  const mutate = async (path: string, method: string, body?: Record<string, unknown>) => {
    setBusy(true);
    try {
      const response = await fetch(`${API}${path}${path.includes("?") ? "&" : "?"}${withParams()}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!response.ok) throw new Error(`${response.status}: ${await response.text().catch(() => "")}`);
      await load();
    } catch (error) {
      setError("Volume operation failed: " + (error as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!name.trim() || sizeGB <= 0) return;
    await mutate("/instance-volumes", "POST", {
      instance_id: inst.id,
      name: name.trim(),
      size_gb: sizeGB,
      tier,
      delete_policy: deletePolicy,
      prepare: prepareGuest && capabilities?.guest_prepare ? {
        filesystem,
        mount_path: mountPath.trim(),
        owner: mountOwner.trim() || "root:root",
        mode: "0755",
        mount_options: "defaults,nofail",
        format_if_blank: true,
      } : undefined,
    });
  };

  let requestedBoot: { size_gb?: number; storage_class?: string; tier?: string; provider_type?: string } | null = null;
  try { requestedBoot = JSON.parse(inst.storage_json || "{}").boot || null; } catch { requestedBoot = null; }

  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/60" style={{ padding: 24 }} onClick={onClose}>
      <div className="bg-bg border border-border rounded shadow-xl overflow-hidden" style={{ width: "min(680px, 100%)" }} onClick={(event) => event.stopPropagation()}>
        <div className="px-5 py-4 flex items-start gap-3" style={{ borderBottom: `1px solid ${SUBTLE_BORDER}` }}>
          <div>
            <h2 className="text-text font-semibold">Volumes · {inst.name}</h2>
            <p className="text-xs text-text-muted">Boot storage and attached data storage are tracked separately. New data volumes default to retain.</p>
          </div>
          <span className="flex-1" />
          <button type="button" onClick={onClose} className="text-text-muted hover:text-text">×</button>
        </div>
        <div className="p-5 space-y-4 max-h-[70vh] overflow-auto">
          <div className="rounded p-3 flex items-center gap-2" style={{ border: `1px solid ${SUBTLE_BORDER}`, backgroundColor: SUB_CARD_BG }}>
            <div>
              <div className="text-sm text-text font-medium">Boot volume</div>
              <div className="text-[10px] text-text-dim">
                boot · {requestedBoot?.size_gb ? `${requestedBoot.storage_class || "provider-default"} · ${requestedBoot.size_gb} GB · ${requestedBoot.provider_type || requestedBoot.tier || "provider-default"}` : "provider/image default"} · lifecycle tied to instance
              </div>
            </div>
            <span className="flex-1" />
            <span className="text-[10px] uppercase text-text-dim">boot</span>
          </div>
          {loading ? <div className="text-xs text-text-muted">Loading volumes…</div> : volumes.length === 0 ? (
            <div className="text-xs text-text-dim">No managed data volumes are attached.</div>
          ) : (
            <div className="space-y-2">
              {volumes.map((volume) => (
                <div key={volume.id} className="rounded p-3 flex flex-wrap items-center gap-2" style={{ border: `1px solid ${SUBTLE_BORDER}`, backgroundColor: SUB_CARD_BG }}>
                  <div className="min-w-0">
                    <div className="text-sm text-text font-medium">{volume.name}</div>
                    <div className="text-[10px] text-text-dim">{volume.role} · {volume.storage_class} · {volume.size_gb} GB · {volume.tier} · {volume.status} · {volume.delete_policy}</div>
                    {volume.mount_path && <div className="text-[10px] text-green font-mono">mounted {volume.mount_path} · {volume.filesystem} · {volume.device_path}</div>}
                    {!volume.mount_path && volume.role === "data" && <div className="text-[10px] text-amber">Attached block device is not mounted in the guest.</div>}
                  </div>
                  <span className="flex-1" />
                  {capabilities?.resize && <button type="button" disabled={busy} className="px-2 py-1 text-[10px] border border-border rounded" onClick={() => {
                    const value = window.prompt("New size in GB (must be larger)", String(volume.size_gb));
                    if (value) mutate(`/instance-volumes/${volume.id}/resize`, "POST", { size_gb: Number(value) });
                  }}>Resize</button>}
                  {volume.role === "data" && !volume.mount_path && capabilities?.guest_prepare && <button type="button" disabled={busy} className="px-2 py-1 text-[10px] border border-blue/60 text-blue rounded" onClick={() => {
                    const value = window.prompt("Mount path. A blank device will be formatted ext4; existing signatures are never overwritten.", `/srv/${volume.name.replace(/[^A-Za-z0-9._-]+/g, "-")}`);
                    if (value) mutate(`/instance-volumes/${volume.id}/prepare`, "POST", { filesystem: "ext4", mount_path: value, owner: "root:root", mode: "0755", mount_options: "defaults,nofail", format_if_blank: true });
                  }}>Prepare & mount</button>}
                  {volume.role === "data" && capabilities?.detach && <button type="button" disabled={busy} className="px-2 py-1 text-[10px] border border-border rounded" onClick={() => mutate(`/instance-volumes/${volume.id}/detach`, "POST")}>Detach</button>}
                </div>
              ))}
            </div>
          )}

          {capabilities?.data_volumes ? (
            <form className="space-y-3 rounded p-3" style={{ border: `1px solid ${SUBTLE_BORDER}` }} onSubmit={create}>
              <div className="text-xs text-text font-medium">Add data volume</div>
              <div className="grid grid-cols-2 gap-2">
                <input value={name} onChange={(event) => setName(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono" placeholder="volume name" />
                <input type="number" min={1} value={sizeGB} onChange={(event) => setSizeGB(Number(event.target.value))} className="bg-bg-input border border-border rounded px-2 py-1 text-sm" />
                <select value={tier} onChange={(event) => setTier(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm">
                  {(capabilities.tiers || []).map((candidate) => <option key={candidate.name} value={candidate.name}>{candidate.name}{candidate.provider_type ? ` (${candidate.provider_type})` : ""}</option>)}
                </select>
                <select value={deletePolicy} onChange={(event) => setDeletePolicy(event.target.value as "retain" | "with_instance")} className="bg-bg-input border border-border rounded px-2 py-1 text-sm">
                  <option value="retain">Retain when instance is destroyed</option>
                  <option value="with_instance">Delete with instance</option>
                </select>
              </div>
              {capabilities.guest_prepare && <label className="flex items-center gap-2 text-xs text-text">
                <input type="checkbox" checked={prepareGuest} onChange={(event) => setPrepareGuest(event.target.checked)} />
                Format if blank and mount automatically over SSH
              </label>}
              {prepareGuest && capabilities.guest_prepare && <div className="grid grid-cols-2 gap-2">
                <input value={mountPath} onChange={(event) => setMountPath(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono" placeholder="/srv/media" />
                <select value={filesystem} onChange={(event) => setFilesystem(event.target.value as "ext4" | "xfs")} className="bg-bg-input border border-border rounded px-2 py-1 text-sm">
                  <option value="ext4">ext4</option>
                  <option value="xfs">xfs</option>
                </select>
                <input value={mountOwner} onChange={(event) => setMountOwner(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono" placeholder="owner, e.g. 1000:1000" />
                <span className="text-[10px] text-text-dim self-center">Persistent UUID mount · defaults,nofail · mode 0755</span>
              </div>}
              <div className="flex items-center gap-2">
                <span className="text-[10px] text-text-dim">Device discovery must be unambiguous. Existing filesystems are preserved; unknown signatures are refused.</span>
                <span className="flex-1" />
                <button type="submit" disabled={busy || !name.trim() || sizeGB <= 0 || (prepareGuest && !!capabilities.guest_prepare && !mountPath.trim())} className="px-3 py-1.5 text-xs rounded bg-blue text-white disabled:opacity-50">{busy ? "Working…" : prepareGuest && capabilities.guest_prepare ? "Create, attach & mount" : "Create & attach"}</button>
              </div>
            </form>
          ) : (
            <div className="text-xs text-amber">{capabilities?.notes || `${inst.provider} does not expose attachable data volumes.`}</div>
          )}
        </div>
      </div>
    </div>
  );
}

interface ServerTypeWire {
  name: string;
  description?: string;
  cores: number;
  memory_gb: number;
  disk_gb: number;
  cpu_type?: string;
  architecture?: string;
  platform?: string;
  resource_class?: string;
  accelerators?: Array<{ kind: string; vendor?: string; model?: string; count?: number; memory_gb?: number }>;
  deprecated?: boolean;
  monthly_price_eur?: number;
  hourly_price_eur?: number;
  monthly_price_usd?: number;
  hourly_price_usd?: number;
  available_in?: string[];
  boot_storage?: Array<{ storage_class: "local" | "block" | "network" | "ephemeral"; provider_type?: string; min_size_gb?: number; max_size_gb?: number }>;
	incompatible_images?: string[];
}

interface LocationWire {
  name: string;
  city?: string;
  country?: string;
  description?: string;
  network_zone?: string;
}

interface ImageWire {
  name: string;
  description?: string;
  os_flavor?: string;
  os_version?: string;
  architecture?: string;
  platform?: string;
  resource_class?: string;
  available_in?: string[];
  compatible_types?: string[];
  provider_type?: string;
}

interface ProviderBindingWire {
  provider: string;
  connection_id: number;
  default: boolean;
}

function catalogMonthlyPrice(t?: ServerTypeWire): number {
  return t?.monthly_price_eur ?? t?.monthly_price_usd ?? 0;
}

function formatCatalogMonthlyPrice(t?: ServerTypeWire): string {
  if (!t) return "";
  if (t.monthly_price_eur) return `€${t.monthly_price_eur.toFixed(2)}/mo`;
  if (t.monthly_price_usd) return `$${t.monthly_price_usd.toFixed(2)}/mo`;
  return "";
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
  const [size, setSize] = useState("");
  const [region, setRegion] = useState("");
  const [image, setImage] = useState("");
  const [busy, setBusy] = useState(false);
  const [providers, setProviders] = useState<ProviderBindingWire[]>([]);
  const [provider, setProvider] = useState("");
  const [providerConnectionID, setProviderConnectionID] = useState(0);
  const [providersLoading, setProvidersLoading] = useState(true);
  // Live catalog from the selected bound provider.
  // Empty arrays mean either still-loading or the provider isn't
  // bound; catalogError carries the failure message in the latter
  // case so the operator sees what to fix instead of an empty form.
  const [serverTypes, setServerTypes] = useState<ServerTypeWire[]>([]);
  const [locations, setLocations] = useState<LocationWire[]>([]);
  const [images, setImages] = useState<ImageWire[]>([]);
  const [catalogProvider, setCatalogProvider] = useState("");
  const [catalogLoading, setCatalogLoading] = useState(true);
  const [catalogError, setCatalogError] = useState<string | null>(null);
  const [storageCapabilities, setStorageCapabilities] = useState<StorageCapabilitiesWire | null>(null);
  const [customBootStorage, setCustomBootStorage] = useState(false);
  const [bootSizeGB, setBootSizeGB] = useState(80);
  const [bootStorageClass, setBootStorageClass] = useState("block");
  const [bootTier, setBootTier] = useState("balanced");
	const [elasticRAID, setElasticRAID] = useState("");

  const selectedType = serverTypes.find((candidate) => candidate.name === size);

  const bootStorageOptions = selectedType?.boot_storage?.length
    ? selectedType.boot_storage
    : (storageCapabilities?.storage_classes || []).map((storageClass) => ({ storage_class: storageClass }));
  const selectedBootConstraint = bootStorageOptions.find((candidate) => candidate.storage_class === bootStorageClass);
  const compatibleBootTiers = (storageCapabilities?.tiers || []).filter((candidate) =>
    !candidate.storage_class || candidate.storage_class === bootStorageClass
  );
  const compatibleLocations = locations.filter((candidate) =>
    !selectedType?.available_in?.length || selectedType.available_in.includes(candidate.name)
  );
  const compatibleImages = images.filter((candidate) => {
    if (selectedType?.resource_class && candidate.resource_class && candidate.resource_class !== selectedType.resource_class) return false;
    if (selectedType?.platform && candidate.platform && candidate.platform !== selectedType.platform) return false;
    if (candidate.available_in?.length && region && !candidate.available_in.includes(region)) return false;
    if (candidate.compatible_types?.length && size && !candidate.compatible_types.includes(size)) return false;
	if (selectedType?.incompatible_images?.includes(candidate.name)) return false;
    if (provider === "scaleway" && customBootStorage && candidate.provider_type) {
      const expectedType = bootStorageClass === "local" ? "l_ssd" : "sbs_volume";
      if (candidate.provider_type !== expectedType) return false;
    }
    return true;
  });

  useEffect(() => {
    if (!customBootStorage || bootStorageOptions.length === 0) return;
    const nextClass = bootStorageOptions.some((candidate) => candidate.storage_class === bootStorageClass)
      ? bootStorageClass
      : bootStorageOptions[0].storage_class;
    if (nextClass !== bootStorageClass) {
      setBootStorageClass(nextClass);
      return;
    }
    const constraint = bootStorageOptions.find((candidate) => candidate.storage_class === nextClass);
    if (constraint?.max_size_gb && bootSizeGB > constraint.max_size_gb) setBootSizeGB(constraint.max_size_gb);
    if (constraint?.min_size_gb && bootSizeGB < constraint.min_size_gb) setBootSizeGB(constraint.min_size_gb);
    const tiers = (storageCapabilities?.tiers || []).filter((candidate) => !candidate.storage_class || candidate.storage_class === nextClass);
    if (tiers.length && !tiers.some((candidate) => candidate.name === bootTier)) setBootTier(tiers[0].name);
  }, [customBootStorage, size, bootStorageClass, bootSizeGB, bootTier, storageCapabilities]);

  useEffect(() => {
    if (!selectedType) return;
    const nextRegion = compatibleLocations.some((candidate) => candidate.name === region)
      ? region
      : compatibleLocations[0]?.name || "";
    if (nextRegion !== region) setRegion(nextRegion);
  }, [size, selectedType, compatibleLocations, region]);

  useEffect(() => {
    const preferred = compatibleImages.find((candidate) =>
      candidate.os_flavor === "ubuntu" && candidate.os_version?.endsWith(".04")
    );
    const nextImage = compatibleImages.some((candidate) => candidate.name === image)
      ? image
      : preferred?.name || compatibleImages[0]?.name || "";
    if (nextImage !== image) setImage(nextImage);
  }, [size, region, compatibleImages, image]);

  useEffect(() => {
    (async () => {
      try {
        const r = await fetch(`${API}/instances-providers?${withParams()}`, { credentials: "same-origin" });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        const body = await r.json();
        const available = ((body.providers || []) as ProviderBindingWire[]).sort((a, b) => a.provider.localeCompare(b.provider) || a.connection_id - b.connection_id);
        const selected = available.find((binding) => binding.default) || available[0];
        setProviders(available);
        setProvider(selected?.provider || body.default || "");
        setProviderConnectionID(selected?.connection_id || 0);
      } catch (e) {
        setCatalogError((e as Error).message);
        setCatalogLoading(false);
      } finally {
        setProvidersLoading(false);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!provider) {
      if (!providersLoading) {
        setCatalogError("No VPS provider is bound to this Instances install.");
        setCatalogLoading(false);
      }
      return;
    }
    (async () => {
      setCatalogLoading(true);
      setCatalogError(null);
      setServerTypes([]);
      setLocations([]);
      setImages([]);
      setStorageCapabilities(null);
      setCustomBootStorage(false);
      setSize("");
      setRegion("");
      setImage("");
      const params = new URLSearchParams(withParams());
      params.set("provider", provider);
      if (providerConnectionID) params.set("provider_connection_id", String(providerConnectionID));
      const qs = params.toString();
      try {
        const [stRes, locRes, imgRes, storageRes] = await Promise.all([
          fetch(`${API}/instances-server-types?${qs}`, { credentials: "same-origin" }),
          fetch(`${API}/instances-locations?${qs}`, { credentials: "same-origin" }),
          fetch(`${API}/instances-images?${qs}`, { credentials: "same-origin" }),
          fetch(`${API}/instances-storage-capabilities?${qs}`, { credentials: "same-origin" }),
        ]);
        if (!stRes.ok) throw new Error(`server_types: ${stRes.status} ${await stRes.text().catch(() => "")}`);
        if (!locRes.ok) throw new Error(`locations: ${locRes.status} ${await locRes.text().catch(() => "")}`);
        if (!imgRes.ok) throw new Error(`images: ${imgRes.status} ${await imgRes.text().catch(() => "")}`);
        if (!storageRes.ok) throw new Error(`storage: ${storageRes.status} ${await storageRes.text().catch(() => "")}`);
        const stJson = await stRes.json();
        const locJson = await locRes.json();
        const imgJson = await imgRes.json();
        const storageJson = await storageRes.json();
        const nextStorageCapabilities = storageJson.capabilities as StorageCapabilitiesWire;
        setStorageCapabilities(nextStorageCapabilities);
        const firstClass = nextStorageCapabilities.storage_classes?.[0] || "block";
        setBootStorageClass(firstClass);
        setBootTier(nextStorageCapabilities.tiers?.find((candidate) => !candidate.storage_class || candidate.storage_class === firstClass)?.name || "balanced");
        setCatalogProvider(stJson.provider || locJson.provider || imgJson.provider || "");
        // Hide deprecated server types from the default view —
        // they still come back in the API for completeness but
        // operators shouldn't pick them for a new server.
        const types: ServerTypeWire[] = (stJson.server_types || []).filter((t: ServerTypeWire) => !t.deprecated);
        const locs: LocationWire[] = locJson.locations || [];
        const imgs: ImageWire[] = [];
        const seenImages = new Set<string>();
        for (const img of (imgJson.images || []) as ImageWire[]) {
          if (!img.name || seenImages.has(img.name)) continue;
          seenImages.add(img.name);
          imgs.push(img);
        }
        // Stable, predictable orderings. Price for sizes (cheapest
        // first), alphabetical for locations + images.
        types.sort((a, b) => catalogMonthlyPrice(a) - catalogMonthlyPrice(b));
        locs.sort((a, b) => a.name.localeCompare(b.name));
        imgs.sort((a, b) => a.name.localeCompare(b.name));
        setServerTypes(types);
        setLocations(locs);
        setImages(imgs);
        // Sensible defaults: cheapest size, first location
        // alphabetically, ubuntu LTS if present otherwise first image.
        if (types.length) setSize(types[0].name);
        if (locs.length) setRegion(locs[0].name);
      } catch (e) {
        setCatalogError((e as Error).message);
      } finally {
        setCatalogLoading(false);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider, providerConnectionID, providersLoading]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/instances?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: name.trim(), provider, provider_connection_id: providerConnectionID, size, region, image,
          storage: customBootStorage ? { boot: { size_gb: bootSizeGB, storage_class: bootStorageClass, tier: bootTier, delete_policy: "with_instance" } } : undefined,
		  elastic_metal: size.startsWith("elastic-metal/") && elasticRAID ? { raid_level: elasticRAID } : undefined,
        }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      onCreated();
    } catch (e) {
      setError("Provision failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  // Compact spec/price suffix shown next to a server-type's name in
  // the dropdown. Falls through to whatever fields are present.
  const sizeLabel = (t: ServerTypeWire): string => {
    const parts: string[] = [];
    if (t.cores) parts.push(`${t.cores} ${t.cpu_type === "dedicated" ? "vCPU dedicated" : "vCPU"}`);
    if (t.memory_gb) parts.push(`${t.memory_gb} GB`);
    if (t.disk_gb) parts.push(`${t.disk_gb} GB disk`);
    if (t.architecture && t.architecture !== "x86") parts.push(t.architecture.toUpperCase());
    const specs = parts.join(", ");
    const price = formatCatalogMonthlyPrice(t);
    const displayName = t.name.startsWith("dedibox/")
      ? (t.description?.split(" — ")[0] || t.name.replace(/^dedibox\//, "Dedibox "))
	  : t.name.startsWith("elastic-metal/")
		? (t.description?.split(" — ")[0] || t.name.replace(/^elastic-metal\//, "Elastic Metal "))
      : t.name.replace(/^apple-silicon\//, "");
    const hostKind = t.name.startsWith("elastic-metal/")
	  ? "on-demand dedicated server"
	  : t.name.startsWith("dedibox/")
      ? "dedicated physical server"
      : t.resource_class === "bare_metal" ? "Mac mini bare metal" : "";
    return [displayName, hostKind && `· ${hostKind}`, price && `(${price}`, specs && (price ? `, ${specs})` : `(${specs})`)]
      .filter(Boolean)
      .join(" ");
  };

  const locLabel = (l: LocationWire): string => {
    const place = [l.city, l.country].filter(Boolean).join(", ");
    return place ? `${place} (${l.name})` : l.name;
  };

  const imageLabel = (i: ImageWire): string => {
    const label = i.description && i.description !== i.name
      ? `${i.description} (${i.name})`
      : i.name;
    const details = [i.architecture, i.os_flavor && i.os_version ? `${i.os_flavor} ${i.os_version}` : "", i.provider_type]
      .filter(Boolean)
      .join(", ");
    return details ? `${label} — ${details}` : label;
  };

  return (
    <div
      className="fixed inset-0 z-30 flex items-center justify-center bg-black/50"
      style={{ padding: 24 }}
      onClick={onClose}
    >
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        className="bg-bg border border-border rounded p-5 space-y-4"
        style={{ width: "min(480px, 100%)" }}
      >
        <h2 className="text-text font-semibold">Provision a new instance</h2>
        {catalogError ? (
          <p className="text-xs text-amber">
            Couldn't load the selected provider catalog: {catalogError}. Enter provider-specific
            size, region, and image values manually.
          </p>
        ) : catalogLoading ? (
          <p className="text-xs text-text-muted">Loading server types, regions, and images from the selected provider…</p>
        ) : (
          <p className="text-xs text-text-muted">
            Live from {catalogProvider || provider}: {serverTypes.length} types · {locations.length} regions · {images.length} images.
          </p>
        )}
        <div>
          <label className="text-xs text-text-muted block mb-1">Provider</label>
          <select
            value={providerConnectionID || ""}
            onChange={(e) => {
              const connectionID = Number(e.target.value);
              const binding = providers.find((candidate) => candidate.connection_id === connectionID);
              setProviderConnectionID(connectionID);
              setProvider(binding?.provider || "");
            }}
            disabled={providersLoading || providers.length === 0}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
          >
            {providers.length === 0 && <option value="">No provider bound</option>}
            {providers.map((binding) => (
              <option key={binding.connection_id} value={binding.connection_id}>
                {binding.provider} · connection {binding.connection_id}{binding.default ? " (default)" : ""}
              </option>
            ))}
          </select>
        </div>
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
        <div className="space-y-2">
          <div>
            <label className="text-xs text-text-muted block mb-1">Size</label>
            {catalogError ? (
              <input
                type="text"
                value={size}
                onChange={(e) => setSize(e.target.value)}
                placeholder="provider size"
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
              />
            ) : (
              <select
                value={size}
                onChange={(e) => setSize(e.target.value)}
                disabled={catalogLoading}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
              >
                {serverTypes.length === 0 && <option value="">—</option>}
                {serverTypes.map((t) => (
                  <option key={t.name} value={t.name}>{sizeLabel(t)}</option>
                ))}
              </select>
            )}
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-text-muted block mb-1">Region</label>
              {catalogError ? (
                <input
                  type="text"
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  placeholder="provider region"
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              ) : (
                <select
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  disabled={catalogLoading}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
                >
                  {locations.length === 0 && <option value="">—</option>}
                  {compatibleLocations.map((l) => (
                    <option key={l.name} value={l.name}>{locLabel(l)}</option>
                  ))}
                </select>
              )}
            </div>
            <div>
              <label className="text-xs text-text-muted block mb-1">Image</label>
              {catalogError ? (
                <input
                  type="text"
                  value={image}
                  onChange={(e) => setImage(e.target.value)}
                  placeholder="provider image"
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
                />
              ) : (
                <select
                  value={image}
                  onChange={(e) => setImage(e.target.value)}
                  disabled={catalogLoading}
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
                >
                  {compatibleImages.length === 0 && <option value="">—</option>}
                  {compatibleImages.map((i) => (
                    <option key={i.name} value={i.name}>{imageLabel(i)}</option>
                  ))}
                </select>
              )}
            </div>
          </div>
        </div>
        {storageCapabilities?.boot_size_configurable && !size.startsWith("elastic-metal/") && (
          <div className="rounded p-3 space-y-2" style={{ border: `1px solid ${SUBTLE_BORDER}` }}>
            <label className="flex items-center gap-2 text-xs text-text-muted">
              <input type="checkbox" checked={customBootStorage} onChange={(event) => setCustomBootStorage(event.target.checked)} />
              <span className="text-text">Custom boot volume</span>
              <span>— otherwise the provider/image default is used</span>
            </label>
            {customBootStorage && (
              <div className="grid grid-cols-2 gap-2">
                <select value={bootStorageClass} onChange={(event) => setBootStorageClass(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm" aria-label="Boot storage class">
                  {bootStorageOptions.map((candidate) => <option key={candidate.storage_class} value={candidate.storage_class}>{candidate.storage_class}{candidate.max_size_gb ? ` (up to ${candidate.max_size_gb} GB)` : ""}</option>)}
                </select>
                <input type="number" min={selectedBootConstraint?.min_size_gb || 1} max={selectedBootConstraint?.max_size_gb || undefined} value={bootSizeGB} onChange={(event) => setBootSizeGB(Number(event.target.value))} className="bg-bg-input border border-border rounded px-2 py-1 text-sm" aria-label="Boot volume size in GB" />
                <select value={bootTier} onChange={(event) => setBootTier(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-sm">
                  {compatibleBootTiers.map((candidate) => <option key={candidate.name} value={candidate.name}>{candidate.name}{candidate.provider_type ? ` (${candidate.provider_type})` : ""}</option>)}
                </select>
                <span className="text-[10px] text-text-dim self-center">Boot · {bootStorageClass} · {bootSizeGB} GB · deleted with the instance</span>
              </div>
            )}
          </div>
        )}
		{size.startsWith("elastic-metal/") && (
		  <div className="rounded p-3 space-y-2" style={{ border: `1px solid ${SUBTLE_BORDER}` }}>
			<div className="text-xs text-text">Elastic Metal local disks</div>
			<p className="text-[10px] text-text-dim">The selected offer's disks are included. Optionally apply RAID to Scaleway's default partitioning schema; the provider validates it before creation.</p>
			<select value={elasticRAID} onChange={(event) => setElasticRAID(event.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm">
			  <option value="">Provider default partitioning</option>
			  <option value="raid_level_0">RAID 0</option><option value="raid_level_1">RAID 1</option><option value="raid_level_5">RAID 5</option><option value="raid_level_6">RAID 6</option><option value="raid_level_10">RAID 10</option>
			</select>
		  </div>
		)}
        <div className="flex justify-end gap-2 pt-1">
          {selectedType?.resource_class === "bare_metal" && selectedType.platform === "macos" && (
            <span className="mr-auto text-[11px] text-amber">24-hour minimum allocation</span>
          )}
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1.5 text-sm rounded border border-border text-text-muted hover:text-text disabled:opacity-50"
          >Cancel</button>
          <button
            type="submit"
            disabled={busy || !name.trim() || !provider || catalogLoading || !size || !region || !image}
            className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
          >{busy ? "Provisioning…" : "Provision"}</button>
        </div>
      </form>
    </div>
  );
}
