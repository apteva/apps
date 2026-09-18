/**
 * Mid-run operator confirmation for tier 3.
 *
 * `apteva test` has no mid-run hook: seed_mcp_calls run before the agent starts
 * and cleanup_mcp_calls after it stops, so neither can release a step while the
 * run is parked. This watcher runs beside the CLI child instead. It polls the
 * scenario's own app database until the human step reports `waiting`, then
 * completes it over the sidecar's HTTP surface as the project operator.
 *
 * The confirmation goes through real HTTP rather than a direct database write so
 * the app's own authorization runs: executorIsActor accepts `operator` only for
 * a human executor, completion still requires output evidence, and the reconcile
 * loop — not the test — is what releases the next step to its agent.
 */
import { Database } from "bun:sqlite";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

type WaitingStep = {
  id: string;
  run_id: string;
  process_id: string;
  project_id: string;
};

/** The CLI assigns APTEVA_APP_PORT from pickFreePort, so the port is discovered, never pinned. */
async function findSidecarPort(step: WaitingStep): Promise<number> {
  const lsof = Bun.spawn(["lsof", "-nP", "-iTCP", "-sTCP:LISTEN"], {
    stdout: "pipe",
    stderr: "ignore",
  });
  const listing = await new Response(lsof.stdout).text();
  await lsof.exited;
  const ports = new Set<number>();
  for (const line of listing.split("\n")) {
    const columns = line.trim().split(/\s+/);
    if (columns[0] !== "sidecar") continue;
    const port = Number(columns[columns.length - 2]?.split(":").pop());
    if (Number.isInteger(port) && port > 0) ports.add(port);
  }
  for (const port of ports) {
    try {
      const response = await fetch(
        `http://127.0.0.1:${port}/processes?project_id=${encodeURIComponent(step.project_id)}`,
        { signal: AbortSignal.timeout(2000) },
      );
      if (!response.ok) continue;
      const body: any = await response.json();
      // Several sidecars can listen at once; match the process this run belongs to.
      if (body.processes?.some((p: any) => p.id === step.process_id)) return port;
    } catch {
      continue;
    }
  }
  throw new Error(
    `No sidecar serving process ${step.process_id} among listening ports [${[...ports]}]`,
  );
}

function findWaitingStep(dbPath: string, stepKey: string): WaitingStep | null {
  let db: Database;
  try {
    db = new Database(dbPath, { readonly: true });
  } catch {
    return null; // the sidecar has not created its database yet
  }
  try {
    return (
      (db
        .query(
          `SELECT s.id, s.run_id, s.project_id, r.process_id
             FROM process_step_runs s
             JOIN process_runs r ON r.id = s.run_id
            WHERE s.step_key = ? AND s.state = 'waiting'`,
        )
        .get(stepKey) as WaitingStep | null) || null
    );
  } catch {
    return null; // migrations may not have run yet
  } finally {
    db.close();
  }
}

export type ConfirmationReport = {
  step: WaitingStep;
  port: number;
  confirmedAt: string;
  output: string;
};

export async function confirmWhenWaiting(
  dbPath: string,
  options: {
    stepKey: string;
    output: string;
    signal: AbortSignal;
    pollMs?: number;
    log?: (message: string) => void;
  },
): Promise<ConfirmationReport> {
  const { stepKey, output, signal } = options;
  const pollMs = options.pollMs ?? 2000;
  const log = options.log ?? (() => {});
  while (!signal.aborted) {
    const step = findWaitingStep(dbPath, stepKey);
    if (!step) {
      await sleep(pollMs);
      continue;
    }
    log(`operator: step ${stepKey} is waiting (${step.id}); confirming`);
    const port = await findSidecarPort(step);
    const response = await fetch(
      `http://127.0.0.1:${port}/processes/${step.process_id}/runs/${step.run_id}` +
        `/steps/${step.id}?project_id=${encodeURIComponent(step.project_id)}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ state: "completed", output }),
      },
    );
    const text = await response.text();
    if (!response.ok) {
      throw new Error(`Operator confirmation rejected (${response.status}): ${text}`);
    }
    const confirmedAt = new Date().toISOString();
    log(`operator: confirmed ${step.id} on port ${port}`);
    return { step, port, confirmedAt, output };
  }
  throw new Error(
    `Operator confirmation aborted: step ${stepKey} never reported waiting`,
  );
}
