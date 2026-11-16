import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties, ReactNode, PointerEvent as RPointerEvent, WheelEvent as RWheelEvent } from "react";

// Places panel: filterable list beside an OpenStreetMap tile map, a
// detail drawer with visits and lists, quick add with lookup, import.
//
// Styling is inline with the dashboard's CSS variables because the
// dashboard's Tailwind build doesn't scan app panels.

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

type Status = "to_try" | "visited" | "favorite" | "closed";

interface Place {
  id: number;
  name: string;
  kind: string;
  status: Status;
  lat: number | null;
  lng: number | null;
  address: string;
  city: string;
  country: string;
  google_place_id?: string;
  google_maps_url?: string;
  website?: string;
  phone?: string;
  price_level: number | null;
  google_rating: number | null;
  my_rating: number | null;
  source_url?: string;
  notes: string;
  tags: string[];
  visit_count: number;
  last_visited_at?: string;
  updated_at: string;
  list_ids: number[];
}

interface Visit {
  id: number;
  visited_on: string;
  with_whom: string;
  dishes: string[];
  spend: number | null;
  currency: string;
  rating: number | null;
  notes: string;
  details: Record<string, unknown>;
}

interface PlaceList {
  id: number;
  name: string;
  place_count: number;
}

interface Candidate {
  source: string;
  name: string;
  kind: string;
  lat: number | null;
  lng: number | null;
  address: string;
  city: string;
  country: string;
  google_place_id?: string;
  osm_id?: string;
  google_maps_url?: string;
  website?: string;
  phone?: string;
  price_level?: number | null;
  google_rating?: number | null;
  saved_place_id?: number;
}

interface SettingsData {
  settings: { places_connection_id: number; home_lat: number | null; home_lng: number | null; home_label: string };
  lookup_source: string;
  kinds: string[];
}

const API = "/api/apps/places";
const STATUSES: { id: Status; label: string; color: string }[] = [
  { id: "to_try", label: "To try", color: "var(--color-blue, #3b82f6)" },
  { id: "visited", label: "Visited", color: "var(--color-green, #22c55e)" },
  { id: "favorite", label: "Favourite", color: "var(--color-yellow, #eab308)" },
  { id: "closed", label: "Closed", color: "var(--color-text-dim, #888)" },
];
const statusColor = (s: string) => STATUSES.find((x) => x.id === s)?.color ?? "var(--color-text-muted)";
const statusLabel = (s: string) => STATUSES.find((x) => x.id === s)?.label ?? s;

// ─── styles ─────────────────────────────────────────────────────────

const c = {
  bg: "var(--color-bg)",
  card: "var(--color-bg-card, var(--color-bg))",
  input: "var(--color-bg-input)",
  hover: "var(--color-bg-hover, var(--color-bg-input))",
  border: "var(--color-border)",
  text: "var(--color-text)",
  muted: "var(--color-text-muted)",
  dim: "var(--color-text-dim, var(--color-text-muted))",
  accent: "var(--color-accent)",
  error: "var(--color-error, #ef4444)",
};
const s = {
  input: { background: c.input, border: `1px solid ${c.border}`, borderRadius: 6, padding: "6px 8px", fontSize: 13, color: c.text, outline: "none", minWidth: 0 } as CSSProperties,
  btn: { background: "transparent", border: `1px solid ${c.border}`, borderRadius: 6, padding: "6px 10px", fontSize: 12, color: c.text, cursor: "pointer", whiteSpace: "nowrap", display: "inline-flex", alignItems: "center", gap: 6 } as CSSProperties,
  primary: { background: c.accent, border: `1px solid ${c.accent}`, borderRadius: 6, padding: "6px 12px", fontSize: 12, color: c.bg, cursor: "pointer", fontWeight: 600, whiteSpace: "nowrap" } as CSSProperties,
  label: { fontSize: 11, color: c.muted, display: "flex", flexDirection: "column", gap: 4 } as CSSProperties,
  chip: (on: boolean): CSSProperties => ({
    border: `1px solid ${on ? c.accent : c.border}`, background: on ? c.input : "transparent", color: on ? c.text : c.muted,
    borderRadius: 999, padding: "3px 10px", fontSize: 12, cursor: "pointer", whiteSpace: "nowrap",
  }),
  tag: { fontSize: 11, padding: "1px 6px", borderRadius: 4, border: `1px solid ${c.border}`, color: c.muted } as CSSProperties,
};

// ─── icons (Lucide geometry, ISC) ───────────────────────────────────

function Icon({ d, size = 14 }: { d: ReactNode; size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {d}
    </svg>
  );
}
const icons = {
  pin: <><path d="M20 10c0 4.993-5.539 10.193-7.399 11.799a1 1 0 0 1-1.202 0C9.539 20.193 4 14.993 4 10a8 8 0 0 1 16 0" /><circle cx="12" cy="10" r="3" /></>,
  plus: <><path d="M5 12h14" /><path d="M12 5v14" /></>,
  minus: <path d="M5 12h14" />,
  x: <><path d="M18 6 6 18" /><path d="m6 6 12 12" /></>,
  upload: <><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><path d="m17 8-5-5-5 5" /><path d="M12 3v12" /></>,
  download: <><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" /><path d="m7 10 5 5 5-5" /><path d="M12 15V3" /></>,
  settings: <><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" /><circle cx="12" cy="12" r="3" /></>,
  external: <><path d="M15 3h6v6" /><path d="M10 14 21 3" /><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" /></>,
  locate: <><circle cx="12" cy="12" r="3" /><path d="M12 2v3" /><path d="M12 19v3" /><path d="M2 12h3" /><path d="M19 12h3" /></>,
  search: <><circle cx="11" cy="11" r="8" /><path d="m21 21-4.3-4.3" /></>,
};

// ─── API ────────────────────────────────────────────────────────────

