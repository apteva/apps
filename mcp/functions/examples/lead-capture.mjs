// lead-capture — a realistic webhook handler. Takes an inbound
// payload, dedupes against the Tables app, stores the new lead, and
// returns a compact JSON receipt. Wire it as an HTTP function
// (POST /fn/lead-capture) or a Jobs cron target.
//
// Pre-req: a `leads` table with email / source / captured_at columns.
//
//   POST /fn/lead-capture  { "email": "marco@example.com", "source": "site" }
//   → { "captured": true, "id": 1 }

export default async function handler(event, context) {
  const email = event?.email?.trim();
  if (!email) throw new Error("payload must include an email");

  // Already have this lead? Skip the insert.
  const { count } = await context.call("tables", "rows_count", {
    table: "leads",
    where: [{ col: "email", op: "eq", value: email }],
  });
  if (count > 0) {
    return { captured: false, reason: "already exists" };
  }

  const { ids } = await context.call("tables", "rows_insert", {
    table: "leads",
    rows: [{
      email,
      source: event?.source ?? "function",
      captured_at: new Date().toISOString(),
    }],
  });

  context.log("captured lead", email);
  return { captured: true, id: ids?.[0] };
}
