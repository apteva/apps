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
  // v0.3 — user/agent-supplied prose. Survives reprobe.
  title?: string;
  description?: string;
  alt_text?: string;
  // v0.4 — transcript status, surfaced via LEFT JOIN so the tile can
  // show an icon without a second roundtrip. "" when no transcript.
  transcript_status?: "" | "pending" | "running" | "ok" | "failed" | "skipped";
  derivations?: Derivation[];
}

interface Transcript {
  file_id: string;
  status: "pending" | "running" | "ok" | "failed" | "skipped";
  language?: string;
  text?: string;
  segments?: Array<{ start_ms: number; end_ms: number; text: string; speaker?: string }>;
  provider?: string;
  model?: string;
  duration_ms?: number;
  error?: string;
  source_kind?: "auto" | "manual" | "imported";
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

  // queueTranscribe POSTs /media/{id}/transcribe; force=true wipes the
  // existing row first (matches the MCP tool's force semantics). The
  // transcriber goroutine in the sidecar drains the queue async.
  const queueTranscribe = useCallback(
    async (fileId: string, force = false) => {
      const params = withParams(force ? { force: "true" } : {});
      const res = await fetch(`${API}/media/${fileId}/transcribe?${params}`, {
        method: "POST",
        credentials: "same-origin",
      });
      if (!res.ok) {
        throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      }
      // Patch in-place so the drawer/tile reflect the new pending status
      // immediately without waiting for the next refresh.
      setRows((prev) =>
        prev.map((r) =>
          r.file_id === fileId ? { ...r, transcript_status: "pending" as const } : r,
        ),
      );
      setSelected((prev) =>
        prev && prev.file_id === fileId
          ? { ...prev, transcript_status: "pending" as const }
          : prev,
      );
    },
    [withParams],
  );

  // Live refresh on transcript completion — the worker emits
  // media.transcribed with {file_id} so we can patch + reload.
  useAppEvents<{ file_id?: string }>("media", projectId, (ev) => {
    if (ev.topic === "media.transcribed") load();
  });

