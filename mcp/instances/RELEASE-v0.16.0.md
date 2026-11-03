# Instances v0.16.0

## Native process observation

Inspection distinguishes a confirmed exit from an unverifiable host. When the
owned systemd unit is `inactive` or `failed` after a start, with no main PID,
an empty cgroup, the expected unit file and no pending restore, the execution
state is `exited` (previously `unknown`). The execution carries `process`:

- running: `pid`, `started_at`
- exited: last `pid`, systemd `result`, `exit_code` or `exit_signal`,
  `started_at`, `exited_at`

All facts come from the existing single `systemctl show` query and are
sanitized in shell and bounded in Go. Units keep `Restart=no`: Instances only
observes; Server owns restart, backoff and activation. Servers that do not
know `exited` treat it like any other non-running state, as before.

## Monitoring CPU and disk

- History ingestion is buffered in memory and committed per host every 10s
  (or at 4096 pending buckets) instead of one transaction per poll. Status and
  latest reads overlay pending data; history and incident reads flush first;
  close flushes.
- `monitoring.db` uses WAL with `synchronous=NORMAL` and a 1MiB
  auto-checkpoint (WAL bounded by `journal_size_limit`).
- Open incidents are re-exported by collectors on every poll with all
  recordings. The summary still follows each flush; the detail blob is
  rewritten at most once a minute and always on close. Incident detail reads
  serve pending recordings directly.
- The local in-process sampler reads CPU every 1s, and every 250ms only while
  the host is viewed (live/history/incident reads in the last minute) or an
  incident is open. Unwatched remote hosts are polled every 10s instead of 2s.
- Status is `stale` after 30s without a sample (was 15s) to fit the slower
  unwatched poll.

Synthetic load (4 hosts, 30 simulated minutes, one open 3000-recording
incident): store CPU 11.6s → 1.65s. Collector binaries are unchanged (0.6.5).
Monitoring of any host, including the local one, can already be disabled per
host with `instance_monitoring` (`enabled: false`).
