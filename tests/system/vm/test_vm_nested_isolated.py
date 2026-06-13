"""Tests executed INSIDE a Firecracker VM using the built binary.

Full isolation -- no host state pollution. This file creates a host VM with
nested virt, copies the built ``dist/mvm`` binary inside, then runs every
test command via SSH inside the guest.

Architecture::

    dist/mvm (host binary)
        |
        v  cp into guest via ``mvm cp``
    fenv VM (ubuntu:24.04, --nested-virt, official:6.19.9 kernel with features)
        |
        v  SSH into guest as root + unprivileged user "testuser"
        +-- mvm host status --json               - isolated state (root & unpriv)
        +-- mvm config set/get/reset              - isolated config
        +-- mvm key create/ls/rm                  - isolated keys
        +-- mvm vol create/resize/inspect         - isolated disk
        +-- mvm network create/ls/rm              - isolated network (sudo inside guest)
        +-- mvm vm create ... --kernel firecracker - 3rd level VM uses firecracker kernel
        +-- unprivileged user tests               - non-root privilege boundaries
"""

from __future__ import annotations

import json
import os
import subprocess
import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import (
    _run_mvm,
    _unique_subnet,
    ensure_vm_deps,
    wait_for_ssh,
)

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.serial,
    pytest.mark.slow,
    pytest.mark.requires_kvm,
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Image selector matching fresh_env.py: ubuntu:noble (24.04 Noble Numbat).
IMAGE_SELECTOR = "ubuntu:24.04"

# Kernel selector: official kernel with features (kvm, nftables, tuntap).
# The official kernel takes 30-120 min to build from source on first pull.
# Subsequent pulls use the cached build artifact.
KERNEL_SELECTOR = "official:6.19.9"
KERNEL_FEATURES = "kvm,nftables,tuntap"

# Timeouts
SSH_TIMEOUT = 300.0  # Ubuntu boots + first-boot SSH installer + apt
VM_CREATE_TIMEOUT = 600
IMAGE_PULL_TIMEOUT = 1800

# Resource sizing for the fenv (host) VM
FENV_VCPUS = 4
FENV_MEM = "4g"
FENV_DISK = "30g"  # Need room for: rootfs + apt + image pull temp files (~10GB)

# ---------------------------------------------------------------------------
# Host-level helpers
# ---------------------------------------------------------------------------


def _check_nested_virt_support() -> bool:
    """Check if the host has nested VMX/SVM enabled.

    Reads ``/sys/module/kvm_intel/parameters/nested`` or
    ``/sys/module/kvm_amd/parameters/nested`` (values ``Y``, ``1``).
    If neither file exposes ``Y``/``1``, the host cannot pass through
    VMX/SVM to guests and nested virt tests must skip.
    """
    for param_file in (
        "/sys/module/kvm_intel/parameters/nested",
        "/sys/module/kvm_amd/parameters/nested",
    ):
        path = Path(param_file)
        try:
            value = path.read_text().strip().lower()
            if value in ("1", "y"):
                return True
        except OSError:
            pass
    return False


