// tables-search — reads rows back from the Tables app and returns
// them as JSON. Pass an email to filter; omit it to list recent rows.
//
//   POST /fn/tables-search  { "email": "marco@example.com" }
//   → { "total": 1, "rows": [{ "id": 1, "email": "marco@example.com" }] }

export default async function handler(event, context) {
  const where = event?.email
    ? [{ col: "email", op: "eq", value: event.email }]
    : [];

  const { rows, total } = await context.call("tables", "rows_search", {
    table: "leads",
    where,
    order_by: "id desc",
    limit: 25,
  });

  return { total, rows };
}
