import {test,expect,type Page} from '@playwright/test';

// The Git & runs view against a scripted backend: pull request approval and
// merge, branch deletion, tags, command runs and the operation log.
const pull={number:42,url:'https://github.com/apteva/apps/pull/42',state:'open',title:'Release places 0.2.0',head:'feat/places',head_sha:'abc123def456',base:'main',draft:true,merged:false,mergeable:true,author:'marco',created_at:'2026-11-19T10:00:00Z'};

async function fixture(page:Page,opts:{gitBacked?:boolean}={}){
 const posts:Array<{path:string;body:any}>=[];
 let approved=false;
 await page.route('**/api/**',async route=>{
  const req=route.request(),url=new URL(req.url()),path=url.pathname;
  if(req.method()==='POST'){
   const body=req.postDataJSON();posts.push({path,body});
   if(path.endsWith('/git/pull-requests')&&body.action==='approve'){approved=true;await route.fulfill({json:{action:'approve',pull_request:{...pull,approved_by:['user:1']}}});return;}
   if(path.endsWith('/git/pull-requests')&&body.action==='merge'){await route.fulfill({json:{action:'merge',merged:true,merge_sha:'m1'}});return;}
   if(path.endsWith('/git/branches/delete')&&!body.force){await route.fulfill({status:409,body:'the local branch of wip has commits not merged into main; pass force=true to delete it anyway'});return;}
   await route.fulfill({json:{}});return;
  }
  let json:any={};
  if(path==='/api/auth/me')json={user_id:1,email:'test@example.invalid'};
  else if(path.endsWith('/repos'))json={repositories:[{id:1,slug:'apps',name:'Apps',framework:'blank'}]};
  else if(path.endsWith('/tree'))json={files:[]};
  else if(path.endsWith('/git/status'))json=opts.gitBacked===false?{git_backed:false,changes:[]}:{git_backed:true,branch:'main',ahead:0,behind:0,dirty:false,conflicted:false,changes:[],remote:{fetch_url:'https://github.com/apteva/apps.git',default_branch:'main'}};
  else if(path.endsWith('/status'))json={dev_run:null};
  else if(path.endsWith('/git/pull-requests'))json={pull_requests:[{...pull,approved_by:approved?['user:1']:undefined}],count:1};
  else if(path.endsWith('/git/branches'))json={branches:[{name:'main',current:true,remote:false,sha:'aaa111'},{name:'wip',current:false,remote:false,sha:'bbb222'},{name:'origin/main',current:false,remote:true,sha:'aaa111'},{name:'origin/feat/places',current:false,remote:true,sha:'abc123def456'}],count:4};
  else if(path.endsWith('/git/tags'))json={tags:[{name:'places/v0.2.0',sha:'abc123def456',annotated:true,on_remote:'absent'},{name:'places/v0.1.0',sha:'aaa111',annotated:true,on_remote:'same'}],count:2};
  else if(path.endsWith('/runs/7'))json={id:7,runtime:'workspace',workspace_id:'ws1',command:'go test ./...',status:'failed',exit_code:1,timed_out:false,duration_ms:36120,actor:'agent:3',output_tail:'--- FAIL: TestPlaces (0.01s)\nFAIL',created_at:'2026-11-19 10:05:00'};
  else if(path.endsWith('/runs'))json={runs:[{id:7,runtime:'workspace',workspace_id:'ws1',command:'go test ./...',status:'failed',exit_code:1,timed_out:false,duration_ms:36120,actor:'agent:3',created_at:'2026-11-19 10:05:00'},{id:6,runtime:'workspace',command:'go vet ./...',status:'completed',exit_code:0,timed_out:false,duration_ms:900,actor:'agent:3',created_at:'2026-11-19 10:04:00'}],count:2};
  else if(path.endsWith('/git/operations'))json={operations:[{id:3,operation:'pr_merge',actor:'agent:3',from_sha:'abc123def456',to_sha:'',status:'error',error:'pull request #42 needs a person\'s approval of its current head abc123def before an agent can merge it',created_at:'2026-11-19 10:06:00'},{id:2,operation:'push',actor:'agent:3',from_sha:'aaa111',to_sha:'abc123def456',status:'ok',created_at:'2026-11-19 10:03:00'}],count:2};
  else if(path.endsWith('/issues'))json={issues:[],total:0};
  await route.fulfill({json});
 });
 await page.goto('/');
 await page.getByText('Apps',{exact:true}).click();
 await page.getByRole('button',{name:'Git & runs'}).click();
 return posts;
}

test('a person approves then merges a pull request pinned to its head',async({page})=>{
 const posts=await fixture(page);
 await expect(page.getByText('#42 Release places 0.2.0')).toBeVisible();
 await expect(page.getByText('needs approval')).toBeVisible();
 await page.getByRole('button',{name:'Approve',exact:true}).click();
 await expect(page.getByText('approved by Person 1')).toBeVisible();
 expect(posts.find(p=>p.body.action==='approve')?.body).toMatchObject({number:42,expected_head_sha:'abc123def456'});
 await page.getByRole('button',{name:'Merge',exact:true}).click();
 await page.getByRole('button',{name:'Confirm squash into main'}).click();
 await expect(page.getByText('Merged #42.')).toBeVisible();
 expect(posts.find(p=>p.body.action==='merge')?.body).toMatchObject({number:42,method:'squash',expected_head_sha:'abc123def456'});
 await page.screenshot({path:test.info().outputPath('git-view-pulls.png')});
});

test('runs show results and full output; activity shows refusals',async({page})=>{
 await fixture(page);
 await page.getByRole('button',{name:'Runs',exact:true}).click();
 await expect(page.getByText('exit 1')).toBeVisible();
 await expect(page.getByText('passed')).toBeVisible();
 await page.getByText('go test ./...').click();
 await expect(page.getByText('--- FAIL: TestPlaces')).toBeVisible();
 await page.screenshot({path:test.info().outputPath('git-view-runs.png')});
 await page.getByRole('button',{name:'Activity',exact:true}).click();
 await expect(page.getByText(/needs a person's approval/)).toBeVisible();
 await expect(page.getByText('pr merge')).toBeVisible();
 await page.screenshot({path:test.info().outputPath('git-view-activity.png')});
});

test('branch delete asks first and offers force after a refusal',async({page})=>{
 const posts=await fixture(page);
 await page.getByRole('button',{name:'Branches',exact:true}).click();
 // The checked-out branch and origin's default branch have no delete.
 await expect(page.getByRole('button',{name:'Delete…'})).toHaveCount(2);
 await page.getByRole('button',{name:'Delete…'}).first().click();
 await page.getByRole('button',{name:'Delete',exact:true}).click();
 await expect(page.getByText(/not merged into main/)).toBeVisible();
 await page.getByRole('button',{name:'Delete…'}).first().click();
 await page.getByRole('button',{name:'Force'}).click();
 const deletes=posts.filter(p=>p.path.endsWith('/git/branches/delete')).map(p=>p.body);
 expect(deletes).toEqual([{name:'wip',where:'local',force:false},{name:'wip',where:'local',force:true}]);
 await page.getByRole('button',{name:'Tags',exact:true}).click();
 await expect(page.getByText('local only')).toBeVisible();
 await expect(page.getByText('published')).toBeVisible();
});

test('a repository without Git shows only its runs',async({page})=>{
 await fixture(page,{gitBacked:false});
 await expect(page.getByText('go test ./...')).toBeVisible();
 await expect(page.getByRole('button',{name:'Pull requests'})).toHaveCount(0);
});
