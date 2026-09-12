import { useConversationLocalization, type ConversationMessageKey, type ConversationMessageParams } from "./i18n";
import type { ToolActivity as StoredActivity } from "./types";
import type { ToolActivity } from "./toolActivityModel";
import { buildToolVisualRegistry } from "./toolVisuals";
import { toolSources } from "./toolSources";

export const toolVisualRegistry = buildToolVisualRegistry([...toolSources], [], []);
export function useToolTranslation() {
  const localization=useConversationLocalization();
  return {t:(key:string,options?:Record<string,unknown>)=>localization.t(key as ConversationMessageKey,options as ConversationMessageParams)};
}
export function toChatToolActivity(activity:StoredActivity):ToolActivity {
  const startedAt=Date.parse(activity.started_at)||0;
  const finishedAt=activity.ended_at ? Date.parse(activity.ended_at) : undefined;
  return {id:String(activity.id),callId:activity.call_id,agentId:activity.agent_id,threadId:activity.thread_id,
    name:activity.name,reason:activity.reason,state:activity.status === "running" ? "running" : activity.status === "interrupted" ? "interrupted" : "done",
    success:activity.status === "failed" ? false : activity.status === "completed" ? true : undefined,
    startedAt,finishedAt,durationMs:finishedAt===undefined?undefined:Math.max(0,finishedAt-startedAt)};
}
