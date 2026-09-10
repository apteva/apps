import {test,expect} from '@playwright/test';
import {mkdtempSync,openSync,ftruncateSync,closeSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
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
 if(u.pathname.endsWith('/uploads')&&req.method()==='GET')return route.fulfill({json:{max_file_bytes:4*1024**3,max_pending_bytes:4*1024**3}});
 if(u.pathname.endsWith('/uploads')){inits++;return route.fulfill({json:{upload_id:'00000000000000000000000001',part_size:5*1024*1024,max_parallel:1,max_parts:100}})}
 if(u.pathname.includes('/parts/')){const n=Number(u.pathname.split('/').at(-1));if(n===1)firstPartWrites++;if(n===2&&fail)return route.fulfill({status:503,body:'temporary'});const size=req.postDataBuffer()!.length;parts.push({n,size});return route.fulfill({json:{size}})}
 if(u.pathname.endsWith('/complete'))return route.fulfill({json:{file:row(7,'large.bin')}});
 return route.fulfill({json:{declared_size:30*1024*1024,parts}});
 });
 await page.goto('/');
 const run=()=>page.evaluate(async()=>{const f=(window as any).retryFile ||= new File([new Uint8Array(30*1024*1024)],'large.bin',{type:'application/octet-stream'});try{return await (window as any).uploadResumable(f,{projectId:'p1',installId:42,parallel:1})}catch(e){return {error:String(e)}}});
 const first=await run();expect(first.error).toContain('failed');fail=false;const second=await run();expect(second.id).toBe(7);expect(inits).toBe(1);expect(firstPartWrites).toBe(1);
});

test('oversized pending allowance fails before reading a large file',async({page})=>{
 let posts=0;
 await page.route('**/api/apps/storage/**',async route=>{
  const r=route.request(),u=new URL(r.url());if(u.pathname.includes('/ui/'))return route.continue();
  if(r.method()==='POST')posts++;
  if(u.pathname.endsWith('/uploads'))return route.fulfill({json:{max_file_bytes:4*1024**3,max_pending_bytes:1024**3}});
  return route.fulfill({json:u.pathname.endsWith('/folders')?{folders:[]}:{files:[]}});
 });
 await page.goto('/');await expect(page.getByRole('button',{name:'+ Folder',exact:true})).toBeVisible();
 const result=await page.evaluate(async()=>{
  let read=false;const file={name:'large.bin',size:3*1024**3,type:'application/octet-stream',stream(){read=true;throw Error('File should not be read')}};
  try{await (window as any).uploadResumable(file,{projectId:'p1',installId:42});return {error:'accepted',read}}
  catch(e){return {error:String(e),read}}
 });
 expect(result.error).toContain('pending-upload allowance (1024 MiB)');expect(result.read).toBe(false);expect(posts).toBe(0);
});

test('shipped panel starts large uploads without reading the file first',async({page})=>{
 await page.addInitScript(()=>{File.prototype.stream=function(){throw Error('Unexpected preparation read')};File.prototype.arrayBuffer=async function(){throw Error('Unexpected whole-file read')};});
 let posts=0;
 await page.route('**/api/apps/storage/**',async route=>{
  const r=route.request(),u=new URL(r.url());if(u.pathname.includes('/ui/'))return route.continue();
  if(u.pathname.endsWith('/uploads')){
   if(r.method()==='GET')return route.fulfill({json:{max_file_bytes:4*1024**3,max_pending_bytes:4*1024**3}});
   posts++;expect(r.postDataJSON().sha256).toBeUndefined();expect(r.postDataJSON().direct).toBe(true);
   return route.fulfill({json:{was_existing:true,file:row(8,'immediate.bin')}});
  }
  return route.fulfill({json:u.pathname.endsWith('/folders')?{folders:[]}:{files:[]}});
 });
 await page.goto('/');await page.locator('input[type=file]').setInputFiles({name:'immediate.bin',mimeType:'application/octet-stream',buffer:Buffer.alloc(26*1024*1024)});
 await expect.poll(()=>posts).toBe(1);await expect(page.getByText('uploaded',{exact:true})).toBeVisible();
 await expect(page.getByText(/Preparing file/)).toHaveCount(0);
});

