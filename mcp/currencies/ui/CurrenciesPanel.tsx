import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/currencies";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Currency {
  code: string;
  name: string;
  minor_units: number | null;
  kind: string;
  active: boolean;
}

interface PathEdge {
  rate_id: number;
  base: string;
  quote: string;
  rate: string;
  rate_kind: string;
  provider: string;
  effective_at: string;
  inverted: boolean;
}

interface RateQuote {
  quote_id: string;
  base: string;
  quote: string;
  rate: string;
  rate_kind: string;
  effective_at: string;
  effective_date: string;
  derived: boolean;
  identity: boolean;
  stale: boolean;
  path: PathEdge[];
  warnings: string[];
}

interface ProviderStatus {
  connection_id: number;
  provider: string;
  name: string;
  status: string;
  priority: number;
  last_success_at?: string;
  last_error?: string;
  failure_count: number;
}

interface SourcesStatus {
  providers: ProviderStatus[];
  tracked_pairs: number;
  observations: number;
  newest_effective_at?: string;
  offline_manual_mode: boolean;
}

function query(projectId: string, params: Record<string, string>) {
  return new URLSearchParams({ project_id: projectId, ...params }).toString();
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    credentials: "same-origin",
    cache: "no-store",
    ...init,
    headers: init?.body ? { "Content-Type": "application/json", ...(init.headers || {}) } : init?.headers,
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || `${res.status} request failed`);
  return body as T;
}

function majorToMinor(value: string, exponent: number): string {
  const clean = value.trim();
  if (!/^-?\d+(\.\d+)?$/.test(clean)) throw new Error("Enter a plain decimal amount.");
  const negative = clean.startsWith("-");
  const unsigned = negative ? clean.slice(1) : clean;
  const [whole, fraction = ""] = unsigned.split(".");
  if (fraction.length > exponent) throw new Error(`This currency supports ${exponent} decimal place${exponent === 1 ? "" : "s"}.`);
  const digits = `${whole}${fraction.padEnd(exponent, "0")}`.replace(/^0+(?=\d)/, "") || "0";
  return `${negative && digits !== "0" ? "-" : ""}${digits}`;
}

function minorToMajor(value: string | number, exponent: number) {
  let raw = String(value);
  const negative = raw.startsWith("-");
  if (negative) raw = raw.slice(1);
  raw = raw.padStart(exponent + 1, "0");
  const whole = exponent ? raw.slice(0, -exponent) : raw;
  const fraction = exponent ? `.${raw.slice(-exponent)}` : "";
  return `${negative ? "-" : ""}${whole}${fraction}`;
}

function when(value?: string) {
  if (!value) return "—";
  try { return new Date(value).toLocaleString(); } catch { return value; }
}

