import { Database } from "bun:sqlite";
import { readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findSidecarPort } from "./operator-confirm";
import { check } from "./verify-outcomes";

export const WORKER_CLEANUP = "processes-worker-cleanup";
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
type Target = {
  run_id: string;
  process_id: string;
  project_id: string;
  agent_id: number;
  thread_id: string;
  mode: string;
  state: string;
};
type Runtime = { port: number; key: string; dir: string };
export type CleanupObservation = {
  run_id: string;
  thread_id: string;
  mode: string;
  seenAlive: boolean;
  parked: boolean;
  timerArmedAt?: string;
  wakeAt?: string;
  cancelledAt?: string;
  cleanedAt?: string;
  checkedUntil?: string;
  historyPreserved?: boolean;
  configRemoved?: boolean;
  lateEventRejected?: boolean;
  noLaterHistory?: boolean;
};
// Locate ONLY this disposable CLI server by the exact test project and agent.
// Its Core key is kept in memory and never written to test evidence or logs.
async function findRuntime(t: Target): Promise<Runtime | null> {
  for (const root of new Set([tmpdir(), "/private/tmp"])) {
    for (const name of await readdir(root).catch(() => [])) {
      if (!name.startsWith("apteva-test-")) continue;
      let db: Database | undefined;
      try {
        const dir = join(root, name);
        db = new Database(join(dir, "apteva.db"), { readonly: true });
        const agent: any = db
          .query(
            "SELECT port,core_api_key FROM agents WHERE id=? AND project_id=? AND status='running'",
          )
          .get(t.agent_id, t.project_id);
        if (agent?.port && agent.core_api_key)
          return {
            port: agent.port,
            key: agent.core_api_key,
            dir: join(dir, `instance_${t.agent_id}`),
          };
      } catch {
      } finally {
        db?.close();
      }
    }
  }
  return null;
}
async function core(rt: Runtime, path: string, body?: unknown) {
  return fetch(`http://127.0.0.1:${rt.port}${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: {
      Authorization: `Bearer ${rt.key}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(3000),
  });
}
function targets(path: string): Target[] {
  let db: Database | undefined;
  try {
    db = new Database(path, { readonly: true });
    return db
      .query(
        `SELECT w.run_id,w.agent_id,w.thread_id,r.process_id,r.state,p.project_id,json_extract(r.assignment_json,'$.parameters.mode') mode FROM process_run_workers w JOIN process_runs r ON r.id=w.run_id JOIN processes p ON p.id=r.process_id`,
      )
      .all() as Target[];
  } catch {
    return [];
  } finally {
    db?.close();
  }
}
function cleanupSaved(path: string, t: Target): boolean {
  const db = new Database(path, { readonly: true });
  try {
    return !!db
      .query(
        `SELECT 1 FROM process_worker_cleanup WHERE run_id=? AND thread_id=? AND completed_at<>'' AND attempts>0 AND last_error=''`,
      )
      .get(t.run_id, t.thread_id);
  } finally {
    db.close();
  }
}
function blocked(path: string, t: Target): boolean {
  const db = new Database(path, { readonly: true });
  try {
    return !!db
      .query(
        `SELECT 1 FROM process_step_runs WHERE run_id=? AND step_key='finish' AND state='blocked' AND output='ACCEPTED-OPERATION-789'`,
      )
      .get(t.run_id);
  } finally {
    db.close();
  }
}
export async function observeWorkerCleanup(
  path: string,
  signal: AbortSignal,
  log: (s: string) => void,
): Promise<CleanupObservation[]> {
  const observations = new Map<string, CleanupObservation>();
  const runtimes = new Map<number, Runtime>();
  const parkedSince = new Map<string, number>();
  const history = new Map<string, string>();
  while (!signal.aborted) {
    const current = targets(path);
    for (const t of current) {
      let rt = runtimes.get(t.agent_id);
      if (!rt) {
        rt = (await findRuntime(t)) || undefined;
        if (!rt) continue;
        runtimes.set(t.agent_id, rt);
      }
      let o = observations.get(t.run_id);
      if (!o) {
        o = {
          run_id: t.run_id,
          thread_id: t.thread_id,
          mode: t.mode,
          seenAlive: false,
          parked: false,
        };
        observations.set(t.run_id, o);
      }
      const response = await core(rt, "/threads");
      check(response.ok, "Core roster unavailable during cleanup observation");
      const roster: any[] = await response.json();
      check(
        roster.some((w) => w.id === "main"),
        "Cleanup stopped main",
      );
      const worker = roster.find((w) => w.id === t.thread_id);
      if (worker) o.seenAlive = true;
      if (
        t.mode === "cancel" &&
        worker &&
        blocked(path, t) &&
        !o.timerArmedAt
      ) {
        // First prove the normal app contract parks with no automatic wake.
        if (!worker.next_wake_at) {
          if (!parkedSince.has(t.run_id)) parkedSince.set(t.run_id, Date.now());
          if (Date.now() - parkedSince.get(t.run_id)! >= 2000) {
            o.parked = true;
            // Fault injection into THIS disposable worker only: simulate the old
            // worker that arms pace while blocked, then cancel through Processes.
            const update = await fetch(
              `http://127.0.0.1:${rt.port}/threads/${encodeURIComponent(t.thread_id)}`,
              {
                method: "PUT",
                headers: {
                  Authorization: `Bearer ${rt.key}`,
                  "Content-Type": "application/json",
                },
                body: JSON.stringify({
                  directive:
                    worker.directive +
                    "\nTEST OPERATOR LIFECYCLE FAULT INJECTION: Immediately call pace with sleep=12s exactly once. This deliberately simulates an old blocked worker's pending timer for cancellation testing. Do not call Processes, done, send or any domain tools. After setting this one timer, wait. The test operator will cancel this disposable run before the timer fires.",
                }),
                signal: AbortSignal.timeout(3000),
              },
            );
            check(update.ok, "Failed to inject old paced-worker behavior");
            o.timerArmedAt = new Date().toISOString();
            log(
              `cleanup: observed parked worker; injecting one pending wake for ${t.run_id}`,
            );
          }
        } else parkedSince.delete(t.run_id);
      }
      if (
        t.mode === "cancel" &&
        worker?.next_wake_at &&
        o.timerArmedAt &&
        !o.cancelledAt
      ) {
        o.wakeAt = worker.next_wake_at;
        check(
          Date.parse(o.wakeAt!) > Date.now(),
          "Wake expired before cancellation",
        );
        const port = await findSidecarPort({
          id: "",
          run_id: t.run_id,
          process_id: t.process_id,
          project_id: t.project_id,
        });
        const cancel = await fetch(
          `http://127.0.0.1:${port}/processes/${t.process_id}/runs/${t.run_id}/cancel?project_id=${encodeURIComponent(t.project_id)}`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              reason: "Tier 3 operator cancellation with armed wake",
            }),
            signal: AbortSignal.timeout(3000),
          },
        );
        check(
          cancel.ok,
          `Operator cancellation rejected (${cancel.status}): ${await cancel.text()}`,
        );
        o.cancelledAt = new Date().toISOString();
        log(`cleanup: cancelled ${t.run_id} with armed wake ${o.wakeAt}`);
      }
      if (
        ["completed", "failed", "cancelled"].includes(t.state) &&
        cleanupSaved(path, t)
      ) {
        check(
          !worker,
          `Acknowledged cleanup left worker alive: ${t.thread_id}`,
        );
        const config = await Bun.file(join(rt.dir, "config.json")).json();
        const saved = Array.isArray(config.threads)
          ? config.threads
          : Object.values(config.threads || {});
        check(
          !saved.some((w: any) => w.id === t.thread_id),
          "Cleaned worker still persisted for restart",
        );
        o.configRemoved = true;
        const file = Bun.file(join(rt.dir, "history", `${t.thread_id}.jsonl`));
        check(await file.exists(), "Worker history removed by cleanup");
        o.historyPreserved = true;
        if (!o.cleanedAt) {
          o.cleanedAt = new Date().toISOString();
          const stale = await core(
            rt,
            `/threads/${encodeURIComponent(t.thread_id)}/events`,
            {
              events: [
                {
                  id: `late-${t.run_id}`,
                  message:
                    "Stale ready delivery; this run was already terminated.",
                },
              ],
            },
          );
          check(stale.status === 404, "Delayed event revived a cleaned worker");
          o.lateEventRejected = true;
        }
        // Allow cleanup/session flushes to settle before checking for later work.
        if (
          Date.now() - Date.parse(o.cleanedAt) > 2000 &&
          !history.has(t.run_id)
        )
          history.set(t.run_id, await file.text());
        const deadline = Math.max(
          Date.parse(o.cleanedAt) + 18000,
          o.wakeAt ? Date.parse(o.wakeAt) + 4000 : 0,
        );
        if (Date.now() >= deadline && history.has(t.run_id)) {
          check(
            (await file.text()) === history.get(t.run_id),
            "Worker wrote new history after shutdown",
          );
          o.noLaterHistory = true;
          o.checkedUntil = new Date().toISOString();
        }
      }
    }
    if (
      observations.size === 3 &&
      [...observations.values()].every((o) => o.checkedUntil)
    )
      return [...observations.values()];
    await sleep(250);
  }
  throw new Error(
    `Cleanup observer aborted before all checks: ${JSON.stringify([...observations.values()])}`,
  );
}
export function verifyWorkerCleanup(
  runs: any[],
  cleanup: any[],
  observations: CleanupObservation[],
  notifications: any[] = [],
) {
  check(
    runs.length === 3 && observations.length === 3,
    "Expected three terminal run observations",
  );
  for (const mode of ["complete", "fail", "cancel"]) {
    const r = runs.find((r) => r.assignment.parameters.mode === mode);
    check(r, `Missing ${mode} run`);
    check(
      r.state ===
        { complete: "completed", fail: "failed", cancel: "cancelled" }[mode],
      `Wrong ${mode} outcome`,
    );
    const receipt = {
      complete: "FINAL-RECEIPT-123",
      fail: "ACCEPTED-OPERATION-456",
      cancel: "ACCEPTED-OPERATION-789",
    }[mode];
    check(
      r.steps.find((s: any) => s.key === "finish")?.output === receipt,
      "Saved receipt changed during cleanup",
    );
    const notice = notifications.find((n) => n.run_id === r.id);
    check(
      notice?.delivered_at && notice.attempts > 0 && !notice.last_error,
      "Missing durable coordinator notification",
    );
    const rows = cleanup.filter((c) => c.run_id === r.id);
    check(
      rows.length === 1 &&
        rows[0].attempts > 0 &&
        rows[0].completed_at &&
        !rows[0].last_error,
      "Missing durable cleanup acknowledgement",
    );
    const o = observations.find((o) => o.run_id === r.id);
    check(
      o?.seenAlive &&
        o.configRemoved &&
        o.historyPreserved &&
        o.noLaterHistory &&
        o.lateEventRejected &&
        o.checkedUntil,
      "Missing live worker cleanup evidence",
    );
    if (mode === "cancel")
      check(
        o.parked &&
          o.timerArmedAt &&
          o.wakeAt &&
          o.cancelledAt &&
          Date.parse(o.cancelledAt) < Date.parse(o.wakeAt) &&
          Date.parse(o.checkedUntil) > Date.parse(o.wakeAt),
        "Missing sleeping worker cancellation evidence",
      );
  }
}
