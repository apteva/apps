import { expect, test } from "bun:test";
import { createConversationLocalization } from "../src/i18n";
import { catalog } from "../src/locales";

test("regional locales select app dictionaries, preserve regional formatting, and safely fall back", () => {
  const fr = createConversationLocalization({ locale: "fr-CA" });
  expect(fr.locale).toBe("fr-CA");
  expect(fr.t("chat.history")).toBe("Historique");
  expect(createConversationLocalization({ locale: "es-MX" }).t("chat.new")).toBe("Nueva conversación");
  expect(createConversationLocalization({ locale: "de-DE" }).t("chat.empty")).toBe("No messages yet — say something.");
  expect(createConversationLocalization({ locale: "invalid_!", timeZone: "invalid" }).locale).toBe("en");
  expect(createConversationLocalization({ timeZone: "invalid" }).timeZone).toBeUndefined();
});

test("host copy overrides selected keys, supports empty strings, and follows locale plural rules", () => {
  const fr = createConversationLocalization({ locale: "fr-FR", messages: {
    "chat.empty": "Bonjour {name}", "chat.placeholder": "",
    "inbox.count": { one: "{count} demande", other: "{count} demandes" },
  } });
  expect(fr.t("chat.empty", { name: "<b>Camille</b>" })).toBe("Bonjour <b>Camille</b>");
  expect(fr.t("chat.placeholder")).toBe("");
  expect(fr.t("chat.history")).toBe("Historique");
  expect(fr.t("inbox.count", { count: 0 })).toBe("0 demande");
  expect(fr.t("inbox.count", { count: 2 })).toBe("2 demandes");
  expect(createConversationLocalization().t("inbox.count", { count: 0 })).toBe("0 items");
  expect(createConversationLocalization({ locale: "es" }).t("inbox.count", { count: 1 })).toBe("1 elemento");
});

test("absolute times use the supplied timezone and relative times use the supplied locale", () => {
  const fr = createConversationLocalization({ locale: "fr-FR", timeZone: "Europe/Paris" });
  const format = { hour: "2-digit", minute: "2-digit", hourCycle: "h23" } as const;
  expect(fr.dateTime("2026-01-01 12:00:00", format)).toBe("13:00");
  expect(fr.dateTime("2026-07-01T12:00:00Z", format)).toBe("14:00");
  expect(fr.relativeTime("2026-01-01T12:00:00Z", Date.parse("2026-01-01T12:02:00Z"))).toBe("il y a 2 minutes");
  expect(fr.relativeTime("2026-01-01T12:03:00Z", Date.parse("2026-01-01T12:02:00Z"))).toBe("dans 1 minute");
  expect(fr.relativeTime("invalid")).toBe("");
  expect(fr.dateTime("invalid")).toBe("");
});

test("all bundled translations preserve interpolation parameters", () => {
  const parameters = (message: string | object) => [...new Set((typeof message === "string" ? message : Object.values(message).join(" ")).match(/\{\w+\}/g) ?? [])].sort();
  for (const [key, translations] of Object.entries(catalog)) {
    expect(translations.length, key).toBe(3);
    for (const translation of translations.slice(1)) expect(parameters(translation), key).toEqual(parameters(translations[0]));
  }
});
