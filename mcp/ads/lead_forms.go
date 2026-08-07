package main

import (
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var leadQuestionTypes = []string{
	"full_name", "first_name", "last_name", "email", "phone", "city", "state",
	"country", "postal_code", "job_title", "company_name", "custom",
}

func leadFormCreateSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":        map[string]any{"type": "integer"},
		"name":                 map[string]any{"type": "string"},
		"identity_resource_id": map[string]any{"type": "integer", "description": "Meta publishing identity. The account default is used when omitted."},
		"questions": map[string]any{
			"type": "array", "minItems": 1,
			"items": map[string]any{"type": "object", "properties": map[string]any{
				"type": map[string]any{"type": "string", "enum": leadQuestionTypes},
				"key":  map[string]any{"type": "string"}, "label": map[string]any{"type": "string"},
				"choices": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, "required": []string{"type"}},
		},
		"privacy_policy_url":         map[string]any{"type": "string"},
		"privacy_policy_link_text":   map[string]any{"type": "string"},
		"locale":                     map[string]any{"type": "string"},
		"follow_up_url":              map[string]any{"type": "string"},
		"business_name":              map[string]any{"type": "string"},
		"headline":                   map[string]any{"type": "string"},
		"description":                map[string]any{"type": "string"},
		"call_to_action":             map[string]any{"type": "string", "enum": []string{"learn_more", "get_quote", "apply_now", "sign_up", "contact_us", "subscribe", "download", "book_now"}},
		"call_to_action_description": map[string]any{"type": "string"},
		"intro":                      map[string]any{"type": "object"},
		"completion":                 map[string]any{"type": "object"},
		"higher_intent":              map[string]any{"type": "boolean"},
		"campaign_id":                map[string]any{"type": "string", "description": "Google campaign to attach the new lead-form asset to."},
		"set_default":                map[string]any{"type": "boolean", "default": true},
	}, []string{"ad_account_id", "name", "questions", "privacy_policy_url"})
}

func leadFormUpdateSchema() map[string]any {
	props := leadFormCreateSchema()["properties"].(map[string]any)
	delete(props, "questions")
	delete(props, "privacy_policy_url")
	props["lead_form_resource_id"] = map[string]any{"type": "integer"}
	return schemaObject(props, []string{"ad_account_id", "lead_form_resource_id"})
}

func leadFormListSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id": map[string]any{"type": "integer"},
		"refresh":       map[string]any{"type": "boolean", "default": true},
	}, []string{"ad_account_id"})
}

func leadFormGetSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":         map[string]any{"type": "integer"},
		"lead_form_resource_id": map[string]any{"type": "integer"},
		"refresh":               map[string]any{"type": "boolean"},
	}, []string{"ad_account_id", "lead_form_resource_id"})
}

func leadFormArchiveSchema() map[string]any {
	return schemaObject(map[string]any{
		"ad_account_id":         map[string]any{"type": "integer"},
		"lead_form_resource_id": map[string]any{"type": "integer"},
		"campaign_id":           map[string]any{"type": "string", "description": "Google campaign from which to detach the asset before removal."},
	}, []string{"ad_account_id", "lead_form_resource_id"})
}

func (a *App) toolLeadFormList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	refresh := true
	if value, ok := args["refresh"].(bool); ok {
		refresh = value
	}
	if refresh {
		resources, discoverErr := a.discoverResources(ctx, acct, resourceLeadForm)
		if discoverErr != nil {
			return discoverErr, nil
		}
		if err := a.replaceResources(ctx, acct, resourceLeadForm, resources); err != nil {
			return nil, err
		}
	}
	resources, err := a.listResources(ctx, acct, resourceLeadForm)
	if err != nil {
		return nil, err
	}
	data := make([]map[string]any, 0, len(resources))
	for _, resource := range resources {
		data = append(data, resource.response())
	}
	return map[string]any{"data": data}, nil
}

func (a *App) toolLeadFormGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	resourceID := int64(intArg(args, "lead_form_resource_id", 0))
	resource, err := a.getResource(ctx, acct, resourceID)
	if err != nil || resource.Kind != resourceLeadForm {
		return mcpError("lead form not found in this ad account"), nil
	}
	if refresh, _ := args["refresh"].(bool); refresh {
		if errOut := a.refreshLeadForm(ctx, acct, resource); errOut != nil {
			return errOut, nil
		}
		resource, err = a.getResource(ctx, acct, resourceID)
		if err != nil {
			return nil, err
		}
	}
	return resource.response(), nil
}

