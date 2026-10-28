export interface CheckoutOrderBump {
  id: number;
  title: string;
  description: string;
  image_url?: string;
  amount_cents: number;
  currency: string;
  selected: boolean;
  available?: boolean;
}

/** Shared buyer UI. The caller owns the API adapter and authoritative totals. */
export function OrderBumps({ offers, disabled, onSelect }: {
  offers: CheckoutOrderBump[];
  disabled: boolean;
  onSelect: (id: number, selected: boolean) => void;
}) {
  if (!offers.length) return null;
  return <fieldset className="order-bumps" disabled={disabled}>
    <legend>Complete your order</legend>
    {offers.map((offer) => <label className={`order-bump ${offer.selected ? "selected" : ""}`} key={offer.id}>
      <input type="checkbox" disabled={offer.available === false && !offer.selected} checked={offer.selected} onChange={(e) => onSelect(offer.id, e.target.checked)} />
      {offer.image_url && <img src={offer.image_url} alt="" loading="lazy" />}
      <span><strong>{offer.title}</strong><small>{offer.description}</small></span>
      <b>+{new Intl.NumberFormat(undefined, { style: "currency", currency: offer.currency }).format(offer.amount_cents / 100)}</b>
    </label>)}
    <small>Optional extras · charged once with this order</small>
  </fieldset>;
}
