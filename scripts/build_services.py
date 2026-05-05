#!/usr/bin/env python3
"""Build mvmctl service and main binaries.

Usage:
    python scripts/build_services.py                    # Build everything (default)
    python scripts/build_services.py --services         # Only build service binaries
    python scripts/build_services.py --service <name>   # Build a specific service
    python scripts/build_services.py --mvm              # Only build main binary
    python scripts/build_services.py --release          # Build both services and mvm
    python scripts/build_services.py --fast             # Fast compile mode (default)
    python scripts/build_services.py --optimize         # Aggressive optimization mode

Prerequisites:
    uv sync --group dev --group build
"""

import argparse
import glob
import os
import shutil
import subprocess
import sys
import sysconfig
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

SERVICE_NAMES: set[str] = {name for name, _ in SERVICES}

# Fast mode: minimal flags for quick compilation.
SERVICE_FAST_FLAGS: list[str] = [
    "--onefile",
    f"--jobs={NPROC}",
]

# Optimize mode: aggressive size reduction and tree-shaking.
SERVICE_OPTIMIZE_FLAGS: list[str] = [
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
    "--deployment",
    "--python-flag=isolated",
    "--python-flag=no_site",
    "--remove-output",
    "--noinclude-default-mode=nofollow",
    "--noinclude-IPython-mode=nofollow",
    "--noinclude-dask-mode=nofollow",
    "--noinclude-numba-mode=nofollow",
    "--nofollow-import-to=pkg_resources",
    f"--jobs={NPROC}",
]

# Fast mode: minimal flags for the main binary.
MAIN_FAST_FLAGS: list[str] = [
    "--onefile",
    f"--output-dir={PROJECT_DIR / 'dist'}",
    "--output-filename=mvm",
    "--include-package=mvmctl",
    f"--include-data-dir={PROJECT_DIR / 'src' / 'mvmctl' / 'assets'}=mvmctl/assets",
    f"--include-data-dir={SERVICES_DIR}=mvmctl/services",
    # passlib uses a dynamic registry to load hash handlers at runtime.
    # Nuitka's static analysis cannot trace these imports, so we force-include
    # the modules that would otherwise be tree-shaken away.
    "--include-module=passlib.handlers.bcrypt",
    "--include-module=passlib.handlers.sha512_crypt",
    f"--jobs={NPROC}",
]

# Optimize mode: add aggressive optimization flags.
MAIN_OPTIMIZE_FLAGS: list[str] = [
    *MAIN_FAST_FLAGS,
    "--lto=yes",
    "--enable-plugin=anti-bloat",
    "--nofollow-import-to=*.tests",
    "--nofollow-import-to=*.unittest",
    "--nofollow-import-to=*.venv",
    "--deployment",
    "--python-flag=isolated",
    "--python-flag=no_site",
    "--remove-output",
    "--noinclude-default-mode=nofollow",
    "--noinclude-IPython-mode=nofollow",
    "--noinclude-dask-mode=nofollow",
    "--noinclude-numba-mode=nofollow",
    "--nofollow-import-to=pkg_resources",
]


# ── Helpers ─────────────────────────────────────────────────────────────────
def _has_static_libpython() -> bool:
    """Check whether the current Python installation supports static linking."""
    libpl = sysconfig.get_config_var("LIBPL")
    if not libpl:
        return False
    return bool(glob.glob(f"{libpl}/libpython*.a"))


def _print_last_lines(path: Path, n: int) -> None:
    """Print the last *n* lines of a file to stderr."""
    try:
        lines = path.read_text().splitlines()
        for line in lines[-n:]:
            print(line, file=sys.stderr)
    except OSError:
        pass


def _run_nuitka(args: list[str], logfile: Path, mode: str = "fast") -> int:
    """Execute Nuitka with *args* and capture output to *logfile*."""
    logfile.parent.mkdir(parents=True, exist_ok=True)
    cmd = [sys.executable, "-m", "nuitka", *args]
    env: dict[str, str] | None = None
    if mode == "optimize":
        env = dict(os.environ)
        env["CCFLAGS"] = "-Os"
        env["LDFLAGS"] = "-Os"
    result = subprocess.run(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env=env,
    )
    logfile.write_text(result.stdout)
    return result.returncode


# ── Build steps ────────────────────────────────────────────────────────────
def _build_all_services(mode: str) -> bool:
    """Build a single multidist binary with all service entry points."""
    _info("Building all service binaries (multidist)...")
    SERVICES_DIR.mkdir(parents=True, exist_ok=True)

    # Create temp symlinks with unique names
    SYMLINKS_DIR.mkdir(parents=True, exist_ok=True)
    main_flags: list[str] = []
    for name, source in SERVICES:
        link_path = SYMLINKS_DIR / name
        if not link_path.exists():
            rel_source = os.path.relpath(PROJECT_DIR / source, SYMLINKS_DIR)
            link_path.symlink_to(rel_source)
        main_flags.append(f"--main={link_path}")

    flags = list(
        SERVICE_OPTIMIZE_FLAGS if mode == "optimize" else SERVICE_FAST_FLAGS
    )
    if mode == "optimize" and _has_static_libpython():
        flags.append("--static-libpython=yes")
    elif mode == "optimize":
        _warn(
            "Static libpython not available — using dynamic linking. "
            "For maximum optimization, build with a standard Python "
            "(e.g. pyenv/system Python) instead of a standalone distribution."
        )
    logfile = SERVICES_DIR / "mvm-services.build.log"
    args: list[str] = [
        *flags,
        f"--output-dir={SERVICES_DIR}",
        "--output-filename=mvm-services",
        *main_flags,
    ]
    rc = _run_nuitka(args, logfile, mode=mode)

    # Clean up symlinks
    shutil.rmtree(SYMLINKS_DIR, ignore_errors=True)

    if rc == 0:
        _ok("mvm-services built successfully")
        return True
    _fail(f"mvm-services build failed (see {logfile})")
    _print_last_lines(logfile, 20)
    return False


