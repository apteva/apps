export function mcpEnvelopeError(data: unknown): string {
  if (!data || typeof data !== "object" || !(data as { isError?: boolean }).isError) {
    return "";
  }
  const content = (data as { content?: unknown }).content;
  if (Array.isArray(content)) {
    const text = content.find(
      (item): item is { type: string; text: string } =>
        !!item && typeof item === "object" &&
        (item as { type?: unknown }).type === "text" &&
        typeof (item as { text?: unknown }).text === "string",
    );
    if (text?.text.trim()) return text.text;
  }
  return "Request failed.";
}

export function finalizedAccountError(data: unknown): string {
  const envelopeError = mcpEnvelopeError(data);
  if (envelopeError) return envelopeError;
  if (!data || typeof data !== "object") return "Account was not finalized.";
  const accountID = Number((data as { social_account_id?: unknown }).social_account_id);
  return Number.isSafeInteger(accountID) && accountID > 0
    ? ""
    : "Account was not finalized.";
}
