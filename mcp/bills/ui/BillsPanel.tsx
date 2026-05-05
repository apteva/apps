// BillsPanel — bills app's project.page panel. The AP mirror of
// billing's panel. Two tabs (Bills / Vendors), per-row detail with
// approve/reject/schedule/record-payment/void actions on the bill
// detail. Same money formatting + SSE pattern as billing's panel.

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ChangeEvent as ReactChangeEvent,
  type DragEvent as ReactDragEvent,
} from "react";

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

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Vendor {
  id: number;
  name: string;
  email?: string;
  phone?: string;
  currency?: string;
  default_payment_method?: string;
  default_payment_terms_days?: number;
  w9_received_at?: string;
  external_id?: string;
}

interface LineItem {
  id?: number;
  description: string;
  quantity: number;
  unit_price_cents: number;
  amount_cents: number;
  tax_rate_bps: number;
}

interface Payment {
  id: number;
  bill_id?: number;
  vendor_id: number;
  amount_cents: number;
  currency: string;
  method: string;
  sent_at: string;
  notes?: string;
}

interface Audit {
  id: number;
  bill_id: number;
  actor: string;
  action: string;
  details?: unknown;
  created_at: string;
}

type BillStatus =
  | "received"
  | "approved"
  | "scheduled"
  | "paid"
  | "disputed"
  | "void";

interface Bill {
  id: number;
  vendor_id: number;
  provider: "local" | "mercury" | "wise" | "bill_dot_com";
  vendor_invoice_number?: string;
  vendor_invoice_date?: string;
  status: BillStatus;
  currency: string;
  subtotal_cents: number;
  tax_cents: number;
  total_cents: number;
  amount_paid_cents: number;
  due_date?: string;
  notes?: string;
  category?: string;
  gl_account?: string;
  attached_file_id?: number;
  approved_at?: string;
  approved_by?: string;
  scheduled_for?: string;
  scheduled_method?: string;
  paid_at?: string;
  voided_at?: string;
  disputed_at?: string;
  created_at?: string;
  updated_at?: string;
  line_items?: LineItem[];
  payments?: Payment[];
  audit_log?: Audit[];
}

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

const STATUS_TONE: Record<BillStatus, string> = {
  received: "bg-yellow-500/15 text-yellow-500",
  approved: "bg-accent/15 text-accent",
  scheduled: "bg-cyan-500/15 text-cyan-500",
  paid: "bg-green-500/15 text-green-500",
  disputed: "bg-orange-500/15 text-orange-500",
  void: "bg-text-dim/15 text-text-dim line-through",
};

const API = "/api/apps/bills";

export default function BillsPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<"bills" | "vendors">("bills");
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
          onClick={() => setTab("bills")}
          className={`px-3 py-1 rounded ${
            tab === "bills" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Bills
        </button>
        <button
          type="button"
          onClick={() => setTab("vendors")}
          className={`px-3 py-1 rounded ${
            tab === "vendors" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Vendors
        </button>
      </nav>
      <div className="flex-1 overflow-hidden">
        {tab === "bills" ? (
          <BillsTab
            projectId={projectId}
            installId={installId}
            apiCall={apiCall}
            queryString={queryString}
          />
        ) : (
          <VendorsTab projectId={projectId} apiCall={apiCall} />
        )}
      </div>
    </div>
  );
}

type ApiCall = <T,>(
  method: string,
  path: string,
  body?: unknown,
  query?: Record<string, string>,
) => Promise<T>;

