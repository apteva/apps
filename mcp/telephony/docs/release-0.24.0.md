# Telephony 0.24.0

- New tools `telephony_access_policy_get` and `telephony_access_policy_set`
  read and replace the communication access policy (identity providers, users,
  groups, delegations). They do what `GET` and `PUT /access/policy` do, with the
  same validation, revision check and table. This lets an Environments seed
  set up a test adviser. Before, the policy could only be set over HTTP, and
  seeds can only call tools.
  - Only platform callers (API key, panel, Environments seeds) can use them.
    Agents are refused, because an agent that could edit the policy could add
    an identity provider it controls and give itself call access. Bound apps
    keep the scoped resource tools, and application users the softphone API.
  - `revision` is required (0 for the first policy). A stale revision is
    refused with "policy revision changed". Unknown fields are refused, because
    the write replaces the whole policy and a misspelled `users` would remove
    every user.
  - `issuer_install_id` may be a number, which is what
    `{"$runtime_install_id": "auth"}` gives in a seed.
  - Failures are tool errors, so a refused write fails the seed instead of
    passing as a result.
- An `apteva-auth` provider without `url` gets that Auth install's `/me` through
  the platform gateway. This applies to the tool and to `PUT /access/policy`.
  The gateway origin was already the only internal address a provider could
  use.
- Seed order: create the browser destination first. Grants can name only
  destinations that exist. Then set the policy, then create the number and its
  flow.

Not yet checked in a live Environments run: that the gateway path
`/api/apps/auth/_install/N/me` reaches the run's Auth app.
