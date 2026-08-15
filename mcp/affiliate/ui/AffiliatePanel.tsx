import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const API = "/api/apps/affiliate";
const PAGE_SIZE = 100;
const INPUT_CLASS = "w-full bg-bg-input border border-border rounded px-2 py-1.5 text-sm";

interface NativePanelProps { appName: string; installId: number; projectId: string }
interface Network {
  id: number; key: string; name: string; enabled: boolean; bound: boolean;
  supports_offers: boolean; supports_products: boolean; supports_stats: boolean; supports_links: boolean;
  supports_clicks: boolean; stats_needs_dates: boolean; stats_mode?: string;
  link_mode?: "provider" | "manual" | "catalog"; last_refreshed_at?: string;
}
interface Offer {
  id: number; network_key: string; external_id: string; merchant_name: string;
  offer_name: string; status: string; commission_summary?: string;
}
interface Product {
  id: number; network_key: string; external_id: string; offer_id?: number;
  source: "provider" | "manual"; status: "active" | "archived";
  merchant_name?: string; name: string; description?: string; category?: string;
  brand?: string; sku?: string; gtin?: string; currency?: string;
  price_cents: number; sale_price_cents: number; image_url?: string;
  destination_url?: string; affiliate_url?: string; availability?: string;
}
interface Link {
  id: number; network_key: string; offer_id?: number; product_id?: number; merchant_name?: string;
  offer_name?: string; product_name?: string; destination_url: string; affiliate_url: string;
  short_url?: string; redirect_rule_id?: number; campaign?: string; subid?: string; status: string;
}
interface StatRow {
  date?: string; network_key?: string; clicks: number; conversions: number;
  revenue_cents: number; commission_cents: number; currency: string;
}
type Tab = "networks" | "offers" | "products" | "links" | "stats";

async function getJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: "same-origin", ...init });
  if (!response.ok) throw new Error((await response.text()).trim() || `Request failed (${response.status})`);
  return response.json();
}

function dateValue(daysAgo: number): string {
  const date = new Date();
  date.setDate(date.getDate() - daysAgo);
  return date.toISOString().slice(0, 10);
}

