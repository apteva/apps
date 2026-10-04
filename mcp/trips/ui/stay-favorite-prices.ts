// Hotel nights follow the displayed calendar dates, independent of DST.
export function stayNightCount(start: string, end: string): number {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(start) || !/^\d{4}-\d{2}-\d{2}$/.test(end)) return 0;
  const a = Date.parse(`${start}T00:00:00Z`), b = Date.parse(`${end}T00:00:00Z`);
  return Number.isFinite(a) && Number.isFinite(b) ? Math.max(0, Math.round((b - a) / 86400000)) : 0;
}

export function stayPriceTotal(amount: number | null, basis: string, start: string, end: string): number | null {
  if (amount == null) return null;
  const nights = stayNightCount(start, end);
  if (basis === "per_night" && !nights) return null;
  const total = basis === "per_night" ? amount * nights : amount;
  return Number.isSafeInteger(total) && total >= 0 ? total : null;
}
