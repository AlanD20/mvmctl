#!/usr/bin/env python3
"""Run system tests — black-box CLI tests that exercise the Go binary.

System tests run one file at a time to avoid cross-file state pollution.

Usage:
    python scripts/run_tests.py                                              # run all system tests
    python scripts/run_tests.py --bin /path/to/mvm                           # use a specific binary
    python scripts/run_tests.py --no-mirror                                  # skip asset mirror
    python scripts/run_tests.py --domain vm                                  # run only vm domain tests
    python scripts/run_tests.py --failed-only                                # re-run only previously failed
    python scripts/run_tests.py --list                                       # list all system test files
    python scripts/run_tests.py --list --domain vm                           # list vm domain test files
    python scripts/run_tests.py --test tests/system/vm/test_vm.py            # single file

    # --pytest-extra: pass extra flags through to pytest
    python scripts/run_tests.py --pytest-extra "-x --timeout=60"

    # --domain: matches tests/system/{domain}/ directories. Each domain has
    #   its own conftest with minimal asset setup (no cross-domain pollution).
    #   Valid domains: bin, cache, cli, config, console, full_journeys, host,
    #   images, init, invariants, kernel, keys, logs, network, ssh, vm, volume,
    #   zzz_destructive

    # --bin:
    #   Default  → searches for compiled binary (./mvm, then dist/mvm)
    #   --bin X  → uses X as MVM_BINARY (path to compiled binary)

    # --failed-only: reads .reports/system-test-results.txt from the last full run
    #   and re-runs only the files that had "FAIL" status. Each line in that file
    #   is "filename: STATUS" (e.g. "test_network.py: PASS"). --failed-only
    #   filters for "filename: FAIL" entries.

    # --ci: Sets MVM_TEST_ENFORCE_NO_SUDO=1 in the environment.
"""

from __future__ import annotations

import argparse
import datetime
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

from common import (
    DEFAULT_MIRROR,
    PROJECT_ROOT,
    print_fail,
    print_info,
    print_success,
    print_warn,
)

REPO_DIR = PROJECT_ROOT
SYSTEM_TEST_DIR = REPO_DIR / "tests" / "system"

REPORTS_DIR = REPO_DIR / ".reports"
RESULTS_FILE = REPO_DIR / ".reports" / "system-test-results-latest.txt"
JUNIT_DIR = REPO_DIR / ".reports" / "junit"


def _fmt_duration(seconds: int) -> str:
    if seconds < 60:
        return f"{seconds}s"
    return f"{seconds // 60}m{seconds % 60:02d}s"


def _find_default_binary() -> str:
    """Find a compiled Go binary: prefer ./mvm, then dist/mvm."""
    for candidate in [REPO_DIR / "mvm", REPO_DIR / "dist" / "mvm"]:
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    print_fail(
        "No compiled mvm binary found. Build one first:\n"
        "  go build -o dist/mvm ./cmd/mvm"
    )
    sys.exit(1)


def ensure_mirror_seeded(mirror: Path, binary: str) -> None:
    """Seed the asset mirror if empty, using *binary* for download commands."""
    if mirror.is_dir() and any(mirror.iterdir()):
        print_success(f"Mirror already seeded at {mirror}")
        return

    print_info("Seeding asset mirror (one-time download)...")
    env = {**os.environ, "MVM_ASSET_MIRROR": str(mirror)}
    seed_cmds = [
        [binary, "kernel", "pull", "--type", "firecracker", "--default"],
        [binary, "image", "pull", "alpine", "--version", "3.21"],
        [binary, "image", "pull", "ubuntu-minimal", "--version", "24.04"],
        [binary, "bin", "pull", "1.15.1", "--default"],
    ]
    for cmd in seed_cmds:
        result = subprocess.run(
            cmd,
            cwd=REPO_DIR,
            capture_output=True,
            text=True,
            env=env,
            timeout=1800,
        )
        if result.returncode != 0:
            print_warn(
                f"Seed command failed: {' '.join(cmd)}: {result.stderr.strip()}"
            )
    print_success(f"Mirror seeded at {mirror}")


