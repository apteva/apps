import React, { useCallback, useEffect, useMemo, useState } from "react";

const h = React.createElement;
const API = "/api/apps/events";

export default function EventsPanel({ projectId = "" } = {}) {
  const [tab, setTab] = useState("events");
  const [events, setEvents] = useState([]);
  const [venues, setVenues] = useState([]);
  const [selectedId, setSelectedId] = useState(0);
  const [applications, setApplications] = useState([]);
  const [tickets, setTickets] = useState([]);
  const [slots, setSlots] = useState([]);
  const [ticketTypes, setTicketTypes] = useState([]);
  const [message, setMessage] = useState("");
  const [editing, setEditing] = useState(null);

  const selected = useMemo(() => events.find((e) => e.id === selectedId) || events[0], [events, selectedId]);
  const eventId = selected?.id || 0;

  const request = useCallback(async (path, opts) => {
    const res = await fetch(API + path + (projectId ? `${path.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(projectId)}` : ""), {
      credentials: "same-origin",
      headers: opts?.body ? { "Content-Type": "application/json" } : undefined,
      ...opts,
    });
    if (!res.ok) throw new Error(await res.text());
    return res.json();
  }, [projectId]);

  const loadEvents = useCallback(async () => {
    try {
      const data = await request("/shows");
      setEvents(data || []);
      if (!selectedId && data?.[0]) setSelectedId(data[0].id);
    } catch (e) {
      setMessage("Events: " + e.message);
    }
  }, [request, selectedId]);

  const loadVenues = useCallback(async () => {
    try { setVenues(await request("/venues") || []); } catch {}
  }, [request]);

  const loadDetail = useCallback(async () => {
    if (!eventId) return;
    try {
      const [apps, ts, sl, tt] = await Promise.all([
        request(`/applications?event_id=${eventId}`),
        request(`/tickets?event_id=${eventId}`),
        request(`/slots?event_id=${eventId}`),
        request(`/ticket_types?event_id=${eventId}`),
      ]);
      setApplications(apps || []);
      setTickets(ts || []);
      setSlots(sl || []);
      setTicketTypes(tt || []);
      setMessage("");
    } catch (e) {
      setMessage("Detail: " + e.message);
    }
  }, [eventId, request]);

  useEffect(() => { loadEvents(); loadVenues(); }, [loadEvents, loadVenues]);
  useEffect(() => { loadDetail(); }, [loadDetail]);

  const refresh = () => { loadEvents(); loadVenues(); loadDetail(); };

  const createEvent = () => setEditing({ timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", status: "draft", visibility: "private" });
  const saveEvent = async (values) => {
    const saved = await request(editing.id ? `/shows/${editing.id}` : "/shows", {
      method: editing.id ? "PATCH" : "POST", body: JSON.stringify(values),
    });
    setEvents(current => [saved, ...current.filter(event => event.id !== saved.id)]);
    setSelectedId(saved.id);
    setEditing(null);
    setMessage("Event saved");
  };

  const publishEvent = async (visibility) => {
    if (!eventId) return;
    try {
      await request(`/shows/${eventId}`, { method: "PATCH", body: JSON.stringify({ status: "published", visibility }) });
      refresh();
    } catch (e) {
      setMessage("Publish: " + e.message);
    }
  };

  const createVenue = async () => {
    const name = prompt("Venue name");
    if (!name) return;
    try {
      await request("/venues", { method: "POST", body: JSON.stringify({ name }) });
      loadVenues();
    } catch (e) {
      setMessage("Venue: " + e.message);
    }
  };

  const createTicketType = async () => {
    if (!eventId) return;
    const name = prompt("Ticket type name", "General admission");
    if (!name) return;
    try {
      await request("/ticket_types", { method: "POST", body: JSON.stringify({ event_id: eventId, name, price_cents: 0, currency: "USD" }) });
      loadDetail();
    } catch (e) {
      setMessage("Ticket type: " + e.message);
    }
  };

  const issueTicket = async () => {
    if (!eventId) return;
    const ticketTypeId = ticketTypes.length ? Number(prompt("Ticket type ID:\n" + ticketTypes.map(t => `${t.id}: ${t.name} (${t.currency} ${(t.price_cents / 100).toFixed(2)})`).join("\n"), String(ticketTypes[0].id))) : 0;
    if (ticketTypes.length && !ticketTypes.some(t => t.id === ticketTypeId)) return;
    const buyer_name = prompt("Buyer name");
    if (!buyer_name) return;
    const buyer_email = prompt("Buyer email") || "";
    try {
      await request("/tickets", { method: "POST", body: JSON.stringify({ event_id: eventId, buyer_name, buyer_email, quantity: 1, ticket_type_id: ticketTypeId }) });
      loadDetail(); loadEvents();
    } catch (e) {
      setMessage("Ticket: " + e.message);
    }
  };

  const submitApplication = async () => {
    if (!eventId) return;
    const applicant_name = prompt("Applicant name");
    if (!applicant_name) return;
    const email = prompt("Email") || "";
    const stage_name = prompt("Stage name", applicant_name) || "";
    try {
      await request("/applications", { method: "POST", body: JSON.stringify({ event_id: eventId, applicant_name, email, stage_name }) });
      loadDetail(); loadEvents();
    } catch (e) {
      setMessage("Application: " + e.message);
    }
  };

  const review = async (app, status) => {
    try {
      await request(`/applications/${app.id}`, { method: "PATCH", body: JSON.stringify({ status }) });
      loadDetail(); loadEvents();
    } catch (e) {
      setMessage("Review: " + e.message);
    }
  };

  const schedule = async (app) => {
    const starts_at = prompt("Starts at (RFC3339 or local text)", selected?.starts_at || "");
    if (starts_at === null) return;
    try {
      await request("/slots", { method: "POST", body: JSON.stringify({ event_id: eventId, application_id: app.id, starts_at }) });
      loadDetail(); loadEvents();
    } catch (e) {
      setMessage("Schedule: " + e.message);
    }
  };

  const checkIn = async (ticket) => {
    try {
      await request(`/tickets/${ticket.id}/check_in`, { method: "POST" });
      loadDetail();
    } catch (e) {
      setMessage("Check-in: " + e.message);
    }
  };

  return h("div", { className: "h-full flex flex-col bg-bg text-text" },
    h("header", { className: "border-b border-border px-4 py-2 flex flex-wrap items-center gap-3" },
      h("strong", null, "Events"),
      h("select", { value: eventId, onChange: (e) => setSelectedId(Number(e.target.value)), className: "bg-bg-input border border-border rounded px-2 py-1 text-sm min-w-64" },
        events.map((e) => h("option", { key: e.id, value: e.id }, e.title))),
      h("button", { onClick: createEvent, className: "border border-border rounded px-3 py-1 text-sm" }, "+ Event"),
      selected && h("button", { onClick: () => setEditing(selected), className: "border border-border rounded px-3 py-1 text-sm" }, "Edit event"),
      h("button", { onClick: refresh, className: "border border-border rounded px-3 py-1 text-sm" }, "Refresh"),
      h("span", { className: "ml-auto text-xs text-text-dim truncate" }, message || selected?.slug || "")
    ),
    h("div", { className: "border-b border-border px-4 py-2 flex gap-2 text-sm" },
      ["events", "applications", "schedule", "tickets", "setup"].map((t) =>
        h("button", { key: t, onClick: () => setTab(t), className: `px-3 py-1 rounded ${tab === t ? "bg-accent text-bg" : "text-text-dim hover:text-text"}` }, label(t)))
    ),
    editing && h(EventEditor, { key: editing.id || "new", event: editing, venues, save: saveEvent, cancel: () => setEditing(null) }),
    h("main", { className: "flex-1 overflow-auto p-4" },
      !selected ? h(Empty, { text: "No events yet." }) :
      tab === "events" ? h(EventsTab, { events, selected, setSelectedId, publishEvent, projectId }) :
      tab === "applications" ? h(ApplicationsTab, { applications, submitApplication, review, schedule }) :
      tab === "schedule" ? h(ScheduleTab, { slots }) :
      tab === "tickets" ? h(TicketsTab, { tickets, issueTicket, checkIn }) :
      h(SetupTab, { event: selected, venues, ticketTypes, createVenue, createTicketType })
    )
  );
}

function EventsTab({ events, selected, setSelectedId, publishEvent, projectId }) {
  return h("div", { className: "grid grid-cols-1 lg:grid-cols-[280px_1fr] gap-4 h-full" },
    h("section", { className: "border border-border rounded overflow-auto" },
      events.map((e) => h("button", { key: e.id, onClick: () => setSelectedId(e.id), className: `block w-full text-left px-3 py-2 border-b border-border last:border-b-0 ${selected.id === e.id ? "bg-bg-elev" : "hover:bg-bg-elev/60"}` },
        h("div", { className: "font-medium truncate" }, e.title),
        h("div", { className: "text-xs text-text-dim flex gap-2" }, h("span", null, e.status), h("span", null, e.visibility), h("span", null, shortDate(e.starts_at)))))
    ),
    h("section", { className: "border border-border rounded p-4" },
      h("div", { className: "flex items-start gap-3" },
        h("div", { className: "flex-1" },
          h("h2", { className: "text-xl font-semibold" }, selected.title),
          h("div", { className: "text-sm text-text-dim mt-1" }, selected.description || "No description"),
          h("div", { className: "grid grid-cols-3 gap-3 mt-4" },
            h(Stat, { label: "Tickets", value: selected.ticket_count }),
            h(Stat, { label: "Applications", value: selected.application_count }),
            h(Stat, { label: "Slots", value: selected.slot_count }))),
        h("div", { className: "flex gap-2" },
          h("button", { onClick: () => publishEvent("private"), className: "border border-border rounded px-3 py-1 text-sm" }, "Publish private"),
          h("button", { onClick: () => publishEvent("public"), className: "bg-accent text-bg rounded px-3 py-1 text-sm" }, "Publish public"))),
      h("div", { className: "mt-5 grid grid-cols-2 gap-3 text-sm" },
        h(Field, { label: "Slug", value: selected.slug }),
        h(Field, { label: "Timezone", value: selected.timezone }),
        h(Field, { label: "Starts", value: selected.starts_at || "-" }),
        h(Field, { label: "Ends", value: selected.ends_at || "-" }),
        h(Field, { label: "Capacity", value: selected.capacity || "unlimited" }),
        h("div", { className: "border border-border rounded p-3 min-w-0" },
          h("div", { className: "text-xs text-text-dim mb-1" }, "Attendee page"),
          selected.status === "published" && selected.visibility === "public"
            ? h("a", { href: `${API}/public/${encodeURIComponent(selected.slug)}${projectId ? "?project_id=" + encodeURIComponent(projectId) : ""}`, target: "_blank", rel: "noopener noreferrer", className: "text-accent underline" }, "Open public event page")
            : h("span", null, "Publish publicly to share")))
    )
  );
}

function ApplicationsTab({ applications, submitApplication, review, schedule }) {
  return h("section", { className: "border border-border rounded" },
    h("div", { className: "px-3 py-2 border-b border-border flex items-center" },
      h("strong", null, `Applications (${applications.length})`),
      h("button", { onClick: submitApplication, className: "ml-auto bg-accent text-bg rounded px-3 py-1 text-sm" }, "+ Application")),
    applications.length ? applications.map((a) => h("div", { key: a.id, className: "px-3 py-2 border-b border-border last:border-b-0" },
      h("div", { className: "flex items-center gap-2" },
        h("span", { className: "font-medium" }, a.stage_name || a.applicant_name),
        h("span", { className: "text-xs text-text-dim" }, a.email),
        h(Status, { value: a.status }),
        h("span", { className: "ml-auto" }),
        h("button", { onClick: () => review(a, "shortlisted"), className: "text-xs border border-border rounded px-2 py-1" }, "Shortlist"),
        h("button", { onClick: () => review(a, "accepted"), className: "text-xs border border-border rounded px-2 py-1" }, "Accept"),
        h("button", { onClick: () => schedule(a), className: "text-xs bg-accent text-bg rounded px-2 py-1" }, "Schedule"),
        h("button", { onClick: () => review(a, "rejected"), className: "text-xs text-error px-2 py-1" }, "Reject")),
      h("div", { className: "text-xs text-text-dim mt-1 line-clamp-2" }, a.bio || a.notes || "No notes")))
    : h(Empty, { text: "No performer applications yet." })
  );
}

function ScheduleTab({ slots }) {
  return h("section", { className: "border border-border rounded" },
    h("div", { className: "px-3 py-2 border-b border-border font-medium" }, `Schedule (${slots.length})`),
    slots.length ? slots.map((s) => h("div", { key: s.id, className: "px-3 py-2 border-b border-border last:border-b-0 flex gap-3" },
      h("div", { className: "w-44 text-xs text-text-dim" }, shortDate(s.starts_at) || "unscheduled"),
      h("div", { className: "font-medium" }, s.performer_name),
      h("div", { className: "text-text-dim text-sm" }, s.title)))
    : h(Empty, { text: "No scheduled slots yet." })
  );
}

function TicketsTab({ tickets, issueTicket, checkIn }) {
  return h("section", { className: "border border-border rounded" },
    h("div", { className: "px-3 py-2 border-b border-border flex items-center" },
      h("strong", null, `Door list (${tickets.length})`),
      h("button", { onClick: issueTicket, className: "ml-auto bg-accent text-bg rounded px-3 py-1 text-sm" }, "+ Ticket")),
    tickets.length ? tickets.map((t) => h("div", { key: t.id, className: "px-3 py-2 border-b border-border last:border-b-0 flex items-center gap-3" },
      h("div", { className: "flex-1 min-w-0" },
        h("div", { className: "font-medium truncate" }, t.attendee_name),
        h("div", { className: "text-xs text-text-dim truncate" }, `${t.attendee_email || "-"} · ${t.code}`)),
      h(Status, { value: t.checkin_status }),
      h("button", { onClick: () => checkIn(t), disabled: t.checkin_status === "checked_in", className: "border border-border rounded px-3 py-1 text-sm disabled:opacity-50" }, "Check in")))
    : h(Empty, { text: "No tickets yet." })
  );
}

function SetupTab({ event, venues, ticketTypes, createVenue, createTicketType }) {
  return h("div", { className: "grid grid-cols-2 gap-4" },
    h("section", { className: "border border-border rounded" },
      h("div", { className: "px-3 py-2 border-b border-border flex items-center" }, h("strong", null, "Venues"), h("button", { onClick: createVenue, className: "ml-auto border border-border rounded px-2 py-1 text-xs" }, "+ Venue")),
      venues.length ? venues.map((v) => h("div", { key: v.id, className: "px-3 py-2 border-b border-border last:border-b-0" }, h("div", { className: "font-medium" }, v.name), h("div", { className: "text-xs text-text-dim" }, v.city || v.address || "No address"))) : h(Empty, { text: "No venues." })),
    h("section", { className: "border border-border rounded" },
      h("div", { className: "px-3 py-2 border-b border-border flex items-center" }, h("strong", null, `Ticket types for ${event.title}`), h("button", { onClick: createTicketType, className: "ml-auto border border-border rounded px-2 py-1 text-xs" }, "+ Type")),
      ticketTypes.length ? ticketTypes.map((t) => h("div", { key: t.id, className: "px-3 py-2 border-b border-border last:border-b-0 flex" }, h("span", { className: "font-medium" }, t.name), h("span", { className: "ml-auto text-xs text-text-dim" }, t.price_cents ? `${t.currency} ${(t.price_cents / 100).toFixed(2)}` : "free"))) : h(Empty, { text: "No ticket types." }))
  );
}

function Stat({ label, value }) {
  return h("div", { className: "border border-border rounded p-3" }, h("div", { className: "text-2xl font-semibold" }, value || 0), h("div", { className: "text-xs text-text-dim" }, label));
}

function Field({ label, value }) {
  return h("div", { className: "border border-border rounded p-3 min-w-0" }, h("div", { className: "text-xs text-text-dim mb-1" }, label), h("div", { className: "truncate" }, String(value ?? "")));
}

function Status({ value }) {
  return h("span", { className: "text-xs px-2 py-0.5 rounded border border-border text-text-dim" }, value);
}

function Empty({ text }) {
  return h("div", { className: "text-sm text-text-dim italic p-4" }, text);
}

function label(t) {
  return t.charAt(0).toUpperCase() + t.slice(1);
}

function shortDate(v) {
  if (!v) return "";
  const d = new Date(v);
  return Number.isFinite(d.getTime()) ? d.toLocaleString() : v;
}


function localDate(value, timezone) {
  if (!value) return "";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "";
  try {
    const parts = new Intl.DateTimeFormat("en-CA", { timeZone: timezone || "UTC", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }).formatToParts(date);
    const p = Object.fromEntries(parts.map(part => [part.type, part.value]));
    return `${p.year}-${p.month}-${p.day}T${p.hour}:${p.minute}:${p.second}`;
  } catch { return ""; }
}

function EventEditor({ event, venues, save, cancel }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const inputClass = "w-full bg-bg-input text-text border border-border rounded px-3 py-2 text-sm";
  const field = (name, title, type = "text", value = event[name] || "", extra = {}) =>
    h("label", { className: "block text-sm" }, h("span", { className: "block mb-1 text-text-dim" }, title),
      h("input", { name, type, defaultValue: value, className: inputClass, ...extra }));
  const select = (name, title, options, value) =>
    h("label", { className: "block text-sm" }, h("span", { className: "block mb-1 text-text-dim" }, title),
      h("select", { name, defaultValue: value, className: inputClass, style: { color: "inherit" } }, options.map(([id, label]) => h("option", { key: id, value: id }, label))));
  return h("div", { className: "fixed inset-0 z-50 bg-black/50 flex items-center justify-center p-4", role: "dialog", "aria-modal": true, "aria-labelledby": "event-editor-title", onKeyDown: e => { if (e.key === "Escape" && !busy) cancel(); } },
    h("form", { className: "bg-bg text-text border border-border rounded-xl p-6 w-full max-w-2xl max-h-[90vh] overflow-auto", onSubmit: async e => {
      e.preventDefault(); if (busy) return;
      setBusy(true); setError("");
      const values = Object.fromEntries(new FormData(e.currentTarget));
      values.capacity = Number(values.capacity); values.venue_id = Number(values.venue_id);
      try { await save(values); } catch (err) { setError(err.message); setBusy(false); }
    } },
      h("h2", { id: "event-editor-title", className: "text-xl font-semibold mb-5" }, event.id ? "Edit event" : "Create event"),
      h("fieldset", { disabled: busy, className: "space-y-4" },
        field("title", "Event title", "text", event.title || "", { required: true, autoFocus: true }),
        h("label", { className: "block text-sm" }, h("span", { className: "block mb-1 text-text-dim" }, "Description"), h("textarea", { name: "description", defaultValue: event.description || "", rows: 4, className: inputClass })),
        field("timezone", "Timezone (dates below use this timezone)", "text", event.timezone || "UTC", { required: true, placeholder: "Europe/Madrid" }),
        h("div", { className: "grid grid-cols-1 sm:grid-cols-2 gap-4" },
          field("starts_at", "Starts", "datetime-local", localDate(event.starts_at, event.timezone), { step: 1 }),
          field("ends_at", "Ends", "datetime-local", localDate(event.ends_at, event.timezone), { step: 1 }),
          field("capacity", "Capacity (0 = unlimited)", "number", event.capacity || 0, { min: 0, step: 1, required: true }),
          select("venue_id", "Venue", [[0, "No venue"], ...venues.map(v => [v.id, v.name])], event.venue_id || 0),
          select("status", "Status", ["draft", "published", "closed", "archived"].map(v => [v, label(v)]), event.status || "draft"),
          select("visibility", "Visibility", [["private", "Private"], ["public", "Public"]], event.visibility || "private")),
        field("external_checkout_url", "External checkout URL (optional)", "url", event.external_checkout_url || "", { placeholder: "https://…" }),
        h("p", { className: "text-xs text-text-dim" }, "An external checkout link replaces free public registration. Paid tickets must be handled by that checkout; no payment is collected here."),
        error && h("p", { role: "alert", className: "text-sm text-error" }, error),
        h("div", { className: "flex justify-end gap-3 pt-3" },
          h("button", { type: "button", onClick: cancel, className: "border border-border rounded px-4 py-2 text-sm" }, "Cancel"),
          h("button", { type: "submit", className: "bg-accent text-bg rounded px-4 py-2 text-sm" }, busy ? "Saving…" : "Save event"))
      )
    )
  );
}
