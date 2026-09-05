import { test, expect } from 'bun:test';
import { Window } from 'happy-dom';
import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import AuthPanel, { UserRolesEditor, UserDrawer } from './AuthPanel';

const win = new Window();
// Bun does not initialize this Happy DOM window intrinsic automatically.
Object.assign(win,{SyntaxError});
Object.assign(globalThis, { window: win, document: win.document, navigator: win.navigator, HTMLElement: win.HTMLElement, IS_REACT_ACT_ENVIRONMENT: true });

test('switching users resets roles even when authorization versions match', async () => {
 const calls: {url: string, body: string}[]=[];
 globalThis.fetch = (async (url: string, opts?: RequestInit) => {
   if(opts?.method==='PUT') calls.push({url:String(url), body:String(opts.body)});
   return Response.json({ roles:[{id:1,key:'admin',name:'Admin',permissions:[]},{id:2,key:'member',name:'Member',permissions:[]}] });
 }) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 const base={orgSlug:'default',projectId:'test',setStatus:()=>{},onChanged:()=>{}};
 const auth=(userId:string,role:string)=>({user_id:userId,organization_id:'1',organization_slug:'default',roles:[role],permissions:[],authorization_version:2});
 await act(async()=>{root.render(<UserRolesEditor {...base} userId={1} authorization={auth('1','admin')}/>)});
 await act(async()=>{root.render(<UserRolesEditor {...base} userId={2} authorization={auth('2','member')}/>)});
 const inputs=div.getElementsByTagName('input');expect(inputs[0].checked).toBe(false);expect(inputs[1].checked).toBe(true);
 expect(div.getElementsByTagName('button')[0]!.disabled).toBe(true);
 expect(calls.length).toBe(0);
 await act(async()=>root.unmount());div.remove();
});

test('switching users hides previous drawer and all actions while loading', async () => {
 const calls:string[]=[];
 const data={user:{id:1,email:'a@example.com',status:'active'},authorization:{user_id:'1',roles:[],permissions:[],authorization_version:1},sessions:[{id:1,client_id:'test'}],audit_log:[]};
 globalThis.fetch=(async(url:string,opts?:RequestInit)=>{
   const s=String(url);if(opts?.method==='POST'){calls.push(s);return Response.json({ok:true})}
   if(s.includes('/users/2/context')) return new Promise(()=>{});
   if(s.includes('/roles')) return Response.json({roles:[]});
   return Response.json(data);
 }) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 const base={orgSlug:'default',projectId:'test',setStatus:()=>{},onChanged:()=>{},onClose:()=>{}};
 await act(async()=>{root.render(<UserDrawer {...base} userId={1}/>)});
 await act(async()=>{root.render(<UserDrawer {...base} userId={2}/>)});
 expect(div.textContent).not.toContain('a@example.com');
 const revoke=Array.from(div.getElementsByTagName('button')).find(b=>b.textContent?.includes('Revoke'));
 expect(revoke).toBeUndefined();expect(calls).toEqual([]);
 await act(async()=>root.unmount());div.remove();
});


test('late response cannot replace the current user',async()=>{
 let resolveA!:(r:Response)=>void;
 globalThis.fetch=(async(url:string)=>{
  if(String(url).includes('/users/1/context'))return new Promise<Response>(resolve=>{resolveA=resolve});
  if(String(url).includes('/roles'))return Response.json({roles:[]});
  return Response.json({user:{id:2,email:'b@example.com',status:'active'},authorization:{user_id:'2',roles:[],permissions:[],authorization_version:1},sessions:[],audit_log:[]});
 }) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 const base={orgSlug:'default',projectId:'test',setStatus:()=>{},onChanged:()=>{},onClose:()=>{}};
 await act(async()=>root.render(<UserDrawer {...base} userId={1}/>));
 await act(async()=>root.render(<UserDrawer {...base} userId={2}/>));
 await act(async()=>resolveA(Response.json({user:{id:1,email:'a@example.com'},authorization:{user_id:'1',roles:[],permissions:[],authorization_version:1},sessions:[],audit_log:[]})));
 expect(div.textContent).toContain('b@example.com');expect(div.textContent).not.toContain('a@example.com');
 await act(async()=>root.unmount());div.remove();
});

