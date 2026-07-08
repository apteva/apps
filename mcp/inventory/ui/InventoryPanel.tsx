import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/inventory";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Summary {
  items: number;
  locations: number;
  on_hand: number;
  reserved: number;
  available: number;
  active_reservations: number;
}

interface Location {
  id: number;
  code: string;
  name: string;
  type: string;
  active: boolean;
}

interface Item {
  id: number;
  sku: string;
  name: string;
  barcode: string;
  unit: string;
  track_quantity: boolean;
  allow_backorder: boolean;
  archived: boolean;
  on_hand: number;
  reserved: number;
  available: number;
  location_count: number;
}

interface Level {
  id: number;
  item_id: number;
  sku: string;
  item_name: string;
  location_id: number;
  location_code: string;
  location_name: string;
  on_hand: number;
  reserved: number;
  available: number;
  safety_stock: number;
}

interface Reservation {
  id: number;
  sku: string;
  item_name: string;
  location_name: string;
  quantity: number;
  status: string;
  reference_app: string;
  reference_type: string;
  reference_id: string;
  created_at: string;
}

interface Movement {
  id: number;
  sku: string;
  item_name: string;
  location_name: string;
  type: string;
  quantity_delta: number;
  on_hand_after: number;
  reserved_after: number;
  reason: string;
  actor: string;
  created_at: string;
}

type View = "stock" | "reservations" | "movements";

