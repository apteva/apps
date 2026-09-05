import {afterEach, expect, mock, test} from "bun:test";

type Node = {type: any; props: any; key?: string};
let cursor = 0;
let slots: any[] = [];
let callbacks: Function[] = [];
let effects: Function[] = [];
const jsx = (type: any, props: any, key?: string): Node => ({type, props, key});
mock.module("react", () => ({
  useState(initial: any) {const i = cursor++; if (!(i in slots)) slots[i] = typeof initial === "function" ? initial() : initial; return [slots[i], (value: any) => {slots[i] = typeof value === "function" ? value(slots[i]) : value;}];},
  useRef(initial: any) {const i = cursor++; if (!(i in slots)) slots[i] = {current: initial};return slots[i];},
  useCallback(fn: Function) {callbacks.push(fn);return fn;},
  useEffect(fn: Function) {effects.push(fn);},
}));
mock.module("react/jsx-runtime", () => ({jsx, jsxs: jsx, Fragment: "fragment"}));
mock.module("react/jsx-dev-runtime", () => ({jsxDEV: jsx, Fragment: "fragment"}));
const {default: JobsPanel} = await import("./JobsPanel");
const props = {projectId: "project-a", installId: 1, appName: "jobs"};
const nativeFetch = globalThis.fetch;
afterEach(() => {globalThis.fetch = nativeFetch;});
function nodes(node: any): Node[] {
  if (!node || typeof node !== "object") return [];
  if (Array.isArray(node)) return node.flatMap(nodes);
  return [node, ...nodes(node.props?.children)];
}
function setup() {
  slots = [];cursor = 0;callbacks = [];effects = [];
  const root: any = JobsPanel(props);
  const scope = root.type(root.props);
  const panel = nodes(scope).find(n => n.type?.name === "ScopedJobsPanel")!;
  slots = [];
  const render = () => {cursor = 0;callbacks = [];effects = [];return panel.type(panel.props);};
  return {render, tree: render()};
}
function deferred() {let resolve!: (r: Response) => void;const promise = new Promise<Response>(r => resolve = r);return {promise, resolve};}
function path(url: string) {return new URL(url, "https://test.invalid").pathname;}
const jobs = [{id: 1, name: "first", status: "pending", schedule_kind: "once"}, {id: 2, name: "second", status: "pending", schedule_kind: "once"}];
async function seedList() {await callbacks[2]();}
function detail(tree: any) {return nodes(tree).find(n => n.type?.name === "DetailDialog");}
function select(tree: any, id: number) {nodes(tree).find(n => n.type === "tr" && n.key === id)?.props.onClick();}
const flush = () => new Promise(r => setTimeout(r, 0));

test("newest selection wins when detail requests complete out of order", async () => {
  const old = deferred();globalThis.fetch = (async (url: string) => {
    if (path(url).endsWith("/jobs")) return Response.json({jobs});
    if (path(url).endsWith("/jobs/1")) return old.promise;
    if (path(url).endsWith("/runs")) return Response.json({runs: []});
    return Response.json({job: jobs[1]});
  }) as typeof fetch;
  const h = setup();await seedList();select(h.render(), 1);select(h.render(), 2);await flush();
  expect(detail(h.render())?.props.job.id).toBe(2);
  old.resolve(Response.json({job: jobs[0]}));await flush();
  expect(detail(h.render())?.props.job.id).toBe(2);
});
test("closing detail invalidates an outstanding refresh", async () => {
  let delayed = false;const pending = deferred();globalThis.fetch = (async (url: string) => {
    if (path(url).endsWith("/jobs")) return Response.json({jobs});
    if (path(url).endsWith("/runs")) return Response.json({runs: []});
    return delayed ? pending.promise : Response.json({job: jobs[0]});
  }) as typeof fetch;
  const h = setup();await seedList();select(h.render(), 1);await flush();const current = detail(h.render())!;
  delayed = true;const refresh = callbacks[3](1);current.props.onClose();pending.resolve(Response.json({job: jobs[0]}));await refresh;
  expect(detail(h.render())).toBeUndefined();
});
test("stale list responses cannot overwrite the latest list", async () => {
  const pending = deferred();let calls = 0;globalThis.fetch = (async () => ++calls === 1 ? pending.promise : Response.json({jobs: [jobs[1]]})) as typeof fetch;
  setup();const old = callbacks[2]();await callbacks[2]();pending.resolve(Response.json({jobs: [jobs[0]]}));await old;
  expect(slots[0].map((j: any) => j.id)).toEqual([2]);
});
test("unmount aborts requests and prevents state changes", async () => {
  const pending = deferred();let signal: AbortSignal | undefined;globalThis.fetch = (async (_url: string, init: RequestInit) => {signal = init.signal as AbortSignal;return pending.promise;}) as typeof fetch;
  setup();const cleanup = effects[0]();const loading = callbacks[2]();cleanup();expect(signal?.aborted).toBe(true);
  pending.resolve(Response.json({jobs}));await loading;expect(slots[0]).toEqual([]);
});
test("pagination sends the cursor and appends the next page", async () => {
  let query = "";globalThis.fetch = (async (url: string) => {query = url;return Response.json({jobs: [jobs[1]], next_cursor: 2});}) as typeof fetch;
  setup();slots[0] = [jobs[0]];await callbacks[2](3);expect(query).toContain("before=3");expect(slots[0].map((j: any) => j.id)).toEqual([1, 2]);
});
test("project and installation changes remount all scoped state", () => {
  const a: any = JobsPanel(props);const b: any = JobsPanel({...props, projectId: "project-b"});const c: any = JobsPanel({...props, installId: 2});expect(a.key).not.toBe(b.key);expect(a.key).not.toBe(c.key);
});
test("global requests omit the project query", async () => {
  slots = [];cursor = 0;const root: any = JobsPanel({...props, projectId: ""});const scope = root.type(root.props);const panel = nodes(scope).find(n => n.type?.name === "ScopedJobsPanel")!;
  slots = [];cursor = 0;callbacks = [];panel.type(panel.props);let query = "";
  globalThis.fetch = (async (url: string) => {query = url;return Response.json({jobs: []});}) as typeof fetch;
  await callbacks[2]();expect(query).toContain("scope=global");expect(query).not.toContain("project_id=");
});
