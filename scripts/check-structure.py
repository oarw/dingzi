#!/usr/bin/env python3
"""Check hand-written source size from the repository root. See MAINTAINING.md."""

import json
import pathlib
import subprocess
import sys


SOURCE_SUFFIXES = {".go", ".js", ".mjs", ".css", ".html", ".py", ".sh", ".yaml", ".yml"}
PRODUCTION = {"lines": 500, "bytes": 24 * 1024}
TEST = {"lines": 700, "bytes": 32 * 1024}
BASELINE = ".github/structure-baseline.json"
VENDOR = "internal/server/web/vendor/"


def source_paths(root):
    output = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=root
    )
    for name in sorted(set(output.decode("utf-8").split("\0")) - {""}):
        path = pathlib.PurePosixPath(name)
        if name.startswith(VENDOR):
            continue
        if path.suffix in SOURCE_SUFFIXES or path.name == "Dockerfile":
            if (root / name).is_file():
                yield name


def check(root):
    baseline = json.loads((root / BASELINE).read_text(encoding="utf-8"))
    errors = []
    seen = set()
    for name in source_paths(root):
        seen.add(name)
        # Text mode normalizes CRLF before both measurements, including byte size.
        content = (root / name).read_text(encoding="utf-8")
        size = {"lines": len(content.splitlines()), "bytes": len(content.encode("utf-8"))}
        limits = TEST if name.endswith("_test.go") or name.startswith("e2e/") else PRODUCTION
        exception = baseline.get(name, {})
        if name in baseline:
            if not exception.get("reason") or not (size.keys() & exception.keys()):
                errors.append(f"{name}: baseline needs a reason and an oversized metric")
            if unknown := exception.keys() - size.keys() - {"reason"}:
                errors.append(f"{name}: unknown baseline fields: {sorted(unknown)}")
        for metric, actual in size.items():
            normal = limits[metric]
            cap = exception.get(metric, normal)
            if actual > cap:
                errors.append(f"{name}: {actual} {metric} exceeds {cap}; split by responsibility")
            if metric in exception:
                if actual <= normal or cap <= normal:
                    errors.append(f"{name}: remove obsolete {metric} baseline (normal limit {normal})")
                elif actual < cap:
                    errors.append(f"{name}: lower {metric} baseline from {cap} to {actual}")
    for name in sorted(baseline.keys() - seen):
        errors.append(f"{name}: remove baseline for missing or excluded source")

    if errors:
        print("Structure check failed:")
        print("\n".join(f"  {error}" for error in errors))
        print(f"See MAINTAINING.md; do not raise {BASELINE} merely to pass CI.")
        return 1
    print(f"Structure OK: {len(seen)} source files, {len(baseline)} recorded legacy exceptions.")
    return 0


if __name__ == "__main__":
    sys.exit(check(pathlib.Path.cwd()))
