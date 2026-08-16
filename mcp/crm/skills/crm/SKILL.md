---
name: crm
description: Use CRM tools for contacts, customer conversations, lists, segments, leads, opportunities, pipelines, and messaging-related CRM work. Activate when the user asks about customers, contacts, inbox triage, sales stages, active leads, or CRM health.
compatibility: Requires the CRM MCP tools supplied by an Apteva app installation.
metadata:
  author: apteva
  version: "1.0"
---

# CRM

Use the CRM tools as the authoritative, project-scoped source for contacts,
customer conversations, lists, segments, opportunities, and pipelines.

## Operating rules

1. Read before writing. Retrieve the relevant contact, conversation, list,
   segment, pipeline, or opportunity before changing it.
2. Use the narrowest tool that answers the request. Do not hand a simple CRM
   lookup to another thread when the needed CRM tool is available here.
3. Never invent IDs, counts, stages, senders, or delivery results. Report the
   values returned by the tools.
4. Treat create, update, merge, archive, status, and message operations as
   mutations. Confirm ambiguous targets or destructive intent first.
5. CRM data is scoped by the host to the current Apteva project. Do not imply
   that a result covers another project unless the host explicitly provides
   that scope.

## Contacts and conversations

- Prefer `contacts_search` to locate a person. Use `contacts_get` for the
  authoritative current record: core fields, channels, tags, custom
  attributes, and active list memberships.
- `contacts_get` intentionally excludes activities/notes, conversations, and
  opportunities. Never interpret those resources as empty from that response;
  use `contacts_get_context` before a consequential change or whenever history
  or sales context matters.
- `contacts_get_context` returns bounded recent collections. Inspect each
  `collection_info.<resource>.truncated` value before claiming that the returned
  history is complete.
- Use `contacts_upsert_by_channel` only when find-or-create behavior is
  intended. Preserve and report its `was_created` result.
- Use `conversations_inbox` for cross-contact triage and
  `contacts_get_conversation` for one conversation's evidence.
- Use `contacts_set_conversation_status` only when the user asks to change
  workflow state or when the requested workflow clearly requires it.

## Lists, segments, and pipeline

- Lists are explicit memberships. Segments are saved predicates or snapshots.
  Choose the model that matches the request rather than treating them as the
  same thing.
- Inspect pipelines and their valid stages before creating or moving an
  opportunity.
- Use opportunity search for pipeline totals and current sales work. State the
  filters used when a count could otherwise be ambiguous.

## Messaging safety

- `contacts_send_message`, `contacts_reply`, and `contacts_send_test` create
  real external messages. Call them only when the user explicitly requests a
  send or a previously approved workflow requires it.
- Use `messaging_senders_list` for configuration checks. Never send a
  placeholder message merely to test whether a sender is configured.
- Preserve returned delivery, threading, and deduplication information in the
  user-facing result.

## Reporting

Give a concise answer with the relevant CRM evidence. For multi-step work,
publish meaningful progress at phase boundaries, not for every read. If a tool
fails, identify the failed operation and leave the CRM state unclaimed rather
than guessing whether a mutation succeeded.