def get_test_files(domain: str | None = None) -> list[Path]:
    """Return system test files sorted by name.

    Args:
        domain: If set, only return files under tests/system/{domain}/.
    """
    if domain:
        domain_dir = SYSTEM_TEST_DIR / domain
        if not domain_dir.is_dir():
            print_fail(f"Unknown domain: {domain}")
            sys.exit(1)
        return sorted(domain_dir.glob("test_*.py"))
    return sorted(SYSTEM_TEST_DIR.rglob("test_*.py"))


def _resolve_test_file(path_str: str) -> Path:
    """Resolve a test file path, supporting absolute or repo-root-relative paths."""
    p = Path(path_str)
    if p.is_absolute():
        return p.resolve()
    return (REPO_DIR / p).resolve()


def _is_system_test(file_path: Path) -> bool:
    """Check if a file path is under tests/system/."""
    try:
        rel = file_path.relative_to(REPO_DIR)
    except ValueError:
        return False
    return rel.parts[:2] == ("tests", "system")


def parse_results(results_file: Path) -> dict[str, str]:
    """Parse saved results file: {filename: PASS/FAIL/SKIP}.

    Format is one line per file::

        test_network.py: PASS
        test_vm_lifecycle.py: FAIL
        test_volume.py: SKIP

    ``--failed-only`` filters for entries where status == ``FAIL``.
    """
    if not results_file.exists():
        return {}
    results: dict[str, str] = {}
    for line in results_file.read_text().splitlines():
        line = line.strip()
        if ":" in line:
            name, status = line.split(":", 1)
            results[name.strip()] = status.strip()
    return results


def append_result(results_file: Path, name: str, status: str) -> None:
    """Append a single test result line to the results file."""
    results_file.parent.mkdir(parents=True, exist_ok=True)
    with open(results_file, "a") as f:
        f.write(f"{name}: {status}\n")


def copy_results_with_timestamp(results_file: Path) -> None:
    """Copy latest results to a timestamped file for history."""
    if not results_file.exists():
        return
    ts = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")
    archived = results_file.parent / f"system-test-results-{ts}.txt"
    shutil.copy2(results_file, archived)
    print_info(f"Results archived: {archived.name}")


def run_test_file(
    test_file: Path,
    binary: str,
    mirror: Path | None,
    use_mirror: bool,
    extra_args: list[str] | None = None,
    junit_dir: Path | None = None,
) -> str:
    """Run a single system test file. Returns PASS, FAIL, or SKIP."""
    env = {**os.environ, "MVM_BINARY": binary, "NO_COLOR": "1"}
    if use_mirror and mirror:
        env["MVM_ASSET_MIRROR"] = str(mirror)

    pytest_cmd: list[str] = [
        sys.executable,
        "-m",
        "pytest",
        str(test_file),
        "-q",
        "--no-header",
        "--no-cov",
        "-n",
        "0",
        "-rs",
    ]
    if junit_dir:
        junit_dir.mkdir(parents=True, exist_ok=True)
        junit_file = junit_dir / f"{test_file.stem}.xml"
        pytest_cmd.extend(["--junit-xml", str(junit_file)])
    if extra_args:
        pytest_cmd.extend(extra_args)

    start = time.monotonic()
    result = subprocess.run(
        pytest_cmd,
        cwd=REPO_DIR,
        capture_output=True,
        text=True,
        env=env,
        timeout=3600,
    )
    elapsed = int(time.monotonic() - start)

    # Parse pytest output for counts
    lines = result.stdout.splitlines()
    pytest_summary = next(
        (l for l in lines if "passed" in l or "failed" in l or "error" in l), ""
    )
    fail_lines = [
        l for l in lines if l.startswith("FAILED") or l.startswith("ERROR")
    ]
    skip_lines = [l for l in lines if l.startswith("SKIPPED")]

    # Classify result
    if result.returncode == 0:
        status = "PASS"
    elif "SKIPPED" in result.stdout or "skipped" in result.stderr:
        passed = lines and " passed" in lines[-1]
        failed = lines and " failed" in lines[-1]
        if not passed and not failed:
            status = "SKIP"
        else:
            status = "FAIL"
    else:
        status = "FAIL"

    # Print detailed output
    status_icon = (
        "\u2705"
        if status == "PASS"
        else ("\u23ed\ufe0f" if status == "SKIP" else "\u274c")
    )
    print(f"  {status_icon}  {test_file.name}  ({_fmt_duration(elapsed)})")
    print(f"       Result: {pytest_summary}")
    if fail_lines:
        print(f"       Failures: {len(fail_lines)}")
        for fl in fail_lines[:5]:
            print(f"         {fl.strip()}")
        if len(fail_lines) > 5:
            print(f"         ... and {len(fail_lines) - 5} more")
    elif status == "FAIL":
        # Fallback: search entire output for FAILED/ERROR lines
        all_fail = [
            l.strip()
            for l in lines
            if "FAILED" in l or "ERROR" in l
        ]
        if all_fail:
            print(f"       Failures: {len(all_fail)}")
            for fl in all_fail[:10]:
                print(f"         {fl}")
            if len(all_fail) > 10:
                print(f"         ... and {len(all_fail) - 10} more")
        else:
            tail = [l.strip() for l in lines[-15:] if l.strip()]
            if tail:
                print("       ── last output ──")
                for tl in tail:
                    print(f"         {tl}")
    if status == "FAIL":
        # Show full failure context — assertion errors and traceback lines
        detail = [
            l for l in lines if l.startswith("E   ") or "AssertionError" in l
        ]
        if detail:
            print("       ── failure detail ──")
            for dl in detail[:30]:
                print(f"         {dl.strip()}")
    if skip_lines:
        print(f"       Skipped: {len(skip_lines)}")
        for sl in skip_lines[:10]:
            print(f"         {sl.strip()}")
        if len(skip_lines) > 10:
            print(f"         ... and {len(skip_lines) - 10} more")
    print()

    return status


