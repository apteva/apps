import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { forward, spikeStep, type Network } from "./network";
type Point = { x: number; y: number; label: number };
type Metric = { epoch: number; loss: number; validation_loss: number; accuracy: number; validation_accuracy: number };
type Experiment = { id: number; name: string; status: string; state: { config: { dataset: string; hidden: number[]; learning_rate: number; epochs: number; seed: number }; network: Network; epoch: number; history: Metric[]; error?: string } };
type Snapshot = { experiment: Experiment; points: Point[]; validation_points: Point[]; metric: Metric };
type Version = { id: number; name: string; experiment_id: number; epoch: number };
type Deployment = { id: number; name: string; version_id: number; endpoint: string };
type Props = { projectId: string; installId: number; appName: string; instanceId?: number };

const css = `
.neural-app{--n-bg:var(--bg);--n-panel:var(--bg-card);--n-line:var(--border);--n-text:var(--text);--n-muted:var(--text-muted);--n-positive:var(--accent);--n-negative:color-mix(in srgb,var(--accent) 55%,var(--text) 45%);background:var(--n-bg);color:var(--n-text);font:13px/1.5 var(--font-base,system-ui);height:100%;overflow:auto;padding:16px;box-sizing:border-box}
.neural-app *{box-sizing:border-box}.neural-app button,.neural-app input,.neural-app select{font:inherit;color:inherit}.neural-app button{cursor:pointer;border:1px solid var(--n-line);background:var(--n-panel);border-radius:var(--radius-md);padding:8px 12px;white-space:nowrap}.neural-app button:hover:not(:disabled){background:var(--bg-hover);border-color:var(--border-strong)}.neural-app button:disabled{opacity:.45;cursor:default}.neural-app button.primary{background:var(--accent);color:var(--bg);border-color:var(--accent);font-weight:600}.neural-app button.primary:hover:not(:disabled){background:var(--accent-hover);border-color:var(--accent-hover)}.neural-app button[aria-pressed=true]{color:var(--accent);border-color:var(--accent);background:color-mix(in srgb,var(--accent) 10%,var(--bg-card))}.neural-app input,.neural-app select{width:100%;min-width:0;border:1px solid var(--n-line);border-radius:var(--radius-md);background:var(--bg-input);padding:7px 9px}.neural-app input[type=range]{padding:0;accent-color:var(--accent)}.neural-app input[type=checkbox]{width:auto;accent-color:var(--accent)}.neural-app label{display:block;color:var(--n-muted);font-size:12px;margin:14px 0 5px}.neural-app h1{font-size:23px;letter-spacing:-.7px;font-weight:500;margin:0}.neural-app h2{font-size:14px;font-weight:500;margin:0}.neural-app p{margin:8px 0}.neural-app .muted{color:var(--n-muted);font-size:12px}.neural-app .eyebrow{font-size:10px;letter-spacing:1.7px;color:var(--accent);text-transform:uppercase;margin-bottom:4px}.neural-app .top{display:flex;justify-content:space-between;gap:16px;align-items:center;flex-wrap:wrap;margin-bottom:22px}.neural-app .top select{width:220px}.neural-app .row{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.neural-app .space{justify-content:space-between}.neural-app .layout{display:grid;grid-template-columns:190px minmax(300px,1fr) 260px;gap:18px;align-items:start}.neural-app .box{border:1px solid var(--n-line);border-radius:var(--radius-lg);box-shadow:var(--shadow-card);background:var(--n-panel);overflow:hidden}.neural-app .pad{padding:16px}.neural-app .section{padding:13px 16px;border-bottom:1px solid var(--n-line)}.neural-app .stack{display:grid;gap:16px}.neural-app .badge{padding:3px 7px;border:1px solid var(--n-line);border-radius:var(--radius-sm);font-size:11px;text-transform:capitalize}.neural-app .running,.neural-app .completed{color:var(--accent);background:color-mix(in srgb,var(--accent) 10%,transparent);border-color:color-mix(in srgb,var(--accent) 25%,var(--border))}.neural-app .metrics{display:grid;grid-template-columns:repeat(3,1fr);border-top:1px solid var(--n-line)}.neural-app .stat{padding:13px 16px}.neural-app .stat+.stat{border-left:1px solid var(--n-line)}.neural-app .number{font-size:21px;letter-spacing:-.6px;font-variant-numeric:tabular-nums}.neural-app .graph{display:block;width:100%;overflow:visible}.neural-app .legend{display:flex;gap:15px;flex-wrap:wrap;color:var(--n-muted);font-size:11px}.neural-app .dot{display:inline-block;width:7px;height:7px;margin-right:5px;border-radius:50%;background:var(--n-positive)}.neural-app .negative{background:var(--n-negative);border-radius:0}.neural-app .failed{color:var(--error)}.neural-app .map{width:100%;aspect-ratio:1;display:block;border-radius:var(--radius-md);touch-action:none;cursor:crosshair}.neural-app .check{display:flex;align-items:center;gap:7px;margin:10px 0}.neural-app .notice{color:var(--error);border:1px solid var(--error);padding:10px 14px;border-radius:var(--radius-md);margin-bottom:16px}.neural-app .empty{padding:65px 24px;text-align:center}.neural-app .foot{margin-top:18px;font-size:11px;color:var(--n-muted);display:flex;justify-content:space-between;gap:12px;flex-wrap:wrap}.neural-app .actionbar{margin-top:16px;display:flex;gap:8px;flex-wrap:wrap}.neural-app .block{width:100%;margin-top:16px}.neural-app .endpoint{overflow-wrap:anywhere;font:11px/1.6 var(--font-mono-fixed,monospace);color:var(--n-text)}.neural-app .saved{border-top:1px solid var(--n-line);padding:11px 0}.neural-app .small{font-size:11px}.neural-app .prediction{font-size:26px;color:var(--n-positive);font-variant-numeric:tabular-nums}.neural-app .weight-node{cursor:pointer}.neural-app :focus-visible{outline:2px solid var(--accent);outline-offset:3px}
@media(max-width:1150px){.neural-app .layout{grid-template-columns:175px minmax(280px,1fr)}.neural-app .right{grid-column:1/-1;grid-template-columns:1fr 1fr}.neural-app .right .map{max-width:320px;margin:auto}}
@media(max-width:680px){.neural-app{padding:12px}.neural-app .layout{grid-template-columns:minmax(0,1fr)}.neural-app .right{grid-column:auto;grid-template-columns:minmax(0,1fr)}.neural-app .config-grid{display:grid;grid-template-columns:1fr 1fr;gap:0 14px}.neural-app .top select{width:100%}.neural-app .stat{padding:10px}.neural-app .number{font-size:18px}}
@media(prefers-reduced-motion:reduce){.neural-app *{scroll-behavior:auto}}
`;

