import { useEffect, useMemo, useState } from "react";
import { jsx, jsxs, Fragment } from "react/jsx-runtime";

const API = "/api/apps/creators";

function qs(projectId, extra = {}) {
  const p = new URLSearchParams();
  if (projectId) p.set("project_id", projectId);
  for (const [k, v] of Object.entries(extra)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

async function api(path, projectId, opts = {}) {
  const res = await fetch(`${API}${path}${path.includes("?") ? "&" : qs(projectId)}`, {
    credentials: "same-origin",
    headers: opts.body ? { "Content-Type": "application/json" } : undefined,
    ...opts,
    body: opts.body ? JSON.stringify(opts.body) : undefined,
  });
  if (!res.ok) {
    let msg = `${res.status}`;
    try {
      const j = await res.json();
      msg = j.error || msg;
    } catch {}
    throw new Error(msg);
  }
  return res.json();
}

function CreatorsPanel({ projectId }) {
  const [tab, setTab] = useState("posts");
  const [space, setSpace] = useState(null);
  const [tiers, setTiers] = useState([]);
  const [members, setMembers] = useState([]);
  const [posts, setPosts] = useState([]);
  const [events, setEvents] = useState([]);
  const [status, setStatus] = useState("");
  const [draft, setDraft] = useState({ title: "", body: "", visibility: "members" });
  const [tierDraft, setTierDraft] = useState({ name: "", price_cents: 500, currency: "USD", interval: "month" });
  const [memberDraft, setMemberDraft] = useState({ email: "", display_name: "", status: "lead" });

  const load = async () => {
    try {
      const [s, t, m, p, e] = await Promise.all([
        api("/space", projectId),
        api("/tiers", projectId),
        api("/members", projectId),
        api("/posts", projectId),
        api("/activity", projectId),
      ]);
      setSpace(s.space);
      setTiers(t || []);
      setMembers(m || []);
      setPosts(p || []);
      setEvents(e || []);
      setStatus("");
    } catch (err) {
      setStatus(err.message || "failed to load");
    }
  };

  useEffect(() => { load(); }, [projectId]);

  const metrics = useMemo(() => {
    const active = members.filter((m) => m.status === "active" || m.status === "comped").length;
    const published = posts.filter((p) => p.status === "published").length;
    const monthly = members.reduce((sum, m) => {
      if (!(m.status === "active" || m.status === "comped") || !m.tier_id) return sum;
      const tier = tiers.find((t) => t.id === m.tier_id);
      return sum + (tier?.interval === "month" ? tier.price_cents || 0 : 0);
    }, 0);
    return { active, published, monthly };
  }, [members, posts, tiers]);

  const createPost = async (e) => {
    e.preventDefault();
    if (!draft.title.trim()) return;
    try {
      await api("/posts", projectId, { method: "POST", body: draft });
      setDraft({ title: "", body: "", visibility: "members" });
      load();
    } catch (err) { setStatus(err.message); }
  };

  const createTier = async (e) => {
    e.preventDefault();
    if (!tierDraft.name.trim()) return;
    try {
      await api("/tiers", projectId, { method: "POST", body: { ...tierDraft, price_cents: Number(tierDraft.price_cents || 0) } });
      setTierDraft({ name: "", price_cents: 500, currency: space?.default_currency || "USD", interval: "month" });
      load();
    } catch (err) { setStatus(err.message); }
  };

  const createMember = async (e) => {
    e.preventDefault();
    if (!memberDraft.email.trim()) return;
    try {
      await api("/members", projectId, { method: "POST", body: memberDraft });
      setMemberDraft({ email: "", display_name: "", status: "lead" });
      load();
    } catch (err) { setStatus(err.message); }
  };

  const publish = async (id) => {
    try {
      await api(`/posts/${id}/publish`, projectId, { method: "POST", body: {} });
      load();
    } catch (err) { setStatus(err.message); }
  };

  return jsxs("div", { className: "h-full overflow-auto bg-bg text-text", children: [
    jsxs("header", { className: "border-b border-border px-5 py-4 flex flex-wrap items-center gap-4", children: [
      jsx("div", { className: "min-w-0", children: jsxs(Fragment, { children: [
        jsx("h1", { className: "text-lg font-semibold", children: space?.name || "Creators" }),
        jsx("p", { className: "text-xs text-text-muted truncate", children: space?.description || "Memberships, gated posts, and supporter files." })
      ]}) }),
      jsx("div", { className: "ml-auto grid grid-cols-3 gap-2 text-right", children: [
        metric("Members", metrics.active),
        metric("Posts", metrics.published),
        metric("MRR", money(metrics.monthly, space?.default_currency || "USD")),
      ]})
    ]}),
    jsxs("nav", { className: "px-5 py-3 border-b border-border flex gap-2", children: ["posts", "tiers", "members", "events"].map((t) =>
      jsx("button", { onClick: () => setTab(t), className: `px-3 py-1.5 rounded text-sm ${tab === t ? "bg-bg-card text-text" : "text-text-muted hover:text-text"}`, children: t[0].toUpperCase() + t.slice(1) }, t)
    ) }),
    status && jsx("div", { className: "mx-5 mt-4 rounded border border-error/40 bg-error/10 px-3 py-2 text-sm text-error", children: status }),
    tab === "posts" && jsxs("main", { className: "p-5 grid lg:grid-cols-[1fr_360px] gap-5", children: [
      jsx("section", { className: "space-y-2", children: posts.length === 0 ? empty("No posts yet.") : posts.map((p) => jsx(PostRow, { post: p, onPublish: () => publish(p.id) }, p.id)) }),
      jsxs("form", { onSubmit: createPost, className: "border border-border rounded bg-bg-card p-4 flex flex-col gap-3 h-fit", children: [
        jsx("h2", { className: "font-medium text-sm", children: "New Post" }),
        jsx("input", { value: draft.title, onChange: (e) => setDraft({ ...draft, title: e.target.value }), placeholder: "Title", className: input }),
        jsx("textarea", { value: draft.body, onChange: (e) => setDraft({ ...draft, body: e.target.value }), placeholder: "Body", className: `${input} min-h-32` }),
        jsx("select", { value: draft.visibility, onChange: (e) => setDraft({ ...draft, visibility: e.target.value }), className: input, children: ["public", "members", "tier", "private"].map((v) => jsx("option", { value: v, children: v }, v)) }),
        jsx("button", { className: primary, children: "Create" })
      ]})
    ]}),
    tab === "tiers" && jsxs("main", { className: "p-5 grid lg:grid-cols-[1fr_360px] gap-5", children: [
      jsx("section", { className: "space-y-2", children: tiers.length === 0 ? empty("No tiers yet.") : tiers.map((t) => jsx(TierRow, { tier: t }, t.id)) }),
      jsxs("form", { onSubmit: createTier, className: "border border-border rounded bg-bg-card p-4 flex flex-col gap-3 h-fit", children: [
        jsx("h2", { className: "font-medium text-sm", children: "New Tier" }),
        jsx("input", { value: tierDraft.name, onChange: (e) => setTierDraft({ ...tierDraft, name: e.target.value }), placeholder: "Name", className: input }),
        jsx("input", { type: "number", value: tierDraft.price_cents, onChange: (e) => setTierDraft({ ...tierDraft, price_cents: e.target.value }), className: input }),
        jsx("select", { value: tierDraft.interval, onChange: (e) => setTierDraft({ ...tierDraft, interval: e.target.value }), className: input, children: ["month", "year", "one_time"].map((v) => jsx("option", { value: v, children: v }, v)) }),
        jsx("button", { className: primary, children: "Create" })
      ]})
    ]}),
    tab === "members" && jsxs("main", { className: "p-5 grid lg:grid-cols-[1fr_360px] gap-5", children: [
      jsx("section", { className: "space-y-2", children: members.length === 0 ? empty("No members yet.") : members.map((m) => jsx(MemberRow, { member: m, tiers }, m.id)) }),
      jsxs("form", { onSubmit: createMember, className: "border border-border rounded bg-bg-card p-4 flex flex-col gap-3 h-fit", children: [
        jsx("h2", { className: "font-medium text-sm", children: "New Member" }),
        jsx("input", { value: memberDraft.email, onChange: (e) => setMemberDraft({ ...memberDraft, email: e.target.value }), placeholder: "email@example.com", className: input }),
        jsx("input", { value: memberDraft.display_name, onChange: (e) => setMemberDraft({ ...memberDraft, display_name: e.target.value }), placeholder: "Display name", className: input }),
        jsx("select", { value: memberDraft.status, onChange: (e) => setMemberDraft({ ...memberDraft, status: e.target.value }), className: input, children: ["lead", "active", "past_due", "paused", "cancelled", "comped"].map((v) => jsx("option", { value: v, children: v }, v)) }),
        jsx("button", { className: primary, children: "Create" })
      ]})
    ]}),
    tab === "events" && jsx("main", { className: "p-5 space-y-2", children: events.length === 0 ? empty("No events yet.") : events.map((e) => jsxs("div", { className: "border border-border rounded bg-bg-card px-3 py-2 text-sm", children: [
      jsxs("div", { className: "flex gap-2", children: [jsx("span", { className: "font-medium", children: e.kind }), jsx("span", { className: "text-text-muted", children: e.created_at })] }),
      jsx("pre", { className: "text-xs text-text-muted whitespace-pre-wrap mt-1", children: JSON.stringify(e.data || {}, null, 2) })
    ]}, e.id)) })
  ]});
}

function metric(label, value) {
  return jsxs("div", { className: "border border-border rounded bg-bg-card px-3 py-2 min-w-24", children: [
    jsx("div", { className: "text-[10px] uppercase text-text-muted", children: label }),
    jsx("div", { className: "font-semibold", children: value })
  ]});
}

function PostRow({ post, onPublish }) {
  return jsxs("div", { className: "border border-border rounded bg-bg-card px-3 py-3", children: [
    jsxs("div", { className: "flex gap-3 items-start", children: [
      jsx("div", { className: "min-w-0 flex-1", children: jsxs(Fragment, { children: [
        jsx("div", { className: "font-medium truncate", children: post.title }),
        jsx("div", { className: "text-xs text-text-muted", children: `${post.status} · ${post.visibility} · /${post.slug}` })
      ]}) }),
      post.status !== "published" && jsx("button", { onClick: onPublish, className: "text-xs border border-border rounded px-2 py-1 hover:border-accent", children: "Publish" })
    ]}),
    post.body && jsx("p", { className: "text-sm text-text-muted mt-2 line-clamp-2", children: post.body })
  ]});
}

function TierRow({ tier }) {
  return jsxs("div", { className: "border border-border rounded bg-bg-card px-3 py-3 flex items-center gap-3", children: [
    jsx("div", { className: "font-medium flex-1", children: tier.name }),
    jsx("div", { className: "text-sm text-text-muted", children: `${money(tier.price_cents, tier.currency)} / ${tier.interval}` })
  ]});
}

function MemberRow({ member, tiers }) {
  const tier = tiers.find((t) => t.id === member.tier_id);
  return jsxs("div", { className: "border border-border rounded bg-bg-card px-3 py-3 flex items-center gap-3", children: [
    jsx("div", { className: "min-w-0 flex-1", children: jsxs(Fragment, { children: [
      jsx("div", { className: "font-medium truncate", children: member.display_name || member.email }),
      jsx("div", { className: "text-xs text-text-muted truncate", children: member.email })
    ]}) }),
    jsx("div", { className: "text-sm text-text-muted", children: tier?.name || "No tier" }),
    jsx("div", { className: "text-xs border border-border rounded px-2 py-1", children: member.status })
  ]});
}

function empty(text) {
  return jsx("div", { className: "border border-border rounded bg-bg-card p-8 text-center text-sm text-text-muted", children: text });
}

function money(cents, currency) {
  return new Intl.NumberFormat(undefined, { style: "currency", currency: currency || "USD" }).format((Number(cents) || 0) / 100);
}

const input = "bg-bg-input border border-border rounded px-3 py-2 text-sm";
const primary = "bg-accent text-bg rounded px-3 py-2 text-sm font-medium disabled:opacity-50";

export default CreatorsPanel;
