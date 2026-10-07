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
