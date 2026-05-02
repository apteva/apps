// FileCard — storage's chat-attachment component. The agent calls
// respond(components=[{app:"storage", name:"file-card", props:{file_id:N}}])
// and the dashboard mounts this under the agent's message bubble.
//
// Composes @apteva/ui-kit primitives — Card, CardHeader, DataList — so
// it inherits the same look as every other chat card on the platform.
// The only storage-specific code is the thumbnail preview (image /
// video / icon by MIME) and the metadata fetch.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList } from "@apteva/ui-kit";

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
  /** Injected by the host — scopes the metadata fetch and the event
   *  subscription to the right project. */
  projectId?: string;
  /** Soft convention: when true, render synthetic sample data
   *  instead of fetching. Used by the dashboard's app detail panel
   *  to preview a brand-new install before any real files exist. */
  preview?: boolean;
}

const previewSample: FileMeta = {
  id: 0,
  name: "vacation-shot.jpg",
  folder: "/inbox/",
  content_type: "image/jpeg",
  size_bytes: 1_842_000,
  sha256: "—",
};

// Inlined event-subscription pattern. The dashboard owns the
// canonical hook, but app bundles can't import from the host
// dashboard tree; the helper hits the same /api/app-events SSE
// endpoint and is small enough to keep here.
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
        onEvent(JSON.parse(e.data));
      } catch {
        /* ignore malformed frames */
      }
    };
    return () => es.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);
}

export default function FileCard({ file_id, projectId, preview }: Props) {
  const [meta, setMeta] = useState<FileMeta | null>(preview ? previewSample : null);
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    if (preview) return;
    if (!projectId) return;
    const url =
      `/api/apps/storage/files/${file_id}` +
      `?project_id=${encodeURIComponent(projectId)}`;
    fetch(url, { credentials: "same-origin" })
      .then((r) => {
        if (r.status === 404) {
          setMissing(true);
          return null;
        }
        return r.json();
      })
      .then((data) => {
        if (!data) return;
        const file =
          (data.file as FileMeta | undefined) ??
          (data.id ? (data as FileMeta) : null);
        if (file) {
          setMeta(file);
          setMissing(false);
        }
      })
      .catch(() => {
        // Leave previous meta in place; we'll get another shot on
        // the next event.
      });
  };

  useEffect(() => {
    refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file_id, projectId]);

  useStorageEvents(preview ? undefined : projectId, (ev) => {
    if (ev?.data?.id !== file_id) return;
    if (ev.topic === "file.deleted") setMissing(true);
    else refetch();
  });

  if (missing) {
    return (
      <Card>
        <CardHeader title={`File #${file_id}`} status={{ label: "deleted", variant: "muted" }} />
      </Card>
    );
  }

  if (!meta) {
    return (
      <Card>
        <CardHeader title={`File #${file_id}`} status={{ label: "loading", variant: "muted" }} />
      </Card>
    );
  }

  const contentURL = preview
    ? "data:image/svg+xml;utf8," +
      encodeURIComponent(
        `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 240 160'>` +
          `<rect width='240' height='160' fill='%23334155'/>` +
          `<text x='50%' y='50%' fill='%2394a3b8' font-family='sans-serif' font-size='20' text-anchor='middle' dy='.3em'>preview</text>` +
          `</svg>`
      )
    : `/api/apps/storage/files/${file_id}/content`;

  return (
    <Card href={contentURL}>
      <CardHeader
        title={meta.name}
        subtitle={meta.folder}
        action={{ label: "Open", href: contentURL }}
      />
      <FilePreview url={contentURL} mime={meta.content_type} />
      <div className="px-3 py-2 border-t border-border">
        <DataList
          items={[
            { label: "Type", value: meta.content_type || "binary" },
            { label: "Size", value: prettySize(meta.size_bytes) },
          ]}
        />
      </div>
    </Card>
  );
}

function FilePreview({ url, mime }: { url: string; mime: string }) {
  const wrap = "bg-bg-input flex items-center justify-center";
  if (mime.startsWith("image/")) {
    return (
      <div className={wrap} style={{ height: 160 }}>
        <img src={url} alt="" className="w-full h-full object-cover" />
      </div>
    );
  }
  if (mime.startsWith("video/")) {
    return (
      <div className={wrap} style={{ height: 160 }}>
        <video src={url} preload="metadata" muted playsInline className="w-full h-full object-cover" />
      </div>
    );
  }
  return (
    <div className={`${wrap} text-text-dim text-2xl`} style={{ height: 96 }}>
      📄
    </div>
  );
}

function prettySize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
