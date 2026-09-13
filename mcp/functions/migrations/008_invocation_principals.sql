-- Additive side tables: preserve existing invocation history and runtime policy.
CREATE TABLE IF NOT EXISTS function_invocation_policies (
 function_id INTEGER PRIMARY KEY REFERENCES functions(id) ON DELETE CASCADE,
 policy_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS function_invocation_identities (
 invocation_id INTEGER PRIMARY KEY REFERENCES function_invocations(id) ON DELETE CASCADE,
 identity_json TEXT NOT NULL
);
