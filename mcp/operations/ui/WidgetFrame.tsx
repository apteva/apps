import type { ReactNode } from "react";

// Common tile chrome: title, a short line under it, a link to the
// Operations page, then the body or a message.
export function WidgetFrame({ title, sub, link, linkLabel = "Open", error, loading, empty, children }: {
  title: string; sub?: ReactNode; link?: string; linkLabel?: string; error?: string; loading?: boolean; empty?: string; children?: ReactNode;
}) {
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card text-text" aria-label={title}>
      <header className="flex items-center gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-semibold">{title}</h2>
          {sub && <p className="mt-0.5 truncate text-xs text-text-dim">{sub}</p>}
        </div>
        {link && <a href={link} className="shrink-0 text-xs text-text-muted hover:text-accent">{linkLabel}</a>}
      </header>
      <div className="min-h-0 flex-1 overflow-auto">
        {error ? <p className="px-4 py-5 text-xs text-red">{error}</p>
          : loading ? <p className="px-4 py-5 text-xs text-text-dim">Loading…</p>
          : empty ? <p className="px-4 py-5 text-xs text-text-dim">{empty}</p>
          : children}
      </div>
    </section>
  );
}

export function NoProject({ title }: { title: string }) {
  return (
    <section className="flex h-full min-h-0 items-center justify-center rounded-lg border border-border bg-bg-card p-5 text-center">
      <div>
        <p className="text-sm font-semibold text-text">{title}</p>
        <p className="mt-1 text-xs text-text-dim">Select a project to see its operations.</p>
      </div>
    </section>
  );
}
