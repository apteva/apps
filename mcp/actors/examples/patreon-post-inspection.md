# Optional Patreon post inspection example

Requires Actors 0.2.16+. This is a user-editable actor definition, not a built-in
site workflow. Supply the assigned `context_id`, explicit `creator_url` and,
for `inspect_page`, exact `page_url`. No creator, account or tier is fixed.

`inspect_page` uses raw extraction and a fresh semantic observation. Its first
row returns the current URL, page title/heading, observed creator URL/token,
visible navigation links and control labels. Further rows preserve raw links
and form state for read-only verification. It performs no form changes.

`fetch_posts` follows the observed Posts and Drafts anchors. In Patreon's
current Library UI, published and scheduled posts share Posts; Scheduled rows
are explicitly identified by their status. Drafts has a separate verified view.
It verifies the account's Your page link against the requested creator on each
batch, scrolls the freshly observed Document region, deduplicates by ID, and
returns each title/status/edit URL. Only published rows have a derived live URL;
a scheduled/draft editor must not be represented as a live publication.

The required footer total must match the number of unique IDs seen in each
view. Completion also requires stable end-of-scroll geometry and no loading
indicator. Selectors, status labels and identity aliases are editable in this
example. If Patreon changes them, the worker must stop for incomplete coverage.
Read the full dataset through `actors_dataset_read` and its cursors; the first
20 preview rows are not the full library.

Before creating a post, require a completed run, `coverage.read_only=true`,
`coverage.inspection_complete=true`, `coverage.more_results_remaining=false`,
all of published/scheduled/draft in checked views, matching creator/context,
and complete per-view count/end evidence. Reconcile any existing exact live,
scheduled or draft IDs and any prior accepted write run independently. A failed
or incomplete scan, including a successful empty preview, cannot prove absence.
Never fall back to a generic run or switch contexts to overcome an error.

Local live acceptance covered published and draft fixtures, one temporarily
scheduled test draft, and known old posts beyond the first batch. The scheduled
fixture was restored to a draft after testing. Production validation is read-only.