export default function AffiliatePanel({}: NativePanelProps) {
  const [tab, setTab] = useState<Tab>("offers");
  const [networks, setNetworks] = useState<Network[]>([]);
  const [offers, setOffers] = useState<Offer[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [links, setLinks] = useState<Link[]>([]);
  const [stats, setStats] = useState<StatRow[]>([]);
  const [clicksAvailable, setClicksAvailable] = useState(false);
  const [offerTotal, setOfferTotal] = useState(0);
  const [productTotal, setProductTotal] = useState(0);
  const [linkTotal, setLinkTotal] = useState(0);
  const [offerPage, setOfferPage] = useState(0);
  const [productPage, setProductPage] = useState(0);
  const [linkPage, setLinkPage] = useState(0);
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [search, setSearch] = useState("");
  const [network, setNetwork] = useState("");
  const [from, setFrom] = useState(dateValue(29));
  const [to, setTo] = useState(dateValue(0));
  const [linkingOffer, setLinkingOffer] = useState<Offer | null>(null);
  const [linkingProduct, setLinkingProduct] = useState<Product | null>(null);
  const [editingProduct, setEditingProduct] = useState<Product | null | undefined>(undefined);
  const offerRequest = useRef(0);
  const productRequest = useRef(0);
  const linkRequest = useRef(0);
  const statsRequest = useRef(0);

  useEffect(() => {
    const timer = window.setTimeout(() => setSearch(query.trim()), 250);
    return () => window.clearTimeout(timer);
  }, [query]);
  useEffect(() => { setOfferPage(0); setProductPage(0); setLinkPage(0); }, [search, network]);

  const loadNetworks = useCallback(async () => {
    const data = await getJSON<{ networks: Network[] }>(`${API}/networks`);
    setNetworks(data.networks || []);
  }, []);
  const loadOffers = useCallback(async () => {
    const request = ++offerRequest.current;
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(offerPage * PAGE_SIZE) });
    if (search) params.set("q", search);
    if (network) params.set("network", network);
    const data = await getJSON<{ offers: Offer[]; total: number }>(`${API}/offers?${params}`);
    if (request === offerRequest.current) { setOffers(data.offers || []); setOfferTotal(data.total || 0); }
  }, [search, network, offerPage]);
  const loadProducts = useCallback(async () => {
    const request = ++productRequest.current;
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(productPage * PAGE_SIZE) });
    if (search) params.set("q", search);
    if (network) params.set("network", network);
    const data = await getJSON<{ products: Product[]; total: number }>(`${API}/products?${params}`);
    if (request === productRequest.current) { setProducts(data.products || []); setProductTotal(data.total || 0); }
  }, [search, network, productPage]);
  const loadLinks = useCallback(async () => {
    const request = ++linkRequest.current;
    const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(linkPage * PAGE_SIZE) });
    if (search) params.set("q", search);
    if (network) params.set("network", network);
    const data = await getJSON<{ links: Link[]; total: number }>(`${API}/links?${params}`);
    if (request === linkRequest.current) { setLinks(data.links || []); setLinkTotal(data.total || 0); }
  }, [search, network, linkPage]);
  const loadStats = useCallback(async () => {
    const request = ++statsRequest.current;
    const params = new URLSearchParams({ group_by: "day", from, to });
    if (network) params.set("network", network);
    const data = await getJSON<{ stats: StatRow[]; clicks_available: boolean }>(`${API}/stats?${params}`);
    if (request === statsRequest.current) {
      setStats(data.stats || []);
      setClicksAvailable(Boolean(data.clicks_available));
    }
  }, [network, from, to]);

  const reload = useCallback(async (showStatus = true) => {
    try {
      if (showStatus) setStatus("Loading...");
      await Promise.all([loadNetworks(), loadOffers(), loadProducts(), loadLinks(), loadStats()]);
      if (showStatus) setStatus("");
    } catch (error) { setStatus((error as Error).message); }
  }, [loadNetworks, loadOffers, loadProducts, loadLinks, loadStats]);
  useEffect(() => { void reload(false); }, [reload]);

  const refreshNetwork = async (item: Network) => {
    try {
      setStatus(`Syncing ${item.name}...`);
      const data = await getJSON<{ offers_upserted: number; products_upserted: number; links_upserted: number; stats_days_upserted: number }>(`${API}/refresh`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ network: item.key, kind: "all", from, to }),
      });
      setStatus(`${data.offers_upserted || 0} offers, ${data.products_upserted || 0} products, ${data.links_upserted || 0} links, ${data.stats_days_upserted || 0} stat rows`);
      await reload(false);
    } catch (error) { setStatus((error as Error).message); }
  };

  const selectedNetwork = networks.find((item) => item.key === linkingOffer?.network_key);
  const selectedProductNetwork = networks.find((item) => item.key === linkingProduct?.network_key);
  return (
    <div className="h-full min-w-0 flex flex-col text-sm">
      <header className="border-b border-border px-3 py-2 flex flex-wrap items-center gap-2">
        <div className="font-medium text-text mr-1">Affiliate</div>
        <div className="flex rounded border border-border overflow-hidden">
          {(["networks", "offers", "products", "links", "stats"] as Tab[]).map((key) => (
            <button key={key} onClick={() => setTab(key)} className={`px-3 py-1 text-xs capitalize ${tab === key ? "bg-accent text-bg" : "text-text-muted hover:bg-bg-elev"}`}>{key}</button>
          ))}
        </div>
        <select value={network} onChange={(event) => setNetwork(event.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-xs max-w-48">
          <option value="">All networks</option>
          {networks.map((item) => <option key={item.key} value={item.key}>{item.name}</option>)}
        </select>
        {(tab === "offers" || tab === "products" || tab === "links") && <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search" className="bg-bg-input border border-border rounded px-2 py-1 text-xs w-40" />}
        {tab === "stats" && <DateRange from={from} to={to} onFrom={setFrom} onTo={setTo} />}
        {tab === "products" && <button onClick={() => setEditingProduct(null)} className="px-2 py-1 bg-accent text-bg rounded text-xs">Add product</button>}
        <button onClick={() => void reload()} className="px-2 py-1 border border-border rounded text-xs">Reload</button>
        <span className="basis-full sm:basis-auto sm:ml-auto text-xs text-text-dim truncate min-h-4">{status}</span>
      </header>
      <main className="flex-1 overflow-auto min-w-0">
        {tab === "networks" && <NetworksView networks={networks} onRefresh={refreshNetwork} />}
        {tab === "offers" && <OffersView offers={offers} onCreateLink={setLinkingOffer} />}
        {tab === "products" && <ProductsView products={products} onCreateLink={setLinkingProduct} onEdit={setEditingProduct} onArchive={async (product) => { if (!window.confirm(`Archive ${product.name}?`)) return; try { await getJSON(`${API}/products/${product.id}`, { method: "DELETE" }); setStatus("Product archived"); await loadProducts(); } catch (error) { setStatus((error as Error).message); } }} />}
        {tab === "links" && <LinksView links={links} />}
        {tab === "stats" && <StatsView stats={stats} clicksAvailable={clicksAvailable} />}
      </main>
      {tab === "offers" && <Pager page={offerPage} total={offerTotal} onPage={setOfferPage} />}
      {tab === "products" && <Pager page={productPage} total={productTotal} onPage={setProductPage} />}
      {tab === "links" && <Pager page={linkPage} total={linkTotal} onPage={setLinkPage} />}
      {linkingOffer && <CreateLinkDialog offer={linkingOffer} network={selectedNetwork} onClose={() => setLinkingOffer(null)} onDone={(warning) => { setLinkingOffer(null); setStatus(warning || "Link created"); void loadLinks(); setTab("links"); }} />}
      {linkingProduct && <CreateProductLinkDialog product={linkingProduct} network={selectedProductNetwork} onClose={() => setLinkingProduct(null)} onDone={(warning) => { setLinkingProduct(null); setStatus(warning || "Link created"); void loadLinks(); setTab("links"); }} />}
      {editingProduct !== undefined && <ManualProductDialog product={editingProduct || undefined} networks={networks} selectedNetwork={network} onClose={() => setEditingProduct(undefined)} onDone={async (message) => { setEditingProduct(undefined); setStatus(message); setProductPage(0); await loadProducts(); }} />}
    </div>
  );
}

function DateRange({ from, to, onFrom, onTo }: { from: string; to: string; onFrom: (v: string) => void; onTo: (v: string) => void }) {
  return <div className="flex items-center gap-1"><input aria-label="From date" type="date" value={from} max={to} onChange={(e) => onFrom(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-xs" /><span className="text-text-dim">to</span><input aria-label="To date" type="date" value={to} min={from} onChange={(e) => onTo(e.target.value)} className="bg-bg-input border border-border rounded px-2 py-1 text-xs" /></div>;
}

function NetworksView({ networks, onRefresh }: { networks: Network[]; onRefresh: (network: Network) => void }) {
  return <DataTable rowCount={networks.length} empty="No affiliate integrations are available." minWidth="min-w-[720px]">
    <thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-2">Network</th><th className="px-3 py-2">Connection</th><th className="px-3 py-2">Data</th><th className="px-3 py-2">Last sync</th><th className="px-3 py-2 text-right">Actions</th></tr></thead>
    <tbody>{networks.map((item) => { const refreshable = item.supports_offers || item.supports_products || item.supports_stats; return <tr key={item.key} className="border-b border-border last:border-b-0"><td className="px-3 py-2 font-medium">{item.name}</td><td className="px-3 py-2 text-text-dim">{item.bound ? "Connected" : "Not connected"}</td><td className="px-3 py-2 text-text-dim">{[item.supports_offers && "Offers", item.supports_products && "Products", item.supports_stats && statsCapabilityLabel(item.stats_mode), item.supports_links && "Links"].filter(Boolean).join(", ") || "Links only"}</td><td className="px-3 py-2 text-text-dim">{item.last_refreshed_at ? timeAgo(item.last_refreshed_at) : "-"}</td><td className="px-3 py-2 text-right"><button title={!item.bound ? "Connect this integration first" : !refreshable ? "This provider has no refreshable data" : `Sync ${item.name}`} disabled={!item.bound || !refreshable} onClick={() => void onRefresh(item)} className="px-2 py-1 border border-border rounded text-xs disabled:opacity-40">Sync</button></td></tr>; })}</tbody>
  </DataTable>;
}

function ProductsView({ products, onCreateLink, onEdit, onArchive }: { products: Product[]; onCreateLink: (product: Product) => void; onEdit: (product: Product) => void; onArchive: (product: Product) => void }) {
  return <DataTable rowCount={products.length} empty="No products found. Sync a connected product network or add one manually." minWidth="min-w-[1040px]">
    <thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-2 w-14">Image</th><th className="px-3 py-2">Product</th><th className="px-3 py-2">Merchant</th><th className="px-3 py-2">Network</th><th className="px-3 py-2">Source</th><th className="px-3 py-2 text-right">Price</th><th className="px-3 py-2">Availability</th><th className="px-3 py-2 text-right">Actions</th></tr></thead>
    <tbody>{products.map((item) => <tr key={item.id} className="border-b border-border last:border-b-0"><td className="px-3 py-2"><div className="size-10 border border-border bg-bg-elev overflow-hidden rounded">{item.image_url && <img src={item.image_url} alt="" loading="lazy" className="size-full object-contain" />}</div></td><td className="px-3 py-2 max-w-[360px]"><div className="font-medium truncate" title={item.name}>{item.name}</div><div className="text-xs text-text-dim truncate">{[item.brand, item.category].filter(Boolean).join(" / ") || item.sku || "-"}</div></td><td className="px-3 py-2 text-text-muted max-w-[200px] truncate" title={item.merchant_name}>{item.merchant_name || "-"}</td><td className="px-3 py-2 text-text-dim">{labelNetwork(item.network_key)}</td><td className="px-3 py-2 text-text-dim capitalize">{item.source}</td><td className="px-3 py-2 text-right whitespace-nowrap">{productPrice(item)}</td><td className="px-3 py-2 text-text-dim capitalize">{item.availability || "-"}</td><td className="px-3 py-2 text-right whitespace-nowrap">{item.source === "manual" && <><button title="Edit product" onClick={() => onEdit(item)} className="px-2 py-1 text-xs text-text-muted hover:text-text">Edit</button><button title="Archive product" onClick={() => onArchive(item)} className="px-2 py-1 text-xs text-text-muted hover:text-text">Archive</button></>}<button title={item.affiliate_url ? "Create a managed link" : "Generate a provider link"} onClick={() => onCreateLink(item)} className="px-2 py-1 bg-accent text-bg rounded text-xs">Create link</button></td></tr>)}</tbody>
  </DataTable>;
}

function OffersView({ offers, onCreateLink }: { offers: Offer[]; onCreateLink: (offer: Offer) => void }) {
  return <DataTable rowCount={offers.length} empty="No offers found. Sync a connected network or adjust the filters." minWidth="min-w-[820px]">
    <thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-2">Merchant</th><th className="px-3 py-2">Offer</th><th className="px-3 py-2">Network</th><th className="px-3 py-2">Status</th><th className="px-3 py-2">Commission</th><th className="px-3 py-2 text-right">Actions</th></tr></thead>
    <tbody>{offers.map((item) => <tr key={item.id} className="border-b border-border last:border-b-0"><td className="px-3 py-2 font-medium">{item.merchant_name}</td><td className="px-3 py-2 text-text-muted">{item.offer_name || "-"}</td><td className="px-3 py-2 text-text-dim">{labelNetwork(item.network_key)}</td><td className="px-3 py-2 text-text-dim">{item.status || "-"}</td><td className="px-3 py-2 text-text-dim max-w-[260px] truncate" title={item.commission_summary}>{item.commission_summary || "-"}</td><td className="px-3 py-2 text-right"><button onClick={() => onCreateLink(item)} className="px-2 py-1 bg-accent text-bg rounded text-xs">Create link</button></td></tr>)}</tbody>
  </DataTable>;
}

function LinksView({ links }: { links: Link[] }) {
  const copy = async (value: string) => { await navigator.clipboard.writeText(value); };
  return <DataTable rowCount={links.length} empty="No links created yet." minWidth="min-w-[1040px]">
    <thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-2">Product / Offer</th><th className="px-3 py-2">Destination</th><th className="px-3 py-2">Affiliate URL</th><th className="px-3 py-2">Short URL</th><th className="px-3 py-2">Tracking</th><th className="px-3 py-2">Status</th></tr></thead>
    <tbody>{links.map((item) => <tr key={item.id} className="border-b border-border last:border-b-0"><td className="px-3 py-2 max-w-[240px]" title={linkOfferTitle(item)}><div className="font-medium truncate">{item.product_name || item.merchant_name || (item.offer_id ? `Offer #${item.offer_id}` : "Unassigned")}</div>{!item.product_name && item.offer_name && item.offer_name !== item.merchant_name && <div className="text-xs text-text-dim truncate">{item.offer_name}</div>}{item.product_name && item.merchant_name && <div className="text-xs text-text-dim truncate">{item.merchant_name}</div>}<div className="text-xs text-text-dim">{labelNetwork(item.network_key)}</div></td><URLCell value={item.destination_url} onCopy={copy} /><URLCell value={item.affiliate_url} onCopy={copy} /><URLCell value={item.short_url || ""} onCopy={copy} /><td className="px-3 py-2 text-text-dim"><div>{item.campaign || "-"}</div>{item.subid && <div className="text-xs">{item.subid}</div>}</td><td className="px-3 py-2 text-text-dim">{item.status}</td></tr>)}</tbody>
  </DataTable>;
}

function URLCell({ value, onCopy }: { value: string; onCopy: (value: string) => void }) {
  if (!value) return <td className="px-3 py-2 text-text-dim">-</td>;
  return <td className="px-3 py-2 max-w-[260px]"><div className="truncate text-text-dim" title={value}>{value}</div><div className="flex gap-2 mt-1 text-xs"><button title="Copy URL" onClick={() => void onCopy(value)} className="text-text-muted hover:text-text">Copy</button><a title="Open URL" href={value} target="_blank" rel="noreferrer" className="text-text-muted hover:text-text">Open</a></div></td>;
}

function StatsView({ stats, clicksAvailable }: { stats: StatRow[]; clicksAvailable: boolean }) {
  const ordered = [...stats].sort((a, b) => String(a.date || "").localeCompare(String(b.date || "")));
  const totals = stats.reduce((acc, row) => ({ clicks: acc.clicks + row.clicks, conversions: acc.conversions + row.conversions }), { clicks: 0, conversions: 0 });
  const moneyByCurrency = useMemo(() => stats.reduce<Record<string, { revenue: number; commission: number }>>((acc, row) => { if (!row.currency) return acc; const key = row.currency; acc[key] ||= { revenue: 0, commission: 0 }; acc[key].revenue += row.revenue_cents; acc[key].commission += row.commission_cents; return acc; }, {}), [stats]);
  const maxActivity = Math.max(1, ...ordered.flatMap((row) => [row.clicks, row.conversions]));
  return <div className="p-3 sm:p-4 space-y-4">
    {stats.length > 0 && <div className="border border-border rounded overflow-hidden"><div className="grid grid-cols-2 lg:grid-cols-4 border-b border-border text-xs"><Metric label="Clicks" value={clicksAvailable ? String(totals.clicks) : "Unavailable"} muted={!clicksAvailable} /><Metric label="Conversions" value={String(totals.conversions)} /><Metric label="Revenue" value={moneySummary(moneyByCurrency, "revenue")} /><Metric label="Commission" value={moneySummary(moneyByCurrency, "commission")} /></div><div className="h-44 px-3 py-4 flex items-end gap-1 overflow-x-auto">{ordered.map((row, index) => <div key={`${row.date}-${row.currency}-${index}`} className="flex-1 min-w-[18px] h-32 flex items-end justify-center gap-0.5" title={`${row.date}: ${row.clicks} clicks, ${row.conversions} conversions`}>{clicksAvailable && <div className="w-[45%] max-w-[10px] bg-text-muted rounded-t" style={{ height: activityBarHeight(row.clicks, maxActivity) }} />}<div className="w-[45%] max-w-[10px] bg-accent rounded-t" style={{ height: activityBarHeight(row.conversions, maxActivity) }} /></div>)}</div><div className="px-3 pb-3 flex items-center gap-4 text-xs text-text-dim">{clicksAvailable && <span className="inline-flex items-center gap-1.5"><span className="size-2 bg-text-muted" />Clicks</span>}<span className="inline-flex items-center gap-1.5"><span className="size-2 bg-accent" />Conversions</span></div></div>}
    <div className="overflow-x-auto border border-border rounded"><table className="w-full min-w-[640px] border-collapse"><thead><tr className="text-left text-xs text-text-dim border-b border-border"><th className="px-3 py-2">Date</th><th className="px-3 py-2 text-right">Clicks</th><th className="px-3 py-2 text-right">Conversions</th><th className="px-3 py-2 text-right">Revenue</th><th className="px-3 py-2 text-right">Commission</th></tr></thead><tbody>{stats.map((row, index) => <tr key={`${row.date}-${row.currency}-${index}`} className="border-b border-border last:border-b-0"><td className="px-3 py-2">{row.date || "-"}</td><td className="px-3 py-2 text-right text-text-dim">{clicksAvailable ? row.clicks : "-"}</td><td className="px-3 py-2 text-right">{row.conversions}</td><td className="px-3 py-2 text-right">{money(row.revenue_cents, row.currency)}</td><td className="px-3 py-2 text-right">{money(row.commission_cents, row.currency)}</td></tr>)}</tbody></table></div>
    {stats.length === 0 && <div className="text-center text-text-dim py-8">No stats imported for this date range.</div>}
  </div>;
}

function activityBarHeight(value: number, maximum: number) {
  return value > 0 ? Math.max(3, Math.round((value / maximum) * 120)) : 0;
}

function Metric({ label, value, muted = false }: { label: string; value: string; muted?: boolean }) { return <div className="px-3 py-2 border-r border-b sm:border-b-0 border-border last:border-r-0 min-w-0"><div className="text-text-dim">{label}</div><div className={`font-medium truncate ${muted ? "text-text-muted" : "text-text"}`} title={value}>{value}</div></div>; }

function ManualProductDialog({ product, networks, selectedNetwork, onClose, onDone }: { product?: Product; networks: Network[]; selectedNetwork: string; onClose: () => void; onDone: (message: string) => Promise<void> }) {
  const initialNetwork = product?.network_key || selectedNetwork || "amazon-associates";
  const [network, setNetwork] = useState(initialNetwork);
  const [name, setName] = useState(product?.name || "");
  const [externalID, setExternalID] = useState(product?.external_id || "");
  const [asin, setAsin] = useState(product?.network_key === "amazon-associates" ? product.external_id : "");
  const [marketplace, setMarketplace] = useState(product?.network_key === "amazon-associates" ? hostname(product.destination_url) || "www.amazon.com" : "www.amazon.com");
  const [partnerTag, setPartnerTag] = useState("");
  const [destinationURL, setDestinationURL] = useState(product?.destination_url || "");
  const [affiliateURL, setAffiliateURL] = useState(product?.affiliate_url || "");
  const [merchantName, setMerchantName] = useState(product?.merchant_name || "");
  const [brand, setBrand] = useState(product?.brand || "");
  const [category, setCategory] = useState(product?.category || "");
  const [description, setDescription] = useState(product?.description || "");
  const [imageURL, setImageURL] = useState(product?.image_url || "");
  const [currency, setCurrency] = useState(product?.currency || "");
  const [price, setPrice] = useState(product?.price_cents ? (product.price_cents / 100).toFixed(2) : "");
  const [salePrice, setSalePrice] = useState(product?.sale_price_cents ? (product.sale_price_cents / 100).toFixed(2) : "");
  const [availability, setAvailability] = useState(product?.availability || "available");
  const [status, setStatus] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const amazon = network === "amazon-associates";
  const submit = async () => {
    const priceCents = moneyToCents(price); const salePriceCents = moneyToCents(salePrice);
    if (priceCents === null || salePriceCents === null) { setStatus("Prices must be valid non-negative amounts"); return; }
    try {
      setSubmitting(true); setStatus(product ? "Saving..." : "Creating...");
      await getJSON(product ? `${API}/products/${product.id}` : `${API}/products`, {
        method: product ? "PATCH" : "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ network, name, external_id: externalID, asin, marketplace, partner_tag: partnerTag, destination_url: destinationURL, affiliate_url: affiliateURL, merchant_name: merchantName, brand, category, description, image_url: imageURL, currency, price_cents: priceCents, sale_price_cents: salePriceCents, availability }),
      });
      await onDone(product ? "Product updated" : "Product added");
    } catch (error) { setStatus((error as Error).message); setSubmitting(false); }
  };
  const canSubmit = Boolean(name && (amazon ? asin || destinationURL : destinationURL && affiliateURL));
  return <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-3" onClick={onClose}><div role="dialog" aria-modal="true" className="bg-bg border border-border rounded-lg p-4 w-[680px] max-w-full max-h-full overflow-auto" onClick={(event) => event.stopPropagation()}><div className="flex items-center mb-3"><div className="font-medium flex-1">{product ? "Edit product" : "Add product"}</div><button aria-label="Close" title="Close" onClick={onClose} className="text-text-dim hover:text-text px-2">x</button></div><div className="space-y-3"><div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Field label="Network"><select value={network} disabled={Boolean(product)} onChange={(event) => setNetwork(event.target.value)} className={`${INPUT_CLASS} disabled:opacity-60`}>{networks.map((item) => <option key={item.key} value={item.key}>{item.name}</option>)}</select></Field><Field label="Product name"><input value={name} onChange={(event) => setName(event.target.value)} autoFocus className={INPUT_CLASS} /></Field></div>{amazon ? <div className="grid grid-cols-1 sm:grid-cols-3 gap-3"><Field label="ASIN"><input value={asin} disabled={Boolean(product)} onChange={(event) => setAsin(event.target.value.toUpperCase())} className={`${INPUT_CLASS} disabled:opacity-60`} /></Field><Field label="Marketplace"><input value={marketplace} onChange={(event) => setMarketplace(event.target.value)} className={INPUT_CLASS} placeholder="www.amazon.com" /></Field><Field label="Partner tag"><input value={partnerTag} onChange={(event) => setPartnerTag(event.target.value)} className={INPUT_CLASS} /></Field></div> : <Field label="External ID"><input value={externalID} disabled={Boolean(product)} onChange={(event) => setExternalID(event.target.value)} className={`${INPUT_CLASS} disabled:opacity-60`} /></Field>}<div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Field label="Destination URL"><input value={destinationURL} onChange={(event) => setDestinationURL(event.target.value)} className={INPUT_CLASS} placeholder={amazon ? "Optional when ASIN is set" : "https://merchant.example/product"} /></Field><Field label="Affiliate URL"><input value={affiliateURL} onChange={(event) => setAffiliateURL(event.target.value)} className={INPUT_CLASS} placeholder={amazon ? "Generated from partner tag when empty" : "https://affiliate.example/tracking"} /></Field></div><div className="grid grid-cols-1 sm:grid-cols-3 gap-3"><Field label="Merchant"><input value={merchantName} onChange={(event) => setMerchantName(event.target.value)} className={INPUT_CLASS} /></Field><Field label="Brand"><input value={brand} onChange={(event) => setBrand(event.target.value)} className={INPUT_CLASS} /></Field><Field label="Category"><input value={category} onChange={(event) => setCategory(event.target.value)} className={INPUT_CLASS} /></Field></div><Field label="Description"><textarea value={description} onChange={(event) => setDescription(event.target.value)} rows={2} className={INPUT_CLASS} /></Field><Field label="Image URL"><input value={imageURL} onChange={(event) => setImageURL(event.target.value)} className={INPUT_CLASS} /></Field><div className="grid grid-cols-2 sm:grid-cols-4 gap-3"><Field label="Currency"><input value={currency} maxLength={3} onChange={(event) => setCurrency(event.target.value.toUpperCase())} className={INPUT_CLASS} placeholder="USD" /></Field><Field label="Price"><input type="number" min="0" step="0.01" value={price} onChange={(event) => setPrice(event.target.value)} className={INPUT_CLASS} /></Field><Field label="Sale price"><input type="number" min="0" step="0.01" value={salePrice} onChange={(event) => setSalePrice(event.target.value)} className={INPUT_CLASS} /></Field><Field label="Availability"><select value={availability} onChange={(event) => setAvailability(event.target.value)} className={INPUT_CLASS}><option value="available">Available</option><option value="unavailable">Unavailable</option><option value="preorder">Preorder</option><option value="">Unspecified</option></select></Field></div>{status && <div className="text-xs text-text-dim break-words">{status}</div>}<div className="flex justify-end gap-2 pt-2"><button onClick={onClose} disabled={submitting} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button><button onClick={() => void submit()} disabled={submitting || !canSubmit} className="px-3 py-1.5 bg-accent text-bg rounded text-sm disabled:opacity-50">{submitting ? "Saving..." : product ? "Save" : "Add product"}</button></div></div></div></div>;
}

function CreateLinkDialog({ offer, network, onClose, onDone }: { offer: Offer; network?: Network; onClose: () => void; onDone: (warning: string) => void }) {
  const [url, setUrl] = useState(""); const [affiliateURL, setAffiliateURL] = useState("");
  const [campaign, setCampaign] = useState(""); const [subid, setSubid] = useState("");
  const [shorten, setShorten] = useState(false); const [shortHostname, setShortHostname] = useState("");
  const [shortPath, setShortPath] = useState(`/${slug(offer.merchant_name)}`); const [status, setStatus] = useState("");
  const [submitting, setSubmitting] = useState(false); const manual = network?.link_mode === "manual";
  const submit = async () => { try { setSubmitting(true); setStatus("Creating..."); const data = await getJSON<{ warning?: string }>(`${API}/links`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ url, affiliate_url: affiliateURL, network: offer.network_key, offer_id: offer.id, campaign, subid, shorten, short_hostname: shortHostname, short_path: shortPath }) }); onDone(data.warning || ""); } catch (error) { setStatus((error as Error).message); setSubmitting(false); } };
  return <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-3" onClick={onClose}><div role="dialog" aria-modal="true" className="bg-bg border border-border rounded-lg p-4 w-[560px] max-w-full max-h-full overflow-auto" onClick={(e) => e.stopPropagation()}><div className="flex items-center mb-3"><div className="font-medium flex-1 min-w-0 truncate">Create link for {offer.merchant_name}</div><button aria-label="Close" title="Close" onClick={onClose} className="text-text-dim hover:text-text px-2">x</button></div><div className="space-y-3"><Field label="Destination URL"><input value={url} onChange={(e) => setUrl(e.target.value)} autoFocus className={INPUT_CLASS} placeholder="https://merchant.com/product" /></Field>{manual && <Field label="Affiliate URL"><input value={affiliateURL} onChange={(e) => setAffiliateURL(e.target.value)} className={INPUT_CLASS} placeholder="Paste the network tracking URL" /></Field>}<div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Field label="Campaign"><input value={campaign} onChange={(e) => setCampaign(e.target.value)} className={INPUT_CLASS} /></Field><Field label="Sub ID"><input value={subid} onChange={(e) => setSubid(e.target.value)} className={INPUT_CLASS} /></Field></div><label className="flex items-center gap-2 text-sm text-text-muted"><input type="checkbox" checked={shorten} onChange={(e) => setShorten(e.target.checked)} />Create branded redirect</label>{shorten && <div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Field label="Short hostname"><input value={shortHostname} onChange={(e) => setShortHostname(e.target.value)} className={INPUT_CLASS} placeholder="go.example.com" /></Field><Field label="Short path"><input value={shortPath} onChange={(e) => setShortPath(e.target.value)} className={INPUT_CLASS} /></Field></div>}{status && <div className="text-xs text-text-dim break-words">{status}</div>}<div className="flex justify-end gap-2 pt-2"><button onClick={onClose} disabled={submitting} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button><button onClick={() => void submit()} disabled={submitting || !url || (manual && !affiliateURL)} className="px-3 py-1.5 bg-accent text-bg rounded text-sm disabled:opacity-50">{submitting ? "Creating..." : "Create"}</button></div></div></div></div>;
}

function CreateProductLinkDialog({ product, network, onClose, onDone }: { product: Product; network?: Network; onClose: () => void; onDone: (warning: string) => void }) {
  const [url, setUrl] = useState(product.destination_url || ""); const [affiliateURL, setAffiliateURL] = useState(product.affiliate_url || "");
  const [campaign, setCampaign] = useState(""); const [subid, setSubid] = useState("");
  const [shorten, setShorten] = useState(false); const [shortHostname, setShortHostname] = useState("");
  const [shortPath, setShortPath] = useState(`/${slug(product.merchant_name || product.name)}`); const [status, setStatus] = useState("");
  const [submitting, setSubmitting] = useState(false); const manual = network?.link_mode === "manual" && !product.affiliate_url;
  const submit = async () => { try { setSubmitting(true); setStatus("Creating..."); const data = await getJSON<{ warning?: string }>(`${API}/links`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ product_id: product.id, url, affiliate_url: affiliateURL, campaign, subid, shorten, short_hostname: shortHostname, short_path: shortPath }) }); onDone(data.warning || ""); } catch (error) { setStatus((error as Error).message); setSubmitting(false); } };
  return <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-3" onClick={onClose}><div role="dialog" aria-modal="true" className="bg-bg border border-border rounded-lg p-4 w-[560px] max-w-full max-h-full overflow-auto" onClick={(e) => e.stopPropagation()}><div className="flex items-center mb-3"><div className="font-medium flex-1 min-w-0 truncate">Create link for {product.name}</div><button aria-label="Close" title="Close" onClick={onClose} className="text-text-dim hover:text-text px-2">x</button></div><div className="space-y-3"><Field label="Destination URL"><input value={url} onChange={(e) => setUrl(e.target.value)} readOnly={Boolean(product.affiliate_url)} autoFocus className={`${INPUT_CLASS} read-only:opacity-70`} /></Field>{manual && <Field label="Affiliate URL"><input value={affiliateURL} onChange={(e) => setAffiliateURL(e.target.value)} className={INPUT_CLASS} placeholder="Paste the network tracking URL" /></Field>}<div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Field label="Campaign"><input value={campaign} onChange={(e) => setCampaign(e.target.value)} className={INPUT_CLASS} /></Field><Field label="Sub ID"><input value={subid} onChange={(e) => setSubid(e.target.value)} className={INPUT_CLASS} /></Field></div><label className="flex items-center gap-2 text-sm text-text-muted"><input type="checkbox" checked={shorten} onChange={(e) => setShorten(e.target.checked)} />Create branded redirect</label>{shorten && <div className="grid grid-cols-1 sm:grid-cols-2 gap-3"><Field label="Short hostname"><input value={shortHostname} onChange={(e) => setShortHostname(e.target.value)} className={INPUT_CLASS} placeholder="go.example.com" /></Field><Field label="Short path"><input value={shortPath} onChange={(e) => setShortPath(e.target.value)} className={INPUT_CLASS} /></Field></div>}{status && <div className="text-xs text-text-dim break-words">{status}</div>}<div className="flex justify-end gap-2 pt-2"><button onClick={onClose} disabled={submitting} className="px-3 py-1.5 text-sm text-text-muted">Cancel</button><button onClick={() => void submit()} disabled={submitting || !url || (manual && !affiliateURL)} className="px-3 py-1.5 bg-accent text-bg rounded text-sm disabled:opacity-50">{submitting ? "Creating..." : "Create"}</button></div></div></div></div>;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) { return <label className="block"><div className="text-xs text-text-dim mb-1">{label}</div>{children}</label>; }