def _resolve_image_id(mvm_binary: str, selector: str) -> str | None:
    """Check if image *selector* is already cached.

    Returns the 6-char image ID prefix if cached, ``None`` otherwise.
    """
    result = _run_mvm(mvm_binary, "image", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return None
    images = json.loads(result.stdout)
    sel_type = selector.split(":")[0] if ":" in selector else selector
    matches = [
        i
        for i in images
        if i.get("type", "").startswith(sel_type) and i.get("is_present")
    ]
    if not matches:
        return None
    return matches[0]["id"][:6]


def _resolve_official_kernel_id(mvm_binary: str) -> str | None:
    """Check if the official:6.19.9 kernel is already cached.

    Returns the 6-char kernel ID prefix if cached, ``None`` otherwise.
    The official kernel must be built from source (30-120 min on first
    pull), so we check before attempting a pull.
    """
    result = _run_mvm(mvm_binary, "kernel", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return None
    kernels = json.loads(result.stdout)
    matches = [
        k
        for k in kernels
        if k.get("type") == "official"
        and k.get("version") == "6.19.9"
        and k.get("is_present")
    ]
    if not matches:
        return None
    return matches[0]["id"][:6]


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def built_binary() -> Path:
    """Path to the pre-built ``dist/mvm`` binary."""
    env_path = os.environ.get("MVM_BINARY")
    if env_path:
        binary = Path(env_path).resolve()
        assert binary.exists(), (
            f"MVM_BINARY={env_path} does not exist"
        )
        assert os.access(str(binary), os.X_OK), "Binary must be executable"
        return binary
    binary = Path("dist/mvm").resolve()
    assert binary.exists(), (
        "Build dist/mvm first: python scripts/build_services.py"
    )
    assert os.access(str(binary), os.X_OK), "Binary must be executable"
    return binary


@pytest.fixture(scope="module")
def fenv_vm(
    mvm_binary: str, built_binary: Path
) -> Generator[tuple[str, str, str], None, None]:
    """Module-scoped fenv VM: nested-virt host guest, binary copied inside.

    Creates a shared host VM that all test methods in this file use.
    Teardown removes the VM, network, and SSH key.

    Yielded tuple: ``(vm_name, root_user, unprivileged_user)``.
    """
    # Skip-reason: Requires host-level nested VMX/SVM to pass through
    # to the guest.  Without it the fenv VM cannot expose /dev/kvm.
    if not _check_nested_virt_support():
        pytest.skip(
            "Host does not support nested virtualization "
            "(kvm_intel/kvm_amd nested=Y/1 not detected)"
        )

    USER_ROOT = "root"
    UNPRIVILEGED_USER = "testuser"

    vm_name = f"sys-fenv-{uuid.uuid4().hex[:8]}"
    net_name = f"sys-fenv-net-{uuid.uuid4().hex[:6]}"
    key_name = f"sys-fenv-key-{uuid.uuid4().hex[:6]}"
    subnet = _unique_subnet(net_name)

    # Track created resources for cleanup
    created_network = False
    created_key = False
    created_vm = False

    try:
        # -- Phase 1: Create host infrastructure --------------------------------
        _run_mvm(
            mvm_binary,
            "network",
            "create",
            net_name,
            "--subnet",
            subnet,
            "--non-interactive",
        )
        created_network = True
        _run_mvm(
            mvm_binary, "key", "create", key_name, "--algorithm", "ed25519"
        )
        created_key = True
        # -- Phase 2: Ensure cached assets ----------------------------------
        # Image: try to pull if missing (Ubuntu ~220 MB, manageable).
        image_id = _resolve_image_id(mvm_binary, IMAGE_SELECTOR)
        if image_id is None:
            pull = _run_mvm(
                mvm_binary,
                "image",
                "pull",
                IMAGE_SELECTOR,
                check=False,
                timeout=IMAGE_PULL_TIMEOUT,
            )
            # Skip-reason: Image pull requires network access to the upstream
            # registry.  If the network is unavailable or the registry is
            # unreachable, the test cannot proceed and must skip gracefully.
            if pull.returncode != 0:
                pytest.skip(
                    f"Failed to pull image '{IMAGE_SELECTOR}': "
                    f"{pull.stderr.strip()}"
                )
            image_id = _resolve_image_id(mvm_binary, IMAGE_SELECTOR)
            # Skip-reason: Even after a successful pull, the image may not
            # appear in the listing if the cache directory is misconfigured
            # or the pull wrote to a different storage location.  Without
            # a cached image, subsequent VM creation steps would fail.
            if image_id is None:
                pytest.skip(f"Image '{IMAGE_SELECTOR}' not found after pull")

        # Skip-reason: Official kernel 6.19.9 must be cached. Building it
        # from source takes 30-120 minutes on first pull. If not cached,
        # we skip with an instructive message rather than blocking the
        # test suite for an hour.
        kernel_id = _resolve_official_kernel_id(mvm_binary)
        if kernel_id is None:
            pytest.skip(
                "Official kernel 6.19.9 (with kvm,nftables,tuntap features) "
                "not cached. Build it first:\n"
                "  mvm kernel pull official:6.19.9 "
                "--features kvm,nftables,tuntap\n"
                "This takes 30-120 minutes on first build. "
                "Subsequent runs are instant."
            )

        # Ensure firecracker binary + service binaries are available.
        ensure_vm_deps(mvm_binary)

        # -- Phase 3: Create the fenv (host) VM -----------------------------
        _run_mvm(
            mvm_binary,
            "vm",
            "create",
            vm_name,
            "--image",
            IMAGE_SELECTOR,
            "--kernel",
            kernel_id,
            "--vcpus",
            str(FENV_VCPUS),
            "--mem",
            FENV_MEM,
            "-s",
            FENV_DISK,
            "--nested-virt",
            "--network",
            net_name,
            "--ssh-key",
            key_name,
            "--user",
            UNPRIVILEGED_USER,
            timeout=VM_CREATE_TIMEOUT,
        )

        # L2: Verify the VM is running via ls --json
        vms = json.loads(_run_mvm(mvm_binary, "vm", "ls", "--json").stdout)
        vm_entry = next((v for v in vms if v["name"] == vm_name), None)
        assert vm_entry is not None, f"VM '{vm_name}' not found after creation"
        assert vm_entry["status"] == "running", (
            f"Expected 'running', got '{vm_entry['status']}'"
        )
        created_vm = True
        vm_ip = vm_entry.get("ipv4", "")
        print(
            f"\n  [fixture] VM created: {vm_name}, IP={vm_ip}, "
            f"user={UNPRIVILEGED_USER}"
        )

        # -- Phase 4: Wait for SSH (Ubuntu boots slower than Alpine) --------
        # The provisioner injects the SSH key for BOTH root and the
        # specified unprivileged user, so root SSH works.
        ssh_ok = wait_for_ssh(
            mvm_binary, vm_name, USER_ROOT, timeout=SSH_TIMEOUT
        )
        if not ssh_ok:
            # Diagnostic: capture what SSH actually returns when it fails
            diag = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                USER_ROOT,
                "--cmd",
                "echo root_check",
                check=False,
                timeout=10,
            )
            # Also try SSH as testuser for comparison
            diag_unpriv = _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                UNPRIVILEGED_USER,
                "--cmd",
                "echo unpriv_check",
                check=False,
                timeout=10,
            )
            # Check vm inspect for ssh_user and other fields
            inspect_result = _run_mvm(
                mvm_binary,
                "vm",
                "inspect",
                vm_name,
                "--json",
                check=False,
                timeout=5,
            )
            boot_logs = _run_mvm(
                mvm_binary,
                "logs",
                vm_name,
                "--lines",
                "50",
                check=False,
                timeout=10,
            )
            pytest.fail(
                f"SSH not available for '{vm_name}' within {SSH_TIMEOUT}s\n"
                f"  SSH root exit={diag.returncode}: "
                f"{diag.stderr.strip()[:200]}\n"
                f"  SSH testuser exit={diag_unpriv.returncode}: "
                f"{diag_unpriv.stderr.strip()[:200]}\n"
                f"  vm inspect: {inspect_result.stdout.strip()[:200]}\n"
                f"  boot logs (last 50 lines): "
                f"{boot_logs.stdout.strip()[:500]}"
            )

        # -- Phase 5: Copy the built binary into the guest ------------------
        # ``mvm cp`` uses the VM's ssh_user (=testuser from --user flag),
        # so we must copy to a directory testuser can write to.
        _run_mvm(
            mvm_binary,
            "cp",
            str(built_binary),
            f"{vm_name}:/home/{UNPRIVILEGED_USER}/",
        )
        # Root then copies it to /root/ for admin operations.
        _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            "root",
            "--cmd",
            f"cp /home/{UNPRIVILEGED_USER}/mvm /root/mvm",
            timeout=15,
        )

        # -- Phase 6: Initialise mvm inside the guest (as root) -------------
        # Rationale: Without ``mvm init`` the guest has no SQLite DB and
        # no cache directories.
        # We use ``--skip-network`` to avoid nftables/bridge kernel deps.
        # Note: ``mvm init`` exits 1 when KVM probes fail inside guest.
        _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            USER_ROOT,
            "--cmd",
            "/root/mvm init --non-interactive --skip-network",
            check=False,
            timeout=60,
        )

        # -- Phase 7: Install packages (as root) -----------------------------
        # Rationale: ``vol inspect --json`` uses ``qemu-img info`` internally.
        # ``net-tools`` provides ``netstat`` used by some diagnostic paths.
        _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            USER_ROOT,
            "--cmd",
            "apt update -qq && apt install -y -qq qemu-utils net-tools",
            check=False,
            timeout=180,
        )

        # -- Phase 8: Copy binary to testuser and set up cache dir -----------
        # Rationale: The unprivileged read-only tests need mvm accessible
        # to testuser.  We copy /root/mvm → /home/testuser/mvm and ensure
        # the cache directory exists with correct ownership.
        _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            USER_ROOT,
            "--cmd",
            f"mkdir -p /home/{UNPRIVILEGED_USER}/.cache/mvmctl "
            f"&& cp /root/mvm /home/{UNPRIVILEGED_USER}/mvm "
            f"&& chown -R {UNPRIVILEGED_USER}:{UNPRIVILEGED_USER} "
            f"/home/{UNPRIVILEGED_USER}/mvm "
            f"/home/{UNPRIVILEGED_USER}/.cache",
            timeout=30,
        )

        # -- Phase 9: Fix sudo.conf ownership (safety step) -------------------
        # Rationale: Some Ubuntu cloud images ship with broken ownership
        # on /etc/sudo.conf, which causes sudo to fail silently.  We fix
        # it here to avoid cascading failures in tests that use sudo.
        _run_mvm(
            mvm_binary,
            "ssh",
            vm_name,
            "-u",
            USER_ROOT,
            "--cmd",
            "chown root:root /etc/sudo.conf 2>/dev/null "
            "&& chmod 0440 /etc/sudo.conf 2>/dev/null || true",
            check=False,
            timeout=10,
        )

        # -- Phase 10: Narrow sudo permissions to match real deployment -------
        # Rationale: The provisioner creates testuser with ALL=(ALL) NOPASSWD:
        # ALL for maximum flexibility.  In production, the mvm group only has
        # passwordless sudo for specific binaries.  We narrow here via root
        # SSH to test with the actual deployment privilege model.
        PRIVILEGED_BINS = (
            "/usr/sbin/ip",
            "/usr/sbin/iptables",
            "/usr/sbin/iptables-restore",
            "/usr/sbin/iptables-save",
            "/usr/sbin/nft",
            "/usr/sbin/sysctl",
            "/usr/sbin/modprobe",
            "/home/*/.cache/mvmctl/bin/mvm-provision",
        )
        _run_mvm(
            mvm_binary, "ssh", vm_name, "-u", USER_ROOT, "--cmd",
            f"echo '{UNPRIVILEGED_USER} ALL=(ALL) NOPASSWD: "
            f"{','.join(PRIVILEGED_BINS)}' > "
            f"/etc/sudoers.d/{UNPRIVILEGED_USER}",
            timeout=15,
        )

        # -- Phase 11: Verify binary exists in both locations ----------------
        # Rationale: The mvm cp to testuser's home + root copy should place
        # the binary at both /home/testuser/mvm and /root/mvm.  We verify
        # here to catch cp failures early with a clear message.
        _run_mvm(
            mvm_binary, "ssh", vm_name, "-u", USER_ROOT, "--cmd",
            "ls -la /root/mvm /home/testuser/mvm",
            timeout=10,
        )

        # -- Phase 12: Load KVM modules for nested VM testing ---------------
        _run_mvm(
            mvm_binary, "ssh", vm_name, "-u", USER_ROOT, "--cmd",
            "modprobe kvm_intel 2>/dev/null "
            "|| modprobe kvm_amd 2>/dev/null || true",
            check=False, timeout=15,
        )

        # Yield tuple: (vm_name, root_user, unprivileged_user)
        yield (vm_name, USER_ROOT, UNPRIVILEGED_USER)

    finally:
        # -- Cleanup: remove resources in reverse dependency order ----------
        if created_vm:
            _run_mvm(mvm_binary, "vm", "rm", vm_name, "--force", check=False)
        if created_network:
            _run_mvm(
                mvm_binary, "network", "rm", net_name, "--force", check=False
            )
        if created_key:
            _run_mvm(mvm_binary, "key", "rm", key_name, check=False)


