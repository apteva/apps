// InvoiceCard — billing's chat-attachment component. The agent
// emits respond(components=[{app:"billing", name:"invoice-card",
// props:{invoice_id:N}}]) and the dashboard renders this under
// the message bubble. Composes the same ui-kit primitives every
// other chat card uses, so the look is consistent.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList } from "@apteva/ui-kit";

interface InvoiceMeta {
  id: number;
  customer_id: number;
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
  /** Injected by the host — scopes the metadata fetch + event sub. */
  projectId?: string;
  /** When true, render synthetic sample data — used by the
   *  dashboard's app-detail preview before any real invoices exist. */
  preview?: boolean;
}

const previewSample: InvoiceMeta = {
  id: 0,
  customer_id: 0,
  number: "INV-2026-0042",
  status: "open",
  provider: "local",
  currency: "USD",
  total_cents: 120000,
  amount_paid_cents: 0,
  due_date: new Date(Date.now() + 14 * 86400_000).toISOString(),
};

const STATUS_TONE: Record<InvoiceMeta["status"], string> = {
  draft: "bg-border text-text-muted",
  open: "bg-accent/15 text-accent",
  paid: "bg-green-500/15 text-green-500",
  void: "bg-text-dim/15 text-text-dim line-through",
  uncollectible: "bg-yellow-500/15 text-yellow-500",
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

function fmtDate(s?: string): string {
  if (!s) return "";
  try {
    return new Date(s).toLocaleDateString();
  } catch {
    return s;
  }
}

// Inlined SSE subscription. See storage's FileCard for the rationale.
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
        /* show stale meta rather than blank — the card is for context */
      });
  };

  useEffect(refetch, [invoice_id, projectId, preview]);

  useBillingEvents(projectId, (ev) => {
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
        <CardHeader title="Invoice unavailable" subtitle={`#${invoice_id}`} />
      </Card>
    );
  }
  if (!meta) {
    return (
      <Card>
        <CardHeader title="Loading invoice…" subtitle={`#${invoice_id}`} />
      </Card>
    );
  }

  const remaining = meta.total_cents - meta.amount_paid_cents;
  const title = meta.number || `Draft invoice #${meta.id}`;
  const subtitle = (
    <span className="flex items-center gap-2">
      <span
        className={`text-[10px] px-1.5 py-0.5 rounded ${
          STATUS_TONE[meta.status]
        }`}
      >
        {meta.status}
      </span>
      <span className="text-[10px] uppercase text-text-dim">{meta.provider}</span>
      {meta.due_date && (
        <span className="text-[11px] text-text-muted">
          due {fmtDate(meta.due_date)}
        </span>
      )}
    </span>
  );

  return (
    <Card>
      <CardHeader
        title={title}
        subtitle={subtitle}
        right={
          <div className="text-right">
            <div className="text-sm text-text font-medium">
              {fmtMoney(meta.total_cents, meta.currency)}
            </div>
            {meta.amount_paid_cents > 0 && meta.status !== "paid" && (
              <div className="text-[11px] text-text-muted">
                {fmtMoney(Math.max(0, remaining), meta.currency)} outstanding
              </div>
            )}
          </div>
        }
      />
      <DataList
        items={[
          { label: "Customer", value: `#${meta.customer_id}` },
          { label: "Currency", value: meta.currency },
          {
            label: "Paid",
            value: fmtMoney(meta.amount_paid_cents, meta.currency),
          },
        ]}
      />
      {meta.external_url && (
        <a
          href={meta.external_url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-accent text-sm underline"
        >
          Open hosted invoice →
        </a>
      )}
    </Card>
  );
}