func (a *App) toolLeadFormCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	name := strings.TrimSpace(stringArgAny(args, "name"))
	privacyURL := strings.TrimSpace(stringArgAny(args, "privacy_policy_url"))
	questions, questionErr := normalizedLeadQuestions(args["questions"])
	if name == "" || privacyURL == "" || questionErr != nil {
		if questionErr != nil {
			return mcpError(questionErr.Error()), nil
		}
		return mcpError("name, questions, and privacy_policy_url required"), nil
	}

	var resource discoveredResource
	if acct.Platform == "meta" {
		page, selectionErr := a.resolveResourceChoice(ctx, acct, "publishing_identity", resourceIdentity, "facebook_page", int64(intArg(args, "identity_resource_id", 0)))
		if selectionErr != nil {
			return selectionErr, nil
		}
		input := map[string]any{
			"pageId": page.NativeID, "name": name, "questions": metaLeadQuestions(questions),
			"privacy_policy":           map[string]any{"url": privacyURL, "link_text": defaultString(stringArgAny(args, "privacy_policy_link_text"), "Privacy policy")},
			"is_optimized_for_quality": boolArg(args, "higher_intent"),
		}
		putString(input, "locale", args, "locale")
		putString(input, "follow_up_action_url", args, "follow_up_url")
		if value := asMap(args["intro"]); len(value) > 0 {
			input["context_card"] = value
		}
		if value := asMap(args["completion"]); len(value) > 0 {
			input["thank_you_page"] = value
		}
		parsed, providerErr := a.execIntegrationTool(ctx, acct, "leadform_create", input)
		if providerErr != nil {
			return providerErr, nil
		}
		nativeID := firstString(asMap(parsed), "id")
		if nativeID == "" {
			return mcpError("Meta did not return a lead form id"), nil
		}
		resource = discoveredResource{Kind: resourceLeadForm, ProviderType: "meta_lead_form", NativeID: nativeID, ParentNativeID: page.NativeID, DisplayName: name, Status: "active", Capabilities: []string{"lead_generation"}, Metadata: leadFormMetadata(args, questions), ManagedByApp: true}
	} else {
		for _, question := range questions {
			if stringArgAny(question, "type") == "custom" {
				return mcpError("Google Ads only accepts its predefined lead-form questions; custom labels are supported on Meta only"), nil
			}
		}
		asset := googleLeadFormAsset(acct, args, questions)
		create := map[string]any{"name": name, "leadFormAsset": asset}
		if followUp := stringArgAny(args, "follow_up_url"); followUp != "" {
			create["finalUrls"] = []any{followUp}
		}
		parsed, providerErr := a.execIntegrationTool(ctx, acct, "asset_mutate", map[string]any{
			"customer_id": acct.NativeAccountID,
			"operations":  []any{map[string]any{"create": create}},
		})
		if providerErr != nil {
			return providerErr, nil
		}
		nativeID := creativeAssetID(parsed, "lead_form")
		if nativeID == "" {
			return mcpError("Google Ads did not return a lead form asset resource name"), nil
		}
		if campaignID := strings.TrimSpace(stringArgAny(args, "campaign_id")); campaignID != "" {
			if attachErr := a.attachGoogleLeadForm(ctx, acct, campaignID, nativeID); attachErr != nil {
				_, _ = a.execIntegrationTool(ctx, acct, "asset_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"remove": nativeID}}})
				return attachErr, nil
			}
		}
		resource = discoveredResource{Kind: resourceLeadForm, ProviderType: "google_lead_form", NativeID: nativeID, DisplayName: name, Status: "active", Capabilities: []string{"lead_generation"}, Metadata: leadFormMetadata(args, questions), ManagedByApp: true}
	}

	created, err := a.upsertResource(ctx, acct, resource)
	if err != nil {
		return nil, err
	}
	setDefault := true
	if value, ok := args["set_default"].(bool); ok {
		setDefault = value
	}
	if setDefault {
		if err := a.setResourceDefault(ctx, acct, "lead_form", created.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"resource": created.response(), "default": setDefault}, nil
}

func (a *App) toolLeadFormUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	resource, errOut := a.resolveLeadForm(ctx, acct, args)
	if errOut != nil {
		return errOut, nil
	}
	if resource.Status == "archived" {
		return mcpError("archived lead forms cannot be updated"), nil
	}
	if acct.Platform == "meta" {
		input := map[string]any{"formId": resource.NativeID}
		putString(input, "name", args, "name")
		putString(input, "follow_up_action_url", args, "follow_up_url")
		if value := asMap(args["intro"]); len(value) > 0 {
			input["context_card"] = value
		}
		if value := asMap(args["completion"]); len(value) > 0 {
			input["thank_you_page"] = value
		}
		if len(input) == 1 {
			return mcpError("no supported lead form fields supplied"), nil
		}
		if _, providerErr := a.execIntegrationTool(ctx, acct, "leadform_update", input); providerErr != nil {
			return providerErr, nil
		}
	} else {
		name := defaultString(stringArgAny(args, "name"), resource.DisplayName)
		questions, _ := metadataQuestions(resource.Metadata)
		merged := mergeLeadFormArgs(resource.Metadata, args)
		asset := googleLeadFormAsset(acct, merged, questions)
		update := map[string]any{"resourceName": resource.NativeID, "name": name, "leadFormAsset": asset}
		mask := "name,lead_form_asset"
		if followUp := stringArgAny(merged, "follow_up_url"); followUp != "" {
			update["finalUrls"] = []any{followUp}
			mask += ",final_urls"
		}
		if _, providerErr := a.execIntegrationTool(ctx, acct, "asset_mutate", map[string]any{
			"customer_id": acct.NativeAccountID,
			"operations":  []any{map[string]any{"update": update, "updateMask": mask}},
		}); providerErr != nil {
			return providerErr, nil
		}
	}
	if name := strings.TrimSpace(stringArgAny(args, "name")); name != "" {
		resource.DisplayName = name
	}
	resource.Metadata = mergeLeadFormArgs(resource.Metadata, args)
	if err := a.updateLeadFormRecord(ctx, acct, resource); err != nil {
		return nil, err
	}
	return resource.response(), nil
}

