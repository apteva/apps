#!/usr/bin/env python3
"""Black-box upgrade/crash test; synthetic data only, never opens an existing DB.

python3 scripts/test-migration-upgrade.py --binary /tmp/functions \
  --target-bytes 9420000000 --hold-seconds 65

Use --baseline-binary to additionally check 1.8.0's duplicate-column failure.
A temporary database is removed on exit; stdout contains the measured results.
"""
import argparse, json, os, pathlib, signal, socket, sqlite3, subprocess, shutil, tempfile, time, urllib.request, urllib.error

args_parser = argparse.ArgumentParser()
args_parser.add_argument('--binary', required=True)
args_parser.add_argument('--baseline-binary')
args_parser.add_argument('--target-bytes', type=int, default=80_000_000)
args_parser.add_argument('--hold-seconds', type=float, default=0)
args_parser.add_argument('--directory')
args_parser.add_argument('--runtime', choices=['node','go'], default='node')
args = args_parser.parse_args()
root = pathlib.Path(__file__).resolve().parents[1]
processes = []

def request(port, path='/health', body=None):
    req = urllib.request.Request(f'http://127.0.0.1:{port}{path}', data=json.dumps(body).encode() if body is not None else None,
        headers={'Authorization':'Bearer migration-fixture-token','Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=180 if path=='/functions' and body is not None else 2) as r: return r.status, json.load(r)
    except urllib.error.HTTPError as e: return e.code, json.load(e)
    except (OSError,ValueError): return None, {}

def boot(binary, directory, migrations=None):
    with socket.socket() as sock: sock.bind(('127.0.0.1',0)); port=sock.getsockname()[1]
    env={**os.environ,'DB_PATH':str(directory/'app.db'),'APTEVA_DATA_DIR':str(directory/'data'),
         'APTEVA_MIGRATIONS_DIR':str(migrations or root/'migrations'),'APTEVA_APP_PORT':str(port),
         'APTEVA_APP_TOKEN':'migration-fixture-token','APTEVA_PROJECT_ID':'test', 'APTEVA_INSTALL_ID':'10'}
    log=open(directory/f'process-{len(processes)}.log','w+')
    proc=subprocess.Popen([str(pathlib.Path(binary).resolve())],env=env,cwd=root,stdout=log,stderr=log)
    processes.append((proc,log));return proc,port,log

def stop(proc):
    if proc.poll() is None:
        proc.terminate()
        try: proc.wait(timeout=10)
        except subprocess.TimeoutExpired: proc.kill();proc.wait()

def wait_ready(proc,port,log):
    started=time.monotonic(); initializing=False
    while time.monotonic()-started<1800:
        if proc.poll() is not None:
            log.seek(0); raise AssertionError(log.read())
        code,body=request(port)
        if code==200: return time.monotonic()-started,initializing
        if code==503: initializing=True
        time.sleep(.02)
    raise AssertionError('startup exceeded manifest budget')

with tempfile.TemporaryDirectory(prefix='functions-migration-',dir=args.directory) as scratch:
    directory=pathlib.Path(scratch); db=sqlite3.connect(directory/'app.db')
    try:
        db.execute('PRAGMA journal_mode=WAL');db.execute('PRAGMA synchronous=NORMAL');db.execute('PRAGMA cache_size=-4096')
        db.execute('CREATE TABLE _migrations(filename TEXT PRIMARY KEY,applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)')
        for path in sorted((root/'migrations').glob('*.sql')):
            db.executescript(path.read_text());db.execute('INSERT INTO _migrations(filename) VALUES(?)',(path.name,));db.commit()
        db.execute("INSERT INTO functions(id,project_id,name,runtime,source_hash) VALUES(1,'test','legacy','node','hash')");db.commit()
        # Real table pages with invocation payloads and the original three indexes.
        rows=0; started=time.monotonic()
        while db.execute('PRAGMA page_count').fetchone()[0]*4096<args.target_bytes:
            db.execute("WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2000) INSERT INTO function_invocations(project_id,function_id,status,trigger_kind,response_body) SELECT 'test',1,'ok','manual',zeroblob(3072) FROM n")
            db.commit();rows+=2000
            if rows%100000==0:
                db.execute('PRAGMA wal_checkpoint(TRUNCATE)');print(json.dumps({'fixture_rows':rows,'bytes':db.execute('PRAGMA page_count').fetchone()[0]*4096}),flush=True)
        original=(root/'testdata/005_execution_identity_v1.8.0.sql').read_text()
        db.executescript(original.split('CREATE INDEX ix_inv_fn_id')[0]);db.commit();db.execute('PRAGMA wal_checkpoint(TRUNCATE)')
        identity=db.execute('SELECT instance_key FROM functions WHERE id=1').fetchone()[0]
        size=(directory/'app.db').stat().st_size
        print(json.dumps({'fixture_bytes':size,'rows':rows,'generate_seconds':time.monotonic()-started}),flush=True)
        db.close()
        if args.baseline_binary:
            legacy=directory/'legacy-migrations';shutil.copytree(root/'migrations',legacy)
            (legacy/'005_execution_identity.sql').write_text(original)
            proc,port,log=boot(args.baseline_binary,directory,legacy)
            proc.wait(timeout=60);log.seek(0);assert 'duplicate column name: instance_key' in log.read()
            print(json.dumps({'baseline_duplicate_column_failure':True}),flush=True)
        # Kill the actual app while its recovery transaction is creating the index.
        proc,port,log=boot(args.binary,directory); deadline=time.monotonic()+180
        while time.monotonic()<deadline:
            if proc.poll() is not None: log.seek(0);raise AssertionError(log.read())
            code,body=request(port)
            if body.get('phase')=='schema-upgrade' and body.get('completed')==14:
                proc.kill();proc.wait();break
            if code==200: raise AssertionError('missed index interruption window; use a larger fixture')
            time.sleep(.002)
        else: raise AssertionError('never entered index phase')
        db=sqlite3.connect(directory/'app.db')
        columns={r[1] for r in db.execute('PRAGMA table_info(function_invocations)')}
        assert 'build_ms' not in columns
        assert db.execute('SELECT count(*) FROM _migrations WHERE filename=?',('005_execution_identity.sql',)).fetchone()[0]==0
        assert not db.execute("SELECT 1 FROM sqlite_master WHERE name='ix_inv_fn_id'").fetchone()
        assert db.execute('SELECT instance_key FROM functions WHERE id=1').fetchone()[0]==identity
        print(json.dumps({'killed_during_index':True,'rollback_preserved_partial_state':True}),flush=True)
        # Hold a real SQLite writer longer than the previous 60-second budget.
        db.execute('BEGIN IMMEDIATE')
        proc,port,log=boot(args.binary,directory); started=time.monotonic()
        while time.monotonic()-started<args.hold_seconds:
            assert proc.poll() is None
            code,body=request(port)
            assert code!=200, 'ready before schema repair'
            if time.monotonic()-started>2: assert code==503 and body.get('status')=='initializing',(code,body)
            time.sleep(.1)
        db.rollback()
        ready_seconds,observed_initializing=wait_ready(proc,port,log)
        total_seconds=time.monotonic()-started
        assert db.execute('SELECT instance_key FROM functions WHERE id=1').fetchone()[0]==identity
        assert db.execute('SELECT count(*) FROM function_invocations').fetchone()[0]==rows
        assert db.execute('SELECT count(*) FROM _migrations WHERE filename=?',('005_execution_identity.sql',)).fetchone()[0]==1
        assert {'build_ms','queue_ms','cold_start_ms','execution_ms'} <= {r[1] for r in db.execute('PRAGMA table_info(function_invocations)')}
        assert db.execute("SELECT 1 FROM sqlite_master WHERE name='ix_inv_fn_id'").fetchone()
        assert db.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
        source='export default async e => ({upgraded: true, value: e.value});'
        if args.runtime=='go': source='package main\nimport "encoding/json"\nfunc Handle(event json.RawMessage, ctx *Context) (any,error) { var e map[string]any; if err:=json.Unmarshal(event,&e);err!=nil{return nil,err}; return map[string]any{"upgraded":true,"value":e["value"]},nil }'
        code,body=request(port,'/functions',{'name':'upgrade-smoke','runtime':args.runtime,'source':source})
        assert code==200,(code,body)
        code,body=request(port,'/fn/upgrade-smoke',{'value':42})
        assert code==200 and body=={'upgraded':True,'value':42},(code,body)
        stop(proc)
        proc,port,log=boot(args.binary,directory);restart_seconds,_=wait_ready(proc,port,log);stop(proc)
        assert db.execute('SELECT instance_key FROM functions WHERE id=1').fetchone()[0]==identity
        db.close()
        print(json.dumps({'result':'PASS','fixture_bytes':size,'rows':rows,'held_writer_seconds':args.hold_seconds,'ready_after_release_seconds':ready_seconds,'total_startup_seconds':total_seconds,'restart_seconds':restart_seconds,'observed_initializing':observed_initializing,'integrity':'ok','receipt_count':1,'identities_preserved':True}),flush=True)
    finally:
        for proc,log in processes: stop(proc);log.close()
        try: db.close()
        except Exception: pass
