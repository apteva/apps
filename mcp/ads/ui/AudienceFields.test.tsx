import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import AudienceFields, { freshAudienceDraft, audienceDefinition, audienceDefinitionValid, audienceDraftFromMetadata } from "./AudienceFields";
const browser = new Window();
Object.assign(globalThis, { window: browser, document: browser.document, HTMLElement: browser.HTMLElement, Event: browser.Event, IS_REACT_ACT_ENVIRONMENT: true });
const { createRoot } = await import("react-dom/client");
const roots: ReturnType<typeof createRoot>[] = [];
afterEach(async () => { for (const r of roots.splice(0)) await act(async () => r.unmount()); document.body.innerHTML = ""; });
test("custom segment payload preserves member types and search behavior", () => {
 const d = { ...freshAudienceDraft(), keywords: " running shoes \n\ntrail runners", urls: "https://example.com", apps: "com.example.app", mode: "search_terms" };
 expect(audienceDefinition("custom_segment", d)).toEqual({ keywords: ["running shoes", "trail runners"], urls: ["https://example.com"], apps: ["com.example.app"], segment_mode: "search_terms" });
 expect(audienceDefinitionValid("custom_segment", freshAudienceDraft())).toBe(false);
});
test("Google lookalikes use local seeds, country codes and expansion without Meta ratio", () => {
 const d = { ...freshAudienceDraft(), seeds: [42, 7], countries: "es, us", expansion: "broad" };
 expect(audienceDefinition("lookalike", d)).toEqual({ source_audience_ids: [42, 7], country_codes: ["ES", "US"], expansion_level: "broad" });
 expect(audienceDefinitionValid("lookalike", { ...d, countries: "USA" })).toBe(false);
 expect(audienceDefinitionValid("lookalike", { ...d, seeds: [] })).toBe(false);
});
test("grouped audiences resolve local segments separately from interest IDs", () => {
 const d = { ...freshAudienceDraft(), segments: [12], interests: ["80001"] };
 expect(audienceDefinition("grouped", d)).toEqual({ segment_audience_ids: [12], user_interest_ids: ["80001"] });
 expect(audienceDefinitionValid("grouped", freshAudienceDraft())).toBe(false);
});
test("lookalike UI excludes grouped/custom/inactive seeds and explains provider eligibility", async () => {
 const node = document.createElement("div");document.body.append(node);const root = createRoot(node);roots.push(root);
 const audiences = [ { id: 1, name: "Viewers", type: "remarketing", status: "active" }, { id: 2, name: "Topics", type: "custom_segment", status: "active" }, { id: 3, name: "Group", type: "grouped", status: "active" }, { id: 4, name: "Closed", type: "website", status: "closed" } ];
 await act(async () => root.render(<AudienceFields kind="lookalike" draft={freshAudienceDraft()} onChange={() => {}} audiences={audiences} adAccountId={1} callTool={async () => ({})} />));
 expect(node.querySelectorAll('input[type="checkbox"]').length).toBe(1);
 expect(node.textContent).toContain("100 active matched");expect(node.textContent).toContain("Viewer");expect(node.textContent).toContain("Audience Manager");expect(node.textContent).not.toContain("Topics");
});

test("Editing imported custom segments retains search interpretation and member collections", () => {
 const draft=audienceDraftFromMetadata({native_type:"SEARCH",members:[{keyword:"running"},{url:"https://example.com"},{app:"com.example.app"}]},[]);
 expect(audienceDefinition("custom_segment",draft)).toEqual({keywords:["running"],urls:["https://example.com"],apps:["com.example.app"],segment_mode:"search_terms"});
});
