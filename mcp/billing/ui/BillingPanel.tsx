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
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    // Cross-bundle multiplexer: the dashboard publishes a shared
    // (app, project) channel pool on window.__aptevaAppEvents. Every
    // panel mounted in the same realm reuses one EventSource per
    // (app, project) instead of opening its own. Without this, a few
    // panels mounted in the agent detail page burn the browser's
    // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: AppEventEnvelope<T>) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe(app, projectId, handler);
    }
    // Fallback: panel running outside the dashboard (or before its
    // hook module loaded). Open an EventSource directly.
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
  const [tab, setTab] = useState<"invoices" | "customers" | "settings">(
    "invoices",
  );

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
        <button
          type="button"
          onClick={() => setTab("settings")}
          className={`px-3 py-1 rounded ml-auto ${
            tab === "settings" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Settings
        </button>
      </nav>

      <div className="flex-1 overflow-hidden">
        {tab === "invoices" && (
          <InvoicesTab projectId={projectId} apiCall={apiCall} />
        )}
        {tab === "customers" && (
          <CustomersTab projectId={projectId} apiCall={apiCall} />
        )}
        {tab === "settings" && <SettingsTab apiCall={apiCall} />}
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
  const [showCreate, setShowCreate] = useState<boolean>(false);
  const [showFinalize, setShowFinalize] = useState<boolean>(false);
  const [showVoid, setShowVoid] = useState<boolean>(false);
  const [showPayment, setShowPayment] = useState<boolean>(false);

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

  const finalize = () => {
    if (detail) setShowFinalize(true);
  };
  const voidIt = () => {
    if (detail) setShowVoid(true);
  };
  const recordPayment = () => {
    if (detail) setShowPayment(true);
  };

  const doFinalize = async () => {
    if (!detail) return;
    await apiCall("POST", `/invoices/${detail.id}/finalize`);
    await loadList();
    await loadDetail(detail.id);
  };
  const doVoid = async (reason: string) => {
    if (!detail) return;
    await apiCall("POST", `/invoices/${detail.id}/void`, { reason });
    await loadList();
    await loadDetail(detail.id);
  };
  const doRecordPayment = async (
    amountCents: number,
    method: string,
    notes: string,
  ) => {
    if (!detail) return;
    const body: Record<string, unknown> = {
      invoice_id: detail.id,
      amount_cents: amountCents,
      method,
    };
    if (notes) body.notes = notes;
    await apiCall("POST", "/payments", body);
    await loadList();
    await loadDetail(detail.id);
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
              placeholder="Search invoices…"
              className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <button
              type="button"
              onClick={() => setShowCreate(true)}
              className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
            >
              + New
            </button>
          </div>
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

      {showCreate && (
        <CreateInvoiceModal
          apiCall={apiCall}
          onClose={() => setShowCreate(false)}
          onCreated={(inv) => {
            setShowCreate(false);
            loadList();
            select(inv.id);
          }}
        />
      )}

      {showFinalize && detail && (
        <FinalizeConfirmModal
          invoice={detail}
          onConfirm={doFinalize}
          onClose={() => setShowFinalize(false)}
        />
      )}

      {showVoid && detail && (
        <VoidInvoiceModal
          invoice={detail}
          onConfirm={doVoid}
          onClose={() => setShowVoid(false)}
        />
      )}

      {showPayment && detail && (
        <RecordPaymentModal
          invoice={detail}
          onConfirm={doRecordPayment}
          onClose={() => setShowPayment(false)}
        />
      )}
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

// ─── Create invoice modal ───────────────────────────────────────────

interface LineDraft {
  description: string;
  quantity: string;      // user-edited; converted to number on submit
  unit_price: string;    // dollars (decimal); converted to cents on submit
  tax_rate: string;      // percent (decimal); converted to bps on submit
}

function emptyLine(): LineDraft {
  return { description: "", quantity: "1", unit_price: "", tax_rate: "" };
}

function CreateInvoiceModal({
  apiCall,
  onClose,
  onCreated,
}: {
  apiCall: ApiCall;
  onClose: () => void;
  onCreated: (invoice: Invoice) => void;
}) {
  const [customer, setCustomer] = useState<Customer | null>(null);
  const [customerSearch, setCustomerSearch] = useState("");
  const [customerResults, setCustomerResults] = useState<Customer[]>([]);
  const [searching, setSearching] = useState(false);

  const [currency, setCurrency] = useState("");
  const [dueDate, setDueDate] = useState("");
  const [notes, setNotes] = useState("");
  const [items, setItems] = useState<LineDraft[]>([emptyLine()]);

  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string>("");

  useEffect(() => {
    if (customer) return;
    const q = customerSearch.trim();
    const t = setTimeout(async () => {
      setSearching(true);
      try {
        const res = await apiCall<{ customers: Customer[] }>(
          "GET",
          "/customers",
          undefined,
          q ? { q } : {},
        );
        setCustomerResults((res.customers || []).slice(0, 20));
      } catch {
        setCustomerResults([]);
      } finally {
        setSearching(false);
      }
    }, 200);
    return () => clearTimeout(t);
  }, [customerSearch, customer, apiCall]);

  const setItem = (i: number, patch: Partial<LineDraft>) =>
    setItems((prev) => prev.map((it, j) => (j === i ? { ...it, ...patch } : it)));
  const addItem = () => setItems((prev) => [...prev, emptyLine()]);
  const removeItem = (i: number) =>
    setItems((prev) => (prev.length <= 1 ? prev : prev.filter((_, j) => j !== i)));

  const previewCurrency = (currency || customer?.currency || "USD").toUpperCase();
  let subtotal = 0;
  let taxTotal = 0;
  for (const it of items) {
    if (!it.description.trim()) continue;
    const qty = parseFloat(it.quantity || "0");
    const unit = parseFloat(it.unit_price || "0");
    if (!isFinite(qty) || qty <= 0 || !isFinite(unit)) continue;
    const lineCents = Math.round(qty * unit * 100);
    subtotal += lineCents;
    const pct = parseFloat(it.tax_rate || "0");
    if (isFinite(pct) && pct > 0) {
      taxTotal += Math.round((lineCents * pct) / 100);
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
    }> = [];
    try {
      lineItems = items
        .map((it, i) => {
          const desc = it.description.trim();
          if (!desc) return null;
          const qty = parseFloat(it.quantity || "0");
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
            unit_price_cents: Math.round(unit * 100),
            tax_rate_bps: isFinite(pct) ? Math.round(pct * 100) : 0,
          };
        })
        .filter((x): x is NonNullable<typeof x> => x !== null);
    } catch (err) {
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
      const cur = currency.trim().toUpperCase();
      if (cur) body.currency = cur;
      if (dueDate) body.due_date = dueDate;
      const trimmedNotes = notes.trim();
      if (trimmedNotes) body.notes = trimmedNotes;
      const res = await apiCall<{ invoice: Invoice }>("POST", "/invoices", body);
      onCreated(res.invoice);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-6"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg w-full overflow-auto"
        style={{ maxWidth: "640px", maxHeight: "90vh" }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">New invoice</h2>
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

        <div className="p-4 space-y-4">
          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Customer
            </label>
            {customer ? (
              <div className="flex items-center justify-between bg-bg-input border border-border rounded px-2 py-1">
                <div className="text-sm text-text">
                  {customer.name}
                  {customer.email ? (
                    <span className="text-text-muted"> · {customer.email}</span>
                  ) : null}
                </div>
                <button
                  type="button"
                  onClick={() => {
                    setCustomer(null);
                    setCustomerSearch("");
                  }}
                  className="text-xs text-accent hover:underline"
                >
                  Change
                </button>
              </div>
            ) : (
              <div className="space-y-1">
                <input
                  type="text"
                  value={customerSearch}
                  onChange={(e) => setCustomerSearch(e.target.value)}
                  placeholder="Search customers by name or email…"
                  className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                  autoFocus
                />
                {customerResults.length > 0 && (
                  <ul
                    className="border border-border rounded overflow-auto"
                    style={{ maxHeight: "192px" }}
                  >
                    {customerResults.map((c) => (
                      <li
                        key={c.id}
                        onClick={() => setCustomer(c)}
                        className="px-2 py-1 cursor-pointer hover:bg-bg-input border-b border-border last:border-b-0"
                      >
                        <div className="text-sm text-text">{c.name}</div>
                        <div className="text-xs text-text-muted">
                          {c.email || "—"}
                        </div>
                      </li>
                    ))}
                  </ul>
                )}
                {searching && customerResults.length === 0 && (
                  <div className="text-xs text-text-dim">Searching…</div>
                )}
                {!searching &&
                  customerResults.length === 0 &&
                  customerSearch.trim().length > 0 && (
                    <div className="text-xs text-text-dim">No matches.</div>
                  )}
              </div>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
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
                placeholder={customer?.currency || "USD"}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Due date
              </label>
              <input
                type="date"
                value={dueDate}
                onChange={(e) => setDueDate(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
          </div>

          <div>
            <div className="flex items-center justify-between mb-1">
              <label className="text-xs uppercase tracking-wide text-text-dim">
                Line items
              </label>
              <button
                type="button"
                onClick={addItem}
                className="text-xs text-accent hover:underline"
              >
                + Add line
              </button>
            </div>
            <div className="space-y-2">
              {items.map((it, i) => (
                <div
                  key={i}
                  className="bg-bg-input border border-border rounded p-2 space-y-2"
                >
                  <input
                    type="text"
                    value={it.description}
                    onChange={(e) => setItem(i, { description: e.target.value })}
                    placeholder="Description"
                    className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"
                  />
                  <div className="grid grid-cols-3 gap-2">
                    <div>
                      <label className="block text-xs text-text-dim mb-0.5">
                        Qty
                      </label>
                      <input
                        type="number"
                        step="any"
                        min="0"
                        value={it.quantity}
                        onChange={(e) => setItem(i, { quantity: e.target.value })}
                        className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-text-dim mb-0.5">
                        Unit price
                      </label>
                      <input
                        type="number"
                        step="0.01"
                        value={it.unit_price}
                        onChange={(e) =>
                          setItem(i, { unit_price: e.target.value })
                        }
                        placeholder="0.00"
                        className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-text-dim mb-0.5">
                        Tax %
                      </label>
                      <input
                        type="number"
                        step="0.01"
                        value={it.tax_rate}
                        onChange={(e) => setItem(i, { tax_rate: e.target.value })}
                        placeholder="0"
                        className="w-full bg-bg border border-border rounded px-2 py-1 text-sm"
                      />
                    </div>
                  </div>
                  {items.length > 1 && (
                    <div className="text-right">
                      <button
                        type="button"
                        onClick={() => removeItem(i)}
                        className="text-xs text-red hover:underline"
                      >
                        Remove line
                      </button>
                    </div>
                  )}
                </div>
              ))}
            </div>
          </div>

          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Notes
            </label>
            <textarea
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              rows={2}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
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
            onClick={submit}
            disabled={submitting || !customer}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            {submitting ? "Creating…" : "Create draft"}
          </button>
        </footer>
      </div>
    </div>
  );
}

// ─── Invoice action modals ──────────────────────────────────────────

function FinalizeConfirmModal({
  invoice,
  onConfirm,
  onClose,
}: {
  invoice: Invoice;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    setError("");
    setBusy(true);
    try {
      await onConfirm();
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-6"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg w-full"
        style={{ maxWidth: "440px" }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">Finalize this draft?</h2>
          <ModalCloseButton onClose={onClose} />
        </header>
        <div className="p-4 text-sm text-text space-y-2">
          <p>
            An invoice number will be minted and the invoice transitions from{" "}
            <strong>draft</strong> to <strong>open</strong>. Line items can no
            longer be added or edited after this.
          </p>
          <p className="text-text-muted">
            {invoice.number || `Draft #${invoice.id}`} ·{" "}
            {fmtMoney(invoice.total_cents, invoice.currency)}
          </p>
        </div>
        {error && (
          <div className="px-4 pb-2 text-sm text-red">{error}</div>
        )}
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            {busy ? "Finalizing…" : "Finalize"}
          </button>
        </footer>
      </div>
    </div>
  );
}

