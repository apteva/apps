import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ActivityRow } from "./CrmPanel";

test("actual conversation row renders sent template text and escapes variables", () => {
  const html = renderToStaticMarkup(<ActivityRow onReply={() => {}} activity={{
    id:"1",kind:"whatsapp_sent",body:"(template #46)",occurred_at:"2026-10-08T12:43:57Z",
    template_snapshot:{template_id:46,name:"reengage",availability:"complete",content_source:"provider_response",body_text:"Hi Roxy <script>literal</script>"},
  }} />);
  expect(html).toContain("reengage (#46)");
  expect(html).toContain("Content confirmed by provider");
  expect(html).toContain("Hi Roxy &lt;script&gt;literal&lt;/script&gt;");
  expect(html).not.toContain("(template #46)");
  expect(html).not.toContain("<script>");
});

test("historical conversation row explicitly reports unavailable content", () => {
  const html = renderToStaticMarkup(<ActivityRow onReply={() => {}} activity={{id:"1",kind:"whatsapp_sent",body:"(template #46)",occurred_at:"2026-10-08T12:43:57Z"}} />);
  expect(html).toContain("Sent content was not saved for this historical message.");
});
