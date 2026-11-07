# Destination-scoped realtime turn detection

AI (`ai`) and on-demand agent (`agent`) destinations can set the following in
their existing configuration. Both the routing HTTP destination save endpoint
and `telephony_destinations_create` validate it before writing:

```json
{
  "agent_id": 4,
  "directive": "Help the caller book an appointment.",
  "turn_detection": {
    "profile": "telephony",
    "silence_duration_ms": 500
  }
}
```

- `turn_detection` is optional and must be an object when supplied.
- `profile` accepts `telephony` or `default`. Omission uses `telephony`.
- `silence_duration_ms` accepts an integer from 1 to 60000, matching Core's
  supported positive range. Omission inherits the profile's silence default.
  Explicit zero is rejected because Core treats zero as omission.
- Unknown keys and incorrect types are rejected. Browser, PSTN, SIP and voicemail
  destinations cannot specify this realtime-only setting.
- Only profile and silence duration are sent. Sensitivity, prefix padding and
  interruption retain the selected profile defaults. The current Core telephony
  profile resolves absent silence to 750 ms; Telephony does not duplicate that
  default or change Core's configuration.
- Mapping to native turn detection is performed by Core's realtime provider.
  Providers apply fields they support; a spawn request alone does not prove a
  particular provider's response latency.

## Routing and session safety

Published flows snapshot destination configuration. Save the destination and
republish/reassign the intended flow to apply a new setting to new inbound calls.
The existing routing API handles these edits without restarting Telephony.
An existing call keeps its selected snapshot, including human-routing exhaustion
to AI, a claimed ring-group offer, and bounded AI startup retries. A destination
edit or flow publication does not update, reconnect or terminate a ready session.

Unconfigured destinations, legacy inbound routes and outbound tool calls retain
their existing telephony profile. There is no environment variable, migration,
shared Core default or carrier-specific branch for this feature.

Installing a new app binary is different from editing destination configuration.
The inspected local server's upgrade path replaces the process for an install
and retires the previous process; fixed-port installs stop the previous process
before activation. Do not assume active media can survive an app upgrade. Drain
live calls for that install before a future deployment. No installation,
staging/production configuration, or real call was changed for this implementation.

## Local verification and later conversational evaluation

Regression tests exercise save-time rejection, omitted/default/500 ms settings,
project isolation, flow publication and per-call snapshots, AI fallback and
temporary startup retry, claimed ring-group configuration, unaffected outbound
defaults, and reuse of a ready realtime session without another spawn or carrier
operation. Tests mock Core and carriers; they do not establish conversational
quality or confirm a live provider's resolved configuration.

Local verification on 9 October 2026, using the published app-sdk v0.99.0 with
`GOWORK=off`: the complete Go suite passed (818 tests/subtests, 3 opt-in skips),
focused race checks passed for turn detection, AI handoff, preparation and
answer-claim paths, and `go vet ./...` plus the app build passed. Browser/audio
assets were not changed. An initial regression involving graph re-simulation
was caught by the existing AI failure-branch test and fixed before this final run.

When a staging experiment is separately authorized, set 500 ms on its AI
destination and compare with 750 ms using clean booking conversations, hesitant
date/time answers, natural mid-sentence pauses, interruptions and café noise.
Verify Core's resolved configuration in `realtime.session_started`, then measure
speech end to first response audio, premature responses and interruptions. This
setting targets turn latency; greeting and speech-guard delays are separate.
