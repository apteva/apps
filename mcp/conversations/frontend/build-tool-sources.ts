import { readdir } from "node:fs/promises";
import { join } from "node:path";
import { parse } from "yaml";

// Public app identity metadata gives exported hosts the same source icons
// without requiring dashboard administration APIs or cross-origin icon fetches.
export async function buildToolSources() {
  const root=join(import.meta.dir,"../..");
  const sources=[];
  for(const name of (await readdir(root)).sort()) {
    const file=Bun.file(join(root,name,"apteva.yaml"));if(!await file.exists())continue;
    const manifest=parse(await file.text());
    if(!manifest.name)continue;
    let icon:string|undefined;
    if(typeof manifest.icon==="string" && /^\/ui\/[^.][^?]*\.svg$/.test(manifest.icon) && !manifest.icon.includes("..")) {
      const asset=Bun.file(join(root,name,manifest.icon.slice(1)));
      if(await asset.exists() && asset.size<32768)icon=`data:image/svg+xml,${encodeURIComponent(await asset.text())}`;
    }
    sources.push({name:manifest.name,display_name:manifest.display_name,icon,icon_style:manifest.icon_style === "monochrome" ? "monochrome" : "image"});
  }
  await Bun.write(join(import.meta.dir,"src/toolSources.ts"),`// Generated from public app manifests by build-tool-sources.ts.\nexport const toolSources = ${JSON.stringify(sources,null,2)} as const;\n`);
}
