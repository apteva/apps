import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";

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
  ports?: Array<{
    container_port: number;
    host_port: number;
    bind_addr: string;
    protocol: string;
  }>;
  volumes?: Array<{
    name: string;
    docker_volume_name: string;
    mount_path: string;
  }>;
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

const TEST_IMAGES = [
  {
    slug: "nginx",
    label: "Nginx",
    image: "nginx:alpine",
    containerPort: "80",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5",
  },
  {
    slug: "whoami",
    label: "Whoami",
    image: "traefik/whoami:v1.10",
    containerPort: "80",
    healthPath: "/",
    memoryMB: "128",
    cpu: "0.25",
  },
  {
    slug: "httpd",
    label: "Apache",
    image: "httpd:alpine",
    containerPort: "80",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5",
  },
  {
    slug: "adminer",
    label: "Adminer",
    image: "adminer:latest",
    containerPort: "8080",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5",
  },
] as const;

export function imageName(image: string): string {
  const withoutTag =
    image.split("@")[0].split("/").pop()!.split(":")[0] || "container";
  const base = withoutTag.split("/").pop() || "container";
  return (
    base
      .toLowerCase()
      .replace(/[^a-z0-9-]+/g, "-")
      .replace(/^-+|-+$/g, "") || "container"
  );
}

function autoName(image: string): string {
  return `test-${imageName(image)}-${crypto.randomUUID().slice(0, 8)}`;
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
  return d.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function portLabel(w: Workload): string {
  return (
    w.ports
      ?.map(
        (p) =>
          `${p.bind_addr}:${p.host_port} → ${p.container_port}/${p.protocol}`,
      )
      .join(", ") || "No ports"
  );
}

function parseEnvironment(value: string): Record<string, string> {
  const env: Record<string, string> = {};
  for (const raw of value.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const split = line.indexOf("=");
    if (split <= 0) throw new Error(`Invalid environment entry: ${line}`);
    const key = line.slice(0, split).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key))
      throw new Error(`Invalid environment key: ${key}`);
    env[key] = raw.slice(raw.indexOf("=") + 1);
  }
  return env;
}

function parseVolumes(
  value: string,
): Array<{ name: string; mount_path: string; retained_from?: string }> {
  const volumes: Array<{
    name: string;
    mount_path: string;
    retained_from?: string;
  }> = [];
  for (const raw of value.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const split = line.indexOf(":");
    if (split <= 0) throw new Error(`Invalid volume entry: ${line}`);
    const [mount, retained] = line
      .slice(split + 1)
      .trim()
      .split("@");
    volumes.push({
      name: line.slice(0, split).trim(),
      mount_path: mount,
      retained_from: retained,
    });
  }
  return volumes;
}

