# Conversations v0.29.1 validation — 2026-11-24

Reported on local install 1765 (0.29.0): a main-routed chat with "venice
test" showed "Model not recorded yet" in Details right after the agent
replied. The database had the model on that reply (message 2291,
`{"provider":"venice","model":"deepseek-v4-1-flash"}`, thread `main`), so
recording worked. Details fetched `/participants` only when the
conversation id changed or the manage dialog closed.

Change: Details also refetches when `conversation.updated_at` changes,
which moves with every message.

## Checks

- New Playwright test: Details open on a chat with no model; the example
  host records a reply with a model and publishes the message; Details
  shows `deepseek-v4-1-flash` and "Main thread · venice". With the fix
  reverted, the test fails.
- Playwright: 184 passed.
- 148 Bun UI/client tests passed; `go test` passed (version check only;
  no Go code changed since 0.29.0).
