import {createContext, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode} from "react";
import {createPortal} from "react-dom";
import {useConversationLocalization} from "./i18n";

export interface CardPresentation {mode:"inline"|"side-panel"|"floating";width:number;height:number;active:boolean}
type Mode=Exclude<CardPresentation["mode"],"inline">;
interface Owner {anchor:HTMLElement;content:ReactNode;order:number;label:string}
interface View {key:string;scope:string;label:string;node:HTMLDivElement;owners:Map<string,Owner>;content:ReactNode;pinned:boolean}
interface Preference {key:string;mode:Mode}

function moveNode(node:HTMLElement,destination:HTMLElement|null|undefined){
  if(!destination||node.parentElement===destination)return;
  const move=(destination as HTMLElement&{moveBefore?:(node:Node,before:Node|null)=>void}).moveBefore;
  if(move&&node.isConnected&&destination.isConnected)move.call(destination,node,null);
  else destination.appendChild(node);
}

function createWorkspace(storageKey:string) {
  const views=new Map<string,View>(),listeners=new Set<()=>void>();
  let revision=0,scope="";
  let parking:HTMLElement|null=null;
  let preferences:Record<string,Preference>={};
  try {preferences=JSON.parse(sessionStorage.getItem(storageKey)??"{}")}catch{}
  const changed=()=>{revision++;for(const listener of listeners)listener()};
  const save=()=>{try{sessionStorage.setItem(storageKey,JSON.stringify(Object.fromEntries(Object.entries(preferences).slice(-50))))}catch{}};
  return {
    views,
    subscribe:(listener:()=>void)=>{listeners.add(listener);return()=>{listeners.delete(listener)}},
    snapshot:()=>revision,
    current:()=>{const preference=preferences[scope];const view=preference&&views.get(preference.key);return view?.pinned?{view,mode:preference.mode}:null},
    scope:()=>scope,
    setParking(element:HTMLElement|null){parking=element},
    activate(next:string){if(scope!==next){const current=this.current()?.view;if(current)moveNode(current.node,parking);scope=next;changed()}},
    invalidate(){for(const view of views.values())view.node.remove();views.clear();preferences={};save();changed()},
    register(key:string,nextScope:string,label:string,owner:string,anchor:HTMLElement,content:ReactNode,order:number){
      let view=views.get(key);
      if(!view){view={key,scope:nextScope,label,node:document.createElement("div"),owners:new Map(),content,pinned:preferences[nextScope]?.key===key};views.set(key,view)}
      view.owners.set(owner,{anchor,content,order,label});
      const latest=[...view.owners.values()].sort((a,b)=>b.order-a.order)[0];
      view.content=latest.content;view.label=latest.label;
      changed();
    },
    unregister(key:string,owner:string){
      const view=views.get(key);if(!view)return;
      if(view.pinned&&view.owners.get(owner)?.anchor.contains(view.node))moveNode(view.node,parking);
      view.owners.delete(owner);
      if(!view.owners.size&&!view.pinned){view.node.remove();views.delete(key)}
      changed();
    },
    open(key:string,mode:Mode){const view=views.get(key);if(!view)return;scope=view.scope;view.pinned=true;preferences[scope]={key,mode};save();changed()},
    close(){const view=this.current()?.view;if(!view)return;const primary=this.primary(view.key);moveNode(view.node,view.owners.get(primary??"")?.anchor??parking);view.pinned=false;const other=[...views.values()].find(item=>item.scope===scope&&item.pinned);if(other)preferences[scope]={key:other.key,mode:preferences[scope].mode};else delete preferences[view.scope];if(!view.owners.size){view.node.remove();views.delete(view.key)}save();changed()},
    primary(key:string){const view=views.get(key);return view?[...view.owners.entries()].sort((a,b)=>a[1].order-b[1].order)[0]?.[0]:undefined},
  };
}
type Workspace=ReturnType<typeof createWorkspace>;
const Context=createContext<Workspace|null>(null);
const Presentation=createContext<CardPresentation>({mode:"inline",width:0,height:0,active:true});
export const useCardPresentation=()=>useContext(Presentation);

