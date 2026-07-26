import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

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
  public_base_url: string;
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
  inventory_item_id?: number;
  catalog_price_id?: number;
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

interface Collection {
  id: number;
  store_id: number;
  handle: string;
  title: string;
  description_html: string;
  status: string;
  products?: Product[];
}

interface CartItem {
  id: number;
  title_snapshot: string;
  sku: string;
  quantity: number;
  unit_amount_cents: number;
  currency: string;
}

interface Cart {
  id: number;
  store_id: number;
  status: string;
  total_cents: number;
  currency: string;
  items?: CartItem[];
  updated_at: string;
}

interface SaleItem {
  id: number;
  title_snapshot: string;
  sku: string;
  quantity: number;
  unit_amount_cents: number;
  currency: string;
}

interface Sale {
  id: number;
  store_id: number;
  invoice_number: string;
  order_id?: number;
  status: string;
  payment_status: string;
  fulfillment_status: string;
  processing_error?: string;
  total_cents: number;
  currency: string;
  customer_email: string;
  customer_name: string;
  shipping_address?: Record<string, unknown>;
  items?: SaleItem[];
  created_at: string;
}

interface ProviderPolicy {
  store_id: number;
  connection_id: number;
  provider_slug: string;
  enabled: boolean;
  fulfillment_mode: "review" | "automatic";
  margin_bps: number;
  settings?: Record<string, unknown>;
  connected: boolean;
  is_default?: boolean;
}

interface ProviderVariant {
  id: string;
  sku: string;
  title: string;
  cost_cents: number;
  suggested_price_cents?: number;
  currency: string;
  available: boolean;
  available_quantity?: number;
}

interface ProviderProduct {
  id: string;
  title: string;
  vendor?: string;
  image_url?: string;
  variants?: ProviderVariant[];
}

interface VariantSource {
  id: number;
  store_id: number;
  variant_id: number;
  provider_slug: string;
  provider_sku: string;
  unit_cost_cents: number;
  currency: string;
  availability: string;
  available_quantity?: number;
  last_synced_at?: string;
}

interface DispatchJob {
  id: number;
  store_id: number;
  sale_id: number;
  provider_slug: string;
  status: string;
  external_order_id?: string;
  error?: string;
  updated_at: string;
}

interface StorefrontStatus {
  store_id: number;
  configured: boolean;
  content_ready: boolean;
  site_id?: number;
  site_slug?: string;
  hostname?: string;
  extension_key?: string;
  version?: string;
  preview_url?: string;
  error?: string;
}

type View = "products" | "collections" | "providers" | "storefront" | "stores" | "carts" | "sales";
type Notice = { tone: "success" | "error"; message: string } | null;

const emptyProduct = { store_id: "", title: "", handle: "", description_html: "", vendor: "", product_type: "", price: "", currency: "USD", sku: "" };
const emptyStore = { name: "", slug: "", default_currency: "USD", default_locale: "en", timezone: "UTC" };
const emptyCollection = { store_id: "", title: "", handle: "", description_html: "" };

