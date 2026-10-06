# Telephony 0.5.0

Add generic, project-scoped inbound routing decisions through a bound Functions
app. Published flows can select allowlisted individual browser destinations with
bounded deadlines and a mandatory local fallback. Durable decisions, atomic
capacity reservations and existing first-answer offers prevent duplicate ringing
and over-allocation across inbound/outbound calls and destination aliases.

Expose configuration and simulation in the Telephony panel and existing routing
HTTP/MCP APIs. Add `telephony_decisions_list` and durable correlated routing,
offer, verified answerer, media-connected and final outcomes. Preserve existing
calls and event history during the database upgrade.

The initial version uses the active version of an internal Function. No external
webhooks, browser presence, durable waiting queues or new voicemail runtime are
introduced. Shared pools remain supported as static fallbacks. Business quotas
and customer-specific routing stay in consuming applications.

All 0.4.5 softphone functionality is retained, including application-user access,
public SDK frontend loading, same-origin CSP-compatible audio assets, microphone
selection/preview, configurable audio processing and buffering, and the Twilio
stream-status/socket ownership fix. The headless client and audio asset bytes are
unchanged. The Go SDK pin advances to v0.81.0 by ancestry.

See [routing-decisions.md](routing-decisions.md) for configuration, request/response
examples, outcome topics, capacity semantics and recovery limits.
