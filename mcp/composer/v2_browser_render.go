package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func renderV2Browser(ctx context.Context, app *sdk.AppCtx, spec *V2Composition, projectID string) (Result, []string, error) {
	start := time.Now()
	if err := validateV2Composition(spec); err != nil {
		return Result{}, nil, err
	}
	out := v2OutputToOutput(spec.Output)
	if out.Format != "mp4" {
		return Result{}, nil, fmt.Errorf("browser composer/v2 renderer supports mp4 output, got %q", out.Format)
	}
	chromePath, err := chromeExecutable()
	if err != nil {
		return Result{}, nil, err
	}
	nodePath, err := exec.LookPath(browserNodePath())
	if err != nil {
		return Result{}, nil, fmt.Errorf("node is required for browser composer/v2 rendering: %w", err)
	}
	w, h := spec.Output.Width, spec.Output.Height
	if w <= 0 || h <= 0 {
		w, h = resolutionWH(out.Resolution, out.Aspect)
	}
	fps := spec.Output.FPS
	if fps <= 0 {
		fps = out.FPS
	}
	if fps <= 0 {
		fps = 24
	}
	duration := v2DurationSeconds(spec)
	if duration <= 0 {
		return Result{}, nil, fmt.Errorf("composer/v2 duration must be > 0")
	}
	scratch, err := os.MkdirTemp("", "composer-v2-browser-")
	if err != nil {
		return Result{}, nil, fmt.Errorf("scratch dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(scratch) }
	framesDir := filepath.Join(scratch, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		cleanup()
		return Result{}, nil, err
	}
	renderSpec, err := browserResolvedSpec(app, spec)
	if err != nil {
		cleanup()
		return Result{}, nil, err
	}
	htmlPath := filepath.Join(scratch, "scene.html")
	if err := os.WriteFile(htmlPath, []byte(browserHTML(renderSpec)), 0o644); err != nil {
		cleanup()
		return Result{}, nil, err
	}
	scriptPath := filepath.Join(scratch, "render.js")
	if err := os.WriteFile(scriptPath, []byte(browserCaptureScript()), 0o755); err != nil {
		cleanup()
		return Result{}, nil, err
	}
	port, err := freeTCPPort()
	if err != nil {
		cleanup()
		return Result{}, nil, err
	}
	frameCount := int(duration * float64(fps))
	if float64(frameCount)/float64(fps) < duration {
		frameCount++
	}
	if frameCount < 1 {
		frameCount = 1
	}
	args := []string{
		scriptPath,
		chromePath,
		fileURL(htmlPath),
		framesDir,
		strconv.Itoa(w),
		strconv.Itoa(h),
		strconv.Itoa(fps),
		strconv.Itoa(frameCount),
		strconv.Itoa(port),
		filepath.Join(scratch, "chrome-profile"),
	}
	cmd := exec.CommandContext(ctx, nodePath, args...)
	var stderr strings.Builder
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if app != nil {
			app.Logger().Warn("kept browser v2 scratch dir for post-mortem", "path", scratch, "err", err)
		}
		return Result{}, nil, fmt.Errorf("browser capture failed: %w\nstdout:\n%s\nstderr:\n%s", err, redactSecrets(truncTail(stdout.String(), 2048)), redactSecrets(truncTail(stderr.String(), 2048)))
	}
	reportRenderProgress(ctx, RenderProgress{Fraction: 0.8, OutTimeSeconds: duration, Frame: int64(frameCount)})
	outFile := filepath.Join(scratch, "out."+out.Format)
	ffmpegArgs, err := buildV2NativeFFmpegArgs(app, spec, out, projectID, filepath.Join(framesDir, "frame_%06d.jpg"), outFile, duration, fps)
	if err != nil {
		cleanup()
		return Result{}, nil, err
	}
	ff := exec.CommandContext(ctx, ffmpegPath(), ffmpegArgs...)
	var ffstderr strings.Builder
	ff.Stderr = &ffstderr
	if err := ff.Run(); err != nil {
		if app != nil {
			app.Logger().Warn("kept browser v2 scratch dir for ffmpeg post-mortem", "path", scratch, "err", err)
		}
		return Result{FFmpegCommand: redactSecrets(shellEcho(ffmpegPath(), ffmpegArgs))}, nil, fmt.Errorf("ffmpeg failed: %w\nstderr (last 1KB):\n%s", err, redactSecrets(truncTail(ffstderr.String(), 1024)))
	}
	reportRenderProgress(ctx, RenderProgress{Fraction: 1, OutTimeSeconds: duration, Frame: int64(frameCount)})
	return Result{
		Sync:          true,
		LocalPath:     outFile,
		Cleanup:       cleanup,
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: redactSecrets(shellEcho(ffmpegPath(), ffmpegArgs)),
	}, []string{"browser composer/v2 renderer used for CSS scene graph and component motion"}, nil
}

func browserResolvedSpec(app *sdk.AppCtx, spec *V2Composition) (*V2Composition, error) {
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	var out V2Composition
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for i := range out.Assets {
		if out.Assets[i].Type == "image" || out.Assets[i].Type == "video" {
			if resolved, err := resolveAssetLocal(app, out.Assets[i].Src); err == nil && resolved != "" {
				out.Assets[i].Src = fileURL(resolved)
			}
		}
	}
	return &out, nil
}

func chromeExecutable() (string, error) {
	candidates := []string{
		browserChromePath(),
		"google-chrome-stable",
		"google-chrome",
		"chromium",
		"chromium-browser",
	}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}, candidates...)
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.Contains(c, "/") {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("Chrome/Chromium not found; set CHROME_BIN for browser composer/v2 rendering")
}

func freeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func fileURL(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	return "file://" + strings.ReplaceAll(abs, " ", "%20")
}

func browserHTML(spec *V2Composition) string {
	b, _ := json.Marshal(spec)
	title := html.EscapeString(firstNonEmpty(spec.Name, "Composer V2 Browser Render"))
	return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>` + title + `</title>
<style>
html,body,#stage{margin:0;width:100%;height:100%;overflow:hidden;background:#000}
body{font-family:Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif}
#stage{position:relative;isolation:isolate}
.el{position:absolute;box-sizing:border-box;transform-origin:center center;will-change:transform,opacity;overflow:visible}
.text{white-space:pre-wrap;line-height:1.08;text-rendering:geometricPrecision}
.shape{overflow:hidden}
.component{overflow:visible}
.browser-window{width:100%;height:100%;background:#fbfaf7;border:1px solid rgba(35,45,58,.16);border-radius:18px;box-shadow:0 30px 90px rgba(25,49,75,.24);overflow:hidden}
.browser-top{height:44px;background:#fff;border-bottom:1px solid rgba(35,45,58,.10);display:flex;align-items:center;gap:8px;padding:0 16px;color:#88919c;font:600 13px ui-sans-serif}
.dot{width:10px;height:10px;border-radius:50%;background:#ef6b57}.dot:nth-child(2){background:#f6bf4f}.dot:nth-child(3){background:#67c56d}
.browser-content{padding:26px 34px;color:#1f2328;font-size:18px;line-height:1.45}
.task-list{display:grid;gap:15px}.task{display:flex;align-items:center;gap:12px;padding:13px 15px;border-radius:13px;background:#fff;box-shadow:0 8px 28px rgba(55,74,91,.12);border:1px solid rgba(35,45,58,.08)}
.task .ico{width:18px;height:18px;border-radius:5px;background:#d9674a}.task .small{color:#8b949e;font-size:12px}
.phone{width:100%;height:100%;border-radius:42px;background:#111;box-shadow:0 35px 90px rgba(20,30,42,.28);padding:16px;box-sizing:border-box}.phone-screen{width:100%;height:100%;border-radius:30px;background:#fff;overflow:hidden;color:#20252b}
.phone-bar{height:38px;background:#0d0d0f;color:white;display:flex;align-items:center;justify-content:center;font-size:14px}.phone-body{padding:22px;font-size:19px;line-height:1.5}
.pill{display:inline-flex;align-items:center;gap:10px;padding:13px 19px;border-radius:15px;background:#fff;box-shadow:0 18px 55px rgba(100,54,42,.18);font:700 22px ui-sans-serif;color:#444}.pill.hot{background:#d9684f;color:white}
.spark{width:18px;height:18px;border-radius:50%;background:radial-gradient(circle,#f2aa8e 0,#cf5d45 45%,transparent 48%)}
.halftone{width:100%;height:100%;background:
radial-gradient(circle at 50% 45%, rgba(213,99,78,.52), rgba(213,99,78,.16) 32%, transparent 57%),
radial-gradient(circle, rgba(255,255,255,.22) 1.4px, transparent 1.8px);background-size:100% 100%, 10px 10px;filter:blur(.2px)}
.loop-pattern{width:100%;height:100%;background:
radial-gradient(ellipse at 22% 30%, transparent 0 18%, rgba(255,255,255,.20) 18.3% 18.9%, transparent 19.2% 100%),
radial-gradient(ellipse at 78% 64%, transparent 0 21%, rgba(255,255,255,.16) 21.3% 21.9%, transparent 22.2% 100%),
radial-gradient(ellipse at 50% 50%, transparent 0 38%, rgba(255,255,255,.12) 38.3% 38.9%, transparent 39.2% 100%);
background-size:360px 240px,420px 260px,520px 320px;opacity:.65}
.laptop{width:100%;height:100%;position:relative}.laptop-screen{position:absolute;left:8%;right:8%;top:4%;height:70%;border-radius:15px;background:#3d77a7;padding:18px;box-shadow:0 30px 80px rgba(60,103,139,.28)}.laptop-inner{width:100%;height:100%;background:#fff;border-radius:8px;overflow:hidden}.laptop-base{position:absolute;left:0;right:0;bottom:4%;height:11%;border-radius:0 0 26px 26px;background:linear-gradient(90deg,#b7c8d9,#eef4f8,#b9cad9)}
.cursor{width:34px;height:34px;filter:drop-shadow(0 6px 12px rgba(0,0,0,.28))}
.cursor:before{content:"";position:absolute;width:0;height:0;border-left:0 solid transparent;border-right:20px solid transparent;border-top:30px solid #111;transform:rotate(-24deg)}
.brand-mark{font-family:Georgia,serif;color:#2c211e;font-size:58px;letter-spacing:-.04em}.brand-star{display:inline-block;width:32px;height:32px;margin-right:12px;background:radial-gradient(circle,#d66a50 0 18%,transparent 20%),conic-gradient(from 0deg,#d66a50,#f0aa8e,#d66a50);clip-path:polygon(50% 0,58% 38%,100% 50%,58% 62%,50% 100%,42% 62%,0 50%,42% 38%)}
.html-component{width:100%;height:100%}
</style>
</head>
<body>
<div id="stage"></div>
<script>
const SPEC = ` + string(b) + `;
` + browserRuntimeJS() + `
</script>
</body>
</html>`
}

func browserRuntimeJS() string {
	return `
const stage = document.getElementById('stage');
const W = SPEC.output?.width || 1920, H = SPEC.output?.height || 1080;
const DW = SPEC.output?.design_width || W, DH = SPEC.output?.design_height || H;
const sx = W / DW, sy = H / DH, ss = Math.min(sx, sy);
stage.style.width = W + 'px'; stage.style.height = H + 'px';
function sceneStarts(){let c=0; return (SPEC.scenes||[]).map(s=>{let st=s.start??c; c=st+(s.duration||0); return st;});}
const starts = sceneStarts();
const assets = Object.fromEntries((SPEC.assets||[]).map(a=>[a.id,a]));
function meas(v, base, scale){ if(v==null) return 0; if(typeof v==='number') return v*scale; let s=String(v); if(s.endsWith('%')) return parseFloat(s)*base/100; return parseFloat(s)*scale || 0; }
function ease(t){t=Math.max(0,Math.min(1,t)); return 1-Math.pow(1-t,3);}
function lerp(a,b,p){return a+(b-a)*p}
function styleNum(o,k,d){let v=o?.[k]; return typeof v==='number'?v:d}
function styleStr(o,k,d){let v=o?.[k]; return typeof v==='string'?v:d}
function key(anim,k,t,d){let arr=anim?.[k]; if(!Array.isArray(arr)) return d; for(const it of arr){let st=it.start||0, du=it.duration||it.length||0; if(du>0 && t>=st && t<=st+du){return lerp(it.from??d,it.to??d,ease((t-st)/du));}} return d}
function motion(el,t,dur){let op=styleNum(el.style,'opacity',1), x=0, y=0, sc=1; const apply=(m,enter)=>{ if(!m)return; let type=(m.type||m.preset||'').toLowerCase(); let du=m.duration||.5, delay=m.delay||0; let p=enter?ease((t-delay)/du):ease((dur-t-delay)/du); p=Math.max(0,Math.min(1,p)); if(type==='fade'){op*=p} if(type==='rise'||type==='fade_up'){op*=p;y+=(1-p)*28*sy;sc*=.98+.02*p} if(type==='drop'||type==='fade_down'){op*=p;y-=(1-p)*28*sy;sc*=.98+.02*p} if(type==='slide_left'){op*=p;x+=(1-p)*140*sx} if(type==='slide_right'){op*=p;x-=(1-p)*140*sx} if(type==='slide_up'){op*=p;y+=(1-p)*110*sy} if(type==='slide_down'){op*=p;y-=(1-p)*110*sy} if(type==='zoom_in'){op*=p;sc*=.94+.06*p} if(type==='zoom_out'){op*=p;sc*=1.06-.06*p} if(type==='pop'||type==='scale_pop'){op*=p;sc*=.82+.18*p} };
 apply(el.enter,true); apply(el.exit,false); x+=key(el.animate,'x',t,0)*sx; y+=key(el.animate,'y',t,0)*sy; op=key(el.animate,'opacity',t,op); sc*=key(el.animate,'scale',t,1); return {op,x,y,sc};}
function box(el){return {x:meas(el.x??el.style?.x,W,sx),y:meas(el.y??el.style?.y,H,sy),w:meas(el.width??el.style?.width,W,sx)||W,h:meas(el.height??el.style?.height,H,sy)||H};}
function applyBox(node,b,m){node.style.left=b.x+'px';node.style.top=b.y+'px';node.style.width=b.w+'px';node.style.height=b.h+'px';node.style.opacity=m.op;node.style.transform='translate('+m.x+'px,'+m.y+'px) scale('+m.sc+')';}
function color(v,d){return v||d}
function gradientCSS(g){if(!g||typeof g!=='object')return ''; let stops=Array.isArray(g.stops)?g.stops.map(s=>String(s.color||'transparent')+' '+Math.max(0,Math.min(100,Number(s.offset||0)*100))+'%').join(', '):String(g.from||'#fff')+', '+String(g.to||'#000'); return 'linear-gradient('+Number(g.angle??90)+'deg, '+stops+')'}
function shadowCSS(s){if(typeof s==='string')return s; if(!s||typeof s!=='object')return ''; let c=s.color||'rgba(0,0,0,'+Number(s.opacity??.35)+')'; return (Number(s.offset_x||0)*ss)+'px '+(Number(s.offset_y??12)*ss)+'px '+(Number(s.blur??20)*ss)+'px '+c}
function componentHTML(el){let c=el.component||el.style?.component||el.meta?.component||''; let title=el.text||el.style?.title||''; if(c==='html')return '<div class="html-component">'+(el.meta?.html||el.meta?.body||'')+'</div>'; if(c==='browser_window')return '<div class="browser-window"><div class="browser-top"><span class="dot"></span><span class="dot"></span><span class="dot"></span><span style="flex:1"></span><span>'+title+'</span></div><div class="browser-content">'+(el.meta?.body||'')+'</div></div>'; if(c==='task_list')return '<div class="task-list">'+(el.meta?.items||[]).map(x=>'<div class="task"><span class="ico"></span><div><b>'+x+'</b><div class="small">working in background</div></div></div>').join('')+'</div>'; if(c==='phone')return '<div class="phone"><div class="phone-screen"><div class="phone-bar">'+(el.meta?.bar||'8:14')+'</div><div class="phone-body">'+(el.meta?.body||title)+'</div></div></div>'; if(c==='pill')return '<div class="pill '+(el.meta?.hot?'hot':'')+'"><span class="spark"></span>'+title+'</div>'; if(c==='halftone')return '<div class="halftone"></div>'; if(c==='loop_pattern')return '<div class="loop-pattern"></div>'; if(c==='laptop')return '<div class="laptop"><div class="laptop-screen"><div class="laptop-inner">'+(el.meta?.body||'')+'</div></div><div class="laptop-base"></div></div>'; if(c==='cursor')return '<div class="cursor"></div>'; if(c==='brand')return '<div class="brand-mark"><span class="brand-star"></span>'+title+'</div>'; return '<div></div>'; }
function applySharedStyle(n,el){if(!el.style)return; if(el.style.background)n.style.background=el.style.background; if(el.style.fill)n.style.background=el.style.fill; if(el.style.gradient)n.style.background=gradientCSS(el.style.gradient); if(el.style.shadow)n.style.boxShadow=shadowCSS(el.style.shadow); if(el.style.filter)n.style.filter=el.style.filter; if(el.style.blend_mode)n.style.mixBlendMode=el.style.blend_mode; if(el.style.opacity!=null)n.style.opacity=el.style.opacity; if(el.style.padding!=null)n.style.padding=(styleNum(el.style,'padding',0)*ss)+'px'; if(el.style.radius!=null)n.style.borderRadius=(styleNum(el.style,'radius',0)*ss)+'px'; if(el.style.stroke)n.style.border=(styleNum(el.style,'stroke_width',1)*ss)+'px solid '+el.style.stroke; n.style.boxSizing='border-box';}
function makeEl(el){let n=document.createElement('div'); n.className='el '+el.type; n.dataset.id=el.id||''; applySharedStyle(n,el); if(el.type==='shape'||el.type==='group'){n.classList.add('shape'); n.style.background=el.style?.gradient?gradientCSS(el.style.gradient):color(el.style?.fill||el.style?.background,'transparent'); n.style.borderRadius=(el.style?.kind==='ellipse'||el.style?.kind==='circle')?'50%':(styleNum(el.style,'radius',0)*ss)+'px'; if(el.style?.stroke)n.style.border=(styleNum(el.style,'stroke_width',1)*ss)+'px solid '+el.style.stroke; if(el.style?.shadow)n.style.boxShadow=shadowCSS(el.style.shadow); if(el.style?.filter)n.style.filter=el.style.filter;} else if(el.type==='text'){n.classList.add('text'); n.textContent=el.text||''; n.style.color=color(el.style?.color,'#111'); n.style.fontSize=(styleNum(el.style,'font_size',styleNum(el.style,'fontSize',48))*ss)+'px'; n.style.fontWeight=styleNum(el.style,'weight',400); n.style.textAlign=styleStr(el.style,'align','left'); n.style.fontFamily=styleStr(el.style,'font_family',styleStr(el.style,'fontFamily','inherit')); n.style.display='flex'; n.style.alignItems=styleStr(el.style,'vertical_align','center'); n.style.justifyContent=styleStr(el.style,'align','left')==='center'?'center':'flex-start'; n.style.overflowWrap='anywhere';} else if(el.type==='image'){let img=document.createElement('img'); img.src=assets[el.asset]?.src||el.src||''; img.style.width='100%'; img.style.height='100%'; img.style.objectFit=el.fit||el.style?.fit||'cover'; n.appendChild(img);} else if(el.type==='component'){n.classList.add('component'); n.innerHTML=componentHTML(el);} return n;}
const sceneNodes = (SPEC.scenes||[]).map((scene,si)=>{let root=document.createElement('div'); root.style.position='absolute'; root.style.inset='0'; root.style.overflow='hidden'; root.style.background=scene.background||'transparent'; stage.appendChild(root); let nodes=(scene.elements||[]).map(el=>{let node=makeEl(el); root.appendChild(node); return {el,node};}); return {scene,root,nodes,start:starts[si]};});
window.__setComposerTime = async function(t){ for(const s of sceneNodes){let local=t-s.start; let active=local>=0 && local<=s.scene.duration; s.root.style.display=active?'block':'none'; if(!active) continue; const cam=s.scene.meta?.camera||{}; const cm={op:1,x:key(cam,'x',local,0)*sx,y:key(cam,'y',local,0)*sy,sc:key(cam,'scale',local,1)}; s.root.style.transformOrigin='50% 50%'; s.root.style.transform='translate('+cm.x+'px,'+cm.y+'px) scale('+cm.sc+')'; const byId=Object.fromEntries(s.nodes.map(x=>[x.el.id,x])); for(const item of s.nodes){let el=item.el; let st=el.start||0, dur=el.duration||Math.max(.001,s.scene.duration-st); let on=local>=st && local<=st+dur; item.node.style.display=on?'block':'none'; if(!on)continue; let b=box(el), m=motion(el,local-st,dur); if(el.parent && byId[el.parent]){let p=byId[el.parent].el; let pm=motion(p,local-(p.start||0),p.duration||s.scene.duration); m.op*=pm.op; m.x+=pm.x; m.y+=pm.y; m.sc*=pm.sc;} applyBox(item.node,b,m);} } await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r))); };
window.__setComposerTime(0);
`
}

func browserCaptureScript() string {
	return `
const fs = require('fs');
const {spawn} = require('child_process');
const [chromePath, url, framesDir, wRaw, hRaw, fpsRaw, countRaw, portRaw, profile] = process.argv.slice(2);
const width=Number(wRaw), height=Number(hRaw), fps=Number(fpsRaw), frames=Number(countRaw), port=Number(portRaw);
fs.mkdirSync(framesDir,{recursive:true});
const chrome = spawn(chromePath, ['--headless=new','--disable-gpu','--hide-scrollbars','--mute-audio','--allow-file-access-from-files','--remote-debugging-port='+port,'--user-data-dir='+profile,'about:blank'], {stdio:['ignore','pipe','pipe']});
chrome.stderr.on('data', d => process.stderr.write(d));
chrome.stdout.on('data', d => process.stderr.write(d));
async function sleep(ms){return new Promise(r=>setTimeout(r,ms))}
async function getJSON(path){const res=await fetch('http://127.0.0.1:'+port+path); if(!res.ok) throw new Error('HTTP '+res.status+' '+path); return res.json();}
async function waitChrome(){for(let i=0;i<80;i++){try{return await getJSON('/json/list')}catch(e){await sleep(100)}} throw new Error('Chrome did not start');}
function cdp(wsUrl){return new Promise((resolve,reject)=>{const ws=new WebSocket(wsUrl); let id=0; const pending=new Map(); ws.onopen=()=>resolve({send(method,params={}){return new Promise((res,rej)=>{const mid=++id; pending.set(mid,{res,rej}); ws.send(JSON.stringify({id:mid,method,params}));});}, close(){ws.close();}}); ws.onerror=reject; ws.onmessage=e=>{const msg=JSON.parse(e.data); if(msg.id&&pending.has(msg.id)){const p=pending.get(msg.id); pending.delete(msg.id); msg.error?p.rej(new Error(JSON.stringify(msg.error))):p.res(msg.result)}};});}
(async()=>{try{let pages=await waitChrome(); let page=pages.find(p=>p.type==='page')||pages[0]; let dev=await cdp(page.webSocketDebuggerUrl); await dev.send('Page.enable'); await dev.send('Runtime.enable'); await dev.send('Emulation.setDeviceMetricsOverride',{width,height,deviceScaleFactor:1,mobile:false}); await dev.send('Page.navigate',{url}); await sleep(700); for(let i=0;i<frames;i++){let t=i/fps; await dev.send('Runtime.evaluate',{expression:'window.__setComposerTime('+t+')',awaitPromise:true}); const cap=await dev.send('Page.captureScreenshot',{format:'jpeg',quality:92,clip:{x:0,y:0,width,height,scale:1},captureBeyondViewport:false}); fs.writeFileSync(framesDir+'/frame_'+String(i+1).padStart(6,'0')+'.jpg', Buffer.from(cap.data,'base64')); if(i%120===0) process.stderr.write('frame '+i+'/'+frames+'\\n');} dev.close(); chrome.kill('SIGTERM');}catch(e){chrome.kill('SIGTERM'); console.error(e.stack||e); process.exit(1);}})();
`
}
