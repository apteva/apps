// PriceCard — chat.message_attachment surface for a specific price.
// Useful when the agent prepares a quote ("Pro monthly · €29/mo").

import { useEffect, useState } from "react";

interface Props {
  appName: string;
  installId: number;
  projectId: string;
  price_id: number;
  preview?: boolean;
}

interface Price {
  id: number;
  product_id: number;
  nickname?: string;
  unit_amount_cents: number;
  currency: string;
  interval?: string;
  interval_count: number;
  trial_days: number;
  active: boolean;
  archived_at?: string;
}

interface Product {
  id: number;
  name: string;
  color?: string;
  type: string;
}

function fmtMoney(cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: (currency || "USD").toUpperCase(),
      currencyDisplay: "narrowSymbol",
    }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency}`;
  }
}

function fmtInterval(p: Price): string {
  if (!p.interval) return "one-time";
  return p.interval_count > 1
    ? `every ${p.interval_count} ${p.interval}s`
    : `/${p.interval}`;
}

export default function PriceCard(props: Props) {
  const [price, setPrice] = useState<Price | null>(null);
  const [product, setProduct] = useState<Product | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (props.preview) {
      setPrice({
        id: 0,
        product_id: 0,
        nickname: "Pro monthly",
        unit_amount_cents: 2900,
        currency: "EUR",
        interval: "month",
        interval_count: 1,
        trial_days: 14,
        active: true,
      });
      setProduct({ id: 0, name: "Apteva SaaS", color: "#3b82f6", type: "recurring" });
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const base = `/api/apps/catalog`;
        const q = `?project_id=${encodeURIComponent(props.projectId)}&install_id=${props.installId}`;
        const r = await fetch(`${base}/prices/${props.price_id}${q}`, {
          credentials: "same-origin",
        });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        const data = (await r.json()) as { price: Price };
        if (cancelled) return;
        setPrice(data.price);
        // Lazy-fetch product name for the header.
        const r2 = await fetch(`${base}/products/${data.price.product_id}${q}`, {
          credentials: "same-origin",
        });
        if (r2.ok) {
          const pd = (await r2.json()) as { product: Product };
          if (!cancelled) setProduct(pd.product);
        }
      } catch (err) {
        if (!cancelled) setError((err as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [props.price_id, props.projectId, props.installId, props.preview]);

  if (error) {
    return (
      <div className="border border-border rounded p-3 text-sm text-red">
        Price #{props.price_id}: {error}
      </div>
    );
  }
  if (!price) {
    return (
      <div className="border border-border rounded p-3 text-sm text-text-muted">
        Loading price…
      </div>
    );
  }
  return (
    <div className="border border-border rounded p-3 max-w-sm">
      {product && (
        <div className="flex items-center gap-2 mb-1">
          {product.color && (
            <span
              className="inline-block w-3 h-3 rounded-full"
              style={{ backgroundColor: product.color }}
            />
          )}
          <span className="text-sm text-text font-medium truncate">
            {product.name}
          </span>
        </div>
      )}
      {price.nickname && (
        <div className="text-xs text-text-muted mb-1">{price.nickname}</div>
      )}
      <div className="flex items-baseline gap-1 mt-1">
        <span className="text-lg font-semibold text-text">
          {fmtMoney(price.unit_amount_cents, price.currency)}
        </span>
        <span className="text-xs text-text-muted">{fmtInterval(price)}</span>
      </div>
      {price.trial_days > 0 && (
        <div className="text-xs text-text-dim mt-1">
          {price.trial_days}-day free trial
        </div>
      )}
      {!price.active && (
        <div className="text-xs text-yellow-500 mt-1">Inactive — not for new sales</div>
      )}
    </div>
  );
}
