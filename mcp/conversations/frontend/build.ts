import { buildToolSources } from "./build-tool-sources";
import { buildStyles } from "./build-styles";
const root = import.meta.dir;
await buildToolSources();
const build=await Bun.build({entrypoints:[`${root}/src/index.ts`,`${root}/src/react.tsx`],outdir:`${root}/dist`,target:"browser",format:"esm",splitting:true,define:{"process.env.NODE_ENV":'"production"'},sourcemap:"external",external:["react","react/jsx-runtime","@apteva/web-sdk"]});
if(!build.success)throw new AggregateError(build.logs,"Conversations package build failed");
for(const command of [["bunx","--no-install","tsc","-p",`${root}/tsconfig.json`]]) {
 const process=Bun.spawn(command,{cwd:root,stdout:"inherit",stderr:"inherit"});if(await process.exited)throw new Error(`Failed: ${command.join(" ")}`);
}
await Bun.write(`${root}/dist/styles.css`, await buildStyles());
await Bun.write(`${root}/dist/styles.d.ts`,"export {};\n");
console.log("Built headless client, shared React exports, declarations and styles.");
