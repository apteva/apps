-- Small side tables avoid rewriting the invocation history during upgrade.
CREATE TABLE IF NOT EXISTS function_runtime_policies(function_id INTEGER PRIMARY KEY REFERENCES functions(id) ON DELETE CASCADE, policy_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS function_capacity_settings(id INTEGER PRIMARY KEY CHECK(id=1), settings_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS function_invocation_resources(invocation_id INTEGER PRIMARY KEY REFERENCES function_invocations(id) ON DELETE CASCADE, resources_json TEXT NOT NULL);
