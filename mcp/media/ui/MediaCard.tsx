// MediaCard — media's chat-attachment component. Mirrors storage's
// FileCard pattern but with media-specific affordances: thumbnail
// preview for video/image, waveform preview for audio, duration
// badge, click-to-open link.
//
// Agent calls:
//   respond(components=[{app:"media", name:"media-card", props:{file_id:N}}])
//
// Subscribes to media events (media.indexed, media.transcribed,
// media.described, media.deleted) so the card live-updates when the
// indexer probes / the transcriber writes / the describer fills in
// the LLM-generated description.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

const mediaLogo = (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor" aria-hidden>
    <path d="M3 5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5zm6 4v6l5-3-5-3z" />
  </svg>
);

const mediaVendor: CardVendor = {
  name: "Media",
  logo: mediaLogo,
  // Same neutral pop as storage's slate but tilted to teal — first-
  // party Apteva app, not a third-party brand.
  color: { light: "#0f766e", dark: "#5eead4" },
};

interface DerivationRow {
  kind: string; // "thumbnail" | "waveform"
  storage_file_id: string;
  status: string; // "ok" | "pending" | "failed"
}

interface MediaMeta {
  file_id: string;
  name: string;
  folder: string;
  has_video: boolean;
  has_audio: boolean;
  is_image: boolean;
  duration_ms?: number;
  width?: number;
  height?: number;
  description?: string;
  title?: string;
  // The API returns derivations as a nested array. The card walks
  // it to find the thumbnail/waveform storage_file_id for preview.
  derivations?: DerivationRow[];
}

interface Props {
  file_id: string | number;
  projectId?: string;
  preview?: boolean;
}

const previewSample: MediaMeta = {
  file_id: "0",
  name: "tears-of-steel.webm",
  folder: "/clips/2026-05/",
  has_video: true,
  has_audio: true,
  is_image: false,
  duration_ms: 734_167,
  width: 1920,
  height: 1080,
  title: "Tears of Steel",
  description:
    "Sci-fi short. Two characters debate her robotic prosthetic on an Amsterdam canal bridge.",
};

// dbg — always-on for now while we hunt the media-card hang. Filter
// the DevTools console by "[media-card]" to isolate this component's
// output from the rest of the dashboard chatter.
function dbg(...args: unknown[]) {
  // eslint-disable-next-line no-console
  console.log("[media-card]", ...args);
}