function useApi(projectId: string) {
  return useCallback(async <T,>(tool: string, args: Record<string, unknown> = {}): Promise<T> => {
    const res = await fetch(`${API}/call/${tool}?project_id=${encodeURIComponent(projectId)}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(args),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || `${tool} failed (${res.status})`);
    return res.json();
  }, [projectId]);
}

// ─── panel ──────────────────────────────────────────────────────────

export default function PlacesPanel({ projectId }: NativePanelProps) {
  const call = useApi(projectId);
  const [places, setPlaces] = useState<Place[]>([]);
  const [lists, setLists] = useState<PlaceList[]>([]);
  const [settings, setSettings] = useState<SettingsData | null>(null);
  const [selectedId, setSelectedId] = useState(0);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<string>("active");
  const [kind, setKind] = useState("");
  const [listId, setListId] = useState(0);
  const [tag, setTag] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [dialog, setDialog] = useState<"" | "import" | "settings">("");
  const [fitKey, setFitKey] = useState(0);
  // Fit the map to the results of the next load (first load, filter changes, imports).
  const fitNext = useRef(true);
  const [mapView, setMapView] = useState<{ lat: number; lng: number } | null>(null);

  const report = (e: unknown) => setError((e as Error).message || String(e));

  const loadPlaces = useCallback(async () => {
    try {
      const args: Record<string, unknown> = { status, limit: 1000 };
      if (query.trim()) args.q = query.trim();
      if (kind) args.kind = kind;
      if (listId) args.list_id = listId;
      if (tag) args.tag = tag;
      const data = await call<{ places: Place[] }>("places_search", args);
      setPlaces(data.places ?? []);
      setError("");
      if (fitNext.current) {
        fitNext.current = false;
        setFitKey((k) => k + 1);
      }
    } catch (e) {
      report(e);
    } finally {
      setLoading(false);
    }
  }, [call, kind, listId, query, status, tag]);

  const loadLists = useCallback(async () => {
    try {
      const data = await call<{ lists: PlaceList[] }>("lists_list");
      setLists(data.lists ?? []);
    } catch (e) {
      report(e);
    }
  }, [call]);

  const loadSettings = useCallback(async () => {
    try {
      setSettings(await call<SettingsData>("settings_get"));
    } catch (e) {
      report(e);
    }
  }, [call]);

  useEffect(() => { loadLists(); loadSettings(); }, [loadLists, loadSettings]);
  useEffect(() => {
    const t = setTimeout(loadPlaces, query ? 250 : 0);
    return () => clearTimeout(t);
  }, [loadPlaces, query]);
  // Re-fit the map when the filters change (not on every edit).
  useEffect(() => { fitNext.current = true; }, [status, kind, listId, tag]);

  const refresh = useCallback(async () => { await Promise.all([loadPlaces(), loadLists()]); }, [loadPlaces, loadLists]);

  const allTags = useMemo(() => {
    const seen = new Map<string, number>();
    for (const p of places) for (const t of p.tags ?? []) seen.set(t, (seen.get(t) ?? 0) + 1);
    return [...seen.entries()].sort((a, b) => b[1] - a[1]).map(([t]) => t);
  }, [places]);
  const kinds = useMemo(() => {
    const set = new Set<string>(settings?.kinds ?? []);
    for (const p of places) set.add(p.kind);
    return [...set];
  }, [places, settings]);
  const selected = places.find((p) => p.id === selectedId) ?? null;
  const counts = useMemo(() => {
    const out: Record<string, number> = {};
    for (const p of places) out[p.status] = (out[p.status] ?? 0) + 1;
    return out;
  }, [places]);

  const home = settings?.settings.home_lat != null && settings?.settings.home_lng != null
    ? { lat: settings.settings.home_lat, lng: settings.settings.home_lng }
    : null;

  return (
    <div style={{ height: "100%", minHeight: 0, display: "flex", flexDirection: "column", background: c.bg, color: c.text, fontSize: 13 }}>
      <header style={{ flexShrink: 0, borderBottom: `1px solid ${c.border}`, padding: "10px 16px", display: "flex", alignItems: "center", gap: 12, flexWrap: "wrap" }}>
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span style={{ color: c.accent, display: "flex" }}><Icon d={icons.pin} size={18} /></span>
          <div>
            <div style={{ fontWeight: 600, fontSize: 14 }}>Places</div>
            <div style={{ fontSize: 11, color: c.muted }}>
              {places.length} shown · lookups via {settings?.lookup_source === "google" ? "Google Places" : "OpenStreetMap"}
            </div>
          </div>
        </div>
        <QuickAdd
          call={call}
          near={mapView ?? home}
          onAdded={async (p, msg) => {
            setMessage(msg);
            await refresh();
            setSelectedId(p.id);
          }}
          onError={report}
        />
        <div style={{ marginLeft: "auto", display: "flex", gap: 6 }}>
          <button type="button" style={s.btn} onClick={() => setDialog("import")}><Icon d={icons.upload} />Import</button>
          <a style={{ ...s.btn, textDecoration: "none" }} href={`${API}/export?format=geojson&project_id=${encodeURIComponent(projectId)}`}><Icon d={icons.download} />Export</a>
          <button type="button" style={s.btn} onClick={() => setDialog("settings")} aria-label="Settings"><Icon d={icons.settings} /></button>
        </div>
      </header>

      <div style={{ flexShrink: 0, borderBottom: `1px solid ${c.border}`, padding: "8px 16px", display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
        <button type="button" style={s.chip(status === "active")} onClick={() => setStatus("active")}>All</button>
        {STATUSES.map((st) => (
          <button key={st.id} type="button" style={s.chip(status === st.id)} onClick={() => setStatus(status === st.id ? "active" : st.id)}>
            <span style={{ display: "inline-block", width: 8, height: 8, borderRadius: 999, background: st.color, marginRight: 6 }} />
            {st.label}{status === "active" && counts[st.id] ? ` ${counts[st.id]}` : ""}
          </button>
        ))}
        <span style={{ width: 1, height: 20, background: c.border, margin: "0 4px" }} />
        <select style={s.input} value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="">All kinds</option>
          {kinds.map((k) => <option key={k} value={k}>{kindLabel(k)}</option>)}
        </select>
        <select style={s.input} value={listId} onChange={(e) => setListId(Number(e.target.value))}>
          <option value={0}>All lists</option>
          {lists.map((l) => <option key={l.id} value={l.id}>{l.name} ({l.place_count})</option>)}
        </select>
        <select style={s.input} value={tag} onChange={(e) => setTag(e.target.value)}>
          <option value="">All tags</option>
          {(tag && !allTags.includes(tag) ? [tag, ...allTags] : allTags).map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <div style={{ position: "relative", marginLeft: "auto" }}>
          <span style={{ position: "absolute", left: 8, top: 8, color: c.dim, display: "flex" }}><Icon d={icons.search} size={13} /></span>
          <input style={{ ...s.input, paddingLeft: 26, width: 220 }} value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search saved places" />
        </div>
      </div>

      {error && (
        <div style={{ flexShrink: 0, padding: "6px 16px", fontSize: 12, color: c.error, borderBottom: `1px solid ${c.border}`, display: "flex", gap: 8 }}>
          <span style={{ flex: 1 }}>{error}</span>
          <button type="button" style={{ ...s.btn, padding: "0 6px", border: "none" }} onClick={() => setError("")} aria-label="Dismiss"><Icon d={icons.x} size={12} /></button>
        </div>
      )}

      <main style={{ flex: 1, minHeight: 0, display: "flex" }}>
        <aside style={{ width: 340, flexShrink: 0, borderRight: `1px solid ${c.border}`, overflowY: "auto" }}>
          {loading ? (
            <div style={{ padding: 16, color: c.muted }}>Loading…</div>
          ) : places.length === 0 ? (
            <EmptyState filtered={!!(query || kind || listId || tag || status !== "active")} />
          ) : (
            places.map((p) => (
              <PlaceRow key={p.id} place={p} selected={p.id === selectedId} onSelect={() => setSelectedId(p.id === selectedId ? 0 : p.id)} />
            ))
          )}
        </aside>

        <section style={{ flex: 1, minWidth: 0, position: "relative" }}>
          <TileMap
            places={places}
            selectedId={selectedId}
            onSelect={setSelectedId}
            fitKey={fitKey}
            home={home}
            onViewChange={setMapView}
          />
          {selected && (
            <PlaceDrawer
              key={selected.id}
              place={selected}
              lists={lists}
              kinds={kinds}
              call={call}
              onClose={() => setSelectedId(0)}
              onChanged={async (msg) => { if (msg) setMessage(msg); await refresh(); }}
              onDeleted={async () => { setSelectedId(0); setMessage("Place deleted."); await refresh(); }}
              onError={report}
            />
          )}
        </section>
      </main>

      <footer style={{ flexShrink: 0, borderTop: `1px solid ${c.border}`, padding: "4px 16px", fontSize: 11, color: c.muted, display: "flex", gap: 12 }}>
        <span>{message}</span>
        <span style={{ marginLeft: "auto" }}>Map data © <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noreferrer" style={{ color: c.muted }}>OpenStreetMap</a> contributors</span>
      </footer>

      {dialog === "import" && (
        <ImportDialog call={call} lists={lists} onClose={() => setDialog("")} onDone={async (msg) => { setDialog(""); setMessage(msg); fitNext.current = true; await refresh(); }} />
      )}
      {dialog === "settings" && settings && (
        <SettingsDialog call={call} data={settings} mapView={mapView} onClose={() => setDialog("")} onSaved={async () => { setDialog(""); await loadSettings(); setMessage("Settings saved."); }} />
      )}
    </div>
  );
}

function EmptyState({ filtered }: { filtered: boolean }) {
  return (
    <div style={{ padding: 20, color: c.muted, fontSize: 13, lineHeight: 1.6 }}>
      {filtered ? (
        <>No places match these filters.</>
      ) : (
        <>
          <div style={{ color: c.text, fontWeight: 600, marginBottom: 4 }}>No places yet</div>
          Type a restaurant, bar or hotel name in the box above, paste a Google Maps link, or import your Google Maps saved places. You can also ask your agent: "save Bar Mut in Barcelona, we want to try it".
        </>
      )}
    </div>
  );
}

function PlaceRow({ place: p, selected, onSelect }: { place: Place; selected: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      onClick={onSelect}
      style={{
        display: "block", width: "100%", textAlign: "left", padding: "10px 14px", border: "none",
        borderBottom: `1px solid ${c.border}`, background: selected ? c.input : "transparent", color: c.text, cursor: "pointer",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span style={{ width: 8, height: 8, borderRadius: 999, background: statusColor(p.status), flexShrink: 0 }} title={statusLabel(p.status)} />
        <span style={{ fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{p.name}</span>
        {p.my_rating != null && <span style={{ marginLeft: "auto", fontSize: 11, color: c.muted, flexShrink: 0 }}>{p.my_rating}/10</span>}
      </div>
      <div style={{ fontSize: 12, color: c.muted, marginTop: 2, paddingLeft: 16 }}>
        {[kindLabel(p.kind), p.city, priceLabel(p.price_level)].filter(Boolean).join(" · ")}
        {p.lat == null && <span style={{ color: c.dim }}> · no location</span>}
      </div>
      {p.tags?.length > 0 && (
        <div style={{ display: "flex", gap: 4, flexWrap: "wrap", marginTop: 6, paddingLeft: 16 }}>
          {p.tags.slice(0, 4).map((t) => <span key={t} style={s.tag}>{t}</span>)}
        </div>
      )}
    </button>
  );
}

// ─── quick add ──────────────────────────────────────────────────────

type CallFn = <T>(tool: string, args?: Record<string, unknown>) => Promise<T>;

function QuickAdd({ call, near, onAdded, onError }: {
  call: CallFn;
  near: { lat: number; lng: number } | null;
  onAdded: (p: Place, msg: string) => void;
  onError: (e: unknown) => void;
}) {
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [cands, setCands] = useState<Candidate[] | null>(null);
  const isLink = /^https?:\/\//i.test(text.trim());

  const add = async (args: Record<string, unknown>) => {
    setBusy(true);
    try {
      const r = await call<{ place: Place; duplicate: boolean; lookup_error?: string }>("places_add", args);
      const msg = r.duplicate ? `Already saved: ${r.place.name}` : `Saved ${r.place.name}${r.lookup_error ? ` (${r.lookup_error})` : ""}`;
      setText("");
      setCands(null);
      onAdded(r.place, msg);
    } catch (e) {
      onError(e);
    } finally {
      setBusy(false);
    }
  };

  const submit = async () => {
    const q = text.trim();
    if (!q) return;
    if (isLink) {
      await add(/google\.|goo\.gl/i.test(q) ? { url: q } : { url: q, name: "" });
      return;
    }
    setBusy(true);
    try {
      const args: Record<string, unknown> = { query: q, limit: 6 };
      if (near) { args.near_lat = near.lat; args.near_lng = near.lng; }
      const r = await call<{ candidates: Candidate[] }>("places_lookup", args);
      setCands(r.candidates ?? []);
    } catch (e) {
      onError(e);
    } finally {
      setBusy(false);
    }
  };

  const fromCandidate = (cd: Candidate) => add({
    name: cd.name, kind: cd.kind, lat: cd.lat, lng: cd.lng, address: cd.address, city: cd.city, country: cd.country,
    google_place_id: cd.google_place_id, website: cd.website, phone: cd.phone, price_level: cd.price_level ?? undefined,
    url: cd.google_maps_url || undefined, lookup: false,
  });

  return (
    <div style={{ position: "relative", flex: "1 1 320px", maxWidth: 520 }}>
      <form onSubmit={(e) => { e.preventDefault(); submit(); }} style={{ display: "flex", gap: 6 }}>
        <input
          style={{ ...s.input, flex: 1 }}
          value={text}
          onChange={(e) => { setText(e.target.value); setCands(null); }}
          placeholder="Add a place: name and city, or a Google Maps link"
          aria-label="Add a place"
        />
        <button type="submit" style={s.primary} disabled={busy || !text.trim()}>
          {busy ? "…" : isLink ? "Save" : "Find"}
        </button>
      </form>
      {cands && (
        <div style={{ position: "absolute", zIndex: 30, top: "calc(100% + 4px)", left: 0, right: 0, background: c.card, border: `1px solid ${c.border}`, borderRadius: 8, boxShadow: "0 8px 24px rgba(0,0,0,.18)", overflow: "hidden" }}>
          {cands.length === 0 && <div style={{ padding: 12, color: c.muted }}>No match found.</div>}
          {cands.map((cd, i) => (
            <button
              key={`${cd.google_place_id || cd.osm_id || i}`}
              type="button"
              disabled={busy}
              onClick={() => fromCandidate(cd)}
              style={{ display: "block", width: "100%", textAlign: "left", padding: "8px 12px", border: "none", borderBottom: `1px solid ${c.border}`, background: "transparent", color: c.text, cursor: "pointer" }}
            >
              <div style={{ display: "flex", gap: 8 }}>
                <span style={{ fontWeight: 600 }}>{cd.name}</span>
                <span style={{ fontSize: 11, color: c.muted }}>{kindLabel(cd.kind)}</span>
                {cd.saved_place_id ? <span style={{ marginLeft: "auto", fontSize: 11, color: c.accent }}>Saved</span> : null}
              </div>
              <div style={{ fontSize: 12, color: c.muted, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{cd.address}</div>
            </button>
          ))}
          <div style={{ display: "flex", gap: 6, padding: 8 }}>
            <button type="button" style={s.btn} disabled={busy} onClick={() => add({ name: text.trim(), lookup: false })}>Save "{text.trim()}" without a location</button>
            <button type="button" style={{ ...s.btn, marginLeft: "auto" }} onClick={() => setCands(null)}>Close</button>
          </div>
        </div>
      )}
    </div>
  );
}

// ─── detail drawer ──────────────────────────────────────────────────

function PlaceDrawer({ place, lists, kinds, call, onClose, onChanged, onDeleted, onError }: {
  place: Place;
  lists: PlaceList[];
  kinds: string[];
  call: CallFn;
  onClose: () => void;
  onChanged: (msg?: string) => Promise<void>;
  onDeleted: () => Promise<void>;
  onError: (e: unknown) => void;
}) {
  const [visits, setVisits] = useState<Visit[]>([]);
  const [draft, setDraft] = useState({ name: place.name, kind: place.kind, notes: place.notes, tags: (place.tags ?? []).join(", "), my_rating: place.my_rating?.toString() ?? "" });
  const [visitDraft, setVisitDraft] = useState<null | { visited_on: string; with_whom: string; dishes: string; spend: string; rating: string; notes: string }>(null);
  const [busy, setBusy] = useState(false);
  const [newList, setNewList] = useState("");

  const loadVisits = useCallback(async () => {
    try {
      const r = await call<{ visits: Visit[] }>("places_get", { id: place.id });
      setVisits(r.visits ?? []);
    } catch (e) {
      onError(e);
    }
  }, [call, onError, place.id]);
  useEffect(() => { loadVisits(); }, [loadVisits]);

  const run = async (fn: () => Promise<unknown>, msg?: string) => {
    setBusy(true);
    try {
      await fn();
      await onChanged(msg);
    } catch (e) {
      onError(e);
    } finally {
      setBusy(false);
    }
  };

  const dirty = draft.name !== place.name || draft.kind !== place.kind || draft.notes !== place.notes ||
    draft.tags !== (place.tags ?? []).join(", ") || draft.my_rating !== (place.my_rating?.toString() ?? "");

  const save = () => run(() => call("places_update", {
    id: place.id, name: draft.name, kind: draft.kind, notes: draft.notes,
    tags: draft.tags.split(",").map((t) => t.trim()).filter(Boolean),
    my_rating: draft.my_rating === "" ? null : Number(draft.my_rating),
  }), "Saved.");

  const mapsHref = place.google_maps_url
    || (place.lat != null ? `https://www.google.com/maps/search/?api=1&query=${place.lat},${place.lng}${place.google_place_id ? `&query_place_id=${place.google_place_id}` : ""}` : `https://www.google.com/maps/search/?api=1&query=${encodeURIComponent(`${place.name} ${place.city}`)}`);

  const today = new Date().toISOString().slice(0, 10);

  return (
    <div style={{ position: "absolute", top: 12, right: 12, bottom: 12, width: 360, maxWidth: "calc(100% - 24px)", zIndex: 20, background: c.card, border: `1px solid ${c.border}`, borderRadius: 10, boxShadow: "0 10px 30px rgba(0,0,0,.2)", display: "flex", flexDirection: "column", overflow: "hidden" }}>
      <div style={{ padding: "12px 14px", borderBottom: `1px solid ${c.border}`, display: "flex", gap: 8, alignItems: "flex-start" }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <input style={{ ...s.input, width: "100%", fontSize: 15, fontWeight: 600, background: "transparent", border: "none", padding: 0 }} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} aria-label="Name" />
          <div style={{ fontSize: 12, color: c.muted, marginTop: 2 }}>{place.address || [place.city, place.country].filter(Boolean).join(", ") || "No address"}</div>
        </div>
        <button type="button" style={{ ...s.btn, border: "none", padding: 4 }} onClick={onClose} aria-label="Close"><Icon d={icons.x} /></button>
      </div>

      <div style={{ flex: 1, overflowY: "auto", padding: 14, display: "flex", flexDirection: "column", gap: 14 }}>
        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
          {STATUSES.map((st) => (
            <button key={st.id} type="button" disabled={busy} style={s.chip(place.status === st.id)}
              onClick={() => place.status !== st.id && run(() => call("places_update", { id: place.id, status: st.id }), `Marked ${st.label.toLowerCase()}.`)}>
              <span style={{ display: "inline-block", width: 8, height: 8, borderRadius: 999, background: st.color, marginRight: 6 }} />{st.label}
            </button>
          ))}
        </div>

        <div style={{ display: "flex", gap: 6, flexWrap: "wrap", fontSize: 12, color: c.muted }}>
          {place.google_rating != null && <span>Google {place.google_rating.toFixed(1)}</span>}
          {place.price_level != null && <span>{priceLabel(place.price_level)}</span>}
          {place.visit_count > 0 && <span>{place.visit_count} visit{place.visit_count === 1 ? "" : "s"}, last {place.last_visited_at}</span>}
        </div>

        <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
          <a style={{ ...s.btn, textDecoration: "none" }} href={mapsHref} target="_blank" rel="noreferrer"><Icon d={icons.external} size={12} />Google Maps</a>
          {place.website && <a style={{ ...s.btn, textDecoration: "none" }} href={place.website} target="_blank" rel="noreferrer"><Icon d={icons.external} size={12} />Website</a>}
          {place.source_url && <a style={{ ...s.btn, textDecoration: "none" }} href={place.source_url} target="_blank" rel="noreferrer"><Icon d={icons.external} size={12} />Source</a>}
          {place.phone && <a style={{ ...s.btn, textDecoration: "none" }} href={`tel:${place.phone}`}>{place.phone}</a>}
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "1fr 90px", gap: 8 }}>
          <label style={s.label}>Kind
            <select style={s.input} value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value })}>
              {(kinds.includes(draft.kind) ? kinds : [draft.kind, ...kinds]).map((k) => <option key={k} value={k}>{kindLabel(k)}</option>)}
            </select>
          </label>
          <label style={s.label}>My rating
            <select style={s.input} value={draft.my_rating} onChange={(e) => setDraft({ ...draft, my_rating: e.target.value })}>
              <option value="">–</option>
              {[10, 9, 8, 7, 6, 5, 4, 3, 2, 1].map((n) => <option key={n} value={n}>{n}/10</option>)}
            </select>
          </label>
        </div>
        <label style={s.label}>Tags
          <input style={s.input} value={draft.tags} onChange={(e) => setDraft({ ...draft, tags: e.target.value })} placeholder="tapas, terrace, date night" />
        </label>
        <label style={s.label}>Notes
          <textarea style={{ ...s.input, minHeight: 70, resize: "vertical", fontFamily: "inherit" }} value={draft.notes} onChange={(e) => setDraft({ ...draft, notes: e.target.value })} placeholder="What to order, who recommended it, best time to go" />
        </label>
        {dirty && (
          <div style={{ display: "flex", gap: 6 }}>
            <button type="button" style={s.primary} disabled={busy} onClick={save}>Save changes</button>
            <button type="button" style={s.btn} onClick={() => setDraft({ name: place.name, kind: place.kind, notes: place.notes, tags: (place.tags ?? []).join(", "), my_rating: place.my_rating?.toString() ?? "" })}>Reset</button>
          </div>
        )}

        <div>
          <div style={{ fontSize: 11, color: c.muted, marginBottom: 6 }}>Lists</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
            {lists.map((l) => {
              const on = place.list_ids?.includes(l.id);
              return (
                <label key={l.id} style={{ display: "flex", alignItems: "center", gap: 8, fontSize: 13, cursor: "pointer" }}>
                  <input type="checkbox" checked={on} disabled={busy}
                    onChange={() => run(() => call(on ? "lists_remove" : "lists_add", { list_id: l.id, place_ids: [place.id] }))} />
                  {l.name}
                </label>
              );
            })}
            <form style={{ display: "flex", gap: 6, marginTop: 4 }} onSubmit={(e) => {
              e.preventDefault();
              const name = newList.trim();
              if (!name) return;
              run(() => call("lists_add", { list_name: name, place_ids: [place.id] }), `Added to ${name}.`).then(() => setNewList(""));
            }}>
              <input style={{ ...s.input, flex: 1 }} value={newList} onChange={(e) => setNewList(e.target.value)} placeholder="New list, e.g. Lisbon trip" />
              <button type="submit" style={s.btn} disabled={busy || !newList.trim()}>Add</button>
            </form>
          </div>
        </div>

        <div>
          <div style={{ display: "flex", alignItems: "center", marginBottom: 6 }}>
            <span style={{ fontSize: 11, color: c.muted }}>Visits</span>
            {!visitDraft && (
              <button type="button" style={{ ...s.btn, marginLeft: "auto", padding: "3px 8px" }}
                onClick={() => setVisitDraft({ visited_on: today, with_whom: "", dishes: "", spend: "", rating: "", notes: "" })}>
                <Icon d={icons.plus} size={12} />Log visit
              </button>
            )}
          </div>
          {visitDraft && (
            <form style={{ display: "flex", flexDirection: "column", gap: 6, padding: 10, border: `1px solid ${c.border}`, borderRadius: 8, marginBottom: 8 }}
              onSubmit={(e) => {
                e.preventDefault();
                run(async () => {
                  await call("places_log_visit", {
                    place_id: place.id, visited_on: visitDraft.visited_on, with_whom: visitDraft.with_whom,
                    dishes: visitDraft.dishes.split(",").map((d) => d.trim()).filter(Boolean),
                    spend: visitDraft.spend === "" ? undefined : Number(visitDraft.spend),
                    rating: visitDraft.rating === "" ? undefined : Number(visitDraft.rating), notes: visitDraft.notes,
                  });
                  setVisitDraft(null);
                  await loadVisits();
                }, "Visit logged.");
              }}>
              <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 6 }}>
                <input type="date" style={s.input} value={visitDraft.visited_on} onChange={(e) => setVisitDraft({ ...visitDraft, visited_on: e.target.value })} />
                <select style={s.input} value={visitDraft.rating} onChange={(e) => setVisitDraft({ ...visitDraft, rating: e.target.value })}>
                  <option value="">Rating</option>
                  {[10, 9, 8, 7, 6, 5, 4, 3, 2, 1].map((n) => <option key={n} value={n}>{n}/10</option>)}
                </select>
                <input style={s.input} value={visitDraft.with_whom} onChange={(e) => setVisitDraft({ ...visitDraft, with_whom: e.target.value })} placeholder="With" />
                <input style={s.input} inputMode="decimal" value={visitDraft.spend} onChange={(e) => setVisitDraft({ ...visitDraft, spend: e.target.value })} placeholder="Spent" />
              </div>
              <input style={s.input} value={visitDraft.dishes} onChange={(e) => setVisitDraft({ ...visitDraft, dishes: e.target.value })} placeholder={place.kind === "hotel" ? "Room, highlights" : "Dishes (comma separated)"} />
              <input style={s.input} value={visitDraft.notes} onChange={(e) => setVisitDraft({ ...visitDraft, notes: e.target.value })} placeholder="Notes" />
              <div style={{ display: "flex", gap: 6 }}>
                <button type="submit" style={s.primary} disabled={busy}>Save visit</button>
                <button type="button" style={s.btn} onClick={() => setVisitDraft(null)}>Cancel</button>
              </div>
            </form>
          )}
          {visits.length === 0 && !visitDraft && <div style={{ fontSize: 12, color: c.dim }}>Not visited yet.</div>}
          {visits.map((v) => (
            <div key={v.id} style={{ padding: "6px 0", borderTop: `1px solid ${c.border}`, fontSize: 12 }}>
              <div style={{ display: "flex", gap: 8 }}>
                <span style={{ fontWeight: 600 }}>{v.visited_on}</span>
                {v.with_whom && <span style={{ color: c.muted }}>with {v.with_whom}</span>}
                {v.rating != null && <span style={{ marginLeft: "auto", color: c.muted }}>{v.rating}/10</span>}
                <button type="button" aria-label="Delete visit" style={{ ...s.btn, border: "none", padding: 0, marginLeft: v.rating != null ? 0 : "auto", color: c.dim }}
                  onClick={() => run(async () => { await call("places_delete_visit", { visit_id: v.id }); await loadVisits(); })}><Icon d={icons.x} size={12} /></button>
              </div>
              {(v.dishes?.length > 0 || v.spend != null) && (
                <div style={{ color: c.muted }}>{[v.dishes?.join(", "), v.spend != null ? `${v.spend}${v.currency ? ` ${v.currency}` : ""}` : ""].filter(Boolean).join(" · ")}</div>
              )}
              {v.notes && <div style={{ color: c.muted }}>{v.notes}</div>}
            </div>
          ))}
        </div>
      </div>

      <div style={{ padding: "10px 14px", borderTop: `1px solid ${c.border}`, display: "flex", gap: 6 }}>
        <button type="button" style={{ ...s.btn, color: c.error }} disabled={busy}
          onClick={() => { if (window.confirm(`Delete ${place.name} and its visits?`)) run(() => call("places_delete", { id: place.id })).then(onDeleted); }}>
          Delete
        </button>
        {place.status !== "closed" && (
          <button type="button" style={s.btn} disabled={busy} onClick={() => run(() => call("places_update", { id: place.id, status: "closed" }), "Marked closed.")}>Mark closed</button>
        )}
      </div>
    </div>
  );
}

