import { check } from "./verify-outcomes";
export const RUN_MEMORY="processes-run-memory";
export function verifyRunMemory(calls:any[],runs:any[],summaries:any[],entries:any[],approvals:any[]) {
 const ordered=[...runs].sort((a,b)=>a.created_at.localeCompare(b.created_at));
 check(ordered.length===2&&ordered.every(r=>r.state==="completed"),"Expected two completed runs");
 check(approvals.length===2&&new Set(approvals.map(a=>a.step.run_id)).size===2,"Missing independent operator approvals");
 const good=(c:any)=>c.ok&&c.completed;
 const wanted=[{area:"north",ref:"source:9007199254740993"},{area:"south",ref:"source:9007199254740995"}];
 ordered.forEach((run,i)=>{
  const {area,ref}=wanted[i];const step=run.steps.find((s:any)=>s.key==="discover"),gate=run.steps.find((s:any)=>s.key==="approve"),record=run.steps.find((s:any)=>s.key==="record");
  check(step.output===`Area: ${area}; receipt: ${ref}`,"Repeated discovery or lost exact identity");
  check(gate.updated_by==="operator"&&gate.output==="MEMORY-APPROVED"&&record.output===`Recorded ${area}; receipt: ${ref}; approval: MEMORY-APPROVED`,"Approval/result evidence changed");
  check(Date.parse(gate.completed_at)<=Date.parse(record.completed_at)&&Date.parse(step.completed_at)<=Date.parse(gate.completed_at),"Approval gate bypassed");
  const checkpoints=summaries.filter(s=>s.run_id===run.id);check(checkpoints.length===2&&checkpoints.map(s=>s.revision).sort().join(",")==="1,2","Missing versioned progress/final checkpoints");
  check(checkpoints.every(s=>JSON.parse(s.body_json).references.includes(ref)&&s.author===`agent:${step.executor.agent_id}:${step.target_thread_id}`),"Checkpoint author/reference drift");
  const writes=calls.filter(c=>good(c)&&c.name==="processes_memory_upsert"&&c.args?.run_id===run.id);
  check(writes.length===2&&writes.every(c=>c.args.key===`area:${area}`&&c.args.scope==="campaign-october")&&JSON.stringify(writes[0].args)===JSON.stringify(writes[1].args),"Non-idempotent/repeated area writes");
  check(calls.some(c=>good(c)&&c.name==="processes_memory_list"&&c.thread_id===step.target_thread_id&&c.args?.assignment_id===run.assignment_id),"Worker didn't read scoped memory");
 });
 check(entries.length===2&&entries.every(e=>e.revision===1)&&new Set(entries.map(e=>e.entry_key)).size===2,"Ledger overwrote previous discovery or retry added records");
 const second=ordered[1].steps.find((s:any)=>s.key==="discover");const reads=calls.filter(c=>good(c)&&c.thread_id===second.target_thread_id);
 const history=reads.findIndex(c=>c.name==="processes_runs"&&c.args.status==="completed");const previous=reads.findIndex(c=>c.name==="processes_run_get"&&c.args.run_id===ordered[0].id);const summary=reads.findIndex(c=>c.name==="processes_summary_get"&&c.args.run_id===ordered[0].id);const ledger=reads.findIndex(c=>c.name==="processes_memory_list");const write=reads.findIndex(c=>c.name==="processes_memory_upsert");
 check(history>=0&&previous>history&&summary>history&&ledger>=0&&write>ledger&&write>summary,"Second run didn't recover previous context before new discovery");
 for(const c of calls.filter(c=>good(c)&&["processes_runs","processes_memory_list"].includes(c.name))){check(c.result_original_bytes<=16*1024,"History or ledger exceeded budget");}
 check(calls.every(c=>!["spawn","processes_run_update"].includes(c.name)),"Main took step ownership");
}
