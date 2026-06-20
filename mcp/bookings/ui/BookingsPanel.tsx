import { useCallback, useEffect, useMemo, useState } from "react";

const API = "/api/apps/bookings";

interface NativePanelProps {
  projectId: string;
}

interface BookingType {
  id: number;
  slug: string;
  title: string;
  description: string;
  duration_minutes: number;
  timezone: string;
  location_kind: "calls" | "phone" | "in_person" | "external_url";
  location_value: string;
  target_kind: "human" | "ai_agent" | "either" | "team";
  calendar_ids: number[];
  agent_instance_id: string;
  calls_enabled: boolean;
  crm_enabled: boolean;
  active: boolean;
  availability_rules: AvailabilityRules;
  intake_schema: unknown[];
  public_url?: string;
}

interface Booking {
  id: number;
  booking_type_id: number;
  status: string;
  start_at: string;
  end_at: string;
  invitee_name: string;
  invitee_email: string;
  invitee_phone: string;
  calendar_event_id?: number;
  calls_room_id?: number;
  calls_guest_join_url?: string;
  crm_contact_id?: number;
  assigned_target_kind: string;
}

interface Slot {
  start: string;
  end: string;
}

interface AvailabilityRules {
  minimum_notice_minutes?: number;
  booking_horizon_days?: number;
  buffer_before_minutes?: number;
  buffer_after_minutes?: number;
  working_hours?: Record<string, { start: string; end: string }>;
}

type DraftType = {
  title: string;
  slug: string;
  description: string;
  duration_minutes: number;
  target_kind: BookingType["target_kind"];
  location_kind: BookingType["location_kind"];
  location_value: string;
  calendar_ids: string;
  agent_instance_id: string;
  calls_enabled: boolean;
  crm_enabled: boolean;
  minimum_notice_minutes: number;
  booking_horizon_days: number;
  buffer_before_minutes: number;
  buffer_after_minutes: number;
  weekday_start: string;
  weekday_end: string;
};

const emptyDraft: DraftType = {
  title: "",
  slug: "",
  description: "",
  duration_minutes: 30,
  target_kind: "human",
  location_kind: "calls",
  location_value: "",
  calendar_ids: "",
  agent_instance_id: "",
  calls_enabled: true,
  crm_enabled: true,
  minimum_notice_minutes: 120,
  booking_horizon_days: 30,
  buffer_before_minutes: 0,
  buffer_after_minutes: 0,
  weekday_start: "09:00",
  weekday_end: "17:00",
};

