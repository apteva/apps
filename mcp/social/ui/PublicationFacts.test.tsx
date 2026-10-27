import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { PublicationFacts } from "./PublicationFacts";
import { postLifecycleDate, postLifecycleDateLabel } from "./postCalendar";

test("native publication is unknown while submission and recording times remain visible", () => {
  const html = renderToStaticMarkup(
    <PublicationFacts
      facts={{
        submitted_at: "2026-10-01T08:00:00Z",
        delivery_recorded_at: "2026-10-01T08:01:00Z",
        native_published_at: null,
        observed_at: "2026-10-01T08:02:00Z",
      }}
    />,
  );
  expect(html).toContain("Native publication</dt><dd>Unknown");
  expect(html).toContain("Submitted</dt><dd>2026-10-01T08:00:00Z");
  expect(html).toContain("Delivery recorded</dt><dd>2026-10-01T08:01:00Z");
  expect(html).toContain("Provider accepted</dt><dd>Unknown");
});

test("private upload and legacy dates are never displayed as publication", () => {
  const html = renderToStaticMarkup(
    <PublicationFacts
      facts={{
        native_uploaded_at: "2018-01-01T10:00:00Z",
        native_published_at: null,
        legacy_published_at: "2026-10-01T08:00:00Z",
      }}
    />,
  );
  expect(html).toContain("Native publication</dt><dd>Unknown");
  expect(html).toContain("Native upload</dt><dd>2018-01-01T10:00:00Z");
  expect(html).toContain("Previous timestamp retained for review");
  expect(html).toContain(
    "audience confirmation do not establish a native publication time",
  );
});

test("published calendar entries with unknown native dates do not use their schedule as publication", () => {
  expect(
    postLifecycleDate({
      status: "published",
      schedule_at: "2026-10-01T08:00:00Z",
      created_at: "2026-09-30T10:00:00Z",
      delivery_recorded_at: "2026-10-01T11:00:00Z",
    })?.toISOString(),
  ).toBe("2026-10-01T11:00:00.000Z");
});

test("calendar date labels keep recording separate from native publication", () => {
 expect(postLifecycleDateLabel({status:"published",created_at:"2026-10-01T08:00:00Z",delivery_recorded_at:"2026-10-01T09:00:00Z"})).toContain("native publication time unknown");
 expect(postLifecycleDateLabel({status:"published",created_at:"2026-10-01T08:00:00Z",published_at:"2026-10-01T09:00:00Z"})).toBe("Native publication");
});
