export interface TemplateSnapshot {
  template_id?: number;
  name?: string;
  provider_template_id?: string;
  captured_at?: string;
  content_source: string;
  availability: string;
  variables?: Record<string, unknown>;
  body_text?: string;
  body_html?: string;
  missing_variables?: string[];
}

// No live template lookups and no HTML injection. A historical placeholder is
// explicitly unavailable rather than presented as the actual sent content.
export function templateMessageDisplay(body: string, snapshot?: TemplateSnapshot) {
  const legacy = /^\(template #\d+\)$/.test((body || "").trim()) || /^\(content_sid\b/.test((body || "").trim());
  if (!snapshot) return {
    body,
    label: legacy ? "Template message" : "",
    notice: legacy ? "Sent content was not saved for this historical message." : "",
  };
  const label = `Template${snapshot.name ? ` · ${snapshot.name}` : ""}${snapshot.template_id ? ` (#${snapshot.template_id})` : ""}`;
  const complete = snapshot.availability === "complete" && !!snapshot.body_text;
  return {
    body: complete ? snapshot.body_text! : legacy ? "" : body,
    label,
    notice: complete
      ? snapshot.content_source === "provider_response" ? "Content confirmed by provider" : "Content captured when sending"
      : snapshot.availability === "incomplete" ? "Exact sent content unavailable: some template values were not captured." : "Exact sent text unavailable for this template message.",
  };
}
