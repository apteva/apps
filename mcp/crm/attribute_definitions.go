package main

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// Unlike the HTTP reader, discovery must not seed definitions. Include system
// defaults in the result without materializing them in a previously untouched
// project; persisted project definitions always take precedence.
func (a *App) toolAttributeDefinitionsList(callCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := callCtx.Err(); err != nil {
		return nil, err
	}
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppReadDB().QueryContext(callCtx, `
	 SELECT key,label,type,COALESCE(enum_values,''),required,sort_order,is_system
	 FROM contact_attribute_defs WHERE project_id=?
	 UNION ALL
	 SELECT t.key,t.label,t.type,COALESCE(t.enum_values,''),0,t.sort_order,1
	 FROM _system_attribute_templates t
	 WHERE NOT EXISTS(SELECT 1 FROM contact_attribute_defs d WHERE d.project_id=? AND d.key=t.key)
	 ORDER BY sort_order,key`, pid, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	defs := []map[string]any{}
	for rows.Next() {
		var key, label, typ, enumJSON string
		var required, sortOrder, system int
		if err := rows.Scan(&key, &label, &typ, &enumJSON, &required, &sortOrder, &system); err != nil {
			return nil, err
		}
		allowed := []string{}
		if enumJSON != "" && (typ == "select" || typ == "multi_select") {
			if err := json.Unmarshal([]byte(enumJSON), &allowed); err != nil {
				return nil, fmt.Errorf("attribute %q has invalid allowed values: %w", key, err)
			}
			if allowed == nil {
				allowed = []string{}
			}
		}
		defs = append(defs, map[string]any{"key": key, "label": label, "type": typ, "enum_values": allowed, "required": required != 0, "sort_order": sortOrder, "is_system": system != 0})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"attribute_defs": defs, "count": len(defs)}, nil
}
