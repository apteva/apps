import copy,json,os,shutil,socket,ssl,subprocess,tempfile,threading,time,unittest
from pathlib import Path
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from proxy_adoption import Proxy,Refused,digest,read_file,command
CADDY=os.environ.get('APTEVA_TEST_CADDY') or shutil.which('caddy')

def site(host,port,deployment='deployment-1'):
    return host+' {\n handle /.well-known/apteva-runtime-ready {\n respond "'+deployment+'" 200\n }\n'+''.join(' @surface'+str(n)+' path '+route+'\n handle @surface'+str(n)+' {\n reverse_proxy 127.0.0.1:'+str(port)+' {\n stream_close_delay 24h\n }\n }\n' for n,route in enumerate(['/browser/*','/carrier/*']))+' handle {\n respond "Not found" 404\n }\n}\n'

class FixtureProxy(Proxy):
    live=None;active=0;loads=0;fail_verify=False;fail_reload=False;lose_response=False
    def service(self):return 'service-identity'
    def require_fresh_owner(self):pass
    def listeners(self):return [80,443]
    def admin_request(self,method='GET',config=None):
        if method=='GET':return copy.deepcopy(self.live)
        self.loads+=1
        if self.fail_reload:self.fail_reload=False;raise Refused('test_reload_failed')
        self.live=copy.deepcopy(config)
        if self.lose_response:self.lose_response=False;raise SystemExit('interrupted_after_reload')
    def connections(self):return self.active
    def tls_probe(self,expected=None):
        if self.fail_verify and expected is not None:raise Refused('test_verification_failed')
        return {'verified':True,'sha256':'same-certificate'}

