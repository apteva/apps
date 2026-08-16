# Using Prospecting

Prospecting is a standalone lead catalog. It can seed, organize, explore,
update, decide, and export leads without Web or CRM. When CRM is connected,
it can also perform explicitly confirmed one-to-one email, SMS, and WhatsApp
outreach through CRM's Messaging binding. It does not make telephone calls or
run campaigns.

## Workflow

1. Call `prospecting_capabilities` to learn whether optional Web discovery and
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
