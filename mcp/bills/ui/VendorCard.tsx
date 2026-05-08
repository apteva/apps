// VendorCard — bills' vendor chat-attachment. Mirrors billing's
// CustomerCard but shows AP-side metrics: lifetime spend +
// outstanding owed.

import { useEffect, useState } from "react";
import { Card, CardHeader, DataList } from "@apteva/ui-kit";

interface Vendor {
  id: number;
  name: string;
  email?: string;
  phone?: string;
  currency?: string;
  default_payment_method?: string;
  default_payment_terms_days?: number;
  w9_received_at?: string;
}

interface Lifetime {
  bill_count?: number;
  billed_cents?: number;
  paid_cents?: number;
  outstanding_cents?: number;
}

interface ContextPayload {
  vendor: Vendor;
  open_bills?: Array<{ id: number }>;
  lifetime?: Lifetime;
}

interface Props {
  vendor_id: number;
  projectId?: string;
  preview?: boolean;
}

const previewSample: ContextPayload = {
  vendor: {
    id: 0,
    name: "AWS",
    email: "billing@aws.amazon.com",
    currency: "USD",
    default_payment_method: "ach",
    default_payment_terms_days: 30,
    w9_received_at: "2025-01-15T00:00:00Z",
  },
  open_bills: [{ id: 1 }],
  lifetime: {
    bill_count: 12,
    billed_cents: 1850000,
    paid_cents: 1700000,
    outstanding_cents: 150000,
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

export default function VendorCard({ vendor_id, projectId, preview }: Props) {
  const [data, setData] = useState<ContextPayload | null>(
    preview ? previewSample : null,
  );
  const [missing, setMissing] = useState(false);

  const refetch = () => {
    if (preview) return;
    if (!projectId) return;
    const url =
      `/api/apps/bills/vendors/${vendor_id}/context` +
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
        if (j && j.vendor) {
          setData(j as ContextPayload);
          setMissing(false);
        }
      })
      .catch(() => {});
  };

  useEffect(refetch, [vendor_id, projectId, preview]);

  useBillsEvents(projectId, (ev) => {
    if (
      ev.data &&
      typeof ev.data.id === "number" &&
      ev.data.id === vendor_id
    ) {
      refetch();
    }
    if (ev.topic === "bill.paid" || ev.topic === "bill.added") {
      refetch();
    }
  });

  if (missing) {
    return (
      <Card>
        <CardHeader title="Vendor unavailable" subtitle={`#${vendor_id}`} />
      </Card>
    );
  }
  if (!data) {
    return (
      <Card>
        <CardHeader title="Loading vendor…" subtitle={`#${vendor_id}`} />
      </Card>
    );
  }

  const v = data.vendor;
  const currency = v.currency || "USD";
  const lt = data.lifetime || {};
  const openCount = data.open_bills?.length || 0;

  const subtitle = (
    <span className="flex items-center gap-2 text-text-muted">
      <span>{v.email || "—"}</span>
      {v.default_payment_terms_days && (
        <span>· Net {v.default_payment_terms_days}</span>
      )}
      {!v.w9_received_at && (
        <span className="text-[10px] px-1 rounded bg-yellow-500/15 text-yellow-500">
          no W-9
        </span>
      )}
    </span>
  );

  return (
    <Card>
      <CardHeader
        title={v.name}
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
            label: "Lifetime billed",
            value: fmtMoney(Number(lt.billed_cents || 0), currency),
          },
          {
            label: "Paid",
            value: fmtMoney(Number(lt.paid_cents || 0), currency),
          },
          {
            label: "Owed",
            value: fmtMoney(Number(lt.outstanding_cents || 0), currency),
          },
          {
            label: "Bills",
            value: String(lt.bill_count || 0),
          },
        ]}
      />
    </Card>
  );
}
