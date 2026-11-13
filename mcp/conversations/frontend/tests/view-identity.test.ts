import {expect,test} from "bun:test";
import {appViewKey} from "../src/cardWorkspace";
test("resource identity ignores display props but separates conversations, projects and installations",async()=>{
 const resource={app_install_id:19,resource_type:"computer.browser_session",resource_id:"br_test"};
 const key=await appViewKey("project","chat",19,"computer","browser-view",resource,{caption:"Before"});
 expect(await appViewKey("project","chat",19,"computer","browser-view",resource,{caption:"After"})).toBe(key);
 for(const [project,chat,install] of [["other","chat",19],["project","other",19],["project","chat",20]] as const)expect(await appViewKey(project,chat,install,"computer","browser-view",resource,{})).not.toBe(key);
});
test("identical legacy references coalesce independent of object property order",async()=>{
 const a=await appViewKey("project","chat",19,"computer","browser-view",undefined,{session_id:"br_test",caption:"Browser"});
 expect(await appViewKey("project","chat",19,"computer","browser-view",undefined,{caption:"Browser",session_id:"br_test"})).toBe(a);
 expect(await appViewKey("project","chat",19,"computer","browser-view",undefined,{session_id:"br_other",caption:"Browser"})).not.toBe(a);
});