export default function InventoryPanel({}: NativePanelProps) {
  const [view, setView] = useState<View>("stock");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [items, setItems] = useState<Item[]>([]);
  const [locations, setLocations] = useState<Location[]>([]);
  const [levels, setLevels] = useState<Level[]>([]);
  const [reservations, setReservations] = useState<Reservation[]>([]);
  const [movements, setMovements] = useState<Movement[]>([]);
  const [query, setQuery] = useState("");
  const [selectedItem, setSelectedItem] = useState<number | null>(null);
  const [status, setStatus] = useState("");
  const [itemForm, setItemForm] = useState({ sku: "", name: "", unit: "each", barcode: "" });
  const [locationForm, setLocationForm] = useState({ name: "", code: "", type: "warehouse" });
  const [stockForm, setStockForm] = useState({ item_id: "", location_id: "", quantity: "", reason: "receive" });

  const api = useCallback(async <T,>(path: string, init?: RequestInit): Promise<T> => {
    const res = await fetch(`${API}${path}`, {
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", ...(init?.headers || {}) },
      ...init,
    });
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      throw new Error(body.error || `Request failed: ${res.status}`);
    }
    return res.json();
  }, []);

  const load = useCallback(async () => {
    const qs = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : "";
    const [sum, itemRows, locRows, levelRows, resRows, movRows] = await Promise.all([
      api<Summary>("/summary"),
      api<Item[]>(`/items${qs}`),
      api<Location[]>("/locations?active=true"),
      api<Level[]>(selectedItem ? `/levels?item_id=${selectedItem}` : "/levels"),
      api<Reservation[]>("/reservations?status=active&limit=100"),
      api<Movement[]>("/movements?limit=100"),
    ]);
    setSummary(sum);
    setItems(itemRows || []);
    setLocations(locRows || []);
    setLevels(levelRows || []);
    setReservations(resRows || []);
    setMovements(movRows || []);
  }, [api, query, selectedItem]);

  useEffect(() => {
    load().catch((err) => setStatus(err.message));
  }, [load]);

  const totals = useMemo(() => summary || {
    items: items.length,
    locations: locations.length,
    on_hand: 0,
    reserved: 0,
    available: 0,
    active_reservations: reservations.length,
  }, [summary, items.length, locations.length, reservations.length]);

  async function createItem() {
    if (!itemForm.sku.trim() || !itemForm.name.trim()) return;
    await api<Item>("/items", { method: "POST", body: JSON.stringify(itemForm) });
    setItemForm({ sku: "", name: "", unit: "each", barcode: "" });
    setStatus("Item created");
    await load();
  }

  async function createLocation() {
    if (!locationForm.name.trim()) return;
    await api<Location>("/locations", { method: "POST", body: JSON.stringify(locationForm) });
    setLocationForm({ name: "", code: "", type: "warehouse" });
    setStatus("Location created");
    await load();
  }

  async function receiveStock() {
    const item_id = Number(stockForm.item_id);
    const location_id = Number(stockForm.location_id);
    const quantity = Number(stockForm.quantity);
    if (!item_id || !location_id || !quantity) return;
    await api("/receive", {
      method: "POST",
      body: JSON.stringify({ item_id, location_id, quantity, reason: stockForm.reason || "receive" }),
    });
    setStockForm({ ...stockForm, quantity: "" });
    setStatus("Stock received");
    await load();
  }

  async function releaseReservation(id: number) {
    await api(`/reservations/${id}/release`, { method: "POST", body: "{}" });
    setStatus(`Reservation #${id} released`);
    await load();
  }

  async function commitReservation(id: number) {
    await api(`/reservations/${id}/commit`, { method: "POST", body: "{}" });
    setStatus(`Reservation #${id} committed`);
    await load();
  }

  return (
    <div className="h-full bg-bg text-text flex flex-col">
      <header className="border-b border-border px-5 py-4 flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">Inventory</h1>
          <p className="text-sm text-text-muted">Reusable stock ledger for SKUs, locations, reservations, and movements.</p>
        </div>
        <div className="flex rounded-md border border-border overflow-hidden text-sm">
          {(["stock", "reservations", "movements"] as View[]).map((v) => (
            <button
              key={v}
              onClick={() => setView(v)}
              className={`px-3 py-1.5 capitalize ${view === v ? "bg-accent text-bg" : "hover:bg-bg-input/50"}`}
            >
              {v}
            </button>
          ))}
        </div>
      </header>

      <main className="flex-1 overflow-auto p-5 space-y-5">
        <section className="grid grid-cols-2 md:grid-cols-6 gap-3">
          <Metric label="Items" value={totals.items} />
          <Metric label="Locations" value={totals.locations} />
          <Metric label="On hand" value={fmtQty(totals.on_hand)} />
          <Metric label="Reserved" value={fmtQty(totals.reserved)} />
          <Metric label="Available" value={fmtQty(totals.available)} />
          <Metric label="Active reservations" value={totals.active_reservations} />
        </section>

        {status && <div className="text-xs text-text-muted">{status}</div>}

        {view === "stock" && (
          <div className="grid xl:grid-cols-[minmax(0,1fr)_360px] gap-5">
            <section className="space-y-4">
              <div className="flex gap-3">
                <input
                  className="w-full rounded-md bg-bg-input border border-border px-3 py-2 text-sm"
                  placeholder="Search SKU, name, or barcode"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                />
                <button className="rounded-md bg-accent text-bg px-4 py-2 text-sm" onClick={() => load()}>
                  Search
                </button>
              </div>

              <div className="overflow-hidden rounded-md border border-border">
                <table className="w-full text-sm">
                  <thead className="bg-bg-input text-text-muted">
                    <tr>
                      <Th>SKU</Th>
                      <Th>Name</Th>
                      <Th>On hand</Th>
                      <Th>Reserved</Th>
                      <Th>Available</Th>
                      <Th>Locations</Th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((item) => (
                      <tr
                        key={item.id}
                        onClick={() => setSelectedItem(item.id === selectedItem ? null : item.id)}
                        className={`border-t border-border cursor-pointer ${item.id === selectedItem ? "bg-accent/10" : "hover:bg-bg-input/40"}`}
                      >
                        <Td mono>{item.sku}</Td>
                        <Td>{item.name}</Td>
                        <Td>{fmtQty(item.on_hand)}</Td>
                        <Td>{fmtQty(item.reserved)}</Td>
                        <Td>{fmtQty(item.available)}</Td>
                        <Td>{item.location_count}</Td>
                      </tr>
                    ))}
                    {items.length === 0 && (
                      <tr><Td colSpan={6}>No inventory items.</Td></tr>
                    )}
                  </tbody>
                </table>
              </div>

              <div className="overflow-hidden rounded-md border border-border">
                <div className="px-3 py-2 bg-bg-input text-sm font-medium">Levels {selectedItem ? "for selected item" : ""}</div>
                <table className="w-full text-sm">
                  <thead className="text-text-muted">
                    <tr>
                      <Th>SKU</Th>
                      <Th>Location</Th>
                      <Th>On hand</Th>
                      <Th>Reserved</Th>
                      <Th>Available</Th>
                    </tr>
                  </thead>
                  <tbody>
                    {levels.map((l) => (
                      <tr key={l.id} className="border-t border-border">
                        <Td mono>{l.sku}</Td>
                        <Td>{l.location_name}</Td>
                        <Td>{fmtQty(l.on_hand)}</Td>
                        <Td>{fmtQty(l.reserved)}</Td>
                        <Td>{fmtQty(l.available)}</Td>
                      </tr>
                    ))}
                    {levels.length === 0 && (
                      <tr><Td colSpan={5}>No stock levels yet.</Td></tr>
                    )}
                  </tbody>
                </table>
              </div>
            </section>

            <aside className="space-y-4">
              <Panel title="Create item">
                <Input label="SKU" value={itemForm.sku} onChange={(sku) => setItemForm({ ...itemForm, sku })} />
                <Input label="Name" value={itemForm.name} onChange={(name) => setItemForm({ ...itemForm, name })} />
                <Input label="Unit" value={itemForm.unit} onChange={(unit) => setItemForm({ ...itemForm, unit })} />
                <Input label="Barcode" value={itemForm.barcode} onChange={(barcode) => setItemForm({ ...itemForm, barcode })} />
                <button className="w-full rounded-md bg-accent text-bg px-3 py-2 text-sm" onClick={() => createItem().catch((e) => setStatus(e.message))}>Create item</button>
              </Panel>

              <Panel title="Create location">
                <Input label="Name" value={locationForm.name} onChange={(name) => setLocationForm({ ...locationForm, name })} />
                <Input label="Code" value={locationForm.code} onChange={(code) => setLocationForm({ ...locationForm, code })} />
                <Select label="Type" value={locationForm.type} onChange={(type) => setLocationForm({ ...locationForm, type })} options={["warehouse", "store", "supplier", "virtual", "damaged"]} />
                <button className="w-full rounded-md bg-accent text-bg px-3 py-2 text-sm" onClick={() => createLocation().catch((e) => setStatus(e.message))}>Create location</button>
              </Panel>

              <Panel title="Receive stock">
                <Select label="Item" value={stockForm.item_id} onChange={(item_id) => setStockForm({ ...stockForm, item_id })} options={items.map((i) => ({ label: `${i.sku} - ${i.name}`, value: String(i.id) }))} />
                <Select label="Location" value={stockForm.location_id} onChange={(location_id) => setStockForm({ ...stockForm, location_id })} options={locations.map((l) => ({ label: l.name, value: String(l.id) }))} />
                <Input label="Quantity" value={stockForm.quantity} onChange={(quantity) => setStockForm({ ...stockForm, quantity })} />
                <Input label="Reason" value={stockForm.reason} onChange={(reason) => setStockForm({ ...stockForm, reason })} />
                <button className="w-full rounded-md bg-accent text-bg px-3 py-2 text-sm" onClick={() => receiveStock().catch((e) => setStatus(e.message))}>Receive</button>
              </Panel>
            </aside>
          </div>
        )}

        {view === "reservations" && (
          <div className="overflow-hidden rounded-md border border-border">
            <table className="w-full text-sm">
              <thead className="bg-bg-input text-text-muted">
                <tr>
                  <Th>ID</Th>
                  <Th>SKU</Th>
                  <Th>Location</Th>
                  <Th>Qty</Th>
                  <Th>Reference</Th>
                  <Th>Created</Th>
                  <Th>Actions</Th>
                </tr>
              </thead>
              <tbody>
                {reservations.map((r) => (
                  <tr key={r.id} className="border-t border-border">
                    <Td>#{r.id}</Td>
                    <Td mono>{r.sku}</Td>
                    <Td>{r.location_name}</Td>
                    <Td>{fmtQty(r.quantity)}</Td>
                    <Td>{[r.reference_app, r.reference_type, r.reference_id].filter(Boolean).join(" / ") || "-"}</Td>
                    <Td>{fmtDate(r.created_at)}</Td>
                    <Td>
                      <div className="flex gap-2">
                        <button className="rounded border border-border px-2 py-1" onClick={() => commitReservation(r.id).catch((e) => setStatus(e.message))}>Commit</button>
                        <button className="rounded border border-border px-2 py-1" onClick={() => releaseReservation(r.id).catch((e) => setStatus(e.message))}>Release</button>
                      </div>
                    </Td>
                  </tr>
                ))}
                {reservations.length === 0 && <tr><Td colSpan={7}>No active reservations.</Td></tr>}
              </tbody>
            </table>
          </div>
        )}

        {view === "movements" && (
          <div className="overflow-hidden rounded-md border border-border">
            <table className="w-full text-sm">
              <thead className="bg-bg-input text-text-muted">
                <tr>
                  <Th>ID</Th>
                  <Th>Type</Th>
                  <Th>SKU</Th>
                  <Th>Location</Th>
                  <Th>Delta</Th>
                  <Th>After</Th>
                  <Th>Reason</Th>
                  <Th>Created</Th>
                </tr>
              </thead>
              <tbody>
                {movements.map((m) => (
                  <tr key={m.id} className="border-t border-border">
                    <Td>#{m.id}</Td>
                    <Td>{m.type}</Td>
                    <Td mono>{m.sku}</Td>
                    <Td>{m.location_name}</Td>
                    <Td>{fmtQty(m.quantity_delta)}</Td>
                    <Td>{fmtQty(m.on_hand_after)} / {fmtQty(m.reserved_after)} reserved</Td>
                    <Td>{m.reason || "-"}</Td>
                    <Td>{fmtDate(m.created_at)}</Td>
                  </tr>
                ))}
                {movements.length === 0 && <tr><Td colSpan={8}>No movements yet.</Td></tr>}
              </tbody>
            </table>
          </div>
        )}
      </main>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="rounded-md border border-border bg-bg-surface px-3 py-2">
      <div className="text-xs text-text-muted">{label}</div>
      <div className="text-lg font-semibold">{value}</div>
    </div>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-bg-surface p-3 space-y-3">
      <h2 className="text-sm font-medium">{title}</h2>
      {children}
    </div>
  );
}

