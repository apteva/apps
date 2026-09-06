#!/usr/bin/env python3
"""Opt-in real-process upgrade regression; uses only synthetic, temporary data.
python3 tests/upgrade_startup.py --old-binary PATH --old-migrations PATH --candidate-binary PATH
"""
import argparse, concurrent.futures, hashlib, json, os, pathlib, signal, socket, sqlite3, subprocess, tempfile, time, urllib.request, urllib.error

parser = argparse.ArgumentParser()
parser.add_argument('--old-binary', required=True)
parser.add_argument('--old-migrations', required=True)
parser.add_argument('--candidate-binary', required=True)
parser.add_argument('--rows', type=int, default=100000)
args = parser.parse_args()
root = pathlib.Path(__file__).resolve().parents[1]
processes = []

def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

def request(base, path='/health', body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base+path, data=data, headers={'Authorization':'Bearer regression-token','Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=10) as r: return r.status, json.load(r)
    except urllib.error.HTTPError as e: return e.code, json.load(e)

def tool(base, tool_name, **kw):
    kw['_project_id']='upgrade-test'
    status, out=request(base, '/mcp', {'jsonrpc':'2.0','id':1,'method':'tools/call','params':{'name':tool_name,'arguments':kw}})
    assert status==200 and 'error' not in out, out
    result=out['result']
    assert not result.get('isError'),result
    return json.loads(result['content'][0]['text'])

def start(binary, migrations, database, folder, label):
    p=port(); log=open(folder/(label+'.log'),'w+')
    env=dict(os.environ, APTEVA_APP_PORT=str(p), APTEVA_APP_TOKEN='regression-token', APTEVA_PROJECT_ID='upgrade-test', DB_PATH=str(database), APTEVA_MIGRATIONS_DIR=str(migrations), APTEVA_APP_CONFIG=json.dumps({'max_rows_per_table':'0','max_query_ms':'30000','max_value_bytes':'20000','max_query_bytes':'1000000'}))
    proc=subprocess.Popen([binary],env=env,cwd=root,stdout=log,stderr=log)
    processes.append((proc,log));return proc,'http://127.0.0.1:'+str(p)

def ready(proc,base):
    states=[];begin=time.monotonic()
    while time.monotonic()-begin<60:
        assert proc.poll() is None, 'process exited before readiness'
        try:
            status,body=request(base)
            if status==200:return time.monotonic()-begin,states
            assert status==503,body
            states.append(body)
        except (ConnectionError,urllib.error.URLError):pass
        time.sleep(.01)
    raise AssertionError('startup exceeded 60 seconds')

def stop(proc):
    proc.send_signal(signal.SIGTERM)
    try:proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill();proc.wait();raise AssertionError('SIGTERM did not complete in 5 seconds')

try:
    with tempfile.TemporaryDirectory(prefix='tables-upgrade-') as tmp:
        folder=pathlib.Path(tmp);dbfile=folder/'tables.db'
        old,base=start(args.old_binary,args.old_migrations,dbfile,folder,'old')
        ready(old,base)
        for i in range(55):
            tool(base,'tables_create',name=f'legacy{i:02}',columns=[{'name':'key','type':'text'},{'name':'at','type':'datetime'},{'name':'payload','type':'json'}])
        connection=sqlite3.connect(dbfile,timeout=30)
        tables=connection.execute('SELECT id,physical_name FROM tables_meta ORDER BY id').fetchall()
        main_id,physical=tables[0]
        payload=json.dumps({'transcript':'x'*5700,'number':9007199254740993})
        connection.executemany(f'INSERT INTO "{physical}"(key,at,payload) VALUES(?,?,?)',((f'call-{i}','2026-01-01T00:00:00.1Z',payload) for i in range(args.rows)))
        connection.execute('UPDATE tables_meta SET row_count=? WHERE id=?',(args.rows,main_id))
        for ident,table in tables[1:]:
            connection.execute(f'INSERT INTO "{table}"(key,at,payload) VALUES(?,?,?)',('small','2026-01-01T00:00:00Z','{}'))
            connection.execute('UPDATE tables_meta SET row_count=1 WHERE id=?',(ident,))
        connection.execute(f'CREATE INDEX regression_calls_key ON "{physical}"(key)')
        connection.execute(f'CREATE INDEX regression_calls_at ON "{physical}"(at)')
        connection.commit();connection.execute('PRAGMA wal_checkpoint(TRUNCATE)')
        size=dbfile.stat().st_size
        assert args.rows<100000 or size>=569000000,size
        roots=connection.execute("SELECT name,rootpage,sql FROM sqlite_master WHERE type='index' OR name=?",(physical,)).fetchall()
        def digest():
            result=hashlib.sha256()
            for row in connection.execute(f'SELECT id,created_at,at,payload FROM "{physical}" ORDER BY id'):
                result.update(str(row).encode())
            return result.hexdigest()
        before=digest()
        # Model the actual failed 0.1.16 attempt: migration 005 already committed.
        connection.executescript((root/'migrations/005_storage_version.sql').read_text())
        connection.execute('INSERT INTO _migrations(filename) VALUES(?)',('005_storage_version.sql',));connection.commit()
        assert connection.execute('SELECT count(*) FROM tables_meta WHERE storage_version=0').fetchone()[0]==55
        # Hold a writer while candidate initializes, then cancel it. The old
        # process must still answer reads; cancellation must not need SIGKILL.
        connection.execute('BEGIN IMMEDIATE')
        canceled,cancel_base=start(args.candidate_binary,root/'migrations',dbfile,folder,'canceled')
        deadline=time.monotonic()+5
        while True:
            try:
                status,state=request(cancel_base)
                if status==503:break
            except (ConnectionError,urllib.error.URLError):pass
            assert time.monotonic()<deadline
            time.sleep(.01)
        assert state['status']=='initializing',state
        assert request(cancel_base,'/tables')[0]==503
        assert request(base)[0]==200
        cancel_start=time.monotonic();stop(canceled);cancel_time=time.monotonic()-cancel_start
        connection.rollback()
        assert digest()==before,'cancellation modified legacy rows'
        # Continuous real 0.1.14 writes overlap a successful candidate startup.
        def activity():
            for i in range(30):
                tool(base,'rows_update',table='legacy01',id=1,fields={'key':f'old-write-{i}'})
                assert tool(base,'rows_get',table='legacy01',id=1)['row']['key']==f'old-write-{i}'
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            work=pool.submit(activity)
            candidate,newbase=start(args.candidate_binary,root/'migrations',dbfile,folder,'candidate')
            elapsed,states=ready(candidate,newbase);work.result()
        assert connection.execute('SELECT count(*) FROM tables_meta WHERE storage_version=1 AND legacy_storage=1').fetchone()[0]==55
        assert digest()==before,'successful migration rewrote legacy rows'
        after_roots=connection.execute("SELECT name,rootpage,sql FROM sqlite_master WHERE type='index' OR name=?",(physical,)).fetchall()
        # ALTER changes table DDL, but preserves table root and every old index.
        for name,page,ddl in roots:
            matching=next(x for x in after_roots if x[0]==name)
            assert page==matching[1],(name,page,matching)
            if name!=physical:assert ddl==matching[2],name
        started=time.monotonic()
        found=tool(newbase,'rows_search',table='legacy00',where=[{'col':'key','op':'eq','value':'call-999'}],select=['id','key'],include_total=False)
        key_ms=(time.monotonic()-started)*1000
        assert len(found['rows'])==1,found
        started=time.monotonic()
        found=tool(newbase,'rows_search',table='legacy00',where=[{'col':'at','op':'eq','value':'2026-01-01T00:00:00.100000000Z'}],select=['id','at'],limit=1,include_total=False)
        date_ms=(time.monotonic()-started)*1000
        assert found['rows'][0]['at']=='2026-01-01T00:00:00.100000000Z',found
        started=time.monotonic()
        counted=tool(newbase,'rows_count',table='legacy00',where=[{'col':'at','op':'eq','value':'2026-01-01T00:00:00.100000000Z'}])
        date_count_ms=(time.monotonic()-started)*1000
        assert counted['count']==args.rows,counted
        plan=connection.execute(f'EXPLAIN QUERY PLAN SELECT id FROM "{physical}" WHERE key=?',('call-999',)).fetchall()
        assert any('regression_calls_key' in r[3] and 'SEARCH' in r[3] for r in plan),plan
        created=tool(newbase,'rows_insert',table='legacy02',rows=[{'key':'new-write','at':'2026-01-01T00:00:00.1Z'}])['ids'][0]
        # Old binary's exact datetime equality must find new writes as well.
        found=tool(base,'rows_search',table='legacy02',where=[{'col':'at','op':'eq','value':'2026-01-01T00:00:00.1Z'}])
        assert any(r['id']==created for r in found['rows']),found
        stop(candidate)
        # Binary fallback retains old CRUD and timestamp equality after commits.
        tool(base,'rows_update',table='legacy02',id=created,fields={'key':'fallback'})
        assert tool(base,'rows_get',table='legacy02',id=created)['row']['key']=='fallback'
        tool(base,'rows_delete',table='legacy02',id=created)
        restarted,resume_base=start(args.candidate_binary,root/'migrations',dbfile,folder,'resumed')
        resume,_=ready(restarted,resume_base)
        next_id=tool(resume_base,'rows_insert',table='legacy02',rows=[{'key':'after-fallback'}])['ids'][0]
        assert next_id>created,(created,next_id)
        assert connection.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
        assert not connection.execute('PRAGMA foreign_key_check').fetchall()
        print(json.dumps({'database_bytes':size,'tables':55,'call_records':args.rows,'upgrade_seconds':round(elapsed,3),'cancel_seconds':round(cancel_time,3),'resume_seconds':round(resume,3),'key_lookup_ms':round(key_ms,2),'datetime_first_page_ms':round(date_ms,2),'datetime_full_count_ms':round(date_count_ms,2),'initializing_responses':len(states),'legacy_row_hash_preserved':True,'old_binary_CRUD_and_datetime_equality':True,'integrity_check':'ok'},indent=2))
        stop(restarted);stop(old);connection.close()
except Exception:
    for proc,log in processes:
        log.flush();log.seek(0);print(log.read()[-6000:])
    raise
finally:
    for proc,log in processes:
        if proc.poll() is None:proc.kill();proc.wait()
        log.close()
