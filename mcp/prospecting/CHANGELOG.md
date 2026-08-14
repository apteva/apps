# Changelog

## v0.2.4

- Reject non-phone tracking identifiers when qualifying United States targets; retain only valid 10-digit US numbers or 11-digit numbers beginning with country code `1`.
- Clear previously extracted invalid phones during requalification.
- Preserve useful discovery company names when a contact page exposes an unrelated person or heading as its site name.
- Normalize generic contact-page search titles back to the company domain brand.

## v0.2.3

- Reject marketplaces and downloadable form-template results during discovery.
- Normalize Google result labels into company names instead of page titles or displayed URLs.
- Traverse contact, team, about, and homepage links across up to five first-party pages.
- Extract and rank emails and phone numbers from links, visible text, metadata, structured data,
  obfuscated email text, and unlabeled footer contact blocks.
- Make repeated batch qualification advance through unenriched candidates by default.

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
