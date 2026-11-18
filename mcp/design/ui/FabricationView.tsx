import { useCallback, useEffect, useState } from "react";

type Req = (path: string, init?: RequestInit) => Promise<any>;

interface Caps { slug: string; name: string; processes: string[]; auto_pay: boolean; cancel: boolean; settings?: string[]; payment_note: string }
interface Bound { connection_id: number; slug: string; supported: boolean; capabilities: Caps; allowed_by_policy: boolean }
interface Option { id: string; provider: string; connection_id: number; vendor?: string; process?: string; material: string; subtotal: number; shipping: number; shipping_name?: string; total: number; currency: string; lead_time_days?: number; checkout_url?: string }
interface Quote { id: number; design_id: number; revision_id: number; options: Option[]; errors: { provider: string; connection_id: number; error: string }[]; expires_at?: string; created_at: string }
interface Order { id: number; design_id: number; revision_id: number; provider: string; status: string; currency: string; quoted_total: number; draft_total: number; option: Option; policy: { reasons: string[]; auto_allowed: boolean }; approval?: any; provider_order_id?: string; provider_status?: string; tracking?: any; error?: string; created_by?: string; created_at: string; draft?: { note?: string } }
interface Address { id: string; label: string; first_name: string; last_name: string; company?: string; line1: string; line2?: string; city: string; state?: string; zip: string; country: string; phone: string; email: string }
interface Settings { auto_order: boolean; max_order_total: number; monthly_budget: number; currency: string; country: string; allowed_providers: string[]; addresses: Address[]; default_address_id: string; provider_settings: Record<string, Record<string, any>> }

const money = (n: number, c: string) => `${(n || 0).toFixed(2)} ${c}`;
const ACTIVE = ["awaiting_approval", "approved"];
const EMPTY_ADDRESS: Address = { id: "", label: "", first_name: "", last_name: "", line1: "", city: "", zip: "", country: "FR", phone: "", email: "" };

