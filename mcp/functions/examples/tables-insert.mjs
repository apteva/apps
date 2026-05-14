// tables-insert — writes a row into a table owned by the Tables app
// via context.call. The Tables app does the database work; this
// function never touches another app's DB directly.
//
// Pre-req: a `leads` table, e.g.
//   tables_create { name: "leads", columns: [{ name: "email", type: "text" }] }
//
//   POST /fn/tables-insert  { "email": "marco@example.com" }
//   → { "inserted": 1, "id": 1 }

export default async function handler(event, context) {
  if (!event?.email) throw new Error("event.email is required");

  const { ids, inserted } = await context.call("tables", "rows_insert", {
    table: "leads",
    rows: [{ email: event.email }],
  });

  return { inserted, id: ids?.[0] };
}
