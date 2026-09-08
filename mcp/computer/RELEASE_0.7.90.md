# Computer v0.7.90

Computer can now enforce an operator-authorized schedule independently of the
agent's click acknowledgements. A fully confirmed immediate Publish is rejected
when the registered workflow allows only a scheduled commit.

- Persist the resource, commit effect, publication instant and browser timezone
  outside agent arguments. Reopening a session and batching actions do not remove
  the constraint. Agents cannot create, replace or delete operator authorization.
- Verify native date/time fields and the exact saved resource immediately before
  dispatch. Reject a wrong time, resource, timezone or unverifiable commit.
- Preserve stale-target, loading, identity and consequence guards. Block keyboard
  submission bypasses while preserving recognized editing/navigation keys.
- Explain the authorized workflow on rejection without offering copyable dangerous
  confirmation fields as a mechanical way to continue.
- Keep the implementation independent of Patreon, with no added LLM calls and no
  additional browser round trip for the live click check.

## Validation

The saved disposable Patreon/Browserbase tier 3 test passed all six phases:
prepare a draft; reject a fully confirmed immediate Publish; reopen and reject
again; configure scheduling; reject an incorrect time; let the LLM correct the
time and commit Schedule. Independent reload checks verified the same post, exact
title and scheduled time. Full unit/race tests, static checks, real local browser
dispatch regressions and server authorization/proxy tests also passed.

## Required server integration

Use the accompanying Apteva server operator-identity patch, commit
`1f3e158` (standalone server release `v0.27.5`), before enabling this API.
The server strips forged operator headers and attributes real operator requests.
Deploy the server and Computer together; an old server cannot provide this trust
boundary. The standalone server tag is separate from Apteva's umbrella versions.

Protection applies to workflows registered by a trusted operator/task controller;
it is not inferred automatically from task prose. Register the persisted resource
URL after saving/reloading a newly created draft. See
[WORKFLOW_CONSTRAINTS.md](WORKFLOW_CONSTRAINTS.md) for the API and lifecycle.
This release does not automatically update existing installations or register
constraints for production tasks.
