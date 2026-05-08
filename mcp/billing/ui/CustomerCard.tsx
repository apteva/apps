// CustomerCard — billing's chat-attachment for a customer. Used when
// the agent says "found Acme Corp — here's their billing snapshot"
// or similar. Composes ui-kit primitives like InvoiceCard does.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList } from "@apteva/ui-kit";

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

interface ContextPayload {
  customer: Customer;
  open_invoices?: Array<{ id: number }>;
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
    currency: "USD",
  },
  open_invoices: [{ id: 1 }, { id: 2 }],
  lifetime: {
    invoice_count: 7,
    invoiced_cents: 540000,
    paid_cents: 460000,
    outstanding_cents: 80000,
  },
};

function fmtMoney(cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: (currency || "USD").toUpperCase(),
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

        // Bridge to the dashboard's shared (app, project) multiplexer
        // when it's loaded. Without this, every Card mount opens its own
        // EventSource — N cards in a chat thread = N connections, which
        // blows past Chrome's per-origin HTTP/1.1 cap and freezes the
        // dashboard. Falls back to a direct EventSource when running
        // outside the dashboard (standalone preview, future surfaces).
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
      } catch {}
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
      .catch(() => {});
  };

  useEffect(refetch, [customer_id, projectId, preview]);

  useBillingEvents(projectId, (ev) => {
    if (
      ev.data &&
      typeof ev.data.id === "number" &&
      ev.data.id === customer_id
    ) {
      refetch();
    }
    if (ev.topic === "invoice.paid" || ev.topic === "invoice.added") {
      // Cheap re-fetch: the lifetime totals may have moved.
      refetch();
    }
  });

  if (missing) {
    return (
      <Card>
        <CardHeader title="Customer unavailable" subtitle={`#${customer_id}`} />
      </Card>
    );
  }
  if (!data) {
    return (
      <Card>
        <CardHeader title="Loading customer…" subtitle={`#${customer_id}`} />
      </Card>
    );
  }

  const c = data.customer;
  const currency = c.currency || "USD";
  const lt = data.lifetime || {};
  const openCount = data.open_invoices?.length || 0;

  const subtitle = (
    <span className="flex items-center gap-2 text-text-muted">
      <span>{c.email || "—"}</span>
      {c.phone && <span>· {c.phone}</span>}
    </span>
  );

  return (
    <Card>
      <CardHeader
        title={c.name}
        subtitle={subtitle}
        right={
          openCount > 0 ? (
            <span className="text-[11px] px-1.5 py-0.5 rounded bg-accent/15 text-accent">
              {openCount} open
            </span>
          ) : undefined
        }
      />
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
            label: "Outstanding",
            value: fmtMoney(Number(lt.outstanding_cents || 0), currency),
          },
          {
            label: "Invoices",
            value: String(lt.invoice_count || 0),
          },
        ]}
      />
    </Card>
  );
}
