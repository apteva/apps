import os
from pathlib import Path
import tempfile
import unittest
import zipfile

import runner


class RunnerTests(unittest.TestCase):
    def test_safe_extract_rejects_parent_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            archive = Path(tmp) / "source.zip"
            with zipfile.ZipFile(archive, "w") as body:
                body.writestr("../escape", "bad")
            destination = Path(tmp) / "source"
            destination.mkdir()
            with self.assertRaises(runner.RunnerError):
                runner.safe_extract(archive, destination)

    def test_safe_extract_preserves_executable_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            archive = Path(tmp) / "source.zip"
            info = zipfile.ZipInfo("scripts/build.sh")
            info.external_attr = 0o755 << 16
            with zipfile.ZipFile(archive, "w") as body:
                body.writestr(info, "#!/bin/sh\n")
            destination = Path(tmp) / "source"
            destination.mkdir()
            runner.safe_extract(archive, destination)
            self.assertTrue(os.access(destination / "scripts" / "build.sh", os.X_OK))


if __name__ == "__main__":
    unittest.main()
