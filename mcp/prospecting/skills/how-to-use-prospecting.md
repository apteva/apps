# Using Prospecting

Prospecting finds and qualifies potential customers before they become CRM
contacts. It does not send email, SMS, WhatsApp messages, or telephone calls.

## Workflow

1. Read existing target profiles with `prospecting_profiles_list` before
   creating another one.
2. Create or update a structured target profile containing industries,
   locations, target titles, and keywords.
3. Run `prospecting_search_run` with a bounded `limit`. Omit `query` to use the
   profile-generated query. Leave the engines unset for Google with automatic
   DuckDuckGo fallback. Known noisy domains are suppressed deterministically.
4. Treat search results as candidates, not verified leads. Run
   `prospecting_candidates_qualify` for one candidate or
   `prospecting_candidates_qualify_batch` for a bounded set. Qualification uses
   fixed rules—not an AI model—to extract facts, detect workflow signals,
   classify eligibility, recalculate scores, and retain first-party evidence.
5. Read each candidate with `prospecting_candidates_get`. Verify its eligibility,
   score reasons, automation signals, contact details, and source evidence.
6. Use `prospecting_candidates_research` when broader evidence is needed. This
   calls Web and can take longer than a cached read.
7. Use `prospecting_candidates_update` to record confirmed company and
   decision-maker fields. Do not invent names, titles, email addresses, phone
   numbers, or research claims.
8. Defer uncertain candidates. Reject clear mismatches and use
   `exclude_company=true` when future discovery should suppress the company.
9. Call `prospecting_candidates_accept` only when the user wants the candidate
   retained in CRM. Acceptance is a real CRM write, requires a valid email or
   phone, is idempotent, and never initiates outreach.

## Ownership boundary

- Prospecting owns target profiles, candidates, evidence references, scores,
  decisions, exclusions, and handoff references.
- Web owns raw browser research artifacts.
- CRM owns accepted contacts and their relationship history.

After acceptance, use CRM tools for contact changes. Prospecting may retain the
qualification evidence but must not become a second canonical contact store.
