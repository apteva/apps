import React from "react";
import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, expect, test } from "bun:test";
import ProspectingPanel from "./ProspectingPanel";

(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
const originalFetch = globalThis.fetch;
let renderer: ReactTestRenderer | undefined;
afterEach(async () => { if (renderer) await act(async () => renderer!.unmount()); renderer = undefined; globalThis.fetch = originalFetch; });
const profile = { id: 8, name: "US staffing", industries: ["staffing"], locations: ["Dallas, Texas"], target_titles: [], keywords: [], status: "active" };
const run = { id: 91, status: "completed_with_errors", options: { query: "staffing in Dallas, Texas", source: "google_places", crm_mode: "auto" }, progress: { phase: "qualify", places_requests: 1 }, counts: { created: 2, transferred: 1, failed: 1 }, items: [{ source_key: "first", candidate_id: 1, crm_contact_id: 701, status: "transferred", reason: "added to CRM" }, { source_key: "second", candidate_id: 2, status: "failed", reason: "CRM temporarily unavailable" }] };
function setup() {
 const writes: Array<{ path: string; body: any }> = [];
 globalThis.fetch = (async (url: string, init?: RequestInit) => {
  const path = new URL(String(url), "http://localhost").pathname.replace("/api/apps/prospecting", "");
  if (init?.method && init.method !== "GET") writes.push({ path, body: init.body ? JSON.parse(String(init.body)) : null });
  const payload: Record<string, any> = { "/capabilities": { web: true, crm: true, google_places: true }, "/overview": {}, "/profiles": { profiles: [profile] }, "/candidates": { candidates: [] }, "/runs": { runs: [] }, "/exclusions": { exclusions: [] }, "/pipeline/runs": init?.method === "POST" ? { run } : { runs: [run] }, "/settings": { places_connection_id: 31, daily_places_request_limit: 100 }, "/connections": { connections: [{ id: 31, name: "Test Places", status: "active" }] }, "/pipeline/runs/91/resume": { run: { ...run, status: "queued" } } };
  return new Response(JSON.stringify(payload[path] || {}), { headers: { "Content-Type": "application/json" } });
 }) as typeof fetch;
 return writes;
}
const text = (node: any): string => typeof node === "string" ? node : (node.children || []).map(text).join("");
async function clickButton(label: string) { const button = renderer!.root.findAllByType("button").find(b => text(b) === label); expect(button).toBeDefined(); await act(async () => { button!.props.onClick(); await new Promise(r => setTimeout(r, 5)); }); }
async function mount() { await act(async () => { renderer = create(<ProspectingPanel appName="prospecting" installId={1641} projectId="project-a" />); await new Promise(r => setTimeout(r, 5)); }); }

test("agent-equivalent UI starts automatic CRM run with thresholds and displays retryable failures", async () => {
 const writes = setup(); await mount(); await clickButton("Discover");
 const source = renderer!.root.findAllByType("select").find(s => s.props.value === "google_places"); expect(source).toBeDefined();
 const mode = renderer!.root.findAllByType("select").find(s => s.props.value === "review");
 await act(async () => mode!.props.onChange({ target: { value: "auto" } }));
 await clickButton("Start prospecting run");
 const write = writes.find(w => w.path === "/pipeline/runs");
 expect(write?.body).toMatchObject({ profile_id: 8, source: "google_places", crm_mode: "auto", qualify: true, min_fit_score: 70, min_confidence_score: 60, limit: 20 });
 expect(write?.body.idempotency_key).toBeString();
 expect(text(renderer!.root)).toContain("CRM #701"); expect(text(renderer!.root)).toContain("CRM temporarily unavailable");
 await clickButton("Retry failed steps"); expect(writes.at(-1)?.path).toBe("/pipeline/runs/91/resume");
});

test("Places connection and daily request budget can be saved in Settings", async () => {
 const writes = setup(); await mount(); await clickButton("Settings"); await clickButton("Save connection");
 expect(writes.at(-1)).toEqual({ path: "/settings", body: { places_connection_id: 31, daily_places_request_limit: 100 } });
 expect(text(renderer!.root)).not.toContain("api_key");
});
