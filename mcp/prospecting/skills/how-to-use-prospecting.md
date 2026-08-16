# Using Prospecting

Prospecting is a standalone lead catalog. It can seed, organize, explore,
update, decide, and export leads without Web or CRM. It does not send email,
SMS, WhatsApp messages, or telephone calls.

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

## Ownership boundary

- Prospecting owns target profiles, candidates, evidence references, scores,
  decisions, exclusions, and handoff references.
- Optional Web owns raw browser research artifacts it creates.
- Optional CRM owns contacts handed to it and their relationship history.

Prospecting remains the working lead catalog. After a CRM handoff, use CRM tools
for changes to the accepted contact record.