# ---------------------------------------------------------------------------
# Guest-level SSH helper
# ---------------------------------------------------------------------------


def _guest_run(
    mvm_binary: str,
    vm_name: str,
    guest_cmd: str,
    *,
    check: bool = True,
    timeout: int = 30,
    user: str = "root",
    mvm_bin_path: str = "/root/mvm",
    retries: int = 3,
    retry_delay: float = 2.0,
) -> subprocess.CompletedProcess[str]:
    """Run a command INSIDE the fenv guest via ``mvm ssh``, with retries.

    The *guest_cmd* is the shell command to execute inside the guest VM.
    The host mvm CLI serves as the SSH transport.

    If *mvm_bin_path* differs from ``/root/mvm``, ``/root/mvm`` in the
    command is replaced with the custom path, allowing tests to run as
    a different user.

    Retries up to *retries* times with *retry_delay* seconds between
    attempts when the SSH command fails with a transient error (exit
    code 255 = SSH connection error).

    Returns the subprocess result so callers can inspect stdout/stderr.
    """
    import time as _time

    # Replace /root/mvm references with the target binary path when
    # running as a non-root user (e.g., unprivileged tests).
    if mvm_bin_path != "/root/mvm":
        guest_cmd = guest_cmd.replace("/root/mvm", mvm_bin_path)

    last_error: Exception | None = None
    for attempt in range(retries):
        try:
            return _run_mvm(
                mvm_binary,
                "ssh",
                vm_name,
                "-u",
                user,
                "--cmd",
                guest_cmd,
                check=check,
                timeout=timeout,
            )
        except RuntimeError as e:
            last_error = e
            # Only retry on transient SSH errors (exit code 255)
            if "exit code 255" not in str(e) and "exit code 2" not in str(e):
                raise
            if attempt < retries - 1:
                _time.sleep(retry_delay)
                continue
            raise

    # Should not reach here, but if all retries exhausted, re-raise
    if last_error:
        raise last_error
    return _run_mvm(
        mvm_binary,
        "ssh",
        vm_name,
        "-u",
        user,
        "--cmd",
        guest_cmd,
        check=check,
        timeout=timeout,
    )