  // saveDescription writes prose via PUT /media/{id}/description (which
  // wraps the same setDescription used by the MCP tool). On success we
  // patch the row in-place so the drawer reflects the save without
  // waiting for a full refresh.
  const saveDescription = useCallback(
    async (fileId: string, fields: { title?: string; description?: string; alt_text?: string }) => {
      const res = await fetch(`${API}/media/${fileId}/description?${withParams()}`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(fields),
      });
      if (!res.ok) {
        throw new Error(`${res.status}: ${await res.text().catch(() => "")}`);
      }
      setRows((prev) =>
        prev.map((r) => (r.file_id === fileId ? { ...r, ...fields } : r)),
      );
      setSelected((prev) => (prev && prev.file_id === fileId ? { ...prev, ...fields } : prev));
    },
    [withParams],
  );

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
        <div className="aspect-video bg-bg-input flex items-center justify-center relative">
          {previewURL ? (
            <img src={previewURL} alt="" className="w-full h-full object-cover" />
          ) : (
            <span className="text-text-dim">
              <KindIcon row={r} className="w-8 h-8" />
            </span>
          )}
          {r.transcript_status ? (
            <span
              className={`absolute top-1 right-1 ${transcriptBadgeClass(r.transcript_status)}`}
              title={`Transcript: ${r.transcript_status}`}
            >
              <TranscriptStatusIcon status={r.transcript_status} className="w-3.5 h-3.5" />
            </span>
          ) : null}
        </div>
        <div className="p-2 flex flex-col gap-0.5">
          <div className="text-xs text-text font-medium truncate" title={r.file_id}>
            {r.title || `#${r.file_id}`}
          </div>
          {r.description ? (
            <div
              className="text-[11px] text-text-muted line-clamp-2"
              title={r.description}
            >
              {r.description}
            </div>
          ) : null}
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
          onSaveDescription={(fields) => saveDescription(selected.file_id, fields)}
          onTranscribe={(force) => queueTranscribe(selected.file_id, force)}
          previewBase={`${STORAGE}/files`}
          apiBase={API}
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
  onSaveDescription,
  onTranscribe,
  previewBase,
  apiBase,
  query,
}: {
  row: MediaRow;
  onClose: () => void;
  onReindex: () => void;
  onSaveDescription: (fields: { title?: string; description?: string; alt_text?: string }) => Promise<void>;
  onTranscribe: (force: boolean) => Promise<void>;
  previewBase: string;
  apiBase: string;
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
          <DescriptionEditor row={row} onSave={onSaveDescription} />
          {row.has_audio ? (
            <TranscriptSection
              row={row}
              apiBase={apiBase}
              query={query}
              onTranscribe={onTranscribe}
            />
          ) : null}
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

// DescriptionEditor — collapsible inline edit of title / description /
// alt_text for one media row. View mode renders the prose read-only;
// edit mode shows three inputs + Save/Cancel. Saves go through the
// parent's onSave callback (which calls PUT /media/{id}/description),
// so the same path the agent uses also writes from the panel.
function DescriptionEditor({
  row,
  onSave,
}: {
  row: MediaRow;
  onSave: (fields: { title?: string; description?: string; alt_text?: string }) => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState(row.title || "");
  const [desc, setDesc] = useState(row.description || "");
  const [alt, setAlt] = useState(row.alt_text || "");
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");

  // Reset local state when the selected row changes (the drawer is
  // reused for different files via key={row.file_id} on selection).
  useEffect(() => {
    setTitle(row.title || "");
    setDesc(row.description || "");
    setAlt(row.alt_text || "");
    setEditing(false);
    setErr("");
  }, [row.file_id]);

  const dirty =
    (row.title || "") !== title ||
    (row.description || "") !== desc ||
    (row.alt_text || "") !== alt;

  const handleSave = async () => {
    setSaving(true);
    setErr("");
    try {
      // Send only fields that actually changed — server treats missing
      // keys as "preserve", empty string as "clear", which is exactly
      // what we want.
      const fields: { title?: string; description?: string; alt_text?: string } = {};
      if ((row.title || "") !== title) fields.title = title;
      if ((row.description || "") !== desc) fields.description = desc;
      if ((row.alt_text || "") !== alt) fields.alt_text = alt;
      await onSave(fields);
      setEditing(false);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const handleCancel = () => {
    setTitle(row.title || "");
    setDesc(row.description || "");
    setAlt(row.alt_text || "");
    setEditing(false);
    setErr("");
  };

  const empty = !row.title && !row.description && !row.alt_text;

  if (!editing) {
    return (
      <section>
        <div className="flex items-center justify-between mb-1">
          <h3 className="text-xs uppercase tracking-wide text-text-dim">Description</h3>
          <button
            type="button"
            onClick={() => setEditing(true)}
            className="text-xs text-accent hover:underline"
          >
            {empty ? "Add description" : "Edit"}
          </button>
        </div>
        {empty ? (
          <div className="text-xs text-text-dim italic">
            No description set. Add one to give agents and viewers context.
          </div>
        ) : (
          <div className="space-y-1 text-sm">
            {row.title ? <div className="text-text font-medium">{row.title}</div> : null}
            {row.description ? (
              <div className="text-text-muted whitespace-pre-wrap">{row.description}</div>
            ) : null}
            {row.alt_text ? (
              <div className="text-xs text-text-dim">
                <span className="text-text-muted">alt: </span>
                {row.alt_text}
              </div>
            ) : null}
          </div>
        )}
      </section>
    );
  }

  return (
    <section>
      <div className="flex items-center justify-between mb-1">
        <h3 className="text-xs uppercase tracking-wide text-text-dim">Description</h3>
      </div>
      <div className="space-y-2 text-sm">
        <label className="block">
          <span className="text-xs text-text-muted block mb-0.5">Title</span>
          <input
            type="text"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            placeholder="Short label, e.g. Q3 board sync"
            disabled={saving}
          />
        </label>
        <label className="block">
          <span className="text-xs text-text-muted block mb-0.5">Description</span>
          <textarea
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            rows={4}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
            placeholder="What's in this clip — couple of sentences."
            disabled={saving}
          />
        </label>
        <label className="block">
          <span className="text-xs text-text-muted block mb-0.5">Alt text</span>
          <input
            type="text"
            value={alt}
            onChange={(e) => setAlt(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            placeholder="Accessibility / screen-reader text (esp. for images)"
            disabled={saving}
          />
        </label>
        {err ? <div className="text-red text-xs">{err}</div> : null}
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleSave}
            disabled={saving || !dirty}
            className="px-3 py-1 text-sm bg-accent text-bg rounded hover:opacity-90 disabled:opacity-40"
          >
            {saving ? "Saving…" : "Save"}
          </button>
          <button
            type="button"
            onClick={handleCancel}
            disabled={saving}
            className="px-3 py-1 text-sm border border-border text-text-muted rounded hover:bg-bg-input"
          >
            Cancel
          </button>
        </div>
      </div>
    </section>
  );
}

// ─── Transcripts ───────────────────────────────────────────────────

// TranscriptSection — collapsible transcript view in the drawer.
// Lazy-fetches /media/{id}/transcript on mount because transcripts
// can be large (KB to MB) and most files won't have one open.
//
// Status display:
//   - none yet → "No transcript yet" + "Transcribe" button
//   - pending|running → spinner + "queued / running on Deepgram"
//   - ok → language + text in <details> (first ~250 chars
//     visible by default, expand to see full)
//   - failed → error + "Retry" button (force=true)
//   - skipped → reason + "Re-queue" button (force=true)
function TranscriptSection({
  row,
  apiBase,
  query,
  onTranscribe,
}: {
  row: MediaRow;
  apiBase: string;
  query: string;
  onTranscribe: (force: boolean) => Promise<void>;
}) {
  const [transcript, setTranscript] = useState<Transcript | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const fetchTranscript = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const res = await fetch(`${apiBase}/media/${row.file_id}/transcript?${query}`, {
        credentials: "same-origin",
      });
      if (!res.ok) {
        // 404 / not-found is not an error here — it just means no row.
        setTranscript(null);
        return;
      }
      const data = await res.json();
      if (data.found) {
        setTranscript(data.transcript as Transcript);
      } else {
        setTranscript(null);
      }
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [apiBase, row.file_id, query]);

  useEffect(() => {
    fetchTranscript();
  }, [fetchTranscript]);

  // Re-fetch when status flips to terminal (the parent's
  // media.transcribed event reload doesn't include the text).
  useEffect(() => {
    if (transcript?.status === "pending" || transcript?.status === "running") {
      const id = setInterval(fetchTranscript, 4000);
      return () => clearInterval(id);
    }
  }, [transcript?.status, fetchTranscript]);

  const handleQueue = async (force: boolean) => {
    setSubmitting(true);
    setErr("");
    try {
      await onTranscribe(force);
      // Optimistic — flip our local view to pending so the UI doesn't
      // sit on stale state while the next poll arrives.
      setTranscript({
        file_id: row.file_id,
        status: "pending",
        source_kind: "manual",
      });
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section>
      <div className="flex items-center justify-between mb-1">
        <h3 className="text-xs uppercase tracking-wide text-text-dim">Transcript</h3>
        {transcript ? (
          <span className={`inline-flex items-center gap-1 text-xs ${transcriptStatusTextClass(transcript.status)}`}>
            <TranscriptStatusIcon status={transcript.status} className="w-3.5 h-3.5" />
            {transcript.status}
            {transcript.language ? <span className="text-text-dim">· {transcript.language}</span> : null}
          </span>
        ) : null}
      </div>

      {err ? <div className="text-red text-xs mb-1">{err}</div> : null}

      {loading ? (
        <div className="text-xs text-text-dim">Loading…</div>
      ) : !transcript ? (
        <div className="space-y-2">
          <div className="text-xs text-text-dim italic">
            No transcript yet. Queue one to have Deepgram transcribe this file.
          </div>
          <button
            type="button"
            onClick={() => handleQueue(false)}
            disabled={submitting}
            className="px-3 py-1 text-sm bg-accent text-bg rounded hover:opacity-90 disabled:opacity-40"
          >
            {submitting ? "Queueing…" : "Transcribe"}
          </button>
        </div>
      ) : transcript.status === "pending" || transcript.status === "running" ? (
        <div className="text-xs text-text-muted">
          {transcript.status === "pending"
            ? "Queued — the transcriber picks this up on its next sweep."
            : "Running on Deepgram. Refreshing automatically every few seconds."}
        </div>
      ) : transcript.status === "ok" ? (
        <details className="text-sm">
          <summary className="text-text cursor-pointer hover:text-accent">
            {snippet(transcript.text, 200)}
          </summary>
          <div className="mt-2 whitespace-pre-wrap text-text-muted text-sm font-mono leading-relaxed">
            {transcript.text || "(empty)"}
          </div>
          <div className="mt-3 flex items-center gap-2">
            <button
              type="button"
              onClick={() => handleQueue(true)}
              disabled={submitting}
              className="px-3 py-1 text-xs border border-border text-text-muted rounded hover:bg-bg-input"
            >
              {submitting ? "Re-queueing…" : "Re-transcribe"}
            </button>
            {transcript.provider ? (
              <span className="text-xs text-text-dim">
                {transcript.provider}
                {transcript.model ? `:${transcript.model}` : ""}
              </span>
            ) : null}
          </div>
        </details>
      ) : transcript.status === "failed" ? (
        <div className="space-y-2">
          <div className="text-xs text-red whitespace-pre-wrap">
            {transcript.error || "Transcription failed."}
          </div>
          <button
            type="button"
            onClick={() => handleQueue(true)}
            disabled={submitting}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-40"
          >
            {submitting ? "Retrying…" : "Retry"}
          </button>
        </div>
      ) : transcript.status === "skipped" ? (
        <div className="space-y-2">
          <div className="text-xs text-text-muted">
            Skipped — {transcript.error || "no reason recorded"}.
          </div>
          <button
            type="button"
            onClick={() => handleQueue(true)}
            disabled={submitting}
            className="px-3 py-1 text-sm border border-border text-text rounded hover:bg-bg-input disabled:opacity-40"
          >
            {submitting ? "Queueing…" : "Queue anyway"}
          </button>
        </div>
      ) : null}
    </section>
  );
}

function snippet(text: string | undefined, n: number): string {
  if (!text) return "(empty transcript)";
  const trimmed = text.trim();
  if (trimmed.length <= n) return trimmed;
  return trimmed.slice(0, n).replace(/\s+\S*$/, "") + "…";
}

function transcriptStatusTextClass(status: string): string {
  switch (status) {
    case "ok":
      return "text-text";
    case "pending":
    case "running":
      return "text-accent";
    case "failed":
      return "text-red";
    case "skipped":
      return "text-text-muted";
    default:
      return "text-text-dim";
  }
}

function transcriptBadgeClass(status: string): string {
  // Pill badge in the corner of the tile thumbnail. The colour class
  // also drives the icon stroke via currentColor.
  const base = "inline-flex items-center justify-center rounded-full border bg-bg/80 backdrop-blur p-0.5";
  switch (status) {
    case "ok":
      return `${base} border-accent/40 text-accent`;
    case "pending":
    case "running":
      return `${base} border-border text-accent animate-pulse`;
    case "failed":
      return `${base} border-red/40 text-red`;
    case "skipped":
      return `${base} border-border text-text-muted`;
    default:
      return `${base} border-border text-text-dim`;
  }
}

// ─── Icons ─────────────────────────────────────────────────────────
//
// Inline SVG icons — no external icon library dependency. Lucide-
// style strokes (24-grid, 2px stroke, round caps/joins). All driven
// by currentColor so the surrounding text colour controls them.

function KindIcon({ row, className }: { row: MediaRow; className?: string }) {
  if (row.is_image) return <ImageIcon className={className} />;
  if (row.has_video) return <VideoIcon className={className} />;
  if (row.has_audio) return <AudioIcon className={className} />;
  return <FileIcon className={className} />;
}

function TranscriptStatusIcon({ status, className }: { status: string; className?: string }) {
  switch (status) {
    case "ok":
      return <MicCheckIcon className={className} />;
    case "running":
    case "pending":
      return <MicProgressIcon className={className} />;
    case "failed":
      return <MicXIcon className={className} />;
    case "skipped":
      return <MicSlashIcon className={className} />;
    default:
      return <MicIcon className={className} />;
  }
}

const svgProps = {
  xmlns: "http://www.w3.org/2000/svg",
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

function ImageIcon({ className }: { className?: string }) {
  return (
    <svg {...svgProps} className={className}>
      <rect x="3" y="3" width="18" height="18" rx="2" />
      <circle cx="9" cy="9" r="2" />
      <path d="m21 15-3.086-3.086a2 2 0 0 0-2.828 0L6 21" />
    </svg>
  );
}

function VideoIcon({ className }: { className?: string }) {
  return (
    <svg {...svgProps} className={className}>
      <path d="m22 8-6 4 6 4V8Z" />
      <rect x="2" y="6" width="14" height="12" rx="2" />
    </svg>
  );
}

function AudioIcon({ className }: { className?: string }) {
  return (
    <svg {...svgProps} className={className}>
      <path d="M9 18V5l12-2v13" />
      <circle cx="6" cy="18" r="3" />
      <circle cx="18" cy="16" r="3" />
    </svg>
  );
}

function FileIcon({ className }: { className?: string }) {
  return (
    <svg {...svgProps} className={className}>
      <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
      <polyline points="14 2 14 8 20 8" />
    </svg>
  );
}

function MicIcon({ className }: { className?: string }) {
  return (
    <svg {...svgProps} className={className}>
      <rect x="9" y="2" width="6" height="12" rx="3" />
      <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
      <line x1="12" y1="19" x2="12" y2="23" />
      <line x1="8" y1="23" x2="16" y2="23" />
    </svg>
  );
}

function MicCheckIcon({ className }: { className?: string }) {
  // Mic with a tick overlay — denotes a completed transcript.
  return (
    <svg {...svgProps} className={className}>
      <rect x="6" y="2" width="6" height="12" rx="3" />
      <path d="M16 10v2a7 7 0 0 1-13.93 1" />
      <polyline points="14 19 17 22 22 16" />
    </svg>
  );
}

function MicProgressIcon({ className }: { className?: string }) {
  // Mic with three pulse arcs — the parent's animate-pulse on the
  // badge does the actual movement; this icon just looks like
  // sound waves emanating from the mic.
  return (
    <svg {...svgProps} className={className}>
      <rect x="9" y="2" width="6" height="12" rx="3" />
      <path d="M5 10v2a7 7 0 0 0 14 0v-2" />
      <path d="M19 19h2" opacity="0.6" />
    </svg>
  );
}

function MicXIcon({ className }: { className?: string }) {
  // Mic with an X — failed transcript.
  return (
    <svg {...svgProps} className={className}>
      <rect x="6" y="2" width="6" height="12" rx="3" />
      <path d="M16 10v2a7 7 0 0 1-13.93 1" />
      <line x1="15" y1="17" x2="21" y2="23" />
      <line x1="21" y1="17" x2="15" y2="23" />
    </svg>
  );
}

function MicSlashIcon({ className }: { className?: string }) {
  // Mic with a slash — skipped (file too long, integration absent, etc.)
  return (
    <svg {...svgProps} className={className}>
      <rect x="9" y="2" width="6" height="12" rx="3" />
      <path d="M19 10v2a7 7 0 0 1-14 0v-2" />
      <line x1="2" y1="2" x2="22" y2="22" />
    </svg>
  );
}