export default function BookingsPanel({ projectId }: NativePanelProps) {
  const [types, setTypes] = useState<BookingType[]>([]);
  const [bookings, setBookings] = useState<Booking[]>([]);
  const [selectedId, setSelectedId] = useState(0);
  const [draft, setDraft] = useState<DraftType>(emptyDraft);
  const [editingId, setEditingId] = useState(0);
  const [slots, setSlots] = useState<Slot[]>([]);
  const [manual, setManual] = useState({ start_at: "", invitee_name: "", invitee_email: "", invitee_phone: "", notes: "" });
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);

  const selected = useMemo(
    () => types.find((t) => t.id === selectedId) ?? types[0] ?? null,
    [types, selectedId],
  );

  const withProject = useCallback((path: string) => {
    const sep = path.includes("?") ? "&" : "?";
    return `${API}${path}${sep}project_id=${encodeURIComponent(projectId)}`;
  }, [projectId]);

  const api = useCallback(async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
    const res = await fetch(withProject(path), {
      method,
      credentials: "same-origin",
      headers: body ? { "Content-Type": "application/json" } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) throw new Error(await res.text());
    return await res.json();
  }, [withProject]);

  const load = useCallback(async () => {
    try {
      const [typeData, bookingData] = await Promise.all([
        api<{ booking_types: BookingType[] }>("GET", "/booking-types?active=false"),
        api<{ bookings: Booking[] }>("GET", "/bookings?limit=100"),
      ]);
      setTypes(typeData.booking_types ?? []);
      setBookings(bookingData.bookings ?? []);
      if (!selectedId && typeData.booking_types?.length) setSelectedId(typeData.booking_types[0].id);
      setStatus(`${typeData.booking_types?.length ?? 0} types · ${bookingData.bookings?.length ?? 0} bookings`);
    } catch (e) {
      setStatus((e as Error).message);
    }
  }, [api, selectedId]);

  useEffect(() => { load(); }, [load]);

  useEffect(() => {
    if (!selected) return;
    setDraft(typeToDraft(selected));
    setEditingId(selected.id);
  }, [selected]);

  const saveType = async () => {
    if (!draft.title.trim()) return;
    setBusy(true);
    try {
      const body = draftToPayload(draft);
      if (editingId) {
        await api("PATCH", `/booking-types/${editingId}`, body);
      } else {
        const data = await api<{ booking_type: BookingType }>("POST", "/booking-types", body);
        setSelectedId(data.booking_type.id);
      }
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const newType = () => {
    setEditingId(0);
    setSelectedId(0);
    setDraft(emptyDraft);
    setSlots([]);
  };

  const archiveType = async () => {
    if (!editingId) return;
    setBusy(true);
    try {
      await api("DELETE", `/booking-types/${editingId}`);
      setSelectedId(0);
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const previewSlots = async () => {
    if (!editingId) return;
    setBusy(true);
    try {
      const data = await api<{ slots: Slot[] }>("GET", `/public/${encodeURIComponent(draft.slug || selected?.slug || "")}/slots?limit=12`);
      setSlots(data.slots ?? []);
      setStatus(`${data.slots?.length ?? 0} slots`);
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const createManualBooking = async () => {
    if (!selected || !manual.start_at || !manual.invitee_email.trim()) return;
    setBusy(true);
    try {
      await api("POST", "/bookings", {
        booking_type_id: selected.id,
        start_at: new Date(manual.start_at).toISOString(),
        invitee_name: manual.invitee_name,
        invitee_email: manual.invitee_email,
        invitee_phone: manual.invitee_phone,
        intake_answers: { notes: manual.notes },
        source: "panel",
      });
      setManual({ start_at: "", invitee_name: "", invitee_email: "", invitee_phone: "", notes: "" });
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const cancelBooking = async (booking: Booking) => {
    setBusy(true);
    try {
      await api("POST", `/bookings/${booking.id}/cancel`, { reason: "cancelled from panel" });
      await load();
    } catch (e) {
      setStatus((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="h-full flex flex-col bg-bg text-text">
      <header className="px-4 py-3 border-b border-border flex items-center gap-2">
        <h1 className="text-sm font-semibold">Bookings</h1>
        <button type="button" onClick={newType} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">New</button>
        <button type="button" onClick={load} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">Refresh</button>
        <span className="ml-auto text-xs text-text-dim truncate">{status}</span>
      </header>

      <div className="flex-1 min-h-0 grid grid-cols-[280px_minmax(0,1fr)]">
        <aside className="border-r border-border overflow-y-auto">
          {types.length === 0 ? (
            <div className="p-4 text-xs text-text-muted">No booking types yet.</div>
          ) : (
            <ul>
              {types.map((type) => (
                <li key={type.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedId(type.id)}
                    className={`w-full px-3 py-2 text-left border-b border-border hover:bg-bg-input/50 ${selected?.id === type.id ? "bg-bg-input" : ""}`}
                  >
                    <div className="flex items-center gap-2">
                      <StatusDot active={type.active} />
                      <span className="text-sm truncate">{type.title}</span>
                    </div>
                    <div className="text-[11px] text-text-dim mt-0.5 truncate">{type.slug} · {type.duration_minutes}m · {type.target_kind}</div>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <main className="min-w-0 overflow-y-auto">
          <div className="p-4 grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_380px] gap-5">
            <section className="space-y-4">
              <Editor draft={draft} setDraft={setDraft} />
              <div className="flex flex-wrap gap-2">
                <button type="button" onClick={saveType} disabled={busy || !draft.title.trim()} className="px-3 py-1.5 text-xs bg-accent text-bg rounded disabled:opacity-50">
                  {editingId ? "Save" : "Create"}
                </button>
                <button type="button" onClick={previewSlots} disabled={busy || !editingId} className="px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input disabled:opacity-50">
                  Preview slots
                </button>
                {editingId > 0 && (
                  <button type="button" onClick={archiveType} disabled={busy} className="px-3 py-1.5 text-xs border border-red text-red rounded hover:bg-red/10 disabled:opacity-50">
                    Archive
                  </button>
                )}
              </div>

              {selected?.public_url && (
                <div className="border border-border rounded p-3">
                  <div className="text-xs text-text-dim mb-1">Public link</div>
                  <div className="flex gap-2">
                    <input readOnly value={selected.public_url} className="flex-1 bg-bg-input border border-border rounded px-2 py-1 text-xs text-text" />
                    <button type="button" onClick={() => navigator.clipboard?.writeText(selected.public_url || "")} className="px-2 py-1 text-xs border border-border rounded hover:bg-bg-input">Copy</button>
                  </div>
                </div>
              )}

              <PanelSection title="Slot Preview">
                {slots.length === 0 ? (
                  <Empty text="Preview slots for the selected booking type." />
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                    {slots.map((slot) => (
                      <button
                        key={slot.start}
                        type="button"
                        onClick={() => setManual((m) => ({ ...m, start_at: localInputValue(slot.start) }))}
                        className="text-left px-3 py-2 border border-border rounded hover:bg-bg-input"
                      >
                        <div className="text-sm">{formatDate(slot.start)}</div>
                        <div className="text-xs text-text-dim">{formatTime(slot.start)} - {formatTime(slot.end)}</div>
                      </button>
                    ))}
                  </div>
                )}
              </PanelSection>
            </section>

            <aside className="space-y-4">
              <PanelSection title="Manual Booking">
                <div className="space-y-2">
                  <Field label="Start">
                    <input type="datetime-local" value={manual.start_at} onChange={(e) => setManual((m) => ({ ...m, start_at: e.target.value }))} className="input" />
                  </Field>
                  <Field label="Name">
                    <input value={manual.invitee_name} onChange={(e) => setManual((m) => ({ ...m, invitee_name: e.target.value }))} className="input" />
                  </Field>
                  <Field label="Email">
                    <input value={manual.invitee_email} onChange={(e) => setManual((m) => ({ ...m, invitee_email: e.target.value }))} className="input" />
                  </Field>
                  <Field label="Phone">
                    <input value={manual.invitee_phone} onChange={(e) => setManual((m) => ({ ...m, invitee_phone: e.target.value }))} className="input" />
                  </Field>
                  <Field label="Notes">
                    <textarea value={manual.notes} onChange={(e) => setManual((m) => ({ ...m, notes: e.target.value }))} rows={3} className="input" />
                  </Field>
                  <button type="button" onClick={createManualBooking} disabled={busy || !selected || !manual.start_at || !manual.invitee_email.trim()} className="w-full px-3 py-1.5 text-xs bg-accent text-bg rounded disabled:opacity-50">
                    Book
                  </button>
                </div>
              </PanelSection>

              <PanelSection title="Recent Bookings">
                {bookings.length === 0 ? (
                  <Empty text="No bookings yet." />
                ) : (
                  <ul className="divide-y divide-border border border-border rounded">
                    {bookings.map((booking) => (
                      <li key={booking.id} className="p-2">
                        <div className="flex items-start gap-2">
                          <div className="min-w-0 flex-1">
                            <div className="text-sm truncate">{booking.invitee_name || booking.invitee_email || "Guest"}</div>
                            <div className="text-xs text-text-dim">{formatDate(booking.start_at)} · {booking.status}</div>
                            <div className="text-[11px] text-text-dim truncate">
                              {typeTitle(types, booking.booking_type_id)}{booking.calls_room_id ? " · call room" : ""}{booking.crm_contact_id ? " · CRM" : ""}
                            </div>
                          </div>
                          {booking.status !== "cancelled" && (
                            <button type="button" onClick={() => cancelBooking(booking)} className="text-xs text-red hover:underline">Cancel</button>
                          )}
                        </div>
                        {booking.calls_guest_join_url && (
                          <a href={booking.calls_guest_join_url} target="_blank" rel="noreferrer" className="mt-1 inline-block text-xs text-accent hover:underline">Join link</a>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </PanelSection>
            </aside>
          </div>
        </main>
      </div>
      <style>{`.input{width:100%;background:var(--color-bg-input);border:1px solid var(--color-border);border-radius:4px;padding:6px 8px;font-size:13px;color:var(--color-text)}`}</style>
    </div>
  );
}

function Editor({ draft, setDraft }: { draft: DraftType; setDraft: (fn: DraftType | ((d: DraftType) => DraftType)) => void }) {
  const update = (patch: Partial<DraftType>) => setDraft((d) => ({ ...d, ...patch }));
  return (
    <PanelSection title="Booking Type">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <Field label="Title">
          <input value={draft.title} onChange={(e) => update({ title: e.target.value })} className="input" />
        </Field>
        <Field label="Slug">
          <input value={draft.slug} onChange={(e) => update({ slug: e.target.value })} className="input" />
        </Field>
        <Field label="Duration">
          <input type="number" min={5} step={5} value={draft.duration_minutes} onChange={(e) => update({ duration_minutes: parseInt(e.target.value) || 30 })} className="input" />
        </Field>
        <Field label="Calendars">
          <input value={draft.calendar_ids} onChange={(e) => update({ calendar_ids: e.target.value })} placeholder="1, 2, 3" className="input" />
        </Field>
        <Field label="Target">
          <select value={draft.target_kind} onChange={(e) => update({ target_kind: e.target.value as DraftType["target_kind"] })} className="input">
            <option value="human">human</option>
            <option value="ai_agent">ai agent</option>
            <option value="either">either</option>
            <option value="team">team</option>
          </select>
        </Field>
        <Field label="Agent ID">
          <input value={draft.agent_instance_id} onChange={(e) => update({ agent_instance_id: e.target.value })} className="input" />
        </Field>
        <Field label="Location">
          <select value={draft.location_kind} onChange={(e) => update({ location_kind: e.target.value as DraftType["location_kind"], calls_enabled: e.target.value === "calls" })} className="input">
            <option value="calls">Calls</option>
            <option value="phone">Phone</option>
            <option value="external_url">External URL</option>
            <option value="in_person">In person</option>
          </select>
        </Field>
        <Field label="Location Value">
          <input value={draft.location_value} onChange={(e) => update({ location_value: e.target.value })} className="input" />
        </Field>
        <Field label="Weekday Start">
          <input type="time" value={draft.weekday_start} onChange={(e) => update({ weekday_start: e.target.value })} className="input" />
        </Field>
        <Field label="Weekday End">
          <input type="time" value={draft.weekday_end} onChange={(e) => update({ weekday_end: e.target.value })} className="input" />
        </Field>
        <Field label="Minimum Notice">
          <input type="number" min={0} value={draft.minimum_notice_minutes} onChange={(e) => update({ minimum_notice_minutes: parseInt(e.target.value) || 0 })} className="input" />
        </Field>
        <Field label="Horizon Days">
          <input type="number" min={1} value={draft.booking_horizon_days} onChange={(e) => update({ booking_horizon_days: parseInt(e.target.value) || 30 })} className="input" />
        </Field>
        <Field label="Buffer Before">
          <input type="number" min={0} value={draft.buffer_before_minutes} onChange={(e) => update({ buffer_before_minutes: parseInt(e.target.value) || 0 })} className="input" />
        </Field>
        <Field label="Buffer After">
          <input type="number" min={0} value={draft.buffer_after_minutes} onChange={(e) => update({ buffer_after_minutes: parseInt(e.target.value) || 0 })} className="input" />
        </Field>
        <label className="flex items-center gap-2 text-xs text-text-muted">
          <input type="checkbox" checked={draft.calls_enabled} onChange={(e) => update({ calls_enabled: e.target.checked })} />
          Create Calls room
        </label>
        <label className="flex items-center gap-2 text-xs text-text-muted">
          <input type="checkbox" checked={draft.crm_enabled} onChange={(e) => update({ crm_enabled: e.target.checked })} />
          Link CRM contact
        </label>
      </div>
      <Field label="Description">
        <textarea value={draft.description} onChange={(e) => update({ description: e.target.value })} rows={3} className="input" />
      </Field>
    </PanelSection>
  );
}

function typeToDraft(type: BookingType): DraftType {
  const rules = type.availability_rules || {};
  const wh = rules.working_hours?.mon || { start: "09:00", end: "17:00" };
  return {
    title: type.title,
    slug: type.slug,
    description: type.description || "",
    duration_minutes: type.duration_minutes || 30,
    target_kind: type.target_kind || "human",
    location_kind: type.location_kind || "calls",
    location_value: type.location_value || "",
    calendar_ids: (type.calendar_ids || []).join(", "),
    agent_instance_id: type.agent_instance_id || "",
    calls_enabled: Boolean(type.calls_enabled),
    crm_enabled: Boolean(type.crm_enabled),
    minimum_notice_minutes: rules.minimum_notice_minutes ?? 120,
    booking_horizon_days: rules.booking_horizon_days ?? 30,
    buffer_before_minutes: rules.buffer_before_minutes ?? 0,
    buffer_after_minutes: rules.buffer_after_minutes ?? 0,
    weekday_start: wh.start || "09:00",
    weekday_end: wh.end || "17:00",
  };
}

function draftToPayload(draft: DraftType) {
  const workingHours = ["mon", "tue", "wed", "thu", "fri"].reduce<Record<string, { start: string; end: string }>>((acc, day) => {
    acc[day] = { start: draft.weekday_start, end: draft.weekday_end };
    return acc;
  }, {});
  return {
    title: draft.title.trim(),
    slug: draft.slug.trim(),
    description: draft.description,
    duration_minutes: draft.duration_minutes,
    target_kind: draft.target_kind,
    location_kind: draft.location_kind,
    location_value: draft.location_value,
    calendar_ids: draft.calendar_ids.split(",").map((s) => parseInt(s.trim(), 10)).filter(Boolean),
    agent_instance_id: draft.agent_instance_id,
    calls_enabled: draft.calls_enabled,
    crm_enabled: draft.crm_enabled,
    availability_rules: {
      minimum_notice_minutes: draft.minimum_notice_minutes,
      booking_horizon_days: draft.booking_horizon_days,
      buffer_before_minutes: draft.buffer_before_minutes,
      buffer_after_minutes: draft.buffer_after_minutes,
      working_hours: workingHours,
    },
  };
}

function PanelSection({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="text-xs uppercase tracking-wide text-text-dim font-medium mb-2">{title}</h2>
      <div className="space-y-3">{children}</div>
    </section>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block text-xs text-text-muted">
      {label}
      <div className="mt-1">{children}</div>
    </label>
  );
}

function Empty({ text }: { text: string }) {
  return <div className="border border-border rounded p-3 text-sm text-text-muted">{text}</div>;
}

function StatusDot({ active }: { active: boolean }) {
  return <span className={`w-2 h-2 rounded-full ${active ? "bg-success" : "bg-text-dim"}`} />;
}

function formatDate(value: string) {
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(new Date(value));
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" }).format(new Date(value));
}

function localInputValue(value: string) {
  const d = new Date(value);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function typeTitle(types: BookingType[], id: number) {
  return types.find((t) => t.id === id)?.title || `Type ${id}`;
}
