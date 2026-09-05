import {useCallback,useEffect,useMemo,useRef,useState} from "react";
export // ─── Inline app-events SSE hook ─────────────────────────────────────
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
export function useAppEvents<T = unknown>(app: string, projectId: string | undefined | null, onEvent: (ev: AppEventEnvelope<T>) => void) {
    const handlerRef = useRef(onEvent);
    handlerRef.current = onEvent;
    useEffect(() => {
        if (!app || !projectId)
            return;
        let refreshTimer: ReturnType<typeof setTimeout> | undefined;
        const handler = (ev: AppEventEnvelope<T>) => { clearTimeout(refreshTimer); refreshTimer = setTimeout(() => handlerRef.current(ev), 150); };
        // Cross-bundle multiplexer: the dashboard publishes a shared
        // (app, project) channel pool on window.__aptevaAppEvents. Every
        // panel mounted in the same realm reuses one EventSource per
        // (app, project) instead of opening its own. Without this, a few
        // panels mounted in the agent detail page burn the browser's
        // per-origin HTTP/1.1 connection budget and stuck POSTs follow.
        const bridge = (window as unknown as {
            __aptevaAppEvents?: {
                subscribe(app: string, projectId: string, fn: (ev: AppEventEnvelope<T>) => void): () => void;
            };
        }).__aptevaAppEvents;
        if (bridge) {
            const unsubscribe = bridge.subscribe(app, projectId, handler);
            return () => { clearTimeout(refreshTimer); unsubscribe(); };
        }
        // Fallback: panel running outside the dashboard (or before its
        // hook module loaded). Open an EventSource directly.
        let lastSeq = 0;
        let es: EventSource | null = null;
        let cancelled = false;
        let reconnectTimer: number | null = null;
        const connect = () => {
            if (cancelled)
                return;
            const url = `/api/app-events/${encodeURIComponent(app)}` +
                `?project_id=${encodeURIComponent(projectId)}` +
                (lastSeq > 0 ? `&since=${lastSeq}` : "");
            es = new EventSource(url, { withCredentials: true });
            es.onmessage = (e) => {
                try {
                    const ev = JSON.parse(e.data) as AppEventEnvelope<T>;
                    if (ev.seq <= lastSeq)
                        return;
                    lastSeq = ev.seq;
                    handler(ev);
                }
                catch { }
            };
            es.onerror = () => {
                if (es && es.readyState === EventSource.CLOSED) {
                    if (reconnectTimer)
                        window.clearTimeout(reconnectTimer);
                    reconnectTimer = window.setTimeout(connect, 2000);
                }
            };
        };
        connect();
        return () => {
            cancelled = true;
            clearTimeout(refreshTimer);
            if (reconnectTimer)
                window.clearTimeout(reconnectTimer);
            if (es)
                es.close();
        };
    }, [app, projectId]);
}
export // ─── Types ──────────────────────────────────────────────────────────
interface NativePanelProps {
    appName: string;
    installId: number;
    projectId: string;
    instanceId?: number;
}
export interface Customer {
    id: number;
    name: string;
    email?: string;
    phone?: string;
    currency?: string;
    external_id?: string;
    created_at?: string;
    updated_at?: string;
    billing_address?: CustomerAddress;
    tax_ids?: TaxIdDraft[];
    metadata?: {
        bank?: {
            iban?: string;
            bic?: string;
            bank_name?: string;
            bank_code?: string;
            beneficiary?: string;
        };
        contact?: {
            name?: string;
            title?: string;
        };
        website?: string;
        notes?: string;
        [key: string]: unknown;
    };
}
export interface CustomerAddress {
    line1?: string;
    line2?: string;
    postal_code?: string;
    city?: string;
    state?: string;
    country?: string;
}
export interface LineItem {
    price_id?: number;
    product_id?: number;
    metadata?: Record<string, unknown>;
    id?: number;
    position?: number;
    description: string;
    quantity: number;
    unit_price_cents: number;
    amount_cents: number;
    tax_rate_bps: number;
}
export interface Payment {
    id: number;
    invoice_id?: number;
    customer_id: number;
    amount_cents: number;
    currency: string;
    method: string;
    received_at: string;
    notes?: string;
}
export interface PaymentMethod {
    id: number;
    customer_id: number;
    customer_name?: string;
    customer_email?: string;
    provider: string;
    provider_customer_id?: string;
    provider_payment_method_id: string;
    provider_mandate_id?: string;
    type: string;
    status: string;
    is_default: boolean;
    reusable: boolean;
    delayed_notification: boolean;
    display_brand?: string;
    display_last4?: string;
    exp_month?: number;
    exp_year?: number;
    country?: string;
    currency?: string;
    created_at?: string;
    updated_at?: string;
    detached_at?: string;
}
export interface AuditEntry {
    id: number;
    invoice_id: number;
    actor: string;
    action: string;
    details?: unknown;
    created_at: string;
}
export interface Invoice {
    accounting_date?: string;
    tax_treatment?: string;
    collection_hold?: boolean;
    credit_cents?: number;
    id: number;
    customer_id: number;
    customer_name?: string;
    customer_email?: string;
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
export // ─── Formatters ─────────────────────────────────────────────────────
function fmtMoney(cents: number, currency: string): string {
    try {
        return new Intl.NumberFormat(undefined, {
            style: "currency",
            currency: (currency || "USD").toUpperCase(),
            currencyDisplay: "narrowSymbol",
        }).format(cents / 100);
    }
    catch {
        return `${(cents / 100).toFixed(2)} ${currency}`;
    }
}
export function fmtDate(s?: string): string {
    if (!s)
        return "—";
    try {
        return new Date(s).toLocaleDateString();
    }
    catch {
        return s;
    }
}
export function fmtDateTime(s?: string): string {
    if (!s)
        return "—";
    try {
        return new Date(s).toLocaleString();
    }
    catch {
        return s;
    }
}
export type InvoiceDatePreset = "all" | "this_month" | "last_month" | "past_3_months" | "custom";
export function dateInputValue(d: Date): string {
    const yyyy = d.getFullYear();
    const mm = String(d.getMonth() + 1).padStart(2, "0");
    const dd = String(d.getDate()).padStart(2, "0");
    return `${yyyy}-${mm}-${dd}`;
}
export function addDays(dateKey: string, days: number): string {
    const d = new Date(`${dateKey}T00:00:00`);
    if (Number.isNaN(d.getTime()))
        return "";
    d.setDate(d.getDate() + days);
    return dateInputValue(d);
}
export function invoiceDateRange(preset: InvoiceDatePreset, customSince: string, customUntil: string): {
    since?: string;
    until?: string;
} {
    const now = new Date();
    if (preset === "this_month") {
        const start = new Date(now.getFullYear(), now.getMonth(), 1);
        const next = new Date(now.getFullYear(), now.getMonth() + 1, 1);
        return { since: dateInputValue(start), until: dateInputValue(next) };
    }
    if (preset === "last_month") {
        const start = new Date(now.getFullYear(), now.getMonth() - 1, 1);
        const next = new Date(now.getFullYear(), now.getMonth(), 1);
        return { since: dateInputValue(start), until: dateInputValue(next) };
    }
    if (preset === "past_3_months") {
        const start = new Date(now);
        start.setMonth(start.getMonth() - 3);
        return { since: dateInputValue(start) };
    }
    if (preset === "custom") {
        return {
            since: customSince || undefined,
            until: customUntil ? addDays(customUntil, 1) : undefined,
        };
    }
    return {};
}
export const STATUS_TONE: Record<Invoice["status"], string> = {
    draft: "bg-border text-text-muted",
    open: "bg-accent/15 text-accent",
    paid: "bg-green-500/15 text-green-500",
    void: "bg-text-dim/15 text-text-dim line-through",
    uncollectible: "bg-yellow-500/15 text-yellow-500",
};
export function roundMoney(value: number) { return Math.sign(value) * Math.round(Math.abs(value)); }
export const API = "/api/apps/billing";
export // ─── Invoices tab ───────────────────────────────────────────────────
type ApiCall = <T>(method: string, path: string, body?: unknown, query?: Record<string, string>) => Promise<T>;
export // ─── Create invoice modal ───────────────────────────────────────────
interface LineDraft {
    metadata?: Record<string, unknown>;
    description: string;
    quantity: string; // user-edited; converted to number on submit
    unit_price: string; // dollars (decimal); converted to cents on submit
    tax_rate: string; // percent (decimal); converted to bps on submit
    // Optional catalog references — set when the user picks a catalog
    // price via the "+ Add from catalog" flow. Carried through to the
    // POST body so billing snapshots the catalog price + records FKs
    // for analytics. NULL on ad-hoc / free-form lines.
    price_id?: number;
    product_id?: number;
    // Cosmetic — shown next to description in the line row so the user
    // sees the line came from the catalog (and which product).
    catalog_label?: string;
}
export function emptyLine(): LineDraft {
    return { description: "", quantity: "1", unit_price: "", tax_rate: "" };
}
export // Catalog price picker: fetches active prices grouped by product on
// open; lets the user pick one, returns it via onPick.
interface CatalogPriceOption {
    price_id: number;
    product_id: number;
    product_name: string;
    product_color?: string;
    nickname: string;
    unit_amount_cents: number;
    currency: string;
    interval?: string;
}
export // Cross-app fetch — billing panel calls catalog through the dashboard
// proxy, same as agents do via MCP. project_id is the only required
// scope; install_id is catalog's internal concern, not billing's.
const catalogRequests = new Map<string, {
    at: number;
    promise: Promise<CatalogPriceOption[]>;
}>();
export function fetchCatalogPriceOptions(projectId: string) { const old = catalogRequests.get(projectId); if (old && Date.now() - old.at < 30000)
    return old.promise; const promise = loadCatalogPriceOptions(projectId).catch(e => { catalogRequests.delete(projectId); throw e; }); catalogRequests.set(projectId, { at: Date.now(), promise }); return promise; }
export async function mapLimited<T, R>(values: T[], fn: (v: T) => Promise<R>): Promise<R[]> { let next = 0; const out: R[] = []; await Promise.all(Array.from({ length: Math.min(4, values.length) }, async () => { while (next < values.length) {
    const n = next++;
    out[n] = await fn(values[n]);
} })); return out; }
export async function loadCatalogPriceOptions(projectId: string): Promise<CatalogPriceOption[]> {
    const r = await fetch(`/api/apps/catalog/products?project_id=${encodeURIComponent(projectId)}&archived=false`, { credentials: "same-origin", cache: "no-store" });
    if (!r.ok) {
        if (r.status === 404) {
            throw new Error("The Catalog app isn't installed. Install it from the app marketplace to pick prices from your catalog.");
        }
        throw new Error(`Catalog unreachable (HTTP ${r.status}).`);
    }
    const data = (await r.json()) as {
        products: CatalogProductWithPrices[];
    };
    const groups = await mapLimited(data.products || [], async (p) => {
        // The products endpoint doesn't include prices, so fetch product details
        // concurrently; latency stays near one round trip instead of N serial ones.
        // Larger catalogs would warrant a /products?include=prices flag
        // on the catalog side, but defer until the use case shows up.
        try {
            const pr = await fetch(`/api/apps/catalog/products/${p.id}?project_id=${encodeURIComponent(projectId)}`, { credentials: "same-origin", cache: "no-store" });
            if (!pr.ok)
                throw new Error(`Could not load catalog product ${p.id} (${pr.status})`);
            const detail = (await pr.json()) as {
                product: CatalogProductWithPrices;
            };
            const options: CatalogPriceOption[] = [];
            for (const price of detail.product.prices || []) {
                if (!price.active || price.archived_at)
                    continue;
                options.push({
                    price_id: price.id,
                    product_id: p.id,
                    product_name: p.name,
                    product_color: p.color,
                    nickname: price.nickname || "",
                    unit_amount_cents: price.unit_amount_cents,
                    currency: price.currency,
                    interval: price.interval,
                });
            }
            return options;
        }
        catch (error) {
            throw error;
        }
    });
    return groups.flat();
}
export interface CatalogProductWithPrices {
    id: number;
    name: string;
    color?: string;
    prices?: Array<{
        id: number;
        nickname?: string;
        unit_amount_cents: number;
        currency: string;
        interval?: string;
        active: boolean;
        archived_at?: string;
    }>;
}
export function fmtCatalogOption(o: CatalogPriceOption): string {
    const amount = (o.unit_amount_cents / 100).toFixed(2);
    const period = o.interval ? `/${o.interval}` : "";
    const label = o.nickname || "(no nickname)";
    return `${o.product_name} — ${label}: ${amount} ${o.currency}${period}`;
}
export // ─── Create customer modal ──────────────────────────────────────────
interface TaxIdDraft {
    type: string;
    value: string;
}
export // ─── Payment methods tab ────────────────────────────────────────────
function paymentMethodLabel(pm: PaymentMethod): string {
    const brand = pm.display_brand || pm.type || pm.provider;
    const last4 = pm.display_last4 ? `•••• ${pm.display_last4}` : pm.provider_payment_method_id;
    return `${brand} ${last4}`.trim();
}
export // ─── Settings tab — issuer (BILL FROM) ──────────────────────────────
interface BillingAddress {
    line1?: string;
    line2?: string;
    postal_code?: string;
    city?: string;
    state?: string;
    country?: string;
}
export interface BankCoords {
    iban?: string;
    bic?: string;
    bank_name?: string;
    bank_code?: string;
    beneficiary?: string;
}
export interface Issuer {
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
