// AnalyticsPanel — read-only overview of the generic event store.
//
// Layout:
//   ┌─ header: title · window (24h/7d/30d/All) · refresh ──────────┐
//   │ stat tiles: total events · topics · apps                     │
//   │ events/day  — inline SVG bar chart over /series              │
//   │ top values  — horizontal bars over /top?by=props.X           │
//   │ topics      — table over /summary.topics                     │
//   └──────────────────────────────────────────────────────────────┘
//
// Charts are hand-rolled inline SVG (no chart library, intentionally
// modest) coloured via currentColor + a theme text token, matching the
// other Apteva panels. Backend: GET /api/apps/analytics/{summary,series,top}.

import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/analytics";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Summary {
  total: number;
  apps: number;
  topics: number;
  topics_list: TopicRow[];
}

interface TopicRow {
  app: string;
  topic: string;
  last_ts: number;
  count: number;
}

interface SeriesPoint {
  day: string;
  count: number;
}

interface TopRow {
  value: string | null;
  count: number;
}

const WINDOWS = [
  { key: "24h", label: "24h", ms: 24 * 3600e3 },
  { key: "7d", label: "7d", ms: 7 * 24 * 3600e3 },
  { key: "30d", label: "30d", ms: 30 * 24 * 3600e3 },
  { key: "all", label: "All", ms: 0 },
];

function fmt(n: number): string {
  return (n ?? 0).toLocaleString();
}

function relTime(ms: number): string {
  if (!ms) return "";
  const dt = Date.now() - ms;
  const s = Math.max(0, Math.floor(dt / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function ChartIcon() {
  return (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M3 3v18h18" />
      <path d="M7 14l4-4 3 3 5-6" />
    </svg>
  );
}

function Empty({ label }: { label: string }) {
  return (
    <div className="py-8 text-center text-text-muted text-sm">{label}</div>
  );
}

function Stat({ label, value }: { label: string; value: number }) {
  return (
    <div className="border border-border rounded px-4 py-3 flex-1">
      <div
        className="text-text font-semibold tabular-nums"
        style={{ fontSize: "24px", lineHeight: 1.1 }}
      >
        {fmt(value)}
      </div>
      <div className="text-text-dim text-xs uppercase mt-1">{label}</div>
    </div>
  );
}

function BarSeries({ data }: { data: SeriesPoint[] }) {
  if (!data.length) return <Empty label="No events in this window." />;
  const W = 600;
  const H = 120;
  const max = Math.max(...data.map((d) => d.count), 1);
  const bw = W / data.length;
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      className="w-full text-accent"
      style={{ height: "120px" }}
      preserveAspectRatio="none"
    >
      {data.map((d, i) => {
        const h = Math.max(1, (d.count / max) * (H - 2));
        return (
          <rect
            key={d.day}
            x={i * bw + bw * 0.12}
            y={H - h}
            width={bw * 0.76}
            height={h}
            fill="currentColor"
          >
            <title>{`${d.day}: ${fmt(d.count)}`}</title>
          </rect>
        );
      })}
    </svg>
  );
}

function TopBars({ rows }: { rows: TopRow[] }) {
  if (!rows.length) return <Empty label="No values for this key in this window." />;
  const max = Math.max(...rows.map((r) => r.count), 1);
  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((r) => (
        <div key={String(r.value)} className="flex items-center gap-2 text-xs">
          <div
            className="text-text-muted truncate"
            style={{ width: "140px" }}
            title={r.value ?? ""}
          >
            {r.value ?? "—"}
          </div>
          <div
            className="flex-1 bg-bg-input rounded overflow-hidden"
            style={{ height: "14px" }}
          >
            <div
              className="h-full bg-accent"
              style={{ width: `${(r.count / max) * 100}%` }}
            />
          </div>
          <div
            className="text-text-dim tabular-nums"
            style={{ width: "56px", textAlign: "right" }}
          >
            {fmt(r.count)}
          </div>
        </div>
      ))}
    </div>
  );
}

