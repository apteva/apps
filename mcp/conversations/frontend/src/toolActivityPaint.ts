import type { ToolActivity } from "./types";
// Paint each call's start before its result, without delaying parallel calls
// or changing execution timestamps.
export function splitActivityPaint(events: ToolActivity[]) {
 const phases = new Map<number, string>();
 const paint: ToolActivity[] = [], deferred: ToolActivity[] = [];
 for (const event of events) {
  if (!phases.has(event.id)) phases.set(event.id,event.status);
  (phases.get(event.id) === event.status ? paint : deferred).push(event);
 }
 return {paint,deferred};
}
