import { useEffect, useMemo, useState } from "react";

type Props = {
  appName?: string;
  projectId?: string;
  installId?: number;
  eventRevision?: number;
  widgetSize?: "half" | "full";
  widgetSettings?: Record<string, unknown>;
};

export type Portfolio = {
  id: number;
  name: string;
  status: string;
  mode: string;
  execution_environment: string;
  live_armed: boolean;
  broker_slug?: string;
  equity: number;
  cash: number;
  buying_power: number;
  day_pnl: number;
  day_pnl_pct: number;
  open_pnl: number;
  open_pnl_pct: number;
  realized_pnl: number;
  total_pnl: number;
  total_pnl_pct: number;
};

const money = (v: number) =>
  `${v < 0 ? "−" : ""}$${Math.abs(v).toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })}`;

const percent = (v: number) => `${v > 0 ? "+" : v < 0 ? "−" : ""}${Math.abs(v).toFixed(2)}%`;

const setting = <T,>(settings: Record<string, unknown> | undefined, key: string, fallback: T): T => {
  const value = settings?.[key];
  return value === undefined || value === null ? fallback : (value as T);
};

// Live money is a broker-routed portfolio. Armed means it can place real
// orders right now — the widget says so loudly because the whole point of a
// glanceable list is that nobody reads the small print before acting.
export const isLive = (p: Portfolio) => p.execution_environment === "broker_live";

export const basisLabelOf = (basis: string) =>
  basis === "open" ? "Open" : basis === "total" ? "Total" : "Day";

export function pnlOf(p: Portfolio, basis: string): { value: number; pct: number; label: string } {
  const label = basisLabelOf(basis);
  if (basis === "open") return { value: p.open_pnl, pct: p.open_pnl_pct, label };
  if (basis === "total") return { value: p.total_pnl, pct: p.total_pnl_pct, label };
  return { value: p.day_pnl, pct: p.day_pnl_pct, label };
}

export function sortPortfolios(rows: Portfolio[], sortBy: string, basis: string): Portfolio[] {
  const sorted = [...rows];
  sorted.sort((a, b) => {
    // Live portfolios stay on top whatever the sort — they are the ones that
    // can lose real money while nobody is looking.
    if (isLive(a) !== isLive(b)) return isLive(a) ? -1 : 1;
    switch (sortBy) {
      case "name":
        return a.name.localeCompare(b.name);
      case "day_pnl_pct":
        return b.day_pnl_pct - a.day_pnl_pct;
      case "total_pnl_pct":
        return b.total_pnl_pct - a.total_pnl_pct;
      default:
        return b.equity - a.equity;
    }
  });
  return sorted;
}

const css = `
.pw-widget{color:var(--color-text,#eceef2);background:var(--color-bg-card,#141414);border:1px solid var(--color-border,#303030);border-radius:4px;font-family:inherit;min-width:0;overflow:hidden}
.pw-widget *{box-sizing:border-box}.pw-widget h2,.pw-widget p{margin:0}.pw-widget button{cursor:pointer;font-family:inherit}.pw-widget :is(button,a):focus-visible{outline:2px solid var(--color-accent,#ff8c36);outline-offset:-2px}
.pw-widget header{padding:14px 16px;border-bottom:1px solid var(--color-border,#303030)}.pw-widget h2{font-size:14px;font-weight:700}.pw-widget header p{font-size:11px;color:var(--color-text-muted,#aaa);margin-top:4px}
.pw-widget .pw-totals{display:flex;gap:18px;padding:11px 16px;border-bottom:1px solid var(--color-border,#303030);flex-wrap:wrap}.pw-widget .pw-total span{display:block;font-size:10px;text-transform:uppercase;letter-spacing:.04em;color:var(--color-text-muted,#999)}.pw-widget .pw-total strong{display:block;font-size:14px;font-weight:700;margin-top:3px;font-variant-numeric:tabular-nums}
.pw-widget .pw-row{display:grid;grid-template-columns:minmax(0,1fr) minmax(110px,32%);gap:12px;align-items:center;width:100%;padding:12px 16px;border-bottom:1px solid var(--color-border,#303030);text-align:left;background:transparent;color:inherit;border-left:0;border-right:0;border-top:0}
.pw-widget .pw-row.live{background:color-mix(in srgb,#c2371f 7%,transparent)}
.pw-widget .pw-copy{min-width:0}.pw-widget .pw-title{display:flex;align-items:center;gap:7px;font-size:13px;line-height:20px;font-weight:600;min-width:0}.pw-widget .pw-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pw-widget .pw-summary{display:block;margin-top:5px;font-size:11px;line-height:18px;color:var(--color-text-muted,#aaa);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pw-widget .pw-metric{text-align:right;min-width:0}.pw-widget .pw-metric strong{display:block;font-size:14px;font-weight:700;font-variant-numeric:tabular-nums;white-space:nowrap}.pw-widget .pw-metric span{display:block;font-size:10px;color:var(--color-text-muted,#999);margin-top:3px;font-variant-numeric:tabular-nums}
.pw-widget .gain{color:#43c878}.pw-widget .loss{color:#f08b8b}.pw-widget .flat{color:var(--color-text-muted,#aaa)}
.pw-widget .pw-badge{display:inline-block;font-size:9px;font-weight:700;text-transform:uppercase;letter-spacing:.05em;line-height:16px;padding:1px 6px;border-radius:3px;border:1px solid var(--color-border,#424242);color:var(--color-text-muted,#aaa);white-space:nowrap;flex:none}
.pw-widget .pw-badge.live{color:#fff;background:#c2371f;border-color:#c2371f}.pw-widget .pw-badge.armed{color:#fff;background:#8a1c0a;border-color:#8a1c0a}.pw-widget .pw-badge.paper{color:#8eabff;border-color:#8eabff60}.pw-widget .pw-badge.halted{color:#e3b86d;border-color:#e3b86d60}
.pw-widget .pw-warning{font-size:11px;color:#e3b86d;padding:10px 16px;overflow-wrap:anywhere}.pw-widget .pw-empty{font-size:12px;color:var(--color-text-muted,#aaa);padding:24px 16px}
.pw-widget footer{display:flex;justify-content:space-between;gap:8px;padding:9px 16px;font-size:11px;color:var(--color-text-muted,#999)}
@media(max-width:480px){.pw-widget .pw-row{grid-template-columns:minmax(0,1fr) minmax(92px,40%);gap:8px;padding:12px}.pw-widget header,.pw-widget .pw-totals{padding-left:12px;padding-right:12px}}
`;

