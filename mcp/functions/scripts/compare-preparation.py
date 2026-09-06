#!/usr/bin/env python3
"""Compare real first requests after artifact loss with an older Functions binary.
Synthetic functions only. New runtime preparation time is reported separately.
"""
import argparse,json,os,pathlib,shutil,socket,subprocess,tempfile,time,urllib.request,urllib.error,statistics
p=argparse.ArgumentParser();p.add_argument('--baseline',required=True);p.add_argument('--candidate',required=True);p.add_argument('--baseline-migrations',required=True);p.add_argument('--runs',type=int,default=3);args=p.parse_args()
root=pathlib.Path(__file__).resolve().parents[1]
source='package main\nimport "encoding/json"\nfunc Handle(event json.RawMessage, ctx *Context) (any,error) { return map[string]any{"prepared":true},nil }'

def request(port,path,body=None):
 req=urllib.request.Request(f'http://127.0.0.1:{port}{path}',data=None if body is None else json.dumps(body).encode(),headers={'Content-Type':'application/json','Authorization':'Bearer preparation-benchmark'})
 with urllib.request.urlopen(req,timeout=180) as r:return json.load(r)

def boot(binary,directory,migrations):
 with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
 env={**os.environ,'DB_PATH':str(directory/'app.db'),'APTEVA_DATA_DIR':str(directory/'data'),'APTEVA_APP_PORT':str(port),'APTEVA_APP_TOKEN':'preparation-benchmark','APTEVA_PROJECT_ID':'test','APTEVA_MIGRATIONS_DIR':str(migrations)}
 log=open(directory/'app.log','a+')
 proc=subprocess.Popen([str(pathlib.Path(binary).resolve())],cwd=root,env=env,stdout=log,stderr=log)
 start=time.monotonic()
 while time.monotonic()-start<180:
  if proc.poll() is not None:log.seek(0);raise RuntimeError(log.read())
  try:request(port,'/health');return proc,port,log
  except (OSError,ValueError):time.sleep(.01)
 proc.kill();proc.wait();raise RuntimeError('startup timeout')

def stop(proc,log):
 if proc.poll() is None:
  proc.terminate()
  try:proc.wait(15)
  except subprocess.TimeoutExpired:proc.kill();proc.wait()
 log.close()

results=[]
for run in range(args.runs):
 for label in (['baseline','candidate','upgrade'] if run%2==0 else ['candidate','upgrade','baseline']):
  binary=args.baseline if label in ('baseline','upgrade') else args.candidate;migrations=pathlib.Path(args.baseline_migrations) if label in ('baseline','upgrade') else root/'migrations'
  with tempfile.TemporaryDirectory(prefix='functions-prepare-benchmark-') as tmp:
   directory=pathlib.Path(tmp);proc=None;log=None
   try:
    proc,port,log=boot(binary,directory,migrations)
    fn=request(port,'/functions',{'name':'entry','runtime':'go','source':source})['function']
    v=request(port,f'/functions/{fn["id"]}/versions')['versions'][0]
    stop(proc,log);proc=None
    # Retain the trusted stdlib cache, just as during a normal runtime upgrade.
    def writable_error(func,path,exc):os.chmod(path,0o700);func(path)
    for d,dirs,files in os.walk(v['build_dir']):os.chmod(d,0o700)
    if label!='upgrade':shutil.rmtree(v['build_dir'],onerror=writable_error)
    else:binary=args.candidate;migrations=root/'migrations'
    proc,port,log=boot(binary,directory,migrations)
    preparation_start=time.monotonic();ready=None
    if label!='baseline':
     while time.monotonic()-preparation_start<180:
      ready=request(port,f'/functions/{fn["id"]}')['function']['runtime_readiness']
      if ready['state']=='ready':break
      if ready['state']=='failed':raise RuntimeError(str(ready))
      time.sleep(.01)
     else:raise RuntimeError('preparation timeout')
    prepared_ms=(time.monotonic()-preparation_start)*1000 if label!='baseline' else 0
    start=time.monotonic();response=request(port,'/fn/entry',{});elapsed=(time.monotonic()-start)*1000
    assert response=={'prepared':True},response
    inv=request(port,f'/functions/{fn["id"]}/invocations')['invocations'][0]
    warm=[]
    for _ in range(50):
     start=time.monotonic();assert request(port,'/fn/entry',{})=={'prepared':True};warm.append((time.monotonic()-start)*1000)
    row={'warm_median_ms':statistics.median(warm),'run':run+1,'version':label,'background_preparation_ms':prepared_ms,'first_request_ms':elapsed,'invocation':{k:inv[k] for k in ['status','build_ms','queue_ms','cold_start_ms','execution_ms']},'readiness':ready}
    results.append(row);print(json.dumps(row),flush=True)
   finally:
    if proc is not None:stop(proc,log)
    for d,dirs,files in os.walk(directory):
     try:os.chmod(d,0o700)
     except OSError:pass
summary={label:statistics.median(r['first_request_ms'] for r in results if r['version']==label) for label in ['baseline','candidate','upgrade']}
summary['first_request_reduction_percent']=(1-summary['candidate']/summary['baseline'])*100
print(json.dumps({'summary':summary}),flush=True)
