import { useState } from "react";

const pad = (n: number) => String(n).padStart(2, "0");
export function localDateKey(date: Date) {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

// Inline calendar avoids OS-native date/time popovers, using host theme tokens.
export function SchedulePicker({ value, onChange, timezone }: {
  value: string; onChange: (value: string) => void; timezone: string;
}) {
  const [month, setMonth] = useState(() => {
    const date = value ? new Date(value) : new Date();
    return new Date(date.getFullYear(), date.getMonth(), 1);
  });
  const selected = value.split("T")[0];
  const [hour, minute] = (value.split("T")[1] || "09:00").split(":");
  const today = localDateKey(new Date());
  const offset = (month.getDay() + 6) % 7;
  const days = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate();
  const choose = (date: string) => onChange(`${date}T${hour}:${minute}`);
  const changeTime = (h: string, m: string) => onChange(`${selected || today}T${h}:${m}`);
  const inputClass = "w-16 rounded border border-border bg-bg-input px-2 py-2 text-center text-sm text-text focus:border-accent focus:outline-none";
  return (
    <div className="rounded border border-border bg-bg-input p-3" role="group" aria-label="Run date and time">
      <div className="flex items-center gap-2">
        <button type="button" aria-label="Previous month" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))} className="rounded px-3 py-1 text-text-muted hover:bg-bg-hover">‹</button>
        <span aria-live="polite" className="flex-1 text-center text-xs font-semibold text-text">{month.toLocaleDateString(undefined, { month: "long", year: "numeric" })}</span>
        <button type="button" aria-label="Next month" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))} className="rounded px-3 py-1 text-text-muted hover:bg-bg-hover">›</button>
      </div>
      <div className="mt-2 grid gap-1" style={{ gridTemplateColumns: "repeat(7, minmax(0, 1fr))" }}>
        {["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"].map(day => <span key={day} className="py-1 text-center text-[10px] text-text-dim">{day}</span>)}
        {Array.from({ length: offset }, (_, index) => <span key={`blank-${index}`} />)}
        {Array.from({ length: days }, (_, index) => {
          const date = new Date(month.getFullYear(), month.getMonth(), index + 1);
          const key = localDateKey(date);
          return <button key={key} type="button" aria-label={date.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" })} aria-pressed={key === selected} aria-current={key === today ? "date" : undefined} onClick={() => choose(key)}
            className={`rounded border py-2 text-xs focus-visible:outline focus-visible:outline-accent ${key === selected ? "border-accent bg-accent/15 font-bold text-accent" : key === today ? "border-accent/40 text-accent hover:bg-bg-hover" : "border-transparent text-text-muted hover:bg-bg-hover"}`}>{index + 1}</button>;
        })}
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border pt-3">
        <span className="mr-1 text-xs text-text-muted">Time</span>
        <input aria-label="Run hour" type="number" min="0" max="23" value={hour} onChange={e => changeTime(e.target.value, minute)} onBlur={() => changeTime(pad(Math.max(0, Math.min(23, Number(hour) || 0))), minute)} className={inputClass} />
        <span className="text-text-dim">:</span>
        <input aria-label="Run minute" type="number" min="0" max="59" value={minute} onChange={e => changeTime(hour, e.target.value)} onBlur={() => changeTime(hour, pad(Math.max(0, Math.min(59, Number(minute) || 0))))} className={inputClass} />
        <button type="button" onClick={() => { const date = new Date(); setMonth(new Date(date.getFullYear(), date.getMonth(), 1)); choose(today); }} className="ml-auto rounded px-2 py-1 text-xs text-accent hover:bg-accent/10">Today</button>
      </div>
      <p className="mt-2 text-[10px] text-text-dim">24-hour time · {timezone.replaceAll("_", " ")}</p>
    </div>
  );
}
