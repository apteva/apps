// A display-only guard for older email conversions. Stored content and
// non-email notes/outbound messages retain their original formatting.
export function messageDisplayBody(kind: string, body?: string): string {
  if (!body || kind !== "email_received") return body || "";
  return body.replace(/\r\n?/g, "\n").replace(/\n(?:[\t \u00a0]*\n){2,}/g, "\n\n");
}