function DecisionMap({ network, points, input, onInput }: { network: Network; points: Point[]; input: number[]; onInput: (p: number[]) => void }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const [themeRevision,setThemeRevision]=useState(0);
  useEffect(()=>{const observer=new MutationObserver(()=>setThemeRevision(v=>v+1));for(let element:Element|null=ref.current?.parentElement||null;element;element=element.parentElement)observer.observe(element,{attributes:true,attributeFilter:['data-theme','data-mode','class','style']});return()=>observer.disconnect();},[]);
  useEffect(() => {
    const canvas = ref.current; if (!canvas) return;
    const ctx = canvas.getContext('2d'); if (!ctx) return;
    // Resolve actual CSS colors, including custom themes using nested variables or color-mix.
    const probe = document.createElement('span'); probe.hidden = true; canvas.parentElement!.appendChild(probe);
    const resolve = (token: string) => { probe.style.color = `var(${token})`; return getComputedStyle(probe).color; };
    const positive=resolve('--n-positive'), negative=resolve('--n-negative'), surface=resolve('--bg-card'), text=resolve('--text');
    probe.remove();
    const size = 360, cells = 40, cell = size / cells;
    ctx.clearRect(0, 0, size, size);
    for (let i = 0; i < cells; i++) for (let j = 0; j < cells; j++) {
      const a = forward(network, (i + .5) / cells * 2 - 1, 1 - (j + .5) / cells * 2), p = a[a.length - 1][0];
      ctx.globalAlpha=1;ctx.fillStyle=surface;ctx.fillRect(i * cell,j * cell,cell+1,cell+1);
      ctx.globalAlpha=.15+Math.abs(p-.5)*.6;ctx.fillStyle=p>=.5?positive:negative;ctx.fillRect(i*cell,j*cell,cell+1,cell+1);ctx.globalAlpha=1;
    }
    points.forEach(p => { const px=(p.x+1)*size/2, py=(1-p.y)*size/2; ctx.beginPath(); if(p.label) ctx.arc(px,py,3,0,Math.PI*2); else ctx.rect(px-3,py-3,6,6); ctx.fillStyle = p.label ? positive : negative; ctx.fill(); ctx.strokeStyle = surface; ctx.lineWidth = 1; ctx.stroke(); });
    const x = (input[0] + 1) * size / 2, y = (1 - input[1]) * size / 2;
    ctx.strokeStyle = text; ctx.lineWidth = 2; ctx.beginPath(); ctx.arc(x, y, 8, 0, Math.PI * 2); ctx.moveTo(x - 13, y); ctx.lineTo(x + 13, y); ctx.moveTo(x, y - 13); ctx.lineTo(x, y + 13); ctx.stroke();
  }, [network, points, input, themeRevision]);
  return <canvas ref={ref} className="map" width={360} height={360} role="img" aria-label="Decision boundary: squares are class A, circles are class B. Click to probe; the X and Y sliders provide keyboard control." onPointerDown={e => { const r = e.currentTarget.getBoundingClientRect(); onInput([Math.max(-1, Math.min(1, (e.clientX - r.left) / r.width * 2 - 1)), Math.max(-1, Math.min(1, 1 - (e.clientY - r.top) / r.height * 2))]); }} />;
}

