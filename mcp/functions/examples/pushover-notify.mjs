// pushover-notify — pings a Pushover device via context.integration.
//
// Pre-req: a Pushover connection in this project. The slug ("pushover")
// resolves to the single matching connection at call time — no need
// to look up its numeric id by hand.
//
//   POST /fn/pushover-notify  { "message": "deploy finished" }
//   → { "ok": true }

export default async function handler(event, context) {
  if (!event?.message) throw new Error("event.message is required");

  await context.integration("pushover", "pushover_send_notification", {
    message: event.message,
    title: event.title ?? "Apteva",
    // priority -2..2 — Pushover default is 0; set to 1 to bypass the
    // user's quiet hours, 2 to require acknowledgement.
    priority: event.priority ?? 0,
  });

  return { ok: true };
}
