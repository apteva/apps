import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { CalendarDays, ChevronLeft, ChevronRight } from "lucide-react";
import { Card, CardHeader, type CardVendor } from "@apteva/ui-kit";
import { platformPresentation } from "./platformPresentation";
import {
  calendarWindow,
  filterCalendarPosts,
  groupPostsByLocalDay,
  localDateKey,
  moveCalendarCursor,
  postLifecycleDate,
  stableSquareStyle,
  type CalendarScale,
} from "./postCalendar";
import {
  fetchSocialCalendar,
  isPostLifecycleEvent,
  postStatusColor,
  postTitle,
  previewCalendarPosts,
  subscribeSocialEvents,
  type SocialPost,
} from "./socialCardData";

interface Props {
  profile_id?: number;
  social_account_id?: number;
  view?: CalendarScale;
  status?: "all" | "scheduled" | "published" | "failed" | "partial" | "draft";
  anchor_date?: string;
  projectId?: string;
  preview?: boolean;
}

const socialVendor: CardVendor = {
  name: "Social",
  logo: <CalendarDays size={14} aria-hidden />,
  color: { light: "#C2410C", dark: "#FB923C" },
};

export default function SocialCalendarCard(props: Props) {
  const [scale, setScale] = useState<CalendarScale>(props.view === "month" ? "month" : "week");
  const [cursor, setCursor] = useState(() => parseAnchor(props.anchor_date));
  const [posts, setPosts] = useState<SocialPost[]>(props.preview ? previewCalendarPosts : []);
  const [loading, setLoading] = useState(!props.preview);
  const [error, setError] = useState("");
  const [compact, setCompact] = useState(false);
  const bodyRef = useRef<HTMLDivElement | null>(null);
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const range = calendarWindow(cursor, scale);
  const rangeStart = range.start.toISOString();
  const rangeEnd = range.end.toISOString();
  const load = useCallback(async () => {
    if (props.preview) return;
    try {
      const next = await fetchSocialCalendar({
        start: new Date(rangeStart),
        end: new Date(rangeEnd),
        projectId: props.projectId,
        profileID: props.profile_id,
      });
      setPosts(next);
      setError("");
    } catch (cause) {
      setError((cause as Error).message || "Couldn't load the social calendar");
    } finally {
      setLoading(false);
    }
  }, [props.preview, props.projectId, props.profile_id, rangeEnd, rangeStart]);

  useEffect(() => {
    setLoading(!props.preview);
    void load();
    if (props.preview) return;
    const interval = globalThis.setInterval(() => void load(), 60_000);
    return () => globalThis.clearInterval(interval);
  }, [load, props.preview]);

  useEffect(() => {
    if (props.preview) return;
    return subscribeSocialEvents(props.projectId, (event) => {
      if (!isPostLifecycleEvent(event.topic)) return;
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
      refreshTimer.current = setTimeout(() => void load(), 150);
    });
  }, [load, props.preview, props.projectId]);

  useEffect(() => () => {
    if (refreshTimer.current) clearTimeout(refreshTimer.current);
  }, []);

  useEffect(() => {
    const element = bodyRef.current;
    if (!element) return;
    const measure = () => setCompact(element.getBoundingClientRect().width < 560);
    measure();
    window.addEventListener("resize", measure);
    if (typeof ResizeObserver === "undefined") {
      return () => window.removeEventListener("resize", measure);
    }
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, []);

  const visiblePosts = useMemo(() => filterCalendarPosts(
    posts,
    props.status || "all",
    props.social_account_id ? String(props.social_account_id) : "all",
  ), [posts, props.social_account_id, props.status]);
  const grouped = useMemo(() => groupPostsByLocalDay(visiblePosts), [visiblePosts]);
  const heading = periodHeading(cursor, scale, range.days);

  return (
    <Card>
      <CardHeader
        vendor={socialVendor}
        title={heading}
        subtitle={scopeLabel(props)}
        status={{ label: loading ? "updating" : `${visiblePosts.length} post${visiblePosts.length === 1 ? "" : "s"}`, variant: "muted" }}
        action={{ label: "Open calendar", href: "/apps/social?view=calendar" }}
      />
      <div ref={bodyRef} className="px-3 py-3 flex flex-col gap-3">
        <div className="flex items-center gap-2">
          <IconButton label="Previous period" onClick={() => setCursor(moveCalendarCursor(cursor, scale, -1))}>
            <ChevronLeft size={16} />
          </IconButton>
          <button
            type="button"
            onClick={() => setCursor(new Date())}
            className="h-8 px-3 border border-border rounded text-xs text-text hover:bg-bg-input"
          >
            Today
          </button>
          <IconButton label="Next period" onClick={() => setCursor(moveCalendarCursor(cursor, scale, 1))}>
            <ChevronRight size={16} />
          </IconButton>
          <div className="ml-auto inline-flex border border-border rounded overflow-hidden" aria-label="Calendar scale">
            <ScaleButton active={scale === "week"} onClick={() => setScale("week")}>Week</ScaleButton>
            <ScaleButton active={scale === "month"} onClick={() => setScale("month")} divided>Month</ScaleButton>
          </div>
        </div>

        {error ? (
          <div className="text-xs text-red py-4 text-center">{error}</div>
        ) : compact ? (
          <Agenda days={range.days} grouped={grouped} loading={loading} />
        ) : (
          <CalendarGrid days={range.days} grouped={grouped} cursor={cursor} scale={scale} loading={loading} />
        )}
      </div>
    </Card>
  );
}

function CalendarGrid({ days, grouped, cursor, scale, loading }: {
  days: Date[];
  grouped: Map<string, SocialPost[]>;
  cursor: Date;
  scale: CalendarScale;
  loading: boolean;
}) {
  const today = localDateKey(new Date());
  return (
    <div className="overflow-x-auto border border-border rounded">
      <div className="grid bg-bg-input/50 border-b border-border" style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))", minWidth: 560 }}>
        {days.slice(0, 7).map((day) => (
          <div key={day.getDay()} className="py-1.5 text-center text-[10px] uppercase text-text-dim border-r border-border last:border-r-0">
            {day.toLocaleDateString(undefined, { weekday: "short" })}
          </div>
        ))}
      </div>
      <div className="grid" style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))", minWidth: 560 }}>
        {days.map((day, index) => {
          const key = localDateKey(day);
          const dayPosts = grouped.get(key) || [];
          const outsideMonth = scale === "month" && day.getMonth() !== cursor.getMonth();
          return (
            <div
              key={key}
              className={`p-1 border-b border-border ${index % 7 !== 6 ? "border-r border-border" : ""} ${outsideMonth ? "bg-bg/40" : "bg-bg"}`}
              style={{ minHeight: scale === "month" ? 82 : 142 }}
            >
              <div className="flex items-center justify-between mb-1">
                <span
                  className={`text-[10px] rounded ${key === today ? "bg-accent text-bg font-bold" : outsideMonth ? "text-text-dim" : "text-text"}`}
                  style={stableSquareStyle(20)}
                >
                  {day.getDate()}
                </span>
                {dayPosts.length > 0 && <span className="text-[9px] text-text-dim">{dayPosts.length}</span>}
              </div>
              <div className="flex flex-col gap-1">
                {dayPosts.slice(0, scale === "month" ? 2 : 4).map((post) => <CalendarEvent key={post.id} post={post} compact />)}
                {dayPosts.length > (scale === "month" ? 2 : 4) && (
                  <span className="text-[9px] text-text-dim px-1">+{dayPosts.length - (scale === "month" ? 2 : 4)} more</span>
                )}
              </div>
            </div>
          );
        })}
      </div>
      {loading && <div className="px-3 py-2 text-[10px] text-text-dim border-t border-border">Refreshing…</div>}
    </div>
  );
}

