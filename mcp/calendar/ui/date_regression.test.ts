import { test, expect } from "bun:test";
import {
  occCoversDay,
  eventEditTimes,
  layoutTimedEvents,
  eventKey,
  fromZonedInput,
  toZonedInput,
  type Occurrence,
} from "./CalendarPanel";
const event = (extra: Partial<Occurrence> = {}): Occurrence => ({
  id: 1,
  event_id: 1,
  calendar_id: 1,
  title: "Meeting",
  description: "",
  location: "",
  start_at: "2026-05-04T09:00:00Z",
  end_at: "2026-05-04T10:00:00Z",
  all_day: false,
  status: "confirmed",
  is_recurring: false,
  occurrence_start_at: "2026-05-04T09:00:00Z",
  timezone: "Europe/Madrid",
  rrule: "",
  ...extra,
});
// Run with TZ=Europe/Madrid, including in CI, to exercise local date boundaries.
test("spring DST day excludes next-day event", () => {
  expect(
    occCoversDay(
      event({
        start_at: "2026-03-30T00:30:00+02:00",
        end_at: "2026-03-30T01:00:00+02:00",
      }),
      new Date("2026-03-29T12:00:00+02:00"),
    ),
  ).toBe(false);
});
test("autumn DST day includes 23:30 event", () => {
  expect(
    occCoversDay(
      event({
        start_at: "2026-10-25T23:30:00+01:00",
        end_at: "2026-10-25T23:45:00+01:00",
      }),
      new Date("2026-10-25T12:00:00+01:00"),
    ),
  ).toBe(true);
});
test("date-only holiday occupies one day", () => {
  const e = event({
    all_day: true,
    start_at: "2026-07-14T00:00:00Z",
    end_at: "2026-07-15T00:00:00Z",
  });
  expect(occCoversDay(e, new Date("2026-07-14T12:00:00"))).toBe(true);
  expect(occCoversDay(e, new Date("2026-07-15T12:00:00"))).toBe(false);
});
test("series title edit omits unchanged master dates", () => {
  const e = event({
    is_recurring: true,
    start_at: "2026-05-06T09:00:00Z",
    end_at: "2026-05-06T10:00:00Z",
  });
  expect(
    eventEditTimes(
      e,
      "all",
      "2026-05-04T09:00:00Z",
      "2026-05-04T10:00:00Z",
      event(),
    ),
  ).toEqual({ scope: "all" });
});
test("occurrence edit preserves selected occurrence identity", () => {
  const e = event({ is_recurring: true });
  expect(eventEditTimes(e, "this", e.start_at, e.end_at, null)).toEqual({
    scope: "this",
    occurrence_start_at: e.occurrence_start_at,
  });
});
test("concurrent meetings occupy separate lanes and disjoint groups reclaim full width", () => {
  const a = event();
  const b = event({
    id: 2,
    start_at: "2026-05-04T09:30:00Z",
    end_at: "2026-05-04T10:30:00Z",
  });
  const c = event({
    id: 3,
    start_at: "2026-05-04T11:00:00Z",
    end_at: "2026-05-04T12:00:00Z",
  });
  const layout = layoutTimedEvents([a, b, c]);
  expect(layout.get(eventKey(a))).toEqual({ column: 0, columns: 2 });
  expect(layout.get(eventKey(b))).toEqual({ column: 1, columns: 2 });
  expect(layout.get(eventKey(c))).toEqual({ column: 0, columns: 1 });
});
test("timezone input conversion keeps chosen local time", () => {
  expect(
    fromZonedInput("2026-03-30T09:00", "Europe/Madrid").toISOString(),
  ).toBe("2026-03-30T07:00:00.000Z");
  expect(toZonedInput(new Date("2026-03-23T08:00:00Z"), "Europe/Madrid")).toBe(
    "2026-03-23T09:00",
  );
});
test("nonexistent spring-forward local time is rejected", () => {
  expect(() => fromZonedInput("2026-03-29T02:30", "Europe/Madrid")).toThrow();
});
