import { useCallback, useEffect, useId, useState, type ReactNode } from "react";

const API = "/api/apps/dlna";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Status {
  project_id: string;
  friendly_name: string;
  uuid: string;
  lan_ip: string;
  http_port: number;
  broadcasting: boolean;
  published_folders: number;
  recent_clients: number;
  storage_reachable: boolean;
  media_reachable: boolean;
  catalog_truncated: boolean;
  update_id: number;
}

interface Settings {
  friendly_name: string;
  publish_root_by_default: boolean;
  media_metadata: boolean;
  client_log_retention_hours: number;
  signed_url_ttl_seconds: number;
  catalog_cache_seconds: number;
}

interface PublishedFolder {
  id: number;
  folder: string;
  label: string;
  created_at: string;
}

interface ClientEntry {
  ip: string;
  user_agent: string;
  last_object_id: string;
  last_action_at: string;
  browse_count: number;
}

type APIPath = (path: string, query?: Record<string, string>) => string;

async function jsonRequest<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init);
  if (!response.ok) {
    const detail = (await response.text()).trim();
    throw new Error(detail || `${response.status} ${response.statusText}`);
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export default function DLNAPanel({ installId, projectId }: NativePanelProps) {
  const api = useCallback<APIPath>((pathname, query = {}) => {
    const params = new URLSearchParams({
      project_id: projectId,
      install_id: String(installId),
      ...query,
    });
    return `${API}${pathname}?${params}`;
  }, [installId, projectId]);

  const [status, setStatus] = useState<Status | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [folders, setFolders] = useState<PublishedFolder[]>([]);
  const [clients, setClients] = useState<ClientEntry[]>([]);
  const [showAdd, setShowAdd] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  const refresh = useCallback(async (signal?: AbortSignal, includeSettings = false) => {
    try {
      const [nextStatus, nextFolders, nextClients] = await Promise.all([
        jsonRequest<Status>(api("/status"), { signal }),
        jsonRequest<PublishedFolder[]>(api("/published_folders"), { signal }),
        jsonRequest<ClientEntry[]>(api("/clients", { limit: "20" }), { signal }),
      ]);
      setStatus(nextStatus);
      setFolders(nextFolders);
      setClients(nextClients);
      if (includeSettings) setSettings(await jsonRequest<Settings>(api("/settings"), { signal }));
      setError("");
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") return;
      setError(err instanceof Error ? err.message : String(err));
    }
  }, [api]);

  useEffect(() => {
    let stopped = false;
    let timer = 0;
    let controller: AbortController | null = null;
    let first = true;
    const poll = async () => {
      controller = new AbortController();
      await refresh(controller.signal, first);
      first = false;
      if (!stopped) timer = window.setTimeout(poll, 15_000);
    };
    void poll();
    return () => {
      stopped = true;
      controller?.abort();
      window.clearTimeout(timer);
    };
  }, [refresh]);

  const removeFolder = async (folder: PublishedFolder) => {
    if (!window.confirm(`Stop sharing ${folder.label || folder.folder} with LAN devices?`)) return;
    setBusy(`remove:${folder.id}`);
    try {
      await jsonRequest<void>(api(`/published_folders/${folder.id}`), { method: "DELETE" });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  const saveSettings = async () => {
    if (!settings) return;
    if (settings.publish_root_by_default && !window.confirm("This exposes every storage file to every device on the LAN. Continue?")) return;
    setBusy("settings");
    try {
      const next = await jsonRequest<Settings>(api("/settings"), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      });
      setSettings(next);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  const announce = async () => {
    setBusy("announce");
    try {
      await jsonRequest(api("/announce"), { method: "POST" });
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="flex flex-col h-full overflow-auto">
      <StatusCard status={status} error={error} onAnnounce={announce} announcing={busy === "announce"} />

      {settings && (
        <Section title="Server settings">
          <div className="p-3 grid gap-3 md:grid-cols-2">
            <Field label="Name shown on TVs">
              <input
                className="w-full bg-bg-elev border border-border rounded px-2 py-1.5 text-sm"
                value={settings.friendly_name}
                placeholder="Apteva (hostname)"
                maxLength={128}
                onChange={(event) => setSettings({ ...settings, friendly_name: event.target.value })}
              />
            </Field>
            <Field label="Catalog cache (seconds)">
              <input
                type="number" min={1} max={300}
                className="w-full bg-bg-elev border border-border rounded px-2 py-1.5 text-sm"
                value={settings.catalog_cache_seconds}
                onChange={(event) => setSettings({ ...settings, catalog_cache_seconds: Number(event.target.value) })}
              />
            </Field>
            <Check
              checked={settings.media_metadata}
              onChange={(checked) => setSettings({ ...settings, media_metadata: checked })}
              label="Add duration and resolution metadata"
            />
            <Check
              checked={settings.publish_root_by_default}
              onChange={(checked) => setSettings({ ...settings, publish_root_by_default: checked })}
              label="Share all storage (not recommended)"
              warning
            />
            <div className="md:col-span-2 flex justify-end">
              <button type="button" className="bg-accent text-bg px-3 py-1.5 rounded text-sm disabled:opacity-50" onClick={saveSettings} disabled={busy === "settings"}>
                {busy === "settings" ? "Saving…" : "Save settings"}
              </button>
            </div>
          </div>
        </Section>
      )}

      <Section
        title="Published folders"
        action={<button type="button" className="bg-accent text-bg px-3 py-1 rounded text-sm" onClick={() => setShowAdd(true)}>+ Add folder</button>}
      >
        {folders.length === 0 ? (
          <Empty hint="Nothing is exposed to the LAN yet — add at least one folder." />
        ) : folders.map((folder) => (
          <Row key={folder.id}>
            <span className="font-mono text-sm min-w-0 truncate">{folder.folder}</span>
            <span className="text-text-dim text-sm flex-1 truncate ml-3">{folder.label || <em className="opacity-60">(no label)</em>}</span>
            <button
              type="button"
              className="text-text-dim hover:text-error text-sm disabled:opacity-50"
              onClick={() => removeFolder(folder)}
              disabled={busy === `remove:${folder.id}`}
              aria-label={`Unpublish ${folder.label || folder.folder}`}
              title="Unpublish"
            >
              {busy === `remove:${folder.id}` ? "…" : "✕"}
            </button>
          </Row>
        ))}
      </Section>

      <Section title={`Recent clients (last 24h, ${status?.recent_clients ?? 0})`}>
        {clients.length === 0 ? (
          <Empty hint="No client has browsed yet. A TV or VLC will appear after it discovers this server." />
        ) : clients.map((client) => (
          <Row key={`${client.ip}|${client.user_agent}`}>
            <span className="font-mono text-sm w-36 truncate">{client.ip}</span>
            <span className="text-sm flex-1 truncate ml-3" title={client.user_agent}>{client.user_agent || <em className="opacity-60">(no user agent)</em>}</span>
            <span className="text-text-dim text-xs w-24 text-right">{timeAgo(client.last_action_at)}</span>
            <span className="text-text-dim text-xs w-16 text-right">{client.browse_count} hits</span>
          </Row>
        ))}
      </Section>

      {showAdd && <AddFolderModal api={api} onClose={() => setShowAdd(false)} onAdded={() => { setShowAdd(false); void refresh(); }} />}
    </div>
  );
}

function StatusCard({ status, error, onAnnounce, announcing }: { status: Status | null; error: string; onAnnounce: () => void; announcing: boolean }) {
  return (
    <div className="px-4 py-3 border-b border-border">
      {error && <div role="alert" className="mb-2 rounded bg-error/10 px-3 py-2 text-sm text-error">{error}</div>}
      {!status ? <div className="text-sm text-text-dim">Loading DLNA status…</div> : (
        <>
          <div className="flex items-center gap-2 text-sm">
            <span aria-hidden className={`w-2 h-2 rounded-full ${status.broadcasting ? "bg-success" : "bg-error"}`} />
            <span className="flex-1">
              {status.broadcasting ? <>Broadcasting as <strong>“{status.friendly_name}”</strong></> : <span className="text-error">Not broadcasting — check multicast/host networking</span>}
            </span>
            <button type="button" className="border border-border rounded px-2 py-1 text-xs disabled:opacity-50" onClick={onAnnounce} disabled={announcing || !status.broadcasting}>
              {announcing ? "Announcing…" : "Announce now"}
            </button>
          </div>
          <div className="text-xs text-text-dim mt-1">
            UUID <span className="font-mono">{status.uuid.slice(0, 8)}…</span> · {status.lan_ip}:{status.http_port} · storage <Dot ok={status.storage_reachable} /> · media <Dot ok={status.media_reachable} /> · update {status.update_id}
          </div>
          {status.catalog_truncated && <div className="mt-2 text-xs text-warning">A physical folder contains at least 500 files; its DLNA listing may be incomplete.</div>}
        </>
      )}
    </div>
  );
}

function Dot({ ok }: { ok: boolean }) {
  return <span title={ok ? "reachable" : "unavailable"} className={`inline-block w-1.5 h-1.5 rounded-full align-middle ${ok ? "bg-success" : "bg-text-dim"}`} />;
}

function AddFolderModal({ api, onClose, onAdded }: { api: APIPath; onClose: () => void; onAdded: () => void }) {
  const [parent, setParent] = useState("/");
  const [folders, setFolders] = useState<string[]>([]);
  const [picked, setPicked] = useState("");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    setError("");
    jsonRequest<{ folders?: string[] }>(api("/storage_folders", { parent }), { signal: controller.signal })
      .then((response) => setFolders(Array.isArray(response.folders) ? response.folders : []))
      .catch((err) => {
        if (!(err instanceof DOMException && err.name === "AbortError")) setError(`Storage: ${err instanceof Error ? err.message : String(err)}`);
      });
    return () => controller.abort();
  }, [api, parent]);

  const childPath = (name: string) => parent === "/" ? `/${name}` : `${parent}/${name}`;
  const up = () => setParent(parent === "/" ? "/" : parent.slice(0, parent.lastIndexOf("/")) || "/");

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await jsonRequest(api("/published_folders"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ folder: picked, label }),
      });
      onAdded();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="Publish a folder" onClose={onClose}>
      <div className="space-y-3">
        <div>
          <div className="flex items-center gap-2 mb-2">
            <button type="button" className="border border-border rounded px-2 py-1 text-xs disabled:opacity-50" onClick={up} disabled={parent === "/"}>← Up</button>
            <span className="font-mono text-xs truncate">{parent}</span>
            <button type="button" className="ml-auto text-xs text-accent" onClick={() => setPicked(parent)}>Select this folder</button>
          </div>
          <div className="border border-border rounded max-h-44 overflow-auto">
            {folders.length === 0 ? <Empty hint="No subfolders here." /> : folders.map((name) => (
              <button type="button" key={name} className="w-full text-left px-3 py-2 border-b border-border last:border-0 text-sm hover:bg-bg-elev" onClick={() => setParent(childPath(name))}>📁 {name}</button>
            ))}
          </div>
        </div>
        <Field label="Selected storage path">
          <input className="w-full bg-bg-elev border border-border rounded px-2 py-1.5 text-sm font-mono" value={picked} placeholder="/movies/kids" onChange={(event) => setPicked(event.target.value)} />
        </Field>
        <Field label="Display label (optional)">
          <input className="w-full bg-bg-elev border border-border rounded px-2 py-1.5 text-sm" value={label} maxLength={128} placeholder="Kids’ movies" onChange={(event) => setLabel(event.target.value)} />
        </Field>
        <p className="text-xs text-text-dim">DLNA clients cannot authenticate. Every device on your LAN can browse and play files underneath the selected path.</p>
        {error && <div role="alert" className="text-sm text-error">{error}</div>}
        <div className="flex gap-2 justify-end pt-2">
          <button type="button" className="px-3 py-1 text-sm" onClick={onClose}>Cancel</button>
          <button type="button" className="bg-accent text-bg px-3 py-1 rounded text-sm disabled:opacity-50" onClick={submit} disabled={busy || !picked}>{busy ? "Publishing…" : "Publish"}</button>
        </div>
      </div>
    </Modal>
  );
}