function Agenda({ days, grouped, loading }: { days: Date[]; grouped: Map<string, SocialPost[]>; loading: boolean }) {
  const populated = days.filter((day) => (grouped.get(localDateKey(day)) || []).length > 0);
  if (populated.length === 0 && !loading) {
    return <div className="py-8 text-center text-xs text-text-dim">No posts in this period.</div>;
  }
  return (
    <div className="flex flex-col gap-3">
      {populated.map((day) => (
        <section key={localDateKey(day)} className="flex flex-col gap-1.5">
          <div className="text-[10px] uppercase text-text-dim">
            {day.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric" })}
          </div>
          {(grouped.get(localDateKey(day)) || []).map((post) => <CalendarEvent key={post.id} post={post} />)}
        </section>
      ))}
      {loading && <div className="text-[10px] text-text-dim">Refreshing…</div>}
    </div>
  );
}

function CalendarEvent({ post, compact = false }: { post: SocialPost; compact?: boolean }) {
  const date = postLifecycleDate(post);
  const platforms = Array.from(new Set(post.targets.map((target) => target.platform).filter(Boolean)));
  return (
    <a
      href={`/apps/social?view=calendar&post=${post.id}`}
      className={`block border border-border rounded bg-bg-card hover:border-border-strong ${compact ? "px-1 py-1" : "px-2 py-2"}`}
      style={{ borderLeftColor: postStatusColor(post.status), borderLeftWidth: 2 }}
    >
      <div className="flex items-center gap-1.5 min-w-0">
        <span className={`${compact ? "text-[9px]" : "text-xs"} text-text tabular-nums flex-shrink-0`}>
          {date?.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
        </span>
        <span className="flex gap-0.5 min-w-0">
          {platforms.slice(0, compact ? 1 : 3).map((platform) => <PlatformMark key={platform} platform={platform} compact={compact} />)}
        </span>
        {!compact && <span className="text-[10px] uppercase text-text-dim ml-auto">{post.status}</span>}
      </div>
      <div className={`${compact ? "text-[9px] mt-0.5" : "text-xs mt-1"} text-text truncate`}>{postTitle(post)}</div>
    </a>
  );
}

function PlatformMark({ platform, compact }: { platform: string; compact: boolean }) {
  const presentation = platformPresentation(platform);
  return (
    <span
      title={presentation.label}
      className="inline-grid place-items-center rounded font-bold flex-shrink-0"
      style={{
        width: compact ? 16 : 20,
        height: compact ? 16 : 20,
        fontSize: compact ? 7 : 8,
        color: presentation.color,
        backgroundColor: `${presentation.color}1F`,
        border: `1px solid ${presentation.color}88`,
      }}
    >
      {presentation.mark}
    </span>
  );
}

function IconButton({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="w-8 h-8 grid place-items-center border border-border rounded text-text hover:bg-bg-input"
      aria-label={label}
      title={label}
    >
      {children}
    </button>
  );
}

function ScaleButton({ active, divided, onClick, children }: {
  active: boolean;
  divided?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`h-8 px-2.5 text-xs ${divided ? "border-l border-border" : ""} ${active ? "bg-bg-input text-text" : "text-text-muted hover:text-text"}`}
    >
      {children}
    </button>
  );
}

function parseAnchor(raw?: string): Date {
  const parsed = raw ? new Date(raw) : new Date();
  return Number.isNaN(parsed.getTime()) ? new Date() : parsed;
}

function periodHeading(cursor: Date, scale: CalendarScale, days: Date[]): string {
  if (scale === "month") return cursor.toLocaleDateString(undefined, { month: "long", year: "numeric" });
  return `${days[0].toLocaleDateString(undefined, { month: "short", day: "numeric" })} – ${days[6].toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}`;
}

function scopeLabel(props: Props): string {
  const filters = [];
  if (props.profile_id) filters.push(`profile #${props.profile_id}`);
  if (props.social_account_id) filters.push(`account #${props.social_account_id}`);
  if (props.status && props.status !== "all") filters.push(props.status);
  return filters.join(" · ") || "All scheduled and published posts";
}