def _list_mode(domain: str | None = None) -> None:
    """List system test files, optionally filtered by domain."""
    print("=== System tests ===")
    for f in get_test_files(domain=domain):
        print(f"  {f.name}")


def _run_system_tests(
    args: argparse.Namespace,
    extra_args: list[str] | None = None,
) -> int:
    """Run system tests one file at a time. Returns number of failures."""
    # Determine binary
    if args.binary:
        binary = args.binary
    else:
        binary = _find_default_binary()

    print_info(f"Using binary: {binary}")

    mirror = None if args.no_mirror else DEFAULT_MIRROR

    # Ensure mirror is seeded
    if mirror and not args.domain:
        ensure_mirror_seeded(mirror, binary)

    test_files = get_test_files(domain=args.domain)

    # Filter to previously failed tests BEFORE clearing results
    if args.failed_only:
        prev_results = parse_results(RESULTS_FILE)
        failed_names = [n for n, s in prev_results.items() if s == "FAIL"]
        test_files = [f for f in test_files if f.name in failed_names]
        if not test_files:
            print_success("No previously failed tests.")
            return 0
        print_info(f"Re-running {len(test_files)} previously failed test(s)")

    # Fresh results file and junit dir for this run
    REPORTS_DIR.mkdir(parents=True, exist_ok=True)
    RESULTS_FILE.write_text("")
    shutil.rmtree(JUNIT_DIR, ignore_errors=True)
    JUNIT_DIR.mkdir(parents=True, exist_ok=True)

    # Run each file
    total_estimate = _fmt_duration(len(test_files) * 60)
    print_info(
        f"Running {len(test_files)} file(s) one by one (est. {total_estimate})..."
    )
    print()

    passed = failed = skipped = 0
    total_start = time.monotonic()

    for i, test_file in enumerate(test_files, 1):
        name = test_file.name
        elapsed_so_far = int(time.monotonic() - total_start)
        print(
            f"[{i}/{len(test_files)}] {name}  (elapsed: {_fmt_duration(elapsed_so_far)})",
            flush=True,
        )
        status = run_test_file(
            test_file,
            binary,
            mirror,
            use_mirror=mirror is not None,
            extra_args=extra_args,
            junit_dir=JUNIT_DIR,
        )
        append_result(RESULTS_FILE, name, status)
        if status == "PASS":
            passed += 1
        elif status == "FAIL":
            failed += 1
        else:
            skipped += 1

    # Summary
    total_elapsed = int(time.monotonic() - total_start)
    print("=" * 58)
    print(
        f"  {passed} passed  {failed} failed  {skipped} skipped  ({_fmt_duration(total_elapsed)})"
    )
    print()

    # Archive with timestamp for history
    copy_results_with_timestamp(RESULTS_FILE)

    return failed


