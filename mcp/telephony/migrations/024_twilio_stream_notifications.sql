-- Provider observations are independent of the live bridge and its socket claim.
CREATE TABLE telephony_twilio_streams (
 call_id TEXT NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
 stream_sid TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('stream-started','stream-stopped','stream-error')),
 error_message TEXT NOT NULL DEFAULT '',
 received_at TEXT NOT NULL,
 PRIMARY KEY(call_id, stream_sid)
);
