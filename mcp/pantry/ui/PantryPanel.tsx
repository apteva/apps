import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/pantry";

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
}

interface Item {
  id: number;
  name: string;
  category: string;
  brand: string;
  default_unit: string;
  min_quantity: number;
  target_quantity: number;
  total_quantity: number;
  lot_count: number;
  next_expires_at?: string;
}

interface Lot {
  id: number;
  item_id: number;
  item_name: string;
  category: string;
  location_id: number;
  location_name: string;
  location_kind: string;
  quantity: number;
  unit: string;
  expires_at?: string;
  notes: string;
}

interface Location {
  id: number;
  name: string;
  kind: string;
}

interface ShoppingLine {
  item_id: number;
  name: string;
  category: string;
  unit: string;
  current_quantity: number;
  min_quantity: number;
  target_quantity: number;
  buy_quantity: number;
}

type View = "stock" | "expiring" | "shopping";

export default function PantryPanel({}: NativePanelProps) {
  const [view, setView] = useState<View>("stock");
  const [items, setItems] = useState<Item[]>([]);
  const [lots, setLots] = useState<Lot[]>([]);
  const [expiring, setExpiring] = useState<Lot[]>([]);
  const [shopping, setShopping] = useState<ShoppingLine[]>([]);
  const [locations, setLocations] = useState<Location[]>([]);
  const [selectedItem, setSelectedItem] = useState<number | null>(null);
  const [quickText, setQuickText] = useState("");
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [form, setForm] = useState({
    name: "",
    quantity: "1",
    unit: "each",
    location: "Pantry",
    expires_at: "",
    category: "",
    min_quantity: "",
    target_quantity: "",
  });

  const loadItems = useCallback(async () => {
    const qs = query.trim() ? `?q=${encodeURIComponent(query.trim())}` : "";
    const res = await fetch(`${API}/items${qs}`, { credentials: "same-origin" });
    if (res.ok) setItems(await res.json() || []);
  }, [query]);

  const loadLots = useCallback(async () => {
    const qs = selectedItem ? `?item_id=${selectedItem}` : "";
    const res = await fetch(`${API}/lots${qs}`, { credentials: "same-origin" });
    if (res.ok) setLots(await res.json() || []);
  }, [selectedItem]);

  const loadExpiring = useCallback(async () => {
    const res = await fetch(`${API}/expiring?days=14&limit=100`, { credentials: "same-origin" });
    if (res.ok) setExpiring(await res.json() || []);
  }, []);

  const loadShopping = useCallback(async () => {
    const res = await fetch(`${API}/shopping_list`, { credentials: "same-origin" });
    if (res.ok) setShopping(await res.json() || []);
  }, []);

  const loadLocations = useCallback(async () => {
    const res = await fetch(`${API}/locations`, { credentials: "same-origin" });
    if (res.ok) setLocations(await res.json() || []);
  }, []);

  const refresh = useCallback(() => {
    loadItems();
    loadLots();
    loadExpiring();
    loadShopping();
    loadLocations();
  }, [loadItems, loadLots, loadExpiring, loadShopping, loadLocations]);

  useEffect(() => { refresh(); }, [refresh]);

  const totals = useMemo(() => {
    const itemCount = items.length;
    const lotCount = items.reduce((sum, i) => sum + i.lot_count, 0);
    const soonCount = expiring.length;
    const shoppingCount = shopping.length;
    return { itemCount, lotCount, soonCount, shoppingCount };
  }, [items, expiring, shopping]);

  const selected = useMemo(
    () => items.find((item) => item.id === selectedItem) || null,
    [items, selectedItem],
  );

  const submitQuick = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!quickText.trim()) return;
    setStatus("");
    const res = await fetch(`${API}/quick`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: quickText, source: "human" }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    setQuickText("");
    setStatus("Saved");
    refresh();
  };

  const submitStock = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name.trim()) return;
    const body = {
      name: form.name,
      quantity: Number(form.quantity || "1"),
      unit: form.unit || "each",
      location: form.location || "Pantry",
      expires_at: form.expires_at,
      category: form.category,
      min_quantity: Number(form.min_quantity || "0"),
      target_quantity: Number(form.target_quantity || "0"),
    };
    const res = await fetch(`${API}/stock/add`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    setForm({ ...form, name: "", quantity: "1", expires_at: "" });
    setStatus("Added");
    refresh();
  };

  const useLot = async (lot: Lot, action: "use" | "discard") => {
    const res = await fetch(`${API}/stock/use`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ item_id: lot.item_id, quantity: 1, location_id: lot.location_id, action }),
    });
    if (!res.ok) {
      setStatus(await res.text());
      return;
    }
    setStatus(action === "use" ? "Used" : "Discarded");
    refresh();
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <div className="border-b border-border px-4 py-3 flex flex-wrap items-center gap-2">
        <form onSubmit={submitQuick} className="flex min-w-[280px] flex-1 gap-2">
          <input
            value={quickText}
            onChange={(e) => setQuickText(e.target.value)}
            placeholder="add 2 milk fridge exp 2026-07-12"
            className="min-w-0 flex-1 bg-bg-input border border-border rounded px-3 py-1.5 text-sm"
          />
          <button className="bg-accent text-bg px-3 py-1.5 rounded text-sm" type="submit">Add</button>
        </form>
        <span className="text-xs text-text-dim min-w-16">{status}</span>
      </div>

      <div className="border-b border-border px-4 py-2 grid grid-cols-4 gap-2 text-sm">
        <Metric label="Items" value={totals.itemCount} />
        <Metric label="Lots" value={totals.lotCount} />
        <Metric label="Expiring" value={totals.soonCount} />
        <Metric label="Buy" value={totals.shoppingCount} />
      </div>

      <div className="flex-1 flex overflow-hidden">
        <aside className="w-72 border-r border-border flex flex-col overflow-hidden">
          <div className="p-3 border-b border-border flex gap-2">
            <input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search"
              className="min-w-0 flex-1 bg-bg-input border border-border rounded px-2 py-1 text-sm"
            />
          </div>
          <div className="flex-1 overflow-auto p-2">
            {items.map((item) => (
              <button
                key={item.id}
                onClick={() => { setSelectedItem(item.id); setView("stock"); }}
                className={`w-full text-left rounded px-2 py-2 mb-1 ${selectedItem === item.id ? "bg-bg-card" : "hover:bg-bg-card/60"}`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium truncate">{item.name}</span>
                  <span className="text-xs text-text-dim shrink-0">{fmtQty(item.total_quantity)} {item.default_unit}</span>
                </div>
                <div className="flex items-center justify-between text-xs text-text-dim">
                  <span className="truncate">{item.category || item.brand || "uncategorized"}</span>
                  <span>{item.next_expires_at || ""}</span>
                </div>
              </button>
            ))}
          </div>
        </aside>

        <main className="flex-1 overflow-auto">
          <div className="border-b border-border px-4 py-2 flex items-center gap-2">
            <button onClick={() => setView("stock")} className={tabClass(view === "stock")}>Stock</button>
            <button onClick={() => setView("expiring")} className={tabClass(view === "expiring")}>Expiring</button>
            <button onClick={() => setView("shopping")} className={tabClass(view === "shopping")}>Shopping</button>
          </div>

          {view === "stock" && (
            <div className="grid lg:grid-cols-[1fr_320px] gap-4 p-4">
              <section>
                <div className="flex items-center gap-2 mb-3">
                  <h3 className="text-base font-semibold">{selected ? selected.name : "All stock"}</h3>
                  <span className="text-xs text-text-dim">{lots.length} lots</span>
                </div>
                <LotList lots={lots} onUse={useLot} />
              </section>

              <form onSubmit={submitStock} className="border border-border rounded p-3 flex flex-col gap-2 h-fit">
                <h3 className="text-sm font-semibold">Add Stock</h3>
                <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Item" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                <div className="grid grid-cols-2 gap-2">
                  <input value={form.quantity} onChange={(e) => setForm({ ...form, quantity: e.target.value })} placeholder="Qty" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                  <input value={form.unit} onChange={(e) => setForm({ ...form, unit: e.target.value })} placeholder="Unit" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                </div>
                <select value={form.location} onChange={(e) => setForm({ ...form, location: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm">
                  {locations.map((l) => <option key={l.id} value={l.name}>{l.name}</option>)}
                </select>
                <input type="date" value={form.expires_at} onChange={(e) => setForm({ ...form, expires_at: e.target.value })} className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                <input value={form.category} onChange={(e) => setForm({ ...form, category: e.target.value })} placeholder="Category" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                <div className="grid grid-cols-2 gap-2">
                  <input value={form.min_quantity} onChange={(e) => setForm({ ...form, min_quantity: e.target.value })} placeholder="Min" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                  <input value={form.target_quantity} onChange={(e) => setForm({ ...form, target_quantity: e.target.value })} placeholder="Target" className="bg-bg-input border border-border rounded px-2 py-1.5 text-sm" />
                </div>
                <button className="bg-accent text-bg rounded px-3 py-1.5 text-sm" type="submit">Save</button>
              </form>
            </div>
          )}

          {view === "expiring" && (
            <div className="p-4">
              <h3 className="text-base font-semibold mb-3">Next 14 Days</h3>
              <LotList lots={expiring} onUse={useLot} />
            </div>
          )}

          {view === "shopping" && (
            <div className="p-4">
              <h3 className="text-base font-semibold mb-3">Shopping List</h3>
              <div className="border border-border rounded overflow-hidden">
                {shopping.length === 0 ? (
                  <div className="p-4 text-sm text-text-muted">Nothing to buy from current thresholds.</div>
                ) : shopping.map((line) => (
                  <div key={line.item_id} className="grid grid-cols-[1fr_120px_120px] gap-3 px-3 py-2 border-b border-border last:border-b-0 text-sm">
                    <div>
                      <div className="font-medium">{line.name}</div>
                      <div className="text-xs text-text-dim">{line.category || "uncategorized"}</div>
                    </div>
                    <div className="text-text-muted">Have {fmtQty(line.current_quantity)} {line.unit}</div>
                    <div className="text-text">Buy {fmtQty(line.buy_quantity)} {line.unit}</div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </main>
      </div>
    </div>
  );
}

function LotList({ lots, onUse }: { lots: Lot[]; onUse: (lot: Lot, action: "use" | "discard") => void }) {
  if (lots.length === 0) {
    return <div className="border border-dashed border-border rounded p-4 text-sm text-text-muted">No stock lots.</div>;
  }
  return (
    <div className="border border-border rounded overflow-hidden">
      {lots.map((lot) => (
        <div key={lot.id} className="grid grid-cols-[1fr_120px_120px_90px] gap-3 px-3 py-2 border-b border-border last:border-b-0 items-center text-sm">
          <div className="min-w-0">
            <div className="font-medium truncate">{lot.item_name}</div>
            <div className="text-xs text-text-dim truncate">{lot.location_name}{lot.category ? ` / ${lot.category}` : ""}</div>
          </div>
          <div className="text-text-muted">{fmtQty(lot.quantity)} {lot.unit}</div>
          <div className={expiryClass(lot.expires_at)}>{lot.expires_at || "No date"}</div>
          <div className="flex justify-end gap-1">
            <button onClick={() => onUse(lot, "use")} className="border border-border rounded px-2 py-1 text-xs">Use</button>
            <button onClick={() => onUse(lot, "discard")} className="border border-border rounded px-2 py-1 text-xs text-error">Drop</button>
          </div>
        </div>
      ))}
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="border border-border rounded px-3 py-2">
      <div className="text-lg font-semibold">{value}</div>
      <div className="text-xs text-text-dim">{label}</div>
    </div>
  );
}

function tabClass(active: boolean) {
  return `px-3 py-1.5 rounded text-sm ${active ? "bg-bg-card text-text" : "text-text-muted hover:text-text"}`;
}

function fmtQty(n: number) {
  if (!Number.isFinite(n)) return "0";
  if (Math.abs(n - Math.round(n)) < 0.001) return String(Math.round(n));
  return n.toFixed(1);
}

function expiryClass(date?: string) {
  if (!date) return "text-text-dim text-xs";
  const days = Math.ceil((new Date(date).getTime() - Date.now()) / 86400000);
  if (days < 0) return "text-error text-xs";
  if (days <= 3) return "text-warn text-xs";
  return "text-text-muted text-xs";
}
