#!/usr/bin/env python3
"""Build mvmctl service and main binaries.

Usage:
    python scripts/build_services.py                    # Build everything (default)
    python scripts/build_services.py --services         # Only build service binaries
    python scripts/build_services.py --service <name>   # Build a specific service

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

from common import (
    PROJECT_ROOT as PROJECT_DIR,
)
from common import (
    Timer,
    print_fail,
    print_info,
    print_success,
    print_warn,
)

# ── Constants ───────────────────────────────────────────────────────────────
SERVICES_DIR = PROJECT_DIR / "dist" / "services"
SYMLINKS_DIR = PROJECT_DIR / "build" / "symlinks"
NPROC = os.cpu_count() or 1

SERVICES: list[tuple[str, str]] = [
    ("mvm-console-relay", "src/mvmctl/services/console_relay/process.py"),
    ("mvm-nocloud-server", "src/mvmctl/services/nocloud_server/process.py"),
    ("mvm-provision", "src/mvmctl/services/loopmount/process.py"),
]

SERVICE_NAMES: set[str] = {name for name, _ in SERVICES}

# ── Service flag sets ───────────────────────────────────────────────────────

# Release mode — aggressive tree-shaking, minimal size.
SERVICE_FLAGS: list[str] = [
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

# ── Main binary flags ───────────────────────────────────────────────────

MAIN_FLAGS: list[str] = [
    "--onefile",
    f"--output-dir={PROJECT_DIR / 'dist'}",
    "--output-filename=mvm",
    "--include-package=mvmctl",
    f"--include-data-dir={PROJECT_DIR / 'src' / 'mvmctl' / 'assets'}=mvmctl/assets",
    f"--include-data-dir={SERVICES_DIR}=mvmctl/services",
    f"--include-data-dir={PROJECT_DIR / 'src' / 'mvmctl' / 'db' / 'migrations'}=mvmctl/db/migrations",
    # Safe force-includes — legitimate runtime modules Nuitka can't auto-detect.
    "--include-module=passlib.handlers.bcrypt",
    "--include-module=passlib.handlers.sha2_crypt",
    "--include-package=rich._unicode_data",
    "--include-module=jinja2.tests",
    "--lto=yes",
    "--enable-plugin=anti-bloat",
    "--nofollow-import-to=*.unittest",
    "--nofollow-import-to=*.venv",
    "--deployment",
    "--python-flag=isolated",
    "--python-flag=no_site",
    "--remove-output",
    "--noinclude-default-mode=nofollow",
    "--noinclude-IPython-mode=nofollow",
    "--noinclude-numba-mode=nofollow",
    "--nofollow-import-to=pkg_resources",
    # Avoid --nofollow-import-to=*.tests — fnmatch "*.tests" also matches
    # legitimate runtime modules like jinja2.tests which we force-include.
    "--nofollow-import-to=pytest",
    "--nofollow-import-to=_pytest",
    f"--jobs={NPROC}",
]


# ── Helpers ─────────────────────────────────────────────────────────────────
def _get_git_short_sha() -> str | None:
    """Return the short git commit hash, or None if not in a git repo."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return None


def _get_pyproject_version() -> str | None:
    """Read version from pyproject.toml."""
    pyproject = PROJECT_DIR / "pyproject.toml"
    if not pyproject.exists():
        return None
    try:
        import tomllib

        data = tomllib.loads(pyproject.read_text())
        return data.get("project", {}).get("version")
    except Exception:
        pass
    return None


def _write_build_version(release: bool) -> str:
    """Write src/mvmctl/_build_version.py with the baked-in version string.

    Always reads the base version from ``pyproject.toml``.
    Without ``--release``: appends ``+git.<short-sha>``.
    With ``--release``: uses the clean pyproject version only.
    """
    base = _get_pyproject_version() or "0.0.0"

    if release:
        version = base
    else:
        sha = _get_git_short_sha()
        version = f"{base}+git.{sha}" if sha else f"{base}+git.0"

    dest = PROJECT_DIR / "src" / "mvmctl" / "_build_version.py"
    dest.write_text(
        f'"""Build-time version — auto-generated by build_services.py."""\nBUILD_VERSION = {version!r}\n'
    )
    return version


def _clean_build_version() -> None:
    """Remove the auto-generated _build_version.py."""
    dest = PROJECT_DIR / "src" / "mvmctl" / "_build_version.py"
    dest.unlink(missing_ok=True)


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