func (a *App) toolLeadFormArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	resource, errOut := a.resolveLeadForm(ctx, acct, args)
	if errOut != nil {
		return errOut, nil
	}
	if acct.Platform == "meta" {
		if _, providerErr := a.execIntegrationTool(ctx, acct, "leadform_update", map[string]any{"formId": resource.NativeID, "status": "ARCHIVED"}); providerErr != nil {
			return providerErr, nil
		}
	} else {
		if campaignID := strings.TrimSpace(stringArgAny(args, "campaign_id")); campaignID != "" {
			customer := googleCustomerID(acct.NativeAccountID)
			campaignAsset := fmt.Sprintf("customers/%s/campaignAssets/%s~%s~LEAD_FORM", customer, googleAssetID(campaignID), googleAssetID(resource.NativeID))
			if _, providerErr := a.execIntegrationTool(ctx, acct, "campaign_asset_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"remove": campaignAsset}}}); providerErr != nil {
				return providerErr, nil
			}
		}
		if _, providerErr := a.execIntegrationTool(ctx, acct, "asset_mutate", map[string]any{"customer_id": acct.NativeAccountID, "operations": []any{map[string]any{"remove": resource.NativeID}}}); providerErr != nil {
			return providerErr, nil
		}
	}
	pid := strings.TrimSpace(ctx.CurrentProject())
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE ad_resources SET status='archived', refreshed_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND ad_account_id=?`, resource.ID, pid, acct.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM ad_resource_defaults WHERE project_id=? AND ad_account_id=? AND resource_id=?`, pid, acct.ID, resource.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	resource.Status = "archived"
	return resource.response(), nil
}

