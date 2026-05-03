-- v0.5.0: full inbound — Twilio SMS/WhatsApp + SES S3-mode + verdicts.
--
-- Two small additions to the messages table; no other schema work.

ALTER TABLE messages ADD COLUMN verdicts TEXT NOT NULL DEFAULT '{}';
-- JSON map of provider verdicts on inbound rows. Today populated by
-- SES SNS notifications (spamVerdict, dkimVerdict, spfVerdict,
-- virusVerdict). Twilio inbound has no equivalent — leaves '{}'.
-- Outbound rows always '{}'.

ALTER TABLE messages ADD COLUMN s3_key TEXT;
-- For SES inbound rows received via the S3 action mode (notification
-- carries an S3 key instead of inline content), this is the bucket
-- key of the raw .eml. NULL for everything else. Lets
-- inbound_redispatch re-fetch + re-parse without round-tripping
-- through SES again.
