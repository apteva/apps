import { useCallback, useEffect, useMemo, useState, type CSSProperties } from "react";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface Workload {
  id: string;
  name: string;
  image: string;
  host_id: number;
  instance_id: number;
  status: string;
  desired_status: string;
  health_status: string;
  public_url: string;
  last_error: string;
  created_at: string;
  updated_at?: string;
  resources?: { memory_mb?: number; cpu?: number };
  ports?: Array<{ container_port: number; host_port: number; bind_addr: string; protocol: string }>;
  volumes?: Array<{ name: string; docker_volume_name: string; mount_path: string }>;
}

interface Blueprint {
  slug: string;
  name: string;
  description: string;
}

interface HostOption {
  id: number;
  name: string;
  provider: string;
  status: string;
  public_ipv4?: string;
  label?: string;
  default?: boolean;
}

const API = "/api/apps/containers/api";

const TEST_IMAGES = [
  { slug: "nginx", label: "Nginx", image: "nginx:alpine", containerPort: "80", healthPath: "/", memoryMB: "256", cpu: "0.5" },
  { slug: "whoami", label: "Whoami", image: "traefik/whoami:v1.10", containerPort: "80", healthPath: "/", memoryMB: "128", cpu: "0.25" },
  { slug: "httpd", label: "Apache", image: "httpd:alpine", containerPort: "80", healthPath: "/", memoryMB: "256", cpu: "0.5" },
  { slug: "adminer", label: "Adminer", image: "adminer:latest", containerPort: "8080", healthPath: "/", memoryMB: "256", cpu: "0.5" },
] as const;

function imageName(image: string): string {
  const withoutTag = image.split("@")[0].split(":")[0] || "container";
  const base = withoutTag.split("/").pop() || "container";
  return base.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "") || "container";
}

function autoName(image: string): string {
  return `test-${imageName(image)}-${Math.floor(Date.now() / 1000).toString(36)}`;
}

function statusColor(status: string): string {
  switch (status) {
    case "running":
      return "var(--success, #22c55e)";
    case "creating":
      return "var(--accent, #38bdf8)";
    case "stopped":
      return "var(--warn, #f59e0b)";
    case "unhealthy":
    case "error":
      return "var(--error, #ef4444)";
    default:
      return "var(--text-dim, #7a7a7a)";
  }
}