// VendorPickModal — used by both "+ New" and the drop-on-list flow.
// Search + pick existing, or upsert a new one inline.
function VendorPickModal({
  apiCall,
  attachedFilename,
  onPick,
  onCancel,
}: {
  apiCall: ApiCall;
  attachedFilename?: string | null;
  onPick: (vendor: Vendor) => void;
  onCancel: () => void;
}) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<Vendor[]>([]);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const t = setTimeout(async () => {
      try {
        const res = await apiCall<{ vendors: Vendor[] }>(
          "GET",
          "/vendors",
          undefined,
          q ? { q } : {},
        );
        if (!cancelled) setResults(res.vendors || []);
      } catch {
        /* swallow */
      }
    }, 200);
    return () => {
      cancelled = true;
      clearTimeout(t);
    };
  }, [q, apiCall]);

  const createNew = async () => {
    const email = prompt("New vendor email:", q.includes("@") ? q : "");
    if (!email) return;
    const name = prompt("Vendor name:", q.includes("@") ? "" : q) || email;
    setBusy(true);
    try {
      const res = await apiCall<{ vendor: Vendor }>(
        "POST",
        "/vendors",
        { email, defaults: { name } },
      );
      onPick(res.vendor);
    } catch (err) {
      alert(`Create vendor failed: ${(err as Error).message}`);
    } finally {
      setBusy(false);
      setCreating(false);
    }
  };

  return (
    <div className="absolute inset-0 z-20 bg-bg/80 flex items-start justify-center pt-24 pointer-events-auto">
      <div className="bg-bg border border-border rounded shadow-lg w-96 max-h-[60vh] flex flex-col">
        <div className="p-3 border-b border-border">
          <h2 className="text-sm font-medium text-text">Pick a vendor</h2>
          {attachedFilename && (
            <p className="text-xs text-text-muted mt-1">
              Attaching <span className="text-accent">{attachedFilename}</span>
            </p>
          )}
          <input
            type="text"
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search by name or email…"
            className="w-full mt-2 bg-bg-input border border-border rounded px-2 py-1 text-sm"
          />
        </div>
        <div className="flex-1 overflow-auto">
          {results.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">
              No vendors match. Use "+ Create new" below.
            </div>
          ) : (
            <ul>
              {results.map((v) => (
                <li
                  key={v.id}
                  onClick={() => onPick(v)}
                  className="px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50"
                >
                  <div className="text-sm text-text">{v.name}</div>
                  <div className="text-xs text-text-muted">{v.email || "—"}</div>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="p-2 border-t border-border flex items-center gap-2">
          <button
            type="button"
            onClick={createNew}
            disabled={busy || creating}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg disabled:opacity-50"
          >
            + Create new
          </button>
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1 text-sm border border-border rounded hover:bg-bg-input ml-auto"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

// ExtractedByOCRBanner — small inline notice when the bill was
// auto-filled by OCR. Reads the bill's audit log for an "extracted"
// entry; absent = silent. Rendered above the attached document
// section so the user sees the provenance before they look at the
// fields themselves.
function ExtractedByOCRBanner({ bill }: { bill: Bill }) {
  const entry = bill.audit_log?.find((a) => a.action === "extracted");
  if (!entry) return null;
  const details = (entry.details as
    | {
        provider?: string;
        fields_filled?: string[];
        vendor_resolved_via?: string;
      }
    | undefined) ?? {};
  const fields = details.fields_filled || [];
  return (
    <div className="border border-accent/30 bg-accent/5 rounded p-3 text-sm">
      <div className="flex items-center gap-2">
        <span className="text-[10px] uppercase tracking-wide text-accent font-semibold">
          Auto-filled by OCR
        </span>
        {details.provider && (
          <span className="text-[10px] uppercase text-text-dim">
            via {details.provider}
          </span>
        )}
      </div>
      <p className="text-text-muted text-xs mt-1">
        {fields.length > 0
          ? `Extraction filled: ${fields.join(", ")}`
          : "Extraction ran but nothing was missing."}
        {details.vendor_resolved_via && (
          <> · vendor resolved via {details.vendor_resolved_via}</>
        )}
      </p>
    </div>
  );
}

// AttachedDocumentSection — renders the bill's attached file, with
// open + replace + remove. When nothing is attached, shows a drop
// zone + "Upload" button. The replace flow uses a hidden <input
// type=file> we click programmatically.
function AttachedDocumentSection({
  bill,
  projectId,
  installId,
  onChanged,
}: {
  bill: Bill;
  projectId: string;
  installId: number;
  onChanged: () => void;
}) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState(false);
  const [dragOver, setDragOver] = useState(false);

  const queryString = () =>
    new URLSearchParams({
      project_id: projectId,
      install_id: String(installId),
    }).toString();

  const upload = async (f: File) => {
    setBusy(true);
    try {
      const fd = new FormData();
      fd.append("file", f);
      const r = await fetch(
        `${API}/bills/${bill.id}/attach?${queryString()}`,
        { method: "POST", credentials: "same-origin", body: fd },
      );
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      onChanged();
    } catch (err) {
      alert(`Attach failed: ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!confirm("Detach the document? It stays in storage; this just unlinks.")) return;
    setBusy(true);
    try {
      const r = await fetch(
        `${API}/bills/${bill.id}/attach?${queryString()}`,
        { method: "DELETE", credentials: "same-origin" },
      );
      if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
      onChanged();
    } catch (err) {
      alert(`Detach failed: ${(err as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const onFileChange = (e: ReactChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (f) upload(f);
    // Allow re-uploading the same file later.
    e.target.value = "";
  };

  const onDrop = (e: ReactDragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const f = e.dataTransfer.files?.[0];
    if (f) upload(f);
  };

  if (bill.attached_file_id) {
    const openHref =
      `/api/apps/storage/files/${bill.attached_file_id}/content` +
      `?project_id=${encodeURIComponent(projectId)}`;
    return (
      <section>
        <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
          Original document
        </h2>
        <div className="border border-border rounded p-3 flex items-center justify-between gap-2">
          <span className="text-sm text-text">
            📄 Storage file #{bill.attached_file_id}
          </span>
          <div className="flex items-center gap-2">
            <a
              href={openHref}
              target="_blank"
              rel="noopener noreferrer"
              className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input"
            >
              Open
            </a>
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={busy}
              className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50"
            >
              Replace
            </button>
            <button
              type="button"
              onClick={remove}
              disabled={busy}
              className="px-2 py-1 text-xs text-red border border-red/50 rounded hover:bg-red/10 disabled:opacity-50"
            >
              ×
            </button>
          </div>
        </div>
        <input
          ref={fileInputRef}
          type="file"
          accept="application/pdf,image/*"
          className="hidden"
          onChange={onFileChange}
        />
      </section>
    );
  }

  return (
    <section>
      <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
        Original document
      </h2>
      <div
        onDragEnter={(e) => {
          e.preventDefault();
          setDragOver(true);
        }}
        onDragOver={(e) => e.preventDefault()}
        onDragLeave={() => setDragOver(false)}
        onDrop={onDrop}
        className={`border-2 border-dashed rounded p-6 text-center cursor-pointer transition-colors ${
          dragOver
            ? "border-accent bg-accent/10"
            : "border-border hover:bg-bg-input/30"
        }`}
        onClick={() => fileInputRef.current?.click()}
      >
        <p className="text-sm text-text-muted">
          {busy
            ? "Uploading…"
            : "Drop a PDF or image here, or click to browse"}
        </p>
      </div>
      <input
        ref={fileInputRef}
        type="file"
        accept="application/pdf,image/*"
        className="hidden"
        onChange={onFileChange}
      />
    </section>
  );
}

function BillsTab({
  projectId,
  installId,
  apiCall,
  queryString,
}: {
  projectId: string;
  installId: number;
  apiCall: ApiCall;
  queryString: (extra?: Record<string, string>) => string;
}) {
  const [list, setList] = useState<Bill[]>([]);
  const [statusFilter, setStatusFilter] = useState("");
  const [search, setSearch] = useState("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<Bill | null>(null);
  const [status, setStatus] = useState("");

  // Drop-to-create-bill flow: stash the dropped file, prompt for
  // vendor, then submit multipart to /bills/from-file.
  const [pendingFile, setPendingFile] = useState<File | null>(null);
  const [vendorPickOpen, setVendorPickOpen] = useState(false);
  const [dragOver, setDragOver] = useState(false);

  const loadList = useCallback(async () => {
    setStatus("Loading…");
    try {
      const query: Record<string, string> = {};
      if (statusFilter) query.status = statusFilter;
      const res = await apiCall<{ bills: Bill[] }>(
        "GET",
        "/bills",
        undefined,
        query,
      );
      const visible = (res.bills || []).filter((b) => {
        if (!search) return true;
        const f = search.toLowerCase();
        return (
          (b.vendor_invoice_number || "").toLowerCase().includes(f) ||
          String(b.id).includes(f) ||
          (b.notes || "").toLowerCase().includes(f) ||
          (b.category || "").toLowerCase().includes(f)
        );
      });
      setList(visible);
      setStatus(`${visible.length} bill${visible.length === 1 ? "" : "s"}`);
    } catch (err) {
      setStatus(`Error: ${(err as Error).message}`);
    }
  }, [apiCall, statusFilter, search]);

  useEffect(() => {
    loadList();
  }, [loadList]);

  useAppEvents("bills", projectId, () => {
    loadList();
    if (selectedId) loadDetail(selectedId);
  });

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const res = await apiCall<{ bill: Bill }>("GET", `/bills/${id}`);
        setDetail(res.bill);
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

  const approve = async () => {
    if (!detail) return;
    if (!confirm(`Approve this bill? It will become eligible for payment.`)) return;
    try {
      await apiCall("POST", `/bills/${detail.id}/approve`);
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Approve failed: ${(err as Error).message}`);
    }
  };

  const reject = async () => {
    if (!detail) return;
    const reason = prompt(
      `Reject this bill (vendor will need to send a corrected invoice).\n\nReason:`,
    );
    if (!reason) return;
    try {
      await apiCall("POST", `/bills/${detail.id}/reject`, { reason });
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Reject failed: ${(err as Error).message}`);
    }
  };

  const schedule = async () => {
    if (!detail) return;
    const dateStr = prompt(
      `Schedule payment date (YYYY-MM-DD or RFC3339, default now):`,
      new Date().toISOString().slice(0, 10),
    );
    if (dateStr === null) return;
    const method = prompt(
      `Method (wire / check / ach / card / other):`,
      "wire",
    );
    if (!method) return;
    try {
      await apiCall("POST", `/bills/${detail.id}/schedule`, {
        scheduled_for: dateStr,
        method,
      });
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Schedule failed: ${(err as Error).message}`);
    }
  };

  const recordPayment = async () => {
    if (!detail) return;
    const remaining = detail.total_cents - detail.amount_paid_cents;
    const amountStr = prompt(
      `Record outbound payment.\n\n` +
        `Outstanding: ${fmtMoney(remaining, detail.currency)}.\n` +
        `Amount in cents (positive only):`,
      String(remaining),
    );
    if (!amountStr) return;
    const amount = parseInt(amountStr, 10);
    if (Number.isNaN(amount) || amount <= 0) {
      alert("Amount must be a positive integer (cents).");
      return;
    }
    const method = prompt(
      "Method (wire / check / cash / ach / card / other):",
      detail.scheduled_method || "wire",
    );
    if (!method) return;
    try {
      await apiCall("POST", "/payments", {
        bill_id: detail.id,
        amount_cents: amount,
        method,
      });
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Record payment failed: ${(err as Error).message}`);
    }
  };

  const newBill = () => {
    setPendingFile(null);
    setVendorPickOpen(true);
  };

  const onDrop = (e: ReactDragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const f = e.dataTransfer.files?.[0];
    if (!f) return;
    setPendingFile(f);
    setVendorPickOpen(true);
  };

  const onVendorPicked = async (vendor: Vendor) => {
    setVendorPickOpen(false);
    try {
      let billId: number;
      if (pendingFile) {
        // multipart → /bills/from-file with bill_json {vendor_id}
        const fd = new FormData();
        fd.append("file", pendingFile);
        fd.append("bill_json", JSON.stringify({ vendor_id: vendor.id }));
        const r = await fetch(
          `${API}/bills/from-file?${queryString()}`,
          { method: "POST", credentials: "same-origin", body: fd },
        );
        if (!r.ok) throw new Error(`${r.status}: ${await r.text().catch(() => "")}`);
        const j = await r.json();
        billId = j.bill.id;
        setPendingFile(null);
      } else {
        // No file — minimal bill, vendor only.
        const j = await apiCall<{ bill: Bill }>("POST", "/bills", {
          vendor_id: vendor.id,
        });
        billId = j.bill.id;
      }
      await loadList();
      select(billId);
    } catch (err) {
      alert(`Create failed: ${(err as Error).message}`);
    }
  };

  const voidIt = async () => {
    if (!detail) return;
    const display = `${detail.vendor_invoice_number || `#${detail.id}`} (${fmtMoney(
      detail.total_cents,
      detail.currency,
    )})`;
    const reason = prompt(
      `Void ${display}?\n\nThis is silent — the vendor won't know.\nUse 'reject' instead if you want them to issue a corrected invoice.\n\nOptional reason:`,
    );
    if (reason === null) return;
    try {
      await apiCall("POST", `/bills/${detail.id}/void`, { reason });
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Void failed: ${(err as Error).message}`);
    }
  };

  return (
    <div
      className="h-full flex relative"
      onDragEnter={(e) => {
        e.preventDefault();
        setDragOver(true);
      }}
      onDragOver={(e) => e.preventDefault()}
      onDragLeave={(e) => {
        // Only clear when leaving the outermost element.
        if (e.currentTarget === e.target) setDragOver(false);
      }}
      onDrop={onDrop}
    >
      {dragOver && (
        <div className="absolute inset-0 z-10 bg-accent/10 border-4 border-dashed border-accent flex items-center justify-center pointer-events-none">
          <div className="text-accent text-lg font-medium">
            Drop a PDF or image to draft a bill
          </div>
        </div>
      )}
      {vendorPickOpen && (
        <VendorPickModal
          apiCall={apiCall}
          attachedFilename={pendingFile?.name}
          onPick={onVendorPicked}
          onCancel={() => {
            setVendorPickOpen(false);
            setPendingFile(null);
          }}
        />
      )}
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border space-y-2">
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search bills…"
              className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
            <button
              type="button"
              onClick={newBill}
              className="px-2 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg whitespace-nowrap"
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
            <option value="received">Received</option>
            <option value="approved">Approved</option>
            <option value="scheduled">Scheduled</option>
            <option value="paid">Paid</option>
            <option value="disputed">Disputed</option>
            <option value="void">Void</option>
          </select>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">No bills.</div>
          ) : (
            <ul>
              {list.map((b) => (
                <li
                  key={b.id}
                  onClick={() => select(b.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    b.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm text-text font-medium truncate">
                      {b.vendor_invoice_number || `#${b.id}`}
                    </span>
                    <span
                      className={`text-[10px] px-1.5 py-0.5 rounded ${
                        STATUS_TONE[b.status]
                      }`}
                    >
                      {b.status}
                    </span>
                  </div>
                  <div className="text-xs text-text-muted mt-0.5 flex items-center justify-between gap-2">
                    <span className="truncate">Vendor #{b.vendor_id}</span>
                    <span className="text-text">
                      {fmtMoney(b.total_cents, b.currency)}
                    </span>
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
        {!detail ? (
          <div className="text-text-muted text-sm text-center mt-12">
            {selectedId ? "Loading…" : "Select a bill to see details."}
          </div>
        ) : (
          <BillDetail
            bill={detail}
            projectId={projectId}
            installId={installId}
            onApprove={approve}
            onReject={reject}
            onSchedule={schedule}
            onRecordPayment={recordPayment}
            onVoid={voidIt}
            onAttachmentChanged={() => {
              loadList();
              loadDetail(detail.id);
            }}
          />
        )}
      </main>
    </div>
  );
}

function BillDetail({
  bill,
  projectId,
  installId,
  onApprove,
  onReject,
  onSchedule,
  onRecordPayment,
  onVoid,
  onAttachmentChanged,
}: {
  bill: Bill;
  projectId: string;
  installId: number;
  onApprove: () => void;
  onReject: () => void;
  onSchedule: () => void;
  onRecordPayment: () => void;
  onVoid: () => void;
  onAttachmentChanged: () => void;
}) {
  const remaining = bill.total_cents - bill.amount_paid_cents;
  const pdfHref = `/api/apps/bills/bills/${bill.id}/pdf?project_id=${encodeURIComponent(projectId)}`;
  const printHref = `/api/apps/bills/bills/${bill.id}/print?project_id=${encodeURIComponent(projectId)}`;

  return (
    <div className="max-w-3xl space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl text-text font-semibold flex items-center gap-2">
            {bill.vendor_invoice_number || `Bill #${bill.id}`}
            <span
              className={`text-[11px] px-1.5 py-0.5 rounded ${STATUS_TONE[bill.status]}`}
            >
              {bill.status}
            </span>
            <span className="text-[10px] uppercase text-text-dim">
              {bill.provider}
            </span>
          </h1>
          <p className="text-text-muted text-sm mt-1">
            Vendor #{bill.vendor_id}
            {bill.due_date ? ` · due ${fmtDate(bill.due_date)}` : ""}
            {bill.category ? ` · ${bill.category}` : ""}
          </p>
        </div>
        <div className="text-right">
          <div className="text-2xl text-text font-semibold">
            {fmtMoney(bill.total_cents, bill.currency)}
          </div>
          {bill.amount_paid_cents > 0 && (
            <div className="text-xs text-text-muted">
              Paid {fmtMoney(bill.amount_paid_cents, bill.currency)} · owed{" "}
              {fmtMoney(Math.max(0, remaining), bill.currency)}
            </div>
          )}
        </div>
      </header>

      {bill.line_items && bill.line_items.length > 0 && (
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
              {bill.line_items.map((li, i) => (
                <tr key={li.id ?? i} className="border-b border-border/50">
                  <td className="py-1 text-text">{li.description}</td>
                  <td className="py-1 text-right text-text-muted">
                    {li.quantity}
                  </td>
                  <td className="py-1 text-right text-text-muted">
                    {fmtMoney(li.unit_price_cents, bill.currency)}
                  </td>
                  <td className="py-1 text-right text-text-muted">
                    {(li.tax_rate_bps / 100).toFixed(2)}%
                  </td>
                  <td className="py-1 text-right text-text">
                    {fmtMoney(li.amount_cents, bill.currency)}
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
                  {fmtMoney(bill.subtotal_cents, bill.currency)}
                </td>
              </tr>
              <tr>
                <td colSpan={4} className="text-right py-1 text-text-muted">
                  Input tax
                </td>
                <td className="text-right py-1">
                  {fmtMoney(bill.tax_cents, bill.currency)}
                </td>
              </tr>
              <tr className="font-medium">
                <td colSpan={4} className="text-right py-1">
                  Total
                </td>
                <td className="text-right py-1">
                  {fmtMoney(bill.total_cents, bill.currency)}
                </td>
              </tr>
            </tfoot>
          </table>
        </section>
      )}

      {(bill.approved_at || bill.scheduled_for) && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Workflow
          </h2>
          <ul className="space-y-1 text-sm text-text-muted">
            {bill.approved_at && (
              <li>
                Approved {fmtDateTime(bill.approved_at)}
                {bill.approved_by ? ` by ${bill.approved_by}` : ""}
              </li>
            )}
            {bill.scheduled_for && (
              <li>
                Scheduled for {fmtDate(bill.scheduled_for)}
                {bill.scheduled_method ? ` (${bill.scheduled_method})` : ""}
              </li>
            )}
            {bill.disputed_at && <li>Disputed {fmtDateTime(bill.disputed_at)}</li>}
            {bill.voided_at && <li>Voided {fmtDateTime(bill.voided_at)}</li>}
          </ul>
        </section>
      )}

      <ExtractedByOCRBanner bill={bill} />

      {bill.status !== "void" && (
        <AttachedDocumentSection
          bill={bill}
          projectId={projectId}
          installId={installId}
          onChanged={onAttachmentChanged}
        />
      )}

      {bill.notes && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-1">
            Notes
          </h2>
          <p className="text-sm text-text whitespace-pre-wrap">{bill.notes}</p>
        </section>
      )}

      {bill.payments && bill.payments.length > 0 && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Payments OUT ({bill.payments.length})
          </h2>
          <ul className="space-y-1">
            {bill.payments.map((p) => (
              <li
                key={p.id}
                className="text-sm flex items-center justify-between border border-border rounded px-2 py-1"
              >
                <span className="flex items-center gap-2">
                  <span className="text-[10px] uppercase text-text-dim w-12">
                    {p.method}
                  </span>
                  <span className="text-text">{fmtDateTime(p.sent_at)}</span>
                </span>
                <span className="text-text">
                  {fmtMoney(p.amount_cents, p.currency)}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="flex flex-wrap items-center gap-2 pt-2 border-t border-border">
        {bill.status === "received" && (
          <>
            <button
              type="button"
              onClick={onApprove}
              className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
            >
              Approve
            </button>
            <button
              type="button"
              onClick={onReject}
              className="px-3 py-1 text-sm text-orange-500 border border-orange-500/50 rounded hover:bg-orange-500/10"
            >
              Reject
            </button>
          </>
        )}
        {bill.status === "approved" && (
          <button
            type="button"
            onClick={onSchedule}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            Schedule payment
          </button>
        )}
        {(bill.status === "approved" || bill.status === "scheduled") && (
          <button
            type="button"
            onClick={onRecordPayment}
            className="px-3 py-1 text-sm border border-accent text-accent rounded hover:bg-accent hover:text-bg"
          >
            Record payment
          </button>
        )}
        {bill.status !== "received" && bill.status !== "void" && (
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
              PDF
            </a>
          </>
        )}
        {bill.status !== "paid" && bill.status !== "void" && (
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

function VendorsTab({
  projectId,
  apiCall,
}: {
  projectId: string;
  apiCall: ApiCall;
}) {
  const [list, setList] = useState<Vendor[]>([]);
  const [search, setSearch] = useState("");
  const [selected, setSelected] = useState<Vendor | null>(null);
  const [openBills, setOpenBills] = useState<Bill[]>([]);
  const [recentPayments, setRecentPayments] = useState<Payment[]>([]);
  const [lifetime, setLifetime] = useState<Record<string, number> | null>(null);
  const [status, setStatus] = useState("");

  const load = useCallback(
    async (q = "") => {
      setStatus("Loading…");
      try {
        const res = await apiCall<{ vendors: Vendor[] }>(
          "GET",
          "/vendors",
          undefined,
          q ? { q } : {},
        );
        setList(res.vendors || []);
        setStatus(
          `${(res.vendors || []).length} vendor${
            (res.vendors || []).length === 1 ? "" : "s"
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

  useAppEvents("bills", projectId, (ev) => {
    if (ev.topic.startsWith("vendor.")) {
      load(search.trim());
    }
  });

  const select = async (v: Vendor) => {
    setSelected(v);
    setOpenBills([]);
    setRecentPayments([]);
    setLifetime(null);
    try {
      const res = await apiCall<{
        open_bills: Bill[];
        recent_payments: Payment[];
        lifetime: Record<string, number>;
      }>("GET", `/vendors/${v.id}/context`);
      setOpenBills(res.open_bills || []);
      setRecentPayments(res.recent_payments || []);
      setLifetime(res.lifetime || null);
    } catch (err) {
      setStatus(`Detail error: ${(err as Error).message}`);
    }
  };

  const create = async () => {
    const email = prompt("Vendor email:");
    if (!email) return;
    const name = prompt("Vendor name (optional):", email) || email;
    try {
      const res = await apiCall<{ vendor: Vendor }>(
        "POST",
        "/vendors",
        { email, defaults: { name } },
      );
      await load("");
      select(res.vendor);
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
            placeholder="Search vendors…"
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
            <div className="p-4 text-text-muted text-xs">No vendors.</div>
          ) : (
            <ul>
              {list.map((v) => (
                <li
                  key={v.id}
                  onClick={() => select(v)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    selected?.id === v.id ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="text-sm text-text font-medium truncate">
                    {v.name}
                  </div>
                  <div className="text-xs text-text-muted truncate">
                    {v.email || "—"}
                    {v.w9_received_at && (
                      <span className="ml-2 text-[10px] px-1 rounded bg-green-500/15 text-green-500">
                        W-9
                      </span>
                    )}
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
            Select a vendor to see open bills + payment history.
          </div>
        ) : (
          <div className="max-w-2xl space-y-6">
            <header>
              <h1 className="text-xl text-text font-semibold">{selected.name}</h1>
              <p className="text-text-muted text-sm">
                {selected.email || "—"}
                {selected.phone ? ` · ${selected.phone}` : ""}
                {selected.currency ? ` · ${selected.currency}` : ""}
                {selected.default_payment_method
                  ? ` · pays via ${selected.default_payment_method}`
                  : ""}
                {selected.default_payment_terms_days
                  ? ` · Net ${selected.default_payment_terms_days}`
                  : ""}
              </p>
              {!selected.w9_received_at && (
                <div className="mt-2 text-xs text-yellow-500">
                  ⚠ No W-9 on file (1099 prerequisite when enabled)
                </div>
              )}
            </header>

            {lifetime && (
              <section className="grid grid-cols-3 gap-2">
                <Stat
                  label="Billed"
                  value={fmtMoney(Number(lifetime.billed_cents || 0), currency)}
                />
                <Stat
                  label="Paid"
                  value={fmtMoney(Number(lifetime.paid_cents || 0), currency)}
                />
                <Stat
                  label="Owed"
                  value={fmtMoney(
                    Number(lifetime.outstanding_cents || 0),
                    currency,
                  )}
                />
              </section>
            )}

            <section>
              <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
                Open bills ({openBills.length})
              </h2>
              {openBills.length === 0 ? (
                <p className="text-text-muted text-sm">None.</p>
              ) : (
                <ul className="space-y-1">
                  {openBills.map((b) => (
                    <li
                      key={b.id}
                      className="flex items-center justify-between border border-border rounded px-2 py-1 text-sm"
                    >
                      <span className="flex items-center gap-2">
                        <span
                          className={`text-[10px] px-1 py-0.5 rounded ${STATUS_TONE[b.status]}`}
                        >
                          {b.status}
                        </span>
                        <span>{b.vendor_invoice_number || `#${b.id}`}</span>
                      </span>
                      <span className="text-text-muted">
                        {fmtMoney(b.total_cents, b.currency)}
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
                        {fmtDateTime(p.sent_at)}
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
