import { check } from "./verify-outcomes";
export const PROCESS_PATCH = "processes-process-patch";
export function verifyProcessPatch(calls: any[], versions: any[], process: any, runs: any[]) {
  check(process.status === "draft" && process.current_version === 3 && runs.length === 0, "Patch activated work or saved the wrong current version");
  check(versions.length === 3 && versions.map(v => v.version).join(",") === "1,2,3", "Expected exactly three immutable versions");
  const [v1,v2,v3] = versions.map(v => JSON.parse(v.body_json));
  const clean = (v:any) => { const d=structuredClone(v); for(const s of d.steps) delete s.position; return d; };
  const first = clean(v1), second = clean(v2), third = clean(v3);
  check(first.instructions === "SHARED-POLICY: Preserve exact evidence and operator approval." && first.approval_requirements === "Human approval is required before publish", "Missing shared/approval policy");
  check(first.steps.map((s:any)=>s.key).join(",") === "prepare,review,publish" && first.steps[0].instructions.includes("source:9007199254740993") && first.steps[1].instructions === "REVIEW-V1: Review prepared receipt", "Missing exact initial step identities/content");
  check(first.parameters.map((p:any)=>p.key).join(",") === "campaign,obsolete" && first.parameters[0].label === "Campaign" && first.parameters[0].default === "October", "Missing initial parameters");
  const expected2 = structuredClone(first);
  expected2.steps[1].instructions = "REVIEW-V2: Check exact receipt and request operator approval";
  check(JSON.stringify(second) === JSON.stringify(expected2), "Single-step patch changed unrelated fields or lost new instructions");
  const expected3 = structuredClone(second);
  expected3.description = "Targeted edits saved";
  expected3.steps = [...expected3.steps.slice(0,2),{key:"archive",name:"Archive",role:"worker",instructions:"Archive approved receipt",expected_output:"Archive receipt",depends_on:["review"]}];
  expected3.parameters = [{...expected3.parameters[0],label:"Campaign name"},{key:"count",label:"Count",type:"number",default:3,required:false}];
  // Object key order in schema-encoded structs is irrelevant to fidelity.
  const canonical = (value:any):string => JSON.stringify(value, (_key,v)=>v && !Array.isArray(v) && typeof v === "object" ? Object.fromEntries(Object.keys(v).sort().map(k=>[k,v[k]])) : v);
  check(canonical(third) === canonical(expected3), "Mixed patch changed unrelated content or lost dependencies/defaults");
  const good = (c:any) => c.ok && c.completed;
  check(calls.filter(c=>good(c)&&c.name==="processes_create").length === 1 && calls.every(c=>!["processes_update","processes_activate","processes_start"].includes(c.name)), "Used full replacement or repeated creation/execution");
  const patches = calls.filter(c=>c.name === "processes_patch" && c.completed);
  check(patches.length === 3 && patches.filter(good).length === 2, "Expected two successful patches and one rejected stale patch");
  const successes=patches.filter(good);
  const changes = (c:any) => typeof c.args.changes === "string" ? JSON.parse(c.args.changes) : c.args.changes;
  check(Object.keys(changes(successes[0])).join(",") === "steps" && changes(successes[0]).steps.update.length === 1 && Object.keys(changes(successes[0]).steps.update[0]).sort().join(",") === "instructions,key", "Agent resent unchanged step/procedure fields");
  const mixed = changes(successes[1]);
  check(Object.keys(mixed).sort().join(",") === "description,parameters,steps" && mixed.parameters.update.length === 1 && Object.keys(mixed.parameters.update[0]).sort().join(",") === "key,label", "Mixed edit resent unchanged parameter/procedure fields");
  const stale=patches.find(c=>!good(c));
  check(Number(stale.args.expected_version) === 1 && changes(stale).description === "STALE EDIT MUST NOT SAVE", "Did not exercise stale version rejection");
  successes.forEach((c,i)=>{
    check(Number(c.args.expected_version) === i+1 && !c.args.definition, "Wrong patch version or full replacement input");
    check(c.result_original_bytes <= 2048 && !c.result_truncated, "Oversized/truncated patch receipt");
    const ack=JSON.parse(c.result);
    check(ack.process_id === process.id && ack.previous_version === i+1 && ack.version === i+2 && ack.status === "draft", "Incorrect saved-state receipt");
    check(ack.changed_fields?.join(",") === (i === 0 ? "steps" : "description,parameters,steps"), "Incorrect changed-field manifest");
    check(i === 0 ? ack.collections?.steps?.updated?.join(",") === "review" : ack.collections?.steps?.added?.join(",") === "archive" && ack.collections?.steps?.removed?.join(",") === "publish" && ack.collections?.parameters?.updated?.join(",") === "campaign" && ack.collections?.parameters?.added?.join(",") === "count" && ack.collections?.parameters?.removed?.join(",") === "obsolete", "Incorrect collection-change manifest");
    check(!["definition","instructions","steps","parameters"].some(k=>k in ack), "Receipt echoed procedure content");
    check(ack.reread?.tool === "processes_get" && ack.reread.args.process_id === process.id && ack.reread.args.version === i+2, "Invalid exact reread reference");
    const index=calls.indexOf(c);
    check(calls.some((read,j)=>j>index&&good(read)&&read.name===ack.reread.tool&&read.args.process_id===process.id&&Number(read.args.version)===ack.reread.args.version), "Agent did not recover the saved patch");
  });
  const lastPatch=calls.indexOf(successes[1]);
  check(calls.some((c,i)=>i>lastPatch&&good(c)&&c.name==="processes_get"&&c.args.process_id===process.id&&Number(c.args.version)===1), "Missing final immutable historical read");
}
