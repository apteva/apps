import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, {
  dateKey,
  eventDay,
  groupByDay,
  monthGrid,
  resolveView,
  startOfWeek,
  windowFor,
  type CalendarEvent,
} from "./EditorialCalendarWidget";

const originalFetch = globalThis.fetch;
const window = new Window({ url: "http://localhost" });
Object.assign(globalThis, {
  window,
  document: window.document,
  HTMLElement: window.HTMLElement,
  IS_REACT_ACT_ENVIRONMENT: true,
});
let root: Root;
afterEach(async () => {
  await act(async () => root?.unmount());
  document.body.innerHTML = "";
  globalThis.fetch = originalFetch;
});

const event = (over: Partial<CalendarEvent>): CalendarEvent => ({
  kind: "item",
  date: "2026-10-10",
  at: "2026-10-10",
  item_id: 1,
  title: "Launch article",
  brand_id: "",
  format: "article",
  status: "brief",
  approval: "not_required",
  owner: "",
  ...over,
});

const mount = async (props: Record<string, unknown>, payload: unknown) => {
  const calls: string[] = [];
  globalThis.fetch = (async (url: string) => {
    calls.push(String(url));
    return { ok: true, status: 200, json: async () => payload };
  }) as unknown as typeof fetch;
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<Widget appName="editorial" projectId="alpha" {...props} />);
  });
  return { host, calls };
};

test("half width falls back to the agenda, full width gets the grid", () => {
  expect(resolveView("auto", "half")).toBe("list");
  expect(resolveView("auto", "full")).toBe("month");
  // An explicit operator choice wins over the size heuristic.
  expect(resolveView("month", "half")).toBe("month");
  expect(resolveView("week", "full")).toBe("week");
  // An unknown stored value must not leave the widget with no view at all.
  expect(resolveView("nonsense", "full")).toBe("month");
});

test("the month grid starts on Monday and covers whole weeks", () => {
  // October 2026 starts on a Thursday.
  const grid = monthGrid(new Date(2026, 9, 1));
  expect(grid.length % 7).toBe(0);
  expect(grid[0].getDay()).toBe(1);
  expect(dateKey(grid[0])).toBe("2026-09-28");
  expect(grid.some((d) => dateKey(d) === "2026-10-31")).toBe(true);
  expect(dateKey(startOfWeek(new Date(2026, 9, 10)))).toBe("2026-10-05");
});

test("requests pad the window so a timestamp cannot fall off either edge", () => {
  const month = windowFor("month", new Date(2026, 9, 15), 30);
  expect(month.from).toBe("2026-09-27");
  expect(month.to).toBe("2026-11-02");

  const week = windowFor("week", new Date(2026, 9, 15), 30);
  expect(week.from).toBe("2026-10-11");
  expect(week.to).toBe("2026-10-19");

  const list = windowFor("list", new Date(2026, 9, 15), 7);
  expect(list.from).toBe("2026-10-14");
  expect(list.to).toBe("2026-10-23");
});

test("events bucket on the viewer's local day, not the stored prefix", () => {
  const plain = event({ at: "2026-10-10", date: "2026-10-10" });
  expect(eventDay(plain)).toBe("2026-10-10");

  // A timestamp resolves through the local timezone exactly as the panel does.
  const stamped = event({ at: "2026-10-10T23:30:00Z", date: "2026-10-10" });
  expect(eventDay(stamped)).toBe(dateKey(new Date("2026-10-10T23:30:00Z")));

  const grouped = groupByDay([
    event({ item_id: 1, at: "2026-10-10" }),
    event({ item_id: 2, at: "2026-10-10" }),
    event({ item_id: 3, at: "2026-10-12" }),
    event({ item_id: 4, at: "", date: "" }),
  ]);
  expect(grouped["2026-10-10"].length).toBe(2);
  expect(grouped["2026-10-12"].length).toBe(1);
  // An undated event has no cell to live in and is dropped rather than guessed.
  // (The endpoint only returns dated rows; this guards the render path anyway.)
  expect(Object.keys(grouped).length).toBe(2);
});

test("renders the agenda at half width and asks the calendar endpoint for a window", async () => {
  const { host, calls } = await mount(
    { widgetSize: "half" },
    { events: [event({ at: dateKey(new Date()), date: dateKey(new Date()), title: "Ship the newsletter" })] },
  );
  expect(host.textContent).toContain("Ship the newsletter");
  expect(calls.length).toBe(1);
  expect(calls[0]).toContain("/api/apps/editorial/calendar?");
  expect(calls[0]).toContain("project_id=alpha");
  expect(calls[0]).toContain("from=");
  expect(calls[0]).toContain("include_releases=true");
});

test("release settings and brand reach the query, and releases are marked apart", async () => {
  const today = dateKey(new Date());
  const { host, calls } = await mount(
    {
      widgetSize: "half",
      widgetSettings: { brand_id: "acme", show_releases: false, date_field: "deadline", horizon_days: 14 },
    },
    { events: [event({ kind: "release", release_id: 9, channel: "Newsletter", at: today, date: today })] },
  );
  expect(calls[0]).toContain("brand_id=acme");
  expect(calls[0]).toContain("include_releases=false");
  expect(calls[0]).toContain("date_field=deadline");
  expect(host.textContent).toContain("Newsletter");
  expect(host.textContent).toContain("Editorial deadlines");
});

test("full width renders a month grid with weekday headers", async () => {
  const today = dateKey(new Date());
  const { host } = await mount({ widgetSize: "full" }, { events: [event({ at: today, date: today })] });
  expect(host.querySelectorAll('[role="gridcell"]').length % 7).toBe(0);
  expect(host.textContent).toContain("Mon");
  expect(host.textContent).toContain("Launch article");
});

test("a failed load explains itself instead of rendering an empty calendar", async () => {
  globalThis.fetch = (async () => ({
    ok: false,
    status: 500,
    json: async () => ({ error: "database is locked" }),
  })) as unknown as typeof fetch;
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<Widget appName="editorial" projectId="alpha" widgetSize="half" />);
  });
  expect(host.textContent).toContain("database is locked");
});

test("a truncated range says so rather than quietly hiding work", async () => {
  const today = dateKey(new Date());
  const { host } = await mount(
    { widgetSize: "half" },
    { events: [event({ at: today, date: today })], truncated: true },
  );
  expect(host.textContent).toContain("Open Editorial for the full calendar");
});
