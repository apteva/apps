// CustomerCard - billing's chat-attachment for a customer. Used when
// the agent surfaces a billing account snapshot in conversation.

import { useEffect, useState } from "react";
import {
  AppCardHeader,
  Card,
  DataList,
  StatusPill,
} from "@apteva/ui-kit";

interface Customer {
  id: number;
  name: string;
  email?: string;
  phone?: string;
  currency?: string;
  external_id?: string;
}

interface Lifetime {
  invoice_count?: number;
  invoiced_cents?: number;
  paid_cents?: number;
  outstanding_cents?: number;
}

interface OpenInvoice {
  id: number;
  number?: string;
  status?: string;
  total_cents?: number;
  currency?: string;
}

interface ContextPayload {
  customer: Customer;
  open_invoices?: OpenInvoice[];
  lifetime?: Lifetime;
}

interface Props {
  customer_id: number;
  projectId?: string;
  preview?: boolean;
}

const previewSample: ContextPayload = {
  customer: {
    id: 0,
    name: "Acme Corp",
    email: "ap@acme.example",
    phone: "+34 600 000 000",
    currency: "EUR",
  },
  open_invoices: [
    { id: 41, number: "INV-2026-0041", status: "open", total_cents: 80000, currency: "EUR" },
    { id: 42, number: "INV-2026-0042", status: "open", total_cents: 40000, currency: "EUR" },
  ],
  lifetime: {
    invoice_count: 7,
    invoiced_cents: 540000,
    paid_cents: 420000,
    outstanding_cents: 120000,
  },
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

export default function CustomerCard({
  customer_id,
  projectId,
  preview,
}: Props) {
  const [data, setData] = useState<ContextPayload | null>(
    preview ? previewSample : null,
  );
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    if (preview) return;
    if (!projectId) return;
    const url =
      `/api/apps/billing/customers/${customer_id}/context` +
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
        if (j && j.customer) {
          setData(j as ContextPayload);
          setMissing(false);
        }
      })
      .catch(() => {
        /* keep stale data */
      });
  };

  useEffect(refetch, [customer_id, projectId, preview]);

  useBillingEvents(preview ? undefined : projectId, (ev) => {
    if (
      ev.data &&
      typeof ev.data.id === "number" &&
      ev.data.id === customer_id
    ) {
      refetch();
    }
    if (ev.topic === "invoice.paid" || ev.topic === "invoice.added") {
      refetch();
    }
  });

  if (missing) {
    return (
      <Card>
        <AppCardHeader
          title={`Customer #${customer_id}`}
          status={{ label: "missing", variant: "muted" }}
        />
      </Card>
    );
  }
  if (!data) {
    return (
      <Card>
        <AppCardHeader
          title={`Customer #${customer_id}`}
          status={{ label: "loading", variant: "muted" }}
        />
      </Card>
    );
  }

  const c = data.customer;
  const currency = c.currency || "USD";
  const lt = data.lifetime || {};
  const openInvoices = data.open_invoices || [];
  const openCount = openInvoices.length;
  const outstanding = Number(lt.outstanding_cents || 0);
  const subtitle = [c.email, c.phone].filter(Boolean).join(" - ");

  return (
    <Card>
      <AppCardHeader
        title={c.name}
        subtitle={subtitle || `Customer #${c.id}`}
        status={{
          label: openCount > 0 ? `${openCount} open` : "current",
          variant: openCount > 0 ? "warn" : "live",
        }}
      />
      <div className="px-4 py-3 border-b border-border bg-bg-input/40">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="text-[11px] uppercase tracking-wide text-text-dim">
              Outstanding
            </div>
            <div className="text-2xl font-semibold text-text leading-tight">
              {fmtMoney(outstanding, currency)}
            </div>
          </div>
          <div className="flex flex-col items-end gap-1.5">
            <StatusPill variant={openCount > 0 ? "warn" : "success"}>
              {openCount > 0 ? `${openCount} open` : "settled"}
            </StatusPill>
            <StatusPill variant="neutral">{currency.toUpperCase()}</StatusPill>
          </div>
        </div>
        {openInvoices.length > 0 && (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {openInvoices.slice(0, 3).map((inv) => (
              <span
                key={inv.id}
                className="inline-flex items-center gap-1 rounded-md border border-border bg-bg-card px-2 py-1 text-[11px] text-text-muted"
              >
                <span className="font-medium text-text">
                  {inv.number || `#${inv.id}`}
                </span>
                {typeof inv.total_cents === "number" && (
                  <span>
                    {fmtMoney(inv.total_cents, inv.currency || currency)}
                  </span>
                )}
              </span>
            ))}
            {openInvoices.length > 3 && (
              <span className="inline-flex items-center rounded-md border border-border bg-bg-card px-2 py-1 text-[11px] text-text-dim">
                +{openInvoices.length - 3} more
              </span>
            )}
          </div>
        )}
      </div>
      <div className="px-4 py-3">
        <DataList
          items={[
            {
              label: "Invoiced",
              value: fmtMoney(Number(lt.invoiced_cents || 0), currency),
            },
            {
              label: "Paid",
              value: fmtMoney(Number(lt.paid_cents || 0), currency),
            },
            {
              label: "Invoices",
              value: String(lt.invoice_count || 0),
            },
            {
              label: "External ID",
              value: c.external_id || "None",
            },
          ]}
        />
      </div>
    </Card>
  );
}
