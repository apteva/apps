# Telephony 0.24.1

- `telephony_access_policy_set` accepts `subject_id` and `organization_id` as
  numbers, as it already did for `issuer_install_id`, and stores them as
  decimal strings. An Environments seed that takes the test adviser's user or
  organization id from an earlier Auth step with `{"$ref": ...}` gets a number.
  In 0.24.0 the policy refused it ("cannot unmarshal number"), so the ids had
  to be typed as string literals.
- The stored strings match what Telephony reads from Auth's `/me` for the
  same user, so the adviser's identity matches its policy entry.
- Ids that are not positive whole numbers are still refused.
