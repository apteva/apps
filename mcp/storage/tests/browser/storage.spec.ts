import {test,expect} from '@playwright/test';
const row=(id:number,name:string,visibility='private')=>({id,name,folder:'/',size_bytes:5,content_type:'text/plain',sha256:'abc',visibility,created_at:'2026-09-05',url:`http://127.0.0.1:19180/api/apps/storage/public/files/${id}/content?project_id=p1&install_id=42`});
test('selection follows visibility changes and paginated lists',async({page})=>{
 let visibility='public';const offsets:string[]=[];
 await page.route('**/api/apps/storage/**',async route=>{if(new URL(route.request().url()).pathname.includes("/ui/"))return route.continue();const r=route.request(),u=new URL(r.url());expect(u.searchParams.get('project_id')).toBe('p1');expect(u.searchParams.get('install_id')).toBe('42');
 if(u.pathname.endsWith('/folders'))return route.fulfill({json:{folders:[]}});
 if(r.method()==='PATCH'){visibility=r.postDataJSON().visibility;return route.fulfill({json:{file:row(1,'note.txt',visibility)}})}
 if(u.pathname.endsWith('/files')){offsets.push(u.searchParams.get('offset')||'');return route.fulfill({json:{files:[row(u.searchParams.get('offset')==='200'?2:1,u.searchParams.get('offset')==='200'?'page-two.txt':'note.txt',visibility)],has_more:u.searchParams.get('offset')!=='200'}})}
 return route.fulfill({body:'hello',contentType:'text/plain'});
 });
 await page.goto('/');await page.getByRole('row').filter({hasText:'note.txt'}).click();await page.getByRole('button',{name:'Make private',exact:true}).click();
 await expect(page.getByRole('button',{name:'Revoke links'})).toBeVisible();await expect(page.getByRole('button',{name:'Share',exact:true})).toBeVisible();
 await page.getByRole('button',{name:'Next',exact:true}).click();await expect(page.getByText('page-two.txt',{exact:true})).toBeVisible();await expect(page.getByRole('button',{name:'Close',exact:true})).toHaveCount(0);expect(offsets).toContain('200');
});
test('a late root refresh cannot overwrite the newly opened folder',async({page})=>{
 let delay=false;
 await page.route('**/api/apps/storage/**',async route=>{if(new URL(route.request().url()).pathname.includes("/ui/"))return route.continue();const u=new URL(route.request().url()),folder=u.searchParams.get('folder')||u.searchParams.get('parent');if(delay&&folder==='/')await new Promise(r=>setTimeout(r,350));
 if(u.pathname.endsWith('/folders'))return route.fulfill({json:{folders:folder==='/'?['notes']:[]}}).catch(()=>{});
 return route.fulfill({json:{files:[row(1,folder==='/notes/'?'inside.txt':'root.txt')]}}).catch(()=>{});
 });
 await page.goto('/');await expect(page.getByText('root.txt',{exact:true})).toBeVisible();delay=true;
 await page.evaluate(()=>{(window as any).fireStorageEvent({topic:'file.updated',install_id:42})});await page.waitForTimeout(130);
 await page.getByRole('button',{name:'notes',exact:true}).click();await expect(page.getByText('inside.txt',{exact:true})).toBeVisible();await page.waitForTimeout(500);await expect(page.getByText('root.txt',{exact:true})).toHaveCount(0);
});
test('batch uploads continue after one failure and folder errors are visible',async({page})=>{
 let posts=0;
 await page.route('**/api/apps/storage/**',async route=>{if(new URL(route.request().url()).pathname.includes("/ui/"))return route.continue();const r=route.request(),u=new URL(r.url());if(r.method()==='POST'){posts++;return route.fulfill(posts===2?{json:row(2,'good.txt')}:{status:500,body:'write failed'})}return route.fulfill({json:u.pathname.endsWith('/folders')?{folders:[]}:{files:[]}})});
 await page.goto('/');await page.locator('input[type=file]').setInputFiles([{name:'bad.txt',mimeType:'text/plain',buffer:Buffer.from('bad')},{name:'good.txt',mimeType:'text/plain',buffer:Buffer.from('good')}]);
 await expect.poll(()=>posts).toBe(2);await expect(page.getByText(/write failed/)).toBeVisible();
 const dialog=page.waitForEvent('dialog');await page.getByPlaceholder(/folder/i).fill('new-folder');await page.getByRole('button',{name:'+ Folder',exact:true}).click();const d=await dialog;expect(d.message()).toContain('Create folder failed');await d.accept();await expect(page.getByPlaceholder(/folder/i)).toHaveValue('new-folder');
});
test('card events use current file ID and every URL includes scope',async({page})=>{
 let second=0;
 await page.route('**/api/apps/storage/**',async route=>{if(new URL(route.request().url()).pathname.includes("/ui/"))return route.continue();const u=new URL(route.request().url());expect(u.searchParams.get('project_id')).toBe('p1');expect(u.searchParams.get('install_id')).toBe('42');const id=Number(u.pathname.split('/')[5]);if(id===2)second++;return route.fulfill({json:{file:row(id,id===2?(second>1?'updated.txt':'second.txt'):'first.txt')}})});
 await page.goto('/?card');await expect(page.getByText('first.txt',{exact:true})).toBeVisible();await page.evaluate(()=>{(window as any).selectCard(2)});await expect(page.getByText('second.txt',{exact:true})).toBeVisible();
 await page.evaluate(()=>{(window as any).fireStorageEvent({topic:'file.updated',data:{id:2}})});await expect(page.getByText('updated.txt',{exact:true})).toBeVisible();await expect(page.getByRole('link',{name:'Open'})).toHaveAttribute('href',/project_id=p1&install_id=42/);
});
test('resuming reuses verified parts after a transient failure',async({page})=>{
 let inits=0,firstPartWrites=0,fail=true;const parts:{n:number,size:number}[]=[];
 await page.route('**/api/apps/storage/**',async route=>{if(new URL(route.request().url()).pathname.includes("/ui/"))return route.continue();const req=route.request(),u=new URL(req.url());
 if(u.pathname.endsWith('/folders'))return route.fulfill({json:{folders:[]}});
 if(u.pathname.endsWith('/files'))return route.fulfill({json:{files:[]}});
 if(u.pathname.endsWith('/uploads')){inits++;return route.fulfill({json:{upload_id:'00000000000000000000000001',part_size:5*1024*1024,max_parallel:1,max_parts:100}})}
 if(u.pathname.includes('/parts/')){const n=Number(u.pathname.split('/').at(-1));if(n===1)firstPartWrites++;if(n===2&&fail)return route.fulfill({status:503,body:'temporary'});const size=req.postDataBuffer()!.length;parts.push({n,size});return route.fulfill({json:{size}})}
 if(u.pathname.endsWith('/complete'))return route.fulfill({json:{file:row(7,'large.bin')}});
 return route.fulfill({json:{declared_size:30*1024*1024,parts}});
 });
 await page.goto('/');
 const run=()=>page.evaluate(async()=>{const f=new File([new Uint8Array(30*1024*1024)],'large.bin',{type:'application/octet-stream'});try{return await (window as any).uploadResumable(f,{projectId:'p1',installId:42,parallel:1})}catch(e){return {error:String(e)}}});
 const first=await run();expect(first.error).toContain('failed');fail=false;const second=await run();expect(second.id).toBe(7);expect(inits).toBe(1);expect(firstPartWrites).toBe(1);
});
