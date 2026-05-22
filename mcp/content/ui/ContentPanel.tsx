// ContentPanel — dashboard surface for the content app.
//
// Two views: a list of posts/pages, and a block editor for one post.
// Talks to /api/apps/content/admin/* through the platform proxy
// (the proxy strips /api/apps/content, the sidecar mounts the REST
// surface under /admin/* so it doesn't collide with the public
// render namespace at / and /posts/:slug).
//
// Bundled to ContentPanel.mjs by apps/scripts/build-panels.ts, which
// externalizes `react` + `react/jsx-runtime` against the dashboard's
// importmap. The dashboard host imports the default export and mounts
// it — the panel must NOT self-mount.

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

// Inlined app-event subscription. Each app ships its own copy because
// panels are bundled standalone and apps are independently installable
// — cross-app imports would break a one-off install. Same shape CRM
// and analytics use.
interface AppEventEnvelope<T = unknown> {
  topic: string;
  app: string;
  project_id: string;
  install_id: number;
  seq: number;
  time: string;
  data: T;
}
function useAppEvents<T = unknown>(
  app: string,
  projectId: string | undefined | null,
  onEvent: (ev: AppEventEnvelope<T>) => void,
) {
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;
  useEffect(() => {
    if (!app || !projectId) return;
    // Preferred path: the dashboard hosts a multiplexed bridge on
    // window.__aptevaAppEvents so multiple panels share one
    // (app, project) channel pool. Fall back to a raw EventSource
    // when the bridge isn't present (older dashboards).
    const handler = (ev: AppEventEnvelope<T>) => handlerRef.current(ev);
    type Bridge = {
      subscribe: (
        app: string,
        projectId: string,
        handler: (ev: AppEventEnvelope) => void,
      ) => () => void;
    };
    const bridge = (window as unknown as { __aptevaAppEvents?: Bridge })
      .__aptevaAppEvents;
    if (bridge) return bridge.subscribe(app, projectId, handler as (ev: AppEventEnvelope) => void);

    let since = 0;
    let es: EventSource | null = null;
    let stopped = false;
    let reconnect: number | null = null;
    const connect = () => {
      if (stopped) return;
      const url =
        `/api/app-events/${encodeURIComponent(app)}` +
        `?project_id=${encodeURIComponent(projectId)}` +
        (since > 0 ? `&since=${since}` : "");
      es = new EventSource(url, { withCredentials: true });
      es.onmessage = (m) => {
        try {
          const ev = JSON.parse(m.data) as AppEventEnvelope<T>;
          if (ev.seq <= since) return;
          since = ev.seq;
          handlerRef.current(ev);
        } catch {
          /* ignore malformed */
        }
      };
      es.onerror = () => {
        if (es && es.readyState === EventSource.CLOSED) {
          if (reconnect) window.clearTimeout(reconnect);
          reconnect = window.setTimeout(connect, 2000);
        }
      };
    };
    connect();
    return () => {
      stopped = true;
      if (reconnect) window.clearTimeout(reconnect);
      if (es) es.close();
    };
  }, [app, projectId]);
}

interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
}

interface Block {
  id: string;
  type: string;
  attrs?: Record<string, any>;
  inner?: Block[];
}

interface Document {
  version: number;
  blocks: Block[];
}

interface Post {
  id: number;
  kind: string;
  slug: string;
  status: string;
  title: string;
  excerpt?: string;
  body_blocks?: Document;
  updated_at?: string;
}

interface BlockTypeInfo {
  name: string;
  display_name: string;
  category: string;
  description?: string;
  container?: boolean;
}

type Kind = "post" | "page";

// ── inline SVG icons (no emojis in app UIs) ─────────────────────
function Icon({ name }: { name: string }) {
  const d: Record<string, string> = {
    plus: "M12 5v14M5 12h14",
    edit: "M12 20h9M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z",
    eye: "M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8Zm11 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
    arrowLeft: "M19 12H5M12 19l-7-7 7-7",
    arrowUp: "M12 19V5M5 12l7-7 7 7",
    arrowDown: "M12 5v14M19 12l-7 7-7-7",
    trash: "M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6",
    save: "M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2zM17 21v-8H7v8M7 3v5h8",
  };
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d={d[name] ?? ""} />
    </svg>
  );
}

// ── shared api helper (scoped via closure to the panel's project) ─
// Scoped <style> — colored status pills ONLY. We don't redefine the
// dashboard's text/border/bg tokens here; the dashboard's own
// Tailwind utilities (text-fg, text-text-muted, border-border,
// bg-bg-input) already work and define contrast correctly in both
// modes. Overriding them via CSS variables broke contrast in dark
// mode (because our fallback was a light-mode color).
const PANEL_STYLES = `
.cp-status-pill {
  display: inline-block;
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  font-weight: 600;
  line-height: 1.4;
}
.cp-status-pill.cp-status-draft     { background: rgba(107,114,128,0.18); color: rgb(107,114,128); }
.cp-status-pill.cp-status-scheduled { background: rgba(245,158,11,0.20);  color: rgb(180,83,9); }
.cp-status-pill.cp-status-published { background: rgba(16,185,129,0.22);  color: rgb(4,120,87); }
.cp-status-pill.cp-status-archived  { background: rgba(220,38,38,0.18);   color: rgb(185,28,28); }
@media (prefers-color-scheme: dark) {
  .cp-status-pill.cp-status-draft     { color: rgb(156,163,175); }
  .cp-status-pill.cp-status-scheduled { color: rgb(251,191,36); }
  .cp-status-pill.cp-status-published { color: rgb(52,211,153); }
  .cp-status-pill.cp-status-archived  { color: rgb(252,165,165); }
}
`;

// Mount the styles once per component module load. Idempotent.
if (typeof document !== "undefined" && !document.getElementById("content-panel-styles")) {
  const el = document.createElement("style");
  el.id = "content-panel-styles";
  el.textContent = PANEL_STYLES;
  document.head.appendChild(el);
}

// makeAPI returns a fetcher pre-bound to a project and (optionally) a
// site slug. The site slug is appended as ?site=<slug> on every URL so
// every admin REST call lands on the right site.
function makeAPI(projectId: string, siteSlug?: string | null) {
  return async function api<T = any>(path: string, opts: RequestInit = {}): Promise<T> {
    const sep = path.includes("?") ? "&" : "?";
    let url = `/api/apps/content${path}${sep}project_id=${encodeURIComponent(projectId)}`;
    if (siteSlug) {
      url += `&site=${encodeURIComponent(siteSlug)}`;
    }
    const r = await fetch(url, {
      headers: { "Content-Type": "application/json" },
      ...opts,
    });
    if (!r.ok) {
      const body = await r.text();
      throw new Error(`${r.status}: ${body.slice(0, 200)}`);
    }
    return r.json();
  };
}

// ── default attrs per block type for newly-inserted blocks ────────
function defaultAttrs(type: string): Record<string, any> {
  switch (type) {
    case "core/heading":   return { level: 2, text: "Heading" };
    case "core/paragraph": return { text_md: "" };
    case "core/list":      return { style: "bullet", items: ["Item"] };
    case "core/quote":     return { citation: "" };
    case "core/code":      return { language: "", source: "" };
    case "core/embed":     return { url: "" };
    case "core/separator": return { style: "plain" };
    case "core/html":      return { source: "" };
    case "core/markdown":  return { source: "" };
    case "core/table":     return { header: [], rows: [] };
    case "core/button":    return { label: "Click", url: "#", style: "primary" };
    case "core/cta":       return { heading: "", body: "", button_label: "Learn more", button_url: "#" };
    case "core/image":     return { media_id: 0, alt: "", size: "inline" };
    case "core/gallery":   return { media_ids: [], columns: 3 };
    case "core/columns":   return {};
    case "core/group":     return {};
    default:               return {};
  }
}

