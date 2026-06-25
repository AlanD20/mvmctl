"""Tests executed DIRECTLY ON THE HOST using a single fenv VM.

No runner VM -- this test creates a Firecracker VM on the host directly
(double-nested: host -> fenv VM -> nested VM), then runs every test command
via SSH into the fenv VM from the host.

Architecture::

    Host (runs mvm directly)
        |
        v  SSH into fenv VM as root + unprivileged user "testuser"
        +-- mvm host status --json               - isolated state
        +-- mvm config set/get/reset              - isolated config
        +-- mvm key create/ls/rm                  - isolated keys
        +-- mvm vol create/resize/inspect         - isolated disk
        +-- mvm network create/ls/rm              - isolated network
        +-- mvm vm create ... --kernel firecracker - 3rd level VM
        +-- unprivileged user tests               - non-root boundaries
"""

from __future__ import annotations

import json
import os
import subprocess
import uuid
from pathlib import Path
from typing import Generator

import pytest

from tests.system.conftest import _run_mvm_host, _unique_subnet

pytestmark = [
    pytest.mark.system,
    pytest.mark.domain_vm,
    pytest.mark.slow,
    pytest.mark.requires_kvm,
]

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

IMAGE_SELECTOR = "mvm-test-runner"
KERNEL_SELECTOR = "official:7.0.11"
KERNEL_FEATURES = "kvm,nftables,tuntap,btrfs"

SSH_TIMEOUT = 300.0
VM_CREATE_TIMEOUT = 600
IMAGE_PULL_TIMEOUT = 1800

FENV_VCPUS = 4
FENV_MEM = "4g"
FENV_DISK = "30g"

# ---------------------------------------------------------------------------
# Host-level helpers
# ---------------------------------------------------------------------------


def _check_nested_virt_support() -> bool:
    """Check if the HOST has nested VMX/SVM enabled."""
    for module in ("kvm_intel", "kvm_amd"):
        param_file = Path(f"/sys/module/{module}/parameters/nested")
        if param_file.exists():
            value = param_file.read_text().strip().lower()
            if value in ("1", "y"):
                return True
    return False


def _resolve_image_id(selector: str) -> str | None:
    """Check if image *selector* is already cached on the host."""
    result = _run_mvm_host("image", "ls", "--json", check=False)
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


def _resolve_official_kernel_id() -> str | None:
    """Check if the official:7.0.11 kernel is already cached on the host."""
    result = _run_mvm_host("kernel", "ls", "--json", check=False)
    if result.returncode != 0 or not result.stdout.strip():
        return None
    kernels = json.loads(result.stdout)
    matches = [
        k
        for k in kernels
        if k.get("type") == "official"
        and k.get("version") == "7.0.11"
        and k.get("is_present")
    ]
    if not matches:
        return None
    return matches[0]["id"][:6]


def _wait_for_ssh_host(
    vm_name: str, user: str = "root", timeout: float = 300
) -> bool:
    """Poll vsock exec until available or timeout."""
    import time as _time

    start = _time.monotonic()
    while _time.monotonic() - start < timeout:
        try:
            result = _run_mvm_host(
                "vm", "exec", vm_name, "--user", user,
                "--timeout", "10", "--", "exit",
                check=False, timeout=15,
            )
            if result.returncode == 0:
                return True
        except subprocess.TimeoutExpired:
            pass
        _time.sleep(0.5)
    return False


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(scope="session")
def built_binary() -> Path:
    """Path to the pre-built ``dist/mvm`` binary on the HOST.

    This fixture reads environment and host filesystem to locate
    the binary. It is used ONLY for the initial copy into the test
    VM (a one-time bootstrap operation). After that, all commands
    run inside the test VM.
    """
    env_path = os.environ.get("MVM_BINARY")
    if env_path:
        binary = Path(env_path).resolve()
        assert binary.exists(), f"MVM_BINARY={env_path} does not exist"
        assert os.access(str(binary), os.X_OK), "Binary must be executable"
        return binary
    binary = Path("dist/mvm").resolve()
    assert binary.exists(), "Build dist/mvm first: ./scripts/build.sh release"
    assert os.access(str(binary), os.X_OK), "Binary must be executable"
    return binary


