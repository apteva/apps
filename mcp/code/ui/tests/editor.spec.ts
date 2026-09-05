import {test,expect,type Page,type Route} from '@playwright/test';
async function fixture(page:Page,intercept?:(route:Route,path:string)=>Promise<boolean>){
 await page.route('**/api/**',async route=>{
  const url=new URL(route.request().url()),path=url.pathname;
  if(intercept && await intercept(route,path))return;
  let json:any={};
  if(path==='/api/auth/me')json={user_id:1,email:'test@example.invalid'};
  else if(path.endsWith('/repos'))json={repositories:[{id:1,slug:'alpha',name:'Alpha',framework:'blank'},{id:2,slug:'beta',name:'Beta',framework:'blank'}]};
  else if(path.endsWith('/tree'))json={files:[{path:'a.txt',size:5},{path:'b.txt',size:5},{path:'space #?%.txt',size:5}]};
  else if(path.includes('/files/')){await route.fulfill({body:path.includes('/beta/')?'Beta content':'Alpha content',headers:{ETag:'"old"'}});return;}
  else if(path.endsWith('/status'))json=path.includes('/git/')?{git_backed:false,changes:[]}:{dev_run:null};
  else if(path.endsWith('/issues'))json={issues:[],total:0};
  await route.fulfill({json});
 });
 await page.goto('/');await expect(page.getByText('Alpha',{exact:true})).toBeVisible();
}
async function openAlpha(page:Page){await page.getByText('Alpha',{exact:true}).click();await page.getByText('a.txt',{exact:true}).click();await expect(page.locator('code')).toContainText('Alpha content');}
test('late file response cannot cross repository selection',async({page})=>{
 let release!:()=>void;const held=new Promise<void>(r=>release=r);let requested!:()=>void;const started=new Promise<void>(r=>requested=r);
 await fixture(page,async(route,path)=>{if(path.includes('/alpha/files/a.txt')){requested();await held;await route.fulfill({body:'STALE ALPHA',headers:{ETag:'"old"'}});return true;}return false;});
 await page.getByText('Alpha',{exact:true}).click();await page.getByText('a.txt',{exact:true}).click();await started;
 await page.getByText('Beta',{exact:true}).click();await page.getByText('b.txt',{exact:true}).click();await expect(page.locator('code')).toContainText('Beta content');release();
 await page.waitForTimeout(150);await expect(page.locator('code')).not.toContainText('STALE ALPHA');
});
test('conflicting save keeps the draft and sends the loaded revision',async({page})=>{
 let revision='';await fixture(page,async(route,path)=>{if(path.includes('/files/')&&route.request().method()==='PUT'){revision=route.request().headers()['if-match'];await route.fulfill({status:409,body:'revision conflict'});return true;}return false;});
 await openAlpha(page);await page.getByRole('button',{name:'Edit',exact:true}).click();await page.locator('textarea').fill('My draft');await page.getByRole('button',{name:'Save',exact:true}).click();await expect(page.getByText(/Save failed; your draft is preserved/)).toBeVisible();await expect(page.locator('textarea')).toHaveValue('My draft');expect(revision).toBe('"old"');
});
test('new typing during a save remains dirty',async({page})=>{
 let release!:()=>void;const held=new Promise<void>(r=>release=r);let requested!:()=>void;const started=new Promise<void>(r=>requested=r);
 await fixture(page,async(route,path)=>{if(path.includes('/files/')&&route.request().method()==='PUT'){requested();await held;await route.fulfill({json:{file:{sha256:'new'}}});return true;}return false;});
 await openAlpha(page);await page.getByRole('button',{name:'Edit',exact:true}).click();await page.locator('textarea').fill('First');await page.getByRole('button',{name:'Save',exact:true}).click();await started;await page.locator('textarea').fill('Second');release();await expect(page.locator('textarea')).toHaveValue('Second');await expect(page.getByRole('button',{name:'Save',exact:true})).toBeEnabled();
});
test('external rename and deletion preserve dirty buffers',async({page})=>{
 await fixture(page);await openAlpha(page);await page.getByRole('button',{name:'Edit',exact:true}).click();await page.locator('textarea').fill('Keep me');
 await page.evaluate(()=> (window as any).emitCodeEvent('file.renamed',{slug:'alpha',from:'a.txt',to:'renamed.txt'}));await expect(page.locator('textarea')).toHaveValue('Keep me');
 await page.evaluate(()=> (window as any).emitCodeEvent('file.deleted',{slug:'alpha',path:'renamed.txt'}));await expect(page.locator('textarea')).toHaveValue('Keep me');await expect(page.getByText(/deleted externally/)).toBeVisible();
});
test('URL metacharacters remain part of the filename',async({page})=>{
 let seen='';await fixture(page,async(route,path)=>{if(path.includes('/files/')&&decodeURIComponent(path).includes('space #?%.txt')){seen=route.request().url();await route.fulfill({body:'Encoded file',headers:{ETag:'"x"'}});return true;}return false;});
 await page.getByText('Alpha',{exact:true}).click();await page.getByText('space #?%.txt',{exact:true}).click();await expect(page.locator('code')).toContainText('Encoded file');expect(seen).toContain('space%20%23%3F%25.txt?');
});

