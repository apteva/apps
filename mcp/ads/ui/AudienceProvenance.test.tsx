import { afterEach, expect, test } from "bun:test";
import { Window } from "happy-dom";
import { act } from "react";
import AudienceProvenance, { provenanceInputValid } from "./AudienceProvenance";
import { audienceTypeLabel } from "./audiencePresentation";
const browser = new Window();
Object.assign(globalThis, { window: browser, document: browser.document, HTMLElement: browser.HTMLElement, Event: browser.Event, IS_REACT_ACT_ENVIRONMENT: true });
const { createRoot } = await import("react-dom/client");
const roots: ReturnType<typeof createRoot>[] = [];
afterEach(async () => { for (const r of roots.splice(0)) await act(async () => r.unmount()); document.body.innerHTML = ""; });
const verified = { status: "verified", source_platform: "youtube", source_channel_id: "UC_fixture", interaction_type: "video_view", verification_evidence: "Checked exact list and channel IDs in source configuration.", verification_method: "manual_attestation", verified_at: "2026-10-27T12:00:00Z" };
async function render() {
 const calls: any[] = []; let saved = 0;
 const node = document.createElement("div");document.body.append(node);const root = createRoot(node);roots.push(root);
 await act(async () => root.render(<AudienceProvenance audienceId={42} adAccountId={6} provenance={verified} callTool={async (name, args) => { calls.push({ name, args }); return {}; }} onSaved={() => saved++} onClose={() => {}} />));
 return { node, calls, saved: () => saved };
}
test("Provenance requires a source identity, event and evidence", () => {
 expect(provenanceInputValid(verified)).toBe(true);
 expect(provenanceInputValid({ ...verified, source_channel_id: "" })).toBe(false);
 expect(provenanceInputValid({ ...verified, verification_evidence: "  " })).toBe(false);
 expect(provenanceInputValid({ source_platform: "crm", source_account_id: "account-1", interaction_type: "segment_membership", verification_evidence: "Checked CRM segment export configuration" })).toBe(true);
});
test("Verification editor requires explicit confirmation even when prefilled", async () => {
 const f = await render(); const save = [...f.node.querySelectorAll("button")].find(b => b.textContent === "Save verification")!;
 expect(save.disabled).toBe(true); expect(f.calls).toHaveLength(0);
 expect(f.node.textContent).toContain("Saved manual verification");
 await act(async () => (f.node.querySelector('input[type="checkbox"]') as HTMLInputElement).click());
 expect(save.disabled).toBe(false);
 await act(async () => save.click());
 expect(f.calls).toHaveLength(1);expect(f.calls[0].name).toBe("audience_provenance_set");
 expect(f.calls[0].args).toMatchObject({ audience_id: 42, ad_account_id: 6, confirmed: true, provenance: { source_platform: "youtube", source_channel_id: "UC_fixture", verification_evidence: verified.verification_evidence } });
 expect(f.calls[0].args.provenance.verified_at).toBeUndefined();expect(f.calls[0].args.provenance.status).toBeUndefined();expect(f.saved()).toBe(1);
});
test("Clear verification is explicit and does not resubmit the previous mapping", async () => {
 const f = await render(); const clear = [...f.node.querySelectorAll("button")].find(b => b.textContent === "Clear verification")!;
 expect(clear.disabled).toBe(true);
 await act(async () => (f.node.querySelector('input[type="checkbox"]') as HTMLInputElement).click());
 await act(async () => clear.click());
 expect(f.calls[0].args).toEqual({ audience_id: 42, ad_account_id: 6, confirmed: true, clear: true });
});
test("Neutral audience mechanism labels do not invent a source", () => {
 expect(audienceTypeLabel("rule_based")).toBe("Rule-based audience");
 expect(audienceTypeLabel("rule_based")).not.toContain("Website");
 expect(audienceTypeLabel("remarketing")).toBe("Remarketing");
 expect(audienceTypeLabel("website")).toBe("Website visitors");
});
