"""Provision generic recipe toolchains and invoke Deploy's pinned Go runner."""
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import urllib.request
import zipfile


def run(args, env=None):
    print('+ ' + ' '.join(args), flush=True)
    return subprocess.check_output(args, text=True, env=env).strip()


def download(url, path):
    with urllib.request.urlopen(url, timeout=120) as response, open(path, 'wb') as out:
        while True:
            data = response.read(1024 * 1024)
            if not data:
                break
            out.write(data)


def emit(values):
    with open(os.environ['CM_ENV'], 'a') as env_file:
        for key, value in values.items():
            if '\n' in str(value) or '\r' in str(value):
                raise ValueError(f'{key} contains a newline')
            env_file.write(f'{key}={value}\n')


def capsule_app(capsule, subdir):
    if Path(subdir).is_absolute() or '\\' in subdir:
        raise ValueError('source build subdirectory must be relative')
    app = (capsule / subdir).resolve(strict=True)
    app.relative_to(capsule.resolve())
    if not app.is_dir():
        raise ValueError('source build subdirectory must be a directory')
    return app


def source_files(capsule, filename):
    # Only inspect capsule inputs, avoiding dependency installs and generated trees.
    found = []
    for root, dirs, files in os.walk(capsule, followlinks=False):
        dirs[:] = [d for d in dirs if d not in {'.git', 'node_modules', 'target', 'generated'}]
        if filename in files:
            path = Path(root) / filename
            path.resolve().relative_to(capsule.resolve())
            found.append(path)
    return found


def exact_version(value, tool):
    if not re.fullmatch(r'\d+\.\d+\.\d+', value):
        raise ValueError(f'{tool} requires an exact major.minor.patch version, got {value!r}')
    return value


def provision(spec, capsule, tools):
    requested = spec.get('software_versions') or {}
    supported = {'bun', 'rust', 'rust_targets', 'xcode', 'node', 'python', 'cocoapods', 'java', 'ruby', 'flutter'}
    unknown = set(requested) - supported
    if unknown:
        raise ValueError('unsupported requested runner versions: ' + ', '.join(sorted(unknown)))
    env = dict(os.environ)
    changed = {}
    package_files = source_files(capsule, 'package.json')
    package_managers = set()
    for file in package_files:
        manager = json.loads(file.read_text()).get('packageManager', '')
        if manager.startswith('bun@'):
            package_managers.add(manager[4:].split('+')[0])
    needs_bun = bool(requested.get('bun') or package_managers or source_files(capsule, 'bun.lock') or source_files(capsule, 'bun.lockb'))
    if needs_bun:
        if len(package_managers) > 1 and not requested.get('bun'):
            raise ValueError('conflicting Bun versions in capsule; configure software_versions.bun')
        version = exact_version(requested.get('bun') or next(iter(package_managers), '1.3.13'), 'Bun')
        arch = {'arm64': 'aarch64', 'x86_64': 'x64'}[platform.machine()]
        bundle = f'bun-darwin-{arch}'
        archive = tools / 'bun.zip'
        download(f'https://github.com/oven-sh/bun/releases/download/bun-v{version}/{bundle}.zip', archive)
        with zipfile.ZipFile(archive) as z:
            binary = tools / 'bun'
            binary.write_bytes(z.read(f'{bundle}/bun'))
        binary.chmod(0o755)
        actual = run([str(binary), '--version'])
        if actual != version:
            raise ValueError(f'Bun version mismatch: expected {version}, got {actual}')
        print(f'Bun {actual}', flush=True)
        changed['PATH'] = str(tools) + ':' + env['PATH']
        env.update(changed)
    rust_files = source_files(capsule, 'rust-toolchain.toml')
    rust_versions = set()
    rust_components = set()
    rust_targets = set(filter(None, requested.get('rust_targets', '').split(',')))
    for file in rust_files:
        # Codemagic Python versions before 3.11 need only the standard toolchain
        # channel. TOML arrays use the source's rustup file during stage commands.
        match = re.search(r'^\s*channel\s*=\s*"([^"]+)"', file.read_text(), re.M)
        if not match:
            raise ValueError(f'{file.name} has no toolchain channel')
        rust_versions.add(match.group(1))
        for key, values in [('targets', rust_targets), ('components', rust_components)]:
            array = re.search(r'^\s*' + key + r'\s*=\s*(\[[^\]]*\])', file.read_text(), re.M)
            if array:
                import ast
                values.update(ast.literal_eval(array.group(1)))
    if requested.get('rust') or rust_files or source_files(capsule, 'Cargo.toml'):
        if len(rust_versions) > 1 and not requested.get('rust'):
            raise ValueError('conflicting Rust versions in capsule; configure software_versions.rust')
        version = exact_version(requested.get('rust') or next(iter(rust_versions), '1.98.1'), 'Rust')
        # An isolated toolchain keeps both the provider image and future builds unchanged.
        changed.update(CARGO_HOME=str(tools / 'cargo'), RUSTUP_HOME=str(tools / 'rustup'), RUSTUP_TOOLCHAIN=version)
        changed['PATH'] = str(tools / 'cargo' / 'bin') + ':' + env['PATH']
        env.update(changed)
        installer = tools / 'rustup-init.sh'
        download('https://sh.rustup.rs', installer)
        subprocess.run(['sh', str(installer), '-y', '--no-modify-path', '--profile', 'minimal', '--default-toolchain', version], env=env, check=True)
        if rust_components:
            subprocess.run(['rustup', 'component', 'add', '--toolchain', version] + sorted(rust_components), env=env, check=True)
        actual = run(['rustc', '--version'], env).split()[1]
        if actual != version:
            raise ValueError(f'Rust version mismatch: expected {version}, got {actual}')
        if spec['target_kind'] == 'ios':
            # Native exporters may produce device + simulator XCFrameworks.
            rust_targets.update({'aarch64-apple-ios', 'aarch64-apple-ios-sim', 'x86_64-apple-ios'})
        elif spec['target_kind'] == 'macos':
            rust_targets.update({'aarch64-apple-darwin', 'x86_64-apple-darwin'})
        if rust_targets:
            subprocess.run(['rustup', 'target', 'add', '--toolchain', version] + sorted(rust_targets), env=env, check=True)
        print(f'Rust {actual}', flush=True)
    checks = {
        'xcode': (['xcodebuild', '-version'], r'Xcode\s+(\S+)'),
        'node': (['node', '--version'], r'v(\S+)'),
        'python': (['python3', '--version'], r'Python\s+(\S+)'),
        'cocoapods': (['pod', '--version'], r'^(\S+)'),
        'java': (['java', '-version'], r'version "([^"]+)"'),
        'ruby': (['ruby', '--version'], r'ruby\s+(\S+)'),
        'flutter': (['flutter', '--version'], r'Flutter\s+(\S+)'),
    }
    for tool, (command, pattern) in checks.items():
        if tool in requested:
            output = subprocess.check_output(command, text=True, stderr=subprocess.STDOUT, env=env)
            found = re.search(pattern, output)
            if not found or found.group(1) != requested[tool]:
                raise ValueError(f'{tool} version mismatch: expected {requested[tool]}, got {output.strip()}')
            print(f'{tool} {found.group(1)}', flush=True)
    emit(changed)
    return env


