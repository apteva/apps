// PodcastPanel — manage podcast shows and episodes.
//
// Two-pane: the shows list (left) and the selected show's detail
// (right) — feed URL, a feed health check, the episode table and the
// add-episode form. Episode rows carry their lifecycle inline: attach
// audio (a storage file id, probed via the media app), publish /
// unpublish, schedule, delete.
//
// The panel only talks to this app's REST surface; cross-app wiring
// (storage probe, routes/domains hostname claim, analytics) happens
// server-side and surfaces here as the `warning` string the create /
// audio endpoints return.

import { useCallback, useEffect, useMemo, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Show {
  id: number;
  slug: string;
  title: string;
  description: string;
  author: string;
  owner_email: string;
  language: string;
  category: string;
  explicit: boolean;
  link: string;
  podcast_type: "episodic" | "serial";
  image_file_id: string;
  copyright: string;
  hostname: string;
  project_id?: string;
}

interface Episode {
  id: number;
  show_id: number;
  guid: string;
  title: string;
  description: string;
  season_number?: number;
  episode_number?: number;
  episode_type: "full" | "trailer" | "bonus";
  status: "draft" | "scheduled" | "published";
  audio_file_id: string;
  audio_url: string;
  audio_bytes: number;
  duration_seconds: number;
  mime_type: string;
  image_file_id: string;
  publish_at?: string;
  published_at?: string;
  downloads: number;
}

interface ValidateResult {
  ok: boolean;
  feed_url: string;
  published: number;
  issues: string[];
}

const API = "/api/apps/podcast/api";

export default function PodcastPanel({ projectId, installId }: NativePanelProps) {
  const [shows, setShows] = useState<Show[] | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [warning, setWarning] = useState("");

  const params = useMemo(
    () => new URLSearchParams({ project_id: projectId, install_id: String(installId) }).toString(),
    [projectId, installId],
  );

  const loadShows = useCallback(async () => {
    try {
      const r = await fetch(`${API}/shows?${params}`, { credentials: "same-origin" });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { shows: Show[] | null };
      const list = j.shows || [];
      setShows(list);
      setError("");
      setSelectedId((cur) => (cur && list.some((s) => s.id === cur) ? cur : list[0]?.id ?? null));
    } catch (e) {
      setError((e as Error).message);
      setShows([]);
    }
  }, [params]);

  useEffect(() => {
    loadShows();
  }, [loadShows]);

  const selected = shows?.find((s) => s.id === selectedId) ?? null;

  return (
    <div className="h-full flex flex-col">
      <header className="px-4 py-3 border-b border-border flex items-baseline gap-3">
        <h1 className="text-text font-semibold flex items-center gap-2">
          <MicIcon />
          Podcast
        </h1>
        <span className="text-xs text-text-muted flex-1">
          Host shows and episodes. Audio lives in Storage; duration is probed by Media; the RSS feed is served by this app.
        </span>
        <button
          type="button"
          onClick={loadShows}
          className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input"
        >
          Refresh
        </button>
      </header>

      {error && <Banner tone="red">{error}</Banner>}
      {warning && <Banner tone="amber">Wiring warning: {warning}</Banner>}

      <div className="flex-1 flex overflow-hidden">
        <aside className="w-64 border-r border-border flex flex-col overflow-hidden">
          <div className="flex-1 overflow-auto">
            {shows === null ? (
              <div className="p-4 text-text-muted text-sm">Loading…</div>
            ) : shows.length === 0 ? (
              <div className="p-4 text-text-dim text-xs">No shows yet. Create one below.</div>
            ) : (
              shows.map((s) => (
                <button
                  type="button"
                  key={s.id}
                  onClick={() => setSelectedId(s.id)}
                  className={
                    "w-full text-left px-3 py-2 border-b border-border hover:bg-bg-input/40 " +
                    (s.id === selectedId ? "bg-bg-input/60" : "")
                  }
                >
                  <div className="text-text text-sm truncate">{s.title}</div>
                  <div className="text-text-dim text-[11px] font-mono truncate">/{s.slug}</div>
                </button>
              ))
            )}
          </div>
          <AddShowForm
            params={params}
            onAdded={(warn, id) => {
              setWarning(warn || "");
              loadShows().then(() => id && setSelectedId(id));
            }}
            setError={setError}
          />
        </aside>

        <main className="flex-1 overflow-auto">
          {selected ? (
            <ShowDetail
              key={selected.id}
              show={selected}
              params={params}
              setError={setError}
              setWarning={setWarning}
              onShowDeleted={() => {
                setSelectedId(null);
                loadShows();
              }}
            />
          ) : (
            <EmptyState />
          )}
        </main>
      </div>
    </div>
  );
}

// ─── show detail ───────────────────────────────────────────────────

function ShowDetail({
  show,
  params,
  setError,
  setWarning,
  onShowDeleted,
}: {
  show: Show;
  params: string;
  setError: (s: string) => void;
  setWarning: (s: string) => void;
  onShowDeleted: () => void;
}) {
  const [episodes, setEpisodes] = useState<Episode[] | null>(null);
  const [validation, setValidation] = useState<ValidateResult | null>(null);
  const [busy, setBusy] = useState(false);

  const loadEpisodes = useCallback(async () => {
    try {
      const r = await fetch(`${API}/episodes?${params}&show_id=${show.id}`, {
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { episodes: Episode[] | null };
      setEpisodes(j.episodes || []);
      setError("");
    } catch (e) {
      setError((e as Error).message);
      setEpisodes([]);
    }
  }, [params, show.id, setError]);

  useEffect(() => {
    setValidation(null);
    loadEpisodes();
  }, [loadEpisodes]);

  const validate = async () => {
    try {
      const r = await fetch(`${API}/shows/${show.id}/validate?${params}`, {
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      setValidation((await r.json()) as ValidateResult);
    } catch (e) {
      setError("Validate failed: " + (e as Error).message);
    }
  };

  const deleteShow = async () => {
    if (!confirm(`Delete "${show.title}" and all its episodes?`)) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/shows/${show.id}?${params}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      onShowDeleted();
    } catch (e) {
      setError("Delete failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col">
      <div className="px-4 py-3 border-b border-border">
        <div className="flex items-baseline gap-3">
          <h2 className="text-text font-semibold">{show.title}</h2>
          {show.explicit && (
            <span className="text-[10px] px-1 py-0.5 border border-border rounded text-text-muted">
              explicit
            </span>
          )}
          <span className="text-[11px] text-text-dim">{show.podcast_type}</span>
          <div className="flex-1" />
          <button
            type="button"
            onClick={validate}
            className="px-2 py-0.5 text-xs border border-border rounded hover:bg-bg-input"
          >
            Validate feed
          </button>
          <button
            type="button"
            onClick={deleteShow}
            disabled={busy}
            className="px-2 py-0.5 text-xs border border-border rounded text-red/80 hover:text-red disabled:opacity-50"
          >
            Delete show
          </button>
        </div>
        <FeedLink slug={show.slug} />
        {validation && <ValidationBlock result={validation} />}
      </div>

      <EpisodeTable
        episodes={episodes}
        params={params}
        setError={setError}
        setWarning={setWarning}
        reload={loadEpisodes}
      />

      <AddEpisodeForm
        showId={show.id}
        params={params}
        setError={setError}
        onAdded={loadEpisodes}
      />
    </div>
  );
}

function FeedLink({ slug }: { slug: string }) {
  const [copied, setCopied] = useState(false);
  // The absolute feed URL depends on the platform host; the relative
  // path is always /feed/{slug}.xml on this app's origin.
  const path = `/feed/${slug}.xml`;
  return (
    <div className="mt-2 flex items-center gap-2 text-xs">
      <span className="text-text-muted">RSS feed</span>
      <code className="text-accent font-mono">{path}</code>
      <button
        type="button"
        onClick={() => {
          navigator.clipboard?.writeText(path);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
        className="text-text-dim hover:text-text"
      >
        {copied ? "copied" : "copy"}
      </button>
    </div>
  );
}

function ValidationBlock({ result }: { result: ValidateResult }) {
  return (
    <div className="mt-3 border border-border rounded p-3 text-xs">
      <div className="flex items-center gap-2">
        <StatusDot ok={result.ok} />
        <span className="text-text">
          {result.ok
            ? `Feed looks good — ${result.published} published episode${result.published === 1 ? "" : "s"}.`
            : `${result.issues.length} issue${result.issues.length === 1 ? "" : "s"} to fix before submitting.`}
        </span>
      </div>
      {result.issues.length > 0 && (
        <ul className="mt-2 ml-5 list-disc text-text-muted space-y-0.5">
          {result.issues.map((iss, i) => (
            <li key={i}>{iss}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

// ─── episode table ─────────────────────────────────────────────────

function EpisodeTable({
  episodes,
  params,
  setError,
  setWarning,
  reload,
}: {
  episodes: Episode[] | null;
  params: string;
  setError: (s: string) => void;
  setWarning: (s: string) => void;
  reload: () => void;
}) {
  if (episodes === null) {
    return <div className="p-6 text-text-muted text-sm">Loading episodes…</div>;
  }
  if (episodes.length === 0) {
    return (
      <div className="p-8 text-center text-text-dim text-xs">
        No episodes yet. Add one below, then attach a Storage audio file.
      </div>
    );
  }
  return (
    <table className="w-full text-xs">
      <thead className="sticky top-0 bg-bg-input/40">
        <tr className="text-text-muted">
          <th className="text-left px-3 py-2 font-normal">Episode</th>
          <th className="text-left px-3 py-2 font-normal">Status</th>
          <th className="text-right px-3 py-2 font-normal">Duration</th>
          <th className="text-right px-3 py-2 font-normal">Downloads</th>
          <th className="text-right px-3 py-2 font-normal">Actions</th>
        </tr>
      </thead>
      <tbody>
        {episodes.map((ep) => (
          <EpisodeRow
            key={ep.id}
            ep={ep}
            params={params}
            setError={setError}
            setWarning={setWarning}
            reload={reload}
          />
        ))}
      </tbody>
    </table>
  );
}

function EpisodeRow({
  ep,
  params,
  setError,
  setWarning,
  reload,
}: {
  ep: Episode;
  params: string;
  setError: (s: string) => void;
  setWarning: (s: string) => void;
  reload: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [audioFileId, setAudioFileId] = useState(ep.audio_file_id);
  const [scheduleAt, setScheduleAt] = useState("");

  const act = async (path: string, body?: unknown) => {
    setBusy(true);
    try {
      const r = await fetch(`${API}/episodes/${ep.id}${path}?${params}`, {
        method: body === undefined && !path ? "DELETE" : "POST",
        credentials: "same-origin",
        headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
        body: body !== undefined ? JSON.stringify(body) : undefined,
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json().catch(() => ({}))) as { warning?: string };
      if (j.warning) setWarning(j.warning);
      reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm(`Delete episode "${ep.title}"?`)) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/episodes/${ep.id}?${params}`, {
        method: "DELETE",
        credentials: "same-origin",
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      reload();
    } catch (e) {
      setError("Delete failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <tr className="border-t border-border hover:bg-bg-input/30">
        <td className="px-3 py-2">
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            className="text-text hover:text-accent text-left"
          >
            {ep.title}
          </button>
          {ep.episode_type !== "full" && (
            <span className="ml-1 text-text-dim text-[10px]">({ep.episode_type})</span>
          )}
          {!ep.audio_file_id && (
            <span className="ml-2 text-amber text-[10px]">no audio</span>
          )}
        </td>
        <td className="px-3 py-2">
          <StatusBadge status={ep.status} />
        </td>
        <td className="px-3 py-2 text-right text-text-muted font-mono">
          {ep.duration_seconds > 0 ? formatDuration(ep.duration_seconds) : "—"}
        </td>
        <td className="px-3 py-2 text-right text-text-muted font-mono">
          {ep.downloads.toLocaleString()}
        </td>
        <td className="px-3 py-2 text-right whitespace-nowrap">
          {ep.status === "published" ? (
            <button
              type="button"
              onClick={() => act("/unpublish", {})}
              disabled={busy}
              className="text-text-muted hover:text-text disabled:opacity-50"
            >
              Unpublish
            </button>
          ) : (
            <button
              type="button"
              onClick={() => act("/publish", {})}
              disabled={busy}
              className="text-accent hover:underline disabled:opacity-50"
            >
              Publish
            </button>
          )}
          <span className="text-border mx-2">|</span>
          <button
            type="button"
            onClick={remove}
            disabled={busy}
            className="text-red/70 hover:text-red disabled:opacity-50"
          >
            Delete
          </button>
        </td>
      </tr>
      {expanded && (
        <tr className="border-t border-border bg-bg-input/20">
          <td colSpan={5} className="px-3 py-3">
            <div className="flex flex-col gap-3 max-w-2xl">
              {/* Attach audio */}
              <div className="flex items-end gap-2">
                <div className="flex-1">
                  <label className="text-[11px] text-text-muted block mb-1">
                    Storage audio file id
                  </label>
                  <input
                    type="text"
                    value={audioFileId}
                    onChange={(e) => setAudioFileId(e.target.value)}
                    placeholder="e.g. 42"
                    className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
                  />
                </div>
                <button
                  type="button"
                  onClick={() => act("/audio", { audio_file_id: audioFileId.trim() })}
                  disabled={busy || !audioFileId.trim()}
                  className="px-3 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
                >
                  Attach &amp; probe
                </button>
              </div>
              {ep.audio_file_id && (
                <div className="text-[11px] text-text-dim font-mono">
                  {formatBytes(ep.audio_bytes)} · {ep.mime_type || "audio/mpeg"} ·{" "}
                  <a
                    href={ep.audio_url}
                    target="_blank"
                    rel="noreferrer"
                    className="text-accent hover:underline"
                  >
                    enclosure
                  </a>
                </div>
              )}

              {/* Schedule */}
              <div className="flex items-end gap-2">
                <div>
                  <label className="text-[11px] text-text-muted block mb-1">
                    Schedule publish
                  </label>
                  <input
                    type="datetime-local"
                    value={scheduleAt}
                    onChange={(e) => setScheduleAt(e.target.value)}
                    className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
                  />
                </div>
                <button
                  type="button"
                  onClick={() => {
                    if (!scheduleAt) return;
                    act("/schedule", { publish_at: new Date(scheduleAt).toISOString() });
                  }}
                  disabled={busy || !scheduleAt}
                  className="px-3 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
                >
                  Schedule
                </button>
                {ep.status === "scheduled" && ep.publish_at && (
                  <span className="text-[11px] text-text-dim self-center">
                    queued for {ep.publish_at} UTC
                  </span>
                )}
              </div>

              {ep.description && (
                <div className="text-[11px] text-text-muted whitespace-pre-wrap">
                  {ep.description}
                </div>
              )}
              <div className="text-[10px] text-text-dim font-mono">guid: {ep.guid}</div>
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

// ─── forms ─────────────────────────────────────────────────────────

function AddShowForm({
  params,
  onAdded,
  setError,
}: {
  params: string;
  onAdded: (warning: string, id?: number) => void;
  setError: (s: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [author, setAuthor] = useState("");
  const [hostname, setHostname] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/shows?${params}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: title.trim(),
          author: author.trim(),
          hostname: hostname.trim(),
        }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      const j = (await r.json()) as { warning?: string; show: { id: number } };
      setTitle("");
      setAuthor("");
      setHostname("");
      onAdded(j.warning || "", j.show?.id);
    } catch (e) {
      setError("Create show failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="border-t border-border p-3 flex flex-col gap-2">
      <div className="text-[11px] text-text-muted font-medium">New show</div>
      <input
        type="text"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Show title"
        className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
      />
      <input
        type="text"
        value={author}
        onChange={(e) => setAuthor(e.target.value)}
        placeholder="Author"
        className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
      />
      <input
        type="text"
        value={hostname}
        onChange={(e) => setHostname(e.target.value)}
        placeholder="Custom feed host (optional)"
        className="bg-bg-input border border-border rounded px-2 py-1 text-xs font-mono"
      />
      <button
        type="submit"
        disabled={busy || !title.trim()}
        className="px-2 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
      >
        Create show
      </button>
    </form>
  );
}

function AddEpisodeForm({
  showId,
  params,
  setError,
  onAdded,
}: {
  showId: number;
  params: string;
  setError: (s: string) => void;
  onAdded: () => void;
}) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [episodeType, setEpisodeType] = useState<"full" | "trailer" | "bonus">("full");
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    try {
      const r = await fetch(`${API}/episodes?${params}`, {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          show_id: showId,
          title: title.trim(),
          description: description.trim(),
          episode_type: episodeType,
        }),
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      setTitle("");
      setDescription("");
      setEpisodeType("full");
      onAdded();
    } catch (e) {
      setError("Create episode failed: " + (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="border-t border-border px-4 py-3 flex flex-col gap-2">
      <div className="text-[11px] text-text-muted font-medium">New episode</div>
      <div className="flex items-end gap-2">
        <div className="flex-1">
          <input
            type="text"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="Episode title"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-xs"
          />
        </div>
        <select
          value={episodeType}
          onChange={(e) => setEpisodeType(e.target.value as "full" | "trailer" | "bonus")}
          className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
        >
          <option value="full">full</option>
          <option value="trailer">trailer</option>
          <option value="bonus">bonus</option>
        </select>
        <button
          type="submit"
          disabled={busy || !title.trim()}
          className="px-3 py-1 text-xs border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >
          Add episode
        </button>
      </div>
      <textarea
        value={description}
        onChange={(e) => setDescription(e.target.value)}
        placeholder="Show notes (HTML allowed)"
        rows={2}
        className="bg-bg-input border border-border rounded px-2 py-1 text-xs"
      />
    </form>
  );
}

// ─── presentational bits ───────────────────────────────────────────

function StatusBadge({ status }: { status: Episode["status"] }) {
  const map: Record<Episode["status"], { label: string; cls: string }> = {
    draft: { label: "draft", cls: "border-border text-text-muted" },
    scheduled: { label: "scheduled", cls: "border-amber/40 text-amber" },
    published: { label: "published", cls: "border-accent/40 text-accent" },
  };
  const s = map[status];
  return <span className={"px-1.5 py-0.5 text-[10px] rounded border " + s.cls}>{s.label}</span>;
}

function StatusDot({ ok }: { ok: boolean }) {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true">
      <circle
        cx="6"
        cy="6"
        r="5"
        fill="none"
        strokeWidth="2"
        style={{ stroke: ok ? "var(--color-accent)" : "var(--color-amber)" }}
      />
    </svg>
  );
}

function Banner({ tone, children }: { tone: "red" | "amber"; children: React.ReactNode }) {
  return (
    <div
      className="px-4 py-2 text-xs border-b border-border"
      style={{ color: tone === "red" ? "var(--color-red)" : "var(--color-amber)" }}
    >
      {children}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="p-8 flex flex-col items-center text-center gap-3">
      <MicIcon size={48} dim />
      <div className="text-text-muted text-sm">Select or create a show.</div>
      <div className="text-text-dim text-xs max-w-md">
        Each show has its own RSS feed. Add episodes, attach a Storage audio file to each
        (Media probes the duration), then publish — the feed updates immediately.
      </div>
    </div>
  );
}

function MicIcon({ size = 16, dim = false }: { size?: number; dim?: boolean }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      style={{ color: dim ? "var(--color-text-dim)" : "currentColor" }}
    >
      <rect x="9" y="2" width="6" height="12" rx="3" />
      <path d="M5 10v2a7 7 0 0 0 14 0v-2" />
      <line x1="12" y1="19" x2="12" y2="22" />
    </svg>
  );
}

// ─── format helpers ────────────────────────────────────────────────

function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
