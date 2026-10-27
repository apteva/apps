export function audienceTypeLabel(type: string): string {
  return ({ customer_list: "Customer list", website: "Website visitors", app_activity: "App activity", engagement: "Engagement", lookalike: "Lookalike", saved_targeting: "Saved targeting", custom_segment: "Custom segment", grouped: "Grouped audience", remarketing: "Remarketing", rule_based: "Rule-based audience", combined: "Combined audience", unknown: "Unknown audience type" } as Record<string, string>)[type] || type.replaceAll("_", " ");
}
