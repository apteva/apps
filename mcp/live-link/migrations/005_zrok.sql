-- Live Link v0.6 — one persistent zrok reserved name per install.
--
-- Credentials are deliberately absent. zrok's enable token and Ziti identity
-- live only in zrok's native 0600 environment files under the install data
-- directory. connection_id lets us reject an accidental rebind to a different
-- zrok account instead of adopting or deleting another account's name.

CREATE TABLE zrok_state (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  connection_id   INTEGER NOT NULL,
  namespace       TEXT    NOT NULL DEFAULT 'public',
  name            TEXT    NOT NULL,
  public_url      TEXT    NOT NULL,
  created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
