# Processes 0.16.8

The entire process list row now opens the process, including its description,
agent, assignment count and status. A pointer cursor and hover highlight make
selection clear. Keyboard users retain native Enter and Space activation on the
process name, with a visible focus outline around the row.

The built panel is included, and the manifest version and runtime source ref
both point to 0.16.8.

Verification: full Go suite, 123 Bun UI/verifier tests, 22 Playwright browser
tests, TypeScript checks and Processes panel build/import checks passed.
Browser coverage checks pointer and hover feedback, selection from the status
cell, and keyboard activation using Enter and Space.
