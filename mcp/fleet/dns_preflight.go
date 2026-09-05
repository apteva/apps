package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Initial ownership must not destroy a pre-existing provider record on rollback.
// Existing DNS can be used with manage_dns=false for a primary hostname; a
// delegation requires an unassigned zone. Never silently take over external DNS.
func ensureNewDNSRecord(ctx *sdk.AppCtx, project, apex, name, kind string) error {
	var reply struct {
		Records []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"records"`
	}
	if err := callDomainsTool(ctx, project, "domain_records_list", map[string]any{"domain": apex, "name": name, "type": kind}, &reply); err != nil {
		return err
	}
	fqdn := composeRecordFQDN(apex, name)
	for _, record := range reply.Records {
		if strings.EqualFold(record.Type, kind) && (strings.EqualFold(strings.TrimSuffix(record.Name, "."), fqdn) || strings.EqualFold(record.Name, name)) {
			return fmt.Errorf("DNS %s %s already exists; preserve it with manage_dns=false or explicitly retire the existing record before assigning managed ownership", kind, fqdn)
		}
	}
	return nil
}
