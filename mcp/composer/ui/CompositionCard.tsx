import { useCallback, useEffect, useState } from "react";
import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

const vendor: CardVendor = {
  name: "Composer",
  logo: <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden><path d="M4 4h16v16H4zM8 4v16M16 4v16M4 9h4M16 9h4M4 15h4M16 15h4" /></svg>,
  color: { light: "#c2410c", dark: "#fb923c" },
};

interface LaneClip { uid: string; start: number; length: number; kind: string; ai_status?: string }
interface Lane { type: string; clips: LaneClip[] }
interface RenderSummary { id: number; status: string; phase?: string }
interface CompositionData {
  id: number; name: string; duration_seconds: number; track_count: number;
  output: { format?: string; aspect?: string; resolution?: string };
  counts: Record<string, number>; ai: Record<string, number>; lanes: Lane[];
  latest_render?: RenderSummary | null;
}
interface Props { composition_id: number; projectId?: string; preview?: boolean }

const sample: CompositionData = {
  id: 1, name: "Product launch overview", duration_seconds: 64, track_count: 3,
  output: { format: "mp4", aspect: "16:9", resolution: "hd" },
  counts: { visual: 4, audio: 3, text: 2, silence: 0, ai: 5 },
  ai: { total: 5, ready: 3, generating: 2, draft: 0, failed: 0 },
  lanes: [
    { type: "visual", clips: [{ uid: "v1", start: 0, length: 24, kind: "video", ai_status: "ready" }, { uid: "v2", start: 24, length: 40, kind: "image", ai_status: "generating" }] },
    { type: "audio", clips: [{ uid: "a1", start: 0, length: 20, kind: "audio", ai_status: "ready" }, { uid: "a2", start: 23, length: 36, kind: "audio", ai_status: "generating" }] },
    { type: "overlay", clips: [{ uid: "t1", start: 4, length: 8, kind: "text" }] },
  ], latest_render: { id: 8, status: "queued", phase: "generating_assets" },
};

function useEvents(projectId: string | undefined, onEvent: (event: any) => void) {
  useEffect(() => {
    if (!projectId) return;
    const bridge = (window as any).__aptevaAppEvents;
    if (bridge) return bridge.subscribe("composer", projectId, onEvent);
    const source = new EventSource(`/api/app-events/composer?project_id=${encodeURIComponent(projectId)}`, { withCredentials: true });
    source.onmessage = (event) => { try { onEvent(JSON.parse(event.data)); } catch { /* ignore */ } };
    return () => source.close();
  }, [projectId, onEvent]);
}

export default function CompositionCard({ composition_id, projectId, preview }: Props) {
  const [data, setData] = useState<CompositionData | null>(preview ? sample : null);
  const [missing, setMissing] = useState(false);
  const load = useCallback(() => {
    if (preview || !projectId) return;
    fetch(`/api/apps/composer/cards/composition/${composition_id}?project_id=${encodeURIComponent(projectId)}`, { credentials: "same-origin" })
      .then((response) => { if (response.status === 404) { setMissing(true); return null; } return response.json(); })
      .then((next) => { if (next) { setData(next); setMissing(false); } }).catch(() => undefined);
  }, [composition_id, projectId, preview]);
  useEffect(() => { load(); }, [load]);
  const onEvent = useCallback((event: any) => {
    if (Number(event?.data?.composition_id ?? event?.data?.id) !== composition_id) return;
    if (event.topic === "composition.deleted") setMissing(true); else load();
  }, [composition_id, load]);
  useEvents(preview ? undefined : projectId, onEvent);
  useEffect(() => {
    if (preview || !data?.latest_render || ["complete", "failed", "cancelled"].includes(data.latest_render.status)) return;
    const timer = setInterval(load, 15000); return () => clearInterval(timer);
  }, [preview, data?.latest_render?.status, load]);

  if (missing) return <Card><CardHeader vendor={vendor} title={`Composition #${composition_id}`} status={{ label: "deleted", variant: "muted" }} /></Card>;
  if (!data) return <Card><CardHeader vendor={vendor} title={`Composition #${composition_id}`} status={{ label: "loading", variant: "muted" }} /></Card>;
  const latest = data.latest_render;
  const status = latest ? { label: latest.status, variant: latest.status === "complete" ? "ok" as const : latest.status === "failed" ? "err" as const : "muted" as const } : undefined;
  return (
    <Card>
      <CardHeader vendor={vendor} title={data.name} subtitle={`composition #${data.id}`} status={status} />
      <Timeline lanes={data.lanes} duration={data.duration_seconds} />
      <div className="px-4 py-3 border-t border-border">
        <DataList items={[
          { label: "Output", value: [data.output.format?.toUpperCase(), data.output.aspect, data.output.resolution].filter(Boolean).join(" · ") },
          { label: "Timeline", value: `${formatTime(data.duration_seconds)} · ${data.track_count} tracks · ${Object.values(data.counts).slice(0, 4).reduce((a, b) => a + b, 0)} clips` },
          { label: "AI assets", value: data.ai.total ? `${data.ai.ready || 0}/${data.ai.total} ready${data.ai.generating ? ` · ${data.ai.generating} generating` : ""}` : "none" },
        ]} />
      </div>
    </Card>
  );
}

function Timeline({ lanes, duration }: { lanes: Lane[]; duration: number }) {
  const total = Math.max(1, duration);
  return <div className="px-4 py-3 bg-bg-input/40 space-y-1.5" aria-label="Composition timeline">
    {lanes.slice(0, 4).map((lane, laneIndex) => <div key={`${lane.type}-${laneIndex}`} className="flex items-center gap-2">
      <span className="w-12 shrink-0 text-[10px] uppercase text-text-dim truncate">{lane.type}</span>
      <div className="relative h-5 flex-1 bg-bg rounded border border-border overflow-hidden">
        {lane.clips.map((clip) => <span key={clip.uid} title={`${clip.uid} · ${clip.kind}`} className={`absolute top-0.5 bottom-0.5 border ${clipClass(clip)}`} style={{ left: `${Math.min(100, clip.start / total * 100)}%`, width: `${Math.max(1.5, Math.min(100, clip.length / total * 100))}%` }} />)}
      </div>
    </div>)}
  </div>;
}

function clipClass(clip: LaneClip): string {
  if (clip.kind === "silence") return "border-dashed border-text-dim/50 bg-transparent";
  if (clip.ai_status === "failed") return "border-red bg-red/20";
  if (clip.ai_status === "generating" || clip.ai_status === "draft") return "border-accent/60 bg-accent/15";
  if (clip.kind === "text") return "border-text-muted bg-border";
  if (clip.kind === "audio") return "border-text-dim bg-bg-input";
  return "border-accent/60 bg-accent/25";
}
function formatTime(seconds: number): string { const m = Math.floor(seconds / 60); const s = Math.round(seconds % 60); return `${m}:${String(s).padStart(2, "0")}`; }
