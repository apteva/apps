import { describe, expect, test } from "bun:test";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { endpoint } from "./ActorsPanel";

describe("dashboard panel contract", () => {
  test("importing the bundle never reads or replaces the host root", async () => {
    const previous = globalThis.document;
    let rootReads = 0;
    globalThis.document = { getElementById() { rootReads++; throw new Error("panel touched host root"); } } as any;
    try {
      const panel = await import("./ActorsPanel.mjs");
      expect(typeof panel.default).toBe("function");
      expect(rootReads).toBe(0);
      const html = renderToStaticMarkup(React.createElement(panel.default, { projectId: "project-a", installId: 42 }));
      expect(html).toContain('aria-label="Actors sections"');
      expect(html).not.toContain('<form');
      expect(html).toContain('overflow-hidden');
      expect(rootReads).toBe(0);
    } finally {
      if (previous === undefined) delete globalThis.document;
      else globalThis.document = previous;
    }
  });

  test("Actors reads, writes, and dataset links target the injected installation", () => {
    for (const path of ["/actors", "/actors/run", "/tasks", "/runs", "/runs/5/dataset", "/schedules/2/cancel"]) {
      const url = new URL(endpoint(path, { projectId: "project a", installId: 42 }, { limit: "50", install_id: "wrong" }), "http://localhost");
      expect(url.pathname).toBe(`/api/apps/actors${path}`);
      expect(url.searchParams.get("project_id")).toBe("project a");
      expect(url.searchParams.get("install_id")).toBe("42");
      expect(url.searchParams.get("limit")).toBe("50");
    }
  });

  test("multiple panels do not share mutable installation scope", () => {
    expect(endpoint("/actors", { projectId: "a", installId: 1 })).toContain("install_id=1");
    expect(endpoint("/actors", { projectId: "b", installId: 2 })).toContain("install_id=2");
    expect(endpoint("/actors", { projectId: "a", installId: 1 })).toContain("project_id=a");
  });
});

describe("data explorer", () => {
  test("columns include schema fields missing from the first row", async () => {
    const { dataColumns } = await import("./ActorsPanel");
    expect(dataColumns([{ name: "A" }, { name: "B", score: 0 }], { name: "string", optional: "string?" })).toEqual(["name", "optional", "score"]);
  });
  test("CSV exports preserve zero, escape quotes and nested values, and quote formula-like text", async () => {
    const { csvRows } = await import("./ActorsPanel");
    const csv = csvRows([{ name: '=SUM(1,2)', score: 0, note: 'a"b', nested: { x: true } }], ["name", "score", "note", "nested"]);
    expect(csv).toContain('"\'=SUM(1,2)"');
    expect(csv).toContain('"0"');
    expect(csv).toContain('"a""b"');
    expect(csv).toContain('"{""x"":true}"');
  });
});
