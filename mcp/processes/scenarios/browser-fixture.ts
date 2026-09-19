/** Real page state: deliberately no cookies, localStorage, or saved profile. */
export function startBrowserFixture() {
  const visits: string[] = [];
  const receipts: { nonce: string; draft: string }[] = [];
  const server = Bun.serve({
    hostname: "127.0.0.1", port: 0,
    async fetch(request) {
      const path = new URL(request.url).pathname;
      if (path === "/") {
        const nonce = crypto.randomUUID();
        visits.push(nonce);
        return new Response(`<!doctype html><html><body>
          <h1>Process browser continuity</h1>
          <p>Page token: <strong id="token">${nonce}</strong></p>
          <label>Draft <input id="draft" autocomplete="off"></label>
          <button id="prepare">Prepare draft</button>
          <button id="submit" disabled>Submit draft</button>
          <p id="status">Empty</p>
          <script>
          let prepared = '';
          document.querySelector('#prepare').onclick = () => {
            prepared = document.querySelector('#draft').value;
            document.querySelector('#submit').disabled = !prepared;
            document.querySelector('#status').textContent = 'Prepared: ' + prepared;
          };
          document.querySelector('#submit').onclick = async () => {
            const response = await fetch('/submit', {method:'POST',
              headers:{'Content-Type':'application/json'},
              body:JSON.stringify({nonce:${JSON.stringify(nonce)},draft:prepared})});
            document.querySelector('#status').textContent = response.ok ? 'Receipt accepted' : 'Receipt rejected';
            document.querySelector('#submit').disabled = true;
          };
          </script></body></html>`, {headers: {"Content-Type":"text/html", "Cache-Control":"no-store"}});
      }
      if (path === "/submit" && request.method === "POST") {
        const body = await request.json();
        if (visits.length !== 1 || receipts.length || body.nonce !== visits[0] || body.draft !== "Continuity verified")
          return new Response("Invalid continuity", {status:409});
        receipts.push(body);
        return new Response("OK");
      }
      return new Response("Not found", {status:404});
    },
  });
  return {url: `http://127.0.0.1:${server.port}/`, visits, receipts, stop: () => server.stop(true)};
}

export function verifyBrowserContinuity(calls: any[], run: any, workers: any[], fixture: ReturnType<typeof startBrowserFixture>) {
  const check = (ok: unknown, message: string) => { if (!ok) throw new Error(message); };
  check(run.state === "completed" && run.steps.length === 3, "Browser run incomplete");
  check(fixture.visits.length === 1 && fixture.receipts.length === 1, "Expected one page lifetime and one real receipt");
  const thread = workers[0].thread_id;
  const sessions = calls.filter(c => c.name === "computer_browser_session" && c.ok && c.completed);
  const opens = sessions.filter(c => c.args.action === "open");
  const closes = sessions.filter(c => c.args.action === "close");
  check(opens.length === 1 && closes.length === 1, "Browser must open and close exactly once");
  check(opens[0].thread_id === thread && closes[0].thread_id === thread, "Browser escaped run worker");
  const uses = calls.filter(c => c.name === "computer_computer_use" && c.ok && c.completed);
  const ids = new Set(uses.map(c => c.args.session_id));
  check(ids.size === 1 && ids.has(closes[0].args.session_id), "Browser session changed across steps");
  let previous = -1;
  for (const step of run.steps) {
    const completion = calls.findIndex(c => c.name === "processes_step_update" && c.ok && c.completed && c.args.step_id === step.id && c.args.state === "completed");
    check(uses.some(c => c.thread_id === thread && calls.indexOf(c) > previous && calls.indexOf(c) < completion), "Each step must really use the browser");
    previous = completion;
  }
  check(calls.indexOf(closes[0]) > calls.findIndex(c => c.name === "processes_step_update" && c.ok && c.args?.step_id === run.steps[1].id && c.args?.state === "completed"), "Browser closed before final step");
}
