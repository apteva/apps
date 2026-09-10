import { createContext, useContext, useMemo, type ReactNode } from "react";
import { catalog } from "./locales";

export type ConversationMessageKey = keyof typeof catalog;
export type ConversationMessage = string | ({ other: string } & Partial<Record<Intl.LDMLPluralRule, string>>);
export type ConversationMessages = Partial<Record<ConversationMessageKey, ConversationMessage>>;
export interface ConversationLocalization {
  /** BCP 47 locale. Defaults to English; regional locales use their base-language dictionary. */
  locale?: string;
  /** IANA timezone for absolute timestamps. Defaults to the browser's timezone. */
  timeZone?: string;
  /** Plain-text overrides for this host's current language; never HTML or message content. */
  messages?: ConversationMessages;
}
export type ConversationMessageParams = Record<string, string | number>;

function validLocale(value?: string): string {
  try { return Intl.getCanonicalLocales(value || "en")[0] || "en"; } catch { return "en"; }
}
function validTimeZone(value?: string): string | undefined {
  if (!value) return undefined;
  try { return new Intl.DateTimeFormat("en", { timeZone: value }).resolvedOptions().timeZone; } catch { return undefined; }
}
function dateValue(value: string | number | Date): Date {
  // App timestamps without an explicit offset are UTC, including SQLite's space separator.
  if (typeof value === "string" && /^\d{4}-\d\d-\d\d[ T]\d\d:\d\d/.test(value) && !/(?:Z|[+-]\d\d:?\d\d)$/i.test(value)) {
    return new Date(value.replace(" ", "T") + "Z");
  }
  return new Date(value);
}

export function createConversationLocalization(options: ConversationLocalization = {}) {
  const locale = validLocale(options.locale);
  const language = new Intl.Locale(locale).language;
  const index = language === "fr" ? 1 : language === "es" ? 2 : 0;
  const timeZone = validTimeZone(options.timeZone);
  const numbers = new Intl.NumberFormat(locale);
  const plurals = new Intl.PluralRules(locale);
  const relative = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  const t = (key: ConversationMessageKey, params: ConversationMessageParams = {}): string => {
    const override = options.messages && Object.prototype.hasOwnProperty.call(options.messages, key) ? options.messages?.[key] : undefined;
    const message: ConversationMessage = override ?? catalog[key][index] ?? catalog[key][0];
    const template = typeof message === "string" ? message : message[plurals.select(Number(params.count ?? 0))] ?? message.other;
    return template.replace(/\{([a-zA-Z][\w]*)\}/g, (match, name: string) => {
      const value = params[name];
      return value === undefined ? match : typeof value === "number" ? numbers.format(value) : value;
    });
  };
  const dateTime = (value: string | number | Date, format: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "short" }) => {
    const date = dateValue(value);
    return Number.isFinite(date.getTime()) ? new Intl.DateTimeFormat(locale, { ...format, timeZone }).format(date) : "";
  };
  const relativeTime = (value: string | number | Date | null, now = Date.now()) => {
    if (value === null) return "";
    const delta = (dateValue(value).getTime() - now) / 1000;
    if (!Number.isFinite(delta)) return "";
    const magnitude = Math.abs(delta);
    const [divisor, unit]: [number, Intl.RelativeTimeFormatUnit] = magnitude < 60 ? [1, "second"] : magnitude < 3600 ? [60, "minute"] : magnitude < 86400 ? [3600, "hour"] : [86400, "day"];
    return relative.format(Math.trunc(delta / divisor), unit);
  };
  // Translate known app-owned status values only. Unknown/custom values remain intact.
  const statusKeys: Record<string, ConversationMessageKey> = {
    public: "chat.public", operator: "chat.operator", private: "chat.private", room: "chat.room", direct: "chat.direct", manual: "telegram.manual", pairing: "telegram.pairing",
    approve: "status.approved", approved: "status.approved", deny: "status.denied", denied: "status.denied",
    pending: "status.pending", info: "status.info", warn: "status.warn", warning: "status.warn",
    error: "status.error", critical: "status.critical", approval: "card.approval", report: "card.report", alert: "card.alert",
  };
  const statusLabel = (value: string) => Object.prototype.hasOwnProperty.call(statusKeys, value) ? t(statusKeys[value]) : value;
  return { locale, timeZone, t, dateTime, relativeTime, number: (value: number) => numbers.format(value), statusLabel,
    direction: /^(ar|fa|he|ur|ps|dv)$/.test(language) ? "rtl" as const : "ltr" as const };
}

const defaultLocalization = createConversationLocalization();
const Context = createContext({ options: {} as ConversationLocalization, value: defaultLocalization });
export function ConversationLocalizationProvider({ children, locale, timeZone, messages }: ConversationLocalization & { children: ReactNode }) {
  const parent = useContext(Context);
  const context = useMemo(() => {
    const options = {
      locale: locale ?? parent.options.locale,
      timeZone: timeZone ?? parent.options.timeZone,
      messages: { ...parent.options.messages, ...messages },
    };
    return { options, value: createConversationLocalization(options) };
  }, [locale, timeZone, messages, parent]);
  return <Context.Provider value={context}>{children}</Context.Provider>;
}
export function useConversationLocalization() { return useContext(Context).value; }
export function ConversationLocaleRegion({ children, className = "", fill = true }: { children: ReactNode; className?: string; fill?: boolean }) {
  const { locale, direction } = useConversationLocalization();
  return <div lang={locale} dir={direction} className={`apteva-conversations ${className}`} style={fill ? { height: "100%", minHeight: 0 } : undefined}>{children}</div>;
}
