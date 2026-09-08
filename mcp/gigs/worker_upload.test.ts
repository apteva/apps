import { test, expect } from 'bun:test';
import { readFileSync } from 'node:fs';

const source = readFileSync(new URL('./worker_upload.js', import.meta.url), 'utf8');
function client(fetcher: typeof fetch) {
  return new Function('fetch', 'publicWorkerURL', 'responseJSON', 'setTimeout', source + '\nreturn {uploadFile};')(
    fetcher, (p: string) => p,
    async (r: Response) => { const d = await r.json(); if (!r.ok) throw Object.assign(new Error(d.error), {status:r.status}); return d; },
    (f: Function, ms: number) => setTimeout(f, ms === 120000 ? ms : 1),
  );
}
const json = (data: any, status = 200) => Response.json(data, {status});

test('binary chunks run four at a time, retry 503, and show 100 only after commit', async () => {
  let active = 0, max = 0, statusCalls = 0, completed = false, fault = false;
  const attempts = new Map<number, number>();
  const stages: string[] = [];
  const file = new File([new Uint8Array(8 * 1024 + 7)], 'lily.mov', {type:'video/quicktime'});
  const c = client(async (path: string, options: RequestInit) => {
    if (path === '/upload/init') return json({upload_id:'test',part_size:1024,status:'uploading'});
    if (path === '/upload/status') {
      statusCalls++;
      if (statusCalls === 1) return json({status:'uploading',parts:[]});
      if (statusCalls === 2) return json({status:'finalizing'});
      completed = true; return json({status:'completed',storage_file_id:91});
    }
    if (path === '/upload/complete') return json({status:'finalizing'}, 202);
    expect(path).toStartWith('/upload/part?'); expect(options.method).toBe('PUT'); expect(options.body).toBeInstanceOf(Blob);
    const n = Number(new URL(path, 'http://test').searchParams.get('part_number'));
    attempts.set(n,(attempts.get(n)||0)+1);
    active++; max = Math.max(max,active); await Bun.sleep(5); active--;
    if (n === 2 && !fault) { fault = true; return json({error:'temporary gateway failure'},503); }
    return json({ok:true});
  });
  const id = await c.uploadFile(file,'step_1',(n: number) => { if(n === 100) expect(completed).toBe(true); }, (s: string) => stages.push(s));
  expect(id).toBe(91); expect(max).toBe(4); expect(attempts.size).toBe(9); expect(attempts.get(2)).toBe(2);
  expect(stages.some(s => s.startsWith('Finalizing'))).toBe(true);
});

test('reselecting a file skips acknowledged parts and preserves sessions on permanent errors', async () => {
  let failing = true, completed = false;
  const sent: number[] = [];
  const c = client(async (path: string, options: RequestInit) => {
    if (path === '/upload/init') return json({upload_id:'resume',part_size:1024,status:'uploading'});
    if (path === '/upload/status') return json(completed ? {status:'completed',storage_file_id:92} : {status:'uploading',parts:[{n:1,size:1024}]});
    if (path === '/upload/complete') { completed = true; return json({status:'finalizing'},202); }
    expect(path).toStartWith('/upload/part?');
    sent.push(Number(new URL(path,'http://test').searchParams.get('part_number')));
    return failing ? json({error:'specific validation detail'},400) : json({ok:true});
  });
  const file = new File([new Uint8Array(1800)],'recording.mov');
  await expect(c.uploadFile(file,'clip',()=>{})).rejects.toThrow('specific validation detail');
  failing = false;
  expect(await c.uploadFile(file,'clip',()=>{})).toBe(92);
  expect(sent).toEqual([2,2]);
});

test('expired partial session is replaced and completed init is reused', async () => {
  let begins = 0, finished = false;
  const c = client(async (path: string) => {
    if(path === '/upload/init') { begins++; return json({upload_id:begins === 1 ? 'old':'new',part_size:1024,status:finished?'completed':'uploading',storage_file_id:finished?93:0}); }
    if(path === '/upload/status') return json({status:begins === 1?'expired':finished?'completed':'uploading',storage_file_id:finished?93:0,parts:[]});
    if(path === '/upload/complete') { finished=true; return json({status:'finalizing'},202); }
    expect(path).toContain('upload_id=new'); return json({ok:true});
  });
  const file = new File([new Uint8Array(10)],'small.mov');
  expect(await c.uploadFile(file,'clip',()=>{})).toBe(93);
  expect(await c.uploadFile(file,'clip',()=>{})).toBe(93);
});
