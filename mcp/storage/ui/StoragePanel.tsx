// StoragePanel — native React port of the storage app's FilesPanel.
// Talks to the storage sidecar via /api/apps/storage/* (the platform
// proxy injects the per-install bearer token). Inherits the dashboard
// theme via Tailwind tokens.

import { useCallback, useEffect, useRef, useState } from "react";
interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}
interface FileRow {
  id: string;
  name: string;
  folder: string;
  size_bytes: number;
  content_type: string;
  visibility: "private" | "signed" | "public";
  sha256: string;
  created_at: string;
}

interface FoldersResp { folders?: string[] }
interface FilesResp { files?: FileRow[] }

const API = "/api/apps/storage";

function formatSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} kB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export default function StoragePanel({ projectId, installId }: NativePanelProps) {
  const [folder, setFolder] = useState("/");
  const [folders, setFolders] = useState<string[]>([]);
  const [files, setFiles] = useState<FileRow[]>([]);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [newFolder, setNewFolder] = useState("");
  const uploadRef = useRef<HTMLInputElement | null>(null);

  const withParams = useCallback((extra: Record<string, string>) => {
    const u = new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra });
    return u.toString();
  }, [projectId, installId]);

  const api = useCallback(async <T,>(method: string, path: string, params?: Record<string, string>, body?: any): Promise<T> => {
    const opts: RequestInit = { method, credentials: "same-origin", headers: {} };
    if (body && !(body instanceof FormData)) {
      (opts.headers as Record<string, string>)["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    } else if (body) {
      opts.body = body;
    }
    const qs = withParams(params || {});
    const res = await fetch(`${API}${path}?${qs}`, opts);
    if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
    return res.json();
  }, [withParams]);

  const load = useCallback(async () => {
    setBusy(true);
    try {
      const [foldersResp, filesResp] = await Promise.all([
        api<FoldersResp>("GET", "/folders", { parent: folder }),
        api<FilesResp>("GET", "/files", { folder }),
      ]);
      setFolders(foldersResp.folders || []);
      setFiles(filesResp.files || []);
      const total = (filesResp.files || []).length;
      const subs = (foldersResp.folders || []).length;
      setStatus(`${total} file${total !== 1 ? "s" : ""} · ${subs} folder${subs !== 1 ? "s" : ""}`);
    } catch (e) {
      setStatus("Error: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [folder, api]);

  useEffect(() => { load(); }, [load]);

  const handleUpload = async (ev: React.ChangeEvent<HTMLInputElement>) => {
    const fileList = Array.from(ev.target.files || []);
    if (fileList.length === 0) return;
    setStatus(`Uploading ${fileList.length} file${fileList.length !== 1 ? "s" : ""}…`);
    setBusy(true);
    try {
      for (const file of fileList) {
        const fd = new FormData();
        fd.append("file", file);
        fd.append("folder", folder);
        await fetch(`${API}/files?${withParams({})}`, {
          method: "POST", credentials: "same-origin", body: fd,
        });
      }
    } finally {
      ev.target.value = "";
      setBusy(false);
      load();
    }
  };

  const handleMakeFolder = async () => {
    const name = newFolder.trim();
    if (!name) return;
    // S3-style: a folder exists when a file does. Drop a placeholder.
    const fd = new FormData();
    fd.append("file", new Blob([""], { type: "text/plain" }), ".placeholder");
    fd.append("folder", folder + name + "/");
    try {
      await fetch(`${API}/files?${withParams({})}`, {
        method: "POST", credentials: "same-origin", body: fd,
      });
      setNewFolder("");
      load();
    } catch (e) {
      alert("Create folder failed: " + (e as Error).message);
    }
  };

  const handleShare = async (f: FileRow) => {
    try {
      await api("PATCH", `/files/${f.id}`, undefined, { visibility: "signed" });
      const url = window.location.origin + `${API}/files/${f.id}/content?${withParams({})}`;
      await navigator.clipboard.writeText(url).catch(() => {});
      alert(`Marked signed. URL copied to clipboard:\n${url}`);
      load();
    } catch (e) {
      alert("Share failed: " + (e as Error).message);
    }
  };

  const handleDelete = async (f: FileRow) => {
    if (!confirm(`Delete ${f.name}?`)) return;
    try {
      await api("DELETE", `/files/${f.id}`);
      load();
    } catch (e) {
      alert("Delete failed: " + (e as Error).message);
    }
  };

  const handleDownload = (f: FileRow) => {
    window.open(`${API}/files/${f.id}/content?${withParams({})}`, "_blank");
  };

  const breadcrumbParts = folder.split("/").filter(Boolean);

  return (
    <div className="h-full flex flex-col p-6 gap-4">
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <nav className="flex items-center gap-1 text-sm">
          <button
            type="button"
            className="text-accent hover:underline"
            onClick={() => setFolder("/")}
          >/</button>
          {breadcrumbParts.map((part, i) => {
            const target = "/" + breadcrumbParts.slice(0, i + 1).join("/") + "/";
            const last = i === breadcrumbParts.length - 1;
            return (
              <span key={target} className="flex items-center gap-1">
                <span className="text-text-dim">/</span>
                {last ? (
                  <span className="text-text">{part}</span>
                ) : (
                  <button
                    type="button"
                    className="text-accent hover:underline"
                    onClick={() => setFolder(target)}
                  >{part}</button>
                )}
              </span>
            );
          })}
        </nav>
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={newFolder}
            onChange={(e) => setNewFolder(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") handleMakeFolder(); }}
            placeholder="new folder…"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm w-40"
          />
          <button
            type="button"
            onClick={handleMakeFolder}
            disabled={!newFolder.trim() || busy}
            className="px-2 py-1 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50"
          >+ Folder</button>
          <button
            type="button"
            onClick={() => uploadRef.current?.click()}
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >Upload</button>
          <input
            ref={uploadRef}
            type="file"
            multiple
            onChange={handleUpload}
            className="hidden"
          />
        </div>
      </div>

      <div className="flex-1 overflow-auto border border-border rounded">
        {folders.length === 0 && files.length === 0 ? (
          <div className="px-12 py-24 text-center text-text-muted text-sm">
            {busy ? "Loading…" : "Empty folder. Drop a file or create a sub-folder to get started."}
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs uppercase tracking-wide bg-bg-input/50">
              <tr>
                <th className="text-left px-4 py-2 font-normal">Name</th>
                <th className="text-left px-4 py-2 font-normal w-24">Size</th>
                <th className="text-left px-4 py-2 font-normal w-24">Visibility</th>
                <th className="text-right px-4 py-2 font-normal w-32">Actions</th>
              </tr>
            </thead>
            <tbody>
              {folders.map((f) => (
                <tr key={`folder-${f}`} className="border-t border-border hover:bg-bg-input/30">
                  <td className="px-4 py-2">
                    <button
                      type="button"
                      onClick={() => setFolder(folder + f + "/")}
                      className="text-accent hover:underline flex items-center gap-1"
                    >
                      <span aria-hidden>📁</span>
                      <span>{f}</span>
                    </button>
                  </td>
                  <td className="px-4 py-2 text-text-dim">—</td>
                  <td className="px-4 py-2 text-text-dim">folder</td>
                  <td className="px-4 py-2"></td>
                </tr>
              ))}
              {files.map((f) => (
                <tr key={f.id} className="border-t border-border hover:bg-bg-input/30">
                  <td className="px-4 py-2">
                    <button
                      type="button"
                      onClick={() => handleDownload(f)}
                      className="text-accent hover:underline truncate max-w-md text-left"
                      title={f.name}
                    >{f.name}</button>
                  </td>
                  <td className="px-4 py-2 text-text-muted">{formatSize(f.size_bytes)}</td>
                  <td className="px-4 py-2">
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${
                      f.visibility === "public" ? "bg-green/15 text-green" :
                      f.visibility === "signed" ? "bg-accent/15 text-accent" :
                      "bg-border text-text-muted"
                    }`}>{f.visibility}</span>
                  </td>
                  <td className="px-4 py-2 text-right">
                    <button
                      type="button"
                      onClick={() => handleShare(f)}
                      className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input mr-1"
                    >Share</button>
                    <button
                      type="button"
                      onClick={() => handleDelete(f)}
                      className="text-xs px-2 py-1 text-red hover:bg-red/10 rounded"
                    >✕</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="text-xs text-text-dim">{status}</div>
    </div>
  );
}