export default function PortfolioWatchWidget(props: Props) {
  return <PortfolioWatch key={`${props.projectId}:${props.installId}:${props.appName}`} {...props} />;
}

function PortfolioWatch(props: Props) {
  const showPaper = setting(props.widgetSettings, "show_paper", true) as boolean;
  const showLive = setting(props.widgetSettings, "show_live", true) as boolean;
  const basis = setting(props.widgetSettings, "pnl_basis", "day") as string;
  const sortBy = setting(props.widgetSettings, "sort_by", "equity") as string;
  const rowLimit = setting(props.widgetSettings, "row_limit", 5) as number;

  const [rows, setRows] = useState<Portfolio[] | null>(null);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);

  useEffect(() => {
    if (!props.projectId) return;
    let alive = true;
    const c = new AbortController();
    const q = new URLSearchParams({ project_id: props.projectId });
    if (props.installId) q.set("install_id", String(props.installId));
    fetch(`/api/apps/${encodeURIComponent(props.appName || "trading")}/portfolios?${q}`, {
      credentials: "same-origin",
      signal: c.signal,
    })
      .then(async (r) => {
        if (!r.ok) throw new Error(`Portfolios unavailable (${r.status})`);
        return r.json();
      })
      .then((d) => {
        if (alive) {
          setRows(Array.isArray(d?.portfolios) ? d.portfolios : []);
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

  const visible = useMemo(() => {
    const all = (rows || []).filter((p) => (isLive(p) ? showLive : showPaper));
    return sortPortfolios(all, sortBy, basis);
  }, [rows, showLive, showPaper, sortBy, basis]);

  const shown = visible.slice(0, rowLimit);
  const totals = useMemo(
    () =>
      visible.reduce(
        (acc, p) => {
          const pnl = pnlOf(p, basis);
          return { equity: acc.equity + p.equity, pnl: acc.pnl + pnl.value };
        },
        { equity: 0, pnl: 0 },
      ),
    [visible, basis],
  );
  const liveCount = visible.filter((p) => isLive(p) && p.live_armed).length;
  const tone = (v: number) => (Math.abs(v) < 0.005 ? "flat" : v > 0 ? "gain" : "loss");
  const basisLabel = basisLabelOf(basis);

  return (
    <section aria-label="Portfolios" className="pw-widget">
      <style>{css}</style>
      <header>
        <h2>Portfolios</h2>
        <p>Equity and {basisLabel.toLowerCase()} P&amp;L across paper and live desks</p>
      </header>

      <div className="pw-totals">
        <div className="pw-total">
          <span>Equity</span>
          <strong>{money(totals.equity)}</strong>
        </div>
        <div className="pw-total">
          <span>{basisLabel} P&amp;L</span>
          <strong className={tone(totals.pnl)}>{money(totals.pnl)}</strong>
        </div>
        {liveCount > 0 && (
          <div className="pw-total">
            <span>Armed live</span>
            <strong className="loss">{liveCount}</strong>
          </div>
        )}
      </div>

      {error && (
        <p role="alert" className="pw-warning">
          {error}
          {rows ? " · Showing the last received snapshot." : ""}{" "}
          <button type="button" onClick={() => setRefresh((v) => v + 1)}>
            Retry
          </button>
        </p>
      )}

      {!props.projectId ? (
        <p className="pw-empty">Select a project to see its portfolios.</p>
      ) : !rows ? (
        <p role="status" className="pw-empty">
          {error ? "Portfolios unavailable." : "Loading portfolios…"}
        </p>
      ) : !shown.length ? (
        <p className="pw-empty">No portfolios match the current filters.</p>
      ) : (
        <>
          {shown.map((p) => {
            const pnl = pnlOf(p, basis);
            const live = isLive(p);
            return (
              <div key={p.id} className={`pw-row${live ? " live" : ""}`}>
                <span className="pw-copy">
                  <span className="pw-title">
                    <span className="pw-name">{p.name}</span>
                    {live ? (
                      <span className={`pw-badge ${p.live_armed ? "armed" : "live"}`}>
                        {p.live_armed ? "Live · armed" : "Live · disarmed"}
                      </span>
                    ) : (
                      <span className="pw-badge paper">Paper</span>
                    )}
                    {p.status !== "active" && <span className="pw-badge halted">{p.status}</span>}
                  </span>
                  <span className="pw-summary">
                    {[
                      `${money(p.cash)} cash`,
                      live && p.broker_slug ? p.broker_slug.replace("-trading", "") : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
                <span className="pw-metric">
                  <strong>{money(p.equity)}</strong>
                  <span className={tone(pnl.value)}>
                    {money(pnl.value)} · {percent(pnl.pct)}
                  </span>
                </span>
              </div>
            );
          })}
          <footer>
            <span>
              {shown.length} of {visible.length} shown
            </span>
            <span>{basisLabel} basis</span>
          </footer>
        </>
      )}
    </section>
  );
}
