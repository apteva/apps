package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	mediaFacetDefaultLimit = 20
	mediaFacetMaxLimit     = 50
)

type MediaFacetBucket struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type MediaFacetResult struct {
	Buckets    []MediaFacetBucket `json:"buckets"`
	OtherCount int                `json:"other_count"`
}

func mediaFacetExpression(name string) (string, error) {
	switch name {
	case "content_type":
		return `CASE WHEN m.is_image=1 THEN 'image' WHEN m.has_video=1 THEN 'video' WHEN m.has_audio=1 THEN 'audio' ELSE 'unknown' END`, nil
	case "audience_rating":
		return `COALESCE(NULLIF(m.audience_rating,''),'unrated')`, nil
	case "patreon_status":
		return `COALESCE(NULLIF(CAST(json_extract(m.metadata,'$.patreon.status') AS TEXT),''),'unknown')`, nil
	case "model":
		return `COALESCE(NULLIF(CAST(json_extract(m.metadata,'$.model.id') AS TEXT),''),NULLIF(CAST(json_extract(m.metadata,'$.model') AS TEXT),''),NULLIF(CAST(json_extract(m.metadata,'$.model_id') AS TEXT),''),'unknown')`, nil
	case "session":
		return `COALESCE(NULLIF(CAST(json_extract(m.metadata,'$.lineage.session_id') AS TEXT),''),NULLIF(CAST(json_extract(m.metadata,'$.session.id') AS TEXT),''),NULLIF(CAST(json_extract(m.metadata,'$.session_id') AS TEXT),''),'unknown')`, nil
	case "hosting_readiness":
		return `CASE
			WHEN json_type(m.metadata,'$.hosting') IS NULL THEN 'unknown'
			WHEN ` + mediaHostingReadinessSQL("m.metadata") + `=1 THEN 'ready'
			ELSE 'not_ready' END`, nil
	case "recording_month":
		return `COALESCE(NULLIF(substr(CAST(json_extract(m.metadata,'$.recording_date') AS TEXT),1,7),''),'unknown')`, nil
	default:
		return "", fmt.Errorf("unsupported media facet %q", name)
	}
}

func mediaFacet(db *sql.DB, projectID string, f SearchFilters, name string, limit, total int) (MediaFacetResult, error) {
	expr, err := mediaFacetExpression(name)
	if err != nil {
		return MediaFacetResult{}, err
	}
	f.Cursor = nil
	f.Offset = 0
	clauses, args := buildMediaSearchWhere(projectID, f)
	args = append(args, limit)
	rows, err := db.Query(`SELECT `+expr+` AS facet_value, COUNT(*) AS facet_count
		FROM media m WHERE `+strings.Join(clauses, " AND ")+`
		GROUP BY facet_value ORDER BY facet_count DESC, facet_value ASC LIMIT ?`, args...)
	if err != nil {
		return MediaFacetResult{}, err
	}
	defer rows.Close()
	result := MediaFacetResult{Buckets: []MediaFacetBucket{}}
	visible := 0
	for rows.Next() {
		var bucket MediaFacetBucket
		if err := rows.Scan(&bucket.Value, &bucket.Count); err != nil {
			return MediaFacetResult{}, err
		}
		result.Buckets = append(result.Buckets, bucket)
		visible += bucket.Count
	}
	result.OtherCount = total - visible
	if result.OtherCount < 0 {
		result.OtherCount = 0
	}
	return result, rows.Err()
}

func (a *App) toolInventory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	groups, err := stringListArg(args["group_by"], "group_by", 7)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, errors.New("group_by requires at least one facet")
	}
	f, err := mediaSearchFiltersFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := int(int64Arg(args["group_limit"]))
	if limit <= 0 {
		limit = mediaFacetDefaultLimit
	}
	if limit > mediaFacetMaxLimit {
		limit = mediaFacetMaxLimit
	}
	total, err := mediaSearchCount(ctx.AppDB(), pid, f, false)
	if err != nil {
		return nil, err
	}
	groupedCounts := make(map[string]MediaFacetResult, len(groups))
	for _, group := range groups {
		if _, exists := groupedCounts[group]; exists {
			continue
		}
		facet, ferr := mediaFacet(ctx.AppDB(), pid, f, group, limit, total)
		if ferr != nil {
			return nil, ferr
		}
		groupedCounts[group] = facet
	}
	return map[string]any{
		"total":            total,
		"groups":           groupedCounts,
		"query":            mediaSearchQueryEcho(f),
		"records_returned": 0,
	}, nil
}