// Inlined SSE subscription, mirroring storage's FileCard. Topic
// filter narrows to media events for this project; payload data.id
// (file_id) gates re-fetches.
function useMediaEvents(
  projectId: string | undefined,
  onEvent: (ev: { topic: string; data: { file_id?: string | number } }) => void,
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
      dbg("subscribe via bridge", { project: projectId });
      const unsub = bridge.subscribe("media", projectId, ((ev: any) => {
        dbg("event via bridge", { topic: ev?.topic, file_id: ev?.data?.file_id });
        onEvent(ev);
      }) as any);
      return () => {
        dbg("unsubscribe (bridge)", { project: projectId });
        unsub();
      };
    }
    dbg("subscribe via direct EventSource (bridge missing)", { project: projectId });
    const url = `/api/app-events/media?project_id=${encodeURIComponent(projectId)}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onopen = () => dbg("EventSource open", { url });
    es.onerror = (e) => dbg("EventSource error", { readyState: es.readyState, e });
    es.onmessage = (e) => {
      try {
        const ev = JSON.parse(e.data);
        dbg("event via direct ES", { topic: ev?.topic, file_id: ev?.data?.file_id });
        onEvent(ev);
      } catch {
        /* ignore */
      }
    };
    return () => {
      dbg("unsubscribe (direct ES)", { project: projectId });
      es.close();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);
}

export default function MediaCard({ file_id, projectId, preview }: Props) {
  const fid = String(file_id);
  const [meta, setMeta] = useState<MediaMeta | null>(preview ? previewSample : null);
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    if (preview || !projectId) return;
    const url =
      `/api/apps/media/media/${encodeURIComponent(fid)}` +
      `?project_id=${encodeURIComponent(projectId)}`;
    const t0 = (typeof performance !== "undefined" ? performance.now() : Date.now());
    dbg("refetch start", { fid, url });
    fetch(url, { credentials: "same-origin" })
      .then((r) => {
        const dt = ((typeof performance !== "undefined" ? performance.now() : Date.now()) - t0).toFixed(1);
        dbg("refetch response", { fid, status: r.status, ms: dt });
        if (r.status === 404) {
          setMissing(true);
          return null;
        }
        return r.json();
      })
      .then((data) => {
        if (!data) return;
        // Server may return either {media: ...} or the row directly.
        const row: MediaMeta | undefined =
          (data.media as MediaMeta | undefined) ??
          (data.file_id ? (data as MediaMeta) : undefined);
        if (row) {
          dbg("refetch parsed", { fid, has_video: row.has_video, has_audio: row.has_audio, derivations: row.derivations?.length ?? 0 });
          setMeta(row);
          setMissing(false);
        } else {
          dbg("refetch unrecognized payload", { fid, data });
        }
      })
      .catch((err) => {
        dbg("refetch failed", { fid, err: String(err) });
      });
  };

  useEffect(() => {
    dbg("mount", { fid, projectId, preview });
    refetch();
    return () => dbg("unmount", { fid, projectId });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fid, projectId]);

  useMediaEvents(preview ? undefined : projectId, (ev) => {
    if (String(ev?.data?.file_id ?? "") !== fid) return;
    if (ev.topic === "media.deleted") {
      dbg("event matched: media.deleted", { fid });
      setMissing(true);
    } else {
      dbg("event matched → refetch", { fid, topic: ev.topic });
      refetch();
    }
  });

  if (missing) {
    return (
      <Card>
        <CardHeader title={`Media #${fid}`} status={{ label: "deleted", variant: "muted" }} />
      </Card>
    );
  }
  if (!meta) {
    return (
      <Card>
        <CardHeader title={`Media #${fid}`} status={{ label: "loading", variant: "muted" }} />
      </Card>
    );
  }

  const contentURL = preview
    ? "https://picsum.photos/seed/apteva-media/480/270"
    : `/api/apps/storage/files/${encodeURIComponent(fid)}/content?project_id=${encodeURIComponent(projectId ?? "")}`;
  const thumbURL = thumbnailURLFor(meta, projectId);

  // Title prefers the operator/agent-set title; falls back to filename.
  const headerTitle = meta.title?.trim() || meta.name;
  const subtitle = meta.folder;

  return (
    <Card>
      <CardHeader
        vendor={mediaVendor}
        title={headerTitle}
        subtitle={subtitle}
        action={{ label: "Open", href: contentURL }}
      />
      <MediaPreview meta={meta} thumbURL={thumbURL} contentURL={contentURL} />
      <div className="px-4 py-3 border-t border-border">
        <DataList items={metaItems(meta)} />
        {meta.description && (
          <p className="text-text-dim text-xs mt-3 leading-relaxed">{meta.description}</p>
        )}
      </div>
    </Card>
  );
}

