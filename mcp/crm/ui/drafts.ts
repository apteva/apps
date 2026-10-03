export interface ReplyDraftContent {
  channel: string; to: string; from: string; subject: string; body: string;
  body_html?: string; template_id?: number; content_sid?: string;
  template_vars?: Record<string, unknown>;
  attachments?: Record<string, unknown>[];
}
export interface SavedReplyDraft {
  id: number; contact_id: number; conversation_id: number; reply_to_activity_id: number;
  content: ReplyDraftContent; revision: number;
  status: "draft" | "sending" | "send_failed" | "sent" | "discarded";
  created_by: string; updated_by: string; created_at: string; updated_at: string; last_error?: string;
}
export interface ReplyDraftSummary {
  id: number; contact_id: number; conversation_id: number; channel: string; from: string; to: string;
  subject: string; preview: string; revision: number; status: SavedReplyDraft["status"];
  created_by: string; updated_by: string; updated_at: string; last_error?: string;
}
export function replyDraftEditable(draft?: SavedReplyDraft): boolean { return !draft || draft.status === "draft"; }
export function replyDraftFingerprint(content: ReplyDraftContent): string {
  return JSON.stringify(stable([content.channel, content.to, content.from, content.subject, content.body, content.body_html || "", content.template_id || 0, content.content_sid || "", content.template_vars || {}, content.attachments || []]));
}
function stable(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stable);
  if (value && typeof value === "object") return Object.fromEntries(Object.entries(value).sort(([a],[b])=>a.localeCompare(b)).map(([key,item])=>[key,stable(item)]));
  return value;
}

export function composerDraftContent(composer: {
  channel:string;to?:string;from:string;subject:string;body:string;bodyHTML?:string;
  templateMode?:boolean;templateId:string;contentSID?:string;templateVars:Record<string,unknown>;
  attachments:Record<string,unknown>[];
}): ReplyDraftContent {
  return {
    channel:composer.channel,to:composer.to || "",from:composer.from,subject:composer.subject,body:composer.body,
    body_html:composer.bodyHTML || "",template_id:composer.templateMode ? Number(composer.templateId || 0) : 0,
    content_sid:composer.templateMode ? composer.contentSID || "" : "",template_vars:composer.templateMode ? composer.templateVars : {},
    attachments:composer.attachments.map(({key:_key,...attachment})=>attachment),
  };
}
