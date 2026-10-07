"""Execute the adapter's Apple build script with hermetic tool stubs."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


def build_script(path):
    lines = path.read_text().splitlines()
    start = lines.index('      - name: Build mobile artifact') + 2
    script = []
    for line in lines[start:]:
        if line.startswith('      - name:'):
            break
        script.append(line[10:] if line.startswith('          ') else line)
    return '\n'.join(script)


class CodemagicAppleBuildTest(unittest.TestCase):
    def run_build(self, existing=None, failure='', configured=False):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            source = root / 'source'
            source.mkdir()
            (source / 'project.yml').write_text('name: Example\n')
            if existing:
                (source / existing).mkdir(parents=True)
            bin_dir = root / 'bin'
            bin_dir.mkdir()
            scripts = {
                'curl': '''[ "$FAILURE" != download ] || exit 22
while [ "$1" != --output ]; do shift; done
touch "$2"
''',
                'shasum': '''cat >/dev/null
[ "$FAILURE" != checksum ]
''',
                'unzip': '''[ "$FAILURE" != extraction ] || exit 1
while [ "$1" != -d ]; do shift; done
mkdir -p "$2/xcodegen/bin"
cat > "$2/xcodegen/bin/xcodegen" <<'SH'
#!/bin/bash
if [ "$1" = --version ]; then
 [ "$FAILURE" != binary ] || exit 1
 if [ "$FAILURE" = version ]; then echo 'Version: 1.0.0'; else echo 'Version: 2.46.0'; fi
else
 [ "$FAILURE" != generation ] || exit 1
 echo generated >> "$CALLS"
 mkdir Example.xcodeproj
fi
SH
chmod +x "$2/xcodegen/bin/xcodegen"
''',
                'keychain': 'echo signing >> "$CALLS"\n',
                'app-store-connect': 'exit 0\n',
                'xcode-project': 'exit 0\n',
                'xcodebuild': '''echo "$*" >> "$CALLS"
while [ "$#" -gt 0 ]; do
 if [ "$1" = -exportPath ]; then mkdir -p "$2"; touch "$2/app.ipa"; fi
 shift
done
''',
                'xcodegen': 'echo "un-pinned XcodeGen used" >&2; exit 99\n',
            }
            for name, body in scripts.items():
                file = bin_dir / name
                file.write_text('#!/bin/bash\nset -e\n' + body)
                file.chmod(0o755)
            env = dict(os.environ, PATH=str(bin_dir) + ':' + os.environ['PATH'],
                       CM_BUILD_DIR=str(root), APTEVA_SOURCE_DIR=str(source),
                       APTEVA_TARGET_KIND='ios', APTEVA_BUNDLE_ID='com.example',
                       APTEVA_XCODE_SCHEME='Example', APTEVA_CONFIGURATION='Release',
                       APTEVA_VERSION_NAME='1.0', APTEVA_BUILD_NUMBER='25',
                       FAILURE=failure, CALLS=str(root / 'calls'), APTEVA_BUILD_CMD='',
                       APTEVA_XCODE_PROJECT='', APTEVA_XCODE_WORKSPACE='')
            if configured:
                env['APTEVA_XCODE_PROJECT' if existing.endswith('xcodeproj') else 'APTEVA_XCODE_WORKSPACE'] = existing
            result = subprocess.run(['/bin/bash'], input=build_script(ROOT / 'codemagic.yaml'),
                                    text=True, capture_output=True, env=env)
            calls = (root / 'calls').read_text() if (root / 'calls').exists() else ''
            return result, calls

    def test_installs_pinned_generator_before_signing(self):
        result, calls = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Version: 2.46.0', result.stdout)
        self.assertLess(calls.index('generated'), calls.index('signing'))
        self.assertIn('-project ./Example.xcodeproj', calls)

    def test_installation_failures_are_clear_and_stop_before_signing(self):
        for failure in ('download', 'checksum', 'extraction', 'binary', 'version'):
            with self.subTest(failure=failure):
                result, calls = self.run_build(failure=failure)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('installation failed', result.stderr.lower())
                self.assertNotIn('signing', calls)

    def test_generation_failure_stops_before_signing(self):
        result, calls = self.run_build(failure='generation')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('failed to generate project.yml', result.stderr)
        self.assertNotIn('signing', calls)

    def test_existing_projects_and_workspaces_skip_installation(self):
        for existing, configured in (('Example.xcodeproj', False), ('Example.xcworkspace', False),
                                     ('nested/Example.xcodeproj', False), ('deep/nested/Example.xcworkspace', True)):
            with self.subTest(existing=existing, configured=configured):
                result, calls = self.run_build(existing, 'download', configured)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn('Installing XcodeGen', result.stdout)
                self.assertNotIn('generated', calls)
                self.assertIn('-workspace' if existing.endswith('xcworkspace') else '-project', calls)

    def test_all_workflow_scripts_parse_in_macos_bash(self):
        # The runner defaults to macOS Bash 3; syntax-check each YAML block.
        lines = (ROOT / 'codemagic.yaml').read_text().splitlines()
        blocks = []
        for idx, line in enumerate(lines):
            if line == '        script: |':
                block = []
                for child in lines[idx + 1:]:
                    if child and not child.startswith('          '):
                        break
                    block.append(child[10:])
                blocks.append('\n'.join(block))
        for script in blocks:
            result = subprocess.run(['/bin/bash', '-n'], input=script, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
