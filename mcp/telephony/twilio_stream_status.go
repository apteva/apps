package main

import "time"

// Store each provider stream independently. The first terminal notification wins;
// duplicate/late starts cannot revive it. The SQL call-status guard also covers a
// completion racing the callback after its initial authentication lookup.
func (c *callsDB) recordTwilioStreamNotification(callID, streamSID, event, message string) (bool, error) {
	res, err := c.db.Exec(`INSERT INTO telephony_twilio_streams
  (call_id, stream_sid, status, error_message, received_at)
  SELECT id, ?, ?, ?, ? FROM calls
  WHERE id = ? AND status NOT IN ('completed','failed','no-answer','busy','canceled')
  ON CONFLICT(call_id, stream_sid) DO UPDATE SET
   status = excluded.status, error_message = excluded.error_message,
   received_at = excluded.received_at
  WHERE telephony_twilio_streams.status = 'stream-started'
   AND excluded.status <> 'stream-started'`,
		streamSID, event, message, time.Now().UTC().Format(time.RFC3339Nano), callID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
