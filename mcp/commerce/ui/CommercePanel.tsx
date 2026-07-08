import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/commerce";

interface NativePanelProps {
  projectId: string;
  installId: number;
}

interface Summary {
  stores: number;
  products: number;
  open_carts: number;
  sales: number;
}

interface Store {
  id: number;
  slug: string;
  name: string;
  status: string;
  default_currency: string;
  default_locale: string;
  timezone: string;
}

interface Variant {
  id: number;
  sku: string;
  title: string;
  price_cents: number;
  currency: string;
}

interface Product {
  id: number;
  store_id: number;
  handle: string;
  title: string;
  vendor: string;
  product_type: string;
  status: string;
  description_html: string;
  variants?: Variant[];
}

interface Sale {
  id: number;
  store_id: number;
  invoice_number: string;
  status: string;
  payment_status: string;
  fulfillment_status: string;
  total_cents: number;
  currency: string;
  customer_email: string;
  created_at: string;
}

type View = "catalog" | "stores" | "sales";

export default function CommercePanel({ projectId, installId }: NativePanelProps) {
  const [view, setView] = useState<View>("catalog");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [stores, setStores] = useState<Store[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [sales, setSales] = useState<Sale[]>([]);
  const [selectedStore, setSelectedStore] = useState("");
  const [status, setStatus] = useState("");
  const [storeForm, setStoreForm] = useState({ slug: "", name: "", default_currency: "USD" });

  const qs = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({ project_id: projectId, install_id: String(installId), ...extra }).toString(),
    [projectId, installId],
  );

  const api = useCallback(
    async <T,>(path: string, init?: RequestInit): Promise<T> => {
      const join = path.includes("?") ? "&" : "?";
      const res = await fetch(`${API}${path}${join}${qs()}`, {
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
        ...init,
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.error || `Request failed: ${res.status}`);
      }
      return res.json();
    },
    [qs],
  );

  const load = useCallback(async () => {
    const productArgs = selectedStore ? `?store_id=${encodeURIComponent(selectedStore)}` : "";
    const [sum, storeRows, productRows, saleRows] = await Promise.all([
      api<Summary>("/admin/summary"),
      api<Store[]>("/admin/stores"),
      api<Product[]>(`/admin/products${productArgs}`),
      api<Sale[]>("/admin/sales?limit=25"),
    ]);
    setSummary(sum);
    setStores(storeRows || []);
    setProducts(productRows || []);
    setSales(saleRows || []);
  }, [api, selectedStore]);

  useEffect(() => {
    load().catch((err) => setStatus(err.message));
  }, [load]);

  const totals = useMemo(
    () => summary || { stores: stores.length, products: products.length, open_carts: 0, sales: sales.length },
    [summary, stores.length, products.length, sales.length],
  );

  async function createStore() {
    if (!storeForm.name.trim()) return;
    await api<Store>("/admin/stores", { method: "POST", body: JSON.stringify(storeForm) });
    setStoreForm({ slug: "", name: "", default_currency: "USD" });
    setStatus("Store created");
    await load();
  }

  return (
    <div className="h-full bg-bg text-text flex flex-col">
      <header className="border-b border-border px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Commerce</h1>
          <p className="text-sm text-text-muted">Stores, products, carts, checkout orchestration, and sales.</p>
        </div>
        <div className="flex rounded-md border border-border overflow-hidden text-sm">
          {(["catalog", "stores", "sales"] as View[]).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={`px-3 py-1.5 capitalize ${view === v ? "bg-accent text-bg" : "hover:bg-bg-input/50"}`}
            >
              {v}
            </button>
          ))}
        </div>
      </header>

      <main className="flex-1 overflow-auto p-5 space-y-5">
        <section className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <Metric label="Stores" value={totals.stores} />
          <Metric label="Products" value={totals.products} />
          <Metric label="Open carts" value={totals.open_carts} />
          <Metric label="Sales" value={totals.sales} />
        </section>

        {status && <div className="text-xs text-text-muted">{status}</div>}

        {view === "catalog" && (
          <section className="space-y-3">
            <div className="flex flex-wrap items-center gap-3">
              <select
                value={selectedStore}
                onChange={(e) => setSelectedStore(e.target.value)}
                className="rounded-md border border-border bg-bg-input px-3 py-2 text-sm"
              >
                <option value="">All stores</option>
                {stores.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name}
                  </option>
                ))}
              </select>
              <button onClick={() => load()} className="rounded-md border border-border px-3 py-2 text-sm hover:bg-bg-input">
                Refresh
              </button>
            </div>
            <div className="overflow-hidden rounded-md border border-border">
              <table className="w-full text-sm">
                <thead className="bg-bg-subtle text-text-muted">
                  <tr>
                    <th className="text-left px-3 py-2">Product</th>
                    <th className="text-left px-3 py-2">Store</th>
                    <th className="text-left px-3 py-2">Status</th>
                    <th className="text-right px-3 py-2">Variants</th>
                  </tr>
                </thead>
                <tbody>
                  {products.map((p) => (
                    <tr key={p.id} className="border-t border-border">
                      <td className="px-3 py-2">
                        <div className="font-medium">{p.title}</div>
                        <div className="text-xs text-text-muted">/{p.handle}</div>
                      </td>
                      <td className="px-3 py-2">{stores.find((s) => s.id === p.store_id)?.name || p.store_id}</td>
                      <td className="px-3 py-2"><Status value={p.status} /></td>
                      <td className="px-3 py-2 text-right">{p.variants?.length || 0}</td>
                    </tr>
                  ))}
                  {products.length === 0 && <EmptyRow cols={4} label="No storefront products yet" />}
                </tbody>
              </table>
            </div>
          </section>
        )}

        {view === "stores" && (
          <div className="grid xl:grid-cols-[minmax(0,1fr)_340px] gap-5">
            <section className="overflow-hidden rounded-md border border-border">
              <table className="w-full text-sm">
                <thead className="bg-bg-subtle text-text-muted">
                  <tr>
                    <th className="text-left px-3 py-2">Store</th>
                    <th className="text-left px-3 py-2">Slug</th>
                    <th className="text-left px-3 py-2">Currency</th>
                    <th className="text-left px-3 py-2">Status</th>
                  </tr>
                </thead>
                <tbody>
                  {stores.map((s) => (
                    <tr key={s.id} className="border-t border-border">
                      <td className="px-3 py-2 font-medium">{s.name}</td>
                      <td className="px-3 py-2">/{s.slug}</td>
                      <td className="px-3 py-2">{s.default_currency}</td>
                      <td className="px-3 py-2"><Status value={s.status} /></td>
                    </tr>
                  ))}
                  {stores.length === 0 && <EmptyRow cols={4} label="No stores yet" />}
                </tbody>
              </table>
            </section>
            <section className="rounded-md border border-border p-4 space-y-3">
              <h2 className="text-sm font-semibold">New store</h2>
              <input className="w-full rounded-md border border-border bg-bg-input px-3 py-2 text-sm" placeholder="Name" value={storeForm.name} onChange={(e) => setStoreForm({ ...storeForm, name: e.target.value })} />
              <input className="w-full rounded-md border border-border bg-bg-input px-3 py-2 text-sm" placeholder="Slug" value={storeForm.slug} onChange={(e) => setStoreForm({ ...storeForm, slug: e.target.value })} />
              <input className="w-full rounded-md border border-border bg-bg-input px-3 py-2 text-sm" placeholder="Currency" value={storeForm.default_currency} onChange={(e) => setStoreForm({ ...storeForm, default_currency: e.target.value.toUpperCase() })} />
              <button onClick={createStore} className="w-full rounded-md bg-accent text-bg px-3 py-2 text-sm font-medium">
                Create store
              </button>
            </section>
          </div>
        )}

        {view === "sales" && (
          <section className="overflow-hidden rounded-md border border-border">
            <table className="w-full text-sm">
              <thead className="bg-bg-subtle text-text-muted">
                <tr>
                  <th className="text-left px-3 py-2">Sale</th>
                  <th className="text-left px-3 py-2">Customer</th>
                  <th className="text-left px-3 py-2">Payment</th>
                  <th className="text-left px-3 py-2">Fulfillment</th>
                  <th className="text-right px-3 py-2">Total</th>
                </tr>
              </thead>
              <tbody>
                {sales.map((s) => (
                  <tr key={s.id} className="border-t border-border">
                    <td className="px-3 py-2">
                      <div className="font-medium">#{s.id}</div>
                      <div className="text-xs text-text-muted">{s.invoice_number || fmtDate(s.created_at)}</div>
                    </td>
                    <td className="px-3 py-2">{s.customer_email || "Guest"}</td>
                    <td className="px-3 py-2"><Status value={s.payment_status} /></td>
                    <td className="px-3 py-2"><Status value={s.fulfillment_status} /></td>
                    <td className="px-3 py-2 text-right">{fmtMoney(s.total_cents, s.currency)}</td>
                  </tr>
                ))}
                {sales.length === 0 && <EmptyRow cols={5} label="No sales yet" />}
              </tbody>
            </table>
          </section>
        )}
      </main>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="rounded-md border border-border p-3">
      <div className="text-xs text-text-muted">{label}</div>
      <div className="text-xl font-semibold">{value}</div>
    </div>
  );
}

function Status({ value }: { value: string }) {
  return <span className="inline-flex rounded px-2 py-0.5 text-xs bg-bg-input text-text">{value || "unknown"}</span>;
}

function EmptyRow({ cols, label }: { cols: number; label: string }) {
  return (
    <tr>
      <td colSpan={cols} className="px-3 py-8 text-center text-sm text-text-muted">
        {label}
      </td>
    </tr>
  );
}

function fmtMoney(cents: number, currency: string) {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: (currency || "USD").toUpperCase(), currencyDisplay: "narrowSymbol" }).format((cents || 0) / 100);
  } catch {
    return `${((cents || 0) / 100).toFixed(2)} ${currency || "USD"}`;
  }
}

function fmtDate(value: string) {
  if (!value) return "";
  try {
    return new Date(value).toLocaleString();
  } catch {
    return value;
  }
}
