// fetch-json — uses the built-in global `fetch` (Node 18+) to pull
// data from a URL in the event. No dependencies, no package.json.
//
//   POST /fn/fetch-json  { "url": "https://api.github.com/zen" }
//   → { "status": 200, "ok": true, "body": "..." }

export default async function handler(event, context) {
  if (!event?.url) throw new Error("event.url is required");
  const res = await fetch(event.url);
  const contentType = res.headers.get("content-type") || "";
  const body = contentType.includes("json") ? await res.json() : await res.text();
  return { status: res.status, ok: res.ok, body };
}
