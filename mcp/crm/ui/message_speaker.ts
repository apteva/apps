import type { MessageAddresses } from "./message_addresses";

export interface MessageParticipants {
  contact?: { name: string; addresses: string[] };
  senders?: { channel: string; address: string; label: string }[];
}

// Match recorded identities only. Never substitute a recipient, default sender
// or integration source for the author of a historical message.
function identity(address: string, channel: string): string {
  const value = address.trim();
  if (channel === "email") return (value.match(/<([^<>]+)>$/)?.[1] || value).trim().toLowerCase();
  const phone = value.replace(/^(whatsapp|sms|tel):/i, "");
  return /^[+\d][\d ()-]*$/.test(phone) ? phone.replace(/[ ()-]/g, "") : phone;
}

export function messageSpeaker(kind: string, addresses?: MessageAddresses, participants?: MessageParticipants) {
  const match = /^(email|sms|whatsapp)_(received|sent|send_failed|test_sent)$/.exec(kind);
  if (!match) return null; // Notes and system activities are not a person speaking.
  const channel = match[1];
  const direction = match[2] === "received" ? "incoming" : "outgoing";
  const address = addresses?.from?.trim() || "";
  const recordedIdentity = identity(address, channel);
  const contact = participants?.contact;
  const name = !address ? undefined : direction === "incoming"
    ? (contact?.addresses.some(candidate => identity(candidate, channel) === recordedIdentity) ? contact.name : undefined)
    : participants?.senders?.find(sender => sender.channel === channel && identity(sender.address, channel) === recordedIdentity)?.label;
  return {
    channel, direction, role: direction === "incoming" ? "Contact" : "Your team",
    directionLabel: direction === "incoming" ? "← Incoming"
      : match[2] === "send_failed" ? "Outgoing → · Send failed"
      : match[2] === "test_sent" ? "Outgoing → · Test message" : "Outgoing →",
    name: name?.trim() || undefined, address: address || undefined,
  };
}
