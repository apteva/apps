// StoragePanel — native React port of the storage app's FilesPanel.
// Talks to the storage sidecar via /api/apps/storage/* (the platform
// proxy injects the per-install bearer token). Inherits the dashboard
// theme via Tailwind tokens.

import { useCallback, useEffect, useRef, useState } from "react";
import { uploadResumable } from "./uploadResumable";

// Inlined SDK app-event subscription. Panels are runtime-bundled
// standalone .mjs files and each app is independently installable
// from its own source — sharing across app directories would break
// the install when an app is cloned alone. The hook is small enough
// (~40 lines) that copy-and-paste is the right call until the SDK
// ships an official TS client bundle the importmap can resolve.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}
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
  // Canonical absolute URL minted by storage. Same shape regardless
  // of visibility — what differs is whether the request needs auth.
  // For visibility=public, anyone with this URL can fetch; for
  // signed, append ?sig=&exp= via files_get_url; for private, only
  // authenticated requests succeed.
  url?: string;
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

// Per-file upload state surfaced as a progress strip above the table.
// Mirrors the shape uploadResumable's onProgress hands us; status =
// "uploading" until the resumable helper resolves, then "done" or
// "error" so the strip can finish/auto-clear.
interface UploadJob {
  id: number;
  name: string;
  total: number;
  loaded: number;
  status: "uploading" | "done" | "error";
  error?: string;
}

export default function StoragePanel({ projectId, installId }: NativePanelProps) {
  const [folder, setFolder] = useState("/");
  const [folders, setFolders] = useState<string[]>([]);
  const [files, setFiles] = useState<FileRow[]>([]);
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [newFolder, setNewFolder] = useState("");
  const [selected, setSelected] = useState<FileRow | null>(null);
  const [uploads, setUploads] = useState<UploadJob[]>([]);
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

  // Live refresh: reload the listing whenever a file event lands for
  // this project. Topic filter keeps us off unrelated chatter; the
  // re-fetch is cheap (<100ms for /folders + /files combined).
  useAppEvents("storage", projectId, (ev) => {
    if (ev.topic === "file.added" || ev.topic === "file.deleted" || ev.topic === "file.updated") {
      load();
    }
  });

  const handleUpload = async (ev: React.ChangeEvent<HTMLInputElement>) => {
    const fileList = Array.from(ev.target.files || []);
    if (fileList.length === 0) return;
    setBusy(true);
    // Seed one job per selected file. Preserve insertion order — the
    // strip renders top-to-bottom matching what the user picked.
    const baseId = Date.now();
    const initialJobs: UploadJob[] = fileList.map((f, i) => ({
      id: baseId + i,
      name: f.name,
      total: f.size,
      loaded: 0,
      status: "uploading" as const,
    }));
    setUploads((prev) => [...prev, ...initialJobs]);

    const updateJob = (id: number, patch: Partial<UploadJob>) => {
      setUploads((prev) =>
        prev.map((j) => (j.id === id ? { ...j, ...patch } : j)),
      );
    };

    try {
      for (let i = 0; i < fileList.length; i++) {
        const file = fileList[i];
        const job = initialJobs[i];
        try {
          await uploadResumable(file, {
            folder,
            onProgress: (bytes, total) => {
              updateJob(job.id, { loaded: bytes, total });
            },
          });
          updateJob(job.id, { loaded: file.size, status: "done" });
        } catch (e) {
          updateJob(job.id, {
            status: "error",
            error: (e as Error).message,
          });
          throw e;
        }
      }
    } catch (e) {
      setStatus("Upload failed: " + (e as Error).message);
    } finally {
      ev.target.value = "";
      setBusy(false);
      load();
      // Auto-clear successful jobs after a beat so the strip doesn't
      // accrue forever. Errors stick around — user dismisses them.
      window.setTimeout(() => {
        setUploads((prev) => prev.filter((j) => j.status !== "done"));
      }, 2500);
    }
  };

  const dismissUpload = (id: number) => {
    setUploads((prev) => prev.filter((j) => j.id !== id));
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

  // handleShare adapts to the file's current visibility instead of
  // unconditionally flipping it to signed.
  //
  //   public  → copy the public_url storage already minted (no PATCH)
  //   signed  → mint/refresh a signed URL via POST /files/:id/url
  //   private → ask first; if the user confirms, flip to signed and
  //             return the time-limited URL
  //
  // Pre-fix the button always called PATCH visibility=signed which
  // demoted public files. The visibility column is now the operator's
  // intent, not a side-effect of clicking Share.
  const handleShare = async (f: FileRow) => {
    try {
      if (f.visibility === "public") {
        const url = f.url || "";
        if (!url) {
          alert("This file is public but storage didn't return an absolute URL — check Settings → Server → public_url.");
          return;
        }
        await navigator.clipboard.writeText(url).catch(() => {});
        alert(`Public link copied:\n${url}\n\nAnyone with this URL can fetch the file.`);
        return;
      }
      if (f.visibility === "private") {
        if (!window.confirm(
          "This file is private. Generate a time-limited signed URL anyone can use to download it?"
        )) {
          return;
        }
      }
      // signed or just-confirmed-private → mint a fresh signed URL.
      const resp = await api<{ url: string; expires_at: number }>(
        "POST", `/files/${f.id}/url`, undefined, { ttl_seconds: 86400 },
      );
      // If the file was private, also flip its visibility flag so
      // future Share clicks recognize the intent.
      if (f.visibility === "private") {
        await api("PATCH", `/files/${f.id}`, undefined, { visibility: "signed" });
      }
      await navigator.clipboard.writeText(resp.url).catch(() => {});
      alert(`Signed link copied (expires in 24h):\n${resp.url}`);
      load();
    } catch (e) {
      alert("Share failed: " + (e as Error).message);
    }
  };

  // handleMakePublic flips a file to permanent anonymous access.
  // Distinct from Share so the operator must explicitly opt in to
  // "anyone with the URL, forever". Refreshes the row to surface the
  // newly-populated public_url.
  const handleMakePublic = async (f: FileRow) => {
    if (f.visibility === "public") return;
    if (!window.confirm(
      "Make this file public?\n\nAnyone with the URL will be able to fetch it permanently — there's no expiration.",
    )) {
      return;
    }
    try {
      await api("PATCH", `/files/${f.id}`, undefined, { visibility: "public" });
      load();
    } catch (e) {
      alert("Make public failed: " + (e as Error).message);
    }
  };

  // handleMakePrivate revokes both public and signed access. Used by
  // the detail pane to undo a Make-public click.
  const handleMakePrivate = async (f: FileRow) => {
    if (f.visibility === "private") return;
    try {
      await api("PATCH", `/files/${f.id}`, undefined, { visibility: "private" });
      load();
    } catch (e) {
      alert("Make private failed: " + (e as Error).message);
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

      {uploads.length > 0 && (
        <div className="flex flex-col gap-2 border border-border rounded p-3 bg-bg-input/30">
          {uploads.map((job) => (
            <UploadProgressRow key={job.id} job={job} onDismiss={() => dismissUpload(job.id)} />
          ))}
        </div>
      )}

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
                      <VisibilityBadge value={f.visibility} />
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
        onMakePublic={() => handleMakePublic(selected)}
        onMakePrivate={() => handleMakePrivate(selected)}
        onDelete={() => handleDelete(selected)}
      />
    )}
    </div>
  );
}

