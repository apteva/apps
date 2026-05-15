// CatalogPanel — the catalog app's project.page panel.
//
// Two-column layout: left aside lists products (filterable by type +
// archived state, searchable); right main shows the selected product
// with its prices block. Prices have a financial-fields-locked edit
// modal that matches the server's immutability rule.
//
// API surface: /api/apps/catalog/* via the platform proxy. Money is
// kept as integer cents in flight; price modals convert to/from
// decimal for input comfort.

import { useCallback, useEffect, useRef, useState } from "react";

// ─── Inline app-events SSE hook ─────────────────────────────────────
// Same multiplexer pattern as billing/crm panels.

interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, handler);
    let lastSeq = 0;
    let es: EventSource | null = null;
    let cancelled = false;
    let reconnectTimer: number | null = null;
    const connect = () => {
      if (cancelled) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (lastSeq > 0 ? `&since=${lastSeq}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (e) => {
        try {
          const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
          if (ev.seq <= lastSeq) return;
          lastSeq = ev.seq;
          handlerRef.current(ev);
        } catch {}
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnectTimer) window.clearTimeout(reconnectTimer);
          reconnectTimer = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      cancelled = true;
      if (reconnectTimer) window.clearTimeout(reconnectTimer);
      if (es) es.close();
    };
  }, [app, projectId]);
}

// ─── Types ──────────────────────────────────────────────────────────

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type ProductType = "one_time" | "recurring" | "service";
type TaxCategory = "" | "standard" | "reduced" | "zero" | "exempt";

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
  tax_inclusive: boolean;
  metadata?: Record<string, unknown>;
  created_at?: string;
  archived_at?: string;
}

interface Product {
  id: number;
  name: string;
  slug?: string;
  description?: string;
  type: ProductType;
  category?: string;
  color?: string;
  tax_category?: TaxCategory;
  metadata?: Record<string, unknown>;
  created_at?: string;
  updated_at?: string;
  archived_at?: string;
  prices?: Price[];
}

type ApiCall = <T,>(
  method: string,
  path: string,
  body?: unknown,
  query?: Record<string, string>,
) => Promise<T>;

// ─── Formatters ─────────────────────────────────────────────────────

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
  const n = p.interval_count > 1 ? `every ${p.interval_count} ${p.interval}s` : `/${p.interval}`;
  return n;
}

function typeLabel(t: ProductType): string {
  if (t === "one_time") return "One-time";
  if (t === "recurring") return "Recurring";
  return "Service";
}

const TYPE_TONE: Record<ProductType, string> = {
  one_time: "bg-blue-500/15 text-blue-500",
  recurring: "bg-accent/15 text-accent",
  service: "bg-purple-500/15 text-purple-500",
};

const API = "/api/apps/catalog";

// ─── Panel root ─────────────────────────────────────────────────────

export default function CatalogPanel({ projectId, installId }: NativePanelProps) {
  const queryString = useCallback(
    (extra: Record<string, string> = {}) =>
      new URLSearchParams({
        project_id: projectId,
        install_id: String(installId),
        ...extra,
      }).toString(),
    [projectId, installId],
  );

  const apiCall = useCallback(
    async <T,>(
      method: string,
      path: string,
      body?: unknown,
      query: Record<string, string> = {},
    ): Promise<T> => {
      const r = await fetch(`${API}${path}?${queryString(query)}`, {
        method,
        credentials: "same-origin",
        headers: body ? { "Content-Type": "application/json" } : {},
        body: body ? JSON.stringify(body) : undefined,
      });
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      return r.json() as Promise<T>;
    },
    [queryString],
  );

  return <ProductsView projectId={projectId} apiCall={apiCall} />;
}

// ─── Products view ──────────────────────────────────────────────────

function ProductsView({ projectId, apiCall }: { projectId: string; apiCall: ApiCall }) {
  const [list, setList] = useState<Product[]>([]);
  const [filter, setFilter] = useState("");
  const [typeFilter, setTypeFilter] = useState<string>("");
  const [includeArchived, setIncludeArchived] = useState(false);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<Product | null>(null);
  const [status, setStatus] = useState("");

  const [showCreateProduct, setShowCreateProduct] = useState(false);
  const [showEditProduct, setShowEditProduct] = useState(false);
  const [showCreatePrice, setShowCreatePrice] = useState(false);
  const [editPrice, setEditPrice] = useState<Price | null>(null);

  const loadList = useCallback(async () => {
    setStatus("Loading…");
    try {
      const query: Record<string, string> = {};
      if (typeFilter) query.type = typeFilter;
      if (includeArchived) query.archived = "true";
      if (filter.trim()) query.q = filter.trim();
      const res = await apiCall<{ products: Product[] }>(
        "GET",
        "/products",
        undefined,
        query,
      );
      setList(res.products || []);
      const n = (res.products || []).length;
      setStatus(`${n} product${n === 1 ? "" : "s"}`);
    } catch (err) {
      setStatus(`Error: ${(err as Error).message}`);
    }
  }, [apiCall, filter, typeFilter, includeArchived]);

  useEffect(() => {
    loadList();
  }, [loadList]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const res = await apiCall<{ product: Product }>(
          "GET",
          `/products/${id}`,
        );
        setDetail(res.product);
      } catch (err) {
        setStatus(`Detail error: ${(err as Error).message}`);
      }
    },
    [apiCall],
  );

  useAppEvents("catalog", projectId, () => {
    loadList();
    if (selectedId) loadDetail(selectedId);
  });

  const select = useCallback(
    (id: number) => {
      setSelectedId(id);
      loadDetail(id);
    },
    [loadDetail],
  );

  const archiveProduct = async () => {
    if (!detail) return;
    if (!confirm(`Archive "${detail.name}"? Existing invoices keep their references; the product hides from new sales.`)) return;
    try {
      await apiCall("DELETE", `/products/${detail.id}`);
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Archive failed: ${(err as Error).message}`);
    }
  };

  const archivePrice = async (price: Price) => {
    if (!confirm(`Archive price "${price.nickname || `#${price.id}`}" (${fmtMoney(price.unit_amount_cents, price.currency)})?`)) return;
    try {
      await apiCall("DELETE", `/prices/${price.id}`);
      if (selectedId) await loadDetail(selectedId);
    } catch (err) {
      alert(`Archive failed: ${(err as Error).message}`);
    }
  };

  return (
    <div className="h-full flex">
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border space-y-2">
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Search products…"
              className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <button
              type="button"
              onClick={() => setShowCreateProduct(true)}
              className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
            >
              + New
            </button>
          </div>
          <div className="flex items-center gap-2">
            <select
              value={typeFilter}
              onChange={(e) => setTypeFilter(e.target.value)}
              className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            >
              <option value="">All types</option>
              <option value="recurring">Recurring</option>
              <option value="one_time">One-time</option>
              <option value="service">Service</option>
            </select>
            <label className="text-xs text-text-muted flex items-center gap-1">
              <input
                type="checkbox"
                checked={includeArchived}
                onChange={(e) => setIncludeArchived(e.target.checked)}
              />
              Archived
            </label>
          </div>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">No products.</div>
          ) : (
            <ul>
              {list.map((p) => (
                <li
                  key={p.id}
                  onClick={() => select(p.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    p.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm text-text font-medium truncate flex items-center gap-2">
                      {p.color && (
                        <span
                          className="inline-block w-2 h-2 rounded-full"
                          style={{ backgroundColor: p.color }}
                        />
                      )}
                      {p.name}
                      {p.archived_at && (
                        <span className="text-[10px] text-text-dim">(archived)</span>
                      )}
                    </span>
                    <span
                      className={`text-[10px] px-1.5 py-0.5 rounded ${TYPE_TONE[p.type]}`}
                    >
                      {typeLabel(p.type)}
                    </span>
                  </div>
                  {p.category && (
                    <div className="text-xs text-text-muted mt-0.5 truncate">
                      {p.category}
                    </div>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="p-2 text-xs text-text-dim border-t border-border">{status}</div>
      </aside>

      <main className="flex-1 overflow-auto p-6">
        {!detail ? (
          <div className="text-text-muted text-sm text-center mt-12">
            {selectedId ? "Loading…" : "Select a product to see details."}
          </div>
        ) : (
          <ProductDetail
            product={detail}
            onEdit={() => setShowEditProduct(true)}
            onArchive={archiveProduct}
            onAddPrice={() => setShowCreatePrice(true)}
            onEditPrice={(p) => setEditPrice(p)}
            onArchivePrice={archivePrice}
          />
        )}
      </main>

      {showCreateProduct && (
        <CreateProductModal
          apiCall={apiCall}
          onClose={() => setShowCreateProduct(false)}
          onCreated={(p) => {
            setShowCreateProduct(false);
            loadList();
            select(p.id);
          }}
        />
      )}

      {showEditProduct && detail && (
        <EditProductModal
          apiCall={apiCall}
          product={detail}
          onClose={() => setShowEditProduct(false)}
          onUpdated={(p) => {
            setShowEditProduct(false);
            setDetail(p);
            loadList();
          }}
        />
      )}

      {showCreatePrice && detail && (
        <CreatePriceModal
          apiCall={apiCall}
          product={detail}
          onClose={() => setShowCreatePrice(false)}
          onCreated={() => {
            setShowCreatePrice(false);
            if (selectedId) loadDetail(selectedId);
          }}
        />
      )}

      {editPrice && (
        <EditPriceModal
          apiCall={apiCall}
          price={editPrice}
          onClose={() => setEditPrice(null)}
          onUpdated={() => {
            setEditPrice(null);
            if (selectedId) loadDetail(selectedId);
          }}
        />
      )}
    </div>
  );
}

// ─── Product detail ─────────────────────────────────────────────────

function ProductDetail({
  product,
  onEdit,
  onArchive,
  onAddPrice,
  onEditPrice,
  onArchivePrice,
}: {
  product: Product;
  onEdit: () => void;
  onArchive: () => void;
  onAddPrice: () => void;
  onEditPrice: (p: Price) => void;
  onArchivePrice: (p: Price) => void;
}) {
  return (
    <div className="max-w-3xl space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl text-text font-semibold flex items-center gap-2">
            {product.color && (
              <span
                className="inline-block w-3 h-3 rounded-full"
                style={{ backgroundColor: product.color }}
              />
            )}
            {product.name}
            <span className={`text-[11px] px-1.5 py-0.5 rounded ${TYPE_TONE[product.type]}`}>
              {typeLabel(product.type)}
            </span>
            {product.archived_at && (
              <span className="text-[11px] text-text-dim">archived</span>
            )}
          </h1>
          <p className="text-text-muted text-sm mt-1">
            {product.slug ? <code>{product.slug}</code> : "—"}
            {product.category ? ` · ${product.category}` : ""}
            {product.tax_category ? ` · tax: ${product.tax_category}` : ""}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {!product.archived_at && (
            <>
              <button
                type="button"
                onClick={onEdit}
                className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
              >
                Edit
              </button>
              <button
                type="button"
                onClick={onArchive}
                className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10"
              >
                Archive
              </button>
            </>
          )}
        </div>
      </header>

      {product.description && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-1">
            Description
          </h2>
          <p className="text-sm text-text whitespace-pre-wrap">{product.description}</p>
        </section>
      )}

      <section>
        <div className="flex items-center justify-between mb-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Prices ({product.prices?.length || 0})
          </h2>
          {!product.archived_at && (
            <button
              type="button"
              onClick={onAddPrice}
              className="text-xs text-accent hover:underline"
            >
              + Add price
            </button>
          )}
        </div>
        {!product.prices || product.prices.length === 0 ? (
          <p className="text-text-muted text-sm">
            No prices yet. Add one to start selling.
          </p>
        ) : (
          <ul className="space-y-1">
            {product.prices.map((price) => (
              <li
                key={price.id}
                className="flex items-center justify-between border border-border rounded px-3 py-2 text-sm gap-2"
              >
                <div className="min-w-0">
                  <div className="text-text truncate">
                    {price.nickname || (
                      <span className="text-text-dim">(no nickname)</span>
                    )}
                    {price.archived_at && (
                      <span className="ml-2 text-[10px] text-text-dim">archived</span>
                    )}
                    {!price.active && !price.archived_at && (
                      <span className="ml-2 text-[10px] text-yellow-500">inactive</span>
                    )}
                  </div>
                  <div className="text-xs text-text-muted mt-0.5">
                    {fmtInterval(price)}
                    {price.trial_days > 0 ? ` · ${price.trial_days}-day trial` : ""}
                    {price.tax_inclusive ? " · tax inclusive" : ""}
                  </div>
                </div>
                <div className="flex items-center gap-3 shrink-0">
                  <span className="text-text font-medium">
                    {fmtMoney(price.unit_amount_cents, price.currency)}
                  </span>
                  {!price.archived_at && (
                    <>
                      <button
                        type="button"
                        onClick={() => onEditPrice(price)}
                        className="text-xs text-text-muted hover:text-text"
                      >
                        Edit
                      </button>
                      <button
                        type="button"
                        onClick={() => onArchivePrice(price)}
                        className="text-xs text-text-muted hover:text-red"
                      >
                        Archive
                      </button>
                    </>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

// ─── Create product modal ───────────────────────────────────────────

function CreateProductModal({
  apiCall,
  onClose,
  onCreated,
}: {
  apiCall: ApiCall;
  onClose: () => void;
  onCreated: (p: Product) => void;
}) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [type, setType] = useState<ProductType>("recurring");
  const [description, setDescription] = useState("");
  const [category, setCategory] = useState("");
  const [color, setColor] = useState("#3b82f6");
  const [taxCategory, setTaxCategory] = useState<TaxCategory>("");

  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  // Auto-generate slug from name (only while user hasn't manually edited).
  const [slugTouched, setSlugTouched] = useState(false);
  useEffect(() => {
    if (slugTouched) return;
    setSlug(
      name
        .toLowerCase()
        .trim()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, ""),
    );
  }, [name, slugTouched]);

  const submit = async () => {
    setError("");
    if (!name.trim()) {
      setError("Name is required.");
      return;
    }
    const body: Record<string, unknown> = {
      name: name.trim(),
      type,
    };
    if (slug.trim()) body.slug = slug.trim();
    if (description.trim()) body.description = description.trim();
    if (category.trim()) body.category = category.trim();
    if (color) body.color = color;
    if (taxCategory) body.tax_category = taxCategory;

    setSubmitting(true);
    try {
      const res = await apiCall<{ product: Product }>("POST", "/products", body);
      onCreated(res.product);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <ModalShell title="New product" onClose={onClose}>
      <section className="space-y-2">
        <div className="grid grid-cols-2 gap-2">
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Name *"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            autoFocus
          />
          <input
            type="text"
            value={slug}
            onChange={(e) => {
              setSlug(e.target.value);
              setSlugTouched(true);
            }}
            placeholder="Slug (optional)"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
        </div>
        <div className="grid grid-cols-3 gap-2">
          <select
            value={type}
            onChange={(e) => setType(e.target.value as ProductType)}
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="recurring">Recurring</option>
            <option value="one_time">One-time</option>
            <option value="service">Service</option>
          </select>
          <input
            type="text"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            placeholder="Category"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <select
            value={taxCategory}
            onChange={(e) => setTaxCategory(e.target.value as TaxCategory)}
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="">Tax (none)</option>
            <option value="standard">standard</option>
            <option value="reduced">reduced</option>
            <option value="zero">zero</option>
            <option value="exempt">exempt</option>
          </select>
        </div>
        <div className="flex items-center gap-2">
          <label className="text-xs text-text-dim">Color</label>
          <input
            type="color"
            value={color}
            onChange={(e) => setColor(e.target.value)}
            className="w-10 h-7 border border-border rounded cursor-pointer"
          />
          <input
            type="text"
            value={color}
            onChange={(e) => setColor(e.target.value)}
            className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
        </div>
        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={3}
          placeholder="Description (optional)"
          className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
        />
      </section>
      <ModalFooter
        submitLabel={submitting ? "Creating…" : "Create product"}
        onClose={onClose}
        onSubmit={submit}
        submitting={submitting}
        error={error}
      />
    </ModalShell>
  );
}

// ─── Edit product modal ─────────────────────────────────────────────

function EditProductModal({
  apiCall,
  product,
  onClose,
  onUpdated,
}: {
  apiCall: ApiCall;
  product: Product;
  onClose: () => void;
  onUpdated: (p: Product) => void;
}) {
  const [name, setName] = useState(product.name);
  const [slug, setSlug] = useState(product.slug || "");
  const [type, setType] = useState<ProductType>(product.type);
  const [description, setDescription] = useState(product.description || "");
  const [category, setCategory] = useState(product.category || "");
  const [color, setColor] = useState(product.color || "#3b82f6");
  const [taxCategory, setTaxCategory] = useState<TaxCategory>(
    (product.tax_category as TaxCategory) || "",
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setError("");
    if (!name.trim()) {
      setError("Name is required.");
      return;
    }
    const patch: Record<string, unknown> = {
      name: name.trim(),
      slug: slug.trim(),
      type,
      description: description.trim(),
      category: category.trim(),
      color,
      tax_category: taxCategory,
    };
    setSubmitting(true);
    try {
      const res = await apiCall<{ product: Product }>(
        "PATCH",
        `/products/${product.id}`,
        patch,
      );
      onUpdated(res.product);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <ModalShell title="Edit product" onClose={onClose}>
      <section className="space-y-2">
        <div className="grid grid-cols-2 gap-2">
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Name *"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            autoFocus
          />
          <input
            type="text"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="Slug"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
        </div>
        <div className="grid grid-cols-3 gap-2">
          <select
            value={type}
            onChange={(e) => setType(e.target.value as ProductType)}
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="recurring">Recurring</option>
            <option value="one_time">One-time</option>
            <option value="service">Service</option>
          </select>
          <input
            type="text"
            value={category}
            onChange={(e) => setCategory(e.target.value)}
            placeholder="Category"
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <select
            value={taxCategory}
            onChange={(e) => setTaxCategory(e.target.value as TaxCategory)}
            className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="">Tax (none)</option>
            <option value="standard">standard</option>
            <option value="reduced">reduced</option>
            <option value="zero">zero</option>
            <option value="exempt">exempt</option>
          </select>
        </div>
        <div className="flex items-center gap-2">
          <label className="text-xs text-text-dim">Color</label>
          <input
            type="color"
            value={color}
            onChange={(e) => setColor(e.target.value)}
            className="w-10 h-7 border border-border rounded cursor-pointer"
          />
          <input
            type="text"
            value={color}
            onChange={(e) => setColor(e.target.value)}
            className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm font-mono"
          />
        </div>
        <textarea
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          rows={3}
          placeholder="Description"
          className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
        />
      </section>
      <ModalFooter
        submitLabel={submitting ? "Saving…" : "Save changes"}
        onClose={onClose}
        onSubmit={submit}
        submitting={submitting}
        error={error}
      />
    </ModalShell>
  );
}

// ─── Create price modal ─────────────────────────────────────────────

function CreatePriceModal({
  apiCall,
  product,
  onClose,
  onCreated,
}: {
  apiCall: ApiCall;
  product: Product;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [nickname, setNickname] = useState("");
  const [amount, setAmount] = useState("");
  const [currency, setCurrency] = useState(
    product.type === "recurring" ? "EUR" : "USD",
  );
  const [recurring, setRecurring] = useState(product.type === "recurring");
  const [interval, setInterval] = useState("month");
  const [intervalCount, setIntervalCount] = useState("1");
  const [trialDays, setTrialDays] = useState("0");
  const [taxInclusive, setTaxInclusive] = useState(false);

  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setError("");
    const value = parseFloat(amount);
    if (!isFinite(value) || value <= 0) {
      setError("Amount must be a positive number.");
      return;
    }
    const cents = Math.round(value * 100);
    const cur = currency.trim().toUpperCase();
    if (cur.length !== 3) {
      setError("Currency must be a 3-letter ISO code.");
      return;
    }
    const body: Record<string, unknown> = {
      product_id: product.id,
      unit_amount_cents: cents,
      currency: cur,
      tax_inclusive: taxInclusive,
    };
    if (nickname.trim()) body.nickname = nickname.trim();
    if (recurring) {
      body.interval = interval;
      const ic = parseInt(intervalCount, 10);
      if (isFinite(ic) && ic > 0) body.interval_count = ic;
      const td = parseInt(trialDays, 10);
      if (isFinite(td) && td > 0) body.trial_days = td;
    }
    setSubmitting(true);
    try {
      await apiCall("POST", "/prices", body);
      onCreated();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <ModalShell title={`New price for ${product.name}`} onClose={onClose}>
      <section className="space-y-3">
        <input
          type="text"
          value={nickname}
          onChange={(e) => setNickname(e.target.value)}
          placeholder="Nickname (internal label, e.g. 'Pro monthly')"
          className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          autoFocus
        />

        <div className="grid grid-cols-2 gap-2">
          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Amount
            </label>
            <input
              type="number"
              step="0.01"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              placeholder="29.00"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Currency
            </label>
            <input
              type="text"
              value={currency}
              onChange={(e) =>
                setCurrency(e.target.value.toUpperCase().slice(0, 3))
              }
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
        </div>

        <label className="flex items-center gap-2 text-sm text-text">
          <input
            type="checkbox"
            checked={recurring}
            onChange={(e) => setRecurring(e.target.checked)}
          />
          Recurring price (subscription)
        </label>

        {recurring && (
          <div className="grid grid-cols-3 gap-2 pl-6">
            <div>
              <label className="block text-xs text-text-dim mb-0.5">Every</label>
              <input
                type="number"
                min="1"
                value={intervalCount}
                onChange={(e) => setIntervalCount(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
            <div>
              <label className="block text-xs text-text-dim mb-0.5">Interval</label>
              <select
                value={interval}
                onChange={(e) => setInterval(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              >
                <option value="day">Day</option>
                <option value="week">Week</option>
                <option value="month">Month</option>
                <option value="year">Year</option>
              </select>
            </div>
            <div>
              <label className="block text-xs text-text-dim mb-0.5">Trial days</label>
              <input
                type="number"
                min="0"
                value={trialDays}
                onChange={(e) => setTrialDays(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
          </div>
        )}

        <label className="flex items-center gap-2 text-sm text-text">
          <input
            type="checkbox"
            checked={taxInclusive}
            onChange={(e) => setTaxInclusive(e.target.checked)}
          />
          Amount includes tax
        </label>

        <div className="text-xs text-yellow-500 border border-yellow-500/30 bg-yellow-500/10 rounded px-2 py-1">
          Once created, the price's amount / currency / interval can't be
          changed. To alter pricing, create a new price and archive this one.
        </div>
      </section>
      <ModalFooter
        submitLabel={submitting ? "Creating…" : "Create price"}
        onClose={onClose}
        onSubmit={submit}
        submitting={submitting}
        error={error}
      />
    </ModalShell>
  );
}

// ─── Edit price modal ───────────────────────────────────────────────
// Financial fields rendered read-only with explanation. Only nickname,
// active, tax_inclusive, metadata are editable — matches the server's
// dbPriceUpdate rule.

function EditPriceModal({
  apiCall,
  price,
  onClose,
  onUpdated,
}: {
  apiCall: ApiCall;
  price: Price;
  onClose: () => void;
  onUpdated: () => void;
}) {
  const [nickname, setNickname] = useState(price.nickname || "");
  const [active, setActive] = useState(price.active);
  const [taxInclusive, setTaxInclusive] = useState(price.tax_inclusive);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setError("");
    setSubmitting(true);
    try {
      await apiCall("PATCH", `/prices/${price.id}`, {
        nickname: nickname.trim(),
        active,
        tax_inclusive: taxInclusive,
      });
      onUpdated();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <ModalShell title="Edit price" onClose={onClose}>
      <section className="space-y-3">
        <div className="text-xs text-text-muted border border-border bg-bg-input/50 rounded px-2 py-1">
          <strong>Locked:</strong> amount, currency, interval. To change those,
          create a new price and archive this one.
        </div>

        <div className="grid grid-cols-2 gap-2 opacity-60">
          <div>
            <label className="block text-xs text-text-dim mb-0.5">Amount</label>
            <input
              type="text"
              value={fmtMoney(price.unit_amount_cents, price.currency)}
              disabled
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <div>
            <label className="block text-xs text-text-dim mb-0.5">Interval</label>
            <input
              type="text"
              value={fmtInterval(price)}
              disabled
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
        </div>

        <div>
          <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
            Nickname
          </label>
          <input
            type="text"
            value={nickname}
            onChange={(e) => setNickname(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            autoFocus
          />
        </div>

        <label className="flex items-center gap-2 text-sm text-text">
          <input
            type="checkbox"
            checked={active}
            onChange={(e) => setActive(e.target.checked)}
          />
          Active (available for new sales)
        </label>

        <label className="flex items-center gap-2 text-sm text-text">
          <input
            type="checkbox"
            checked={taxInclusive}
            onChange={(e) => setTaxInclusive(e.target.checked)}
          />
          Amount includes tax
        </label>
      </section>
      <ModalFooter
        submitLabel={submitting ? "Saving…" : "Save changes"}
        onClose={onClose}
        onSubmit={submit}
        submitting={submitting}
        error={error}
      />
    </ModalShell>
  );
}

// ─── Shared modal shell ─────────────────────────────────────────────

function ModalShell({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-6"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg w-full overflow-auto"
        style={{ maxWidth: "560px", maxHeight: "90vh" }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="p-4 border-b border-border flex items-center justify-between sticky top-0 bg-bg z-10">
          <h2 className="text-text font-semibold">{title}</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="text-text-muted hover:text-text"
          >
            <svg
              width="16"
              height="16"
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
              strokeLinecap="round"
            >
              <path d="M4 4 L12 12" />
              <path d="M12 4 L4 12" />
            </svg>
          </button>
        </header>
        <div className="p-4 space-y-4">{children}</div>
      </div>
    </div>
  );
}

function ModalFooter({
  submitLabel,
  onClose,
  onSubmit,
  submitting,
  error,
}: {
  submitLabel: string;
  onClose: () => void;
  onSubmit: () => void;
  submitting: boolean;
  error?: string;
}) {
  return (
    <>
      {error && <div className="text-sm text-red px-4 pb-2">{error}</div>}
      <footer className="-mx-4 -mb-4 p-4 border-t border-border flex items-center justify-end gap-2 bg-bg">
        <button
          type="button"
          onClick={onClose}
          disabled={submitting}
          className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={onSubmit}
          disabled={submitting}
          className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
        >
          {submitLabel}
        </button>
      </footer>
    </>
  );
}
