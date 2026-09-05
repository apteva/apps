import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Customer} from "../shared";

export function PaymentMethodSetupModal({ apiCall, onClose, onCreated, }: {
    apiCall: ApiCall;
    onClose: () => void;
    onCreated: (message: string) => Promise<void>;
}) {
    const [query, setQuery] = useState("");
    const [customers, setCustomers] = useState<Customer[]>([]);
    const [selected, setSelected] = useState<Customer | null>(null);
    const [loadingCustomers, setLoadingCustomers] = useState(false);
    const [card, setCard] = useState(true);
    const [sepaDebit, setSepaDebit] = useState(false);
    const [setDefault, setSetDefault] = useState(true);
    const [showAdvanced, setShowAdvanced] = useState(false);
    const [successUrl, setSuccessUrl] = useState("");
    const [cancelUrl, setCancelUrl] = useState("");
    const [error, setError] = useState("");
    const [submitting, setSubmitting] = useState(false);
    useEffect(() => {
        if (selected)
            return;
        let cancelled = false;
        const timer = window.setTimeout(async () => {
            setLoadingCustomers(true);
            try {
                const res = await apiCall<{
                    customers: Customer[];
                }>("GET", "/customers", undefined, query.trim() ? { q: query.trim() } : { limit: "20" });
                if (!cancelled)
                    setCustomers((res.customers || []).slice(0, 20));
            }
            catch {
                if (!cancelled)
                    setCustomers([]);
            }
            finally {
                if (!cancelled)
                    setLoadingCustomers(false);
            }
        }, 180);
        return () => {
            cancelled = true;
            window.clearTimeout(timer);
        };
    }, [apiCall, query, selected]);
    const paymentTypes = () => {
        const types: string[] = [];
        if (card)
            types.push("card");
        if (sepaDebit)
            types.push("sepa_debit");
        return types;
    };
    const submit = async () => {
        setError("");
        if (!selected) {
            setError("Pick a customer.");
            return;
        }
        const types = paymentTypes();
        if (types.length === 0) {
            setError("Pick at least one payment type.");
            return;
        }
        setSubmitting(true);
        try {
            const body: Record<string, unknown> = {
                customer_id: selected.id,
                payment_method_types: types,
                set_default: setDefault,
            };
            if (successUrl.trim())
                body.success_url = successUrl.trim();
            if (cancelUrl.trim())
                body.cancel_url = cancelUrl.trim();
            const res = await apiCall<{
                url?: string;
            }>("POST", "/setup-sessions", body);
            if (res.url) {
                window.open(res.url, "_blank", "noopener,noreferrer");
                await onCreated("Setup link opened.");
            }
            else {
                await onCreated("Setup session created.");
            }
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
          <h2 className="text-text font-semibold">New setup link</h2>
          <button type="button" onClick={onClose} aria-label="Close" className="text-text-muted hover:text-text">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
              <path d="M4 4 L12 12"/>
              <path d="M12 4 L4 12"/>
            </svg>
          </button>
        </header>

        <div className="p-4 space-y-4">
          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Customer
            </h3>
            {selected ? (<div className="border border-border rounded p-3 flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="text-sm text-text truncate">{selected.name}</div>
                  <div className="text-xs text-text-muted truncate">
                    {selected.email || `Customer #${selected.id}`}
                  </div>
                </div>
                <button type="button" onClick={() => {
                setSelected(null);
                setQuery("");
            }} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">
                  Change
                </button>
              </div>) : (<>
                <input type="text" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search customers…" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
                <div className="border border-border rounded max-h-48 overflow-auto">
                  {loadingCustomers && (<div className="p-3 text-xs text-text-muted">Loading…</div>)}
                  {!loadingCustomers && customers.length === 0 && (<div className="p-3 text-xs text-text-muted">No customers.</div>)}
                  {!loadingCustomers &&
                customers.map((c) => (<button key={c.id} type="button" onClick={() => setSelected(c)} className="w-full text-left px-3 py-2 border-b border-border last:border-b-0 hover:bg-bg-input">
                        <div className="text-sm text-text truncate">{c.name}</div>
                        <div className="text-xs text-text-muted truncate">
                          {c.email || `Customer #${c.id}`}
                        </div>
                      </button>))}
                </div>
              </>)}
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Payment types
            </h3>
            <div className="flex flex-wrap gap-3">
              <label className="flex items-center gap-2 text-sm text-text">
                <input type="checkbox" checked={card} onChange={(e) => setCard(e.target.checked)}/>
                Card
              </label>
              <label className="flex items-center gap-2 text-sm text-text">
                <input type="checkbox" checked={sepaDebit} onChange={(e) => setSepaDebit(e.target.checked)}/>
                SEPA debit
              </label>
              <label className="flex items-center gap-2 text-sm text-text">
                <input type="checkbox" checked={setDefault} onChange={(e) => setSetDefault(e.target.checked)}/>
                Default
              </label>
            </div>
          </section>

          <section className="space-y-2">
            <button type="button" onClick={() => setShowAdvanced((v) => !v)} className="text-xs text-accent hover:underline">
              {showAdvanced ? "Hide advanced" : "Advanced"}
            </button>
            {showAdvanced && (<div className="grid gap-2">
                <input type="url" value={successUrl} onChange={(e) => setSuccessUrl(e.target.value)} placeholder="Success URL" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
                <input type="url" value={cancelUrl} onChange={(e) => setCancelUrl(e.target.value)} placeholder="Cancel URL" className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
              </div>)}
          </section>

          {error && <div className="text-sm text-red">{error}</div>}
        </div>

        <footer className="p-4 border-t border-border flex items-center justify-end gap-2 sticky bottom-0 bg-bg">
          <button type="button" onClick={onClose} disabled={submitting} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Cancel
          </button>
          <button type="button" onClick={submit} disabled={submitting} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50">
            {submitting ? "Creating…" : "Create setup link"}
          </button>
        </footer>
      </div>
    </div>);
}
