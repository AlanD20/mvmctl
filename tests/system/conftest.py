"""System test helpers — commands run INSIDE the test VM.

All tests run inside a test VM via ``mvm vm exec -- python3 -m pytest``.
The orchestrator (``scripts/run-system-tests.py``) sets ``MVM_TEST_VM`` to
the VM name, and tests call ``mvm`` directly through ``_run_mvm()``.
The ``vm_name`` parameter is accepted for API compatibility but ignored —
we are already inside the target VM.
"""

from __future__ import annotations

import json
import os
import random
import shlex
import subprocess
import time
import uuid
from typing import Any, Generator

import pytest


# ============================================================================
# Runner VM name (from orchestrator)
# ============================================================================


@pytest.fixture(scope="session")
def runner_vm() -> str:
    """Return the test VM name, set by orchestrator via MVM_TEST_VM env var."""
    return os.environ.get("MVM_TEST_VM", "t1-base")


# ============================================================================
# _guest_run: run a shell command directly inside the VM
# ============================================================================


def _guest_run(
    vm_name: str,
    guest_cmd: str,
    *,
    check: bool = True,
    timeout: int = 60,
) -> subprocess.CompletedProcess[str]:
    """Run a shell command. vm_name is ignored — we're already inside the test VM."""
    cmd = ["sh", "-c", guest_cmd]
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout + 30,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"command failed:\n"
            f"  cmd: {guest_cmd}\n"
            f"  rc: {result.returncode}\n"
            f"  stdout: {result.stdout}\n"
            f"  stderr: {result.stderr}"
        )
    return result


# ============================================================================
# _run_mvm: run mvm commands directly inside the test VM
# ============================================================================


def _run_mvm(
    vm_name: str, *args: str, check: bool = True, timeout: int = 60
) -> subprocess.CompletedProcess[str]:
    """Run an mvm command. vm_name is ignored — we're already inside the test VM."""
    cmd = ["mvm", *args]
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout + 30,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"mvm command failed:\n"
            f"  args: {' '.join(args)}\n"
            f"  rc: {result.returncode}\n"
            f"  stdout: {result.stdout}\n"
            f"  stderr: {result.stderr}"
        )
    return result


# ============================================================================
# Unique name fixtures
# ============================================================================


@pytest.fixture
def unique_vm_name() -> str:
    return f"sys-vm-{uuid.uuid4().hex[:8]}"


@pytest.fixture
def unique_network_name() -> str:
    return f"sys-net-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def unique_key_name() -> str:
    return f"sys-key-{uuid.uuid4().hex[:6]}"


@pytest.fixture
def unique_volume_name() -> str:
    return f"sys-vol-{uuid.uuid4().hex[:6]}"


# ============================================================================
# Utility functions
# ============================================================================


def _unique_subnet(network_name: str) -> str:
    """Deterministically generate a unique subnet from a network name."""
    rng = random.Random(network_name)
    return f"10.{rng.randint(1, 254)}.{rng.randint(0, 254)}.0/24"


def _parse_vm_list(json_output: str) -> list[dict[str, Any]]:
    return json.loads(json_output) if json_output else []


# ============================================================================
# Asset pre-seeding (Tier 2 only — idempotent)
# ============================================================================


def _print_prep(msg: str) -> None:
    print(f"[prepare] {msg}", file=__import__('sys').stderr, flush=True)


def _ensure_kernel(vm_name: str) -> None:
    """Ensure default kernel is registered."""
    r = _run_mvm(vm_name, "kernel", "ls", "--json", timeout=30, check=False)
    if r.returncode != 0 or not r.stdout.strip():
        _run_mvm(vm_name, "kernel", "pull", "--type", "firecracker",
                 "--version", "v1.15", "--default", timeout=300)
        return
    kernels = json.loads(r.stdout)
    if not any(k.get("is_default") and k.get("is_present") for k in kernels):
        present = [k for k in kernels if k.get("is_present")]
        if present:
            _run_mvm(vm_name, "kernel", "default", present[0]["id"][:6], timeout=30)
        else:
            _run_mvm(vm_name, "kernel", "pull", "--type", "firecracker",
                     "--version", "v1.15", "--default", timeout=300)


def _ensure_image(vm_name: str, image: str = "alpine:3.23") -> None:
    """Ensure an image is registered."""
    img_type = image.split(":")[0] if ":" in image else image
    r = _run_mvm(vm_name, "image", "ls", "--json", timeout=30, check=False)
    if r.returncode != 0 or not r.stdout.strip():
        pull_args = ["image", "pull", img_type]
        if ":" in image:
            pull_args.extend(["--version", image.split(":")[1]])
        _run_mvm(vm_name, *pull_args, timeout=300)
        return
    cached = json.loads(r.stdout) if r.stdout else []
    matching = [
        i for i in cached
        if i.get("type", "").startswith(img_type) and i.get("is_present")
    ]
    if not matching:
        pull_args = ["image", "pull", img_type]
        if ":" in image:
            pull_args.extend(["--version", image.split(":")[1]])
        pull_args.extend(["--skip-optimization"])
        _run_mvm(vm_name, *pull_args, timeout=300)