test('panel instances keep independent project and install scope',async()=>{
 const calls:string[]=[];
 globalThis.fetch=(async(url:string)=>{calls.push(String(url));return Response.json({organizations:[],events:[],active:0,disabled:0,locked:0,signups_7d:0,logins_24h:0})}) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 await act(async()=>root.render(<><AuthPanel appName="auth" projectId="alpha" installId={11}/><AuthPanel appName="auth" projectId="beta" installId={22}/></>));
 expect(calls.length).toBeGreaterThanOrEqual(4);
 for(const raw of calls){const q=new URL(raw,'https://example.com').searchParams;expect(q.get('install_id')).toBe(q.get('project_id')==='alpha'?'11':'22')}
 await act(async()=>root.unmount());div.remove();
});

test('saving after switching users sends only the current selection',async()=>{
 const writes:{url:string;body:any}[]=[];
 globalThis.fetch=(async(url:string,init?:RequestInit)=>{if(init?.method==='PUT')writes.push({url:String(url),body:JSON.parse(String(init.body))});return Response.json({roles:[{id:1,key:'admin',name:'Admin',permissions:[]},{id:2,key:'member',name:'Member',permissions:[]}]})}) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 const base={orgSlug:'default',projectId:'test',setStatus:()=>{},onChanged:()=>{}};
 const auth=(id:string,role:string)=>({user_id:id,organization_id:'1',organization_slug:'default',roles:[role],permissions:[],authorization_version:2});
 await act(async()=>root.render(<UserRolesEditor {...base} userId={1} authorization={auth('1','admin')}/>));
 await act(async()=>root.render(<UserRolesEditor {...base} userId={2} authorization={auth('2','member')}/>));
 await act(async()=>div.getElementsByTagName('input')[1]!.click());
 await act(async()=>div.getElementsByTagName('button')[0]!.click());
 expect(writes).toHaveLength(1);expect(writes[0].url).toContain('/users/2/roles');expect(writes[0].body.role_ids).toEqual([]);
 await act(async()=>root.unmount());div.remove();
});

test('new user roles cannot be saved while the catalog is pending',async()=>{
 let calls=0;
 globalThis.fetch=(async(_url:string)=>{if(++calls>1)return new Promise(()=>{});return Response.json({roles:[{id:1,key:'admin',name:'Admin',permissions:[]}]})}) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 const base={orgSlug:'default',projectId:'test',setStatus:()=>{},onChanged:()=>{}};
 const auth=(id:string)=>({user_id:id,organization_id:'1',organization_slug:'default',roles:['admin'],permissions:[],authorization_version:2});
 await act(async()=>root.render(<UserRolesEditor {...base} userId={1} authorization={auth('1')}/>));
 await act(async()=>root.render(<UserRolesEditor {...base} userId={2} authorization={auth('2')}/>));
 expect(div.getElementsByTagName('button')[0]!.disabled).toBe(true);expect(div.getElementsByTagName('input')).toHaveLength(0);
 await act(async()=>root.unmount());div.remove();
});

test('same ID in another organization resets the drawer immediately',async()=>{
 globalThis.fetch=(async(url:string)=>{if(String(url).includes('organization_slug=beta'))return new Promise(()=>{});if(String(url).includes('/roles'))return Response.json({roles:[]});return Response.json({user:{id:1,email:'alpha@example.com',status:'active'},authorization:{user_id:'1',roles:[],permissions:[],authorization_version:1},sessions:[],audit_log:[]})}) as typeof fetch;
 const div=document.createElement('div');document.body.append(div);const root=createRoot(div);
 const base={userId:1,projectId:'test',setStatus:()=>{},onChanged:()=>{},onClose:()=>{}};
 await act(async()=>root.render(<UserDrawer {...base} orgSlug="alpha"/>));
 await act(async()=>root.render(<UserDrawer {...base} orgSlug="beta"/>));
 expect(div.textContent).not.toContain('alpha@example.com');expect(div.textContent).toContain('Loading');
 await act(async()=>root.unmount());div.remove();
});
