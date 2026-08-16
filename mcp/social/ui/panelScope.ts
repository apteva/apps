export function scopedAppURL(
  base: string,
  path: string,
  projectId?: string | null,
): string {
  if (!projectId) return `${base}${path}`;
  const separator = path.includes("?") ? "&" : "?";
  return `${base}${path}${separator}project_id=${encodeURIComponent(projectId)}`;
}

export function parseStoredProfileId(raw: string | null): number | null {
  if (!raw) return null;
  const id = Number(raw);
  return Number.isSafeInteger(id) && id > 0 ? id : null;
}

export function isTrustedOAuthMessage(
  eventOrigin: string,
  currentOrigin: string,
  data: unknown,
): data is { type: "social.oauth_ready"; pending_account_id: number; connection_id: number } {
  if (eventOrigin !== currentOrigin || !data || typeof data !== "object") return false;
  const message = data as Record<string, unknown>;
  return message.type === "social.oauth_ready" &&
    typeof message.pending_account_id === "number" &&
    message.pending_account_id > 0 &&
    typeof message.connection_id === "number";
}
