import {afterEach,expect,test} from "bun:test";
import {Window} from "happy-dom";
import {act} from "react";
import {createRoot,type Root} from "react-dom/client";
import Trading from "./TradingPanel";
import Strategies from "../../strategies/ui/StrategiesPanel";
const originalFetch=globalThis.fetch;
const window=new Window({url:"http://localhost"});
Object.assign(globalThis,{window,document:window.document,HTMLElement:window.HTMLElement,IS_REACT_ACT_ENVIRONMENT:true,EventSource:class {close(){}}});
let root:Root;
afterEach(async()=>{await act(async()=>root?.unmount());document.body.innerHTML="";globalThis.fetch=originalFetch});
for(const [name,Component,expected]of [["Trading",Trading,["portfolios","trade","positions","risk","agents","brokers","journal"]],["Strategies",Strategies,["strategies","backtests"]]]as const){
 test(`${name} exposes only its own work areas`,async()=>{
  globalThis.fetch=(async()=>new Response(JSON.stringify({portfolios:[],strategies:[]}),{headers:{"Content-Type":"application/json"}}))as typeof fetch;
  const node=document.createElement("div");document.body.append(node);root=createRoot(node);
  await act(async()=>{root.render(<Component projectId="test" installId={10}/>);await new Promise(resolve=>setTimeout(resolve,0))});
  expect(Array.from(node.querySelectorAll("nav button")).map(button=>button.textContent)).toEqual([...expected]);
 });
}