function VoidInvoiceModal({
  invoice,
  onConfirm,
  onClose,
}: {
  invoice: Invoice;
  onConfirm: (reason: string) => Promise<void>;
  onClose: () => void;
}) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async () => {
    setError("");
    setBusy(true);
    try {
      await onConfirm(reason.trim());
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-6"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg w-full"
        style={{ maxWidth: "480px" }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">Void invoice?</h2>
          <ModalCloseButton onClose={onClose} />
        </header>
        <div className="p-4 text-sm text-text space-y-3">
          <p className="text-text-muted">
            {invoice.number || `#${invoice.id}`} ·{" "}
            {fmtMoney(invoice.total_cents, invoice.currency)}
          </p>
          <p>
            Voiding is permanent. The invoice will be marked{" "}
            <strong>void</strong> and excluded from open / outstanding totals.
            Recorded payments stay on the audit log but won't be reversed.
          </p>
          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Reason (optional, kept in the audit log)
            </label>
            <textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              rows={3}
              placeholder="Duplicate of INV-…, customer cancelled, billing error, …"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              autoFocus
            />
          </div>
        </div>
        {error && (
          <div className="px-4 pb-2 text-sm text-red">{error}</div>
        )}
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            className="px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10 disabled:opacity-50"
          >
            {busy ? "Voiding…" : "Void invoice"}
          </button>
        </footer>
      </div>
    </div>
  );
}

function RecordPaymentModal({
  invoice,
  onConfirm,
  onClose,
}: {
  invoice: Invoice;
  onConfirm: (
    amountCents: number,
    method: string,
    notes: string,
  ) => Promise<void>;
  onClose: () => void;
}) {
  const outstandingCents = Math.max(
    0,
    invoice.total_cents - invoice.amount_paid_cents,
  );
  const outstandingDecimal = (outstandingCents / 100).toFixed(2);

  const [amount, setAmount] = useState(outstandingDecimal);
  const [method, setMethod] = useState("wire");
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = async () => {
    setError("");
    const value = parseFloat(amount);
    if (!isFinite(value) || value === 0) {
      setError("Amount must be a non-zero number.");
      return;
    }
    const cents = Math.round(value * 100);
    setBusy(true);
    try {
      await onConfirm(cents, method, notes.trim());
      onClose();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-6"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg w-full"
        style={{ maxWidth: "480px" }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="p-4 border-b border-border flex items-center justify-between">
          <h2 className="text-text font-semibold">Record payment</h2>
          <ModalCloseButton onClose={onClose} />
        </header>
        <div className="p-4 space-y-3 text-sm">
          <div className="flex items-center justify-between text-text-muted">
            <span>{invoice.number || `#${invoice.id}`}</span>
            <span>
              Outstanding{" "}
              <span className="text-text">
                {fmtMoney(outstandingCents, invoice.currency)}
              </span>
            </span>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Amount ({invoice.currency})
              </label>
              <input
                type="number"
                step="0.01"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
                autoFocus
              />
              <p className="text-xs text-text-dim mt-1">
                Use a negative number for a refund record.
              </p>
            </div>
            <div>
              <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
                Method
              </label>
              <select
                value={method}
                onChange={(e) => setMethod(e.target.value)}
                className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
              >
                <option value="wire">Wire</option>
                <option value="cash">Cash</option>
                <option value="check">Check</option>
                <option value="other">Other</option>
              </select>
            </div>
          </div>

          <div>
            <label className="block text-xs uppercase tracking-wide text-text-dim mb-1">
              Notes (optional)
            </label>
            <textarea
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              rows={2}
              placeholder="Transaction reference, payer name, …"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
        </div>
        {error && (
          <div className="px-4 pb-2 text-sm text-red">{error}</div>
        )}
        <footer className="p-4 border-t border-border flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            {busy ? "Recording…" : "Record payment"}
          </button>
        </footer>
      </div>
    </div>
  );
}

// Shared close (X) button — same SVG used by all the modals.
function ModalCloseButton({ onClose }: { onClose: () => void }) {
  return (
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
  const [showCreate, setShowCreate] = useState(false);

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

  const onCreated = async (c: Customer) => {
    setShowCreate(false);
    await load("");
    select(c);
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
            onClick={() => setShowCreate(true)}
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

      {showCreate && (
        <CreateCustomerModal
          apiCall={apiCall}
          onClose={() => setShowCreate(false)}
          onCreated={onCreated}
        />
      )}
    </div>
  );
}

// ─── Create customer modal ──────────────────────────────────────────

interface TaxIdDraft {
  type: string;
  value: string;
}

function CreateCustomerModal({
  apiCall,
  onClose,
  onCreated,
}: {
  apiCall: ApiCall;
  onClose: () => void;
  onCreated: (customer: Customer) => void;
}) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [currency, setCurrency] = useState("");

  const [line1, setLine1] = useState("");
  const [line2, setLine2] = useState("");
  const [city, setCity] = useState("");
  const [region, setRegion] = useState("");
  const [postalCode, setPostalCode] = useState("");
  const [country, setCountry] = useState("");

  const [taxIds, setTaxIds] = useState<TaxIdDraft[]>([
    { type: "vat", value: "" },
  ]);

  const [contactName, setContactName] = useState("");
  const [contactTitle, setContactTitle] = useState("");
  const [website, setWebsite] = useState("");

  const [iban, setIban] = useState("");
  const [bic, setBic] = useState("");
  const [bankName, setBankName] = useState("");
  const [bankCode, setBankCode] = useState("");

  const [notes, setNotes] = useState("");

  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const [warning, setWarning] = useState("");

  const setTaxId = (i: number, patch: Partial<TaxIdDraft>) =>
    setTaxIds((prev) => prev.map((t, j) => (j === i ? { ...t, ...patch } : t)));
  const addTaxId = () =>
    setTaxIds((prev) => [...prev, { type: "vat", value: "" }]);
  const removeTaxId = (i: number) =>
    setTaxIds((prev) =>
      prev.length <= 1 ? prev : prev.filter((_, j) => j !== i),
    );

  const submit = async () => {
    setError("");
    setWarning("");
    const cleanEmail = email.trim();
    const cleanName = name.trim();
    if (!cleanEmail) {
      setError("Email is required.");
      return;
    }
    if (!cleanName) {
      setError("Name is required.");
      return;
    }

    const billingAddress: Record<string, string> = {};
    if (line1.trim()) billingAddress.line1 = line1.trim();
    if (line2.trim()) billingAddress.line2 = line2.trim();
    if (postalCode.trim()) billingAddress.postal_code = postalCode.trim();
    if (city.trim()) billingAddress.city = city.trim();
    if (region.trim()) billingAddress.state = region.trim();
    if (country.trim()) billingAddress.country = country.trim().toUpperCase();

    const cleanTaxIds = taxIds
      .map((t) => ({ type: t.type.trim(), value: t.value.trim() }))
      .filter((t) => t.type && t.value);

    const metadata: Record<string, unknown> = {};
    const bank: Record<string, string> = {};
    if (iban.trim()) bank.iban = iban.trim().toUpperCase().replace(/\s+/g, "");
    if (bic.trim()) bank.bic = bic.trim().toUpperCase().replace(/\s+/g, "");
    if (bankName.trim()) bank.name = bankName.trim();
    if (bankCode.trim()) bank.code = bankCode.trim();
    if (Object.keys(bank).length > 0) metadata.bank = bank;

    const contact: Record<string, string> = {};
    if (contactName.trim()) contact.name = contactName.trim();
    if (contactTitle.trim()) contact.title = contactTitle.trim();
    if (Object.keys(contact).length > 0) metadata.contact = contact;

    if (website.trim()) metadata.website = website.trim();
    if (notes.trim()) metadata.notes = notes.trim();

    const defaults: Record<string, unknown> = { name: cleanName };
    if (phone.trim()) defaults.phone = phone.trim();
    if (currency.trim()) defaults.currency = currency.trim().toUpperCase();
    if (Object.keys(billingAddress).length > 0)
      defaults.billing_address = billingAddress;
    if (cleanTaxIds.length > 0) defaults.tax_ids = cleanTaxIds;
    if (Object.keys(metadata).length > 0) defaults.metadata = metadata;

    setSubmitting(true);
    try {
      const res = await apiCall<{ customer: Customer; was_created: boolean }>(
        "POST",
        "/customers",
        { email: cleanEmail, defaults },
      );
      if (res.was_created === false) {
        // Upsert-by-email returns the existing row and ignores defaults.
        // Surface that instead of silently dropping the user's entries.
        setWarning(
          "A customer with this email already exists. Opening the existing record — your new field values were NOT applied.",
        );
        setTimeout(() => onCreated(res.customer), 1500);
        return;
      }
      onCreated(res.customer);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-6"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded-lg w-full overflow-auto"
        style={{ maxWidth: "640px", maxHeight: "90vh" }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="p-4 border-b border-border flex items-center justify-between sticky top-0 bg-bg z-10">
          <h2 className="text-text font-semibold">New customer</h2>
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

        <div className="p-4 space-y-5">
          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Identity
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Name or company *"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
                autoFocus
              />
              <input
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="Email *"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="tel"
                value={phone}
                onChange={(e) => setPhone(e.target.value)}
                placeholder="Phone"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={currency}
                onChange={(e) =>
                  setCurrency(e.target.value.toUpperCase().slice(0, 3))
                }
                placeholder="Default currency (e.g. EUR)"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Billing address
            </h3>
            <input
              type="text"
              value={line1}
              onChange={(e) => setLine1(e.target.value)}
              placeholder="Street address"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={line2}
              onChange={(e) => setLine2(e.target.value)}
              placeholder="Address line 2 (optional)"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <div className="grid grid-cols-3 gap-2">
              <input
                type="text"
                value={postalCode}
                onChange={(e) => setPostalCode(e.target.value)}
                placeholder="Postal code"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={city}
                onChange={(e) => setCity(e.target.value)}
                placeholder="City"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                placeholder="State / region"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
            <input
              type="text"
              value={country}
              onChange={(e) =>
                setCountry(e.target.value.toUpperCase().slice(0, 2))
              }
              placeholder="Country (2-char ISO, e.g. FR, EE, US)"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </section>

          <section className="space-y-2">
            <div className="flex items-center justify-between">
              <h3 className="text-xs uppercase tracking-wide text-text-dim">
                Tax IDs
              </h3>
              <button
                type="button"
                onClick={addTaxId}
                className="text-xs text-accent hover:underline"
              >
                + Add ID
              </button>
            </div>
            {taxIds.map((t, i) => (
              <div key={i} className="flex items-center gap-2">
                <select
                  value={t.type}
                  onChange={(e) => setTaxId(i, { type: e.target.value })}
                  className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
                >
                  <option value="vat">VAT</option>
                  <option value="ein">EIN</option>
                  <option value="gst">GST</option>
                  <option value="abn">ABN</option>
                  <option value="company_reg">Company reg.</option>
                  <option value="siret">SIRET</option>
                  <option value="other">Other</option>
                </select>
                <input
                  type="text"
                  value={t.value}
                  onChange={(e) => setTaxId(i, { value: e.target.value })}
                  placeholder="Value"
                  className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
                />
                {taxIds.length > 1 && (
                  <button
                    type="button"
                    onClick={() => removeTaxId(i)}
                    aria-label="Remove tax ID"
                    className="text-text-muted hover:text-red"
                  >
                    <svg
                      width="14"
                      height="14"
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
                )}
              </div>
            ))}
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Contact (optional)
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input
                type="text"
                value={contactName}
                onChange={(e) => setContactName(e.target.value)}
                placeholder="Contact name"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={contactTitle}
                onChange={(e) => setContactTitle(e.target.value)}
                placeholder="Title"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
            <input
              type="text"
              value={website}
              onChange={(e) => setWebsite(e.target.value)}
              placeholder="Website"
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Bank details (optional)
            </h3>
            <div className="grid grid-cols-2 gap-2">
              <input
                type="text"
                value={iban}
                onChange={(e) => setIban(e.target.value)}
                placeholder="IBAN"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={bic}
                onChange={(e) => setBic(e.target.value)}
                placeholder="BIC / SWIFT"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={bankName}
                onChange={(e) => setBankName(e.target.value)}
                placeholder="Bank name"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              <input
                type="text"
                value={bankCode}
                onChange={(e) => setBankCode(e.target.value)}
                placeholder="Bank code"
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
            </div>
          </section>

          <section className="space-y-2">
            <h3 className="text-xs uppercase tracking-wide text-text-dim">
              Notes
            </h3>
            <textarea
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              rows={2}
              className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </section>

          {warning && (
            <div className="text-sm text-yellow-500 border border-yellow-500/30 bg-yellow-500/10 rounded px-2 py-1">
              {warning}
            </div>
          )}
          {error && <div className="text-sm text-red">{error}</div>}
        </div>

        <footer className="p-4 border-t border-border flex items-center justify-end gap-2 sticky bottom-0 bg-bg">
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
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            {submitting ? "Creating…" : "Create customer"}
          </button>
        </footer>
      </div>
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

// ─── Settings tab — issuer (BILL FROM) ──────────────────────────────

interface BillingAddress {
  line1?: string;
  line2?: string;
  postal_code?: string;
  city?: string;
  state?: string;
  country?: string;
}

interface BankCoords {
  iban?: string;
  bic?: string;
  bank_name?: string;
  bank_code?: string;
  beneficiary?: string;
}

interface Issuer {
  display_name?: string;
  legal_name?: string;
  email?: string;
  phone?: string;
  website?: string;
  brand_color?: string;
  address?: BillingAddress;
  tax_ids?: TaxIdDraft[];
  bank?: BankCoords;
  footer_text?: string;
  default_terms?: string;
  configured?: boolean;
}

function SettingsTab({ apiCall }: { apiCall: ApiCall }) {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");

  const [displayName, setDisplayName] = useState("");
  const [legalName, setLegalName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [website, setWebsite] = useState("");

  const [line1, setLine1] = useState("");
  const [line2, setLine2] = useState("");
  const [postalCode, setPostalCode] = useState("");
  const [city, setCity] = useState("");
  const [region, setRegion] = useState("");
  const [country, setCountry] = useState("");

  const [taxIds, setTaxIds] = useState<TaxIdDraft[]>([
    { type: "vat", value: "" },
  ]);

  const [iban, setIban] = useState("");
  const [bic, setBic] = useState("");
  const [bankName, setBankName] = useState("");
  const [bankCode, setBankCode] = useState("");
  const [beneficiary, setBeneficiary] = useState("");

  const [footerText, setFooterText] = useState("");
  const [defaultTerms, setDefaultTerms] = useState("");

  const [configured, setConfigured] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [savedNotice, setSavedNotice] = useState("");

  // Load current issuer.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await apiCall<{ issuer: Issuer }>("GET", "/issuer");
        if (cancelled) return;
        const i = res.issuer || {};
        setDisplayName(i.display_name || "");
        setLegalName(i.legal_name || "");
        setEmail(i.email || "");
        setPhone(i.phone || "");
        setWebsite(i.website || "");
        const addr = i.address || {};
        setLine1(addr.line1 || "");
        setLine2(addr.line2 || "");
        setPostalCode(addr.postal_code || "");
        setCity(addr.city || "");
        setRegion(addr.state || "");
        setCountry(addr.country || "");
        const ids = (i.tax_ids && i.tax_ids.length > 0)
          ? i.tax_ids
          : [{ type: "vat", value: "" }];
        setTaxIds(ids);
        const bank = i.bank || {};
        setIban(bank.iban || "");
        setBic(bank.bic || "");
        setBankName(bank.bank_name || "");
        setBankCode(bank.bank_code || "");
        setBeneficiary(bank.beneficiary || "");
        setFooterText(i.footer_text || "");
        setDefaultTerms(i.default_terms || "");
        setConfigured(!!i.configured);
      } catch (err) {
        if (!cancelled) setLoadError((err as Error).message);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [apiCall]);

  const setTaxId = (i: number, patch: Partial<TaxIdDraft>) =>
    setTaxIds((prev) => prev.map((t, j) => (j === i ? { ...t, ...patch } : t)));
  const addTaxId = () =>
    setTaxIds((prev) => [...prev, { type: "vat", value: "" }]);
  const removeTaxId = (i: number) =>
    setTaxIds((prev) =>
      prev.length <= 1 ? prev : prev.filter((_, j) => j !== i),
    );

  const save = async () => {
    setSaveError("");
    setSavedNotice("");
    if (!displayName.trim()) {
      setSaveError("Display name is required.");
      return;
    }

    const address: BillingAddress = {};
    if (line1.trim()) address.line1 = line1.trim();
    if (line2.trim()) address.line2 = line2.trim();
    if (postalCode.trim()) address.postal_code = postalCode.trim();
    if (city.trim()) address.city = city.trim();
    if (region.trim()) address.state = region.trim();
    if (country.trim()) address.country = country.trim().toUpperCase();

    const cleanTaxIds = taxIds
      .map((t) => ({ type: t.type.trim(), value: t.value.trim() }))
      .filter((t) => t.type && t.value);

    const bank: BankCoords = {};
    if (iban.trim()) bank.iban = iban.trim().toUpperCase().replace(/\s+/g, "");
    if (bic.trim()) bank.bic = bic.trim().toUpperCase().replace(/\s+/g, "");
    if (bankName.trim()) bank.bank_name = bankName.trim();
    if (bankCode.trim()) bank.bank_code = bankCode.trim();
    if (beneficiary.trim()) bank.beneficiary = beneficiary.trim();

    const body: Record<string, unknown> = { display_name: displayName.trim() };
    if (legalName.trim()) body.legal_name = legalName.trim();
    if (email.trim()) body.email = email.trim();
    if (phone.trim()) body.phone = phone.trim();
    if (website.trim()) body.website = website.trim();
    if (Object.keys(address).length > 0) body.address = address;
    if (cleanTaxIds.length > 0) body.tax_ids = cleanTaxIds;
    if (Object.keys(bank).length > 0) body.bank = bank;
    if (footerText.trim()) body.footer_text = footerText.trim();
    if (defaultTerms.trim()) body.default_terms = defaultTerms.trim();

    setSaving(true);
    try {
      const res = await apiCall<{ issuer: Issuer }>("PUT", "/issuer", body);
      setConfigured(!!res.issuer.configured);
      setSavedNotice("Saved. Next invoice PDF will use these settings.");
      setTimeout(() => setSavedNotice(""), 3000);
    } catch (err) {
      setSaveError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="p-6 text-text-muted text-sm">Loading settings…</div>
    );
  }
  if (loadError) {
    return (
      <div className="p-6 text-sm text-red">Load failed: {loadError}</div>
    );
  }

  return (
    <div className="h-full overflow-auto">
      <div className="max-w-2xl mx-auto p-6 space-y-6">
        <header>
          <h1 className="text-xl text-text font-semibold">Billing identity</h1>
          <p className="text-text-muted text-sm mt-1">
            The entity that emits invoices — appears as the BILL FROM block on
            every PDF and print page. Singleton across this install.
          </p>
          {!configured && (
            <div className="mt-2 text-xs text-yellow-500 border border-yellow-500/30 bg-yellow-500/10 rounded px-2 py-1">
              Not configured yet — PDFs currently show a placeholder. Fill in
              the form below and Save.
            </div>
          )}
        </header>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Identity
          </h2>
          <div className="grid grid-cols-2 gap-2">
            <input
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Display name *"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={legalName}
              onChange={(e) => setLegalName(e.target.value)}
              placeholder="Legal name (registered entity)"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="Email"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="tel"
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="Phone"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <input
            type="text"
            value={website}
            onChange={(e) => setWebsite(e.target.value)}
            placeholder="Website"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </section>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Address
          </h2>
          <input
            type="text"
            value={line1}
            onChange={(e) => setLine1(e.target.value)}
            placeholder="Street address"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <input
            type="text"
            value={line2}
            onChange={(e) => setLine2(e.target.value)}
            placeholder="Address line 2 (optional)"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <div className="grid grid-cols-3 gap-2">
            <input
              type="text"
              value={postalCode}
              onChange={(e) => setPostalCode(e.target.value)}
              placeholder="Postal code"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={city}
              onChange={(e) => setCity(e.target.value)}
              placeholder="City"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              placeholder="State / region"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <input
            type="text"
            value={country}
            onChange={(e) =>
              setCountry(e.target.value.toUpperCase().slice(0, 2))
            }
            placeholder="Country (2-char ISO, e.g. EE, FR, US)"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </section>

        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <h2 className="text-xs uppercase tracking-wide text-text-dim">
              Tax IDs
            </h2>
            <button
              type="button"
              onClick={addTaxId}
              className="text-xs text-accent hover:underline"
            >
              + Add ID
            </button>
          </div>
          {taxIds.map((t, i) => (
            <div key={i} className="flex items-center gap-2">
              <select
                value={t.type}
                onChange={(e) => setTaxId(i, { type: e.target.value })}
                className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
              >
                <option value="vat">VAT</option>
                <option value="ein">EIN</option>
                <option value="gst">GST</option>
                <option value="abn">ABN</option>
                <option value="company_reg">Company reg.</option>
                <option value="siret">SIRET</option>
                <option value="other">Other</option>
              </select>
              <input
                type="text"
                value={t.value}
                onChange={(e) => setTaxId(i, { value: e.target.value })}
                placeholder="Value"
                className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
              />
              {taxIds.length > 1 && (
                <button
                  type="button"
                  onClick={() => removeTaxId(i)}
                  aria-label="Remove tax ID"
                  className="text-text-muted hover:text-red"
                >
                  <svg
                    width="14"
                    height="14"
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
              )}
            </div>
          ))}
        </section>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Bank details (rendered on PDF as "Pay by bank transfer")
          </h2>
          <input
            type="text"
            value={iban}
            onChange={(e) => setIban(e.target.value)}
            placeholder="IBAN"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <div className="grid grid-cols-2 gap-2">
            <input
              type="text"
              value={bic}
              onChange={(e) => setBic(e.target.value)}
              placeholder="BIC / SWIFT"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={bankCode}
              onChange={(e) => setBankCode(e.target.value)}
              placeholder="Bank code"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={bankName}
              onChange={(e) => setBankName(e.target.value)}
              placeholder="Bank name"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <input
              type="text"
              value={beneficiary}
              onChange={(e) => setBeneficiary(e.target.value)}
              placeholder="Beneficiary (defaults to legal name)"
              className="bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
        </section>

        <section className="space-y-2">
          <h2 className="text-xs uppercase tracking-wide text-text-dim">
            Footer + defaults
          </h2>
          <textarea
            value={footerText}
            onChange={(e) => setFooterText(e.target.value)}
            rows={2}
            placeholder="Footer text (small print at the bottom of each invoice)"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
          <input
            type="text"
            value={defaultTerms}
            onChange={(e) => setDefaultTerms(e.target.value)}
            placeholder="Default payment terms (e.g. Payment due within 30 days)"
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </section>

        {saveError && <div className="text-sm text-red">{saveError}</div>}
        {savedNotice && (
          <div className="text-sm text-green-500 border border-green-500/30 bg-green-500/10 rounded px-2 py-1">
            {savedNotice}
          </div>
        )}

        <div className="pt-2 border-t border-border flex items-center justify-end">
          <button
            type="button"
            onClick={save}
            disabled={saving}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            {saving ? "Saving…" : "Save"}
          </button>
        </div>
      </div>
    </div>
  );
}
