# Using Prospecting

Prospecting is a standalone lead catalog. It can seed, organize, explore,
update, decide, and export leads without Web or CRM. When CRM is connected,
it can also perform explicitly confirmed one-to-one email, SMS, and WhatsApp
outreach through CRM's Messaging binding. It does not make telephone calls or
run campaigns.

## Workflow

1. Call `prospecting_capabilities` to learn whether Google Places, optional Web discovery and
   optional CRM handoff are connected.
2. Read existing target profiles with `prospecting_profiles_list` before
   creating another one.
3. Seed the catalog with `prospecting_candidates_create` or
   `prospecting_candidates_import`. The profile is optional for both; when none
   exists, Prospecting creates an `Imported leads` profile. Never invent contact
   details.
4. Use `prospecting_candidates_search`, `prospecting_candidates_get`, and
   `prospecting_candidates_update` to explore and curate the standalone catalog.
   Use `prospecting_candidates_export` when the user wants a portable copy.
5. If Web is connected, create or update a structured target profile containing industries,
   locations, target titles, and keywords.
6. If Web is connected, run `prospecting_search_run` with a bounded `limit`. Omit `query` to use the
   profile-generated query. Leave the engines unset for Google with automatic
   DuckDuckGo fallback. Known noisy domains are suppressed deterministically.
7. Treat search results as candidates, not verified leads. Run
   `prospecting_candidates_qualify` for one candidate or
   `prospecting_candidates_qualify_batch` for a bounded set. Qualification uses
   fixed rules—not an AI model—to extract facts, detect workflow signals,
   classify eligibility, recalculate scores, and retain first-party evidence.
8. Read each candidate with `prospecting_candidates_get`. Verify its eligibility,
   score reasons, automation signals, contact details, and source evidence.
9. Use `prospecting_candidates_research` when broader evidence is needed. This
   calls Web and can take longer than a cached read.
10. Use `prospecting_candidates_update` to record confirmed company and
   decision-maker fields. Do not invent names, titles, email addresses, phone
   numbers, or research claims.
11. Defer uncertain candidates. Reject clear mismatches and use
   `exclude_company=true` when future discovery should suppress the company.
   Permanently remove all rejected candidates only when the user explicitly
   requests cleanup, using `prospecting_candidates_purge_rejected` with
   `confirm=true`.
12. If CRM is connected, call `prospecting_candidates_accept` only when the
   user explicitly wants the candidate retained there. Acceptance is a real
   CRM write, requires a valid email or phone, is idempotent, and never
   initiates outreach.
13. When the user wants to contact a lead while keeping it in the active
    Prospecting queue, call `prospecting_candidate_outreach_start`. This is a
    real CRM write but does not send anything. Then use
    `prospecting_candidate_outreach_get` to inspect recent CRM activity,
    conversations, verified senders, and WhatsApp session/template state.
14. Call `prospecting_candidate_outreach_send` only after the user explicitly
    approves the exact external message, recipient, and channel. Pass
    `confirm=true` plus a unique `idempotency_key`. New email conversations
    require a subject. Replies should pass `conversation_id` so CRM preserves
    threading. WhatsApp outside the 24-hour reply window requires an approved
    template and any required `template_vars`.
15. Never use a send to test configuration. Read sender state with
    `prospecting_candidate_outreach_get`. Do not send bulk messages, create a
    cadence, or contact a rejected lead.

## Ownership boundary

- Prospecting owns target profiles, candidates, evidence references, scores,
  decisions, exclusions, and handoff references.
- Optional Web owns raw browser research artifacts it creates.
- Optional CRM owns linked contacts, conversations, activities, suppression,
  delivery rules, and relationship history. CRM delegates delivery to its
  Messaging binding; Prospecting never connects to Messaging directly.

Prospecting remains the working lead catalog. After a CRM handoff, use CRM tools
for changes to the accepted contact record.

## Bounded automated prospecting

When the user asks to discover and qualify a batch, use `prospecting_run` rather
than chaining individual tools for every company. The agent only needs
Prospecting tools; Prospecting calls Places, Web, and CRM internally.

- Use `source=google_places` for local businesses by industry and location, or
  `source=web` for broader company discovery.
- Check capabilities and select an active target profile. To configure Places,
  list `prospecting_places_connections` and save the chosen accessible connection
  with `prospecting_settings`. Never request or expose the raw API key.
- Default to `limit=20`, `qualify=true`, and `crm_mode=review`. If the user has
  requested automatic CRM addition, use `crm_mode=auto` with their target list and
  fit/confidence thresholds (defaults 70/60). This creates or links CRM contacts
  and records notes; it does not contact anyone. The user's existing authorization
  to add matching leads to CRM applies to the bounded run.
- Set an `idempotency_key` unique to this intended run and reuse it only for
  retries of the same request. Changing options with the same key is rejected.
- The tool returns immediately with a queued run ID. Poll `prospecting_run_get`
  at sensible intervals, then report created/existing/excluded/qualified/retained/
  transferred/failed counts with candidate and CRM IDs. Do not repeatedly restart
  discovery while the run is still processing.
- Use `prospecting_run_resume` to retry failed steps or resume after interruption.
  Completed qualification and handoffs are preserved. Abrupt process crashes can
  leave a lease active for up to five minutes. Respect per-run/daily request limits.
- Preserve branch identity and operator edits. Places provides company listing
  details and business phone numbers; it does not confirm executive identities,
  direct emails, company size, or buying intent. Do not invent them. Ratings and
  review counts are not buying signals. Google Maps/provider attribution remains
  attached to listing details.
- Prospects without a website, with incomplete qualification, below thresholds,
  rejected, deferred, or excluded remain outside automatic CRM transfer. Explain
  their saved reasons instead of lowering thresholds silently.
- Search returns ranked results, not every business in a city. Geographic query
  text and optional location restriction define the target area; US region bias
  alone does not guarantee US-only results.
- Sending email, SMS, or WhatsApp still requires the separate exact-message
  authorization described above. Never use a send to test this pipeline.
