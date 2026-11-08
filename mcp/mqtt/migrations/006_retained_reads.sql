-- App-owned cursor signing key survives process restart. It is never exposed
-- through management APIs; cursor authorization still requires the caller scope.
CREATE TABLE mqtt_retained_read_key(id INTEGER PRIMARY KEY CHECK(id=1), secret BLOB NOT NULL CHECK(length(secret)=32));

-- Older retained values stored the original expiry interval but predate the
-- absolute expires_at column. Align their read visibility with broker recovery.
-- CASE guards JSON functions so corrupt properties remain repairable and are
-- reported by normal metadata validation rather than breaking this migration.
UPDATE mqtt_retained
SET expires_at=unixepoch(updated_at)+CAST(CASE WHEN json_valid(properties) THEN json_extract(properties,'$.messageExpiry') ELSE 0 END AS INTEGER)
WHERE expires_at=0 AND unixepoch(updated_at) IS NOT NULL
AND CASE WHEN json_valid(properties)
    THEN json_type(properties,'$.messageExpiry')='integer'
         AND json_extract(properties,'$.messageExpiry') BETWEEN 1 AND 4294967295
    ELSE 0 END;
