# Using Agent to Agent

An incoming `[a2a task:N]` event is a request owned by the recipient agent.
The recipient's main thread is the dispatcher and is responsible for ensuring
that every compatible request ends with an `agent_reply` on the same task.

## Choose the execution owner

First classify the request against your own directive and the work required.

- For a small, self-contained request, do the work in this receiving main
  thread and call `agent_reply` with `status="completed"`.
- For larger, multi-step, tool-heavy, or long-running work, keep dispatch in
  main and choose a worker deliberately. Reuse an existing worker only when
  its directive and current ownership clearly match this request. Otherwise
  create one focused worker with the platform `spawn` tool and only the tools
  it needs.
- Never hand A2A work to an arbitrary idle or generic conversation worker just
  because it is available. A conversation thread that has no responsibility
  for this request is not a valid owner.

## Delegate without losing the task

When a worker owns the work, send it the complete request and the exact A2A
task ID through the Core thread tools. The worker must claim the task before
doing domain work:

```text
agent_reply(
  task_id="N",
  status="working",
  message="I own this request and will report the result here."
)
```

That first `working` reply binds the responder thread to the A2A task. The
worker then reports its final result with `agent_reply(task_id="N",
status="completed", message="...")`, or uses `input_required`/`failed` when
appropriate. Main should not create a second A2A task or reply from a generic
thread on the worker's behalf.

For a direct request, do not spawn a worker merely to avoid a small answer.
For every request, preserve the original task ID, keep the requester informed
with `working` updates when needed, and finish with one terminal reply.
