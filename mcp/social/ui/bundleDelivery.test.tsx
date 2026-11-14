import { expect, test } from "bun:test";
import { readFile } from "node:fs/promises";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { parse } from "yaml";

const appRoot = new URL("../", import.meta.url);
const manifest = parse(await readFile(new URL("apteva.yaml", appRoot), "utf8"));
const entries: string[] = [
  ...manifest.provides.ui_panels,
  ...manifest.provides.ui_components,
].map((surface: { entry: string }) => surface.entry);
const hostImports = new Set(["react", "react/jsx-runtime", "react/jsx-dev-runtime", "@apteva/ui-kit"]);
const parser = new Bun.Transpiler({ loader: "js" });

test("released Social modules load through an install-scoped asset route without unscoped chunk requests", async () => {
  const requests: URL[] = [];
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    async fetch(request) {
      const url = new URL(request.url);
      requests.push(url);
      if (url.searchParams.get("install_id") !== "34" || url.searchParams.get("project_id") !== "widget-project")
        return new Response("app not installed: social", { status: 404 });
      const entry = url.pathname.replace("/api/apps/social", "");
      if (!entries.includes(entry)) return new Response("unknown entry", { status: 404 });
      return new Response(await readFile(new URL(`.${entry}`, appRoot)), { headers: { "Content-Type": "text/javascript" } });
    },
  });
  try {
    for (const entry of entries) {
      const url = new URL(`/api/apps/social${entry}?v=${manifest.version}&install_id=34&project_id=widget-project`, server.url);
      const response = await fetch(url);
      expect(response.status).toBe(200);
      for (const dependency of parser.scanImports(await response.text())) {
        if (hostImports.has(dependency.path)) continue;
        const child = await fetch(new URL(dependency.path, url));
        expect(child.status, `${entry} imports ${dependency.path} outside its install scope`).toBe(200);
        throw new Error(`${entry} must ship its app-owned dependencies in its entry bundle`);
      }
    }
    expect(requests).toHaveLength(entries.length);
    expect(entries).toHaveLength(5);
  } finally {
    server.stop(true);
  }
});

for (const [file, heading] of [
  ["SocialPerformanceWidget.mjs", "Social · Performance"],
  ["SocialPublishingCalendarWidget.mjs", "Social · Publishing calendar"],
]) {
  test(`released ${file} imports and renders using the host React`, async () => {
    const { default: Widget } = await import(new URL(`ui/${file}`, appRoot).href);
    const html = renderToStaticMarkup(<Widget projectId="widget-project" installId={34} preview />);
    expect(html).toContain(heading);
    expect(html).toContain("install_id=34");
  });
}
