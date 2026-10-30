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
function runSpaTag() {
  const fetched: string[] = [];
  const timers: (() => void)[] = [];
  const clickListeners: ((e: any) => void)[] = [];
  const windowListeners: Record<string, () => void> = {};
  const location = {
    href: "https://example.com/",
    origin: "https://example.com",
    host: "example.com",
    pathname: "/",
    search: "",
  };
  const document: any = {
    currentScript: {
      src: "https://analytics.test/ui/tag.js",
      getAttribute: () => "write-key",
    },
    title: "Home",
    referrer: "",
    addEventListener: (_: string, fn: (e: any) => void) => clickListeners.push(fn),
  };
  const history: any = {
    pushState(_s: unknown, _t: string, path: string) {
      location.pathname = path;
    },
  };
  runInNewContext(tag, {
    document,
    window: {
      addEventListener: (name: string, fn: () => void) => (windowListeners[name] = fn),
    },
    localStorage: { getItem: () => null, setItem() {} },
    location,
    navigator: { userAgent: "test", language: "en" },
    history,
    screen: { width: 100, height: 100 },
    URL,
    Date,
    Math,
    Number,
    setTimeout: (fn: () => void) => timers.push(fn),
    clearTimeout() {},
    fetch: (url: string) => {
      fetched.push(url);
      return Promise.resolve({});
    },
    Image: class {},
    console,
  });
  const sent = () => fetched.map((u) => new URL(u).searchParams);
  return { history, document, timers, clickListeners, windowListeners, sent };
}
describe("tracking tag SPA and outbound links", () => {
  test("route change page_view waits for the new document.title", () => {
    const t = runSpaTag();
    t.history.pushState({}, "", "/pricing");
    t.document.title = "Pricing";
    expect(t.sent()).toHaveLength(1);
    t.timers.forEach((fn) => fn());
    const pv = t.sent()[1];
    expect(pv.get("path")).toBe("/pricing");
    expect(pv.get("title")).toBe("Pricing");
  });
  test("a newer route change or page exit flushes the pending page_view", () => {
    const t = runSpaTag();
    t.history.pushState({}, "", "/a");
    t.history.pushState({}, "", "/b");
    expect(t.sent().map((p) => p.get("path"))).toEqual(["/", "/a"]);
    t.windowListeners.pagehide();
    expect(t.sent().map((p) => p.get("path"))).toEqual(["/", "/a", "/b"]);
    t.timers.forEach((fn) => fn());
    expect(t.sent()).toHaveLength(3);
  });
  test("outbound_click drops the destination query and fragment", () => {
    const t = runSpaTag();
    const anchor = {
      tagName: "A",
      href: "https://go.example.net/r/42?aff=secret#frag",
      textContent: "Buy",
      hasAttribute: () => false,
      getAttribute: () => null,
    };
    t.clickListeners[0]({ target: anchor });
    const click = t.sent()[1];
    const props = JSON.parse(click.get("p")!);
    expect(click.get("e")).toBe("outbound_click");
    expect(props.link_url).toBe("https://go.example.net/r/42");
    expect(props.link_path).toBe("/r/42");
    expect(JSON.stringify(props)).not.toContain("secret");
  });
});
type TagOptions = {
  attrs?: Record<string, string>;
  storage?: Map<string, string>;
  search?: string;
  referrer?: string;
  now?: number;
  reject?: boolean;
  window?: any;
};
function runConfiguredTag(o: TagOptions = {}) {
  const storage = o.storage ?? new Map<string, string>();
  const fetched: string[] = [];
  const images: any[] = [];
  const replaced: string[] = [];
  const clickListeners: ((e: any) => void)[] = [];
  const now = o.now ?? 1_000_000;
  const location: any = {
    origin: "https://example.com",
    host: "example.com",
    hostname: "example.com",
    protocol: "https:",
    pathname: "/landing",
    search: o.search ?? "",
    hash: "",
  };
  const document: any = {
    currentScript: {
      src: "https://analytics.test/ui/tag.js",
      getAttribute: (name: string) =>
        name === "data-key" ? "write-key" : (o.attrs?.[name] ?? null),
    },
    title: "Landing",
    referrer: o.referrer ?? "",
    cookie: "",
    addEventListener: (_: string, fn: (e: any) => void) => clickListeners.push(fn),
  };
  const history: any = {
    state: null,
    replaceState(_s: unknown, _t: string, path: string) {
      replaced.push(path);
      const [p, s] = path.split("?");
      location.pathname = p;
      location.search = s ? "?" + s : "";
    },
  };
  const window = o.window ?? { listeners: {} as Record<string, () => void> };
  window.addEventListener = (name: string, fn: () => void) => (window.listeners[name] = fn);
  runInNewContext(tag, {
    document,
    window,
    localStorage: {
      getItem: (k: string) => storage.get(k) ?? null,
      setItem: (k: string, v: string) => storage.set(k, v),
      removeItem: (k: string) => storage.delete(k),
    },
    location,
    navigator: { userAgent: "test", language: "en" },
    history,
    screen: { width: 100, height: 100 },
    URL,
    Date: { now: () => now },
    Math,
    Number,
    setTimeout: () => 0,
    clearTimeout() {},
    fetch: (url: string) => {
      fetched.push(url);
      return o.reject ? Promise.reject(new Error("offline")) : Promise.resolve({});
    },
    Image: class {
      onerror: (() => void) | null = null;
      set src(v: string) {
        images.push(this);
        (this as any).url = v;
      }
    },
    console,
  });
  const props = (i: number) => JSON.parse(new URL(fetched[i]).searchParams.get("p")!);
  return { storage, fetched, images, replaced, clickListeners, document, window, props };
}
describe("tracking tag acquisition attribution", () => {
  test("keeps the first touch and updates the latest; direct visits change nothing", () => {
    const storage = new Map<string, string>();
    const day = 24 * 60 * 60 * 1000;
    runConfiguredTag({ storage, search: "?utm_source=newsletter&utm_medium=email", now: day });
    const later = runConfiguredTag({
      storage,
      search: "?utm_source=google&utm_medium=cpc&utm_campaign=brand%20q4",
      now: 2 * day,
    });
    let p = later.props(0);
    expect(p.first_utm_source).toBe("newsletter");
    expect(p.first_touch_ts).toBe(day);
    expect(p.first_landing).toBe("/landing");
    expect(p.utm_source).toBe("google");
    expect(p.utm_campaign).toBe("brand q4");
    expect(p.touch_ts).toBe(2 * day);
    const direct = runConfiguredTag({ storage, now: 3 * day });
    p = direct.props(0);
    expect(p.utm_source).toBe("google");
    expect(p.first_utm_source).toBe("newsletter");
  });
  test("external referrers are touches, own and linked hosts are not", () => {
    const ext = runConfiguredTag({ referrer: "https://news.ycombinator.com/item?id=1" }).props(0);
    expect(ext.touch_referrer).toBe("news.ycombinator.com");
    expect(ext.first_referrer).toBe("news.ycombinator.com");
    for (const referrer of ["https://example.com/pricing", "https://www.example.com/", "https://app.example.org/"]) {
      const p = runConfiguredTag({
        referrer,
        attrs: { "data-cookie-domain": ".example.com", "data-linker": "app.example.org" },
      }).props(0);
      expect(p.touch_referrer).toBeUndefined();
    }
  });
  test("touches expire after data-attribution-days", () => {
    const storage = new Map<string, string>();
    const day = 24 * 60 * 60 * 1000;
    runConfiguredTag({ storage, search: "?utm_source=old", now: day, attrs: { "data-attribution-days": "7" } });
    const p = runConfiguredTag({ storage, now: 9 * day, attrs: { "data-attribution-days": "7" } }).props(0);
    expect(p.first_utm_source).toBeUndefined();
    expect(p.utm_source).toBeUndefined();
  });
  test("custom props win over attribution props", () => {
    const t = runConfiguredTag({ search: "?utm_source=google" });
    t.window.apa("signup", { utm_source: "partner", plan: "pro" });
    expect(t.props(1).utm_source).toBe("partner");
    expect(t.props(1).plan).toBe("pro");
  });
});
describe("tracking tag consent, identity and delivery", () => {
  test("consent required keeps nothing on the device until granted, denied wipes it", () => {
    const t = runConfiguredTag({
      search: "?utm_source=google",
      attrs: { "data-consent": "required", "data-cookie": "true" },
    });
    expect(t.storage.size).toBe(0);
    expect(t.document.cookie).toBe("");
    const vid = t.props(0).visitor_id;
    expect(t.props(0).utm_source).toBe("google");
    t.window.apa("consent", "granted");
    expect(t.storage.get("apa_vid")).toBe(vid);
    expect(JSON.parse(t.storage.get("apa_attr_v1")!).first.source).toBe("google");
    expect(t.document.cookie).toContain("apa_vid=" + vid);
    expect(t.document.cookie).toContain("Secure");
    t.window.apa("consent", "denied");
    expect(t.storage.size).toBe(0);
    expect(t.document.cookie).toContain("Max-Age=0");
    expect(t.window.apa.visitorId()).not.toBe(vid);
    expect(t.fetched).toHaveLength(1);
  });
  test("cookie is opt-in and exposes the visitor ID for backend identify", () => {
    expect(runConfiguredTag().document.cookie).toBe("");
    const t = runConfiguredTag({ attrs: { "data-cookie-domain": ".example.com" } });
    expect(t.document.cookie).toContain("apa_vid=" + t.window.apa.visitorId());
    expect(t.document.cookie).toContain("Domain=example.com");
    expect(t.props(0).visitor_id).toBe(t.window.apa.visitorId());
  });
  test("network failures queue the delivery and the next load retries it with its age", async () => {
    const storage = new Map<string, string>();
    const failed = runConfiguredTag({ storage, reject: true, now: 1_000_000 });
    await Promise.resolve();
    failed.images[0].onerror();
    const queue = JSON.parse(storage.get("apa_queue_v1")!);
    expect(queue).toHaveLength(1);
    expect(queue[0].u).toBe(failed.fetched[0]);
    const next = runConfiguredTag({ storage, now: 1_060_000 });
    expect(next.fetched).toHaveLength(2);
    const retried = new URL(next.fetched[1]);
    expect(retried.searchParams.get("eid")).toBe(new URL(failed.fetched[0]).searchParams.get("eid"));
    expect(retried.searchParams.get("age")).toBe("60000");
    expect(storage.get("apa_queue_v1")).toBe("[]");
  });
  test("queued deliveries older than a day are dropped", () => {
    const storage = new Map<string, string>([
      ["apa_queue_v1", JSON.stringify([{ u: "https://analytics.test/collect?k=x&eid=old", t: 0 }])],
    ]);
    const t = runConfiguredTag({ storage, now: 25 * 60 * 60 * 1000 });
    expect(t.fetched).toHaveLength(1);
  });
  test("loading the tag twice does not double-track", () => {
    const window = { listeners: {} };
    const first = runConfiguredTag({ window });
    const second = runConfiguredTag({ window });
    expect(first.fetched).toHaveLength(1);
    expect(second.fetched).toHaveLength(0);
  });
});
describe("tracking tag cross-domain handoff", () => {
  const link = (t: ReturnType<typeof runConfiguredTag>, href: string) => {
    const anchor: any = {
      tagName: "A",
      href,
      textContent: "Go",
      hasAttribute: () => false,
      getAttribute: () => null,
    };
    t.clickListeners[0]({ target: anchor });
    return anchor.href as string;
  };
  test("decorates links to approved hosts only", () => {
    const t = runConfiguredTag({ attrs: { "data-linker": "app.example.org" }, now: 1_000_000 });
    const approved = new URL(link(t, "https://app.example.org/signup?plan=pro"));
    const [vid, sid, at] = approved.searchParams.get("_apa")!.split(".");
    expect(vid).toBe(t.window.apa.visitorId());
    expect(sid).toBe(new URL(t.fetched[0]).searchParams.get("sid"));
    expect(parseInt(at, 36)).toBe(1_000_000);
    expect(approved.searchParams.get("plan")).toBe("pro");
    expect(link(t, "https://other.example.net/")).toBe("https://other.example.net/");
  });
  test("adopts a fresh handoff and removes it from the address bar", () => {
    const at = (1_000_000).toString(36);
    const t = runConfiguredTag({
      search: `?plan=pro&_apa=s_visitorabc.s_sessionabc.${at}`,
      referrer: "https://example.org/pricing",
      attrs: { "data-linker": "example.org" },
      now: 1_060_000,
    });
    expect(t.replaced).toEqual(["/landing?plan=pro"]);
    expect(t.props(0).visitor_id).toBe("s_visitorabc");
    expect(new URL(t.fetched[0]).searchParams.get("sid")).toBe("s_sessionabc");
    expect(t.props(0).touch_referrer).toBeUndefined();
    expect(t.storage.get("apa_vid")).toBe("s_visitorabc");
  });
  test("ignores stale, foreign-referrer, or malformed handoffs but still strips them", () => {
    const at = (1_000_000).toString(36);
    for (const [search, referrer, now] of [
      [`?_apa=s_visitorabc.s_sessionabc.${at}`, "", 1_000_000 + 10 * 60 * 1000],
      [`?_apa=s_visitorabc.s_sessionabc.${at}`, "https://evil.example/", 1_000_000],
      [`?_apa=not-an-id.s_sessionabc.${at}`, "", 1_000_000],
    ] as const) {
      const t = runConfiguredTag({ search, referrer, now, attrs: { "data-linker": "example.org" } });
      expect(t.props(0).visitor_id).not.toBe("s_visitorabc");
      expect(t.replaced).toEqual(["/landing"]);
    }
  });
});
