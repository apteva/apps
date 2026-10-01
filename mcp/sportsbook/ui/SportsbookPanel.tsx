// Native project panel, matching CRM's tabbed list/detail layout.
// The dashboard owns navigation, typography, theme and the React instance.
// Every request uses its authenticated app proxy and installation scope.
import {useCallback, useEffect, useRef, useState} from "react";
import type {ButtonHTMLAttributes, ReactNode} from "react";

type Event = {id:string;sport:string;competition:string;competition_id:string;home:string;away:string;starts_at:number;status:string;home_score:number|null;away_score:number|null;source:string;example:number};
type Market = {id:string;event_id:string;rules:string;outcome_profile:string};
type Quote = {id:number;market_id:string;selection:string;bookmaker:string;odds_micros:number;observed_at:number;source:string;connection_id:number};
type Prediction = {id:string;market_id:string;model:string;probabilities:Record<string,number>;features:{home_samples?:number;away_samples?:number;note?:string;calibrated?:boolean};expires_at:number;created_at:number};
type Bankroll = {id:string;name:string;currency:string;cash_minor:number;locked_minor:number;pnl_minor:number;max_stake_bps:number;max_exposure_bps:number};
type Proposal = {id:string;event_id:string;bankroll_id:string;selection:string;bookmaker:string;odds_micros:number;stake_minor:number;expected_value:number;status:string;expires_at:number;rationale:string};
type Bet = {id:string;event_id:string;bankroll_id:string;selection:string;bookmaker:string;odds_micros:number;stake_minor:number;status:string;settlement_note:string};
type Provider = {id:number;slug:string;default:boolean;kind:string;sports:string[];capabilities:string[]};
type Route = {role:string;sport:string;connection_id:number;model:string};
type Integrations = {roles:Record<string,Provider[]>;routes:Route[]};
type Sport = {id:string;name:string;enabled:number};
type Competition = {id:string;sport:string;name:string;enabled:number};
type MarketSettings = {sport:string;type:string;outcome_profile:string;rules:string;prediction_model:string;history_scope:string;home_advantage:number;enabled:number};
type Mapping = {sport:string;competition_id:string;role:string;provider_slug:string;external_key:string;enabled:number};
type Catalog = {sports:Sport[];competitions:Competition[];sport_market_types:MarketSettings[];provider_sport_mappings:Mapping[]};
const emptyCatalog:Catalog={sports:[],competitions:[],sport_market_types:[],provider_sport_mappings:[]};
const marketOutcomes=(m?:Market)=>m?.outcome_profile==='three_way'?['home','draw','away']:['home','away'];
type Workspace = {events:Event[];markets:Market[];quotes:Quote[];predictions:Prediction[];bankrolls:Bankroll[];proposals:Proposal[];bets:Bet[];explanations:{prediction_id:string;text:string;model:string}[];server_time:number};
export interface NativePanelProps {
  appName: string;
  installId: number;
  projectId: string;
  instanceId?: number;
  eventRevision?: number;
}
const empty:Workspace={events:[],markets:[],quotes:[],predictions:[],bankrolls:[],proposals:[],bets:[],explanations:[],server_time:0};
const labels:Record<string,string>={sports_data:'Sports data',odds:'Odds',execution:'Execution',llm:'LLM'};
const pct=(n:number)=>`${(n*100).toFixed(1)}%`;
const odds=(q:Quote|Proposal|Bet)=> (q.odds_micros/1e6).toFixed(2);
const money=(n:number,currency='EUR')=>new Intl.NumberFormat(undefined,{style:'currency',currency}).format(n/100);
const datetime=(t:number)=>new Date(t*1000).toLocaleString(undefined,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
function minor(s:string){if(!/^\d+(\.\d{1,2})?$/.test(s)) throw Error('Enter an amount with at most two decimal places.'); const [whole,fraction='']=s.split('.');const amount=Number(whole)*100+Number(fraction.padEnd(2,'0'));if(!Number.isSafeInteger(amount)||amount<=0)throw Error('Enter a positive amount.');return amount;}

const inputClass = "w-full min-w-0 bg-bg-input border border-border rounded px-2 py-1 text-sm text-text";
const mutedClass = "text-xs text-text-muted";

function Button({primary=false,className='',...props}:ButtonHTMLAttributes<HTMLButtonElement>&{primary?:boolean}) {
 return <button type="button" {...props} className={`px-2 py-1 text-xs border rounded whitespace-nowrap disabled:opacity-50 disabled:cursor-default ${primary?'border-accent text-accent hover:bg-accent hover:text-bg':'border-border text-text hover:bg-bg-input'} ${className}`}/>;
}
function Section({title,action,children}:{title:string;action?:ReactNode;children:ReactNode}) {
 return <section className="border border-border rounded">
  <div className="flex items-center justify-between gap-2 px-3 py-2 border-b border-border">
   <h2 className="text-sm font-medium text-text">{title}</h2>{action}
  </div>
  <div className="p-3 space-y-3">{children}</div>
 </section>;
}
function Pill({children,accent=false}:{children:ReactNode;accent?:boolean}) {
 return <span className={`px-1.5 py-0.5 rounded text-xs whitespace-nowrap ${accent?'bg-accent/10 text-accent':'bg-border text-text-muted'}`}>{children}</span>;
}
function Probabilities({prediction}:{prediction:Prediction}) {
 return <div className="space-y-2">
  {['home','draw','away'].filter(s=>prediction.probabilities[s]!==undefined).map(s=><div key={s} className="flex items-center gap-3 text-xs">
   <span className="w-12 text-text-muted">{s==='draw'?'Draw':s==='home'?'Home':'Away'}</span>
   <div className="flex-1 h-1 bg-border rounded overflow-hidden"><div className="h-full bg-accent" style={{width:pct(prediction.probabilities[s])}}/></div>
   <span className="w-12 text-right tabular-nums">{pct(prediction.probabilities[s])}</span>
  </div>)}
 </div>;
}
function Empty({children}:{children:ReactNode}) { return <div className="p-6 text-sm text-text-muted text-center">{children}</div>; }

export default function SportsbookPanel({appName='sportsbook',projectId,installId,eventRevision=0}:NativePanelProps) {
 const [catalog,setCatalog]=useState<Catalog>(emptyCatalog);
 const [data,setData]=useState<Workspace>(empty),[integrations,setIntegrations]=useState<Integrations>({roles:{},routes:[]});
 const [tab,setTab]=useState('events'),[sport,setSport]=useState(''),[example,setExample]=useState(false),[selected,setSelected]=useState(''),[search,setSearch]=useState('');
 const [error,setError]=useState(''),[notice,setNotice]=useState(''),[busy,setBusy]=useState(false),[loading,setLoading]=useState(true);
 const [quoteID,setQuoteID]=useState(0),[bankrollID,setBankrollID]=useState(''),[stake,setStake]=useState('10.00'),[rationale,setRationale]=useState('');
 const [date,setDate]=useState(new Date().toISOString().slice(0,10)),[sportKey,setSportKey]=useState(''),[syncCompetition,setSyncCompetition]=useState(''),[syncSport,setSyncSport]=useState('football'),[model,setModel]=useState('');
 const [settlement,setSettlement]=useState<{bet:Bet;outcome:string}|null>(null),[settlementNote,setSettlementNote]=useState('Manual paper settlement for simulation');
 const [newBankroll,setNewBankroll]=useState(false),[bankrollName,setBankrollName]=useState('My paper bankroll'),[initial,setInitial]=useState('1000.00'),[currency,setCurrency]=useState('EUR');
 const [clock,setClock]=useState(Date.now()/1000);const mounted=useRef(true),requestID=useRef(0),mutation=useRef(false);
 const scope=JSON.stringify([appName,projectId,installId,example]);
 const scopeRef=useRef(scope);scopeRef.current=scope;
 const rpc=useCallback(async(tool:string,args:Record<string,unknown>={})=>{
  const params=new URLSearchParams();if(projectId)params.set('project_id',projectId);if(installId)params.set('install_id',String(installId));
  const res=await fetch(`/api/apps/${encodeURIComponent(appName)}/rpc?${params}`,{method:'POST',credentials:'same-origin',headers:{'Content-Type':'application/json'},body:JSON.stringify({tool,args})});
  const body=await res.json();if(!res.ok)throw Error(body.error||`Request failed (${res.status})`);return body;
 },[projectId,installId,appName]);
 const refresh=useCallback(async()=>{const id=++requestID.current;const startedScope=scope;const [workspace,connections,sportsCatalog]=await Promise.all([rpc('workspace_get',{example}),rpc('integrations_list'),rpc('catalog_get')]);if(mounted.current&&id===requestID.current&&scopeRef.current===startedScope){setData(workspace);setIntegrations(connections);setCatalog(sportsCatalog);setClock(workspace.server_time);setError('');}},[rpc,example,scope]);
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;requestID.current++;};},[]);
 useEffect(()=>{let cancelled=false;setLoading(true);setData(empty);setCatalog(emptyCatalog);setSelected('');setQuoteID(0);setBankrollID('');setSettlement(null);setError('');setNotice('');refresh().catch(e=>{if(mounted.current&&!cancelled)setError(e.message);}).finally(()=>{if(mounted.current&&!cancelled)setLoading(false);});return()=>{cancelled=true;requestID.current++;};},[refresh]);
 useEffect(()=>{const t=window.setInterval(()=>setClock(v=>v+5),5000);return()=>window.clearInterval(t);},[]);
 const run=async(operation:()=>Promise<unknown>,message='')=>{if(mutation.current)return;const startedScope=scopeRef.current;mutation.current=true;setBusy(true);setError('');setNotice('');try{await operation();if(mounted.current&&scopeRef.current===startedScope){await refresh();if(message&&scopeRef.current===startedScope)setNotice(message);}}catch(e){if(mounted.current&&scopeRef.current===startedScope)setError(e instanceof Error?e.message:String(e));}finally{mutation.current=false;if(mounted.current)setBusy(false);}};
 const upcoming=data.events.filter(e=>e.status==='scheduled'&&e.starts_at>clock).sort((a,b)=>a.starts_at-b.starts_at);
 const visible=upcoming.filter(e=>(!sport||sport===e.sport)&&(!search||`${e.home} ${e.away} ${e.competition}`.toLowerCase().includes(search.toLowerCase())));
 const event=visible.find(e=>e.id===selected)||visible[0];const market=data.markets.find(m=>m.event_id===event?.id);
 const prediction=data.predictions.find(p=>p.market_id===market?.id);const freshPrediction=prediction&&prediction.expires_at>clock?prediction:undefined;
 const quotes=data.quotes.filter(q=>q.market_id===market?.id&&q.observed_at+900>clock);
 const quote=quotes.find(q=>q.id===quoteID);const bankroll=data.bankrolls.find(b=>b.id===bankrollID)||data.bankrolls[0];
 const cash=data.bankrolls.reduce((s,b)=>s+b.cash_minor,0),locked=data.bankrolls.reduce((s,b)=>s+b.locked_minor,0);const currencies=new Set(data.bankrolls.map(b=>b.currency));const totalCurrency=currencies.size===1?data.bankrolls[0]?.currency:'EUR';
 const name=(id:string)=>{const e=data.events.find(e=>e.id===id);return e?`${e.home} vs ${e.away}`:'Event'};
 const selectionName=(selection:string,e=event)=>selection==='draw'?'Draw':selection==='home'?e?.home||'Home':e?.away||'Away';
 const bookmakers=[...new Set(quotes.map(q=>q.bookmaker))];const outcomes=marketOutcomes(market);
 const latestExplanation=data.explanations.find(x=>x.prediction_id===prediction?.id);
 useEffect(()=>{if(catalog.sports.length&&!catalog.sports.some(s=>s.id===syncSport&&s.enabled)){setSyncSport(catalog.sports.find(s=>s.enabled)?.id||'');setSyncCompetition('');setSportKey('');}},[catalog,syncSport]);
 const syncData=(role:'sports_data'|'odds')=>run(async()=>{const out=await rpc(role==='odds'?'odds_sync':'sports_sync',{sport:syncSport,date,sport_key:sportKey,competition_id:syncCompetition,all_sources:role==='odds'});const failed=out.sources.filter((s:{success:boolean})=>!s.success);setNotice(out.sources.map((s:{provider:string;events:number;success:boolean})=>`${s.provider}: ${s.success?`${s.events} events`:'import failed'}`).join(' · '));if(failed.length)throw Error(failed.map((s:{provider:string;error?:string})=>`${s.provider}: ${s.error||'Import failed'}`).join(' · '));});
 const setEvent=(e:Event)=>{setSelected(e.id);setQuoteID(0);setRationale('');};
 const predict=()=>run(()=>rpc('prediction_run',{market_id:market?.id}),'Prediction saved with its source evidence.');
 const proposal=()=>run(()=>rpc('bet_propose',{bankroll_id:bankroll?.id,prediction_id:freshPrediction?.id,quote_id:quote?.id,stake_minor:minor(stake),rationale}),'Paper proposal created. Review it in Bets.');
 const settle=(bet:Bet,outcome:string)=>{setSettlementNote('Manual paper settlement for simulation');setSettlement({bet,outcome});};
 useEffect(()=>{if(eventRevision>0)void refresh().catch(e=>{if(mounted.current)setError(e.message);});},[eventRevision,refresh]);
 return <div className="h-full min-h-0 flex flex-col bg-bg text-text">
  <nav aria-label="Sportsbook views" role="tablist" className="flex gap-1 border-b border-border px-3 pt-2 text-xs overflow-x-auto shrink-0">
   {([['events','Events'],['predictions','Predictions'],['bets','Bets'],['sports','Sports'],['integrations','Integrations']] as const).map(([id,label])=>
    <button key={id} type="button" role="tab" id={`sportsbook-tab-${id}`} aria-selected={tab===id} aria-controls={`sportsbook-view-${id}`}
     onClick={()=>setTab(id)} className={`px-3 py-2 border-b-2 whitespace-nowrap ${tab===id?'border-accent text-accent':'border-transparent text-text-muted hover:text-text'}`}>{label}</button>
   )}
  </nav>
  <div className="flex items-center justify-between flex-wrap gap-2 px-3 py-2 border-b border-border text-xs shrink-0">
   <div className="flex items-center gap-2">
    <Pill accent>Paper betting</Pill>
    <select disabled={busy} aria-label="Data workspace" className="bg-bg-input border border-border rounded px-2 py-1 text-xs" value={example?'examples':'connected'} onChange={e=>setExample(e.target.value==='examples')}>
     <option value="connected">Connected data</option><option value="examples">Examples</option>
    </select>
    {example&&<Button disabled={busy} onClick={()=>void run(()=>rpc('demo_load'),'Examples loaded.')}>Load examples</Button>}
   </div>
   <div className="flex items-center gap-3">
    <span className="text-text-muted">{upcoming.length} events · {data.bets.filter(b=>b.status==='open').length} open bets</span>
    <Button disabled={busy||loading} onClick={()=>void run(refresh)}>Refresh</Button>
   </div>
  </div>
  {example&&<p className="px-3 py-2 text-xs text-text-dim border-b border-border shrink-0">Fictional fixtures and prices · Separate virtual bankrolls</p>}
  {error&&<div className="flex items-center justify-between gap-3 px-3 py-2 text-xs text-error border-b border-border shrink-0" role="alert"><span>{error}</span><Button aria-label="Dismiss error" onClick={()=>setError('')}>×</Button></div>}
  {notice&&<div className="flex items-center justify-between gap-3 px-3 py-2 text-xs text-accent border-b border-border shrink-0" role="status"><span>{notice}</span><Button aria-label="Dismiss message" onClick={()=>setNotice('')}>×</Button></div>}
  <div id={`sportsbook-view-${tab}`} role="tabpanel" aria-labelledby={`sportsbook-tab-${tab}`} className="flex-1 min-h-0 overflow-auto">
   {loading?<Empty>Loading sports workspace…</Empty>:<>
    {tab==='events'&&<div className="h-full flex flex-col md:flex-row">
     <aside className="w-full md:w-72 md:shrink-0 max-h-60 md:max-h-none border-b md:border-b-0 md:border-r border-border flex flex-col min-h-0">
      <div className="p-3 border-b border-border space-y-2">
       <input aria-label="Search events" className={inputClass} placeholder="Search teams, players or competitions…" value={search} onChange={e=>setSearch(e.target.value)}/>
       <select aria-label="Filter sport" className={inputClass} value={sport} onChange={e=>setSport(e.target.value)}>
        <option value="">All sports</option>{catalog.sports.map(s=><option key={s.id} value={s.id}>{s.name}{s.enabled?'':' (disabled)'}</option>)}
       </select>
      </div>
      <div className="flex-1 overflow-auto">
       {visible.length===0?<div className="p-4 space-y-3">
        <p className={mutedClass}>No upcoming events. Import fixtures and odds from your selected providers, or load fictional examples.</p>
        <Button primary onClick={()=>setTab('integrations')}>Import data</Button>
       </div>:<ul>{visible.map(e=>{
        const m=data.markets.find(m=>m.event_id===e.id);
        const qs=data.quotes.filter(q=>q.market_id===m?.id&&q.observed_at+900>clock);
        return <li key={e.id}><button type="button" aria-pressed={event?.id===e.id} onClick={()=>setEvent(e)} className={`w-full text-left px-3 py-3 border-b border-border hover:bg-bg-input ${event?.id===e.id?'bg-bg-input':''}`}>
         <div className="flex items-center justify-between gap-2 text-xs text-text-dim"><span className="truncate">{e.competition}</span><span className="shrink-0">{e.sport}</span></div>
         <div className="text-sm font-medium mt-1">{e.home} vs {e.away}</div>
         <div className="text-xs text-text-muted mt-1">{datetime(e.starts_at)} · {e.source}</div>
         <div className="flex gap-2 mt-2">
          {marketOutcomes(data.markets.find(m=>m.event_id===e.id)).map((s,i)=>{
           const best=qs.filter(q=>q.selection===s).sort((a,b)=>b.odds_micros-a.odds_micros)[0];
           return <span key={s} className="px-2 py-1 text-xs rounded border border-border tabular-nums"><span className="text-text-dim mr-2">{s==='draw'?'X':i===0?'1':'2'}</span>{best?odds(best):'—'}</span>;
          })}
         </div>
        </button></li>;
       })}</ul>}
      </div>
      <div className="p-2 text-xs text-text-dim border-t border-border">{visible.length} upcoming · {data.events.filter(e=>e.status==='finished').length} completed</div>
     </aside>
     <main className="flex-1 min-w-0 min-h-0 overflow-auto p-4 md:p-5">
      {!event?<Empty>Select an event to view prices, predictions and bet proposals.</Empty>:<div className="space-y-5">
       <header>
        <div className="flex items-center gap-2 mb-2"><Pill>{catalog.sports.find(s=>s.id===event.sport)?.name||event.sport}</Pill><Pill>{market?.rules==='regulation'?'Regulation time':'Completed match'}</Pill></div>
        <h1 className="text-lg font-medium">{event.home} vs {event.away}</h1>
        <p className="text-xs text-text-muted mt-1">{event.competition} · {datetime(event.starts_at)} · {event.source}</p>
       </header>
       <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">
        <div className="space-y-4">
         <Section title="Prediction" action={<Button primary disabled={busy||!market} onClick={()=>void predict()}>{prediction?'Update prediction':'Run prediction'}</Button>}>
          {prediction?<>
           <div className="flex items-center justify-between gap-2"><Pill accent>{prediction.model.startsWith('elo')?'Experimental Elo':'Bookmaker baseline'}</Pill><span className={mutedClass}>{prediction.expires_at>clock?'Current':'Expired'}</span></div>
           <Probabilities prediction={prediction}/>
           <p className={mutedClass}>{prediction.features.note}</p>
          </>:<p className={mutedClass}>Use imported results to estimate probabilities. Insufficient history produces a labeled bookmaker baseline.</p>}
         </Section>
         <Section title="Match-winner prices" action={<span className={mutedClass}>{outcomes.includes('draw')?'1 / X / 2':'1 / 2'}</span>}>
          {bookmakers.length?<div className="divide-y divide-border">{bookmakers.map(book=><div key={book} className="flex items-center justify-between gap-2 py-2">
           <div className="min-w-0"><div className="text-xs font-medium truncate">{book}</div><div className="text-xs text-text-dim">{quotes.find(q=>q.bookmaker===book)?.source}</div></div>
           <div className="flex gap-1 shrink-0">{outcomes.map(s=>{
            const q=quotes.filter(q=>q.bookmaker===book&&q.selection===s).sort((a,b)=>b.odds_micros-a.odds_micros)[0];
            return q?<Button key={s} primary={quoteID===q.id} aria-pressed={quoteID===q.id} aria-label={`${selectionName(s)} at ${odds(q)} with ${book}`} onClick={()=>setQuoteID(q.id)} className="tabular-nums">{odds(q)}</Button>:<span key={s} className="text-xs text-text-dim">—</span>;
           })}</div>
          </div>)}</div>:<p className={mutedClass}>No fresh prices. Import odds for this event's date.</p>}
          <p className="text-xs text-text-dim">Prices expire after 15 minutes.</p>
         </Section>
        </div>
        <Section title="Bet proposal" action={<Pill>Single · Paper</Pill>}>
         {quote?<>
          <h3 className="text-sm font-medium">{selectionName(quote.selection)}</h3>
          <p className={mutedClass}>{quote.bookmaker} · Decimal odds {odds(quote)}</p>
          <div><label htmlFor="sb-bankroll" className="block text-xs text-text-muted mb-1">Bankroll</label><select id="sb-bankroll" className={inputClass} value={bankroll?.id||''} onChange={e=>setBankrollID(e.target.value)}>
           {data.bankrolls.length?data.bankrolls.map(b=><option key={b.id} value={b.id}>{b.name} · {money(b.cash_minor,b.currency)}</option>):<option value="">Create a paper bankroll first</option>}
          </select></div>
          <div><label htmlFor="sb-stake" className="block text-xs text-text-muted mb-1">Stake ({bankroll?.currency||'EUR'})</label><input id="sb-stake" className={inputClass} inputMode="decimal" value={stake} onChange={e=>setStake(e.target.value)}/></div>
          <dl className="grid grid-cols-2 gap-3 text-xs"><div><dt className="text-text-muted">Estimated value</dt><dd className="text-accent text-sm mt-1 tabular-nums">{freshPrediction?pct(freshPrediction.probabilities[quote.selection]*quote.odds_micros/1e6-1):'Run prediction'}</dd></div><div><dt className="text-text-muted">Potential return</dt><dd className="text-sm mt-1 tabular-nums">{money((Number(stake)||0)*quote.odds_micros/1e6*100,bankroll?.currency)}</dd></div></dl>
          <div><label htmlFor="sb-rationale" className="block text-xs text-text-muted mb-1">Your reasoning</label><textarea id="sb-rationale" className={inputClass} rows={3} value={rationale} placeholder="Why consider this selection?" onChange={e=>setRationale(e.target.value)}/></div>
          <p className={mutedClass}>Stake cap {bankroll?(bankroll.max_stake_bps/100).toFixed(1):'2'}% · Exposure cap {bankroll?(bankroll.max_exposure_bps/100).toFixed(1):'10'}%</p>
          <Button primary disabled={busy||!bankroll||!freshPrediction||rationale.trim().length<10} onClick={()=>void proposal()}>Create paper proposal</Button>
         </>:<p className={mutedClass}>Select a bookmaker price to create a proposal. Review and accept it in Bets to reserve virtual funds.</p>}
        </Section>
       </div>
      </div>}
     </main>
    </div>}
    {tab==='predictions'&&<div className="p-4 space-y-4">
     <div className="flex justify-between items-center gap-3 flex-wrap"><p className={mutedClass}>Experimental Elo estimates and bookmaker baselines. Every run keeps its source evidence.</p><Button primary disabled={busy||!market} onClick={()=>void predict()}>Run for {event?.home||'selected event'}</Button></div>
     <div className="border border-border rounded overflow-x-auto">
      {data.predictions.length?<table className="w-full text-left text-xs"><thead className="text-text-dim border-b border-border"><tr>{['Event','Model','Home','Draw','Away','Status',''].map((s,i)=><th key={i} className="px-3 py-2 font-medium">{s}</th>)}</tr></thead><tbody>{data.predictions.map(p=>{
       const m=data.markets.find(m=>m.id===p.market_id),e=data.events.find(e=>e.id===m?.event_id);
       return <tr key={p.id} className="border-b border-border"><td className="px-3 py-3"><div className="text-sm">{e?`${e.home} vs ${e.away}`:'Event'}</div><div className="text-text-dim mt-1">{datetime(p.created_at)}</div></td><td className="px-3 py-3">{p.model.startsWith('elo')?'Experimental Elo':'Bookmaker baseline'}<div className="text-text-dim mt-1">{p.features.home_samples||0} / {p.features.away_samples||0} observations</div></td>{['home','draw','away'].map(s=><td key={s} className="px-3 py-3 tabular-nums">{p.probabilities[s]!==undefined?pct(p.probabilities[s]):'—'}</td>)}<td className="px-3 py-3">{p.expires_at>clock?'Current':'Expired'}</td><td className="px-3 py-3"><Button onClick={()=>{if(e)setEvent(e);setTab('events');}}>View event</Button></td></tr>;
      })}</tbody></table>:<Empty>No predictions. Select an event and run one after importing history or odds.</Empty>}
     </div>
     <Section title="AI explanation">
      <p className={mutedClass}>Explain the selected event's prediction using a bound LLM. The explanation cannot change probabilities or stakes.</p>
      <p className="text-xs">{event?`${event.home} vs ${event.away}`:'Select an event first'}</p>
      <div className="flex items-center gap-2 flex-wrap"><input aria-label="LLM model" className={inputClass} style={{maxWidth:300}} placeholder="Model available on your LLM account" value={model} onChange={e=>setModel(e.target.value)}/><Button disabled={busy||!freshPrediction||!integrations.roles.llm?.length||!model} onClick={()=>void run(()=>rpc('prediction_explain',{prediction_id:freshPrediction?.id,model}),'Explanation saved.')}>Explain prediction</Button></div>
      {latestExplanation&&<p className="whitespace-pre-wrap text-sm text-text-muted">{latestExplanation.text}</p>}
     </Section>
    </div>}
    {tab==='bets'&&<div className="p-4 space-y-4">
     <div className="flex items-center gap-4 flex-wrap text-xs"><span>Available: <strong className="font-medium">{currencies.size>1?'Multiple currencies':money(cash,totalCurrency)}</strong></span><span>Reserved: <strong className="font-medium">{currencies.size>1?'Multiple currencies':money(locked,totalCurrency)}</strong></span><span className="text-text-dim">Virtual funds only</span></div>
     <Section title="Bankrolls" action={<Button onClick={()=>setNewBankroll(v=>!v)}>+ New bankroll</Button>}>
      {data.bankrolls.length?<div className="divide-y divide-border">{data.bankrolls.map(b=><div key={b.id} className="flex items-center justify-between gap-3 flex-wrap py-2">
       <div><h3 className="text-sm font-medium">{b.name}</h3><p className={mutedClass}>Stake cap {b.max_stake_bps/100}% · Exposure cap {b.max_exposure_bps/100}%</p></div>
       <div className="text-xs text-right tabular-nums"><div>{money(b.cash_minor,b.currency)} available</div><div className="text-text-muted">{money(b.locked_minor,b.currency)} reserved · P/L {money(b.pnl_minor,b.currency)}</div></div>
      </div>)}</div>:<p className={mutedClass}>Create a virtual bankroll to start proposing bets.</p>}
      {newBankroll&&<div className="grid grid-cols-1 md:grid-cols-2 gap-3 border-t border-border pt-3">
       <div><label htmlFor="sb-bankroll-name" className="block text-xs text-text-muted mb-1">Name</label><input id="sb-bankroll-name" className={inputClass} value={bankrollName} onChange={e=>setBankrollName(e.target.value)}/></div>
       <div><label htmlFor="sb-initial" className="block text-xs text-text-muted mb-1">Starting virtual balance</label><input id="sb-initial" className={inputClass} inputMode="decimal" value={initial} onChange={e=>setInitial(e.target.value)}/></div>
       <select aria-label="Bankroll currency" className={inputClass} value={currency} onChange={e=>setCurrency(e.target.value)}>{['EUR','USD','GBP'].map(c=><option key={c}>{c}</option>)}</select>
       <div><Button primary disabled={busy} onClick={()=>void run(async()=>{await rpc('bankroll_create',{name:bankrollName,currency,initial_minor:minor(initial),example});setNewBankroll(false);},'Paper bankroll created.')}>Create bankroll</Button></div>
      </div>}
     </Section>
     <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">
      <Section title="Proposals" action={<Pill>{data.proposals.length}</Pill>}>
       {data.proposals.length?<div className="divide-y divide-border">{data.proposals.map(p=><div key={p.id} className="py-3 space-y-2">
        <div className="flex items-center justify-between gap-2"><h3 className="text-sm font-medium">{name(p.event_id)}</h3><Pill>{p.status==='accepted'?'Accepted':p.expires_at<=clock?'Expired':'Proposed'}</Pill></div>
        <p className="text-xs">{p.selection} · {p.bookmaker} · {odds(p)}</p><p className={mutedClass}>{p.rationale}</p>
        <div className="flex items-center justify-between gap-2 flex-wrap"><span className="text-xs">{money(p.stake_minor,data.bankrolls.find(b=>b.id===p.bankroll_id)?.currency)} · EV {pct(p.expected_value)}</span><Button primary disabled={busy||p.status!=='proposed'||p.expires_at<=clock} onClick={()=>void run(()=>rpc('proposal_accept',{proposal_id:p.id}),'Paper bet accepted and stake reserved.')}>Accept paper bet</Button></div>
       </div>)}</div>:<p className={mutedClass}>Proposals appear here for review.</p>}
      </Section>
      <Section title="Bets & settlements" action={<Pill>Paper only</Pill>}>
       {data.bets.length?<div className="divide-y divide-border">{data.bets.map(b=><div key={b.id} className="py-3 space-y-2">
        <div className="flex items-center justify-between gap-2"><h3 className="text-sm font-medium">{name(b.event_id)}</h3><Pill accent={b.status==='won'}>{b.status}</Pill></div>
        <p className="text-xs">{b.selection} · {b.bookmaker} · {odds(b)}</p><p className={mutedClass}>Stake {money(b.stake_minor,data.bankrolls.find(k=>k.id===b.bankroll_id)?.currency)}</p>
        {b.status==='open'?<div className="flex gap-2 flex-wrap">{['won','lost','void'].map(s=><Button disabled={busy} key={s} onClick={()=>settle(b,s)}>Settle {s}</Button>)}</div>:<p className={mutedClass}>{b.settlement_note}</p>}
       </div>)}</div>:<p className={mutedClass}>Accepted paper bets appear here.</p>}
      </Section>
     </div>
    </div>}
    {tab==='sports'&&<CatalogEditor catalog={catalog} integrations={integrations} busy={busy} rpc={rpc} run={run}/>}
    {tab==='integrations'&&<div className="p-4 space-y-4">
     <p className={mutedClass}>Bind connections in this app's platform settings. Choose a default for each role, with optional sport-specific routing.</p>
     <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">{Object.entries(labels).map(([role,label])=>
      <Section key={role} title={label} action={<Pill>Multiple providers</Pill>}>
       <p className={mutedClass}>{({sports_data:'Fixtures and results from selected sports data providers.',odds:'Complete bookmaker markets, price comparison and history.',execution:'Paper execution is built in. Connected wagering is reserved for a compatible executor app.',llm:'Supplemental explanations through selected language models.'} as Record<string,string>)[role]}</p>
       {integrations.roles[role]?.length?<div className="divide-y divide-border">{integrations.roles[role].map(p=><div key={p.id} className="flex justify-between gap-2 py-2">
        <div><span className="text-sm">{p.slug||'Execution app'}</span><div className={mutedClass}>{p.sports?.join(' · ')||p.capabilities?.join(' · ')||'Live execution disabled'}</div></div><Pill accent={p.default}>{p.default?'Default':'Selected'}</Pill>
       </div>)}</div>:<p className={mutedClass}>{role==='execution'?'Paper engine available. No connected executor.':'No providers selected.'}</p>}
       {!!integrations.roles[role]?.length&&<RoutePicker sports={catalog.sports} role={role} providers={integrations.roles[role]} routes={integrations.routes} busy={busy} save={(sport,id,model)=>run(()=>rpc('provider_route_set',{role,sport,connection_id:id,model}),'Provider route saved.')}/>}
      </Section>
     )}</div>
     <Section title="Import sports & odds">
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
       <div><label htmlFor="sb-sync-sport" className="block text-xs text-text-muted mb-1">Sport</label><select id="sb-sync-sport" className={inputClass} value={syncSport} onChange={e=>{setSyncSport(e.target.value);setSyncCompetition('');setSportKey('');}}>{catalog.sports.filter(s=>s.enabled).map(s=><option key={s.id} value={s.id}>{s.name}</option>)}</select></div>
       <div><label htmlFor="sb-sync-date" className="block text-xs text-text-muted mb-1">Event date (UTC)</label><input id="sb-sync-date" className={inputClass} type="date" value={date} onChange={e=>setDate(e.target.value)}/></div>
       <div><label htmlFor="sb-sync-competition" className="block text-xs text-text-muted mb-1">Competition</label><select id="sb-sync-competition" className={inputClass} value={syncCompetition} onChange={e=>{setSyncCompetition(e.target.value);setSportKey('');}}><option value="">Sport default mapping</option>{catalog.competitions.filter(c=>c.sport===syncSport&&c.enabled).map(c=><option key={c.id} value={c.id}>{c.name}</option>)}</select></div>
       <div><label htmlFor="sb-sport-key" className="block text-xs text-text-muted mb-1">Provider key override (optional)</label><input id="sb-sport-key" className={inputClass} value={sportKey} onChange={e=>setSportKey(e.target.value)} placeholder="Use saved provider mappings"/></div>
       <div className="flex items-end gap-2 flex-wrap"><Button disabled={busy||!syncSport} onClick={()=>void syncData('sports_data')}>Import sports data</Button><Button primary disabled={busy||!syncSport} onClick={()=>void syncData('odds')}>Import odds</Button></div>
      </div>
      <p className={mutedClass}>Configure sport and competition mappings in Sports. Sports data uses the routed provider; odds compares all compatible providers. A key override applies to every selected provider, so prefer saved mappings.</p>
      {example&&<p className="text-xs text-text-dim">Imports appear in Connected data. Switch workspace to view them.</p>}
     </Section>
    </div>}
   </>}
  </div>
  {settlement&&<div className="fixed inset-0 z-50 flex items-center justify-center bg-bg-overlay p-4">
   <section role="dialog" aria-modal="true" aria-labelledby="sb-settlement-title" className="w-full max-w-md bg-bg border border-border rounded-lg shadow-lg p-4 space-y-3"
    onKeyDown={e=>{if(e.key==='Escape'&&!busy)setSettlement(null);if(e.key==='Tab'){const items=Array.from(e.currentTarget.querySelectorAll<HTMLElement>('textarea:not(:disabled),button:not(:disabled)'));const first=items[0],last=items[items.length-1];if(e.shiftKey&&document.activeElement===first){e.preventDefault();last?.focus();}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first?.focus();}}}}>
    <h2 id="sb-settlement-title" className="text-sm font-medium">Settle paper bet as {settlement.outcome}</h2>
    <p className={mutedClass}>{name(settlement.bet.event_id)} · Virtual funds only</p>
    <div><label htmlFor="sb-settlement-note" className="block text-xs text-text-muted mb-1">Settlement note</label><textarea autoFocus id="sb-settlement-note" className={inputClass} rows={3} value={settlementNote} onChange={e=>setSettlementNote(e.target.value)}/></div>
    {error&&<p className="text-xs text-error" role="alert">{error}</p>}
    <div className="flex justify-end gap-2"><Button disabled={busy} onClick={()=>setSettlement(null)}>Cancel</Button><Button primary disabled={busy||settlementNote.trim().length<5} onClick={()=>void run(async()=>{await rpc('bet_settle',{bet_id:settlement.bet.id,outcome:settlement.outcome,note:settlementNote});setSettlement(null);},'Paper settlement recorded.')}>Save paper settlement</Button></div>
   </section>
  </div>}
 </div>;
}

