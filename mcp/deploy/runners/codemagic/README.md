# Codemagic adapter

This directory is the source of truth for Deploy's Codemagic bootstrap
workflow. Codemagic requires an application backed by a Git repository, so a
single shared bootstrap repository must contain this `codemagic.yaml`. User
application source is never read from that repository. Deploy supplies it as a
signed, short-lived source capsule using the `apteva.build/v1` contract.

The same `apteva-mobile-capsule` workflow accepts iOS, macOS, and Android targets.
`artifact_mode=file` or `bundle` returns the signed IPA/PKG/AAB to Deploy, which
publishes it through the bound App Store Connect or Google Play integration.
`artifact_mode=store_upload` lets the build provider upload directly when the
Deploy host cannot do so, then Deploy adopts the store result.

The bootstrap repository contains no per-app source or secrets. Signing and
publishing credentials are imported from provider secret groups selected in
the deployment environment.

Android jobs implement `apteva.mobile-signing/v1`: they decode the temporary
Deploy-owned PKCS#12 key, require every signing variable, sign and verify the
AAB, publish the actual certificate SHA-256 in the artifact manifest, and
remove the temporary keystore. Deploy verifies that artifact independently
before accepting the build.

Apple source bundles with `project.yml` and no existing selected Xcode project
or workspace install XcodeGen **2.46.0** from the official release archive,
verify its SHA-256, print and validate its version, then generate the project.
The extracted binary keeps its bundled setting presets and does not depend on
Homebrew or the runner's PATH. Existing `.xcodeproj` and `.xcworkspace` inputs
skip installation and generation; workspaces are preferred during discovery.

Run the adapter regressions with `python3 -m unittest discover -s tests -v`
from this directory. Keep the workflow and tests in sync with Deploy's
`mcp/deploy/runners/codemagic/` template when publishing adapter changes.


## Generic native pipelines (Deploy 0.28.0)

The adapter decodes `APTEVA_BUILD_SPEC_B64` and selects the capsule's
`build_subdir` before preparation. The capsule keeps sibling dependencies in
place. `APTEVA_SOURCE_BUILD_SUBDIR`, when supplied, must match the build spec.
Recipes supply argument arrays, directories, environment values, timeouts and
outputs; no engine or game logic belongs in this repository.

For a pipeline, the bootstrap downloads checksum-pinned Darwin Deploy runner
binaries from `deploy/v0.28.0`. `--cloud-pipeline prepare`, `finalize` and
`verify` share Deploy's local `buildWithPipeline` execution, tree hashing and
evidence validation. Native archive/sign/export runs inside
`pipeline.build_directory`; `DEPLOY_SOURCE_DIR` remains the app source root
and `DEPLOY_ARTIFACT_DIR` is the final artifact directory. Stage directories and
outputs are relative to the source root for preparation and the artifact root
for tests. A recipe requiring shell syntax must explicitly invoke a shell.

A native pipeline declares exactly one IPA, PKG or AAB as its primary output.
That name is used for signing, the manifest, tests and upload. Additional
outputs are retained in the returned ZIP, including their permissions and safe
relative links. Tests run after signing and before upload. Any failing test or
change to the artifact tree or manifest stops the job. Evidence records the
configuration hash, artifact hash, passing test names and UTC completion time.
Deploy validates and retains this evidence before accepting success and again
before releasing a pipeline build, including targets without release policies.

The bootstrap installs Bun when a Bun lockfile/package-manager declaration or
`software_versions.bun` requires it (default pinned version `1.3.13`). Rust is
installed when Cargo/toolchain manifests or `software_versions.rust` require it;
the source's exact toolchain version is honored (fallback `1.98.1`). iOS adds
`aarch64-apple-ios`; macOS adds both Darwin architectures. Requested Rust
components/targets are honored, with additional comma-separated targets from
`software_versions.rust_targets`. Explicit versions override source defaults.
Bun and Rust versions must be exact; conflicting source pins require an
explicit runner version. Requested Xcode, Node, Python, CocoaPods, Java, Ruby
and Flutter versions must match the runner. Unsupported version keys fail
clearly. Jobs without a pipeline retain the existing native packaging path.

`go test ./...` in Deploy compiles the runner and runs the full hermetic native
workflow regressions. To run these from the adapter repository, set
`TEST_DEPLOY_PIPELINE_RUNNER` to a compiled Deploy binary and run
`python3 -B -m unittest discover -s tests -v`.
