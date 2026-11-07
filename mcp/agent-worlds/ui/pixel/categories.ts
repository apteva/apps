// Places get a visual category from their name, so a newly installed app gets
// a fitting building without any theme knowing that app.
export type Category =
  | "storage" | "people" | "mail" | "tasks" | "build" | "money" | "media"
  | "data" | "talk" | "web" | "time" | "link" | "other";

const RULES: [Category, RegExp][] = [
  ["mail", /mail|messag|inbox|newsletter|smtp|postmark|sendgrid/],
  ["talk", /slack|discord|telegram|whatsapp|chat|convers|telephon|twilio|phone|voice|sms|call/],
  ["people", /crm|contact|lead|people|customer|member|communit|hr|recruit/],
  ["tasks", /task|todo|project|process|kanban|ticket|issue|workflow|job/],
  ["build", /code|deploy|function|github|gitlab|build|server|cert|domain|dns|docker|actor|computer|browser/],
  ["money", /bill|invoice|finance|payment|stripe|checkout|commerce|shop|order|trading|stock|bank|account|factur/],
  ["media", /social|content|media|image|video|youtube|tiktok|instagram|twitter|facebook|linkedin|pinterest|stream|webinar|photo|design/],
  ["data", /table|database|analytic|metric|report|sheet|data|search|catalog|status|monitor/],
  ["web", /site|seo|web|page|blog|cms|ads|marketing/],
  ["time", /calendar|schedul|booking|meeting|event|trip|travel/],
  ["storage", /storage|file|drive|dropbox|s3|bucket|asset|document|sign/],
];

export function categoryFor(name: string, kind: string): Category {
  const value = name.toLowerCase();
  for (const [category, pattern] of RULES) if (pattern.test(value)) return category;
  return kind === "integration" ? "link" : "other";
}

// 7x7 sign icons, one per category. "x" is the ink colour.
export const ICONS: Record<Category, string[]> = {
  storage: ["xxxxxxx", "x.....x", "xxxxxxx", "x..x..x", "x.....x", "x.....x", "xxxxxxx"],
  people: ["..xxx..", "..xxx..", "..xxx..", ".......", ".xxxxx.", "xxxxxxx", "xxxxxxx"],
  mail: ["xxxxxxx", "xx...xx", "x.x.x.x", "x..x..x", "x.....x", "x.....x", "xxxxxxx"],
  tasks: ["......x", ".....xx", "x...xx.", "xx.xx..", ".xxx...", "..x....", "......."],
  build: ["...x...", ".x.x.x.", "..xxx..", "xxx.xxx", "..xxx..", ".x.x.x.", "...x..."],
  money: ["..xxx..", ".x.x.x.", ".x.x...", "..xxx..", "...x.x.", ".x.x.x.", "..xxx.."],
  media: ["...x...", "...x...", "xxxxxxx", ".xxxxx.", "..xxx..", ".xx.xx.", ".x...x."],
  data: [".....xx", ".....xx", "...xxxx", "...xxxx", ".xxxxxx", ".xxxxxx", "xxxxxxx"],
  talk: [".xxxxx.", "xxxxxxx", "xx.x.xx", "xxxxxxx", ".xxxxx.", ".xx....", "x......"],
  web: ["..xxx..", ".x.x.x.", "xxxxxxx", "x..x..x", "xxxxxxx", ".x.x.x.", "..xxx.."],
  time: ["..xxx..", ".x...x.", "x..x..x", "x..xx.x", "x.....x", ".x...x.", "..xxx.."],
  link: ["..x.x..", "..x.x..", ".xxxxx.", ".xxxxx.", "..xxx..", "...x...", "...x..."],
  other: ["..xxx..", ".xxxxx.", "xx...xx", "xx...xx", "xx...xx", ".xxxxx.", "..xxx.."],
};
