# Actors 0.2.28

The editable AdultFolio exact lookup clicks the model-role label within its unique, selected autocomplete suggestion. Full exact suggestion name, role and numeric ID are verified before selection; selected name/ID are verified before the guarded navigation. This avoids a wide-row center hitting a child label with different live semantics. The observed destination URL, exact direct heading name, independent message ID and authenticated account remain mandatory after navigation. No coordinates or provider-specific compiled behavior are introduced.

Exact lookup fixtures and existing reply precommit/persistence tests pass. The runtime engine is unchanged from 0.2.26. Live message sending still requires an explicitly selected recipient and approved copy.
