"""Fleet's bounded, no-follow remote extractor. Only publishes a complete archive."""
import ctypes
import gzip
import os
import pathlib
import shutil
import sys
import tarfile
import tempfile
import urllib.request


def publish(source, destination):
    libc = ctypes.CDLL(None, use_errno=True)
    if sys.platform == "darwin":
        result = libc.renamex_np(os.fsencode(source), os.fsencode(destination), 4)
    else:
        result = libc.renameat2(-100, os.fsencode(source), -100, os.fsencode(destination), 1)
    if result != 0:
        raise OSError(ctypes.get_errno(), "cannot publish tenant directory")


def extract(source, destination):
    parent = os.path.dirname(destination)
    os.makedirs(parent, mode=0o700, exist_ok=True)
    if os.path.lexists(destination):
        raise ValueError("target data directory already exists")
    stage = tempfile.mkdtemp(prefix=".fleet-extract-", dir=parent)
    try:
        stream = urllib.request.urlopen(source, timeout=180) if source.startswith("https://") else open(source, "rb")
        with stream, gzip.GzipFile(fileobj=stream) as compressed:
            with tarfile.open(fileobj=compressed, mode="r|") as archive:
                total = count = 0
                for member in archive:
                    count += 1
                    total += member.size
                    if count > 2000000 or member.size < 0 or total > (1 << 40):
                        raise ValueError("tenant archive exceeds extraction limits")
                    name = pathlib.PurePosixPath(member.name)
                    if name.is_absolute() or ".." in name.parts:
                        raise ValueError("unsafe archive path")
                    if str(name) == ".":
                        if not member.isdir():
                            raise ValueError("invalid archive root")
                        continue
                    target = os.path.join(stage, *name.parts)
                    cursor = stage
                    for part in name.parts[:-1]:
                        cursor = os.path.join(cursor, part)
                        if os.path.islink(cursor):
                            raise ValueError("symlink ancestor in archive")
                        os.makedirs(cursor, mode=0o700, exist_ok=True)
                    if member.isdir():
                        if os.path.islink(target):
                            raise ValueError("directory conflicts with symlink")
                        os.makedirs(target, mode=(member.mode & 0o777) | 0o700, exist_ok=True)
                    elif member.isfile():
                        # Private staging, exclusive creates and rejected link ancestors
                        # prevent both archive traversal and duplicate-entry replacement.
                        fd = os.open(target, os.O_CREAT | os.O_EXCL | os.O_WRONLY, member.mode & 0o777)
                        with os.fdopen(fd, "wb") as out, archive.extractfile(member) as payload:
                            shutil.copyfileobj(payload, out, 1 << 20)
                    elif member.issym():
                        link = member.linkname
                        if os.path.isabs(link) or os.path.normpath(os.path.join(str(name.parent), link)).split('/')[0] == "..":
                            raise ValueError("unsafe symlink target")
                        os.symlink(link, target)
                    else:
                        raise ValueError("unsupported archive entry")
            # Validate gzip checksum/trailer even after tar's end marker.
            while compressed.read(1 << 20):
                pass
        publish(stage, destination)
    finally:
        if os.path.lexists(stage):
            shutil.rmtree(stage)


if __name__ == "__main__":
    if sys.argv[1] == "--publish":
        publish(sys.argv[2], sys.argv[3])
    else:
        extract(sys.argv[1], sys.argv[2])
