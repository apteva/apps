import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ActivityRow } from "./CrmPanel";
import { messageSpeaker, type MessageParticipants } from "./message_speaker";

const participants: MessageParticipants = {
  contact: { name: "Alice", addresses: ["alice@example.com", "+447757771608"] },
  senders: [
    { channel: "email", address: "team@example.com", label: "Support" },
    { channel: "whatsapp", address: "+447380368854", label: "Support WhatsApp" },
    { channel: "sms", address: "+447380368854", label: "Support SMS" },
  ],
};

for (const channel of ["email", "whatsapp", "sms"]) {
  const contactAddress = channel === "email" ? "alice@example.com" : "+447757771608";
  const teamAddress = channel === "email" ? "team@example.com" : "+447380368854";
  test(`${channel}: recorded From identifies incoming contact and outgoing team`, () => {
    expect(messageSpeaker(`${channel}_received`, { from: contactAddress, to: [teamAddress] }, participants)).toMatchObject({
      direction: "incoming", role: "Contact", name: "Alice", address: contactAddress,
    });
    expect(messageSpeaker(`${channel}_sent`, { from: teamAddress, to: [contactAddress] }, participants)).toMatchObject({
      direction: "outgoing", role: "Your team", name: participants.senders!.find(sender => sender.channel === channel)!.label, address: teamAddress,
    });
  });
  test(`${channel}: rendered incoming/outgoing rows have distinct layout and explicit labels`, () => {
    const render = (kind: string, from: string, to: string) => renderToStaticMarkup(<ActivityRow
      compact participants={participants} onReply={() => {}} activity={{
        id: "1", kind, body: "Actual message", occurred_at: "2026-10-08T14:00:00Z", source: "messaging",
        message_addresses: { from, to: [to] }, message_status: { id: 10, status: "delivered" },
      }} />);
    const incoming = render(`${channel}_received`, contactAddress, teamAddress);
    const outgoing = render(`${channel}_sent`, teamAddress, contactAddress);
    expect(incoming).toContain('data-message-direction="incoming"');
    expect(outgoing).toContain('data-message-direction="outgoing"');
    expect(incoming).toContain("Contact · Alice");
    expect(incoming).toContain("← Incoming");
    expect(outgoing).toContain("Your team · Support");
    expect(outgoing).toContain("Outgoing →");
    expect(outgoing).toContain("bg-accent/10 border-accent/30");
    expect(incoming).toContain("margin-right:clamp(");
    expect(outgoing).toContain("margin-left:clamp(");
    expect(outgoing).toContain("via messaging");
    expect(outgoing).toContain("delivered");
    for (const html of [incoming, outgoing]) {
      expect(html).toContain("Actual message");
      expect(html).toContain(contactAddress);
      expect(html).toContain(teamAddress);
      expect(html).toContain('aria-label="Message addresses"');
    }
    expect(incoming).toContain(">Reply</button>");
    expect(outgoing).not.toContain(">Reply</button>");
  });
}

test("missing From never borrows To, receiving address or a default sender", () => {
  for (const kind of ["email_received", "email_sent", "whatsapp_received", "sms_sent"]) {
    const speaker = messageSpeaker(kind, { to: ["alice@example.com"], received_at: "team@example.com" }, participants)!;
    expect(speaker.name).toBeUndefined();
    expect(speaker.address).toBeUndefined();
    const html = renderToStaticMarkup(<ActivityRow participants={participants} onReply={() => {}} activity={{
      id: "1", kind, body: "Historical message", occurred_at: "2026-10-08T14:00:00Z",
    }} />);
    expect(html).toContain("Sender not recorded");
    expect(html).not.toContain("Alice");
    expect(html).not.toContain("Support");
  }
});

test("unmatched authors keep their recorded identity without borrowing a name", () => {
  expect(messageSpeaker("email_received", { from: "other@example.com" }, participants)).toMatchObject({ address: "other@example.com", name: undefined });
  expect(messageSpeaker("whatsapp_sent", { from: "+19999999999" }, participants)).toMatchObject({ address: "+19999999999", name: undefined });
  expect(messageSpeaker("sms_received", { from: "+991757771608" }, participants)?.name).toBeUndefined();
});

test("identity normalization permits mailbox wrappers/phone formatting, not aliases", () => {
  expect(messageSpeaker("email_received", { from: "Alice <ALICE@EXAMPLE.COM>" }, participants)?.name).toBe("Alice");
  expect(messageSpeaker("email_received", { from: "alice+sales@example.com" }, participants)?.name).toBeUndefined();
  expect(messageSpeaker("whatsapp_received", { from: "whatsapp:+44 7757-771608" }, participants)?.name).toBe("Alice");
  expect(messageSpeaker("sms_received", { from: "07757771608" }, participants)?.name).toBeUndefined();
});

test("sender labels are matched to the message channel, not merely the number", () => {
  expect(messageSpeaker("sms_sent", { from: "+447380368854" }, participants)?.name).toBe("Support SMS");
  expect(messageSpeaker("whatsapp_sent", { from: "+447380368854" }, participants)?.name).toBe("Support WhatsApp");
});

test("failed/test sends are outgoing but never presented as normal sent replies", () => {
  for (const channel of ["email", "whatsapp", "sms"]) {
    expect(messageSpeaker(`${channel}_send_failed`)?.directionLabel).toBe("Outgoing → · Send failed");
    expect(messageSpeaker(`${channel}_test_sent`)?.directionLabel).toBe("Outgoing → · Test message");
  }
  for (const kind of ["note", "system", "call", "meeting", "email_unsubscribed"]) expect(messageSpeaker(kind)).toBeNull();
});

test("speaker names, recorded addresses and bodies are escaped as text", () => {
  const html = renderToStaticMarkup(<ActivityRow onReply={() => {}} participants={{ contact: {
    name: "Alice <script>alert(1)</script>", addresses: ["alice@example.com"],
  } }} activity={{ id: "1", kind: "email_received", occurred_at: "2026-10-08T14:00:00Z",
    body: "<script>body</script>", message_addresses: { from: "Alice <alice@example.com>" },
  }} />);
  expect(html).not.toContain("<script>");
  expect(html).toContain("Alice &lt;script&gt;alert(1)&lt;/script&gt;");
  expect(html).toContain("Alice &lt;alice@example.com&gt;");
});
