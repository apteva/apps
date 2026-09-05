export function scheduleInstant(value: string): string {
  const date = new Date(value);
  if (!value || !Number.isFinite(date.getTime())) throw new Error("Choose a valid schedule time.");
  // Reject browser normalization of nonexistent local times in a DST gap.
  if (/^\d{4}-\d\d-\d\dT\d\d:\d\d$/.test(value) && localScheduleInput(date.toISOString()) !== value) {
    throw new Error("That local time does not exist because the clocks change. Choose another time.");
  }
  return date.toISOString();
}
export function localScheduleInput(instant: string): string {
  if (!instant) return "";
  const date = new Date(instant); if (!Number.isFinite(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth()+1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
