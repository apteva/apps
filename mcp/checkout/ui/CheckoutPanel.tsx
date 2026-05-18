// CheckoutPanel — admin view for carts + checkout sessions.
//
// v0.1.0 is admin-only (no buyer-facing UI). The customer-facing
// storefront blocks live in the `content` app and call the same REST
// endpoints. This panel is for the seller: see what's in flight,
// debug abandoned carts, cancel stale sessions, follow up on
// awaiting-payment invoices.

import { useCallback, useEffect, useRef, useState } from "react";

// ─── Inline app-events SSE hook ─────────────────────────────────────

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

interface CartItem {
  id: number;
  cart_id: number;
  price_id: number;
  product_id: number;
  description: string;
  unit_amount_cents: number;
  currency: string;
  quantity: number;
}

interface Cart {
  id: number;
  session_token?: string;
  customer_id?: number;
  subtotal_cents: number;
  currency: string;
  item_count: number;
  status: "open" | "checkout" | "converted" | "abandoned";
  invoice_id?: number;
  created_at?: string;
  updated_at?: string;
  expires_at?: string;
  items?: CartItem[];
}

interface CheckoutSession {
  id: number;
  cart_id: number;
  provider: "manual" | "stripe";
  provider_session_id?: string;
  email?: string;
  customer_name?: string;
  shipping_address?: Record<string, unknown>;
  billing_address?: Record<string, unknown>;
  status:
    | "started"
    | "awaiting_payment"
    | "paid"
    | "cancelled"
    | "expired";
  invoice_id?: number;
  subtotal_cents: number;
  tax_cents: number;
  total_cents: number;
  currency: string;
  created_at?: string;
  updated_at?: string;
  completed_at?: string;
  expires_at?: string;
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

function fmtDate(s?: string): string {
  if (!s) return "—";
  try {
    return new Date(s).toLocaleString();
  } catch {
    return s;
  }
}

function fmtRelative(s?: string): string {
  if (!s) return "";
  try {
    const t = new Date(s).getTime();
    const diff = Date.now() - t;
    if (diff < 60_000) return "just now";
    if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
    if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
    return `${Math.floor(diff / 86_400_000)}d ago`;
  } catch {
    return "";
  }
}

const CART_STATUS_TONE: Record<Cart["status"], string> = {
  open: "bg-accent/15 text-accent",
  checkout: "bg-yellow-500/15 text-yellow-500",
  converted: "bg-green-500/15 text-green-500",
  abandoned: "bg-text-dim/15 text-text-dim",
};

const SESSION_STATUS_TONE: Record<CheckoutSession["status"], string> = {
  started: "bg-accent/15 text-accent",
  awaiting_payment: "bg-yellow-500/15 text-yellow-500",
  paid: "bg-green-500/15 text-green-500",
  cancelled: "bg-text-dim/15 text-text-dim",
  expired: "bg-red/15 text-red",
};

const API = "/api/apps/checkout";

// ─── Panel root ─────────────────────────────────────────────────────

export default function CheckoutPanel({ projectId, installId }: NativePanelProps) {
  const [tab, setTab] = useState<"carts" | "sessions">("carts");

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
          onClick={() => setTab("carts")}
          className={`px-3 py-1 rounded ${
            tab === "carts" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Carts
        </button>
        <button
          type="button"
          onClick={() => setTab("sessions")}
          className={`px-3 py-1 rounded ${
            tab === "sessions" ? "bg-accent text-bg" : "hover:bg-bg-input/50"
          }`}
        >
          Sessions
        </button>
      </nav>

      <div className="flex-1 overflow-hidden">
        {tab === "carts" ? (
          <CartsTab projectId={projectId} apiCall={apiCall} />
        ) : (
          <SessionsTab projectId={projectId} apiCall={apiCall} />
        )}
      </div>
    </div>
  );
}

// ─── Carts tab ──────────────────────────────────────────────────────

function CartsTab({ projectId, apiCall }: { projectId: string; apiCall: ApiCall }) {
  const [list, setList] = useState<Cart[]>([]);
  const [statusFilter, setStatusFilter] = useState<string>("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<Cart | null>(null);
  const [status, setStatus] = useState<string>("");

  const loadList = useCallback(async () => {
    setStatus("Loading…");
    try {
      const query: Record<string, string> = {};
      if (statusFilter) query.status = statusFilter;
      const res = await apiCall<{ carts: Cart[] }>(
        "GET",
        "/carts",
        undefined,
        query,
      );
      setList(res.carts || []);
      const n = (res.carts || []).length;
      setStatus(`${n} cart${n === 1 ? "" : "s"}`);
    } catch (err) {
      setStatus(`Error: ${(err as Error).message}`);
    }
  }, [apiCall, statusFilter]);

  useEffect(() => {
    loadList();
  }, [loadList]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const res = await apiCall<{ cart: Cart }>("GET", `/carts/${id}`);
        setDetail(res.cart);
      } catch (err) {
        setStatus(`Detail error: ${(err as Error).message}`);
      }
    },
    [apiCall],
  );

  useAppEvents("checkout", projectId, () => {
    loadList();
    if (selectedId) loadDetail(selectedId);
  });

  const select = (id: number) => {
    setSelectedId(id);
    loadDetail(id);
  };

  return (
    <div className="h-full flex">
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border">
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="">All statuses</option>
            <option value="open">Open</option>
            <option value="checkout">In checkout</option>
            <option value="converted">Converted</option>
            <option value="abandoned">Abandoned</option>
          </select>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">No carts.</div>
          ) : (
            <ul>
              {list.map((c) => (
                <li
                  key={c.id}
                  onClick={() => select(c.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    c.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm text-text font-medium truncate">
                      Cart #{c.id}
                    </span>
                    <span
                      className={`text-[10px] px-1.5 py-0.5 rounded ${CART_STATUS_TONE[c.status]}`}
                    >
                      {c.status}
                    </span>
                  </div>
                  <div className="text-xs text-text-muted mt-0.5 flex items-center justify-between gap-2">
                    <span className="truncate">
                      {c.item_count} item{c.item_count === 1 ? "" : "s"}
                      {c.session_token ? " · guest" : ""}
                      {c.customer_id ? ` · customer #${c.customer_id}` : ""}
                    </span>
                    <span className="text-text">
                      {fmtMoney(c.subtotal_cents, c.currency)}
                    </span>
                  </div>
                  <div className="text-[10px] text-text-dim mt-0.5">
                    {fmtRelative(c.updated_at)}
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
            {selectedId ? "Loading…" : "Select a cart to see its items."}
          </div>
        ) : (
          <CartDetail cart={detail} />
        )}
      </main>
    </div>
  );
}

