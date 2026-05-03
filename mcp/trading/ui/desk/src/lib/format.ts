// Display helpers. Pure formatting — no business logic, no
// rounding semantics that would matter to a real trade engine.

const usd0 = new Intl.NumberFormat("en-US", {
  style: "currency", currency: "USD",
  minimumFractionDigits: 0, maximumFractionDigits: 0,
});
const usd2 = new Intl.NumberFormat("en-US", {
  style: "currency", currency: "USD",
  minimumFractionDigits: 2, maximumFractionDigits: 2,
});
const num2 = new Intl.NumberFormat("en-US", {
  minimumFractionDigits: 2, maximumFractionDigits: 2,
});
const num0 = new Intl.NumberFormat("en-US", {
  minimumFractionDigits: 0, maximumFractionDigits: 0,
});

export function money(n: number, opts: { decimals?: 0 | 2 } = {}): string {
  return (opts.decimals === 0 ? usd0 : usd2).format(n);
}
export function moneySigned(n: number): string {
  const sign = n > 0 ? "+" : n < 0 ? "−" : "";
  return sign + usd2.format(Math.abs(n));
}
export function pct(n: number, decimals = 2): string {
  return n.toFixed(decimals) + "%";
}
export function pctSigned(n: number, decimals = 2): string {
  const sign = n > 0 ? "+" : n < 0 ? "−" : "";
  return sign + Math.abs(n).toFixed(decimals) + "%";
}
export function qty(n: number): string {
  return Math.abs(n) >= 1 ? num0.format(n) : num2.format(n);
}
export function bigNum(n: number): string {
  if (Math.abs(n) >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (Math.abs(n) >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (Math.abs(n) >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return num2.format(n);
}
export function timeAgo(ts: number | string): string {
  const ms = typeof ts === "string" ? new Date(ts).getTime() : ts;
  const s = Math.floor((Date.now() - ms) / 1000);
  if (s < 5) return "just now";
  if (s < 60) return s + "s ago";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m ago";
  const h = Math.floor(m / 60);
  if (h < 24) return h + "h ago";
  return Math.floor(h / 24) + "d ago";
}
export function clockTime(ts: number): string {
  return new Date(ts).toLocaleTimeString("en-US", {
    hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
  });
}
export { num2, num0 };
