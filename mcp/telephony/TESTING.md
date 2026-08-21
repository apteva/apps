# Telephony testing

Run the deterministic app tests on every change:

```bash
apteva test --tier 1,2 .
```

Tier 1 runs the fast in-process suite. Tier 2 compiles and starts the real
sidecar while deterministic carrier and browser peers exercise HTTP,
WebSockets, audio, reconnection, recording, lifecycle persistence, and app-bus
events. Neither tier requires carrier credentials by default.

## Live carrier profile

The opt-in live profile places a real, billable call between two dedicated test
numbers. The first number must be voice-capable for outbound calls. The second
must route inbound calls to this Telephony install with `answer_mode` set to
`human_browser`.

```bash
export RUN_TELEPHONY_LIVE_CARRIER=1
export APTEVA_LIVE_BASE_URL=https://public-apteva.example
export APTEVA_LIVE_API_KEY=...
export APTEVA_LIVE_PROJECT_ID=...
export TELEPHONY_LIVE_FROM_NUMBER=+...
export TELEPHONY_LIVE_TO_NUMBER=+...

apteva test --tier 2 --profile live-carrier .
```

The test calls through the installed Telephony app, answers both call legs with
softphone protocol clients, sends known PCM tones in both directions, checks
signal identity and level, hangs up, and waits for terminal carrier lifecycle
events. It does not invoke an LLM.
