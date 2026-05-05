#!/usr/bin/env python3
"""Build all service binaries concurrently, then build the main mvm binary.

Usage:
    python scripts/build_services.py                    # Build everything
    python scripts/build_services.py --services-only    # Only build service binaries
    python scripts/build_services.py --main-only        # Only build main binary

Prerequisites:
    uv sync --group dev --group build
"""

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path

# ── Colors ──────────────────────────────────────────────────────────────────
try:
    from rich.console import Console

    _console = Console()

    def _info(msg: str) -> None:
        _console.print(f"[cyan][build][/] {msg}")

    def _ok(msg: str) -> None:
        _console.print(f"[green][  ok][/] {msg}")

    def _fail(msg: str) -> None:
        _console.print(f"[red][fail][/] {msg}")

    def _warn(msg: str) -> None:
        _console.print(f"[yellow][warn][/] {msg}")

except ImportError:
    _RED = "\033[0;31m"
    _GREEN = "\033[0;32m"
    _YELLOW = "\033[1;33m"
    _CYAN = "\033[0;36m"
    _NC = "\033[0m"

    def _info(msg: str) -> None:
        print(f"{_CYAN}[build]{_NC} {msg}")

    def _ok(msg: str) -> None:
        print(f"{_GREEN}[  ok]{_NC} {msg}")

    def _fail(msg: str) -> None:
        print(f"{_RED}[fail]{_NC} {msg}")

    def _warn(msg: str) -> None:
        print(f"{_YELLOW}[warn]{_NC} {msg}")


# ── Constants ───────────────────────────────────────────────────────────────
SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_DIR = SCRIPT_DIR.parent
SERVICES_DIR = PROJECT_DIR / "dist" / "services"
SYMLINKS_DIR = PROJECT_DIR / "build" / "symlinks"
NPROC = os.cpu_count() or 1

SERVICES: list[tuple[str, str]] = [
    ("mvm-console-relay", "src/mvmctl/services/console_relay/process.py"),
    ("mvm-nocloud-server", "src/mvmctl/services/nocloud_server/process.py"),
    ("mvm-provision", "src/mvmctl/services/loopmount/process.py"),
]

# Nuitka flags optimized for small stdlib-only binaries.
# - lto=yes: link-time optimization, dead code elimination
# - anti-bloat: strips known-bloat imports (docstrings, deprecation warnings)
# - no_docstrings: discards all docstrings at compile time
# - no_asserts: removes assert statements
# - nofollow-import-to: prevents following unused stdlib modules
# - noinclude-*-mode=error: fails fast if bloat packages are accidentally included
SERVICE_NUITKA_FLAGS: list[str] = [
    "--onefile",
    "--lto=yes",
    "--enable-plugin=anti-bloat",
    "--python-flag=no_docstrings",
    "--python-flag=no_asserts",
    "--nofollow-import-to=*.tests",
    "--nofollow-import-to=*.distutils",
    "--nofollow-import-to=*.unittest",
    "--nofollow-import-to=*.venv",
    "--nofollow-import-to=*.ctypes",
    "--nofollow-import-to=*.email",
    "--nofollow-import-to=*.xml",
    "--nofollow-import-to=*.logging",
    "--nofollow-import-to=*.http",
    "--nofollow-import-to=*.urllib",
    "--nofollow-import-to=*.pdb",
    "--nofollow-import-to=*.inspect",
    "--nofollow-import-to=*.pydoc",
    "--nofollow-import-to=*.ensurepip",
    "--noinclude-setuptools-mode=error",
    "--noinclude-pytest-mode=error",
    "--noinclude-unittest-mode=error",
    "--noinclude-pydoc-mode=error",
]


# ── Helpers ─────────────────────────────────────────────────────────────────
def _print_last_lines(path: Path, n: int) -> None:
    """Print the last *n* lines of a file to stderr."""
    try:
        lines = path.read_text().splitlines()
        for line in lines[-n:]:
            print(line, file=sys.stderr)
    except OSError:
        pass