@pytest.fixture(scope="module")
def fenv_vm() -> Generator[tuple[str, str, str], None, None]:
    """Module-scoped fenv VM: created directly on host, nested-virt enabled.

    Double-nested architecture: host -> fenv VM -> third-level VM.
    """
    # Phase 0: Check nested virt support on HOST
    nested_ok = _check_nested_virt_support()
    assert nested_ok, (
        "Nested virtualization not supported on host "
        "(kvm_intel/kvm_amd nested=Y/1 not detected)"
    )

    USER_ROOT = "root"
    UNPRIVILEGED_USER = "testuser"

    vm_name = f"sys-fenv-{uuid.uuid4().hex[:8]}"
    net_name = f"sys-fenv-net-{uuid.uuid4().hex[:6]}"
    key_name = f"sys-fenv-key-{uuid.uuid4().hex[:6]}"
    subnet = _unique_subnet(net_name)

    created_network = False
    created_key = False
    created_vm = False

    try:
        # Phase 1: Create host infrastructure
        _run_mvm_host(
            "network", "create", net_name,
            "--subnet", subnet, "--non-interactive",
        )
        created_network = True
        _run_mvm_host("key", "create", key_name, "--algorithm", "ed25519")
        created_key = True

        # Phase 2: Ensure cached assets
        image_id = _resolve_image_id(IMAGE_SELECTOR)
        if image_id is None:
            pull = _run_mvm_host(
                "image", "pull", IMAGE_SELECTOR, "--force",
                check=False, timeout=IMAGE_PULL_TIMEOUT,
            )
            assert pull.returncode == 0, (
                f"Failed to pull image '{IMAGE_SELECTOR}': "
                f"{pull.stderr.strip()}"
            )
            image_id = _resolve_image_id(IMAGE_SELECTOR)
            assert image_id is not None, (
                f"Image '{IMAGE_SELECTOR}' not found after pull"
            )

        kernel_id = _resolve_official_kernel_id()
        if kernel_id is None:
            pull = _run_mvm_host(
                "kernel", "pull", "--type", "official", "--version", "7.0.11",
                "--features", KERNEL_FEATURES,
                check=False, timeout=300,
            )
            assert pull.returncode == 0, (
                f"Failed to pull kernel 'official:7.0.11': "
                f"{pull.stderr.strip()}"
            )
            kernel_id = _resolve_official_kernel_id()
            assert kernel_id is not None, (
                "Official kernel 7.0.11 (with kvm,nftables,tuntap features) "
                "not cached. Build it first:\n"
                "  mvm kernel pull official:7.0.11 "
                "--features kvm,nftables,tuntap"
            )

        # Phase 3: Create the fenv VM with --user testuser.
        # The loopmount provisioner creates testuser + SSH keys + sudo automatically.
        _run_mvm_host(
            "vm", "create", vm_name,
            "--image", IMAGE_SELECTOR,
            "--kernel", kernel_id,
            "--vcpu", str(FENV_VCPUS),
            "--mem", FENV_MEM,
            "-s", FENV_DISK,
            "--nested-virt",
            "--network", net_name,
            "--ssh-key", key_name,
            "--user", UNPRIVILEGED_USER,
            timeout=VM_CREATE_TIMEOUT,
        )

        vms = json.loads(_run_mvm_host("vm", "ls", "--json").stdout)
        vm_entry = next((v for v in vms if v["name"] == vm_name), None)
        assert vm_entry is not None, f"VM '{vm_name}' not found after creation"
        assert vm_entry["status"] == "running", (
            f"VM '{vm_name}' status is '{vm_entry.get('status')}', "
            "expected 'running'"
        )
        created_vm = True
        vm_ip = vm_entry.get("ipv4", "")
        print(
            f"\n  [fixture] VM created: {vm_name}, IP={vm_ip}, "
            f"user={UNPRIVILEGED_USER}"
        )

        # Phase 4: Wait for SSH (from host directly)
        ssh_ok = _wait_for_ssh_host(vm_name, USER_ROOT, timeout=SSH_TIMEOUT)
        assert ssh_ok, f"SSH not available for '{vm_name}' within {SSH_TIMEOUT}s"

        # Phase 5-8: Skipped — mvm-test-runner image has mvm binary,
        # test user, init, and packages pre-installed. The --user testuser
        # flag on vm create above already created testuser with SSH keys + sudo.

        # Phase 9: Narrow sudo permissions for privileged binaries
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
        _run_mvm_host(
            "vm", "exec", vm_name, "--user", USER_ROOT, "--timeout", "30", "--",
            f"echo '{UNPRIVILEGED_USER} ALL=(ALL) NOPASSWD: "
            f"{','.join(PRIVILEGED_BINS)}' > "
            f"/etc/sudoers.d/{UNPRIVILEGED_USER}",
            timeout=15,
        )

        # Phase 10: Apply pending migrations (base image may be behind current binary)
        _run_mvm_host(
            "vm", "exec", vm_name, "--user", USER_ROOT, "--timeout", "30", "--",
            "mvm init --non-interactive",
            check=False, timeout=60,
        )
        # Also init for testuser so unprivileged read-only commands work
        _run_mvm_host(
            "vm", "exec", vm_name, "--user", USER_ROOT, "--timeout", "60", "--",
            f"su - {UNPRIVILEGED_USER} -c 'mvm init --non-interactive --skip-network'",
            check=False, timeout=90,
        )

        # Phase 11: Copy mvm to testuser's home (tests reference /home/testuser/mvm)
        _run_mvm_host(
            "vm", "exec", vm_name, "--user", USER_ROOT, "--timeout", "30", "--",
            f"cp /usr/local/bin/mvm /home/{UNPRIVILEGED_USER}/mvm && "
            f"chown {UNPRIVILEGED_USER}:{UNPRIVILEGED_USER} "
            f"/home/{UNPRIVILEGED_USER}/mvm",
            timeout=15,
        )

        # Phase 12: Load KVM modules for nested VM testing
        _run_mvm_host(
            "vm", "exec", vm_name, "--user", USER_ROOT, "--timeout", "30", "--",
            "modprobe kvm_intel 2>/dev/null "
            "|| modprobe kvm_amd 2>/dev/null || true",
            check=False, timeout=15,
        )

        yield (vm_name, USER_ROOT, UNPRIVILEGED_USER)

    finally:
        if created_vm:
            try:
                _run_mvm_host("vm", "rm", vm_name, "--force", check=False, timeout=120)
            except subprocess.TimeoutExpired:
                pass
        if created_network:
            try:
                _run_mvm_host("network", "rm", net_name, "--force", check=False, timeout=60)
            except subprocess.TimeoutExpired:
                pass
        if created_key:
            try:
                _run_mvm_host("key", "rm", key_name, check=False)
            except subprocess.TimeoutExpired:
                pass

        # Clean up pulled kernel
        kernel_id = None
        try:
            ls_result = _run_mvm_host("kernel", "ls", "--json", check=False, timeout=10)
            if ls_result.returncode == 0 and ls_result.stdout.strip():
                kernels = json.loads(ls_result.stdout)
                for k in kernels:
                    if k.get("type") == "official" and "7.0.11" in str(k.get("version", "")):
                        kernel_id = k["id"][:6]
                        break
            if kernel_id:
                _run_mvm_host("kernel", "rm", kernel_id, "--force", check=False, timeout=30)
        except Exception:
            pass