// Preferences contain only opaque resource keys/mode, never component props,
// screenshots or credentials. The authorized transcript recreates each view.
export function CardWorkspaceProvider({storageKey,children}:{storageKey:string;children:ReactNode}) {
  const workspace=useMemo(()=>createWorkspace(`conversations:views:${storageKey}`),[storageKey]);
  useSyncExternalStore(workspace.subscribe,workspace.snapshot,workspace.snapshot);
  const {t}=useConversationLocalization();
  const root=useRef<HTMLDivElement>(null),target=useRef<HTMLDivElement>(null),parking=useRef<HTMLDivElement>(null);
  const [box,setBox]=useState({width:0,height:0});const [width,setWidth]=useState(420);
  const [minimized,setMinimized]=useState(false),[expanded,setExpanded]=useState(false);
  const [position,setPosition]=useState<{left:number;top:number}|null>(null);
  const [size,setSize]=useState({width:0,height:0});
  const panel=useRef<HTMLElement>(null);
  const current=workspace.current();
  const floating=Boolean(current&&(current.mode==="floating"||box.width<960));
  const actualWidth=Math.min(width,Math.max(280,box.width-520));
  const floatingWidth=Math.min(420,Math.max(280,box.width-24));
  // CSS viewport clamps apply immediately, before resize observers run.
  const left=position?`clamp(8px, ${position.left}px, max(8px, calc(100vw - ${floatingWidth}px - 8px)))`:undefined;
  const top=position?`clamp(8px, ${position.top}px, max(8px, calc(100dvh - min(65vh, 560px) - 8px)))`:undefined;
  useEffect(()=>{
    if(!root.current)return;
    const measure=()=>{if(root.current){const {width,height}=root.current.getBoundingClientRect();setBox({width,height})}};measure();
    if(typeof ResizeObserver==="undefined"){window.addEventListener("resize",measure);return()=>window.removeEventListener("resize",measure)}
    const observer=new ResizeObserver(entries=>{const {width,height}=entries[0].contentRect;setBox({width,height})});observer.observe(root.current);return()=>observer.disconnect();
  },[]);
  useEffect(()=>{const invalidate=()=>workspace.invalidate();window.addEventListener("apteva:apps-changed",invalidate);return()=>window.removeEventListener("apteva:apps-changed",invalidate)},[workspace]);
  useLayoutEffect(()=>{
    if(!target.current)return;
    if(typeof ResizeObserver==="undefined"){const {width,height}=target.current.getBoundingClientRect();setSize({width,height});return;}
    const observer=new ResizeObserver(entries=>{const {width,height}=entries[0].contentRect;setSize({width,height})});observer.observe(target.current);return()=>observer.disconnect();
  },[Boolean(current),floating]);
  useEffect(()=>{setMinimized(false);setExpanded(false);setPosition(null)},[current?.view.key]);
  useEffect(()=>{
    const clamp=()=>setPosition(value=>value?{left:Math.max(8,Math.min(value.left,innerWidth-(panel.current?.offsetWidth??280)-8)),top:Math.max(8,Math.min(value.top,innerHeight-(panel.current?.offsetHeight??60)-8))}:value);
    window.addEventListener("resize",clamp);return()=>window.removeEventListener("resize",clamp);
  },[]);
  useLayoutEffect(()=>{
    workspace.setParking(parking.current);
    for(const view of workspace.views.values()){
      const owner=workspace.primary(view.key);const anchor=owner?view.owners.get(owner)?.anchor:undefined;
      const destination=current?.view.key===view.key?target.current:view.pinned?parking.current:anchor??parking.current;
      moveNode(view.node,destination);
    }
  });
  const close=()=>{const key=current?.view.key;workspace.close();if(key){const owner=workspace.primary(key);workspace.views.get(key)?.owners.get(owner??"")?.anchor.parentElement?.querySelector<HTMLButtonElement>("button")?.focus()}};
  const drag=useRef<{x:number;y:number;left:number;top:number}|null>(null);
  const pinned=[...workspace.views.values()].filter(view=>view.scope===workspace.scope()&&view.pinned);
  return <Context.Provider value={workspace}><div ref={root} className="flex h-full min-h-0 min-w-0 w-full" style={{position:"relative",height:"100%",maxHeight:"100%",minHeight:0,overflow:"hidden"}}>
    <div className="min-h-0 min-w-0 flex-1" style={{height:"100%",minHeight:0,overflow:"hidden"}}>{children}</div>
    <div ref={parking} style={{display:"none"}}/>
    {current&&<aside ref={panel} aria-label={t("view.workspace")} data-card-workspace={floating?"floating":"side-panel"}
      className="flex min-w-0 flex-col overflow-hidden rounded-lg border border-border bg-bg-card text-text shadow-lg"
      style={floating?{position:"fixed",zIndex:110,...(expanded?{inset:8,width:"auto",height:"auto"}:{...(position?{left,top}:{right:16,bottom:16}),width:floatingWidth,height:minimized?"auto":"min(65vh, 560px)",maxWidth:left?`calc(100vw - ${left} - 8px)`:"calc(100vw - 16px)",maxHeight:top?`calc(100dvh - ${top} - 8px)`:"calc(100dvh - 16px)",resize:minimized?"none":"both"})}:{width:actualWidth,height:"100%",maxHeight:"100%",flexShrink:0,marginLeft:12,position:"relative"}}>
      {!floating&&<div role="separator" tabIndex={0} aria-label={t("view.resize")} aria-orientation="vertical" aria-valuenow={Math.round(actualWidth)} className="absolute bottom-0 left-0 top-0 w-2 cursor-col-resize" onKeyDown={event=>{if(event.key==="ArrowLeft"||event.key==="ArrowRight"){event.preventDefault();setWidth(value=>Math.max(280,Math.min(box.width-520,value+(event.key==="ArrowLeft"?24:-24))))}}}
        onPointerDown={event=>{event.currentTarget.setPointerCapture(event.pointerId);drag.current={x:event.clientX,y:0,left:width,top:0}}} onPointerMove={event=>{if(drag.current&&event.currentTarget.hasPointerCapture(event.pointerId))setWidth(Math.max(280,Math.min(box.width-520,drag.current.left+drag.current.x-event.clientX)))}} onPointerUp={()=>{drag.current=null}}/>}
      <header className="flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-3 py-2" style={{cursor:floating&&!expanded?"move":undefined,touchAction:"none"}}
        onPointerDown={event=>{if(!floating||expanded||(event.target as HTMLElement).closest("button,select"))return;const rect=panel.current!.getBoundingClientRect();drag.current={x:event.clientX,y:event.clientY,left:rect.left,top:rect.top};event.currentTarget.setPointerCapture(event.pointerId)}}
        onPointerMove={event=>{if(!drag.current||!event.currentTarget.hasPointerCapture(event.pointerId))return;setPosition({left:Math.max(8,Math.min(innerWidth-(panel.current?.offsetWidth??280)-8,drag.current.left+event.clientX-drag.current.x)),top:Math.max(8,Math.min(innerHeight-(panel.current?.offsetHeight??60)-8,drag.current.top+event.clientY-drag.current.y))})}} onPointerUp={()=>{drag.current=null}}>
        {pinned.length>1?<select aria-label={t("view.switch")} value={current.view.key} onChange={event=>workspace.open(event.target.value,current.mode)} className="min-w-0 flex-1 rounded border border-border bg-bg-input p-1 text-sm">{pinned.map(view=><option key={view.key} value={view.key}>{view.label}</option>)}</select>:<span className="min-w-0 flex-1 truncate text-sm font-semibold">{current.view.label}</span>}
        {(!floating||box.width>=960)&&<button type="button" className="text-xs" onClick={()=>workspace.open(current.view.key,floating?"side-panel":"floating")}>{t(floating?"view.dock":"view.float")}</button>}
        {floating&&<><button type="button" className="text-xs" onClick={()=>{setExpanded(value=>!value);setMinimized(false)}}>{t(expanded?"view.restore":"view.expand")}</button><button type="button" className="text-xs" onClick={()=>setMinimized(value=>!value)}>{t(minimized?"view.show":"view.minimize")}</button></>}
        <button type="button" className="text-xs" onClick={close}>{t("view.return")}</button>
      </header>
      <div ref={target} className="min-h-0 min-w-0 flex-1 overflow-auto p-2" style={{display:minimized?"none":undefined}}/>
    </aside>}
    {[...workspace.views.values()].map(view=>createPortal(<Presentation.Provider value={{mode:current?.view.key===view.key?(floating?"floating":"side-panel"):"inline",width:current?.view.key===view.key?size.width:0,height:current?.view.key===view.key?size.height:0,active:!view.pinned||(current?.view.key===view.key&&!minimized)}}>{view.content}</Presentation.Provider>,view.node,view.key))}
  </div></Context.Provider>;
}

