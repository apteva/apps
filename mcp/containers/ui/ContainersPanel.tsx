import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";

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
  spec?: {
    image?: string;
    ports?: Array<{ container_port: number; host_port?: number }>;
    health_path?: string;
    resources?: { memory_mb?: number; cpu?: number };
  };
}

interface FileDraft {
  path: string;
  content: string;
  mode: string;
  secret: boolean;
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

function parseEnvironment(value: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const raw of value.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const split = line.indexOf("=");
    if (split <= 0) throw new Error(`Invalid environment entry: ${line}`);
    env[line.slice(0, split).trim()] = line.slice(split + 1);
  }
  return env;
}

function parseVolumes(value: string): Array<{ name: string; mount_path: string }> {
  const volumes: Array<{ name: string; mount_path: string }> = [];
  for (const raw of value.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const split = line.indexOf(":");
    if (split <= 0) throw new Error(`Invalid volume entry: ${line}`);
    volumes.push({ name: line.slice(0, split).trim(), mount_path: line.slice(split + 1).trim() });
  }
  return volumes;
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
  const loadingRef = useRef(false);
  const [logs, setLogs] = useState<{ name: string; body: string } | null>(null);
  const [pendingDestroy, setPendingDestroy] = useState<{ id: string; name: string; deleteVolumes: boolean } | null>(null);
  const [form, setForm] = useState({
    name: "test-nginx",
    image: "nginx:alpine",
    blueprintSlug: "",
    pullPolicy: "missing",
    containerPort: "80",
    hostPort: "",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5",
    hostId: "0",
    environment: "",
    volumes: "",
    files: [] as FileDraft[],
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
    if (loadingRef.current) return;
    loadingRef.current = true;
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
    } finally {
      loadingRef.current = false;
    }
  }, []);

  useEffect(() => {
    loadHosts();
    load();
    const timer = window.setInterval(load, 5000);
    return () => window.clearInterval(timer);
  }, [load, loadHosts]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || busy) return;
      setLogs(null);
      setPendingDestroy(null);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy]);

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
      const env = parseEnvironment(next.environment);
      const volumes = parseVolumes(next.volumes);
      const files = next.files
        .filter((file) => file.path.trim() || file.content)
        .map((file) => ({ path: file.path.trim(), content: file.content, mode: file.mode.trim() || "0600", secret: file.secret }));
      await api("/workloads", {
        method: "POST",
        body: JSON.stringify({
          name,
          image: next.image.trim(),
          blueprint_slug: next.blueprintSlug || undefined,
          instance_id: Number.isFinite(instanceID) && instanceID > 0 ? instanceID : 0,
          use_local: instanceID === 0,
          pull_policy: next.pullPolicy,
          ports,
          env,
          volumes,
          files,
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

  const destroy = useCallback(async (id: string, deleteVolumes: boolean) => {
    setBusy(`destroy:${id}`);
    setError("");
    try {
      await api(`/workloads/${encodeURIComponent(id)}${deleteVolumes ? "?delete_volumes=1" : ""}`, { method: "DELETE" });
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
      blueprintSlug: "",
      pullPolicy: form.pullPolicy,
      containerPort: preset.containerPort,
      hostPort: "",
      healthPath: preset.healthPath,
      memoryMB: preset.memoryMB,
      cpu: preset.cpu,
      hostId: form.hostId,
      environment: form.environment,
      volumes: form.volumes,
      files: form.files,
    });
  };

  const useBlueprint = (blueprint: Blueprint) => {
    const spec = blueprint.spec || {};
    const port = spec.ports?.[0];
    setForm((current) => ({
      ...current,
      blueprintSlug: blueprint.slug,
      image: spec.image || current.image,
      containerPort: port?.container_port ? String(port.container_port) : current.containerPort,
      hostPort: port?.host_port ? String(port.host_port) : "",
      healthPath: spec.health_path || current.healthPath,
      memoryMB: spec.resources?.memory_mb ? String(spec.resources.memory_mb) : current.memoryMB,
      cpu: spec.resources?.cpu ? String(spec.resources.cpu) : current.cpu,
      name: autoName(spec.image || current.image),
    }));
  };

  const updateFile = (index: number, patch: Partial<FileDraft>) => {
    setForm((current) => ({
      ...current,
      files: current.files.map((file, fileIndex) => fileIndex === index ? { ...file, ...patch } : file),
    }));
  };

  return (
    <div style={styles.root}>
      <style>{responsiveCSS}</style>
      <header className="containers-header" style={styles.header}>
        <div>
          <h1 style={styles.title}>Containers</h1>
          <div style={styles.subtitle}>Docker workloads across connected hosts</div>
        </div>
        <div style={styles.headerRight}>
          <span style={styles.statusText}>{status}</span>
          <button type="button" style={styles.button} onClick={load} disabled={!!busy}>Refresh</button>
        </div>
      </header>

      {error && <div style={styles.error}>{error}</div>}

      <main className="containers-main" style={styles.main}>
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
                  <div className="containers-row-top" style={styles.rowTop}>
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
                      <button type="button" style={styles.button} disabled={!!busy || w.status !== "running"} onClick={() => action(w.id, "restart")}>Restart</button>
                      <button type="button" style={styles.button} disabled={!!busy} onClick={() => showLogs(w)}>Logs</button>
                    </div>
                  </div>

                  <div className="containers-meta-grid" style={styles.metaGrid}>
                    <Meta label="URL" value={w.public_url || "-"} />
                    <Meta label="Host" value={hostLabels.get(w.instance_id || w.host_id || 0) || `Host ${w.instance_id || w.host_id || 0}`} />
                    <Meta label="Port" value={portLabel(w)} />
                    <Meta label="Created" value={fmtDate(w.created_at)} />
                    <Meta label="Resources" value={`${w.resources?.memory_mb || 0} MB / ${w.resources?.cpu || 0} CPU`} />
                  </div>

                  {w.last_error && <div style={styles.errorSmall}>{w.last_error}</div>}

                  <div style={styles.secondaryActions}>
                    <button type="button" style={styles.linkButton} disabled={!!busy || w.status !== "stopped"} onClick={() => action(w.id, "start")}>Start</button>
                    <button type="button" style={styles.linkButton} disabled={!!busy || w.status !== "running"} onClick={() => action(w.id, "stop")}>Stop</button>
                    <button type="button" style={styles.linkButton} disabled={!!busy} onClick={() => action(w.id, "health")}>Health check</button>
                    <button type="button" style={{ ...styles.linkButton, color: "var(--error, #ef4444)" }} disabled={!!busy} onClick={() => setPendingDestroy({ id: w.id, name: w.name, deleteVolumes: false })}>Destroy</button>
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
                      blueprintSlug: "",
                      pullPolicy: form.pullPolicy,
                      containerPort: preset.containerPort,
                      hostPort: "",
                      healthPath: preset.healthPath,
                      memoryMB: preset.memoryMB,
                      cpu: preset.cpu,
                      hostId: form.hostId,
                      environment: form.environment,
                      volumes: form.volumes,
                      files: form.files,
                    })}>Run</button>
                  </div>
                </div>
              ))}
            </div>
          </section>

          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Run Image</h2>
            <div style={styles.form}>
              <label style={styles.fieldLabel}>Name<input style={styles.input} value={form.name} onChange={(e) => setForm((current) => ({ ...current, name: e.target.value }))} /></label>
              <label style={styles.fieldLabel}>Host<select style={styles.input} value={form.hostId} onChange={(e) => setForm((current) => ({ ...current, hostId: e.target.value }))}>
                {hosts.map((host) => (
                  <option key={host.id} value={String(host.id)}>
                    {host.label || host.name}{host.status !== "ready" ? ` · ${host.status}` : ""}
                  </option>
                ))}
              </select></label>
              <label style={styles.fieldLabel}>Blueprint<select style={styles.input} value={form.blueprintSlug} onChange={(e) => {
                const blueprint = blueprints.find((item) => item.slug === e.target.value);
                if (blueprint) useBlueprint(blueprint);
                else setForm((current) => ({ ...current, blueprintSlug: "" }));
              }}>
                <option value="">None</option>
                {blueprints.map((blueprint) => <option key={blueprint.slug} value={blueprint.slug}>{blueprint.name}</option>)}
              </select></label>
              <label style={styles.fieldLabel}>Image<input style={styles.input} placeholder="nginx:alpine" value={form.image} onChange={(e) => setForm((current) => ({ ...current, image: e.target.value }))} /></label>
              <label style={styles.fieldLabel}>Pull policy<select style={styles.input} value={form.pullPolicy} onChange={(e) => setForm((current) => ({ ...current, pullPolicy: e.target.value }))}>
                <option value="missing">When missing</option>
                <option value="always">Always</option>
                <option value="never">Never</option>
              </select></label>
              <div className="containers-two-cols" style={styles.twoCols}>
                <label style={styles.fieldLabel}>Container port<input inputMode="numeric" style={styles.input} value={form.containerPort} onChange={(e) => setForm((current) => ({ ...current, containerPort: e.target.value }))} /></label>
                <label style={styles.fieldLabel}>Host port<input inputMode="numeric" style={styles.input} placeholder="Auto" value={form.hostPort} onChange={(e) => setForm((current) => ({ ...current, hostPort: e.target.value }))} /></label>
              </div>
              <div className="containers-three-cols" style={styles.threeCols}>
                <label style={styles.fieldLabel}>Health path<input style={styles.input} value={form.healthPath} onChange={(e) => setForm((current) => ({ ...current, healthPath: e.target.value }))} /></label>
                <label style={styles.fieldLabel}>Memory MB<input inputMode="numeric" style={styles.input} value={form.memoryMB} onChange={(e) => setForm((current) => ({ ...current, memoryMB: e.target.value }))} /></label>
                <label style={styles.fieldLabel}>CPU<input inputMode="decimal" style={styles.input} value={form.cpu} onChange={(e) => setForm((current) => ({ ...current, cpu: e.target.value }))} /></label>
              </div>
              <label style={styles.fieldLabel}>Environment<textarea style={styles.textarea} placeholder="PORT=8080" value={form.environment} onChange={(e) => setForm((current) => ({ ...current, environment: e.target.value }))} /></label>
              <label style={styles.fieldLabel}>Volumes<textarea style={styles.textarea} placeholder="data:/data" value={form.volumes} onChange={(e) => setForm((current) => ({ ...current, volumes: e.target.value }))} /></label>
              <div style={styles.filesHeader}>
                <span style={styles.fieldTitle}>Files</span>
                <button type="button" style={styles.button} onClick={() => setForm((current) => ({ ...current, files: [...current.files, { path: "", content: "", mode: "0600", secret: false }] }))}>Add file</button>
              </div>
              {form.files.map((file, index) => (
                <div key={index} style={styles.fileEditor}>
                  <div className="containers-two-cols" style={styles.twoCols}>
                    <label style={styles.fieldLabel}>Path<input style={styles.input} placeholder="/data/config.yaml" value={file.path} onChange={(e) => updateFile(index, { path: e.target.value })} /></label>
                    <label style={styles.fieldLabel}>Mode<input style={styles.input} value={file.mode} onChange={(e) => updateFile(index, { mode: e.target.value })} /></label>
                  </div>
                  <label style={styles.fieldLabel}>Content<textarea style={styles.fileContent} value={file.content} onChange={(e) => updateFile(index, { content: e.target.value })} /></label>
                  <div style={styles.fileFooter}>
                    <label style={styles.checkboxLabel}><input type="checkbox" checked={file.secret} onChange={(e) => updateFile(index, { secret: e.target.checked })} /> Secret</label>
                    <button type="button" style={styles.linkButton} onClick={() => setForm((current) => ({ ...current, files: current.files.filter((_, fileIndex) => fileIndex !== index) }))}>Remove</button>
                  </div>
                </div>
              ))}
              <button type="button" style={styles.primaryButtonWide} onClick={() => runSpec(form)} disabled={busy === "run" || (!form.image.trim() && !form.blueprintSlug)}>
                {busy === "run" ? "Starting..." : "Run container"}
              </button>
            </div>
          </section>

          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Blueprints</h2>
            <div style={styles.blueprints}>
              {blueprints.map((b) => (
                <div key={b.slug} style={styles.blueprint}>
                  <div style={styles.blueprintTop}><div style={styles.quickTitle}>{b.name}</div><button type="button" style={styles.button} onClick={() => useBlueprint(b)}>Use</button></div>
                  <div style={styles.muted}>{b.description}</div>
                </div>
              ))}
            </div>
          </section>
        </aside>
      </main>

      {logs && (
        <div style={styles.modalBackdrop} onClick={() => setLogs(null)}>
          <div role="dialog" aria-modal="true" aria-label={`Logs for ${logs.name}`} style={styles.modal} onClick={(e) => e.stopPropagation()}>
            <div style={styles.modalHeader}>
              <div style={styles.sectionTitle}>Logs: {logs.name}</div>
              <button autoFocus type="button" style={styles.button} onClick={() => setLogs(null)}>Close</button>
            </div>
            <pre style={styles.pre}>{logs.body || "(no logs)"}</pre>
          </div>
        </div>
      )}

      {pendingDestroy && (
        <div style={styles.confirmBackdrop} onClick={() => !busy && setPendingDestroy(null)}>
          <div role="alertdialog" aria-modal="true" aria-label={`Destroy ${pendingDestroy.name}`} style={styles.confirmModal} onClick={(e) => e.stopPropagation()}>
            <div style={styles.modalHeader}>
              <div style={styles.sectionTitle}>Destroy container</div>
              <button autoFocus type="button" style={styles.button} disabled={!!busy} onClick={() => setPendingDestroy(null)}>Close</button>
            </div>
            <div style={styles.modalBody}>
              <div style={styles.confirmName}>{pendingDestroy.name}</div>
              <label style={styles.checkboxLabel}><input type="checkbox" checked={pendingDestroy.deleteVolumes} onChange={(e) => setPendingDestroy({ ...pendingDestroy, deleteVolumes: e.target.checked })} /> Delete volumes</label>
            </div>
            <div style={styles.modalFooter}>
              <button type="button" style={styles.button} disabled={!!busy} onClick={() => setPendingDestroy(null)}>Cancel</button>
              <button type="button" style={styles.dangerButton} disabled={!!busy} onClick={() => destroy(pendingDestroy.id, pendingDestroy.deleteVolumes)}>
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

const responsiveCSS = `
.containers-main, .containers-header, .containers-row-top, .containers-meta-grid, .containers-two-cols, .containers-three-cols { min-width: 0; }
button:disabled { cursor: not-allowed !important; opacity: .45; }
@media (max-width: 980px) {
  .containers-main { display: flex !important; flex-direction: column; }
  .containers-main > * { flex-shrink: 0; width: 100%; box-sizing: border-box; }
}
@media (max-width: 640px) {
  .containers-header { align-items: flex-start !important; flex-direction: column; padding: 14px 16px !important; }
  .containers-main { padding: 12px !important; }
  .containers-row-top { flex-direction: column; }
  .containers-meta-grid { grid-template-columns: repeat(2, minmax(0, 1fr)) !important; }
  .containers-three-cols { grid-template-columns: minmax(0, 1fr) !important; }
}
@media (max-width: 400px) {
  .containers-meta-grid { grid-template-columns: minmax(0, 1fr) !important; }
  .containers-two-cols { grid-template-columns: minmax(0, 1fr) !important; }
}
`;

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
  metaGrid: { display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: 8 },
  metaItem: { minWidth: 0, padding: "8px 10px", border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg, #0b0b0c)" },
  metaLabel: { fontSize: 10, textTransform: "uppercase", color: "var(--text-dim, #8a8a8f)", marginBottom: 4 },
  metaValue: { fontSize: 12, color: "var(--text, #e5e7eb)", overflowWrap: "anywhere" },
  card: { border: "1px solid var(--border, #2a2a2d)", borderRadius: 8, background: "var(--bg-card, #111114)", padding: 14 },
  quickGrid: { marginTop: 12, display: "grid", gap: 10 },
  quickCard: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12, padding: 10, border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg, #0b0b0c)" },
  quickTitle: { fontSize: 13, fontWeight: 650 },
  quickActions: { display: "flex", gap: 8, flexShrink: 0 },
  form: { marginTop: 12, display: "flex", flexDirection: "column", gap: 10 },
  fieldLabel: { display: "flex", flexDirection: "column", gap: 5, minWidth: 0, fontSize: 11, color: "var(--text-dim, #8a8a8f)" },
  fieldTitle: { fontSize: 11, color: "var(--text-dim, #8a8a8f)" },
  twoCols: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 },
  threeCols: { display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 8 },
  input: { width: "100%", boxSizing: "border-box", border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg-input, #17171a)", color: "var(--text, #e5e7eb)", padding: "8px 10px", fontSize: 13, outline: "none" },
  textarea: { width: "100%", minHeight: 66, resize: "vertical", boxSizing: "border-box", border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg-input, #17171a)", color: "var(--text, #e5e7eb)", padding: "8px 10px", fontSize: 12, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace", outline: "none" },
  fileContent: { width: "100%", minHeight: 100, resize: "vertical", boxSizing: "border-box", border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg-input, #17171a)", color: "var(--text, #e5e7eb)", padding: "8px 10px", fontSize: 12, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace", outline: "none" },
  filesHeader: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 },
  fileEditor: { display: "flex", flexDirection: "column", gap: 8, padding: 10, border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "var(--bg, #0b0b0c)" },
  fileFooter: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8 },
  checkboxLabel: { display: "inline-flex", alignItems: "center", gap: 7, fontSize: 12, color: "var(--text-muted, #a1a1aa)" },
  button: { border: "1px solid var(--border, #2a2a2d)", borderRadius: 6, background: "transparent", color: "var(--text-muted, #a1a1aa)", padding: "6px 10px", fontSize: 12, cursor: "pointer" },
  primaryButton: { border: "1px solid var(--accent, #38bdf8)", borderRadius: 6, background: "var(--accent, #38bdf8)", color: "var(--bg, #0b0b0c)", padding: "6px 10px", fontSize: 12, fontWeight: 650, cursor: "pointer" },
  primaryButtonWide: { border: "1px solid var(--accent, #38bdf8)", borderRadius: 6, background: "var(--accent, #38bdf8)", color: "var(--bg, #0b0b0c)", padding: "9px 10px", fontSize: 13, fontWeight: 650, cursor: "pointer", width: "100%" },
  primaryLink: { border: "1px solid var(--accent, #38bdf8)", borderRadius: 6, color: "var(--accent, #38bdf8)", textDecoration: "none", padding: "6px 10px", fontSize: 12 },
  linkButton: { border: 0, background: "transparent", color: "var(--text-muted, #a1a1aa)", padding: 0, fontSize: 12, cursor: "pointer" },
  blueprints: { marginTop: 12, display: "grid", gap: 8 },
  blueprint: { padding: 10, border: "1px solid var(--border, #2a2a2d)", borderRadius: 6 },
  blueprintTop: { display: "flex", alignItems: "center", justifyContent: "space-between", gap: 8, marginBottom: 4 },
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
