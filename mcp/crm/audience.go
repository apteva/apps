package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// AudienceRecipient is a channel-specific, currently messageable route. CRM
// owns route selection and delivery health; consumers such as Campaigns only
// need the resolved address and must not duplicate that policy.
type AudienceRecipient struct {
	ContactID int64  `json:"contact_id"`
	Address   string `json:"address"`
}

type AudienceExclusion struct {
	ContactID int64  `json:"contact_id"`
	Reason    string `json:"reason"`
}

type AudienceResolution struct {
	Channel            string              `json:"channel"`
	RawCount           int64               `json:"raw_count"`
	EligibleCount      int64               `json:"eligible_count"`
	ExcludedCount      int64               `json:"excluded_count"`
	ExcludedByReason   map[string]int64    `json:"excluded_by_reason"`
	Recipients         []AudienceRecipient `json:"recipients"`
	Exclusions         []AudienceExclusion `json:"exclusions"`
	NextAfterContactID int64               `json:"next_after_contact_id,omitempty"`
	HasMore            bool                `json:"has_more"`
}

type audienceSource struct {
	query string
	args  []any
}

// toolResolveAudience evaluates exactly one CRM-owned audience source and
// resolves a healthy address for the requested transport. It is deliberately
// campaign-agnostic so CRM and Campaigns remain independently deployable.
func (a *App) toolResolveAudience(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	channel := strings.ToLower(strings.TrimSpace(strArg(args, "channel")))
	if channel != channelEmail && channel != channelSMS && channel != channelWhatsApp {
		return nil, fmt.Errorf("invalid channel %q (email|sms|whatsapp)", channel)
	}
	source, err := buildAudienceSource(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 1000)
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	afterID := int64Arg(args, "after_contact_id")
	includeAutomated := boolArg(args, "include_automated", false)
	includeCounts := boolArg(args, "include_counts", true)
	return resolveAudience(ctx.AppDB(), source, pid, channel, afterID, limit, includeAutomated, includeCounts)
}

func buildAudienceSource(db *sql.DB, pid string, args map[string]any) (*audienceSource, error) {
	segmentID := int64Arg(args, "segment_id")
	listID := int64Arg(args, "list_id")
	contactID := int64Arg(args, "contact_id")
	set := 0
	for _, id := range []int64{segmentID, listID, contactID} {
		if id != 0 {
			set++
		}
	}
	if set != 1 {
		return nil, errors.New("exactly one of segment_id, list_id, or contact_id is required")
	}

	active := `c.deleted_at IS NULL AND (c.status IS NULL OR c.status = 'active')`
	if contactID != 0 {
		return &audienceSource{
			query: `SELECT c.id AS contact_id FROM contacts c
				WHERE c.project_id = ? AND c.id = ? AND ` + active,
			args: []any{pid, contactID},
		}, nil
	}
	if listID != 0 {
		list, err := dbListGet(db, pid, listID)
		if err != nil {
			return nil, err
		}
		if list == nil || list.ArchivedAt != "" {
			return nil, errors.New("list not found")
		}
		return &audienceSource{
			query: `SELECT c.id AS contact_id FROM contact_list_members m
				JOIN contacts c ON c.id = m.contact_id AND c.project_id = m.project_id
				WHERE m.project_id = ? AND m.list_id = ? AND ` + active,
			args: []any{pid, listID},
		}, nil
	}

	segment, err := dbSegmentGet(db, pid, segmentID)
	if err != nil {
		return nil, err
	}
	if segment == nil || segment.ArchivedAt != "" {
		return nil, errors.New("segment not found")
	}
	if segment.Kind == "static" {
		return &audienceSource{
			query: `SELECT c.id AS contact_id FROM contact_segment_snapshots s
				JOIN contacts c ON c.id = s.contact_id AND c.project_id = s.project_id
				WHERE s.project_id = ? AND s.segment_id = ? AND ` + active,
			args: []any{pid, segmentID},
		}, nil
	}
	filter, err := compileSegmentDefinition(pid, segment.ListID, segment.Definition)
	if err != nil {
		return nil, err
	}
	return &audienceSource{
		query: `SELECT c.id AS contact_id FROM contacts c WHERE ` + strings.Join(filter.where, " AND "),
		args:  filter.args,
	}, nil
}

