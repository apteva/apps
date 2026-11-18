-- Outbound SIP call details referenced calls without ON DELETE CASCADE, so
-- retention could not delete an outbound direct SIP call: every housekeeping
-- batch that reached one failed with FOREIGN KEY constraint failed and rolled
-- back the whole batch.
CREATE TABLE outbound_sip_calls_new (
 call_id TEXT PRIMARY KEY REFERENCES calls(id) ON DELETE CASCADE,
 resource_id TEXT NOT NULL,
 host TEXT NOT NULL,
 codecs_json TEXT NOT NULL
);
INSERT INTO outbound_sip_calls_new(call_id,resource_id,host,codecs_json)
 SELECT call_id,resource_id,host,codecs_json FROM outbound_sip_calls;
DROP TABLE outbound_sip_calls;
ALTER TABLE outbound_sip_calls_new RENAME TO outbound_sip_calls;
