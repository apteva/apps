import { expect, test } from "bun:test";
import { composerDraftContent, replyDraftEditable, replyDraftFingerprint, type SavedReplyDraft } from "./drafts";

const composer = {
  channel:"email",to:"private@example.test",from:"support@example.test",subject:"Re: Question",
  body:"Preserve all my text",bodyHTML:"<b>HTML too</b>",templateId:"",templateVars:{},
  attachments:[{key:"ui-only",storage_id:7,filename:"document.pdf",content_type:"application/pdf",size_bytes:100},
    {key:"upload",content_base64:"bm90ZXM=",filename:"notes.txt"},{key:"url",url:"https://example.test/photo.jpg",filename:"photo.jpg"}],
};

test("draft serialization retains all content and attachment sources, strips UI keys",()=>{
  const content=composerDraftContent(composer);
  expect(content.body).toBe(composer.body);expect(content.body_html).toBe(composer.bodyHTML);
  expect(content.to).toBe("private@example.test");expect(content.attachments).toEqual(composer.attachments.map(({key,...rest})=>rest));
  expect(content.attachments?.some(attachment=>"key" in attachment)).toBe(false);
});
test("fingerprints ignore JSON key order and optional empty fields, not changes",()=>{
  const content=composerDraftContent(composer);
  const reordered={...content,template_vars:{},attachments:content.attachments?.map(attachment=>Object.fromEntries(Object.entries(attachment).reverse()))};
  expect(replyDraftFingerprint(content)).toBe(replyDraftFingerprint(reordered));
  for (const field of ["body","body_html","subject","from","to","channel"] as const) expect(replyDraftFingerprint({...content,[field]:"changed"})).not.toBe(replyDraftFingerprint(content));
  expect(replyDraftFingerprint({...content,attachments:[]})).not.toBe(replyDraftFingerprint(content));
});
test("closed WhatsApp window never drops freeform draft text or silently selects template",()=>{
  const content=composerDraftContent({...composer,channel:"whatsapp",templateId:"7",templateVars:{name:"Alice"},templateMode:false});
  expect(content.body).toBe(composer.body);expect(content.template_id).toBe(0);expect(content.template_vars).toEqual({});
  const template=composerDraftContent({...composer,channel:"whatsapp",templateId:"7",templateVars:{name:"Alice",count:2},templateMode:true});
  expect(template.body).toBe(composer.body);expect(template.template_id).toBe(7);expect(template.template_vars).toEqual({name:"Alice",count:2});
});
test("sent, discarded, sending and uncertain drafts cannot be edited",()=>{
  expect(replyDraftEditable()).toBe(true);
  for (const status of ["draft","sending","send_failed","sent","discarded"] as const) expect(replyDraftEditable({status} as SavedReplyDraft)).toBe(status==="draft");
});
