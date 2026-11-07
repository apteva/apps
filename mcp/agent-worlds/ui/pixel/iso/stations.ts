// Which part of a building a tool call sends an agent to, and how to say what
// the agent is doing. Both work from the tool name alone ("contacts_search",
// "conversation_drafts_send"), so every app gets them without describing itself.

// Work spots are tagged with nouns ("contact") and verb classes ("read").
export type Spot = { x: number; y: number; tags?: string[] };

const CLASSES: Record<string, "read" | "write" | "send"> = {};
for (const word of "search get list find query count eval check read fetch lookup inbox status view describe resolve membership verify".split(" ")) CLASSES[word] = "read";
for (const word of "create update set add upsert merge delete archive remove log define save upload edit move assign materialise attach import".split(" ")) CLASSES[word] = "write";
for (const word of "send reply message post publish notify draft receive inbound broadcast".split(" ")) CLASSES[word] = "send";

const VERBS: Record<string, string> = {
  search: "searching", get: "reading", list: "listing", find: "finding", query: "querying", count: "counting", eval: "evaluating",
  check: "checking", read: "reading", fetch: "fetching", verify: "verifying", resolve: "resolving", create: "adding", add: "adding",
  update: "updating", set: "updating", upsert: "saving", save: "saving", merge: "merging", delete: "deleting", archive: "archiving",
  remove: "removing", log: "logging", define: "defining", upload: "uploading", edit: "editing", move: "moving", assign: "assigning",
  send: "sending", reply: "replying to", post: "posting", publish: "publishing", notify: "notifying", draft: "drafting",
  import: "importing", materialise: "building", attach: "attaching", deploy: "deploying", build: "building", run: "running",
};

// Singular, lowercase: "opportunities" -> "opportunity", "contacts" -> "contact".
export function stem(word: string) {
  const w = word.toLowerCase();
  if (w.length > 4 && w.endsWith("ies")) return `${w.slice(0, -3)}y`;
  if (w.length > 3 && w.endsWith("s") && !w.endsWith("ss") && !w.endsWith("us")) return w.slice(0, -1);
  return w;
}
export const words = (tool: string) => tool.split(/[^a-zA-Z0-9]+/).filter(Boolean).map(word => word.toLowerCase());

// Nouns score double, so "opportunities_search" goes to the pipeline board
// rather than any shelf that is merely tagged "read".
export function stationSpots<S extends Spot>(spots: S[], tool: string | undefined): S[] {
  if (!tool) return spots;
  const nouns = new Set(words(tool).map(stem)), classes = new Set([...nouns].map(word => CLASSES[word]).filter(Boolean));
  let best = 0, chosen: S[] = [];
  for (const spot of spots) {
    const score = (spot.tags || []).reduce((sum, tag) => sum + (nouns.has(tag) ? 2 : classes.has(tag as any) ? 1 : 0), 0);
    if (score > best) { best = score; chosen = [spot]; }
    else if (score === best && score > 0) chosen.push(spot);
  }
  return best > 0 ? chosen : spots;
}

// "contacts_search" -> "searching contacts", "contacts_set_conversation_status"
// -> "updating conversation status". Words naming the app itself are dropped.
export function describeTool(tool: string, appName = "") {
  const app = new Set(words(appName));
  const parts = words(tool).filter(word => !app.has(word));
  const at = parts.findIndex(word => VERBS[word]);
  if (at < 0) return parts.length ? `using ${parts.join(" ")}` : "working";
  const after = parts.slice(at + 1), before = parts.slice(0, at);
  const object = (after.length ? after : before).join(" ");
  return object ? `${VERBS[parts[at]]} ${object}` : VERBS[parts[at]].replace(/ to$/, "");
}
