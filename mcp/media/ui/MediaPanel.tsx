// MediaPanel — native React panel for the media app. Shows a grid of
// indexed media files with thumbnails (video/image) or waveforms
// (audio), filter chips, sort, and a detail drawer.
//
// Loaded by the dashboard via dynamic import — the host's React copy
// is provided through an importmap, so we use bare `react` specifiers
// without bundling our own. All data flows through /api/apps/media/*
// (the platform proxy injects the install token); thumbnail/waveform
// images live in storage and are fetched at
// /api/apps/storage/files/<id>/content via same-origin cookies.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// Inlined SDK app-event subscription. Each app ships its own copy
// because panels are bundled standalone and apps install independently.
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


// Host props contract — copy of the dashboard's NativePanelProps.
// Inlined rather than imported so the panel has no cross-repo build
// dependency on the dashboard. If the contract changes, every panel
// updates; the dashboard's resolvePanelComponent caller stays the
// source of truth for what's actually passed in.
interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Derivation {
  id: number;
  file_id: string;
  kind: "thumbnail" | "waveform" | "cover";
  storage_file_id: string;
  width?: number;
  height?: number;
  status: "ok" | "failed" | "stale";
}

interface MediaRow {
  file_id: string;
  project_id: string;
  format_name?: string;
  duration_ms?: number;
  bitrate?: number;
  has_video: boolean;
  has_audio: boolean;
  is_image: boolean;
  width?: number;
  height?: number;
  fps?: number;
  video_codec?: string;
  channels?: number;
  sample_rate?: number;
  audio_codec?: string;
  probe_status: "pending" | "ok" | "failed" | "unsupported" | "skipped_size";
  probe_error?: string;
  raw_probe?: unknown;
  derivations?: Derivation[];
}

const API = "/api/apps/media";
const STORAGE = "/api/apps/storage";

type Kind = "all" | "video" | "audio" | "image";
type Sort = "created_at" | "duration_ms" | "updated_at";

