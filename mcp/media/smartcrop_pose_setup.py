"""Provision a versioned isolated runtime; never modify system Python packages."""
import sys,os,subprocess,pathlib,fcntl,hashlib,urllib.request,platform,zipfile,io,time
root=pathlib.Path(sys.argv[1]);root.mkdir(parents=True,exist_ok=True)
os.environ['UV_PYTHON_INSTALL_DIR']=str(root/'python')
os.environ['UV_CACHE_DIR']=str(root/'uv-cache')
hybrid=len(sys.argv)>2 and sys.argv[2]=='hybrid'
def prepare_recovery():
    if not hybrid:return
    # Recovery is optional: provisioning trouble must not disable Full.
    try:
        assets=[('yolo11n-pose.onnx','eb9543a3f625fc19e7d6134cf33e801542a81b34a15aaa9bdc25cdd1ec741309','https://raw.githubusercontent.com/apteva/apps/media/v0.14.26/mcp/media/smartcrop_model/yolo11n-pose.onnx',None),
          ('rtmpose-m-halpe26.onnx','26f3a19e61304a600dfb82d1001d41d24343b89fc70a33ffc84657e0b0bf2ecf','https://download.openmmlab.com/mmpose/v1/projects/rtmposev1/onnx_sdk/rtmpose-m_simcc-body7_pt-body7-halpe26_700e-256x192-4d3e73dd_20230605.zip','55b81170e236040b59fc792ad0a8315301ac4c079a3bdb1095d838aad3088d18')]
        for name,digest,url,archive_digest in assets:
            dest=root/name
            if dest.exists() and hashlib.sha256(dest.read_bytes()).hexdigest()==digest:continue
            with urllib.request.urlopen(url,timeout=30) as response:data=response.read(64*1024*1024+1)
            if len(data)>64*1024*1024:raise RuntimeError('recovery_model_size_exceeded')
            if archive_digest:
                if hashlib.sha256(data).hexdigest()!=archive_digest:raise RuntimeError('recovery_archive_hash_mismatch')
                with zipfile.ZipFile(io.BytesIO(data)) as archive:
                    members=[m for m in archive.infolist() if m.filename.endswith('/end2end.onnx')]
                    if len(members)!=1 or members[0].file_size>64*1024*1024:raise RuntimeError('recovery_archive_invalid')
                    data=archive.read(members[0])
            if hashlib.sha256(data).hexdigest()!=digest:raise RuntimeError('recovery_model_hash_mismatch')
            temp=root/(name+'.tmp');temp.write_bytes(data);temp.replace(dest)
    except MemoryError:raise
    except Exception:
        print('POSE_RECOVERY_SETUP_UNAVAILABLE',file=sys.stderr)
