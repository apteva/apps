import type { Schedule } from "./Assignments";

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const pad = (n: string) => n.padStart(2, "0");
const dayName = (d: string) => DAYS[Number(d) % 7];
const dayList = (field: string) => {
  if (field === "1-5") return "weekday";
  if (field === "0,6" || field === "6,0") return "weekend day";
  const range = field.match(/^(\d)-(\d)$/);
  if (range) return `${dayName(range[1])} to ${dayName(range[2])}`;
  if (/^\d(,\d)*$/.test(field)) return field.split(",").map(dayName).join(", ");
  return "";
};
const everyUnit = (every: string) => {
  const m = every.trim().match(/^(\d+)\s*(s|m|h|d|w)$/i);
  if (!m) return `Every ${every}`;
  const n = Number(m[1]);
  const unit = { s: "second", m: "minute", h: "hour", d: "day", w: "week" }[m[2].toLowerCase() as "s"]!;
  return n === 1 ? `Every ${unit}` : `Every ${n} ${unit}s`;
};

/** A plain-language schedule: "Every Monday at 09:00", "Every hour 09:15–23:15".
 * Unusual cron expressions fall back to the expression itself. */
export const scheduleTitle = (schedule: Schedule) => {
  if (schedule.kind === "interval" && schedule.every) return everyUnit(schedule.every);
  const parts = (schedule.cron || "").trim().split(/\s+/);
  if (parts.length !== 5) return schedule.cron || "Schedule";
  const [min, hour, dom, mon, dow] = parts;
  if (!/^\d+$/.test(min) || dom !== "*" || mon !== "*") {
    if (/^\d+$/.test(min) && /^\d+$/.test(hour) && /^\d+$/.test(dom) && mon === "*" && dow === "*")
      return `Monthly on day ${dom} at ${pad(hour)}:${pad(min)}`;
    return schedule.cron!;
  }
  const days = dow === "*" ? "day" : dayList(dow);
  if (!days) return schedule.cron!;
  if (/^\d+$/.test(hour)) return `Every ${days} at ${pad(hour)}:${pad(min)}`;
  const hours = hour.match(/^(\d+)-(\d+)$/);
  if (hours) return `Hourly at :${pad(min)}, ${pad(hours[1])}:${pad(min)}–${pad(hours[2])}:${pad(min)}${days === "day" ? "" : `, every ${days}`}`;
  if (hour === "*") return `Hourly at :${pad(min)}${days === "day" ? "" : `, every ${days}`}`;
  return schedule.cron!;
};

/** "conversation.message.received" → "Conversation message received". */
export const eventTitle = (topic: string) => {
  const words = topic.replace(/\.\*$/, "").split(/[._-]+/).filter(Boolean).join(" ");
  const title = words.charAt(0).toUpperCase() + words.slice(1);
  return topic.endsWith(".*") ? `Any ${title.toLowerCase()} event` : title;
};
