import { useConversationLocalization, type ConversationMessageKey, type ConversationMessageParams } from "./i18n";
import { useConversationAPI } from "./context";
import { useEffect, useState } from "react";
import type { ToolActivity as StoredActivity } from "./types";
import type { ToolActivity } from "./toolActivityModel";
import { buildToolVisualRegistry } from "./toolVisuals";
import { toolSources } from "./toolSources";

export const toolVisualRegistry = buildToolVisualRegistry([...toolSources], [], []);
export function useToolVisualRegistry() {
  const { conversationsClient } = useConversationAPI();
  const [registry, setRegistry] = useState(toolVisualRegistry);
  useEffect(() => {
    let active = true;
    conversationsClient.toolVisuals().then(({ integrations }) => {
      if (active && integrations?.length) setRegistry(buildToolVisualRegistry([...toolSources], integrations.map(i => ({app_slug:i.slug, app_name:i.name, logo:i.logo})), []));
    }).catch(() => {});
    return () => { active = false; };
  }, [conversationsClient]);
  return registry;
}
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
    startedAt,finishedAt,durationMs:Number.isFinite(activity.duration_ms) && activity.duration_ms! >= 0 ? activity.duration_ms : finishedAt===undefined?undefined:Math.max(0,finishedAt-startedAt)};
}
