type Expr = Record<string, any>;
type Program = Record<string, any>;
const field = "w-full px-2 py-1 rounded border border-border bg-bg-input text-text text-xs";
const button = "px-2 py-1 rounded border border-border text-xs hover:bg-bg-hover";
const frames = ["1m", "5m", "15m", "1h", "4h", "1d"];
const metrics = ["close", "open", "high", "low", "volume", "range", "body", "close_fraction", "sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "price", "bid", "ask", "position_qty", "entry_count", "minute", "weekday", "equity", "window_high", "window_low", "window_complete", "correlation", "beta", "day_of_month", "month", "feature"];
const rolling = new Set(["sma", "ema", "stddev", "volatility", "rsi", "atr", "highest_high", "lowest_low", "return", "correlation", "beta"]);
const operators = [">", ">=", "<", "<=", "==", "!=", "and", "or", "crosses_above", "crosses_below", "+", "-", "*", "/", "min", "max", "not", "abs", "sign", "log", "exp", "sqrt", "clamp", "if", "cs_rank", "cs_zscore", "cs_demean", "cs_count", "top", "bottom", "quantile"];
const unaryOps = new Set(["not", "abs", "sign", "log", "exp", "sqrt", "cs_rank", "cs_zscore", "cs_demean", "cs_count"]);
const ternaryOps = new Set(["clamp", "if"]);
const number = (value = 0): Expr => ({ value });
const defaultCondition = (): Expr => ({ op: ">", args: [{ metric: "close" }, number(0)] });
const defaultEntry = () => ({ kind: "enter", side: "buy", order_type: "market", stop_pct: .01, sizing: { mode: "fixed_risk", amount: 100 } });

export function ExpressionEditor({ value, onChange, program, depth = 0 }: { value: Expr; onChange: (e: Expr) => void; program: Program; depth?: number }) {
 const kind = value.op ? "operation" : value.metric ? "metric" : value.name ? "name" : "value";
 if (depth > 12) return <p className="text-xs text-text-muted">Use the advanced definition editor for deeper expressions.</p>;
 const names = [...Object.keys(program.calculations || {}), ...Object.keys(program.initial || {})];
 const update = (patch: Expr) => onChange({ ...value, ...patch });
 return <div className="grid gap-1 rounded border border-border p-2">
  <div className="flex flex-wrap gap-1">
   <select aria-label="Expression kind" className={field + " flex-1"} value={kind} onChange={e => onChange(e.target.value === "operation" ? defaultCondition() : e.target.value === "metric" ? { metric: "close" } : e.target.value === "name" ? { name: names[0] || "calculation" } : number())}>
    <option value="metric">Market / state value</option><option value="value">Number</option><option value="operation">Comparison / formula</option><option value="name">Calculation / variable</option>
   </select>
   {kind === "value" && <input aria-label="Constant value" type="number" step="any" className={field + " flex-1"} value={value.value ?? 0} onChange={e => onChange(number(Number(e.target.value)))} />}
   {kind === "name" && <input aria-label="Calculation name" className={field + " flex-1"} value={value.name} onChange={e => onChange({ name: e.target.value })} placeholder={names.join(", ")} />}
   {kind === "metric" && <select aria-label="Metric" className={field + " flex-1"} value={value.metric} onChange={e => onChange({ metric: e.target.value, ...(rolling.has(e.target.value) ? { period: 20 } : {}), ...(e.target.value.startsWith("window_") ? { window: Object.keys(program.windows || {})[0] || "window" } : {}), ...(e.target.value === "feature" ? { feature: "sentiment", field: "score" } : {}) })}>{metrics.map(m => <option key={m}>{m}</option>)}</select>}
   {kind === "operation" && <select aria-label="Operator" className={field + " flex-1"} value={value.op} onChange={e => { const op = e.target.value; const n = unaryOps.has(op) ? 1 : ternaryOps.has(op) ? 3 : 2; const count = { top: 1, bottom: 1, quantile: 0.5 }[op as "top"]; onChange({ op, args: Array.from({ length: n }, (_, i) => count !== undefined && i === 1 ? number(count) : value.args?.[i] || number()) }); }}>{operators.map(op => <option key={op}>{op}</option>)}</select>}
  </div>
  {kind === "metric" && <div className="flex flex-wrap gap-1">
   {!["price", "bid", "ask", "position_qty", "entry_count", "minute", "weekday", "equity", "feature"].includes(value.metric) && !value.metric.startsWith("window_") && <>
    <label className="text-xs flex-1">Timeframe<select aria-label="Metric timeframe" className={field} value={value.timeframe || ""} onChange={e => { const next = { ...value }; if (e.target.value) next.timeframe = e.target.value; else delete next.timeframe; onChange(next); }}><option value="">Base timeframe</option>{frames.map(f => <option key={f}>{f}</option>)}</select></label>
    <label className="text-xs flex-1">Historical offset<input aria-label="Historical offset" className={field} type="number" min="0" max="2000" value={value.offset || 0} onChange={e => update({ offset: Number(e.target.value) })} /></label>
   </>}
   {rolling.has(value.metric) && <label className="text-xs flex-1">Period<input aria-label="Indicator period" className={field} type="number" min="1" max="2000" value={value.period || 20} onChange={e => update({ period: Number(e.target.value) })} /></label>}
   {value.metric.startsWith("window_") && <label className="text-xs flex-1">Window<input aria-label="Window name" className={field} value={value.window || ""} onChange={e => update({ window: e.target.value })} /></label>}
   {value.metric === "feature" && <><input aria-label="Feature name" className={field} value={value.feature || ""} onChange={e => update({ feature: e.target.value })} /><input aria-label="Feature field" className={field} value={value.field || ""} onChange={e => update({ field: e.target.value })} /></>}
  </div>}
  {kind === "operation" && (value.args || []).map((arg: Expr, i: number) => <ExpressionEditor key={i} value={arg} program={program} depth={depth + 1} onChange={next => update({ args: value.args.map((a: Expr, j: number) => j === i ? next : a) })} />)}
  {kind === "metric" && ["sma", "ema", "stddev"].includes(value.metric) && <>
   <label className="text-xs"><input type="checkbox" checked={!!value.input} onChange={e => { const next = { ...value }; if (e.target.checked) next.input = { metric: "close", ...(value.timeframe ? { timeframe: value.timeframe } : {}) }; else delete next.input; onChange(next); }} /> Apply to a calculated series</label>
   {value.input && <ExpressionEditor value={value.input} program={program} depth={depth + 1} onChange={input => update({ input })} />}
  </>}
 </div>;
}

function ActionEditor({ action, program, onChange }: { action: Expr; program: Program; onChange: (a: Expr) => void }) {
 const update = (patch: Expr) => onChange({ ...action, ...patch });
 const boundary = (which: "stop" | "target") => {
  const pct = `${which}_pct`;
  const kind = action[which] ? "expression" : action[pct] ? "percent" : "none";
  return <div className="grid gap-1"><label className="text-xs">{which === "stop" ? "Stop loss" : "Take profit"}<select aria-label={`${which} mode`} className={field} value={kind} onChange={e => { const next = { ...action }; delete next[which]; delete next[pct]; if (e.target.value === "expression") next[which] = { metric: "close" }; if (e.target.value === "percent") next[pct] = .01; onChange(next); }}><option value="none">None</option><option value="percent">Percent from actual fill</option><option value="expression">Price from expression</option></select></label>
   {kind === "percent" && <input aria-label={`${which} percent`} type="number" min="0" step="any" className={field} value={action[pct] * 100} onChange={e => update({ [pct]: Number(e.target.value) / 100 })} />}
   {kind === "expression" && <ExpressionEditor value={action[which]} program={program} onChange={value => update({ [which]: value })} />}
  </div>;
 };
 return <div className="grid gap-2 p-2 rounded bg-bg-input border border-border">
  <select aria-label="Action kind" className={field} value={action.kind} onChange={e => onChange(e.target.value === "enter" ? defaultEntry() : e.target.value === "set" ? { kind: "set", name: Object.keys(program.initial || {})[0] || "variable", value: number() } : e.target.value === "target" ? { kind: "target", weight: number(1), construct: { normalize: "gross", gross: 1 } } : { kind: e.target.value })}><option value="enter">Enter a trade</option><option value="target">Hold target weights</option><option value="flatten">Close position and cancel orders</option><option value="cancel">Cancel orders</option><option value="set">Update variable</option></select>
  {(program.symbols || []).length > 1 && ["enter", "flatten", "cancel"].includes(action.kind) && <label className="text-xs">Symbol (blank: the rule's symbol, or every symbol for flatten/cancel)<input aria-label="Action symbol" className={field} value={action.symbol || ""} onChange={e => { const next = { ...action, symbol: e.target.value.toUpperCase() }; if (!next.symbol) delete next.symbol; onChange(next); }} /></label>}
  {action.kind === "target" && <>
   <p className="text-xs">Weight per symbol (a signed score; negative needs simulator shorts)</p>
   <ExpressionEditor value={action.weight || number(1)} program={program} onChange={weight => update({ weight })} />
   <div className="grid grid-cols-3 gap-2">
    <label className="text-xs">Normalize<select aria-label="Target normalize" className={field} value={action.construct?.normalize || "legs"} onChange={e => update({ construct: { ...action.construct, normalize: e.target.value } })}><option value="legs">Long and short legs</option><option value="gross">Scale to gross</option><option value="none">Weights as given</option></select></label>
    <label className="text-xs">Gross<input aria-label="Target gross" type="number" step="any" className={field} value={action.construct?.gross ?? 1} onChange={e => update({ construct: { ...action.construct, gross: Number(e.target.value) } })} /></label>
    <label className="text-xs">Max per symbol<input aria-label="Target max position" type="number" step="any" className={field} value={action.construct?.max_position ?? 0} onChange={e => update({ construct: { ...action.construct, max_position: Number(e.target.value) } })} /></label>
   </div>
  </>}
  {action.kind === "enter" && <>
   <div className="grid grid-cols-2 gap-2">
    <label className="text-xs">Direction<select aria-label="Entry side" className={field} value={action.side} onChange={e => update({ side: e.target.value })}><option value="buy">Long / buy</option><option value="sell">Short / sell</option></select></label>
    <label className="text-xs">Order type<select aria-label="Entry order type" className={field} value={action.order_type || "market"} onChange={e => { const next: Expr = { ...action, order_type: e.target.value }; if (e.target.value === "market") delete next.price; else next.price ||= { metric: "close" }; onChange(next); }}><option value="market">Market</option><option value="limit">Limit</option><option value="stop">Stop</option></select></label>
   </div>
   {action.order_type && action.order_type !== "market" && <ExpressionEditor value={action.price || { metric: "close" }} program={program} onChange={price => update({ price })} />}
   <div className="grid grid-cols-2 gap-2"><label className="text-xs">Sizing<select aria-label="Sizing mode" className={field} value={action.sizing?.mode || "fixed_risk"} onChange={e => update({ sizing: { mode: e.target.value, amount: e.target.value === "equity_pct" ? .1 : 100 } })}><option value="fixed_risk">Account-currency risk at stop</option><option value="quantity">Quantity</option><option value="notional">Account-currency notional</option><option value="equity_pct">Fraction of account equity</option></select></label><label className="text-xs">Amount<input aria-label="Sizing amount" type="number" step="any" className={field} value={action.sizing?.amount ?? 100} onChange={e => update({ sizing: { ...action.sizing, amount: Number(e.target.value) } })} /></label></div>
   {boundary("stop")}{boundary("target")}
   <div className="grid grid-cols-2 gap-2"><label className="text-xs">Trail activation profit (%)<input aria-label="Trail activation" className={field} type="number" min="0" step="any" value={(action.trail_activation_pct || 0) * 100} onChange={e => update({ trail_activation_pct: Number(e.target.value) / 100 })} /></label><label className="text-xs">Trail distance (%)<input aria-label="Trail distance" className={field} type="number" min="0" step="any" value={(action.trail_distance_pct || 0) * 100} onChange={e => update({ trail_distance_pct: Number(e.target.value) / 100 })} /></label></div>
  </>}
  {action.kind === "target" ? null : action.kind === "set" ? <><input aria-label="Variable to update" className={field} value={action.name || ""} onChange={e => update({ name: e.target.value })} /><ExpressionEditor value={action.value || number()} program={program} onChange={value => update({ value })} /></> : <label className="text-xs">Linked order group (optional)<input aria-label="Order group" className={field} value={action.group || ""} onChange={e => update({ group: e.target.value })} /></label>}
 </div>;
}

export function RuleProgramEditor({ program, onChange }: { program: Program; onChange: (p: Program) => void }) {
 const update = (patch: Program) => onChange({ ...program, ...patch });
 const rules = program.rules || [];
 const newName = (map: Record<string, unknown>, prefix: string) => { let i = 1; while (`${prefix}_${i}` in map) i++; return `${prefix}_${i}`; };
 const updateRule = (index: number, patch: Expr) => update({ rules: rules.map((r: Expr, i: number) => i === index ? { ...r, ...patch } : r) });
 return <div className="grid gap-3 text-text">
  <div className="grid grid-cols-3 gap-2"><label className="text-xs">Symbols<input aria-label="Program symbols" className={field} value={(program.symbols || []).join(", ")} onChange={e => update({ symbols: e.target.value.toUpperCase().split(/[\s,]+/).filter(Boolean) })} /></label><label className="text-xs">Base timeframe<select aria-label="Program timeframe" className={field} value={program.timeframe || "1h"} onChange={e => update({ timeframe: e.target.value })}>{frames.map(f => <option key={f}>{f}</option>)}</select></label><label className="text-xs">Timezone<input aria-label="Program timezone" className={field} value={program.timezone || "UTC"} onChange={e => update({ timezone: e.target.value })} /></label></div>
  <details><summary className="cursor-pointer text-xs">Calculations, variables, windows and clock schedules</summary>
   <div className="grid gap-2 mt-2">
    {Object.entries(program.calculations || {}).map(([name, expr]) => <div key={name}><div className="flex items-center gap-2 text-xs"><strong>{name}</strong><button type="button" className={button} onClick={() => { const next = { ...program.calculations }; delete next[name]; update({ calculations: next }); }}>Remove</button></div><ExpressionEditor value={expr as Expr} program={program} onChange={value => update({ calculations: { ...program.calculations, [name]: value } })} /></div>)}
    <button type="button" className={button} onClick={() => { const name = newName(program.calculations || {}, "calculation"); update({ calculations: { ...program.calculations, [name]: { metric: "sma", period: 20 } } }); }}>Add calculation</button>
    {Object.entries(program.initial || {}).map(([name, value]) => <label key={name} className="text-xs">Variable: {name}<input aria-label={`Initial ${name}`} className={field} type="number" step="any" value={Number(value)} onChange={e => update({ initial: { ...program.initial, [name]: Number(e.target.value) } })} /></label>)}
    <button type="button" className={button} onClick={() => update({ initial: { ...program.initial, [newName(program.initial || {}, "variable")]: 0 } })}>Add variable</button>
    {Object.entries(program.windows || {}).map(([name, raw]) => { const window = raw as Expr; return <div key={name} className="grid grid-cols-3 gap-2 text-xs"><span>Window: {name}</span><input aria-label={`${name} start`} className={field} type="time" value={window.start} onChange={e => update({ windows: { ...program.windows, [name]: { ...window, start: e.target.value } } })} /><input aria-label={`${name} end`} className={field} type="time" value={window.end} onChange={e => update({ windows: { ...program.windows, [name]: { ...window, end: e.target.value } } })} /></div>; })}
    <button type="button" className={button} onClick={() => update({ windows: { ...program.windows, [newName(program.windows || {}, "window")]: { start: "08:00", end: "11:00" } } })}>Add session window</button>
    {Object.entries(program.schedules || {}).map(([name, clock]) => <label key={name} className="text-xs">Clock: {name}<input aria-label={`${name} clock`} className={field} type="time" value={String(clock)} onChange={e => update({ schedules: { ...program.schedules, [name]: e.target.value } })} /></label>)}
    <button type="button" className={button} onClick={() => update({ schedules: { ...program.schedules, [newName(program.schedules || {}, "clock")]: "18:00" } })}>Add clock schedule</button>
   </div>
  </details>
  {rules.map((rule: Expr, index: number) => <details key={index} open className="border border-border rounded p-2">
   <summary className="cursor-pointer text-sm font-semibold">{rule.id || `Rule ${index + 1}`}</summary>
   <div className="grid gap-2 mt-2">
    <div className="grid grid-cols-3 gap-2"><input aria-label="Rule name" className={field} value={rule.id || ""} onChange={e => updateRule(index, { id: e.target.value })} /><select aria-label="Rule trigger" className={field} value={rule.on || "bar.close"} onChange={e => { const next: Expr = { ...rule, on: e.target.value }; if (next.on !== "clock") delete next.schedule; update({ rules: rules.map((r: Expr, i: number) => i === index ? next : r) }); }}><option value="bar.close">Candle close</option><option value="quote">Quote</option><option value="fill">Fill</option><option value="clock">Clock</option><option value="any">Any event</option></select><select aria-label="Repeat policy" className={field} value={rule.repeat || "once_per_bar"} onChange={e => updateRule(index, { repeat: e.target.value, ...(e.target.value === "until_reset" ? { reset: rule.reset || defaultCondition() } : {}) })}><option value="once_per_bar">Once per candle</option><option value="once_per_day">Once per local day</option><option value="until_reset">Wait for reset</option><option value="always">Every matching event</option></select></div>
    {(program.symbols || []).length > 1 && <label className="text-xs"><input aria-label="Rule for each symbol" type="checkbox" checked={!!rule.for_each} onChange={e => { const next = { ...rule }; if (e.target.checked) next.for_each = true; else delete next.for_each; update({ rules: rules.map((r: Expr, i: number) => i === index ? next : r) }); }} /> Run for each symbol</label>}
    {rule.on === "bar.close" && <label className="text-xs">Signal timeframe<select aria-label="Rule timeframe" className={field} value={rule.timeframe || ""} onChange={e => updateRule(index, { timeframe: e.target.value })}><option value="">Base timeframe</option>{frames.map(f => <option key={f}>{f}</option>)}</select></label>}
    {rule.on === "clock" && <label className="text-xs">Clock schedule<select aria-label="Rule clock" className={field} value={rule.schedule || ""} onChange={e => updateRule(index, { schedule: e.target.value })}><option value="">Any clock observation</option>{Object.keys(program.schedules || {}).map(n => <option key={n}>{n}</option>)}</select></label>}
    <label className="text-xs"><input type="checkbox" checked={!!rule.when} onChange={e => { const next = { ...rule }; if (e.target.checked) next.when = defaultCondition(); else delete next.when; update({ rules: rules.map((r: Expr, i: number) => i === index ? next : r) }); }} /> Require a condition</label>
    {rule.when && <ExpressionEditor value={rule.when} program={program} onChange={when => updateRule(index, { when })} />}
    {rule.repeat === "until_reset" && <div><p className="text-xs my-1">Rearm when</p><ExpressionEditor value={rule.reset || defaultCondition()} program={program} onChange={reset => updateRule(index, { reset })} /></div>}
    {(rule.actions || []).map((action: Expr, i: number) => <div key={i}><ActionEditor action={action} program={program} onChange={next => updateRule(index, { actions: rule.actions.map((a: Expr, j: number) => j === i ? next : a) })} /><button type="button" className={button + " mt-1"} onClick={() => updateRule(index, { actions: rule.actions.filter((_: Expr, j: number) => j !== i) })}>Remove action</button></div>)}
    <div className="flex gap-2"><button type="button" className={button} onClick={() => updateRule(index, { actions: [...(rule.actions || []), defaultEntry()] })}>Add action</button><button type="button" className={button} onClick={() => update({ rules: rules.filter((_: Expr, i: number) => i !== index) })}>Remove rule</button></div>
   </div>
  </details>)}
  <button type="button" className={button} onClick={() => update({ rules: [...rules, { id: newName(Object.fromEntries(rules.map((r: Expr) => [r.id, true])), "rule"), on: "bar.close", when: defaultCondition(), actions: [defaultEntry()] }] })}>Add rule</button>
 </div>;
}
