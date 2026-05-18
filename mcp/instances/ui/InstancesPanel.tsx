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

interface ServerTypeWire {
  name: string;
  description?: string;
  cores: number;
  memory_gb: number;
  disk_gb: number;
  cpu_type?: string;
  architecture?: string;
  deprecated?: boolean;
  monthly_price_eur?: number;
  hourly_price_eur?: number;
  available_in?: string[];
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
  // Live catalog from the bound provider — populated on mount via
  // the new /api/instances-server-types|locations|images routes.
  // Empty arrays mean either still-loading or the provider isn't
  // bound; catalogError carries the failure message in the latter
  // case so the operator sees what to fix instead of an empty form.
  const [serverTypes, setServerTypes] = useState<ServerTypeWire[]>([]);
  const [locations, setLocations] = useState<LocationWire[]>([]);
  const [images, setImages] = useState<ImageWire[]>([]);
  const [catalogLoading, setCatalogLoading] = useState(true);
  const [catalogError, setCatalogError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      setCatalogLoading(true);
      setCatalogError(null);
      const qs = withParams();
      try {
        const [stRes, locRes, imgRes] = await Promise.all([
          fetch(`${API}/instances-server-types?${qs}`, { credentials: "same-origin" }),
          fetch(`${API}/instances-locations?${qs}`, { credentials: "same-origin" }),
          fetch(`${API}/instances-images?${qs}`, { credentials: "same-origin" }),
        ]);
        if (!stRes.ok) throw new Error(`server_types: ${stRes.status} ${await stRes.text().catch(() => "")}`);
        if (!locRes.ok) throw new Error(`locations: ${locRes.status} ${await locRes.text().catch(() => "")}`);
        if (!imgRes.ok) throw new Error(`images: ${imgRes.status} ${await imgRes.text().catch(() => "")}`);
        const stJson = await stRes.json();
        const locJson = await locRes.json();
        const imgJson = await imgRes.json();
        // Hide deprecated server types from the default view —
        // they still come back in the API for completeness but
        // operators shouldn't pick them for a new server.
        const types: ServerTypeWire[] = (stJson.server_types || []).filter((t: ServerTypeWire) => !t.deprecated);
        const locs: LocationWire[] = locJson.locations || [];
        const imgs: ImageWire[] = imgJson.images || [];
        // Stable, predictable orderings. Price for sizes (cheapest
        // first), alphabetical for locations + images.
        types.sort((a, b) => (a.monthly_price_eur ?? 0) - (b.monthly_price_eur ?? 0));
        locs.sort((a, b) => a.name.localeCompare(b.name));
        imgs.sort((a, b) => a.name.localeCompare(b.name));
        setServerTypes(types);
        setLocations(locs);
        setImages(imgs);
        // Sensible defaults: cheapest size, first location
        // alphabetically, ubuntu LTS if present otherwise first image.
        if (types.length && !size) setSize(types[0].name);
        if (locs.length && !region) setRegion(locs[0].name);
        if (imgs.length && !image) {
          const ubuntu = imgs.find((i) => i.os_flavor === "ubuntu" && i.os_version?.endsWith(".04"));
          setImage(ubuntu?.name || imgs[0].name);
        }
      } catch (e) {
        setCatalogError((e as Error).message);
      } finally {
        setCatalogLoading(false);
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/instances?${withParams()}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.trim(), provider: "hetzner", size, region, image }),
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
    const price = t.monthly_price_eur ? `€${t.monthly_price_eur.toFixed(2)}/mo` : "";
    return [t.name, price && `(${price}`, specs && (price ? `, ${specs})` : `(${specs})`)]
      .filter(Boolean)
      .join(" ");
  };

  const locLabel = (l: LocationWire): string => {
    const place = [l.city, l.country].filter(Boolean).join(", ");
    return place ? `${place} (${l.name})` : l.name;
  };

  return (
    <div className="fixed inset-0 z-30 flex items-center justify-center bg-black/50" onClick={onClose}>
      <form
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        className="w-[480px] bg-bg border border-border rounded p-5 space-y-4"
      >
        <h2 className="text-text font-semibold">Provision a new instance</h2>
        {catalogError ? (
          <p className="text-xs text-red-500">
            Couldn't load provider catalog: {catalogError}. Bind a Hetzner connection on this
            install (Integrations → Add → Hetzner Cloud), then reopen this dialog.
          </p>
        ) : catalogLoading ? (
          <p className="text-xs text-text-muted">Loading server types, regions, and images from Hetzner…</p>
        ) : (
          <p className="text-xs text-text-muted">
            Live from Hetzner: {serverTypes.length} types · {locations.length} regions · {images.length} images.
          </p>
        )}
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
            <select
              value={size}
              onChange={(e) => setSize(e.target.value)}
              disabled={catalogLoading || !!catalogError}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
            >
              {serverTypes.length === 0 && <option value="">—</option>}
              {serverTypes.map((t) => (
                <option key={t.name} value={t.name}>{sizeLabel(t)}</option>
              ))}
            </select>
          </div>
          <div className="grid grid-cols-2 gap-2">
            <div>
              <label className="text-xs text-text-muted block mb-1">Region</label>
              <select
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                disabled={catalogLoading || !!catalogError}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
              >
                {locations.length === 0 && <option value="">—</option>}
                {locations.map((l) => (
                  <option key={l.name} value={l.name}>{locLabel(l)}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-xs text-text-muted block mb-1">Image</label>
              <select
                value={image}
                onChange={(e) => setImage(e.target.value)}
                disabled={catalogLoading || !!catalogError}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm disabled:opacity-50"
              >
                {images.length === 0 && <option value="">—</option>}
                {images.map((i) => (
                  <option key={i.name} value={i.name}>{i.description || i.name}</option>
                ))}
              </select>
            </div>
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
            disabled={busy || !name.trim() || catalogLoading || !!catalogError || !size || !region}
            className="px-3 py-1.5 text-sm rounded bg-blue text-white hover:bg-blue/90 disabled:opacity-50"
          >{busy ? "Provisioning…" : "Provision"}</button>
        </div>
      </form>
    </div>
  );
}
