import type { PublicOffer } from "./types";

export function formatPublicOffer(offer: PublicOffer): string {
  const amount = (() => {
    try {
      return new Intl.NumberFormat(undefined, { style: "currency", currency: offer.currency })
        .format(offer.unit_amount_cents / 100);
    } catch {
      return `${offer.currency} ${(offer.unit_amount_cents / 100).toFixed(2)}`;
    }
  })();
  if (!offer.interval) return amount;
  const count = (offer.interval_count || 1) > 1 ? `${offer.interval_count} ` : "";
  return `${amount} / ${count}${offer.interval}${(offer.interval_count || 1) > 1 ? "s" : ""}`;
}


export function formatCheckoutOffer(offer: PublicOffer, amount: number, currency: string, labelOneTime = false): string {
  const price = formatPublicOffer({ ...offer, unit_amount_cents: amount, currency });
  return price + (labelOneTime && !offer.interval ? " one-time" : "");
}
