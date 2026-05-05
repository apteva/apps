// BillingPanel — billing app's project.page panel.
//
// Three tabs: Invoices, Customers, and a per-row detail. Talks to
// /api/apps/billing/* through the platform proxy. Money formatting
// is locale-aware via Intl.NumberFormat; we keep cents as integers
// in flight and only format at render.
//
// v0.1.0 surface: list + detail + finalize / void / record-payment
// actions. v0.1.1 will add a "Pay link" button on stripe-provider
// open invoices.

import { useCallback, useEffect, useRef, useState } from "react";

// ─── Inline app-events SSE hook ─────────────────────────────────────
// Each app ships its own copy (see CrmPanel for the rationale).

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

interface Customer {
  id: number;
  name: string;
  email?: string;
  phone?: string;
  currency?: string;
  external_id?: string;
  created_at?: string;
  updated_at?: string;
}

interface LineItem {
  id?: number;
  position?: number;
  description: string;
  quantity: number;
  unit_price_cents: number;
  amount_cents: number;
  tax_rate_bps: number;
}

interface Payment {
  id: number;
  invoice_id?: number;
  customer_id: number;
  amount_cents: number;
  currency: string;
  method: string;
  received_at: string;
  notes?: string;
}

interface AuditEntry {
  id: number;
  invoice_id: number;
  actor: string;
  action: string;
  details?: unknown;
  created_at: string;
}

interface Invoice {
  id: number;
  customer_id: number;
  provider: "local" | "stripe";
  number?: string;
  status: "draft" | "open" | "paid" | "void" | "uncollectible";
  currency: string;
  subtotal_cents: number;
  tax_cents: number;
  total_cents: number;
  amount_paid_cents: number;
  due_date?: string;
  notes?: string;
  finalized_at?: string;
  paid_at?: string;
  voided_at?: string;
  created_at?: string;
  updated_at?: string;
  line_items?: LineItem[];
  payments?: Payment[];
  audit_log?: AuditEntry[];
}

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

function fmtDate(s?: string): string {
  if (!s) return "—";
  try {
    return new Date(s).toLocaleDateString();
  } catch {
    return s;
  }
}

function fmtDateTime(s?: string): string {
  if (!s) return "—";
  try {
    return new Date(s).toLocaleString();
  } catch {
    return s;
  }
}

const STATUS_TONE: Record<Invoice["status"], string> = {
  draft: "bg-border text-text-muted",
  open: "bg-accent/15 text-accent",
  paid: "bg-green-500/15 text-green-500",
  void: "bg-text-dim/15 text-text-dim line-through",
  uncollectible: "bg-yellow-500/15 text-yellow-500",
};

const API = "/api/apps/billing";

// ─── Panel ──────────────────────────────────────────────────────────

