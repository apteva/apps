# Managed remote deployment

Telephony 0.12.15 publishes the standard SDK `runtime.package` lifecycle
capabilities. Server uses its existing Go source entry point, frontend build
script, `native/` runtime directory and declared migrations. Operators select
only the release and a supported backend target; Server determines the target
platform, supplies its reachable gateway, prepares/caches/verifies the package,
delivers it and runs readiness, activation and draining.

No Instances or Domains integration is required by Telephony. Instances 0.7.1
advertises package delivery and artifact-independent host inspection; any other
backend implementing those contracts can be selected. Supporting Server code is
currently local/unreleased. Existing installations are not migrated by this
release. Public SIP/WebRTC network setup remains an explicit separate operation.
