// A real browser fixture for the Computer renderer; the UI kit is supplied by
// the host in production, so the fixture implements its container contract.
const bundle = await Bun.build({
  entrypoints: [new URL("./browser-view-harness.tsx", import.meta.url).pathname],
  target: "browser", minify: false,
  plugins: [{ name: "host-ui-kit", setup(build) {
    build.onResolve({ filter: /^@apteva\/ui-kit$/ }, () => ({ path: "ui-kit", namespace: "host" }));
    build.onLoad({ filter: /.*/, namespace: "host" }, () => ({ loader: "tsx", contents: `
      export const Card = ({children, fullWidth}) => <div style={{display:'flex',flexDirection:'column',boxSizing:'border-box',border:'1px solid #444',width:'100%',maxWidth:fullWidth?'none':672,overflow:'hidden'}}>{children}</div>;
      export const AppCardHeader = ({title,action}) => <header style={{padding:16}}>{title}{action&&<a href={action.href}>{action.label}</a>}</header>;
      export const DataList = () => <div>Browser frame</div>;
    ` }));
  } }],
});
if (!bundle.success) throw new Error(bundle.logs.join("\n"));
const script = await bundle.outputs[0].text();
Bun.serve({ port: 4319, hostname: "127.0.0.1", fetch(request) {
  if (new URL(request.url).pathname === "/bundle.js") return new Response(script, { headers: { "content-type": "application/javascript" } });
  return new Response(`<!DOCTYPE html><style>
    *{box-sizing:border-box}body{margin:0} .w-full{width:100%}.h-full{height:100%}.relative{position:relative}.absolute{position:absolute}.inset-0{inset:0}.flex{display:flex}.items-center{align-items:center}.justify-center{justify-content:center}.object-contain{object-fit:contain}.block{display:block}
  </style><div id="root"></div><script type="module" src="/bundle.js"></script>`, { headers: { "content-type": "text/html" } });
} });
console.log("Browser view fixture ready");