export default function BillingPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<"invoices" | "customers">("invoices");

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

  return (
    <div className="h-full flex flex-col">
      <nav className="flex gap-2 p-2 border-b border-border text-sm">
        <button
          type="button"
          onClick={() => setTab("invoices")}
          className={`px-3 py-1 rounded ${
            tab === "invoices" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Invoices
        </button>
        <button
          type="button"
          onClick={() => setTab("customers")}
          className={`px-3 py-1 rounded ${
            tab === "customers" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Customers
        </button>
      </nav>

      <div className="flex-1 overflow-hidden">
        {tab === "invoices" ? (
          <InvoicesTab
            projectId={projectId}
            apiCall={apiCall}
          />
        ) : (
          <CustomersTab
            projectId={projectId}
            apiCall={apiCall}
          />
        )}
      </div>
    </div>
  );
}

// ─── Invoices tab ───────────────────────────────────────────────────

type ApiCall = <T,>(
  method: string,
  path: string,
  body?: unknown,
  query?: Record<string, string>,
) => Promise<T>;

function InvoicesTab({ projectId, apiCall }: { projectId: string; apiCall: ApiCall }) {
  const [list, setList] = useState<Invoice[]>([]);
  const [filter, setFilter] = useState<string>("");
  const [statusFilter, setStatusFilter] = useState<string>("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<Invoice | null>(null);
  const [status, setStatus] = useState<string>("");

  const loadList = useCallback(
    async () => {
      setStatus("Loading…");
      try {
        const query: Record<string, string> = {};
        if (statusFilter) query.status = statusFilter;
        const res = await apiCall<{ invoices: Invoice[] }>(
          "GET",
          "/invoices",
          undefined,
          query,
        );
        const visible = (res.invoices || []).filter((inv) => {
          if (!filter) return true;
          const f = filter.toLowerCase();
          return (
            (inv.number || "").toLowerCase().includes(f) ||
            String(inv.id).includes(f) ||
            (inv.notes || "").toLowerCase().includes(f)
          );
        });
        setList(visible);
        setStatus(`${visible.length} invoice${visible.length === 1 ? "" : "s"}`);
      } catch (err) {
        setStatus(`Error: ${(err as Error).message}`);
      }
    },
    [apiCall, filter, statusFilter],
  );

  useEffect(() => {
    loadList();
  }, [loadList]);

  useAppEvents("billing", projectId, () => {
    loadList();
    if (selectedId) loadDetail(selectedId);
  });

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const res = await apiCall<{ invoice: Invoice }>(
          "GET",
          `/invoices/${id}`,
        );
        setDetail(res.invoice);
      } catch (err) {
        setStatus(`Detail error: ${(err as Error).message}`);
      }
    },
    [apiCall],
  );

  const select = useCallback(
    (id: number) => {
      setSelectedId(id);
      loadDetail(id);
    },
    [loadDetail],
  );

  const finalize = async () => {
    if (!detail) return;
    if (!confirm(`Finalize this draft? An invoice number will be minted.`)) return;
    try {
      await apiCall("POST", `/invoices/${detail.id}/finalize`);
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Finalize failed: ${(err as Error).message}`);
    }
  };

  const voidIt = async () => {
    if (!detail) return;
    const display = `${detail.number || `#${detail.id}`} (${fmtMoney(
      detail.total_cents,
      detail.currency,
    )})`;
    const reason = prompt(`Void ${display}?\n\nOptional reason:`);
    if (reason === null) return;
    try {
      await apiCall("POST", `/invoices/${detail.id}/void`, { reason });
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Void failed: ${(err as Error).message}`);
    }
  };

  const recordPayment = async () => {
    if (!detail) return;
    const remaining = detail.total_cents - detail.amount_paid_cents;
    const amountStr = prompt(
      `Record payment for ${detail.number || `#${detail.id}`}.\n\n` +
        `Outstanding: ${fmtMoney(remaining, detail.currency)}.\n` +
        `Amount in cents (use a negative number for a refund record):`,
      String(remaining),
    );
    if (!amountStr) return;
    const amount = parseInt(amountStr, 10);
    if (Number.isNaN(amount) || amount === 0) {
      alert("Amount must be a non-zero integer (cents).");
      return;
    }
    const method = prompt("Method (wire / cash / check / other):", "wire");
    if (!method) return;
    try {
      await apiCall("POST", "/payments", {
        invoice_id: detail.id,
        amount_cents: amount,
        method,
      });
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Record payment failed: ${(err as Error).message}`);
    }
  };

  return (
    <div className="h-full flex">
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border space-y-2">
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="Search invoices…"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="">All statuses</option>
            <option value="draft">Draft</option>
            <option value="open">Open</option>
            <option value="paid">Paid</option>
            <option value="void">Void</option>
            <option value="uncollectible">Uncollectible</option>
          </select>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">No invoices.</div>
          ) : (
            <ul>
              {list.map((inv) => (
                <li
                  key={inv.id}
                  onClick={() => select(inv.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    inv.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm text-text font-medium truncate">
                      {inv.number || `Draft #${inv.id}`}
                    </span>
                    <span
                      className={`text-[10px] px-1.5 py-0.5 rounded ${
                        STATUS_TONE[inv.status]
                      }`}
                    >
                      {inv.status}
                    </span>
                  </div>
                  <div className="text-xs text-text-muted mt-0.5 flex items-center justify-between gap-2">
                    <span className="truncate">
                      Customer #{inv.customer_id}
                    </span>
                    <span className="text-text">
                      {fmtMoney(inv.total_cents, inv.currency)}
                    </span>
                  </div>
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
            {selectedId ? "Loading…" : "Select an invoice to see details."}
          </div>
        ) : (
          <InvoiceDetail
            invoice={detail}
            projectId={projectId}
            onFinalize={finalize}
            onVoid={voidIt}
            onRecordPayment={recordPayment}
          />
        )}
      </main>
    </div>
  );
}

