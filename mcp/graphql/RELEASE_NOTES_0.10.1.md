# GraphQL v0.10.1

Fixes `aggregate_pipeline` stage budget validation after JSON decoding. Valid
integer budgets such as `2097152` (2 MiB) were formatted as scientific notation
before integer parsing and incorrectly rejected. Budgets now use typed integer
validation, with finite/integral/range checks before conversion of JSON float64
values and direct validation of integer and json.Number inputs.

Fractional, non-finite, overflowing and invalid-type values remain rejected.
Existing stage row/byte bounds and cumulative release/request budgets still apply.
This fixes both version 2 relational stages and version 3 separate statements.

Validation: full Go race suite, vet and source build; typed numeric boundaries;
stored-JSON publication and execution with 2 MiB budgets in both resolver formats;
real Tables 0.2.16 snapshot pipeline integration with 2 MiB stage budgets.

SDK remains pinned to the latest main-line tag v0.99.0. Tables >=0.2.16 is unchanged.
No running installation or production deployment is updated by this release.
