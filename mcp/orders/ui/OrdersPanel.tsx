import { useCallback, useEffect, useState } from "react";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface OrderItem {
  id: number;
  title: string;
  sku?: string;
  quantity: number;
  unit_amount_cents: number;
  currency: string;
}

interface FulfillmentItem {
  order_item_id: number;
  quantity: number;
}

interface Fulfillment {
  id: number;
  provider: string;
  provider_order_id?: string;
  status: string;
  error?: string;
  items?: FulfillmentItem[];
}

interface Shipment {
  id: number;
  provider: string;
  carrier?: string;
  tracking_number?: string;
  tracking_url?: string;
  status: string;
}

interface Order {
  id: number;
  order_number: string;
  source: string;
  source_ref?: string;
  customer_email?: string;
  customer_name?: string;
  currency: string;
  total_cents: number;
  payment_status: string;
  order_status: string;
  fulfillment_status: string;
  created_at?: string;
  updated_at?: string;
  items?: OrderItem[];
  fulfillments?: Fulfillment[];
  shipments?: Shipment[];
  returns?: Array<Record<string, unknown>>;
  events?: Array<Record<string, unknown>>;
}

const API = "/api/apps/orders";

function money(cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: (currency || "USD").toUpperCase(),
      currencyDisplay: "narrowSymbol",
    }).format((cents || 0) / 100);
  } catch {
    return `${((cents || 0) / 100).toFixed(2)} ${currency}`;
  }
}

function badgeTone(value: string): string {
  if (["paid", "fulfilled", "delivered", "accepted"].includes(value)) {
    return "border-green-500/30 text-green-500 bg-green-500/10";
  }
  if (["cancelled", "failed", "returned", "refunded"].includes(value)) {
    return "border-red-500/30 text-red-500 bg-red-500/10";
  }
  if (["submitted", "fulfilling", "queued", "ready_to_fulfill"].includes(value)) {
    return "border-blue-500/30 text-blue-500 bg-blue-500/10";
  }
  return "border-border text-text-muted bg-bg-input";
}

function StatusBadge({ value }: { value: string }) {
  return (
    <span className={`inline-flex items-center rounded border px-2 py-0.5 text-xs ${badgeTone(value)}`}>
      {value.replaceAll("_", " ")}
    </span>
  );
}

