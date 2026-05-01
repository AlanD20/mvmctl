"""System test fixtures and utilities.

System tests are black-box integration tests that invoke mvm via subprocess.
NO imports from mvmctl.* — tests must work against the actual CLI.
"""

import json
import os
import re
import shlex
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Any, Generator, Optional

import pytest

# ============================================================================
# Session-scoped fixtures (expensive setup, run once per session)
# ============================================================================


@pytest.fixture(scope="session", autouse=True)
def _verify_system_test_iptables() -> None:
    """Verify MVM iptables chains exist without modifying them.

    System tests require that 'mvm host init' has been run so that the
    MVM iptables chains exist. We intentionally do NOT flush them here
    because that would break the default network's NAT rules. Individual
    tests are responsible for cleaning up their own networks.
    """
    for chain in ("MVM-POSTROUTING", "MVM-FORWARD", "MVM-NOCLOUDNET-INPUT"):
        table = "-t nat" if chain == "MVM-POSTROUTING" else ""
        result = subprocess.run(
            ["sudo", "iptables", *table.split(), "-L", chain],
            capture_output=True,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip(
                f"iptables chain '{chain}' not found. "
                "Run 'sudo mvm host init' first."
            )


@pytest.fixture(scope="session")
def mvm_binary() -> str:
    """Resolve MVM binary path from env var or default."""
    binary = os.environ.get("MVM_BINARY", "uv run mvm")

    # Verify binary works
    result = subprocess.run(
        [*shlex.split(binary), "--version"],
        capture_output=True,
        text=True,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if result.returncode != 0:
        pytest.skip(f"MVM binary not functional: {binary}")

    return binary


@pytest.fixture(scope="session")
def check_system_prerequisites() -> None:
    """Verify system can run real VM tests.

    Fails fast with clear message if prerequisites not met.
    """
    # Check KVM
    if not Path("/dev/kvm").exists():
        pytest.skip("System tests require /dev/kvm (KVM not available)")

    # Check mvm group membership
    import grp

    try:
        mvm_group = grp.getgrnam("mvm")
        if mvm_group.gr_gid not in os.getgroups() and os.getgid() != 0:
            pytest.skip("User not in 'mvm' group (run 'mvm host init' first)")
    except KeyError:
        pytest.skip("'mvm' group not found (run 'mvm host init' first)")

    # Check host initialization
    cache_dir = Path.home() / ".cache" / "mvmctl"
    if not (cache_dir / "mvmdb.db").exists():
        pytest.skip("mvmctl not initialized (run 'mvm host init' first)")


@pytest.fixture(scope="session")
def system_cache_dir() -> Path:
    """Real mvmctl cache directory (NOT the isolated tmp_path)."""
    return Path.home() / ".cache" / "mvmctl"


@pytest.fixture(scope="session")
def timing_targets() -> dict[str, float]:
    """Per-image boot timing targets in seconds."""
    return {
        "alpine-3.21": 5.0,
        "ubuntu-24.04-minimal": 10.0,
        "ubuntu-24.04": 20.0,
        "archlinux": 10.0,
        "debian-bookworm": 10.0,
    }


@pytest.fixture(scope="session", autouse=True)
def prepare_system_env(mvm_binary, check_system_prerequisites) -> None:
    """Ensure required assets are cached before tests run.

    Automatically fetches missing kernel, images, and Firecracker binary
    so users don't need to run manual preparation steps.

    Skips gracefully if prerequisites are not met (KVM, group, etc.)
    or if network-dependent operations fail.
    """
    binary = mvm_binary

    # Skip auto-fetches when running under pytest-xdist to avoid races
    # on cold cache (multiple workers downloading the same asset).
    # Users should pre-fetch assets before running in parallel.
    if os.environ.get("PYTEST_XDIST_WORKER"):
        _print_prep(
            "Parallel mode (xdist) — skipping auto-fetch. Pre-fetch assets before running parallel."
        )
        return

    _print_prep("Checking system environment...")

    # ── 1. Verify DB exists (mvmctl initialized) ────────────────────────
    cache_dir = Path.home() / ".cache" / "mvmctl"
    db_path = cache_dir / "mvmdb.db"
    if not db_path.exists():
        _print_prep("mvmctl not initialized. Run 'mvm host init' first.")
        pytest.skip("mvmctl not initialized (run 'mvm host init')")

    # ── 2. Kernel: fetch if missing, set default if none set ──────────
    _print_prep("Checking kernel cache...")
    result = subprocess.run(
        [*shlex.split(binary), "kernel", "ls", "--json"],
        capture_output=True,
        text=True,
        timeout=30,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if result.returncode == 0:
        kernels = json.loads(result.stdout)
        if not kernels:
            _print_prep("No kernel cached. Fetching official kernel...")
            subprocess.run(
                [
                    *shlex.split(binary),
                    "kernel",
                    "fetch",
                    "--type",
                    "official",
                    "--set-default",
                ],
                capture_output=True,
                text=True,
                timeout=300,
                env={**os.environ, "NO_COLOR": "1"},
                check=False,
            )
            _print_prep("Kernel fetch complete.")
        elif not any(k.get("is_default") for k in kernels):
            _print_prep("No default kernel set. Setting first cached kernel...")
            subprocess.run(
                [
                    *shlex.split(binary),
                    "kernel",
                    "set-default",
                    kernels[0]["id"][:6],
                ],
                capture_output=True,
                check=False,
            )

    # ── 3. Images: fetch if missing, set default if none set ──────────
    _print_prep("Checking image cache...")
    result = subprocess.run(
        [*shlex.split(binary), "image", "ls", "--json"],
        capture_output=True,
        text=True,
        timeout=30,
        env={**os.environ, "NO_COLOR": "1"},
    )
    cached_images: list[str] = []
    image_entries: list[dict[str, Any]] = []
    if result.returncode == 0:
        image_entries = json.loads(result.stdout)
        cached_images = [i.get("name", "") for i in image_entries]

    required_images = ["alpine-3.21", "ubuntu-24.04-minimal"]
    for img in required_images:
        if img not in cached_images:
            _print_prep(
                f"Image '{img}' not cached. Fetching (this may take a while)..."
            )
            subprocess.run(
                [*shlex.split(binary), "image", "fetch", img],
                capture_output=True,
                text=True,
                timeout=600,
                env={**os.environ, "NO_COLOR": "1"},
                check=False,
            )
            _print_prep(f"Image '{img}' fetch complete.")

    if image_entries and not any(i.get("is_default") for i in image_entries):
        _print_prep("No default image set. Setting alpine-3.21 as default...")
        subprocess.run(
            [*shlex.split(binary), "image", "set-default", "alpine-3.21"],
            capture_output=True,
            check=False,
        )

    # ── 4. Binary: fetch if missing, set default if none set ──────────
    _print_prep("Checking Firecracker binary cache...")
    result = subprocess.run(
        [*shlex.split(binary), "bin", "ls", "--json"],
        capture_output=True,
        text=True,
        timeout=30,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if result.returncode == 0:
        binaries = json.loads(result.stdout)
        if not binaries:
            _print_prep("No Firecracker binary cached. Fetching latest...")
            remote_result = subprocess.run(
                [*shlex.split(binary), "bin", "ls", "--remote"],
                capture_output=True,
                text=True,
                timeout=30,
                env={**os.environ, "NO_COLOR": "1"},
            )
            if remote_result.returncode == 0:
                versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if versions:
                    target = max(
                        versions,
                        key=lambda v: tuple(int(x) for x in v.split(".")),
                    )
                    _print_prep(
                        f"Fetching Firecracker v{target} (this may take a while)..."
                    )
                    subprocess.run(
                        [
                            *shlex.split(binary),
                            "bin",
                            "fetch",
                            target,
                            "--set-default",
                        ],
                        capture_output=True,
                        text=True,
                        timeout=300,
                        env={**os.environ, "NO_COLOR": "1"},
                        check=False,
                    )
                    _print_prep(f"Firecracker v{target} fetch complete.")
        elif not any(b.get("is_default") for b in binaries):
            _print_prep("No default binary set. Setting first cached binary...")
            subprocess.run(
                [
                    *shlex.split(binary),
                    "bin",
                    "default",
                    binaries[0]["id"][:6],
                ],
                capture_output=True,
                check=False,
            )

    _print_prep("System environment ready.")


def _print_prep(msg: str) -> None:
    """Print a prepare-step message that is visible even with pytest output capture."""
    print(f"[prepare] {msg}", file=sys.stderr, flush=True)


def _unique_subnet(network_name: str) -> str:
    """Generate a deterministic unique /24 subnet from a network name.

    Uses the network name as a seed so the same name always produces
    the same subnet, but different names produce different subnets.
    This avoids collisions when tests run in parallel via pytest-xdist.
    """
    import random

    rng = random.Random(network_name)
    octet2 = rng.randint(1, 254)
    octet3 = rng.randint(0, 254)
    return f"10.{octet2}.{octet3}.0/24"


def _skip_if_parallel() -> None:
    """Skip the current test if running under pytest-xdist.

    Use for tests that mutate shared global state (images, kernels,
    binaries in the shared cache) where parallel workers would race.
    """
    if os.environ.get("PYTEST_XDIST_WORKER"):
        pytest.skip("Test mutates shared global state — not safe under xdist")


# ============================================================================
# Function-scoped autouse fixture (CRITICAL: overrides root conftest)
# ============================================================================


@pytest.fixture(autouse=True)
def _restore_real_dirs(monkeypatch, system_cache_dir) -> None:
    """CRITICAL: Override root conftest env var isolation.

    The root tests/conftest.py has an autouse fixture that redirects
    MVM_CACHE_DIR and MVM_CONFIG_DIR to empty tmp_path directories.
    This breaks system tests because subprocess mvm invocations inherit
    those env vars and can't find cached images/kernels.

    This fixture overrides them back to real paths.
    """
    real_config = Path.home() / ".config" / "mvmctl"

    monkeypatch.setenv("MVM_CACHE_DIR", str(system_cache_dir))
    monkeypatch.setenv("MVM_CONFIG_DIR", str(real_config))
    monkeypatch.setenv("MVM_TEMP_DIR", str(Path("/tmp") / "mvmctl"))
    monkeypatch.setenv("NO_COLOR", "1")  # Prevent ANSI codes in output


# ============================================================================
# Function-scoped fixtures (per test, with guaranteed cleanup)
# ============================================================================


@pytest.fixture
def unique_vm_name() -> str:
    """Generate unique VM name for test isolation."""
    return f"sys-vm-{uuid.uuid4().hex[:8]}"


@pytest.fixture
def unique_network_name() -> str:
    """Generate unique network name for test isolation."""
    return f"sys-net-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def unique_key_name() -> str:
    """Generate unique key name for test isolation."""
    return f"sys-key-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def created_vm(
    mvm_binary, unique_vm_name
) -> Generator[dict[str, Any], None, None]:
    """Create a VM and guarantee cleanup.

    Yields VM info dict from 'mvm vm ls --json'.
    Cleans up VM even if test fails.
    """
    # Create VM (image must be pre-cached or will download)
    _run_mvm(
        mvm_binary,
        "vm",
        "create",
        "--name",
        unique_vm_name,
        "--image",
        "alpine-3.21",
    )

    # Get VM info
    vms = _parse_vm_list(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
    vm_info = next((v for v in vms if v["name"] == unique_vm_name), None)

    if not vm_info:
        raise RuntimeError(f"Failed to find created VM: {unique_vm_name}")

    try:
        yield vm_info
    finally:
        # Guaranteed cleanup
        _run_mvm(
            mvm_binary,
            "vm",
            "rm",
            "--name",
            unique_vm_name,
            "--force",
            check=False,
        )


@pytest.fixture
def created_network(
    mvm_binary, unique_network_name
) -> Generator[str, None, None]:
    """Create a network and guarantee cleanup."""
    subnet = _unique_subnet(unique_network_name)
    _run_mvm(
        mvm_binary,
        "network",
        "create",
        unique_network_name,
        "--subnet",
        subnet,
    )

    try:
        yield unique_network_name
    finally:
        _run_mvm(mvm_binary, "network", "rm", unique_network_name, check=False)


@pytest.fixture
def created_key(mvm_binary, unique_key_name) -> Generator[str, None, None]:
    """Create an SSH key and guarantee cleanup."""
    _run_mvm(
        mvm_binary, "key", "create", unique_key_name, "--algorithm", "ed25519"
    )

    try:
        yield unique_key_name
    finally:
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


# ============================================================================
# Module-scoped fixtures (shared across tests in a module)
# ============================================================================


@pytest.fixture(scope="module")
def lifecycle_vm(mvm_binary) -> Generator[dict[str, Any], None, None]:
    """One VM shared across module for stateful operation tests.

    Used for pause→resume→stop→start→start chain tests.
    VM is created once, goes through state changes, then cleaned up.
    """
    vm_name = f"sys-lifecycle-{uuid.uuid4().hex[:8]}"

    _run_mvm(
        mvm_binary, "vm", "create", "--name", vm_name, "--image", "alpine-3.21"
    )

    vms = _parse_vm_list(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
    vm_info = next((v for v in vms if v["name"] == vm_name), None)

    if not vm_info:
        raise RuntimeError(f"Failed to create lifecycle VM: {vm_name}")

    try:
        yield vm_info
    finally:
        _run_mvm(
            mvm_binary, "vm", "rm", "--name", vm_name, "--force", check=False
        )


# ============================================================================
# Helper functions (not fixtures)
# ============================================================================


def _run_mvm(
    binary: str,
    *args: str,
    check: bool = True,
    timeout: Optional[int] = 300,
) -> subprocess.CompletedProcess[str]:
    """Run mvm command via subprocess.

    Args:
        binary: MVM binary path or "uv run mvm"
        args: Command arguments
        check: Raise on non-zero exit (default True)
        timeout: Command timeout in seconds

    Returns:
        CompletedProcess with stdout/stderr
    """
    cmd = [*shlex.split(binary), *args]

    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )

    if check and result.returncode != 0:
        raise RuntimeError(
            f"mvm command failed: {' '.join(args)}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )

    return result


def _parse_vm_list(json_output: str) -> list[dict[str, Any]]:
    """Parse 'mvm vm ls --json' output."""
    data: list[dict[str, Any]] = json.loads(json_output)
    return data


def wait_for_ssh(
    vm_ip: str,
    user: str,
    timeout: float,
) -> bool:
    """Poll SSH until available or timeout.

    Args:
        vm_ip: VM IP address
        user: SSH username (root for Alpine/Arch/Debian, ubuntu for Ubuntu)
        timeout: Maximum wait time in seconds

    Returns:
        True if SSH available, False if timeout
    """
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        try:
            # Try SSH connection
            result = subprocess.run(
                [
                    "ssh",
                    "-o",
                    "StrictHostKeyChecking=no",
                    "-o",
                    "ConnectTimeout=2",
                    f"{user}@{vm_ip}",
                    "exit",
                ],
                capture_output=True,
                timeout=5,
            )
            if result.returncode == 0:
                return True
        except subprocess.TimeoutExpired:
            pass

        time.sleep(0.5)

    return False
