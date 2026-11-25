/** Splits an agent-written alert into what a card shows: a title, a markdown
 * body and the reference it came from. Agents write alerts as one markdown
 * string ("**Title.** Body…\n\nSource: `ref`"); none of it is required. */
export interface AlertParts {
  title: string;
  body: string;
  source: string;
}

const LEADING_BOLD = /^(?:\*\*|__)(.+?)(?:\*\*|__)[ \t]*[.:—–-]?[ \t]*/s;
const LEADING_HEADING = /^#{1,4}[ \t]+(.+?)[ \t]*#*[ \t]*(?:\n|$)/;
const SOURCE_LINE = /^[ \t]*(?:\*\*|__)?sources?[ \t]*(?:\*\*|__)?[ \t]*:[ \t]*(?:\*\*|__)?[ \t]*(.+?)[ \t]*$/i;

export function alertParts(text: string, title = ""): AlertParts {
  let body = text.replace(/\r\n?/g, "\n").trim();
  let source = "";
  const lines = body.split("\n");
  const last = lines[lines.length - 1] ?? "";
  const sourceMatch = lines.length > 1 || title ? SOURCE_LINE.exec(last) : null;
  if (sourceMatch) {
    source = sourceMatch[1].replace(/^`+|`+$/g, "").trim();
    body = lines.slice(0, -1).join("\n").trim();
  }
  let heading = title.trim();
  if (!heading) {
    const bold = LEADING_BOLD.exec(body);
    const rest = bold ? body.slice(bold[0].length).trim() : "";
    // A bold lead-in is the title only when something follows it.
    if (bold && rest && !bold[1].includes("\n")) {
      heading = bold[1].trim();
      body = rest;
    } else {
      const atx = LEADING_HEADING.exec(body);
      if (atx && body.slice(atx[0].length).trim()) {
        heading = atx[1].trim();
        body = body.slice(atx[0].length).trim();
      }
    }
  }
  return { title: heading.replace(/[.:]\s*$/, ""), body, source };
}

/** One-line plain text for list rows and notifications. */
export function plainLine(source: string, max = 160): string {
  return source
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/(\*\*|__|`|~~)/g, "")
    .replace(/^\s{0,3}(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)/gm, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, max);
}
