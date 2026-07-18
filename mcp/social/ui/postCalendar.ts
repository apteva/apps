export interface CalendarPostLike {
  status: string;
  schedule_at?: string;
  published_at?: string;
  created_at: string;
  targets?: { social_account_id: number }[];
}

export type PostViewMode = "list" | "calendar";
export type CalendarScale = "month" | "week";

export function stableSquareStyle(size: number) {
  return {
    width: size,
    height: size,
    minWidth: size,
    minHeight: size,
    maxWidth: size,
    maxHeight: size,
    flex: `0 0 ${size}px`,
    display: "grid",
    placeItems: "center",
    lineHeight: 1,
  } as const;
}

export function calendarPlatformMarkStyle(size = 18) {
  return {
    ...stableSquareStyle(size),
    boxSizing: "border-box",
    borderRadius: 4,
    fontSize: size <= 18 ? 8 : 9,
    fontWeight: 700,
    letterSpacing: 0,
    overflow: "hidden",
  } as const;
}

export function listLeadMediaStyle(size = 156) {
  return {
    width: size,
    height: size,
    minWidth: size,
    minHeight: size,
    maxWidth: size,
    maxHeight: size,
    flex: `0 0 ${size}px`,
    display: "grid",
    placeItems: "center",
    lineHeight: 1,
  } as const;
}

export function listPostRowStyle(height = 176) {
  return {
    height,
    minHeight: height,
    maxHeight: height,
  } as const;
}

export function postLifecycleDate(post: CalendarPostLike): Date | null {
  const source =
    (post.status === "published" || post.status === "partial") && post.published_at
      ? post.published_at
      : post.schedule_at || post.created_at;
  const parsed = new Date(source);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

export function localDateKey(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function startOfLocalWeek(date: Date): Date {
  const out = new Date(date.getFullYear(), date.getMonth(), date.getDate());
  const mondayOffset = (out.getDay() + 6) % 7;
  out.setDate(out.getDate() - mondayOffset);
  return out;
}

export function calendarWindow(cursor: Date, scale: CalendarScale): { start: Date; end: Date; days: Date[] } {
  const anchor = scale === "month"
    ? new Date(cursor.getFullYear(), cursor.getMonth(), 1)
    : new Date(cursor.getFullYear(), cursor.getMonth(), cursor.getDate());
  const start = startOfLocalWeek(anchor);
  const count = scale === "month" ? 42 : 7;
  const days = Array.from({ length: count }, (_, index) => {
    const day = new Date(start);
    day.setDate(start.getDate() + index);
    return day;
  });
  const end = new Date(start);
  end.setDate(start.getDate() + count);
  return { start, end, days };
}

export function moveCalendarCursor(cursor: Date, scale: CalendarScale, amount: number): Date {
  if (scale === "month") {
    return new Date(cursor.getFullYear(), cursor.getMonth() + amount, 1);
  }
  const next = new Date(cursor);
  next.setDate(next.getDate() + amount * 7);
  return next;
}

export function filterCalendarPosts<T extends CalendarPostLike>(
  posts: T[],
  status: string,
  accountID: string,
): T[] {
  const parsedAccountID = accountID === "all" ? 0 : Number(accountID);
  return posts.filter((post) => {
    if (status !== "all" && post.status !== status) return false;
    if (parsedAccountID > 0 && !post.targets?.some((target) => target.social_account_id === parsedAccountID)) {
      return false;
    }
    return true;
  });
}

export function sortPostList<T extends CalendarPostLike>(posts: T[], now = new Date()): T[] {
  const nowTime = now.getTime();
  const isUpcoming = (post: T) => {
    if (post.status !== "scheduled" || !post.schedule_at) return false;
    const scheduledTime = new Date(post.schedule_at).getTime();
    return !Number.isNaN(scheduledTime) && scheduledTime >= nowTime;
  };
  return [...posts].sort((left, right) => {
    const leftUpcoming = isUpcoming(left);
    const rightUpcoming = isUpcoming(right);
    if (leftUpcoming && rightUpcoming) {
      return new Date(left.schedule_at!).getTime() - new Date(right.schedule_at!).getTime();
    }
    if (leftUpcoming !== rightUpcoming) return leftUpcoming ? -1 : 1;
    return (postLifecycleDate(right)?.getTime() || 0) - (postLifecycleDate(left)?.getTime() || 0);
  });
}

export function groupPostsByLocalDay<T extends CalendarPostLike>(posts: T[]): Map<string, T[]> {
  const grouped = new Map<string, T[]>();
  for (const post of posts) {
    const date = postLifecycleDate(post);
    if (!date) continue;
    const key = localDateKey(date);
    const day = grouped.get(key) || [];
    day.push(post);
    grouped.set(key, day);
  }
  for (const day of grouped.values()) {
    day.sort((left, right) =>
      (postLifecycleDate(left)?.getTime() || 0) - (postLifecycleDate(right)?.getTime() || 0));
  }
  return grouped;
}