func resolveAudience(db *sql.DB, source *audienceSource, pid, channel string, afterID int64, limit int, includeAutomated, includeCounts bool) (*AudienceResolution, error) {
	if source == nil {
		return nil, errors.New("audience source required")
	}
	kind := "phone"
	if channel == channelEmail {
		kind = "email"
	}
	// channel and kind are validated closed-set constants before interpolation.
	healthy := `EXISTS (SELECT 1 FROM contact_channels cc
		JOIN contact_channel_delivery_state ds ON ds.project_id = cc.project_id AND ds.channel_id = cc.id
		WHERE cc.project_id = c.project_id AND cc.contact_id = c.id
		  AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `'
		  AND ds.suppressed = 0 AND ds.quarantined = 0
		  AND ds.status NOT IN ('hard_bounced','complained','unsubscribed'))`
	automated := `EXISTS (SELECT 1 FROM contact_tags t WHERE t.project_id = c.project_id
		AND t.contact_id = c.id AND t.tag_name = 'automated')`
	reason := `CASE
		WHEN ` + fmt.Sprintf("%d", boolToInt(!includeAutomated)) + ` = 1 AND ` + automated + ` THEN 'automated'
		WHEN ` + healthy + ` THEN 'eligible'
		WHEN NOT EXISTS (SELECT 1 FROM contact_channels cc WHERE cc.project_id = c.project_id
			AND cc.contact_id = c.id AND cc.kind = '` + kind + `') THEN 'no_channel'
		WHEN EXISTS (SELECT 1 FROM contact_channels cc JOIN contact_channel_delivery_state ds
			ON ds.project_id = cc.project_id AND ds.channel_id = cc.id WHERE cc.project_id = c.project_id
			AND cc.contact_id = c.id AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `' AND ds.suppressed = 1) THEN 'suppressed'
		WHEN EXISTS (SELECT 1 FROM contact_channels cc JOIN contact_channel_delivery_state ds
			ON ds.project_id = cc.project_id AND ds.channel_id = cc.id WHERE cc.project_id = c.project_id
			AND cc.contact_id = c.id AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `' AND ds.quarantined = 1) THEN 'quarantined'
		WHEN EXISTS (SELECT 1 FROM contact_channels cc JOIN contact_channel_delivery_state ds
			ON ds.project_id = cc.project_id AND ds.channel_id = cc.id WHERE cc.project_id = c.project_id
			AND cc.contact_id = c.id AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `' AND ds.status = 'complained') THEN 'complained'
		WHEN EXISTS (SELECT 1 FROM contact_channels cc JOIN contact_channel_delivery_state ds
			ON ds.project_id = cc.project_id AND ds.channel_id = cc.id WHERE cc.project_id = c.project_id
			AND cc.contact_id = c.id AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `' AND ds.status = 'hard_bounced') THEN 'hard_bounced'
		WHEN EXISTS (SELECT 1 FROM contact_channels cc JOIN contact_channel_delivery_state ds
			ON ds.project_id = cc.project_id AND ds.channel_id = cc.id WHERE cc.project_id = c.project_id
			AND cc.contact_id = c.id AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `' AND ds.status = 'unsubscribed') THEN 'unsubscribed'
		ELSE 'unmessageable' END`
	address := `(SELECT cc.value FROM contact_channels cc
		JOIN contact_channel_delivery_state ds ON ds.project_id = cc.project_id AND ds.channel_id = cc.id
		WHERE cc.project_id = c.project_id AND cc.contact_id = c.id
		  AND cc.kind = '` + kind + `' AND ds.transport = '` + channel + `'
		  AND ds.suppressed = 0 AND ds.quarantined = 0
		  AND ds.status NOT IN ('hard_bounced','complained','unsubscribed')
		ORDER BY cc.is_primary DESC, cc.id ASC LIMIT 1)`

	result := &AudienceResolution{
		Channel:          channel,
		ExcludedByReason: map[string]int64{},
		Recipients:       []AudienceRecipient{},
		Exclusions:       []AudienceExclusion{},
	}
	if includeCounts {
		countSQL := `SELECT reason, COUNT(*) FROM (SELECT ` + reason + ` AS reason
			FROM (` + source.query + `) a JOIN contacts c ON c.id = a.contact_id) GROUP BY reason`
		rows, err := db.Query(countSQL, source.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var why string
			var count int64
			if err := rows.Scan(&why, &count); err != nil {
				rows.Close()
				return nil, err
			}
			result.RawCount += count
			if why == "eligible" {
				result.EligibleCount = count
			} else {
				result.ExcludedByReason[why] = count
				result.ExcludedCount += count
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	pageArgs := append(append([]any{}, source.args...), afterID, limit+1)
	pageSQL := `SELECT a.contact_id, COALESCE(` + address + `, ''), ` + reason + `
		FROM (` + source.query + `) a JOIN contacts c ON c.id = a.contact_id
		WHERE a.contact_id > ? ORDER BY a.contact_id LIMIT ?`
	rows, err := db.Query(pageSQL, pageArgs...)
	if err != nil {
		return nil, err
	}
	type pageRow struct {
		id      int64
		address string
		reason  string
	}
	page := []pageRow{}
	for rows.Next() {
		var row pageRow
		if err := rows.Scan(&row.id, &row.address, &row.reason); err != nil {
			rows.Close()
			return nil, err
		}
		page = append(page, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(page) > limit {
		result.HasMore = true
		page = page[:limit]
	}
	for _, row := range page {
		if row.reason == "eligible" {
			result.Recipients = append(result.Recipients, AudienceRecipient{ContactID: row.id, Address: row.address})
		} else {
			result.Exclusions = append(result.Exclusions, AudienceExclusion{ContactID: row.id, Reason: row.reason})
		}
		result.NextAfterContactID = row.id
	}
	if !result.HasMore {
		result.NextAfterContactID = 0
	}
	return result, nil
}
