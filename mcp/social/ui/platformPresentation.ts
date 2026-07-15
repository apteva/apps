export interface PlatformPresentation {
  key: string;
  label: string;
  mark: string;
  color: string;
}

const PLATFORM_PRESENTATIONS: Record<string, PlatformPresentation> = {
  facebook: { key: "facebook", label: "Facebook", mark: "f", color: "#4B93FF" },
  instagram: { key: "instagram", label: "Instagram", mark: "IG", color: "#F05B78" },
  linkedin: { key: "linkedin", label: "LinkedIn", mark: "in", color: "#4FA3E3" },
  youtube: { key: "youtube", label: "YouTube", mark: "YT", color: "#FF4D5E" },
  tiktok: { key: "tiktok", label: "TikTok", mark: "TT", color: "#25F4EE" },
  x: { key: "x", label: "X", mark: "X", color: "#E7E9EA" },
  threads: { key: "threads", label: "Threads", mark: "@", color: "#D7D9DA" },
  pinterest: { key: "pinterest", label: "Pinterest", mark: "P", color: "#FF4B64" },
  reddit: { key: "reddit", label: "Reddit", mark: "r", color: "#FF6A33" },
  whatsapp: { key: "whatsapp", label: "WhatsApp", mark: "WA", color: "#4ADE80" },
  snapchat: { key: "snapchat", label: "Snapchat", mark: "SC", color: "#FDE047" },
  googlebusiness: { key: "googlebusiness", label: "Google Business", mark: "G", color: "#60A5FA" },
};

export function platformPresentation(platform: string): PlatformPresentation {
  let key = platform.trim().toLowerCase().replace(/[\s_-]+/g, "");
  if (key === "twitter") key = "x";
  if (key === "googlebusinessprofile" || key === "googlemybusiness") key = "googlebusiness";
  const known = PLATFORM_PRESENTATIONS[key];
  if (known) return known;
  const label = platform.trim() || "Social";
  return {
    key: key || "social",
    label,
    mark: label.slice(0, 2).toUpperCase(),
    color: "#A1A1AA",
  };
}
