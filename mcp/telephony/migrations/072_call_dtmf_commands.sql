-- Keypad presses requested through telephony_send_dtmf. A retried tool call
-- with the same idempotency key returns the recorded outcome instead of
-- pressing the keys again.
CREATE TABLE IF NOT EXISTS call_dtmf_commands (
    call_id         TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    project_id      TEXT NOT NULL,
    digits          TEXT NOT NULL,
    status          TEXT NOT NULL,
    error           TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (call_id, idempotency_key)
);