# Cross-process locking also protects concurrent sidecars on one host.
with open(root/'setup.lock','w') as lock:
    deadline=time.monotonic()+240
    while True:
        try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB);break
        except BlockingIOError:
            if time.monotonic()>deadline:raise RuntimeError('pose_setup_lock_timeout')
            time.sleep(.2)
    model=root/'model.task'
    expected='4eaa5eb7a98365221087693fcc286334cf0858e2eb6e15b506aa4a7ecdcec4ad'
    if not model.exists() or hashlib.sha256(model.read_bytes()).hexdigest()!=expected:
        with urllib.request.urlopen('https://storage.googleapis.com/mediapipe-models/pose_landmarker/pose_landmarker_full/float16/latest/pose_landmarker_full.task',timeout=45) as response:data=response.read(10*1024*1024)
        if hashlib.sha256(data).hexdigest()!=expected:raise RuntimeError('pose_model_hash_mismatch')
        temp=root/'model.task.tmp';temp.write_bytes(data);temp.replace(model)
    python=root/'venv/bin/python'
    def run(args):subprocess.run([str(a) for a in args],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=240)
    if python.exists():
        check_code="import mediapipe,numpy;assert mediapipe.__version__=='0.10.21' and numpy.__version__=='1.26.4'"
        check=subprocess.run([str(python),'-c',check_code],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if check.returncode==0:
            if hybrid:
                check=subprocess.run([str(python),'-c',"import onnxruntime;assert onnxruntime.__version__=='1.22.1'"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                if check.returncode!=0 and (root/'uv').exists():
                    try:run([root/'uv','pip','install','--python',python,'onnxruntime==1.22.1'])
                    except Exception:print('POSE_RECOVERY_RUNTIME_UNAVAILABLE',file=sys.stderr)
            prepare_recovery();sys.exit(0)
    assets={
      ('Linux','x86_64'):('https://files.pythonhosted.org/packages/4c/4a/d5357825bb47ff73762d247b1a553a966fef6802e3ab829fe60934cbf339/uv-0.9.9-py3-none-manylinux_2_17_x86_64.manylinux2014_x86_64.whl','afdd00ddc25e12ed756e069090011ca55f127753e1192e51f45fa288a024f3df'),
      ('Linux','aarch64'):('https://files.pythonhosted.org/packages/15/04/b22cd0716369f63265c76ab254e98573cb65e2ee7908f5ffa90e1c2e18fc/uv-0.9.9-py3-none-manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64.whl','036e8d38f87ffbebcd478e6b61a2c4f8733f77fbdf34140b78e0f5ab238810cf'),
      ('Darwin','arm64'):('https://files.pythonhosted.org/packages/cc/47/436863f6d99cfc3e41408e1d28d07fb3d20227d5ff66f52666564a5649f5/uv-0.9.9-py3-none-macosx_11_0_arm64.whl','e8303e17b7d2a2dc65ebc4cc65cc0b2be493566b4f7421279b008ecb10adfc5f'),
      ('Darwin','x86_64'):('https://files.pythonhosted.org/packages/80/d5/d9e18da60593d8d127a435fe5451033dba2ec6d11baea06d6cbad5e2e6b0/uv-0.9.9-py3-none-macosx_10_12_x86_64.whl','7ea663b3e5e5b20a17efbc6c7f8db602abf72447d7cced0882a0dff71c2de1ef')}
    url,digest=assets[(platform.system(),platform.machine())]
    uv=root/'uv'
    if not uv.exists():
        with urllib.request.urlopen(url,timeout=45) as response:data=response.read(30*1024*1024)
        if hashlib.sha256(data).hexdigest()!=digest:raise RuntimeError('pose_uv_hash_mismatch')
        archive=zipfile.ZipFile(io.BytesIO(data));member=next(n for n in archive.namelist() if n.endswith('/scripts/uv'))
        temp=root/'uv.tmp';temp.write_bytes(archive.read(member));temp.chmod(0o700);temp.replace(uv)
    # uv downloads a managed CPython when the host's Python is incompatible.
    os.environ['UV_PYTHON_INSTALL_DIR']=str(root/'python')
    os.environ['UV_CACHE_DIR']=str(root/'uv-cache')
    run([uv,'venv','--clear','--python','3.11',root/'venv'])
    run([uv,'pip','install','--python',python,'mediapipe==0.10.21','numpy==1.26.4','opencv-contrib-python==4.11.0.86'])
    # Tasks do not need GUI OpenCV; headless avoids libGL on small servers.
    run([uv,'pip','uninstall','--python',python,'opencv-contrib-python'])
    run([uv,'pip','install','--python',python,'opencv-contrib-python-headless==4.11.0.86'])
    if hybrid:
        try:run([uv,'pip','install','--python',python,'onnxruntime==1.22.1'])
        except Exception:print('POSE_RECOVERY_RUNTIME_UNAVAILABLE',file=sys.stderr)
    run([python,'-c',"import mediapipe,numpy;assert mediapipe.__version__=='0.10.21' and numpy.__version__=='1.26.4'"])
    prepare_recovery()
