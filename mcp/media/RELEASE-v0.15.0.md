# Media v0.15.0 — private executor components

Media offloads processing through App SDK v0.109.0's owned `executor` component.
Server owns deployment, placement, credentials, activation, draining and backend
selection. Media no longer declares Instances or calls its command API, and its
configuration no longer exposes an infrastructure host selector.

The optional worker handles rendering, indexing, read-only analysis, Smart Crop
subprocesses and transcript audio preparation. Media retains its catalog, UI,
provider credentials and durable job outcomes. Existing local processing remains
available. Component placement is operator-selected and unavailable components
never silently fall back to local CPU.

The release adds durable job IDs and exact-plan reconciliation, bounded weighted
admission, confirmed process-session cancellation, passive startup and draining,
restart receipts and acknowledgement-based receipt compaction. Storage transfers
use bounded job capabilities rather than exposing Media's app token to a worker.
Both S3 direct upload and disk-backed resumable upload retain output validation.

The worker source is pinned to an exact tested commit in the parent manifest.
Deploy it explicitly through Server's existing components API, approve its
`net.egress` permission, then select `execution_mode=component`. Existing nonzero
`render_host_id` settings require migration; they never silently select local
execution or cause a direct infrastructure call. See README.md for setup,
dependency requirements and migration details.

Validation includes worker and parent race tests, real worker subprocess
rendering/thumbnail/guarded-audio integration, lost submit response reconciliation,
confirmed cancellation, restart/orphan handling, transfer scope and expiration,
manifest/tool parity, existing Media regressions, Server's component suite and
Linux cross-compilation. These checks use disposable fixtures and do not change
production installations.
