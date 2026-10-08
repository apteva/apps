import { expect, test } from "bun:test";
import { inboxRowsWithSelection, refreshedInboxSelection, type InboxItem } from "./inbox";

const row = (id:number, status="open"):InboxItem => ({id, status, contact_id:id, channel:"email", priority:"normal", last_activity_at:"2026-10-08T12:00:00Z"});

test("reply moving open to pending cannot auto-advance to another conversation",()=>{
  const current=row(2);
  expect(refreshedInboxSelection([row(1)],current)).toBe(current);
  expect(refreshedInboxSelection([],current)).toBe(current);
});
test("status/event/manual refresh and pagination retain an absent selected thread",()=>{
  const current=row(80,"closed");
  for(const rows of [[],[row(1)],Array.from({length:50},(_,i)=>row(i+1))]) {
    expect(refreshedInboxSelection(rows,current)).toBe(current);
  }
});
test("refresh updates selected row metadata without following list order",()=>{
  const current=row(2), updated=row(2,"pending");
  expect(refreshedInboxSelection([row(1),updated],current)).toBe(updated);
});
test("initial/deep-link selection and deliberate filter changes still work",()=>{
  const rows=[row(1),row(2)];
  expect(refreshedInboxSelection(rows,null)).toBe(rows[0]);
  expect(refreshedInboxSelection(rows,null,2)).toBe(rows[1]);
  expect(refreshedInboxSelection([],null)).toBeNull();
  expect(refreshedInboxSelection(rows,row(3),undefined,true)).toBe(rows[0]);
  expect(refreshedInboxSelection([],row(3),undefined,true)).toBeNull();
  expect(refreshedInboxSelection(rows,row(2),undefined,true)).toBe(rows[1]);
});

test("retained selection is display-only, unique, and disappears on navigation", () => {
  const queue = [row(1), row(2)];
  const retained = row(3, "pending");
  expect(inboxRowsWithSelection(queue, retained)).toEqual([retained, ...queue]);
  expect(queue).toHaveLength(2);
  expect(inboxRowsWithSelection(queue, queue[1])).toBe(queue);
  expect(inboxRowsWithSelection(queue, null)).toBe(queue);
  expect(inboxRowsWithSelection([], retained)).toEqual([retained]);
});
