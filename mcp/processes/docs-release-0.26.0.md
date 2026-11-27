# Processes 0.26.0

Any agent in a run's project can now stop it.

- `run_cancel` used to accept only the run's coordinator (the assignment's
  owner agent) or the dashboard operator. An agent that started a run could
  not cancel it. Now any agent in the run's project may cancel it, like any
  may start one. Agents from other projects still can't see the run. Finer
  access belongs in platform grants.
- `run_cancel` also stops single-agent runs. It used to refuse them with
  "use run_update", which only the owner may call. A cancelled single-agent
  run ends with the given reason and queues worker cleanup like any terminal
  run.
- A reason is still required; repeating the same cancellation is a no-op and
  finished runs stay unchanged.
