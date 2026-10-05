import { expect,test } from "bun:test";
import { verifyMCPResponseRecovery } from "./mcp-response-recovery";
function fixture(){
 const runs=[{id:"r",process_id:"p",workflow:false,state:"completed",result:"Net: 1000",version:2,request_key:"a:manual:recovery-1"}];
 const versions=[1,2].map(version=>({version,body_json:JSON.stringify({instructions:`POLICY-V${version}`,completion_criteria:`Save exactly Net: ${version===1?800:1000}`})}));
 const call=(name:string,args:any={},result?:any)=>({name,args,ok:true,completed:true,thread_id:"main",...(result?{result:JSON.stringify(result),result_original_bytes:500,result_truncated:false}:{})});
 const calls=[call("processes_create"),call("processes_update"),call("processes_list"),call("processes_get",{process_id:"p",version:"2"}),call("processes_get",{process_id:"p"}),call("processes_get",{process_id:"p",version:"1"}),call("processes_start"),call("processes_run_get",{run_id:"r"}),call("processes_run_update",{run_id:"r",state:"completed"},{state:"completed",progress:100,done:true,procedure_version:2,process_id:"p",run_id:"r",reread:{tool:"processes_run_get",args:{process_id:"p",run_id:"r"}}}),call("processes_run_get",{process_id:"p",run_id:"r"}),call("processes_runs")];
 return {calls,runs,versions};
}
const verify=(f:ReturnType<typeof fixture>)=>verifyMCPResponseRecovery(f.calls,f.runs,f.versions);
test("accepts immutable versions and a recovered compact direct receipt",()=>expect(()=>verify(fixture())).not.toThrow());
const mutations:Record<string,(f:any)=>void>={
 "wrong frozen version":f=>f.runs[0].version=1,
 "wrong saved output":f=>f.runs[0].result="Net: 800",
 "lost historical policy":f=>f.versions[0].body_json='{}',
 "repeated occurrence":f=>f.runs.push(f.runs[0]),
 "missing discovery":f=>f.calls=f.calls.filter((c:any)=>c.name!=="processes_list"),
 "missing immutable reread":f=>f.calls=f.calls.filter((c:any)=>c.args.version!=="1"),
 "oversized receipt":f=>f.calls[8].result_original_bytes=10000,
 "wrong recovery target":f=>{const a=JSON.parse(f.calls[8].result);a.reread.args.run_id="wrong";f.calls[8].result=JSON.stringify(a);},
 "echoed procedure":f=>{const a=JSON.parse(f.calls[8].result);a.definition={instructions:"repeated"};f.calls[8].result=JSON.stringify(a);},
 "missing outcome reread":f=>f.calls.splice(9,1),
 "worker escaped main":f=>f.calls[8].thread_id="other",
};
for(const [name,mutate] of Object.entries(mutations))test(`rejects ${name}`,()=>{const f=fixture();mutate(f);expect(()=>verify(f)).toThrow();});