def _run_nuitka(args: list[str], logfile: Path) -> int:
    """Execute Nuitka with *args* and capture output to *logfile*."""
    logfile.parent.mkdir(parents=True, exist_ok=True)
    cmd = [sys.executable, "-m", "nuitka", *args]
    env: dict[str, str] | None = dict(os.environ)
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
def _build_all_services() -> bool:
    """Build a single multidist binary with all service entry points."""
    timer = Timer()
    print_info("Building all service binaries (multidist)...")
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

    flags = list(SERVICE_FLAGS)
    if _has_static_libpython():
        flags.append("--static-libpython=yes")
    else:
        print_warn(
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
    rc = _run_nuitka(args, logfile)

    # Clean up symlinks
    shutil.rmtree(SYMLINKS_DIR, ignore_errors=True)

    if rc == 0:
        print_success(f"mvm-services built successfully ({timer.elapsed}s)")
        return True
    print_fail(f"mvm-services build failed (see {logfile})")
    _print_last_lines(logfile, 20)
    return False


def _build_single_service(name: str, source: str) -> bool:
    """Build a single service binary."""
    timer = Timer()
    print_info(f"Building service {name}...")
    flags = list(SERVICE_FLAGS)
    if _has_static_libpython():
        flags.append("--static-libpython=yes")
    else:
        print_warn(
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
    rc = _run_nuitka(args, logfile)
    if rc == 0:
        print_success(f"{name} built successfully ({timer.elapsed}s)")
        return True
    print_fail(f"{name} build failed (see {logfile})")
    _print_last_lines(logfile, 20)
    return False


def build_services(names: list[str] | None = None) -> bool:
    """Build service binaries.

    If *names* is ``None``, build all services as a single multidist binary.
    If *names* is provided, build each named service individually.
    """
    if names is None:
        return _build_all_services()

    success = True
    for name in names:
        source = next(
            (src for svc_name, src in SERVICES if svc_name == name),
            None,
        )
        if source is None:
            print_fail(f"Unknown service: {name}")
            return False
        success = _build_single_service(name, source) and success

    return success


def build_main() -> bool:
    """Build the main ``mvm`` binary."""
    timer = Timer()
    print_info("Building main mvm binary...")

    # Ensure combined service binary exists before building main
    if not (SERVICES_DIR / "mvm-services").exists():
        print_warn(
            "Combined service binary mvm-services not found — building services first"
        )
        if not build_services():
            return False

    flags = list(MAIN_FLAGS)

    if _has_static_libpython():
        flags.append("--static-libpython=yes")
    else:
        print_warn(
            "Static libpython not available — using dynamic linking. "
            "For maximum optimization, build with a standard Python "
            "(e.g. pyenv/system Python) instead of a standalone distribution."
        )
    logfile = PROJECT_DIR / "dist" / "mvm.build.log"
    args: list[str] = [
        *flags,
        str(PROJECT_DIR / "src" / "mvmctl" / "main.py"),
    ]
    rc = _run_nuitka(args, logfile)
    if rc == 0:
        print_success(f"Main binary built at dist/mvm ({timer.elapsed}s)")
        return True
    print_fail(f"Main binary build failed (see {logfile})")
    _print_last_lines(logfile, 30)
    return False


# ── Entry point ────────────────────────────────────────────────────────────
def main() -> None:
    timer = Timer()
    parser = argparse.ArgumentParser(
        description="Build mvmctl service and main binaries",
    )

    # Target flags (which binaries to build)
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
        "--release",
        action="store_true",
        help="Use clean version from pyproject.toml instead of git SHA",
    )
    args = parser.parse_args()

    # Bake the version string into the source so it's compiled into the binary
    build_version = _write_build_version(release=args.release)
    success = True
    try:
        # Validate service names
        if args.service:
            for name in args.service:
                if name not in SERVICE_NAMES:
                    print_fail(
                        f"Unknown service: {name!r}. "
                        f"Valid: {', '.join(sorted(SERVICE_NAMES))}"
                    )
                    sys.exit(1)

        # Determine targets: if none specified, build everything
        has_target = args.services or args.service
        build_all_services = args.services or not has_target
        build_specific_services = (
            args.service if (args.service and not build_all_services) else []
        )
        build_main_binary = not has_target

        print_info(f"Version: {build_version}")

        if build_all_services:
            success = build_services() and success
        if build_specific_services:
            success = build_services(names=build_specific_services) and success
        if build_main_binary:
            success = build_main() and success

        if success:
            print_success(f"Build complete ({timer.elapsed}s)")
    finally:
        _clean_build_version()
    sys.exit(0 if success else 1)


if __name__ == "__main__":
    main()
