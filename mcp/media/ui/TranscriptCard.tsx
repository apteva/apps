// TranscriptCard — surface a media file's transcript in chat. The
// agent calls this when answering "what's said in this video" or
// when it wants to ground its answer in the audio it just
// transcribed.
//
// Agent calls:
//   respond(components=[{app:"media", name:"transcript-card",
//                        props:{file_id:N, max_lines: 8}}])
//
// Subscribes to media.transcribed so a still-pending transcript
// auto-fills in once Deepgram finishes.

import { useEffect, useState } from "react";
import { Card, CardHeader, type CardVendor } from "@apteva/ui-kit";

const transcriptLogo = (
  <svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor" aria-hidden>
    <path d="M4 4h16v3H4zM4 10h16v3H4zM4 16h12v3H4z" />
  </svg>
);

const transcriptVendor: CardVendor = {
  name: "Transcript",
  logo: transcriptLogo,
  color: { light: "#0f766e", dark: "#5eead4" },
};

interface TranscriptRow {
  file_id: string;
  status: "pending" | "running" | "ok" | "failed" | "skipped";
  language?: string;
  text?: string;
  provider?: string;
  model?: string;
  duration_ms?: number;
  error?: string;
}

interface Props {
  file_id: string | number;
  projectId?: string;
  /** Cap on text lines displayed inline before "Show more". Default 8. */
  max_lines?: number;
  preview?: boolean;
}

const previewSample: TranscriptRow = {
  file_id: "0",
  status: "ok",
  language: "en",
  text:
    "Why don't you just admit that you're freaked out by my robot hand? " +
    "I'm not freaked out — I think it's amazing. " +
    "Then look at me when I'm talking to you.",
  provider: "deepgram",
  model: "nova-3",
};

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
          return bridge.subscribe("media", projectId, onEvent as any);
        }
            const url = `/api/app-events/media?project_id=${encodeURIComponent(projectId)}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      try {
        onEvent(JSON.parse(e.data));
      } catch {
        /* ignore */
      }
    };
    return () => es.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);
}

export default function TranscriptCard({ file_id, projectId, max_lines, preview }: Props) {
  const fid = String(file_id);
  const cap = max_lines ?? 8;
  const [row, setRow] = useState<TranscriptRow | null>(preview ? previewSample : null);
  const [missing, setMissing] = useState(false);
  const [expanded, setExpanded] = useState(false);

  const refetch = () => {
    if (preview || !projectId) return;
    const url =
      `/api/apps/media/transcripts/${encodeURIComponent(fid)}` +
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
        const t: TranscriptRow | undefined =
          (data.transcript as TranscriptRow | undefined) ??
          (data.status ? (data as TranscriptRow) : undefined);
        if (t) {
          setRow(t);
          setMissing(false);
        }
      })
      .catch(() => undefined);
  };

  useEffect(() => {
    refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fid, projectId]);

  useMediaEvents(preview ? undefined : projectId, (ev) => {
    if (String(ev?.data?.file_id ?? "") !== fid) return;
    if (ev.topic === "media.deleted") {
      setMissing(true);
      return;
    }
    if (ev.topic === "media.transcribed") refetch();
  });

  if (missing) {
    return (
      <Card>
        <CardHeader title={`Transcript #${fid}`} status={{ label: "no transcript", variant: "muted" }} />
      </Card>
    );
  }
  if (!row) {
    return (
      <Card>
        <CardHeader title={`Transcript #${fid}`} status={{ label: "loading", variant: "muted" }} />
      </Card>
    );
  }

  const status = statusLabel(row);
  const lines = (row.text ?? "").split(/(?<=[.!?])\s+/).filter(Boolean);
  const visibleLines = expanded ? lines : lines.slice(0, cap);
  const truncated = lines.length > cap && !expanded;

  return (
    <Card>
      <CardHeader
        vendor={transcriptVendor}
        title={row.language ? `Transcript · ${row.language}` : "Transcript"}
        subtitle={row.provider ? `${row.provider}${row.model ? ` / ${row.model}` : ""}` : undefined}
        status={status}
      />
      <div className="px-4 py-3 max-h-[24rem] overflow-y-auto">
        {row.status === "ok" && row.text ? (
          <>
            <p className="text-text text-sm leading-relaxed whitespace-pre-line">
              {visibleLines.join(" ")}
            </p>
            {truncated && (
              <button
                type="button"
                onClick={() => setExpanded(true)}
                className="text-xs text-accent hover:underline mt-2"
              >
                Show {lines.length - cap} more sentences
              </button>
            )}
          </>
        ) : row.status === "pending" || row.status === "running" ? (
          <p className="text-text-muted text-xs">Transcribing…</p>
        ) : row.status === "skipped" ? (
          <p className="text-text-muted text-xs leading-relaxed">
            {row.error || "Skipped."}
          </p>
        ) : row.status === "failed" ? (
          <p className="text-red text-xs leading-relaxed">
            {row.error || "Transcription failed."}
          </p>
        ) : (
          <p className="text-text-muted text-xs">No transcript text.</p>
        )}
      </div>
    </Card>
  );
}

function statusLabel(t: TranscriptRow): { label: string; variant: "muted" | "ok" | "warn" | "err" } {
  switch (t.status) {
    case "ok":
      return { label: "ok", variant: "ok" };
    case "pending":
    case "running":
      return { label: "running", variant: "muted" };
    case "skipped":
      return { label: "skipped", variant: "muted" };
    case "failed":
      return { label: "failed", variant: "err" };
    default:
      return { label: t.status, variant: "muted" };
  }
}
