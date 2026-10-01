// Match scripts/build-panels.ts: ESM, shared host React, external source map.
const define = {'process.env.NODE_ENV': '"production"'};
const shared = ['react', 'react/jsx-runtime', 'react/jsx-dev-runtime', '@apteva/ui-kit'];
const builds = [
 {entrypoints:['./SportsbookPanel.tsx'],outdir:'.',external:shared,naming:'[name].mjs',sourcemap:'external' as const},
 {entrypoints:['./preview-react.ts'],outdir:'./vendor',external:[],naming:'react.mjs'},
 {entrypoints:['./preview-react-dom.ts'],outdir:'./vendor',external:['react'],naming:'react-dom.mjs'},
 {entrypoints:['./preview-jsx-runtime.ts'],outdir:'./vendor',external:['react'],naming:'jsx-runtime.mjs'},
 {entrypoints:['./preview.tsx'],outdir:'.',external:[...shared,'react-dom/client','./SportsbookPanel.mjs'],naming:'app.mjs'},
];
for(const options of builds) {
 const result=await Bun.build({...options,target:'browser',format:'esm',minify:true,define});
 if(!result.success){for(const log of result.logs)console.error(log);process.exit(1);}
 for(const output of result.outputs)console.log(output.path,output.size);
}
// Only the local harness loads this CSS. Dashboard panels inherit host styles.
const css=Bun.spawn(['bun','./node_modules/@tailwindcss/cli/dist/index.mjs','-i','preview.css','-o','preview.min.css','--minify'],{stdout:'inherit',stderr:'inherit'});
if(await css.exited!==0)process.exit(1);
export {};
