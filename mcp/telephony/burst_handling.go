package main

import (
	"database/sql"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const burstAnswerAnnouncement = "answer_announcement"

type burstHandlingPolicy struct {
	Action, Message, Language string
	MaxSeconds, MaxConcurrent int64
}

func loadBurstHandlingPolicy(config sdk.Config) burstHandlingPolicy {
	p := burstHandlingPolicy{Action: "reject", Message: "We cannot take your call right now. Goodbye.", Language: "en-US", MaxSeconds: 20, MaxConcurrent: 3}
	if config.Get("inbound_burst_action") == burstAnswerAnnouncement {
		p.Action = burstAnswerAnnouncement
	}
	if text := strings.TrimSpace(config.Get("inbound_burst_message")); text != "" && len([]rune(text)) <= 240 {
		p.Message = text
	}
	if language := strings.TrimSpace(config.Get("inbound_burst_language")); language != "" && len(language) <= 32 {
		p.Language = language
	}
	if n := boundedBurstSetting(config, "inbound_burst_max_seconds", 20, 60); n >= 5 {
		p.MaxSeconds = n
	}
	if n := boundedBurstSetting(config, "inbound_burst_max_concurrent", 3, 50); n >= 1 {
		p.MaxConcurrent = n
	}
	return p
}

// Choose and persist the disposition with call admission. Capacity counts only
// suppressed calls; it cannot close a destination to unrelated callers. Caller
// ID and Diversion content are never treated as proof of an upstream identity.
func burstHandlingPlanTx(tx *sql.Tx, call *callRow, policy burstHandlingPolicy) (*inboundRoutingPlan, error) {
	plan := &inboundRoutingPlan{NodeID: "suppression", TerminalType: "reject", SuppressionAction: "reject", SuppressionReason: "configured_rejection"}
	if call.HandlingReason == handlingSpamSuppressed {
		plan.SuppressionReason = "explicit_caller_block"
		return plan, nil
	}
	if policy.Action != burstAnswerAnnouncement {
		return plan, nil
	}
	// The generic policy can gain other adapters after their answer/completion
	// semantics are verified. Direct SIP must not enter a programmable workflow.
	var transport string
	err := tx.QueryRow(`SELECT COALESCE(inbound_transport,'') FROM inbound_routes WHERE id=? AND project_id=?`, call.RouteID, call.ProjectID).Scan(&transport)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == sql.ErrNoRows || call.CarrierSlug != "telnyx" || transport == inboundTransportSIPDirect {
		plan.SuppressionReason = "announcement_unsupported"
		return plan, nil
	}
	var active int64
	err = tx.QueryRow(`SELECT COUNT(*) FROM (SELECT id FROM calls WHERE project_id=? AND handling_reason='burst_suppressed' AND announcement_text<>'' AND status NOT IN ('completed','failed','no-answer','busy','canceled') LIMIT ?)`, call.ProjectID, policy.MaxConcurrent).Scan(&active)
	if err != nil {
		return nil, err
	}
	if active >= policy.MaxConcurrent {
		plan.SuppressionReason = "announcement_capacity"
		return plan, nil
	}
	plan.TerminalType = "hangup"
	plan.SuppressionAction = burstAnswerAnnouncement
	plan.SuppressionReason = "configured_announcement"
	plan.TerminalMessage = policy.Message
	plan.TerminalLanguage = policy.Language
	call.AnnouncementState = "awaiting_answer"
	call.AnnouncementText = policy.Message
	deadline := ringTime(time.Now().Add(time.Duration(policy.MaxSeconds) * time.Second))
	call.DeadlineAt, call.StateExpiresAt = deadline, deadline
	call.RecordingMode = recordingModeOff
	return plan, nil
}