# ---------------------------------------------------------------------------
# Guest-level SSH helper (from host)
# ---------------------------------------------------------------------------


def _guest_run(
    vm_name: str,
    guest_cmd: str,
    *,
    check: bool = True,
    timeout: int = 30,
    user: str = "root",
    retries: int = 3,
    retry_delay: float = 2.0,
) -> subprocess.CompletedProcess[str]:
    """Run a command INSIDE the fenv guest via host mvm vm exec (vsock), with retries."""
    import time as _time

    last_error: Exception | None = None
    for attempt in range(retries):
        try:
            return _run_mvm_host(
                "vm", "exec", vm_name,
                "--user", user,
                "--timeout", str(timeout),
                "--",
                guest_cmd,
                check=check, timeout=timeout,
            )
        except (RuntimeError, subprocess.TimeoutExpired) as e:
            last_error = e
            if isinstance(e, RuntimeError):
                if "exit code 255" not in str(e) and "exit code 2" not in str(e):
                    raise
            if attempt < retries - 1:
                _time.sleep(retry_delay)
                continue
            raise

    if last_error:
        raise last_error
    return _run_mvm_host(
        "vm", "exec", vm_name,
        "--user", user,
        "--timeout", str(timeout),
        "--",
        guest_cmd,
        check=check, timeout=timeout,
    )


