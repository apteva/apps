import { useCallback, useEffect, useState } from "react";
import { Card, CardHeader, DataList, type CardVendor } from "@apteva/ui-kit";

const vendor: CardVendor = {
  name: "Composer",
  logo: <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden><path d="M12 3v4m0 10v4M4.2 4.2 7 7m10 10 2.8 2.8M3 12h4m10 0h4M4.2 19.8 7 17m10-10 2.8-2.8" /></svg>,
  color: { light: "#c2410c", dark: "#fb923c" },
};
interface RenderData {
  render_id: number; composition_id: number; composition_name: string; executor: string;
  status: string; phase: string; progress_pct: number; progress?: { message?: string; ready?: number; total?: number };
  storage_id: number; duration_ms: number; cost_usd: number; error?: string; output_url?: string;
  output: { format?: string; aspect?: string; resolution?: string }; qa?: { warnings?: string[] };
}
interface Props { render_id: number; projectId?: string; preview?: boolean }
const sample: RenderData = { render_id: 1, composition_id: 4, composition_name: "Product launch overview", executor: "local", status: "rendering", phase: "rendering", progress_pct: 50, progress: { message: "Rendering composition" }, storage_id: 0, duration_ms: 0, cost_usd: 0, output: { format: "mp4", aspect: "16:9", resolution: "hd" } };
const phases = ["queued", "generating_assets", "preparing", "rendering", "quality_checks", "uploading", "complete"];

export default function RenderCard({ render_id, projectId, preview }: Props) {
  const [row, setRow] = useState<RenderData | null>(preview ? sample : null);
  const [missing, setMissing] = useState(false);
  const load = useCallback(() => {
    if (preview || !projectId) return;
    fetch(`/api/apps/composer/cards/render/${render_id}?project_id=${encodeURIComponent(projectId)}`, { credentials: "same-origin" })
      .then((response) => { if (response.status === 404) { setMissing(true); return null; } return response.json(); })
      .then((next) => { if (next) { setRow(next); setMissing(false); } }).catch(() => undefined);
  }, [render_id, projectId, preview]);
  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    if (preview || !projectId) return;
    const onEvent = (event: any) => { if (Number(event?.data?.render_id) === render_id) load(); };
    const bridge = (window as any).__aptevaAppEvents;
    if (bridge) return bridge.subscribe("composer", projectId, onEvent);
    const source = new EventSource(`/api/app-events/composer?project_id=${encodeURIComponent(projectId)}`, { withCredentials: true });
    source.onmessage = (event) => { try { onEvent(JSON.parse(event.data)); } catch { /* ignore */ } };
    return () => source.close();
  }, [render_id, projectId, preview, load]);
  useEffect(() => {
    if (preview || !row || terminal(row.status)) return;
    const timer = setInterval(load, 10000); return () => clearInterval(timer);
  }, [preview, row?.status, load]);

  if (missing) return <Card><CardHeader vendor={vendor} title={`Render #${render_id}`} status={{ label: "missing", variant: "muted" }} /></Card>;
  if (!row) return <Card><CardHeader vendor={vendor} title={`Render #${render_id}`} status={{ label: "loading", variant: "muted" }} /></Card>;
  const status = { label: row.status, variant: row.status === "complete" ? "ok" as const : row.status === "failed" ? "err" as const : "muted" as const };
  const audio = ["mp3", "wav", "m4a", "aac"].includes((row.output.format || "").toLowerCase());
  return <Card>
    <CardHeader vendor={vendor} title={row.composition_name} subtitle={`render #${row.render_id} · ${row.executor}`} status={status} action={row.output_url ? { label: "Open", href: row.output_url } : undefined} />
    {row.status === "complete" && row.output_url ? (
      audio ? <div className="px-4 py-4"><audio controls preload="metadata" src={row.output_url} className="w-full" /></div>
        : <video controls preload="metadata" playsInline src={row.output_url} className="w-full block bg-black object-contain" style={{ maxHeight: 280 }} />
    ) : <div className="px-4 py-4">
      <PhaseRail phase={row.phase} status={row.status} />
      {!terminal(row.status) && <><div className="h-1.5 bg-bg-input rounded overflow-hidden mt-3"><div className="h-full bg-accent transition-all duration-300" style={{ width: `${Math.max(2, Math.min(100, row.progress_pct || 0))}%` }} /></div><div className="mt-1.5 text-xs text-text-muted">{row.progress?.message || prettyPhase(row.phase)}</div></>}
      {row.status === "failed" && <div className="text-xs text-red leading-relaxed mt-2">{row.error || "Render failed."}</div>}
      {row.status === "cancelled" && <div className="text-xs text-text-muted mt-2">Cancelled.</div>}
    </div>}
    <div className="px-4 py-3 border-t border-border"><DataList items={[
      { label: "Output", value: [row.output.format?.toUpperCase(), row.output.aspect, row.output.resolution].filter(Boolean).join(" · ") },
      { label: "Stage", value: prettyPhase(row.phase) },
      ...(row.cost_usd > 0 ? [{ label: "Cost", value: `$${row.cost_usd.toFixed(3)}` }] : []),
    ]} /></div>
  </Card>;
}

function PhaseRail({ phase, status }: { phase: string; status: string }) {
  const index = status === "failed" || status === "cancelled" ? -1 : Math.max(0, phases.indexOf(phase));
  return <div className="flex items-center gap-1" aria-label={`Render phase: ${prettyPhase(phase)}`}>
    {phases.slice(0, -1).map((item, i) => <span key={item} className={`h-1 flex-1 rounded ${i <= index ? "bg-accent" : "bg-border"}`} title={prettyPhase(item)} />)}
  </div>;
}
function terminal(status: string): boolean { return ["complete", "failed", "cancelled"].includes(status); }
function prettyPhase(phase: string): string { return ({ generating_assets: "Generating assets", quality_checks: "Quality checks" } as Record<string, string>)[phase] || phase.replaceAll("_", " ").replace(/^./, c => c.toUpperCase()) || "Queued"; }
