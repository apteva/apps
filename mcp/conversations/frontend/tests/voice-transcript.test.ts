import { expect,test } from "bun:test";
import { voiceTranscriptMessages } from "../src/voiceTranscript";
import type { Message } from "../src/types";
const voice=(id:number,text:string,start:number,end:number,role:Message["role"]="agent",session="one"):Message=>({id,conversation_id:"chat",role,agent_id:41,content:text,components:[],created_at:new Date(100000+start).toISOString(),metadata:{source:"voice",voice_session_id:session,transcript:{id:`segment-${id}`,generation:1,start_ms:start,end_ms:end,status:"application_grouped"}}});
test("voice fragments form a readable turn without mutating durable segment identities",()=>{
 const first=voice(1,"I'm doing",20000,20200),last=voice(2,"good. What's on your mind?",20200,21400);
 expect(voiceTranscriptMessages([last,first]).map(m=>m.content)).toEqual(["I'm doing good. What's on your mind?"]);
 expect(first.content).toBe("I'm doing");expect(last.id).toBe(2);
 const updated={...last,content:"great.",revision:9};
 expect(voiceTranscriptMessages([first,updated])).toMatchObject([{id:1,content:"I'm doing great.",revision:9}]);
});
test("different speakers, typed messages, cards, sessions, generations and long gaps stay separate",()=>{
 const first=voice(1,"Hello",100,200),second=voice(2,"again",200,300);
 const typed={...second,id:3,role:"user" as const,content:"Typed interruption",metadata:undefined,created_at:new Date(100201).toISOString()};
 expect(voiceTranscriptMessages([first,typed,{...second,created_at:new Date(100202).toISOString()}])).toHaveLength(3);
 for(const changed of [voice(2,"again",200,300,"user"),voice(2,"again",200,300,"agent","two"),voice(2,"again",4000,4100),{...second,components:[{app:"conversations",name:"approval-card",props:{}}]},{...second,metadata:{...second.metadata,transcript:{...(second.metadata!.transcript as object),generation:2}}}])expect(voiceTranscriptMessages([first,changed])).toHaveLength(2);
});
test("blocked transcripts are absent and legacy rows are never guessed away",()=>{
 const blocked=voice(2,"sensitive text",200,300);(blocked.metadata!.transcript as any).status="blocked";
 expect(voiceTranscriptMessages([voice(1,"Hi",100,200),blocked,voice(3,"again",300,400)]).map(m=>m.content)).toEqual(["Hi","again"]);
 const legacy={...voice(1,"Repeated words",100,200),metadata:{source:"voice"}};
 expect(voiceTranscriptMessages([legacy,{...legacy,id:2}])).toHaveLength(2);
});

test("captured crêpe interruption: timed evidence splits an already checkpointed reply around delayed caller captions",()=>{
 const recipe=voice(1674,"Tu mélanges de la farine, des œufs, du lait, Oui, je t'écoute.",30600,35000);
 const interruption=voice(1682,"Non attends, attends, arrête",33800,34600,"user");
 (recipe.metadata!.transcript as any).fragments=[
  {text:"Tu mélanges de la farine, des œufs, du lait,",start_ms:30600,end_ms:33200},
  {text:" Oui,",start_ms:34400,end_ms:34600},{text:" je t'écoute.",start_ms:34600,end_ms:35000}];
 const rows=voiceTranscriptMessages([recipe,interruption]);
 expect(rows.map(m=>m.content.trim())).toEqual(["Tu mélanges de la farine, des œufs, du lait,","Non attends, attends, arrête","Oui, je t'écoute."]);
 expect(new Set(rows.map(m=>m.presentation_key??m.id)).size).toBe(3);
 expect(recipe.content).toContain("lait, Oui");
 const again=voice(1690,"D’accord. Donc, tu mélanges farine, œufs, lait",36000,39000);
 const stop=voice(1691,"Ouais non c'est bon, arrête, arrête",39200,40200,"user");
 const reply=voice(1692,"Ok, je m'arrête.",40400,41000);
 expect(voiceTranscriptMessages([recipe,interruption,voice(1689,"Continue la recette",35200,35600,"user"),again,stop,reply]).slice(-3).map(m=>m.content)).toEqual([again.content,stop.content,reply.content]);
});

test("fragment text is preserved exactly across syllables and malformed evidence falls back safely",()=>{
 const m=voice(1,"Bonjour.",100,400);
 (m.metadata!.transcript as any).fragments=[{text:"Bon",start_ms:100,end_ms:200},{text:"jour.",start_ms:200,end_ms:400}];
 expect(voiceTranscriptMessages([m]).map(m=>m.content)).toEqual(["Bonjour."]);
 (m.metadata!.transcript as any).fragments[1]=null;
 expect(voiceTranscriptMessages([m]).map(m=>m.content)).toEqual(["Bonjour."]);
 (m.metadata!.transcript as any).fragments[1]={text:"different",start_ms:200,end_ms:400};
 expect(voiceTranscriptMessages([m]).map(m=>m.content)).toEqual(["Bonjour."]);
});
