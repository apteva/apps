import { useMemo } from "react";
import {
  Empty,
  LoadingOrError,
  WidgetHostProps,
  displayLabel,
  settingString,
  useWidgetQuery,
  widgetConfig,
} from "./StandaloneWidget";

function rankingType(aggregation: string): "top" | "table" {
  return aggregation === "count" ? "top" : "table";
}

export default function AnalyticsRankingWidget(props: WidgetHostProps) {
  const config = useMemo(() => widgetConfig(props.widgetSettings), [props.widgetSettings]);
  const type = rankingType(String(config.aggregation || "count"));
  const { data, loading, error } = useWidgetQuery(props, type, config);
  const title = settingString(props.widgetSettings, "title", "Ranking");
  const rows = props.preview
    ? [{ value: "Example A", count: 120 }, { value: "Example B", count: 84 }, { value: "Example C", count: 63 }]
    : type === "top" ? (data?.top ?? []) : (data?.rows ?? []);
  const max = Math.max(1, ...rows.map((row: any) => Number(row.value ?? row.count ?? 0)));
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card p-3">
      <header className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-xs font-bold uppercase tracking-wide text-text-dim">{title}</h2>
          <p className="truncate text-[10px] text-text-muted">{displayLabel(config)} · {config.by || "group"} · {config.window}</p>
        </div>
        <span className="rounded border border-success/25 bg-success/10 px-1.5 py-0.5 text-[8px] font-bold uppercase text-success">Live</span>
      </header>
      <LoadingOrError loading={loading} error={error} />
      {!loading && !error && (
        <div className="mt-3 space-y-2">
          {rows.slice(0, Number(config.limit ?? 10)).map((row: any) => {
            const value = Number(row.value ?? row.count ?? 0);
            return (
              <div key={String(row.value ?? row.group)} className="space-y-1">
                <div className="flex items-center gap-2 text-[10px]">
                  <span className="min-w-0 flex-1 truncate text-text-muted">{String(row.value ?? row.group ?? "—")}</span>
                  <span className="tabular-nums text-text">{value.toLocaleString()}</span>
                </div>
                <div className="h-1 overflow-hidden rounded bg-bg-input"><div className="h-full rounded bg-accent" style={{ width: `${Math.max(2, (value / max) * 100)}%` }} /></div>
              </div>
            );
          })}
          {!rows.length && <Empty>No values in this window.</Empty>}
        </div>
      )}
    </section>
  );
}
