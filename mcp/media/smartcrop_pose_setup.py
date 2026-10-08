"""Provision a versioned isolated runtime; never modify system Python packages."""
import sys,os,subprocess,pathlib,fcntl,hashlib,urllib.request,platform,zipfile,io,time
root=pathlib.Path(sys.argv[1]);root.mkdir(parents=True,exist_ok=True)
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
        check=subprocess.run([str(python),'-c',"import mediapipe,numpy;assert mediapipe.__version__=='0.10.21' and numpy.__version__=='1.26.4'"],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        if check.returncode==0:sys.exit(0)
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
    run([python,'-c',"import mediapipe,numpy;assert mediapipe.__version__=='0.10.21' and numpy.__version__=='1.26.4'"])
