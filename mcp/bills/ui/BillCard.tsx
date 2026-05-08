// BillCard — bills' chat-attachment component. Mirrors billing's
// InvoiceCard. Status pill colours align with the panel's tone map.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList } from "@apteva/ui-kit";

interface BillMeta {
  id: number;
  vendor_id: number;
  vendor_invoice_number?: string;
  status: "received" | "approved" | "scheduled" | "paid" | "disputed" | "void";
  provider: "local" | "mercury" | "wise" | "bill_dot_com";
  currency: string;
  total_cents: number;
  amount_paid_cents: number;
  due_date?: string;
  category?: string;
}

interface Props {
  bill_id: number;
  projectId?: string;
  preview?: boolean;
}

const previewSample: BillMeta = {
  id: 0,
  vendor_id: 0,
  vendor_invoice_number: "VENDOR-INV-2026-0123",
  status: "approved",
  provider: "local",
  currency: "USD",
  total_cents: 280000,
  amount_paid_cents: 0,
  due_date: new Date(Date.now() + 21 * 86400_000).toISOString(),
  category: "software",
};

const STATUS_TONE: Record<BillMeta["status"], string> = {
  received: "bg-yellow-500/15 text-yellow-500",
  approved: "bg-accent/15 text-accent",
  scheduled: "bg-cyan-500/15 text-cyan-500",
  paid: "bg-green-500/15 text-green-500",
  disputed: "bg-orange-500/15 text-orange-500",
  void: "bg-text-dim/15 text-text-dim line-through",
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

function useBillsEvents(
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
          return bridge.subscribe("bills", projectId, onEvent as any);
        }
            const url = `/api/app-events/bills?project_id=${encodeURIComponent(projectId)}`;
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

export default function BillCard({ bill_id, projectId, preview }: Props) {
  const [meta, setMeta] = useState<BillMeta | null>(
    preview ? previewSample : null,
  );
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    if (preview) return;
    if (!projectId) return;
    const url =
      `/api/apps/bills/bills/${bill_id}` +
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
        if (j && j.bill) {
          setMeta(j.bill as BillMeta);
          setMissing(false);
        }
      })
      .catch(() => {});
  };

  useEffect(refetch, [bill_id, projectId, preview]);

  useBillsEvents(projectId, (ev) => {
    if (
      ev.data &&
      typeof ev.data.id === "number" &&
      ev.data.id === bill_id
    ) {
      refetch();
    }
  });

  if (missing) {
    return (
      <Card>
        <CardHeader title="Bill unavailable" subtitle={`#${bill_id}`} />
      </Card>
    );
  }
  if (!meta) {
    return (
      <Card>
        <CardHeader title="Loading bill…" subtitle={`#${bill_id}`} />
      </Card>
    );
  }

  const remaining = meta.total_cents - meta.amount_paid_cents;
  const title = meta.vendor_invoice_number || `Bill #${meta.id}`;
  const subtitle = (
    <span className="flex items-center gap-2">
      <span className={`text-[10px] px-1.5 py-0.5 rounded ${STATUS_TONE[meta.status]}`}>
        {meta.status}
      </span>
      <span className="text-[10px] uppercase text-text-dim">{meta.provider}</span>
      {meta.due_date && (
        <span className="text-[11px] text-text-muted">due {fmtDate(meta.due_date)}</span>
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
                {fmtMoney(Math.max(0, remaining), meta.currency)} owed
              </div>
            )}
          </div>
        }
      />
      <DataList
        items={[
          { label: "Vendor", value: `#${meta.vendor_id}` },
          { label: "Currency", value: meta.currency },
          {
            label: "Paid",
            value: fmtMoney(meta.amount_paid_cents, meta.currency),
          },
          ...(meta.category
            ? [{ label: "Category", value: meta.category }]
            : []),
        ]}
      />
    </Card>
  );
}
