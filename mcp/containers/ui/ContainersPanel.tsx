import { useCallback, useEffect, useState } from "react";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface Workload {
  id: string;
  name: string;
  image: string;
  status: string;
  desired_status: string;
  health_status: string;
  public_url: string;
  last_error: string;
  created_at: string;
  ports?: Array<{ container_port: number; host_port: number; bind_addr: string; protocol: string }>;
  volumes?: Array<{ name: string; docker_volume_name: string; mount_path: string }>;
}

interface Blueprint {
  slug: string;
  name: string;
  description: string;
}

const API = "/api/apps/containers/api";

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

const inputCls =
  "bg-surface-2 text-text border border-border rounded px-3 py-2 text-sm " +
  "placeholder:text-text-dim focus:outline-none focus:ring-1 focus:ring-accent";

function imageName(image: string): string {
  const withoutTag = image.split("@")[0].split(":")[0] || "container";
  const base = withoutTag.split("/").pop() || "container";
  const clean = base.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^-+|-+$/g, "");
  return clean || "container";
}

function autoName(image: string): string {
  const suffix = Math.floor(Date.now() / 1000).toString(36);
  return `test-${imageName(image)}-${suffix}`;
}

function statusClass(status: string): string {
  if (status === "running") return "text-green";
  if (status === "creating") return "text-blue";
  if (status === "unhealthy" || status === "error") return "text-red";
  if (status === "stopped") return "text-yellow";
  return "text-text-dim";
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(`${API}${path}`, {
    credentials: "same-origin",
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
  });
  if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
  return (await r.json()) as T;
}

