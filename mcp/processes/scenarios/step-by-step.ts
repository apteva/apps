import { Database } from "bun:sqlite";
import { findSidecarPort } from "./operator-confirm";
import { check } from "./verify-outcomes";
import { PARALLEL_APPROVAL } from "./ai-parallel-steps";
export const STEP_BY_STEP = "processes-step-by-step";
export type ReleaseObservation = {
  step_id: string;
  key: string;
  held_at: string;
  released_at: string;
  response: any;
  duplicate: any;
};
export type ControlReport = {
  observations: ReleaseObservation[];
  approval: any;
};
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// Reads app storage independently, but all mutations use its real HTTP surface.
// Waiting twice before release proves a ready record survives reconciliation.
export async function controlStepByStep(
  dbPath: string,
  signal: AbortSignal,
  log: (message: string) => void,
): Promise<ControlReport> {
  const observations: ReleaseObservation[] = [];
  let port = 0,
    approval: any;
  while (!signal.aborted) {
    let db: Database | undefined;
    try {
      db = new Database(dbPath, { readonly: true });
    } catch {
      await sleep(500);
      continue;
    }
    let steps: any[] = [];
    try {
      steps = db
        .query(
          `SELECT s.*,r.process_id FROM process_step_runs s JOIN process_runs r ON r.id=s.run_id ORDER BY s.position`,
        )
        .all() as any[];
    } catch {
    } finally {
      db.close();
    }
    if (steps.length === 0) {
      await sleep(500);
      continue;
    }
    if (steps.every((s) => s.state === "completed"))
      return { observations, approval };
    const held = steps.filter(
      (s) => s.state === "ready" && !s.released_at && s.step_key !== "prepare",
    );
    if (held.length === 0) {
      await sleep(500);
      continue;
    }
    const heldAt = new Date().toISOString();
    for (const s of held)
      check(
        !s.delivered_at && !s.target_thread_id && !s.output,
        `${s.step_key}: premature held work`,
      );
    await sleep(2000);
    const before = new Database(dbPath, { readonly: true });
    for (const s of held) {
      const fresh = before
        .query("SELECT * FROM process_step_runs WHERE id=?")
        .get(s.id) as any;
      check(
        fresh.state === "ready" &&
          !fresh.released_at &&
          !fresh.delivered_at &&
          !fresh.output,
        `${s.step_key}: held state changed without controller`,
      );
    }
    before.close();
    if (!port) port = await findSidecarPort(held[0]);
    for (const s of held) {
      const base = `http://127.0.0.1:${port}/processes/${s.process_id}/runs/${s.run_id}`;
      const query = `?project_id=${encodeURIComponent(s.project_id)}`;
      const read: any = await (await fetch(base + query)).json();
      check(
        read.run.control_mode === "step_by_step" &&
          read.run.eligible_steps.some((x: any) => x.id === s.id),
        `${s.step_key}: missing exact eligible ID`,
      );
      const body = {
        step_id: s.id,
        idempotency_key: `operator-release-${s.id}`,
      };
      const release = async () => {
        const res = await fetch(base + "/advance" + query, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
        check(
          res.ok,
          `${s.step_key}: advance rejected ${res.status} ${await res.clone().text()}`,
        );
        return res.json() as Promise<any>;
      };
      const response = await release(),
        duplicate = await release();
      check(
        duplicate.duplicate === true && duplicate.step_id === s.id,
        `${s.step_key}: duplicate release mismatch`,
      );
      observations.push({
        step_id: s.id,
        key: s.step_key,
        held_at: heldAt,
        released_at: response.step.released_at,
        response,
        duplicate,
      });
      log(`operator: released ${s.step_key} after held-state check`);
      if (s.step_key === "approve") {
        const res = await fetch(base + `/steps/${s.id}` + query, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            state: "completed",
            output: PARALLEL_APPROVAL,
          }),
        });
        check(res.ok, `approval rejected ${res.status}`);
        approval = {
          step: {
            id: s.id,
            run_id: s.run_id,
            process_id: s.process_id,
            project_id: s.project_id,
          },
          port,
          confirmedAt: new Date().toISOString(),
          output: PARALLEL_APPROVAL,
        };
      }
    }
  }
  throw new Error("Step-by-step controller stopped before completion");
}

export function verifyControlEvidence(
  runs: any[],
  advances: any[],
  report: ControlReport,
) {
  check(
    runs.length === 1 &&
      runs[0].state === "completed" &&
      runs[0].assignment.control_mode === "step_by_step",
    "Missing frozen controlled run",
  );
  const r = runs[0];
  check(
    advances.length === r.steps.length &&
      new Set(advances.map((a) => a.step_id)).size === r.steps.length,
    "Duplicate or missing durable releases",
  );
  check(
    report.observations.length === r.steps.length - 1,
    "Missing HTTP hold/release observations",
  );
  const byKey = Object.fromEntries(r.steps.map((s: any) => [s.key, s]));
  for (const s of r.steps) {
    const release = advances.find((a) => a.step_id === s.id);
    check(
      release?.run_id === r.id && release.created_at === s.released_at,
      `${s.key}: wrong durable release identity`,
    );
    check(
      Date.parse(s.released_at) <= Date.parse(s.completed_at),
      `${s.key}: work completed before release`,
    );
    if (s.delivered_at)
      check(
        Date.parse(s.released_at) <= Date.parse(s.delivered_at),
        `${s.key}: work delivered before release`,
      );
    for (const dep of s.definition.depends_on)
      check(
        Date.parse(byKey[dep].completed_at) <= Date.parse(s.released_at),
        `${s.key}: release preceded dependency receipt`,
      );
    if (s.key !== "prepare") {
      const o = report.observations.find((o) => o.step_id === s.id);
      check(
        o &&
          Date.parse(o.released_at) - Date.parse(o.held_at) >= 1900 &&
          o.response.step_id === s.id &&
          o.duplicate.duplicate === true,
        `${s.key}: missing stable held-state/duplicate evidence`,
      );
      check(
        release.actor === "operator",
        `${s.key}: worker released its own step`,
      );
    } else
      check(
        release.actor.startsWith(`agent:${r.assignment.owner_agent_id}:`) &&
          release.request_key === "release-prepare",
        "Initial MCP controller release missing",
      );
  }
  check(
    report.approval?.step.id === byKey.approve.id,
    "Missing HTTP human approval",
  );
}
