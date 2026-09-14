import { expect, test } from "bun:test";
import { pendingResponsePhase, responseToolGroup } from "../src/responseActivity";
import { toolDurationMs, toolGroupDurationMs, type ToolActivity } from "../src/toolActivityModel";

const base: ToolActivity = { id:"1", callId:"c1", agentId:41, threadId:"chat-1", name:"repos", reason:"Checking repositories", state:"done", startedAt:1000, finishedAt:2500, durationMs:1500 };
test("tool activity owns progress while a response is calling tools", () => {
  const bubble = { agentId:41, threadId:"chat-1", afterMessageId:7, createdAt:1000 };
  const user = { id:7, conversation_id:"chat-1", role:"user" as const, content:"hi", components:[], created_at:new Date(1000).toISOString() };
  expect(pendingResponsePhase(bubble, [{...base, status:"running", agent_id:41, thread_id:"chat-1", started_at:new Date(1000).toISOString()} as any], [user])).toBeNull();
  expect(pendingResponsePhase(bubble, [{status:"completed", agent_id:41, thread_id:"chat-1", started_at:new Date(1500).toISOString()} as any], [user])).toBe("preparing");
});
test("tool durations use provider duration and group parallel intervals once", () => {
  expect(toolDurationMs(base, 9999)).toBe(1500);
  const second = {...base, id:"2", startedAt:1800, finishedAt:2800, durationMs:1000};
  expect(toolGroupDurationMs([base, second], 9999)).toBe(1800);
});

test("fast call/result bursts paint running first without blocking parallel starts", async () => {
 const {splitActivityPaint}=await import("../src/toolActivityPaint");
 const a={id:1,status:"running",revision:1} as any;
 const result={...a,status:"completed",revision:2};
 const b={...a,id:2};
 const first=splitActivityPaint([a,result,b]);
 expect(first.paint).toEqual([a,b]);expect(first.deferred).toEqual([result]);
 expect(splitActivityPaint(first.deferred).paint).toEqual([result]);
});

test("tool progress belongs to the response, not the last transcript item",()=>{
 const user={id:7,role:"user",created_at:new Date(900).toISOString()} as any;
 const timeline=[{kind:"toolGroup",key:"group",tools:[base]}, {kind:"message",key:"reply",message:{id:8,role:"agent"}}] as any;
 const response={agentId:41,threadId:"chat-1",afterMessageId:7,createdAt:1200};
 expect(responseToolGroup(response,timeline,[user])).toBe("group");
 expect(responseToolGroup({...response,agentId:42},timeline,[user])).toBeUndefined();
 expect(responseToolGroup({...response,threadId:"other"},timeline,[user])).toBeUndefined();
 expect(responseToolGroup({...response,afterMessageId:8,createdAt:3000},timeline,[user])).toBeUndefined();
 expect(responseToolGroup({...response,optimistic:true,createdAt:3000},timeline,[user])).toBeUndefined();
});