function CartDetail({ cart }: { cart: Cart }) {
  return (
    <div className="max-w-2xl space-y-6">
      <header>
        <h1 className="text-xl text-text font-semibold flex items-center gap-2">
          Cart #{cart.id}
          <span
            className={`text-[11px] px-1.5 py-0.5 rounded ${CART_STATUS_TONE[cart.status]}`}
          >
            {cart.status}
          </span>
        </h1>
        <p className="text-text-muted text-sm mt-1">
          {cart.session_token ? (
            <>
              Guest · token <code className="text-[10px]">{cart.session_token.slice(0, 12)}…</code>
            </>
          ) : cart.customer_id ? (
            <>Customer #{cart.customer_id}</>
          ) : (
            "—"
          )}
          {cart.invoice_id ? ` · invoice #${cart.invoice_id}` : ""}
        </p>
        <p className="text-xs text-text-dim mt-1">
          Created {fmtDate(cart.created_at)} · updated {fmtRelative(cart.updated_at)}
          {cart.expires_at ? ` · expires ${fmtRelative(cart.expires_at)}` : ""}
        </p>
      </header>

      <section>
        <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
          Items ({cart.item_count})
        </h2>
        {!cart.items || cart.items.length === 0 ? (
          <p className="text-text-muted text-sm">Empty cart.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-text-dim text-xs">
              <tr className="border-b border-border">
                <th className="text-left font-normal py-1">Description</th>
                <th className="text-right font-normal py-1 w-16">Qty</th>
                <th className="text-right font-normal py-1 w-28">Unit</th>
                <th className="text-right font-normal py-1 w-32">Line</th>
              </tr>
            </thead>
            <tbody>
              {cart.items.map((it) => {
                const line = Math.round(it.unit_amount_cents * it.quantity);
                return (
                  <tr key={it.id} className="border-b border-border/50">
                    <td className="py-1 text-text">
                      {it.description}
                      <div className="text-[10px] text-text-dim">
                        price #{it.price_id} · product #{it.product_id}
                      </div>
                    </td>
                    <td className="py-1 text-right text-text-muted">{it.quantity}</td>
                    <td className="py-1 text-right text-text-muted">
                      {fmtMoney(it.unit_amount_cents, it.currency)}
                    </td>
                    <td className="py-1 text-right text-text">
                      {fmtMoney(line, it.currency)}
                    </td>
                  </tr>
                );
              })}
            </tbody>
            <tfoot>
              <tr className="font-medium text-text">
                <td colSpan={3} className="text-right py-1">
                  Subtotal
                </td>
                <td className="text-right py-1">
                  {fmtMoney(cart.subtotal_cents, cart.currency)}
                </td>
              </tr>
            </tfoot>
          </table>
        )}
      </section>
    </div>
  );
}

