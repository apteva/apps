import { createHash } from "node:crypto";
import { check } from "./verify-outcomes";
import { CONTINUITY_APPROVAL, type FixtureState } from "./executor-continuity";
import type { ConfirmationReport } from "./operator-confirm";
export const COMPACT_HANDOFFS = "processes-compact-handoffs";
const canonical = (v:any) => JSON.stringify(v,(_key,value)=>value && typeof value === "object" && !Array.isArray(value) ? Object.fromEntries(Object.keys(value).sort().map(k=>[k,value[k]])) : value);
export function verifyCompactHandoffs(calls:any[],runs:any[],workers:any[],evidence:any[],fixture:FixtureState,approval:ConfirmationReport) {
  check(runs.length===1 && runs[0].state==="completed" && workers.length===1,"Expected one completed run and one persistent worker");
  const run=runs[0],thread=workers[0].thread_id;
  check(run.assignment.worker_continuity==="per_executor" && workers[0].run_id===run.id && fixture.contexts.length===1 && fixture.contexts[0].thread_id===thread,"Lost frozen worker/prepared context");
  check(run.steps.map((s:any)=>s.key).join(",")==="inventory,portrait_3,portrait_4,validate,approve,publish","Unexpected steps");
  const byKey=Object.fromEntries(run.steps.map((s:any)=>[s.key,s]));
  check(byKey.approve.executor.kind==="human" && byKey.approve.updated_by==="operator" && byKey.approve.output===CONTINUITY_APPROVAL && approval.step.id===byKey.approve.id && approval.step.run_id===run.id && approval.output===CONTINUITY_APPROVAL,"Missing independent HTTP approval");
  check(fixture.operations.map(o=>o.name).join(",")==="prepare,render,render,validate,publish" && evidence.length===5,"Repeated/missing fixture work or diagnostics");
  const good=(c:any)=>c.ok&&c.completed;
  const domain=calls.filter(c=>c.name.startsWith("test-continuity_"));
  check(domain.length===5 && domain.every(c=>good(c)&&c.thread_id===thread),"Fixture calls escaped worker or repeated");
  const steps=run.steps.filter((s:any)=>s.key!=="approve");
  steps.forEach((step:any,i:number)=>{
    check(step.state==="completed" && step.target_thread_id===thread && step.updated_by===`agent:${workers[0].agent_id}:${thread}` && !!step.execution_id,"Lost worker/step attribution");
    check(Buffer.byteLength(step.output)<=4096,"Oversized handoff persisted");
    const handoff=JSON.parse(step.output), saved=evidence.find(e=>e.step_id===step.id);
    check(saved && saved.run_id===run.id && saved.evidence_key===step.key && saved.author===step.updated_by && handoff.evidence_refs?.includes(saved.id),"Handoff lost exact evidence identity");
    check(createHash("sha256").update(saved.content).digest("hex")===saved.sha256,"Evidence content/hash mismatch");
    const receipt=JSON.parse(saved.content),actual=JSON.parse(fixture.operations[i].receipt_json);
    check(canonical(receipt)===canonical(actual) && receipt.context_id===fixture.contexts[0].id && receipt.source_id===fixture.contexts[0].source_id,"Saved evidence differs from real artifact/source receipt");
    check(!["source_id","context_id","digest","instructions"].some(k=>k in handoff),"Handoff copied diagnostic fields");
    if(step.key.startsWith("portrait_"))check(saved.artifact_id===actual.artifact_id && handoff.artifact_ids?.includes(actual.artifact_id),"Artifact identity lost");
    if(step.key==="validate"||step.key==="publish")check(canonical(handoff.artifact_ids)==='["portrait-3.png","portrait-4.png"]',"Final handoff lost output identities");
    const claim=calls.findIndex(c=>good(c)&&c.name==="processes_step_claim"&&c.thread_id===thread&&c.args.step_id===step.id);
    const instructions=calls.findIndex((c,j)=>j>claim&&good(c)&&c.name==="processes_step_read"&&c.thread_id===thread&&c.args.step_id===step.id&&c.args.target==="instructions");
    const work=calls.indexOf(domain[i]);
    const save=calls.findIndex(c=>good(c)&&c.name==="processes_step_evidence_save"&&c.thread_id===thread&&c.args.step_id===step.id&&c.args.key===step.key);
    const finish=calls.findIndex(c=>good(c)&&c.name==="processes_step_update"&&c.thread_id===thread&&c.args.step_id===step.id&&c.args.state==="completed");
    check(claim>=0&&instructions>claim&&work>instructions&&save>work&&finish>save,"Missing targeted instruction recovery or evidence-before-completion");
    const update=calls[finish];check(update.args.handoff && !update.args.output && update.result_original_bytes<=2048,"Missing structured handoff or oversized acknowledgement");
    const ack=JSON.parse(update.result);check(ack.done===(i===steps.length-1),"Worker ended before approval/publication");
    const claimed=calls[claim];check(claimed.result_original_bytes<=16*1024,"Claim exceeded compact response budget");
    if(!claimed.result_truncated){
      const response=JSON.parse(claimed.result),keys=Object.keys(response.dependencies).sort();
      check(canonical(keys)===canonical([...step.definition.depends_on].sort()),"Claim automatically copied ancestor outputs");
      if(i===0)check(response.context_values&&Object.keys(response.context_values).length===1,"Repeated authorization was not deduplicated");
    }
  });
  check(new Set(steps.map((s:any)=>s.execution_id)).size===5,"Step execution records were merged");
  const publish=calls.indexOf(domain[4]);
  check(calls.some((c,i)=>i<publish&&good(c)&&c.name==="processes_step_read"&&c.args.step_id===byKey.publish.id&&c.args.target==="shared"&&c.args.field==="approval_requirements"),"Publication did not reread frozen authorization policy");
  check(calls.some((c,i)=>i<publish&&good(c)&&c.name==="processes_step_read"&&c.args.target==="dependency"&&c.args.dependency_key==="inventory"&&c.args.step_id===byKey.portrait_4.id),"Missing targeted ancestor recovery");
  check(calls.some((c,i)=>i<publish&&good(c)&&c.name==="processes_step_read"&&c.args.step_id===byKey.publish.id&&c.args.target==="evidence"&&c.args.artifact_id==="portrait-3.png"),"Missing targeted artifact recovery");
  const completion=(s:any)=>{const events=s.events.filter((e:any)=>e.state==="completed");check(events.length===1,"Duplicate/missing completion");return events[0].id};
  for(const step of run.steps)for(const dep of step.definition.depends_on){const ready=step.events.find((e:any)=>["ready","waiting"].includes(e.state));check(ready&&ready.id>completion(byKey[dep]),"Dependency/approval gate bypassed")}
  check(calls.every(c=>!["processes_run_evidence","processes_run_update","spawn"].includes(c.name)),"Recovery rebuilt whole JSON or main took step ownership");
}
