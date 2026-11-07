import { useEffect, useState } from "react";

type Receipt = { unallocated_refund_cents: number; items: Array<{ id: number; description: string; net_cents: number; refunded_cents: number; currency: string; status: string }> };
export function CheckoutReceipt({ sessionId, apiCall }: { sessionId: number; apiCall: <T>(method: string, path: string, body?: unknown) => Promise<T> }) {
  const [receipt, setReceipt] = useState<Receipt>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = async () => setReceipt(await apiCall<Receipt>("GET", `/sessions/${sessionId}/receipt`));
  useEffect(() => { setReceipt(undefined); void load().catch((e) => setError(e.message)); }, [sessionId, apiCall]);
  async function refund(item: Receipt["items"][number]) {
    if (!confirm(`Refund the remaining balance for ${item.description}?`)) return;
    setBusy(true); setError("");
    try {
      await apiCall("POST", `/sessions/${sessionId}/refund`, { item_id: item.id, idempotency_key: `panel:${sessionId}:${item.id}:${item.refunded_cents}` });
      await load();
    } catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <section className="mt-4 border-t border-border pt-4 space-y-3">
    <h3 className="font-semibold">Purchased items</h3>
    {error && <p role="alert" className="text-red">{error}</p>}
    {!!receipt?.unallocated_refund_cents && <p role="alert">This invoice includes an external partial refund that has no item allocation. Review it before issuing more refunds.</p>}
    {receipt?.items.map((item) => <div key={item.id} className="flex items-center justify-between gap-3 text-sm">
      <div><strong>{item.description}</strong><p>{new Intl.NumberFormat(undefined, { style: "currency", currency: item.currency }).format(item.net_cents / 100)} · {item.status.replaceAll("_", " ")}</p></div>
      {(item.status === "paid" || item.status === "partially_refunded") && <button className="border border-border rounded px-3 py-1" disabled={busy || !!receipt.unallocated_refund_cents} onClick={() => void refund(item)}>Refund this item</button>}
    </div>)}
    <button className="text-sm underline" onClick={() => void load().catch((e) => setError(e.message))}>Refresh payment status</button>
  </section>;
}