// ─── Sessions tab ──────────────────────────────────────────────────

function SessionsTab({ projectId, apiCall }: { projectId: string; apiCall: ApiCall }) {
  const [list, setList] = useState<CheckoutSession[]>([]);
  const [statusFilter, setStatusFilter] = useState<string>("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [detail, setDetail] = useState<CheckoutSession | null>(null);
  const [linkedCart, setLinkedCart] = useState<Cart | null>(null);
  const [status, setStatus] = useState<string>("");

  const loadList = useCallback(async () => {
    setStatus("Loading…");
    try {
      const query: Record<string, string> = {};
      if (statusFilter) query.status = statusFilter;
      const res = await apiCall<{ sessions: CheckoutSession[] }>(
        "GET",
        "/sessions",
        undefined,
        query,
      );
      setList(res.sessions || []);
      const n = (res.sessions || []).length;
      setStatus(`${n} session${n === 1 ? "" : "s"}`);
    } catch (err) {
      setStatus(`Error: ${(err as Error).message}`);
    }
  }, [apiCall, statusFilter]);

  useEffect(() => {
    loadList();
  }, [loadList]);

  const loadDetail = useCallback(
    async (id: number) => {
      try {
        const res = await apiCall<{ session: CheckoutSession }>(
          "GET",
          `/sessions/${id}`,
        );
        setDetail(res.session);
        try {
          const cartRes = await apiCall<{ cart: Cart }>(
            "GET",
            `/carts/${res.session.cart_id}`,
          );
          setLinkedCart(cartRes.cart);
        } catch {
          setLinkedCart(null);
        }
      } catch (err) {
        setStatus(`Detail error: ${(err as Error).message}`);
      }
    },
    [apiCall],
  );

  useAppEvents("checkout", projectId, () => {
    loadList();
    if (selectedId) loadDetail(selectedId);
  });

  const select = (id: number) => {
    setSelectedId(id);
    loadDetail(id);
  };

  const cancel = async () => {
    if (!detail) return;
    if (!confirm(`Cancel session #${detail.id}? Cart will release back to open.`)) return;
    try {
      await apiCall("POST", `/sessions/${detail.id}/cancel`);
      await loadList();
      await loadDetail(detail.id);
    } catch (err) {
      alert(`Cancel failed: ${(err as Error).message}`);
    }
  };

  return (
    <div className="h-full flex">
      <aside className="w-96 border-r border-border flex flex-col">
        <div className="p-2 border-b border-border">
          <select
            value={statusFilter}
            onChange={(e) => setStatusFilter(e.target.value)}
            className="w-full bg-bg-input border border-border rounded px-2 py-1 text-sm"
          >
            <option value="">All statuses</option>
            <option value="started">Started</option>
            <option value="awaiting_payment">Awaiting payment</option>
            <option value="paid">Paid</option>
            <option value="cancelled">Cancelled</option>
            <option value="expired">Expired</option>
          </select>
        </div>
        <div className="flex-1 overflow-auto">
          {list.length === 0 ? (
            <div className="p-4 text-text-muted text-xs">No sessions.</div>
          ) : (
            <ul>
              {list.map((s) => (
                <li
                  key={s.id}
                  onClick={() => select(s.id)}
                  className={`px-3 py-2 cursor-pointer border-b border-border hover:bg-bg-input/50 ${
                    s.id === selectedId ? "bg-bg-input" : ""
                  }`}
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-sm text-text font-medium truncate">
                      Session #{s.id}
                    </span>
                    <span
                      className={`text-[10px] px-1.5 py-0.5 rounded ${SESSION_STATUS_TONE[s.status]}`}
                    >
                      {s.status.replace("_", " ")}
                    </span>
                  </div>
                  <div className="text-xs text-text-muted mt-0.5 flex items-center justify-between gap-2">
                    <span className="truncate">
                      {s.email || "(no email)"}
                    </span>
                    <span className="text-text">
                      {fmtMoney(s.total_cents, s.currency)}
                    </span>
                  </div>
                  <div className="text-[10px] text-text-dim mt-0.5">
                    cart #{s.cart_id} · {s.provider} · {fmtRelative(s.updated_at)}
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
            {selectedId ? "Loading…" : "Select a session to see details."}
          </div>
        ) : (
          <SessionDetail
            session={detail}
            cart={linkedCart}
            onCancel={cancel}
          />
        )}
      </main>
    </div>
  );
}

function SessionDetail({
  session,
  cart,
  onCancel,
}: {
  session: CheckoutSession;
  cart: Cart | null;
  onCancel: () => void;
}) {
  const canCancel = session.status === "started" || session.status === "awaiting_payment";
  return (
    <div className="max-w-3xl space-y-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl text-text font-semibold flex items-center gap-2">
            Session #{session.id}
            <span
              className={`text-[11px] px-1.5 py-0.5 rounded ${SESSION_STATUS_TONE[session.status]}`}
            >
              {session.status.replace("_", " ")}
            </span>
            <span className="text-[10px] uppercase text-text-dim">
              {session.provider}
            </span>
          </h1>
          <p className="text-text-muted text-sm mt-1">
            Cart #{session.cart_id}
            {session.invoice_id ? ` · invoice #${session.invoice_id}` : ""}
          </p>
          <p className="text-xs text-text-dim mt-1">
            Started {fmtDate(session.created_at)}
            {session.completed_at
              ? ` · completed ${fmtDate(session.completed_at)}`
              : session.expires_at
                ? ` · expires ${fmtRelative(session.expires_at)}`
                : ""}
          </p>
        </div>
        <div className="text-right">
          <div className="text-2xl text-text font-semibold">
            {fmtMoney(session.total_cents, session.currency)}
          </div>
        </div>
      </header>

      <section>
        <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">Buyer</h2>
        <div className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm">
          <div className="text-text-muted">Email</div>
          <div className="text-text">{session.email || "—"}</div>
          <div className="text-text-muted">Name</div>
          <div className="text-text">{session.customer_name || "—"}</div>
          <div className="text-text-muted">Shipping</div>
          <div className="text-text whitespace-pre-wrap">
            {session.shipping_address && Object.keys(session.shipping_address).length > 0
              ? Object.entries(session.shipping_address)
                  .map(([k, v]) => `${k}: ${v}`)
                  .join("\n")
              : "—"}
          </div>
          <div className="text-text-muted">Billing</div>
          <div className="text-text whitespace-pre-wrap">
            {session.billing_address && Object.keys(session.billing_address).length > 0
              ? Object.entries(session.billing_address)
                  .map(([k, v]) => `${k}: ${v}`)
                  .join("\n")
              : "—"}
          </div>
        </div>
      </section>

      {cart && cart.items && cart.items.length > 0 && (
        <section>
          <h2 className="text-xs uppercase tracking-wide text-text-dim mb-2">
            Items snapshot
          </h2>
          <ul className="space-y-1">
            {cart.items.map((it) => (
              <li
                key={it.id}
                className="flex items-center justify-between border border-border rounded px-2 py-1 text-sm"
              >
                <span className="truncate">
                  {it.description} <span className="text-text-dim">× {it.quantity}</span>
                </span>
                <span className="text-text-muted">
                  {fmtMoney(
                    Math.round(it.unit_amount_cents * it.quantity),
                    it.currency,
                  )}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <div className="flex items-center gap-2 pt-2 border-t border-border">
        {session.invoice_id && (
          <span className="text-xs text-text-muted">
            Manual payment path — record payment from billing once received.
          </span>
        )}
        {canCancel && (
          <button
            type="button"
            onClick={onCancel}
            className="ml-auto px-3 py-1 text-sm text-red border border-red/50 rounded hover:bg-red/10"
          >
            Cancel session
          </button>
        )}
      </div>
    </div>
  );
}