def _ensure_binary(vm_name: str) -> None:
    """Ensure firecracker binary is registered."""
    r = _run_mvm(vm_name, "bin", "ls", "--json", timeout=30, check=False)
    has = False
    if r.returncode == 0 and r.stdout.strip():
        has = any(
            b.get("type") == "firecracker" and b.get("is_present")
            for b in json.loads(r.stdout)
        )
    if not has:
        _run_mvm(vm_name, "bin", "pull", "firecracker", "--version", "1.15.0",
                 "--default", timeout=300)


def ensure_vm_deps(vm_name: str) -> None:
    """Ensure kernel, alpine image, firecracker binary inside the test VM.

    This is used by Tier 2 domains (need nested VMs). Idempotent.

    NOTE: _ensure_binary must be called BEFORE _ensure_image because
    ``image pull`` requires a default firecracker binary (to resolve CI
    version). If the binary hasn't been pulled yet, the image pull
    will fail with "No firecracker binary is installed".
    """
    _run_mvm(vm_name, "config", "set", "settings", "guestfs_enabled",
             "false", timeout=10, check=False)
    _ensure_binary(vm_name)   # must come before _ensure_image
    _ensure_kernel(vm_name)
    _ensure_image(vm_name)


# ============================================================================
# VM creation helpers (Tier 2)
# ============================================================================


def _cleanup_stale_processes(vm_name: str) -> None:
    """Kill stale firecracker/jailer processes inside the test VM."""
    for name in ("firecracker", "jailer"):
        _guest_run(vm_name,
                 f"kill -9 $(pgrep {name}) 2>/dev/null; true",
                 timeout=5, check=False)


def create_vm_core(
    vm_name: str,
    net_name: str,
    *,
    ssh_key_name: str | None = None,
    image: str = "alpine:3.23",
    extra_args: list[str] | None = None,
) -> dict[str, Any]:
    """Core VM creation inside the test VM.

    Ensures deps, creates network, returns VM info from JSON.
    """
    rv = os.environ.get("MVM_TEST_VM", "t1-base")
    ensure_vm_deps(rv)

    subnet = _unique_subnet(net_name)

    # Clean stale bridges inside test VM
    _guest_run(rv,
             'for br in $(ip -o link show 2>/dev/null | grep -o "mvm-[^:@]*"); '
             'do ip link delete "$br" 2>/dev/null; done',
             timeout=10, check=False)

    _run_mvm(rv, "network", "create", net_name, "--subnet", subnet,
             "--non-interactive", timeout=30)

    cmd_args: list[str] = [
        "vm", "create", vm_name, "--image", image,
        "--network", net_name, ]
    if ssh_key_name:
        cmd_args.extend(["--ssh-key", ssh_key_name])
    if extra_args:
        cmd_args.extend(extra_args)
    _run_mvm(rv, *cmd_args, timeout=180)

    ls_result = _run_mvm(rv, "vm", "ls", "--json", timeout=30)
    vms: list[dict[str, Any]] = json.loads(ls_result.stdout)
    vm_info = next((v for v in vms if v["name"] == vm_name), None)
    if not vm_info:
        raise RuntimeError(f"Failed to find created VM: {vm_name}")
    return vm_info


def cleanup_vm_resources(
    vm_name: str, net_name: str, key_name: str | None = None
) -> None:
    """Clean up VM, network, and optional key inside the test VM."""
    rv = os.environ.get("MVM_TEST_VM", "t1-base")
    for _ in range(2):
        _run_mvm(rv, "vm", "rm", vm_name, "--force", timeout=120, check=False)
    for _ in range(3):
        _run_mvm(rv, "network", "rm", net_name, "--force", timeout=60, check=False)
    if key_name:
        _run_mvm(rv, "key", "rm", key_name, check=False)


@pytest.fixture
def minimal_vm(
    runner_vm, unique_vm_name, unique_network_name
) -> Generator[dict[str, Any], None, None]:
    """Bare VM: no SSH, no console. Fastest creation. Self-contained deps."""
    vm_name = unique_vm_name
    net_name = unique_network_name
    vm_info = create_vm_core(vm_name, net_name)
    try:
        yield vm_info
    finally:
        cleanup_vm_resources(vm_name, net_name)


@pytest.fixture
def created_vm(
    runner_vm, unique_vm_name
) -> Generator[dict[str, Any], None, None]:
    """VM with SSH key injected. Self-contained deps. No console."""
    vm_name = unique_vm_name
    key_name = f"sys-vmkey-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-vm-net-{uuid.uuid4().hex[:6]}"

    _run_mvm(runner_vm, "key", "create", key_name, "--algorithm",
             "ed25519", timeout=30)
    _run_mvm(runner_vm, "key", "default", key_name, check=False, timeout=10)

    _ensure_image(runner_vm, "ubuntu:24.04")
    vm_info = create_vm_core(vm_name, net_name, ssh_key_name=key_name, image="ubuntu:24.04",
                             extra_args=["--user", "runner"])
    wait_for_ssh(runner_vm, vm_name, "root", timeout=120)
    try:
        yield vm_info
    finally:
        cleanup_vm_resources(vm_name, net_name, key_name)


