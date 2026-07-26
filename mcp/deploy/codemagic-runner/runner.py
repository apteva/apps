#!/usr/bin/env python3

import base64
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
import zipfile


class RunnerError(RuntimeError):
    pass


def required_env(name):
    value = os.environ.get(name, "").strip()
    if not value:
        raise RunnerError(f"{name} is required")
    return value


def decode_json_env(name):
    encoded = os.environ.get(name, "").strip()
    if not encoded:
        return {}
    try:
        value = json.loads(base64.b64decode(encoded, validate=True))
    except (ValueError, json.JSONDecodeError) as exc:
        raise RunnerError(f"{name} is not valid base64 JSON") from exc
    if not isinstance(value, dict):
        raise RunnerError(f"{name} must decode to a JSON object")
    return value


def download_source(destination):
    source_url = required_env("APTEVA_SOURCE_URL")
    expected_sha = required_env("APTEVA_SOURCE_SHA256").lower()
    try:
        expected_size = int(required_env("APTEVA_SOURCE_SIZE"))
    except ValueError as exc:
        raise RunnerError("APTEVA_SOURCE_SIZE must be an integer") from exc
    if expected_size < 0:
        raise RunnerError("APTEVA_SOURCE_SIZE must not be negative")

    request = urllib.request.Request(
        source_url,
        headers={"User-Agent": "Apteva-Codemagic-Runner/1"},
    )
    digest = hashlib.sha256()
    written = 0
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            with destination.open("wb") as output:
                while True:
                    chunk = response.read(1024 * 1024)
                    if not chunk:
                        break
                    written += len(chunk)
                    if written > expected_size:
                        raise RunnerError("source capsule exceeds declared size")
                    digest.update(chunk)
                    output.write(chunk)
    except RunnerError:
        raise
    except urllib.error.HTTPError as exc:
        raise RunnerError(f"source download failed with HTTP {exc.code}") from None
    except urllib.error.URLError:
        raise RunnerError("source download failed") from None

    actual_sha = digest.hexdigest()
    if written != expected_size:
        raise RunnerError(
            f"source capsule size mismatch: got {written}, expected {expected_size}"
        )
    if actual_sha != expected_sha:
        raise RunnerError("source capsule SHA-256 mismatch")
    os.environ.pop("APTEVA_SOURCE_URL", None)
    print(f"Verified source capsule: {written} bytes, sha256={actual_sha}")


def safe_extract(archive_path, destination):
    destination = destination.resolve()
    with zipfile.ZipFile(archive_path) as archive:
        for entry in archive.infolist():
            posix_path = PurePosixPath(entry.filename)
            if (
                not entry.filename
                or posix_path.is_absolute()
                or ".." in posix_path.parts
                or "\x00" in entry.filename
            ):
                raise RunnerError(f"unsafe source archive entry: {entry.filename!r}")
            mode = entry.external_attr >> 16
            if stat.S_ISLNK(mode):
                raise RunnerError(f"source archive symlinks are not allowed: {entry.filename}")
            target = (destination / Path(*posix_path.parts)).resolve()
            if target != destination and destination not in target.parents:
                raise RunnerError(f"source archive entry escapes root: {entry.filename!r}")
            if entry.is_dir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            with archive.open(entry) as source, target.open("wb") as output:
                shutil.copyfileobj(source, output)
            target.chmod(0o755 if mode & stat.S_IXUSR else 0o644)


def run(command, cwd, env, shell=False):
    if shell:
        printable = command
    else:
        printable = " ".join(str(part) for part in command)
    print(f"+ {printable}")
    subprocess.run(command, cwd=cwd, env=env, shell=shell, check=True)


def find_container(source, configured, suffix):
    if configured:
        candidate = (source / configured).resolve()
        if source.resolve() not in candidate.parents:
            raise RunnerError(f"configured {suffix} path escapes the source directory")
        if not candidate.exists() or not candidate.name.endswith(suffix):
            raise RunnerError(f"configured {suffix} path does not exist: {configured}")
        return candidate
    candidates = sorted(
        path
        for path in source.rglob(f"*{suffix}")
        if "Pods" not in path.parts and ".build" not in path.parts
    )
    if not candidates:
        return None
    return candidates[0]


def xcode_container_args(workspace, project):
    if workspace:
        return ["--workspace", str(workspace)]
    if project:
        return ["--project", str(project)]
    raise RunnerError("no .xcworkspace or .xcodeproj found in source capsule")


def detect_scheme(source, workspace, project, configured, env):
    if configured:
        return configured
    command = ["xcodebuild", "-list", "-json"]
    if workspace:
        command.extend(["-workspace", str(workspace)])
        key = "workspace"
    else:
        command.extend(["-project", str(project)])
        key = "project"
    result = subprocess.run(
        command,
        cwd=source,
        env=env,
        check=True,
        text=True,
        capture_output=True,
    )
    payload = json.loads(result.stdout)
    schemes = payload.get(key, {}).get("schemes", [])
    if len(schemes) != 1:
        raise RunnerError(
            "target_config_json.scheme is required when Xcode exposes zero or multiple schemes"
        )
    return schemes[0]