// ── top-level panel ──────────────────────────────────────────────
//
// Three views:
//   - "list"      → the post/page index (default)
//   - "templates" → site-kit catalog (Finance Blog, SaaS Marketing, …)
//   - "editor"    → block editor for a specific post
//
// Switching between list and templates is via the top tab bar;
// opening the editor replaces the view entirely (own back button).

type View = "list" | "templates" | "themes" | "blocks";

interface SiteSummary {
  id: number;
  slug: string;
  name: string;
  hostname?: string;
  is_default: boolean;
}

export default function ContentPanel({ projectId }: NativePanelProps) {
  const [sites, setSites] = useState<SiteSummary[]>([]);
  const [activeSite, setActiveSite] = useState<string | null>(null);
  const [sitesError, setSitesError] = useState<string | null>(null);

  // Sites list — fetch once per project. The active site defaults to
  // the default; user can switch via the dropdown. Refreshes whenever
  // a new site is created.
  const refreshSites = useCallback(() => {
    const url = `/api/apps/content/admin/sites?project_id=${encodeURIComponent(projectId)}`;
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`${r.status}`))))
      .then((r: { sites: SiteSummary[] | null }) => {
        const list = r.sites ?? [];
        setSites(list);
        // Pick the default site initially; sticky on user choice
        // through component lifetime.
        setActiveSite((curr) => {
          if (curr && list.find((s) => s.slug === curr)) return curr;
          const def = list.find((s) => s.is_default) ?? list[0];
          return def ? def.slug : null;
        });
        setSitesError(null);
      })
      .catch((e) => setSitesError(String(e)));
  }, [projectId]);
  useEffect(refreshSites, [refreshSites]);

  const api = useMemo(() => makeAPI(projectId, activeSite), [projectId, activeSite]);
  const [editing, setEditing] = useState<number | null>(null);
  const [view, setView] = useState<View>("list");

  if (editing != null) {
    return (
      <Editor
        api={api}
        projectId={projectId}
        postId={editing}
        onExit={() => setEditing(null)}
      />
    );
  }
  return (
    <div>
      <Tabs
        view={view}
        onChange={setView}
        sites={sites}
        activeSite={activeSite}
        onSiteChange={setActiveSite}
        onCreatedSite={refreshSites}
        projectId={projectId}
      />
      {sitesError && (
        <div className="bg-red-100 text-red-800 rounded px-3 py-2 mx-4 mt-2">{sitesError}</div>
      )}
      {view === "list" && <ListView api={api} projectId={projectId} onOpen={setEditing} />}
      {view === "templates" && (
        <TemplatesView api={api} projectId={projectId} siteSlug={activeSite} onApplied={() => setView("list")} />
      )}
      {view === "themes" && <ThemesView api={api} />}
      {view === "blocks" && <BlocksView api={api} />}
    </div>
  );
}

