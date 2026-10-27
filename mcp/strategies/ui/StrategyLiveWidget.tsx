import { useEffect, useMemo, useState } from "react";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};

type Position = {
  symbol: string;
  qty: number;
  avg_cost: number;
  mark?: number;
  market_value: number;
  unrealized_pnl: number;
  shared?: boolean;
};

export type Row = {
  strategy_id: number;
  strategy_name: string;
  strategy_status: string;
  portfolio_id: number;
  portfolio_name: string;
  mode: string;
  execution_environment: string;
  live_armed: boolean;
  assignment_status: string;
  cadence?: string;
  eligibility?: string;
  last_evaluated_at?: string;
  realized_pnl: number;
  unrealized_pnl: number;
  total_pnl: number;
  execution_cost: number;
  fees_paid: number;
  notional_traded: number;
  fill_count: number;
  last_fill_at?: string;
  runs_completed: number;
  runs_orders_submitted: number;
  runs_failed: number;
  last_run_at?: string;
  last_run_status?: string;
  last_run_error?: string;
  shared_symbols: number;
  positions?: Position[];
};

export type Data = {
  strategies: Row[];
  totals: {
    realized_pnl: number;
    unrealized_pnl: number;
    total_pnl: number;
    execution_cost: number;
  };
  count: number;
};

