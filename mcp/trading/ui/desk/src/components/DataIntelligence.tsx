import type { CorporateAction, ExchangeSession, ReferenceIssue, ReferenceStatus } from "../api/reference.ts";

export function DataIntelligence({ status, actions, issues, sessions, symbol }: {
  status: ReferenceStatus | null; actions: CorporateAction[]; issues: ReferenceIssue[]; sessions: ExchangeSession[]; symbol: string;
}) {
  return (
    <div className="h-full overflow-auto p-3 grid gap-3 lg:grid-cols-[minmax(0,1.6fr)_minmax(280px,.8fr)]">
      <div className="space-y-3 min-w-0">
        <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
          <Metric label="Securities" value={status?.securities ?? 0} />
          <Metric label="Corporate actions" value={status?.corporate_actions ?? 0} />
          <Metric label="Sessions" value={status?.sessions ?? 0} />
          <Metric label="Open issues" value={status?.open_issues ?? 0} warn={(status?.open_issues ?? 0) > 0} />
        </div>
        <section className="glass-inset rounded-xl overflow-hidden">
          <div className="px-3 py-2 border-b border-[var(--border)] flex items-center justify-between">
            <span className="text-xs font-semibold">Corporate-action tape</span>
            <span className="text-[10px] mono t-tertiary">{symbol || "all securities"} · latest revisions</span>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-[11px]">
              <thead className="t-tertiary"><tr>{["Date","Security","Action","Terms","State"].map(h => <th key={h} className="text-left font-medium px-3 py-2">{h}</th>)}</tr></thead>
              <tbody>{actions.length === 0 ? <tr><td colSpan={5} className="px-3 py-8 text-center t-tertiary">No actions loaded for this selection.</td></tr> : actions.map(a => (
                <tr key={`${a.provider}:${a.provider_event_id}:${a.revision}`} className="border-t border-[var(--border)]">
                  <td className="px-3 py-2 mono whitespace-nowrap">{actionDate(a) || "—"}</td>
                  <td className="px-3 py-2 font-semibold">{a.symbol || "—"}{a.new_symbol ? <span className="t-tertiary"> → {a.new_symbol}</span> : null}</td>
                  <td className="px-3 py-2">{a.action_type.replaceAll("_", " ")}</td>
                  <td className="px-3 py-2 mono">{terms(a)}</td>
                  <td className="px-3 py-2"><span className={a.data_quality === "complete" ? "t-up" : "t-down"}>{a.data_quality}</span> <span className="t-tertiary">r{a.revision}</span></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </section>
      </div>
      <div className="space-y-3">
        <section className="glass-inset rounded-xl p-3">
          <div className="text-xs font-semibold mb-2">Point-in-time coverage</div>
          <div className="text-[11px] space-y-1.5">
            <Row label="Universe" value={status?.universe?.id || "not loaded"} />
            <Row label="Coverage starts" value={status?.universe?.coverage_start || "not established"} />
            <Row label="Historical universe" value={status?.universe?.historical_verified ? "verified" : "forward snapshots"} warn={!status?.universe?.historical_verified} />
          </div>
        </section>
        <section className="glass-inset rounded-xl p-3">
          <div className="text-xs font-semibold mb-2">Upcoming US sessions</div>
          <div className="space-y-1.5">{sessions.slice(0, 7).map(s => <Row key={s.session_date} label={s.session_date} value={s.status === "open" ? `${clock(s.open_at)}–${clock(s.close_at)}` : s.status} />)}{sessions.length===0&&<div className="text-[11px] t-tertiary">Authoritative calendar not loaded.</div>}</div>
        </section>
        <section className="glass-inset rounded-xl p-3">
          <div className="text-xs font-semibold mb-2">Data quality</div>
          <div className="space-y-2">{issues.slice(0,8).map(i => <div key={i.issue_key} className="text-[11px]"><div className="flex justify-between"><span className={i.severity === "error" ? "t-down" : "text-amber-500"}>{i.category}</span><span className="mono t-tertiary">{i.provider}</span></div><div className="t-secondary mt-0.5">{i.message}</div></div>)}{issues.length===0&&<div className="text-[11px] t-up">No open reference-data issues.</div>}</div>
        </section>
      </div>
    </div>
  );
}

function Metric({label,value,warn=false}:{label:string;value:number;warn?:boolean}){return <div className="glass-inset rounded-lg p-2.5"><div className="text-[10px] uppercase tracking-wide t-tertiary">{label}</div><div className={`mono text-lg font-semibold ${warn?"text-amber-500":"t-primary"}`}>{value.toLocaleString()}</div></div>}
function Row({label,value,warn=false}:{label:string;value:string;warn?:boolean}){return <div className="flex justify-between gap-3"><span className="t-tertiary">{label}</span><span className={`mono text-right ${warn?"text-amber-500":"t-secondary"}`}>{value}</span></div>}
function actionDate(a:CorporateAction){return a.effective_date||a.ex_date||a.payable_date||a.process_date||a.announcement_date}
function terms(a:CorporateAction){if(a.ratio_numerator&&a.ratio_denominator)return `${a.ratio_numerator}:${a.ratio_denominator}`;if(a.cash_amount)return `${a.cash_amount} ${a.currency||""}`.trim();return "—"}
function clock(value?:string){if(!value)return "—";const d=new Date(value);return Number.isNaN(d.getTime())?value:d.toLocaleTimeString([],{hour:"2-digit",minute:"2-digit"})}
