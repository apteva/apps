import * as React from "react";
import {join,dirname} from "node:path";
import {existsSync} from "node:fs";
import {mkdir} from "node:fs/promises";

/** A real dashboard-like import map: app cards and host share one React runtime. */
export async function buildAppCardAssets(outdir:string) {
  await mkdir(outdir,{recursive:true});
  let workspace=import.meta.dir;
  while(!existsSync(join(workspace,"ui-kit/src/index.ts"))) {
    const parent=dirname(workspace);if(parent===workspace)throw Error("Workspace UI-kit source not found");workspace=parent;
  }
  const vendor=join(outdir,"react-entry.ts");
  await Bun.write(vendor,`import React from ${JSON.stringify(Bun.resolveSync("react",import.meta.dir))};export default React;${Object.keys(React).filter(key=>key!=="default"&&/^[A-Za-z_$][\w$]*$/.test(key)).map(key=>`export const ${key}=React.${key};`).join("")}`);
  const jsx=join(outdir,"jsx-entry.ts");
  await Bun.write(jsx,'import {createElement,Fragment} from "react";export {Fragment};export function jsx(type,props,key){return createElement(type,key===undefined?props:{...props,key});}export const jsxs=jsx;export const jsxDEV=jsx;');
  const entries=[
    {entry:vendor,name:"react.js",external:[]},
    {entry:jsx,name:"jsx.js",external:["react"]},
    {entry:join(workspace,"ui-kit/src/index.ts"),name:"ui-kit.js",external:["react","react/jsx-runtime"]},
    {entry:join(import.meta.dir,"app-card-host.tsx"),name:"host.js",external:["react","react/jsx-runtime","@apteva/ui-kit"]},
  ];
  const files=new Map<string,Blob>();
  for(const {entry,name,external} of entries){
    const result=await Bun.build({entrypoints:[entry],target:"browser",format:"esm",external,define:{"process.env.NODE_ENV":'"production"'}});
    if(!result.success)throw new AggregateError(result.logs,"App card test host build failed");
    files.set(`/__card-assets/${name}`,result.outputs[0]);
  }
  return files;
}
export const appCardHTML=`<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><script type="importmap">{"imports":{"react":"/__card-assets/react.js","react/jsx-runtime":"/__card-assets/jsx.js","react/jsx-dev-runtime":"/__card-assets/jsx.js","@apteva/ui-kit":"/__card-assets/ui-kit.js"}}</script><style>body{margin:0;background:#0a0a0a;color:#eee;font-family:Arial}img,iframe{max-width:100%}</style></head><body><div id="root"></div><script type="module" src="/__card-assets/host.js"></script></body></html>`;
