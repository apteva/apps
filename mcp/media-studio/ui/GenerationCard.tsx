import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import {
  AudioLines,
  Download,
  ExternalLink,
  Image as ImageIcon,
  Maximize2,
  Pause,
  Play,
  Sparkles,
  Video,
  Volume2,
  VolumeX,
} from "lucide-react";
import { Card, CardHeader, type CardVendor } from "@apteva/ui-kit";
import {
  formatGenerationCost,
  formatGenerationDuration,
  generationMediaURLs,
  generationPoster,
  isAudioKind,
  isTerminalJobStatus,
  isVideoKind,
  mediaKindLabel,
  shouldRefreshGenerationCard,
} from "./generationCardLogic";

type Generation = {
  id: number;
  kind: string;
  prompt: string;
  revised_prompt?: string;
  provider: string;
  model: string;
  size?: string;
  duration_ms?: number;
  estimated_duration_seconds?: number;
  actual_duration_seconds?: number;
  storage_urls?: string[];
  upstream_urls?: string[];
  local_cache_url?: string;
  thumbnail_b64?: string;
  status: string;
  count?: number;
  cost_usd?: number;
  created_at?: string;
};

type GenerationJob = {
  id: number;
  kind: string;
  provider: string;
  model: string;
  prompt: string;
  status: string;
  error?: string;
  generation_id?: number;
  attempts?: number;
  cost_usd?: number;
  estimated_duration_seconds?: number;
};

type Props = {
  generation_id?: number | string;
  job_id?: number | string;
  projectId?: string;
  preview?: boolean;
};

type MediaEvent = {
  topic?: string;
  data?: { generation_id?: number | string; job_id?: number | string };
};

const studioVendor: CardVendor = {
  name: "Media Studio",
  logo: <Sparkles size={14} strokeWidth={1.8} aria-hidden="true" />,
  color: { light: "#c2410c", dark: "#fb923c" },
};

const previewGeneration: Generation = {
  id: 1,
  kind: "image",
  prompt: "A cinematic Barcelona terrace in warm Mediterranean light",
  provider: "venice-ai",
  model: "gpt-image-2",
  status: "ready",
  count: 1,
  cost_usd: 0.27,
  storage_urls: ["https://picsum.photos/seed/media-studio-generation/960/640"],
};

