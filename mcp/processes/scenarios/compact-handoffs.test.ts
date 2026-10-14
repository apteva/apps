import { expect,test } from "bun:test";
import { createHash } from "node:crypto";
import { verifyCompactHandoffs } from "./compact-handoffs";
import { CONTINUITY_APPROVAL } from "./executor-continuity";
function fixture(){
 const keys=["inventory","portrait_3","portrait_4","validate","approve","publish"];
 const deps=[[],["inventory"],["portrait_3"],["portrait_3","portrait_4"],["validate"],["approve"]];
 const receipt=(key:string):any=>({context_id:"ctx",source_id:"source:9007199254740993",...(key.startsWith("portrait_")?{artifact_id:key.replace("_","-")+".png",digest:"digest-"+key}:{}),...(key==="validate"?{validated:["portrait-3.png","portrait-4.png"]}:{}),...(key==="publish"?{receipt:"accepted",artifact_ids:["portrait-3.png","portrait-4.png"]}:{})});
 const steps=keys.map((key,i)=>({id:key,key,state:"completed",executor:key==="approve"?{kind:"human"}:{kind:"agent",agent_id:7},updated_by:key==="approve"?"operator":"agent:7:worker",target_thread_id:key==="approve"?"":"worker",execution_id:key==="approve"?"":`exec-${key}`,definition:{depends_on:deps[i]},events:[{id:i*3+1,state:"ready"},{id:i*3+2,state:"completed"}],output:key==="approve"?CONTINUITY_APPROVAL:JSON.stringify({outcome:"Done",evidence_refs:["ev-"+key],...(key.startsWith("portrait_")?{artifact_ids:[key.replace("_","-")+".png"]}:{}),...(["validate","publish"].includes(key)?{artifact_ids:["portrait-3.png","portrait-4.png"]}:{})})}));
 const agent=steps.filter(s=>s.key!=="approve");
 const evidence=agent.map(s=>{const content=JSON.stringify(receipt(s.key));return {id:"ev-"+s.key,step_id:s.id,run_id:"run",evidence_key:s.key,artifact_id:s.key.startsWith("portrait_")?s.key.replace("_","-")+".png":"",content,sha256:createHash("sha256").update(content).digest("hex"),author:"agent:7:worker"}});
 const call=(name:string,args:any={},result?:any)=>({name,args,ok:true,completed:true,thread_id:"worker",result:JSON.stringify(result),result_original_bytes:500,result_truncated:false});
 const calls:any[]=[];
 agent.forEach((s,i)=>{
  calls.push(call("processes_step_claim",{step_id:s.id},{dependencies:Object.fromEntries(s.definition.depends_on.map(key=>[key,{output:steps.find(s=>s.key===key)?.output}])),...(i===0?{context_values:{hash:"policy"}}:{})}));
  calls.push(call("processes_step_read",{step_id:s.id,target:"instructions"}));
  if(s.key==="portrait_4")calls.push(call("processes_step_read",{step_id:s.id,target:"dependency",dependency_key:"inventory"}));
  if(s.key==="publish"){
   calls.push(call("processes_step_read",{step_id:s.id,target:"shared",field:"approval_requirements"}));
   calls.push(call("processes_step_read",{step_id:s.id,target:"evidence",artifact_id:"portrait-3.png"}));
  }
  calls.push(call("test-continuity_"+["prepare","render","render","validate","publish"][i]));
  calls.push(call("processes_step_evidence_save",{step_id:s.id,key:s.key}));
  calls.push(call("processes_step_update",{step_id:s.id,state:"completed",handoff:JSON.parse(s.output)},{done:i===4}));
 });
 const runs=[{id:"run",state:"completed",assignment:{worker_continuity:"per_executor"},steps}];
 const workers=[{run_id:"run",thread_id:"worker",agent_id:7}];
 const fixture={contexts:[{id:"ctx",source_id:"source:9007199254740993",thread_id:"worker"}],operations:agent.map((s,i)=>({name:["prepare","render","render","validate","publish"][i],receipt_json:JSON.stringify(receipt(s.key))}))};
 const approval:any={step:{id:"approve",run_id:"run"},output:CONTINUITY_APPROVAL};
 return {calls,runs,workers,evidence,fixture,approval};
}
const verify=(f:ReturnType<typeof fixture>)=>verifyCompactHandoffs(f.calls,f.runs,f.workers,f.evidence,f.fixture,f.approval);
test("accepts exact compact handoffs with targeted recovery and independent approval",()=>expect(()=>verify(fixture())).not.toThrow());
const mutations:Record<string,(f:ReturnType<typeof fixture>)=>void>={
 "missing evidence":f=>{f.evidence.pop()},
 "wrong source":f=>{f.evidence[1].content=f.evidence[1].content.replace("9007199254740993","9007199254740992")},
 "oversized handoff":f=>{f.runs[0].steps[0].output=JSON.stringify({outcome:"x".repeat(5000)})},
 "repeated render":f=>{f.fixture.operations.push(f.fixture.operations[1])},
 "worker context lost":f=>{f.workers.push(f.workers[0])},
 "premature done":f=>{f.calls.find(c=>c.name==="processes_step_update").result='{"done":true}'},
 "missing current instructions":f=>{f.calls=f.calls.filter(c=>!(c.name==="processes_step_read"&&c.args.target==="instructions"&&c.args.step_id==="publish"))},
 "missing policy recovery":f=>{f.calls=f.calls.filter(c=>c.args.field!=="approval_requirements")},
 "missing ancestor recovery":f=>{f.calls=f.calls.filter(c=>c.args.dependency_key!=="inventory")},
 "missing artifact recovery":f=>{f.calls=f.calls.filter(c=>c.args.artifact_id!=="portrait-3.png")},
 "approval bypass":f=>{f.runs[0].steps[4].updated_by="agent:7:worker"},
 "early publication":f=>{f.runs[0].steps[5].events[0].id=1},
 "copied ancestors":f=>{const claim=f.calls.find(c=>c.name==="processes_step_claim"&&c.args.step_id==="publish");claim.result=JSON.stringify({dependencies:{approve:{},inventory:{}}})},
 "unbounded claim":f=>{f.calls[0].result_original_bytes=100000},
 "whole-run reconstruction":f=>{f.calls.push({name:"processes_run_evidence"})},
};
for(const [name,mutate] of Object.entries(mutations))test(`rejects ${name}`,()=>{const f=fixture();mutate(f);expect(()=>verify(f)).toThrow()});
