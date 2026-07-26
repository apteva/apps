# Apteva Codemagic Runner

This directory is the source for the fixed repository used by Deploy's
Codemagic `source_mode: bundle`. Codemagic requires `codemagic.yaml` at the
repository root, so publish the contents of this directory as the root of the
Apteva-owned runner repository.

Deploy supplies the source URL, SHA-256, size, target configuration, and build
identity through build API environment variables. The runner downloads the
source without logging the signed URL, verifies it before extraction, rejects
unsafe ZIP entries, and builds in an isolated temporary directory.

Use `target_config_json.smoke_only: true` for an unsigned iOS Simulator compile.
A signed IPA requires the App Store Connect variables in a Codemagic encrypted
variable group:

- `APP_STORE_CONNECT_ISSUER_ID`
- `APP_STORE_CONNECT_KEY_IDENTIFIER`
- `APP_STORE_CONNECT_PRIVATE_KEY`
- `CERTIFICATE_PRIVATE_KEY`

Example Deploy backend configuration:

```json
{
  "app_id": "CODMAGIC_RUNNER_APP_ID",
  "workflow_id": "apteva-ios-runner",
  "branch": "main",
  "source_mode": "bundle",
  "artifact_mode": "file",
  "artifact_name": "ipa",
  "groups": ["appstore_credentials"]
}
```

For a local Apteva test, configure its public URL to an HTTPS tunnel reachable
by Codemagic, or set `source_base_url` in the backend configuration to that
tunnel origin.

