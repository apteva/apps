import { useEffect, useState, useRef } from "react";
import type { Portfolio, Position, Sym } from "../api/types.ts";
import { money } from "../lib/format.ts";
import { placeOrder, type PlaceOrderArgs, type PlaceOrderResult } from "../api/portfolios.ts";

type Side = "buy" | "sell" | "yes" | "no";
type Type = "market" | "limit" | "stop";
type TIF = "day" | "gtc" | "ioc";

export function OrderTicket({
  symbol,
  portfolio,
  universe,
  positions,
}: {
  symbol: string;
  portfolio: Portfolio;
  universe: Sym[];
  positions: Position[];
}) {
  const sym = universe.find((s) => s.symbol === symbol);
  const isPoly = sym?.asset_class === "polymarket";
  const allowed = sym ? portfolio.allowed_classes.includes(sym.asset_class) : false;

  const requestIntent = useRef<{ body: string; key: string } | null>(null);
  const [side, setSide] = useState<Side>(isPoly ? "yes" : "buy");
  const [outcome, setOutcome] = useState<"yes" | "no">("yes");
  const [type, setType] = useState<Type>("limit");
  const [tif, setTif] = useState<TIF>("day");
  const [qty, setQty] = useState(isPoly ? "100" : "10");
  const [price, setPrice] = useState(initialPrice(sym));
  const [rationale, setRationale] = useState("");
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<null | { tone: "ok" | "warn"; msg: string }>(null);

  // Reset when the symbol changes.
  useEffect(() => {
    setSide(isPoly ? "yes" : "buy");
    setOutcome("yes");
    setType(isPoly ? "limit" : "limit");
    setQty(isPoly ? "100" : "10");
    setPrice(initialPrice(sym));
    setFeedback(null);
  }, [symbol]);

  const heldOutcomes = positions
    .filter((p) => p.symbol === symbol && p.asset_class === "polymarket" && p.qty > 0 && p.outcome)
    .map((p) => p.outcome!.toLowerCase() as "yes" | "no");
  const closeOutcomes: readonly ("yes" | "no")[] = heldOutcomes.length > 0
    ? Array.from(new Set(heldOutcomes))
    : ["yes", "no"];
  const selectedOutcome = side === "sell" ? outcome : side === "no" ? "no" : "yes";

  useEffect(() => {
    if (side === "sell" && !closeOutcomes.includes(outcome)) setOutcome(closeOutcomes[0] ?? "yes");
  }, [side, outcome, closeOutcomes.join(",")]);

  // Polymarket: keep the price snapped to the selected outcome.
  useEffect(() => {
    if (!isPoly || !sym) return;
    setPrice(((selectedOutcome === "yes" ? sym.yes_price : sym.no_price) ?? sym.price ?? 0).toFixed(2));
  }, [selectedOutcome, isPoly, symbol]);

  if (!sym) return null;

  const qtyNum = parseFloat(qty) || 0;
  const priceNum = parseFloat(price) || 0;
  const selectedMark = isPoly
    ? ((selectedOutcome === "yes" ? sym.yes_price : sym.no_price) ?? sym.price)
    : sym.price;
  const effectivePrice = type === "market" ? selectedMark : priceNum;
  const notional = qtyNum * effectivePrice;

  const submit = async () => {
    if (!allowed) {
      setFeedback({ tone: "warn", msg: `${sym.asset_class} not in this portfolio's allowed classes.` });
      return;
    }
    if (rationale.trim().length < 30) {
      setFeedback({ tone: "warn", msg: "Rationale must be at least 30 characters." });
      return;
    }
    if (isPoly && (priceNum <= 0 || priceNum >= 1)) {
      setFeedback({ tone: "warn", msg: "Polymarket prices must be between 0 and 1." });
      return;
    }

    const args: PlaceOrderArgs = {
      symbol: sym.symbol,
      side,
      type,
      qty: qtyNum,
      tif,
      rationale,
    };
    if (isPoly && side === "sell") args.outcome = outcome;
    if (type === "limit") args.limit_price = priceNum;
    if (type === "stop")  args.stop_price  = priceNum;

    const body = JSON.stringify(args);
    if (!requestIntent.current || requestIntent.current.body !== body) requestIntent.current = { body, key: crypto.randomUUID() };
    args.idempotency_key = requestIntent.current.key;
    setBusy(true);
    try {
      const r: PlaceOrderResult = await placeOrder(portfolio.id, args);
      if ("status" in r && r.status === "rejected") {
        setFeedback({ tone: "warn", msg: `Rejected: ${r.code} — ${r.detail}` });
      } else if ("order_id" in r) {
        setFeedback({ tone: r.uncertain ? "warn" : "ok", msg: r.uncertain ? `Order ${r.order_id} needs reconciliation. ${r.detail ?? "The broker response was uncertain."}` : `Order ${r.order_id} placed (${r.status}).` });
        if (!r.uncertain) { setRationale(""); requestIntent.current = null; }
      }
    } catch (e: any) {
      setFeedback({ tone: "warn", msg: e?.message ?? String(e) });
    } finally {
      setBusy(false);
    }
  };

  const sideToTone = (s: Side) => (s === "buy" || s === "yes" ? "up" : "down");
  const sideButtonClass = (s: Side) => {
    const tone = sideToTone(s);
    return side === s ? (tone === "up" ? "btn-up" : "btn-down") : "";
  };

  const sides: Side[] = isPoly ? ["yes", "no", "sell"] : ["buy", "sell"];

  return (
    <section className="glass rounded-2xl p-4 fade-up">
      <header className="flex items-center justify-between mb-3">
        <h2 className="text-[12px] uppercase tracking-wider t-tertiary font-semibold">Order ticket</h2>
        <span className="text-[10px] t-tertiary mono truncate ml-2 max-w-[160px]" title={sym.symbol}>
          {prettySymbol(sym.symbol)}
        </span>
      </header>

      {!allowed && (
        <p className="text-[11px] mb-3 px-2 py-1.5 rounded-md bg-warn-soft tabular">
          {sym.asset_class} is not in <b>{portfolio.name}</b>'s allowed classes.
        </p>
      )}

      <div className={`grid ${isPoly ? "grid-cols-3" : "grid-cols-2"} gap-2 mb-3`}>
        {sides.map((s) => (
          <button key={s} onClick={() => setSide(s)} className={`btn ${sideButtonClass(s)} justify-center`} disabled={!allowed || busy}>
            {s.toUpperCase()}
          </button>
        ))}
      </div>

      {isPoly && side === "sell" && (
        <div className="mb-3">
          <Field label="Outcome to close">
            <select className="field" value={outcome} onChange={(e) => setOutcome(e.target.value as "yes" | "no")} disabled={!allowed || busy}>
              {closeOutcomes.map((value) => <option key={value} value={value}>{value.toUpperCase()}</option>)}
            </select>
          </Field>
        </div>
      )}

      <div className="flex gap-1 mb-3">
        {(isPoly ? (["market", "limit"] as const) : (["market", "limit", "stop"] as const)).map((t) => (
          <button key={t} onClick={() => setType(t)} className={`tab flex-1 text-center ${type === t ? "active" : ""}`} disabled={!allowed || busy}>
            {t}
          </button>
        ))}
      </div>

      <div className="space-y-2.5">
        <Field label={isPoly ? "Shares" : "Quantity"}>
          <input className="field" type="number" min={0} step={isPoly ? 1 : 0.01} value={qty} onChange={(e) => setQty(e.target.value)} disabled={!allowed || busy} />
        </Field>
        {type !== "market" && (
          <Field label={isPoly ? "Limit price (0–1)" : type === "limit" ? "Limit price" : "Stop price"}>
            <input
              className="field"
              type="number"
              min={isPoly ? 0.01 : 0}
              max={isPoly ? 0.99 : undefined}
              step="0.01"
              value={price}
              onChange={(e) => setPrice(e.target.value)}
              disabled={!allowed || busy}
            />
          </Field>
        )}
        <Field label="Time in force">
          <select className="field" value={tif} onChange={(e) => setTif(e.target.value as TIF)} disabled={!allowed || busy}>
            <option value="day">Day</option>
            <option value="gtc">Good til cancelled</option>
            <option value="ioc">Immediate or cancel</option>
          </select>
        </Field>
        <Field label="Rationale (≥ 30 chars, required)">
          <textarea
            className="field"
            rows={2}
            placeholder="Why this trade?"
            value={rationale}
            onChange={(e) => setRationale(e.target.value)}
            style={{ resize: "vertical", fontFamily: "var(--font-sans)" }}
            disabled={!allowed || busy}
          />
        </Field>
      </div>

      <div className="grid grid-cols-2 gap-3 mt-3 pt-3 border-t border-[var(--border)]">
        <Mini label={isPoly && side === "sell" ? "Estimated proceeds" : isPoly ? "Cost" : "Estimated cost"} value={money(notional)} />
        <Mini
          label={isPoly && side === "sell" ? "Outcome" : isPoly ? "Max payout" : "Cash after"}
          value={isPoly && side === "sell" ? selectedOutcome.toUpperCase() : money(isPoly ? qtyNum : portfolio.cash - (side === "buy" ? notional : -notional))}
          tone={isPoly ? "up" : undefined}
        />
      </div>

      <button
        onClick={submit}
        className={`btn ${sideToTone(side) === "up" ? "btn-up" : "btn-down"} w-full justify-center mt-3 py-2.5 text-[13px]`}
        disabled={!allowed || busy}
      >
        {busy ? "Placing…" : `Place ${side.toUpperCase()} order`}
      </button>

      {feedback && (
        <p className={`text-[11px] mt-2 px-2 py-1.5 rounded-md ${feedback.tone === "warn" ? "bg-down-soft" : "bg-up-soft"}`}>
          {feedback.msg}
        </p>
      )}
    </section>
  );
}

function initialPrice(sym: Sym | undefined): string {
  if (!sym) return "0";
  if (sym.asset_class === "polymarket") return ((sym.yes_price ?? sym.price) ?? 0).toFixed(2);
  return sym.price.toFixed(2);
}

function prettySymbol(s: string): string {
  return s.startsWith("POLY:") ? s.slice(5) : s;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="text-[10px] uppercase tracking-wider t-tertiary font-medium">{label}</span>
      <div className="mt-1">{children}</div>
    </label>
  );
}

function Mini({ label, value, tone }: { label: string; value: string; tone?: "up" | "down" }) {
  const cls = tone === "up" ? "t-up" : tone === "down" ? "t-down" : "t-primary";
  return (
    <div className="flex flex-col leading-tight">
      <span className="text-[10px] uppercase tracking-wider t-tertiary font-medium">{label}</span>
      <span className={`mono text-[13px] tabular ${cls}`}>{value}</span>
    </div>
  );
}
