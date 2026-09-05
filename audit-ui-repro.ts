// Executes the actual release's pure conversion helpers and contact-loading
// callback with controlled local promises. No browser or network is used.
const source = await Bun.file(new URL("./mcp/crm/ui/CrmPanel.tsx", import.meta.url)).text();
const transpiler = new Bun.Transpiler({ loader: "tsx" });
const start = source.indexOf("function predicateToDraft(");
const end = source.indexOf("function SegmentEditorModal(", start);
const helpers = transpiler.transformSync(source.slice(start, end));
const roundTrip = new Function(helpers + ";return p=>draftToPredicate(predicateToDraft(p))")();
for (const value of [42, true, "2026-09-05", ["a", "b"]]) {
  const before = { predicate: "attribute", key: "example", op: "eq", value };
  const after=roundTrip(before);if(JSON.stringify(after)!==JSON.stringify(before)) throw new Error("Segment round-trip failed: "+JSON.stringify({before,after}));
  console.log(JSON.stringify({ check: "typed segment round-trip", before, after }));
}

const callbackStart = source.indexOf("const selectContact = useCallback(");
const callbackEnd = source.indexOf("const handleSave =", callbackStart);
const callback = transpiler.transformSync(source.slice(callbackStart, callbackEnd));
const state: Record<string, unknown> = {};
const setterNames = ["setSelectedId", "setDetail", "setActivities", "setConversations", "setContactLists", "setContactOpportunities", "setEdits", "setStatus"];
const setters = setterNames.map(name => (value: unknown) => { state[name] = value; });
const releases: Record<string, () => void> = {};
const ready = Object.fromEntries(["A", "B"].map(id => [id, new Promise<void>(resolve => { releases[id] = resolve; })]));
const api = async (_method: string, path: string) => {
  const id = path.split("/")[2];
  await ready[id];
  return { contact: { id }, activities: [], conversations: [], lists: [], opportunities: [] };
};
const selectContact = new Function("useCallback", "api", "detailSequence", "selectedContactRef", "edits", "window", "detailAbort", "setConfirmDialog", ...setterNames, callback + ";return selectContact;")((fn: unknown) => fn, api, {current:0}, {current:null}, {}, {confirm:()=>true}, {current:null}, ()=>{}, ...setters);
const pendingA = selectContact("A");
const pendingB = selectContact("B");
releases.B();
await pendingB;
releases.A();
await pendingA;
console.log(JSON.stringify({ issue: "out-of-order contact loads", selected: state.setSelectedId, displayed: state.setDetail }));

if (state.setSelectedId !== "B" || (state.setDetail as any)?.id !== "B") throw new Error("Stale selection response overwrote detail");

const parseDraft = new Function(helpers + ';return draftToPredicate')();
for (const type of ['number','boolean','array']) {
  if (!parseDraft({k:'attribute',key:'example',op:'is_null',valueType:type,value:''})) throw new Error('Null predicate rejected for '+type);
}
let confirmation: any;
const ref = {current:'A'};
const guarded = new Function('useCallback','api','detailSequence','selectedContactRef','edits','window','detailAbort','setConfirmDialog',...setterNames,callback+';return selectContact;')((fn:unknown)=>fn,api,{current:0},ref,{first_name:'draft'},{},{current:null},(value:any)=>confirmation=value,...setters);
await guarded('B');
if(ref.current!=='A'||!confirmation)throw new Error('Dirty selection did not preserve draft');
await confirmation.onConfirm();
if(ref.current!=='B')throw new Error('Confirmed discard did not switch contact');
console.log(JSON.stringify({check:'typed null predicates and custom discard dialog callback',passed:true}));
