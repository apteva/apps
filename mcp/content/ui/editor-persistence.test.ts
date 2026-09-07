import { expect, test } from "bun:test";
import { persistEditor, type ContentAPI } from "./editor-persistence";
const post = { id: 7, edit_version: 1, title: "New title", body_blocks: { blocks: [{ id: "", type: "core/form" }] } };
test("failed save never publishes", async () => {
 const calls: string[] = [];
 const api: ContentAPI = async path => { calls.push(path); throw new Error("save failed"); };
 await expect(persistEditor(api, post, [], true, true, () => {})).rejects.toThrow("save failed");
 expect(calls).toEqual(["/admin/posts/7"]);
});
test("publish uses the saved version and accepts server block IDs", async () => {
 const calls: Array<{path:string;body:any}> = [];
 const saved = {...post, edit_version:2, body_blocks:{blocks:[{id:"b_server",type:"core/form"}]}};
 const published = {...saved, edit_version:3};
 const api = (async (path: string, opts: RequestInit) => {calls.push({path,body:JSON.parse(String(opts.body))});return {post:opts.method==="PATCH"?saved:published};}) as ContentAPI;
 let accepted:any;
 expect(await persistEditor(api,post,post.body_blocks.blocks,true,true,p => {accepted=p})).toEqual(published);
 expect(accepted.body_blocks.blocks[0].id).toBe("b_server");
 expect(calls.map(c=>c.body.expected_version)).toEqual([1,2]);
});
test("successful save remains accepted if publication fails", async () => {
 const saved={...post,edit_version:2};let accepted:any;
 const api = (async (_path:string,opts:RequestInit) => {if(opts.method==="POST")throw new Error("conflict");return {post:saved};}) as ContentAPI;
 await expect(persistEditor(api,post,[],true,true,p=>{accepted=p})).rejects.toThrow("conflict");
 expect(accepted).toEqual(saved);
});
test("clean document publishes without an unnecessary save", async () => {
 const calls:string[]=[];
 const api=(async (path:string)=>{calls.push(path);return {post};}) as ContentAPI;
 await persistEditor(api,post,[],false,true,()=>{throw new Error("unexpected save")});
 expect(calls).toEqual(["/admin/posts/7/publish"]);
});