function RoutePicker({sports,role,providers,routes,busy,save}:{sports:Sport[];role:string;providers:Provider[];routes:Route[];busy:boolean;save:(sport:string,id:number,model:string)=>Promise<void>}) {
 const [sport,setSport]=useState('*'),[id,setID]=useState(0),[model,setModel]=useState('');
 const eligible=providers.filter(p=>sport==='*'||p.sports.includes(sport));
 useEffect(()=>{
  const options=providers.filter(p=>sport==='*'||p.sports.includes(sport));
  const route=routes.find(r=>r.role===role&&r.sport===sport);
  const chosen=options.find(p=>p.id===route?.connection_id)||options.find(p=>p.default)||options[0];
  setID(chosen?.id||0);setModel(route?.model||'');
 },[role,sport,routes,providers]);
 return <div className="space-y-2 pt-2 border-t border-border">
  <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
   <div><label className="block text-xs text-text-muted mb-1">Apply route to</label><select aria-label={`${labels[role]} route sport`} className={inputClass} value={sport} onChange={e=>setSport(e.target.value)}><option value="*">All supported sports</option>{role!=='llm'&&role!=='execution'&&sports.filter(s=>s.enabled).map(s=><option key={s.id} value={s.id}>{s.name}</option>)}</select></div>
   <div><label className="block text-xs text-text-muted mb-1">Preferred provider</label><select aria-label={`${labels[role]} preferred provider`} className={inputClass} value={id} onChange={e=>setID(Number(e.target.value))}>{eligible.length?eligible.map(p=><option key={p.id} value={p.id}>{p.slug||`Execution app ${p.id}`}</option>):<option value={0}>No compatible provider</option>}</select></div>
  </div>
  {role==='llm'&&<input aria-label="Default LLM model" className={inputClass} value={model} placeholder="Model available on this account" onChange={e=>setModel(e.target.value)}/>}
  <Button disabled={busy||!eligible.some(p=>p.id===id)} onClick={()=>void save(sport,id,model)}>Save route</Button>
 </div>;
}

