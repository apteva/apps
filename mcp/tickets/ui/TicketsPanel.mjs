// ui/TicketsPanel.tsx
import { useCallback, useEffect, useMemo, useState } from "react";
import { jsxDEV, Fragment } from "react/jsx-dev-runtime";
var API = "/api/apps/tickets";
var statuses = ["new", "acknowledged", "planned", "in_progress", "waiting_client", "resolved", "closed"];
var types = ["feedback", "bug", "feature", "change_request", "question", "support"];
var priorities = ["low", "normal", "high", "urgent"];
function TicketsPanel({ projectId }) {
  const [tickets, setTickets] = useState([]), [areas, setAreas] = useState([]);
  const [selectedId, setSelectedId] = useState(0), [detail, setDetail] = useState(null);
  const [query, setQuery] = useState(""), [statusFilter, setStatusFilter] = useState(""), [areaFilter, setAreaFilter] = useState("");
  const [message, setMessage] = useState(""), [busy, setBusy] = useState(false), [creating, setCreating] = useState(false), [showAreas, setShowAreas] = useState(false);
  const [portal, setPortal] = useState(null), [composer, setComposer] = useState(""), [internal, setInternal] = useState(false);
  const [draft, setDraft] = useState(emptyDraft());
  const withProject = useCallback((path) => `${API}${path}${path.includes("?") ? "&" : "?"}project_id=${encodeURIComponent(projectId)}`, [projectId]);
  const loadAreas = useCallback(async () => {
    const r = await fetch(withProject("/areas"), { credentials: "same-origin" });
    if (r.ok)
      setAreas((await r.json()).areas ?? []);
  }, [withProject]);
  const loadPortal = useCallback(async () => {
    const r = await fetch(withProject("/portal"), { credentials: "same-origin" });
    if (r.ok)
      setPortal((await r.json()).portal ?? null);
  }, [withProject]);
  const loadTickets = useCallback(async () => {
    const p = new URLSearchParams;
    if (query.trim())
      p.set("q", query.trim());
    if (statusFilter)
      p.set("status", statusFilter);
    if (areaFilter)
      p.set("area", areaFilter);
    try {
      const r = await fetch(withProject(`/tickets?${p}`), { credentials: "same-origin" });
      if (!r.ok)
        throw new Error(await r.text());
      const out = await r.json();
      const rows = out.tickets ?? [];
      setTickets(rows);
      setSelectedId((current) => current && rows.some((t) => t.id === current) ? current : rows[0]?.id ?? 0);
      setMessage(`${out.total ?? rows.length} ticket${(out.total ?? rows.length) === 1 ? "" : "s"}`);
    } catch (e) {
      setMessage(e.message);
    }
  }, [areaFilter, query, statusFilter, withProject]);
  const loadDetail = useCallback(async (id) => {
    if (!id) {
      setDetail(null);
      return;
    }
    try {
      const r = await fetch(withProject(`/tickets/${id}`), { credentials: "same-origin" });
      if (!r.ok)
        throw new Error(await r.text());
      const out = await r.json();
      setDetail(out);
      const t = out.ticket;
      setDraft({ title: t.title, description: t.description, type: t.type, status: t.status, priority: t.priority, area: t.area_slug || "general", requester_name: t.requester_name || "", requester_email: t.requester_email || "", requester_organization: t.requester_organization || "", assignee_name: t.assignee_name || "", due_at: t.due_at || "" });
    } catch (e) {
      setMessage(e.message);
    }
  }, [withProject]);
  useEffect(() => {
    loadAreas();
    loadPortal();
  }, [loadAreas, loadPortal]);
  useEffect(() => {
    const timer = window.setTimeout(() => void loadTickets(), query ? 220 : 0);
    return () => window.clearTimeout(timer);
  }, [loadTickets, query]);
  useEffect(() => {
    if (!creating && !showAreas)
      loadDetail(selectedId);
  }, [creating, loadDetail, selectedId, showAreas]);
  const selected = useMemo(() => tickets.find((t) => t.id === selectedId) ?? null, [selectedId, tickets]);
  const startNew = () => {
    setCreating(true);
    setShowAreas(false);
    setSelectedId(0);
    setDetail(null);
    setDraft(emptyDraft());
    setMessage("New ticket");
  };
  const createTicket = async () => {
    if (!draft.title.trim()) {
      setMessage("Title is required.");
      return;
    }
    setBusy(true);
    try {
      const body = { ...draft, status: undefined, due_at: draft.due_at || "" };
      const r = await fetch(withProject("/tickets"), { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const out = await r.json();
      if (!r.ok)
        throw new Error(out.error || "Create failed");
      setCreating(false);
      await loadTickets();
      setSelectedId(out.ticket.id);
      setMessage(`${out.ticket.key} created.`);
    } catch (e) {
      setMessage(e.message);
    } finally {
      setBusy(false);
    }
  };
  const saveTicket = async () => {
    if (!detail)
      return;
    setBusy(true);
    try {
      const patch = { title: draft.title, description: draft.description, type: draft.type, priority: draft.priority, area: draft.area, requester_name: draft.requester_name, requester_email: draft.requester_email, requester_organization: draft.requester_organization, assignee_name: draft.assignee_name, due_at: draft.due_at };
      let r = await fetch(withProject(`/tickets/${detail.ticket.id}`), { method: "PATCH", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
      let out = await r.json();
      if (!r.ok)
        throw new Error(out.error || "Save failed");
      if (draft.status !== detail.ticket.status) {
        r = await fetch(withProject(`/tickets/${detail.ticket.id}/status`), { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ status: draft.status }) });
        out = await r.json();
        if (!r.ok)
          throw new Error(out.error || "Status update failed");
      }
      await loadTickets();
      await loadDetail(detail.ticket.id);
      setMessage("Saved.");
    } catch (e) {
      setMessage(e.message);
    } finally {
      setBusy(false);
    }
  };
  const addComment = async () => {
    if (!detail || !composer.trim())
      return;
    setBusy(true);
    try {
      const path = internal ? "internal-notes" : "comments";
      const r = await fetch(withProject(`/tickets/${detail.ticket.id}/${path}`), { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ body: composer }) });
      const out = await r.json();
      if (!r.ok)
        throw new Error(out.error || "Reply failed");
      setComposer("");
      await loadDetail(detail.ticket.id);
      await loadTickets();
      setMessage(internal ? "Internal note added." : "Reply added.");
    } catch (e) {
      setMessage(e.message);
    } finally {
      setBusy(false);
    }
  };
  const upload = async (files) => {
    if (!detail || !files?.length)
      return;
    setBusy(true);
    try {
      for (const file of Array.from(files)) {
        if (file.size > 10 * 1024 * 1024)
          throw new Error(`${file.name} exceeds 10 MB`);
        setMessage(`Uploading ${file.name}…`);
        const content_base64 = await fileBase64(file);
        const r = await fetch(withProject(`/tickets/${detail.ticket.id}/attachments`), { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: file.name, content_type: file.type || "application/octet-stream", content_base64, visibility: internal ? "internal" : "public" }) });
        const out = await r.json();
        if (!r.ok)
          throw new Error(out.error || "Upload failed");
      }
      await loadDetail(detail.ticket.id);
      setMessage("Attachment added.");
    } catch (e) {
      setMessage(e.message);
    } finally {
      setBusy(false);
    }
  };
  const copyPortal = async () => {
    if (!portal)
      return;
    await navigator.clipboard.writeText(portal.intake_url);
    setMessage("Client intake link copied.");
  };
  return /* @__PURE__ */ jsxDEV("div", {
    className: "h-full min-h-0 flex flex-col bg-bg text-text",
    children: [
      /* @__PURE__ */ jsxDEV("header", {
        className: "shrink-0 border-b border-border px-4 py-3 flex items-center gap-3",
        children: [
          /* @__PURE__ */ jsxDEV("div", {
            children: [
              /* @__PURE__ */ jsxDEV("h1", {
                className: "text-sm font-semibold",
                children: "Tickets"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("p", {
                className: "text-xs text-text-muted",
                children: "Client feedback, support requests, and permanent history."
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("input", {
            value: query,
            onChange: (e) => setQuery(e.target.value),
            placeholder: "Search tickets",
            className: "ml-auto w-64 bg-bg-input border border-border rounded px-2.5 py-1.5 text-sm"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            onClick: () => {
              setShowAreas(true);
              setCreating(false);
            },
            className: "px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input",
            children: "Areas"
          }, undefined, false, undefined, this),
          portal && /* @__PURE__ */ jsxDEV("button", {
            onClick: copyPortal,
            className: "px-3 py-1.5 text-xs border border-border rounded hover:bg-bg-input",
            children: "Copy intake link"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            onClick: startNew,
            className: "px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium",
            children: "New ticket"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("main", {
        className: "flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[380px_minmax(0,1fr)]",
        children: [
          /* @__PURE__ */ jsxDEV("aside", {
            className: "border-r border-border min-h-0 flex flex-col",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "p-3 border-b border-border grid grid-cols-2 gap-2",
                children: [
                  /* @__PURE__ */ jsxDEV("select", {
                    value: statusFilter,
                    onChange: (e) => setStatusFilter(e.target.value),
                    className: "bg-bg-input border border-border rounded px-2 py-1.5 text-xs",
                    children: [
                      /* @__PURE__ */ jsxDEV("option", {
                        value: "",
                        children: "All statuses"
                      }, undefined, false, undefined, this),
                      statuses.map((v) => /* @__PURE__ */ jsxDEV("option", {
                        value: v,
                        children: label(v)
                      }, v, false, undefined, this))
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("select", {
                    value: areaFilter,
                    onChange: (e) => setAreaFilter(e.target.value),
                    className: "bg-bg-input border border-border rounded px-2 py-1.5 text-xs",
                    children: [
                      /* @__PURE__ */ jsxDEV("option", {
                        value: "",
                        children: "All areas"
                      }, undefined, false, undefined, this),
                      areas.map((v) => /* @__PURE__ */ jsxDEV("option", {
                        value: v.slug,
                        children: v.name
                      }, v.id, false, undefined, this))
                    ]
                  }, undefined, true, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "flex-1 min-h-0 overflow-auto",
                children: tickets.length === 0 ? /* @__PURE__ */ jsxDEV("div", {
                  className: "p-4 text-sm text-text-muted",
                  children: "No tickets match."
                }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("ul", {
                  className: "divide-y divide-border",
                  children: tickets.map((t) => /* @__PURE__ */ jsxDEV("li", {
                    children: /* @__PURE__ */ jsxDEV("button", {
                      onClick: () => {
                        setCreating(false);
                        setShowAreas(false);
                        setSelectedId(t.id);
                      },
                      className: `w-full text-left px-4 py-3 hover:bg-bg-input ${selectedId === t.id && !creating && !showAreas ? "bg-bg-input" : ""}`,
                      children: [
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "flex items-center gap-2",
                          children: [
                            /* @__PURE__ */ jsxDEV("span", {
                              className: "text-[10px] text-text-dim",
                              children: t.key
                            }, undefined, false, undefined, this),
                            /* @__PURE__ */ jsxDEV("span", {
                              className: `text-[10px] rounded px-1.5 py-0.5 ${statusClass(t.status)}`,
                              children: label(t.status)
                            }, undefined, false, undefined, this),
                            /* @__PURE__ */ jsxDEV("span", {
                              className: "ml-auto text-[10px] text-text-muted",
                              children: relative(t.updated_at)
                            }, undefined, false, undefined, this)
                          ]
                        }, undefined, true, undefined, this),
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "mt-1 text-sm font-medium line-clamp-2",
                          children: t.title
                        }, undefined, false, undefined, this),
                        /* @__PURE__ */ jsxDEV("div", {
                          className: "mt-1 flex items-center gap-2 text-[11px] text-text-muted",
                          children: [
                            /* @__PURE__ */ jsxDEV("span", {
                              children: t.area_name || "General"
                            }, undefined, false, undefined, this),
                            /* @__PURE__ */ jsxDEV("span", {
                              children: "·"
                            }, undefined, false, undefined, this),
                            /* @__PURE__ */ jsxDEV("span", {
                              children: label(t.type)
                            }, undefined, false, undefined, this),
                            t.requester_name && /* @__PURE__ */ jsxDEV(Fragment, {
                              children: [
                                /* @__PURE__ */ jsxDEV("span", {
                                  children: "·"
                                }, undefined, false, undefined, this),
                                /* @__PURE__ */ jsxDEV("span", {
                                  className: "truncate",
                                  children: t.requester_name
                                }, undefined, false, undefined, this)
                              ]
                            }, undefined, true, undefined, this)
                          ]
                        }, undefined, true, undefined, this)
                      ]
                    }, undefined, true, undefined, this)
                  }, t.id, false, undefined, this))
                }, undefined, false, undefined, this)
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("footer", {
                className: "px-3 py-2 border-t border-border text-xs text-text-muted",
                children: message
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          showAreas ? /* @__PURE__ */ jsxDEV(AreaManager, {
            areas,
            withProject,
            reload: loadAreas,
            close: () => setShowAreas(false)
          }, undefined, false, undefined, this) : creating ? /* @__PURE__ */ jsxDEV(TicketEditor, {
            draft,
            setDraft,
            areas,
            busy,
            onSave: createTicket,
            create: true
          }, undefined, false, undefined, this) : detail && selected ? /* @__PURE__ */ jsxDEV("section", {
            className: "min-h-0 flex flex-col",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "shrink-0 border-b border-border px-4 py-3 flex items-center gap-3",
                children: [
                  /* @__PURE__ */ jsxDEV("div", {
                    children: [
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "text-xs text-text-muted",
                        children: detail.ticket.key
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "font-semibold",
                        children: detail.ticket.title
                      }, undefined, false, undefined, this)
                    ]
                  }, undefined, true, undefined, this),
                  detail.ticket.requester_crm_contact_id && /* @__PURE__ */ jsxDEV("span", {
                    className: "text-[10px] border border-border rounded px-2 py-1",
                    children: [
                      "CRM #",
                      detail.ticket.requester_crm_contact_id
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("a", {
                    href: detail.ticket.portal_url,
                    target: "_blank",
                    rel: "noreferrer",
                    className: "ml-auto text-xs text-accent hover:underline",
                    children: "Open client view"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("button", {
                    disabled: busy,
                    onClick: saveTicket,
                    className: "px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50",
                    children: busy ? "Saving…" : "Save"
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "flex-1 min-h-0 overflow-auto",
                children: [
                  /* @__PURE__ */ jsxDEV(TicketEditor, {
                    draft,
                    setDraft,
                    areas,
                    busy,
                    onSave: saveTicket
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "border-t border-border p-4",
                    children: [
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "flex gap-2 mb-2",
                        children: [
                          /* @__PURE__ */ jsxDEV("button", {
                            onClick: () => setInternal(false),
                            className: `px-2.5 py-1 text-xs rounded ${!internal ? "bg-accent text-bg" : "border border-border"}`,
                            children: "Public reply"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            onClick: () => setInternal(true),
                            className: `px-2.5 py-1 text-xs rounded ${internal ? "bg-yellow/20 text-yellow" : "border border-border"}`,
                            children: "Internal note"
                          }, undefined, false, undefined, this)
                        ]
                      }, undefined, true, undefined, this),
                      /* @__PURE__ */ jsxDEV("textarea", {
                        value: composer,
                        onChange: (e) => setComposer(e.target.value),
                        placeholder: internal ? "Visible only to the team and agents" : "Visible to the client",
                        className: "w-full min-h-24 bg-bg-input border border-border rounded p-3 text-sm resize-y"
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "mt-2 flex items-center gap-2",
                        children: [
                          /* @__PURE__ */ jsxDEV("label", {
                            className: "px-3 py-1.5 text-xs border border-border rounded cursor-pointer hover:bg-bg-input",
                            children: [
                              "Attach file",
                              /* @__PURE__ */ jsxDEV("input", {
                                type: "file",
                                multiple: true,
                                className: "hidden",
                                onChange: (e) => void upload(e.target.files)
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this),
                          /* @__PURE__ */ jsxDEV("button", {
                            disabled: busy || !composer.trim(),
                            onClick: addComment,
                            className: "px-3 py-1.5 text-xs bg-accent text-bg rounded font-medium disabled:opacity-50",
                            children: internal ? "Add note" : "Send reply"
                          }, undefined, false, undefined, this)
                        ]
                      }, undefined, true, undefined, this)
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV(Timeline, {
                    detail
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this) : /* @__PURE__ */ jsxDEV("div", {
            className: "grid place-items-center text-sm text-text-muted",
            children: "Select or create a ticket."
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function TicketEditor({ draft, setDraft, areas, busy, onSave, create = false }) {
  const set = (key, value) => setDraft({ ...draft, [key]: value });
  return /* @__PURE__ */ jsxDEV("div", {
    className: "p-4 grid grid-cols-1 md:grid-cols-3 gap-3 border-b border-border",
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        className: "md:col-span-3 text-xs text-text-muted",
        children: [
          "Title",
          /* @__PURE__ */ jsxDEV("input", {
            value: draft.title,
            onChange: (e) => set("title", e.target.value),
            placeholder: "Short summary",
            className: "mt-1 w-full bg-bg-input border border-border rounded px-3 py-2 text-sm text-text"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Area",
          /* @__PURE__ */ jsxDEV("select", {
            value: draft.area,
            onChange: (e) => set("area", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm",
            children: areas.map((a) => /* @__PURE__ */ jsxDEV("option", {
              value: a.slug,
              children: a.name
            }, a.id, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Type",
          /* @__PURE__ */ jsxDEV("select", {
            value: draft.type,
            onChange: (e) => set("type", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm",
            children: types.map((v) => /* @__PURE__ */ jsxDEV("option", {
              value: v,
              children: label(v)
            }, v, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Priority",
          /* @__PURE__ */ jsxDEV("select", {
            value: draft.priority,
            onChange: (e) => set("priority", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm",
            children: priorities.map((v) => /* @__PURE__ */ jsxDEV("option", {
              value: v,
              children: label(v)
            }, v, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      !create && /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Status",
          /* @__PURE__ */ jsxDEV("select", {
            value: draft.status,
            onChange: (e) => set("status", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm",
            children: statuses.map((v) => /* @__PURE__ */ jsxDEV("option", {
              value: v,
              children: label(v)
            }, v, false, undefined, this))
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Requester name",
          /* @__PURE__ */ jsxDEV("input", {
            value: draft.requester_name,
            onChange: (e) => set("requester_name", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Requester email",
          /* @__PURE__ */ jsxDEV("input", {
            type: "email",
            value: draft.requester_email,
            onChange: (e) => set("requester_email", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Organization",
          /* @__PURE__ */ jsxDEV("input", {
            value: draft.requester_organization,
            onChange: (e) => set("requester_organization", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      !create && /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Assignee",
          /* @__PURE__ */ jsxDEV("input", {
            value: draft.assignee_name,
            onChange: (e) => set("assignee_name", e.target.value),
            placeholder: "Optional",
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "text-xs text-text-muted",
        children: [
          "Deadline ",
          /* @__PURE__ */ jsxDEV("span", {
            className: "font-normal text-text-dim",
            children: "optional"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("input", {
            type: "datetime-local",
            value: draft.due_at,
            onChange: (e) => set("due_at", e.target.value),
            className: "mt-1 w-full bg-bg-input border border-border rounded px-2 py-2 text-sm"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("label", {
        className: "md:col-span-3 text-xs text-text-muted",
        children: [
          "Description",
          /* @__PURE__ */ jsxDEV("textarea", {
            value: draft.description,
            onChange: (e) => set("description", e.target.value),
            placeholder: "Detailed feedback or support request",
            className: "mt-1 w-full min-h-32 bg-bg-input border border-border rounded p-3 text-sm resize-y"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      create && /* @__PURE__ */ jsxDEV("div", {
        className: "md:col-span-3",
        children: /* @__PURE__ */ jsxDEV("button", {
          disabled: busy,
          onClick: onSave,
          className: "px-4 py-2 text-sm bg-accent text-bg rounded font-medium disabled:opacity-50",
          children: busy ? "Creating…" : "Create ticket"
        }, undefined, false, undefined, this)
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function Timeline({ detail }) {
  const items = [...detail.comments.map((c) => ({ kind: "comment", at: c.created_at, id: `c${c.id}`, visibility: c.visibility, title: c.visibility === "internal" ? "Internal note" : "Comment", actor: c.author_name || label(c.author_kind), body: c.body })), ...detail.events.filter((e) => !["ticket.commented", "ticket.internal_note.added"].includes(e.event_type)).map((e) => ({ kind: "event", at: e.created_at, id: `e${e.id}`, visibility: e.visibility, title: label(e.event_type.replace("ticket.", "")), actor: e.actor_name || label(e.actor_kind), body: eventSummary(e) }))].sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime());
  return /* @__PURE__ */ jsxDEV("div", {
    className: "p-4",
    children: [
      /* @__PURE__ */ jsxDEV("h3", {
        className: "text-xs font-semibold uppercase tracking-wide text-text-muted mb-3",
        children: "History"
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "space-y-3",
        children: [
          items.map((i) => /* @__PURE__ */ jsxDEV("div", {
            className: `border rounded p-3 ${i.visibility === "internal" ? "border-yellow/30 bg-yellow/5" : "border-border bg-bg-input/30"}`,
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "flex gap-2 text-xs",
                children: [
                  /* @__PURE__ */ jsxDEV("strong", {
                    children: i.title
                  }, undefined, false, undefined, this),
                  i.visibility === "internal" && /* @__PURE__ */ jsxDEV("span", {
                    className: "text-yellow",
                    children: "internal"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("span", {
                    className: "text-text-muted",
                    children: [
                      "by ",
                      i.actor
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("span", {
                    className: "ml-auto text-text-dim",
                    children: formatDate(i.at)
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              i.body && /* @__PURE__ */ jsxDEV("p", {
                className: "mt-2 whitespace-pre-wrap text-sm",
                children: i.body
              }, undefined, false, undefined, this)
            ]
          }, i.id, true, undefined, this)),
          items.length === 0 && /* @__PURE__ */ jsxDEV("p", {
            className: "text-sm text-text-muted",
            children: "No activity yet."
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      detail.attachments.length > 0 && /* @__PURE__ */ jsxDEV("div", {
        className: "mt-5",
        children: [
          /* @__PURE__ */ jsxDEV("h3", {
            className: "text-xs font-semibold uppercase tracking-wide text-text-muted mb-2",
            children: "Attachments"
          }, undefined, false, undefined, this),
          detail.attachments.map((a) => /* @__PURE__ */ jsxDEV("a", {
            href: a.url,
            target: "_blank",
            rel: "noreferrer",
            className: "block text-sm text-accent hover:underline",
            children: [
              a.name,
              " ",
              /* @__PURE__ */ jsxDEV("span", {
                className: "text-text-muted",
                children: formatBytes(a.size_bytes)
              }, undefined, false, undefined, this)
            ]
          }, a.id, true, undefined, this))
        ]
      }, undefined, true, undefined, this),
      detail.links.length > 0 && /* @__PURE__ */ jsxDEV("div", {
        className: "mt-5",
        children: [
          /* @__PURE__ */ jsxDEV("h3", {
            className: "text-xs font-semibold uppercase tracking-wide text-text-muted mb-2",
            children: "Linked work"
          }, undefined, false, undefined, this),
          detail.links.map((l) => /* @__PURE__ */ jsxDEV("div", {
            className: "text-sm",
            children: l.url ? /* @__PURE__ */ jsxDEV("a", {
              href: l.url,
              target: "_blank",
              rel: "noreferrer",
              className: "text-accent hover:underline",
              children: l.label || `${label(l.kind)} ${l.external_id}`
            }, undefined, false, undefined, this) : /* @__PURE__ */ jsxDEV("span", {
              children: l.label || `${label(l.kind)} ${l.external_id}`
            }, undefined, false, undefined, this)
          }, l.id, false, undefined, this))
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function AreaManager({ areas, withProject, reload, close }) {
  const [name, setName] = useState(""), [color, setColor] = useState("#6b7280"), [message, setMessage] = useState("");
  const create = async () => {
    const r = await fetch(withProject("/areas"), { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, color }) });
    const out = await r.json();
    if (!r.ok) {
      setMessage(out.error || "Could not create area");
      return;
    }
    setName("");
    await reload();
    setMessage("Area created.");
  };
  return /* @__PURE__ */ jsxDEV("section", {
    className: "p-5 overflow-auto",
    children: [
      /* @__PURE__ */ jsxDEV("div", {
        className: "flex items-center",
        children: [
          /* @__PURE__ */ jsxDEV("div", {
            children: [
              /* @__PURE__ */ jsxDEV("h2", {
                className: "font-semibold",
                children: "Feedback areas"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("p", {
                className: "text-xs text-text-muted",
                children: "Use areas for Backend, Frontend, Design, or any client-specific category."
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            onClick: close,
            className: "ml-auto px-3 py-1.5 text-xs border border-border rounded",
            children: "Close"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "mt-5 max-w-xl border border-border rounded divide-y divide-border",
        children: areas.map((a) => /* @__PURE__ */ jsxDEV("div", {
          className: "p-3 flex items-center gap-3",
          children: [
            /* @__PURE__ */ jsxDEV("span", {
              className: "w-3 h-3 rounded-full",
              style: { background: a.color }
            }, undefined, false, undefined, this),
            /* @__PURE__ */ jsxDEV("span", {
              className: "font-medium text-sm",
              children: a.name
            }, undefined, false, undefined, this),
            /* @__PURE__ */ jsxDEV("span", {
              className: "text-xs text-text-muted",
              children: a.slug
            }, undefined, false, undefined, this)
          ]
        }, a.id, true, undefined, this))
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("div", {
        className: "mt-4 max-w-xl flex gap-2",
        children: [
          /* @__PURE__ */ jsxDEV("input", {
            value: name,
            onChange: (e) => setName(e.target.value),
            placeholder: "New area",
            className: "flex-1 bg-bg-input border border-border rounded px-3 py-2 text-sm"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("input", {
            type: "color",
            value: color,
            onChange: (e) => setColor(e.target.value),
            className: "h-9 w-12 bg-bg-input border border-border rounded"
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("button", {
            disabled: !name.trim(),
            onClick: create,
            className: "px-3 py-2 text-sm bg-accent text-bg rounded disabled:opacity-50",
            children: "Add"
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("p", {
        className: "mt-2 text-xs text-text-muted",
        children: message
      }, undefined, false, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function emptyDraft() {
  return { title: "", description: "", type: "feedback", status: "new", priority: "normal", area: "general", requester_name: "", requester_email: "", requester_organization: "", assignee_name: "", due_at: "" };
}
function label(v) {
  return v.replaceAll("_", " ").replace(/\b\w/g, (c) => c.toUpperCase());
}
function statusClass(v) {
  if (v === "resolved" || v === "closed")
    return "bg-green/15 text-green";
  if (v === "waiting_client")
    return "bg-yellow/15 text-yellow";
  if (v === "in_progress")
    return "bg-accent/15 text-accent";
  return "bg-border text-text-muted";
}
function formatDate(v) {
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : d.toLocaleString();
}
function relative(v) {
  const ms = Date.now() - new Date(v).getTime();
  if (!v || Number.isNaN(ms))
    return "";
  const m = Math.floor(ms / 60000);
  if (m < 1)
    return "now";
  if (m < 60)
    return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24)
    return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}
function eventSummary(e) {
  const d = e.data || {};
  if (d.reason)
    return String(d.reason);
  if (d.from !== undefined && d.to !== undefined)
    return `${label(String(d.from))} → ${label(String(d.to))}`;
  if (d.changes && typeof d.changes === "object")
    return `Changed ${Object.keys(d.changes).map(label).join(", ")}`;
  return "";
}
function fileBase64(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader;
    r.onload = () => resolve(String(r.result).split(",")[1] || "");
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}
function formatBytes(n) {
  if (!n)
    return "";
  if (n < 1024)
    return `${n} B`;
  if (n < 1024 * 1024)
    return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
export {
  TicketsPanel as default
};

//# debugId=63FA567A2FD54CB564756E2164756E21
//# sourceMappingURL=TicketsPanel.mjs.map
