"""System test fixtures and utilities.

System tests are black-box integration tests that invoke mvm via subprocess.
NO imports from mvmctl.* — tests must work against the actual CLI.
"""

import json
import os
import re
import subprocess
import sys
import time
import uuid
from pathlib import Path
from typing import Generator, Optional

import pytest

# ============================================================================
# Session-scoped fixtures (expensive setup, run once per session)
# ============================================================================


@pytest.fixture(autouse=True)
def _cleanup_system_test_iptables(request) -> None:
    """Pre-clean iptables for system tests to ensure clean state.

    System tests run real iptables commands and skip the root conftest
    _isolate_iptables_rules fixture. This ensures no stale rules exist
    before each system test runs.
    """
    if not request.node.get_closest_marker("system"):
        return

    # Pre-test cleanup: Flush all MVM chains (ignore errors if chains don't exist)
    subprocess.run(
        ["sudo", "iptables", "-t", "nat", "-F", "MVM-POSTROUTING"],
        capture_output=True,
        check=False,
    )
    subprocess.run(
        ["sudo", "iptables", "-F", "MVM-FORWARD"],
        capture_output=True,
        check=False,
    )
    subprocess.run(
        ["sudo", "iptables", "-F", "MVM-NOCLOUD-INPUT"],
        capture_output=True,
        check=False,
    )


@pytest.fixture(scope="session")
def mvm_binary() -> str:
    """Resolve MVM binary path from env var or default."""
    binary = os.environ.get("MVM_BINARY", "uv run mvm")

    # Verify binary works
    result = subprocess.run(
        [*binary.split(), "--version"],
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
        if os.getgid() not in [mvm_group.gr_gid, 0]:
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
    _print_prep("Checking system environment...")

    # ── 1. Verify DB exists (mvmctl initialized) ────────────────────────
    cache_dir = Path.home() / ".cache" / "mvmctl"
    db_path = cache_dir / "mvmdb.db"
    if not db_path.exists():
        _print_prep(
            "mvmctl not initialized. Run 'mvm init --non-interactive' first."
        )
        pytest.skip("mvmctl not initialized (run 'mvm init --non-interactive')")

    # ── 2. Fetch official kernel if not cached ─────────────────────────
    _print_prep("Checking kernel cache...")
    result = subprocess.run(
        [*binary.split(), "kernel", "ls", "--json"],
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
                    *binary.split(),
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

    # ── 3. Fetch required images if not cached ─────────────────────────
    _print_prep("Checking image cache...")
    result = subprocess.run(
        [*binary.split(), "image", "ls", "--json"],
        capture_output=True,
        text=True,
        timeout=30,
        env={**os.environ, "NO_COLOR": "1"},
    )
    cached_images: list[str] = []
    if result.returncode == 0:
        cached_images = [i.get("name", "") for i in json.loads(result.stdout)]

    required_images = ["alpine-3.21", "ubuntu-24.04-minimal"]
    for img in required_images:
        if img not in cached_images:
            _print_prep(
                f"Image '{img}' not cached. Fetching (this may take a while)..."
            )
            subprocess.run(
                [*binary.split(), "image", "fetch", img],
                capture_output=True,
                text=True,
                timeout=600,
                env={**os.environ, "NO_COLOR": "1"},
                check=False,
            )
            _print_prep(f"Image '{img}' fetch complete.")

    # ── 4. Fetch Firecracker binary if none cached ────────────────────
    _print_prep("Checking Firecracker binary cache...")
    result = subprocess.run(
        [*binary.split(), "bin", "ls", "--json"],
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
                [*binary.split(), "bin", "ls", "--remote"],
                capture_output=True,
                text=True,
                timeout=30,
                env={**os.environ, "NO_COLOR": "1"},
            )
            if remote_result.returncode == 0:
                versions = re.findall(r"\d+\.\d+\.\d+", remote_result.stdout)
                if versions:
                    target = versions[-1]
                    _print_prep(
                        f"Fetching Firecracker v{target} (this may take a while)..."
                    )
                    subprocess.run(
                        [
                            *binary.split(),
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

    _print_prep("System environment ready.")


def _print_prep(msg: str) -> None:
    """Print a prepare-step message that is visible even with pytest output capture."""
    print(f"[prepare] {msg}", file=sys.stderr, flush=True)


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
def created_vm(mvm_binary, unique_vm_name) -> Generator[dict, None, None]:
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
        _run_mvm(mvm_binary, "vm", "rm", "--name", unique_vm_name, check=False)


@pytest.fixture
def created_network(
    mvm_binary, unique_network_name
) -> Generator[str, None, None]:
    """Create a network and guarantee cleanup."""
    _run_mvm(
        mvm_binary,
        "network",
        "create",
        unique_network_name,
        "--subnet",
        "10.99.0.0/24",
    )

    try:
        yield unique_network_name
    finally:
        _run_mvm(mvm_binary, "network", "rm", unique_network_name, check=False)


@pytest.fixture
def created_key(mvm_binary, unique_key_name) -> Generator[str, None, None]:
    """Create an SSH key and guarantee cleanup."""
    _run_mvm(mvm_binary, "key", "create", unique_key_name, "--type", "ed25519")

    try:
        yield unique_key_name
    finally:
        _run_mvm(mvm_binary, "key", "rm", unique_key_name, check=False)


# ============================================================================
# Module-scoped fixtures (shared across tests in a module)
# ============================================================================


@pytest.fixture(scope="module")
def lifecycle_vm(mvm_binary) -> Generator[dict, None, None]:
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
        _run_mvm(mvm_binary, "vm", "rm", "--name", vm_name, check=False)


# ============================================================================
# Helper functions (not fixtures)
# ============================================================================


def _run_mvm(
    binary: str,
    *args: str,
    check: bool = True,
    timeout: Optional[int] = 300,
) -> subprocess.CompletedProcess:
    """Run mvm command via subprocess.

    Args:
        binary: MVM binary path or "uv run mvm"
        args: Command arguments
        check: Raise on non-zero exit (default True)
        timeout: Command timeout in seconds

    Returns:
        CompletedProcess with stdout/stderr
    """
    cmd = [*binary.split(), *args]

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


def _parse_vm_list(json_output: str) -> list[dict]:
    """Parse 'mvm vm ls --json' output."""
    return json.loads(json_output)


def wait_for_ssh(
    vm_ip: str,
    user: str,
    timeout: float,
    key_path: Optional[Path] = None,
) -> bool:
    """Poll SSH until available or timeout.

    Args:
        vm_ip: VM IP address
        user: SSH username (root for Alpine/Arch/Debian, ubuntu for Ubuntu)
        timeout: Maximum wait time in seconds
        key_path: Path to SSH private key (optional)

    Returns:
        True if SSH available, False if timeout
    """
    import socket

    start = time.monotonic()
    while time.monotonic() - start < timeout:
        try:
            # Try SSH connection
            cmd = [
                "ssh",
                "-o",
                "StrictHostKeyChecking=no",
                "-o",
                "ConnectTimeout=2",
            ]
            if key_path:
                cmd.extend(["-i", str(key_path)])
            cmd.extend([f"{user}@{vm_ip}", "exit"])

            result = subprocess.run(cmd, capture_output=True, timeout=5)
            if result.returncode == 0:
                return True
        except (subprocess.TimeoutExpired, socket.error):
            pass

        time.sleep(0.5)

    return False
