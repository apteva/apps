import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Invoice} from "../shared";
import {type Customer} from "../shared";
import {type LineDraft} from "../shared";
import {emptyLine} from "../shared";
import {roundMoney} from "../shared";
import {ModalCloseButton} from "./ModalCloseButton";
import {fmtMoney} from "../shared";

export // ─── Edit invoice modal (status-aware) ──────────────────────────────
//
// Drafts: full edit — customer, currency, line items, notes, due_date.
// Finalized (open/paid/uncollectible): notes + due_date only — those
// are the fields the server's dbInvoiceUpdate still accepts post-
// finalize. Void invoices can't be edited at all; the panel hides the
// Edit button for them and the backend rejects voided patches.
//
// Pre-populates from the invoice. Line items live as decimal strings
// for editing comfort, then convert to integer cents on submit (matches
// CreateInvoiceModal's quirk).
function EditInvoiceModal({ apiCall, invoice, onClose, onUpdated, }: {
    apiCall: ApiCall;
    invoice: Invoice;
    onClose: () => void;
    onUpdated: (invoice: Invoice) => void;
}) {
    const isDraft = invoice.status === "draft";
    const [accountingDate, setAccountingDate] = useState(invoice.accounting_date || "");
    const [taxTreatment, setTaxTreatment] = useState(invoice.tax_treatment || "standard");
    // Always-editable fields.
    const [notes, setNotes] = useState(invoice.notes || "");
    const [dueDate, setDueDate] = useState(invoice.due_date ? invoice.due_date.slice(0, 10) : "");
    // Draft-only fields.
    const [customer, setCustomer] = useState<Customer | null>(null);
    const [customerSearch, setCustomerSearch] = useState("");
    const [customerResults, setCustomerResults] = useState<Customer[]>([]);
    const [searching, setSearching] = useState(false);
    const [pickerOpen, setPickerOpen] = useState(false);
    const [currency, setCurrency] = useState(invoice.currency || "");
    const [items, setItems] = useState<LineDraft[]>(isDraft && invoice.line_items && invoice.line_items.length > 0
        ? invoice.line_items.map((li) => ({
            price_id: li.price_id, product_id: li.product_id, metadata: li.metadata,
            description: li.description,
            quantity: String(li.quantity),
            unit_price: (li.unit_price_cents / 100).toFixed(2),
            tax_rate: (li.tax_rate_bps / 100).toString(),
        }))
        : [emptyLine()]);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState("");
    // Load the current customer on mount so the picker shows who we're
    // billing without making the user search again. Only matters for
    // drafts (since non-drafts hide the picker).
    useEffect(() => {
        if (!isDraft)
            return;
        let cancelled = false;
        (async () => {
            try {
                const res = await apiCall<{
                    customer: Customer;
                }>("GET", `/customers/${invoice.customer_id}`);
                if (!cancelled)
                    setCustomer(res.customer);
            }
            catch {
                // Customer might have been soft-deleted — leave picker empty
                // and let the user pick a replacement.
            }
        })();
        return () => {
            cancelled = true;
        };
    }, [apiCall, invoice.customer_id, isDraft]);
    // Debounced customer search (only when picker is open).
    useEffect(() => {
        if (!isDraft || !pickerOpen)
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
    }, [customerSearch, isDraft, pickerOpen, apiCall]);
    const setItem = (i: number, patch: Partial<LineDraft>) => setItems((prev) => prev.map((it, j) => (j === i ? { ...it, ...patch } : it)));
    const addItem = () => setItems((prev) => [...prev, emptyLine()]);
    const removeItem = (i: number) => setItems((prev) => (prev.length <= 1 ? prev : prev.filter((_, j) => j !== i)));
    // Live totals preview (drafts only).
    const previewCurrency = (currency || "USD").toUpperCase();
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
        const patch: Record<string, unknown> = {
            notes: notes,
            due_date: dueDate,
        };
        if (isDraft) {
            if (!customer) {
                setError("Pick a customer.");
                return;
            }
            const cur = currency.trim().toUpperCase();
            if (!cur || cur.length !== 3) {
                setError("Currency must be a 3-letter ISO code.");
                return;
            }
            let lineItems: Array<{
                price_id?: number;
                product_id?: number;
                metadata?: Record<string, unknown>;
                description: string;
                quantity: number;
                unit_price_cents: number;
                tax_rate_bps: number;
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
                        price_id: it.price_id, product_id: it.product_id, metadata: it.metadata,
                        description: desc,
                        quantity: qty,
                        unit_price_cents: roundMoney(unit * 100),
                        tax_rate_bps: isFinite(pct) ? roundMoney(pct * 100) : 0,
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
            patch.customer_id = customer.id;
            patch.currency = cur;
            patch.line_items = lineItems;
            patch.tax_treatment = taxTreatment;
        }
        patch.accounting_date = accountingDate;
        setSubmitting(true);
        try {
            const res = await apiCall<{
                invoice: Invoice;
            }>("PATCH", `/invoices/${invoice.id}`, patch);
            onUpdated(res.invoice);
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
        <header className="p-4 border-b border-border flex items-center justify-between sticky top-0 bg-bg z-10">
          <h2 className="text-text font-semibold">
            Edit {invoice.number || `draft #${invoice.id}`}
          </h2>
          <ModalCloseButton onClose={onClose}/>
        </header>

        <div className="p-4 space-y-4">
          {!isDraft && (<div className="text-xs text-text-muted border border-border bg-bg-input/50 rounded px-2 py-1">
              This invoice is <strong>{invoice.status}</strong>. Only notes and
              due date can be changed after finalization. To change line items
              or customer, void this invoice and create a new one.
            </div>)}

          {isDraft && (<>
              <div>
                <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                  Customer
                </label>
                {customer && !pickerOpen ? (<div className="flex items-center justify-between bg-bg-input border border-border rounded px-2 py-1">
                    <div className="text-sm text-text">
                      {customer.name}
                      {customer.email ? (<span className="text-text-muted">
                          {" "}
                          · {customer.email}
                        </span>) : null}
                    </div>
                    <button type="button" onClick={() => {
                    setPickerOpen(true);
                    setCustomerSearch("");
                }} className="text-xs text-accent hover:underline">
                      Change
                    </button>
                  </div>) : (<div className="space-y-1">
                    <input type="text" value={customerSearch} onChange={(e) => setCustomerSearch(e.target.value)} placeholder="Search customers by name or email…" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" autoFocus/>
                    {customerResults.length > 0 && (<ul className="border border-border rounded overflow-auto" style={{ maxHeight: "192px" }}>
                        {customerResults.map((c) => (<li key={c.id} onClick={() => {
                            setCustomer(c);
                            setPickerOpen(false);
                        }} className="px-2 py-1 cursor-pointer hover:bg-bg-input border-b border-border last:border-b-0">
                            <div className="text-sm text-text">{c.name}</div>
                            <div className="text-xs text-text-muted">
                              {c.email || "—"}
                            </div>
                          </li>))}
                      </ul>)}
                    {searching && customerResults.length === 0 && (<div className="text-xs text-text-dim">Searching…</div>)}
                  </div>)}
              </div>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                    Currency
                  </label>
                  <input type="text" value={currency} onChange={(e) => setCurrency(e.target.value.toUpperCase().slice(0, 3))} placeholder="EUR" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
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
                  <button type="button" onClick={addItem} className="text-xs text-accent hover:underline">
                    + Add line
                  </button>
                </div>
                <div className="space-y-2">
                  {items.map((it, i) => (<div key={i} className="bg-bg-input border border-border rounded p-2 space-y-2">
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
            </>)}

          {!isDraft && (<div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Due date
              </label>
              <input type="date" value={dueDate} onChange={(e) => setDueDate(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            </div>)}

          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Notes
            </label>
            <textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={3} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          </div>

          {error && <div className="text-sm text-red">{error}</div>}
        </div>

        <div className="p-4 flex gap-3"><label>Accounting date<input className="block bg-bg-input" type="date" value={accountingDate} onChange={e => setAccountingDate(e.target.value)}/></label>{isDraft && <label>Tax treatment<select className="block bg-bg-input" value={taxTreatment} onChange={e => setTaxTreatment(e.target.value)}><option value="standard">Standard</option><option value="reverse_charge">Reverse charge</option><option value="exempt">Exempt</option></select></label>}</div>
 <footer className="p-4 border-t border-border flex items-center justify-end gap-2 sticky bottom-0 bg-bg">
          <button type="button" onClick={onClose} disabled={submitting} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={submitting} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {submitting ? "Saving…" : "Save changes"}
          </button>
        </footer>
      </div>
    </div>);
}
