// InvoiceCard - billing's chat-attachment component. The agent
// emits respond(components=[{app:"billing", name:"invoice-card",
// props:{invoice_id:N}}]) and the dashboard renders this under
// the message bubble.

import { useEffect, useState } from "react";
import {
  AppCardHeader,
  Card,
  DataList,
  StatusPill,
  type StatusDotVariant,
  type StatusPillVariant,
} from "@apteva/ui-kit";

interface InvoiceMeta {
  id: number;
  customer_id: number;
  customer_name?: string;
  number?: string;
  status: "draft" | "open" | "paid" | "void" | "uncollectible";
  provider: "local" | "stripe";
  currency: string;
  total_cents: number;
  amount_paid_cents: number;
  due_date?: string;
  external_url?: string;
}

interface Props {
  invoice_id: number;
  projectId?: string;
  preview?: boolean;
}

const previewSample: InvoiceMeta = {
  id: 0,
  customer_id: 0,
  customer_name: "Acme Corp",
  number: "INV-2026-0042",
  status: "open",
  provider: "stripe",
  currency: "EUR",
  total_cents: 120000,
  amount_paid_cents: 45000,
  due_date: new Date(Date.now() + 14 * 86400_000).toISOString(),
  external_url: "https://checkout.stripe.com/example",
};

const statusDot: Record<InvoiceMeta["status"], StatusDotVariant> = {
  draft: "muted",
  open: "active",
  paid: "live",
  void: "muted",
  uncollectible: "warn",
};

const statusPill: Record<InvoiceMeta["status"], StatusPillVariant> = {
  draft: "neutral",
  open: "info",
  paid: "success",
  void: "neutral",
  uncollectible: "warn",
};

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
  if (!s) return "";
  try {
    return new Date(s).toLocaleDateString();
  } catch {
    return s;
  }
}

function useBillingEvents(
  projectId: string | undefined,
  onEvent: (ev: { topic: string; data: { id?: number } }) => void,
) {
  useEffect(() => {
    if (!projectId) return;

    const bridge = (window as unknown as {
      __aptevaAppEvents?: {
        subscribe(
          app: string,
          projectId: string,
          fn: (ev: { topic: string; app: string; project_id: string; data: any }) => void,
        ): () => void;
      };
    }).__aptevaAppEvents;
    if (bridge) {
      return bridge.subscribe("billing", projectId, onEvent as any);
    }
    const url = `/api/app-events/billing?project_id=${encodeURIComponent(projectId)}`;
    const es = new EventSource(url, { withCredentials: true });
    es.onmessage = (e) => {
      try {
        onEvent(JSON.parse(e.data));
      } catch {
        /* ignore malformed frames */
      }
    };
    return () => es.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);
}

export default function InvoiceCard({ invoice_id, projectId, preview }: Props) {
  const [meta, setMeta] = useState<InvoiceMeta | null>(
    preview ? previewSample : null,
  );
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    if (preview) return;
    if (!projectId) return;
    const url =
      `/api/apps/billing/invoices/${invoice_id}` +
      `?project_id=${encodeURIComponent(projectId)}`;
    fetch(url, { credentials: "same-origin" })
      .then((r) => {
        if (r.status === 404) {
          setMissing(true);
          return null;
        }
        return r.json();
      })
      .then((j) => {
        if (j && j.invoice) {
          setMeta(j.invoice as InvoiceMeta);
          setMissing(false);
        }
      })
      .catch(() => {
        /* show stale meta rather than blank */
      });
  };

  useEffect(refetch, [invoice_id, projectId, preview]);

  useBillingEvents(preview ? undefined : projectId, (ev) => {
    if (
      ev.data &&
      typeof ev.data.id === "number" &&
      ev.data.id === invoice_id
    ) {
      refetch();
    }
  });

  if (missing) {
    return (
      <Card>
        <AppCardHeader
          title={`Invoice #${invoice_id}`}
          status={{ label: "missing", variant: "muted" }}
        />
      </Card>
    );
  }
  if (!meta) {
    return (
      <Card>
        <AppCardHeader
          title={`Invoice #${invoice_id}`}
          status={{ label: "loading", variant: "muted" }}
        />
      </Card>
    );
  }

  const total = Number(meta.total_cents || 0);
  const paid = Number(meta.amount_paid_cents || 0);
  const outstanding = Math.max(0, total - paid);
  const title = meta.number || `Draft invoice #${meta.id}`;
  const customer = meta.customer_name || `Customer #${meta.customer_id}`;

  return (
    <Card>
      <AppCardHeader
        title={title}
        subtitle={customer}
        status={{ label: meta.status, variant: statusDot[meta.status] }}
        action={meta.external_url ? { label: "Open", href: meta.external_url } : undefined}
      />
      <div className="px-4 py-3 border-b border-border bg-bg-input/40">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="text-[11px] uppercase tracking-wide text-text-dim">
              Invoice total
            </div>
            <div className="text-2xl font-semibold text-text leading-tight">
              {fmtMoney(total, meta.currency)}
            </div>
          </div>
          <div className="flex flex-col items-end gap-1.5">
            <StatusPill variant={statusPill[meta.status]}>{meta.status}</StatusPill>
            <StatusPill variant={meta.provider === "stripe" ? "info" : "neutral"}>
              {meta.provider}
            </StatusPill>
          </div>
        </div>
        {meta.status !== "paid" && meta.status !== "void" && (
          <div className="mt-2 text-xs text-text-muted">
            {fmtMoney(outstanding, meta.currency)} outstanding
          </div>
        )}
      </div>
      <div className="px-4 py-3">
        <DataList
          items={[
            { label: "Paid", value: fmtMoney(paid, meta.currency) },
            {
              label: "Due",
              value: meta.due_date ? fmtDate(meta.due_date) : "No due date",
            },
            { label: "Customer", value: customer },
            { label: "Currency", value: meta.currency.toUpperCase() },
          ]}
        />
      </div>
    </Card>
  );
}
