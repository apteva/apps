// hello — the canonical example. Echoes a name from the event.
//
//   functions_invoke { name: "hello", event: { name: "Marco" } }
//   → { "hello": "Marco" }
//
//   POST /fn/hello  { "name": "Marco" }   (empty body → "world")

export default async function handler(event, context) {
  return { hello: event?.name ?? "world" };
}
