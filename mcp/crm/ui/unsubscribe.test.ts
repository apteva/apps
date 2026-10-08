import { expect, test } from "bun:test";
import { emailUnsubscribeConfirmation, emailUnsubscribeLabel, emailUnsubscribeRequest, type EmailUnsubscribeState } from "./unsubscribe";
import { latestMessageAddresses } from "./message_addresses";

const state:EmailUnsubscribeState={project_id:"p",contact_id:1,conversation_id:2,address:"edbis@free.fr",outbound_blocked:false,inbound_blocked:false,unsubscribed:false,confirmed:false};
test("unsubscribe audit cannot replace the latest message recipient",()=>{
  const addresses={from:"edbis@free.fr",to:["support@example.test"]};
  expect(latestMessageAddresses([{kind:"email_received",message_addresses:addresses},{kind:"system"}])).toEqual(addresses);
  expect(latestMessageAddresses([{kind:"email_received",message_addresses:addresses},{kind:"email_received"}])).toBeUndefined();
});
test("unsubscribe confirmation pins exact email, project and outbound scope",()=>{
  expect(emailUnsubscribeRequest(state)).toEqual({expected_address:"edbis@free.fr"});
  const text=emailUnsubscribeConfirmation(state);
  expect(text).toContain("edbis@free.fr");expect(text).toContain("this project only");expect(text).toContain("campaigns and manual replies");expect(text).toContain("Incoming replies will still reach");
  expect(emailUnsubscribeConfirmation({...state,inbound_blocked:true})).toContain("existing inbound block remains");
});
test("unsubscribed and stronger blocks are visibly different",()=>{
  expect(emailUnsubscribeLabel(state)).toBe("Unsubscribe this email");
  expect(emailUnsubscribeLabel({...state,outbound_blocked:true})).toBe("Email blocked");
  expect(emailUnsubscribeLabel({...state,outbound_blocked:true,unsubscribed:true})).toBe("Unsubscribed");
  expect(()=>emailUnsubscribeRequest({...state,address:"Unknown"})).toThrow();
});
