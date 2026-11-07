# Multiple direct account connections

The Add Account dialog offers the same two choices for native Pinterest,
YouTube, Facebook, Instagram, X and other direct adapters:

1. Use a named active connection from this project. Linked account names and
   brand/profile labels help identify it. This does not open an OAuth popup.
2. **Connect another account** starts fresh OAuth and keeps the selected Social
   brand/profile. Sign in to the desired account in the provider's window.

Facebook/Instagram still select their publishing identity after authorization.
Pinterest boards remain child destinations, separate from account selection.
Zernio continues to use its provider connection/import flow.

## API

`account_list_connections(platform)` and authenticated
`GET /accounts/connections?platform=...` return active project connections and
linked Social account/profile labels, with no credentials or provider mutations.

`account_add(platform, connection_id, profile_id?)` explicitly reuses a validated
connection. `force_new=true` starts fresh OAuth; it cannot be combined with
`connection_id`. Both inputs are forwarded by `POST /accounts/start`.

For backward compatibility, calls without either selector retain single-grant
reuse on platforms that previously supported it. Multiple reusable grants return
`status=select_connection` and choices; no pending row or OAuth call is created.
The platform listing remains authoritative: connection IDs must belong to the
selected integration/project and be active, even if Social retains stale rows.

Finalizing a previously linked account cannot silently move it to another
brand/profile. Use the explicit account Move action instead. New connections
create separate accounts; each retains its own destination options and history.

This is entirely in Social. No server, SDK or integration JSON change is needed.
