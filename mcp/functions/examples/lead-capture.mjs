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

  // Atomic deduplication by email also handles concurrent webhook deliveries.
  // A repeat delivery updates source/captured_at to the latest values.
  const { ids, inserted } = await context.call("tables", "rows_upsert", {
    table: "leads",
    key: ["email"],
    rows: [{
      email,
      source: event?.source ?? "function",
      captured_at: new Date().toISOString(),
    }],
  });

  context.log("captured lead", email);
  return { captured: inserted > 0, id: ids?.[0] };
}
