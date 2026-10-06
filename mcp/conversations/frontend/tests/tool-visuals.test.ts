import { expect, test } from "bun:test";
import { buildToolVisualRegistry, resolveToolVisual } from "../src/toolVisuals";

test("installed app artwork wins over a legacy integration sharing its namespace", () => {
  const registry = buildToolVisualRegistry(
    [{ name: "torrent", display_name: "Torrent", icon: "/ui/icon.svg", icon_style: "monochrome" }],
    [{ app_slug: "torrent", app_name: "Torrent Manager", logo: "https://example.com/legacy.png" }],
  );
  for (const name of ["torrent_torrent_search", "torrent_torrent_add"]) {
    expect(resolveToolVisual(name, registry)).toMatchObject({
      key: "app:torrent", iconUrl: "/ui/icon.svg", iconStyle: "monochrome",
    });
  }
});

test("specific matching integration namespaces and exact tool ownership still win", () => {
  const registry = buildToolVisualRegistry(
    [{ name: "torrent", surfaces: { mcp_tool_names: ["torrent_cloud_owned"] } }],
    [{ app_slug: "torrent-cloud", app_name: "Cloud", logo: "https://example.com/cloud.png" }],
  );
  expect(resolveToolVisual("torrent_cloud_search", registry).key).toBe("integration:torrent_cloud");
  expect(resolveToolVisual("torrent_cloud_owned", registry).key).toBe("app:torrent");
});
