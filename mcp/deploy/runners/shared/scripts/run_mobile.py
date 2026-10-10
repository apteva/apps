"""Run Deploy's shared native contract on native CI workers."""
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys
import uuid


ROOT = Path(__file__).resolve().parents[1]


def load_environment(path, env):
    if path.exists():
        for line in path.read_text().splitlines():
            key, separator, value = line.partition('=')
            if not separator or not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', key):
                raise ValueError('invalid runner environment file entry')
            env[key] = value


def run_steps(steps, root, env):
    env_file = root / '.apteva-env'
    env_file.write_text('')
    env_file.chmod(0o600)
    env.update(APTEVA_BUILD_DIR=str(root), APTEVA_ENV_FILE=str(env_file))
    for step in steps:
        print('\n=== ' + step['name'] + ' ===', flush=True)
        script = step['script'].replace('stat -f \'%z\' "$capsule"',
            'python3 -c \'import os,sys; print(os.path.getsize(sys.argv[1]))\' "$capsule"')
        subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', script], cwd=root, env=env, check=True)
        load_environment(env_file, env)


def cleanup_profiles(signing_root, home=None):
    marker = signing_root / 'installed-profile-files.json'
    if not marker.exists():
        return
    home = Path.home() if home is None else home
    allowed = {home / 'Library/Developer/Xcode/UserData/Provisioning Profiles',
               home / 'Library/MobileDevice/Provisioning Profiles'}
    for value in json.loads(marker.read_text()):
        path = Path(value)
        if path.parent not in allowed or path.suffix not in {'.mobileprovision', '.provisionprofile'}:
            raise ValueError('invalid managed provisioning profile cleanup path')
        uuid.UUID(path.stem)
        path.unlink(missing_ok=True)


def main():
    root = ROOT
    env = dict(os.environ)
    if env.get('APTEVA_TARGET_KIND') in {'ios', 'macos'}:
        # These are local open-source Xcode utilities, independent of the CI provider.
        tools = root / 'apteva-tools' / 'apple-cli'
        subprocess.run([sys.executable, '-m', 'venv', str(tools)], check=True)
        subprocess.run([str(tools / 'bin' / 'pip'), 'install', '--disable-pip-version-check',
                        'codemagic-cli-tools==0.64.0'], check=True)
        env['PATH'] = str(tools / 'bin') + ':' + env['PATH']
    try:
        run_steps(json.loads((ROOT / 'scripts' / 'mobile_steps.json').read_text()), root, env)
        if env.get('AC_OUTPUT_DIR'):
            destination = Path(env['AC_OUTPUT_DIR'])
            destination.mkdir(parents=True, exist_ok=True)
            shutil.copy2(root / (env.get('APTEVA_ARTIFACT_NAME', 'apteva-build') + '.zip'), destination / (env.get('APTEVA_ARTIFACT_NAME', 'apteva-build') + '.zip'))
    finally:
        try:
            cleanup_profiles(root / 'apteva-signing')
        finally:
            shutil.rmtree(root / 'apteva-signing', ignore_errors=True)
            (root / '.apteva-env').unlink(missing_ok=True)
            for key in root.glob('AuthKey_*.p8'):
                key.unlink(missing_ok=True)
            for key in ('ANDROID_UPLOAD_KEYSTORE_BASE64', 'ANDROID_UPLOAD_STORE_PASSWORD',
                        'ANDROID_UPLOAD_KEY_PASSWORD', 'CERTIFICATE_PRIVATE_KEY',
                        'APTEVA_CERTIFICATE_PEM', 'APTEVA_PROVISIONING_PROFILE_BASE64',
                        'APP_STORE_CONNECT_PRIVATE_KEY'):
                env.pop(key, None)


if __name__ == '__main__':
    try:
        main()
    except subprocess.CalledProcessError as error:
        raise SystemExit(f'Deploy native stage failed (exit {error.returncode})')
