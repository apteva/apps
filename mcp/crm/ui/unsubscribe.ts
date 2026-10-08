export type EmailUnsubscribeState = {
  project_id: string;
  contact_id: number;
  conversation_id: number;
  address: string;
  outbound_blocked: boolean;
  inbound_blocked: boolean;
  unsubscribed: boolean;
  confirmed: boolean;
  reason?: string;
  direction?: "outbound" | "both";
};

export function emailUnsubscribeLabel(state: EmailUnsubscribeState): string {
  return state.unsubscribed ? "Unsubscribed" : state.outbound_blocked ? "Email blocked" : "Unsubscribe this email";
}

export function emailUnsubscribeConfirmation(state: EmailUnsubscribeState): string {
  return `Stop all outbound email to ${state.address} in this project only, including campaigns and manual replies? ` +
    (state.inbound_blocked ? "An existing inbound block remains in place." : "Incoming replies will still reach the inbox.") +
    " No messages will be sent or deleted; other addresses and projects are unchanged.";
}

export function emailUnsubscribeRequest(state: EmailUnsubscribeState): { expected_address: string } {
  if (!state.address || !state.address.includes("@") || state.address.includes(" ")) throw new Error("Reload a valid unsubscribe preview first.");
  return { expected_address: state.address };
}
