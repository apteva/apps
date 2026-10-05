import { expect, test } from "bun:test";
import { CONTINUITY_APPROVAL, verifyExecutorContinuity } from "./executor-continuity";

function fixture() {
  const thread = "process-run-r-agent-7-worker", source_id = "source-real", context_id = "context-one";
  const keys = ["inventory", "portrait_3", "portrait_4", "validate", "approve", "publish"];
  const deps = [[], ["inventory"], ["inventory"], ["inventory", "portrait_3", "portrait_4"], ["validate"], ["approve"]];
  const events = [[1,3],[4,7],[5,9],[10,12],[13,14],[15,17]];
  const outputs = [
    {source_id,context_id}, {source_id,context_id,artifact_id:"portrait-3.png",digest:"digest-3"},
    {source_id,context_id,artifact_id:"portrait-4.png",digest:"digest-4"},
    {source_id,context_id,validated:["portrait-3.png","portrait-4.png"]}, null,
    {source_id,context_id,receipt:"accepted",artifact_ids:["portrait-3.png","portrait-4.png"]},
  ];
  const run = {id:"r",process_id:"p",workflow:true,state:"completed",assignment:{owner_agent_id:7,worker_continuity:"per_executor"},steps:keys.map((key,i) => ({
    id:key,key,state:"completed",definition:{depends_on:deps[i]},
    executor:i===4?{kind:"human"}:{kind:"agent",agent_id:7},
    updated_by:i===4?"operator":`agent:7:${thread}`,target_thread_id:i===4?"":thread,
    output:i===4?CONTINUITY_APPROVAL:JSON.stringify(outputs[i]),execution_id:`exec-${i}`,delivered_at:"today",
    events:[{id:events[i][0],state:i===4?"waiting":"ready"},{id:events[i][1],state:"completed"}],
  }))};
  const calls:any[]=[];
  const operations:any[]=[];
  const names=["prepare","render","render","validate","","publish"];
  const call = (name:string,args:Record<string,unknown>={}) => calls.push({name,args,ok:true,completed:true,thread_id:thread});
  for (let i=0;i<keys.length;i++) {
    if(i===4) continue;
    call("processes_step_claim",{step_id:keys[i], ...(i === 0 ? {} : {include_context:"false"})});
    if(i===0) call("tool_search",{query:"test-continuity tools"});
    call(`test-continuity_${names[i]}`,{context_id});
    operations.push({context_id,name:names[i],artifact_id:i===1?"portrait-3.png":i===2?"portrait-4.png":"",digest:i===1?"digest-3":i===2?"digest-4":"",receipt_json:JSON.stringify(outputs[i]),tool_call_id:`call-${i}`});
    call("processes_step_update",{step_id:keys[i],state:"completed"});
    const ack = JSON.stringify({step_id:keys[i],run_id:"r",state:"completed",revision:3,progress:100,next_action:"Follow saved state",done:i===5,reread:{tool:"processes_step_get",args:{process_id:"p",run_id:"r",step_id:keys[i],include_context:true}}});
    Object.assign(calls.at(-1), {result:ack,result_original_bytes:ack.length,result_truncated:false});
  }
  call("done", { message: "all assigned steps complete" });
  calls.push({name:"processes_runs",args:{},ok:true,completed:true,thread_id:"main"});
  return {calls,runs:[run],workers:[{run_id:"r",agent_id:7,thread_id:thread}],domain:{contexts:[{id:context_id,source_id,agent_id:7,thread_id:thread}],operations},confirmation:{step:{id:"approve",run_id:"r",process_id:"p",project_id:"proj"},port:1,confirmedAt:"today",output:CONTINUITY_APPROVAL}};
}
const verify = (f:ReturnType<typeof fixture>) => verifyExecutorContinuity(f.calls,f.runs,f.workers,f.domain,f.confirmation);
test("accepts one real prepared context, worker and exact receipts across branching, join and human gate", () => expect(() => verify(fixture())).not.toThrow());
const mutations: Record<string, (f: any) => unknown> = {
  "premature worker completion":(f:any)=>f.calls.splice(1,0,{name:"done",args:{},ok:true,completed:true,thread_id:f.workers[0].thread_id}),
  "different step worker":(f:any)=>f.runs[0].steps[2].target_thread_id="other",
  "mixed execution receipts":(f:any)=>f.runs[0].steps[2].execution_id=f.runs[0].steps[1].execution_id,
  "lost artifact identity":(f:any)=>f.runs[0].steps[2].output=JSON.stringify({source_id:"source-real",context_id:"context-one",artifact_id:"wrong.png",digest:"digest-4"}),
  "early validation":(f:any)=>f.runs[0].steps[3].events[0].id=8,
  "agent self-approval":(f:any)=>f.runs[0].steps[4].updated_by="agent:7:worker",
  "no HTTP approval":(f:any)=>f.confirmation.step.id="other",
  "repeated setup":(f:any)=>f.domain.operations.push(f.domain.operations[0]),
  "rediscovered fixture tools":(f:any)=>f.calls.splice(8,0,{name:"tool_search",args:{query:"test-continuity render"},ok:true,completed:true,thread_id:f.workers[0].thread_id}),
  "policy omitted on first claim":(f:any)=>f.calls.find((c:any)=>c.name==="processes_step_claim").args.include_context="false",
  "shared policy repeated on later claim":(f:any)=>delete f.calls.find((c:any)=>c.name==="processes_step_claim"&&c.args.step_id==="portrait_4").args.include_context,
  "shared policy explicitly requested on later claim":(f:any)=>f.calls.find((c:any)=>c.name==="processes_step_claim"&&c.args.step_id==="portrait_4").args.include_context="true",
  "oversized completion response":(f:any)=>f.calls.find((c:any)=>c.name==="processes_step_update").result_original_bytes=5000,
  "receipt echoed in acknowledgement":(f:any)=>{const c=f.calls.find((c:any)=>c.name==="processes_step_update");c.result=JSON.stringify({...JSON.parse(c.result),output:"repeated receipt"});},
  "premature done acknowledgement":(f:any)=>{const c=f.calls.find((c:any)=>c.name==="processes_step_update");c.result=JSON.stringify({...JSON.parse(c.result),done:true});},
  "wrong recovery reference":(f:any)=>{const c=f.calls.find((c:any)=>c.name==="processes_step_update");const a=JSON.parse(c.result);a.reread.args.step_id="wrong";c.result=JSON.stringify(a);},
  "missing claim":(f:any)=>f.calls=f.calls.filter((c:any)=>!(c.name==="processes_step_claim"&&c.args.step_id==="portrait_4")),
  "fabricated fixture receipt":(f:any)=>f.domain.operations[2].receipt_json=JSON.stringify({source_id:"fabricated",context_id:"context-one",artifact_id:"portrait-4.png",digest:"digest-4"}),
  "parallelism disguised as continuity":(f:any)=>f.runs[0].assignment.worker_continuity="isolated",
  "prepared context belongs to another thread":(f:any)=>f.domain.contexts[0].thread_id="other-worker",
};
for (const [name, mutate] of Object.entries(mutations)) test(`rejects ${name}`, () => { const f=fixture(); mutate(f); expect(()=>verify(f)).toThrow(); });
