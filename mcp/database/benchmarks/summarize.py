#!/usr/bin/env python3
"""Summarize benchmark JSON without mixing failed requests into success latency."""
import argparse
import csv
import json
import math
from pathlib import Path
from statistics import median
from collections import defaultdict, Counter

def percentile(values, quantile):
    if not values:
        return None
    return sorted(values)[max(0, math.ceil(len(values) * quantile) - 1)]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    reports = [json.loads(p.read_text()) for p in sorted(args.directory.glob("*-run*.json"))]
    groups = defaultdict(list)
    storage = []
    for report in reports:
        storage.append({k: report[k] for k in ("adapter", "rows", "run", "live_disk_mib", "closed_disk_mib", "process_peak_rss_mib", "wall_seconds", "correctness_checks")})
        for metric in report["metrics"]:
            groups[report["adapter"], report["rows"], metric["operation"]].append(metric)
    summary = []
    for (adapter, rows, operation), metrics in sorted(groups.items()):
        good = [v for m in metrics for v in m.get("samples_ms", [])]
        bad = [v for m in metrics for v in m.get("error_samples_ms", [])]
        errors = Counter()
        for m in metrics:
            errors.update(m.get("errors") or {})
        summary.append({
            "adapter": adapter, "rows": rows, "operation": operation,
            "attempts": len(good) + len(bad), "successes": len(good),
            "p50_ms": percentile(good, .5), "p95_ms": percentile(good, .95),
            "max_ms": max(good) if good else None,
            "error_p50_ms": percentile(bad, .5), "errors": dict(errors),
            "median_successful_ops_per_second": median(m["successful_ops_per_second"] for m in metrics),
            "median_records_per_second": median(m.get("records_per_second", 0) for m in metrics),
            "median_allocated_bytes_per_attempt": median(m["allocated_bytes_per_attempt"] for m in metrics),
            "median_phase_heap_mib": median(m["heap_alloc_mib"] for m in metrics),
        })
    incomplete = [p.stem for p in sorted(args.directory.glob("*-run*.log")) if not p.with_suffix(".json").exists()]
    output = {"reports": len(reports), "attempts_without_completed_report": incomplete, "latency_method": "nearest-rank percentiles over pooled successful samples; errors separate", "throughput_method": "median of per-run phase throughput", "operations": summary, "storage": storage}
    (args.directory / "summary.json").write_text(json.dumps(output, indent=2) + "\n")
    if summary:
        with (args.directory / "operations.csv").open("w", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=list(summary[0]))
            writer.writeheader()
            writer.writerows(summary)
    print(f"Summarized {len(reports)} runs into {args.directory / 'summary.json'}")

if __name__ == "__main__":
    main()