function InvoiceDetail({
  invoice,
  projectId,
  onFinalize,
  onVoid,
  onRecordPayment,
}: {
  invoice: Invoice;
  projectId: string;
  onFinalize: () => void;
  onVoid: () => void;
  onRecordPayment: () => void;
}) {
  const remaining = invoice.total_cents - invoice.amount_paid_cents;
  const pdfHref =
    `/api/apps/billing/invoices/${invoice.id}/pdf?project_id=${encodeURIComponent(projectId)}`;
  const printHref =
    `/api/apps/billing/invoices/${invoice.id}/print?project_id=${encodeURIComponent(projectId)}`;
  const canRender = invoice.status !== "draft";
  return (
    <div className="max-w-3xl space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl text-text font-semibold flex items-center gap-2">
            {invoice.number || `Draft #${invoice.id}`}
            <span
              className={`text-[11px] px-1.5 py-0.5 rounded ${
                STATUS_TONE[invoice.status]
              }`}
            >
              {invoice.status}
            </span>
            <span className="text-[10px] uppercase text-text-dim">
              {invoice.provider}
            </span>
          </h1>
          <p className="text-text-muted text-sm mt-1">
            Customer #{invoice.customer_id}
            {invoice.due_date ? ` · due ${fmtDate(invoice.due_date)}` : ""}
          </p>
        </div>
        <div className="text-right">
          <div className="text-2xl text-text font-semibold">
            {fmtMoney(invoice.total_cents, invoice.currency)}
          </div>
          {invoice.amount_paid_cents > 0 && (
            <div className="text-xs text-text-muted">
              Paid {fmtMoney(invoice.amount_paid_cents, invoice.currency)} ·
              outstanding {fmtMoney(Math.max(0, remaining), invoice.currency)}
            </div>
          )}
        </div>
      </header>

      {invoice.line_items && invoice.line_items.length > 0 && (
        <section>
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
              {invoice.line_items.map((li, i) => (
                <tr key={li.id ?? i} className="border-b border-border/50">
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
                </tr>
              ))}
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
        </section>
      )}

      {invoice.notes && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-1">
            Notes
          </h2>
          <p className="text-sm text-text whitespace-pre-wrap">{invoice.notes}</p>
        </section>
      )}

      {invoice.payments && invoice.payments.length > 0 && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Payments ({invoice.payments.length})
          </h2>
          <ul className="space-y-1">
            {invoice.payments.map((p) => (
              <li
                key={p.id}
                className="text-sm flex items-center justify-between border border-border rounded px-2 py-1"
              >
                <span className="flex items-center gap-2">
                  <span className="text-[10px] uppercase text-text-dim w-12">
                    {p.method}
                  </span>
                  <span className="text-text">{fmtDateTime(p.received_at)}</span>
                </span>
                <span
                  className={`text-text ${p.amount_cents < 0 ? "text-yellow-500" : ""}`}
                >
                  {fmtMoney(p.amount_cents, p.currency)}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {invoice.audit_log && invoice.audit_log.length > 0 && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            History
          </h2>
          <ul className="space-y-1 text-xs text-text-muted">
            {invoice.audit_log.slice(0, 10).map((a) => (
              <li
                key={a.id}
                className="flex items-center gap-2 border-l-2 border-border pl-2 py-0.5"
              >
                <span className="text-[10px] px-1 py-0.5 rounded bg-accent/10 text-accent uppercase">
                  {a.action}
                </span>
                <span>{fmtDateTime(a.created_at)}</span>
                <span className="text-text-dim">· {a.actor}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="flex items-center gap-2 pt-2 border-t border-border">
        {invoice.status === "draft" && (
          <button
            type="button"
            onClick={onFinalize}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            Finalize
          </button>
        )}
        {(invoice.status === "open" || invoice.status === "uncollectible") && (
          <button
            type="button"
            onClick={onRecordPayment}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            Record payment
          </button>
        )}
        {canRender && (
          <>
            <a
              href={printHref}
              target="_blank"
              rel="noopener noreferrer"
              className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
            >
              Print
            </a>
            <a
              href={pdfHref}
              target="_blank"
              rel="noopener noreferrer"
              className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
            >
              Download PDF
            </a>
          </>
        )}
        {(invoice.status === "open" || invoice.status === "uncollectible") && (
          <button
            type="button"
            onClick={onVoid}
            className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 ml-auto"
          >
            Void
          </button>
        )}
      </div>
    </div>
  );
}

// ─── Customers tab ──────────────────────────────────────────────────

function CustomersTab({
  projectId,
  apiCall,
}: {
  projectId: string;
  apiCall: ApiCall;
}) {
  const [list, setList] = useState<Customer[]>([]);
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Customer | null>(null);
  const [openInvoices, setOpenInvoices] = useState<Invoice[]>([]);
  const [recentPayments, setRecentPayments] = useState<Payment[]>([]);
  const [lifetime, setLifetime] = useState<Record<string, number> | null>(null);
  const [status, setStatus] = useState("");

  const load = useCallback(
    async (q = "") => {
      setStatus("Loading…");
      try {
        const res = await apiCall<{ customers: Customer[] }>(
          "GET",
          "/customers",
          undefined,
          q ? { q } : {},
        );
        setList(res.customers || []);
        setStatus(
          `${(res.customers || []).length} customer${
            (res.customers || []).length === 1 ? "" : "s"
          }`,
        );
      } catch (err) {
        setStatus(`Error: ${(err as Error).message}`);
      }
    },
    [apiCall],
  );

  useEffect(() => {
    load("");
  }, [load]);

  useEffect(() => {
    const t = setTimeout(() => load(search.trim()), 250);
    return () => clearTimeout(t);
  }, [search, load]);

  useAppEvents("billing", projectId, (ev) => {
    if (
      ev.topic === "customer.added" ||
      ev.topic === "customer.updated" ||
      ev.topic === "customer.deleted" ||
      ev.topic === "customer.merged"
    ) {
      load(search.trim());
    }
  });

  const select = async (c: Customer) => {
    setSelected(c);
    setOpenInvoices([]);
    setRecentPayments([]);
    setLifetime(null);
    try {
      const res = await apiCall<{
        open_invoices: Invoice[];
        recent_payments: Payment[];
        lifetime: Record<string, number>;
      }>("GET", `/customers/${c.id}/context`);
      setOpenInvoices(res.open_invoices || []);
      setRecentPayments(res.recent_payments || []);
      setLifetime(res.lifetime || null);
    } catch (err) {
      setStatus(`Detail error: ${(err as Error).message}`);
    }
  };

  const create = async () => {
    const email = prompt("Email:");
    if (!email) return;
    const name = prompt("Name (optional):", email) || email;
    try {
      const res = await apiCall<{ customer: Customer }>(
        "POST",
        "/customers",
        { email, defaults: { name } },
      );
      await load("");
      select(res.customer);
    } catch (err) {
      alert(`Create failed: ${(err as Error).message}`);
    }
  };

  const currency = selected?.currency || "USD";

  return (
    <div className="h-full flex">
      <aside className="w-80 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border flex items-center gap-2">
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search customers…"
            className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <button
            type="button"
            onClick={create}
            className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            + New
          </button>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">No customers.</div>
          ) : (
            <ul>
              {list.map((c) => (
                <li
                  key={c.id}
                  onClick={() => select(c)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    selected?.id === c.id ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="text-sm text-text font-medium truncate">
                    {c.name}
                  </div>
                  <div className="text-xs text-text-muted truncate">
                    {c.email || "—"}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="p-2 text-xs text-text-dim border-t border-border">
          {status}
        </div>
      </aside>

      <main className="flex-1 overflow-auto p-6">
        {!selected ? (
          <div className="text-text-muted text-sm text-center mt-12">
            Select a customer to see open invoices + payment history.
          </div>
        ) : (
          <div className="max-w-2xl space-y-6">
            <header>
              <h1 className="text-xl text-text font-semibold">{selected.name}</h1>
              <p className="text-text-muted text-sm">
                {selected.email || "—"}
                {selected.phone ? ` · ${selected.phone}` : ""}
                {selected.currency ? ` · ${selected.currency}` : ""}
              </p>
            </header>

            {lifetime && (
              <section className="grid grid-cols-3 gap-2">
                <Stat
                  label="Invoiced"
                  value={fmtMoney(Number(lifetime.invoiced_cents || 0), currency)}
                />
                <Stat
                  label="Paid"
                  value={fmtMoney(Number(lifetime.paid_cents || 0), currency)}
                />
                <Stat
                  label="Outstanding"
                  value={fmtMoney(
                    Number(lifetime.outstanding_cents || 0),
                    currency,
                  )}
                />
              </section>
            )}

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                Open invoices ({openInvoices.length})
              </h2>
              {openInvoices.length === 0 ? (
                <p className="text-text-muted text-sm">None.</p>
              ) : (
                <ul className="space-y-1">
                  {openInvoices.map((inv) => (
                    <li
                      key={inv.id}
                      className="flex items-center justify-between border border-border rounded px-2 py-1 text-sm"
                    >
                      <span>{inv.number || `Draft #${inv.id}`}</span>
                      <span className="text-text-muted">
                        {fmtMoney(inv.total_cents, inv.currency)}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                Recent payments ({recentPayments.length})
              </h2>
              {recentPayments.length === 0 ? (
                <p className="text-text-muted text-sm">None.</p>
              ) : (
                <ul className="space-y-1">
                  {recentPayments.map((p) => (
                    <li
                      key={p.id}
                      className="flex items-center justify-between border border-border rounded px-2 py-1 text-sm"
                    >
                      <span className="flex items-center gap-2">
                        <span className="text-[10px] uppercase text-text-dim">
                          {p.method}
                        </span>
                        {fmtDateTime(p.received_at)}
                      </span>
                      <span>{fmtMoney(p.amount_cents, p.currency)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </section>
          </div>
        )}
      </main>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="border border-border rounded p-2">
      <div className="text-[10px] uppercase tracking-wide text-text-dim">
        {label}
      </div>
      <div className="text-sm text-text mt-0.5">{value}</div>
    </div>
  );
}
