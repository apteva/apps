# Processes 0.16.15

Processes now ensures its current MCP installation is attached to each executor
before dispatch. Worker threads inherit the executor's spawnable domain tools,
so they can claim their Processes step and use integrations such as WeatherAPI.
Unclaimed recovery and legacy-worker handoffs reconcile the existing worker's
profile without replacing its history or immutable delivery envelopes. Failed
provisioning records a durable blocker and uses backoff instead of waking an
unusable worker.

Procedure steps are editable directly in the flow's side panel. Save changes
and Cancel remain visible while scrolling on desktop. Saving a published
procedure creates a new draft version in one transaction; existing runs retain
their frozen instructions, step identities and receipts. Historical versions
remain read-only. Validation, version conflicts and failed saves preserve the
operator's edits, and successful saves retain their confirmation even if a
subsequent refresh fails.

The UI save-as-draft operation is operator HTTP only. MCP update retains its
existing pause/edit lifecycle. The new platform.mcp.attach permission lets
Processes attach its own current tools within the platform's installation and
project checks; upgrades must approve that permission before dispatch.

Verification:
- Go short tests, race checks, vet, and real-sidecar workflow/restart checks.
- 124 UI and saved-evidence verifier tests; TypeScript and panel import checks.
- All 29 browser tests, including desktop/mobile inline edits, cancel, retry,
  historical reads and persisted timing rules.
- GPT-6.1 Sol tier 3: six-step executor continuity with one preparation, exact
  receipts, a validation join and an HTTP approval gate; and the final
  three-step sequential-worker smoke test.
- The stalled local run was recovered using its original worker: WeatherAPI
  completed, and Pushover remained waiting for separate operator approval.