function LossChart({ history }: { history: Metric[] }) {
  const width = 500, height = 140, maxX = Math.max(10, history.at(-1)?.epoch || 0), maxY = Math.max(.1, ...history.flatMap(m => [m.loss, m.validation_loss])) * 1.1;
  const path = (key: 'loss' | 'validation_loss') => history.map((m, i) => `${i ? 'L' : 'M'}${40 + m.epoch / maxX * 443},${108 - m[key] / maxY * 88}`).join(' ');
  return <svg viewBox={`0 0 ${width} ${height}`} style={{ width: '100%', display: 'block' }} role="img" aria-label="Actual training and validation binary cross-entropy by epoch">
    {[0, .5, 1].map(f => <g key={f}><line x1="40" x2="483" y1={108 - f * 88} y2={108 - f * 88} stroke="var(--border)" strokeWidth=".6" /><text x="33" y={112 - f * 88} textAnchor="end" fill="var(--text-muted)" fontSize="11">{(f * maxY).toFixed(2)}</text></g>)}
    <path d={path('loss')} fill="none" stroke="var(--n-positive)" strokeWidth="2" /><path d={path('validation_loss')} fill="none" stroke="var(--n-negative)" strokeWidth="2" strokeDasharray="5 3" />
    <text x="40" y="128" fill="var(--text-muted)" fontSize="11">0</text><text x="255" y="132" textAnchor="middle" fill="var(--text-muted)" fontSize="11">Epoch</text><text x="483" y="128" textAnchor="end" fill="var(--text-muted)" fontSize="11">{maxX}</text><text x="40" y="12" fill="var(--text-muted)" fontSize="11">Cross-entropy</text>
  </svg>;
}

