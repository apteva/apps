import { check } from "./verify-outcomes";
export const MCP_RESPONSE_RECOVERY = "processes-mcp-response-recovery";
export function verifyMCPResponseRecovery(calls:any[], runs:any[], versions:any[]) {
  check(versions.length===2 && versions.map(v=>v.version).sort().join(",")==="1,2", "Expected two immutable versions");
  const byVersion=Object.fromEntries(versions.map(v=>[v.version,JSON.parse(v.body_json)]));
  check(byVersion[1].instructions.includes("POLICY-V1") && byVersion[2].instructions.includes("POLICY-V2") && byVersion[1].completion_criteria==="Save exactly Net: 800" && byVersion[2].completion_criteria==="Save exactly Net: 1000", "Lost version fidelity");
  check(runs.length===1 && !runs[0].workflow && runs[0].state==="completed" && runs[0].result==="Net: 1000" && runs[0].version===2 && runs[0].request_key.endsWith(":recovery-1"), "Wrong saved occurrence/version/result");
  const run=runs[0];
  const good=(c:any)=>c.ok&&c.completed;
  check(calls.filter(c=>good(c)&&c.name==="processes_create").length===1 && calls.filter(c=>good(c)&&c.name==="processes_update").length===1 && calls.filter(c=>good(c)&&c.name==="processes_start").length===1, "Repeated creation, revision or start");
  check(calls.every(c=>!c.name.startsWith("processes_step_") && c.thread_id==="main" && !["spawn","send"].includes(c.name)), "Recovery escaped direct main execution");
  const list=calls.findIndex(c=>good(c)&&c.name==="processes_list");
  const getVersion=(v:number)=>calls.findIndex(c=>good(c)&&c.name==="processes_get"&&Number(c.args?.version)===v&&c.args?.process_id===run.process_id);
  check(list>=0 && getVersion(2)>list && getVersion(1)>list && calls.some(c=>good(c)&&c.name==="processes_get"&&!c.args?.version), "Missing discovery or version rereads");
  const finish=calls.findIndex(c=>good(c)&&c.name==="processes_run_update"&&c.args?.run_id===run.id&&c.args?.state==="completed");
  check(finish>getVersion(1) && calls.some((c,i)=>i<finish&&good(c)&&c.name==="processes_run_get"&&c.args?.run_id===run.id), "Missing frozen execution read");
  const receipt=calls[finish];
  check(receipt.result_original_bytes<=1024 && !receipt.result_truncated, "Oversized update receipt");
  const ack=JSON.parse(receipt.result);
  check(ack.state==="completed"&&ack.progress===100&&ack.done===true&&ack.procedure_version===2&&ack.process_id===run.process_id&&ack.run_id===run.id, "Invalid saved-state receipt");
  check(!["run","definition","snapshot","result","output"].some(k=>k in ack), "Receipt repeated procedure or evidence");
  check(ack.reread?.tool==="processes_run_get"&&ack.reread.args?.process_id===run.process_id&&ack.reread.args?.run_id===run.id, "Invalid recovery reference");
  const reread=calls.findIndex((c,i)=>i>finish&&good(c)&&c.name===ack.reread.tool&&Object.entries(ack.reread.args).every(([k,v])=>c.args?.[k]===v));
  check(reread>finish && calls.some((c,i)=>i>reread&&good(c)&&c.name==="processes_runs"), "Saved outcome was not recovered and inspected");
}