export default function CurrenciesPanel({ projectId }: NativePanelProps) {
  const [currencies, setCurrencies] = useState<Currency[]>([]);
  const [sources, setSources] = useState<SourcesStatus | null>(null);
  const [base, setBase] = useState("EUR");
  const [quote, setQuote] = useState("USD");
  const [asOf, setAsOf] = useState("");
  const [amount, setAmount] = useState("100");
  const [rate, setRate] = useState<RateQuote | null>(null);
  const [conversion, setConversion] = useState<any>(null);
  const [loading, setLoading] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");

  const [manualBase, setManualBase] = useState("EUR");
  const [manualQuote, setManualQuote] = useState("USD");
  const [manualRate, setManualRate] = useState("");
  const [manualDate, setManualDate] = useState("");
  const [manualReason, setManualReason] = useState("");

  const currencyByCode = useMemo(() => new Map(currencies.map((c) => [c.code, c])), [currencies]);
  const selectable = useMemo(() => currencies.filter((c) => c.active && c.minor_units !== null && c.kind !== "special"), [currencies]);

  const refreshStatus = useCallback(async () => {
    const status = await request<SourcesStatus>(`${API}/sources?${query(projectId, {})}`);
    setSources(status);
  }, [projectId]);

  useEffect(() => {
    Promise.all([
      request<{ currencies: Currency[] }>(`${API}/currencies?${query(projectId, { active: "true", limit: "250" })}`),
      request<SourcesStatus>(`${API}/sources?${query(projectId, {})}`),
    ]).then(([defs, status]) => {
      setCurrencies(defs.currencies || []);
      setSources(status);
    }).catch((e) => setError(e.message));
  }, [projectId]);

  const lookup = async () => {
    setLoading(true); setError(""); setNotice(""); setConversion(null);
    try {
      const params: Record<string, string> = { base, quote, fetch: "true", allow_inverse: "true", allow_triangulation: "true" };
      if (asOf) params.as_of = new Date(`${asOf}T23:59:59Z`).toISOString();
      const result = await request<RateQuote>(`${API}/rates?${query(projectId, params)}`);
      setRate(result);
      await refreshStatus();
    } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  };

  const convert = async () => {
    setLoading(true); setError(""); setNotice("");
    try {
      const fromDef = currencyByCode.get(base);
      if (!fromDef || fromDef.minor_units === null) throw new Error(`No minor-unit definition for ${base}.`);
      const body: Record<string, unknown> = {
        amount_minor: majorToMinor(amount, fromDef.minor_units), from: base, to: quote,
        rounding: "half_even", fetch: true, allow_inverse: true, allow_triangulation: true,
      };
      if (asOf) body.as_of = new Date(`${asOf}T23:59:59Z`).toISOString();
      const result = await request<any>(`${API}/convert?${query(projectId, {})}`, { method: "POST", body: JSON.stringify(body) });
      setConversion(result);
      setRate(result.rate_snapshot);
      await refreshStatus();
    } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  };

  const saveManual = async () => {
    setLoading(true); setError(""); setNotice("");
    try {
      const body: Record<string, unknown> = { base: manualBase, quote: manualQuote, rate: manualRate, reason: manualReason };
      if (manualDate) body.effective_at = new Date(`${manualDate}T12:00:00Z`).toISOString();
      const result = await request<any>(`${API}/manual-rate?${query(projectId, {})}`, { method: "POST", body: JSON.stringify(body) });
      setNotice(result.created ? `Manual ${manualBase}/${manualQuote} rate saved.` : "That exact observation already exists.");
      setManualRate(""); setManualReason("");
      await refreshStatus();
    } catch (e) { setError((e as Error).message); }
    finally { setLoading(false); }
  };

  const swap = () => { setBase(quote); setQuote(base); setRate(null); setConversion(null); };
  const toDef = currencyByCode.get(quote);

  return (
    <div className="h-full min-w-0 overflow-auto p-4 md:p-5 text-text">
      <div className="w-full space-y-5">
        <header className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <h1 className="text-xl font-semibold">Currencies</h1>
            <p className="text-sm text-text-muted mt-1">Current and historical FX with immutable source provenance.</p>
          </div>
          <span className={`text-xs px-2 py-1 rounded border ${sources?.offline_manual_mode ? "border-yellow-500/40 text-yellow-500" : "border-green-500/40 text-green-500"}`}>
            {sources?.offline_manual_mode ? "Manual / cached mode" : `${sources?.providers.length || 0} provider${sources?.providers.length === 1 ? "" : "s"}`}
          </span>
        </header>

        <section className="grid grid-cols-1 sm:grid-cols-3 gap-3">
          <Stat label="ISO definitions" value={currencies.length || "—"} />
          <Stat label="Tracked pairs" value={sources?.tracked_pairs ?? "—"} />
          <Stat label="Observations" value={sources?.observations ?? "—"} />
        </section>

        {(error || notice) && <div className={`text-sm rounded border px-3 py-2 ${error ? "border-red/40 text-red" : "border-green-500/40 text-green-500"}`}>{error || notice}</div>}

        <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
          <section className="border border-border rounded-lg p-4 space-y-4">
            <div>
              <h2 className="font-medium">Rate and conversion</h2>
              <p className="text-xs text-text-muted mt-1">Leave the date empty for the latest eligible observation.</p>
            </div>
            <div className="flex flex-col sm:flex-row sm:items-end gap-2">
              <CurrencySelect label="From" value={base} onChange={setBase} currencies={selectable} />
              <button type="button" onClick={swap} className="h-9 w-full sm:w-auto shrink-0 border border-border rounded px-3 hover:bg-bg-input" aria-label="Swap currencies">⇄</button>
              <CurrencySelect label="To" value={quote} onChange={setQuote} currencies={selectable} />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <label className="text-xs text-text-muted">Amount
                <input value={amount} onChange={(e) => setAmount(e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text" />
              </label>
              <label className="text-xs text-text-muted">As of
                <input type="date" value={asOf} onChange={(e) => setAsOf(e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text" />
              </label>
            </div>
            <div className="flex gap-2">
              <button type="button" disabled={loading} onClick={lookup} className="px-3 py-1.5 text-sm border border-border rounded hover:bg-bg-input disabled:opacity-50">Get rate</button>
              <button type="button" disabled={loading} onClick={convert} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">Convert</button>
            </div>
            {rate && <RateResult rate={rate} />}
            {conversion && toDef?.minor_units !== null && toDef?.minor_units !== undefined && (
              <div className="rounded bg-accent/10 border border-accent/30 p-3">
                <div className="text-xs text-text-muted">Converted amount</div>
                <div className="text-2xl font-semibold mt-1">{minorToMajor(conversion.converted_amount_minor, toDef.minor_units)} {quote}</div>
                <div className="text-xs text-text-muted mt-1">Rounding: {conversion.rounding}{conversion.rounding_occurred ? " · rounded" : " · exact"}</div>
              </div>
            )}
          </section>

          <section className="border border-border rounded-lg p-4 space-y-4">
            <div>
              <h2 className="font-medium">Manual observation</h2>
              <p className="text-xs text-text-muted mt-1">Append-only fallback for official, contractual, or offline rates.</p>
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
              <CurrencySelect label="Base" value={manualBase} onChange={setManualBase} currencies={selectable} />
              <CurrencySelect label="Quote" value={manualQuote} onChange={setManualQuote} currencies={selectable} />
            </div>
            <label className="text-xs text-text-muted block">Rate · 1 {manualBase} equals
              <input value={manualRate} onChange={(e) => setManualRate(e.target.value)} placeholder="1.08425" className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text" />
            </label>
            <label className="text-xs text-text-muted block">Effective date
              <input type="date" value={manualDate} onChange={(e) => setManualDate(e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text" />
            </label>
            <label className="text-xs text-text-muted block">Reason
              <input value={manualReason} onChange={(e) => setManualReason(e.target.value)} placeholder="ECB reference rate for filing" className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text" />
            </label>
            <button type="button" disabled={loading || !manualRate || !manualReason} onClick={saveManual} className="px-3 py-1.5 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">Save observation</button>
          </section>
        </div>

        <section className="border border-border rounded-lg overflow-x-auto">
          <div className="p-3 border-b border-border"><h2 className="font-medium">FX providers</h2></div>
          {!sources?.providers.length ? <p className="p-4 text-sm text-text-muted">No provider bound. Identity, manual, and cached rates remain available.</p> : (
            <table className="w-full text-sm"><thead className="text-xs text-text-muted"><tr className="border-b border-border"><th className="text-left p-2">Provider</th><th className="text-left p-2">Status</th><th className="text-left p-2">Last success</th><th className="text-left p-2">Health</th></tr></thead>
              <tbody>{sources.providers.map((p) => <tr key={p.connection_id} className="border-b border-border/60"><td className="p-2">{p.name || p.provider}<div className="text-[10px] text-text-dim">{p.provider} · priority {p.priority}</div></td><td className="p-2">{p.status || "connected"}</td><td className="p-2 text-text-muted">{when(p.last_success_at)}</td><td className="p-2">{p.last_error ? <span className="text-red" title={p.last_error}>{p.failure_count} failure(s)</span> : <span className="text-green-500">Healthy</span>}</td></tr>)}</tbody>
            </table>
          )}
        </section>
      </div>
    </div>
  );
}

function CurrencySelect({ label, value, onChange, currencies }: { label: string; value: string; onChange: (v: string) => void; currencies: Currency[] }) {
  return <label className="min-w-0 flex-1 text-xs text-text-muted">{label}<select value={value} onChange={(e) => onChange(e.target.value)} className="mt-1 w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm text-text">{currencies.map((c) => <option key={c.code} value={c.code}>{c.code} · {c.name}</option>)}</select></label>;
}

function Stat({ label, value }: { label: string; value: string | number }) {
  return <div className="border border-border rounded-lg p-3"><div className="text-xs text-text-muted">{label}</div><div className="text-xl font-semibold mt-1">{value}</div></div>;
}

function RateResult({ rate }: { rate: RateQuote }) {
  return <div className="border border-border rounded p-3 space-y-2">
    <div className="flex items-baseline justify-between gap-3"><span className="text-lg font-semibold">1 {rate.base} = {rate.rate} {rate.quote}</span><span className={`text-xs ${rate.stale ? "text-yellow-500" : "text-green-500"}`}>{rate.stale ? "Stale" : "Eligible"}</span></div>
    <div className="text-xs text-text-muted">{rate.rate_kind} · effective {rate.effective_date || when(rate.effective_at)} · {rate.derived ? "derived" : "direct"} · {rate.quote_id}</div>
    {rate.path.map((p) => <div key={`${p.rate_id}-${p.base}`} className="text-xs text-text-muted">#{p.rate_id} {p.base}/{p.quote} {p.rate} · {p.provider}{p.inverted ? " · inverted" : ""}</div>)}
    {!!rate.warnings?.length && <ul className="text-xs text-yellow-500 list-disc ml-4">{rate.warnings.map((w) => <li key={w}>{w}</li>)}</ul>}
  </div>;
}
