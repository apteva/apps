# Changelog

## v0.2.2

- Pin source installs to the immutable `prospecting/v0.2.2` release tag.

## v0.2.1

- Strip bullet-separated marketing taglines from extracted company names.

## v0.2.0

- Add Google-to-DuckDuckGo discovery fallback when a provider blocks a search.
- Automatically suppress known directories, social networks, community sites,
  job boards, and obvious provider content during discovery.
- Add bounded, deterministic first-party website qualification without an AI
  model, for one candidate or batches of up to 25.
- Extract normalized company identity, email, phone, location, decision-maker,
  approximate team size, and location count from site content and metadata.
- Detect evidence-backed automation opportunities such as appointment intake,
  reminders, insurance administration, front-desk coordination, missed calls,
  messaging, financing, and multi-location routing.
- Add explicit eligible/review/ineligible classification, qualification
  explanations, new scores, evidence, UI actions, and overview counters.

## v0.1.0

- Add project-scoped target profiles and bounded Web discovery runs.
- Add company-first candidates with editable decision-maker details.
- Add explainable fit and confidence scoring.
- Add cited Web research evidence and exclusions.
- Add idempotent accepted-candidate handoff to CRM.
- Add Overview, Discover, Candidates, and Settings panel surfaces.