# ========================================================================
# TestNestedIsolated
# ========================================================================


class TestNestedIsolated:
    """Run mvm commands inside a nested VM for fully isolated testing."""

    pytestmark = [
        pytest.mark.system,
        pytest.mark.domain_vm,
        pytest.mark.slow,
        pytest.mark.requires_kvm,
    ]

    def test_inside_guest_host_status(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Verify ``mvm host status --json`` inside the guest."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        result = _guest_run(vm_name, "mvm host status --json")
        status = json.loads(result.stdout)
        assert "kvm_accessible" in status
        assert "missing_binaries" in status
        if not status.get("kvm_accessible"):
            print("  [info] KVM not accessible inside guest")

    def test_inside_guest_config_roundtrip(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Config set/get/reset roundtrip inside the guest (as root)."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        _guest_run(vm_name, "mvm config set defaults.vm vcpu_count 4")
        get_result = _guest_run(
            vm_name, "mvm config get defaults.vm vcpu_count",
        )
        assert "4" in get_result.stdout

        _guest_run(vm_name, "mvm config reset defaults.vm vcpu_count")
        get_result2 = _guest_run(
            vm_name, "mvm config get defaults.vm vcpu_count",
        )
        assert "1" in get_result2.stdout

    def test_inside_guest_key_operations(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Key create/list/remove lifecycle inside the guest."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        key_name = f"test-key-{uuid.uuid4().hex[:6]}"
        try:
            _guest_run(
                vm_name,
                f"mvm key create {key_name} --algorithm ed25519 --force",
            )
            ls_result = _guest_run(vm_name, "mvm key ls --json")
            keys = json.loads(ls_result.stdout)
            assert any(k.get("name") == key_name for k in keys)
        finally:
            _guest_run(
                vm_name, f"mvm key rm {key_name}", check=False,
            )
        ls_result2 = _guest_run(vm_name, "mvm key ls --json")
        keys2 = json.loads(ls_result2.stdout)
        assert not any(k.get("name") == key_name for k in keys2)

    def test_inside_guest_volume_create_and_resize(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Volume create, list, resize, inspect, and remove inside the guest."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        vol_name = f"test-vol-{uuid.uuid4().hex[:6]}"
        try:
            _guest_run(vm_name, f"mvm vol create {vol_name} 512M")
            ls_result = _guest_run(vm_name, "mvm vol ls --json")
            vols = json.loads(ls_result.stdout)
            vol_entry = next(
                (v for v in vols if v.get("name") == vol_name), None
            )
            assert vol_entry is not None
            size_bytes = vol_entry.get("size_bytes", 0)
            assert size_bytes == 536870912

            _guest_run(vm_name, f"mvm vol resize {vol_name} 1G")
            inspect_result = _guest_run(
                vm_name, f"mvm vol inspect {vol_name} --json",
            )
            vol_info = json.loads(inspect_result.stdout)
            vol_data = vol_info.get("volume", vol_info)
            new_size = vol_data.get("size_bytes", 0)
            assert new_size == 1073741824
        finally:
            _guest_run(
                vm_name, f"mvm vol rm {vol_name} --force", check=False,
            )

    def test_inside_guest_network_operations(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Network create/list/remove inside the guest."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        net_name = f"test-net-{uuid.uuid4().hex[:6]}"
        try:
            create_result = _guest_run(
                vm_name,
                f"mvm network create {net_name} "
                "--subnet 10.200.0.0/24 --non-interactive",
                check=False, timeout=60,
            )
            assert create_result.returncode == 0, (
                "Guest network creation failed: "
                f"{create_result.stderr.strip()}"
            )

            ls_result = _guest_run(vm_name, "mvm network ls --json")
            nets = json.loads(ls_result.stdout)
            assert any(n.get("name") == net_name for n in nets)
        finally:
            _guest_run(
                vm_name, f"mvm network rm {net_name} --force", check=False,
            )

    def test_inside_guest_nested_vm_lifecycle(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Create a nested VM INSIDE the fenv guest (triple nesting)."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        nested_vm_name = f"nested-{uuid.uuid4().hex[:6]}"

        kvm_check = _guest_run(
            vm_name,
            "test -c /dev/kvm && echo KVM_OK || echo KVM_MISSING",
            check=False,
        )
        assert "KVM_OK" in kvm_check.stdout, (
            "/dev/kvm not available inside fenv guest -- "
            "cannot test nested VM creation"
        )

        nested_net = f"nestnet-{uuid.uuid4().hex[:6]}"
        try:
            img_pull = _guest_run(
                vm_name, "mvm image pull ubuntu:24.04",
                timeout=300, check=False,
            )
            assert img_pull.returncode == 0, (
                f"Image pull inside guest failed (exit "
                f"{img_pull.returncode}): "
                f"{img_pull.stderr.strip()[:300]}"
            )

            _guest_run(
                vm_name,
                "mvm kernel pull --type firecracker --version v1.15 --default",
                timeout=60,
            )

            _guest_run(
                vm_name,
                f"mvm network create {nested_net} "
                "--subnet 10.199.0.0/24 --non-interactive",
                timeout=60,
            )

            create_result = _guest_run(
                vm_name,
                f"mvm vm create {nested_vm_name} "
                f"--vcpu 1 --mem 256m "
                f"--image ubuntu:24.04 "
                f"--kernel firecracker "
                f"--network {nested_net} ",
                check=False, timeout=120,
            )
            assert create_result.returncode == 0, (
                f"Nested VM creation failed (exit "
                f"{create_result.returncode}): "
                f"{create_result.stderr.strip()[:1000]}"
            )

            ls_result = _guest_run(vm_name, "mvm vm ls --json")
            vms = json.loads(ls_result.stdout)
            assert any(v.get("name") == nested_vm_name for v in vms)
        finally:
            _guest_run(
                vm_name, f"mvm vm rm {nested_vm_name} --force", check=False,
            )
            _guest_run(
                vm_name, f"mvm network rm {nested_net} --force", check=False,
            )

    def test_inside_guest_unprivileged_read_only(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Read-only mvm commands work as an unprivileged user."""
        vm_name, _root_user, unpriv_user = fenv_vm
        mvm_for_user = f"/home/{unpriv_user}/mvm"

        ls_result = _guest_run(
            vm_name, f"{mvm_for_user} vm ls --json",
            check=False, user=unpriv_user,
        )
        assert ls_result.returncode == 0
        data = json.loads(ls_result.stdout)
        assert isinstance(data, list)

        host_result = _guest_run(
            vm_name, f"{mvm_for_user} host status --json",
            check=False, user=unpriv_user,
        )
        assert host_result.returncode == 0
        host_data = json.loads(host_result.stdout)
        assert "kvm_accessible" in host_data

        key_result = _guest_run(
            vm_name, f"{mvm_for_user} key ls --json",
            check=False, user=unpriv_user,
        )
        assert key_result.returncode == 0
        key_data = json.loads(key_result.stdout)
        assert isinstance(key_data, list)

    def test_inside_guest_host_reset(
        self, fenv_vm: tuple[str, str, str]
    ) -> None:
        """Test ``mvm host reset --force`` inside the isolated guest."""
        vm_name, _root_user, _unpriv_user = fenv_vm
        result = _guest_run(
            vm_name, "mvm host reset --force",
            check=False, timeout=120,
        )
        assert result.returncode in (0, 1), (
            f"host reset returned unexpected exit code {result.returncode}: "
            f"{result.stderr.strip()[:200]}"
        )
