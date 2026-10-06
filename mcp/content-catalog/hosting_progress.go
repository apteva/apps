package main

import (
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func bunnyStage(status int64) string {
	switch status {
	case 0:
		return "created"
	case 1:
		return "uploaded"
	case 2:
		return "processing"
	case 3:
		return "transcoding"
	case 4:
		return "ready"
	case 5:
		return "encoding_failed"
	case 6:
		return "upload_failed"
	default:
		return fmt.Sprintf("provider_status_%d", status)
	}
}

func saveHostProgress(ctx *sdk.AppCtx, pid, id string, obs HostObservation) error {
	messages := obs.TranscodingMessages
	if len(messages) == 0 {
		messages = json.RawMessage(`[]`)
	}
	next := nextHostingCheck(obs.Status)
	_, err := ctx.AppDB().Exec(`UPDATE hostings SET encode_progress=?,provider_status=?,provider_stage=?,transcoding_messages=?,next_check_at=?,check_attempts=0,check_error='' WHERE project_id=? AND id=?`, obs.EncodeProgress, obs.ProviderStatus, obs.ProviderStage, string(messages), next, pid, id)
	return err
}

func deferHostingCheck(ctx *sdk.AppCtx, pid, id, message string) error {
	var attempts int
	if err := ctx.AppDB().QueryRow(`SELECT check_attempts FROM hostings WHERE project_id=? AND id=?`, pid, id).Scan(&attempts); err != nil {
		return err
	}
	next := time.Now().UTC().Add(hostingBackoff(attempts + 1)).Format(time.RFC3339Nano)
	_, err := ctx.AppDB().Exec(`UPDATE hostings SET check_error=?,check_attempts=check_attempts+1,next_check_at=? WHERE project_id=? AND id=?`, message, next, pid, id)
	return err
}

func nextHostingCheck(status string) string {
	if status != "processing" {
		return ""
	}
	return time.Now().UTC().Add(15 * time.Second).Format(time.RFC3339)
}
