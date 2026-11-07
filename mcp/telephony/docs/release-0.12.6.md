# Telephony 0.12.6

Cumulative release retaining 0.12.5's SIP playback, browser audio, recordings,
telemetry, recovery and admission controls.

## Automatic outbound SIP

The Calls panel and headless `outboundTransport` API now offer automatic setup
for Telnyx and Twilio. Operators select SIP for an authorized carrier account or
one owned number. Telephony creates a dedicated outbound connection/trunk with
strong, separate SIP credentials, stores credentials encrypted, verifies carrier
readiness, then saves the transport selection for future human and AI calls.

Telnyx uses an explicitly selected enabled Outbound Voice Profile, or the sole
enabled profile. When creating a profile, operators must choose allowed country
codes and a positive USD daily spending limit. Telephony translates internal
codec names to Telnyx's provisioning names and uses TLS signaling. Twilio creates
a secure termination trunk, credential list and credential, and verifies the
list attachment. Neither path reassigns inbound numbers, changes inbound routes,
rotates saved credentials or enables provider recording.

Setup uses a persistent journal and a cancellable 30-second deadline. Concurrent
configuration requests are serialized per project/account independently of media
and dialing. Lost create responses are reconciled through exact resource names
and saved IDs; an uncertain result that is not yet visible prevents another
create. Partial setup does not replace the existing outbound selection. The
advanced existing-resource form remains available. Resetting to Voice API keeps
managed resources available for reuse and does not delete active-call resources.

## Exclusive activation

SDK v0.100.0 is pinned and `min_apteva_version` is 0.87.4. Both source and embedded
manifests declare `runtime.activation_strategy: exclusive`, a 60-second startup
budget and backward-compatible database upgrades. The server prepares the new
version while the old app serves, then stops/reaps the previous process before
starting and verifying its replacement. An unsuccessful candidate follows the
server's verified rollback contract. This prevents an old SIP listener from
blocking its replacement on the same configured port.

Telephony binds configured SIP listeners during mounting before HTTP readiness.
This now also covers installations that use SIP only for outbound calls, even
when `sip_enabled` is false and there are no inbound SIP routes. Ordinary
WebSocket-only defaults do not start a SIP listener. Ports remain installation
configuration: staging and production still need separate SIP signaling ports
and media ranges. Runtime number/route settings remain admission-only and do
not restart Telephony.

Exclusive app upgrades interrupt active sessions. Publish/install the supporting
Apteva server before installing this version; schedule app upgrades when there
are no live calls. This release does not update or configure any installation.

## Verification

Provider contract tests cover complete Telnyx/Twilio setup, explicit profile
selection and spending policy, encrypted credentials, failure resumption, lost
create responses, temporary provider-list visibility, concurrent setup,
cancellation, ownership/authorization, inherited/manual settings and preservation
of existing calls. Three local browser tests exercise automatic and advanced
forms, while headless tests cover scoped requests and policy serialization.

Activation tests check both manifest declarations and JSON persistence. A real
local TLS listener conflict verifies that outbound-only SIP startup cannot be
reported ready until the port is available. SIP shutdown now waits for the listener loop to exit within the shutdown budget, and the test confirms immediate port reuse.
The local internal-audio benchmark harness and its prior evidence are retained.
Full Go/frontend tests, focused race tests, vet, typecheck, panel import checks
and builds are publication gates. Telephony CI includes the new activation/setup
race cases and browser setup tests.

Provider provisioning is verified against local contract fixtures. This release
makes no real carrier writes/calls and performs no staging/production installation.
Actual SIP/TLS, SRTP, two-way audio and Storage delivery still require controlled
validation on the chosen host. The SDK/server lifecycle is consumed through its
public contract; server implementation changes are outside this app release.