function Tabs({
  view,
  onChange,
  sites,
  activeSite,
  onSiteChange,
  onCreatedSite,
  projectId,
}: {
  view: View;
  onChange: (v: View) => void;
  sites: SiteSummary[];
  activeSite: string | null;
  onSiteChange: (slug: string) => void;
  onCreatedSite: () => void;
  projectId: string;
}) {
  const [creating, setCreating] = useState(false);
  const [connectingDomain, setConnectingDomain] = useState(false);
  const tab = (id: View, label: string) => (
    <button
      key={id}
      onClick={() => onChange(id)}
      className={`px-4 py-2 text-sm border-b-2 ${
        view === id ? "border-border-strong font-semibold" : "border-transparent text-text-muted"
      }`}
    >
      {label}
    </button>
  );
  return (
    <div className="flex items-center justify-between border-b border-border px-4 pt-2 bg-bg">
      <div className="flex gap-1">
        {tab("list", "Content")}
        {tab("templates", "Templates")}
        {tab("themes", "Themes")}
        {tab("blocks", "Blocks")}
      </div>
      {/* Site switcher — hidden when only one site exists (single-site UX). */}
      {sites.length >= 2 ? (
        <div className="flex items-center gap-2 pb-2">
          <span className="text-xs text-text-muted">Site</span>
          <select
            value={activeSite ?? ""}
            onChange={(e) => onSiteChange(e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input text-sm"
          >
            {sites.map((s) => (
              <option key={s.slug} value={s.slug}>
                {s.name}{s.is_default ? " (default)" : ""}
                {s.hostname ? ` · ${s.hostname}` : ""}
              </option>
            ))}
          </select>
          <button
            onClick={() => setConnectingDomain(true)}
            className="px-2 py-1 text-xs rounded border border-border"
            title="Attach a custom hostname to this site"
          >
            Connect domain
          </button>
          <button
            onClick={() => setCreating(true)}
            className="px-2 py-1 text-xs rounded border border-border"
          >
            + New site
          </button>
        </div>
      ) : (
        // Single-site mode: show a discreet "+ Add second site" button
        // so users can discover multi-site without it being noisy.
        <div className="pb-2 flex items-center gap-3">
          <button
            onClick={() => setConnectingDomain(true)}
            className="text-xs text-text-muted hover:text-text"
          >
            Connect domain
          </button>
          <button
            onClick={() => setCreating(true)}
            className="text-xs text-text-muted hover:text-text"
          >
            + Add second site
          </button>
        </div>
      )}
      {creating && (
        <NewSiteDialog
          projectId={projectId}
          onClose={() => setCreating(false)}
          onCreated={(slug) => {
            setCreating(false);
            onCreatedSite();
            onSiteChange(slug);
          }}
        />
      )}
      {connectingDomain && activeSite && (
        <ConnectDomainDialog
          projectId={projectId}
          siteSlug={activeSite}
          siteName={sites.find((s) => s.slug === activeSite)?.name ?? activeSite}
          onClose={() => setConnectingDomain(false)}
          onAttached={() => {
            setConnectingDomain(false);
            onCreatedSite();
          }}
        />
      )}
    </div>
  );
}

function NewSiteDialog({
  projectId,
  onClose,
  onCreated,
}: {
  projectId: string;
  onClose: () => void;
  onCreated: (slug: string) => void;
}) {
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [hostname, setHostname] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    if (!slug.trim()) {
      setError("slug required");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const url = `/api/apps/content/admin/sites?project_id=${encodeURIComponent(projectId)}`;
      const r = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ slug, name: name || slug, hostname }),
      });
      if (!r.ok) {
        const body = await r.text();
        throw new Error(`${r.status}: ${body.slice(0, 200)}`);
      }
      const out: { site: SiteSummary } = await r.json();
      onCreated(out.site.slug);
    } catch (e) {
      setError(String(e));
      setSubmitting(false);
    }
  };

  return (
    <div
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded p-4 w-96"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="font-semibold mb-3">New site</h3>
        <p className="text-xs text-text-muted mb-3">
          Sites in the same project share templates but have their own posts,
          pages, menus, settings, and theme. After creating, use "Connect domain"
          to attach a public hostname — that requires the routes app bound (and
          optionally domains + certs for auto-DNS and auto-HTTPS).
        </p>
        <label className="block mb-2">
          <span className="text-xs text-text-muted">Slug (URL-safe id)</span>
          <input
            type="text"
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="e.g. blog"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
            autoFocus
          />
        </label>
        <label className="block mb-2">
          <span className="text-xs text-text-muted">Display name</span>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Blog"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
          />
        </label>
        <label className="block mb-3">
          <span className="text-xs text-text-muted">Hostname (optional)</span>
          <input
            type="text"
            value={hostname}
            onChange={(e) => setHostname(e.target.value)}
            placeholder="e.g. blog.example.com"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
          />
        </label>
        {error && (
          <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>
        )}
        <div className="flex justify-end gap-2 mt-3">
          <button onClick={onClose} className="px-3 py-1 rounded border border-border">
            Cancel
          </button>
          <button
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1 rounded border border-border font-semibold disabled:opacity-50"
          >
            {submitting ? "Creating…" : "Create site"}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── connect-domain dialog ────────────────────────────────────────
//
// One-shot custom-domain attach UI. Calls the
// /admin/sites/{slug}/attach-domain endpoint which orchestrates
// routes + (optionally) domains + (optionally) certs behind the
// scenes. Shows the DNS records to set manually when auto-DNS isn't
// available, or progress when it is.

interface AttachDomainResp {
  site: { id: number; slug: string; hostname: string };
  route: { hostname: string; target: string; registered: boolean };
  dns: { managed: boolean; records: { name: string; type: string; value: string; ttl?: number }[]; note?: string };
  tls: { managed: boolean; status: string; cert_id?: string };
}

function ConnectDomainDialog({
  projectId,
  siteSlug,
  siteName,
  onClose,
  onAttached,
}: {
  projectId: string;
  siteSlug: string;
  siteName: string;
  onClose: () => void;
  onAttached: () => void;
}) {
  const [fqdn, setFqdn] = useState("");
  const [target, setTarget] = useState("");
  const [autoDNS, setAutoDNS] = useState(true);
  const [autoTLS, setAutoTLS] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<AttachDomainResp | null>(null);

  const submit = async () => {
    const value = fqdn.trim().toLowerCase();
    if (!value) {
      setError("FQDN required");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const url = `/api/apps/content/admin/sites/${encodeURIComponent(siteSlug)}/attach-domain?project_id=${encodeURIComponent(projectId)}`;
      const r = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          fqdn: value,
          target: target.trim() || undefined,
          auto_dns: autoDNS,
          auto_tls: autoTLS,
        }),
      });
      const body = await r.json();
      if (!r.ok) {
        throw new Error(body?.error || `HTTP ${r.status}`);
      }
      setResult(body as AttachDomainResp);
    } catch (e) {
      setError(String(e instanceof Error ? e.message : e));
      setSubmitting(false);
    }
  };

  if (result) {
    return (
      <div
        className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
        onClick={onClose}
      >
        <div
          className="bg-bg border border-border rounded p-4 w-[28rem] max-h-[90vh] overflow-y-auto"
          onClick={(e) => e.stopPropagation()}
        >
          <h3 className="font-semibold mb-2">Domain connected — {siteName}</h3>
          <p className="text-xs text-text-muted mb-3">
            <code>{result.site.hostname}</code> is wired to this site. Routing is live; DNS + TLS status below.
          </p>

          <section className="text-sm border border-border rounded p-2 mb-2">
            <p className="text-xs text-text-muted mb-1">Route</p>
            <p>✓ registered → <code className="text-xs">{result.route.target}</code></p>
          </section>

          <section className="text-sm border border-border rounded p-2 mb-2">
            <p className="text-xs text-text-muted mb-1">DNS — {result.dns.managed ? "auto-provisioned" : "set manually"}</p>
            <table className="w-full text-xs">
              <thead><tr className="text-text-muted">
                <th className="text-left">Name</th><th className="text-left">Type</th><th className="text-left">Value</th>
              </tr></thead>
              <tbody>
                {result.dns.records.map((rec, i) => (
                  <tr key={i}>
                    <td className="pr-2"><code>{rec.name}</code></td>
                    <td className="pr-2">{rec.type}</td>
                    <td><code className="break-all">{rec.value || "(your server IP)"}</code></td>
                  </tr>
                ))}
              </tbody>
            </table>
            {result.dns.note && <p className="text-xs text-text-dim mt-2">{result.dns.note}</p>}
          </section>

          <section className="text-sm border border-border rounded p-2 mb-3">
            <p className="text-xs text-text-muted mb-1">TLS</p>
            <p>
              {result.tls.managed
                ? <>{result.tls.status === "pending" ? "⧗ issuance pending" : "✓ " + result.tls.status} {result.tls.cert_id && <span className="text-text-dim text-xs">cert #{result.tls.cert_id}</span>}</>
                : <span className="text-text-dim">skipped (certs app not bound)</span>}
            </p>
          </section>

          <div className="flex justify-end">
            <button onClick={onAttached} className="px-3 py-1 rounded border border-border font-semibold">Done</button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
      onClick={onClose}
    >
      <div
        className="bg-bg border border-border rounded p-4 w-96"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="font-semibold mb-2">Connect a domain to {siteName}</h3>
        <p className="text-xs text-text-muted mb-3">
          The domain will point to this site. Requires the <code>routes</code> app bound; <code>domains</code> and <code>certs</code> are optional for auto-DNS + auto-HTTPS.
        </p>
        <label className="block mb-2">
          <span className="text-xs text-text-muted">FQDN</span>
          <input
            type="text"
            value={fqdn}
            onChange={(e) => setFqdn(e.target.value)}
            placeholder="e.g. acme.example.com"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
            autoFocus
          />
        </label>
        <label className="block mb-2">
          <span className="text-xs text-text-muted">DNS target (optional)</span>
          <input
            type="text"
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder="server IP for A, or hostname for CNAME"
            className="block w-full mt-1 border border-border rounded px-2 py-1 bg-bg-input"
          />
          <span className="block text-xs text-text-dim mt-1">Leave blank to infer from APTEVA_PUBLIC_URL.</span>
        </label>
        <label className="flex items-center gap-2 mb-1 text-xs">
          <input type="checkbox" checked={autoDNS} onChange={(e) => setAutoDNS(e.target.checked)} />
          Auto-provision DNS (requires domains app)
        </label>
        <label className="flex items-center gap-2 mb-3 text-xs">
          <input type="checkbox" checked={autoTLS} onChange={(e) => setAutoTLS(e.target.checked)} />
          Auto-issue HTTPS cert (requires certs app)
        </label>
        {error && (
          <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2 text-xs">{error}</div>
        )}
        <div className="flex justify-end gap-2 mt-3">
          <button onClick={onClose} className="px-3 py-1 rounded border border-border">Cancel</button>
          <button
            onClick={submit}
            disabled={submitting}
            className="px-3 py-1 rounded border border-border font-semibold disabled:opacity-50"
          >
            {submitting ? "Connecting…" : "Connect"}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── list view ───────────────────────────────────────────────────
function ListView({
  api,
  projectId,
  onOpen,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  onOpen: (id: number) => void;
}) {
  const [posts, setPosts] = useState<Post[]>([]);
  const [kind, setKind] = useState<Kind>("post");
  const [status, setStatus] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [draftTitle, setDraftTitle] = useState("");

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    api<{ posts: Post[] | null }>(
      `/admin/posts?kind=${kind}${status ? `&status=${status}` : ""}`,
    )
      .then((r) => setPosts(r.posts ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api, kind, status]);

  useEffect(refresh, [refresh]);

  // Live updates: refresh the list when content events fire. Throttled
  // by topic so a burst of post.updated events doesn't cause N fetches.
  useAppEvents("content", projectId, (ev) => {
    const t = ev.topic;
    if (
      t === "post.created" ||
      t === "post.updated" ||
      t === "post.published" ||
      t === "post.unpublished" ||
      t === "post.archived" ||
      t === "post.deleted" ||
      t === "post.revision_restored" ||
      t === "template.applied" ||
      t === "site.created" ||
      t === "site.archived" ||
      t === "site.default_changed"
    ) {
      refresh();
    }
  });

  const createDraft = () => {
    const title = draftTitle.trim();
    if (!title) return;
    api<{ post: Post }>("/admin/posts", {
      method: "POST",
      body: JSON.stringify({
        kind,
        title,
        blocks: [{ type: "core/paragraph", attrs: { text_md: "" } }],
      }),
    })
      .then((r) => {
        setDraftTitle("");
        onOpen(r.post.id);
      })
      .catch((e) => setError(String(e)));
  };

  const act = (id: number, action: "publish" | "unpublish" | "archive") => {
    api(`/admin/posts/${id}/${action}`, { method: "POST" })
      .then(refresh)
      .catch((e) => setError(String(e)));
  };

  // Permanent delete — opens a custom confirm modal (no native window.confirm
  // anywhere in this panel; we own the modal styling).
  const [pendingDelete, setPendingDelete] = useState<Post | null>(null);
  const remove = (p: Post) => setPendingDelete(p);
  const confirmDelete = async () => {
    if (!pendingDelete) return;
    try {
      await api(`/admin/posts/${pendingDelete.id}?hard=true`, { method: "DELETE" });
      setPendingDelete(null);
      refresh();
    } catch (e) {
      setError(String(e));
      setPendingDelete(null);
    }
  };

  return (
    <div className="p-4 text-sm">
      <header className="flex items-center justify-between gap-4">
        <h2 className="text-base font-semibold">Content</h2>
        <div className="flex gap-2">
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as Kind)}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            <option value="post">Posts</option>
            <option value="page">Pages</option>
          </select>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            <option value="">All statuses</option>
            <option value="draft">Draft</option>
            <option value="scheduled">Scheduled</option>
            <option value="published">Published</option>
            <option value="archived">Archived</option>
          </select>
        </div>
      </header>

      <section className="flex gap-2 my-4">
        <input
          type="text"
          value={draftTitle}
          placeholder={`New ${kind} title…`}
          onChange={(e) => setDraftTitle(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && createDraft()}
          className="flex-1 border border-border rounded px-2 py-1 bg-bg-input"
        />
        <button
          onClick={createDraft}
          className="flex items-center gap-1 px-3 py-1 rounded border border-border"
        >
          <Icon name="plus" /> New & edit
        </button>
      </section>

      {error && <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>}
      {loading && <div className="text-text-muted py-4">Loading…</div>}

      <ul className="list-none p-0 m-0">
        {posts.map((p) => (
          <li
            key={p.id}
            className="flex items-center justify-between py-3 border-b border-border"
          >
            <div className="flex items-baseline gap-2">
              <strong>{p.title || <em>(untitled)</em>}</strong>
              <span className={`cp-status-pill cp-status-${p.status}`}>{p.status}</span>
              <span className="text-xs text-text-muted">
                /{p.kind === "post" ? "posts/" : ""}
                {p.slug}
              </span>
            </div>
            <div className="flex gap-1">
              <button
                onClick={() => onOpen(p.id)}
                className="flex items-center gap-1 px-2 py-1 text-xs rounded border border-border"
              >
                <Icon name="edit" /> Edit
              </button>
              {p.status === "archived" ? (
                // Archived rows: Restore (back to draft) + permanent Delete.
                <button
                  onClick={() => act(p.id, "unpublish")}
                  className="px-2 py-1 text-xs rounded border border-border"
                  title="Move back to draft"
                >
                  Restore
                </button>
              ) : (
                <>
                  {p.status !== "published" && (
                    <button
                      onClick={() => act(p.id, "publish")}
                      className="px-2 py-1 text-xs rounded border border-border"
                    >
                      Publish
                    </button>
                  )}
                  <button
                    onClick={() => act(p.id, "archive")}
                    className="px-2 py-1 text-xs rounded border border-border"
                    title="Soft-delete (restorable)"
                  >
                    Archive
                  </button>
                </>
              )}
              <button
                onClick={() => remove(p)}
                className="flex items-center px-2 py-1 text-xs rounded border border-border text-red-700 hover:bg-red-50"
                title="Permanently delete — cannot be undone"
              >
                <Icon name="trash" />
              </button>
              <a
                href={
                  p.status === "published"
                    ? `/api/apps/content/${p.kind === "post" ? "posts/" : ""}${p.slug}?project_id=${encodeURIComponent(projectId)}`
                    : `/api/apps/content/admin/posts/${p.id}?project_id=${encodeURIComponent(projectId)}`
                }
                target="_blank"
                rel="noreferrer"
                className="flex items-center px-2 py-1 text-xs rounded border border-border"
                title={p.status === "published" ? "View rendered page" : "View JSON (drafts can't render publicly)"}
              >
                <Icon name="eye" />
              </a>
            </div>
          </li>
        ))}
        {!loading && posts.length === 0 && (
          <li className="text-text-muted py-8 text-center">
            No {kind}s yet — create one above.
          </li>
        )}
      </ul>

      {pendingDelete && (
        <ConfirmDialog
          title={`Delete "${pendingDelete.title || "(untitled)"}"?`}
          body={
            <>
              <p>
                This permanently removes the {pendingDelete.kind} — the row is
                marked deleted and disappears from every list, feed, and
                rendered page.
              </p>
              <p className="text-text-muted text-xs mt-2">
                Want a recoverable removal? Cancel and use <strong>Archive</strong>{" "}
                instead.
              </p>
            </>
          }
          confirmLabel="Delete permanently"
          danger
          onCancel={() => setPendingDelete(null)}
          onConfirm={confirmDelete}
        />
      )}
    </div>
  );
}

// ── reusable confirm dialog — replaces window.confirm everywhere ────
function ConfirmDialog({
  title,
  body,
  confirmLabel,
  cancelLabel,
  danger,
  onCancel,
  onConfirm,
}: {
  title: string;
  body?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  danger?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  // ESC closes; clicking the backdrop closes; Enter confirms.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
      if (e.key === "Enter") onConfirm();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel, onConfirm]);

  return (
    <div
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 bg-black/50 flex items-center justify-center z-50"
      onClick={onCancel}
    >
      <div
        className="bg-bg border border-border rounded p-5 w-[28rem] max-w-[calc(100vw-2rem)] shadow-lg"
        onClick={(e) => e.stopPropagation()}
      >
        <h3 className="font-semibold text-base mb-2">{title}</h3>
        {body && <div className="text-sm mb-4">{body}</div>}
        <div className="flex justify-end gap-2">
          <button
            onClick={onCancel}
            autoFocus
            className="px-3 py-1 rounded border border-border"
          >
            {cancelLabel ?? "Cancel"}
          </button>
          <button
            onClick={onConfirm}
            className={`px-3 py-1 rounded font-semibold ${
              danger
                ? "bg-red-600 text-white hover:bg-red-700 border border-red-600"
                : "border border-border"
            }`}
          >
            {confirmLabel ?? "Confirm"}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── editor view ─────────────────────────────────────────────────
function Editor({
  api,
  projectId,
  postId,
  onExit,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  postId: number;
  onExit: () => void;
}) {
  const [post, setPost] = useState<Post | null>(null);
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [types, setTypes] = useState<BlockTypeInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);

  // Fetch post + block types on mount.
  useEffect(() => {
    setLoading(true);
    Promise.all([
      api<{ post: Post }>(`/admin/posts/${postId}`),
      api<{ types: BlockTypeInfo[] }>(`/admin/block-types`),
    ])
      .then(([p, t]) => {
        setPost(p.post);
        setBlocks(p.post.body_blocks?.blocks ?? []);
        setTypes(t.types);
      })
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api, postId]);

  const setTitle = (title: string) => {
    if (!post) return;
    setPost({ ...post, title });
    setDirty(true);
  };
  const setExcerpt = (excerpt: string) => {
    if (!post) return;
    setPost({ ...post, excerpt });
    setDirty(true);
  };

  const replaceBlock = (idx: number, next: Block) => {
    setBlocks((prev) => prev.map((b, i) => (i === idx ? next : b)));
    setDirty(true);
  };
  const moveBlock = (idx: number, delta: number) => {
    setBlocks((prev) => {
      const next = [...prev];
      const j = idx + delta;
      if (j < 0 || j >= next.length) return prev;
      [next[idx], next[j]] = [next[j], next[idx]];
      return next;
    });
    setDirty(true);
  };
  const deleteBlock = (idx: number) => {
    setBlocks((prev) => prev.filter((_, i) => i !== idx));
    setDirty(true);
  };
  const insertBlockAt = (idx: number, type: string) => {
    setBlocks((prev) => {
      const next = [...prev];
      next.splice(idx, 0, { id: "", type, attrs: defaultAttrs(type) });
      return next;
    });
    setDirty(true);
  };

  const save = async () => {
    if (!post) return;
    setSaving(true);
    setError(null);
    try {
      await api(`/admin/posts/${postId}`, {
        method: "PATCH",
        body: JSON.stringify({
          title: post.title,
          excerpt: post.excerpt ?? "",
          blocks,
        }),
      });
      setDirty(false);
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const publish = async () => {
    if (!post) return;
    if (dirty) {
      await save();
    }
    try {
      const r = await api<{ post: Post }>(`/admin/posts/${postId}/publish`, { method: "POST" });
      setPost(r.post);
    } catch (e) {
      setError(String(e));
    }
  };

  if (loading) return <div className="p-4 text-text-muted">Loading…</div>;
  if (!post) return <div className="p-4">Post not found.</div>;

  return (
    <div className="p-4 text-sm">
      <header className="flex items-center justify-between gap-2 mb-3">
        <button
          onClick={onExit}
          className="flex items-center gap-1 px-2 py-1 rounded border border-border"
        >
          <Icon name="arrowLeft" /> Back
        </button>
        <div className="flex items-baseline gap-2">
          <span className={`cp-status-pill cp-status-${post.status}`}>{post.status}</span>
          <span className="text-xs text-text-muted">/{post.kind === "post" ? "posts/" : ""}{post.slug}</span>
        </div>
        <div className="flex gap-2">
          <button
            disabled={!dirty || saving}
            onClick={save}
            className="flex items-center gap-1 px-3 py-1 rounded border border-border disabled:opacity-50"
          >
            <Icon name="save" /> {saving ? "Saving…" : dirty ? "Save" : "Saved"}
          </button>
          <button
            onClick={publish}
            className="px-3 py-1 rounded border border-border"
          >
            {post.status === "published" ? "Republish" : "Publish"}
          </button>
        </div>
      </header>

      {error && <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>}

      <input
        type="text"
        value={post.title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="Title"
        className="w-full text-2xl font-bold border-0 bg-transparent py-2 mb-2 focus:outline-none"
      />
      <input
        type="text"
        value={post.excerpt ?? ""}
        onChange={(e) => setExcerpt(e.target.value)}
        placeholder="Excerpt (optional)"
        className="w-full text-text-muted border-0 bg-transparent py-1 mb-4 focus:outline-none"
      />

      <Insert types={types} onInsert={(t) => insertBlockAt(0, t)} />

      <div className="flex flex-col gap-3">
        {blocks.map((b, i) => (
          <div key={`${i}-${b.id || b.type}`}>
            <BlockCard
              block={b}
              onChange={(nb) => replaceBlock(i, nb)}
              onMoveUp={i > 0 ? () => moveBlock(i, -1) : undefined}
              onMoveDown={i < blocks.length - 1 ? () => moveBlock(i, +1) : undefined}
              onDelete={() => deleteBlock(i)}
            />
            <Insert types={types} onInsert={(t) => insertBlockAt(i + 1, t)} />
          </div>
        ))}
        {blocks.length === 0 && (
          <div className="text-text-muted text-center py-8">Empty post — add a block above.</div>
        )}
      </div>
    </div>
  );
}

// ── insertion bar ────────────────────────────────────────────
function Insert({
  types,
  onInsert,
}: {
  types: BlockTypeInfo[];
  onInsert: (type: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const grouped = useMemo(() => {
    const out: Record<string, BlockTypeInfo[]> = {};
    for (const t of types) {
      (out[t.category] ??= []).push(t);
    }
    return out;
  }, [types]);

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="flex items-center gap-1 text-xs text-text-muted py-1 px-2 my-1 rounded border border-dashed border-border hover:text-text"
      >
        <Icon name="plus" /> Add block
      </button>
    );
  }
  return (
    <div className="border border-border rounded p-2 my-2 bg-bg-input">
      <div className="flex justify-between items-center mb-2">
        <span className="text-xs text-text-muted">Insert block</span>
        <button onClick={() => setOpen(false)} className="text-xs text-text-muted">close</button>
      </div>
      {Object.entries(grouped).map(([cat, ts]) => (
        <div key={cat} className="mb-2">
          <div className="text-xs uppercase text-text-muted mb-1">{cat}</div>
          <div className="flex flex-wrap gap-1">
            {ts.map((t) => (
              <button
                key={t.name}
                onClick={() => {
                  onInsert(t.name);
                  setOpen(false);
                }}
                title={t.description ?? ""}
                className="px-2 py-1 text-xs rounded border border-border"
              >
                {t.display_name}
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

// ── per-block card ───────────────────────────────────────────
function BlockCard({
  block,
  onChange,
  onMoveUp,
  onMoveDown,
  onDelete,
}: {
  block: Block;
  onChange: (nb: Block) => void;
  onMoveUp?: () => void;
  onMoveDown?: () => void;
  onDelete: () => void;
}) {
  const setAttr = (key: string, value: any) => {
    onChange({ ...block, attrs: { ...(block.attrs ?? {}), [key]: value } });
  };

  return (
    <div className="border border-border rounded p-3">
      <div className="flex justify-between items-center mb-2">
        <span className="text-xs text-text-muted">{block.type}</span>
        <div className="flex gap-1">
          {onMoveUp && (
            <button onClick={onMoveUp} title="Move up" className="px-1 py-1 rounded border border-border">
              <Icon name="arrowUp" />
            </button>
          )}
          {onMoveDown && (
            <button onClick={onMoveDown} title="Move down" className="px-1 py-1 rounded border border-border">
              <Icon name="arrowDown" />
            </button>
          )}
          <button onClick={onDelete} title="Delete" className="px-1 py-1 rounded border border-border">
            <Icon name="trash" />
          </button>
        </div>
      </div>
      <BlockEditor block={block} setAttr={setAttr} />
    </div>
  );
}

// ── per-type inline editors ─────────────────────────────────
function BlockEditor({
  block,
  setAttr,
}: {
  block: Block;
  setAttr: (key: string, value: any) => void;
}) {
  const a = block.attrs ?? {};
  const input =
    "w-full border border-border rounded px-2 py-1 bg-bg-input";
  const textarea =
    "w-full border border-border rounded px-2 py-1 bg-bg-input font-mono text-xs";

  switch (block.type) {
    case "core/heading":
      return (
        <div className="flex gap-2">
          <select
            value={a.level ?? 2}
            onChange={(e) => setAttr("level", Number(e.target.value))}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            {[1, 2, 3, 4, 5, 6].map((n) => (
              <option key={n} value={n}>H{n}</option>
            ))}
          </select>
          <input
            type="text"
            value={a.text ?? ""}
            onChange={(e) => setAttr("text", e.target.value)}
            placeholder="Heading text"
            className={input}
          />
        </div>
      );

    case "core/paragraph":
      return (
        <textarea
          rows={3}
          value={a.text_md ?? ""}
          onChange={(e) => setAttr("text_md", e.target.value)}
          placeholder="Paragraph text (markdown — *bold*, _italic_, [link](url))"
          className={textarea}
        />
      );

    case "core/list": {
      const items: string[] = Array.isArray(a.items) ? a.items : [];
      return (
        <div className="flex flex-col gap-1">
          <select
            value={a.style ?? "bullet"}
            onChange={(e) => setAttr("style", e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input self-start"
          >
            <option value="bullet">Bullet</option>
            <option value="number">Numbered</option>
          </select>
          {items.map((it, idx) => (
            <div key={idx} className="flex gap-1">
              <input
                type="text"
                value={it}
                onChange={(e) => {
                  const next = [...items];
                  next[idx] = e.target.value;
                  setAttr("items", next);
                }}
                className={input}
              />
              <button
                onClick={() => setAttr("items", items.filter((_, i) => i !== idx))}
                className="px-2 py-1 rounded border border-border"
              >
                <Icon name="trash" />
              </button>
            </div>
          ))}
          <button
            onClick={() => setAttr("items", [...items, ""])}
            className="self-start px-2 py-1 text-xs rounded border border-border"
          >
            + Item
          </button>
        </div>
      );
    }

    case "core/quote":
      return (
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={a.citation ?? ""}
            onChange={(e) => setAttr("citation", e.target.value)}
            placeholder="Citation (optional)"
            className={input}
          />
          <div className="text-xs text-text-muted">
            Quote body comes from nested blocks (add inside via MCP for now).
          </div>
        </div>
      );

    case "core/code":
      return (
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={a.language ?? ""}
            onChange={(e) => setAttr("language", e.target.value)}
            placeholder="Language (e.g. go, js, py)"
            className={input}
          />
          <textarea
            rows={6}
            value={a.source ?? ""}
            onChange={(e) => setAttr("source", e.target.value)}
            placeholder="Code"
            className={textarea}
          />
        </div>
      );

    case "core/embed":
      return (
        <input
          type="text"
          value={a.url ?? ""}
          onChange={(e) => setAttr("url", e.target.value)}
          placeholder="Embed URL (YouTube, Twitter, etc.)"
          className={input}
        />
      );

    case "core/separator":
      return (
        <select
          value={a.style ?? "plain"}
          onChange={(e) => setAttr("style", e.target.value)}
          className="border border-border rounded px-2 py-1 bg-bg-input"
        >
          <option value="plain">Plain</option>
          <option value="wide">Wide</option>
          <option value="dots">Dots</option>
        </select>
      );

    case "core/html":
      return (
        <textarea
          rows={6}
          value={a.source ?? ""}
          onChange={(e) => setAttr("source", e.target.value)}
          placeholder="<div>HTML (sanitized at render)</div>"
          className={textarea}
        />
      );

    case "core/markdown":
      return (
        <textarea
          rows={8}
          value={a.source ?? ""}
          onChange={(e) => setAttr("source", e.target.value)}
          placeholder="# Heading\n\nMulti-paragraph markdown source."
          className={textarea}
        />
      );

    case "core/button":
      return (
        <div className="flex gap-2">
          <input
            type="text"
            value={a.label ?? ""}
            onChange={(e) => setAttr("label", e.target.value)}
            placeholder="Button label"
            className={input}
          />
          <input
            type="text"
            value={a.url ?? ""}
            onChange={(e) => setAttr("url", e.target.value)}
            placeholder="URL"
            className={input}
          />
          <select
            value={a.style ?? "primary"}
            onChange={(e) => setAttr("style", e.target.value)}
            className="border border-border rounded px-2 py-1 bg-bg-input"
          >
            <option value="primary">Primary</option>
            <option value="secondary">Secondary</option>
            <option value="ghost">Ghost</option>
          </select>
        </div>
      );

    case "core/cta":
      return (
        <div className="flex flex-col gap-1">
          <input
            type="text"
            value={a.heading ?? ""}
            onChange={(e) => setAttr("heading", e.target.value)}
            placeholder="CTA heading"
            className={input}
          />
          <textarea
            rows={2}
            value={a.body ?? ""}
            onChange={(e) => setAttr("body", e.target.value)}
            placeholder="CTA body"
            className={textarea}
          />
          <div className="flex gap-2">
            <input
              type="text"
              value={a.button_label ?? ""}
              onChange={(e) => setAttr("button_label", e.target.value)}
              placeholder="Button label"
              className={input}
            />
            <input
              type="text"
              value={a.button_url ?? ""}
              onChange={(e) => setAttr("button_url", e.target.value)}
              placeholder="Button URL"
              className={input}
            />
          </div>
        </div>
      );

    case "core/image":
      return (
        <div className="flex flex-col gap-1">
          <div className="flex gap-2">
            <input
              type="number"
              value={a.media_id ?? 0}
              onChange={(e) => setAttr("media_id", Number(e.target.value))}
              placeholder="media_id"
              className={input}
            />
            <select
              value={a.size ?? "inline"}
              onChange={(e) => setAttr("size", e.target.value)}
              className="border border-border rounded px-2 py-1 bg-bg-input"
            >
              <option value="inline">Inline</option>
              <option value="wide">Wide</option>
              <option value="full">Full</option>
            </select>
          </div>
          <input
            type="text"
            value={a.alt ?? ""}
            onChange={(e) => setAttr("alt", e.target.value)}
            placeholder="Alt text"
            className={input}
          />
          <input
            type="text"
            value={a.caption ?? ""}
            onChange={(e) => setAttr("caption", e.target.value)}
            placeholder="Caption (optional)"
            className={input}
          />
          <div className="text-xs text-text-muted">
            Upload media via the media library (coming v1.1). For now,
            media_id refers to an already-uploaded row.
          </div>
        </div>
      );

    case "core/columns":
    case "core/group":
      return (
        <div className="text-xs text-text-muted">
          Container block — nested blocks edited via MCP tools in v1.0.
          {block.inner && block.inner.length > 0 && (
            <span> ({block.inner.length} inside)</span>
          )}
        </div>
      );

    default:
      // Unknown / cross-app block: show the attrs as JSON.
      return (
        <textarea
          rows={4}
          value={JSON.stringify(block.attrs ?? {}, null, 2)}
          onChange={(e) => {
            try {
              const next = JSON.parse(e.target.value);
              if (typeof next === "object" && next != null && !Array.isArray(next)) {
                Object.entries(next as Record<string, any>).forEach(([k, v]) => setAttr(k, v));
              }
            } catch {
              // ignore until valid
            }
          }}
          className={textarea}
        />
      );
  }
}

// ── templates view (site-kit catalog) ─────────────────────────────

interface TemplateListItem {
  name: string;
  display_name: string;
  version: string;
  description: string;
  tags: string[] | null;
  source: string;
}

interface ApplySummary {
  template: string;
  version: string;
  mode: string;
  created: Record<string, number>;
  skipped: Record<string, number>;
  homepage_pinned: boolean;
  warnings?: string[];
  dry_run?: boolean;
  would_refuse?: boolean;
  refuse_reason?: string;
  existing_count?: number;
}

function TemplatesView({
  api,
  projectId,
  siteSlug,
  onApplied,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  siteSlug: string | null;
  onApplied: () => void;
}) {
  const [templates, setTemplates] = useState<TemplateListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [picked, setPicked] = useState<TemplateListItem | null>(null);

  useEffect(() => {
    setLoading(true);
    api<{ templates: TemplateListItem[] | null }>("/admin/templates")
      .then((r) => setTemplates(r.templates ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api]);

  if (picked) {
    return (
      <ApplyTemplateDialog
        api={api}
        projectId={projectId}
        siteSlug={siteSlug}
        template={picked}
        onClose={() => setPicked(null)}
        onApplied={() => {
          setPicked(null);
          onApplied();
        }}
      />
    );
  }

  return (
    <div className="p-4 text-sm">
      <header className="flex items-baseline justify-between mb-3">
        <h2 className="text-base font-semibold">Templates</h2>
        <p className="text-xs text-text-muted">
          Apply a starter to populate your site with pages, posts, terms, and menus.
        </p>
      </header>

      {error && (
        <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>
      )}
      {loading && <div className="text-text-muted py-4">Loading…</div>}

      <ul className="grid grid-cols-1 md:grid-cols-2 gap-3 list-none p-0">
        {templates.map((t) => (
          <li key={t.name} className="border border-border rounded p-3 flex flex-col">
            <div className="flex items-baseline justify-between gap-2">
              <h3 className="font-semibold">{t.display_name}</h3>
              <span className="text-xs text-text-muted">v{t.version}</span>
            </div>
            <p className="text-text-muted text-sm flex-1 mt-1">{t.description}</p>
            <div className="flex flex-wrap gap-1 mt-2">
              {(t.tags ?? []).map((tag) => (
                <span key={tag} className="text-xs px-2 py-0.5 rounded bg-bg-input border border-border">
                  {tag}
                </span>
              ))}
              <span className="text-xs px-2 py-0.5 rounded bg-bg-input border border-border ml-auto">
                {t.source}
              </span>
            </div>
            <div className="flex gap-2 mt-3">
              <button
                onClick={() => setPicked(t)}
                className="flex-1 px-3 py-1 rounded border border-border font-medium"
              >
                Apply
              </button>
            </div>
          </li>
        ))}
        {!loading && templates.length === 0 && (
          <li className="text-text-muted text-center py-8 col-span-full">No templates available.</li>
        )}
      </ul>
    </div>
  );
}

interface TemplatePageEntry {
  kind: "page" | "post";
  slug: string;
  title: string;
  blocks_count: number;
}

interface TemplateDetail {
  pages: TemplatePageEntry[];
  homepage_slug: string;
}

function ApplyTemplateDialog({
  api,
  projectId,
  siteSlug,
  template,
  onClose,
  onApplied,
}: {
  api: ReturnType<typeof makeAPI>;
  projectId: string;
  siteSlug: string | null;
  template: TemplateListItem;
  onClose: () => void;
  onApplied: () => void;
}) {
  const [mode, setMode] = useState<"empty_only" | "append" | "overwrite">("empty_only");
  const [detail, setDetail] = useState<TemplateDetail | null>(null);
  const [summary, setSummary] = useState<ApplySummary | null>(null);
  const [activePage, setActivePage] = useState<string>("");
  const [detailLoading, setDetailLoading] = useState(true);
  const [summaryLoading, setSummaryLoading] = useState(true);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<ApplySummary | null>(null);

  // One-shot fetch of the template's page list. Sequenced first
  // because templates_get also runs seedBundledTemplates — the
  // apply-summary fetch below relies on the template being present
  // in the DB. Running them in parallel races: if summary fires
  // before detail finishes seeding, applyTemplate's dbGetTemplate
  // returns nil and the user sees "template not found" in the
  // 'Will create:' panel even though the page picker renders fine.
  useEffect(() => {
    setDetailLoading(true);
    setError(null);
    api<TemplateDetail & { template: any }>(`/admin/templates/${template.name}`)
      .then((r) => {
        const d: TemplateDetail = { pages: r.pages ?? [], homepage_slug: r.homepage_slug ?? "" };
        setDetail(d);
        // Pick the homepage first; fall back to the first page in the
        // template; fall back to "" so the right pane shows the empty
        // state instead of a broken iframe.
        const first = d.homepage_slug || d.pages[0]?.slug || "";
        setActivePage(first);
      })
      .catch((e) => setError(String(e)))
      .finally(() => setDetailLoading(false));
  }, [api, template.name]);

  // Refetch the apply-summary whenever mode flips — that's what tells
  // the user which slugs would be created vs skipped in their current
  // site state. Gated on detail having loaded so seedBundledTemplates
  // has definitely run before applyTemplate's dbGetTemplate fires.
  useEffect(() => {
    if (detailLoading) return;
    setSummaryLoading(true);
    setError(null);
    api<{ summary: ApplySummary }>(`/admin/templates/${template.name}/preview?mode=${mode}`)
      .then((r) => setSummary(r.summary))
      .catch((e) => setError(String(e)))
      .finally(() => setSummaryLoading(false));
  }, [api, template.name, mode, detailLoading]);

  // Build the iframe URL manually — we can't go through the api()
  // closure because <iframe> takes a URL, not JSON-fetched bytes.
  // Same query params makeAPI uses (project_id + site) so the same
  // resolution rules apply.
  const previewURL = activePage
    ? `/api/apps/content/admin/templates/${encodeURIComponent(template.name)}/preview-render` +
      `?page=${encodeURIComponent(activePage)}` +
      `&project_id=${encodeURIComponent(projectId)}` +
      (siteSlug ? `&site=${encodeURIComponent(siteSlug)}` : "")
    : "";

  const apply = async () => {
    setApplying(true);
    setError(null);
    try {
      const r = await api<{ summary: ApplySummary }>(`/admin/templates/${template.name}/apply`, {
        method: "POST",
        body: JSON.stringify({ mode }),
      });
      setResult(r.summary);
    } catch (e) {
      setError(String(e));
    } finally {
      setApplying(false);
    }
  };

  if (result) {
    return (
      <div className="p-4 text-sm">
        <h2 className="text-base font-semibold mb-3">Applied — {template.display_name}</h2>
        <SummaryTable s={result} />
        <button onClick={onApplied} className="mt-4 px-3 py-1 rounded border border-border">
          Back to content
        </button>
      </div>
    );
  }

  return (
    <div className="flex flex-col text-sm" style={{ height: "calc(100vh - 8rem)" }}>
      <header className="flex items-baseline justify-between px-4 py-3 border-b border-border">
        <div>
          <h2 className="text-base font-semibold">Apply — {template.display_name}</h2>
          <p className="text-xs text-text-muted mt-0.5">Live preview of each page rendered through the active theme. Nothing is written until you click Apply.</p>
        </div>
        <button onClick={onClose} className="text-text-muted text-xs">close</button>
      </header>

      <div className="flex flex-1 min-h-0">
        {/* LEFT RAIL: pages + apply controls */}
        <aside className="w-72 shrink-0 border-r border-border overflow-y-auto p-3 flex flex-col gap-4">
          <section>
            <p className="text-xs text-text-muted leading-relaxed">{template.description}</p>
          </section>

          <section>
            <h3 className="text-xs uppercase tracking-wide text-text-muted mb-2">
              Pages ({detail?.pages.length ?? 0})
            </h3>
            {detailLoading && <p className="text-xs text-text-muted">Loading…</p>}
            <ul className="space-y-0.5">
              {detail?.pages.map((p) => {
                const isActive = activePage === p.slug;
                const isHome = detail.homepage_slug === p.slug;
                return (
                  <li key={`${p.kind}-${p.slug}`}>
                    <button
                      onClick={() => setActivePage(p.slug)}
                      className={`w-full text-left px-2 py-1.5 rounded text-xs flex items-baseline gap-2 ${
                        isActive ? "bg-bg-card border border-border-strong" : "hover:bg-bg-card border border-transparent"
                      }`}
                    >
                      <span className="flex-1 truncate text-text font-medium">{p.title}</span>
                      {isHome && <span className="text-text-dim text-[10px] uppercase">home</span>}
                      <span className="text-text-dim">/{p.slug}</span>
                    </button>
                  </li>
                );
              })}
              {!detailLoading && detail && detail.pages.length === 0 && (
                <li className="text-xs text-text-muted px-2 py-1">No pages in this template.</li>
              )}
            </ul>
          </section>

          <section className="border-t border-border pt-3">
            <h3 className="text-xs uppercase tracking-wide text-text-muted mb-2">Apply</h3>

            <label className="block mb-3">
              <span className="text-xs text-text-muted">Mode</span>
              <select
                value={mode}
                onChange={(e) => setMode(e.target.value as typeof mode)}
                className="block mt-1 w-full border border-border rounded px-2 py-1 bg-bg-input text-text"
              >
                <option value="empty_only">Empty only — refuse if site has content</option>
                <option value="append">Append — add only missing slugs</option>
                <option value="overwrite">Overwrite — replace by slug</option>
              </select>
            </label>

            <div className="border border-border rounded p-2 my-2">
              <p className="text-xs text-text-muted mb-1">Will create:</p>
              {summaryLoading && <p className="text-text-muted text-xs">Loading…</p>}
              {summary && <SummaryTable s={summary} />}
            </div>

            {summary?.would_refuse && (
              <div className="bg-amber-100 text-amber-900 rounded px-2 py-2 my-2 text-xs">
                <p className="mb-1">{summary.refuse_reason}</p>
                <button
                  onClick={() => setMode("append")}
                  className="px-2 py-1 rounded border border-amber-300 bg-white"
                >
                  Switch to append
                </button>
              </div>
            )}

            {error && <div className="bg-red-100 text-red-800 rounded px-2 py-2 my-2 text-xs">{error}</div>}

            <div className="flex gap-2 mt-3">
              <button onClick={onClose} className="px-3 py-1 rounded border border-border text-xs">
                Cancel
              </button>
              <button
                onClick={apply}
                disabled={summaryLoading || applying || Boolean(summary?.would_refuse)}
                className="flex-1 px-3 py-1 rounded border border-border font-semibold text-xs disabled:opacity-50 bg-accent text-white border-accent"
                title={summary?.would_refuse ? "Change mode to enable Apply" : undefined}
              >
                {applying ? "Applying…" : "Apply"}
              </button>
            </div>
          </section>
        </aside>

        {/* RIGHT: live preview iframe */}
        <section className="flex-1 bg-bg-card min-w-0 flex flex-col">
          <div className="px-3 py-2 border-b border-border text-xs text-text-muted flex items-center gap-2">
            <span>Preview</span>
            {activePage && <code className="text-text-dim">/{activePage}</code>}
            <span className="ml-auto text-text-dim">Forms are inert in preview.</span>
          </div>
          <div className="flex-1 min-h-0">
            {previewURL ? (
              <iframe
                key={previewURL}
                src={previewURL}
                className="w-full h-full border-0 bg-white"
                title={`Preview of ${activePage}`}
              />
            ) : (
              <div className="flex items-center justify-center h-full text-text-muted text-sm">
                Select a page to preview
              </div>
            )}
          </div>
        </section>
      </div>
    </div>
  );
}

function SummaryTable({ s }: { s: ApplySummary }) {
  const entries = Object.entries(s.created || {}).filter(([_, n]) => n > 0);
  return (
    <ul className="list-none p-0 m-0">
      {entries.map(([kind, n]) => (
        <li key={kind} className="flex justify-between py-0.5">
          <span>{kind}</span>
          <strong>{n}</strong>
        </li>
      ))}
      {s.homepage_pinned && (
        <li className="flex justify-between py-0.5">
          <span>homepage pinned</span>
          <strong>yes</strong>
        </li>
      )}
      {(s.warnings ?? []).length > 0 && (
        <li className="mt-2">
          <p className="text-xs text-text-muted">Warnings:</p>
          <ul className="pl-4 text-xs text-text-muted list-disc">
            {s.warnings!.map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </li>
      )}
    </ul>
  );
}

// ── themes view (catalog + switcher) ─────────────────────────────

interface ThemeInfo {
  slug: string;
  name: string;
  version: string;
  source: string;
  active: boolean;
}

function ThemesView({ api }: { api: ReturnType<typeof makeAPI> }) {
  const [themes, setThemes] = useState<ThemeInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [switching, setSwitching] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    api<{ themes: ThemeInfo[] | null }>("/admin/themes")
      .then((r) => setThemes(r.themes ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api]);

  useEffect(refresh, [refresh]);

  const activate = async (slug: string) => {
    setSwitching(slug);
    setError(null);
    try {
      // themes_set_active via the /tools/call surface — REST mirror is
      // limited to listing for now; the agent path is /tools/call.
      await api("/admin/themes", {
        method: "POST",
        body: JSON.stringify({ slug }),
      });
      refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setSwitching(null);
    }
  };

  return (
    <div className="p-4 text-sm">
      <header className="mb-3">
        <h2 className="text-base font-semibold">Themes</h2>
        <p className="text-xs text-text-muted">
          A theme controls how rendered HTML looks. Each site picks its own
          theme; the active theme on the current site is highlighted below.
        </p>
      </header>

      {error && (
        <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>
      )}
      {loading && <div className="text-text-muted py-4">Loading…</div>}

      <ul className="grid grid-cols-1 md:grid-cols-2 gap-3 list-none p-0">
        {themes.map((t) => (
          <li
            key={t.slug}
            className={`border rounded p-3 flex flex-col ${
              t.active ? "border-border-strong" : "border-border"
            }`}
          >
            <div className="flex items-baseline justify-between gap-2">
              <h3 className="font-semibold">{t.name}</h3>
              <span className="text-xs text-text-muted">v{t.version}</span>
            </div>
            <p className="text-text-muted text-xs mt-1">
              Source: {t.source}
              {t.active && (
                <span className="ml-2 inline-block px-2 py-0.5 rounded bg-bg-input border border-border text-text">
                  Active
                </span>
              )}
            </p>
            <div className="flex gap-2 mt-3">
              <button
                disabled={t.active || switching === t.slug}
                onClick={() => activate(t.slug)}
                className="flex-1 px-3 py-1 rounded border border-border disabled:opacity-50"
              >
                {switching === t.slug
                  ? "Switching…"
                  : t.active
                    ? "Active"
                    : "Use this theme"}
              </button>
            </div>
          </li>
        ))}
        {!loading && themes.length === 0 && (
          <li className="col-span-full text-text-muted text-center py-8">
            No themes installed.
          </li>
        )}
      </ul>

      <footer className="text-text-muted text-xs pt-4 mt-4 border-t border-border">
        Multiple themes are coming in v2.2 — for now, the default ships with the
        binary. Custom themes will be loadable from the bound storage app under{" "}
        <code>/.themes/&lt;slug&gt;/</code> once that path is wired.
      </footer>
    </div>
  );
}

// ── blocks view (catalog browser) ────────────────────────────────

interface BlockTypeInfoFull {
  name: string;
  display_name: string;
  category: string;
  description: string;
  container?: boolean;
}

function BlocksView({ api }: { api: ReturnType<typeof makeAPI> }) {
  const [types, setTypes] = useState<BlockTypeInfoFull[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setLoading(true);
    api<{ types: BlockTypeInfoFull[] | null }>("/admin/block-types")
      .then((r) => setTypes(r.types ?? []))
      .catch((e) => setError(String(e)))
      .finally(() => setLoading(false));
  }, [api]);

  // Group by category, in a stable order that puts the common ones first.
  const ORDER = ["text", "media", "layout", "embed", "advanced"];
  const grouped = useMemo(() => {
    const out: Record<string, BlockTypeInfoFull[]> = {};
    for (const t of types) (out[t.category] ??= []).push(t);
    // Stable name order inside each category.
    for (const cat in out) out[cat].sort((a, b) => a.name.localeCompare(b.name));
    return out;
  }, [types]);

  const cats = Object.keys(grouped).sort(
    (a, b) => (ORDER.indexOf(a) === -1 ? 99 : ORDER.indexOf(a)) - (ORDER.indexOf(b) === -1 ? 99 : ORDER.indexOf(b)),
  );

  return (
    <div className="p-4 text-sm">
      <header className="mb-3">
        <h2 className="text-base font-semibold">Blocks</h2>
        <p className="text-xs text-text-muted">
          The catalog of block types available in the post editor. Use these
          as building blocks for pages and posts. Container blocks (✱) accept
          nested children — e.g. <code>columns</code>, <code>group</code>,{" "}
          <code>quote</code>, <code>cta</code>.
        </p>
      </header>

      {error && <div className="bg-red-100 text-red-800 rounded px-3 py-2 my-2">{error}</div>}
      {loading && <div className="text-text-muted py-4">Loading…</div>}

      {cats.map((cat) => (
        <section key={cat} className="mb-6">
          <h3 className="text-xs uppercase tracking-wider text-text-muted mb-2">
            {cat} <span className="opacity-50">({grouped[cat].length})</span>
          </h3>
          <ul className="grid grid-cols-1 md:grid-cols-2 gap-2 list-none p-0">
            {grouped[cat].map((t) => (
              <li key={t.name} className="border border-border rounded p-3">
                <div className="flex items-baseline justify-between gap-2">
                  <strong>{t.display_name}</strong>
                  {t.container && (
                    <span title="Container block" className="text-xs text-text-muted">✱</span>
                  )}
                </div>
                <p className="text-xs text-text-muted mt-1">
                  <code className="font-mono">{t.name}</code>
                </p>
                <p className="text-text-muted mt-2 leading-snug">{t.description}</p>
              </li>
            ))}
          </ul>
        </section>
      ))}

      <footer className="text-text-muted text-xs pt-4 mt-2 border-t border-border">
        Cross-app blocks (e.g. <code>image-studio/generated</code>,{" "}
        <code>crm/subscribe</code>) will appear here once those apps are bound
        and have registered their block types — scheduled for v2.5.
      </footer>
    </div>
  );
}
