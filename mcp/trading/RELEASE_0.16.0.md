# Trading v0.16.0

Separate portfolio execution from automated strategy research.

Trading retains portfolios, direct human/agent orders, broker and paper
execution, risk controls, fills and performance. Strategies v0.1 is a separate
app and database, with Trading as its required integration.

Legacy strategy assignments pause during upgrade. Research data is preserved
for explicit, idempotent import into Strategies. No portfolio, order, fill or
broker binding is moved. Imported assignments require explicit reassignment.

App-origin order attribution and namespaced retry keys preserve provenance and
prevent retry collisions across installations. See Strategies/MIGRATION.md.
