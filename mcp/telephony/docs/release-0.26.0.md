# Telephony 0.26.0

- The dashboard Calls panel is rebuilt from current sources. Its bundle
  (`ui/CallsPanel.mjs`) was last built for 0.19.0, so panel users did not get
  the panel and browser-client changes released since then:
  - 0.21.0: WebRTC audio connects while a call rings, so answering only claims
    the call.
  - 0.22.0: automatic pickup keeps running through failed check-ins. The panel
    shows **Retry now** for a temporary block and **Use this browser** for the
    others.
  - 0.23.0: the panel offers **No agent** when a number rings in the browser,
    and shows routed numbers with no agent as such.
  - 0.25.0: browser audio works for a Telephony copy inside an Environment.
- Embedded clients, which load the hashed client from `ui/frontend/`, already
  had these changes. Only the dashboard panel changes. There is no server
  change.