const money = (v: number) =>
  `${v < 0 ? "−" : ""}$${Math.abs(v).toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;

const relative = (value?: string) => {
  const time = Date.parse(value || "");
  if (!Number.isFinite(time)) return "—";
  const delta = Date.now() - time;
  if (delta < 60000) return "just now";
  if (delta < 3600000) return `${Math.floor(delta / 60000)}m ago`;
  if (delta < 86400000) return `${Math.floor(delta / 3600000)}h ago`;
  return `${Math.floor(delta / 86400000)}d ago`;
};

const setting = <T,>(settings: Record<string, unknown> | undefined, key: string, fallback: T): T => {
  const value = settings?.[key];
  return value === undefined || value === null ? fallback : (value as T);
};

// A strategy is live money when it routes to a broker. Paper is everything
// else, including a disarmed broker portfolio that cannot currently trade.
export const isLive = (row: Row) => row.execution_environment === "broker_live";

export function metricOf(row: Row, metric: string): { label: string; value: string; tone: number } {
  switch (metric) {
    case "unrealized_pnl":
      return { label: "Unrealized", value: money(row.unrealized_pnl), tone: row.unrealized_pnl };
    case "realized_pnl":
      return { label: "Realized", value: money(row.realized_pnl), tone: row.realized_pnl };
    case "execution_cost":
      return { label: "Cost", value: money(row.execution_cost), tone: -row.execution_cost };
    default:
      return { label: "Total", value: money(row.total_pnl), tone: row.total_pnl };
  }
}

const css = `
.sl-widget{color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-family:inherit;min-width:0;overflow:hidden}
.sl-widget *{box-sizing:border-box}.sl-widget h2,.sl-widget p{margin:0}.sl-widget button{cursor:pointer;font-family:inherit}.sl-widget :is(button,a):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}
.sl-widget header{padding:14px 16px;border-bottom:1px solid var(--color-border,#303030)}.sl-widget h2{font-size:14px;font-weight:700}.sl-widget header p{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}
.sl-widget .sl-totals{display:flex;gap:18px;padding:11px 16px;border-bottom:1px solid var(--color-border,#303030);flex-wrap:wrap}.sl-widget .sl-total{min-width:0}.sl-widget .sl-total span{display:block;font-size:10px;text-transform:uppercase;letter-spacing:.04em;color:var(--color-text-muted,#999)}.sl-widget .sl-total strong{display:block;font-size:14px;font-weight:700;margin-top:3px;font-variant-numeric:tabular-nums}
.sl-widget .sl-filters{display:flex;gap:5px;padding:8px 16px;border-bottom:1px solid var(--color-border,#303030);overflow-x:auto}.sl-widget .sl-filters button{background:transparent;color:var(--color-text-muted,#aaa);font-size:11px;font-weight:600;border:1px solid var(--color-border,#303030);border-radius:4px;padding:6px 9px;white-space:nowrap}.sl-widget .sl-filters button[aria-pressed=true]{color:var(--color-accent,#ff8c36);border-color:var(--color-accent,#ff8c36);background:color-mix(in srgb,var(--color-accent,#ff8c36) 10%,transparent)}
.sl-widget .sl-row{display:grid;grid-template-columns:minmax(0,1fr) minmax(120px,30%);gap:12px;align-items:center;width:100%;padding:12px 16px;background:transparent;color:inherit;border:0;border-bottom:1px solid var(--color-border,#303030);text-align:left}.sl-widget .sl-row:hover,.sl-widget .sl-row[aria-expanded=true]{background:var(--color-bg-hover,#202020)}
.sl-widget .sl-copy{min-width:0}.sl-widget .sl-title{display:flex;align-items:center;gap:7px;font-size:13px;line-height:20px;font-weight:600;min-width:0}.sl-widget .sl-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sl-widget .sl-summary{display:block;margin-top:5px;font-size:11px;line-height:18px;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sl-widget .sl-metric{text-align:right;min-width:0}.sl-widget .sl-metric strong{display:block;font-size:14px;font-weight:700;font-variant-numeric:tabular-nums;white-space:nowrap}.sl-widget .sl-metric span{display:block;font-size:10px;color:var(--color-text-muted,#999);margin-top:3px}
.sl-widget .gain{color:#43c878}.sl-widget .loss{color:#f08b8b}.sl-widget .flat{color:var(--color-text-muted,#aaa)}
.sl-widget .sl-badge{display:inline-block;font-size:9px;font-weight:700;text-transform:uppercase;letter-spacing:.05em;line-height:16px;padding:1px 6px;border-radius:3px;border:1px solid var(--color-border,#424242);color:var(--color-text-muted,#aaa);white-space:nowrap;flex:none}
.sl-widget .sl-badge.live{color:#fff;background:#c2371f;border-color:#c2371f}.sl-widget .sl-badge.armed{color:#fff;background:#8a1c0a;border-color:#8a1c0a}.sl-widget .sl-badge.paper{color:#8eabff;border-color:#8eabff60}.sl-widget .sl-badge.failed{color:#f08b8b;border-color:#f08b8b60}.sl-widget .sl-badge.unassigned{color:#e3b86d;border-color:#e3b86d60}
.sl-widget .sl-note{font-size:10px;color:#e3b86d;padding:0 16px 10px}
.sl-widget .sl-detail{padding:14px 16px;border-bottom:1px solid var(--color-border,#303030);font-size:12px}.sl-widget .sl-detail dl{display:grid;grid-template-columns:repeat(auto-fit,minmax(110px,1fr));gap:10px;margin:0 0 10px}.sl-widget .sl-detail dt{font-size:10px;text-transform:uppercase;letter-spacing:.04em;color:var(--color-text-muted,#999)}.sl-widget .sl-detail dd{margin:3px 0 0;font-size:12px;font-weight:600;font-variant-numeric:tabular-nums}
.sl-widget .sl-detail ul{list-style:none;padding:0;margin:0}.sl-widget .sl-detail li{display:flex;justify-content:space-between;gap:12px;padding:7px 0;border-top:1px solid var(--color-border,#303030);font-variant-numeric:tabular-nums}.sl-widget .sl-detail li small{color:var(--color-text-muted,#aaa);font-weight:400}
.sl-widget .sl-warning{font-size:11px;color:#e3b86d;padding:10px 16px;overflow-wrap:anywhere}.sl-widget .sl-empty{font-size:12px;color:var(--color-text-muted,#aaa);padding:24px 16px}
.sl-widget footer{display:flex;justify-content:space-between;gap:8px;padding:9px 16px;font-size:11px;color:var(--color-text-muted,#999)}
@media(max-width:480px){.sl-widget .sl-row{grid-template-columns:minmax(0,1fr) minmax(96px,38%);gap:8px;padding:12px}.sl-widget header,.sl-widget .sl-filters,.sl-widget .sl-totals{padding-left:12px;padding-right:12px}}
`;

export default function StrategyLiveWidget(props: Props) {
  return <StrategyLive key={`${props.projectId}:${props.installId}:${props.appName}`} {...props} />;
}

function StrategyLive(props: Props) {
  const defaultScope = setting(props.widgetSettings, "scope", "live") as string;
  const primaryMetric = setting(props.widgetSettings, "primary_metric", "total_pnl") as string;
  const rowLimit = setting(props.widgetSettings, "row_limit", 6) as number;

  const [view, setView] = useState(defaultScope === "live" ? "live" : "all");
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [selected, setSelected] = useState<string | null>(null);

  useEffect(() => {
    if (!props.projectId) return;
    let alive = true;
    const c = new AbortController();
    const q = new URLSearchParams({ project_id: props.projectId });
    if (props.installId) q.set("install_id", String(props.installId));
    fetch(`/api/apps/${encodeURIComponent(props.appName || "trading")}/strategies/live?${q}`, {
      credentials: "same-origin",
      signal: c.signal,
    })
      .then(async (r) => {
        if (!r.ok) throw new Error(`Strategies unavailable (${r.status})`);
        return r.json();
      })
      .then((d) => {
        if (alive) {
          setData(d);
          setError("");
        }
      })
      .catch((e) => {
        if (alive && e.name !== "AbortError") setError(e.message);
      });
    return () => {
      alive = false;
      c.abort();
    };
  }, [props.appName, props.projectId, props.installId, props.eventRevision, refresh]);

  const rows = useMemo(() => {
    const all = data?.strategies || [];
    if (view === "live") return all.filter(isLive);
    if (view === "paper") return all.filter((r) => !isLive(r));
    return all;
  }, [data, view]);

  const shown = rows.slice(0, rowLimit);
  const shared = shown.some((r) => r.shared_symbols > 0);
  const totals = useMemo(
    () =>
      rows.reduce(
        (acc, r) => ({
          realized: acc.realized + r.realized_pnl,
          unrealized: acc.unrealized + r.unrealized_pnl,
          total: acc.total + r.total_pnl,
        }),
        { realized: 0, unrealized: 0, total: 0 },
      ),
    [rows],
  );
  const tone = (v: number) => (Math.abs(v) < 0.005 ? "flat" : v > 0 ? "gain" : "loss");
  const detail = shown.find((r) => `${r.portfolio_id}:${r.strategy_id}` === selected);

  return (
    <section aria-label="Live strategies" className="sl-widget">
      <style>{css}</style>
      <header>
        <h2>Live strategies</h2>
        <p>Realized and open P&amp;L attributed to the strategy that traded it</p>
      </header>

      <div className="sl-totals">
        <div className="sl-total">
          <span>Total P&amp;L</span>
          <strong className={tone(totals.total)}>{money(totals.total)}</strong>
        </div>
        <div className="sl-total">
          <span>Realized</span>
          <strong className={tone(totals.realized)}>{money(totals.realized)}</strong>
        </div>
        <div className="sl-total">
          <span>Open</span>
          <strong className={tone(totals.unrealized)}>{money(totals.unrealized)}</strong>
        </div>
      </div>

      <nav className="sl-filters" aria-label="Filter strategies">
        {[
          ["live", "Live"],
          ["paper", "Paper"],
          ["all", "All"],
        ].map(([value, label]) => (
          <button
            key={value}
            type="button"
            aria-pressed={view === value}
            onClick={() => {
              setView(value);
              setSelected(null);
            }}
          >
            {label}
          </button>
        ))}
      </nav>

      {error && (
        <p role="alert" className="sl-warning">
          {error}
          {data ? " · Showing the last received snapshot." : ""}{" "}
          <button type="button" onClick={() => setRefresh((v) => v + 1)}>
            Retry
          </button>
        </p>
      )}

      {!props.projectId ? (
        <p className="sl-empty">Select a project to see its strategies.</p>
      ) : !data ? (
        <p role="status" className="sl-empty">
          {error ? "Strategies unavailable." : "Loading strategies…"}
        </p>
      ) : !shown.length ? (
        <p className="sl-empty">
          {view === "live"
            ? "No strategy is trading live. Switch to Paper to see simulated runs."
            : "No strategy has traded yet."}
        </p>
      ) : (
        <>
          {shown.map((row) => {
            const key = `${row.portfolio_id}:${row.strategy_id}`;
            const metric = metricOf(row, primaryMetric);
            const live = isLive(row);
            return (
              <button
                key={key}
                type="button"
                className="sl-row"
                aria-expanded={selected === key}
                onClick={() => setSelected(selected === key ? null : key)}
              >
                <span className="sl-copy">
                  <span className="sl-title">
                    <span className="sl-name">{row.strategy_name}</span>
                    {live ? (
                      <span className={`sl-badge ${row.live_armed ? "armed" : "live"}`}>
                        {row.live_armed ? "Live · armed" : "Live"}
                      </span>
                    ) : (
                      <span className="sl-badge paper">Paper</span>
                    )}
                    {row.runs_failed > 0 && <span className="sl-badge failed">{row.runs_failed} failed</span>}
                    {row.assignment_status !== "active" && (
                      <span className="sl-badge unassigned">{row.assignment_status}</span>
                    )}
                  </span>
                  <span className="sl-summary">
                    {[
                      row.portfolio_name,
                      `${row.fill_count} fill${row.fill_count === 1 ? "" : "s"}`,
                      row.last_run_at ? `ran ${relative(row.last_run_at)}` : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                <span className="sl-metric">
                  <strong className={tone(metric.tone)}>{metric.value}</strong>
                  <span>{metric.label}</span>
                </span>
              </button>
            );
          })}

          {detail && (
            <div className="sl-detail">
              <dl>
                <div>
                  <dt>Realized</dt>
                  <dd className={tone(detail.realized_pnl)}>{money(detail.realized_pnl)}</dd>
                </div>
                <div>
                  <dt>Open</dt>
                  <dd className={tone(detail.unrealized_pnl)}>{money(detail.unrealized_pnl)}</dd>
                </div>
                <div>
                  <dt>Costs</dt>
                  <dd>{money(detail.execution_cost)}</dd>
                </div>
                <div>
                  <dt>Traded</dt>
                  <dd>{money(detail.notional_traded)}</dd>
                </div>
                <div>
                  <dt>Runs</dt>
                  <dd>
                    {detail.runs_completed}✓ {detail.runs_failed}✗
                  </dd>
                </div>
                <div>
                  <dt>Cadence</dt>
                  <dd>{detail.cadence || "—"}</dd>
                </div>
              </dl>
              {!!detail.positions?.length && (
                <ul aria-label="Open positions">
                  {detail.positions.map((p) => (
                    <li key={p.symbol}>
                      <span>
                        {p.symbol} <small>{p.qty} @ {money(p.avg_cost)}</small>
                      </span>
                      <span className={tone(p.unrealized_pnl)}>{money(p.unrealized_pnl)}</span>
                    </li>
                  ))}
                </ul>
              )}
              {detail.last_run_error && <p className="sl-warning">{detail.last_run_error}</p>}
            </div>
          )}

          {shared && (
            <p className="sl-note">
              Some symbols are traded by more than one book. Those strategies close against their own
              lots, not the portfolio average.
            </p>
          )}

          <footer>
            <span>
              {shown.length} of {rows.length} shown
            </span>
          </footer>
        </>
      )}
    </section>
  );
}
