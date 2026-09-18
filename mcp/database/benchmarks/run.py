#!/usr/bin/env python3
"""Run each adapter/scale/repetition in a fresh process, sequentially."""
import argparse
import hashlib
import json
import os
import platform
from pathlib import Path
import subprocess
import tempfile
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[1]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--rows", nargs="+", type=int, default=[10000, 100000, 1000000])
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--samples", type=int, default=200)
    parser.add_argument("--slow-samples", type=int, default=7)
    parser.add_argument("--output", type=Path, default=ROOT / "benchmarks" / "results" / datetime.now(timezone.utc).strftime("%Y-%m-%d"))
    parser.add_argument("--binary", type=Path)
    parser.add_argument("--resume", action="store_true", help="continue unattempted trials, preserving existing logs/results")
    parser.add_argument("--trials", nargs="+", help="run only these adapter-rows-runN trial names")
    args = parser.parse_args()
    valid_trials = {f"{adapter}-{count}-run{repeat}" for count in args.rows for repeat in range(1, args.repeats + 1) for adapter in ("sqlite", "pebble")}
    if args.trials and not set(args.trials) <= valid_trials:
        parser.error("--trials must name trials in the selected rows/repeats matrix")
    if (args.output / "machine.json").exists() and not args.resume:
        parser.error("result directory already contains a run; choose --output or --resume")
    args.output.mkdir(parents=True, exist_ok=True)
    env = os.environ.copy()
    env.update(GOWORK="off", CGO_ENABLED="0")
    env.setdefault("GOTOOLCHAIN", "local")
    with tempfile.TemporaryDirectory(prefix="apteva-database-bench-build-") as tmp:
        binary = args.binary or Path(tmp) / "benchmark"
        if not args.binary:
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/bench"], cwd=ROOT, env=env, check=True)
        checksum = hashlib.sha256()
        for path in sorted([ROOT / "go.mod", ROOT / "go.sum", *ROOT.glob("engine/*.go"), *ROOT.glob("cmd/bench/*.go")]):
            checksum.update(str(path.relative_to(ROOT)).encode())
            checksum.update(path.read_bytes())
        machine = {"platform": platform.platform(), "cpu_count": os.cpu_count(), "source_sha256": checksum.hexdigest(), "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "cgo_enabled": False, "argv": vars(args).copy(), "started_utc": datetime.now(timezone.utc).isoformat()}
        machine["argv"] = {k: str(v) if isinstance(v, Path) else v for k, v in machine["argv"].items()}
        if platform.system() == "Darwin":
            for key in ["machdep.cpu.brand_string", "hw.memsize", "hw.logicalcpu"]:
                machine[key] = subprocess.check_output(["sysctl", "-n", key], text=True).strip()
        metadata = args.output / "machine.json"
        if args.resume and metadata.exists():
            previous = json.loads(metadata.read_text())
            if any(previous[k] != machine[k] for k in ("source_sha256", "binary_sha256")):
                parser.error("cannot resume with different source or binary")
            for key in ("rows", "repeats", "samples", "slow_samples", "trials"):
                if previous["argv"].get(key) != machine["argv"].get(key):
                    parser.error(f"cannot resume with changed {key}")
        else:
            metadata.write_text(json.dumps(machine, indent=2) + "\n")
        failures = []
        for count in args.rows:
            for repeat in range(1, args.repeats + 1):
                adapters = ["sqlite", "pebble"] if repeat % 2 else ["pebble", "sqlite"]
                for adapter in adapters:
                    stem = f"{adapter}-{count}-run{repeat}"
                    if args.trials and stem not in args.trials:
                        continue
                    if args.resume and (args.output / (stem + ".log")).exists():
                        if not (args.output / (stem + ".json")).exists():
                            failures.append({"trial": stem, "reason": "previous attempt did not complete; see preserved log"})
                        continue
                    cmd = [str(binary), "--adapter", adapter, "--rows", str(count), "--run", str(repeat), "--samples", str(args.samples), "--slow-samples", str(args.slow_samples), "--output", str(args.output / (stem + ".json"))]
                    print(f"\nRUN {stem}", flush=True)
                    with (args.output / (stem + ".log")).open("w") as log:
                        proc = subprocess.Popen(cmd, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
                        for line in proc.stdout:
                            log.write(line)
                            log.flush()
                            print(line, end="", flush=True)
                        status = proc.wait()
                        if status:
                            failures.append({"trial": stem, "exit_code": status, "reason": "see log"})
                            print(f"Benchmark {stem} failed; preserved {log.name}; continuing", flush=True)
                    (args.output / "failures.json").write_text(json.dumps(failures, indent=2) + "\n")
        print(f"Results: {args.output}", flush=True)
        if failures:
            raise SystemExit(f"{len(failures)} trial(s) did not complete; see failures.json and logs")

if __name__ == "__main__":
    main()