// ─── import / settings ──────────────────────────────────────────────

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  return (
    <div style={{ position: "fixed", inset: 0, zIndex: 50, background: "rgba(0,0,0,.4)", display: "flex", alignItems: "center", justifyContent: "center", padding: 16 }} onClick={onClose}>
      <div style={{ width: 460, maxWidth: "100%", background: c.card, border: `1px solid ${c.border}`, borderRadius: 10, padding: 16, display: "flex", flexDirection: "column", gap: 12, color: c.text }} onClick={(e) => e.stopPropagation()}>
        <div style={{ display: "flex", alignItems: "center" }}>
          <span style={{ fontWeight: 600, fontSize: 14 }}>{title}</span>
          <button type="button" style={{ ...s.btn, border: "none", marginLeft: "auto", padding: 4 }} onClick={onClose} aria-label="Close"><Icon d={icons.x} /></button>
        </div>
        {children}
      </div>
    </div>
  );
}

function ImportDialog({ call, lists, onClose, onDone }: { call: CallFn; lists: PlaceList[]; onClose: () => void; onDone: (msg: string) => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [listName, setListName] = useState("");
  const [status, setStatus] = useState("to_try");
  const [lookup, setLookup] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const go = async () => {
    if (!file) return;
    setBusy(true);
    setErr("");
    try {
      const content = await file.text();
      const r = await call<{ added: number; duplicates: number; failed: number; without_coordinates: number }>("places_import", {
        content, list_name: listName.trim() || undefined, status, lookup,
      });
      onDone(`Imported ${r.added} place${r.added === 1 ? "" : "s"}${r.duplicates ? `, ${r.duplicates} already saved` : ""}${r.without_coordinates ? `, ${r.without_coordinates} without location` : ""}${r.failed ? `, ${r.failed} failed` : ""}.`);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title="Import places" onClose={onClose}>
      <div style={{ fontSize: 12, color: c.muted, lineHeight: 1.5 }}>
        From Google Takeout (Maps (your places) → "Saved Places.json", or a list CSV from the Saved folder), a GeoJSON file, or a CSV with name, lat and lng columns.
      </div>
      <input type="file" accept=".json,.geojson,.csv,application/json,text/csv" onChange={(e) => setFile(e.target.files?.[0] ?? null)} style={{ fontSize: 12 }} />
      <label style={s.label}>Add to list (optional)
        <input style={s.input} list="places-import-lists" value={listName} onChange={(e) => setListName(e.target.value)} placeholder="e.g. Google Maps favourites" />
        <datalist id="places-import-lists">{lists.map((l) => <option key={l.id} value={l.name} />)}</datalist>
      </label>
      <label style={s.label}>Status for imported places
        <select style={s.input} value={status} onChange={(e) => setStatus(e.target.value)}>
          {STATUSES.map((st) => <option key={st.id} value={st.id}>{st.label}</option>)}
        </select>
      </label>
      <label style={{ display: "flex", gap: 8, alignItems: "center", fontSize: 12 }}>
        <input type="checkbox" checked={lookup} onChange={(e) => setLookup(e.target.checked)} />
        Look up rows that have no location (up to 50, slower)
      </label>
      {err && <div style={{ color: c.error, fontSize: 12 }}>{err}</div>}
      <div style={{ display: "flex", gap: 6, justifyContent: "flex-end" }}>
        <button type="button" style={s.btn} onClick={onClose}>Cancel</button>
        <button type="button" style={s.primary} disabled={!file || busy} onClick={go}>{busy ? "Importing…" : "Import"}</button>
      </div>
    </Modal>
  );
}

function SettingsDialog({ call, data, mapView, onClose, onSaved }: {
  call: CallFn; data: SettingsData; mapView: { lat: number; lng: number } | null; onClose: () => void; onSaved: () => void;
}) {
  const [conns, setConns] = useState<{ id: number; name: string; status: string }[] | null>(null);
  const [connId, setConnId] = useState(data.settings.places_connection_id || 0);
  const [home, setHome] = useState<{ lat: number; lng: number } | null>(
    data.settings.home_lat != null && data.settings.home_lng != null ? { lat: data.settings.home_lat, lng: data.settings.home_lng } : null);
  const [label, setLabel] = useState(data.settings.home_label);
  const [err, setErr] = useState("");
  useEffect(() => {
    call<{ connections: { id: number; name: string; status: string }[] }>("connections_list")
      .then((r) => setConns(r.connections ?? []))
      .catch(() => setConns([]));
  }, [call]);
  const save = async () => {
    try {
      await call("settings_set", { places_connection_id: connId, home_lat: home?.lat ?? null, home_lng: home?.lng ?? null, home_label: label });
      onSaved();
    } catch (e) {
      setErr((e as Error).message);
    }
  };
  return (
    <Modal title="Places settings" onClose={onClose}>
      <label style={s.label}>Place lookups
        <select style={s.input} value={connId} onChange={(e) => setConnId(Number(e.target.value))}>
          <option value={0}>OpenStreetMap (free, no setup)</option>
          {(conns ?? []).map((cn) => <option key={cn.id} value={cn.id}>Google Places: {cn.name}{cn.status !== "active" ? ` (${cn.status})` : ""}</option>)}
        </select>
        {conns && conns.length === 0 && <span style={{ color: c.dim }}>No Google Places connection in this project. Add one under Integrations for better restaurant and hotel data.</span>}
      </label>
      <label style={s.label}>Home area (centres the map and biases lookups)
        <input style={s.input} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="e.g. Barcelona" />
      </label>
      <div style={{ display: "flex", gap: 6, alignItems: "center", fontSize: 12, color: c.muted }}>
        <span style={{ flex: 1 }}>{home ? `${home.lat.toFixed(4)}, ${home.lng.toFixed(4)}` : "Not set"}</span>
        <button type="button" style={s.btn} disabled={!mapView} onClick={() => mapView && setHome(mapView)}><Icon d={icons.locate} size={12} />Use map centre</button>
        {home && <button type="button" style={s.btn} onClick={() => setHome(null)}>Clear</button>}
      </div>
      {err && <div style={{ color: c.error, fontSize: 12 }}>{err}</div>}
      <div style={{ display: "flex", gap: 6, justifyContent: "flex-end" }}>
        <button type="button" style={s.btn} onClick={onClose}>Cancel</button>
        <button type="button" style={s.primary} onClick={save}>Save</button>
      </div>
    </Modal>
  );
}

// ─── tile map (Web Mercator, OSM raster tiles) ──────────────────────

const TILE = 256;
const MIN_Z = 2;
const MAX_Z = 19;
const project = (lat: number, lng: number, z: number) => {
  const scale = TILE * 2 ** z;
  const sin = Math.min(Math.max(Math.sin((lat * Math.PI) / 180), -0.9999), 0.9999);
  return { x: ((lng + 180) / 360) * scale, y: (0.5 - Math.log((1 + sin) / (1 - sin)) / (4 * Math.PI)) * scale };
};
const unproject = (x: number, y: number, z: number) => {
  const scale = TILE * 2 ** z;
  const lng = (x / scale) * 360 - 180;
  const n = Math.PI - (2 * Math.PI * y) / scale;
  return { lat: (180 / Math.PI) * Math.atan(Math.sinh(n)), lng };
};

function TileMap({ places, selectedId, onSelect, fitKey, home, onViewChange }: {
  places: Place[];
  selectedId: number;
  onSelect: (id: number) => void;
  fitKey: number;
  home: { lat: number; lng: number } | null;
  onViewChange: (v: { lat: number; lng: number }) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [view, setView] = useState({ lat: 41.39, lng: 2.17, z: 12 });
  const drag = useRef<{ x: number; y: number; cx: number; cy: number; moved: boolean } | null>(null);
  const fittedFor = useRef(-1);
  const located = places.filter((p) => p.lat != null && p.lng != null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Fit to the visible places once per filter change (and on first data).
  useEffect(() => {
    if (!size.w || !size.h || fittedFor.current === fitKey) return;
    if (located.length === 0) {
      if (home) setView({ lat: home.lat, lng: home.lng, z: 13 });
      fittedFor.current = fitKey;
      return;
    }
    const lats = located.map((p) => p.lat as number);
    const lngs = located.map((p) => p.lng as number);
    const bounds = { n: Math.max(...lats), s: Math.min(...lats), e: Math.max(...lngs), w: Math.min(...lngs) };
    let z = 16;
    for (; z > MIN_Z; z--) {
      const a = project(bounds.n, bounds.w, z);
      const b = project(bounds.s, bounds.e, z);
      if (b.x - a.x < size.w - 80 && b.y - a.y < size.h - 80) break;
    }
    setView({ lat: (bounds.n + bounds.s) / 2, lng: (bounds.e + bounds.w) / 2, z });
    fittedFor.current = fitKey;
  }, [fitKey, located, size, home]);

  // Pan the selected place into the part of the map the drawer doesn't
  // cover (the drawer is 360px wide + 12px margins on the right).
  useEffect(() => {
    const p = located.find((x) => x.id === selectedId);
    if (!p || !size.w) return;
    const inset = size.w > 600 ? 384 : 0;
    const visibleW = size.w - inset;
    const ctr = project(view.lat, view.lng, view.z);
    const pt = project(p.lat as number, p.lng as number, view.z);
    const x = pt.x - ctr.x + size.w / 2;
    const y = pt.y - ctr.y + size.h / 2;
    if (x < 40 || x > visibleW - 40 || y < 60 || y > size.h - 40) {
      const ll = unproject(pt.x + inset / 2, pt.y, view.z);
      setView((v) => ({ ...v, lat: ll.lat, lng: ll.lng }));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedId]);

  useEffect(() => { onViewChange({ lat: view.lat, lng: view.lng }); }, [view.lat, view.lng, onViewChange]);

  const center = project(view.lat, view.lng, view.z);
  const originX = center.x - size.w / 2;
  const originY = center.y - size.h / 2;
  const n = 2 ** view.z;

  const tiles: { key: string; x: number; y: number; url: string }[] = [];
  if (size.w && size.h) {
    const x0 = Math.floor(originX / TILE);
    const y0 = Math.floor(originY / TILE);
    const x1 = Math.floor((originX + size.w) / TILE);
    const y1 = Math.floor((originY + size.h) / TILE);
    for (let ty = y0; ty <= y1; ty++) {
      if (ty < 0 || ty >= n) continue;
      for (let tx = x0; tx <= x1; tx++) {
        const wx = ((tx % n) + n) % n;
        tiles.push({ key: `${view.z}/${tx}/${ty}`, x: tx * TILE - originX, y: ty * TILE - originY, url: `https://tile.openstreetmap.org/${view.z}/${wx}/${ty}.png` });
      }
    }
  }

  const zoomAt = (dz: number, px = size.w / 2, py = size.h / 2) => {
    setView((v) => {
      const z = Math.min(MAX_Z, Math.max(MIN_Z, v.z + dz));
      if (z === v.z) return v;
      const ctr = project(v.lat, v.lng, v.z);
      const anchor = unproject(ctr.x - size.w / 2 + px, ctr.y - size.h / 2 + py, v.z);
      const a2 = project(anchor.lat, anchor.lng, z);
      const c2 = unproject(a2.x - px + size.w / 2, a2.y - py + size.h / 2, z);
      return { lat: c2.lat, lng: c2.lng, z };
    });
  };

  const wheelLock = useRef(0);
  const onWheel = (e: RWheelEvent) => {
    const now = Date.now();
    if (now - wheelLock.current < 180 || Math.abs(e.deltaY) < 4) return;
    wheelLock.current = now;
    const r = ref.current!.getBoundingClientRect();
    zoomAt(e.deltaY < 0 ? 1 : -1, e.clientX - r.left, e.clientY - r.top);
  };

  const onPointerDown = (e: RPointerEvent) => {
    if ((e.target as HTMLElement).closest("[data-pin],[data-ctl]")) return;
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    drag.current = { x: e.clientX, y: e.clientY, cx: center.x, cy: center.y, moved: false };
  };
  const onPointerMove = (e: RPointerEvent) => {
    const d = drag.current;
    if (!d) return;
    const dx = e.clientX - d.x;
    const dy = e.clientY - d.y;
    if (Math.abs(dx) + Math.abs(dy) > 3) d.moved = true;
    const ll = unproject(d.cx - dx, d.cy - dy, view.z);
    setView((v) => ({ ...v, lat: ll.lat, lng: ll.lng }));
  };
  const onPointerUp = () => {
    if (drag.current && !drag.current.moved) onSelect(0);
    drag.current = null;
  };

  return (
    <div
      ref={ref}
      onWheel={onWheel}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      style={{ position: "absolute", inset: 0, overflow: "hidden", background: "#e8e4dc", cursor: drag.current ? "grabbing" : "grab", touchAction: "none", userSelect: "none" }}
      role="application"
      aria-label="Map of saved places"
    >
      {tiles.map((t) => (
        <img key={t.key} src={t.url} alt="" draggable={false} width={TILE} height={TILE}
          style={{ position: "absolute", left: t.x, top: t.y, width: TILE, height: TILE, pointerEvents: "none" }} />
      ))}
      {located.map((p) => {
        const pt = project(p.lat as number, p.lng as number, view.z);
        const x = pt.x - originX;
        const y = pt.y - originY;
        if (x < -40 || y < -40 || x > size.w + 40 || y > size.h + 40) return null;
        const sel = p.id === selectedId;
        const sz = sel ? 34 : 26;
        return (
          <button
            key={p.id}
            data-pin=""
            type="button"
            title={`${p.name} (${statusLabel(p.status)})`}
            onClick={(e) => { e.stopPropagation(); onSelect(p.id); }}
            style={{ position: "absolute", left: x - sz / 2, top: y - sz, width: sz, height: sz, padding: 0, border: "none", background: "transparent", cursor: "pointer", zIndex: sel ? 10 : 1 }}
          >
            <svg width={sz} height={sz} viewBox="0 0 24 24" aria-hidden="true">
              <path d="M12 23s8-7.2 8-13a8 8 0 1 0-16 0c0 5.8 8 13 8 13z" fill={statusColor(p.status)} stroke="#fff" strokeWidth={1.6} />
              <circle cx="12" cy="10" r="3" fill="#fff" />
            </svg>
          </button>
        );
      })}
      {located.length > 0 && (() => {
        const p = located.find((x) => x.id === selectedId);
        if (!p) return null;
        const pt = project(p.lat as number, p.lng as number, view.z);
        return (
          <div style={{ position: "absolute", left: pt.x - originX, top: pt.y - originY - 40, transform: "translate(-50%, -100%)", background: "#fff", color: "#111", fontSize: 12, fontWeight: 600, padding: "3px 8px", borderRadius: 6, boxShadow: "0 2px 8px rgba(0,0,0,.25)", whiteSpace: "nowrap", pointerEvents: "none", zIndex: 11 }}>
            {p.name}
          </div>
        );
      })()}
      <div data-ctl="" style={{ position: "absolute", left: 12, top: 12, display: "flex", flexDirection: "column", background: "#fff", borderRadius: 8, boxShadow: "0 2px 8px rgba(0,0,0,.2)", overflow: "hidden", zIndex: 15 }}>
        <button type="button" aria-label="Zoom in" onClick={() => zoomAt(1)} style={mapCtl}><Icon d={icons.plus} /></button>
        <button type="button" aria-label="Zoom out" onClick={() => zoomAt(-1)} style={{ ...mapCtl, borderTop: "1px solid #ddd" }}><Icon d={icons.minus} /></button>
      </div>
      {places.length > 0 && located.length < places.length && (
        <div style={{ position: "absolute", left: 12, bottom: 12, background: "rgba(255,255,255,.92)", color: "#333", fontSize: 11, padding: "3px 8px", borderRadius: 6, zIndex: 15 }}>
          {places.length - located.length} without a location not shown
        </div>
      )}
    </div>
  );
}

const mapCtl: CSSProperties = { width: 32, height: 32, border: "none", background: "#fff", color: "#222", cursor: "pointer", display: "flex", alignItems: "center", justifyContent: "center" };

// ─── format helpers ─────────────────────────────────────────────────

function kindLabel(kind: string): string {
  if (!kind) return "";
  return kind.replace(/_/g, " ").replace(/^\w/, (m) => m.toUpperCase());
}

function priceLabel(level: number | null | undefined): string {
  if (level == null) return "";
  return level === 0 ? "Free" : "€".repeat(level);
}
