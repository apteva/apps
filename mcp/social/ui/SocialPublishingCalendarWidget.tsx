import { useEffect, useState } from "react";
import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { groupPostsByLocalDay, localDateKey, moveCalendarCursor, postLifecycleDate } from "./postCalendar";
import { postStatusVariant, postTitle, type SocialPost } from "./socialCardData";
import { platformPresentation } from "./platformPresentation";
import { previewPublishingSummary, publishingWindow, socialPageLink, useWidgetData, widgetPreferences, widgetURL, type HomeWidgetProps, type PublishingSummary } from "./homeWidgetData";

export default function SocialPublishingCalendarWidget(props: HomeWidgetProps) {
  const prefs = widgetPreferences(props);
  const [cursor, setCursor] = useState(() => new Date());
  // Keep the window stable between minute ticks to avoid request loops.
  const [now, setNow] = useState(() => new Date());
  useEffect(() => { const tick = setInterval(() => setNow(new Date()), 60_000); return () => clearInterval(tick); }, []);
  const range = publishingWindow(now, prefs.mode, prefs.horizonDays, cursor);
  const url = widgetURL(props, "/widgets/publishing-calendar", { mode: prefs.mode, from: range.start.toISOString(), to: range.end.toISOString(), limit: prefs.maxPosts });
  const { data, error, retry } = useWidgetData<PublishingSummary>(url, props, previewPublishingSummary);
  const grouped = groupPostsByLocalDay(data?.posts || []);
  const openCalendar = socialPageLink(props, { view: "calendar" });
  const today = localDateKey(new Date());
  return (
    <section className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card">
      <header className="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
        <div className="min-w-0"><h2 className="flex items-center gap-2 text-sm font-bold text-text"><CalendarDays size={16} aria-hidden />Social · Publishing calendar</h2>
          <p className="mt-1 text-[11px] text-text-dim">{prefs.mode === "calendar" ? `${shortDate(range.start)} – ${shortDate(range.days[6])}` : `Scheduled over the next ${prefs.horizonDays} days`}</p></div>
        <a href={openCalendar} className="shrink-0 text-[11px] text-text-muted hover:text-accent">Open calendar →</a>
      </header>
      {prefs.showAttention && data && (data.attention.failed + data.attention.approval + data.attention.overdue > 0) && (
        <div className="flex flex-wrap gap-x-3 gap-y-1 border-b border-border bg-bg-input/40 px-4 py-2 text-[11px]">
          {data.attention.failed > 0 && <a href={socialPageLink(props, { status: "attention" })} className="text-error hover:underline">{data.attention.failed} failed or partial</a>}
          {data.attention.approval > 0 && <a href={socialPageLink(props, { status: "in_review" })} className="text-warn hover:underline">{data.attention.approval} awaiting approval</a>}
          {data.attention.overdue > 0 && <a href={socialPageLink(props, { status: "scheduled" })} className="text-warn hover:underline">{data.attention.overdue} overdue</a>}
        </div>
      )}
      {error && <div role="alert" className="flex items-center gap-2 px-4 py-2 text-xs text-error">{error}<button className="underline" onClick={retry}>Retry</button></div>}
      {!data ? <p className="min-h-28 px-4 py-6 text-xs text-text-dim">{error ? "" : "Loading publishing calendar…"}</p> : prefs.mode === "calendar" ? (
        <div className="min-h-0 flex-1 overflow-auto p-3">
          <div className="mb-3 flex items-center gap-2">
            <button aria-label="Previous week" className="rounded border border-border p-1.5 hover:bg-bg-hover" onClick={() => setCursor(moveCalendarCursor(cursor, "week", -1))}><ChevronLeft size={14} /></button>
            <button className="rounded border border-border px-2 py-1 text-xs hover:bg-bg-hover" onClick={() => { const date = new Date(); setCursor(date); setNow(date); }}>Today</button>
            <button aria-label="Next week" className="rounded border border-border p-1.5 hover:bg-bg-hover" onClick={() => setCursor(moveCalendarCursor(cursor, "week", 1))}><ChevronRight size={14} /></button>
            <span className="ml-auto text-[11px] text-text-dim">{data.total} posts</span>
          </div>
          <div className="grid gap-2" style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))", minWidth: 630 }}>
            {range.days.map(day => {
              const key = localDateKey(day); const posts = grouped.get(key) || [];
              return <div key={key} className={`min-h-32 min-w-0 rounded border p-2 ${key === today ? "border-accent/60 bg-accent/5" : "border-border"}`}>
                <p className={`mb-2 text-[11px] font-semibold ${key === today ? "text-accent" : "text-text-muted"}`}>{day.toLocaleDateString(undefined, { weekday: "short", day: "numeric" })}</p>
                <div className="space-y-2">{posts.slice(0, 4).map(post => <PostRow key={post.id} post={post} props={props} compact />)}</div>
                {posts.length === 0 && <span className="text-[10px] text-text-dim">No posts</span>}
                {posts.length > 4 && <a href={socialPageLink(props, { view: "calendar", anchor_date: key })} className="mt-2 block text-[10px] text-accent">+{posts.length - 4} more</a>}
              </div>;
            })}
          </div>
          {data.total > data.posts.length && <p className="mt-2 text-[11px] text-text-dim">Showing the first {data.posts.length} posts. <a className="text-accent" href={openCalendar}>Open the calendar for more.</a></p>}
        </div>
      ) : data.posts.length === 0 ? (
        <div className="flex min-h-28 flex-1 flex-col justify-center px-4 py-5"><p className="text-xs font-semibold text-text">No posts scheduled in this window</p><p className="mt-1 text-[11px] text-text-dim">Create a post to keep your publishing calendar moving.</p></div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto px-4 py-3">
          {[...grouped].map(([day, posts]) => <div key={day} className="mb-3 last:mb-0"><p className="mb-2 text-[10px] font-bold uppercase tracking-wide text-text-dim">{day === today ? "Today" : shortDate(new Date(`${day}T12:00:00`))}</p><div className="space-y-2">{posts.map(post => <PostRow key={post.id} post={post} props={props} />)}</div></div>)}
          {data.total > data.posts.length && <a className="text-[11px] text-accent" href={openCalendar}>{data.total - data.posts.length} more scheduled posts →</a>}
        </div>
      )}
      <footer className="flex items-center justify-between border-t border-border px-4 py-2 text-[11px] text-text-dim"><span>Times in your local timezone</span><a href={socialPageLink(props, { compose: 1 })} className="font-semibold text-accent">Create post →</a></footer>
    </section>
  );
}