function formatDuration(ms?: number): string {
  if (!ms) return "—";
  const s = Math.round(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`;
  return `${m}:${String(sec).padStart(2, "0")}`;
}

function formatBitrate(b?: number): string {
  if (!b) return "";
  if (b < 1_000_000) return `${(b / 1000).toFixed(0)} kbps`;
  return `${(b / 1_000_000).toFixed(1)} Mbps`;
}

export default function MediaPanel({ projectId, installId }: NativePanelProps) {
  const [rows, setRows] = useState<MediaRow[]>([]);
  const [status, setStatus] = useState<Record<string, number>>({});
  const [kind, setKind] = useState<Kind>("all");
  const [sort, setSort] = useState<Sort>("created_at");
  const [selected, setSelected] = useState<MediaRow | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const withParams = useCallback(
    (extra: Record<string, string> = {}) => {
      const u = new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      });
      return u.toString();
    },
    [projectId, installId],
  );

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const params: Record<string, string> = {
        order_by: sort,
        limit: "200",
      };
      if (kind === "video") params.has_video = "true";
      if (kind === "audio") {
        params.has_audio = "true";
        // exclude videos that happen to have audio
        params.is_image = "false";
      }
      if (kind === "image") params.is_image = "true";
      const res = await fetch(`${API}/media?${withParams(params)}`, {
        credentials: "same-origin",
      });
      if (!res.ok) throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      const data = (await res.json()) as { media: MediaRow[] };
      setRows(data.media || []);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [withParams, kind, sort]);

  // Status counts via the MCP-style summary endpoint — implemented as
  // a fan over rows here to avoid a second roundtrip; once we add a
  // dedicated /status route we'll switch to that.
  useEffect(() => {
    const counts: Record<string, number> = {};
    for (const r of rows) counts[r.probe_status] = (counts[r.probe_status] || 0) + 1;
    setStatus(counts);
  }, [rows]);

  useEffect(() => { load(); }, [load]);

  // Poll while anything is mid-probe so the panel updates live.
  useEffect(() => {
    if (!rows.some((r) => r.probe_status === "pending")) return;
    const id = setInterval(load, 4000);
    return () => clearInterval(id);
  }, [rows, load]);

  // Live refresh — the indexer worker emits media.indexed when a
  // newly-seen storage file finishes probing. Cheaper than the
  // 4s poll above (which stays as a safety net while pending rows
  // exist) and surfaces the result immediately.
  useAppEvents("media", projectId, (ev) => {
    if (ev.topic === "media.indexed") load();
  });

  const counts = useMemo(() => {
    const c = { all: rows.length, video: 0, audio: 0, image: 0 };
    for (const r of rows) {
      if (r.is_image) c.image++;
      else if (r.has_video) c.video++;
      else if (r.has_audio) c.audio++;
    }
    return c;
  }, [rows]);

  const handleReindex = async (fileId: string) => {
    await fetch(`${API}/media/${fileId}/reindex?${withParams()}`, {
      method: "POST",
      credentials: "same-origin",
    });
    setTimeout(load, 500);
  };

  const renderTile = (r: MediaRow) => {
    const thumb = r.derivations?.find((d) => d.kind === "thumbnail" && d.status === "ok");
    const wave = r.derivations?.find((d) => d.kind === "waveform" && d.status === "ok");
    const preview = thumb || wave;
    const previewURL = preview
      ? `${STORAGE}/files/${preview.storage_file_id}/content?${withParams()}`
      : null;
    return (
      <button
        key={r.file_id}
        type="button"
        onClick={() => setSelected(r)}
        className="text-left bg-bg-input/40 border border-border rounded overflow-hidden hover:border-accent/50 transition-colors flex flex-col"
      >
        <div className="aspect-video bg-bg-input flex items-center justify-center">
          {previewURL ? (
            <img src={previewURL} alt="" className="w-full h-full object-cover" />
          ) : (
            <span className="text-text-dim text-2xl">
              {r.is_image ? "🖼" : r.has_video ? "🎞" : r.has_audio ? "🔊" : "?"}
            </span>
          )}
        </div>
        <div className="p-2 flex flex-col gap-0.5">
          <div className="text-xs text-text font-medium truncate" title={r.file_id}>
            #{r.file_id}
          </div>
          <div className="text-[11px] text-text-muted flex flex-wrap gap-1">
            {r.duration_ms ? <span>{formatDuration(r.duration_ms)}</span> : null}
            {r.width && r.height ? <span>· {r.width}×{r.height}</span> : null}
            {r.video_codec ? <span>· {r.video_codec}</span> : null}
            {!r.video_codec && r.audio_codec ? <span>· {r.audio_codec}</span> : null}
          </div>
        </div>
      </button>
    );
  };

  return (
    <div className="h-full flex flex-col p-6 gap-4">
      <div className="flex items-center gap-2 flex-wrap">
        {(["all", "video", "audio", "image"] as Kind[]).map((k) => (
          <button
            key={k}
            type="button"
            onClick={() => setKind(k)}
            className={`px-2 py-1 text-xs rounded border transition-colors ${
              kind === k
                ? "bg-accent text-bg border-accent"
                : "border-border text-text-muted hover:text-text hover:border-accent/40"
            }`}
          >
            {k} {counts[k] ? <span className="opacity-60">({counts[k]})</span> : null}
          </button>
        ))}
        <span className="text-text-dim text-xs ml-2">·</span>
        <label className="text-xs text-text-dim">sort</label>
        <select
          value={sort}
          onChange={(e) => setSort(e.target.value as Sort)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
        >
          <option value="created_at">newest</option>
          <option value="duration_ms">longest</option>
          <option value="updated_at">recently updated</option>
        </select>
        <div className="flex-1" />
        <button
          type="button"
          onClick={load}
          className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
        >
          Refresh
        </button>
      </div>

      <div className="flex-1 overflow-auto">
        {error ? (
          <div className="text-red text-sm p-4">{error}</div>
        ) : loading && rows.length === 0 ? (
          <div className="text-text-muted text-sm text-center mt-12">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="text-text-muted text-sm text-center mt-12">
            No indexed media yet. Upload audio, video, or image files to storage —
            the indexer picks them up within ~30s.
          </div>
        ) : (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(200px,1fr))] gap-3">
            {rows.map(renderTile)}
          </div>
        )}
      </div>

      <footer className="text-xs text-text-dim flex items-center gap-3 border-t border-border pt-2">
        <span>{rows.length} indexed</span>
        {(["pending", "failed", "unsupported", "skipped_size"] as const).map((s) =>
          status[s] ? (
            <span
              key={s}
              className={
                s === "pending"
                  ? "text-accent"
                  : s === "failed"
                    ? "text-red"
                    : "text-text-muted"
              }
            >
              · {status[s]} {s.replace("_", " ")}
            </span>
          ) : null,
        )}
      </footer>

      {selected && (
        <DetailDrawer
          row={selected}
          onClose={() => setSelected(null)}
          onReindex={() => handleReindex(selected.file_id)}
          previewBase={`${STORAGE}/files`}
          query={withParams()}
        />
      )}
    </div>
  );
}

function DetailDrawer({
  row,
  onClose,
  onReindex,
  previewBase,
  query,
}: {
  row: MediaRow;
  onClose: () => void;
  onReindex: () => void;
  previewBase: string;
  query: string;
}) {
  const thumb = row.derivations?.find((d) => d.kind === "thumbnail");
  const wave = row.derivations?.find((d) => d.kind === "waveform");
  return (
    <div className="fixed inset-0 z-30 flex" onClick={onClose}>
      <div className="flex-1 bg-black/50" />
      <aside
        onClick={(e) => e.stopPropagation()}
        className="w-[480px] max-w-full bg-bg border-l border-border h-full overflow-auto"
      >
        <div className="p-4 border-b border-border flex items-center justify-between">
          <div>
            <div className="text-xs text-text-dim">file_id</div>
            <div className="text-text font-medium">#{row.file_id}</div>
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-text-dim hover:text-text text-sm px-2 py-1"
          >
            ✕
          </button>
        </div>
        <div className="p-4 space-y-4">
          {row.has_video ? (
            // Native player against the source. preload="metadata"
            // means the browser fetches just the moov atom + first
            // keyframe for the poster — opening the drawer is fast
            // even on a multi-GB clip; clicking play streams the rest.
            <video
              src={`${previewBase}/${row.file_id}/content?${query}`}
              poster={
                thumb
                  ? `${previewBase}/${thumb.storage_file_id}/content?${query}`
                  : undefined
              }
              controls
              preload="metadata"
              playsInline
              className="w-full rounded border border-border bg-black"
            />
          ) : row.has_audio ? (
            <>
              {wave && (
                <img
                  src={`${previewBase}/${wave.storage_file_id}/content?${query}`}
                  alt="waveform"
                  className="w-full rounded border border-border"
                />
              )}
              <audio
                src={`${previewBase}/${row.file_id}/content?${query}`}
                controls
                preload="metadata"
                className="w-full"
              />
            </>
          ) : row.is_image ? (
            // Click to open full-resolution in a new tab; the inline
            // preview keeps the drawer compact.
            <a
              href={`${previewBase}/${row.file_id}/content?${query}`}
              target="_blank"
              rel="noopener"
              title="Open full image"
            >
              <img
                src={`${previewBase}/${row.file_id}/content?${query}`}
                alt=""
                className="w-full rounded border border-border"
              />
            </a>
          ) : (thumb || wave) ? (
            <img
              src={`${previewBase}/${(thumb || wave)!.storage_file_id}/content?${query}`}
              alt=""
              className="w-full rounded border border-border"
            />
          ) : null}
          <a
            href={`${previewBase}/${row.file_id}/content?${query}`}
            target="_blank"
            rel="noopener"
            className="block text-xs text-accent hover:underline truncate"
          >
            Open source file ↗
          </a>
          <Section title="Container">
            <Field label="format" value={row.format_name} />
            <Field label="duration" value={formatDuration(row.duration_ms)} />
            <Field label="bitrate" value={formatBitrate(row.bitrate)} />
          </Section>
          {row.has_video && (
            <Section title="Video">
              <Field label="codec" value={row.video_codec} />
              <Field
                label="size"
                value={row.width && row.height ? `${row.width}×${row.height}` : undefined}
              />
              <Field label="fps" value={row.fps ? row.fps.toFixed(2) : undefined} />
            </Section>
          )}
          {row.has_audio && (
            <Section title="Audio">
              <Field label="codec" value={row.audio_codec} />
              <Field label="channels" value={row.channels?.toString()} />
              <Field
                label="sample rate"
                value={row.sample_rate ? `${(row.sample_rate / 1000).toFixed(1)} kHz` : undefined}
              />
            </Section>
          )}
          <Section title="Status">
            <Field label="probe" value={row.probe_status} />
            {row.probe_error ? <Field label="error" value={row.probe_error} /> : null}
          </Section>
          <details>
            <summary className="text-xs text-text-dim cursor-pointer hover:text-text">
              raw ffprobe
            </summary>
            <pre className="text-[11px] bg-bg-input border border-border rounded p-2 mt-2 overflow-auto max-h-96">
              {JSON.stringify(row.raw_probe, null, 2)}
            </pre>
          </details>
          <button
            type="button"
            onClick={onReindex}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            Re-index
          </button>
        </div>
      </aside>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h3 className="text-xs uppercase tracking-wide text-text-dim mb-1">{title}</h3>
      <div className="grid grid-cols-[100px_1fr] gap-y-1 text-sm">{children}</div>
    </section>
  );
}

function Field({ label, value }: { label: string; value?: string | number }) {
  if (value === undefined || value === null || value === "") return null;
  return (
    <>
      <span className="text-text-muted">{label}</span>
      <span className="text-text">{String(value)}</span>
    </>
  );
}