function MediaPreview({
  meta,
  thumbURL,
  contentURL,
}: {
  meta: MediaMeta;
  thumbURL?: string;
  contentURL: string;
}) {
  // Image → render the file directly (no thumbnail step).
  if (meta.is_image) {
    return (
      <img
        src={contentURL}
        alt={meta.title || meta.name}
        className="w-full block bg-bg-input object-cover"
        style={{ height: 200 }}
      />
    );
  }
  // Video → use the cached thumbnail when we have it; fallback to a
  // <video> element for a frame. Don't autoplay — reasonable
  // default for chat attachments.
  if (meta.has_video) {
    if (thumbURL) {
      return (
        <div className="relative">
          <img
            src={thumbURL}
            alt=""
            className="w-full block bg-bg-input object-cover"
            style={{ height: 200 }}
          />
          <PlayBadge />
          {meta.duration_ms != null && <DurationPill ms={meta.duration_ms} />}
        </div>
      );
    }
    return (
      <div className="relative">
        <video
          src={contentURL}
          preload="metadata"
          muted
          playsInline
          className="w-full block bg-bg-input object-cover"
          style={{ height: 200 }}
        />
        {meta.duration_ms != null && <DurationPill ms={meta.duration_ms} />}
      </div>
    );
  }
  // Audio-only → waveform if cached, else a generic audio glyph.
  if (meta.has_audio) {
    if (thumbURL) {
      return (
        <div className="relative">
          <img
            src={thumbURL}
            alt=""
            className="w-full block bg-bg-input object-contain"
            style={{ height: 100 }}
          />
          {meta.duration_ms != null && <DurationPill ms={meta.duration_ms} />}
        </div>
      );
    }
    return (
      <div
        className="bg-bg-input flex items-center justify-center text-text-dim relative"
        style={{ height: 96 }}
      >
        <svg viewBox="0 0 24 24" width="28" height="28" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden>
          <path d="M9 18V6l12-2v12" />
          <circle cx="6" cy="18" r="3" />
          <circle cx="18" cy="16" r="3" />
        </svg>
        {meta.duration_ms != null && <DurationPill ms={meta.duration_ms} />}
      </div>
    );
  }
  return null;
}

function PlayBadge() {
  return (
    <div className="absolute inset-0 flex items-center justify-center pointer-events-none">
      <div className="w-12 h-12 rounded-full bg-black/60 flex items-center justify-center">
        <svg viewBox="0 0 24 24" width="20" height="20" fill="white" aria-hidden>
          <path d="M8 5v14l11-7z" />
        </svg>
      </div>
    </div>
  );
}

function DurationPill({ ms }: { ms: number }) {
  return (
    <div className="absolute bottom-2 right-2 px-1.5 py-0.5 rounded bg-black/70 text-white text-[10px] font-mono">
      {prettyDuration(ms)}
    </div>
  );
}

function metaItems(m: MediaMeta) {
  const items: { label: string; value: string }[] = [];
  if (m.duration_ms) items.push({ label: "Duration", value: prettyDuration(m.duration_ms) });
  if (m.width && m.height) items.push({ label: "Size", value: `${m.width}×${m.height}` });
  const kind = m.is_image ? "image" : m.has_video ? "video" : m.has_audio ? "audio" : "—";
  items.push({ label: "Kind", value: kind });
  return items;
}

// thumbnailURLFor — finds the cached thumbnail (or waveform for
// audio-only) derivation in media.derivations[] and builds a URL
// to its bytes via storage's content endpoint. Returns undefined
// when no ok-status derivation exists yet (file is mid-probe or the
// derive step failed); the preview falls back to a <video>/<audio>
// element in that case.
//
// Earlier versions read flat fields (m.thumbnail_file_id) that the
// API never returned, so this always came back undefined and the
// card showed an empty <video preload="metadata"> placeholder.
function thumbnailURLFor(m: MediaMeta, projectId?: string): string | undefined {
  if (!projectId) return undefined;
  const derivs = m.derivations ?? [];
  // Prefer "thumbnail" (video + image cache); fall back to "waveform"
  // for audio-only files so the card shows the wave instead of a
  // generic music glyph.
  const pick =
    derivs.find((d) => d.kind === "thumbnail" && d.status === "ok") ??
    derivs.find((d) => d.kind === "waveform" && d.status === "ok");
  if (!pick) return undefined;
  return `/api/apps/storage/files/${encodeURIComponent(pick.storage_file_id)}/content?project_id=${encodeURIComponent(projectId)}`;
}

function prettyDuration(ms: number): string {
  const total = Math.round(ms / 1000);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}:${m.toString().padStart(2, "0")}:${s.toString().padStart(2, "0")}`;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
