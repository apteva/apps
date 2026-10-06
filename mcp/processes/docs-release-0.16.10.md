# Processes 0.16.10

Publish process and Return to draft are now beside the status at the top of
process details, visible across every tab. Published, Draft and Paused states
have short explanations. Pause process and editing controls are also available
there, replacing the lifecycle controls previously buried in Operations.

Return to draft is a real state transition, available through the UI, MCP
`draft` and HTTP `POST /processes/{process}/draft`. It stops new manual,
scheduled and triggered runs while preserving immutable procedure versions,
assignment configuration and existing work. Existing runs can finish, and
publishing again restores enabled assignments while leaving paused ones paused.
Returning to draft does not create a version; saving edits creates a new draft
version. Archived procedures remain archived.

Verification: full Go suite, 123 Bun UI/verifier tests, 24 Playwright browser
tests, TypeScript checks and Processes panel build/import checks passed.
Coverage includes desktop and mobile publishing controls, editing and reload,
HTTP/MCP draft transitions, scheduling suspension and restoration, retained run
and version identities, existing work completion, project isolation and queued
trigger invalidation across a draft/publish transition.

The manifest version and runtime source ref both point to 0.16.10.
