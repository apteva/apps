UPDATE voice_notes
   SET transcript_status = 'none',
       error_message = '',
       status = CASE WHEN status = 'transcribing' THEN 'recorded' ELSE status END,
       updated_at = CURRENT_TIMESTAMP
 WHERE transcript_status = 'failed'
   AND error_message = 'no Deepgram transcript integration bound';
