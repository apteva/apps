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

const inputCls =
  "bg-surface-2 text-text border border-border rounded px-3 py-2 text-sm " +
  "placeholder:text-text-dim focus:outline-none focus:ring-1 focus:ring-accent";

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
  const [busy, setBusy] = useState("");
  const [logs, setLogs] = useState<{ name: string; body: string } | null>(null);
  const [form, setForm] = useState({
    name: "",
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

  const run = useCallback(async () => {
    setBusy("run");
    try {
      const ports = form.containerPort
        ? [{
            container_port: Number(form.containerPort),
            host_port: form.hostPort ? Number(form.hostPort) : 0,
            bind_addr: "127.0.0.1",
            protocol: "tcp",
          }]
        : [];
      await api("/workloads", {
        method: "POST",
        body: JSON.stringify({
          name: form.name,
          image: form.image,
          ports,
          health_path: form.healthPath || "/",
          resources: {
            memory_mb: Number(form.memoryMB || 0),
            cpu: Number(form.cpu || 0),
          },
        }),
      });
      setForm((f) => ({ ...f, name: "" }));
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy("");
    }
  }, [form, load]);

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
    <div className="h-full flex flex-col">
      <div className="px-6 pt-6 pb-3 border-b border-border flex items-center justify-between">
        <div>
          <h1 className="text-lg font-semibold">Containers</h1>
          <p className="text-xs text-text-dim mt-1">Local Docker workloads. Remote hosts, routes, backups, and blueprints come next.</p>
        </div>
        <button className="btn btn-sm" onClick={load} disabled={!!busy}>Refresh</button>
      </div>

      {err && <div className="mx-6 mt-4 rounded border border-red/40 bg-red/10 text-red px-3 py-2 text-sm">{err}</div>}

      <div className="p-6 grid gap-5 lg:grid-cols-[360px_1fr] overflow-auto">
        <section className="space-y-4">
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
            <button className="btn btn-primary w-full" onClick={run} disabled={busy === "run" || !form.name || !form.image}>
              Run container
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
                      {p.bind_addr}:{p.host_port} → {p.container_port}/{p.protocol}
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
