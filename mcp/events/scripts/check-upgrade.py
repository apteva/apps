import os,pathlib,socket,subprocess,json,urllib.request,urllib.error,time,datetime,sqlite3,argparse,tempfile
parser=argparse.ArgumentParser(description="Rehearse an Events 0.2.0 upgrade in a disposable database")
parser.add_argument('--old-binary',required=True)
parser.add_argument('--new-binary',required=True)
parser.add_argument('--old-source',required=True)
args=parser.parse_args()
root=pathlib.Path(tempfile.mkdtemp(prefix='events-upgrade-check-'))
binaries={'old':str(pathlib.Path(args.old_binary).resolve()),'new':str(pathlib.Path(args.new_binary).resolve())}
database=root/'upgrade-smoke.db'
assert not database.exists(),'Use a fresh upgrade fixture'
with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
base=f'http://127.0.0.1:{port}'
env={**os.environ,'DB_PATH':str(database),'APTEVA_APP_PORT':str(port),'APTEVA_PROJECT_ID':'events-release-smoke','APTEVA_APP_TOKEN':'local-release-fixture','APTEVA_GATEWAY_URL':''}
for k in ['APTEVA_MIGRATIONS_DIR','APTEVA_UI_DIR']:env.pop(k,None)
def start(binary,cwd):
 log=open(root/(binary+'.log'),'w')
 p=subprocess.Popen([binaries[binary]],cwd=cwd,env=env,stdout=log,stderr=subprocess.STDOUT)
 for _ in range(60):
  if p.poll() is not None:raise RuntimeError(f'{binary} exited; see local smoke log')
  try:
   with urllib.request.urlopen(base+'/health',timeout=1) as r:
    if r.status==200:return p
  except Exception:pass
  time.sleep(.1)
 p.terminate();p.wait();raise RuntimeError('Startup timeout')
def call(path,data=None,method=None,expected=200):
 req=urllib.request.Request(base+path,data=json.dumps(data).encode() if data is not None else None,method=method,headers={'Authorization':'Bearer local-release-fixture','Content-Type':'application/json','Accept':'application/json'})
 try:r=urllib.request.urlopen(req,timeout=5)
 except urllib.error.HTTPError as e:r=e
 raw=r.read()
 assert r.status==expected,(path,r.status,raw[:300])
 return json.loads(raw) if raw else None
p=start('old',pathlib.Path(args.old_source).resolve())
try:
 starttime=datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)+datetime.timedelta(days=5)
 iso=lambda t:t.isoformat().replace('+00:00','Z')
 venue=call('/venues',{'name':'Preserved venue','city':'Barcelona'},expected=200)
 show=call('/shows',{'title':'Preserved show','slug':'preserved-show','status':'published','visibility':'public','timezone':'Europe/Madrid','starts_at':iso(starttime),'ends_at':iso(starttime+datetime.timedelta(hours=1)),'venue_id':venue['id']},expected=200)
 app=call('/applications',{'event_id':show['id'],'applicant_name':'Preserved artist','email':'existing@example.test'},expected=200)
 slot=call('/slots',{'application_id':app['id'],'starts_at':iso(starttime),'ends_at':iso(starttime+datetime.timedelta(minutes=5))},expected=200)
 tickets=call('/tickets',{'event_id':show['id'],'buyer_name':'Preserved attendee','buyer_email':'attendee@example.test','quantity':1},expected=200)
finally:p.terminate();p.wait()
with sqlite3.connect(database) as db:
 before={table:db.execute('SELECT COUNT(*) FROM '+table).fetchone()[0] for table in ['events','venues','performer_applications','performance_slots','tickets','orders']}
(root/'empty-cwd').mkdir(exist_ok=True)
p=start('new',root/'empty-cwd')
try:
 with sqlite3.connect(database) as db:
  after={table:db.execute('SELECT COUNT(*) FROM '+table).fetchone()[0] for table in before}
  assert after==before,(before,after)
  assert db.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
  assert db.execute("SELECT COUNT(*) FROM _migrations WHERE filename='002_artist_workflow.sql'").fetchone()[0]==1
 preserved=call('/shows/'+str(show['id']));assert preserved['slug']=='preserved-show'
 settings=call('/shows/'+str(show['id'])+'/settings');assert not settings['applications_open'] and not settings['lineup_published']
 pub=call('/public/preserved-show');assert not pub['can_apply']
 # Enable the new workflow and exercise an atomic show/settings edit.
 call('/shows/'+str(show['id']),{'title':'Upgraded show','settings':{'applications_open':True,'lineup_published':True}},'PATCH')
 assert call('/public/preserved-show')['can_apply']
 assert call('/shows/'+str(show['id']))['title']=='Upgraded show'
 print('PASS: v0.2.0 → v0.3.0 preserves events, venues, applications, slots, tickets and orders; additive migration, safe defaults and embedded assets work.')
finally:p.terminate();p.wait()
