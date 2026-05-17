#!/usr/bin/env python3
"""Run tests at one or more levels (unit, compliance, integration, system) in sequence.

Levels run in the order: unit → compliance → integration → system. If a level fails,
execution stops immediately and the script exits with the total failure count.

Unit, compliance, and integration tests run in parallel via pytest-xdist (if available).
System tests run one file at a time to avoid cross-file state pollution.

Compliance tests are NOT included by default — they must be explicitly requested with
``--compliance``.

Usage:
    python scripts/run_tests.py                                              # run unit + integration + system
    python scripts/run_tests.py --unit                                       # unit only
    python scripts/run_tests.py --compliance                                 # compliance only
    python scripts/run_tests.py --integration                                # integration only
    python scripts/run_tests.py --system                                     # system only (legacy behavior)
    python scripts/run_tests.py --unit --compliance                          # unit + compliance
    python scripts/run_tests.py --unit --compliance --integration            # unit + compliance + integration
    python scripts/run_tests.py --unit --compliance --integration --system   # all four levels
    python scripts/run_tests.py --system --build                             # system with build
    python scripts/run_tests.py --list                                       # list all test files

    # --pytest-extra: pass extra flags through to pytest invocations
    python scripts/run_tests.py --unit --pytest-extra "--cov=src/mvmctl --cov-fail-under=80"
    python scripts/run_tests.py --compliance --pytest-extra "-x"
    python scripts/run_tests.py --system --pytest-extra "-x --timeout=60"
    python scripts/run_tests.py --test tests/unit/test_foo.py --pytest-extra "-p no:cacheprovider"
    python scripts/run_tests.py --list --unit                                # list unit test files
    python scripts/run_tests.py --list --compliance                          # list compliance test files
    python scripts/run_tests.py --list --system --domain vm                  # list vm domain system tests
    python scripts/run_tests.py --list --unit --compliance --system          # list unit + compliance + system

    # --test: run a single specific test file (absolute or relative to repo root)
    python scripts/run_tests.py --test tests/unit/test_foo.py                # run a specific unit test
    python scripts/run_tests.py --test tests/layer_compliance/test_foo.py --compliance
    python scripts/run_tests.py --test tests/system/vm/test_vm.py --system  # system test, single file
    python scripts/run_tests.py --test tests/unit/test_foo.py --unit --system  # file matches unit level only

    # --system flags (only apply to system mode):
    python scripts/run_tests.py --system --build                             # build dist/mvm onefile first
    python scripts/run_tests.py --system --bin /path/to/mvm                  # use a specific binary
    python scripts/run_tests.py --system --no-mirror                         # skip asset mirror
    python scripts/run_tests.py --system --domain vm                         # run only vm domain tests
    python scripts/run_tests.py --system --failed-only                       # re-run only previously failed

    # --domain: matches tests/system/{domain}/ directories. Each domain has
    #   its own conftest with minimal asset setup (no cross-domain pollution).
    #   Valid domains: bin, cache, cli, config, console, full_journeys, host,
    #   images, init, invariants, kernel, keys, logs, network, ssh, vm, volume,
    #   zzz_destructive

    # --bin vs --build:
    #   Default (no flags)  → "uv run mvm" (from source, no build needed)
    #   --build             → builds dist/mvm with --release, then uses it
    #   --bin X             → uses X as MVM_BINARY, skips build (path or "uv run mvm")

    # --failed-only: reads .reports/system-test-results.txt from the last full run
    #   and re-runs only the files that had "FAIL" status. Each line in that file
    #   is "filename: STATUS" (e.g. "test_network.py: PASS"). --failed-only
    #   filters for "filename: FAIL" entries.

    # --ci: Sets MVM_TEST_ENFORCE_NO_SUDO=1 in the environment.
    #   No separate execution path — runs the same as without --ci.
    #   Works with any combination of --unit, --compliance, --integration, --system.
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

REPO_DIR = Path(__file__).resolve().parent.parent
SYSTEM_TEST_DIR = REPO_DIR / "tests" / "system"
UNIT_TEST_DIR = REPO_DIR / "tests" / "unit"
INTEGRATION_TEST_DIR = REPO_DIR / "tests" / "integration"
COMPLIANCE_TEST_DIR = REPO_DIR / "tests" / "layer_compliance"
DEFAULT_MIRROR = Path.home() / ".cache" / "mvm-asset-mirror"
BUILT_BINARY = REPO_DIR / "dist" / "mvm"

REPORTS_DIR = REPO_DIR / ".reports"
RESULTS_FILE = REPO_DIR / ".reports" / "system-test-results-latest.txt"
JUNIT_DIR = REPO_DIR / ".reports" / "junit"

# Cached check for pytest-xdist availability
_XDIST_AVAILABLE: bool | None = None


def _info(msg: str) -> None:
    print(f"[info] {msg}")


def _ok(msg: str) -> None:
    print(f"[ ok ] {msg}")


def _fail(msg: str) -> None:
    print(f"[fail] {msg}")


def _warn(msg: str) -> None:
    print(f"[warn] {msg}")


def _fmt_duration(seconds: int) -> str:
    if seconds < 60:
        return f"{seconds}s"
    return f"{seconds // 60}m{seconds % 60:02d}s"


def is_built_binary(binary: str) -> bool:
    """Check if *binary* is a file path (built binary) vs a command string."""
    return (
        not binary.startswith("uv ")
        and not shutil.which(binary.split()[0]) != binary.split()[0]
        if binary
        else False
    )


def build_binary() -> str:
    """Build dist/mvm with --release and return the path."""
    if BUILT_BINARY.exists():
        _ok(f"Binary already built at {BUILT_BINARY}")
        return str(BUILT_BINARY)

    _info("Building dist/mvm with --release ...")
    build_script = REPO_DIR / "scripts" / "build_services.py"
    result = subprocess.run(
        [sys.executable, str(build_script), "--release"],
        cwd=REPO_DIR,
        capture_output=True,
        text=True,
        timeout=3600,
    )
    if result.returncode != 0:
        print(result.stderr, file=sys.stderr)
        sys.exit(1)
    _ok(f"Binary built at {BUILT_BINARY}")
    return str(BUILT_BINARY)


def ensure_mirror_seeded(mirror: Path) -> None:
    """Seed the asset mirror if empty."""
    if mirror.is_dir() and any(mirror.iterdir()):
        _ok(f"Mirror already seeded at {mirror}")
        return

    _info("Seeding asset mirror (one-time download)...")
    env = {**os.environ, "MVM_ASSET_MIRROR": str(mirror)}
    seed_cmds = [
        [
            "uv",
            "run",
            "mvm",
            "kernel",
            "pull",
            "--type",
            "firecracker",
            "--default",
        ],
        ["uv", "run", "mvm", "image", "pull", "alpine", "--version", "3.21"],
        [
            "uv",
            "run",
            "mvm",
            "image",
            "pull",
            "ubuntu-minimal",
            "--version",
            "24.04",
        ],
        ["uv", "run", "mvm", "bin", "pull", "1.15.1", "--default"],
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
            _warn(
                f"Seed command failed: {' '.join(cmd)}: {result.stderr.strip()}"
            )
    _ok(f"Mirror seeded at {mirror}")


def get_test_files(domain: str | None = None) -> list[Path]:
    """Return system test files sorted by name.

    Args:
        domain: If set, only return files under tests/system/{domain}/.
    """
    if domain:
        domain_dir = SYSTEM_TEST_DIR / domain
        if not domain_dir.is_dir():
            _fail(f"Unknown domain: {domain}")
            sys.exit(1)
        return sorted(domain_dir.glob("test_*.py"))
    return sorted(SYSTEM_TEST_DIR.rglob("test_*.py"))


def _resolve_test_file(path_str: str) -> Path:
    """Resolve a test file path, supporting absolute or repo-root-relative paths."""
    p = Path(path_str)
    if p.is_absolute():
        return p.resolve()
    return (REPO_DIR / p).resolve()


def _get_file_test_level(file_path: Path) -> str | None:
    """Determine the test level (unit/integration/system) from a file's path.

    Returns ``None`` if the path is not under any recognised test directory.
    """
    try:
        rel = file_path.relative_to(REPO_DIR)
    except ValueError:
        return None
    parts = rel.parts
    if len(parts) >= 2 and parts[0] == "tests":
        if parts[1] == "unit":
            return "unit"
        if parts[1] == "integration":
            return "integration"
        if parts[1] == "system":
            return "system"
        if parts[1] == "layer_compliance":
            return "compliance"
    return None


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
    _info(f"Results archived: {archived.name}")


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
        "uv",
        "run",
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


def _check_xdist() -> bool:
    """Check if pytest-xdist is available."""
    global _XDIST_AVAILABLE
    if _XDIST_AVAILABLE is not None:
        return _XDIST_AVAILABLE
    result = subprocess.run(
        ["uv", "run", "python", "-c", "import xdist"],
        capture_output=True,
        text=True,
        timeout=30,
    )
    _XDIST_AVAILABLE = result.returncode == 0
    return _XDIST_AVAILABLE


def _run_pytest_level(
    test_dir: Path,
    label: str,
    timeout: int = 1200,
    test_file: Path | None = None,
    extra_args: list[str] | None = None,
) -> bool:
    """Run pytest on a test directory. Returns True if all tests pass.

    When *test_file* is provided it overrides *test_dir* and forces serial
    execution (``-n 0``) to avoid xdist issues with a single file.
    """
    target = str(test_file) if test_file else str(test_dir)
    cmd: list[str] = [
        "uv",
        "run",
        "pytest",
        target,
        "-q",
        "--no-header",
        "--no-cov",
        "-rs",
        "-o",
        "addopts=",
    ]
    if test_file:
        cmd.extend(["-n", "0"])
    elif _check_xdist():
        cmd.extend(["-n", "auto"])

    if extra_args:
        cmd.extend(extra_args)

    _info(f"Running {label} tests ...")
    start = time.monotonic()
    result = subprocess.run(
        cmd,
        cwd=REPO_DIR,
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    elapsed = int(time.monotonic() - start)

    if result.returncode == 0:
        print(f"   \u2705  {label}  ({_fmt_duration(elapsed)})")
        return True

    # Failure details
    print(f"   \u274c  {label}  ({_fmt_duration(elapsed)})")
    lines = result.stdout.splitlines()
    fail_lines = [
        line
        for line in lines
        if line.startswith("FAILED") or line.startswith("ERROR")
    ]
    skip_lines = [l for l in lines if l.startswith("SKIPPED")]
    pytest_summary = next(
        (
            line
            for line in lines
            if "passed" in line or "failed" in line or "error" in line
        ),
        "",
    )
    if pytest_summary:
        print(f"       Result: {pytest_summary}")
    if fail_lines:
        print(f"       Failures: {len(fail_lines)}")
        for fl in fail_lines[:5]:
            print(f"         {fl.strip()}")
        if len(fail_lines) > 5:
            print(f"         ... and {len(fail_lines) - 5} more")
    if skip_lines:
        print(f"       Skipped: {len(skip_lines)}")
        for sl in skip_lines[:10]:
            print(f"         {sl.strip()}")
        if len(skip_lines) > 10:
            print(f"         ... and {len(skip_lines) - 10} more")
    return False


def _list_mode(levels: list[str], domain: str | None = None) -> None:
    """List test files for the specified levels."""
    for level in levels:
        if level == "unit":
            print("=== Unit tests ===")
            for f in sorted(UNIT_TEST_DIR.rglob("test_*.py")):
                print(f"  {f.name}")
        elif level == "compliance":
            print("=== Layer compliance tests ===")
            for f in sorted(COMPLIANCE_TEST_DIR.rglob("test_*.py")):
                print(f"  {f.name}")
        elif level == "integration":
            print("=== Integration tests ===")
            for f in sorted(INTEGRATION_TEST_DIR.rglob("test_*.py")):
                print(f"  {f.name}")
        elif level == "system":
            print("=== System tests ===")
            for f in get_test_files(domain=domain):
                print(f"  {f.name}")


def _run_system_tests(
    args: argparse.Namespace,
    extra_args: list[str] | None = None,
) -> int:
    """Run system tests one file at a time. Returns number of failures."""
    # Determine binary
    if args.build:
        binary = build_binary()
    elif args.binary:
        binary = args.binary
    else:
        binary = "uv run mvm"

    _info(f"Using binary: {binary}")

    mirror = None if args.no_mirror else DEFAULT_MIRROR

    # Ensure mirror is seeded (only when running all domains)
    if mirror and not args.domain:
        ensure_mirror_seeded(mirror)

    test_files = get_test_files(domain=args.domain)

    # Filter to previously failed tests BEFORE clearing results
    if args.failed_only:
        prev_results = parse_results(RESULTS_FILE)
        failed_names = [n for n, s in prev_results.items() if s == "FAIL"]
        test_files = [f for f in test_files if f.name in failed_names]
        if not test_files:
            _ok("No previously failed tests.")
            return 0
        _info(f"Re-running {len(test_files)} previously failed test(s)")

    # Fresh results file and junit dir for this run
    REPORTS_DIR.mkdir(parents=True, exist_ok=True)
    RESULTS_FILE.write_text("")
    shutil.rmtree(JUNIT_DIR, ignore_errors=True)
    JUNIT_DIR.mkdir(parents=True, exist_ok=True)

    # Run each file
    total_estimate = _fmt_duration(len(test_files) * 60)
    _info(
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

    # Skip ratio check after all system test files complete
    if args.skip_ratio_check and test_files:
        _info("Running skip ratio check...")
        check_script = REPO_DIR / "scripts" / "check_skip_ratio.py"
        check_result = subprocess.run(
            [
                sys.executable,
                str(check_script),
                "--junit-xml",
                str(JUNIT_DIR),
                "--verbose",
            ],
            cwd=REPO_DIR,
            capture_output=True,
            text=True,
            timeout=60,
        )
        # Print check output regardless
        print(check_result.stdout)
        if check_result.stderr:
            print(check_result.stderr, file=sys.stderr)

        if check_result.returncode != 0:
            _fail(
                "Skip ratio check failed: one or more test files exceed the skip threshold."
            )
            failed += 1

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
    if args.build:
        binary = build_binary()
    elif args.binary:
        binary = args.binary
    else:
        binary = "uv run mvm"

    _info(f"Using binary: {binary}")
    mirror = None if args.no_mirror else DEFAULT_MIRROR

    _info(f"Running system test: {test_file.name}")
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
    _fail(f"System test failed: {test_file.name} ({_fmt_duration(elapsed)})")
    return 1


def _run_levels(
    args: argparse.Namespace, levels: list[str], test_file: Path | None = None
) -> int:
    """Run tests for the specified levels in sequence.

    When *test_file* is provided it is passed down to individual level runners
    instead of running the full directory.

    Returns the total number of failures (0 on success).
    """
    extra_args: list[str] | None = (
        args.pytest_extra.split() if args.pytest_extra else None
    )

    failures = 0
    for level in levels:
        if level == "unit":
            ok = _run_pytest_level(
                UNIT_TEST_DIR,
                "unit tests",
                test_file=test_file,
                extra_args=extra_args,
            )
            if not ok:
                failures += 1
                return failures
        elif level == "compliance":
            ok = _run_pytest_level(
                COMPLIANCE_TEST_DIR,
                "layer compliance tests",
                test_file=test_file,
                extra_args=extra_args,
            )
            if not ok:
                failures += 1
                return failures
        elif level == "integration":
            ok = _run_pytest_level(
                INTEGRATION_TEST_DIR,
                "integration tests",
                test_file=test_file,
                extra_args=extra_args,
            )
            if not ok:
                failures += 1
                return failures
        elif level == "system":
            if test_file:
                sys_failures = _run_single_system_test(
                    args, test_file, extra_args=extra_args
                )
            else:
                sys_failures = _run_system_tests(args, extra_args=extra_args)
            if sys_failures > 0:
                failures += sys_failures
                print()
                _fail(f"{sys_failures} system test file(s) failed")
                return failures

    return failures


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Run tests at one or more levels (unit, compliance, integration, system).",
    )

    # Level flags
    parser.add_argument(
        "--unit",
        action="store_true",
        help="Run unit tests",
    )
    parser.add_argument(
        "--compliance",
        action="store_true",
        help="Run layer compliance tests (not included by default)",
    )
    parser.add_argument(
        "--integration",
        action="store_true",
        help="Run integration tests",
    )
    parser.add_argument(
        "--system",
        action="store_true",
        help="Run system tests (legacy behavior)",
    )
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
        help="Run a specific test file (absolute path or relative to repo root)",
    )

    # Skip ratio check flag (applies to system tests)
    parser.add_argument(
        "--skip-ratio-check",
        "--no-skip-ratio-check",
        dest="skip_ratio_check",
        action=argparse.BooleanOptionalAction,
        default=True,
        help="Enable/disable skip ratio check after system tests (default: enabled)",
    )

    # System-only flags (unchanged)
    parser.add_argument(
        "--bin",
        "--binary",
        dest="binary",
        default=None,
        help='MVM_BINARY value (default: "uv run mvm"). Skips build.',
    )
    parser.add_argument(
        "--build",
        action="store_true",
        help="Build dist/mvm with --release first, then use it",
    )
    parser.add_argument(
        "--no-mirror",
        action="store_true",
        help="Skip asset mirror (download from internet)",
    )
    parser.add_argument(
        "--list",
        action="store_true",
        help="List test files and exit",
    )
    parser.add_argument(
        "--domain",
        default=None,
        help="Run only a specific domain (e.g. 'vm', 'network')",
    )
    parser.add_argument(
        "--failed-only",
        action="store_true",
        help="Re-run only previously failed tests (reads .reports/system-test-results.txt)",
    )

    # Extra pytest flags (all test levels)
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

    # Determine which levels to run
    levels: list[str] = []
    if args.unit:
        levels.append("unit")
    if args.compliance:
        levels.append("compliance")
    if args.integration:
        levels.append("integration")
    if args.system:
        levels.append("system")
    if not levels:
        levels = ["unit", "integration", "system"]

    # When --test is provided, narrow levels to the file's actual location
    if test_file:
        file_level = _get_file_test_level(test_file)
        if file_level is None:
            parser.error(
                f"Cannot determine test level for {test_file} — "
                "must be under tests/unit/, tests/integration/, "
                "tests/layer_compliance/, or tests/system/"
            )
        matched = [lvl for lvl in levels if lvl == file_level]
        if not matched:
            parser.error(
                f"Test file {test_file.name} is a {file_level} test, "
                f"but --{file_level} was not requested"
            )
        levels = matched

    # Validate: --domain requires --system (skip when running a specific file)
    if not test_file and args.domain and "system" not in levels:
        parser.error("--domain requires --system (it is a system-test concept)")

    # CI mode: just set the env var, then fall through to normal execution
    if args.ci:
        os.environ["MVM_TEST_ENFORCE_NO_SUDO"] = "1"

    # List mode
    if args.list:
        _list_mode(levels, domain=args.domain)
        return

    # Run levels in sequence
    total_failures = _run_levels(args, levels, test_file=test_file)

    if total_failures == 0:
        _ok("All tests passed")
    else:
        sys.exit(total_failures)


if __name__ == "__main__":
    main()
