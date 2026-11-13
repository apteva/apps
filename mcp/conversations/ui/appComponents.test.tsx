import "./testDom";
import {afterEach, beforeEach, expect, test} from "bun:test";
import {Window} from "happy-dom";
import {act} from "react";
import {createRoot, type Root} from "react-dom/client";
import {AptevaClient} from "@apteva/web-sdk";
import {conversationsExtension} from "../frontend/src/client";
import {ConversationsProvider} from "../frontend/src/context";
import {MessageComponents, ConversationComponentProvider} from "../frontend/src/messageComponents";
import {InstalledAppComponentProvider, resolveAppCard, type AppComponentLoader, type InstalledComponentApp} from "../frontend/src/appComponents";
import type {Message} from "../frontend/src/types";

const app = (name="computer", project_id=""): InstalledComponentApp => ({name, install_id:19, project_id, version:"1.2.3", status:"running", ui_components:[{name:"preview",entry:"/ui/PreviewCard.mjs",slots:["chat.message_attachment"]}]});
const fixture = (name="computer"): Message => ({id:1, conversation_id:"chat", role:"agent", content:"Result", created_at:"",components:[{app:name,name:"preview",props:{title:"Saved result",projectId:"forged",installId:999}}]});
let win:Window, root:Root, element:HTMLElement;
beforeEach(()=>{
  win=new Window({url:"http://localhost"});
  Object.assign(globalThis,{window:win,document:win.document,navigator:win.navigator,HTMLElement:win.HTMLElement,IS_REACT_ACT_ENVIRONMENT:true});
  element=win.document.createElement("div") as any;win.document.body.append(element as any);root=createRoot(element);
});
afterEach(async()=>{await act(async()=>root.unmount());await win.happyDOM.abort()});
const conversations=new AptevaClient({baseURL:"http://localhost"}).use(conversationsExtension(),{projectId:"project",installId:7});
const flush=async()=>{await act(async()=>{await new Promise(resolve=>setTimeout(resolve,5))})};
async function mount(loader:AppComponentLoader,messages=[fixture()],projectId="project") {
  await act(async()=>root.render(<ConversationsProvider conversations={conversations}><InstalledAppComponentProvider loader={loader}>{messages.map(message=><MessageComponents key={message.id} message={message} projectId={projectId}/>)}</InstalledAppComponentProvider></ConversationsProvider>));
  await flush();
}

test("declared cards share the catalog/module and receive trusted scope",async()=>{
  const catalogs:string[]=[],urls:string[]=[];
  const loader:AppComponentLoader={catalog:async project=>{catalogs.push(project);return [app()]},loadModule:async url=>{urls.push(url);return {default:props=><p>{props.title}: {props.projectId}/{props.installId}</p>}}};
  await mount(loader,[fixture(),{...fixture(),id:2}]);
  expect(catalogs).toEqual(["project"]);expect(urls).toHaveLength(1);
  expect(urls[0]).toBe("/api/apps/computer/ui/PreviewCard.mjs?v=1.2.3&install_id=19&project_id=project");
  expect(element.textContent).toContain("Saved result: project/19");expect(element.querySelector("details")).toBeNull();
});
test("a different app uses the same provider; global Inbox passes the item's project",async()=>{
  const catalogs:string[]=[];
  const loader:AppComponentLoader={catalog:async project=>{catalogs.push(project);return [app("tasks","other")]},loadModule:async()=>({default:props=><p>Task in {props.projectId}</p>})};
  await mount(loader,[fixture("tasks")],"other");expect(catalogs).toEqual(["other"]);expect(element.textContent).toContain("Task in other");
});
test("failed imports and render errors can retry without exposing props",async()=>{
  let attempts=0,crash=true;
  const urls:string[]=[];
  const loader:AppComponentLoader={catalog:async()=>[app()],loadModule:async url=>{urls.push(url);if(++attempts===1)throw Error("offline");return {default:()=>{if(crash)throw Error("card bug");return <p>Recovered</p>}}}};
  await mount(loader);expect(element.textContent).toContain("This preview is unavailable");expect(element.textContent).not.toContain("forged");
  await act(async()=>element.querySelector("button")!.click());await flush();expect(element.textContent).toContain("This preview is unavailable");
  crash=false;await act(async()=>element.querySelector("button")!.click());await flush();expect(element.textContent).toContain("Recovered");expect(urls[1]).toContain("retry=1");
});
test("catalog refresh upgrades mounted cards and removes revoked installations",async()=>{
  let rows=[app()];const urls:string[]=[];
  const loader:AppComponentLoader={catalog:async()=>rows,loadModule:async url=>{urls.push(url);return {default:()=> <p>{url.includes("v=2.0.0")?"New version":"Old version"}</p>}}};
  await mount(loader);expect(element.textContent).toContain("Old version");
  rows=[{...app(),version:"2.0.0"}];await act(async()=>window.dispatchEvent(new Event("apteva:apps-changed")));await flush();expect(element.textContent).toContain("New version");
  rows=[];await act(async()=>window.dispatchEvent(new Event("apteva:apps-changed")));await flush();expect(element.textContent).toContain("This preview is unavailable");expect(element.textContent).not.toContain("New version");
});
test("local renderer registration overrides automatic imports",async()=>{
  const loader:AppComponentLoader={catalog:async()=>{throw Error("should not load")},loadModule:async()=>{throw Error("should not load")}};
  await act(async()=>root.render(<ConversationsProvider conversations={conversations}><InstalledAppComponentProvider loader={loader}><ConversationComponentProvider renderers={{"computer/preview":()=> <p>Local export</p>}}><MessageComponents message={fixture()}/></ConversationComponentProvider></InstalledAppComponentProvider></ConversationsProvider>));
  expect(element.textContent).toContain("Local export");
});
test("scope and slot checks reject undeclared, inaccessible and ambiguous cards",()=>{
  const base=app();
  for(const rows of [[],[app("computer","foreign")],[base,base],[{...base,status:"disabled"}],[{...base,ui_components:[]}],[{...base,ui_components:[{...base.ui_components![0],slots:["dashboard.home"]}]}]])expect(()=>resolveAppCard(rows,"computer","preview","project")).toThrow();
  expect(resolveAppCard([base,app("computer","project")],"computer","preview","project").app.project_id).toBe("project");
  expect(()=>resolveAppCard([base],"computer","preview","")).toThrow();
  for(const entry of ["https://evil.test/Card.mjs","//evil.test/Card.mjs","/ui/../private/Card.mjs","/ui/%2e%2e/Card.mjs","/ui/Card.mjs?entry=bad","/private/Card.mjs","/ui/Card.mjs#fragment"]){
    expect(()=>resolveAppCard([{...base,ui_components:[{...base.ui_components![0],entry}]}],"computer","preview","project")).toThrow();
  }
});
test("integration cards use the existing integration module route",()=>{
 const integration={...app("example","project"),source:"integration",install_id:0};
 expect(resolveAppCard([integration],"example","preview","project").url).toBe("/api/integrations/example/ui/PreviewCard.mjs?v=1.2.3");
});
