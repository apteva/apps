# Conversations 0.21.1 validation

Post-release testing of 0.21.0 found one failure in twelve real Codex workflows:
a main-thread approval verdict was delivered correctly, but conversations_send
refused its acknowledgment because ordinary main-thread replies are forbidden.
The other eleven workflows and the standalone external browser checks passed.

0.21.1 permits one acknowledgment of the calling main thread's own resolved
approval, identified explicitly by approval_message_id. The verdict event and
skill document the exact call. Ordinary main replies remain forbidden. Tests
cover approvals and denials, retries, pending decisions, incorrect conversations,
other agent participants and approvals raised by a conversation thread.

The frontend assets are unchanged from 0.21.0; only their manifest version moves.
The published SDK remains 0.7.0 and no Conversations npm package is published.

External-host evidence against published 0.21.0 and SDK 0.7.0:
- Real Codex reply, history after reload, mobile geometry and visitor isolation passed.
- Local cold chat readiness: 119 ms; reload: 59 ms, with all three assets cached.
- A fresh load at simulated 100 ms latency and 1.6 Mbps: 2616 ms for the whole
  demo page; 529.5 ms from frontend loading start to completion.
- Real Codex reply in this run: 2375 ms. These are individual local measurements,
  not production guarantees. Production dashboard installations were not changed.

Post-release patch results are recorded in the GitHub release notes and local
validation runtime logs.
