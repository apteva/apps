import { useState } from "react";
import { areaChartEndMarkerPath, areaChartGeometry, formatMetric } from "./dashboard-ui";
import type { WidgetConfig } from "./standalone-config";

export default function TrendChart({ rows, config, gradientId }: {
  rows: Array<Record<string, any>>; config: WidgetConfig; gradientId: string;
}) {
  const [active, setActive] = useState<number | null>(null);
  const values = rows.map(row => {
    const raw = Object.prototype.hasOwnProperty.call(row, "value") ? row.value : row.count;
    const value = raw == null ? null : Number(raw);
    return value != null && Number.isFinite(value) ? value : null;
  });
  const timestamps = rows.map((row, i) => Number(row.ts) || Date.parse(String(row.bucket)) || i);
  const { points, linePath, areaPath, baseline } = areaChartGeometry(values, 300, 92, timestamps);
  const valid = values.filter((value): value is number => value != null);
  const min = Math.min(0, ...valid), max = Math.max(1, ...valid);
  const first = timestamps[0], last = timestamps.at(-1)!;
  const positions = timestamps.map(time => last > first ? 8 + (time - first) / (last - first) * 284 : 150);
  const date = (index: number, full = false) => {
    const row = rows[index];
    const ts = Number(row.ts) || Date.parse(String(row.bucket));
    return Number.isFinite(ts) ? new Date(ts).toLocaleDateString(undefined, {
      month: "short", day: "numeric", ...(full ? { year: "numeric" } : {}),
      ...(config.interval === "hour" ? { hour: "numeric", minute: "2-digit" } : {}),
    }) : String(row.bucket);
  };
  const index = active != null && active < rows.length ? active : null;
  const x = index == null ? 0 : positions[index] / 3;
  const point = points.find(point => point.index === index);
  const tooltipID = `${gradientId}-tooltip`;

  return <div className="mt-3">
    <div className="flex gap-2">
      <div className="relative shrink-0 text-right text-[10px] tabular-nums text-text-dim" style={{ width: 48, height: 176 }} aria-hidden="true">
        {[0, 1, 2, 3].map(step => <span key={step} className="absolute right-0" style={{ top: `${(8 + step * 26) / 92 * 100}%`, transform: "translateY(-50%)" }}>
          {formatMetric(max - (max - min) * step / 3, { ...config, compact: true, decimals: 0 })}
        </span>)}
      </div>
      <div className="relative min-w-0 flex-1" style={{ height: 176 }}>
        {/* CSS lines stay one physical CSS pixel thick at every widget width. */}
        {[0, 1, 2, 3].map(step => <div key={step} className="pointer-events-none absolute left-0 right-0 bg-border" style={{ top: `${(8 + step * 26) / 92 * 100}%`, height: 1, opacity: 0.6 }} />)}
        <svg viewBox="0 0 300 92" className="absolute inset-0 h-full w-full text-accent outline-none focus-visible:ring-1 focus-visible:ring-accent" preserveAspectRatio="none"
          role="group" tabIndex={0} aria-label="Analytics trend. Hover to see values, or use left and right arrow keys." aria-describedby={index == null ? undefined : tooltipID}
          onPointerMove={event => {
            const bounds = event.currentTarget.getBoundingClientRect();
            const position = (event.clientX - bounds.left) / bounds.width * 300;
            let nearest = 0;
            positions.forEach((x, i) => { if (Math.abs(x - position) < Math.abs(positions[nearest] - position)) nearest = i; });
            setActive(nearest);
          }}
          onPointerLeave={() => setActive(null)} onFocus={() => setActive(rows.length - 1)} onBlur={() => setActive(null)}
          onKeyDown={event => {
            if (!["ArrowLeft", "ArrowRight", "Home", "End", "Escape"].includes(event.key)) return;
            event.preventDefault();
            setActive(current => event.key === "Escape" ? null : event.key === "Home" ? 0 : event.key === "End" ? rows.length - 1 :
              Math.max(0, Math.min(rows.length - 1, (current ?? rows.length - 1) + (event.key === "ArrowLeft" ? -1 : 1))));
          }}>
          <defs><linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="currentColor" stopOpacity="0.18" /><stop offset="100%" stopColor="currentColor" stopOpacity="0.02" /></linearGradient></defs>
          <path d={areaPath} fill={`url(#${gradientId})`} />
          <path d={linePath} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" vectorEffect="non-scaling-stroke" />
          {points.length > 0 && <path d={areaChartEndMarkerPath(points, baseline)} fill="none" stroke="currentColor" strokeWidth="5" strokeLinecap="round" vectorEffect="non-scaling-stroke" />}
        </svg>
        {index != null && <>
          <div className="pointer-events-none absolute bg-accent" style={{ left: `${x}%`, top: "8.7%", bottom: "6.5%", width: 1, opacity: 0.45 }} />
          {point && <div className="pointer-events-none absolute rounded-full border-2 border-bg-card bg-accent" style={{ left: `${x}%`, top: `${point.y / 92 * 100}%`, width: 10, height: 10, transform: "translate(-50%, -50%)" }} />}
          <div id={tooltipID} role="tooltip" className="pointer-events-none absolute z-10 rounded border border-border bg-bg-card px-3 py-2 text-xs shadow-lg" style={{
            top: 0, left: `${x}%`, maxWidth: "100%", transform: x < 20 ? "translateX(0)" : x > 80 ? "translateX(-100%)" : "translateX(-50%)",
          }}>
            <div className="whitespace-nowrap text-[10px] text-text-muted">{date(index, true)}</div>
            <div className="font-bold tabular-nums text-text">{values[index] == null ? "No observation" : formatMetric(values[index], config)}</div>
          </div>
        </>}
      </div>
    </div>
    <div className="mt-1 flex justify-between text-[10px] text-text-dim" style={{ marginLeft: 56 }}><span>{date(0)}</span><span>{date(rows.length - 1)}</span></div>
    <div className="mt-3 flex items-center justify-between gap-2 text-xs text-text-muted"><span>Latest: <span className="tabular-nums text-text">{formatMetric(values.at(-1) ?? null, config)}</span></span><span className="text-[10px] text-text-dim">Hover for details</span></div>
  </div>;
}