function CatalogEditor({catalog,integrations,busy,rpc,run}:{catalog:Catalog;integrations:Integrations;busy:boolean;rpc:(tool:string,args?:Record<string,unknown>)=>Promise<any>;run:(operation:()=>Promise<unknown>,message?:string)=>Promise<void>}) {
 const [selected,setSelected]=useState('football'),[id,setID]=useState('football'),[name,setName]=useState('Football'),[enabled,setEnabled]=useState(true);
 const [profile,setProfile]=useState('three_way'),[rules,setRules]=useState('regulation'),[predictionModel,setPredictionModel]=useState('elo'),[historyScope,setHistoryScope]=useState('competition'),[advantage,setAdvantage]=useState('60'),[marketEnabled,setMarketEnabled]=useState(true);
 const [competition,setCompetition]=useState(''),[competitionID,setCompetitionID]=useState(''),[competitionName,setCompetitionName]=useState(''),[competitionEnabled,setCompetitionEnabled]=useState(true);
 const [mappingCompetition,setMappingCompetition]=useState(''),[role,setRole]=useState('odds'),[slug,setSlug]=useState('the-odds-api'),[key,setKey]=useState(''),[mappingEnabled,setMappingEnabled]=useState(true);
 const [discoveryConnection,setDiscoveryConnection]=useState(0),[discovered,setDiscovered]=useState<{key:string;title:string;group:string;active:boolean}[]>([]);
 const resetSport=()=>{const s=catalog.sports.find(s=>s.id===selected),m=catalog.sport_market_types.find(m=>m.sport===selected);
  setID(s?.id||'');setName(s?.name||'');setEnabled(s?!!s.enabled:true);setProfile(m?.outcome_profile||'two_way');setRules(m?.rules||'match_completed');setPredictionModel(m?.prediction_model||'baseline');setHistoryScope(m?.history_scope||'competition');setAdvantage(String(m?.home_advantage||0));setMarketEnabled(m?!!m.enabled:true);
 };
 useEffect(()=>{resetSport();},[selected,catalog]);
 useEffect(()=>{setCompetition('');setMappingCompetition('');},[selected]);
 useEffect(()=>{const c=catalog.competitions.find(c=>c.id===competition);setCompetitionID(c?.id||'');setCompetitionName(c?.name||'');setCompetitionEnabled(c?!!c.enabled:true);},[competition,catalog]);
 useEffect(()=>{const m=catalog.provider_sport_mappings.find(m=>m.sport===selected&&m.role===role&&m.provider_slug===slug&&m.competition_id===mappingCompetition);setKey(m?.external_key||'');setMappingEnabled(m?!!m.enabled:true);},[selected,role,slug,mappingCompetition,catalog]);
 const competitions=catalog.competitions.filter(c=>c.sport===selected);
 const field=(label:string,value:string,set:(value:string)=>void,options?:[string,string][],disabled=false)=><div><label className="block text-xs text-text-muted mb-1">{label}{options?<select aria-label={label} disabled={disabled||busy} className={inputClass} value={value} onChange={e=>set(e.target.value)}>{options.map(([v,t])=><option key={v} value={v}>{t}</option>)}</select>:<input aria-label={label} disabled={disabled||busy} className={inputClass} value={value} onChange={e=>set(e.target.value)}/>}</label></div>;
 const check=(label:string,value:boolean,set:(value:boolean)=>void)=><label className="flex items-center gap-2 text-xs"><input type="checkbox" disabled={busy} checked={value} onChange={e=>set(e.target.checked)}/>{label}</label>;
 const providers=[['the-odds-api','The Odds API'],...(role==='sports_data'?[['the-sports-db','TheSportsDB'],...(selected==='football'?[['api-sports','API-Sports Football']]:[])]:[]),...(selected==='tennis'?[['api-tennis','API Tennis']]:[])] as [string,string][];
 const oddsConnections=integrations.roles.odds?.filter(p=>p.slug==='the-odds-api')||[];
 return <div className="p-4 space-y-4">
  <p className={mutedClass}>Define sports and competitions here, then map them to your providers. Import and prediction support follow the configured adapters and market rules.</p>
  <Section title="Sport & market settings">
   <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
    {field('Edit sport',selected,setSelected,[['','+ New sport'],...catalog.sports.map(s=>[s.id,`${s.name}${s.enabled?'':' (disabled)'}`] as [string,string])])}
    {field('Sport ID',id,setID,undefined,!!selected)}{field('Sport name',name,setName)}
    {field('Match-winner outcomes',profile,setProfile,[['two_way','Two outcomes: home / away'],['three_way','Three outcomes: home / draw / away']])}
    {field('Settlement rules',rules,setRules,[['regulation','Regulation time'],['match_completed','Completed match (including extra time)']])}
    {field('Prediction model',predictionModel,setPredictionModel,[['baseline','Bookmaker baseline'],['elo','Experimental Elo with baseline fallback'],['none','Disabled']])}
    {field('Historical scope',historyScope,setHistoryScope,[['competition','Same competition'],['sport','Across the sport']])}
    {field('Elo home advantage (0–200)',advantage,setAdvantage)}
   </div>
   <div className="flex gap-4 flex-wrap">{check('Sport enabled',enabled,setEnabled)}{check('Match-winner market enabled',marketEnabled,setMarketEnabled)}</div>
   <p className={mutedClass}>Market settings apply to new events; existing markets retain their original rules and model configuration. Disabling an entry blocks imports, predictions and new bets; open bets can still be settled. Additional market types such as totals, spreads and outrights require engine adapters.</p>
   <Button primary disabled={busy||!id||!name} onClick={()=>void run(async()=>{
    await rpc('sport_upsert',{sport:id,name,enabled,outcome_profile:profile,rules,prediction_model:predictionModel,history_scope:historyScope,home_advantage:Number(advantage),market_enabled:marketEnabled});setSelected(id);
   },'Sport and market settings saved.')}>Save sport</Button>
  </Section>
  {!!selected&&<>
   <Section title="Competitions">
    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
     {field('Edit competition',competition,setCompetition,[['','+ New competition'],...competitions.map(c=>[c.id,`${c.name}${c.enabled?'':' (disabled)'}`] as [string,string])])}
     {field('Competition ID',competitionID,setCompetitionID,undefined,!!competition)}{field('Competition name',competitionName,setCompetitionName)}
    </div>
    {check('Competition enabled',competitionEnabled,setCompetitionEnabled)}
    <Button disabled={busy||!competitionID||!competitionName} onClick={()=>void run(async()=>{await rpc('competition_upsert',{sport:selected,competition_id:competitionID,name:competitionName,enabled:competitionEnabled});setCompetition(competitionID);},'Competition saved.')}>Save competition</Button>
    <p className={mutedClass}>Unmapped provider competition names are registered on import. Add a competition and mapping first to unify names across providers.</p>
   </Section>
   <Section title="Provider mappings">
    <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
     {field('Mapping role',role,v=>{setRole(v);setSlug('the-odds-api');},[['odds','Odds'],['sports_data','Sports data']])}
     {field('Mapping provider',slug,setSlug,providers)}
     {field('Mapping competition',mappingCompetition,setMappingCompetition,[['','Sport default'],...competitions.map(c=>[c.id,c.name] as [string,string])])}
     {field('Provider sport / competition key',key,setKey)}
    </div>
    {check('Mapping enabled',mappingEnabled,setMappingEnabled)}
    <p className={mutedClass}>The Odds API uses keys such as basketball_nba. TheSportsDB uses a sport name such as Basketball at sport level; selected competitions are filtered by name. API-Sports and API Tennis use numeric league/tournament IDs at competition level.</p>
    <Button disabled={busy||!key} onClick={()=>void run(()=>rpc('provider_sport_mapping_set',{sport:selected,competition_id:mappingCompetition,role,provider_slug:slug,external_key:key,enabled:mappingEnabled}),'Provider mapping saved.')}>Save mapping</Button>
    <div className="divide-y divide-border">{catalog.provider_sport_mappings.filter(m=>m.sport===selected).map(m=><button type="button" key={`${m.role}:${m.provider_slug}:${m.competition_id}`} className="block w-full text-left text-xs py-2 hover:text-accent" style={{overflowWrap:'anywhere'}} onClick={()=>{setRole(m.role);setSlug(m.provider_slug);setMappingCompetition(m.competition_id);}}>{m.provider_slug} · {m.role} · {competitions.find(c=>c.id===m.competition_id)?.name||'Sport default'} → {m.external_key} {m.enabled?'':'(disabled)'}</button>)}</div>
   </Section>
  </>}
  <Section title="Discover provider coverage">
   <p className={mutedClass}>List active and out-of-season keys from a bound The Odds API account. Choose a sport and competition above, choose The Odds API as the mapping provider, then select a key to fill its mapping.</p>
   <div className="flex gap-2 flex-wrap">
    <select aria-label="Discovery connection" className={inputClass} style={{maxWidth:300}} value={discoveryConnection||oddsConnections[0]?.id||0} onChange={e=>setDiscoveryConnection(Number(e.target.value))}>{oddsConnections.length?oddsConnections.map(p=><option key={p.id} value={p.id}>{p.slug} · Connection {p.id}</option>):<option value={0}>Bind The Odds API to discover keys</option>}</select>
    <Button disabled={busy||!oddsConnections.length} onClick={()=>void run(async()=>{setDiscovered(await rpc('provider_sports_list',{connection_id:discoveryConnection||oddsConnections[0]?.id}));})}>Discover sports</Button>
   </div>
   {!!discovered.length&&<div className="max-h-60 overflow-auto divide-y divide-border">{discovered.map(s=><button type="button" key={s.key} className="block w-full text-left text-xs py-2 hover:text-accent" style={{overflowWrap:'anywhere'}} disabled={!selected||slug!=='the-odds-api'} onClick={()=>{setKey(s.key);setMappingEnabled(true);}}>{s.group} · {s.title} · {s.key} · {s.active?'Active':'Out of season'}</button>)}</div>}
  </Section>
 </div>;
}
