import { check } from "./verify-outcomes";
import type { ConfirmationReport } from "./operator-confirm";

export const EXECUTOR_CONTINUITY = "processes-executor-continuity";
export const CONTINUITY_APPROVAL = "Operator approved: portrait-3.png and portrait-4.png after validation.";
export type FixtureState = { contexts: any[]; operations: any[] };

/** Independent app receipts and thread ownership prevent a model claim from passing. */
export function verifyExecutorContinuity(
  calls: any[], runs: any[], workers: any[], fixture: FixtureState,
  confirmation: ConfirmationReport,
) {
  check(runs.length === 1 && runs[0].workflow && runs[0].state === "completed", "Expected one completed continuity run");
  const run = runs[0];
  check(run.assignment.worker_continuity === "per_executor", "Run did not freeze explicit continuity");
  check(run.steps.map((s: any) => s.key).join(",") === "inventory,portrait_3,portrait_4,validate,approve,publish", "Wrong continuity steps");
  check(workers.length === 1 && workers[0].run_id === run.id && workers[0].agent_id === run.assignment.owner_agent_id, "Expected exactly one executor worker");
  const thread = workers[0].thread_id;
  check(thread && thread !== "main", "Worker must be isolated from main");
  const byKey = Object.fromEntries(run.steps.map((s: any) => [s.key, s]));
  const agentSteps = run.steps.filter((s: any) => s.key !== "approve");
  check(JSON.stringify(byKey.portrait_3.definition.depends_on) === '["inventory"]' && JSON.stringify(byKey.portrait_4.definition.depends_on) === '["inventory"]', "Fixture lost same-agent branching");
  check(new Set(byKey.validate.definition.depends_on).size === 3 && ["inventory", "portrait_3", "portrait_4"].every(k => byKey.validate.definition.depends_on.includes(k)), "Fixture lost validation dependency join");
  check(JSON.stringify(byKey.approve.definition.depends_on) === '["validate"]' && JSON.stringify(byKey.publish.definition.depends_on) === '["approve"]', "Fixture lost validation/approval gates");
  check(byKey.approve.executor.kind === "human" && byKey.approve.updated_by === "operator" && byKey.approve.output === CONTINUITY_APPROVAL, "Human approval was bypassed");
  check(confirmation?.step.id === byKey.approve.id && confirmation.step.run_id === run.id && confirmation.output === CONTINUITY_APPROVAL, "Missing actual HTTP operator confirmation");
  check(fixture.contexts.length === 1 && fixture.operations.length === 5, "Expected one prepared context and five real fixture operations");
  const context = fixture.contexts[0];
  check(context.thread_id === thread && context.agent_id === workers[0].agent_id, "Prepared context belongs to another worker");
  check(fixture.operations.map(o => o.name).join(",") === "prepare,render,render,validate,publish", "Fixture work repeated or skipped");
  check(fixture.operations[1].artifact_id === "portrait-3.png" && fixture.operations[2].artifact_id === "portrait-4.png", "Fixture artifact identities changed");
  const outputs: Record<string, any> = {};
  for (const [i,s] of agentSteps.entries()) {
    check(s.state === "completed" && s.executor.agent_id === workers[0].agent_id && s.target_thread_id === thread && s.updated_by === `agent:${workers[0].agent_id}:${thread}`, `${s.key}: worker ownership changed`);
    check(s.execution_id && s.delivered_at, `${s.key}: missing tracked execution receipt`);
    outputs[s.key] = JSON.parse(s.output);
    const operation = fixture.operations[i], receipt = JSON.parse(operation.receipt_json);
    check(operation.context_id === context.id && operation.tool_call_id, `${s.key}: fixture lost trusted call context`);
    check(outputs[s.key].source_id === context.source_id && outputs[s.key].context_id === context.id, `${s.key}: exact source/context identity lost`);
    check(Object.keys(receipt).length === Object.keys(outputs[s.key]).length && Object.entries(receipt).every(([k,v]) => JSON.stringify(outputs[s.key][k]) === JSON.stringify(v)), `${s.key}: saved output does not match real fixture receipt`);
  }
  check(new Set(agentSteps.map((s: any) => s.execution_id)).size === agentSteps.length, "Step execution receipts were mixed in shared worker");
  check(outputs.portrait_3.artifact_id === "portrait-3.png" && outputs.portrait_4.artifact_id === "portrait-4.png", "Artifact identities changed");
  check(outputs.portrait_3.digest === fixture.operations[1].digest && outputs.portrait_4.digest === fixture.operations[2].digest && outputs.portrait_3.digest !== outputs.portrait_4.digest, "Saved artifact digests changed");
  check(JSON.stringify(outputs.validate.validated) === '["portrait-3.png","portrait-4.png"]' && JSON.stringify(outputs.publish.artifact_ids) === '["portrait-3.png","portrait-4.png"]' && outputs.publish.receipt === "accepted", "Validation or publication lost exact artifacts");
  const completion = (s: any) => {
    const events = s.events.filter((e: any) => e.state === "completed");
    check(events.length === 1, `${s.key}: duplicate/missing durable completion`);
    return events[0].id;
  };
  for (const s of run.steps) for (const dep of s.definition.depends_on) {
    const ready = s.events.find((e: any) => ["ready", "waiting"].includes(e.state));
    check(ready && ready.id > completion(byKey[dep]), `${s.key}: dependency released before ${dep}`);
  }
  const success = (c: any) => c.ok && c.completed;
  const domainCalls = calls.filter(c => c.name.startsWith("test-continuity_"));
  check(domainCalls.length === 5 && domainCalls.every(c => success(c) && c.thread_id === thread), "Fixture calls repeated, failed or escaped worker");
  check(domainCalls.map(c => c.name).join(",") === "test-continuity_prepare,test-continuity_render,test-continuity_render,test-continuity_validate,test-continuity_publish", "Real fixture tool sequence changed");
  let previous = -1;
  for (const [i,s] of agentSteps.entries()) {
    const claim = calls.findIndex(c => success(c) && c.name === "processes_step_claim" && c.thread_id === thread && c.args?.step_id === s.id);
    const claimed = calls[claim];
    // CLI tool telemetry stores argument values as strings (map[string]string).
    const omittedPolicy = claimed?.args?.include_context === false || claimed?.args?.include_context === "false";
    check(i === 0 ? !omittedPolicy : omittedPolicy, `${s.key}: shared policy was not loaded once and reused`);
    const done = calls.findIndex(c => success(c) && c.name === "processes_step_update" && c.thread_id === thread && c.args?.step_id === s.id && c.args?.state === "completed");
    const update = calls[done];
    check(update && update.result_original_bytes <= 1024 && !update.result_truncated, `${s.key}: oversized completion acknowledgement`);
    const ack = JSON.parse(update.result);
    check(ack.step_id === s.id && ack.run_id === run.id && ack.state === "completed" && Number.isInteger(ack.revision) && ack.progress === 100 && typeof ack.next_action === "string" && ack.done === (i === agentSteps.length - 1), `${s.key}: invalid compact completion acknowledgement`);
    check(ack.reread?.tool === "processes_step_get" && ack.reread.args?.process_id === run.process_id && ack.reread.args?.run_id === run.id && ack.reread.args?.step_id === s.id && ack.reread.args?.include_context === true, `${s.key}: invalid receipt recovery reference`);
    check(!["step", "run", "definition", "dependencies", "dependency_outputs", "parameters", "output", "instructions"].some(k => k in ack), `${s.key}: acknowledgement repeated context or receipts`);
    const domain = calls.indexOf(domainCalls[i]);
    check(claim > previous && domain > claim && done > domain, `${s.key}: missing serialized claim/domain/completion`);
    previous = done;
  }
  for (const c of calls) {
    check(!["spawn", "send", "processes_run_update", "processes_step_assign"].includes(c.name), "Model spawned/forwarded or bypassed structured execution");
    check(!/^(code_|web_|fetch_|http_|shell_|exec_|computer_)/.test(c.name), "Unexpected domain tool escaped isolated fixture");
  }
  const firstDone = calls.findIndex(c => success(c) && c.name === "processes_step_update" && c.args?.step_id === byKey.inventory.id && c.args?.state === "completed");
  const discoveries = calls.filter(c => ["search_tools", "tool_search", "discover_tools"].includes(c.name) && success(c) && c.thread_id === thread && /test.continuity/.test(JSON.stringify(c.args).toLowerCase()));
  check(discoveries.every(c => calls.indexOf(c) < firstDone), "Rediscovered fixture tools after prepared context");
  const finish = calls.filter(c => success(c) && c.name === "done" && c.thread_id === thread);
  check(finish.length === 1 && calls.indexOf(finish[0]) > previous, "Worker must finish exactly once after its final step");
  check(calls.some((c,i) => i > previous && success(c) && c.name === "processes_runs"), "Missing final coordinator history verification");
}
