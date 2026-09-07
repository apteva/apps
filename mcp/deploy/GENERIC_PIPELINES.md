# Generic command builds and releases

Deploy separates source acquisition, command execution, artifact validation,
publishing and availability observation. There are no engine-specific builders.
Existing service/Android/iOS builders and release adapters remain supported.

## Sources

For Code 0.10.0+, set `source_kind: "code"`, `source_ref` to the repository slug,
and `source_extra_json` to a JSON string such as:

```json
{"subdir":"clients/mobile","max_bytes":1073741824,"max_expanded_bytes":2147483648}
```

Alternatively provide `snapshot_id` to use an already captured snapshot. A new
capture can select a subdirectory; the resulting directory becomes the ZIP root.
An existing ID already identifies the selected tree. For shared parent files,
export the repository root and use `pipeline.build_directory` instead.

Deploy saves the receipt before transfer. Authenticated `repos_snapshot_read`
calls fetch at most 1 MiB at a time into a partial file. Identity, offsets, size
and SHA-256 are checked before bounded extraction. A repeated transfer using the
same build directory resumes its partial archive. Cancellation is checked between
RPCs. Missing/expired sources fail without recapturing. The completed archive is
retained with the build, independently of Code's 24-hour handoff window. Build
retention also removes retained archives and partial downloads.

Small legacy inline exports remain readable but provide no immutable revision
contract. New release policies require an attested build from this Deploy version.

Operator limits apply to both Deploy and capsule runners:

- `DEPLOY_SOURCE_MAX_BYTES`: compressed bytes; default 1 GiB.
- `DEPLOY_SOURCE_MAX_EXPANDED_BYTES`: extracted bytes; default 2 GiB.
- Source selections can lower those limits. Code's capture/cache limits must also
  accommodate the source. Existing archive entry/path protections still apply.

## Commands

Put `pipeline` inside `target_config_json` (a JSON string in deployment APIs).
`framework: "command"` runs `build_cmd`; commands must write final files into
`DEPLOY_ARTIFACT_DIR`. Other builders still perform their native packaging after
preparation. For example an exporter can produce a Gradle or Xcode project, then
set `build_directory` to that generated project.

```json
{
  "pipeline": {
    "os": "linux",
    "arch": "amd64",
    "prepare": [
      {"name":"export","command":["sh","ci/export.sh"],"outputs":["generated/project"]}
    ],
    "build_directory":"generated/project",
    "outputs":["application"],
    "tests":[
      {"name":"smoke","command":["./application","--self-test"],"timeout_seconds":120}
    ]
  }
}
```

Stages have unique names, argument-array `command`, optional relative `directory`,
explicit `env`, `timeout_seconds` (default 1800, maximum 86400), and required
`outputs`. Shell evaluation is explicit (`["sh","-c","..."]`). Preparation runs
inside source; tests run inside the final artifact directory. Both receive
`DEPLOY_SOURCE_DIR` and `DEPLOY_ARTIFACT_DIR`. Tests must write reports outside the
artifact directory: modifications to tested files fail the build. The manifest
records passing test names, the pipeline configuration hash and the artifact hash.
Deploy independently retains an attestation in its database before build success.

Commands run with the same trusted-workload authority as existing Deploy builds;
use an isolated capsule runner for source that should not execute on the Deploy
host. Engine/toolchain versions, export options, licensing and command scripts
are project/runner configuration. An OS/architecture mismatch fails explicitly.

Local builds and the bundled capsule runner execute this pipeline. Existing
Codemagic/GitHub integrations receive the target configuration through their build
contract; their workflow must execute compatible stages and return matching
pipeline evidence. Missing evidence fails closed; Deploy does not assume that an
external workflow ran configured tests. `store_upload`/`none` output modes cannot
substitute for retained tested artifacts when a pipeline/policy is configured.

## Target connections

`connections` maps manifest roles to explicitly selected connection IDs:

```json
{"connections":{"app_store":123,"play_store":456,"publisher":789}}
```

Select IDs from the installation's authorized bindings for that role. An invalid
explicit ID fails; it never falls back to another account. The mobile store roles
now accept multiple bindings. The optional `publisher` role accepts publisher
integrations, including the existing `steamworks` catalog integration. Credentials
stay in the connection service. Release records retain account and application
identity; subsequent synchronization uses that saved selection. Revoked bindings
are rejected. Switching an attested build to another publisher requires a new
build/authorization.

## Generic publishers