test('2 GiB upload uses parallel direct parts, retries with fresh signatures, and sends no bytes through Apteva',async({page})=>{
 let active=0,peak=0,bytes=0,attempts=0,signs=0,complete=0;
 await page.route('https://bucket.example/**',async route=>{
  expect(route.request().method()).toBe('PUT');expect(route.request().headers().cookie).toBeUndefined();
  active++;peak=Math.max(peak,active);attempts++;const attempt=attempts;
  await new Promise(r=>setTimeout(r,20));active--;
  if(attempt===1)return route.fulfill({status:503,body:'temporary'});
  bytes+=route.request().postDataBuffer()!.length;
  return route.fulfill({status:200,headers:{'Access-Control-Allow-Origin':'*'}});
 });
 await page.route('**/api/apps/storage/**',async route=>{
  const r=route.request(),u=new URL(r.url());if(u.pathname.includes('/ui/'))return route.continue();
  expect(r.method()).not.toBe('PUT');
  if(u.pathname.endsWith('/uploads'))return route.fulfill({json:r.method()==='GET'?{max_file_bytes:5*1024**3,max_pending_bytes:5*1024**3}:{upload_id:'DIRECT0001',mode:'s3_multipart',part_size:16*1024**2,max_parallel:4,max_parts:10000}});
  if(u.pathname.includes('/parts/')){signs++;return route.fulfill({json:{url:'https://bucket.example/'+u.pathname.split('/').at(-1),headers:{}}})}
  if(u.pathname.endsWith('/complete')){complete++;return route.fulfill({json:{file:row(9,'video.mp4')}})}
  return route.fulfill({json:u.pathname.endsWith('/folders')?{folders:[]}:{files:[]}});
 });
 await page.goto('/');
 const result=await page.evaluate(async()=>{
  // A 2 GiB descriptor exercises all 128 slices without allocating a 2 GiB
  // fixture. Smaller payloads keep intercepted browser traffic bounded.
  const slices:number[]=[];
  const file={name:'video.mp4',type:'video/mp4',size:2*1024**3,stream(){throw Error('whole-file read')},arrayBuffer(){throw Error('whole-file read')},slice(start:number,end:number){slices.push(end-start);return new Blob([new Uint8Array(1024)])}};
  const out=await (window as any).uploadResumable(file,{projectId:'p1',installId:42});return {out,slices};
 });
 expect(result.out.id).toBe(9);expect(result.slices.length).toBeGreaterThanOrEqual(128);expect(result.slices.every((n:number)=>n===16*1024**2)).toBe(true);
 expect(peak).toBeGreaterThan(1);expect(peak).toBeLessThanOrEqual(4);expect(complete).toBe(1);expect(bytes).toBe(128*1024);expect(signs).toBe(129);
});

test('real 2 GiB body streams directly to a cross-origin backend',async({page,request})=>{
 test.setTimeout(120000);
 const id='STREAMREAL2G';
 await page.goto('/');
 const directory=mkdtempSync(join(tmpdir(),'storage-2g-'));
 const path=join(directory,'real-2g.mp4');const fd=openSync(path,'w');ftruncateSync(fd,2*1024**3);closeSync(fd);
 const start=Date.now();
 try{
  await page.evaluate(()=>{File.prototype.stream=function(){throw Error('whole-file read')};File.prototype.arrayBuffer=async function(){throw Error('whole-file read')};});
  await page.locator('input[type=file]').setInputFiles(path);
  await expect(page.getByText('uploaded',{exact:true})).toBeVisible({timeout:100000});
 }finally{rmSync(directory,{recursive:true,force:true})}
 const elapsed=Date.now()-start;
 const stats=await (await request.get(`http://127.0.0.1:19181/${id}`)).json();
 expect(stats.bytes).toBe(2*1024**3);expect(stats.parts).toBe(128);expect(stats.peak).toBeGreaterThan(1);expect(stats.peak).toBeLessThanOrEqual(4);
 expect(stats.apiBytes).toBeLessThan(2048);
 console.log(`2 GiB direct transfer: ${elapsed} ms; ${stats.parts} parts; peak concurrency ${stats.peak}; Apteva API bytes ${stats.apiBytes}`);
});
