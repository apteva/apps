import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
const app = resolve(import.meta.dir, "..");
const root = resolve(app, "../..");
const kit = process.env.APTEVA_UI_KIT_DIR || resolve(root, "../ui-kit");
if (!await Bun.file(join(kit,"src/index.ts")).exists()) throw new Error("Set APTEVA_UI_KIT_DIR to the host ui-kit checkout");
const temp = await mkdtemp(join(tmpdir(),"computer-ui-types-"));
try {
 const config=join(temp,"tsconfig.json");
 await Bun.write(config,JSON.stringify({compilerOptions:{target:"ES2022",module:"ESNext",moduleResolution:"bundler",jsx:"react-jsx",strict:true,noEmit:true,skipLibCheck:true,paths:{"@apteva/ui-kit":[join(kit,"src/index.ts")],"react":[join(root,"node_modules/@types/react/index.d.ts")],"react/*":[join(root,"node_modules/@types/react/*")],"lucide-react":[join(root,"node_modules/lucide-react/dist/lucide-react.d.ts")]}},include:[join(app,"ui/*.tsx"),join(app,"ui/refresh.ts")],exclude:[join(app,"ui/*.test.tsx")]}));
 const proc=Bun.spawn([join(root,"node_modules/.bin/tsc"),"-p",config],{stdout:"inherit",stderr:"inherit"});
 process.exitCode=await proc.exited;
}finally{await rm(temp,{recursive:true,force:true});}
