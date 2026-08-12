import { useEffect, useState } from "react";

interface Props { design_id: number; revision_id?: number; projectId?: string; preview?: boolean }

export default function DesignCard({ design_id, projectId = "", preview }: Props) {
  const [design, setDesign] = useState<any>(preview ? { name: "Parametric bracket", kind: "parametric", current_revision: { revision_number: 4 }, latest_build: { status: "passed", report: { bounds: { size: [80, 50, 24] }, triangle_count: 428 } } } : null);
  useEffect(() => {
    if (preview || !design_id) return;
    fetch(`/api/apps/design/api/designs/${design_id}?project_id=${encodeURIComponent(projectId)}`, { credentials: "same-origin" }).then((r) => r.json()).then((j) => setDesign(j.design)).catch(() => setDesign(null));
  }, [design_id, projectId, preview]);
  if (!design) return <div className="rounded-lg border border-border p-3 text-xs text-text-muted">Design unavailable</div>;
  const report = design.latest_build?.report;
  return <div className="rounded-xl border border-border bg-bg overflow-hidden min-w-[280px]">
    <div className="h-24 bg-gradient-to-br from-[#101b28] to-[#0b1016] grid place-items-center"><span className="text-5xl text-accent/60">◇</span></div>
    <div className="p-3"><div className="flex items-center gap-2"><strong className="text-sm">{design.name}</strong><span className="ml-auto text-[10px] text-text-muted">r{design.current_revision?.revision_number}</span></div>
      <div className="mt-2 flex gap-2 text-[10px] text-text-muted"><span className="rounded bg-bg-input px-1.5 py-1">{design.kind}</span>{report && <span className="rounded bg-bg-input px-1.5 py-1">{report.bounds.size.map((n:number)=>Math.round(n)).join(" × ")} mm</span>}<span className={design.latest_build?.status === "passed" ? "text-green ml-auto" : "text-text-muted"}>● {design.latest_build?.status || "not built"}</span></div>
    </div>
  </div>;
}
