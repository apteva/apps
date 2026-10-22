import { afterEach, expect, test } from "bun:test";
import { bytes, endpoint, request, saveSettings, settingsPatch, managedSettings } from "./api";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

test("requests target the selected installation and project", () => {
  expect(endpoint(42, "a/b & c", "/status")).toBe("/api/apps/turn/_install/42/status?project_id=a%2Fb+%26+c");
  expect(endpoint(42, "", "/status")).toBe("/api/apps/turn/_install/42/status");
  expect(() => endpoint(0, "a", "/status")).toThrow();
});

test("settings preserve existing secrets unless deliberately replaced", () => {
  const initial = { public_host: "turn.example.com", realm: "apteva" };
  expect(settingsPatch(initial, { ...initial, realm: "team" }, "", "", "")).toEqual({ realm: "team" });
  expect(() => settingsPatch(initial, initial, "certificate", "", "")).toThrow("both");
  expect(settingsPatch(initial, initial, "certificate", "private-key", "new-secret")).toEqual({ tls_certificate_pem: "certificate", tls_private_key_pem: "private-key", shared_secret: "new-secret" });
});

test("invalid settings are never written to platform configuration", async () => {
  const calls: string[] = [];
  globalThis.fetch = (async (url: string) => { calls.push(url); return new Response("Certificate does not cover hostname", { status: 400 }); }) as typeof fetch;
  await expect(saveSettings({ appName: "turn", installId: 42, projectId: "p" }, { public_host: "wrong.example.com" })).rejects.toThrow("Certificate does not cover hostname");
  expect(calls).toEqual(["/api/apps/turn/_install/42/settings/validate?project_id=p"]);
});

test("validated settings use the platform persistence API and return only restart status", async () => {
  const calls: { url: string; options: RequestInit }[] = [];
  globalThis.fetch = (async (url: string, options: RequestInit) => {
    calls.push({ url, options });
    return Response.json(calls.length === 1 ? { valid: true } : { restarted: true, config: { shared_secret: "do-not-retain", tls_private_key_pem: "private-key" } });
  }) as typeof fetch;
  expect(await saveSettings({ appName: "turn", installId: 42, projectId: "p" }, { realm: "team" })).toBe(true);
  expect(calls[1].url).toBe("/api/apps/installs/42/config");
  expect(calls[1].options.method).toBe("PUT");
  expect(calls[1].options.credentials).toBe("same-origin");
  expect(JSON.parse(String(calls[1].options.body))).toEqual({ config: { realm: "team" } });
});

test("relay requests do not cache credentials and surface bounded errors", async () => {
  let options: RequestInit | undefined;
  globalThis.fetch = (async (_url: string, supplied: RequestInit) => { options = supplied; return Response.json({ ok: true }); }) as typeof fetch;
  expect(await request("/test", { session_id: "call" })).toEqual({ ok: true });
  expect(options?.cache).toBe("no-store");
  expect(options?.credentials).toBe("same-origin");
  globalThis.fetch = (async () => new Response("x".repeat(1000), { status: 503 })) as typeof fetch;
  try { await request("/test"); throw new Error("expected rejection"); } catch (error) { expect((error as Error).message.length).toBe(400); }
  expect(bytes(1048576)).toBe("1.0 MiB");
});


test("managed TLS generates a stable storage key only during first setup", () => {
 const draft = {tls_mode:"managed", dns_zone:"example.com"};
 const initial = managedSettings({}, draft, false, "p");
 expect(initial.tls_storage_key).toMatch(/^[0-9a-f]{96}$/);
 expect(initial.dns_project_id).toBe("p");
 expect(managedSettings({}, draft, true, "")).toEqual({});
 expect(managedSettings({}, {tls_mode:"manual"}, false, "p")).toEqual({});
});
