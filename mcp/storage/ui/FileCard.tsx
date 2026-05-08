// FileCard — storage's chat-attachment component. The agent calls
// respond(components=[{app:"storage", name:"file-card", props:{file_id:N}}])
// and the dashboard mounts this under the agent's message bubble.
//
// Composes @apteva/ui-kit primitives — Card, CardHeader, DataList — so
// it inherits the same look as every other chat card on the platform.
// The only storage-specific code is the thumbnail preview (image /
// video / icon by MIME) and the metadata fetch.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

// Brand identity for storage cards. First-party Apteva app, so we
// use a neutral slate-blue rather than a third-party brand color.
// Inline SVG (folder + file glyph) so this file has no extra deps —
// apps/ doesn't carry lucide-react the way integrations/ does.
const storageLogo = (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor" aria-hidden>
    <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z" />
  </svg>
);

const storageVendor: CardVendor = {
  name: "Storage",
  logo: storageLogo,
  // Slate dims hard against the dashboard's terminal-dark surface
  // (#0a0a0a). Light variant = slate-600 so it reads on warm beige
  // light mode; dark variant = slate-300 so it pops on near-black.
  color: { light: "#475569", dark: "#cbd5e1" },
};

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

    // Bridge to the dashboard's shared (app, project) multiplexer
    // when it's loaded. Without this, every Card mount opens its own
    // EventSource — N cards in a chat thread = N connections, which
    // blows past Chrome's per-origin HTTP/1.1 cap and freezes the
    // dashboard. Falls back to a direct EventSource when running
    // outside the dashboard (standalone preview, future surfaces).
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: { topic: string; app: string; project_id: string; data: any }) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe("storage", projectId, onEvent as any);
    }
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

  // In preview mode, hand the <img> a real photograph instead of an
  // SVG placeholder so the card looks like a card rendered for an
  // actual file. Picsum returns a stable image when seeded — same
  // photo on every refresh, no dependency on auth or our backend.
  const contentURL = preview
    ? "https://picsum.photos/seed/apteva-storage/480/300"
    : `/api/apps/storage/files/${file_id}/content`;

  // Whole card isn't wrapped in <a> — that nested an anchor inside
  // CardHeader's own "Open" link, which the HTML parser splits and
  // breaks the header's layout. The CardHeader action is enough.
  return (
    <Card>
      <CardHeader
        vendor={storageVendor}
        title={meta.name}
        subtitle={meta.folder}
        action={{ label: "Open", href: contentURL }}
      />
      <FilePreview url={contentURL} mime={meta.content_type} />
      <div className="px-4 py-3 border-t border-border">
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
  // Height constraint sits directly on the media element. The earlier
  // pattern (flex wrapper with style={{ height: 160 }} and an
  // h-full img inside) didn't constrain the image — its natural
  // intrinsic size was leaking through because the cascading
  // h-full ended up resolving against the wrapper's content size,
  // not its declared height. Putting the height on the <img> /
  // <video> directly is unambiguous: the element is 160px tall,
  // object-cover crops the source.
  if (mime.startsWith("image/")) {
    return (
      <img
        src={url}
        alt=""
        className="w-full block bg-bg-input object-cover"
        style={{ height: 160 }}
      />
    );
  }
  if (mime.startsWith("video/")) {
    return (
      <video
        src={url}
        preload="metadata"
        muted
        playsInline
        className="w-full block bg-bg-input object-cover"
        style={{ height: 160 }}
      />
    );
  }
  return (
    <div
      className="bg-bg-input flex items-center justify-center text-text-dim"
      style={{ height: 96 }}
    >
      <svg viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden>
        <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5z" />
        <path d="M14 3v5h5" />
      </svg>
    </div>
  );
}

function prettySize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
