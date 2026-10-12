# Actors 0.2.30

Editable AdultFolio workflows now include native country/gender/unmessaged age-filter search, profile-age inspection, initial outreach screening, preparation and guarded initial sending for a genuinely empty conversation. Age bounds and expected role remain caller configuration; the example uses 18–39. Account, profile URL, numeric recipient ID, country, role and reported age are rechecked before outreach. Independent native "No message history" evidence and two empty-thread checks prevent confusing a partially loaded or existing thread with an empty conversation.

Initial-send persistence requires a unique outgoing saved server message, exact copy, sender and timestamp after reopening the conversation. A read-only verifier supports reconciliation after ambiguous outcomes. It never sends or proves non-delivery on failure. Initial sending and replying share the original reply guard namespace and stable request keys, preserving existing guards and blocking cross-operation replay.

The site-neutral engine adds inclusive numeric assertions and optional shared effect-guard namespaces. An explicitly unsuccessful Computer interaction fails instead of silently continuing. No account, country, provider or age policy is embedded in the engine.

Fixture coverage includes age boundaries and filter drift, empty/loading/nonempty threads, changed identity/recipient/composer, a message appearing before dispatch, lost accepted/unaccepted responses, saved-record mismatches and cross-operation duplicate protection. Live acceptance and deployment evidence are recorded separately; fixtures are not a claim that a real message was sent.
