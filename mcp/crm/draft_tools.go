package main

import sdk "github.com/apteva/app-sdk"

func (a *App) draftTools() []sdk.Tool {
	fields := map[string]any{
		"channel": map[string]any{"type": "string", "enum": []string{"email", "sms", "whatsapp"}},
		"from":    map[string]any{"type": "string"}, "to": map[string]any{"type": "string", "description": "Pinned recipient. For first outreach, optionally choose an existing contact channel; otherwise defaults to the primary channel. Replies/follow-ups must match the original message recipient. Updates cannot redirect a draft."},
		"subject": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "body_html": map[string]any{"type": "string"},
		"template_id": map[string]any{"type": "integer", "minimum": 0}, "content_sid": map[string]any{"type": "string"}, "template_vars": map[string]any{"type": "object"},
		"attachments": map[string]any{"type": "array", "items": messageAttachmentInputSchema(), "maxItems": maxMessageAttachments},
		"source":      map[string]any{"type": "string", "description": "Authorship label, for example human or agent:Name; not an authorization grant."},
	}
	create := map[string]any{}
	update := map[string]any{}
	for key, value := range fields {
		create[key] = value
		update[key] = value
	}
	create["conversation_id"] = map[string]any{"type": "integer", "minimum": 1}
	create["contact_id"] = map[string]any{"type": "integer", "minimum": 1}
	create["mode"] = map[string]any{"type": "string", "enum": []string{"reply", "message"}, "description": "Defaults to reply with conversation_id, otherwise message. Use message for first outreach or outbound-only follow-ups; reply requires an inbound anchor."}
	create["reply_to_activity_id"] = map[string]any{"type": "integer", "minimum": 1}
	create["client_key"] = map[string]any{"type": "string", "description": "Optional unique client operation key; repeating it returns the original draft without creating duplicates."}
	createSchema := schemaObject(create, nil)
	createSchema["anyOf"] = []any{map[string]any{"required": []string{"conversation_id"}}, map[string]any{"required": []string{"contact_id", "channel"}}}
	id := map[string]any{"type": "integer", "minimum": 1}
	update["id"] = id
	update["expected_revision"] = id
	safe := map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}
	read := map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false}
	return []sdk.Tool{
		{Name: "conversation_drafts_create", Description: "SAVE ONLY — NEVER SENDS. Save incomplete or complete CRM drafts: replies, first outreach to an existing contact, or outbound follow-ups without an inbound message. Supply conversation_id for replies (default mode:reply, pins inbound anchor); supply contact_id and channel for first outreach (mode:message), or mode:message and conversation_id for an outbound follow-up (pins historical outbound recipient). Saving never creates activities/conversations or changes their status. Email/SMS/WhatsApp, HTML, templates and attachments supported. Multiple drafts allowed. Returns draft.id and revision. Not Gmail-synced. Examples: {\"conversation_id\":123,\"body\":\"Proposed reply\"}; {\"mode\":\"message\",\"contact_id\":42,\"channel\":\"email\",\"subject\":\"Introduction\",\"body\":\"Hello\"}; {\"mode\":\"message\",\"conversation_id\":123,\"body\":\"Following up\"}.", InputSchema: createSchema, Annotations: safe, Handler: a.toolDraftCreate},
		{Name: "conversation_drafts_get", Description: "Read a saved CRM draft (reply or message) with its complete content, attachments, mode, revision and status. No send. Use before editing or explicitly sending; sending/sent/send_failed drafts are not editable.", InputSchema: schemaObject(map[string]any{"id": id}, []string{"id"}), Annotations: read, Handler: a.toolDraftGet},
		{Name: "conversation_drafts_list", Description: "Read saved draft summaries by conversation_id OR contact_id (includes first outreach drafts without a conversation); default excludes sent/discarded drafts. Full bodies/attachments are not included: use conversation_drafts_get. Returns drafts,total,limit,offset. No send.", InputSchema: schemaObject(map[string]any{"conversation_id": id, "contact_id": id, "include_finished": map[string]any{"type": "boolean"}, "limit": id, "offset": map[string]any{"type": "integer", "minimum": 0}}, nil), Annotations: read, Handler: a.toolDraftList},
		{Name: "conversation_drafts_update", Description: "SAVE ONLY — NEVER SENDS. Partial update to an editable draft. Requires id and expected_revision from get/create/update; a concurrent edit returns a conflict, never overwrites newer content. Recipient/conversation/mode/anchor stay pinned. Message drafts also pin channel. Reply drafts permit explicit SMS/WhatsApp switch with a new channel and sender; email cannot switch. Empty strings, attachments:[], template_id:0 and template_vars:{} intentionally clear those fields.", InputSchema: schemaObject(update, []string{"id", "expected_revision"}), Annotations: safe, Handler: a.toolDraftUpdate},
		{Name: "conversation_drafts_discard", Description: "Soft-discard an editable saved draft; NEVER SENDS and keeps its content for audit. Requires id and expected_revision. Cannot discard sent/in-progress/uncertain drafts; resolve uncertain delivery by retrying the same draft.", InputSchema: schemaObject(map[string]any{"id": id, "expected_revision": id, "source": fields["source"]}, []string{"id", "expected_revision"}), Annotations: safe, Handler: a.toolDraftDiscard},
		{Name: "conversation_drafts_send", Description: "REAL EXTERNAL SEND. Use ONLY when the user explicitly approves sending this saved draft, never for drafting/configuration checks. Requires id and expected_revision. Rechecks pinned recipient, contact/conversation/project ownership, bound Messaging source, verified sender, suppression, do-not-contact and WhatsApp window. Replies/follow-ups keep their conversation and email threading; first outreach creates a conversation only when sent. Never falls back to another recipient. Repeated sends/retries use a durable idempotency key; an already-sent draft returns the original result. Failures preserve the draft and return sent:false,error and fresh revision. If status=send_failed, delivery is uncertain: retry THIS SAME draft; never make a replacement send or edit its content.", InputSchema: schemaObject(map[string]any{"id": id, "expected_revision": id}, []string{"id", "expected_revision"}), Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true, "idempotentHint": true}, Handler: a.toolDraftSend},
	}
}
