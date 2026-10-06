import { afterEach, expect, test } from "bun:test";
import { subscribeAudioDashboardEvents, isAudioDashboardEvent } from "../../ui/audio-dashboard-events";
const host = { appName: "telephony", projectId: "p1", installId: 42 };
const event = { app: "telephony", project_id: "p1", install_id: 42, topic: "telephony.audio.reports.changed", seq: 1 };
const oldWindow = globalThis.window, oldSource = globalThis.EventSource;
afterEach(() => { globalThis.window = oldWindow; globalThis.EventSource = oldSource; });
test("SSE hints are scoped to the exact app, project, installation and topic", () => {
 expect(isAudioDashboardEvent(host, event)).toBe(true);
 for (const changed of [{ app: "crm" }, { project_id: "p2" }, { install_id: 43 }, { topic: "call.completed" }]) expect(isAudioDashboardEvent(host, { ...event, ...changed })).toBe(false);
});
test("reuses and cleans up the host's shared SSE subscription", () => {
 const win = new EventTarget() as unknown as Window;
 let receive!: (value: typeof event) => void, released = 0, changed = 0;
 (win as any).__aptevaAppEvents = { subscribe: (app: string, project: string, fn: typeof receive) => { expect(app).toBe("telephony"); expect(project).toBe("p1"); receive = fn; return () => released++; } };
 globalThis.window = win as Window & typeof globalThis;
 globalThis.EventSource = class { constructor() { throw new Error("must not allocate a widget EventSource"); } } as any;
 const modes: string[] = [];
 const stop = subscribeAudioDashboardEvents(host, () => changed++, (mode) => modes.push(mode));
 receive({ ...event, project_id: "p2" }); expect(changed).toBe(0);
 receive(event); expect(changed).toBe(1);
 const connected = new Event("apteva:app-events-connected"); (connected as any).detail = { projectId: "p1" };win.dispatchEvent(connected);expect(changed).toBe(2);
 stop();win.dispatchEvent(connected);expect(changed).toBe(2);expect(released).toBe(1);expect(modes).toContain("sse");
});
test("standalone SSE refreshes on connection and ignores malformed, duplicate and unrelated events", () => {
 globalThis.window = new EventTarget() as unknown as Window & typeof globalThis;
 const instances: any[] = [];
 class FakeSource {
  onmessage?: (message: {data: string}) => void;onopen?: () => void;onerror?: () => void;closed = false;
  constructor(public url: string, public options: any) { instances.push(this); }
  close() { this.closed = true; }
 }
 globalThis.EventSource = FakeSource as any;
 let changed = 0;const modes: string[]=[];
 const stop = subscribeAudioDashboardEvents(host, () => changed++, mode => modes.push(mode));
 const source=instances[0];expect(source.url).toContain("project_id=p1");expect(source.options.withCredentials).toBe(true);
 source.onopen();expect(changed).toBe(1);
 for(const data of ["invalid",JSON.stringify({...event,project_id:"p2"}),JSON.stringify(event),JSON.stringify(event)])source.onmessage({data});
 expect(changed).toBe(2);source.onerror();expect(source.closed).toBe(true);expect(modes.at(-1)).toBe("fallback");stop();
});
test("unavailable SSE selects polling fallback without throwing", () => {
 globalThis.window = new EventTarget() as unknown as Window & typeof globalThis;
 globalThis.EventSource = undefined as any;
 const modes: string[]=[];subscribeAudioDashboardEvents(host, () => {}, mode => modes.push(mode))();expect(modes).toEqual(["fallback"]);
});
