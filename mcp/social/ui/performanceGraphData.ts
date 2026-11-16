import type { MetricPoint, PerformanceSummary } from "./homeWidgetData";
import {
  sortPerformanceAccounts,
  type PerformanceMetric,
} from "./performanceFilters";

export const graphMetricLabels: Record<PerformanceMetric, string> = {
  followers: "Followers",
  views: "Views",
  impressions: "Impressions",
  interactions: "Interactions",
};
export const graphColors = [
  "#38bdf8",
  "#fb7185",
  "#34d399",
  "#a78bfa",
  "#fbbf24",
  "#f472b6",
  "#22d3ee",
  "#fb923c",
  "#a3e635",
  "#60a5fa",
  "#e879f9",
  "#2dd4bf",
  "#facc15",
  "#818cf8",
  "#fda4af",
  "#bef264",
  "#c084fc",
  "#fdba74",
  "#67e8f9",
  "#94a3b8",
];
export interface GraphSeries {
  id: string;
  name: string;
  color: string;
  points: MetricPoint[];
  expected: number;
  note?: string;
}

export function graphDates(data: PerformanceSummary) {
  if (
    !/^\d{4}-\d{2}-\d{2}$/.test(data.start_date || "") ||
    !/^\d{4}-\d{2}-\d{2}$/.test(data.end_date || "")
  )
    return [];
  const start = Date.parse(data.start_date),
    end = Date.parse(data.end_date);
  if (
    !Number.isFinite(start) ||
    !Number.isFinite(end) ||
    end < start ||
    end - start > 365 * 86400000
  )
    return [];
  const dates: string[] = [];
  for (let day = start; day <= end; day += 86400000)
    dates.push(new Date(day).toISOString().slice(0, 10));
  return dates;
}

export function performanceGraphSeries(
  data: PerformanceSummary,
  metric: PerformanceMetric,
  mode: string,
  limit: number,
) {
  const dates = graphDates(data);
  const align = (points: MetricPoint[] | undefined) => {
    const byDate = new Map((points || []).map((p) => [p.date, p]));
    return dates.map((date) => {
      const p = byDate.get(date);
      return p && p.value != null && Number.isFinite(p.value) && p.value >= 0
        ? p
        : { date, value: null, accounts: 0 };
    });
  };
  if (mode === "combined") {
    const expected = data.accounts.filter(
      (a) => !a.aggregate_exclusion_reason,
    ).length;
    return [
      {
        id: "combined",
        name: "Combined",
        color: graphColors[0],
        points: align(data.metrics[metric]?.trend),
        expected,
      },
    ] as GraphSeries[];
  }
  return sortPerformanceAccounts(data.accounts, metric, true)
    .slice(0, limit)
    .map((a) => ({
      id: String(a.id),
      name: a.name,
      color: graphColors[Math.abs(a.id - 1) % graphColors.length],
      points: align(a.metrics[metric]?.trend),
      expected: 1,
      note: a.aggregate_exclusion_reason,
    }));
}

export function graphPath(
  points: MetricPoint[],
  width: number,
  height: number,
  max: number,
) {
  let connected = false;
  return points
    .map((p, i) => {
      if (p.value == null) {
        connected = false;
        return "";
      }
      const x =
        points.length === 1 ? width / 2 : (i / (points.length - 1)) * width;
      const y = height - (p.value / max) * height;
      const command = connected ? "L" : "M";
      connected = true;
      return `${command}${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
}
