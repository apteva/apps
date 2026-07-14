import { expect, test } from "bun:test";
import {
  calendarWindow,
  filterCalendarPosts,
  groupPostsByLocalDay,
  localDateKey,
  moveCalendarCursor,
  postLifecycleDate,
} from "./postCalendar";

test("postLifecycleDate follows lifecycle status", () => {
  const base = { created_at: "2026-07-01T10:00:00Z", targets: [] };
  expect(postLifecycleDate({ ...base, status: "published", schedule_at: "2026-07-02T10:00:00Z", published_at: "2026-07-03T10:00:00Z" })?.toISOString())
    .toBe("2026-07-03T10:00:00.000Z");
  expect(postLifecycleDate({ ...base, status: "scheduled", schedule_at: "2026-07-04T10:00:00Z" })?.toISOString())
    .toBe("2026-07-04T10:00:00.000Z");
  expect(postLifecycleDate({ ...base, status: "failed", published_at: "2026-07-05T10:00:00Z" })?.toISOString())
    .toBe("2026-07-01T10:00:00.000Z");
  expect(postLifecycleDate({ ...base, status: "draft" })?.toISOString())
    .toBe("2026-07-01T10:00:00.000Z");
});

test("calendarWindow uses stable Monday-first month and week ranges", () => {
  const cursor = new Date(2026, 6, 15, 12, 0, 0);
  const month = calendarWindow(cursor, "month");
  expect(month.days).toHaveLength(42);
  expect(month.start.getDay()).toBe(1);
  expect(month.end.getTime() - month.start.getTime()).toBeGreaterThanOrEqual(41 * 24 * 60 * 60 * 1000);

  const week = calendarWindow(cursor, "week");
  expect(week.days).toHaveLength(7);
  expect(week.start.getDay()).toBe(1);
  expect(moveCalendarCursor(cursor, "week", 1).getDate()).toBe(22);
});

test("filters by status/account and groups in lifecycle order", () => {
  const posts = [
    { id: 1, status: "scheduled", schedule_at: "2026-07-15T12:00:00Z", published_at: "", created_at: "2026-07-01T00:00:00Z", targets: [{ social_account_id: 9 }] },
    { id: 2, status: "scheduled", schedule_at: "2026-07-15T08:00:00Z", published_at: "", created_at: "2026-07-01T00:00:00Z", targets: [{ social_account_id: 9 }] },
    { id: 3, status: "published", schedule_at: "", published_at: "2026-07-15T10:00:00Z", created_at: "2026-07-01T00:00:00Z", targets: [{ social_account_id: 7 }] },
  ];
  const filtered = filterCalendarPosts(posts, "scheduled", "9");
  expect(filtered.map((post) => post.id)).toEqual([1, 2]);
  const grouped = groupPostsByLocalDay(filtered);
  const key = localDateKey(postLifecycleDate(filtered[0])!);
  expect(grouped.get(key)?.map((post) => post.id)).toEqual([2, 1]);
});
