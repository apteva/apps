"""Provision the versioned Parakeet runtime; never modify system Python packages.

Usage: python3 setup.py ROOT
ROOT/venv holds pinned sherpa-onnx + numpy; ROOT/model holds the int8 model,
each file checked against a pinned SHA-256. Safe for concurrent callers.
"""
import fcntl
import hashlib
import io
import json
import os
import pathlib
import platform
import subprocess
import sys
import tarfile
import time
import urllib.request
import zipfile

root = pathlib.Path(sys.argv[1])
root.mkdir(parents=True, exist_ok=True)
MODEL_URL = "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2"
ARCHIVE_MAX = 600 * 1024 * 1024
MODEL_FILES = {
    "encoder.int8.onnx": ("1afbe69c1ffdabee94755f42e4daf1d93549131a3da11356bda84fcef0f2c479", 700 * 1024 * 1024),
    "decoder.int8.onnx": ("0222a86147299d4dee90edf95b6d13827edb42ad8b7c9cf0b2582f030937c0b5", 32 * 1024 * 1024),
    "joiner.int8.onnx": ("cbd02f3696e9944970111fb439d9c3fd365a80374565f13f2b9c7007d253fc98", 32 * 1024 * 1024),
    "tokens.txt": ("d58544679ea4bc6ac563d1f545eb7d474bd6cfa467f0a6e2c1dc1c7d37e3c35d", 1024 * 1024),
}
PACKAGES = ["sherpa-onnx==2.0.2", "sherpa-onnx-core==2.0.2", "numpy==2.2.6"]
CHECK = "import sherpa_onnx,numpy;assert sherpa_onnx.__version__=='2.0.2' and numpy.__version__=='2.2.6'"
UV = {
    ("Linux", "x86_64"): ("https://files.pythonhosted.org/packages/4c/4a/d5357825bb47ff73762d247b1a553a966fef6802e3ab829fe60934cbf339/uv-0.9.9-py3-none-manylinux_2_17_x86_64.manylinux2014_x86_64.whl", "afdd00ddc25e12ed756e069090011ca55f127753e1192e51f45fa288a024f3df"),
    ("Linux", "aarch64"): ("https://files.pythonhosted.org/packages/15/04/b22cd0716369f63265c76ab254e98573cb65e2ee7908f5ffa90e1c2e18fc/uv-0.9.9-py3-none-manylinux_2_17_aarch64.manylinux2014_aarch64.musllinux_1_1_aarch64.whl", "036e8d38f87ffbebcd478e6b61a2c4f8733f77fbdf34140b78e0f5ab238810cf"),
    ("Darwin", "arm64"): ("https://files.pythonhosted.org/packages/cc/47/436863f6d99cfc3e41408e1d28d07fb3d20227d5ff66f52666564a5649f5/uv-0.9.9-py3-none-macosx_11_0_arm64.whl", "e8303e17b7d2a2dc65ebc4cc65cc0b2be493566b4f7421279b008ecb10adfc5f"),
    ("Darwin", "x86_64"): ("https://files.pythonhosted.org/packages/80/d5/d9e18da60593d8d127a435fe5451033dba2ec6d11baea06d6cbad5e2e6b0/uv-0.9.9-py3-none-macosx_10_12_x86_64.whl", "7ea663b3e5e5b20a17efbc6c7f8db602abf72447d7cced0882a0dff71c2de1ef"),
}


def fail(code):
    print("APTEVA_PARAKEET_ERROR:" + json.dumps({"code": code}), flush=True)
    sys.exit(1)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def model_ready(model):
    """The marker records size+mtime after a full hash check, so later runs
    skip rehashing 650 MB unless a file changed."""
    try:
        marker = json.loads((model / "verified.json").read_text())
        for name, (digest, _) in MODEL_FILES.items():
            st = (model / name).stat()
            if marker.get(name) != [digest, st.st_size, int(st.st_mtime)]:
                return False
        return True
    except Exception:
        return False


def prepare_model():
    model = root / "model"
    if model_ready(model):
        return
    model.mkdir(exist_ok=True)
    archive = root / "model.tar.bz2.tmp"
    try:
        with urllib.request.urlopen(MODEL_URL, timeout=60) as response, open(archive, "wb") as out:
            total = 0
            for block in iter(lambda: response.read(1 << 20), b""):
                total += len(block)
                if total > ARCHIVE_MAX:
                    fail("parakeet_model_size_exceeded")
                out.write(block)
    except SystemExit:
        raise
    except Exception:
        archive.unlink(missing_ok=True)
        fail("parakeet_model_download_failed")
    marker = {}
    try:
        with tarfile.open(archive, "r:bz2") as tar:
            members = {pathlib.PurePosixPath(m.name).name: m for m in tar.getmembers() if m.isfile()}
            for name, (digest, limit) in MODEL_FILES.items():
                member = members.get(name)
                if member is None or member.size > limit:
                    fail("parakeet_model_archive_invalid")
                temp = model / (name + ".tmp")
                with tar.extractfile(member) as src, open(temp, "wb") as dst:
                    for block in iter(lambda: src.read(1 << 20), b""):
                        dst.write(block)
                if sha256_file(temp) != digest:
                    temp.unlink(missing_ok=True)
                    fail("parakeet_model_hash_mismatch")
                temp.replace(model / name)
                st = (model / name).stat()
                marker[name] = [digest, st.st_size, int(st.st_mtime)]
    finally:
        archive.unlink(missing_ok=True)
    (model / "verified.json").write_text(json.dumps(marker))


def run(args):
    subprocess.run([str(a) for a in args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=600)


def prepare_python():
    python = root / "venv" / "bin" / "python"
    if python.exists() and subprocess.run([str(python), "-c", CHECK], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
        return
    key = (platform.system(), platform.machine())
    if key not in UV:
        fail("parakeet_platform_unsupported")
    url, digest = UV[key]
    uv = root / "uv"
    if not uv.exists():
        with urllib.request.urlopen(url, timeout=45) as response:
            data = response.read(30 * 1024 * 1024)
        if hashlib.sha256(data).hexdigest() != digest:
            fail("parakeet_uv_hash_mismatch")
        archive = zipfile.ZipFile(io.BytesIO(data))
        member = next(n for n in archive.namelist() if n.endswith("/scripts/uv"))
        temp = root / "uv.tmp"
        temp.write_bytes(archive.read(member))
        temp.chmod(0o700)
        temp.replace(uv)
    # uv fetches a managed CPython when the host's Python is incompatible.
    os.environ["UV_PYTHON_INSTALL_DIR"] = str(root / "python")
    os.environ["UV_CACHE_DIR"] = str(root / "uv-cache")
    try:
        run([uv, "venv", "--clear", "--python", "3.12", root / "venv"])
        run([uv, "pip", "install", "--python", python, *PACKAGES])
        run([python, "-c", CHECK])
    except Exception:
        fail("parakeet_runtime_install_failed")


with open(root / "setup.lock", "w") as lock:
    deadline = time.monotonic() + 900
    while True:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except BlockingIOError:
            if time.monotonic() > deadline:
                fail("parakeet_setup_lock_timeout")
            time.sleep(0.5)
    prepare_python()
    prepare_model()
