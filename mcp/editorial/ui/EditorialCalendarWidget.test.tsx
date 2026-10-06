import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import Widget, {
  brandColor,
  brandName,
  dateKey,
  eventDay,
  groupByDay,
  monthGrid,
  resolveView,
  startOfWeek,
  windowFor,
  type Brand,
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
  try {
    window.localStorage.clear();
  } catch {
    // Storage is optional; the widget and these tests work without it.
  }
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

const mount = async (
  props: Record<string, unknown>,
  payload: unknown,
  brands: Brand[] = [],
) => {
  const calls: string[] = [];
  globalThis.fetch = (async (url: string) => {
    const target = String(url);
    calls.push(target);
    const body = target.includes("/settings") ? { brands } : payload;
    return { ok: true, status: 200, json: async () => body };
  }) as unknown as typeof fetch;
  const host = document.createElement("div");
  document.body.appendChild(host);
  await act(async () => {
    root = createRoot(host);
    root.render(<Widget appName="editorial" projectId="alpha" {...props} />);
  });
  const calendarCalls = () => calls.filter((c) => c.includes("/calendar?"));
  return { host, calls, calendarCalls, lastCalendar: () => calendarCalls().at(-1)! };
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
  const { host, calendarCalls, lastCalendar } = await mount(
    { widgetSize: "half" },
    { events: [event({ at: dateKey(new Date()), date: dateKey(new Date()), title: "Ship the newsletter" })] },
  );
  expect(host.textContent).toContain("Ship the newsletter");
  expect(calendarCalls().length).toBe(1);
  expect(lastCalendar()).toContain("/api/apps/editorial/calendar?");
  expect(lastCalendar()).toContain("project_id=alpha");
  expect(lastCalendar()).toContain("from=");
  expect(lastCalendar()).toContain("include_releases=true");
  // Every brand by default: no brand_id is sent until something is chosen.
  expect(lastCalendar()).not.toContain("brand_id");
});

test("release settings and brand reach the query, and releases are marked apart", async () => {
  const today = dateKey(new Date());
  const { host, lastCalendar } = await mount(
    {
      widgetSize: "half",
      widgetSettings: { brand_id: "acme", show_releases: false, date_field: "deadline", horizon_days: 14 },
    },
    { events: [event({ kind: "release", release_id: 9, channel: "Newsletter", at: today, date: today })] },
  );
  expect(lastCalendar()).toContain("brand_id=acme");
  expect(lastCalendar()).toContain("include_releases=false");
  expect(lastCalendar()).toContain("date_field=deadline");
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

const BRANDS: Brand[] = [
  { id: "acme", name: "Acme", color: "#c2371f" },
  { id: "globex", name: "Globex", color: "#3f8ee0" },
];

test("brand lookups tolerate unknown ids and colourless brands", () => {
  expect(brandColor(BRANDS, "acme")).toBe("#c2371f");
  expect(brandName(BRANDS, "globex")).toBe("Globex");
  // Unassigned content and unknown ids fall back rather than throwing.
  expect(brandColor(BRANDS, "")).toBe("");
  expect(brandColor(BRANDS, "missing")).toBe("");
  expect(brandName(BRANDS, "missing")).toBe("");
  expect(brandColor([{ id: "x", name: "X", color: "" }], "x")).toBe("");
});

test("every brand shows by default and the picker offers all, each brand, and unassigned", async () => {
  const today = dateKey(new Date());
  const { host, lastCalendar } = await mount(
    { widgetSize: "full" },
    { events: [event({ at: today, date: today, brand_id: "acme" })] },
    BRANDS,
  );
  const select = host.querySelector("select") as HTMLSelectElement;
  expect(select).toBeTruthy();
  expect([...select.options].map((o) => o.value)).toEqual(["", "acme", "globex", "unassigned"]);
  expect([...select.options].map((o) => o.textContent)).toEqual([
    "All brands",
    "Acme",
    "Globex",
    "Unassigned",
  ]);
  // Default selection is every brand, so the request carries no brand filter.
  expect(select.value).toBe("");
  expect(lastCalendar()).not.toContain("brand_id");
});

test("choosing a brand refetches the calendar scoped to it", async () => {
  const today = dateKey(new Date());
  const { host, calendarCalls, lastCalendar } = await mount(
    { widgetSize: "full" },
    { events: [event({ at: today, date: today, brand_id: "acme" })] },
    BRANDS,
  );
  expect(calendarCalls().length).toBe(1);

  const select = host.querySelector("select") as HTMLSelectElement;
  await act(async () => {
    select.value = "globex";
    select.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
  expect(calendarCalls().length).toBe(2);
  expect(lastCalendar()).toContain("brand_id=globex");

  // "Unassigned" is a real selection, not the same as clearing the filter.
  await act(async () => {
    select.value = "unassigned";
    select.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
  expect(lastCalendar()).toContain("brand_id=unassigned");

  await act(async () => {
    select.value = "";
    select.dispatchEvent(new window.Event("change", { bubbles: true }));
  });
  expect(lastCalendar()).not.toContain("brand_id");
});

test("entries carry their brand's colour, and unbranded ones keep the kind colour", async () => {
  const today = dateKey(new Date());
  const { host } = await mount(
    { widgetSize: "half" },
    {
      events: [
        event({ item_id: 1, at: today, date: today, brand_id: "acme", title: "Branded" }),
        event({ item_id: 2, at: today, date: today, brand_id: "", title: "House" }),
      ],
    },
    BRANDS,
  );
  const marks = host.querySelectorAll(".ec-mark");
  expect(marks.length).toBe(2);
  // Inline colour only where a brand supplies one; otherwise the stylesheet wins.
  expect((marks[0] as HTMLElement).style.background).toBeTruthy();
  expect((marks[1] as HTMLElement).style.background).toBe("");
  // Colour is never the only carrier of the brand.
  expect(host.querySelector('[title="Branded — Acme"]')).toBeTruthy();
  expect(host.textContent).toContain("Acme");
});

test("a project with no brands gets no picker and no brand chrome", async () => {
  const today = dateKey(new Date());
  const { host, lastCalendar } = await mount(
    { widgetSize: "full" },
    { events: [event({ at: today, date: today })] },
    [],
  );
  expect(host.querySelector("select")).toBeNull();
  expect(lastCalendar()).not.toContain("brand_id");
});
