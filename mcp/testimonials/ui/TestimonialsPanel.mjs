// ui/TestimonialsPanel.tsx
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { jsxDEV } from "react/jsx-dev-runtime";
var API = "/api/apps/testimonials";
var PAGE_SIZE = 25;
var statuses = ["draft", "submitted", "approved", "rejected", "published", "archived"];
var kinds = ["text", "review", "video", "audio", "image"];
var consents = ["unknown", "granted", "denied", "revoked"];
var scopes = ["private", "internal", "public", "marketing"];
var inputClass = "w-full min-w-0 rounded border border-border bg-bg-input px-3 py-2 text-sm text-text";
var blank = {
  id: 0,
  status: "draft",
  kind: "text",
  source: "manual",
  title: "",
  quote: "",
  body: "",
  author_name: "",
  author_role: "",
  author_company: "",
  author_email: "",
  media_file_id: "",
  media_url: "",
  consent_status: "unknown",
  permission_scope: "internal",
  tags: [],
  metadata: {}
};
function TestimonialsPanel({ projectId }) {
  const [items, setItems] = useState([]);
  const [selectedId, setSelectedId] = useState(undefined);
  const [draft, setDraft] = useState(() => copyTestimonial(blank));
  const [tagText, setTagText] = useState("");
  const [baseline, setBaseline] = useState(() => fingerprint(blank, ""));
  const [statusFilter, setStatusFilter] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState(null);
  const [mobileView, setMobileView] = useState("list");
  const [showMedia, setShowMedia] = useState(false);
  const initializedRef = useRef(false);
  const requestSeqRef = useRef(0);
  const selectedIdRef = useRef(selectedId);
  const dirtyRef = useRef(false);
  const payload = useMemo(() => toPayload(draft, tagText), [draft, tagText]);
  const dirty = useMemo(() => JSON.stringify(payload) !== baseline, [baseline, payload]);
  const publishBlocked = draft.status === "published" && (draft.consent_status !== "granted" || !["public", "marketing"].includes(draft.permission_scope));
  const busy = loading || saving;
  const pageStart = total === 0 ? 0 : page * PAGE_SIZE + 1;
  const pageEnd = Math.min((page + 1) * PAGE_SIZE, total);
  useEffect(() => {
    selectedIdRef.current = selectedId;
  }, [selectedId]);
  useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setQuery(queryInput.trim());
      setPage(0);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [queryInput]);
  const api = useCallback(async (method, path, body) => {
    const url = new URL(`${API}${path}`, window.location.origin);
    url.searchParams.set("project_id", projectId);
    const init = { method, credentials: "same-origin" };
    if (body !== undefined) {
      init.headers = { "Content-Type": "application/json" };
      init.body = JSON.stringify(body);
    }
    const res = await fetch(`${url.pathname}${url.search}`, init);
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      throw new Error(data?.error || `${res.status} ${res.statusText}`);
    }
    return await res.json();
  }, [projectId]);
  const load = useCallback(async (preferredId) => {
    const seq = ++requestSeqRef.current;
    setLoading(true);
    try {
      const params = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(page * PAGE_SIZE) });
      if (statusFilter)
        params.set("status", statusFilter);
      if (kindFilter)
        params.set("kind", kindFilter);
      if (query)
        params.set("q", query);
      const data = await api("GET", `/testimonials?${params}`);
      if (seq !== requestSeqRef.current)
        return;
      const list = data.testimonials || [];
      setItems(list);
      setTotal(data.total || 0);
      setHasMore(Boolean(data.has_more));
      const currentId = preferredId ?? selectedIdRef.current;
      const fresh = typeof currentId === "number" ? list.find((item) => item.id === currentId) : undefined;
      if (!initializedRef.current) {
        initializedRef.current = true;
        if (list[0])
          openLoadedItem(list[0], false);
      } else if (fresh && (!dirtyRef.current || preferredId !== undefined)) {
        openLoadedItem(fresh, false);
      } else if (typeof currentId === "number" && !fresh && !dirtyRef.current && preferredId === undefined) {
        if (list[0])
          openLoadedItem(list[0], false);
        else
          resetToNew(false);
      }
    } catch (error) {
      if (seq === requestSeqRef.current)
        setNotice({ tone: "error", text: error.message });
    } finally {
      if (seq === requestSeqRef.current)
        setLoading(false);
    }
  }, [api, kindFilter, page, query, statusFilter]);
  useEffect(() => {
    load();
  }, [load]);
  function openLoadedItem(item, showEditor = true) {
    const copy = copyTestimonial(item);
    const tags = (copy.tags || []).join(", ");
    selectedIdRef.current = item.id;
    dirtyRef.current = false;
    setSelectedId(item.id);
    setDraft(copy);
    setTagText(tags);
    setBaseline(fingerprint(copy, tags));
    setShowMedia(false);
    if (showEditor)
      setMobileView("editor");
  }
  function resetToNew(showEditor = true) {
    const next = copyTestimonial(blank);
    selectedIdRef.current = null;
    dirtyRef.current = false;
    setSelectedId(null);
    setDraft(next);
    setTagText("");
    setBaseline(fingerprint(next, ""));
    setShowMedia(false);
    if (showEditor)
      setMobileView("editor");
  }
  function confirmDiscard() {
    return !dirtyRef.current || window.confirm("Discard unsaved changes?");
  }
  function startNew() {
    if (!confirmDiscard())
      return;
    resetToNew();
    setNotice(null);
  }
  function selectItem(item) {
    if (selectedIdRef.current === item.id) {
      setMobileView("editor");
      return;
    }
    if (!confirmDiscard())
      return;
    openLoadedItem(item);
    setNotice(null);
  }
  function backToList() {
    if (!confirmDiscard())
      return;
    if (dirtyRef.current) {
      if (typeof selectedIdRef.current === "number") {
        const original = items.find((item) => item.id === selectedIdRef.current);
        if (original)
          openLoadedItem(original, false);
      } else {
        resetToNew(false);
      }
    }
    setMobileView("list");
  }
  async function save() {
    if (!hasContent(payload)) {
      setNotice({ tone: "error", text: "Add text or a media reference before saving." });
      return;
    }
    if (publishBlocked) {
      setNotice({ tone: "error", text: "Publishing requires granted consent and public or marketing permission." });
      return;
    }
    setSaving(true);
    try {
      const data = draft.id ? await api("PATCH", `/testimonials/${draft.id}`, payload) : await api("POST", "/testimonials", payload);
      openLoadedItem(data.testimonial);
      setNotice({ tone: "success", text: draft.id ? "Changes saved." : "Testimonial created." });
      setPage(0);
      await load(data.testimonial.id);
    } catch (error) {
      setNotice({ tone: "error", text: error.message });
    } finally {
      setSaving(false);
    }
  }
  async function archive() {
    if (!draft.id || !window.confirm(`Archive “${labelFor(draft)}”?`))
      return;
    setSaving(true);
    try {
      await api("DELETE", `/testimonials/${draft.id}`);
      initializedRef.current = false;
      selectedIdRef.current = undefined;
      dirtyRef.current = false;
      setSelectedId(undefined);
      setPage(0);
      setNotice({ tone: "success", text: "Testimonial archived." });
      await load();
      setMobileView("list");
    } catch (error) {
      setNotice({ tone: "error", text: error.message });
    } finally {
      setSaving(false);
    }
  }
  function setFilter(setter, value) {
    setter(value);
    setPage(0);
  }
  return /* @__PURE__ */ jsxDEV("div", {
    className: "testimonials-shell h-full min-h-0 bg-bg text-text",
    children: [
      /* @__PURE__ */ jsxDEV("style", {
        children: `
        .testimonials-shell { display: block; }
        .testimonials-meta-grid, .testimonials-content-grid { display: grid; }
        @media (min-width: 1024px) {
          .testimonials-shell { display: grid; grid-template-columns: 320px minmax(0, 1fr); }
        }
        @media (min-width: 1280px) {
          .testimonials-meta-grid { grid-template-columns: minmax(0, 1fr) 150px 150px 170px; }
          .testimonials-content-grid { grid-template-columns: minmax(0, 1fr) 300px; }
        }
      `
      }, undefined, false, undefined, this),
      /* @__PURE__ */ jsxDEV("aside", {
        className: `${mobileView === "editor" ? "hidden lg:flex" : "flex"} h-full min-h-0 flex-col border-r border-border`,
        children: [
          /* @__PURE__ */ jsxDEV("div", {
            className: "border-b border-border p-3",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "flex items-center gap-2",
                children: [
                  /* @__PURE__ */ jsxDEV("h1", {
                    className: "min-w-0 flex-1 text-sm font-semibold",
                    children: "Testimonials"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("button", {
                    type: "button",
                    onClick: startNew,
                    disabled: busy,
                    className: "flex h-8 w-8 items-center justify-center rounded border border-border text-lg hover:bg-bg-input disabled:opacity-40",
                    "aria-label": "New testimonial",
                    title: "New testimonial",
                    children: "+"
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "mt-3 grid grid-cols-2 gap-2",
                children: [
                  /* @__PURE__ */ jsxDEV("label", {
                    className: "sr-only",
                    htmlFor: "testimonials-status-filter",
                    children: "Status filter"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("select", {
                    id: "testimonials-status-filter",
                    value: statusFilter,
                    onChange: (event) => setFilter(setStatusFilter, event.target.value),
                    className: "min-w-0 rounded border border-border bg-bg-input px-2 py-1.5 text-sm",
                    children: [
                      /* @__PURE__ */ jsxDEV("option", {
                        value: "",
                        children: "Active statuses"
                      }, undefined, false, undefined, this),
                      statuses.map((value) => /* @__PURE__ */ jsxDEV("option", {
                        value,
                        children: capitalize(value)
                      }, value, false, undefined, this))
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("label", {
                    className: "sr-only",
                    htmlFor: "testimonials-kind-filter",
                    children: "Type filter"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("select", {
                    id: "testimonials-kind-filter",
                    value: kindFilter,
                    onChange: (event) => setFilter(setKindFilter, event.target.value),
                    className: "min-w-0 rounded border border-border bg-bg-input px-2 py-1.5 text-sm",
                    children: [
                      /* @__PURE__ */ jsxDEV("option", {
                        value: "",
                        children: "All types"
                      }, undefined, false, undefined, this),
                      kinds.map((value) => /* @__PURE__ */ jsxDEV("option", {
                        value,
                        children: capitalize(value)
                      }, value, false, undefined, this))
                    ]
                  }, undefined, true, undefined, this)
                ]
              }, undefined, true, undefined, this),
              /* @__PURE__ */ jsxDEV("label", {
                className: "sr-only",
                htmlFor: "testimonials-search",
                children: "Search testimonials"
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("input", {
                id: "testimonials-search",
                value: queryInput,
                onChange: (event) => setQueryInput(event.target.value),
                placeholder: "Search testimonials",
                className: "mt-2 w-full rounded border border-border bg-bg-input px-2 py-1.5 text-sm"
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("div", {
            className: "min-h-0 flex-1 overflow-auto",
            children: loading && items.length === 0 ? /* @__PURE__ */ jsxDEV("div", {
              className: "p-4 text-sm text-text-muted",
              children: "Loading…"
            }, undefined, false, undefined, this) : items.length === 0 ? /* @__PURE__ */ jsxDEV("div", {
              className: "p-4 text-sm text-text-muted",
              children: "No testimonials match these filters."
            }, undefined, false, undefined, this) : items.map((item) => /* @__PURE__ */ jsxDEV("button", {
              type: "button",
              onClick: () => selectItem(item),
              className: `block w-full border-b border-border/60 px-3 py-3 text-left hover:bg-bg-input ${selectedId === item.id ? "bg-bg-input" : ""}`,
              children: [
                /* @__PURE__ */ jsxDEV("div", {
                  className: "flex items-center gap-2",
                  children: [
                    /* @__PURE__ */ jsxDEV("div", {
                      className: "min-w-0 flex-1 truncate text-sm font-medium",
                      children: labelFor(item)
                    }, undefined, false, undefined, this),
                    /* @__PURE__ */ jsxDEV("span", {
                      className: `shrink-0 rounded px-1.5 py-0.5 text-[10px] ${statusTone(item.status)}`,
                      children: capitalize(item.status)
                    }, undefined, false, undefined, this)
                  ]
                }, undefined, true, undefined, this),
                /* @__PURE__ */ jsxDEV("div", {
                  className: "mt-1 truncate text-xs text-text-muted",
                  children: [
                    authorLine(item) || capitalize(item.kind),
                    item.rating ? ` · ${item.rating}/5` : ""
                  ]
                }, undefined, true, undefined, this),
                /* @__PURE__ */ jsxDEV("div", {
                  className: "mt-1 line-clamp-2 text-xs text-text-dim",
                  children: item.quote || item.body || item.media_url || item.media_file_id
                }, undefined, false, undefined, this)
              ]
            }, item.id, true, undefined, this))
          }, undefined, false, undefined, this),
          /* @__PURE__ */ jsxDEV("div", {
            className: "border-t border-border p-2",
            children: /* @__PURE__ */ jsxDEV("div", {
              className: "flex items-center justify-between gap-2 text-xs text-text-dim",
              children: [
                /* @__PURE__ */ jsxDEV("span", {
                  children: [
                    pageStart,
                    "–",
                    pageEnd,
                    " of ",
                    total
                  ]
                }, undefined, true, undefined, this),
                /* @__PURE__ */ jsxDEV("div", {
                  className: "flex gap-1",
                  children: [
                    /* @__PURE__ */ jsxDEV("button", {
                      type: "button",
                      onClick: () => setPage((value) => Math.max(0, value - 1)),
                      disabled: page === 0 || loading,
                      className: "flex h-7 w-7 items-center justify-center rounded border border-border hover:bg-bg-input disabled:opacity-30",
                      "aria-label": "Previous page",
                      title: "Previous page",
                      children: "←"
                    }, undefined, false, undefined, this),
                    /* @__PURE__ */ jsxDEV("button", {
                      type: "button",
                      onClick: () => setPage((value) => value + 1),
                      disabled: !hasMore || loading,
                      className: "flex h-7 w-7 items-center justify-center rounded border border-border hover:bg-bg-input disabled:opacity-30",
                      "aria-label": "Next page",
                      title: "Next page",
                      children: "→"
                    }, undefined, false, undefined, this)
                  ]
                }, undefined, true, undefined, this)
              ]
            }, undefined, true, undefined, this)
          }, undefined, false, undefined, this)
        ]
      }, undefined, true, undefined, this),
      /* @__PURE__ */ jsxDEV("main", {
        className: `${mobileView === "list" ? "hidden lg:flex" : "flex"} h-full min-h-0 flex-col`,
        children: [
          /* @__PURE__ */ jsxDEV("header", {
            className: "min-h-14 border-b border-border px-3 py-2 sm:px-4",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "flex min-w-0 items-center gap-2",
                children: [
                  /* @__PURE__ */ jsxDEV("button", {
                    type: "button",
                    onClick: backToList,
                    className: "flex h-8 w-8 shrink-0 items-center justify-center rounded border border-border hover:bg-bg-input lg:hidden",
                    "aria-label": "Back to testimonials",
                    title: "Back to testimonials",
                    children: "←"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("div", {
                    className: "min-w-0 flex-1",
                    children: [
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "flex items-center gap-2",
                        children: [
                          /* @__PURE__ */ jsxDEV("h2", {
                            className: "truncate text-sm font-semibold sm:text-base",
                            children: draft.id ? labelFor(draft) : "New testimonial"
                          }, undefined, false, undefined, this),
                          dirty && /* @__PURE__ */ jsxDEV("span", {
                            className: "shrink-0 rounded bg-yellow/15 px-1.5 py-0.5 text-[10px] text-yellow",
                            children: "Unsaved"
                          }, undefined, false, undefined, this)
                        ]
                      }, undefined, true, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "truncate text-xs text-text-muted",
                        children: [
                          draft.id ? `#${draft.id} · ${capitalize(draft.kind)}` : "New testimonial",
                          draft.updated_at ? ` · Updated ${formatDate(draft.updated_at)}` : ""
                        ]
                      }, undefined, true, undefined, this)
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("button", {
                    type: "button",
                    onClick: save,
                    disabled: saving || !dirty,
                    className: "rounded border border-border px-3 py-1.5 text-sm font-medium hover:bg-bg-input disabled:opacity-40",
                    children: "Save"
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV("button", {
                    type: "button",
                    onClick: archive,
                    disabled: !draft.id || saving,
                    className: "hidden rounded border border-red/50 px-3 py-1.5 text-sm text-red hover:bg-red/10 disabled:opacity-40 sm:block",
                    children: "Archive"
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              notice && /* @__PURE__ */ jsxDEV("div", {
                className: `mt-2 text-xs ${notice.tone === "error" ? "text-red" : notice.tone === "success" ? "text-green" : "text-text-muted"}`,
                role: notice.tone === "error" ? "alert" : "status",
                children: notice.text
              }, undefined, false, undefined, this)
            ]
          }, undefined, true, undefined, this),
          /* @__PURE__ */ jsxDEV("div", {
            className: "min-h-0 flex-1 overflow-auto p-3 sm:p-4",
            children: [
              /* @__PURE__ */ jsxDEV("div", {
                className: "testimonials-meta-grid grid-cols-1 gap-3 sm:grid-cols-2",
                children: [
                  /* @__PURE__ */ jsxDEV(Field, {
                    label: "Title",
                    id: "testimonial-title",
                    children: /* @__PURE__ */ jsxDEV("input", {
                      id: "testimonial-title",
                      value: draft.title,
                      maxLength: 500,
                      onChange: (event) => setDraft({ ...draft, title: event.target.value }),
                      className: inputClass
                    }, undefined, false, undefined, this)
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV(Field, {
                    label: "Status",
                    id: "testimonial-status",
                    children: /* @__PURE__ */ jsxDEV("select", {
                      id: "testimonial-status",
                      value: draft.status,
                      onChange: (event) => setDraft({ ...draft, status: event.target.value }),
                      className: inputClass,
                      children: statuses.map((value) => /* @__PURE__ */ jsxDEV("option", {
                        value,
                        children: capitalize(value)
                      }, value, false, undefined, this))
                    }, undefined, false, undefined, this)
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV(Field, {
                    label: "Type",
                    id: "testimonial-kind",
                    children: /* @__PURE__ */ jsxDEV("select", {
                      id: "testimonial-kind",
                      value: draft.kind,
                      onChange: (event) => setDraft({ ...draft, kind: event.target.value }),
                      className: inputClass,
                      children: kinds.map((value) => /* @__PURE__ */ jsxDEV("option", {
                        value,
                        children: capitalize(value)
                      }, value, false, undefined, this))
                    }, undefined, false, undefined, this)
                  }, undefined, false, undefined, this),
                  /* @__PURE__ */ jsxDEV(Field, {
                    label: "Source",
                    id: "testimonial-source",
                    children: /* @__PURE__ */ jsxDEV("input", {
                      id: "testimonial-source",
                      value: draft.source,
                      maxLength: 100,
                      onChange: (event) => setDraft({ ...draft, source: event.target.value }),
                      className: inputClass
                    }, undefined, false, undefined, this)
                  }, undefined, false, undefined, this)
                ]
              }, undefined, true, undefined, this),
              publishBlocked && /* @__PURE__ */ jsxDEV("div", {
                className: "mt-3 rounded border border-yellow/40 bg-yellow/10 px-3 py-2 text-sm text-yellow",
                role: "alert",
                children: "Publishing requires granted consent and public or marketing permission."
              }, undefined, false, undefined, this),
              /* @__PURE__ */ jsxDEV("div", {
                className: "testimonials-content-grid mt-4 grid-cols-1 gap-5",
                children: [
                  /* @__PURE__ */ jsxDEV("section", {
                    className: "min-w-0",
                    children: [
                      /* @__PURE__ */ jsxDEV(Field, {
                        label: "Quote",
                        id: "testimonial-quote",
                        children: /* @__PURE__ */ jsxDEV("textarea", {
                          id: "testimonial-quote",
                          value: draft.quote,
                          maxLength: 5000,
                          onChange: (event) => setDraft({ ...draft, quote: event.target.value }),
                          className: `${inputClass} h-28 resize-y p-3 leading-5`
                        }, undefined, false, undefined, this)
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "mt-4",
                        children: /* @__PURE__ */ jsxDEV(Field, {
                          label: "Full review",
                          id: "testimonial-body",
                          children: /* @__PURE__ */ jsxDEV("textarea", {
                            id: "testimonial-body",
                            value: draft.body,
                            maxLength: 1e5,
                            onChange: (event) => setDraft({ ...draft, body: event.target.value }),
                            className: `${inputClass} h-44 resize-y p-3 leading-5`
                          }, undefined, false, undefined, this)
                        }, undefined, false, undefined, this)
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "mt-4 border-t border-border pt-4",
                        children: [
                          /* @__PURE__ */ jsxDEV("div", {
                            className: "flex items-center justify-between gap-3",
                            children: [
                              /* @__PURE__ */ jsxDEV("h3", {
                                className: "text-sm font-medium",
                                children: "Media"
                              }, undefined, false, undefined, this),
                              /* @__PURE__ */ jsxDEV("a", {
                                href: "/apps/storage/page",
                                className: "text-xs text-accent hover:underline",
                                children: "Open Storage"
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this),
                          /* @__PURE__ */ jsxDEV("div", {
                            className: "mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2",
                            children: [
                              /* @__PURE__ */ jsxDEV(Field, {
                                label: "Storage file ID",
                                id: "testimonial-media-file-id",
                                children: /* @__PURE__ */ jsxDEV("input", {
                                  id: "testimonial-media-file-id",
                                  value: draft.media_file_id,
                                  maxLength: 500,
                                  onChange: (event) => setDraft({ ...draft, media_file_id: event.target.value }),
                                  className: inputClass
                                }, undefined, false, undefined, this)
                              }, undefined, false, undefined, this),
                              /* @__PURE__ */ jsxDEV(Field, {
                                label: "Media URL",
                                id: "testimonial-media-url",
                                children: /* @__PURE__ */ jsxDEV("input", {
                                  id: "testimonial-media-url",
                                  type: "url",
                                  value: draft.media_url,
                                  maxLength: 4096,
                                  onChange: (event) => {
                                    setDraft({ ...draft, media_url: event.target.value });
                                    setShowMedia(false);
                                  },
                                  className: inputClass
                                }, undefined, false, undefined, this)
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this),
                          draft.media_url && isPreviewableURL(draft.media_url) && /* @__PURE__ */ jsxDEV("div", {
                            className: "mt-3",
                            children: [
                              /* @__PURE__ */ jsxDEV("button", {
                                type: "button",
                                onClick: () => setShowMedia((value) => !value),
                                className: "rounded border border-border px-3 py-1.5 text-sm hover:bg-bg-input",
                                children: showMedia ? "Hide preview" : "Preview media"
                              }, undefined, false, undefined, this),
                              showMedia && /* @__PURE__ */ jsxDEV(MediaPreview, {
                                kind: draft.kind,
                                url: draft.media_url
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this)
                        ]
                      }, undefined, true, undefined, this)
                    ]
                  }, undefined, true, undefined, this),
                  /* @__PURE__ */ jsxDEV("aside", {
                    className: "min-w-0 border-t border-border pt-4 xl:border-l xl:border-t-0 xl:pl-5 xl:pt-0",
                    children: [
                      /* @__PURE__ */ jsxDEV("div", {
                        children: [
                          /* @__PURE__ */ jsxDEV("span", {
                            className: "block text-xs text-text-muted",
                            children: "Rating"
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV("div", {
                            className: "mt-1 flex items-center gap-1",
                            role: "group",
                            "aria-label": "Rating",
                            children: [
                              [1, 2, 3, 4, 5].map((value) => /* @__PURE__ */ jsxDEV("button", {
                                type: "button",
                                onClick: () => setDraft({ ...draft, rating: value }),
                                className: `flex h-8 w-8 items-center justify-center rounded border text-base ${draft.rating && value <= draft.rating ? "border-yellow/60 bg-yellow/10 text-yellow" : "border-border text-text-dim hover:bg-bg-input"}`,
                                "aria-label": `${value} star${value === 1 ? "" : "s"}`,
                                "aria-pressed": draft.rating === value,
                                children: "★"
                              }, value, false, undefined, this)),
                              draft.rating && /* @__PURE__ */ jsxDEV("button", {
                                type: "button",
                                onClick: () => setDraft({ ...draft, rating: undefined }),
                                className: "ml-1 text-xs text-text-muted hover:text-text",
                                children: "Clear"
                              }, undefined, false, undefined, this)
                            ]
                          }, undefined, true, undefined, this)
                        ]
                      }, undefined, true, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "mt-4 grid grid-cols-2 gap-3",
                        children: [
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Consent",
                            id: "testimonial-consent",
                            children: /* @__PURE__ */ jsxDEV("select", {
                              id: "testimonial-consent",
                              value: draft.consent_status,
                              onChange: (event) => setDraft({ ...draft, consent_status: event.target.value }),
                              className: inputClass,
                              children: consents.map((value) => /* @__PURE__ */ jsxDEV("option", {
                                value,
                                children: capitalize(value)
                              }, value, false, undefined, this))
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Permission",
                            id: "testimonial-permission",
                            children: /* @__PURE__ */ jsxDEV("select", {
                              id: "testimonial-permission",
                              value: draft.permission_scope,
                              onChange: (event) => setDraft({ ...draft, permission_scope: event.target.value }),
                              className: inputClass,
                              children: scopes.map((value) => /* @__PURE__ */ jsxDEV("option", {
                                value,
                                children: capitalize(value)
                              }, value, false, undefined, this))
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this)
                        ]
                      }, undefined, true, undefined, this),
                      /* @__PURE__ */ jsxDEV("div", {
                        className: "mt-4 space-y-3",
                        children: [
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Author name",
                            id: "testimonial-author-name",
                            children: /* @__PURE__ */ jsxDEV("input", {
                              id: "testimonial-author-name",
                              value: draft.author_name,
                              maxLength: 500,
                              onChange: (event) => setDraft({ ...draft, author_name: event.target.value }),
                              className: inputClass
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Role",
                            id: "testimonial-author-role",
                            children: /* @__PURE__ */ jsxDEV("input", {
                              id: "testimonial-author-role",
                              value: draft.author_role,
                              maxLength: 500,
                              onChange: (event) => setDraft({ ...draft, author_role: event.target.value }),
                              className: inputClass
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Company",
                            id: "testimonial-author-company",
                            children: /* @__PURE__ */ jsxDEV("input", {
                              id: "testimonial-author-company",
                              value: draft.author_company,
                              maxLength: 500,
                              onChange: (event) => setDraft({ ...draft, author_company: event.target.value }),
                              className: inputClass
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Email",
                            id: "testimonial-author-email",
                            children: /* @__PURE__ */ jsxDEV("input", {
                              id: "testimonial-author-email",
                              type: "email",
                              value: draft.author_email,
                              maxLength: 500,
                              onChange: (event) => setDraft({ ...draft, author_email: event.target.value }),
                              className: inputClass
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this),
                          /* @__PURE__ */ jsxDEV(Field, {
                            label: "Tags",
                            id: "testimonial-tags",
                            children: /* @__PURE__ */ jsxDEV("input", {
                              id: "testimonial-tags",
                              value: tagText,
                              onChange: (event) => setTagText(event.target.value),
                              placeholder: "homepage, video",
                              className: inputClass
                            }, undefined, false, undefined, this)
                          }, undefined, false, undefined, this)
                        ]
                      }, undefined, true, undefined, this),
                      parseTags(tagText).length > 0 && /* @__PURE__ */ jsxDEV("div", {
                        className: "mt-2 flex flex-wrap gap-1",
                        children: parseTags(tagText).map((tag) => /* @__PURE__ */ jsxDEV("span", {
                          className: "rounded bg-bg-card px-1.5 py-0.5 text-[11px] text-text-muted",
                          children: tag
                        }, tag, false, undefined, this))
                      }, undefined, false, undefined, this),
                      /* @__PURE__ */ jsxDEV("button", {
                        type: "button",
                        onClick: archive,
                        disabled: !draft.id || saving,
                        className: "mt-5 w-full rounded border border-red/50 px-3 py-2 text-sm text-red hover:bg-red/10 disabled:opacity-40 sm:hidden",
                        children: "Archive"
                      }, undefined, false, undefined, this)
                    ]
                  }, undefined, true, undefined, this)
                ]
              }, undefined, true, undefined, this)
            ]
          }, undefined, true, undefined, this)
        ]
      }, undefined, true, undefined, this)
    ]
  }, undefined, true, undefined, this);
}
function Field({ label, id, children }) {
  return /* @__PURE__ */ jsxDEV("div", {
    className: "min-w-0",
    children: [
      /* @__PURE__ */ jsxDEV("label", {
        htmlFor: id,
        className: "mb-1 block text-xs text-text-muted",
        children: label
      }, undefined, false, undefined, this),
      children
    ]
  }, undefined, true, undefined, this);
}
function MediaPreview({ kind, url }) {
  if (kind === "video")
    return /* @__PURE__ */ jsxDEV("video", {
      className: "mt-3 max-h-80 w-full rounded border border-border bg-black",
      src: url,
      controls: true,
      preload: "metadata"
    }, undefined, false, undefined, this);
  if (kind === "audio")
    return /* @__PURE__ */ jsxDEV("audio", {
      className: "mt-3 w-full",
      src: url,
      controls: true,
      preload: "metadata"
    }, undefined, false, undefined, this);
  if (kind === "image")
    return /* @__PURE__ */ jsxDEV("img", {
      className: "mt-3 max-h-80 max-w-full rounded border border-border object-contain",
      src: url,
      alt: "Testimonial media preview"
    }, undefined, false, undefined, this);
  return /* @__PURE__ */ jsxDEV("a", {
    href: url,
    target: "_blank",
    rel: "noreferrer",
    className: "mt-3 inline-block text-sm text-accent hover:underline",
    children: "Open media"
  }, undefined, false, undefined, this);
}
function copyTestimonial(testimonial) {
  return { ...testimonial, tags: [...testimonial.tags || []], metadata: { ...testimonial.metadata || {} } };
}
function toPayload(testimonial, tagText) {
  return {
    status: testimonial.status,
    kind: testimonial.kind,
    source: testimonial.source || "manual",
    title: testimonial.title,
    quote: testimonial.quote,
    body: testimonial.body,
    rating: testimonial.rating || null,
    author_name: testimonial.author_name,
    author_role: testimonial.author_role,
    author_company: testimonial.author_company,
    author_email: testimonial.author_email,
    media_file_id: testimonial.media_file_id,
    media_url: testimonial.media_url,
    consent_status: testimonial.consent_status,
    permission_scope: testimonial.permission_scope,
    tags: parseTags(tagText),
    metadata: testimonial.metadata || {}
  };
}
function fingerprint(testimonial, tagText) {
  return JSON.stringify(toPayload(testimonial, tagText));
}
function parseTags(value) {
  return [...new Set(value.split(",").map((tag) => tag.trim()).filter(Boolean))].slice(0, 50);
}
function hasContent(payload) {
  return Boolean(payload.title.trim() || payload.quote.trim() || payload.body.trim() || payload.media_file_id.trim() || payload.media_url.trim());
}
function isPreviewableURL(value) {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}
function labelFor(testimonial) {
  return testimonial.title || testimonial.quote || authorLine(testimonial) || testimonial.body || testimonial.media_url || "Untitled testimonial";
}
function authorLine(testimonial) {
  return [testimonial.author_name, testimonial.author_role, testimonial.author_company].filter(Boolean).join(" · ");
}
function statusTone(status) {
  switch (status) {
    case "published":
      return "bg-green/15 text-green";
    case "approved":
      return "bg-accent/15 text-accent";
    case "rejected":
      return "bg-red/15 text-red";
    case "archived":
      return "bg-bg-card text-text-dim";
    case "submitted":
      return "bg-yellow/15 text-yellow";
    default:
      return "bg-bg-card text-text-muted";
  }
}
function capitalize(value) {
  return value ? value[0].toUpperCase() + value.slice(1) : value;
}
function formatDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime()))
    return value;
  return date.toLocaleDateString();
}
export {
  TestimonialsPanel as default
};

//# debugId=720870866B59EE0E64756E2164756E21