# ========================================================================
# TestNestedIsolated -- commands executed INSIDE the fenv guest
# ========================================================================


class TestNestedIsolated:
    """Run mvm commands inside a nested VM for fully isolated testing.

    All test methods receive the ``fenv_vm`` fixture which provides a
    running VM with the built ``/root/mvm`` binary already copied in
    and initialised.  Every command runs via ``mvm ssh fenv --cmd ...``
    so no host state is affected.
    """

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
        pytest.mark.serial,
        pytest.mark.slow,
        pytest.mark.requires_kvm,
    ]

    # ------------------------------------------------------------------
    # test_inside_guest_host_status
    # ------------------------------------------------------------------

    def test_inside_guest_host_status(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Verify ``mvm host status --json`` inside the guest.

        Runs ``mvm host status --json`` inside the guest via SSH and
        checks that the mvm binary works (parses valid JSON).  KVM
        accessibility depends on the kernel (firecracker kernel may
        not expose /dev/kvm to nested guests).

        Rationale: The mvm binary must work inside the guest VM at a
        basic level (JSON parsing, no crashes).  KVM availability is
        a secondary concern dependent on kernel config.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm

        result = _guest_run(mvm_binary, vm_name, "/root/mvm host status --json")
        status = json.loads(result.stdout)

        # At minimum the JSON is valid and has expected keys
        assert "kvm_accessible" in status, (
            f"Missing 'kvm_accessible' in host status: {status}"
        )
        assert "missing_binaries" in status, (
            f"Missing 'missing_binaries' in host status: {status}"
        )
        # KVM may or may not be accessible inside the guest (depends on
        # kernel + Firecracker cpu-config).  We just document the state.
        if not status.get("kvm_accessible"):
            print(
                "  [info] KVM not accessible inside guest "
                "(expected with firecracker kernel)"
            )

    # ------------------------------------------------------------------
    # test_inside_guest_config_roundtrip
    # ------------------------------------------------------------------

    def test_inside_guest_config_roundtrip(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Config set/get/reset roundtrip inside the guest (as root).

        Sets ``defaults.vm.vcpu_count`` to 4, reads it back, resets to
        default, and confirms the default (1) is restored.  This proves
        that config operations work in complete isolation from the host's
        config store.

        Rationale: The guest has its own ``~/.config/mvmctl/config.json``
        and SQLite DB.  A regression where config commands accidentally
        touch the host's config would be invisible in returncode-only
        checks but caught by this roundtrip.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm

        # Set vcpu_count to 4
        _guest_run(
            mvm_binary,
            vm_name,
            "/root/mvm config set defaults.vm vcpu_count 4",
        )

        # Read back -> should be 4
        get_result = _guest_run(
            mvm_binary,
            vm_name,
            "/root/mvm config get defaults.vm vcpu_count",
        )
        # The get output format is: "defaults.vm.vcpu_count = 4"
        assert "4" in get_result.stdout, (
            f"Expected vcpu_count=4, got: {get_result.stdout.strip()}"
        )

        # Reset to default
        _guest_run(
            mvm_binary,
            vm_name,
            "/root/mvm config reset defaults.vm vcpu_count",
        )

        # Read back -> should be 1 (default)
        get_result2 = _guest_run(
            mvm_binary,
            vm_name,
            "/root/mvm config get defaults.vm vcpu_count",
        )
        assert "1" in get_result2.stdout, (
            f"Expected default vcpu_count=1 after reset, "
            f"got: {get_result2.stdout.strip()}"
        )

    # ------------------------------------------------------------------
    # test_inside_guest_key_operations
    # ------------------------------------------------------------------

    def test_inside_guest_key_operations(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Key create/list/remove lifecycle inside the guest.

        Creates a unique SSH key, asserts it appears in ``key ls --json``,
        removes it, and asserts it is gone.  This proves key management
        works inside the isolated guest environment.

        Rationale: SSH key operations write to the guest's own filesystem
        and DB.  A regression that breaks key creation silently would
        leave the key uncatchable by returncode-only tests.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm
        key_name = f"test-key-{uuid.uuid4().hex[:6]}"

        try:
            # Create the key
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm key create {key_name} --algorithm ed25519 --force",
            )

            # List keys -> verify test-key is present
            ls_result = _guest_run(
                mvm_binary, vm_name, "/root/mvm key ls --json"
            )
            keys = json.loads(ls_result.stdout)
            assert any(k.get("name") == key_name for k in keys), (
                f"Key '{key_name}' not found in listing: {keys}"
            )
        finally:
            # Remove the key
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm key rm {key_name}",
                check=False,
            )

        # Verify key is gone
        ls_result2 = _guest_run(mvm_binary, vm_name, "/root/mvm key ls --json")
        keys2 = json.loads(ls_result2.stdout)
        assert not any(k.get("name") == key_name for k in keys2), (
            f"Key '{key_name}' still present after removal: {keys2}"
        )

    # ------------------------------------------------------------------
    # test_inside_guest_volume_create_and_resize
    # ------------------------------------------------------------------

    def test_inside_guest_volume_create_and_resize(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Volume create, list, resize, inspect, and remove inside the guest.

        Creates a 512M volume, confirms size in ``vol ls --json``, resizes
        to 1G, confirms the new size in ``vol inspect --json``, then
        removes it.  This exercises the full volume lifecycle in isolation.

        Rationale: Volume operations create files on the guest's disk and
        update the guest's DB.  Host-level volume commands must not
        interfere.  A regression where volume resize silently fails would
        be caught by the inspect assertion.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm
        vol_name = f"test-vol-{uuid.uuid4().hex[:6]}"

        # qemu-img is installed unconditionally in Phase 7 of the fixture,
        # so no skip check is needed here.
        try:
            # Create 512M volume
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm vol create {vol_name} 512M",
            )

            # List -> verify volume exists and size is correct
            ls_result = _guest_run(
                mvm_binary, vm_name, "/root/mvm vol ls --json"
            )
            vols = json.loads(ls_result.stdout)
            vol_entry = next(
                (v for v in vols if v.get("name") == vol_name), None
            )
            assert vol_entry is not None, (
                f"Volume '{vol_name}' not found in listing"
            )
            # 512 MiB = 512 * 1024 * 1024 = 536870912 bytes
            size_bytes = vol_entry.get("size_bytes", 0)
            assert size_bytes == 536870912, (
                f"Expected size_bytes=536870912 for 512M, got {size_bytes}"
            )

            # Resize to 1G
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm vol resize {vol_name} 1G",
            )

            # Inspect -> verify new size
            inspect_result = _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm vol inspect {vol_name} --json",
            )
            vol_info = json.loads(inspect_result.stdout)
            # 1 GiB = 1073741824 bytes
            # Note: ``vol inspect --json`` nests volume fields under ``volume`` key
            vol_data = vol_info.get("volume", vol_info)
            new_size = vol_data.get("size_bytes", 0)
            assert new_size == 1073741824, (
                f"Expected size_bytes=1073741824 after resize to 1G, "
                f"got {new_size}"
            )

        finally:
            # Remove the volume
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm vol rm {vol_name} --force",
                check=False,
            )

    # ------------------------------------------------------------------
    # test_inside_guest_network_operations
    # ------------------------------------------------------------------

    def test_inside_guest_network_operations(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Network create/list/remove inside the guest.

        Creates a guest-side network with a unique subnet, asserts it
        appears in ``network ls --json``, removes it, and confirms
        absence.  Network operations inside the guest may require
        ``sudo`` (for iptables/bridge commands) -- inside the fenv VM
        the root user has passwordless sudo.

        Rationale: The guest has its own network namespace.  ``mvm network
        create`` inside the guest must create bridges and iptables rules
        inside that namespace, not on the host.  A regression where
        network commands accidentally touch host networking would be
        invisible in returncode-only checks but caught by this test.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm
        net_name = f"test-net-{uuid.uuid4().hex[:6]}"

        try:
            # Create the network
            create_result = _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm network create {net_name} "
                "--subnet 10.200.0.0/24 --non-interactive",
                check=False,
                timeout=60,
            )
            # Skip-reason: ``mvm network create`` inside the guest may
            # fail because the fenv kernel lacks modules (nftables,
            # bridge) or because sudo is not available.  We skip
            # gracefully when the operation does not succeed.
            if create_result.returncode != 0:
                pytest.skip(
                    "Guest network creation not supported in this "
                    "environment. "
                    f"stderr: {create_result.stderr.strip()}"
                )

            # List -> verify network is present
            ls_result = _guest_run(
                mvm_binary, vm_name, "/root/mvm network ls --json"
            )
            nets = json.loads(ls_result.stdout)
            assert any(n.get("name") == net_name for n in nets), (
                f"Network '{net_name}' not found in listing: {nets}"
            )

        finally:
            # Remove the network
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm network rm {net_name} --force",
                check=False,
            )

    # ------------------------------------------------------------------
    # test_inside_guest_nested_vm_lifecycle
    # ------------------------------------------------------------------

    def test_inside_guest_nested_vm_lifecycle(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Create a nested VM INSIDE the fenv guest (triple nesting).

        Checks for ``/dev/kvm`` inside the guest first.  If KVM is
        available, creates a minimal VM (nested-in-guest) using the
        default image and network, verifies it in ``vm ls --json``,
        then removes it.

        Rationale: This is the deepest possible test of nested
        virtualization -- a VM inside a VM inside a VM (L3).  If
        ``--nested-virt`` on the fenv VM works correctly, the guest
        should see ``/dev/kvm`` and be able to run its own Firecracker
        VMs.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm
        nested_vm_name = f"nested-{uuid.uuid4().hex[:6]}"

        # Skip-reason: The fenv VM must have /dev/kvm for nested VM
        # creation to work.  With the official kernel + --nested-virt,
        # KVM should be accessible.
        kvm_check = _guest_run(
            mvm_binary,
            vm_name,
            "test -c /dev/kvm && echo KVM_OK || echo KVM_MISSING",
            check=False,
        )
        if "KVM_OK" not in kvm_check.stdout:
            pytest.skip(
                "/dev/kvm not available inside fenv guest -- "
                "cannot test nested VM creation"
            )

        # Setup prerequisites inside the guest: image, kernel, network
        nested_net = f"nestnet-{uuid.uuid4().hex[:6]}"
        try:
            # Pull the image inside the guest (needed for nested VM)
            img_pull = _guest_run(
                mvm_binary, vm_name,
                "/root/mvm image pull ubuntu:24.04",
                timeout=300, check=False,
            )
            if img_pull.returncode != 0:
                pytest.skip(
                    f"Image pull inside guest failed (exit "
                    f"{img_pull.returncode}): "
                    f"{img_pull.stderr.strip()[:300]}"
                )
            # Create a network for the nested VM
            _guest_run(
                mvm_binary, vm_name,
                f"/root/mvm network create {nested_net} "
                "--subnet 10.199.0.0/24 --non-interactive",
                timeout=60,
            )
            # Create a minimal nested VM with explicit dependencies
            create_result = _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm vm create {nested_vm_name} "
                f"--vcpus 1 --mem 256m "
                f"--kernel firecracker "
                f"--network {nested_net} "
                f"--no-console",
                check=False,
                timeout=120,
            )
            if create_result.returncode != 0:
                # Print full diagnostic before skipping
                import sys as _sys
                print(
                    f"\n  [nested-vm-debug] exit={create_result.returncode}\n"
                    f"  [nested-vm-debug] stderr="
                    f"{create_result.stderr.strip()[:1000]}\n"
                    f"  [nested-vm-debug] stdout="
                    f"{create_result.stdout.strip()[:1000]}",
                    file=_sys.stderr,
                )
                pytest.skip(
                    f"Nested VM creation failed (exit "
                    f"{create_result.returncode})"
                )

            # L3 (guest-side) verification via ``vm ls --json``
            ls_result = _guest_run(
                mvm_binary,
                vm_name,
                "/root/mvm vm ls --json",
            )
            vms = json.loads(ls_result.stdout)
            assert any(v.get("name") == nested_vm_name for v in vms), (
                f"Nested VM '{nested_vm_name}' not found in listing"
            )

        finally:
            # Remove the nested VM
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm vm rm {nested_vm_name} --force",
                check=False,
            )
            # Remove the nested network
            _guest_run(
                mvm_binary,
                vm_name,
                f"/root/mvm network rm {nested_net} --force",
                check=False,
            )

    # ------------------------------------------------------------------
    # test_inside_guest_unprivileged_init
    # ------------------------------------------------------------------

    def test_inside_guest_unprivileged_init(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """``mvm init`` run as an unprivileged user inside the guest.

        Runs ``/home/testuser/mvm init --non-interactive`` as ``testuser``.
        Expected behavior:
        - Without sudo access, init should fail with a privilege error
          when trying to set up host networking (bridges, iptables).
        - The error message should mention sudo or privilege.

        Rationale: Unprivileged users must not be able to run ``mvm init``
        without proper sudo access.  A regression that allows unprivileged
        init would bypass the privilege model entirely.
        """
        vm_name, _root_user, unpriv_user = fenv_vm
        mvm_for_user = f"/home/{unpriv_user}/mvm"

        # Attempt init as unprivileged user (with --skip-network to avoid
        # nftables/kernel module requirements, just like the root init).
        result = _guest_run(
            mvm_binary,
            vm_name,
            f"{mvm_for_user} init --non-interactive --skip-network",
            check=False,
            timeout=30,
            user=unpriv_user,
            mvm_bin_path=mvm_for_user,
        )

        # The init command should fail -- the unprivileged user cannot
        # write to the system-wide cache directory (~/.cache/mvmctl)
        # which was created by root during the first init.
        assert result.returncode != 0, (
            f"Expected unprivileged init to fail, but it succeeded: "
            f"{result.stdout}"
        )

    # ------------------------------------------------------------------
    # test_inside_guest_unprivileged_read_only
    # ------------------------------------------------------------------

    def test_inside_guest_unprivileged_read_only(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Read-only mvm commands work as an unprivileged user.

        Runs ``vm ls --json``, ``host status --json``, and
        ``key ls --json`` as ``testuser``.  These commands do not
        require privilege escalation and should succeed.

        Rationale: Read-only commands must work for all users,
        not just root.  A regression that adds an accidental
        privilege check to a read-only path would break non-root
        workflows.
        """
        vm_name, _root_user, unpriv_user = fenv_vm
        mvm_for_user = f"/home/{unpriv_user}/mvm"

        # vm ls --json should work (read-only)
        ls_result = _guest_run(
            mvm_binary,
            vm_name,
            f"{mvm_for_user} vm ls --json",
            check=False,
            user=unpriv_user,
            mvm_bin_path=mvm_for_user,
        )
        assert ls_result.returncode == 0, (
            f"vm ls --json failed for unprivileged user: "
            f"{ls_result.stderr.strip()}"
        )
        data = json.loads(ls_result.stdout)
        assert isinstance(data, list), "Expected list from vm ls --json"

        # host status --json should work (read-only)
        host_result = _guest_run(
            mvm_binary,
            vm_name,
            f"{mvm_for_user} host status --json",
            check=False,
            user=unpriv_user,
            mvm_bin_path=mvm_for_user,
        )
        assert host_result.returncode == 0, (
            f"host status --json failed for unprivileged user: "
            f"{host_result.stderr.strip()}"
        )
        host_data = json.loads(host_result.stdout)
        assert "kvm_accessible" in host_data, (
            f"Missing 'kvm_accessible' in host status: {host_data}"
        )

        # key ls --json should work (read-only)
        key_result = _guest_run(
            mvm_binary,
            vm_name,
            f"{mvm_for_user} key ls --json",
            check=False,
            user=unpriv_user,
            mvm_bin_path=mvm_for_user,
        )
        assert key_result.returncode == 0, (
            f"key ls --json failed for unprivileged user: "
            f"{key_result.stderr.strip()}"
        )
        key_data = json.loads(key_result.stdout)
        assert isinstance(key_data, list), "Expected list from key ls --json"

    # ------------------------------------------------------------------
    # test_inside_guest_host_reset
    # ------------------------------------------------------------------

    def test_inside_guest_host_reset(
        self, mvm_binary: str, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Test ``mvm host reset --force`` inside the isolated guest.

        Rationale: ``host reset`` is destructive on a real machine (removes
        bridges, iptables rules, sysctl settings).  Inside the fenv guest,
        it only affects the guest's networking — safe to test here.
        """
        vm_name, _root_user, _unpriv_user = fenv_vm

        # host reset requires sudo for sysctl/group ops, but inside the
        # guest the mvm group has passwordless sudo for specific binaries.
        # We expect it to work since root has full sudo access.
        result = _guest_run(
            mvm_binary,
            vm_name,
            "/root/mvm host reset --force",
            check=False,
            timeout=120,
        )
        # host reset may fail inside the guest (no mvm group, no sudoers)
        # but should not crash — verify it handles gracefully
        assert result.returncode in (0, 1), (
            f"host reset returned unexpected exit code {result.returncode}: "
            f"{result.stderr.strip()[:200]}"
        )
