package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func scopedSESConfigName(ctx *sdk.AppCtx, connID int64) string {
	id, _ := ctx.PlatformAPI().WhoAmI()
	identity := os.Getenv("APTEVA_INSTALL_ID")
	if id != nil && id.InstallID > 0 {
		identity = strconv.FormatInt(id.InstallID, 10)
	}
	if identity == "" {
		identity = "local:" + os.Getenv("APTEVA_PROJECT_ID")
	}
	sum := sha256.Sum256([]byte(os.Getenv("APTEVA_GATEWAY_URL") + ":" + identity + ":" + strconv.FormatInt(connID, 10)))
	return "apteva-messaging-" + hex.EncodeToString(sum[:8])
}
func sesConfigName(ctx *sdk.AppCtx, connID int64) string {
	var name string
	if err := ctx.AppDB().QueryRow(`SELECT value FROM messaging_settings WHERE name=?`, fmt.Sprintf("ses_config:%d", connID)).Scan(&name); err == nil && name != "" {
		return name
	}
	// Existing installations keep receiving events until their next setup refresh.
	return sesEventConfigurationSetName
}
func saveSESConfigName(ctx *sdk.AppCtx, connID int64, name string) error {
	_, err := ctx.AppDB().Exec(`INSERT INTO messaging_settings(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, fmt.Sprintf("ses_config:%d", connID), name)
	return err
}
func webhookRoutingQuery(ctx *sdk.AppCtx) url.Values {
	q := url.Values{}
	installID := os.Getenv("APTEVA_INSTALL_ID")
	if ctx != nil {
		if id, err := ctx.PlatformAPI().WhoAmI(); err == nil && id != nil && id.InstallID > 0 {
			installID = strconv.FormatInt(id.InstallID, 10)
		}
	}
	if installID != "" {
		q.Set("install_id", installID)
	}
	return q
}

func readReceiptRule(ctx *sdk.AppCtx, connID int64, set, name string) (map[string]any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "describe_receipt_rule", map[string]any{"RuleSetName": set, "RuleName": name})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("describe receipt rule: %s", truncateResData(res))
	}
	var root map[string]any
	if err := json.Unmarshal(res.Data, &root); err != nil {
		return nil, err
	}
	for _, key := range []string{"DescribeReceiptRuleResponse", "DescribeReceiptRuleResult"} {
		if v, ok := root[key].(map[string]any); ok {
			root = v
		}
	}
	rule, ok := root["Rule"].(map[string]any)
	if !ok || rule["Name"] != name {
		return nil, errors.New("invalid receipt rule response; refusing to overwrite")
	}
	if actions, ok := rule["Actions"]; !ok || !validAWSActions(actions) {
		return nil, errors.New("receipt rule response missing actions")
	}
	if _, err := receiptRecipients(rule["Recipients"]); err != nil {
		return nil, err
	}
	return rule, nil
}
func flattenAWSQuery(out map[string]any, prefix string, v any) {
	switch x := v.(type) {
	case map[string]any:
		for key, value := range x {
			if key == "member" {
				switch value.(type) {
				case []any, []string:
					flattenAWSQuery(out, prefix, value)
				default:
					flattenAWSQuery(out, prefix+".member.1", value)
				}
			} else {
				flattenAWSQuery(out, prefix+"."+key, value)
			}
		}
	case []any:
		for i, value := range x {
			flattenAWSQuery(out, fmt.Sprintf("%s.member.%d", prefix, i+1), value)
		}
	case []string:
		for i, value := range x {
			flattenAWSQuery(out, fmt.Sprintf("%s.member.%d", prefix, i+1), value)
		}
	case nil:
	default:
		out[prefix] = fmt.Sprint(x)
	}
}
func activeReceiptRuleSet(ctx *sdk.AppCtx, connID int64) (string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "describe_active_receipt_rule_set", map[string]any{})
	if err != nil {
		return "", err
	}
	if res == nil || !res.Success {
		return "", fmt.Errorf("read active receipt ruleset: %s", truncateResData(res))
	}
	var root map[string]any
	if err := json.Unmarshal(res.Data, &root); err != nil {
		return "", err
	}
	for _, key := range []string{"DescribeActiveReceiptRuleSetResponse", "DescribeActiveReceiptRuleSetResult"} {
		if v, ok := root[key].(map[string]any); ok {
			root = v
		}
	}
	if m, ok := root["Metadata"].(map[string]any); ok {
		name := strings.TrimSpace(firstStringField(m, "Name"))
		if name == "" {
			return "", errors.New("active ruleset metadata is missing its name; refusing to switch rulesets")
		}
		return name, nil
	}
	if len(root) == 0 {
		return "", nil
	} // SES returns an empty result when none is active.
	return "", errors.New("unrecognized active ruleset response")
}

// Merge the app-owned statement only; preserve other services' bucket grants.
func mergeBucketPolicy(existing, desired []byte) (string, error) {
	var old, next map[string]any
	if len(existing) == 0 {
		existing = []byte(`{"Version":"2012-10-17","Statement":[]}`)
	}
	if err := json.Unmarshal(existing, &old); err != nil {
		return "", err
	}
	if err := json.Unmarshal(desired, &next); err != nil {
		return "", err
	}
	list, ok := old["Statement"].([]any)
	if !ok {
		if one, yes := old["Statement"].(map[string]any); yes {
			list = []any{one}
		} else {
			return "", errors.New("invalid bucket policy statements")
		}
	}
	additions := next["Statement"].([]any)
	for _, add := range additions {
		a := add.(map[string]any)
		found := false
		for i, item := range list {
			if row, ok := item.(map[string]any); ok && row["Sid"] == a["Sid"] {
				list[i] = a
				found = true
				break
			}
		}
		if !found {
			list = append(list, a)
		}
	}
	old["Statement"] = list
	return string(mustJSON(old)), nil
}

func validateProviderRegions(ctx *sdk.AppCtx, requested string, connIDs ...int64) (string, error) {
	region := strings.TrimSpace(requested)
	for _, connID := range connIDs {
		if connID == 0 {
			continue
		}
		observed := strings.TrimSpace(lookupConnectionCredential(ctx, connID, "region"))
		if observed == "" {
			return "", fmt.Errorf("cannot read region for provider connection %d; configure its region and credential access before DNS/bootstrap", connID)
		}
		if region != "" && region != observed {
			return "", fmt.Errorf("provider connection %d uses region %s, expected %s", connID, observed, region)
		}
		region = observed
	}
	if region == "" {
		region = "eu-west-1"
	}
	return region, nil
}

// Public metadata must not reproduce credentials from legacy callback URLs.
func redactCallbackCredentials(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = redactCallbackCredentials(item)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = redactCallbackCredentials(item)
		}
		return v
	case string:
		u, err := url.Parse(v)
		if err == nil && u.Host != "" {
			q := u.Query()
			if q.Has("api_key") {
				q.Del("api_key")
				u.RawQuery = q.Encode()
				return u.String()
			}
		}
	}
	return value
}

type setupDNSRecord struct{ ID, Name, Type, Value string }

func planDNSRecord(existing []setupDNSRecord, domain, name, kind, value string) (next, recordID string, unchanged bool, err error) {
	matching := []setupDNSRecord{}
	fq := name + "." + domain
	if name == "@" {
		fq = domain
	}
	for _, row := range existing {
		if strings.EqualFold(row.Type, kind) && (strings.EqualFold(strings.TrimSuffix(row.Name, "."), fq) || strings.EqualFold(row.Name, name)) {
			matching = append(matching, row)
		}
	}
	if len(matching) == 0 {
		return value, "", false, nil
	}
	if kind == "TXT" && strings.HasPrefix(value, "v=spf1 ") {
		count := 0
		for _, row := range matching {
			if strings.HasPrefix(strings.Trim(row.Value, "\""), "v=spf1 ") {
				count++
			}
		}
		if count > 1 {
			return "", "", false, errors.New("multiple SPF records; resolve the existing DNS conflict before setup")
		}
	}
	for _, row := range matching {
		if strings.EqualFold(strings.Trim(row.Value, "\""), value) {
			return value, row.ID, true, nil
		}
	}
	if kind == "TXT" && strings.HasPrefix(value, "v=spf1 ") {
		for _, row := range matching {
			current := strings.Trim(row.Value, "\"")
			if !strings.HasPrefix(current, "v=spf1 ") {
				continue
			}
			if strings.Contains(current, "include:amazonses.com") {
				return current, row.ID, true, nil
			}
			fields := strings.Fields(current)
			index := len(fields)
			for i, field := range fields {
				if strings.HasSuffix(field, "all") || strings.HasPrefix(field, "redirect=") {
					index = i
					break
				}
			}
			fields = append(fields[:index], append([]string{"include:amazonses.com"}, fields[index:]...)...)
			if row.ID == "" {
				return "", "", false, errors.New("SPF update needs an unambiguous record id; existing DNS left unchanged")
			}
			return strings.Join(fields, " "), row.ID, false, nil
		}
	}
	if kind == "TXT" && strings.HasPrefix(value, "v=DMARC1;") {
		for _, row := range matching {
			if strings.HasPrefix(strings.Trim(row.Value, "\""), "v=DMARC1;") {
				return row.Value, row.ID, true, nil
			}
		}
	}
	return "", "", false, fmt.Errorf("existing %s %s conflicts with proposed %q; existing DNS left unchanged", kind, fq, value)
}

// SES XML may encode an empty list as null/empty text and a singleton as member.
// Unknown structures are errors: never turn uncertain parsing into a catch-all.
func receiptRecipients(v any) ([]string, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		if x == "" {
			return nil, nil
		}
		return []string{x}, nil
	case map[string]any:
		if len(x) == 0 {
			return nil, nil
		}
		if member, ok := x["member"]; ok && len(x) == 1 {
			return receiptRecipients(member)
		}
	case []any:
		out := []string{}
		for _, item := range x {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, errors.New("invalid receipt rule recipients")
			}
			out = append(out, text)
		}
		return out, nil
	}
	return nil, errors.New("invalid receipt rule recipients; existing rule left unchanged")
}
func validAWSActions(v any) bool {
	if m, ok := v.(map[string]any); ok {
		if member, yes := m["member"]; yes && len(m) == 1 {
			return validAWSActions(member)
		}
		if len(m) != 1 {
			return false
		}
		for name, action := range m {
			if !strings.HasSuffix(name, "Action") {
				return false
			}
			fields, ok := action.(map[string]any)
			return ok && len(fields) > 0
		}
	}
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return false
		}
		for _, item := range list {
			if !validAWSActions(item) {
				return false
			}
		}
		return true
	}
	return false
}
