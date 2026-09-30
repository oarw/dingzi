"""Exercise the CLI in disposable Git repositories, without touching project sources."""

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest


CHECKER = pathlib.Path(__file__).resolve().parents[1] / "scripts/check-structure.py"


class StructureTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="dingzi-structure-")
        self.addCleanup(temporary.cleanup)
        self.root = pathlib.Path(temporary.name)
        subprocess.run(["git", "init", "-q"], cwd=self.root, check=True)
        self.baseline({})

    def write(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content.encode("utf-8"))

    def baseline(self, entries):
        self.write(".github/structure-baseline.json", json.dumps(entries))

    def run_check(self, expected, message="Structure OK"):
        result = subprocess.run(
            [sys.executable, str(CHECKER)], cwd=self.root, capture_output=True, text=True
        )
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        self.assertIn(message, result.stdout)

    def test_boundaries_and_newline_normalization(self):
        for newline in ("\n", "\r\n"):
            with self.subTest(newline=repr(newline)):
                self.write("internal/limit.go", ("x" + newline) * 500)
                self.write("internal/limit_test.go", ("x" + newline) * 700)
                # Exactly 24 KiB after normalization, even with CRLF on disk.
                self.write("web.js", "x" * (24 * 1024 - 1) + newline)
                self.run_check(0)

    def test_untracked_growth_and_utf8_size(self):
        self.write("new.go", "x\n" * 501)
        self.run_check(1, "new.go: 501 lines exceeds 500")
        self.write("new.go", "x\n")
        self.write("web.js", "钉" * 8193)
        self.run_check(1, "web.js: 24579 bytes exceeds 24576")

    def test_baseline_must_ratchet_down(self):
        self.baseline({"legacy.go": {"lines": 510, "reason": "Split before extending this feature"}})
        self.write("legacy.go", "x\n" * 510)
        self.run_check(0)
        self.write("legacy.go", "x\n" * 511)
        self.run_check(1, "511 lines exceeds 510")
        self.write("legacy.go", "x\n" * 509)
        self.run_check(1, "lower lines baseline from 510 to 509")
        self.write("legacy.go", "x\n" * 500)
        self.run_check(1, "remove obsolete lines baseline")
        (self.root / "legacy.go").unlink()
        self.run_check(1, "remove baseline for missing or excluded source")

    def test_scope_and_tracked_ignored_files(self):
        self.write(".gitignore", "/scratch/\n")
        self.write("scratch/local.go", "x\n" * 800)
        self.write("internal/server/web/vendor/xterm.js", "x\n" * 800)
        self.write("notes.md", "x\n" * 800)
        self.run_check(0)
        subprocess.run(["git", "add", "-f", "scratch/local.go"], cwd=self.root, check=True)
        self.run_check(1, "scratch/local.go: 800 lines exceeds 500")


if __name__ == "__main__":
    unittest.main()
