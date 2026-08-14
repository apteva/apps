# Changelog

## v0.2.8

- Treat `Website`, `Homepage`, and `Official Website` as generic search/page titles.
- Replace those labels with first-party site identity metadata, a meaningful page title, or the domain brand during qualification.

## v0.2.7

- Extract structured-data phones only from explicit `telephone`, `phone`, `phoneNumber`, or `contactPhone` fields.
- Ignore numeric fragments in structured-data image URLs, analytics identifiers, hashes, and unrelated metadata.

## v0.2.6

- Rebuild previously website-derived email and phone fields during requalification instead of preserving stale extraction output.
- Normalize valid pre-existing email values on an initial qualification pass.
- Require phone labels to be near unformatted digit sequences, preventing a distant `contact` word in minified page text from legitimizing tracking IDs.
- Continue accepting semantically typed `tel:` and structured-data phones plus visibly formatted phone numbers.

## v0.2.5

- Accept extracted email addresses only when they match the target's domain or a recognized public mailbox provider.
- Repair collapsed contact text such as a phone number concatenated before an `info@` mailbox, while rejecting long numeric local parts.
- Require formatting or semantic contact context before treating plain digits as a phone number.
- Filter additional directories, suppliers, hosted schedulers, PDF documents, email-list vendors, and staging domains during discovery.
- Clear previously extracted malformed or third-party email addresses during requalification.

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