function shortDate(date: Date) { return date.toLocaleDateString(undefined, { month: "short", day: "numeric" }); }

function PostRow({ post, props, compact = false }: { post: SocialPost; props: HomeWidgetProps; compact?: boolean }) {
  const lead = post.external_media_urls?.[0] || (post.media_storage_ids?.[0] ? `/api/apps/storage/files/${post.media_storage_ids[0]}/content?project_id=${encodeURIComponent(props.projectId || "")}` : "");
  const platforms = [...new Set(post.targets.map(target => target.platform))];
  const variant = postStatusVariant(post.status);
  const date = postLifecycleDate(post);
  return <a href={socialPageLink(props, { post: post.id })} title={postTitle(post)} className={`flex gap-2 rounded border border-border bg-bg-input/30 p-2 hover:border-accent/50 ${compact ? "flex-col" : "items-center"}`}>
    {lead && <img src={lead} alt="" loading="lazy" onError={event => { event.currentTarget.hidden = true; }} className={`rounded object-cover ${compact ? "h-16 w-full" : "h-10 w-10 shrink-0"}`} />}
    <div className="min-w-0 flex-1"><p className={`text-xs font-semibold text-text ${compact ? "line-clamp-2" : "truncate"}`}>{postTitle(post)}</p>
      <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] text-text-muted">
        <span>{date?.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })}</span>
        {platforms.map(platform => { const item = platformPresentation(platform); return <span key={platform} title={item.label} aria-label={item.label} className="rounded border border-border px-1 font-bold" style={{ color: item.key === "x" || item.key === "threads" ? "var(--text)" : item.color }}>{item.mark}</span>; })}
        <span className={variant === "error" ? "text-error" : variant === "warn" ? "text-warn" : variant === "live" ? "text-success" : "text-text-dim"}>{post.status.replace(/_/g, " ")}</span>
      </div>
    </div>
  </a>;
}
