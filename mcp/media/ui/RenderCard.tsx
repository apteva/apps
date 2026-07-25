// RenderCard — live-updating progress card for one media render. The
// agent surfaces this immediately after submitting media_trim /
// media_resize / media_transcode etc., and the card auto-promotes to
// a preview of the output once the render hits status=ok.
//
// Agent calls:
//   respond(components=[{app:"media", name:"render-card", props:{render_id:N}}])
//
// Lifecycle:
//   pending  → spinner + "queued"
//   running  → progress bar + percent
//   ok       → embedded MediaCard for the output_file_id
//   failed   → error message + retry hint
//   cancelled → muted "cancelled by operator"

import { useEffect, useState } from "react";
import { AppCardHeader, Card } from "@apteva/ui-kit";
import MediaCard from "./MediaCard";

interface RenderRow {
  id: number;
  operation: string;
  status: "pending" | "running" | "ok" | "failed" | "cancelled";
  progress_pct?: number;
  output_file_id?: string;
  output_name?: string;
  output_folder?: string;
  error?: string;
  source_file_ids?: string[];
}

interface Props {
  render_id: number;
  projectId?: string;
  preview?: boolean;
}

const previewSample: RenderRow = {
  id: 0,
  operation: "extract_frame",
  status: "running",
  progress_pct: 42,
};

export default function RenderCard({ render_id, projectId, preview }: Props) {
  const [row, setRow] = useState<RenderRow | null>(preview ? previewSample : null);
  const [missing, setMissing] = useState(false);

  // Render events drive refreshes; a slow poll remains as a safety
  // net for reconnect gaps and older hosts without the shared event
  // bridge.
  useEffect(() => {
    if (preview || !projectId) return;
    let alive = true;
    let terminal = false;

    const tick = () => {
      const url =
        `/api/apps/media/renders/${render_id}` +
        `?project_id=${encodeURIComponent(projectId)}`;
      fetch(url, { credentials: "same-origin" })
        .then((r) => {
          if (r.status === 404) {
            if (alive) setMissing(true);
            return null;
          }
          return r.json();
        })
        .then((data) => {
          if (!alive || !data) return;
          const r: RenderRow = (data.render as RenderRow) ?? (data as RenderRow);
          setRow(r);
          if (r.status === "ok" || r.status === "failed" || r.status === "cancelled") {
            terminal = true;
          }
        })
        .catch(() => undefined);
    };

    tick();
    const id = setInterval(() => alive && !terminal && tick(), 15000);

    const onEvent = (ev: { topic?: string; data?: { render_id?: string | number } }) => {
      if (!ev.topic?.startsWith("render.")) return;
      if (String(ev.data?.render_id ?? "") === String(render_id)) tick();
    };
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(app: string, projectId: string, fn: (ev: unknown) => void): () => void;
      };
    }).__aptevaAppEvents;
    let unsubscribe: (() => void) | undefined;
    let source: EventSource | undefined;
    if (bridge) {
      unsubscribe = bridge.subscribe("media", projectId, onEvent as (ev: unknown) => void);
    } else {
      source = new EventSource(`/api/app-events/media?project_id=${encodeURIComponent(projectId)}`, {
        withCredentials: true,
      });
      source.onmessage = (event) => {
        try { onEvent(JSON.parse(event.data)); } catch { /* ignore malformed events */ }
      };
    }
    return () => {
      alive = false;
      clearInterval(id);
      unsubscribe?.();
      source?.close();
    };
  }, [render_id, projectId, preview]);

  if (missing) {
    return (
      <Card>
        <AppCardHeader title={`Render #${render_id}`} status={{ label: "missing", variant: "muted" }} />
      </Card>
    );
  }
  if (!row) {
    return (
      <Card>
        <AppCardHeader title={`Render #${render_id}`} status={{ label: "loading", variant: "muted" }} />
      </Card>
    );
  }

  // Terminal-OK → promote to a MediaCard for the output file. The
  // user reads the chat from oldest-to-newest, so the card silently
  // upgrading from "running 70%" to "here's the clip" is exactly
  // what they want.
  if (row.status === "ok" && row.output_file_id) {
    return <MediaCard file_id={row.output_file_id} projectId={projectId} preview={preview} />;
  }

  const status = statusLabel(row.status);
  const pct = row.progress_pct ?? 0;

  return (
    <Card>
      <AppCardHeader
        title={prettyOp(row.operation)}
        subtitle={row.output_name || `render #${row.id}`}
        status={status}
      />
      <div className="px-4 py-4">
        {row.status === "running" && (
          <div>
            <div className="h-1.5 bg-bg-input rounded overflow-hidden">
              <div
                className="h-full bg-accent transition-all duration-200"
                style={{ width: `${Math.min(100, Math.max(0, pct))}%` }}
              />
            </div>
            <div className="text-xs text-text-muted mt-1.5 font-mono">
              {pct.toFixed(0)}%
            </div>
          </div>
        )}
        {row.status === "pending" && (
          <div className="text-xs text-text-muted">Queued — waiting for a render slot.</div>
        )}
        {row.status === "failed" && (
          <div className="text-xs text-red leading-relaxed">
            {row.error || "Render failed."}
          </div>
        )}
        {row.status === "cancelled" && (
          <div className="text-xs text-text-muted">Cancelled.</div>
        )}
      </div>
    </Card>
  );
}

function statusLabel(s: RenderRow["status"]): { label: string; variant: "muted" | "ok" | "warn" | "err" } {
  switch (s) {
    case "ok":
      return { label: "done", variant: "ok" };
    case "running":
      return { label: "running", variant: "muted" };
    case "pending":
      return { label: "queued", variant: "muted" };
    case "failed":
      return { label: "failed", variant: "err" };
    case "cancelled":
      return { label: "cancelled", variant: "muted" };
    default:
      return { label: s, variant: "muted" };
  }
}

function prettyOp(op: string): string {
  switch (op) {
    case "trim":
      return "Trim clip";
    case "resize":
      return "Resize";
    case "transcode":
      return "Transcode";
    case "concat":
      return "Concatenate";
    case "crop":
      return "Crop";
    case "extract_frame":
      return "Extract frame";
    case "audio_extract":
      return "Extract audio";
    default:
      return op;
  }
}
