import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type ApiCall} from "../shared";
import {type Invoice} from "../shared";
import {type InvoiceDatePreset} from "../shared";
import {invoiceDateRange} from "../shared";
import {useAppEvents} from "../shared";
import {STATUS_TONE} from "../shared";
import {fmtMoney} from "../shared";
import {fmtDate} from "../shared";
import {InvoiceDetail} from "./InvoiceDetail";
import {EditInvoiceModal} from "./EditInvoiceModal";
import {CreateInvoiceModal} from "./CreateInvoiceModal";
import {FinalizeConfirmModal} from "./FinalizeConfirmModal";
import {VoidInvoiceModal} from "./VoidInvoiceModal";
import {RecordPaymentModal} from "./RecordPaymentModal";
import {SendPaymentLinkModal} from "./SendPaymentLinkModal";

export function InvoicesTab({ projectId, apiCall, installId }: {
    projectId: string;
    apiCall: ApiCall;
    installId: number;
}) {
    const [list, setList] = useState<Invoice[]>([]);
    const [filter, setFilter] = useState<string>("");
    const [statusFilter, setStatusFilter] = useState<string>("");
    const [datePreset, setDatePreset] = useState<InvoiceDatePreset>("all");
    const [customSince, setCustomSince] = useState<string>("");
    const [customUntil, setCustomUntil] = useState<string>("");
    const [selectedId, setSelectedId] = useState<number | null>(null);
    const [detail, setDetail] = useState<Invoice | null>(null);
    const [status, setStatus] = useState<string>("");
    const [showCreate, setShowCreate] = useState<boolean>(false);
    const [showEdit, setShowEdit] = useState<boolean>(false);
    const [showFinalize, setShowFinalize] = useState<boolean>(false);
    const [showVoid, setShowVoid] = useState<boolean>(false);
    const [showPayment, setShowPayment] = useState<boolean>(false);
    const [showPaymentLink, setShowPaymentLink] = useState<boolean>(false);
    const [page, setPage] = useState(0);
    const listRequest = useRef(0), detailRequest = useRef(0);
    const visibleInvoices = list;
    useEffect(() => { setPage(0); }, [filter, statusFilter, datePreset, customSince, customUntil]);
    const loadList = useCallback(async () => {
        const request = ++listRequest.current;
        setStatus("Loading…");
        try {
            const query: Record<string, string> = { sort: "due_date", q: filter.trim(), limit: "50", offset: String(page * 50) };
            if (statusFilter)
                query.status = statusFilter;
            const range = invoiceDateRange(datePreset, customSince, customUntil);
            if (range.since)
                query.since = range.since;
            if (range.until)
                query.until = range.until;
            const res = await apiCall<{
                invoices: Invoice[];
            }>("GET", "/invoices", undefined, query);
            if (request !== listRequest.current)
                return;
            const invoices = res.invoices || [];
            setList(invoices);
            setStatus(`${invoices.length} invoice${invoices.length === 1 ? "" : "s"}`);
        }
        catch (err) {
            if (request === listRequest.current)
                setStatus(`Error: ${(err as Error).message}`);
        }
    }, [apiCall, customSince, customUntil, datePreset, statusFilter, filter, page]);
    useEffect(() => {
        loadList();
    }, [loadList]);
    useAppEvents("billing", projectId, () => {
        loadList();
        if (selectedId)
            loadDetail(selectedId);
    });
    const loadDetail = useCallback(async (id: number) => {
        const request = ++detailRequest.current;
        try {
            const res = await apiCall<{
                invoice: Invoice;
            }>("GET", `/invoices/${id}`);
            if (request !== detailRequest.current)
                return;
            setDetail(res.invoice);
        }
        catch (err) {
            if (request === detailRequest.current)
                setStatus(`Detail error: ${(err as Error).message}`);
        }
    }, [apiCall]);
    const select = useCallback((id: number) => {
        setSelectedId(id);
        setDetail(null);
        loadDetail(id);
    }, [loadDetail]);
    const editInvoice = () => {
        if (detail)
            setShowEdit(true);
    };
    const onInvoiceUpdated = async (inv: Invoice) => {
        setShowEdit(false);
        setDetail(inv);
        await loadList();
    };
    const finalize = () => {
        if (detail)
            setShowFinalize(true);
    };
    const voidIt = () => {
        if (detail)
            setShowVoid(true);
    };
    const recordPayment = () => {
        if (detail)
            setShowPayment(true);
    };
    const doFinalize = async () => {
        if (!detail)
            return;
        await apiCall("POST", `/invoices/${detail.id}/finalize`);
        await loadList();
        await loadDetail(detail.id);
    };
    const doVoid = async (reason: string) => {
        if (!detail)
            return;
        await apiCall("POST", `/invoices/${detail.id}/void`, { reason });
        await loadList();
        await loadDetail(detail.id);
    };
    const doRecordPayment = async (amountCents: number, method: string, notes: string, receivedAt: string) => {
        if (!detail)
            return;
        const body: Record<string, unknown> = {
            invoice_id: detail.id,
            amount_cents: amountCents,
            method,
        };
        if (notes)
            body.notes = notes;
        if (receivedAt)
            body.received_at = new Date(receivedAt).toISOString();
        await apiCall("POST", "/payments", body);
        await loadList();
        await loadDetail(detail.id);
    };
    // Calls invoices_send_payment_link via the MCP HTTP gateway —
    // the panel uses its own /api/apps/billing/* surface; the MCP
    // tool surface lives at a separate dashboard endpoint. Simplest:
    // call the new tool through a small companion HTTP handler.
    // For v0.8.0 we just call the MCP gateway directly via fetch.
    const sendPaymentLink = () => {
        if (detail)
            setShowPaymentLink(true);
    };
    return (<div className="h-full flex">
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border space-y-2">
          <div className="flex items-center gap-2">
            <input type="text" value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="Search invoices…" className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"/>
            <button type="button" onClick={() => setShowCreate(true)} className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              + New
            </button>
          </div>
          <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm">
            <option value="">All statuses</option>
            <option value="draft">Draft</option>
            <option value="open">Open</option>
            <option value="paid">Paid</option>
            <option value="void">Void</option>
            <option value="uncollectible">Uncollectible</option>
          </select>
          <select value={datePreset} onChange={(e) => setDatePreset(e.target.value as InvoiceDatePreset)} className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm" title="Filter by invoice creation date">
            <option value="all">All dates</option>
            <option value="this_month">Created this month</option>
            <option value="last_month">Created last month</option>
            <option value="past_3_months">Created in past 3 months</option>
            <option value="custom">Custom created period</option>
          </select>
          {datePreset === "custom" && (<div className="grid grid-cols-2 gap-2">
              <input type="date" value={customSince} onChange={(e) => setCustomSince(e.target.value)} className="min-w-0 bg-bg-input border border-border rounded px-2 py-1 text-sm" aria-label="Created from"/>
              <input type="date" value={customUntil} onChange={(e) => setCustomUntil(e.target.value)} className="min-w-0 bg-bg-input border border-border rounded px-2 py-1 text-sm" aria-label="Created through"/>
            </div>)}
        </div>
        <div className="flex-1 overflow-auto">
		  {visibleInvoices.length === 0 ? (<div className="p-4 text-text-muted text-xs">No invoices.</div>) : (<ul>
			  {visibleInvoices.map((inv) => (<li key={inv.id} onClick={() => select(inv.id)} className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${inv.id === selectedId ? "bg-bg-input" : ""}`}>
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm text-text font-medium truncate">
                      {inv.number || `Draft #${inv.id}`}
                    </span>
                    <span className={`text-[10px] px-1.5 py-0.5 rounded ${STATUS_TONE[inv.status]}`}>
                      {inv.status}
                    </span>
                  </div>
                  <div className="text-xs text-text-muted mt-0.5 flex items-center justify-between gap-2">
                    <span className="truncate">
                      {inv.customer_name || `Customer #${inv.customer_id}`}
                    </span>
                    <span className="text-text">
                      {fmtMoney(inv.total_cents, inv.currency)}
                    </span>
                  </div>
                  <div className="text-[11px] text-text-dim mt-0.5">
                    {inv.due_date ? `Due ${fmtDate(inv.due_date)}` : "No due date"} - created{" "}
                    {fmtDate(inv.created_at)}
                  </div>
                </li>))}
            </ul>)}
        </div>
        <div className="p-2 text-xs text-text-dim border-t border-border">{status}<div className="flex gap-3 mt-2"><button disabled={page === 0} onClick={() => setPage(p => p - 1)}>Previous</button><span>Page {page + 1}</span><button disabled={list.length < 50} onClick={() => setPage(p => p + 1)}>Next</button></div></div>
      </aside>

      <main className="flex-1 overflow-auto p-6">
 {detail && <div className="flex gap-3 mb-3 text-xs">
 {detail.status === "draft" && <button onClick={async () => { try {
            await apiCall("DELETE", `/invoices/${detail.id}`);
            setDetail(null);
            setSelectedId(null);
            await loadList();
        }
        catch (e) {
            setStatus(String(e));
        } }}>Delete draft</button>}
 {detail.collection_hold && <button onClick={async () => { try {
            await apiCall("POST", `/invoices/${detail.id}/resume-collection`, {});
            await loadDetail(detail.id);
        }
        catch (e) {
            setStatus(String(e));
        } }}>Resume collection after refund</button>}
 {(detail.status === "open" || detail.status === "uncollectible") && <button onClick={async () => { try {
            await apiCall("POST", `/invoices/${detail.id}/cancel-payment`, {});
            await loadDetail(detail.id);
        }
        catch (e) {
            setStatus(String(e));
        } }}>Cancel active provider payment</button>}
 </div>}

        {!detail ? (<div className="text-text-muted text-sm text-center mt-12">
            {selectedId ? "Loading…" : "Select an invoice to see details."}
          </div>) : (<InvoiceDetail key={detail.id} apiCall={apiCall} installId={installId} invoice={detail} projectId={projectId} onEdit={editInvoice} onFinalize={finalize} onVoid={voidIt} onRecordPayment={recordPayment} onSendPaymentLink={sendPaymentLink}/>)}
      </main>

      {showEdit && detail && (<EditInvoiceModal apiCall={apiCall} invoice={detail} onClose={() => setShowEdit(false)} onUpdated={onInvoiceUpdated}/>)}

      {showCreate && (<CreateInvoiceModal apiCall={apiCall} projectId={projectId} onClose={() => setShowCreate(false)} onCreated={(inv) => {
                setShowCreate(false);
                loadList();
                select(inv.id);
            }}/>)}

      {showFinalize && detail && (<FinalizeConfirmModal invoice={detail} onConfirm={doFinalize} onClose={() => setShowFinalize(false)}/>)}

      {showVoid && detail && (<VoidInvoiceModal invoice={detail} onConfirm={doVoid} onClose={() => setShowVoid(false)}/>)}

      {showPayment && detail && (<RecordPaymentModal invoice={detail} onConfirm={doRecordPayment} onClose={() => setShowPayment(false)}/>)}

      {showPaymentLink && detail && (<SendPaymentLinkModal apiCall={apiCall} invoice={detail} projectId={projectId} onClose={() => setShowPaymentLink(false)}/>)}
    </div>);
}