function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <section className="border-b border-border"><div className="flex items-center px-4 py-2"><h3 className="text-sm font-medium flex-1">{title}</h3>{action}</div><div className="px-4 pb-3"><div className="border border-border rounded">{children}</div></div></section>;
}

function Row({ children }: { children: ReactNode }) {
  return <div className="flex items-center gap-2 px-3 py-2 border-b border-border last:border-b-0">{children}</div>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block"><span className="block text-xs text-text-dim mb-1">{label}</span>{children}</label>;
}

function Check({ checked, onChange, label, warning = false }: { checked: boolean; onChange: (checked: boolean) => void; label: string; warning?: boolean }) {
  return <label className={`flex items-center gap-2 text-sm ${warning && checked ? "text-warning" : ""}`}><input type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} />{label}</label>;
}

function Empty({ hint }: { hint: string }) {
  return <div className="text-xs text-text-dim italic px-3 py-3">{hint}</div>;
}

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const titleId = useId();
  useEffect(() => {
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
  }, [onClose]);
  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-3" onMouseDown={(event) => { if (event.currentTarget === event.target) onClose(); }}>
      <div role="dialog" aria-modal="true" aria-labelledby={titleId} className="bg-bg border border-border rounded-lg p-4 w-[480px] max-w-full max-h-[90vh] overflow-auto">
        <div className="flex items-center mb-3"><h3 id={titleId} className="text-base font-medium flex-1">{title}</h3><button type="button" className="text-text-dim hover:text-text text-lg" onClick={onClose} aria-label="Close dialog">×</button></div>
        {children}
      </div>
    </div>
  );
}

function timeAgo(rfc3339: string): string {
  const time = new Date(rfc3339).getTime();
  if (!Number.isFinite(time)) return "—";
  const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}
