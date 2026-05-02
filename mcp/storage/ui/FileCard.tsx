// FileCard — storage's chat-attachment component. The agent calls
// respond(components=[{app:"storage", name:"file-card", props:{file_id:N}}])
// and the dashboard mounts this under the agent's message bubble.
//
// Self-fetches metadata via /api/apps/storage/files/<id> on mount,
// subscribes to the storage app's event bus for live updates (file
// deleted / replaced / re-tagged → re-fetch), renders one of three
// previews based on MIME:
//
//   image/*  →  <img>
//   video/*  →  <video preload="metadata"> (first keyframe only)
//   other    →  generic icon + extension
//
// Click → open the source bytes in a new tab. The cookie auth on the
// dashboard origin covers the GET on /content; for shared / external
// hot-link cases the storage app's signed URL flow is the answer
// (out of scope here).

import { useEffect, useState } from "react";

interface FileMeta {
  id: number;
  name: string;
  folder: string;
  content_type: string;
  size_bytes: number;
  sha256: string;
}

interface Props {
  file_id: number;
  compact?: boolean;
  /** Injected by the host so we can scope event subscription to the
   *  right project. */
  projectId?: string;
}

// Inlined event-subscription pattern. The dashboard already provides
// the canonical hook, but components can't import from the host
// dashboard tree (each app's bundle is independent). The helper
// hits the same /api/app-events SSE endpoint and is small enough
// (~30 lines) to copy here.
function useStorageEvents(
  projectId: string | undefined,
  onEvent: (ev: { topic: string; data: { id?: number } }) => void,
) {
  useEffect(() => {
    if (!projectId) return;
    const url = `/api/app-events/storage?project_id=${encodeURIComponent(projectId)}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data);
        onEvent(ev);
      } catch {
        /* ignore malformed frames */
      }
    };
    return () => es.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);
}

export default function FileCard({ file_id, compact, projectId }: Props) {
  const [meta, setMeta] = useState<FileMeta | null>(null);
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    fetch(`/api/apps/storage/files/${file_id}`, { credentials: "same-origin" })
      .then((r) => {
        if (r.status === 404) {
          setMissing(true);
          return null;
        }
        return r.json();
      })
      .then((data) => {
        if (!data) return;
        // Storage returns either {file: {...}} or the row at the
        // top level depending on version — accept both.
        const file =
          (data.file as FileMeta | undefined) ??
          (data.id ? (data as FileMeta) : null);
        if (file) {
          setMeta(file);
          setMissing(false);
        }
      })
      .catch(() => {
        // Leave the previous meta in place; we'll get another shot
        // on the next event.
      });
  };

  useEffect(() => {
    refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file_id]);

  // Live updates: refetch when our specific file is touched, and
  // mark missing on a delete event so the card flips to a tombstone.
  useStorageEvents(projectId, (ev) => {
    const id = ev?.data?.id;
    if (id !== file_id) return;
    if (ev.topic === "file.deleted") {
      setMissing(true);
    } else {
      refetch();
    }
  });

  if (missing) return <Tombstone fileId={file_id} compact={compact} />;
  if (!meta) return <Skeleton compact={compact} />;

  const url = `/api/apps/storage/files/${file_id}/content`;
  const isImage = meta.content_type.startsWith("image/");
  const isVideo = meta.content_type.startsWith("video/");

  return (
    <a
      href={url}
      target="_blank"
      rel="noopener"
      className={
        compact
          ? "flex items-center gap-2 p-2 border border-border rounded hover:border-accent transition-colors max-w-md"
          : "flex flex-col gap-0 border border-border rounded overflow-hidden hover:border-accent transition-colors max-w-md w-fit"
      }
      title={`${meta.name} · ${prettySize(meta.size_bytes)}`}
    >
      {!compact && (
        <div className="bg-bg-input flex items-center justify-center" style={{ height: 160 }}>
          <Preview url={url} mime={meta.content_type} />
        </div>
      )}
      {compact && <Preview url={url} mime={meta.content_type} compact />}
      <div className={compact ? "flex-1 min-w-0" : "p-2"}>
        <div className="text-text text-xs font-medium truncate">{meta.name}</div>
        <div className="text-text-dim text-[10px] flex items-center gap-1.5">
          <span>{prettySize(meta.size_bytes)}</span>
          <span>·</span>
          <span className="truncate">{meta.content_type || "binary"}</span>
          {(isImage || isVideo) && (
            <>
              <span className="ml-auto text-accent">Open ↗</span>
            </>
          )}
        </div>
      </div>
    </a>
  );
}

function Preview({ url, mime, compact }: { url: string; mime: string; compact?: boolean }) {
  const cls = compact
    ? "w-12 h-12 rounded object-cover flex-shrink-0 bg-bg-input"
    : "w-full h-full object-cover";
  if (mime.startsWith("image/")) {
    return <img src={url} alt="" className={cls} />;
  }
  if (mime.startsWith("video/")) {
    return <video src={url} preload="metadata" muted playsInline className={cls} />;
  }
  // Generic icon — kept tiny so we don't ship lucide etc.
  return (
    <div
      className={
        (compact ? "w-12 h-12 " : "w-full h-full ") +
        "flex items-center justify-center text-text-dim text-xs"
      }
    >
      📄
    </div>
  );
}

function Skeleton({ compact }: { compact?: boolean }) {
  return (
    <div
      className={
        (compact ? "h-12 w-64 " : "h-40 w-64 ") +
        "border border-border rounded animate-pulse bg-bg-input"
      }
    />
  );
}

function Tombstone({ fileId, compact }: { fileId: number; compact?: boolean }) {
  return (
    <div
      className={
        (compact ? "p-2 " : "p-3 ") +
        "border border-border/40 rounded text-text-dim text-xs italic"
      }
    >
      File #{fileId} no longer exists.
    </div>
  );
}

function prettySize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
