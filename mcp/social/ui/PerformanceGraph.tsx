import { useEffect, useRef, useState, type PointerEvent } from "react";
import type { PerformanceSummary } from "./homeWidgetData";
import {
  performanceMetrics,
  type PerformanceMetric,
} from "./performanceFilters";
import {
  graphDates,
  graphMetricLabels,
  graphPath,
  performanceGraphSeries,
} from "./performanceGraphData";

const compact = (n: number) =>
  new Intl.NumberFormat(undefined, {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(n);
const dateLabel = (stamp: string) =>
  new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  }).format(new Date(stamp + "T00:00:00Z"));
const control =
  "min-h-7 rounded border border-border bg-bg-input px-2 py-1 text-[11px] text-text";

export default function PerformanceGraph({
  data,
  settings,
  full,
  mode,
  metric,
  onMode,
  onMetric,
}: {
  data: PerformanceSummary;
  settings?: Record<string, unknown>;
  full: boolean;
  mode: "combined" | "by_account";
  metric: PerformanceMetric;
  onMode: (mode: "combined" | "by_account") => void;
  onMetric: (metric: PerformanceMetric) => void;
}) {
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const [index, setIndex] = useState<number | null>(null);
  const [width, setWidth] = useState(600);
  const ref = useRef<HTMLDivElement>(null);
  const rawLimit = Number(settings?.graph_max_accounts ?? 20),
    limit = Number.isSafeInteger(rawLimit)
      ? Math.max(1, Math.min(100, rawLimit))
      : 20;
  const dates = graphDates(data),
    series = performanceGraphSeries(data, metric, mode, limit),
    visible = series.filter((s) => !hidden.has(s.id));
  const selected = Math.min(
    index ?? Math.max(0, dates.length - 1),
    Math.max(0, dates.length - 1),
  );
  const values = visible.flatMap((s) =>
    s.points.flatMap((p) => (p.value == null ? [] : [p.value])),
  );
  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    const resize = () => {
      const w = node.getBoundingClientRect().width;
      if (w > 0) setWidth(Math.max(w, 200));
    };
    resize();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(resize);
    observer.observe(node);
    return () => observer.disconnect();
  }, [Boolean(values.length), mode, metric]);
  const max = Math.max(1, ...values),
    height = full ? 190 : 160,
    left = 44,
    right = 12,
    top = 12,
    bottom = 28,
    pw = width - left - right,
    ph = height - top - bottom;
  const x = (i: number) =>
    left +
    (dates.length === 1 ? pw / 2 : (i / Math.max(1, dates.length - 1)) * pw);
  const change = () => {
    setHidden(new Set());
    setIndex(null);
  };
  const count =
    mode === "combined"
      ? `${data.accounts.filter((a) => !a.aggregate_exclusion_reason).length} matching accounts`
      : `${series.length < data.accounts.length ? `Top ${series.length} of ${data.accounts.length}` : series.length} accounts`;
  const selectDate = (e: PointerEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width > 0)
      setIndex(
        Math.max(
          0,
          Math.min(
            dates.length - 1,
            Math.round(
              ((((e.clientX - rect.left) * width) / rect.width - left) / pw) *
                Math.max(1, dates.length - 1),
            ),
          ),
        ),
      );
  };
  return (
    <section
      aria-label="Performance graph"
      className="mt-2 rounded border border-border p-2"
    >
      <div className="flex flex-wrap items-center gap-2">
        <select
          aria-label="Graph mode"
          className={control}
          value={mode}
          onChange={(e) => {
            onMode(e.target.value as "combined" | "by_account");
            change();
          }}
        >
          <option value="combined">Combined</option>
          <option value="by_account">By account</option>
        </select>
        <select
          aria-label="Graph metric"
          className={control}
          value={metric}
          onChange={(e) => {
            onMetric(e.target.value as PerformanceMetric);
            change();
          }}
        >
          {performanceMetrics.map((m) => (
            <option key={m} value={m}>
              {graphMetricLabels[m]}
            </option>
          ))}
        </select>
        <span className="text-[10px] text-text-dim">{count} · UTC</span>
      </div>
      <p className="mt-1 text-[10px] text-text-dim">
        {metric === "followers"
          ? "Observed follower balance at each day’s close"
          : "Daily activity"}{" "}
        · Gaps are unavailable data
        {mode === "combined"
          ? " · Totals use accounts with available metrics"
          : ""}
      </p>
      {!dates.length || !values.length ? (
        <p className="py-4 text-xs text-text-dim">
          {series.length && hidden.size === series.length
            ? "Select a legend entry to show its line."
            : "No history for this metric in the selected dates."}
        </p>
      ) : (
        <div
          ref={ref}
          tabIndex={0}
          role="group"
          aria-label={`${graphMetricLabels[metric]} history; use left and right arrows to choose a date`}
          className="mt-1 outline-offset-2"
          onKeyDown={(e) => {
            let next = selected;
            if (e.key === "ArrowLeft") next--;
            else if (e.key === "ArrowRight") next++;
            else if (e.key === "Home") next = 0;
            else if (e.key === "End") next = dates.length - 1;
            else return;
            e.preventDefault();
            setIndex(Math.max(0, Math.min(dates.length - 1, next)));
          }}
        >
          <svg
            width="100%"
            height={height}
            viewBox={`0 0 ${width} ${height}`}
            role="img"
            aria-label={`${graphMetricLabels[metric]} ${mode === "combined" ? "combined" : "by account"} history`}
            onPointerMove={selectDate}
            onPointerDown={selectDate}
          >
            {[0, 0.5, 1].map((f) => (
              <g key={f}>
                <line
                  x1={left}
                  x2={width - right}
                  y1={top + ph * (1 - f)}
                  y2={top + ph * (1 - f)}
                  stroke="currentColor"
                  className="text-border"
                />
                <text
                  x={left - 5}
                  y={top + ph * (1 - f) + 3}
                  textAnchor="end"
                  fill="currentColor"
                  className="text-text-dim"
                  fontSize={9}
                >
                  {compact(max * f)}
                </text>
              </g>
            ))}
            {[
              ...new Set([
                0,
                Math.floor((dates.length - 1) / 2),
                dates.length - 1,
              ]),
            ].map((i) => (
              <text
                key={i}
                x={x(i)}
                y={height - 7}
                textAnchor={
                  dates.length === 1
                    ? "middle"
                    : i === 0
                      ? "start"
                      : i === dates.length - 1
                        ? "end"
                        : "middle"
                }
                fill="currentColor"
                className="text-text-dim"
                fontSize={9}
              >
                {dateLabel(dates[i])}
              </text>
            ))}
            {visible.map((s) => (
              <g key={s.id} data-series-id={s.id}>
                <path
                  transform={`translate(${left},${top})`}
                  d={graphPath(s.points, pw, ph, max)}
                  stroke={s.color}
                  fill="none"
                  strokeWidth={1.8}
                  strokeLinejoin="round"
                  vectorEffect="non-scaling-stroke"
                />
                {s.points.map((p, i) =>
                  p.value != null &&
                  (i === selected ||
                    ((i === 0 || s.points[i - 1].value == null) &&
                      (i === s.points.length - 1 ||
                        s.points[i + 1].value == null))) ? (
                    <circle
                      key={i}
                      cx={x(i)}
                      cy={top + ph - (p.value / max) * ph}
                      r={i === selected ? 3 : 2}
                      fill={s.color}
                    />
                  ) : null,
                )}
              </g>
            ))}
            <line
              x1={x(selected)}
              x2={x(selected)}
              y1={top}
              y2={top + ph}
              stroke="currentColor"
              strokeDasharray="3 3"
              className="text-text-dim"
            />
          </svg>
        </div>
      )}
      {!!dates.length && (
        <div
          role="status"
          aria-live="polite"
          aria-label="Graph hover values"
          className="mt-1 max-h-32 overflow-auto rounded bg-bg-input px-2 py-1 text-[10px]"
        >
          <p className="font-medium text-text">{dates[selected]} · UTC</p>
          <div className="flex flex-wrap gap-x-3 gap-y-1">
            {visible.map((s) => {
              const p = s.points[selected];
              return (
                <p
                  key={s.id}
                  title={
                    p?.observed_at ? `Observed ${p.observed_at}` : undefined
                  }
                >
                  <span style={{ color: s.color }}>●</span> {s.name}:{" "}
                  <strong>
                    {p?.value == null
                      ? "Unavailable"
                      : p.value.toLocaleString()}
                  </strong>
                  {mode === "combined" &&
                    p != null &&
                    p.accounts < s.expected &&
                    ` · Partial ${p.accounts}/${s.expected} accounts`}
                </p>
              );
            })}
          </div>
        </div>
      )}
      <div
        aria-label="Graph legend"
        className="mt-2 flex max-h-28 flex-wrap gap-x-3 gap-y-1 overflow-auto text-[10px]"
      >
        {series.map((s) => (
          <button
            key={s.id}
            type="button"
            aria-pressed={!hidden.has(s.id)}
            title={`${hidden.has(s.id) ? "Show" : "Hide"} ${s.name}${s.note ? `. ${s.note}` : ""}`}
            className={hidden.has(s.id) ? "opacity-40" : "text-text-muted"}
            onClick={() =>
              setHidden((previous) => {
                const next = new Set(previous);
                if (next.has(s.id)) next.delete(s.id);
                else next.add(s.id);
                return next;
              })
            }
          >
            <span style={{ color: s.color }}>●</span> {s.name}
            {s.note ? " †" : ""}
          </button>
        ))}
      </div>
    </section>
  );
}