function fmtDate(s: string): string {
  if (!s) return "";
  const d = new Date(s);
  if (!Number.isFinite(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function portLabel(w: Workload): string {
  const p = w.ports?.[0];
  if (!p) return "No ports";
  return `${p.bind_addr}:${p.host_port} -> ${p.container_port}/${p.protocol}`;
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
  return (await res.json()) as T;
}

export default function ContainersPanel(_props: NativePanelProps) {
  const [workloads, setWorkloads] = useState<Workload[]>([]);
  const [blueprints, setBlueprints] = useState<Blueprint[]>([]);
  const [hosts, setHosts] = useState<HostOption[]>([{ id: 0, name: "localhost", provider: "local", status: "ready", label: "Local Docker host" }]);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("Loading containers...");
  const [busy, setBusy] = useState("");
  const [logs, setLogs] = useState<{ name: string; body: string } | null>(null);
  const [pendingDestroy, setPendingDestroy] = useState<{ id: string; name: string } | null>(null);
  const [form, setForm] = useState({
    name: "test-nginx",
    image: "nginx:alpine",
    containerPort: "80",
    hostPort: "",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5",
    hostId: "0",
  });

  const runningCount = useMemo(() => workloads.filter((w) => w.status === "running").length, [workloads]);
  const hostLabels = useMemo(() => {
    const labels = new Map<number, string>();
    for (const host of hosts) labels.set(host.id, host.label || host.name || `Host ${host.id}`);
    labels.set(0, labels.get(0) || "Local Docker host");
    return labels;
  }, [hosts]);

  const loadHosts = useCallback(async () => {
    try {
      const res = await api<{ hosts: HostOption[]; default_host_id?: number; warning?: string }>("/hosts");
      const rows = res.hosts?.length ? res.hosts : [{ id: 0, name: "localhost", provider: "local", status: "ready", label: "Local Docker host" }];
      setHosts(rows);
      const defaultID = res.default_host_id ?? rows.find((h) => h.default)?.id ?? 0;
      setForm((f) => ({ ...f, hostId: String(defaultID) }));
    } catch {
      setHosts([{ id: 0, name: "localhost", provider: "local", status: "ready", label: "Local Docker host" }]);
    }
  }, []);

  const load = useCallback(async () => {
    try {
      const [w, b] = await Promise.all([
        api<{ workloads: Workload[]; count?: number }>("/workloads"),
        api<{ blueprints: Blueprint[] }>("/blueprints"),
      ]);
      const rows = (w.workloads || []).filter((row) => row.status !== "destroyed");
      setWorkloads(rows);
      setBlueprints(b.blueprints || []);
      setError("");
      setStatus(`${rows.length} tracked, ${rows.filter((row) => row.status === "running").length} running`);
    } catch (e) {
      setError((e as Error).message);
      setStatus("Load failed");
    }
  }, []);

  useEffect(() => {
    loadHosts();
    load();
    const timer = window.setInterval(load, 5000);
    return () => window.clearInterval(timer);
  }, [load, loadHosts]);

  const runSpec = useCallback(async (next: typeof form) => {
    setBusy("run");
    setError("");
    setStatus("Starting container...");
    try {
      const name = next.name.trim() || autoName(next.image);
      const ports = next.containerPort
        ? [{
            container_port: Number(next.containerPort),
            host_port: next.hostPort ? Number(next.hostPort) : 0,
            protocol: "tcp",
          }]
        : [];
      const instanceID = Number(next.hostId || 0);
      await api("/workloads", {
        method: "POST",
        body: JSON.stringify({
          name,
          image: next.image.trim(),
          instance_id: Number.isFinite(instanceID) && instanceID > 0 ? instanceID : 0,
          ports,
          health_path: next.healthPath || "/",
          resources: {
            memory_mb: Number(next.memoryMB || 0),
            cpu: Number(next.cpu || 0),
          },
        }),
      });
      setStatus(`Queued ${name}. Refreshing list...`);
      setForm((f) => ({ ...f, name: autoName(f.image) }));
      await load();
    } catch (e) {
      setError((e as Error).message);
      setStatus("Start failed");
    } finally {
      setBusy("");
    }
  }, [load]);

  const action = useCallback(async (id: string, act: "start" | "stop" | "restart" | "health") => {
    setBusy(`${act}:${id}`);
    setError("");
    try {
      await api(`/workloads/${encodeURIComponent(id)}/${act}`, { method: "POST" });
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [load]);

  const destroy = useCallback(async (id: string) => {
    setBusy(`destroy:${id}`);
    setError("");
    try {
      await api(`/workloads/${encodeURIComponent(id)}`, { method: "DELETE" });
      setPendingDestroy(null);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [load]);

  const showLogs = useCallback(async (w: Workload) => {
    setBusy(`logs:${w.id}`);
    setError("");
    try {
      const res = await api<{ logs: string }>(`/workloads/${encodeURIComponent(w.id)}/logs?tail=300`);
      setLogs({ name: w.name, body: res.logs || "" });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy("");
    }
  }, []);

  const fillTestImage = (preset: typeof TEST_IMAGES[number]) => {
    setForm({
      name: autoName(preset.image),
      image: preset.image,
      containerPort: preset.containerPort,
      hostPort: "",
      healthPath: preset.healthPath,
      memoryMB: preset.memoryMB,
      cpu: preset.cpu,
      hostId: form.hostId,
    });
  };

  return (
    <div style={styles.root}>
      <header style={styles.header}>
        <div>
          <h1 style={styles.title}>Containers</h1>
          <div style={styles.subtitle}>Local Docker workloads</div>
        </div>
        <div style={styles.headerRight}>
          <span style={styles.statusText}>{status}</span>
          <button type="button" style={styles.button} onClick={load} disabled={!!busy}>Refresh</button>
        </div>
      </header>

      {error && <div style={styles.error}>{error}</div>}

      <main style={styles.main}>
        <section style={styles.workloads}>
          <div style={styles.sectionHeader}>
            <div>
              <h2 style={styles.sectionTitle}>Workloads</h2>
              <div style={styles.muted}>{workloads.length} tracked containers, {runningCount} running</div>
            </div>
          </div>

          {workloads.length === 0 ? (
            <div style={styles.empty}>No containers are tracked yet. Run nginx or whoami from the quick tests.</div>
          ) : (
            <div style={styles.list}>
              {workloads.map((w) => (
                <article key={w.id} style={styles.row}>
                  <div style={styles.rowTop}>
                    <div style={styles.rowMain}>
                      <div style={styles.nameLine}>
                        <span style={styles.workloadName}>{w.name}</span>
                        <span style={{ ...styles.pill, color: statusColor(w.status), borderColor: statusColor(w.status) }}>{w.status}</span>
                        <span style={styles.pill}>{w.health_status || "unknown"}</span>
                      </div>
                      <div style={styles.image}>{w.image}</div>
                    </div>
                    <div style={styles.actions}>
                      {w.public_url && <a style={styles.primaryLink} href={w.public_url} target="_blank" rel="noreferrer">Open</a>}
                      <button type="button" style={styles.button} disabled={!!busy} onClick={() => action(w.id, "restart")}>Restart</button>
                      <button type="button" style={styles.button} disabled={!!busy} onClick={() => showLogs(w)}>Logs</button>
                    </div>
                  </div>

                  <div style={styles.metaGrid}>
                    <Meta label="URL" value={w.public_url || "-"} />
                    <Meta label="Host" value={hostLabels.get(w.instance_id || w.host_id || 0) || `Host ${w.instance_id || w.host_id || 0}`} />
                    <Meta label="Port" value={portLabel(w)} />
                    <Meta label="Created" value={fmtDate(w.created_at)} />
                    <Meta label="Resources" value={`${w.resources?.memory_mb || 0} MB / ${w.resources?.cpu || 0} CPU`} />
                  </div>

                  {w.last_error && <div style={styles.errorSmall}>{w.last_error}</div>}

                  <div style={styles.secondaryActions}>
                    <button type="button" style={styles.linkButton} disabled={!!busy} onClick={() => action(w.id, "start")}>Start</button>
                    <button type="button" style={styles.linkButton} disabled={!!busy} onClick={() => action(w.id, "stop")}>Stop</button>
                    <button type="button" style={styles.linkButton} disabled={!!busy} onClick={() => action(w.id, "health")}>Health check</button>
                    <button type="button" style={{ ...styles.linkButton, color: "var(--error, #ef4444)" }} disabled={!!busy} onClick={() => setPendingDestroy({ id: w.id, name: w.name })}>Destroy</button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>

        <aside style={styles.side}>
          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Quick Tests</h2>
            <div style={styles.quickGrid}>
              {TEST_IMAGES.map((preset) => (
                <div key={preset.slug} style={styles.quickCard}>
                  <div>
                    <div style={styles.quickTitle}>{preset.label}</div>
                    <div style={styles.image}>{preset.image}</div>
                    <div style={styles.muted}>{preset.containerPort}/tcp</div>
                  </div>
                  <div style={styles.quickActions}>
                    <button type="button" style={styles.button} disabled={!!busy} onClick={() => fillTestImage(preset)}>Fill</button>
                    <button type="button" style={styles.primaryButton} disabled={!!busy} onClick={() => runSpec({
                      name: autoName(preset.image),
                      image: preset.image,
                      containerPort: preset.containerPort,
                      hostPort: "",
                      healthPath: preset.healthPath,
                      memoryMB: preset.memoryMB,
                      cpu: preset.cpu,
                      hostId: form.hostId,
                    })}>Run</button>
                  </div>
                </div>
              ))}
            </div>
          </section>

          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Run Image</h2>
            <div style={styles.form}>
              <input style={styles.input} placeholder="name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
              <select style={styles.input} value={form.hostId} onChange={(e) => setForm({ ...form, hostId: e.target.value })}>
                {hosts.map((host) => (
                  <option key={host.id} value={String(host.id)}>
                    {host.label || host.name}{host.status !== "ready" ? ` · ${host.status}` : ""}
                  </option>
                ))}
              </select>
              <input style={styles.input} placeholder="image, e.g. nginx:alpine" value={form.image} onChange={(e) => setForm({ ...form, image: e.target.value })} />
              <div style={styles.twoCols}>
                <input style={styles.input} placeholder="container port" value={form.containerPort} onChange={(e) => setForm({ ...form, containerPort: e.target.value })} />
                <input style={styles.input} placeholder="host port auto" value={form.hostPort} onChange={(e) => setForm({ ...form, hostPort: e.target.value })} />
              </div>
              <div style={styles.threeCols}>
                <input style={styles.input} placeholder="/health" value={form.healthPath} onChange={(e) => setForm({ ...form, healthPath: e.target.value })} />
                <input style={styles.input} placeholder="MB" value={form.memoryMB} onChange={(e) => setForm({ ...form, memoryMB: e.target.value })} />
                <input style={styles.input} placeholder="CPU" value={form.cpu} onChange={(e) => setForm({ ...form, cpu: e.target.value })} />
              </div>
              <button type="button" style={styles.primaryButtonWide} onClick={() => runSpec(form)} disabled={busy === "run" || !form.image.trim()}>
                {busy === "run" ? "Starting..." : "Run container"}
              </button>
            </div>
          </section>

          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Blueprints</h2>
            <div style={styles.blueprints}>
              {blueprints.map((b) => (
                <div key={b.slug} style={styles.blueprint}>
                  <div style={styles.quickTitle}>{b.name}</div>
                  <div style={styles.muted}>{b.description}</div>
                </div>
              ))}
            </div>
          </section>
        </aside>
      </main>

      {logs && (
        <div style={styles.modalBackdrop} onClick={() => setLogs(null)}>
          <div style={styles.modal} onClick={(e) => e.stopPropagation()}>
            <div style={styles.modalHeader}>
              <div style={styles.sectionTitle}>Logs: {logs.name}</div>
              <button type="button" style={styles.button} onClick={() => setLogs(null)}>Close</button>
            </div>
            <pre style={styles.pre}>{logs.body || "(no logs)"}</pre>
          </div>
        </div>
      )}

      {pendingDestroy && (
        <div style={styles.confirmBackdrop} onClick={() => !busy && setPendingDestroy(null)}>
          <div style={styles.confirmModal} onClick={(e) => e.stopPropagation()}>
            <div style={styles.modalHeader}>
              <div style={styles.sectionTitle}>Destroy container</div>
              <button type="button" style={styles.button} disabled={!!busy} onClick={() => setPendingDestroy(null)}>Close</button>
            </div>
            <div style={styles.modalBody}>
              <div style={styles.confirmName}>{pendingDestroy.name}</div>
              <div style={styles.muted}>This removes the Docker container and its network. Docker volumes are preserved.</div>
            </div>
            <div style={styles.modalFooter}>
              <button type="button" style={styles.button} disabled={!!busy} onClick={() => setPendingDestroy(null)}>Cancel</button>
              <button type="button" style={styles.dangerButton} disabled={!!busy} onClick={() => destroy(pendingDestroy.id)}>
                {busy === `destroy:${pendingDestroy.id}` ? "Destroying..." : "Destroy"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

function Meta({ label, value }: { label: string; value: string }) {
  return (
    <div style={styles.metaItem}>
      <div style={styles.metaLabel}>{label}</div>
      <div style={styles.metaValue}>{value}</div>
    </div>
  );
}

const styles: Record<string, CSSProperties> = {
  root: { height: "100%", minHeight: 0, display: "flex", flexDirection: "column", color: "var(--text, #e5e7eb)", background: "var(--bg, #0b0b0c)" },
  header: { flexShrink: 0, display: "flex", alignItems: "center", justifyContent: "space-between", gap: 16, padding: "18px 24px", borderBottom: "1px solid var(--border, #2a2a2d)" },
  title: { margin: 0, fontSize: 18, fontWeight: 650 },
  subtitle: { marginTop: 4, fontSize: 12, color: "var(--text-dim, #8a8a8f)" },
  headerRight: { display: "flex", alignItems: "center", gap: 10 },
  statusText: { fontSize: 12, color: "var(--text-muted, #a1a1aa)" },
  main: { flex: 1, minHeight: 0, overflow: "auto", padding: 20, display: "grid", gridTemplateColumns: "minmax(520px, 1fr) 380px", gap: 16, alignItems: "start" },
  workloads: { minWidth: 0, border: "1px solid var(--border, #2a2a2d)", borderRadius: 8, background: "var(--bg-card, #111114)", overflow: "hidden" },
  side: { display: "flex", flexDirection: "column", gap: 16, minWidth: 0 },
  sectionHeader: { display: "flex", alignItems: "center", justifyContent: "space-between", padding: "14px 16px", borderBottom: "1px solid var(--border, #2a2a2d)" },
  sectionTitle: { margin: 0, fontSize: 14, fontWeight: 650 },
  muted: { fontSize: 12, color: "var(--text-dim, #8a8a8f)", lineHeight: 1.45 },
  error: { margin: "12px 20px 0", padding: "10px 12px", border: "1px solid rgba(239,68,68,.35)", borderRadius: 6, background: "rgba(239,68,68,.10)", color: "var(--error, #ef4444)", fontSize: 13, whiteSpace: "pre-wrap" },
  errorSmall: { padding: "8px 10px", borderRadius: 6, background: "rgba(239,68,68,.10)", color: "var(--error, #ef4444)", fontSize: 12 },
  empty: { padding: 24, color: "var(--text-muted, #a1a1aa)", fontSize: 14 },
  list: { display: "flex", flexDirection: "column" },
  row: { padding: 16, borderTop: "1px solid var(--border, #2a2a2d)", display: "flex", flexDirection: "column", gap: 12 },
  rowTop: { display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 16 },
  rowMain: { minWidth: 0 },
  nameLine: { display: "flex", alignItems: "center", flexWrap: "wrap", gap: 8 },
  workloadName: { fontSize: 15, fontWeight: 650 },
  image: { marginTop: 4, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace", fontSize: 12, color: "var(--text-muted, #a1a1aa)", overflowWrap: "anywhere" },
  pill: { display: "inline-flex", alignItems: "center", minHeight: 20, padding: "2px 7px", borderRadius: 999, border: "1px solid var(--border, #2a2a2d)", color: "var(--text-muted, #a1a1aa)", fontSize: 11, textTransform: "uppercase" },
  actions: { display: "flex", alignItems: "center", gap: 8, flexShrink: 0 },
  secondaryActions: { display: "flex", flexWrap: "wrap", gap: 12 },
  metaGrid: { display: "grid", gridTemplateColumns: "repeat(4, minmax(0, 1fr))", gap: 8 },
  metaItem: { minWidth: 0, padding: "8px 10px", border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg, #0b0b0c)" },
  metaLabel: { fontSize: 10, textTransform: "uppercase", color: "var(--text-dim, #8a8a8f)", marginBottom: 4 },
  metaValue: { fontSize: 12, color: "var(--text, #e5e7eb)", overflowWrap: "anywhere" },
  card: { border: "1px solid var(--border, #2a2a2d)", borderRadius: 8, background: "var(--bg-card, #111114)", padding: 14 },
  quickGrid: { marginTop: 12, display: "grid", gap: 10 },
  quickCard: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, padding: 10, border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg, #0b0b0c)" },
  quickTitle: { fontSize: 13, fontWeight: 650 },
  quickActions: { display: "flex", gap: 8, flexShrink: 0 },
  form: { marginTop: 12, display: "flex", flexDirection: "column", gap: 10 },
  twoCols: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 },
  threeCols: { display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 8 },
  input: { width: "100%", boxSizing: "border-box", border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg-input, #17171a)", color: "var(--text, #e5e7eb)", padding: "8px 10px", fontSize: 13, outline: "none" },
  button: { border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "transparent", color: "var(--text-muted, #a1a1aa)", padding: "6px 10px", fontSize: 12, cursor: "pointer" },
  primaryButton: { border: "1px solid var(--accent, #38bdf8)", borderRadius: 6, background: "var(--accent, #38bdf8)", color: "var(--bg, #0b0b0c)", padding: "6px 10px", fontSize: 12, fontWeight: 650, cursor: "pointer" },
  primaryButtonWide: { border: "1px solid var(--accent, #38bdf8)", borderRadius: 6, background: "var(--accent, #38bdf8)", color: "var(--bg, #0b0b0c)", padding: "9px 10px", fontSize: 13, fontWeight: 650, cursor: "pointer", width: "100%" },
  primaryLink: { border: "1px solid var(--accent, #38bdf8)", borderRadius: 6, color: "var(--accent, #38bdf8)", textDecoration: "none", padding: "6px 10px", fontSize: 12 },
  linkButton: { border: 0, background: "transparent", color: "var(--text-muted, #a1a1aa)", padding: 0, fontSize: 12, cursor: "pointer" },
  blueprints: { marginTop: 12, display: "grid", gap: 8 },
  blueprint: { padding: 10, border: "1px solid var(--border, #2a2a2d)", borderRadius: 6 },
  modalBackdrop: { position: "fixed", inset: 0, background: "rgba(0,0,0,.62)", display: "flex", alignItems: "flex-end", justifyContent: "center", padding: 24, zIndex: 80 },
  confirmBackdrop: { position: "fixed", inset: 0, background: "rgba(0,0,0,.62)", display: "flex", alignItems: "center", justifyContent: "center", padding: 24, zIndex: 80 },
  modal: { width: "min(100%, 980px)", maxHeight: "76vh", display: "flex", flexDirection: "column", border: "1px solid var(--border, #2a2a2d)", borderRadius: 8, background: "var(--bg-card, #111114)", overflow: "hidden" },
  confirmModal: { width: "min(100%, 420px)", display: "flex", flexDirection: "column", border: "1px solid var(--border, #2a2a2d)", borderRadius: 8, background: "var(--bg-card, #111114)", overflow: "hidden", boxShadow: "0 20px 70px rgba(0,0,0,.42)" },
  modalHeader: { display: "flex", alignItems: "center", justifyContent: "space-between", padding: "12px 14px", borderBottom: "1px solid var(--border, #2a2a2d)" },
  modalBody: { padding: 14, display: "flex", flexDirection: "column", gap: 8 },
  modalFooter: { display: "flex", justifyContent: "flex-end", gap: 8, padding: 14, borderTop: "1px solid var(--border, #2a2a2d)" },
  confirmName: { fontSize: 15, fontWeight: 650, overflowWrap: "anywhere" },
  dangerButton: { border: "1px solid var(--error, #ef4444)", borderRadius: 6, background: "var(--error, #ef4444)", color: "white", padding: "6px 10px", fontSize: 12, fontWeight: 650, cursor: "pointer" },
  pre: { margin: 0, padding: 14, overflow: "auto", whiteSpace: "pre-wrap", fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace", fontSize: 12, color: "var(--text, #e5e7eb)" },
};
