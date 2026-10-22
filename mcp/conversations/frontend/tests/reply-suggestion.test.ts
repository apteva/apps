import {test,expect} from "bun:test";
import {latestReplySuggestion} from "../src/replySuggestion";
import type {Message} from "../src/types";
const message=(id=2):Message=>({id,conversation_id:"chat",agent_id:41,role:"agent",phase:"final",content:"Ready.",created_at:"",components:[{id:"next",version:1,app:"conversations",name:"reply-suggestions",props:{items:[{id:"next",text:"Run the dry-run validation next."}]}}]});
test("only the latest final in the intended conversation offers one suggestion",()=>{
 const m=message();expect(latestReplySuggestion([m],"chat")?.text).toBe("Run the dry-run validation next.");expect(latestReplySuggestion([m],"other")).toBeNull();
 for(const newer of [{...message(3),role:"user" as const,queue_state:"queued"},{...message(3),role:"user" as const,queue_state:"cancelled"},{...message(3),components:[]},{...message(3),phase:"progress"}])expect(latestReplySuggestion([newer,m],"chat")).toBeNull();
});
test("unknown versions, multiple choices, interactions and empty suggestions never become ghost text",()=>{
 for(const change of [(m:Message)=>{m.components[0].version=2},(m:Message)=>{m.phase="progress"},(m:Message)=>{m.components[0].props.items=[{text:"One"},{text:"Two"}]},(m:Message)=>{m.components[0].props.items=[{text:" "}]},(m:Message)=>{m.components.push(m.components[0])}]) {const m=message();change(m);expect(latestReplySuggestion([m],"chat")).toBeNull()}
});