export function useCardConversation(project:string,conversation:string,retain=false) {
  const workspace=useContext(Context);
  useLayoutEffect(()=>{if(!workspace||!conversation)return;const scope=JSON.stringify([project,conversation]);workspace.activate(scope);return()=>{if(!retain&&workspace.scope()===scope)workspace.activate("")}},[workspace,project,conversation,retain]);
}

/** A stable portal container moves between placements without remounting the
 * producer component, iframe or subscriptions. Repeated references share it. */
export function PersistentAppCard({viewKey,projectId,conversationId,owner,label,order,children}:{viewKey:string;projectId:string;conversationId:string;owner:string;label:string;order:number;children:ReactNode}) {
  const workspace=useContext(Context),anchor=useRef<HTMLDivElement>(null);const {t}=useConversationLocalization();
  useSyncExternalStore(workspace?.subscribe??(()=>()=>{}),workspace?.snapshot??(()=>0),()=>0);
  const scope=JSON.stringify([projectId,conversationId]);
  useLayoutEffect(()=>{if(workspace&&anchor.current)workspace.register(viewKey,scope,label,owner,anchor.current,children,order)},[workspace,viewKey,scope,label,owner,children,order]);
  useLayoutEffect(()=>()=>workspace?.unregister(viewKey,owner),[workspace,viewKey,owner]);
  if(!workspace)return <>{children}</>;
  const primary=workspace.primary(viewKey)===owner;
  const pinned=workspace.views.get(viewKey)?.pinned;
  return <div className="min-w-0" data-card-reference={viewKey}>
    <div className="mb-1 flex flex-wrap gap-3 text-xs text-text-muted">
      <button type="button" onClick={()=>workspace.open(viewKey,"side-panel")}>{t(pinned||!primary?"view.open":"view.keep")}</button>
      <button type="button" onClick={()=>workspace.open(viewKey,"floating")}>{t("view.float")}</button>
    </div>
    {(pinned||!primary)&&<p className="text-xs text-text-muted">{label} · {t(pinned?"view.kept":"view.reused")}</p>}
    <div ref={anchor} style={{display:pinned||!primary?"none":undefined}}/>
  </div>;
}

function stable(value:unknown):string {if(Array.isArray(value))return `[${value.map(stable).join(",")}]`;if(value&&typeof value==="object")return `{${Object.entries(value).sort(([a],[b])=>a.localeCompare(b)).map(([key,v])=>`${JSON.stringify(key)}:${stable(v)}`).join(",")}}`;return JSON.stringify(value)??"null"}
/** SHA-256 also keeps arbitrary legacy props out of preference storage. */
export async function appViewKey(project:string,conversation:string,install:number,app:string,name:string,resource:unknown,props:Record<string,unknown>) {
  const bytes=new TextEncoder().encode(stable([project,conversation,install,app,resource??[name,props]]));
  const hash=await crypto.subtle.digest("SHA-256",bytes);
  return Array.from(new Uint8Array(hash),byte=>byte.toString(16).padStart(2,"0")).join("");
}