@unittest.skipUnless(CADDY,'Caddy required for adapter integration tests')
class AdoptionTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(dir='/private/tmp' if Path('/private/tmp').exists() else '/tmp');self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name);self.main=self.root/'Caddyfile';self.source=self.root/'voice.caddy';self.host='voice.example.test'
        self.source.write_text(self.host+' {\n header Access-Control-Allow-Origin https://obsolete.example\n reverse_proxy 127.0.0.1:55001\n}\n');self.source.chmod(0o600)
        self.main.write_text('import '+str(self.source)+'\nother.example.test {\n respond unrelated 200\n}\n');self.main.chmod(0o600)
        self.payload={'action':'inspect','root':'/var/lib/apteva-native/40/ingress','install_id':52,'host_key':'machine-a','deployment_id':'deployment-1','generation':1,'control_port':55001,'site':site(self.host,55001),'routes':[{'path':'/browser/*'},{'path':'/carrier/*'}],'request':{'proxy':{'adapter':'caddy','hostname':self.host,'service':'caddy.service','binary':os.path.realpath(CADDY),'config':str(self.main),'site_file':str(self.source),'format':'caddyfile','admin':'localhost:2019'}}}
        self.proxy=FixtureProxy(self.payload);self.proxy.live=self.proxy.adapt(str(self.main));self.original=self.source.read_bytes();self.old_live=copy.deepcopy(self.proxy.live)
    def inspect(self):self.payload['action']='inspect';return self.proxy.run()
    def adopt(self):
        plan=self.inspect();self.payload['request'].update(plan_sha256=plan['plan_sha256'],maintenance_window=True);self.payload['action']='adopt';return self.proxy.run()
    def test_inspection_is_read_only_and_sanitized(self):
        before=set(self.root.iterdir());result=self.inspect();self.assertEqual(before,set(self.root.iterdir()));self.assertEqual(self.source.read_bytes(),self.original);self.assertEqual(self.proxy.loads,0);self.assertNotIn('obsolete.example',json.dumps(result));self.assertEqual(result['declared_routes'],['/browser/*','/carrier/*'])
    def test_adoption_preserves_other_sites_tls_and_surfaces(self):
        result=self.adopt();self.assertTrue(result['verified']);self.assertEqual(self.proxy.without_host(self.old_live),self.proxy.without_host(self.proxy.live));self.assertNotIn(b'Access-Control-Allow-Origin',self.source.read_bytes());self.assertIn(b'/carrier/*',self.source.read_bytes());self.assertIn(b'/browser/*',self.source.read_bytes());self.assertIn(b'stream_close_delay 24h',self.source.read_bytes());self.assertEqual(os.stat(self.proxy.journal).st_mode&0o777,0o600)
    def test_duplicate_and_restart_do_not_reload(self):
        self.adopt();self.assertEqual(self.proxy.loads,1);self.assertTrue(self.proxy.run()['reused']);self.assertEqual(self.proxy.loads,1)
        fresh=FixtureProxy(copy.deepcopy(self.payload));fresh.live=copy.deepcopy(self.proxy.live);self.assertTrue(fresh.run()['reused']);self.assertEqual(fresh.loads,0)
    def test_active_connections_and_missing_maintenance_refused(self):
        plan=self.inspect();self.payload['request']['plan_sha256']=plan['plan_sha256'];self.payload['action']='adopt'
        with self.assertRaisesRegex(Refused,'maintenance_window'):self.proxy.run()
        self.payload['request']['maintenance_window']=True;self.proxy.active=1
        with self.assertRaisesRegex(Refused,'active_connections'):self.proxy.run()
        self.assertEqual(self.proxy.loads,0);self.assertEqual(self.source.read_bytes(),self.original)
    def test_stale_plan_and_foreign_upstream(self):
        plan=self.inspect();self.payload['request'].update(plan_sha256=plan['plan_sha256'],maintenance_window=True);self.payload['action']='adopt';self.source.write_text(self.source.read_text()+'\n# changed\n')
        with self.assertRaisesRegex(Refused,'plan_is_stale'):self.proxy.run()
        self.source.write_text(self.source.read_text().replace(':55001',':55002'));self.proxy.live=self.proxy.adapt(str(self.main))
        with self.assertRaisesRegex(Refused,'upstream_does_not_belong'):self.proxy.run()
        self.assertEqual(self.proxy.loads,0)
    def test_shared_fragment_symlink_and_manual_drift(self):
        self.source.write_text(self.source.read_text()+'third.example.test {\n respond unrelated 200\n}\n');self.proxy.live=self.proxy.adapt(str(self.main))
        with self.assertRaisesRegex(Refused,'unrelated_or_global'):self.inspect()
        self.source.write_bytes(self.original);self.proxy.live=copy.deepcopy(self.old_live);target=self.root/'actual.caddy';self.source.rename(target);self.source.symlink_to(target)
        with self.assertRaisesRegex(Refused,'symlink'):self.inspect()
        self.source.unlink();target.rename(self.source);self.adopt();self.source.write_text(self.source.read_text()+'# external edit\n');self.payload['action']='publish'
        with self.assertRaisesRegex(Refused,'modified_externally'):self.proxy.run()
    def test_failed_reload_or_https_verification_rolls_back(self):
        for fail in ('fail_verify','fail_reload'):
            with self.subTest(fail=fail):
                for name in [self.proxy.journal,self.proxy.owner_file]:
                    if Path(name).exists():Path(name).unlink()
                setattr(self.proxy,fail,True)
                with self.assertRaisesRegex(Refused,'previous_configuration_restored'):self.adopt()
                setattr(self.proxy,fail,False);self.assertEqual(self.source.read_bytes(),self.original);self.assertEqual(self.proxy.live,self.old_live);self.assertEqual(self.proxy.state()['phase'],'rolled_back')
    def test_interrupted_reload_recovers_without_reloading(self):
        self.proxy.lose_response=True
        with self.assertRaises(SystemExit):self.adopt()
        self.assertEqual(self.proxy.state()['phase'],'pending');self.assertEqual(self.proxy.loads,1);self.assertTrue(self.proxy.run()['reused']);self.assertEqual(self.proxy.loads,1);self.assertEqual(self.proxy.state()['phase'],'active')
    def test_explicit_rollback_exact_bytes_and_retry(self):
        self.adopt();self.payload['action']='rollback';self.assertTrue(self.proxy.run()['rollback_verified']);self.assertEqual(self.source.read_bytes(),self.original);self.assertEqual(self.proxy.live,self.old_live);self.assertTrue(self.proxy.run()['reused'])
    def test_upgrade_and_old_stop_fencing(self):
        self.adopt();self.payload.update(deployment_id='deployment-2',generation=2,control_port=55002,site=site(self.host,55002,'deployment-2'));self.payload['request']['plan_sha256']=self.inspect()['plan_sha256'];self.payload['action']='publish';self.assertTrue(self.proxy.run()['verified']);self.assertIn(b':55002',self.source.read_bytes());self.payload.update(deployment_id='deployment-1',generation=1,action='unpublish');self.assertTrue(self.proxy.run()['newer_deployment_preserved']);self.assertIn(b':55002',self.source.read_bytes())
    def test_json_configuration_preserves_unrelated_sites(self):
        main=self.root/'caddy.json';main.write_text(json.dumps(self.old_live));main.chmod(0o600);self.payload['request']['proxy'].update(config=str(main),site_file=str(main),format='json');self.proxy=FixtureProxy(self.payload);self.proxy.live=copy.deepcopy(self.old_live);self.adopt();self.assertEqual(self.proxy.without_host(self.old_live),self.proxy.without_host(self.proxy.live))
    def test_fresh_preparation_and_adoption_use_same_publication_primitive(self):
        self.source.write_text(self.host+' {\n respond "Not found" 404\n}\n');self.proxy.live=self.proxy.adapt(str(self.main));self.payload['action']='prepare'
        self.assertEqual(self.proxy.run()['phase'],'prepared');self.assertEqual(self.proxy.loads,0)
        plan=self.inspect();self.payload['request'].update(plan_sha256=plan['plan_sha256'],maintenance_window=True);self.payload['action']='publish'
        self.assertTrue(self.proxy.run()['verified']);self.assertIn(b'/browser/*',self.source.read_bytes())
        self.payload['action']='unpublish';self.proxy.run();self.assertNotIn(b'reverse_proxy',self.source.read_bytes())
        self.payload['action']='release';self.assertTrue(self.proxy.run()['unpublished']);self.assertTrue(self.proxy.run()['reused']);self.assertFalse(Path(self.proxy.owner_file).exists())
        self.assertTrue(Path(self.proxy.journal+'.released').exists())
    def test_explicit_current_owner_cas_allows_application_rollback(self):
        self.adopt()
        self.payload.update(deployment_id='deployment-2',generation=2,control_port=55002,site=site(self.host,55002,'deployment-2'))
        self.payload['request']['plan_sha256']=self.inspect()['plan_sha256'];self.payload['action']='publish';self.proxy.run()
        self.payload.update(deployment_id='deployment-1',generation=1,control_port=55001,site=site(self.host,55001),expected_deployment_id='deployment-2',expected_generation=2)
        self.payload['request']['plan_sha256']=self.inspect()['plan_sha256'];self.payload['action']='publish';self.assertTrue(self.proxy.run()['verified'])
        self.payload.update(deployment_id='deployment-3',generation=3,control_port=55003,site=site(self.host,55003,'deployment-3'),expected_deployment_id='deployment-2',expected_generation=2)
        self.payload['request']['plan_sha256']=self.inspect()['plan_sha256'];self.payload['action']='publish'
        with self.assertRaisesRegex(Refused,'owner_changed'):self.proxy.run()
    def test_foreign_owner_oversize_and_live_disk_conflict(self):
        Path(self.proxy.owner_file).write_text('{"install_id":999}');Path(self.proxy.owner_file).chmod(0o600)
        with self.assertRaisesRegex(Refused,'owned_by_another'):self.inspect()
        Path(self.proxy.owner_file).unlink();self.proxy.live['logging']={'logs':{}}
        with self.assertRaisesRegex(Refused,'live_proxy_differs'):self.inspect()
        self.proxy.live=copy.deepcopy(self.old_live);self.source.write_bytes(b'x'*(2*1024*1024+1))
        with self.assertRaisesRegex(Refused,'size_or_permissions'):self.inspect()



