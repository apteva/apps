import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Customer} from "../shared";
import {type Invoice} from "../shared";
import {type Payment} from "../shared";
import {useAppEvents} from "../shared";
import {Stat} from "./Stat";
import {fmtMoney} from "../shared";
import {fmtDateTime} from "../shared";
import {CreateCustomerModal} from "./CreateCustomerModal";
import {EditCustomerModal} from "./EditCustomerModal";

export // ─── Customers tab ──────────────────────────────────────────────────
function CustomersTab({ projectId, apiCall, }: {
    projectId: string;
    apiCall: ApiCall;
}) {
    const [page, setPage] = useState(0);
    const listRequest = useRef(0), detailRequest = useRef(0);
    const [list, setList] = useState<Customer[]>([]);
    const [search, setSearch] = useState("");
    useEffect(() => setPage(0), [search]);
    const [selected, setSelected] = useState<Customer | null>(null);
    const [openInvoices, setOpenInvoices] = useState<Invoice[]>([]);
    const [recentPayments, setRecentPayments] = useState<Payment[]>([]);
    const [lifetime, setLifetime] = useState<{
        by_currency?: Array<{
            currency: string;
            invoiced_cents: number;
            paid_cents: number;
            outstanding_cents: number;
        }>;
    } | null>(null);
    const [status, setStatus] = useState("");
    const [showCreate, setShowCreate] = useState(false);
    const [showEdit, setShowEdit] = useState(false);
    const load = useCallback(async (q = "") => {
        const request = ++listRequest.current;
        setStatus("Loading…");
        try {
            const res = await apiCall<{
                customers: Customer[];
            }>("GET", "/customers", undefined, { q, offset: String(page * 50), limit: "50" });
            if (request !== listRequest.current)
                return;
            setList(res.customers || []);
            setStatus(`${(res.customers || []).length} customer${(res.customers || []).length === 1 ? "" : "s"}`);
        }
        catch (err) {
            if (request === listRequest.current)
                setStatus(`Error: ${(err as Error).message}`);
        }
    }, [apiCall, page]);
    useEffect(() => {
        load(search.trim());
    }, [load]);
    useEffect(() => {
        const t = setTimeout(() => load(search.trim()), 250);
        return () => clearTimeout(t);
    }, [search, load]);
    useAppEvents("billing", projectId, (ev) => {
        if (ev.topic === "customer.added" ||
            ev.topic === "customer.updated" ||
            ev.topic === "customer.deleted" ||
            ev.topic === "customer.merged") {
            load(search.trim());
        }
    });
    const select = async (c: Customer) => {
        const request = ++detailRequest.current;
        setSelected(c);
        setOpenInvoices([]);
        setRecentPayments([]);
        setLifetime(null);
        try {
            const res = await apiCall<{
                open_invoices: Invoice[];
                recent_payments: Payment[];
                lifetime: {
                    by_currency?: Array<{
                        currency: string;
                        invoiced_cents: number;
                        paid_cents: number;
                        outstanding_cents: number;
                    }>;
                };
            }>("GET", `/customers/${c.id}/context`);
            if (request !== detailRequest.current)
                return;
            setOpenInvoices(res.open_invoices || []);
            setRecentPayments(res.recent_payments || []);
            setLifetime(res.lifetime || null);
        }
        catch (err) {
            if (request === detailRequest.current)
                setStatus(`Detail error: ${(err as Error).message}`);
        }
    };
    const onCreated = async (c: Customer) => {
        setShowCreate(false);
        await load("");
        select(c);
    };
    const onUpdated = async (c: Customer) => {
        setShowEdit(false);
        setSelected(c);
        await load(search.trim());
    };
    const currency = selected?.currency || "USD";
    return (<div className="h-full flex">
      <aside className="w-80 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border flex items-center gap-2">
          <input type="text" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search customers…" className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
          <button type="button" onClick={() => setShowCreate(true)} className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
            + New
          </button>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (<div className="p-4 text-text-muted text-xs">No customers.</div>) : (<ul>
              {list.map((c) => (<li key={c.id} onClick={() => select(c)} className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${selected?.id === c.id ? "bg-bg-input" : ""}`}>
                  <div className="text-sm text-text font-medium truncate">
                    {c.name}
                  </div>
                  <div className="text-xs text-text-muted truncate">
                    {c.email || "—"}
                  </div>
                </li>))}
            </ul>)}
        </div>
        <div className="p-2 text-xs text-text-dim border-t border-border">
          {status}<div className="flex gap-3 mt-2"><button disabled={page === 0} onClick={() => setPage(p => p - 1)}>Previous</button><span>Page {page + 1}</span><button disabled={list.length < 50} onClick={() => setPage(p => p + 1)}>Next</button></div>
        </div>
      </aside>

      <main className="flex-1 overflow-auto p-6">
        {!selected ? (<div className="text-text-muted text-sm text-center mt-12">
            Select a customer to see open invoices + payment history.
          </div>) : (<div className="max-w-2xl space-y-6">
            <header className="flex items-start justify-between gap-4">
              <div>
                <h1 className="text-xl text-text font-semibold">
                  {selected.name}
                </h1>
                <p className="text-text-muted text-sm">
                  {selected.email || "—"}
                  {selected.phone ? ` · ${selected.phone}` : ""}
                  {selected.currency ? ` · ${selected.currency}` : ""}
                </p>
              </div>
              <button type="button" onClick={() => setShowEdit(true)} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
                Edit
              </button>
            </header>

        {lifetime?.by_currency?.map(group => <section key={group.currency} className="grid grid-cols-3 gap-2"><Stat label={`Invoiced (${group.currency})`} value={fmtMoney(group.invoiced_cents, group.currency)}/><Stat label="Paid" value={fmtMoney(group.paid_cents, group.currency)}/><Stat label="Outstanding" value={fmtMoney(group.outstanding_cents, group.currency)}/></section>)}

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                Open invoices ({openInvoices.length})
              </h2>
              {openInvoices.length === 0 ? (<p className="text-text-muted text-sm">None.</p>) : (<ul className="space-y-1">
                  {openInvoices.map((inv) => (<li key={inv.id} className="flex items-center justify-between border border-border rounded px-2 py-1 text-sm">
                      <span>{inv.number || `Draft #${inv.id}`}</span>
                      <span className="text-text-muted">
                        {fmtMoney(inv.total_cents, inv.currency)}
                      </span>
                    </li>))}
                </ul>)}
            </section>

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                Recent payments ({recentPayments.length})
              </h2>
              {recentPayments.length === 0 ? (<p className="text-text-muted text-sm">None.</p>) : (<ul className="space-y-1">
                  {recentPayments.map((p) => (<li key={p.id} className="flex items-center justify-between border border-border rounded px-2 py-1 text-sm">
                      <span className="flex items-center gap-2">
                        <span className="text-[10px] uppercase text-text-dim">
                          {p.method}
                        </span>
                        {fmtDateTime(p.received_at)}
                      </span>
                      <span>{fmtMoney(p.amount_cents, p.currency)}</span>
                    </li>))}
                </ul>)}
            </section>
          </div>)}
      </main>

      {showCreate && (<CreateCustomerModal apiCall={apiCall} onClose={() => setShowCreate(false)} onCreated={onCreated}/>)}

      {showEdit && selected && (<EditCustomerModal apiCall={apiCall} customer={selected} onClose={() => setShowEdit(false)} onUpdated={onUpdated}/>)}
    </div>);
}