export default function FabricationView({ request, designId, revisionId }: { request: Req; designId?: number; revisionId?: number }) {
  const [providers, setProviders] = useState<Bound[]>([]);
  const [supported, setSupported] = useState<Caps[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [quotes, setQuotes] = useState<Quote[]>([]);
  const [orders, setOrders] = useState<Order[]>([]);
  const [quantity, setQuantity] = useState("1");
  const [addressId, setAddressId] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [view, setView] = useState<"quotes" | "orders" | "settings">("quotes");

  const load = useCallback(async () => {
    try {
      const [p, s, o] = await Promise.all([request("/fabrication/providers"), request("/fabrication/settings"), request(`/fabrication/orders${designId ? `?design_id=${designId}` : ""}`)]);
      setProviders(p.providers || []); setSupported(p.supported || []); setSettings(s); setOrders(o.orders || []);
      setAddressId((current) => current || s.default_address_id || s.addresses?.[0]?.id || "");
      if (designId) setQuotes((await request(`/fabrication/quotes?design_id=${designId}&limit=5`)).quotes || []);
    } catch (e) { setError((e as Error).message); }
  }, [request, designId]);
  useEffect(() => { load(); }, [load]);

  async function run(label: string, fn: () => Promise<any>) {
    setBusy(label); setError("");
    try { await fn(); await load(); } catch (e) { setError((e as Error).message); } finally { setBusy(""); }
  }
  const quote = () => run("Collecting quotes", () => request("/fabrication/quotes", { method: "POST", body: JSON.stringify({ design_id: designId, revision_id: revisionId, quantity: Number(quantity) || 1 }) }));
  const prepare = (q: Quote, o: Option) => run("Preparing order", async () => { await request("/fabrication/orders", { method: "POST", body: JSON.stringify({ quote_id: q.id, option_id: o.id, address_id: addressId }) }); setView("orders"); });
  const act = (o: Order, action: string, body?: any) => run(action, () => request(`/fabrication/orders/${o.id}/${action}`, { method: "POST", body: JSON.stringify(body || {}) }));
  const saveSettings = (next: Settings) => run("Saving settings", () => request("/fabrication/settings", { method: "PUT", body: JSON.stringify(next) }));

  const latest = quotes[0];
  const pending = orders.filter((o) => o.status === "awaiting_approval").length;

  return <div className="fab">
    <div className="fab-bar">
      <button className={view === "quotes" ? "tab-active" : "tab"} onClick={() => setView("quotes")}>Quotes</button>
      <button className={view === "orders" ? "tab-active" : "tab"} onClick={() => setView("orders")}>Orders{pending ? ` (${pending} to approve)` : ""}</button>
      <button className={view === "settings" ? "tab-active" : "tab"} onClick={() => setView("settings")}>Policy and addresses</button>
      <span className="flex-1" />
      {busy && <span className="text-[11px] text-text-muted">{busy}...</span>}
    </div>
    {error && <div className="fab-error">{error}</div>}
    <div className="fab-body">
      {view === "quotes" && <>
        <section className="fab-card">
          <h3 className="fab-h">Print services</h3>
          {providers.length ? <div className="fab-chips">{providers.map((p) => <span key={p.connection_id} className="fab-chip" data-ok={p.supported && p.allowed_by_policy}>{p.capabilities?.name || p.slug} · #{p.connection_id}{!p.supported ? " · no adapter" : !p.allowed_by_policy ? " · not allowed" : ""}</span>)}</div>
            : <p className="fab-muted">No print service is bound. Bind Craftcloud, Slant 3D, Shapeways, Treatstock or Sculpteo connections to the "3D printing services" role in this app's settings.</p>}
          <div className="fab-row">
            <label className="fab-muted">Quantity per part<input className="field fab-num" type="number" min={1} value={quantity} onChange={(e) => setQuantity(e.target.value)} /></label>
            <button className="design-button design-button-primary" disabled={!!busy || !designId || !providers.length} onClick={quote}>Get quotes for this revision</button>
          </div>
          <p className="fab-muted">Builds STL files for the printed parts and sends only those files to the services. Nothing is ordered.</p>
        </section>
        {latest && <section className="fab-card">
          <h3 className="fab-h">Quote #{latest.id} · revision {latest.revision_id}{latest.expires_at ? ` · valid until ${new Date(latest.expires_at).toLocaleString()}` : ""}</h3>
          {latest.errors?.map((e, i) => <p key={i} className="fab-warn">{e.provider} #{e.connection_id}: {e.error}</p>)}
          {latest.options?.length ? <>
            <div className="fab-row"><label className="fab-muted">Ship to<select className="field" value={addressId} onChange={(e) => setAddressId(e.target.value)}><option value="">Choose a saved address</option>{settings?.addresses?.map((a) => <option key={a.id} value={a.id}>{a.label || a.id} ({a.country})</option>)}</select></label></div>
            <table className="fab-table"><thead><tr><th>Option</th><th>Service</th><th>Material</th><th>Parts</th><th>Shipping</th><th>Total</th><th>Lead</th><th /></tr></thead>
              <tbody>{latest.options.map((o) => <tr key={o.id}><td className="font-mono">{o.id}</td><td>{o.provider}{o.vendor ? ` / ${o.vendor}` : ""}</td><td>{o.material}{o.process ? ` (${o.process})` : ""}</td><td>{money(o.subtotal, o.currency)}</td><td>{money(o.shipping, o.currency)}{o.shipping_name ? <div className="fab-muted">{o.shipping_name}</div> : null}</td><td className="font-mono">{money(o.total, o.currency)}</td><td>{o.lead_time_days ? `${o.lead_time_days} d` : "-"}</td>
                <td><button className="design-button" disabled={!!busy || !addressId} onClick={() => prepare(latest, o)}>Prepare order</button></td></tr>)}</tbody></table>
            {!settings?.addresses?.length && <p className="fab-muted">Add a shipping address under Policy and addresses before preparing an order.</p>}
          </> : <p className="fab-muted">No options returned.</p>}
        </section>}
      </>}

      {view === "orders" && <section className="fab-card">
        <h3 className="fab-h">Orders</h3>
        {!orders.length && <p className="fab-muted">No orders yet.</p>}
        {orders.map((o) => <div key={o.id} className="fab-order" data-status={o.status}>
          <div className="fab-row"><strong>Order #{o.id}</strong><span className="fab-status">{o.status.replace(/_/g, " ")}</span><span className="fab-muted">{o.provider}{o.option?.vendor ? ` / ${o.option.vendor}` : ""} · {o.option?.material} · design {o.design_id} r{o.revision_id}</span><span className="flex-1" /><span className="font-mono">{money(o.draft_total, o.currency)}</span></div>
          {o.draft_total !== o.quoted_total && <p className="fab-muted">Quoted {money(o.quoted_total, o.currency)}; provider confirmed {money(o.draft_total, o.currency)}.</p>}
          {o.status === "awaiting_approval" && <p className="fab-muted">Needs approval: {o.policy?.reasons?.join("; ")}</p>}
          {o.provider_order_id && <p className="fab-muted">Provider order {o.provider_order_id}{o.provider_status ? ` · ${o.provider_status}` : ""}</p>}
          {o.tracking?.payment_url && <p className="fab-muted">Payment pending at the provider: <a className="text-accent" href={o.tracking.payment_url} target="_blank" rel="noreferrer">open payment page</a></p>}
          {o.tracking?.note && <p className="fab-muted">{o.tracking.note}</p>}
          {(o.tracking?.tracking_number || o.tracking?.tracking_url) && <p className="fab-muted">Tracking: {o.tracking.carrier ? `${o.tracking.carrier} ` : ""}{o.tracking.tracking_url ? <a className="text-accent" href={o.tracking.tracking_url} target="_blank" rel="noreferrer">{o.tracking.tracking_number || "track parcel"}</a> : o.tracking.tracking_number}</p>}
          {o.error && <p className="fab-warn">{o.error}</p>}
          <div className="fab-row">
            {o.status === "awaiting_approval" && <>
              <button className="design-button design-button-primary" disabled={!!busy} onClick={() => act(o, "approve", { confirm_total: o.draft_total })}>Approve {money(o.draft_total, o.currency)}</button>
              <button className="design-button" disabled={!!busy} onClick={() => act(o, "decline")}>Decline</button></>}
            {o.status === "approved" && <button className="design-button design-button-primary" disabled={!!busy} onClick={() => act(o, "submit")}>Place order now</button>}
            {!ACTIVE.includes(o.status) && !["declined", "cancelled", "failed", "delivered"].includes(o.status) && <button className="design-button" disabled={!!busy} onClick={() => act(o, "refresh")}>Refresh status</button>}
            {["awaiting_approval", "approved", "submitted", "awaiting_payment"].includes(o.status) && <button className="design-button" disabled={!!busy} onClick={() => act(o, "cancel")}>Cancel</button>}
          </div>
        </div>)}
      </section>}

      {view === "settings" && settings && <SettingsForm settings={settings} supported={supported} busy={!!busy} onSave={saveSettings} />}
    </div>
    <style>{styles}</style>
  </div>;
}

function SettingsForm({ settings, supported, busy, onSave }: { settings: Settings; supported: Caps[]; busy: boolean; onSave: (s: Settings) => void }) {
  const [s, setS] = useState<Settings>(settings);
  const [draft, setDraft] = useState<Address>(EMPTY_ADDRESS);
  const [providerJSON, setProviderJSON] = useState(JSON.stringify(settings.provider_settings || {}, null, 2));
  const [jsonError, setJsonError] = useState("");
  useEffect(() => { setS(settings); setProviderJSON(JSON.stringify(settings.provider_settings || {}, null, 2)); }, [settings]);
  const set = (patch: Partial<Settings>) => setS((v) => ({ ...v, ...patch }));
  function save() {
    try { const ps = JSON.parse(providerJSON || "{}"); setJsonError(""); onSave({ ...s, provider_settings: ps }); } catch { setJsonError("Provider settings must be valid JSON."); }
  }
  function addAddress() {
    const id = draft.id || (draft.label || "address").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || `address-${s.addresses.length + 1}`;
    set({ addresses: [...(s.addresses || []).filter((a) => a.id !== id), { ...draft, id }], default_address_id: s.default_address_id || id });
    setDraft(EMPTY_ADDRESS);
  }
  const toggleProvider = (slug: string) => set({ allowed_providers: s.allowed_providers?.includes(slug) ? s.allowed_providers.filter((p) => p !== slug) : [...(s.allowed_providers || []), slug] });
  const field = (k: keyof Address, label: string) => <label className="fab-muted">{label}<input className="field" value={(draft[k] as string) || ""} onChange={(e) => setDraft({ ...draft, [k]: e.target.value })} /></label>;

  return <>
    <section className="fab-card">
      <h3 className="fab-h">Order policy</h3>
      <label className="fab-check"><input type="checkbox" checked={s.auto_order} onChange={(e) => set({ auto_order: e.target.checked })} /> Let agents place orders without approval when within these limits</label>
      <div className="fab-grid">
        <label className="fab-muted">Max per order<input className="field" type="number" min={0} value={s.max_order_total} onChange={(e) => set({ max_order_total: Number(e.target.value) })} /></label>
        <label className="fab-muted">Monthly budget<input className="field" type="number" min={0} value={s.monthly_budget} onChange={(e) => set({ monthly_budget: Number(e.target.value) })} /></label>
        <label className="fab-muted">Currency<select className="field" value={s.currency} onChange={(e) => set({ currency: e.target.value })}>{["EUR", "USD", "GBP", "CAD", "AUD", "CHF"].map((c) => <option key={c}>{c}</option>)}</select></label>
        <label className="fab-muted">Quote country<input className="field" maxLength={2} value={s.country} onChange={(e) => set({ country: e.target.value.toUpperCase() })} /></label>
      </div>
      <p className="fab-muted">Allowed services (none checked means all bound services are allowed):</p>
      <div className="fab-chips">{supported.map((c) => <label key={c.slug} className="fab-check"><input type="checkbox" checked={!!s.allowed_providers?.includes(c.slug)} onChange={() => toggleProvider(c.slug)} /> {c.name}</label>)}</div>
      <p className="fab-muted">Orders outside the limits, a price that rises more than 5% after quoting, or a different currency always wait for approval here.</p>
    </section>
    <section className="fab-card">
      <h3 className="fab-h">Shipping addresses</h3>
      {s.addresses?.map((a) => <div key={a.id} className="fab-row"><label className="fab-check"><input type="radio" name="default-address" checked={s.default_address_id === a.id} onChange={() => set({ default_address_id: a.id })} /> {a.label || a.id}</label><span className="fab-muted">{a.first_name} {a.last_name}, {a.line1}, {a.zip} {a.city}, {a.country}</span><span className="flex-1" /><button className="design-button" onClick={() => set({ addresses: s.addresses.filter((x) => x.id !== a.id), default_address_id: s.default_address_id === a.id ? "" : s.default_address_id })}>Remove</button></div>)}
      <div className="fab-grid">{field("label", "Label")}{field("first_name", "First name")}{field("last_name", "Last name")}{field("company", "Company")}{field("line1", "Address line 1")}{field("line2", "Address line 2")}{field("zip", "Postal code")}{field("city", "City")}{field("state", "State")}{field("country", "Country (2 letters)")}{field("phone", "Phone")}{field("email", "Email")}</div>
      <button className="design-button" disabled={!draft.line1 || !draft.city || !draft.country} onClick={addAddress}>Add address</button>
    </section>
    <section className="fab-card">
      <h3 className="fab-h">Service settings</h3>
      <p className="fab-muted">Per-service options keyed by slug. {supported.filter((c) => c.settings?.length).map((c) => `${c.slug}: ${c.settings!.join(", ")}`).join(" · ")}</p>
      <textarea className="field fab-json" spellCheck={false} value={providerJSON} onChange={(e) => setProviderJSON(e.target.value)} />
      {jsonError && <p className="fab-warn">{jsonError}</p>}
    </section>
    <div className="fab-row"><button className="design-button design-button-primary" disabled={busy} onClick={save}>Save policy and addresses</button></div>
  </>;
}

const styles = `
.fab{display:flex;flex-direction:column;flex:1;min-height:0}
.fab-bar{display:flex;align-items:center;gap:6px;padding:8px 12px;border-bottom:1px solid var(--color-border,#252a31)}
.fab-body{flex:1;min-height:0;overflow-y:auto;padding:12px;display:flex;flex-direction:column;gap:12px}
.fab-card{border:1px solid var(--color-border,#252a31);border-radius:8px;padding:12px;background:var(--color-bg,#090b0e)}
.fab-h{margin:0 0 10px;font-size:12px;font-weight:600}
.fab-muted{font-size:11px;color:var(--color-text-muted,#8b949e);display:flex;flex-direction:column;gap:4px}
.fab-warn{font-size:11px;color:#f87171;margin:4px 0}
.fab-error{margin:8px 12px 0;padding:8px;border-radius:6px;font-size:12px;color:#fecaca;background:rgba(239,68,68,.12);border:1px solid rgba(239,68,68,.4)}
.fab-row{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin:8px 0}
.fab-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:8px;margin:8px 0}
.fab-chips{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:8px}
.fab-chip{font-size:11px;padding:3px 8px;border-radius:999px;border:1px solid var(--color-border,#30363d);color:var(--color-text-muted,#8b949e)}.fab-chip[data-ok="true"]{color:var(--color-text,#e8edf2);border-color:color-mix(in srgb,var(--color-accent,#f97316) 50%,transparent)}
.fab-check{font-size:12px;display:flex;align-items:center;gap:6px}
.fab-num{width:80px}
.fab-json{min-height:90px;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:11px}
.fab-table{width:100%;border-collapse:collapse;font-size:11px}.fab-table th{text-align:left;font-weight:500;color:var(--color-text-muted,#8b949e);padding:6px 4px;border-bottom:1px solid var(--color-border,#252a31)}.fab-table td{padding:6px 4px;border-bottom:1px solid var(--color-border,#1b2027);vertical-align:top}
.fab-order{border-top:1px solid var(--color-border,#252a31);padding:8px 0;font-size:12px}.fab-order:first-of-type{border-top:0}
.fab-status{font-size:10px;text-transform:uppercase;letter-spacing:.08em;padding:2px 6px;border-radius:4px;background:var(--color-bg-input,#151a21)}
.fab-order[data-status="awaiting_approval"] .fab-status{color:var(--color-accent,#f97316)}.fab-order[data-status="submit_unknown"] .fab-status,.fab-order[data-status="failed"] .fab-status{color:#f87171}
`;