export default function ContainersPanel(_props: NativePanelProps) {
  const [workloads, setWorkloads] = useState<Workload[]>([]);
  const [blueprints, setBlueprints] = useState<Blueprint[]>([]);
  const [err, setErr] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [logs, setLogs] = useState<{ name: string; body: string } | null>(null);
  const [form, setForm] = useState({
    name: "test-nginx",
    image: "nginx:alpine",
    containerPort: "80",
    hostPort: "",
    healthPath: "/",
    memoryMB: "256",
    cpu: "0.5",
  });

  const load = useCallback(async () => {
    try {
      const [w, b] = await Promise.all([
        api<{ workloads: Workload[] }>("/workloads"),
        api<{ blueprints: Blueprint[] }>("/blueprints"),
      ]);
      setWorkloads(w.workloads || []);
      setBlueprints(b.blueprints || []);
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    }
  }, []);

  useEffect(() => {
    load();
    const t = window.setInterval(load, 10000);
    return () => window.clearInterval(t);
  }, [load]);

  const runSpec = useCallback(async (nextForm: typeof form) => {
    setBusy("run");
    setNotice("Pulling the image and starting the container. First run can take a minute.");
    try {
      const name = nextForm.name.trim() || autoName(nextForm.image);
      const ports = nextForm.containerPort
        ? [{
            container_port: Number(nextForm.containerPort),
            host_port: nextForm.hostPort ? Number(nextForm.hostPort) : 0,
            bind_addr: "127.0.0.1",
            protocol: "tcp",
          }]
        : [];
      await api("/workloads", {
        method: "POST",
        body: JSON.stringify({
          name,
          image: nextForm.image,
          ports,
          health_path: nextForm.healthPath || "/",
          resources: {
            memory_mb: Number(nextForm.memoryMB || 0),
            cpu: Number(nextForm.cpu || 0),
          },
        }),
      });
      setNotice(`Queued ${name}. Workload status will update in the list.`);
      setForm((f) => ({ ...f, name: autoName(f.image) }));
      await load();
    } catch (e) {
      setErr((e as Error).message);
      setNotice("");
    } finally {
      setBusy("");
    }
  }, [load]);

  const run = useCallback(async () => {
    await runSpec(form);
  }, [form, runSpec]);

  const fillTestImage = useCallback((preset: typeof TEST_IMAGES[number]) => {
    setForm({
      name: autoName(preset.image),
      image: preset.image,
      containerPort: preset.containerPort,
      hostPort: "",
      healthPath: preset.healthPath,
      memoryMB: preset.memoryMB,
      cpu: preset.cpu,
    });
  }, []);

  const runTestImage = useCallback(async (preset: typeof TEST_IMAGES[number]) => {
    await runSpec({
      name: autoName(preset.image),
      image: preset.image,
      containerPort: preset.containerPort,
      hostPort: "",
      healthPath: preset.healthPath,
      memoryMB: preset.memoryMB,
      cpu: preset.cpu,
    });
  }, [runSpec]);

  const action = useCallback(async (id: string, act: "start" | "stop" | "restart" | "health") => {
    setBusy(`${act}:${id}`);
    try {
      await api(`/workloads/${encodeURIComponent(id)}/${act}`, { method: "POST" });
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [load]);

  const destroy = useCallback(async (id: string, name: string) => {
    if (!confirm(`Destroy ${name}? Docker volumes are preserved.`)) return;
    setBusy(`destroy:${id}`);
    try {
      await api(`/workloads/${encodeURIComponent(id)}`, { method: "DELETE" });
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [load]);

  const showLogs = useCallback(async (w: Workload) => {
    setBusy(`logs:${w.id}`);
    try {
      const res = await api<{ logs: string }>(`/workloads/${encodeURIComponent(w.id)}/logs?tail=300`);
      setLogs({ name: w.name, body: res.logs || "" });
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
    }
  }, []);

  return (
    <div className="h-full min-h-0 flex flex-col">
      <div className="px-6 pt-6 pb-3 border-b border-border flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">Containers</h1>
          <p className="text-xs text-text-dim mt-1">Local Docker workloads. Remote hosts, routes, backups, and blueprints come next.</p>
        </div>
        <button className="btn btn-sm" onClick={load} disabled={!!busy}>Refresh</button>
      </div>

      {err && <div className="mx-6 mt-4 rounded border border-red/40 bg-red/10 text-red px-3 py-2 text-sm">{err}</div>}
      {notice && <div className="mx-6 mt-4 rounded border border-border bg-surface-2 text-text px-3 py-2 text-sm">{notice}</div>}

      <div className="min-h-0 flex-1 overflow-auto p-6 grid gap-5 lg:grid-cols-[minmax(0,1fr)_360px]">
        <section className="border border-border rounded bg-surface overflow-hidden">
          <div className="px-4 py-3 border-b border-border flex justify-between">
            <h2 className="text-sm font-semibold">Workloads</h2>
            <span className="text-xs text-text-dim">{workloads.length} total</span>
          </div>
          <div className="divide-y divide-border">
            {workloads.length === 0 && <div className="p-6 text-sm text-text-dim">No workloads yet.</div>}
            {workloads.map((w) => (
              <div key={w.id} className="p-4 space-y-3">
                <div className="flex items-start justify-between gap-4">
                  <div>
                    <div className="font-medium">{w.name}</div>
                    <div className="text-xs text-text-dim font-mono mt-1">{w.image}</div>
                  </div>
                  <div className="text-right">
                    <div className={`text-xs uppercase font-semibold ${statusClass(w.status)}`}>{w.status}</div>
                    <div className="text-xs text-text-dim mt-1">{w.health_status}</div>
                  </div>
                </div>
                <div className="flex flex-wrap gap-2 text-xs">
                  {w.public_url && <a className="text-accent underline" href={w.public_url} target="_blank" rel="noreferrer">{w.public_url}</a>}
                  {w.ports?.map((p) => (
                    <span key={`${p.container_port}-${p.host_port}`} className="px-2 py-1 rounded bg-surface-2 border border-border">
                      {p.bind_addr}:{p.host_port} -> {p.container_port}/{p.protocol}
                    </span>
                  ))}
                </div>
                {w.last_error && <div className="text-xs text-red">{w.last_error}</div>}
                <div className="flex flex-wrap gap-2">
                  <button className="btn btn-xs" disabled={!!busy} onClick={() => action(w.id, "start")}>Start</button>
                  <button className="btn btn-xs" disabled={!!busy} onClick={() => action(w.id, "stop")}>Stop</button>
                  <button className="btn btn-xs" disabled={!!busy} onClick={() => action(w.id, "restart")}>Restart</button>
                  <button className="btn btn-xs" disabled={!!busy} onClick={() => action(w.id, "health")}>Health</button>
                  <button className="btn btn-xs" disabled={!!busy} onClick={() => showLogs(w)}>Logs</button>
                  <button className="btn btn-xs text-red" disabled={!!busy} onClick={() => destroy(w.id, w.name)}>Destroy</button>
                </div>
              </div>
            ))}
          </div>
        </section>

        <section className="space-y-4">
          <div className="border border-border rounded bg-surface p-4 space-y-3">
            <h2 className="text-sm font-semibold">Quick Tests</h2>
            <div className="grid gap-2">
              {TEST_IMAGES.map((preset) => (
                <div key={preset.slug} className="rounded border border-border/60 p-3 space-y-2">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <div className="text-sm font-medium">{preset.label}</div>
                      <div className="text-xs text-text-dim font-mono mt-1">{preset.image}</div>
                    </div>
                    <div className="text-xs text-text-dim shrink-0">{preset.containerPort}/tcp</div>
                  </div>
                  <div className="flex gap-2">
                    <button className="btn btn-xs" onClick={() => fillTestImage(preset)} disabled={!!busy}>Fill</button>
                    <button className="btn btn-xs btn-primary" onClick={() => runTestImage(preset)} disabled={!!busy}>Run</button>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="border border-border rounded bg-surface p-4 space-y-3">
            <h2 className="text-sm font-semibold">Run Image</h2>
            <input className={inputCls} placeholder="name, e.g. demo-nginx" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            <input className={inputCls} placeholder="image" value={form.image} onChange={(e) => setForm({ ...form, image: e.target.value })} />
            <div className="grid grid-cols-2 gap-2">
              <input className={inputCls} placeholder="container port" value={form.containerPort} onChange={(e) => setForm({ ...form, containerPort: e.target.value })} />
              <input className={inputCls} placeholder="host port auto" value={form.hostPort} onChange={(e) => setForm({ ...form, hostPort: e.target.value })} />
            </div>
            <div className="grid grid-cols-3 gap-2">
              <input className={inputCls} placeholder="/health" value={form.healthPath} onChange={(e) => setForm({ ...form, healthPath: e.target.value })} />
              <input className={inputCls} placeholder="MB" value={form.memoryMB} onChange={(e) => setForm({ ...form, memoryMB: e.target.value })} />
              <input className={inputCls} placeholder="CPU" value={form.cpu} onChange={(e) => setForm({ ...form, cpu: e.target.value })} />
            </div>
            <button className="btn btn-primary w-full" onClick={run} disabled={busy === "run" || !form.image}>
              {busy === "run" ? "Starting container..." : "Run container"}
            </button>
          </div>

          <div className="border border-border rounded bg-surface p-4">
            <h2 className="text-sm font-semibold mb-3">Blueprints</h2>
            <div className="space-y-2">
              {blueprints.map((b) => (
                <div key={b.slug} className="rounded border border-border/60 p-3">
                  <div className="text-sm font-medium">{b.name}</div>
                  <div className="text-xs text-text-dim mt-1">{b.description}</div>
                </div>
              ))}
            </div>
          </div>
        </section>

      </div>

      {logs && (
        <div className="fixed inset-0 bg-black/50 flex items-end justify-center p-6" onClick={() => setLogs(null)}>
          <div className="bg-surface border border-border rounded max-h-[70vh] w-full max-w-5xl flex flex-col" onClick={(e) => e.stopPropagation()}>
            <div className="px-4 py-3 border-b border-border flex justify-between">
              <div className="font-medium">Logs: {logs.name}</div>
              <button className="btn btn-xs" onClick={() => setLogs(null)}>Close</button>
            </div>
            <pre className="p-4 overflow-auto text-xs font-mono whitespace-pre-wrap">{logs.body || "(no logs)"}</pre>
          </div>
        </div>
      )}
    </div>
  );
}