export default function GenerationCard({
  generation_id,
  job_id,
  projectId,
  preview,
}: Props) {
  const generationId = positiveNumber(generation_id);
  const jobId = positiveNumber(job_id);
  const [generation, setGeneration] = useState<Generation | null>(preview ? previewGeneration : null);
  const [job, setJob] = useState<GenerationJob | null>(null);
  const [loading, setLoading] = useState(!preview);
  const [missing, setMissing] = useState(false);
  const [error, setError] = useState("");

  const fetchGeneration = useCallback(async (id: number): Promise<Generation | null> => {
    if (!projectId) return null;
    const response = await fetch(
      `/api/apps/media-studio/generations/${id}?project_id=${encodeURIComponent(projectId)}`,
      { credentials: "same-origin" },
    );
    if (response.status === 404) {
      setMissing(true);
      return null;
    }
    if (!response.ok) throw new Error(`Generation request failed (${response.status})`);
    const body = await response.json();
    const row = body.generation as Generation | undefined;
    if (row) {
      setGeneration(row);
      setMissing(false);
    }
    return row || null;
  }, [projectId]);

  const refresh = useCallback(async () => {
    if (preview) return;
    if (!projectId || (!generationId && !jobId)) {
      setError(!projectId ? "Project context is unavailable." : "No generation was selected.");
      setLoading(false);
      return;
    }
    try {
      setError("");
      if (jobId) {
        const response = await fetch(
          `/api/apps/media-studio/video-jobs/${jobId}?project_id=${encodeURIComponent(projectId)}`,
          { credentials: "same-origin" },
        );
        if (response.status === 404) {
          setMissing(true);
          return;
        }
        if (!response.ok) throw new Error(`Job request failed (${response.status})`);
        const body = await response.json();
        const row = body.job as GenerationJob | undefined;
        if (row) {
          setJob(row);
          setMissing(false);
          if (row.generation_id) await fetchGeneration(row.generation_id);
        }
      } else if (generationId) {
        await fetchGeneration(generationId);
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Unable to load this generation.");
    } finally {
      setLoading(false);
    }
  }, [fetchGeneration, generationId, jobId, preview, projectId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (preview || !jobId || generation || isTerminalJobStatus(job?.status)) return;
    const timer = window.setInterval(() => void refresh(), 12000);
    return () => window.clearInterval(timer);
  }, [generation, job?.status, jobId, preview, refresh]);

  useEffect(() => {
    if (preview || !projectId) return;
    const onEvent = (event: MediaEvent) => {
      if (shouldRefreshGenerationCard(event, generationId, jobId)) void refresh();
    };
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(app: string, projectId: string, fn: (event: MediaEvent) => void): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe("media-studio", projectId, onEvent);

    const source = new EventSource(
      `/api/app-events/media-studio?project_id=${encodeURIComponent(projectId)}`,
      { withCredentials: true },
    );
    source.onmessage = (event) => {
      try { onEvent(JSON.parse(event.data)); } catch { /* ignore malformed frames */ }
    };
    return () => source.close();
  }, [generationId, jobId, preview, projectId, refresh]);

  return (
    <div className="msc-root">
      <style>{cardStyles}</style>
      {generation ? (
        <CompletedGeneration generation={generation} />
      ) : job ? (
        <PendingGeneration job={job} />
      ) : (
        <Card>
          <CardHeader
            vendor={studioVendor}
            title={missing ? "Generation unavailable" : "Loading generation"}
            status={{
              label: missing ? "missing" : error ? "error" : "loading",
              variant: missing ? "muted" : error ? "err" : "muted",
            }}
          />
          <div className="msc-message">
            {missing ? "This generation was deleted or belongs to another project." : error || (loading ? "Loading media…" : "Media unavailable.")}
          </div>
        </Card>
      )}
    </div>
  );
}

function CompletedGeneration({ generation }: { generation: Generation }) {
  const urls = generationMediaURLs(generation);
  const primaryURL = urls[0] || "";
  const poster = generationPoster(generation);
  const title = mediaKindLabel(generation.kind);
  const duration = generation.actual_duration_seconds ||
    (generation.duration_ms ? generation.duration_ms / 1000 : 0);
  return (
    <Card>
      <CardHeader
        vendor={studioVendor}
        title={title}
        subtitle={[generation.provider, generation.model].filter(Boolean).join(" · ")}
        status={{ label: "ready", variant: "ok" }}
      />
      <GenerationPreview generation={generation} urls={urls} poster={poster} />
      <div className="msc-details">
        <p className="msc-prompt">{generation.revised_prompt || generation.prompt}</p>
        <div className="msc-meta" aria-label="Generation details">
          {generation.size && <span>{generation.size}</span>}
          {duration > 0 && <span>{formatGenerationDuration(duration)}</span>}
          {generation.count && generation.count > 1 && <span>{generation.count} outputs</span>}
          {formatGenerationCost(generation.cost_usd) && <span>{formatGenerationCost(generation.cost_usd)}</span>}
        </div>
        {primaryURL && (
          <div className="msc-actions">
            <a href={primaryURL} target="_blank" rel="noreferrer" title="Open generated media">
              <ExternalLink size={14} aria-hidden="true" /> Open
            </a>
            <a href={primaryURL} download title="Download generated media">
              <Download size={14} aria-hidden="true" /> Download
            </a>
          </div>
        )}
      </div>
    </Card>
  );
}

function GenerationPreview({
  generation,
  urls,
  poster,
}: {
  generation: Generation;
  urls: string[];
  poster?: string;
}) {
  if (generation.kind === "image") {
    const images = urls.length > 0 ? urls.slice(0, 4) : poster ? [poster] : [];
    if (images.length === 0) return <EmptyPreview icon={<ImageIcon size={24} />} />;
    return (
      <div className={`msc-images ${images.length > 1 ? "msc-images-grid" : ""}`}>
        {images.map((url, index) => (
          <img key={url} src={url} alt={`${generation.prompt || "Generated image"}${images.length > 1 ? `, variation ${index + 1}` : ""}`} />
        ))}
        {urls.length > 4 && <span className="msc-more">+{urls.length - 4}</span>}
      </div>
    );
  }
  const primary = urls[0];
  if (!primary) return <EmptyPreview icon={isVideoKind(generation.kind) ? <Video size={24} /> : <AudioLines size={24} />} />;
  if (isVideoKind(generation.kind)) {
    return <StudioPlayer src={primary} mediaType="video" label={mediaKindLabel(generation.kind)} poster={poster} />;
  }
  if (isAudioKind(generation.kind)) {
    return <StudioPlayer src={primary} mediaType="audio" label={mediaKindLabel(generation.kind)} />;
  }
  return <EmptyPreview icon={<Sparkles size={24} />} />;
}

function PendingGeneration({ job }: { job: GenerationJob }) {
  const failed = job.status === "failed";
  const label = job.status === "polling" ? "generating" : job.status;
  return (
    <Card>
      <CardHeader
        vendor={studioVendor}
        title={mediaKindLabel(job.kind)}
        subtitle={[job.provider, job.model].filter(Boolean).join(" · ")}
        status={{ label, variant: failed ? "err" : "muted" }}
      />
      <div className="msc-pending">
        <div className="msc-pending-icon" aria-hidden="true">
          {isVideoKind(job.kind) ? <Video size={22} /> : <Sparkles size={22} />}
        </div>
        <div className="msc-pending-copy">
          <strong>{failed ? "Generation failed" : job.status === "finalizing" ? "Preparing media" : "Creating media"}</strong>
          <span>{failed ? job.error || "The provider could not complete this generation." : job.prompt}</span>
        </div>
      </div>
      {!failed && <div className="msc-progress" role="progressbar" aria-label="Generation in progress"><span /></div>}
      <div className="msc-details msc-details-compact">
        <div className="msc-meta">
          <span>job #{job.id}</span>
          {job.estimated_duration_seconds ? <span>est. {formatGenerationDuration(job.estimated_duration_seconds)}</span> : null}
          {formatGenerationCost(job.cost_usd) && <span>{formatGenerationCost(job.cost_usd)}</span>}
        </div>
      </div>
    </Card>
  );
}

function EmptyPreview({ icon }: { icon: ReactNode }) {
  return <div className="msc-empty" aria-label="No media preview available">{icon}</div>;
}

const PLAYER_EVENT = "media-studio:generation-card-play";

function StudioPlayer({
  src,
  mediaType,
  label,
  poster,
}: {
  src: string;
  mediaType: "audio" | "video";
  label: string;
  poster?: string;
}) {
  const playerId = useId();
  const mediaRef = useRef<HTMLMediaElement | null>(null);
  const stageRef = useRef<HTMLDivElement | null>(null);
  const [playing, setPlaying] = useState(false);
  const [muted, setMuted] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);

  useEffect(() => {
    const pauseOther = (event: Event) => {
      if ((event as CustomEvent<string>).detail !== playerId) mediaRef.current?.pause();
    };
    window.addEventListener(PLAYER_EVENT, pauseOther);
    return () => window.removeEventListener(PLAYER_EVENT, pauseOther);
  }, [playerId]);

  const announcePlay = () => window.dispatchEvent(new CustomEvent(PLAYER_EVENT, { detail: playerId }));
  const togglePlayback = () => {
    const media = mediaRef.current;
    if (!media) return;
    if (media.paused) {
      announcePlay();
      void media.play().catch(() => setPlaying(false));
    } else {
      media.pause();
    }
  };
  const toggleMuted = () => {
    const media = mediaRef.current;
    if (!media) return;
    media.muted = !media.muted;
    setMuted(media.muted);
  };
  const syncDuration = () => {
    const value = mediaRef.current?.duration || 0;
    if (Number.isFinite(value) && value > 0) setDuration(value);
  };
  const controls = (
    <div className="msc-player-controls">
      <button type="button" onClick={togglePlayback} aria-label={playing ? `Pause ${label}` : `Play ${label}`} title={playing ? "Pause" : "Play"}>
        {playing ? <Pause size={16} /> : <Play size={16} fill="currentColor" />}
      </button>
      <input
        type="range"
        min={0}
        max={Math.max(duration, 0.01)}
        step={0.01}
        value={Math.min(currentTime, Math.max(duration, 0.01))}
        onChange={(event) => {
          const value = Number(event.currentTarget.value);
          if (mediaRef.current) mediaRef.current.currentTime = value;
          setCurrentTime(value);
        }}
        aria-label={`Seek ${label}`}
      />
      <span>{playerTime(currentTime)} / {playerTime(duration)}</span>
      <button type="button" onClick={toggleMuted} aria-label={muted ? `Unmute ${label}` : `Mute ${label}`} title={muted ? "Unmute" : "Mute"}>
        {muted ? <VolumeX size={16} /> : <Volume2 size={16} />}
      </button>
      {mediaType === "video" && (
        <button type="button" onClick={() => void stageRef.current?.requestFullscreen?.()} aria-label={`View ${label} fullscreen`} title="Fullscreen">
          <Maximize2 size={16} />
        </button>
      )}
    </div>
  );

  const events = {
    onLoadedMetadata: syncDuration,
    onDurationChange: syncDuration,
    onTimeUpdate: () => setCurrentTime(mediaRef.current?.currentTime || 0),
    onPlay: () => { announcePlay(); setPlaying(true); },
    onPause: () => setPlaying(false),
    onEnded: () => setPlaying(false),
    onVolumeChange: () => setMuted(Boolean(mediaRef.current?.muted)),
  };
  if (mediaType === "audio") {
    return (
      <div className="msc-audio-player">
        <div className="msc-audio-art" aria-hidden="true"><AudioLines size={28} /></div>
        <div className="msc-audio-body">
          <strong>{label}</strong>
          <audio ref={(node) => { mediaRef.current = node; }} src={src} preload="metadata" {...events} />
          {controls}
        </div>
      </div>
    );
  }
  return (
    <div ref={stageRef} className="msc-video-player">
      <video ref={(node) => { mediaRef.current = node; }} src={src} poster={poster} preload="metadata" playsInline onClick={togglePlayback} {...events} />
      {!playing && (
        <button className="msc-video-play" type="button" onClick={togglePlayback} aria-label={`Play ${label}`} title="Play">
          <Play size={22} fill="currentColor" />
        </button>
      )}
      {controls}
    </div>
  );
}