export function apiURL(path: string, props: NativePanelProps): string {
  const [route, query = ""] = path.split("?");
  const params = new URLSearchParams(query);
  if (props.projectId) params.set("project_id", props.projectId);
  return `/api/apps/containers/_install/${props.installId}/api${route}?${params}`;
}
export function numeric(
  value: string,
  label: string,
  min: number,
  max: number,
  integer = true,
): number {
  const n = value.trim() ? Number(value) : 0;
  if (
    !Number.isFinite(n) ||
    n < min ||
    n > max ||
    (integer && !Number.isInteger(n))
  )
    throw new Error(
      `${label} must be ${integer ? "an integer" : "a number"} between ${min} and ${max}`,
    );
  return n;
}
export default function ContainersPanel(props: NativePanelProps) {
  return (
    <ScopedContainersPanel
      key={`${props.installId}:${props.projectId}`}
      {...props}
    />
  );
}
function ScopedContainersPanel(props: NativePanelProps) {
  const requests = useRef(new Set<AbortController>());
  const api = useCallback(
    async <T,>(path: string, init?: RequestInit): Promise<T> => {
      const controller = new AbortController();
      requests.current.add(controller);
      try {
        const res = await fetch(apiURL(path, props), {
          credentials: "same-origin",
          ...init,
          signal: controller.signal,
          headers: { "Content-Type": "application/json", ...init?.headers },
        });
        if (!res.ok) throw new Error(`${res.status}: ${await res.text()}`);
        return (await res.json()) as T;
      } finally {
        requests.current.delete(controller);
      }
    },
    [props.installId, props.projectId],
  );
  useEffect(
    () => () => {
      for (const c of requests.current) c.abort();
    },
    [],
  );
  const [loadError, setLoadError] = useState("");
  const [retained, setRetained] = useState<Workload[]>([]);
  const [history, setHistory] = useState<{
    w: Workload;
    executions: Array<{
      id: string;
      status: string;
      output: string;
      output_truncated: boolean;
    }>;
    sessions: Array<{ session_key: string; active: boolean }>;
  } | null>(null);
  const inFlight = useRef(new Set<string>());
  const [busyKeys, setBusyKeys] = useState(new Set<string>());
  const begin = (key: string) => {
    if (inFlight.current.has(key)) return false;
    inFlight.current.add(key);
    setBusyKeys(new Set(inFlight.current));
    return true;
  };
  const end = (key: string) => {
    inFlight.current.delete(key);
    setBusyKeys(new Set(inFlight.current));
  };
  const [page, setPage] = useState(0);
  const [retainedPage, setRetainedPage] = useState(0);
  const [workloads, setWorkloads] = useState<Workload[]>([]);
  const [blueprints, setBlueprints] = useState<Blueprint[]>([]);
  const [hosts, setHosts] = useState<HostOption[]>([
    {
      id: 0,
      name: "localhost",
      provider: "local",
      status: "unknown",
      label: "Local Docker host",
    },
  ]);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("Loading containers...");
  const busy = busyKeys.has("run") ? "run" : "";
  const loadingRef = useRef(false);
  const [logs, setLogs] = useState<{ name: string; body: string } | null>(null);
  const [pendingDestroy, setPendingDestroy] = useState<{
    id: string;
    name: string;
    deleteVolumes: boolean;
  } | null>(null);
  const [form, setForm] = useState({
    name: autoName("nginx:alpine"),
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

  const runningCount = useMemo(
    () => workloads.filter((w) => w.status === "running").length,
    [workloads],
  );
  const hostLabels = useMemo(() => {
    const labels = new Map<number, string>();
    for (const host of hosts)
      labels.set(host.id, host.label || host.name || `Host ${host.id}`);
    labels.set(0, labels.get(0) || "Local Docker host");
    return labels;
  }, [hosts]);

  const loadHosts = useCallback(async () => {
    try {
      const res = await api<{
        hosts: HostOption[];
        default_host_id?: number;
        warning?: string;
      }>("/hosts");
      const rows = res.hosts?.length
        ? res.hosts
        : [
            {
              id: 0,
              name: "localhost",
              provider: "local",
              status: "unknown",
              label: "Local Docker host",
            },
          ];
      setHosts(rows);
      const defaultID =
        res.default_host_id ?? rows.find((h) => h.default)?.id ?? 0;
      setForm((f) => ({ ...f, hostId: f.hostId || String(defaultID) }));
    } catch (e) {
      setLoadError(`Host discovery: ${(e as Error).message}`);
      setHosts([
        {
          id: 0,
          name: "localhost",
          provider: "local",
          status: "unknown",
          label: "Local Docker host",
        },
      ]);
    }
  }, [api]);

  const load = useCallback(async () => {
    if (loadingRef.current) return;
    loadingRef.current = true;
    try {
      const [w, r] = await Promise.all([
        api<{ workloads: Workload[]; count?: number }>(
          `/workloads?limit=100&offset=${page * 100}`,
        ),
        api<{ workloads: Workload[] }>(
          `/workloads?status=destroyed&retained=1&limit=100&offset=${retainedPage * 100}`,
        ),
      ]);
      const rows = (w.workloads || []).filter(
        (row) => row.status !== "destroyed",
      );
      setWorkloads(rows);
      setRetained((r.workloads || []).filter((row) => row.volumes?.length));
      setLoadError("");
      setStatus(
        `${rows.length} tracked, ${rows.filter((row) => row.status === "running").length} running`,
      );
    } catch (e) {
      if ((e as Error).name !== "AbortError")
        setLoadError((e as Error).message);
      setStatus("Load failed");
    } finally {
      loadingRef.current = false;
    }
  }, [api, page, retainedPage]);

  useEffect(() => {
    load();
    const timer = window.setInterval(load, 5000);
    return () => window.clearInterval(timer);
  }, [load]);
  useEffect(() => {
    loadHosts();
    api<{ blueprints: Blueprint[] }>("/blueprints")
      .then((r) => setBlueprints(r.blueprints || []))
      .catch((e) => setLoadError(e.message));
  }, [api, loadHosts]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || busy) return;
      setLogs(null);
      setPendingDestroy(null);
      setHistory(null);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [busy]);

  const rootRef = useRef<HTMLDivElement>(null);
  const dialogOpen = !!logs || !!pendingDestroy || !!history;
  useEffect(() => {
    if (!dialogOpen) return;
    const previous = document.activeElement as HTMLElement | null;
    const dialog = rootRef.current?.querySelector<HTMLElement>(
      '[aria-modal="true"]',
    );
    const focusables = () =>
      Array.from(
        dialog?.querySelectorAll<HTMLElement>(
          'button:not(:disabled), input:not(:disabled), [tabindex="0"]',
        ) || [],
      );
    focusables()[0]?.focus();
    const trap = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      const nodes = focusables();
      const i = nodes.indexOf(document.activeElement as HTMLElement);
      e.preventDefault();
      nodes[(i + (e.shiftKey ? -1 : 1) + nodes.length) % nodes.length]?.focus();
    };
    dialog?.addEventListener("keydown", trap);
    return () => {
      dialog?.removeEventListener("keydown", trap);
      previous?.focus();
    };
  }, [dialogOpen]);
  const showHistory = async (w: Workload) => {
    try {
      const data = await api<{
        executions: NonNullable<typeof history>["executions"];
        sessions: NonNullable<typeof history>["sessions"];
      }>(`/workloads/${encodeURIComponent(w.id)}/executions`);
      setHistory({ w, ...data });
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const historyAction = async (path: string, method: string) => {
    if (!history || !begin(history.w.id)) return;
    try {
      await api(`/workloads/${encodeURIComponent(history.w.id)}/${path}`, {
        method,
      });
      await showHistory(history.w);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      end(history.w.id);
    }
  };
  const runSpec = useCallback(
    async (next: typeof form) => {
      if (!begin("run")) return;
      setError("");
      setStatus("Starting container...");
      try {
        const name = next.name.trim() || autoName(next.image);
        const ports = next.containerPort
          ? [
              {
                container_port: numeric(
                  next.containerPort,
                  "Container port",
                  1,
                  65535,
                ),
                host_port: numeric(next.hostPort, "Host port", 0, 65535),
                protocol: "tcp",
              },
            ]
          : [];
        const instanceID = numeric(
          next.hostId,
          "Host",
          0,
          Number.MAX_SAFE_INTEGER,
        );
        const env = parseEnvironment(next.environment);
        const volumes = parseVolumes(next.volumes);
        const files = next.files
          .filter((file) => file.path.trim() || file.content)
          .map((file) => ({
            path: file.path.trim(),
            content: file.content,
            mode: file.mode.trim() || "0600",
            secret: file.secret,
          }));
        await api("/workloads", {
          method: "POST",
          body: JSON.stringify({
            name,
            image: next.image.trim(),
            blueprint_slug: next.blueprintSlug || undefined,
            instance_id:
              Number.isFinite(instanceID) && instanceID > 0 ? instanceID : 0,
            use_local: instanceID === 0,
            pull_policy: next.pullPolicy,
            ports,
            env,
            volumes,
            files,
            health_path: next.healthPath,
            disable_health_check: !next.healthPath.trim(),
            resources: {
              memory_mb: numeric(next.memoryMB, "Memory MB", 0, 1048576),
              cpu: numeric(next.cpu, "CPU", 0, 1024, false),
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
        end("run");
      }
    },
    [load, api],
  );

  const action = useCallback(
    async (id: string, act: "start" | "stop" | "restart" | "health") => {
      if (!begin(id)) return;
      setError("");
      try {
        await api(`/workloads/${encodeURIComponent(id)}/${act}`, {
          method: "POST",
        });
        await load();
      } catch (e) {
        setError((e as Error).message);
      } finally {
        end(id);
      }
    },
    [load, api],
  );

  const destroy = useCallback(
    async (id: string, deleteVolumes: boolean) => {
      if (!begin(id)) return;
      setError("");
      try {
        await api(
          `/workloads/${encodeURIComponent(id)}${deleteVolumes ? "?delete_volumes=1" : ""}`,
          { method: "DELETE" },
        );
        setPendingDestroy(null);
        await load();
      } catch (e) {
        setError((e as Error).message);
      } finally {
        end(id);
      }
    },
    [load, api],
  );

  const showLogs = useCallback(
    async (w: Workload) => {
      if (!begin(w.id)) return;
      setError("");
      try {
        const res = await api<{ logs: string }>(
          `/workloads/${encodeURIComponent(w.id)}/logs?tail=300`,
        );
        setLogs({ name: w.name, body: res.logs || "" });
      } catch (e) {
        setError((e as Error).message);
      } finally {
        end(w.id);
      }
    },
    [api],
  );

  const fillTestImage = (preset: (typeof TEST_IMAGES)[number]) => {
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
      environment: "",
      volumes: "",
      files: [],
    });
  };

  const useBlueprint = (blueprint: Blueprint) => {
    const spec = blueprint.spec || {};
    const port = spec.ports?.[0];
    setForm((current) => ({
      ...current,
      blueprintSlug: blueprint.slug,
      image: spec.image || current.image,
      containerPort: port?.container_port ? String(port.container_port) : "",
      hostPort: port?.host_port ? String(port.host_port) : "",
      healthPath: spec.health_path || "",
      memoryMB: String(spec.resources?.memory_mb ?? 0),
      cpu: String(spec.resources?.cpu ?? 0),
      name: autoName(spec.image || current.image),
    }));
  };

  const updateFile = (index: number, patch: Partial<FileDraft>) => {
    setForm((current) => ({
      ...current,
      files: current.files.map((file, fileIndex) =>
        fileIndex === index ? { ...file, ...patch } : file,
      ),
    }));
  };

  return (
    <div ref={rootRef} className="containers-panel" style={styles.root}>
      <style>{responsiveCSS}</style>
      <header className="containers-header" style={styles.header}>
        <div>
          <h1 style={styles.title}>Containers</h1>
          <div style={styles.subtitle}>
            Docker workloads across connected hosts
          </div>
        </div>
        <div style={styles.headerRight}>
          <span style={styles.statusText}>{status}</span>
          <button
            type="button"
            style={styles.button}
            onClick={load}
            disabled={!!busy}
          >
            Refresh
          </button>
        </div>
      </header>

      {loadError && (
        <div role="status" style={styles.error}>
          {loadError}
        </div>
      )}
      {error && (
        <div role="alert" style={styles.error}>
          {error}{" "}
          <button style={styles.button} onClick={() => setError("")}>
            Dismiss
          </button>
        </div>
      )}

      <main className="containers-main" style={styles.main}>
        <section style={styles.workloads}>
          <div style={styles.sectionHeader}>
            <div>
              <h2 style={styles.sectionTitle}>Workloads</h2>
              <div style={styles.muted}>
                {workloads.length} tracked containers, {runningCount} running
              </div>
            </div>
          </div>

          {workloads.length === 0 ? (
            <div style={styles.empty}>
              No containers are tracked yet. Run nginx or whoami from the quick
              tests.
            </div>
          ) : (
            <div style={styles.list}>
              {workloads.map((w) => (
                <article key={w.id} style={styles.row}>
                  <div className="containers-row-top" style={styles.rowTop}>
                    <div style={styles.rowMain}>
                      <div style={styles.nameLine}>
                        <span style={styles.workloadName}>{w.name}</span>
                        <span
                          style={{
                            ...styles.pill,
                            color: statusColor(w.status),
                            borderColor: statusColor(w.status),
                          }}
                        >
                          {w.status}
                        </span>
                        <span style={styles.pill}>
                          {w.health_status || "unknown"}
                        </span>
                      </div>
                      <div style={styles.image}>{w.image}</div>
                    </div>
                    <div style={styles.actions}>
                      {w.public_url && (
                        <a
                          style={styles.primaryLink}
                          href={w.public_url}
                          target="_blank"
                          rel="noreferrer"
                        >
                          Open
                        </a>
                      )}
                      <button
                        type="button"
                        style={styles.button}
                        disabled={busyKeys.has(w.id) || w.status !== "running"}
                        onClick={() => action(w.id, "restart")}
                      >
                        Restart
                      </button>
                      <button
                        type="button"
                        style={styles.button}
                        disabled={busyKeys.has(w.id)}
                        onClick={() => showLogs(w)}
                      >
                        Logs
                      </button>
                    </div>
                  </div>

                  <div className="containers-meta-grid" style={styles.metaGrid}>
                    <Meta label="URL" value={w.public_url || "-"} />
                    <Meta
                      label="Host"
                      value={
                        hostLabels.get(w.instance_id || w.host_id || 0) ||
                        `Host ${w.instance_id || w.host_id || 0}`
                      }
                    />
                    <Meta label="Port" value={portLabel(w)} />
                    <Meta label="Created" value={fmtDate(w.created_at)} />
                    <Meta
                      label="Resources"
                      value={`${w.resources?.memory_mb ? `${w.resources.memory_mb} MB` : "Unlimited memory"} / ${w.resources?.cpu ? `${w.resources.cpu} CPU` : "Unlimited CPU"}`}
                    />
                  </div>

                  {w.last_error && (
                    <div style={styles.errorSmall}>{w.last_error}</div>
                  )}

                  <div style={styles.secondaryActions}>
                    <button
                      style={styles.linkButton}
                      disabled={busyKeys.has(w.id)}
                      onClick={() => showHistory(w)}
                    >
                      Executions & sessions
                    </button>
                    <button
                      type="button"
                      style={styles.linkButton}
                      disabled={busyKeys.has(w.id) || w.status !== "stopped"}
                      onClick={() => action(w.id, "start")}
                    >
                      Start
                    </button>
                    <button
                      type="button"
                      style={styles.linkButton}
                      disabled={busyKeys.has(w.id) || w.status !== "running"}
                      onClick={() => action(w.id, "stop")}
                    >
                      Stop
                    </button>
                    <button
                      type="button"
                      style={styles.linkButton}
                      disabled={
                        busyKeys.has(w.id) ||
                        ["creating", "destroying"].includes(w.status)
                      }
                      onClick={() => action(w.id, "health")}
                    >
                      Health check
                    </button>
                    <button
                      type="button"
                      style={{
                        ...styles.linkButton,
                        color: "var(--error, #ef4444)",
                      }}
                      disabled={busyKeys.has(w.id)}
                      onClick={() =>
                        setPendingDestroy({
                          id: w.id,
                          name: w.name,
                          deleteVolumes: false,
                        })
                      }
                    >
                      Destroy
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
          <div style={styles.row}>
            <div style={styles.actions}>
              <button
                style={styles.button}
                disabled={page === 0}
                onClick={() => setPage((p) => p - 1)}
              >
                Previous
              </button>
              <span>Page {page + 1}</span>
              <button
                style={styles.button}
                disabled={workloads.length < 100}
                onClick={() => setPage((p) => p + 1)}
              >
                Next
              </button>
            </div>
            <div style={styles.actions}>
              <button
                style={styles.button}
                disabled={retainedPage === 0}
                onClick={() => setRetainedPage((p) => p - 1)}
              >
                Previous retained
              </button>
              <span>Retained page {retainedPage + 1}</span>
              <button
                style={styles.button}
                disabled={retained.length < 100}
                onClick={() => setRetainedPage((p) => p + 1)}
              >
                Next retained
              </button>
            </div>
            {retained.length > 0 && (
              <h2 style={styles.sectionTitle}>Retained volumes</h2>
            )}
            {retained.map((w) => (
              <div key={w.id}>
                <strong>{w.name}</strong>
                {w.volumes?.map((v) => (
                  <div key={v.name} style={styles.secondaryActions}>
                    <span>
                      {v.name} · {v.mount_path}
                    </span>
                    <button
                      style={styles.button}
                      onClick={() =>
                        setForm((f) => ({
                          ...f,
                          hostId: String(w.instance_id || w.host_id || 0),
                          volumes:
                            `${f.volumes}\n${v.name}:${v.mount_path}@${w.id}`.trim(),
                        }))
                      }
                    >
                      Reuse in form
                    </button>
                  </div>
                ))}
                <button
                  style={styles.linkButton}
                  onClick={() =>
                    setPendingDestroy({
                      id: w.id,
                      name: w.name,
                      deleteVolumes: true,
                    })
                  }
                >
                  Delete retained volumes…
                </button>
              </div>
            ))}
          </div>
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
                    <button
                      type="button"
                      style={styles.button}
                      disabled={!!busy}
                      onClick={() => fillTestImage(preset)}
                    >
                      Fill
                    </button>
                    <button
                      type="button"
                      style={styles.primaryButton}
                      disabled={!!busy}
                      onClick={() =>
                        runSpec({
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
                          environment: "",
                          volumes: "",
                          files: [],
                        })
                      }
                    >
                      Run
                    </button>
                  </div>
                </div>
              ))}
            </div>
          </section>

          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Run Image</h2>
            <div style={styles.form}>
              <label style={styles.fieldLabel}>
                Name
                <input
                  style={styles.input}
                  value={form.name}
                  onChange={(e) =>
                    setForm((current) => ({ ...current, name: e.target.value }))
                  }
                />
              </label>
              <label style={styles.fieldLabel}>
                Host
                <select
                  style={styles.input}
                  value={form.hostId}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      hostId: e.target.value,
                    }))
                  }
                >
                  {hosts.map((host) => (
                    <option key={host.id} value={String(host.id)}>
                      {host.label || host.name}
                      {host.status !== "ready" ? ` · ${host.status}` : ""}
                    </option>
                  ))}
                </select>
              </label>
              <label style={styles.fieldLabel}>
                Blueprint
                <select
                  style={styles.input}
                  value={form.blueprintSlug}
                  onChange={(e) => {
                    const blueprint = blueprints.find(
                      (item) => item.slug === e.target.value,
                    );
                    if (blueprint) useBlueprint(blueprint);
                    else
                      setForm((current) => ({ ...current, blueprintSlug: "" }));
                  }}
                >
                  <option value="">None</option>
                  {blueprints.map((blueprint) => (
                    <option key={blueprint.slug} value={blueprint.slug}>
                      {blueprint.name}
                    </option>
                  ))}
                </select>
              </label>
              <label style={styles.fieldLabel}>
                Image
                <input
                  style={styles.input}
                  placeholder="nginx:alpine"
                  value={form.image}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      image: e.target.value,
                    }))
                  }
                />
              </label>
              <label style={styles.fieldLabel}>
                Pull policy
                <select
                  style={styles.input}
                  value={form.pullPolicy}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      pullPolicy: e.target.value,
                    }))
                  }
                >
                  <option value="missing">When missing</option>
                  <option value="always">Always</option>
                  <option value="never">Never</option>
                </select>
              </label>
              <div className="containers-two-cols" style={styles.twoCols}>
                <label style={styles.fieldLabel}>
                  Container port
                  <input
                    inputMode="numeric"
                    style={styles.input}
                    value={form.containerPort}
                    onChange={(e) =>
                      setForm((current) => ({
                        ...current,
                        containerPort: e.target.value,
                      }))
                    }
                  />
                </label>
                <label style={styles.fieldLabel}>
                  Host port
                  <input
                    inputMode="numeric"
                    style={styles.input}
                    placeholder="Auto"
                    value={form.hostPort}
                    onChange={(e) =>
                      setForm((current) => ({
                        ...current,
                        hostPort: e.target.value,
                      }))
                    }
                  />
                </label>
              </div>
              <div className="containers-three-cols" style={styles.threeCols}>
                <label style={styles.fieldLabel}>
                  Health path
                  <input
                    style={styles.input}
                    value={form.healthPath}
                    onChange={(e) =>
                      setForm((current) => ({
                        ...current,
                        healthPath: e.target.value,
                      }))
                    }
                  />
                </label>
                <label style={styles.fieldLabel}>
                  Memory MB
                  <input
                    inputMode="numeric"
                    style={styles.input}
                    value={form.memoryMB}
                    onChange={(e) =>
                      setForm((current) => ({
                        ...current,
                        memoryMB: e.target.value,
                      }))
                    }
                  />
                </label>
                <label style={styles.fieldLabel}>
                  CPU
                  <input
                    inputMode="decimal"
                    style={styles.input}
                    value={form.cpu}
                    onChange={(e) =>
                      setForm((current) => ({
                        ...current,
                        cpu: e.target.value,
                      }))
                    }
                  />
                </label>
              </div>
              <label style={styles.fieldLabel}>
                Environment
                <textarea
                  style={styles.textarea}
                  placeholder="PORT=8080"
                  value={form.environment}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      environment: e.target.value,
                    }))
                  }
                />
              </label>
              <label style={styles.fieldLabel}>
                Volumes
                <textarea
                  style={styles.textarea}
                  placeholder="data:/data"
                  value={form.volumes}
                  onChange={(e) =>
                    setForm((current) => ({
                      ...current,
                      volumes: e.target.value,
                    }))
                  }
                />
              </label>
              <div style={styles.filesHeader}>
                <span style={styles.fieldTitle}>Files</span>
                <button
                  type="button"
                  style={styles.button}
                  onClick={() =>
                    setForm((current) => ({
                      ...current,
                      files: [
                        ...current.files,
                        { path: "", content: "", mode: "0600", secret: false },
                      ],
                    }))
                  }
                >
                  Add file
                </button>
              </div>
              {form.files.map((file, index) => (
                <div key={index} style={styles.fileEditor}>
                  <div className="containers-two-cols" style={styles.twoCols}>
                    <label style={styles.fieldLabel}>
                      Path
                      <input
                        style={styles.input}
                        placeholder="/data/config.yaml"
                        value={file.path}
                        onChange={(e) =>
                          updateFile(index, { path: e.target.value })
                        }
                      />
                    </label>
                    <label style={styles.fieldLabel}>
                      Mode
                      <input
                        style={styles.input}
                        value={file.mode}
                        onChange={(e) =>
                          updateFile(index, { mode: e.target.value })
                        }
                      />
                    </label>
                  </div>
                  <label style={styles.fieldLabel}>
                    Content
                    <textarea
                      style={styles.fileContent}
                      value={file.content}
                      onChange={(e) =>
                        updateFile(index, { content: e.target.value })
                      }
                    />
                  </label>
                  <div style={styles.fileFooter}>
                    <label style={styles.checkboxLabel}>
                      <input
                        type="checkbox"
                        checked={file.secret}
                        onChange={(e) =>
                          updateFile(index, { secret: e.target.checked })
                        }
                      />{" "}
                      Secret
                    </label>
                    <button
                      type="button"
                      style={styles.linkButton}
                      onClick={() =>
                        setForm((current) => ({
                          ...current,
                          files: current.files.filter(
                            (_, fileIndex) => fileIndex !== index,
                          ),
                        }))
                      }
                    >
                      Remove
                    </button>
                  </div>
                </div>
              ))}
              <button
                type="button"
                style={styles.primaryButtonWide}
                onClick={() => runSpec(form)}
                disabled={
                  busy === "run" || (!form.image.trim() && !form.blueprintSlug)
                }
              >
                {busy === "run" ? "Starting..." : "Run container"}
              </button>
            </div>
          </section>

          <section style={styles.card}>
            <h2 style={styles.sectionTitle}>Blueprints</h2>
            <div style={styles.blueprints}>
              {blueprints.map((b) => (
                <div key={b.slug} style={styles.blueprint}>
                  <div style={styles.blueprintTop}>
                    <div style={styles.quickTitle}>{b.name}</div>
                    <button
                      type="button"
                      style={styles.button}
                      onClick={() => useBlueprint(b)}
                    >
                      Use
                    </button>
                  </div>
                  <div style={styles.muted}>{b.description}</div>
                </div>
              ))}
            </div>
          </section>
        </aside>
      </main>

      {history && (
        <div style={styles.modalBackdrop} onClick={() => setHistory(null)}>
          <div
            role="dialog"
            aria-modal="true"
            aria-label={`Executions for ${history.w.name}`}
            style={styles.modal}
            onClick={(e) => e.stopPropagation()}
          >
            <div style={styles.modalHeader}>
              <strong>{history.w.name}: executions & sessions</strong>
              <button style={styles.button} onClick={() => setHistory(null)}>
                Close
              </button>
            </div>
            <div style={{ ...styles.modalBody, overflow: "auto" }}>
              <button
                style={styles.button}
                onClick={() => showHistory(history.w)}
              >
                Refresh history
              </button>
              {history.sessions.map((session) => (
                <div key={session.session_key}>
                  {session.session_key} · {session.active ? "Active" : "Idle"}{" "}
                  <button
                    style={styles.button}
                    disabled={session.active || busyKeys.has(history.w.id)}
                    onClick={() =>
                      historyAction(
                        `sessions/${encodeURIComponent(session.session_key)}`,
                        "DELETE",
                      )
                    }
                  >
                    Close session
                  </button>
                </div>
              ))}
              {history.executions.length === 0 && (
                <p>No executions recorded.</p>
              )}
              {history.executions.map((e) => (
                <details key={e.id}>
                  <summary>
                    {e.id} · {e.status}
                  </summary>
                  <button
                    style={styles.button}
                    onClick={async () => {
                      try {
                        const data = await api<{
                          output: string;
                          output_truncated: boolean;
                        }>(
                          `/workloads/${encodeURIComponent(history.w.id)}/executions/${encodeURIComponent(e.id)}`,
                        );
                        setHistory((h) =>
                          h
                            ? {
                                ...h,
                                executions: h.executions.map((row) =>
                                  row.id === e.id ? { ...row, ...data } : row,
                                ),
                              }
                            : null,
                        );
                      } catch (err) {
                        setError((err as Error).message);
                      }
                    }}
                  >
                    Load output
                  </button>
                  {["queued", "running", "cancelling"].includes(e.status) && (
                    <button
                      style={styles.button}
                      disabled={busyKeys.has(history.w.id)}
                      onClick={() =>
                        historyAction(
                          `executions/${encodeURIComponent(e.id)}/cancel`,
                          "POST",
                        )
                      }
                    >
                      Cancel execution
                    </button>
                  )}
                  {e.output_truncated && (
                    <div>Showing retained output tail</div>
                  )}
                  <pre style={styles.pre}>
                    {e.output || "No captured output yet"}
                  </pre>
                </details>
              ))}
            </div>
          </div>
        </div>
      )}
      {logs && (
        <div style={styles.modalBackdrop} onClick={() => setLogs(null)}>
          <div
            role="dialog"
            aria-modal="true"
            aria-label={`Logs for ${logs.name}`}
            style={styles.modal}
            onClick={(e) => e.stopPropagation()}
          >
            <div style={styles.modalHeader}>
              <div style={styles.sectionTitle}>Logs: {logs.name}</div>
              <button
                type="button"
                style={styles.button}
                onClick={() => setLogs(null)}
              >
                Close
              </button>
            </div>
            <pre style={styles.pre}>{logs.body || "(no logs)"}</pre>
          </div>
        </div>
      )}

      {pendingDestroy && (
        <div
          style={styles.confirmBackdrop}
          onClick={() => !busy && setPendingDestroy(null)}
        >
          <div
            role="alertdialog"
            aria-modal="true"
            aria-label={`Destroy ${pendingDestroy.name}`}
            style={styles.confirmModal}
            onClick={(e) => e.stopPropagation()}
          >
            <div style={styles.modalHeader}>
              <div style={styles.sectionTitle}>Destroy container</div>
              <button
                type="button"
                style={styles.button}
                disabled={!!busy}
                onClick={() => setPendingDestroy(null)}
              >
                Close
              </button>
            </div>
            <div style={styles.modalBody}>
              <div style={styles.confirmName}>{pendingDestroy.name}</div>
              <label style={styles.checkboxLabel}>
                <input
                  type="checkbox"
                  checked={pendingDestroy.deleteVolumes}
                  onChange={(e) =>
                    setPendingDestroy({
                      ...pendingDestroy,
                      deleteVolumes: e.target.checked,
                    })
                  }
                />{" "}
                Delete volumes
              </label>
            </div>
            <div style={styles.modalFooter}>
              <button
                type="button"
                style={styles.button}
                disabled={!!busy}
                onClick={() => setPendingDestroy(null)}
              >
                Cancel
              </button>
              <button
                type="button"
                style={styles.dangerButton}
                disabled={!!busy}
                onClick={() =>
                  destroy(pendingDestroy.id, pendingDestroy.deleteVolumes)
                }
              >
                {busyKeys.has(pendingDestroy.id) ? "Destroying..." : "Destroy"}
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
.containers-panel button:disabled { cursor: not-allowed !important; opacity: .45; }
.containers-panel :focus-visible { outline: 2px solid var(--accent, #38bdf8) !important; outline-offset: 2px; }
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
  root: {
    height: "100%",
    minHeight: 0,
    display: "flex",
    flexDirection: "column",
    color: "var(--text, #e5e7eb)",
    background: "var(--bg, #0b0b0c)",
  },
  header: {
    flexShrink: 0,
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 16,
    padding: "18px 24px",
    borderBottom: "1px solid var(--border, #2a2a2d)",
  },
  title: { margin: 0, fontSize: 18, fontWeight: 650 },
  subtitle: { marginTop: 4, fontSize: 12, color: "var(--text-dim, #8a8a8f)" },
  headerRight: { display: "flex", alignItems: "center", gap: 10 },
  statusText: { fontSize: 12, color: "var(--text-muted, #a1a1aa)" },
  main: {
    flex: 1,
    minHeight: 0,
    overflow: "auto",
    padding: 20,
    display: "grid",
    gridTemplateColumns: "minmax(520px, 1fr) 380px",
    gap: 16,
    alignItems: "start",
  },
  workloads: {
    minWidth: 0,
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 8,
    background: "var(--bg-card, #111114)",
    overflow: "hidden",
  },
  side: { display: "flex", flexDirection: "column", gap: 16, minWidth: 0 },
  sectionHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "14px 16px",
    borderBottom: "1px solid var(--border, #2a2a2d)",
  },
  sectionTitle: { margin: 0, fontSize: 14, fontWeight: 650 },
  muted: { fontSize: 12, color: "var(--text-dim, #8a8a8f)", lineHeight: 1.45 },
  error: {
    margin: "12px 20px 0",
    padding: "10px 12px",
    border: "1px solid rgba(239,68,68,.35)",
    borderRadius: 6,
    background: "rgba(239,68,68,.10)",
    color: "var(--error, #ef4444)",
    fontSize: 13,
    whiteSpace: "pre-wrap",
  },
  errorSmall: {
    padding: "8px 10px",
    borderRadius: 6,
    background: "rgba(239,68,68,.10)",
    color: "var(--error, #ef4444)",
    fontSize: 12,
  },
  empty: { padding: 24, color: "var(--text-muted, #a1a1aa)", fontSize: 14 },
  list: { display: "flex", flexDirection: "column" },
  row: {
    padding: 16,
    borderTop: "1px solid var(--border, #2a2a2d)",
    display: "flex",
    flexDirection: "column",
    gap: 12,
  },
  rowTop: {
    display: "flex",
    alignItems: "flex-start",
    justifyContent: "space-between",
    gap: 16,
  },
  rowMain: { minWidth: 0 },
  nameLine: { display: "flex", alignItems: "center", flexWrap: "wrap", gap: 8 },
  workloadName: { fontSize: 15, fontWeight: 650 },
  image: {
    marginTop: 4,
    fontFamily:
      "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
    fontSize: 12,
    color: "var(--text-muted, #a1a1aa)",
    overflowWrap: "anywhere",
  },
  pill: {
    display: "inline-flex",
    alignItems: "center",
    minHeight: 20,
    padding: "2px 7px",
    borderRadius: 999,
    border: "1px solid var(--border, #2a2a2d)",
    color: "var(--text-muted, #a1a1aa)",
    fontSize: 11,
    textTransform: "uppercase",
  },
  actions: { display: "flex", alignItems: "center", gap: 8, flexShrink: 0 },
  secondaryActions: { display: "flex", flexWrap: "wrap", gap: 12 },
  metaGrid: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))",
    gap: 8,
  },
  metaItem: {
    minWidth: 0,
    padding: "8px 10px",
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "var(--bg, #0b0b0c)",
  },
  metaLabel: {
    fontSize: 10,
    textTransform: "uppercase",
    color: "var(--text-dim, #8a8a8f)",
    marginBottom: 4,
  },
  metaValue: {
    fontSize: 12,
    color: "var(--text, #e5e7eb)",
    overflowWrap: "anywhere",
  },
  card: {
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 8,
    background: "var(--bg-card, #111114)",
    padding: 14,
  },
  quickGrid: { marginTop: 12, display: "grid", gap: 10 },
  quickCard: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 12,
    padding: 10,
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "var(--bg, #0b0b0c)",
  },
  quickTitle: { fontSize: 13, fontWeight: 650 },
  quickActions: { display: "flex", gap: 8, flexShrink: 0 },
  form: { marginTop: 12, display: "flex", flexDirection: "column", gap: 10 },
  fieldLabel: {
    display: "flex",
    flexDirection: "column",
    gap: 5,
    minWidth: 0,
    fontSize: 11,
    color: "var(--text-dim, #8a8a8f)",
  },
  fieldTitle: { fontSize: 11, color: "var(--text-dim, #8a8a8f)" },
  twoCols: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 },
  threeCols: { display: "grid", gridTemplateColumns: "1fr 1fr 1fr", gap: 8 },
  input: {
    width: "100%",
    boxSizing: "border-box",
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "var(--bg-input, #17171a)",
    color: "var(--text, #e5e7eb)",
    padding: "8px 10px",
    fontSize: 13,
    outline: "none",
  },
  textarea: {
    width: "100%",
    minHeight: 66,
    resize: "vertical",
    boxSizing: "border-box",
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "var(--bg-input, #17171a)",
    color: "var(--text, #e5e7eb)",
    padding: "8px 10px",
    fontSize: 12,
    fontFamily:
      "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
    outline: "none",
  },
  fileContent: {
    width: "100%",
    minHeight: 100,
    resize: "vertical",
    boxSizing: "border-box",
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "var(--bg-input, #17171a)",
    color: "var(--text, #e5e7eb)",
    padding: "8px 10px",
    fontSize: 12,
    fontFamily:
      "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
    outline: "none",
  },
  filesHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 8,
  },
  fileEditor: {
    display: "flex",
    flexDirection: "column",
    gap: 8,
    padding: 10,
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "var(--bg, #0b0b0c)",
  },
  fileFooter: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 8,
  },
  checkboxLabel: {
    display: "inline-flex",
    alignItems: "center",
    gap: 7,
    fontSize: 12,
    color: "var(--text-muted, #a1a1aa)",
  },
  button: {
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
    background: "transparent",
    color: "var(--text-muted, #a1a1aa)",
    padding: "6px 10px",
    fontSize: 12,
    cursor: "pointer",
  },
  primaryButton: {
    border: "1px solid var(--accent, #38bdf8)",
    borderRadius: 6,
    background: "var(--accent, #38bdf8)",
    color: "var(--bg, #0b0b0c)",
    padding: "6px 10px",
    fontSize: 12,
    fontWeight: 650,
    cursor: "pointer",
  },
  primaryButtonWide: {
    border: "1px solid var(--accent, #38bdf8)",
    borderRadius: 6,
    background: "var(--accent, #38bdf8)",
    color: "var(--bg, #0b0b0c)",
    padding: "9px 10px",
    fontSize: 13,
    fontWeight: 650,
    cursor: "pointer",
    width: "100%",
  },
  primaryLink: {
    border: "1px solid var(--accent, #38bdf8)",
    borderRadius: 6,
    color: "var(--accent, #38bdf8)",
    textDecoration: "none",
    padding: "6px 10px",
    fontSize: 12,
  },
  linkButton: {
    border: 0,
    background: "transparent",
    color: "var(--text-muted, #a1a1aa)",
    padding: 0,
    fontSize: 12,
    cursor: "pointer",
  },
  blueprints: { marginTop: 12, display: "grid", gap: 8 },
  blueprint: {
    padding: 10,
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 6,
  },
  blueprintTop: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: 8,
    marginBottom: 4,
  },
  modalBackdrop: {
    position: "fixed",
    inset: 0,
    background: "rgba(0,0,0,.62)",
    display: "flex",
    alignItems: "flex-end",
    justifyContent: "center",
    padding: 24,
    zIndex: 80,
  },
  confirmBackdrop: {
    position: "fixed",
    inset: 0,
    background: "rgba(0,0,0,.62)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: 24,
    zIndex: 80,
  },
  modal: {
    width: "min(100%, 980px)",
    maxHeight: "76vh",
    display: "flex",
    flexDirection: "column",
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 8,
    background: "var(--bg-card, #111114)",
    overflow: "hidden",
  },
  confirmModal: {
    width: "min(100%, 420px)",
    display: "flex",
    flexDirection: "column",
    border: "1px solid var(--border, #2a2a2d)",
    borderRadius: 8,
    background: "var(--bg-card, #111114)",
    overflow: "hidden",
    boxShadow: "0 20px 70px rgba(0,0,0,.42)",
  },
  modalHeader: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    padding: "12px 14px",
    borderBottom: "1px solid var(--border, #2a2a2d)",
  },
  modalBody: { padding: 14, display: "flex", flexDirection: "column", gap: 8 },
  modalFooter: {
    display: "flex",
    justifyContent: "flex-end",
    gap: 8,
    padding: 14,
    borderTop: "1px solid var(--border, #2a2a2d)",
  },
  confirmName: { fontSize: 15, fontWeight: 650, overflowWrap: "anywhere" },
  dangerButton: {
    border: "1px solid var(--error, #ef4444)",
    borderRadius: 6,
    background: "var(--error, #ef4444)",
    color: "white",
    padding: "6px 10px",
    fontSize: 12,
    fontWeight: 650,
    cursor: "pointer",
  },
  pre: {
    margin: 0,
    padding: 14,
    overflow: "auto",
    whiteSpace: "pre-wrap",
    fontFamily:
      "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
    fontSize: 12,
    color: "var(--text, #e5e7eb)",
  },
};