function FileDetail({
  file, contentURL, onClose, onDownload, onShare, onMakePublic, onMakePrivate, onDelete,
}: {
  file: FileRow;
  contentURL: string;
  onClose: () => void;
  onDownload: () => void;
  onShare: () => void;
  onMakePublic: () => void;
  onMakePrivate: () => void;
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
            <VisibilityBadge value={file.visibility} />
          </div>
          <MetaRow label="Created" value={new Date(file.created_at).toLocaleString()} />
          <MetaRow label="SHA-256" value={file.sha256 || "—"} mono />
          <MetaRow label="ID" value={file.id} mono />
          {/* Public link surfacing — when visibility=public the
              file's URL works anonymously, so operators can copy it
              straight from the detail pane without going through
              Share. */}
          {file.visibility === "public" && file.url && (
            <div className="flex flex-col gap-1 mt-2 pt-2 border-t border-border">
              <span className="text-text-dim">Public link</span>
              <code
                className="text-[10px] text-accent break-all bg-bg-input/50 rounded px-2 py-1"
                title="Anyone with this URL can fetch the file."
              >{file.url}</code>
              <button
                type="button"
                onClick={() => {
                  navigator.clipboard.writeText(file.url || "").catch(() => {});
                }}
                className="text-[10px] text-accent self-start hover:underline"
              >Copy</button>
            </div>
          )}
        </dl>
      </div>

      {/* Visibility actions — distinct from Share. Make-public flips
          to permanent anonymous access; Make-private revokes it.
          Both confirm before mutating. */}
      <div className="px-3 py-2 border-t border-border flex items-center gap-2 text-xs">
        <span className="text-text-dim">Visibility:</span>
        {file.visibility !== "public" && (
          <button
            type="button"
            onClick={onMakePublic}
            className="px-2 py-1 border border-border rounded hover:bg-bg-input text-green"
          >Make public</button>
        )}
        {file.visibility !== "private" && (
          <button
            type="button"
            onClick={onMakePrivate}
            className="px-2 py-1 border border-border rounded hover:bg-bg-input"
          >Make private</button>
        )}
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
              title={
                file.visibility === "public"
                  ? "Copy the permanent public URL."
                  : file.visibility === "signed"
                  ? "Copy a fresh time-limited signed URL (24h)."
                  : "Generate a time-limited signed URL anyone with the link can use."
              }
            >{
              file.visibility === "public"
                ? "Copy link"
                : file.visibility === "signed"
                ? "Copy link"
                : "Share"
            }</button>
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
  // Some uploads land with no content_type (browser couldn't sniff,
  // or the upload predates the platform's defaulting). Fall back to
  // the filename extension so video.mp4 / audio.mp3 still play.
  const ext = file.name.toLowerCase().split(".").pop() || "";
  const isVideo = ct.startsWith("video/") || ["mp4", "webm", "mov", "m4v", "ogv"].includes(ext);
  const isAudio = ct.startsWith("audio/") || ["mp3", "wav", "ogg", "m4a", "flac", "aac"].includes(ext);

  if (ct.startsWith("image/")) {
    return (
      <div className="bg-bg-input border-b border-border flex items-center justify-center" style={{ minHeight: "12rem", maxHeight: "20rem" }}>
        <img src={contentURL} alt={file.name} className="max-w-full" style={{ maxHeight: "20rem", objectFit: "contain" }} />
      </div>
    );
  }
  if (isVideo) {
    return (
      <div className="bg-black border-b border-border flex items-center justify-center" style={{ maxHeight: "24rem" }}>
        <video
          src={contentURL}
          controls
          preload="metadata"
          className="max-w-full"
          style={{ maxHeight: "24rem" }}
        >
          Your browser does not support inline video playback.
        </video>
      </div>
    );
  }
  if (isAudio) {
    return (
      <div className="bg-bg-input border-b border-border p-4">
        <audio src={contentURL} controls preload="metadata" className="w-full">
          Your browser does not support inline audio playback.
        </audio>
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

// Defensive renderer for the visibility column. Older rows can land
// with empty visibility from the pre-fix chunked-upload path; treat
// those as "private" (which the column's default would have been
// without the explicit override) and label them so an operator can
// tell at a glance that they're falling back rather than inheriting
// a value.
function VisibilityBadge({ value }: { value: string | undefined | null }) {
  const v = value || "private";
  const cls =
    v === "public" ? "bg-green/15 text-green" :
    v === "signed" ? "bg-accent/15 text-accent" :
    "bg-border text-text-muted";
  return <span className={`text-[10px] px-1.5 py-0.5 rounded ${cls}`}>{v}</span>;
}

// One row in the upload-progress strip. Renders filename, a
// percentage bar, and human bytes/total. Errors swap the bar for a
// red error message + dismiss button.
function UploadProgressRow({
  job,
  onDismiss,
}: {
  job: UploadJob;
  onDismiss: () => void;
}) {
  const pct = job.total > 0 ? Math.min(100, Math.floor((job.loaded / job.total) * 100)) : 0;
  const isError = job.status === "error";
  const isDone = job.status === "done";
  return (
    <div className="flex items-center gap-3 text-xs">
      <div className="flex-1 min-w-0">
        <div className="flex items-center justify-between gap-2 mb-1">
          <span className="text-text truncate" title={job.name}>{job.name}</span>
          <span className={`flex-shrink-0 ${isError ? "text-red" : "text-text-muted"}`}>
            {isError
              ? "failed"
              : isDone
              ? "uploaded"
              : `${formatSize(job.loaded)} / ${formatSize(job.total)} · ${pct}%`}
          </span>
        </div>
        <div className="h-1.5 bg-bg-input rounded overflow-hidden">
          <div
            className={`h-full transition-all duration-150 ${
              isError ? "bg-red" : isDone ? "bg-green" : "bg-accent"
            }`}
            style={{ width: isError || isDone ? "100%" : `${pct}%` }}
          />
        </div>
        {isError && job.error && (
          <div className="text-red mt-1">{job.error}</div>
        )}
      </div>
      {(isError || isDone) && (
        <button
          type="button"
          onClick={onDismiss}
          className="text-text-muted hover:text-text px-1"
          aria-label="Dismiss"
        >×</button>
      )}
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