def _build_single_service(name: str, source: str, mode: str) -> bool:
    """Build a single service binary."""
    _info(f"Building service {name}...")
    flags = list(
        SERVICE_OPTIMIZE_FLAGS if mode == "optimize" else SERVICE_FAST_FLAGS
    )
    if mode == "optimize" and _has_static_libpython():
        flags.append("--static-libpython=yes")
    elif mode == "optimize":
        _warn(
            "Static libpython not available — using dynamic linking. "
            "For maximum optimization, build with a standard Python "
            "(e.g. pyenv/system Python) instead of a standalone distribution."
        )
    logfile = SERVICES_DIR / f"{name}.build.log"
    args: list[str] = [
        *flags,
        f"--output-dir={SERVICES_DIR}",
        f"--output-filename={name}",
        str(PROJECT_DIR / source),
    ]
    rc = _run_nuitka(args, logfile, mode=mode)
    if rc == 0:
        _ok(f"{name} built successfully")
        return True
    _fail(f"{name} build failed (see {logfile})")
    _print_last_lines(logfile, 20)
    return False


def build_services(names: list[str] | None = None, mode: str = "fast") -> bool:
    """Build service binaries.

    If *names* is ``None``, build all services as a single multidist binary.
    If *names* is provided, build each named service individually.
    """
    if names is None:
        return _build_all_services(mode)

    success = True
    for name in names:
        source = next(
            (src for svc_name, src in SERVICES if svc_name == name),
            None,
        )
        if source is None:
            _fail(f"Unknown service: {name}")
            return False
        success = _build_single_service(name, source, mode) and success

    return success


def build_main(mode: str = "fast") -> bool:
    """Build the main ``mvm`` binary."""
    _info("Building main mvm binary...")

    # Ensure combined service binary exists before building main
    if not (SERVICES_DIR / "mvm-services").exists():
        _warn(
            "Combined service binary mvm-services not found — building services first"
        )
        if not build_services(mode=mode):
            return False

    flags = list(MAIN_OPTIMIZE_FLAGS if mode == "optimize" else MAIN_FAST_FLAGS)
    if mode == "optimize" and _has_static_libpython():
        flags.append("--static-libpython=yes")
    elif mode == "optimize":
        _warn(
            "Static libpython not available — using dynamic linking. "
            "For maximum optimization, build with a standard Python "
            "(e.g. pyenv/system Python) instead of a standalone distribution."
        )
    logfile = PROJECT_DIR / "dist" / "mvm.build.log"
    args: list[str] = [
        *flags,
        str(PROJECT_DIR / "src" / "mvmctl" / "main.py"),
    ]
    rc = _run_nuitka(args, logfile, mode=mode)
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
        "--services",
        action="store_true",
        help="Build all service binaries",
    )
    parser.add_argument(
        "--service",
        action="append",
        metavar="NAME",
        help="Build a specific service by name",
    )
    parser.add_argument(
        "--mvm",
        action="store_true",
        help="Build the main mvm binary",
    )
    parser.add_argument(
        "--release",
        action="store_true",
        help="Build both services and mvm (default when no target flag is specified)",
    )
    parser.add_argument(
        "--fast",
        action="store_true",
        help="Fast compile mode with minimal Nuitka flags (default)",
    )
    parser.add_argument(
        "--optimize",
        action="store_true",
        help="Aggressive optimization mode with all Nuitka size and tree-shaking flags",
    )
    args = parser.parse_args()

    # Validate service names
    if args.service:
        for name in args.service:
            if name not in SERVICE_NAMES:
                _fail(
                    f"Unknown service: {name!r}. "
                    f"Valid: {', '.join(sorted(SERVICE_NAMES))}"
                )
                sys.exit(1)

    # Compile mode: optimize overrides fast
    mode = "optimize" if args.optimize else "fast"

    # Determine targets
    has_target = args.services or args.service or args.mvm or args.release
    if not has_target:
        args.release = True

    # --services takes precedence over --service; --mvm requires all services
    build_all_services = args.services or args.release or args.mvm
    build_specific_services = (
        args.service if (args.service and not build_all_services) else []
    )
    build_main_binary = args.mvm or args.release

    success = True
    if build_all_services:
        success = build_services(mode=mode) and success
    if build_specific_services:
        success = (
            build_services(names=build_specific_services, mode=mode) and success
        )
    if build_main_binary:
        success = build_main(mode=mode) and success

    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
