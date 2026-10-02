export interface MessageAddresses {
  from?: string;
  to?: string[];
  cc?: string[];
  bcc?: string[];
  received_at?: string;
}

export function messageRecipientSummary(addresses?: MessageAddresses): string {
  const to = addresses?.to?.filter(Boolean).join(", ");
  if (to) return `To: ${to}`;
  if (addresses?.received_at) return `Received at: ${addresses.received_at}`;
  return "To: Unknown";
}

export function messageAddressLines(addresses?: MessageAddresses): [string, string][] {
  const lines: [string, string][] = [
    ["From", addresses?.from || "Unknown"],
    ["To", addresses?.to?.filter(Boolean).join(", ") || "Unknown"],
  ];
  if (addresses?.cc?.length) lines.push(["CC", addresses.cc.join(", ")]);
  if (addresses?.bcc?.length) lines.push(["BCC", addresses.bcc.join(", ")]);
  if (addresses?.received_at) lines.push(["Received at", addresses.received_at]);
  return lines;
}
