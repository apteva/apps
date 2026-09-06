SELECT
        a.id,
        a.local_id,
        a.raw_call_id,
        a.provider_call_id,
        a.audio_files,
        a.ai_status,
        a.started_at,
        a.ended_at,
        a.status_at,
        a.talk_duration_seconds,
        r.id AS reconciliation_id,
        r.status AS reconciliation_status,
        r.attempts AS reconciliation_attempts,
        r.retry_after AS reconciliation_retry_after
      FROM {appels} a
      LEFT JOIN {ringover_audio_reconciliations} r
        ON r.id = (
          SELECT r2.id
          FROM {ringover_audio_reconciliations} r2
          WHERE r2.appel_local_id = a.local_id
          ORDER BY r2.id DESC
          LIMIT 1
        )
      WHERE a.provider = 'ringover'
        AND COALESCE(a.ended_at, '') <> ''
        AND COALESCE(a.ended_at, a.status_at, a.started_at) >= ?
        AND COALESCE(a.ended_at, a.status_at, a.started_at) <= ?
        AND COALESCE(a.talk_duration_seconds, a.duration_seconds, 0) > 0
        AND COALESCE(a.status, '') NOT IN ('manque', 'missed', 'voicemail', 'cancelled', 'failed')
        AND NOT EXISTS (
          SELECT 1
          FROM json_each(CASE WHEN json_valid(a.audio_files) THEN a.audio_files ELSE '[]' END) audio
          WHERE COALESCE(json_extract(audio.value, '$.storage_file_id'), '') <> ''
             OR COALESCE(json_extract(audio.value, '$.storage_file.id'), '') <> ''
        )
        AND (
          r.id IS NULL
          OR (
            COALESCE(r.status, '') NOT IN ('recovered', 'invalid_call')
            AND (COALESCE(r.retry_after, '') = '' OR r.retry_after <= ?)
          )
        )
      ORDER BY COALESCE(a.ended_at, a.status_at, a.started_at) ASC, a.id ASC
      LIMIT ?
