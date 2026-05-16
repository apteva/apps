// slack-post — posts a message to a Slack channel via the project's
// Slack connection. Shows the same context.integration shape as
// pushover-notify but with a different upstream and a templated body.
//
// Pre-req: a Slack connection in this project with chat:write scope.
//
//   POST /fn/slack-post  { "channel": "#releases", "text": "v1.4.0 shipped" }
//   → { "ok": true, "ts": "1684169123.123456" }

export default async function handler(event, context) {
  const channel = event?.channel || "#general";
  const text = event?.text;
  if (!text) throw new Error("event.text is required");

  const res = await context.integration("slack", "slack_chat_postMessage", {
    channel,
    text,
  });

  // The upstream returns the standard Slack response — surface the
  // timestamp so downstream callers can edit / react to the message.
  return { ok: true, ts: res?.ts };
}
