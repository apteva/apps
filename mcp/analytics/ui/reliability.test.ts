import { describe, test, expect } from "bun:test";
import { createRefreshQueue } from "./live-refresh";
import { areaChartGeometry, formatMetric } from "./dashboard-ui";
import { requestFilters, encodeFilter } from "./FilterControl";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
function clock() {
  let now = 0,
    id = 0;
  const jobs = new Map<number, { at: number; fn: () => void }>();
  return {
    schedule: ((fn: () => void, ms: number) => {
      jobs.set(++id, { at: now + ms, fn });
      return id;
    }) as any,
    cancel: ((id: number) => jobs.delete(id)) as any,
    async advance(ms: number) {
      now += ms;
      for (const [id, job] of [...jobs])
        if (job.at <= now) {
          jobs.delete(id);
          job.fn();
        }
      await Promise.resolve();
      await Promise.resolve();
    },
    size: () => jobs.size,
  };
}
describe("live refresh lifecycle", () => {
  test("continuous events do not starve a refresh", async () => {
    const c = clock();
    let calls = 0;
    const q = createRefreshQueue(
      async () => {
        calls++;
      },
      1000,
      c.schedule,
      c.cancel,
    );
    for (let i = 0; i < 120; i++) {
      q.request();
      await c.advance(500);
    }
    expect(calls).toBeGreaterThanOrEqual(59);
    expect(calls).toBeLessThanOrEqual(60);
    q.dispose();
    expect(c.size()).toBe(0);
  });
  test("one in flight, one trailing refresh, no work after unmount", async () => {
    const c = clock();
    let calls = 0,
      release = () => {};
    const q = createRefreshQueue(
      () => {
        calls++;
        return new Promise<void>((r) => (release = r));
      },
      1000,
      c.schedule,
      c.cancel,
    );
    q.request();
    await c.advance(1000);
    for (let i = 0; i < 100; i++) {
      q.request();
      await c.advance(100);
    }
    expect(calls).toBe(1);
    release();
    await Promise.resolve();
    await c.advance(1000);
    expect(calls).toBe(2);
    q.dispose();
    release();
    await Promise.resolve();
    await c.advance(10000);
    expect(calls).toBe(2);
  });
});
describe("typed filters and honest charts", () => {
  test("preserves null, false, numeric ID and literal all", () => {
    for (const v of [null, false, 42, "42", "all", [true, 2]])
      expect(requestFilters({ v: encodeFilter(v) })).toEqual({
        v: { value: v },
      });
  });
  test("missing observations break the line and timestamp distances are proportional", () => {
    const g = areaChartGeometry([1, null, 2, 3], 300, 72, [0, 1, 2, 10]);
    expect(g.points).toHaveLength(3);
    expect(g.linePath.match(/M/g)).toHaveLength(2);
    expect(g.points[1].x).toBeCloseTo(64.8);
    expect(g.linePath).not.toContain("NaN");
  });
  test("empty measurements and invalid currencies cannot crash rendering", () => {
    expect(formatMetric(null, {})).toBe("—");
    expect(formatMetric(1, { format: "currency", currency: "invalid" })).toBe(
      "$1",
    );
    expect(
      formatMetric(25, { format: "percent", percent_input: "points" }),
    ).toBe("25%");
  });
});
const tag = readFileSync(new URL("./tag.js", import.meta.url), "utf8");
function runTag(storage: Map<string, string>, now: number, reject = false) {
  const fetched: string[] = [],
    images: string[] = [];
  const location = {
    href: "https://example.com/reset?token=secret#private",
    origin: "https://example.com",
    host: "example.com",
    pathname: "/reset",
    search: "?token=secret",
  };
  const window: any = { addEventListener() {} };
  runInNewContext(tag, {
    document: {
      currentScript: {
        src: "https://analytics.test/ui/tag.js",
        getAttribute: () => "write-key",
      },
      title: "Test",
      referrer: "",
    },
    window,
    localStorage: {
      getItem: (k: string) => storage.get(k) ?? null,
      setItem: (k: string, v: string) => storage.set(k, v),
    },
    location,
    navigator: { userAgent: "test", language: "en" },
    history: {},
    screen: { width: 100, height: 100 },
    URL,
    Date: { now: () => now },
    Math,
    Number,
    fetch: (url: string) => {
      fetched.push(url);
      return reject
        ? Promise.reject(new Error("offline"))
        : Promise.resolve({});
    },
    Image: class {
      set src(v: string) {
        images.push(v);
      }
    },
    console,
  });
  return { fetched, images, window };
}
describe("tracking tag delivery and sessions", () => {
  test("shares active session across tabs and rotates after inactivity", () => {
    const storage = new Map<string, string>();
    const a = new URL(runTag(storage, 1000).fetched[0]);
    const b = new URL(runTag(storage, 1100).fetched[0]);
    const c = new URL(runTag(storage, 31 * 60 * 1000).fetched[0]);
    expect(a.searchParams.get("sid")).toBe(b.searchParams.get("sid"));
    expect(c.searchParams.get("sid")).not.toBe(a.searchParams.get("sid"));
    expect(JSON.parse(c.searchParams.get("p")!).visitor_id).toBe(
      JSON.parse(a.searchParams.get("p")!).visitor_id,
    );
  });
  test("rejected fetch falls back with the identical idempotency ID", async () => {
    const { fetched, images } = runTag(new Map(), 1000, true);
    await Promise.resolve();
    expect(images).toEqual(fetched);
    expect(new URL(fetched[0]).searchParams.get("eid")).toBeTruthy();
  });
  test("scrubs query and fragment before transport", () => {
    const u = new URL(runTag(new Map(), 1000).fetched[0]);
    expect(u.searchParams.get("url")).toBe("https://example.com/reset");
    expect(u.searchParams.get("path")).toBe("/reset");
    expect(u.toString()).not.toContain("secret");
  });
});