export default function CommercePanel({ projectId, installId }: NativePanelProps) {
  const [view, setView] = useState<View>("products");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [stores, setStores] = useState<Store[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [collections, setCollections] = useState<Collection[]>([]);
  const [carts, setCarts] = useState<Cart[]>([]);
  const [sales, setSales] = useState<Sale[]>([]);
  const [providers, setProviders] = useState<ProviderPolicy[]>([]);
  const [sources, setSources] = useState<VariantSource[]>([]);
  const [dispatches, setDispatches] = useState<DispatchJob[]>([]);
  const [storefront, setStorefront] = useState<StorefrontStatus | null>(null);
  const [providerDrafts, setProviderDrafts] = useState<Record<number, ProviderPolicy>>({});
  const [catalogProvider, setCatalogProvider] = useState<ProviderPolicy | null>(null);
  const [providerProducts, setProviderProducts] = useState<ProviderProduct[]>([]);
  const [selectedProviderProduct, setSelectedProviderProduct] = useState<ProviderProduct | null>(null);
  const [selectedStore, setSelectedStore] = useState("");
  const [selectedProduct, setSelectedProduct] = useState<Product | null>(null);
  const [selectedCollection, setSelectedCollection] = useState<Collection | null>(null);
  const [selectedSale, setSelectedSale] = useState<Sale | null>(null);
  const [productForm, setProductForm] = useState(emptyProduct);
  const [storeForm, setStoreForm] = useState(emptyStore);
  const [collectionForm, setCollectionForm] = useState(emptyCollection);
  const [variantForm, setVariantForm] = useState({ title: "", sku: "", price: "", currency: "USD", inventory_item_id: "" });
  const [collectionProductId, setCollectionProductId] = useState("");
  const [editingProduct, setEditingProduct] = useState(false);
  const [editingStore, setEditingStore] = useState<Store | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [notice, setNotice] = useState<Notice>(null);
  const loadSequence = useRef(0);

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
    const sequence = ++loadSequence.current;
    setLoading(true);
    const filter = selectedStore ? `?store_id=${encodeURIComponent(selectedStore)}` : "";
    try {
      const [nextSummary, nextStores, nextProducts, nextCollections, nextCarts, nextSales, nextProviders, nextSources, nextDispatches, nextStorefront] = await Promise.all([
        api<Summary>("/admin/summary"),
        api<Store[]>("/admin/stores"),
        api<Product[]>(`/admin/products${filter}`),
        api<Collection[]>(`/admin/collections${filter}`),
        api<Cart[]>(`/admin/carts${filter}`),
        api<Sale[]>(`/admin/sales${filter ? `${filter}&limit=50` : "?limit=50"}`),
        api<ProviderPolicy[]>(`/admin/providers${filter}`),
        api<VariantSource[]>(`/admin/variant-sources${filter}`),
        api<DispatchJob[]>(`/admin/dispatches${filter ? `${filter}&limit=100` : "?limit=100"}`),
        selectedStore ? api<StorefrontStatus>(`/admin/storefront?store_id=${encodeURIComponent(selectedStore)}`) : Promise.resolve(null),
      ]);
      if (sequence !== loadSequence.current) return;
      setSummary(nextSummary);
      setStores(nextStores || []);
      setProducts(nextProducts || []);
      setCollections(nextCollections || []);
      setCarts(nextCarts || []);
      setSales(nextSales || []);
      setProviders(nextProviders || []);
      setSources(nextSources || []);
      setDispatches(nextDispatches || []);
      setStorefront(nextStorefront);
      setProviderDrafts(Object.fromEntries((nextProviders || []).map((provider) => [provider.connection_id, {
        ...provider, settings: { ...(provider.settings || {}) },
      }])));
    } catch (error) {
      if (sequence === loadSequence.current) setNotice({ tone: "error", message: errorMessage(error) });
    } finally {
      if (sequence === loadSequence.current) setLoading(false);
    }
  }, [api, selectedStore]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!productForm.store_id && stores.length === 1) setProductForm((current) => ({ ...current, store_id: String(stores[0].id), currency: stores[0].default_currency }));
    if (!collectionForm.store_id && stores.length === 1) setCollectionForm((current) => ({ ...current, store_id: String(stores[0].id) }));
  }, [stores, productForm.store_id, collectionForm.store_id]);

  const totals = summary || { stores: stores.length, products: products.length, open_carts: 0, sales: sales.length };
  const currentStore = useMemo(() => stores.find((store) => String(store.id) === selectedStore), [stores, selectedStore]);

  async function run(action: () => Promise<void>, success: string) {
    setBusy(true);
    setNotice(null);
    try {
      await action();
      setNotice({ tone: "success", message: success });
      await load();
    } catch (error) {
      setNotice({ tone: "error", message: errorMessage(error) });
    } finally {
      setBusy(false);
    }
  }

  function beginProduct(product?: Product) {
    if (product) {
      setSelectedProduct(product);
      setProductForm({
        store_id: String(product.store_id), title: product.title, handle: product.handle,
        description_html: product.description_html, vendor: product.vendor, product_type: product.product_type,
        price: "", currency: product.variants?.[0]?.currency || "USD", sku: "",
      });
    } else {
      const store = currentStore || (stores.length === 1 ? stores[0] : undefined);
      setSelectedProduct(null);
      setProductForm({ ...emptyProduct, store_id: store ? String(store.id) : "", currency: store?.default_currency || "USD" });
    }
    setEditingProduct(true);
  }

  async function saveProduct() {
    if (!productForm.title.trim() || !productForm.store_id) throw new Error("Store and title are required");
    if (selectedProduct) {
      const updated = await api<Product>(`/admin/products/${selectedProduct.id}`, {
        method: "PATCH",
        body: JSON.stringify({
          title: productForm.title, handle: productForm.handle, description_html: productForm.description_html,
          vendor: productForm.vendor, product_type: productForm.product_type,
        }),
      });
      setSelectedProduct(updated);
    } else {
      const payload: Record<string, unknown> = {
        store_id: Number(productForm.store_id), title: productForm.title, handle: productForm.handle,
        description_html: productForm.description_html, vendor: productForm.vendor, product_type: productForm.product_type,
        currency: productForm.currency, sku: productForm.sku,
      };
      if (productForm.price) payload.price_cents = moneyToCents(productForm.price);
      const created = await api<Product>("/admin/products", { method: "POST", body: JSON.stringify(payload) });
      setSelectedProduct(created);
    }
    setEditingProduct(false);
  }

  async function loadProduct(id: number) {
    try {
      const product = await api<Product>(`/admin/products/${id}`);
      beginProduct(product);
    } catch (error) {
      setNotice({ tone: "error", message: errorMessage(error) });
    }
  }

  async function addVariant() {
    if (!selectedProduct || !variantForm.price) throw new Error("Variant price is required");
    await api<Variant>(`/admin/products/${selectedProduct.id}/variants`, {
      method: "POST",
      body: JSON.stringify({
        title: variantForm.title || "Default", sku: variantForm.sku,
        price_cents: moneyToCents(variantForm.price), currency: variantForm.currency,
        inventory_item_id: variantForm.inventory_item_id ? Number(variantForm.inventory_item_id) : undefined,
      }),
    });
    const refreshed = await api<Product>(`/admin/products/${selectedProduct.id}`);
    setSelectedProduct(refreshed);
    setVariantForm({ title: "", sku: "", price: "", currency: variantForm.currency, inventory_item_id: "" });
  }

  async function openCollection(id: number) {
    try {
      setSelectedCollection(await api<Collection>(`/admin/collections/${id}`));
      setCollectionProductId("");
    } catch (error) {
      setNotice({ tone: "error", message: errorMessage(error) });
    }
  }

  async function openSale(id: number) {
    try {
      setSelectedSale(await api<Sale>(`/admin/sales/${id}`));
    } catch (error) {
      setNotice({ tone: "error", message: errorMessage(error) });
    }
  }

  async function retrySale() {
    if (!selectedSale) return;
    const refreshed = await api<Sale>(`/admin/sales/${selectedSale.id}/retry`, { method: "POST" });
    setSelectedSale(refreshed);
  }

  function updateProviderDraft(connectionId: number, patch: Partial<ProviderPolicy>) {
    setProviderDrafts((current) => ({
      ...current,
      [connectionId]: { ...(current[connectionId] || providers.find((row) => row.connection_id === connectionId)!), ...patch },
    }));
  }

  function updateProviderSetting(connectionId: number, key: string, value: unknown) {
    const current = providerDrafts[connectionId] || providers.find((row) => row.connection_id === connectionId);
    if (!current) return;
    updateProviderDraft(connectionId, { settings: { ...(current.settings || {}), [key]: value } });
  }

  async function saveProvider(provider: ProviderPolicy) {
    if (!selectedStore) throw new Error("Select a store before changing provider policy");
    const draft = providerDrafts[provider.connection_id] || provider;
    await api("/admin/providers", {
      method: "PATCH",
      body: JSON.stringify({
        store_id: Number(selectedStore), connection_id: provider.connection_id, enabled: draft.enabled,
        fulfillment_mode: draft.fulfillment_mode, margin_bps: draft.margin_bps, settings: draft.settings || {},
      }),
    });
  }

  async function browseProvider(provider: ProviderPolicy) {
    if (!selectedStore) throw new Error("Select a store before browsing a provider");
    const result = await api<{ products: ProviderProduct[] }>("/admin/provider-catalog", {
      method: "POST",
      body: JSON.stringify({ store_id: Number(selectedStore), connection_id: provider.connection_id }),
    });
    setCatalogProvider(provider);
    setProviderProducts(result.products || []);
    setSelectedProviderProduct(null);
  }

  async function openProviderProduct(product: ProviderProduct) {
    if (!catalogProvider || !selectedStore) return;
    const result = await api<{ product: ProviderProduct }>("/admin/provider-products", {
      method: "POST",
      body: JSON.stringify({
        store_id: Number(selectedStore), connection_id: catalogProvider.connection_id,
        external_product_id: product.id,
      }),
    });
    setSelectedProviderProduct(result.product);
  }

  async function importProviderProduct() {
    if (!catalogProvider || !selectedProviderProduct || !selectedStore) return;
    await api("/admin/provider-products/import", {
      method: "POST",
      body: JSON.stringify({
        store_id: Number(selectedStore), connection_id: catalogProvider.connection_id,
        product: selectedProviderProduct,
      }),
    });
    setSelectedProviderProduct(null);
  }

  async function syncSources() {
    await api("/admin/variant-sources", {
      method: "POST",
      body: JSON.stringify({ store_id: selectedStore ? Number(selectedStore) : undefined, limit: 100 }),
    });
  }

  async function submitDispatch(id: number) {
    await api(`/admin/dispatches/${id}/submit`, { method: "POST" });
  }

  async function configureStorefront() {
    if (!selectedStore) throw new Error("Select a store before configuring its storefront");
    const status = await api<StorefrontStatus>("/admin/storefront", {
      method: "POST",
      body: JSON.stringify({ store_id: Number(selectedStore) }),
    });
    setStorefront(status);
  }

  const tabs: Array<{ id: View; label: string }> = [
    { id: "products", label: "Products" }, { id: "collections", label: "Collections" },
    { id: "providers", label: "Providers" }, { id: "storefront", label: "Storefront" },
    { id: "stores", label: "Stores" },
    { id: "carts", label: "Carts" }, { id: "sales", label: "Sales" },
  ];

  return (
    <div className="h-full bg-bg text-text flex flex-col min-w-0">
      <header className="border-b border-border px-4 md:px-5 py-3 flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-lg font-semibold">Commerce</h1>
        <div className="flex items-center gap-2 min-w-0">
          <select value={selectedStore} onChange={(event) => setSelectedStore(event.target.value)} className="max-w-44 rounded-md border border-border bg-bg-input px-2.5 py-1.5 text-sm">
            <option value="">All stores</option>
            {stores.map((store) => <option key={store.id} value={store.id}>{store.name}</option>)}
          </select>
          <button type="button" onClick={() => void load()} disabled={loading} className="h-8 px-3 rounded-md border border-border text-sm hover:bg-bg-input disabled:opacity-50">Refresh</button>
        </div>
        <nav className="basis-full flex overflow-x-auto border-t border-border -mx-4 md:-mx-5 px-4 md:px-5 pt-2 gap-1" aria-label="Commerce sections">
          {tabs.map((tab) => (
            <button key={tab.id} type="button" onClick={() => setView(tab.id)} className={`h-8 px-3 rounded-md text-sm whitespace-nowrap ${view === tab.id ? "bg-text text-bg" : "text-text-muted hover:bg-bg-input hover:text-text"}`}>{tab.label}</button>
          ))}
        </nav>
      </header>
      {loading && <div className="h-0.5 shrink-0 bg-text/40 animate-pulse" role="progressbar" aria-label="Loading commerce data" />}

      <main className="flex-1 overflow-auto p-4 md:p-5 space-y-4">
        <section className="grid grid-cols-2 lg:grid-cols-4 border border-border rounded-md overflow-hidden">
          <Metric label="Stores" value={totals.stores} />
          <Metric label="Products" value={totals.products} />
          <Metric label="Open carts" value={totals.open_carts} />
          <Metric label="Sales" value={totals.sales} />
        </section>

        {notice && <div role="status" aria-live="polite" className={`border px-3 py-2 rounded-md text-sm ${notice.tone === "error" ? "border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-300" : "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"}`}>{notice.message}</div>}

        {view === "products" && (
          <section className="space-y-3">
            <div className="flex items-center justify-between gap-3">
              <h2 className="text-sm font-semibold">Products</h2>
              <button type="button" onClick={() => beginProduct()} disabled={stores.length === 0} className="h-8 px-3 rounded-md bg-accent text-bg text-sm font-medium disabled:opacity-50">New product</button>
            </div>
            <DataTable headers={["Product", "Store", "Price", "Status", "Variants"]} empty={products.length === 0} emptyLabel="No products">
              {products.map((product) => (
                <tr key={product.id} onClick={() => void loadProduct(product.id)} className="border-t border-border cursor-pointer hover:bg-bg-input/50">
                  <td className="px-3 py-2.5"><div className="font-medium">{product.title}</div><div className="text-xs text-text-muted">/{product.handle}</div></td>
                  <td className="px-3 py-2.5 whitespace-nowrap">{storeName(stores, product.store_id)}</td>
                  <td className="px-3 py-2.5 whitespace-nowrap">{product.variants?.[0] ? fmtMoney(product.variants[0].price_cents, product.variants[0].currency) : "No price"}</td>
                  <td className="px-3 py-2.5"><Status value={product.status} /></td>
                  <td className="px-3 py-2.5 text-right">{product.variants?.length || 0}</td>
                </tr>
              ))}
            </DataTable>
          </section>
        )}

        {view === "collections" && (
          <section className="grid xl:grid-cols-[minmax(0,1fr)_360px] gap-5">
            <div className="space-y-3 min-w-0">
              <h2 className="text-sm font-semibold">Collections</h2>
              <DataTable headers={["Collection", "Store", "Status"]} empty={collections.length === 0} emptyLabel="No collections">
                {collections.map((collection) => (
                  <tr key={collection.id} onClick={() => void openCollection(collection.id)} className="border-t border-border cursor-pointer hover:bg-bg-input/50">
                    <td className="px-3 py-2.5"><div className="font-medium">{collection.title}</div><div className="text-xs text-text-muted">/{collection.handle}</div></td>
                    <td className="px-3 py-2.5">{storeName(stores, collection.store_id)}</td>
                    <td className="px-3 py-2.5"><Status value={collection.status} /></td>
                  </tr>
                ))}
              </DataTable>
            </div>
            <aside className="xl:border-l xl:border-border xl:pl-5 space-y-4">
              {selectedCollection ? (
                <>
                  <div className="flex items-center justify-between"><h2 className="text-sm font-semibold">{selectedCollection.title}</h2><button type="button" onClick={() => setSelectedCollection(null)} className="text-sm text-text-muted hover:text-text">Close</button></div>
                  <div className="space-y-2">
                    {(selectedCollection.products || []).map((product) => <div key={product.id} className="flex items-center justify-between border-b border-border py-2 text-sm"><span>{product.title}</span><button type="button" disabled={busy} onClick={() => void run(async () => { await api(`/admin/collections/${selectedCollection.id}/products`, { method: "DELETE", body: JSON.stringify({ listing_id: product.id }) }); await openCollection(selectedCollection.id); }, "Product removed") } className="text-red-600 hover:underline">Remove</button></div>)}
                    {(selectedCollection.products || []).length === 0 && <p className="text-sm text-text-muted">No products</p>}
                  </div>
                  <div className="flex gap-2">
                    <select value={collectionProductId} onChange={(event) => setCollectionProductId(event.target.value)} className="min-w-0 flex-1 rounded-md border border-border bg-bg-input px-3 py-2 text-sm">
                      <option value="">Select product</option>
                      {products.filter((product) => product.store_id === selectedCollection.store_id && !(selectedCollection.products || []).some((row) => row.id === product.id)).map((product) => <option key={product.id} value={product.id}>{product.title}</option>)}
                    </select>
                    <button type="button" disabled={!collectionProductId || busy} onClick={() => void run(async () => { await api(`/admin/collections/${selectedCollection.id}/products`, { method: "POST", body: JSON.stringify({ listing_id: Number(collectionProductId) }) }); await openCollection(selectedCollection.id); }, "Product added") } className="px-3 rounded-md border border-border text-sm disabled:opacity-50">Add</button>
                  </div>
                </>
              ) : (
                <form onSubmit={(event) => { event.preventDefault(); void run(async () => { if (!collectionForm.store_id || !collectionForm.title.trim()) throw new Error("Store and title are required"); await api("/admin/collections", { method: "POST", body: JSON.stringify({ ...collectionForm, store_id: Number(collectionForm.store_id) }) }); setCollectionForm(emptyCollection); }, "Collection created"); }} className="space-y-3">
                  <h2 className="text-sm font-semibold">New collection</h2>
                  <Field label="Store"><select value={collectionForm.store_id} onChange={(event) => setCollectionForm({ ...collectionForm, store_id: event.target.value })} className={inputClass}><option value="">Select store</option>{stores.map((store) => <option key={store.id} value={store.id}>{store.name}</option>)}</select></Field>
                  <Field label="Title"><input value={collectionForm.title} onChange={(event) => setCollectionForm({ ...collectionForm, title: event.target.value })} className={inputClass} /></Field>
                  <Field label="Handle"><input value={collectionForm.handle} onChange={(event) => setCollectionForm({ ...collectionForm, handle: event.target.value })} className={inputClass} /></Field>
                  <button type="submit" disabled={busy} className="w-full h-9 rounded-md bg-accent text-bg text-sm font-medium disabled:opacity-50">Create collection</button>
                </form>
              )}
            </aside>
          </section>
        )}

        {view === "providers" && (
          <div className="space-y-6">
            {!selectedStore && <div className="border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">Select a store to configure providers and import products.</div>}
            <section className="grid xl:grid-cols-[minmax(0,1fr)_420px] gap-5">
              <div className="min-w-0 space-y-3">
                <h2 className="text-sm font-semibold">Supplier connections</h2>
                <div className="border border-border rounded-md divide-y divide-border">
                  {providers.map((provider) => {
                    const draft = providerDrafts[provider.connection_id] || provider;
                    const settings = draft.settings || {};
                    return (
                      <div key={provider.connection_id} className="p-3 space-y-3">
                        <div className="flex flex-wrap items-center justify-between gap-2">
                          <div><div className="font-medium capitalize">{provider.provider_slug}</div><div className="text-xs text-text-muted">Connection #{provider.connection_id}{provider.is_default ? " · Default" : ""}</div></div>
                          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={draft.enabled} onChange={(event) => updateProviderDraft(provider.connection_id, { enabled: event.target.checked })} />Enabled</label>
                        </div>
                        <div className="grid sm:grid-cols-2 lg:grid-cols-4 gap-3">
                          <Field label="Fulfillment"><select value={draft.fulfillment_mode} onChange={(event) => updateProviderDraft(provider.connection_id, { fulfillment_mode: event.target.value as ProviderPolicy["fulfillment_mode"] })} className={inputClass}><option value="review">Review first</option><option value="automatic">Automatic</option></select></Field>
                          <Field label="Target margin %"><input type="number" min="0" max="94.99" step="0.01" value={(draft.margin_bps / 100).toString()} onChange={(event) => updateProviderDraft(provider.connection_id, { margin_bps: Math.round(Number(event.target.value) * 100) })} className={inputClass} /></Field>
                          <Field label="Flat shipping"><input type="number" min="0" step="0.01" value={(Number(settings.flat_shipping_cents || 0) / 100).toString()} onChange={(event) => updateProviderSetting(provider.connection_id, "flat_shipping_cents", Math.round(Number(event.target.value) * 100))} className={inputClass} /></Field>
                          {provider.provider_slug === "printify" ? <Field label="Printify shop ID"><input value={String(settings.shop_id || "")} onChange={(event) => updateProviderSetting(provider.connection_id, "shop_id", event.target.value)} className={inputClass} /></Field> : <div />}
                        </div>
                        <div className="flex justify-end gap-2">
                          <button type="button" disabled={!selectedStore || busy || !draft.enabled} onClick={() => void run(() => browseProvider(provider), `${provider.provider_slug} catalog loaded`)} className="h-8 rounded-md border border-border px-3 text-sm disabled:opacity-50">Browse catalog</button>
                          <button type="button" disabled={!selectedStore || busy} onClick={() => void run(() => saveProvider(provider), "Provider policy saved")} className="h-8 rounded-md bg-accent px-3 text-sm font-medium text-bg disabled:opacity-50">Save</button>
                        </div>
                      </div>
                    );
                  })}
                  {providers.length === 0 && <div className="px-3 py-10 text-center text-sm text-text-muted">No Printful, Printify, BigBuy, or CJ Dropshipping connection is bound to Commerce.</div>}
                </div>
              </div>
              <aside className="xl:border-l xl:border-border xl:pl-5 space-y-3 min-w-0">
                <div className="flex items-center justify-between gap-2"><h2 className="text-sm font-semibold">{catalogProvider ? `${catalogProvider.provider_slug} catalog` : "Provider catalog"}</h2>{catalogProvider && <button type="button" onClick={() => { setCatalogProvider(null); setProviderProducts([]); setSelectedProviderProduct(null); }} className="text-sm text-text-muted hover:text-text">Close</button>}</div>
                {selectedProviderProduct ? (
                  <div className="space-y-3">
                    <div className="flex gap-3">{selectedProviderProduct.image_url && <img src={selectedProviderProduct.image_url} alt="" className="h-16 w-16 rounded object-cover bg-bg-input" />}<div className="min-w-0"><div className="font-medium">{selectedProviderProduct.title}</div><div className="text-xs text-text-muted">{selectedProviderProduct.variants?.length || 0} variants</div></div></div>
                    <div className="max-h-64 overflow-auto border-y border-border divide-y divide-border">{(selectedProviderProduct.variants || []).map((variant) => <div key={variant.id} className="flex items-center justify-between gap-3 py-2 text-sm"><span className="min-w-0"><span className="block truncate">{variant.title}</span><span className="text-xs text-text-muted">{variant.sku || variant.id}</span></span><span className="text-right whitespace-nowrap"><span className="block">{fmtMoney(variant.cost_cents, variant.currency)}</span><span className="text-xs text-text-muted">{variant.available ? "Available" : "Unavailable"}</span></span></div>)}</div>
                    <div className="flex gap-2"><button type="button" onClick={() => setSelectedProviderProduct(null)} className="flex-1 h-9 rounded-md border border-border text-sm">Back</button><button type="button" disabled={busy || !(selectedProviderProduct.variants?.length)} onClick={() => void run(importProviderProduct, "Product imported as draft")} className="flex-1 h-9 rounded-md bg-accent text-bg text-sm font-medium disabled:opacity-50">Import product</button></div>
                  </div>
                ) : catalogProvider ? (
                  <div className="max-h-[520px] overflow-auto border-y border-border divide-y divide-border">{providerProducts.map((product) => <button type="button" key={product.id} onClick={() => void openProviderProduct(product)} className="w-full flex items-center gap-3 py-2 text-left hover:bg-bg-input"><span className="h-10 w-10 shrink-0 bg-bg-input rounded overflow-hidden">{product.image_url && <img src={product.image_url} alt="" className="h-full w-full object-cover" />}</span><span className="min-w-0"><span className="block truncate text-sm font-medium">{product.title}</span><span className="text-xs text-text-muted">{product.vendor || `ID ${product.id}`}</span></span></button>)}{providerProducts.length === 0 && <p className="py-10 text-center text-sm text-text-muted">No products returned</p>}</div>
                ) : <p className="text-sm text-text-muted">Choose a connected provider to browse its live catalog.</p>}
              </aside>
            </section>

            <section className="space-y-3">
              <div className="flex items-center justify-between gap-3"><h2 className="text-sm font-semibold">Imported sources</h2><button type="button" disabled={busy || sources.length === 0} onClick={() => void run(syncSources, "Provider sources synchronized")} className="h-8 rounded-md border border-border px-3 text-sm disabled:opacity-50">Sync now</button></div>
              <DataTable headers={["Variant", "Provider", "Supplier SKU", "Availability", "Cost", "Last sync"]} empty={sources.length === 0} emptyLabel="No provider-sourced variants">
                {sources.map((source) => <tr key={source.id} className="border-t border-border"><td className="px-3 py-2.5">#{source.variant_id}</td><td className="px-3 py-2.5 capitalize">{source.provider_slug}</td><td className="px-3 py-2.5">{source.provider_sku || "-"}</td><td className="px-3 py-2.5"><Status value={source.availability} /></td><td className="px-3 py-2.5">{fmtMoney(source.unit_cost_cents, source.currency)}</td><td className="px-3 py-2.5 text-text-muted">{fmtDate(source.last_synced_at || "")}</td></tr>)}
              </DataTable>
            </section>

            <section className="space-y-3">
              <h2 className="text-sm font-semibold">Fulfillment dispatches</h2>
              <DataTable headers={["Dispatch", "Sale", "Provider", "Provider order", "Status", "Action"]} empty={dispatches.length === 0} emptyLabel="No provider dispatches">
                {dispatches.map((dispatch) => <tr key={dispatch.id} className="border-t border-border"><td className="px-3 py-2.5">#{dispatch.id}</td><td className="px-3 py-2.5">#{dispatch.sale_id}</td><td className="px-3 py-2.5 capitalize">{dispatch.provider_slug}</td><td className="px-3 py-2.5">{dispatch.external_order_id || "-"}</td><td className="px-3 py-2.5"><Status value={dispatch.status} />{dispatch.error && <div className="mt-1 max-w-xs text-xs text-red-600 truncate" title={dispatch.error}>{dispatch.error}</div>}</td><td className="px-3 py-2.5 text-right">{(dispatch.status === "review" || dispatch.status === "failed") && <button type="button" disabled={busy} onClick={() => void run(() => submitDispatch(dispatch.id), dispatch.status === "review" ? "Dispatch submitted" : "Dispatch retried")} className="h-8 rounded-md border border-border px-3 text-sm disabled:opacity-50">{dispatch.status === "review" ? "Approve" : "Retry"}</button>}</td></tr>)}
              </DataTable>
            </section>
          </div>
        )}

        {view === "stores" && (
          <section className="grid xl:grid-cols-[minmax(0,1fr)_360px] gap-5">
            <DataTable headers={["Store", "Currency", "Locale", "Timezone", "Status"]} empty={stores.length === 0} emptyLabel="No stores">
              {stores.map((store) => <tr key={store.id} onClick={() => { setEditingStore(store); setStoreForm({ name: store.name, slug: store.slug, default_currency: store.default_currency, default_locale: store.default_locale, timezone: store.timezone }); }} className="border-t border-border cursor-pointer hover:bg-bg-input/50"><td className="px-3 py-2.5"><div className="font-medium">{store.name}</div><div className="text-xs text-text-muted">/{store.slug}</div></td><td className="px-3 py-2.5">{store.default_currency}</td><td className="px-3 py-2.5">{store.default_locale}</td><td className="px-3 py-2.5">{store.timezone}</td><td className="px-3 py-2.5"><Status value={store.status} /></td></tr>)}
            </DataTable>
            <form onSubmit={(event) => { event.preventDefault(); void run(async () => { if (!storeForm.name.trim()) throw new Error("Store name is required"); if (editingStore) await api(`/admin/stores/${editingStore.id}`, { method: "PATCH", body: JSON.stringify(storeForm) }); else await api("/admin/stores", { method: "POST", body: JSON.stringify(storeForm) }); setEditingStore(null); setStoreForm(emptyStore); }, editingStore ? "Store updated" : "Store created"); }} className="xl:border-l xl:border-border xl:pl-5 space-y-3">
              <div className="flex items-center justify-between"><h2 className="text-sm font-semibold">{editingStore ? "Edit store" : "New store"}</h2>{editingStore && <button type="button" onClick={() => { setEditingStore(null); setStoreForm(emptyStore); }} className="text-sm text-text-muted hover:text-text">Cancel</button>}</div>
              <Field label="Name"><input value={storeForm.name} onChange={(event) => setStoreForm({ ...storeForm, name: event.target.value })} className={inputClass} /></Field>
              <Field label="Slug"><input value={storeForm.slug} disabled={Boolean(editingStore)} onChange={(event) => setStoreForm({ ...storeForm, slug: event.target.value })} className={inputClass} /></Field>
              <div className="grid grid-cols-2 gap-3"><Field label="Currency"><input value={storeForm.default_currency} maxLength={3} onChange={(event) => setStoreForm({ ...storeForm, default_currency: event.target.value.toUpperCase() })} className={inputClass} /></Field><Field label="Locale"><input value={storeForm.default_locale} onChange={(event) => setStoreForm({ ...storeForm, default_locale: event.target.value })} className={inputClass} /></Field></div>
              <Field label="Timezone"><input value={storeForm.timezone} onChange={(event) => setStoreForm({ ...storeForm, timezone: event.target.value })} className={inputClass} /></Field>
              <button type="submit" disabled={busy} className="w-full h-9 rounded-md bg-accent text-bg text-sm font-medium disabled:opacity-50">{editingStore ? "Save store" : "Create store"}</button>
            </form>
          </section>
        )}

        {view === "storefront" && (
          <section className="max-w-4xl space-y-5">
            <div>
              <h2 className="text-sm font-semibold">Customer storefront</h2>
              <p className="mt-1 text-sm text-text-muted">Content owns the public site, theme, domains, and publishing. Commerce supplies live products, collections, carts, and checkout actions.</p>
            </div>
            {!selectedStore ? (
              <div className="rounded-md border border-border px-4 py-10 text-center text-sm text-text-muted">Select a store to manage its storefront.</div>
            ) : (
              <div className="grid gap-5 md:grid-cols-3">
                <div className="border border-border rounded-md divide-y divide-border md:col-span-2">
                  <StorefrontRow label="Store" value={currentStore?.name || `Store #${selectedStore}`} />
                  <StorefrontRow label="Content site" value={storefront?.site_slug || "Not configured"} />
                  <StorefrontRow label="Extension" value={storefront?.extension_key || "Not installed"} />
                  <StorefrontRow label="Theme version" value={storefront?.version || "-"} />
                  <StorefrontRow label="Domain" value={storefront?.hostname || "Configure in Content"} />
                  <div className="flex items-center justify-between gap-3 px-3 py-3 text-sm">
                    <span className="text-text-muted">Status</span>
                    <Status value={storefront?.content_ready ? "active" : storefront?.configured ? "unavailable" : "not configured"} />
                  </div>
                </div>
                <aside className="space-y-3">
                  {storefront?.error && <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">{storefront.error}</div>}
                  <button type="button" disabled={busy} onClick={() => void run(configureStorefront, storefront?.configured ? "Storefront refreshed" : "Storefront configured")} className="w-full h-9 rounded-md bg-accent text-bg text-sm font-medium disabled:opacity-50">
                    {storefront?.configured ? "Refresh configuration" : "Configure with Content"}
                  </button>
                  {storefront?.preview_url && <a href={storefront.preview_url} target="_blank" rel="noreferrer" className="flex h-9 items-center justify-center rounded-md border border-border text-sm hover:bg-bg-input">Open storefront</a>}
                  {storefront?.configured && <a href="/apps/content/page" className="flex h-9 items-center justify-center rounded-md border border-border text-sm hover:bg-bg-input">Customize in Content</a>}
                  <p className="text-xs leading-5 text-text-muted">Without Content installed, Commerce remains available for merchant operations but exposes no public storefront.</p>
                </aside>
              </div>
            )}
          </section>
        )}

        {view === "carts" && (
          <section className="space-y-3"><h2 className="text-sm font-semibold">Carts</h2><DataTable headers={["Cart", "Store", "Items", "Status", "Updated", "Total"]} empty={carts.length === 0} emptyLabel="No carts">{carts.map((cart) => <tr key={cart.id} className="border-t border-border"><td className="px-3 py-2.5 font-medium">#{cart.id}</td><td className="px-3 py-2.5">{storeName(stores, cart.store_id)}</td><td className="px-3 py-2.5">{cart.items?.length || 0}</td><td className="px-3 py-2.5"><Status value={cart.status} /></td><td className="px-3 py-2.5 whitespace-nowrap text-text-muted">{fmtDate(cart.updated_at)}</td><td className="px-3 py-2.5 text-right whitespace-nowrap">{fmtMoney(cart.total_cents, cart.currency)}</td></tr>)}</DataTable></section>
        )}

        {view === "sales" && (
          <section className="grid xl:grid-cols-[minmax(0,1fr)_380px] gap-5">
            <div className="space-y-3 min-w-0"><h2 className="text-sm font-semibold">Sales</h2><DataTable headers={["Sale", "Customer", "Payment", "Fulfillment", "Total"]} empty={sales.length === 0} emptyLabel="No sales">{sales.map((sale) => <tr key={sale.id} onClick={() => void openSale(sale.id)} className="border-t border-border cursor-pointer hover:bg-bg-input/50"><td className="px-3 py-2.5"><div className="font-medium">#{sale.id}</div><div className="text-xs text-text-muted">{sale.invoice_number || fmtDate(sale.created_at)}</div></td><td className="px-3 py-2.5">{sale.customer_email || "Guest"}</td><td className="px-3 py-2.5"><Status value={sale.payment_status} /></td><td className="px-3 py-2.5"><Status value={sale.fulfillment_status} /></td><td className="px-3 py-2.5 text-right whitespace-nowrap">{fmtMoney(sale.total_cents, sale.currency)}</td></tr>)}</DataTable></div>
            <aside className="xl:border-l xl:border-border xl:pl-5 space-y-4">{selectedSale ? <><div className="flex items-center justify-between"><h2 className="text-sm font-semibold">Sale #{selectedSale.id}</h2><button type="button" onClick={() => setSelectedSale(null)} className="text-sm text-text-muted hover:text-text">Close</button></div><dl className="grid grid-cols-[120px_1fr] gap-y-2 text-sm"><dt className="text-text-muted">Invoice</dt><dd>{selectedSale.invoice_number || "-"}</dd><dt className="text-text-muted">Order</dt><dd>{selectedSale.order_id ? `#${selectedSale.order_id}` : "Pending"}</dd><dt className="text-text-muted">Customer</dt><dd>{selectedSale.customer_name || selectedSale.customer_email || "Guest"}</dd><dt className="text-text-muted">Payment</dt><dd><Status value={selectedSale.payment_status} /></dd></dl>{selectedSale.processing_error && <div className="space-y-2 rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300"><p>{selectedSale.processing_error}</p><button type="button" disabled={busy} onClick={() => void run(retrySale, "Sale processing completed")} className="h-8 rounded-md border border-red-500/40 px-3 text-sm font-medium disabled:opacity-50">Retry processing</button></div>}<div>{(selectedSale.items || []).map((item) => <div key={item.id} className="flex justify-between gap-3 border-b border-border py-2 text-sm"><span className="min-w-0"><span className="block truncate">{item.title_snapshot}</span><span className="text-xs text-text-muted">{item.quantity} x {fmtMoney(item.unit_amount_cents, item.currency)}</span></span><span className="whitespace-nowrap">{fmtMoney(Math.round(item.unit_amount_cents * item.quantity), item.currency)}</span></div>)}</div><div className="flex justify-between font-semibold"><span>Total</span><span>{fmtMoney(selectedSale.total_cents, selectedSale.currency)}</span></div></> : <p className="text-sm text-text-muted">Select a sale</p>}</aside>
          </section>
        )}
      </main>

      {editingProduct && (
        <div className="fixed inset-0 z-50 bg-black/35 flex justify-end" onMouseDown={(event) => { if (event.currentTarget === event.target) setEditingProduct(false); }}>
          <div role="dialog" aria-modal="true" aria-label={selectedProduct ? "Edit product" : "New product"} className="h-full w-full max-w-xl bg-bg border-l border-border overflow-auto p-5 space-y-5">
            <div className="flex items-center justify-between"><h2 className="text-base font-semibold">{selectedProduct ? "Edit product" : "New product"}</h2><button type="button" onClick={() => setEditingProduct(false)} className="text-sm text-text-muted hover:text-text">Close</button></div>
            <form onSubmit={(event) => { event.preventDefault(); void run(saveProduct, selectedProduct ? "Product updated" : "Product created"); }} className="space-y-3">
              <Field label="Store"><select disabled={Boolean(selectedProduct)} value={productForm.store_id} onChange={(event) => { const store = stores.find((row) => String(row.id) === event.target.value); setProductForm({ ...productForm, store_id: event.target.value, currency: store?.default_currency || productForm.currency }); }} className={inputClass}><option value="">Select store</option>{stores.map((store) => <option key={store.id} value={store.id}>{store.name}</option>)}</select></Field>
              <Field label="Title"><input value={productForm.title} onChange={(event) => setProductForm({ ...productForm, title: event.target.value })} className={inputClass} /></Field>
              <Field label="Handle"><input value={productForm.handle} onChange={(event) => setProductForm({ ...productForm, handle: event.target.value })} className={inputClass} /></Field>
              <Field label="Description"><textarea rows={5} value={productForm.description_html} onChange={(event) => setProductForm({ ...productForm, description_html: event.target.value })} className={inputClass} /></Field>
              <div className="grid grid-cols-2 gap-3"><Field label="Vendor"><input value={productForm.vendor} onChange={(event) => setProductForm({ ...productForm, vendor: event.target.value })} className={inputClass} /></Field><Field label="Product type"><input value={productForm.product_type} onChange={(event) => setProductForm({ ...productForm, product_type: event.target.value })} className={inputClass} /></Field></div>
              {!selectedProduct && <div className="grid grid-cols-[1fr_90px] gap-3"><Field label="First price"><input inputMode="decimal" value={productForm.price} onChange={(event) => setProductForm({ ...productForm, price: event.target.value })} className={inputClass} /></Field><Field label="Currency"><input maxLength={3} value={productForm.currency} onChange={(event) => setProductForm({ ...productForm, currency: event.target.value.toUpperCase() })} className={inputClass} /></Field></div>}
              {!selectedProduct && <Field label="SKU"><input value={productForm.sku} onChange={(event) => setProductForm({ ...productForm, sku: event.target.value })} className={inputClass} /></Field>}
              <button type="submit" disabled={busy} className="w-full h-9 rounded-md bg-accent text-bg text-sm font-medium disabled:opacity-50">Save product</button>
            </form>
            {selectedProduct && <section className="space-y-3 border-t border-border pt-4"><div className="flex items-center justify-between"><h3 className="text-sm font-semibold">Variants</h3><Status value={selectedProduct.status} /></div>{(selectedProduct.variants || []).map((variant) => <div key={variant.id} className="grid grid-cols-[1fr_auto] gap-3 border-b border-border pb-2 text-sm"><span><span className="block font-medium">{variant.title}</span><span className="text-xs text-text-muted">{variant.sku || "No SKU"}</span></span><span>{fmtMoney(variant.price_cents, variant.currency)}</span></div>)}<div className="grid grid-cols-2 gap-2"><input placeholder="Variant title" value={variantForm.title} onChange={(event) => setVariantForm({ ...variantForm, title: event.target.value })} className={inputClass} /><input placeholder="SKU" value={variantForm.sku} onChange={(event) => setVariantForm({ ...variantForm, sku: event.target.value })} className={inputClass} /><input placeholder="Price" inputMode="decimal" value={variantForm.price} onChange={(event) => setVariantForm({ ...variantForm, price: event.target.value })} className={inputClass} /><input placeholder="Inventory item ID" inputMode="numeric" value={variantForm.inventory_item_id} onChange={(event) => setVariantForm({ ...variantForm, inventory_item_id: event.target.value })} className={inputClass} /></div><button type="button" disabled={busy} onClick={() => void run(addVariant, "Variant added")} className="w-full h-9 rounded-md border border-border text-sm disabled:opacity-50">Add variant</button><div className="flex gap-2"><button type="button" disabled={busy || selectedProduct.status === "active"} onClick={() => void run(async () => { const updated = await api<Product>(`/admin/products/${selectedProduct.id}/publish`, { method: "POST" }); setSelectedProduct(updated); }, "Product published")} className="flex-1 h-9 rounded-md bg-emerald-600 text-white text-sm disabled:opacity-50">Publish</button><button type="button" disabled={busy || selectedProduct.status === "archived"} onClick={() => void run(async () => { await api(`/admin/products/${selectedProduct.id}/archive`, { method: "POST" }); setEditingProduct(false); }, "Product archived")} className="flex-1 h-9 rounded-md border border-red-500/50 text-red-600 text-sm disabled:opacity-50">Archive</button></div></section>}
          </div>
        </div>
      )}
    </div>
  );
}

const inputClass = "w-full rounded-md border border-border bg-bg-input px-3 py-2 text-sm disabled:opacity-60";

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block space-y-1"><span className="text-xs font-medium text-text-muted">{label}</span>{children}</label>;
}

function Metric({ label, value }: { label: string; value: number }) {
  return <div className="px-3 py-2.5 border-r border-b lg:border-b-0 border-border last:border-r-0"><div className="text-xs text-text-muted">{label}</div><div className="text-lg font-semibold tabular-nums">{value}</div></div>;
}

function StorefrontRow({ label, value }: { label: string; value: string }) {
  return <div className="grid grid-cols-[120px_minmax(0,1fr)] gap-3 px-3 py-3 text-sm"><span className="text-text-muted">{label}</span><span className="truncate text-right font-medium" title={value}>{value}</span></div>;
}

function DataTable({ headers, children, empty, emptyLabel }: { headers: string[]; children: ReactNode; empty: boolean; emptyLabel: string }) {
  return <div className="overflow-x-auto rounded-md border border-border"><table className="w-full min-w-[640px] text-sm"><thead className="bg-bg-subtle text-text-muted"><tr>{headers.map((header, index) => <th key={header} className={`${index === headers.length - 1 ? "text-right" : "text-left"} px-3 py-2 font-medium`}>{header}</th>)}</tr></thead><tbody>{children}{empty && <tr><td colSpan={headers.length} className="px-3 py-10 text-center text-text-muted">{emptyLabel}</td></tr>}</tbody></table></div>;
}

function Status({ value }: { value: string }) {
  const tone = value === "paid" || value === "active" || value === "converted" ? "bg-emerald-500/15 text-emerald-700 dark:text-emerald-300" : value === "processing" || value === "awaiting_payment" || value === "checkout" ? "bg-amber-500/15 text-amber-700 dark:text-amber-300" : value === "archived" || value === "cancelled" || value === "failed" ? "bg-red-500/15 text-red-700 dark:text-red-300" : "bg-bg-input text-text";
  return <span className={`inline-flex rounded px-2 py-0.5 text-xs whitespace-nowrap ${tone}`}>{(value || "unknown").replaceAll("_", " ")}</span>;
}

function storeName(stores: Store[], id: number) {
  return stores.find((store) => store.id === id)?.name || `Store #${id}`;
}

function moneyToCents(value: string) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) throw new Error("Price must be greater than zero");
  return Math.round(number * 100);
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
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Request failed";
}
