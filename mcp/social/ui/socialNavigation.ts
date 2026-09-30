export function socialNavigation(search: string) {
  const params = new URLSearchParams(search);
  const positiveID = (value: string | null) => value && /^\d+$/.test(value) && Number.isSafeInteger(Number(value)) && Number(value) > 0 ? Number(value) : undefined;
  const requestedTab = params.get("tab");
  const tab: "posts" | "accounts" | "inbox" | "metrics" = requestedTab === "accounts" || requestedTab === "inbox" || requestedTab === "metrics" ? requestedTab : "posts";
  const status = params.get("status") || "all";
  const anchor = params.get("anchor_date");
  return {
    tab,
    profileID: params.get("profile_id") === "0" ? null : positiveID(params.get("profile_id")),
    postID: positiveID(params.get("post")),
    accountIDs: (params.get("account_ids") || "").split(",").map(value => positiveID(value.trim())).filter((id): id is number => id != null),
    compose: params.get("compose") === "1",
    calendar: params.get("view") === "calendar",
    status: ["scheduled", "published", "failed", "partial", "draft", "in_review", "approved", "rejected", "attention"].includes(status) ? status : "all",
    anchorDate: anchor && /^\d{4}-\d{2}-\d{2}$/.test(anchor) && Number.isFinite(Date.parse(anchor)) ? new Date(`${anchor}T12:00:00`) : undefined,
    range: ["7d", "28d", "90d"].includes(params.get("range") || "") ? params.get("range")! : "28d",
  };
}
export type SocialNavigation = ReturnType<typeof socialNavigation>;