Use `target_kind: "artifact"` for artifacts published through integration tools.
See `examples/steamworks-target.json` for the existing Steamworks connector.
The orchestration supports any authorized integration with compatible tools.

A publisher declares `role`, `provider` (catalog slug), `identity`, optional upload
commands/receipt, a `publish` integration action and an optional `observe` action.
Upload commands run on the Deploy execution host with a scratch copy of the
attested files. Set up tools such as SteamCMD on that host, or make the command
invoke your chosen authenticated remote uploader. They must upload without
promoting a branch. They receive the scratch artifact path through
`DEPLOY_ARTIFACT_DIR`. Write the JSON receipt in the command working directory,
separate from artifact contents. The receipt can contain an exact external build
ID. Changes to artifact contents during upload block subsequent publication.

If an external build runner already uploaded, omit `upload` and set `receipt_file`
to the corresponding receipt within the retained artifact. Never identify an
upload by selecting whichever remote build happens to be newest.

Integration action inputs use full-string references such as `$channel`,
`$identity.appid`, `$upload.buildid`, `$build_id` and `$artifact_sha256`. References
preserve JSON types; SteamID64 should remain a string. Command arguments do not
interpolate these references. Publisher keys are resolved by the platform, never
passed as command arguments. Upload authentication (for example SteamCMD's cached
build-account session) is separate from publisher Web API authentication.

An accepted call is not availability evidence. Configure `pending_statuses:[201]`
for Steam Mobile confirmation. Observers use JSON pointers and equality checks;
all checks must match. Pointer segments may reference a variable (`/$channel/`).
Numeric array indexes are supported. Configure pointers against the actual
provider response for your account; an unknown/missing shape remains unconfirmed.
`published` checks prove a channel assignment. Separate `available` checks should
only be configured where the API actually proves the declared audience/region can
access the release. Steam branch assignment alone does not prove first store
release, purchases, entitlement or every region's availability.

Promotions reuse the retained artifact and upload receipt for the same publisher
account/application. `deploy_promote` accepts `id`, `release_id`, `target_channel`
and optional `target_environment`. `deploy_release_sync` refreshes observation
from the frozen release configuration. The background worker refreshes pending
and tracked releases. HTTP success never directly sets the release live.

Upload/publish phases are saved before external effects. After an ambiguous
failure or crash, Deploy does not automatically repeat a mutation. Synchronize
the existing release to reconcile a completed request. If its upload receipt was
lost, explicitly stop the deployment and verify the provider outcome before a
new upload. Stopping local tracking cannot undo an already accepted store action.

## Policies and approval

```json
{
  "release_policy": {
    "version":"1",
    "auto_channel":"internal",
    "approver_agent_ids":[123],
    "channels": {
      "internal":{"automatic":true,"required_tests":["smoke"]},
      "production":{
        "from_channel":"internal",
        "required_tests":["smoke"],
        "minimum_age_seconds":3600,
        "require_approval":true
      }
    }
  }
}
```

Every permitted channel must have an explicit rule. `auto_channel` requests a
release when a build succeeds; `automatic:true` permits that action. Promotions
require the same build and unchanged artifact, matching pipeline evidence, source
channel history and the configured waiting period. `require_available:true` also
requires positive availability evidence no older than
`maximum_observation_age_seconds` (default 600). Leave it off when the store does
not provide that evidence. Google Play track commits are not treated as proof of
download availability. Existing Apple observations expose availability separately.

For approval, an explicitly allowlisted agent (`approver_agent_ids`) or delegated
subject (`approver_subject_ids`) calls `deploy_release_approve` with deployment
`id`, `build_id`, environment and channel. The first call returns a digest without
approving; the approver submits that exact `approval_digest`. The platform-owned
caller identity is recorded. Missing or unauthorized caller identities fail
closed. Changing the artifact, target configuration, account, channel, policy or
release options invalidates approval. Treat policy editing as administrative
configuration, protected by the platform's existing tool/API access controls.

## Verification scope

Regression tests cover transfer interruption and resume, snapshot identity,
expiry, limits, binary permissions, generated build directories, artifact tests,
path escapes, cancellation, selected connections, unchanged artifacts, approval
invalidation and Steam-style 201/branch observation behavior. An integration test
boots real Code 0.10.0, interrupts a multi-chunk Deploy transfer, edits source and
verifies the original subdirectory snapshot. Provider tests use synthetic responses;
real store uploads require configured publisher/build accounts.