function Neurons({ network, input, spikes, motion }: { network: Network; input: number[]; spikes: boolean; motion: boolean }) {
  const ref = useRef<HTMLDivElement>(null), membrane = useRef<number[][]>([]), trace = useRef<number[]>([]);
  const [width, setWidth] = useState(500), [selected, setSelected] = useState([1, 0]);
  const [frame, setFrame] = useState({ fired: [] as boolean[][], trace: [] as number[] });
  const activations = useMemo(() => forward(network, input[0], input[1]), [network, input]);
  const current = useRef(activations); current.current = activations;
  useEffect(() => { if (!ref.current) return; const ro = new ResizeObserver(entries => setWidth(entries[0].contentRect.width)); ro.observe(ref.current); return () => ro.disconnect(); }, []);
  useEffect(() => { membrane.current = network.shape.map(n => Array(n).fill(0)); trace.current = []; setFrame({ fired: [], trace: [] }); setSelected([1, 0]); }, [network.shape.join(',')]);
  useEffect(() => {
    if (!motion || !spikes) return;
    const timer = window.setInterval(() => {
      if (document.hidden) return;
      const fired = current.current.map((layer, l) => layer.map((activation, j) => {
        const step = spikeStep(membrane.current[l]?.[j] || 0, activation, .05);
        if (!membrane.current[l]) membrane.current[l] = [];
        membrane.current[l][j] = step.voltage; return step.fired;
      }));
      trace.current = [...trace.current.slice(-79), fired[selected[0]]?.[selected[1]] ? 1 : membrane.current[selected[0]]?.[selected[1]] || 0];
      setFrame({ fired, trace: trace.current });
    }, 50);
    return () => window.clearInterval(timer);
  }, [motion, spikes, selected]);
  const height = Math.max(260, Math.max(...network.shape) * 35 + 80), r = width < 380 ? 12 : 17;
  const pos = (l: number, j: number) => [42 + l * (width - 84) / (network.shape.length - 1), 56 + (j + .5) * (height - 94) / network.shape[l]];
  const value = activations[selected[0]]?.[selected[1]] || 0;
  return <div ref={ref}>
    <svg className="graph" viewBox={`0 0 ${width} ${height}`} role="img" aria-label="Live neural network. Solid lines show positive weights and dashed lines negative weights, thickness shows magnitude, and neuron pulses encode actual activation strength.">
      {network.weights.flatMap((layer, l) => layer.flatMap((row, j) => row.slice(0, -1).map((w, i) => {
        const [x1, y1] = pos(l, i), [x2, y2] = pos(l + 1, j), fired = spikes && motion && frame.fired[l]?.[i];
        return <line key={`${l}-${j}-${i}`} x1={x1 + r} y1={y1} x2={x2 - r} y2={y2} stroke={w >= 0 ? 'var(--n-positive)' : 'var(--n-negative)'} strokeDasharray={w < 0 ? '4 3' : undefined} strokeOpacity={fired ? .85 : Math.min(.5, .18 + Math.abs(w) * .12)} strokeWidth={Math.min(3, .5 + Math.abs(w) * .5)} />;
      }))) }
      {network.shape.map((n, l) => <text key={l} x={pos(l, 0)[0]} y="25" textAnchor="middle" fill="var(--text-muted)" fontSize="11">{l === 0 ? 'Input' : l === network.shape.length - 1 ? 'Output' : `Hidden ${l}`}</text>)}
      {activations.flatMap((layer, l) => layer.map((a, j) => {
        const [x, y] = pos(l, j), fired = spikes && motion && frame.fired[l]?.[j], color = a >= 0 ? 'var(--n-positive)' : 'var(--n-negative)', picked = selected[0] === l && selected[1] === j;
        return <g className="weight-node" key={`${l}-${j}`} onClick={() => {setSelected([l, j]);trace.current = [];}}>
          <title>{`Layer ${l}, neuron ${j + 1}: activation ${a.toFixed(3)}`}</title>
          {fired && <circle cx={x} cy={y} r={r + 7} fill={color} opacity=".18" />}
          <circle cx={x} cy={y} r={r} fill={fired ? color : 'var(--bg-card)'} stroke={picked ? 'var(--text)' : color} strokeWidth={picked ? 2 : 1.2} strokeOpacity={Math.max(.35, Math.abs(a))} />
          <circle cx={x} cy={y} r={Math.max(1, Math.abs(a) * (r - 5))} fill={color} opacity={fired ? 1 : .32} />
          <text x={x} y={y + 4} textAnchor="middle" fontSize={width < 380 ? 9 : 10} fill={fired ? 'var(--bg)' : 'var(--text)'}>{a.toFixed(1)}</text>
        </g>;
      }))}
    </svg>
    <div className="pad" style={{ borderTop: '1px solid var(--n-line)' }}>
      <div className="row space"><span className="muted">Inspect neuron</span><select aria-label="Inspect neuron" style={{ width: 'auto', maxWidth: '100%' }} value={selected.join(':')} onChange={e => {setSelected(e.target.value.split(':').map(Number));trace.current=[];}}>{network.shape.flatMap((n,l)=>Array.from({length:n},(_,j)=><option key={`${l}:${j}`} value={`${l}:${j}`}>Layer {l} · neuron {j+1}</option>))}</select></div>
      <div className="row space" style={{marginTop:8}}><span className="small muted">{spikes ? 'Spike encoder · membrane potential' : 'Dense activation'}</span><span className="small">a = {value.toFixed(3)}</span></div>
      {spikes && <svg viewBox={`0 0 ${Math.max(280,width-32)} 42`} style={{width:'100%',display:'block',marginTop:8}} role="img" aria-label="Selected neuron's spike encoding trace; peaks indicate threshold crossings"><line x1="0" x2={width-32} y1="6" y2="6" stroke="var(--border-strong)" strokeDasharray="3 4"/><path d={frame.trace.map((v,i)=>`${i?'L':'M'}${i/79*Math.max(280,width-32)},${38-v*32}`).join(' ')} fill="none" stroke="var(--n-positive)" strokeWidth="1.5"/></svg>}
    </div>
  </div>;
}

