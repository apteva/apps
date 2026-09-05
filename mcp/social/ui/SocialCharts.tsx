import { Area,AreaChart,ResponsiveContainer,Tooltip,XAxis,YAxis } from "recharts";
import {metricTrendEntries} from "./metricsPresentation";
export default function InsightCharts({ metrics }: { metrics: { insights?: Record<string, {time?: string; value: number}[]> } }) {
  const entries = metricTrendEntries(metrics.insights);
  if (entries.length === 0) {
    return <div className="py-6 text-center text-text-dim text-sm">No trend data yet.</div>;
  }
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
      {entries.map(({ name, points }) => {
        const latest = points[points.length - 1];
        return (
          <div key={name} className="border border-border rounded px-3 py-2 bg-bg">
            <div className="flex items-center justify-between gap-3">
              <span className="text-text text-sm">{name.replace(/_/g, " ")}</span>
              <span className="text-text font-medium">{formatNumber(latest.value)}</span>
            </div>
            <Sparkline points={points} gradientId={`socialMetricFill-${name.replace(/[^a-zA-Z0-9_-]/g, "-")}`} />
            <div className="text-text-dim text-xs mt-1">
              {points.length} point{points.length !== 1 ? "s" : ""}
              {latest.time ? ` · latest ${new Date(latest.time).toLocaleDateString()}` : ""}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function Sparkline({ points, gradientId }: { points: { time?: string; value: number }[]; gradientId: string }) {
  const data = points.map((point, index) => ({
    index,
    label: point.time ? new Date(point.time).toLocaleDateString() : String(index + 1),
    value: Number(point.value) || 0,
  }));
  if (data.length === 0) return null;
  return (
    <div className="mt-2 h-16 w-full" role="img" aria-hidden="true">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{ top: 4, right: 0, bottom: 0, left: 0 }}>
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="#f97316" stopOpacity={0.28} />
              <stop offset="100%" stopColor="#f97316" stopOpacity={0.02} />
            </linearGradient>
          </defs>
          <XAxis dataKey="index" hide />
          <YAxis hide domain={["dataMin", "dataMax"]} />
          <Tooltip
            cursor={{ stroke: "#3a3a3a", strokeWidth: 1 }}
            contentStyle={{
              background: "#111",
              border: "1px solid #333",
              borderRadius: 4,
              color: "#e5e5e5",
              fontSize: 12,
            }}
            labelFormatter={(_, payload) => payload?.[0]?.payload?.label || ""}
            formatter={(value) => [formatNumber(Number(value) || 0), "value"]}
          />
          <Area
            type="monotone"
            dataKey="value"
            stroke="#f97316"
            strokeWidth={2}
            fill={`url(#${gradientId})`}
            dot={false}
            activeDot={{ r: 3, stroke: "#f97316", strokeWidth: 1, fill: "#111" }}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}


function formatNumber(n: number) { return Intl.NumberFormat(undefined,{notation:"compact"}).format(n); }
