// ProductCard — chat.message_attachment surface.
// Agents call respond(components=[{app:"catalog", name:"product-card",
// props:{product_id: N}}]) when they want to surface a product reference
// in conversation. Fetches /products/:id on mount; shows name, type,
// category, lowest active price.

import { useEffect, useState } from "react";

interface Props {
  appName: string;
  installId: number;
  projectId: string;
  product_id: number;
  preview?: boolean;
}

interface Price {
  id: number;
  unit_amount_cents: number;
  currency: string;
  interval?: string;
  interval_count: number;
  active: boolean;
  archived_at?: string;
}

interface Product {
  id: number;
  name: string;
  slug?: string;
  description?: string;
  type: string;
  category?: string;
  color?: string;
  archived_at?: string;
  prices?: Price[];
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

function lowestActivePrice(prices?: Price[]): Price | null {
  if (!prices) return null;
  const sellable = prices.filter((p) => p.active && !p.archived_at);
  if (sellable.length === 0) return null;
  return sellable.reduce((lo, p) =>
    p.unit_amount_cents < lo.unit_amount_cents ? p : lo,
  );
}

function typeLabel(t: string): string {
  if (t === "one_time") return "One-time";
  if (t === "recurring") return "Recurring";
  if (t === "service") return "Service";
  return t;
}

export default function ProductCard(props: Props) {
  const [product, setProduct] = useState<Product | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (props.preview) {
      setProduct({
        id: 0,
        name: "Apteva SaaS",
        type: "recurring",
        category: "subscription",
        color: "#3b82f6",
        description: "Continuous thinking platform — agent-orchestrated automation.",
        prices: [
          {
            id: 0,
            unit_amount_cents: 2900,
            currency: "EUR",
            interval: "month",
            interval_count: 1,
            active: true,
          },
        ],
      });
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const url = `/api/apps/catalog/products/${props.product_id}?project_id=${encodeURIComponent(props.projectId)}&install_id=${props.installId}`;
        const r = await fetch(url, { credentials: "same-origin" });
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        const data = (await r.json()) as { product: Product };
        if (!cancelled) setProduct(data.product);
      } catch (err) {
        if (!cancelled) setError((err as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [props.product_id, props.projectId, props.installId, props.preview]);

  if (error) {
    return (
      <div className="border border-border rounded p-3 text-sm text-red">
        Product #{props.product_id}: {error}
      </div>
    );
  }
  if (!product) {
    return (
      <div className="border border-border rounded p-3 text-sm text-text-muted">
        Loading product…
      </div>
    );
  }
  const price = lowestActivePrice(product.prices);
  return (
    <div className="border border-border rounded p-3 max-w-sm">
      <div className="flex items-center gap-2 mb-1">
        {product.color && (
          <span
            className="inline-block w-3 h-3 rounded-full"
            style={{ backgroundColor: product.color }}
          />
        )}
        <span className="text-sm font-medium text-text truncate">
          {product.name}
        </span>
        <span className="text-[10px] uppercase tracking-wide text-text-dim ml-auto">
          {typeLabel(product.type)}
        </span>
      </div>
      {product.description && (
        <p className="text-xs text-text-muted line-clamp-2 mb-2">
          {product.description}
        </p>
      )}
      {price ? (
        <div className="text-sm text-text">
          <span className="font-semibold">
            {fmtMoney(price.unit_amount_cents, price.currency)}
          </span>
          {price.interval && (
            <span className="text-text-muted text-xs ml-1">
              {price.interval_count > 1
                ? `every ${price.interval_count} ${price.interval}s`
                : `/${price.interval}`}
            </span>
          )}
        </div>
      ) : (
        <div className="text-xs text-text-dim">No active price yet.</div>
      )}
    </div>
  );
}
