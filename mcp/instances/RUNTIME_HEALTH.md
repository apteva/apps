# Native runtime control-path diagnostics

Native inspection reports timestamped `health` observations independently:

- `host_reachable` and `administrative_ssh`: whether the host inspection succeeded through administrative SSH; failure does not identify a provider outage.
- `process_running`: the inspected service is running.
- `control_forwarding`: an HTTP response arrived through the control tunnel, including an app error response.
- `admitting`: the app's authenticated status matched the deployment ID and generation. Null means unknown; process or host readiness never implies admission.

Backend `ready` remains startup readiness. Server separately evaluates admission and current deployment ownership before recovery.

Control tunnels keep their assigned loopback ports. Closed listeners are recreated on inspection/open. A timed-out or broken SSH channel open evicts the cached forwarding transport; the next request reconnects. Ordinary forwarding refusals do not evict a healthy connection. Forwarded HTTP mutations are never replayed. Administrative commands and metrics use independent SSH transports.

`instance_open_tunnel` returns timestamped channel attempts/failures under `forwarding`; successful channel opening is not proof of an HTTP response. Inspect native health for that distinction.

## Network evidence

`instance_network_evidence` accepts `id` (registered source), `target_id` (registered destination), `port` (default 443) and `samples` (1–5, default 3). It runs bounded, read-only TCP connection probes with timestamps, durations and failures, captures source-host cumulative TCP counters and obtains a local route observation when available. Python 3 is required on the source; missing utilities produce unavailable evidence rather than invented healthy values.

Run the same target/port from another registered instance for an independent comparison, for example a DigitalOcean host alongside Vultr/Hetzner. No providers are hard-coded and no probes are enabled automatically. Counter deltas are host-wide and do not establish which network/provider caused an interruption. Persist returned timestamped results in the caller's monitoring/audit system when scheduling repeated checks.

Regression tests include real loopback HTTP tunnels, closed-listener recreation, timed-out SSH channel eviction, independent administrative commands, healthy SSH with broken forwarding, fenced app admission, and successful/refused TCP probes.