class EchoHandler(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def do_GET(self):
        if self.headers.get('Upgrade','').lower()=='websocket':
            import hashlib,base64
            accept=base64.b64encode(hashlib.sha1((self.headers['Sec-WebSocket-Key']+'258EAFA5-E914-47DA-95CA-C5AB0DC85B11').encode()).digest()).decode()
            self.send_response(101);self.send_header('Upgrade','websocket');self.send_header('Connection','Upgrade');self.send_header('Sec-WebSocket-Accept',accept);self.end_headers()
            self.server.ws_active=True
            try:
                while True:
                    raw=self.connection.recv(1024)
                    if not raw:break
                    self.connection.sendall(raw)
            finally:self.server.ws_active=False
            return
        body=self.path.encode();self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
    def do_POST(self):
        body=self.rfile.read(int(self.headers.get('Content-Length',0)))
        self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
    def log_message(self,*args):pass

class LiveProxy(Proxy):
    def service(self):return 'fixture-process-service-identity'
    def listeners(self):return [443]
    def connections(self):return int(self.echo.ws_active)
    def tls_probe(self,expected=None):
        import http.client,hashlib
        context=ssl.create_default_context(cafile=str(self.ca))
        with socket.create_connection(('127.0.0.1',self.port),timeout=4) as raw:
            with context.wrap_socket(raw,server_hostname=self.host) as tls:
                result={'verified':True,'sha256':hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest()}
                if expected is not None:
                    tls.sendall(('GET /.well-known/apteva-runtime-ready HTTP/1.1\r\nHost: '+self.host+'\r\nConnection: close\r\n\r\n').encode())
                    response=http.client.HTTPResponse(tls);response.begin()
                    if response.status!=200 or response.read(256).strip().decode()!=expected:raise Refused('published_deployment_marker_mismatch')
                return result

@unittest.skipUnless(CADDY,'Caddy required for live HTTPS/WebSocket tests')
class LiveCaddyTests(unittest.TestCase):
    def test_live_https_browser_callback_websocket_and_rollback(self):
        import http.client
        temp=tempfile.TemporaryDirectory(dir='/private/tmp' if Path('/private/tmp').exists() else '/tmp');self.addCleanup(temp.cleanup);root=Path(temp.name)
        echo=ThreadingHTTPServer(('127.0.0.1',0),EchoHandler);echo.daemon_threads=True;echo.ws_active=False
        thread=threading.Thread(target=echo.serve_forever,daemon=True);thread.start();self.addCleanup(echo.server_close);self.addCleanup(echo.shutdown)
        with socket.socket() as probe:probe.bind(('127.0.0.1',0));port=probe.getsockname()[1]
        main=root/'caddy.json';admin='unix/'+str(root/'admin.sock');host='voice.example.test'
        adapted=json.loads(command([CADDY,'adapt','--adapter','caddyfile','--config','-'],data=(host+' {\n header Access-Control-Allow-Origin https://obsolete.example\n reverse_proxy 127.0.0.1:'+str(echo.server_port)+'\n}\nother.example.test {\n respond unrelated 200\n}\n').encode()))
        adapted['admin']={'listen':admin};adapted['apps']['http']['servers']['srv0']['listen']=['127.0.0.1:'+str(port)];adapted['apps']['http']['servers']['srv0']['tls_connection_policies']=[{}];adapted['apps']['http']['servers']['srv0']['automatic_https']={'disable_redirects':True}
        adapted['apps']['tls']={'automation':{'policies':[{'subjects':[host,'other.example.test'],'issuers':[{'module':'internal'}]}]}}
        adapted['apps']['pki']={'certificate_authorities':{'local':{'install_trust':False}}}
        main.write_text(json.dumps(adapted));main.chmod(0o600);original=main.read_bytes()
        env=dict(os.environ,XDG_DATA_HOME=str(root/'data'),XDG_CONFIG_HOME=str(root/'config'))
        log=open(root/'caddy.log','wb');self.addCleanup(log.close)
        proc=subprocess.Popen([CADDY,'run','--config',str(main)],stdout=log,stderr=log,env=env)
        def stop():
            proc.terminate()
            try:proc.wait(timeout=5)
            except subprocess.TimeoutExpired:proc.kill();proc.wait()
        self.addCleanup(stop)
        ca=root/'data'/'caddy'/'pki'/'authorities'/'local'/'root.crt'
        payload={'action':'inspect','root':'/var/lib/apteva-native/40/ingress','install_id':52,'host_key':'machine-a','deployment_id':'deployment-1','generation':1,'control_port':echo.server_port,'site':site(host,echo.server_port),'routes':[{'path':'/browser/*'},{'path':'/carrier/*'}],'request':{'proxy':{'adapter':'caddy','hostname':host,'service':'caddy.service','binary':os.path.realpath(CADDY),'config':str(main),'site_file':str(main),'format':'json','admin':admin}}}
        proxy=LiveProxy(payload);proxy.echo=echo;proxy.ca=ca;proxy.port=port
        deadline=time.monotonic()+15
        while True:
            try:
                if ca.exists():before=proxy.run();break
            except (OSError,Refused):pass
            if proc.poll() is not None or time.monotonic()>deadline:self.fail((root/'caddy.log').read_text()[-4000:])
            time.sleep(.1)
        def connection():
            return ssl.create_default_context(cafile=str(ca)).wrap_socket(socket.create_connection(('127.0.0.1',port),timeout=4),server_hostname=host)
        ws=connection();ws.sendall(('GET /browser/ws HTTP/1.1\r\nHost: '+host+'\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        response=b''
        while b'\r\n\r\n' not in response:response+=ws.recv(1024)
        self.assertIn(b'101 Switching Protocols',response)
        payload['request'].update(plan_sha256=before['plan_sha256'],maintenance_window=True);payload['action']='adopt'
        with self.assertRaisesRegex(Refused,'active_connections'):proxy.run()
        ws.sendall(b'ping');self.assertEqual(ws.recv(4),b'ping');ws.close()
        deadline=time.monotonic()+4
        while echo.ws_active and time.monotonic()<deadline:time.sleep(.02)
        self.assertFalse(echo.ws_active)
        self.assertTrue(proxy.run()['verified'])
        self.assertEqual(before['tls']['sha256'],proxy.tls_probe()['sha256'])
        for path,method,body in [('/browser/page','GET',None),('/carrier/callback','POST',b'fixture-callback')]:
            sock=connection();body=body or b'';sock.sendall((method+' '+path+' HTTP/1.1\r\nHost: '+host+'\r\nContent-Length: '+str(len(body))+'\r\nConnection: close\r\n\r\n').encode()+body);resp=http.client.HTTPResponse(sock);resp.begin();self.assertEqual(resp.status,200);self.assertEqual(resp.read(),body or path.encode());self.assertIsNone(resp.getheader('Access-Control-Allow-Origin'));sock.close()
        sock=connection();sock.sendall(b'GET / HTTP/1.1\r\nHost: other.example.test\r\nConnection: close\r\n\r\n');resp=http.client.HTTPResponse(sock);resp.begin();self.assertEqual(resp.read(),b'unrelated');sock.close()
        payload['action']='rollback';self.assertTrue(proxy.run()['rollback_verified']);self.assertEqual(main.read_bytes(),original)
        self.assertEqual(before['tls']['sha256'],proxy.tls_probe()['sha256'])

if __name__=='__main__':unittest.main()