function DataTable({ rowCount, empty, minWidth, children }: { rowCount: number; empty: string; minWidth: string; children: React.ReactNode }) { return <div className="p-3 sm:p-4">{rowCount > 0 ? <div className="border border-border rounded overflow-x-auto"><table className={`w-full border-collapse ${minWidth}`}>{children}</table></div> : <div className="text-center text-text-dim py-8">{empty}</div>}</div>; }
function Pager({ page, total, onPage }: { page: number; total: number; onPage: (page: number) => void }) { if (total <= PAGE_SIZE) return null; const pages = Math.ceil(total / PAGE_SIZE); return <div className="border-t border-border px-4 py-2 flex items-center justify-end gap-2 text-xs text-text-dim"><span>{page * PAGE_SIZE + 1}-{Math.min(total, (page + 1) * PAGE_SIZE)} of {total}</span><button aria-label="Previous page" title="Previous page" disabled={page === 0} onClick={() => onPage(page - 1)} className="border border-border rounded px-2 py-1 disabled:opacity-40">&lt;</button><button aria-label="Next page" title="Next page" disabled={page >= pages - 1} onClick={() => onPage(page + 1)} className="border border-border rounded px-2 py-1 disabled:opacity-40">&gt;</button></div>; }

function labelNetwork(key: string): string { const labels: Record<string, string> = { "target-circle": "Target Circle (Circlewise)", impact: "Impact", awin: "Awin", "cj-affiliate": "CJ Affiliate", "amazon-associates": "Amazon Associates", "ebay-partner-network": "eBay Partner Network", "rakuten-advertising": "Rakuten Advertising", skimlinks: "Skimlinks", sovrn: "Sovrn", partnerstack: "PartnerStack", shareasale: "ShareASale" }; return labels[key] || key; }
function statsCapabilityLabel(mode?: string): string { if (mode === "current-day") return "Stats (today)"; if (mode === "daily") return "Stats (daily)"; return "Stats"; }
function timeAgo(value: string): string { const time = new Date(value).getTime(); if (!Number.isFinite(time)) return "-"; const seconds = Math.max(0, Math.floor((Date.now() - time) / 1000)); if (seconds < 60) return `${seconds}s ago`; if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`; if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`; return `${Math.floor(seconds / 86400)}d ago`; }
function hostname(value?: string): string { try { return value ? new URL(value).hostname : ""; } catch { return ""; } }
function moneyToCents(value: string): number | null { if (!value.trim()) return 0; const amount = Number(value); return Number.isFinite(amount) && amount >= 0 ? Math.round(amount * 100) : null; }
function money(cents: number, currency: string): string { return currency ? `${currency} ${(cents / 100).toFixed(2)}` : "-"; }
function moneySummary(values: Record<string, { revenue: number; commission: number }>, key: "revenue" | "commission"): string { const rows = Object.entries(values); if (!rows.length) return "-"; return rows.map(([currency, amounts]) => money(amounts[key], currency)).join(" + "); }
function productPrice(product: Product): string { const current = product.sale_price_cents || product.price_cents; if (!current || !product.currency) return "-"; const formatted = money(current, product.currency); return product.sale_price_cents > 0 && product.price_cents > product.sale_price_cents ? `${formatted} (was ${money(product.price_cents, product.currency)})` : formatted; }
function linkOfferTitle(link: Link): string { if (link.product_name) return [link.product_name, link.merchant_name].filter(Boolean).join(" - "); if (link.merchant_name && link.offer_name && link.merchant_name !== link.offer_name) return `${link.merchant_name} - ${link.offer_name}`; return link.merchant_name || link.offer_name || (link.offer_id ? `Offer #${link.offer_id}` : "Unassigned"); }
function slug(value: string): string { return value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "") || "affiliate-link"; }
