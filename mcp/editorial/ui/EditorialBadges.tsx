import { Facebook, Globe, Instagram, Linkedin, Mail, Podcast, Send, Twitter, Youtube } from "lucide-react";

const destinationKey = (value: string) => value.trim().toLowerCase();

const icons = new Map([
  ["facebook", Facebook], ["facebook page", Facebook], ["facebook pages", Facebook],
  ["instagram", Instagram], ["instagram business", Instagram], ["instagram reel", Instagram],
  ["linkedin", Linkedin], ["linkedin page", Linkedin], ["linkedin profile", Linkedin],
  ["youtube", Youtube], ["youtube shorts", Youtube],
  ["twitter", Twitter],
  ["website", Globe], ["blog", Globe],
  ["newsletter", Mail], ["email", Mail], ["podcast", Podcast],
]);

export function itemDestinations(releases: { item_id: number; channel: string; archived: boolean }[]) {
  const byItem = new Map<number, string[]>();
  for (const release of releases) {
    const channel = release.channel.trim();
    if (release.archived || !channel) continue;
    const destinations = byItem.get(release.item_id) || [];
    destinations.push(channel);
    byItem.set(release.item_id, destinations);
  }
  return byItem;
}

export function destinationNames(channels: readonly string[] = []) {
  const names = new Map<string, string>();
  for (const value of channels) {
    const name = value.trim();
    const key = destinationKey(name);
    if (key && !names.has(key)) names.set(key, name);
  }
  return [...names.values()].sort((a, b) => a.localeCompare(b));
}

export function DestinationBadges({ channels }: { channels?: readonly string[] }) {
  const names = destinationNames(channels);
  if (!names.length) return null;
  return (
    <span className={`ed-destinations ${names.length > 1 ? "multiple" : ""}`}>
      {names.map((name) => {
        const key = destinationKey(name);
        const Icon = icons.get(key);
        const isX = key === "x" || key === "x (twitter)";
        const Glyph = Icon || Send;
        return (
          <span className="ed-destination" data-known-icon={Icon || isX ? "" : undefined} key={key} title={name}>
            {isX ? (
              <svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6">
                <path d="M4 3h4l12 18h-4L4 3Zm16 0L4 21" />
              </svg>
            ) : <Glyph aria-hidden="true" strokeWidth={1.8} />}
            <span className="ed-destination-name">{name}</span>
          </span>
        );
      })}
    </span>
  );
}

const tones = new Map([
  ["approved", "success"], ["published", "success"], ["completed", "success"],
  ["scheduled", "info"], ["ready", "info"],
  ["in_progress", "accent"], ["drafting", "accent"],
  ["pending", "warn"], ["review", "warn"], ["in_review", "warn"], ["blocked", "warn"],
  ["failed", "error"], ["changes_requested", "error"],
]);

export function statusLabel(value: string) {
  return value.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase());
}

export function StatusBadge({ value }: { value: string }) {
  const tone = tones.get(value.trim().toLowerCase().replace(/[\s-]+/g, "_")) || "neutral";
  return (
    <span className="ed-status" data-tone={tone}>
      <span className="ed-status-dot" aria-hidden="true" />
      <span className="ed-status-name">{statusLabel(value)}</span>
    </span>
  );
}

export const badgeStyles = `
.ed-destinations{display:inline-flex;flex-wrap:wrap;align-items:center;gap:4px;min-width:0}
.ed-destination,.ed-status{display:inline-flex;align-items:center;gap:4px;border-radius:4px;padding:2px 5px;line-height:1.3;font-size:10px;font-weight:600;vertical-align:middle}
.ed-destination{border:1px solid var(--color-border,var(--border,#444));min-width:0;max-width:100%}
.ed-destination svg{width:12px;height:12px;flex:none}
.ed-destination-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ed-status{--ed-tone:var(--color-text-muted,var(--text-muted,#999));background:color-mix(in srgb,var(--ed-tone) 16%,transparent);white-space:nowrap}
.ed-status[data-tone=success]{--ed-tone:var(--color-success,var(--success,#22c55e))}
.ed-status[data-tone=info]{--ed-tone:var(--color-info,var(--info,#60a5fa))}
.ed-status[data-tone=accent]{--ed-tone:var(--color-accent,var(--accent,#ff8c36))}
.ed-status[data-tone=warn]{--ed-tone:var(--color-warn,var(--warn,#eab308))}
.ed-status[data-tone=error]{--ed-tone:var(--color-error,var(--error,#f87171))}
.ed-status-dot{height:6px;width:6px;border-radius:50%;background:var(--ed-tone);flex:none}
`;
