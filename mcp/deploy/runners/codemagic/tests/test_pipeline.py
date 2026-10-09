"""Exercise native packaging between real Deploy prepare/finalize invocations."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from test_codemagic import ROOT, build_script


def script_named(name):
    lines = (ROOT / 'codemagic.yaml').read_text().splitlines()
    start = lines.index('      - name: ' + name) + 2
    script = []
    for line in lines[start:]:
        if line and not line.startswith('          '):
            break
        script.append(line[10:])
    return '\n'.join(script)


class NativePipelineTest(unittest.TestCase):
    def run_pipeline(self, test_command=None, prepare_command=None, store=False):
        runner = os.environ.get('TEST_DEPLOY_PIPELINE_RUNNER')
        if not runner:
            self.skipTest('Set TEST_DEPLOY_PIPELINE_RUNNER to the compiled Deploy binary')
        module_spec = importlib.util.spec_from_file_location('pipeline_prepare', ROOT / 'scripts/prepare_pipeline.py')
        module = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            capsule = root / 'capsule'
            (capsule / 'app').mkdir(parents=True)
            (capsule / 'dependency').mkdir()
            (capsule / 'dependency/pin').write_text('pinned')
            pipeline = {
                'prepare': [{'name': 'export', 'command': prepare_command or ['sh', '-ec', 'test "$(cat ../dependency/pin)" = pinned; mkdir -p generated/native/Example.xcodeproj'], 'outputs': ['generated/native/Example.xcodeproj']}],
                'build_directory': 'generated/native',
                'outputs': ['Example.ipa'],
                'tests': [{'name': 'signed-artifact', 'command': test_command or ['python3', '-c', 'import json,pathlib; p=pathlib.Path("Example.ipa"); assert p.exists(); assert json.load(open(".apteva-artifact.json"))["primary"] == p.name']}],
            }
            spec = {'target_kind': 'ios', 'build_subdir': 'app', 'target_config_json': json.dumps({'pipeline': pipeline})}
            bin_dir = root / 'bin'; bin_dir.mkdir()
            calls = root / 'calls'
            scripts = {
                'keychain': 'echo signing >> "$CALLS"',
                'app-store-connect': 'exit 0',
                'xcode-project': 'exit 0',
                'xcodebuild': '''echo "$PWD $*" >> "$CALLS"
while [ "$#" -gt 0 ]; do
 if [ "$1" = -exportPath ]; then mkdir -p "$2"; echo signed > "$2/original.ipa"; fi
 shift
done''',
                'xcrun': 'echo uploaded >> "$CALLS"',
            }
            for name, body in scripts.items():
                file = bin_dir / name; file.write_text('#!/bin/bash\nset -e\n' + body + '\n'); file.chmod(0o755)
            env = dict(os.environ, PATH=str(bin_dir) + ':' + os.environ['PATH'],
                       CM_BUILD_DIR=str(root), CM_ENV=str(root / 'env'), APTEVA_CAPSULE_DIR=str(capsule),
                       APTEVA_SOURCE_BUILD_SUBDIR='app', APTEVA_BUILD_SPEC_B64=base64.b64encode(json.dumps(spec).encode()).decode(),
                       APTEVA_TARGET_KIND='ios', APTEVA_BUNDLE_ID='com.example', APTEVA_XCODE_SCHEME='Example',
                       APTEVA_CONFIGURATION='Release', APTEVA_VERSION_NAME='1.0', APTEVA_BUILD_NUMBER='25',
                       APTEVA_BUILD_CMD='', APTEVA_XCODE_PROJECT='', APTEVA_XCODE_WORKSPACE='', CALLS=str(calls),
                       APTEVA_ARTIFACT_MODE='store_upload' if store else 'file',
                       APP_STORE_CONNECT_ISSUER_ID='test-issuer', APP_STORE_CONNECT_KEY_IDENTIFIER='test-key',
                       APP_STORE_CONNECT_PRIVATE_KEY='test-placeholder')
            # Test the actual bootstrap with a locally compiled release binary.
            # Network/download and toolchains are the only substitutions.
            def download(url, dest):
                shutil.copyfile(runner, dest)
            pins = {'darwin-' + {'arm64': 'arm64', 'x86_64': 'amd64'}[module.platform.machine()]: {'url': 'test://runner', 'sha256': hashlib.sha256(Path(runner).read_bytes()).hexdigest()}}
            real_read = Path.read_text
            def read(path, *args, **kwargs):
                return json.dumps(pins) if path.name == 'pipeline-runner.json' else real_read(path, *args, **kwargs)
            with patch.dict(os.environ, env, clear=True), patch.object(module, 'download', download), patch.object(Path, 'read_text', read):
                try:
                    module.main()
                except subprocess.CalledProcessError:
                    return 1, calls.read_text() if calls.exists() else '', None, False
            for line in (root / 'env').read_text().splitlines():
                key, value = line.split('=', 1); env[key] = value
            result = subprocess.run(['/bin/bash'], input=build_script(ROOT / 'codemagic.yaml'), text=True, env=env, capture_output=True)
            if result.returncode == 0:
                result = subprocess.run(['/bin/bash'], input=script_named('Publish or package result'), text=True, env=env, capture_output=True)
            manifest_path = root / 'apteva-output/.apteva-artifact.json'
            manifest = json.loads(manifest_path.read_text()) if manifest_path.exists() else None
            return result.returncode, calls.read_text() if calls.exists() else '', manifest, (root / 'apteva-build.zip').exists()

    def test_sibling_recipe_packages_declared_filename_and_evidence(self):
        code, calls, manifest, archived = self.run_pipeline()
        self.assertEqual(code, 0, calls)
        self.assertIn('/app/generated/native ', calls)
        self.assertEqual(manifest['primary'], 'Example.ipa')
        self.assertEqual(manifest['pipeline']['tests'], ['signed-artifact'])
        self.assertTrue(manifest['pipeline']['artifact_sha256'])
        self.assertTrue(archived)

    def test_prepare_failure_never_signs(self):
        code, calls, manifest, archived = self.run_pipeline(prepare_command=['false'])
        self.assertNotEqual(code, 0)
        self.assertNotIn('signing', calls)
        self.assertFalse(archived)

    def test_failed_or_mutating_tests_never_upload_or_package(self):
        for command in (['false'], ['sh', '-c', 'echo modified > Example.ipa']):
            with self.subTest(command=command):
                code, calls, manifest, archived = self.run_pipeline(test_command=command, store=True)
                self.assertNotEqual(code, 0)
                self.assertNotIn('uploaded', calls)
                self.assertNotIn('pipeline', manifest)
                self.assertFalse(archived)

    def test_source_subdirectory_rejects_escape(self):
        module_spec = importlib.util.spec_from_file_location('pipeline_prepare', ROOT / 'scripts/prepare_pipeline.py')
        module = importlib.util.module_from_spec(module_spec); module_spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as folder:
            with self.assertRaises(ValueError):
                module.capsule_app(Path(folder), '..')

class ToolchainVersionTest(unittest.TestCase):
    def module(self):
        spec = importlib.util.spec_from_file_location('pipeline_prepare', ROOT / 'scripts/prepare_pipeline.py')
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        return module

    def test_requested_runner_version_mismatch_blocks_preparation(self):
        module = self.module()
        with tempfile.TemporaryDirectory() as folder, patch.object(module.subprocess, 'check_output', return_value='Xcode 26.0\nBuild version example'):
            with self.assertRaisesRegex(ValueError, 'version mismatch'):
                module.provision({'software_versions': {'xcode': '26.1'}}, Path(folder), Path(folder))

    def test_unpinned_and_unsupported_toolchain_requests_fail_clearly(self):
        module = self.module()
        with tempfile.TemporaryDirectory() as folder:
            for versions in ({'bun': 'latest'}, {'rust': 'stable'}, {'unknown': '1.0'}):
                with self.subTest(versions=versions), self.assertRaises(ValueError):
                    module.provision({'software_versions': versions}, Path(folder), Path(folder))
