# Appcircle capsule workflow

Create an iOS or Android build profile pointing to this adapter repository. Create a dedicated workflow and build configuration for each Deploy target, with these steps in order:

1. Git Clone (the adapter repository).
2. Custom Script, Execute With `bash`:

   ```bash
   set -euo pipefail
   cd "$AC_REPOSITORY_DIR"
   python3 scripts/run_mobile.py
   ```

3. Export Build Artifacts, `AC_UPLOAD_DIR=$AC_OUTPUT_DIR`, `AC_DISABLE_UPLOAD_ON_FAIL=true`.

Keep "continue on failure" disabled for the custom script. Disable Appcircle auto-signing and automatic publishing; the shared runner uses Deploy's signing identity, reserved versions, pipeline tests and release contract. Use a macOS worker for Apple targets; Android can use Linux or macOS.

Select the bound Appcircle connection and configure:

```json
{
  "connection_id": 123,
  "profile_id": "PROFILE_UUID",
  "configuration_id": "CONFIGURATION_UUID",
  "workflow_id": "WORKFLOW_UUID",
  "branch_id": "ADAPTER_BRANCH_UUID",
  "source_mode": "bundle",
  "artifact_mode": "file"
}
```

`commit_id` may replace `branch_id` to pin the adapter commit. The game/app's actual source comes from the signed Deploy capsule, independent of the adapter repository. Run Deploy's mobile signing setup after switching providers. Deploy adds a dedicated secret variable group to the chosen configuration without replacing unrelated settings. Build-start overrides contain public contract variables only.

Appcircle's artifact API downloads a ZIP of all output files. Deploy extracts the named `apteva-build.zip`, then validates the same final artifact evidence used by other providers.