def _run_single_system_test(
    args: argparse.Namespace,
    test_file: Path,
    extra_args: list[str] | None = None,
) -> int:
    """Run a single system test file in isolation. Returns 1 on failure, 0 on success.

    Skips mirror seeding, domain filtering, and ``--failed-only`` logic —
    those are irrelevant when the user specified an exact file.
    """
    if args.binary:
        binary = args.binary
    else:
        binary = _find_default_binary()

    print_info(f"Using binary: {binary}")
    mirror = None if args.no_mirror else DEFAULT_MIRROR

    print_info(f"Running system test: {test_file.name}")
    start = time.monotonic()
    status = run_test_file(
        test_file,
        binary,
        mirror,
        use_mirror=mirror is not None,
        extra_args=extra_args,
    )
    elapsed = int(time.monotonic() - start)

    if status == "PASS":
        return 0
    print_fail(f"System test failed: {test_file.name} ({_fmt_duration(elapsed)})")
    return 1


def _run(args: argparse.Namespace) -> int:
    """Run system tests. Returns number of failures (0 on success)."""
    extra_args: list[str] | None = (
        args.pytest_extra.split() if args.pytest_extra else None
    )

    if args.test_file:
        sys_failures = _run_single_system_test(
            args, args.test_file, extra_args=extra_args
        )
    else:
        sys_failures = _run_system_tests(args, extra_args=extra_args)

    if sys_failures > 0:
        print()
        print_fail(f"{sys_failures} system test file(s) failed")
    return sys_failures


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Run black-box system tests against the mvmctl Go binary.",
    )

    # CI mode
    parser.add_argument(
        "--ci",
        action="store_true",
        help="Sets MVM_TEST_ENFORCE_NO_SUDO=1 in the environment",
    )

    # Single-file flag
    parser.add_argument(
        "--test",
        "--file",
        dest="test_file",
        default=None,
        help="Run a specific system test file (absolute or relative to repo root)",
    )

    # Binary selection
    parser.add_argument(
        "--bin",
        "--binary",
        dest="binary",
        default=None,
        help="Path to compiled mvm binary (default: auto-detect ./mvm or dist/mvm)",
    )

    # Mirror / asset
    parser.add_argument(
        "--no-mirror",
        action="store_true",
        help="Skip asset mirror (download from internet)",
    )

    # Listing
    parser.add_argument(
        "--list",
        action="store_true",
        help="List system test files and exit",
    )

    # Domain filter
    parser.add_argument(
        "--domain",
        default=None,
        help="Run only a specific domain (e.g. 'vm', 'network')",
    )

    # Failed-only re-run
    parser.add_argument(
        "--failed-only",
        action="store_true",
        help="Re-run only previously failed tests (reads .reports/system-test-results.txt)",
    )

    # Extra pytest flags
    parser.add_argument(
        "--pytest-extra",
        dest="pytest_extra",
        default=None,
        help='Extra flags to pass through to pytest (e.g. --pytest-extra "-x --timeout=60")',
    )

    args = parser.parse_args()

    # Resolve and validate --test/--file argument
    test_file: Path | None = None
    if args.test_file:
        test_file = _resolve_test_file(args.test_file)
        if not test_file.is_file():
            parser.error(f"Test file not found: {test_file}")
        name = test_file.name
        if not (name.startswith("test_") or name.endswith("_test.py")):
            parser.error(
                f"File does not look like a test file: {name} "
                "(must start with test_ or end with _test.py)"
            )
        if not _is_system_test(test_file):
            parser.error(
                f"Test file must be under tests/system/: {test_file}"
            )

    # Validate: --domain makes no sense with a single file
    if test_file and args.domain:
        parser.error("--domain is not supported with --test (single file)")

    # Validate: --failed-only incompatible with --test
    if test_file and args.failed_only:
        parser.error("--failed-only is not supported with --test (single file)")

    # CI mode: just set the env var, then fall through to normal execution
    if args.ci:
        os.environ["MVM_TEST_ENFORCE_NO_SUDO"] = "1"

    # List mode
    if args.list:
        _list_mode(domain=args.domain)
        return

    # Run system tests
    total_failures = _run(args)

    if total_failures == 0:
        print_success("All tests passed")
    else:
        sys.exit(total_failures)


if __name__ == "__main__":
    main()
