import { useMemo } from "react";
import {
  Empty,
  LoadingOrError,
  TrendChart,
  WidgetHostProps,
  displayLabel,
  settingString,
  useWidgetQuery,
  widgetConfig,
} from "./StandaloneWidget";

export default function AnalyticsTrendWidget(props: WidgetHostProps) {
  const config = useMemo(() => widgetConfig(props.widgetSettings, true), [props.widgetSettings]);
  const { data, loading, error } = useWidgetQuery(props, "timeseries", config);
  const title = settingString(props.widgetSettings, "title", "Trend");
  const rows = props.preview
    ? [
        { bucket: "7d ago", count: 42 },
        { bucket: "6d ago", count: 58 },
        { bucket: "5d ago", count: 51 },
        { bucket: "4d ago", count: 74 },
        { bucket: "3d ago", count: 68 },
        { bucket: "2d ago", count: 91 },
        { bucket: "today", count: 84 },
      ]
    : (data?.series ?? []);
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded border border-border bg-bg-card p-3">
      <header className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-xs font-bold uppercase tracking-wide text-text-dim">{title}</h2>
          <p className="truncate text-[10px] text-text-muted">{displayLabel(config)} · {config.window}</p>
        </div>
        <span className="rounded border border-success/25 bg-success/10 px-1.5 py-0.5 text-[8px] font-bold uppercase text-success">Live</span>
      </header>
      <LoadingOrError loading={loading} error={error} />
      {!loading && !error && <TrendChart rows={rows} config={config} gradientId={`analytics-trend-${props.widgetId || "preview"}`} />}
      {!loading && !error && !rows.length && <Empty>No values in this window.</Empty>}
    </section>
  );
}
