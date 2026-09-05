import { describe, expect, test } from "bun:test";
import { corsEnabledFrom, corsCredentialsFrom, updatedCORS, LatestRequest, fetchPanelRows } from "./policy";

describe("CORS editing", () => {
  test("preserves advanced policy settings during a simple edit", () => {
    const prior = { enabled: true, origins: ["https://old.example"], allow_methods: ["POST"], allow_headers: ["x-custom"], expose_headers: ["x-result"], max_age: 60 };
    expect(updatedCORS(JSON.stringify(prior), true, "https://new.example", false)).toEqual({ ...prior, origins: ["https://new.example"], credentials: false });
  });
  test("explicit false wins over existing origins and legacy aliases", () => {
    expect(corsEnabledFrom('{"enabled":false,"origins":["https://example.com"]}')).toBe(false);
    expect(corsCredentialsFrom('{"credentials":false,"allow_credentials":true}')).toBe(false);
    expect(updatedCORS('{"origins":["https://example.com"],"max_age":30}', false, "https://example.com", false)).toMatchObject({ enabled: false, max_age: 30 });
  });
});

test("slow prior selection cannot overwrite a newer completed selection", async () => {
  const requests = new LatestRequest();
  let resolveOld!: (r: Response) => void;
  const oldResponse = new Promise<Response>(resolve => { resolveOld = resolve; });
  const fake = ((url: string) => url.includes("old") ? oldResponse : Promise.resolve(Response.json({ routes: [{ id: 2 }] }))) as typeof fetch;
  let visible: unknown[] = [];
  const old = requests.begin();
  const loadOld = fetchPanelRows(fake, "https://test/old", "routes", old.signal).then(rows => { if (old.current()) visible = rows; });
  const next = requests.begin();
  const rows = await fetchPanelRows(fake, "https://test/new", "routes", next.signal);
  if (next.current()) visible = rows;
  resolveOld(Response.json({ routes: [{ id: 1 }] }));
  await loadOld;
  expect(old.signal.aborted).toBe(true);
  expect(visible).toEqual([{ id: 2 }]);
});

test("failed detail loading remains an error rather than an empty list", async () => {
  const fake = (() => Promise.resolve(new Response("not authorized", { status: 403 }))) as typeof fetch;
  await expect(fetchPanelRows(fake, "https://test/rows", "routes", new AbortController().signal)).rejects.toThrow("403");
});
