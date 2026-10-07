# CRM v0.9.11

Fixes slow audience previews and full audience counts in
`contacts_resolve_audience` for projects with thousands of routes.

Delivery-health lookups now explicitly start from each contact's channels,
using the existing contact index and delivery-state primary key instead of
repeatedly scanning project-wide routes. The change covers eligibility,
exclusion reasons, and preferred-address selection for email, SMS, and WhatsApp.

Audience queries and list/segment metadata reads use the SDK read-only database
pool and propagate the MCP request context. Counts and paging check iteration
errors so cancellation cannot silently return a partial audience. Tool metadata
now documents `include_counts` and its count-free response behavior.

Regression coverage includes 12,000 contacts, contact-first query plans for all
three transports, legacy policy equivalence, read-pool isolation, cancellation
while waiting for connections and while executing SQLite queries, and real
sidecar MCP dispatch. Existing pagination and suppression behavior is preserved.

No database migrations or CRM data changes. All previously released CRM fixes
remain included.
