import { describe, expect, test } from "bun:test";
import { buildAssetFamilies, lineageBadge } from "./lineage";
const source = (asset_id: string, relation = "derived") => ({ asset_id, relation });
const assets = [
  { id: "original", kind: "video", sources: [] },
  { id: "other", kind: "image", sources: [] },
  { id: "reel", kind: "video", sources: [source("original", "reel"), source("other")] },
  { id: "clip", kind: "video", sources: [source("reel", "clip")] },
  { id: "external", kind: "image", sources: [source("outside-session")] },
];
describe("recorded asset families", () => {
 test("groups multi-parent and nested derivatives once, retaining external parents", () => {
  const groups = buildAssetFamilies(assets, assets);
  expect(groups.map(g => g.asset.id)).toEqual(["original", "other", "external"]);
  expect(groups[0].children[0].asset.id).toBe("reel");
  expect(groups[0].children[0].children[0].asset.id).toBe("clip");
  expect(groups[0].derivedCount).toBe(2);
  expect(groups[0].children[0].asset.sources).toHaveLength(2);
 });
 test("a nested derivative-only match keeps its source context and hides non-matches", () => {
  const groups = buildAssetFamilies(assets, [assets[3]]);
  expect(groups).toHaveLength(1);
  expect(groups[0].matches).toBe(false);
  expect(groups[0].children[0].matches).toBe(false);
  expect(groups[0].children[0].children[0].matches).toBe(true);
  expect(groups[0].matchingCount).toBe(1);
 });
 test("root-only filters hide derivatives and sort follows matching results", () => {
  const groups = buildAssetFamilies(assets, [assets[1], assets[0]]);
  expect(groups.map(g => g.asset.id)).toEqual(["other", "original"]);
  expect(groups[1].children).toHaveLength(0);
  expect(groups[1].derivedCount).toBe(2);
 });
 test("malformed cyclic links cannot lose assets or recurse forever", () => {
  const cyclic = [{ id:"a", kind:"video",sources:[source("b")] }, {id:"b",kind:"video",sources:[source("a")]}];
  const groups=buildAssetFamilies(cyclic,cyclic);
  expect(groups).toHaveLength(1);
  expect(groups[0].matchingCount).toBe(2);
 });
 test("badges use recorded metadata and never infer an original from absent links", () => {
  expect(lineageBadge(assets[0])).toBe("No source linked");
  expect(lineageBadge(assets[2])).toBe("Reel");
  expect(lineageBadge(assets[3])).toBe("Clip");
  expect(lineageBadge(assets[4])).toBe("Derived");
  expect(lineageBadge({id:"explicit",kind:"original"})).toBe("Original");
 });
});