function Input({ label, value, onChange }: { label: string; value: string; onChange: (value: string) => void }) {
  return (
    <label className="block text-xs text-text-muted space-y-1">
      <span>{label}</span>
      <input className="w-full rounded-md bg-bg-input border border-border px-2 py-1.5 text-sm text-text" value={value} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}

function Select({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  options: Array<string | { label: string; value: string }>;
}) {
  return (
    <label className="block text-xs text-text-muted space-y-1">
      <span>{label}</span>
      <select className="w-full rounded-md bg-bg-input border border-border px-2 py-1.5 text-sm text-text" value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">Select</option>
        {options.map((opt) => {
          const o = typeof opt === "string" ? { label: opt, value: opt } : opt;
          return <option key={o.value} value={o.value}>{o.label}</option>;
        })}
      </select>
    </label>
  );
}

function Th({ children }: { children: React.ReactNode }) {
  return <th className="text-left font-medium px-3 py-2">{children}</th>;
}

function Td({ children, mono, colSpan }: { children: React.ReactNode; mono?: boolean; colSpan?: number }) {
  return <td colSpan={colSpan} className={`px-3 py-2 align-middle ${mono ? "font-mono text-xs" : ""}`}>{children}</td>;
}

function fmtQty(v: number) {
  if (!Number.isFinite(v)) return "0";
  return Math.round(v * 10000) / 10000;
}

function fmtDate(v: string) {
  if (!v) return "-";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return v;
  return d.toLocaleString();
}
