# Codemagic adapter

This directory is the source of truth for Deploy's Codemagic bootstrap
workflow. Codemagic requires an application backed by a Git repository, so a
single shared bootstrap repository must contain this `codemagic.yaml`. User
application source is never read from that repository. Deploy supplies it as a
signed, short-lived source capsule using the `apteva.build/v1` contract.

The same `apteva-mobile-capsule` workflow accepts iOS and Android targets.
`artifact_mode=file` or `bundle` returns the signed IPA/AAB to Deploy, which
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