function NeuralWorkspace({ projectId, installId }: Props) {
  const [list, setList] = useState<{ id: number; name: string; status: string }[]>([]), [id, setID] = useState(0);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null), [error, setError] = useState(''), [busy, setBusy] = useState(false), [loading,setLoading]=useState(true), [connectionError,setConnectionError]=useState('');
  const [config, setConfig] = useState({ name: 'My first network', dataset: 'xor', hidden: '6,4', learning_rate: .03, epochs: 800, seed: 42 });
  const [input, setInput] = useState([.6, .45]), [auto, setAuto] = useState(false), [spikes, setSpikes] = useState(true);
  const [motion, setMotion] = useState(() => !window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  const [versions, setVersions] = useState<Version[]>([]), [deployments, setDeployments] = useState<Deployment[]>([]), [tab, setTab] = useState('lab');
  const [serverPrediction,setServerPrediction]=useState<any>(null), [target,setTarget]=useState('experiment');
  const generation = useRef(0), mounted = useRef(true), mutating = useRef(false);
  const rpc = useCallback(async (tool: string, args: Record<string, unknown> = {}, signal?: AbortSignal) => {
    const q = new URLSearchParams({ project_id: projectId, install_id: String(installId) });
    const res = await fetch(`/api/apps/neural/rpc?${q}`, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ tool, args }), signal });
    const data = await res.json(); if (!res.ok) throw Error(data.error || `Request failed (${res.status})`); return data;
  }, [projectId, installId]);
  const reloadList = useCallback(async () => { const data = await rpc('experiments_list'); if(mounted.current)setList(data.experiments); return data.experiments; }, [rpc]);
  const reloadSaved = useCallback(async () => { const [v,d] = await Promise.all([rpc('model_versions_list'),rpc('deployments_list')]);if(mounted.current){setVersions(v.versions);setDeployments(d.deployments);} },[rpc]);
  useEffect(() => {
    const bridge = (window as unknown as { __aptevaAppEvents?: { subscribe: (app: string, project: string, listener: (event: { install_id: number; topic: string }) => void) => () => void } }).__aptevaAppEvents;
    return bridge?.subscribe('neural', projectId, event => {
      if (event.install_id !== installId) return;
      if (event.topic.startsWith('neural.experiment.')) {
        reloadList().then(items => {if(mounted.current)setID(current=>current||items[0]?.id||0);}).catch(e=>{if(mounted.current)setConnectionError(e.message);});
      }
    });
  }, [projectId,installId,reloadList]);
  useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;generation.current++;};},[]);
  useEffect(() => {let cancelled=false;generation.current++;setSnapshot(null);setID(0);setLoading(true);setError('');setVersions([]);setDeployments([]);rpc('experiments_list').then(data=>{if(!cancelled){setList(data.experiments);setID(data.experiments[0]?.id || 0);}}).catch(e=>{if(!cancelled)setError(e.message);}).finally(()=>{if(!cancelled)setLoading(false);});return()=>{cancelled=true;};}, [rpc]);
  useEffect(() => {
    if (!id) return;let cancelled=false;const controller=new AbortController();let timer:ReturnType<typeof setTimeout>;
    async function poll() {const g=generation.current;try {if (!document.hidden && !mutating.current) {const data=await rpc('experiments_get',{id},controller.signal);if(!cancelled&&g===generation.current){setSnapshot(data);setConnectionError('');}}}catch(e){if(!cancelled&&g===generation.current)setConnectionError((e as Error).message);}finally{if(!cancelled)timer=setTimeout(poll,500);}}
    poll();return()=>{cancelled=true;controller.abort();clearTimeout(timer);};
  }, [id,rpc]);
  useEffect(()=>{if(!auto||!motion||!snapshot?.points.length)return;let i=0;const points=snapshot.points;const t=setInterval(()=>{if(!document.hidden){const p=points[(i++*17)%points.length];setInput([p.x,p.y]);}},850);return()=>clearInterval(t);},[auto,motion,id,!!snapshot?.points.length]);
  const run = async (fn:()=>Promise<void>) => {if(mutating.current)return;mutating.current=true;setBusy(true);setError('');generation.current++;try{await fn();}catch(e){if(mounted.current)setError((e as Error).message);}finally{mutating.current=false;generation.current++;if(mounted.current)setBusy(false);}};
  const create = () => run(async()=>{const data=await rpc('experiments_create',{...config,hidden:config.hidden.split(',').map(Number)});setSnapshot(null);setID(data.experiment.id);setServerPrediction(null);setTarget('experiment');await reloadList();setTab('lab');});
  const control = (action:string) => run(async()=>{const data=await rpc('experiments_control',{id,action});setSnapshot(data);await reloadList();});
  const e=snapshot?.experiment, net=e?.state.network;
  const activations=useMemo(()=>net?forward(net,input[0],input[1]):[],[net,input]);
  const probability=activations.at(-1)?.[0] || 0;
  const setProbe=(p:number[])=>{setAuto(false);setInput(p);setServerPrediction(null);};
  const params=net?.weights.flatMap(l=>l.flat()).length || 0;
  return <div className="neural-app"><style>{css}</style>
    <div className="top"><div><h1>Neural</h1><span className="muted">Train, inspect, and use small neural networks</span></div><div className="row"><select aria-label="Experiment" disabled={busy||!list.length} value={id} onChange={ev=>{generation.current++;setID(Number(ev.target.value));setSnapshot(null);setServerPrediction(null);setTarget('experiment');}}>{!list.length&&<option value={0}>No experiments yet</option>}{list.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</select><span className="badge">Local CPU</span></div></div>
    {(error||connectionError)&&<div className="notice" role="alert">{error||connectionError}</div>}
    <div className="row" style={{marginBottom:18}}><button aria-pressed={tab==='lab'} onClick={()=>{setTab('lab');setServerPrediction(null);setTarget('experiment');}}>Learning lab</button><button aria-pressed={tab==='versions'} onClick={()=>run(async()=>{await reloadSaved();setTab('versions');})}>Versions & endpoints</button></div>
    {tab==='lab'?<div className="layout">
      <aside className="box pad"><h2>New experiment</h2><div className="muted">Small enough to see every neuron.</div><div className="config-grid">
        <div><label htmlFor="neural-name">Name</label><input id="neural-name" maxLength={100} value={config.name} onChange={ev=>setConfig({...config,name:ev.target.value})}/></div>
        <div><label htmlFor="neural-dataset">Dataset</label><select id="neural-dataset" value={config.dataset} onChange={ev=>setConfig({...config,dataset:ev.target.value})}><option value="xor">XOR quadrants</option><option value="circles">Circle</option><option value="linear">Linear split</option></select></div>
        <div><label htmlFor="neural-shape">Hidden neurons</label><select id="neural-shape" value={config.hidden} onChange={ev=>setConfig({...config,hidden:ev.target.value})}><option value="4">4</option><option value="6,4">6 → 4</option><option value="8,6">8 → 6</option><option value="12,8">12 → 8</option></select></div>
        <div><label htmlFor="neural-rate">Learning rate</label><select id="neural-rate" value={config.learning_rate} onChange={ev=>setConfig({...config,learning_rate:Number(ev.target.value)})}><option value={.001}>0.001 · slow</option><option value={.01}>0.01</option><option value={.03}>0.03</option><option value={.1}>0.1 · fast</option></select></div>
        <div><label htmlFor="neural-epochs">Epochs</label><input id="neural-epochs" type="number" min={10} max={2000} value={config.epochs} onChange={ev=>setConfig({...config,epochs:Number(ev.target.value)})}/></div>
        <div><label htmlFor="neural-seed">Random seed</label><input id="neural-seed" type="number" min={0} max={2147483647} value={config.seed} onChange={ev=>setConfig({...config,seed:Number(ev.target.value)})}/></div>
      </div><button className="primary block" disabled={busy||loading} onClick={create}>Create network</button><p className="muted small">192 training points<br/>96 held-out validation points<br/>Tanh · sigmoid · Adam</p></aside>
      <div className="stack">{snapshot&&e&&net?<>
        <section className="box"><div className="section row space"><div><h2>{e.name}</h2><span className="muted">{net.shape.join(' → ')} · {params} parameters · {e.state.config.dataset}</span></div><span className={`badge ${e.status}`}>{e.status}</span></div>
          <div className="pad" style={{paddingBottom:0}}><div className="row space"><div className="legend"><span><i className="dot"/>Positive weight</span><span><i className="dot negative"/>Negative · dashed</span></div><label className="check"><input type="checkbox" checked={spikes} onChange={ev=>setSpikes(ev.target.checked)}/>Spike view</label></div></div>
          <Neurons key={e.id} network={net} input={input} spikes={spikes} motion={motion}/>
          <div className="metrics"><div className="stat"><div className="muted">Epoch</div><div className="number" data-testid="epoch">{e.state.epoch}<span className="muted"> / {e.state.config.epochs}</span></div></div><div className="stat"><div className="muted">Training loss</div><div className="number">{snapshot.metric.loss.toFixed(4)}</div></div><div className="stat"><div className="muted">Validation accuracy</div><div className="number">{(snapshot.metric.validation_accuracy*100).toFixed(1)}<span className="muted">%</span></div></div></div>
        </section>
        <section className="box pad"><div className="row space"><h2>Learning curve</h2><div className="legend"><span><i className="dot"/>Train</span><span><i className="dot negative"/>Validation</span></div></div><LossChart history={e.state.history}/><div className="actionbar"><button className="primary" disabled={busy||e.status==='completed'||e.status==='failed'} onClick={()=>control(e.status==='running'?'pause':'start')}>{e.status==='running'?'Pause training':'Train network'}</button><button disabled={busy||e.status!=='paused'} onClick={()=>control('step')}>Step 1 epoch</button><button disabled={busy||e.state.epoch===0||e.status==='failed'} onClick={()=>run(async()=>{await rpc('model_versions_create',{experiment_id:id});await reloadSaved();setTab('versions');})}>Save version</button></div>{e.state.error&&<p role="alert">{e.state.error}</p>}</section>
      </>:<section className="box empty"><div className="eyebrow">Your first neural network</div><h2>{loading||id?'Loading your network…':'Make a network. Watch it learn.'}</h2><p className="muted">Create an experiment, then start training.<br/>Probe the decision map to see neurons respond.</p></section>}</div>
      <aside className="stack right">{snapshot&&e&&net&&<><section className="box pad"><div className="row space"><h2>Decision map</h2><span className="muted">Click to probe</span></div><div style={{margin:'12px 0'}}><DecisionMap network={net} points={snapshot.points} input={input} onInput={setProbe}/></div><div className="legend"><span><i className="dot negative"/>Class A</span><span><i className="dot"/>Class B</span></div><label className="check"><input type="checkbox" checked={auto} disabled={!motion} onChange={ev=>setAuto(ev.target.checked)}/>Cycle training inputs</label><label className="check"><input type="checkbox" checked={motion} onChange={ev=>setMotion(ev.target.checked)}/>Animate neuron activity</label></section>
      <section className="box pad"><h2>Live probe</h2><div className="row space" style={{margin:'12px 0'}}><span className="muted">Probability of B</span><span className="prediction">{(probability*100).toFixed(1)}%</span></div><label htmlFor="neural-x">Input X <span style={{float:'right'}}>{input[0].toFixed(2)}</span></label><input id="neural-x" type="range" min={-1} max={1} step={.01} value={input[0]} onChange={ev=>setProbe([Number(ev.target.value),input[1]])}/><label htmlFor="neural-y">Input Y <span style={{float:'right'}}>{input[1].toFixed(2)}</span></label><input id="neural-y" type="range" min={-1} max={1} step={.01} value={input[1]} onChange={ev=>setProbe([input[0],Number(ev.target.value)])}/><button className="block" disabled={busy} onClick={()=>run(async()=>{setServerPrediction({...await rpc('predictions_create',{experiment_id:id,x:input[0],y:input[1]}),x:input[0],y:input[1]});})}>Verify on server</button>{serverPrediction&&<p className="muted" aria-live="polite">Server snapshot at ({serverPrediction.x?.toFixed(2)}, {serverPrediction.y?.toFixed(2)}): {(serverPrediction.probability*100).toFixed(1)}% B · epoch {serverPrediction.epoch}</p>}</section></>}</aside>
    </div>:<div className="layout" style={{gridTemplateColumns:'repeat(auto-fit,minmax(260px,1fr))'}}>
      <section className="box pad"><h2>Saved versions</h2><p className="muted">Immutable weights, architecture, and metrics.</p>{!versions.length&&<p>No saved versions yet. Train a network, then save it.</p>}{versions.map(v=><div className="saved" key={v.id}><div className="row space"><span>{v.name} · v{v.id}</span><span className="muted">Epoch {v.epoch}</span></div><button style={{marginTop:8}} disabled={busy} onClick={()=>run(async()=>{await rpc('deployments_create',{version_id:v.id});await reloadSaved();})}>Deploy this version</button></div>)}</section>
      <section className="box pad"><h2>Prediction endpoints</h2><p className="muted">Project-authenticated CPU inference · pinned versions.</p>{!deployments.length&&<p>No endpoints yet.</p>}{deployments.map(d=><div className="saved" key={d.id}><div>{d.name} · version {d.version_id}</div><div className="endpoint">POST {d.endpoint}</div><button style={{marginTop:8}} disabled={busy} onClick={()=>run(async()=>{setTarget(String(d.id));setServerPrediction(await rpc('predictions_create',{deployment_id:d.id,x:input[0],y:input[1]}));})}>Test with current input</button></div>)}{serverPrediction&&target!=='experiment'&&<p aria-live="polite">Endpoint {target}: {(serverPrediction.probability*100).toFixed(1)}% B · version {serverPrediction.version_id}</p>}</section>
    </div>}
    <div className="foot"><span>Real CPU training · persisted weights & optimizer · reproducible seeds</span><span>Spike view encodes dense activations with integrate-and-fire neurons; the trained model is a dense network.</span></div>
  </div>;
}

export default function NeuralPanel(props: Props) {
  return <NeuralWorkspace key={`${props.projectId}:${props.installId}`} {...props} />;
}
