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
  const [selected, setSelected] = useState<FileRow | null>(null);
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
    try {
      await api("DELETE", `/files/${f.id}`);
      if (selected?.id === f.id) setSelected(null);
      load();
    } catch (e) {
      alert("Delete failed: " + (e as Error).message);
    }
  };

  const handleDownload = (f: FileRow) => {
    window.open(`${API}/files/${f.id}/content?${withParams({})}`, "_blank");
  };

  const contentURL = (f: FileRow) => `${API}/files/${f.id}/content?${withParams({})}`;

  const breadcrumbParts = folder.split("/").filter(Boolean);

  return (
    <div className="h-full flex">
    <div className="flex-1 flex flex-col p-6 gap-4 min-w-0">
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
          <div className="py-12 px-6 text-center text-text-muted text-sm">
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
              {files.map((f) => {
                const isSel = selected?.id === f.id;
                return (
                  <tr
                    key={f.id}
                    onClick={() => setSelected(f)}
                    className={`border-t border-border cursor-pointer ${isSel ? "bg-accent/10" : "hover:bg-bg-input/30"}`}
                  >
                    <td className="px-4 py-2">
                      <span className="text-text truncate max-w-md inline-block align-middle" title={f.name}>{f.name}</span>
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
                        onClick={(e) => { e.stopPropagation(); handleDownload(f); }}
                        className="text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
                      >Open</button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <div className="text-xs text-text-dim">{status}</div>
    </div>
    {selected && (
      <FileDetail
        file={selected}
        contentURL={contentURL(selected)}
        onClose={() => setSelected(null)}
        onDownload={() => handleDownload(selected)}
        onShare={() => handleShare(selected)}
        onDelete={() => handleDelete(selected)}
      />
    )}
    </div>
  );
}

function FileDetail({
  file, contentURL, onClose, onDownload, onShare, onDelete,
}: {
  file: FileRow;
  contentURL: string;
  onClose: () => void;
  onDownload: () => void;
  onShare: () => void;
  onDelete: () => void;
}) {
  const [confirmDelete, setConfirmDelete] = useState(false);
  // Reset the inline delete confirm when the user navigates to a
  // different file — otherwise the red bar carries over and the
  // operator could accidentally confirm a delete on a file they
  // didn't intend to act on.
  useEffect(() => { setConfirmDelete(false); }, [file.id]);

  return (
    <aside className="w-96 border-l border-border bg-bg-card flex flex-col">
      <header className="flex items-center gap-2 px-4 py-3 border-b border-border">
        <span className="text-text font-medium truncate flex-1" title={file.name}>{file.name}</span>
        <button
          type="button"
          onClick={onClose}
          className="text-text-muted hover:text-text text-lg leading-none px-1"
          aria-label="Close"
        >×</button>
      </header>

      <div className="flex-1 overflow-auto">
        <FilePreview file={file} contentURL={contentURL} />
        <dl className="px-4 py-3 text-xs flex flex-col gap-2">
          <MetaRow label="Size" value={formatSize(file.size_bytes)} />
          <MetaRow label="Type" value={file.content_type || "—"} />
          <MetaRow label="Folder" value={file.folder} />
          <div className="flex items-center gap-2">
            <span className="text-text-dim w-20 flex-shrink-0">Visibility</span>
            <span className={`text-[10px] px-1.5 py-0.5 rounded ${
              file.visibility === "public" ? "bg-green/15 text-green" :
              file.visibility === "signed" ? "bg-accent/15 text-accent" :
              "bg-border text-text-muted"
            }`}>{file.visibility}</span>
          </div>
          <MetaRow label="Created" value={new Date(file.created_at).toLocaleString()} />
          <MetaRow label="SHA-256" value={file.sha256 || "—"} mono />
          <MetaRow label="ID" value={file.id} mono />
        </dl>
      </div>

      <footer className="border-t border-border p-3">
        {confirmDelete ? (
          <div className="bg-red/10 border border-red/40 rounded p-2 flex items-center gap-2">
            <span className="text-xs text-red flex-1">Delete this file?</span>
            <button
              type="button"
              onClick={() => { setConfirmDelete(false); onDelete(); }}
              className="text-xs text-red font-medium hover:underline"
            >confirm</button>
            <button
              type="button"
              onClick={() => setConfirmDelete(false)}
              className="text-xs text-text-muted hover:text-text"
            >cancel</button>
          </div>
        ) : (
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={onDownload}
              className="flex-1 text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
            >Open</button>
            <button
              type="button"
              onClick={onShare}
              className="flex-1 text-xs px-2 py-1 border border-border rounded hover:bg-bg-input"
            >Share</button>
            <button
              type="button"
              onClick={() => setConfirmDelete(true)}
              className="text-xs px-2 py-1 text-red border border-red/40 rounded hover:bg-red/10"
              aria-label="Delete"
            >Delete</button>
          </div>
        )}
      </footer>
    </aside>
  );
}

function MetaRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex gap-2">
      <span className="text-text-dim w-20 flex-shrink-0">{label}</span>
      <span
        className={`flex-1 min-w-0 break-all ${mono ? "text-text-muted font-mono text-[10px]" : "text-text truncate"}`}
        title={value}
      >{value}</span>
    </div>
  );
}

function FilePreview({ file, contentURL }: { file: FileRow; contentURL: string }) {
  const ct = (file.content_type || "").toLowerCase();
  const TEXT_LIMIT = 256 * 1024;

  if (ct.startsWith("image/")) {
    return (
      <div className="bg-bg-input border-b border-border flex items-center justify-center" style={{ minHeight: "12rem", maxHeight: "20rem" }}>
        <img src={contentURL} alt={file.name} className="max-w-full" style={{ maxHeight: "20rem", objectFit: "contain" }} />
      </div>
    );
  }
  if (ct === "application/pdf") {
    return (
      <iframe
        src={contentURL}
        title={file.name}
        className="w-full border-b border-border bg-bg-input"
        style={{ height: "20rem" }}
      />
    );
  }
  const isText =
    ct.startsWith("text/") ||
    ct === "application/json" ||
    ct === "application/javascript" ||
    ct === "application/xml" ||
    ct === "application/x-yaml";
  if (isText && file.size_bytes <= TEXT_LIMIT) {
    return <TextPreview contentURL={contentURL} />;
  }

  // Generic placeholder — content type unknown or binary too large.
  return (
    <div className="bg-bg-input border-b border-border flex flex-col items-center justify-center text-text-dim text-xs" style={{ height: "8rem" }}>
      <div className="text-3xl mb-1" aria-hidden>📄</div>
      <div>No preview available</div>
    </div>
  );
}

function TextPreview({ contentURL }: { contentURL: string }) {
  const [text, setText] = useState<string | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let alive = true;
    fetch(contentURL, { credentials: "same-origin" })
      .then((r) => r.ok ? r.text() : Promise.reject(new Error(`${r.status}`)))
      .then((t) => { if (alive) setText(t); })
      .catch((e) => { if (alive) setErr(e.message); });
    return () => { alive = false; };
  }, [contentURL]);
  if (err) {
    return <div className="bg-bg-input border-b border-border p-3 text-xs text-red">Preview failed: {err}</div>;
  }
  if (text === null) {
    return <div className="bg-bg-input border-b border-border p-3 text-xs text-text-muted">Loading preview…</div>;
  }
  return (
    <pre className="bg-bg-input border-b border-border p-3 text-[11px] text-text font-mono whitespace-pre-wrap break-all overflow-auto" style={{ maxHeight: "20rem" }}>
      {text}
    </pre>
  );
}
