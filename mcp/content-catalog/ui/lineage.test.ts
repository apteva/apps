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

describe("content purposes and hidden provenance", () => {
 test("eight main masters retain thirteen outputs through hidden frames and archived originals", () => {
  const all: any[]=[];
  for(let i=0;i<8;i++) {
   const old={id:`old-${i}`,kind:"video",role:"main",lifecycle:"archived",sources:[]};
   const main={id:`main-${i}`,kind:"video",role:"main",sources:[source(old.id,"cleaned_master")],ancestors:[old]};all.push(old,main);
   const hidden={id:`helper-${i}`,kind:"image",role:"intermediate",lifecycle:i%2?"archived":"active",sources:[source(main.id,"extracted_frame")]};
   all.push(hidden);
   for(const [category,n] of [["reel",3],["screenshot",5],["portrait",5]] as const) for(let j=0;j<n;j++) {
    all.push({id:`${category}-${i}-${j}`,kind:category==="reel"?"video":"image",role:"derivative",output_type:category,sources:[source(category==="portrait"&&j===0?hidden.id:main.id)],ancestors:[hidden,main,old]});
   }
  }
  const groups=buildAssetFamilies(all,all);
  expect(groups).toHaveLength(8);
  for(const group of groups){expect(group.asset.role).toBe("main");expect(group.derivedCount).toBe(13);expect(group.children).toHaveLength(13);expect(group.outputCounts).toEqual({reel:3,screenshot:5,portrait:5});}
  const orphan=all.find(a=>a.id==="portrait-0-0");expect(orphan.sources[0].asset_id).toBe("helper-0");
 });
 test("hidden ancestors are context only, even when not among the content results",()=>{
  const main={id:"main",kind:"video",role:"main",sources:[source("old","cleaned_master")]};
  const hidden={id:"frame",kind:"image",role:"intermediate",lifecycle:"archived",sources:[source("main")]};
  const crop={id:"crop",kind:"image",role:"derivative",sources:[source("frame")],ancestors:[hidden,main]};
  const groups=buildAssetFamilies([main,crop],[crop]);expect(groups).toHaveLength(1);expect(groups[0].asset.id).toBe("main");expect(groups[0].children[0].asset.id).toBe("crop");expect(groups[0].derivedCount).toBe(1);
 });
 test("main purpose never depends on having no parent",()=>{expect(lineageBadge({id:"master",kind:"video",role:"main",sources:[source("old","cleaned_master")]})).toBe("Main");});
 test("explicit archive and intermediate inspection remain available without inflating outputs",()=>{
  const main={id:"main",kind:"video",role:"main"};const frame={id:"frame",kind:"image",role:"intermediate",sources:[source("main")]};const old={id:"old",kind:"video",lifecycle:"archived"};
  expect(buildAssetFamilies([main,frame,old],[main,frame,old])).toHaveLength(1);
  const inspected=buildAssetFamilies([main,frame,old],[main,frame,old],{includeIntermediates:true});expect(inspected[0].children).toHaveLength(1);expect(inspected[0].derivedCount).toBe(0);
  expect(buildAssetFamilies([main,frame,old],[main,frame,old],{lifecycle:"archived"})[0].asset.id).toBe("old");
 });
});

test("main-only view keeps selected outputs accessible",()=>{
 const main={id:"main",kind:"video",role:"main"};const crop={id:"crop",kind:"image",role:"derivative",sources:[source(main.id)]};
 const groups=buildAssetFamilies([main,crop],[main],{mainOnly:true});expect(groups[0].matchingCount).toBe(2);expect(groups[0].children[0].asset.id).toBe("crop");
});