@pytest.fixture(scope="module")
def module_vm(runner_vm) -> Generator[dict[str, Any], None, None]:
    """Module-scoped VM with SSH key. Shared across a module for read-only tests."""
    vm_name = f"sys-modvm-{uuid.uuid4().hex[:8]}"
    key_name = f"sys-modvm-key-{uuid.uuid4().hex[:6]}"
    net_name = f"sys-modvm-net-{uuid.uuid4().hex[:6]}"

    _run_mvm(runner_vm, "key", "create", key_name, "--algorithm",
             "ed25519", timeout=30)
    _ensure_image(runner_vm, "ubuntu:24.04")
    vm_info = create_vm_core(vm_name, net_name, ssh_key_name=key_name, image="ubuntu:24.04",
                             extra_args=["--user", "runner"])
    wait_for_ssh(runner_vm, vm_name, "root", timeout=120)
    try:
        yield vm_info
    finally:
        cleanup_vm_resources(vm_name, net_name, key_name)


@pytest.fixture(scope="module")
def module_network(runner_vm) -> Generator[str, None, None]:
    """Module-scoped network for read-only tests."""
    name = f"sys-modnet-{uuid.uuid4().hex[:6]}"
    subnet = _unique_subnet(name)
    _run_mvm(runner_vm, "network", "create", name, "--subnet", subnet,
             "--non-interactive", timeout=30)
    try:
        yield name
    finally:
        _run_mvm(runner_vm, "network", "rm", name, check=False)
        result = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        if result.returncode == 0:
            nets = json.loads(result.stdout)
            if not any(n.get("is_default") for n in nets) and nets:
                _run_mvm(runner_vm, "network", "default", nets[0]["name"],
                         check=False)


@pytest.fixture
def created_network(
    runner_vm, unique_network_name
) -> Generator[str, None, None]:
    """Create a network inside the test VM and guarantee cleanup."""
    subnet = _unique_subnet(unique_network_name)
    _run_mvm(runner_vm, "network", "create", unique_network_name, "--subnet",
             subnet, "--non-interactive", timeout=30)
    try:
        yield unique_network_name
    finally:
        _run_mvm(runner_vm, "network", "rm", unique_network_name, check=False)
        result = _run_mvm(runner_vm, "network", "ls", "--json", check=False)
        if result.returncode == 0:
            nets = json.loads(result.stdout)
            if not any(n.get("is_default") for n in nets) and nets:
                _run_mvm(runner_vm, "network", "default", nets[0]["name"],
                         check=False)


@pytest.fixture
def created_key(runner_vm, unique_key_name) -> Generator[str, None, None]:
    """Create an SSH key inside the test VM and guarantee cleanup."""
    _run_mvm(runner_vm, "key", "create", unique_key_name, "--algorithm",
             "ed25519", timeout=30)
    try:
        yield unique_key_name
    finally:
        _run_mvm(runner_vm, "key", "rm", unique_key_name, check=False)


# ============================================================================
# SSH wait helper
# ============================================================================


def wait_for_ssh(
    vm_name: str, test_vm_name: str, user: str = "root", timeout: float = 60.0
) -> bool:
    """Poll SSH via mvm ssh until available or timeout."""
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        try:
            result = _run_mvm(
                vm_name, "ssh", test_vm_name, "-u", user, "--cmd", "exit",
                check=False, timeout=10,
            )
            if result.returncode == 0:
                return True
        except subprocess.TimeoutExpired:
            pass
        time.sleep(0.5)
    return False


# ============================================================================
# Timing targets
# ============================================================================


@pytest.fixture(scope="session")
def timing_targets() -> dict[str, float]:
    return {
        "alpine:3.23": 10.0,
        "ubuntu:24.04": 30.0,
        "ubuntu-24.04": 30.0,
        "archlinux": 10.0,
        "debian-bookworm": 10.0,
    }


# ============================================================================
# Host-level helpers (for Tier 3 tests that run directly on the host)
# ============================================================================


def _run_mvm_host(
    *args: str, check: bool = True, timeout: int = 60
) -> subprocess.CompletedProcess[str]:
    """Run mvm command on the HOST (not inside a test VM).

    Use this for tests that must execute directly on the host machine,
    such as Category 4 (triple-nested → host-run) tests that create
    a VM on the host and then SSH into it.

    Example: _run_mvm_host("vm", "create", "myvm", "--image", "ubuntu:24.04")
        -> runs ``mvm vm create myvm --image ubuntu:24.04`` on the host.
    """
    mvm_binary = os.environ.get("MVM_BINARY", "mvm")
    cmd = [*shlex.split(mvm_binary), *args]
    result = subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
        env={**os.environ, "NO_COLOR": "1"},
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"mvm host command failed: {' '.join(args)}\n"
            f"stdout: {result.stdout}\n"
            f"stderr: {result.stderr}"
        )
    return result