test('starting previews remain stoppable',async({page})=>{
 let stopped=false;
 await fixture(page,async(route,path)=>{
  if(path.endsWith('/dev/status')){await route.fulfill({json:{dev_run:{id:1,status:stopped?'stopped':'starting',framework:'nextjs',port:4400}}});return true;}
  if(path.endsWith('/dev/stop')){stopped=true;await route.fulfill({json:{}});return true;}return false;
 });
 await page.getByText('Alpha',{exact:true}).click();await expect(page.getByRole('button',{name:'Stop',exact:true})).toBeEnabled();await page.getByRole('button',{name:'Stop',exact:true}).click();await expect(page.getByRole('button',{name:'Run',exact:true})).toBeEnabled();expect(stopped).toBe(true);
});

test('late tree response cannot replace the selected repository tree',async({page})=>{
 let release!:()=>void;const held=new Promise<void>(r=>release=r);let requested!:()=>void;const started=new Promise<void>(r=>requested=r);
 await fixture(page,async(route,path)=>{
  if(path.includes('/alpha/tree')){requested();await held;await route.fulfill({json:{files:[{path:'stale-alpha.txt',size:4}]}});return true;}return false;
 });
 await page.getByText('Alpha',{exact:true}).click();await started;await page.getByText('Beta',{exact:true}).click();await expect(page.getByText('b.txt',{exact:true})).toBeVisible();release();await page.waitForTimeout(150);await expect(page.getByText('stale-alpha.txt',{exact:true})).toHaveCount(0);await expect(page.getByText('b.txt',{exact:true})).toBeVisible();
});

function issue(number:number){return {id:number,number,repo_slug:'alpha',title:`Issue ${number}`,body:`Body ${number}`,type:'task',status:'todo',state:'open',priority:'medium',created_at:'2026-09-01T00:00:00Z',updated_at:'2026-09-01T00:00:00Z'};}
async function issueFixture(page:Page){await fixture(page,async(route,path)=>{
 const url=new URL(route.request().url());
 if(path==='/api/apps/code/api/issues'){const offset=Number(url.searchParams.get('offset')||0);await route.fulfill({json:{issues:Array.from({length:Math.min(100,205-offset)},(_,i)=>issue(offset+i+1)),total:205}});return true;}
 const match=path.match(/\/repos\/alpha\/issues\/(\d+)$/);if(match){const offset=Number(url.searchParams.get('history_offset')||0);await route.fulfill({json:{issue:issue(Number(match[1])),comments:[{id:offset+1,body:`History comment ${offset+1}`,author:'Test',created_at:'2026-09-01T00:00:00Z'}],links:[],comments_total:201,events_total:0,links_total:0}});return true;}return false;
});}
test('issues beyond the first page and long histories remain accessible',async({page})=>{
 await issueFixture(page);await page.getByRole('button',{name:'Issues',exact:true}).click();await expect(page.getByText('205 issues',{exact:true})).toBeVisible();await page.getByRole('button',{name:'Next',exact:true}).click();await expect(page.getByText('Issue 101',{exact:true})).toBeVisible();await page.getByRole('button',{name:'Next',exact:true}).click();await expect(page.getByText('Issue 205',{exact:true})).toBeVisible();await page.getByText('Issue 205',{exact:true}).click();
 await expect(page.getByPlaceholder('Describe the issue…')).toHaveValue('Body 205');await page.getByRole('button',{name:'Next history',exact:true}).click();await expect(page.getByText('History comment 201',{exact:true})).toBeVisible();
});
test('a deep-linked issue outside the first page loads directly',async({page})=>{
 await issueFixture(page);await page.goto('/?view=issues&repo=alpha&issue=205');await expect(page.getByPlaceholder('Describe the issue…')).toHaveValue('Body 205');await expect(page.getByText('205 issues',{exact:true})).toBeVisible();
});
