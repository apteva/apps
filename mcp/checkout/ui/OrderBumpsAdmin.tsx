import { useEffect, useState } from "react";

type Bump = {
  id?: number; title: string; description: string; image_url: string;
  trigger_price_id: number; trigger_product_id: number; price_id: number;
  source_app: string; context_ref: string; fulfillment_app: string; fulfillment_ref: string;
  active: boolean; sort_order: number; starts_at: string; ends_at: string;
  impressions?: number; selections?: number; paid_conversions?: number; revenue_cents?: number;
};
const blank: Bump = { title: "", description: "", image_url: "", trigger_price_id: 0, trigger_product_id: 0, price_id: 0, source_app: "", context_ref: "", fulfillment_app: "", fulfillment_ref: "", active: true, sort_order: 0, starts_at: "", ends_at: "" };

export function OrderBumpsAdmin({ apiCall }: { apiCall: <T>(method: string, path: string, body?: unknown) => Promise<T> }) {
  const [bumps, setBumps] = useState<Bump[]>([]);
  const [draft, setDraft] = useState<Bump>(blank);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const load = async () => { const result = await apiCall<{ bumps: Bump[] }>("GET", "/order-bumps"); setBumps(result.bumps); };
  useEffect(() => { void load().catch((e) => setError(e.message)); }, [apiCall]);
  async function save(e: React.FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    try { await apiCall("POST", "/order-bumps", draft); setDraft(blank); await load(); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  }
  return <div className="h-full overflow-auto p-4 space-y-4">
    <div><h2 className="font-semibold">Order bumps</h2><p className="text-sm text-text-dim">Offer optional products before payment. Prices come from Catalog; each item names the app responsible for delivery.</p></div>
    {error && <p role="alert" className="text-red">{error}</p>}
    <div className="flex flex-wrap gap-3">{bumps.map((b) => <button key={b.id} className="border border-border rounded p-3 text-left" onClick={() => setDraft(b)}>
      <strong>{b.title}</strong><p>{b.active ? "Enabled" : "Disabled"}</p>
      <small>{b.impressions || 0} views · {b.selections || 0} selections · {b.paid_conversions || 0} paid</small>
    </button>)}</div>
    <form onSubmit={save} className="grid grid-cols-2 gap-3 max-w-3xl">
      {([
        ["title", "Offer title"], ["description", "Description"], ["image_url", "Image URL (HTTPS)"],
        ["trigger_price_id", "Main Catalog price ID (or use product)"], ["trigger_product_id", "Main Catalog product ID (or use price)"],
        ["price_id", "Add-on Catalog price ID"], ["source_app", "Selling app (optional, e.g. community)"], ["context_ref", "Checkout context (optional, Community ID)"],
        ["fulfillment_app", "Delivery app (e.g. community)"], ["fulfillment_ref", "Delivery reference (e.g. course space ID)"],
        ["sort_order", "Display order"], ["starts_at", "Starts at (optional RFC3339)"], ["ends_at", "Ends at (optional RFC3339)"],
      ] as const).map(([key, label]) => <label key={key} className="text-sm flex flex-col gap-1">{label}<input className="bg-bg-input border border-border rounded p-2" value={draft[key]} type={typeof draft[key] === "number" ? "number" : "text"} min={0} onChange={(e) => setDraft({ ...draft, [key]: typeof draft[key] === "number" ? Number(e.target.value) : e.target.value })} /></label>)}
      <label><input type="checkbox" checked={draft.active} onChange={(e) => setDraft({ ...draft, active: e.target.checked })} /> Enabled</label>
      <div className="flex gap-3"><button disabled={busy} className="bg-accent text-bg rounded px-3 py-2">{busy ? "Saving…" : draft.id ? "Save offer" : "Create offer"}</button><button type="button" onClick={() => setDraft(blank)}>New offer</button></div>
    </form>
  </div>;
}
