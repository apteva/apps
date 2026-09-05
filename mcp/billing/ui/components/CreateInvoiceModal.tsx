import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Invoice} from "../shared";
import {type Customer} from "../shared";
import {type LineDraft} from "../shared";
import {emptyLine} from "../shared";
import {type CatalogPriceOption} from "../shared";
import {roundMoney} from "../shared";
import {fetchCatalogPriceOptions} from "../shared";
import {fmtMoney} from "../shared";

export function CreateInvoiceModal({ apiCall, onClose, onCreated, projectId, }: {
    apiCall: ApiCall;
    onClose: () => void;
    onCreated: (invoice: Invoice) => void;
    projectId: string;
}) {
    const [customer, setCustomer] = useState<Customer | null>(null);
    const [customerSearch, setCustomerSearch] = useState("");
    const [customerResults, setCustomerResults] = useState<Customer[]>([]);
    const [searching, setSearching] = useState(false);
    const [currency, setCurrency] = useState("");
    const [dueDate, setDueDate] = useState("");
    const [notes, setNotes] = useState("");
    const [items, setItems] = useState<LineDraft[]>([emptyLine()]);
    const [catalogPicker, setCatalogPicker] = useState<{
        open: boolean;
        loading: boolean;
        error: string;
        options: CatalogPriceOption[];
    }>({ open: false, loading: false, error: "", options: [] });
    const [defaultCurrency, setDefaultCurrency] = useState("USD");
    useEffect(() => { let active = true; apiCall<{
        currency: string;
    }>("GET", "/defaults").then(d => { if (active && d.currency)
        setDefaultCurrency(d.currency); }).catch(() => { }); return () => { active = false; }; }, [apiCall]);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string>("");
    useEffect(() => {
        if (customer)
            return;
        const q = customerSearch.trim();
        let cancelled = false;
        const t = setTimeout(async () => {
            setSearching(true);
            try {
                const res = await apiCall<{
                    customers: Customer[];
                }>("GET", "/customers", undefined, q ? { q } : {});
                if (!cancelled)
                    setCustomerResults((res.customers || []).slice(0, 20));
            }
            catch {
                if (!cancelled)
                    setCustomerResults([]);
            }
            finally {
                if (!cancelled)
                    setSearching(false);
            }
        }, 200);
        return () => { cancelled = true; clearTimeout(t); };
    }, [customerSearch, customer, apiCall]);
    const setItem = (i: number, patch: Partial<LineDraft>) => setItems((prev) => prev.map((it, j) => (j === i ? { ...it, ...patch } : it)));
    const addItem = () => setItems((prev) => [...prev, emptyLine()]);
    const removeItem = (i: number) => setItems((prev) => (prev.length <= 1 ? prev : prev.filter((_, j) => j !== i)));
    const previewCurrency = (currency || customer?.currency || defaultCurrency).toUpperCase();
    let subtotal = 0;
    let taxTotal = 0;
    for (const it of items) {
        if (!it.description.trim())
            continue;
        const qty = parseFloat(it.quantity || "1");
        const unit = parseFloat(it.unit_price || "0");
        if (!isFinite(qty) || qty <= 0 || !isFinite(unit))
            continue;
        const lineCents = roundMoney(qty * unit * 100);
        subtotal += lineCents;
        const pct = parseFloat(it.tax_rate || "0");
        if (isFinite(pct) && pct > 0) {
            taxTotal += roundMoney((lineCents * pct) / 100);
        }
    }
    const total = subtotal + taxTotal;
    const submit = async () => {
        setError("");
        if (!customer) {
            setError("Pick a customer.");
            return;
        }
        let lineItems: Array<{
            description: string;
            quantity: number;
            unit_price_cents: number;
            tax_rate_bps: number;
            price_id?: number;
            product_id?: number;
        }> = [];
        try {
            lineItems = items
                .map((it, i) => {
                const desc = it.description.trim();
                if (!desc)
                    return null;
                const qty = parseFloat(it.quantity || "1");
                if (!isFinite(qty) || qty <= 0) {
                    throw new Error(`Line ${i + 1}: quantity must be > 0`);
                }
                const unit = parseFloat(it.unit_price || "0");
                if (!isFinite(unit)) {
                    throw new Error(`Line ${i + 1}: unit price required`);
                }
                const pct = parseFloat(it.tax_rate || "0");
                return {
                    description: desc,
                    quantity: qty,
                    unit_price_cents: roundMoney(unit * 100),
                    tax_rate_bps: isFinite(pct) ? roundMoney(pct * 100) : 0,
                    ...(it.price_id ? { price_id: it.price_id } : {}),
                    ...(it.product_id ? { product_id: it.product_id } : {}),
                };
            })
                .filter((x): x is NonNullable<typeof x> => x !== null);
        }
        catch (err) {
            setError((err as Error).message);
            return;
        }
        if (lineItems.length === 0) {
            setError("Add at least one line item with a description.");
            return;
        }
        setSubmitting(true);
        try {
            const body: Record<string, unknown> = {
                customer_id: customer.id,
                line_items: lineItems,
            };
            body.currency = previewCurrency;
            if (dueDate)
                body.due_date = dueDate;
            const trimmedNotes = notes.trim();
            if (trimmedNotes)
                body.notes = trimmedNotes;
            const res = await apiCall<{
                invoice: Invoice;
            }>("POST", "/invoices", body);
            onCreated(res.invoice);
        }
        catch (err) {
            setError((err as Error).message);
        }
        finally {
            setSubmitting(false);
        }
    };
    return (<div className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-6" onClick={onClose}>
      <div className="bg-bg border border-border rounded-lg w-full overflow-auto" style={{ maxWidth: "640px", maxHeight: "90vh" }} onClick={(e) => e.stopPropagation()}>
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">New invoice</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
              <path d="M4 4 L12 12"/>
              <path d="M12 4 L4 12"/>
            </svg>
          </button>
        </header>

        <div className="p-4 space-y-4">
          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Customer
            </label>
            {customer ? (<div className="flex items-center justify-between bg-bg-input border border-border rounded px-2 py-1">
                <div className="text-sm text-text">
                  {customer.name}
                  {customer.email ? (<span className="text-text-muted"> · {customer.email}</span>) : null}
                </div>
                <button type="button" onClick={() => {
                setCustomer(null);
                setCustomerSearch("");
            }} className="text-xs text-accent hover:underline">
                  Change
                </button>
              </div>) : (<div className="space-y-1">
                <input type="text" value={customerSearch} onChange={(e) => setCustomerSearch(e.target.value)} placeholder="Search customers by name or email…" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" autoFocus/>
                {customerResults.length > 0 && (<ul className="border border-border rounded overflow-auto" style={{ maxHeight: "192px" }}>
                    {customerResults.map((c) => (<li key={c.id} onClick={() => setCustomer(c)} className="px-2 py-1 cursor-pointer hover:bg-bg-input border-b border-border last:border-b-0">
                        <div className="text-sm text-text">{c.name}</div>
                        <div className="text-xs text-text-muted">
                          {c.email || "—"}
                        </div>
                      </li>))}
                  </ul>)}
                {searching && customerResults.length === 0 && (<div className="text-xs text-text-dim">Searching…</div>)}
                {!searching &&
                customerResults.length === 0 &&
                customerSearch.trim().length > 0 && (<div className="text-xs text-text-dim">No matches.</div>)}
              </div>)}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Currency
              </label>
              <input type="text" value={currency} onChange={(e) => setCurrency(e.target.value.toUpperCase().slice(0, 3))} placeholder={customer?.currency || "USD"} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Due date
              </label>
              <input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>
          </div>

          <div>
            <div className="flex items-center justify-between mb-1">
              <label className="text-xs uppercase tracking-wide text-text-dim">
                Line items
              </label>
              <div className="flex items-center gap-3">
                <button type="button" onClick={async () => {
            setCatalogPicker((p) => ({
                ...p,
                open: true,
                loading: true,
                error: "",
            }));
            try {
                const opts = await fetchCatalogPriceOptions(projectId);
                setCatalogPicker({
                    open: true,
                    loading: false,
                    error: "",
                    options: opts,
                });
            }
            catch (err) {
                setCatalogPicker({
                    open: true,
                    loading: false,
                    error: (err as Error).message,
                    options: [],
                });
            }
        }} className="text-xs text-accent hover:underline">
                  + From catalog
                </button>
                <button type="button" onClick={addItem} className="text-xs text-accent hover:underline">
                  + Add line
                </button>
              </div>
            </div>

            {catalogPicker.open && (<div className="mb-2 border border-border rounded bg-bg-input/50">
                <div className="px-2 py-1 border-b border-border flex items-center justify-between">
                  <span className="text-xs uppercase tracking-wide text-text-dim">
                    Pick a catalog price
                  </span>
                  <button type="button" onClick={() => setCatalogPicker((p) => ({ ...p, open: false }))} className="text-xs text-text-muted hover:text-text">
                    Close
                  </button>
                </div>
                {catalogPicker.loading ? (<div className="p-3 text-xs text-text-dim">Loading…</div>) : catalogPicker.error ? (<div className="p-3 text-xs text-yellow-500">
                    {catalogPicker.error}
                  </div>) : catalogPicker.options.length === 0 ? (<div className="p-3 text-xs text-text-dim">
                    No active catalog prices. Add one in the Catalog app first.
                  </div>) : (<ul className="overflow-auto" style={{ maxHeight: "240px" }}>
                    {catalogPicker.options.map((o) => (<li key={o.price_id} onClick={() => {
                        // Add a new line from this catalog option.
                        const dollars = (o.unit_amount_cents / 100).toFixed(2);
                        setItems((prev) => [
                            ...prev,
                            {
                                description: o.nickname || o.product_name,
                                quantity: "1",
                                unit_price: dollars,
                                tax_rate: "",
                                price_id: o.price_id,
                                product_id: o.product_id,
                                catalog_label: `${o.product_name}${o.nickname ? " — " + o.nickname : ""}`,
                            },
                        ]);
                        // Adopt the catalog's currency if invoice has none yet.
                        if (!currency.trim())
                            setCurrency(o.currency);
                        setCatalogPicker((p) => ({ ...p, open: false }));
                    }} className="px-2 py-1 cursor-pointer hover:bg-bg-input border-b border-border last:border-b-0 text-sm">
                        <div className="flex items-center gap-2">
                          {o.product_color && (<span className="inline-block w-2 h-2 rounded-full" style={{ backgroundColor: o.product_color }}/>)}
                          <span className="text-text truncate">
                            {o.product_name}
                            {o.nickname ? (<span className="text-text-muted">
                                {" "}
                                — {o.nickname}
                              </span>) : null}
                          </span>
                          <span className="ml-auto text-text">
                            {(o.unit_amount_cents / 100).toFixed(2)}{" "}
                            {o.currency}
                            {o.interval ? (<span className="text-text-dim">
                                /{o.interval}
                              </span>) : null}
                          </span>
                        </div>
                      </li>))}
                  </ul>)}
              </div>)}

            <div className="space-y-2">
              {items.map((it, i) => (<div key={i} className="bg-bg-input border border-border rounded p-2 space-y-2">
                  {it.catalog_label && (<div className="flex items-center gap-1 text-[10px] uppercase tracking-wide text-accent">
                      <svg width="10" height="10" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden="true">
                        <path d="M2 5 L8 2 L14 5 L8 8 Z"/>
                        <path d="M2 5 L2 11 L8 14 L14 11 L14 5"/>
                        <path d="M8 8 L8 14"/>
                      </svg>
                      <span>{it.catalog_label}</span>
                    </div>)}
                  <input type="text" value={it.description} onChange={(e) => setItem(i, { description: e.target.value })} placeholder="Description" className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"/>
                  <div className="grid grid-cols-3 gap-2">
                    <div>
                      <label className="block text-xs text-text-dim mb-0.5">
                        Qty
                      </label>
                      <input type="number" step="any" min="0" value={it.quantity} onChange={(e) => setItem(i, { quantity: e.target.value })} className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"/>
                    </div>
                    <div>
                      <label className="block text-xs text-text-dim mb-0.5">
                        Unit price
                      </label>
                      <input type="number" step="0.01" value={it.unit_price} onChange={(e) => setItem(i, { unit_price: e.target.value })} placeholder="0.00" className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"/>
                    </div>
                    <div>
                      <label className="block text-xs text-text-dim mb-0.5">
                        Tax %
                      </label>
                      <input type="number" step="0.01" value={it.tax_rate} onChange={(e) => setItem(i, { tax_rate: e.target.value })} placeholder="0" className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"/>
                    </div>
                  </div>
                  {items.length > 1 && (<div className="text-right">
                      <button type="button" onClick={() => removeItem(i)} className="text-xs text-red hover:underline">
                        Remove line
                      </button>
                    </div>)}
                </div>))}
            </div>
          </div>

          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Notes
            </label>
            <textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={2} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </div>

          <div className="border-t border-border pt-3 text-sm space-y-0.5">
            <div className="flex justify-between text-text-muted">
              <span>Subtotal</span>
              <span>{fmtMoney(subtotal, previewCurrency)}</span>
            </div>
            <div className="flex justify-between text-text-muted">
              <span>Tax</span>
              <span>{fmtMoney(taxTotal, previewCurrency)}</span>
            </div>
            <div className="flex justify-between text-text font-medium">
              <span>Total</span>
              <span>{fmtMoney(total, previewCurrency)}</span>
            </div>
          </div>

          {error && <div className="text-sm text-red">{error}</div>}
        </div>

        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} disabled={submitting} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={submitting || !customer} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {submitting ? "Creating…" : "Create draft"}
          </button>
        </footer>
      </div>
    </div>);
}