def _run_nuitka(args: list[str], logfile: Path) -> int:
    """Execute ``uv run python -m nuitka`` with *args* and tee output to *logfile*."""
    logfile.parent.mkdir(parents=True, exist_ok=True)
    cmd = ["uv", "run", "python", "-m", "nuitka", *args]
    result = subprocess.run(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    logfile.write_text(result.stdout)
    return result.returncode


# ── Build steps ────────────────────────────────────────────────────────────
def build_services() -> bool:
    """Build a single multidist binary with all 3 service entry points.

    Creates temporary symlinks with unique names (since all sources are
    ``process.py``), compiles with ``--main`` for each, then cleans up.
    The output file ``mvm-services`` is a single binary that dispatches
    to the correct service based on ``sys.argv[0]``.
    """
    _info("Building service binaries (multidist)...")
    SERVICES_DIR.mkdir(parents=True, exist_ok=True)

    # Create temp symlinks with unique names
    SYMLINKS_DIR.mkdir(parents=True, exist_ok=True)
    main_flags: list[str] = []
    for name, source in SERVICES:
        link_path = SYMLINKS_DIR / name
        if not link_path.exists():
            rel_source = os.path.relpath(PROJECT_DIR / source, SYMLINKS_DIR)
            link_path.symlink_to(rel_source)
        main_flags.extend(["--main", str(link_path)])

    logfile = SERVICES_DIR / "mvm-services.build.log"
    args: list[str] = [
        *SERVICE_NUITKA_FLAGS,
        f"--output-dir={SERVICES_DIR}",
        "--output-filename=mvm-services",
        *main_flags,
    ]
    rc = _run_nuitka(args, logfile)

    # Clean up symlinks
    shutil.rmtree(SYMLINKS_DIR, ignore_errors=True)

    if rc == 0:
        _ok("mvm-services built successfully")
        return True
    _fail(f"mvm-services build failed (see {logfile})")
    _print_last_lines(logfile, 20)
    return False


def build_main() -> bool:
    """Build the main ``mvm`` binary."""
    _info("Building main mvm binary...")

    # Ensure combined service binary exists before building main
    if not (SERVICES_DIR / "mvm-services").exists():
        _warn(
            "Combined service binary mvm-services not found — building services first"
        )
        if not build_services():
            return False

    logfile = PROJECT_DIR / "dist" / "mvm.build.log"
    args: list[str] = [
        "--onefile",
        f"--output-dir={PROJECT_DIR / 'dist'}",
        "--output-filename=mvm",
        "--include-package=mvmctl",
        f"--include-data-dir={PROJECT_DIR / 'src' / 'mvmctl' / 'assets'}=mvmctl/assets",
        f"--include-data-dir={SERVICES_DIR}=mvmctl/services",
        "--lto=yes",
        "--enable-plugin=anti-bloat",
        "--nofollow-import-to=*.tests",
        "--nofollow-import-to=*.unittest",
        "--nofollow-import-to=*.venv",
        f"--jobs={NPROC}",
        str(PROJECT_DIR / "src" / "mvmctl" / "main.py"),
    ]
    rc = _run_nuitka(args, logfile)
    if rc == 0:
        _ok("Main binary built at dist/mvm")
        return True
    _fail(f"Main binary build failed (see {logfile})")
    _print_last_lines(logfile, 30)
    return False


# ── Entry point ────────────────────────────────────────────────────────────
def main() -> None:
    parser = argparse.ArgumentParser(
        description="Build mvmctl service and main binaries",
    )
    parser.add_argument(
        "--services-only",
        action="store_true",
        help="Only build service binaries",
    )
    parser.add_argument(
        "--main-only",
        action="store_true",
        help="Only build main binary",
    )
    args = parser.parse_args()

    if args.services_only:
        success = build_services()
    elif args.main_only:
        success = build_main()
    else:
        success = build_services() and build_main()

    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
