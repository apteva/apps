# Fleet 0.10.7 — stop intent and residual workers

Based on the latest Fleet 0.10.6 sources, with app-sdk 0.75.0 (verified descendant of 0.74.1).

- Persist a recovery-required operation before signalling any local/hosted tenant. Unknown SSH completion, partial stop, controller restart or failed stopped-state persistence cannot release the health/respawn fence.
- Release the fence only after the stopped status has been persisted. Existing explicit `recover-operation` stops and verifies recorded endpoints, preserves data, and leaves the tenant stopped for an explicit Start.
- Scan exact tenant ownership across sessions, not only the current launch session. Recognize legacy managed apteva-core binaries by both the managed versions path and exact numeric tenant instance working directory.
- Exclude the SSH control ancestry and independent static deployment process groups. Revalidate ownership before signalling individual PIDs; never issue broad process-name or process-group kills.

Validation: full Go tests, race detector and vet; stop-error + failed verification + subsequent health/respawn + controller-restart regression; explicit recovery; successful stop cleanup; SSH shell syntax. Linux process tests passed on the locally registered DigitalOcean test-render host, using RAM-backed temporary files: separate tenant process groups stopped; older-session workers without PID files stopped; unrelated tenant and static deployment protected; repeated stop successful.

This repairs the observed unsafe recovery behavior. It does not establish the original cause of the production SSH interruption. No production install, DNS or migration operation is part of this release.
