from __future__ import annotations

import argparse
import csv
import subprocess
import sys
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import NoReturn

import psutil


def _die(message: str) -> NoReturn:
    """Print an error message and exit with code 1."""
    print(message, file=sys.stderr)
    sys.exit(1)


def collect_tests(target: str) -> list[str]:
    """Collect pytest test node IDs from the target path or node ID."""
    cmd = [
        sys.executable,
        "-m",
        "pytest",
        "--no-cov",
        "--collect-only",
        "-q",
        target,
    ]
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode == 5:
        return []
    if result.returncode != 0:
        _die(f"Failed to collect tests:\n{result.stderr}")

    tests: list[str] = []
    for line in result.stdout.splitlines():
        line = line.strip()
        if not line or line.startswith("=") or " collected" in line:
            continue
        # Only keep actual test node IDs or file paths
        if "::" in line or line.endswith(".py"):
            tests.append(line)
    return tests


@dataclass
class RunResult:
    """Result of profiling a single test or file."""

    name: str
    returncode: int
    peak_mb: float
    stdout: str


def _monitor_memory(
    proc: subprocess.Popen[str],
    peak_ref: list[float],
) -> None:
    """Monitor memory usage of a process and its children."""
    try:
        psutil_proc = psutil.Process(proc.pid)
    except psutil.NoSuchProcess:
        return

    while proc.poll() is None:
        try:
            total_rss = psutil_proc.memory_info().rss
            for child in psutil_proc.children(recursive=True):
                try:
                    total_rss += child.memory_info().rss
                except psutil.NoSuchProcess:
                    pass
            peak_ref[0] = max(
                peak_ref[0],
                total_rss / (1024 * 1024),
            )
        except psutil.NoSuchProcess:
            break
        time.sleep(0.1)


def run_test(test_id: str, timeout: int) -> RunResult:
    """Run a single test or file and monitor peak memory."""
    cmd = [
        sys.executable,
        "-m",
        "pytest",
        test_id,
        "-v",
        "--no-cov",
        "--timeout=60",
        "-x",
    ]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )

    peak_ref: list[float] = [0.0]
    monitor_thread = threading.Thread(
        target=_monitor_memory,
        args=(proc, peak_ref),
    )
    monitor_thread.start()

    try:
        stdout, _ = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        stdout, _ = proc.communicate()
        monitor_thread.join()
        return RunResult(
            name=test_id,
            returncode=-1,
            peak_mb=peak_ref[0],
            stdout=stdout,
        )

    monitor_thread.join()
    return RunResult(
        name=test_id,
        returncode=proc.returncode,
        peak_mb=peak_ref[0],
        stdout=stdout,
    )


def _print_table(results: list[RunResult]) -> None:
    """Print a formatted table sorted by peak memory descending."""
    if not results:
        print("No results to display.")
        return

    sorted_results = sorted(
        results,
        key=lambda r: r.peak_mb,
        reverse=True,
    )
    name_width = max(len(r.name) for r in sorted_results)
    name_width = max(name_width, 10)
    status_width = 6
    mem_width = 12

    header = (
        f"{'Name':<{name_width}}  "
        f"{'Status':<{status_width}}  "
        f"{'Peak MB':>{mem_width}}"
    )
    print(header)
    print("-" * len(header))

    for result in sorted_results:
        status = "PASS" if result.returncode == 0 else "FAIL"
        print(
            f"{result.name:<{name_width}}  "
            f"{status:<{status_width}}  "
            f"{result.peak_mb:>{mem_width}.1f}"
        )


def _print_leaks(results: list[RunResult], threshold_mb: int) -> None:
    """Print tests that exceeded the memory threshold."""
    leaks = [r for r in results if r.peak_mb > threshold_mb]
    if not leaks:
        return

    print(f"Flagged leaks (>{threshold_mb} MB):")
    for leak in sorted(leaks, key=lambda r: r.peak_mb, reverse=True):
        status = "PASS" if leak.returncode == 0 else "FAIL"
        print(f"  {leak.name}: {leak.peak_mb:.1f} MB ({status})")
    print()


def _write_tsv(results: list[RunResult], path: Path) -> None:
    """Write results to a TSV file."""
    with path.open("w", newline="") as f:
        writer = csv.writer(f, delimiter="\t")
        writer.writerow(["name", "status", "peak_mb"])
        for result in results:
            status = "PASS" if result.returncode == 0 else "FAIL"
            writer.writerow([result.name, status, f"{result.peak_mb:.1f}"])


def _print_stdout_tail(stdout: str, num_lines: int = 20) -> None:
    """Print the last N lines of stdout."""
    lines = stdout.splitlines()
    tail = lines[-num_lines:] if len(lines) > num_lines else lines
    for line in tail:
        print(f"    {line}")


def main() -> int:
    """Run the memory profiler."""
    parser = argparse.ArgumentParser(
        description="Profile pytest tests for memory usage.",
    )
    parser.add_argument(
        "target",
        help="pytest node ID or path",
    )
    parser.add_argument(
        "--level",
        choices=["file", "test"],
        default="test",
        help="profile granularity (default: test)",
    )
    parser.add_argument(
        "--timeout",
        type=int,
        default=120,
        help="seconds per subprocess (default: 120)",
    )
    parser.add_argument(
        "--threshold-mb",
        type=int,
        default=200,
        help="memory threshold for leak flagging (default: 200)",
    )
    parser.add_argument(
        "--output",
        type=Path,
        default=None,
        help="TSV output file",
    )
    parser.add_argument(
        "-v",
        "--verbose",
        action="store_true",
        help="print stdout tail on failures",
    )
    args = parser.parse_args()

    print(f"Collecting tests from {args.target}...")
    tests = collect_tests(args.target)

    if not tests:
        print("No tests found.", file=sys.stderr)
        return 1

    if args.level == "file":
        files: dict[str, list[str]] = {}
        for test in tests:
            file_path = test.split("::")[0]
            files.setdefault(file_path, []).append(test)
        items = sorted(files.keys())
    else:
        items = tests

    results: list[RunResult] = []
    total = len(items)

    for idx, item in enumerate(items, 1):
        print(f"[{idx}/{total}] Running {item}...")
        result = run_test(item, args.timeout)
        results.append(result)

        status = "PASS" if result.returncode == 0 else "FAIL"
        print(f"  -> {status}, peak: {result.peak_mb:.1f} MB")

        if result.returncode != 0 and args.verbose:
            print("  stdout tail:")
            _print_stdout_tail(result.stdout)

    print()
    _print_table(results)
    print()
    _print_leaks(results, args.threshold_mb)

    if args.output:
        _write_tsv(results, args.output)
        print(f"Results written to {args.output}")

    any_fail = any(r.returncode != 0 for r in results)
    any_leak = any(r.peak_mb > args.threshold_mb for r in results)
    return 1 if any_fail or any_leak else 0


if __name__ == "__main__":
    sys.exit(main())
