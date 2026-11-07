# Optional membership audiences

Social discovers memberships per connected account through that account's existing
integration connection. `account_audience_capabilities` and
`account_membership_levels` are optional tools. Opening Accounts, Compose, or Home
never refreshes membership discovery automatically. `refresh=true` explicitly
queries the provider; cached tiers expire after 24 hours. Discovery errors,
permission failures, empty results, and stale cached results remain distinct.

Existing `post_create` requests keep their current behavior. To opt in, use a
per-target audience:

```json
{
  "mode": "publish",
  "body": "Members video",
  "media_storage_ids": [123],
  "targets": [{
    "social_account_id": 7,
    "audience": {"type": "members", "level_ids": ["tier-id"]}
  }]
}
```

Omit `level_ids` for members generally. Specific tier IDs require successful,
fresh discovery for the same account and connection. At delivery, explicitly
selected tiers are refreshed if their cached validation has expired; ordinary
posts and membership targets without tier IDs never trigger this refresh. Membership options can be
saved in drafts and scheduled posts; approval and revision locking still apply.
Changing membership audiences after upload requires a new draft or platform UI.

## YouTube

The adapter uses `youtube-api`, not a separate membership integration or OAuth
connection. The existing connection may need OAuth reauthorization for
`youtube.channel-memberships.creator`. Google additionally restricts this API to
approved channels. Tier discovery returns IDs and names, not prices.

YouTube's public video API cannot select members-only access or tiers. Social
stages membership videos as **unlisted**. Anyone holding the link can watch until
an operator changes access in YouTube Studio. The target and parent post show
`awaiting_audience_setup`; they are not counted as completed membership
publications. Post details links to Studio and requires explicit confirmation of
members-only access and the requested tiers. Confirmation records
`audience_status=human_confirmed`, time and actor; it is an operator attestation,
not provider API verification. Confirmation never assigns a native publication timestamp; the original upload and independent membership dates retain their meanings. The confirmation route is authenticated and is not
an MCP tool.

## Upload dependency and recovery

Membership uploads require the updated YouTube integration JSON containing
`upload_video_chunk`, `upload_video_status`, and init MIME/size header mappings.
The server must forward `Range` and `Retry-After`, and honor `body_none:true` for
status probes. This release does not modify or deploy the server or publish the
integration catalog. Ordinary publishing retains its existing path.

Membership video bytes use bounded 4 MiB authenticated integration requests;
credentials remain in the server. Videos are temporarily spooled to disk, capped
at 32 GiB, and deleted after each attempt. Persisted upload sessions are tied to
the connection, metadata, and exact video digest. Retries probe that same session
and resume from the reported offset. Provider Retry-After delays are retained.
Ambiguous completion, missing progress headers, and interrupted processes do not
start replacement uploads. Expired sessions require checking the remote outcome
before explicitly creating a replacement post. Completed remote IDs are saved
before thumbnail operations; thumbnail errors do not create another video.

## Additional integrations

Register an adapter by provider and platform in `audienceAdapters`. It supplies
capabilities, account-scoped discovery, and optional native or manual publishing.
The MCP target contract, cache, composer, and confirmation lifecycle are shared.
Unsupported destinations reject explicit membership audiences before publishing.
