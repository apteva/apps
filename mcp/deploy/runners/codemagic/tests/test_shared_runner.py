"""Provider-neutral sequencing, cleanup and managed Apple identity checks."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2] / 'shared'

def module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / 'scripts' / (name + '.py'))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

class SharedRunnerTest(unittest.TestCase):
    def test_environment_flows_across_stages_and_failure_stops_packaging(self):
        runner = module('run_mobile')
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            env = dict(os.environ)
            steps = [
                {'name': 'prepare', 'script': 'echo APTEVA_NATIVE_BUILD_DIR=generated/ios >> "$APTEVA_ENV_FILE"'},
                {'name': 'native', 'script': 'test "$APTEVA_NATIVE_BUILD_DIR" = generated/ios; echo built > artifact'},
                {'name': 'tests', 'script': 'exit 23'},
                {'name': 'package', 'script': 'touch packaged.zip'},
            ]
            with self.assertRaises(subprocess.CalledProcessError) as caught:
                runner.run_steps(steps, root, env)
            self.assertEqual(caught.exception.returncode, 23)
            self.assertTrue((root / 'artifact').exists())
            self.assertFalse((root / 'packaged.zip').exists())
            self.assertEqual((root / '.apteva-env').stat().st_mode & 0o777, 0o600)

    def test_failure_cleans_signing_files_and_environment(self):
        runner = module('run_mobile')
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); (root / 'scripts').mkdir()
            (root / 'scripts/mobile_steps.json').write_text(json.dumps([{'name': 'fail', 'script': 'mkdir -p apteva-signing; echo secret > apteva-signing/key; exit 1'}]))
            with patch.object(runner, 'ROOT', root), patch.dict(os.environ, {'APTEVA_TARGET_KIND':'android'}):
                with self.assertRaises(subprocess.CalledProcessError): runner.main()
            self.assertFalse((root / 'apteva-signing').exists())
            self.assertFalse((root / '.apteva-env').exists())

    def test_appcircle_exports_only_the_named_verified_archive(self):
        runner = module('run_mobile')
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder); (root / 'scripts').mkdir()
            (root / 'scripts/mobile_steps.json').write_text(json.dumps([{'name':'package','script':'echo sealed > "$APTEVA_ARTIFACT_NAME.zip"'}]))
            destination = root / 'exports'
            with patch.object(runner, 'ROOT', root), patch.dict(os.environ, {'APTEVA_TARGET_KIND':'android','APTEVA_ARTIFACT_NAME':'named','AC_OUTPUT_DIR':str(destination)}): runner.main()
            self.assertEqual([p.name for p in destination.iterdir()], ['named.zip'])

    def test_templates_use_the_same_shared_runner_and_exact_managed_signing(self):
        steps = json.loads((ROOT / 'scripts/mobile_steps.json').read_text())
        scripts = '\n'.join(s['script'] for s in steps)
        self.assertIn('install_managed_signing.py', scripts)
        self.assertNotIn('fetch-signing-files', scripts)
        self.assertIn('${APTEVA_ARTIFACT_NAME:-apteva-build}', scripts)
        self.assertNotIn('CM_BUILD_DIR', scripts)
        self.assertIn('scripts/run_mobile.py', (ROOT.parent / 'bitrise/bitrise.yml').read_text())
        self.assertIn('scripts/run_mobile.py', (ROOT.parent / 'appcircle/README.md').read_text())

class ManagedSigningTest(unittest.TestCase):
    def test_wrong_bundle_or_certificate_never_imports_a_key(self):
        installer = module('install_managed_signing')
        certificate = b'certificate-der'
        for wrong in ('bundle','certificate'):
            with self.subTest(wrong=wrong), tempfile.TemporaryDirectory() as folder:
                contents = {'Entitlements': {'application-identifier':'TEAM.com.wrong' if wrong=='bundle' else 'TEAM.com.example'},
                            'DeveloperCertificates': [b'wrong' if wrong=='certificate' else certificate]}
                env = {'APTEVA_BUILD_DIR':folder,'CERTIFICATE_PRIVATE_KEY':'test-key','APTEVA_CERTIFICATE_PEM':'test-certificate',
                       'APTEVA_PROVISIONING_PROFILE_BASE64':base64.b64encode(b'test-profile').decode(),
                       'APTEVA_APPLE_CERT_SHA256':hashlib.sha256(certificate).hexdigest(),'APTEVA_BUNDLE_ID':'com.example'}
                with patch.dict(os.environ,env), patch.object(installer.subprocess,'check_output',side_effect=[certificate,plistlib.dumps(contents)]), patch.object(installer.subprocess,'run') as calls:
                    with self.assertRaisesRegex(ValueError,'does not match'): installer.main()
                    calls.assert_not_called()

class GeneratorProvisioningTest(unittest.TestCase):
    def test_apple_preparation_receives_xcodegen_before_recipe_execution(self):
        prepare = module('prepare_pipeline')
        with tempfile.TemporaryDirectory() as folder, patch.object(prepare, 'provision_xcodegen', return_value={'PATH':'pinned-xcodegen:/usr/bin'}) as install, patch.object(prepare,'emit'):
            env = prepare.provision({'target_kind':'ios'}, Path(folder), Path(folder))
            install.assert_called_once()
            self.assertTrue(env['PATH'].startswith('pinned-xcodegen:'))
    def test_generator_integrity_failure_blocks_preparation(self):
        prepare = module('prepare_pipeline')
        with tempfile.TemporaryDirectory() as folder:
            def download(url,path): path.write_bytes(b'wrong-release')
            with patch.object(prepare,'download',download):
                with self.assertRaisesRegex(ValueError,'checksum mismatch'):
                    prepare.provision_xcodegen({},Path(folder),{'PATH':'/usr/bin'})