export default function AnalyticsPanel(_props: NativePanelProps) {
  const [windowKey, setWindowKey] = useState("7d");
  const [byKey, setByKey] = useState("props.platform");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [series, setSeries] = useState<SeriesPoint[]>([]);
  const [top, setTop] = useState<TopRow[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState("");

  const since = useMemo(() => {
    const w = WINDOWS.find((x) => x.key === windowKey);
    return w && w.ms ? Date.now() - w.ms : 0;
  }, [windowKey]);

  const qs = useMemo(() => (since ? `?since=${since}` : ""), [since]);

  const loadMain = useCallback(async () => {
    setLoading(true);
    setErr("");
    try {
      const [sumR, serR] = await Promise.all([
        fetch(`${API}/summary${qs}`, { credentials: "same-origin" }),
        fetch(`${API}/series${qs}`, { credentials: "same-origin" }),
      ]);
      if (sumR.ok) {
        const d = await sumR.json();
        setSummary({
          total: d.total ?? 0,
          apps: d.apps ?? 0,
          topics: d.topics ?? 0,
          topics_list: Array.isArray(d.topics_list) ? d.topics_list : [],
        });
      } else {
        setErr(`summary ${sumR.status}`);
      }
      if (serR.ok) setSeries((await serR.json()).series ?? []);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [qs]);

  const loadTop = useCallback(async () => {
    try {
      const sep = qs ? "&" : "?";
      const res = await fetch(`${API}/top${qs}${sep}by=${encodeURIComponent(byKey)}`, {
        credentials: "same-origin",
      });
      if (res.ok) setTop((await res.json()).top ?? []);
    } catch {
      /* top is best-effort; main load surfaces errors */
    }
  }, [qs, byKey]);

  useEffect(() => {
    loadMain();
  }, [loadMain]);

  useEffect(() => {
    const t = setTimeout(loadTop, 250);
    return () => clearTimeout(t);
  }, [loadTop]);

  const refresh = () => {
    loadMain();
    loadTop();
  };

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 border-b border-border px-4 py-2">
        <div className="flex items-center gap-2 text-text font-medium">
          <span className="text-accent">
            <ChartIcon />
          </span>
          Analytics
        </div>
        <div className="flex items-center gap-1 ml-2">
          {WINDOWS.map((w) => (
            <button
              key={w.key}
              onClick={() => setWindowKey(w.key)}
              className={
                "px-2 py-1 text-xs rounded border " +
                (windowKey === w.key
                  ? "border-accent text-accent"
                  : "border-border text-text-muted hover:text-text")
              }
            >
              {w.label}
            </button>
          ))}
        </div>
        <button
          onClick={refresh}
          disabled={loading}
          className="px-3 py-1 text-sm border border-border rounded text-text-muted hover:text-text disabled:opacity-50"
        >
          Refresh
        </button>
        <span className="ml-auto text-text-dim text-xs">
          {err ? <span className="text-error">{err}</span> : loading ? "loading…" : ""}
        </span>
      </header>

      <div className="flex-1 overflow-auto p-4 flex flex-col gap-4">
        <div className="flex gap-3">
          <Stat label="Events" value={summary?.total ?? 0} />
          <Stat label="Topics" value={summary?.topics ?? 0} />
          <Stat label="Apps" value={summary?.apps ?? 0} />
        </div>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Events per day</div>
          <BarSeries data={series} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <div className="text-text-dim text-xs uppercase">Top values</div>
            <input
              type="text"
              value={byKey}
              onChange={(e) => setByKey(e.target.value)}
              placeholder="props.platform"
              spellCheck={false}
              className="ml-auto bg-bg-input border border-border rounded px-2 py-1 text-xs"
              style={{ width: "200px" }}
            />
          </div>
          <TopBars rows={top} />
        </section>

        <section className="border border-border rounded p-4 flex flex-col gap-2">
          <div className="text-text-dim text-xs uppercase">Topics</div>
          {summary && summary.topics_list.length ? (
            <table className="w-full text-sm">
              <thead>
                <tr className="text-text-dim text-xs uppercase">
                  <th className="text-left font-normal py-1">App</th>
                  <th className="text-left font-normal py-1">Topic</th>
                  <th className="text-right font-normal py-1">Count</th>
                  <th className="text-right font-normal py-1">Last seen</th>
                </tr>
              </thead>
              <tbody>
                {summary.topics_list.map((t) => (
                  <tr key={`${t.app}/${t.topic}`} className="border-t border-border">
                    <td className="py-1.5 text-text-muted">{t.app}</td>
                    <td className="py-1.5 text-text">{t.topic}</td>
                    <td className="py-1.5 text-right tabular-nums text-text">{fmt(t.count)}</td>
                    <td className="py-1.5 text-right text-text-dim">{relTime(t.last_ts)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <Empty label="No events recorded yet. Apps write via the analytics_track tool." />
          )}
        </section>
      </div>
    </div>
  );
}