def main():
    spec = json.loads(base64.b64decode(os.environ['APTEVA_BUILD_SPEC_B64'], validate=True))
    subdir = spec.get('build_subdir', '')
    if 'APTEVA_SOURCE_BUILD_SUBDIR' in os.environ and os.environ['APTEVA_SOURCE_BUILD_SUBDIR'] != subdir:
        raise ValueError('source build subdirectory differs from build spec')
    capsule = Path(os.environ['APTEVA_CAPSULE_DIR']).resolve(strict=True)
    app = capsule_app(capsule, subdir)
    pipeline = json.loads(spec.get('target_config_json') or '{}').get('pipeline')
    output = Path(os.environ['CM_BUILD_DIR']) / 'apteva-output'
    output.mkdir(exist_ok=True)
    if pipeline is None:
        ext = {'ios': 'ipa', 'macos': 'pkg', 'android': 'aab'}[spec['target_kind']]
        emit({'APTEVA_SOURCE_DIR': app, 'APTEVA_NATIVE_BUILD_DIR': app, 'APTEVA_OUTPUT_PRIMARY': 'app.' + ext})
        return
    tools = Path(os.environ['CM_BUILD_DIR']) / 'apteva-tools' / 'pipeline'
    tools.mkdir(parents=True, exist_ok=True)
    pins = json.loads((Path(__file__).parent / 'pipeline-runner.json').read_text())
    arch = {'arm64': 'arm64', 'x86_64': 'amd64'}[platform.machine()]
    pin = pins['darwin-' + arch]
    runner = tools / 'deploy-pipeline'
    download(pin['url'], runner)
    if hashlib.sha256(runner.read_bytes()).hexdigest() != pin['sha256']:
        raise ValueError('Deploy pipeline runner checksum mismatch')
    runner.chmod(0o755)
    env = provision(spec, capsule, tools)
    subprocess.run([str(runner), '--cloud-pipeline', 'prepare', '--source', str(capsule), '--artifact', str(output), '--env-file', os.environ['CM_ENV']], env=env, check=True)
    emit({'APTEVA_PIPELINE_RUNNER': runner})


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        raise SystemExit(f'Pipeline preparation failed: {error}')
