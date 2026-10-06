# Processes 0.16.7

Assignments now use process parameters for their execution inputs. The separate
"Page, client, or business" target label has been removed from forms, assignment
and run views, overview widgets, MCP schemas, API output and worker context.
The assignment name, agent, parameter values, scheduling and run controls remain.

Migration 017 removes the obsolete top-level label from saved assignments and
run snapshots. It preserves declared parameters, including a parameter named
`target`, exact run identities and immutable delivery requests for safe retries.
The manifest source ref now matches the release version.

Verification: full Go suite, 123 Bun UI/verifier tests, 21 Playwright browser
tests, TypeScript checks, and Processes panel build/import checks passed.
Regression coverage verifies migration fidelity, parameter-based execution,
absence of the label in MCP and UI, and preservation of saved delivery requests.

Changes are confined to Processes. This release does not upgrade installed apps.