export default function OrdersPanel({ projectId, installId }: NativePanelProps) {
  const [orders, setOrders] = useState<Order[]>([]);
  const [selected, setSelected] = useState<Order | null>(null);
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const qs = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
    [projectId, installId],
  );

  const loadOrders = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const extra: Record<string, string> = { limit: "100" };
      if (q.trim()) extra.q = q.trim();
      if (status) extra.fulfillment_status = status;
      const res = await fetch(`${API}/orders?${qs(extra)}`, { credentials: "include" });
      if (!res.ok) throw new Error(await res.text());
      const data = (await res.json()) as { orders: Order[] };
      setOrders(data.orders || []);
      if (!selected && data.orders?.[0]) setSelected(data.orders[0]);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  }, [q, qs, selected, status]);

  const loadOrder = useCallback(
    async (id: number) => {
      setError("");
      try {
        const res = await fetch(`${API}/orders/${id}?${qs()}`, { credentials: "include" });
        if (!res.ok) throw new Error(await res.text());
        const data = (await res.json()) as { order: Order };
        setSelected(data.order);
      } catch (err) {
        setError((err as Error).message);
      }
    },
    [qs],
  );

  useEffect(() => {
    loadOrders();
  }, [loadOrders]);

  return (
    <div className="h-full min-h-0 flex flex-col bg-bg text-text">
      <header className="border-b border-border px-4 py-3 flex items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-semibold">Orders</h1>
          <div className="text-xs text-text-muted">Physical fulfillment, shipments, returns</div>
        </div>
        <button
          type="button"
          onClick={loadOrders}
          className="px-3 py-1.5 rounded border border-border text-sm hover:bg-bg-input"
        >
          {loading ? "Loading..." : "Refresh"}
        </button>
      </header>

      <div className="border-b border-border px-4 py-2 flex items-center gap-2">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") loadOrders();
          }}
          placeholder="Search orders"
          className="w-64 max-w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
        />
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
        >
          <option value="">All fulfillment</option>
          <option value="unsubmitted">Unsubmitted</option>
          <option value="queued">Queued</option>
          <option value="submitted">Submitted</option>
          <option value="shipped">Shipped</option>
          <option value="delivered">Delivered</option>
          <option value="failed">Failed</option>
        </select>
        <button
          type="button"
          onClick={loadOrders}
          className="px-3 py-1 rounded border border-border text-sm hover:bg-bg-input"
        >
          Apply
        </button>
      </div>

      {error && <div className="m-4 rounded border border-red-500/30 bg-red-500/10 p-2 text-sm text-red-500">{error}</div>}

      <main className="min-h-0 flex-1 grid grid-cols-[minmax(280px,380px)_1fr]">
        <aside className="min-h-0 overflow-auto border-r border-border">
          {orders.length === 0 && !loading && (
            <div className="p-4 text-sm text-text-muted">No orders yet.</div>
          )}
          {orders.map((order) => (
            <button
              key={order.id}
              type="button"
              onClick={() => loadOrder(order.id)}
              className={`w-full text-left px-4 py-3 border-b border-border hover:bg-bg-input ${
                selected?.id === order.id ? "bg-bg-input" : ""
              }`}
            >
              <div className="flex items-center justify-between gap-2">
                <div className="font-medium truncate">{order.order_number}</div>
                <div className="text-sm tabular-nums">{money(order.total_cents, order.currency)}</div>
              </div>
              <div className="mt-1 text-xs text-text-muted truncate">
                {order.customer_name || order.customer_email || order.source}
              </div>
              <div className="mt-2 flex flex-wrap gap-1">
                <StatusBadge value={order.payment_status} />
                <StatusBadge value={order.fulfillment_status} />
              </div>
            </button>
          ))}
        </aside>

        <section className="min-h-0 overflow-auto p-4">
          {!selected ? (
            <div className="text-sm text-text-muted">Select an order.</div>
          ) : (
            <div className="space-y-4">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <h2 className="text-xl font-semibold">{selected.order_number}</h2>
                  <div className="text-sm text-text-muted">
                    {selected.source}
                    {selected.source_ref ? ` / ${selected.source_ref}` : ""}
                  </div>
                </div>
                <div className="text-right">
                  <div className="text-2xl font-semibold tabular-nums">
                    {money(selected.total_cents, selected.currency)}
                  </div>
                  <div className="mt-2 flex justify-end gap-1">
                    <StatusBadge value={selected.order_status} />
                    <StatusBadge value={selected.fulfillment_status} />
                  </div>
                </div>
              </div>

              <section className="border border-border rounded p-3">
                <h3 className="text-sm font-medium mb-2">Customer</h3>
                <div className="text-sm">{selected.customer_name || "Unnamed"}</div>
                <div className="text-sm text-text-muted">{selected.customer_email || "No email"}</div>
              </section>

              <section className="border border-border rounded overflow-hidden">
                <h3 className="text-sm font-medium p-3 border-b border-border">Items</h3>
                <table className="w-full text-sm">
                  <tbody>
                    {(selected.items || []).map((item) => (
                      <tr key={item.id} className="border-b border-border last:border-b-0">
                        <td className="p-3">
                          <div className="font-medium">{item.title}</div>
                          {item.sku && <div className="text-xs text-text-muted font-mono">{item.sku}</div>}
                        </td>
                        <td className="p-3 text-right tabular-nums">{item.quantity}</td>
                        <td className="p-3 text-right tabular-nums">
                          {money(item.unit_amount_cents, item.currency)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </section>

              <div className="grid grid-cols-3 gap-3">
                <MiniStat label="Fulfillments" value={selected.fulfillments?.length || 0} />
                <MiniStat label="Shipments" value={selected.shipments?.length || 0} />
                <MiniStat label="Returns" value={selected.returns?.length || 0} />
              </div>

              {(selected.fulfillments || []).length > 0 && <section className="border border-border rounded overflow-hidden">
                <h3 className="text-sm font-medium p-3 border-b border-border">Fulfillments</h3>
                <div className="divide-y divide-border">{selected.fulfillments!.map((fulfillment) => <div key={fulfillment.id} className="p-3 flex items-start justify-between gap-3 text-sm"><div className="min-w-0"><div className="font-medium capitalize">{fulfillment.provider} #{fulfillment.id}</div><div className="text-xs text-text-muted">{fulfillment.items?.length || 0} order line{fulfillment.items?.length === 1 ? "" : "s"}{fulfillment.provider_order_id ? ` · provider order ${fulfillment.provider_order_id}` : ""}</div>{fulfillment.error && <div className="mt-1 text-xs text-red-500">{fulfillment.error}</div>}</div><StatusBadge value={fulfillment.status} /></div>)}</div>
              </section>}

              {(selected.shipments || []).length > 0 && <section className="border border-border rounded overflow-hidden">
                <h3 className="text-sm font-medium p-3 border-b border-border">Shipments</h3>
                <div className="divide-y divide-border">{selected.shipments!.map((shipment) => <div key={shipment.id} className="p-3 flex items-start justify-between gap-3 text-sm"><div className="min-w-0"><div className="font-medium">{shipment.carrier || shipment.provider}</div>{shipment.tracking_url ? <a href={shipment.tracking_url} target="_blank" rel="noreferrer" className="text-xs text-blue-500 hover:underline">{shipment.tracking_number || "Open tracking"}</a> : <div className="text-xs text-text-muted">{shipment.tracking_number || "Tracking pending"}</div>}</div><StatusBadge value={shipment.status} /></div>)}</div>
              </section>}
            </div>
          )}
        </section>
      </main>
    </div>
  );
}

function MiniStat({ label, value }: { label: string; value: number }) {
  return (
    <div className="border border-border rounded p-3">
      <div className="text-xs uppercase text-text-muted">{label}</div>
      <div className="mt-1 text-2xl font-semibold tabular-nums">{value}</div>
    </div>
  );
}
