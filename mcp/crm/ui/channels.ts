export const channelPresentation: Record<string, { label: string; icon: string; color: string; backgroundColor: string }> = {
  email: { label: "Email", icon: "✉", color: "var(--info, #60a5fa)", backgroundColor: "rgba(59,130,246,0.14)" },
  whatsapp: { label: "WhatsApp", icon: "◉", color: "var(--success, #4ade80)", backgroundColor: "rgba(34,197,94,0.14)" },
  sms: { label: "SMS", icon: "▤", color: "var(--crm-sms-color, #c4b5fd)", backgroundColor: "rgba(139,92,246,0.14)" },
};

export const channelThemeCSS = ':root{--crm-sms-color:#c4b5fd}[data-mode="light"]{--crm-sms-color:#6d28d9}';

export type WhatsAppSessionState =
  | { state: "idle" | "checking" }
  | { state: "active" | "closed"; lastInbound?: string; deadline?: number }
  | { state: "error"; error?: string };

export interface WhatsAppSessionResponse { active: boolean; last_inbound?: string; expires_at?: string; checked_at?: string }

export function sessionFromResponse(response: WhatsAppSessionResponse, now = Date.now()): WhatsAppSessionState {
  const expires = Date.parse(response.expires_at || "");
  const checked = Date.parse(response.checked_at || "");
  // Use server-relative remaining time, so a skewed browser clock cannot
  // incorrectly extend/close the window. Missing expiry fails closed.
  const remaining = expires - checked;
  return { state: response.active && Number.isFinite(remaining) && remaining > 0 ? "active" : "closed",
    deadline: Number.isFinite(remaining) ? now + remaining : undefined, lastInbound: response.last_inbound };
}

export function whatsappSessionRequiresTemplate(session: WhatsAppSessionState, now = Date.now()): boolean {
  return session.state !== "active" || !session.deadline || now >= session.deadline;
}

export function whatsappWindowLabel(session: WhatsAppSessionState, now = Date.now()): string {
  if (session.state === "checking") return "Checking WhatsApp reply window…";
  if (session.state === "error") return "Unable to check WhatsApp window · use an approved template";
  if (session.state === "idle") return "Choose a WhatsApp sender to check the reply window";
  if (whatsappSessionRequiresTemplate(session, now)) return "WhatsApp window closed · approved template required";
  const minutes = Math.max(1, Math.ceil(((session.deadline || now) - now) / 60000));
  const duration = minutes >= 60 ? `${Math.floor(minutes / 60)}h ${minutes % 60}m` : `${minutes}m`;
  return `WhatsApp free-form reply available · expires in ${duration}`;
}

export function conversationChannels(original: string, kinds: string[]): string[] {
  return [...new Set([original, ...kinds.map(kind => /^(email|sms|whatsapp)_(sent|received)$/.exec(kind)?.[1] || "")].filter(Boolean))];
}
