import {useCallback,useEffect,useMemo,useRef,useState} from "react";
import {type Invoice} from "../shared";
import {type ApiCall} from "../shared";
import {type Payment} from "../shared";
import {type AuditEntry} from "../shared";
import {STATUS_TONE} from "../shared";
import {fmtDate} from "../shared";
import {fmtMoney} from "../shared";
import {fmtDateTime} from "../shared";

export function InvoiceDetail({ invoice, apiCall, installId, projectId, onEdit, onFinalize, onVoid, onRecordPayment, onSendPaymentLink, }: {
    invoice: Invoice;
    apiCall: ApiCall;
    installId: number;
    projectId: string;
    onEdit: () => void;
    onFinalize: () => void;
    onVoid: () => void;
    onRecordPayment: () => void;
    onSendPaymentLink: () => void;
}) {
    const remaining = Math.max(0, invoice.total_cents - invoice.amount_paid_cents);
    const [historyPage, setHistoryPage] = useState(0);
    const [history, setHistory] = useState<{
        payments?: Payment[];
 refunds?: {id:number;amount_cents:number;currency:string;status:string;error?:string;created_at:string}[];
 refunds_has_more?: boolean;
        audit_log?: AuditEntry[];
        payments_has_more?: boolean;
        audit_has_more?: boolean;
    }>({});
    const [historyError, setHistoryError] = useState("");
    useEffect(() => { let cancelled = false; setHistoryError(""); apiCall<typeof history>("GET", `/invoices/${invoice.id}/history`, undefined, { limit: "50", offset: String(historyPage * 50) }).then(data => { if (!cancelled)
        setHistory(data); }).catch(e => { if (!cancelled)
        setHistoryError(String(e)); }); return () => { cancelled = true; }; }, [invoice.id, invoice.updated_at, historyPage, apiCall]);
    const payments = history.payments || [], audit = history.audit_log || [];
    const pdfHref = `/api/apps/billing/invoices/${invoice.id}/pdf?project_id=${encodeURIComponent(projectId)}&install_id=${installId}`;
    const printHref = `/api/apps/billing/invoices/${invoice.id}/print?project_id=${encodeURIComponent(projectId)}&install_id=${installId}`;
    const canRender = invoice.status !== "draft";
    return (<div className="max-w-3xl space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl text-text font-semibold flex items-center gap-2">
            {invoice.number || `Draft #${invoice.id}`}
            <span className={`text-[11px] px-1.5 py-0.5 rounded ${STATUS_TONE[invoice.status]}`}>
              {invoice.status}
            </span>
            <span className="text-[10px] uppercase text-text-dim">
              {invoice.provider}
            </span>
          </h1>
          <p className="text-text-muted text-sm mt-1">
            {invoice.customer_name || `Customer #${invoice.customer_id}`}
            {invoice.due_date ? ` · due ${fmtDate(invoice.due_date)}` : ""}
          </p>
        </div>
        <div className="text-right">
          <div className="text-2xl text-text font-semibold">
            {fmtMoney(invoice.total_cents, invoice.currency)}
          </div>
          {invoice.amount_paid_cents > 0 && (<div className="text-xs text-text-muted">
              Paid {fmtMoney(invoice.amount_paid_cents, invoice.currency)} ·
              outstanding {fmtMoney(Math.max(0, remaining), invoice.currency)}
            </div>)}
        </div>
      </header>

      {invoice.line_items && invoice.line_items.length > 0 && (<section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Line items
          </h2>
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs">
              <tr className="border-b border-border">
                <th className="text-left font-normal py-1">Description</th>
                <th className="text-right font-normal py-1 w-16">Qty</th>
                <th className="text-right font-normal py-1 w-28">Unit</th>
                <th className="text-right font-normal py-1 w-20">Tax</th>
                <th className="text-right font-normal py-1 w-32">Amount</th>
              </tr>
            </thead>
            <tbody>
              {invoice.line_items.map((li, i) => (<tr key={li.id ?? i} className="border-b border-border/50">
                  <td className="py-1 text-text">{li.description}</td>
                  <td className="py-1 text-right text-text-muted">
                    {li.quantity}
                  </td>
                  <td className="py-1 text-right text-text-muted">
                    {fmtMoney(li.unit_price_cents, invoice.currency)}
                  </td>
                  <td className="py-1 text-right text-text-muted">
                    {(li.tax_rate_bps / 100).toFixed(2)}%
                  </td>
                  <td className="py-1 text-right text-text">
                    {fmtMoney(li.amount_cents, invoice.currency)}
                  </td>
                </tr>))}
            </tbody>
            <tfoot className="text-text">
              <tr>
                <td colSpan={4} className="text-right py-1 text-text-muted">
                  Subtotal
                </td>
                <td className="text-right py-1">
                  {fmtMoney(invoice.subtotal_cents, invoice.currency)}
                </td>
              </tr>
              <tr>
                <td colSpan={4} className="text-right py-1 text-text-muted">
                  Tax
                </td>
                <td className="text-right py-1">
                  {fmtMoney(invoice.tax_cents, invoice.currency)}
                </td>
              </tr>
              <tr className="font-medium">
                <td colSpan={4} className="text-right py-1">
                  Total
                </td>
                <td className="text-right py-1">
                  {fmtMoney(invoice.total_cents, invoice.currency)}
                </td>
              </tr>
            </tfoot>
          </table>
        </section>)}

      {invoice.notes && (<section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-1">
            Notes
          </h2>
          <p className="text-sm text-text whitespace-pre-wrap">{invoice.notes}</p>
        </section>)}

      {payments.length > 0 && (<section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Payments ({payments.length})
          </h2>
          <ul className="space-y-1">
            {payments.map((p) => (<li key={p.id} className="text-sm flex items-center justify-between border border-border rounded px-2 py-1">
                <span className="flex items-center gap-2">
                  <span className="text-[10px] uppercase text-text-dim w-12">
                    {p.method}
                  </span>
                  <span className="text-text">{fmtDateTime(p.received_at)}</span>
                </span>
                <span className={`text-text ${p.amount_cents < 0 ? "text-yellow-500" : ""}`}>
                  {fmtMoney(p.amount_cents, p.currency)}
                </span>
              </li>))}
          </ul>
        </section>)}

      {!!history.refunds?.length && <section><h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Refund requests</h2><ul className="space-y-1">{history.refunds.map(r => <li key={r.id} className="text-sm border border-border rounded px-2 py-1"><span>{fmtMoney(r.amount_cents,r.currency)} · {r.status} · {fmtDateTime(r.created_at)}</span>{r.error && <p className="text-red">{r.error}</p>}</li>)}</ul></section>}
      {audit.length > 0 && (<section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            History
          </h2>
          <ul className="space-y-1 text-xs text-text-muted">
            {audit.map((a) => (<li key={a.id} className="flex items-center gap-2 border-l-2 border-border pl-2 py-0.5">
                <span className="text-[10px] px-1 py-0.5 rounded bg-accent/10 text-accent uppercase">
                  {a.action}
                </span>
                <span>{fmtDateTime(a.created_at)}</span>
                <span className="text-text-dim">· {a.actor}</span>
              </li>))}
          </ul>
        </section>)}

    <section className="text-xs space-y-2"><p>Accounting date: {invoice.accounting_date || "Not set"} · Tax treatment: {invoice.tax_treatment || "standard"}</p>{invoice.collection_hold && <p className="text-yellow-500">Collection is on hold after a refund.</p>}{!!invoice.credit_cents && <p>Overpayment credit: {fmtMoney(invoice.credit_cents, invoice.currency)}</p>}{historyError && <p role="alert">{historyError}</p>}<div className="flex gap-3"><button disabled={historyPage === 0} onClick={() => setHistoryPage(p => p - 1)}>Previous history page</button><span>History page {historyPage + 1}</span><button disabled={!history.payments_has_more && !history.audit_has_more && !history.refunds_has_more} onClick={() => setHistoryPage(p => p + 1)}>Next history page</button></div></section>
      <div className="flex items-center gap-2 pt-2 border-t border-border">
        {invoice.status !== "void" && (<button type="button" onClick={onEdit} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
            Edit
          </button>)}
        {invoice.status === "draft" && (<button type="button" onClick={onFinalize} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
            Finalize
          </button>)}
        {(invoice.status === "open" || invoice.status === "uncollectible") && (<>
            <button type="button" onClick={onRecordPayment} className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg">
              Record payment
            </button>
            <button type="button" onClick={onSendPaymentLink} className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input" title="Generate a Stripe-hosted payment URL (requires payment_processor integration)">
              Send payment link
            </button>
          </>)}
        {canRender && (<>
            <a href={printHref} target="_blank" rel="noopener noreferrer" className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
              Print
            </a>
            <a href={pdfHref} target="_blank" rel="noopener noreferrer" className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input">
              Download PDF
            </a>
          </>)}
        {(invoice.status === "open" || invoice.status === "uncollectible") && (<button type="button" onClick={onVoid} className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 ml-auto">
            Void
          </button>)}
      </div>
    </div>);
}
