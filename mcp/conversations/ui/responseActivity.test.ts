import { expect, test } from "bun:test";
import { responseWaitingToolGroup } from "../frontend/src/responseActivity";
import { buildChatTimeline } from "../frontend/src/toolActivityModel";
import type { Message } from "../frontend/src/types";

const user={id:1,role:"user",created_at:"2026-10-08T17:42:00Z"} as Message;
const tool={id:"1",callId:"c1",name:"processes_start",reason:"Starting rebuild",agentId:41,threadId:"chat-a",state:"done" as const,startedAt:Date.parse("2026-10-08T17:43:00Z"),finishedAt:Date.parse("2026-10-08T17:43:01Z")};
const response={agentId:41,threadId:"chat-a",afterMessageId:1,createdAt:Date.parse(user.created_at)};
test("model waits stay scoped to the latest batch and response",()=>{
 const timeline=buildChatTimeline([user],[tool]).filter(item=>item.kind!=="day" && item.kind!=="time");
 expect(responseWaitingToolGroup(response,timeline,[user])).toBe(timeline.at(-1)!.key);
 for(const other of [{...response,agentId:42},{...response,threadId:"other"},{...response,afterMessageId:0,createdAt:tool.finishedAt+1},{...response,optimistic:true,createdAt:tool.finishedAt+1}]) expect(responseWaitingToolGroup(other,timeline,[user])).toBeUndefined();
 const later={id:2,role:"agent",created_at:"2026-10-08T17:43:02Z"} as Message;
 expect(responseWaitingToolGroup(response,buildChatTimeline([user,later],[tool]),[user,later])).toBeUndefined();
 const running={...tool,state:"running" as const};
 expect(responseWaitingToolGroup(response,buildChatTimeline([user],[running]),[user])).toBeUndefined();
});
