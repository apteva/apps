import { expect, test } from "bun:test";
import { verifyProcessPatch } from "./process-patch";
function fixture() {
 const v1:any={instructions:"SHARED-POLICY: Preserve exact evidence and operator approval.",approval_requirements:"Human approval is required before publish",description:"Keep this description",steps:[
  {key:"prepare",instructions:"KEEP-PREPARE: source:9007199254740993",depends_on:[]},
  {key:"review",instructions:"REVIEW-V1: Review prepared receipt",depends_on:["prepare"]},
  {key:"publish",instructions:"KEEP-PUBLISH",depends_on:["review"]}],
  parameters:[{key:"campaign",label:"Campaign",type:"string",default:"October",required:false},{key:"obsolete",type:"boolean",default:false,required:false}]};
 const v2=structuredClone(v1);v2.steps[1].instructions="REVIEW-V2: Check exact receipt and request operator approval";
 const v3=structuredClone(v2);v3.description="Targeted edits saved";
 v3.steps=[...v3.steps.slice(0,2),{key:"archive",name:"Archive",role:"worker",instructions:"Archive approved receipt",expected_output:"Archive receipt",depends_on:["review"]}];
 v3.parameters=[{...v3.parameters[0],label:"Campaign name"},{key:"count",label:"Count",type:"number",default:3,required:false}];
 const versions=[v1,v2,v3].map((v,i)=>({version:i+1,body_json:JSON.stringify(v)}));
 const call=(name:string,args:any={},ok=true,result?:any)=>({name,args,ok,completed:true,result:JSON.stringify(result),result_original_bytes:450,result_truncated:false});
 const receipt=(version:number)=>({process_id:"p",previous_version:version-1,version,status:"draft",changed_fields:version===2?["steps"]:["description","parameters","steps"],collections:version===2?{steps:{updated:["review"]}}:{steps:{added:["archive"],removed:["publish"]},parameters:{added:["count"],updated:["campaign"],removed:["obsolete"]}},reread:{tool:"processes_get",args:{process_id:"p",version}}});
 const calls=[call("processes_create"),call("processes_patch",{process_id:"p",expected_version:"1",changes:{steps:{update:[{key:"review",instructions:v2.steps[1].instructions}]}}},true,receipt(2)),call("processes_get",{process_id:"p",version:"2"}),call("processes_patch",{process_id:"p",expected_version:"1",changes:{description:"STALE EDIT MUST NOT SAVE"}},false),call("processes_patch",{process_id:"p",expected_version:"2",changes:{description:v3.description,steps:{remove:["publish"],add:[v3.steps[2]]},parameters:{remove:["obsolete"],update:[{key:"campaign",label:"Campaign name"}],add:[v3.parameters[1]]}}},true,receipt(3)),call("processes_get",{process_id:"p",version:"3"}),call("processes_get",{process_id:"p",version:"1"})];
 return {calls,versions,process:{id:"p",status:"draft",current_version:3},runs:[] as any[]};
}
const verify=(f:ReturnType<typeof fixture>)=>verifyProcessPatch(f.calls,f.versions,f.process,f.runs);
test("accepts targeted edits and recovered immutable history",()=>expect(()=>verify(fixture())).not.toThrow());
const mutations:Record<string,(f:ReturnType<typeof fixture>)=>void>={
 "unrelated instructions changed":f=>{const v=JSON.parse(f.versions[1].body_json);v.instructions="lost";f.versions[1].body_json=JSON.stringify(v)},
 "historical receipt changed":f=>{const v=JSON.parse(f.versions[0].body_json);v.steps[0].instructions="rounded:9007199254740992";f.versions[0].body_json=JSON.stringify(v)},
 "default lost in partial edit":f=>{const v=JSON.parse(f.versions[2].body_json);delete v.parameters[0].default;f.versions[2].body_json=JSON.stringify(v)},
 "stale edit succeeded":f=>{f.calls[3].ok=true},
 "extra version":f=>f.versions.push(f.versions[2]),
 "agent resends entire step":f=>{f.calls[1].args.changes.steps.update[0].name="Unchanged name"},
 "oversized receipt":f=>{f.calls[1].result_original_bytes=10000},
 "wrong reread":f=>{const a=JSON.parse(f.calls[1].result!);a.reread.args.process_id="other";f.calls[1].result=JSON.stringify(a)},
 "missing recovered v3":f=>{f.calls.splice(5,1)},
 "missing historical read":f=>{f.calls.pop()},
 "unexpected run":f=>{f.runs.push({})},
};
for(const [name,mutate] of Object.entries(mutations))test(`rejects ${name}`,()=>{const f=fixture();mutate(f);expect(()=>verify(f)).toThrow()});