function positiveNumber(value: number | string | undefined): number | undefined {
  const parsed = Number(value || 0);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : undefined;
}

function playerTime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0:00";
  const total = Math.floor(seconds);
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

const cardStyles = `
.msc-root { width: 100%; min-width: 0; letter-spacing: 0; }
.msc-root img, .msc-root video { display: block; max-width: 100%; }
.msc-images { position: relative; background: #090909; overflow: hidden; }
.msc-images img { width: 100%; max-height: 420px; object-fit: contain; margin: 0 auto; }
.msc-images-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1px; }
.msc-images-grid img { width: 100%; height: 220px; object-fit: cover; }
.msc-more { position: absolute; right: 10px; bottom: 10px; padding: 3px 7px; border-radius: 4px; background: rgba(0,0,0,.78); color: #fff; font: 600 11px/1.4 ui-monospace, monospace; }
.msc-details { padding: 13px 16px 14px; border-top: 1px solid var(--border, #2a2a2a); }
.msc-details-compact { padding-top: 10px; }
.msc-prompt { margin: 0; color: var(--text, #eee); font-size: 13px; line-height: 1.48; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.msc-meta { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 10px; }
.msc-meta span { padding: 3px 6px; border: 1px solid var(--border, #303030); border-radius: 4px; color: var(--text-muted, #999); font: 500 10px/1.3 ui-monospace, monospace; }
.msc-actions { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.msc-actions a { display: inline-flex; align-items: center; gap: 6px; min-height: 30px; padding: 0 9px; border: 1px solid var(--border, #303030); border-radius: 5px; color: var(--text, #eee); font-size: 11px; font-weight: 600; text-decoration: none; }
.msc-actions a:hover, .msc-actions a:focus-visible { border-color: #f97316; color: #fb923c; outline: none; }
.msc-empty { height: 180px; display: grid; place-items: center; color: var(--text-muted, #888); background: var(--bg-input, #111); }
.msc-message { padding: 18px 16px; color: var(--text-muted, #999); font-size: 12px; line-height: 1.5; }
.msc-pending { display: grid; grid-template-columns: 42px minmax(0, 1fr); gap: 12px; align-items: center; padding: 18px 16px 14px; }
.msc-pending-icon { width: 42px; height: 42px; display: grid; place-items: center; border-radius: 6px; color: #fb923c; background: color-mix(in srgb, #f97316 12%, transparent); border: 1px solid color-mix(in srgb, #f97316 28%, transparent); }
.msc-pending-copy { min-width: 0; display: grid; gap: 4px; }
.msc-pending-copy strong { color: var(--text, #eee); font-size: 13px; }
.msc-pending-copy span { color: var(--text-muted, #999); font-size: 11px; line-height: 1.42; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.msc-progress { height: 3px; overflow: hidden; background: var(--bg-input, #151515); }
.msc-progress span { display: block; width: 38%; height: 100%; background: #f97316; animation: msc-progress 1.35s ease-in-out infinite; }
@keyframes msc-progress { from { transform: translateX(-110%); } to { transform: translateX(290%); } }
.msc-video-player { position: relative; width: 100%; aspect-ratio: 16 / 10; overflow: hidden; background: #050505; }
.msc-video-player video { width: 100%; height: 100%; object-fit: contain; cursor: pointer; }
.msc-video-player:fullscreen { display: grid; place-items: center; background: #000; }
.msc-video-play { position: absolute; left: 50%; top: 50%; transform: translate(-50%, -50%); width: 48px; height: 48px; display: grid; place-items: center; border: 1px solid rgba(255,255,255,.42); border-radius: 50%; color: #fff; background: rgba(0,0,0,.68); cursor: pointer; }
.msc-audio-player { display: grid; grid-template-columns: 74px minmax(0, 1fr); gap: 14px; align-items: center; min-height: 112px; padding: 16px; background: var(--bg-input, #111); }
.msc-audio-art { width: 74px; height: 74px; display: grid; place-items: center; border-radius: 6px; color: #fb923c; background: #18110d; border: 1px solid #3a2418; }
.msc-audio-body { min-width: 0; display: grid; gap: 10px; }
.msc-audio-body > strong { color: var(--text, #eee); font-size: 12px; }
.msc-audio-body audio { display: none; }
.msc-player-controls { display: flex; align-items: center; gap: 7px; min-width: 0; }
.msc-video-player .msc-player-controls { position: absolute; left: 8px; right: 8px; bottom: 8px; padding: 6px; border-radius: 5px; background: rgba(0,0,0,.76); backdrop-filter: blur(8px); }
.msc-player-controls button { flex: 0 0 30px; width: 30px; height: 30px; display: grid; place-items: center; border: 0; border-radius: 4px; padding: 0; color: inherit; background: transparent; cursor: pointer; }
.msc-video-player .msc-player-controls button, .msc-video-player .msc-player-controls span { color: #fff; }
.msc-player-controls button:hover, .msc-player-controls button:focus-visible { color: #fb923c; background: rgba(255,255,255,.08); outline: none; }
.msc-player-controls input[type=range] { min-width: 40px; flex: 1 1 auto; accent-color: #f97316; }
.msc-player-controls > span { flex: 0 0 auto; color: var(--text-muted, #999); font: 500 10px/1 ui-monospace, monospace; }
@media (max-width: 520px) {
  .msc-images-grid img { height: 150px; }
  .msc-audio-player { grid-template-columns: 46px minmax(0, 1fr); padding: 12px; gap: 10px; }
  .msc-audio-art { width: 46px; height: 62px; }
  .msc-player-controls { gap: 3px; }
  .msc-player-controls > span { display: none; }
  .msc-actions a { flex: 1 1 120px; justify-content: center; }
}
@media (prefers-reduced-motion: reduce) { .msc-progress span { animation-duration: 3s; } }
`;