func (a *App) resolveLeadForm(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (*adResource, map[string]any) {
	resource, err := a.getResource(ctx, acct, int64(intArg(args, "lead_form_resource_id", 0)))
	wantType := acct.Platform + "_lead_form"
	if acct.Platform == "meta" {
		wantType = "meta_lead_form"
	}
	if err != nil || resource.Kind != resourceLeadForm || resource.ProviderType != wantType {
		return nil, mcpError("lead form not found in this ad account")
	}
	return resource, nil
}

func (a *App) refreshLeadForm(ctx *sdk.AppCtx, acct *adAccount, resource *adResource) map[string]any {
	if acct.Platform == "meta" {
		parsed, errOut := a.execIntegrationTool(ctx, acct, "leadform_get", map[string]any{"formId": resource.NativeID, "fields": "id,name,status,leads_count,questions,privacy_policy_url,created_time"})
		if errOut != nil {
			return errOut
		}
		row := asMap(parsed)
		if name := firstString(row, "name"); name != "" {
			resource.DisplayName = name
		}
		resource.Status = normalizedResourceStatus(firstString(row, "status"))
		resource.Metadata["leads_count"] = row["leads_count"]
		resource.Metadata["questions"] = row["questions"]
		resource.Metadata["privacy_policy_url"] = row["privacy_policy_url"]
		if err := a.updateLeadFormRecord(ctx, acct, resource); err != nil {
			return mcpError(err.Error())
		}
		return nil
	}
	resources, errOut := a.discoverResources(ctx, acct, resourceLeadForm)
	if errOut != nil {
		return errOut
	}
	if err := a.replaceResources(ctx, acct, resourceLeadForm, resources); err != nil {
		return mcpError(err.Error())
	}
	return nil
}

func (a *App) updateLeadFormRecord(ctx *sdk.AppCtx, acct *adAccount, resource *adResource) error {
	metadata, _ := json.Marshal(resource.Metadata)
	_, err := ctx.AppDB().Exec(`UPDATE ad_resources SET display_name=?, status=?, metadata_json=?, refreshed_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND ad_account_id=?`, resource.DisplayName, resource.Status, string(metadata), resource.ID, strings.TrimSpace(ctx.CurrentProject()), acct.ID)
	return err
}

func (a *App) setResourceDefault(ctx *sdk.AppCtx, acct *adAccount, purpose string, resourceID int64) error {
	_, err := ctx.AppDB().Exec(`INSERT INTO ad_resource_defaults (project_id, ad_account_id, purpose, resource_id) VALUES (?, ?, ?, ?) ON CONFLICT(project_id, ad_account_id, purpose) DO UPDATE SET resource_id=excluded.resource_id, updated_at=CURRENT_TIMESTAMP`, strings.TrimSpace(ctx.CurrentProject()), acct.ID, purpose, resourceID)
	return err
}

func (a *App) attachGoogleLeadForm(ctx *sdk.AppCtx, acct *adAccount, campaignID, assetResourceName string) map[string]any {
	customer := googleCustomerID(acct.NativeAccountID)
	_, errOut := a.execIntegrationTool(ctx, acct, "campaign_asset_mutate", map[string]any{
		"customer_id": acct.NativeAccountID,
		"operations": []any{map[string]any{"create": map[string]any{
			"campaign": googleCampaignResource(customer, campaignID),
			"asset":    assetResourceName, "fieldType": "LEAD_FORM",
		}}},
	})
	return errOut
}

func normalizedLeadQuestions(raw any) ([]map[string]any, error) {
	rows, ok := raw.([]any)
	if !ok || len(rows) == 0 {
		return nil, fmt.Errorf("questions must contain at least one question")
	}
	allowed := map[string]bool{}
	for _, value := range leadQuestionTypes {
		allowed[value] = true
	}
	out := make([]map[string]any, 0, len(rows))
	for index, rawRow := range rows {
		row := asMap(rawRow)
		kind := strings.ToLower(strings.TrimSpace(stringArgAny(row, "type")))
		if !allowed[kind] {
			return nil, fmt.Errorf("question %d has unsupported type", index+1)
		}
		question := map[string]any{"type": kind}
		if kind == "custom" {
			key, label := strings.TrimSpace(stringArgAny(row, "key")), strings.TrimSpace(stringArgAny(row, "label"))
			if key == "" || label == "" {
				return nil, fmt.Errorf("custom question %d requires key and label", index+1)
			}
			question["key"], question["label"] = key, label
			if choices, ok := row["choices"].([]any); ok && len(choices) > 0 {
				question["choices"] = choices
			}
		}
		out = append(out, question)
	}
	return out, nil
}

func metaLeadQuestions(questions []map[string]any) []any {
	out := make([]any, 0, len(questions))
	for _, question := range questions {
		item := map[string]any{"type": strings.ToUpper(stringArgAny(question, "type"))}
		for _, key := range []string{"key", "label", "choices"} {
			if value, ok := question[key]; ok {
				item[key] = value
			}
		}
		out = append(out, item)
	}
	return out
}

func googleLeadFormAsset(acct *adAccount, args map[string]any, questions []map[string]any) map[string]any {
	fields := make([]any, 0, len(questions))
	googleTypes := map[string]string{"phone": "PHONE_NUMBER", "postal_code": "POSTAL_CODE", "company_name": "COMPANY_NAME"}
	for _, question := range questions {
		kind := stringArgAny(question, "type")
		inputType := strings.ToUpper(kind)
		if mapped := googleTypes[kind]; mapped != "" {
			inputType = mapped
		}
		field := map[string]any{"inputType": inputType}
		if kind == "custom" {
			field["inputType"] = "CUSTOM_QUESTION"
			field["name"] = stringArgAny(question, "label")
		}
		fields = append(fields, field)
	}
	name := stringArgAny(args, "name")
	asset := map[string]any{
		"businessName":            defaultString(stringArgAny(args, "business_name"), name),
		"headline":                defaultString(stringArgAny(args, "headline"), name),
		"description":             defaultString(stringArgAny(args, "description"), "Request more information"),
		"callToActionType":        googleLeadCTA(stringArgAny(args, "call_to_action")),
		"callToActionDescription": defaultString(stringArgAny(args, "call_to_action_description"), "Submit the form to get in touch"),
		"privacyPolicyUrl":        stringArgAny(args, "privacy_policy_url"),
		"fields":                  fields,
		"desiredIntent":           map[bool]string{true: "HIGH_INTENT", false: "LOW_INTENT"}[boolArg(args, "higher_intent")],
		"postSubmitHeadline":      "Thank you",
		"postSubmitDescription":   "Your information was submitted.",
	}
	if stringArgAny(args, "follow_up_url") != "" {
		asset["postSubmitCallToActionType"] = "VISIT_SITE"
	}
	if completion := asMap(args["completion"]); len(completion) > 0 {
		if value := firstString(completion, "title", "headline"); value != "" {
			asset["postSubmitHeadline"] = value
		}
		if value := firstString(completion, "body", "description"); value != "" {
			asset["postSubmitDescription"] = value
		}
	}
	_ = acct
	return asset
}

func leadFormMetadata(args map[string]any, questions []map[string]any) map[string]any {
	return map[string]any{
		"questions": questions, "privacy_policy_url": stringArgAny(args, "privacy_policy_url"),
		"follow_up_url": stringArgAny(args, "follow_up_url"), "business_name": stringArgAny(args, "business_name"),
		"headline": stringArgAny(args, "headline"), "description": stringArgAny(args, "description"),
		"call_to_action": stringArgAny(args, "call_to_action"), "call_to_action_description": stringArgAny(args, "call_to_action_description"),
		"higher_intent": boolArg(args, "higher_intent"), "locale": stringArgAny(args, "locale"),
	}
}

func mergeLeadFormArgs(metadata, args map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+len(args))
	for key, value := range metadata {
		out[key] = value
	}
	for key, value := range args {
		if value != nil && value != "" {
			out[key] = value
		}
	}
	return out
}

func metadataQuestions(metadata map[string]any) ([]map[string]any, error) {
	if rows, ok := metadata["questions"].([]map[string]any); ok {
		return rows, nil
	}
	return normalizedLeadQuestions(metadata["questions"])
}

func googleLeadCTA(value string) string {
	mapping := map[string]string{"learn_more": "LEARN_MORE", "get_quote": "GET_QUOTE", "apply_now": "APPLY_NOW", "sign_up": "SIGN_UP", "contact_us": "CONTACT_US", "subscribe": "SUBSCRIBE", "download": "DOWNLOAD", "book_now": "BOOK_NOW"}
	if mapped := mapping[strings.ToLower(value)]; mapped != "" {
		return mapped
	}
	return "GET_QUOTE"
}

func googleAssetID(resourceName string) string {
	parts := strings.Split(strings.TrimSpace(resourceName), "/")
	return parts[len(parts)-1]
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}
