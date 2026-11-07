package main

import (
	"database/sql"
	"testing"
	"time"
)

func seedEfficiencyArchive(t *testing.T, db *sql.DB, n int, ended time.Time) {
	t.Helper()
	stamp := ended.UTC().Format(time.RFC3339)
	_, err := db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?)
 INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,ended_at,project_id,direction,peer_kind,ingress_path,callback_secret,carrier_slug)
 SELECT 'efficiency-'||x,'thread-'||x,'','','','','','completed',?,?,'project-a','inbound','human','sip_direct','','telnyx' FROM n`, n, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,provider_status,storage_status,provider_deleted_at,created_at)
 SELECT 'rec-'||id,id,project_id,'telephony',10,'original-'||id,'completed','stored',?,? FROM calls LIMIT 5906`, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	// Archived fixtures already have their materialized telemetry reports.
	if _, err = db.Exec(`DELETE FROM telephony_audio_pending`); err != nil {
		t.Fatal(err)
	}
}