def collect_ipas(source, output):
    ipas = sorted(source.rglob("*.ipa"))
    for index, ipa in enumerate(ipas):
        name = "apteva-build.ipa" if index == 0 else f"apteva-build-{index + 1}.ipa"
        shutil.copy2(ipa, output / name)
    return len(ipas)


def build_smoke(source, output, workspace, project, scheme, configuration, env):
    derived = Path("/tmp/apteva-derived")
    shutil.rmtree(derived, ignore_errors=True)
    command = ["xcodebuild"]
    if workspace:
        command.extend(["-workspace", str(workspace)])
    else:
        command.extend(["-project", str(project)])
    command.extend(
        [
            "-scheme",
            scheme,
            "-configuration",
            configuration,
            "-sdk",
            "iphonesimulator",
            "-destination",
            "generic/platform=iOS Simulator",
            "-derivedDataPath",
            str(derived),
            "CODE_SIGNING_ALLOWED=NO",
            "build",
        ]
    )
    run(command, source, env)
    apps = sorted((derived / "Build" / "Products").rglob("*.app"))
    if not apps:
        raise RunnerError("Xcode smoke build produced no .app")
    archive = output / "apteva-build-smoke.zip"
    with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as result:
        app = apps[0]
        for path in app.rglob("*"):
            if path.is_file():
                result.write(path, Path(app.name) / path.relative_to(app))
    print(f"Created unsigned smoke artifact: {archive}")


def build_signed_ipa(source, output, target, workspace, project, scheme, env):
    bundle_id = str(target.get("bundle_id", "")).strip()
    if not bundle_id:
        raise RunnerError("target_config_json.bundle_id is required for a signed IPA")
    for name in (
        "APP_STORE_CONNECT_ISSUER_ID",
        "APP_STORE_CONNECT_KEY_IDENTIFIER",
        "APP_STORE_CONNECT_PRIVATE_KEY",
    ):
        required_env(name)

    run(["keychain", "initialize"], source, env)
    run(
        [
            "app-store-connect",
            "fetch-signing-files",
            bundle_id,
            "--type",
            "IOS_APP_STORE",
            "--create",
        ],
        source,
        env,
    )
    run(["keychain", "add-certificates"], source, env)
    signing_project = project or find_container(source, "", ".xcodeproj")
    if not signing_project:
        raise RunnerError("an .xcodeproj is required to apply signing profiles")
    run(
        ["xcode-project", "use-profiles", "--project", str(signing_project)],
        source,
        env,
    )
    command = ["xcode-project", "build-ipa"]
    command.extend(xcode_container_args(workspace, project))
    command.extend(["--scheme", scheme])
    run(command, source, env)
    if collect_ipas(source, output) == 0:
        raise RunnerError("signed Xcode build produced no .ipa")


def main():
    if os.environ.get("APTEVA_SOURCE_FORMAT", "") != "zip-v1":
        raise RunnerError("unsupported APTEVA_SOURCE_FORMAT")
    target = decode_json_env("APTEVA_TARGET_CONFIG_B64")
    deployment_env = decode_json_env("APTEVA_ENV_B64")
    process_env = os.environ.copy()
    process_env.pop("APTEVA_ENV_B64", None)
    process_env.pop("APTEVA_TARGET_CONFIG_B64", None)
    for key, value in deployment_env.items():
        process_env[str(key)] = str(value)

    output = Path("/tmp/apteva-output")
    shutil.rmtree(output, ignore_errors=True)
    output.mkdir(parents=True)
    work = Path(tempfile.mkdtemp(prefix="apteva-source-", dir="/tmp"))
    archive = work / "source.zip"
    source = work / "source"
    source.mkdir()
    try:
        download_source(archive)
        process_env.pop("APTEVA_SOURCE_URL", None)
        safe_extract(archive, source)
        archive.unlink()

        if (source / "project.yml").exists() and not list(source.glob("*.xcodeproj")):
            run(["xcodegen", "generate"], source, process_env)
        if (source / "Podfile").exists():
            run(["pod", "install"], source, process_env)

        build_command = os.environ.get("APTEVA_BUILD_CMD", "").strip()
        if build_command:
            run(build_command, source, process_env, shell=True)
            if collect_ipas(source, output) == 0:
                raise RunnerError("APTEVA_BUILD_CMD produced no .ipa")
            return

        workspace = find_container(
            source, str(target.get("workspace_path", "")).strip(), ".xcworkspace"
        )
        project = find_container(
            source, str(target.get("project_path", "")).strip(), ".xcodeproj"
        )
        if not workspace and not project:
            raise RunnerError("source capsule contains no Xcode workspace or project")
        scheme = detect_scheme(
            source,
            workspace,
            project,
            str(target.get("scheme", "")).strip(),
            process_env,
        )
        configuration = str(target.get("configuration", "Release")).strip() or "Release"
        if target.get("smoke_only") is True:
            build_smoke(
                source, output, workspace, project, scheme, configuration, process_env
            )
        else:
            build_signed_ipa(
                source, output, target, workspace, project, scheme, process_env
            )
    finally:
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    try:
        main()
    except (RunnerError, subprocess.CalledProcessError) as exc:
        print(f"Apteva runner failed: {exc}", file=sys.stderr)
        sys.exit(1)
